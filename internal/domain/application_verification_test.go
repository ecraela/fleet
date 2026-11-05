package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func entrypointVerificationFixture() (ConfigurationDocument, Discovery) {
	doc, d, p := verificationFixture()
	doc.Applications[0].Verification.Entrypoint = true
	grant := d.ApplicationProbeGrants[0]
	grant.Scope, grant.Name = "entrypoint", "store-entrypoint-http"
	d.ApplicationProbeGrants = append(d.ApplicationProbeGrants, grant)
	p.SpecDigest = doc.Applications[0].Digest()
	d.ApplicationProbes = []ApplicationProbe{p}
	p.Scope, p.Name = grant.Scope, grant.Name
	d.ApplicationProbes = append(d.ApplicationProbes, p)
	return doc, d
}

func TestEntrypointVerificationRequiresBothLocallyGrantedPaths(t *testing.T) {
	doc, d := entrypointVerificationFixture()
	checks, ok := ApplicationChecks(doc, d)
	if !ok || len(checks) != 2 {
		t.Fatal("missing frozen path", checks)
	}
	for _, grants := range [][]ApplicationProbeGrant{d.ApplicationProbeGrants[:1], d.ApplicationProbeGrants[1:]} {
		incomplete := d
		incomplete.ApplicationProbeGrants = grants
		if _, ok := ApplicationChecks(doc, incomplete); ok {
			t.Fatal("one path satisfied both gates")
		}
	}
	doc.Applications[0].Verification.Entrypoint = false
	checks, ok = ApplicationChecks(doc, d)
	if !ok || len(checks) != 1 || checks[0].Scope != "node" {
		t.Fatal("existing policy silently gained entrypoint requirements")
	}
	raw, _ := json.Marshal(doc.Applications[0].Verification)
	if strings.Contains(string(raw), "entrypoint") {
		t.Fatal("opt-out changed existing document digests")
	}
}

func TestEntrypointVerificationStartsOnlyAfterRejoinAndRequiresDistinctFreshPasses(t *testing.T) {
	doc, d := entrypointVerificationFixture()
	checks, _ := ApplicationChecks(doc, d)
	now := time.UnixMilli(d.ObservedAtMS)
	before := NewApplicationVerification(checks, "before_rejoin", now.Add(-time.Second))
	d.ApplicationProbes[1].StatusCode, d.ApplicationProbes[1].ErrorCode = 503, "response_status"
	before.Advance(checks, &d, now)
	d.ObservedAtMS += 5000
	d.Configuration.ObservedAtMS += 5000
	for i := range d.ApplicationProbes {
		d.ApplicationProbes[i].ObservedAtMS += 5000
	}
	if before.Advance(checks, &d, now.Add(5*time.Second)) != "passed" || len(before.Checks) != 1 {
		t.Fatal("withdrawn node needed the shared path", before)
	}
	after := NewApplicationVerification(checks, "after_rejoin", now.Add(6*time.Second))
	if after.Advance(checks, &d, now.Add(6*time.Second)) != "waiting" || after.Checks[0].Passes != 0 || after.Checks[1].Passes != 0 {
		t.Fatal("pre-rejoin evidence crossed the phase barrier", after)
	}
	for _, offset := range []int64{10000, 10000, 15000} {
		at := now.Add(time.Duration(offset) * time.Millisecond)
		d.ObservedAtMS, d.Configuration.ObservedAtMS = at.UnixMilli(), at.UnixMilli()
		for i := range d.ApplicationProbes {
			d.ApplicationProbes[i].ObservedAtMS = at.UnixMilli()
			d.ApplicationProbes[i].StatusCode, d.ApplicationProbes[i].ErrorCode = 200, ""
		}
		status := after.Advance(checks, &d, at)
		if offset == 10000 && (status != "waiting" || after.Checks[1].Passes != 1) {
			t.Fatal("replayed entrypoint result earned a pass", after)
		}
		if offset == 15000 && status != "passed" {
			t.Fatal("both paths did not complete", after)
		}
	}
	if after.Advance(checks, nil, now.Add(time.Minute)) != "failed" {
		t.Fatal("missing current evidence retained green completion")
	}
}

