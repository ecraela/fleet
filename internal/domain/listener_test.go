package domain

import (
	"strings"
	"testing"
	"time"
)

func TestTLSCertificateGrantAndValidity(t *testing.T) {
	now := time.Now()
	b := ListenerBinding{Name: "public", Address: "*", Port: 8443}
	l := HTTPSListener{ListenerBinding: b, CertificateRef: "first", CertificateSHA256: strings.Repeat("a", 64)}
	c := TLSCertificate{Reference: l.CertificateRef, SHA256: l.CertificateSHA256, DNSNames: []string{"*.example.test"}, NotBeforeMS: now.Add(-time.Hour).UnixMilli(), NotAfterMS: now.Add(time.Hour).UnixMilli()}
	s := ConfigurationState{ListenerControlVersion: 1, ListenerBindings: []ListenerBinding{b}, TLSCertificates: []TLSCertificate{c}}
	a := HTTPApplication{Listener: "public", Hostname: "shop.example.test"}
	if !s.ListenerReady(l, []HTTPApplication{a}, now) || !s.ValidTLSInventory() {
		t.Fatal("valid local grant rejected")
	}
	a.Hostname = "nested.shop.example.test"
	if s.ListenerReady(l, []HTTPApplication{a}, now) {
		t.Fatal("wildcard covered multiple labels")
	}
	if s.ListenerReady(l, nil, now.Add(time.Hour)) || s.ListenerReady(l, nil, now.Add(-2*time.Hour)) {
		t.Fatal("invalid certificate time accepted")
	}
	changed := l
	changed.Port++
	if s.ListenerReady(changed, nil, now) {
		t.Fatal("remote port bypassed local policy")
	}
	changed = l
	changed.CertificateSHA256 = strings.Repeat("b", 64)
	if s.ListenerReady(changed, nil, now) {
		t.Fatal("remote alias changed leaf identity")
	}
	if ValidListeners([]HTTPSListener{l, l}) {
		t.Fatal("duplicate bind accepted")
	}
}

func TestIncomingPortForwardingMustAlreadyExistOnTheNode(t *testing.T) {
	d := ConfigurationDocument{Backends: []ConfigurationBackend{{Name: "nodes", Algorithm: "roundrobin", Servers: []ConfigurationServer{{Name: "node-a", Address: "node-a", ForwardIncomingPort: true, Weight: 100}}}}}
	if !d.KnownIncomingPorts(d) {
		t.Fatal("existing infrastructure profile could not be preserved")
	}
	fixed := d.Canonical()
	fixed.Backends[0].Servers[0].ForwardIncomingPort = false
	fixed.Backends[0].Servers[0].Port = 8080
	if d.KnownIncomingPorts(fixed) {
		t.Fatal("typed editor introduced incoming-port forwarding")
	}
}
