package domain

import "time"

// ConnectorLink binds one cloud organization to one installation. Credentials
// are digests; the manager keeps its own random credential on its private disk.
type ConnectorLink struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	Name           string     `json:"name"`
	CreatedAt      time.Time  `json:"created_at"`
	PairExpiresAt  time.Time  `json:"pair_expires_at"`
	ClaimedAt      *time.Time `json:"claimed_at"`
	RevokedAt      *time.Time `json:"revoked_at"`
	PairDigest     string     `json:"-"`
	TokenDigest    string     `json:"-"`
}
