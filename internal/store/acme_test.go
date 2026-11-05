package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func acmeRig(t *testing.T, db Store) *managedRolloutRig {
	r := tlsRig(t, db)
	for i := range 2 {
		c := r.discoveries[i].Configuration
		c.NodeID = r.nodes[i].ID
		c.ACMEGrants = []domain.ACMEGrant{{Issuer: "local-ca", Listener: "public", Environment: "staging", Challenges: []domain.ACMEChallenge{{Hostname: "apps.example.test", Target: "app.validation.test"}}, PolicyDigest: strings.Repeat("a", 64)}}
	}
	r.refresh()
	if _, e := db.(ApplicationStore).DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, appForTest("Store", "/shop"), 0)); e != nil {
		t.Fatal(e)
	}
	completeTLS(r)
	return r
}
func acmeInput(r *managedRolloutRig) domain.ACMEInput {
	return domain.ACMEInput{RequestID: domain.NewID(), Action: "enable", Issuer: "local-ca", Binding: r.discoveries[0].Configuration.ListenerBindings[0]}
}
func acmeIssue(t *testing.T, r *managedRolloutRig, db ACMEStore, plan domain.ACMEPlan) {
	t.Helper()
	for i := range 2 {
		job, e := db.NodeACME(r.ctx, r.org, r.cluster, r.nodes[i].ID)
		if e != nil || job == nil || job.NodeID != r.nodes[i].ID {
			t.Fatal("node job", e, job)
		}
		if e = db.ReportACME(r.ctx, r.org, r.cluster, r.nodes[1-i].ID, domain.ACMEResult{JobID: job.ID, ErrorCode: "dns_update_failed"}); !errors.Is(e, domain.ErrNotFound) {
			t.Fatal("foreign node reported", e)
		}
		c := domain.TLSCertificate{Reference: job.Reference, SHA256: strings.Repeat(string(rune('c'+i)), 64), DNSNames: job.Hostnames, NotBeforeMS: r.now.Add(-time.Minute).UnixMilli(), NotAfterMS: r.now.Add(time.Hour).UnixMilli()}
		wrong := c
		wrong.DNSNames = []string{"wrong.test"}
		if e = db.ReportACME(r.ctx, r.org, r.cluster, r.nodes[i].ID, domain.ACMEResult{JobID: job.ID, Certificate: &wrong}); !errors.Is(e, domain.ErrInvalidInput) {
			t.Fatal("wrong SAN accepted", e)
		}
		result := domain.ACMEResult{JobID: job.ID, Certificate: &c}
		if e = db.ReportACME(r.ctx, r.org, r.cluster, r.nodes[i].ID, result); e != nil {
			t.Fatal(e)
		}
		if e = db.ReportACME(r.ctx, r.org, r.cluster, r.nodes[i].ID, result); e != nil {
			t.Fatal("result retry", e)
		}
		r.discoveries[i].Configuration.TLSCertificates = append(r.discoveries[i].Configuration.TLSCertificates, c)
	}
	r.refresh()
}
func acmeContract(t *testing.T, db Store) {
	r := acmeRig(t, db)
	ca := db.(ACMEStore)
	in := acmeInput(r)
	if _, e := ca.SetACME(r.ctx, domain.NewID(), r.cluster, in); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign setup", e)
	}
	p, e := ca.SetACME(r.ctx, r.org, r.cluster, in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ca.GetACME(r.ctx, domain.NewID(), r.cluster); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign read", e)
	}
	same, e := ca.SetACME(r.ctx, r.org, r.cluster, in)
	if e != nil || same.ID != p.ID || same.Generation != 1 {
		t.Fatal("idempotent setup", e)
	}
	bad := in
	bad.Issuer = "other"
	if _, e = ca.SetACME(r.ctx, r.org, r.cluster, bad); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("changed retry", e)
	}
	acmeIssue(t, r, ca, p)
	if e = ca.AdvanceACME(r.ctx); e != nil {
		t.Fatal(e)
	}
	p, e = ca.GetACME(r.ctx, r.org, r.cluster)
	if e != nil || p.Status != "deploying" || p.Activation == nil || len(p.Activation.Listeners[0].NodeCertificates) != 2 {
		t.Fatal("activation", e, p.Status)
	}
	id := p.RolloutID
	if e = ca.AdvanceACME(r.ctx); e != nil {
		t.Fatal(e)
	}
	p, _ = ca.GetACME(r.ctx, r.org, r.cluster)
	if p.RolloutID != id {
		t.Fatal("activation repeated")
	}
	completeTLS(r)
	r.now = r.now.Add(4 * time.Second)
	r.refresh()
	if e = ca.AdvanceACME(r.ctx); e != nil {
		t.Fatal(e)
	}
	p, _ = ca.GetACME(r.ctx, r.org, r.cluster)
	if p.Status != "active" || len(p.History) != 1 || p.RenewAtMS <= r.now.UnixMilli() || p.RenewAtMS >= r.now.Add(time.Hour).UnixMilli() {
		t.Fatal("renewal schedule", p.Status, p.RenewAtMS)
	}
	for i := range 2 {
		if !r.discoveries[i].Configuration.Document.Serving(r.discoveries[i]) {
			t.Fatal("node-local certificate not serving")
		}
	}
	// Advance the durable scheduler, with no browser session or manual renew call.
	r.now = time.UnixMilli(p.RenewAtMS + 1)
	r.discoveries[0].Configuration.ControlAllowed = false
	r.refresh()
	if e = ca.AdvanceACME(r.ctx); e != nil {
		t.Fatal(e)
	}
	deferred, _ := ca.GetACME(r.ctx, r.org, r.cluster)
	if deferred.Status != "active" || deferred.NextRunMS <= r.now.UnixMilli() {
		t.Fatal("temporary node outage disabled renewal", deferred.Status)
	}
	r.discoveries[0].Configuration.ControlAllowed = true
	r.now = r.now.Add(31 * time.Second)
	r.refresh()
	if e = ca.AdvanceACME(r.ctx); e != nil {
		t.Fatal(e)
	}
	p, _ = ca.GetACME(r.ctx, r.org, r.cluster)
	if p.Generation != 2 || p.Status != "issuing" {
		t.Fatal("automatic renewal", p.Status, p.Generation)
	}
	job, e := ca.NodeACME(r.ctx, r.org, r.cluster, r.nodes[0].ID)
	if e != nil || job == nil {
		t.Fatal(e)
	}
	if e = ca.ReportACME(r.ctx, r.org, r.cluster, r.nodes[0].ID, domain.ACMEResult{JobID: job.ID, ErrorCode: "acme_rate_limited", RetryAfterSeconds: 3600}); e != nil {
		t.Fatal(e)
	}
	p, _ = ca.GetACME(r.ctx, r.org, r.cluster)
	if p.Jobs[0].NextTryMS < r.now.Add(time.Hour).UnixMilli() {
		t.Fatal("rate limit lost")
	}
	if current := r.discoveries[0].Configuration.Document.Listeners[0]; current.CertificateRef != p.History[0].Reference {
		t.Fatal("failed issuance changed traffic")
	}
	if e = ca.AdvanceACME(r.ctx); e != nil {
		t.Fatal(e)
	}
	limited, _ := ca.GetACME(r.ctx, r.org, r.cluster)
	if limited.Jobs[0].NextTryMS != p.Jobs[0].NextTryMS {
		t.Fatal("authority retry delay was shortened")
	}
	disabled, e := ca.SetACME(r.ctx, r.org, r.cluster, domain.ACMEInput{RequestID: domain.NewID(), ExpectedVersion: p.Version, Action: "disable"})
	if e != nil || disabled.Enabled {
		t.Fatal("disable", e)
	}
	if job, e = ca.NodeACME(r.ctx, r.org, r.cluster, r.nodes[1].ID); e != nil || job != nil {
		t.Fatal("disabled issuer still authorized", e)
	}
}
func TestACMEMemoryContract(t *testing.T)   { acmeContract(t, NewMemory()) }
func TestACMEPostgresContract(t *testing.T) { acmeContract(t, postgresForTest(t)) }

