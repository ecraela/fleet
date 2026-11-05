package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func configurationForAPI() domain.Discovery {
	d := discoveryForControl()
	doc := domain.ConfigurationDocument{Backends: []domain.ConfigurationBackend{{Name: "application", Algorithm: "roundrobin", Servers: []domain.ConfigurationServer{{Name: "app-a", Address: "backend.internal", Port: 8080, Weight: 100}, {Name: "app-b", Address: "backend.internal", Port: 8081, Weight: 100}}}}}
	d.Configuration = &domain.ConfigurationState{Version: 1, ArtifactID: strings.Repeat("a", 64), Document: &doc, ControlAllowed: true, ObservedAtMS: time.Now().UnixMilli()}
	return d
}
func configurationBody(d domain.Discovery) map[string]any {
	doc := d.Configuration.Document.Canonical()
	doc.Backends[0].Servers[0].Weight = 200
	return map[string]any{"request_id": domain.NewID(), "expected_generation": d.Generation, "expected_artifact_id": d.Configuration.ArtifactID, "expected_version": 1, "document": doc}
}
func TestConfigurationEndpointsRejectOtherTenantsAndNodes(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	node, _, _, _ := f.enrollNode(t)
	other := domain.NewID()
	cluster, _ := f.store.CreateCluster(t.Context(), other, "Other")
	foreign := enrollForFleet(t, f, other, cluster.ID)
	d := configurationForAPI()
	session := domain.NewID()
	_, _ = f.store.OpenControl(t.Context(), f.org, node.ID, session)
	_, _, _ = f.store.ReportControl(t.Context(), f.org, node.ID, session, &d, nil)
	_, versions, _ := f.store.GetConfiguration(t.Context(), f.org, node.ID)
	for _, id := range []string{node.ID, foreign.ID} {
		for _, route := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/v1/nodes/" + id + "/configuration", nil},
			{"POST", "/v1/nodes/" + id + "/configuration/versions/" + versions[0].ID + "/rollback", map[string]any{"request_id": domain.NewID(), "expected_generation": d.Generation, "expected_artifact_id": d.Configuration.ArtifactID, "expected_version": 1}},
		} {
			if got := f.request(route.method, route.path, route.body, false); got.Code != 401 {
				t.Fatal("anonymous configuration access", got.Code)
			}
			got := f.request(route.method, route.path, route.body, true)
			want := 404
			if id == node.ID {
				want = 200
				if route.method == "POST" {
					want = 202
				}
			}
			if got.Code != want {
				t.Fatalf("%s got %d want %d: %s", route.path, got.Code, want, got.Body.String())
			}
		}
	}
	if got := f.request("POST", "/v1/nodes/"+foreign.ID+"/configuration", configurationBody(d), true); got.Code != 404 {
		t.Fatal("foreign configuration changed", got.Code)
	}
}
func TestConfigurationBrowserUsesPermissionsAndCSRFDirectly(t *testing.T) {
	for _, manage := range []bool{false, true} {
		f := browserFixture(t)
		node, _, _, _ := f.enrollNode(t)
		d := configurationForAPI()
		session := domain.NewID()
		_, _ = f.store.OpenControl(t.Context(), f.org, node.ID, session)
		_, _, _ = f.store.ReportControl(t.Context(), f.org, node.ID, session, &d, nil)
		pending, nonce := challenge(t, f.server)
		claims := validClaims(f, nonce)
		if manage {
			claims.Capabilities = append(claims.Capabilities, "haproxy.manage")
		}
		created := exchange(t, f, pending, claims)
		cookie := sessionCookie(t, created)
		var browser struct {
			CSRF string `json:"csrf_token"`
		}
		_ = json.Unmarshal(created.Body.Bytes(), &browser)
		for _, csrf := range []string{"", browser.CSRF} {
			body, _ := json.Marshal(configurationBody(d))
			r := httptest.NewRequest("POST", "/v1/nodes/"+node.ID+"/configuration", strings.NewReader(string(body)))
			r.AddCookie(cookie)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", testBrowserOrigin)
			r.Header.Set("X-CSRF-Token", csrf)
			got := httptest.NewRecorder()
			f.server.ServeHTTP(got, r)
			want := 403
			if manage && csrf != "" {
				want = 202
			}
			if got.Code != want {
				t.Fatalf("manage=%v csrf=%v got=%d want=%d", manage, csrf != "", got.Code, want)
			}
		}
	}
}
