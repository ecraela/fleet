package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed control_schema.sql
var controlSchemaSQL string

//go:embed direct_control_schema.sql
var directControlSchemaSQL string

type ControlStore interface {
	OpenControl(context.Context, string, string, string) (domain.NodeControl, error)
	ReportControl(context.Context, string, string, string, *domain.Discovery, *domain.OperationResult) (domain.NodeControl, *domain.Operation, error)
	GetControl(context.Context, string, string) (domain.NodeControl, []domain.Operation, error)
	CreateOperation(context.Context, string, domain.Operation) (domain.Operation, error)
}
type controlTransaction struct {
	now                    time.Time
	activeRolloutID        string
	revisionNumber         int64
	revision               func(string) (domain.ConfigurationRevision, error)
	revisions              func() ([]domain.ConfigurationRevision, error)
	saveRevision           func(domain.ConfigurationRevision) error
	observationHistoryFull func() (bool, error)
	node                   domain.Node
	state                  domain.NodeControl
	get                    func(string, bool) (domain.Operation, error)
	save                   func(domain.Operation) error
	audit                  func(string, string) error
}
type controlPersistence interface {
	controlTransaction(context.Context, string, string, func(*controlTransaction) error) error
}
type controlClockKey struct{}

func controlNow(ctx context.Context) time.Time {
	if clock, ok := ctx.Value(controlClockKey{}).(func() time.Time); ok {
		return clock().UTC()
	}
	return time.Now().UTC()
}

