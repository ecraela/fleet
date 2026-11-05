package store

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func maintenanceOperation(tx *maintenanceTransaction, m domain.Maintenance) (out domain.Operation, err error) {
	if m.OperationID == "" {
		return
	}
	err = tx.persistence.controlTransaction(tx.ctx, m.OrganizationID, m.ControllerNodeID, func(c *controlTransaction) error {
		if err := expirePending(c); err != nil {
			return err
		}
		var e error
		out, e = c.get(m.OperationID, false)
		return e
	})
	return
}
func maintenanceAttention(m *domain.Maintenance, code string) {
	m.Status = "attention"
	m.ErrorCode = code
	resetMaintenanceWindow(m)
}
func resetMaintenanceWindow(m *domain.Maintenance) {
	m.HealthSinceMS = 0
	m.LastHealthMS = 0
	m.LastObservationMS = 0
}
func maintenanceWindow(m *domain.Maintenance, s domain.NodeControl, healthy bool, duration time.Duration, now time.Time) bool {
	if !healthy || s.Discovery == nil {
		resetMaintenanceWindow(m)
		return false
	}
	at := s.Discovery.ObservedAtMS
	if at <= m.LastObservationMS {
		return false
	}
	checkpoint := now.UnixMilli()
	if m.LastHealthMS == 0 || checkpoint-m.LastHealthMS > 3000 || at-m.LastObservationMS > 3000 {
		m.HealthSinceMS = checkpoint
	}
	m.LastHealthMS = checkpoint
	m.LastObservationMS = at
	return checkpoint-m.HealthSinceMS >= duration.Milliseconds()
}
func queueMaintenance(tx *maintenanceTransaction, m *domain.Maintenance, s domain.NodeControl, configuration, withdraw bool) error {
	ctx := context.WithValue(domain.WithActor(tx.ctx, m.ActorUserID), maintenanceWriterKey{}, m.ID)
	d := s.Discovery
	var op domain.Operation
	var err error
	if configuration {
		doc := m.Document(withdraw)
		op, err = createConfiguration(ctx, tx.persistence, m.OrganizationID, domain.Operation{RequestID: domain.NewID(), NodeID: m.ControllerNodeID, ExpectedGeneration: d.Generation, BaseArtifactID: d.Configuration.ArtifactID, BaseVersion: d.Configuration.Version, Configuration: &doc}, "")
	} else {
		target, ok := d.Server(m.Backend, m.Server)
		if !ok {
			return domain.ErrConflict
		}
		kind := "restore"
		if withdraw {
			kind = "maintenance"
		}
		op, err = createOperation(ctx, tx.persistence, m.OrganizationID, domain.Operation{RequestID: domain.NewID(), NodeID: m.ControllerNodeID, Kind: kind, Backend: m.Backend, Server: m.Server, ExpectedGeneration: d.Generation, ExpectedState: target.State, ExpectedWeight: target.Weight})
	}
	if err != nil {
		return err
	}
	m.OperationID = op.ID
	return nil
}

// A checkpoint owns exactly one operation. Unknown outcomes are never replayed;
// an externally changed artifact stays fenced for operator investigation.
func settleMaintenanceOperation(tx *maintenanceTransaction, m *domain.Maintenance, s domain.NodeControl, now time.Time) (done, success bool, err error) {
	op, err := maintenanceOperation(tx, *m)
	if err != nil {
		return false, false, err
	}
	if !operationQuiescent(op, rolloutObservation{state: s}, now) {
		return false, false, nil
	}
	if op.Status != "succeeded" {
		return true, false, nil
	}
	if op.Kind == "configuration" {
		var revision domain.ConfigurationRevision
		err = tx.persistence.controlTransaction(tx.ctx, m.OrganizationID, m.ControllerNodeID, func(c *controlTransaction) error {
			var e error
			revision, e = c.revision(op.ConfigurationRevisionID)
			return e
		})
		if err != nil {
			return false, false, err
		}
		if !configurationEnabled(s, now) || revision.ArtifactID == "" || s.Discovery.Configuration.ArtifactID != revision.ArtifactID || !s.Discovery.Configuration.Document.Equal(*op.Configuration) {
			return false, false, nil
		}
		m.OwnedArtifactID = revision.ArtifactID
		m.OwnedVersion = s.Discovery.Configuration.Version
	}
	m.OperationID = ""
	return true, true, nil
}
func recoverMaintenance(m *domain.Maintenance, now time.Time) {
	m.Status = "withdrawing"
	m.Step = "runtime"
	m.ErrorCode = "rejoin_failed"
	m.Deadline = now.Add(2 * time.Minute)
	resetMaintenanceWindow(m)
}
func advanceMaintenance(ctx context.Context, p maintenancePersistence, org, node, id string, now time.Time) error {
	return p.maintenanceTransaction(ctx, org, node, func(tx *maintenanceTransaction) error {
		return advanceMaintenanceTransaction(tx, org, node, id, now)
	})
}

