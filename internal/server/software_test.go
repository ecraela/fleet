package server

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ecraela/fleet/internal/buildinfo"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
)

func setupSoftwareTest(t *testing.T) (*fixture, domain.Node, *tls.ConnectionState, software.Release) {
	t.Helper()
	previousVersion, previousRevision := buildinfo.Version, buildinfo.Revision
	buildinfo.Version, buildinfo.Revision = "v1.1.0", strings.Repeat("a", 40)
	t.Cleanup(func() { buildinfo.Version, buildinfo.Revision = previousVersion, previousRevision })
	f := newFixture(t, "manager", "http://metrics.invalid")
	node, leaf, _, _ := f.enrollNode(t)
	current := &buildinfo.Info{Version: "v1.1.0", Revision: strings.Repeat("a", 40), Protocol: 1, Platform: "linux/amd64", ExecutableSHA256: strings.Repeat("b", 64)}
	softwareDiscovery(t, f, node, current, "")
	release := software.Release{Version: "v1.2.0", Revision: strings.Repeat("c", 40), ManifestSHA256: strings.Repeat("d", 64),
		Publisher: "sigstore:ecraela/fleet", Image: "ghcr.io/ecraela/fleet@sha256:" + strings.Repeat("e", 64), Protocol: 1, StateSchema: 1,
		Platforms: []string{"linux/amd64", "linux/arm64"}, AgentDigests: map[string]string{"linux/amd64": strings.Repeat("f", 64), "linux/arm64": strings.Repeat("a", 64)},
		ManagerDigests: map[string]string{"linux/amd64": strings.Repeat("b", 64), "linux/arm64": strings.Repeat("c", 64)}, ManagedUpgrades: true,
		Compatibility: software.Compatibility{Manager: software.Range{Min: "v1.1.0", Max: "v1.2.0"}, Agent: software.Range{Min: "v1.1.0", Max: "v1.2.0"}}}
	raw, _ := json.Marshal(software.Catalog{Schema: 1, Releases: []software.Release{release}})
	path := filepath.Join(t.TempDir(), "releases.json")
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	f.server.config.ReleaseCatalogFile = path
	identity := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	if got := nodeSoftwareRequest(f, identity, "POST", "/v1/identity/software/heartbeat", software.Updater{CurrentVersion: current.Version, Ready: true}); got.Code != 200 {
		t.Fatal("updater heartbeat", got.Code, got.Body.String())
	}
	return f, node, identity, release
}

func softwareDiscovery(t *testing.T, f *fixture, node domain.Node, build *buildinfo.Info, session string) string {
	t.Helper()
	if session == "" {
		session = domain.NewID()
		if _, err := f.store.OpenControl(t.Context(), node.OrganizationID, node.ID, session); err != nil {
			t.Fatal(err)
		}
	}
	d := discoveryForControl()
	d.Agent = build
	if _, _, err := f.store.ReportControl(t.Context(), node.OrganizationID, node.ID, session, &d, nil); err != nil {
		t.Fatal(err)
	}
	return session
}

func nodeSoftwareRequest(f *fixture, state *tls.ConnectionState, method, path string, body any) *httptest.ResponseRecorder {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.TLS = state
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	return w
}

