package software

import "time"

type Update struct {
	ID              string     `json:"id"`
	OrganizationID  string     `json:"organization_id"`
	ClusterID       string     `json:"cluster_id"`
	NodeID          string     `json:"node_id"`
	RequestID       string     `json:"request_id"`
	Action          string     `json:"action"`
	TargetVersion   string     `json:"target_version"`
	TargetRevision  string     `json:"target_revision"`
	AgentSHA256     string     `json:"agent_sha256"`
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

func (u Update) Active() bool {
	return ActiveStatus(u.Status)
}

func (u Update) Terminal() bool {
	return TerminalStatus(u.Status)
}

func ValidTransition(from, to string) bool {
	switch from {
	case "queued":
		return to == "preparing" || to == "cancelled" || to == "needs_attention"
	case "preparing":
		return to == "activating" || to == "needs_attention" || to == "rolled_back"
	case "activating":
		return to == "verifying" || to == "needs_attention" || to == "rolled_back"
	case "verifying":
		return to == "succeeded" || to == "needs_attention" || to == "rolled_back"
	case "needs_attention":
		return to == "recovered"
	default:
		return false
	}
}

func ValidErrorCode(code string) bool {
	switch code {
	case "", "download_failed", "signature_invalid", "manifest_mismatch", "peer_incompatible", "resource_unavailable",
		"identity_invalid", "activation_failed", "health_failed", "rollback_failed", "recovery_failed", "status_unconfirmed":
		return true
	default:
		return false
	}
}
