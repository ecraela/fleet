package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed maintenance_schema.sql
var maintenanceSchemaSQL string

type MaintenanceStore interface {
	GetMaintenance(context.Context, string, string) (domain.MaintenanceView, error)
	ChangeMaintenance(context.Context, string, string, domain.MaintenanceInput) (domain.Maintenance, error)
	ListMaintenance(context.Context, string, string) ([]domain.Maintenance, error)
	AdvanceMaintenance(context.Context) error
}
type maintenanceWriterKey struct{}
type maintenanceRequest struct{ ID, OrganizationID, MaintenanceID, ClusterID, Digest string }
type maintenancePersistence interface {
	controlPersistence
	maintenanceTransaction(context.Context, string, string, func(*maintenanceTransaction) error) error
	activeMaintenance(context.Context) ([]domain.Maintenance, error)
}
type maintenanceTransaction struct {
	ctx                context.Context
	persistence        controlPersistence
	node, controller   domain.Node
	cluster            domain.Cluster
	nodes              []domain.Node
	history            []domain.Maintenance
	rolloutMaintenance *domain.Maintenance
	peerArtifact       func(string) string
	get                func(string) (domain.Maintenance, error)
	save               func(domain.Maintenance) error
	request            func(string) (maintenanceRequest, error)
	saveRequest        func(maintenanceRequest) error
	reserve            func() error
	admitWork          func(string) error
	audit              func(domain.Maintenance, string) error
}

