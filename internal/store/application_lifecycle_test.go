package store

import (
	"errors"
	"github.com/ecraela/fleet/internal/domain"
	"testing"
)

func lifecycleInput(r *managedRolloutRig, revision domain.ApplicationRevision, action string) domain.ApplicationInput {
	in := appInput(r, domain.HTTPApplication{}, revision.Number)
	in.Action = action
	return in
}
func lifecycleContract(t *testing.T, db Store) {
	r := tlsRig(t, db)
	apps := db.(ApplicationStore)
	if _, e := db.(ListenerStore).DeployListeners(r.ctx, r.org, r.cluster, "", tlsInput(r, 0, 0)); e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	a, e := apps.DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, appForTest("Store", "/shop"), 0))
	if e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	b, e := apps.DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, appForTest("Admin", "/shop/admin"), 0))
	if e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	baseline := r.discoveries[0].Configuration.Document.Canonical()
	check := func(present, disabled bool) {
		t.Helper()
		for i := range 2 {
			doc := r.discoveries[i].Configuration.Document
			if !domain.SameListeners(doc.Listeners, baseline.Listeners) || !(domain.ConfigurationDocument{Backends: doc.Backends}).Equal(domain.ConfigurationDocument{Backends: baseline.Backends}) {
				t.Fatal("shared TLS or unmanaged backend changed")
			}
			found, neighbor := false, false
			for _, v := range doc.Applications {
				if v.ID == a.ApplicationID {
					found = true
					if v.Disabled != disabled {
						t.Fatal("wrong lifecycle")
					}
				}
				neighbor = neighbor || v.Equal(b.Spec)
			}
			if found != present || !neighbor || !doc.Serving(r.discoveries[i]) {
				t.Fatal("application lifecycle changed neighbor or loaded state")
			}
		}
	}
	in := lifecycleInput(r, a, "disable")
	if _, e = apps.DeployApplication(r.ctx, domain.NewID(), r.cluster, a.ApplicationID, "", in); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign tenant lifecycle", e)
	}
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, domain.NewID(), "", in); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign application lifecycle", e)
	}
	r.discoveries[0].Configuration.ApplicationControlVersion = 1
	r.refresh()
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", in); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("old agent accepted lifecycle", e)
	}
	r.discoveries[0].Configuration.ApplicationControlVersion = 2
	r.refresh()
	disabled, e := apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", in)
	if e != nil {
		t.Fatal(e)
	}
	same, e := apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", in)
	if e != nil || same.ID != disabled.ID {
		t.Fatal("lifecycle retry", e)
	}
	changed := in
	changed.Action = "delete"
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", changed); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("changed replay", e)
	}
	completeTLS(r)
	check(true, true)
	rows, _ := apps.ListApplications(r.ctx, r.org, r.cluster)
	controls := map[string]domain.NodeControl{}
	for _, node := range r.nodes {
		c, _, err := db.(ControlStore).GetControl(r.ctx, r.org, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		controls[node.ID] = c
	}
	for _, h := range domain.ApplicationHealthAt(rows, r.nodes[:2], controls, r.now) {
		if h.ApplicationID == a.ApplicationID && h.Reachability != "disabled" {
			t.Fatal("disabled is not unhealthy", h)
		}
	}
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", appInput(r, a.Spec, disabled.Number)); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("edit bypassed enable", e)
	}
	enabled, e := apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", lifecycleInput(r, disabled, "enable"))
	if e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	check(true, false)
	deleted, e := apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", lifecycleInput(r, enabled, "delete"))
	if e != nil || !deleted.Deleted {
		t.Fatal("delete", e)
	}
	completeTLS(r)
	check(false, false)
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", appInput(r, a.Spec, deleted.Number)); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("stale editor restored deletion", e)
	}
	restored, e := apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", lifecycleInput(r, deleted, "restore"))
	if e != nil {
		t.Fatal("restore", e)
	}
	completeTLS(r)
	check(true, false)
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", lifecycleInput(r, enabled, "disable")); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("stale lifecycle", e)
	}
	deleted, e = apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", lifecycleInput(r, restored, "delete"))
	if e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	replacement, e := apps.DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, appForTest("Replacement", "/shop"), 0))
	if e != nil {
		t.Fatal("deleted route not released", e)
	}
	completeTLS(r)
	if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", lifecycleInput(r, deleted, "restore")); !errors.Is(e, domain.ErrInvalidInput) {
		t.Fatal("restore stole another route", e)
	}
	for _, rev := range []domain.ApplicationRevision{replacement, b} {
		if _, e = apps.DeployApplication(r.ctx, r.org, r.cluster, rev.ApplicationID, "", lifecycleInput(r, rev, "delete")); e != nil {
			t.Fatal(e)
		}
		completeTLS(r)
	}
	for i := range 2 {
		doc := r.discoveries[i].Configuration.Document
		if len(doc.Applications) != 0 || !domain.SameListeners(doc.Listeners, baseline.Listeners) || !doc.Serving(r.discoveries[i]) {
			t.Fatal("last app deletion removed shared listener")
		}
	}
}
func TestApplicationLifecycleMemory(t *testing.T)   { lifecycleContract(t, NewMemory()) }
func TestApplicationLifecyclePostgres(t *testing.T) { lifecycleContract(t, postgresForTest(t)) }
