package store

import (
	"context"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

// BrowserStore keeps cloud handoffs optional for independent installations.
// Consuming a grant and publishing its session must be one atomic operation.
type BrowserStore interface {
	ConsumeBrowserGrant(context.Context, string, time.Time, domain.BrowserSession, string, time.Time) error
	GetBrowserSession(context.Context, string, time.Time) (domain.BrowserSession, error)
	DeleteBrowserSession(context.Context, string) error
}

func validBrowserSession(s domain.BrowserSession) bool {
	if !domain.ValidDigest(s.Digest) || !domain.ValidID(s.UserID) || !domain.ValidID(s.OrganizationID) || !s.ExpiresAt.After(s.CreatedAt) || s.ExpiresAt.Sub(s.CreatedAt) > time.Minute || len(s.Capabilities) == 0 || len(s.Capabilities) > 2 {
		return false
	}
	if s.SessionKind != "global" && s.SessionKind != "organization" {
		return false
	}
	read, manage := false, false
	for _, cap := range s.Capabilities {
		switch cap {
		case "haproxy.read":
			if read {
				return false
			}
			read = true
		case "haproxy.manage":
			if manage {
				return false
			}
			manage = true
		default:
			return false
		}
	}
	return read
}

func cloneBrowserSession(s domain.BrowserSession) domain.BrowserSession {
	s.Capabilities = append([]string(nil), s.Capabilities...)
	return s
}
