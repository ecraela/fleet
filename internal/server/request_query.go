package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

type requestWindow struct{ duration, step time.Duration }

// Keep each history near 60 buckets. Grouped latency quantiles otherwise
// exhaust the small self-hosted log store's per-query memory budget under load.
var requestWindows = map[string]requestWindow{"15m": {15 * time.Minute, 15 * time.Second}, "1h": {time.Hour, time.Minute}, "6h": {6 * time.Hour, 6 * time.Minute}, "24h": {24 * time.Hour, 24 * time.Minute}}

type requestCursor struct {
	AtMS int64  `json:"at_ms"`
	ID   string `json:"id"`
}
type requestFilter struct {
	rangeName, node, scheme, status, method, digest, id string
	path, order                                         string
	country, asn, source, rule, decision                string
	start, end, minimum                                 int64
	step                                                int64
	before                                              *requestCursor
}

func parseRequestFilter(r *http.Request, now time.Time) (requestFilter, bool) {
	f := requestFilter{rangeName: "15m", end: now.Unix() * 1000}
	q := r.URL.Query()
	for k, v := range q {
		if len(v) != 1 || !slices.Contains([]string{"range", "end", "node", "scheme", "status_class", "method", "min_ms", "spec_digest", "before", "request_id", "path", "path_order", "country", "asn", "client_source", "traffic_rule", "traffic_decision"}, k) {
			return f, false
		}
	}
	if v := q.Get("range"); v != "" {
		f.rangeName = v
	}
	w, ok := requestWindows[f.rangeName]
	if !ok {
		return f, false
	}
	f.step = int64(w.step.Seconds())
	if v := q.Get("end"); v != "" {
		end, err := strconv.ParseInt(v, 10, 64)
		if err != nil || end > now.Add(time.Second).UnixMilli() || end < now.Add(-24*time.Hour).UnixMilli() {
			return f, false
		}
		f.end = end
	}
	f.start = f.end - w.duration.Milliseconds()
	f.node, f.scheme, f.status, f.method, f.digest, f.id = q.Get("node"), q.Get("scheme"), q.Get("status_class"), q.Get("method"), q.Get("spec_digest"), q.Get("request_id")
	f.path, f.order = q.Get("path"), q.Get("path_order")
	f.country, f.asn = q.Get("country"), q.Get("asn")
	f.source = q.Get("client_source")
	f.rule, f.decision = q.Get("traffic_rule"), q.Get("traffic_decision")
	if f.rule != "" && f.rule != "unknown" && !domain.ValidID(f.rule) || f.decision != "" && !slices.Contains([]string{"allow", "block", "would_allow", "would_block"}, f.decision) {
		return f, false
	}
	if f.source != "" && !slices.Contains([]string{"peer", "xff", "proxy_protocol", "unknown"}, f.source) {
		return f, false
	}
	if f.country != "" {
		if len(f.country) > 128 {
			return f, false
		}
		countries := strings.Split(f.country, ",")
		if len(countries) > 32 {
			return f, false
		}
		seen := map[string]bool{}
		for _, country := range countries {
			if country != "unknown" && !accesslog.ValidCountry(country) || seen[country] {
				return f, false
			}
			seen[country] = true
		}
		slices.Sort(countries)
		f.country = strings.Join(countries, ",")
	}
	if f.asn != "" {
		n, err := strconv.ParseUint(f.asn, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != f.asn {
			return f, false
		}
	}
	if f.order == "" {
		f.order = "requests"
	}
	if f.path != "" && !accesslog.ValidPath(f.path) || !slices.Contains([]string{"requests", "errors", "latency"}, f.order) {
		return f, false
	}
	if f.node != "" && !domain.ValidID(f.node) || f.scheme != "" && !slices.Contains([]string{"http", "https"}, f.scheme) || f.status != "" && !slices.Contains([]string{"1xx", "2xx", "3xx", "4xx", "5xx", "other"}, f.status) || f.method != "" && !accesslog.ValidMethod(f.method) || f.digest != "" && !domain.ValidDigest(f.digest) || f.id != "" && !accesslog.ValidEventID(f.id) {
		return f, false
	}
	if v := q.Get("min_ms"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 || n > 60000 {
			return f, false
		}
		f.minimum = n
	}
	if v := q.Get("before"); v != "" {
		if len(v) > 256 {
			return f, false
		}
		raw, err := base64.RawURLEncoding.DecodeString(v)
		var c requestCursor
		if err != nil || json.Unmarshal(raw, &c) != nil || !accesslog.ValidEventID(c.ID) || c.AtMS < f.start || c.AtMS >= f.end {
			return f, false
		}
		f.before = &c
	}
	return f, true
}
func requestScope(org, cluster, app string, f requestFilter) string {
	q := fmt.Sprintf(`organization_id:=%q cluster_id:=%q application_id:=%q kind:=request`, org, cluster, app)
	for _, v := range [][2]string{{"node_id", f.node}, {"scheme", f.scheme}, {"status_class", f.status}, {"method", f.method}, {"spec_digest", f.digest}, {"event_id", f.id}, {"path", f.path}, {"traffic_rule", f.rule}, {"traffic_decision", f.decision}} {
		if v[1] != "" {
			q += " " + v[0] + ":=" + strconv.Quote(v[1])
		}
	}
	if f.minimum > 0 {
		q += fmt.Sprintf(" duration_ms:>=%d", f.minimum)
	}
	if f.country != "" {
		var terms []string
		for _, country := range strings.Split(f.country, ",") {
			if country == "unknown" {
				country = ""
			}
			terms = append(terms, "country:="+strconv.Quote(country))
		}
		if len(terms) == 1 {
			q += " " + terms[0]
		} else {
			q += " (" + strings.Join(terms, " OR ") + ")"
		}
	}
	if f.asn == "0" {
		q += ` (asn:="" OR asn:="0")`
	} else if f.asn != "" {
		q += " asn:=" + strconv.Quote(f.asn)
	}
	if f.source == "unknown" {
		q += ` (client_source:="" OR client_source:="unknown")`
	} else if f.source != "" {
		q += " client_source:=" + strconv.Quote(f.source)
	}
	return q
}

type RequestStats struct {
	Requests uint64            `json:"requests"`
	Classes  map[string]uint64 `json:"classes"`
	Bytes    uint64            `json:"bytes"`
	MeanMS   *float64          `json:"mean_ms"`
	P50MS    *float64          `json:"p50_ms"`
	P95MS    *float64          `json:"p95_ms"`
	P99MS    *float64          `json:"p99_ms"`
}
type RequestPoint struct {
	At int64 `json:"at"`
	RequestStats
}
type RequestCollection struct {
	NodeID   string              `json:"node_id"`
	Name     string              `json:"name"`
	Fresh    bool                `json:"fresh"`
	Pipeline *accesslog.Pipeline `json:"pipeline"`
}
type RequestTraffic struct {
	OrganizationID string               `json:"organization_id"`
	ClusterID      string               `json:"cluster_id"`
	ApplicationID  string               `json:"application_id"`
	Range          string               `json:"range"`
	StartMS        int64                `json:"start_ms"`
	EndMS          int64                `json:"end_ms"`
	StepSeconds    int64                `json:"step_seconds"`
	Summary        RequestStats         `json:"summary"`
	Points         []RequestPoint       `json:"points"`
	Collection     []RequestCollection  `json:"collection"`
	Paths          []RequestPathSummary `json:"paths"`
	PathOrder      string               `json:"path_order"`
	MorePaths      bool                 `json:"more_paths"`
}

type RequestPathSummary struct {
	Path     string `json:"path"`
	PathKind string `json:"path_kind"`
	RequestStats
}

const requestBaseStats = `count() as requests, count() if (status_class:=1xx) as s1, count() if (status_class:=2xx) as s2, count() if (status_class:=3xx) as s3, count() if (status_class:=4xx) as s4, count() if (status_class:=5xx) as s5, count() if (status_class:=other) as other, sum(bytes) as bytes, avg(duration_ms) as mean_ms`
const requestStats = requestBaseStats + `, quantile(0.5,duration_ms) as p50_ms, quantile(0.95,duration_ms) as p95_ms, quantile(0.99,duration_ms) as p99_ms`
const requestPointStats = requestBaseStats + `, quantile(0.95,duration_ms) as p95_ms`

// VictoriaLogs gives each grouped quantile its own working set. Three
// percentiles in one grouped series exceed the small self-hosted query
// budget during sustained traffic, even when each percentile fits alone.
func mergePointQuantile(points, quantiles []map[string]string, field string) bool {
	byTime := make(map[string]map[string]string, len(points))
	for _, point := range points {
		at := point["_time"]
		if at == "" || byTime[at] != nil {
			return false
		}
		byTime[at] = point
	}
	seen := make(map[string]bool, len(quantiles))
	for _, row := range quantiles {
		at := row["_time"]
		point := byTime[at]
		if point == nil || seen[at] || row[field] == "" {
			return false
		}
		point[field] = row[field]
		seen[at] = true
	}
	return len(seen) == len(points)
}

func readNumber(row map[string]string, k string, max float64) (float64, bool) {
	v, err := strconv.ParseFloat(row[k], 64)
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= max
}
func decodeRequestStats(row map[string]string) (RequestStats, bool) {
	s := RequestStats{Classes: map[string]uint64{}}
	n, ok := readNumber(row, "requests", 1<<53-1)
	if !ok || math.Trunc(n) != n {
		return s, false
	}
	s.Requests = uint64(n)
	if n == 0 {
		return s, true
	}
	for _, v := range [][2]string{{"1xx", "s1"}, {"2xx", "s2"}, {"3xx", "s3"}, {"4xx", "s4"}, {"5xx", "s5"}, {"other", "other"}} {
		c, ok := readNumber(row, v[1], n)
		if !ok || math.Trunc(c) != c {
			return s, false
		}
		s.Classes[v[0]] = uint64(c)
	}
	var total uint64
	for _, c := range s.Classes {
		total += c
	}
	if total != s.Requests {
		return s, false
	}
	b, ok := readNumber(row, "bytes", 1<<53-1)
	if !ok {
		return s, false
	}
	s.Bytes = uint64(b)
	for _, v := range []struct {
		k  string
		to **float64
	}{{"mean_ms", &s.MeanMS}, {"p50_ms", &s.P50MS}, {"p95_ms", &s.P95MS}, {"p99_ms", &s.P99MS}} {
		n, ok := readNumber(row, v.k, 86400000)
		if !ok {
			return s, false
		}
		*v.to = &n
	}
	return s, true
}

func (s *Server) configureRequestTelemetry() {
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/applications/{application}/geography", s.authorizeCapability("haproxy.read", s.applicationGeography))
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/applications/{application}/traffic", s.authorizeCapability("haproxy.read", s.applicationTraffic))
	s.mux.HandleFunc("GET /v1/clusters/{cluster}/applications/{application}/requests", s.authorizeCapability("haproxy.read", s.applicationRequests))
}
func (s *Server) requestResource(w http.ResponseWriter, r *http.Request, org string) ([]domain.ApplicationRevision, []domain.Node, requestFilter, bool) {
	f, valid := parseRequestFilter(r, time.Now())
	if !valid {
		fail(w, 400, "invalid request filters")
		return nil, nil, f, false
	}
	cluster, app := r.PathValue("cluster"), r.PathValue("application")
	if !domain.ValidID(cluster) || !domain.ValidID(app) {
		fail(w, 404, "resource not found")
		return nil, nil, f, false
	}
	if _, err := s.config.Store.GetCluster(r.Context(), org, cluster); err != nil {
		storeError(w, err)
		return nil, nil, f, false
	}
	db, ok := s.config.Store.(store.ApplicationStore)
	if !ok {
		fail(w, 503, "application storage unavailable")
		return nil, nil, f, false
	}
	rows, err := db.ListApplications(r.Context(), org, cluster)
	if err != nil {
		storeError(w, err)
		return nil, nil, f, false
	}
	rows = slices.DeleteFunc(rows, func(v domain.ApplicationRevision) bool { return v.ApplicationID != app })
	if len(rows) == 0 {
		fail(w, 404, "application not found")
		return nil, nil, f, false
	}
	nodes, err := s.config.Store.ListNodes(r.Context(), org)
	if err != nil {
		storeError(w, err)
		return nil, nil, f, false
	}
	nodes = slices.DeleteFunc(nodes, func(n domain.Node) bool { return n.ClusterID != cluster })
	if len(slices.DeleteFunc(slices.Clone(nodes), func(n domain.Node) bool { return n.RevokedAt != nil })) > 12 {
		fail(w, 409, "cluster exceeds request profile")
		return nil, nil, f, false
	}
	if f.node != "" && !slices.ContainsFunc(nodes, func(n domain.Node) bool { return n.ID == f.node }) {
		fail(w, 404, "node not found")
		return nil, nil, f, false
	}
	if s.logs == nil {
		fail(w, 503, "request telemetry is not configured")
		return nil, nil, f, false
	}
	return rows, nodes, f, true
}

func (s *Server) applicationTraffic(w http.ResponseWriter, r *http.Request, org string) {
	_, nodes, f, ok := s.requestResource(w, r, org)
	if !ok {
		return
	}
	if f.before != nil {
		fail(w, 400, "pagination is only supported for request search")
		return
	}
	if !acquire(s.logQueries) {
		fail(w, 429, "request query capacity reached")
		return
	}
	defer func() { <-s.logQueries }()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	base := requestScope(org, r.PathValue("cluster"), r.PathValue("application"), f)
	type query struct {
		text       string
		start, end int64
		limit      int
		rows       []map[string]string
		err        error
	}
	queries := []query{
		{text: base + " | stats " + requestStats, start: f.start, end: f.end, limit: 1},
		{text: fmt.Sprintf("%s | stats by (_time:%ds) %s | sort by (_time)", base, f.step, requestPointStats), start: f.start, end: f.end, limit: 362},
		{text: fmt.Sprintf(`organization_id:=%q cluster_id:=%q kind:=pipeline | sort by (_time desc) partition by (node_id) limit 1`, org, r.PathValue("cluster")), start: time.Now().Add(-90 * time.Second).UnixMilli(), end: time.Now().Add(time.Second).UnixMilli(), limit: 12},
	}
	var wg sync.WaitGroup
	for i := range queries {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := &queries[i]
			q.rows, q.err = s.logs.Query(ctx, q.text, q.start, q.end, q.limit)
		}(i)
	}
	wg.Wait()
	for _, q := range queries {
		if q.err != nil {
			fail(w, 503, "request telemetry query unavailable")
			return
		}
	}
	for _, quantile := range []struct {
		phi, field string
	}{{"0.5", "p50_ms"}, {"0.99", "p99_ms"}} {
		text := fmt.Sprintf("%s | stats by (_time:%ds) quantile(%s,duration_ms) as %s | sort by (_time)", base, f.step, quantile.phi, quantile.field)
		rows, err := s.logs.Query(ctx, text, f.start, f.end, 362)
		if err != nil || !mergePointQuantile(queries[1].rows, rows, quantile.field) {
			fail(w, 503, "request telemetry query unavailable")
			return
		}
	}
	// Run after the three core queries so one watching console does not exceed
	// the private store's concurrency budget alongside its request search.
	sortField := map[string]string{"requests": "requests", "errors": "s5", "latency": "p95_ms"}[f.order]
	pathRows, err := s.logs.Query(ctx, base+` | format if (path_kind:="") "unavailable" as path_kind | stats by (path, path_kind) `+requestStats+" | sort by ("+sortField+" desc, path, path_kind) limit 11", f.start, f.end, 11)
	if err != nil {
		fail(w, 503, "request path query unavailable")
		return
	}
	out := RequestTraffic{OrganizationID: org, ClusterID: r.PathValue("cluster"), ApplicationID: r.PathValue("application"), Range: f.rangeName, StartMS: f.start, EndMS: f.end, StepSeconds: f.step, Points: []RequestPoint{}, Collection: []RequestCollection{}, Paths: []RequestPathSummary{}, PathOrder: f.order, MorePaths: len(pathRows) > 10}
	if out.MorePaths {
		pathRows = pathRows[:10]
	}
	seenPaths := map[string]bool{}
	for _, row := range pathRows {
		p := RequestPathSummary{Path: row["path"], PathKind: row["path_kind"]}
		if p.PathKind == "" {
			p.PathKind = "unavailable"
		}
		key := p.PathKind + ":" + p.Path
		if !accesslog.ValidPathEvidence(p.Path, p.PathKind) || f.path != "" && p.Path != f.path || seenPaths[key] {
			fail(w, 503, "invalid request path summary")
			return
		}
		seenPaths[key] = true
		p.RequestStats, ok = decodeRequestStats(row)
		if !ok || p.Requests == 0 {
			fail(w, 503, "invalid request path statistics")
			return
		}
		out.Paths = append(out.Paths, p)
	}
	if len(queries[0].rows) == 0 {
		out.Summary = RequestStats{Classes: map[string]uint64{}}
	} else {
		out.Summary, ok = decodeRequestStats(queries[0].rows[0])
		if !ok {
			fail(w, 503, "invalid request summary")
			return
		}
	}
	for _, row := range queries[1].rows {
		at, err := time.Parse(time.RFC3339Nano, row["_time"])
		stats, valid := decodeRequestStats(row)
		if err != nil || !valid || at.UnixMilli() < f.start-f.step*1000 || at.UnixMilli() >= f.end {
			fail(w, 503, "invalid request history")
			return
		}
		out.Points = append(out.Points, RequestPoint{At: at.Unix(), RequestStats: stats})
	}
	for _, n := range nodes {
		if n.RevokedAt != nil {
			continue
		}
		if f.node != "" && f.node != n.ID {
			continue
		}
		c := RequestCollection{NodeID: n.ID, Name: n.Name}
		for _, row := range queries[2].rows {
			if row["node_id"] != n.ID {
				continue
			}
			p, valid := decodePipeline(row, org, r.PathValue("cluster"))
			if !valid {
				fail(w, 503, "invalid request collection status")
				return
			}
			c.Pipeline = &p
			c.Fresh = p.Listening && time.Now().UnixMilli()-p.AtMS <= 15000 && p.AtMS <= time.Now().Add(time.Second).UnixMilli()
		}
		out.Collection = append(out.Collection, c)
	}
	if !s.canReadOrganization(r, org) {
		fail(w, 401, "valid organization session required")
		return
	}
	writeJSON(w, 200, out)
}

