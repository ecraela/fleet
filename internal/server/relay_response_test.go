package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/connectwire"
	"github.com/ecraela/fleet/internal/domain"
)

func TestRelayRejectsActiveContentAndKeepsAPIResponsesInert(t *testing.T) {
	for _, test := range []struct {
		name, path, contentType, body string
		status, want                  int
	}{
		{"html", "/v1/nodes", "text/html", "<script>throw 'unsigned connector content'</script>", 200, 502},
		{"svg", "/v1/nodes", "image/svg+xml", "<svg onload='alert(1)'/>", 200, 502},
		{"missing type", "/v1/nodes", "", "<html>untyped</html>", 200, 502},
		{"malformed type", "/v1/nodes", "application/json; broken", "{}", 200, 502},
		{"events on ordinary route", "/v1/nodes", "text/event-stream", "data: {}\n\n", 200, 502},
		{"json", "/v1/nodes", "Application/JSON; charset=utf-8", `{"nodes":[]}`, 200, 200},
		{"empty", "/v1/nodes", "", "", 204, 204},
		{"events", "/v1/clusters/" + domain.NewID() + "/events", "text/event-stream", "data: {}\n\n", 200, 200},
		{"event error", "/v1/clusters/" + domain.NewID() + "/events", "application/json", `{"error":"offline"}`, 503, 503},
		{"data before headers", "/v1/nodes", "application/json", "<html>early content</html>", 200, 502},
		{"dns intent", "/v1/clusters/" + domain.NewID() + "/applications/" + domain.NewID() + "/dns-intent", "application/json", "dns-evidence", 200, 200},
		{"dns delegation", "/v1/clusters/" + domain.NewID() + "/applications/" + domain.NewID() + "/dns-delegation", "application/json", "dns-evidence", 200, 200},
		{"dns duplicate headers", "/v1/clusters/" + domain.NewID() + "/applications/" + domain.NewID() + "/dns-intent", "application/json", "dns-evidence", 200, 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := browserFixture(t)
			cookie, _ := cloudLogin(t, f, strings.HasPrefix(test.name, "dns"))
			link := domain.ConnectorLink{ID: domain.NewID(), OrganizationID: f.org, Name: "Synthetic peer", PairDigest: strings.Repeat("a", 64), CreatedAt: time.Now(), PairExpiresAt: time.Now().Add(time.Minute)}
			if err := f.store.CreateConnector(t.Context(), link); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			peer := &relayPeer{ctx: ctx, cancel: cancel, send: make(chan connectwire.Frame, 2), pending: map[string]chan connectwire.Frame{}, localOrg: domain.NewID()}
			f.server.relay = &relayService{peers: map[string]*relayPeer{link.ID: peer}}
			// Direct document navigation omits Origin but carries a valid session.
			request := httptest.NewRequest("GET", test.path, nil).WithContext(ctx)
			request.AddCookie(cookie)
			body := test.body
			if body == "dns-evidence" {
				identity, _ := f.server.browserIdentity(request)
				raw, _ := json.Marshal(map[string]any{"connector_dns_claims": map[string]any{"org": peer.localOrg, "sub": identity.UserID, "aud": "converge-dns", "iss": "local-manager", "v": 1, "hostname": "shop.example.test", "request_id": domain.NewID()}})
				body = string(raw)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				select {
				case request := <-peer.send:
					peer.mu.Lock()
					response := peer.pending[request.ID]
					peer.mu.Unlock()
					if test.name == "data before headers" {
						response <- connectwire.Frame{Kind: "data", Data: []byte(body)}
					}
					response <- connectwire.Frame{Kind: "headers", Status: test.status, Headers: map[string]string{"Content-Type": test.contentType}}
					if test.name == "dns duplicate headers" {
						response <- connectwire.Frame{Kind: "headers", Status: 200, Headers: map[string]string{"Content-Type": "application/json"}}
					}
					response <- connectwire.Frame{Kind: "data", Data: []byte(body[:len(body)/2])}
					response <- connectwire.Frame{Kind: "data", Data: []byte(body[len(body)/2:])}
					response <- connectwire.Frame{Kind: "end"}
				case <-ctx.Done():
				}
			}()
			response := httptest.NewRecorder()
			f.server.forwardAuthorized(response, request, f.org)
			<-done
			if response.Code != test.want {
				t.Fatalf("status %d, want %d", response.Code, test.want)
			}
			if !strings.Contains(response.Header().Get("Content-Security-Policy"), "sandbox") || response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("relay API response is missing its inert document policy")
			}
			if test.want == 502 && strings.Contains(response.Body.String(), test.body) {
				t.Fatal("untrusted active content reached the response")
			}
			if test.want != 502 && test.body != "dns-evidence" && response.Body.String() != test.body {
				t.Fatal("legitimate API body changed")
			}
			if test.body == "dns-evidence" && test.want == 200 && (!strings.Contains(response.Body.String(), `"intent":`) || strings.Contains(response.Body.String(), "connector_dns_claims")) {
				t.Fatal("relay did not sign DNS evidence locally")
			}
			if test.name == "dns duplicate headers" && !strings.Contains(response.Body.String(), "invalid connector response") {
				t.Fatal("duplicate DNS headers were not rejected before signing")
			}
			if test.name == "events" && !response.Flushed {
				t.Fatal("live events stopped flushing")
			}
		})
	}
}
