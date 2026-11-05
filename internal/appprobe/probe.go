// Package appprobe runs opt-in, node-local application checks outside the
// management/metrics loops. Remote commands never supply a network destination.
package appprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type Check struct {
	Scope          string `json:"scope,omitempty"`
	Name           string `json:"name"`
	Listener       string `json:"listener"`
	Hostname       string `json:"hostname"`
	PathPrefix     string `json:"path_prefix"`
	Path           string `json:"path"`
	Scheme         string `json:"scheme"`
	Address        string `json:"address"`
	Port           uint32 `json:"port"`
	CAFile         string `json:"ca_file,omitempty"`
	ExpectedStatus uint32 `json:"expected_status"`
	ResponseHeader string `json:"response_header,omitempty"`
	ResponseValue  string `json:"response_value,omitempty"`
	client         *http.Client
	policyDigest   string
}
type snapshot struct {
	Apps       []domain.HTTPApplication
	ArtifactID string
	Generation string
}
type Runner struct {
	checks  []Check
	input   chan snapshot
	mu      sync.RWMutex
	results []domain.ApplicationProbe
}

func Load(ctx context.Context, path string) (*Runner, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("probe policy requires an absolute local path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("probe policy unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<10 {
		return nil, errors.New("invalid probe policy file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return nil, errors.New("invalid probe policy file")
	}
	var policy struct {
		Checks []Check `json:"checks"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&policy) != nil || d.Decode(new(any)) != io.EOF || len(policy.Checks) == 0 || len(policy.Checks) > 32 {
		return nil, errors.New("invalid probe policy")
	}
	names := map[string]bool{}
	for i := range policy.Checks {
		c := &policy.Checks[i]
		if c.Scope == "" {
			c.Scope = "entrypoint"
		}
		a := domain.HTTPApplication{ID: domain.NewID(), Name: "Probe", Listener: c.Listener, Hostname: c.Hostname, PathPrefix: c.PathPrefix, HealthPath: "/", Algorithm: "roundrobin", Servers: []domain.ConfigurationServer{{Name: "probe", Address: "localhost", Port: 80, Weight: 100}}}
		if !domain.ValidRuntimeName(c.Name) || names[c.Name] || (c.Scope != "node" && c.Scope != "entrypoint") || !a.Valid() || !domain.HTTPPath(c.Path) || !strings.HasPrefix(c.Path, c.PathPrefix) || (c.Scheme != "http" && c.Scheme != "https") || !domain.ConfigurationAddress(c.Address) || c.Port == 0 || c.Port > 65535 || c.ExpectedStatus < 200 || c.ExpectedStatus > 399 || len(c.ResponseValue) > 128 || strings.ContainsAny(c.ResponseValue, "\r\n") || (c.ResponseHeader == "") != (c.ResponseValue == "") {
			return nil, errors.New("invalid probe check")
		}
		for _, r := range c.ResponseHeader {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return nil, errors.New("invalid probe response header")
			}
		}
		if len(c.ResponseHeader) > 64 {
			return nil, errors.New("invalid probe response header")
		}
		names[c.Name] = true
		var customTrust []byte
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if c.CAFile != "" {
			if c.Scheme != "https" || !filepath.IsAbs(c.CAFile) {
				return nil, errors.New("invalid probe CA path")
			}
			f, err := os.Open(c.CAFile)
			if err != nil {
				return nil, errors.New("probe CA unavailable")
			}
			b, err := io.ReadAll(io.LimitReader(f, (128<<10)+1))
			_ = f.Close()
			if err != nil || len(b) > 128<<10 || !roots.AppendCertsFromPEM(b) {
				return nil, errors.New("invalid probe CA")
			}
			customTrust = b
		}
		// Resolve only local policy destinations, once per agent session. Dial
		// the pinned IP; requests cannot select a host, proxy, redirect or port.
		lookup, cancel := context.WithTimeout(ctx, 2*time.Second)
		ips, err := net.DefaultResolver.LookupNetIP(lookup, "ip", c.Address)
		cancel()
		if err != nil || len(ips) == 0 {
			return nil, errors.New("probe destination unavailable")
		}
		ip := ips[0]
		if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
			return nil, errors.New("invalid probe destination")
		}
		address := net.JoinHostPort(ip.String(), strconv.Itoa(int(c.Port)))
		policyBytes, _ := json.Marshal(struct {
			Check         Check
			PinnedAddress string
			Roots         [][]byte
			CustomTrust   []byte
		}{*c, address, roots.Subjects(), customTrust})
		digest := sha256.Sum256(policyBytes)
		c.policyDigest = hex.EncodeToString(digest[:])
		transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 8 << 10, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: c.Hostname}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
		}}
		c.client = &http.Client{Transport: transport, Timeout: 1500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Runner{checks: policy.Checks, input: make(chan snapshot, 1)}, nil
}
func (r *Runner) Update(d *domain.Discovery) {
	var apps []domain.HTTPApplication
	s := snapshot{}
	if d != nil && d.Configuration != nil && d.Configuration.Document != nil {
		apps = d.Configuration.Document.Applications
		s.ArtifactID, s.Generation = d.Configuration.ArtifactID, d.Generation
	}
	copy := make([]domain.HTTPApplication, len(apps))
	for i, a := range apps {
		copy[i] = a.Canonical()
	}
	select {
	case <-r.input:
	default:
	}
	select {
	case r.input <- snapshot{Apps: copy, ArtifactID: s.ArtifactID, Generation: s.Generation}:
	default:
	}
}
func (r *Runner) Grants() []domain.ApplicationProbeGrant {
	out := make([]domain.ApplicationProbeGrant, 0, len(r.checks))
	for _, c := range r.checks {
		out = append(out, domain.ApplicationProbeGrant{Name: c.Name, Scope: c.Scope, Listener: c.Listener, Hostname: c.Hostname, PathPrefix: c.PathPrefix, Path: c.Path, Scheme: c.Scheme, ExpectedStatus: c.ExpectedStatus, PolicyDigest: c.policyDigest})
	}
	return out
}
func (r *Runner) Snapshot() []domain.ApplicationProbe {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]domain.ApplicationProbe{}, r.results...)
}
func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var current snapshot
	for {
		select {
		case <-ctx.Done():
			return
		case current = <-r.input:
			if len(r.Snapshot()) > 0 {
				continue
			}
		case <-ticker.C:
		}
		results := r.checkSnapshot(ctx, current)
		r.mu.Lock()
		r.results = results
		r.mu.Unlock()
	}
}

// Check has a four-second total budget and at most four concurrent requests.
func (r *Runner) Check(parent context.Context, apps []domain.HTTPApplication) []domain.ApplicationProbe {
	return r.checkSnapshot(parent, snapshot{Apps: apps})
}
func (r *Runner) checkSnapshot(parent context.Context, current snapshot) []domain.ApplicationProbe {
	apps := current.Apps
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	type job struct {
		check    Check
		app      domain.HTTPApplication
		conflict bool
	}
	jobs := []job{}
	for _, c := range r.checks {
		for _, a := range apps {
			if c.Scope == "node" && (!domain.ValidDigest(current.ArtifactID) || !domain.ValidDigest(current.Generation)) {
				continue
			}
			if a.Disabled || a.Listener != c.Listener || a.Hostname != c.Hostname || a.PathPrefix != c.PathPrefix {
				continue
			}
			conflict := false
			for _, other := range apps {
				if other.Listener == a.Listener && other.Hostname == a.Hostname && len(other.PathPrefix) > len(a.PathPrefix) && strings.HasPrefix(c.Path, other.PathPrefix) {
					conflict = true
				}
			}
			jobs = append(jobs, job{c, a, conflict})
		}
	}
	results := make([]domain.ApplicationProbe, len(jobs))
	work := make(chan int, len(jobs))
	for i := range jobs {
		work <- i
	}
	close(work)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for i := range work {
				j := jobs[i]
				results[i] = probe(ctx, j.check, j.app, j.conflict)
				results[i].ArtifactID, results[i].Generation = current.ArtifactID, current.Generation
			}
		})
	}
	wg.Wait()
	return results
}
func probe(ctx context.Context, c Check, a domain.HTTPApplication, conflict bool) (p domain.ApplicationProbe) {
	start := time.Now()
	p = domain.ApplicationProbe{ApplicationID: a.ID, SpecDigest: a.Digest(), Name: c.Name, Scheme: c.Scheme, Path: c.Path, ExpectedStatus: c.ExpectedStatus, ObservedAtMS: start.UnixMilli(), Scope: c.Scope, PolicyDigest: c.policyDigest}
	defer func() { p.DurationMS = min(time.Since(start).Milliseconds(), 5000) }()
	if conflict {
		p.ErrorCode = "route_conflict"
		return
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, (&url.URL{Scheme: c.Scheme, Host: net.JoinHostPort(c.Hostname, strconv.Itoa(int(c.Port))), Path: c.Path}).String(), nil)
	request.Host = c.Hostname
	response, err := c.client.Do(request)
	if err != nil {
		p.ErrorCode = "connection_failed"
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			p.ErrorCode = "tls_failed"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			p.ErrorCode = "deadline"
		}
		return
	}
	defer response.Body.Close()
	p.StatusCode = uint32(response.StatusCode)
	if response.TLS != nil && len(response.TLS.VerifiedChains) > 0 && len(response.TLS.PeerCertificates) > 0 {
		cert := response.TLS.PeerCertificates[0]
		sum := sha256.Sum256(cert.Raw)
		p.TLSVerified = true
		p.CertificateSHA256 = hex.EncodeToString(sum[:])
		p.CertificateNotAfterMS = cert.NotAfter.UnixMilli()
	}
	n, err := io.Copy(io.Discard, io.LimitReader(response.Body, (64<<10)+1))
	switch {
	case err != nil:
		p.ErrorCode = "connection_failed"
	case n > 64<<10:
		p.ErrorCode = "response_too_large"
	case p.StatusCode != c.ExpectedStatus:
		p.ErrorCode = "response_status"
	case c.ResponseHeader != "" && response.Header.Get(c.ResponseHeader) != c.ResponseValue:
		p.ErrorCode = "response_mismatch"
	}
	return
}
