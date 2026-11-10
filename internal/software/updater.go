package software

import "time"

// Updater is last reported by the separate host-owned reconciliation process.
// It is readiness evidence, not proof of a signed installation or host health.
type Updater struct {
	CurrentVersion  string    `json:"current_version"`
	IngestVersion   string    `json:"ingest_version,omitempty"`
	PreviousVersion string    `json:"previous_version,omitempty"`
	Ready           bool      `json:"ready"`
	LastSeen        time.Time `json:"last_seen"`
}
