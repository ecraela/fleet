package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

type dnsSnapshotStore struct {
	*store.Memory
	rows     []domain.ApplicationRevision
	nodes    []domain.Node
	controls map[string]domain.NodeControl
}

func (d *dnsSnapshotStore) ListApplications(ctx context.Context, org, cluster string) ([]domain.ApplicationRevision, error) {
	if _, err := d.GetCluster(ctx, org, cluster); err != nil {
		return nil, err
	}
	return d.rows, nil
}
func (d *dnsSnapshotStore) ListNodes(ctx context.Context, org string) ([]domain.Node, error) {
	return d.nodes, nil
}
func (d *dnsSnapshotStore) GetControl(ctx context.Context, org, node string) (domain.NodeControl, []domain.Operation, error) {
	return d.controls[node], nil, nil
}

func dnsIntentFixture(t *testing.T) (*fixture, *dnsSnapshotStore, domain.ApplicationRevision) {
	f := browserFixture(t)
	now := time.Now()
	a := domain.HTTPApplication{ID: domain.NewID(), Name: "Store", Hostname: "apps.example.test", Listener: "public", PathPrefix: "/shop", HealthPath: "/healthz", Algorithm: "roundrobin", Servers: []domain.ConfigurationServer{{Name: "web", Address: "app", Port: 8080, Weight: 100}}}
	revision := domain.ApplicationRevision{ID: domain.NewID(), OrganizationID: f.org, ClusterID: f.cluster.ID, ApplicationID: a.ID, Number: 1, Status: "succeeded", Spec: a}
	d := &dnsSnapshotStore{Memory: f.store, rows: []domain.ApplicationRevision{revision}, controls: map[string]domain.NodeControl{}}
	for i := 0; i < 2; i++ {
		n := domain.Node{ID: domain.NewID(), OrganizationID: f.org, ClusterID: f.cluster.ID, Name: "node"}
		d.nodes = append(d.nodes, n)
		d.controls[n.ID] = domain.NodeControl{SessionID: domain.NewID(), LastSeen: now, Discovery: &domain.Discovery{ObservedAtMS: now.UnixMilli(),
			Configuration:     &domain.ConfigurationState{Version: 1, ArtifactID: strings.Repeat("a", 64), ObservedAtMS: now.UnixMilli(), Document: &domain.ConfigurationDocument{Backends: []domain.ConfigurationBackend{{Name: "fallback", Algorithm: "roundrobin", Servers: a.Servers}}, Applications: []domain.HTTPApplication{a}}},
			Proxies:           []domain.Proxy{{Name: a.BackendName(), Kind: "backend", Mode: "http", Algorithm: "roundrobin"}},
			Servers:           []domain.BackendServer{{Backend: a.BackendName(), Name: "web", Weight: 100, InitialWeight: 100, State: "ready", Health: "UP"}},
			ApplicationProbes: []domain.ApplicationProbe{{ApplicationID: a.ID, SpecDigest: a.Digest(), Name: "local", Scheme: "http", Path: "/shop", ExpectedStatus: 200, StatusCode: 200, ObservedAtMS: now.UnixMilli()}},
		}}
	}
	f.server.config.Store = d
	return f, d, revision
}
func TestDNSIntentRequiresExactFreshReadinessAndTenantManagement(t *testing.T) {
	f, d, revision := dnsIntentFixture(t)
	now := time.UnixMilli(d.controls[d.nodes[0].ID].Discovery.ObservedAtMS)
	a := revision.Spec
	input := dnsIntentInput{RequestID: domain.NewID(), RevisionID: revision.ID, ZoneID: domain.NewID(), WorkingVersionID: domain.NewID(), RecordType: "A", Value: "192.0.2.10", TTL: 300}
	path := "/v1/clusters/" + f.cluster.ID + "/applications/" + a.ID + "/dns-intent"
	if got := f.request("POST", path, input, true); got.Code != 403 {
		t.Fatal("installation token acquired Converge intent", got.Code)
	}
	for _, manage := range []bool{false, true} {
		pending, nonce := challenge(t, f.server)
		claims := validClaims(f, nonce)
		if manage {
			claims.Capabilities = append(claims.Capabilities, "haproxy.manage")
		}
		created := exchange(t, f, pending, claims)
		cookie := sessionCookie(t, created)
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		_ = json.Unmarshal(created.Body.Bytes(), &session)
		call := func(target string, csrf string) *httptest.ResponseRecorder {
			raw, _ := json.Marshal(input)
			r := httptest.NewRequest("POST", target, strings.NewReader(string(raw)))
			r.AddCookie(cookie)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", testBrowserOrigin)
			r.Header.Set("X-CSRF-Token", csrf)
			w := httptest.NewRecorder()
			f.server.ServeHTTP(w, r)
			return w
		}
		if got := call(path, ""); got.Code != 403 {
			t.Fatal("missing CSRF accepted")
		}
		got := call(path, session.CSRF)
		if !manage {
			if got.Code != 403 {
				t.Fatal("viewer acquired intent")
			}
			continue
		}
		if got.Code != 200 {
			t.Fatal(got.Code, got.Body.String())
		}
		var body struct {
			Intent string `json:"intent"`
		}
		_ = json.Unmarshal(got.Body.Bytes(), &body)
		parts := strings.Split(body.Intent, ".")
		raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
		var c map[string]any
		_ = json.Unmarshal(raw, &c)
		mac := hmac.New(sha256.New, f.server.config.Browser.Secret)
		mac.Write([]byte(dnsIntentPurpose + parts[0]))
		if parts[1] != base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) || c["sub"] != claims.UserID || c["org"] != f.org || c["revision_id"] != revision.ID || c["exp"].(float64)-c["iat"].(float64) != 60 {
			t.Fatal("intent identity or signature changed")
		}
		foreign, _ := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
		if got := call(strings.Replace(path, f.cluster.ID, foreign.ID, 1), session.CSRF); got.Code != 404 {
			t.Fatal("foreign cluster", got.Code)
		}
		if got := call(strings.Replace(path, a.ID, domain.NewID(), 1), session.CSRF); got.Code != 404 {
			t.Fatal("foreign app", got.Code)
		}
		input.RevisionID = domain.NewID()
		if got := call(path, session.CSRF); got.Code != 409 {
			t.Fatal("stale revision accepted")
		}
		input.RevisionID = revision.ID
		for _, failure := range []string{"disconnected", "stale", "failed", "digest", "running", "empty"} {
			c := d.controls[d.nodes[1].ID]
			old := *c.Discovery
			next := old
			next.ApplicationProbes = append([]domain.ApplicationProbe{}, old.ApplicationProbes...)
			switch failure {
			case "disconnected":
				c.LastSeen = now.Add(-7 * time.Second)
			case "stale":
				next.ApplicationProbes[0].ObservedAtMS = now.Add(-16 * time.Second).UnixMilli()
			case "failed":
				next.ApplicationProbes[0].ErrorCode = "response_status"
				next.ApplicationProbes[0].StatusCode = 503
			case "digest":
				next.ApplicationProbes[0].SpecDigest = strings.Repeat("b", 64)
			case "running":
				d.rows[0].Status = "running"
			case "empty":
				next.ApplicationProbes = nil
			}
			c.Discovery = &next
			d.controls[d.nodes[1].ID] = c
			if got := call(path, session.CSRF); got.Code != 409 {
				t.Fatal("unverified application accepted", failure, got.Code)
			}
			c.LastSeen = now
			c.Discovery = &old
			d.controls[d.nodes[1].ID] = c
			d.rows[0].Status = "succeeded"
		}
	}
}

