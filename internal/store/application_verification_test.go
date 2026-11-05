package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

type verificationRig struct {
	*managedRolloutRig
	storeID        string
	fault          string
	faultNode      int
	rollbackFault  string
	entrypoint     bool
	sharedFault    bool
	finalNodeFault bool
}

func newVerificationRig(t *testing.T, db Store) *verificationRig {
	return newVerificationRigForPaths(t, db, false)
}

func newVerificationRigForPaths(t *testing.T, db Store, entrypoint bool) *verificationRig {
	r := tlsRig(t, db)
	if _, err := db.(ListenerStore).DeployListeners(r.ctx, r.org, r.cluster, "", tlsInput(r, 0, 0)); err != nil {
		t.Fatal(err)
	}
	completeTLS(r)
	v := &verificationRig{managedRolloutRig: r, entrypoint: entrypoint}
	for i := range 2 {
		d := &r.discoveries[i]
		d.Configuration.ApplicationControlVersion = 3
		for _, route := range []struct{ name, path string }{{"store", "/shop"}, {"admin", "/shop/admin"}} {
			for _, scheme := range []string{"http", "https"} {
				d.ApplicationProbeGrants = append(d.ApplicationProbeGrants, domain.ApplicationProbeGrant{Name: route.name + "-" + scheme, Scope: "node", Listener: "public", Hostname: "apps.example.test", PathPrefix: route.path, Path: route.path + "/health", Scheme: scheme, ExpectedStatus: 200, PolicyDigest: strings.Repeat("e", 64)})
			}
		}
		if entrypoint {
			d.Configuration.ApplicationControlVersion = 4
			for _, grant := range append([]domain.ApplicationProbeGrant{}, d.ApplicationProbeGrants...) {
				grant.Scope, grant.Name = "entrypoint", grant.Name+"-entrypoint"
				d.ApplicationProbeGrants = append(d.ApplicationProbeGrants, grant)
			}
		}
	}
	r.refresh()
	for _, a := range []domain.HTTPApplication{appForTest("Store", "/shop"), appForTest("Admin", "/shop/admin")} {
		a.Verification = &domain.ApplicationVerificationPolicy{HTTP: true, HTTPS: true, Entrypoint: entrypoint}
		revision, err := db.(ApplicationStore).DeployApplication(r.ctx, r.org, r.cluster, "", "", appInput(r, a, 0))
		if err != nil {
			t.Fatal(err)
		}
		if a.Name == "Store" {
			v.storeID = revision.ApplicationID
		}
		v.complete("succeeded")
	}
	return v
}

func (v *verificationRig) step() {
	for i := range 2 {
		d := &v.discoveries[i]
		d.Configuration.ApplicationControlVersion = 3
		if v.entrypoint {
			d.Configuration.ApplicationControlVersion = 4
		}
		d.ApplicationProbes = nil
		for _, a := range d.Configuration.Document.Applications {
			for _, g := range d.ApplicationProbeGrants {
				if a.Disabled || !g.Matches(a) {
					continue
				}
				bad := i == v.faultNode && a.ID == v.storeID && a.Servers[0].Address == "wrong.backend"
				if v.entrypoint {
					bad = bad && g.Scope == "entrypoint"
					if v.sharedFault && v.rollout.Status == "rolling_back" && a.ID == v.storeID && g.Scope == "entrypoint" {
						for _, peer := range v.discoveries[:2] {
							for _, app := range peer.Configuration.Document.Applications {
								bad = bad || (app.ID == v.storeID && app.Servers[0].Address == "wrong.backend")
							}
						}
					}
				}
				if v.fault == "after_rejoin" {
					m := maintenanceForRolloutNode(v.rollout, d.Configuration.NodeID)
					// The fixtures can omit NodeID; use the enrolled target identity.
					if m == nil {
						m = maintenanceForRolloutNode(v.rollout, v.nodes[i].ID)
					}
					bad = bad && m != nil && m.Status == "completed"
				}
				if bad && v.fault == "missing" {
					continue
				}
				p := domain.ApplicationProbe{ApplicationID: a.ID, SpecDigest: a.Digest(), Name: g.Name, Scheme: g.Scheme, Path: g.Path, ExpectedStatus: g.ExpectedStatus, StatusCode: 200, ObservedAtMS: v.now.Add(time.Second).UnixMilli(), Scope: g.Scope, PolicyDigest: g.PolicyDigest, ArtifactID: d.Configuration.ArtifactID, Generation: d.Generation}
				if g.Scheme == "https" {
					p.TLSVerified = true
					p.CertificateSHA256 = strings.Repeat("a", 64)
					p.CertificateNotAfterMS = v.now.Add(time.Hour).UnixMilli()
				}
				if bad && ((v.fault == "http" && g.Scheme == "http") || (v.fault == "after_rejoin" && g.Scheme == "http")) {
					p.StatusCode = 500
					p.ErrorCode = "response_status"
				}
				if bad && v.fault == "https" && g.Scheme == "https" {
					p.TLSVerified = false
					p.CertificateSHA256 = ""
					p.CertificateNotAfterMS = 0
					p.StatusCode = 0
					p.ErrorCode = "tls_failed"
				}
				if v.rollout.Status == "rolling_back" && a.ID == v.storeID && !bad && g.Scheme == "http" && (!v.entrypoint || g.Scope == "entrypoint") {
					n := v.rollout.Nodes[0]
					m := rolloutMaintenance(&n, true)
					if n.RollbackOperationID != "" && (v.rollbackFault == "before_rejoin" || (v.rollbackFault == "after_rejoin" && m != nil && m.Status == "completed")) {
						p.ErrorCode, p.StatusCode = "response_status", 503
					}
				}
				if v.finalNodeFault && v.rollout.Status == "rolling_back" && v.rollout.Index < 0 && i == 0 && a.ID == v.storeID && g.Scope == "node" {
					p.ErrorCode, p.StatusCode = "response_status", 503
				}
				d.ApplicationProbes = append(d.ApplicationProbes, p)
			}
		}
	}
	v.managedRolloutRig.step()
	for i := range 2 {
		if op := v.commands[i]; op != nil {
			delete(v.commands, i)
			finishTLS(v.managedRolloutRig, i, op)
			v.discoveries[i].Configuration.ApplicationControlVersion = 3
			if v.entrypoint {
				v.discoveries[i].Configuration.ApplicationControlVersion = 4
			}
		}
	}
}

