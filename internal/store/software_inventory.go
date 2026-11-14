package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
)

type SoftwareInventoryEntry struct {
	Updater *software.Updater `json:"updater"`
	Latest  *software.Update  `json:"latest"`
}

type SoftwareInventoryStore interface {
	SoftwareInventory(context.Context, string, []string) (map[string]SoftwareInventoryEntry, error)
}

func (m *Memory) SoftwareInventory(ctx context.Context, org string, ids []string) (map[string]SoftwareInventoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validBuildSelection(org, ids) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]SoftwareInventoryEntry, len(ids))
	for _, id := range ids {
		node, found := m.nodes[id]
		if !found || node.OrganizationID != org || node.RevokedAt != nil {
			continue
		}
		entry := SoftwareInventoryEntry{}
		if updater, found := m.softwareUpdaters[id]; found {
			entry.Updater = &updater
		}
		for _, update := range m.softwareUpdates {
			if update.OrganizationID == org && update.NodeID == id &&
				(entry.Latest == nil || update.CreatedAt.After(entry.Latest.CreatedAt) ||
					update.CreatedAt.Equal(entry.Latest.CreatedAt) && update.ID > entry.Latest.ID) {
				copy := update
				entry.Latest = &copy
			}
		}
		result[id] = entry
	}
	return result, nil
}

func (p *Postgres) SoftwareInventory(ctx context.Context, org string, ids []string) (map[string]SoftwareInventoryEntry, error) {
	if !validBuildSelection(org, ids) {
		return nil, domain.ErrInvalidInput
	}
	rows, err := p.pool.Query(ctx, `SELECT n.id::text, u.current_version, u.previous_version, u.ready, u.last_seen, latest.payload
		FROM hm_nodes n
		LEFT JOIN hm_software_updaters u ON u.node_id=n.id AND u.organization_id=n.organization_id
		LEFT JOIN LATERAL (
			SELECT payload FROM hm_software_updates
			WHERE organization_id=n.organization_id AND node_id=n.id
			ORDER BY created_at DESC,id DESC LIMIT 1
		) latest ON true
		WHERE n.organization_id=$1 AND n.id=ANY($2::uuid[]) AND n.revoked_at IS NULL`, org, ids)
	if err != nil {
		return nil, normalizeError(err)
	}
	defer rows.Close()
	result := make(map[string]SoftwareInventoryEntry, len(ids))
	for rows.Next() {
		var id string
		var current, previous *string
		var ready *bool
		var seen *time.Time
		var raw []byte
		if err := rows.Scan(&id, &current, &previous, &ready, &seen, &raw); err != nil {
			return nil, normalizeError(err)
		}
		entry := SoftwareInventoryEntry{}
		if current != nil && previous != nil && ready != nil && seen != nil {
			entry.Updater = &software.Updater{CurrentVersion: *current, PreviousVersion: *previous, Ready: *ready, LastSeen: *seen}
		}
		if raw != nil {
			var update software.Update
			if err := json.Unmarshal(raw, &update); err != nil {
				return nil, errors.New("invalid persisted software update")
			}
			entry.Latest = &update
		}
		result[id] = entry
	}
	return result, normalizeError(rows.Err())
}
