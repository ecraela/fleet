// Package acmeissuer owns node-local ACME accounts, keys and DNS-01 authority.
// Its network destinations and filesystem paths come only from local policy.
package acmeissuer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type Profile struct {
	Issuer         string                 `json:"issuer"`
	Listener       string                 `json:"listener"`
	Environment    string                 `json:"environment"`
	DirectoryURL   string                 `json:"directory_url"`
	TrustFile      string                 `json:"trust_file,omitempty"`
	TermsAgreed    bool                   `json:"terms_agreed"`
	DNSServer      string                 `json:"dns_server"`
	Resolver       string                 `json:"resolver"`
	ValidationZone string                 `json:"validation_zone"`
	KeyName        string                 `json:"key_name"`
	KeyFile        string                 `json:"key_file"`
	Challenges     []domain.ACMEChallenge `json:"challenges"`
	secret         string
	roots          *x509.CertPool
	origin         string
	grant          domain.ACMEGrant
}
type Policy struct {
	CertificateDir string    `json:"certificate_dir"`
	Profiles       []Profile `json:"profiles"`
}
type Asset struct {
	Reference   string `json:"reference"`
	Path        string `json:"path"`
	CreatedAtMS int64  `json:"created_at_ms"`
}
type Runner struct {
	policy Policy
	state  string
	mu     sync.RWMutex
	assets []Asset
}

