package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func configurationDiscovery() domain.Discovery {
	d := controlDiscovery()
	doc := domain.ConfigurationDocument{Backends: []domain.ConfigurationBackend{{Name: "application", Algorithm: "roundrobin", Servers: []domain.ConfigurationServer{
		{Name: "app-a", Address: "127.0.0.1", Port: 8080, Weight: 100}, {Name: "app-b", Address: "127.0.0.2", Port: 8080, Weight: 100},
	}}}}
	d.Configuration = &domain.ConfigurationState{Version: 1, ArtifactID: strings.Repeat("a", 64), Document: &doc, ControlAllowed: true, ObservedAtMS: time.Now().UnixMilli()}
	return d
}
func configurationContract(t *testing.T, db Store) {
	t.Helper()
	ctx := t.Context()
	org := domain.NewID()
	cluster := createCluster(t, db, org)
	node, err := db.Enroll(ctx, issueToken(t, db, org, cluster.ID), domain.Node{ID: domain.NewID(), Name: "proxy"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	control := db.(ControlStore)
	configs := db.(ConfigurationStore)
	session := domain.NewID()
	d := configurationDiscovery()
	if _, err = control.OpenControl(ctx, org, node.ID, session); err != nil {
		t.Fatal(err)
	}
	if _, _, err = control.ReportControl(ctx, org, node.ID, session, &d, nil); err != nil {
		t.Fatal(err)
	}
	state, versions, err := configs.GetConfiguration(ctx, org, node.ID)
	if err != nil || len(versions) != 1 || versions[0].Number != 1 || versions[0].ID != state.CurrentRevisionID {
		t.Fatal("baseline not captured", err)
	}
	original := versions[0]
	desired := d.Configuration.Document.Canonical()
	desired.Backends[0].Servers[0].Weight = 200
	input := domain.Operation{RequestID: domain.NewID(), NodeID: node.ID, ExpectedGeneration: d.Generation, BaseArtifactID: d.Configuration.ArtifactID, BaseVersion: 1, Configuration: &desired}
	foreign := domain.NewID()
	if _, _, err = configs.GetConfiguration(ctx, foreign, node.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign version history exposed", err)
	}
	if _, err = configs.CreateConfiguration(ctx, foreign, input, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign config write accepted", err)
	}
	if _, err = configs.CreateConfiguration(ctx, foreign, input, original.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign rollback accepted", err)
	}
	d.Configuration.ControlAllowed = false
	_, _, _ = control.ReportControl(ctx, org, node.ID, session, &d, nil)
	if _, err = configs.CreateConfiguration(ctx, org, input, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("local read-only policy bypassed", err)
	}
	d.Configuration.ControlAllowed = true
	_, _, _ = control.ReportControl(ctx, org, node.ID, session, &d, nil)
	actor := domain.NewID()
	first, err := configs.CreateConfiguration(domain.WithActor(ctx, actor), org, input, "")
	if err != nil {
		t.Fatal("direct save rejected", err)
	}
	duplicate, err := configs.CreateConfiguration(ctx, org, input, "")
	if err != nil || first.ID != duplicate.ID {
		t.Fatal("duplicate created another revision", err)
	}
	changed := input
	changed.RequestID = domain.NewID()
	if _, err = configs.CreateConfiguration(ctx, org, changed, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("concurrent save accepted", err)
	}
	_, command, err := control.ReportControl(ctx, org, node.ID, session, &d, nil)
	if err != nil || command == nil || command.ID != first.ID {
		t.Fatal("configuration was not dispatched", err)
	}
	d.ProcessID = "9"
	d.Servers[0].InitialWeight = 200
	d.Servers[0].Weight = 200
	d.Configuration = &domain.ConfigurationState{Version: 2, ArtifactID: strings.Repeat("b", 64), Document: &desired, ControlAllowed: true, ObservedAtMS: time.Now().UnixMilli()}
	d.ObservedAtMS = time.Now().UnixMilli()
	d.SetGeneration()
	result := &domain.OperationResult{OperationID: first.ID, Outcome: "succeeded", Discovery: &d, CommandDigest: command.CommandDigest}
	if _, _, err = control.ReportControl(ctx, org, node.ID, session, &d, result); err != nil {
		t.Fatal(err)
	}
	if _, _, err = control.ReportControl(ctx, org, node.ID, session, &d, result); err != nil {
		t.Fatal("ack replay failed", err)
	}
	state, versions, err = configs.GetConfiguration(ctx, org, node.ID)
	if err != nil || len(versions) != 2 || versions[0].Status != "applied" || versions[0].ActorUserID != actor || state.CurrentRevisionID != first.ConfigurationRevisionID || versions[1].Document.Backends[0].Servers[0].Weight != 100 {
		t.Fatal("immutable version content or applied status lost", versions, err)
	}
	bad := input
	bad.RequestID = domain.NewID()
	if _, err = configs.CreateConfiguration(ctx, org, bad, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale file version accepted", err)
	}
	// Revisions are scoped to both tenant and node, even within the same tenant.
	peer, err := db.Enroll(ctx, issueToken(t, db, org, cluster.ID), domain.Node{ID: domain.NewID(), Name: "peer"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	peerSession := domain.NewID()
	_, _ = control.OpenControl(ctx, org, peer.ID, peerSession)
	_, _, _ = control.ReportControl(ctx, org, peer.ID, peerSession, &d, nil)
	_, peerVersions, err := configs.GetConfiguration(ctx, org, peer.ID)
	if err != nil {
		t.Fatal(err)
	}
	rollbackInput := domain.Operation{RequestID: domain.NewID(), NodeID: node.ID, ExpectedGeneration: d.Generation, BaseArtifactID: d.Configuration.ArtifactID, BaseVersion: 2}
	if _, err = configs.CreateConfiguration(ctx, org, rollbackInput, peerVersions[0].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("another node's snapshot restored", err)
	}
	rollback, err := configs.CreateConfiguration(ctx, org, rollbackInput, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, restoreCommand, err := control.ReportControl(ctx, org, node.ID, session, &d, nil)
	if err != nil || restoreCommand == nil {
		t.Fatal(err)
	}
	nextSession := domain.NewID()
	_, _ = control.OpenControl(ctx, org, node.ID, nextSession)
	state, versions, err = configs.GetConfiguration(ctx, org, node.ID)
	if err != nil || versions[0].Status != "unknown" || versions[0].RollbackOf != original.ID || versions[0].Number != 3 {
		t.Fatal("interrupted rollback lost history", err)
	}
	d.ProcessID = "10"
	d.Servers[0].InitialWeight = 100
	d.Servers[0].Weight = 100
	restored := original.Document.Canonical()
	d.Configuration = &domain.ConfigurationState{Version: 3, ArtifactID: original.ArtifactID, Document: &restored, ControlAllowed: true, ObservedAtMS: time.Now().UnixMilli()}
	d.ObservedAtMS = time.Now().UnixMilli()
	d.SetGeneration()
	restoredResult := &domain.OperationResult{OperationID: rollback.ID, Outcome: "succeeded", Discovery: &d, CommandDigest: restoreCommand.CommandDigest}
	if _, _, err = control.ReportControl(ctx, org, node.ID, session, &d, restoredResult); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old controller retained authority", err)
	}
	if _, _, err = control.ReportControl(ctx, org, node.ID, nextSession, &d, restoredResult); err != nil {
		t.Fatal(err)
	}
	state, versions, err = configs.GetConfiguration(ctx, org, node.ID)
	if err != nil || len(versions) != 3 || versions[0].Status != "applied" || state.CurrentRevisionID != rollback.ConfigurationRevisionID {
		t.Fatal("rollback was not a new verified revision", versions, err)
	}
	if repeated, err := configs.CreateConfiguration(ctx, org, rollbackInput, original.ID); err != nil || repeated.ID != rollback.ID {
		t.Fatal("completed rollback lost idempotency", err)
	}
	if _, err := configs.CreateConfiguration(ctx, org, rollbackInput, versions[0].ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("request ID accepted another revision with identical content", err)
	}
	// A run of rejected changes must not hide the older current revision from
	// the bounded history response, or the console loses its current settings.
	for range 52 {
		d.ObservedAtMS = time.Now().UnixMilli()
		d.Configuration.ObservedAtMS = d.ObservedAtMS
		_, _, _ = control.ReportControl(ctx, org, node.ID, nextSession, &d, nil)
		attempt := domain.Operation{RequestID: domain.NewID(), NodeID: node.ID, ExpectedGeneration: d.Generation, BaseArtifactID: d.Configuration.ArtifactID, BaseVersion: 3, Configuration: &desired}
		op, err := configs.CreateConfiguration(ctx, org, attempt, "")
		if err != nil {
			t.Fatal(err)
		}
		_, command, err := control.ReportControl(ctx, org, node.ID, nextSession, &d, nil)
		if err != nil || command == nil {
			t.Fatal("failed change was not dispatched", err)
		}
		failure := &domain.OperationResult{OperationID: op.ID, Outcome: "failed", ErrorCode: "configuration_rejected", Discovery: &d, CommandDigest: command.CommandDigest}
		if _, _, err := control.ReportControl(ctx, org, node.ID, nextSession, &d, failure); err != nil {
			t.Fatal(err)
		}
	}
	state, versions, err = configs.GetConfiguration(ctx, org, node.ID)
	if err != nil || len(versions) != 50 || versions[len(versions)-1].ID != state.CurrentRevisionID || state.CurrentRevisionID != rollback.ConfigurationRevisionID {
		t.Fatal("bounded history lost its current revision", len(versions), err)
	}
	switch p := db.(type) {
	case *Memory:
		count := 0
		for _, e := range p.audit {
			if e.operationID == first.ID && e.action == "operation.requested" {
				count++
				if e.actorUserID != actor {
					t.Fatal("actor missing")
				}
			}
		}
		if count != 1 {
			t.Fatal("idempotent save duplicated audit")
		}
	case *Postgres:
		var count int
		var owner string
		if err := p.pool.QueryRow(ctx, `SELECT count(*),min(actor_user_id::text) FROM hm_audit_events WHERE operation_id=$1 AND action='operation.requested'`, first.ID).Scan(&count, &owner); err != nil || count != 1 || owner != actor {
			t.Fatal("configuration audit mismatch", err)
		}
		for _, sql := range []string{`UPDATE hm_configuration_revisions SET payload=jsonb_set(payload,'{document,backends,0,algorithm}','"first"')`, `DELETE FROM hm_configuration_revisions`, `TRUNCATE hm_configuration_revisions CASCADE`} {
			if _, err = p.pool.Exec(ctx, sql); err == nil {
				t.Fatal("version history was mutable")
			}
		}
	}
}
func TestMemoryConfigurationHistoryAndRollback(t *testing.T) { configurationContract(t, NewMemory()) }
func TestPostgresConfigurationHistoryAndRollback(t *testing.T) {
	configurationContract(t, postgresForTest(t))
}

func observationHistoryBudgetContract(t *testing.T, db Store) {
	t.Helper()
	ctx := t.Context()
	org := domain.NewID()
	cluster := createCluster(t, db, org)
	node, err := db.Enroll(ctx, issueToken(t, db, org, cluster.ID), domain.Node{ID: domain.NewID(), Name: "observer"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	control, configs := db.(ControlStore), db.(ConfigurationStore)
	session := domain.NewID()
	d := configurationDiscovery()
	d.ControlAllowed, d.Configuration.ControlAllowed = false, false
	for i := 0; i < 1030; i++ {
		if i%100 == 0 {
			session = domain.NewID()
			if _, err = control.OpenControl(ctx, org, node.ID, session); err != nil {
				t.Fatal(err)
			}
		}
		d.ObservedAtMS = time.Now().UnixMilli()
		d.Configuration.ObservedAtMS = d.ObservedAtMS
		d.Configuration.Version = int64(i + 1)
		// Raw files can change without changing the reduced document.
		d.Configuration.ArtifactID = fmt.Sprintf("%064x", i+1)
		if _, _, err = control.ReportControl(ctx, org, node.ID, session, &d, nil); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	switch p := db.(type) {
	case *Memory:
		for _, r := range p.revisions {
			if r.NodeID == node.ID && r.Source == "discovered" {
				count++
			}
		}
	case *Postgres:
		err = p.pool.QueryRow(ctx, `SELECT count(*) FROM hm_configuration_revisions WHERE node_id=$1 AND payload->>'source'='discovered'`, node.ID).Scan(&count)
	}
	if err != nil || count != 1024 {
		t.Fatalf("unbounded discovered history: %d %v", count, err)
	}
	state, _, err := configs.GetConfiguration(ctx, org, node.ID)
	if err != nil || state.Discovery.Configuration.ArtifactID != d.Configuration.ArtifactID {
		t.Fatal("latest observation was lost", err)
	}
	raw, _ := json.Marshal(state)
	var metadata map[string]any
	json.Unmarshal(raw, &metadata)
	if metadata["configuration_history_limited"] != true {
		t.Fatal("history limit was hidden from operators")
	}
	// Permissioned edits still capture their actual current base for rollback.
	d.ControlAllowed, d.Configuration.ControlAllowed = true, true
	d.ObservedAtMS = time.Now().UnixMilli()
	d.Configuration.ObservedAtMS = d.ObservedAtMS
	if _, _, err = control.ReportControl(ctx, org, node.ID, session, &d, nil); err != nil {
		t.Fatal(err)
	}
	desired := d.Configuration.Document.Canonical()
	desired.Backends[0].Servers[0].Weight = 200
	op, err := configs.CreateConfiguration(ctx, org, domain.Operation{RequestID: domain.NewID(), NodeID: node.ID, ExpectedGeneration: d.Generation, BaseArtifactID: d.Configuration.ArtifactID, BaseVersion: d.Configuration.Version, Configuration: &desired}, "")
	if err != nil {
		t.Fatal("history limit blocked an authorized edit", err)
	}
	state, revisions, err := configs.GetConfiguration(ctx, org, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	var base, edit domain.ConfigurationRevision
	for _, r := range revisions {
		if r.ID == state.CurrentRevisionID {
			base = r
		}
		if r.ID == op.ConfigurationRevisionID {
			edit = r
		}
	}
	if base.ArtifactID != d.Configuration.ArtifactID || edit.ParentID != base.ID {
		t.Fatal("authorized edit lost its real rollback base")
	}
}

func TestMemoryObservationHistoryBudget(t *testing.T) {
	observationHistoryBudgetContract(t, NewMemory())
}
func TestPostgresObservationHistoryBudget(t *testing.T) {
	observationHistoryBudgetContract(t, postgresForTest(t))
}
