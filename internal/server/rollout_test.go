package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func rolloutForAPI(t *testing.T, f *fixture) domain.RolloutInput {
	t.Helper()
	d := configurationForAPI()
	d.Proxies = []domain.Proxy{{Name: "public", Kind: "frontend", Mode: "http", Status: "OPEN"}, {Name: "application", Kind: "backend", Mode: "http", Algorithm: "roundrobin", Status: "UP"}}
	for i := range d.Servers {
		d.Servers[i].CheckStatus = "L7OK"
	}
	d.SetGeneration()
	input := domain.RolloutInput{RequestID: domain.NewID(), Document: d.Configuration.Document.Canonical()}
	input.Document.Backends[0].Servers[0].Weight = 200
	for range 2 {
		n, _, _, _ := f.enrollNode(t)
		session := domain.NewID()
		_, err := f.store.OpenControl(t.Context(), f.org, n.ID, session)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = f.store.ReportControl(t.Context(), f.org, n.ID, session, &d, nil); err != nil {
			t.Fatal(err)
		}
		input.Nodes = append(input.Nodes, domain.RolloutTarget{NodeID: n.ID, ExpectedGeneration: d.Generation, ExpectedArtifactID: d.Configuration.ArtifactID, ExpectedVersion: 1})
	}
	return input
}
func TestRolloutEndpointsAreTenantBoundAndDirect(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	input := rolloutForAPI(t, f)
	other := domain.NewID()
	foreign, err := f.store.CreateCluster(t.Context(), other, "Other")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		for _, cluster := range []string{f.cluster.ID, foreign.ID} {
			path := "/v1/clusters/" + cluster + "/rollouts"
			var body any
			if method == "POST" {
				body = input
			}
			if got := f.request(method, path, body, false); got.Code != 401 {
				t.Fatal("anonymous rollout access", got.Code)
			}
			want := 404
			if cluster == f.cluster.ID {
				want = 200
				if method == "POST" {
					want = 202
				}
			}
			if got := f.request(method, path, body, true); got.Code != want {
				t.Fatal("tenant rollout status", got.Code, want, got.Body.String())
			}
		}
	}
	// A valid cluster path cannot target a node from another tenant.
	otherFixture := newFixture(t, "manager", "http://metrics.invalid")
	bad := rolloutForAPI(t, otherFixture)
	bad.Nodes[0].NodeID = input.Nodes[0].NodeID
	if got := otherFixture.request("POST", "/v1/clusters/"+otherFixture.cluster.ID+"/rollouts", bad, true); got.Code != 404 {
		t.Fatal("foreign node accepted in rollout", got.Code)
	}
}
func TestBrowserRolloutRequiresManageAndCSRFWithoutApproval(t *testing.T) {
	for _, manage := range []bool{false, true} {
		for _, csrfValid := range []bool{false, true} {
			f := browserFixture(t)
			input := rolloutForAPI(t, f)
			pending, nonce := challenge(t, f.server)
			claims := validClaims(f, nonce)
			if manage {
				claims.Capabilities = append(claims.Capabilities, "haproxy.manage")
			}
			response := exchange(t, f, pending, claims)
			cookie := sessionCookie(t, response)
			var browser struct {
				CSRF string `json:"csrf_token"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &browser); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(input)
			request := httptest.NewRequest("POST", "/v1/clusters/"+f.cluster.ID+"/rollouts", strings.NewReader(string(raw)))
			request.AddCookie(cookie)
			request.Header.Set("Origin", testBrowserOrigin)
			request.Header.Set("Content-Type", "application/json")
			if csrfValid {
				request.Header.Set("X-CSRF-Token", browser.CSRF)
			}
			got := httptest.NewRecorder()
			f.server.ServeHTTP(got, request)
			want := 403
			if manage && csrfValid {
				want = 202
			}
			if got.Code != want {
				t.Fatal("rollout manage/CSRF gate", got.Code, want, got.Body.String())
			}
			if want == 202 {
				var accepted struct {
					Rollout domain.Rollout `json:"rollout"`
				}
				_ = json.Unmarshal(got.Body.Bytes(), &accepted)
				if accepted.Rollout.Status != "running" || accepted.Rollout.ActorUserID == "" || time.Since(accepted.Rollout.CreatedAt) > time.Minute {
					t.Fatal("rollout did not start directly under the browser actor")
				}
			}
		}
	}
}
