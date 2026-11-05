package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
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

func livePoint(node domain.Node, name, value string) map[string]any {
	return map[string]any{"metric": map[string]string{"series": name, "organization_id": node.OrganizationID, "cluster_id": node.ClusterID, "node_id": node.ID}, "value": []any{time.Now().Unix(), value}}
}
func livePoints(nodes ...domain.Node) []any {
	result := []any{}
	for _, node := range nodes {
		result = append(result, livePoint(node, "up", "1"), livePoint(node, "observed_at", strconv.FormatFloat(float64(time.Now().UnixMilli())/1000, 'f', 3, 64)), livePoint(node, "traffic_out", "8192"))
	}
	return result
}
func readLiveFrame(t *testing.T, r *bufio.Reader) ClusterLive {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			var frame ClusterLive
			if err := json.Unmarshal([]byte(strings.TrimSpace(line[6:])), &frame); err != nil {
				t.Fatal(err)
			}
			return frame
		}
	}
}
func TestClusterLiveScopeCadenceAndBounds(t *testing.T) {
	var nodes []domain.Node
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		expression := q.Get("query")
		for _, n := range nodes {
			if !strings.Contains(expression, n.ID) || !strings.Contains(expression, n.OrganizationID) || !strings.Contains(expression, n.ClusterID) {
				t.Error("unscoped live query")
			}
		}
		if !strings.Contains(expression, "rate(") || !strings.Contains(expression, "[5s]") || q.Get("latency_offset") != "1ms" || q.Get("nocache") != "1" || q.Get("timeout") != "2s" {
			t.Error("live query did not bypass history delay")
		}
		fleetVector(w, livePoints(nodes...))
	}))
	defer upstream.Close()
	f := newFixture(t, "manager", upstream.URL)
	nodes = []domain.Node{enrollForFleet(t, f, f.org, f.cluster.ID), enrollForFleet(t, f, f.org, f.cluster.ID)}
	path := "/v1/clusters/" + f.cluster.ID + "/events?nodes=" + nodes[0].ID + "," + nodes[1].ID
	otherOrg := domain.NewID()
	otherCluster, _ := f.store.CreateCluster(t.Context(), otherOrg, "Foreign")
	foreign := enrollForFleet(t, f, otherOrg, otherCluster.ID)
	elsewhere, _ := f.store.CreateCluster(t.Context(), f.org, "Another cluster")
	neighbor := enrollForFleet(t, f, f.org, elsewhere.ID)
	for _, tc := range []struct {
		path string
		auth bool
		code int
	}{
		{path, false, 401}, {"/v1/clusters/" + otherCluster.ID + "/events?nodes=" + foreign.ID, true, 404},
		{"/v1/clusters/" + f.cluster.ID + "/events?nodes=" + foreign.ID, true, 404},
		{"/v1/clusters/" + f.cluster.ID + "/events?nodes=" + neighbor.ID, true, 404},
		{path + "&query=up", true, 400}, {path + "&nodes=" + nodes[0].ID, true, 400},
		{"/v1/clusters/" + f.cluster.ID + "/events?nodes=" + nodes[0].ID + "," + nodes[0].ID, true, 400},
		{"/v1/clusters/" + f.cluster.ID + "/events?nodes=" + strings.Repeat(nodes[0].ID+",", 13), true, 400},
	} {
		if got := f.request("GET", tc.path, nil, tc.auth); got.Code != tc.code {
			t.Fatalf("%s: %d want %d", tc.path, got.Code, tc.code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected requests reached metric storage")
	}
	server := httptest.NewServer(f.server)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+operatorToken)
	req.Header.Set("X-Organization-ID", f.org)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first := readLiveFrame(t, reader)
	at := time.Now()
	second := readLiveFrame(t, reader)
	if time.Since(at) > 2500*time.Millisecond || len(first.Nodes) != 2 || len(second.Nodes) != 2 || first.ClusterID != f.cluster.ID {
		t.Fatal("cluster cadence or shape mismatch")
	}
	for _, n := range second.Nodes {
		if !n.Fresh || !n.ScrapeOK || len(n.Series) != 1 || n.Series[0].Value != 8192 {
			t.Fatal("invalid live node", n)
		}
	}
}
func TestClusterLiveRejectsForeignAndMalformedStorage(t *testing.T) {
	for _, mode := range []string{"foreign tenant", "foreign node", "foreign cluster", "duplicate", "nonfinite", "unknown series", "too many"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, "manager", "http://metrics.invalid")
			node := enrollForFleet(t, f, f.org, f.cluster.ID)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				point := livePoint(node, "traffic_out", "42")
				labels := point["metric"].(map[string]string)
				points := []any{point}
				switch mode {
				case "foreign tenant":
					labels["organization_id"] = domain.NewID()
				case "foreign node":
					labels["node_id"] = domain.NewID()
				case "foreign cluster":
					labels["cluster_id"] = domain.NewID()
				case "duplicate":
					points = append(points, point)
				case "nonfinite":
					point["value"] = []any{time.Now().Unix(), "NaN"}
				case "unknown series":
					labels["series"] = "secret"
				case "too many":
					for range 65 {
						points = append(points, point)
					}
				}
				fleetVector(w, points)
			}))
			defer upstream.Close()
			f.server.metrics, _ = url.Parse(upstream.URL)
			if _, err := f.server.clusterLive(t.Context(), f.cluster, []domain.Node{node}); err == nil {
				t.Fatal("invalid live vector was accepted")
			}
		})
	}
}
func TestClusterLiveRechecksBrowserSessionAndNodeRevocation(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(strconv.FormatBool(expire), func(t *testing.T) {
			f := browserFixture(t)
			node := enrollForFleet(t, f, f.org, f.cluster.ID)
			pending, nonce := challenge(t, f.server)
			cookie := sessionCookie(t, exchange(t, f, pending, validClaims(f, nonce)))
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if expire {
					_ = f.store.DeleteBrowserSession(t.Context(), digest(cookie.Value))
				} else {
					_ = f.store.RevokeNode(t.Context(), f.org, node.ID)
				}
				fleetVector(w, livePoints(node))
			}))
			defer upstream.Close()
			f.server.metrics, _ = url.Parse(upstream.URL)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			req := httptest.NewRequest("GET", "/v1/clusters/"+f.cluster.ID+"/events?nodes="+node.ID, nil).WithContext(ctx)
			req.AddCookie(cookie)
			req.Header.Set("X-Organization-ID", f.org)
			recorder := httptest.NewRecorder()
			if !expire {
				ctx, cancel2 := context.WithCancel(ctx)
				defer cancel2()
				req = req.WithContext(ctx)
				go func() { time.Sleep(100 * time.Millisecond); cancel2() }()
			}
			f.server.ServeHTTP(recorder, req)
			raw, _ := io.ReadAll(recorder.Result().Body)
			if expire {
				if strings.Contains(string(raw), "8192") {
					t.Fatal("expired session received a frame")
				}
			} else {
				if strings.Contains(string(raw), "8192") || !strings.Contains(string(raw), `"revoked":true`) {
					t.Fatal("revoked node values published")
				}
			}
		})
	}
}

var _ store.ClusterNodesStore = (*store.Memory)(nil)
