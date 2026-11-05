package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/connectwire"
	"github.com/ecraela/fleet/internal/domain"
)

func (c *connectorClient) execute(ctx context.Context, state *connectorState, frame connectwire.Frame, send chan<- connectwire.Frame) {
	in := frame.Request
	deadline := time.Now().Add(65 * time.Second)
	if in != nil && in.Identity.ExpiresAt.After(time.Now()) {
		deadline = minTime(deadline, in.Identity.ExpiresAt)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	w := &connectorWriter{ctx: ctx, id: frame.ID, send: send, header: make(http.Header), localOrg: state.LocalOrganizationID, cloudOrg: state.CloudOrganizationID}
	defer w.close()
	if in == nil {
		fail(w, 400, "management request required")
		return
	}
	if !connectwire.Allowed(in.Method, in.Path) || len(in.Body) > connectwire.MaxBody || len(in.Query) > 4096 || !domain.ValidID(in.Identity.UserID) || in.Identity.OrganizationID != state.CloudOrganizationID || !in.Identity.ExpiresAt.After(time.Now()) || in.Identity.ExpiresAt.After(time.Now().Add(65*time.Second)) {
		fail(w, 403, "invalid linked management request")
		return
	}
	read, manage := false, false
	for _, cap := range in.Identity.Capabilities {
		switch cap {
		case "haproxy.read":
			if read {
				fail(w, 403, "invalid capabilities")
				return
			}
			read = true
		case "haproxy.manage":
			if manage {
				fail(w, 403, "invalid capabilities")
				return
			}
			manage = true
		default:
			fail(w, 403, "invalid capabilities")
			return
		}
	}
	if !read || len(in.Identity.Capabilities) > 2 {
		fail(w, 403, "read permission required")
		return
	}
	caps := []string{"haproxy.read"}
	if manage && state.AllowManage {
		caps = append(caps, "haproxy.manage")
	}
	identity := BrowserIdentity{UserID: in.Identity.UserID, OrganizationID: state.LocalOrganizationID, Capabilities: caps, ExpiresAt: in.Identity.ExpiresAt}
	ctx = context.WithValue(ctx, connectorRequestKey{}, connectorRequestIdentity{identity: identity, client: c, state: state})
	r, err := http.NewRequestWithContext(ctx, in.Method, "https://linked-manager.invalid"+in.Path, bytes.NewReader(in.Body))
	if err != nil {
		fail(w, 400, "invalid request")
		return
	}
	r.URL.RawQuery = in.Query
	if _, err := url.ParseQuery(in.Query); err != nil {
		fail(w, 400, "invalid query")
		return
	}
	for _, name := range []string{"Accept", "Content-Type", "Last-Event-ID"} {
		if v := in.Headers[name]; v != "" && len(v) <= 1024 && !strings.ContainsAny(v, "\r\n") {
			r.Header.Set(name, v)
		}
	}
	r.Header.Set("X-Organization-ID", state.LocalOrganizationID)
	c.server.ServeHTTP(w, r)
}

// Responses are bounded and translated structurally. Organization identifiers
// in arbitrary application text are not rewritten. SSE flushes retain live
// updates; ordinary JSON is buffered before publishing its headers/body.
type connectorWriter struct {
	ctx                context.Context
	id                 string
	send               chan<- connectwire.Frame
	header             http.Header
	status             int
	sent               bool
	failed             bool
	buffer             bytes.Buffer
	total              int
	localOrg, cloudOrg string
}

func (w *connectorWriter) Header() http.Header { return w.header }
func (w *connectorWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *connectorWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.failed || w.ctx.Err() != nil {
		return 0, errors.New("connector response canceled")
	}
	if w.total+len(b) > connectwire.MaxResponse {
		w.failed = true
		return 0, errors.New("connector response limit")
	}
	w.total += len(b)
	return w.buffer.Write(b)
}
func (w *connectorWriter) emit(f connectwire.Frame) bool {
	if w.failed {
		return false
	}
	f.ID = w.id
	select {
	case w.send <- f:
		return true
	case <-w.ctx.Done():
		w.failed = true
		return false
	case <-time.After(2 * time.Second):
		w.failed = true
		return false
	}
}
func (w *connectorWriter) Flush() {
	if !w.sent {
		if w.status == 0 {
			w.status = 200
		}
		headers := map[string]string{}
		for _, name := range []string{"Content-Type", "Retry-After", "X-Accel-Buffering"} {
			if v := w.header.Get(name); v != "" {
				headers[name] = v
			}
		}
		if !w.emit(connectwire.Frame{Kind: "headers", Status: w.status, Headers: headers}) {
			return
		}
		w.sent = true
	}
	b := append([]byte(nil), w.buffer.Bytes()...)
	w.buffer.Reset()
	if strings.HasPrefix(w.header.Get("Content-Type"), "text/event-stream") {
		lines := bytes.Split(b, []byte("\n"))
		for i, line := range lines {
			if value, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
				lines[i] = append([]byte("data: "), mapConnectorJSON(value, w.localOrg, w.cloudOrg)...)
			}
		}
		b = bytes.Join(lines, []byte("\n"))
	} else if strings.HasPrefix(w.header.Get("Content-Type"), "application/json") {
		b = mapConnectorJSON(b, w.localOrg, w.cloudOrg)
	}
	for len(b) > 0 {
		n := min(len(b), 16<<10)
		if !w.emit(connectwire.Frame{Kind: "data", Data: append([]byte(nil), b[:n]...)}) {
			return
		}
		b = b[n:]
	}
}
func (w *connectorWriter) close()                             { w.Flush(); w.emit(connectwire.Frame{Kind: "end"}) }
func (w *connectorWriter) SetWriteDeadline(t time.Time) error { return nil }
func mapConnectorJSON(b []byte, local, cloud string) []byte {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return b
	}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				if key == "organization_id" && value == local {
					x[key] = cloud
				} else {
					visit(value)
				}
			}
		case []any:
			for _, item := range x {
				visit(item)
			}
		}
	}
	visit(value)
	mapped, err := json.Marshal(value)
	if err != nil {
		return b
	}
	return mapped
}
func (s *Server) relayDNSIntent(w http.ResponseWriter, r *http.Request, identity BrowserIdentity, localOrg string, b []byte) {
	var response struct {
		Claims map[string]any `json:"connector_dns_claims"`
	}
	if json.Unmarshal(b, &response) != nil || response.Claims == nil {
		fail(w, 502, "linked manager returned invalid DNS evidence")
		return
	}
	claims := response.Claims
	// The manager provides verified application facts, never signing authority.
	// Scope, actor, audience and lifetime are fixed by this authenticated relay.
	if claims["org"] != localOrg || claims["sub"] != identity.UserID || claims["aud"] != "converge-dns" || !identity.Has("haproxy.manage") {
		fail(w, 502, "linked DNS evidence scope mismatch")
		return
	}
	claims["org"] = identity.OrganizationID
	claims["iss"] = s.config.Browser.Audience
	claims["iat"] = time.Now().Unix()
	claims["exp"] = time.Now().Add(time.Minute).Unix()
	s.writeDNSIntent(w, r, claims)
}
