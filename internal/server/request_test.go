package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

type requestStore struct {
	*store.Memory
	rows []domain.ApplicationRevision
}

func (db requestStore) ListApplications(ctx context.Context, org, cluster string) ([]domain.ApplicationRevision, error) {
	if _, err := db.GetCluster(ctx, org, cluster); err != nil {
		return nil, err
	}
	out := []domain.ApplicationRevision{}
	for _, v := range db.rows {
		if v.OrganizationID == org && v.ClusterID == cluster {
			out = append(out, v)
		}
	}
	return out, nil
}

type requestTransport func(*http.Request) (*http.Response, error)

func (f requestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func logResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func requestFixture(t *testing.T) (*fixture, domain.ApplicationRevision, domain.Node, *x509.Certificate) {
	t.Helper()
	f := browserFixture(t)
	node, leaf, _, _ := f.enrollNode(t)
	a := domain.HTTPApplication{ID: domain.NewID(), Name: "Store", Listener: "public", Hostname: "apps.example.test", PathPrefix: "/shop", Algorithm: "roundrobin", HealthPath: "/healthz", Servers: []domain.ConfigurationServer{{Name: "web", Address: "backend.internal", Port: 8080, Weight: 100}}, RequestTelemetry: true}
	rev := domain.ApplicationRevision{ID: domain.NewID(), OrganizationID: f.org, ClusterID: f.cluster.ID, ApplicationID: a.ID, Spec: a, Number: 1, Status: "succeeded"}
	f.server.config.Store = requestStore{Memory: f.store, rows: []domain.ApplicationRevision{rev}}
	return f, rev, node, leaf
}
func requestRow(f *fixture, rev domain.ApplicationRevision, node domain.Node) map[string]string {
	return map[string]string{"organization_id": f.org, "cluster_id": f.cluster.ID, "application_id": rev.ApplicationID, "node_id": node.ID, "event_id": accesslog.EventID(domain.NewID(), 1), "observed_at_ms": strconvInt(time.Now().Add(-time.Second).UnixMilli()), "spec_digest": rev.Spec.Digest(), "server": "web", "method": "GET", "scheme": "https", "status": "503", "status_class": "5xx", "bytes": "16", "duration_ms": "25", "response_ms": "12", "connect_ms": "4", "queue_ms": "0", "termination": "--"}
}
func strconvInt(n int64) string { return strconv.FormatInt(n, 10) }

func TestRequestTelemetryUnavailableRemainsTenantBound(t *testing.T) {
	f, rev, _, _ := requestFixture(t)
	foreign, err := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cluster string
		status  int
	}{{f.cluster.ID, http.StatusServiceUnavailable}, {foreign.ID, http.StatusNotFound}} {
		base := "/v1/clusters/" + tc.cluster + "/applications/" + rev.ApplicationID
		for _, suffix := range []string{"/traffic", "/requests"} {
			if got := f.request("GET", base+suffix, nil, true); got.Code != tc.status {
				t.Fatalf("%s: got %d, want %d", suffix, got.Code, tc.status)
			}
		}
		input := domain.ApplicationInput{RequestID: domain.NewID(), ExpectedRevision: rev.Number, Spec: rev.Spec}
		if got := f.request("POST", base+"/deploy", input, true); got.Code != tc.status {
			t.Fatalf("telemetry deployment: got %d, want %d", got.Code, tc.status)
		}
	}
}

