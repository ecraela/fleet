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

type managedRolloutRig struct{ *maintenanceRig }

func newManagedRolloutRig(t *testing.T, db Store) *managedRolloutRig {
	r := &managedRolloutRig{newMaintenanceRig(t, db)}
	r.ctx = context.WithValue(r.ctx, controlClockKey{}, func() time.Time { return r.now })
	return r
}
func (r *managedRolloutRig) input() domain.RolloutInput {
	in := r.rolloutRig.input()
	in.Nodes = in.Nodes[:2]
	v, e := r.db.(MaintenanceStore).GetMaintenance(r.ctx, r.org, r.nodes[0].ID)
	if e != nil {
		r.t.Fatal(e)
	}
	d := v.Controller.Discovery
	in.Entrypoint = &domain.RolloutEntrypointTarget{RolloutTarget: domain.RolloutTarget{NodeID: r.nodes[2].ID, ExpectedGeneration: d.Generation, ExpectedArtifactID: d.Configuration.ArtifactID, ExpectedVersion: d.Configuration.Version}, BindingID: v.BindingID}
	return in
}
func (r *managedRolloutRig) start(in domain.RolloutInput) { r.rolloutRig.start(in) }
func (r *managedRolloutRig) step() {
	r.rolloutRig.step()
	if op := r.commands[2]; op != nil {
		delete(r.commands, 2)
		r.maintenanceRig.finish(op, true)
	}
	disabled := 0
	for _, s := range r.discoveries[2].Configuration.Document.Backends[0].Servers {
		if s.Maintenance {
			disabled++
		}
	}
	if disabled > 1 {
		r.t.Fatal("both entrypoint upstreams were withdrawn")
	}
	if r.rollout.Nodes[1].ApplyOperationID != "" && r.rollout.Status == "running" && (r.rollout.Nodes[0].Maintenance == nil || r.rollout.Nodes[0].Maintenance.Status != "completed") {
		r.t.Fatal("second node started before first rejoined")
	}
}
func (r *managedRolloutRig) next(i int, rollback bool) *domain.Operation {
	for range 100 {
		r.step()
		if op := r.commands[i]; op != nil && (op.RollbackArtifactID != "") == rollback {
			delete(r.commands, i)
			m := rolloutMaintenance(&r.rollout.Nodes[i], rollback)
			if m == nil || m.Status != "maintenance" || m.Connections == nil || *m.Connections != 0 {
				r.t.Fatal("configuration dispatched before verified drain", m)
			}
			if op.RolloutID != r.rollout.ID {
				r.t.Fatal("operation lost rollout attribution")
			}
			return op
		}
		if r.rollout.Terminal() {
			break
		}
	}
	r.t.Fatal("operation not scheduled", i, rollback, r.rollout.Status, r.rollout.ErrorCode, r.rollout.Nodes)
	return nil
}
func (r *managedRolloutRig) complete(status string) {
	for range 100 {
		r.step()
		if r.rollout.Terminal() {
			break
		}
	}
	if r.rollout.Status != status {
		r.t.Fatal("unexpected outcome", r.rollout.Status, r.rollout.ErrorCode, r.rollout.Nodes)
	}
}
func managedRolloutContract(t *testing.T, db Store) {
	r := newManagedRolloutRig(t, db)
	in := r.input()
	if _, e := db.(RolloutStore).CreateRollout(r.ctx, domain.NewID(), r.cluster, in); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign write accepted", e)
	}
	bad := in
	bad.Entrypoint = nil
	if _, e := db.(RolloutStore).CreateRollout(r.ctx, r.org, r.cluster, bad); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("managed entrypoint bypassed", e)
	}
	r.connections(1)
	r.start(in)
	if repeat, e := db.(RolloutStore).CreateRollout(r.ctx, r.org, r.cluster, in); e != nil || repeat.ID != r.rollout.ID {
		t.Fatal("idempotency lost", e)
	}
	bad = in
	epoint := *in.Entrypoint
	epoint.ExpectedVersion++
	bad.Entrypoint = &epoint
	if _, e := db.(RolloutStore).CreateRollout(r.ctx, r.org, r.cluster, bad); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("controller precondition changed on retry", e)
	}
	for range 16 {
		r.step()
		if r.rollout.Nodes[0].Maintenance != nil && r.rollout.Nodes[0].Maintenance.Status == "draining" {
			break
		}
	}
	for range 4 {
		r.step()
	}
	if r.rollout.Nodes[0].ApplyOperationID != "" || r.rollout.Nodes[1].Maintenance != nil {
		t.Fatal("active request did not block reload")
	}
	for _, i := range []int{0, 1, 2} {
		d := r.discoveries[i]
		doc := d.Configuration.Document.Canonical()
		doc.Backends[0].Servers[0].Weight = 180
		if _, e := db.(ConfigurationStore).CreateConfiguration(r.ctx, r.org, domain.Operation{RequestID: domain.NewID(), NodeID: r.nodes[i].ID, ExpectedGeneration: d.Generation, BaseArtifactID: d.Configuration.ArtifactID, BaseVersion: d.Configuration.Version, Configuration: &doc}, ""); !errors.Is(e, domain.ErrConflict) {
			t.Fatal("rollout fence bypassed", i, e)
		}
	}
	view, e := db.(MaintenanceStore).GetMaintenance(r.ctx, r.org, r.nodes[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.(TopologyStore).SetTopology(r.ctx, r.org, r.cluster, r.rollout.Entrypoint.Topology); e == nil {
		t.Fatal("topology changed during rollout")
	}
	if _, e = db.(MaintenanceStore).ChangeMaintenance(r.ctx, r.org, r.nodes[0].ID, domain.MaintenanceInput{RequestID: domain.NewID(), Action: "withdraw", BindingID: view.BindingID, ExpectedGeneration: view.Target.Discovery.Generation, ExpectedEntrypointGeneration: view.Controller.Discovery.Generation, ExpectedArtifactID: view.Controller.Discovery.Configuration.ArtifactID, ExpectedVersion: view.Controller.Discovery.Configuration.Version, DrainTimeoutSeconds: 30}); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("maintenance bypassed rollout", e)
	}
	r.connections(0)
	a := r.next(0, false)
	r.rolloutRig.finish(0, a, true)
	b := r.next(1, false)
	r.rolloutRig.finish(1, b, true)
	r.complete("succeeded")
	for _, n := range r.rollout.Nodes {
		if n.Maintenance == nil || n.Maintenance.Status != "completed" || n.Status != "applied" {
			t.Fatal("node was not rejoined", n)
		}
	}
	if !r.discoveries[2].Configuration.Document.Equal(r.rollout.Entrypoint.Baseline) {
		t.Fatal("entrypoint was not restored")
	}
	state, _, e := db.(ControlStore).GetControl(r.ctx, r.org, r.nodes[2].ID)
	if e != nil || state.ActiveRolloutID != "" {
		t.Fatal("controller fence retained", e)
	}
}
func TestMemoryMaintenanceRollingConfiguration(t *testing.T) { managedRolloutContract(t, NewMemory()) }
func TestPostgresMaintenanceRollingConfiguration(t *testing.T) {
	managedRolloutContract(t, postgresForTest(t))
}

