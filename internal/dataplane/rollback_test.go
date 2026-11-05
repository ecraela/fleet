package dataplane

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/haproxyruntime"
)

type rollbackFixture struct {
	mu                        sync.Mutex
	raw                       string
	weight, version, pid      int
	frontends, savedFrontends []object
	appBackends               []object
	writes, validations       int
	beforeValidationResponse  func()
}

func newRollbackFixture(t *testing.T) (*Client, *rollbackFixture) {
	t.Helper()
	f := &rollbackFixture{weight: 100, version: 1, pid: 9, raw: "global\n  maxconn 256\nbackend application\n  server app-a 127.0.0.1:8080 weight 100 check inter 3s\n", frontends: []object{{"name": json.RawMessage(`"public"`), "mode": json.RawMessage(`"http"`)}}}
	c := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Configuration-Version", strconv.Itoa(f.version))
		switch r.URL.Path {
		case "/v3/info":
			fmt.Fprint(w, `{"api":{"version":"v3.4.5"}}`)
		case "/v3/services/haproxy/configuration/backends":
			var backends []object
			_ = json.Unmarshal([]byte(fmt.Sprintf(`[{"name":"application","balance":{"algorithm":"roundrobin"},"servers":{"app-a":{"name":"app-a","address":"127.0.0.1","port":8080,"weight":%d}}}]`, f.weight)), &backends)
			_ = json.NewEncoder(w).Encode(append(backends, f.appBackends...))
		case "/v3/services/haproxy/configuration/frontends":
			_ = json.NewEncoder(w).Encode(f.frontends)
		case "/v3/services/haproxy/configuration/raw":
			if r.Method == "GET" {
				fmt.Fprint(w, f.raw)
				return
			}
			candidate, _ := io.ReadAll(r.Body)
			if r.URL.Query().Get("only_validate") == "true" {
				f.validations++
				if f.beforeValidationResponse != nil {
					f.beforeValidationResponse()
				}
				w.WriteHeader(202)
				fmt.Fprint(w, f.raw)
				return
			}
			f.writes++
			// DPA normalizes raw input; recovery must use its committed receipt.
			f.raw = strings.ReplaceAll(string(candidate), "  ", " ")
			f.weight = 100
			f.frontends = f.savedFrontends
			f.version++
			f.pid++
			w.WriteHeader(201)
			fmt.Fprint(w, f.raw)
		default:
			t.Errorf("unexpected DPA path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	dir, err := os.MkdirTemp("/tmp", "hm-rollback-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "runtime.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadString('\n')
				f.mu.Lock()
				defer f.mu.Unlock()
				switch strings.TrimSpace(line) {
				case "show info":
					fmt.Fprintf(conn, "Version: 3.4.7\nPid: %d\nStart_time_sec: 1792800000\n", f.pid)
				case "show stat":
					fmt.Fprintf(conn, "# pxname,svname,scur,status,weight,type,mode,algo,addr,\npublic,FRONTEND,0,OPEN,0,0,http,,,\napplication,BACKEND,0,UP,%d,1,http,roundrobin,,\napplication,app-a,0,UP,%d,2,http,,127.0.0.1:8080,\n", f.weight, f.weight)
				case "show servers state":
					fmt.Fprintf(conn, "1\n# be_id be_name srv_id srv_name srv_addr srv_op_state srv_admin_state srv_uweight srv_iweight\n1 application 1 app-a 127.0.0.1 2 0 %d %d\n", f.weight, f.weight)
				default:
					t.Errorf("unexpected runtime command %q", line)
				}
			}()
		}
	}()
	c.Runtime = haproxyruntime.Client{Socket: socket}
	return c, f
}

func (f *rollbackFixture) advance() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.savedFrontends = f.frontends
	f.raw = "global\n  maxconn 512\nbackend application\n  server app-a 127.0.0.1:8080 weight 200 check inter 5s\n"
	f.frontends = []object{{"name": json.RawMessage(`"public"`), "mode": json.RawMessage(`"http"`)}}
	f.weight = 200
	f.version++
}

func rollbackOperation(t *testing.T, c *Client, target *domain.ConfigurationState) domain.Operation {
	t.Helper()
	current := c.observed(t.Context())
	if !current.Fresh(time.Now()) || current.Configuration == nil || !current.Configuration.Fresh(time.Now()) {
		t.Fatal("invalid current rollback fixture", current.ErrorCode, current.Configuration)
	}
	doc := target.Document.Canonical()
	return domain.Operation{ID: domain.NewID(), Kind: "configuration", Configuration: &doc, RollbackArtifactID: target.ArtifactID, ExpectedGeneration: current.Generation, ExpectedProcessID: current.ProcessID, BaseVersion: current.Configuration.Version, BaseArtifactID: current.Configuration.ArtifactID}
}

