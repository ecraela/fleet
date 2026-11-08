package dataplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ecraela/fleet/internal/domain"
)

const applicationMarker = "converge-http-v1:"

func normalizedObject(value object) string {
	raw, _ := json.Marshal(value)
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	raw, _ = json.Marshal(decoded)
	return string(raw)
}

func applicationDescription(a domain.HTTPApplication) string {
	raw, _ := json.Marshal(a.Canonical())
	return applicationMarker + base64.RawURLEncoding.EncodeToString(raw)
}
func applicationCondition(a domain.HTTPApplication) string {
	return "{ req.hdr(host),lower,field(1,:) -m str " + a.Hostname + " } { path_beg " + a.PathPrefix + " }"
}
func applicationRules(apps []domain.HTTPApplication, listener string) []object {
	ordered := append([]domain.HTTPApplication{}, apps...)
	slices.SortFunc(ordered, func(a, b domain.HTTPApplication) int {
		if len(a.PathPrefix) != len(b.PathPrefix) {
			return len(b.PathPrefix) - len(a.PathPrefix)
		}
		return strings.Compare(a.ID, b.ID)
	})
	out := []object{}
	for _, a := range ordered {
		if a.Listener == listener {
			v := object{}
			set(v, "name", a.BackendName())
			set(v, "cond", "if")
			set(v, "cond_test", applicationCondition(a))
			out = append(out, v)
		}
	}
	return out
}
func projectApplications(backends []object) ([]object, []domain.HTTPApplication, error) {
	return (&Client{}).projectApplications(backends)
}
func (c *Client) projectApplications(backends []object) ([]object, []domain.HTTPApplication, error) {
	unmanaged := []object{}
	apps := []domain.HTTPApplication{}
	for _, b := range backends {
		name := decode[string](b["name"])
		if !strings.HasPrefix(name, domain.ApplicationBackendPrefix) {
			unmanaged = append(unmanaged, b)
			continue
		}
		marker := decode[string](b["description"])
		if !strings.HasPrefix(marker, applicationMarker) {
			return nil, nil, failure("configuration_unsupported")
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(marker, applicationMarker))
		var a domain.HTTPApplication
		if err != nil || len(raw) > 32768 || json.Unmarshal(raw, &a) != nil || !a.Valid() || a.BackendName() != name || marker != applicationDescription(a) {
			return nil, nil, failure("configuration_unsupported")
		}
		// Inherited defaults belong to the local node; all other section fields
		// have one compiler owner. Unrecognized edits remain blocking drift.
		owned := object{}
		for k, v := range b {
			if k != "from" {
				owned[k] = v
			}
		}
		if a.Disabled && len(decode[map[string]object](owned["servers"])) == 0 {
			set(owned, "servers", map[string]object{})
		}
		if raw, ok := b["from"]; ok {
			var from *string
			if json.Unmarshal(raw, &from) != nil || (from != nil && *from != "" && !domain.ValidRuntimeName(*from)) {
				return nil, nil, failure("configuration_unsupported")
			}
		}
		expected, err := c.applicationBackend(a)
		if err != nil || normalizedObject(owned) != normalizedObject(expected) {
			if a.TrafficPolicy == nil || a.Disabled {
				if err != nil {
					return nil, nil, err
				}
				return nil, nil, failure("configuration_unsupported")
			}
			legacy, err := c.applicationBackendWithTraffic(a, true)
			if err != nil || normalizedObject(owned) != normalizedObject(legacy) {
				return nil, nil, failure("configuration_unsupported")
			}
		}
		if a.Disabled {
			// An owned empty backend preserves route precedence and returns 503.
			// Its original endpoints remain only in safe intent for re-enabling.
			apps = append(apps, a)
			continue
		}
		actual, err := project([]object{b})
		if err != nil || !(domain.ConfigurationDocument{Backends: []domain.ConfigurationBackend{a.Backend()}}).Equal(actual) {
			return nil, nil, failure("configuration_unsupported")
		}
		if decode[string](b["mode"]) != "http" || decode[string](b["adv_check"]) != "httpchk" {
			return nil, nil, failure("configuration_unsupported")
		}
		params := decode[object](b["httpchk_params"])
		if decode[string](params["method"]) != "GET" || decode[string](params["uri"]) != a.HealthPath {
			return nil, nil, failure("configuration_unsupported")
		}
		for _, s := range decode[map[string]object](b["servers"]) {
			if decode[string](s["check"]) != "enabled" {
				return nil, nil, failure("configuration_unsupported")
			}
		}
		apps = append(apps, a)
	}
	if !domain.ValidApplications(apps) {
		return nil, nil, failure("configuration_unsupported")
	}
	return unmanaged, apps, nil
}
func (c *Client) frontends(ctx context.Context, query string) ([]object, error) {
	raw, _, _, err := c.call(ctx, "GET", "/services/haproxy/configuration/frontends?full_section=true"+query, "", nil)
	var result []object
	if err != nil || json.Unmarshal(raw, &result) != nil || len(result) > 32 {
		return nil, failure("configuration_unavailable")
	}
	return result, nil
}
func (c *Client) verifyApplicationRoutes(ctx context.Context, apps []domain.HTTPApplication) error {
	frontends, err := c.frontends(ctx, "")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, f := range frontends {
		name := decode[string](f["name"])
		expected := applicationRules(apps, name)
		actual := decode[[]object](f["backend_switching_rule_list"])
		if len(expected) > len(actual) {
			return failure("configuration_unsupported")
		}
		for i, rule := range expected {
			if decode[string](actual[i]["name"]) != decode[string](rule["name"]) || decode[string](actual[i]["cond"]) != "if" || decode[string](actual[i]["cond_test"]) != decode[string](rule["cond_test"]) {
				return failure("configuration_unsupported")
			}
		}
		for _, rule := range actual[len(expected):] {
			if strings.HasPrefix(decode[string](rule["name"]), domain.ApplicationBackendPrefix) {
				return failure("configuration_unsupported")
			}
		}
		seen[name] = true
	}
	for _, a := range apps {
		if a.TrafficPolicy != nil && !slices.ContainsFunc(frontends, func(f object) bool { return decode[string](f["name"]) == a.Listener && c.trafficProfile(f) }) {
			return failure("configuration_unsupported")
		}
		if !seen[a.Listener] {
			return failure("configuration_unsupported")
		}
	}
	return nil
}
func applicationBackend(a domain.HTTPApplication) object {
	b, _ := (&Client{}).applicationBackend(a)
	return b
}
func (c *Client) applicationBackend(a domain.HTTPApplication) (object, error) {
	return c.applicationBackendWithTraffic(a, false)
}
func (c *Client) applicationBackendWithTraffic(a domain.HTTPApplication, legacy bool) (object, error) {
	if a.TrafficPolicy != nil && (c.Traffic == nil || !a.TrafficPolicy.Supported(&c.Traffic.Capabilities)) {
		return nil, failure("configuration_unsupported")
	}
	b := object{}
	set(b, "name", a.BackendName())
	set(b, "description", applicationDescription(a))
	set(b, "mode", "http")
	set(b, "balance", map[string]string{"algorithm": a.Algorithm})
	set(b, "adv_check", "httpchk")
	set(b, "httpchk_params", map[string]string{"method": "GET", "uri": a.HealthPath})
	set(b, "check_timeout", 1000)
	if a.RequestTelemetry {
		set(b, "http_request_rule_list", []object{{
			"type":      json.RawMessage(`"set-var"`),
			"var_scope": json.RawMessage(`"txn"`),
			"var_name":  json.RawMessage(`"cv_spec"`),
			"var_expr":  json.RawMessage(strconv.Quote("str(" + a.Digest() + ")")),
		}})
	}
	if a.TrafficPolicy != nil && !a.Disabled {
		var rules []object
		if legacy {
			rules = c.Traffic.legacyRules(a.TrafficPolicy)
		} else {
			var err error
			rules, err = c.Traffic.rules(a.TrafficPolicy)
			if err != nil {
				return nil, err
			}
		}
		set(b, "http_request_rule_list", append(decode[[]object](b["http_request_rule_list"]), rules...))
	}
	servers := map[string]object{}
	for _, s := range a.Servers {
		if a.Disabled {
			break
		}
		v := object{}
		set(v, "name", s.Name)
		set(v, "address", s.Address)
		set(v, "port", s.Port)
		set(v, "weight", s.Weight)
		set(v, "check", "enabled")
		set(v, "inter", 1000)
		set(v, "rise", 2)
		set(v, "fall", 2)
		set(v, "init-addr", "libc,none")
		set(v, "init-state", "fully-down")
		if s.Maintenance {
			set(v, "maintenance", "enabled")
		} else {
			set(v, "maintenance", "disabled")
		}
		servers[s.Name] = v
	}
	set(b, "servers", servers)
	return b, nil
}

