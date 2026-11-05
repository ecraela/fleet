package store

import (
	"errors"
	"github.com/ecraela/fleet/internal/domain"
	"strings"
	"testing"
)

func appForTest(name, path string) domain.HTTPApplication {
	return domain.HTTPApplication{Name: name, Listener: "public", Hostname: "apps.example.test", PathPrefix: path, Algorithm: "roundrobin", Servers: []domain.ConfigurationServer{{Name: "web", Address: "backend.internal", Port: 8080, Weight: 100}}, HealthPath: "/healthz"}
}
func appInput(r *managedRolloutRig, spec domain.HTTPApplication, n int) domain.ApplicationInput {
	in := r.input()
	return domain.ApplicationInput{RequestID: domain.NewID(), ExpectedRevision: n, Spec: spec, Nodes: in.Nodes, Entrypoint: in.Entrypoint}
}
func finishApplication(r *managedRolloutRig, i int, op *domain.Operation) {
	d := &r.discoveries[i]
	servers := d.Servers[:0]
	for _, s := range d.Servers {
		if !strings.HasPrefix(s.Backend, domain.ApplicationBackendPrefix) {
			servers = append(servers, s)
		}
	}
	d.Servers = servers
	proxies := d.Proxies[:0]
	for _, p := range d.Proxies {
		if !strings.HasPrefix(p.Name, domain.ApplicationBackendPrefix) {
			proxies = append(proxies, p)
		}
	}
	d.Proxies = proxies
	for _, a := range op.Configuration.Applications {
		d.Proxies = append(d.Proxies, domain.Proxy{Name: a.BackendName(), Kind: "backend", Mode: "http", Algorithm: a.Algorithm, Status: "UP"})
		for _, s := range a.Servers {
			if a.Disabled {
				break
			}
			d.Servers = append(d.Servers, domain.BackendServer{Backend: a.BackendName(), Name: s.Name, State: "ready", Weight: s.Weight, InitialWeight: s.Weight, Health: "UP", CheckStatus: "L7OK"})
		}
	}
	r.rolloutRig.finish(i, op, true)
	d.Configuration.ApplicationControlVersion = 2
}
func applicationContract(t *testing.T, db Store) {
	r := newManagedRolloutRig(t, db)
	apps := db.(ApplicationStore)
	for i := range 2 {
		r.discoveries[i].Configuration.ApplicationControlVersion = 1
		for j := range r.discoveries[i].Servers {
			r.discoveries[i].Servers[j].CheckStatus = "* L7OK"
		}
	}
	r.refresh()
	input := appInput(r, appForTest("Store", "/shop"), 0)
	if _, e := apps.DeployApplication(r.ctx, domain.NewID(), r.cluster, "", "", input); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign deployment", e)
	}
	first, e := apps.DeployApplication(r.ctx, r.org, r.cluster, "", "", input)
	if e != nil {
		t.Fatal(e)
	}
	duplicate, e := apps.DeployApplication(r.ctx, r.org, r.cluster, "", "", input)
	if e != nil || duplicate.ID != first.ID {
		t.Fatal("idempotency", e)
	}
	input.Spec.Name = "different"
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, "", "", input); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("changed retry", e)
	}
	complete := func() {
		rows, e := db.(RolloutStore).ListRollouts(r.ctx, r.org, r.cluster)
		if e != nil {
			t.Fatal(e)
		}
		r.rollout = rows[0]
		for i := range 2 {
			finishApplication(r, i, r.next(i, false))
		}
		r.complete("succeeded")
	}
	complete()
	second, e := apps.DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, appForTest("Admin", "/shop/admin"), 0))
	if e != nil {
		t.Fatal("second app", e)
	}
	complete()
	if len(r.discoveries[0].Configuration.Document.Applications) != 2 {
		t.Fatal("shared listener lost an app")
	}
	conflict := appForTest("Overlap", "/shop")
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, conflict, 0)); !errors.Is(e, domain.ErrInvalidInput) {
		t.Fatal("duplicate route", e)
	}
	changed := first.Spec
	changed.PathPrefix = "/store"
	revision, e := apps.DeployApplication(r.ctx, r.org, r.cluster, first.ApplicationID, "", appInput(r, changed, 1))
	if e != nil {
		t.Fatal("edit", e)
	}
	complete()
	rollback, e := apps.DeployApplication(r.ctx, r.org, r.cluster, first.ApplicationID, first.ID, appInput(r, domain.HTTPApplication{}, 2))
	if e != nil {
		t.Fatal("rollback", e)
	}
	if rollback.Number != 3 || rollback.RollbackOf != first.ID {
		t.Fatal("rollback rewrote history")
	}
	complete()
	for i := range 2 {
		doc := r.discoveries[i].Configuration.Document
		found := false
		for _, a := range doc.Applications {
			found = found || a.Equal(second.Spec)
		}
		if !found {
			t.Fatal("rollback changed neighboring app")
		}
	}
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, first.ApplicationID, "", appInput(r, changed, 1)); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("stale revision", e)
	}
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, domain.NewID(), revision.ID, appInput(r, domain.HTTPApplication{}, 1)); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign app history", e)
	}
	d := r.input()
	d.Document = r.discoveries[0].Configuration.Document.Canonical()
	d.Document.Applications[0].PathPrefix = "/bypass"
	if _, e = db.(RolloutStore).CreateRollout(r.ctx, r.org, r.cluster, d); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("legacy rollout bypass", e)
	}
	if rows, e := apps.ListApplications(r.ctx, domain.NewID(), r.cluster); !errors.Is(e, domain.ErrNotFound) || len(rows) > 0 {
		t.Fatal("foreign app history exposed", e)
	}
}
func TestApplicationMemoryContract(t *testing.T)   { applicationContract(t, NewMemory()) }
func TestApplicationPostgresContract(t *testing.T) { applicationContract(t, postgresForTest(t)) }

