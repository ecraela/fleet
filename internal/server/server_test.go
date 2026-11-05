package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/pki"
	"github.com/ecraela/fleet/internal/store"
	"github.com/ecraela/fleet/internal/telemetry"
	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

const operatorToken = "test-operator-token-0123456789abcdef"

type fixture struct {
	store     *store.Memory
	authority *pki.Authority
	server    *Server
	org       string
	cluster   domain.Cluster
}

func newFixture(t *testing.T, role, metricsURL string) *fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pki")
	if err := pki.Init(dir, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		t.Fatal(err)
	}
	a, err := pki.Load(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	db := store.NewMemory()
	org := domain.NewID()
	cluster, err := db.CreateCluster(context.Background(), org, "test pair")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Role: role, Store: db, Authority: a, MetricsURL: metricsURL, Operators: []OperatorCredential{{OrganizationID: org, TokenSHA256: digest(operatorToken)}}})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{store: db, authority: a, server: s, org: org, cluster: cluster}
}

func (f *fixture) token(t *testing.T, expires time.Time) string {
	t.Helper()
	value := secret()
	if err := f.store.CreateEnrollmentToken(context.Background(), f.org, f.cluster.ID, digest(value), expires); err != nil {
		t.Fatal(err)
	}
	return value
}

func csr(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), key
}

func (f *fixture) request(method, path string, body any, authenticated bool) *httptest.ResponseRecorder {
	var input io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		input = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, path, input)
	r.Header.Set("Content-Type", "application/json")
	if authenticated {
		r.Header.Set("Authorization", "Bearer "+operatorToken)
		r.Header.Set("X-Organization-ID", f.org)
	}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	return w
}

