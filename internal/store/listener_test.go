package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func tlsRig(t *testing.T, db Store) *managedRolloutRig {
	r := newManagedRolloutRig(t, db)
	for i := range 2 {
		c := r.discoveries[i].Configuration
		c.ApplicationControlVersion = 1
		c.ListenerControlVersion = 1
		c.ListenerBindings = []domain.ListenerBinding{{Name: "public", Address: "*", Port: 8443}}
		for j, ref := range []string{"first", "second"} {
			c.TLSCertificates = append(c.TLSCertificates, domain.TLSCertificate{Reference: ref, SHA256: strings.Repeat(string(rune('a'+j)), 64), DNSNames: []string{"apps.example.test"}, NotBeforeMS: r.now.Add(-time.Hour).UnixMilli(), NotAfterMS: r.now.Add(24 * time.Hour).UnixMilli()})
		}
	}
	r.refresh()
	return r
}
func tlsInput(r *managedRolloutRig, n int, ref int) domain.ListenerInput {
	in := r.input()
	c := r.discoveries[0].Configuration
	return domain.ListenerInput{RequestID: domain.NewID(), ExpectedRevision: n, Listeners: []domain.HTTPSListener{{ListenerBinding: c.ListenerBindings[0], CertificateRef: c.TLSCertificates[ref].Reference, CertificateSHA256: c.TLSCertificates[ref].SHA256}}, Nodes: in.Nodes, Entrypoint: in.Entrypoint}
}
func finishTLS(r *managedRolloutRig, i int, op *domain.Operation) {
	c := r.discoveries[i].Configuration
	c.TLSListeners = nil
	for _, l := range op.Configuration.Listeners {
		c.TLSListeners = append(c.TLSListeners, domain.TLSListenerObservation{Name: l.Name, SHA256: l.ExpectedCertificate(c.NodeID), Loaded: true})
	}
	finishApplication(r, i, op)
}
func completeTLS(r *managedRolloutRig) {
	r.t.Helper()
	rows, e := r.db.(RolloutStore).ListRollouts(r.ctx, r.org, r.cluster)
	if e != nil {
		r.t.Fatal(e)
	}
	r.rollout = rows[0]
	for i := range 2 {
		finishTLS(r, i, r.next(i, false))
	}
	r.complete("succeeded")
}
func listenerContract(t *testing.T, db Store) {
	r := tlsRig(t, db)
	ls := db.(ListenerStore)
	input := tlsInput(r, 0, 0)
	if _, e := ls.DeployListeners(r.ctx, domain.NewID(), r.cluster, "", input); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign write", e)
	}
	first, e := ls.DeployListeners(r.ctx, r.org, r.cluster, "", input)
	if e != nil {
		t.Fatal(e)
	}
	repeat, e := ls.DeployListeners(r.ctx, r.org, r.cluster, "", input)
	if e != nil || repeat.ID != first.ID {
		t.Fatal("retry created another rollout", e)
	}
	input.Listeners[0].Port++
	if _, e = ls.DeployListeners(r.ctx, r.org, r.cluster, "", input); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("changed retry", e)
	}
	completeTLS(r)
	second, e := ls.DeployListeners(r.ctx, r.org, r.cluster, "", tlsInput(r, 1, 1))
	if e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	// Application writes recompose TLS guards and retain the listener identity.
	app, e := db.(ApplicationStore).DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, appForTest("After TLS", "/new"), 0))
	if e != nil {
		t.Fatal("application after TLS", e)
	}
	completeTLS(r)
	restored, e := ls.DeployListeners(r.ctx, r.org, r.cluster, first.ID, tlsInput(r, 2, 0))
	if e != nil {
		t.Fatal("rollback", e)
	}
	if restored.RollbackOf != first.ID || restored.Number != 3 {
		t.Fatal("rollback mutated history")
	}
	completeTLS(r)
	for i := range 2 {
		doc := r.discoveries[i].Configuration.Document
		if len(doc.Applications) != 1 || !doc.Applications[0].Equal(app.Spec) || !domain.SameListeners(doc.Listeners, first.Listeners) {
			t.Fatal("rollback lost current app or certificate")
		}
	}
	rows, e := ls.ListListeners(r.ctx, r.org, r.cluster)
	if e != nil || len(rows) != 3 || rows[1].ID != second.ID || rows[0].Status != "succeeded" {
		t.Fatal("history", e, rows)
	}
	if _, e = ls.ListListeners(r.ctx, domain.NewID(), r.cluster); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign history", e)
	}
	if _, e = ls.DeployListeners(r.ctx, r.org, r.cluster, domain.NewID(), tlsInput(r, 3, 1)); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign rollback", e)
	}
	// A retired/expired alias cannot be restored even if its revision succeeded.
	for i := range 2 {
		r.discoveries[i].Configuration.TLSCertificates[1].NotAfterMS = r.now.Add(-time.Minute).UnixMilli()
	}
	r.refresh()
	if _, e = ls.DeployListeners(r.ctx, r.org, r.cluster, second.ID, tlsInput(r, 3, 1)); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("expired rollback", e)
	}
	// Generic writers cannot replace a listener or bypass its cluster history.
	generic := r.input()
	generic.Document = r.discoveries[0].Configuration.Document.Canonical()
	generic.Document.Listeners = nil
	if _, e = db.(RolloutStore).CreateRollout(r.ctx, r.org, r.cluster, generic); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("listener ownership bypass", e)
	}
	disable := tlsInput(r, 3, 0)
	disable.Listeners = []domain.HTTPSListener{}
	off, e := ls.DeployListeners(r.ctx, r.org, r.cluster, "", disable)
	if e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	if off.Listeners == nil || len(r.discoveries[0].Configuration.Document.Listeners) != 0 || len(r.discoveries[0].Configuration.Document.Applications) != 1 {
		t.Fatal("disable changed application")
	}
}
func TestListenerMemoryContract(t *testing.T)   { listenerContract(t, NewMemory()) }
func TestListenerPostgresContract(t *testing.T) { listenerContract(t, postgresForTest(t)) }

