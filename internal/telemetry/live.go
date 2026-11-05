package telemetry

import (
	"github.com/ecraela/fleet/internal/domain"
	"github.com/prometheus/prometheus/prompb"
)

// LiveSummary reduces an already validated batch without retaining arbitrary labels.
// Older agents without a collection timestamp continue through the history path.
func LiveSummary(batch *prompb.WriteRequest) *domain.LiveObservation {
	o := domain.LiveObservation{Frontends: map[string]domain.LiveFrontend{}, LatencyMS: map[string]float64{}}
	latencyCounts := map[string]int{}
	for _, series := range batch.Timeseries {
		name, proxy, code := "", "", ""
		for _, label := range series.Labels {
			switch label.Name {
			case "__name__":
				name = label.Value
			case "proxy":
				proxy = label.Value
			case "code":
				code = label.Value
			}
		}
		value := series.Samples[0].Value
		switch name {
		case "converge_agent_scrape_timestamp_seconds":
			if value <= 0 || value > 1e12 {
				return nil
			}
			o.AtMS = int64(value * 1000)
		case "converge_agent_haproxy_up":
			if value != 0 && value != 1 {
				return nil
			}
			o.ScrapeOK = value == 1
		case "haproxy_process_current_connections":
			if o.Connections == nil {
				o.Connections = new(float64)
			}
			*o.Connections += value
		case "haproxy_backend_response_time_average_seconds":
			o.LatencyMS[proxy] += value * 1000
			latencyCounts[proxy]++
		case "haproxy_frontend_bytes_in_total", "haproxy_frontend_bytes_out_total", "haproxy_frontend_current_sessions", "haproxy_frontend_http_responses_total":
			f := o.Frontends[proxy]
			switch name {
			case "haproxy_frontend_bytes_in_total":
				if f.BytesIn == nil {
					f.BytesIn = new(float64)
				}
				*f.BytesIn += value
			case "haproxy_frontend_bytes_out_total":
				if f.BytesOut == nil {
					f.BytesOut = new(float64)
				}
				*f.BytesOut += value
			case "haproxy_frontend_current_sessions":
				if f.Connections == nil {
					f.Connections = new(float64)
				}
				*f.Connections += value
			case "haproxy_frontend_http_responses_total":
				if f.Responses == nil {
					f.Responses = map[string]float64{}
				}
				f.Responses[code] += value
			}
			o.Frontends[proxy] = f
		}
	}
	for name, count := range latencyCounts {
		o.LatencyMS[name] /= float64(count)
	}
	if !o.Valid() {
		return nil
	}
	return &o
}
