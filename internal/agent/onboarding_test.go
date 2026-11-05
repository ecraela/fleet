package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func TestPreflightObservesWithoutEnrollmentOrState(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/metrics" {
			t.Error("preflight attempted another request")
		}
		_, _ = w.Write([]byte("haproxy_process_current_connections 2\n"))
	}))
	defer metrics.Close()
	state := filepath.Join(t.TempDir(), "must-not-exist")
	report, err := Preflight(t.Context(), Config{StateDir: state, MetricsURL: metrics.URL + "/metrics", ConfigurationOwner: "external"})
	if err != nil || !PreflightOK(report) {
		t.Fatalf("read-only onboarding failed: %v %#v", err, report)
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("host check created identity or state")
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), state) || strings.Contains(string(data), metrics.URL) {
		t.Fatal("local locations entered the report")
	}
}

func TestPreflightFailuresDoNotCreateStateAndDoNotExposeResponses(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("private-upstream-diagnostic"))
	}))
	defer metrics.Close()
	report, err := Preflight(t.Context(), Config{MetricsURL: metrics.URL})
	if err != nil || PreflightOK(report) {
		t.Fatal("unreadable metrics passed", err)
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "private-upstream") || !strings.Contains(string(data), "metrics_unavailable") {
		t.Fatal("unexpected diagnostic response")
	}
}

func TestOwnershipHintsAreBoundedAndNeverGrantControl(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.cfg")
	if err := os.WriteFile(path, []byte("# Ansible managed: synthetic-private-host\nbackend app\n"), 0600); err != nil {
		t.Fatal(err)
	}
	files, automated := inspectFiles([]string{path})
	if files != "files_ready" || !automated {
		t.Fatal("external writer hint missing")
	}
	if configurationOwnershipAllows(Config{AllowConfigurationControl: true}, files, automated) ||
		configurationOwnershipAllows(Config{ConfigurationOwner: "external"}, files, false) ||
		!configurationOwnershipAllows(Config{ConfigurationOwner: "manager"}, files, automated) {
		t.Fatal("configuration ownership was inferred or ignored")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if code, _ := inspectFiles([]string{link}); code != "files_unreadable" {
		t.Fatal("configuration symlink followed")
	}
	if code, _ := inspectFiles([]string{dir}); code != "files_unreadable" {
		t.Fatal("non-regular configuration accepted")
	}
	if _, err := normalizeLocal(Config{ConfigurationOwner: "external", AllowConfigurationControl: true}); err == nil {
		t.Fatal("external owner accepted configuration-write grant")
	}
	if _, err := normalizeLocal(Config{ConfigurationOwner: "cloud"}); err == nil {
		t.Fatal("arbitrary owner accepted")
	}
}

func TestSetupDoesNotClaimUnavailableCapabilities(t *testing.T) {
	c := Config{AllowRuntimeControl: true, AllowConfigurationControl: true}
	d := domain.Discovery{Version: "3.4.7", ErrorCode: "runtime_unavailable"}
	metrics := &metricsObservation{At: time.Now().Add(-time.Minute), OK: true}
	report := nodeSetup(c, d, metrics, "files_unreadable", false, false)
	if !report.Valid() || PreflightOK(report) {
		t.Fatal("unavailable host capabilities passed")
	}
	for _, check := range report.Checks {
		if check.Status == "ready" {
			t.Fatal("broken prerequisites advertised readiness", check)
		}
	}
	d.ErrorCode = ""
	report = nodeSetup(c, d, &metricsObservation{At: time.Now(), OK: true}, "files_ready", false, false)
	if report.Checks[4].Code != "runtime_admin_required" || report.Checks[5].Code != "configuration_unavailable" {
		t.Fatal("missing admin or DPA access was hidden")
	}
	// Explicitly configured DPA access must be checked even when writes are off.
	c = Config{DataplaneSocket: "/local/dpa.sock"}
	report = nodeSetup(c, d, &metricsObservation{At: time.Now(), OK: true}, "files_ready", false, false)
	if report.Checks[5].Code != "configuration_unavailable" || PreflightOK(report) {
		t.Fatal("broken read-only DPA configuration passed preflight")
	}
}