func openControl(ctx context.Context, p controlPersistence, org, node, session string) (domain.NodeControl, error) {
	var state domain.NodeControl
	if !domain.ValidID(session) {
		return state, domain.ErrInvalidInput
	}
	err := p.controlTransaction(ctx, org, node, func(tx *controlTransaction) error {
		if tx.state.PendingOperationID != "" {
			op, err := tx.get(tx.state.PendingOperationID, false)
			if err != nil {
				return err
			}
			if op.Status == "running" {
				op.Status = "unknown"
				op.ErrorCode = "control_reconnected"
				now := controlNow(ctx)
				op.CompletedAt = &now

				if err = saveControlOperation(tx, op); err != nil {
					return err
				}
				if err = tx.audit("operation.unknown", op.ID); err != nil {
					return err
				}
				tx.state.PendingOperationID = ""
			}
		}
		tx.state.SessionID = session
		tx.state.ConnectedAt = time.Now().UTC()
		tx.state.LastSeen = tx.state.ConnectedAt
		// An old connection's discovery cannot authorize a new connection.
		tx.state.Discovery = nil
		state = tx.state
		return nil
	})
	return state, err
}
func validResult(r *domain.OperationResult) bool {
	if r == nil {
		return true
	}
	if !domain.ValidID(r.OperationID) || !domain.ValidDigest(r.CommandDigest) || len(r.ErrorCode) > 64 {
		return false
	}
	if r.Outcome != "succeeded" && r.Outcome != "failed" && r.Outcome != "unknown" {
		return false
	}
	switch r.ErrorCode {
	case "", "runtime_unavailable", "generation_changed", "target_missing", "state_changed", "last_available_server", "invalid_operation", "runtime_rejected", "verification_deadline", "outcome_unverified", "journal_unavailable", "control_disabled", "configuration_unavailable", "configuration_unsupported", "configuration_changed", "configuration_rejected", "configuration_history_missing", "configuration_storage_full", "configuration_reload_failed":
	default:
		return false
	}
	return r.Discovery != nil && r.Discovery.Valid()
}
func reportControl(ctx context.Context, p controlPersistence, org, node, session string, d *domain.Discovery, result *domain.OperationResult) (domain.NodeControl, *domain.Operation, error) {
	if d != nil && d.Configuration != nil && d.Configuration.NodeID != "" && d.Configuration.NodeID != node {
		return domain.NodeControl{}, nil, domain.ErrInvalidInput
	}

	if result != nil && result.Discovery != nil && result.Discovery.Configuration != nil && result.Discovery.Configuration.NodeID != "" && result.Discovery.Configuration.NodeID != node {
		return domain.NodeControl{}, nil, domain.ErrInvalidInput
	}
	var state domain.NodeControl
	var command *domain.Operation
	if !domain.ValidID(session) || d == nil || !d.Valid() || !validResult(result) {
		return state, nil, domain.ErrInvalidInput
	}
	err := p.controlTransaction(ctx, org, node, func(tx *controlTransaction) error {
		if tx.state.SessionID != session {
			return domain.ErrConflict
		}
		now := controlNow(ctx)
		tx.state.LastSeen = now
		if d.ObservedAtMS > now.Add(time.Minute).UnixMilli() || d.ObservedAtMS < now.Add(-time.Minute).UnixMilli() {
			return domain.ErrInvalidInput
		}
		tx.state.Discovery = cloneDiscovery(d)
		if result != nil {
			op, err := tx.get(result.OperationID, false)
			if err != nil {
				return err
			}
			if op.CommandDigest != result.CommandDigest || op.Status == "queued" {
				return domain.ErrConflict
			}
			if !op.Terminal() || (op.Status == "unknown" && (result.Outcome != "unknown" || result.ErrorCode != op.ErrorCode)) {
				previousStatus := op.Status
				op.Status = result.Outcome
				op.ErrorCode = result.ErrorCode
				if op.Status == "succeeded" && (!result.Discovery.Fresh(now) || !op.VerifiedAt(*result.Discovery, now)) {
					op.Status = "unknown"
					op.ErrorCode = "outcome_unverified"
				}
				op.CompletedAt = &now
				if op.Kind == "configuration" && op.Status == "succeeded" {
					revision, readErr := tx.revision(op.ConfigurationRevisionID)
					if readErr != nil {
						return readErr
					}
					revision.ArtifactID = result.Discovery.Configuration.ArtifactID
					if err = tx.saveRevision(revision); err != nil {
						return err
					}
					tx.state.CurrentRevisionID = revision.ID
					if result.Discovery.ObservedAtMS >= d.ObservedAtMS {
						tx.state.Discovery = cloneDiscovery(result.Discovery)
					}
				}
				if err = saveControlOperation(tx, op); err != nil {
					return err
				}
				// Unknown reasons may change, including a success claim that still
				// cannot be verified. Audit state transitions, not repeated claims.
				if op.Status != previousStatus {
					if err = tx.audit("operation."+op.Status, op.ID); err != nil {
						return err
					}
				}
			}
			if tx.state.PendingOperationID == op.ID {
				tx.state.PendingOperationID = ""
			}
		}
		if tx.state.PendingOperationID != "" {
			op, err := tx.get(tx.state.PendingOperationID, false)
			if err != nil {
				return err
			}
			if !now.Before(op.Deadline) {
				op.Status = "failed"
				op.ErrorCode = "verification_deadline"
				if op.CommandDigest != "" {
					op.Status = "unknown"
				}
				op.CompletedAt = &now
				if err = saveControlOperation(tx, op); err != nil {
					return err
				}
				if err = tx.audit("operation."+op.Status, op.ID); err != nil {
					return err
				}
				tx.state.PendingOperationID = ""
			} else if op.Status == "queued" {
				if (!(op.Kind == "configuration" && configurationEnabled(tx.state, now)) && !(op.Kind != "configuration" && managementEnabled(tx.state, now))) || op.ExpectedGeneration != d.Generation {
					op.Status = "failed"
					op.ErrorCode = "generation_changed"
					op.CompletedAt = &now
					tx.state.PendingOperationID = ""
				} else {
					op.Status = "running"
					op.CommandDigest = op.Digest()
					copy := op
					command = &copy
				}
				if err = saveControlOperation(tx, op); err != nil {
					return err
				}
				action := "operation.dispatched"
				if op.Status == "failed" {
					action = "operation.failed"
				}
				if err = tx.audit(action, op.ID); err != nil {
					return err
				}
			}
		}
		if err := observeConfiguration(tx, false); err != nil {
			return err
		}
		state = tx.state
		return nil
	})
	return state, command, err
}
func managementEnabled(s domain.NodeControl, now time.Time) bool {
	return s.Discovery != nil && s.Discovery.Fresh(now) && s.Discovery.ControlAllowed && now.Sub(s.LastSeen) < 6*time.Second
}
func expirePending(tx *controlTransaction) error {
	if tx.state.PendingOperationID == "" {
		return nil
	}
	op, err := tx.get(tx.state.PendingOperationID, false)
	if err != nil {
		return err
	}
	now := tx.now
	if now.Before(op.Deadline) {
		return nil
	}
	if !op.Terminal() {
		op.Status = "failed"
		if op.CommandDigest != "" {
			op.Status = "unknown"
		}
		op.ErrorCode = "verification_deadline"
		op.CompletedAt = &now
		if err = saveControlOperation(tx, op); err != nil {
			return err
		}
		if err = tx.audit("operation."+op.Status, op.ID); err != nil {
			return err
		}
	}
	tx.state.PendingOperationID = ""
	return nil
}
func createOperation(ctx context.Context, p controlPersistence, org string, input domain.Operation) (domain.Operation, error) {
	var created domain.Operation
	if !input.Valid() {
		return created, domain.ErrInvalidInput
	}
	err := p.controlTransaction(ctx, org, input.NodeID, func(tx *controlTransaction) error {
		existing, err := tx.get(input.RequestID, true)
		if err == nil {
			if existing.NodeID != input.NodeID || existing.Kind != input.Kind || existing.Backend != input.Backend || existing.Server != input.Server || existing.Weight != input.Weight || existing.ExpectedGeneration != input.ExpectedGeneration || existing.ExpectedState != input.ExpectedState || existing.ExpectedWeight != input.ExpectedWeight {
				return domain.ErrConflict
			}
			created = existing
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if !rolloutWriter(ctx, tx) {
			return domain.ErrConflict
		}
		now := controlNow(ctx)
		if !managementEnabled(tx.state, now) || tx.state.Discovery.Generation != input.ExpectedGeneration || tx.state.PendingOperationID != "" {
			return domain.ErrConflict
		}
		target, ok := tx.state.Discovery.Server(input.Backend, input.Server)
		if !ok {
			return domain.ErrNotFound
		}
		if target.State != input.ExpectedState || target.Weight != input.ExpectedWeight {
			return domain.ErrConflict
		}
		if strings.HasPrefix(input.Backend, domain.ApplicationBackendPrefix) {
			return domain.ErrConflict
		}
		if (input.Kind == "maintenance" || input.Kind == "drain" || (input.Kind == "weight" && input.Weight == 0)) && !tx.state.Discovery.HasCapacity(input.Backend, input.Server) {
			return domain.ErrConflict
		}
		input.ID = domain.NewID()
		input.OrganizationID = org
		input.ExpectedState = target.State
		input.ExpectedWeight = target.Weight
		input.Status = "queued"
		input.MaintenanceID, _ = ctx.Value(maintenanceWriterKey{}).(string)
		input.RolloutID, _ = ctx.Value(rolloutWriterKey{}).(string)
		input.ActorUserID = domain.Actor(ctx)
		input.CreatedAt = now
		input.Deadline = now.Add(30 * time.Second)
		if err = tx.save(input); err != nil {
			return err
		}
		if err = tx.audit("operation.requested", input.ID); err != nil {
			return err
		}
		tx.state.PendingOperationID = input.ID
		created = input
		return nil
	})
	return created, err
}
func cloneDiscovery(d *domain.Discovery) *domain.Discovery {
	if d == nil {
		return nil
	}
	copy := *d
	if d.Setup != nil {
		setup := *d.Setup
		setup.Checks = append([]domain.SetupCheck{}, d.Setup.Checks...)
		copy.Setup = &setup
	}
	copy.ApplicationProbes = append([]domain.ApplicationProbe{}, d.ApplicationProbes...)
	if d.Configuration != nil {
		value := *d.Configuration
		value.ListenerBindings = append([]domain.ListenerBinding{}, value.ListenerBindings...)
		value.TLSListeners = append([]domain.TLSListenerObservation{}, value.TLSListeners...)
		value.ACMEGrants = append([]domain.ACMEGrant{}, value.ACMEGrants...)
		for i := range value.ACMEGrants {
			value.ACMEGrants[i].Challenges = append([]domain.ACMEChallenge{}, value.ACMEGrants[i].Challenges...)
		}
		value.TLSCertificates = append([]domain.TLSCertificate{}, value.TLSCertificates...)
		for i := range value.TLSCertificates {
			value.TLSCertificates[i].DNSNames = append([]string{}, value.TLSCertificates[i].DNSNames...)
		}
		if value.Document != nil {
			doc := value.Document.Canonical()
			value.Document = &doc
		}
		copy.Configuration = &value
	}
	copy.Proxies = append([]domain.Proxy{}, d.Proxies...)
	for i, p := range copy.Proxies {
		if p.ActiveConnections != nil {
			v := *p.ActiveConnections
			copy.Proxies[i].ActiveConnections = &v
		}
	}
	copy.Servers = append([]domain.BackendServer{}, d.Servers...)
	return &copy
}

type operationRecord struct {
	domain.Operation
	PrivateDigest string `json:"command_digest"`
}

func decodeOperation(raw []byte) (domain.Operation, error) {
	var record operationRecord
	err := json.Unmarshal(raw, &record)
	record.Operation.CommandDigest = record.PrivateDigest
	return record.Operation, err
}

func (m *Memory) controlTransaction(ctx context.Context, org, node string, fn func(*controlTransaction) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !domain.ValidID(org) || !domain.ValidID(node) {
		return domain.ErrInvalidInput
	}
	if staged, ok := ctx.Value(memoryRolloutTxKey{}).(*Memory); ok {
		return staged.controlTransactionLocked(ctx, org, node, fn)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.controlTransactionLocked(ctx, org, node, fn)
}
func (m *Memory) controlTransactionLocked(ctx context.Context, org, node string, fn func(*controlTransaction) error) error {
	n, ok := m.nodes[node]
	if !ok || n.OrganizationID != org {
		return domain.ErrNotFound
	}
	if n.RevokedAt != nil {
		return domain.ErrRevoked
	}
	if m.controls == nil {
		m.controls = map[string]domain.NodeControl{}
		m.operations = map[string]domain.Operation{}
	}
	state := m.controls[node]
	state.NodeID = node
	state.Discovery = cloneDiscovery(state.Discovery)
	staged := map[string]domain.Operation{}
	events := []auditEvent{}
	tx := &controlTransaction{node: n, state: state, activeRolloutID: m.activeRollout(org, n.ClusterID), now: controlNow(ctx)}
	tx.state.ActiveRolloutID = tx.activeRolloutID
	m.setMaintenanceGuard(tx)
	if m.revisions == nil {
		m.revisions = map[string]domain.ConfigurationRevision{}
	}
	stagedRevisions := map[string]domain.ConfigurationRevision{}
	tx.observationHistoryFull = func() (bool, error) {
		count := 0
		for id, r := range m.revisions {
			if _, replaced := stagedRevisions[id]; !replaced && r.OrganizationID == org && r.NodeID == node && r.Source == "discovered" {
				count++
			}
		}
		for _, r := range stagedRevisions {
			if r.Source == "discovered" {
				count++
			}
		}
		return count >= discoveredRevisionLimit, nil
	}
	tx.revision = func(id string) (domain.ConfigurationRevision, error) {
		for _, collection := range []map[string]domain.ConfigurationRevision{stagedRevisions, m.revisions} {
			if r, ok := collection[id]; ok && r.OrganizationID == org && r.NodeID == node {
				return cloneRevision(r), nil
			}
		}
		return domain.ConfigurationRevision{}, domain.ErrNotFound
	}
	tx.revisions = func() ([]domain.ConfigurationRevision, error) {
		all := map[string]domain.ConfigurationRevision{}
		for _, collection := range []map[string]domain.ConfigurationRevision{m.revisions, stagedRevisions} {
			for id, r := range collection {
				if r.OrganizationID == org && r.NodeID == node {
					all[id] = cloneRevision(r)
				}
			}
		}
		list := []domain.ConfigurationRevision{}
		for _, r := range all {
			list = append(list, r)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Number > list[j].Number })
		if len(list) > 50 {
			list = list[:50]
		}
		return list, nil
	}
	for _, r := range m.revisions {
		if r.OrganizationID == org && r.NodeID == node && r.Number > tx.revisionNumber {
			tx.revisionNumber = r.Number
		}
	}
	tx.saveRevision = func(r domain.ConfigurationRevision) error {
		stagedRevisions[r.ID] = cloneRevision(r)
		if r.Number > tx.revisionNumber {
			tx.revisionNumber = r.Number
		}
		return nil
	}
	tx.get = func(id string, request bool) (domain.Operation, error) {
		for _, collection := range []map[string]domain.Operation{staged, m.operations} {
			for key, op := range collection {
				if op.OrganizationID == org && ((request && op.RequestID == id) || (!request && key == id && op.NodeID == node)) {
					return cloneOperation(op), nil
				}
			}
		}
		return domain.Operation{}, domain.ErrNotFound
	}
	tx.save = func(op domain.Operation) error { staged[op.ID] = cloneOperation(op); return nil }
	tx.audit = func(action, op string) error {
		events = append(events, auditEvent{actorUserID: domain.Actor(ctx), organizationID: org, clusterID: n.ClusterID, nodeID: node, action: action, createdAt: time.Now().UTC(), operationID: op})
		return nil
	}
	if err := fn(tx); err != nil {
		return err
	}
	for id, r := range stagedRevisions {
		m.revisions[id] = r
	}
	m.controls[node] = tx.state
	for id, op := range staged {
		m.operations[id] = op
	}
	m.audit = append(m.audit, events...)
	return nil
}
func (p *Postgres) controlTransaction(ctx context.Context, org, node string, fn func(*controlTransaction) error) error {
	if !domain.ValidID(org) || !domain.ValidID(node) {
		return domain.ErrInvalidInput
	}
	if db, ok := ctx.Value(postgresRolloutTxKey{}).(pgx.Tx); ok {
		return p.controlTransactionDB(ctx, db, org, node, fn)
	}
	return p.transaction(ctx, func(db pgx.Tx) error { return p.controlTransactionDB(ctx, db, org, node, fn) })
}
func (p *Postgres) controlTransactionDB(ctx context.Context, db pgx.Tx, org, node string, fn func(*controlTransaction) error) error {
	// Cluster before node is the shared lock order for single-node writes and rollouts.
	var cluster string
	if err := db.QueryRow(ctx, `SELECT cluster_id::text FROM hm_nodes WHERE organization_id=$1 AND id=$2`, org, node).Scan(&cluster); err != nil {
		return err
	}
	if err := db.QueryRow(ctx, `SELECT id::text FROM hm_clusters WHERE organization_id=$1 AND id=$2 FOR UPDATE`, org, cluster).Scan(&cluster); err != nil {
		return err
	}
	var n domain.Node
	if err := db.QueryRow(ctx, `SELECT id::text,organization_id::text,cluster_id::text,revoked_at FROM hm_nodes WHERE organization_id=$1 AND id=$2 FOR UPDATE`, org, node).Scan(&n.ID, &n.OrganizationID, &n.ClusterID, &n.RevokedAt); err != nil {
		return err
	}
	if n.RevokedAt != nil {
		return domain.ErrRevoked
	}
	if _, err := db.Exec(ctx, `INSERT INTO hm_node_controls(node_id,organization_id,cluster_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, node, org, n.ClusterID); err != nil {
		return err
	}
	tx := &controlTransaction{node: n, now: controlNow(ctx)}
	tx.observationHistoryFull = func() (bool, error) {
		var full bool
		err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_configuration_revisions WHERE organization_id=$1 AND node_id=$2 AND payload->>'source'='discovered' OFFSET $3 LIMIT 1)`, org, node, discoveredRevisionLimit-1).Scan(&full)
		return full, err
	}
	if err := db.QueryRow(ctx, `SELECT COALESCE((SELECT id::text FROM hm_rollouts WHERE organization_id=$1 AND (cluster_id=$2 OR controller_cluster_id=$2) AND status IN ('running','rolling_back') LIMIT 1),'')`, org, n.ClusterID).Scan(&tx.activeRolloutID); err != nil {
		return err
	}
	tx.state.ActiveRolloutID = tx.activeRolloutID
	var maintenanceID, maintenanceNode, maintenanceStatus string
	err := db.QueryRow(ctx, `SELECT id::text,node_id::text,status FROM hm_maintenance WHERE organization_id=$1 AND (cluster_id=$2 OR controller_cluster_id=$2) AND status NOT IN ('completed','failed') LIMIT 1`, org, n.ClusterID).Scan(&maintenanceID, &maintenanceNode, &maintenanceStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	tx.state.MaintenanceID = maintenanceID
	tx.state.MaintenanceBlocked = maintenanceID != "" && !(maintenanceNode == n.ID && maintenanceStatus == "maintenance")
	var raw []byte
	var session, pending, revision *string
	var seen *time.Time
	if err := db.QueryRow(ctx, `SELECT session_id::text,connected_at,CASE WHEN last_seen='-infinity' THEN NULL ELSE last_seen END,discovery,pending_operation_id::text,current_revision_id::text FROM hm_node_controls WHERE organization_id=$1 AND node_id=$2 FOR UPDATE`, org, node).Scan(&session, &tx.state.ConnectedAt, &seen, &raw, &pending, &revision); err != nil {
		return err
	}
	if revision != nil {
		tx.state.CurrentRevisionID = *revision
	}
	tx.state.NodeID = node
	if session != nil {
		tx.state.SessionID = *session
	}
	if pending != nil {
		tx.state.PendingOperationID = *pending
	}
	if seen != nil {
		tx.state.LastSeen = *seen
	}
	if raw != nil {
		tx.state.Discovery = &domain.Discovery{}
		if err := json.Unmarshal(raw, tx.state.Discovery); err != nil {
			return err
		}
	}
	tx.revision = func(id string) (domain.ConfigurationRevision, error) {
		var raw []byte
		var r domain.ConfigurationRevision
		if err := db.QueryRow(ctx, `SELECT payload FROM hm_configuration_revisions WHERE id=$1 AND node_id=$2 AND organization_id=$3`, id, node, org).Scan(&raw); err != nil {
			return r, normalizeError(err)
		}
		err := json.Unmarshal(raw, &r)
		return r, err
	}
	tx.revisions = func() ([]domain.ConfigurationRevision, error) {
		rows, err := db.Query(ctx, `SELECT payload FROM hm_configuration_revisions WHERE node_id=$1 AND organization_id=$2 ORDER BY number DESC LIMIT 50`, node, org)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		result := []domain.ConfigurationRevision{}
		for rows.Next() {
			var raw []byte
			var r domain.ConfigurationRevision
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
	if err := db.QueryRow(ctx, `SELECT COALESCE(MAX(number),0) FROM hm_configuration_revisions WHERE node_id=$1 AND organization_id=$2`, node, org).Scan(&tx.revisionNumber); err != nil {
		return err
	}
	tx.saveRevision = func(r domain.ConfigurationRevision) error {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = db.Exec(ctx, `INSERT INTO hm_configuration_revisions(id,organization_id,node_id,number,created_at,payload,cluster_id) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, r.ID, org, node, r.Number, r.CreatedAt, raw, n.ClusterID)
		if err == nil && r.Number > tx.revisionNumber {
			tx.revisionNumber = r.Number
		}
		return err
	}
	tx.get = func(id string, request bool) (domain.Operation, error) {
		var payload []byte
		query := `SELECT payload FROM hm_runtime_operations WHERE organization_id=$1 AND node_id=$2 AND id=$3`
		args := []any{org, node, id}
		if request {
			query = `SELECT payload FROM hm_runtime_operations WHERE organization_id=$1 AND request_id=$2`
			args = []any{org, id}
		}
		if err := db.QueryRow(ctx, query, args...).Scan(&payload); err != nil {
			return domain.Operation{}, normalizeError(err)
		}
		return decodeOperation(payload)
	}
	tx.save = func(op domain.Operation) error {
		// Command digests are private transport bookkeeping, persisted explicitly.
		payload, err := json.Marshal(operationRecord{Operation: op, PrivateDigest: op.CommandDigest})
		if err != nil {
			return err
		}
		_, err = db.Exec(ctx, `INSERT INTO hm_runtime_operations(id,organization_id,cluster_id,node_id,request_id,status,created_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO UPDATE SET status=excluded.status,payload=excluded.payload`, op.ID, org, n.ClusterID, node, op.RequestID, op.Status, op.CreatedAt, payload)
		return err
	}
	tx.audit = func(action, op string) error {
		_, err := db.Exec(ctx, `INSERT INTO hm_audit_events(id,organization_id,cluster_id,node_id,action,actor_user_id,operation_id,created_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,clock_timestamp())`, domain.NewID(), org, n.ClusterID, node, action, domain.Actor(ctx), op)
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	raw, err = json.Marshal(tx.state.Discovery)
	if err != nil {
		return err
	}
	if tx.state.Discovery == nil {
		raw = nil
	}
	_, err = db.Exec(ctx, `UPDATE hm_node_controls SET session_id=NULLIF($3,'')::uuid,connected_at=$4,last_seen=$5,discovery=$6,pending_operation_id=NULLIF($7,'')::uuid,current_revision_id=NULLIF($8,'')::uuid WHERE organization_id=$1 AND node_id=$2`, org, node, tx.state.SessionID, tx.state.ConnectedAt, tx.state.LastSeen, raw, tx.state.PendingOperationID, tx.state.CurrentRevisionID)
	return err
}
func (m *Memory) OpenControl(ctx context.Context, org, node, session string) (domain.NodeControl, error) {
	return openControl(ctx, m, org, node, session)
}
func (p *Postgres) OpenControl(ctx context.Context, org, node, session string) (domain.NodeControl, error) {
	return openControl(ctx, p, org, node, session)
}
func (m *Memory) ReportControl(ctx context.Context, org, node, session string, d *domain.Discovery, r *domain.OperationResult) (domain.NodeControl, *domain.Operation, error) {
	return reportControl(ctx, m, org, node, session, d, r)
}
func (p *Postgres) ReportControl(ctx context.Context, org, node, session string, d *domain.Discovery, r *domain.OperationResult) (domain.NodeControl, *domain.Operation, error) {
	return reportControl(ctx, p, org, node, session, d, r)
}
func (m *Memory) CreateOperation(ctx context.Context, org string, o domain.Operation) (domain.Operation, error) {
	return createOperation(ctx, m, org, o)
}
func (p *Postgres) CreateOperation(ctx context.Context, org string, o domain.Operation) (domain.Operation, error) {
	return createOperation(ctx, p, org, o)
}
func (m *Memory) GetControl(ctx context.Context, org, node string) (domain.NodeControl, []domain.Operation, error) {
	var state domain.NodeControl
	ops := []domain.Operation{}
	err := m.controlTransaction(ctx, org, node, func(tx *controlTransaction) error {
		if err := expirePending(tx); err != nil {
			return err
		}
		state = tx.state
		for _, op := range m.operations {
			if op.OrganizationID == org && op.NodeID == node {
				ops = append(ops, op)
			}
		}
		sort.Slice(ops, func(i, j int) bool { return ops[i].CreatedAt.After(ops[j].CreatedAt) })
		if len(ops) > 50 {
			ops = ops[:50]
		}
		return nil
	})
	return state, ops, err
}
func (p *Postgres) GetControl(ctx context.Context, org, node string) (domain.NodeControl, []domain.Operation, error) {
	var state domain.NodeControl
	err := p.controlTransaction(ctx, org, node, func(tx *controlTransaction) error {
		if err := expirePending(tx); err != nil {
			return err
		}
		state = tx.state
		return nil
	})
	if err != nil {
		return state, nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT payload FROM hm_runtime_operations WHERE organization_id=$1 AND node_id=$2 ORDER BY created_at DESC,id DESC LIMIT 50`, org, node)
	if err != nil {
		return state, nil, normalizeError(err)
	}
	defer rows.Close()
	ops := []domain.Operation{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return state, nil, err
		}
		op, err := decodeOperation(raw)
		if err != nil {
			return state, nil, err
		}
		ops = append(ops, op)
	}
	return state, ops, rows.Err()
}
