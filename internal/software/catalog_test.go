package software

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRelease(version string) Release {
	return Release{Version: version, Revision: strings.Repeat("a", 40), ManifestSHA256: strings.Repeat("b", 64),
		Publisher: "sigstore:ecraela/fleet", Image: "ghcr.io/ecraela/fleet@sha256:" + strings.Repeat("c", 64),
		Protocol: 1, StateSchema: 1, Platforms: []string{"linux/amd64", "linux/arm64"},
		AgentDigests:   map[string]string{"linux/amd64": strings.Repeat("d", 64), "linux/arm64": strings.Repeat("e", 64)},
		ManagerDigests: map[string]string{"linux/amd64": strings.Repeat("f", 64), "linux/arm64": strings.Repeat("a", 64)},
		Compatibility:  Compatibility{Manager: Range{Min: "v1.0.0", Max: "v2.0.0"}, Agent: Range{Min: "v1.0.0", Max: "v2.0.0"}}}
}

func TestExactReleaseVersionsAndInclusiveRanges(t *testing.T) {
	for _, invalid := range []string{"v01.2.3", "v1.2", "v1.2.3+build", "v1.2.3-RC1", "v1.2.3-01", "1.2.3"} {
		if ValidVersion(invalid) {
			t.Fatal("accepted", invalid)
		}
	}
	for _, pair := range [][2]string{{"v1.2.0-rc.2", "v1.2.0-rc.10"}, {"v1.2.0-rc.10", "v1.2.0"}, {"v1.2.0", "v1.2.1"}} {
		if order, err := Compare(pair[0], pair[1]); err != nil || order >= 0 {
			t.Fatal(pair, order, err)
		}
	}
	if !(Range{Min: "v1.2.0", Max: "v1.3.0"}).Contains("v1.3.0") || (Range{Min: "v1.2.0", Max: "v1.3.0"}).Contains("v1.3.1") {
		t.Fatal("range comparison")
	}
}

func TestCatalogRejectsMutableOrUntrustedEntries(t *testing.T) {
	release := testRelease("v1.2.0")
	if !release.Valid() {
		t.Fatal("valid signed release metadata refused")
	}
	for _, mutate := range []func(*Release){
		func(r *Release) { r.AgentDigests["linux/amd64"] = "latest" },
		func(r *Release) { r.Compatibility.Manager.Min = "v1.3.0" },
		func(r *Release) { r.Publisher = "unknown" },
		func(r *Release) { r.Platforms = append(r.Platforms, "darwin/arm64") },
	} {
		copy := testRelease("v1.2.0")
		mutate(&copy)
		if copy.Valid() {
			t.Fatal("invalid catalog entry accepted", copy)
		}
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "releases.json")
	write := func(value any) {
		t.Helper()
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(Catalog{Schema: 1, Releases: []Release{testRelease("v1.1.0"), testRelease("v1.2.0")}})
	got, err := LoadCatalog(path)
	if err != nil || got.Releases[0].Version != "v1.2.0" {
		t.Fatal(got, err)
	}
	write(Catalog{Schema: 1, Releases: []Release{release, release}})
	if _, err := LoadCatalog(path); err == nil {
		t.Fatal("duplicate version accepted")
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(path); err == nil {
		t.Fatal("writable catalog accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "other"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(path); err == nil {
		t.Fatal("catalog symlink accepted")
	}
}
