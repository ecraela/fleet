package store

import (
	"context"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/localauth"
	"time"
)

type loginBucket struct {
	at    time.Time
	count int
}
type localMemoryState struct {
	users    map[string]domain.LocalUser
	sessions map[string]domain.LocalSession
	limits   map[string]loginBucket
}

func (m *Memory) localState() *localMemoryState {
	if m.local == nil {
		m.local = &localMemoryState{users: map[string]domain.LocalUser{}, sessions: map[string]domain.LocalSession{}, limits: map[string]loginBucket{}}
	}
	return m.local
}

var _ LocalStore = (*Memory)(nil)

func (m *Memory) CreateLocalUser(ctx context.Context, u domain.LocalUser) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validLocalUser(u) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.localState()
	for _, old := range s.users {
		if old.Username == u.Username || old.ID == u.ID {
			return domain.ErrConflict
		}
	}
	s.users[u.ID] = u
	return nil
}
func (m *Memory) GetLocalUser(ctx context.Context, name string) (domain.LocalUser, error) {
	if err := ctx.Err(); err != nil {
		return domain.LocalUser{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.localState().users {
		if u.Username == name {
			return u, nil
		}
	}
	return domain.LocalUser{}, domain.ErrNotFound
}
func (m *Memory) ChangeLocalPassword(ctx context.Context, id, previous, next string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !domain.ValidID(id) || !localauth.ValidHash(next) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.localState()
	u, ok := s.users[id]
	if !ok || u.Disabled || u.PasswordHash != previous {
		return domain.ErrNotFound
	}
	u.PasswordHash = next
	s.users[id] = u
	for k, v := range s.sessions {
		if v.UserID == id {
			delete(s.sessions, k)
		}
	}
	return nil
}
func (m *Memory) DisableLocalUser(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.localState()
	u, ok := s.users[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.Disabled = true
	s.users[id] = u
	for k, v := range s.sessions {
		if v.UserID == id {
			delete(s.sessions, k)
		}
	}
	return nil
}
func (m *Memory) AdmitLocalLogin(ctx context.Context, source, identity string, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !domain.ValidDigest(source) || !domain.ValidDigest(identity) {
		return false, domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.localState()
	now = freshTime(now).Truncate(time.Minute)
	for k, b := range s.limits {
		if b.at.Before(now.Add(-time.Minute)) {
			delete(s.limits, k)
		}
	}
	if len(s.limits) > 10000 {
		return false, nil
	}
	allowed := true
	for _, b := range []struct {
		key string
		max int
	}{{"source:" + source, 20}, {"identity:" + identity, 5}, {"global", 120}} {
		v := s.limits[b.key]
		if !v.at.Equal(now) {
			v = loginBucket{at: now}
		}
		v.count++
		s.limits[b.key] = v
		if v.count > b.max {
			allowed = false
			break
		}
	}
	return allowed, nil
}
func (m *Memory) CreateLocalSession(ctx context.Context, v domain.LocalSession, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validLocalSession(v) {
		return domain.ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.localState()
	u, ok := s.users[v.UserID]
	if !ok || u.Disabled || u.PasswordHash != hash {
		return domain.ErrNotFound
	}
	if len(s.sessions) >= 10000 {
		return domain.ErrConflict
	}
	s.sessions[v.Digest] = v
	return nil
}
func (m *Memory) GetLocalSession(ctx context.Context, hash string, now time.Time) (domain.LocalUser, domain.LocalSession, error) {
	if err := ctx.Err(); err != nil {
		return domain.LocalUser{}, domain.LocalSession{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.localState()
	v, ok := s.sessions[hash]
	u := s.users[v.UserID]
	now = freshTime(now)
	if !ok || u.Disabled || !now.Before(v.ExpiresAt) || !now.Before(v.LastSeenAt.Add(30*time.Minute)) {
		return domain.LocalUser{}, domain.LocalSession{}, domain.ErrNotFound
	}
	v.LastSeenAt = now
	s.sessions[hash] = v
	return u, v, nil
}
func (m *Memory) DeleteLocalSession(ctx context.Context, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.localState().sessions, hash)
	return nil
}
