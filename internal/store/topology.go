package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed topology_schema.sql
var topologySchemaSQL string

type TopologyStore interface {
	SetTopology(context.Context, string, string, domain.Topology) error
}

func validTopology(t domain.Topology) bool {
	entry, err := url.Parse(t.Entrypoint)
	if err != nil || (entry.Scheme != "http" && entry.Scheme != "https") || entry.Hostname() == "" || entry.User != nil || entry.RawQuery != "" || entry.Fragment != "" || len(t.Entrypoint) > 512 {
		return false
	}
	if t.Strategy != "roundrobin" && t.Strategy != "leastconn" && t.Strategy != "active-passive" {
		return false
	}
	if !validName(t.Frontend) || !validName(t.Backend) || len(t.Nodes) < 1 || len(t.Nodes) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, n := range t.Nodes {
		if !domain.ValidID(n.NodeID) || seen[n.NodeID] || !validName(n.Address) || strings.ContainsFunc(n.Address, unicode.IsControl) {
			return false
		}
		seen[n.NodeID] = true
	}
	if t.Control != nil {
		if !domain.ValidID(t.Control.NodeID) || !domain.ValidRuntimeName(t.Control.Backend) || len(t.Nodes) < 2 || len(t.Nodes) > 12 || len(t.Control.Servers) != len(t.Nodes) || seen[t.Control.NodeID] {
			return false
		}
		names := map[string]bool{}
		bound := map[string]bool{}
		for _, s := range t.Control.Servers {
			if !seen[s.NodeID] || bound[s.NodeID] || !domain.ValidRuntimeName(s.Server) || names[s.Server] {
				return false
			}
			bound[s.NodeID] = true
			names[s.Server] = true
		}
	}
	return true
}

func cloneCluster(c domain.Cluster) domain.Cluster {
	if c.Topology != nil {
		t := *c.Topology
		t.Nodes = append([]domain.TopologyNode(nil), t.Nodes...)
		if t.Control != nil {
			control := *t.Control
			control.Servers = append([]domain.EntrypointServer(nil), control.Servers...)
			t.Control = &control
		}
		c.Topology = &t
	}
	return c
}

func (m *Memory) SetTopology(ctx context.Context, org, clusterID string, topology domain.Topology) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !domain.ValidID(org) || !domain.ValidID(clusterID) || !validTopology(topology) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cluster, ok := m.clusters[clusterID]
	if !ok || cluster.OrganizationID != org {
		return domain.ErrNotFound
	}
	if m.maintenanceClusterBusy(org, clusterID) || m.activeRollout(org, clusterID) != "" {
		return domain.ErrConflict
	}
	for _, revision := range m.applicationRevisions {
		if revision.OrganizationID == org && revision.ClusterID == clusterID && revision.Spec.Listener != topology.Frontend {
			return domain.ErrConflict
		}
	}
	if topology.Control != nil {
		c, ok := m.nodes[topology.Control.NodeID]
		if !ok || c.OrganizationID != org || c.ClusterID == clusterID {
			return domain.ErrNotFound
		}
		if c.RevokedAt != nil {
			return domain.ErrRevoked
		}
	}
	for _, n := range topology.Nodes {
		node, ok := m.nodes[n.NodeID]
		if !ok || node.OrganizationID != org || node.ClusterID != clusterID {
			return domain.ErrNotFound
		}
		if node.RevokedAt != nil {
			return domain.ErrRevoked
		}
	}
	cluster.Topology = &topology
	m.clusters[clusterID] = cloneCluster(cluster)
	m.audit = append(m.audit, auditEvent{actorUserID: domain.Actor(ctx), organizationID: org, clusterID: clusterID, action: "topology.updated", createdAt: time.Now().UTC()})
	return nil
}

func (p *Postgres) SetTopology(ctx context.Context, org, clusterID string, topology domain.Topology) error {
	if !domain.ValidID(org) || !domain.ValidID(clusterID) || !validTopology(topology) {
		return domain.ErrInvalidInput
	}
	raw, err := json.Marshal(topology)
	if err != nil {
		return err
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM hm_clusters WHERE organization_id=$1 AND id=$2 FOR UPDATE`, org, clusterID).Scan(&id); err != nil {
			return err
		}
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_maintenance WHERE organization_id=$1 AND (cluster_id=$2 OR controller_cluster_id=$2) AND status NOT IN ('completed','failed'))`, org, clusterID).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return domain.ErrConflict
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_rollouts WHERE organization_id=$1 AND (cluster_id=$2 OR controller_cluster_id=$2) AND status IN ('running','rolling_back'))`, org, clusterID).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return domain.ErrConflict
		}
		var incompatible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hm_application_revisions WHERE organization_id=$1 AND cluster_id=$2 AND payload->'spec'->>'listener'<>$3)`, org, clusterID, topology.Frontend).Scan(&incompatible); err != nil {
			return err
		}
		if incompatible {
			return domain.ErrConflict
		}
		if topology.Control != nil {
			var controllerCluster string
			var revoked *time.Time
			if err := tx.QueryRow(ctx, `SELECT cluster_id::text,revoked_at FROM hm_nodes WHERE organization_id=$1 AND id=$2`, org, topology.Control.NodeID).Scan(&controllerCluster, &revoked); err != nil {
				return err
			}
			if controllerCluster == clusterID {
				return domain.ErrNotFound
			}
			if revoked != nil {
				return domain.ErrRevoked
			}
		}
		for _, n := range topology.Nodes {
			var revoked *time.Time
			if err := tx.QueryRow(ctx, `SELECT revoked_at FROM hm_nodes WHERE organization_id=$1 AND cluster_id=$2 AND id=$3 FOR UPDATE`, org, clusterID, n.NodeID).Scan(&revoked); err != nil {
				return err
			}
			if revoked != nil {
				return domain.ErrRevoked
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE hm_clusters SET topology=$3 WHERE organization_id=$1 AND id=$2`, org, clusterID, raw); err != nil {
			return err
		}
		return appendAudit(ctx, tx, org, clusterID, "", "topology.updated", time.Now().UTC())
	})
}
