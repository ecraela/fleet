package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
	"github.com/jackc/pgx/v5"
)

//go:embed software_schema.sql
var softwareSchemaSQL string

type SoftwareRequest struct {
	RequestID       string
	Action          string
	TargetVersion   string
	TargetRevision  string
	AgentSHA256     string
	Platform        string
	ManifestSHA256  string
	Publisher       string
	ExpectedVersion string
}

type SoftwareStore interface {
	CreateSoftwareUpdate(context.Context, string, string, SoftwareRequest) (software.Update, error)
	SoftwareUpdateByRequest(context.Context, string, string) (software.Update, error)
	HeartbeatSoftwareUpdater(context.Context, string, string, software.Updater) (software.Updater, error)
	SoftwareUpdater(context.Context, string, string) (software.Updater, error)
	ListSoftwareUpdates(context.Context, string, string) ([]software.Update, error)
	ActiveSoftwareUpdate(context.Context, string, string) (software.Update, error)
	TransitionSoftwareUpdate(context.Context, string, string, string, string, string) (software.Update, error)
}

func validUpdater(value software.Updater) bool {
	return software.ValidVersion(value.CurrentVersion) &&
		(value.PreviousVersion == "" || software.ValidVersion(value.PreviousVersion) && value.PreviousVersion != value.CurrentVersion)
}

