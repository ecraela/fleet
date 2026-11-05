package server

import (
	"encoding/json"
	"github.com/ecraela/fleet/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTopologyEndpointTenantAndNodeBinding(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	node := enrollForFleet(t, f, f.org, f.cluster.ID)
	topology := domain.Topology{Entrypoint: "http://localhost:18081/", Strategy: "roundrobin", Frontend: "public", Backend: "application:8080", Nodes: []domain.TopologyNode{{NodeID: node.ID, Address: "a:8080"}}}
	route := "/v1/clusters/" + f.cluster.ID + "/topology"
	if got := f.request("PUT", route, topology, false); got.Code != 401 {
		t.Fatal("anonymous mutation accepted")
	}
	if got := f.request("PUT", route, topology, true); got.Code != 200 {
		t.Fatalf("topology failed %d %s", got.Code, got.Body.String())
	}
	foreign := domain.NewID()
	cluster, _ := f.store.CreateCluster(t.Context(), foreign, "Foreign")
	if got := f.request("PUT", "/v1/clusters/"+cluster.ID+"/topology", topology, true); got.Code != 404 {
		t.Fatal("foreign cluster visible")
	}
	topology.Nodes[0].NodeID = enrollForFleet(t, f, foreign, cluster.ID).ID
	if got := f.request("PUT", route, topology, true); got.Code != 404 {
		t.Fatal("foreign node bound")
	}
	clusters := f.request("GET", "/v1/clusters", nil, true)
	if strings.Contains(clusters.Body.String(), cluster.ID) || !strings.Contains(clusters.Body.String(), "haproxy") && !strings.Contains(clusters.Body.String(), "18081") {
		t.Fatal("topology inventory scope mismatch")
	}
	query := scopedHistoryQuery(node, 5*time.Second, `public"{tenant="other`)
	if !strings.Contains(query, `proxy="public\"{tenant=\"other"`) {
		t.Fatal("frontend selector not escaped")
	}
	if !strings.Contains(scopedHistoryQuery(node, 5*time.Second, "public"), `haproxy_frontend_current_sessions`) {
		t.Fatal("connections not scoped to traffic frontend")
	}
}

func TestTopologyBrowserCapabilityAndCSRF(t *testing.T) {
	f := browserFixture(t)
	node := enrollForFleet(t, f, f.org, f.cluster.ID)
	topology := domain.Topology{Entrypoint: "http://localhost:18081/", Strategy: "roundrobin", Frontend: "public", Backend: "application:8080", Nodes: []domain.TopologyNode{{NodeID: node.ID, Address: "a:8080"}}}
	raw, _ := json.Marshal(topology)
	for _, manage := range []bool{false, true} {
		pending, nonce := challenge(t, f.server)
		claims := validClaims(f, nonce)
		if manage {
			claims.Capabilities = append(claims.Capabilities, "haproxy.manage")
		}
		created := exchange(t, f, pending, claims)
		cookie := sessionCookie(t, created)
		var body struct {
			CSRF string `json:"csrf_token"`
		}
		_ = json.Unmarshal(created.Body.Bytes(), &body)
		for _, csrf := range []string{"", body.CSRF} {
			request := httptest.NewRequest(http.MethodPut, "/v1/clusters/"+f.cluster.ID+"/topology", strings.NewReader(string(raw)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", testBrowserOrigin)
			request.Header.Set("X-CSRF-Token", csrf)
			request.AddCookie(cookie)
			got := httptest.NewRecorder()
			f.server.ServeHTTP(got, request)
			want := 403
			if manage && csrf != "" {
				want = 200
			}
			if got.Code != want {
				t.Fatalf("manage=%v csrf=%v got=%d want=%d", manage, csrf != "", got.Code, want)
			}
		}
	}
}
