package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ecraela/fleet/internal/buildinfo"
	"sort"
	"strconv"
	"strings"
	"time"
)

const MaxProxies = 128
const MaxBackendServers = 256
const MaxDiscoveryBytes = 96 << 10

type Proxy struct {
	ActiveConnections *uint32 `json:"active_connections,omitempty"`
	Name              string  `json:"name"`
	Kind              string  `json:"kind"`
	Mode              string  `json:"mode"`
	Algorithm         string  `json:"algorithm"`
	Status            string  `json:"status"`
}
type BackendServer struct {
	Backend           string `json:"backend"`
	Name              string `json:"name"`
	Address           string `json:"address"`
	State             string `json:"state"`
	Weight            uint32 `json:"weight"`
	InitialWeight     uint32 `json:"initial_weight"`
	ActiveConnections uint32 `json:"active_connections"`
	Health            string `json:"health"`
	CheckStatus       string `json:"check_status,omitempty"`
	AdminFlags        uint32 `json:"admin_flags"`
}
type Discovery struct {
	Setup                  *NodeSetup              `json:"setup,omitempty"`
	Agent                  *buildinfo.Info         `json:"agent,omitempty"`
	ApplicationProbeGrants []ApplicationProbeGrant `json:"application_probe_grants,omitempty"`
	ApplicationProbes      []ApplicationProbe      `json:"application_probes,omitempty"`
	Configuration          *ConfigurationState     `json:"configuration,omitempty"`
	Version                string                  `json:"version"`
	ProcessID              string                  `json:"process_id"`
	StartedAt              int64                   `json:"started_at"`
	ConfigFingerprint      string                  `json:"config_fingerprint"`
	Generation             string                  `json:"generation"`
	ControlAllowed         bool                    `json:"control_allowed"`
	ObservedAtMS           int64                   `json:"observed_at_ms"`
	ErrorCode              string                  `json:"error_code"`
	Proxies                []Proxy                 `json:"proxies"`
	Servers                []BackendServer         `json:"servers"`
	ConfigFiles            uint32                  `json:"config_files"`
}
type NodeControl struct {
	ConfigurationHistoryLimited bool       `json:"configuration_history_limited,omitempty"`
	MaintenanceID               string     `json:"maintenance_id,omitempty"`
	MaintenanceBlocked          bool       `json:"maintenance_blocked,omitempty"`
	ActiveRolloutID             string     `json:"active_rollout_id,omitempty"`
	CurrentRevisionID           string     `json:"current_revision_id,omitempty"`
	NodeID                      string     `json:"node_id"`
	SessionID                   string     `json:"-"`
	ConnectedAt                 time.Time  `json:"connected_at"`
	LastSeen                    time.Time  `json:"last_seen"`
	Discovery                   *Discovery `json:"discovery"`
	PendingOperationID          string     `json:"-"`
}
type OperationResult struct {
	OperationID   string
	Outcome       string
	ErrorCode     string
	Discovery     *Discovery
	CommandDigest string
}
type Operation struct {
	MaintenanceID           string                 `json:"maintenance_id,omitempty"`
	RolloutID               string                 `json:"rollout_id,omitempty"`
	ConfigurationRevisionID string                 `json:"configuration_revision_id,omitempty"`
	Configuration           *ConfigurationDocument `json:"configuration,omitempty"`
	BaseArtifactID          string                 `json:"base_artifact_id,omitempty"`
	BaseVersion             int64                  `json:"base_version,omitempty"`
	RollbackArtifactID      string                 `json:"rollback_artifact_id,omitempty"`
	ExpectedProcessID       string                 `json:"expected_process_id,omitempty"`
	ID                      string                 `json:"id"`
	RequestID               string                 `json:"request_id"`
	OrganizationID          string                 `json:"organization_id"`
	NodeID                  string                 `json:"node_id"`
	Kind                    string                 `json:"kind"`
	Backend                 string                 `json:"backend"`
	Server                  string                 `json:"server"`
	Weight                  uint32                 `json:"weight"`
	ExpectedGeneration      string                 `json:"expected_generation"`
	ExpectedState           string                 `json:"expected_state"`
	ExpectedWeight          uint32                 `json:"expected_weight"`
	Status                  string                 `json:"status"`
	ErrorCode               string                 `json:"error_code"`
	ActorUserID             string                 `json:"actor_user_id,omitempty"`
	CreatedAt               time.Time              `json:"created_at"`
	Deadline                time.Time              `json:"deadline"`
	CompletedAt             *time.Time             `json:"completed_at,omitempty"`
	CommandDigest           string                 `json:"-"`
}

