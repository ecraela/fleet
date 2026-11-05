package store

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func sameRolloutEntrypoint(r domain.Rollout, in domain.RolloutInput) bool {
	if r.Entrypoint == nil || in.Entrypoint == nil {
		return r.Entrypoint == nil && in.Entrypoint == nil
	}
	return r.Entrypoint.RolloutEntrypointTarget == *in.Entrypoint && r.DrainTimeoutSeconds == in.DrainTimeoutSeconds
}

func rolloutMaintenanceTransaction(tx *rolloutTransaction, r *domain.Rollout, n domain.Node) *maintenanceTransaction {
	mt := &maintenanceTransaction{ctx: context.WithValue(domain.WithActor(tx.ctx, r.ActorUserID), rolloutWriterKey{}, r.ID), persistence: tx.persistence, cluster: tx.cluster, controller: tx.controller, node: n, nodes: tx.nodes}
	mt.peerArtifact = func(node string) string {
		for _, target := range r.Nodes {
			if target.NodeID == node {
				if target.AppliedArtifactID != "" {
					return target.AppliedArtifactID
				}
				return target.ExpectedArtifactID
			}
		}
		return ""
	}
	return mt
}

func prepareRolloutEntrypoint(tx *rolloutTransaction, r *domain.Rollout, in domain.RolloutInput, now time.Time) error {
	t := tx.cluster.Topology
	if t == nil || t.Control == nil {
		if in.Entrypoint != nil {
			return domain.ErrConflict
		}
		// Existing standalone integrations without a managed entrypoint retain
		// their health-gated rollout API. The console requires a managed binding.
		return nil
	}
	if in.Entrypoint == nil || in.Entrypoint.NodeID != tx.controller.ID || in.Entrypoint.BindingID != t.BindingID() || !validTopology(*t) || len(t.Nodes) != len(tx.nodes) {
		return domain.ErrConflict
	}
	mt := rolloutMaintenanceTransaction(tx, r, tx.nodes[0])
	s, err := readMaintenanceControl(mt, r.OrganizationID, tx.controller.ID)
	if err != nil {
		return err
	}
	if !configurationEnabled(s, now) || !managementEnabled(s, now) || !maintenanceReady(s, now) || s.PendingOperationID != "" {
		return domain.ErrConflict
	}
	d := s.Discovery
	c := d.Configuration
	if d.Generation != in.Entrypoint.ExpectedGeneration || c.ArtifactID != in.Entrypoint.ExpectedArtifactID || c.Version != in.Entrypoint.ExpectedVersion {
		return domain.ErrConflict
	}
	mapped := map[string]bool{}
	for _, binding := range t.Control.Servers {
		found := false
		for _, node := range tx.nodes {
			found = found || node.ID == binding.NodeID
		}
		server, ok := d.Server(t.Control.Backend, binding.Server)
		if !found || !ok || server.State != "ready" || server.Weight == 0 {
			return domain.ErrConflict
		}
		mapped[binding.Server] = true
	}
	for _, b := range c.Document.Backends {
		if b.Name != t.Control.Backend {
			continue
		}
		if len(b.Servers) != len(mapped) {
			return domain.ErrConflict
		}
		for _, s := range b.Servers {
			if !mapped[s.Name] || s.Maintenance || s.Weight == 0 {
				return domain.ErrConflict
			}
		}
	}
	r.Entrypoint = &domain.RolloutEntrypoint{RolloutEntrypointTarget: *in.Entrypoint, ClusterID: tx.controller.ClusterID, Topology: *cloneCluster(tx.cluster).Topology, Baseline: c.Document.Canonical(), OwnedArtifactID: c.ArtifactID, OwnedVersion: c.Version}
	return nil
}

