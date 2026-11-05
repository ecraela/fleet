package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/telemetry"
	"github.com/prometheus/prometheus/prompb"
)

func TestSeriesBudgetRejectsChurnAndReclaimsExpiredNodes(t *testing.T) {
	budget := newSeriesBudget()
	node := domain.Node{ID: domain.NewID(), OrganizationID: domain.NewID()}
	now := time.Now()
	batch := &prompb.WriteRequest{}
	for i := 0; i < telemetry.MaxSeries; i++ {
		batch.Timeseries = append(batch.Timeseries, prompb.TimeSeries{Labels: []prompb.Label{{Name: "__name__", Value: "haproxy_backend_status"}, {Name: "proxy", Value: fmt.Sprint(i)}}})
	}
	if !budget.admit(node, batch, now) || !budget.admit(node, batch, now.Add(time.Second)) {
		t.Fatal("existing series rejected")
	}
	newBatch := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{{Labels: []prompb.Label{{Name: "__name__", Value: "haproxy_backend_status"}, {Name: "proxy", Value: "new"}}}}}
	if budget.admit(node, newBatch, now.Add(time.Second)) {
		t.Fatal("series churn exceeded node limit")
	}
	if !budget.admit(node, newBatch, now.Add(7*time.Minute)) {
		t.Fatal("expired series budget not reclaimed")
	}
	if len(budget.series) != 1 || budget.nodes[node.ID] != 1 || budget.organizations[node.OrganizationID] != 1 {
		t.Fatal("incorrect budget reclamation")
	}
}
