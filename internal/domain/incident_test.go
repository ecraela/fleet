package domain

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func incidentFixture() (IncidentSnapshot, time.Time) {
	now := time.Now().UTC()
	a := HTTPApplication{ID: NewID(), Name: "Store", Listener: "public", Hostname: "apps.example.test", PathPrefix: "/shop", HealthPath: "/healthz", Algorithm: "roundrobin", Servers: []ConfigurationServer{{Name: "web", Address: "backend", Port: 8080, Weight: 100}}}
	b := a
	b.ID = NewID()
	b.Name = "Admin"
	b.PathPrefix = "/shop/admin"
	s := IncidentSnapshot{Cluster: Cluster{ID: NewID(), OrganizationID: NewID(), Name: "Production"}, Controls: map[string]NodeControl{}, Apps: []ApplicationRevision{{ApplicationID: a.ID, Number: 1, Status: "succeeded", Spec: a}, {ApplicationID: b.ID, Number: 1, Status: "succeeded", Spec: b}}}
	for _, name := range []string{"haproxy-a", "haproxy-b"} {
		n := Node{ID: NewID(), Name: name}
		s.Nodes = append(s.Nodes, n)
		d := &Discovery{ObservedAtMS: now.UnixMilli(), Generation: strings.Repeat("a", 64), Configuration: &ConfigurationState{Version: 1, ArtifactID: strings.Repeat("b", 64), ObservedAtMS: now.UnixMilli(), Document: &ConfigurationDocument{Backends: []ConfigurationBackend{{Name: "unmanaged", Algorithm: "roundrobin", Servers: a.Servers}}, Applications: []HTTPApplication{a, b}}}}
		for _, app := range []HTTPApplication{a, b} {
			for _, scope := range []string{"node", "entrypoint"} {
				for _, scheme := range []string{"http", "https"} {
					g := ApplicationProbeGrant{Name: strings.ToLower(app.Name) + "-" + scope + "-" + scheme, Scope: scope, Listener: app.Listener, Hostname: app.Hostname, PathPrefix: app.PathPrefix, Path: app.PathPrefix + "/items", Scheme: scheme, ExpectedStatus: 200, PolicyDigest: strings.Repeat("c", 64)}
					p := ApplicationProbe{Name: g.Name, Scope: scope, ApplicationID: app.ID, SpecDigest: app.Digest(), Scheme: scheme, Path: g.Path, ExpectedStatus: 200, StatusCode: 200, PolicyDigest: g.PolicyDigest, ArtifactID: d.Configuration.ArtifactID, Generation: d.Generation, ObservedAtMS: now.UnixMilli()}
					if scheme == "https" {
						p.TLSVerified = true
						p.CertificateSHA256 = strings.Repeat("d", 64)
						p.CertificateNotAfterMS = now.Add(24 * time.Hour).UnixMilli()
					}
					d.ApplicationProbeGrants = append(d.ApplicationProbeGrants, g)
					d.ApplicationProbes = append(d.ApplicationProbes, p)
				}
			}
		}
		s.Controls[n.ID] = NodeControl{SessionID: NewID(), LastSeen: now, Discovery: d}
	}
	return s, now
}
func observations(s IncidentSnapshot, now time.Time) []IncidentObservation {
	return IncidentObservations(s, IncidentTelemetry{Available: true, QueriedAtMS: now.UnixMilli()}, now)
}
func TestIncidentsAttributeRequestFailureAndRejectStaleOrForeignEvidence(t *testing.T) {
	s, now := incidentFixture()
	n := s.Nodes[0]
	d := s.Controls[n.ID].Discovery
	p := &d.ApplicationProbes[0]
	p.StatusCode = 503
	p.ErrorCode = "response_status"
	app := s.Apps[0].ApplicationID
	get := func() IncidentObservation {
		for _, o := range observations(s, now) {
			if o.Rule == "application_health" && o.ResourceID == app && o.Scope == "node" && o.Scheme == "http" {
				return o
			}
		}
		t.Fatal("missing app observation")
		return IncidentObservation{}
	}
	if o := get(); o.State != "failing" || !slices.Equal(o.NodeIDs, []string{n.ID}) {
		t.Fatal(o)
	}
	for _, o := range observations(s, now) {
		if o.Rule == "application_health" && o.ResourceID != app && o.State != "healthy" {
			t.Fatal("neighbor affected", o)
		}
	}
	saved := *p
	for _, mutate := range []func(*ApplicationProbe){
		func(p *ApplicationProbe) { p.ObservedAtMS = now.Add(-16 * time.Second).UnixMilli() },
		func(p *ApplicationProbe) { p.ArtifactID = strings.Repeat("e", 64) },
		func(p *ApplicationProbe) { p.Generation = strings.Repeat("e", 64) },
		func(p *ApplicationProbe) { p.SpecDigest = strings.Repeat("e", 64) },
		func(p *ApplicationProbe) { p.PolicyDigest = strings.Repeat("e", 64) },
		func(p *ApplicationProbe) { p.ApplicationID = NewID() },
		func(p *ApplicationProbe) { p.Path = "/another-path" },
		func(p *ApplicationProbe) { p.ObservedAtMS = now.Add(2 * time.Second).UnixMilli() },
	} {
		*p = saved
		mutate(p)
		if o := get(); o.State != "unknown" {
			t.Fatal("untrusted evidence used", o)
		}
	}
	*p = saved
	s.Apps[0].Spec.Disabled = true
	if o := get(); o.State != "retired" {
		t.Fatal("intentional disable alerted", o)
	}
}
func TestIncidentsTLSOnlyUsesLoadedReferencesAndHandlesShortLivedCertificates(t *testing.T) {
	s, now := incidentFixture()
	for _, n := range s.Nodes {
		cfg := s.Controls[n.ID].Discovery.Configuration
		cfg.NodeID = n.ID
		cfg.ListenerControlVersion = 1
		l := HTTPSListener{ListenerBinding: ListenerBinding{Name: "public", Address: "*", Port: 8443}, CertificateRef: "active", CertificateSHA256: strings.Repeat("d", 64)}
		cfg.ListenerBindings = []ListenerBinding{l.ListenerBinding}
		cfg.Document.Listeners = []HTTPSListener{l}
		cfg.TLSListeners = []TLSListenerObservation{{Name: "public", SHA256: l.CertificateSHA256, Loaded: true}}
		cfg.TLSCertificates = []TLSCertificate{{Reference: "active", SHA256: l.CertificateSHA256, DNSNames: []string{"apps.example.test"}, NotBeforeMS: now.Add(-time.Hour).UnixMilli(), NotAfterMS: now.Add(23 * time.Hour).UnixMilli()}, {Reference: "old-rollback", SHA256: strings.Repeat("f", 64), DNSNames: []string{"apps.example.test"}, NotBeforeMS: now.Add(-25 * time.Hour).UnixMilli(), NotAfterMS: now.Add(-time.Hour).UnixMilli()}}
	}
	get := func() IncidentObservation {
		for _, o := range observations(s, now) {
			if o.Rule == "tls_expiry" {
				return o
			}
		}
		t.Fatal("missing TLS")
		return IncidentObservation{}
	}
	if o := get(); o.State != "healthy" {
		t.Fatal("short-lived or unused certificate raised expiry", o)
	}
	cfg := s.Controls[s.Nodes[0].ID].Discovery.Configuration
	cfg.TLSCertificates[0].NotBeforeMS = now.Add(-23 * time.Hour).UnixMilli()
	cfg.TLSCertificates[0].NotAfterMS = now.Add(time.Hour).UnixMilli()
	if o := get(); o.State != "failing" || o.Severity != "critical" || len(o.NodeIDs) != 1 {
		t.Fatal(o)
	}
	// A healthy peer cannot clear absent evidence from another node.
	cfg.TLSCertificates[0].NotAfterMS = now.Add(23 * time.Hour).UnixMilli()
	cfg.TLSListeners = nil
	if o := get(); o.State != "unknown" {
		t.Fatal("missing loaded inventory recovered TLS", o)
	}
}
func TestIncidentsSeparateStorageOutageFromMissingMetricsAndKeepFailedDeployment(t *testing.T) {
	s, now := incidentFixture()
	rows := IncidentObservations(s, IncidentTelemetry{}, now)
	for _, o := range rows {
		if o.Rule == "metrics_missing" && o.State != "unknown" {
			t.Fatal(o)
		}
	}
	s.Rollouts = []Rollout{{ID: NewID(), Status: "rolled_back", CreatedAt: now.Add(-time.Minute), UpdatedAt: now}}
	for _, o := range observations(s, now) {
		if o.Rule == "deployment_failed" && (!o.RecordResolved || o.Code != "rollback_verified") {
			t.Fatal(o)
		}
	}
}

