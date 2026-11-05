package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type maintenanceRig struct {
	*rolloutRig
	p   maintenancePersistence
	job domain.Maintenance
}

func newMaintenanceRig(t *testing.T, db Store) *maintenanceRig {
	return newMaintenanceRigForOrganization(t, db, domain.NewID())
}
func newMaintenanceRigForOrganization(t *testing.T, db Store, org string) *maintenanceRig {
	r := newRolloutRigForOrganization(t, db, org)
	c := createCluster(t, db, r.org)
	n, e := db.Enroll(r.ctx, issueToken(t, db, r.org, c.ID), domain.Node{ID: domain.NewID(), Name: "entrypoint"}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	r.nodes = append(r.nodes, n)
	r.sessions = append(r.sessions, domain.NewID())
	d := cloneDiscovery(&r.discoveries[0])
	r.discoveries = append(r.discoveries, *d)
	if _, e = db.(ControlStore).OpenControl(r.ctx, r.org, n.ID, r.sessions[2]); e != nil {
		t.Fatal(e)
	}
	for i := range r.discoveries {
		for j := range r.discoveries[i].Proxies {
			v := uint32(0)
			r.discoveries[i].Proxies[j].ActiveConnections = &v
		}
	}
	r.refresh()
	topology := domain.Topology{Entrypoint: "http://localhost:18081/", Strategy: "roundrobin", Frontend: "public", Backend: "application", Nodes: []domain.TopologyNode{{NodeID: r.nodes[0].ID, Address: "proxy-0:8080"}, {NodeID: r.nodes[1].ID, Address: "proxy-1:8080"}}, Control: &domain.EntrypointControl{NodeID: n.ID, Backend: "application", Servers: []domain.EntrypointServer{{NodeID: r.nodes[0].ID, Server: "app-a"}, {NodeID: r.nodes[1].ID, Server: "app-b"}}}}
	if e = db.(TopologyStore).SetTopology(r.ctx, r.org, r.cluster, topology); e != nil {
		t.Fatal(e)
	}
	return &maintenanceRig{rolloutRig: r, p: db.(maintenancePersistence)}
}
func (r *maintenanceRig) input(action string) domain.MaintenanceInput {
	v, e := r.db.(MaintenanceStore).GetMaintenance(r.ctx, r.org, r.nodes[0].ID)
	if e != nil {
		r.t.Fatal(e)
	}
	in := domain.MaintenanceInput{RequestID: domain.NewID(), Action: action, BindingID: v.BindingID, ExpectedGeneration: v.Target.Discovery.Generation, ExpectedEntrypointGeneration: v.Controller.Discovery.Generation, ExpectedArtifactID: v.Controller.Discovery.Configuration.ArtifactID, ExpectedVersion: v.Controller.Discovery.Configuration.Version, DrainTimeoutSeconds: 30}
	if action == "rejoin" {
		in.MaintenanceID = r.job.ID
	}
	return in
}
func (r *maintenanceRig) start() {
	r.t.Helper()
	var e error
	r.job, e = r.db.(MaintenanceStore).ChangeMaintenance(domain.WithActor(r.ctx, domain.NewID()), r.org, r.nodes[0].ID, r.input("withdraw"))
	if e != nil {
		r.t.Fatal(e)
	}
}
func (r *maintenanceRig) step() {
	r.t.Helper()
	r.now = r.now.Add(time.Second)
	r.refresh()
	if e := advanceMaintenance(r.ctx, r.p, r.org, r.nodes[0].ID, r.job.ID, r.now); e != nil {
		r.t.Fatal(e)
	}
	v, e := r.db.(MaintenanceStore).GetMaintenance(r.ctx, r.org, r.nodes[0].ID)
	if e != nil {
		r.t.Fatal(e)
	}
	r.job = *v.Current
}
func (r *maintenanceRig) next(kind string) *domain.Operation {
	r.t.Helper()
	for range 18 {
		r.step()
		if op := r.commands[2]; op != nil && op.Kind == kind {
			delete(r.commands, 2)
			return op
		}
	}
	r.t.Fatal("maintenance operation not queued", kind, r.job)
	return nil
}
func (r *maintenanceRig) finish(op *domain.Operation, success bool) {
	r.t.Helper()
	d := &r.discoveries[2]
	outcome, code := "failed", "runtime_rejected"
	if success {
		if op.Kind == "configuration" {
			doc := op.Configuration.Canonical()
			sum := sha256.Sum256([]byte(op.ID))
			d.Configuration = &domain.ConfigurationState{Version: d.Configuration.Version + 1, ArtifactID: hex.EncodeToString(sum[:]), Document: &doc, ObservedAtMS: r.now.UnixMilli(), ControlAllowed: true}
			d.StartedAt++
			d.ProcessID = fmt.Sprint(42 + d.Configuration.Version)
			for _, b := range doc.Backends {
				for _, s := range b.Servers {
					for i := range d.Servers {
						a := &d.Servers[i]
						if a.Backend == b.Name && a.Name == s.Name {
							a.State = "ready"
							a.Health = "UP"
							if s.Maintenance {
								a.State = "maint"
								a.Health = "MAINT"
							}
							a.Weight = s.Weight
							a.InitialWeight = s.Weight
						}
					}
				}
			}
		} else {
			for i := range d.Servers {
				if d.Servers[i].Name == op.Server {
					d.Servers[i].State = "maint"
					d.Servers[i].Health = "MAINT"
					if op.Kind == "restore" {
						d.Servers[i].State = "ready"
						d.Servers[i].Health = "UP"
					}
				}
			}
		}
		d.SetGeneration()
		outcome, code = "succeeded", ""
	}
	result := &domain.OperationResult{OperationID: op.ID, Outcome: outcome, ErrorCode: code, Discovery: d, CommandDigest: op.CommandDigest}
	if _, _, e := r.db.(ControlStore).ReportControl(r.ctx, r.org, r.nodes[2].ID, r.sessions[2], d, result); e != nil {
		r.t.Fatal(e)
	}
}
func (r *maintenanceRig) connections(count uint32) {
	for i := range r.discoveries[0].Proxies {
		p := &r.discoveries[0].Proxies[i]
		if p.Kind == "frontend" && p.Name == "public" {
			p.ActiveConnections = &count
		}
	}
}
func (r *maintenanceRig) hold() {
	r.start()
	r.finish(r.next("maintenance"), true)
	r.finish(r.next("configuration"), true)
	for range 8 {
		r.step()
		if r.job.Status == "maintenance" {
			return
		}
	}
	r.t.Fatal("not held", r.job)
}
func maintenanceContract(t *testing.T, db Store) {
	r := newMaintenanceRig(t, db)
	r.connections(1)
	r.start()
	for _, org := range []string{domain.NewID()} {
		if _, e := db.(MaintenanceStore).GetMaintenance(r.ctx, org, r.nodes[0].ID); !errors.Is(e, domain.ErrNotFound) {
			t.Fatal("foreign view", e)
		}
		if _, e := db.(MaintenanceStore).ChangeMaintenance(r.ctx, org, r.nodes[0].ID, r.input("withdraw")); !errors.Is(e, domain.ErrNotFound) {
			t.Fatal("foreign write", e)
		}
		if _, e := db.(MaintenanceStore).ListMaintenance(r.ctx, org, r.cluster); !errors.Is(e, domain.ErrNotFound) {
			t.Fatal("foreign history", e)
		}
	}
	if _, e := db.(RolloutStore).CreateRollout(r.ctx, r.org, r.cluster, r.rolloutRig.input()); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("rollout bypassed maintenance", e)
	}
	runtime := r.next("maintenance")
	r.finish(runtime, true)
	if r.job.Status != "withdrawing" {
		t.Fatal("runtime alone marked safe")
	}
	config := r.next("configuration")
	if !config.Configuration.Backends[0].Servers[0].Maintenance {
		t.Fatal("withdrawal was not persistent")
	}
	r.finish(config, true)
	for range 5 {
		r.step()
	}
	if r.job.Status != "draining" || r.job.Connections == nil || *r.job.Connections != 1 {
		t.Fatal("live connection was not retained", r.job)
	}
	r.connections(0)
	for range 3 {
		r.step()
		if r.job.Status == "maintenance" {
			t.Fatal("zero window skipped")
		}
	}
	r.step()
	if r.job.Status != "maintenance" {
		t.Fatal("zero connections not verified", r.job)
	}
	state, _, _ := db.(ControlStore).GetControl(r.ctx, r.org, r.nodes[0].ID)
	if state.MaintenanceBlocked {
		t.Fatal("maintained node cannot be edited")
	}
	state, _, _ = db.(ControlStore).GetControl(r.ctx, r.org, r.nodes[2].ID)
	if !state.MaintenanceBlocked {
		t.Fatal("controller fence absent")
	}
	input := r.input("rejoin")
	job, e := db.(MaintenanceStore).ChangeMaintenance(domain.WithActor(r.ctx, domain.NewID()), r.org, r.nodes[0].ID, input)
	if e != nil {
		t.Fatal(e)
	}
	r.job = job
	duplicate, e := db.(MaintenanceStore).ChangeMaintenance(domain.WithActor(r.ctx, domain.NewID()), r.org, r.nodes[0].ID, input)
	if e != nil || duplicate.ID != job.ID {
		t.Fatal("retry duplicated job", e)
	}
	for range 5 {
		r.step()
		if r.commands[2] != nil {
			t.Fatal("rejoin skipped health window")
		}
	}
	config = r.next("configuration")
	if config.Configuration.Backends[0].Servers[0].Maintenance {
		t.Fatal("rejoin kept disabled")
	}
	r.finish(config, true)
	for range 12 {
		r.step()
		if r.job.Status == "completed" {
			break
		}
	}
	if r.job.Status != "completed" {
		t.Fatal("did not rejoin", r.job)
	}
	state, _, _ = db.(ControlStore).GetControl(r.ctx, r.org, r.nodes[2].ID)
	if state.MaintenanceBlocked {
		t.Fatal("completed job retained fence")
	}
	rows, e := db.(MaintenanceStore).ListMaintenance(r.ctx, r.org, r.cluster)
	if e != nil || len(rows) != 1 || rows[0].CompletedAt == nil || rows[0].ActorUserID == "" {
		t.Fatal("history/actor lost", e)
	}
	if p, ok := db.(*Postgres); ok {
		for _, q := range []string{`DELETE FROM hm_maintenance`, `TRUNCATE hm_maintenance CASCADE`, `UPDATE hm_maintenance SET payload=jsonb_set(payload,'{baseline,backends,0,algorithm}','"first"')`} {
			if _, e := p.pool.Exec(r.ctx, q); e == nil {
				t.Fatal("history mutable")
			}
		}
	}
}
func TestMemoryWholeNodeMaintenance(t *testing.T)   { maintenanceContract(t, NewMemory()) }
func TestPostgresWholeNodeMaintenance(t *testing.T) { maintenanceContract(t, postgresForTest(t)) }
func TestMaintenanceTimeoutDoesNotRejoinOrKillConnections(t *testing.T) {
	r := newMaintenanceRig(t, NewMemory())
	r.connections(1)
	r.start()
	r.finish(r.next("maintenance"), true)
	r.finish(r.next("configuration"), true)
	for range 40 {
		r.step()
		if r.job.Status == "attention" {
			break
		}
	}
	if r.job.Status != "attention" || r.discoveries[2].Servers[0].State != "maint" || r.job.OperationID != "" {
		t.Fatal("timeout released node", r.job)
	}
	if len(r.db.(*Memory).operations) != 2 {
		t.Fatal("timeout dispatched another mutation")
	}
}
func TestUnhealthyRejoinReturnsToPersistentIsolation(t *testing.T) {
	r := newMaintenanceRig(t, NewMemory())
	r.hold()
	var e error
	r.job, e = r.db.(MaintenanceStore).ChangeMaintenance(r.ctx, r.org, r.nodes[0].ID, r.input("rejoin"))
	if e != nil {
		t.Fatal(e)
	}
	config := r.next("configuration")
	r.finish(config, true)
	r.discoveries[0].Servers[0].Health = "DOWN"
	r.step()
	if r.job.Status != "withdrawing" || r.job.ErrorCode != "rejoin_failed" {
		t.Fatal("no automatic isolation", r.job)
	}
	r.finish(r.next("maintenance"), true)
	r.finish(r.next("configuration"), true)
	for range 8 {
		r.step()
		if r.job.Status == "maintenance" {
			break
		}
	}
	if r.job.Status != "maintenance" || !r.discoveries[2].Configuration.Document.Backends[0].Servers[0].Maintenance {
		t.Fatal("failed rejoin not isolated", r.job)
	}
}
func TestMissingFrontendCountAndForeignControllerAreRejected(t *testing.T) {
	r := newMaintenanceRig(t, NewMemory())
	for i := range r.discoveries[0].Proxies {
		r.discoveries[0].Proxies[i].ActiveConnections = nil
	}
	r.refresh()
	if _, e := r.db.(MaintenanceStore).ChangeMaintenance(r.ctx, r.org, r.nodes[0].ID, r.input("withdraw")); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("unknown count treated as zero", e)
	}
	c := createCluster(t, r.db, domain.NewID())
	foreign, e := r.db.Enroll(r.ctx, issueToken(t, r.db, c.OrganizationID, c.ID), domain.Node{ID: domain.NewID(), Name: "foreign"}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	db := r.db.(*Memory)
	topology := *cloneCluster(db.clusters[r.cluster]).Topology
	topology.Control.NodeID = foreign.ID
	if e = db.SetTopology(r.ctx, r.org, r.cluster, topology); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("foreign controller bound", e)
	}
}

