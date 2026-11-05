package server

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

func (s *Server) latestLive(ctx context.Context, cluster domain.Cluster, nodes []domain.Node) (ClusterLive, bool, error) {
	frame := ClusterLive{ClusterID: cluster.ID, Nodes: make([]LiveNode, len(nodes))}
	persistence, ok := s.config.Store.(store.LiveStore)
	if !ok {
		return frame, false, nil
	}
	ids := make([]string, len(nodes))
	for i, node := range nodes {
		ids[i] = node.ID
	}
	latest, err := persistence.GetLive(ctx, cluster.OrganizationID, cluster.ID, ids)
	if err != nil {
		return frame, false, err
	}
	// Old ingest processes can coexist during an upgrade. Keep the existing
	// history-backed feed until every selected node has a latest-value summary.
	for _, node := range nodes {
		if node.RevokedAt == nil {
			pair, ok := latest[node.ID]
			if !ok || time.Since(time.UnixMilli(pair.Current.AtMS)) > 15*time.Second {
				return frame, false, nil
			}
		}
	}
	for i, node := range nodes {
		live := LiveNode{NodeID: node.ID, Revoked: node.RevokedAt != nil, Series: []LiveSeries{}}
		frame.Nodes[i] = live
		if live.Revoked {
			continue
		}
		pair := latest[node.ID]
		if !pair.Current.Valid() || (pair.Previous != nil && (!pair.Previous.Valid() || pair.Previous.AtMS >= pair.Current.AtMS)) {
			return frame, true, errors.New("invalid latest observation")
		}
		at := time.UnixMilli(pair.Current.AtMS).UTC()
		live.ObservedAt = &at
		live.ScrapeOK = pair.Current.ScrapeOK
		age := time.Since(at)
		live.Fresh = age >= -time.Minute && age <= 15*time.Second
		if live.Fresh && live.ScrapeOK {
			frontend := ""
			if cluster.Topology != nil {
				for _, declared := range cluster.Topology.Nodes {
					if declared.NodeID == node.ID {
						frontend = cluster.Topology.Frontend
						break
					}
				}
			}
			live.Series, err = latestSeries(pair, frontend)
			if err != nil {
				return frame, true, err
			}
		}
		frame.Nodes[i] = live
	}
	return frame, true, nil
}

func latestSeries(pair domain.LivePair, frontend string) ([]LiveSeries, error) {
	values := map[string]float64{}
	add := func(name string, value float64) { values[name] += value }
	current := pair.Current
	previous := pair.Previous
	seconds := 0.0
	if previous != nil && previous.ScrapeOK {
		seconds = float64(current.AtMS-previous.AtMS) / 1000
	}
	if seconds <= 0 || seconds > 15 {
		previous = nil
	}
	rate := func(a, b *float64) (float64, bool) {
		if a == nil || b == nil {
			return 0, false
		}
		delta := *a - *b
		if delta < 0 {
			delta = *a
		}
		return delta / seconds, true
	}
	if frontend == "" && current.Connections != nil {
		add("connections", *current.Connections)
	}
	for name, f := range current.Frontends {
		if frontend != "" && name != frontend {
			continue
		}
		if frontend != "" && f.Connections != nil {
			add("connections", *f.Connections)
		}
		if previous == nil {
			continue
		}
		before, ok := previous.Frontends[name]
		if !ok {
			continue
		}
		if value, ok := rate(f.BytesIn, before.BytesIn); ok {
			add("traffic_in", value*8)
		}
		if value, ok := rate(f.BytesOut, before.BytesOut); ok {
			add("traffic_out", value*8)
		}
		for code, value := range f.Responses {
			if old, ok := before.Responses[code]; ok {
				if v, ok := rate(&value, &old); ok {
					add("http_responses/"+code, v)
				}
			}
		}
	}
	series := make([]LiveSeries, 0, len(values)+len(current.LatencyMS))
	for key, value := range values {
		metric := map[string]string{"series": key}
		if code, ok := strings.CutPrefix(key, "http_responses/"); ok {
			metric = map[string]string{"series": "http_responses", "code": code}
		}
		series = append(series, LiveSeries{Metric: metric, Value: value})
	}
	for proxy, value := range current.LatencyMS {
		series = append(series, LiveSeries{Metric: map[string]string{"series": "latency", "proxy": proxy}, Value: value})
	}
	if len(series) > 62 {
		return nil, errors.New("live series limit exceeded")
	}
	for _, point := range series {
		if math.IsNaN(point.Value) || math.IsInf(point.Value, 0) || point.Value < 0 {
			return nil, errors.New("invalid live value")
		}
	}
	sort.Slice(series, func(i, j int) bool {
		a, b := series[i].Metric, series[j].Metric
		return a["series"]+a["code"]+a["proxy"] < b["series"]+b["code"]+b["proxy"]
	})
	return series, nil
}
