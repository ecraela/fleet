// Package agent enrolls one node and exports bounded, read-only observations.
package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/acmeissuer"
	"github.com/ecraela/fleet/internal/buildinfo"
	"github.com/ecraela/fleet/internal/dataplane"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/pki"
	"github.com/ecraela/fleet/internal/telemetry"
)

const maxIdentityBytes = 128 << 10

type Config struct {
	ConfigurationOwner        string
	metricsHealth             *atomic.Pointer[metricsObservation]
	AccessLogSocket           string
	RequestPathPolicyFile     string
	RequestGeoPolicyFile      string
	TrafficPolicyFile         string
	traffic                   *dataplane.TrafficPolicy
	DataplaneSocket           string
	DataplaneCredentialsFile  string
	TLSPolicyFile             string
	ProbePolicyFile           string
	ACMEPolicyFile            string
	issuer                    *acmeissuer.Runner
	AllowConfigurationControl bool
	RuntimeSocket             string
	ConfigFiles               []string
	AllowRuntimeControl       bool
	ServerURL                 string
	IngestURL                 string
	ServerCA                  string
	EnrollmentTokenFile       string
	StateDir                  string
	Name                      string
	MetricsURL                string
	Interval                  time.Duration
}

type Agent struct{ Config Config }

type identity struct {
	Node           domain.Node `json:"node"`
	CertificatePEM string      `json:"certificate_pem"`
	CAPEM          string      `json:"ca_pem"`
	KeyPEM         string      `json:"key_pem"`
	ServerURL      string      `json:"server_url"`
	IngestURL      string      `json:"ingest_url"`
}

