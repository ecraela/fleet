package appprobe

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ecraela/fleet/internal/domain"
)

func app() domain.HTTPApplication {
	return domain.HTTPApplication{ID: domain.NewID(), Name: "Store", Listener: "public", Hostname: "example.com", PathPrefix: "/shop", HealthPath: "/healthz", Algorithm: "roundrobin", Servers: []domain.ConfigurationServer{{Name: "web", Address: "localhost", Port: 8080, Weight: 100}}}
}
func checkFor(t *testing.T, s *httptest.Server) Check {
	t.Helper()
	host, port, _ := net.SplitHostPort(s.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return Check{Name: "entry-http", Listener: "public", Hostname: "example.com", PathPrefix: "/shop", Path: "/shop/items", Scheme: "http", Address: host, Port: uint32(p), ExpectedStatus: 200, ResponseHeader: "X-App", ResponseValue: "store"}
}
func load(t *testing.T, checks ...Check) *Runner {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"checks": checks})
	path := filepath.Join(t.TempDir(), "probes.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestProbeUsesExplicitHostPathAndResponseContract(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Host != "example.com" || r.URL.Path != "/shop/items" {
			t.Errorf("unexpected request %s %s", r.Host, r.URL.Path)
		}
		w.Header().Set("X-App", "store")
		_, _ = w.Write([]byte("ok"))
	}))
	defer s.Close()
	c := checkFor(t, s)
	r := load(t, c)
	a := app()
	p := r.Check(t.Context(), []domain.HTTPApplication{a})[0]
	if !p.Valid() || p.ErrorCode != "" || p.StatusCode != 200 || p.SpecDigest != a.Digest() {
		t.Fatalf("invalid healthy evidence: %+v", p)
	}
	c.ResponseValue = "different"
	p = load(t, c).Check(t.Context(), []domain.HTTPApplication{a})[0]
	if p.ErrorCode != "response_mismatch" {
		t.Fatal(p)
	}
	foreign := a
	disabled := a
	disabled.Disabled = true
	if len(r.Check(t.Context(), []domain.HTTPApplication{disabled})) != 0 || requests.Load() != 2 {
		t.Fatal("disabled application was probed as an unavailable live app")
	}
	foreign.Hostname = "other.example.com"
	if len(r.Check(t.Context(), []domain.HTTPApplication{foreign})) != 0 || requests.Load() != 2 {
		t.Fatal("remote app changed local probe authority")
	}
	neighbor := a
	neighbor.ID = domain.NewID()
	neighbor.PathPrefix = "/shop/items"
	p = r.Check(t.Context(), []domain.HTTPApplication{a, neighbor})[0]
	if p.ErrorCode != "route_conflict" || requests.Load() != 2 {
		t.Fatal("probe crossed into a neighboring route", p)
	}
}

func TestNodeProbeBindsEvidenceToLocalArtifactAndPolicy(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-App", "store") }))
	defer s.Close()
	c := checkFor(t, s)
	c.Scope = "node"
	r := load(t, c)
	a := app()
	if len(r.Check(t.Context(), []domain.HTTPApplication{a})) != 0 {
		t.Fatal("direct evidence without a loaded snapshot")
	}
	input := snapshot{Apps: []domain.HTTPApplication{a}, ArtifactID: strings.Repeat("a", 64), Generation: strings.Repeat("b", 64)}
	p := r.checkSnapshot(t.Context(), input)[0]
	grants := r.Grants()
	if !p.Valid() || p.Scope != "node" || p.ArtifactID != input.ArtifactID || p.Generation != input.Generation || len(grants) != 1 || !grants[0].Valid() || p.PolicyDigest != grants[0].PolicyDigest {
		t.Fatal("uncorrelated direct result", p, grants)
	}
	c.ResponseValue = "changed"
	changed := load(t, c).Grants()[0]
	if changed.PolicyDigest == grants[0].PolicyDigest {
		t.Fatal("response policy change retained old authority")
	}
	c.Scope = "peer"
	raw, _ := json.Marshal(map[string]any{"checks": []Check{c}})
	path := filepath.Join(t.TempDir(), "invalid.json")
	_ = os.WriteFile(path, raw, 0600)
	if _, err := Load(t.Context(), path); err == nil {
		t.Fatal("unknown probe scope accepted")
	}
}

