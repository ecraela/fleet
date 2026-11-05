package server

import (
	"encoding/json"
	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/domain"
	"net/http"
	"strings"
	"testing"
)

func TestTrafficRuleCountsRemainBoundToTenantApplicationAndVersion(t *testing.T) {
	f, rev, _, _ := requestFixture(t)
	id := domain.NewID()
	rev.Spec.TrafficPolicy = &domain.TrafficPolicy{Version: 1, Mode: "observe", UnknownAction: "allow", Rules: []domain.TrafficRule{{ID: id, Name: "Countries", Action: "block", Countries: []string{"RU"}}}}
	f.server.config.Store = requestStore{Memory: f.store, rows: []domain.ApplicationRevision{rev}}
	corrupt := false
	calls := 0
	f.server.logs, _ = accesslog.NewStorage("http://logs.invalid", &http.Client{Transport: requestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		r.ParseForm()
		q := r.Form.Get("query")
		if strings.Contains(q, "kind:=pipeline") {
			return logResponse(""), nil
		}
		for _, scope := range []string{`organization_id:="` + f.org + `"`, `cluster_id:="` + f.cluster.ID + `"`, `application_id:="` + rev.ApplicationID + `"`, `spec_digest:="` + rev.Spec.Digest() + `"`} {
			if !strings.Contains(q, scope) {
				t.Error("missing scope", q)
			}
		}
		row := map[string]string{"traffic_rule": id, "traffic_decision": "would_block", "requests": "3"}
		if corrupt {
			row["traffic_rule"] = domain.NewID()
		}
		raw, _ := json.Marshal(row)
		return logResponse(string(raw) + "\n"), nil
	})})
	base := "/v1/clusters/" + f.cluster.ID + "/applications/" + rev.ApplicationID + "/traffic-rules"
	query := "?spec_digest=" + rev.Spec.Digest()
	if got := f.request("GET", base+query, nil, false); got.Code != 401 {
		t.Fatal("anonymous", got.Code)
	}
	foreign, _ := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
	path := "/v1/clusters/" + foreign.ID + "/applications/" + rev.ApplicationID + "/traffic-rules" + query
	if got := f.request("GET", path, nil, true); got.Code != 404 {
		t.Fatal("foreign", got.Code)
	}
	for _, query := range []string{"", "?traffic_rule=ip%7Cstats", "?traffic_decision=raw", "?traffic_rule=unknown&traffic_rule=unknown"} {
		if got := f.request("GET", base+query, nil, true); got.Code != 400 {
			t.Fatal("invalid filter", query, got.Code)
		}
	}
	if calls != 0 {
		t.Fatal("invalid request reached storage")
	}
	if got := f.request("GET", base+query, nil, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"requests":3`) {
		t.Fatal(got.Code, got.Body.String())
	}
	corrupt = true
	if got := f.request("GET", base+query, nil, true); got.Code != 503 {
		t.Fatal("unknown stored rule escaped validation", got.Code)
	}
}
func TestTrafficDecisionsMustMatchTheAttributedPolicy(t *testing.T) {
	id := domain.NewID()
	a := domain.HTTPApplication{TrafficPolicy: &domain.TrafficPolicy{Version: 1, Mode: "enforce", UnknownAction: "block", Rules: []domain.TrafficRule{{ID: id, Action: "allow"}}}}
	for _, tc := range []struct {
		id, decision string
		status       int
		want         bool
	}{{id, "allow", 200, true}, {id, "block", 403, false}, {domain.NewID(), "allow", 200, false}, {"unknown", "block", 403, true}, {"unknown", "block", 200, false}, {"unknown", "would_block", 200, false}} {
		if validTrafficEvidence(a, accesslog.TrafficDecision{RuleID: tc.id, Decision: tc.decision}, tc.status) != tc.want {
			t.Fatal(tc)
		}
	}
	a.TrafficPolicy.Mode = "observe"
	if !validTrafficEvidence(a, accesslog.TrafficDecision{RuleID: "unknown", Decision: "would_block"}, 200) {
		t.Fatal("observe match rejected")
	}
}
