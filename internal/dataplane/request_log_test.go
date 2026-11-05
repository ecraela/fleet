package dataplane

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ecraela/fleet/internal/domain"
)

func TestTelemetryIdentityIsCompilerOwnedAndRequiresLocalLoggingProfile(t *testing.T) {
	a := testApplication("/shop")
	legacy := applicationBackend(a)
	if _, ok := legacy["http_request_rule_list"]; ok {
		t.Fatal("collection enabled without opt-in")
	}
	a.RequestTelemetry = true
	b := applicationBackend(a)
	rules := decode[[]object](b["http_request_rule_list"])
	if len(rules) != 1 || decode[string](rules[0]["var_expr"]) != "str("+a.Digest()+")" || decode[string](rules[0]["var_scope"]) != "txn" {
		t.Fatal("missing trusted configuration identity", rules)
	}
	if _, _, err := projectApplications([]object{b}); err != nil {
		t.Fatal(err)
	}
	set(rules[0], "var_expr", "req.hdr(X-Application-ID)")
	set(b, "http_request_rule_list", rules)
	if _, _, err := projectApplications([]object{b}); err == nil {
		t.Fatal("client-controlled attribution accepted")
	}
	if _, _, err := projectApplications([]object{legacy}); err != nil {
		t.Fatal("legacy apps no longer discoverable", err)
	}
	f := object{}
	set(f, "name", "public")
	set(f, "default_backend", "legacy")
	writes := 0
	c := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			raw, _ := json.Marshal([]object{f})
			w.Write(raw)
			return
		}
		writes++
		w.WriteHeader(200)
	})
	if err := c.applyApplications(context.Background(), "test", nil, []domain.HTTPApplication{a}); err == nil || writes != 0 {
		t.Fatal("missing local socket accepted")
	}
	c.AllowRequestLogs = true
	if err := c.applyApplications(context.Background(), "test", nil, []domain.HTTPApplication{a}); err == nil || writes != 0 {
		t.Fatal("unmanaged logging profile overwritten")
	}
	set(f, "log_format", domain.RequestLogFormat)
	if err := c.applyApplications(context.Background(), "test", nil, []domain.HTTPApplication{a}); err != nil || writes != 2 {
		t.Fatal("supported logging profile refused", err, writes)
	}
	set(f, "log_format", domain.RequestPathLogFormat)
	if err := c.applyApplications(context.Background(), "paths", nil, []domain.HTTPApplication{a}); err != nil || writes != 4 {
		t.Fatal("path logging profile refused", err, writes)
	}
	set(f, "log_format", domain.RequestGeoLogFormat)
	if err := c.applyApplications(context.Background(), "geo", nil, []domain.HTTPApplication{a}); err != nil || writes != 6 {
		t.Fatal("Geo logging profile refused", err, writes)
	}
}

func TestLocalPathCapturePreservesManagedTLSGuardOrdering(t *testing.T) {
	policy, listener := listenerFixture(t)
	c := &Client{TLS: policy}
	bind, err := c.listenerObject(listener)
	if err != nil {
		t.Fatal(err)
	}
	app := testApplication("/shop")
	guards := listenerRules([]domain.HTTPApplication{app}, listener.Name)
	capture := object{}
	set(capture, "type", "set-var")
	set(capture, "var_scope", "txn")
	set(capture, "var_name", "cv_path")
	set(capture, "var_expr", "path")
	f := object{}
	set(f, "name", listener.Name)
	set(f, "description", listenerDescription("", listener))
	set(f, "binds", map[string]object{listenerBind: bind})
	set(f, "http_request_rule_list", append(append([]object{}, guards...), capture))
	if found, err := c.projectListeners([]object{f}, []domain.HTTPApplication{app}); err != nil || len(found) != 1 {
		t.Fatal("local capture after managed guards rejected", err)
	}
	set(f, "http_request_rule_list", append([]object{capture}, guards...))
	if _, err = c.projectListeners([]object{f}, []domain.HTTPApplication{app}); err == nil {
		t.Fatal("capture before managed TLS guard prefix accepted")
	}
}
