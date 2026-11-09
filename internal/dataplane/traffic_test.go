package dataplane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/domain"
)

func testTraffic() *TrafficPolicy {
	return &TrafficPolicy{Source: accesslog.GeoPolicy{Mode: "peer"}, Capabilities: domain.TrafficCapabilities{Profile: strings.Repeat("a", 64), ClientSource: "peer", CountryReady: true, ASNReady: true}, CountryMap: "/maps/country.map", ASNMap: "/maps/asn.map"}
}
func TestApplicationTrafficIsOwnedAndCannotCrossBackend(t *testing.T) {
	a, b := testApplication("/shop"), testApplication("/shop/admin")
	a.RequestTelemetry = true
	a.TrafficPolicy = &domain.TrafficPolicy{Version: 1, Mode: "enforce", UnknownAction: "allow", Rules: []domain.TrafficRule{{ID: domain.NewID(), Name: "Country", Action: "block", Countries: []string{"RU"}}}}
	c := &Client{Traffic: testTraffic()}
	owned, err := c.applicationBackend(a)
	if err != nil {
		t.Fatal(err)
	}
	neighbor := applicationBackend(b)
	if len(decode[[]object](neighbor["http_request_rule_list"])) != 0 {
		t.Fatal("neighbor inherited policy")
	}
	if _, _, err = c.projectApplications([]object{owned, neighbor}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = projectApplications([]object{owned}); err == nil {
		t.Fatal("policy adopted without host grant")
	}
	rules := decode[[]object](owned["http_request_rule_list"])
	if decode[string](rules[0]["var_name"]) != "cv_spec" || decode[string](rules[len(rules)-1]["type"]) != "deny" {
		t.Fatal("attribution or enforcement order lost")
	}
	set(rules[len(rules)-1], "deny_status", 200)
	set(owned, "http_request_rule_list", rules)
	if _, _, err = c.projectApplications([]object{owned}); err == nil {
		t.Fatal("modified action adopted")
	}
	a.TrafficPolicy.Mode = "observe"
	observed, _ := c.applicationBackend(a)
	for _, r := range decode[[]object](observed["http_request_rule_list"]) {
		if decode[string](r["type"]) == "deny" {
			t.Fatal("observe mode denied traffic")
		}
	}
}
func TestTrafficAssetsAreLocalImmutableAndShareTheClientPolicy(t *testing.T) {
	dir := t.TempDir()
	maps := filepath.Join(dir, "maps")
	if err := os.Mkdir(maps, 0755); err != nil {
		t.Fatal(err)
	}
	country, _ := filepath.Abs("../accesslog/testdata/GeoIP2-Country-Test.mmdb")
	asn, _ := filepath.Abs("../accesslog/testdata/GeoLite2-ASN-Test.mmdb")
	ipPath := filepath.Join(dir, "client.json")
	grant := filepath.Join(dir, "traffic.json")
	write := func(path string, v any) {
		raw, _ := json.Marshal(v)
		if os.WriteFile(path, raw, 0600) != nil {
			t.Fatal("write")
		}
	}
	write(ipPath, accesslog.GeoPolicy{Version: 1, Mode: "peer", CountryDatabase: country, ASNDatabase: asn})
	write(grant, trafficGrant{Version: 1, ClientIPPolicy: ipPath, MapsDirectory: maps, LogSocket: "/run/haproxy/access.sock"})
	p, err := LoadTrafficPolicy(grant, ipPath)
	if err != nil || !p.Capabilities.Valid() || !p.Capabilities.CountryReady || !p.Capabilities.ASNReady {
		t.Fatal(p, err)
	}
	if _, err = LoadTrafficPolicy(grant, filepath.Join(dir, "other.json")); err == nil {
		t.Fatal("divergent client trust accepted")
	}
	before, err := os.ReadFile(p.CountryMap)
	if err != nil || !strings.Contains(string(before), " GB\n") {
		t.Fatal("missing real map data", err)
	}
	if _, err = LoadTrafficPolicy(grant, ipPath); err != nil {
		t.Fatal("idempotent asset stage", err)
	}
	policy := &domain.TrafficPolicy{Version: 1, Mode: "enforce", UnknownAction: "block", Rules: []domain.TrafficRule{{ID: domain.NewID(), Name: "Country", Action: "block", Countries: []string{"GB"}}}}
	rules, err := p.rules(policy)
	if err != nil {
		t.Fatal(err)
	}
	mapFor := func(name string) string {
		for _, rule := range rules {
			if decode[string](rule["var_name"]) == name {
				expression := decode[string](rule["var_expr"])
				if strings.Contains(expression, "map_ip(") {
					return strings.TrimSuffix(strings.SplitAfter(expression, "map_ip(")[1], ")")
				}
			}
		}
		return ""
	}
	countryMap := mapFor("cv_country")
	if mapFor("cv_asn") != "" {
		t.Fatal("country-only policy loaded ASN map")
	}
	if countryMap == "" || countryMap == p.CountryMap {
		t.Fatal("country policy did not select a compact map", countryMap)
	}
	legacy := p.legacyRules(policy)
	if dependencyProfile(legacy) == dependencyProfile(rules) || !p.matchesRuleProfile(policy, dependencyProfile(legacy)) {
		t.Fatal("historical full-map rules cannot be validated after upgrade")
	}
	a := testApplication("/shop")
	a.RequestTelemetry = true
	a.TrafficPolicy = policy
	c := &Client{Traffic: p}
	oldBackend, err := c.applicationBackendWithTraffic(a, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, apps, err := c.projectApplications([]object{oldBackend}); err != nil || len(apps) != 1 {
		t.Fatal("historical full-map backend cannot be observed after upgrade", err)
	}
	c.StateDir = filepath.Join(dir, "state")
	id := strings.Repeat("b", 64)
	doc := domain.ConfigurationDocument{Backends: []domain.ConfigurationBackend{{
		Name: "baseline", Algorithm: "roundrobin", Servers: []domain.ConfigurationServer{{
			Name: "origin", Address: "backend.internal", Port: 8080, Weight: 100,
		}},
	}}, Applications: []domain.HTTPApplication{a}}
	if !doc.Valid() {
		t.Fatal("invalid historical binding fixture")
	}
	bindings := filepath.Join(c.StateDir, "configuration-bindings")
	if err := os.MkdirAll(bindings, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bindings, id+".json")
	historical := artifactBinding{Version: 1, ArtifactID: id, Document: doc.Canonical(),
		CertificatePaths: map[string]string{}, LogProfiles: map[string]string{},
		TrafficRules: map[string]string{a.ID: dependencyProfile(legacy)}}
	storeBinding := func(b artifactBinding) {
		raw, _ := json.Marshal(b)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	storeBinding(historical)
	if err := c.retainBinding(id, doc, nil); err != nil {
		t.Fatal("historical full-map binding rejected after upgrade", err)
	}
	loaded, err := c.loadBinding(id)
	if err != nil || loaded.TrafficRules[a.ID] != dependencyProfile(legacy) {
		t.Fatal("historical binding was replaced", err)
	}
	historical.TrafficRules[a.ID] = strings.Repeat("f", 64)
	storeBinding(historical)
	if err := c.retainBinding(id, doc, nil); err == nil {
		t.Fatal("unrecognized historical traffic profile accepted")
	}
	selected, err := os.ReadFile(countryMap)
	if err != nil || !strings.Contains(string(selected), " GB\n") || !strings.Contains(string(selected), " __other__\n") {
		t.Fatal("selected and other known countries missing", err)
	}
	if len(selected) >= len(before) {
		t.Fatal("country map was not reduced")
	}
	second, err := p.rules(policy)
	if err != nil || dependencyProfile(rules) != dependencyProfile(second) {
		t.Fatal("selected map was not stable", err)
	}
	policy.Rules[0].Countries = nil
	policy.Rules[0].CIDRs = []string{"198.51.100.0/24"}
	rules, err = p.rules(policy)
	if err != nil {
		t.Fatal(err)
	}
	if mapFor("cv_country") != "" || mapFor("cv_asn") != "" {
		t.Fatal("IP-only policy loaded a Geo map")
	}
	fullASN, err := os.ReadFile(p.ASNMap)
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Fields(strings.SplitN(string(fullASN), "\n", 2)[0])
	n, err := strconv.ParseUint(first[1], 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	policy.Rules[0].CIDRs = nil
	policy.Rules[0].ASNs = []uint32{uint32(n)}
	rules, err = p.rules(policy)
	if err != nil {
		t.Fatal(err)
	}
	if mapFor("cv_country") != "" {
		t.Fatal("ASN-only policy loaded country map")
	}
	asnMap := mapFor("cv_asn")
	if asnMap == "" || asnMap == p.ASNMap {
		t.Fatal("ASN policy did not select a compact map", asnMap)
	}
	selectedASN, err := os.ReadFile(asnMap)
	if err != nil || !strings.Contains(string(selectedASN), " "+strconv.FormatUint(n, 10)+"\n") || !strings.Contains(string(selectedASN), " 0\n") {
		t.Fatal("selected and other known ASNs missing", err)
	}
	if err := os.WriteFile(countryMap, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	policy.Rules[0].ASNs = nil
	policy.Rules[0].Countries = []string{"GB"}
	if _, err := p.rules(policy); err == nil {
		t.Fatal("tampered selected map reused")
	}
	if _, apps, err := c.projectApplications([]object{oldBackend}); err != nil || len(apps) != 1 || !p.matchesRuleProfile(policy, dependencyProfile(legacy)) {
		t.Fatal("historical policy became unreadable when selected map was unavailable", err)
	}
	if os.WriteFile(p.CountryMap, []byte("changed"), 0644) != nil {
		t.Fatal("tamper")
	}
	if _, err = LoadTrafficPolicy(grant, ipPath); err == nil {
		t.Fatal("tampered immutable map reused")
	}
}

func TestTrafficMapCompactionPreservesUnknownAndCoalescesOther(t *testing.T) {
	source := []byte("10.0.0.0/8 RO\n11.0.0.0/8 DE\n12.0.0.0/8 FI\n13.0.0.0/8 DE\n2001:db8::/32 RO\n")
	compact, err := compactTrafficMap(source, map[string]bool{"RO": true}, "__other__")
	if err != nil {
		t.Fatal(err)
	}
	want := "10.0.0.0/8 RO\n11.0.0.0/8 __other__\n12.0.0.0/7 __other__\n2001:db8::/32 RO\n"
	if string(compact) != want || strings.Contains(string(compact), "14.0.0.0") {
		t.Fatalf("compaction changed lookup semantics:\n%s", compact)
	}
}

func TestTrafficProfileRequiresUnmodifiedSourceCaptures(t *testing.T) {
	p := testTraffic()
	p.LogSocket = "/run/haproxy/access.sock"
	c := &Client{Traffic: p, AllowRequestLogs: true}
	baseline := object{}
	set(baseline, "log_format", domain.RequestTrafficLogFormat)
	set(baseline, "log_target_list", []any{map[string]any{"nolog": true}, map[string]any{"address": p.LogSocket, "format": "raw", "length": 2048}})
	set(baseline, "tcp_request_rule_list", []any{
		map[string]any{"type": "connection", "action": "set-var", "var_scope": "sess", "var_name": "cv_peer", "expr": "src"},
		map[string]any{"type": "session", "action": "set-var", "var_scope": "sess", "var_name": "cv_client", "expr": "src"},
	})
	set(baseline, "http_request_rule_list", []object{trafficVariable("cv_xff_count", "req.fhdr_cnt(X-Forwarded-For)", ""), trafficVariable("cv_xff", "req.fhdr(X-Forwarded-For)", "")})
	if !c.trafficProfile(baseline) {
		t.Fatal("safe host capture rejected")
	}
	for name, mutate := range map[string]func(object){
		"source rewritten before capture": func(f object) {
			r := object{}
			set(r, "action", "set-src")
			set(r, "type", "connection")
			set(f, "tcp_request_rule_list", append([]object{r}, decode[[]object](f["tcp_request_rule_list"])...))
		},
		"header rewritten before capture": func(f object) {
			r := object{}
			set(r, "type", "set-header")
			set(r, "hdr_name", "X-Forwarded-For")
			set(f, "http_request_rule_list", append([]object{r}, decode[[]object](f["http_request_rule_list"])...))
		},
		"session evidence overwritten in HTTP": func(f object) {
			r := trafficVariable("cv_peer", "src", "")
			set(r, "var_scope", "sess")
			set(f, "http_request_rule_list", append(decode[[]object](f["http_request_rule_list"]), r))
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(baseline)
			var f object
			_ = json.Unmarshal(raw, &f)
			mutate(f)
			if c.trafficProfile(f) {
				t.Fatal("unsafe capture advertised as ready")
			}
		})
	}
}