// Runtime identifiers are literal CLI tokens, never caller-provided commands.
func ValidRuntimeName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.:-", c)) {
			return false
		}
	}
	return true
}
func (d Discovery) Server(backend, name string) (BackendServer, bool) {
	for _, s := range d.Servers {
		if s.Backend == backend && s.Name == name {
			return s, true
		}
	}
	return BackendServer{}, false
}
func (d Discovery) Fresh(now time.Time) bool {
	age := now.Sub(time.UnixMilli(d.ObservedAtMS))
	return d.ErrorCode == "" && age >= -time.Minute && age <= 6*time.Second
}
func (d Discovery) HasCapacity(backend, excluding string) bool {
	for _, s := range d.Servers {
		if s.Backend == backend && s.Name != excluding && s.State == "ready" && s.Weight > 0 && strings.HasPrefix(s.Health, "UP") {
			return true
		}
	}
	return false
}
func (d Discovery) Valid() bool {
	if d.Setup != nil && !d.Setup.Valid() {
		return false
	}
	if d.Agent != nil && (len(d.Agent.Version) > 64 || len(d.Agent.Revision) > 64 || len(d.Agent.Platform) > 32 || d.Agent.Protocol != buildinfo.Protocol || !ValidDigest(d.Agent.ExecutableSHA256)) {
		return false
	}
	if len(d.ApplicationProbeGrants) > 32 {
		return false
	}
	grants := map[string]bool{}
	for _, g := range d.ApplicationProbeGrants {
		if !g.Valid() || grants[g.Name] {
			return false
		}
		grants[g.Name] = true
	}
	if len(d.ApplicationProbes) > 32 {
		return false
	}
	checks := map[string]bool{}
	for _, p := range d.ApplicationProbes {
		if !p.Valid() || checks[p.Name] {
			return false
		}
		checks[p.Name] = true
	}
	if len(d.Proxies) > MaxProxies || len(d.Servers) > MaxBackendServers {
		return false
	}
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > MaxDiscoveryBytes {
		return false
	}
	if d.Configuration != nil && !d.Configuration.Valid() {
		return false
	}
	if d.ErrorCode != "" {
		valid := d.ErrorCode == "runtime_unavailable" || d.ErrorCode == "runtime_not_configured" || d.ErrorCode == "unsupported_runtime"
		return valid && len(d.Version) <= 128 && len(d.Proxies) == 0 && len(d.Servers) == 0 && d.Generation == "" && d.ConfigFingerprint == "" && !d.ControlAllowed && d.ProcessID == "" && d.StartedAt == 0 && d.ConfigFiles == 0
	}
	if len(d.Version) == 0 || len(d.Version) > 128 || len(d.ProcessID) == 0 || len(d.ProcessID) > 32 || d.StartedAt <= 0 || !ValidDigest(d.Generation) || (!ValidDigest(d.ConfigFingerprint) && d.ConfigFingerprint != "") || d.ConfigFiles > 16 || len(d.Proxies) > MaxProxies || len(d.Servers) > MaxBackendServers {
		return false
	}
	seen := map[string]bool{}
	for _, p := range d.Proxies {
		key := p.Kind + "/" + p.Name
		if !ValidRuntimeName(p.Name) || (p.Kind != "frontend" && p.Kind != "backend") || seen[key] || len(p.Mode) > 16 || len(p.Algorithm) > 64 || len(p.Status) > 64 {
			return false
		}
		seen[key] = true
	}
	for _, s := range d.Servers {
		key := "server/" + s.Backend + "/" + s.Name
		if !ValidRuntimeName(s.Backend) || !ValidRuntimeName(s.Name) || seen[key] || len(s.Address) > 255 || len(s.Health) > 64 || len(s.CheckStatus) > 32 || s.Weight > 256 || s.InitialWeight > 256 || (s.State != "ready" && s.State != "drain" && s.State != "maint") {
			return false
		}
		seen[key] = true
	}
	return true
}
func (o Operation) Valid() bool {
	if o.Kind == "configuration" {
		return ValidID(o.RequestID) && ValidID(o.NodeID) && ValidID(o.ConfigurationRevisionID) && ValidDigest(o.ExpectedGeneration) && ValidDigest(o.BaseArtifactID) && o.BaseVersion > 0 && o.Configuration != nil && o.Configuration.Valid() && (o.RollbackArtifactID == "" || ValidDigest(o.RollbackArtifactID)) && len(o.ExpectedProcessID) > 0 && len(o.ExpectedProcessID) <= 32
	}
	return ValidID(o.RequestID) && ValidID(o.NodeID) && ValidRuntimeName(o.Backend) && ValidRuntimeName(o.Server) && ValidDigest(o.ExpectedGeneration) && (o.Kind == "maintenance" || o.Kind == "drain" || o.Kind == "restore" || o.Kind == "weight") && o.Weight <= 256 && (o.Kind == "weight" || o.Weight == 0) && (o.ExpectedState == "ready" || o.ExpectedState == "drain" || o.ExpectedState == "maint") && o.ExpectedWeight <= 256
}
func (o Operation) Terminal() bool {
	return o.Status == "succeeded" || o.Status == "failed" || o.Status == "unknown"
}
func (o Operation) Verified(d Discovery) bool { return o.VerifiedAt(d, time.Now()) }
func (o Operation) VerifiedAt(d Discovery, now time.Time) bool {
	if o.Kind == "configuration" {
		return d.ErrorCode == "" && d.ProcessID != o.ExpectedProcessID && d.Configuration != nil && d.Configuration.Fresh(now) && o.Configuration != nil && d.Configuration.Document.Equal(*o.Configuration) && o.Configuration.Serving(d)
	}
	s, ok := d.Server(o.Backend, o.Server)
	if !ok || d.Generation != o.ExpectedGeneration || d.ErrorCode != "" {
		return false
	}
	switch o.Kind {
	case "maintenance":
		return s.State == "maint"
	case "drain":
		return s.State == "drain" && s.ActiveConnections == 0
	case "restore":
		return s.State == "ready"
	case "weight":
		return s.Weight == o.Weight
	}
	return false
}
func (o Operation) Digest() string {
	fields := []any{o.ID, o.Kind, o.Backend, o.Server, o.Weight, o.ExpectedGeneration, o.ExpectedState, o.ExpectedWeight, o.Deadline.UnixMilli()}
	if o.Kind == "configuration" {
		fields = append(fields, o.ConfigurationRevisionID, o.Configuration, o.BaseArtifactID, o.BaseVersion, o.RollbackArtifactID, o.ExpectedProcessID)
	}
	raw, _ := json.Marshal(fields)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Generation excludes changing counters, health, runtime weights and state.
func (d *Discovery) SetGeneration() {
	sort.Slice(d.Proxies, func(i, j int) bool {
		return d.Proxies[i].Kind+"/"+d.Proxies[i].Name < d.Proxies[j].Kind+"/"+d.Proxies[j].Name
	})
	sort.Slice(d.Servers, func(i, j int) bool {
		return d.Servers[i].Backend+"/"+d.Servers[i].Name < d.Servers[j].Backend+"/"+d.Servers[j].Name
	})
	names := []string{}
	for _, p := range d.Proxies {
		names = append(names, p.Kind+"/"+p.Name+"/"+p.Mode+"/"+p.Algorithm)
	}
	for _, s := range d.Servers {
		names = append(names, s.Backend+"/"+s.Name+"/"+strconv.FormatUint(uint64(s.InitialWeight), 10))
	}
	raw, _ := json.Marshal([]any{d.Version, d.ProcessID, d.StartedAt, d.ConfigFingerprint, names})
	sum := sha256.Sum256(raw)
	d.Generation = hex.EncodeToString(sum[:])
}