func TestEntrypointVerificationRejectsFailuresAndUncorrelatedEvidence(t *testing.T) {
	for _, fault := range []string{"http", "https", "missing", "policy", "artifact", "generation", "spec", "scope"} {
		t.Run(fault, func(t *testing.T) {
			doc, d := entrypointVerificationFixture()
			if fault == "https" {
				doc.Applications[0].Verification.HTTP = false
				doc.Applications[0].Verification.HTTPS = true
				for i := range d.ApplicationProbeGrants {
					d.ApplicationProbeGrants[i].Scheme = "https"
					d.ApplicationProbes[i].Scheme, d.ApplicationProbes[i].SpecDigest = "https", doc.Applications[0].Digest()
					d.ApplicationProbes[i].TLSVerified = true
					d.ApplicationProbes[i].CertificateNotAfterMS = d.ObservedAtMS + 60000
				}
			}
			checks, _ := ApplicationChecks(doc, d)
			p := &d.ApplicationProbes[1]
			switch fault {
			case "http":
				p.ErrorCode, p.StatusCode = "response_mismatch", 200
			case "https":
				p.ErrorCode, p.TLSVerified = "tls_failed", false
			case "policy":
				d.ApplicationProbeGrants[1].PolicyDigest = strings.Repeat("f", 64)
			case "artifact":
				p.ArtifactID = strings.Repeat("f", 64)
			case "generation":
				p.Generation = strings.Repeat("f", 64)
			case "spec":
				p.SpecDigest = strings.Repeat("f", 64)
			case "scope":
				p.Scope = "node"
			case "missing":
				d.ApplicationProbes = d.ApplicationProbes[:1]
			}
			now := time.UnixMilli(d.ObservedAtMS)
			v := NewApplicationVerification(checks, "after_rejoin", now.Add(-time.Second))
			status := v.Advance(checks, &d, now)
			if fault == "http" || fault == "https" || fault == "policy" {
				if status != "failed" || !strings.HasPrefix(v.ErrorCode, "application_entrypoint_") || v.Checks[0].ErrorCode != "" {
					t.Fatal("entrypoint failure was not distinguished from node failure", v)
				}
			} else if status != "waiting" || v.Checks[1].Passes != 0 {
				t.Fatal("uncorrelated entrypoint evidence passed", v)
			}
		})
	}
}

func verificationFixture() (ConfigurationDocument, Discovery, ApplicationProbe) {
	now := time.Unix(1800000000, 0)
	a := HTTPApplication{ID: NewID(), Name: "Store", Listener: "public", Hostname: "apps.example.test", PathPrefix: "/shop", HealthPath: "/healthz", Algorithm: "roundrobin", Servers: []ConfigurationServer{{Name: "web", Address: "backend", Port: 8080, Weight: 100}}, Verification: &ApplicationVerificationPolicy{HTTP: true}}
	doc := ConfigurationDocument{Backends: []ConfigurationBackend{{Name: "unmanaged", Algorithm: "roundrobin", Servers: a.Servers}}, Applications: []HTTPApplication{a}}
	g := ApplicationProbeGrant{Name: "store-http", Scope: "node", Listener: a.Listener, Hostname: a.Hostname, PathPrefix: a.PathPrefix, Path: "/shop/items", Scheme: "http", ExpectedStatus: 200, PolicyDigest: strings.Repeat("c", 64)}
	d := Discovery{Generation: strings.Repeat("a", 64), ObservedAtMS: now.UnixMilli(), Configuration: &ConfigurationState{Version: 1, ArtifactID: strings.Repeat("b", 64), ObservedAtMS: now.UnixMilli(), Document: &doc}, ApplicationProbeGrants: []ApplicationProbeGrant{g}}
	p := ApplicationProbe{ApplicationID: a.ID, SpecDigest: a.Digest(), Name: g.Name, Scope: "node", Scheme: g.Scheme, Path: g.Path, ExpectedStatus: 200, StatusCode: 200, ObservedAtMS: now.UnixMilli(), PolicyDigest: g.PolicyDigest, ArtifactID: d.Configuration.ArtifactID, Generation: d.Generation}
	d.ApplicationProbes = []ApplicationProbe{p}
	return doc, d, p
}

func TestApplicationVerificationRequiresLocalNodeAuthority(t *testing.T) {
	doc, d, _ := verificationFixture()
	checks, ok := ApplicationChecks(doc, d)
	if !ok || len(checks) != 1 {
		t.Fatal("missing direct check", checks)
	}
	d.ApplicationProbeGrants[0].Scope = "entrypoint"
	if _, ok = ApplicationChecks(doc, d); ok {
		t.Fatal("healthy peer could stand in for the target")
	}
	d.ApplicationProbeGrants = nil
	if _, ok = ApplicationChecks(doc, d); ok {
		t.Fatal("missing local authority accepted")
	}
	doc.Applications[0].Disabled = true
	if checks, ok = ApplicationChecks(doc, d); !ok || len(checks) != 0 {
		t.Fatal("disabled route required request success")
	}
}

