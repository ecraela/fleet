package accesslog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const MaxPath = 512

// PathPolicy is local operator authority. It is never populated by the manager.
// Sanitized mode exports only configured templates; exact mode is explicit.
type PathPolicy struct {
	Version int      `json:"version"`
	Mode    string   `json:"mode"`
	Routes  []string `json:"routes,omitempty"`
	Redact  []string `json:"redact,omitempty"`
	digest  string
}

func LoadPathPolicy(path string) (*PathPolicy, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Lstat(path)
	if !filepath.IsAbs(path) || err != nil || !f.Mode().IsRegular() || f.Mode().Perm()&0022 != 0 || f.Size() > 32768 {
		return nil, errors.New("request path policy must be a bounded local regular file writable only by its owner")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("request path policy unavailable")
	}
	return parsePathPolicy(raw)
}

func parsePathPolicy(raw []byte) (*PathPolicy, error) {
	p := &PathPolicy{}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 32768 || d.Decode(p) != nil || d.Decode(new(any)) != io.EOF || p.Version != 1 ||
		(p.Mode != "sanitized" && p.Mode != "exact" && p.Mode != "off") || len(p.Routes)+len(p.Redact) > 128 ||
		p.Mode != "sanitized" && len(p.Routes) != 0 || p.Mode != "exact" && len(p.Redact) != 0 {
		return nil, errors.New("invalid request path policy")
	}
	seen := map[string]bool{}
	for _, patterns := range [][]string{p.Routes, p.Redact} {
		for _, pattern := range patterns {
			if !validTemplate(pattern) || seen[pattern] {
				return nil, errors.New("invalid or repeated request path template")
			}
			seen[pattern] = true
		}
	}
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	p.digest = hex.EncodeToString(sum[:])
	return p, nil
}

func ValidPath(path string) bool {
	if len(path) == 0 || len(path) > MaxPath || path[0] != '/' || strings.Contains(path, "//") {
		return false
	}
	for _, c := range path {
		if c < 33 || c > 126 || strings.ContainsRune("?#%\\\"'<>|", c) {
			return false
		}
	}
	for _, s := range strings.Split(path, "/") {
		if s == "." || s == ".." {
			return false
		}
	}
	return true
}

func validTemplate(pattern string) bool {
	if !ValidPath(pattern) || len(strings.Split(pattern, "/")) > 65 {
		return false
	}
	segments := strings.Split(pattern, "/")
	for i, s := range segments {
		if !strings.ContainsAny(s, ":*") {
			continue
		}
		if len(s) < 2 || s[0] != ':' && s[0] != '*' || s[0] == '*' && i != len(segments)-1 {
			return false
		}
		for _, c := range s[1:] {
			if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}

func matchesPath(pattern, path string) bool {
	a, b := strings.Split(pattern, "/"), strings.Split(path, "/")
	for i, s := range a {
		if i >= len(b) {
			return false
		}
		if strings.HasPrefix(s, "*") {
			return b[i] != ""
		}
		if strings.HasPrefix(s, ":") {
			if b[i] == "" {
				return false
			}
		} else if s != b[i] {
			return false
		}
	}
	return len(a) == len(b)
}

func (p *PathPolicy) capture(raw string) (string, string) {
	if p == nil || p.Mode == "off" {
		return "", "withheld"
	}
	// Defense in depth: HAProxy supplies path, never URL/query. Do not decode
	// query or fragment bytes, and fail closed on ambiguous/double encodings.
	raw = strings.SplitN(strings.SplitN(raw, "?", 2)[0], "#", 2)[0]
	if strings.Contains(strings.ToLower(raw), "%2f") {
		return "", "invalid"
	}
	path, err := url.PathUnescape(raw)
	if err != nil || !ValidPath(path) {
		return "", "invalid"
	}
	patterns := p.Routes
	if p.Mode == "exact" {
		patterns = p.Redact
	}
	for _, pattern := range patterns {
		if matchesPath(pattern, path) {
			return pattern, "template"
		}
	}
	if p.Mode == "exact" {
		return path, "exact"
	}
	return "", "unmatched"
}

func ValidPathEvidence(path, kind string) bool {
	switch kind {
	case "template":
		return validTemplate(path)
	case "exact":
		return ValidPath(path)
	case "", "unavailable", "withheld", "unmatched", "invalid":
		return path == ""
	}
	return false
}
