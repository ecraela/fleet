package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

const testBrowserOrigin = "https://console.example.test"

func browserFixture(t *testing.T) *fixture {
	f := newFixture(t, "manager", "http://metrics.invalid")
	f.server.config.Browser = BrowserConfig{Secret: bytes.Repeat([]byte{42}, 32), Origin: testBrowserOrigin}
	if err := f.server.configureBrowser(); err != nil {
		t.Fatal(err)
	}
	f.server.registerBrowserRoutes()
	return f
}

func browserRequest(t *testing.T, s *Server, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Origin", testBrowserOrigin)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func challenge(t *testing.T, s *Server) (*http.Cookie, string) {
	t.Helper()
	w := browserRequest(t, s, "POST", "/v1/browser/challenge", "")
	if w.Code != 200 {
		t.Fatalf("challenge failed %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Nonce string `json:"nonce"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == browserPendingCookie {
			return cookie, body.Nonce
		}
	}
	t.Fatal("no pending cookie")
	return nil, ""
}

func validClaims(f *fixture, nonce string) browserClaims {
	now := time.Now().Unix()
	return browserClaims{Version: 1, Issuer: "converge", Audience: "haproxy-manager", UserID: domain.NewID(), OrganizationID: f.org, Nonce: nonce, JTI: domain.NewID(), IssuedAt: now, ExpiresAt: now + 30, Capabilities: []string{"haproxy.read"}, SessionKind: "global"}
}