func (m *Memory) HeartbeatSoftwareUpdater(ctx context.Context, org, nodeID string, value software.Updater) (software.Updater, error) {
	if err := ctx.Err(); err != nil {
		return software.Updater{}, err
	}
	if !domain.ValidID(org) || !domain.ValidID(nodeID) || !validUpdater(value) {
		return software.Updater{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.nodes[nodeID]
	if !ok || node.OrganizationID != org || node.RevokedAt != nil {
		return software.Updater{}, domain.ErrNotFound
	}
	value.LastSeen = time.Now().UTC()
	m.softwareUpdaters[nodeID] = value
	return value, nil
}

func (m *Memory) SoftwareUpdater(ctx context.Context, org, nodeID string) (software.Updater, error) {
	if err := ctx.Err(); err != nil {
		return software.Updater{}, err
	}
	if !domain.ValidID(org) || !domain.ValidID(nodeID) {
		return software.Updater{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.nodes[nodeID]
	if !ok || node.OrganizationID != org || node.RevokedAt != nil {
		return software.Updater{}, domain.ErrNotFound
	}
	value, ok := m.softwareUpdaters[nodeID]
	if !ok {
		return software.Updater{}, domain.ErrNotFound
	}
	return value, nil
}

func (p *Postgres) HeartbeatSoftwareUpdater(ctx context.Context, org, nodeID string, value software.Updater) (software.Updater, error) {
	if !domain.ValidID(org) || !domain.ValidID(nodeID) || !validUpdater(value) {
		return software.Updater{}, domain.ErrInvalidInput
	}
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		var cluster string
		if err := tx.QueryRow(ctx, `SELECT cluster_id::text FROM hm_nodes WHERE organization_id=$1 AND id=$2 AND revoked_at IS NULL FOR UPDATE`, org, nodeID).Scan(&cluster); err != nil {
			return err
		}
		value.LastSeen = time.Now().UTC()
		_, err := tx.Exec(ctx, `INSERT INTO hm_software_updaters(node_id,organization_id,cluster_id,current_version,previous_version,ready,last_seen)
		 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (node_id) DO UPDATE SET current_version=EXCLUDED.current_version,
		 previous_version=EXCLUDED.previous_version,ready=EXCLUDED.ready,last_seen=EXCLUDED.last_seen`,
			nodeID, org, cluster, value.CurrentVersion, value.PreviousVersion, value.Ready, value.LastSeen)
		return err
	})
	return value, normalizeError(err)
}

func (p *Postgres) SoftwareUpdater(ctx context.Context, org, nodeID string) (software.Updater, error) {
	var value software.Updater
	if !domain.ValidID(org) || !domain.ValidID(nodeID) {
		return value, domain.ErrInvalidInput
	}
	err := p.pool.QueryRow(ctx, `SELECT u.current_version,u.previous_version,u.ready,u.last_seen FROM hm_software_updaters u JOIN hm_nodes n ON n.id=u.node_id AND n.organization_id=u.organization_id WHERE u.organization_id=$1 AND u.node_id=$2 AND n.revoked_at IS NULL`, org, nodeID).Scan(&value.CurrentVersion, &value.PreviousVersion, &value.Ready, &value.LastSeen)
	return value, normalizeError(err)
}

func (m *Memory) SoftwareUpdateByRequest(ctx context.Context, org, requestID string) (software.Update, error) {
	if err := ctx.Err(); err != nil {
		return software.Update{}, err
	}
	if !domain.ValidID(org) || !domain.ValidID(requestID) {
		return software.Update{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.softwareRequests[org+":"+requestID]; ok {
		return m.softwareUpdates[id], nil
	}
	return software.Update{}, domain.ErrNotFound
}

func (p *Postgres) SoftwareUpdateByRequest(ctx context.Context, org, requestID string) (software.Update, error) {
	var result software.Update
	if !domain.ValidID(org) || !domain.ValidID(requestID) {
		return result, domain.ErrInvalidInput
	}
	var raw []byte
	err := p.pool.QueryRow(ctx, `SELECT payload FROM hm_software_updates WHERE organization_id=$1 AND request_id=$2`, org, requestID).Scan(&raw)
	if err != nil {
		return result, normalizeError(err)
	}
	if json.Unmarshal(raw, &result) != nil {
		return software.Update{}, errors.New("invalid persisted software update")
	}
	return result, nil
}

func validSoftwareRequest(r SoftwareRequest) bool {
	return domain.ValidID(r.RequestID) && (r.Action == "upgrade" || r.Action == "rollback") &&
		software.ValidVersion(r.TargetVersion) && domain.ValidDigest(r.ManifestSHA256) &&
		software.ValidRevision(r.TargetRevision) &&
		domain.ValidDigest(r.AgentSHA256) && (r.Platform == "linux/amd64" || r.Platform == "linux/arm64") &&
		validPublisher(r.Publisher) &&
		software.ValidVersion(r.ExpectedVersion)
}

func validPublisher(value string) bool {
	return value == "sigstore:ecraela/fleet" || len(value) == len("ed25519:")+64 && value[:8] == "ed25519:" && domain.ValidDigest(value[8:])
}

func sameSoftwareTarget(u software.Update, r SoftwareRequest) bool {
	return u.RequestID == r.RequestID && u.Action == r.Action && u.TargetVersion == r.TargetVersion &&
		u.TargetRevision == r.TargetRevision && u.AgentSHA256 == r.AgentSHA256 && u.Platform == r.Platform &&
		u.ManifestSHA256 == r.ManifestSHA256 && u.Publisher == r.Publisher && u.ExpectedVersion == r.ExpectedVersion
}

func (m *Memory) CreateSoftwareUpdate(ctx context.Context, org, nodeID string, r SoftwareRequest) (software.Update, error) {
	if err := ctx.Err(); err != nil {
		return software.Update{}, err
	}
	if !domain.ValidID(org) || !domain.ValidID(nodeID) || !validSoftwareRequest(r) {
		return software.Update{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.nodes[nodeID]
	if !ok || node.OrganizationID != org || node.RevokedAt != nil {
		return software.Update{}, domain.ErrNotFound
	}
	if id, ok := m.softwareRequests[org+":"+r.RequestID]; ok {
		old := m.softwareUpdates[id]
		if old.NodeID == nodeID && sameSoftwareTarget(old, r) {
			return old, nil
		}
		return software.Update{}, domain.ErrConflict
	}
	for _, update := range m.softwareUpdates {
		if update.OrganizationID == org && update.ClusterID == node.ClusterID && update.Active() {
			return software.Update{}, domain.ErrConflict
		}
	}
	for _, update := range m.installationUpdates {
		if update.Active() {
			return software.Update{}, domain.ErrConflict
		}
	}
	now := time.Now().UTC()
	u := software.Update{ID: domain.NewID(), OrganizationID: org, ClusterID: node.ClusterID, NodeID: nodeID,
		RequestID: r.RequestID, Action: r.Action, TargetVersion: r.TargetVersion, TargetRevision: r.TargetRevision,
		AgentSHA256: r.AgentSHA256, Platform: r.Platform, ManifestSHA256: r.ManifestSHA256,
		Publisher: r.Publisher, ExpectedVersion: r.ExpectedVersion, Status: "queued", ActorUserID: domain.Actor(ctx), CreatedAt: now, UpdatedAt: now}
	m.softwareUpdates[u.ID] = u
	m.softwareRequests[org+":"+r.RequestID] = u.ID
	m.softwareEvents = append(m.softwareEvents, u)
	return u, nil
}

func (m *Memory) ListSoftwareUpdates(ctx context.Context, org, nodeID string) ([]software.Update, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !domain.ValidID(org) || !domain.ValidID(nodeID) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[nodeID]
	if !ok || n.OrganizationID != org {
		return nil, domain.ErrNotFound
	}
	out := []software.Update{}
	for _, u := range m.softwareUpdates {
		if u.OrganizationID == org && u.NodeID == nodeID {
			out = append(out, u)
		}
	}
	sortSoftwareUpdates(out)
	if len(out) > 20 {
		out = out[:20]
	}
	return out, nil
}

func (m *Memory) ActiveSoftwareUpdate(ctx context.Context, org, nodeID string) (software.Update, error) {
	if err := ctx.Err(); err != nil {
		return software.Update{}, err
	}
	if !domain.ValidID(org) || !domain.ValidID(nodeID) {
		return software.Update{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[nodeID]
	if !ok || n.OrganizationID != org || n.RevokedAt != nil {
		return software.Update{}, domain.ErrNotFound
	}
	for _, u := range m.softwareUpdates {
		if u.OrganizationID == org && u.NodeID == nodeID && u.Active() {
			return u, nil
		}
	}
	return software.Update{}, domain.ErrNotFound
}

func (m *Memory) TransitionSoftwareUpdate(ctx context.Context, org, nodeID, id, status, code string) (software.Update, error) {
	if err := ctx.Err(); err != nil {
		return software.Update{}, err
	}
	if !domain.ValidID(org) || !domain.ValidID(nodeID) || !domain.ValidID(id) || !software.ValidErrorCode(code) ||
		(status == "needs_attention" || status == "rolled_back") && code == "" ||
		(status != "needs_attention" && status != "rolled_back") && code != "" {
		return software.Update{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.softwareUpdates[id]
	if !ok || u.OrganizationID != org || u.NodeID != nodeID {
		return software.Update{}, domain.ErrNotFound
	}
	if u.Status == status && u.ErrorCode == code {
		return u, nil
	}
	if !software.ValidTransition(u.Status, status) {
		return software.Update{}, domain.ErrConflict
	}
	now := time.Now().UTC()
	u.Status, u.ErrorCode, u.UpdatedAt = status, code, now
	if u.Terminal() {
		u.CompletedAt = &now
	}
	m.softwareUpdates[id] = u
	m.softwareEvents = append(m.softwareEvents, u)
	return u, nil
}

func sortSoftwareUpdates(rows []software.Update) {
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].CreatedAt.After(rows[j].CreatedAt) || rows[i].CreatedAt.Equal(rows[j].CreatedAt) && rows[i].ID > rows[j].ID
	})
}

func (p *Postgres) CreateSoftwareUpdate(ctx context.Context, org, nodeID string, r SoftwareRequest) (software.Update, error) {
	if !domain.ValidID(org) || !domain.ValidID(nodeID) || !validSoftwareRequest(r) {
		return software.Update{}, domain.ErrInvalidInput
	}
	var result software.Update
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "software:installation"); err != nil {
			return err
		}
		// Serialize a repeated request across manager replicas before checking it.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":"+r.RequestID); err != nil {
			return err
		}
		var existing []byte
		err := tx.QueryRow(ctx, `SELECT payload FROM hm_software_updates WHERE organization_id=$1 AND request_id=$2`, org, r.RequestID).Scan(&existing)
		if err == nil {
			if json.Unmarshal(existing, &result) != nil || result.NodeID != nodeID || !sameSoftwareTarget(result, r) {
				return domain.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var installationActive bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_installation_updates WHERE status IN ('queued','preparing','activating','verifying','needs_attention'))`).Scan(&installationActive); err != nil {
			return err
		}
		if installationActive {
			return domain.ErrConflict
		}
		var cluster string
		if err := tx.QueryRow(ctx, `SELECT cluster_id::text FROM hm_nodes WHERE organization_id=$1 AND id=$2 AND revoked_at IS NULL FOR UPDATE`, org, nodeID).Scan(&cluster); err != nil {
			return err
		}
		now := time.Now().UTC()
		result = software.Update{ID: domain.NewID(), OrganizationID: org, ClusterID: cluster, NodeID: nodeID,
			RequestID: r.RequestID, Action: r.Action, TargetVersion: r.TargetVersion, TargetRevision: r.TargetRevision,
			AgentSHA256: r.AgentSHA256, Platform: r.Platform, ManifestSHA256: r.ManifestSHA256,
			Publisher: r.Publisher, ExpectedVersion: r.ExpectedVersion, Status: "queued", ActorUserID: domain.Actor(ctx), CreatedAt: now, UpdatedAt: now}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hm_software_updates(id,organization_id,cluster_id,node_id,request_id,status,created_at,updated_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			result.ID, org, cluster, nodeID, r.RequestID, result.Status, now, now, payload); err != nil {
			return err
		}
		return auditSoftwareUpdate(ctx, tx, result)
	})
	return result, normalizeError(err)
}

func (p *Postgres) ListSoftwareUpdates(ctx context.Context, org, nodeID string) ([]software.Update, error) {
	if !domain.ValidID(org) || !domain.ValidID(nodeID) {
		return nil, domain.ErrInvalidInput
	}
	if _, err := p.GetNode(ctx, org, nodeID); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT payload FROM hm_software_updates WHERE organization_id=$1 AND node_id=$2 ORDER BY created_at DESC,id DESC LIMIT 20`, org, nodeID)
	if err != nil {
		return nil, normalizeError(err)
	}
	defer rows.Close()
	out := []software.Update{}
	for rows.Next() {
		var raw []byte
		var u software.Update
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &u) != nil {
			return nil, errors.New("invalid persisted software update")
		}
		out = append(out, u)
	}
	return out, normalizeError(rows.Err())
}

func (p *Postgres) ActiveSoftwareUpdate(ctx context.Context, org, nodeID string) (software.Update, error) {
	var result software.Update
	if !domain.ValidID(org) || !domain.ValidID(nodeID) {
		return result, domain.ErrInvalidInput
	}
	var raw []byte
	err := p.pool.QueryRow(ctx, `SELECT u.payload FROM hm_software_updates u JOIN hm_nodes n ON n.id=u.node_id AND n.organization_id=u.organization_id WHERE u.organization_id=$1 AND u.node_id=$2 AND n.revoked_at IS NULL AND u.status IN ('queued','preparing','activating','verifying','needs_attention')`, org, nodeID).Scan(&raw)
	if err != nil {
		return result, normalizeError(err)
	}
	if json.Unmarshal(raw, &result) != nil {
		return software.Update{}, errors.New("invalid persisted software update")
	}
	return result, nil
}

func (p *Postgres) TransitionSoftwareUpdate(ctx context.Context, org, nodeID, id, status, code string) (software.Update, error) {
	var result software.Update
	if !domain.ValidID(org) || !domain.ValidID(nodeID) || !domain.ValidID(id) || !software.ValidErrorCode(code) ||
		(status == "needs_attention" || status == "rolled_back") && code == "" ||
		(status != "needs_attention" && status != "rolled_back") && code != "" {
		return result, domain.ErrInvalidInput
	}
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT payload FROM hm_software_updates WHERE organization_id=$1 AND node_id=$2 AND id=$3 FOR UPDATE`, org, nodeID, id).Scan(&raw); err != nil {
			return err
		}
		if json.Unmarshal(raw, &result) != nil {
			return errors.New("invalid persisted software update")
		}
		if result.Status == status && result.ErrorCode == code {
			return nil
		}
		if !software.ValidTransition(result.Status, status) {
			return domain.ErrConflict
		}
		now := time.Now().UTC()
		result.Status, result.ErrorCode, result.UpdatedAt = status, code, now
		if result.Terminal() {
			result.CompletedAt = &now
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE hm_software_updates SET status=$2,updated_at=$3,completed_at=$4,payload=$5 WHERE id=$1`, id, status, now, result.CompletedAt, payload); err != nil {
			return err
		}
		return auditSoftwareUpdate(ctx, tx, result)
	})
	return result, normalizeError(err)
}

func auditSoftwareUpdate(ctx context.Context, tx pgx.Tx, u software.Update) error {
	var actor any
	if domain.ValidID(domain.Actor(ctx)) {
		actor = domain.Actor(ctx)
	}
	_, err := tx.Exec(ctx, `INSERT INTO hm_software_update_events(id,update_id,organization_id,cluster_id,node_id,actor_user_id,status,error_code,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		domain.NewID(), u.ID, u.OrganizationID, u.ClusterID, u.NodeID, actor, u.Status, u.ErrorCode, time.Now().UTC())
	return err
}