func TestRejoinWaitsForMissingConfigurationButRejectsObservedDrift(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(fmt.Sprint(drift), func(t *testing.T) {
			r := newMaintenanceRig(t, NewMemory())
			r.hold()
			var err error
			r.job, err = r.db.(MaintenanceStore).ChangeMaintenance(r.ctx, r.org, r.nodes[0].ID, r.input("rejoin"))
			if err != nil {
				t.Fatal(err)
			}
			c := r.discoveries[0].Configuration
			if drift {
				c.ArtifactID = strings.Repeat("f", 64)
			} else {
				r.discoveries[0].Configuration = &domain.ConfigurationState{ErrorCode: "configuration_unavailable"}
			}
			r.step()
			if drift {
				if r.job.Status != "attention" || r.job.ErrorCode != "target_changed" {
					t.Fatal("known drift accepted", r.job)
				}
				return
			}
			if r.job.Status != "checking_rejoin" || r.job.HealthSinceMS != 0 {
				t.Fatal("unknown observation treated as drift or health", r.job)
			}
			r.discoveries[0].Configuration = c
			for range 8 {
				r.step()
				if r.job.Status == "rejoining" {
					break
				}
			}
			if r.job.Status != "rejoining" {
				t.Fatal("fresh health could not resume", r.job)
			}
		})
	}
}

