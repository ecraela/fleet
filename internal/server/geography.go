package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
)

type CountryTraffic struct {
	Country string `json:"country"`
	RequestStats
}
type ASNTraffic struct {
	ASN uint32 `json:"asn"`
	RequestStats
}
type GeographyTraffic struct {
	OrganizationID      string              `json:"organization_id"`
	ClusterID           string              `json:"cluster_id"`
	ApplicationID       string              `json:"application_id"`
	Range               string              `json:"range"`
	StartMS             int64               `json:"start_ms"`
	EndMS               int64               `json:"end_ms"`
	Summary             RequestStats        `json:"summary"`
	CountryKnown        uint64              `json:"country_known"`
	ASNKnown            uint64              `json:"asn_known"`
	Unresolved          uint64              `json:"unresolved"`
	DatabaseUnavailable uint64              `json:"database_unavailable"`
	Countries           []CountryTraffic    `json:"countries"`
	ASNs                []ASNTraffic        `json:"asns"`
	MoreCountries       bool                `json:"more_countries"`
	MoreASNs            bool                `json:"more_asns"`
	Collection          []RequestCollection `json:"collection"`
	ClientSourceFilter  bool                `json:"client_source_filter"`
}

func (s *Server) applicationGeography(w http.ResponseWriter, r *http.Request, org string) {
	_, nodes, f, ok := s.requestResource(w, r, org)
	if !ok {
		return
	}
	if f.before != nil {
		fail(w, 400, "pagination is only supported for request search")
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
	type query struct {
		text  string
		limit int
		rows  []map[string]string
		err   error
	}
	queries := []query{
		{text: base + ` | stats ` + requestStats + `, count() if (country:~"^[A-Z]{2}$") as country_known, count() if (asn:>0) as asn_known, count() if (geo_status:=unresolved) as unresolved, count() if (geo_status:=database_unavailable) as database_unavailable`, limit: 1},
		{text: base + ` | format if (country:="") "unknown" as country | stats by (country) ` + requestStats + ` | sort by (requests desc, country) limit 11`, limit: 11},
		{text: base + ` | format if (asn:="") "0" as asn | stats by (asn) ` + requestStats + ` | sort by (requests desc, asn) limit 11`, limit: 11},
	}
	var wg sync.WaitGroup
	for i := range queries {
		wg.Go(func() { q := &queries[i]; q.rows, q.err = s.logs.Query(ctx, q.text, f.start, f.end, q.limit) })
	}
	wg.Wait()
	for _, q := range queries {
		if q.err != nil {
			fail(w, 503, "geography query unavailable")
			return
		}
	}
	out := GeographyTraffic{OrganizationID: org, ClusterID: r.PathValue("cluster"), ApplicationID: r.PathValue("application"), Range: f.rangeName, StartMS: f.start, EndMS: f.end, Countries: []CountryTraffic{}, ASNs: []ASNTraffic{}, Collection: []RequestCollection{}, Summary: RequestStats{Classes: map[string]uint64{}}}
	out.ClientSourceFilter = true
	if len(queries[0].rows) > 0 {
		row := queries[0].rows[0]
		out.Summary, ok = decodeRequestStats(row)
		if !ok {
			fail(w, 503, "invalid geography summary")
			return
		}
		for _, item := range []struct {
			key string
			to  *uint64
		}{{"country_known", &out.CountryKnown}, {"asn_known", &out.ASNKnown}, {"unresolved", &out.Unresolved}, {"database_unavailable", &out.DatabaseUnavailable}} {
			n, valid := readNumber(row, item.key, float64(out.Summary.Requests))
			if !valid || n != float64(uint64(n)) {
				fail(w, 503, "invalid geography coverage")
				return
			}
			*item.to = uint64(n)
		}
	}
	out.MoreCountries, out.MoreASNs = len(queries[1].rows) > 10, len(queries[2].rows) > 10
	seen := map[string]bool{}
	for _, row := range queries[1].rows[:min(10, len(queries[1].rows))] {
		code := row["country"]
		if code == "unknown" {
			code = ""
		}
		stats, valid := decodeRequestStats(row)
		if !valid || stats.Requests == 0 || code != "" && !accesslog.ValidCountry(code) || seen[code] || !requestCountryMatches(f.country, code) {
			fail(w, 503, "invalid country statistics")
			return
		}
		seen[code] = true
		out.Countries = append(out.Countries, CountryTraffic{Country: code, RequestStats: stats})
	}
	seen = map[string]bool{}
	for _, row := range queries[2].rows[:min(10, len(queries[2].rows))] {
		number, err := strconv.ParseUint(row["asn"], 10, 32)
		stats, valid := decodeRequestStats(row)
		if err != nil || strconv.FormatUint(number, 10) != row["asn"] || !valid || stats.Requests == 0 || seen[row["asn"]] || f.asn != "" && f.asn != row["asn"] {
			fail(w, 503, "invalid network statistics")
			return
		}
		seen[row["asn"]] = true
		out.ASNs = append(out.ASNs, ASNTraffic{ASN: uint32(number), RequestStats: stats})
	}
	// Keep collection status outside the three parallel aggregate queries so a
	// watching console stays inside the private log store's concurrency budget.
	pipeline, err := s.logs.Query(ctx, fmt.Sprintf(`organization_id:=%q cluster_id:=%q kind:=pipeline | sort by (_time desc) partition by (node_id) limit 1`, org, r.PathValue("cluster")), time.Now().Add(-90*time.Second).UnixMilli(), time.Now().Add(time.Second).UnixMilli(), 12)
	if err != nil {
		fail(w, 503, "geography collection status unavailable")
		return
	}
	for _, n := range nodes {
		if n.RevokedAt != nil || f.node != "" && n.ID != f.node {
			continue
		}
		c := RequestCollection{NodeID: n.ID, Name: n.Name}
		for _, row := range pipeline {
			if row["node_id"] != n.ID {
				continue
			}
			p, valid := decodePipeline(row, org, r.PathValue("cluster"))
			if !valid {
				fail(w, 503, "invalid geography collection status")
				return
			}
			c.Pipeline = &p
			c.Fresh = p.Listening && time.Now().UnixMilli()-p.AtMS <= 15000 && p.AtMS <= time.Now().Add(time.Second).UnixMilli()
		}
		out.Collection = append(out.Collection, c)
	}
	slices.SortFunc(out.Collection, func(a, b RequestCollection) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	if !s.canReadOrganization(r, org) {
		fail(w, 401, "valid organization session required")
		return
	}
	writeJSON(w, 200, out)
}