func TestApplicationVerificationCorrelatesPhaseArtifactAndDistinctReports(t *testing.T) {
	doc, d, p := verificationFixture()
	checks, _ := ApplicationChecks(doc, d)
	start := time.UnixMilli(p.ObservedAtMS).Add(-time.Second)
	for _, alter := range []func(*ApplicationProbe){
		func(p *ApplicationProbe) { p.SpecDigest = strings.Repeat("d", 64) },
		func(p *ApplicationProbe) { p.ArtifactID = strings.Repeat("d", 64) },
		func(p *ApplicationProbe) { p.Generation = strings.Repeat("d", 64) },
		func(p *ApplicationProbe) { p.Scope = "entrypoint" },
		func(p *ApplicationProbe) { p.PolicyDigest = strings.Repeat("d", 64) },
		func(p *ApplicationProbe) { p.ObservedAtMS = start.UnixMilli() },
	} {
		v := NewApplicationVerification(checks, "before_rejoin", start)
		bad := p
		alter(&bad)
		d.ApplicationProbes = []ApplicationProbe{bad}
		if v.Advance(checks, &d, time.UnixMilli(p.ObservedAtMS)) != "waiting" || v.Checks[0].Passes != 0 {
			t.Fatal("unrelated evidence accepted", v)
		}
	}
	d.ApplicationProbes = []ApplicationProbe{p}
	v := NewApplicationVerification(checks, "before_rejoin", start)
	now := time.UnixMilli(p.ObservedAtMS)
	for range 3 {
		if v.Advance(checks, &d, now) != "waiting" || v.Checks[0].Passes != 1 {
			t.Fatal("report replay earned another pass", v)
		}
	}
	d.ApplicationProbes[0].ObservedAtMS += 5000
	d.ObservedAtMS += 5000
	d.Configuration.ObservedAtMS += 5000
	if v.Advance(checks, &d, now.Add(5*time.Second)) != "passed" {
		t.Fatal(v)
	}
	after := NewApplicationVerification(checks, "after_rejoin", now.Add(6*time.Second))
	if after.Advance(checks, &d, now.Add(6*time.Second)) != "waiting" || after.Checks[0].Passes != 0 {
		t.Fatal("pre-rejoin success crossed phase barrier", after)
	}
	if after.Advance(checks, nil, now.Add(36*time.Second)) != "failed" || after.ErrorCode != "application_verification_timeout" {
		t.Fatal("missing observations did not time out", after)
	}
}

func TestApplicationVerificationRejectsHTTPAndTLSFailures(t *testing.T) {
	for _, fault := range []string{"response_status", "response_mismatch", "tls_failed", "expired"} {
		t.Run(fault, func(t *testing.T) {
			doc, d, p := verificationFixture()
			if fault == "tls_failed" || fault == "expired" {
				doc.Applications[0].Verification = &ApplicationVerificationPolicy{HTTPS: true}
				d.ApplicationProbeGrants[0].Scheme = "https"
				p.Scheme = "https"
				p.SpecDigest = doc.Applications[0].Digest()
				p.CertificateNotAfterMS = p.ObservedAtMS - 1
				p.CertificateSHA256 = strings.Repeat("e", 64)
				p.TLSVerified = true
			}
			if fault != "expired" {
				p.ErrorCode = fault
			}
			d.ApplicationProbes = []ApplicationProbe{p}
			checks, _ := ApplicationChecks(doc, d)
			v := NewApplicationVerification(checks, "before_rejoin", time.UnixMilli(p.ObservedAtMS-1000))
			if v.Advance(checks, &d, time.UnixMilli(p.ObservedAtMS)) != "failed" {
				t.Fatal(v)
			}
			if v.ErrorCode != "application_"+p.Scheme+"_failed" {
				t.Fatal(v)
			}
		})
	}
}

func TestApplicationVerificationPolicyIsFrozen(t *testing.T) {
	doc, d, p := verificationFixture()
	checks, _ := ApplicationChecks(doc, d)
	v := NewApplicationVerification(checks, "before_rejoin", time.UnixMilli(p.ObservedAtMS-1000))
	d.ApplicationProbeGrants[0].PolicyDigest = strings.Repeat("f", 64)
	if v.Advance(checks, &d, time.UnixMilli(p.ObservedAtMS)) != "failed" || v.ErrorCode != "application_probe_policy_changed" {
		t.Fatal(v)
	}
	doc.Applications[0].Verification = &ApplicationVerificationPolicy{}
	if doc.Valid() {
		t.Fatal("empty verification policy accepted")
	}
}

func TestApplicationVerificationRechecksCompletedNodeWithFreshEvidence(t *testing.T) {
	doc, d, p := verificationFixture()
	checks, _ := ApplicationChecks(doc, d)
	v := NewApplicationVerification(checks, "after_rejoin", time.UnixMilli(p.ObservedAtMS-1000))
	for _, offset := range []int64{0, 5000, 45000} {
		now := time.UnixMilli(p.ObservedAtMS + offset)
		d.ObservedAtMS, d.Configuration.ObservedAtMS, d.ApplicationProbes[0].ObservedAtMS = now.UnixMilli(), now.UnixMilli(), now.UnixMilli()
		status := v.Advance(checks, &d, now)
		if offset > 0 && status != "passed" {
			t.Fatal("completed node lost progress while peer rolled", v)
		}
	}
	if v.Advance(checks, nil, time.UnixMilli(p.ObservedAtMS+61000)) != "failed" {
		t.Fatal("completed gate retained stale green evidence")
	}
}
