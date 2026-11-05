package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

func (s *Server) configureMaintenance() {
	s.mux.HandleFunc("GET /v1/nodes/{node}/maintenance", s.authorizeCapability("haproxy.read", s.getMaintenance))
	s.mux.HandleFunc("POST /v1/nodes/{node}/maintenance", s.authorizeCapability("haproxy.manage", s.changeMaintenance))
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/maintenance", s.authorizeCapability("haproxy.read", s.listMaintenance))
}
func (s *Server) getMaintenance(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.config.Store.(store.MaintenanceStore)
	if !ok {
		fail(w, 503, "maintenance storage unavailable")
		return
	}
	view, err := db.GetMaintenance(r.Context(), org, r.PathValue("node"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) changeMaintenance(w http.ResponseWriter, r *http.Request, org string) {
	var input domain.MaintenanceInput
	if !readJSON(w, r, &input) {
		return
	}
	db, ok := s.config.Store.(store.MaintenanceStore)
	if !ok {
		fail(w, 503, "maintenance storage unavailable")
		return
	}
	result, err := db.ChangeMaintenance(r.Context(), org, r.PathValue("node"), input)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"maintenance": result})
}
func (s *Server) listMaintenance(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.config.Store.(store.MaintenanceStore)
	if !ok {
		fail(w, 503, "maintenance storage unavailable")
		return
	}
	rows, err := db.ListMaintenance(r.Context(), org, r.PathValue("cluster"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"maintenance": rows})
}
func (s *Server) RunMaintenance(ctx context.Context) {
	db, ok := s.config.Store.(store.MaintenanceStore)
	if !ok || s.config.Role != "manager" {
		return
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for ctx.Err() == nil {
		if err := db.AdvanceMaintenance(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("HAProxy maintenance checkpoint failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
