package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type LiveSeries struct {
	Metric map[string]string `json:"metric"`
	Value  float64           `json:"value"`
}

type LiveNode struct {
	NodeID     string       `json:"node_id"`
	ObservedAt *time.Time   `json:"observed_at"`
	Fresh      bool         `json:"fresh"`
	ScrapeOK   bool         `json:"scrape_ok"`
	Revoked    bool         `json:"revoked"`
	Series     []LiveSeries `json:"series"`
}

type ClusterLive struct {
	ClusterID string     `json:"cluster_id"`
	Nodes     []LiveNode `json:"nodes"`
}

func liveQuery(cluster domain.Cluster, nodes []domain.Node) string {
	declared := map[string]bool{}
	if cluster.Topology != nil {
		for _, n := range cluster.Topology.Nodes {
			declared[n.NodeID] = true
		}
	}
	groups := map[string][]string{}
	for _, n := range nodes {
		if n.RevokedAt != nil {
			continue
		}
		frontend := ""
		if declared[n.ID] {
			frontend = cluster.Topology.Frontend
		}
		groups[frontend] = append(groups[frontend], n.ID)
	}
	parts := make([]string, 0, len(groups)*3)
	for frontend, ids := range groups {
		scope := fmt.Sprintf(`organization_id="%s",cluster_id="%s",node_id=~"%s"`, cluster.OrganizationID, cluster.ID, strings.Join(ids, "|"))
		parts = append(parts, observationQuery(scope, 5*time.Second, frontend),
			fmt.Sprintf(`label_replace((%s), "series", "up", "", "")`, singletonStatusQuery(fmt.Sprintf(`converge_agent_haproxy_up{%s}`, scope))),
			fmt.Sprintf(`label_replace((%s), "series", "observed_at", "", "")`, singletonStatusQuery(fmt.Sprintf(`converge_agent_scrape_timestamp_seconds{%s}`, scope))))
	}
	if len(parts) == 0 {
		return ""
	}
	// Keep one overflow sentinel per node instead of letting its label diversity
	// exhaust the whole response. Readers reject that node above 64 series.
	return fmt.Sprintf("topk by (organization_id,cluster_id,node_id) (65, %s)", strings.Join(parts, " or "))
}

func (s *Server) clusterLive(ctx context.Context, cluster domain.Cluster, nodes []domain.Node) (ClusterLive, error) {
	frame := ClusterLive{ClusterID: cluster.ID, Nodes: make([]LiveNode, len(nodes))}
	index := make(map[string]int, len(nodes))
	for i, n := range nodes {
		index[n.ID] = i
		frame.Nodes[i] = LiveNode{NodeID: n.ID, Revoked: n.RevokedAt != nil, Series: []LiveSeries{}}
	}
	query := liveQuery(cluster, nodes)
	if query == "" {
		return frame, nil
	}
	if !acquire(s.queries) {
		return frame, errors.New("query capacity reached")
	}
	defer func() { <-s.queries }()
	if latest, handled, err := s.latestLive(ctx, cluster, nodes); handled || err != nil {
		return latest, err
	}
	u := s.storageURL("/api/v1/query")
	q := u.Query()
	q.Set("query", query)
	q.Set("timeout", "2s")
	q.Set("nocache", "1")
	q.Set("latency_offset", "1ms")
	u.RawQuery = q.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return frame, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return frame, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return frame, errors.New("live query failed")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return frame, errors.New("live response exceeds limit")
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
		return frame, errors.New("invalid live vector")
	}
	// Scope errors invalidate the entire response, even after a malformed row.
	for _, point := range envelope.Data.Result {
		i, ok := index[point.Metric["node_id"]]
		if !ok || nodes[i].RevokedAt != nil || point.Metric["organization_id"] != cluster.OrganizationID || point.Metric["cluster_id"] != cluster.ID {
			return frame, errors.New("live scope mismatch")
		}
	}
	seen := map[string]bool{}
	invalid := map[int]bool{}
	counts := map[int]int{}
	for _, point := range envelope.Data.Result {
		i := index[point.Metric["node_id"]]
		counts[i]++
		if len(point.Value) != 2 || counts[i] > 64 {
			invalid[i] = true
			continue
		}
		name, code, proxy := point.Metric["series"], point.Metric["code"], point.Metric["proxy"]
		key := nodes[i].ID + "\x00" + name + "\x00" + code + "\x00" + proxy
		valid := (name == "up" || name == "observed_at" || name == "traffic_in" || name == "traffic_out" || name == "connections") && code == "" && proxy == ""
		valid = valid || (name == "latency" && code == "" && proxy != "" && len(proxy) <= 255)
		switch code {
		case "1xx", "2xx", "3xx", "4xx", "5xx", "other":
			valid = valid || (name == "http_responses" && proxy == "")
		}
		if !valid || seen[key] {
			invalid[i] = true
			continue
		}
		for label := range point.Metric {
			switch label {
			case "__name__", "organization_id", "cluster_id", "node_id", "series", "code", "proxy":
			default:
				invalid[i] = true
			}
		}
		seen[key] = true
		var text string
		var evaluated float64
		if json.Unmarshal(point.Value[1], &text) != nil || json.Unmarshal(point.Value[0], &evaluated) != nil || math.IsNaN(evaluated) || math.IsInf(evaluated, 0) {
			invalid[i] = true
			continue
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			invalid[i] = true
			continue
		}
		node := &frame.Nodes[i]
		switch name {
		case "up":
			if !telemetry.ValidStatusValue("converge_agent_haproxy_up", value) {
				invalid[i] = true
				continue
			}
			node.ScrapeOK = value == 1
		case "observed_at":
			if !telemetry.ValidStatusValue("converge_agent_scrape_timestamp_seconds", value) {
				invalid[i] = true
				continue
			}
			at := time.UnixMilli(int64(value * 1000)).UTC()
			node.ObservedAt = &at
		default:
			metric := map[string]string{"series": name}
			if code != "" {
				metric["code"] = code
			}
			if proxy != "" {
				metric["proxy"] = proxy
			}
			node.Series = append(node.Series, LiveSeries{Metric: metric, Value: value})
			if len(node.Series) > 62 {
				invalid[i] = true
			}
		}
	}
	for i := range frame.Nodes {
		n := &frame.Nodes[i]
		if invalid[i] {
			*n = LiveNode{NodeID: n.NodeID, Series: []LiveSeries{}}
			continue
		}
		if n.ObservedAt != nil {
			age := time.Since(*n.ObservedAt)
			n.Fresh = age >= -time.Minute && age <= 15*time.Second
		}
		if !n.Fresh || !n.ScrapeOK {
			n.Series = []LiveSeries{}
		}
	}
	if len(invalid) > 0 {
		return frame, errPartialFleetObservations
	}
	return frame, nil
}

