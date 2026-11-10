package software

import "time"

// InstallationUpdate changes manager and ingest together under the external
// host supervisor. HAProxy data-plane nodes are not part of this transaction.
type InstallationUpdate struct {
	ID              string     `json:"id"`
	OrganizationID  string     `json:"organization_id"`
	RequestID       string     `json:"request_id"`
	Action          string     `json:"action"`
	TargetVersion   string     `json:"target_version"`
	TargetRevision  string     `json:"target_revision"`
	ManagerSHA256   string     `json:"manager_sha256"`
	Platform        string     `json:"platform"`
	ManifestSHA256  string     `json:"manifest_sha256"`
	Publisher       string     `json:"publisher"`
	ExpectedVersion string     `json:"expected_version"`
	Status          string     `json:"status"`
	ErrorCode       string     `json:"error_code,omitempty"`
	ActorUserID     string     `json:"actor_user_id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

func (u InstallationUpdate) Active() bool   { return ActiveStatus(u.Status) }
func (u InstallationUpdate) Terminal() bool { return TerminalStatus(u.Status) }

func ActiveStatus(status string) bool {
	switch status {
	case "queued", "preparing", "activating", "verifying", "needs_attention":
		return true
	default:
		return false
	}
}

func TerminalStatus(status string) bool {
	switch status {
	case "succeeded", "rolled_back", "cancelled", "recovered":
		return true
	default:
		return false
	}
}
