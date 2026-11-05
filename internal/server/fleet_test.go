package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

type fleetResponse struct {
	Clusters   []domain.Cluster `json:"clusters"`
	Nodes      []FleetNode      `json:"nodes"`
	NextCursor *string          `json:"next_cursor"`
	Partial    bool             `json:"partial"`
}

func vectorPoint(node domain.Node, name, value string) map[string]any {
	return map[string]any{"metric": map[string]string{"__name__": name, "organization_id": node.OrganizationID, "cluster_id": node.ClusterID, "node_id": node.ID}, "value": []any{time.Now().Unix(), value}}
}

func fleetVector(w http.ResponseWriter, points []any) {
	writeJSON(w, 200, map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": points}})
}

func enrollForFleet(t *testing.T, f *fixture, organizationID, clusterID string) domain.Node {
	t.Helper()
	token := secret()
	if err := f.store.CreateEnrollmentToken(t.Context(), organizationID, clusterID, digest(token), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	node, err := f.store.Enroll(t.Context(), digest(token), domain.Node{ID: domain.NewID(), Name: "node"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func TestFleetTenantIsolationAndPagination(t *testing.T) {
	var forbidden string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), forbidden) {
			t.Error("query included another tenant")
		}
		fleetVector(w, []any{})
	}))
	defer upstream.Close()
	f := newFixture(t, "manager", upstream.URL)
	otherOrg := domain.NewID()
	otherCluster, err := f.store.CreateCluster(t.Context(), otherOrg, "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	foreign := enrollForFleet(t, f, otherOrg, otherCluster.ID)
	forbidden = foreign.ID
	for range store.FleetPageSize + 1 {
		enrollForFleet(t, f, f.org, f.cluster.ID)
	}
	get := f.request("GET", "/v1/fleet", nil, true)
	var first fleetResponse
	if get.Code != 200 || json.Unmarshal(get.Body.Bytes(), &first) != nil {
		t.Fatalf("fleet failed: %d %s", get.Code, get.Body.String())
	}
	if len(first.Nodes) != store.FleetPageSize || first.NextCursor == nil || first.Partial || len(first.Clusters) != 1 || first.Clusters[0].ID != f.cluster.ID || strings.Contains(get.Body.String(), foreign.ID) || strings.Contains(get.Body.String(), otherOrg) {
		t.Fatal("fleet leaked a tenant or lost pagination")
	}
	seen := make(map[string]bool)
	for _, node := range first.Nodes {
		seen[node.ID] = true
	}
	secondResponse := f.request("GET", "/v1/fleet?cursor="+*first.NextCursor, nil, true)
	var second fleetResponse
	json.Unmarshal(secondResponse.Body.Bytes(), &second)
	if secondResponse.Code != 200 || len(second.Nodes) != 1 || second.NextCursor != nil || seen[second.Nodes[0].ID] {
		t.Fatal("cursor duplicated or omitted inventory")
	}
	if got := f.request("GET", "/v1/fleet?cursor=invalid", nil, true); got.Code != 400 {
		t.Fatal("invalid fleet cursor accepted")
	}
	if got := f.request("GET", "/v1/clusters", nil, true); got.Code != 200 || strings.Contains(got.Body.String(), otherOrg) {
		t.Fatal("cluster inventory tenant boundary failed")
	}
	if got := f.request("GET", "/v1/fleet", nil, false); got.Code != 401 {
		t.Fatal("unauthenticated fleet was visible")
	}
}

func TestFleetFreshnessAndUnavailableMetrics(t *testing.T) {
	for _, tc := range []struct {
		name          string
		age           time.Duration
		up            string
		unavailable   bool
		fresh, scrape bool
	}{{"live", 0, "1.0", false, true, true}, {"stale", time.Minute, "1", false, false, true}, {"scrape failed", 0, "0", false, true, false}, {"storage unavailable", 0, "1", true, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var node domain.Node
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query().Get("query")
				if !strings.Contains(query, `organization_id="`+node.OrganizationID+`"`) || !strings.Contains(query, node.ID) {
					t.Error("unscoped fleet query")
				}
				if tc.unavailable {
					w.WriteHeader(503)
					return
				}
				fleetVector(w, []any{vectorPoint(node, "converge_agent_haproxy_up", tc.up), vectorPoint(node, "converge_agent_scrape_timestamp_seconds", strconv.FormatFloat(float64(time.Now().Add(-tc.age).UnixMilli())/1000, 'f', 3, 64))})
			}))
			defer upstream.Close()
			f := newFixture(t, "manager", upstream.URL)
			node = enrollForFleet(t, f, f.org, f.cluster.ID)
			w := f.request("GET", "/v1/fleet", nil, true)
			var response fleetResponse
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Nodes) != 1 {
				t.Fatal(w.Body.String())
			}
			got := response.Nodes[0]
			if response.Partial != tc.unavailable || got.MetricsAvailable == tc.unavailable || got.TelemetryFresh != tc.fresh || got.ScrapeOK != tc.scrape || len(response.Clusters) != 1 {
				t.Fatalf("wrong fleet freshness/partial result: %s", w.Body.String())
			}
			if tc.unavailable && got.ObservedAt != nil {
				t.Fatal("unavailable query fabricated observation time")
			}
		})
	}
}