func TestApplicationReconcilesItsKnownPartialRevision(t *testing.T) {
	db := NewMemory()
	r := newManagedRolloutRig(t, db)
	for i := range 2 {
		r.discoveries[i].Configuration.ApplicationControlVersion = 1
	}
	r.refresh()
	app, err := db.DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, appForTest("Partial", "/partial"), 0))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := db.ListRollouts(r.ctx, r.org, r.cluster)
	r.rollout = rows[0]
	finishApplication(r, 0, r.next(0, false))
	for range 50 {
		r.step()
		if r.rollout.Nodes[0].Status == "applied" {
			break
		}
	}
	// A failed peer prevents rollback from withdrawing the only serving node.
	r.discoveries[1].Servers[0].Health = "DOWN"
	for range 220 {
		r.step()
		if r.rollout.Terminal() {
			break
		}
	}
	if r.rollout.Status != "rollback_failed" {
		t.Fatal("expected bounded partial recovery", r.rollout.Status)
	}
	r.discoveries[1].Servers[0].Health = "UP"
	r.refresh()
	app, err = db.DeployApplication(r.ctx, r.org, r.cluster, app.ApplicationID, "", appInput(r, app.Spec, 1))
	if err != nil {
		t.Fatal("explicit partial reconciliation rejected", err)
	}
	if app.Number != 2 {
		t.Fatal("partial recovery changed revision identity")
	}
	rows, _ = db.ListRollouts(r.ctx, r.org, r.cluster)
	r.rollout = rows[0]
	// The first node already has the desired app and only needs maintenance;
	// the second node receives the missing application configuration.
	finishApplication(r, 1, r.next(1, false))
	r.complete("succeeded")
	for i := range 2 {
		if len(r.discoveries[i].Configuration.Document.Applications) != 1 {
			t.Fatal("partial application was not reconciled")
		}
	}
}
