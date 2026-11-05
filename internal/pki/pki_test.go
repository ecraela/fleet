package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func TestIssuedIdentityIgnoresCSRClaims(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	if err := Init(dir, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		t.Fatal(err)
	}
	a, err := Load(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	u, _ := url.Parse("spiffe://attacker/organizations/other")
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "admin"}, DNSNames: []string{"admin.internal"}, URIs: []*url.URL{u}}, key)
	node := domain.Node{ID: domain.NewID(), OrganizationID: domain.NewID(), ClusterID: domain.NewID()}
	certPEM, err := a.Issue(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})), node, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := Identity(leaf)
	if err != nil || identity.ID != node.ID || identity.OrganizationID != node.OrganizationID || identity.ClusterID != node.ClusterID {
		t.Fatalf("incorrect identity: %+v %v", identity, err)
	}
	if leaf.IsCA || len(leaf.DNSNames) != 0 || leaf.Subject.CommonName != node.ID {
		t.Fatal("CSR claimed authority leaked into certificate")
	}
	pool := x509.NewCertPool()
	pool.AddCert(a.Certificate)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
		t.Fatal("agent certificate accepted as server")
	}
	verifier, err := Load(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Issue(string(certPEM), node, time.Now()); err == nil {
		t.Fatal("verifier signed a certificate")
	}
	if err := Init(dir, []string{"replacement"}, nil); err == nil {
		t.Fatal("existing PKI overwritten")
	}
}

func TestIdentityRejectsAmbiguousAndMalformedIDs(t *testing.T) {
	for _, raw := range []string{"spiffe://wrong/organizations/a/clusters/b/nodes/c", "spiffe://converge-haproxy/organizations/a/clusters/b/nodes/c", "https://converge-haproxy/organizations/a/clusters/b/nodes/c"} {
		u, _ := url.Parse(raw)
		if _, err := Identity(&x509.Certificate{URIs: []*url.URL{u}}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := Identity(nil); err == nil {
		t.Fatal("accepted absent certificate")
	}
}
