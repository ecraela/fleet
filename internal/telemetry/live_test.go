package telemetry

import (
	"testing"
	"time"

	"github.com/prometheus/prometheus/prompb"
)

func TestLiveSummaryPreservesZeroAndIgnoresUnrelatedMetrics(t *testing.T) {
	at := time.Now().UnixMilli()
	series := func(name, proxy, code string, value float64) prompb.TimeSeries {
		labels := []prompb.Label{{Name: "__name__", Value: name}}
		if code != "" {
			labels = append(labels, prompb.Label{Name: "code", Value: code})
		}
		if proxy != "" {
			labels = append(labels, prompb.Label{Name: "proxy", Value: proxy})
		}
		return prompb.TimeSeries{Labels: labels, Samples: []prompb.Sample{{Timestamp: at, Value: value}}}
	}
	batch := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series("converge_agent_scrape_timestamp_seconds", "", "", float64(at)/1000),
		series("converge_agent_haproxy_up", "", "", 1),
		series("haproxy_frontend_bytes_out_total", "public", "", 0),
		series("haproxy_frontend_http_responses_total", "public", "3xx", 4),
		series("haproxy_backend_response_time_average_seconds", "app", "", .012),
		series("haproxy_frontend_limit_sessions", "public", "", -1),
	}}
	if err := Validate(batch); err != nil {
		t.Fatal(err)
	}
	got := LiveSummary(batch)
	if got == nil || !got.ScrapeOK || got.Frontends["public"].BytesOut == nil || *got.Frontends["public"].BytesOut != 0 || got.LatencyMS["app"] != 12 {
		t.Fatal("summary changed metrics", got)
	}
	if got.AtMS < at-1 || got.AtMS > at {
		t.Fatal("collection timestamp changed")
	}
	batch.Timeseries = batch.Timeseries[1:]
	if LiveSummary(batch) != nil {
		t.Fatal("invented collection time for legacy batch")
	}
}
