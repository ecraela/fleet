package domain

import "time"

// The application selects protocols; destinations, trust and response assertions
// remain in the agent's local policy.
type ApplicationVerificationPolicy struct {
	HTTP       bool `json:"http"`
	HTTPS      bool `json:"https"`
	Entrypoint bool `json:"entrypoint,omitempty"`
}

const ApplicationVerificationTimeout = 30 * time.Second
const ApplicationVerificationPasses = 2

type ApplicationProbeGrant struct {
	Name           string `json:"name"`
	Scope          string `json:"scope"`
	Listener       string `json:"listener"`
	Hostname       string `json:"hostname"`
	PathPrefix     string `json:"path_prefix"`
	Path           string `json:"path"`
	Scheme         string `json:"scheme"`
	ExpectedStatus uint32 `json:"expected_status"`
	PolicyDigest   string `json:"policy_digest"`
}

func (g ApplicationProbeGrant) Valid() bool {
	a := HTTPApplication{ID: "00000000-0000-0000-0000-000000000001", Name: "Probe", Listener: g.Listener, Hostname: g.Hostname, PathPrefix: g.PathPrefix, HealthPath: "/", Algorithm: "roundrobin", Servers: []ConfigurationServer{{Name: "probe", Address: "localhost", Port: 80, Weight: 100}}}
	return a.Valid() && ValidRuntimeName(g.Name) && (g.Scope == "node" || g.Scope == "entrypoint") && HTTPPath(g.Path) && len(g.Path) >= len(g.PathPrefix) && g.Path[:len(g.PathPrefix)] == g.PathPrefix && (g.Scheme == "http" || g.Scheme == "https") && g.ExpectedStatus >= 200 && g.ExpectedStatus <= 399 && ValidDigest(g.PolicyDigest)
}
func (g ApplicationProbeGrant) Matches(a HTTPApplication) bool {
	return g.Listener == a.Listener && g.Hostname == a.Hostname && g.PathPrefix == a.PathPrefix
}
func (a HTTPApplication) RequiresVerification(scheme string) bool {
	return !a.Disabled && a.Verification != nil && ((scheme == "http" && a.Verification.HTTP) || (scheme == "https" && a.Verification.HTTPS))
}

type RolloutApplicationCheck struct {
	ApplicationProbeGrant
	ApplicationID string `json:"application_id"`
	SpecDigest    string `json:"spec_digest"`
}
type RolloutApplicationCheckResult struct {
	Name           string `json:"name"`
	Scope          string `json:"scope,omitempty"`
	ApplicationID  string `json:"application_id"`
	Scheme         string `json:"scheme"`
	Path           string `json:"path"`
	ExpectedStatus uint32 `json:"expected_status"`
	StatusCode     uint32 `json:"status_code"`
	TLSVerified    bool   `json:"tls_verified"`
	ObservedAtMS   int64  `json:"observed_at_ms"`
	Passes         int    `json:"passes"`
	ErrorCode      string `json:"error_code"`
}
type RolloutApplicationVerification struct {
	Phase       string                          `json:"phase"`
	Status      string                          `json:"status"`
	StartedAtMS int64                           `json:"started_at_ms"`
	ErrorCode   string                          `json:"error_code"`
	Checks      []RolloutApplicationCheckResult `json:"checks"`
}

// Freeze local authority for both paths. Entrypoint evidence complements direct
// node checks; a healthy peer must never stand in for the node being updated.
func ApplicationChecks(doc ConfigurationDocument, d Discovery) ([]RolloutApplicationCheck, bool) {
	checks := []RolloutApplicationCheck{}
	for _, a := range doc.Applications {
		for _, scheme := range []string{"http", "https"} {
			if !a.RequiresVerification(scheme) {
				continue
			}
			scopes := []string{"node"}
			if a.Verification.Entrypoint {
				scopes = append(scopes, "entrypoint")
			}
			for _, scope := range scopes {
				found := false
				for _, g := range d.ApplicationProbeGrants {
					if g.Valid() && g.Scope == scope && g.Matches(a) && g.Scheme == scheme {
						checks = append(checks, RolloutApplicationCheck{ApplicationProbeGrant: g, ApplicationID: a.ID, SpecDigest: a.Digest()})
						found = true
					}
				}
				if !found {
					return nil, false
				}
			}
		}
	}
	return checks, len(checks) <= 32
}

func phaseApplicationChecks(checks []RolloutApplicationCheck, phase string) []RolloutApplicationCheck {
	if phase != "before_rejoin" && phase != "after_node_restore" {
		return checks
	}
	out := []RolloutApplicationCheck{}
	for _, c := range checks {
		if c.Scope == "node" {
			out = append(out, c)
		}
	}
	return out
}

