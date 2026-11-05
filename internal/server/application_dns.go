package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type dnsIntentInput struct {
	RequestID        string `json:"request_id"`
	RevisionID       string `json:"revision_id"`
	ZoneID           string `json:"zone_id"`
	WorkingVersionID string `json:"working_version_id"`
	RecordType       string `json:"record_type"`
	Value            string `json:"value"`
	TTL              int    `json:"ttl"`
}

// Purpose separation prevents a DNS intent from being used as a login grant.
const dnsIntentPurpose = "haproxy-dns-v1."

func (s *Server) applicationDNSIntent(w http.ResponseWriter, r *http.Request, org string) {
	actor := domain.Actor(r.Context())
	if !domain.ValidID(actor) || (len(s.config.Browser.Secret) < 32 && !s.isConnectorRequest(r)) {
		fail(w, 403, "Connect DNS requires a Converge management login")
		return
	}
	var input dnsIntentInput
	if !readJSON(w, r, &input) {
		return
	}
	if !validDNSIntentInput(input) || !domain.ValidID(r.PathValue("application")) {
		fail(w, 400, "provide a zone, current versions, and an A, AAAA or CNAME traffic entrypoint")
		return
	}
	rows, nodes, controls, err := s.applicationSnapshot(r.Context(), org, r.PathValue("cluster"))
	if err != nil {
		if err == errApplicationHealthUnavailable {
			fail(w, 503, err.Error())
		} else {
			storeError(w, err)
		}
		return
	}
	now := time.Now()
	revision, verifiedAt, err := verifiedDNSApplication(rows, nodes, controls, r.PathValue("application"), input.RevisionID, now)
	if err != nil {
		if err == domain.ErrNotFound {
			storeError(w, err)
		} else {
			fail(w, 409, "application must have its current successful version loaded with fresh passing entrypoint checks on every node")
		}
		return
	}
	claims := map[string]any{
		"v": 1, "iss": s.config.Browser.Audience, "aud": "converge-dns", "sub": actor, "org": org,
		"request_id": input.RequestID, "cluster_id": r.PathValue("cluster"), "application_id": revision.ApplicationID,
		"revision_id": revision.ID, "revision_number": revision.Number, "hostname": revision.Spec.Hostname,
		"zone_id": input.ZoneID, "working_version_id": input.WorkingVersionID, "record_type": input.RecordType,
		"value": input.Value, "ttl": input.TTL, "verified_at_ms": verifiedAt, "iat": now.Unix(), "exp": now.Unix() + 60,
	}
	s.writeDNSIntent(w, r, claims)
}
func (s *Server) isConnectorRequest(r *http.Request) bool { _, ok := s.connectorIdentity(r); return ok }

func (s *Server) writeDNSIntent(w http.ResponseWriter, r *http.Request, claims map[string]any) {
	if s.isConnectorRequest(r) {
		writeJSON(w, 200, map[string]any{"connector_dns_claims": claims})
		return
	}
	raw, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, s.config.Browser.Secret)
	_, _ = mac.Write([]byte(dnsIntentPurpose + payload))
	writeJSON(w, 200, map[string]any{"intent": payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), "organization_id": claims["org"], "cluster_id": claims["cluster_id"], "application_id": claims["application_id"], "request_id": claims["request_id"]})
}

func validDNSIntentInput(in dnsIntentInput) bool {
	if !domain.ValidID(in.RequestID) || !domain.ValidID(in.RevisionID) || !domain.ValidID(in.ZoneID) || !domain.ValidID(in.WorkingVersionID) || in.TTL < 60 || in.TTL > 86400 {
		return false
	}
	if in.RecordType == "CNAME" {
		return len(in.Value) <= 253 && in.Value == strings.ToLower(in.Value) && dnsHostname(in.Value)
	}
	a, err := netip.ParseAddr(in.Value)
	if err != nil || a.Zone() != "" || a.Is4In6() || a.String() != in.Value {
		return false
	}
	return in.RecordType == "A" && a.Is4() || in.RecordType == "AAAA" && a.Is6()
}

