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
)

type historyWindow struct {
	duration time.Duration
	step     time.Duration
}

var historyWindows = map[string]historyWindow{
	"15m": {15 * time.Minute, 5 * time.Second},
	"1h":  {time.Hour, 15 * time.Second},
	"6h":  {6 * time.Hour, time.Minute},
	"24h": {24 * time.Hour, 4 * time.Minute},
}

type HistorySeries struct {
	Metric map[string]string `json:"metric"`
	Values [][2]float64      `json:"values"`
}

type History struct {
	NodeID      string          `json:"node_id"`
	Range       string          `json:"range"`
	Start       int64           `json:"start"`
	End         int64           `json:"end"`
	StepSeconds int64           `json:"step_seconds"`
	Series      []HistorySeries `json:"series"`
}

func (s *Server) nodeHistory(w http.ResponseWriter, r *http.Request, org string) {
	node, err := s.config.Store.GetNode(r.Context(), org, r.PathValue("node"))
	if err != nil {
		storeError(w, err)
		return
	}
	if node.RevokedAt != nil {
		storeError(w, domain.ErrRevoked)
		return
	}
	requested := r.URL.Query()
	window := requested.Get("range")
	if window == "" {
		window = "1h"
	}
	config, ok := historyWindows[window]
	if !ok || len(requested) > 1 || len(requested["range"]) > 1 || (len(requested) == 1 && requested["range"] == nil) {
		fail(w, 400, "choose a history range: 15m, 1h, 6h or 24h")
		return
	}
	result, err := s.history(r.Context(), node, window, config)
	if err != nil {
		fail(w, 503, "history query unavailable")
		return
	}
	// A query may race revocation or a short-lived browser session expiring.
	if !s.canReadOrganization(r, org) {
		fail(w, 401, "valid organization session required")
		return
	}
	node, err = s.config.Store.GetNode(r.Context(), org, node.ID)
	if err != nil || node.RevokedAt != nil {
		fail(w, 403, "node access revoked")
		return
	}
	writeJSON(w, 200, result)
}

func scopedHistoryQuery(node domain.Node, step time.Duration, frontend string) string {
	scope := fmt.Sprintf(`organization_id="%s",cluster_id="%s",node_id="%s"`, node.OrganizationID, node.ClusterID, node.ID)
	return observationQuery(scope, time.Duration(max(30, int(step.Seconds())*2))*time.Second, frontend)
}

func observationQuery(scope string, window time.Duration, frontend string) string {
	trafficScope := scope
	connections := "haproxy_process_current_connections"
	if frontend != "" {
		trafficScope += fmt.Sprintf(",proxy=%q", frontend)
		connections = "haproxy_frontend_current_sessions"
	}
	identity := "organization_id,cluster_id,node_id"
	// Gate every evaluation using the agent's actual collection timestamp. A
	// storage lookback must not turn a collection gap into zero or a flat line.
	up := singletonStatusQuery(fmt.Sprintf(`converge_agent_haproxy_up{%s}`, scope))
	collected := singletonStatusQuery(fmt.Sprintf(`converge_agent_scrape_timestamp_seconds{%s}`, scope))
	health := fmt.Sprintf(`((%s == 1) and on (%s) ((time() - (%s)) <= 15) and on (%s) ((time() - (%s)) >= -60))`, up, identity, collected, identity, collected)
	lookback := max(5, int(window.Seconds()))
	part := func(expression, name string) string {
		return fmt.Sprintf(`label_replace((%s) and on (%s) %s, "series", "%s", "", "")`, expression, identity, health, name)
	}
	return strings.Join([]string{
		part(fmt.Sprintf(`8 * sum by (%s) (rate(haproxy_frontend_bytes_in_total{%s}[%ds]))`, identity, trafficScope, lookback), "traffic_in"),
		part(fmt.Sprintf(`8 * sum by (%s) (rate(haproxy_frontend_bytes_out_total{%s}[%ds]))`, identity, trafficScope, lookback), "traffic_out"),
		part(fmt.Sprintf(`sum by (%s) (%s{%s})`, identity, connections, trafficScope), "connections"),
		part(fmt.Sprintf(`sum by (%s,code) (rate(haproxy_frontend_http_responses_total{%s}[%ds]))`, identity, trafficScope, lookback), "http_responses"),
		part(fmt.Sprintf(`1000 * avg by (%s,proxy) (haproxy_backend_response_time_average_seconds{%s})`, identity, scope), "latency"),
	}, " or ")
}

