package software

import "time"

// FleetUpdate is one installation-wide desired release. Each step has a stable
// idempotency key so a restarted manager can safely resume dispatch.
type FleetUpdate struct {
	ID             string      `json:"id"`
	OrganizationID string      `json:"organization_id"`
	RequestID      string      `json:"request_id"`
	TargetVersion  string      `json:"target_version"`
	ManifestSHA256 string      `json:"manifest_sha256"`
	Fingerprint    string      `json:"fingerprint"`
	Steps          []FleetStep `json:"steps"`
	NextStep       int         `json:"next_step"`
	Status         string      `json:"status"`
	Reason         string      `json:"reason,omitempty"`
	ActorUserID    string      `json:"actor_user_id"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	CompletedAt    *time.Time  `json:"completed_at,omitempty"`
}

type FleetStep struct {
	Role            string `json:"role"`
	NodeID          string `json:"node_id,omitempty"`
	ClusterID       string `json:"cluster_id,omitempty"`
	Name            string `json:"name"`
	ExpectedVersion string `json:"expected_version"`
	RequestID       string `json:"request_id,omitempty"`
}

func (u FleetUpdate) Active() bool { return u.Status == "queued" || u.Status == "running" }
