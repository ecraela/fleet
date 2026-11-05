package domain

import (
	"slices"
	"sort"
	"time"
)

// Incidents describe operational evidence, not DNS changes or approval requests.
// Group keys contain only manager-owned identities and fixed rule identifiers.
type Incident struct {
	ID             string   `json:"id"`
	OrganizationID string   `json:"organization_id"`
	ClusterID      string   `json:"cluster_id"`
	Rule           string   `json:"rule"`
	ResourceID     string   `json:"resource_id"`
	ResourceName   string   `json:"resource_name"`
	Scope          string   `json:"scope,omitempty"`
	Scheme         string   `json:"scheme,omitempty"`
	Status         string   `json:"status"`
	Severity       string   `json:"severity"`
	Evidence       string   `json:"evidence"`
	Code           string   `json:"code"`
	NodeIDs        []string `json:"node_ids"`
	OpenedAtMS     int64    `json:"opened_at_ms"`
	UpdatedAtMS    int64    `json:"updated_at_ms"`
	ResolvedAtMS   int64    `json:"resolved_at_ms,omitempty"`
	ObservedAtMS   int64    `json:"observed_at_ms"`
	Occurrences    int      `json:"occurrences"`
}

type IncidentEvent struct {
	Sequence int64    `json:"sequence"`
	Kind     string   `json:"kind"`
	AtMS     int64    `json:"at_ms"`
	Incident Incident `json:"incident"`
}

type IncidentTelemetry struct {
	Available   bool
	QueriedAtMS int64
	Nodes       map[string]IncidentNodeTelemetry
}
type IncidentNodeTelemetry struct {
	ObservedAtMS int64
	ScrapeOK     bool
	ScrapeKnown  bool
}

// Unknown evidence holds an open incident and resets both debounce timers. It
// cannot declare recovery or count as another successful check.
type IncidentObservation struct {
	Key, Rule, ResourceID, ResourceName, Scope, Scheme string
	State, Severity, Code                              string
	NodeIDs                                            []string
	ObservedAtMS                                       int64
	OpenAfter                                          time.Duration
	RecordResolved                                     bool
}

func (o IncidentObservation) GroupKey() string {
	return o.Rule + "/" + o.ResourceID + "/" + o.Scope + "/" + o.Scheme
}

type IncidentSnapshot struct {
	Cluster  Cluster
	Nodes    []Node
	Controls map[string]NodeControl
	Apps     []ApplicationRevision
	Rollouts []Rollout
	ACME     *ACMEPlan
}

func incidentControlFresh(c NodeControl, now time.Time) bool {
	return c.SessionID != "" && now.Sub(c.LastSeen) >= -time.Second && now.Sub(c.LastSeen) <= 6*time.Second && c.Discovery != nil && c.Discovery.Fresh(now)
}

func (s IncidentSnapshot) IncidentRetired(i Incident, now time.Time) bool {
	if i.Rule == "agent_offline" || i.Rule == "metrics_missing" {
		return !slices.ContainsFunc(s.Nodes, func(n Node) bool { return n.ID == i.ResourceID })
	}
	if i.Rule == "telemetry_unavailable" {
		return len(s.Nodes) == 0
	}
	if i.Rule == "tls_expiry" && len(s.Nodes) > 0 {
		for _, n := range s.Nodes {
			c := s.Controls[n.ID]
			if !incidentControlFresh(c, now) || c.Discovery.Configuration == nil || !c.Discovery.Configuration.Fresh(now) {
				return false
			}
			for _, l := range c.Discovery.Configuration.Document.Listeners {
				if l.Name == i.Scope {
					return false
				}
			}
		}
		return true
	}
	return false
}

