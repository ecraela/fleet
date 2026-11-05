// Package telemetry implements the bounded Remote Write 1.0 observation contract.
package telemetry

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

const (
	MaxCompressed  = 256 << 10
	MaxDecoded     = 2 << 20
	MaxSeries      = 1000
	MaxLabels      = 16
	MaxLabelLength = 256
	MaxSamples     = 1
)

var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// SupportedMetrics intentionally excludes arbitrary user-created metrics.
// Extend this observation contract deliberately, rather than accepting labels
// and metric families that can turn a node credential into generic ingestion.
var SupportedMetrics = map[string]bool{
	"haproxy_process_uptime_seconds":             true,
	"haproxy_process_current_connections":        true,
	"haproxy_process_max_connections":            true,
	"haproxy_process_idle_time_percent":          true,
	"converge_agent_haproxy_up":                  true,
	"converge_agent_scrape_failures_total":       true,
	"converge_agent_remote_write_failures_total": true,
	"converge_agent_scrape_timestamp_seconds":    true,
}

func init() {
	for _, scope := range []string{"frontend", "backend", "server"} {
		for _, suffix := range []string{"bytes_in_total", "bytes_out_total", "current_sessions", "sessions_total", "limit_sessions", "current_session_rate", "http_requests_total", "http_responses_total", "status", "requests_denied_total", "responses_denied_total", "request_errors_total", "connection_errors_total", "response_errors_total", "current_queue", "max_queue", "weight", "uweight", "check_status", "check_duration_seconds", "check_failures_total", "check_up_down_total", "check_last_change_seconds", "downtime_seconds_total", "active_servers", "backup_servers", "queue_time_average_seconds", "connect_time_average_seconds", "response_time_average_seconds", "total_time_average_seconds"} {
			SupportedMetrics["haproxy_"+scope+"_"+suffix] = true
		}
	}
}

func reserved(name string) bool {
	switch strings.ToLower(name) {
	case "tenant", "tenant_id", "tenantid", "org", "org_id", "organization", "organization_id", "organizationid", "cluster", "cluster_id", "clusterid", "node", "node_id", "nodeid":
		return true
	}
	return false
}

// Validate checks an untrusted agent batch, before any identity labels are added.
func Validate(req *prompb.WriteRequest) error {
	if req == nil || len(req.Timeseries) == 0 || len(req.Timeseries) > MaxSeries || len(req.Metadata) != 0 {
		return errors.New("invalid telemetry series count or metadata")
	}
	seenSeries := make(map[string]bool, len(req.Timeseries))
	for _, ts := range req.Timeseries {
		if len(ts.Labels) == 0 || len(ts.Labels) > MaxLabels-3 || len(ts.Samples) != MaxSamples || len(ts.Exemplars) != 0 || len(ts.Histograms) != 0 {
			return errors.New("invalid telemetry labels or samples")
		}
		previous, metric := "", ""
		var key strings.Builder
		for i, l := range ts.Labels {
			if len(l.Name) > MaxLabelLength || len(l.Value) > MaxLabelLength || !labelName.MatchString(l.Name) || !utf8.ValidString(l.Value) || reserved(l.Name) || (strings.HasPrefix(l.Name, "__") && l.Name != "__name__") || (i > 0 && l.Name <= previous) {
				return errors.New("invalid, reserved, duplicate or unsorted telemetry label")
			}
			previous = l.Name
			if l.Name == "__name__" {
				metric = l.Value
			}
			fmt.Fprintf(&key, "%d:%s%d:%s", len(l.Name), l.Name, len(l.Value), l.Value)
		}
		if !SupportedMetrics[metric] || seenSeries[key.String()] {
			return errors.New("unsupported or duplicate telemetry series")
		}
		seenSeries[key.String()] = true
		s := ts.Samples[0]
		if s.Timestamp <= 0 || math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			return errors.New("invalid telemetry sample")
		}
		if metric == "converge_agent_haproxy_up" || metric == "converge_agent_scrape_timestamp_seconds" {
			if len(ts.Labels) != 1 || !ValidStatusValue(metric, s.Value) {
				return errors.New("invalid singleton agent status")
			}
			if metric == "converge_agent_scrape_timestamp_seconds" && math.Abs(s.Value-float64(s.Timestamp)/1000) > 60 {
				return errors.New("agent collection timestamp differs from its sample")
			}
		}
	}
	return nil
}

// ValidStatusValue is shared by untrusted ingestion and historical status reads.
// Status presence remains optional for older agents without collection times.
func ValidStatusValue(name string, value float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	switch name {
	case "converge_agent_haproxy_up":
		return value == 0 || value == 1
	case "converge_agent_scrape_timestamp_seconds":
		// Fleet/live responses encode collection times as RFC 3339. A retained
		// sample outside JSON's four-digit year range must not break peer output.
		return value > 0 && value <= 253402300799
	}
	return false
}

func Encode(req *prompb.WriteRequest) ([]byte, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return encode(req)
}

