package dataplane

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
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

const listenerMarker = " | converge-https-v1:"
const listenerBind = "cv_https"
const listenerVariable = "cv_https_route"

type LocalCertificate struct {
	Reference string `json:"reference"`
	Path      string `json:"path"`
}
type TLSPolicy struct {
	Bindings     []domain.ListenerBinding `json:"bindings"`
	Certificates []LocalCertificate       `json:"certificates"`
	// ProbeHost is locally configured for sidecars in a separate network namespace.
	// Remote commands can only select a granted listener, never a probe target.
	ProbeHost string `json:"probe_host,omitempty"`
}

func (c *Client) LoadTLSPolicy(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("TLS policy file unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(raw) > 16384 {
		return errors.New("TLS policy file too large")
	}
	var policy TLSPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&policy) != nil || decoder.Decode(new(any)) != io.EOF || len(policy.Bindings) == 0 || len(policy.Bindings) > domain.MaxHTTPSListeners || len(policy.Certificates) > 8 {
		return errors.New("invalid TLS policy")
	}
	if policy.ProbeHost != "" && !domain.ConfigurationAddress(policy.ProbeHost) {
		return errors.New("invalid TLS probe host")
	}
	for i, b := range policy.Bindings {
		if !b.Valid() {
			return errors.New("invalid TLS listener binding")
		}
		for _, a := range policy.Bindings[:i] {
			if a.Name == b.Name || a.Port == b.Port {
				return errors.New("duplicate TLS listener binding")
			}
		}
	}
	seen := map[string]bool{}
	for _, r := range policy.Certificates {
		if !domain.ValidRuntimeName(r.Reference) || len(r.Reference) > 64 || seen[r.Reference] || !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || !regexp.MustCompile(`^/[A-Za-z0-9_./-]+\.pem$`).MatchString(r.Path) {
			return errors.New("invalid local certificate reference")
		}
		seen[r.Reference] = true
	}
	c.TLS = &policy
	return nil
}
func certificateFile(r LocalCertificate) (domain.TLSCertificate, *x509.Certificate, error) {
	metadata := domain.TLSCertificate{Reference: r.Reference, ErrorCode: "certificate_unavailable", DNSNames: []string{}}
	f, err := os.Open(r.Path)
	if err != nil {
		return metadata, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<10 {
		return metadata, nil, errors.New("certificate file unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	if err != nil || len(raw) > 128<<10 {
		return metadata, nil, errors.New("certificate file unavailable")
	}
	pair, err := tls.X509KeyPair(raw, raw)
	metadata.ErrorCode = "certificate_invalid"
	if err != nil || len(pair.Certificate) == 0 || len(pair.Certificate) > 8 {
		return metadata, nil, errors.New("invalid certificate material")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || len(leaf.DNSNames) == 0 || len(leaf.DNSNames) > 32 {
		return metadata, nil, errors.New("invalid certificate names")
	}
	previous := leaf
	for _, der := range pair.Certificate[1:] {
		issuer, e := x509.ParseCertificate(der)
		if e != nil || previous.CheckSignatureFrom(issuer) != nil {
			return metadata, nil, errors.New("invalid certificate chain")
		}
		previous = issuer
	}
	metadata.DNSNames = append([]string{}, leaf.DNSNames...)
	metadata.NotBeforeMS = leaf.NotBefore.UnixMilli()
	metadata.NotAfterMS = leaf.NotAfter.UnixMilli()
	hash := sha256.Sum256(leaf.Raw)
	metadata.SHA256 = hex.EncodeToString(hash[:])
	metadata.ErrorCode = ""
	if time.Now().Before(leaf.NotBefore) {
		metadata.ErrorCode = "certificate_not_yet_valid"
	} else if !time.Now().Add(time.Minute).Before(leaf.NotAfter) {
		metadata.ErrorCode = "certificate_expired"
	}
	return metadata, leaf, nil
}
func (c *Client) certificate(reference string) (LocalCertificate, bool) {
	if c.TLS != nil {
		for _, r := range c.certificates() {
			if r.Reference == reference {
				return r, true
			}
		}
	}
	return LocalCertificate{}, false
}
func (c *Client) certificates() []LocalCertificate {
	out := []LocalCertificate{}
	if c.TLS != nil {
		out = append(out, c.TLS.Certificates...)
	}
	if c.DynamicCertificates != nil {
		out = append(out, c.DynamicCertificates()...)
	}
	return out
}
func (c *Client) tlsInventory(documents ...domain.ConfigurationDocument) domain.ConfigurationState {
	out := domain.ConfigurationState{NodeID: c.NodeID}
	if c.ACMEGrants != nil {
		out.ACMEGrants = c.ACMEGrants()
	}
	if c.TLS == nil {
		return out
	}
	out.ListenerControlVersion = 1
	out.ListenerBindings = append([]domain.ListenerBinding{}, c.TLS.Bindings...)
	selected := map[string]bool{}
	for _, doc := range documents {
		for _, l := range doc.Listeners {
			selected[l.CertificateRef] = true
		}
	}
	for _, r := range c.TLS.Certificates {
		selected[r.Reference] = true
	}
	inventory := c.certificates()
	for i, r := range inventory {
		if i < len(c.TLS.Certificates) || (i < len(c.TLS.Certificates)+6 && len(selected) < 16) {
			selected[r.Reference] = true
		}
	}
	for _, r := range inventory {
		if !selected[r.Reference] {
			continue
		}
		m, _, _ := certificateFile(r)
		out.TLSCertificates = append(out.TLSCertificates, m)
	}
	return out
}
func listenerDescription(prefix string, l domain.HTTPSListener) string {
	raw, _ := json.Marshal(l)
	return prefix + listenerMarker + base64.RawURLEncoding.EncodeToString(raw)
}
func (c *Client) listenerObject(l domain.HTTPSListener) (object, error) {
	r, ok := c.certificate(l.CertificateRef)
	if !ok {
		return nil, failure("configuration_unsupported")
	}
	b := object{}
	set(b, "name", listenerBind)
	set(b, "address", l.Address)
	set(b, "port", l.Port)
	set(b, "ssl", true)
	set(b, "ssl_certificate", r.Path)
	set(b, "ssl_min_ver", "TLSv1.2")
	set(b, "strict_sni", true)
	set(b, "force_strict_sni", "enabled")
	set(b, "alpn", "h2,http/1.1")
	return b, nil
}
func listenerRules(apps []domain.HTTPApplication, name string) []object {
	apps = append([]domain.HTTPApplication{}, apps...)
	slices.SortFunc(apps, func(a, b domain.HTTPApplication) int { return strings.Compare(a.ID, b.ID) })
	reset := object{}
	set(reset, "type", "set-var")
	set(reset, "var_scope", "txn")
	set(reset, "var_name", listenerVariable)
	set(reset, "var_expr", "str(no)")
	set(reset, "cond", "if")
	set(reset, "cond_test", "{ ssl_fc }")
	out := []object{reset}
	for _, a := range apps {
		if a.Listener != name {
			continue
		}
		rule := object{}
		set(rule, "type", "set-var")
		set(rule, "var_scope", "txn")
		set(rule, "var_name", listenerVariable)
		set(rule, "var_expr", "str(yes)")
		set(rule, "cond", "if")
		set(rule, "cond_test", "{ ssl_fc } { ssl_fc_sni,lower -m str "+a.Hostname+" } "+applicationCondition(a))
		out = append(out, rule)
	}
	deny := object{}
	set(deny, "type", "return")
	set(deny, "return_status_code", 404)
	set(deny, "cond", "if")
	set(deny, "cond_test", "{ ssl_fc } !{ var(txn."+listenerVariable+") -m str yes }")
	out = append(out, deny)
	return out
}
func normalizedRule(v object) string {
	copy := object{}
	for k, value := range v {
		if k != "index" {
			copy[k] = value
		}
	}
	return normalizedObject(copy)
}
func isListenerRule(v object) bool {
	return decode[string](v["var_name"]) == listenerVariable || strings.Contains(decode[string](v["cond_test"]), "txn."+listenerVariable)
}
func (c *Client) projectListeners(frontends []object, apps []domain.HTTPApplication) ([]domain.HTTPSListener, error) {
	out := []domain.HTTPSListener{}
	for _, f := range frontends {
		description := decode[string](f["description"])
		index := strings.Index(description, listenerMarker)
		binds := decode[map[string]object](f["binds"])
		managed, hasBind := binds[listenerBind]
		rules := decode[[]object](f["http_request_rule_list"])
		if index < 0 {
			if hasBind {
				return nil, failure("configuration_unsupported")
			}
			for _, r := range rules {
				if isListenerRule(r) {
					return nil, failure("configuration_unsupported")
				}
			}
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(description[index+len(listenerMarker):])
		var l domain.HTTPSListener
		if err != nil || len(raw) > 2048 || json.Unmarshal(raw, &l) != nil || !l.Valid() || l.Name != decode[string](f["name"]) || listenerDescription(description[:index], l) != description {
			return nil, failure("configuration_unsupported")
		}
		expected, err := c.listenerObject(l)
		if err != nil || !hasBind || normalizedObject(managed) != normalizedObject(expected) {
			return nil, failure("configuration_unsupported")
		}
		for name, b := range binds {
			if name != listenerBind && decode[bool](b["ssl"]) {
				return nil, failure("configuration_unsupported")
			}
		}
		expectedRules := listenerRules(apps, l.Name)
		if len(rules) < len(expectedRules) {
			return nil, failure("configuration_unsupported")
		}
		for i, r := range expectedRules {
			if normalizedRule(r) != normalizedRule(rules[i]) {
				return nil, failure("configuration_unsupported")
			}
		}
		for _, r := range rules[len(expectedRules):] {
			if isListenerRule(r) {
				return nil, failure("configuration_unsupported")
			}
		}
		out = append(out, l)
	}
	if !domain.ValidListeners(out) {
		return nil, failure("configuration_unsupported")
	}
	return domain.CanonicalListeners(out), nil
}
func (c *Client) validateListeners(doc domain.ConfigurationDocument) error {
	state := c.tlsInventory(doc)
	for _, l := range doc.Listeners {
		if !state.ListenerReady(l, doc.Applications, time.Now()) {
			return failure("configuration_rejected")
		}
	}
	return nil
}
func (c *Client) applyListeners(ctx context.Context, transaction string, previous, desired domain.ConfigurationDocument) error {
	if domain.SameListeners(previous.Listeners, desired.Listeners) && domain.SameApplications(previous.Applications, desired.Applications) {
		return nil
	}
	if len(previous.Listeners) == 0 && len(desired.Listeners) == 0 {
		return nil
	}
	if err := c.validateListeners(desired); err != nil {
		return err
	}
	frontends, err := c.frontends(ctx, "&transaction_id="+transaction)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, f := range frontends {
		name := decode[string](f["name"])
		var target *domain.HTTPSListener
		previousOwned := false
		for i := range desired.Listeners {
			if desired.Listeners[i].Name == name {
				target = &desired.Listeners[i]
			}
		}
		for _, l := range previous.Listeners {
			previousOwned = previousOwned || l.Name == name
		}
		if target == nil && !previousOwned {
			continue
		}
		if mode := decode[string](f["mode"]); mode != "http" && mode != "" {
			return failure("configuration_unsupported")
		}
		binds := decode[map[string]object](f["binds"])
		if binds == nil {
			binds = map[string]object{}
		}
		if !previousOwned {
			if _, exists := binds[listenerBind]; exists {
				return failure("configuration_unsupported")
			}
			for _, b := range binds {
				if decode[bool](b["ssl"]) {
					return failure("configuration_unsupported")
				}
			}
		}
		description := decode[string](f["description"])
		if i := strings.Index(description, listenerMarker); i >= 0 {
			description = description[:i]
		}
		var rules []object
		if target != nil {
			for _, other := range frontends {
				for key, b := range decode[map[string]object](other["binds"]) {
					start := decode[uint32](b["port"])
					end := max(start, decode[uint32](b["port-range-end"]))
					if start > 0 && target.Port >= start && target.Port <= end && !(decode[string](other["name"]) == name && key == listenerBind && previousOwned) {
						return failure("configuration_rejected")
					}
				}
			}
			bind, e := c.listenerObject(*target)
			if e != nil {
				return e
			}
			binds[listenerBind] = bind
			description = listenerDescription(description, *target)
			rules = listenerRules(desired.Applications, name)
			seen[name] = true
		} else {
			delete(binds, listenerBind)
		}
		for _, r := range decode[[]object](f["http_request_rule_list"]) {
			if !isListenerRule(r) {
				rules = append(rules, r)
			}
		}
		set(f, "description", description)
		set(f, "binds", binds)
		set(f, "http_request_rule_list", rules)
		raw, _ := json.Marshal(f)
		if _, _, _, err = c.call(ctx, "PUT", "/services/haproxy/configuration/frontends/"+name+"?full_section=true&transaction_id="+transaction, "application/json", raw); err != nil {
			return err
		}
	}
	for _, l := range desired.Listeners {
		if !seen[l.Name] {
			return failure("configuration_unsupported")
		}
	}
	return nil
}
func (c *Client) observeTLS(ctx context.Context, doc domain.ConfigurationDocument) []domain.TLSListenerObservation {
	var out []domain.TLSListenerObservation
	for _, l := range doc.Listeners {
		o := domain.TLSListenerObservation{Name: l.Name}
		r, ok := c.certificate(l.CertificateRef)
		if !ok {
			out = append(out, o)
			continue
		}
		metadata, leaf, err := certificateFile(r)
		if err != nil || metadata.SHA256 != l.ExpectedCertificate(c.NodeID) {
			out = append(out, o)
			continue
		}
		host := leaf.DNSNames[0]
		if strings.HasPrefix(host, "*.") {
			host = "probe." + strings.TrimPrefix(host, "*.")
		}
		for _, a := range doc.Applications {
			if a.Listener == l.Name {
				host = a.Hostname
				break
			}
		}
		address := l.Address
		if address == "*" || address == "0.0.0.0" {
			address = "127.0.0.1"
		}
		if address == "::" {
			address = "::1"
		}
		if c.TLS != nil && c.TLS.ProbeHost != "" {
			address = c.TLS.ProbeHost
		}
		probe, stop := context.WithTimeout(ctx, 500*time.Millisecond)
		// This local probe verifies the exact configured leaf identity, including
		// private PKI. Validity and hostname coverage are checked separately before
		// deployment; an expired loaded certificate remains observable for repair.
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 300 * time.Millisecond}, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, InsecureSkipVerify: true, VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("TLS identity unavailable")
			}
			actual := state.PeerCertificates[0]
			sum := sha256.Sum256(actual.Raw)
			if hex.EncodeToString(sum[:]) != l.ExpectedCertificate(c.NodeID) || actual.VerifyHostname(host) != nil {
				return errors.New("TLS identity mismatch")
			}
			return nil
		}}}
		connection, e := dialer.DialContext(probe, "tcp", net.JoinHostPort(address, strconv.Itoa(int(l.Port))))
		if e == nil {
			o.Loaded = true
			o.SHA256 = l.ExpectedCertificate(c.NodeID)
			_ = connection.Close()
		}
		stop()
		out = append(out, o)
	}
	return out
}