// Run returns when cancelled or when local identity is unusable. Transient
// scrape/ingestion failures discard the current batch; there is no disk spool
// or unbounded queue. A cancelled Run never enrolls again on the next start.
func (a *Agent) Run(ctx context.Context) error {
	cfg, err := normalize(a.Config)
	if err != nil {
		return err
	}
	if err := secureDir(cfg.StateDir); err != nil {
		return err
	}
	cfg.metricsHealth = &atomic.Pointer[metricsObservation]{}
	lock, err := lockState(cfg.StateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	trust, err := serverTrust(cfg.ServerCA)
	if err != nil {
		return err
	}
	statePath := filepath.Join(cfg.StateDir, "identity.json")
	state, err := readState(statePath)
	if errors.Is(err, os.ErrNotExist) {
		state, err = enroll(ctx, cfg, trust)
		if err != nil {
			return err
		}
		if err = saveState(statePath, state); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if state.ServerURL != cfg.ServerURL || state.IngestURL != cfg.IngestURL {
		return errors.New("stored node identity is bound to different management endpoints")
	}
	client, leaf, err := identityClient(state, trust)
	if err != nil {
		return err
	}
	defer func() { client.CloseIdleConnections() }()
	if cfg.ACMEPolicyFile != "" {
		if !cfg.AllowConfigurationControl || cfg.TLSPolicyFile == "" {
			return errors.New("ACME requires local TLS and configuration authority")
		}
		cfg.issuer, err = acmeissuer.Load(ctx, cfg.ACMEPolicyFile, cfg.StateDir)
		if err != nil {
			return errors.New("local ACME policy unavailable")
		}
	}
	controlCtx, cancelControl := context.WithCancel(ctx)
	if cfg.TrafficPolicyFile != "" {
		cfg.traffic, err = dataplane.LoadTrafficPolicy(cfg.TrafficPolicyFile, cfg.RequestGeoPolicyFile)
		if err != nil {
			cancelControl()
			return err
		}
	}
	if cfg.AccessLogSocket != "" {
		policy, err := accesslog.LoadPathPolicy(cfg.RequestPathPolicyFile)
		if err != nil {
			cancelControl()
			return err
		}
		geo, err := accesslog.LoadGeoPolicy(cfg.RequestGeoPolicyFile)
		if err != nil {
			cancelControl()
			return err
		}
		defer geo.Close()
		receiver, err := accesslog.OpenWithPolicies(cfg.AccessLogSocket, policy, geo)
		if err != nil {
			cancelControl()
			return err
		}
		logsDone := make(chan struct{})
		go func() { defer close(logsDone); accessLoop(controlCtx, cfg, trust, receiver) }()
		defer func() { cancelControl(); <-logsDone }()
	}
	controlDone := make(chan struct{})
	go func() { defer close(controlDone); controlLoop(controlCtx, cfg, trust) }()
	if cfg.issuer != nil {
		issuerDone := make(chan struct{})
		go func() { defer close(issuerDone); acmeLoop(controlCtx, cfg, trust) }()
		defer func() { cancelControl(); <-issuerDone }()
	}

	defer func() { cancelControl(); <-controlDone }()
	collector := &telemetry.Collector{URL: cfg.MetricsURL}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	var nextRenew time.Time
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Until(leaf.NotAfter) < 24*time.Hour && !time.Now().Before(nextRenew) {
			nextRenew = time.Now().Add(time.Minute)
			renewed, renewErr := renew(ctx, cfg, state, client, trust)
			if renewErr == nil {
				if err := saveState(statePath, renewed); err != nil {
					return err
				}
				client.CloseIdleConnections()
				state = renewed
				client, leaf, err = identityClient(state, trust)
				if err != nil {
					return err
				}
			}
			if !time.Now().Before(leaf.NotAfter) {
				return errors.New("node certificate expired; restore connectivity before renewal")
			}
		}
		// One short deadline covers scraping and both delivery attempts. A slow
		// backend cannot turn the next observation into a backlog of stale data.
		batchCtx, cancelBatch := context.WithTimeout(ctx, 5*time.Second)
		batch, scrapeErr := collector.Collect(batchCtx, time.Now())
		cfg.metricsHealth.Store(&metricsObservation{At: time.Now(), OK: scrapeErr == nil})
		if batch != nil {
			payload, err := telemetry.Encode(batch)
			if err != nil {
				cancelBatch()
				return err
			}
			if err := export(batchCtx, client, cfg.IngestURL, payload); err != nil {
				collector.ExportFailures++
			} else if scrapeErr == nil {
				// Private, local readiness evidence for the host installer. It contains
				// no credentials and never grants authority to the manager.
				_ = saveJSON(filepath.Join(cfg.StateDir, "agent-health.json"), struct {
					NodeID     string         `json:"node_id"`
					ObservedAt time.Time      `json:"observed_at"`
					Build      buildinfo.Info `json:"build"`
				}{state.Node.ID, time.Now().UTC(), buildinfo.Current()})
			}
		}
		cancelBatch()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func normalize(c Config) (Config, error) {
	c, err := normalizeLocal(c)
	if err != nil {
		return c, err
	}
	if c.StateDir == "" {
		return c, errors.New("state directory is required")
	}
	if c.Name == "" || len(c.Name) > 128 {
		return c, errors.New("node name must contain 1 to 128 bytes")
	}
	c.ServerURL, err = endpoint(c.ServerURL, "")
	if err != nil {
		return c, err
	}
	c.IngestURL, err = endpoint(c.IngestURL, "/v1/metrics/write")
	return c, err
}

func normalizeLocal(c Config) (Config, error) {
	if c.RequestGeoPolicyFile != "" && (!filepath.IsAbs(c.RequestGeoPolicyFile) || c.AccessLogSocket == "") {
		return c, errors.New("geography requires an absolute local policy and request-log socket")
	}
	if c.TrafficPolicyFile != "" && (!filepath.IsAbs(c.TrafficPolicyFile) || !c.AllowConfigurationControl || c.AccessLogSocket == "") {
		return c, errors.New("traffic rules require a local policy, configuration authority and private request log socket")
	}
	if c.ConfigurationOwner != "" && c.ConfigurationOwner != "manager" && c.ConfigurationOwner != "external" {
		return c, errors.New("configuration owner must be manager or external")
	}
	if c.ConfigurationOwner == "external" && c.AllowConfigurationControl {
		return c, errors.New("configuration edits require resolving the external writer before granting manager ownership")
	}
	if c.RequestPathPolicyFile != "" && (!filepath.IsAbs(c.RequestPathPolicyFile) || c.AccessLogSocket == "") {
		return c, errors.New("request paths require a local policy and request-log socket")
	}
	if c.AccessLogSocket != "" && (!filepath.IsAbs(c.AccessLogSocket) || c.DataplaneSocket == "") {
		return c, errors.New("request telemetry requires a local Unix log socket and configuration discovery")
	}
	if c.ACMEPolicyFile != "" && (!filepath.IsAbs(c.ACMEPolicyFile) || !c.AllowConfigurationControl || c.TLSPolicyFile == "") {
		return c, errors.New("ACME requires an absolute local policy and TLS configuration authority")
	}
	if c.ProbePolicyFile != "" && (!filepath.IsAbs(c.ProbePolicyFile) || c.DataplaneSocket == "") {
		return c, errors.New("application probes require an absolute local policy path and configuration discovery")
	}
	if c.DataplaneSocket != "" || c.DataplaneCredentialsFile != "" || c.AllowConfigurationControl || c.TLSPolicyFile != "" {
		if !filepath.IsAbs(c.DataplaneSocket) || !filepath.IsAbs(c.DataplaneCredentialsFile) || c.RuntimeSocket == "" || len(c.ConfigFiles) == 0 {
			return c, errors.New("persistent configuration requires a local Data Plane API socket, private credentials, Runtime API socket and explicit configuration files")
		}
		if c.TLSPolicyFile != "" && (!filepath.IsAbs(c.TLSPolicyFile) || !c.AllowConfigurationControl) {
			return c, errors.New("TLS policy requires an absolute local path and configuration control")
		}
	}

	if (c.AllowRuntimeControl || len(c.ConfigFiles) > 0) && c.RuntimeSocket == "" {
		return c, errors.New("runtime socket is required for configuration discovery and control")
	}
	if c.AllowRuntimeControl && len(c.ConfigFiles) == 0 {
		return c, errors.New("configuration files are required before runtime management can be enabled")
	}
	if c.RuntimeSocket != "" && !filepath.IsAbs(c.RuntimeSocket) {
		return c, errors.New("runtime socket must be an absolute local path")
	}
	if len(c.ConfigFiles) > 16 {
		return c, errors.New("at most 16 configuration files are supported")
	}
	for _, path := range c.ConfigFiles {
		if !filepath.IsAbs(path) {
			return c, errors.New("configuration files must use absolute local paths")
		}
	}
	if c.Interval == 0 {
		c.Interval = time.Second
	}
	if c.Interval < time.Second {
		return c, errors.New("scrape interval must be at least one second")
	}
	if c.MetricsURL == "" {
		c.MetricsURL = "http://127.0.0.1:8405/metrics"
	}
	u, err := url.Parse(c.MetricsURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, errors.New("invalid HAProxy metrics URL")
	}
	return c, nil
}

func endpoint(raw, path string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/" && u.Path != path) {
		return "", errors.New("management endpoints require HTTPS and the expected path")
	}
	u.Path = path
	return strings.TrimSuffix(u.String(), "/"), nil
}

func serverTrust(path string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("cannot read server CA")
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, errors.New("invalid server CA")
		}
	}
	return pool, nil
}

