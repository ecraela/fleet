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

//go:embed fleet_software_schema.sql
var fleetSoftwareSchemaSQL string

type FleetSoftwareRequest struct {
	RequestID      string
	TargetVersion  string
	ManifestSHA256 string
	Fingerprint    string
	Steps          []software.FleetStep
}

type FleetSoftwareStore interface {
	CreateFleetUpdate(context.Context, string, FleetSoftwareRequest) (software.FleetUpdate, error)
	FleetUpdateByRequest(context.Context, string, string) (software.FleetUpdate, error)
	ListFleetUpdates(context.Context, string) ([]software.FleetUpdate, error)
	ActiveFleetUpdates(context.Context) ([]software.FleetUpdate, error)
	AdvanceFleetUpdate(context.Context, string, string, int, int, string, string) (software.FleetUpdate, error)
}

func (m *Memory) FleetUpdateByRequest(ctx context.Context, org, requestID string) (software.FleetUpdate, error) {
	if err := ctx.Err(); err != nil {
		return software.FleetUpdate{}, err
	}
	if !domain.ValidID(org) || !domain.ValidID(requestID) {
		return software.FleetUpdate{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, found := m.fleetRequests[org+":"+requestID]; found {
		return m.fleetUpdates[id], nil
	}
	return software.FleetUpdate{}, domain.ErrNotFound
}

func (p *Postgres) FleetUpdateByRequest(ctx context.Context, org, requestID string) (software.FleetUpdate, error) {
	var update software.FleetUpdate
	if !domain.ValidID(org) || !domain.ValidID(requestID) {
		return update, domain.ErrInvalidInput
	}
	var raw []byte
	if err := p.pool.QueryRow(ctx, `SELECT payload FROM hm_fleet_updates WHERE organization_id=$1 AND request_id=$2`, org, requestID).Scan(&raw); err != nil {
		return update, normalizeError(err)
	}
	if json.Unmarshal(raw, &update) != nil {
		return software.FleetUpdate{}, errors.New("invalid persisted fleet update")
	}
	return update, nil
}

type fleetStepWriterKey struct{}

// FleetStepWriter is used only by the manager's durable coordinator. Stores
// verify that the request is the current step of an active fleet update.
func FleetStepWriter(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, fleetStepWriterKey{}, id)
}

func validFleetRequest(r FleetSoftwareRequest) bool {
	if !domain.ValidID(r.RequestID) || !software.ValidVersion(r.TargetVersion) ||
		!domain.ValidDigest(r.ManifestSHA256) || !domain.ValidDigest(r.Fingerprint) ||
		len(r.Steps) == 0 || len(r.Steps) > 5001 {
		return false
	}
	requests, nodes := map[string]bool{}, map[string]bool{}
	manager := false
	for index, step := range r.Steps {
		if !domain.ValidID(step.RequestID) || requests[step.RequestID] ||
			!software.ValidVersion(step.ExpectedVersion) || len(step.Name) == 0 || len(step.Name) > 255 {
			return false
		}
		requests[step.RequestID] = true
		switch step.Role {
		case "manager":
			if manager || index != 0 || step.NodeID != "" || step.ClusterID != "" {
				return false
			}
			manager = true
		case "agent":
			if !domain.ValidID(step.NodeID) || !domain.ValidID(step.ClusterID) || nodes[step.NodeID] {
				return false
			}
			nodes[step.NodeID] = true
		default:
			return false
		}
	}
	return true
}

func fleetStepAuthorized(ctx context.Context, update software.FleetUpdate, org, role, nodeID, requestID string) bool {
	writer, _ := ctx.Value(fleetStepWriterKey{}).(string)
	return writer == update.ID && update.OrganizationID == org && update.Active() && update.NextStep < len(update.Steps) &&
		update.Steps[update.NextStep].RequestID == requestID && update.Steps[update.NextStep].Role == role &&
		update.Steps[update.NextStep].NodeID == nodeID
}

func (m *Memory) activeFleetUpdate() *software.FleetUpdate {
	for _, update := range m.fleetUpdates {
		if update.Active() {
			copy := update
			return &copy
		}
	}
	return nil
}

func (m *Memory) CreateFleetUpdate(ctx context.Context, org string, r FleetSoftwareRequest) (software.FleetUpdate, error) {
	if err := ctx.Err(); err != nil {
		return software.FleetUpdate{}, err
	}
	if !domain.ValidID(org) || !validFleetRequest(r) {
		return software.FleetUpdate{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.fleetRequests[org+":"+r.RequestID]; ok {
		prior := m.fleetUpdates[id]
		if prior.TargetVersion == r.TargetVersion && prior.ManifestSHA256 == r.ManifestSHA256 && prior.Fingerprint == r.Fingerprint {
			return prior, nil
		}
		return software.FleetUpdate{}, domain.ErrConflict
	}
	if m.activeFleetUpdate() != nil {
		return software.FleetUpdate{}, domain.ErrConflict
	}
	for _, step := range r.Steps {
		if step.Role != "agent" {
			continue
		}
		node, found := m.nodes[step.NodeID]
		if !found || node.OrganizationID != org || node.ClusterID != step.ClusterID || node.RevokedAt != nil {
			return software.FleetUpdate{}, domain.ErrNotFound
		}
	}
	for _, update := range m.installationUpdates {
		if update.Active() {
			return software.FleetUpdate{}, domain.ErrConflict
		}
	}
	for _, update := range m.softwareUpdates {
		if update.Active() {
			return software.FleetUpdate{}, domain.ErrConflict
		}
	}
	now := time.Now().UTC()
	update := software.FleetUpdate{ID: domain.NewID(), OrganizationID: org, RequestID: r.RequestID,
		TargetVersion: r.TargetVersion, ManifestSHA256: r.ManifestSHA256, Fingerprint: r.Fingerprint,
		Steps: append([]software.FleetStep{}, r.Steps...), Status: "queued", ActorUserID: domain.Actor(ctx), CreatedAt: now, UpdatedAt: now}
	m.fleetUpdates[update.ID] = update
	m.fleetRequests[org+":"+r.RequestID] = update.ID
	m.fleetEvents = append(m.fleetEvents, update)
	return update, nil
}

func sortFleetUpdates(rows []software.FleetUpdate) {
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].CreatedAt.After(rows[j].CreatedAt) || rows[i].CreatedAt.Equal(rows[j].CreatedAt) && rows[i].ID > rows[j].ID
	})
}

