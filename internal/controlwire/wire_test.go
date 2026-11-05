package controlwire

import (
	"encoding/json"
	pb "github.com/ecraela/fleet/internal/controlv1"
	"github.com/ecraela/fleet/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestTrafficPolicyAndHostCapabilitiesRoundTrip(t *testing.T) {
	a := domain.HTTPApplication{ID: domain.NewID(), TrafficPolicy: &domain.TrafficPolicy{Version: 1, Mode: "observe", UnknownAction: "allow", Rules: []domain.TrafficRule{{ID: domain.NewID(), Name: "Country", Action: "block", CIDRs: []string{"192.0.2.0/24"}, Countries: []string{"RU"}, ASNs: []uint32{15169}}}}}
	d := domain.ConfigurationState{ApplicationControlVersion: 6, TrafficRules: &domain.TrafficCapabilities{Profile: strings.Repeat("a", 64), ClientSource: "proxy_protocol", CountryReady: true, ASNReady: true}, Document: &domain.ConfigurationDocument{Applications: []domain.HTTPApplication{a}, Backends: []domain.ConfigurationBackend{}}}
	x, _ := json.Marshal(d)
	y, _ := json.Marshal(ReadConfigurationState(ConfigurationState(&d)))
	if string(x) != string(y) {
		t.Fatal("traffic authority or ordered intent lost on wire")
	}
}

func TestSetupProtocolBoundsAndOlderAgents(t *testing.T) {
	setup := &domain.NodeSetup{ObservedAtMS: 1, Owner: "external", Checks: []domain.SetupCheck{
		domain.SetupResult("metrics", "metrics_ready"), domain.SetupResult("discovery", "runtime_not_configured"),
		domain.SetupResult("files", "files_not_configured"), domain.SetupResult("ownership", "owner_external"),
		domain.SetupResult("runtime_control", "runtime_control_disabled"), domain.SetupResult("configuration_control", "configuration_control_disabled"),
	}}
	wire := Discovery(&domain.Discovery{Setup: setup})
	restored := ReadDiscovery(wire)
	if restored == nil || restored.Setup == nil || !restored.Setup.Valid() {
		t.Fatal("setup diagnostics lost in control stream")
	}
	wire.Setup.Checks = append(wire.Setup.Checks, &pb.SetupCheck{Name: "extra"})
	if ReadDiscovery(wire) != nil {
		t.Fatal("unbounded setup check list accepted")
	}
	old := ReadDiscovery(&pb.Discovery{})
	if old == nil || old.Setup != nil {
		t.Fatal("older agent compatibility lost")
	}
}

func TestTLSInventoryAndPinnedCommandRoundTrip(t *testing.T) {
	l := domain.HTTPSListener{ListenerBinding: domain.ListenerBinding{Name: "public", Address: "*", Port: 8443}, CertificateRef: "demo", CertificateSHA256: strings.Repeat("a", 64)}
	d := domain.Discovery{Configuration: &domain.ConfigurationState{ListenerControlVersion: 1, ListenerBindings: []domain.ListenerBinding{l.ListenerBinding}, TLSCertificates: []domain.TLSCertificate{{Reference: "demo", SHA256: l.CertificateSHA256, DNSNames: []string{"apps.example.test"}, NotBeforeMS: 1, NotAfterMS: 2}}, TLSListeners: []domain.TLSListenerObservation{{Name: "public", SHA256: l.CertificateSHA256, Loaded: true}}, Document: &domain.ConfigurationDocument{Listeners: []domain.HTTPSListener{l}, Backends: []domain.ConfigurationBackend{{Name: "nodes", Algorithm: "roundrobin", Servers: []domain.ConfigurationServer{{Name: "one", Address: "node", ForwardIncomingPort: true}}}}}}}
	decoded := ReadDiscovery(Discovery(&d))
	a, _ := json.Marshal(d.Configuration)
	b, _ := json.Marshal(decoded.Configuration)
	if string(a) != string(b) {
		t.Fatal("TLS protocol lost bounded inventory or incoming-port policy")
	}
	op := domain.Operation{Kind: "configuration", Configuration: d.Configuration.Document}
	restored := ReadCommand(Command(&op))
	if op.Digest() != restored.Digest() {
		t.Fatal("TLS protocol changed command identity")
	}
}

func TestNodeLocalACMEAuthorityAndCertificatesRoundTrip(t *testing.T) {
	node := domain.NewID()
	pin := strings.Repeat("a", 64)
	l := domain.HTTPSListener{ListenerBinding: domain.ListenerBinding{Name: "public", Address: "*", Port: 8443}, CertificateRef: "acme-issued", NodeCertificates: map[string]string{node: pin, domain.NewID(): strings.Repeat("b", 64)}}
	d := domain.Discovery{Configuration: &domain.ConfigurationState{NodeID: node, ListenerControlVersion: 1, ACMEGrants: []domain.ACMEGrant{{Issuer: "local-issuer", Listener: "public", Environment: "staging", PolicyDigest: strings.Repeat("c", 64), Challenges: []domain.ACMEChallenge{{Hostname: "apps.example.test", Target: "apps.validation.example.test"}}}}, Document: &domain.ConfigurationDocument{Listeners: []domain.HTTPSListener{l}, Backends: []domain.ConfigurationBackend{{Name: "application", Algorithm: "roundrobin", Servers: []domain.ConfigurationServer{{Name: "server", Address: "127.0.0.1", Port: 8080, Weight: 100}}}}}}}
	x, _ := json.Marshal(d.Configuration)
	y, _ := json.Marshal(ReadDiscovery(Discovery(&d)).Configuration)
	if string(x) != string(y) {
		t.Fatal("wire changed node-local certificate pins or ACME authority")
	}
	op := domain.Operation{Kind: "configuration", Configuration: d.Configuration.Document}
	if op.Digest() != ReadCommand(Command(&op)).Digest() {
		t.Fatal("wire changed per-node TLS command identity")
	}
}

