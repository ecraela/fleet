package server

import (
	"errors"
	"github.com/ecraela/fleet/internal/store"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// AgentSetup contains installation-owned public connection settings, never keys.
type AgentSetup struct {
	ReleaseVersion string `json:"release_version,omitempty"`
	ReleaseURL     string `json:"release_url,omitempty"`
	Image          string `json:"image"`
	ManagerURL     string `json:"manager_url"`
	IngestURL      string `json:"ingest_url"`
	Network        string `json:"network"`
	MetricsURL     string `json:"metrics_url"`
	CAPEM          string `json:"ca_pem"`
}

func (s *Server) validateAgentSetup() error {
	a := &s.config.AgentSetup
	if a.Image == "" && a.ManagerURL == "" && a.IngestURL == "" {
		return nil
	}
	if a.ReleaseVersion != "" || a.ReleaseURL != "" {
		if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-z0-9.-]+)?$`).MatchString(a.ReleaseVersion) || a.ReleaseURL != "https://github.com/ecraela/fleet/releases/download/"+a.ReleaseVersion || !regexp.MustCompile(`^ghcr\.io/ecraela/fleet@sha256:[a-f0-9]{64}$`).MatchString(a.Image) {
			return errors.New("release setup requires an exact signed release URL and immutable image digest")
		}
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,511}$`).MatchString(a.Image) || len(a.Network) > 128 {
		return errors.New("agent setup requires an image and valid connection settings")
	}
	for _, address := range []string{a.ManagerURL, a.IngestURL} {
		u, err := url.Parse(address)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("agent setup addresses must be HTTPS origins")
		}
	}
	if a.MetricsURL == "" {
		a.MetricsURL = "http://127.0.0.1:8405/metrics"
	}
	u, err := url.Parse(a.MetricsURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid agent metrics address")
	}
	if a.Network == "" {
		a.Network = "host"
	}
	a.CAPEM = string(s.config.Authority.PEM)
	return nil
}

func (s *Server) agentSetup(w http.ResponseWriter, r *http.Request, org string) {
	if s.config.AgentSetup.Image == "" {
		fail(w, 503, "agent installer is not configured")
		return
	}
	writeJSON(w, 200, s.config.AgentSetup)
}

func (s *Server) enrollmentStatus(w http.ResponseWriter, r *http.Request, org string) {
	var body struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Token) < 32 || len(body.Token) > 256 {
		fail(w, 400, "invalid enrollment token")
		return
	}
	storage, ok := s.config.Store.(store.EnrollmentStatusStore)
	if !ok {
		fail(w, 503, "enrollment status unavailable")
		return
	}
	result, err := storage.EnrollmentStatus(r.Context(), org, digest(body.Token), time.Now())
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
