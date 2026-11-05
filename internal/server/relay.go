package server

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ecraela/fleet/internal/connectwire"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type relayKey struct{}
type relayAuthentication struct {
	link  domain.ConnectorLink
	token string
}
type relayService struct {
	server  *Server
	rpc     *grpc.Server
	mu      sync.Mutex
	peers   map[string]*relayPeer
	slots   chan struct{}
	limiter *browserLimiter
}
type relayPeer struct {
	ctx         context.Context
	cancel      context.CancelFunc
	link        domain.ConnectorLink
	localOrg    string
	allowManage bool
	send        chan connectwire.Frame
	mu          sync.Mutex
	pending     map[string]chan connectwire.Frame
}

func newRelay(config Config) (*Server, error) {
	if config.Store == nil || config.Authority == nil || config.Local.Origin != "" || len(config.Browser.Secret) < 32 {
		return nil, errors.New("relay requires its own storage, TLS and cloud browser login; local login belongs to the manager")
	}
	if _, ok := config.Store.(store.ConnectorStore); !ok {
		return nil, errors.New("relay requires persistent connector storage")
	}
	s := &Server{config: config, mux: http.NewServeMux()}
	if err := s.configureBrowser(); err != nil {
		return nil, err
	}
	s.registerBrowserRoutes()
	h := &relayService{server: s, peers: make(map[string]*relayPeer), slots: make(chan struct{}, 128), limiter: &browserLimiter{buckets: map[string]int{}, window: time.Now()}}
	h.rpc = grpc.NewServer(grpc.MaxRecvMsgSize(connectwire.MaxFrame+1024), grpc.MaxSendMsgSize(connectwire.MaxFrame+1024), grpc.MaxConcurrentStreams(128))
	h.rpc.RegisterService(&connectwire.Description, h)
	s.relay = h
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "role": "relay"})
	})
	s.mux.HandleFunc("GET /v1/connector", s.authorizeCapability("haproxy.read", s.relayStatus))
	s.mux.HandleFunc("POST /v1/connector", s.authorizeCapability("haproxy.manage", s.relayCreate))
	s.mux.HandleFunc("DELETE /v1/connector", s.authorizeCapability("haproxy.manage", s.relayRevoke))
	s.mux.HandleFunc("POST /v1/connector/claim", s.relayClaim)
	s.mux.HandleFunc("DELETE /v1/connector/claim", s.relayMachineRevoke)
	s.mux.HandleFunc("/", s.relayForward)
	return s, nil
}
func (s *Server) relayStatus(w http.ResponseWriter, r *http.Request, org string) {
	l, err := s.config.Store.(store.ConnectorStore).ConnectorForOrganization(r.Context(), org)
	if errors.Is(err, domain.ErrNotFound) {
		writeJSON(w, 200, map[string]any{"mode": "outbound", "link": nil, "connected": false})
		return
	}
	if err != nil {
		storeError(w, err)
		return
	}
	s.relay.mu.Lock()
	p := s.relay.peers[l.ID]
	connected := p != nil && p.ctx.Err() == nil
	s.relay.mu.Unlock()
	writeJSON(w, 200, map[string]any{"mode": "outbound", "link": l, "connected": connected, "allow_manage": p != nil && p.allowManage})
}
func (s *Server) relayCreate(w http.ResponseWriter, r *http.Request, org string) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	now := time.Now().UTC()
	id := domain.NewID()
	code := id + "." + org + "." + secret()
	l := domain.ConnectorLink{ID: id, OrganizationID: org, Name: strings.TrimSpace(body.Name), PairDigest: digest(code), CreatedAt: now, PairExpiresAt: now.Add(10 * time.Minute)}
	if err := s.config.Store.(store.ConnectorStore).CreateConnector(r.Context(), l); err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"link": l, "code": code})
}
func (s *Server) relayRevoke(w http.ResponseWriter, r *http.Request, org string) {
	l, err := s.config.Store.(store.ConnectorStore).ConnectorForOrganization(r.Context(), org)
	if err != nil {
		storeError(w, err)
		return
	}
	if err := s.config.Store.(store.ConnectorStore).RevokeConnector(r.Context(), org, l.ID); err != nil {
		storeError(w, err)
		return
	}
	s.relay.mu.Lock()
	if p := s.relay.peers[l.ID]; p != nil {
		p.cancel()
	}
	s.relay.mu.Unlock()
	w.WriteHeader(204)
}
func parsePairCode(code string) (string, string, bool) {
	parts := strings.Split(code, ".")
	if len(parts) != 3 || !domain.ValidID(parts[0]) || !domain.ValidID(parts[1]) || !validBrowserCredential(parts[2]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}
func (s *Server) relayClaim(w http.ResponseWriter, r *http.Request) {
	if !s.relay.limiter.admit(r.RemoteAddr) {
		fail(w, 429, "connector claim rate limit")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	var body struct {
		Code  string `json:"code"`
		Token string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	id, org, ok := parsePairCode(body.Code)
	if !ok || !validBrowserCredential(body.Token) {
		fail(w, 400, "valid pairing code and credential required")
		return
	}
	l, err := s.config.Store.(store.ConnectorStore).ClaimConnector(r.Context(), id, digest(body.Code), digest(body.Token), time.Now())
	if err != nil || l.OrganizationID != org {
		fail(w, 401, "pairing code expired, consumed or revoked")
		return
	}
	writeJSON(w, 200, map[string]any{"link": l})
}
func (h *relayService) serve(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor != 2 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
		fail(w, 400, "gRPC over TLS required")
		return
	}
	if !h.limiter.admit(r.RemoteAddr) {
		fail(w, 429, "connector connection rate limit")
		return
	}
	id := r.Header.Get("X-Connector-ID")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !validBrowserCredential(token) {
		fail(w, 401, "connector credential required")
		return
	}
	l, err := h.server.config.Store.(store.ConnectorStore).AuthenticateConnector(r.Context(), id, digest(token))
	if err != nil {
		fail(w, 401, "connector unavailable or revoked")
		return
	}
	if !acquire(h.slots) {
		fail(w, 429, "connector capacity reached")
		return
	}
	defer func() { <-h.slots }()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	h.rpc.ServeHTTP(w, r.WithContext(context.WithValue(ctx, relayKey{}, relayAuthentication{l, digest(token)})))
}
func (h *relayService) Connect(stream grpc.ServerStream) error {
	auth, ok := stream.Context().Value(relayKey{}).(relayAuthentication)
	if !ok {
		return status.Error(codes.Unauthenticated, "connector authentication required")
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	p := &relayPeer{ctx: ctx, cancel: cancel, link: auth.link, send: make(chan connectwire.Frame, 64), pending: map[string]chan connectwire.Frame{}}
	type received struct {
		f connectwire.Frame
		e error
	}
	inbox := make(chan received, 1)
	go func() {
		for {
			f, e := connectwire.Receive(stream)
			select {
			case inbox <- received{f, e}:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()
	select {
	case item := <-inbox:
		if item.e != nil || item.f.Kind != "hello" || !domain.ValidID(item.f.OrganizationID) {
			return status.Error(codes.InvalidArgument, "installation hello required")
		}
		p.localOrg = item.f.OrganizationID
		p.allowManage = item.f.AllowManage
	case <-time.After(5 * time.Second):
		return status.Error(codes.DeadlineExceeded, "installation hello overdue")
	case <-ctx.Done():
		return ctx.Err()
	}
	h.mu.Lock()
	if old := h.peers[p.link.ID]; old != nil {
		old.cancel()
	}
	h.peers[p.link.ID] = p
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.peers[p.link.ID] == p {
			delete(h.peers, p.link.ID)
		}
		h.mu.Unlock()
	}()
	sendDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				sendDone <- ctx.Err()
				return
			case f := <-p.send:
				if err := connectwire.Send(stream, f); err != nil {
					sendDone <- err
					return
				}
			}
		}
	}()
	p.send <- connectwire.Frame{Kind: "ready", OrganizationID: auth.link.OrganizationID}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-sendDone:
			return e
		case <-tick.C:
			if time.Since(last) > 8*time.Second {
				return status.Error(codes.DeadlineExceeded, "connector heartbeat overdue")
			}
			check, done := context.WithTimeout(ctx, time.Second)
			_, err := h.server.config.Store.(store.ConnectorStore).AuthenticateConnector(check, auth.link.ID, auth.token)
			done()
			if err != nil {
				return status.Error(codes.PermissionDenied, "connector revoked or storage unavailable")
			}
		case item := <-inbox:
			if item.e != nil {
				return item.e
			}
			last = time.Now()
			f := item.f
			if f.Kind == "ping" {
				select {
				case p.send <- connectwire.Frame{Kind: "pong"}:
				default:
					return status.Error(codes.ResourceExhausted, "connector backpressure")
				}
				continue
			}
			if (f.Kind != "headers" && f.Kind != "data" && f.Kind != "end") || !domain.ValidID(f.ID) || len(f.Data) > 16<<10 {
				return status.Error(codes.InvalidArgument, "invalid connector response")
			}
			p.mu.Lock()
			ch := p.pending[f.ID]
			if ch != nil {
				select {
				case ch <- f:
				default:
					delete(p.pending, f.ID)
					select {
					case p.send <- connectwire.Frame{Kind: "cancel", ID: f.ID}:
					default:
						cancel()
					}
				}
			}
			p.mu.Unlock()
		}
	}
}
func (s *Server) relayForward(w http.ResponseWriter, r *http.Request) {
	if !connectwire.Allowed(r.Method, r.URL.Path) || r.URL.RawPath != "" || len(r.URL.RawQuery) > 4096 {
		http.NotFound(w, r)
		return
	}
	capability := "haproxy.read"
	if r.Method != "GET" && !(r.Method == "POST" && r.URL.Path == "/v1/enrollment-status") {
		capability = "haproxy.manage"
	}
	s.authorizeCapability(capability, s.forwardAuthorized)(w, r)
}
func (s *Server) forwardAuthorized(w http.ResponseWriter, r *http.Request, org string) {
	// A paired installation supplies API data, never a document at the cloud origin.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	identity, ok := s.browserIdentity(r)
	if !ok {
		fail(w, 401, "cloud session required")
		return
	}
	l, err := s.config.Store.(store.ConnectorStore).ConnectorForOrganization(r.Context(), org)
	if err != nil {
		fail(w, 503, "link a self-hosted manager to continue")
		return
	}
	s.relay.mu.Lock()
	p := s.relay.peers[l.ID]
	s.relay.mu.Unlock()
	if p == nil || p.ctx.Err() != nil {
		fail(w, 503, "self-hosted manager is offline")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, connectwire.MaxBody+1))
	if err != nil || len(body) > connectwire.MaxBody {
		fail(w, 413, "connector request too large")
		return
	}
	id := domain.NewID()
	ch := make(chan connectwire.Frame, 8)
	p.mu.Lock()
	if len(p.pending) >= connectwire.MaxRequests {
		p.mu.Unlock()
		fail(w, 429, "connector request capacity reached")
		return
	}
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		select {
		case p.send <- connectwire.Frame{Kind: "cancel", ID: id}:
		default:
			p.cancel()
		}
	}()
	expires := minTime(identity.ExpiresAt, time.Now().Add(60*time.Second))
	if !strings.HasSuffix(r.URL.Path, "/events") {
		expires = minTime(expires, time.Now().Add(9*time.Second))
	}
	request := &connectwire.Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body, Headers: map[string]string{}, Identity: connectwire.Identity{UserID: identity.UserID, OrganizationID: org, Capabilities: identity.Capabilities, ExpiresAt: expires}}
	for _, name := range []string{"Accept", "Content-Type", "Last-Event-ID"} {
		if value := r.Header.Get(name); value != "" && len(value) <= 1024 {
			request.Headers[name] = value
		}
	}
	select {
	case p.send <- connectwire.Frame{Kind: "request", ID: id, Request: request}:
	case <-p.ctx.Done():
		fail(w, 503, "connector disconnected")
		return
	default:
		fail(w, 429, "connector backpressure")
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	timer := time.NewTimer(time.Until(expires))
	defer timer.Stop()
	started := false
	headersReceived := false
	total := 0
	// DNS evidence is signed only by the cloud relay, after the linked manager
	// executes the same verified-readiness checks. No DNS provider call is made.
	dns := strings.HasSuffix(r.URL.Path, "/dns-intent") || strings.HasSuffix(r.URL.Path, "/dns-delegation")
	var evidence []byte
	responseStatus := 0
	for {
		select {
		case <-r.Context().Done():
			return
		case <-p.ctx.Done():
			if !started {
				fail(w, 503, "connector disconnected; check operation history before retrying a change")
			}
			return
		case <-timer.C:
			if !started {
				fail(w, 504, "connector request expired; check operation history before retrying a change")
			}
			return
		case <-tick.C:
			if _, ok := s.browserIdentity(r); !ok {
				return
			}
		case f := <-ch:
			switch f.Kind {
			case "headers":
				if headersReceived || f.Status < 200 || f.Status > 599 {
					if !started {
						fail(w, 502, "invalid connector response")
					}
					return
				}
				headersReceived = true
				contentType, valid := relayContentType(r, f)
				if !valid {
					fail(w, 502, "invalid connector response type")
					return
				}
				w.Header().Set("Content-Type", contentType)
				responseStatus = f.Status
				if dns && f.Status == 200 {
					continue
				}
				for _, name := range []string{"Retry-After", "X-Accel-Buffering"} {
					if v := f.Headers[name]; v != "" && len(v) <= 256 && !strings.ContainsAny(v, "\r\n") {
						w.Header().Set(name, v)
					}
				}
				w.WriteHeader(f.Status)
				started = true
			case "data":
				if !headersReceived {
					fail(w, 502, "connector response headers required")
					return
				}
				total += len(f.Data)
				if total > connectwire.MaxResponse {
					return
				}
				if dns && responseStatus == 200 {
					evidence = append(evidence, f.Data...)
					if len(evidence) > 16384 {
						return
					}
					continue
				}
				if !started {
					return
				}
				if _, err := w.Write(f.Data); err != nil {
					return
				}
				if strings.HasSuffix(r.URL.Path, "/events") {
					_ = http.NewResponseController(w).Flush()
				}
			case "end":
				if !headersReceived {
					fail(w, 502, "connector response headers required")
					return
				}
				if dns && responseStatus == 200 {
					s.relayDNSIntent(w, r, identity, p.localOrg, evidence)
				}
				return
			}
		}
	}
}