func TestIncidentsAgentPresenceDoesNotRequireRuntimeOrConfigurationAccess(t *testing.T) {
	s, now := incidentFixture()
	for _, n := range s.Nodes {
		c := s.Controls[n.ID]
		c.Discovery = &Discovery{ErrorCode: "runtime_not_configured", ObservedAtMS: now.UnixMilli()}
		s.Controls[n.ID] = c
	}
	for _, o := range IncidentObservations(s, IncidentTelemetry{}, now) {
		if o.Rule == "agent_offline" && o.State != "healthy" {
			t.Fatal("heartbeat needs runtime access", o)
		}
	}
	for _, n := range s.Nodes {
		c := s.Controls[n.ID]
		c.LastSeen = now.Add(-time.Minute)
		s.Controls[n.ID] = c
	}
	telemetry := IncidentTelemetry{Available: true, QueriedAtMS: now.UnixMilli(), Nodes: map[string]IncidentNodeTelemetry{}}
	for _, n := range s.Nodes {
		telemetry.Nodes[n.ID] = IncidentNodeTelemetry{ObservedAtMS: now.UnixMilli(), ScrapeOK: false}
	}
	for _, o := range IncidentObservations(s, telemetry, now) {
		if o.Rule == "agent_offline" && o.State != "healthy" {
			t.Fatal("fresh failed scrape did not establish agent presence", o)
		}
	}
}

func TestIncidentsFailedRenewalAndExplicitMaintenanceRecoveryAreSeparate(t *testing.T) {
	s, now := incidentFixture()
	s.ACME = &ACMEPlan{ID: NewID(), Enabled: true, Status: "error", Binding: ListenerBinding{Name: "public"}}
	for _, o := range observations(s, now) {
		if o.Rule == "tls_renewal" && (o.State != "failing" || o.Severity != "critical") {
			t.Fatal(o)
		}
	}
	id := NewID()
	s.Rollouts = []Rollout{{ID: NewID(), Status: "rollback_failed", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, Nodes: []RolloutNode{{Status: "rollback_failed", RecoveryMaintenanceID: id, RollbackMaintenance: &Maintenance{ID: id, Status: "completed"}}}}}
	for _, o := range observations(s, now) {
		if o.Rule == "deployment_failed" && (!o.RecordResolved || o.Code != "maintenance_recovered") {
			t.Fatal(o)
		}
	}
	s.Rollouts[0].Nodes[0].RollbackMaintenance.Status = "attention"
	for _, o := range observations(s, now) {
		if o.Rule == "deployment_failed" && o.State != "failing" {
			t.Fatal("unfinished maintenance recovered", o)
		}
	}
}
