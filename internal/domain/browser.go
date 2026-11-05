package domain

import "time"

// BrowserSession contains only a digest of the browser's random credential.
// User/organization and capabilities come from a verified, one-use handoff.
type BrowserSession struct {
	Digest         string
	UserID         string
	OrganizationID string
	Capabilities   []string
	SessionKind    string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}
