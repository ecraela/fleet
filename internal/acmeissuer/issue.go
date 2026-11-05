package acmeissuer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"golang.org/x/crypto/acme"
)

type savedAccount struct {
	URI string `json:"uri"`
}
type savedChallenge struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type savedOrder struct {
	URI        string           `json:"uri"`
	Challenges []savedChallenge `json:"challenges"`
}
type issueError struct {
	code  string
	retry int
}

func (e issueError) Error() string { return e.code }
func problem(code string) error    { return issueError{code: code} }
func caError(err error) error {
	if delay, ok := acme.RateLimit(err); ok {
		seconds := int(delay.Seconds())
		return issueError{code: "acme_rate_limited", retry: max(60, min(172800, seconds))}
	}
	return problem("acme_unavailable")
}

// Resolve the account by its retained key. A test CA can lose its account
// database on restart; retrying a stale KID cannot repair that state.
func issuerAccount(ctx context.Context, p *Profile, client *acme.Client, path string) (bool, error) {
	previous := savedAccount{}
	if _, e := os.Lstat(path); e == nil {
		if readJSON(path, &previous, 4096) != nil || !sameOrigin(previous.URI, p.origin) {
			return false, problem("local_storage_failed")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return false, problem("local_storage_failed")
	}
	var account *acme.Account
	var err error
	if previous.URI != "" {
		account, err = client.GetReg(ctx, previous.URI)
		if err != nil && !errors.Is(err, acme.ErrNoAccount) {
			return false, caError(err)
		}
	}
	if account == nil {
		account, err = client.Register(ctx, &acme.Account{}, func(string) bool { return p.TermsAgreed })
		if err != nil {
			return false, caError(err)
		}
	}
	if account.Status != acme.StatusValid || !sameOrigin(account.URI, p.origin) {
		return false, problem("policy_unavailable")
	}
	if previous.URI != account.URI && saveJSON(path, savedAccount{URI: account.URI}) != nil {
		return false, problem("local_storage_failed")
	}
	client.KID = acme.KeyID(account.URI)
	return previous.URI != "" && previous.URI != account.URI, nil
}
func localKey(path string) (*ecdsa.PrivateKey, []byte, error) {
	raw, err := privateRead(path, 16<<10)
	if err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, nil, problem("local_storage_failed")
		}
		key, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		typed, ok := key.(*ecdsa.PrivateKey)
		if e != nil || !ok {
			return nil, nil, problem("local_storage_failed")
		}
		return typed, raw, nil
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, nil, problem("local_storage_failed")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	raw = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if atomicFile(path, raw, 0600) != nil {
		return nil, nil, problem("local_storage_failed")
	}
	return key, raw, nil
}
func (r *Runner) cleanup(p *Profile, order *savedOrder, path string) error {
	if len(order.Challenges) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, c := range order.Challenges {
		if p.updateTXT(ctx, c.Name, c.Value, true) != nil {
			return problem("dns_update_failed")
		}
	}
	order.Challenges = nil
	if saveJSON(path, order) != nil {
		return problem("local_storage_failed")
	}
	return nil
}
func (r *Runner) certificate(p *Profile, j domain.ACMEJob, keyPEM, certPEM []byte) (domain.TLSCertificate, error) {
	result := domain.TLSCertificate{Reference: j.Reference}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(pair.Certificate) == 0 || len(pair.Certificate) > 8 {
		return result, problem("certificate_invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || len(leaf.DNSNames) != len(j.Hostnames) {
		return result, problem("certificate_invalid")
	}
	actual := append([]string{}, leaf.DNSNames...)
	expected := append([]string{}, j.Hostnames...)
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		return result, problem("certificate_invalid")
	}
	intermediates := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		c, e := x509.ParseCertificate(der)
		if e != nil {
			return result, problem("certificate_invalid")
		}
		intermediates.AddCert(c)
	}
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: p.roots, Intermediates: intermediates, DNSName: j.Hostnames[0], KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil || !leaf.NotAfter.After(time.Now().Add(time.Minute)) {
		return result, problem("certificate_invalid")
	}
	hash := sha256.Sum256(leaf.Raw)
	result.SHA256 = hex.EncodeToString(hash[:])
	result.DNSNames = actual
	result.NotBeforeMS = leaf.NotBefore.UnixMilli()
	result.NotAfterMS = leaf.NotAfter.UnixMilli()
	return result, nil
}
func (r *Runner) registerAsset(j domain.ACMEJob, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.assets {
		if a.Reference == j.Reference {
			return nil
		}
	}
	if len(r.assets) >= 128 {
		return problem("certificate_storage_full")
	}
	next := append(append([]Asset{}, r.assets...), Asset{Reference: j.Reference, Path: path, CreatedAtMS: time.Now().UnixMilli()})
	if saveJSON(filepath.Join(r.state, "assets.json"), next) != nil {
		return problem("local_storage_failed")
	}
	r.assets = next
	return nil
}

