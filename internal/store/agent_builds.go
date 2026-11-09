package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ecraela/fleet/internal/buildinfo"
	"github.com/ecraela/fleet/internal/domain"
)

type AgentObservation struct {
	Build          *buildinfo.Info
	HAProxyVersion string
	LastSeen       time.Time
	Connected      bool
}
type AgentBuildStore interface {
	AgentBuilds(context.Context, string, []string) (map[string]AgentObservation, error)
}

func validBuildSelection(org string, ids []string) bool {
	if !domain.ValidID(org) || len(ids) > FleetPageSize {
		return false
	}
	for _, id := range ids {
		if !domain.ValidID(id) {
			return false
		}
	}
	return true
}

func (m *Memory) AgentBuilds(ctx context.Context, org string, ids []string) (map[string]AgentObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validBuildSelection(org, ids) {
		return nil, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]AgentObservation{}
	for _, id := range ids {
		node, ok := m.nodes[id]
		if !ok || node.OrganizationID != org || node.RevokedAt != nil {
			continue
		}
		c := m.controls[id]
		if c.Discovery == nil || c.Discovery.Agent == nil {
			continue
		}
		v := *c.Discovery.Agent
		out[id] = AgentObservation{Build: &v, HAProxyVersion: c.Discovery.Version, LastSeen: c.LastSeen, Connected: c.SessionID != ""}
	}
	return out, nil
}

func (p *Postgres) AgentBuilds(ctx context.Context, org string, ids []string) (map[string]AgentObservation, error) {
	if !validBuildSelection(org, ids) {
		return nil, domain.ErrInvalidInput
	}
	rows, err := p.pool.Query(ctx, `SELECT c.node_id::text,c.discovery->'agent',COALESCE(c.discovery->>'version',''),c.last_seen,c.session_id IS NOT NULL FROM hm_node_controls c JOIN hm_nodes n ON n.id=c.node_id AND n.organization_id=c.organization_id WHERE c.organization_id=$1 AND c.node_id=ANY($2::uuid[]) AND n.revoked_at IS NULL AND c.discovery->'agent' IS NOT NULL AND c.last_seen<>'-infinity'`, org, ids)
	if err != nil {
		return nil, normalizeError(err)
	}
	defer rows.Close()
	out := map[string]AgentObservation{}
	for rows.Next() {
		var id string
		var raw []byte
		var v AgentObservation
		if err := rows.Scan(&id, &raw, &v.HAProxyVersion, &v.LastSeen, &v.Connected); err != nil {
			return nil, normalizeError(err)
		}
		if err := json.Unmarshal(raw, &v.Build); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, normalizeError(rows.Err())
}
