package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

func TestInvalidAgentStatusNeverReachesLiveOrHistoricalStorage(t *testing.T) {
	for name, mutate := range map[string]func(*prompb.WriteRequest){
		"nonboolean status":    func(b *prompb.WriteRequest) { b.Timeseries[1].Samples[0].Value = 2 },
		"zero collection time": func(b *prompb.WriteRequest) { b.Timeseries[0].Samples[0].Value = 0 },
		"extra status label": func(b *prompb.WriteRequest) {
			b.Timeseries[1].Labels = append(b.Timeseries[1].Labels, prompb.Label{Name: "proxy", Value: "invented"})
		},
		"label-distinct singleton": func(b *prompb.WriteRequest) {
			extra := b.Timeseries[1]
			extra.Labels = []prompb.Label{{Name: "__name__", Value: "converge_agent_haproxy_up"}, {Name: "proxy", Value: "invented"}}
			b.Timeseries = append(b.Timeseries, extra)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var writes atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writes.Add(1); w.WriteHeader(204) }))
			defer upstream.Close()
			f := newFixture(t, "manager", upstream.URL)
			node, leaf, _, _ := f.enrollNode(t)
			f.server.config.Role = "ingest"
			var err error
			f.server, err = New(f.server.config)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UnixMilli()
			batch := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
				{Labels: []prompb.Label{{Name: "__name__", Value: "converge_agent_scrape_timestamp_seconds"}}, Samples: []prompb.Sample{{Timestamp: at, Value: float64(at) / 1000}}},
				{Labels: []prompb.Label{{Name: "__name__", Value: "converge_agent_haproxy_up"}}, Samples: []prompb.Sample{{Timestamp: at, Value: 1}}},
			}}
			mutate(batch)
			raw, err := batch.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if got := nodeRequest(f, leaf, snappy.Encode(nil, raw)); got.Code != 400 {
				t.Fatal("invalid singleton batch accepted", got.Code)
			}
			live, err := f.store.GetLive(t.Context(), f.org, f.cluster.ID, []string{node.ID})
			if err != nil || len(live) != 0 || writes.Load() != 0 {
				t.Fatal("invalid batch crossed a storage boundary", err, live, writes.Load())
			}
			// A valid batch from the same authenticated node still works afterward.
			batch.Timeseries = []prompb.TimeSeries{
				{Labels: []prompb.Label{{Name: "__name__", Value: "converge_agent_scrape_timestamp_seconds"}}, Samples: []prompb.Sample{{Timestamp: at, Value: float64(at) / 1000}}},
				{Labels: []prompb.Label{{Name: "__name__", Value: "converge_agent_haproxy_up"}}, Samples: []prompb.Sample{{Timestamp: at, Value: 1}}},
			}
			raw, err = batch.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if got := nodeRequest(f, leaf, snappy.Encode(nil, raw)); got.Code != 204 {
				t.Fatal("valid control rejected", got.Code)
			}
			live, err = f.store.GetLive(t.Context(), f.org, f.cluster.ID, []string{node.ID})
			if err != nil || !live[node.ID].Current.ScrapeOK || writes.Load() != 1 {
				t.Fatal("valid control was not recorded", err)
			}
		})
	}
}

func TestLegacyLiveFallbackKeepsValidPeersAndChecksEveryScope(t *testing.T) {
	for _, mode := range []string{"duplicate status", "invalid status", "unencodable collection time", "extra labels", "overflow", "late foreign scope"} {
		t.Run(mode, func(t *testing.T) {
			var attacker, peer domain.Node
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				points := livePoints(attacker, peer)
				switch mode {
				case "duplicate status":
					points = append(points, points[0])
				case "invalid status":
					points[0] = livePoint(attacker, "up", "2")
				case "unencodable collection time":
					points[1] = livePoint(attacker, "observed_at", "1000000000000")
				case "extra labels":
					points[0].(map[string]any)["metric"].(map[string]string)["extra"] = "poison"
				case "overflow":
					for range 65 {
						points = append(points, points[0])
					}
				case "late foreign scope":
					points[0] = livePoint(attacker, "up", "2")
					foreign := livePoint(attacker, "up", "1")
					foreign["metric"].(map[string]string)["organization_id"] = domain.NewID()
					points = append(points, foreign)
				}
				fleetVector(w, points)
			}))
			defer upstream.Close()
			f := newFixture(t, "manager", upstream.URL)
			attacker, peer = enrollForFleet(t, f, f.org, f.cluster.ID), enrollForFleet(t, f, f.org, f.cluster.ID)
			frame, err := f.server.clusterLive(t.Context(), f.cluster, []domain.Node{attacker, peer})
			if err == nil {
				t.Fatal("poisoned live fallback was not reported")
			}
			if mode == "late foreign scope" {
				for _, n := range frame.Nodes {
					if n.Fresh || n.ScrapeOK || len(n.Series) != 0 {
						t.Fatal("foreign scope leaked live data", frame)
					}
				}
				return
			}
			if frame.Nodes[0].Fresh || frame.Nodes[0].ScrapeOK || len(frame.Nodes[0].Series) != 0 || !frame.Nodes[1].Fresh || !frame.Nodes[1].ScrapeOK || len(frame.Nodes[1].Series) != 1 {
				t.Fatal("one legacy node hid its live peer", err, frame)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			request := httptest.NewRequest("GET", "/v1/clusters/"+f.cluster.ID+"/events?nodes="+attacker.ID+","+peer.ID, nil).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer "+operatorToken)
			request.Header.Set("X-Organization-ID", f.org)
			response := httptest.NewRecorder()
			f.server.ServeHTTP(response, request)
			if !strings.Contains(response.Body.String(), "event: metrics") || strings.Contains(response.Body.String(), "event: unavailable") {
				t.Fatal("partial live frame replaced every node with unavailable", response.Body.String())
			}
		})
	}
}

