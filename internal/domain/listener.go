package domain

import (
	"crypto/x509"
	"maps"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"time"
)

const MaxHTTPSListeners = 4

// ListenerBinding is a node-local grant to attach HTTPS to an existing frontend.
// Addresses, ports and certificate paths cannot be supplied by a remote command.
type ListenerBinding struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    uint32 `json:"port"`
}

func (b ListenerBinding) Valid() bool {
	ip, err := netip.ParseAddr(b.Address)
	return ValidRuntimeName(b.Name) && !strings.HasPrefix(b.Name, ApplicationBackendPrefix) && (b.Address == "*" || (err == nil && ip.Zone() == "")) && b.Port > 0 && b.Port <= 65535
}

type HTTPSListener struct {
	ListenerBinding
	CertificateRef    string            `json:"certificate_ref"`
	CertificateSHA256 string            `json:"certificate_sha256"`
	NodeCertificates  map[string]string `json:"node_certificates,omitempty"`
}

func (l HTTPSListener) Valid() bool {
	if !l.ListenerBinding.Valid() || !ValidRuntimeName(l.CertificateRef) || len(l.CertificateRef) > 64 {
		return false
	}
	if len(l.NodeCertificates) == 0 {
		return ValidDigest(l.CertificateSHA256)
	}
	if l.CertificateSHA256 != "" || len(l.NodeCertificates) > MaxRolloutNodes {
		return false
	}
	for id, hash := range l.NodeCertificates {
		if !ValidID(id) || !ValidDigest(hash) {
			return false
		}
	}
	return true
}
func CanonicalListeners(values []HTTPSListener) []HTTPSListener {
	if len(values) == 0 {
		return nil
	}
	out := append([]HTTPSListener{}, values...)
	for i := range out {
		out[i].NodeCertificates = maps.Clone(out[i].NodeCertificates)
	}
	slices.SortFunc(out, func(a, b HTTPSListener) int { return strings.Compare(a.Name, b.Name) })
	return out
}
func SameListeners(a, b []HTTPSListener) bool {
	return reflect.DeepEqual(CanonicalListeners(a), CanonicalListeners(b))
}
func (l HTTPSListener) ExpectedCertificate(node string) string {
	if len(l.NodeCertificates) > 0 {
		return l.NodeCertificates[node]
	}
	return l.CertificateSHA256
}
func (l HTTPSListener) Equal(other HTTPSListener) bool {
	return SameListeners([]HTTPSListener{l}, []HTTPSListener{other})
}
func ValidListeners(values []HTTPSListener) bool {
	if len(values) > MaxHTTPSListeners {
		return false
	}
	for i, l := range values {
		if !l.Valid() {
			return false
		}
		for _, other := range values[:i] {
			if l.Name == other.Name || l.Port == other.Port {
				return false
			}
		}
	}
	return true
}

type TLSCertificate struct {
	Reference   string   `json:"reference"`
	SHA256      string   `json:"sha256"`
	DNSNames    []string `json:"dns_names"`
	NotBeforeMS int64    `json:"not_before_ms"`
	NotAfterMS  int64    `json:"not_after_ms"`
	ErrorCode   string   `json:"error_code"`
}

func (c TLSCertificate) Usable(now time.Time) bool {
	return c.ErrorCode == "" && ValidDigest(c.SHA256) && now.UnixMilli() >= c.NotBeforeMS && now.Add(time.Minute).UnixMilli() < c.NotAfterMS
}
func (c TLSCertificate) Covers(host string) bool {
	return (&x509.Certificate{DNSNames: c.DNSNames}).VerifyHostname(host) == nil
}

type TLSListenerObservation struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Loaded bool   `json:"loaded"`
}

func (s ConfigurationState) ListenerReady(l HTTPSListener, apps []HTTPApplication, now time.Time) bool {
	if s.ListenerControlVersion != 1 || !slices.Contains(s.ListenerBindings, l.ListenerBinding) {
		return false
	}
	for _, c := range s.TLSCertificates {
		if c.Reference != l.CertificateRef || c.SHA256 != l.ExpectedCertificate(s.NodeID) || !c.Usable(now) {
			continue
		}
		for _, a := range apps {
			if a.Listener == l.Name && !c.Covers(a.Hostname) {
				return false
			}
		}
		return true
	}
	return false
}
func (s ConfigurationState) ValidTLSInventory() bool {
	if (s.NodeID != "" && !ValidID(s.NodeID)) || !ValidACMEGrants(s.ACMEGrants) || s.ListenerControlVersion > 1 || len(s.ListenerBindings) > MaxHTTPSListeners || len(s.TLSCertificates) > 16 || len(s.TLSListeners) > MaxHTTPSListeners {
		return false
	}
	for _, b := range s.ListenerBindings {
		if !b.Valid() {
			return false
		}
	}
	seen := map[string]bool{}
	for _, c := range s.TLSCertificates {
		if !ValidRuntimeName(c.Reference) || len(c.Reference) > 64 || seen[c.Reference] || len(c.DNSNames) > 32 {
			return false
		}
		seen[c.Reference] = true
		if c.SHA256 != "" && !ValidDigest(c.SHA256) {
			return false
		}
		for _, name := range c.DNSNames {
			if len(name) == 0 || len(name) > 253 {
				return false
			}
		}
		switch c.ErrorCode {
		case "", "certificate_unavailable", "certificate_invalid", "certificate_expired", "certificate_not_yet_valid":
		default:
			return false
		}
	}
	for _, l := range s.TLSListeners {
		if !ValidRuntimeName(l.Name) || (l.SHA256 != "" && !ValidDigest(l.SHA256)) {
			return false
		}
	}
	return true
}

type ListenerRevision struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	ClusterID      string          `json:"cluster_id"`
	Number         int             `json:"number"`
	RequestID      string          `json:"request_id"`
	RequestDigest  string          `json:"-"`
	Listeners      []HTTPSListener `json:"listeners"`
	RolloutID      string          `json:"rollout_id"`
	RollbackOf     string          `json:"rollback_of,omitempty"`
	ActorUserID    string          `json:"actor_user_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	Status         string          `json:"status"`
}
type ListenerInput struct {
	RequestID        string                   `json:"request_id"`
	ExpectedRevision int                      `json:"expected_revision"`
	Listeners        []HTTPSListener          `json:"listeners"`
	Nodes            []RolloutTarget          `json:"nodes"`
	Entrypoint       *RolloutEntrypointTarget `json:"entrypoint"`
}