// Maintenance checkpoints are embedded in the rollout, under its two-cluster
// reservation. They use the standalone maintenance state machine and outbound
// operations, but cannot be advanced or reclaimed by a public maintenance call.
func advanceRolloutMaintenance(tx *rolloutTransaction, r *domain.Rollout, m *domain.Maintenance, now time.Time) error {
	mt := rolloutMaintenanceTransaction(tx, r, domain.Node{ID: m.NodeID, OrganizationID: r.OrganizationID, ClusterID: r.ClusterID})
	mt.get = func(string) (domain.Maintenance, error) { return cloneMaintenance(*m), nil }
	mt.save = func(next domain.Maintenance) error {
		*m = cloneMaintenance(next)
		r.Entrypoint.OwnedArtifactID, r.Entrypoint.OwnedVersion = m.OwnedArtifactID, m.OwnedVersion
		return nil
	}
	mt.audit = func(m domain.Maintenance, _ string) error { return tx.audit(*r, "rollout.node_"+m.Status) }
	return advanceMaintenanceTransaction(mt, r.OrganizationID, m.NodeID, m.ID, now)
}

func beginRolloutMaintenance(tx *rolloutTransaction, r *domain.Rollout, n domain.RolloutNode, o rolloutObservation, all []rolloutObservation, now time.Time) (*domain.Maintenance, error) {
	e := r.Entrypoint
	if tx.cluster.Topology == nil || tx.cluster.Topology.BindingID() != e.BindingID || tx.controller.ID != e.NodeID || !freshRolloutNode(o, now) || !peersHealthy(*r, all, r.Index, now) {
		return nil, domain.ErrConflict
	}
	mt := rolloutMaintenanceTransaction(tx, r, domain.Node{ID: n.NodeID, OrganizationID: r.OrganizationID, ClusterID: r.ClusterID})
	c, err := readMaintenanceControl(mt, r.OrganizationID, e.NodeID)
	if err != nil {
		return nil, err
	}
	if !configurationEnabled(c, now) || !managementEnabled(c, now) || c.PendingOperationID != "" {
		return nil, domain.ErrConflict
	}
	d := c.Discovery
	if d.Configuration.ArtifactID != e.OwnedArtifactID || d.Configuration.Version != e.OwnedVersion || !d.Configuration.Document.Equal(e.Baseline) {
		return nil, domain.ErrConflict
	}
	if _, known := domain.FrontendConnections(o.state, e.Topology.Frontend, now); !known {
		return nil, domain.ErrConflict
	}
	server := ""
	for _, binding := range e.Topology.Control.Servers {
		if binding.NodeID == n.NodeID {
			server = binding.Server
		}
	}
	actual, ok := d.Server(e.Topology.Control.Backend, server)
	if !ok || actual.State != "ready" || actual.Weight == 0 {
		return nil, domain.ErrConflict
	}
	// Every remaining upstream must be healthy before this one is removed.
	for _, binding := range e.Topology.Control.Servers {
		if binding.NodeID == n.NodeID {
			continue
		}
		s, ok := d.Server(e.Topology.Control.Backend, binding.Server)
		if !ok || s.State != "ready" || s.Weight == 0 || s.Health != "UP" {
			return nil, domain.ErrConflict
		}
	}
	m := &domain.Maintenance{ID: domain.NewID(), OrganizationID: r.OrganizationID, ClusterID: r.ClusterID, NodeID: n.NodeID, ControllerNodeID: e.NodeID, ControllerClusterID: e.ClusterID, ActorUserID: r.ActorUserID, BindingID: e.BindingID, Backend: e.Topology.Control.Backend, Server: server, Address: domain.MaintenanceAddress(e.Baseline, e.Topology.Control.Backend, server), Frontend: e.Topology.Frontend, Topology: e.Topology, Baseline: e.Baseline.Canonical(), OwnedArtifactID: e.OwnedArtifactID, OwnedVersion: e.OwnedVersion, TargetGeneration: o.state.Discovery.Generation, Status: "withdrawing", Step: "runtime", DrainTimeoutSeconds: r.DrainTimeoutSeconds, CreatedAt: now, UpdatedAt: now, Deadline: now.Add(2 * time.Minute)}
	return m, nil
}

