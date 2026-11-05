package acmeissuer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/miekg/dns"
)

func dnsQuery(ctx context.Context, server, name string, kind uint16) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), kind)
	m.RecursionDesired = true
	c := &dns.Client{Net: "tcp", Timeout: 3 * time.Second}
	out, _, err := c.ExchangeContext(ctx, m, server)
	if err != nil || out == nil || out.Rcode != dns.RcodeSuccess || out.Truncated {
		return nil, errors.New("DNS answer unavailable")
	}
	return out, nil
}
func (p *Profile) delegation(ctx context.Context, challenge domain.ACMEChallenge) bool {
	owner := "_acme-challenge." + challenge.Hostname
	out, err := dnsQuery(ctx, p.Resolver, owner, dns.TypeCNAME)
	if err != nil {
		return false
	}
	for _, rr := range out.Answer {
		if c, ok := rr.(*dns.CNAME); ok && strings.EqualFold(c.Hdr.Name, dns.Fqdn(owner)) && strings.EqualFold(c.Target, dns.Fqdn(challenge.Target)) {
			return true
		}
	}
	return false
}
func (p *Profile) updateTXT(ctx context.Context, name, value string, remove bool) error {
	if len(value) != 43 || !strings.HasSuffix(name, "."+p.ValidationZone) {
		return errors.New("invalid challenge record")
	}
	allowed := false
	for _, c := range p.Challenges {
		allowed = allowed || c.Target == name
	}
	if !allowed {
		return errors.New("ungranted challenge record")
	}
	m := new(dns.Msg)
	m.SetUpdate(dns.Fqdn(p.ValidationZone))
	rr := &dns.TXT{Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 30}, Txt: []string{value}}
	if remove {
		m.Remove([]dns.RR{rr})
	} else {
		m.Insert([]dns.RR{rr})
	}
	m.SetTsig(dns.Fqdn(p.KeyName), dns.HmacSHA256, 60, time.Now().Unix())
	c := &dns.Client{Net: "tcp", Timeout: 3 * time.Second, TsigSecret: map[string]string{dns.Fqdn(p.KeyName): p.secret}}
	response, _, err := c.ExchangeContext(ctx, m, p.DNSServer)
	if err != nil || response == nil || response.Rcode != dns.RcodeSuccess || response.IsTsig() == nil {
		return errors.New("DNS-01 update failed")
	}
	return nil
}
func (p *Profile) propagated(ctx context.Context, c domain.ACMEChallenge, value string) bool {
	if !p.delegation(ctx, c) {
		return false
	}
	out, err := dnsQuery(ctx, p.Resolver, c.Target, dns.TypeTXT)
	if err != nil {
		return false
	}
	for _, rr := range out.Answer {
		if txt, ok := rr.(*dns.TXT); ok && strings.EqualFold(txt.Hdr.Name, dns.Fqdn(c.Target)) && strings.Join(txt.Txt, "") == value {
			return true
		}
	}
	return false
}