func acmeTemporaryFailureContract(t *testing.T, db Store) {
	r := acmeRig(t, db)
	ca := db.(ACMEStore)
	plan, err := ca.SetACME(r.ctx, r.org, r.cluster, acmeInput(r))
	if err != nil {
		t.Fatal(err)
	}
	var jobID, reference string
	for i := range 7 {
		r.refresh()
		job, err := ca.NodeACME(r.ctx, r.org, r.cluster, r.nodes[0].ID)
		if err != nil || job == nil {
			t.Fatal("automatic retry stopped", i, err)
		}
		if i == 0 {
			jobID, reference = job.ID, job.Reference
		} else if job.ID != jobID || job.Reference != reference {
			t.Fatal("retry replaced generation or key reference")
		}
		if err = ca.ReportACME(r.ctx, r.org, r.cluster, r.nodes[0].ID, domain.ACMEResult{JobID: job.ID, ErrorCode: "acme_unavailable"}); err != nil {
			t.Fatal(err)
		}
		current, _ := ca.GetACME(r.ctx, r.org, r.cluster)
		if current.Status != "issuing" || current.Generation != plan.Generation || current.Jobs[0].Status != "pending" {
			t.Fatal("temporary failure became terminal", current.Status, current.Jobs[0].Status)
		}
		if early, err := ca.NodeACME(r.ctx, r.org, r.cluster, r.nodes[0].ID); err != nil || early != nil {
			t.Fatal("backoff bypassed", err)
		}
		r.now = time.UnixMilli(current.Jobs[0].NextTryMS + 1)
	}
	r.refresh()
	job, err := ca.NodeACME(r.ctx, r.org, r.cluster, r.nodes[0].ID)
	if err != nil || job == nil || job.Attempts != 8 {
		t.Fatal("retry did not resume after long outage", err)
	}
	cert := domain.TLSCertificate{Reference: reference, SHA256: strings.Repeat("c", 64), DNSNames: job.Hostnames, NotBeforeMS: r.now.Add(-time.Minute).UnixMilli(), NotAfterMS: r.now.Add(time.Hour).UnixMilli()}
	if err = ca.ReportACME(r.ctx, r.org, r.cluster, r.nodes[0].ID, domain.ACMEResult{JobID: job.ID, Certificate: &cert}); err != nil {
		t.Fatal("recovery failed", err)
	}
}

