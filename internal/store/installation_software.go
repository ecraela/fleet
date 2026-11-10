package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
	"github.com/jackc/pgx/v5"
)

type InstallationRequest struct {
	RequestID       string
	Action          string
	TargetVersion   string
	TargetRevision  string
	ManagerSHA256   string
	Platform        string
	ManifestSHA256  string
	Publisher       string
	ExpectedVersion string
}

type InstallationSoftwareStore interface {
	CreateInstallationUpdate(context.Context, string, InstallationRequest) (software.InstallationUpdate, error)
	InstallationUpdateByRequest(context.Context, string, string) (software.InstallationUpdate, error)
	ListInstallationUpdates(context.Context, string) ([]software.InstallationUpdate, error)
	ActiveInstallationUpdate(context.Context, string) (software.InstallationUpdate, error)
	TransitionInstallationUpdate(context.Context, string, string, string, string) (software.InstallationUpdate, error)
	HeartbeatInstallationUpdater(context.Context, string, software.Updater) (software.Updater, error)
	InstallationUpdater(context.Context, string) (software.Updater, error)
	HasActiveSoftwareUpdates(context.Context, string) (bool, error)
}

func validInstallationRequest(r InstallationRequest) bool {
	return domain.ValidID(r.RequestID) && (r.Action == "upgrade" || r.Action == "rollback") &&
		software.ValidVersion(r.TargetVersion) && software.ValidRevision(r.TargetRevision) &&
		domain.ValidDigest(r.ManagerSHA256) && (r.Platform == "linux/amd64" || r.Platform == "linux/arm64") &&
		domain.ValidDigest(r.ManifestSHA256) && validPublisher(r.Publisher) && software.ValidVersion(r.ExpectedVersion)
}

func sameInstallationTarget(u software.InstallationUpdate, r InstallationRequest) bool {
	return u.RequestID == r.RequestID && u.Action == r.Action && u.TargetVersion == r.TargetVersion &&
		u.TargetRevision == r.TargetRevision && u.ManagerSHA256 == r.ManagerSHA256 && u.Platform == r.Platform &&
		u.ManifestSHA256 == r.ManifestSHA256 && u.Publisher == r.Publisher && u.ExpectedVersion == r.ExpectedVersion
}

