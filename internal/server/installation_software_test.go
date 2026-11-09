package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ecraela/fleet/internal/buildinfo"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
)

func hostSoftwareRequest(f *fixture, org, token, method, path string, body any) *httptest.ResponseRecorder {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("X-Organization-ID", org)
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	return w
}

func TestInstallationSoftwarePlanHostAuthenticationAndObservedCompletion(t *testing.T) {
	f, node, _, release := setupSoftwareTest(t)
	manager := buildinfo.Info{Version: "v1.1.0", Revision: strings.Repeat("a", 40), Protocol: 1,
		Platform: "linux/amd64", ExecutableSHA256: strings.Repeat("9", 64)}
	f.server.buildInfo = func() buildinfo.Info { return manager }
	token := strings.Repeat("secret", 12)
	hash := sha256.Sum256([]byte(token))
	f.server.config.InstallationOrganizationID = node.OrganizationID
	f.server.config.SoftwareHostTokenSHA256 = hex.EncodeToString(hash[:])
	planPath := "/v1/software/manager/plan?version=v1.2.0&action=upgrade"
	if got := f.request("GET", planPath, nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":false`) {
		t.Fatal("unregistered host", got.Code, got.Body.String())
	}
	if got := hostSoftwareRequest(f, node.OrganizationID, "", "GET", "/v1/host/software", nil); got.Code != 401 {
		t.Fatal("anonymous host", got.Code)
	}
	if got := hostSoftwareRequest(f, domain.NewID(), token, "GET", "/v1/host/software", nil); got.Code != 401 {
		t.Fatal("foreign host", got.Code)
	}
	if got := hostSoftwareRequest(f, node.OrganizationID, token, "POST", "/v1/host/software/heartbeat",
		software.Updater{CurrentVersion: manager.Version, IngestVersion: manager.Version, Ready: true}); got.Code != 200 {
		t.Fatal("host heartbeat", got.Code, got.Body.String())
	}
	if got := f.request("GET", planPath, nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":true`) || !strings.Contains(got.Body.String(), `"agent_count":1`) {
		t.Fatal("eligible plan", got.Code, got.Body.String())
	}
	input := map[string]string{"request_id": domain.NewID(), "action": "upgrade", "version": release.Version, "expected_version": manager.Version}
	if got := f.request("POST", "/v1/software/manager", input, false); got.Code != 401 {
		t.Fatal("anonymous request", got.Code)
	}
	created := f.request("POST", "/v1/software/manager", input, true)
	if created.Code != 202 {
		t.Fatal("create", created.Code, created.Body.String())
	}
	var receipt struct {
		Update struct {
			ID string `json:"id"`
		} `json:"update"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if got := f.request("POST", "/v1/software/manager", input, true); got.Code != 200 || !strings.Contains(got.Body.String(), receipt.Update.ID) {
		t.Fatal("idempotent request", got.Code, got.Body.String())
	}
	if got := hostSoftwareRequest(f, node.OrganizationID, token, "GET", "/v1/host/software", nil); got.Code != 200 || !strings.Contains(got.Body.String(), receipt.Update.ID) || !strings.Contains(got.Body.String(), node.ID) {
		t.Fatal("desired state and cohort", got.Code, got.Body.String())
	}
	statusPath := "/v1/host/software/" + receipt.Update.ID + "/status"
	for _, step := range []string{"preparing", "activating", "verifying"} {
		if got := hostSoftwareRequest(f, node.OrganizationID, token, "POST", statusPath, map[string]string{"status": step}); got.Code != 200 {
			t.Fatal(step, got.Code, got.Body.String())
		}
	}
	if got := hostSoftwareRequest(f, node.OrganizationID, token, "POST", statusPath, map[string]string{"status": "succeeded"}); got.Code != 409 {
		t.Fatal("unobserved manager success", got.Code)
	}
	manager.Version, manager.Revision, manager.ExecutableSHA256 = release.Version, release.Revision, release.ManagerDigests[manager.Platform]
	if got := hostSoftwareRequest(f, node.OrganizationID, token, "POST", "/v1/host/software/heartbeat",
		software.Updater{CurrentVersion: manager.Version, IngestVersion: manager.Version, Ready: true}); got.Code != 200 {
		t.Fatal("post-switch ingest", got.Code, got.Body.String())
	}
	if got := hostSoftwareRequest(f, node.OrganizationID, token, "POST", statusPath, map[string]string{"status": "succeeded"}); got.Code != 200 {
		t.Fatal("verified manager success", got.Code, got.Body.String())
	}
	if got := hostSoftwareRequest(f, node.OrganizationID, token, "GET", "/v1/host/software", nil); got.Code != 200 || !strings.Contains(got.Body.String(), `"update":null`) {
		t.Fatal("completed desired state", got.Code, got.Body.String())
	}
}

func TestInstallationSoftwareBlocksStaleOrIncompatibleAgents(t *testing.T) {
	f, node, _, _ := setupSoftwareTest(t)
	manager := buildinfo.Info{Version: "v1.1.0", Revision: strings.Repeat("a", 40), Protocol: 1,
		Platform: "linux/amd64", ExecutableSHA256: strings.Repeat("9", 64)}
	f.server.buildInfo = func() buildinfo.Info { return manager }
	token := strings.Repeat("secret", 12)
	hash := sha256.Sum256([]byte(token))
	f.server.config.InstallationOrganizationID = node.OrganizationID
	f.server.config.SoftwareHostTokenSHA256 = hex.EncodeToString(hash[:])
	if got := hostSoftwareRequest(f, node.OrganizationID, token, "POST", "/v1/host/software/heartbeat",
		software.Updater{CurrentVersion: manager.Version, IngestVersion: manager.Version, Ready: true}); got.Code != 200 {
		t.Fatal(got.Code)
	}
	path := "/v1/software/manager/plan?version=v1.2.0&action=upgrade"
	if got := f.request("GET", path, nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":true`) {
		t.Fatal(got.Code, got.Body.String())
	}
	softwareDiscovery(t, f, node, &buildinfo.Info{Version: "v2.0.0", Revision: strings.Repeat("a", 40),
		Platform: "linux/amd64", Protocol: 1, ExecutableSHA256: strings.Repeat("b", 64)}, f.storeControlSession(t, node))
	if got := f.request("GET", path, nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":false`) || !strings.Contains(got.Body.String(), "incompatible") {
		t.Fatal("incompatible agent", got.Code, got.Body.String())
	}
	if _, err := f.store.OpenControl(t.Context(), node.OrganizationID, node.ID, domain.NewID()); err != nil {
		t.Fatal(err)
	}
	if got := f.request("GET", path, nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":false`) {
		t.Fatal("stale agent", got.Code, got.Body.String())
	}
}
