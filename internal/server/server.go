// Package server exposes the manager and ingestion roles without depending on
// Phoenix. Both deployment modes use these same handlers and tenant boundaries.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/pki"
	"github.com/ecraela/fleet/internal/store"
	"github.com/ecraela/fleet/internal/telemetry"
	"github.com/ecraela/fleet/internal/webui"
	"google.golang.org/grpc"
)

type Config struct {
	Connector     ConnectorConfig
	LogsURL       string
	LogsClient    *http.Client
	Notifications []NotificationTarget
	Role          string
	Store         store.Store
	Authority     *pki.Authority
	Operators     []OperatorCredential
	MetricsURL    string
	Client        *http.Client
	Local         LocalConfig
	Browser       BrowserConfig
	AgentSetup    AgentSetup
}

type Server struct {
	connector          *connectorClient
	relay              *relayService
	logs               *accesslog.Storage
	logIngestion       chan struct{}
	logQueries         chan struct{}
	logBudget          *accesslog.Budget
	notificationClient *http.Client
	controlRPC         *grpc.Server
	controls           chan struct{}
	controlReports     *nodeReportBudget
	config             Config
	mux                *http.ServeMux
	metrics            *url.URL
	client             *http.Client
	ingestion          chan struct{}
	queries            chan struct{}
	streams            chan struct{}
	mu                 sync.Mutex
	active             map[string]bool
	enrollers          map[string]int
	window             time.Time
	budget             *seriesBudget
}

func New(config Config) (*Server, error) {
	if config.Role == "relay" {
		return newRelay(config)
	}
	if err := validateNotificationTargets(config.Notifications); err != nil {
		return nil, err
	}
	if config.Role != "manager" && config.Role != "ingest" {
		return nil, errors.New("role must be manager or ingest")
	}
	if config.Store == nil || config.Authority == nil || (config.Role == "manager" && len(config.Operators) == 0) {
		return nil, errors.New("store, authority, and manager operator credentials are required")
	}
	metrics, err := url.Parse(config.MetricsURL)
	if err != nil || (metrics.Scheme != "http" && metrics.Scheme != "https") || metrics.Host == "" || metrics.User != nil || metrics.RawQuery != "" || metrics.Fragment != "" {
		return nil, errors.New("metrics URL must be an HTTP(S) service base URL")
	}
	client := config.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{MaxConnsPerHost: 16, MaxIdleConnsPerHost: 16, IdleConnTimeout: 30 * time.Second}}
	}
	// A redirect must not move authenticated node telemetry to another service.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s := &Server{config: config, metrics: metrics, client: &copyClient, mux: http.NewServeMux(),
		ingestion: make(chan struct{}, 8), queries: make(chan struct{}, 8), streams: make(chan struct{}, 32),
		active: make(map[string]bool), enrollers: make(map[string]int), window: time.Now(), budget: newSeriesBudget()}
	s.notificationClient = newNotificationClient()
	s.logIngestion = make(chan struct{}, 4)
	s.logQueries = make(chan struct{}, 4)
	s.logBudget = accesslog.NewBudget()
	if config.LogsURL != "" {
		s.logs, err = accesslog.NewStorage(config.LogsURL, config.LogsClient)
		if err != nil {
			return nil, err
		}
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "role": config.Role})
	})
	if config.Role == "manager" {
		s.configureControl()
		s.configureRollouts()
		s.configureApplications()
		s.configureRequestTelemetry()
		s.configureListeners()
		s.configureACME()
		s.configureIncidents()
		s.configureMaintenance()
		if err := s.validateAgentSetup(); err != nil {
			return nil, err
		}
		if err := s.configureBrowser(); err != nil {
			return nil, err
		}
		s.registerBrowserRoutes()
		if err := s.configureLocal(); err != nil {
			return nil, err
		}
		if err := s.configureConnector(); err != nil {
			return nil, err
		}
		if s.config.Local.Origin != "" {
			s.mux.Handle("/", webui.Handler())
		}
		s.mux.HandleFunc("GET /v1/agent-setup", s.authorizeCapability("haproxy.read", s.agentSetup))
		s.mux.HandleFunc("POST /v1/enrollment-status", s.authorizeCapability("haproxy.read", s.enrollmentStatus))
		s.mux.HandleFunc("POST /v1/enroll", s.enroll)
		s.mux.HandleFunc("POST /v1/identity/renew", s.renew)
		s.mux.HandleFunc("GET /v1/clusters", s.authorizeCapability("haproxy.read", s.listClusters))
		s.mux.HandleFunc("GET /v1/clusters/{cluster}/events", s.authorizeCapability("haproxy.read", s.clusterEvents))
		s.mux.HandleFunc("GET /v1/fleet", s.authorizeCapability("haproxy.read", s.fleet))
		s.mux.HandleFunc("PUT /v1/clusters/{cluster}/topology", s.authorizeCapability("haproxy.manage", s.setTopology))
		s.mux.HandleFunc("POST /v1/clusters", s.authorizeCapability("haproxy.manage", s.createCluster))
		s.mux.HandleFunc("POST /v1/clusters/{cluster}/enrollment-tokens", s.authorizeCapability("haproxy.manage", s.createToken))
		s.mux.HandleFunc("DELETE /v1/enrollment-tokens", s.authorizeCapability("haproxy.manage", s.revokeToken))
		s.mux.HandleFunc("GET /v1/nodes", s.authorizeCapability("haproxy.read", s.listNodes))
		s.mux.HandleFunc("POST /v1/nodes/{node}/revoke", s.authorizeCapability("haproxy.manage", s.revokeNode))
		s.mux.HandleFunc("GET /v1/nodes/{node}/metrics", s.authorizeCapability("haproxy.read", s.nodeMetrics))
		s.mux.HandleFunc("GET /v1/nodes/{node}/history", s.authorizeCapability("haproxy.read", s.nodeHistory))
		s.mux.HandleFunc("GET /v1/nodes/{node}/events", s.authorizeCapability("haproxy.read", s.nodeEvents))
	} else {
		s.mux.HandleFunc("POST /v1/metrics/write", s.ingest)
		s.mux.HandleFunc("POST /v1/logs/write", s.ingestLogs)
	}
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if s.relay != nil && r.URL.Path == "/ecraela.haproxy.connector.v1.Relay/Connect" {
		s.relay.serve(w, r)
		return
	}
	if s.config.Role == "manager" && r.URL.Path == "/converge.haproxy.control.v1.Control/Connect" {
		s.serveControl(w, r)
		return
	}
	if s.config.Role == "manager" && s.config.Local.Origin != "" && strings.HasPrefix(r.URL.Path, "/api/haproxy/v1/") {
		r = r.Clone(r.Context())
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/haproxy")
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) TLSConfig() *tls.Config {
	return s.config.Authority.TLSConfig(s.config.Role == "ingest")
}

