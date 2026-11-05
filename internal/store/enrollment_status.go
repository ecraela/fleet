package store

import (
	"context"
	"github.com/ecraela/fleet/internal/domain"
	"time"
)

type EnrollmentStatus struct {
	State string       `json:"state"`
	Node  *domain.Node `json:"node,omitempty"`
}

type EnrollmentStatusStore interface {
	EnrollmentStatus(context.Context, string, string, time.Time) (EnrollmentStatus, error)
}

func (m *Memory) EnrollmentStatus(ctx context.Context, org, hash string, now time.Time) (EnrollmentStatus, error) {
	if err := ctx.Err(); err != nil {
		return EnrollmentStatus{}, err
	}
	if !domain.ValidID(org) || !domain.ValidDigest(hash) {
		return EnrollmentStatus{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	token, ok := m.tokens[hash]
	if !ok || token.OrganizationID != org {
		return EnrollmentStatus{}, domain.ErrNotFound
	}
	if token.revoked {
		return EnrollmentStatus{State: "revoked"}, nil
	}
	if token.consumed {
		node := cloneNode(m.nodes[token.nodeID])
		return EnrollmentStatus{State: "connected", Node: &node}, nil
	}
	if !freshTime(now).Before(token.ExpiresAt) {
		return EnrollmentStatus{State: "expired"}, nil
	}
	return EnrollmentStatus{State: "pending"}, nil
}

func (p *Postgres) EnrollmentStatus(ctx context.Context, org, hash string, now time.Time) (EnrollmentStatus, error) {
	if !domain.ValidID(org) || !domain.ValidDigest(hash) {
		return EnrollmentStatus{}, domain.ErrInvalidInput
	}
	var state string
	var nodeID *string
	err := p.pool.QueryRow(ctx, `SELECT CASE WHEN revoked_at IS NOT NULL THEN 'revoked' WHEN consumed_node_id IS NOT NULL THEN 'connected' WHEN expires_at<=GREATEST($3::timestamptz,clock_timestamp()) THEN 'expired' ELSE 'pending' END,consumed_node_id::text FROM hm_enrollment_tokens WHERE organization_id=$1 AND digest=$2`, org, hash, now).Scan(&state, &nodeID)
	if err != nil {
		return EnrollmentStatus{}, normalizeError(err)
	}
	result := EnrollmentStatus{State: state}
	if nodeID != nil && state == "connected" {
		node, err := p.GetNode(ctx, org, *nodeID)
		if err != nil {
			return EnrollmentStatus{}, err
		}
		result.Node = &node
	}
	return result, nil
}
