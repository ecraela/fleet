package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type rolloutRig struct {
	t            *testing.T
	ctx          context.Context
	db           Store
	p            rolloutPersistence
	org, cluster string
	nodes        []domain.Node
	sessions     []string
	discoveries  []domain.Discovery
	baseline     []domain.ConfigurationRevision
	commands     map[int]*domain.Operation
	now          time.Time
	rollout      domain.Rollout
}

func newRolloutRig(t *testing.T, db Store) *rolloutRig {
	return newRolloutRigForOrganization(t, db, domain.NewID())
}
func newRolloutRigForOrganization(t *testing.T, db Store, org string) *rolloutRig {
	t.Helper()
	r := &rolloutRig{t: t, ctx: t.Context(), db: db, p: db.(rolloutPersistence), org: org, commands: map[int]*domain.Operation{}, now: time.Now().UTC()}
	c := createCluster(t, db, r.org)
	r.cluster = c.ID
	for i := range 2 {
		n, err := db.Enroll(r.ctx, issueToken(t, db, r.org, c.ID), domain.Node{ID: domain.NewID(), Name: fmt.Sprintf("proxy-%d", i)}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		r.nodes = append(r.nodes, n)
		r.sessions = append(r.sessions, domain.NewID())
		d := configurationDiscovery()
		for j := range d.Proxies {
			if d.Proxies[j].Kind == "frontend" {
				d.Proxies[j].Status = "OPEN"
			} else {
				d.Proxies[j].Status = "UP"
			}
		}
		for j := range d.Servers {
			d.Servers[j].CheckStatus = "L7OK"
		}
		d.SetGeneration()
		r.discoveries = append(r.discoveries, d)
		_, err = db.(ControlStore).OpenControl(r.ctx, r.org, n.ID, r.sessions[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	r.refresh()
	for _, n := range r.nodes {
		_, versions, err := db.(ConfigurationStore).GetConfiguration(r.ctx, r.org, n.ID)
		if err != nil {
			t.Fatal(err)
		}
		r.baseline = append(r.baseline, versions[0])
	}
	return r
}
func (r *rolloutRig) input() domain.RolloutInput {
	in := domain.RolloutInput{RequestID: domain.NewID(), Document: r.baseline[0].Document.Canonical()}
	in.Document.Backends[0].Servers[0].Weight = 200
	for i, n := range r.nodes {
		d := r.discoveries[i]
		in.Nodes = append(in.Nodes, domain.RolloutTarget{NodeID: n.ID, ExpectedGeneration: d.Generation, ExpectedArtifactID: d.Configuration.ArtifactID, ExpectedVersion: d.Configuration.Version})
	}
	return in
}
func (r *rolloutRig) refresh() {
	r.t.Helper()
	for i, n := range r.nodes {
		d := &r.discoveries[i]
		d.ObservedAtMS = r.now.UnixMilli()
		d.Configuration.ObservedAtMS = d.ObservedAtMS
		_, command, err := r.db.(ControlStore).ReportControl(r.ctx, r.org, n.ID, r.sessions[i], d, nil)
		if err != nil {
			r.t.Fatal(err)
		}
		if command != nil {
			r.commands[i] = command
		}
		// Keep all three clocks coherent while advancing a deterministic test clock.
		err = r.p.controlTransaction(r.ctx, r.org, n.ID, func(tx *controlTransaction) error { tx.state.LastSeen = r.now; return nil })
		if err != nil {
			r.t.Fatal(err)
		}
	}
}
func (r *rolloutRig) step() {
	r.t.Helper()
	r.now = r.now.Add(time.Second)
	r.refresh()
	if err := advanceRollout(r.ctx, r.p, r.org, r.cluster, r.rollout.ID, r.now); err != nil {
		r.t.Fatalf("checkpoint: %v; rollout %s index %d nodes %+v", err, r.rollout.Status, r.rollout.Index, r.rollout.Nodes)
	}
	rows, err := r.db.(RolloutStore).ListRollouts(r.ctx, r.org, r.cluster)
	if err != nil {
		r.t.Fatal(err)
	}
	r.rollout = rows[0]
}
func (r *rolloutRig) start(input domain.RolloutInput) {
	r.t.Helper()
	var err error
	r.rollout, err = r.db.(RolloutStore).CreateRollout(domain.WithActor(r.ctx, domain.NewID()), r.org, r.cluster, input)
	if err != nil {
		r.t.Fatal(err)
	}
}
func (r *rolloutRig) next(i int, rollback bool) *domain.Operation {
	r.t.Helper()
	for range 15 {
		r.step()
		cmd := r.commands[i]
		if cmd != nil && (cmd.RollbackArtifactID != "") == rollback {
			delete(r.commands, i)
			return cmd
		}
	}
	r.t.Fatal("command was not scheduled", i, rollback, r.rollout.Status, r.rollout.Nodes)
	return nil
}
func (r *rolloutRig) finish(i int, command *domain.Operation, success bool) {
	r.t.Helper()
	d := &r.discoveries[i]
	outcome, code := "failed", "configuration_rejected"
	if success {
		doc := command.Configuration.Canonical()
		d.ProcessID = fmt.Sprintf("%d", 8+d.Configuration.Version)
		d.StartedAt++
		sum := sha256.Sum256([]byte(command.ID))
		artifact := hex.EncodeToString(sum[:])
		if command.RollbackArtifactID != "" {
			artifact = command.RollbackArtifactID
		}
		previous := d.Configuration
		d.Configuration = &domain.ConfigurationState{NodeID: previous.NodeID, ACMEGrants: previous.ACMEGrants, Version: previous.Version + 1, ArtifactID: artifact, Document: &doc, ObservedAtMS: r.now.UnixMilli(), ControlAllowed: true, ApplicationControlVersion: previous.ApplicationControlVersion, ListenerControlVersion: previous.ListenerControlVersion, ListenerBindings: previous.ListenerBindings, TLSCertificates: previous.TLSCertificates, TLSListeners: previous.TLSListeners}
		for _, b := range doc.Backends {
			for _, s := range b.Servers {
				for j := range d.Servers {
					actual := &d.Servers[j]
					if actual.Backend == b.Name && actual.Name == s.Name {
						actual.Weight = s.Weight
						actual.InitialWeight = s.Weight
						actual.State = "ready"
						actual.Health = "UP"
					}
				}
			}
		}
		d.ObservedAtMS = r.now.UnixMilli()
		d.SetGeneration()
		outcome, code = "succeeded", ""
	}
	result := &domain.OperationResult{OperationID: command.ID, Outcome: outcome, ErrorCode: code, Discovery: d, CommandDigest: command.CommandDigest}
	if _, _, err := r.db.(ControlStore).ReportControl(r.ctx, r.org, r.nodes[i].ID, r.sessions[i], d, result); err != nil {
		r.t.Fatal(err)
	}
}
func rolloutContract(t *testing.T, db Store) {
	r := newRolloutRig(t, db)
	input := r.input()
	if _, err := db.(RolloutStore).ListRollouts(r.ctx, domain.NewID(), r.cluster); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign rollout history exposed", err)
	}
	if _, err := db.(RolloutStore).CreateRollout(r.ctx, domain.NewID(), r.cluster, input); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign rollout accepted", err)
	}
	r.start(input)
	repeated, err := db.(RolloutStore).CreateRollout(r.ctx, r.org, r.cluster, input)
	if err != nil || repeated.ID != r.rollout.ID {
		t.Fatal("duplicate rollout created", err)
	}
	changed := input
	changed.Document = input.Document.Canonical()
	changed.Document.Backends[0].Servers[0].Weight = 150
	if _, err := db.(RolloutStore).CreateRollout(r.ctx, r.org, r.cluster, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("request ID changed rollout contents", err)
	}
	changed.RequestID = domain.NewID()
	if _, err := db.(RolloutStore).CreateRollout(r.ctx, r.org, r.cluster, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("concurrent cluster rollout accepted", err)
	}
	d := r.discoveries[1]
	if _, err := db.(ConfigurationStore).CreateConfiguration(r.ctx, r.org, domain.Operation{RequestID: domain.NewID(), NodeID: r.nodes[1].ID, ExpectedGeneration: d.Generation, BaseArtifactID: d.Configuration.ArtifactID, BaseVersion: 1, Configuration: &input.Document}, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("single-node configuration bypassed rollout", err)
	}
	if _, err := db.(ControlStore).CreateOperation(r.ctx, r.org, domain.Operation{RequestID: domain.NewID(), NodeID: r.nodes[1].ID, Kind: "weight", Backend: "application", Server: "app-a", Weight: 50, ExpectedGeneration: d.Generation, ExpectedState: "ready", ExpectedWeight: 100}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("runtime mutation bypassed rollout", err)
	}
	first := r.next(0, false)
	for range 3 {
		r.step()
	}
	if r.rollout.Nodes[1].ApplyOperationID != "" {
		t.Fatal("second node started before first finished")
	}
	r.finish(0, first, true)
	for range 4 {
		r.step()
		if r.rollout.Nodes[1].ApplyOperationID != "" {
			t.Fatal("health window was skipped")
		}
	}
	second := r.next(1, false)
	r.finish(1, second, true)
	for range 10 {
		r.step()
		if r.rollout.Terminal() {
			break
		}
	}
	if r.rollout.Status != "succeeded" {
		t.Fatal("healthy rollout failed", r.rollout)
	}
	for _, n := range r.rollout.Nodes {
		if n.Status != "applied" {
			t.Fatal("missing per-node result")
		}
	}
	// The public operation ID is retained, and new versions link back to it.
	_, ops, err := db.(ControlStore).GetControl(r.ctx, r.org, r.nodes[0].ID)
	if err != nil || ops[0].RolloutID != r.rollout.ID {
		t.Fatal("node operation lost rollout attribution", err)
	}
	switch p := db.(type) {
	case *Memory:
		count := 0
		for _, event := range p.audit {
			if event.rolloutID == r.rollout.ID && event.action == "rollout.started" {
				count++
			}
		}
		if count != 1 {
			t.Fatal("duplicate start audit")
		}
	case *Postgres:
		var count int
		if err := p.pool.QueryRow(r.ctx, `SELECT count(*) FROM hm_audit_events WHERE rollout_id=$1 AND action='rollout.started'`, r.rollout.ID).Scan(&count); err != nil || count != 1 {
			t.Fatal("rollout audit lost", err)
		}
		for _, query := range []string{`UPDATE hm_rollouts SET payload=jsonb_set(payload,'{document,backends,0,algorithm}','"first"')`, `DELETE FROM hm_rollouts`, `TRUNCATE hm_rollouts CASCADE`} {
			if _, err := p.pool.Exec(r.ctx, query); err == nil {
				t.Fatal("rollout history was mutable")
			}
		}
	}
}
func TestMemoryRollingConfiguration(t *testing.T)   { rolloutContract(t, NewMemory()) }
func TestPostgresRollingConfiguration(t *testing.T) { rolloutContract(t, postgresForTest(t)) }
func rollbackContract(t *testing.T, db Store) {
	r := newRolloutRig(t, db)
	r.start(r.input())
	a := r.next(0, false)
	r.finish(0, a, true)
	b := r.next(1, false)
	r.finish(1, b, true)
	r.discoveries[1].Servers[1].Health = "DOWN"
	for range 25 {
		r.step()
		if r.rollout.Status == "rolling_back" {
			break
		}
	}
	if r.rollout.Status != "rolling_back" {
		t.Fatal("unhealthy second node did not trigger rollback", r.rollout)
	}
	restoreB := r.next(1, true)
	if r.rollout.Nodes[0].RollbackOperationID != "" {
		t.Fatal("rollback did not start with the last changed node")
	}
	r.finish(1, restoreB, true)
	restoreA := r.next(0, true)
	r.finish(0, restoreA, true)
	for range 10 {
		r.step()
		if r.rollout.Terminal() {
			break
		}
	}
	if r.rollout.Status != "rolled_back" {
		t.Fatal("automatic rollback failed", r.rollout)
	}
	for i, node := range r.nodes {
		state, versions, err := db.(ConfigurationStore).GetConfiguration(r.ctx, r.org, node.ID)
		if err != nil || versions[0].RollbackOf != r.baseline[i].ID || versions[0].Status != "applied" || state.CurrentRevisionID != versions[0].ID {
			t.Fatal("rollback lost per-node snapshots or history", err)
		}
	}
}
func TestMemoryRollingHealthFailureRestoresEveryChangedNode(t *testing.T) {
	rollbackContract(t, NewMemory())
}
func TestPostgresRollingHealthFailureRestoresEveryChangedNode(t *testing.T) {
	rollbackContract(t, postgresForTest(t))
}
func TestConcurrentRolloutWorkersQueueOnlyOneNodeOperation(t *testing.T) {
	r := newRolloutRig(t, NewMemory())
	r.start(r.input())
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := r.db.(RolloutStore).AdvanceRollouts(r.ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	db := r.db.(*Memory)
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.operations) != 1 {
		t.Fatal("workers queued duplicate operations", len(db.operations))
	}
}

func TestUnsentWorkOnOfflineNodeDoesNotDelayRestoringChangedPeers(t *testing.T) {
	r := newRolloutRig(t, NewMemory())
	r.start(r.input())
	first := r.next(0, false)
	r.finish(0, first, true)
	for range 12 {
		r.step()
		if r.rollout.Nodes[1].ApplyOperationID != "" {
			break
		}
	}
	if r.rollout.Nodes[1].ApplyOperationID == "" {
		t.Fatal("second command was not queued")
	}
	// Stop reports before this queued operation can be dispatched.
	db := r.db.(*Memory)
	db.mu.Lock()
	record := db.rollouts[r.rollout.ID]
	failRollout(&record, "node_unavailable", r.now)
	db.rollouts[record.ID] = record
	state := db.controls[r.nodes[1].ID]
	state.LastSeen = r.now.Add(-time.Minute)
	db.controls[state.NodeID] = state
	db.mu.Unlock()
	if err := advanceRollout(r.ctx, r.p, r.org, r.cluster, r.rollout.ID, r.now); err != nil {
		t.Fatal(err)
	}
	rows, _ := db.ListRollouts(r.ctx, r.org, r.cluster)
	r.rollout = rows[0]
	if r.rollout.Index != 0 || r.rollout.Nodes[1].Status != "skipped" {
		t.Fatal("offline unsent work blocked recovery", r.rollout)
	}
	_, ops, _ := db.GetControl(r.ctx, r.org, r.nodes[1].ID)
	if ops[0].Status != "failed" || ops[0].ErrorCode != "rollout_cancelled" || ops[0].CommandDigest != "" {
		t.Fatal("unsent operation remained dispatchable")
	}
	restore := r.next(0, true)
	r.finish(0, restore, true)
	for range 10 {
		r.step()
		if r.rollout.Terminal() {
			break
		}
	}
	if r.rollout.Status != "rolled_back" {
		t.Fatal(r.rollout.Status)
	}
}
func TestRecoveryDeadlineCancelsWorkThatWasNeverDispatched(t *testing.T) {
	r := newRolloutRig(t, NewMemory())
	r.start(r.input())
	r.step()
	r.step()
	db := r.db.(*Memory)
	db.mu.Lock()
	record := db.rollouts[r.rollout.ID]
	if record.Nodes[0].ApplyOperationID == "" {
		db.mu.Unlock()
		t.Fatal("first operation was not queued")
	}
	failRollout(&record, "rollout_deadline", r.now)
	record.Deadline = r.now.Add(-time.Second)
	db.rollouts[record.ID] = record
	db.mu.Unlock()
	if err := advanceRollout(r.ctx, r.p, r.org, r.cluster, record.ID, r.now); err != nil {
		t.Fatal(err)
	}
	state, ops, err := db.GetControl(r.ctx, r.org, r.nodes[0].ID)
	if err != nil || len(ops) != 1 || ops[0].Status != "failed" || ops[0].ErrorCode != "rollout_cancelled" || state.PendingOperationID != "" {
		t.Fatal("deadline left a command dispatchable after releasing the cluster", err, ops)
	}
}

func TestUnknownDispatchedWorkIsNotReplayedAfterControllerReconnect(t *testing.T) {
	r := newRolloutRig(t, NewMemory())
	r.start(r.input())
	op := r.next(0, false)
	r.sessions[0] = domain.NewID()
	if _, err := r.db.(ControlStore).OpenControl(r.ctx, r.org, r.nodes[0].ID, r.sessions[0]); err != nil {
		t.Fatal(err)
	}
	r.now = op.Deadline.Add(4 * time.Second)
	r.step()
	if r.rollout.Status != "rolling_back" {
		t.Fatal("unknown result did not stop rollout")
	}
	for range 6 {
		r.step()
		if r.rollout.Terminal() {
			break
		}
	}
	if r.rollout.Status != "rolled_back" || r.rollout.Nodes[1].ApplyOperationID != "" {
		t.Fatal("unknown write advanced or replayed", r.rollout)
	}
	db := r.db.(*Memory)
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.operations) != 1 {
		t.Fatal("unknown unchanged write was replayed", len(db.operations))
	}
}
func TestRolloutHealthWindowRequiresNewContinuousObservations(t *testing.T) {
	r := newRolloutRig(t, NewMemory())
	state, _, _ := r.db.(ControlStore).GetControl(r.ctx, r.org, r.nodes[0].ID)
	doc := r.baseline[0].Document
	o := rolloutObservation{state: state}
	node := domain.RolloutNode{}
	if healthWindow(&node, o, doc, r.now) {
		t.Fatal("first observation passed")
	}
	for range 8 {
		if healthWindow(&node, o, doc, r.now) {
			t.Fatal("same observation advanced health window")
		}
	}
	o.state.Discovery.ObservedAtMS += 4000
	o.state.Discovery.Configuration.ObservedAtMS += 4000
	o.state.LastSeen = r.now.Add(4 * time.Second)
	if healthWindow(&node, o, doc, r.now.Add(4*time.Second)) || node.HealthSinceMS != o.state.Discovery.ObservedAtMS {
		t.Fatal("missing observations did not restart health window")
	}
	for _, check := range []string{"", "INI", "L4CON", "L7STS"} {
		copy := cloneDiscovery(state.Discovery)
		copy.Servers[0].CheckStatus = check
		invalid := state
		invalid.Discovery = copy
		if domain.RolloutHealthy(invalid, doc, r.now) {
			t.Fatal("unchecked or failed server passed", check)
		}
	}
}
func TestMembershipChangeStopsAnActiveRollout(t *testing.T) {
	r := newRolloutRig(t, NewMemory())
	r.start(r.input())
	_, err := r.db.Enroll(r.ctx, issueToken(t, r.db, r.org, r.cluster), domain.Node{ID: domain.NewID(), Name: "new-member"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r.step()
	if r.rollout.Status != "rolling_back" || r.rollout.ErrorCode != "cluster_membership_changed" {
		t.Fatal("membership drift was ignored", r.rollout.Status)
	}
}

func TestHealthWindowUsesManagerTimeRatherThanAgentClockAdvance(t *testing.T) {
	r := newRolloutRig(t, NewMemory())
	state, _, _ := r.db.(ControlStore).GetControl(r.ctx, r.org, r.nodes[0].ID)
	o := rolloutObservation{state: state}
	n := domain.RolloutNode{}
	for i := range 7 {
		o.state.Discovery.ObservedAtMS = r.now.Add(time.Duration(i) * time.Second).UnixMilli()
		o.state.Discovery.Configuration.ObservedAtMS = o.state.Discovery.ObservedAtMS
		if healthWindow(&n, o, r.baseline[0].Document, r.now.Add(time.Duration(i)*100*time.Millisecond)) {
			t.Fatal("agent clock advance bypassed five seconds of manager time")
		}
	}
}
func TestAutomaticRollbackDoesNotOverwriteAnExternalConfigurationChange(t *testing.T) {
	r := newRolloutRig(t, NewMemory())
	r.start(r.input())
	op := r.next(0, false)
	r.finish(0, op, true)
	r.step()
	r.discoveries[0].Configuration.ArtifactID = fmt.Sprintf("%064x", 73)
	for range 8 {
		r.step()
		if r.rollout.Terminal() {
			break
		}
	}
	if r.rollout.Status != "rollback_failed" || r.rollout.Nodes[0].ErrorCode != "configuration_changed" {
		t.Fatal("external configuration was not fenced", r.rollout)
	}
	db := r.db.(*Memory)
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.operations) != 1 {
		t.Fatal("recovery overwrote a configuration outside its artifact", len(db.operations))
	}
}