// All changes share DPA's single versioned transaction with unmanaged backend
// edits. Existing frontend fields, binds, rules and fallback stay local/intact.
func (c *Client) applyApplications(ctx context.Context, transaction string, previous, desired []domain.HTTPApplication) error {
	if domain.SameApplications(previous, desired) {
		return nil
	}
	query := "&transaction_id=" + url.QueryEscape(transaction)
	frontends, err := c.frontends(ctx, query)
	if err != nil {
		return err
	}
	for _, a := range desired {
		if a.RequestTelemetry && !slices.ContainsFunc(frontends, func(f object) bool {
			return c.AllowRequestLogs && decode[string](f["name"]) == a.Listener && domain.ValidRequestLogFormat(decode[string](f["log_format"])) && (a.TrafficPolicy == nil || c.trafficProfile(f))
		}) {
			return failure("configuration_unsupported")
		}
	}
	known := map[string]bool{}
	for _, a := range previous {
		known[a.ID] = true
	}
	for _, a := range desired {
		unchanged := false
		for _, b := range previous {
			unchanged = unchanged || a.Equal(b)
		}
		if unchanged {
			continue
		}
		method, path := "POST", "/services/haproxy/configuration/backends?full_section=true"+query
		if known[a.ID] {
			method = "PUT"
			path = "/services/haproxy/configuration/backends/" + url.PathEscape(a.BackendName()) + "?full_section=true" + query
		}
		backend, err := c.applicationBackend(a)
		if err != nil {
			return err
		}
		// Defaults inheritance belongs to the local node. DPA exposes it after
		// a cold parse even when it was implicit when the section was created.
		if known[a.ID] {
			raw, _, _, err := c.call(ctx, "GET", "/services/haproxy/configuration/backends/"+url.PathEscape(a.BackendName())+"?full_section=true"+query, "", nil)
			if err != nil {
				return err
			}
			var existing object
			if json.Unmarshal(raw, &existing) != nil {
				return failure("configuration_unavailable")
			}
			if from, ok := existing["from"]; ok {
				backend["from"] = from
			}
		} else {
			for _, f := range frontends {
				if decode[string](f["name"]) == a.Listener {
					if from, ok := f["from"]; ok {
						backend["from"] = from
					}
				}
			}
		}
		payload, _ := json.Marshal(backend)
		if _, _, _, err = c.call(ctx, method, path, "application/json", payload); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, f := range frontends {
		name := decode[string](f["name"])
		rules := applicationRules(desired, name)
		before := applicationRules(previous, name)
		if len(rules) == 0 && len(before) == 0 {
			continue
		}
		if mode := decode[string](f["mode"]); mode != "" && mode != "http" {
			return failure("configuration_unsupported")
		}
		for _, rule := range decode[[]object](f["backend_switching_rule_list"]) {
			if !strings.HasPrefix(decode[string](rule["name"]), domain.ApplicationBackendPrefix) {
				rules = append(rules, rule)
			}
		}
		set(f, "backend_switching_rule_list", rules)
		payload, _ := json.Marshal(f)
		if _, _, _, err = c.call(ctx, "PUT", "/services/haproxy/configuration/frontends/"+url.PathEscape(name)+"?full_section=true"+query, "application/json", payload); err != nil {
			return err
		}
		seen[name] = true
	}
	for _, a := range desired {
		if !seen[a.Listener] {
			return failure("configuration_unsupported")
		}
	}
	for _, a := range previous {
		found := false
		for _, b := range desired {
			found = found || a.ID == b.ID
		}
		if !found {
			if _, _, _, err = c.call(ctx, "DELETE", "/services/haproxy/configuration/backends/"+url.PathEscape(a.BackendName())+"?transaction_id="+url.QueryEscape(transaction), "", nil); err != nil {
				return err
			}
		}
	}
	return nil
}
