package store

import (
	"context"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type browserMemoryState struct {
	sessions map[string]domain.BrowserSession
	grants   map[string]time.Time
}

var _ BrowserStore = (*Memory)(nil)

func (m *Memory) browserState() *browserMemoryState {
	if m.browser == nil {
		m.browser = &browserMemoryState{sessions: make(map[string]domain.BrowserSession), grants: make(map[string]time.Time)}
	}
	return m.browser
}

func (m *Memory) ConsumeBrowserGrant(ctx context.Context, jti string, grantExpires time.Time, session domain.BrowserSession, replace string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !domain.ValidID(jti) || !validBrowserSession(session) || (replace != "" && !domain.ValidDigest(replace)) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now = freshTime(now)
	state := m.browserState()
	for key, s := range state.sessions {
		if !now.Before(s.ExpiresAt) {
			delete(state.sessions, key)
		}
	}
	for key, expiry := range state.grants {
		if !now.Before(expiry) {
			delete(state.grants, key)
		}
	}
	if !now.Before(grantExpires) || !now.Before(session.ExpiresAt) {
		return domain.ErrNotFound
	}
	if _, ok := state.grants[jti]; ok {
		return domain.ErrConflict
	}
	if _, ok := state.sessions[session.Digest]; ok {
		return domain.ErrConflict
	}
	if len(state.sessions) >= 10000 || len(state.grants) >= 10000 {
		return domain.ErrConflict
	}
	state.grants[jti] = grantExpires
	state.sessions[session.Digest] = cloneBrowserSession(session)
	if replace != "" && replace != session.Digest {
		delete(state.sessions, replace)
	}
	return nil
}

func (m *Memory) GetBrowserSession(ctx context.Context, hash string, now time.Time) (domain.BrowserSession, error) {
	if err := ctx.Err(); err != nil {
		return domain.BrowserSession{}, err
	}
	if !domain.ValidDigest(hash) {
		return domain.BrowserSession{}, domain.ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.browserState()
	s, ok := state.sessions[hash]
	if !ok || !freshTime(now).Before(s.ExpiresAt) {
		delete(state.sessions, hash)
		return domain.BrowserSession{}, domain.ErrNotFound
	}
	return cloneBrowserSession(s), nil
}

func (m *Memory) DeleteBrowserSession(ctx context.Context, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !domain.ValidDigest(hash) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.browserState().sessions, hash)
	return nil
}