func TestEntrypointEvidenceCannotHideBehindHealthyDirectNodeChecks(t *testing.T) {
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-App", "store") }))
	defer direct.Close()
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer entry.Close()
	node, edge := checkFor(t, direct), checkFor(t, entry)
	node.Scope, node.Name = "node", "direct-http"
	edge.Scope, edge.Name = "entrypoint", "entrypoint-http"
	r := load(t, node, edge)
	input := snapshot{Apps: []domain.HTTPApplication{app()}, ArtifactID: strings.Repeat("a", 64), Generation: strings.Repeat("b", 64)}
	probes := r.checkSnapshot(t.Context(), input)
	if len(probes) != 2 || probes[0].ErrorCode != "" || probes[1].ErrorCode != "response_status" {
		t.Fatal("shared entrypoint failure was hidden by direct success", probes)
	}
	for _, p := range probes {
		if !p.Valid() || p.ArtifactID != input.ArtifactID || p.Generation != input.Generation {
			t.Fatal("probe lost the initiating agent's loaded context", p)
		}
	}
}
func TestProbeDoesNotFollowRedirectsOrEnvironmentProxy(t *testing.T) {
	var followed atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	defer destination.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer s.Close()
	t.Setenv("HTTP_PROXY", destination.URL)
	t.Setenv("HTTPS_PROXY", destination.URL)
	p := load(t, checkFor(t, s)).Check(t.Context(), []domain.HTTPApplication{app()})[0]
	if p.ErrorCode != "response_status" || p.StatusCode != 302 || followed.Load() != 0 {
		t.Fatal("probe escaped destination", p)
	}
}
func TestProbeVerifiesTLSChainHostnameAndReportsPublicLeaf(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("X-App", "store") }))
	defer s.Close()
	c := checkFor(t, s)
	c.Scheme = "https"
	c.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(c.CAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	a := app()
	a.Hostname = s.Certificate().DNSNames[0]
	c.Hostname = a.Hostname
	p := load(t, c).Check(t.Context(), []domain.HTTPApplication{a})[0]
	if !p.Valid() || p.ErrorCode != "" || !p.TLSVerified || !domain.ValidDigest(p.CertificateSHA256) {
		t.Fatal("trusted TLS probe failed", p)
	}
	c.Hostname = "not-in-certificate.invalid"
	a.Hostname = c.Hostname
	p = load(t, c).Check(t.Context(), []domain.HTTPApplication{a})[0]
	if !p.Valid() || p.ErrorCode != "tls_failed" || p.TLSVerified {
		t.Fatal("hostname verification bypassed", p)
	}
	c.CAFile = ""
	p = load(t, c).Check(t.Context(), []domain.HTTPApplication{a})[0]
	if p.ErrorCode != "tls_failed" {
		t.Fatal("untrusted certificate accepted", p)
	}
}
func TestProbeLimitsResponseAndCancellation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", (64<<10)+2))) }))
	defer s.Close()
	r := load(t, checkFor(t, s))
	p := r.Check(t.Context(), []domain.HTTPApplication{app()})[0]
	if p.ErrorCode != "response_too_large" {
		t.Fatal(p)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p = r.Check(ctx, []domain.HTTPApplication{app()})[0]
	if p.ErrorCode == "" {
		t.Fatal("cancelled check succeeded")
	}
}
func TestPolicyRejectsUnboundedOrAmbiguousAuthority(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer s.Close()
	c := checkFor(t, s)
	for _, mutate := range []func(*Check){func(c *Check) { c.Address = "169.254.169.254" }, func(c *Check) { c.Path = "//other.example.com" }, func(c *Check) { c.Scheme = "ftp" }, func(c *Check) { c.ResponseHeader = "X-App\r\nHost" }, func(c *Check) { c.ExpectedStatus = 0 }, func(c *Check) { c.CAFile = "relative.pem" }} {
		bad := c
		mutate(&bad)
		raw, _ := json.Marshal(map[string]any{"checks": []Check{bad}})
		path := filepath.Join(t.TempDir(), "bad.json")
		_ = os.WriteFile(path, raw, 0600)
		if _, err := Load(t.Context(), path); err == nil {
			t.Fatal("unsafe policy accepted", bad.Name)
		}
	}
}