func cloneMaintenance(m domain.Maintenance) domain.Maintenance {
	m.Baseline = m.Baseline.Canonical()
	c := cloneCluster(domain.Cluster{Topology: &m.Topology})
	m.Topology = *c.Topology
	if m.Connections != nil {
		v := *m.Connections
		m.Connections = &v
	}
	if m.CompletedAt != nil {
		v := *m.CompletedAt
		m.CompletedAt = &v
	}
	return m
}
func (m *Memory) stageControlMemory() *Memory {
	s := &Memory{nodes: m.nodes, clusters: m.clusters, controls: map[string]domain.NodeControl{}, operations: map[string]domain.Operation{}, revisions: map[string]domain.ConfigurationRevision{}, rollouts: map[string]domain.Rollout{}, maintenances: map[string]domain.Maintenance{}, maintenanceRequests: map[string]maintenanceRequest{}, audit: append([]auditEvent{}, m.audit...)}
	s.incidents = map[string]incidentRecord{}
	for id, r := range m.incidents {
		r.Incident = cloneIncident(r.Incident)
		s.incidents[id] = r
	}
	s.incidentEvaluations = map[string]int64{}
	for id, at := range m.incidentEvaluations {
		s.incidentEvaluations[id] = at
	}
	s.incidentEvents = append([]domain.IncidentEvent{}, m.incidentEvents...)
	s.incidentSequence = m.incidentSequence
	s.notificationDestinations = map[string]notificationDestination{}
	for id, r := range m.notificationDestinations {
		s.notificationDestinations[id] = r
	}
	s.incidentDeliveries = map[string]notificationRecord{}
	for key, r := range m.incidentDeliveries {
		r.Event.Incident = cloneIncident(r.Event.Incident)
		s.incidentDeliveries[key] = r
	}
	for id, c := range m.controls {
		c.Discovery = cloneDiscovery(c.Discovery)
		s.controls[id] = c
	}
	for id, o := range m.operations {
		s.operations[id] = cloneOperation(o)
	}
	for id, r := range m.revisions {
		s.revisions[id] = cloneRevision(r)
	}
	for id, r := range m.rollouts {
		s.rollouts[id] = cloneRollout(r)
	}
	for id, r := range m.maintenances {
		s.maintenances[id] = cloneMaintenance(r)
	}
	for id, r := range m.maintenanceRequests {
		s.maintenanceRequests[id] = r
	}
	s.acmePlans = map[string]domain.ACMEPlan{}
	for id, r := range m.acmePlans {
		s.acmePlans[id] = cloneACME(r)
	}
	s.applicationRevisions = map[string]domain.ApplicationRevision{}
	s.listenerRevisions = map[string]domain.ListenerRevision{}
	for id, r := range m.listenerRevisions {
		s.listenerRevisions[id] = cloneListenerRevision(r)
	}
	for id, r := range m.applicationRevisions {
		s.applicationRevisions[id] = cloneApplicationRevision(r)
	}
	return s
}
func (m *Memory) commitControlMemory(s *Memory) {
	m.incidents, m.incidentEvaluations, m.incidentEvents, m.incidentSequence = s.incidents, s.incidentEvaluations, s.incidentEvents, s.incidentSequence
	m.notificationDestinations, m.incidentDeliveries = s.notificationDestinations, s.incidentDeliveries
	m.acmePlans = s.acmePlans
	m.applicationRevisions = s.applicationRevisions
	m.listenerRevisions = s.listenerRevisions
	m.controls, m.operations, m.revisions, m.rollouts, m.maintenances, m.maintenanceRequests, m.audit = s.controls, s.operations, s.revisions, s.rollouts, s.maintenances, s.maintenanceRequests, s.audit
}
func maintenanceGuard(r domain.Maintenance, node domain.Node) (bool, bool) {
	involved := !r.Terminal() && (r.ClusterID == node.ClusterID || r.ControllerClusterID == node.ClusterID)
	blocked := involved && !(r.NodeID == node.ID && r.Status == "maintenance")
	return involved, blocked
}
func (m *Memory) setMaintenanceGuard(tx *controlTransaction) {
	for _, r := range m.maintenances {
		if involved, blocked := maintenanceGuard(r, tx.node); involved {
			tx.state.MaintenanceID = r.ID
			tx.state.MaintenanceBlocked = blocked
			return
		}
	}
	tx.state.MaintenanceID = ""
	tx.state.MaintenanceBlocked = false
}
func (m *Memory) maintenanceClusterBusy(org, cluster string) bool {
	for _, r := range m.maintenances {
		if r.OrganizationID == org && !r.Terminal() && (r.ClusterID == cluster || r.ControllerClusterID == cluster) {
			return true
		}
	}
	return false
}
func maintenanceWriter(ctx context.Context, tx *controlTransaction) bool {
	id, _ := ctx.Value(maintenanceWriterKey{}).(string)
	return !tx.state.MaintenanceBlocked || id == tx.state.MaintenanceID
}
func (m *Memory) maintenanceTransaction(ctx context.Context, org, node string, fn func(*maintenanceTransaction) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !domain.ValidID(org) || !domain.ValidID(node) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[node]
	if !ok || n.OrganizationID != org {
		return domain.ErrNotFound
	}
	c, ok := m.clusters[n.ClusterID]
	if !ok || c.OrganizationID != org {
		return domain.ErrNotFound
	}
	s := m.stageControlMemory()
	tx := &maintenanceTransaction{ctx: context.WithValue(ctx, memoryRolloutTxKey{}, s), persistence: m, node: n, cluster: cloneCluster(c)}
	if c.Topology != nil && c.Topology.Control != nil {
		controller, ok := m.nodes[c.Topology.Control.NodeID]
		if !ok || controller.OrganizationID != org || controller.ClusterID == c.ID {
			return domain.ErrNotFound
		}
		tx.controller = controller
	}
	for _, other := range m.nodes {
		if other.OrganizationID == org && other.ClusterID == c.ID && other.RevokedAt == nil {
			tx.nodes = append(tx.nodes, other)
		}
	}
	tx.get = func(id string) (domain.Maintenance, error) {
		r, ok := s.maintenances[id]
		if !ok || r.OrganizationID != org || r.NodeID != node {
			return r, domain.ErrNotFound
		}
		return cloneMaintenance(r), nil
	}
	tx.save = func(r domain.Maintenance) error { s.maintenances[r.ID] = cloneMaintenance(r); return nil }
	tx.request = func(id string) (maintenanceRequest, error) {
		r, ok := s.maintenanceRequests[org+"/"+id]
		if !ok {
			return r, domain.ErrNotFound
		}
		return r, nil
	}
	tx.saveRequest = func(r maintenanceRequest) error { s.maintenanceRequests[org+"/"+r.ID] = r; return nil }
	for _, r := range s.maintenances {
		if r.OrganizationID == org && r.NodeID == node {
			tx.history = append(tx.history, cloneMaintenance(r))
		}
	}
	for _, r := range s.rollouts {
		if r.OrganizationID == org && r.ClusterID == c.ID && !r.Terminal() {
			tx.rolloutMaintenance = maintenanceForRolloutNode(r, node)
		}
	}
	sort.Slice(tx.history, func(i, j int) bool { return tx.history[i].CreatedAt.After(tx.history[j].CreatedAt) })
	if len(tx.history) > 20 {
		tx.history = tx.history[:20]
	}
	tx.admitWork = func(exclude string) error { return s.admitWork(org, exclude) }
	tx.reserve = func() error {
		count := 0
		for _, r := range s.maintenances {
			if !r.Terminal() && r.OrganizationID == org {
				count++
				if r.ClusterID == c.ID || r.ControllerClusterID == c.ID || r.ClusterID == tx.controller.ClusterID || r.ControllerClusterID == tx.controller.ClusterID {
					return domain.ErrConflict
				}
			}
		}
		if count >= maxTenantMaintenanceReservations || s.activeRollout(org, c.ID) != "" || s.activeRollout(org, tx.controller.ClusterID) != "" {
			return domain.ErrConflict
		}
		return tx.admitWork("")
	}
	tx.audit = func(r domain.Maintenance, action string) error {
		s.audit = append(s.audit, auditEvent{maintenanceID: r.ID, actorUserID: r.ActorUserID, organizationID: org, clusterID: c.ID, nodeID: node, action: action, createdAt: r.UpdatedAt})
		return nil
	}
	if err := fn(tx); err != nil {
		return err
	}
	m.commitControlMemory(s)
	return nil
}
func (p *Postgres) maintenanceTransaction(ctx context.Context, org, node string, fn func(*maintenanceTransaction) error) error {
	if !domain.ValidID(org) || !domain.ValidID(node) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(db pgx.Tx) error {
		var n domain.Node
		if err := db.QueryRow(ctx, `SELECT id::text,organization_id::text,cluster_id::text,name,revoked_at FROM hm_nodes WHERE organization_id=$1 AND id=$2`, org, node).Scan(&n.ID, &n.OrganizationID, &n.ClusterID, &n.Name, &n.RevokedAt); err != nil {
			return err
		}
		var c domain.Cluster
		if err := db.QueryRow(ctx, `SELECT id::text,organization_id::text,name,created_at,topology FROM hm_clusters WHERE organization_id=$1 AND id=$2`, org, n.ClusterID).Scan(&c.ID, &c.OrganizationID, &c.Name, &c.CreatedAt, &c.Topology); err != nil {
			return err
		}
		var controller domain.Node
		original := ""
		if c.Topology != nil && c.Topology.Control != nil {
			original = c.Topology.Control.NodeID
			if err := db.QueryRow(ctx, `SELECT id::text,organization_id::text,cluster_id::text,name,revoked_at FROM hm_nodes WHERE organization_id=$1 AND id=$2`, org, original).Scan(&controller.ID, &controller.OrganizationID, &controller.ClusterID, &controller.Name, &controller.RevokedAt); err != nil {
				return err
			}
			if controller.ClusterID == c.ID {
				return domain.ErrInvalidInput
			}
		}
		// Every multi-cluster coordinator locks clusters in the same UUID order,
		// before any node row. Ordinary control transactions acquire only one.
		ids := []string{c.ID}
		if controller.ID != "" {
			ids = append(ids, controller.ClusterID)
		}
		sort.Strings(ids)
		for _, id := range ids {
			var locked string
			if err := db.QueryRow(ctx, `SELECT id::text FROM hm_clusters WHERE organization_id=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&locked); err != nil {
				return err
			}
		}
		if err := db.QueryRow(ctx, `SELECT topology FROM hm_clusters WHERE organization_id=$1 AND id=$2`, org, c.ID).Scan(&c.Topology); err != nil {
			return err
		}
		current := ""
		if c.Topology != nil && c.Topology.Control != nil {
			current = c.Topology.Control.NodeID
		}
		if current != original {
			return domain.ErrConflict
		}
		tx := &maintenanceTransaction{ctx: context.WithValue(ctx, postgresRolloutTxKey{}, db), persistence: p, node: n, controller: controller, cluster: c}
		rows, err := db.Query(ctx, `SELECT id::text,name FROM hm_nodes WHERE organization_id=$1 AND cluster_id=$2 AND revoked_at IS NULL ORDER BY name,id`, org, c.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			other := domain.Node{OrganizationID: org, ClusterID: c.ID}
			if err = rows.Scan(&other.ID, &other.Name); err != nil {
				rows.Close()
				return err
			}
			tx.nodes = append(tx.nodes, other)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		tx.get = func(id string) (domain.Maintenance, error) {
			var r domain.Maintenance
			var raw []byte
			if err := db.QueryRow(ctx, `SELECT payload FROM hm_maintenance WHERE organization_id=$1 AND node_id=$2 AND id=$3`, org, node, id).Scan(&raw); err != nil {
				return r, normalizeError(err)
			}
			err := json.Unmarshal(raw, &r)
			return r, err
		}
		tx.save = func(r domain.Maintenance) error {
			raw, err := json.Marshal(r)
			if err != nil {
				return err
			}
			_, err = db.Exec(ctx, `INSERT INTO hm_maintenance(id,organization_id,cluster_id,node_id,controller_cluster_id,controller_node_id,status,created_at,updated_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO UPDATE SET status=excluded.status,updated_at=excluded.updated_at,payload=excluded.payload`, r.ID, org, c.ID, node, r.ControllerClusterID, r.ControllerNodeID, r.Status, r.CreatedAt, r.UpdatedAt, raw)
			return err
		}
		tx.request = func(id string) (maintenanceRequest, error) {
			r := maintenanceRequest{ID: id, OrganizationID: org}
			err := db.QueryRow(ctx, `SELECT maintenance_id::text,cluster_id::text,digest FROM hm_maintenance_requests WHERE organization_id=$1 AND request_id=$2`, org, id).Scan(&r.MaintenanceID, &r.ClusterID, &r.Digest)
			return r, normalizeError(err)
		}
		tx.saveRequest = func(r maintenanceRequest) error {
			_, err := db.Exec(ctx, `INSERT INTO hm_maintenance_requests(request_id,organization_id,maintenance_id,cluster_id,digest) VALUES($1,$2,$3,$4,$5)`, r.ID, org, r.MaintenanceID, c.ID, r.Digest)
			return err
		}
		rows, err = db.Query(ctx, `SELECT payload FROM hm_maintenance WHERE organization_id=$1 AND node_id=$2 ORDER BY created_at DESC LIMIT 20`, org, node)
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw []byte
			var r domain.Maintenance
			if err = rows.Scan(&raw); err == nil {
				err = json.Unmarshal(raw, &r)
			}
			if err != nil {
				rows.Close()
				return err
			}
			tx.history = append(tx.history, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		var rolloutRaw []byte
		err = db.QueryRow(ctx, `SELECT payload FROM hm_rollouts WHERE organization_id=$1 AND cluster_id=$2 AND status IN ('running','rolling_back') LIMIT 1`, org, c.ID).Scan(&rolloutRaw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			var rollout domain.Rollout
			if err = json.Unmarshal(rolloutRaw, &rollout); err != nil {
				return err
			}
			tx.rolloutMaintenance = maintenanceForRolloutNode(rollout, node)
		}
		tx.admitWork = func(exclude string) error { return admitWork(ctx, db, org, exclude) }
		tx.reserve = func() error {
			if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(492584126125)`); err != nil {
				return err
			}
			var count int
			var own bool
			if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE organization_id=$1),COALESCE(bool_or(organization_id=$1 AND (cluster_id=ANY($2::uuid[]) OR controller_cluster_id=ANY($2::uuid[]))),false) FROM hm_maintenance WHERE status NOT IN ('completed','failed')`, org, ids).Scan(&count, &own); err != nil {
				return err
			}
			if count >= maxTenantMaintenanceReservations || own {
				return domain.ErrConflict
			}
			if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_rollouts WHERE organization_id=$1 AND (cluster_id=ANY($2::uuid[]) OR controller_cluster_id=ANY($2::uuid[])) AND status IN ('running','rolling_back'))`, org, ids).Scan(&own); err != nil {
				return err
			}
			if own {
				return domain.ErrConflict
			}
			return tx.admitWork("")
		}
		tx.audit = func(r domain.Maintenance, action string) error {
			_, err := db.Exec(ctx, `INSERT INTO hm_audit_events(id,organization_id,cluster_id,node_id,action,actor_user_id,maintenance_id,created_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8)`, domain.NewID(), org, c.ID, node, action, r.ActorUserID, r.ID, r.UpdatedAt)
			return err
		}
		return fn(tx)
	})
}
func readMaintenanceControl(tx *maintenanceTransaction, org, node string) (domain.NodeControl, error) {
	var out domain.NodeControl
	err := tx.persistence.controlTransaction(tx.ctx, org, node, func(c *controlTransaction) error {
		if err := expirePending(c); err != nil {
			return err
		}
		out = c.state
		return nil
	})
	return out, err
}
func maintenanceView(tx *maintenanceTransaction, org string) (domain.MaintenanceView, error) {
	view := domain.MaintenanceView{NodeID: tx.node.ID, ClusterID: tx.cluster.ID, ReasonCode: "entrypoint_not_managed"}
	if tx.node.RevokedAt != nil {
		return view, domain.ErrRevoked
	}
	var err error
	view.Target, err = readMaintenanceControl(tx, org, tx.node.ID)
	if err != nil {
		return view, err
	}
	if len(tx.history) > 0 {
		r := cloneMaintenance(tx.history[0])
		view.Current = &r
	}
	if tx.rolloutMaintenance != nil {
		copy := cloneMaintenance(*tx.rolloutMaintenance)
		view.Current = &copy
	}
	t := tx.cluster.Topology
	if t == nil || t.Control == nil {
		return view, nil
	}
	view.Frontend = t.Frontend
	view.BindingID = t.BindingID()
	view.Entrypoint = t.Entrypoint
	view.Backend = t.Control.Backend
	view.ControllerNodeID = tx.controller.ID
	view.ControllerName = tx.controller.Name
	for _, s := range t.Control.Servers {
		if s.NodeID == tx.node.ID {
			view.Server = s.Server
		}
	}
	if view.Server == "" {
		return view, nil
	}
	view.Controller, err = readMaintenanceControl(tx, org, tx.controller.ID)
	if err != nil {
		view.ReasonCode = "entrypoint_unavailable"
		return view, nil
	}
	view.ReasonCode = ""
	return view, nil
}
func getMaintenance(ctx context.Context, p maintenancePersistence, org, node string) (domain.MaintenanceView, error) {
	var out domain.MaintenanceView
	err := p.maintenanceTransaction(ctx, org, node, func(tx *maintenanceTransaction) error { var err error; out, err = maintenanceView(tx, org); return err })
	return out, err
}
func maintenanceReady(s domain.NodeControl, now time.Time) bool {
	return s.Discovery != nil && s.Discovery.Configuration != nil && s.Discovery.Configuration.Document != nil && domain.RolloutHealthy(s, *s.Discovery.Configuration.Document, now)
}
func maintenancePeers(tx *maintenanceTransaction, org, target string, now time.Time) bool {
	if tx.cluster.Topology == nil || len(tx.cluster.Topology.Nodes) != len(tx.nodes) || len(tx.nodes) < 2 || len(tx.nodes) > 12 {
		return false
	}
	count := 0
	for _, n := range tx.nodes {
		found := false
		for _, binding := range tx.cluster.Topology.Nodes {
			found = found || binding.NodeID == n.ID
		}
		if !found {
			return false
		}
		if n.ID == target {
			continue
		}
		s, err := readMaintenanceControl(tx, org, n.ID)
		if err != nil || !maintenanceReady(s, now) || s.PendingOperationID != "" {
			return false
		}
		if tx.peerArtifact != nil {
			if expected := tx.peerArtifact(n.ID); expected != "" && s.Discovery.Configuration.ArtifactID != expected {
				return false
			}
		}
		count++
	}
	return count > 0
}
func changeMaintenance(ctx context.Context, p maintenancePersistence, org, node string, input domain.MaintenanceInput) (domain.Maintenance, error) {
	var out domain.Maintenance
	if input.DrainTimeoutSeconds == 0 {
		input.DrainTimeoutSeconds = 30
	}
	if !input.Valid() {
		return out, domain.ErrInvalidInput
	}
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	err := p.maintenanceTransaction(ctx, org, node, func(tx *maintenanceTransaction) error {
		if req, err := tx.request(input.RequestID); err == nil {
			if req.Digest != digest || req.ClusterID != tx.cluster.ID {
				return domain.ErrConflict
			}
			var err error
			out, err = tx.get(req.MaintenanceID)
			return err
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		v, err := maintenanceView(tx, org)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if v.ReasonCode != "" || v.BindingID != input.BindingID || !configurationEnabled(v.Controller, now) || !managementEnabled(v.Controller, now) || v.Controller.PendingOperationID != "" || v.Target.Discovery == nil || !v.Target.Discovery.Fresh(now) || v.Target.Discovery.Generation != input.ExpectedGeneration || v.Target.PendingOperationID != "" {
			return domain.ErrConflict
		}
		d := v.Controller.Discovery
		if d.Generation != input.ExpectedEntrypointGeneration || d.Configuration.ArtifactID != input.ExpectedArtifactID || d.Configuration.Version != input.ExpectedVersion {
			return domain.ErrConflict
		}
		if _, ok := domain.FrontendConnections(v.Target, tx.cluster.Topology.Frontend, now); !ok {
			return domain.ErrConflict
		}
		target, ok := d.Server(v.Backend, v.Server)
		if !ok {
			return domain.ErrConflict
		}
		if input.MaintenanceID == "" {
			if input.Action != "withdraw" {
				return domain.ErrInvalidInput
			}
			if err := tx.reserve(); err != nil {
				return err
			}
			if !maintenanceReady(v.Target, now) || !maintenanceReady(v.Controller, now) || !maintenancePeers(tx, org, node, now) || target.State != "ready" || target.Weight == 0 {
				return domain.ErrConflict
			}
			// Every route in this managed backend has an explicit target identity.
			mapped := map[string]bool{}
			for _, s := range tx.cluster.Topology.Control.Servers {
				mapped[s.Server] = true
			}
			for _, s := range d.Servers {
				if s.Backend == v.Backend && !mapped[s.Name] {
					return domain.ErrConflict
				}
			}
			baseline := d.Configuration.Document.Canonical()
			for _, b := range baseline.Backends {
				if b.Name == v.Backend {
					for _, s := range b.Servers {
						if s.Name == v.Server && (s.Maintenance || s.Weight == 0) {
							return domain.ErrConflict
						}
					}
				}
			}
			out = domain.Maintenance{ID: domain.NewID(), OrganizationID: org, ClusterID: tx.cluster.ID, NodeID: node, ControllerNodeID: tx.controller.ID, ControllerClusterID: tx.controller.ClusterID, ActorUserID: domain.Actor(ctx), BindingID: v.BindingID, Backend: v.Backend, Server: v.Server, Address: domain.MaintenanceAddress(baseline, v.Backend, v.Server), Frontend: tx.cluster.Topology.Frontend, Topology: *cloneCluster(tx.cluster).Topology, Baseline: baseline, OwnedArtifactID: d.Configuration.ArtifactID, OwnedVersion: d.Configuration.Version, TargetGeneration: v.Target.Discovery.Generation, Status: "withdrawing", Step: "runtime", DrainTimeoutSeconds: input.DrainTimeoutSeconds, CreatedAt: now, UpdatedAt: now, Deadline: now.Add(2 * time.Minute)}
		} else {
			out, err = tx.get(input.MaintenanceID)
			if err != nil {
				return err
			}
			if out.Terminal() || (out.Status != "maintenance" && out.Status != "attention") {
				return domain.ErrConflict
			}
			if out.OperationID != "" {
				o, e := maintenanceOperation(tx, out)
				if e != nil || !operationQuiescent(o, rolloutObservation{state: v.Controller}, now) {
					return domain.ErrConflict
				}
				// A rollout may hand off while an entrypoint receipt is still in
				// flight. Only its verified success may advance artifact ownership.
				if o.Status == "succeeded" {
					done, success, e := settleMaintenanceOperation(tx, &out, v.Controller, now)
					if e != nil {
						return e
					}
					if !done || !success {
						return domain.ErrConflict
					}
				}
			}
			if d.Configuration.ArtifactID != out.OwnedArtifactID || domain.MaintenanceAddress(*d.Configuration.Document, out.Backend, out.Server) != out.Address {
				return domain.ErrConflict
			}
			if err := tx.admitWork(out.ID); err != nil {
				return err
			}
			// An operator may have restored the exact owned artifact through DPA,
			// advancing only its version counter. A fresh, explicit retry can adopt
			// that counter; changed content and stale request versions stay fenced.
			out.OwnedVersion = d.Configuration.Version
			out.ActorUserID = domain.Actor(ctx)
			out.ErrorCode = ""
			out.OperationID = ""
			out.HealthSinceMS = 0
			out.LastHealthMS = 0
			out.LastObservationMS = 0
			out.TargetGeneration = v.Target.Discovery.Generation
			out.TargetArtifactID = ""
			if v.Target.Discovery.Configuration != nil {
				out.TargetArtifactID = v.Target.Discovery.Configuration.ArtifactID
			}
			out.UpdatedAt = now
			out.Deadline = now.Add(2 * time.Minute)
			if input.Action == "rejoin" {
				if !maintenanceReady(v.Target, now) || !maintenancePeers(tx, org, node, now) {
					return domain.ErrConflict
				}
				out.Status = "checking_rejoin"
				out.Step = "health"
			} else {
				out.Status = "withdrawing"
				out.Step = "runtime"
				out.DrainTimeoutSeconds = input.DrainTimeoutSeconds
			}
		}
		if err := tx.save(out); err != nil {
			return err
		}
		if err := tx.saveRequest(maintenanceRequest{ID: input.RequestID, OrganizationID: org, MaintenanceID: out.ID, ClusterID: out.ClusterID, Digest: digest}); err != nil {
			return err
		}
		return tx.audit(out, "maintenance.requested")
	})
	return cloneMaintenance(out), err
}
func (m *Memory) GetMaintenance(ctx context.Context, org, node string) (domain.MaintenanceView, error) {
	return getMaintenance(ctx, m, org, node)
}
func (p *Postgres) GetMaintenance(ctx context.Context, org, node string) (domain.MaintenanceView, error) {
	return getMaintenance(ctx, p, org, node)
}
func (m *Memory) ChangeMaintenance(ctx context.Context, org, node string, in domain.MaintenanceInput) (domain.Maintenance, error) {
	return changeMaintenance(ctx, m, org, node, in)
}
func (p *Postgres) ChangeMaintenance(ctx context.Context, org, node string, in domain.MaintenanceInput) (domain.Maintenance, error) {
	return changeMaintenance(ctx, p, org, node, in)
}
func (m *Memory) ListMaintenance(ctx context.Context, org, cluster string) ([]domain.Maintenance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !domain.ValidID(org) || !domain.ValidID(cluster) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clusters[cluster]
	if !ok || c.OrganizationID != org {
		return nil, domain.ErrNotFound
	}
	out := []domain.Maintenance{}
	for _, r := range m.maintenances {
		if r.OrganizationID == org && r.ClusterID == cluster {
			out = append(out, cloneMaintenance(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > 20 {
		out = out[:20]
	}
	return out, nil
}
func (p *Postgres) ListMaintenance(ctx context.Context, org, cluster string) ([]domain.Maintenance, error) {
	if _, err := p.GetCluster(ctx, org, cluster); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT payload FROM hm_maintenance WHERE organization_id=$1 AND cluster_id=$2 ORDER BY created_at DESC LIMIT 20`, org, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Maintenance{}
	for rows.Next() {
		var raw []byte
		var r domain.Maintenance
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (m *Memory) activeMaintenance(ctx context.Context) ([]domain.Maintenance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runnable, idle := []domain.Maintenance{}, []domain.Maintenance{}
	for _, r := range m.maintenances {
		if maintenanceRunnable(r) {
			runnable = append(runnable, cloneMaintenance(r))
		} else if !r.Terminal() {
			idle = append(idle, cloneMaintenance(r))
		}
	}
	page := func(rows []domain.Maintenance) []domain.Maintenance {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
				return rows[i].ID < rows[j].ID
			}
			return rows[i].UpdatedAt.Before(rows[j].UpdatedAt)
		})
		if len(rows) > maxRunnableWork {
			rows = rows[:maxRunnableWork]
		}
		return rows
	}
	return append(page(runnable), page(idle)...), ctx.Err()
}
func (p *Postgres) activeMaintenance(ctx context.Context) ([]domain.Maintenance, error) {
	// Runnable health/drain windows need every tick. Rotate idle safety checks
	// separately so retained fences cannot stretch those windows past freshness.
	rows, err := p.pool.Query(ctx, `(SELECT payload FROM hm_maintenance WHERE status NOT IN ('completed','failed','maintenance','attention') ORDER BY updated_at,id LIMIT 16)
 UNION ALL
 (SELECT payload FROM hm_maintenance WHERE status IN ('maintenance','attention') ORDER BY updated_at,id LIMIT 16)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Maintenance{}
	for rows.Next() {
		var raw []byte
		var r domain.Maintenance
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
