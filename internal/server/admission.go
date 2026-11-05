package server

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/telemetry"
	"github.com/prometheus/prometheus/prompb"
)

const tenantSeriesLimit = 10_000
const installationSeriesLimit = 100_000

const nodeReportsPerSecond = 8

type nodeReportWindow struct {
	started time.Time
	count   int
}

// Shared by all control streams on this manager, including reconnects.
type nodeReportBudget struct {
	mu      sync.Mutex
	windows map[string]nodeReportWindow
	swept   time.Time
}

func (b *nodeReportBudget) admit(node string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Sub(b.swept) >= time.Minute {
		for id, window := range b.windows {
			if now.Sub(window.started) >= 2*time.Minute {
				delete(b.windows, id)
			}
		}
		b.swept = now
	}
	window, exists := b.windows[node]
	if !exists && len(b.windows) >= 4096 {
		return false
	}
	if !exists || now.Sub(window.started) >= time.Second {
		window = nodeReportWindow{started: now}
	}
	if now.Before(window.started) || window.count >= nodeReportsPerSecond {
		return false
	}
	window.count++
	b.windows[node] = window
	return true
}

type seriesRecord struct {
	node, organization string
	last               time.Time
}

// The rolling budget bounds series churn across batches as well as each batch.
// Expired reservations are reclaimed, including samples rejected by storage.
type seriesBudget struct {
	mu            sync.Mutex
	series        map[[32]byte]seriesRecord
	nodes         map[string]int
	organizations map[string]int
	swept         time.Time
}

func newSeriesBudget() *seriesBudget {
	return &seriesBudget{series: make(map[[32]byte]seriesRecord), nodes: make(map[string]int), organizations: make(map[string]int)}
}

func (b *seriesBudget) admit(node domain.Node, batch *prompb.WriteRequest, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Sub(b.swept) >= time.Minute {
		for key, record := range b.series {
			if now.Sub(record.last) > 5*time.Minute {
				delete(b.series, key)
				b.nodes[record.node]--
				b.organizations[record.organization]--
				if b.nodes[record.node] == 0 {
					delete(b.nodes, record.node)
				}
				if b.organizations[record.organization] == 0 {
					delete(b.organizations, record.organization)
				}
			}
		}
		b.swept = now
	}
	keys := make([][32]byte, len(batch.Timeseries))
	newSeries := 0
	for i, series := range batch.Timeseries {
		hash := sha256.New()
		_, _ = hash.Write([]byte(node.ID))
		for _, label := range series.Labels {
			var length [8]byte
			binary.BigEndian.PutUint64(length[:], uint64(len(label.Name)))
			_, _ = hash.Write(length[:])
			_, _ = hash.Write([]byte(label.Name))
			binary.BigEndian.PutUint64(length[:], uint64(len(label.Value)))
			_, _ = hash.Write(length[:])
			_, _ = hash.Write([]byte(label.Value))
		}
		copy(keys[i][:], hash.Sum(nil))
		if _, exists := b.series[keys[i]]; !exists {
			newSeries++
		}
	}
	if b.nodes[node.ID]+newSeries > telemetry.MaxSeries || b.organizations[node.OrganizationID]+newSeries > tenantSeriesLimit || len(b.series)+newSeries > installationSeriesLimit {
		return false
	}
	for _, key := range keys {
		if _, exists := b.series[key]; !exists {
			b.nodes[node.ID]++
			b.organizations[node.OrganizationID]++
		}
		b.series[key] = seriesRecord{node: node.ID, organization: node.OrganizationID, last: now}
	}
	return true
}
