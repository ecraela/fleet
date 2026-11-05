package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

func (a HTTPApplication) Digest() string {
	raw, _ := json.Marshal(a.Canonical())
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ApplicationProbe is bounded evidence from a locally configured probe location.
// It never includes request credentials, response headers, bodies or error text.
type ApplicationProbe struct {
	Scope                 string `json:"scope,omitempty"`
	PolicyDigest          string `json:"policy_digest,omitempty"`
	ArtifactID            string `json:"artifact_id,omitempty"`
	Generation            string `json:"generation,omitempty"`
	ApplicationID         string `json:"application_id"`
	SpecDigest            string `json:"spec_digest"`
	Name                  string `json:"name"`
	Scheme                string `json:"scheme"`
	Path                  string `json:"path"`
	ExpectedStatus        uint32 `json:"expected_status"`
	StatusCode            uint32 `json:"status_code"`
	ObservedAtMS          int64  `json:"observed_at_ms"`
	DurationMS            int64  `json:"duration_ms"`
	ErrorCode             string `json:"error_code"`
	TLSVerified           bool   `json:"tls_verified"`
	CertificateSHA256     string `json:"certificate_sha256"`
	CertificateNotAfterMS int64  `json:"certificate_not_after_ms"`
}

func (p ApplicationProbe) Valid() bool {
	if p.Scope != "" && p.Scope != "node" && p.Scope != "entrypoint" {
		return false
	}
	if p.Scope == "node" && (!ValidDigest(p.PolicyDigest) || !ValidDigest(p.ArtifactID) || !ValidDigest(p.Generation)) {
		return false
	}
	if p.Scope != "" && !ValidDigest(p.PolicyDigest) {
		return false
	}
	if !ValidID(p.ApplicationID) || !ValidDigest(p.SpecDigest) || !ValidRuntimeName(p.Name) || !HTTPPath(p.Path) || (p.Scheme != "http" && p.Scheme != "https") || p.ExpectedStatus < 200 || p.ExpectedStatus > 399 || (p.StatusCode != 0 && (p.StatusCode < 100 || p.StatusCode > 599)) || p.ObservedAtMS <= 0 || p.DurationMS < 0 || p.DurationMS > 5000 {
		return false
	}
	switch p.ErrorCode {
	case "", "connection_failed", "tls_failed", "response_status", "response_mismatch", "response_too_large", "deadline", "route_conflict":
	default:
		return false
	}
	if p.ErrorCode == "" && p.StatusCode != p.ExpectedStatus {
		return false
	}
	if p.Scheme == "http" {
		return !p.TLSVerified && p.CertificateSHA256 == "" && p.CertificateNotAfterMS == 0
	}
	return (p.TLSVerified && ValidDigest(p.CertificateSHA256) && p.CertificateNotAfterMS > 0) || (!p.TLSVerified && p.CertificateSHA256 == "" && p.CertificateNotAfterMS == 0 && p.ErrorCode != "")
}
func (p ApplicationProbe) Fresh(now time.Time) bool {
	age := now.Sub(time.UnixMilli(p.ObservedAtMS))
	return age >= -time.Second && age <= 15*time.Second
}

type ApplicationNodeHealth struct {
	NodeID       string             `json:"node_id"`
	Name         string             `json:"name"`
	Fresh        bool               `json:"fresh"`
	ObservedAtMS int64              `json:"observed_at_ms"`
	Loaded       bool               `json:"loaded"`
	Revision     int                `json:"revision"`
	SpecDigest   string             `json:"spec_digest"`
	Probes       []ApplicationProbe `json:"probes"`
	Servers      []BackendServer    `json:"servers"`
}

// ApplicationHealthAt keeps deployment, loaded configuration and request-path
// evidence separate. A previous success never makes stale observations healthy.
func ApplicationHealthAt(revisions []ApplicationRevision, nodes []Node, controls map[string]NodeControl, now time.Time) []ApplicationHealth {
	latest := map[string]ApplicationRevision{}
	for _, r := range revisions {
		if latest[r.ApplicationID].Number < r.Number {
			latest[r.ApplicationID] = r
		}
	}
	out := []ApplicationHealth{}
	for id, r := range latest {
		effective := EffectiveApplication(r, revisions)
		target := effective.Spec
		h := ApplicationHealth{ApplicationID: id, Configuration: "loaded", Reachability: "reachable", Nodes: []ApplicationNodeHealth{}}
		missing, failed := len(nodes) == 0, false
		for _, n := range nodes {
			c := controls[n.ID]
			d := c.Discovery
			row := ApplicationNodeHealth{NodeID: n.ID, Name: n.Name, Probes: []ApplicationProbe{}, Servers: []BackendServer{}}
			row.Fresh = c.SessionID != "" && now.Sub(c.LastSeen) >= -time.Second && now.Sub(c.LastSeen) <= 6*time.Second && d != nil && d.Fresh(now) && d.Configuration != nil && d.Configuration.Fresh(now)
			if d != nil && d.Configuration != nil {
				row.ObservedAtMS = min(c.LastSeen.UnixMilli(), d.ObservedAtMS, d.Configuration.ObservedAtMS)
			}
			if row.Fresh {
				if effective.Deleted {
					row.Loaded = true
					for _, a := range d.Configuration.Document.Applications {
						if a.ID == id {
							row.Loaded = false
						}
					}
					for _, p := range d.Proxies {
						if p.Kind == "backend" && p.Name == target.BackendName() {
							row.Loaded = false
						}
					}
					for _, s := range d.Servers {
						if s.Backend == target.BackendName() {
							row.Loaded = false
						}
					}
					if row.Loaded {
						row.Revision = effective.Number
						row.SpecDigest = target.Digest()
					}
				}
				for _, a := range d.Configuration.Document.Applications {
					if effective.Deleted {
						break
					}
					if a.ID != id {
						continue
					}
					row.Loaded = a.Equal(target) && applicationRuntimeLoaded(a, *d)
					row.SpecDigest = a.Digest()
					for _, v := range revisions {
						if v.ApplicationID == id && v.Spec.Equal(a) && v.Number > row.Revision {
							row.Revision = v.Number
						}
					}
					for _, s := range d.Servers {
						if s.Backend == a.BackendName() {
							row.Servers = append(row.Servers, s)
						}
					}
					for _, p := range d.ApplicationProbes {
						if p.ApplicationID == id && p.Scope != "node" {
							row.Probes = append(row.Probes, p)
							if !p.Fresh(now) || p.SpecDigest != a.Digest() || !row.Loaded {
								missing = true
							} else if p.ErrorCode != "" || p.StatusCode != p.ExpectedStatus || (p.Scheme == "https" && !p.TLSVerified) {
								failed = true
							}
						}
					}
				}
			}
			if !row.Fresh {
				h.Configuration = "unknown"
			} else if !row.Loaded && h.Configuration != "unknown" {
				h.Configuration = "drifted"
			}
			if len(row.Probes) == 0 && !target.Disabled && !effective.Deleted {
				missing = true
			}
			if !row.Fresh {
				missing = true
			}
			h.Nodes = append(h.Nodes, row)
		}
		if len(nodes) == 0 {
			h.Configuration = "unknown"
		}
		if r.Status == "running" || r.Status == "rolling_back" {
			h.Configuration = "updating"
		}
		if failed {
			h.Reachability = "unhealthy"
		} else if missing {
			h.Reachability = "unknown"
		}
		if target.Disabled || effective.Deleted {
			h.Reachability = "unknown"
			if h.Configuration == "loaded" {
				h.Reachability = effective.Lifecycle()
			}
		}
		out = append(out, h)
	}
	return out
}

func applicationRuntimeLoaded(a HTTPApplication, d Discovery) bool {
	proxy := false
	for _, p := range d.Proxies {
		proxy = proxy || p.Kind == "backend" && p.Name == a.BackendName() && p.Mode == "http" && p.Algorithm == a.Algorithm
	}
	if !proxy {
		return false
	}
	if a.Disabled {
		for _, s := range d.Servers {
			if s.Backend == a.BackendName() {
				return false
			}
		}
		return true
	}
	count := 0
	for _, s := range d.Servers {
		if s.Backend == a.BackendName() {
			count++
		}
	}
	if count != len(a.Servers) {
		return false
	}
	for _, s := range a.Servers {
		actual, ok := d.Server(a.BackendName(), s.Name)
		if !ok || actual.InitialWeight != s.Weight || s.Maintenance != (actual.State == "maint") {
			return false
		}
	}
	return true
}

type ApplicationHealth struct {
	ApplicationID string                  `json:"application_id"`
	Configuration string                  `json:"configuration"`
	Reachability  string                  `json:"reachability"`
	Nodes         []ApplicationNodeHealth `json:"nodes"`
}