func rolloutMaintenance(n *domain.RolloutNode, rollback bool) *domain.Maintenance {
	if rollback && n.RollbackMaintenance != nil {
		return n.RollbackMaintenance
	}
	return n.Maintenance
}

func maintenanceForRolloutNode(r domain.Rollout, node string) *domain.Maintenance {
	for _, n := range r.Nodes {
		if n.NodeID != node {
			continue
		}
		m := rolloutMaintenance(&n, r.Status == "rolling_back")
		if m != nil {
			copy := cloneMaintenance(*m)
			return &copy
		}
	}
	return nil
}

func requestRolloutRejoin(m *domain.Maintenance, o rolloutObservation, now time.Time) {
	m.Status, m.Step, m.ErrorCode = "checking_rejoin", "health", ""
	m.OperationID = ""
	m.TargetGeneration = o.state.Discovery.Generation
	m.TargetArtifactID = o.state.Discovery.Configuration.ArtifactID
	m.Deadline = now.Add(20 * time.Second)
	resetMaintenanceWindow(m)
}

// An unverified or failed recovery leaves a normal maintenance reservation for
// the operator. Releasing the rollout never silently restores traffic, and the
// remaining serving node is not withdrawn to roll back an earlier step.
func handoffRolloutMaintenance(tx *rolloutTransaction, r *domain.Rollout, n *domain.RolloutNode, m *domain.Maintenance, code string, now time.Time) error {
	if m != nil && !m.Terminal() {
		maintenanceAttention(m, code)
		m.UpdatedAt = now
		if err := tx.handoff(*m); err != nil {
			return err
		}
		n.RecoveryMaintenanceID = m.ID
	}
	n.Status, n.ErrorCode = "rollback_failed", code
	if r.ErrorCode == "" {
		r.ErrorCode = code
	}
	markRolloutTerminal(r, "rollback_failed", now)
	return nil
}

func managedRolloutMembership(tx *rolloutTransaction, r domain.Rollout) bool {
	if len(tx.nodes) != len(r.Nodes) {
		return false
	}
	for _, n := range r.Nodes {
		found := false
		for _, actual := range tx.nodes {
			found = found || n.NodeID == actual.ID
		}
		if !found {
			return false
		}
	}
	return true
}

func advanceManagedRollout(tx *rolloutTransaction, r *domain.Rollout, now time.Time) error {
	previous := r.Status
	r.UpdatedAt = now
	all := make([]rolloutObservation, len(r.Nodes))
	for i, n := range r.Nodes {
		all[i] = readRolloutNode(tx, *r, n)
	}
	if r.Status == "running" && !managedRolloutMembership(tx, *r) {
		failRollout(r, "cluster_membership_changed", now)
	}
	if r.Status == "running" && !now.Before(r.Deadline) {
		failRollout(r, "rollout_deadline", now)
	}
	var err error
	if r.Status == "running" {
		err = advanceManagedForward(tx, r, all, now)
	} else {
		err = advanceManagedRollback(tx, r, all, now)
	}
	if err != nil {
		return err
	}
	if err = tx.save(*r); err != nil {
		return err
	}
	if r.Status != previous {
		return tx.audit(*r, "rollout."+r.Status)
	}
	return nil
}

