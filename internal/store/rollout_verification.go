package store

import (
	_ "embed"
	"errors"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

//go:embed application_verification_schema.sql
var applicationVerificationSchemaSQL string

//go:embed entrypoint_verification_schema.sql
var entrypointVerificationSchemaSQL string

func prepareApplicationChecks(r domain.Rollout, d domain.Discovery) ([]domain.RolloutApplicationCheck, []domain.RolloutApplicationCheck, error) {
	forward, ok := domain.ApplicationChecks(r.Document, d)
	if !ok {
		return nil, nil, domain.ErrConflict
	}
	rollback, ok := domain.ApplicationChecks(*d.Configuration.Document, d)
	if !ok {
		return nil, nil, domain.ErrConflict
	}
	minimumVersion := uint32(3)
	for _, checks := range [][]domain.RolloutApplicationCheck{forward, rollback} {
		for _, c := range checks {
			if c.Scope == "entrypoint" {
				minimumVersion = 4
			}
		}
	}
	if (len(forward) > 0 || len(rollback) > 0) && (r.Entrypoint == nil || d.Configuration.ApplicationControlVersion < minimumVersion || d.Configuration.ApplicationControlVersion > 6) {
		return nil, nil, domain.ErrConflict
	}
	return forward, rollback, nil
}

func beginApplicationGate(n *domain.RolloutNode, rollback bool, phase string, now time.Time) {
	checks, gate := n.ApplicationChecks, &n.ApplicationVerification
	if rollback {
		checks, gate = n.RollbackApplicationChecks, &n.RollbackApplicationVerification
	}
	if len(checks) == 0 || (*gate != nil && (*gate).Phase == phase) {
		return
	}
	*gate = domain.NewApplicationVerification(checks, phase, now)
	deadline := now.Add(domain.ApplicationVerificationTimeout)
	if n.Deadline.Before(deadline) {
		n.Deadline = deadline
	}
}

func hasEntrypointChecks(checks []domain.RolloutApplicationCheck) bool {
	for _, c := range checks {
		if c.Scope == "entrypoint" {
			return true
		}
	}
	return false
}

func verifyRolloutApplications(n *domain.RolloutNode, o rolloutObservation, rollback bool, phase string, now time.Time) (bool, string) {
	// Shared requests can hit a peer still awaiting compensation. Verify each
	// restored node directly first, then the shared path after all baselines return.
	if rollback && phase == "after_rejoin" && hasEntrypointChecks(n.RollbackApplicationChecks) {
		phase = "after_node_restore"
	}
	beginApplicationGate(n, rollback, phase, now)
	checks, gate := n.ApplicationChecks, n.ApplicationVerification
	if rollback {
		checks, gate = n.RollbackApplicationChecks, n.RollbackApplicationVerification
	}
	if gate == nil {
		return true, ""
	}
	d := o.state.Discovery
	if !freshRolloutNode(o, now) {
		d = nil
	}
	switch gate.Advance(checks, d, now) {
	case "passed":
		return true, ""
	case "failed":
		return false, gate.ErrorCode
	default:
		return false, ""
	}
}

func verifyRestoredCluster(tx *rolloutTransaction, r *domain.Rollout, all []rolloutObservation, now time.Time) error {
	waiting := false
	for i := range r.Nodes {
		n, o := &r.Nodes[i], all[i]
		if !hasEntrypointChecks(n.RollbackApplicationChecks) {
			continue
		}
		evidence := o
		if !domain.RolloutHealthy(o.state, o.baseline.Document, now) {
			evidence.err = domain.ErrConflict
		}
		if ready, code := verifyRolloutApplications(n, evidence, true, "after_cluster_restore", now); !ready {
			if code == "" {
				waiting = true
				continue
			}
			if strings.HasPrefix(code, "application_entrypoint_") {
				// A shared failure does not identify an unsafe restored node.
				// Keep directly verified baselines serving during an external outage.
				n.Status, n.ErrorCode = "rollback_failed", "rollback_entrypoint_verification_failed"
				markRolloutTerminal(r, "rollback_failed", now)
				return nil
			}
			r.Index = i
			m, err := beginRolloutMaintenance(tx, r, *n, o, all, now)
			if err != nil {
				r.Index = -1
			}
			if errors.Is(err, domain.ErrConflict) {
				if !now.Before(r.Deadline) {
					n.Status, n.ErrorCode = "rollback_failed", "rollback_application_verification_failed"
					markRolloutTerminal(r, "rollback_failed", now)
				}
				return nil
			}
			if err != nil {
				return err
			}
			n.RollbackMaintenance, n.Status, r.Index = m, "withdrawing", i
			n.Deadline = now.Add(2 * time.Minute)
			return nil
		}
	}
	if waiting {
		return nil
	}
	markRolloutTerminal(r, "rolled_back", now)
	return nil
}
