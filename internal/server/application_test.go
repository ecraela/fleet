package server

import (
	"encoding/json"
	"github.com/ecraela/fleet/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
)

func applicationAPIInput() domain.ApplicationInput {
	return domain.ApplicationInput{RequestID: domain.NewID(), Entrypoint: &domain.RolloutEntrypointTarget{}, Spec: domain.HTTPApplication{Verification: &domain.ApplicationVerificationPolicy{HTTP: true, HTTPS: true, Entrypoint: true}}}
}
func TestApplicationEndpointsRequireTenantPermissionsAndCSRF(t *testing.T) {
	f := browserFixture(t)
	foreign, _ := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
	for _, cluster := range []string{f.cluster.ID, foreign.ID} {
		base := "/v1/clusters/" + cluster + "/applications"
		if got := f.request("GET", base+"/status", nil, false); got.Code != 401 {
			t.Fatal("anonymous application health", got.Code)
		}
		wantStatus := 200
		if cluster == foreign.ID {
			wantStatus = 404
		}
		if got := f.request("GET", base+"/status", nil, true); got.Code != wantStatus {
			t.Fatal("tenant application health", got.Code, got.Body.String())
		}
		for _, path := range []string{base, base + "/" + domain.NewID() + "/deploy", base + "/" + domain.NewID() + "/versions/" + domain.NewID() + "/rollback"} {
			if got := f.request("POST", path, applicationAPIInput(), false); got.Code != 401 {
				t.Fatal("anonymous application write", got.Code)
			}
			if got := f.request("POST", path, applicationAPIInput(), true); cluster == foreign.ID && got.Code != 404 {
				t.Fatal("foreign application write", got.Code, got.Body.String())
			}
		}
		got := f.request("GET", base, nil, true)
		want := 200
		if cluster == foreign.ID {
			want = 404
		}
		if got.Code != want {
			t.Fatal("tenant read", got.Code, got.Body.String())
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
		read := httptest.NewRequest("GET", "/v1/clusters/"+f.cluster.ID+"/applications/status", nil)
		read.AddCookie(cookie)
		readResult := httptest.NewRecorder()
		f.server.ServeHTTP(readResult, read)
		if readResult.Code != 200 {
			t.Fatal("viewer cannot read application health", readResult.Code)
		}
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		_ = json.Unmarshal(created.Body.Bytes(), &session)
		for _, csrf := range []string{"", session.CSRF} {
			raw, _ := json.Marshal(applicationAPIInput())
			request := httptest.NewRequest("POST", "/v1/clusters/"+f.cluster.ID+"/applications", strings.NewReader(string(raw)))
			request.AddCookie(cookie)
			request.Header.Set("Origin", testBrowserOrigin)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", csrf)
			got := httptest.NewRecorder()
			f.server.ServeHTTP(got, request)
			want := 403
			if manage && csrf != "" {
				want = 400
			}
			if got.Code != want {
				t.Fatal("application manage/CSRF gate", manage, csrf != "", got.Code, got.Body.String())
			}
		}
	}
}
func TestApplicationLifecycleRequiresTenantManageAndCSRF(t *testing.T) {
	f := browserFixture(t)
	foreign, _ := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
	body := map[string]any{"request_id": domain.NewID(), "action": "disable", "expected_revision": 1, "nodes": []any{}, "entrypoint": map[string]any{}}
	for _, cluster := range []string{f.cluster.ID, foreign.ID} {
		path := "/v1/clusters/" + cluster + "/applications/" + domain.NewID() + "/lifecycle"
		if got := f.request("POST", path, body, false); got.Code != 401 {
			t.Fatal("anonymous lifecycle", got.Code)
		}
		if got := f.request("POST", path, body, true); got.Code != 404 {
			t.Fatal("tenant lifecycle", got.Code, got.Body.String())
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
			raw, _ := json.Marshal(body)
			request := httptest.NewRequest("POST", "/v1/clusters/"+f.cluster.ID+"/applications/"+domain.NewID()+"/lifecycle", strings.NewReader(string(raw)))
			request.AddCookie(cookie)
			request.Header.Set("Origin", testBrowserOrigin)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", csrf)
			got := httptest.NewRecorder()
			f.server.ServeHTTP(got, request)
			want := 403
			if manage && csrf != "" {
				want = 404
			}
			if got.Code != want {
				t.Fatal("lifecycle capability/CSRF", manage, csrf != "", got.Code, got.Body.String())
			}
		}
	}
}