func TestFleetRejectsInvalidObservationsAtomically(t *testing.T) {
	for name, mutate := range map[string]func(domain.Node, []any) []any{
		"foreign tenant": func(n domain.Node, p []any) []any {
			bad := n
			bad.OrganizationID = domain.NewID()
			p[1] = vectorPoint(bad, "converge_agent_scrape_timestamp_seconds", "1")
			return p
		},
		"foreign cluster": func(n domain.Node, p []any) []any {
			n.ClusterID = domain.NewID()
			p[1] = vectorPoint(n, "converge_agent_scrape_timestamp_seconds", "1")
			return p
		},
		"foreign node": func(n domain.Node, p []any) []any {
			n.ID = domain.NewID()
			p[1] = vectorPoint(n, "converge_agent_scrape_timestamp_seconds", "1")
			return p
		},
		"duplicate":  func(n domain.Node, p []any) []any { p[1] = p[0]; return p },
		"invalid up": func(n domain.Node, p []any) []any { p[0] = vectorPoint(n, "converge_agent_haproxy_up", "2"); return p },
		"invalid timestamp": func(n domain.Node, p []any) []any {
			p[1] = vectorPoint(n, "converge_agent_scrape_timestamp_seconds", "0")
			return p
		},
		"nonfinite": func(n domain.Node, p []any) []any {
			p[1] = vectorPoint(n, "converge_agent_scrape_timestamp_seconds", "NaN")
			return p
		},
	} {
		t.Run(name, func(t *testing.T) {
			var node domain.Node
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fleetVector(w, mutate(node, []any{vectorPoint(node, "converge_agent_haproxy_up", "1"), vectorPoint(node, "converge_agent_scrape_timestamp_seconds", strconv.FormatInt(time.Now().Unix(), 10))}))
			}))
			defer upstream.Close()
			f := newFixture(t, "manager", upstream.URL)
			node = enrollForFleet(t, f, f.org, f.cluster.ID)
			w := f.request("GET", "/v1/fleet", nil, true)
			var got fleetResponse
			json.Unmarshal(w.Body.Bytes(), &got)
			if w.Code != 200 || !got.Partial || len(got.Nodes) != 1 || got.Nodes[0].MetricsAvailable || got.Nodes[0].ObservedAt != nil {
				t.Fatal("invalid storage response published partial scoped observations")
			}
		})
	}
}

func TestFleetOmitsRevokedNodesFromQueries(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fleetVector(w, []any{}) }))
	defer upstream.Close()
	f := newFixture(t, "manager", upstream.URL)
	node := enrollForFleet(t, f, f.org, f.cluster.ID)
	if err := f.store.RevokeNode(t.Context(), f.org, node.ID); err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/v1/fleet", nil, true)
	var response fleetResponse
	json.Unmarshal(w.Body.Bytes(), &response)
	if w.Code != 200 || calls.Load() != 0 || len(response.Nodes) != 1 || response.Nodes[0].RevokedAt == nil || response.Nodes[0].MetricsAvailable {
		t.Fatal("revoked inventory caused telemetry queries")
	}
}