func (s *Server) clusterEvents(w http.ResponseWriter, r *http.Request, org string) {
	query := r.URL.Query()
	if len(query) != 1 || len(query["nodes"]) != 1 || len(query.Get("nodes")) > store.MaxLiveNodes*37 {
		fail(w, 400, "select up to 12 cluster nodes")
		return
	}
	ids := strings.Split(query.Get("nodes"), ",")
	persistence, ok := s.config.Store.(store.ClusterNodesStore)
	if !ok {
		fail(w, 503, "cluster inventory unavailable")
		return
	}
	cluster, err := s.config.Store.GetCluster(r.Context(), org, r.PathValue("cluster"))
	if err != nil {
		storeError(w, err)
		return
	}
	if _, err := persistence.GetClusterNodes(r.Context(), org, cluster.ID, ids); err != nil {
		storeError(w, err)
		return
	}
	if !acquire(s.streams) {
		fail(w, 429, "stream capacity reached")
		return
	}
	defer func() { <-s.streams }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	// Leave headroom for collection and storage visibility inside the five-second
	// live-view budget. Each tick still performs one bounded cluster query.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	controller := http.NewResponseController(w)
	for {
		if !s.canReadOrganization(r, org) {
			return
		}
		frameCtx, cancelFrame := context.WithTimeout(ctx, 3*time.Second)
		// Re-read the bounded node set in one database query per authorization pass.
		nodes, err := persistence.GetClusterNodes(frameCtx, org, cluster.ID, ids)
		if err != nil {
			cancelFrame()
			return
		}
		currentCluster, err := s.config.Store.GetCluster(frameCtx, org, cluster.ID)
		if err != nil {
			cancelFrame()
			return
		}
		frame, queryErr := s.clusterLive(frameCtx, currentCluster, nodes)
		checked, checkErr := persistence.GetClusterNodes(frameCtx, org, cluster.ID, ids)
		cancelFrame()
		if checkErr != nil || !s.canReadOrganization(r, org) {
			return
		}
		for i, n := range checked {
			if n.RevokedAt != nil {
				frame.Nodes[i] = LiveNode{NodeID: n.ID, Revoked: true, Series: []LiveSeries{}}
			}
		}
		event := "metrics"
		var payload any = frame
		if queryErr != nil && !errors.Is(queryErr, errPartialFleetObservations) {
			event = "unavailable"
			payload = map[string]string{"cluster_id": cluster.ID, "error": "live observations unavailable"}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); err != nil {
			return
		}
		if controller.Flush() != nil {
			return
		}
		_ = controller.SetWriteDeadline(time.Time{})
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