// Issue runs independently of HAProxy configuration and telemetry. Successful
// output contains public metadata only; all account, CSR and certificate keys
// remain in the node's private journal and immutable local certificate files.
func (r *Runner) Issue(ctx context.Context, j domain.ACMEJob) (result domain.ACMEResult) {
	result.JobID = j.ID
	if !j.Valid() || j.DeadlineMS <= time.Now().UnixMilli() || j.DeadlineMS > time.Now().Add(16*time.Minute).UnixMilli() {
		result.ErrorCode = "authority_expired"
		return
	}
	p := r.profile(j)
	if p == nil {
		result.ErrorCode = "policy_unavailable"
		return
	}
	run, stop := context.WithDeadline(ctx, time.UnixMilli(j.DeadlineMS))
	defer stop()
	cert, err := r.issue(run, p, j)
	if err == nil {
		result.Certificate = &cert
		return
	}
	var failure issueError
	if errors.As(err, &failure) {
		result.ErrorCode = failure.code
		result.RetryAfterSeconds = failure.retry
	} else if run.Err() != nil {
		result.ErrorCode = "issuance_timeout"
	} else {
		result.ErrorCode = "acme_unavailable"
	}
	return
}
func (r *Runner) issue(ctx context.Context, p *Profile, j domain.ACMEJob) (metadata domain.TLSCertificate, err error) {
	if entries, e := os.ReadDir(r.state); e != nil || len(entries) >= 512 {
		return metadata, problem("certificate_storage_full")
	}

	r.mu.RLock()
	full := len(r.assets) >= 128 && !slices.ContainsFunc(r.assets, func(a Asset) bool { return a.Reference == j.Reference })
	r.mu.RUnlock()
	if full {
		return metadata, problem("certificate_storage_full")
	}
	key, keyPEM, err := localKey(filepath.Join(r.state, j.Reference+".key"))
	if err != nil {
		return metadata, err
	}
	destination := filepath.Join(r.policy.CertificateDir, j.Reference+".pem")
	if _, e := os.Lstat(destination); e == nil {
		raw, e := privateRead(destination, 128<<10)
		if e != nil {
			return metadata, problem("certificate_invalid")
		}
		metadata, err = r.certificate(p, j, keyPEM, raw)
		if err != nil {
			return
		}
		path := filepath.Join(r.state, j.Reference+".order.json")
		journal := savedOrder{}
		if _, e := os.Lstat(path); e == nil {
			if readJSON(path, &journal, 16<<10) != nil || len(journal.Challenges) > 8 {
				return metadata, problem("local_storage_failed")
			}
			if err = r.cleanup(p, &journal, path); err != nil {
				return
			}
		}
		err = r.registerAsset(j, destination)
		return
	}
	for _, h := range j.Hostnames {
		for _, c := range p.Challenges {
			if c.Hostname == h && !p.delegation(ctx, c) {
				return metadata, problem("delegation_missing")
			}
		}
	}
	accountHash := sha256.Sum256([]byte(p.DirectoryURL))
	accountName := p.Issuer + "-" + hex.EncodeToString(accountHash[:8])
	accountKey, _, err := localKey(filepath.Join(r.state, accountName+".account.key"))
	if err != nil {
		return metadata, err
	}
	client := &acme.Client{Key: accountKey, DirectoryURL: p.DirectoryURL, HTTPClient: p.client(), UserAgent: "converge-haproxy-agent"}
	defer client.HTTPClient.CloseIdleConnections()
	accountPath := filepath.Join(r.state, accountName+".account.json")
	accountChanged, err := issuerAccount(ctx, p, client, accountPath)
	if err != nil {
		return metadata, err
	}
	journalPath := filepath.Join(r.state, j.Reference+".order.json")
	journal := savedOrder{}
	if _, e := os.Lstat(journalPath); e == nil {
		if readJSON(journalPath, &journal, 16<<10) != nil {
			return metadata, problem("local_storage_failed")
		}
	}
	if len(journal.Challenges) > 8 {
		return metadata, problem("local_storage_failed")
	}
	if err = r.cleanup(p, &journal, journalPath); err != nil {
		return metadata, err
	}
	if accountChanged {
		journal.URI = ""
		if saveJSON(journalPath, journal) != nil {
			return metadata, problem("local_storage_failed")
		}
	}
	defer func() {
		cleanErr := r.cleanup(p, &journal, journalPath)
		if err == nil && cleanErr != nil {
			err = cleanErr
		}
	}()
	var order *acme.Order
	if journal.URI != "" {
		if !sameOrigin(journal.URI, p.origin) {
			return metadata, problem("policy_unavailable")
		}
		order, err = client.GetOrder(ctx, journal.URI)
		if err != nil {
			var missing *acme.Error
			if !errors.As(err, &missing) || missing.StatusCode != 404 {
				return metadata, caError(err)
			}
			order = nil
		}
		if order != nil && order.Status == acme.StatusInvalid {
			order = nil
		}
	}
	if order == nil {
		order, err = client.AuthorizeOrder(ctx, acme.DomainIDs(j.Hostnames...))
		if err != nil {
			return metadata, caError(err)
		}
		journal.URI = order.URI
		if saveJSON(journalPath, journal) != nil {
			return metadata, problem("local_storage_failed")
		}
	}
	if len(order.AuthzURLs) > 8 {
		return metadata, problem("acme_validation_failed")
	}
	if order.Status == acme.StatusPending {
		for _, uri := range order.AuthzURLs {
			authorization, e := client.GetAuthorization(ctx, uri)
			if e != nil {
				return metadata, caError(e)
			}
			if authorization.Identifier.Type != "dns" || !slices.Contains(j.Hostnames, authorization.Identifier.Value) || authorization.Wildcard {
				return metadata, problem("acme_validation_failed")
			}
			if authorization.Status == acme.StatusValid {
				continue
			}
			var challenge *acme.Challenge
			for _, c := range authorization.Challenges {
				if c.Type == "dns-01" {
					challenge = c
					break
				}
			}
			if challenge == nil {
				return metadata, problem("acme_validation_failed")
			}
			value, e := client.DNS01ChallengeRecord(challenge.Token)
			if e != nil {
				return metadata, problem("acme_validation_failed")
			}
			var grant domain.ACMEChallenge
			for _, c := range p.Challenges {
				if c.Hostname == authorization.Identifier.Value {
					grant = c
				}
			}
			if grant.Target == "" || !p.delegation(ctx, grant) {
				return metadata, problem("delegation_missing")
			}
			journal.Challenges = append(journal.Challenges, savedChallenge{Name: grant.Target, Value: value})
			if saveJSON(journalPath, journal) != nil {
				return metadata, problem("local_storage_failed")
			}
			if p.updateTXT(ctx, grant.Target, value, false) != nil {
				return metadata, problem("dns_update_failed")
			}
			propagated := false
			propagation, stop := context.WithTimeout(ctx, 45*time.Second)
			for propagation.Err() == nil {
				if p.propagated(propagation, grant, value) {
					propagated = true
					break
				}
				select {
				case <-propagation.Done():
				case <-time.After(time.Second):
				}
			}
			stop()
			if !propagated {
				return metadata, problem("dns_propagation_failed")
			}
			if _, e = client.Accept(ctx, challenge); e != nil {
				return metadata, caError(e)
			}
			if _, e = client.WaitAuthorization(ctx, uri); e != nil {
				if _, limited := acme.RateLimit(e); limited {
					return metadata, caError(e)
				}
				return metadata, problem("acme_validation_failed")
			}
		}
	}
	if order.Status != acme.StatusValid {
		order, err = client.WaitOrder(ctx, journal.URI)
		if err != nil {
			return metadata, caError(err)
		}
	}
	var chain [][]byte
	if order.Status == acme.StatusValid {
		chain, err = client.FetchCert(ctx, order.CertURL, true)
	} else {
		csr, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: j.Hostnames}, key)
		if e != nil {
			return metadata, problem("certificate_invalid")
		}
		order.URI = journal.URI
		chain, _, err = client.CreateCertFromOrder(ctx, order, csr, true)
	}
	if err != nil {
		return metadata, caError(err)
	}
	var bundle []byte
	for _, der := range chain {
		bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	if len(bundle) > 96<<10 {
		return metadata, problem("certificate_invalid")
	}
	metadata, err = r.certificate(p, j, keyPEM, bundle)
	if err != nil {
		return metadata, err
	}
	bundle = append(bundle, keyPEM...)
	// This alias was checked absent while holding the agent's installation lock.
	// A generation is immutable: recovery reuses it and never overwrites it.
	if _, e := os.Lstat(destination); !errors.Is(e, os.ErrNotExist) {
		return metadata, problem("certificate_invalid")
	}
	if atomicFile(destination, bundle, 0640) != nil {
		return metadata, problem("local_storage_failed")
	}
	err = r.registerAsset(j, destination)
	return
}
func sameOrigin(raw, origin string) bool { return strings.HasPrefix(raw, origin+"/") }

// Reconcile interrupted challenge cleanup even when renewal was disabled while
// the agent was offline. Only values from private journals and currently granted
// exact TXT owners can be removed. No record-set deletion is possible.
func (r *Runner) Cleanup(ctx context.Context) {
	entries, e := os.ReadDir(r.state)
	if e != nil || len(entries) > 512 {
		return
	}
	processed := 0
	for _, entry := range entries {
		if ctx.Err() != nil || processed >= 4 {
			return
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".order.json") {
			continue
		}
		path := filepath.Join(r.state, entry.Name())
		var order savedOrder
		if readJSON(path, &order, 16<<10) != nil || len(order.Challenges) == 0 || len(order.Challenges) > 8 {
			continue
		}
		for i := range r.policy.Profiles {
			p := &r.policy.Profiles[i]
			allowed := true
			for _, record := range order.Challenges {
				allowed = allowed && slices.ContainsFunc(p.Challenges, func(c domain.ACMEChallenge) bool { return c.Target == record.Name })
			}
			if allowed {
				_ = r.cleanup(p, &order, path)
				processed++
				break
			}
		}
	}
}