func managedRollbackContract(t *testing.T, db Store) {
	r := newManagedRolloutRig(t, db)
	r.start(r.input())
	r.rolloutRig.finish(0, r.next(0, false), true)
	r.rolloutRig.finish(1, r.next(1, false), true)
	r.discoveries[1].Servers[1].Health = "DOWN"
	for range 30 {
		r.step()
		if r.rollout.Status == "rolling_back" {
			break
		}
	}
	if r.rollout.Status != "rolling_back" {
		t.Fatal("failed health did not start recovery", r.rollout.Status)
	}
	r.rolloutRig.finish(1, r.next(1, true), true)
	r.connections(1)
	for range 35 {
		r.step()
		if m := r.rollout.Nodes[0].RollbackMaintenance; m != nil && m.Status == "draining" {
			break
		}
	}
	if r.rollout.Nodes[0].RollbackMaintenance == nil || r.rollout.Nodes[0].RollbackMaintenance.Status != "draining" || r.rollout.Nodes[0].RollbackOperationID != "" {
		t.Fatal("rollback did not wait for an existing connection")
	}
	r.connections(0)
	restoreA := r.next(0, true)
	if r.rollout.Nodes[1].Maintenance.Status != "completed" {
		t.Fatal("earlier node withdrawn before failed node rejoined")
	}
	if r.rollout.Nodes[0].RollbackMaintenance == nil {
		t.Fatal("earlier rejoined node was not drained again")
	}
	r.rolloutRig.finish(0, restoreA, true)
	r.complete("rolled_back")
	for i := range 2 {
		if !r.discoveries[i].Configuration.Document.Equal(r.baseline[i].Document) {
			t.Fatal("baseline not restored")
		}
	}
}
func TestMemoryMaintenanceRollbackDrainsPreviouslyRejoinedNodes(t *testing.T) {
	managedRollbackContract(t, NewMemory())
}
func TestPostgresMaintenanceRollbackDrainsPreviouslyRejoinedNodes(t *testing.T) {
	managedRollbackContract(t, postgresForTest(t))
}

