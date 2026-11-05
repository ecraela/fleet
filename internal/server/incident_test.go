package server

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
)

type slowIncidentStore struct {
	*store.Memory
	slow    string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *slowIncidentStore) ReconcileIncidents(ctx context.Context, org, cluster string, telemetry domain.IncidentTelemetry, now time.Time) error {
	if cluster == s.slow {
		s.once.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Memory.ReconcileIncidents(ctx, org, cluster, telemetry, now)
}

func TestSlowIncidentClusterDoesNotBlockHealthyPeers(t *testing.T) {
	f := newFixture(t, "manager", "http://metrics.invalid")
	for range 6 {
		if _, err := f.store.CreateCluster(t.Context(), domain.NewID(), "Synthetic peer"); err != nil {
			t.Fatal(err)
		}
	}
	clusters, err := f.store.IncidentClusters(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	blocked := &slowIncidentStore{Memory: f.store, slow: clusters[0].ID, started: make(chan struct{}), release: make(chan struct{})}
	f.server.config.Store = blocked
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); f.server.RunIncidents(ctx) }()
	defer func() { cancel(); close(blocked.release); <-done }()
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("slow fixture was not reached")
	}
	target := clusters[len(clusters)-1]
	deadline := time.Now().Add(time.Second)
	for {
		page, err := f.store.ListIncidents(ctx, target.OrganizationID, target.ID, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if page.EvaluatedAtMS > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("one blocked cluster starved a healthy peer")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIncidentSweepDoesNotPauseBetweenInventoryPages(t *testing.T) {
	for _, count := range []int{50, 100, 150, 151, 251} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := newFixture(t, "manager", "http://metrics.invalid")
			clusters := []domain.Cluster{f.cluster}
			for i := 1; i < count; i++ {
				org := f.org
				if i%2 == 0 {
					org = domain.NewID()
				}
				cluster, err := f.store.CreateCluster(t.Context(), org, "Synthetic inventory")
				if err != nil {
					t.Fatal(err)
				}
				clusters = append(clusters, cluster)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); f.server.RunIncidents(ctx) }()
			defer func() { cancel(); <-done }()
			deadline := time.Now().Add(2 * time.Second)
			for {
				complete := true
				for _, cluster := range clusters {
					page, err := f.store.ListIncidents(ctx, cluster.OrganizationID, cluster.ID, "", 0)
					if err != nil {
						t.Fatal(err)
					}
					complete = complete && page.EvaluatedAtMS > 0
				}
				if complete {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("one tenant's inventory stalled the incident sweep")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestIncidentEndpointsRequireTenantReadAndDoNotExposeControlActions(t *testing.T) {
	f := browserFixture(t)
	foreign, err := f.store.CreateCluster(t.Context(), domain.NewID(), "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{f.cluster.ID, foreign.ID} {
		path := "/v1/clusters/" + c + "/incidents"
		if got := f.request("GET", path, nil, false); got.Code != 401 {
			t.Fatal(got.Code)
		}
		want := 200
		if c == foreign.ID {
			want = 404
		}
		if got := f.request("GET", path, nil, true); got.Code != want {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	for _, query := range []string{"?before=-1", "?before=bad", "?before=1&before=2", "?incident=bad", "?url=https://foreign.invalid", "?organization_id=" + domain.NewID()} {
		if got := f.request("GET", "/v1/clusters/"+f.cluster.ID+"/incidents"+query, nil, true); got.Code != 400 {
			t.Fatal(query, got.Code, got.Body.String())
		}
	}
	pending, nonce := challenge(t, f.server)
	created := exchange(t, f, pending, validClaims(f, nonce))
	got := browserRequest(t, f.server, "GET", "/v1/clusters/"+f.cluster.ID+"/incidents", "", sessionCookie(t, created))
	if got.Code != 200 {
		t.Fatal("viewer incidents", got.Code)
	}
}