func signedGrant(t *testing.T, s *Server, claims browserClaims) string {
	t.Helper()
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, s.config.Browser.Secret)
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func exchange(t *testing.T, f *fixture, pending *http.Cookie, claims browserClaims, old ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"grant": signedGrant(t, f.server, claims)})
	return browserRequest(t, f.server, "POST", "/v1/browser/session", string(body), append([]*http.Cookie{pending}, old...)...)
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == browserSessionCookie {
			return cookie
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestBrowserChallengeSessionRefreshAndLogout(t *testing.T) {
	f := browserFixture(t)
	pending, nonce := challenge(t, f.server)
	if !pending.Secure || !pending.HttpOnly || pending.SameSite != http.SameSiteStrictMode || pending.Domain != "" || pending.Path != "/api/haproxy" {
		t.Fatal("challenge cookie weakened defaults")
	}
	claims := validClaims(f, nonce)
	w := exchange(t, f, pending, claims)
	if w.Code != 200 {
		t.Fatalf("exchange %d %s", w.Code, w.Body.String())
	}
	cookie := sessionCookie(t, w)
	if !cookie.Secure || !cookie.HttpOnly || cookie.MaxAge > 60 || cookie.Path != "/api/haproxy" {
		t.Fatal("invalid session cookie")
	}
	var body struct {
		CSRF           string    `json:"csrf_token"`
		OrganizationID string    `json:"organization_id"`
		UserID         string    `json:"user_id"`
		ExpiresAt      time.Time `json:"expires_at"`
	}
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.CSRF == "" || body.OrganizationID != f.org || body.UserID != claims.UserID || body.ExpiresAt.After(time.Unix(claims.IssuedAt+60, 0)) {
		t.Fatal("session response lost bound identity or lifetime")
	}
	get := browserRequest(t, f.server, "GET", "/v1/browser/session", "", cookie)
	if get.Code != 200 || !strings.Contains(get.Body.String(), body.CSRF) {
		t.Fatal("session or derived CSRF was not restored")
	}
	if replay := exchange(t, f, pending, claims); replay.Code != 401 {
		t.Fatal("accepted replayed one-use handoff")
	}
	secondPending, secondNonce := challenge(t, f.server)
	secondClaims := validClaims(f, secondNonce)
	secondClaims.UserID = claims.UserID
	refreshed := exchange(t, f, secondPending, secondClaims, cookie)
	if refreshed.Code != 200 {
		t.Fatal(refreshed.Body.String())
	}
	next := sessionCookie(t, refreshed)
	if old := browserRequest(t, f.server, "GET", "/v1/browser/session", "", cookie); old.Code != 401 {
		t.Fatal("refresh retained old browser session")
	}
	var refreshBody struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(refreshed.Body.Bytes(), &refreshBody)
	r := httptest.NewRequest("DELETE", "/v1/browser/session", nil)
	r.AddCookie(next)
	r.Header.Set("Origin", testBrowserOrigin)
	r.Header.Set("X-CSRF-Token", refreshBody.CSRF)
	deleted := httptest.NewRecorder()
	f.server.ServeHTTP(deleted, r)
	if deleted.Code != 204 {
		t.Fatal("logout failed")
	}
	if got := browserRequest(t, f.server, "GET", "/v1/browser/session", "", next); got.Code != 401 {
		t.Fatal("logout did not revoke persisted session")
	}
}

func TestBrowserRejectsSignatureAudienceNonceAndClaims(t *testing.T) {
	for name, change := range map[string]func(*browserClaims){
		"audience": func(c *browserClaims) { c.Audience = "different-product" }, "issuer": func(c *browserClaims) { c.Issuer = "other" }, "version": func(c *browserClaims) { c.Version = 2 },
		"subject": func(c *browserClaims) { c.UserID = "not-a-user" }, "organization": func(c *browserClaims) { c.OrganizationID = "not-an-org" },
		"nonce": func(c *browserClaims) { c.Nonce = strings.Repeat("0", 64) }, "future": func(c *browserClaims) { c.IssuedAt = time.Now().Unix() + 6; c.ExpiresAt = c.IssuedAt + 30 },
		"expired": func(c *browserClaims) { c.IssuedAt = time.Now().Unix() - 36; c.ExpiresAt = c.IssuedAt + 30 }, "long TTL": func(c *browserClaims) { c.ExpiresAt = c.IssuedAt + 300 },
		"capability": func(c *browserClaims) { c.Capabilities = []string{"haproxy.read", "owner"} }, "duplicate capability": func(c *browserClaims) { c.Capabilities = []string{"haproxy.read", "haproxy.read"} },
		"missing read": func(c *browserClaims) { c.Capabilities = []string{"haproxy.manage"} }, "session kind": func(c *browserClaims) { c.SessionKind = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			f := browserFixture(t)
			pending, nonce := challenge(t, f.server)
			claims := validClaims(f, nonce)
			change(&claims)
			if got := exchange(t, f, pending, claims); got.Code != 401 {
				t.Fatalf("accepted invalid handoff %s: %d", name, got.Code)
			}
		})
	}
	f := browserFixture(t)
	pending, nonce := challenge(t, f.server)
	grant := signedGrant(t, f.server, validClaims(f, nonce))
	parts := strings.Split(grant, ".")
	parts[1] = strings.Repeat("A", 43)
	body, _ := json.Marshal(map[string]string{"grant": strings.Join(parts, ".")})
	if got := browserRequest(t, f.server, "POST", "/v1/browser/session", string(body), pending); got.Code != 401 {
		t.Fatal("accepted invalid signature")
	}
	other, _ := challenge(t, f.server)
	if got := exchange(t, f, other, validClaims(f, nonce)); got.Code != 401 {
		t.Fatal("accepted handoff in a different challenged browser")
	}
}

func TestBrowserOriginCSRFAndTenantGuards(t *testing.T) {
	f := browserFixture(t)
	r := httptest.NewRequest("POST", "/v1/browser/challenge", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != 403 || len(w.Result().Cookies()) != 0 {
		t.Fatal("cross-origin challenge accepted")
	}
	pending, nonce := challenge(t, f.server)
	claims := validClaims(f, nonce)
	created := exchange(t, f, pending, claims)
	cookie := sessionCookie(t, created)
	identityReq := httptest.NewRequest("GET", "/v1/nodes", nil)
	identityReq.AddCookie(cookie)
	identity, ok := f.server.browserIdentity(identityReq)
	if !ok || identity.OrganizationID != f.org || !identity.Has("haproxy.read") || identity.Has("haproxy.manage") {
		t.Fatal("incorrect browser identity capabilities")
	}
	identityReq.Header.Set("X-Organization-ID", domain.NewID())
	if _, ok := f.server.browserIdentity(identityReq); ok {
		t.Fatal("client organization header changed browser scope")
	}
	for name, tc := range rangeCases() {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("DELETE", "/v1/browser/session", nil)
			req.AddCookie(cookie)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-CSRF-Token", tc.csrf)
			if f.server.validateBrowserCSRF(req) {
				t.Fatal("accepted missing or wrong CSRF/origin")
			}
		})
	}
	duplicate := httptest.NewRequest("GET", "/v1/browser/session", nil)
	duplicate.AddCookie(cookie)
	duplicate.AddCookie(cookie)
	if _, ok := f.server.browserIdentity(duplicate); ok {
		t.Fatal("ambiguous duplicate browser cookies accepted")
	}
}

