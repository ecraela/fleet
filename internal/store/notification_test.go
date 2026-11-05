package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func testIncidentEvent(t *testing.T, db Store, org, cluster, id, kind string, at time.Time) {
	t.Helper()
	err := db.(rolloutPersistence).rolloutTransaction(t.Context(), org, cluster, func(tx *rolloutTransaction) error {
		status := "active"
		if kind == "resolved" {
			status = "resolved"
		}
		i := domain.Incident{ID: id, OrganizationID: org, ClusterID: cluster, Rule: "agent_offline", ResourceID: domain.NewID(), ResourceName: "haproxy-a", Status: status, Severity: "warning", Evidence: "failing", Code: "agent_offline", OpenedAtMS: at.UnixMilli(), UpdatedAtMS: at.UnixMilli(), Occurrences: 1, NodeIDs: []string{}}
		if status == "resolved" {
			i.ResolvedAtMS = at.UnixMilli()
			i.Evidence = "healthy"
		}
		if err := tx.incidents.save(incidentRecord{Key: "agent/" + id, Incident: i}); err != nil {
			return err
		}
		return tx.incidents.event(domain.IncidentEvent{Kind: kind, AtMS: at.UnixMilli(), Incident: i})
	})
	if err != nil {
		t.Fatal(err)
	}
}
func notificationContract(t *testing.T, db Store) {
	ctx := t.Context()
	org, id, digest := domain.NewID(), domain.NewID(), strings.Repeat("a", 64)
	cluster, err := db.CreateCluster(ctx, org, "Notification lab")
	if err != nil {
		t.Fatal(err)
	}
	store := db.(NotificationStore)
	now := time.Now().UTC()
	claim := func(at time.Time) *domain.IncidentDelivery {
		d, err := store.ClaimIncidentNotification(ctx, org, id, digest, at)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	incident := domain.NewID()
	testIncidentEvent(t, db, org, cluster.ID, incident, "opened", now.Add(-time.Hour))
	if d := claim(now); d != nil {
		t.Fatal("new destination replayed historical notifications")
	}
	if _, err := store.ClaimIncidentNotification(ctx, domain.NewID(), id, digest, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("destination changed tenants", err)
	}
	testIncidentEvent(t, db, org, cluster.ID, incident, "reopened", now)
	// Replica leases prevent concurrent sends; a crash yields the same stable key.
	var deliveries []*domain.IncidentDelivery
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			d, e := store.ClaimIncidentNotification(ctx, org, id, digest, now)
			if e != nil {
				t.Error(e)
			}
			if d != nil {
				mu.Lock()
				deliveries = append(deliveries, d)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(deliveries) != 1 {
		t.Fatal("duplicate delivery lease", len(deliveries))
	}
	first := deliveries[0]
	if d := claim(now.Add(20 * time.Second)); d != nil {
		t.Fatal("unexpired lease reclaimed")
	}
	second := claim(now.Add(31 * time.Second))
	if second == nil || second.Event.Sequence != first.Event.Sequence || second.LeaseID == first.LeaseID {
		t.Fatal("crash did not reclaim stable event", second)
	}
	if err := store.FinishIncidentNotification(ctx, org, *first, true, now.Add(32*time.Second), 0); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old lease checkpoint accepted", err)
	}
	if err := store.FinishIncidentNotification(ctx, org, *second, true, now.Add(32*time.Second), 0); err != nil {
		t.Fatal(err)
	}
	testIncidentEvent(t, db, org, cluster.ID, incident, "resolved", now.Add(40*time.Second))
	if d := claim(now.Add(40 * time.Second)); d != nil {
		t.Fatal("cooldown ignored")
	}
	testIncidentEvent(t, db, org, cluster.ID, incident, "reopened", now.Add(50*time.Second))
	d := claim(now.Add(6 * time.Minute))
	if d == nil || d.Event.Kind != "reopened" {
		t.Fatal("latest incident state not coalesced", d)
	}
	if err := store.FinishIncidentNotification(ctx, org, *d, false, now.Add(6*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if d := claim(now.Add(6*time.Minute + 30*time.Second)); d != nil {
		t.Fatal("retry backoff ignored")
	}
	d = claim(now.Add(7 * time.Minute))
	if d == nil || d.Attempts < 2 {
		t.Fatal("retry state lost", d)
	}
	for attempt := d.Attempts; attempt <= 8; attempt++ {
		at := now.Add(time.Duration(7+attempt) * time.Minute)
		if err := store.FinishIncidentNotification(ctx, org, *d, false, at, time.Second); err != nil {
			t.Fatal(err)
		}
		next := claim(at.Add(2 * time.Second))
		if attempt < 8 {
			if next == nil {
				t.Fatal("early retry stop")
			}
			d = next
		} else if next != nil {
			t.Fatal("unbounded retries")
		}
	}
	status, err := store.IncidentNotificationStatus(ctx, org, cluster.ID)
	if err != nil || status.Failed != 1 {
		t.Fatal("failed delivery absent", status, err)
	}
	foreign, err := db.CreateCluster(ctx, domain.NewID(), "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.IncidentNotificationStatus(ctx, org, foreign.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign status", err)
	}
	// State transitions remain durable even when a DB transaction is interrupted.
	err = db.(rolloutPersistence).rolloutTransaction(ctx, org, cluster.ID, func(tx *rolloutTransaction) error {
		if err := tx.incidents.event(domain.IncidentEvent{Incident: domain.Incident{ID: incident, OrganizationID: org, ClusterID: cluster.ID}, Kind: "opened", AtMS: now.UnixMilli()}); err != nil {
			return err
		}
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestMemoryIncidentNotifications(t *testing.T)   { notificationContract(t, NewMemory()) }
func TestPostgresIncidentNotifications(t *testing.T) { notificationContract(t, postgresForTest(t)) }

func notificationBacklogContract(t *testing.T, db Store) {
	ctx := t.Context()
	org, id, digest := domain.NewID(), domain.NewID(), strings.Repeat("b", 64)
	cluster, err := db.CreateCluster(ctx, org, "Backlog lab")
	if err != nil {
		t.Fatal(err)
	}
	store := db.(NotificationStore)
	now := time.Now().UTC()
	if _, err := store.ClaimIncidentNotification(ctx, org, id, digest, now); err != nil {
		t.Fatal(err)
	}
	incident := domain.NewID()
	for range 100 {
		testIncidentEvent(t, db, org, cluster.ID, incident, "opened", now)
	}
	testIncidentEvent(t, db, org, cluster.ID, incident, "resolved", now.Add(time.Second))
	if d, err := store.ClaimIncidentNotification(ctx, org, id, digest, now.Add(2*time.Second)); err != nil || d != nil {
		t.Fatal("sent stale opening before backlog recovery was staged", d, err)
	}
	d, err := store.ClaimIncidentNotification(ctx, org, id, digest, now.Add(7*time.Second))
	if err != nil || d == nil || d.Event.Kind != "resolved" {
		t.Fatal("latest recovery lost", d, err)
	}
}
func TestMemoryIncidentNotificationBacklog(t *testing.T) { notificationBacklogContract(t, NewMemory()) }
func TestPostgresIncidentNotificationBacklog(t *testing.T) {
	notificationBacklogContract(t, postgresForTest(t))
}
