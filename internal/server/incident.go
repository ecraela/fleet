package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

func (s *Server) configureIncidents() {
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/incidents", s.authorizeCapability("haproxy.read", s.incidents))
}
func (s *Server) incidents(w http.ResponseWriter, r *http.Request, org string) {
	db, ok := s.config.Store.(store.IncidentStore)
	if !ok {
		fail(w, 503, "incident storage unavailable")
		return
	}
	before := int64(0)
	q := r.URL.Query()
	if len(q) > 2 || len(q["before"]) > 1 || len(q["incident"]) > 1 {
		fail(w, 400, "invalid incident query")
		return
	}
	for k := range q {
		if k != "before" && k != "incident" {
			fail(w, 400, "invalid incident query")
			return
		}
	}
	if v := q.Get("before"); v != "" {
		var err error
		before, err = strconv.ParseInt(v, 10, 53)
		if err != nil || before < 1 {
			fail(w, 400, "invalid incident cursor")
			return
		}
	}
	page, err := db.ListIncidents(r.Context(), org, r.PathValue("cluster"), q.Get("incident"), before)
	if err != nil {
		storeError(w, err)
		return
	}
	if ns, ok := s.config.Store.(store.NotificationStore); ok {
		status, err := ns.IncidentNotificationStatus(r.Context(), org, r.PathValue("cluster"))
		if err != nil {
			storeError(w, err)
			return
		}
		status.Configured = s.notificationConfigured(org)
		page.Notifications = status
	}
	writeJSON(w, 200, page)
}

// Evaluate independently of browser traffic. Queries use the manager's private
// telemetry service, with the same bounded tenant validation as fleet status.
func (s *Server) evaluateIncidentCluster(ctx context.Context, c domain.Cluster) error {
	db := s.config.Store.(store.IncidentStore)
	nodes, err := s.config.Store.ListNodes(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	selected := []FleetNode{}
	for _, n := range nodes {
		if n.ClusterID == c.ID && n.RevokedAt == nil {
			selected = append(selected, FleetNode{Node: n})
		}
	}
	// This release shares the 12-node control-plane profile. Do not silently
	// evaluate a truncated fleet as though the missing nodes were healthy.
	if len(selected) > 12 {
		return domain.ErrConflict
	}
	metricErr := s.fleetObservations(ctx, c.OrganizationID, selected)
	t := domain.IncidentTelemetry{Available: metricErr == nil || errors.Is(metricErr, errPartialFleetObservations), QueriedAtMS: time.Now().UnixMilli(), Nodes: map[string]domain.IncidentNodeTelemetry{}}
	for _, n := range selected {
		if n.ObservedAt != nil {
			t.Nodes[n.ID] = domain.IncidentNodeTelemetry{ObservedAtMS: n.ObservedAt.UnixMilli(), ScrapeOK: n.ScrapeOK, ScrapeKnown: n.MetricsAvailable}
		}
	}
	return db.ReconcileIncidents(ctx, c.OrganizationID, c.ID, t, time.Now().UTC())
}
func (s *Server) RunIncidents(ctx context.Context) {
	db, ok := s.config.Store.(store.IncidentStore)
	if !ok || s.config.Role != "manager" {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	cursor := ""
	for ctx.Err() == nil {
		inventory, cancel := context.WithTimeout(ctx, 3*time.Second)
		clusters, err := db.IncidentClusters(inventory, cursor)
		cancel()
		if err != nil {
			slog.Warn("HAProxy incident inventory unavailable")
		} else {
			s.evaluateIncidentPage(ctx, clusters)
			if len(clusters) > 0 {
				cursor = clusters[len(clusters)-1].ID
			}
			if len(clusters) < 50 {
				cursor = ""
			} else {
				// Drain the full sweep before waiting, including exact page multiples.
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) evaluateIncidentPage(ctx context.Context, clusters []domain.Cluster) {
	work := make(chan domain.Cluster)
	var workers sync.WaitGroup
	for range min(4, len(clusters)) {
		workers.Go(func() {
			for cluster := range work {
				step, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := s.evaluateIncidentCluster(step, cluster)
				cancel()
				if err != nil && ctx.Err() == nil {
					slog.Warn("HAProxy incident evaluation failed")
				}
			}
		})
	}
	for _, cluster := range clusters {
		select {
		case work <- cluster:
		case <-ctx.Done():
			close(work)
			workers.Wait()
			return
		}
	}
	close(work)
	workers.Wait()
}
