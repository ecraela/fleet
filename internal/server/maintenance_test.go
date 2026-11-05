package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ecraela/fleet/internal/domain"
)

func maintenanceInput() domain.MaintenanceInput {
	return domain.MaintenanceInput{RequestID: domain.NewID(), Action: "withdraw", BindingID: strings.Repeat("a", 64), ExpectedGeneration: strings.Repeat("b", 64), ExpectedEntrypointGeneration: strings.Repeat("c", 64), ExpectedArtifactID: strings.Repeat("d", 64), ExpectedVersion: 1, DrainTimeoutSeconds: 30}
}
func TestMaintenanceEndpointsEnforceTenantScope(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	node, _, _, _ := f.enrollNode(t)
	other := domain.NewID()
	cluster, _ := f.store.CreateCluster(t.Context(), other, "Foreign")
	foreign := enrollForFleet(t, f, other, cluster.ID)
	for _, v := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/v1/nodes/" + foreign.ID + "/maintenance", nil},
		{"POST", "/v1/nodes/" + foreign.ID + "/maintenance", maintenanceInput()},
		{"GET", "/v1/clusters/" + cluster.ID + "/maintenance", nil},
	} {
		if got := f.request(v.method, v.path, v.body, true); got.Code != 404 {
			t.Fatal(v.method, v.path, got.Code, got.Body.String())
		}
	}
	if got := f.request("GET", "/v1/nodes/"+node.ID+"/maintenance", nil, true); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	if got := f.request("POST", "/v1/nodes/"+node.ID+"/maintenance", maintenanceInput(), true); got.Code != 409 {
		t.Fatal("unbound node maintenance accepted", got.Code)
	}
}
func TestMaintenanceUsesExistingPermissionsAndCSRF(t *testing.T) {
	f := browserFixture(t)
	node, _, _, _ := f.enrollNode(t)
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
			body, _ := json.Marshal(maintenanceInput())
			request := httptest.NewRequest("POST", "/v1/nodes/"+node.ID+"/maintenance", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", testBrowserOrigin)
			request.Header.Set("X-CSRF-Token", csrf)
			request.AddCookie(cookie)
			got := httptest.NewRecorder()
			f.server.ServeHTTP(got, request)
			want := 403
			if manage && csrf != "" {
				want = 409
			}
			if got.Code != want {
				t.Fatalf("manage=%v csrf=%v got=%d", manage, csrf != "", got.Code)
			}
		}
		for _, path := range []string{"/v1/nodes/" + node.ID + "/maintenance", "/v1/clusters/" + node.ClusterID + "/maintenance"} {
			request := httptest.NewRequest("GET", path, nil)
			request.AddCookie(cookie)
			got := httptest.NewRecorder()
			f.server.ServeHTTP(got, request)
			if got.Code != 200 {
				t.Fatal("read permission rejected", got.Code, got.Body.String())
			}
		}
	}
}
