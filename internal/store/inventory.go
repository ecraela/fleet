package store

import (
	"context"
	"sort"

	"github.com/ecraela/fleet/internal/domain"
)

const FleetPageSize = 100

type InventoryStore interface {
	ListClusters(context.Context, string) ([]domain.Cluster, error)
	ListNodesPage(context.Context, string, string, int) ([]domain.Node, string, error)
}

func (m *Memory) ListClusters(ctx context.Context, org string) ([]domain.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !domain.ValidID(org) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]domain.Cluster, 0)
	for _, cluster := range m.clusters {
		if cluster.OrganizationID == org {
			result = append(result, cloneCluster(cluster))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name || (result[i].Name == result[j].Name && result[i].ID < result[j].ID)
	})
	return result, nil
}

func (m *Memory) ListNodesPage(ctx context.Context, org, cursor string, limit int) ([]domain.Node, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if !domain.ValidID(org) || (cursor != "" && !domain.ValidID(cursor)) || limit < 1 || limit > FleetPageSize {
		return nil, "", domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	nodes := make([]domain.Node, 0)
	for _, node := range m.nodes {
		if node.OrganizationID == org && node.ID > cursor {
			nodes = append(nodes, cloneNode(node))
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	next := ""
	if len(nodes) > limit {
		nodes = nodes[:limit]
		next = nodes[len(nodes)-1].ID
	}
	return nodes, next, nil
}

func (p *Postgres) ListClusters(ctx context.Context, org string) ([]domain.Cluster, error) {
	if !domain.ValidID(org) {
		return nil, domain.ErrInvalidInput
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,organization_id::text,name,created_at,topology FROM hm_clusters WHERE organization_id=$1 ORDER BY name,id`, org)
	if err != nil {
		return nil, normalizeError(err)
	}
	defer rows.Close()
	result := make([]domain.Cluster, 0)
	for rows.Next() {
		var cluster domain.Cluster
		if err := rows.Scan(&cluster.ID, &cluster.OrganizationID, &cluster.Name, &cluster.CreatedAt, &cluster.Topology); err != nil {
			return nil, normalizeError(err)
		}
		result = append(result, cloneCluster(cluster))
	}
	return result, normalizeError(rows.Err())
}

func (p *Postgres) ListNodesPage(ctx context.Context, org, cursor string, limit int) ([]domain.Node, string, error) {
	if !domain.ValidID(org) || (cursor != "" && !domain.ValidID(cursor)) || limit < 1 || limit > FleetPageSize {
		return nil, "", domain.ErrInvalidInput
	}
	var after any
	if cursor != "" {
		after = cursor
	}
	rows, err := p.pool.Query(ctx, `SELECT id::text,organization_id::text,cluster_id::text,name,created_at,revoked_at FROM hm_nodes WHERE organization_id=$1 AND ($2::uuid IS NULL OR id>$2::uuid) ORDER BY id LIMIT $3`, org, after, limit+1)
	if err != nil {
		return nil, "", normalizeError(err)
	}
	defer rows.Close()
	nodes := make([]domain.Node, 0)
	for rows.Next() {
		var node domain.Node
		if err := rows.Scan(&node.ID, &node.OrganizationID, &node.ClusterID, &node.Name, &node.CreatedAt, &node.RevokedAt); err != nil {
			return nil, "", normalizeError(err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizeError(err)
	}
	next := ""
	if len(nodes) > limit {
		nodes = nodes[:limit]
		next = nodes[len(nodes)-1].ID
	}
	return nodes, next, nil
}
