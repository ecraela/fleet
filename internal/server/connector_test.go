package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/connectwire"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

func TestConnectorDNSEvidenceKeepsNormalCloudScope(t *testing.T) {
	f := browserFixture(t)
	localOrg := domain.NewID()
	user := domain.NewID()
	identity := BrowserIdentity{UserID: user, OrganizationID: f.org, Capabilities: []string{"haproxy.read", "haproxy.manage"}, ExpiresAt: time.Now().Add(time.Minute)}
	claims := map[string]any{"org": localOrg, "sub": user, "aud": "converge-dns", "iss": "local-manager", "v": 1, "hostname": "shop.example.test", "request_id": domain.NewID()}
	r := httptest.NewRequest("POST", "/v1/clusters/"+domain.NewID()+"/applications/"+domain.NewID()+"/dns-intent", nil)
	for _, bad := range []string{"org", "sub", "aud"} {
		copy := map[string]any{}
		for k, v := range claims {
			copy[k] = v
		}
		copy[bad] = "foreign"
		body, _ := json.Marshal(map[string]any{"connector_dns_claims": copy})
		w := httptest.NewRecorder()
		f.server.relayDNSIntent(w, r, identity, localOrg, body)
		if w.Code != 502 {
			t.Fatal("signed foreign DNS evidence", bad)
		}
	}
	body, _ := json.Marshal(map[string]any{"connector_dns_claims": claims})
	w := httptest.NewRecorder()
	f.server.relayDNSIntent(w, r, identity, localOrg, body)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var result struct {
		Intent string `json:"intent"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	parts := strings.Split(result.Intent, ".")
	if len(parts) != 2 {
		t.Fatal("missing signed intent")
	}
	mac := hmac.New(sha256.New, f.server.config.Browser.Secret)
	mac.Write([]byte(dnsIntentPurpose + parts[0]))
	sig, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !hmac.Equal(sig, mac.Sum(nil)) {
		t.Fatal("wrong cloud signing authority")
	}
	encoded, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var signed map[string]any
	json.Unmarshal(encoded, &signed)
	if signed["org"] != f.org || signed["sub"] != user || signed["aud"] != "converge-dns" || signed["iss"] != f.server.config.Browser.Audience {
		t.Fatal("cloud DNS identity not bound")
	}
}

func cloudRequest(t *testing.T, s *Server, cookie *http.Cookie, csrf, org, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Origin", testBrowserOrigin)
	r.Header.Set("X-Organization-ID", org)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func cloudLogin(t *testing.T, f *fixture, manage bool) (*http.Cookie, string) {
	p, n := challenge(t, f.server)
	claims := validClaims(f, n)
	if manage {
		claims.Capabilities = append(claims.Capabilities, "haproxy.manage")
	}
	w := exchange(t, f, p, claims)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var v struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &v)
	return sessionCookie(t, w), v.CSRF
}
func waitConnector(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("connector did not reach expected state")
}
func TestOutboundConnectorPairingTenancyPermissionsAndRevocation(t *testing.T) {
	local, user := localFixture(t)
	cookie, localCSRF := localLoginForTest(t, local.server, user.Username)
	cloudStore := store.NewMemory()
	cloudOrg := domain.NewID()
	relay, err := New(Config{Role: "relay", Store: cloudStore, Authority: local.authority, Browser: BrowserConfig{Origin: testBrowserOrigin, Secret: bytes.Repeat([]byte{43}, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	cloud := &fixture{store: cloudStore, server: relay, org: cloudOrg}
	ts := httptest.NewUnstartedServer(relay)
	ts.EnableHTTP2 = true
	ts.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	ts.StartTLS()
	defer ts.Close()
	ca := filepath.Join(t.TempDir(), "relay-ca.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	local.server.config.Connector = ConnectorConfig{RelayURL: ts.URL, CAFile: ca, StateDir: t.TempDir()}
	if err := os.Chmod(local.server.config.Connector.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := local.server.configureConnector(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go local.server.RunConnector(ctx)
	admin, csrf := cloudLogin(t, cloud, true)
	viewer, viewCSRF := cloudLogin(t, cloud, false)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		return cloudRequest(t, relay, admin, csrf, cloudOrg, method, path, body)
	}
	if w := cloudRequest(t, relay, viewer, viewCSRF, cloudOrg, "POST", "/v1/connector", `{"name":"denied"}`); w.Code != 403 {
		t.Fatal("viewer created link", w.Code)
	}
	if w := cloudRequest(t, relay, admin, csrf, local.org, "GET", "/v1/connector", ""); w.Code != 401 {
		t.Fatal("cross tenant link access", w.Code)
	}
	if w := request("POST", "/v1/connector", `{"name":"Local installation"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	} else {
		var result struct {
			Code string `json:"code"`
		}
		json.Unmarshal(w.Body.Bytes(), &result)
		body, _ := json.Marshal(map[string]any{"code": result.Code, "allow_manage": true})
		if w := localRequest(t, local.server, "POST", "/v1/local/connector", string(body), localTestOrigin, "", cookie); w.Code != 403 {
			t.Fatal("link without csrf", w.Code)
		}
		if w := localRequest(t, local.server, "POST", "/v1/local/connector", string(body), localTestOrigin, localCSRF, cookie); w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	waitConnector(t, func() bool { relay.relay.mu.Lock(); defer relay.relay.mu.Unlock(); return len(relay.relay.peers) == 1 })
	if w := request("GET", "/v1/clusters", ""); w.Code != 200 || !strings.Contains(w.Body.String(), cloudOrg) || strings.Contains(w.Body.String(), local.org) {
		t.Fatal("tenant mapping failed", w.Code, w.Body.String())
	}
	if w := request("POST", "/v1/clusters", `{"name":"Created through outbound connection"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/v1/local/session", "/v1/local/connector", "/v1/enroll", "/v1/identity/renew", "/v1/metrics/write", "/v3/services/haproxy/configuration/raw"} {
		if w := request("POST", path, `{}`); w.Code != 404 {
			t.Fatal("forbidden tunnel surface", path, w.Code)
		}
	}
	if w := cloudRequest(t, relay, viewer, viewCSRF, cloudOrg, "POST", "/v1/clusters", `{"name":"denied"}`); w.Code != 403 {
		t.Fatal("viewer wrote through link", w.Code)
	}
	// Credential revocation closes remote requests without touching local sessions.
	if w := request("DELETE", "/v1/connector", ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	waitConnector(t, func() bool { relay.relay.mu.Lock(); defer relay.relay.mu.Unlock(); return len(relay.relay.peers) == 0 })
	if w := request("GET", "/v1/clusters", ""); w.Code != 503 {
		t.Fatal("revoked link still read data", w.Code)
	}
	if w := localRequest(t, local.server, "GET", "/v1/clusters", "", localTestOrigin, "", cookie); w.Code != 200 {
		t.Fatal("cloud revocation broke local login", w.Code)
	}
	if w := localRequest(t, local.server, "DELETE", "/v1/local/connector", "", localTestOrigin, localCSRF, cookie); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if _, e := os.Stat(filepath.Join(local.server.config.Connector.StateDir, "connector.json")); !os.IsNotExist(e) {
		t.Fatal("credential retained after unlink")
	}
}
func TestConnectorRequestPolicyAndStructuredStreaming(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	ctx := t.Context()
	state := &connectorState{LocalOrganizationID: f.org, CloudOrganizationID: domain.NewID(), AllowManage: false}
	c := &connectorClient{server: f.server, state: state}
	for _, test := range []struct {
		method, path string
		caps         []string
		status       int
	}{{"GET", "/v1/clusters", []string{"haproxy.read"}, 200}, {"POST", "/v1/clusters", []string{"haproxy.read", "haproxy.manage"}, 403}, {"GET", "/v1/local/session", []string{"haproxy.read"}, 403}, {"GET", "/v1/clusters", []string{"dns.manage"}, 403}} {
		send := make(chan connectwire.Frame, 32)
		c.execute(ctx, state, connectwire.Frame{ID: domain.NewID(), Request: &connectwire.Request{Method: test.method, Path: test.path, Body: []byte(`{"name":"denied"}`), Identity: connectwire.Identity{UserID: domain.NewID(), OrganizationID: state.CloudOrganizationID, Capabilities: test.caps, ExpiresAt: time.Now().Add(time.Minute)}}}, send)
		first := <-send
		if first.Status != test.status {
			t.Fatal(test.path, test.method, first.Status)
		}
	}
	b := []byte(`{"organization_id":"` + f.org + `","text":"` + f.org + `","counter":18446744073709551615}`)
	mapped := mapConnectorJSON(b, f.org, state.CloudOrganizationID)
	if !strings.Contains(string(mapped), `"text":"`+f.org+`"`) || !strings.Contains(string(mapped), "18446744073709551615") || !strings.Contains(string(mapped), `"organization_id":"`+state.CloudOrganizationID+`"`) {
		t.Fatal("JSON translation changed data")
	}
	send := make(chan connectwire.Frame, 8)
	w := &connectorWriter{ctx: ctx, id: domain.NewID(), send: send, header: http.Header{}, localOrg: f.org, cloudOrg: state.CloudOrganizationID}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, "id: 1\nevent: metrics\ndata: ")
	w.Write(b)
	io.WriteString(w, "\n\n")
	w.Flush()
	<-send
	data := <-send
	if !strings.Contains(string(data.Data), state.CloudOrganizationID) {
		t.Fatal("SSE not mapped on flush")
	}
}
