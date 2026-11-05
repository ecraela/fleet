package store

import (
	"github.com/ecraela/fleet/internal/domain"
	"testing"
	"time"
)

func TestACMERenewalWaitsForApplicationRollout(t *testing.T) {
	db := NewMemory()
	r := acmeRig(t, db)
	p, e := db.SetACME(r.ctx, r.org, r.cluster, acmeInput(r))
	if e != nil {
		t.Fatal(e)
	}
	acmeIssue(t, r, db, p)
	if e = db.AdvanceACME(r.ctx); e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	r.now = r.now.Add(4 * time.Second)
	r.refresh()
	if e = db.AdvanceACME(r.ctx); e != nil {
		t.Fatal(e)
	}
	p, _ = db.GetACME(r.ctx, r.org, r.cluster)
	r.now = time.UnixMilli(p.RenewAtMS + 1)
	r.refresh()
	rows, _ := db.ListApplications(r.ctx, r.org, r.cluster)
	spec := rows[0].Spec
	spec.Name = "Edited while renewal is due"
	if _, e = db.DeployApplication(r.ctx, r.org, r.cluster, rows[0].ApplicationID, "", appInput(r, spec, rows[0].Number)); e != nil {
		t.Fatal(e)
	}
	if e = db.AdvanceACME(r.ctx); e != nil {
		t.Fatal(e)
	}
	p, _ = db.GetACME(r.ctx, r.org, r.cluster)
	if p.Status != "active" || p.NextRunMS <= r.now.UnixMilli() {
		t.Fatal("app rollout cancelled renewal authority", p.Status, p.ErrorCode)
	}
	completeTLS(r)
	r.now = r.now.Add(31 * time.Second)
	r.refresh()
	if e = db.AdvanceACME(r.ctx); e != nil {
		t.Fatal(e)
	}
	p, _ = db.GetACME(r.ctx, r.org, r.cluster)
	if p.Status != "issuing" || p.Generation != 2 {
		t.Fatal("renewal did not resume", p.Status, p.ErrorCode)
	}
}

func TestFailedEnableRollsBackToDisabledWithoutRemovingTLS(t *testing.T) {
	db := NewMemory()
	r := tlsRig(t, db)
	if _, e := db.DeployListeners(r.ctx, r.org, r.cluster, "", tlsInput(r, 0, 0)); e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	a, e := db.DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, appForTest("Store", "/shop"), 0))
	if e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	disabled, e := db.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", lifecycleInput(r, a, "disable"))
	if e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	baseline := r.discoveries[0].Configuration.Document.Canonical()
	enabled, e := db.DeployApplication(r.ctx, r.org, r.cluster, a.ApplicationID, "", lifecycleInput(r, disabled, "enable"))
	if e != nil {
		t.Fatal(e)
	}
	rows, _ := db.ListRollouts(r.ctx, r.org, r.cluster)
	r.rollout = rows[0]
	finishTLS(r, 0, r.next(0, false))
	finishTLS(r, 1, r.next(1, false))
	for i := range r.discoveries[1].Servers {
		if r.discoveries[1].Servers[i].Backend == a.Spec.BackendName() {
			r.discoveries[1].Servers[i].Health = "DOWN"
		}
	}
	for range 35 {
		r.step()
		if r.rollout.Status == "rolling_back" {
			break
		}
	}
	if r.rollout.Status != "rolling_back" {
		t.Fatal("failed enable was not compensated", r.rollout.Status)
	}
	finishTLS(r, 1, r.next(1, true))
	finishTLS(r, 0, r.next(0, true))
	r.complete("rolled_back")
	for i := range 2 {
		if !r.discoveries[i].Configuration.Document.Equal(baseline) {
			t.Fatal("failed enable lost disabled baseline or shared TLS")
		}
	}
	apps, _ := db.ListApplications(r.ctx, r.org, r.cluster)
	for _, rev := range apps {
		if rev.ID == enabled.ID && domain.EffectiveApplication(rev, apps).Lifecycle() != "disabled" {
			t.Fatal("failed enable displayed active")
		}
	}
}