func TestTLSRolloutRollsBackBeforeRejoiningAnUnhealthyNode(t *testing.T) {
	db := NewMemory()
	r := tlsRig(t, db)
	_, err := db.DeployListeners(r.ctx, r.org, r.cluster, "", tlsInput(r, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := db.ListRollouts(r.ctx, r.org, r.cluster)
	r.rollout = rows[0]
	finishTLS(r, 0, r.next(0, false))
	r.discoveries[0].Servers[0].Health = "DOWN"
	op := r.next(0, true)
	if r.rollout.Nodes[1].ApplyOperationID != "" {
		t.Fatal("peer updated after failed first health window")
	}
	finishTLS(r, 0, op)
	r.complete("rolled_back")
	if len(r.discoveries[0].Configuration.Document.Listeners) != 0 {
		t.Fatal("automatic rollback retained failed TLS intent")
	}
}
func TestListenerAdmissionRejectsUnavailableOrMismatchedCertificates(t *testing.T) {
	for _, kind := range []string{"missing", "pin", "hostname", "grant", "capability", "expired", "stale"} {
		t.Run(kind, func(t *testing.T) {
			db := NewMemory()
			r := tlsRig(t, db)
			in := tlsInput(r, 0, 0)
			switch kind {
			case "missing":
				r.discoveries[1].Configuration.TLSCertificates = nil
			case "pin":
				r.discoveries[1].Configuration.TLSCertificates[0].SHA256 = strings.Repeat("c", 64)
			case "hostname":
				app := appForTest("Uncovered", "/")
				app.ID = domain.NewID()
				app.Hostname = "different.example.test"
				for i := range 2 {
					r.discoveries[i].Configuration.Document.Applications = []domain.HTTPApplication{app}
				}
			case "grant":
				in.Listeners[0].Port++
			case "capability":
				r.discoveries[1].Configuration.ListenerControlVersion = 0
			case "expired":
				r.discoveries[0].Configuration.TLSCertificates[0].NotAfterMS = r.now.Add(-time.Second).UnixMilli()
			case "stale":
				in.Nodes[0].ExpectedVersion++
			}
			r.refresh()
			if _, e := db.DeployListeners(r.ctx, r.org, r.cluster, "", in); !errors.Is(e, domain.ErrConflict) {
				t.Fatal("unsafe listener admitted", e)
			}
			rows, _ := db.ListListeners(r.ctx, r.org, r.cluster)
			rollouts, _ := db.ListRollouts(r.ctx, r.org, r.cluster)
			if len(rows) != 0 || len(rollouts) != 0 {
				t.Fatal("rejection leaked a write")
			}
		})
	}
}