func decodePipeline(row map[string]string, org, cluster string) (accesslog.Pipeline, bool) {
	p := accesslog.Pipeline{BootID: row["boot_id"], Listening: row["listening"] == "true", PathMode: row["path_mode"], PathPolicy: row["path_policy"]}
	if raw := row["geo_profile_data"]; raw != "" {
		if len(raw) > 2048 {
			return p, false
		}
		decoder := json.NewDecoder(bytes.NewBufferString(raw))
		decoder.DisallowUnknownFields()
		var geo accesslog.GeoCollection
		if decoder.Decode(&geo) != nil || decoder.Decode(new(any)) != io.EOF || !geo.Valid() {
			return p, false
		}
		p.Geo = &geo
	}
	if p.PathMode != "" && !slices.Contains([]string{"off", "sanitized", "exact"}, p.PathMode) || p.PathPolicy != "" && !domain.ValidDigest(p.PathPolicy) {
		return p, false
	}
	if row["organization_id"] != org || row["cluster_id"] != cluster || !domain.ValidID(p.BootID) || !slices.Contains([]string{"true", "false"}, row["listening"]) {
		return p, false
	}
	var err error
	p.AtMS, err = strconv.ParseInt(row["observed_at_ms"], 10, 64)
	if err != nil {
		return p, false
	}
	for _, v := range []struct {
		k  string
		to *uint64
	}{{"received", &p.Received}, {"ignored", &p.Ignored}, {"rejected", &p.Rejected}, {"dropped", &p.Dropped}, {"unconfirmed", &p.Unconfirmed}} {
		n, ok := readNumber(row, v.k, 1<<53-1)
		if !ok || math.Trunc(n) != n {
			return p, false
		}
		*v.to = uint64(n)
	}
	return p, true
}

