package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

// Use a dedicated test store: this intentionally recreates series accepted by
// older ingestion. No invalid batch is sent through the current ingest API.
func TestFleetStorageBoundsLegacySingletonCardinality(t *testing.T) {
	endpoint := os.Getenv("HAPROXY_TEST_METRICS_URL")
	if endpoint == "" {
		t.Skip("set HAPROXY_TEST_METRICS_URL to an isolated VictoriaMetrics instance")
	}
	f := newFixture(t, "manager", endpoint)
	attacker, peer := enrollForFleet(t, f, f.org, f.cluster.ID), enrollForFleet(t, f, f.org, f.cluster.ID)
	at := time.Now().UnixMilli()
	var body bytes.Buffer
	encode := json.NewEncoder(&body)
	write := func(node domain.Node, name string, value float64, labels map[string]string) {
		t.Helper()
		metric := map[string]string{"__name__": name, "organization_id": node.OrganizationID, "cluster_id": node.ClusterID, "node_id": node.ID}
		for key, value := range labels {
			metric[key] = value
		}
		if err := encode.Encode(map[string]any{"metric": metric, "values": []float64{value}, "timestamps": []int64{at}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 1000 {
		labels := map[string]string{}
		for j := range 8 {
			labels[fmt.Sprintf("label%d", j)] = fmt.Sprintf("%04d", i) + strings.Repeat("x", 252)
		}
		write(attacker, "converge_agent_haproxy_up", 1, labels)
	}
	write(peer, "converge_agent_haproxy_up", 1, nil)
	write(peer, "converge_agent_scrape_timestamp_seconds", float64(at)/1000, nil)
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/api/v1/import"
	request, err := http.NewRequestWithContext(t.Context(), "POST", u.String(), &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("legacy fixture import failed", response.StatusCode)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		nodes := []FleetNode{{Node: attacker}, {Node: peer}}
		err := f.server.fleetObservations(t.Context(), f.org, nodes)
		if err == nil && nodes[1].MetricsAvailable && nodes[1].ObservedAt != nil {
			if !nodes[1].ScrapeOK || nodes[0].MetricsAvailable || nodes[0].ObservedAt != nil {
				t.Fatal("legacy singleton selection published ambiguous status", nodes)
			}
			frame, err := f.server.clusterLive(t.Context(), f.cluster, []domain.Node{attacker, peer})
			if err != nil || !frame.Nodes[1].Fresh || !frame.Nodes[1].ScrapeOK || frame.Nodes[0].ScrapeOK {
				t.Fatal("legacy singleton cardinality hid the history-backed live peer", err, frame)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("legacy cardinality hid an honest peer", err, nodes)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