func verifiedDNSApplication(rows []domain.ApplicationRevision, nodes []domain.Node, controls map[string]domain.NodeControl, app, expected string, now time.Time) (domain.ApplicationRevision, int64, error) {
	var latest domain.ApplicationRevision
	for _, v := range rows {
		if v.ApplicationID == app && v.Number > latest.Number {
			latest = v
		}
	}
	if latest.ID == "" {
		return latest, 0, domain.ErrNotFound
	}
	if latest.ID != expected || latest.Status != "succeeded" || latest.Deleted || latest.Spec.Disabled || len(nodes) == 0 {
		return latest, 0, domain.ErrConflict
	}
	for _, h := range domain.ApplicationHealthAt(rows, nodes, controls, now) {
		if h.ApplicationID != app {
			continue
		}
		if h.Configuration != "loaded" || h.Reachability != "reachable" {
			break
		}
		verified := now.UnixMilli()
		for _, n := range h.Nodes {
			if !n.Fresh || !n.Loaded || n.SpecDigest != latest.Spec.Digest() || len(n.Probes) == 0 {
				return latest, 0, domain.ErrConflict
			}
			verified = min(verified, n.ObservedAtMS)
			for _, p := range n.Probes {
				verified = min(verified, p.ObservedAtMS)
			}
		}
		return latest, verified, nil
	}
	return latest, 0, domain.ErrConflict
}

func dnsHostname(value string) bool {
	if len(value) == 0 || len(value) > 253 || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	_, err := netip.ParseAddr(value)
	return err != nil
}

// Only the challenge owner and the target granted on all nodes can be signed.
// Phoenix then prepares an ordinary DNS working draft; no provider write occurs.
func (s *Server) applicationDNSDelegation(w http.ResponseWriter, r *http.Request, org string) {
	actor := domain.Actor(r.Context())
	if !domain.ValidID(actor) || (len(s.config.Browser.Secret) < 32 && !s.isConnectorRequest(r)) {
		fail(w, 403, "DNS delegation requires a Converge management login")
		return
	}
	var in struct {
		RequestID        string `json:"request_id"`
		RevisionID       string `json:"revision_id"`
		ZoneID           string `json:"zone_id"`
		WorkingVersionID string `json:"working_version_id"`
		Issuer           string `json:"issuer"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !domain.ValidID(in.RequestID) || !domain.ValidID(in.RevisionID) || !domain.ValidID(in.ZoneID) || !domain.ValidID(in.WorkingVersionID) || !domain.ValidRuntimeName(in.Issuer) {
		fail(w, 400, "provide a local issuer, zone and current versions")
		return
	}
	rows, nodes, controls, err := s.applicationSnapshot(r.Context(), org, r.PathValue("cluster"))
	if err != nil {
		storeError(w, err)
		return
	}
	now := time.Now()
	revision, verifiedAt, err := verifiedDNSApplication(rows, nodes, controls, r.PathValue("application"), in.RevisionID, now)
	if err != nil {
		storeError(w, err)
		return
	}
	options, err := s.acmeOptions(r.Context(), org, r.PathValue("cluster"))
	if err != nil {
		storeError(w, err)
		return
	}
	target := ""
	for _, option := range options {
		if option.Issuer == in.Issuer && option.Binding.Name == revision.Spec.Listener {
			for _, c := range option.Challenges {
				if c.Hostname == revision.Spec.Hostname {
					target = c.Target
				}
			}
		}
	}
	if target == "" {
		fail(w, 409, "grant the same DNS-01 delegation on every node before preparing DNS")
		return
	}
	s.writeDNSIntent(w, r, map[string]any{"v": 1, "iss": s.config.Browser.Audience, "aud": "converge-dns", "sub": actor, "org": org, "request_id": in.RequestID, "cluster_id": r.PathValue("cluster"), "application_id": revision.ApplicationID, "revision_id": revision.ID, "revision_number": revision.Number, "hostname": "_acme-challenge." + revision.Spec.Hostname, "zone_id": in.ZoneID, "working_version_id": in.WorkingVersionID, "record_type": "CNAME", "value": target, "ttl": 300, "verified_at_ms": verifiedAt, "iat": now.Unix(), "exp": now.Unix() + 60})
}
