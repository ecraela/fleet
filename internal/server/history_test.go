package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ecraela/fleet/internal/domain"
)

func historyMatrix(w http.ResponseWriter, series []any) {
	writeJSON(w, 200, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": series}})
}

func historyPoint(node domain.Node, end int64) map[string]any {
	return map[string]any{
		"metric": map[string]string{"series": "traffic_in", "organization_id": node.OrganizationID, "cluster_id": node.ClusterID, "node_id": node.ID},
		"values": []any{[]any{end - 15, "80"}, []any{end, "160"}},
	}
}

func TestHistoryScopesAndBoundsQueries(t *testing.T) {
	var node domain.Node
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		query := q.Get("query")
		if r.URL.Path != "/api/v1/query_range" || !strings.Contains(query, `organization_id="`+node.OrganizationID+`"`) || !strings.Contains(query, `cluster_id="`+node.ClusterID+`"`) || !strings.Contains(query, `node_id="`+node.ID+`"`) || !strings.Contains(query, "converge_agent_scrape_timestamp_seconds") || !strings.Contains(query, "converge_agent_haproxy_up") {
			t.Error("unscoped or ungated history query")
		}
		start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
		end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
		step, _ := strconv.ParseInt(q.Get("step"), 10, 64)
		if end-start > 86400 || (end-start)/step > 360 || step < 5 || q.Get("timeout") != "2s" || q.Get("nocache") != "1" {
			t.Error("unbounded history query")
		}
		historyMatrix(w, []any{historyPoint(node, end)})
	}))
	defer upstream.Close()
	f := newFixture(t, "manager", upstream.URL)
	node = enrollForFleet(t, f, f.org, f.cluster.ID)
	route := "/v1/nodes/" + node.ID + "/history"
	for _, window := range []string{"15m", "1h", "6h", "24h"} {
		got := f.request("GET", route+"?range="+window, nil, true)
		var response History
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &response) != nil || response.NodeID != node.ID || response.Range != window || len(response.Series) != 1 || response.Series[0].Values[1][1] != 160 {
			t.Fatalf("history failed: %d %s", got.Code, got.Body.String())
		}
	}
	for _, query := range []string{"?range=7d", "?range=1h&query=up", "?range=1h&range=24h", "?start=0"} {
		if got := f.request("GET", route+query, nil, true); got.Code != 400 {
			t.Fatalf("accepted caller-controlled history query %s: %d", query, got.Code)
		}
	}
	foreignOrg := domain.NewID()
	foreignCluster, _ := f.store.CreateCluster(t.Context(), foreignOrg, "Elsewhere")
	foreignNode := enrollForFleet(t, f, foreignOrg, foreignCluster.ID)
	if got := f.request("GET", "/v1/nodes/"+foreignNode.ID+"/history", nil, true); got.Code != 404 {
		t.Fatal("foreign node history was visible")
	}
	if got := f.request("GET", route, nil, false); got.Code != 401 {
		t.Fatal("anonymous history was visible")
	}
	_ = f.store.RevokeNode(t.Context(), f.org, node.ID)
	if got := f.request("GET", route, nil, true); got.Code != 403 {
		t.Fatal("revoked node history was visible")
	}
	if calls.Load() != 4 {
		t.Fatal("rejected history requests reached storage")
	}
}

func TestHistoryRejectsInvalidStorageResults(t *testing.T) {
	for _, mode := range []string{"foreign tenant", "foreign node", "foreign cluster", "nonfinite", "unordered", "out of range", "unknown series", "duplicate", "too many points", "too many series", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			var node domain.Node
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
				point := historyPoint(node, end)
				labels := point["metric"].(map[string]string)
				points := []any{point}
				switch mode {
				case "foreign tenant":
					labels["organization_id"] = domain.NewID()
				case "foreign node":
					labels["node_id"] = domain.NewID()
				case "foreign cluster":
					labels["cluster_id"] = domain.NewID()
				case "nonfinite":
					point["values"] = []any{[]any{end, "NaN"}}
				case "unordered":
					point["values"] = []any{[]any{end, "1"}, []any{end - 15, "2"}}
				case "out of range":
					point["values"] = []any{[]any{end + 15, "1"}}
				case "unknown series":
					labels["series"] = "private_metric"
				case "duplicate":
					points = append(points, point)
				case "too many points":
					values := make([]any, 362)
					for i := range values {
						values[i] = []any{end - 362 + int64(i), "1"}
					}
					point["values"] = values
				case "too many series":
					points = make([]any, 65)
					for i := range points {
						points[i] = point
					}
				case "unavailable":
					w.WriteHeader(503)
					return
				}
				historyMatrix(w, points)
			}))
			defer upstream.Close()
			f := newFixture(t, "manager", upstream.URL)
			node = enrollForFleet(t, f, f.org, f.cluster.ID)
			got := f.request("GET", "/v1/nodes/"+node.ID+"/history", nil, true)
			if got.Code != 503 || strings.Contains(got.Body.String(), "traffic_in") {
				t.Fatalf("invalid history was published: %s", got.Body.String())
			}
		})
	}
}

func TestHistoryBrowserReaderAndRevocationDuringQuery(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(strconv.FormatBool(revoke), func(t *testing.T) {
			f := browserFixture(t)
			node := enrollForFleet(t, f, f.org, f.cluster.ID)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if revoke {
					_ = f.store.RevokeNode(r.Context(), f.org, node.ID)
				}
				historyMatrix(w, []any{})
			}))
			defer upstream.Close()
			f.server.metrics, _ = url.Parse(upstream.URL)
			pending, nonce := challenge(t, f.server)
			cookie := sessionCookie(t, exchange(t, f, pending, validClaims(f, nonce)))
			route := "/v1/nodes/" + node.ID + "/history"
			got := browserRequest(t, f.server, "GET", route, "", cookie)
			want := 200
			if revoke {
				want = 403
			}
			if got.Code != want {
				t.Fatalf("history status %d, want %d", got.Code, want)
			}
			request := httptest.NewRequest("GET", route, nil)
			request.AddCookie(cookie)
			request.Header.Set("X-Organization-ID", domain.NewID())
			denied := httptest.NewRecorder()
			f.server.ServeHTTP(denied, request)
			if denied.Code != 401 {
				t.Fatal("browser history accepted a different tenant")
			}
		})
	}
}
