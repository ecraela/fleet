package telemetry

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/prompb"
)

const MaxScrape = 2 << 20

type Collector struct {
	URL    string
	Client *http.Client
	// AllowedMetrics can narrow the shared supported observation contract.
	AllowedMetrics map[string]bool
	ScrapeFailures uint64
	ExportFailures uint64
}

func (c *Collector) Collect(ctx context.Context, now time.Time) (*prompb.WriteRequest, error) {
	series, err := c.scrape(ctx, now)
	up := 1.0
	if err != nil {
		c.ScrapeFailures++
		up = 0
		series = nil
	}
	for name, value := range map[string]float64{"converge_agent_haproxy_up": up, "converge_agent_scrape_failures_total": float64(c.ScrapeFailures), "converge_agent_remote_write_failures_total": float64(c.ExportFailures), "converge_agent_scrape_timestamp_seconds": float64(now.UnixMilli()) / 1000} {
		series = append(series, prompb.TimeSeries{Labels: []prompb.Label{{Name: "__name__", Value: name}}, Samples: []prompb.Sample{{Value: value, Timestamp: now.UnixMilli()}}})
	}
	req := &prompb.WriteRequest{Timeseries: series}
	if validation := Validate(req); validation != nil {
		return nil, validation
	}
	return req, err
}

func (c *Collector) scrape(ctx context.Context, now time.Time) ([]prompb.TimeSeries, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, errors.New("invalid scrape URL")
	}
	req.Header.Set("Accept", "text/plain;version=0.0.4")
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("HAProxy scrape failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HAProxy scrape returned status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxScrape+1))
	if err != nil || len(b) > MaxScrape {
		return nil, errors.New("HAProxy scrape body exceeds limit or is unreadable")
	}
	// Bound text lines and label allocations before calling the maintained parser.
	scanner := bufio.NewScanner(bytes.NewReader(b))
	scanner.Buffer(make([]byte, 4096), 16<<10)
	lines := 0
	for scanner.Scan() {
		s := strings.TrimSpace(scanner.Text())
		if s != "" && !strings.HasPrefix(s, "#") {
			lines++
			if lines > MaxSeries-4 {
				return nil, errors.New("HAProxy scrape cardinality exceeds limit")
			}
		}
	}
	if scanner.Err() != nil {
		return nil, errors.New("HAProxy scrape line exceeds limit")
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("invalid HAProxy Prometheus text")
	}
	series := make([]prompb.TimeSeries, 0, lines)
	for name, family := range families {
		// HAProxy includes information families (for example process_node with
		// a node label) outside our observation contract. They are never sent.
		if !strings.HasPrefix(name, "haproxy_") || !SupportedMetrics[name] || (c.AllowedMetrics != nil && !c.AllowedMetrics[name]) {
			continue
		}
		for _, metric := range family.Metric {
			if len(metric.Label) > MaxLabels-4 {
				return nil, errors.New("too many HAProxy labels")
			}
			for _, label := range metric.Label {
				if reserved(label.GetName()) {
					return nil, errors.New("reserved identity label in HAProxy scrape")
				}
			}
			var value float64
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				value = metric.GetCounter().GetValue()
			case dto.MetricType_GAUGE:
				value = metric.GetGauge().GetValue()
			case dto.MetricType_UNTYPED:
				value = metric.GetUntyped().GetValue()
			default:
				return nil, errors.New("unsupported HAProxy metric type")
			}
			// The native exporter uses NaN for unavailable/unlimited settings,
			// such as server_limit_sessions without maxconn. Omit that sample:
			// it has no numeric observation and must not turn into a zero.
			if math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			labels := []prompb.Label{{Name: "__name__", Value: name}}
			for _, label := range metric.Label {
				labels = append(labels, prompb.Label{Name: label.GetName(), Value: label.GetValue()})
			}
			sort.Slice(labels, func(i, j int) bool { return labels[i].Name < labels[j].Name })
			series = append(series, prompb.TimeSeries{Labels: labels, Samples: []prompb.Sample{{Timestamp: now.UnixMilli(), Value: value}}})
		}
	}
	if len(series) == 0 {
		return nil, errors.New("HAProxy scrape has no supported metrics")
	}
	if err := Validate(&prompb.WriteRequest{Timeseries: series}); err != nil {
		return nil, err
	}
	return series, nil
}
