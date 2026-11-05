package domain

import "time"

type LocalUser struct {
	ID             string
	Username       string
	OrganizationID string
	PasswordHash   string
	Role           string
	Disabled       bool
}

func (u LocalUser) Capabilities() []string {
	if u.Disabled {
		return nil
	}
	if u.Role == "administrator" {
		return []string{"haproxy.read", "haproxy.manage"}
	}
	if u.Role == "viewer" {
		return []string{"haproxy.read"}
	}
	return nil
}

type LocalSession struct {
	Digest     string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
}