func (m *Memory) CreateInstallationUpdate(ctx context.Context, org string, r InstallationRequest) (software.InstallationUpdate, error) {
	if err := ctx.Err(); err != nil {
		return software.InstallationUpdate{}, err
	}
	if !domain.ValidID(org) || !validInstallationRequest(r) {
		return software.InstallationUpdate{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.installationRequests[org+":"+r.RequestID]; ok {
		old := m.installationUpdates[id]
		if sameInstallationTarget(old, r) {
			return old, nil
		}
		return software.InstallationUpdate{}, domain.ErrConflict
	}
	for _, update := range m.installationUpdates {
		if update.Active() {
			return software.InstallationUpdate{}, domain.ErrConflict
		}
	}
	for _, update := range m.softwareUpdates {
		if update.Active() {
			return software.InstallationUpdate{}, domain.ErrConflict
		}
	}
	now := time.Now().UTC()
	u := software.InstallationUpdate{ID: domain.NewID(), OrganizationID: org, RequestID: r.RequestID, Action: r.Action,
		TargetVersion: r.TargetVersion, TargetRevision: r.TargetRevision, ManagerSHA256: r.ManagerSHA256, Platform: r.Platform,
		ManifestSHA256: r.ManifestSHA256, Publisher: r.Publisher, ExpectedVersion: r.ExpectedVersion,
		Status: "queued", ActorUserID: domain.Actor(ctx), CreatedAt: now, UpdatedAt: now}
	m.installationUpdates[u.ID] = u
	m.installationRequests[org+":"+r.RequestID] = u.ID
	m.installationEvents = append(m.installationEvents, u)
	return u, nil
}

func (m *Memory) InstallationUpdateByRequest(ctx context.Context, org, requestID string) (software.InstallationUpdate, error) {
	if err := ctx.Err(); err != nil {
		return software.InstallationUpdate{}, err
	}
	if !domain.ValidID(org) || !domain.ValidID(requestID) {
		return software.InstallationUpdate{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.installationRequests[org+":"+requestID]; ok {
		return m.installationUpdates[id], nil
	}
	return software.InstallationUpdate{}, domain.ErrNotFound
}

func (m *Memory) ListInstallationUpdates(ctx context.Context, org string) ([]software.InstallationUpdate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !domain.ValidID(org) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []software.InstallationUpdate{}
	for _, u := range m.installationUpdates {
		if u.OrganizationID == org {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt) || out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].ID > out[j].ID
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out, nil
}

func (m *Memory) ActiveInstallationUpdate(ctx context.Context, org string) (software.InstallationUpdate, error) {
	if err := ctx.Err(); err != nil {
		return software.InstallationUpdate{}, err
	}
	if !domain.ValidID(org) {
		return software.InstallationUpdate{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.installationUpdates {
		if u.OrganizationID == org && u.Active() {
			return u, nil
		}
	}
	return software.InstallationUpdate{}, domain.ErrNotFound
}

func (m *Memory) TransitionInstallationUpdate(ctx context.Context, org, id, status, code string) (software.InstallationUpdate, error) {
	if err := ctx.Err(); err != nil {
		return software.InstallationUpdate{}, err
	}
	if !validInstallationTransition(org, id, status, code) {
		return software.InstallationUpdate{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.installationUpdates[id]
	if !ok || u.OrganizationID != org {
		return software.InstallationUpdate{}, domain.ErrNotFound
	}
	if u.Status == status && u.ErrorCode == code {
		return u, nil
	}
	if !software.ValidTransition(u.Status, status) {
		return software.InstallationUpdate{}, domain.ErrConflict
	}
	now := time.Now().UTC()
	u.Status, u.ErrorCode, u.UpdatedAt = status, code, now
	if u.Terminal() {
		u.CompletedAt = &now
	}
	m.installationUpdates[id] = u
	m.installationEvents = append(m.installationEvents, u)
	return u, nil
}

func validInstallationTransition(org, id, status, code string) bool {
	return domain.ValidID(org) && domain.ValidID(id) && software.ValidErrorCode(code) &&
		((status == "needs_attention" || status == "rolled_back") && code != "" ||
			status != "needs_attention" && status != "rolled_back" && code == "")
}

func (m *Memory) HeartbeatInstallationUpdater(ctx context.Context, org string, value software.Updater) (software.Updater, error) {
	if err := ctx.Err(); err != nil {
		return software.Updater{}, err
	}
	if !domain.ValidID(org) || !validUpdater(value) || value.IngestVersion == "" && value.Ready || value.IngestVersion != "" && !software.ValidVersion(value.IngestVersion) {
		return software.Updater{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.installationUpdater != nil && m.installationUpdaterOrg != org {
		return software.Updater{}, domain.ErrConflict
	}
	value.LastSeen = time.Now().UTC()
	m.installationUpdater = &value
	m.installationUpdaterOrg = org
	return value, nil
}

func (m *Memory) InstallationUpdater(ctx context.Context, org string) (software.Updater, error) {
	if err := ctx.Err(); err != nil {
		return software.Updater{}, err
	}
	if !domain.ValidID(org) {
		return software.Updater{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.installationUpdater == nil || m.installationUpdaterOrg != org {
		return software.Updater{}, domain.ErrNotFound
	}
	return *m.installationUpdater, nil
}

func (m *Memory) HasActiveSoftwareUpdates(ctx context.Context, org string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !domain.ValidID(org) {
		return false, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.softwareUpdates {
		if u.OrganizationID == org && u.Active() {
			return true, nil
		}
	}
	return false, nil
}

func (p *Postgres) CreateInstallationUpdate(ctx context.Context, org string, r InstallationRequest) (software.InstallationUpdate, error) {
	var result software.InstallationUpdate
	if !domain.ValidID(org) || !validInstallationRequest(r) {
		return result, domain.ErrInvalidInput
	}
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "software:installation"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "installation:"+org+":"+r.RequestID); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT payload FROM hm_installation_updates WHERE organization_id=$1 AND request_id=$2`, org, r.RequestID).Scan(&raw)
		if err == nil {
			if json.Unmarshal(raw, &result) != nil || !sameInstallationTarget(result, r) {
				return domain.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var agentsActive bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_software_updates WHERE status IN ('queued','preparing','activating','verifying','needs_attention'))`).Scan(&agentsActive); err != nil {
			return err
		}
		if agentsActive {
			return domain.ErrConflict
		}
		now := time.Now().UTC()
		result = software.InstallationUpdate{ID: domain.NewID(), OrganizationID: org, RequestID: r.RequestID, Action: r.Action,
			TargetVersion: r.TargetVersion, TargetRevision: r.TargetRevision, ManagerSHA256: r.ManagerSHA256, Platform: r.Platform,
			ManifestSHA256: r.ManifestSHA256, Publisher: r.Publisher, ExpectedVersion: r.ExpectedVersion,
			Status: "queued", ActorUserID: domain.Actor(ctx), CreatedAt: now, UpdatedAt: now}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO hm_installation_updates(id,organization_id,request_id,status,created_at,updated_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7)`, result.ID, org, r.RequestID, result.Status, now, now, payload); err != nil {
			return err
		}
		return auditInstallationUpdate(ctx, tx, result)
	})
	return result, normalizeError(err)
}

func (p *Postgres) InstallationUpdateByRequest(ctx context.Context, org, requestID string) (software.InstallationUpdate, error) {
	var result software.InstallationUpdate
	if !domain.ValidID(org) || !domain.ValidID(requestID) {
		return result, domain.ErrInvalidInput
	}
	var raw []byte
	err := p.pool.QueryRow(ctx, `SELECT payload FROM hm_installation_updates WHERE organization_id=$1 AND request_id=$2`, org, requestID).Scan(&raw)
	if err != nil {
		return result, normalizeError(err)
	}
	if json.Unmarshal(raw, &result) != nil {
		return software.InstallationUpdate{}, errors.New("invalid persisted installation update")
	}
	return result, nil
}

func (p *Postgres) ListInstallationUpdates(ctx context.Context, org string) ([]software.InstallationUpdate, error) {
	if !domain.ValidID(org) {
		return nil, domain.ErrInvalidInput
	}
	rows, err := p.pool.Query(ctx, `SELECT payload FROM hm_installation_updates WHERE organization_id=$1 ORDER BY created_at DESC,id DESC LIMIT 20`, org)
	if err != nil {
		return nil, normalizeError(err)
	}
	defer rows.Close()
	out := []software.InstallationUpdate{}
	for rows.Next() {
		var raw []byte
		var u software.InstallationUpdate
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &u) != nil {
			return nil, errors.New("invalid persisted installation update")
		}
		out = append(out, u)
	}
	return out, normalizeError(rows.Err())
}

func (p *Postgres) ActiveInstallationUpdate(ctx context.Context, org string) (software.InstallationUpdate, error) {
	var result software.InstallationUpdate
	if !domain.ValidID(org) {
		return result, domain.ErrInvalidInput
	}
	var raw []byte
	err := p.pool.QueryRow(ctx, `SELECT payload FROM hm_installation_updates WHERE organization_id=$1 AND status IN ('queued','preparing','activating','verifying','needs_attention')`, org).Scan(&raw)
	if err != nil {
		return result, normalizeError(err)
	}
	if json.Unmarshal(raw, &result) != nil {
		return software.InstallationUpdate{}, errors.New("invalid persisted installation update")
	}
	return result, nil
}

func (p *Postgres) TransitionInstallationUpdate(ctx context.Context, org, id, status, code string) (software.InstallationUpdate, error) {
	var result software.InstallationUpdate
	if !validInstallationTransition(org, id, status, code) {
		return result, domain.ErrInvalidInput
	}
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT payload FROM hm_installation_updates WHERE organization_id=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&raw); err != nil {
			return err
		}
		if json.Unmarshal(raw, &result) != nil {
			return errors.New("invalid persisted installation update")
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
		if _, err = tx.Exec(ctx, `UPDATE hm_installation_updates SET status=$2,updated_at=$3,completed_at=$4,payload=$5 WHERE id=$1`, id, status, now, result.CompletedAt, payload); err != nil {
			return err
		}
		return auditInstallationUpdate(ctx, tx, result)
	})
	return result, normalizeError(err)
}

func auditInstallationUpdate(ctx context.Context, tx pgx.Tx, u software.InstallationUpdate) error {
	var actor any
	if domain.ValidID(domain.Actor(ctx)) {
		actor = domain.Actor(ctx)
	}
	_, err := tx.Exec(ctx, `INSERT INTO hm_installation_update_events(id,update_id,organization_id,actor_user_id,status,error_code,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, domain.NewID(), u.ID, u.OrganizationID, actor, u.Status, u.ErrorCode, time.Now().UTC())
	return err
}

func (p *Postgres) HeartbeatInstallationUpdater(ctx context.Context, org string, value software.Updater) (software.Updater, error) {
	if !domain.ValidID(org) || !validUpdater(value) || value.IngestVersion == "" && value.Ready || value.IngestVersion != "" && !software.ValidVersion(value.IngestVersion) {
		return software.Updater{}, domain.ErrInvalidInput
	}
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		var existing string
		err := tx.QueryRow(ctx, `SELECT organization_id::text FROM hm_installation_updater WHERE id=true FOR UPDATE`).Scan(&existing)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if existing != "" && existing != org {
			return domain.ErrConflict
		}
		value.LastSeen = time.Now().UTC()
		_, err = tx.Exec(ctx, `INSERT INTO hm_installation_updater(id,organization_id,current_version,ingest_version,previous_version,ready,last_seen) VALUES(true,$1,$2,$3,$4,$5,$6)
		 ON CONFLICT (id) DO UPDATE SET current_version=EXCLUDED.current_version,ingest_version=EXCLUDED.ingest_version,previous_version=EXCLUDED.previous_version,ready=EXCLUDED.ready,last_seen=EXCLUDED.last_seen`, org, value.CurrentVersion, value.IngestVersion, value.PreviousVersion, value.Ready, value.LastSeen)
		return err
	})
	return value, normalizeError(err)
}

func (p *Postgres) InstallationUpdater(ctx context.Context, org string) (software.Updater, error) {
	var value software.Updater
	if !domain.ValidID(org) {
		return value, domain.ErrInvalidInput
	}
	err := p.pool.QueryRow(ctx, `SELECT current_version,ingest_version,previous_version,ready,last_seen FROM hm_installation_updater WHERE id=true AND organization_id=$1`, org).Scan(&value.CurrentVersion, &value.IngestVersion, &value.PreviousVersion, &value.Ready, &value.LastSeen)
	return value, normalizeError(err)
}

func (p *Postgres) HasActiveSoftwareUpdates(ctx context.Context, org string) (bool, error) {
	var active bool
	if !domain.ValidID(org) {
		return false, domain.ErrInvalidInput
	}
	err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_software_updates WHERE organization_id=$1 AND status IN ('queued','preparing','activating','verifying','needs_attention'))`, org).Scan(&active)
	return active, normalizeError(err)
}
