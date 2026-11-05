package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func TestIncidentDebounceRecoveryReplayAndRestart(t *testing.T) {
	now := time.Now().UTC()
	org, cluster := domain.NewID(), domain.NewID()
	o := domain.IncidentObservation{Rule: "application_health", ResourceID: domain.NewID(), ResourceName: "Store", Scope: "node", Scheme: "http", State: "failing", Code: "request_failed", Severity: "critical", OpenAfter: 10 * time.Second}
	o.Key = o.GroupKey()
	var r incidentRecord
	var events []domain.IncidentEvent
	step := func(seconds int, state string) {
		o.State = state
		o.ObservedAtMS = now.Add(time.Duration(seconds) * time.Second).UnixMilli()
		var next []domain.IncidentEvent
		r, next = advanceIncident(r, o, org, cluster, now.Add(time.Duration(seconds)*time.Second))
		events = append(events, next...)
	}
	step(0, "failing")
	step(5, "failing")
	if r.Incident.Status != "pending" || len(events) != 0 {
		t.Fatal("transient failure opened")
	}
	step(10, "failing")
	id := r.Incident.ID
	if r.Incident.Status != "active" || len(events) != 1 {
		t.Fatal(r, events)
	}
	step(15, "healthy")
	step(20, "unknown")
	step(25, "healthy")
	step(30, "healthy")
	if r.Incident.Status != "active" {
		t.Fatal("missing evidence recovered")
	}
	// Replayed fresh observations cannot complete recovery.
	o.ObservedAtMS = now.Add(30 * time.Second).UnixMilli()
	r, _ = advanceIncident(r, o, org, cluster, now.Add(35*time.Second))
	if r.Incident.Status != "active" || r.Incident.Evidence != "unknown" {
		t.Fatal("cached observation recovered", r)
	}
	step(40, "healthy")
	step(45, "healthy")
	step(50, "healthy")
	if r.Incident.Status != "resolved" || len(events) != 2 {
		t.Fatal("fresh recovery missing", r, events)
	}
	step(55, "failing")
	step(60, "failing")
	step(65, "failing")
	if r.Incident.ID != id || r.Incident.Occurrences != 2 || events[2].Kind != "reopened" {
		t.Fatal("flap made duplicate", r, events)
	}
	step(70, "healthy")
	// A long evaluation gap must not count time while the manager was down.
	step(100, "healthy")
	if r.Incident.Status != "active" {
		t.Fatal("restart counted downtime as healthy")
	}
	step(105, "healthy")
	step(110, "healthy")
	if r.Incident.Status != "resolved" {
		t.Fatal("recovery after restart", r)
	}
	before := r
	r, _ = advanceIncident(r, o, org, cluster, now.Add(50*time.Second))
	if r.LastEvaluatedMS != before.LastEvaluatedMS {
		t.Fatal("old replica moved state backwards")
	}
}

