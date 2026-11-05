package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

type ACMEChallenge struct {
	Hostname string `json:"hostname"`
	Target   string `json:"target"`
}
type ACMEGrant struct {
	Issuer       string          `json:"issuer"`
	Listener     string          `json:"listener"`
	Environment  string          `json:"environment"`
	Challenges   []ACMEChallenge `json:"challenges"`
	PolicyDigest string          `json:"policy_digest"`
}

func DNSHostname(name string) bool {
	if len(name) == 0 || len(name) > 253 || name != strings.ToLower(name) {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func ValidACMEGrants(grants []ACMEGrant) bool {
	if len(grants) > 4 {
		return false
	}
	seen := map[string]bool{}
	for _, g := range grants {
		if !ValidRuntimeName(g.Issuer) || len(g.Issuer) > 40 || !ValidRuntimeName(g.Listener) || !ValidDigest(g.PolicyDigest) || (g.Environment != "production" && g.Environment != "staging") || len(g.Challenges) == 0 || len(g.Challenges) > 8 || seen[g.Issuer+"/"+g.Listener] {
			return false
		}
		seen[g.Issuer+"/"+g.Listener] = true
		names := map[string]bool{}
		for _, c := range g.Challenges {
			if !DNSHostname(c.Hostname) || !DNSHostname(c.Target) || c.Target == c.Hostname || names[c.Hostname] {
				return false
			}
			names[c.Hostname] = true
		}
	}
	return true
}
func (g ACMEGrant) Covers(names []string) bool {
	for _, name := range names {
		if !slices.ContainsFunc(g.Challenges, func(c ACMEChallenge) bool { return c.Hostname == name }) {
			return false
		}
	}
	return len(names) > 0
}

type ACMEJob struct {
	ID           string          `json:"id"`
	PlanID       string          `json:"plan_id"`
	Generation   int             `json:"generation"`
	NodeID       string          `json:"node_id"`
	Issuer       string          `json:"issuer"`
	Listener     string          `json:"listener"`
	Hostnames    []string        `json:"hostnames"`
	PolicyDigest string          `json:"policy_digest"`
	Reference    string          `json:"reference"`
	Status       string          `json:"status"`
	ErrorCode    string          `json:"error_code"`
	Certificate  *TLSCertificate `json:"certificate,omitempty"`
	DeadlineMS   int64           `json:"deadline_ms"`
	Attempts     int             `json:"attempts"`
	NextTryMS    int64           `json:"next_try_ms"`
}

func (j ACMEJob) Valid() bool {
	if !ValidID(j.ID) || !ValidID(j.PlanID) || !ValidID(j.NodeID) || j.Generation < 1 || j.Generation > 1000000 || !ValidRuntimeName(j.Issuer) || !ValidRuntimeName(j.Listener) || !ValidDigest(j.PolicyDigest) || j.Reference != ACMEReference(j.PlanID, j.Generation) || len(j.Hostnames) == 0 || len(j.Hostnames) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, h := range j.Hostnames {
		if !DNSHostname(h) || seen[h] {
			return false
		}
		seen[h] = true
	}
	return true
}
func ACMEReference(plan string, generation int) string {
	// Keep aliases independent of domain/display names and within the TLS limit.
	raw, _ := json.Marshal([]any{plan, generation})
	sum := sha256.Sum256(raw)
	return "acme-" + hex.EncodeToString(sum[:20])
}

type ACMEResult struct {
	JobID             string          `json:"job_id"`
	Certificate       *TLSCertificate `json:"certificate,omitempty"`
	ErrorCode         string          `json:"error_code"`
	RetryAfterSeconds int             `json:"retry_after_seconds,omitempty"`
}

func ACMEError(code string) bool {
	return slices.Contains([]string{"", "delegation_missing", "dns_update_failed", "dns_propagation_failed", "acme_unavailable", "acme_validation_failed", "acme_rate_limited", "certificate_invalid", "certificate_storage_full", "policy_unavailable", "authority_expired", "issuance_timeout", "local_storage_failed"}, code)
}

// Temporary CA and transport failures must not permanently stop renewal.
// Configuration, DNS authority and invalid certificate failures remain bounded.
func ACMERetryable(code string) bool {
	return slices.Contains([]string{"acme_unavailable", "acme_rate_limited", "issuance_timeout"}, code)
}

type ACMEHistory struct {
	Generation    int                       `json:"generation"`
	Reference     string                    `json:"reference"`
	RolloutID     string                    `json:"rollout_id"`
	Status        string                    `json:"status"`
	CompletedAtMS int64                     `json:"completed_at_ms"`
	Certificates  map[string]TLSCertificate `json:"certificates"`
}
type ACMEAction struct {
	RequestID   string `json:"request_id"`
	Digest      string `json:"digest"`
	Action      string `json:"action"`
	ActorUserID string `json:"actor_user_id,omitempty"`
	AtMS        int64  `json:"at_ms"`
}
type ACMEInput struct {
	RequestID       string          `json:"request_id"`
	ExpectedVersion int             `json:"expected_version"`
	Action          string          `json:"action"`
	Issuer          string          `json:"issuer"`
	Binding         ListenerBinding `json:"binding"`
}
type ACMEPlan struct {
	ActorUserID    string          `json:"actor_user_id,omitempty"`
	BindingID      string          `json:"binding_id"`
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	ClusterID      string          `json:"cluster_id"`
	Version        int             `json:"version"`
	Enabled        bool            `json:"enabled"`
	Issuer         string          `json:"issuer"`
	Environment    string          `json:"environment"`
	Binding        ListenerBinding `json:"binding"`
	Hostnames      []string        `json:"hostnames"`
	Challenges     []ACMEChallenge `json:"challenges"`
	Generation     int             `json:"generation"`
	Status         string          `json:"status"`
	ErrorCode      string          `json:"error_code"`
	Jobs           []ACMEJob       `json:"jobs"`
	Actions        []ACMEAction    `json:"actions"`
	History        []ACMEHistory   `json:"history"`
	Activation     *ListenerInput  `json:"activation,omitempty"`
	RolloutID      string          `json:"rollout_id,omitempty"`
	RenewAtMS      int64           `json:"renew_at_ms"`
	NextRunMS      int64           `json:"next_run_ms"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// Renewal is scheduled before expiry from the shortest-lived node certificate.
// Deterministic jitter spreads profiles while remaining safely inside validity.
func ACMERenewAt(id string, certs map[string]TLSCertificate) int64 {
	next := int64(0)
	sum := sha256.Sum256([]byte(id))
	for _, c := range certs {
		lifetime := c.NotAfterMS - c.NotBeforeMS
		due := c.NotBeforeMS + lifetime*int64(60+sum[0]%10)/100
		if next == 0 || due < next {
			next = due
		}
	}
	return next
}
