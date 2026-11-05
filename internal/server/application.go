package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

var errApplicationHealthUnavailable = errors.New("application health unavailable")

func (s *Server) configureApplications() {
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/applications/{application}/traffic-rules", s.authorizeCapability("haproxy.read", s.applicationTrafficRules))
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/applications/{application}/versions/{revision}/traffic-rules", s.authorizeCapability("haproxy.read", s.applicationTrafficRules))
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/applications/status", s.authorizeCapability("haproxy.read", s.applicationHealth))
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/applications", s.authorizeCapability("haproxy.read", s.listApplications))
	s.mux.HandleFunc("POST /v1/clusters/{cluster}/applications/{application}/dns-intent", s.authorizeCapability("haproxy.manage", s.applicationDNSIntent))
	s.mux.HandleFunc("POST /v1/clusters/{cluster}/applications", s.authorizeCapability("haproxy.manage", s.deployApplication))
	s.mux.HandleFunc("POST /v1/clusters/{cluster}/applications/{application}/deploy", s.authorizeCapability("haproxy.manage", s.deployApplication))
	s.mux.HandleFunc("POST /v1/clusters/{cluster}/applications/{application}/lifecycle", s.authorizeCapability("haproxy.manage", s.applicationLifecycle))
	s.mux.HandleFunc("POST /v1/clusters/{cluster}/applications/{application}/versions/{revision}/rollback", s.authorizeCapability("haproxy.manage", s.deployApplication))
}
func (s *Server) applicationHealth(w http.ResponseWriter, r *http.Request, org string) {
	rows, nodes, controls, err := s.applicationSnapshot(r.Context(), org, r.PathValue("cluster"))
	if err != nil {
		if err == errApplicationHealthUnavailable {
			fail(w, 503, err.Error())
		} else {
			storeError(w, err)
		}
		return
	}
	rowsOut := domain.ApplicationHealthAt(rows, nodes, controls, time.Now())
	sort.Slice(rowsOut, func(i, j int) bool { return rowsOut[i].ApplicationID < rowsOut[j].ApplicationID })
	writeJSON(w, 200, map[string]any{"organization_id": org, "cluster_id": r.PathValue("cluster"), "applications": rowsOut, "observed_at_ms": time.Now().UnixMilli()})
}
func (s *Server) applicationSnapshot(ctx context.Context, org, cluster string) ([]domain.ApplicationRevision, []domain.Node, map[string]domain.NodeControl, error) {
	apps, ok := s.config.Store.(store.ApplicationStore)
	control, controlOK := s.config.Store.(store.ControlStore)
	if !ok || !controlOK {
		return nil, nil, nil, errApplicationHealthUnavailable
	}
	if _, err := s.config.Store.GetCluster(ctx, org, cluster); err != nil {
		return nil, nil, nil, err
	}
	rows, err := apps.ListApplications(ctx, org, cluster)
	if err != nil {
		return nil, nil, nil, err
	}
	all, err := s.config.Store.ListNodes(ctx, org)
	if err != nil {
		return nil, nil, nil, err
	}
	nodes := []domain.Node{}
	controls := map[string]domain.NodeControl{}
	for _, n := range all {
		if n.ClusterID == cluster && n.RevokedAt == nil {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) > 12 {
		return nil, nil, nil, domain.ErrConflict
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	for _, n := range nodes {
		c, _, err := control.GetControl(ctx, org, n.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		controls[n.ID] = c
	}
	return rows, nodes, controls, nil
}

func (s *Server) listApplications(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.config.Store.(store.ApplicationStore)
	if !ok {
		fail(w, 503, "application storage unavailable")
		return
	}
	rows, err := db.ListApplications(r.Context(), org, r.PathValue("cluster"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revisions": rows})
}
func (s *Server) deployApplication(w http.ResponseWriter, r *http.Request, org string) {
	var input domain.ApplicationInput
	if !readJSON(w, r, &input) {
		return
	}
	if input.Action != "" {
		storeError(w, domain.ErrInvalidInput)
		return
	}
	s.writeApplication(w, r, org, input)
}
func (s *Server) applicationLifecycle(w http.ResponseWriter, r *http.Request, org string) {
	var body struct {
		RequestID        string                          `json:"request_id"`
		ExpectedRevision int                             `json:"expected_revision"`
		Action           string                          `json:"action"`
		Nodes            []domain.RolloutTarget          `json:"nodes"`
		Entrypoint       *domain.RolloutEntrypointTarget `json:"entrypoint"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Action == "" {
		storeError(w, domain.ErrInvalidInput)
		return
	}
	s.writeApplication(w, r, org, domain.ApplicationInput{RequestID: body.RequestID, ExpectedRevision: body.ExpectedRevision, Action: body.Action, Nodes: body.Nodes, Entrypoint: body.Entrypoint})
}
func (s *Server) writeApplication(w http.ResponseWriter, r *http.Request, org string, input domain.ApplicationInput) {
	if input.Spec.RequestTelemetry && s.logs == nil {
		if _, err := s.config.Store.GetCluster(r.Context(), org, r.PathValue("cluster")); err != nil {
			storeError(w, err)
			return
		}
		fail(w, 503, "request telemetry is not configured")
		return
	}
	db, ok := s.config.Store.(store.ApplicationStore)
	if !ok {
		fail(w, 503, "application storage unavailable")
		return
	}
	revision, err := db.DeployApplication(r.Context(), org, r.PathValue("cluster"), r.PathValue("application"), r.PathValue("revision"), input)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"revision": revision})
}
