package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/telemetry"
	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

func floatPointer(value float64) *float64 { return &value }

func TestLatestSeriesRatesResetMissingSamplesAndFrontendScope(t *testing.T) {
	before := domain.LiveObservation{AtMS: 1000, ScrapeOK: true, Frontends: map[string]domain.LiveFrontend{
		"public":     {BytesOut: floatPointer(100), Responses: map[string]float64{"2xx": 100}},
		"statistics": {BytesOut: floatPointer(1000), Responses: map[string]float64{"2xx": 1000}},
	}}
	after := domain.LiveObservation{AtMS: 2000, ScrapeOK: true, Connections: floatPointer(9), Frontends: map[string]domain.LiveFrontend{
		"public":     {BytesOut: floatPointer(20), Connections: floatPointer(2), Responses: map[string]float64{"2xx": 3}},
		"statistics": {BytesOut: floatPointer(9000), Connections: floatPointer(7), Responses: map[string]float64{"2xx": 9000}},
	}, LatencyMS: map[string]float64{"app": 12}}
	pair := domain.LivePair{Current: after, Previous: &before}
	read := func(pair domain.LivePair, frontend string) map[string]float64 {
		series, err := latestSeries(pair, frontend)
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]float64{}
		for _, point := range series {
			values[point.Metric["series"]+point.Metric["code"]+point.Metric["proxy"]] = point.Value
		}
		return values
	}
	values := read(pair, "public")
	if values["traffic_out"] != 160 || values["http_responses2xx"] != 3 || values["connections"] != 2 || values["latencyapp"] != 12 {
		t.Fatal("reset or frontend scope incorrect", values)
	}
	if read(pair, "")["connections"] != 9 {
		t.Fatal("process gauge not selected")
	}
	pair.Previous = nil
	if _, ok := read(pair, "public")["traffic_out"]; ok {
		t.Fatal("invented rate without a previous sample")
	}
	before.AtMS = -20000
	pair.Previous = &before
	if _, ok := read(pair, "public")["traffic_out"]; ok {
		t.Fatal("invented rate across a collection gap")
	}
}

func TestLatestFeedDoesNotWaitForHistoryAndRespectsRevocation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("latest feed reached historical storage")
		w.WriteHeader(503)
	}))
	defer upstream.Close()
	f := newFixture(t, "manager", upstream.URL)
	node := enrollForFleet(t, f, f.org, f.cluster.ID)
	observation := domain.LiveObservation{AtMS: time.Now().UnixMilli(), ScrapeOK: true, Connections: floatPointer(3)}
	if err := f.store.RecordLive(t.Context(), node, observation); err != nil {
		t.Fatal(err)
	}
	frame, err := f.server.clusterLive(t.Context(), f.cluster, []domain.Node{node})
	if err != nil || !frame.Nodes[0].Fresh || !frame.Nodes[0].ScrapeOK || len(frame.Nodes[0].Series) != 1 {
		t.Fatal("latest feed unavailable", err, frame)
	}
	if err := f.store.RevokeNode(t.Context(), f.org, node.ID); err != nil {
		t.Fatal(err)
	}
	node, err = f.store.GetNode(t.Context(), f.org, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	frame, err = f.server.clusterLive(t.Context(), f.cluster, []domain.Node{node})
	if err != nil || !frame.Nodes[0].Revoked || frame.Nodes[0].Fresh || len(frame.Nodes[0].Series) != 0 {
		t.Fatal("revoked node exposed", err, frame)
	}
}

func TestValidatedIngestPublishesLiveWithoutAcknowledgingFailedHistory(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer upstream.Close()
	f := newFixture(t, "manager", upstream.URL)
	node, leaf, _, _ := f.enrollNode(t)
	f.server.config.Role = "ingest"
	ingest, err := New(f.server.config)
	if err != nil {
		t.Fatal(err)
	}
	f.server = ingest
	at := time.Now().UnixMilli()
	batch := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		{Labels: []prompb.Label{{Name: "__name__", Value: "converge_agent_scrape_timestamp_seconds"}}, Samples: []prompb.Sample{{Timestamp: at, Value: float64(at) / 1000}}},
		{Labels: []prompb.Label{{Name: "__name__", Value: "converge_agent_haproxy_up"}}, Samples: []prompb.Sample{{Timestamp: at, Value: 1}}},
	}}
	payload, err := telemetry.Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	if code := nodeRequest(f, leaf, payload).Code; code != 503 {
		t.Fatal("failed historical write acknowledged", code)
	}
	rows, err := f.store.GetLive(t.Context(), f.org, f.cluster.ID, []string{node.ID})
	if err != nil || !rows[node.ID].Current.ScrapeOK {
		t.Fatal("validated live observation missing", err)
	}
	before := rows[node.ID].Current.AtMS
	batch.Timeseries[0].Labels = append(batch.Timeseries[0].Labels, prompb.Label{Name: "organization_id", Value: domain.NewID()})
	raw, _ := batch.Marshal()
	// Use the raw wire encoder deliberately: the normal encoder rejects spoofed identity labels.
	invalid := snappy.Encode(nil, raw)
	if code := nodeRequest(f, leaf, invalid).Code; code != 400 {
		t.Fatal("spoofed scope accepted", code)
	}
	rows, err = f.store.GetLive(t.Context(), f.org, f.cluster.ID, []string{node.ID})
	if err != nil || rows[node.ID].Current.AtMS != before {
		t.Fatal("invalid batch changed latest state", err)
	}
}
