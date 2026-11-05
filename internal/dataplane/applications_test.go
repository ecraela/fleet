package dataplane

import (
	"encoding/json"
	"github.com/ecraela/fleet/internal/domain"
	"net/http"
	"testing"
)

func testApplication(path string) domain.HTTPApplication {
	return domain.HTTPApplication{ID: domain.NewID(), Name: "Store", Listener: "public", Hostname: "apps.example.test", PathPrefix: path, Algorithm: "roundrobin", HealthPath: "/healthz", Servers: []domain.ConfigurationServer{{Name: "web", Address: "backend.internal", Port: 8080, Weight: 100}}}
}
func TestApplicationProjectionVerifiesOwnedHealthAndRoutes(t *testing.T) {
	a, b := testApplication("/shop"), testApplication("/shop/admin")
	owned := []object{applicationBackend(a), applicationBackend(b)}
	set(owned[0], "from", "node_defaults")
	_, apps, e := projectApplications(owned)
	if e != nil || len(apps) != 2 {
		t.Fatal("safe projection", e)
	}
	rules := applicationRules(apps, "public")
	if decode[string](rules[0]["name"]) != b.BackendName() {
		t.Fatal("longest path did not win")
	}
	f := object{}
	set(f, "name", "public")
	set(f, "backend_switching_rule_list", rules)
	client := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/services/haproxy/configuration/frontends" {
			t.Error("unexpected path", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		payload, _ := json.Marshal([]object{f})
		_, _ = w.Write(payload)
	})
	if e = client.verifyApplicationRoutes(t.Context(), apps); e != nil {
		t.Fatal("matching routes", e)
	}
	set(f, "backend_switching_rule_list", []object{rules[1], rules[0]})
	if e = client.verifyApplicationRoutes(t.Context(), apps); e == nil {
		t.Fatal("external route precedence accepted")
	}
	foreign := object{}
	set(foreign, "name", "unmanaged")
	set(f, "backend_switching_rule_list", append([]object{foreign}, rules...))
	if e = client.verifyApplicationRoutes(t.Context(), apps); e == nil {
		t.Fatal("external rule inserted ahead of app ownership was accepted")
	}
	set(owned[0], "server_timeout", 42)
	if _, _, e = projectApplications(owned); e == nil {
		t.Fatal("unrecognized local app edit would be silently discarded")
	}
	delete(owned[0], "server_timeout")
	set(owned[0], "httpchk_params", map[string]string{"method": "GET", "uri": "/different"})
	if _, _, e = projectApplications(owned); e == nil {
		t.Fatal("externally changed check trusted")
	}
	set(owned[0], "description", "unmanaged")
	if _, _, e = projectApplications(owned); e == nil {
		t.Fatal("reserved name collision adopted")
	}
}
func TestApplicationTransactionPreservesUnmanagedFrontend(t *testing.T) {
	a := testApplication("/")
	var sawFrontend, sawBackend bool
	f := object{}
	set(f, "name", "public")
	set(f, "default_backend", "legacy")
	set(f, "from", "local_defaults")
	set(f, "binds", map[string]any{"http": map[string]any{"name": "http", "address": "*", "port": 8080}})
	set(f, "backend_switching_rule_list", []object{{"name": json.RawMessage(`"legacy-rule"`), "cond": json.RawMessage(`"if"`), "cond_test": json.RawMessage(`"{ path_beg /legacy }"`)}})
	c := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("transaction_id") != "test-transaction" {
			t.Error("write/read escaped transaction")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v3/services/haproxy/configuration/frontends":
			payload, _ := json.Marshal([]object{f})
			_, _ = w.Write(payload)
		case "POST /v3/services/haproxy/configuration/backends":
			sawBackend = true
			var b object
			_ = json.NewDecoder(r.Body).Decode(&b)
			if decode[string](b["adv_check"]) != "httpchk" || decode[string](b["from"]) != "local_defaults" {
				t.Error("missing HTTP check")
			}
		case "PUT /v3/services/haproxy/configuration/frontends/public":
			sawFrontend = true
			var changed object
			_ = json.NewDecoder(r.Body).Decode(&changed)
			rules := decode[[]object](changed["backend_switching_rule_list"])
			if len(rules) != 2 || decode[string](rules[1]["name"]) != "legacy-rule" || decode[string](changed["default_backend"]) != "legacy" || string(changed["binds"]) != string(f["binds"]) {
				t.Error("unmanaged listener settings changed")
			}
		default:
			t.Error("unexpected mutation", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	if e := c.applyApplications(t.Context(), "test-transaction", nil, []domain.HTTPApplication{a}); e != nil || !sawFrontend || !sawBackend {
		t.Fatal("application transaction", e, sawFrontend, sawBackend)
	}
}

func TestEditingAnApplicationPreservesItsLocalDefaultsAfterRestart(t *testing.T) {
	a := testApplication("/shop")
	changed := a
	changed.PathPrefix = "/store"
	backend := applicationBackend(a)
	set(backend, "from", "backend_defaults")
	frontend := object{}
	set(frontend, "name", "public")
	set(frontend, "mode", "http")
	set(frontend, "from", "frontend_defaults")
	wrote := false
	c := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("transaction_id") != "local" {
			t.Error("escaped transaction")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v3/services/haproxy/configuration/frontends":
			_ = json.NewEncoder(w).Encode([]object{frontend})
		case "GET /v3/services/haproxy/configuration/backends/" + a.BackendName():
			_ = json.NewEncoder(w).Encode(backend)
		case "PUT /v3/services/haproxy/configuration/backends/" + a.BackendName():
			wrote = true
			var next object
			_ = json.NewDecoder(r.Body).Decode(&next)
			if decode[string](next["from"]) != "backend_defaults" {
				t.Error("local backend defaults were replaced")
			}
		case "PUT /v3/services/haproxy/configuration/frontends/public":
		default:
			t.Error("unexpected local request", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	if err := c.applyApplications(t.Context(), "local", []domain.HTTPApplication{a}, []domain.HTTPApplication{changed}); err != nil || !wrote {
		t.Fatal("application edit", err, wrote)
	}
}

func TestDisabledApplicationKeepsRouteAndHasNoUpstreamServers(t *testing.T) {
	a, b := testApplication("/shop"), testApplication("/shop/admin")
	b.Disabled = true
	backend := applicationBackend(b)
	if len(decode[map[string]object](backend["servers"])) != 0 {
		t.Fatal("disabled backend still connects to endpoints")
	}
	// DPA omits an empty server map after parsing saved configuration.
	delete(backend, "servers")
	_, apps, e := projectApplications([]object{applicationBackend(a), backend})
	if e != nil || len(apps) != 2 || !apps[1].Disabled {
		t.Fatal("disabled projection", e)
	}
	rules := applicationRules(apps, "public")
	if decode[string](rules[0]["name"]) != b.BackendName() {
		t.Fatal("disabled nested route fell through to parent")
	}
	set(backend, "servers", decode[map[string]object](applicationBackend(a)["servers"]))
	if _, _, e = projectApplications([]object{backend}); e == nil {
		t.Fatal("disabled backend with an endpoint was trusted")
	}
}

func TestApplicationRemovalPreservesNeighborAndListenerBinds(t *testing.T) {
	a, b := testApplication("/shop"), testApplication("/shop/admin")
	f := object{}
	set(f, "name", "public")
	set(f, "mode", "http")
	set(f, "binds", map[string]object{"https": {"name": json.RawMessage(`"https"`), "port": json.RawMessage(`8443`), "ssl": json.RawMessage(`true`)}})
	set(f, "backend_switching_rule_list", applicationRules([]domain.HTTPApplication{a, b}, "public"))
	deleted, wrote := false, false
	c := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("transaction_id") != "remove-app" {
			t.Error("escaped transaction")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v3/services/haproxy/configuration/frontends":
			_ = json.NewEncoder(w).Encode([]object{f})
		case "PUT /v3/services/haproxy/configuration/frontends/public":
			wrote = true
			var changed object
			_ = json.NewDecoder(r.Body).Decode(&changed)
			rules := decode[[]object](changed["backend_switching_rule_list"])
			if len(rules) != 1 || decode[string](rules[0]["name"]) != b.BackendName() || normalizedObject(decode[object](changed["binds"])) != normalizedObject(decode[object](f["binds"])) {
				t.Error("neighbor route or TLS bind changed")
			}
		case "DELETE /v3/services/haproxy/configuration/backends/" + a.BackendName():
			deleted = true
		default:
			t.Errorf("unrelated mutation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	})
	if e := c.applyApplications(t.Context(), "remove-app", []domain.HTTPApplication{a, b}, []domain.HTTPApplication{b}); e != nil || !deleted || !wrote {
		t.Fatal("owned removal", e)
	}
}
