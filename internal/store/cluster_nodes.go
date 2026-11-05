package store

import (
	"context"
	"sort"

	"github.com/ecraela/fleet/internal/domain"
)

const MaxLiveNodes = 12

type ClusterNodesStore interface {
	GetClusterNodes(context.Context, string, string, []string) ([]domain.Node, error)
}

func validNodeSelection(org, cluster string, ids []string) bool {
	if !domain.ValidID(org) || !domain.ValidID(cluster) || len(ids) == 0 || len(ids) > MaxLiveNodes {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !domain.ValidID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (m *Memory) GetClusterNodes(ctx context.Context, org, cluster string, ids []string) ([]domain.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validNodeSelection(org, cluster, ids) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	nodes := make([]domain.Node, 0, len(ids))
	for _, id := range ids {
		node, ok := m.nodes[id]
		if !ok || node.OrganizationID != org || node.ClusterID != cluster {
			return nil, domain.ErrNotFound
		}
		nodes = append(nodes, cloneNode(node))
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return nodes, nil
}

func (p *Postgres) GetClusterNodes(ctx context.Context, org, cluster string, ids []string) ([]domain.Node, error) {
	if !validNodeSelection(org, cluster, ids) {
		return nil, domain.ErrInvalidInput
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,organization_id::text,cluster_id::text,name,created_at,revoked_at FROM hm_nodes WHERE organization_id=$1 AND cluster_id=$2 AND id=ANY($3::uuid[]) ORDER BY id`, org, cluster, ids)
	if err != nil {
		return nil, normalizeError(err)
	}
	defer rows.Close()
	nodes := make([]domain.Node, 0, len(ids))
	for rows.Next() {
		var n domain.Node
		if err := rows.Scan(&n.ID, &n.OrganizationID, &n.ClusterID, &n.Name, &n.CreatedAt, &n.RevokedAt); err != nil {
			return nil, normalizeError(err)
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeError(err)
	}
	if len(nodes) != len(ids) {
		return nil, domain.ErrNotFound
	}
	return nodes, nil
}
