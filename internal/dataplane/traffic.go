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
	"regexp"
	"strconv"
	"strings"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/domain"
)

type TrafficPolicy struct {
	Source             accesslog.GeoPolicy
	LogSocket          string
	CountryMap, ASNMap string
	Capabilities       domain.TrafficCapabilities
}
type trafficGrant struct {
	Version        int    `json:"version"`
	ClientIPPolicy string `json:"client_ip_policy"`
	MapsDirectory  string `json:"maps_directory,omitempty"`
	LogSocket      string `json:"haproxy_log_socket"`
}

var safeMapPath = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)

func (c *Client) trafficProfile(f object) bool {
	if c.Traffic == nil || !c.AllowRequestLogs || decode[string](f["log_format"]) != domain.RequestTrafficLogFormat {
		return false
	}
	logs := decode[[]object](f["log_target_list"])
	if len(logs) != 2 || !decode[bool](logs[0]["nolog"]) || decode[string](logs[1]["address"]) != c.Traffic.LogSocket || c.Traffic.LogSocket == "" || decode[string](logs[1]["format"]) != "raw" || decode[int](logs[1]["length"]) != 2048 {
		return false
	}
	for _, b := range decode[map[string]object](f["binds"]) {
		if decode[bool](b["accept_proxy"]) {
			return false
		}
	}
	peer, client, proxy := -1, -1, -1
	for i, r := range decode[[]object](f["tcp_request_rule_list"]) {
		name := decode[string](r["var_name"])
		if name == "cv_peer" || name == "cv_client" {
			if decode[string](r["action"]) != "set-var" || decode[string](r["var_scope"]) != "sess" || decode[string](r["expr"]) != "src" || decode[string](r["cond"]) != "" {
				return false
			}
			if name == "cv_peer" {
				if peer >= 0 || decode[string](r["type"]) != "connection" {
					return false
				}
				peer = i
			} else {
				if client >= 0 || decode[string](r["type"]) != "session" {
					return false
				}
				client = i
			}
		}
		if decode[string](r["action"]) == "expect-proxy" {
			if proxy >= 0 {
				return false
			}
			proxy = i
			if c.Traffic.Source.Mode == "proxy_protocol" && (decode[string](r["cond"]) != "if" || decode[string](r["cond_test"]) != "{ var(sess.cv_peer) -m ip "+strings.Join(c.Traffic.Source.TrustedProxies, " ")+" }") {
				return false
			}
		}
	}
	if peer < 0 || client <= peer || proxy >= 0 && (proxy < peer || proxy > client) || c.Traffic.Source.Mode == "proxy_protocol" && proxy < 0 {
		return false
	}
	for i, r := range decode[[]object](f["tcp_request_rule_list"]) {
		action := decode[string](r["action"])
		if i < client && (action == "set-src" || action == "set-src-port") {
			return false
		}
	}
	captured := -1
	for name, expr := range map[string]string{"cv_xff_count": "req.fhdr_cnt(X-Forwarded-For)", "cv_xff": "req.fhdr(X-Forwarded-For)"} {
		count := 0
		for i, r := range decode[[]object](f["http_request_rule_list"]) {
			if decode[string](r["var_name"]) == name {
				if decode[string](r["type"]) != "set-var" || decode[string](r["var_scope"]) != "txn" || decode[string](r["var_expr"]) != expr || decode[string](r["cond"]) != "" {
					return false
				}
				count++
				if i > captured {
					captured = i
				}
			}
		}
		if count != 1 {
			return false
		}
	}
	for i, r := range decode[[]object](f["http_request_rule_list"]) {
		name, scope := decode[string](r["var_name"]), decode[string](r["var_scope"])
		if scope == "sess" && (name == "cv_peer" || name == "cv_client") {
			return false
		}
		if i < captured {
			switch decode[string](r["type"]) {
			case "set-header", "add-header", "del-header", "replace-header", "replace-value":
				return false
			}
		}
	}
	return true
}

func localTrafficFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("absolute local traffic policy required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > limit {
		return nil, errors.New("owner-writable regular traffic policy required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, errors.New("traffic policy changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("traffic policy exceeds local bound")
	}
	return raw, nil
}

