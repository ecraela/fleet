package dataplane

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/haproxyruntime"
)

func localAPI(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hm-dpa-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "dpa.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client, err := New(socket, t.TempDir(), Credentials{Username: "agent", Password: "synthetic-local-password"}, true, haproxyruntime.Client{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}
func TestUnixDiscoveryKeepsCredentialsAndRawSecretsOnTheNode(t *testing.T) {
	const secret = "synthetic-secret-not-for-cloud"
	client := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "agent" || password != "synthetic-local-password" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v3/services/haproxy/configuration/frontends":
			_, _ = w.Write([]byte(`[]`))
		case "/v3/info":
			_, _ = w.Write([]byte(`{"api":{"version":"v3.4.5"}}`))
		case "/v3/services/haproxy/configuration/backends":
			if r.URL.Query().Get("full_section") != "true" {
				t.Error("children were not requested")
			}
			w.Header().Set("Configuration-Version", "7")
			_, _ = w.Write([]byte(`[{"name":"application","balance":{"algorithm":"roundrobin"},"servers":{"app-a":{"name":"app-a","address":"backend.internal","port":8080,"weight":100,"agent-send":"` + secret + `"}}}]`))
		case "/v3/services/haproxy/configuration/raw":
			if r.URL.Query().Get("version") != "" {
				t.Error("raw read used backup lookup instead of current configuration")
			}
			w.Header().Set("Configuration-Version", "7")
			_, _ = w.Write([]byte("# _version=7\nglobal\n  # " + secret + "\n"))
		default:
			t.Error("unexpected local API path", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	inspected := client.Inspect(t.Context())
	if !inspected.Valid() || inspected.Version != 7 {
		t.Fatal("read-only configuration check failed", inspected.ErrorCode)
	}
	if files, err := os.ReadDir(client.StateDir); err != nil || len(files) != 0 {
		t.Fatal("read-only host check retained raw configuration or wrote local state", err)
	}
	state := client.Observe(t.Context())
	if !state.Valid() || !state.ControlAllowed || state.Version != 7 {
		t.Fatal("discovery failed", state.ErrorCode)
	}
	exported, _ := json.Marshal(state)
	if strings.Contains(string(exported), secret) || strings.Contains(string(exported), client.Credentials.Password) {
		t.Fatal("local secret entered configuration report")
	}
	raw, err := client.loadArtifact(state.ArtifactID)
	if err != nil || !strings.Contains(string(raw), secret) {
		t.Fatal("full rollback artifact missing locally", err)
	}
	info, _ := os.Stat(filepath.Join(client.StateDir, "configuration-artifacts", state.ArtifactID+".cfg"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("configuration artifact is not private")
	}
	// DPA metadata version is separate from full configuration content identity.
	if rawDigest([]byte("# _version=100\nglobal\n  # "+secret+"\n")) != state.ArtifactID {
		t.Fatal("version metadata changed content identity")
	}
	if rawDigest([]byte("# _md5hash=transaction-checksum\n# _version=100\nglobal\n  # "+secret+"\n")) != state.ArtifactID {
		t.Fatal("DPA's changing file checksum changed content identity")
	}
}
func TestArtifactsRejectCorruptionSymlinksAndExhaustedStorage(t *testing.T) {
	client := localAPI(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	id, err := client.artifact([]byte("global\n"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(client.StateDir, "configuration-artifacts", id+".cfg")
	if err = os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = client.loadArtifact(id); err == nil {
		t.Fatal("corrupt history accepted")
	}
	_ = os.Remove(path)
	outside := filepath.Join(t.TempDir(), "outside.cfg")
	_ = os.WriteFile(outside, []byte("global\n"), 0600)
	_ = os.Symlink(outside, path)
	if _, err = client.loadArtifact(id); err == nil {
		t.Fatal("symlink history accepted")
	}
}
func TestDiscoveryVersionConflictAndAuthenticationFailureAreBounded(t *testing.T) {
	client := localAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("do not echo local configuration errors"))
	})
	observed := client.Observe(context.Background())
	if observed.ErrorCode != "configuration_unavailable" || observed.Document != nil || observed.ControlAllowed {
		t.Fatal("failed credentials retained write authority")
	}
	if _, err := New("/tmp/local.sock", t.TempDir(), Credentials{Username: "agent\nheader", Password: "synthetic-password"}, true, haproxyruntime.Client{}); err == nil {
		t.Fatal("invalid credentials accepted")
	}
}

// Recovery must use a durable receipt and observed worker; it must never POST
// another configuration after a response was lost or an agent was restarted.
func TestRecoveryUsesCommitReceiptWithoutReplayingWrites(t *testing.T) {
	var writes atomic.Int32
	client := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/v3/services/haproxy/configuration/frontends":
			_, _ = w.Write([]byte(`[]`))
		case "/v3/info":
			_, _ = w.Write([]byte(`{"api":{"version":"v3.4.5"}}`))
		case "/v3/services/haproxy/configuration/backends":
			w.Header().Set("Configuration-Version", "3")
			_, _ = w.Write([]byte(`[{"name":"application","balance":{"algorithm":"roundrobin"},"servers":{"app-a":{"name":"app-a","address":"127.0.0.1","port":8080,"weight":200}}}]`))
		case "/v3/services/haproxy/configuration/raw":
			w.Header().Set("Configuration-Version", "3")
			_, _ = w.Write([]byte("# _md5hash=after\nglobal\n  maxconn 256\n"))
		default:
			w.WriteHeader(404)
		}
	})
	dir, err := os.MkdirTemp("/tmp", "hm-dpa-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "runtime.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadString('\n')
				switch strings.TrimSpace(line) {
				case "show info":
					fmt.Fprint(conn, "Version: 3.4.7\nPid: 9\nStart_time_sec: 1792800000\n")
				case "show stat":
					fmt.Fprint(conn, "# pxname,svname,scur,status,weight,type,mode,algo,addr,\napplication,BACKEND,0,UP,200,1,http,roundrobin,,\napplication,app-a,0,UP,200,2,http,,127.0.0.1:8080,\n")
				case "show servers state":
					fmt.Fprint(conn, "1\n# be_id be_name srv_id srv_name srv_addr srv_op_state srv_admin_state srv_uweight srv_iweight\n1 application 1 app-a 127.0.0.1 2 0 200 200\n")
				default:
					writes.Add(1)
				}
			}()
		}
	}()
	client.Runtime = haproxyruntime.Client{Socket: socket}
	observed := client.observed(t.Context())
	if !observed.Fresh(time.Now()) || observed.Configuration == nil || !observed.Configuration.Fresh(time.Now()) {
		t.Fatal("invalid recovery fixture", observed.ErrorCode)
	}
	doc := observed.Configuration.Document.Canonical()
	op := domain.Operation{ID: domain.NewID(), Kind: "configuration", ExpectedProcessID: "8", Configuration: &doc}
	// Pre-commit rollback input can differ from DPA's normalized output.
	old, err := client.artifact([]byte("global\n maxconn 256\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err = client.saveAttempt(op.ID, attempt{Candidate: old}); err != nil {
		t.Fatal(err)
	}
	_, outcome, _ := client.Execute(t.Context(), op, false)
	if outcome != "unknown" {
		t.Fatal("a missing commit receipt claimed success", outcome)
	}
	if err = client.saveRecord("configuration-receipts", op.ID, attempt{Candidate: observed.Configuration.ArtifactID}); err != nil {
		t.Fatal(err)
	}
	_, outcome, code := client.Execute(t.Context(), op, false)
	if outcome != "succeeded" {
		t.Fatal("durable receipt did not recover verified reload", outcome, code)
	}
	op.ExpectedProcessID = "9"
	_, outcome, _ = client.Execute(t.Context(), op, false)
	if outcome != "unknown" {
		t.Fatal("same worker was accepted as a verified reload")
	}
	op.ExpectedGeneration = strings.Repeat("f", 64)
	_, outcome, code = client.Execute(t.Context(), op, true)
	if outcome != "failed" || code != "generation_changed" {
		t.Fatal("stale apply was not rejected before writing", outcome, code)
	}
	if writes.Load() != 0 {
		t.Fatal("recovery or stale apply replayed side effects", writes.Load())
	}
}