func TestDNSIntentTargetsAreBounded(t *testing.T) {
	in := dnsIntentInput{RequestID: domain.NewID(), RevisionID: domain.NewID(), ZoneID: domain.NewID(), WorkingVersionID: domain.NewID(), RecordType: "AAAA", Value: "2001:db8::1", TTL: 300}
	if !validDNSIntentInput(in) {
		t.Fatal("valid IPv6 refused")
	}
	for _, v := range []string{"::ffff:192.0.2.1", "fe80::1%eth0", "2001:DB8::1", "http://example.test", "2001:db8::1/64"} {
		in.Value = v
		if validDNSIntentInput(in) {
			t.Fatal("unsafe target", v)
		}
	}
	in.RecordType = "CNAME"
	in.Value = "entry.example.test"
	if !validDNSIntentInput(in) {
		t.Fatal("valid CNAME refused")
	}
	for _, v := range []string{"*.example.test", "entry.example.test.", "entry.example.test/path", "-bad.test", "192.0.2.1"} {
		in.Value = v
		if validDNSIntentInput(in) {
			t.Fatal("invalid CNAME", v)
		}
	}
}

func TestDNSDelegationSignsOnlyNodeGrantedTargetAndKeepsTenantBoundary(t *testing.T) {
	f, d, revision := dnsIntentFixture(t)
	binding := domain.ListenerBinding{Name: "public", Address: "*", Port: 8443}
	for id, control := range d.controls {
		c := control.Discovery.Configuration
		c.NodeID = id
		c.ListenerBindings = []domain.ListenerBinding{binding}
		c.ACMEGrants = []domain.ACMEGrant{{Issuer: "local-ca", Listener: "public", Environment: "staging", PolicyDigest: strings.Repeat("c", 64), Challenges: []domain.ACMEChallenge{{Hostname: revision.Spec.Hostname, Target: "app.validation.test"}}}}
	}
	pending, nonce := challenge(t, f.server)
	claims := validClaims(f, nonce)
	claims.Capabilities = append(claims.Capabilities, "haproxy.manage")
	created := exchange(t, f, pending, claims)
	cookie := sessionCookie(t, created)
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &session)
	input := map[string]any{"request_id": domain.NewID(), "revision_id": revision.ID, "zone_id": domain.NewID(), "working_version_id": domain.NewID(), "issuer": "local-ca"}
	path := "/v1/clusters/" + f.cluster.ID + "/applications/" + revision.ApplicationID + "/dns-delegation"
	call := func(path string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", testBrowserOrigin)
		r.Header.Set("X-CSRF-Token", session.CSRF)
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		return w
	}
	got := call(path)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	var payload struct {
		Intent string `json:"intent"`
	}
	_ = json.Unmarshal(got.Body.Bytes(), &payload)
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(payload.Intent, ".")[0])
	var safe map[string]any
	_ = json.Unmarshal(raw, &safe)
	if safe["hostname"] != "_acme-challenge."+revision.Spec.Hostname || safe["record_type"] != "CNAME" || safe["value"] != "app.validation.test" {
		t.Fatal("unbounded delegation", safe)
	}
	foreign, _ := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
	if got = call(strings.Replace(path, f.cluster.ID, foreign.ID, 1)); got.Code != 404 {
		t.Fatal("foreign delegation", got.Code)
	}
	input["value"] = "attacker.test"
	if got = call(path); got.Code != 400 {
		t.Fatal("caller target accepted", got.Code)
	}
	delete(input, "value")
	c := d.controls[d.nodes[1].ID].Discovery.Configuration
	c.ACMEGrants[0].Challenges[0].Target = "different.validation.test"
	if got = call(path); got.Code != 409 {
		t.Fatal("mismatched node authority", got.Code)
	}
}