func (v *verificationRig) complete(expected string) {
	rows, err := v.db.(RolloutStore).ListRollouts(v.ctx, v.org, v.cluster)
	if err != nil {
		v.t.Fatal(err)
	}
	v.rollout = rows[0]
	for range 250 {
		v.step()
		if v.rollout.Terminal() {
			break
		}
	}
	if v.rollout.Status != expected {
		for i, n := range v.rollout.Nodes {
			v.t.Logf("node %d gate %+v", i, n.ApplicationVerification)
		}
		v.t.Fatalf("expected %s, got %s: %s; nodes %+v", expected, v.rollout.Status, v.rollout.ErrorCode, v.rollout.Nodes)
	}
}

func applicationVerificationContract(t *testing.T, db Store) {
	v := newVerificationRig(t, db)
	for _, fault := range []string{"http", "https", "missing", "after_rejoin"} {
		t.Run(fault, func(t *testing.T) {
			baseline := v.discoveries[0].Configuration.Document.Canonical()
			rows, _ := db.(ApplicationStore).ListApplications(v.ctx, v.org, v.cluster)
			var latest domain.ApplicationRevision
			for _, r := range rows {
				if r.ApplicationID == v.storeID && r.Number > latest.Number {
					latest = r
				}
			}
			a := domain.EffectiveApplication(latest, rows).Spec.Canonical()
			a.Servers[0].Address = "wrong.backend"
			v.fault = fault
			input := appInput(v.managedRolloutRig, a, latest.Number)
			if _, err := db.(ApplicationStore).DeployApplication(v.ctx, domain.NewID(), v.cluster, v.storeID, "", input); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("cross-tenant verification write", err)
			}
			revision, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", input)
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", input)
			if err != nil || duplicate.ID != revision.ID {
				t.Fatal("verification duplicated on retry", err)
			}
			v.complete("rolled_back")
			code := "application_" + fault + "_failed"
			if fault == "missing" {
				code = "application_verification_timeout"
			}
			if fault == "after_rejoin" {
				code = "application_http_failed"
			}
			if v.rollout.ErrorCode != code || v.rollout.Nodes[1].ApplyOperationID != "" {
				t.Fatal("failed app advanced the second node", v.rollout.ErrorCode, v.rollout.Nodes[1])
			}
			gate := v.rollout.Nodes[0].ApplicationVerification
			if gate == nil || gate.Status != "failed" || (fault == "after_rejoin" && gate.Phase != "after_rejoin") {
				t.Fatal("failure evidence lost", gate)
			}
			rollback := v.rollout.Nodes[0].RollbackApplicationVerification
			if rollback == nil || rollback.Status != "passed" || rollback.Phase != "after_rejoin" {
				t.Fatal("restoration skipped request verification", rollback)
			}
			if fault != "after_rejoin" && v.rollout.Nodes[0].RollbackMaintenance != nil {
				t.Fatal("restored node was withdrawn again while waiting for its post-rejoin probes")
			}
			for i := range 2 {
				if !v.discoveries[i].Configuration.Document.Equal(baseline) {
					t.Fatal("rollback changed neighbor, TLS or unmanaged settings")
				}
			}
		})
	}
	v.fault, v.faultNode = "http", 1
	rowsForFailure, _ := db.(ApplicationStore).ListApplications(v.ctx, v.org, v.cluster)
	var previous domain.ApplicationRevision
	for _, r := range rowsForFailure {
		if r.ApplicationID == v.storeID && r.Number > previous.Number {
			previous = r
		}
	}
	bad := domain.EffectiveApplication(previous, rowsForFailure).Spec.Canonical()
	bad.Servers[0].Address = "wrong.backend"
	if _, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", appInput(v.managedRolloutRig, bad, previous.Number)); err != nil {
		t.Fatal(err)
	}
	v.complete("rolled_back")
	for _, n := range v.rollout.Nodes {
		if n.Status != "rolled_back" || n.RollbackApplicationVerification == nil || n.RollbackApplicationVerification.Status != "passed" {
			t.Fatal("second-node failure did not restore and verify both nodes", n)
		}
	}
	rows, _ := db.(ApplicationStore).ListApplications(v.ctx, v.org, v.cluster)
	latest := rows[0]
	for _, r := range rows {
		if r.ApplicationID == v.storeID && r.Number > latest.Number {
			latest = r
		}
	}
	a := domain.EffectiveApplication(latest, rows).Spec.Canonical()
	a.Name = "Store verified"
	input := appInput(v.managedRolloutRig, a, latest.Number)
	v.discoveries[0].Configuration.ApplicationControlVersion = 2
	v.refresh()
	if _, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("legacy agent accepted required verification", err)
	}
	v.discoveries[0].Configuration.ApplicationControlVersion = 3
	saved := v.discoveries[0].ApplicationProbeGrants
	v.discoveries[0].ApplicationProbeGrants = nil
	v.refresh()
	input = appInput(v.managedRolloutRig, a, latest.Number)
	if _, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("missing local checks accepted", err)
	}
	v.discoveries[0].ApplicationProbeGrants = saved
	v.refresh()
}