// LoadTrafficPolicy is called once at agent startup. The host grants one shared
// client-address policy and a directory visible read-only to HAProxy. Map files
// contain public dataset prefixes, never request addresses or credentials.
func LoadTrafficPolicy(path, requestGeoPolicy string) (*TrafficPolicy, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := localTrafficFile(path, 16384)
	if err != nil {
		return nil, err
	}
	var g trafficGrant
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&g) != nil || d.Decode(new(any)) != io.EOF || g.Version != 1 || g.ClientIPPolicy == "" || requestGeoPolicy != "" && requestGeoPolicy != g.ClientIPPolicy {
		return nil, errors.New("traffic rules require the shared host client-IP policy")
	}
	if !safeMapPath.MatchString(g.LogSocket) || filepath.Clean(g.LogSocket) != g.LogSocket || len(g.LogSocket) > 107 {
		return nil, errors.New("traffic rules require the private logger path in HAProxy's namespace")
	}
	geo, err := accesslog.LoadClientPolicy(g.ClientIPPolicy)
	if err != nil || geo == nil {
		return nil, errors.New("traffic client-IP policy unavailable")
	}
	defer geo.Close()
	source := geo.ClientPolicy()
	if source.Mode == "off" {
		return nil, errors.New("traffic client-IP policy is disabled")
	}
	p := &TrafficPolicy{Source: source, LogSocket: g.LogSocket, Capabilities: domain.TrafficCapabilities{ClientSource: source.Mode}}
	if source.CountryDatabase != "" || source.ASNDatabase != "" {
		if !safeMapPath.MatchString(g.MapsDirectory) || filepath.Clean(g.MapsDirectory) != g.MapsDirectory {
			return nil, errors.New("traffic maps require a safe absolute shared directory")
		}
		info, err := os.Lstat(g.MapsDirectory)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("traffic map directory must exist and be owner-writable")
		}
		for _, kind := range []string{"country", "asn"} {
			data, err := geo.MapSnapshot(kind)
			if err != nil {
				continue
			}
			name, err := retainTrafficMap(g.MapsDirectory, data)
			if err != nil {
				return nil, err
			}
			if kind == "country" {
				p.CountryMap = name
				p.Capabilities.CountryReady = true
			} else {
				p.ASNMap = name
				p.Capabilities.ASNReady = true
			}
		}
	}
	profile, _ := json.Marshal(struct {
		Version int
		Grant   trafficGrant
		Geo     string
	}{1, g, geo.Snapshot().Profile})
	sum := sha256.Sum256(profile)
	p.Capabilities.Profile = hex.EncodeToString(sum[:])
	return p, nil
}
func retainTrafficMap(dir string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	path := filepath.Join(dir, "cv_geo_v1_"+hex.EncodeToString(sum[:])+".map")
	if existing, err := localTrafficFile(path, 64<<20); err == nil {
		if !bytes.Equal(existing, data) {
			return "", errors.New("immutable traffic map changed")
		}
		return path, nil
	} else if _, err := os.Lstat(path); err == nil {
		return "", errors.New("unsafe existing traffic map")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) >= 16 {
		return "", errors.New("traffic map retention is full")
	}
	total := int64(len(data))
	for _, e := range entries {
		i, err := e.Info()
		if err != nil || !i.Mode().IsRegular() {
			return "", errors.New("unsafe traffic map directory")
		}
		total += i.Size()
	}
	if total > 256<<20 {
		return "", errors.New("traffic map storage is full")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", errors.New("traffic map unavailable")
	}
	_, err = f.Write(data)
	if err == nil {
		// Service umasks may be private by default. These files contain only
		// licensed dataset prefixes; a separate HAProxy user must read them.
		err = f.Chmod(0644)
	}
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil || closed != nil {
		_ = os.Remove(path)
		return "", errors.New("traffic map write failed")
	}
	if syncDir(dir) != nil {
		return "", errors.New("traffic map persistence failed")
	}
	return path, nil
}

