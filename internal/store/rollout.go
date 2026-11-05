package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed rollout_schema.sql
var rolloutSchemaSQL string

//go:embed rollout_maintenance_schema.sql
var rolloutMaintenanceSchemaSQL string

type RolloutStore interface {
	CreateRollout(context.Context, string, string, domain.RolloutInput) (domain.Rollout, error)
	ListRollouts(context.Context, string, string) ([]domain.Rollout, error)
	AdvanceRollouts(context.Context) error
}
type memoryRolloutTxKey struct{}
type postgresRolloutTxKey struct{}
type rolloutWriterKey struct{}

func rolloutWriter(ctx context.Context, tx *controlTransaction) bool {
	id, _ := ctx.Value(rolloutWriterKey{}).(string)
	return (tx.activeRolloutID == "" || id == tx.activeRolloutID) && maintenanceWriter(ctx, tx)
}

type rolloutTransaction struct {
	incidents               incidentTransaction
	acmePlan                func() (domain.ACMEPlan, error)
	saveACME                func(domain.ACMEPlan) error
	auditACME               func(domain.ACMEPlan, string) error
	ctx                     context.Context
	persistence             controlPersistence
	nodes                   []domain.Node
	cluster                 domain.Cluster
	controller              domain.Node
	get                     func(string, bool) (domain.Rollout, error)
	list                    func() ([]domain.Rollout, error)
	save                    func(domain.Rollout) error
	reserve                 func() error
	audit                   func(domain.Rollout, string) error
	handoff                 func(domain.Maintenance) error
	recoveries              func([]string) (map[string]domain.Maintenance, error)
	applicationRevisions    func() ([]domain.ApplicationRevision, error)
	saveApplicationRevision func(domain.ApplicationRevision) error
	listenerRevisions       func() ([]domain.ListenerRevision, error)
	saveListenerRevision    func(domain.ListenerRevision) error
}
type rolloutPersistence interface {
	controlPersistence
	rolloutTransaction(context.Context, string, string, func(*rolloutTransaction) error) error
	activeRollouts(context.Context) ([]domain.Rollout, error)
}

