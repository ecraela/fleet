package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ecraela/fleet/internal/domain"
)

func latestVerifiedApplication(v *verificationRig) domain.ApplicationRevision {
	rows, err := v.db.(ApplicationStore).ListApplications(v.ctx, v.org, v.cluster)
	if err != nil {
		v.t.Fatal(err)
	}
	var latest domain.ApplicationRevision
	for _, row := range rows {
		if row.ApplicationID == v.storeID && row.Number > latest.Number {
			latest = row
		}
	}
	// The next write fences on the latest attempt, but edits its effective spec.
	latest.Spec = domain.EffectiveApplication(latest, rows).Spec
	return latest
}

func entrypointVerificationContract(t *testing.T, db Store) {
	v := newVerificationRigForPaths(t, db, true)
	for _, fault := range []string{"http", "https", "missing"} {
		t.Run(fault, func(t *testing.T) {
			baseline := v.discoveries[0].Configuration.Document.Canonical()
			previous := latestVerifiedApplication(v)
			spec := previous.Spec.Canonical()
			spec.Servers[0].Address = "wrong.backend"
			v.fault = fault
			input := appInput(v.managedRolloutRig, spec, previous.Number)
			if _, err := db.(ApplicationStore).DeployApplication(v.ctx, domain.NewID(), v.cluster, v.storeID, "", input); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("foreign entrypoint verification write", err)
			}
			revision, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", input)
			if err != nil {
				t.Fatal(err)
			}
			if duplicate, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", input); err != nil || duplicate.ID != revision.ID {
				t.Fatal("retry changed frozen authority", err)
			}
			v.complete("rolled_back")
			code := "application_entrypoint_" + fault + "_failed"
			if fault == "missing" {
				code = "application_entrypoint_verification_timeout"
			}
			if v.rollout.ErrorCode != code || v.rollout.Nodes[1].ApplyOperationID != "" {
				t.Fatal("entrypoint failure advanced the peer", v.rollout.ErrorCode)
			}
			n := v.rollout.Nodes[0]
			gate := n.ApplicationVerification
			if gate == nil || gate.Phase != "after_rejoin" || gate.Status != "failed" || n.RollbackMaintenance == nil {
				t.Fatal("entrypoint failure did not withdraw the rejoined node for rollback", n)
			}
			for _, result := range gate.Checks {
				if result.Scope == "node" && result.ErrorCode != "" {
					t.Fatal("test did not isolate the shared entrypoint path", result)
				}
			}
			rollback := n.RollbackApplicationVerification
			if rollback == nil || rollback.Status != "passed" || rollback.Phase != "after_cluster_restore" || len(rollback.Checks) != 8 {
				t.Fatal("baseline or neighboring app skipped path verification", rollback)
			}
			for _, c := range v.discoveries[:2] {
				if !c.Configuration.Document.Equal(baseline) {
					t.Fatal("rollback changed the neighbor or shared TLS")
				}
			}
		})
	}
}

func TestEntrypointVerificationMemory(t *testing.T) {
	entrypointVerificationContract(t, NewMemory())
}
func TestEntrypointVerificationPostgres(t *testing.T) {
	entrypointVerificationContract(t, postgresForTest(t))
}

func entrypointVerificationAuthorityContract(t *testing.T, db Store) {
	v := newVerificationRigForPaths(t, db, true)
	previous := latestVerifiedApplication(v)
	spec := previous.Spec.Canonical()
	spec.Name = "Store entrypoint verified"
	for _, fault := range []string{"old_agent", "missing_grant", "rollback_authority"} {
		t.Run(fault, func(t *testing.T) {
			saved := v.discoveries[0].ApplicationProbeGrants
			if fault == "missing_grant" {
				v.discoveries[0].ApplicationProbeGrants = saved[:len(saved)/2]
			} else {
				v.discoveries[0].Configuration.ApplicationControlVersion = 3
			}
			candidate := spec.Canonical()
			if fault == "rollback_authority" {
				candidate.Verification.Entrypoint = false
			}
			v.refresh()
			if _, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", appInput(v.managedRolloutRig, candidate, previous.Number)); !errors.Is(err, domain.ErrConflict) {
				t.Fatal("deployment accepted incomplete desired/baseline authority", err)
			}
			v.discoveries[0].Configuration.ApplicationControlVersion = 4
			v.discoveries[0].ApplicationProbeGrants = saved
			v.refresh()
		})
	}
	if _, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", appInput(v.managedRolloutRig, spec, previous.Number)); err != nil {
		t.Fatal(err)
	}
	v.complete("succeeded")
	for _, n := range v.rollout.Nodes {
		if n.ApplicationVerification.Status != "passed" || len(n.ApplicationVerification.Checks) != 8 {
			t.Fatal("successful rollout skipped a path", n)
		}
	}
}

func TestEntrypointVerificationAuthorityMemory(t *testing.T) {
	entrypointVerificationAuthorityContract(t, NewMemory())
}
func TestEntrypointVerificationAuthorityPostgres(t *testing.T) {
	entrypointVerificationAuthorityContract(t, postgresForTest(t))
}