func TestRequestAPIsEnforceTenantReadAndTypedFilters(t *testing.T) {
	f, rev, node, _ := requestFixture(t)
	var calls atomic.Int32
	var wrong atomic.Bool
	row := requestRow(f, rev, node)
	row["path"], row["path_kind"] = "/orders/:id", "template"
	client := &http.Client{Transport: requestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		r.ParseForm()
		q := r.Form.Get("query")
		if !strings.Contains(q, `organization_id:="`+f.org+`"`) || !strings.Contains(q, `cluster_id:="`+f.cluster.ID+`"`) {
			t.Error("missing tenant query scope", q)
		}
		if strings.Contains(q, "kind:=pipeline") {
			return logResponse(""), nil
		}
		if !strings.Contains(q, `application_id:="`+rev.ApplicationID+`"`) {
			t.Error("missing application scope", q)
		}
		if strings.Contains(q, "sort by (observed_at_ms") {
			copy := maps.Clone(row)
			if wrong.Load() {
				copy["application_id"] = domain.NewID()
			}
			b, _ := json.Marshal(copy)
			return logResponse(string(b) + "\n"), nil
		}
		stats := map[string]string{"requests": "1", "s1": "0", "s2": "0", "s3": "0", "s4": "0", "s5": "1", "other": "0", "bytes": "16", "mean_ms": "25", "p50_ms": "25", "p95_ms": "25", "p99_ms": "25"}
		if strings.Contains(q, "stats by (path, path_kind)") {
			if !strings.Contains(q, `format if (path_kind:="") "unavailable" as path_kind`) {
				t.Error("legacy path state was not normalized before aggregation")
			}
			stats["path"], stats["path_kind"] = row["path"], row["path_kind"]
		}
		if strings.Contains(q, "stats by") {
			stats["_time"] = time.Now().Add(-5 * time.Second).UTC().Truncate(5 * time.Second).Format(time.RFC3339Nano)
		}
		if strings.Contains(q, "stats by (_time:") {
			if strings.Count(q, "quantile(") != 1 {
				return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("grouped quantiles exceed memory budget"))}, nil
			}
			for _, field := range []string{"p50_ms", "p95_ms", "p99_ms"} {
				if !strings.Contains(q, " as "+field) {
					delete(stats, field)
				}
			}
		}
		b, _ := json.Marshal(stats)
		return logResponse(string(b) + "\n"), nil
	})}
	var err error
	f.server.logs, err = accesslog.NewStorage("http://private-logs.invalid", client)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
	base := "/v1/clusters/" + f.cluster.ID + "/applications/" + rev.ApplicationID
	for _, suffix := range []string{"/traffic", "/requests"} {
		if got := f.request("GET", base+suffix, nil, false); got.Code != 401 {
			t.Fatal("anonymous", got.Code)
		}
		if got := f.request("GET", "/v1/clusters/"+foreign.ID+"/applications/"+rev.ApplicationID+suffix, nil, true); got.Code != 404 {
			t.Fatal("foreign cluster", got.Code)
		}
		if got := f.request("GET", "/v1/clusters/"+f.cluster.ID+"/applications/"+domain.NewID()+suffix, nil, true); got.Code != 404 {
			t.Fatal("foreign app", got.Code)
		}
		if got := f.request("GET", base+suffix, nil, true); got.Code != 200 {
			t.Fatal(suffix, got.Code, got.Body.String())
		}
		if got := f.request("GET", base+suffix+"?path=%2Forders%2F%3Aid&path_order=errors", nil, true); got.Code != 200 {
			t.Fatal("path filter", got.Code, got.Body.String())
		}
		if got := f.request("GET", base+suffix+"?path=%2Fother", nil, true); got.Code != 503 {
			t.Fatal("unfiltered store result escaped path filter", got.Code)
		}
	}
	before := calls.Load()
	for _, query := range []string{"?query=*", "?organization_id=" + domain.NewID(), "?range=all", "?node=" + domain.NewID(), "?range=1h&range=6h", "?min_ms=-1", "?min_ms=60001", "?spec_digest=header", "?before=bad", "?method=GET%7CHEAD", "?end=99999999999999", "?path=%2Forders%3Ftoken%3Dsecret", "?path=%2Forders%2F..", "?path=%2Forders%7Cstats", "?path_order=arbitrary"} {
		got := f.request("GET", base+"/requests"+query, nil, true)
		if got.Code != 400 && got.Code != 404 {
			t.Fatal(query, got.Code)
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid filters reached log storage")
	}
	pending, nonce := challenge(t, f.server)
	created := exchange(t, f, pending, validClaims(f, nonce))
	if got := browserRequest(t, f.server, "GET", base+"/requests", "", sessionCookie(t, created)); got.Code != 200 {
		t.Fatal("read-only viewer blocked", got.Code)
	}
	wrong.Store(true)
	if got := f.request("GET", base+"/requests", nil, true); got.Code != 503 || strings.Contains(got.Body.String(), "spec_digest") {
		t.Fatal("foreign storage result escaped", got.Code)
	}
}

func TestMergePointQuantileRequiresMatchingTimeBuckets(t *testing.T) {
	points := []map[string]string{{"_time": "first"}, {"_time": "second"}}
	if !mergePointQuantile(points, []map[string]string{{"_time": "second", "p50_ms": "20"}, {"_time": "first", "p50_ms": "10"}}, "p50_ms") {
		t.Fatal("matching time buckets were rejected")
	}
	if points[0]["p50_ms"] != "10" || points[1]["p50_ms"] != "20" {
		t.Fatal("quantiles were merged into the wrong time buckets")
	}
	for _, rows := range [][]map[string]string{
		{{"_time": "first", "p99_ms": "10"}},
		{{"_time": "first", "p99_ms": "10"}, {"_time": "first", "p99_ms": "11"}},
		{{"_time": "first", "p99_ms": "10"}, {"_time": "other", "p99_ms": "20"}},
		{{"_time": "first", "p99_ms": "10"}, {"_time": "second"}},
	} {
		if mergePointQuantile(points, rows, "p99_ms") {
			t.Fatal("mismatched quantile buckets were accepted", rows)
		}
	}
}

