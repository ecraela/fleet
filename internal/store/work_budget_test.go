package store

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

// Seed an already admitted checkpoint without executing a whole drain for each
// topology. The ordinary lifecycle contracts exercise those drain transitions.
func retainedMaintenance(t *testing.T, db Store, org, status string) *maintenanceRig {
	t.Helper()
	r := newMaintenanceRigForOrganization(t, db, org)
	r.start()
	r.job.Status = status
	r.job.UpdatedAt = time.Now().Add(-time.Hour)
	if err := r.p.maintenanceTransaction(r.ctx, org, r.nodes[0].ID, func(tx *maintenanceTransaction) error { return tx.save(r.job) }); err != nil {
		t.Fatal(err)
	}
	return r
}

func workBudgetContract(t *testing.T, factory func(*testing.T) Store) {
	t.Run("held inventories cannot delay runnable health windows", func(t *testing.T) {
		db := factory(t)
		for range 4 {
			org := domain.NewID()
			for i := range 16 {
				status := "attention"
				if i%2 == 0 {
					status = "maintenance"
				}
				retainedMaintenance(t, db, org, status)
			}
		}
		r := newMaintenanceRig(t, db)
		r.start()
		r.finish(r.next("maintenance"), true)
		r.finish(r.next("configuration"), true)
		// Select the same batches as the real worker, with deterministic one-second
		// observations so the test does not need a wall-clock drain timeout.
		for range 20 {
			r.now = r.now.Add(time.Second)
			r.refresh()
			page, err := r.p.activeMaintenance(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range page {
				if err := advanceMaintenance(t.Context(), r.p, m.OrganizationID, m.NodeID, m.ID, r.now); err != nil {
					t.Fatal(err)
				}
			}
			view, err := db.(MaintenanceStore).GetMaintenance(t.Context(), r.org, r.nodes[0].ID)
			if err != nil || view.Current == nil {
				t.Fatal(err)
			}
			if view.Current.Status == "maintenance" {
				return
			}
		}
		t.Fatal("held inventories prevented an otherwise healthy node from finishing its drain")
	})
	t.Run("idle fences isolate tenants and rotate worker pages", func(t *testing.T) {
		db := factory(t)
		org := domain.NewID()
		var first *maintenanceRig
		for i := range 16 {
			status := "attention"
			if i%2 == 0 {
				status = "maintenance"
			}
			r := retainedMaintenance(t, db, org, status)
			if first == nil {
				first = r
			}
		}
		victim := newMaintenanceRig(t, db)
		victim.start()
		rollout := newRolloutRig(t, db)
		rollout.start(rollout.input())
		if rollout.rollout.ID == "" || victim.job.ID == "" {
			t.Fatal("idle foreign fences denied new work")
		}
		if err := db.(TopologyStore).SetTopology(t.Context(), org, first.cluster, first.job.Topology); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("idle fence stopped protecting its topology", err)
		}
		if err := db.(MaintenanceStore).AdvanceMaintenance(t.Context()); err != nil {
			t.Fatal(err)
		}
		page, err := first.p.activeMaintenance(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range page {
			found = found || m.ID == victim.job.ID
		}
		if !found {
			t.Fatal("idle/attention rows permanently occupied the worker page")
		}
		view, err := db.(MaintenanceStore).GetMaintenance(t.Context(), org, first.nodes[0].ID)
		if err != nil || view.Current == nil || view.Current.ID != first.job.ID || view.Current.Terminal() {
			t.Fatal("scheduler released a durable safety fence", err)
		}
		extra := newMaintenanceRigForOrganization(t, db, org)
		if _, err := db.(MaintenanceStore).ChangeMaintenance(t.Context(), org, extra.nodes[0].ID, extra.input("withdraw")); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("tenant retained-reservation bound was lost", err)
		}
	})
	t.Run("runnable quotas include retries and mixed operation types", func(t *testing.T) {
		db := factory(t)
		org := domain.NewID()
		held := retainedMaintenance(t, db, org, "maintenance")
		for i := range 8 {
			if i%2 == 0 {
				r := newRolloutRigForOrganization(t, db, org)
				r.start(r.input())
			} else {
				r := newMaintenanceRigForOrganization(t, db, org)
				r.start()
			}
		}
		held.now = time.Now().UTC()
		held.refresh()
		for _, action := range []string{"withdraw", "rejoin"} {
			input := held.input(action)
			input.MaintenanceID = held.job.ID
			if _, err := db.(MaintenanceStore).ChangeMaintenance(t.Context(), org, held.nodes[0].ID, input); !errors.Is(err, domain.ErrConflict) {
				t.Fatal("held work bypassed tenant runnable admission", action, err)
			}
		}
		fresh := newRolloutRigForOrganization(t, db, org)
		if _, err := db.(RolloutStore).CreateRollout(t.Context(), org, fresh.cluster, fresh.input()); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("rollout bypassed mixed runnable quota", err)
		}
		other := newMaintenanceRig(t, db)
		other.start()
	})
	t.Run("concurrent tenant admission reserves space for peers", func(t *testing.T) {
		db := factory(t)
		org := domain.NewID()
		rigs := make([]*maintenanceRig, 12)
		inputs := make([]domain.MaintenanceInput, len(rigs))
		for i := range rigs {
			rigs[i] = newMaintenanceRigForOrganization(t, db, org)
		}
		for i, r := range rigs {
			r.now = time.Now().UTC()
			r.refresh()
			inputs[i] = r.input("withdraw")
		}
		var accepted atomic.Int32
		var wg sync.WaitGroup
		for i, r := range rigs {
			wg.Go(func() {
				_, err := db.(MaintenanceStore).ChangeMaintenance(t.Context(), org, r.nodes[0].ID, inputs[i])
				if err == nil {
					accepted.Add(1)
				} else if !errors.Is(err, domain.ErrConflict) {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if accepted.Load() != 8 {
			t.Fatal("concurrent admission exceeded or lost tenant capacity", accepted.Load())
		}
		for range 8 {
			r := newMaintenanceRigForOrganization(t, db, domain.NewID())
			r.start()
		}
		extra := newMaintenanceRig(t, db)
		if _, err := db.(MaintenanceStore).ChangeMaintenance(t.Context(), extra.org, extra.nodes[0].ID, extra.input("withdraw")); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("installation runnable bound was lost", err)
		}
	})
}

func TestMemoryWorkBudget(t *testing.T) {
	workBudgetContract(t, func(*testing.T) Store { return NewMemory() })
}

func TestPostgresWorkBudget(t *testing.T) {
	workBudgetContract(t, func(t *testing.T) Store { return postgresForTest(t) })
}