func (s *Server) authorizeCapability(capability string, handler func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if org, ok := s.operator(r); ok {
			handler(w, r, org)
			return
		}
		identity, ok := s.managementIdentity(r)
		if !ok {
			fail(w, 401, "valid organization session required")
			return
		}
		if requested := r.Header.Get("X-Organization-ID"); requested != "" && requested != identity.OrganizationID {
			fail(w, 401, "organization session does not match request")
			return
		}
		if !identity.Has(capability) {
			fail(w, 403, "HAProxy permission required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.managementCSRF(r) {
			fail(w, 403, "valid origin and CSRF token required")
			return
		}
		handler(w, r.WithContext(domain.WithActor(r.Context(), identity.UserID)), identity.OrganizationID)
	}
}

func (s *Server) createCluster(w http.ResponseWriter, r *http.Request, org string) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	cluster, err := s.config.Store.CreateCluster(r.Context(), org, body.Name)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"cluster": cluster})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, org string) {
	var body struct {
		TTL int `json:"ttl_seconds"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.TTL == 0 {
		body.TTL = 600
	}
	if body.TTL < 1 || body.TTL > 3600 {
		fail(w, 400, "ttl_seconds must be between 1 and 3600")
		return
	}
	token := secret()
	expires := time.Now().UTC().Add(time.Duration(body.TTL) * time.Second)
	if err := s.config.Store.CreateEnrollmentToken(r.Context(), org, r.PathValue("cluster"), digest(token), expires); err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "expires_at": expires})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, org string) {
	var body struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Token) < 32 || len(body.Token) > 256 {
		fail(w, 400, "invalid token")
		return
	}
	if err := s.config.Store.RevokeEnrollmentToken(r.Context(), org, digest(body.Token)); err != nil {
		storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	if !s.admitEnrollment(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "enrollment rate limit")
		return
	}
	var body struct {
		Token string `json:"token"`
		Name  string `json:"name"`
		CSR   string `json:"csr_pem"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Token) < 32 || len(body.Token) > 256 || strings.TrimSpace(body.Name) == "" || len(body.Name) > 255 {
		fail(w, 400, "invalid enrollment request")
		return
	}
	now := time.Now().UTC()
	enrollment, err := s.config.Store.LookupEnrollment(r.Context(), digest(body.Token), now)
	if err != nil {
		storeError(w, err)
		return
	}
	node := domain.Node{ID: domain.NewID(), OrganizationID: enrollment.OrganizationID, ClusterID: enrollment.ClusterID, Name: body.Name}
	cert, err := s.config.Authority.Issue(body.CSR, node, now)
	if err != nil {
		fail(w, 400, "invalid certificate request")
		return
	}
	// Only certificates for the winning atomic enrollment become authorized.
	node, err = s.config.Store.Enroll(r.Context(), digest(body.Token), node, now)
	if err != nil {
		storeError(w, err)
		return
	}
	s.identityResponse(w, http.StatusCreated, node, cert)
}

