package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed live_schema.sql
var liveSchemaSQL string

type LiveStore interface {
	RecordLive(context.Context, domain.Node, domain.LiveObservation) error
	GetLive(context.Context, string, string, []string) (map[string]domain.LivePair, error)
}

func (m *Memory) RecordLive(ctx context.Context, node domain.Node, observation domain.LiveObservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if validateNode(node) != nil || !observation.Valid() {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	owned, ok := m.nodes[node.ID]
	if !ok || owned.OrganizationID != node.OrganizationID || owned.ClusterID != node.ClusterID || owned.RevokedAt != nil {
		return domain.ErrNotFound
	}
	if m.live == nil {
		m.live = map[string][]byte{}
	}
	var pair domain.LivePair
	if raw := m.live[node.ID]; raw != nil {
		if err := json.Unmarshal(raw, &pair); err != nil {
			return err
		}
		if pair.Current.AtMS >= observation.AtMS {
			return nil
		}
		previous := pair.Current
		pair.Previous = &previous
	}
	pair.Current = observation
	raw, err := json.Marshal(pair)
	if err == nil {
		m.live[node.ID] = raw
	}
	return err
}

func (m *Memory) GetLive(ctx context.Context, org, cluster string, ids []string) (map[string]domain.LivePair, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validNodeSelection(org, cluster, ids) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := map[string]domain.LivePair{}
	for _, id := range ids {
		node, ok := m.nodes[id]
		if !ok || node.OrganizationID != org || node.ClusterID != cluster {
			return nil, domain.ErrNotFound
		}
		if node.RevokedAt != nil || m.live[id] == nil {
			continue
		}
		var pair domain.LivePair
		if err := json.Unmarshal(m.live[id], &pair); err != nil {
			return nil, err
		}
		result[id] = pair
	}
	return result, nil
}

func (p *Postgres) RecordLive(ctx context.Context, node domain.Node, observation domain.LiveObservation) error {
	if validateNode(node) != nil || !observation.Valid() {
		return domain.ErrInvalidInput
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	var id string
	err = p.pool.QueryRow(ctx, `INSERT INTO hm_node_live(node_id,organization_id,cluster_id,observed_at_ms,current_sample)
 SELECT id,organization_id,cluster_id,$4,$5 FROM hm_nodes WHERE id=$1 AND organization_id=$2 AND cluster_id=$3 AND revoked_at IS NULL
 ON CONFLICT(node_id) DO UPDATE SET previous_sample=hm_node_live.current_sample,current_sample=EXCLUDED.current_sample,observed_at_ms=EXCLUDED.observed_at_ms
 WHERE hm_node_live.observed_at_ms<EXCLUDED.observed_at_ms RETURNING node_id::text`, node.ID, node.OrganizationID, node.ClusterID, observation.AtMS, raw).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		owned, lookup := p.GetNode(ctx, node.OrganizationID, node.ID)
		if lookup != nil {
			return lookup
		}
		if owned.ClusterID != node.ClusterID || owned.RevokedAt != nil {
			return domain.ErrNotFound
		}
		return nil
	}
	return normalizeError(err)
}

func (p *Postgres) GetLive(ctx context.Context, org, cluster string, ids []string) (map[string]domain.LivePair, error) {
	if !validNodeSelection(org, cluster, ids) {
		return nil, domain.ErrInvalidInput
	}
	if _, err := p.GetClusterNodes(ctx, org, cluster, ids); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT l.node_id::text,l.current_sample,l.previous_sample FROM hm_node_live l JOIN hm_nodes n ON n.id=l.node_id AND n.organization_id=l.organization_id AND n.cluster_id=l.cluster_id WHERE l.organization_id=$1 AND l.cluster_id=$2 AND l.node_id=ANY($3::uuid[]) AND n.revoked_at IS NULL`, org, cluster, ids)
	if err != nil {
		return nil, normalizeError(err)
	}
	defer rows.Close()
	result := map[string]domain.LivePair{}
	for rows.Next() {
		var id string
		var current, previous []byte
		var pair domain.LivePair
		if err := rows.Scan(&id, &current, &previous); err != nil {
			return nil, normalizeError(err)
		}
		if err := json.Unmarshal(current, &pair.Current); err != nil {
			return nil, err
		}
		if len(previous) > 0 {
			if err := json.Unmarshal(previous, &pair.Previous); err != nil {
				return nil, err
			}
		}
		result[id] = pair
	}
	return result, normalizeError(rows.Err())
}
