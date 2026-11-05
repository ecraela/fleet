package haproxyruntime

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type fakeRuntime struct {
	mu      sync.Mutex
	weights map[string]int
	states  map[string]int
	active  int
	writes  int
}

func fakeSocket(t *testing.T) (Client, *fakeRuntime) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hm-runtime-")
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
	fake := &fakeRuntime{weights: map[string]int{"app-a": 100, "app-b": 100}, states: map[string]int{"app-a": 0, "app-b": 0}}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadString('\n')
				fake.mu.Lock()
				defer fake.mu.Unlock()
				switch strings.TrimSpace(line) {
				case "show cli level":
					fmt.Fprintln(conn, "admin")
				case "show info":
					fmt.Fprintf(conn, "Version: 3.4.7\nPid: 8\nStart_time_sec: 1792800000\n")
				case "show stat":
					fmt.Fprintln(conn, "# pxname,svname,scur,status,weight,type,mode,algo,addr,")
					fmt.Fprintln(conn, "public,FRONTEND,0,OPEN,0,0,http,,,")
					fmt.Fprintln(conn, "application,BACKEND,0,UP,200,1,http,roundrobin,,")
					for _, name := range []string{"app-a", "app-b"} {
						active := 0
						if name == "app-a" {
							active = fake.active
						}
						fmt.Fprintf(conn, "application,%s,%d,UP,%d,2,http,,127.0.0.1:8080,\n", name, active, fake.weights[name])
					}
				case "show servers state":
					fmt.Fprintln(conn, "1\n# be_id be_name srv_id srv_name srv_addr srv_op_state srv_admin_state srv_uweight srv_iweight")
					for i, name := range []string{"app-a", "app-b"} {
						fmt.Fprintf(conn, "1 application %d %s 127.0.0.1 2 %d %d 100\n", i+1, name, fake.states[name], fake.weights[name])
					}
				default:
					var name, kind, value string
					fields := strings.Fields(line)
					if len(fields) == 5 && fields[0] == "set" && fields[1] == "server" {
						name = strings.TrimPrefix(fields[2], "application/")
						kind = fields[3]
						value = fields[4]
						if _, ok := fake.states[name]; ok {
							fake.writes++
							if kind == "state" {
								if value == "drain" {
									fake.states[name] = 8
								} else if value == "maint" {
									fake.states[name] = 1
								} else {
									fake.states[name] = 0
								}
							} else if kind == "weight" {
								var weight int
								fmt.Sscanf(value, "%d", &weight)
								fake.weights[name] = weight
							}
							return
						}
					}
					fmt.Fprintln(conn, "Unknown command")
				}
			}()
		}
	}()
	return Client{Socket: socket, AllowControl: true}, fake
}
func TestAdminAccessIsReadOnly(t *testing.T) {
	client, fake := fakeSocket(t)
	if !client.AdminAccess(t.Context()) {
		t.Fatal("admin socket not recognized")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.writes != 0 {
		t.Fatal("access check changed server state")
	}
}
func TestDiscoveryFingerprintsFilesAndKeepsSecretsLocal(t *testing.T) {
	client, _ := fakeSocket(t)
	path := filepath.Join(t.TempDir(), "haproxy.cfg")
	if err := os.WriteFile(path, []byte("backend application\n  # synthetic-secret-not-for-reports\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client.ConfigFiles = []string{path}
	d := client.Discover(t.Context())
	if !d.Valid() || !d.ControlAllowed || len(d.Servers) != 2 || len(d.Proxies) != 2 || d.ConfigFingerprint == "" {
		t.Fatal("discovery failed", d)
	}
	before := d.Generation
	_ = os.WriteFile(path, []byte("changed configuration"), 0600)
	if after := client.Discover(t.Context()); after.Generation == before {
		t.Fatal("configuration change did not change generation")
	}
}
func TestDrainVerifiesConnectionsAndRejectsStaleCommands(t *testing.T) {
	client, fake := fakeSocket(t)
	d := client.Discover(t.Context())
	op := domain.Operation{ID: domain.NewID(), Kind: "drain", Backend: "application", Server: "app-a", ExpectedGeneration: d.Generation, ExpectedState: "ready", ExpectedWeight: 100}
	fake.mu.Lock()
	fake.active = 1
	fake.mu.Unlock()
	done := make(chan string, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() { _, outcome, _ := client.Execute(ctx, op, true); done <- outcome }()
	time.Sleep(50 * time.Millisecond)
	select {
	case outcome := <-done:
		t.Fatal("drain completed with a live connection", outcome)
	default:
	}
	fake.mu.Lock()
	fake.active = 0
	fake.mu.Unlock()
	if outcome := <-done; outcome != "succeeded" {
		t.Fatal(outcome)
	}
	_, outcome, code := client.Execute(ctx, op, true)
	if outcome != "failed" || code != "state_changed" {
		t.Fatal("stale command accepted", outcome, code)
	}
	fake.mu.Lock()
	writes := fake.writes
	fake.mu.Unlock()
	if writes != 1 {
		t.Fatal("duplicate mutation performed", writes)
	}
	op.Server = "app-b"
	if _, _, code = client.Execute(ctx, op, true); code != "last_available_server" {
		t.Fatal("last server guard failed", code)
	}
}
func TestRecoveryNeverReplaysAnUnverifiedMutation(t *testing.T) {
	client, fake := fakeSocket(t)
	d := client.Discover(t.Context())
	op := domain.Operation{ID: domain.NewID(), Kind: "weight", Backend: "application", Server: "app-a", Weight: 200, ExpectedGeneration: d.Generation, ExpectedState: "ready", ExpectedWeight: 100}
	_, outcome, _ := client.Execute(t.Context(), op, false)
	if outcome != "unknown" {
		t.Fatal(outcome)
	}
	fake.mu.Lock()
	writes := fake.writes
	fake.weights["app-a"] = 200
	fake.mu.Unlock()
	if writes != 0 {
		t.Fatal("recovery replayed side effects")
	}
	_, outcome, _ = client.Execute(t.Context(), op, false)
	if outcome != "succeeded" {
		t.Fatal("recovery did not verify the applied weight", outcome)
	}
}

func TestMaintenanceWithdrawsWithoutWaitingForOrClosingLiveConnections(t *testing.T) {
	client, fake := fakeSocket(t)
	d := client.Discover(t.Context())
	if d.Proxies[0].ActiveConnections == nil || *d.Proxies[0].ActiveConnections != 0 {
		t.Fatal("frontend connection count missing")
	}
	fake.mu.Lock()
	fake.active = 1
	fake.mu.Unlock()
	op := domain.Operation{ID: domain.NewID(), Kind: "maintenance", Backend: "application", Server: "app-a", ExpectedGeneration: d.Generation, ExpectedState: "ready", ExpectedWeight: 100}
	observed, outcome, code := client.Execute(t.Context(), op, true)
	if outcome != "succeeded" || code != "" {
		t.Fatal(outcome, code)
	}
	target, _ := observed.Server("application", "app-a")
	if target.State != "maint" || target.ActiveConnections != 1 {
		t.Fatal("maintenance killed or waited for connection", target)
	}
	op.Server = "app-b"
	if _, _, code = client.Execute(t.Context(), op, true); code != "last_available_server" {
		t.Fatal("last node guard absent", code)
	}
}
