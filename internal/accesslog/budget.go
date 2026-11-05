package accesslog

import (
	"sync"
	"time"
)

type reservation struct {
	at    time.Time
	count int
}
type Budget struct {
	mu    sync.Mutex
	nodes map[string]reservation
}

func NewBudget() *Budget { return &Budget{nodes: map[string]reservation{}} }
func (b *Budget) Admit(node string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, r := range b.nodes {
		if now.Sub(r.at) >= 10*time.Second {
			delete(b.nodes, id)
		}
	}
	r, ok := b.nodes[node]
	if !ok {
		if len(b.nodes) >= 1024 {
			return false
		}
		r.at = now
	}
	if r.count >= 100 {
		return false
	}
	r.count++
	b.nodes[node] = r
	return true
}