func rangeCases() map[string]struct{ origin, csrf string } {
	return map[string]struct{ origin, csrf string }{"no csrf": {testBrowserOrigin, ""}, "bad csrf": {testBrowserOrigin, "incorrect"}, "bad origin": {"https://evil.example", "incorrect"}, "no origin": {"", "incorrect"}}
}

type expiredBrowserStore struct{ *store.Memory }

func (s expiredBrowserStore) GetBrowserSession(ctx context.Context, hash string, now time.Time) (domain.BrowserSession, error) {
	return s.Memory.GetBrowserSession(ctx, hash, now.Add(time.Minute))
}

func TestBrowserSessionRechecksPersistentExpiry(t *testing.T) {
	f := browserFixture(t)
	pending, nonce := challenge(t, f.server)
	cookie := sessionCookie(t, exchange(t, f, pending, validClaims(f, nonce)))
	f.server.config.Store = expiredBrowserStore{f.store}
	if got := browserRequest(t, f.server, "GET", "/v1/browser/session", "", cookie); got.Code != 401 {
		t.Fatal("expired persisted identity still authenticated")
	}
}

func TestBrowserConfigDisabledAndSecureByDefault(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	if got := browserRequest(t, f.server, "POST", "/v1/browser/challenge", ""); got.Code != 404 {
		t.Fatal("standalone enabled cloud login implicitly")
	}
	for name, cfg := range map[string]BrowserConfig{"short secret": {Secret: []byte("short"), Origin: testBrowserOrigin}, "missing origin": {Secret: bytes.Repeat([]byte{1}, 32)}, "HTTP default": {Secret: bytes.Repeat([]byte{1}, 32), Origin: "http://localhost:5173"}, "public insecure": {Secret: bytes.Repeat([]byte{1}, 32), Origin: "https://example.com", InsecureCookies: true}} {
		t.Run(name, func(t *testing.T) {
			f.server.config.Browser = cfg
			if f.server.configureBrowser() == nil {
				t.Fatal("invalid browser security configuration accepted")
			}
		})
	}
	f.server.config.Browser = BrowserConfig{Secret: bytes.Repeat([]byte{1}, 32), Origin: "http://localhost:5173", InsecureCookies: true}
	if err := f.server.configureBrowser(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	f.server.browserCookie(w, browserSessionCookie, secret(), time.Now().Add(time.Minute))
	if w.Result().Cookies()[0].Secure {
		t.Fatal("explicit loopback development setting was ignored")
	}
}

func TestBrowserManageCapabilityAndCSRFOnActualRoute(t *testing.T) {
	f := browserFixture(t)
	pending, nonce := challenge(t, f.server)
	readClaims := validClaims(f, nonce)
	created := exchange(t, f, pending, readClaims)
	readCookie := sessionCookie(t, created)
	var response struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(created.Body.Bytes(), &response)
	request := func(cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/clusters", strings.NewReader(`{"name":"browser pair"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", testBrowserOrigin)
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		return w
	}
	if got := request(readCookie, response.CSRF); got.Code != 403 {
		t.Fatalf("read-only browser mutated cluster: %d", got.Code)
	}
	pending, nonce = challenge(t, f.server)
	manageClaims := validClaims(f, nonce)
	manageClaims.Capabilities = []string{"haproxy.read", "haproxy.manage"}
	created = exchange(t, f, pending, manageClaims, readCookie)
	manageCookie := sessionCookie(t, created)
	json.Unmarshal(created.Body.Bytes(), &response)
	if got := request(manageCookie, ""); got.Code != 403 {
		t.Fatalf("browser mutation accepted without CSRF: %d", got.Code)
	}
	if got := request(manageCookie, response.CSRF); got.Code != 201 {
		t.Fatalf("authorized browser mutation failed: %d %s", got.Code, got.Body.String())
	}
}
