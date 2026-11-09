package software

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var imagePattern = regexp.MustCompile(`^(?:ghcr\.io/ecraela/fleet@)?sha256:[0-9a-f]{64}$`)

func ValidRevision(value string) bool { return revisionPattern.MatchString(value) }

// Release is copied only from a catalog created by a host-owned verifier.
// The updater independently verifies the signed bundle before activation.
type Release struct {
	Version         string            `json:"version"`
	Revision        string            `json:"revision"`
	ManifestSHA256  string            `json:"manifest_sha256"`
	Publisher       string            `json:"publisher"`
	Image           string            `json:"image"`
	Protocol        uint32            `json:"protocol"`
	StateSchema     uint32            `json:"state_schema"`
	Platforms       []string          `json:"platforms"`
	AgentDigests    map[string]string `json:"agent_digests"`
	ManagerDigests  map[string]string `json:"manager_digests"`
	ManagedUpgrades bool              `json:"managed_upgrades"`
	Compatibility   Compatibility     `json:"compatibility"`
	Notes           string            `json:"notes,omitempty"`
}

func (r Release) Valid() bool {
	if !ValidVersion(r.Version) || !revisionPattern.MatchString(r.Revision) || !digestPattern.MatchString(r.ManifestSHA256) ||
		!(r.Publisher == "sigstore:ecraela/fleet" || strings.HasPrefix(r.Publisher, "ed25519:") && digestPattern.MatchString(strings.TrimPrefix(r.Publisher, "ed25519:"))) ||
		!imagePattern.MatchString(r.Image) || r.Protocol != 1 || r.StateSchema != 1 || !r.Compatibility.Valid(r.Version) ||
		len(r.Platforms) == 0 || len(r.Platforms) > 2 || len(r.Notes) > 4096 || len(r.AgentDigests) != len(r.Platforms) || len(r.ManagerDigests) != len(r.Platforms) {
		return false
	}
	seen := map[string]bool{}
	for _, platform := range r.Platforms {
		if platform != "linux/amd64" && platform != "linux/arm64" || seen[platform] {
			return false
		}
		if !digestPattern.MatchString(r.AgentDigests[platform]) || !digestPattern.MatchString(r.ManagerDigests[platform]) {
			return false
		}
		seen[platform] = true
	}
	for platform := range r.AgentDigests {
		if !seen[platform] {
			return false
		}
	}
	for platform := range r.ManagerDigests {
		if !seen[platform] {
			return false
		}
	}
	return true
}

func (r Release) Supports(platform string) bool {
	for _, p := range r.Platforms {
		if p == platform {
			return true
		}
	}
	return false
}

type Catalog struct {
	Schema   int       `json:"schema"`
	Releases []Release `json:"releases"`
}

func (c Catalog) Release(version string) (Release, bool) {
	for _, release := range c.Releases {
		if release.Version == version {
			return release, true
		}
	}
	return Release{}, false
}

// LoadCatalog reads a root-provisioned catalog from a regular, non-writable
// file. The file contains no credentials, so it can be mounted read-only into
// the unprivileged manager container. No network request runs in this handler.
func LoadCatalog(path string) (Catalog, error) {
	var catalog Catalog
	if path == "" {
		return catalog, errors.New("release catalog is not configured")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return catalog, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 128<<10 {
		return catalog, errors.New("release catalog must be a bounded, non-writable regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return catalog, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return Catalog{}, errors.New("release catalog changed during opening")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 128<<10+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&catalog) != nil || decoder.Decode(new(any)) != io.EOF || catalog.Schema != 1 || len(catalog.Releases) > 50 {
		return Catalog{}, errors.New("invalid release catalog")
	}
	seen := map[string]bool{}
	for _, release := range catalog.Releases {
		if !release.Valid() || seen[release.Version] {
			return Catalog{}, errors.New("invalid release catalog entry")
		}
		seen[release.Version] = true
	}
	sort.Slice(catalog.Releases, func(i, j int) bool {
		order, _ := Compare(catalog.Releases[i].Version, catalog.Releases[j].Version)
		return order > 0
	})
	return catalog, nil
}
