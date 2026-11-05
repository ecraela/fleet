package telemetry

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

func sample() *prompb.WriteRequest {
	return &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{{Labels: []prompb.Label{{Name: "__name__", Value: "haproxy_backend_http_responses_total"}, {Name: "code", Value: "2xx"}, {Name: "proxy", Value: "api"}}, Samples: []prompb.Sample{{Value: 41, Timestamp: time.Now().UnixMilli()}}}}}
}

func TestRemoteWriteRoundTripAndAuthenticatedStamp(t *testing.T) {
	req := sample()
	b, err := Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Timeseries[0].Samples[0].Value != 41 {
		t.Fatal("lost sample")
	}
	stamped, err := Stamp(decoded, Scope{OrganizationID: "organization-a", ClusterID: "cluster-a", NodeID: "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Timeseries[0].Labels) != 3 {
		t.Fatal("stamp mutated untrusted input")
	}
	if _, err := EncodeStamped(stamped); err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(stamped); err == nil {
		t.Fatal("node encoder accepted reserved labels")
	}
	if _, err := Decode(bytes.NewReader(mustMarshal(t, stamped))); err == nil {
		t.Fatal("ingestion accepted sender stamped identity")
	}
}

func mustMarshal(t *testing.T, req *prompb.WriteRequest) []byte {
	t.Helper()
	b, err := req.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return snappy.Encode(nil, b)
}

func TestRejectUntrustedMetricContracts(t *testing.T) {
	for name, mutate := range map[string]func(*prompb.WriteRequest){
		"reserved": func(r *prompb.WriteRequest) {
			r.Timeseries[0].Labels = append(r.Timeseries[0].Labels, prompb.Label{Name: "tenant_id", Value: "other"})
		},
		"duplicate labels": func(r *prompb.WriteRequest) { r.Timeseries[0].Labels[2].Name = "code" },
		"long label":       func(r *prompb.WriteRequest) { r.Timeseries[0].Labels[2].Value = strings.Repeat("a", MaxLabelLength+1) },
		"nonfinite":        func(r *prompb.WriteRequest) { r.Timeseries[0].Samples[0].Value = math.Inf(1) },
		"extra sample": func(r *prompb.WriteRequest) {
			r.Timeseries[0].Samples = append(r.Timeseries[0].Samples, r.Timeseries[0].Samples[0])
		},
		"unsupported":      func(r *prompb.WriteRequest) { r.Timeseries[0].Labels[0].Value = "arbitrary_custom_metric" },
		"duplicate series": func(r *prompb.WriteRequest) { r.Timeseries = append(r.Timeseries, r.Timeseries[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			r := sample()
			mutate(r)
			if _, err := Decode(bytes.NewReader(mustMarshal(t, r))); err == nil {
				t.Fatal("accepted invalid batch")
			}
		})
	}
}

func TestAgentStatusSingletonContract(t *testing.T) {
	at := time.Now().UnixMilli()
	status := func(name string, value float64) *prompb.WriteRequest {
		return &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{{Labels: []prompb.Label{{Name: "__name__", Value: name}}, Samples: []prompb.Sample{{Timestamp: at, Value: value}}}}}
	}
	for name, request := range map[string]*prompb.WriteRequest{
		"invalid up":          status("converge_agent_haproxy_up", 2),
		"negative up":         status("converge_agent_haproxy_up", -1),
		"zero timestamp":      status("converge_agent_scrape_timestamp_seconds", 0),
		"huge timestamp":      status("converge_agent_scrape_timestamp_seconds", 1e12+1),
		"unrelated timestamp": status("converge_agent_scrape_timestamp_seconds", float64(at)/1000-120),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(bytes.NewReader(mustMarshal(t, request))); err == nil {
				t.Fatal("invalid singleton entered ingestion")
			}
		})
	}
	for _, name := range []string{"converge_agent_haproxy_up", "converge_agent_scrape_timestamp_seconds"} {
		for _, duplicate := range []bool{false, true} {
			r := status(name, 1)
			if name == "converge_agent_scrape_timestamp_seconds" {
				r.Timeseries[0].Samples[0].Value = float64(at) / 1000
			}
			labeled := r.Timeseries[0]
			labeled.Labels = append(append([]prompb.Label{}, labeled.Labels...), prompb.Label{Name: "variant", Value: "poison"})
			if duplicate {
				r.Timeseries = append(r.Timeseries, labeled)
			} else {
				r.Timeseries[0] = labeled
			}
			if _, err := Decode(bytes.NewReader(mustMarshal(t, r))); err == nil {
				t.Fatal("label-distinct singleton was accepted", name, duplicate)
			}
		}
	}
	// Legacy agents may report status without the newer collection metric.
	for _, up := range []float64{0, 1} {
		r := status("converge_agent_haproxy_up", up)
		encoded, err := Encode(r)
		if err != nil {
			t.Fatal("legacy status rejected", err)
		}
		if _, err = Decode(bytes.NewReader(encoded)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentStatusCollectionTimesRemainJSONEncodable(t *testing.T) {
	for _, value := range []float64{0, -1, 253402300800, 1e12} {
		if ValidStatusValue("converge_agent_scrape_timestamp_seconds", value) {
			t.Fatal("unencodable collection time accepted", value)
		}
	}
	for _, value := range []float64{0.001, float64(time.Now().UnixMilli()) / 1000, 253402300799} {
		if !ValidStatusValue("converge_agent_scrape_timestamp_seconds", value) {
			t.Fatal("valid collection time rejected", value)
		}
		at := time.UnixMilli(int64(value * 1000)).UTC()
		if _, err := at.MarshalJSON(); err != nil {
			t.Fatal("valid status cannot be returned as JSON", err)
		}
	}
}

func TestDecodeBoundsBeforeExpansion(t *testing.T) {
	oversized := snappy.Encode(nil, bytes.Repeat([]byte{0}, MaxDecoded+1))
	if _, err := Decode(bytes.NewReader(oversized)); err == nil {
		t.Fatal("accepted compression bomb")
	}
	if _, err := Decode(bytes.NewReader(bytes.Repeat([]byte{0}, MaxCompressed+1))); err == nil {
		t.Fatal("accepted oversized body")
	}
	var many bytes.Buffer
	for range MaxSeries + 1 {
		many.Write([]byte{0x0a, 0x00})
	}
	if _, err := Decode(bytes.NewReader(snappy.Encode(nil, many.Bytes()))); err == nil {
		t.Fatal("accepted excessive empty series")
	}
	// An attacker cannot encode a huge label length and cause allocation.
	var malformed bytes.Buffer
	malformed.WriteByte(0x0a)
	var length [10]byte
	n := binary.PutUvarint(length[:], math.MaxUint64)
	malformed.Write(length[:n])
	if _, err := Decode(bytes.NewReader(snappy.Encode(nil, malformed.Bytes()))); err == nil {
		t.Fatal("accepted invalid length")
	}
}

func TestCollectorFailureDoesNotFabricateTraffic(t *testing.T) {
	for name, body := range map[string]string{
		"malformed":  "this isn't prometheus\n",
		"reserved":   "haproxy_backend_status{node_id=\"spoof\"} 1\n",
		"too many":   strings.Repeat("haproxy_backend_status 1\n", MaxSeries+1),
		"long value": "haproxy_backend_status{proxy=\"" + strings.Repeat("a", 257) + "\"} 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			c := Collector{URL: srv.URL}
			req, err := c.Collect(context.Background(), time.Now())
			if err == nil {
				t.Fatal("expected scrape failure")
			}
			if len(req.Timeseries) != 4 {
				t.Fatalf("unexpected fabricated metrics: %d", len(req.Timeseries))
			}
			for _, ts := range req.Timeseries {
				if strings.HasPrefix(ts.Labels[0].Value, "haproxy_") {
					t.Fatal("failed scrape produced HAProxy zeros")
				}
				if ts.Labels[0].Value == "converge_agent_haproxy_up" && ts.Samples[0].Value != 0 {
					t.Fatal("failed scrape was up")
				}
			}
		})
	}
}

