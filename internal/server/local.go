package server

import (
	"crypto/subtle"
	"errors"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/localauth"
	"github.com/ecraela/fleet/internal/store"
	"net"
	"net/http"
	"net/url"
	"time"
)

const localCookie = "haproxy_manager_local"

type LocalConfig struct {
	Origin          string
	InsecureCookies bool
	slots           chan struct{}
}

func (s *Server) configureLocal() error {
	c := &s.config.Local
	if c.Origin == "" {
		return nil
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.String() != c.Origin || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("local origin must be an exact HTTP(S) origin")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if (u.Scheme != "https" && !c.InsecureCookies) || (c.InsecureCookies && !loopback) {
		return errors.New("local insecure cookies require an explicit loopback development origin")
	}
	if s.config.Role != "manager" {
		return errors.New("local login requires manager role")
	}
	if _, ok := s.config.Store.(store.LocalStore); !ok {
		return errors.New("local login requires persistent local account storage")
	}
	c.slots = make(chan struct{}, 2)
	s.mux.HandleFunc("POST /v1/local/session", s.localLogin)
	s.mux.HandleFunc("GET /v1/local/session", s.localGetSession)
	s.mux.HandleFunc("DELETE /v1/local/session", s.localLogout)
	s.mux.HandleFunc("POST /v1/local/password", s.localPassword)
	return nil
}
func (s *Server) localOrigin(r *http.Request) bool {
	return s.config.Local.Origin != "" && r.Header.Get("Origin") == s.config.Local.Origin
}
func (s *Server) localSession(r *http.Request) (domain.LocalUser, domain.LocalSession, string, bool) {
	if s.config.Local.Origin == "" {
		return domain.LocalUser{}, domain.LocalSession{}, "", false
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.config.Local.Origin {
		return domain.LocalUser{}, domain.LocalSession{}, "", false
	}
	value, err := singleCookie(r, localCookie)
	if err != nil || !validBrowserCredential(value) {
		return domain.LocalUser{}, domain.LocalSession{}, "", false
	}
	u, session, err := s.config.Store.(store.LocalStore).GetLocalSession(r.Context(), digest(value), time.Now())
	if err != nil || len(u.Capabilities()) == 0 {
		return domain.LocalUser{}, domain.LocalSession{}, "", false
	}
	if org := r.Header.Get("X-Organization-ID"); org != "" && org != u.OrganizationID {
		return domain.LocalUser{}, domain.LocalSession{}, "", false
	}
	return u, session, value, true
}
func (s *Server) localCSRF(r *http.Request) bool {
	if !s.localOrigin(r) {
		return false
	}
	_, _, value, ok := s.localSession(r)
	return ok && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(digest("local-csrf:"+value))) == 1
}
func (s *Server) managementIdentity(r *http.Request) (BrowserIdentity, bool) {
	if _, ok := r.Context().Value(connectorRequestKey{}).(connectorRequestIdentity); ok {
		return s.connectorIdentity(r)
	}
	if r.Header.Get("X-HAProxy-Session") == "local" {
		u, session, _, ok := s.localSession(r)
		return BrowserIdentity{UserID: u.ID, OrganizationID: u.OrganizationID, Capabilities: u.Capabilities(), ExpiresAt: session.ExpiresAt}, ok
	}
	return s.browserIdentity(r)
}
func (s *Server) managementCSRF(r *http.Request) bool {
	if _, ok := r.Context().Value(connectorRequestKey{}).(connectorRequestIdentity); ok {
		_, valid := s.connectorIdentity(r)
		return valid
	}
	if r.Header.Get("X-HAProxy-Session") == "local" {
		return s.localCSRF(r)
	}
	return s.validateBrowserCSRF(r)
}
func (s *Server) localSetCookie(w http.ResponseWriter, value string, expires time.Time) {
	c := &http.Cookie{Name: localCookie, Value: value, Path: "/api/haproxy", Secure: !s.config.Local.InsecureCookies, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: max(1, int(time.Until(expires).Seconds()))}
	if value == "" {
		c.MaxAge = -1
		c.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, c)
}
func (s *Server) localResponse(w http.ResponseWriter, u domain.LocalUser, session domain.LocalSession, value string) {
	writeJSON(w, 200, map[string]any{"user_id": u.ID, "username": u.Username, "organization_id": u.OrganizationID, "role": u.Role, "capabilities": u.Capabilities(), "csrf_token": digest("local-csrf:" + value), "expires_at": session.ExpiresAt})
}
func (s *Server) localSlot(w http.ResponseWriter) bool {
	select {
	case s.config.Local.slots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "5")
		fail(w, 429, "sign-in is busy; retry shortly")
		return false
	}
}
func (s *Server) localLogin(w http.ResponseWriter, r *http.Request) {
	if !s.localOrigin(r) {
		fail(w, 403, "local browser origin is not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, err := localauth.Username(body.Username)
	if err != nil {
		name = "invalid"
	}
	source, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		source = r.RemoteAddr
	}
	db := s.config.Store.(store.LocalStore)
	allowed, err := db.AdmitLocalLogin(r.Context(), digest(source), digest(name), time.Now())
	if err != nil {
		fail(w, 503, "local account storage unavailable")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "too many sign-in attempts; retry in a minute")
		return
	}
	if !s.localSlot(w) {
		return
	}
	defer func() { <-s.config.Local.slots }()
	u, lookupErr := db.GetLocalUser(r.Context(), name)
	// Hash even an unknown or disabled account to avoid a cheap enumeration path.
	valid := localauth.Verify(u.PasswordHash, body.Password)
	if lookupErr != nil || !valid || u.Disabled {
		fail(w, 401, "username or password is incorrect")
		return
	}
	now := time.Now().UTC()
	value := secret()
	session := domain.LocalSession{Digest: digest(value), UserID: u.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(12 * time.Hour)}
	if err := db.CreateLocalSession(r.Context(), session, u.PasswordHash); err != nil {
		fail(w, 401, "sign-in could not complete; retry")
		return
	}
	// Never retain a pre-login session identifier.
	if old, e := singleCookie(r, localCookie); e == nil && validBrowserCredential(old) {
		_ = db.DeleteLocalSession(r.Context(), digest(old))
	}
	s.localSetCookie(w, value, session.ExpiresAt)
	s.localResponse(w, u, session, value)
}
func (s *Server) localGetSession(w http.ResponseWriter, r *http.Request) {
	u, session, value, ok := s.localSession(r)
	if !ok {
		fail(w, 401, "local sign-in required")
		return
	}
	s.localResponse(w, u, session, value)
}
func (s *Server) localLogout(w http.ResponseWriter, r *http.Request) {
	if !s.localCSRF(r) {
		fail(w, 403, "valid local origin and CSRF token required")
		return
	}
	value, _ := singleCookie(r, localCookie)
	if err := s.config.Store.(store.LocalStore).DeleteLocalSession(r.Context(), digest(value)); err != nil {
		fail(w, 503, "local account storage unavailable")
		return
	}
	s.localSetCookie(w, "", time.Time{})
	w.WriteHeader(204)
}
func (s *Server) localPassword(w http.ResponseWriter, r *http.Request) {
	if !s.localCSRF(r) {
		fail(w, 403, "valid local origin and CSRF token required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	u, _, _, ok := s.localSession(r)
	if !ok {
		fail(w, 401, "local sign-in required")
		return
	}
	source, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		source = r.RemoteAddr
	}
	db := s.config.Store.(store.LocalStore)
	allowed, err := db.AdmitLocalLogin(r.Context(), digest(source), digest("password:"+u.ID), time.Now())
	if err != nil {
		fail(w, 503, "local account storage unavailable")
		return
	}
	if !allowed {
		fail(w, 429, "too many attempts; retry in a minute")
		return
	}
	if !s.localSlot(w) {
		return
	}
	defer func() { <-s.config.Local.slots }()
	if !localauth.Verify(u.PasswordHash, body.Current) {
		fail(w, 401, "current password is incorrect")
		return
	}
	hash, err := localauth.Hash(body.Next)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if body.Current == body.Next {
		fail(w, 400, "choose a different password")
		return
	}
	if err = db.ChangeLocalPassword(r.Context(), u.ID, u.PasswordHash, hash); err != nil {
		fail(w, 409, "account changed; sign in again")
		return
	}
	s.localSetCookie(w, "", time.Time{})
	w.WriteHeader(204)
}