func httpClient(config *tls.Config) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: config, MaxIdleConns: 2, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 3 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func enroll(ctx context.Context, c Config, trust *x509.CertPool) (*identity, error) {
	token, err := readPrivate(c.EnrollmentTokenFile, 4096)
	if err != nil {
		return nil, errors.New("cannot read private enrollment token file")
	}
	secret := strings.TrimSpace(string(token))
	if len(secret) < 16 || len(secret) > 4096 {
		return nil, errors.New("invalid enrollment token file")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	csr, err := csrPEM(key)
	if err != nil {
		return nil, err
	}
	client := httpClient(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: trust})
	defer client.CloseIdleConnections()
	state, err := identityRequest(ctx, client, c.ServerURL+"/v1/enroll", map[string]string{"token": secret, "name": c.Name, "csr_pem": csr})
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	state.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	state.ServerURL = c.ServerURL
	state.IngestURL = c.IngestURL
	if _, _, err := identityClient(state, trust); err != nil {
		return nil, err
	}
	return state, nil
}

func renew(ctx context.Context, c Config, old *identity, client *http.Client, trust *x509.CertPool) (*identity, error) {
	block, _ := pem.Decode([]byte(old.KeyPEM))
	if block == nil {
		return nil, errors.New("invalid node private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ecdsaKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("invalid node private key type")
	}
	csr, err := csrPEM(ecdsaKey)
	if err != nil {
		return nil, err
	}
	state, err := identityRequest(ctx, client, c.ServerURL+"/v1/identity/renew", map[string]string{"csr_pem": csr})
	if err != nil {
		return nil, err
	}
	if state.Node.ID != old.Node.ID || state.Node.OrganizationID != old.Node.OrganizationID || state.Node.ClusterID != old.Node.ClusterID || state.CAPEM != old.CAPEM {
		return nil, errors.New("renewal changed node identity or CA")
	}
	state.KeyPEM = old.KeyPEM
	state.ServerURL = old.ServerURL
	state.IngestURL = old.IngestURL
	if _, _, err := identityClient(state, trust); err != nil {
		return nil, err
	}
	return state, nil
}

func csrPEM(key *ecdsa.PrivateKey) (string, error) {
	b, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: b})), nil
}