func (s *Server) history(ctx context.Context, node domain.Node, window string, config historyWindow) (History, error) {
	if !acquire(s.queries) {
		return History{}, errors.New("query capacity reached")
	}
	defer func() { <-s.queries }()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	step := int64(config.step.Seconds())
	end := time.Now().Unix() / step * step
	start := end - int64(config.duration.Seconds())
	u := s.storageURL("/api/v1/query_range")
	q := u.Query()
	q.Set("query", scopedHistoryQuery(node, config.step, s.trafficFrontend(ctx, node)))
	q.Set("start", strconv.FormatInt(start, 10))
	q.Set("end", strconv.FormatInt(end, 10))
	q.Set("step", strconv.FormatInt(step, 10))
	q.Set("timeout", "2s")
	q.Set("nocache", "1")
	q.Set("latency_offset", "1ms")
	u.RawQuery = q.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return History{}, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return History{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return History{}, errors.New("history query failed")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil || len(raw) > 2<<20 {
		return History{}, errors.New("history query response exceeds limit")
	}
	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string   `json:"metric"`
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Status != "success" || envelope.Data.ResultType != "matrix" || len(envelope.Data.Result) > 64 {
		return History{}, errors.New("invalid history matrix")
	}
	result := History{NodeID: node.ID, Range: window, Start: start, End: end, StepSeconds: step, Series: make([]HistorySeries, 0, len(envelope.Data.Result))}
	seen := make(map[string]bool)
	for _, series := range envelope.Data.Result {
		if series.Metric["organization_id"] != node.OrganizationID || series.Metric["cluster_id"] != node.ClusterID || series.Metric["node_id"] != node.ID || len(series.Values) > 361 {
			return History{}, errors.New("history scope or point limit mismatch")
		}
		name, code, proxy := series.Metric["series"], series.Metric["code"], series.Metric["proxy"]
		valid := (name == "traffic_in" || name == "traffic_out" || name == "connections") && code == "" && proxy == ""
		switch code {
		case "1xx", "2xx", "3xx", "4xx", "5xx", "other":
			valid = valid || (name == "http_responses" && proxy == "")
		}
		valid = valid || (name == "latency" && code == "" && proxy != "" && len(proxy) <= 255)
		key := name + "\x00" + code + "\x00" + proxy
		if !valid || seen[key] {
			return History{}, errors.New("invalid or duplicate history series")
		}
		seen[key] = true
		metric := map[string]string{"series": name}
		if code != "" {
			metric["code"] = code
		}
		if proxy != "" {
			metric["proxy"] = proxy
		}
		output := HistorySeries{Metric: metric, Values: make([][2]float64, 0, len(series.Values))}
		previous := float64(start) - 1
		for _, point := range series.Values {
			var at float64
			var text string
			if len(point) != 2 || json.Unmarshal(point[0], &at) != nil || json.Unmarshal(point[1], &text) != nil {
				return History{}, errors.New("invalid history point")
			}
			value, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || math.IsNaN(at) || math.IsInf(at, 0) || at < float64(start) || at > float64(end) || at <= previous {
				return History{}, errors.New("invalid history value or timestamp")
			}
			previous = at
			output.Values = append(output.Values, [2]float64{at, value})
		}
		result.Series = append(result.Series, output)
	}
	return result, nil
}