func (m *Memory) ListFleetUpdates(ctx context.Context, org string) ([]software.FleetUpdate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !domain.ValidID(org) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := []software.FleetUpdate{}
	for _, update := range m.fleetUpdates {
		if update.OrganizationID == org {
			rows = append(rows, update)
		}
	}
	sortFleetUpdates(rows)
	if len(rows) > 20 {
		rows = rows[:20]
	}
	return rows, nil
}

func (m *Memory) ActiveFleetUpdates(ctx context.Context) ([]software.FleetUpdate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := []software.FleetUpdate{}
	for _, update := range m.fleetUpdates {
		if update.Active() {
			rows = append(rows, update)
		}
	}
	sortFleetUpdates(rows)
	return rows, nil
}

func validFleetTransition(update software.FleetUpdate, before, next int, status, reason string) bool {
	if !update.Active() || update.NextStep != before || next < before || next > before+1 || next > len(update.Steps) || len(reason) > 256 {
		return false
	}
	switch status {
	case "running":
		return next < len(update.Steps) && reason == ""
	case "paused":
		return next == before && reason != ""
	case "succeeded":
		return next == len(update.Steps) && reason == ""
	default:
		return false
	}
}

func advanceFleet(update software.FleetUpdate, next int, status, reason string) software.FleetUpdate {
	now := time.Now().UTC()
	update.NextStep, update.Status, update.Reason, update.UpdatedAt = next, status, reason, now
	if status == "paused" || status == "succeeded" {
		update.CompletedAt = &now
	}
	return update
}