func TestACMETemporaryFailureMemory(t *testing.T) { acmeTemporaryFailureContract(t, NewMemory()) }
func TestACMETemporaryFailurePostgres(t *testing.T) {
	acmeTemporaryFailureContract(t, postgresForTest(t))
}

func acmeLegacyRetryContract(t *testing.T, db Store) {
	r := acmeRig(t, db)
	ca := db.(ACMEStore)
	plan, err := ca.SetACME(r.ctx, r.org, r.cluster, acmeInput(r))
	if err != nil {
		t.Fatal(err)
	}
	retryAt := r.now.Add(time.Hour).UnixMilli()
	err = db.(rolloutPersistence).rolloutTransaction(r.ctx, r.org, r.cluster, func(tx *rolloutTransaction) error {
		p, e := tx.acmePlan()
		if e != nil {
			return e
		}
		p.Status = "error"
		p.ErrorCode = "acme_unavailable"
		p.NextRunMS = 0
		p.Jobs[0].Status = "failed"
		p.Jobs[0].ErrorCode = "acme_unavailable"
		p.Jobs[0].Attempts = 5
		p.Jobs[0].NextTryMS = retryAt
		return tx.saveACME(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = ca.AdvanceACME(r.ctx); err != nil {
		t.Fatal(err)
	}
	current, err := ca.GetACME(r.ctx, r.org, r.cluster)
	if err != nil || current.Status != "issuing" || current.Generation != plan.Generation || current.Jobs[0].ID != plan.Jobs[0].ID || current.Jobs[0].NextTryMS != retryAt {
		t.Fatal("legacy failure not recovered safely", err, current.Status)
	}
	if job, err := ca.NodeACME(r.ctx, r.org, r.cluster, r.nodes[0].ID); err != nil || job != nil {
		t.Fatal("legacy recovery bypassed backoff", err)
	}
	r.now = r.now.Add(4 * time.Second)
	r.refresh()
	if err = ca.AdvanceACME(r.ctx); err != nil {
		t.Fatal(err)
	}
	current, _ = ca.GetACME(r.ctx, r.org, r.cluster)
	if current.Jobs[0].NextTryMS != r.now.Add(5*time.Minute).UnixMilli() {
		t.Fatal("legacy outage backoff was not bounded", current.Jobs[0].NextTryMS)
	}
}
func TestACMELegacyRetryMemory(t *testing.T)   { acmeLegacyRetryContract(t, NewMemory()) }
func TestACMELegacyRetryPostgres(t *testing.T) { acmeLegacyRetryContract(t, postgresForTest(t)) }
