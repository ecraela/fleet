package server

import (
	"encoding/json"
	"github.com/ecraela/fleet/internal/domain"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGuidedEnrollmentAuthorization(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	token := f.token(t, time.Now().Add(time.Minute))
	for _, path := range []string{"/v1/enrollment-status"} {
		if r := f.request("POST", path, map[string]string{"token": token}, false); r.Code != 401 {
			t.Fatalf("anonymous status: %d", r.Code)
		}
		if r := f.request("POST", path, map[string]string{"token": "short"}, true); r.Code != 400 {
			t.Fatalf("invalid token: %d", r.Code)
		}
		foreignOrg := domain.NewID()
		cluster, err := f.store.CreateCluster(t.Context(), foreignOrg, "Other")
		if err != nil {
			t.Fatal(err)
		}
		foreignToken := secret()
		if err := f.store.CreateEnrollmentToken(t.Context(), foreignOrg, cluster.ID, digest(foreignToken), time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if r := f.request("POST", path, map[string]string{"token": foreignToken}, true); r.Code != 404 {
			t.Fatalf("foreign status: %d", r.Code)
		}
		r := f.request("POST", path, map[string]string{"token": token}, true)
		if r.Code != 200 {
			t.Fatalf("own status: %d", r.Code)
		}
		var result map[string]any
		_ = json.Unmarshal(r.Body.Bytes(), &result)
		if result["state"] != "pending" {
			t.Fatal(result)
		}
	}
	if r := f.request("GET", "/v1/agent-setup", nil, false); r.Code != 401 {
		t.Fatal(r.Code)
	}
	if r := f.request("GET", "/v1/agent-setup", nil, true); r.Code != 503 {
		t.Fatal(r.Code)
	}
	f.server.config.AgentSetup = AgentSetup{Image: "example.test/agent@sha256:demo", ManagerURL: "https://manager.test", IngestURL: "https://ingest.test"}
	if err := f.server.validateAgentSetup(); err != nil {
		t.Fatal(err)
	}
	r := f.request("GET", "/v1/agent-setup", nil, true)
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	var setup AgentSetup
	_ = json.Unmarshal(r.Body.Bytes(), &setup)
	if setup.CAPEM != string(f.authority.PEM) || setup.Network != "host" {
		t.Fatal("missing public trust or network")
	}
	f.server.config.AgentSetup.ManagerURL = "http://unsafe.test"
	if f.server.validateAgentSetup() == nil {
		t.Fatal("unencrypted enrollment address accepted")
	}
	// Ingestion never exposes installer metadata or browser setup tracking.
	ingest := newFixture(t, "ingest", "http://metrics.invalid")
	response := httptest.NewRecorder()
	ingest.server.ServeHTTP(response, httptest.NewRequest("GET", "/v1/agent-setup", nil))
	if response.Code != 404 {
		t.Fatal(response.Code)
	}
}
