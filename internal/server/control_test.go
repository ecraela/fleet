package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/ecraela/fleet/internal/controlv1"
	"github.com/ecraela/fleet/internal/controlwire"
	"github.com/ecraela/fleet/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func discoveryForControl() domain.Discovery {
	d := domain.Discovery{Version: "3.4.7", ProcessID: "8", StartedAt: 1792800000, ObservedAtMS: time.Now().UnixMilli(), ControlAllowed: true, Proxies: []domain.Proxy{}, Servers: []domain.BackendServer{{Backend: "application", Name: "app-a", State: "ready", Weight: 100, InitialWeight: 100, Health: "UP"}, {Backend: "application", Name: "app-b", State: "ready", Weight: 100, InitialWeight: 100, Health: "UP"}}}
	d.SetGeneration()
	return d
}
func TestSetupDiagnosticsAreTenantBoundAndCannotGrantControl(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	node, _, _, _ := f.enrollNode(t)
	other := domain.NewID()
	cluster, err := f.store.CreateCluster(t.Context(), other, "Other")
	if err != nil {
		t.Fatal(err)
	}
	foreign := enrollForFleet(t, f, other, cluster.ID)
	for _, item := range []domain.Node{node, foreign} {
		session := domain.NewID()
		if _, err = f.store.OpenControl(t.Context(), item.OrganizationID, item.ID, session); err != nil {
			t.Fatal(err)
		}
		d := discoveryForControl()
		d.ControlAllowed = false
		d.Setup = &domain.NodeSetup{ObservedAtMS: time.Now().UnixMilli(), Owner: "external", Checks: []domain.SetupCheck{
			domain.SetupResult("metrics", "metrics_ready"), domain.SetupResult("discovery", "runtime_ready"),
			domain.SetupResult("files", "files_ready"), domain.SetupResult("ownership", "owner_external"),
			domain.SetupResult("runtime_control", "runtime_control_ready"), domain.SetupResult("configuration_control", "configuration_control_disabled"),
		}}
		if _, _, err = f.store.ReportControl(t.Context(), item.OrganizationID, item.ID, session, &d, nil); err != nil {
			t.Fatal(err)
		}
	}
	result := f.request("GET", "/v1/nodes/"+node.ID+"/discovery", nil, true)
	if result.Code != 200 || !strings.Contains(result.Body.String(), "owner_external") || !strings.Contains(result.Body.String(), "\"management_enabled\":false") {
		t.Fatal("diagnostics lost or granted runtime authority", result.Code)
	}
	if result := f.request("GET", "/v1/nodes/"+foreign.ID+"/discovery", nil, true); result.Code != 404 {
		t.Fatal("another tenant's setup exposed", result.Code)
	}
}
func TestControlEndpointsTenantBoundaryAndInput(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	node, _, _, _ := f.enrollNode(t)
	other := domain.NewID()
	cluster, _ := f.store.CreateCluster(t.Context(), other, "Other")
	foreign := enrollForFleet(t, f, other, cluster.ID)
	for _, id := range []string{node.ID, foreign.ID} {
		prefix := "/v1/nodes/" + id
		operations := map[string]any{"request_id": domain.NewID(), "kind": "weight", "backend": "application", "server": "app-a", "weight": 200, "expected_generation": strings.Repeat("a", 64), "expected_state": "ready", "expected_weight": 100}
		for _, route := range []struct {
			method, path string
			body         any
		}{{"GET", prefix + "/discovery", nil}, {"POST", prefix + "/operations", operations}} {
			if got := f.request(route.method, route.path, route.body, false); got.Code != 401 {
				t.Fatal("anonymous control accepted", got.Code)
			}
			got := f.request(route.method, route.path, route.body, true)
			want := 404
			if id == node.ID {
				want = 409
				if strings.HasSuffix(route.path, "discovery") {
					want = 200
				}
			}
			if got.Code != want {
				t.Fatalf("%s got %d want %d: %s", route.path, got.Code, want, got.Body.String())
			}
		}
	}
	bad := map[string]any{"request_id": domain.NewID(), "kind": "weight", "backend": "application\nshutdown sessions server x/y", "server": "app-a", "weight": 200, "expected_generation": strings.Repeat("a", 64), "expected_state": "ready", "expected_weight": 100}
	if got := f.request("POST", "/v1/nodes/"+node.ID+"/operations", bad, true); got.Code != 400 {
		t.Fatal("CLI injection input accepted", got.Code)
	}
}
func TestControlBrowserPermissionsAndCSRF(t *testing.T) {
	f := browserFixture(t)
	node, _, _, _ := f.enrollNode(t)
	for _, manage := range []bool{false, true} {
		pending, nonce := challenge(t, f.server)
		claims := validClaims(f, nonce)
		if manage {
			claims.Capabilities = append(claims.Capabilities, "haproxy.manage")
		}
		created := exchange(t, f, pending, claims)
		cookie := sessionCookie(t, created)
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		_ = json.Unmarshal(created.Body.Bytes(), &session)
		for _, csrf := range []string{"", session.CSRF} {
			request := httptest.NewRequest("POST", "/v1/nodes/"+node.ID+"/operations", strings.NewReader(`{"request_id":"`+domain.NewID()+`","kind":"drain","backend":"application","server":"app-a","expected_generation":"`+strings.Repeat("a", 64)+`","expected_state":"ready","expected_weight":100}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", testBrowserOrigin)
			request.Header.Set("X-CSRF-Token", csrf)
			request.AddCookie(cookie)
			got := httptest.NewRecorder()
			f.server.ServeHTTP(got, request)
			want := 403
			if manage && csrf != "" {
				want = 409 // Authorized directly, but this node has not reported discovery.
			}
			if got.Code != want {
				t.Fatalf("manage=%v csrf=%v got=%d", manage, csrf != "", got.Code)
			}
		}
	}
}
func TestGRPCControlUsesMTLSAndVerifiesOperation(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	node, _, cert, key := f.enrollNode(t)
	https := httptest.NewUnstartedServer(f.server)
	https.EnableHTTP2 = true
	https.TLS = f.server.TLSConfig()
	https.Config.ReadTimeout = 100 * time.Millisecond
	https.StartTLS()
	defer https.Close()
	roots := x509.NewCertPool()
	roots.AddCert(https.Certificate())
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	pair, err := tls.X509KeyPair(cert, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	connection, err := grpc.NewClient(strings.TrimPrefix(https.URL, "https://"), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{pair}})))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := pb.NewControlClient(connection).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := discoveryForControl()
	if err = stream.Send(&pb.AgentReport{Discovery: controlwire.Discovery(&d)}); err != nil {
		t.Fatal(err)
	}
	frame, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	session := frame.SessionId
	if !domain.ValidID(session) || !frame.ManagementEnabled {
		t.Fatal("unexpected initial authority")
	}
	op, err := f.store.CreateOperation(t.Context(), f.org, domain.Operation{RequestID: domain.NewID(), NodeID: node.ID, Kind: "weight", Backend: "application", Server: "app-a", Weight: 200, ExpectedGeneration: d.Generation, ExpectedState: "ready", ExpectedWeight: 100})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if err = stream.Send(&pb.AgentReport{SessionId: session, Discovery: controlwire.Discovery(&d)}); err != nil {
		t.Fatal(err)
	}
	frame, err = stream.Recv()
	if err != nil {
		t.Fatal("control stream did not survive HTTP read deadline", err)
	}
	if !frame.ManagementEnabled || frame.Command.GetId() != op.ID {
		t.Fatal("operation was not delivered")
	}
	command := controlwire.ReadCommand(frame.Command)
	d.Servers[0].Weight = 200
	d.ObservedAtMS = time.Now().UnixMilli()
	result := domain.OperationResult{OperationID: op.ID, Outcome: "succeeded", Discovery: &d, CommandDigest: command.Digest()}
	if err = stream.Send(&pb.AgentReport{SessionId: session, Discovery: controlwire.Discovery(&d), Result: controlwire.Result(&result)}); err != nil {
		t.Fatal(err)
	}
	frame, err = stream.Recv()
	if err != nil || frame.AckId != op.ID {
		t.Fatal("verified result was not acknowledged", err)
	}
	_, operations, err := f.store.GetControl(t.Context(), f.org, node.ID)
	if err != nil || len(operations) != 1 || operations[0].Status != "succeeded" {
		t.Fatal("operation was not persisted", err)
	}
	if err = f.store.RevokeNode(t.Context(), f.org, node.ID); err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&pb.AgentReport{SessionId: session, Discovery: controlwire.Discovery(&d)})
	if _, err = stream.Recv(); err == nil {
		t.Fatal("revoked node retained control")
	}
}
