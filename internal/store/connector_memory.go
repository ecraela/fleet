package store

import (
	"context"
	"github.com/ecraela/fleet/internal/domain"
	"time"
)

func cloneConnector(l domain.ConnectorLink) domain.ConnectorLink {
	if l.ClaimedAt != nil {
		t := *l.ClaimedAt
		l.ClaimedAt = &t
	}
	if l.RevokedAt != nil {
		t := *l.RevokedAt
		l.RevokedAt = &t
	}
	return l
}
func (m *Memory) CreateConnector(ctx context.Context, l domain.ConnectorLink) error {
	if !validConnector(l) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connectors == nil {
		m.connectors = make(map[string]domain.ConnectorLink)
	}
	count := 0
	now := time.Now()
	for id, old := range m.connectors {
		if old.OrganizationID != l.OrganizationID {
			continue
		}
		if old.CreatedAt.After(now.Add(-time.Hour)) {
			count++
		}
		if old.RevokedAt == nil {
			if old.ClaimedAt != nil || old.PairExpiresAt.After(now) {
				return domain.ErrConflict
			}
			old.RevokedAt = &now
			m.connectors[id] = old
		}
	}
	if count >= 10 {
		return domain.ErrConflict
	}
	if _, ok := m.connectors[l.ID]; ok {
		return domain.ErrConflict
	}
	m.connectors[l.ID] = cloneConnector(l)
	return nil
}
func (m *Memory) ConnectorForOrganization(ctx context.Context, org string) (domain.ConnectorLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.connectors {
		if l.OrganizationID == org && l.RevokedAt == nil {
			return cloneConnector(l), nil
		}
	}
	return domain.ConnectorLink{}, domain.ErrNotFound
}
func (m *Memory) ClaimConnector(ctx context.Context, id, pair, token string, now time.Time) (domain.ConnectorLink, error) {
	if !domain.ValidID(id) || !domain.ValidDigest(pair) || !domain.ValidDigest(token) {
		return domain.ConnectorLink{}, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.connectors[id]
	if !ok || l.RevokedAt != nil || !l.PairExpiresAt.After(now) || l.PairDigest != pair || (l.TokenDigest != "" && l.TokenDigest != token) {
		return domain.ConnectorLink{}, domain.ErrNotFound
	}
	if l.ClaimedAt == nil {
		l.ClaimedAt = &now
	}
	l.TokenDigest = token
	m.connectors[id] = l
	return cloneConnector(l), nil
}
func (m *Memory) AuthenticateConnector(ctx context.Context, id, token string) (domain.ConnectorLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.connectors[id]
	if !ok || l.RevokedAt != nil || l.ClaimedAt == nil || l.TokenDigest != token {
		return domain.ConnectorLink{}, domain.ErrNotFound
	}
	return cloneConnector(l), nil
}
func (m *Memory) RevokeConnector(ctx context.Context, org, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.connectors[id]
	if !ok || l.OrganizationID != org || l.RevokedAt != nil {
		return domain.ErrNotFound
	}
	now := time.Now()
	l.RevokedAt = &now
	m.connectors[id] = l
	return nil
}
func (m *Memory) RecordConnectorAudit(ctx context.Context, org, id, action string) error {
	if !domain.ValidID(org) || !domain.ValidID(id) || (action != "connector.local_linked" && action != "connector.local_unlinked") {
		return domain.ErrInvalidInput
	}
	return nil
}
