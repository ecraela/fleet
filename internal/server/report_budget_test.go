package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func TestNodeReportBudgetIsSharedAcrossStreamsAndBounded(t *testing.T) {
	budget := &nodeReportBudget{windows: map[string]nodeReportWindow{}}
	now := time.Now()
	node := domain.NewID()
	var admitted atomic.Int32
	var calls sync.WaitGroup
	for range 100 {
		calls.Go(func() {
			if budget.admit(node, now) {
				admitted.Add(1)
			}
		})
	}
	calls.Wait()
	if admitted.Load() != 8 {
		t.Fatalf("concurrent admission %d", admitted.Load())
	}
	// A reconnect uses the same authenticated node key, not a new stream bucket.
	if budget.admit(node, now.Add(time.Millisecond)) {
		t.Fatal("reconnect reset the report budget")
	}
	if !budget.admit(domain.NewID(), now) {
		t.Fatal("one node blocked another node")
	}
	if !budget.admit(node, now.Add(time.Second)) {
		t.Fatal("normal cadence did not resume")
	}
	for i := 0; i < 5000; i++ {
		budget.admit(domain.NewID(), now)
	}
	if len(budget.windows) > 4096 {
		t.Fatal("unbounded report limiter state")
	}
	if !budget.admit(domain.NewID(), now.Add(3*time.Minute)) {
		t.Fatal("expired nodes were not reclaimed")
	}
}