func (f *fixture) enrollNode(t *testing.T) (domain.Node, *x509.Certificate, []byte, *ecdsa.PrivateKey) {
	t.Helper()
	requestCSR, key := csr(t)
	w := f.request("POST", "/v1/enroll", map[string]string{"token": f.token(t, time.Now().Add(time.Minute)), "name": "node-a", "csr_pem": requestCSR}, false)
	if w.Code != 201 {
		t.Fatalf("enroll: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Node        domain.Node `json:"node"`
		Certificate string      `json:"certificate_pem"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(response.Certificate))
	if block == nil {
		t.Fatal("missing certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return response.Node, leaf, []byte(response.Certificate), key
}

func TestEnrollmentSingleUseAndCSRValidation(t *testing.T) {
	f := newFixture(t, "manager", "http://127.0.0.1:8428")
	token := f.token(t, time.Now().Add(time.Minute))
	bad := f.request("POST", "/v1/enroll", map[string]string{"token": token, "name": "node", "csr_pem": "bad"}, false)
	if bad.Code != 400 {
		t.Fatalf("bad CSR: %d", bad.Code)
	}
	requestCSR, _ := csr(t)
	body := map[string]string{"token": token, "name": "node", "csr_pem": requestCSR}
	w := f.request("POST", "/v1/enroll", body, false)
	if w.Code != 201 {
		t.Fatalf("valid CSR after invalid CSR: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), token) {
		t.Fatal("enrollment leaked credential")
	}
	if replay := f.request("POST", "/v1/enroll", body, false); replay.Code != 401 {
		t.Fatalf("reused token: %d", replay.Code)
	}
	expired := f.token(t, time.Now().Add(5*time.Millisecond))
	time.Sleep(10 * time.Millisecond)
	body["token"] = expired
	if w := f.request("POST", "/v1/enroll", body, false); w.Code != 401 {
		t.Fatalf("expired token: %d", w.Code)
	}
}

func TestTenantBoundaryOnEveryOperatorEndpoint(t *testing.T) {
	f := newFixture(t, "manager", "http://127.0.0.1:8428")
	node, _, _, _ := f.enrollNode(t)
	otherOrg := domain.NewID()
	otherCluster, _ := f.store.CreateCluster(context.Background(), otherOrg, "other")
	f.server.config.Operators = append(f.server.config.Operators, OperatorCredential{OrganizationID: otherOrg, TokenSHA256: digest("other-operator-token-0123456789abcdef")})
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/v1/clusters", map[string]string{"name": "pair"}},
		{"POST", "/v1/clusters/" + f.cluster.ID + "/enrollment-tokens", map[string]int{"ttl_seconds": 600}},
		{"DELETE", "/v1/enrollment-tokens", map[string]string{"token": "not-a-real-enrollment-token-0123456789"}},
		{"GET", "/v1/nodes", nil},
		{"POST", "/v1/nodes/" + node.ID + "/revoke", nil},
		{"GET", "/v1/nodes/" + node.ID + "/metrics", nil},
		{"GET", "/v1/nodes/" + node.ID + "/events", nil},
	} {
		if w := f.request(tc.method, tc.path, tc.body, false); w.Code != 401 {
			t.Fatalf("anonymous %s: %d", tc.path, w.Code)
		}
	}
	for _, path := range []string{"/v1/nodes/" + node.ID + "/metrics", "/v1/nodes/" + node.ID + "/events", "/v1/nodes/" + node.ID + "/revoke"} {
		method := "GET"
		if strings.HasSuffix(path, "/revoke") {
			method = "POST"
		}
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer other-operator-token-0123456789abcdef")
		r.Header.Set("X-Organization-ID", otherOrg)
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("cross-tenant %s: %d", path, w.Code)
		}
	}
	if w := f.request("POST", "/v1/clusters/"+otherCluster.ID+"/enrollment-tokens", map[string]int{"ttl_seconds": 600}, true); w.Code != 404 {
		t.Fatalf("cross-tenant token issuance: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/v1/nodes", nil)
	r.Header.Set("Authorization", "Bearer "+operatorToken)
	r.Header.Set("X-Organization-ID", otherOrg)
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("organization selector expanded authority")
	}
}

func sampleBatch(t *testing.T, label string) []byte {
	t.Helper()
	labels := []prompb.Label{{Name: "__name__", Value: "haproxy_process_current_connections"}}
	if label != "" {
		labels = append(labels, prompb.Label{Name: label, Value: "spoof"})
	}
	req := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{{Labels: labels, Samples: []prompb.Sample{{Value: 12, Timestamp: time.Now().UnixMilli()}}}}}
	raw, err := req.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return snappy.Encode(nil, raw)
}

func nodeRequest(f *fixture, leaf *x509.Certificate, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/metrics/write", bytes.NewReader(body))
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, f.authority.Certificate}}}
	r.Header.Set("Content-Type", "application/x-protobuf")
	r.Header.Set("Content-Encoding", "snappy")
	r.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	return w
}

func TestIngestionScopesAndAcknowledgesStorage(t *testing.T) {
	var got *prompb.WriteRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/write" {
			t.Errorf("bad storage path %s", r.URL.Path)
		}
		encoded, _ := io.ReadAll(r.Body)
		raw, err := snappy.Decode(nil, encoded)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		got = new(prompb.WriteRequest)
		if err := got.Unmarshal(raw); err != nil {
			t.Error(err)
		}
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	f := newFixture(t, "manager", upstream.URL)
	node, leaf, _, _ := f.enrollNode(t)
	f.server.config.Role = "ingest"
	ingest, err := New(f.server.config)
	if err != nil {
		t.Fatal(err)
	}
	f.server = ingest
	if w := nodeRequest(f, leaf, sampleBatch(t, "")); w.Code != 204 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	labels := map[string]string{}
	for _, l := range got.Timeseries[0].Labels {
		labels[l.Name] = l.Value
	}
	if labels["organization_id"] != node.OrganizationID || labels["cluster_id"] != node.ClusterID || labels["node_id"] != node.ID {
		t.Fatal("ingestion lost authenticated scope")
	}
	if w := nodeRequest(f, leaf, sampleBatch(t, "organization_id")); w.Code != 400 {
		t.Fatalf("accepted spoof: %d", w.Code)
	}
	if w := nodeRequest(f, leaf, make([]byte, telemetry.MaxCompressed+1)); w.Code != 400 {
		t.Fatalf("accepted oversized batch: %d", w.Code)
	}
	if err := f.store.RevokeNode(context.Background(), f.org, node.ID); err != nil {
		t.Fatal(err)
	}
	if w := nodeRequest(f, leaf, sampleBatch(t, "")); w.Code != 401 {
		t.Fatalf("revoked connection retained access: %d", w.Code)
	}
}

func TestIngestionFailureIsNotSuccessfulAcknowledgment(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer upstream.Close()
	f := newFixture(t, "manager", upstream.URL)
	_, leaf, _, _ := f.enrollNode(t)
	f.server.config.Role = "ingest"
	f.server, _ = New(f.server.config)
	if w := nodeRequest(f, leaf, sampleBatch(t, "")); w.Code != 503 {
		t.Fatalf("storage failure acknowledged: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/v1/metrics/write", bytes.NewReader(sampleBatch(t, "")))
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("anonymous ingestion: %d", w.Code)
	}
}

func TestIngestionRequiresMTLSAndStoredCertificateScope(t *testing.T) {
	f := newFixture(t, "manager", "http://127.0.0.1:8428")
	node, leaf, _, _ := f.enrollNode(t)
	f.server.config.Role = "ingest"
	f.server, _ = New(f.server.config)
	wrongNode := node
	wrongNode.OrganizationID = domain.NewID()
	requestCSR, _ := csr(t)
	certPEM, err := f.authority.Issue(requestCSR, wrongNode, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	wrongLeaf, _ := x509.ParseCertificate(block.Bytes)
	if w := nodeRequest(f, wrongLeaf, sampleBatch(t, "")); w.Code != 401 {
		t.Fatalf("certificate tenant mismatch accepted: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/v1/metrics/write", bytes.NewReader(sampleBatch(t, "")))
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("unverified certificate trusted")
	}
	https := httptest.NewUnstartedServer(f.server)
	https.TLS = f.server.TLSConfig()
	https.StartTLS()
	defer https.Close()
	if response, err := https.Client().Get(https.URL + "/healthz"); err == nil {
		response.Body.Close()
		t.Fatal("ingestion TLS accepted absent client certificate")
	}
}

func TestLiveStreamSurvivesHTTP2PollInterval(t *testing.T) {
	var queries atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if queries.Add(1) > 1 {
			time.Sleep(150 * time.Millisecond)
		}
		writeJSON(w, 200, map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{}}})
	}))
	defer upstream.Close()
	f := newFixture(t, "manager", upstream.URL)
	node, _, _, _ := f.enrollNode(t)
	https := httptest.NewUnstartedServer(f.server)
	https.EnableHTTP2 = true
	https.StartTLS()
	defer https.Close()
	client := https.Client()
	client.Timeout = 8 * time.Second
	request, _ := http.NewRequest("GET", https.URL+"/v1/nodes/"+node.ID+"/events", nil)
	request.Header.Set("Authorization", "Bearer "+operatorToken)
	request.Header.Set("X-Organization-ID", f.org)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatal("regression test requires HTTP/2")
	}
	reader := bufio.NewReader(response.Body)
	for events := 0; events < 2; {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("live stream ended before second observation: %v", err)
		}
		if strings.HasPrefix(line, "event: metrics") {
			events++
		}
	}
}

func TestMetricsFreshnessAndTenantQuery(t *testing.T) {
	var node domain.Node
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, `organization_id="`+node.OrganizationID+`"`) || !strings.Contains(query, `node_id="`+node.ID+`"`) {
			t.Error("unscoped query")
		}
		writeJSON(w, 200, map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{
			map[string]any{"metric": map[string]string{"__name__": "converge_agent_scrape_timestamp_seconds", "organization_id": node.OrganizationID, "cluster_id": node.ClusterID, "node_id": node.ID}, "value": []any{time.Now().Unix(), fmtFloat(time.Now().Unix())}},
			map[string]any{"metric": map[string]string{"__name__": "converge_agent_haproxy_up", "organization_id": node.OrganizationID, "cluster_id": node.ClusterID, "node_id": node.ID}, "value": []any{time.Now().Unix(), "1"}},
		}}})
	}))
	defer upstream.Close()
	f := newFixture(t, "manager", upstream.URL)
	node, _, _, _ = f.enrollNode(t)
	w := f.request("GET", "/v1/nodes/"+node.ID+"/metrics", nil, true)
	if w.Code != 200 {
		t.Fatalf("metrics: %d %s", w.Code, w.Body.String())
	}
	var snap Snapshot
	if json.Unmarshal(w.Body.Bytes(), &snap) != nil || !snap.Fresh || !snap.ScrapeOK || snap.ObservedAt == nil {
		t.Fatalf("freshness unavailable: %s", w.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("GET", "/v1/nodes/"+node.ID+"/events", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+operatorToken)
	r.Header.Set("X-Organization-ID", f.org)
	recorder := &cancelOnFlush{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	f.server.ServeHTTP(recorder, r)
	if recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatal("missing live stream")
	}
}

type cancelOnFlush struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *cancelOnFlush) Flush() {
	w.ResponseRecorder.Flush()
	w.cancel()
}

func fmtFloat(n int64) string { data, _ := json.Marshal(n); return string(data) }