func (s *Server) renew(w http.ResponseWriter, r *http.Request) {
	node, err := s.authenticateNode(r)
	if err != nil {
		fail(w, 401, "valid node certificate required")
		return
	}
	var body struct {
		CSR string `json:"csr_pem"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	cert, err := s.config.Authority.Issue(body.CSR, node, time.Now().UTC())
	if err != nil {
		fail(w, 400, "invalid certificate request")
		return
	}
	// Recheck revocation after signing before releasing the new certificate.
	if _, err := s.config.Store.AuthenticateNode(r.Context(), node.ID); err != nil {
		fail(w, 401, "node revoked")
		return
	}
	s.identityResponse(w, http.StatusOK, node, cert)
}

func (s *Server) identityResponse(w http.ResponseWriter, code int, node domain.Node, cert []byte) {
	writeJSON(w, code, map[string]any{"node": node, "certificate_pem": string(cert), "ca_pem": string(s.config.Authority.PEM)})
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request, org string) {
	nodes, err := s.config.Store.ListNodes(r.Context(), org)
	if err != nil {
		storeError(w, err)
		return
	}
	if nodes == nil {
		nodes = []domain.Node{}
	}
	writeJSON(w, 200, map[string]any{"nodes": nodes})
}

func (s *Server) revokeNode(w http.ResponseWriter, r *http.Request, org string) {
	if err := s.config.Store.RevokeNode(r.Context(), org, r.PathValue("node")); err != nil {
		storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authenticateNode(r *http.Request) (domain.Node, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return domain.Node{}, domain.ErrNotFound
	}
	identity, err := pki.Identity(r.TLS.PeerCertificates[0])
	if err != nil {
		return domain.Node{}, err
	}
	node, err := s.config.Store.AuthenticateNode(r.Context(), identity.ID)
	if err != nil {
		return domain.Node{}, err
	}
	if node.OrganizationID != identity.OrganizationID || node.ClusterID != identity.ClusterID {
		return domain.Node{}, domain.ErrNotFound
	}
	return node, nil
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	node, err := s.authenticateNode(r)
	if err != nil {
		fail(w, 401, "valid node certificate required")
		return
	}
	if !acquire(s.ingestion) {
		w.Header().Set("Retry-After", "1")
		fail(w, 429, "ingestion capacity reached")
		return
	}
	defer func() { <-s.ingestion }()
	s.mu.Lock()
	if s.active[node.ID] {
		s.mu.Unlock()
		fail(w, 429, "node upload already in progress")
		return
	}
	s.active[node.ID] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, node.ID); s.mu.Unlock() }()
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-protobuf" || r.Header.Get("Content-Encoding") != "snappy" || r.Header.Get("X-Prometheus-Remote-Write-Version") != "0.1.0" {
		fail(w, 415, "Prometheus Remote Write 1.0 with Snappy is required")
		return
	}
	batch, err := telemetry.Decode(http.MaxBytesReader(w, r.Body, telemetry.MaxCompressed))
	if err != nil {
		fail(w, 400, "invalid or oversized telemetry batch")
		return
	}
	now := time.Now()
	for _, series := range batch.Timeseries {
		for _, sample := range series.Samples {
			when := time.UnixMilli(sample.Timestamp)
			if when.Before(now.Add(-30*time.Second)) || when.After(now.Add(time.Minute)) {
				fail(w, 400, "telemetry timestamp outside collection window")
				return
			}
		}
	}
	if !s.budget.admit(node, batch, now) {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "active series budget reached")
		return
	}
	if latest := telemetry.LiveSummary(batch); latest != nil {
		if persistence, ok := s.config.Store.(store.LiveStore); ok {
			liveCtx, cancelLive := context.WithTimeout(r.Context(), time.Second)
			err := persistence.RecordLive(liveCtx, node, *latest)
			cancelLive()
			if err != nil {
				fail(w, 503, "latest observations unavailable")
				return
			}
		}
	}
	batch, err = telemetry.Stamp(batch, telemetry.Scope{OrganizationID: node.OrganizationID, ClusterID: node.ClusterID, NodeID: node.ID})
	if err != nil {
		fail(w, 400, "invalid telemetry scope")
		return
	}
	encoded, err := telemetry.EncodeStamped(batch)
	if err != nil {
		fail(w, 400, "invalid telemetry batch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, s.storageURL("/api/v1/write").String(), bytes.NewReader(encoded))
	if err != nil {
		fail(w, 503, "metrics storage unavailable")
		return
	}
	upstream.Header.Set("Content-Type", "application/x-protobuf")
	upstream.Header.Set("Content-Encoding", "snappy")
	upstream.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	upstream.Header.Set("User-Agent", "Converge-HAProxy-Ingest/0.1")
	response, err := s.client.Do(upstream)
	if err != nil {
		fail(w, 503, "metrics storage unavailable")
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		fail(w, 503, "metrics storage rejected batch")
		return
	}
	// Acknowledge only after the configured storage endpoint acknowledges. This
	// is not a promise of a per-batch fsync by that endpoint.
	w.WriteHeader(http.StatusNoContent)
}

type Snapshot struct {
	TrafficFrontend string          `json:"traffic_frontend,omitempty"`
	NodeID          string          `json:"node_id"`
	ObservedAt      *time.Time      `json:"observed_at"`
	Fresh           bool            `json:"fresh"`
	ScrapeOK        bool            `json:"scrape_ok"`
	Data            json.RawMessage `json:"data"`
}

func (s *Server) nodeMetrics(w http.ResponseWriter, r *http.Request, org string) {
	node, err := s.config.Store.GetNode(r.Context(), org, r.PathValue("node"))
	if err != nil {
		storeError(w, err)
		return
	}
	snapshot, err := s.snapshot(r.Context(), node)
	if err != nil {
		fail(w, 503, "metrics query unavailable")
		return
	}
	writeJSON(w, 200, snapshot)
}

func (s *Server) snapshot(ctx context.Context, node domain.Node) (Snapshot, error) {
	if !acquire(s.queries) {
		return Snapshot{}, errors.New("query limit")
	}
	defer func() { <-s.queries }()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	u := s.storageURL("/api/v1/query")
	q := u.Query()
	q.Set("query", fmt.Sprintf(`{__name__=~"haproxy_.*|converge_agent_.*",organization_id="%s",cluster_id="%s",node_id="%s"}`, node.OrganizationID, node.ClusterID, node.ID))
	q.Set("nocache", "1")
	q.Set("latency_offset", "1ms")
	q.Set("timeout", "2s")
	u.RawQuery = q.Encode()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Snapshot{}, err
	}
	response, err := s.client.Do(r)
	if err != nil {
		return Snapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Snapshot{}, errors.New("storage query failed")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil || len(data) > 2<<20 {
		return Snapshot{}, errors.New("query response too large")
	}
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Status != "success" || len(envelope.Data) == 0 {
		return Snapshot{}, errors.New("invalid storage response")
	}
	var vector struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(envelope.Data, &vector) != nil || vector.ResultType != "vector" || len(vector.Result) > telemetry.MaxSeries {
		return Snapshot{}, errors.New("invalid metric vector")
	}
	snapshot := Snapshot{NodeID: node.ID, Data: envelope.Data, TrafficFrontend: s.trafficFrontend(ctx, node)}
	for _, point := range vector.Result {
		if point.Metric["organization_id"] != node.OrganizationID || point.Metric["cluster_id"] != node.ClusterID || point.Metric["node_id"] != node.ID {
			return Snapshot{}, errors.New("storage scope mismatch")
		}
		if len(point.Value) != 2 {
			return Snapshot{}, errors.New("invalid metric value")
		}
		var value string
		if json.Unmarshal(point.Value[1], &value) != nil {
			return Snapshot{}, errors.New("invalid metric value")
		}
		numeric, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(numeric) || math.IsInf(numeric, 0) {
			return Snapshot{}, errors.New("invalid metric value")
		}
		var evaluatedAt float64
		if json.Unmarshal(point.Value[0], &evaluatedAt) != nil || math.IsNaN(evaluatedAt) || math.IsInf(evaluatedAt, 0) {
			return Snapshot{}, errors.New("invalid metric evaluation timestamp")
		}
		if point.Metric["__name__"] == "converge_agent_haproxy_up" {
			if !telemetry.ValidStatusValue("converge_agent_haproxy_up", numeric) {
				return Snapshot{}, errors.New("invalid HAProxy availability")
			}
			snapshot.ScrapeOK = numeric == 1
		}
		if point.Metric["__name__"] == "converge_agent_scrape_timestamp_seconds" {
			seconds := numeric
			if telemetry.ValidStatusValue("converge_agent_scrape_timestamp_seconds", seconds) {
				observed := time.UnixMilli(int64(seconds * 1000)).UTC()
				snapshot.ObservedAt = &observed
				age := time.Since(observed)
				snapshot.Fresh = age >= -time.Minute && age <= 15*time.Second
			} else {
				return Snapshot{}, errors.New("invalid metric observation timestamp")
			}
		}
	}
	return snapshot, nil
}

func (s *Server) nodeEvents(w http.ResponseWriter, r *http.Request, org string) {
	node, err := s.config.Store.GetNode(r.Context(), org, r.PathValue("node"))
	if err != nil {
		storeError(w, err)
		return
	}
	if !acquire(s.streams) {
		fail(w, 429, "stream capacity reached")
		return
	}
	defer func() { <-s.streams }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	controller := http.NewResponseController(w)
	for {
		// Recheck authorization and revocation for every stream observation.
		if !s.canReadOrganization(r, org) {
			return
		}
		current, err := s.config.Store.GetNode(ctx, org, node.ID)
		if err != nil || current.RevokedAt != nil {
			return
		}
		snapshot, err := s.snapshot(ctx, node)
		event := "metrics"
		var payload any = snapshot
		if err != nil {
			event = "unavailable"
			payload = map[string]string{"node_id": node.ID, "error": "metrics query unavailable"}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		// A query may outlive the short browser session or race node revocation.
		// Authorize the actual event publication after that bounded query too.
		if !s.canReadOrganization(r, org) {
			return
		}
		current, err = s.config.Store.GetNode(ctx, org, node.ID)
		if err != nil || current.RevokedAt != nil {
			return
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return
		}
		if controller.Flush() != nil {
			return
		}
		// HTTP/2 enforces write deadlines asynchronously. Keep the bound around
		// each write, not around the idle interval and the next storage query.
		_ = controller.SetWriteDeadline(time.Time{})
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) storageURL(path string) *url.URL {
	u := *s.metrics
	u.Path = strings.TrimRight(u.Path, "/") + path
	return &u
}

func (s *Server) admitEnrollment(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.window) >= time.Minute {
		clear(s.enrollers)
		s.window = time.Now()
	}
	if s.enrollers[host] >= 10 || (s.enrollers[host] == 0 && len(s.enrollers) >= 1024) {
		return false
	}
	s.enrollers[host]++
	return true
}

func acquire(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "application/json required")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		fail(w, 400, "invalid JSON request")
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		fail(w, 400, "one JSON object required")
		return false
	}
	return true
}

func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidEnrollment):
		fail(w, 401, "invalid or expired enrollment token")
	case errors.Is(err, domain.ErrNotFound):
		fail(w, 404, "resource not found")
	case errors.Is(err, domain.ErrRevoked):
		fail(w, 403, "node revoked")
	case errors.Is(err, domain.ErrInvalidInput):
		fail(w, 400, "invalid resource")
	case errors.Is(err, domain.ErrConflict):
		fail(w, 409, "resource conflict")
	default:
		fail(w, 503, "control storage unavailable")
	}
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
func fail(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}
func secret() string {
	var value [32]byte
	_, _ = rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}