func trafficVariable(name, expression, condition string) object {
	r := object{}
	set(r, "type", "set-var")
	set(r, "var_scope", "txn")
	set(r, "var_name", name)
	set(r, "var_expr", expression)
	if condition != "" {
		set(r, "cond", "if")
		set(r, "cond_test", condition)
	}
	return r
}
func trafficUnset(name, condition string) object {
	r := object{}
	set(r, "type", "unset-var")
	set(r, "var_scope", "txn")
	set(r, "var_name", name)
	if condition != "" {
		set(r, "cond", "if")
		set(r, "cond_test", condition)
	}
	return r
}
func (p *TrafficPolicy) addressRules() []object {
	rules := []object{trafficUnset("cv_ip", ""), trafficUnset("cv_bad", ""), trafficUnset("cv_country", ""), trafficUnset("cv_asn", "")}
	ip := "{ var(txn.cv_ip) -m found }"
	trust := "{ var(sess.cv_peer) -m ip " + strings.Join(p.Source.TrustedProxies, " ") + " }"
	switch p.Source.Mode {
	case "peer":
		rules = append(rules, trafficVariable("cv_ip", "var(sess.cv_peer)", ""))
	case "proxy_protocol":
		rules = append(rules, trafficVariable("cv_ip", "var(sess.cv_client)", trust+" { fc_rcvd_proxy } { var(txn.cv_xff_count) -m int eq 0 }"))
	case "xff":
		rules = append(rules, trafficVariable("cv_ip", "var(sess.cv_peer)", "!"+trust))
		valid := trust + " !{ fc_rcvd_proxy } { var(txn.cv_xff_count) -m int eq 1 } { req.fhdr_cnt(X-Forwarded-For) eq 1 } { req.fhdr(X-Forwarded-For),strcmp(txn.cv_xff) eq 0 } { var(txn.cv_xff),length -m int le 256 } { req.hdr_cnt(X-Forwarded-For) ge 1 } { req.hdr_cnt(X-Forwarded-For) le 16 }"
		for i := 1; i <= 16; i++ {
			part := "req.hdr(X-Forwarded-For," + strconv.Itoa(i) + ")"
			base := valid + " { req.hdr_cnt(X-Forwarded-For) ge " + strconv.Itoa(i) + " }"
			rules = append(rules, trafficVariable("cv_bad", "int(1)", base+" !{ "+part+" -m ip 0.0.0.0/0 ::/0 }"), trafficVariable("cv_bad", "int(1)", base+" !{ "+part+" -m reg ^[0-9A-Fa-f:.]+$ }"))
			// HAProxy's address cast also accepts IPv4:port and integer IPv4.
			// Match the telemetry resolver's bare-address grammar before using it.
			octet := "(0|[1-9][0-9]?[0-9]?)"
			dotted := "^([0-9A-Fa-f:]*:)?" + octet + "[.]" + octet + "[.]" + octet + "[.]" + octet + "$"
			rules = append(rules, trafficVariable("cv_bad", "int(1)", base+" !{ "+part+" -m sub . : }"), trafficVariable("cv_bad", "int(1)", base+" { "+part+" -m sub . } !{ "+part+" -m reg "+dotted+" }"))
			rules = append(rules, trafficVariable("cv_bad", "int(1)", base+" { "+part+" -m ip 0.0.0.0/32 ::/128 224.0.0.0/4 ff00::/8 }"))
		}
		valid += " !{ var(txn.cv_bad) -m found }"
		rules = append(rules, trafficVariable("cv_ip", "var(sess.cv_peer)", valid))
		for i := 16; i >= 1; i-- {
			rules = append(rules, trafficVariable("cv_ip", "req.hdr_ip(X-Forwarded-For,"+strconv.Itoa(i)+")", valid+" { req.hdr_cnt(X-Forwarded-For) ge "+strconv.Itoa(i)+" } { var(txn.cv_ip) -m ip "+strings.Join(p.Source.TrustedProxies, " ")+" }"))
		}
		rules = append(rules, trafficUnset("cv_ip", trust+" { var(txn.cv_ip) -m ip "+strings.Join(p.Source.TrustedProxies, " ")+" }"))
	}
	rules = append(rules, trafficUnset("cv_ip", "{ var(txn.cv_ip) -m ip 0.0.0.0/32 ::/128 224.0.0.0/4 ff00::/8 }"))
	public := ip + " !{ var(txn.cv_ip) -m ip 0.0.0.0/8 10.0.0.0/8 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 ::/128 ::1/128 fc00::/7 fe80::/10 ff00::/8 }"
	if p.CountryMap != "" {
		rules = append(rules, trafficVariable("cv_country", "var(txn.cv_ip),map_ip("+p.CountryMap+")", public))
	}
	if p.ASNMap != "" {
		rules = append(rules, trafficVariable("cv_asn", "var(txn.cv_ip),map_ip("+p.ASNMap+")", public))
	}
	return rules
}
func (p *TrafficPolicy) rules(policy *domain.TrafficPolicy) []object {
	rules := p.addressRules()
	rules = append(rules, trafficVariable("cv_rule", "str(none)", ""), trafficVariable("cv_decision", "str(none)", ""))
	first := "{ var(txn.cv_rule) -m str none }"
	country, asn := false, false
	match := func(id, action, condition string) {
		rules = append(rules, trafficVariable("cv_decision", "str("+action+")", condition), trafficVariable("cv_rule", "str("+id+")", condition))
	}
	for _, r := range policy.Rules {
		condition := first + " { var(txn.cv_ip) -m found }"
		if r.PathPrefix != "" {
			condition += " { path_beg " + r.PathPrefix + " }"
		}
		if len(r.CIDRs) > 0 {
			condition += " { var(txn.cv_ip) -m ip " + strings.Join(r.CIDRs, " ") + " }"
		}
		if len(r.Countries) > 0 {
			country = true
			condition += " { var(txn.cv_country) -m str " + strings.Join(r.Countries, " ") + " }"
		}
		if len(r.ASNs) > 0 {
			asn = true
			values := []string{}
			for _, n := range r.ASNs {
				values = append(values, strconv.FormatUint(uint64(n), 10))
			}
			condition += " { var(txn.cv_asn) -m int " + strings.Join(values, " ") + " }"
		}
		match(r.ID, r.Action, condition)
	}
	unknown := []string{"!{ var(txn.cv_ip) -m found }"}
	if country {
		unknown = append(unknown, "!{ var(txn.cv_country) -m found }")
	}
	if asn {
		unknown = append(unknown, "!{ var(txn.cv_asn) -m found }")
	}
	for _, condition := range unknown {
		match("unknown", policy.UnknownAction, first+" "+condition)
	}
	if policy.Mode == "observe" {
		rules = append(rules, trafficVariable("cv_decision", "str(would_block)", "{ var(txn.cv_decision) -m str block }"), trafficVariable("cv_decision", "str(would_allow)", "{ var(txn.cv_decision) -m str allow }"))
	} else {
		deny := object{}
		set(deny, "type", "deny")
		set(deny, "deny_status", 403)
		set(deny, "cond", "if")
		set(deny, "cond_test", "{ var(txn.cv_decision) -m str block }")
		rules = append(rules, deny)
	}
	return rules
}