func NewApplicationVerification(checks []RolloutApplicationCheck, phase string, now time.Time) *RolloutApplicationVerification {
	checks = phaseApplicationChecks(checks, phase)
	if len(checks) == 0 {
		return nil
	}
	v := &RolloutApplicationVerification{Phase: phase, Status: "waiting", StartedAtMS: now.UnixMilli(), Checks: []RolloutApplicationCheckResult{}}
	for _, c := range checks {
		v.Checks = append(v.Checks, RolloutApplicationCheckResult{Name: c.Name, Scope: c.Scope, ApplicationID: c.ApplicationID, Scheme: c.Scheme, Path: c.Path, ExpectedStatus: c.ExpectedStatus})
	}
	return v
}

// Advance consumes distinct observations made after the phase barrier and tied
// to the exact loaded artifact/runtime generation. Replaying a report never
// earns another pass. Missing evidence waits; a verified failure stops at once.
func (v *RolloutApplicationVerification) Advance(checks []RolloutApplicationCheck, d *Discovery, now time.Time) string {
	if v == nil {
		return "passed"
	}
	if v.Status == "failed" {
		return v.Status
	}
	checks = phaseApplicationChecks(checks, v.Phase)
	failureCode := func(scope, suffix string) string {
		if scope == "entrypoint" {
			return "application_entrypoint_" + suffix
		}
		return "application_" + suffix
	}
	complete := len(checks) > 0 && len(v.Checks) == len(checks)
	for i, c := range checks {
		if i >= len(v.Checks) {
			complete = false
			break
		}
		result := &v.Checks[i]
		var p *ApplicationProbe
		if d != nil && d.Fresh(now) && d.Configuration != nil && d.Configuration.Fresh(now) {
			grant := false
			for _, g := range d.ApplicationProbeGrants {
				grant = grant || g == c.ApplicationProbeGrant
			}
			if !grant {
				result.ErrorCode = "probe_policy_changed"
				v.Status, v.ErrorCode = "failed", failureCode(c.Scope, "probe_policy_changed")
				return v.Status
			}
			for j := range d.ApplicationProbes {
				probe := &d.ApplicationProbes[j]
				if probe.Name == c.Name && probe.ApplicationID == c.ApplicationID && probe.SpecDigest == c.SpecDigest && probe.Scope == c.Scope && probe.PolicyDigest == c.PolicyDigest && probe.ArtifactID == d.Configuration.ArtifactID && probe.Generation == d.Generation && probe.Path == c.Path && probe.Scheme == c.Scheme && probe.ExpectedStatus == c.ExpectedStatus && probe.ObservedAtMS > v.StartedAtMS && probe.Fresh(now) {
					p = probe
				}
			}
		}
		if p == nil {
			result.Passes = 0
			complete = false
			continue
		}
		if c.Scheme == "https" && (!p.TLSVerified || p.CertificateNotAfterMS <= now.UnixMilli()) {
			result.ObservedAtMS, result.StatusCode, result.TLSVerified = p.ObservedAtMS, p.StatusCode, p.TLSVerified
			result.ErrorCode = "tls_failed"
			v.Status, v.ErrorCode = "failed", failureCode(c.Scope, "https_failed")
			return v.Status
		}
		if p.ObservedAtMS > result.ObservedAtMS {
			if result.Passes < ApplicationVerificationPasses && p.ObservedAtMS-result.ObservedAtMS > 15000 {
				result.Passes = 0
			}
			result.ObservedAtMS, result.StatusCode, result.TLSVerified, result.ErrorCode = p.ObservedAtMS, p.StatusCode, p.TLSVerified, p.ErrorCode
			if p.ErrorCode != "" || p.StatusCode != c.ExpectedStatus {
				if result.ErrorCode == "" {
					result.ErrorCode = "response_status"
				}
				v.Status, v.ErrorCode = "failed", failureCode(c.Scope, c.Scheme+"_failed")
				return v.Status
			}
			result.Passes = min(ApplicationVerificationPasses, result.Passes+1)
		}
		complete = complete && result.Passes >= ApplicationVerificationPasses
	}
	if complete {
		v.Status = "passed"
	} else if now.UnixMilli()-v.StartedAtMS >= ApplicationVerificationTimeout.Milliseconds() {
		v.Status, v.ErrorCode = "failed", "application_verification_timeout"
		for _, c := range v.Checks {
			if c.Passes < ApplicationVerificationPasses {
				v.ErrorCode = failureCode(c.Scope, "verification_timeout")
				break
			}
		}
	} else {
		v.Status = "waiting"
	}
	return v.Status
}