func relayContentType(r *http.Request, f connectwire.Frame) (string, bool) {
	value := f.Headers["Content-Type"]
	if f.Status == http.StatusNoContent && value == "" {
		return "application/json; charset=utf-8", true
	}
	if len(value) > 256 || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	kind, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", false
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events") && f.Status == http.StatusOK {
		return "text/event-stream; charset=utf-8", kind == "text/event-stream"
	}
	return "application/json; charset=utf-8", kind == "application/json"
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Server) relayMachineRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.relay.limiter.admit(r.RemoteAddr) {
		fail(w, 429, "connector rate limit")
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !validBrowserCredential(token) {
		fail(w, 401, "connector credential required")
		return
	}
	l, err := s.config.Store.(store.ConnectorStore).AuthenticateConnector(r.Context(), r.Header.Get("X-Connector-ID"), digest(token))
	if err != nil {
		fail(w, 401, "connector revoked")
		return
	}
	if err := s.config.Store.(store.ConnectorStore).RevokeConnector(r.Context(), l.OrganizationID, l.ID); err != nil {
		storeError(w, err)
		return
	}
	s.relay.mu.Lock()
	if p := s.relay.peers[l.ID]; p != nil {
		p.cancel()
	}
	s.relay.mu.Unlock()
	w.WriteHeader(204)
}