func TestBrowserReaderFleetRoutesRemainTenantBound(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fleetVector(w, []any{}) }))
	defer upstream.Close()
	f := browserFixture(t)
	f.server.metrics, _ = url.Parse(upstream.URL)
	pending, nonce := challenge(t, f.server)
	cookie := sessionCookie(t, exchange(t, f, pending, validClaims(f, nonce)))
	for _, route := range []string{"/v1/clusters", "/v1/fleet", "/v1/nodes"} {
		w := browserRequest(t, f.server, "GET", route, "", cookie)
		if w.Code != 200 {
			t.Fatalf("reader could not access %s: %d", route, w.Code)
		}
		request := httptest.NewRequest("GET", route, nil)
		request.AddCookie(cookie)
		request.Header.Set("X-Organization-ID", domain.NewID())
		denied := httptest.NewRecorder()
		f.server.ServeHTTP(denied, request)
		if denied.Code != 401 {
			t.Fatalf("reader supplied a different tenant to %s", route)
		}
	}
}

func TestNodeEventsRechecksSessionAndRevocationAfterQuery(t *testing.T) {
	for _, expire := range []bool{true, false} {
		t.Run(strconv.FormatBool(expire), func(t *testing.T) {
			var f *fixture
			var node domain.Node
			var browserCookie *http.Cookie
			var expired atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if expire {
					expired.Store(true)
				} else {
					if err := f.store.RevokeNode(r.Context(), f.org, node.ID); err != nil {
						t.Error(err)
					}
				}
				fleetVector(w, []any{})
			}))
			defer upstream.Close()
			f = browserFixture(t)
			f.server.config.Store = &browserClockStore{Memory: f.store, expired: &expired}
			f.server.metrics, _ = url.Parse(upstream.URL)
			node = enrollForFleet(t, f, f.org, f.cluster.ID)
			pending, nonce := challenge(t, f.server)
			browserCookie = sessionCookie(t, exchange(t, f, pending, validClaims(f, nonce)))
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			request := httptest.NewRequest("GET", "/v1/nodes/"+node.ID+"/events", nil).WithContext(ctx)
			request.AddCookie(browserCookie)
			w := httptest.NewRecorder()
			f.server.ServeHTTP(w, request)
			if strings.Contains(w.Body.String(), "event:") {
				t.Fatal("SSE published after identity expired or node was revoked during query")
			}
		})
	}
}

type browserClockStore struct {
	*store.Memory
	expired *atomic.Bool
}

func (s *browserClockStore) GetBrowserSession(ctx context.Context, hash string, now time.Time) (domain.BrowserSession, error) {
	if s.expired.Load() {
		now = now.Add(time.Minute)
	}
	return s.Memory.GetBrowserSession(ctx, hash, now)
}

func TestNodeSnapshotMatchesFleetNumericStatusContract(t *testing.T) {
	for _, value := range []string{"1.0", "2", "NaN"} {
		t.Run(value, func(t *testing.T) {
			var node domain.Node
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fleetVector(w, []any{vectorPoint(node, "converge_agent_haproxy_up", value), vectorPoint(node, "converge_agent_scrape_timestamp_seconds", strconv.FormatInt(time.Now().Unix(), 10))})
			}))
			defer upstream.Close()
			f := newFixture(t, "manager", upstream.URL)
			node = enrollForFleet(t, f, f.org, f.cluster.ID)
			w := f.request("GET", "/v1/nodes/"+node.ID+"/metrics", nil, true)
			if value != "1.0" {
				if w.Code != 503 {
					t.Fatal("invalid stored status accepted")
				}
				return
			}
			var snap Snapshot
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snap) != nil || !snap.ScrapeOK || !snap.Fresh {
				t.Fatal("valid numeric status disagreed with fleet")
			}
		})
	}
}
