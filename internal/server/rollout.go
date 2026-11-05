package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

func (s *Server) configureRollouts() {
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/rollouts", s.authorizeCapability("haproxy.read", s.listRollouts))
	s.mux.HandleFunc("POST /v1/clusters/{cluster}/rollouts", s.authorizeCapability("haproxy.manage", s.createRollout))
}
func (s *Server) listRollouts(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.config.Store.(store.RolloutStore)
	if !ok {
		fail(w, 503, "rollout storage unavailable")
		return
	}
	rows, err := db.ListRollouts(r.Context(), org, r.PathValue("cluster"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"rollouts": rows})
}
func (s *Server) createRollout(w http.ResponseWriter, r *http.Request, org string) {
	var input domain.RolloutInput
	if !readJSON(w, r, &input) {
		return
	}
	db, ok := s.config.Store.(store.RolloutStore)
	if !ok {
		fail(w, 503, "rollout storage unavailable")
		return
	}
	rollout, err := db.CreateRollout(r.Context(), org, r.PathValue("cluster"), input)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"rollout": rollout})
}

// RunRollouts belongs to the manager process, not an HTTP request or browser
// session. All replicas use the same transactional coordinator and node fences.
func (s *Server) RunRollouts(ctx context.Context) {
	db, ok := s.config.Store.(store.RolloutStore)
	if !ok || s.config.Role != "manager" {
		return
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for ctx.Err() == nil {
		if err := db.AdvanceRollouts(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("HAProxy rollout checkpoint failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
