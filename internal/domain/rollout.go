package domain

import (
	"fmt"
	"strings"
	"time"
)

const MaxRolloutNodes = 12
const RolloutHealthWindow = 5 * time.Second

type RolloutTarget struct {
	NodeID             string `json:"node_id"`
	ExpectedGeneration string `json:"expected_generation"`
	ExpectedArtifactID string `json:"expected_artifact_id"`
	ExpectedVersion    int64  `json:"expected_version"`
}
type RolloutInput struct {
	RequestID           string                   `json:"request_id"`
	Document            ConfigurationDocument    `json:"document"`
	Nodes               []RolloutTarget          `json:"nodes"`
	Entrypoint          *RolloutEntrypointTarget `json:"entrypoint,omitempty"`
	DrainTimeoutSeconds int                      `json:"drain_timeout_seconds,omitempty"`
}
type RolloutEntrypointTarget struct {
	RolloutTarget
	BindingID string `json:"binding_id"`
}
type RolloutEntrypoint struct {
	RolloutEntrypointTarget
	ClusterID       string                `json:"cluster_id"`
	Topology        Topology              `json:"topology"`
	Baseline        ConfigurationDocument `json:"baseline"`
	OwnedArtifactID string                `json:"owned_artifact_id"`
	OwnedVersion    int64                 `json:"owned_version"`
}
type RolloutNode struct {
	ApplicationChecks               []RolloutApplicationCheck       `json:"application_checks,omitempty"`
	RollbackApplicationChecks       []RolloutApplicationCheck       `json:"rollback_application_checks,omitempty"`
	ApplicationVerification         *RolloutApplicationVerification `json:"application_verification,omitempty"`
	RollbackApplicationVerification *RolloutApplicationVerification `json:"rollback_application_verification,omitempty"`
	RolloutTarget
	Name                  string       `json:"name"`
	BaselineRevisionID    string       `json:"baseline_revision_id"`
	ApplyOperationID      string       `json:"apply_operation_id,omitempty"`
	RollbackOperationID   string       `json:"rollback_operation_id,omitempty"`
	AppliedArtifactID     string       `json:"applied_artifact_id,omitempty"`
	Status                string       `json:"status"`
	ErrorCode             string       `json:"error_code"`
	Deadline              time.Time    `json:"deadline"`
	HealthSinceMS         int64        `json:"health_since_ms"`
	LastHealthMS          int64        `json:"last_health_ms"`
	LastObservationMS     int64        `json:"last_observation_ms"`
	Maintenance           *Maintenance `json:"maintenance,omitempty"`
	RollbackMaintenance   *Maintenance `json:"rollback_maintenance,omitempty"`
	RecoveryMaintenanceID string       `json:"recovery_maintenance_id,omitempty"`
	RollbackRejoinStarted bool         `json:"rollback_rejoin_started,omitempty"`
}
type Rollout struct {
	ID                  string                `json:"id"`
	OrganizationID      string                `json:"organization_id"`
	ClusterID           string                `json:"cluster_id"`
	RequestID           string                `json:"request_id"`
	ActorUserID         string                `json:"actor_user_id,omitempty"`
	Document            ConfigurationDocument `json:"document"`
	Nodes               []RolloutNode         `json:"nodes"`
	Status              string                `json:"status"`
	ErrorCode           string                `json:"error_code"`
	Index               int                   `json:"index"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
	Deadline            time.Time             `json:"deadline"`
	CompletedAt         *time.Time            `json:"completed_at,omitempty"`
	Entrypoint          *RolloutEntrypoint    `json:"entrypoint,omitempty"`
	DrainTimeoutSeconds int                   `json:"drain_timeout_seconds,omitempty"`
}

func (r Rollout) Terminal() bool {
	return r.Status == "succeeded" || r.Status == "rolled_back" || r.Status == "rollback_failed"
}
func (i RolloutInput) Valid() bool {
	if !ValidID(i.RequestID) || !i.Document.Valid() || len(i.Nodes) < 2 || len(i.Nodes) > MaxRolloutNodes {
		return false
	}
	if i.DrainTimeoutSeconds != 0 && (i.DrainTimeoutSeconds < 5 || i.DrainTimeoutSeconds > 300) {
		return false
	}
	if i.Entrypoint != nil {
		e := i.Entrypoint
		if !ValidID(e.NodeID) || !ValidDigest(e.BindingID) || !ValidDigest(e.ExpectedGeneration) || !ValidDigest(e.ExpectedArtifactID) || e.ExpectedVersion < 1 {
			return false
		}
	}
	seen := map[string]bool{}
	for _, n := range i.Nodes {
		if !ValidID(n.NodeID) || seen[n.NodeID] || !ValidDigest(n.ExpectedGeneration) || !ValidDigest(n.ExpectedArtifactID) || n.ExpectedVersion < 1 {
			return false
		}
		seen[n.NodeID] = true
	}
	return true
}

// RolloutHealthy is stricter than an immediate configuration acknowledgement:
// every enabled, weighted server must have completed its HAProxy health checks.
func RolloutHealthy(s NodeControl, doc ConfigurationDocument, now time.Time) bool {
	return RolloutHealthIssue(s, doc, now) == ""
}
func RolloutHealthIssue(s NodeControl, doc ConfigurationDocument, now time.Time) string {
	if s.SessionID == "" || now.Sub(s.LastSeen) > 6*time.Second {
		return "control_unavailable"
	}
	if s.Discovery == nil || !s.Discovery.Fresh(now) {
		return "runtime_observation_unavailable"
	}
	if s.Discovery.Configuration == nil || !s.Discovery.Configuration.Fresh(now) {
		return "saved_configuration_unavailable"
	}
	if !s.Discovery.Configuration.Document.Equal(doc) {
		return "saved_configuration_mismatch"
	}
	if !doc.Serving(*s.Discovery) {
		return "loaded_configuration_mismatch"
	}
	frontends := 0
	for _, p := range s.Discovery.Proxies {
		if p.Kind == "frontend" {
			frontends++
			if p.Status != "OPEN" {
				return "frontend_unavailable"
			}
		}
	}
	if frontends == 0 {
		return "frontend_unavailable"
	}
	for _, b := range doc.AllBackends() {
		for _, server := range b.Servers {
			actual, ok := s.Discovery.Server(b.Name, server.Name)
			if !ok {
				return "backend_missing"
			}
			if server.Maintenance {
				if actual.State != "maint" {
					return "backend_maintenance_mismatch"
				}
				continue
			}
			if server.Weight > 0 && (actual.State != "ready" || actual.Health != "UP" || !successfulHealthCheck(actual.CheckStatus) || actual.Weight != server.Weight) {
				return fmt.Sprintf("backend_health_failed:%s/%s state=%s health=%s check=%s weight=%d/%d", b.Name, server.Name, actual.State, actual.Health, actual.CheckStatus, actual.Weight, server.Weight)
			}
		}
	}
	return ""
}

func successfulHealthCheck(status string) bool {
	// HAProxy prefixes the last completed result with "* " while a new check runs.
	status = strings.TrimPrefix(status, "* ")
	switch status {
	case "L4OK", "L6OK", "L7OK", "L7OKC":
		return true
	}
	return false
}
