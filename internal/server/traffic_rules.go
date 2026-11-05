package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/domain"
)

func validTrafficEvidence(a domain.HTTPApplication, d accesslog.TrafficDecision, status int) bool {
	p := a.TrafficPolicy
	if p == nil || !d.Valid() {
		return false
	}
	action := ""
	if d.RuleID == "unknown" {
		action = p.UnknownAction
	} else {
		for _, r := range p.Rules {
			if r.ID == d.RuleID {
				action = r.Action
			}
		}
	}
	if action == "" {
		return false
	}
	if p.Mode == "observe" {
		return d.Decision == "would_"+action
	}
	return d.Decision == action && (action != "block" || status == 403)
}

type TrafficRuleCount struct {
	RuleID   string `json:"rule_id"`
	Decision string `json:"decision"`
	Requests uint64 `json:"requests"`
}

func (s *Server) applicationTrafficRules(w http.ResponseWriter, r *http.Request, org string) {
	versions, nodes, f, ok := s.requestResource(w, r, org)
	if !ok {
		return
	}
	var spec domain.HTTPApplication
	for _, v := range versions {
		if r.PathValue("revision") != "" && v.ID == r.PathValue("revision") {
			if f.digest != "" && f.digest != v.Spec.Digest() {
				fail(w, 400, "policy version differs from request filter")
				return
			}
			f.digest = v.Spec.Digest()
			spec = v.Spec
			break
		}
		if r.PathValue("revision") == "" && v.Spec.Digest() == f.digest {
			spec = v.Spec
		}
	}
	if r.PathValue("revision") != "" && spec.ID == "" {
		fail(w, 404, "application version not found")
		return
	}
	if spec.TrafficPolicy == nil || f.before != nil {
		fail(w, 400, "a traffic policy version is required")
		return
	}
	if !acquire(s.logQueries) {
		fail(w, 429, "request query capacity reached")
		return
	}
	defer func() { <-s.logQueries }()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	base := requestScope(org, r.PathValue("cluster"), r.PathValue("application"), f)
	rows, err := s.logs.Query(ctx, base+` traffic_rule:~".+" | stats by (traffic_rule, traffic_decision) count() as requests | sort by (traffic_rule, traffic_decision) limit 37`, f.start, f.end, 37)
	if err != nil || len(rows) > 36 {
		fail(w, 503, "traffic rule statistics unavailable")
		return
	}
	out := []TrafficRuleCount{}
	seen := map[string]bool{}
	for _, row := range rows {
		d := accesslog.TrafficDecision{RuleID: row["traffic_rule"], Decision: row["traffic_decision"]}
		status := 200
		if d.Decision == "block" {
			status = 403
		}
		n, valid := readNumber(row, "requests", 1<<53-1)
		key := d.RuleID + ":" + d.Decision
		if !valid || n < 1 || n != float64(uint64(n)) || !validTrafficEvidence(spec, d, status) || seen[key] {
			fail(w, 503, "invalid traffic rule statistics")
			return
		}
		seen[key] = true
		out = append(out, TrafficRuleCount{RuleID: d.RuleID, Decision: d.Decision, Requests: uint64(n)})
	}
	pipeline, err := s.logs.Query(ctx, fmt.Sprintf(`organization_id:=%q cluster_id:=%q kind:=pipeline | sort by (_time desc) partition by (node_id) limit 1`, org, r.PathValue("cluster")), time.Now().Add(-90*time.Second).UnixMilli(), time.Now().Add(time.Second).UnixMilli(), 12)
	if err != nil {
		fail(w, 503, "traffic collection status unavailable")
		return
	}
	required, reporting := 0, 0
	complete := true
	for _, n := range nodes {
		if n.RevokedAt != nil || f.node != "" && n.ID != f.node {
			continue
		}
		required++
		fresh := false
		for _, row := range pipeline {
			if row["node_id"] == n.ID {
				p, valid := decodePipeline(row, org, r.PathValue("cluster"))
				if !valid {
					fail(w, 503, "invalid traffic collection status")
					return
				}
				fresh = p.Listening && time.Now().UnixMilli()-p.AtMS <= 15000 && p.AtMS <= time.Now().Add(time.Second).UnixMilli()
				complete = complete && p.Dropped == 0 && p.Unconfirmed == 0 && p.Rejected == 0
			}
		}
		if fresh {
			reporting++
		} else {
			complete = false
		}
	}
	if !s.canReadOrganization(r, org) {
		fail(w, 401, "valid organization session required")
		return
	}
	writeJSON(w, 200, map[string]any{"organization_id": org, "cluster_id": r.PathValue("cluster"), "application_id": r.PathValue("application"), "spec_digest": f.digest, "start_ms": f.start, "end_ms": f.end, "matches": out, "collection_complete": complete && required > 0, "reporting_nodes": reporting, "expected_nodes": required})
}
