package dataplane

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
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
)

func testPEM(t *testing.T, host string, expires time.Time) ([]byte, tls.Certificate) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	raw := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})...)
	pair, e := tls.X509KeyPair(raw, raw)
	if e != nil {
		t.Fatal(e)
	}
	return raw, pair
}
func listenerFixture(t *testing.T) (*TLSPolicy, domain.HTTPSListener) {
	t.Helper()
	raw, _ := testPEM(t, "apps.example.test", time.Now().Add(time.Hour))
	path := filepath.Join(t.TempDir(), "first.pem")
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	ref := LocalCertificate{Reference: "first", Path: path}
	metadata, _, e := certificateFile(ref)
	if e != nil {
		t.Fatal(e)
	}
	b := domain.ListenerBinding{Name: "public", Address: "*", Port: 8443}
	return &TLSPolicy{Bindings: []domain.ListenerBinding{b}, Certificates: []LocalCertificate{ref}}, domain.HTTPSListener{ListenerBinding: b, CertificateRef: ref.Reference, CertificateSHA256: metadata.SHA256}
}
func TestCertificateInventoryDoesNotExportKeysOrPaths(t *testing.T) {
	policy, l := listenerFixture(t)
	c := &Client{TLS: policy}
	state := c.tlsInventory()
	raw, _ := json.Marshal(state)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), policy.Certificates[0].Path) || !state.ListenerReady(l, []domain.HTTPApplication{testApplication("/")}, time.Now()) {
		t.Fatal("unsafe/incomplete metadata")
	}
	if e := os.WriteFile(policy.Certificates[0].Path, []byte("invalid private material"), 0600); e != nil {
		t.Fatal(e)
	}
	if c.validateListeners(domain.ConfigurationDocument{Listeners: []domain.HTTPSListener{l}}) == nil || c.tlsInventory().TLSCertificates[0].ErrorCode != "certificate_invalid" {
		t.Fatal("changed local identity accepted")
	}
	rawExpired, _ := testPEM(t, "apps.example.test", time.Now().Add(-time.Minute))
	_ = os.WriteFile(policy.Certificates[0].Path, rawExpired, 0600)
	if c.tlsInventory().TLSCertificates[0].ErrorCode != "certificate_expired" {
		t.Fatal("expired material accepted")
	}
}
func TestTLSProbePinsLoadedCertificateAndHostname(t *testing.T) {
	policy, l := listenerFixture(t)
	raw, e := os.ReadFile(policy.Certificates[0].Path)
	if e != nil {
		t.Fatal(e)
	}
	pair, e := tls.X509KeyPair(raw, raw)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	port, e := strconv.Atoi(strings.Split(server.Listener.Addr().String(), ":")[1])
	if e != nil {
		t.Fatal(e)
	}
	l.Port = uint32(port)
	policy.Bindings[0].Port = l.Port
	c := &Client{TLS: policy}
	doc := domain.ConfigurationDocument{Listeners: []domain.HTTPSListener{l}}
	if result := c.observeTLS(t.Context(), doc); len(result) != 1 || !result[0].Loaded {
		t.Fatal("matching local TLS leaf not verified", result)
	}
	// Material on disk is changed to a different valid identity while the old worker is still serving.
	replacement, other := testPEM(t, "apps.example.test", time.Now().Add(time.Hour))
	_ = os.WriteFile(policy.Certificates[0].Path, replacement, 0600)
	sum := sha256.Sum256(other.Certificate[0])
	doc.Listeners[0].CertificateSHA256 = hex.EncodeToString(sum[:])
	if result := c.observeTLS(t.Context(), doc); result[0].Loaded {
		t.Fatal("accepted a worker serving a different leaf")
	}
}
func TestListenerTransactionPreservesLocalHTTPAndDetectsDrift(t *testing.T) {
	policy, l := listenerFixture(t)
	a := testApplication("/shop")
	var mu sync.Mutex
	f := object{}
	set(f, "name", "public")
	set(f, "mode", "http")
	set(f, "description", "local description")
	set(f, "default_backend", "legacy")
	set(f, "binds", map[string]any{"http": map[string]any{"name": "http", "address": "*", "port": 8080}})
	localRule := object{}
	set(localRule, "type", "set-header")
	set(localRule, "hdr_name", "X-Local")
	set(localRule, "hdr_format", "yes")
	set(f, "http_request_rule_list", []object{localRule})
	c := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Query().Get("transaction_id") != "local-tx" {
			t.Error("escaped transaction")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v3/services/haproxy/configuration/frontends":
			_ = json.NewEncoder(w).Encode([]object{f})
		case "PUT /v3/services/haproxy/configuration/frontends/public":
			if e := json.NewDecoder(r.Body).Decode(&f); e != nil {
				t.Error(e)
			}
		default:
			t.Error("unexpected local API", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	c.TLS = policy
	original := domain.ConfigurationDocument{Applications: []domain.HTTPApplication{a}}
	desired := original.Canonical()
	desired.Listeners = []domain.HTTPSListener{l}
	if e := c.applyListeners(t.Context(), "local-tx", original, desired); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	binds := decode[map[string]object](f["binds"])
	rules := decode[[]object](f["http_request_rule_list"])
	frontends := []object{f}
	mu.Unlock()
	if len(binds) != 2 || decode[uint32](binds["http"]["port"]) != 8080 || decode[string](f["default_backend"]) != "legacy" || normalizedRule(rules[len(rules)-1]) != normalizedRule(localRule) {
		t.Fatal("unmanaged HTTP config changed")
	}
	if projected, e := c.projectListeners(frontends, desired.Applications); e != nil || !domain.SameListeners(projected, desired.Listeners) {
		t.Fatal("managed projection", e)
	}
	mu.Lock()
	set(binds[listenerBind], "ssl_min_ver", "TLSv1.0")
	set(f, "binds", binds)
	mu.Unlock()
	if _, e := c.projectListeners(frontends, desired.Applications); e == nil {
		t.Fatal("unsafe local TLS drift silently accepted")
	}
	mu.Lock()
	binds[listenerBind], _ = c.listenerObject(l)
	set(f, "binds", binds)
	mu.Unlock()
	if e := c.applyListeners(t.Context(), "local-tx", desired, original); e != nil {
		t.Fatal("disable", e)
	}
	mu.Lock()
	defer mu.Unlock()
	if decode[string](f["description"]) != "local description" || len(decode[map[string]object](f["binds"])) != 1 || len(decode[[]object](f["http_request_rule_list"])) != 1 {
		t.Fatal("disable lost local fields")
	}
}
func TestListenerPolicyBoundsAndIncomingPortProfile(t *testing.T) {
	c := &Client{}
	path := filepath.Join(t.TempDir(), "policy.json")
	for _, raw := range []string{`{"bindings":[],"certificates":[]}`, `{"bindings":[{"name":"public","address":"*","port":8443}],"certificates":[{"reference":"first","path":"/var/lib/test.pem"}],"probe_host":"https://outside"}`, `{"bindings":[{"name":"public","address":"*","port":8443}],"certificates":[{"reference":"first","path":"/var/lib/../test.pem"}]}`} {
		_ = os.WriteFile(path, []byte(raw), 0600)
		if c.LoadTLSPolicy(path) == nil {
			t.Fatal("unsafe local policy accepted")
		}
	}
	var raw []object
	_ = json.Unmarshal([]byte(`[{"name":"nodes","mode":"tcp","balance":{"algorithm":"roundrobin"},"servers":{"one":{"name":"one","address":"haproxy-a","health_check_port":8404,"weight":100}}}]`), &raw)
	doc, e := project(raw)
	if e != nil || !doc.Backends[0].Servers[0].ForwardIncomingPort {
		t.Fatal("bounded passthrough profile", e)
	}
	delete(raw[0], "mode")
	if _, e = project(raw); e == nil {
		t.Fatal("missing port accepted for HTTP backend")
	}
}

func TestTLSGuardOrderIsIndependentOfDataplaneBackendOrder(t *testing.T) {
	a, b := testApplication("/shop"), testApplication("/admin")
	first, _ := json.Marshal(listenerRules([]domain.HTTPApplication{a, b}, "public"))
	second, _ := json.Marshal(listenerRules([]domain.HTTPApplication{b, a}, "public"))
	if string(first) != string(second) {
		t.Fatal("DPA backend ordering changed owned TLS guards")
	}
}

func TestHTTPSDoesNotCollideWithAnExistingPortRange(t *testing.T) {
	policy, l := listenerFixture(t)
	wrote := false
	var f object
	_ = json.Unmarshal([]byte(`{"name":"public","mode":"http","binds":{"existing":{"name":"existing","port":8000,"port-range-end":9000}}}`), &f)
	c := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			wrote = true
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode([]object{f})
	})
	c.TLS = policy
	if e := c.applyListeners(t.Context(), "local", domain.ConfigurationDocument{}, domain.ConfigurationDocument{Listeners: []domain.HTTPSListener{l}}); e == nil || wrote {
		t.Fatal("HTTPS collided with an existing bind range", e, wrote)
	}
}

func TestAutomaticTLSCanStartWithOnlyLocalBindingAndSeparateNodePins(t *testing.T) {
	c := &Client{NodeID: domain.NewID()}
	path := filepath.Join(t.TempDir(), "tls.json")
	if e := os.WriteFile(path, []byte(`{"bindings":[{"name":"public","address":"*","port":8443}],"certificates":[]}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := c.LoadTLSPolicy(path); e != nil {
		t.Fatal("initial issuance required an existing certificate", e)
	}
	policy, l := listenerFixture(t)
	c.DynamicCertificates = func() []LocalCertificate { return policy.Certificates }
	l.NodeCertificates = map[string]string{c.NodeID: l.CertificateSHA256, domain.NewID(): strings.Repeat("b", 64)}
	l.CertificateSHA256 = ""
	if e := c.validateListeners(domain.ConfigurationDocument{Listeners: []domain.HTTPSListener{l}}); e != nil {
		t.Fatal("local node fingerprint rejected", e)
	}
	delete(l.NodeCertificates, c.NodeID)
	if e := c.validateListeners(domain.ConfigurationDocument{Listeners: []domain.HTTPSListener{l}}); e == nil {
		t.Fatal("another node's certificate authority admitted")
	}
}