func advanceManagedForward(tx *rolloutTransaction, r *domain.Rollout, all []rolloutObservation, now time.Time) error {
	if r.Index >= len(r.Nodes) {
		for i, o := range all {
			if !freshRolloutNode(o, now) || !domain.RolloutHealthy(o.state, r.Document, now) {
				slog.Warn("HAProxy rollout final health failed", "node_id", r.Nodes[i].NodeID, "reason", domain.RolloutHealthIssue(o.state, r.Document, now))
				failRollout(r, "cluster_health_failed", now)
				return nil
			}
			if ready, code := verifyRolloutApplications(&r.Nodes[i], o, false, "after_rejoin", now); !ready {
				if code != "" {
					r.Nodes[i].ErrorCode = code
					failRollout(r, code, now)
				}
				return nil
			}
		}
		mt := rolloutMaintenanceTransaction(tx, r, tx.nodes[0])
		controller, err := readMaintenanceControl(mt, r.OrganizationID, r.Entrypoint.NodeID)
		if err != nil || !maintenanceReady(controller, now) || controller.Discovery.Configuration.ArtifactID != r.Entrypoint.OwnedArtifactID || controller.Discovery.Configuration.Version != r.Entrypoint.OwnedVersion {
			failRollout(r, "entrypoint_unavailable", now)
			return nil
		}
		markRolloutTerminal(r, "succeeded", now)
		return nil
	}
	n := &r.Nodes[r.Index]
	o := all[r.Index]
	if n.Maintenance != nil && !n.Maintenance.Terminal() {
		if err := advanceRolloutMaintenance(tx, r, n.Maintenance, now); err != nil {
			return err
		}
		m := n.Maintenance
		if m.Status == "attention" {
			return handoffRolloutMaintenance(tx, r, n, m, m.ErrorCode, now)
		}
		if m.ErrorCode == "rejoin_failed" {
			failRollout(r, "health_check_failed", now)
			return nil
		}
	}
	fail := func(code string) { n.ErrorCode = code; failRollout(r, code, now) }
	switch n.Status {
	case "pending":
		n.Status = "waiting"
		n.Deadline = now.Add(20 * time.Second)
	case "waiting":
		if !freshRolloutNode(o, now) || !domain.RolloutHealthy(o.state, o.baseline.Document, now) || !peersHealthy(*r, all, r.Index, now) {
			if !now.Before(n.Deadline) {
				fail("preflight_health_failed")
			}
			break
		}
		c := o.state.Discovery.Configuration
		if c.ArtifactID != n.ExpectedArtifactID || c.Version != n.ExpectedVersion || o.state.Discovery.Generation != n.ExpectedGeneration {
			fail("configuration_changed")
			break
		}
		m, err := beginRolloutMaintenance(tx, r, *n, o, all, now)
		if errors.Is(err, domain.ErrConflict) {
			if !now.Before(n.Deadline) {
				fail("entrypoint_unavailable")
			}
			break
		}
		if err != nil {
			return err
		}
		n.Maintenance = m
		n.Status = "withdrawing"
		return tx.audit(*r, "rollout.node_withdrawing")
	case "withdrawing", "draining":
		m := n.Maintenance
		n.Status = m.Status
		if m.Status != "maintenance" {
			break
		}
		if !freshRolloutNode(o, now) || !peersHealthy(*r, all, r.Index, now) {
			fail("preflight_health_failed")
			break
		}
		c := o.state.Discovery.Configuration
		count, known := domain.FrontendConnections(o.state, m.Frontend, now)
		if c.ArtifactID != n.ExpectedArtifactID || c.Version != n.ExpectedVersion || o.state.Discovery.Generation != m.TargetGeneration || !known || count != 0 {
			fail("configuration_changed")
			break
		}
		if c.Document.Equal(r.Document) {
			n.AppliedArtifactID = c.ArtifactID
			n.Status = "checking"
			n.Deadline = now.Add(20 * time.Second)
			beginApplicationGate(n, false, "before_rejoin", now)
			break
		}
		op, err := queueRolloutConfiguration(tx, *r, *n, o, false)
		if errors.Is(err, domain.ErrConflict) {
			fail("configuration_changed")
			break
		}
		if err != nil {
			return err
		}
		n.ApplyOperationID = op.ID
		n.Status = "applying"
		n.Deadline = now.Add(50 * time.Second)
	case "applying":
		if o.err != nil {
			if !now.Before(n.Deadline) {
				fail("node_unavailable")
			}
			break
		}
		if o.operation.Status == "succeeded" {
			n.AppliedArtifactID = o.applied.ArtifactID
			n.Status = "checking"
			n.Deadline = now.Add(20 * time.Second)
			beginApplicationGate(n, false, "before_rejoin", now)
		} else if o.operation.Status == "failed" || (o.operation.Status == "unknown" && now.After(o.operation.Deadline.Add(3*time.Second))) {
			code := o.operation.ErrorCode
			if code == "" {
				code = "operation_failed"
			}
			fail(code)
		}
	case "checking", "checking_application", "rejoining", "verifying_application":
		m := n.Maintenance
		if freshRolloutNode(o, now) && o.state.Discovery.Configuration.ArtifactID != n.AppliedArtifactID {
			fail("configuration_changed")
			break
		}
		if m.Status == "completed" {
			n.Status = "verifying_application"
			if ready, code := verifyRolloutApplications(n, o, false, "after_rejoin", now); !ready {
				if code != "" {
					fail(code)
				}
				break
			}
			n.Status = "applied"
			r.Index++
			break
		}
		if m.Status == "maintenance" {
			if !freshRolloutNode(o, now) || !domain.RolloutHealthy(o.state, r.Document, now) || !peersHealthy(*r, all, r.Index, now) {
				if !now.Before(n.Deadline) {
					fail("health_check_failed")
				}
				break
			}
			if ready, code := verifyRolloutApplications(n, o, false, "before_rejoin", now); !ready {
				n.Status = "checking_application"
				if code != "" {
					fail(code)
				}
				break
			}
			requestRolloutRejoin(m, o, now)
			return tx.audit(*r, "rollout.node_checking_rejoin")
		}
		if m.Status == "rejoining" {
			n.Status = "rejoining"
		}
	}
	return nil
}

