package server

import (
	"context"
	"encoding/json"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/localauth"
	"github.com/ecraela/fleet/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const localTestOrigin = "https://standalone.example.test"

func localFixture(t *testing.T) (*fixture, domain.LocalUser) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	f.server.config.Local = LocalConfig{Origin: localTestOrigin}
	if err := f.server.configureLocal(); err != nil {
		t.Fatal(err)
	}
	hash, err := localauth.Hash("local synthetic password")
	if err != nil {
		t.Fatal(err)
	}
	u := domain.LocalUser{ID: domain.NewID(), Username: "local-admin", OrganizationID: f.org, Role: "administrator", PasswordHash: hash}
	if err = f.store.CreateLocalUser(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	return f, u
}
func localRequest(t *testing.T, s *Server, method, path, body, origin, csrf string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Origin", origin)
	r.Header.Set("X-HAProxy-Session", "local")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func localLoginForTest(t *testing.T, s *Server, name string) (*http.Cookie, string) {
	t.Helper()
	w := localRequest(t, s, "POST", "/api/haproxy/v1/local/session", `{"username":"`+name+`","password":"local synthetic password"}`, localTestOrigin, "")
	if w.Code != 200 {
		t.Fatalf("login %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	for _, c := range w.Result().Cookies() {
		if c.Name == localCookie {
			if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/api/haproxy" {
				t.Fatal("cookie policy")
			}
			return c, body.CSRF
		}
	}
	t.Fatal("missing local cookie")
	return nil, ""
}
func TestLocalLoginPermissionsTenantCSRFAndLogout(t *testing.T) {
	f, u := localFixture(t)
	cookie, csrf := localLoginForTest(t, f.server, "LOCAL-ADMIN")
	// The local cookie grants nothing to the optional cloud handoff identity.
	r := httptest.NewRequest("GET", "/v1/fleet", nil)
	r.AddCookie(cookie)
	if _, ok := f.server.browserIdentity(r); ok {
		t.Fatal("local cookie accepted as cloud handoff")
	}
	for _, bad := range []string{"", "https://evil.example.test"} {
		w := localRequest(t, f.server, "POST", "/v1/clusters", `{"name":"denied"}`, bad, csrf, cookie)
		if w.Code != 403 && w.Code != 401 {
			t.Fatalf("bad origin %q: %d", bad, w.Code)
		}
	}
	if w := localRequest(t, f.server, "POST", "/v1/clusters", `{"name":"denied"}`, localTestOrigin, "", cookie); w.Code != 403 {
		t.Fatal("missing csrf allowed")
	}
	if w := localRequest(t, f.server, "POST", "/v1/clusters", `{"name":"allowed"}`, localTestOrigin, csrf, cookie); w.Code != 201 {
		t.Fatalf("write %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/v1/fleet", nil)
	r.Header.Set("X-HAProxy-Session", "local")
	r.Header.Set("X-Organization-ID", domain.NewID())
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("cross-tenant header allowed")
	}
	other := domain.LocalUser{ID: domain.NewID(), Username: "other-viewer", OrganizationID: domain.NewID(), Role: "viewer", PasswordHash: u.PasswordHash}
	if err := f.store.CreateLocalUser(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	viewer, viewerCSRF := localLoginForTest(t, f.server, other.Username)
	if w = localRequest(t, f.server, "GET", "/v1/clusters", "", localTestOrigin, "", viewer); w.Code != 200 || strings.Contains(w.Body.String(), "allowed") {
		t.Fatal("cross-tenant data leak")
	}
	if w = localRequest(t, f.server, "GET", "/v1/clusters/"+f.cluster.ID+"/configuration", "", localTestOrigin, "", viewer); w.Code != 404 {
		t.Fatalf("cross-tenant cluster %d", w.Code)
	}
	if w = localRequest(t, f.server, "POST", "/v1/clusters", `{"name":"viewer write"}`, localTestOrigin, viewerCSRF, viewer); w.Code != 403 {
		t.Fatal("viewer wrote")
	}
	if w = localRequest(t, f.server, "GET", "/v1/fleet", "", localTestOrigin, "", cookie, cookie); w.Code != 401 {
		t.Fatal("duplicate cookie accepted")
	}
	r = httptest.NewRequest("GET", "/v1/fleet", nil)
	r.Header.Set("X-HAProxy-Session", "local")
	r.AddCookie(cookie)
	if !f.server.canReadOrganization(r, u.OrganizationID) {
		t.Fatal("stream authorization did not accept local user")
	}
	if w = localRequest(t, f.server, "DELETE", "/v1/local/session", "", localTestOrigin, csrf, cookie); w.Code != 204 {
		t.Fatal("logout failed")
	}
	if w = localRequest(t, f.server, "GET", "/v1/fleet", "", localTestOrigin, "", cookie); w.Code != 401 {
		t.Fatal("logout session remained valid")
	}
}
func TestLocalResetRevokesAllSessionsAndDisable(t *testing.T) {
	f, u := localFixture(t)
	a, csrf := localLoginForTest(t, f.server, u.Username)
	b, _ := localLoginForTest(t, f.server, u.Username)
	w := localRequest(t, f.server, "POST", "/v1/local/password", `{"current_password":"local synthetic password","new_password":"replacement synthetic password"}`, localTestOrigin, csrf, a)
	if w.Code != 204 {
		t.Fatalf("password update %d %s", w.Code, w.Body.String())
	}
	for _, c := range []*http.Cookie{a, b} {
		if w = localRequest(t, f.server, "GET", "/v1/fleet", "", localTestOrigin, "", c); w.Code != 401 {
			t.Fatal("password update retained session")
		}
	}
	if w = localRequest(t, f.server, "POST", "/v1/local/session", `{"username":"local-admin","password":"local synthetic password"}`, localTestOrigin, ""); w.Code != 401 {
		t.Fatal("old password accepted")
	}
	if err := f.store.DisableLocalUser(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}
	if w = localRequest(t, f.server, "POST", "/v1/local/session", `{"username":"local-admin","password":"replacement synthetic password"}`, localTestOrigin, ""); w.Code != 401 {
		t.Fatal("disabled user signed in")
	}
}

type localLoginClock struct {
	*store.Memory
	now time.Time
}

func (s *localLoginClock) AdmitLocalLogin(ctx context.Context, source, identity string, _ time.Time) (bool, error) {
	return s.Memory.AdmitLocalLogin(ctx, source, identity, s.now)
}

func TestLocalLoginRateLimitsAndIsolationFromOrigin(t *testing.T) {
	f, _ := localFixture(t)
	// Admission rejects stale caller time. Keep its test window ahead of the
	// real clock, then advance it explicitly instead of crossing a wall minute.
	clock := &localLoginClock{Memory: f.store, now: time.Now().Add(time.Hour).Truncate(time.Minute)}
	f.server.config.Store = clock
	body := `{"username":"missing-user","password":"some wrong password"}`
	if w := localRequest(t, f.server, "POST", "/v1/local/session", body, "https://evil.example.test", ""); w.Code != 403 {
		t.Fatal("login csrf")
	}
	for i := 0; i < 6; i++ {
		w := localRequest(t, f.server, "POST", "/v1/local/session", body, localTestOrigin, "")
		expected := 401
		if i == 5 {
			expected = 429
		}
		if w.Code != expected {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	clock.now = clock.now.Add(time.Minute)
	if w := localRequest(t, f.server, "POST", "/v1/local/session", body, localTestOrigin, ""); w.Code != 401 {
		t.Fatalf("new minute did not reset admission: %d", w.Code)
	}
}
func TestLocalOriginSecureDefaults(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	for _, c := range []LocalConfig{{Origin: "http://public.example"}, {Origin: "https://public.example", InsecureCookies: true}, {Origin: "https://example.test/path"}, {Origin: "http://localhost:5180"}} {
		f.server.config.Local = c
		if f.server.configureLocal() == nil {
			t.Fatal("unsafe local origin allowed")
		}
	}
	f.server.config.Local = LocalConfig{Origin: "http://localhost:5180", InsecureCookies: true}
	if err := f.server.configureLocal(); err != nil {
		t.Fatal(err)
	}
}
