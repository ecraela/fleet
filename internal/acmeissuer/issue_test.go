package acmeissuer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
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
	"github.com/miekg/dns"
)

// RFC 8555 finalization may omit Location. The known order URI must survive
// asynchronous processing, and restart/retry must reuse the same local key.
func TestAsyncOrderFinalizationAndImmutableLocalRecovery(t *testing.T) {
	rootKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	rootDER, e := x509.CreateCertificate(rand.Reader, template, template, &rootKey.PublicKey, rootKey)
	if e != nil {
		t.Fatal(e)
	}
	root, _ := x509.ParseCertificate(rootDER)
	var base string
	var issued atomic.Bool
	var finalized atomic.Int32
	var certificate []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", base64.RawURLEncoding.EncodeToString([]byte(domain.NewID())))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/dir":
			_ = json.NewEncoder(w).Encode(map[string]string{"newNonce": base + "/nonce", "newAccount": base + "/account", "newOrder": base + "/new-order"})
		case "/nonce":
			w.WriteHeader(200)
		case "/account":
			w.Header().Set("Location", base+"/account/1")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "valid"})
		case "/new-order", "/order":
			w.Header().Set("Location", base+"/order")
			if r.URL.Path == "/new-order" {
				w.WriteHeader(http.StatusCreated)
			}
			status := "ready"
			if issued.Load() {
				status = "valid"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "finalize": base + "/finalize", "certificate": base + "/cert", "identifiers": []map[string]string{{"type": "dns", "value": "app.example.test"}}})
		case "/finalize":
			var jws struct{ Payload string }
			if json.NewDecoder(r.Body).Decode(&jws) != nil {
				t.Error("JWS unavailable")
				w.WriteHeader(400)
				return
			}
			raw, _ := base64.RawURLEncoding.DecodeString(jws.Payload)
			var value struct{ CSR string }
			_ = json.Unmarshal(raw, &value)
			der, _ := base64.RawURLEncoding.DecodeString(value.CSR)
			csr, e := x509.ParseCertificateRequest(der)
			if e != nil || csr.CheckSignature() != nil {
				t.Error("invalid CSR")
				w.WriteHeader(400)
				return
			}
			leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: csr.DNSNames, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			der, e = x509.CreateCertificate(rand.Reader, leaf, root, csr.PublicKey, rootKey)
			if e != nil {
				t.Error(e)
				return
			}
			certificate = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})...)
			issued.Store(true)
			finalized.Add(1)
			// Intentionally no Location header on asynchronous finalization.
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "processing"})
		case "/cert":
			w.Header().Set("Content-Type", "application/pem-certificate-chain")
			_, _ = w.Write(certificate)
		default:
			t.Error("unexpected ACME path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	base = server.URL
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	dnsServer := &dns.Server{Listener: listener, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		out := new(dns.Msg)
		out.SetReply(m)
		out.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: "_acme-challenge.app.example.test.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET}, Target: "app.validation.test."}}
		_ = w.WriteMsg(out)
	})}
	go dnsServer.ActivateAndServe()
	defer dnsServer.Shutdown()
	roots := x509.NewCertPool()
	roots.AddCert(root)
	roots.AddCert(server.Certificate())
	grant := domain.ACMEGrant{Issuer: "local", Listener: "public", Environment: "staging", PolicyDigest: strings.Repeat("a", 64), Challenges: []domain.ACMEChallenge{{Hostname: "app.example.test", Target: "app.validation.test"}}}
	p := Profile{Issuer: grant.Issuer, Listener: grant.Listener, DirectoryURL: base + "/dir", origin: base, Resolver: listener.Addr().String(), Challenges: grant.Challenges, roots: roots, grant: grant}
	r := &Runner{state: t.TempDir(), policy: Policy{CertificateDir: t.TempDir(), Profiles: []Profile{p}}}
	hash := sha256.Sum256([]byte(p.DirectoryURL))
	accountName := p.Issuer + "-" + hex.EncodeToString(hash[:8])
	if _, _, e = localKey(filepath.Join(r.state, accountName+".account.key")); e != nil {
		t.Fatal(e)
	}
	if e = saveJSON(filepath.Join(r.state, accountName+".account.json"), savedAccount{URI: base + "/account/old"}); e != nil {
		t.Fatal(e)
	}
	j := domain.ACMEJob{ID: domain.NewID(), PlanID: domain.NewID(), Generation: 1, NodeID: domain.NewID(), Issuer: "local", Listener: "public", Hostnames: []string{"app.example.test"}, PolicyDigest: grant.PolicyDigest, DeadlineMS: time.Now().Add(20 * time.Second).UnixMilli()}
	j.Reference = domain.ACMEReference(j.PlanID, j.Generation)
	if e = saveJSON(filepath.Join(r.state, j.Reference+".order.json"), savedOrder{URI: base + "/obsolete-order"}); e != nil {
		t.Fatal(e)
	}
	result := r.Issue(t.Context(), j)
	if result.ErrorCode != "" || result.Certificate == nil || finalized.Load() != 1 {
		t.Fatal("asynchronous finalization", result.ErrorCode, finalized.Load())
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "BEGIN CERTIFICATE") {
		t.Fatal("secret material crossed boundary")
	}
	info, e := os.Stat(filepath.Join(r.policy.CertificateDir, j.Reference+".pem"))
	if e != nil || info.Mode().Perm() != 0640 {
		t.Fatal("certificate permissions", e)
	}
	again := r.Issue(t.Context(), j)
	if again.ErrorCode != "" || again.Certificate.SHA256 != result.Certificate.SHA256 || finalized.Load() != 1 {
		t.Fatal("retry replaced immutable certificate")
	}
	bad := j
	bad.PolicyDigest = strings.Repeat("b", 64)
	if result = r.Issue(t.Context(), bad); result.ErrorCode != "policy_unavailable" {
		t.Fatal("ungranted policy admitted")
	}
}
