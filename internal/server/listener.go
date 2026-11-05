package server

import (
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
	"net/http"
)

func (s *Server) configureListeners() {
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/listeners", s.authorizeCapability("haproxy.read", s.listListeners))
	s.mux.HandleFunc("POST /v1/clusters/{cluster}/listeners", s.authorizeCapability("haproxy.manage", s.deployListeners))
	s.mux.HandleFunc("POST /v1/clusters/{cluster}/listeners/versions/{revision}/rollback", s.authorizeCapability("haproxy.manage", s.deployListeners))
}
func (s *Server) listListeners(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.config.Store.(store.ListenerStore)
	if !ok {
		fail(w, 503, "listener storage unavailable")
		return
	}
	rows, err := db.ListListeners(r.Context(), org, r.PathValue("cluster"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revisions": rows})
}
func (s *Server) deployListeners(w http.ResponseWriter, r *http.Request, org string) {
	var in domain.ListenerInput
	if !readJSON(w, r, &in) {
		return
	}
	db, ok := s.config.Store.(store.ListenerStore)
	if !ok {
		fail(w, 503, "listener storage unavailable")
		return
	}
	row, err := db.DeployListeners(r.Context(), org, r.PathValue("cluster"), r.PathValue("revision"), in)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"revision": row})
}