func TestHeldTargetRemainsEditableWhilePeersAndEntrypointAreFenced(t *testing.T) {
	r := newMaintenanceRig(t, NewMemory())
	r.hold()
	for _, i := range []int{1, 2} {
		d := r.discoveries[i]
		doc := d.Configuration.Document.Canonical()
		doc.Backends[0].Servers[0].Weight = 120
		if _, e := r.db.(ConfigurationStore).CreateConfiguration(r.ctx, r.org, domain.Operation{RequestID: domain.NewID(), NodeID: r.nodes[i].ID, ExpectedGeneration: d.Generation, BaseArtifactID: d.Configuration.ArtifactID, BaseVersion: d.Configuration.Version, Configuration: &doc}, ""); !errors.Is(e, domain.ErrConflict) {
			t.Fatal("maintenance fence bypassed", i, e)
		}
	}
	d := r.discoveries[0]
	doc := d.Configuration.Document.Canonical()
	doc.Backends[0].Servers[0].Weight = 120
	op, e := r.db.(ConfigurationStore).CreateConfiguration(r.ctx, r.org, domain.Operation{RequestID: domain.NewID(), NodeID: r.nodes[0].ID, ExpectedGeneration: d.Generation, BaseArtifactID: d.Configuration.ArtifactID, BaseVersion: d.Configuration.Version, Configuration: &doc}, "")
	if e != nil {
		t.Fatal("held target edit rejected", e)
	}
	r.refresh()
	cmd := r.commands[0]
	if cmd == nil || cmd.ID != op.ID {
		t.Fatal("held edit not dispatched")
	}
	r.rolloutRig.finish(0, cmd, true)
	r.step()
	if r.job.Status != "maintenance" {
		t.Fatal("target reload released reservation")
	}
	input := r.input("rejoin")
	if _, e = r.db.(MaintenanceStore).ChangeMaintenance(r.ctx, r.org, r.nodes[0].ID, input); e != nil {
		t.Fatal("edited target could not rejoin", e)
	}
}
func TestMaintenanceNeverOverwritesExternalEntrypointConfiguration(t *testing.T) {
	r := newMaintenanceRig(t, NewMemory())
	r.hold()
	r.discoveries[2].Configuration.ArtifactID = fmt.Sprintf("%064x", 53)
	r.step()
	if r.job.Status != "attention" || r.job.ErrorCode != "entrypoint_configuration_changed" {
		t.Fatal("external artifact change ignored", r.job)
	}
	if len(r.db.(*Memory).operations) != 2 {
		t.Fatal("external config overwritten")
	}
	if _, e := r.db.(MaintenanceStore).ChangeMaintenance(r.ctx, r.org, r.nodes[0].ID, r.input("rejoin")); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("external config overwritten on rejoin", e)
	}
}
func TestMaintenanceRetryAfterExactArtifactRestorationRequiresFreshVersion(t *testing.T) {
	r := newMaintenanceRig(t, NewMemory())
	r.hold()
	stale := r.input("rejoin")
	r.discoveries[2].Configuration.Version++
	r.step()
	if r.job.Status != "attention" || r.job.ErrorCode != "entrypoint_configuration_changed" {
		t.Fatal("version change was not fenced", r.job)
	}
	if _, err := r.db.(MaintenanceStore).ChangeMaintenance(r.ctx, r.org, r.nodes[0].ID, stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale counter accepted", err)
	}
	job, err := r.db.(MaintenanceStore).ChangeMaintenance(r.ctx, r.org, r.nodes[0].ID, r.input("rejoin"))
	if err != nil || job.OwnedVersion != r.discoveries[2].Configuration.Version {
		t.Fatal("exact restored artifact could not adopt its current counter", job, err)
	}
	r.job = job
	r.finish(r.next("configuration"), true)
	for range 12 {
		r.step()
		if r.job.Status == "completed" {
			return
		}
	}
	t.Fatal("exact artifact recovery did not rejoin", r.job)
}
func TestConcurrentMaintenanceWorkersKeepOneDurableOperation(t *testing.T) {
	r := newMaintenanceRig(t, NewMemory())
	r.start()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if e := r.db.(MaintenanceStore).AdvanceMaintenance(r.ctx); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	db := r.db.(*Memory)
	if len(db.operations) != 1 {
		t.Fatal("duplicate maintenance command", len(db.operations))
	}
	in := r.input("withdraw")
	in.RequestID = domain.NewID()
	if _, e := r.db.(MaintenanceStore).ChangeMaintenance(r.ctx, r.org, r.nodes[1].ID, in); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("second node withdrawal accepted", e)
	}
}