// EncodeStamped is only for a server-authenticated batch after Stamp.
func EncodeStamped(req *prompb.WriteRequest) ([]byte, error) {
	if req == nil {
		return nil, errors.New("missing stamped telemetry")
	}
	untrusted := &prompb.WriteRequest{Timeseries: make([]prompb.TimeSeries, len(req.Timeseries))}
	for i, ts := range req.Timeseries {
		identities := map[string]bool{}
		labels := make([]prompb.Label, 0, len(ts.Labels))
		if len(ts.Labels) > MaxLabels {
			return nil, errors.New("too many stamped telemetry labels")
		}
		for _, l := range ts.Labels {
			if reserved(l.Name) {
				if (l.Name != "organization_id" && l.Name != "cluster_id" && l.Name != "node_id") || identities[l.Name] || len(l.Value) == 0 || len(l.Value) > MaxLabelLength {
					return nil, errors.New("invalid stamped identity")
				}
				identities[l.Name] = true
			} else {
				labels = append(labels, l)
			}
		}
		if len(identities) != 3 {
			return nil, errors.New("missing stamped identity")
		}
		ts.Labels = labels
		untrusted.Timeseries[i] = ts
	}
	if err := Validate(untrusted); err != nil {
		return nil, err
	}
	return encode(req)
}

func encode(req *prompb.WriteRequest) ([]byte, error) {
	if req.Size() > MaxDecoded {
		return nil, errors.New("decoded telemetry exceeds limit")
	}
	b, err := req.Marshal()
	if err != nil {
		return nil, err
	}
	compressed := snappy.Encode(nil, b)
	if len(compressed) > MaxCompressed {
		return nil, errors.New("compressed telemetry exceeds limit")
	}
	return compressed, nil
}

func Decode(r io.Reader) (*prompb.WriteRequest, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxCompressed+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxCompressed {
		return nil, errors.New("compressed telemetry exceeds limit")
	}
	n, err := snappy.DecodedLen(b)
	if err != nil || n > MaxDecoded {
		return nil, errors.New("invalid or oversized snappy telemetry")
	}
	decoded, err := snappy.Decode(nil, b)
	if err != nil {
		return nil, errors.New("invalid snappy telemetry")
	}
	if err := preflight(decoded); err != nil {
		return nil, err
	}
	req := new(prompb.WriteRequest)
	if err := req.Unmarshal(decoded); err != nil {
		return nil, errors.New("invalid telemetry protobuf")
	}
	if err := Validate(req); err != nil {
		return nil, err
	}
	return req, nil
}

// preflight bounds repeated protobuf allocations before invoking the generated
// decoder. Only label and sample messages are supported in the initial slice.
func preflight(b []byte) error {
	series := 0
	return fields(b, func(number uint64, wire uint64, body []byte) error {
		series++
		if number != 1 || wire != 2 || series > MaxSeries {
			return errors.New("unsupported or excessive telemetry fields")
		}
		labels, samples := 0, 0
		return fields(body, func(n, w uint64, value []byte) error {
			switch n {
			case 1:
				labels++
				if w != 2 || labels > MaxLabels-3 || len(value) > 2*MaxLabelLength+16 {
					return errors.New("excessive telemetry labels")
				}
			case 2:
				samples++
				if w != 2 || samples > MaxSamples || len(value) > 32 {
					return errors.New("excessive telemetry samples")
				}
			default:
				return errors.New("unsupported telemetry series field")
			}
			return nil
		})
	})
}

func fields(b []byte, visit func(uint64, uint64, []byte) error) error {
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 || tag>>3 == 0 {
			return errors.New("invalid telemetry wire format")
		}
		b = b[n:]
		wire := tag & 7
		var body []byte
		switch wire {
		case 2:
			length, n := binary.Uvarint(b)
			if n <= 0 || length > uint64(len(b)-n) {
				return errors.New("invalid telemetry field length")
			}
			body, b = b[n:n+int(length)], b[n+int(length):]
		default:
			return errors.New("unsupported telemetry wire type")
		}
		if err := visit(tag>>3, wire, body); err != nil {
			return err
		}
	}
	return nil
}

type Scope struct{ OrganizationID, ClusterID, NodeID string }

// Stamp makes a distinct batch so client-supplied data never owns the tenant
// labels. Validate rejects attempts to supply any identity alias beforehand.
func Stamp(req *prompb.WriteRequest, scope Scope) (*prompb.WriteRequest, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if scope.OrganizationID == "" || scope.ClusterID == "" || scope.NodeID == "" {
		return nil, errors.New("missing authenticated telemetry scope")
	}
	out := &prompb.WriteRequest{Timeseries: make([]prompb.TimeSeries, len(req.Timeseries))}
	for i, ts := range req.Timeseries {
		labels := append([]prompb.Label(nil), ts.Labels...)
		labels = append(labels, prompb.Label{Name: "organization_id", Value: scope.OrganizationID}, prompb.Label{Name: "cluster_id", Value: scope.ClusterID}, prompb.Label{Name: "node_id", Value: scope.NodeID})
		sort.Slice(labels, func(i, j int) bool { return labels[i].Name < labels[j].Name })
		out.Timeseries[i] = prompb.TimeSeries{Labels: labels, Samples: append([]prompb.Sample(nil), ts.Samples...)}
	}
	return out, nil
}