type RequestRecord struct {
	accesslog.Event
	NodeID           string `json:"node_id"`
	MatchingVersions []int  `json:"matching_versions"`
}

func (s *Server) applicationRequests(w http.ResponseWriter, r *http.Request, org string) {
	revisions, nodes, f, ok := s.requestResource(w, r, org)
	if !ok {
		return
	}
	if !acquire(s.logQueries) {
		fail(w, 429, "request query capacity reached")
		return
	}
	defer func() { <-s.logQueries }()
	q := requestScope(org, r.PathValue("cluster"), r.PathValue("application"), f)
	if c := f.before; c != nil {
		q += fmt.Sprintf(` (observed_at_ms:<%d OR (observed_at_ms:=%d event_id:<%q))`, c.AtMS, c.AtMS, c.ID)
	}
	q += " | sort by (observed_at_ms desc, event_id desc) limit 11"
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	rows, err := s.logs.Query(ctx, q, f.start, f.end, 11)
	if err != nil {
		fail(w, 503, "request search unavailable")
		return
	}
	more := len(rows) > 10
	if more {
		rows = rows[:10]
	}
	items := []RequestRecord{}
	for _, row := range rows {
		item, valid := decodeRequest(row, org, r.PathValue("cluster"), r.PathValue("application"), f, nodes)
		if !valid {
			fail(w, 503, "invalid request search result")
			return
		}
		for _, v := range revisions {
			if v.Spec.Digest() == item.SpecDigest {
				if item.Traffic != nil && !validTrafficEvidence(v.Spec, *item.Traffic, item.Status) {
					fail(w, 503, "invalid stored traffic decision")
					return
				}
				item.MatchingVersions = append(item.MatchingVersions, v.Number)
			}
		}
		if item.Traffic != nil && len(item.MatchingVersions) == 0 {
			fail(w, 503, "unknown stored traffic policy version")
			return
		}
		slices.Sort(item.MatchingVersions)
		items = append(items, item)
	}
	var cursor string
	if more {
		last := items[len(items)-1]
		raw, _ := json.Marshal(requestCursor{AtMS: last.AtMS, ID: last.ID})
		cursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	if !s.canReadOrganization(r, org) {
		fail(w, 401, "valid organization session required")
		return
	}
	writeJSON(w, 200, map[string]any{"organization_id": org, "cluster_id": r.PathValue("cluster"), "application_id": r.PathValue("application"), "start_ms": f.start, "end_ms": f.end, "requests": items, "next_cursor": cursor})
}
func decodeRequest(row map[string]string, org, cluster, app string, f requestFilter, nodes []domain.Node) (RequestRecord, bool) {
	e := RequestRecord{Event: accesslog.Event{ID: row["event_id"], ApplicationID: row["application_id"], SpecDigest: row["spec_digest"], Server: row["server"], Method: row["method"], Scheme: row["scheme"], Termination: row["termination"], Path: row["path"], PathKind: row["path_kind"]}, NodeID: row["node_id"], MatchingVersions: []int{}}
	if row["traffic_rule"] != "" || row["traffic_decision"] != "" {
		e.Traffic = &accesslog.TrafficDecision{RuleID: row["traffic_rule"], Decision: row["traffic_decision"]}
		if !e.Traffic.Valid() {
			return e, false
		}
	}
	if f.rule != "" && (e.Traffic == nil || e.Traffic.RuleID != f.rule) || f.decision != "" && (e.Traffic == nil || e.Traffic.Decision != f.decision) {
		return e, false
	}
	if row["geo_status"] != "" {
		asn, err := strconv.ParseUint(row["asn"], 10, 32)
		if row["asn"] != "" && err != nil {
			return e, false
		}
		e.Geo = &accesslog.Geography{Status: row["geo_status"], Source: row["client_source"], Country: row["country"], ASN: uint32(asn), Profile: row["geo_profile"]}
		if !e.Geo.Valid() {
			return e, false
		}
	}
	if row["organization_id"] != org || row["cluster_id"] != cluster || e.ApplicationID != app || !slices.ContainsFunc(nodes, func(n domain.Node) bool { return n.ID == e.NodeID }) {
		return e, false
	}
	for _, v := range []struct {
		k  string
		to *int64
	}{{"observed_at_ms", &e.AtMS}, {"bytes", &e.Bytes}, {"duration_ms", &e.DurationMS}, {"response_ms", &e.ResponseMS}, {"connect_ms", &e.ConnectMS}, {"queue_ms", &e.QueueMS}} {
		n, err := strconv.ParseInt(row[v.k], 10, 64)
		if err != nil {
			return e, false
		}
		*v.to = n
	}
	status, err := strconv.Atoi(row["status"])
	if err != nil {
		return e, false
	}
	e.Status = status
	// Reuse shape validation at the stored timestamp; historical records are not
	// required to be fresh. Enforce the caller's frozen search interval separately.
	if !e.Event.Valid(time.UnixMilli(e.AtMS)) || e.AtMS < f.start || e.AtMS >= f.end {
		return e, false
	}
	if f.node != "" && e.NodeID != f.node || f.scheme != "" && e.Scheme != f.scheme || f.status != "" && row["status_class"] != f.status || f.method != "" && e.Method != f.method || f.digest != "" && e.SpecDigest != f.digest || f.id != "" && e.ID != f.id || f.path != "" && e.Path != f.path || e.DurationMS < f.minimum {
		return e, false
	}
	country, asn := "", "0"
	if e.Geo != nil {
		country, asn = e.Geo.Country, strconv.FormatUint(uint64(e.Geo.ASN), 10)
	}
	source := "unknown"
	if e.Geo != nil {
		source = e.Geo.Source
	}
	if f.source != "" && source != f.source {
		return e, false
	}
	if !requestCountryMatches(f.country, country) || f.asn != "" && asn != f.asn {
		return e, false
	}
	if c := f.before; c != nil && (e.AtMS > c.AtMS || e.AtMS == c.AtMS && strings.Compare(e.ID, c.ID) >= 0) {
		return e, false
	}
	return e, true
}

func requestCountryMatches(filter, country string) bool {
	if filter == "" {
		return true
	}
	if country == "" {
		country = "unknown"
	}
	return slices.Contains(strings.Split(filter, ","), country)
}
