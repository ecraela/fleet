package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ecraela/fleet/internal/buildinfo"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
	"github.com/ecraela/fleet/internal/telemetry"
)

var errPartialFleetObservations = errors.New("some node status observations are invalid")

type FleetNode struct {
	Agent          *buildinfo.Info `json:"agent,omitempty"`
	AgentFresh     bool            `json:"agent_fresh"`
	HAProxyVersion string          `json:"haproxy_version,omitempty"`
	domain.Node
	TelemetryFresh   bool       `json:"telemetry_fresh"`
	ScrapeOK         bool       `json:"scrape_ok"`
	ObservedAt       *time.Time `json:"observed_at"`
	MetricsAvailable bool       `json:"metrics_available"`
}

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request, org string) {
	inventory, ok := s.config.Store.(store.InventoryStore)
	if !ok {
		fail(w, 503, "inventory storage unavailable")
		return
	}
	clusters, err := inventory.ListClusters(r.Context(), org)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"clusters": clusters})
}

func (s *Server) fleet(w http.ResponseWriter, r *http.Request, org string) {
	inventory, ok := s.config.Store.(store.InventoryStore)
	if !ok {
		fail(w, 503, "inventory storage unavailable")
		return
	}
	clusters, err := inventory.ListClusters(r.Context(), org)
	if err != nil {
		storeError(w, err)
		return
	}
	nodes, next, err := inventory.ListNodesPage(r.Context(), org, r.URL.Query().Get("cursor"), store.FleetPageSize)
	if err != nil {
		storeError(w, err)
		return
	}
	result := make([]FleetNode, len(nodes))
	for i, node := range nodes {
		result[i].Node = node
	}
	if builds, ok := s.config.Store.(store.AgentBuildStore); ok {
		ids := make([]string, len(nodes))
		for i, n := range nodes {
			ids[i] = n.ID
		}
		observations, e := builds.AgentBuilds(r.Context(), org, ids)
		if e != nil {
			storeError(w, e)
			return
		}
		for i, n := range nodes {
			if o, exists := observations[n.ID]; exists {
				result[i].Agent = o.Build
				result[i].AgentFresh = o.Connected && time.Since(o.LastSeen) <= 6*time.Second
				result[i].HAProxyVersion = o.HAProxyVersion
			}
		}
	}
	err = s.fleetObservations(r.Context(), org, result)
	var nextCursor any
	if next != "" {
		nextCursor = next
	}
	writeJSON(w, 200, map[string]any{"clusters": clusters, "nodes": result, "next_cursor": nextCursor, "partial": err != nil, "manager_build": buildinfo.Current()})
}

func (s *Server) fleetObservations(ctx context.Context, org string, nodes []FleetNode) error {
	index := make(map[string]int, len(nodes))
	ids := make([]string, 0, len(nodes))
	for i, node := range nodes {
		if node.RevokedAt == nil {
			index[node.ID] = i
			ids = append(ids, node.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if !acquire(s.queries) {
		return errors.New("query capacity reached")
	}
	defer func() { <-s.queries }()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	u := s.storageURL("/api/v1/query")
	q := u.Query()
	selector := fmt.Sprintf(`{__name__=~"converge_agent_haproxy_up|converge_agent_scrape_timestamp_seconds",organization_id="%s",node_id=~"%s"}`, org, strings.Join(ids, "|"))
	q.Set("query", singletonStatusQuery(selector))
	q.Set("nocache", "1")
	q.Set("latency_offset", "1ms")
	q.Set("timeout", "2s")
	u.RawQuery = q.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("metrics query failed")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("metrics query response exceeds limit")
	}
	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Status != "success" || envelope.Data.ResultType != "vector" {
		return errors.New("invalid fleet metrics response")
	}
	// Scope is a global trust boundary. Check every row before publishing any
	// observation, including rows after a malformed or duplicate node value.
	for _, point := range envelope.Data.Result {
		i, exists := index[point.Metric["node_id"]]
		name := point.Metric["__name__"]
		if !exists || point.Metric["organization_id"] != org || point.Metric["cluster_id"] != nodes[i].ClusterID || (name != "converge_agent_haproxy_up" && name != "converge_agent_scrape_timestamp_seconds") {
			return errors.New("fleet observation scope mismatch")
		}
	}
	seen := make(map[string]bool, len(envelope.Data.Result))
	invalid := map[string]bool{}
	values := map[string]float64{}
	for _, point := range envelope.Data.Result {
		id, name := point.Metric["node_id"], point.Metric["__name__"]
		key := id + name
		if len(point.Value) != 2 || len(point.Metric) != 4 || seen[key] {
			invalid[id] = true
			continue
		}
		seen[key] = true
		var text string
		if json.Unmarshal(point.Value[1], &text) != nil {
			invalid[id] = true
			continue
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil || !telemetry.ValidStatusValue(name, value) {
			invalid[id] = true
			continue
		}
		var evaluatedAt float64
		if json.Unmarshal(point.Value[0], &evaluatedAt) != nil || math.IsNaN(evaluatedAt) || math.IsInf(evaluatedAt, 0) {
			invalid[id] = true
			continue
		}
		values[key] = value
	}
	for id, i := range index {
		nodes[i].ScrapeOK, nodes[i].MetricsAvailable, nodes[i].TelemetryFresh, nodes[i].ObservedAt = false, false, false, nil
		if invalid[id] {
			continue
		}
		if value, ok := values[id+"converge_agent_haproxy_up"]; ok {
			nodes[i].ScrapeOK = value == 1
			nodes[i].MetricsAvailable = true
		}
		if seconds, ok := values[id+"converge_agent_scrape_timestamp_seconds"]; ok {
			observed := time.UnixMilli(int64(seconds * 1000)).UTC()
			nodes[i].ObservedAt = &observed
			age := time.Since(observed)
			nodes[i].TelemetryFresh = age >= -time.Minute && age <= 15*time.Second
		}
	}
	if len(invalid) > 0 {
		return errPartialFleetObservations
	}
	return nil
}