func (m *Memory) AdvanceFleetUpdate(ctx context.Context, org, id string, before, next int, status, reason string) (software.FleetUpdate, error) {
	if err := ctx.Err(); err != nil {
		return software.FleetUpdate{}, err
	}
	if !domain.ValidID(org) || !domain.ValidID(id) {
		return software.FleetUpdate{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	update, ok := m.fleetUpdates[id]
	if !ok || update.OrganizationID != org {
		return software.FleetUpdate{}, domain.ErrNotFound
	}
	if !validFleetTransition(update, before, next, status, reason) {
		return software.FleetUpdate{}, domain.ErrConflict
	}
	update = advanceFleet(update, next, status, reason)
	m.fleetUpdates[id] = update
	m.fleetEvents = append(m.fleetEvents, update)
	return update, nil
}

func (p *Postgres) CreateFleetUpdate(ctx context.Context, org string, r FleetSoftwareRequest) (software.FleetUpdate, error) {
	var update software.FleetUpdate
	if !domain.ValidID(org) || !validFleetRequest(r) {
		return update, domain.ErrInvalidInput
	}
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "software:installation"); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT payload FROM hm_fleet_updates WHERE organization_id=$1 AND request_id=$2`, org, r.RequestID).Scan(&raw)
		if err == nil {
			if json.Unmarshal(raw, &update) != nil || update.TargetVersion != r.TargetVersion || update.ManifestSHA256 != r.ManifestSHA256 || update.Fingerprint != r.Fingerprint {
				return domain.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_fleet_updates WHERE status IN ('queued','running')) OR
			EXISTS(SELECT 1 FROM hm_installation_updates WHERE status IN ('queued','preparing','activating','verifying','needs_attention')) OR
			EXISTS(SELECT 1 FROM hm_software_updates WHERE status IN ('queued','preparing','activating','verifying','needs_attention'))`).Scan(&active); err != nil {
			return err
		}
		if active {
			return domain.ErrConflict
		}
		expected := map[string]string{}
		ids := []string{}
		for _, step := range r.Steps {
			if step.Role == "agent" {
				expected[step.NodeID] = step.ClusterID
				ids = append(ids, step.NodeID)
			}
		}
		if len(ids) > 0 {
			rows, err := tx.Query(ctx, `SELECT id::text,cluster_id::text FROM hm_nodes WHERE organization_id=$1 AND revoked_at IS NULL AND id=ANY($2::uuid[])`, org, ids)
			if err != nil {
				return err
			}
			seen := 0
			for rows.Next() {
				var id, cluster string
				if err := rows.Scan(&id, &cluster); err != nil {
					rows.Close()
					return err
				}
				if expected[id] != cluster {
					rows.Close()
					return domain.ErrNotFound
				}
				seen++
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if seen != len(ids) {
				return domain.ErrNotFound
			}
		}
		now := time.Now().UTC()
		update = software.FleetUpdate{ID: domain.NewID(), OrganizationID: org, RequestID: r.RequestID,
			TargetVersion: r.TargetVersion, ManifestSHA256: r.ManifestSHA256, Fingerprint: r.Fingerprint,
			Steps: append([]software.FleetStep{}, r.Steps...), Status: "queued", ActorUserID: domain.Actor(ctx), CreatedAt: now, UpdatedAt: now}
		payload, err := json.Marshal(update)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hm_fleet_updates(id,organization_id,request_id,status,next_step,created_at,updated_at,payload)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, update.ID, org, r.RequestID, update.Status, update.NextStep, now, now, payload); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO hm_fleet_update_events(id,update_id,organization_id,actor_user_id,status,next_step,reason,created_at)
			VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8)`, domain.NewID(), update.ID, org, update.ActorUserID, update.Status, 0, "", now)
		return err
	})
	return update, normalizeError(err)
}

func (p *Postgres) ListFleetUpdates(ctx context.Context, org string) ([]software.FleetUpdate, error) {
	if !domain.ValidID(org) {
		return nil, domain.ErrInvalidInput
	}
	rows, err := p.pool.Query(ctx, `SELECT payload FROM hm_fleet_updates WHERE organization_id=$1 ORDER BY created_at DESC,id DESC LIMIT 20`, org)
	if err != nil {
		return nil, normalizeError(err)
	}
	defer rows.Close()
	result := []software.FleetUpdate{}
	for rows.Next() {
		var raw []byte
		var update software.FleetUpdate
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &update) != nil {
			return nil, errors.New("invalid persisted fleet update")
		}
		result = append(result, update)
	}
	return result, normalizeError(rows.Err())
}

func (p *Postgres) ActiveFleetUpdates(ctx context.Context) ([]software.FleetUpdate, error) {
	rows, err := p.pool.Query(ctx, `SELECT payload FROM hm_fleet_updates WHERE status IN ('queued','running') ORDER BY created_at,id LIMIT 50`)
	if err != nil {
		return nil, normalizeError(err)
	}
	defer rows.Close()
	result := []software.FleetUpdate{}
	for rows.Next() {
		var raw []byte
		var update software.FleetUpdate
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &update) != nil {
			return nil, errors.New("invalid persisted fleet update")
		}
		result = append(result, update)
	}
	return result, normalizeError(rows.Err())
}

func (p *Postgres) AdvanceFleetUpdate(ctx context.Context, org, id string, before, next int, status, reason string) (software.FleetUpdate, error) {
	var update software.FleetUpdate
	if !domain.ValidID(org) || !domain.ValidID(id) {
		return update, domain.ErrInvalidInput
	}
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT payload FROM hm_fleet_updates WHERE organization_id=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&raw); err != nil {
			return err
		}
		if json.Unmarshal(raw, &update) != nil {
			return errors.New("invalid persisted fleet update")
		}
		if !validFleetTransition(update, before, next, status, reason) {
			return domain.ErrConflict
		}
		update = advanceFleet(update, next, status, reason)
		payload, err := json.Marshal(update)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE hm_fleet_updates SET status=$3,next_step=$4,updated_at=$5,completed_at=$6,payload=$7 WHERE organization_id=$1 AND id=$2`, org, id, status, next, update.UpdatedAt, update.CompletedAt, payload); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO hm_fleet_update_events(id,update_id,organization_id,actor_user_id,status,next_step,reason,created_at)
			VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8)`, domain.NewID(), id, org, update.ActorUserID, status, next, reason, update.UpdatedAt)
		return err
	})
	return update, normalizeError(err)
}
