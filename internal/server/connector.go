package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ecraela/fleet/internal/connectwire"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

type ConnectorConfig struct{ RelayURL, StateDir, CAFile string }
type connectorState struct {
	LinkID              string `json:"link_id"`
	LocalOrganizationID string `json:"local_organization_id"`
	CloudOrganizationID string `json:"cloud_organization_id"`
	Token               string `json:"token"`
	PendingCode         string `json:"pending_code,omitempty"`
	RelayURL            string `json:"relay_url"`
	AllowManage         bool   `json:"allow_manage"`
}
type connectorClient struct {
	server *Server
	config ConnectorConfig
	tls    *tls.Config
	http   *http.Client
	lock   *os.File
	mu     sync.Mutex
	state  *connectorState
	status string
	cancel context.CancelFunc
	wake   chan struct{}
}
type connectorRequestKey struct{}
type connectorRequestIdentity struct {
	identity BrowserIdentity
	client   *connectorClient
	state    *connectorState
}

func (s *Server) configureConnector() error {
	c := s.config.Connector
	if c.RelayURL == "" && c.StateDir == "" && c.CAFile == "" {
		return nil
	}
	u, err := url.Parse(c.RelayURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.String() != c.RelayURL || !filepath.IsAbs(c.StateDir) || s.config.Local.Origin == "" {
		return errors.New("connector requires a fixed HTTPS relay origin, absolute private state directory and independent local login")
	}
	if err := os.MkdirAll(c.StateDir, 0700); err != nil {
		return errors.New("cannot create private connector state directory")
	}
	info, err := os.Lstat(c.StateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("connector state directory must be private, mode 0700, and not a symlink")
	}
	lock, err := os.OpenFile(filepath.Join(c.StateDir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return errors.New("cannot lock connector state")
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		return errors.New("another manager owns this connector state")
	}
	valid := false
	defer func() {
		if !valid {
			lock.Close()
		}
	}()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return errors.New("cannot read relay CA")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return errors.New("invalid relay CA")
		}
		tlsConfig.RootCAs = roots
	}
	client := &connectorClient{server: s, config: c, tls: tlsConfig, lock: lock, status: "unlinked", wake: make(chan struct{}, 1)}
	client.http = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig.Clone(), MaxConnsPerHost: 2}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	f, err := os.OpenFile(filepath.Join(c.StateDir, "connector.json"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == nil {
		defer f.Close()
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
			return errors.New("connector state must be a private regular file; refusing to regenerate identity")
		}
		data, e := io.ReadAll(io.LimitReader(f, 8193))
		var state connectorState
		if e != nil || len(data) > 8192 || json.Unmarshal(data, &state) != nil || !domain.ValidID(state.LinkID) || !domain.ValidID(state.LocalOrganizationID) || !domain.ValidID(state.CloudOrganizationID) || !validBrowserCredential(state.Token) || state.RelayURL != c.RelayURL {
			return errors.New("connector state is corrupt or belongs to another relay; refusing to regenerate identity")
		}
		if state.PendingCode != "" {
			id, org, ok := parsePairCode(state.PendingCode)
			if !ok || id != state.LinkID || org != state.CloudOrganizationID {
				return errors.New("invalid pending connector identity")
			}
		}
		client.state = &state
		client.status = "connecting"
	} else if !os.IsNotExist(err) {
		return errors.New("cannot open connector identity")
	}
	s.connector = client
	valid = true
	s.mux.HandleFunc("GET /v1/local/connector", s.localConnector)
	s.mux.HandleFunc("POST /v1/local/connector", s.localConnector)
	s.mux.HandleFunc("DELETE /v1/local/connector", s.localConnector)
	return nil
}
func (c *connectorClient) save(state *connectorState) error {
	path := filepath.Join(c.config.StateDir, "connector.json")
	if state == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		b, err := json.Marshal(state)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(c.config.StateDir, ".connector-")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if _, err = f.Write(b); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = os.Rename(f.Name(), path); err != nil {
			return err
		}
	}
	dir, err := os.Open(c.config.StateDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *Server) localConnector(w http.ResponseWriter, r *http.Request) {
	u, _, _, ok := s.localSession(r)
	if !ok || r.Header.Get("X-HAProxy-Session") != "local" {
		fail(w, 401, "independent local login required")
		return
	}
	c := s.connector
	if r.Method != "GET" && (!s.localCSRF(r) || u.Role != "administrator") {
		fail(w, 403, "local administrator and CSRF token required")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != nil && c.state.LocalOrganizationID != u.OrganizationID {
		fail(w, 409, "this installation already has a connection for another local organization")
		return
	}
	if r.Method == "GET" {
		result := map[string]any{"enabled": true, "relay_url": c.config.RelayURL, "state": c.status, "linked": c.state != nil}
		if c.state != nil {
			result["link_id"] = c.state.LinkID
			result["cloud_organization_id"] = c.state.CloudOrganizationID
			result["allow_manage"] = c.state.AllowManage
		}
		writeJSON(w, 200, result)
		return
	}
	ctx := domain.WithActor(r.Context(), u.ID)
	if r.Method == "DELETE" {
		old := c.state
		if old == nil {
			w.WriteHeader(204)
			return
		}
		if err := s.config.Store.(store.ConnectorStore).RecordConnectorAudit(ctx, u.OrganizationID, old.LinkID, "connector.local_unlinked"); err != nil {
			storeError(w, err)
			return
		}
		if err := c.save(nil); err != nil {
			fail(w, 503, "cannot remove connector credential")
			return
		}
		c.state = nil
		c.status = "unlinked"
		if c.cancel != nil {
			c.cancel()
		}
		select {
		case c.wake <- struct{}{}:
		default:
		}
		// Local revocation is immediate even if the cloud is unavailable.
		go func() { _, _ = c.call(context.Background(), "DELETE", "/v1/connector/claim", nil, old) }()
		w.WriteHeader(204)
		return
	}
	if c.state != nil {
		fail(w, 409, "unlink the existing manager connection first")
		return
	}
	var body struct {
		Code        string `json:"code"`
		AllowManage bool   `json:"allow_manage"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	if !readJSON(w, r, &body) {
		return
	}
	id, org, ok := parsePairCode(strings.TrimSpace(body.Code))
	if !ok {
		fail(w, 400, "paste a valid Converge linking code")
		return
	}
	state := &connectorState{LinkID: id, LocalOrganizationID: u.OrganizationID, CloudOrganizationID: org, Token: secret(), PendingCode: strings.TrimSpace(body.Code), RelayURL: c.config.RelayURL, AllowManage: body.AllowManage}
	if err := s.config.Store.(store.ConnectorStore).RecordConnectorAudit(ctx, u.OrganizationID, id, "connector.local_linked"); err != nil {
		storeError(w, err)
		return
	}
	// Persist the credential before consuming the one-use code. A lost response
	// or restart retries the same identity, never creates a replacement.
	if err := c.save(state); err != nil {
		fail(w, 503, "cannot preserve connector identity")
		return
	}
	c.state = state
	c.status = "connecting"
	select {
	case c.wake <- struct{}{}:
	default:
	}
	writeJSON(w, 202, map[string]string{"state": "connecting"})
}
func (c *connectorClient) call(ctx context.Context, method, path string, body any, state *connectorState) ([]byte, error) {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.config.RelayURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if state != nil {
		r.Header.Set("X-Connector-ID", state.LinkID)
		r.Header.Set("Authorization", "Bearer "+state.Token)
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, errors.New("relay unavailable")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if err != nil || len(b) > 16384 || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("relay rejected the connection; check the code or unlink and create a new link")
	}
	return b, nil
}
func (s *Server) RunConnector(ctx context.Context) {
	c := s.connector
	if c == nil {
		return
	}
	defer c.lock.Close()
	backoff := time.Second
	for ctx.Err() == nil {
		c.mu.Lock()
		state := c.state
		run, cancel := context.WithCancel(ctx)
		c.cancel = cancel
		c.mu.Unlock()
		if state == nil {
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-c.wake:
				continue
			}
		}
		started := time.Now()
		if state != nil {
			if state.PendingCode != "" {
				_, err := c.call(run, "POST", "/v1/connector/claim", map[string]string{"code": state.PendingCode, "token": state.Token}, nil)
				if err == nil {
					c.mu.Lock()
					if c.state == state {
						copy := *state
						copy.PendingCode = ""
						if c.save(&copy) == nil {
							c.state = &copy
							state = &copy
						}
					}
					c.mu.Unlock()
				}
			}
			// A claim response can be lost after publication. The preserved token
			// may already be valid even if the pairing code has since expired.
			_ = c.connect(run, state)
		}
		cancel()
		c.mu.Lock()
		if c.state == state {
			c.status = "offline"
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-time.After(backoff + time.Duration(rand.Int64N(int64(backoff/2)))):
		}
		if time.Since(started) > 10*time.Second {
			backoff = time.Second
		} else {
			backoff = min(30*time.Second, backoff*2)
		}
	}
}
func (c *connectorClient) connect(ctx context.Context, state *connectorState) error {
	u, _ := url.Parse(c.config.RelayURL)
	target := u.Host
	if u.Port() == "" {
		target = u.Host + ":443"
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(c.tls.Clone())), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(connectwire.MaxFrame+1024), grpc.MaxCallSendMsgSize(connectwire.MaxFrame+1024)))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+state.Token, "x-connector-id", state.LinkID))
	stream, err := conn.NewStream(ctx, &connectwire.Description.Streams[0], connectwire.Path)
	if err != nil {
		return err
	}
	if err := connectwire.Send(stream, connectwire.Frame{Kind: "hello", OrganizationID: state.LocalOrganizationID, AllowManage: state.AllowManage}); err != nil {
		return err
	}
	type received struct {
		f connectwire.Frame
		e error
	}
	inbox := make(chan received, 1)
	send := make(chan connectwire.Frame, 64)
	done := make(chan error, 1)
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
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-send:
				if err := connectwire.Send(stream, f); err != nil {
					done <- err
					return
				}
			}
		}
	}()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	last := time.Now()
	ready := false
	var requests sync.Map
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	slots := make(chan struct{}, connectwire.MaxRequests)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return err
		case <-tick.C:
			if time.Since(last) > 8*time.Second {
				return errors.New("relay heartbeat overdue")
			}
			select {
			case send <- connectwire.Frame{Kind: "ping"}:
			default:
				return errors.New("connector backpressure")
			}
		case item := <-inbox:
			if item.e != nil {
				return item.e
			}
			last = time.Now()
			f := item.f
			switch f.Kind {
			case "ready":
				if ready || f.OrganizationID != state.CloudOrganizationID {
					return errors.New("relay organization mismatch")
				}
				ready = true
				c.mu.Lock()
				if c.state == state {
					c.status = "connected"
				}
				c.mu.Unlock()
			case "pong":
			case "cancel":
				if stop, ok := requests.Load(f.ID); ok {
					stop.(context.CancelFunc)()
				}
			case "request":
				if !ready || !domain.ValidID(f.ID) || f.Request == nil || !acquire(slots) {
					return errors.New("invalid request or connector capacity reached")
				}
				run, stop := context.WithCancel(ctx)
				if _, loaded := requests.LoadOrStore(f.ID, context.CancelFunc(stop)); loaded {
					stop()
					<-slots
					return errors.New("duplicate connector request")
				}
				workers.Add(1)
				go func(f connectwire.Frame) {
					defer workers.Done()
					defer func() { stop(); requests.Delete(f.ID); <-slots }()
					c.execute(run, state, f, send)
				}(f)
			default:
				return errors.New("invalid relay frame")
			}
		}
	}
}
func (s *Server) connectorIdentity(r *http.Request) (BrowserIdentity, bool) {
	value, ok := r.Context().Value(connectorRequestKey{}).(connectorRequestIdentity)
	if !ok || !time.Now().Before(value.identity.ExpiresAt) {
		return BrowserIdentity{}, false
	}
	value.client.mu.Lock()
	valid := value.client.state == value.state
	value.client.mu.Unlock()
	return value.identity, valid
}
