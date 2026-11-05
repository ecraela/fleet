package acmeissuer

import (
	"encoding/base64"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/miekg/dns"
)

func TestDNS01DelegationAndExactValueCleanup(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
	key := "node.validation.test."
	challenge := domain.ACMEChallenge{Hostname: "app.example.test", Target: "app.validation.test"}
	records := map[string]bool{}
	var mu sync.Mutex
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		mu.Lock()
		defer mu.Unlock()
		out := new(dns.Msg)
		out.SetReply(m)
		if m.Opcode == dns.OpcodeUpdate {
			if w.TsigStatus() != nil || m.IsTsig() == nil {
				out.Rcode = dns.RcodeRefused
			} else {
				for _, rr := range m.Ns {
					txt, ok := rr.(*dns.TXT)
					if !ok || txt.Hdr.Name != dns.Fqdn(challenge.Target) || len(txt.Txt) != 1 {
						out.Rcode = dns.RcodeRefused
						continue
					}
					if txt.Hdr.Class == dns.ClassINET {
						records[txt.Txt[0]] = true
					} else if txt.Hdr.Class == dns.ClassNONE {
						delete(records, txt.Txt[0])
					} else {
						t.Error("cleanup attempted an RRset deletion")
					}
				}
				out.SetTsig(key, dns.HmacSHA256, 60, time.Now().Unix())
			}
		} else if len(m.Question) == 1 {
			q := m.Question[0]
			if q.Qtype == dns.TypeCNAME && q.Name == "_acme-challenge.app.example.test." {
				out.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET}, Target: dns.Fqdn(challenge.Target)}}
			}
			if q.Qtype == dns.TypeTXT && q.Name == dns.Fqdn(challenge.Target) {
				for value := range records {
					out.Answer = append(out.Answer, &dns.TXT{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{value}})
				}
			}
		}
		_ = w.WriteMsg(out)
	})
	server := &dns.Server{MsgAcceptFunc: func(dns.Header) dns.MsgAcceptAction { return dns.MsgAccept }, Listener: listener, Handler: handler, TsigSecret: map[string]string{key: secret}}
	go server.ActivateAndServe()
	t.Cleanup(func() { _ = server.Shutdown() })
	p := &Profile{DNSServer: listener.Addr().String(), Resolver: listener.Addr().String(), ValidationZone: "validation.test", KeyName: key, secret: secret, Challenges: []domain.ACMEChallenge{challenge}}
	a, b := strings.Repeat("a", 43), strings.Repeat("b", 43)
	if !p.delegation(t.Context(), challenge) {
		t.Fatal("delegation missing")
	}
	for _, value := range []string{a, b} {
		if e = p.updateTXT(t.Context(), challenge.Target, value, false); e != nil {
			t.Fatal(e)
		}
	}
	if !p.propagated(t.Context(), challenge, a) || !p.propagated(t.Context(), challenge, b) {
		t.Fatal("concurrent challenges lost")
	}
	if e = p.updateTXT(t.Context(), challenge.Target, a, true); e != nil {
		t.Fatal(e)
	}
	if p.propagated(t.Context(), challenge, a) || !p.propagated(t.Context(), challenge, b) {
		t.Fatal("cleanup deleted another order's challenge")
	}
	if e = p.updateTXT(t.Context(), "ungranted.validation.test", a, false); e == nil {
		t.Fatal("ungranted target updated")
	}
	if e = p.updateTXT(t.Context(), challenge.Target, "short", false); e == nil {
		t.Fatal("invalid TXT updated")
	}
	wrong := challenge
	wrong.Target = "other.validation.test"
	if p.delegation(t.Context(), wrong) {
		t.Fatal("incorrect delegation accepted")
	}
	own := savedOrder{Challenges: []savedChallenge{{Name: challenge.Target, Value: b}}}
	runner := &Runner{state: t.TempDir(), policy: Policy{Profiles: []Profile{*p}}}
	journal := filepath.Join(runner.state, "interrupted.order.json")
	if e = saveJSON(journal, own); e != nil {
		t.Fatal(e)
	}
	runner.Cleanup(t.Context())
	if p.propagated(t.Context(), challenge, b) {
		t.Fatal("disabled/offline journal cleanup did not resume")
	}
	p.secret = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	if e = p.updateTXT(t.Context(), challenge.Target, a, false); e == nil {
		t.Fatal("invalid TSIG accepted")
	}
}
