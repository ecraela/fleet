package dataplane

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if os.WriteFile(p.CountryMap, []byte("changed"), 0644) != nil {
		t.Fatal("tamper")
	}
	if _, err = LoadTrafficPolicy(grant, ipPath); err == nil {
		t.Fatal("tampered immutable map reused")
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
