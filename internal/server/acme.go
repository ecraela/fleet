package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

type acmeOption struct {
	Issuer      string                 `json:"issuer"`
	Environment string                 `json:"environment"`
	Binding     domain.ListenerBinding `json:"binding"`
	Challenges  []domain.ACMEChallenge `json:"challenges"`
}

func (s *Server) configureACME() {
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/certificates", s.authorizeCapability("haproxy.read", s.getACME))
	s.mux.HandleFunc("POST /v1/clusters/{cluster}/certificates", s.authorizeCapability("haproxy.manage", s.setACME))
	s.mux.HandleFunc("POST /v1/clusters/{cluster}/applications/{application}/dns-delegation", s.authorizeCapability("haproxy.manage", s.applicationDNSDelegation))
	s.mux.HandleFunc("GET /v1/identity/acme", s.nodeACME)
	s.mux.HandleFunc("POST /v1/identity/acme/{job}/result", s.reportACME)
}
func (s *Server) acmeOptions(ctx context.Context, org, cluster string) ([]acmeOption, error) {
	_, nodes, controls, err := s.applicationSnapshot(ctx, org, cluster)
	if err != nil {
		return nil, err
	}
	options := []acmeOption{}
	if len(nodes) < 2 {
		return options, nil
	}
	first := controls[nodes[0].ID].Discovery
	if first == nil || first.Configuration == nil {
		return options, nil
	}
	state := first.Configuration
	for _, grant := range state.ACMEGrants {
		for _, binding := range state.ListenerBindings {
			if binding.Name != grant.Listener {
				continue
			}
			valid := true
			for _, node := range nodes {
				c := controls[node.ID]
				if c.Discovery == nil || !c.Discovery.Fresh(time.Now()) || c.Discovery.Configuration == nil {
					valid = false
					break
				}
				other := c.Discovery.Configuration
				valid = valid && other.NodeID == node.ID && slices.Contains(other.ListenerBindings, binding) && slices.ContainsFunc(other.ACMEGrants, func(g domain.ACMEGrant) bool {
					return g.Issuer == grant.Issuer && g.Listener == grant.Listener && g.Environment == grant.Environment && slices.Equal(g.Challenges, grant.Challenges)
				})
			}
			if valid {
				options = append(options, acmeOption{Issuer: grant.Issuer, Environment: grant.Environment, Binding: binding, Challenges: grant.Challenges})
			}
		}
	}
	return options, nil
}
func (s *Server) getACME(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.config.Store.(store.ACMEStore)
	if !ok {
		fail(w, 503, "certificate storage unavailable")
		return
	}
	options, err := s.acmeOptions(r.Context(), org, r.PathValue("cluster"))
	if err != nil {
		storeError(w, err)
		return
	}
	plan, err := db.GetACME(r.Context(), org, r.PathValue("cluster"))
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		storeError(w, err)
		return
	}
	var result *domain.ACMEPlan
	if err == nil {
		result = &plan
	}
	writeJSON(w, 200, map[string]any{"plan": result, "options": options})
}
func (s *Server) setACME(w http.ResponseWriter, r *http.Request, org string) {
	var in domain.ACMEInput
	if !readJSON(w, r, &in) {
		return
	}
	db, ok := s.config.Store.(store.ACMEStore)
	if !ok {
		fail(w, 503, "certificate storage unavailable")
		return
	}
	p, err := db.SetACME(r.Context(), org, r.PathValue("cluster"), in)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"plan": p})
}
func (s *Server) nodeACME(w http.ResponseWriter, r *http.Request) {
	n, err := s.authenticateNode(r)
	if err != nil {
		fail(w, 401, "valid node certificate required")
		return
	}
	db, ok := s.config.Store.(store.ACMEStore)
	if !ok {
		fail(w, 503, "certificate storage unavailable")
		return
	}
	job, err := db.NodeACME(r.Context(), n.OrganizationID, n.ClusterID, n.ID)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"job": job})
}
func (s *Server) reportACME(w http.ResponseWriter, r *http.Request) {
	n, err := s.authenticateNode(r)
	if err != nil {
		fail(w, 401, "valid node certificate required")
		return
	}
	var result domain.ACMEResult
	if !readJSON(w, r, &result) {
		return
	}
	if result.JobID != r.PathValue("job") {
		fail(w, 400, "issuance result does not match job")
		return
	}
	db, ok := s.config.Store.(store.ACMEStore)
	if !ok {
		fail(w, 503, "certificate storage unavailable")
		return
	}
	if err = db.ReportACME(r.Context(), n.OrganizationID, n.ClusterID, n.ID, result); err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"accepted": true})
}
func (s *Server) RunACME(ctx context.Context) {
	db, ok := s.config.Store.(store.ACMEStore)
	if !ok || s.config.Role != "manager" {
		return
	}
	timer := time.NewTicker(3 * time.Second)
	defer timer.Stop()
	for ctx.Err() == nil {
		if err := db.AdvanceACME(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("HAProxy certificate checkpoint failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