func entrypointVerificationRecoveryContract(t *testing.T, db Store) {
	v := newVerificationRigForPaths(t, db, true)
	baseline := v.discoveries[1].Configuration.Document.Canonical()
	previous := latestVerifiedApplication(v)
	spec := previous.Spec.Canonical()
	spec.Servers[0].Address = "wrong.backend"
	v.fault, v.rollbackFault = "http", "after_rejoin"
	if _, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", appInput(v.managedRolloutRig, spec, previous.Number)); err != nil {
		t.Fatal(err)
	}
	v.complete("rollback_failed")
	n := v.rollout.Nodes[0]
	if n.RecoveryMaintenanceID != "" || rolloutMaintenance(&n, true).Status != "completed" || n.RollbackApplicationVerification.ErrorCode != "application_entrypoint_http_failed" || n.ErrorCode != "rollback_entrypoint_verification_failed" || v.rollout.Nodes[1].ApplyOperationID != "" || !v.discoveries[1].Configuration.Document.Equal(baseline) {
		t.Fatal("external shared-path failure withdrew a verified baseline or changed the peer", n)
	}
}

func entrypointPeerCompensationContract(t *testing.T, db Store) {
	v := newVerificationRigForPaths(t, db, true)
	baseline := v.discoveries[0].Configuration.Document.Canonical()
	previous := latestVerifiedApplication(v)
	spec := previous.Spec.Canonical()
	spec.Servers[0].Address = "wrong.backend"
	v.fault, v.faultNode, v.sharedFault = "http", 1, true
	if _, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", appInput(v.managedRolloutRig, spec, previous.Number)); err != nil {
		t.Fatal(err)
	}
	v.complete("rolled_back")
	for i, n := range v.rollout.Nodes {
		if n.ApplyOperationID == "" || n.Status != "rolled_back" || n.RecoveryMaintenanceID != "" || n.RollbackApplicationVerification.Phase != "after_cluster_restore" || n.RollbackApplicationVerification.Status != "passed" || !v.discoveries[i].Configuration.Document.Equal(baseline) {
			t.Fatal("a healthy restored peer was quarantined before the remaining node could roll back", n)
		}
	}
}

func TestEntrypointPeerCompensationMemory(t *testing.T) {
	entrypointPeerCompensationContract(t, NewMemory())
}
func TestEntrypointPeerCompensationPostgres(t *testing.T) {
	entrypointPeerCompensationContract(t, postgresForTest(t))
}

func entrypointFinalDirectFailureContract(t *testing.T, db Store) {
	v := newVerificationRigForPaths(t, db, true)
	previous := latestVerifiedApplication(v)
	spec := previous.Spec.Canonical()
	spec.Servers[0].Address = "wrong.backend"
	v.fault, v.finalNodeFault = "http", true
	if _, err := db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", appInput(v.managedRolloutRig, spec, previous.Number)); err != nil {
		t.Fatal(err)
	}
	v.complete("rollback_failed")
	n := v.rollout.Nodes[0]
	if n.RecoveryMaintenanceID == "" || rolloutMaintenance(&n, true).Status != "attention" || n.RollbackApplicationVerification.Phase != "after_cluster_restore" || n.RollbackApplicationVerification.ErrorCode != "application_http_failed" {
		t.Fatal("a failed direct baseline check did not retain withdrawal", n)
	}
}

func TestEntrypointFinalDirectFailureMemory(t *testing.T) {
	entrypointFinalDirectFailureContract(t, NewMemory())
}
func TestEntrypointFinalDirectFailurePostgres(t *testing.T) {
	entrypointFinalDirectFailureContract(t, postgresForTest(t))
}

func TestEntrypointVerificationRecoveryMemory(t *testing.T) {
	entrypointVerificationRecoveryContract(t, NewMemory())
}
func TestEntrypointVerificationRecoveryPostgres(t *testing.T) {
	entrypointVerificationRecoveryContract(t, postgresForTest(t))
}

func TestPostgresEntrypointCheckAuthoritySurvivesCoordinatorRestart(t *testing.T) {
	p := postgresForTest(t)
	v := newVerificationRigForPaths(t, p, true)
	// Recreate the coordinator handle: all frozen authority/progress is read from
	// PostgreSQL, rather than held in a worker's memory.
	v.db = &Postgres{pool: p.pool}
	previous := latestVerifiedApplication(v)
	spec := previous.Spec.Canonical()
	spec.Servers[0].Address = "wrong.backend"
	v.fault = "http"
	if _, err := v.db.(ApplicationStore).DeployApplication(v.ctx, v.org, v.cluster, v.storeID, "", appInput(v.managedRolloutRig, spec, previous.Number)); err != nil {
		t.Fatal(err)
	}
	rows, _ := v.db.(RolloutStore).ListRollouts(v.ctx, v.org, v.cluster)
	for i, check := range rows[0].Nodes[0].ApplicationChecks {
		if check.Scope != "entrypoint" {
			continue
		}
		if _, err := p.pool.Exec(t.Context(), `UPDATE hm_rollouts SET payload=jsonb_set(payload,ARRAY['nodes','0','application_checks',$2,'scope'],'"node"'::jsonb) WHERE id=$1`, rows[0].ID, fmt.Sprint(i)); err == nil {
			t.Fatal("entrypoint authority could be changed after dispatch")
		}
		break
	}
	v.complete("rolled_back")
}
