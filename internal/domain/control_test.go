package domain

import (
	"strings"
	"testing"
	"time"
)

func discoveryFixture() Discovery {
	d := Discovery{Version: "3.4.7", ProcessID: "8", StartedAt: 1792800000, ObservedAtMS: time.Now().UnixMilli(), ConfigFingerprint: strings.Repeat("a", 64), ConfigFiles: 1,
		Proxies: []Proxy{{Name: "public", Kind: "frontend", Mode: "http"}},
		Servers: []BackendServer{{Backend: "application", Name: "app-a", State: "ready", Weight: 100, InitialWeight: 100, Health: "UP"}}}
	d.SetGeneration()
	return d
}
func TestDiscoveryGenerationTracksConfigurationButNotLiveCounters(t *testing.T) {
	d := discoveryFixture()
	if !d.Valid() {
		t.Fatal("fixture invalid")
	}
	generation := d.Generation
	d.Servers[0].Weight = 200
	d.Servers[0].State = "drain"
	d.Servers[0].ActiveConnections = 42
	d.Servers[0].Health = "DRAIN"
	d.ObservedAtMS++
	d.SetGeneration()
	if d.Generation != generation {
		t.Fatal("runtime observation changed configuration generation")
	}
	d.StartedAt++
	d.SetGeneration()
	if d.Generation == generation {
		t.Fatal("process change did not change generation")
	}
}
func TestDiscoveryRejectsOversizedErrorFieldsAndInventory(t *testing.T) {
	d := Discovery{ErrorCode: "runtime_unavailable", Proxies: []Proxy{}, Servers: []BackendServer{}}
	if !d.Valid() {
		t.Fatal("clean failure rejected")
	}
	d.ProcessID = strings.Repeat("x", MaxDiscoveryBytes+1)
	if d.Valid() {
		t.Fatal("oversized failure accepted")
	}
	d = discoveryFixture()
	for i := 0; i < MaxBackendServers; i++ {
		d.Servers = append(d.Servers, BackendServer{Backend: "pool", Name: strings.Repeat("s", 128), Address: strings.Repeat("a", 255), State: "ready", Weight: 100, InitialWeight: 100, Health: "UP"})
	}
	if d.Valid() {
		t.Fatal("unbounded discovery accepted")
	}
}
func TestRuntimeOperationValidationAndVerification(t *testing.T) {
	d := discoveryFixture()
	op := Operation{RequestID: NewID(), NodeID: NewID(), Kind: "drain", Backend: "application", Server: "app-a", ExpectedGeneration: d.Generation, ExpectedState: "ready", ExpectedWeight: 100}
	if !op.Valid() {
		t.Fatal("valid operation rejected")
	}
	for _, name := range []string{"app-a\nshutdown sessions server x/y", "app-a;quit", "app-a other", "../app-a"} {
		invalid := op
		invalid.Server = name
		if invalid.Valid() {
			t.Fatalf("invalid runtime token accepted: %q", name)
		}
	}
	d.Servers[0].State = "drain"
	d.Servers[0].ActiveConnections = 1
	if op.Verified(d) {
		t.Fatal("drain verified before active connections reached zero")
	}
	d.Servers[0].ActiveConnections = 0
	if !op.Verified(d) {
		t.Fatal("completed drain not verified")
	}
	d.Generation = strings.Repeat("b", 64)
	if op.Verified(d) {
		t.Fatal("new generation verified an old operation")
	}
}