// IncidentObservations uses fixed local probe grants. It does not fetch URLs,
// infer public reachability or turn aggregate traffic counters into app health.
func IncidentObservations(s IncidentSnapshot, telemetry IncidentTelemetry, now time.Time) []IncidentObservation {
	out := []IncidentObservation{}
	add := func(o IncidentObservation) {
		o.Key = o.GroupKey()
		o.NodeIDs = slices.Compact(slices.Sorted(slices.Values(o.NodeIDs)))
		if o.NodeIDs == nil {
			o.NodeIDs = []string{}
		}
		if o.Severity == "" {
			o.Severity = "warning"
		}
		out = append(out, o)
	}
	telemetryKnown := telemetry.Available && now.UnixMilli()-telemetry.QueriedAtMS >= -1000 && now.UnixMilli()-telemetry.QueriedAtMS <= 5000
	if len(s.Nodes) > 0 {
		state := "failing"
		if telemetryKnown {
			state = "healthy"
		}
		add(IncidentObservation{Rule: "telemetry_unavailable", ResourceID: s.Cluster.ID, ResourceName: s.Cluster.Name, State: state, Code: "telemetry_unavailable", OpenAfter: 30 * time.Second, ObservedAtMS: telemetry.QueriedAtMS})
	}
	for _, n := range s.Nodes {
		c := s.Controls[n.ID]
		state := "unknown"
		observedAgent := max(int64(0), c.LastSeen.UnixMilli())
		heartbeatFresh := c.SessionID != "" && now.Sub(c.LastSeen) >= -time.Second && now.Sub(c.LastSeen) <= 6*time.Second
		if telemetryKnown {
			state = "failing"
			if m, ok := telemetry.Nodes[n.ID]; ok && now.UnixMilli()-m.ObservedAtMS >= -1000 && now.UnixMilli()-m.ObservedAtMS <= 15000 {
				state = "healthy"
				observedAgent = max(observedAgent, m.ObservedAtMS)
			}
		}
		if heartbeatFresh {
			state = "healthy"
		}
		add(IncidentObservation{Rule: "agent_offline", ResourceID: n.ID, ResourceName: n.Name, State: state, Code: "agent_offline", NodeIDs: []string{n.ID}, OpenAfter: 30 * time.Second, ObservedAtMS: observedAgent})
		metricState, code := "unknown", "metrics_missing"
		observed := int64(0)
		if telemetryKnown {
			metricState = "failing"
			if m, ok := telemetry.Nodes[n.ID]; ok {
				observed = m.ObservedAtMS
				age := now.UnixMilli() - observed
				if age >= -1000 && age <= 15000 {
					if m.ScrapeKnown && m.ScrapeOK {
						metricState = "healthy"
					} else if m.ScrapeKnown {
						code = "haproxy_scrape_failed"
					}
				}
			}
		}
		add(IncidentObservation{Rule: "metrics_missing", ResourceID: n.ID, ResourceName: n.Name, State: metricState, Code: code, NodeIDs: []string{n.ID}, OpenAfter: 30 * time.Second, ObservedAtMS: observed})
	}
	latest := map[string]ApplicationRevision{}
	for _, r := range s.Apps {
		if r.Number > latest[r.ApplicationID].Number {
			latest[r.ApplicationID] = r
		}
	}
	for _, r := range latest {
		effective := EffectiveApplication(r, s.Apps)
		a := effective.Spec
		for _, scope := range []string{"node", "entrypoint"} {
			for _, scheme := range []string{"http", "https"} {
				o := IncidentObservation{Rule: "application_health", ResourceID: a.ID, ResourceName: a.Name, Scope: scope, Scheme: scheme, State: "healthy", Code: "request_failed", OpenAfter: 10 * time.Second, ObservedAtMS: now.UnixMilli()}
				if a.Disabled || effective.Deleted {
					o.State = "retired"
					o.Code = "application_inactive"
					add(o)
					continue
				}
				missing, failed, monitored := len(s.Nodes) == 0, false, false
				for _, n := range s.Nodes {
					c := s.Controls[n.ID]
					if !incidentControlFresh(c, now) || c.Discovery.Configuration == nil || !c.Discovery.Configuration.Fresh(now) {
						missing = true
						continue
					}
					d := c.Discovery
					loaded := false
					for _, actual := range d.Configuration.Document.Applications {
						loaded = loaded || actual.Equal(a)
					}
					if !loaded {
						missing = true
						continue
					}
					found := false
					for _, g := range d.ApplicationProbeGrants {
						if !g.Matches(a) || g.Scope != scope || g.Scheme != scheme {
							continue
						}
						monitored = true
						var evidence *ApplicationProbe
						for i := range d.ApplicationProbes {
							p := &d.ApplicationProbes[i]
							if p.Name == g.Name && p.Scope == scope && p.PolicyDigest == g.PolicyDigest && p.ApplicationID == a.ID && p.SpecDigest == a.Digest() && p.ArtifactID == d.Configuration.ArtifactID && p.Generation == d.Generation && p.Scheme == scheme && p.Path == g.Path && p.ExpectedStatus == g.ExpectedStatus && p.Valid() && p.Fresh(now) {
								evidence = p
							}
						}
						if evidence == nil {
							missing = true
							continue
						}
						found = true
						o.ObservedAtMS = min(o.ObservedAtMS, evidence.ObservedAtMS)
						if evidence.ErrorCode != "" || evidence.StatusCode != g.ExpectedStatus || scheme == "https" && (!evidence.TLSVerified || evidence.CertificateNotAfterMS <= now.UnixMilli()) {
							failed = true
							o.NodeIDs = append(o.NodeIDs, n.ID)
						}
					}
					if !found {
						missing = true
					}
				}
				if !monitored || missing {
					o.State = "unknown"
					o.Code = "checks_unavailable"
				}
				if failed {
					o.State = "failing"
					o.Code = "request_failed"
					o.Severity = "critical"
				}
				add(o)
			}
		}
	}
	// Only certificates referenced by currently loaded shared listeners matter;
	// old inventory entries retained for rollback must not create expiry alarms.
	listeners := map[string]IncidentObservation{}
	listenerNodes := map[string]int{}
	for _, n := range s.Nodes {
		c := s.Controls[n.ID]
		if !incidentControlFresh(c, now) || c.Discovery.Configuration == nil || !c.Discovery.Configuration.Fresh(now) {
			continue
		}
		cfg := c.Discovery.Configuration
		for _, l := range cfg.Document.Listeners {
			listenerNodes[l.Name]++
			o, ok := listeners[l.Name]
			if !ok {
				o = IncidentObservation{Rule: "tls_expiry", ResourceID: s.Cluster.ID, ResourceName: l.Name, Scope: l.Name, State: "healthy", Code: "certificate_expiring", OpenAfter: 10 * time.Second, ObservedAtMS: cfg.ObservedAtMS}
			}
			found := false
			for _, cert := range cfg.TLSCertificates {
				if cert.Reference != l.CertificateRef || cert.SHA256 != l.ExpectedCertificate(n.ID) {
					continue
				}
				loaded := slices.ContainsFunc(cfg.TLSListeners, func(v TLSListenerObservation) bool { return v.Name == l.Name && v.Loaded && v.SHA256 == cert.SHA256 })
				if !loaded || cert.ErrorCode != "" {
					continue
				}
				found = true
				lifetime := time.Duration(cert.NotAfterMS-cert.NotBeforeMS) * time.Millisecond
				left := time.Duration(cert.NotAfterMS-now.UnixMilli()) * time.Millisecond
				if lifetime <= 0 || left <= min(7*24*time.Hour, lifetime/3) {
					o.State = "failing"
					o.NodeIDs = append(o.NodeIDs, n.ID)
					if left <= min(time.Hour, lifetime/10) {
						o.Severity = "critical"
					}
					if left <= 0 {
						o.Code = "certificate_expired"
					}
				}
			}
			if !found && o.State != "failing" {
				o.State = "unknown"
			}
			listeners[l.Name] = o
		}
	}
	for _, o := range listeners {
		if o.State == "healthy" {
			if listenerNodes[o.Scope] != len(s.Nodes) {
				o.State = "unknown"
			}
			for _, n := range s.Nodes {
				if !incidentControlFresh(s.Controls[n.ID], now) {
					o.State = "unknown"
				}
			}
		}
		add(o)
	}
	if s.ACME != nil {
		p := s.ACME
		o := IncidentObservation{Rule: "tls_renewal", ResourceID: p.ID, ResourceName: p.Binding.Name, State: "healthy", Code: "renewal_failed", OpenAfter: 10 * time.Second, ObservedAtMS: now.UnixMilli()}
		if !p.Enabled {
			o.State = "retired"
			o.Code = "automatic_tls_disabled"
		} else if p.Status == "error" {
			o.State = "failing"
			o.Severity = "critical"
		} else if p.Status != "active" {
			o.State = "unknown"
		}
		add(o)
	}
	// Persist failed attempts even if compensation finished between evaluations.
	// A successful rollback is a recovered deployment incident, not a success
	// deployment. Failed recovery stays open until a newer successful update.
	var success time.Time
	for _, r := range s.Rollouts {
		if r.Status == "succeeded" && r.CreatedAt.After(success) {
			success = r.CreatedAt
		}
	}
	for _, r := range s.Rollouts {
		if r.Status != "rolling_back" && r.Status != "rolled_back" && r.Status != "rollback_failed" {
			continue
		}
		if r.Status == "rolled_back" && now.Sub(r.UpdatedAt) > 30*24*time.Hour {
			continue
		}
		o := IncidentObservation{Rule: "deployment_failed", ResourceID: r.ID, ResourceName: "Cluster configuration", State: "failing", Code: "rollout_failed", Severity: "warning", ObservedAtMS: r.UpdatedAt.UnixMilli()}
		for _, n := range r.Nodes {
			if n.ErrorCode != "" || n.Status == "rollback_failed" {
				o.NodeIDs = append(o.NodeIDs, n.NodeID)
			}
		}
		if r.Status == "rollback_failed" {
			o.Code = "rollback_failed"
			o.Severity = "critical"
			failed, recovered := 0, true
			for _, n := range r.Nodes {
				if n.Status == "rollback_failed" || n.RecoveryMaintenanceID != "" {
					failed++
					recovered = recovered && n.RecoveryMaintenanceID != "" && ((n.Maintenance != nil && n.Maintenance.ID == n.RecoveryMaintenanceID && n.Maintenance.Status == "completed") || (n.RollbackMaintenance != nil && n.RollbackMaintenance.ID == n.RecoveryMaintenanceID && n.RollbackMaintenance.Status == "completed"))
				}
			}
			if failed > 0 && recovered {
				o.State = "healthy"
				o.RecordResolved = true
				o.Code = "maintenance_recovered"
				o.Severity = "warning"
			}
		}
		if r.Status == "rolled_back" || success.After(r.CreatedAt) {
			o.State = "healthy"
			o.RecordResolved = true
			if r.Status == "rolled_back" {
				o.Code = "rollback_verified"
			} else {
				o.Code = "superseded_by_success"
			}
		}
		add(o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