func TestApplicationProbeRoundTrip(t *testing.T) {
	d := domain.Discovery{ApplicationProbes: []domain.ApplicationProbe{{ApplicationID: domain.NewID(), SpecDigest: strings.Repeat("a", 64), Name: "store", Scheme: "https", Path: "/shop", ExpectedStatus: 200, StatusCode: 200, ObservedAtMS: 1, DurationMS: 2, TLSVerified: true, CertificateSHA256: strings.Repeat("b", 64), CertificateNotAfterMS: 3}}}
	x, _ := json.Marshal(d.ApplicationProbes)
	y, _ := json.Marshal(ReadDiscovery(Discovery(&d)).ApplicationProbes)
	if string(x) != string(y) {
		t.Fatal("probe protocol changed health evidence")
	}
}

func TestDirectProbeAuthorityAndVerificationPolicyRoundTrip(t *testing.T) {
	a := domain.HTTPApplication{ID: domain.NewID(), RequestTelemetry: true, Verification: &domain.ApplicationVerificationPolicy{HTTP: true, HTTPS: true, Entrypoint: true}}
	d := domain.Discovery{ApplicationProbeGrants: []domain.ApplicationProbeGrant{{Name: "local-http", Scope: "node", PolicyDigest: strings.Repeat("a", 64)}}, ApplicationProbes: []domain.ApplicationProbe{{ApplicationID: a.ID, Scope: "node", PolicyDigest: strings.Repeat("a", 64), ArtifactID: strings.Repeat("b", 64), Generation: strings.Repeat("c", 64)}}, Configuration: &domain.ConfigurationState{ApplicationControlVersion: 4, Document: &domain.ConfigurationDocument{Applications: []domain.HTTPApplication{a}, Backends: []domain.ConfigurationBackend{}}}}
	decoded := ReadDiscovery(Discovery(&d))
	x, _ := json.Marshal(d.ApplicationProbeGrants)
	y, _ := json.Marshal(decoded.ApplicationProbeGrants)
	if string(x) != string(y) {
		t.Fatal("wire lost local check authority")
	}
	x, _ = json.Marshal(d.ApplicationProbes)
	y, _ = json.Marshal(decoded.ApplicationProbes)
	if string(x) != string(y) || !d.Configuration.Document.Equal(*decoded.Configuration.Document) {
		t.Fatal("wire lost direct evidence or required protocols")
	}
}

func TestConfigurationCommandRoundTripRetainsJournalDigest(t *testing.T) {
	doc := domain.ConfigurationDocument{Backends: []domain.ConfigurationBackend{{Name: "application", Algorithm: "roundrobin", Servers: []domain.ConfigurationServer{{Name: "app-a", Address: "127.0.0.1", Port: 8080, Weight: 100}}}}}
	op := domain.Operation{ID: domain.NewID(), Kind: "configuration", ExpectedGeneration: strings.Repeat("a", 64), ConfigurationRevisionID: domain.NewID(), Configuration: &doc, BaseArtifactID: strings.Repeat("b", 64), BaseVersion: 7, RollbackArtifactID: strings.Repeat("c", 64), ExpectedProcessID: "8", Deadline: time.Now().Add(time.Minute)}
	decoded := ReadCommand(Command(&op))
	if decoded.Digest() != op.Digest() {
		t.Fatal("wire conversion changed operation identity")
	}
}
func TestDisabledApplicationRoundTrip(t *testing.T) {
	a := domain.HTTPApplication{ID: domain.NewID(), Name: "Paused", Listener: "public", Hostname: "apps.example.test", PathPrefix: "/shop", HealthPath: "/healthz", Algorithm: "roundrobin", Disabled: true, Servers: []domain.ConfigurationServer{{Name: "web", Address: "localhost", Port: 8080, Weight: 100}}}
	doc := &domain.ConfigurationDocument{Backends: []domain.ConfigurationBackend{}, Applications: []domain.HTTPApplication{a}}
	decoded := ReadConfigurationDocument(ConfigurationDocument(doc))
	if !decoded.Applications[0].Disabled || !doc.Equal(*decoded) {
		t.Fatal("protocol lost disabled route ownership")
	}
}

func TestFrontendConnectionsPreserveUnknownVersusZero(t *testing.T) {
	for _, value := range []*uint32{nil, new(uint32), new(uint32(12))} {
		d := domain.Discovery{Proxies: []domain.Proxy{{Name: "public", Kind: "frontend", ActiveConnections: value}}}
		decoded := ReadDiscovery(Discovery(&d))
		actual := decoded.Proxies[0].ActiveConnections
		if (value == nil) != (actual == nil) || (value != nil && *value != *actual) {
			t.Fatal("wire changed connection presence")
		}
	}
}