func TestApplicationVerificationMemory(t *testing.T) { applicationVerificationContract(t, NewMemory()) }
func TestApplicationVerificationPostgres(t *testing.T) {
	applicationVerificationContract(t, postgresForTest(t))
}

func applicationVerificationRecoveryContract(t *testing.T, db Store) {
	v := newVerificationRig(t, db)
	baseline := v.discoveries[1].Configuration.Document.Canonical()
	rows, _ := db.(ApplicationStore).ListApplications(v.ctx, v.org, v.cluster)
	var latest domain.ApplicationRevision
	for _, r := range rows {
		if r.ApplicationID == v.storeID && r.Number > latest.Number {
			latest = r
		}
	}
	a := latest.Spec.Canonical()
	a.Servers[0].Address = "wrong.backend"
	v.fault, v.rollbackFault = "http", t.Name()[strings.LastIndex(t.Name(), "/")+1:]
	if _, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", appInput(v.managedRolloutRig, a, latest.Number)); err != nil {
		t.Fatal(err)
	}
	v.complete("rollback_failed")
	n := v.rollout.Nodes[0]
	if n.RecoveryMaintenanceID == "" || n.RollbackApplicationVerification.Status != "failed" || v.rollout.Nodes[1].ApplyOperationID != "" {
		t.Fatal("unverified recovery was released", n)
	}
	m := rolloutMaintenance(&n, true)
	if m.Status != "attention" || !v.discoveries[1].Configuration.Document.Equal(baseline) {
		t.Fatal("failed recovery did not preserve withdrawal and serving peer", m)
	}
}

func TestApplicationVerificationRecoveryMemory(t *testing.T) {
	for _, phase := range []string{"before_rejoin", "after_rejoin"} {
		t.Run(phase, func(t *testing.T) { applicationVerificationRecoveryContract(t, NewMemory()) })
	}
}
func TestApplicationVerificationRecoveryPostgres(t *testing.T) {
	for _, phase := range []string{"before_rejoin", "after_rejoin"} {
		t.Run(phase, func(t *testing.T) { applicationVerificationRecoveryContract(t, postgresForTest(t)) })
	}
}

func TestPostgresApplicationCheckAuthorityImmutable(t *testing.T) {
	p := postgresForTest(t)
	v := newVerificationRig(t, p)
	for _, path := range []string{"application_checks", "rollback_application_checks"} {
		if _, err := p.pool.Exec(t.Context(), `UPDATE hm_rollouts SET payload=jsonb_set(payload,ARRAY['nodes','0',$2,'0','policy_digest'],to_jsonb($3::text)) WHERE id=$1`, v.rollout.ID, path, strings.Repeat("f", 64)); err == nil {
			t.Fatal("frozen authority was mutable", path)
		}
	}
	for _, policy := range []string{`null`, `{}`, `{"http":true}`, `{"http":false,"https":false}`, `{"http":true,"https":false,"entrypoint":null}`, `{"http":true,"https":false,"entrypoint":"yes"}`} {
		_, err := p.pool.Exec(t.Context(), `INSERT INTO hm_application_revisions(id,organization_id,cluster_id,application_id,number,request_id,request_digest,rollout_id,created_at,payload)
SELECT $2::uuid,organization_id,cluster_id,application_id,63,$3::uuid,request_digest,rollout_id,created_at,
payload || jsonb_build_object('id',$2::uuid::text,'number',63,'request_id',$3::uuid::text,'spec',payload->'spec' || jsonb_build_object('verification',$4::jsonb))
FROM hm_application_revisions WHERE application_id=$1 LIMIT 1`, v.storeID, domain.NewID(), domain.NewID(), policy)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.ConstraintName != "hm_application_verification_shape" {
			t.Fatal("invalid persisted policy escaped shape constraint", policy, err)
		}
	}
}