func TestCollectorPreservesSupportedLabelsAndUsesCaptureTimestamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "# TYPE haproxy_backend_http_responses_total counter\nhaproxy_backend_http_responses_total{proxy=\"api\",code=\"5xx\"} 32 1000\nother_metric 9\n")
	}))
	defer srv.Close()
	now := time.Now()
	c := Collector{URL: srv.URL}
	req, err := c.Collect(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Timeseries) != 5 {
		t.Fatal("wrong metric count")
	}
	for _, ts := range req.Timeseries {
		if ts.Labels[0].Value == "haproxy_backend_http_responses_total" {
			if ts.Samples[0].Timestamp != now.UnixMilli() || ts.Samples[0].Value != 32 || ts.Labels[1].Value != "5xx" {
				t.Fatal("lost samples or labels")
			}
		}
	}
}

func TestCollectorBoundsResponseAndRequestTime(t *testing.T) {
	for _, slow := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slow {
				<-r.Context().Done()
				return
			}
			fmt.Fprint(w, strings.Repeat("#", MaxScrape+1))
		}))
		c := Collector{URL: srv.URL, Client: &http.Client{Timeout: 30 * time.Millisecond}}
		if _, err := c.Collect(context.Background(), time.Now()); err == nil {
			t.Fatal("accepted oversized or stalled scrape")
		}
		srv.Close()
	}
}

func TestNativeHAProxyInformationAndUndefinedGauge(t *testing.T) {
	// Native HAProxy 3.4 exposition contains a node-labelled information
	// family and NaN for settings that have no configured numeric limit.
	const native = `# HELP haproxy_process_node HAProxy node name
# TYPE haproxy_process_node gauge
haproxy_process_node{node="local-load-balancer"} 1
# HELP haproxy_server_limit_sessions Configured maximum number of connections
# TYPE haproxy_server_limit_sessions gauge
haproxy_server_limit_sessions{proxy="application",server="app"} NaN
# HELP haproxy_backend_status Current status of the service
# TYPE haproxy_backend_status gauge
haproxy_backend_status{proxy="application",state="DOWN"} 0
haproxy_backend_status{proxy="application",state="UP"} 1
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, native) }))
	defer srv.Close()
	c := Collector{URL: srv.URL}
	req, err := c.Collect(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Timeseries) != 6 {
		t.Fatalf("expected two status series and four agent series, got %d", len(req.Timeseries))
	}
	for _, ts := range req.Timeseries {
		name := ts.Labels[0].Value
		if name == "haproxy_process_node" || name == "haproxy_server_limit_sessions" {
			t.Fatal("exported information or undefined value")
		}
		if name == "converge_agent_haproxy_up" && ts.Samples[0].Value != 1 {
			t.Fatal("native scrape marked unavailable")
		}
	}
}