func TestSoftwarePlanDesiredStateAndObservedCompletion(t *testing.T) {
	f, node, identity, release := setupSoftwareTest(t)
	prefix := "/v1/nodes/" + node.ID + "/software"
	if got := f.request("GET", "/v1/software/releases", nil, true); got.Code != 200 {
		t.Fatal("catalog", got.Code, got.Body.String())
	}
	if got := f.request("GET", prefix+"/plan?version=v1.2.0&action=upgrade", nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":true`) {
		t.Fatal("plan", got.Code, got.Body.String())
	}
	input := map[string]string{"request_id": domain.NewID(), "action": "upgrade", "version": "v1.2.0", "expected_version": "v1.1.0"}
	if got := f.request("POST", prefix, input, false); got.Code != 401 {
		t.Fatal("anonymous update", got.Code)
	}
	created := f.request("POST", prefix, input, true)
	if created.Code != 202 {
		t.Fatal("create", created.Code, created.Body.String())
	}
	var record struct {
		Update struct {
			ID string `json:"id"`
		} `json:"update"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if got := f.request("POST", prefix, input, true); got.Code != 200 || !strings.Contains(got.Body.String(), record.Update.ID) {
		t.Fatal("replay", got.Code)
	}
	if got := nodeSoftwareRequest(f, nil, "GET", "/v1/identity/software", nil); got.Code != 401 {
		t.Fatal("anonymous desired state", got.Code)
	}
	if got := nodeSoftwareRequest(f, identity, "GET", "/v1/identity/software", nil); got.Code != 200 || !strings.Contains(got.Body.String(), record.Update.ID) {
		t.Fatal("desired state", got.Code, got.Body.String())
	}
	status := "/v1/identity/software/" + record.Update.ID + "/status"
	for _, step := range []string{"preparing", "activating", "verifying"} {
		if got := nodeSoftwareRequest(f, identity, "POST", status, map[string]string{"status": step}); got.Code != 200 {
			t.Fatal(step, got.Code, got.Body.String())
		}
	}
	if got := nodeSoftwareRequest(f, identity, "POST", status, map[string]string{"status": "succeeded"}); got.Code != 409 {
		t.Fatal("unobserved success", got.Code)
	}
	build := &buildinfo.Info{Version: release.Version, Revision: release.Revision, Protocol: 1, Platform: "linux/amd64", ExecutableSHA256: release.AgentDigests["linux/amd64"]}
	softwareDiscovery(t, f, node, build, f.storeControlSession(t, node))
	if got := nodeSoftwareRequest(f, identity, "POST", status, map[string]string{"status": "succeeded"}); got.Code != 200 {
		t.Fatal("verified success", got.Code, got.Body.String())
	}
	if got := nodeSoftwareRequest(f, identity, "GET", "/v1/identity/software", nil); got.Code != 200 || !strings.Contains(got.Body.String(), `"update":null`) {
		t.Fatal("completed desired state", got.Code, got.Body.String())
	}
}

func (f *fixture) storeControlSession(t *testing.T, node domain.Node) string {
	t.Helper()
	control, _, err := f.store.GetControl(t.Context(), node.OrganizationID, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	return control.SessionID
}

func TestSoftwarePlanBlocksIncompatibleAndStaleBuilds(t *testing.T) {
	f, node, _, release := setupSoftwareTest(t)
	prefix := "/v1/nodes/" + node.ID + "/software/plan?version=" + release.Version + "&action=upgrade"
	if got := f.request("GET", prefix, nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":true`) {
		t.Fatal(got.Code, got.Body.String())
	}
	buildinfo.Version = "dev"
	if got := f.request("GET", prefix, nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":false`) {
		t.Fatal("unknown manager", got.Code, got.Body.String())
	}
	buildinfo.Version = "v1.1.0"
	if _, err := f.store.OpenControl(t.Context(), node.OrganizationID, node.ID, domain.NewID()); err != nil {
		t.Fatal(err)
	}
	if got := f.request("GET", prefix, nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":false`) {
		t.Fatal("stale build", got.Code, got.Body.String())
	}
	if got := f.request("GET", "/v1/nodes/"+domain.NewID()+"/software", nil, true); got.Code != 404 {
		t.Fatal("foreign history", got.Code)
	}
}

func TestSoftwarePlanRejectsReleaseWithoutManagedUpdater(t *testing.T) {
	f, node, _, release := setupSoftwareTest(t)
	release.ManagedUpgrades = false
	raw, err := json.Marshal(software.Catalog{Schema: 1, Releases: []software.Release{release}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.server.config.ReleaseCatalogFile, raw, 0644); err != nil {
		t.Fatal(err)
	}
	path := "/v1/nodes/" + node.ID + "/software/plan?version=" + release.Version + "&action=upgrade"
	got := f.request("GET", path, nil, true)
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":false`) ||
		!strings.Contains(got.Body.String(), "target release does not support managed upgrades") {
		t.Fatal(got.Code, got.Body.String())
	}
}
