package agent

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/pki"
	"github.com/ecraela/fleet/internal/telemetry"
)

func TestEnrollmentPersistenceMTLSAndRenewal(t *testing.T) {
	base := t.TempDir()
	pkiDir := filepath.Join(base, "pki")
	if err := pki.Init(pkiDir, nil, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		t.Fatal(err)
	}
	authority, err := pki.Load(pkiDir, true)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(pkiDir, "server.crt"), filepath.Join(pkiDir, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	node := domain.Node{ID: domain.NewID(), OrganizationID: domain.NewID(), ClusterID: domain.NewID(), Name: "haproxy-1"}
	var enrolled, renewed, writes atomic.Int32
	written := make(chan struct{}, 4)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/enroll", "/v1/identity/renew":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("bad request")
				w.WriteHeader(400)
				return
			}
			if strings.Contains(body["csr_pem"], "PRIVATE KEY") || body["key_pem"] != "" {
				t.Error("agent transmitted private key")
			}
			if r.URL.Path == "/v1/enroll" {
				if body["token"] != "bootstrap-secret-for-test-only" {
					t.Error("wrong token")
				}
				enrolled.Add(1)
			} else {
				if len(r.TLS.PeerCertificates) != 1 {
					t.Error("renewal missing client identity")
				}
				renewed.Add(1)
			}
			cert, err := authority.Issue(body["csr_pem"], node, time.Now())
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(identity{Node: node, CertificatePEM: string(cert), CAPEM: string(authority.PEM)})
		case "/v1/metrics/write":
			if len(r.TLS.PeerCertificates) != 1 {
				t.Error("metrics missing mTLS identity")
				w.WriteHeader(401)
				return
			}
			if r.Header.Get("X-Prometheus-Remote-Write-Version") != "0.1.0" || r.Header.Get("Content-Encoding") != "snappy" {
				t.Error("wrong remote write protocol")
			}
			if _, err := telemetry.Decode(r.Body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			writes.Add(1)
			written <- struct{}{}
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	})
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = authority.TLSConfig(false)
	srv.TLS.Certificates = []tls.Certificate{pair}
	srv.StartTLS()
	defer srv.Close()
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("haproxy_backend_status{proxy=\"api\"} 1\n"))
	}))
	defer metrics.Close()
	tokenPath := filepath.Join(base, "token")
	if err := os.WriteFile(tokenPath, []byte("bootstrap-secret-for-test-only"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ServerURL: srv.URL, IngestURL: srv.URL, ServerCA: filepath.Join(pkiDir, "ca.crt"), EnrollmentTokenFile: tokenPath, StateDir: filepath.Join(base, "state"), Name: node.Name, MetricsURL: metrics.URL, Interval: time.Hour}
	runOnce := func() {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- (&Agent{Config: cfg}).Run(ctx) }()
		select {
		case <-written:
			cancel()
		case err := <-result:
			cancel()
			t.Fatalf("agent exited before metrics: %v", err)
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("agent did not export immediate first scrape")
		}
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected exit: %v", err)
		}
	}
	runOnce()
	statePath := filepath.Join(cfg.StateDir, "identity.json")
	state, err := readState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(statePath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("identity permissions")
	}
	dir, _ := os.Stat(cfg.StateDir)
	if dir.Mode().Perm() != 0700 {
		t.Fatal("state directory permissions")
	}
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	runOnce()
	if enrolled.Load() != 1 || writes.Load() != 2 {
		t.Fatal("restart reenrolled instead of reusing identity")
	}
	// Move the valid client certificate into its renewal window, retaining key.
	// tls.Certificate exposes the existing signer through the standard parser.
	keypair, err := tls.X509KeyPair([]byte(state.CertificatePEM), []byte(state.KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, keypair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	csr := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	cert, err := authority.Issue(csr, node, time.Now().Add(-6*24*time.Hour-12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	state.CertificatePEM = string(cert)
	if err := saveState(statePath, state); err != nil {
		t.Fatal(err)
	}
	runOnce()
	if renewed.Load() != 1 || enrolled.Load() != 1 {
		t.Fatal("certificate was not renewed through existing mTLS identity")
	}
}

func TestPrivateStateRejectsUnsafeCredentials(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "private")
	if err := os.WriteFile(good, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(link, 100); err == nil {
		t.Fatal("accepted credential symlink")
	}
	if err := os.Chmod(good, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(good, 100); err == nil {
		t.Fatal("accepted public credential file")
	}
	if err := saveState(link, &identity{}); err == nil {
		t.Fatal("replaced symlink credential")
	}
	unsafe := filepath.Join(dir, "unsafe")
	os.Mkdir(unsafe, 0755)
	if err := secureDir(unsafe); err == nil {
		t.Fatal("accepted public state directory")
	}
	if err := secureDir(filepath.Join(dir, "safe")); err != nil {
		t.Fatal(err)
	}
	lock, err := lockState(filepath.Join(dir, "safe"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if other, err := lockState(filepath.Join(dir, "safe")); err == nil {
		other.Close()
		t.Fatal("allowed concurrent identity owners")
	}
}

func TestExistingInvalidStateNeverReenrolls(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	os.WriteFile(filepath.Join(dir, "identity.json"), []byte("invalid"), 0600)
	var requests atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer srv.Close()
	cfg := Config{ServerURL: srv.URL, IngestURL: srv.URL, StateDir: dir, Name: "node", Interval: time.Second}
	if err := (&Agent{Config: cfg}).Run(context.Background()); err == nil {
		t.Fatal("accepted invalid stored identity")
	}
	if requests.Load() != 0 {
		t.Fatal("re-enrolled with existing state")
	}
}

func TestEndpointAndRetryBounds(t *testing.T) {
	for _, raw := range []string{"http://manager.example", "https://user:secret@manager.example", "https://manager.example?token=secret", "https://manager.example/other"} {
		if _, err := endpoint(raw, ""); err == nil {
			t.Fatal("accepted unsafe endpoint")
		}
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer srv.Close()
	if err := export(context.Background(), srv.Client(), srv.URL, []byte("batch")); err == nil {
		t.Fatal("expected export failure")
	}
	if requests.Load() != 2 {
		t.Fatalf("retry count: %d", requests.Load())
	}
	requests.Store(0)
	noRetry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(401) }))
	defer noRetry.Close()
	export(context.Background(), noRetry.Client(), noRetry.URL, nil)
	if requests.Load() != 1 {
		t.Fatal("retried rejected credential")
	}
}