func advanceManagedRollback(tx *rolloutTransaction, r *domain.Rollout, all []rolloutObservation, now time.Time) error {
	if r.Index < 0 {
		return verifyRestoredCluster(tx, r, all, now)
	}
	n := &r.Nodes[r.Index]
	o := all[r.Index]
	m := rolloutMaintenance(n, true)
	if !now.Before(r.Deadline) || !now.Before(n.Deadline) {
		if o.err == nil {
			if err := cancelUnsentRolloutOperation(tx, *r, *n, o.operation, now); err != nil {
				return err
			}
		}
		return handoffRolloutMaintenance(tx, r, n, m, "rollback_deadline", now)
	}
	if n.ApplyOperationID == "" && (m == nil || m.Terminal()) {
		n.Status = "skipped"
		nextRollbackNode(r, now)
		return nil
	}
	if n.ApplyOperationID != "" && n.RollbackOperationID == "" && o.err == nil && o.operation.CommandDigest == "" {
		if err := cancelUnsentRolloutOperation(tx, *r, *n, o.operation, now); err != nil {
			return err
		}
	}
	if !freshRolloutNode(o, now) {
		n.Status = "waiting_for_rollback"
		return nil
	}
	if n.ApplyOperationID != "" && !operationQuiescent(o.operation, o, now) {
		return nil
	}
	// Previously rejoined nodes need a fresh withdrawal and drain before rollback.
	if m == nil || (m.Terminal() && n.RollbackMaintenance == nil && !n.RollbackRejoinStarted) {
		var err error
		m, err = beginRolloutMaintenance(tx, r, *n, o, all, now)
		if errors.Is(err, domain.ErrConflict) {
			n.Status = "waiting_for_rollback"
			return nil
		}
		if err != nil {
			return err
		}
		n.RollbackMaintenance = m
		n.Status = "withdrawing"
		return tx.audit(*r, "rollout.node_withdrawing")
	}
	if !m.Terminal() {
		if err := advanceRolloutMaintenance(tx, r, m, now); err != nil {
			return err
		}
		if m.Status == "attention" {
			return handoffRolloutMaintenance(tx, r, n, m, m.ErrorCode, now)
		}
		if m.ErrorCode == "rejoin_failed" && m.Status == "maintenance" {
			if n.RollbackRejoinStarted {
				return handoffRolloutMaintenance(tx, r, n, m, "rollback_health_failed", now)
			}
			m.ErrorCode = ""
		}
		if m.Status == "maintenance" && n.RollbackApplicationVerification != nil && n.RollbackApplicationVerification.Status == "failed" {
			return handoffRolloutMaintenance(tx, r, n, m, "rollback_application_verification_failed", now)
		}
	}
	if m.Status == "completed" {
		if !domain.RolloutHealthy(o.state, o.baseline.Document, now) {
			return handoffRolloutMaintenance(tx, r, n, nil, "rollback_health_failed", now)
		}
		n.AppliedArtifactID = o.state.Discovery.Configuration.ArtifactID
		if ready, code := verifyRolloutApplications(n, o, true, "after_rejoin", now); !ready {
			n.Status = "verifying_application_rollback"
			if code != "" {
				withdraw, err := beginRolloutMaintenance(tx, r, *n, o, all, now)
				if errors.Is(err, domain.ErrConflict) {
					return nil
				}
				if err != nil {
					return err
				}
				n.RollbackMaintenance = withdraw
			}
			return nil
		}
		n.Status = "rolled_back"
		n.AppliedArtifactID = o.state.Discovery.Configuration.ArtifactID
		nextRollbackNode(r, now)
		return nil
	}
	if m.Status == "withdrawing" || m.Status == "draining" {
		n.Status = m.Status
		return nil
	}
	if m.Status == "checking_rejoin" || m.Status == "rejoining" {
		n.Status = "rejoining_rollback"
		return nil
	}
	if m.Status != "maintenance" {
		return nil
	}
	c := o.state.Discovery.Configuration
	if n.RollbackOperationID != "" {
		if o.operation.Status != "succeeded" {
			return handoffRolloutMaintenance(tx, r, n, m, "rollback_operation_failed", now)
		}
		if c.ArtifactID != o.applied.ArtifactID {
			return handoffRolloutMaintenance(tx, r, n, m, "configuration_changed", now)
		}
	} else if c.ArtifactID != n.ExpectedArtifactID {
		if n.AppliedArtifactID != "" && c.ArtifactID != n.AppliedArtifactID {
			return handoffRolloutMaintenance(tx, r, n, m, "configuration_changed", now)
		}
		if n.AppliedArtifactID == "" && (c.Version != n.ExpectedVersion+1 || (!c.Document.Equal(r.Document) && !c.Document.Equal(o.baseline.Document))) {
			return handoffRolloutMaintenance(tx, r, n, m, "configuration_changed", now)
		}
		count, known := domain.FrontendConnections(o.state, m.Frontend, now)
		if !known || count != 0 {
			return nil
		}
		op, err := queueRolloutConfiguration(tx, *r, *n, o, true)
		if errors.Is(err, domain.ErrConflict) {
			return handoffRolloutMaintenance(tx, r, n, m, "configuration_changed", now)
		}
		if err != nil {
			return err
		}
		n.RollbackOperationID = op.ID
		n.Status = "rolling_back"
		n.Deadline = now.Add(90 * time.Second)
		return nil
	}
	n.Status = "checking_rollback"
	if domain.RolloutHealthy(o.state, o.baseline.Document, now) && peersHealthy(*r, all, r.Index, now) {
		if ready, code := verifyRolloutApplications(n, o, true, "before_rejoin", now); !ready {
			if code != "" {
				return handoffRolloutMaintenance(tx, r, n, m, "rollback_application_verification_failed", now)
			}
			return nil
		}
		n.RollbackRejoinStarted = true
		requestRolloutRejoin(m, o, now)
		return tx.audit(*r, "rollout.node_checking_rejoin")
	}
	return nil
}
