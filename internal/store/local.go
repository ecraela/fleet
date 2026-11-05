package store

import (
	"context"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/localauth"
	"time"
)

// LocalStore sessions are independent of optional cloud handoffs. All identities
// are bound to one installation tenant at account creation, never by a login body.
type LocalStore interface {
	CreateLocalUser(context.Context, domain.LocalUser) error
	GetLocalUser(context.Context, string) (domain.LocalUser, error)
	ChangeLocalPassword(context.Context, string, string, string) error
	DisableLocalUser(context.Context, string) error
	AdmitLocalLogin(context.Context, string, string, time.Time) (bool, error)
	CreateLocalSession(context.Context, domain.LocalSession, string) error
	GetLocalSession(context.Context, string, time.Time) (domain.LocalUser, domain.LocalSession, error)
	DeleteLocalSession(context.Context, string) error
}

func validLocalUser(u domain.LocalUser) bool {
	name, err := localauth.Username(u.Username)
	return err == nil && name == u.Username && domain.ValidID(u.ID) && domain.ValidID(u.OrganizationID) && localauth.ValidHash(u.PasswordHash) && (u.Role == "administrator" || u.Role == "viewer") && !u.Disabled
}
func validLocalSession(s domain.LocalSession) bool {
	return domain.ValidDigest(s.Digest) && domain.ValidID(s.UserID) && s.ExpiresAt.After(s.CreatedAt) && s.ExpiresAt.Sub(s.CreatedAt) <= 12*time.Hour && s.LastSeenAt.Equal(s.CreatedAt)
}