func TestRequestIngestionRejectsExtraFieldsAndScopesNodeEvidence(t *testing.T) {
	f, rev, node, leaf := requestFixture(t)
	var saved []map[string]any
	client := &http.Client{Transport: requestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/insert/jsonline" || r.URL.Query().Get("_stream_fields") != "organization_id,cluster_id,node_id,kind,application_id" {
			t.Error("incorrect private insert", r.URL)
		}
		decoder := json.NewDecoder(r.Body)
		for {
			var row map[string]any
			if err := decoder.Decode(&row); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			saved = append(saved, row)
		}
		return logResponse(""), nil
	})}
	ingest, err := New(Config{Role: "ingest", Store: f.server.config.Store, Authority: f.authority, MetricsURL: "http://metrics.invalid", LogsURL: "http://logs.invalid", LogsClient: client})
	if err != nil {
		t.Fatal(err)
	}
	boot := domain.NewID()
	e := accesslog.Event{ID: accesslog.EventID(boot, 1), AtMS: time.Now().UnixMilli(), ApplicationID: rev.ApplicationID, SpecDigest: rev.Spec.Digest(), Server: "web", Method: "GET", Status: 200, Bytes: 42, DurationMS: 20, ResponseMS: 10, ConnectMS: 3, QueueMS: 0, Scheme: "https", Termination: "--"}
	batch := accesslog.Batch{Events: []accesslog.Event{e}, Pipeline: accesslog.Pipeline{BootID: boot, AtMS: e.AtMS, Listening: true, Received: 1}}
	send := func(body any, peer bool) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/v1/logs/write", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if peer {
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
		}
		w := httptest.NewRecorder()
		ingest.ServeHTTP(w, r)
		return w
	}
	if got := send(batch, false); got.Code != 401 {
		t.Fatal("mTLS required", got.Code)
	}
	if got := send(batch, true); got.Code != 204 {
		t.Fatal(got.Code, got.Body.String())
	}
	if len(saved) != 2 || saved[0]["organization_id"] != f.org || saved[0]["node_id"] != node.ID || saved[0]["kind"] != "request" {
		t.Fatal("scope not stamped", saved)
	}
	if saved[0]["path"] != "" || saved[0]["path_kind"] != "unavailable" {
		t.Fatal("legacy request invented a path")
	}
	for _, k := range []string{"url", "query", "authorization", "headers", "body", "client_ip"} {
		if _, ok := saved[0][k]; ok {
			t.Fatal("private content exported", k)
		}
	}
	withPath := batch
	pathEvent := e
	pathEvent.ID = accesslog.EventID(boot, 2)
	pathEvent.Path, pathEvent.PathKind = "/orders/:id", "template"
	withPath.Events = []accesslog.Event{pathEvent}
	withPath.Pipeline.PathMode, withPath.Pipeline.PathPolicy = "sanitized", strings.Repeat("b", 64)
	if got := send(withPath, true); got.Code != 204 || saved[2]["path"] != "/orders/:id" || saved[2]["path_kind"] != "template" || saved[3]["path_mode"] != "sanitized" {
		t.Fatal("sanitized path ingestion", got.Code)
	}
	withPath.Events[0].Path = "/orders/private?token=secret"
	if got := send(withPath, true); got.Code != 400 {
		t.Fatal("query string accepted in stored path", got.Code)
	}
	for _, mutate := range []func(map[string]any){func(v map[string]any) { v["organization_id"] = domain.NewID() }, func(v map[string]any) { v["events"].([]any)[0].(map[string]any)["authorization"] = "private" }, func(v map[string]any) { v["events"].([]any)[0].(map[string]any)["application_id"] = domain.NewID() }, func(v map[string]any) {
		v["events"].([]any)[0].(map[string]any)["spec_digest"] = strings.Repeat("f", 64)
	}, func(v map[string]any) {
		v["events"].([]any)[0].(map[string]any)["at_ms"] = time.Now().Add(-2 * time.Minute).UnixMilli()
	}} {
		raw, _ := json.Marshal(batch)
		v := map[string]any{}
		json.Unmarshal(raw, &v)
		mutate(v)
		if got := send(v, true); got.Code != 400 {
			t.Fatal("invalid evidence accepted", got.Code)
		}
	}
	f.store.RevokeNode(t.Context(), f.org, node.ID)
	if got := send(batch, true); got.Code != 401 {
		t.Fatal("revoked node uploaded", got.Code)
	}
}