func TestRollbackRejectsMismatchedOrUnboundHistoricalIntent(t *testing.T) {
	for _, unbound := range []bool{false, true} {
		t.Run(strconv.FormatBool(unbound), func(t *testing.T) {
			c, f := newRollbackFixture(t)
			saved := c.Observe(t.Context())
			if !saved.Fresh(time.Now()) {
				t.Fatal("historical observation failed", saved.ErrorCode)
			}
			f.advance()
			op := rollbackOperation(t, c, saved)
			if unbound {
				// Legacy snapshots are retained, but lack independent local intent.
				_ = os.Remove(filepath.Join(c.StateDir, "configuration-bindings", saved.ArtifactID+".json"))
			} else {
				op.Configuration.Backends[0].Servers[0].Weight = 200
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, outcome, code := c.Execute(ctx, op, true)
			if outcome != "failed" || code != "configuration_history_missing" || f.writes != 0 || f.validations != 0 {
				t.Fatal("unbound raw bytes reached DPA", outcome, code, f.writes, f.validations)
			}
			if _, err := c.loadArtifact(saved.ArtifactID); err != nil {
				t.Fatal("rejection deleted historical raw bytes", err)
			}
		})
	}
}

func TestRollbackRejectsHistoricalTLSOutsideCurrentGrant(t *testing.T) {
	for _, mode := range []string{"hidden listener", "changed certificate path", "grant changes before commit", "authorized listener"} {
		t.Run(mode, func(t *testing.T) {
			c, f := newRollbackFixture(t)
			policy, l := listenerFixture(t)
			l.Name, policy.Bindings[0].Name = "public", "public"
			raw, err := os.ReadFile(policy.Certificates[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			pair, err := tls.X509KeyPair(raw, raw)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
			server.StartTLS()
			defer server.Close()
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			number, err := strconv.Atoi(port)
			if err != nil {
				t.Fatal(err)
			}
			l.Port, policy.Bindings[0].Port = uint32(number), uint32(number)
			c.TLS = policy
			bind, err := c.listenerObject(l)
			if err != nil {
				t.Fatal(err)
			}
			frontend := object{}
			set(frontend, "name", "public")
			set(frontend, "mode", "http")
			set(frontend, "description", listenerDescription("", l))
			set(frontend, "binds", map[string]object{listenerBind: bind})
			set(frontend, "http_request_rule_list", listenerRules(nil, "public"))
			f.frontends = []object{frontend}
			f.raw += "frontend public\n  bind " + l.Address + ":" + strconv.Itoa(int(l.Port)) + " ssl crt " + policy.Certificates[0].Path + "\n"
			saved := c.Observe(t.Context())
			if !saved.Fresh(time.Now()) {
				t.Fatal("historical TLS observation failed", saved.ErrorCode)
			}
			f.advance()
			op := rollbackOperation(t, c, saved)
			switch mode {
			case "hidden listener":
				op.Configuration.Listeners = nil
				c.TLS = nil
			case "changed certificate path":
				data, _ := os.ReadFile(policy.Certificates[0].Path)
				path := filepath.Join(t.TempDir(), "replacement.pem")
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				policy.Certificates[0].Path = path
			case "grant changes before commit":
				f.beforeValidationResponse = func() { _ = os.WriteFile(policy.Certificates[0].Path, []byte("unusable"), 0600) }
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, outcome, _ := c.Execute(ctx, op, true)
			if mode == "authorized listener" {
				if outcome != "succeeded" || f.writes != 1 {
					t.Fatal("authorized TLS rollback rejected", outcome, f.writes)
				}
				return
			}
			if outcome != "failed" || f.writes != 0 {
				t.Fatal("historical TLS bypassed current node grant", outcome, f.writes)
			}
		})
	}
}

func TestRollbackRejectsChangedTrafficDependencies(t *testing.T) {
	for _, change := range []string{"proxy trust", "client source", "country map", "request log permission", "log target"} {
		t.Run(change, func(t *testing.T) {
			c, f := newRollbackFixture(t)
			c.AllowRequestLogs = true
			c.Traffic = testTraffic()
			c.Traffic.Source.Mode, c.Traffic.Capabilities.ClientSource = "xff", "xff"
			c.Traffic.Source.TrustedProxies = []string{"10.0.0.0/24"}
			c.Traffic.LogSocket = "/run/haproxy/access.sock"
			a := testApplication("/shop")
			a.RequestTelemetry = true
			a.TrafficPolicy = &domain.TrafficPolicy{Version: 1, Mode: "enforce", UnknownAction: "allow", Rules: []domain.TrafficRule{{ID: domain.NewID(), Name: "Country", Action: "block", Countries: []string{"RU"}}}}
			if change == "request log permission" {
				a.TrafficPolicy = nil
			}
			backend, err := c.applicationBackend(a)
			if err != nil {
				t.Fatal(err)
			}
			f.appBackends = []object{backend}
			frontend := object{}
			set(frontend, "name", "public")
			set(frontend, "mode", "http")
			set(frontend, "log_format", domain.RequestTrafficLogFormat)
			set(frontend, "log_target_list", []any{map[string]any{"nolog": true}, map[string]any{"address": c.Traffic.LogSocket, "format": "raw", "length": 2048}})
			set(frontend, "tcp_request_rule_list", []any{
				map[string]any{"type": "connection", "action": "set-var", "var_scope": "sess", "var_name": "cv_peer", "expr": "src"},
				map[string]any{"type": "session", "action": "set-var", "var_scope": "sess", "var_name": "cv_client", "expr": "src"},
			})
			set(frontend, "http_request_rule_list", []object{trafficVariable("cv_xff_count", "req.fhdr_cnt(X-Forwarded-For)", ""), trafficVariable("cv_xff", "req.fhdr(X-Forwarded-For)", "")})
			set(frontend, "backend_switching_rule_list", applicationRules([]domain.HTTPApplication{a}, "public"))
			f.frontends = []object{frontend}
			saved := c.Observe(t.Context())
			if !saved.Fresh(time.Now()) {
				t.Fatal("historical policy was not observed", saved.ErrorCode)
			}
			f.advance()
			f.frontends = []object{frontend}
			switch change {
			case "proxy trust":
				c.Traffic.Source.TrustedProxies = []string{"192.0.2.0/24"}
			case "client source":
				c.Traffic.Source.Mode, c.Traffic.Capabilities.ClientSource = "peer", "peer"
			case "country map":
				c.Traffic.CountryMap = "/maps/replacement.map"
			case "request log permission":
				c.AllowRequestLogs = false
			case "log target":
				c.Traffic.LogSocket = "/run/haproxy/other.sock"
				set(frontend, "log_target_list", []any{map[string]any{"nolog": true}, map[string]any{"address": c.Traffic.LogSocket, "format": "raw", "length": 2048}})
			}
			backend, err = c.applicationBackend(a)
			if err != nil {
				t.Fatal(err)
			}
			f.appBackends = []object{backend}
			op := rollbackOperation(t, c, saved)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, outcome, _ := c.Execute(ctx, op, true)
			if outcome != "failed" || f.writes != 0 || f.validations != 0 {
				t.Fatal("old traffic authority reached raw DPA", change, outcome, f.writes, f.validations)
			}
		})
	}
}

func TestAuthorizedRawRollbackPreservesSettingsAndNormalizationReceipt(t *testing.T) {
	c, f := newRollbackFixture(t)
	saved := c.Observe(t.Context())
	f.advance()
	op := rollbackOperation(t, c, saved)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	found, outcome, code := c.Execute(ctx, op, true)
	if outcome != "succeeded" || code != "" || f.writes != 1 || f.validations != 1 || !strings.Contains(f.raw, "check inter 3s") || !found.Configuration.Document.Equal(*saved.Document) {
		t.Fatal("authorized full rollback failed", outcome, code, f.writes, f.validations)
	}
	if found.Configuration.ArtifactID == saved.ArtifactID {
		t.Fatal("fixture did not exercise DPA normalization")
	}
	if _, outcome, code = c.Execute(ctx, op, false); outcome != "succeeded" || code != "" || f.writes != 1 {
		t.Fatal("receipt recovery replayed or lost committed rollback", outcome, code, f.writes)
	}
}

func TestRollbackRejectsUnsafeLocalBindingRecords(t *testing.T) {
	for _, mode := range []string{"corrupt", "unknown field", "public permissions", "symlink", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			c, f := newRollbackFixture(t)
			saved := c.Observe(t.Context())
			f.advance()
			op := rollbackOperation(t, c, saved)
			path := filepath.Join(c.StateDir, "configuration-bindings", saved.ArtifactID+".json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "corrupt":
				err = os.WriteFile(path, []byte("{"), 0600)
			case "unknown field":
				var record map[string]json.RawMessage
				if err = json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				record["unexpected"] = json.RawMessage(`true`)
				raw, err = json.Marshal(record)
				if err == nil {
					err = os.WriteFile(path, raw, 0600)
				}
			case "public permissions":
				err = os.Chmod(path, 0644)
			case "symlink":
				target := filepath.Join(t.TempDir(), "binding.json")
				if err = os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(path); err == nil {
					err = os.Symlink(target, path)
				}
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat("x", 129<<10)), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, outcome, code := c.Execute(ctx, op, true)
			if outcome != "failed" || code != "configuration_history_missing" || f.writes != 0 || f.validations != 0 {
				t.Fatal("unsafe binding authorized a raw write", mode, outcome, code)
			}
		})
	}
}
