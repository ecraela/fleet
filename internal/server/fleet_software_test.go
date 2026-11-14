package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/buildinfo"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
	"github.com/ecraela/fleet/internal/store"
)

func setupFleetSoftwareTest(t *testing.T) (*fixture, domain.Node, software.Release, *buildinfo.Info) {
	t.Helper()
	f, node, _, release := setupSoftwareTest(t)
	manager := &buildinfo.Info{Version: "v1.1.0", Revision: strings.Repeat("a", 40), Protocol: 1,
		Platform: "linux/amd64", ExecutableSHA256: strings.Repeat("9", 64)}
	f.server.buildInfo = func() buildinfo.Info { return *manager }
	token := strings.Repeat("secret", 12)
	hash := sha256.Sum256([]byte(token))
	f.server.config.InstallationOrganizationID = node.OrganizationID
	f.server.config.SoftwareHostTokenSHA256 = hex.EncodeToString(hash[:])
	if got := hostSoftwareRequest(f, node.OrganizationID, token, "POST", "/v1/host/software/heartbeat",
		software.Updater{CurrentVersion: manager.Version, IngestVersion: manager.Version, Ready: true}); got.Code != 200 {
		t.Fatal("manager updater", got.Code, got.Body.String())
	}
	return f, node, release, manager
}

func healthyFleetMetrics(t *testing.T, f *fixture, node domain.Node) {
	healthyFleetMetricsAt(t, f, node, time.Now().Add(10*time.Millisecond))
}

func healthyFleetMetricsAt(t *testing.T, f *fixture, node domain.Node, observedAt time.Time) {
	t.Helper()
	f.server.client = &http.Client{Transport: fleetMetricsTransport(func(_ *http.Request) (*http.Response, error) {
		labels := func(name string) map[string]string {
			return map[string]string{
				"__name__": name, "organization_id": node.OrganizationID, "cluster_id": node.ClusterID, "node_id": node.ID,
			}
		}
		now := float64(observedAt.UnixNano()) / 1e9
		body, _ := json.Marshal(map[string]any{"status": "success", "data": map[string]any{
			"resultType": "vector", "result": []any{
				map[string]any{"metric": labels("converge_agent_haproxy_up"), "value": []any{now, "1"}},
				map[string]any{"metric": labels("converge_agent_scrape_timestamp_seconds"), "value": []any{now, fmt.Sprintf("%.9f", now)}},
			},
		}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
}

type fleetMetricsTransport func(*http.Request) (*http.Response, error)

func (f fleetMetricsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFleetSoftwareCoordinatesManagerAndAgent(t *testing.T) {
	f, node, release, manager := setupFleetSoftwareTest(t)
	second, _, _, _ := f.enrollNode(t)
	softwareDiscovery(t, f, second, &buildinfo.Info{Version: "v1.1.0", Revision: strings.Repeat("a", 40),
		Protocol: 1, Platform: "linux/amd64", ExecutableSHA256: strings.Repeat("b", 64)}, "")
	if _, err := f.store.HeartbeatSoftwareUpdater(t.Context(), second.OrganizationID, second.ID,
		software.Updater{CurrentVersion: "v1.1.0", Ready: true}); err != nil {
		t.Fatal(err)
	}
	planResponse := f.request("GET", "/v1/software/fleet/plan?version="+release.Version, nil, true)
	var planned struct {
		Plan fleetSoftwarePlan `json:"plan"`
	}
	if planResponse.Code != 200 || json.Unmarshal(planResponse.Body.Bytes(), &planned) != nil || !planned.Plan.Eligible ||
		len(planned.Plan.Steps) != 3 || planned.Plan.Steps[0].Role != "manager" || planned.Plan.Steps[1].Role != "agent" || planned.Plan.Steps[2].Role != "agent" {
		t.Fatal("fleet plan", planResponse.Code, planResponse.Body.String())
	}
	nodes := map[string]domain.Node{node.ID: node, second.ID: second}
	firstAgent, lastAgent := nodes[planned.Plan.Steps[1].NodeID], nodes[planned.Plan.Steps[2].NodeID]
	input := map[string]string{"request_id": domain.NewID(), "version": release.Version, "fingerprint": planned.Plan.Fingerprint}
	if got := f.request("POST", "/v1/software/fleet", input, false); got.Code != 401 {
		t.Fatal("anonymous fleet request", got.Code)
	}
	created := f.request("POST", "/v1/software/fleet", input, true)
	var receipt struct {
		Update software.FleetUpdate `json:"update"`
	}
	if created.Code != 202 || json.Unmarshal(created.Body.Bytes(), &receipt) != nil {
		t.Fatal("create", created.Code, created.Body.String())
	}
	if got := f.request("POST", "/v1/software/fleet", input, true); got.Code != 200 {
		t.Fatal("retry", got.Code, got.Body.String())
	}
	if got := f.request("GET", "/v1/software/fleet", nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), receipt.Update.ID) {
		t.Fatal("history", got.Code, got.Body.String())
	}
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, receipt.Update); err != nil {
		t.Fatal("dispatch manager", err)
	}
	managerChild, err := f.store.InstallationUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.Steps[0].RequestID)
	if err != nil {
		t.Fatal("manager child", err)
	}
	if _, err := f.store.SoftwareUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.Steps[1].RequestID); err != domain.ErrNotFound {
		t.Fatal("agent dispatched before manager", err)
	}
	for _, state := range []string{"preparing", "activating", "verifying", "succeeded"} {
		if _, err := f.store.TransitionInstallationUpdate(t.Context(), node.OrganizationID, managerChild.ID, state, ""); err != nil {
			t.Fatal(state, err)
		}
	}
	manager.Version, manager.Revision, manager.ExecutableSHA256 = release.Version, release.Revision, release.ManagerDigests[manager.Platform]
	current, err := f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, current); err != nil {
		t.Fatal("manager checkpoint", err)
	}
	current, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.RequestID)
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, current); err != nil {
		t.Fatal("dispatch agent", err)
	}
	agentChild, err := f.store.SoftwareUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.Steps[1].RequestID)
	if err != nil {
		t.Fatal("agent child", err)
	}
	if _, err := f.store.SoftwareUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.Steps[2].RequestID); err != domain.ErrNotFound {
		t.Fatal("second agent dispatched early", err)
	}
	for _, state := range []string{"preparing", "activating", "verifying", "succeeded"} {
		if _, err := f.store.TransitionSoftwareUpdate(t.Context(), node.OrganizationID, firstAgent.ID, agentChild.ID, state, ""); err != nil {
			t.Fatal(state, err)
		}
	}
	healthyFleetMetricsAt(t, f, firstAgent, agentChild.CreatedAt)
	current, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.RequestID)
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, current); err != nil {
		t.Fatal("stale agent scrape", err)
	}
	current, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.RequestID)
	if current.NextStep != 1 {
		t.Fatal("stale scrape advanced fleet update", current)
	}
	healthyFleetMetrics(t, f, firstAgent)
	current, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.RequestID)
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, current); err != nil {
		t.Fatal("agent checkpoint", err)
	}
	current, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.RequestID)
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, current); err != nil {
		t.Fatal("dispatch second agent", err)
	}
	lastChild, err := f.store.SoftwareUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.Steps[2].RequestID)
	if err != nil {
		t.Fatal("second agent child", err)
	}
	for _, state := range []string{"preparing", "activating", "verifying", "succeeded"} {
		if _, err := f.store.TransitionSoftwareUpdate(t.Context(), node.OrganizationID, lastAgent.ID, lastChild.ID, state, ""); err != nil {
			t.Fatal(state, err)
		}
	}
	healthyFleetMetrics(t, f, lastAgent)
	current, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.RequestID)
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, current); err != nil {
		t.Fatal("second agent checkpoint", err)
	}
	current, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, receipt.Update.RequestID)
	if current.Status != "succeeded" || current.NextStep != 3 {
		t.Fatal("installation did not complete", current)
	}
}

