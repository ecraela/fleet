package server

import (
	"net/http"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

func (s *Server) configureConfiguration() {
	s.mux.HandleFunc("GET /v1/nodes/{node}/configuration", s.authorizeCapability("haproxy.read", s.nodeConfiguration))
	s.mux.HandleFunc("POST /v1/nodes/{node}/configuration", s.authorizeCapability("haproxy.manage", s.saveConfiguration))
	s.mux.HandleFunc("POST /v1/nodes/{node}/configuration/versions/{revision}/rollback", s.authorizeCapability("haproxy.manage", s.saveConfiguration))
}
func (s *Server) nodeConfiguration(w http.ResponseWriter, r *http.Request, org string) {
	persistence, ok := s.config.Store.(store.ConfigurationStore)
	if !ok {
		fail(w, 503, "configuration unavailable")
		return
	}
	state, versions, err := persistence.GetConfiguration(r.Context(), org, r.PathValue("node"))
	if err != nil {
		storeError(w, err)
		return
	}
	connected := state.SessionID != "" && time.Since(state.LastSeen) <= 6*time.Second
	fresh := connected && state.Discovery != nil && state.Discovery.Fresh(time.Now()) && state.Discovery.Configuration != nil && state.Discovery.Configuration.Fresh(time.Now())
	writeJSON(w, 200, map[string]any{"control": state, "connected": connected, "fresh": fresh, "configuration_enabled": fresh && state.Discovery.Configuration.ControlAllowed, "versions": versions})
}
func (s *Server) saveConfiguration(w http.ResponseWriter, r *http.Request, org string) {
	var body struct {
		RequestID  string                        `json:"request_id"`
		Generation string                        `json:"expected_generation"`
		ArtifactID string                        `json:"expected_artifact_id"`
		Version    int64                         `json:"expected_version"`
		Document   *domain.ConfigurationDocument `json:"document"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	persistence, ok := s.config.Store.(store.ConfigurationStore)
	if !ok {
		fail(w, 503, "configuration unavailable")
		return
	}
	rollback := r.PathValue("revision")
	if rollback != "" && body.Document != nil {
		fail(w, 400, "rollback uses the stored revision")
		return
	}
	op, err := persistence.CreateConfiguration(r.Context(), org, domain.Operation{NodeID: r.PathValue("node"), RequestID: body.RequestID, ExpectedGeneration: body.Generation, BaseArtifactID: body.ArtifactID, BaseVersion: body.Version, Configuration: body.Document}, rollback)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"operation": op})
}