func rolloutDrainTimeoutContract(t *testing.T, db Store) {
	r := newManagedRolloutRig(t, db)
	in := r.input()
	in.DrainTimeoutSeconds = 5
	r.connections(1)
	r.start(in)
	r.complete("rollback_failed")
	n := r.rollout.Nodes[0]
	if n.ApplyOperationID != "" || n.RecoveryMaintenanceID == "" || r.rollout.Nodes[1].Maintenance != nil {
		t.Fatal("deadline changed a node or lost maintenance", n)
	}
	v, e := r.db.(MaintenanceStore).GetMaintenance(r.ctx, r.org, n.NodeID)
	if e != nil || v.Current == nil || v.Current.Status != "attention" || v.Current.ID != n.RecoveryMaintenanceID || !r.discoveries[2].Configuration.Document.Backends[0].Servers[0].Maintenance {
		t.Fatal("isolation did not survive rollout completion", e)
	}
	// A subsequent operator action uses fresh permissions and preconditions.
	r.connections(0)
	r.refresh()
	r.job = *v.Current
	if _, e = r.db.(MaintenanceStore).ChangeMaintenance(r.ctx, r.org, n.NodeID, r.maintenanceRig.input("rejoin")); e != nil {
		t.Fatal("maintenance handoff was not recoverable", e)
	}
	for range 40 {
		r.maintenanceRig.step()
		if op := r.commands[2]; op != nil {
			delete(r.commands, 2)
			r.maintenanceRig.finish(op, true)
		}
		if r.job.Status == "completed" {
			break
		}
	}
	if r.job.Status != "completed" {
		t.Fatal("handoff recovery did not finish", r.job.Status)
	}
	rows, e := r.db.(RolloutStore).ListRollouts(r.ctx, r.org, r.cluster)
	if e != nil || rows[0].Status != "rollback_failed" || rows[0].Nodes[0].Maintenance.Status != "completed" {
		t.Fatal("rollout history hid recovery progress", e)
	}
	// Recovery is a live referenced record; the failed rollout remains immutable.
	if p, ok := db.(*Postgres); ok {
		var original string
		if e := p.pool.QueryRow(r.ctx, `SELECT payload->'nodes'->0->'maintenance'->>'status' FROM hm_rollouts WHERE id=$1`, r.rollout.ID).Scan(&original); e != nil || original != "attention" {
			t.Fatal("completed history was rewritten", e, original)
		}
	} else if db.(*Memory).rollouts[r.rollout.ID].Nodes[0].Maintenance.Status != "attention" {
		t.Fatal("completed history was rewritten")
	}
}
func TestRolloutDrainTimeoutHandsOffWithoutReloadingOrRejoining(t *testing.T) {
	rolloutDrainTimeoutContract(t, NewMemory())
}
func TestPostgresRolloutDrainTimeoutHandoff(t *testing.T) {
	rolloutDrainTimeoutContract(t, postgresForTest(t))
}

