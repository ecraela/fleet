package dataplane

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ecraela/fleet/internal/domain"
)

const maxArtifactBinding = 128 << 10

// Only a complete, version-fenced observation may associate raw bytes with
// intent. Commands and commit responses are not independent evidence of intent.
// These records, including local certificate paths, never leave the node.
type artifactBinding struct {
	Version          int                          `json:"version"`
	ArtifactID       string                       `json:"artifact_id"`
	Document         domain.ConfigurationDocument `json:"document"`
	CertificatePaths map[string]string            `json:"certificate_paths"`
	LogProfiles      map[string]string            `json:"log_profiles"`
	TrafficRules     map[string]string            `json:"traffic_rules"`
}

func dependencyProfile(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func frontendLogProfile(f object) string {
	return dependencyProfile(object{"log_format": f["log_format"], "log_target_list": f["log_target_list"]})
}

func (c *Client) retainBinding(id string, doc domain.ConfigurationDocument, frontends []object) error {
	b := artifactBinding{Version: 1, ArtifactID: id, Document: doc.Canonical(), CertificatePaths: map[string]string{}, LogProfiles: map[string]string{}, TrafficRules: map[string]string{}}
	for _, a := range doc.Applications {
		if a.TrafficPolicy != nil && !a.Disabled && c.Traffic != nil {
			rules, err := c.Traffic.rules(a.TrafficPolicy)
			if err != nil {
				return err
			}
			b.TrafficRules[a.ID] = dependencyProfile(rules)
		}
	}
	for _, f := range frontends {
		name := decode[string](f["name"])
		for _, l := range doc.Listeners {
			if l.Name == name {
				b.CertificatePaths[name] = decode[string](decode[map[string]object](f["binds"])[listenerBind]["ssl_certificate"])
			}
		}
		for _, a := range doc.Applications {
			if a.Listener == name && a.RequestTelemetry {
				b.LogProfiles[name] = frontendLogProfile(f)
			}
		}
	}
	raw, err := json.Marshal(b)
	if err != nil || len(raw) > maxArtifactBinding {
		return failure("configuration_unsupported")
	}
	dir, err := c.directory("configuration-bindings")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, id+".json")
	checkExisting := func() error {
		previous, err := c.loadBinding(id)
		encoded, _ := json.Marshal(previous)
		if err != nil || !bytes.Equal(encoded, raw) {
			return failure("configuration_history_missing")
		}
		return nil
	}
	if _, err := os.Lstat(path); err == nil {
		return checkExisting()
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) >= 256 {
		return failure("configuration_storage_full")
	}
	file, err := os.CreateTemp(dir, ".binding-")
	if err != nil {
		return failure("configuration_storage_full")
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return failure("configuration_storage_full")
	}
	// Publish without exposing a partially written record or replacing a binding.
	if err = os.Link(file.Name(), path); errors.Is(err, os.ErrExist) {
		return checkExisting()
	} else if err != nil {
		return failure("configuration_storage_full")
	}
	return syncDir(dir)
}

func (c *Client) loadBinding(id string) (artifactBinding, error) {
	var b artifactBinding
	if !domain.ValidDigest(id) {
		return b, failure("configuration_history_missing")
	}
	f, err := os.OpenFile(filepath.Join(c.StateDir, "configuration-bindings", id+".json"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return b, failure("configuration_history_missing")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxArtifactBinding {
		return b, failure("configuration_history_missing")
	}
	d := json.NewDecoder(io.LimitReader(f, maxArtifactBinding+1))
	d.DisallowUnknownFields()
	if d.Decode(&b) != nil || d.Decode(new(any)) != io.EOF || b.Version != 1 || b.ArtifactID != id || !b.Document.Valid() || len(b.CertificatePaths) != len(b.Document.Listeners) {
		return b, failure("configuration_history_missing")
	}
	return b, nil
}

func (c *Client) validateBinding(id string, doc domain.ConfigurationDocument, frontends []object) error {
	b, err := c.loadBinding(id)
	if err != nil || !b.Document.Equal(doc) {
		return failure("configuration_history_missing")
	}
	if err := c.validateListeners(b.Document); err != nil {
		return err
	}
	for _, l := range b.Document.Listeners {
		certificate, ok := c.certificate(l.CertificateRef)
		if !ok || b.CertificatePaths[l.Name] == "" || b.CertificatePaths[l.Name] != certificate.Path {
			return failure("configuration_rejected")
		}
	}
	for _, a := range b.Document.Applications {
		if a.RequestTelemetry && !c.AllowRequestLogs {
			return failure("configuration_rejected")
		}
		if a.TrafficPolicy != nil && (c.Traffic == nil || !a.TrafficPolicy.Supported(&c.Traffic.Capabilities)) {
			return failure("configuration_rejected")
		}
		if a.TrafficPolicy != nil && !a.Disabled {
			rules, err := c.Traffic.rules(a.TrafficPolicy)
			if err != nil || b.TrafficRules[a.ID] != dependencyProfile(rules) {
				return failure("configuration_rejected")
			}
		}
		if !a.RequestTelemetry {
			continue
		}
		found := false
		for _, f := range frontends {
			if decode[string](f["name"]) == a.Listener && domain.ValidRequestLogFormat(decode[string](f["log_format"])) && b.LogProfiles[a.Listener] == frontendLogProfile(f) && (a.TrafficPolicy == nil || c.trafficProfile(f)) {
				found = true
			}
		}
		if !found {
			return failure("configuration_rejected")
		}
	}
	return nil
}
