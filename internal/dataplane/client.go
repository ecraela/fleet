// Package dataplane owns a bounded, local-only adapter to Data Plane API 3.4.
// Raw configuration and authentication material never enter the control protocol.
package dataplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/haproxyruntime"
)

const maxRaw = 1 << 20

var reloadID = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{1,20}$`)

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type Client struct {
	Socket              string
	StateDir            string
	Credentials         Credentials
	AllowControl        bool
	AllowRequestLogs    bool
	Traffic             *TrafficPolicy
	Runtime             haproxyruntime.Client
	TLS                 *TLSPolicy
	NodeID              string
	DynamicCertificates func() []LocalCertificate
	ACMEGrants          func() []domain.ACMEGrant
	http                *http.Client
}
type apiError struct{ code string }

func (e apiError) Error() string { return e.code }
func failure(code string) error  { return apiError{code} }
func New(socket, state string, credentials Credentials, allow bool, runtime haproxyruntime.Client) (*Client, error) {
	if !filepath.IsAbs(socket) || len(credentials.Username) == 0 || len(credentials.Username) > 128 || len(credentials.Password) < 16 || len(credentials.Password) > 1024 || strings.ContainsAny(credentials.Username, ":\r\n") || strings.ContainsAny(credentials.Password, "\r\n") {
		return nil, failure("configuration_not_configured")
	}
	transport := &http.Transport{Proxy: nil, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, IdleConnTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
		}}
	return &Client{Socket: socket, StateDir: state, Credentials: credentials, AllowControl: allow, Runtime: runtime, http: &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) call(ctx context.Context, method, path, contentType string, body []byte) ([]byte, http.Header, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost/v3"+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, 0, failure("configuration_unavailable")
	}
	req.SetBasicAuth(c.Credentials.Username, c.Credentials.Password)
	req.Header.Set("Accept", "application/json")
	if strings.Contains(path, "/configuration/raw") {
		req.Header.Set("Accept", "text/plain")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, nil, 0, failure("configuration_unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2*maxRaw+1))
	if err != nil || len(raw) > 2*maxRaw {
		return nil, nil, 0, failure("configuration_unavailable")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		code := "configuration_rejected"
		if response.StatusCode == 409 || response.StatusCode == 406 {
			code = "configuration_changed"
		}
		if response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 429 || response.StatusCode >= 500 {
			code = "configuration_unavailable"
		}
		return nil, response.Header, response.StatusCode, failure(code)
	}
	return raw, response.Header, response.StatusCode, nil
}

type object = map[string]json.RawMessage

func decode[T any](raw json.RawMessage) T { var value T; _ = json.Unmarshal(raw, &value); return value }
func set(m object, k string, v any)       { m[k], _ = json.Marshal(v) }
func canonicalRaw(raw []byte) []byte {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "# _version=") || strings.HasPrefix(strings.TrimSpace(line), "# _md5hash=") {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.TrimSpace(strings.Join(out, "\n")) + "\n")
}
func rawDigest(raw []byte) string {
	sum := sha256.Sum256(canonicalRaw(raw))
	return hex.EncodeToString(sum[:])
}
func (c *Client) directory(name string) (string, error) {
	dir := filepath.Join(c.StateDir, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", failure("configuration_storage_full")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", failure("configuration_storage_full")
	}
	return dir, nil
}
func (c *Client) artifact(raw []byte) (string, error) {
	raw = canonicalRaw(raw)
	if len(raw) == 0 || len(raw) > maxRaw {
		return "", failure("configuration_unsupported")
	}
	dir, err := c.directory("configuration-artifacts")
	if err != nil {
		return "", err
	}
	id := rawDigest(raw)
	path := filepath.Join(dir, id+".cfg")
	if stored, err := c.loadArtifact(id); err == nil {
		if !bytes.Equal(stored, raw) {
			return "", failure("configuration_unavailable")
		}
		return id, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) >= 256 {
		return "", failure("configuration_storage_full")
	}
	total := int64(0)
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	if total+int64(len(raw)) > 128<<20 {
		return "", failure("configuration_storage_full")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", failure("configuration_storage_full")
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", failure("configuration_storage_full")
	}
	if err = syncDir(dir); err != nil {
		return "", failure("configuration_storage_full")
	}
	return id, nil
}
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (c *Client) loadArtifact(id string) ([]byte, error) {
	if !domain.ValidDigest(id) {
		return nil, failure("configuration_history_missing")
	}
	f, err := os.OpenFile(filepath.Join(c.StateDir, "configuration-artifacts", id+".cfg"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, failure("configuration_history_missing")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxRaw {
		return nil, failure("configuration_history_missing")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxRaw+1))
	if err != nil || len(raw) > maxRaw || rawDigest(raw) != id {
		return nil, failure("configuration_history_missing")
	}
	return raw, nil
}
func project(backends []object) (domain.ConfigurationDocument, error) {
	doc := domain.ConfigurationDocument{Backends: []domain.ConfigurationBackend{}}
	for _, raw := range backends {
		if len(decode[map[string]any](raw["server_templates"])) > 0 {
			return doc, failure("configuration_unsupported")
		}
		balance := decode[object](raw["balance"])
		algorithm := decode[string](balance["algorithm"])
		if algorithm == "" {
			algorithm = "roundrobin"
		}
		if len(balance) > 1 {
			return doc, failure("configuration_unsupported")
		}
		b := domain.ConfigurationBackend{Name: decode[string](raw["name"]), Algorithm: algorithm, Servers: []domain.ConfigurationServer{}}
		servers := decode[map[string]object](raw["servers"])
		for name, s := range servers {
			weight := uint32(1)
			if v, ok := s["weight"]; ok && string(v) != "null" {
				if err := json.Unmarshal(v, &weight); err != nil {
					return doc, failure("configuration_unsupported")
				}
			}
			if decode[string](s["name"]) != name {
				return doc, failure("configuration_unsupported")
			}
			port := decode[uint32](s["port"])
			forward := port == 0 && decode[string](raw["mode"]) == "tcp" && decode[uint32](s["health_check_port"]) > 0
			b.Servers = append(b.Servers, domain.ConfigurationServer{Name: name, Address: decode[string](s["address"]), Port: port, ForwardIncomingPort: forward, Weight: weight, Maintenance: decode[string](s["maintenance"]) == "enabled"})
		}
		doc.Backends = append(doc.Backends, b)
	}
	doc = doc.Canonical()
	if !doc.Valid() {
		return doc, failure("configuration_unsupported")
	}
	return doc, nil
}
func (c *Client) read(ctx context.Context) (*domain.ConfigurationState, []object, error) {
	return c.readConfiguration(ctx, true)
}

func (c *Client) readConfiguration(ctx context.Context, retain bool) (*domain.ConfigurationState, []object, error) {
	info, _, _, err := c.call(ctx, "GET", "/info", "", nil)
	if err != nil {
		return nil, nil, err
	}
	version := decode[struct {
		API struct {
			Version string `json:"version"`
		} `json:"api"`
	}](info).API.Version
	if !strings.HasPrefix(strings.TrimPrefix(version, "v"), "3.4.") {
		return nil, nil, failure("configuration_unsupported")
	}
	raw, headers, _, err := c.call(ctx, "GET", "/services/haproxy/configuration/backends?full_section=true", "", nil)
	if err != nil {
		return nil, nil, err
	}
	v, err := strconv.ParseInt(headers.Get("Configuration-Version"), 10, 64)
	if err != nil || v < 1 {
		return nil, nil, failure("configuration_unavailable")
	}
	var backends []object
	if json.Unmarshal(raw, &backends) != nil || len(backends) > 32 {
		return nil, nil, failure("configuration_unsupported")
	}
	unmanaged, apps, err := c.projectApplications(backends)
	if err != nil {
		return nil, nil, err
	}
	doc, err := project(unmanaged)
	if err != nil {
		return nil, nil, err
	}
	doc.Applications = apps
	frontends, err := c.frontends(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	doc.Listeners, err = c.projectListeners(frontends, apps)
	if err != nil {
		return nil, nil, err
	}
	if !doc.Valid() {
		return nil, nil, failure("configuration_unsupported")
	}
	if len(apps) > 0 {
		if err = c.verifyApplicationRoutes(ctx, apps); err != nil {
			return nil, nil, err
		}
	}
	raw, rawHeaders, _, err := c.call(ctx, "GET", "/services/haproxy/configuration/raw", "", nil)
	if err != nil {
		return nil, nil, err
	}
	if rawHeaders.Get("Configuration-Version") != strconv.FormatInt(v, 10) {
		return nil, nil, failure("configuration_changed")
	}
	id := rawDigest(raw)
	if retain {
		id, err = c.artifact(raw)
		if err != nil {
			return nil, nil, err
		}
		if err = c.retainBinding(id, doc, frontends); err != nil {
			return nil, nil, err
		}
	}
	state := c.tlsInventory(doc)
	state.ApplicationControlVersion = 6
	if c.Traffic != nil && slices.ContainsFunc(frontends, c.trafficProfile) {
		caps := c.Traffic.Capabilities
		state.TrafficRules = &caps
	}
	state.RequestLogsAllowed = c.AllowRequestLogs && slices.ContainsFunc(frontends, func(f object) bool { return domain.ValidRequestLogFormat(decode[string](f["log_format"])) })
	state.Version = v
	state.ArtifactID = id
	state.Document = &doc
	state.ObservedAtMS = time.Now().UnixMilli()
	state.ControlAllowed = c.AllowControl
	state.TLSListeners = c.observeTLS(ctx, doc)
	return &state, unmanaged, nil
}
func (c *Client) Observe(ctx context.Context) *domain.ConfigurationState {
	return c.observe(ctx, true)
}

// Inspect checks configuration compatibility without creating local artifacts.
func (c *Client) Inspect(ctx context.Context) *domain.ConfigurationState {
	return c.observe(ctx, false)
}

func (c *Client) observe(ctx context.Context, retain bool) *domain.ConfigurationState {
	if c == nil {
		return &domain.ConfigurationState{ErrorCode: "configuration_not_configured", ObservedAtMS: time.Now().UnixMilli()}
	}
	state, _, err := c.readConfiguration(ctx, retain)
	if err == nil {
		return state
	}
	code := "configuration_unavailable"
	var failure apiError
	if errors.As(err, &failure) && (failure.code == "configuration_unsupported" || failure.code == "configuration_storage_full") {
		code = failure.code
	}
	return &domain.ConfigurationState{ErrorCode: code, ObservedAtMS: time.Now().UnixMilli()}
}

type attempt struct {
	Candidate     string `json:"candidate"`
	Previous      string `json:"previous"`
	TransactionID string `json:"transaction_id,omitempty"`
}

func (c *Client) saveAttempt(id string, a attempt) error {
	return c.saveRecord("configuration-operations", id, a)
}
func (c *Client) saveRecord(directory, id string, a attempt) error {
	dir, err := c.directory(directory)
	if err != nil {
		return err
	}
	// One file per command, bounded by the same node's operation journal budget.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) >= 4096 {
		return failure("configuration_storage_full")
	}
	raw, _ := json.Marshal(a)
	path := filepath.Join(dir, id+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return failure("configuration_storage_full")
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	_ = f.Close()
	if err != nil {
		return failure("configuration_storage_full")
	}
	if err = syncDir(dir); err != nil {
		return failure("configuration_storage_full")
	}
	return nil
}
func (c *Client) loadRecord(directory, id string) (attempt, error) {
	var a attempt
	if !domain.ValidID(id) {
		return a, failure("configuration_history_missing")
	}
	path := filepath.Join(c.StateDir, directory, id+".json")
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return a, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &a) != nil || !domain.ValidDigest(a.Candidate) {
		return a, failure("configuration_history_missing")
	}
	return a, nil
}
func (c *Client) observed(ctx context.Context) domain.Discovery {
	d := c.Runtime.Discover(ctx)
	d.Configuration = c.Observe(ctx)
	return d
}
func (c *Client) Execute(ctx context.Context, o domain.Operation, apply bool) (domain.Discovery, string, string) {
	current := c.observed(ctx)
	if !apply {
		a, err := c.loadRecord("configuration-operations", o.ID)
		if receipt, receiptErr := c.loadRecord("configuration-receipts", o.ID); receiptErr == nil {
			a, err = receipt, nil
		}
		if err == nil && current.Configuration != nil && current.Configuration.ArtifactID == a.Candidate && o.Verified(current) {
			return current, "succeeded", ""
		}
		return current, "unknown", "outcome_unverified"
	}
	if !c.AllowControl || o.Configuration == nil || !o.Configuration.Valid() {
		return current, "failed", "control_disabled"
	}
	if !current.Fresh(time.Now()) || current.Generation != o.ExpectedGeneration || current.ProcessID != o.ExpectedProcessID {
		return current, "failed", "generation_changed"
	}
	state, backends, err := c.read(ctx)
	if err != nil || !state.Fresh(time.Now()) {
		return current, "failed", "configuration_unavailable"
	}
	if state.Version != o.BaseVersion || state.ArtifactID != o.BaseArtifactID {
		return current, "failed", "configuration_changed"
	}
	if o.RollbackArtifactID == "" && !o.Configuration.KnownIncomingPorts(*state.Document) {
		return current, "failed", "configuration_unsupported"
	}
	if err = c.validateListeners(*o.Configuration); err != nil {
		return current, "failed", errorCode(err)
	}
	for _, l := range o.Configuration.Listeners {
		httpFrontend := false
		for _, p := range current.Proxies {
			httpFrontend = httpFrontend || (p.Kind == "frontend" && p.Name == l.Name && p.Mode == "http" && p.Status == "OPEN")
		}
		if !httpFrontend {
			return current, "failed", "configuration_unsupported"
		}
	}
	var candidate []byte
	transaction := ""
	if o.RollbackArtifactID != "" {
		candidate, err = c.loadArtifact(o.RollbackArtifactID)
		if err != nil {
			return current, "failed", "configuration_history_missing"
		}
		frontends, readErr := c.frontends(ctx, "")
		if readErr != nil {
			return current, "failed", errorCode(readErr)
		}
		if err = c.validateBinding(o.RollbackArtifactID, *o.Configuration, frontends); err != nil {
			return current, "failed", errorCode(err)
		}
		_, _, _, err = c.call(ctx, "POST", "/services/haproxy/configuration/raw?only_validate=true&version="+strconv.FormatInt(state.Version, 10), "text/plain", candidate)
	} else {
		var raw []byte
		raw, _, _, err = c.call(ctx, "POST", "/services/haproxy/transactions?version="+strconv.FormatInt(state.Version, 10), "", nil)
		if err == nil {
			transaction = decode[struct {
				ID string `json:"id"`
			}](raw).ID
			if !domain.ValidID(transaction) {
				err = failure("configuration_unavailable")
			}
		}
		if err == nil {
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
				defer stop()
				_, _, _, _ = c.call(cleanup, "DELETE", "/services/haproxy/transactions/"+url.PathEscape(transaction), "", nil)
			}()
			if len(o.Configuration.Backends) != len(backends) {
				err = failure("configuration_unsupported")
			}
			for _, desired := range o.Configuration.Backends {
				if err != nil {
					break
				}
				var base object
				for _, b := range backends {
					if decode[string](b["name"]) == desired.Name {
						base = b
						break
					}
				}
				if base == nil {
					err = failure("configuration_unsupported")
					break
				}
				previous := decode[map[string]object](base["servers"])
				servers := map[string]object{}
				for _, s := range desired.Servers {
					value := previous[s.Name]
					if value == nil {
						value = object{}
						set(value, "check", "enabled")
						set(value, "init-state", "fully-down")
					}
					set(value, "name", s.Name)
					set(value, "address", s.Address)
					set(value, "port", s.Port)
					if s.ForwardIncomingPort {
						delete(value, "port")
					}
					set(value, "weight", s.Weight)
					if s.Maintenance {
						set(value, "maintenance", "enabled")
					} else {
						set(value, "maintenance", "disabled")
					}
					servers[s.Name] = value
				}
				set(base, "balance", map[string]string{"algorithm": desired.Algorithm})
				set(base, "servers", servers)
				payload, _ := json.Marshal(base)
				_, _, _, err = c.call(ctx, "PUT", "/services/haproxy/configuration/backends/"+url.PathEscape(desired.Name)+"?transaction_id="+url.QueryEscape(transaction)+"&full_section=true", "application/json", payload)
			}
			if err == nil {
				err = c.applyApplications(ctx, transaction, state.Document.Applications, o.Configuration.Applications)
			}
			if err == nil {
				err = c.applyListeners(ctx, transaction, *state.Document, *o.Configuration)
			}
			if err == nil {
				candidate, _, _, err = c.call(ctx, "GET", "/services/haproxy/configuration/raw?transaction_id="+url.QueryEscape(transaction), "", nil)
			}
		}
	}
	if err != nil {
		return current, "failed", errorCode(err)
	}
	id, err := c.artifact(candidate)
	if err != nil {
		return current, "failed", errorCode(err)
	}
	if err = c.saveAttempt(o.ID, attempt{Candidate: id, Previous: state.ArtifactID, TransactionID: transaction}); err != nil {
		return current, "failed", errorCode(err)
	}
	var headers http.Header
	var committed []byte
	if err = c.validateListeners(*o.Configuration); err != nil {
		return current, "failed", errorCode(err)
	}
	if o.RollbackArtifactID != "" {
		frontends, readErr := c.frontends(ctx, "")
		if readErr != nil {
			return current, "failed", errorCode(readErr)
		}
		if err = c.validateBinding(o.RollbackArtifactID, *o.Configuration, frontends); err != nil {
			return current, "failed", errorCode(err)
		}
	}
	if transaction != "" {
		_, headers, _, err = c.call(ctx, "PUT", "/services/haproxy/transactions/"+url.PathEscape(transaction)+"?force_reload=true", "", nil)
	} else {
		committed, headers, _, err = c.call(ctx, "POST", "/services/haproxy/configuration/raw?version="+strconv.FormatInt(state.Version, 10)+"&force_reload=true", "text/plain", candidate)
	}
	if err != nil {
		// The response may have been lost after commit. Reconciliation never repeats it.
		return c.observed(ctx), "unknown", errorCode(err)
	}
	if transaction == "" {
		// Raw restores are parsed and formatted by DPA. Persist its returned full
		// configuration as the commit receipt before claiming a verified outcome.
		// A lost response remains unknown; recovery never repeats the write.
		id, err = c.artifact(committed)
		if err == nil {
			err = c.saveRecord("configuration-receipts", o.ID, attempt{Candidate: id, Previous: state.ArtifactID})
		}
		if err != nil {
			return c.observed(ctx), "unknown", errorCode(err)
		}
	}
	reload := headers.Get("Reload-ID")
	if reload != "" && !reloadID.MatchString(reload) {
		return current, "unknown", "configuration_unavailable"
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		loaded := reload == ""
		if reload != "" {
			raw, _, _, readErr := c.call(ctx, "GET", "/services/haproxy/reloads/"+reload, "", nil)
			if readErr == nil {
				status := decode[struct {
					Status string `json:"status"`
				}](raw).Status
				if status == "failed" {
					return c.observed(ctx), "failed", "configuration_reload_failed"
				}
				loaded = status == "succeeded"
			}
		}
		current = c.observed(ctx)
		if loaded && current.Configuration != nil && current.Configuration.ArtifactID == id && o.Verified(current) {
			return current, "succeeded", ""
		}
		select {
		case <-ctx.Done():
			return current, "unknown", "verification_deadline"
		case <-ticker.C:
		}
	}
}
func errorCode(err error) string {
	var e apiError
	if errors.As(err, &e) {
		return e.code
	}
	return "configuration_unavailable"
}
