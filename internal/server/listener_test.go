package server

import (
	"encoding/json"
	"github.com/ecraela/fleet/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListenerTenantPermissionsAndCSRF(t *testing.T) {
	f := browserFixture(t)
	foreign, _ := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
	target := func() domain.RolloutTarget {
		return domain.RolloutTarget{NodeID: domain.NewID(), ExpectedGeneration: strings.Repeat("a", 64), ExpectedArtifactID: strings.Repeat("b", 64), ExpectedVersion: 1}
	}
	input := domain.ListenerInput{RequestID: domain.NewID(), Nodes: []domain.RolloutTarget{target(), target()}, Entrypoint: &domain.RolloutEntrypointTarget{RolloutTarget: target(), BindingID: strings.Repeat("c", 64)}}
	for _, cluster := range []string{f.cluster.ID, foreign.ID} {
		base := "/v1/clusters/" + cluster + "/listeners"
		for _, path := range []string{base, base + "/versions/" + domain.NewID() + "/rollback"} {
			if got := f.request("POST", path, input, false); got.Code != 401 {
				t.Fatal("anonymous write", got.Code)
			}
			if got := f.request("POST", path, input, true); cluster == foreign.ID && got.Code != 404 {
				t.Fatal("foreign write", got.Code, got.Body.String())
			}
		}
		want := 200
		if cluster == foreign.ID {
			want = 404
		}
		if got := f.request("GET", base, nil, true); got.Code != want {
			t.Fatal("tenant history", got.Code)
		}
	}
	for _, manage := range []bool{false, true} {
		pending, nonce := challenge(t, f.server)
		claims := validClaims(f, nonce)
		if manage {
			claims.Capabilities = append(claims.Capabilities, "haproxy.manage")
		}
		created := exchange(t, f, pending, claims)
		cookie := sessionCookie(t, created)
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		_ = json.Unmarshal(created.Body.Bytes(), &session)
		for _, csrf := range []string{"", session.CSRF} {
			raw, _ := json.Marshal(input)
			req := httptest.NewRequest("POST", "/v1/clusters/"+f.cluster.ID+"/listeners", strings.NewReader(string(raw)))
			req.AddCookie(cookie)
			req.Header.Set("Origin", testBrowserOrigin)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-CSRF-Token", csrf)
			got := httptest.NewRecorder()
			f.server.ServeHTTP(got, req)
			want := 403
			if manage && csrf != "" {
				want = 409
			}
			if got.Code != want {
				t.Fatal("manage/CSRF gate", manage, csrf != "", got.Code, got.Body.String())
			}
		}
	}
}
