package domain

import (
	"net/netip"
	"slices"
	"strings"
)

// TrafficPolicy is ordered application intent. Proxy trust and datasets remain
// local authority; neither HAProxy expressions nor local file paths are accepted.
type TrafficPolicy struct {
	Version       uint32        `json:"version"`
	Mode          string        `json:"mode"`
	UnknownAction string        `json:"unknown_action"`
	Rules         []TrafficRule `json:"rules"`
}
type TrafficRule struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Action     string   `json:"action"`
	PathPrefix string   `json:"path_prefix,omitempty"`
	CIDRs      []string `json:"cidrs,omitempty"`
	Countries  []string `json:"countries,omitempty"`
	ASNs       []uint32 `json:"asns,omitempty"`
}
type TrafficCapabilities struct {
	Profile      string `json:"profile"`
	ClientSource string `json:"client_source"`
	CountryReady bool   `json:"country_ready"`
	ASNReady     bool   `json:"asn_ready"`
}

func (c TrafficCapabilities) Valid() bool {
	return ValidDigest(c.Profile) && slices.Contains([]string{"peer", "xff", "proxy_protocol"}, c.ClientSource)
}
func (p *TrafficPolicy) Canonical() *TrafficPolicy {
	if p == nil {
		return nil
	}
	out := *p
	out.Rules = append([]TrafficRule{}, p.Rules...)
	for i := range out.Rules {
		r := &out.Rules[i]
		r.Name = strings.TrimSpace(r.Name)
		r.CIDRs = append([]string(nil), r.CIDRs...)
		slices.Sort(r.CIDRs)
		r.Countries = append([]string(nil), r.Countries...)
		slices.Sort(r.Countries)
		r.ASNs = append([]uint32(nil), r.ASNs...)
		slices.Sort(r.ASNs)
	}
	return &out
}
func (p *TrafficPolicy) Valid(path string) bool {
	if p == nil {
		return true
	}
	if p.Version != 1 || !slices.Contains([]string{"observe", "enforce"}, p.Mode) || !slices.Contains([]string{"allow", "block"}, p.UnknownAction) || len(p.Rules) == 0 || len(p.Rules) > 8 {
		return false
	}
	ids := map[string]bool{}
	for _, r := range p.Rules {
		if !ValidID(r.ID) || ids[r.ID] || len(r.Name) == 0 || len(r.Name) > 64 || strings.TrimSpace(r.Name) != r.Name || strings.ContainsAny(r.Name, "\r\n\t") || !slices.Contains([]string{"allow", "block"}, r.Action) || r.PathPrefix != "" && (!HTTPPath(r.PathPrefix) || !strings.HasPrefix(r.PathPrefix, path)) || len(r.CIDRs)+len(r.Countries)+len(r.ASNs) == 0 || len(r.CIDRs) > 16 || len(r.Countries) > 16 || len(r.ASNs) > 16 {
			return false
		}
		ids[r.ID] = true
		for _, c := range r.Name {
			if c < 32 || c == 127 {
				return false
			}
		}
		seen := map[string]bool{}
		for _, s := range r.CIDRs {
			v, e := netip.ParsePrefix(s)
			if e != nil || v != v.Masked() || v.String() != s || v.Addr().Is4In6() || seen[s] {
				return false
			}
			seen[s] = true
		}
		for _, s := range r.Countries {
			if len(s) != 2 || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' || s == "ZZ" || seen[s] {
				return false
			}
			seen[s] = true
		}
		asns := map[uint32]bool{}
		for _, n := range r.ASNs {
			if n == 0 || asns[n] {
				return false
			}
			asns[n] = true
		}
	}
	return true
}
func (p *TrafficPolicy) Supported(c *TrafficCapabilities) bool {
	if p == nil {
		return true
	}
	if c == nil || !c.Valid() {
		return false
	}
	for _, r := range p.Rules {
		if len(r.Countries) > 0 && !c.CountryReady || len(r.ASNs) > 0 && !c.ASNReady {
			return false
		}
	}
	return true
}