func cloneRollout(r domain.Rollout) domain.Rollout {
	r.Document = r.Document.Canonical()
	r.Nodes = append([]domain.RolloutNode{}, r.Nodes...)
	for i := range r.Nodes {
		if r.Nodes[i].Maintenance != nil {
			v := cloneMaintenance(*r.Nodes[i].Maintenance)
			r.Nodes[i].Maintenance = &v
		}
		if r.Nodes[i].RollbackMaintenance != nil {
			v := cloneMaintenance(*r.Nodes[i].RollbackMaintenance)
			r.Nodes[i].RollbackMaintenance = &v
		}
	}
	if r.Entrypoint != nil {
		v := *r.Entrypoint
		v.Baseline = v.Baseline.Canonical()
		v.Topology = *cloneCluster(domain.Cluster{Topology: &v.Topology}).Topology
		r.Entrypoint = &v
	}
	if r.CompletedAt != nil {
		at := *r.CompletedAt
		r.CompletedAt = &at
	}
	return r
}
func (m *Memory) activeRollout(org, cluster string) string {
	for _, r := range m.rollouts {
		if r.OrganizationID == org && (r.ClusterID == cluster || (r.Entrypoint != nil && r.Entrypoint.ClusterID == cluster)) && !r.Terminal() {
			return r.ID
		}
	}
	return ""
}
func (m *Memory) rolloutTransaction(ctx context.Context, org, cluster string, fn func(*rolloutTransaction) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !domain.ValidID(org) || !domain.ValidID(cluster) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clusters[cluster]
	if !ok || c.OrganizationID != org {
		return domain.ErrNotFound
	}
	staged := m.stageControlMemory()
	scoped := context.WithValue(ctx, memoryRolloutTxKey{}, staged)
	tx := &rolloutTransaction{ctx: scoped, persistence: m, cluster: cloneCluster(c)}
	if c.Topology != nil && c.Topology.Control != nil {
		n, ok := m.nodes[c.Topology.Control.NodeID]
		if !ok || n.OrganizationID != org || n.ClusterID == cluster {
			return domain.ErrNotFound
		}
		tx.controller = n
	}
	for _, n := range m.nodes {
		if n.OrganizationID == org && n.ClusterID == cluster && n.RevokedAt == nil {
			tx.nodes = append(tx.nodes, n)
		}
	}
	sort.Slice(tx.nodes, func(i, j int) bool {
		if tx.nodes[i].Name == tx.nodes[j].Name {
			return tx.nodes[i].ID < tx.nodes[j].ID
		}
		return tx.nodes[i].Name < tx.nodes[j].Name
	})
	tx.get = func(id string, request bool) (domain.Rollout, error) {
		for _, r := range staged.rollouts {
			if r.OrganizationID == org && ((request && r.RequestID == id) || (!request && r.ID == id && r.ClusterID == cluster)) {
				return cloneRollout(r), nil
			}
		}
		return domain.Rollout{}, domain.ErrNotFound
	}
	tx.list = func() ([]domain.Rollout, error) {
		all := []domain.Rollout{}
		for _, r := range staged.rollouts {
			if r.OrganizationID == org && r.ClusterID == cluster {
				all = append(all, cloneRollout(r))
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
		if len(all) > 20 {
			all = all[:20]
		}
		return all, nil
	}
	tx.save = func(r domain.Rollout) error { staged.rollouts[r.ID] = cloneRollout(r); return nil }
	tx.reserve = func() error {
		if staged.maintenanceClusterBusy(org, cluster) || (tx.controller.ID != "" && staged.maintenanceClusterBusy(org, tx.controller.ClusterID)) {
			return domain.ErrConflict
		}
		if staged.activeRollout(org, cluster) != "" || (tx.controller.ID != "" && staged.activeRollout(org, tx.controller.ClusterID) != "") {
			return domain.ErrConflict
		}
		return staged.admitWork(org, "")
	}
	tx.audit = func(r domain.Rollout, action string) error {
		staged.audit = append(staged.audit, auditEvent{actorUserID: r.ActorUserID, organizationID: org, clusterID: cluster, action: action, createdAt: r.UpdatedAt, rolloutID: r.ID})
		return nil
	}
	tx.handoff = func(r domain.Maintenance) error {
		staged.maintenances[r.ID] = cloneMaintenance(r)
		staged.audit = append(staged.audit, auditEvent{maintenanceID: r.ID, actorUserID: r.ActorUserID, organizationID: org, clusterID: cluster, nodeID: r.NodeID, action: "maintenance.attention", createdAt: r.UpdatedAt})
		return nil
	}
	tx.recoveries = func(ids []string) (map[string]domain.Maintenance, error) {
		out := map[string]domain.Maintenance{}
		for _, id := range ids {
			if r, ok := staged.maintenances[id]; ok && r.OrganizationID == org && r.ClusterID == cluster {
				out[id] = cloneMaintenance(r)
			}
		}
		return out, nil
	}
	configureMemoryApplications(tx, staged, org, cluster)
	configureMemoryListeners(tx, staged, org, cluster)
	configureMemoryACME(tx, staged, org, cluster)
	configureMemoryIncidents(tx, staged, org, cluster)
	if err := fn(tx); err != nil {
		return err
	}
	m.commitControlMemory(staged)
	return nil
}
func (p *Postgres) rolloutTransaction(ctx context.Context, org, cluster string, fn func(*rolloutTransaction) error) error {
	if !domain.ValidID(org) || !domain.ValidID(cluster) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(db pgx.Tx) error {
		var c domain.Cluster
		if err := db.QueryRow(ctx, `SELECT id::text,organization_id::text,name,created_at,topology FROM hm_clusters WHERE organization_id=$1 AND id=$2`, org, cluster).Scan(&c.ID, &c.OrganizationID, &c.Name, &c.CreatedAt, &c.Topology); err != nil {
			return err
		}
		var controller domain.Node
		original := ""
		ids := []string{cluster}
		if c.Topology != nil && c.Topology.Control != nil {
			original = c.Topology.Control.NodeID
			if err := db.QueryRow(ctx, `SELECT id::text,organization_id::text,cluster_id::text,name,revoked_at FROM hm_nodes WHERE organization_id=$1 AND id=$2`, org, original).Scan(&controller.ID, &controller.OrganizationID, &controller.ClusterID, &controller.Name, &controller.RevokedAt); err != nil {
				return err
			}
			if controller.ClusterID == cluster {
				return domain.ErrInvalidInput
			}
			ids = append(ids, controller.ClusterID)
		}
		sort.Strings(ids)
		for _, id := range ids {
			var locked string
			if err := db.QueryRow(ctx, `SELECT id::text FROM hm_clusters WHERE organization_id=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&locked); err != nil {
				return err
			}
		}
		if err := db.QueryRow(ctx, `SELECT topology FROM hm_clusters WHERE organization_id=$1 AND id=$2`, org, cluster).Scan(&c.Topology); err != nil {
			return err
		}
		current := ""
		if c.Topology != nil && c.Topology.Control != nil {
			current = c.Topology.Control.NodeID
		}
		if current != original {
			return domain.ErrConflict
		}
		scoped := context.WithValue(ctx, postgresRolloutTxKey{}, db)
		tx := &rolloutTransaction{ctx: scoped, persistence: p, cluster: c, controller: controller}
		rows, err := db.Query(ctx, `SELECT id::text,name FROM hm_nodes WHERE organization_id=$1 AND cluster_id=$2 AND revoked_at IS NULL ORDER BY name,id`, org, cluster)
		if err != nil {
			return err
		}
		for rows.Next() {
			n := domain.Node{OrganizationID: org, ClusterID: cluster}
			if err = rows.Scan(&n.ID, &n.Name); err != nil {
				rows.Close()
				return err
			}
			tx.nodes = append(tx.nodes, n)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		tx.get = func(value string, request bool) (domain.Rollout, error) {
			var raw []byte
			var r domain.Rollout
			query := `SELECT payload FROM hm_rollouts WHERE organization_id=$1 AND cluster_id=$2 AND id=$3`
			args := []any{org, cluster, value}
			if request {
				query = `SELECT payload FROM hm_rollouts WHERE organization_id=$1 AND request_id=$2`
				args = []any{org, value}
			}
			if err := db.QueryRow(ctx, query, args...).Scan(&raw); err != nil {
				return r, normalizeError(err)
			}
			err := json.Unmarshal(raw, &r)
			return r, err
		}
		tx.list = func() ([]domain.Rollout, error) {
			rows, err := db.Query(ctx, `SELECT payload FROM hm_rollouts WHERE organization_id=$1 AND cluster_id=$2 ORDER BY created_at DESC LIMIT 20`, org, cluster)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			result := []domain.Rollout{}
			for rows.Next() {
				var raw []byte
				var r domain.Rollout
				if err = rows.Scan(&raw); err != nil {
					return nil, err
				}
				if err = json.Unmarshal(raw, &r); err != nil {
					return nil, err
				}
				result = append(result, r)
			}
			return result, rows.Err()
		}
		tx.save = func(r domain.Rollout) error {
			raw, err := json.Marshal(r)
			if err != nil {
				return err
			}
			controllerCluster := ""
			if r.Entrypoint != nil {
				controllerCluster = r.Entrypoint.ClusterID
			}
			_, err = db.Exec(ctx, `INSERT INTO hm_rollouts(id,organization_id,cluster_id,request_id,status,created_at,updated_at,payload,controller_cluster_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid) ON CONFLICT(id) DO UPDATE SET status=excluded.status,updated_at=excluded.updated_at,payload=excluded.payload`, r.ID, org, cluster, r.RequestID, r.Status, r.CreatedAt, r.UpdatedAt, raw, controllerCluster)
			return err
		}
		tx.reserve = func() error {
			if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(492584126125)`); err != nil {
				return err
			}
			var own bool
			if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_maintenance WHERE organization_id=$1 AND (cluster_id=ANY($2::uuid[]) OR controller_cluster_id=ANY($2::uuid[])) AND status NOT IN ('completed','failed'))`, org, ids).Scan(&own); err != nil {
				return err
			}
			if own {
				return domain.ErrConflict
			}
			if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_rollouts WHERE organization_id=$1 AND (cluster_id=ANY($2::uuid[]) OR controller_cluster_id=ANY($2::uuid[])) AND status IN ('running','rolling_back'))`, org, ids).Scan(&own); err != nil {
				return err
			}
			if own {
				return domain.ErrConflict
			}
			return admitWork(ctx, db, org, "")
		}
		tx.audit = func(r domain.Rollout, action string) error {
			_, err := db.Exec(ctx, `INSERT INTO hm_audit_events(id,organization_id,cluster_id,action,actor_user_id,rollout_id,created_at) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,$7)`, domain.NewID(), org, cluster, action, r.ActorUserID, r.ID, r.UpdatedAt)
			return err
		}
		tx.handoff = func(m domain.Maintenance) error {
			raw, err := json.Marshal(m)
			if err != nil {
				return err
			}
			if _, err = db.Exec(ctx, `INSERT INTO hm_maintenance(id,organization_id,cluster_id,node_id,controller_cluster_id,controller_node_id,status,created_at,updated_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, m.ID, org, cluster, m.NodeID, m.ControllerClusterID, m.ControllerNodeID, m.Status, m.CreatedAt, m.UpdatedAt, raw); err != nil {
				return err
			}
			_, err = db.Exec(ctx, `INSERT INTO hm_audit_events(id,organization_id,cluster_id,node_id,action,actor_user_id,maintenance_id,created_at) VALUES($1,$2,$3,$4,'maintenance.attention',NULLIF($5,'')::uuid,$6,$7)`, domain.NewID(), org, cluster, m.NodeID, m.ActorUserID, m.ID, m.UpdatedAt)
			return err
		}
		tx.recoveries = func(ids []string) (map[string]domain.Maintenance, error) {
			out := map[string]domain.Maintenance{}
			if len(ids) == 0 {
				return out, nil
			}
			rows, err := db.Query(ctx, `SELECT payload FROM hm_maintenance WHERE organization_id=$1 AND cluster_id=$2 AND id=ANY($3::uuid[])`, org, cluster, ids)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			for rows.Next() {
				var raw []byte
				var m domain.Maintenance
				if err = rows.Scan(&raw); err != nil {
					return nil, err
				}
				if err = json.Unmarshal(raw, &m); err != nil {
					return nil, err
				}
				out[m.ID] = m
			}
			return out, rows.Err()
		}
		configurePostgresApplications(tx, db, org, cluster)
		configurePostgresListeners(tx, db, org, cluster)
		configurePostgresACME(tx, db, org, cluster)
		configurePostgresIncidents(tx, db, org, cluster)
		return fn(tx)
	})
}
func createRollout(ctx context.Context, p rolloutPersistence, org, cluster string, input domain.RolloutInput) (domain.Rollout, error) {
	var result domain.Rollout
	if input.DrainTimeoutSeconds == 0 {
		input.DrainTimeoutSeconds = 30
	}
	if !input.Valid() {
		return result, domain.ErrInvalidInput
	}
	err := p.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error {
		var err error
		result, err = createRolloutInTransaction(ctx, tx, org, cluster, input)
		return err
	})
	return cloneRollout(result), err
}
func createRolloutInTransaction(ctx context.Context, tx *rolloutTransaction, org, cluster string, input domain.RolloutInput) (domain.Rollout, error) {
	var result domain.Rollout
	p := tx.persistence
	perform := func() error {
		existing, err := tx.get(input.RequestID, true)
		if err == nil {
			if existing.ClusterID != cluster || !existing.Document.Equal(input.Document) || len(existing.Nodes) != len(input.Nodes) || !sameRolloutEntrypoint(existing, input) {
				return domain.ErrConflict
			}
			for _, node := range existing.Nodes {
				matched := false
				for _, target := range input.Nodes {
					matched = matched || node.RolloutTarget == target
				}
				if !matched {
					return domain.ErrConflict
				}
			}
			result = existing
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if err := tx.reserve(); err != nil {
			return err
		}
		if len(tx.nodes) != len(input.Nodes) {
			return domain.ErrConflict
		}
		now := controlNow(ctx)
		r := domain.Rollout{ID: domain.NewID(), RequestID: input.RequestID, OrganizationID: org, ClusterID: cluster, ActorUserID: domain.Actor(ctx), Document: input.Document.Canonical(), Status: "running", Nodes: []domain.RolloutNode{}, CreatedAt: now, UpdatedAt: now, Deadline: now.Add(15 * time.Minute), DrainTimeoutSeconds: input.DrainTimeoutSeconds}
		if err := prepareRolloutEntrypoint(tx, &r, input, now); err != nil {
			return err
		}
		different := false
		for _, node := range tx.nodes {
			var target *domain.RolloutTarget
			for i := range input.Nodes {
				if input.Nodes[i].NodeID == node.ID {
					target = &input.Nodes[i]
					break
				}
			}
			if target == nil {
				return domain.ErrNotFound
			}
			err := p.controlTransaction(tx.ctx, org, node.ID, func(control *controlTransaction) error {
				if !configurationEnabled(control.state, now) || control.state.PendingOperationID != "" {
					return domain.ErrConflict
				}
				if r.Entrypoint != nil {
					if _, known := domain.FrontendConnections(control.state, r.Entrypoint.Topology.Frontend, now); !known {
						return domain.ErrConflict
					}
				}
				d := control.state.Discovery
				s := d.Configuration
				if d.Generation != target.ExpectedGeneration || s.ArtifactID != target.ExpectedArtifactID || s.Version != target.ExpectedVersion || !domain.RolloutHealthy(control.state, *s.Document, now) {
					return domain.ErrConflict
				}
				if !domain.SameApplications(s.Document.Applications, input.Document.Applications) && ctx.Value(applicationWriterKey{}) != true {
					return domain.ErrConflict
				}
				if !domain.SameListeners(s.Document.Listeners, input.Document.Listeners) && ctx.Value(listenerWriterKey{}) != true {
					return domain.ErrConflict
				}
				for _, l := range input.Document.Listeners {
					if !s.ListenerReady(l, input.Document.Applications, now) {
						return domain.ErrConflict
					}
				}
				if len(s.Document.Backends) != len(input.Document.Backends) {
					return domain.ErrInvalidInput
				}
				if !input.Document.KnownIncomingPorts(*s.Document) {
					return domain.ErrInvalidInput
				}
				for _, b := range s.Document.Backends {
					found := false
					for _, desired := range input.Document.Backends {
						found = found || desired.Name == b.Name
					}
					if !found {
						return domain.ErrInvalidInput
					}
				}
				if err := observeConfiguration(control, true); err != nil {
					return err
				}
				baseline, err := control.revision(control.state.CurrentRevisionID)
				if err != nil {
					return err
				}
				if baseline.ArtifactID != s.ArtifactID {
					return domain.ErrConflict
				}
				checks, rollbackChecks, err := prepareApplicationChecks(r, *d)
				if err != nil {
					return err
				}
				r.Nodes = append(r.Nodes, domain.RolloutNode{RolloutTarget: *target, Name: node.Name, BaselineRevisionID: baseline.ID, Status: "pending", Deadline: now.Add(20 * time.Second), ApplicationChecks: checks, RollbackApplicationChecks: rollbackChecks})
				different = different || !s.Document.Equal(input.Document)
				return nil
			})
			if err != nil {
				return err
			}
		}
		if !different {
			return domain.ErrConflict
		}
		if err := tx.save(r); err != nil {
			return err
		}
		if err := tx.audit(r, "rollout.started"); err != nil {
			return err
		}
		result = r
		return nil
	}
	err := perform()
	return cloneRollout(result), err
}
func listRollouts(ctx context.Context, p rolloutPersistence, org, cluster string) ([]domain.Rollout, error) {
	out := []domain.Rollout{}
	err := p.rolloutTransaction(ctx, org, cluster, func(tx *rolloutTransaction) error {
		var err error
		out, err = tx.list()
		if err != nil {
			return err
		}
		ids := []string{}
		for _, r := range out {
			for _, n := range r.Nodes {
				if n.RecoveryMaintenanceID != "" {
					ids = append(ids, n.RecoveryMaintenanceID)
				}
			}
		}
		recoveries, err := tx.recoveries(ids)
		if err != nil {
			return err
		}
		// A handoff is a reference to a maintenance reservation that can outlive
		// the rollout. Resolve its current state even after it leaves the latest
		// maintenance-history page, without changing the stored rollout outcome.
		for i := range out {
			for j := range out[i].Nodes {
				n := &out[i].Nodes[j]
				m, ok := recoveries[n.RecoveryMaintenanceID]
				if !ok || m.NodeID != n.NodeID || out[i].Entrypoint == nil || m.ControllerNodeID != out[i].Entrypoint.NodeID || m.BindingID != out[i].Entrypoint.BindingID {
					continue
				}
				if n.Maintenance != nil && n.Maintenance.ID == m.ID {
					copy := cloneMaintenance(m)
					n.Maintenance = &copy
				}
				if n.RollbackMaintenance != nil && n.RollbackMaintenance.ID == m.ID {
					copy := cloneMaintenance(m)
					n.RollbackMaintenance = &copy
				}
			}
		}
		return nil
	})
	return out, err
}
func (m *Memory) CreateRollout(ctx context.Context, org, cluster string, input domain.RolloutInput) (domain.Rollout, error) {
	return createRollout(ctx, m, org, cluster, input)
}
func (p *Postgres) CreateRollout(ctx context.Context, org, cluster string, input domain.RolloutInput) (domain.Rollout, error) {
	return createRollout(ctx, p, org, cluster, input)
}
func (m *Memory) ListRollouts(ctx context.Context, org, cluster string) ([]domain.Rollout, error) {
	return listRollouts(ctx, m, org, cluster)
}
func (p *Postgres) ListRollouts(ctx context.Context, org, cluster string) ([]domain.Rollout, error) {
	return listRollouts(ctx, p, org, cluster)
}
func (m *Memory) activeRollouts(ctx context.Context) ([]domain.Rollout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.Rollout{}
	for _, r := range m.rollouts {
		if !r.Terminal() {
			out = append(out, cloneRollout(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	if len(out) > maxRunnableWork {
		out = out[:maxRunnableWork]
	}
	return out, ctx.Err()
}
func (p *Postgres) activeRollouts(ctx context.Context) ([]domain.Rollout, error) {
	rows, err := p.pool.Query(ctx, `SELECT payload FROM hm_rollouts WHERE status IN ('running','rolling_back') ORDER BY updated_at,id LIMIT 16`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Rollout{}
	for rows.Next() {
		var raw []byte
		var r domain.Rollout
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
