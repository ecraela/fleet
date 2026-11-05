package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"time"
)

const MaxConfigurationBytes = 16 << 10

type ConfigurationServer struct {
	Name                string `json:"name"`
	Address             string `json:"address"`
	Port                uint32 `json:"port"`
	Weight              uint32 `json:"weight"`
	Maintenance         bool   `json:"maintenance"`
	ForwardIncomingPort bool   `json:"forward_incoming_port,omitempty"`
}
type ConfigurationBackend struct {
	Name      string                `json:"name"`
	Algorithm string                `json:"algorithm"`
	Servers   []ConfigurationServer `json:"servers"`
}
type ConfigurationDocument struct {
	Backends     []ConfigurationBackend `json:"backends"`
	Applications []HTTPApplication      `json:"applications,omitempty"`
	Listeners    []HTTPSListener        `json:"listeners,omitempty"`
}
type ConfigurationState struct {
	NodeID                    string                   `json:"node_id,omitempty"`
	ACMEGrants                []ACMEGrant              `json:"acme_grants,omitempty"`
	Version                   int64                    `json:"version"`
	ArtifactID                string                   `json:"artifact_id"`
	Document                  *ConfigurationDocument   `json:"document,omitempty"`
	ObservedAtMS              int64                    `json:"observed_at_ms"`
	ControlAllowed            bool                     `json:"control_allowed"`
	ErrorCode                 string                   `json:"error_code"`
	ApplicationControlVersion uint32                   `json:"application_control_version,omitempty"`
	RequestLogsAllowed        bool                     `json:"request_logs_allowed,omitempty"`
	TrafficRules              *TrafficCapabilities     `json:"traffic_rules,omitempty"`
	ListenerControlVersion    uint32                   `json:"listener_control_version,omitempty"`
	ListenerBindings          []ListenerBinding        `json:"listener_bindings,omitempty"`
	TLSCertificates           []TLSCertificate         `json:"tls_certificates,omitempty"`
	TLSListeners              []TLSListenerObservation `json:"tls_listeners,omitempty"`
}
type ConfigurationRevision struct {
	ID             string                `json:"id"`
	OrganizationID string                `json:"organization_id"`
	NodeID         string                `json:"node_id"`
	Number         int64                 `json:"number"`
	ParentID       string                `json:"parent_id,omitempty"`
	RollbackOf     string                `json:"rollback_of,omitempty"`
	OperationID    string                `json:"operation_id,omitempty"`
	ArtifactID     string                `json:"artifact_id,omitempty"`
	Document       ConfigurationDocument `json:"document"`
	Source         string                `json:"source"`
	Status         string                `json:"status"`
	ErrorCode      string                `json:"error_code"`
	ActorUserID    string                `json:"actor_user_id,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
	CompletedAt    *time.Time            `json:"completed_at,omitempty"`
}

func ConfigurationAddress(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	if net.ParseIP(s) != nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}
func (d ConfigurationDocument) Valid() bool {
	if len(d.Backends) == 0 || len(d.Backends)+len(d.Applications) > 32 || !ValidApplications(d.Applications) || !ValidListeners(d.Listeners) {
		return false
	}
	seen := map[string]bool{}
	count := 0
	for _, b := range d.AllBackends() {
		if !ValidRuntimeName(b.Name) || seen[b.Name] || (b.Algorithm != "roundrobin" && b.Algorithm != "leastconn" && b.Algorithm != "first") || len(b.Servers) == 0 {
			return false
		}
		seen[b.Name] = true
		servers := map[string]bool{}
		available := false
		for _, s := range b.Servers {
			count++
			if count > 128 || !ValidRuntimeName(s.Name) || servers[s.Name] || !ConfigurationAddress(s.Address) || (s.Port == 0) != s.ForwardIncomingPort || s.Port > 65535 || s.Weight > 256 {
				return false
			}
			servers[s.Name] = true
			available = available || (!s.Maintenance && s.Weight > 0)
		}
		if !available {
			return false
		}
	}
	raw, err := json.Marshal(d)
	return err == nil && len(raw) <= MaxConfigurationBytes
}
func (d ConfigurationDocument) Canonical() ConfigurationDocument {
	copy := ConfigurationDocument{Backends: append([]ConfigurationBackend{}, d.Backends...)}
	copy.Listeners = CanonicalListeners(d.Listeners)
	for _, a := range d.Applications {
		copy.Applications = append(copy.Applications, a.Canonical())
	}
	slices.SortFunc(copy.Applications, func(a, b HTTPApplication) int { return strings.Compare(a.ID, b.ID) })
	for i := range copy.Backends {
		copy.Backends[i].Servers = append([]ConfigurationServer{}, copy.Backends[i].Servers...)
		slices.SortFunc(copy.Backends[i].Servers, func(a, b ConfigurationServer) int { return strings.Compare(a.Name, b.Name) })
	}
	slices.SortFunc(copy.Backends, func(a, b ConfigurationBackend) int { return strings.Compare(a.Name, b.Name) })
	return copy
}
func (d ConfigurationDocument) Digest() string {
	raw, _ := json.Marshal(d.Canonical())
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (d ConfigurationDocument) Equal(other ConfigurationDocument) bool {
	return d.Digest() == other.Digest()
}
func (s ConfigurationState) Valid() bool {
	if s.TrafficRules != nil && !s.TrafficRules.Valid() {
		return false
	}
	if !s.ValidTLSInventory() {
		return false
	}
	if s.ErrorCode != "" {
		switch s.ErrorCode {
		case "configuration_not_configured", "configuration_unavailable", "configuration_unsupported", "configuration_storage_full":
		default:
			return false
		}
		return s.Document == nil && s.ArtifactID == "" && !s.ControlAllowed
	}
	return s.Version > 0 && ValidDigest(s.ArtifactID) && s.Document != nil && s.Document.Valid()
}
func (s ConfigurationState) Fresh(now time.Time) bool {
	age := now.Sub(time.UnixMilli(s.ObservedAtMS))
	return s.ErrorCode == "" && s.Valid() && age >= -time.Minute && age <= 6*time.Second
}

// Serving verifies loaded configuration fields, keeping temporary runtime overrides separate.
func (doc ConfigurationDocument) Serving(d Discovery) bool {
	for _, p := range d.Proxies {
		if p.Kind == "backend" && strings.HasPrefix(p.Name, ApplicationBackendPrefix) && !slices.ContainsFunc(doc.Applications, func(a HTTPApplication) bool { return a.BackendName() == p.Name }) {
			return false
		}
	}
	for _, a := range doc.Applications {
		if !a.Disabled {
			continue
		}
		found := false
		for _, p := range d.Proxies {
			found = found || (p.Kind == "backend" && p.Name == a.BackendName() && p.Mode == "http" && p.Algorithm == a.Algorithm)
		}
		if !found {
			return false
		}
		for _, s := range d.Servers {
			if s.Backend == a.BackendName() {
				return false
			}
		}
	}
	for _, listener := range doc.Listeners {
		loaded := false
		httpFrontend := false
		for _, p := range d.Proxies {
			httpFrontend = httpFrontend || (p.Kind == "frontend" && p.Name == listener.Name && p.Mode == "http" && p.Status == "OPEN")
		}
		if !httpFrontend {
			return false
		}
		if d.Configuration == nil {
			return false
		}
		for _, s := range d.Configuration.TLSListeners {
			loaded = loaded || (s.Name == listener.Name && s.Loaded && s.SHA256 == listener.ExpectedCertificate(d.Configuration.NodeID))
		}
		if !loaded {
			return false
		}
	}
	for _, b := range doc.AllBackends() {
		proxy := false
		for _, p := range d.Proxies {
			if p.Kind == "backend" && p.Name == b.Name && p.Algorithm == b.Algorithm {
				proxy = true
				break
			}
		}
		if !proxy {
			return false
		}
		count := 0
		healthy := false
		for _, s := range d.Servers {
			if s.Backend == b.Name {
				count++
			}
		}
		if count != len(b.Servers) {
			return false
		}
		for _, s := range b.Servers {
			actual, ok := d.Server(b.Name, s.Name)
			if !ok || actual.InitialWeight != s.Weight || (s.Maintenance && actual.State != "maint") {
				return false
			}
			healthy = healthy || (!s.Maintenance && s.Weight > 0 && strings.HasPrefix(actual.Health, "UP"))
		}
		if !healthy {
			return false
		}
	}
	return true
}

func (d ConfigurationDocument) AllBackends() []ConfigurationBackend {
	out := append([]ConfigurationBackend{}, d.Backends...)
	for _, a := range d.Applications {
		if !a.Disabled {
			out = append(out, a.Backend())
		}
	}
	return out
}

// KnownIncomingPorts preserves an observed infrastructure-only port profile;
// ordinary typed edits cannot introduce it on an HTTP backend or a new server.
func (d ConfigurationDocument) KnownIncomingPorts(base ConfigurationDocument) bool {
	for _, b := range d.Backends {
		for _, s := range b.Servers {
			if !s.ForwardIncomingPort {
				continue
			}
			found := false
			for _, old := range base.Backends {
				if old.Name != b.Name {
					continue
				}
				for _, server := range old.Servers {
					found = found || (server.Name == s.Name && server.ForwardIncomingPort)
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}
