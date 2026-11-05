package domain

import (
	"strings"
	"testing"
	"time"
)

func TestApplicationHealthDoesNotConfuseDeploymentWithReachability(t *testing.T) {
	now := time.Now()
	a := HTTPApplication{ID: NewID(), Name: "Store", Listener: "public", Hostname: "apps.example.test", PathPrefix: "/shop", HealthPath: "/healthz", Algorithm: "roundrobin", Servers: []ConfigurationServer{{Name: "web", Address: "app", Port: 8080, Weight: 100}}}
	b := a
	b.ID = NewID()
	b.Name = "Admin"
	b.PathPrefix = "/admin"
	revisions := []ApplicationRevision{{ApplicationID: a.ID, Number: 1, Status: "succeeded", Spec: a}, {ApplicationID: b.ID, Number: 1, Status: "succeeded", Spec: b}}
	n := Node{ID: NewID(), Name: "node-a"}
	c := NodeControl{SessionID: NewID(), LastSeen: now, Discovery: &Discovery{ObservedAtMS: now.UnixMilli(), Configuration: &ConfigurationState{Version: 1, ArtifactID: strings.Repeat("a", 64), ObservedAtMS: now.UnixMilli(), Document: &ConfigurationDocument{Backends: []ConfigurationBackend{a.Backend()}, Applications: []HTTPApplication{b}}}}}
	c.Discovery.Configuration.Document.Backends = []ConfigurationBackend{{Name: "unmanaged", Algorithm: "roundrobin", Servers: a.Servers}}
	c.Discovery.Configuration.Document.Applications = []HTTPApplication{a, b}
	for _, app := range []HTTPApplication{a, b} {
		c.Discovery.Proxies = append(c.Discovery.Proxies, Proxy{Name: app.BackendName(), Kind: "backend", Mode: "http", Algorithm: app.Algorithm})
		c.Discovery.Servers = append(c.Discovery.Servers, BackendServer{Backend: app.BackendName(), Name: "web", InitialWeight: 100, Weight: 100, Health: "UP", State: "ready"})
	}
	controls := map[string]NodeControl{n.ID: c}
	get := func(id string) ApplicationHealth {
		for _, h := range ApplicationHealthAt(revisions, []Node{n}, controls, now) {
			if h.ApplicationID == id {
				return h
			}
		}
		t.Fatal("missing app")
		return ApplicationHealth{}
	}
	if h := get(a.ID); h.Configuration != "loaded" || h.Reachability != "unknown" {
		t.Fatal("configuration implied reachability", h)
	}
	for _, app := range []HTTPApplication{a, b} {
		c.Discovery.ApplicationProbes = append(c.Discovery.ApplicationProbes, ApplicationProbe{ApplicationID: app.ID, SpecDigest: app.Digest(), Name: app.Name, Scheme: "http", Path: app.PathPrefix, ExpectedStatus: 200, StatusCode: 200, ObservedAtMS: now.UnixMilli()})
	}
	controls[n.ID] = c
	if h := get(a.ID); h.Reachability != "reachable" {
		t.Fatal(h)
	}
	c.Discovery.ApplicationProbes[0].ErrorCode = "response_status"
	c.Discovery.ApplicationProbes[0].StatusCode = 503
	if get(a.ID).Reachability != "unhealthy" || get(b.ID).Reachability != "reachable" {
		t.Fatal("one app failure affected its neighbor")
	}
	c.Discovery.ApplicationProbes[0].ObservedAtMS = now.Add(-16 * time.Second).UnixMilli()
	if get(a.ID).Reachability != "unknown" {
		t.Fatal("stale failure remained current")
	}
	c.Discovery.ApplicationProbes[1].SpecDigest = strings.Repeat("b", 64)
	if get(b.ID).Reachability != "unknown" {
		t.Fatal("old revision probe credited new config")
	}
	c.LastSeen = now.Add(-10 * time.Second)
	controls[n.ID] = c
	if get(b.ID).Configuration != "unknown" || get(b.ID).Reachability != "unknown" {
		t.Fatal("disconnected node remained healthy")
	}
}
