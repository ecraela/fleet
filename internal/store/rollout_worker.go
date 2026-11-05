package store

import (
	"context"
	"errors"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type rolloutObservation struct {
	state     domain.NodeControl
	operation domain.Operation
	baseline  domain.ConfigurationRevision
	applied   domain.ConfigurationRevision
	err       error
}

func readRolloutNode(tx *rolloutTransaction, r domain.Rollout, n domain.RolloutNode) (out rolloutObservation) {
	out.err = tx.persistence.controlTransaction(tx.ctx, r.OrganizationID, n.NodeID, func(c *controlTransaction) error {
		if err := expirePending(c); err != nil {
			return err
		}
		out.state = c.state
		var err error
		out.baseline, err = c.revision(n.BaselineRevisionID)
		if err != nil {
			return err
		}
		id := n.ApplyOperationID
		if r.Status == "rolling_back" && n.RollbackOperationID != "" {
			id = n.RollbackOperationID
		}
		if id != "" {
			out.operation, err = c.get(id, false)
			if err != nil {
				return err
			}
			if out.operation.ConfigurationRevisionID != "" {
				out.applied, err = c.revision(out.operation.ConfigurationRevisionID)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	return
}
func freshRolloutNode(o rolloutObservation, now time.Time) bool {
	return o.err == nil && configurationEnabled(o.state, now)
}
func markRolloutTerminal(r *domain.Rollout, status string, now time.Time) {
	r.Status = status
	r.CompletedAt = &now
}
func failRollout(r *domain.Rollout, code string, now time.Time) {
	r.Status = "rolling_back"
	r.ErrorCode = code
	r.Deadline = now.Add(15 * time.Minute)
	if r.Index >= len(r.Nodes) {
		r.Index = len(r.Nodes) - 1
	}
	for i := range r.Nodes {
		r.Nodes[i].HealthSinceMS = 0
		r.Nodes[i].LastHealthMS = 0
		r.Nodes[i].LastObservationMS = 0
		r.Nodes[i].Deadline = now.Add(2 * time.Minute)
		if i > r.Index {
			r.Nodes[i].Status = "skipped"
		}
	}
}
func nextRollbackNode(r *domain.Rollout, now time.Time) {
	r.Index--
	if r.Index >= 0 {
		r.Nodes[r.Index].Deadline = now.Add(2 * time.Minute)
	}
}
func healthWindow(n *domain.RolloutNode, o rolloutObservation, doc domain.ConfigurationDocument, now time.Time) bool {
	if !domain.RolloutHealthy(o.state, doc, now) {
		n.HealthSinceMS = 0
		n.LastHealthMS = 0
		n.LastObservationMS = 0
		return false
	}
	at := o.state.Discovery.ObservedAtMS
	if at <= n.LastObservationMS {
		return false
	}
	checkpoint := now.UnixMilli()
	if n.LastHealthMS == 0 || checkpoint-n.LastHealthMS > 3000 || at-n.LastObservationMS > 3000 {
		n.HealthSinceMS = checkpoint
	}
	n.LastObservationMS = at
	n.LastHealthMS = checkpoint
	return checkpoint-n.HealthSinceMS >= domain.RolloutHealthWindow.Milliseconds()
}

func peersHealthy(r domain.Rollout, all []rolloutObservation, index int, now time.Time) bool {
	for i, o := range all {
		if i == index {
			continue
		}
		if !freshRolloutNode(o, now) || o.state.Discovery.Configuration.Document == nil || !domain.RolloutHealthy(o.state, *o.state.Discovery.Configuration.Document, now) {
			return false
		}
		expected := r.Nodes[i].ExpectedArtifactID
		if r.Nodes[i].AppliedArtifactID != "" {
			expected = r.Nodes[i].AppliedArtifactID
		}
		if o.state.Discovery.Configuration.ArtifactID != expected {
			return false
		}
	}
	return true
}
func operationQuiescent(op domain.Operation, o rolloutObservation, now time.Time) bool {
	if !op.Terminal() {
		return false
	}
	if op.Status != "unknown" {
		return true
	}
	return now.After(op.Deadline.Add(3*time.Second)) && freshRolloutNode(o, now) && o.state.Discovery.ObservedAtMS > op.Deadline.Add(2*time.Second).UnixMilli()
}
func queueRolloutConfiguration(tx *rolloutTransaction, r domain.Rollout, n domain.RolloutNode, o rolloutObservation, rollback bool) (domain.Operation, error) {
	d := o.state.Discovery
	s := d.Configuration
	input := domain.Operation{RequestID: domain.NewID(), NodeID: n.NodeID, ExpectedGeneration: d.Generation, BaseArtifactID: s.ArtifactID, BaseVersion: s.Version, Configuration: &r.Document}
	restore := ""
	if rollback {
		restore = n.BaselineRevisionID
		input.Configuration = nil
	}
	ctx := context.WithValue(domain.WithActor(tx.ctx, r.ActorUserID), rolloutWriterKey{}, r.ID)
	return createConfiguration(ctx, tx.persistence, r.OrganizationID, input, restore)
}
func cancelUnsentRolloutOperation(tx *rolloutTransaction, r domain.Rollout, n domain.RolloutNode, op domain.Operation, now time.Time) error {
	if op.Status != "queued" {
		return nil
	}
	ctx := domain.WithActor(tx.ctx, r.ActorUserID)
	return tx.persistence.controlTransaction(ctx, r.OrganizationID, n.NodeID, func(c *controlTransaction) error {
		queued, err := c.get(op.ID, false)
		if err != nil {
			return err
		}
		if queued.Status != "queued" || queued.CommandDigest != "" {
			return domain.ErrConflict
		}
		queued.Status = "failed"
		queued.ErrorCode = "rollout_cancelled"
		queued.CompletedAt = &now
		if err := saveControlOperation(c, queued); err != nil {
			return err
		}
		if c.state.PendingOperationID == queued.ID {
			c.state.PendingOperationID = ""
		}
		return c.audit("operation.failed", queued.ID)
	})
}
func advanceRollout(ctx context.Context, p rolloutPersistence, org, cluster, id string, now time.Time) error {
	return p.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error {
		r, err := tx.get(id, false)
		if err != nil {
			return err
		}
		if r.Terminal() {
			return nil
		}
		if r.Entrypoint != nil {
			return advanceManagedRollout(tx, &r, now)
		}
		previous := r.Status
		r.UpdatedAt = now
		all := make([]rolloutObservation, len(r.Nodes))
		for i, n := range r.Nodes {
			all[i] = readRolloutNode(tx, r, n)
		}
		if r.Status == "running" {
			membership := len(tx.nodes) == len(r.Nodes)
			for _, target := range r.Nodes {
				found := false
				for _, node := range tx.nodes {
					found = found || target.NodeID == node.ID
				}
				membership = membership && found
			}
			if !membership {
				failRollout(&r, "cluster_membership_changed", now)
			}
		}
		if r.Status == "running" && !now.Before(r.Deadline) {
			failRollout(&r, "rollout_deadline", now)
		}
		if r.Status == "running" {
			if r.Index >= len(r.Nodes) {
				healthy := true
				for _, o := range all {
					healthy = healthy && freshRolloutNode(o, now) && domain.RolloutHealthy(o.state, r.Document, now)
				}
				if healthy {
					markRolloutTerminal(&r, "succeeded", now)
				} else {
					failRollout(&r, "cluster_health_failed", now)
				}
			} else {
				n := &r.Nodes[r.Index]
				o := all[r.Index]
				switch n.Status {
				case "pending":
					n.Status = "waiting"
					n.Deadline = now.Add(20 * time.Second)
				case "waiting":
					if o.err != nil && (errors.Is(o.err, domain.ErrRevoked) || errors.Is(o.err, domain.ErrNotFound)) {
						n.ErrorCode = "node_unavailable"
						failRollout(&r, n.ErrorCode, now)
						break
					}
					if !freshRolloutNode(o, now) || !peersHealthy(r, all, r.Index, now) || !domain.RolloutHealthy(o.state, o.baseline.Document, now) {
						if !now.Before(n.Deadline) {
							n.ErrorCode = "preflight_health_failed"
							failRollout(&r, n.ErrorCode, now)
						}
						break
					}
					s := o.state.Discovery.Configuration
					if s.ArtifactID != n.ExpectedArtifactID || s.Version != n.ExpectedVersion || o.state.Discovery.Generation != n.ExpectedGeneration {
						n.ErrorCode = "configuration_changed"
						failRollout(&r, n.ErrorCode, now)
						break
					}
					if s.Document.Equal(r.Document) {
						n.Status = "checking"
						n.AppliedArtifactID = s.ArtifactID
						n.Deadline = now.Add(20 * time.Second)
						break
					}
					op, err := queueRolloutConfiguration(tx, r, *n, o, false)
					if err != nil {
						if errors.Is(err, domain.ErrConflict) {
							n.ErrorCode = "configuration_changed"
							failRollout(&r, n.ErrorCode, now)
							break
						}
						return err
					}
					n.ApplyOperationID = op.ID
					n.Status = "applying"
					n.Deadline = op.Deadline.Add(20 * time.Second)
				case "applying":
					if o.err != nil {
						if !now.Before(n.Deadline) {
							n.ErrorCode = "node_unavailable"
							failRollout(&r, n.ErrorCode, now)
						}
						break
					}
					op := o.operation
					if op.Status == "succeeded" {
						n.AppliedArtifactID = o.applied.ArtifactID
						n.Status = "checking"
						n.Deadline = now.Add(20 * time.Second)
					} else if op.Status == "failed" || (op.Status == "unknown" && now.After(op.Deadline.Add(3*time.Second))) {
						n.ErrorCode = op.ErrorCode
						if n.ErrorCode == "" {
							n.ErrorCode = "operation_failed"
						}
						failRollout(&r, n.ErrorCode, now)
					}
				case "checking":
					if freshRolloutNode(o, now) && o.state.Discovery.Configuration.ArtifactID != n.AppliedArtifactID {
						n.ErrorCode = "configuration_changed"
						failRollout(&r, n.ErrorCode, now)
						break
					}
					if !peersHealthy(r, all, r.Index, now) {
						n.HealthSinceMS = 0
						n.LastHealthMS = 0
						n.LastObservationMS = 0
						if !now.Before(n.Deadline) {
							n.ErrorCode = "peer_health_failed"
							failRollout(&r, n.ErrorCode, now)
						}
						break
					}
					if healthWindow(n, o, r.Document, now) {
						n.Status = "applied"
						r.Index++
					} else if !now.Before(n.Deadline) {
						n.ErrorCode = "health_check_failed"
						failRollout(&r, n.ErrorCode, now)
					}
				}
			}
		} else if r.Status == "rolling_back" {
			if !now.Before(r.Deadline) {
				for i := range r.Nodes {
					n := &r.Nodes[i]
					if all[i].err == nil {
						if err := cancelUnsentRolloutOperation(tx, r, *n, all[i].operation, now); err != nil {
							return err
						}
					}
					if n.ApplyOperationID != "" && n.Status != "rolled_back" {
						n.Status = "rollback_failed"
						n.ErrorCode = "rollback_deadline"
					}
				}
				markRolloutTerminal(&r, "rollback_failed", now)
			} else if r.Index < 0 {
				status := "rolled_back"
				for _, n := range r.Nodes {
					if n.Status == "rollback_failed" {
						status = "rollback_failed"
					}
				}
				markRolloutTerminal(&r, status, now)
			} else {
				n := &r.Nodes[r.Index]
				o := all[r.Index]
				switch {
				case n.ApplyOperationID == "":
					n.Status = "skipped"
					nextRollbackNode(&r, now)
				case n.RollbackOperationID == "" && o.err == nil && o.operation.CommandDigest == "":
					// Work never sent to an agent cannot have changed an offline node.
					if err := cancelUnsentRolloutOperation(tx, r, *n, o.operation, now); err != nil {
						return err
					}
					n.Status = "skipped"
					nextRollbackNode(&r, now)
				case o.err != nil || !freshRolloutNode(o, now):
					n.Status = "waiting_for_rollback"
					if !now.Before(n.Deadline) {
						n.Status = "rollback_failed"
						n.ErrorCode = "node_unavailable"
						nextRollbackNode(&r, now)
					}
				case n.RollbackOperationID != "":
					op := o.operation
					if op.Status == "succeeded" {
						if o.state.Discovery.Configuration.ArtifactID != o.applied.ArtifactID {
							n.Status = "rollback_failed"
							n.ErrorCode = "configuration_changed"
							nextRollbackNode(&r, now)
							break
						}
						if n.Status != "checking_rollback" {
							n.Status = "checking_rollback"
							n.Deadline = now.Add(20 * time.Second)
							n.HealthSinceMS = 0
							n.LastHealthMS = 0
							n.LastObservationMS = 0
						}
						if healthWindow(n, o, o.baseline.Document, now) {
							n.Status = "rolled_back"
							nextRollbackNode(&r, now)
						} else if !now.Before(n.Deadline) {
							n.Status = "rollback_failed"
							n.ErrorCode = "rollback_health_failed"
							nextRollbackNode(&r, now)
						}
					} else if operationQuiescent(op, o, now) {
						n.Status = "rollback_failed"
						n.ErrorCode = op.ErrorCode
						nextRollbackNode(&r, now)
					}
				default:
					op := o.operation

					if !operationQuiescent(op, o, now) {
						break
					}
					s := o.state.Discovery.Configuration
					if s.ArtifactID == n.ExpectedArtifactID && domain.RolloutHealthy(o.state, o.baseline.Document, now) {
						n.Status = "rolled_back"
						nextRollbackNode(&r, now)
						break
					}
					expected := n.AppliedArtifactID
					if expected != "" && s.ArtifactID != expected {
						n.Status = "rollback_failed"
						n.ErrorCode = "configuration_changed"
						nextRollbackNode(&r, now)
						break
					}
					if expected == "" && (s.Version != n.ExpectedVersion+1 || (!s.Document.Equal(r.Document) && !s.Document.Equal(o.baseline.Document))) {
						n.Status = "rollback_failed"
						n.ErrorCode = "configuration_changed"
						nextRollbackNode(&r, now)
						break
					}
					restore, err := queueRolloutConfiguration(tx, r, *n, o, true)
					if err != nil {
						if errors.Is(err, domain.ErrConflict) {
							n.Status = "rollback_failed"
							n.ErrorCode = "configuration_changed"
							nextRollbackNode(&r, now)
							break
						}
						return err
					}
					n.RollbackOperationID = restore.ID
					n.Status = "rolling_back"
					n.Deadline = restore.Deadline.Add(20 * time.Second)
					n.HealthSinceMS = 0
					n.LastHealthMS = 0
					n.LastObservationMS = 0
				}
			}
		}
		if err := tx.save(r); err != nil {
			return err
		}
		if r.Status != previous {
			return tx.audit(r, "rollout."+r.Status)
		}
		return nil
	})
}
func advanceRollouts(ctx context.Context, p rolloutPersistence) error {
	all, err := p.activeRollouts(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, r := range all {
		step, stop := context.WithTimeout(ctx, 3*time.Second)
		err := advanceRollout(step, p, r.OrganizationID, r.ClusterID, r.ID, time.Now().UTC())
		stop()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}
func (m *Memory) AdvanceRollouts(ctx context.Context) error   { return advanceRollouts(ctx, m) }
func (p *Postgres) AdvanceRollouts(ctx context.Context) error { return advanceRollouts(ctx, p) }
