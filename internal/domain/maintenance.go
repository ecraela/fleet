package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"strconv"
	"time"
)

// EntrypointControl binds enrolled identities and literal Runtime API names.
// Display URLs and addresses never become management destinations.
type EntrypointControl struct {
	NodeID  string             `json:"node_id"`
	Backend string             `json:"backend"`
	Servers []EntrypointServer `json:"servers"`
}
type EntrypointServer struct {
	NodeID string `json:"node_id"`
	Server string `json:"server"`
}

func (t Topology) BindingID() string {
	raw, _ := json.Marshal(t)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type MaintenanceInput struct {
	RequestID                    string `json:"request_id"`
	Action                       string `json:"action"`
	MaintenanceID                string `json:"maintenance_id,omitempty"`
	BindingID                    string `json:"binding_id"`
	ExpectedGeneration           string `json:"expected_generation"`
	ExpectedEntrypointGeneration string `json:"expected_entrypoint_generation"`
	ExpectedArtifactID           string `json:"expected_artifact_id"`
	ExpectedVersion              int64  `json:"expected_version"`
	DrainTimeoutSeconds          int    `json:"drain_timeout_seconds"`
}

func (i MaintenanceInput) Valid() bool {
	return ValidID(i.RequestID) && (i.Action == "withdraw" || i.Action == "rejoin") && (i.MaintenanceID == "" || ValidID(i.MaintenanceID)) && (i.Action != "rejoin" || i.MaintenanceID != "") && ValidDigest(i.BindingID) && ValidDigest(i.ExpectedGeneration) && ValidDigest(i.ExpectedEntrypointGeneration) && ValidDigest(i.ExpectedArtifactID) && i.ExpectedVersion > 0 && i.DrainTimeoutSeconds >= 5 && i.DrainTimeoutSeconds <= 300
}

type Maintenance struct {
	ID                  string                `json:"id"`
	OrganizationID      string                `json:"organization_id"`
	ClusterID           string                `json:"cluster_id"`
	NodeID              string                `json:"node_id"`
	ControllerNodeID    string                `json:"controller_node_id"`
	ControllerClusterID string                `json:"controller_cluster_id"`
	ActorUserID         string                `json:"actor_user_id,omitempty"`
	BindingID           string                `json:"binding_id"`
	Backend             string                `json:"backend"`
	Server              string                `json:"server"`
	Address             string                `json:"address"`
	Frontend            string                `json:"frontend"`
	Topology            Topology              `json:"topology"`
	Baseline            ConfigurationDocument `json:"baseline"`
	OwnedArtifactID     string                `json:"owned_artifact_id"`
	OwnedVersion        int64                 `json:"owned_version"`
	TargetGeneration    string                `json:"target_generation"`
	TargetArtifactID    string                `json:"target_artifact_id,omitempty"`
	Status              string                `json:"status"`
	Step                string                `json:"step"`
	ErrorCode           string                `json:"error_code"`
	OperationID         string                `json:"operation_id,omitempty"`
	DrainTimeoutSeconds int                   `json:"drain_timeout_seconds"`
	Connections         *uint32               `json:"connections,omitempty"`
	HealthSinceMS       int64                 `json:"health_since_ms"`
	LastHealthMS        int64                 `json:"last_health_ms"`
	LastObservationMS   int64                 `json:"last_observation_ms"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
	Deadline            time.Time             `json:"deadline"`
	CompletedAt         *time.Time            `json:"completed_at,omitempty"`
}

func (m Maintenance) Terminal() bool { return m.Status == "completed" || m.Status == "failed" }
func (m Maintenance) Document(withdrawn bool) ConfigurationDocument {
	doc := m.Baseline.Canonical()
	for b := range doc.Backends {
		if doc.Backends[b].Name == m.Backend {
			for s := range doc.Backends[b].Servers {
				if doc.Backends[b].Servers[s].Name == m.Server {
					doc.Backends[b].Servers[s].Maintenance = withdrawn
				}
			}
		}
	}
	return doc
}
func MaintenanceAddress(doc ConfigurationDocument, backend, server string) string {
	for _, b := range doc.Backends {
		if b.Name == backend {
			for _, s := range b.Servers {
				if s.Name == server {
					if s.ForwardIncomingPort {
						return net.JoinHostPort(s.Address, "*")
					}
					return net.JoinHostPort(s.Address, strconv.Itoa(int(s.Port)))
				}
			}
		}
	}
	return ""
}
func FrontendConnections(s NodeControl, frontend string, now time.Time) (uint32, bool) {
	if s.SessionID == "" || now.Sub(s.LastSeen) > 6*time.Second || s.Discovery == nil || !s.Discovery.Fresh(now) {
		return 0, false
	}
	for _, p := range s.Discovery.Proxies {
		if p.Kind == "frontend" && p.Name == frontend && p.Status == "OPEN" && p.ActiveConnections != nil {
			return *p.ActiveConnections, true
		}
	}
	return 0, false
}

type MaintenanceView struct {
	Frontend         string       `json:"frontend"`
	NodeID           string       `json:"node_id"`
	ClusterID        string       `json:"cluster_id"`
	BindingID        string       `json:"binding_id,omitempty"`
	ReasonCode       string       `json:"reason_code"`
	Entrypoint       string       `json:"entrypoint"`
	Backend          string       `json:"backend"`
	Server           string       `json:"server"`
	ControllerNodeID string       `json:"controller_node_id,omitempty"`
	ControllerName   string       `json:"controller_name"`
	Target           NodeControl  `json:"target"`
	Controller       NodeControl  `json:"controller"`
	Current          *Maintenance `json:"maintenance,omitempty"`
}