// The rollout coordinator supplies the same durable checkpoints inside its transaction.
func advanceMaintenanceTransaction(tx *maintenanceTransaction, org, node, id string, now time.Time) error {
	m, err := tx.get(id)
	if err != nil {
		return err
	}
	if m.Terminal() {
		return nil
	}
	previous := m.Status
	m.UpdatedAt = now
	save := func() error {
		if err := tx.save(m); err != nil {
			return err
		}
		if m.Status != previous {
			return tx.audit(m, "maintenance."+m.Status)
		}
		return nil
	}
	if m.Status == "attention" {
		return save()
	}
	if tx.cluster.Topology == nil || tx.cluster.Topology.BindingID() != m.BindingID || tx.controller.ID != m.ControllerNodeID || tx.controller.RevokedAt != nil {
		maintenanceAttention(&m, "entrypoint_binding_changed")
		return save()
	}
	controller, err := readMaintenanceControl(tx, org, m.ControllerNodeID)
	if err != nil || !configurationEnabled(controller, now) || !managementEnabled(controller, now) {
		if m.Status == "maintenance" {
			return save()
		}
		if !now.Before(m.Deadline) {
			maintenanceAttention(&m, "entrypoint_unavailable")
			return save()
		}
		return nil
	}
	// A successful configuration receipt is the only way to advance ownership.
	if m.OperationID != "" {
		done, success, err := settleMaintenanceOperation(tx, &m, controller, now)
		if err != nil {
			return err
		}
		if !done {
			if !now.Before(m.Deadline) {
				maintenanceAttention(&m, "operation_unverified")
				return save()
			}
			return nil
		}
		if !success {
			if m.Status == "rejoining" && controller.Discovery.Configuration.ArtifactID == m.OwnedArtifactID {
				m.OperationID = ""
				recoverMaintenance(&m, now)
			} else {
				maintenanceAttention(&m, "operation_failed")
				return save()
			}
		}
	}
	d := controller.Discovery
	c := d.Configuration
	server, found := d.Server(m.Backend, m.Server)
	if !found || domain.MaintenanceAddress(*c.Document, m.Backend, m.Server) != m.Address || c.ArtifactID != m.OwnedArtifactID || c.Version != m.OwnedVersion {
		maintenanceAttention(&m, "entrypoint_configuration_changed")
		return save()
	}
	target, targetErr := readMaintenanceControl(tx, org, node)
	count, known := domain.FrontendConnections(target, m.Frontend, now)
	if targetErr != nil {
		known = false
	}
	m.Connections = nil
	if known {
		m.Connections = &count
	}
	peers := maintenancePeers(tx, org, node, now)
	healthy := targetErr == nil && maintenanceReady(target, now) && target.PendingOperationID == "" && peers
	targetKnown := targetErr == nil && target.Discovery != nil && target.Discovery.Fresh(now) && (m.TargetArtifactID == "" || (target.Discovery.Configuration != nil && target.Discovery.Configuration.Fresh(now)))
	targetMatches := target.Discovery != nil && target.Discovery.Generation == m.TargetGeneration && (m.TargetArtifactID == "" || (target.Discovery.Configuration != nil && target.Discovery.Configuration.ArtifactID == m.TargetArtifactID))
	if m.Status != "maintenance" && !now.Before(m.Deadline) {
		if m.Status == "rejoining" {
			recoverMaintenance(&m, now)
		} else {
			maintenanceAttention(&m, "maintenance_deadline")
			return save()
		}
	}
	queue := func(config, withdraw bool) error {
		err := queueMaintenance(tx, &m, controller, config, withdraw)
		if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrNotFound) {
			maintenanceAttention(&m, "entrypoint_changed")
			return save()
		}
		if err != nil {
			return err
		}
		return save()
	}
	switch m.Status {
	case "withdrawing":
		switch m.Step {
		case "runtime":
			// Stop all new selection before a graceful reload leaves old workers alive.
			if server.State != "maint" {
				return queue(false, true)
			}
			m.Step = "persist"
		case "persist":
			if !c.Document.Equal(m.Document(true)) {
				return queue(true, true)
			}
			if server.State != "maint" {
				m.Step = "runtime"
				break
			}
			m.Status = "draining"
			m.Step = "connections"
			m.TargetGeneration = ""
			if target.Discovery != nil {
				m.TargetGeneration = target.Discovery.Generation
			}
			m.Deadline = now.Add(time.Duration(m.DrainTimeoutSeconds) * time.Second)
			resetMaintenanceWindow(&m)
		}
	case "draining":
		if server.State != "maint" || !c.Document.Equal(m.Document(true)) {
			maintenanceAttention(&m, "entrypoint_not_withdrawn")
			break
		}
		if !known || target.Discovery.Generation != m.TargetGeneration {
			resetMaintenanceWindow(&m)
			break
		}
		if maintenanceWindow(&m, target, count == 0, 3*time.Second, now) {
			m.Status = "maintenance"
			m.Step = "held"
			resetMaintenanceWindow(&m)
		}
	case "maintenance":
		if server.State != "maint" || !c.Document.Equal(m.Document(true)) {
			maintenanceAttention(&m, "entrypoint_not_withdrawn")
		}
	case "checking_rejoin":
		if server.State != "maint" || !c.Document.Equal(m.Document(true)) {
			maintenanceAttention(&m, "entrypoint_not_withdrawn")
			break
		}
		if !targetKnown {
			// An unavailable observation is not evidence of a changed target.
			// Keep it withdrawn and restart the bounded continuous health window.
			resetMaintenanceWindow(&m)
			break
		}
		if !targetMatches {
			maintenanceAttention(&m, "target_changed")
			break
		}
		if maintenanceWindow(&m, target, healthy, domain.RolloutHealthWindow, now) {
			m.Status = "rejoining"
			m.Step = "persist"
			resetMaintenanceWindow(&m)
			m.Deadline = now.Add(2 * time.Minute)
		}
	case "rejoining":
		if !healthy || !targetMatches {
			doc := domain.ConfigurationDocument{}
			if target.Discovery != nil && target.Discovery.Configuration != nil && target.Discovery.Configuration.Document != nil {
				doc = *target.Discovery.Configuration.Document
			}
			slog.Warn("HAProxy rejoin health failed", "node_id", m.NodeID, "reason", domain.RolloutHealthIssue(target, doc, now), "peers_healthy", peers, "target_matches", targetMatches)
			recoverMaintenance(&m, now)
			break
		}
		switch m.Step {
		case "persist":
			if !c.Document.Equal(m.Document(false)) {
				return queue(true, false)
			}
			m.Step = "runtime"
		case "runtime":
			if server.State != "ready" {
				return queue(false, false)
			}
			m.Step = "verify"
			m.Deadline = now.Add(20 * time.Second)
			resetMaintenanceWindow(&m)
		case "verify":
			if !c.Document.Equal(m.Document(false)) || server.State != "ready" {
				recoverMaintenance(&m, now)
				break
			}
			if maintenanceWindow(&m, target, healthy && maintenanceReady(controller, now), domain.RolloutHealthWindow, now) {
				m.Status = "completed"
				m.Step = "joined"
				m.ErrorCode = ""
				m.CompletedAt = &now
			}
		}
	}
	return save()
}

func advanceMaintenances(ctx context.Context, p maintenancePersistence) error {
	all, err := p.activeMaintenance(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, m := range all {
		step, stop := context.WithTimeout(ctx, 3*time.Second)
		err := advanceMaintenance(step, p, m.OrganizationID, m.NodeID, m.ID, time.Now().UTC())
		stop()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}
func (m *Memory) AdvanceMaintenance(ctx context.Context) error   { return advanceMaintenances(ctx, m) }
func (p *Postgres) AdvanceMaintenance(ctx context.Context) error { return advanceMaintenances(ctx, p) }