func identityRequest(ctx context.Context, client *http.Client, endpoint string, body map[string]string) (*identity, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("invalid identity endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("node identity request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("node identity request returned status %d", resp.StatusCode)
	}
	b, err = io.ReadAll(io.LimitReader(resp.Body, maxIdentityBytes+1))
	if err != nil || len(b) > maxIdentityBytes {
		return nil, errors.New("invalid identity response size")
	}
	var state identity
	if json.Unmarshal(b, &state) != nil {
		return nil, errors.New("invalid identity response")
	}
	return &state, nil
}

func identityClient(state *identity, trust *x509.CertPool) (*http.Client, *x509.Certificate, error) {
	pair, err := tls.X509KeyPair([]byte(state.CertificatePEM), []byte(state.KeyPEM))
	if err != nil || len(pair.Certificate) != 1 {
		return nil, nil, errors.New("invalid node certificate or key")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, errors.New("invalid node certificate")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(state.CAPEM)) {
		return nil, nil, errors.New("invalid node CA")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, nil, errors.New("node certificate cannot be verified")
	}
	node, err := pki.Identity(leaf)
	if err != nil || node.ID != state.Node.ID || node.OrganizationID != state.Node.OrganizationID || node.ClusterID != state.Node.ClusterID {
		return nil, nil, errors.New("certificate does not match enrolled node identity")
	}
	rootPool := trust.Clone()
	rootPool.AppendCertsFromPEM([]byte(state.CAPEM))
	return httpClient(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: rootPool, Certificates: []tls.Certificate{pair}}), leaf, nil
}

func export(ctx context.Context, client *http.Client, endpoint string, payload []byte) error {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		req.Header.Set("Content-Encoding", "snappy")
		req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
		req.Header.Set("User-Agent", "Converge-HAProxy-Agent/0.1")
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			if resp.StatusCode != 429 && resp.StatusCode < 500 {
				return fmt.Errorf("telemetry rejected with status %d", resp.StatusCode)
			}
		}
		if attempt == 0 {
			var jitter [1]byte
			_, _ = rand.Read(jitter[:])
			timer := time.NewTimer(time.Duration(100+int(jitter[0])*200/255) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return errors.New("telemetry delivery failed")
}

func secureDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("state directory must be a private directory with mode 0700")
	}
	return nil
}

func lockState(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "agent.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("cannot open agent lock")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("agent lock is not private")
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, errors.New("another agent is using this state directory")
	}
	return f, nil
}

func readPrivate(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > max {
		return nil, errors.New("credential file must be private, regular and bounded")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if len(b) > int(max) {
		return nil, errors.New("credential file too large")
	}
	return b, err
}

func readState(path string) (*identity, error) {
	b, err := readPrivate(path, maxIdentityBytes)
	if err != nil {
		return nil, err
	}
	var state identity
	if err := json.Unmarshal(b, &state); err != nil {
		return nil, errors.New("invalid stored node identity")
	}
	return &state, nil
}

func saveState(path string, state *identity) error {
	return saveJSON(path, state)
}
func saveJSON(path string, state any) error {
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return errors.New("refusing unsafe identity destination")
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".identity-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