type capturedIncidentStore struct {
	*store.Memory
	telemetry domain.IncidentTelemetry
}

func (s *capturedIncidentStore) ReconcileIncidents(_ context.Context, _, _ string, telemetry domain.IncidentTelemetry, _ time.Time) error {
	s.telemetry = telemetry
	return nil
}

func TestHistoricalStatusPoisoningDoesNotHidePeersOrSuppressIncidents(t *testing.T) {
	for name, mutate := range map[string]func(domain.Node, []any) []any{
		"invalid numeric status": func(n domain.Node, p []any) []any { p[0] = vectorPoint(n, "converge_agent_haproxy_up", "2"); return p },
		"invalid collection time": func(n domain.Node, p []any) []any {
			p[1] = vectorPoint(n, "converge_agent_scrape_timestamp_seconds", "NaN")
			return p
		},
		"unencodable collection time": func(n domain.Node, p []any) []any {
			p[1] = vectorPoint(n, "converge_agent_scrape_timestamp_seconds", "1000000000000")
			return p
		},
		"extra labels": func(_ domain.Node, p []any) []any {
			p[0].(map[string]any)["metric"].(map[string]string)["proxy"] = "invented"
			return p
		},
		"more duplicates than fleet-wide allowance": func(_ domain.Node, p []any) []any { return append(p, p[0], p[0], p[0]) },
		"malformed value": func(_ domain.Node, p []any) []any { p[0].(map[string]any)["value"] = []any{}; return p },
	} {
		t.Run(name, func(t *testing.T) {
			var attacker, peer domain.Node
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				at := strconv.FormatInt(time.Now().Unix(), 10)
				points := []any{vectorPoint(attacker, "converge_agent_haproxy_up", "1"), vectorPoint(attacker, "converge_agent_scrape_timestamp_seconds", at), vectorPoint(peer, "converge_agent_haproxy_up", "1"), vectorPoint(peer, "converge_agent_scrape_timestamp_seconds", at)}
				fleetVector(w, mutate(attacker, points))
			}))
			defer upstream.Close()
			f := newFixture(t, "manager", upstream.URL)
			attacker, peer = enrollForFleet(t, f, f.org, f.cluster.ID), enrollForFleet(t, f, f.org, f.cluster.ID)
			got := f.request("GET", "/v1/fleet", nil, true)
			var response fleetResponse
			if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &response) != nil || !response.Partial || len(response.Nodes) != 2 {
				t.Fatal(got.Body.String())
			}
			for _, n := range response.Nodes {
				if n.ID == peer.ID && (!n.MetricsAvailable || !n.TelemetryFresh || !n.ScrapeOK || n.ObservedAt == nil) {
					t.Fatal("one node hid its healthy peer", got.Body.String())
				}
				if n.ID == attacker.ID && (n.MetricsAvailable || n.ObservedAt != nil) {
					t.Fatal("ambiguous status was published", got.Body.String())
				}
			}
			captured := &capturedIncidentStore{Memory: f.store}
			f.server.config.Store = captured
			if err := f.server.evaluateIncidentCluster(t.Context(), f.cluster); err != nil {
				t.Fatal(err)
			}
			if !captured.telemetry.Available || !captured.telemetry.Nodes[peer.ID].ScrapeKnown || !captured.telemetry.Nodes[peer.ID].ScrapeOK || captured.telemetry.Nodes[peer.ID].ObservedAtMS == 0 {
				t.Fatal("node poison suppressed peer incident evaluation", captured.telemetry)
			}
			if _, exists := captured.telemetry.Nodes[attacker.ID]; exists {
				t.Fatal("invalid node was passed to incident evaluation")
			}
		})
	}
}

func TestFleetChecksAllScopesBeforePublishingPeerStatus(t *testing.T) {
	for _, scope := range []string{"organization_id", "cluster_id", "node_id"} {
		t.Run(scope, func(t *testing.T) {
			var attacker, peer domain.Node
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				foreign := vectorPoint(attacker, "converge_agent_scrape_timestamp_seconds", "1")
				foreign["metric"].(map[string]string)[scope] = domain.NewID()
				fleetVector(w, []any{vectorPoint(peer, "converge_agent_haproxy_up", "1"), vectorPoint(peer, "converge_agent_scrape_timestamp_seconds", strconv.FormatInt(time.Now().Unix(), 10)), vectorPoint(attacker, "converge_agent_haproxy_up", "2"), foreign})
			}))
			defer upstream.Close()
			f := newFixture(t, "manager", upstream.URL)
			attacker, peer = enrollForFleet(t, f, f.org, f.cluster.ID), enrollForFleet(t, f, f.org, f.cluster.ID)
			got := f.request("GET", "/v1/fleet", nil, true)
			var response fleetResponse
			if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &response) != nil || !response.Partial || len(response.Nodes) != 2 {
				t.Fatal(got.Body.String())
			}
			for _, n := range response.Nodes {
				if n.MetricsAvailable || n.ObservedAt != nil {
					t.Fatal("storage scope mismatch published status", got.Body.String())
				}
			}
			captured := &capturedIncidentStore{Memory: f.store}
			f.server.config.Store = captured
			if err := f.server.evaluateIncidentCluster(t.Context(), f.cluster); err != nil {
				t.Fatal(err)
			}
			if captured.telemetry.Available || len(captured.telemetry.Nodes) != 0 {
				t.Fatal("global scope failure looked like a node-local gap", captured.telemetry)
			}
		})
	}
}