func TestRejoinHealthFailureIsIsolatedThenRolledBack(t *testing.T) {
	r := newManagedRolloutRig(t, NewMemory())
	r.start(r.input())
	r.rolloutRig.finish(0, r.next(0, false), true)
	for range 20 {
		r.step()
		m := r.rollout.Nodes[0].Maintenance
		if m != nil && m.Status == "rejoining" && !r.discoveries[2].Configuration.Document.Backends[0].Servers[0].Maintenance {
			break
		}
	}
	r.discoveries[0].Servers[0].Health = "DOWN"
	r.step()
	if r.rollout.Status != "rolling_back" {
		t.Fatal("failed rejoin did not start rollback")
	}
	r.rolloutRig.finish(0, r.next(0, true), true)
	r.complete("rolled_back")
	if r.rollout.Nodes[1].Maintenance != nil {
		t.Fatal("peer was withdrawn after failed first node")
	}
}

func TestManagedRejoinPinsTargetAndPeerArtifactsThroughHealthChecks(t *testing.T) {
	for _, changed := range []int{0, 1} {
		t.Run([]string{"target", "peer"}[changed], func(t *testing.T) {
			r := newManagedRolloutRig(t, NewMemory())
			r.start(r.input())
			r.rolloutRig.finish(0, r.next(0, false), true)
			for range 15 {
				r.step()
				if r.rollout.Nodes[0].Maintenance.Status == "checking_rejoin" {
					break
				}
			}
			if r.rollout.Nodes[0].Maintenance.Status != "checking_rejoin" {
				t.Fatal("health gate not reached")
			}
			// A local edit can alter the file without changing the running worker
			// generation. Passing health alone must not authorize rejoin.
			r.discoveries[changed].Configuration.ArtifactID = strings.Repeat("e", 64)
			r.complete("rollback_failed")
			if r.rollout.Nodes[0].RecoveryMaintenanceID == "" || r.rollout.Nodes[1].Maintenance != nil || !r.discoveries[2].Configuration.Document.Backends[0].Servers[0].Maintenance {
				t.Fatal("artifact drift released isolation")
			}
			if len(r.db.(*Memory).operations) != 3 {
				t.Fatal("rejoin or overwrite was queued after external drift")
			}
		})
	}
}

func concurrentManagedRolloutContract(t *testing.T, db Store) {
	r := newManagedRolloutRig(t, db)
	r.start(r.input())
	for range 3 { // Reach withdrawal and its first durable operation.
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if e := advanceRollout(r.ctx, r.rolloutRig.p, r.org, r.cluster, r.rollout.ID, r.now); e != nil {
					t.Error(e)
				}
			})
		}
		wg.Wait()
	}
	_, ops, e := db.(ControlStore).GetControl(r.ctx, r.org, r.nodes[2].ID)
	if e != nil || len(ops) != 1 || ops[0].Kind != "maintenance" {
		t.Fatal("concurrent workers duplicated the entrypoint operation", e, len(ops))
	}
}
func TestConcurrentManagedRolloutWorkers(t *testing.T) {
	concurrentManagedRolloutContract(t, NewMemory())
}
func TestPostgresConcurrentManagedRolloutWorkers(t *testing.T) {
	concurrentManagedRolloutContract(t, postgresForTest(t))
}

func TestManagedRolloutUnknownControllerCommandIsNeverReplayed(t *testing.T) {
	r := newManagedRolloutRig(t, NewMemory())
	r.start(r.input())
	// Leave the first runtime withdrawal outstanding and advance its deadline.
	for range 4 {
		r.rolloutRig.step()
	}
	if len(r.db.(*Memory).operations) != 1 {
		t.Fatal("withdrawal not queued")
	}
	r.now = r.now.Add(3 * time.Minute)
	r.rolloutRig.step()
	if r.rollout.Status != "rollback_failed" || len(r.db.(*Memory).operations) != 1 || r.rollout.Nodes[0].RecoveryMaintenanceID == "" {
		t.Fatal("unknown operation was replayed or fence released", r.rollout)
	}
}