func TestFleetSoftwarePausesAfterHostFailure(t *testing.T) {
	f, node, release, manager := setupFleetSoftwareTest(t)
	plan, err := f.server.fleetPreflight(testRequest(t), node.OrganizationID, release.Version)
	if err != nil || !plan.Eligible {
		t.Fatal("plan", err, plan.Reason)
	}
	for i := range plan.Steps {
		plan.Steps[i].RequestID = domain.NewID()
	}
	root, err := f.store.CreateFleetUpdate(t.Context(), node.OrganizationID, store.FleetSoftwareRequest{RequestID: domain.NewID(),
		TargetVersion: release.Version, ManifestSHA256: release.ManifestSHA256, Fingerprint: plan.Fingerprint, Steps: plan.Steps})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, root); err != nil {
		t.Fatal(err)
	}
	managerChild, _ := f.store.InstallationUpdateByRequest(t.Context(), node.OrganizationID, root.Steps[0].RequestID)
	for _, state := range []string{"preparing", "activating", "verifying", "succeeded"} {
		if _, err := f.store.TransitionInstallationUpdate(t.Context(), node.OrganizationID, managerChild.ID, state, ""); err != nil {
			t.Fatal(err)
		}
	}
	manager.Version, manager.Revision, manager.ExecutableSHA256 = release.Version, release.Revision, release.ManagerDigests[manager.Platform]
	root, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, root.RequestID)
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, root); err != nil {
		t.Fatal(err)
	}
	root, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, root.RequestID)
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, root); err != nil {
		t.Fatal(err)
	}
	agentChild, _ := f.store.SoftwareUpdateByRequest(t.Context(), node.OrganizationID, root.Steps[1].RequestID)
	if _, err := f.store.TransitionSoftwareUpdate(t.Context(), node.OrganizationID, node.ID, agentChild.ID, "needs_attention", "activation_failed"); err != nil {
		t.Fatal(err)
	}
	root, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, root.RequestID)
	if err := f.server.advanceFleetUpdate(t.Context(), f.store, root); err != nil {
		t.Fatal(err)
	}
	root, _ = f.store.FleetUpdateByRequest(t.Context(), node.OrganizationID, root.RequestID)
	if root.Status != "paused" || root.NextStep != 1 || !strings.Contains(root.Reason, "activation_failed") {
		t.Fatal("did not pause", root)
	}
}

func TestFleetSoftwareRejectsChangedPlan(t *testing.T) {
	f, node, release, _ := setupFleetSoftwareTest(t)
	plan, err := f.server.fleetPreflight(testRequest(t), node.OrganizationID, release.Version)
	if err != nil || !plan.Eligible {
		t.Fatal("initial plan", err, plan.Reason)
	}
	if _, err := f.store.OpenControl(t.Context(), node.OrganizationID, node.ID, domain.NewID()); err != nil {
		t.Fatal(err)
	}
	request := map[string]string{"request_id": domain.NewID(), "version": release.Version, "fingerprint": plan.Fingerprint}
	if got := f.request("POST", "/v1/software/fleet", request, true); got.Code != 409 {
		t.Fatal("stale plan accepted", got.Code, got.Body.String())
	}
	if got := f.request("GET", "/v1/software/fleet/plan?version="+release.Version, nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"eligible":false`) {
		t.Fatal("stale plan not blocked", got.Code, got.Body.String())
	}
}

func testRequest(t *testing.T) *http.Request { t.Helper(); return httptest.NewRequest("GET", "/", nil) }
