package domain

import (
	"strings"
	"testing"
)

func trafficFixture() *TrafficPolicy {
	return &TrafficPolicy{Version: 1, Mode: "observe", UnknownAction: "allow", Rules: []TrafficRule{{ID: NewID(), Name: "Country block", Action: "block", Countries: []string{"RU"}}, {ID: NewID(), Name: "Networks", Action: "allow", CIDRs: []string{"192.0.2.0/24", "2001:db8::/32"}, ASNs: []uint32{15169}}}}
}
func TestTrafficPolicyRejectsUnsafeAndUnboundedIntent(t *testing.T) {
	p := trafficFixture()
	if !p.Valid("/shop") {
		t.Fatal("valid policy rejected")
	}
	for _, change := range []func(*TrafficPolicy){
		func(p *TrafficPolicy) { p.Version = 2 }, func(p *TrafficPolicy) { p.Mode = "raw" }, func(p *TrafficPolicy) { p.UnknownAction = "guess" },
		func(p *TrafficPolicy) { p.Rules[0].Action = "http-request allow" }, func(p *TrafficPolicy) { p.Rules[0].ID = p.Rules[1].ID },
		func(p *TrafficPolicy) { p.Rules[0].Countries = []string{"ru"} }, func(p *TrafficPolicy) { p.Rules[0].Countries = []string{"RU", "RU"} },
		func(p *TrafficPolicy) { p.Rules[0].Countries = []string{"RU } http-request allow"} }, func(p *TrafficPolicy) { p.Rules[0].ASNs = []uint32{0} },
		func(p *TrafficPolicy) { p.Rules[0].CIDRs = []string{"192.0.2.1/24"} }, func(p *TrafficPolicy) { p.Rules[0].CIDRs = []string{"::ffff:192.0.2.0/120"} },
		func(p *TrafficPolicy) { p.Rules[0].PathPrefix = "/other" }, func(p *TrafficPolicy) { p.Rules[0].Name = "name\x00" },
		func(p *TrafficPolicy) { p.Rules[0].Countries = nil }, func(p *TrafficPolicy) { p.Rules = append(p.Rules, make([]TrafficRule, 8)...) },
	} {
		bad := p.Canonical()
		change(bad)
		if bad.Valid("/shop") {
			t.Fatal("unsafe traffic policy accepted", bad)
		}
	}
	canonical := p.Canonical()
	canonical.Rules[1].CIDRs[0] = "changed"
	if strings.Contains(strings.Join(p.Rules[1].CIDRs, " "), "changed") {
		t.Fatal("canonical aliases original")
	}
}
func TestTrafficPolicyRequiresLocalCapabilitiesAndVersionsTheApplication(t *testing.T) {
	p := trafficFixture()
	c := &TrafficCapabilities{Profile: strings.Repeat("a", 64), ClientSource: "peer", CountryReady: true, ASNReady: true}
	if p.Supported(nil) || !p.Supported(c) {
		t.Fatal("local authority missing")
	}
	c.CountryReady = false
	if p.Supported(c) {
		t.Fatal("missing required data accepted")
	}
}