func TestDelayedIncidentEvaluationsEventuallyOpenWithoutCountingDowntime(t *testing.T) {
	now := time.Now().UTC()
	org, cluster := domain.NewID(), domain.NewID()
	o := domain.IncidentObservation{Rule: "application_health", ResourceID: domain.NewID(), State: "failing", Code: "request_failed", Severity: "critical", OpenAfter: 10 * time.Second}
	o.Key = o.GroupKey()
	var record incidentRecord
	for index, delay := range []time.Duration{0, 30 * time.Second, 60 * time.Second} {
		at := now.Add(delay)
		o.ObservedAtMS = at.UnixMilli()
		var events []domain.IncidentEvent
		record, events = advanceIncident(record, o, org, cluster, at)
		if index < 2 && record.Incident.Status != "pending" {
			t.Fatal("unobserved downtime counted as failure evidence")
		}
		if index == 2 && (record.Incident.Status != "active" || len(events) != 1) {
			t.Fatal("slow scheduling permanently suppressed a sustained fault")
		}
	}
	// Cached application evidence cannot advance even a delayed opening.
	o.ObservedAtMS = now.Add(time.Minute).UnixMilli()
	record, _ = advanceIncident(incidentRecord{}, o, org, cluster, now.Add(time.Minute))
	record, _ = advanceIncident(record, o, org, cluster, now.Add(2*time.Minute))
	if record.Incident.Status != "pending" || record.Incident.Evidence != "unknown" {
		t.Fatal("cached failure opened an incident")
	}
}
func incidentContract(t *testing.T, db Store) {
	ctx := t.Context()
	org := domain.NewID()
	c, err := db.CreateCluster(ctx, org, "Incident lab")
	if err != nil {
		t.Fatal(err)
	}
	is := db.(IncidentStore)
	now := time.Now().UTC()
	// A durable terminal failure is observed even when the UI was closed and
	// compensation ended before this worker started.
	err = db.(rolloutPersistence).rolloutTransaction(ctx, org, c.ID, func(tx *rolloutTransaction) error {
		r := domain.Rollout{ID: domain.NewID(), RequestID: domain.NewID(), OrganizationID: org, ClusterID: c.ID, Status: "rolled_back", CreatedAt: now.Add(-time.Minute), UpdatedAt: now, CompletedAt: &now, Document: domain.ConfigurationDocument{Backends: []domain.ConfigurationBackend{}}}
		return tx.save(r)
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if err := is.ReconcileIncidents(ctx, org, c.ID, domain.IncidentTelemetry{}, now); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	page, err := is.ListIncidents(ctx, org, c.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || len(page.Incidents) != 1 || page.Events[0].Kind != "recovered" || page.Incidents[0].Status != "resolved" {
		t.Fatal("duplicate/missing durable history", page)
	}
	if _, err := is.ListIncidents(ctx, domain.NewID(), c.ID, "", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign incident read", err)
	}
	// Identical polls and the next evaluation do not duplicate terminal history.
	if err := is.ReconcileIncidents(ctx, org, c.ID, domain.IncidentTelemetry{}, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	next, err := is.ListIncidents(ctx, org, c.ID, "", 0)
	if err != nil || len(next.Events) != 1 {
		t.Fatal(next, err)
	}
	if _, err := is.ListIncidents(ctx, org, c.ID, "not-an-id", 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal(err)
	}
	page.Incidents[0].NodeIDs = append(page.Incidents[0].NodeIDs, "modified")
	next, _ = is.ListIncidents(ctx, org, c.ID, "", 0)
	if len(next.Incidents[0].NodeIDs) != 0 {
		t.Fatal("caller modified persisted state")
	}
}
func TestMemoryIncidents(t *testing.T)   { incidentContract(t, NewMemory()) }
func TestPostgresIncidents(t *testing.T) { incidentContract(t, postgresForTest(t)) }

func incidentHistoryContract(t *testing.T, db Store) {
	org := domain.NewID()
	cluster, err := db.CreateCluster(t.Context(), org, "History lab")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	id := domain.NewID()
	for i := range 55 {
		testIncidentEvent(t, db, org, cluster.ID, id, "opened", now.Add(time.Duration(i)*time.Millisecond))
	}
	is := db.(IncidentStore)
	p, err := is.ListIncidents(t.Context(), org, cluster.ID, "", 0)
	if err != nil || len(p.Events) != 50 || p.NextCursor == 0 {
		t.Fatal("first page", p, err)
	}
	next, err := is.ListIncidents(t.Context(), org, cluster.ID, id, p.NextCursor)
	if err != nil || len(next.Events) != 5 || next.NextCursor != 0 {
		t.Fatal("second page", next, err)
	}
	if next.Events[0].Sequence >= p.Events[len(p.Events)-1].Sequence {
		t.Fatal("cursor overlapped pages")
	}
	old := domain.NewID()
	testIncidentEvent(t, db, org, cluster.ID, old, "resolved", now.Add(-31*24*time.Hour))
	active := domain.NewID()
	testIncidentEvent(t, db, org, cluster.ID, active, "opened", now.Add(-31*24*time.Hour))
	if err := is.ReconcileIncidents(t.Context(), org, cluster.ID, domain.IncidentTelemetry{}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	p, err = is.ListIncidents(t.Context(), org, cluster.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range p.Incidents {
		if i.ID == old {
			t.Fatal("closed state past retention retained")
		}
		if i.ID == active {
			found = true
		}
	}
	if !found {
		t.Fatal("retention removed active incident")
	}
}
func TestMemoryIncidentHistory(t *testing.T)   { incidentHistoryContract(t, NewMemory()) }
func TestPostgresIncidentHistory(t *testing.T) { incidentHistoryContract(t, postgresForTest(t)) }