func privateRead(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("local ACME file unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit || info.Mode().Perm()&0007 != 0 {
		return nil, errors.New("local ACME file must be bounded and private")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(raw) > int(limit) {
		return nil, errors.New("local ACME file too large")
	}
	return raw, err
}
func readJSON(path string, value any, limit int64) error {
	raw, err := privateRead(path, limit)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid local ACME state")
	}
	return nil
}
func atomicFile(path string, raw []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".acme-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err == nil {
		d, e := os.Open(filepath.Dir(path))
		if e == nil {
			err = d.Sync()
			d.Close()
		} else {
			err = e
		}
	}
	return err
}
func saveJSON(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return atomicFile(path, raw, 0600)
}
func pinDNS(ctx context.Context, address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 1 || number > 65535 {
		return "", errors.New("DNS server must include a port")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return "", errors.New("DNS server unavailable")
	}
	ip := ips[0]
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return "", errors.New("unsupported DNS server address")
	}
	return net.JoinHostPort(ip.String(), port), nil
}
func Load(ctx context.Context, path, state string) (*Runner, error) {
	var policy Policy
	if err := readJSON(path, &policy, 32<<10); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(policy.CertificateDir) || len(policy.Profiles) == 0 || len(policy.Profiles) > 4 {
		return nil, errors.New("invalid local ACME policy")
	}
	state = filepath.Join(state, "acme")
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{state, 0700}, {policy.CertificateDir, 0750}} {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return nil, errors.New("ACME directory unavailable")
		}
		info, err := os.Lstat(d.path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0007 != 0 {
			return nil, errors.New("ACME directory must be private and real")
		}
	}
	seen := map[string]bool{}
	for i := range policy.Profiles {
		p := &policy.Profiles[i]
		u, err := url.Parse(p.DirectoryURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !p.TermsAgreed || !domain.DNSHostname(p.ValidationZone) || !domain.DNSHostname(p.KeyName) || !filepath.IsAbs(p.KeyFile) {
			return nil, errors.New("invalid ACME issuer or DNS authority")
		}
		p.origin = u.Scheme + "://" + u.Host
		raw, err := privateRead(p.KeyFile, 4096)
		if err != nil {
			return nil, errors.New("DNS-01 credential unavailable")
		}
		p.secret = strings.TrimSpace(string(raw))
		decoded, err := base64.StdEncoding.DecodeString(p.secret)
		if err != nil || len(decoded) < 32 || len(decoded) > 128 {
			return nil, errors.New("invalid DNS-01 credential")
		}
		p.roots, err = x509.SystemCertPool()
		if err != nil {
			p.roots = x509.NewCertPool()
		}
		if p.TrustFile != "" {
			if !filepath.IsAbs(p.TrustFile) {
				return nil, errors.New("ACME trust path must be absolute")
			}
			file, e := os.Open(p.TrustFile)
			if e != nil {
				return nil, errors.New("ACME trust unavailable")
			}
			raw, e := io.ReadAll(io.LimitReader(file, 128<<10+1))
			file.Close()
			if e != nil || len(raw) > 128<<10 || !p.roots.AppendCertsFromPEM(raw) {
				return nil, errors.New("ACME trust unavailable")
			}
		}
		local, stop := context.WithTimeout(ctx, 3*time.Second)
		p.DNSServer, err = pinDNS(local, p.DNSServer)
		if err == nil {
			p.Resolver, err = pinDNS(local, p.Resolver)
		}
		stop()
		if err != nil {
			return nil, err
		}
		slices.SortFunc(p.Challenges, func(a, b domain.ACMEChallenge) int { return strings.Compare(a.Hostname, b.Hostname) })
		for _, c := range p.Challenges {
			if !strings.HasSuffix(c.Target, "."+p.ValidationZone) || c.Target == "_acme-challenge."+c.Hostname {
				return nil, errors.New("DNS-01 target must be inside the delegated validation zone")
			}
		}
		safe := domain.ACMEGrant{Issuer: p.Issuer, Listener: p.Listener, Environment: p.Environment, Challenges: p.Challenges}
		raw, _ = json.Marshal(struct {
			Grant     domain.ACMEGrant
			Directory string
			Zone      string
		}{safe, p.DirectoryURL, p.ValidationZone})
		sum := sha256.Sum256(raw)
		safe.PolicyDigest = hex.EncodeToString(sum[:])
		p.grant = safe
		if !domain.ValidACMEGrants([]domain.ACMEGrant{safe}) || seen[p.Issuer+"/"+p.Listener] {
			return nil, errors.New("invalid or duplicate ACME profile")
		}
		seen[p.Issuer+"/"+p.Listener] = true
	}
	r := &Runner{policy: policy, state: state, assets: []Asset{}}
	index := filepath.Join(state, "assets.json")
	if _, err := os.Lstat(index); err == nil {
		if readJSON(index, &r.assets, 64<<10) != nil || len(r.assets) > 128 {
			return nil, errors.New("invalid ACME inventory")
		}
	}
	for _, a := range r.assets {
		if !strings.HasPrefix(a.Reference, "acme-") || !domain.ValidRuntimeName(a.Reference) || a.Path != filepath.Join(policy.CertificateDir, a.Reference+".pem") {
			return nil, errors.New("invalid ACME asset path")
		}
	}
	return r, nil
}
func (r *Runner) Grants() []domain.ACMEGrant {
	out := []domain.ACMEGrant{}
	for _, p := range r.policy.Profiles {
		g := p.grant
		g.Challenges = append([]domain.ACMEChallenge{}, g.Challenges...)
		out = append(out, g)
	}
	return out
}
func (r *Runner) Assets() []Asset {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]Asset{}, r.assets...)
	slices.SortFunc(out, func(a, b Asset) int {
		if a.CreatedAtMS > b.CreatedAtMS {
			return -1
		}
		if a.CreatedAtMS < b.CreatedAtMS {
			return 1
		}
		return strings.Compare(a.Reference, b.Reference)
	})
	return out
}
func (r *Runner) profile(j domain.ACMEJob) *Profile {
	for i := range r.policy.Profiles {
		p := &r.policy.Profiles[i]
		if p.Issuer == j.Issuer && p.Listener == j.Listener && p.grant.PolicyDigest == j.PolicyDigest && p.grant.Covers(j.Hostnames) {
			return p
		}
	}
	return nil
}

type boundedTransport struct {
	base   http.RoundTripper
	origin string
}
type limitedBody struct {
	io.Reader
	io.Closer
}

func (t boundedTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
func (t boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme+"://"+req.URL.Host != t.origin || req.URL.User != nil {
		return nil, errors.New("ACME origin changed")
	}
	response, err := t.base.RoundTrip(req)
	if err == nil {
		response.Body = limitedBody{io.LimitReader(response.Body, 512<<10), response.Body}
	}
	return response, err
}
func (p *Profile) client() *http.Client {
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: p.roots}, MaxConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 16 << 10}
	return &http.Client{Timeout: 20 * time.Second, Transport: boundedTransport{transport, p.origin}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
