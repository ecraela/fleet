package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

const browserSessionCookie = "converge_haproxy_session"
const browserPendingCookie = "converge_haproxy_pending"

type BrowserConfig struct {
	Secret          []byte
	Origin          string
	Issuer          string
	Audience        string
	CookiePath      string
	InsecureCookies bool
	limiter         *browserLimiter
}

type BrowserIdentity struct {
	UserID         string
	OrganizationID string
	Capabilities   []string
	ExpiresAt      time.Time
}

func (b BrowserIdentity) Has(capability string) bool {
	for _, cap := range b.Capabilities {
		if cap == capability {
			return true
		}
	}
	return false
}

type browserClaims struct {
	Version        int      `json:"v"`
	Issuer         string   `json:"iss"`
	Audience       string   `json:"aud"`
	UserID         string   `json:"sub"`
	OrganizationID string   `json:"org"`
	Nonce          string   `json:"nonce"`
	JTI            string   `json:"jti"`
	IssuedAt       int64    `json:"iat"`
	ExpiresAt      int64    `json:"exp"`
	Capabilities   []string `json:"capabilities"`
	SessionKind    string   `json:"session_kind"`
}

func (s *Server) configureBrowser() error {
	b := &s.config.Browser
	if len(b.Secret) == 0 && b.Origin == "" {
		return nil
	}
	if len(b.Secret) < 32 || b.Origin == "" || (s.config.Role != "manager" && s.config.Role != "relay") {
		return errors.New("browser login requires a dedicated secret of at least 32 bytes and an allowed origin on manager")
	}
	u, err := url.Parse(b.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.String() != b.Origin || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("browser origin must be an exact HTTP(S) origin without a path")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if (u.Scheme != "https" && !b.InsecureCookies) || (b.InsecureCookies && !loopback) {
		return errors.New("insecure browser cookies require an explicit loopback development origin")
	}
	if b.Issuer == "" {
		b.Issuer = "converge"
	}
	if b.Audience == "" {
		b.Audience = "haproxy-manager"
	}
	if b.CookiePath == "" {
		b.CookiePath = "/api/haproxy"
	}
	if len(b.Issuer) > 128 || len(b.Audience) > 128 || len(b.CookiePath) > 128 || !strings.HasPrefix(b.CookiePath, "/") || path.Clean(b.CookiePath) != b.CookiePath || strings.ContainsAny(b.CookiePath, ";,\r\n\t ") {
		return errors.New("invalid browser login configuration")
	}
	if _, ok := s.config.Store.(store.BrowserStore); !ok {
		return errors.New("browser login requires persistent browser session storage")
	}
	b.Secret = append([]byte(nil), b.Secret...)
	b.limiter = &browserLimiter{buckets: make(map[string]int), window: time.Now()}
	return nil
}

func (s *Server) registerBrowserRoutes() {
	if !s.browserEnabled() {
		return
	}
	s.mux.HandleFunc("POST /v1/browser/challenge", s.browserChallenge)
	s.mux.HandleFunc("POST /v1/browser/session", s.browserCreateSession)
	s.mux.HandleFunc("GET /v1/browser/session", s.browserGetSession)
	s.mux.HandleFunc("DELETE /v1/browser/session", s.browserDeleteSession)
}

func (s *Server) browserEnabled() bool {
	return len(s.config.Browser.Secret) >= 32 && s.config.Browser.Origin != ""
}

func (s *Server) browserOrigin(r *http.Request) bool {
	return s.browserEnabled() && r.Header.Get("Origin") == s.config.Browser.Origin
}

func (s *Server) browserChallenge(w http.ResponseWriter, r *http.Request) {
	if !s.browserOrigin(r) {
		fail(w, 403, "browser origin is not allowed")
		return
	}
	if !s.config.Browser.limiter.admit(r.RemoteAddr) {
		fail(w, 429, "browser login rate limit")
		return
	}
	value := secret() + "." + strconv.FormatInt(time.Now().Unix(), 10)
	value += "." + s.browserMAC("pending:"+value)
	s.browserCookie(w, browserPendingCookie, value, time.Now().Add(2*time.Minute))
	writeJSON(w, 200, map[string]string{"nonce": digest(value)})
}

func (s *Server) browserCreateSession(w http.ResponseWriter, r *http.Request) {
	if !s.browserOrigin(r) {
		fail(w, 403, "browser origin is not allowed")
		return
	}
	if !s.config.Browser.limiter.admit(r.RemoteAddr) {
		fail(w, 429, "browser login rate limit")
		return
	}
	var body struct {
		Grant string `json:"grant"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	claims, err := s.verifyBrowserGrant(body.Grant, time.Now())
	if err != nil {
		fail(w, 401, "invalid or expired browser handoff")
		return
	}
	pending, err := singleCookie(r, browserPendingCookie)
	if err != nil || !s.validBrowserPending(pending, time.Now()) || !hmac.Equal([]byte(claims.Nonce), []byte(digest(pending))) {
		fail(w, 401, "browser challenge does not match handoff")
		return
	}
	now := time.Now().UTC()
	expires := time.Unix(claims.IssuedAt, 0).Add(time.Minute).UTC()
	if maximum := now.Add(time.Minute); expires.After(maximum) {
		expires = maximum
	}
	value := secret()
	session := domain.BrowserSession{Digest: digest(value), UserID: claims.UserID, OrganizationID: claims.OrganizationID, Capabilities: claims.Capabilities, SessionKind: claims.SessionKind, CreatedAt: now, ExpiresAt: expires}
	replace := ""
	if old, err := singleCookie(r, browserSessionCookie); err == nil && validBrowserCredential(old) {
		replace = digest(old)
	}
	storage := s.config.Store.(store.BrowserStore)
	if err := storage.ConsumeBrowserGrant(r.Context(), claims.JTI, time.Unix(claims.ExpiresAt, 0).Add(5*time.Second), session, replace, now); err != nil {
		fail(w, 401, "browser handoff was consumed or expired")
		return
	}
	s.browserCookie(w, browserSessionCookie, value, expires)
	s.clearBrowserCookie(w, browserPendingCookie)
	s.browserSessionResponse(w, session, value)
}

func (s *Server) verifyBrowserGrant(grant string, now time.Time) (browserClaims, error) {
	invalid := errors.New("invalid browser grant")
	var claims browserClaims
	if len(grant) == 0 || len(grant) > 8192 {
		return claims, invalid
	}
	parts := strings.Split(grant, ".")
	if len(parts) != 2 {
		return claims, invalid
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return claims, invalid
	}
	mac := hmac.New(sha256.New, s.config.Browser.Secret)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, invalid
	}
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return claims, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&claims) != nil {
		return claims, invalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return claims, invalid
	}
	if claims.Version != 1 || claims.Issuer != s.config.Browser.Issuer || claims.Audience != s.config.Browser.Audience || !domain.ValidID(claims.UserID) || !domain.ValidID(claims.OrganizationID) || !domain.ValidID(claims.JTI) || !domain.ValidDigest(claims.Nonce) || claims.IssuedAt <= 0 || claims.IssuedAt > now.Unix()+5 || claims.IssuedAt < now.Unix()-35 || claims.ExpiresAt-claims.IssuedAt != 30 || now.Unix() >= claims.ExpiresAt+5 {
		return claims, invalid
	}
	if claims.SessionKind != "global" && claims.SessionKind != "organization" {
		return claims, invalid
	}
	read, manage := false, false
	for _, cap := range claims.Capabilities {
		switch cap {
		case "haproxy.read":
			if read {
				return claims, invalid
			}
			read = true
		case "haproxy.manage":
			if manage {
				return claims, invalid
			}
			manage = true
		default:
			return claims, invalid
		}
	}
	if !read {
		return claims, invalid
	}
	return claims, nil
}

func (s *Server) browserIdentity(r *http.Request) (BrowserIdentity, bool) {
	if !s.browserEnabled() {
		return BrowserIdentity{}, false
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.config.Browser.Origin {
		return BrowserIdentity{}, false
	}
	value, err := singleCookie(r, browserSessionCookie)
	if err != nil || !validBrowserCredential(value) {
		return BrowserIdentity{}, false
	}
	storage, ok := s.config.Store.(store.BrowserStore)
	if !ok {
		return BrowserIdentity{}, false
	}
	session, err := storage.GetBrowserSession(r.Context(), digest(value), time.Now())
	if err != nil {
		return BrowserIdentity{}, false
	}
	if requested := r.Header.Get("X-Organization-ID"); requested != "" && requested != session.OrganizationID {
		return BrowserIdentity{}, false
	}
	return BrowserIdentity{UserID: session.UserID, OrganizationID: session.OrganizationID, Capabilities: session.Capabilities, ExpiresAt: session.ExpiresAt}, true
}

// validateBrowserCSRF also verifies the persisted session, preventing a valid
// CSRF token from reviving a removed or expired session.
func (s *Server) validateBrowserCSRF(r *http.Request) bool {
	if !s.browserOrigin(r) {
		return false
	}
	if _, ok := s.browserIdentity(r); !ok {
		return false
	}
	value, err := singleCookie(r, browserSessionCookie)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.browserMAC("csrf:"+value)))
}

func (s *Server) browserGetSession(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.browserIdentity(r)
	if !ok {
		fail(w, 401, "valid browser session required")
		return
	}
	value, _ := singleCookie(r, browserSessionCookie)
	s.browserSessionResponse(w, domain.BrowserSession{OrganizationID: identity.OrganizationID, UserID: identity.UserID, Capabilities: identity.Capabilities, ExpiresAt: identity.ExpiresAt}, value)
}

func (s *Server) browserDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.validateBrowserCSRF(r) {
		fail(w, 403, "valid browser origin and CSRF token required")
		return
	}
	value, _ := singleCookie(r, browserSessionCookie)
	if err := s.config.Store.(store.BrowserStore).DeleteBrowserSession(r.Context(), digest(value)); err != nil {
		fail(w, 503, "browser session storage unavailable")
		return
	}
	s.clearBrowserCookie(w, browserSessionCookie)
	s.clearBrowserCookie(w, browserPendingCookie)
	w.WriteHeader(204)
}

func (s *Server) browserSessionResponse(w http.ResponseWriter, session domain.BrowserSession, value string) {
	writeJSON(w, 200, map[string]any{"organization_id": session.OrganizationID, "user_id": session.UserID, "capabilities": session.Capabilities, "csrf_token": s.browserMAC("csrf:" + value), "expires_at": session.ExpiresAt})
}

func (s *Server) validBrowserPending(value string, now time.Time) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || !validBrowserCredential(parts[0]) {
		return false
	}
	issued, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || issued < now.Unix()-120 || issued > now.Unix()+5 {
		return false
	}
	return hmac.Equal([]byte(parts[2]), []byte(s.browserMAC("pending:"+parts[0]+"."+parts[1])))
}

func (s *Server) browserMAC(value string) string {
	mac := hmac.New(sha256.New, s.config.Browser.Secret)
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) browserCookie(w http.ResponseWriter, name, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: s.config.Browser.CookiePath, Secure: !s.config.Browser.InsecureCookies, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: max(1, int(time.Until(expires).Seconds()))})
}

func (s *Server) clearBrowserCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: s.config.Browser.CookiePath, Secure: !s.config.Browser.InsecureCookies, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func singleCookie(r *http.Request, name string) (string, error) {
	values := r.CookiesNamed(name)
	if len(values) != 1 {
		return "", errors.New("missing or ambiguous browser cookie")
	}
	return values[0].Value, nil
}

func validBrowserCredential(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

type browserLimiter struct {
	mu      sync.Mutex
	buckets map[string]int
	window  time.Time
}

func (l *browserLimiter) admit(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.window) >= time.Minute {
		clear(l.buckets)
		l.window = time.Now()
	}
	if l.buckets[host] >= 120 || (l.buckets[host] == 0 && len(l.buckets) >= 1024) {
		return false
	}
	l.buckets[host]++
	return true
}
