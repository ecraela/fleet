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

func controlDiscovery() domain.Discovery {
	d := domain.Discovery{Version: "3.4.7", ProcessID: "8", StartedAt: time.Now().Add(-time.Hour).Unix(), ObservedAtMS: time.Now().UnixMilli(), ControlAllowed: true, ConfigFingerprint: strings.Repeat("a", 64), ConfigFiles: 1, Proxies: []domain.Proxy{{Name: "public", Kind: "frontend", Mode: "http"}, {Name: "application", Kind: "backend", Mode: "http", Algorithm: "roundrobin"}}, Servers: []domain.BackendServer{{Backend: "application", Name: "app-a", State: "ready", Weight: 100, InitialWeight: 100, Health: "UP"}, {Backend: "application", Name: "app-b", State: "ready", Weight: 100, InitialWeight: 100, Health: "UP"}}}
	d.SetGeneration()
	return d
}
func controlContract(t *testing.T, db Store) {
	t.Helper()
	ctx := t.Context()
	org := domain.NewID()
	cluster := createCluster(t, db, org)
	node, err := db.Enroll(ctx, issueToken(t, db, org, cluster.ID), domain.Node{ID: domain.NewID(), Name: "proxy"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	control := db.(ControlStore)
	session := domain.NewID()
	d := controlDiscovery()
	d.ApplicationProbes = []domain.ApplicationProbe{{ApplicationID: domain.NewID(), SpecDigest: strings.Repeat("a", 64), Name: "entrypoint-http", Scheme: "http", Path: "/shop", ExpectedStatus: 200, StatusCode: 200, ObservedAtMS: time.Now().UnixMilli()}}
	if _, err = control.OpenControl(ctx, org, node.ID, session); err != nil {
		t.Fatal(err)
	}
	if _, _, err = control.ReportControl(ctx, org, node.ID, session, &d, nil); err != nil {
		t.Fatal(err)
	}
	d.ApplicationProbes[0].StatusCode = 503
	stored, _, err := control.GetControl(ctx, org, node.ID)
	if err != nil || len(stored.Discovery.ApplicationProbes) != 1 || stored.Discovery.ApplicationProbes[0].StatusCode != 200 {
		t.Fatal("probe report not durably isolated", err)
	}
	d.ApplicationProbes[0].StatusCode = 200
	input := domain.Operation{RequestID: domain.NewID(), NodeID: node.ID, Kind: "drain", Backend: "application", Server: "app-a", ExpectedGeneration: d.Generation, ExpectedState: "ready", ExpectedWeight: 100}
	// Local observation-only policy blocks writes; no adoption or approval exists.
	d.ControlAllowed = false
	if _, _, err = control.ReportControl(ctx, org, node.ID, session, &d, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = control.CreateOperation(ctx, org, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("observation-only node accepted write", err)
	}
	d.ControlAllowed = true
	if _, _, err = control.ReportControl(ctx, org, node.ID, session, &d, nil); err != nil {
		t.Fatal(err)
	}
	foreign := domain.NewID()
	if _, _, err = control.GetControl(ctx, foreign, node.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign discovery exposed", err)
	}
	if _, err = control.CreateOperation(ctx, foreign, input); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign operation accepted", err)
	}
	first, err := control.CreateOperation(ctx, org, input)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := control.CreateOperation(ctx, org, input)
	if err != nil || duplicate.ID != first.ID {
		t.Fatal("idempotent request changed identity", err)
	}
	changed := input
	changed.Kind = "restore"
	if _, err = control.CreateOperation(ctx, org, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("request id reused for another command", err)
	}
	_, command, err := control.ReportControl(ctx, org, node.ID, session, &d, nil)
	if err != nil || command == nil || command.ID != first.ID || command.CommandDigest == "" {
		t.Fatal("command was not durably claimed", err)
	}
	if _, again, err := control.ReportControl(ctx, org, node.ID, session, &d, nil); err != nil || again != nil {
		t.Fatal("running command replayed", err)
	}
	next := domain.NewID()
	if _, err = control.OpenControl(ctx, org, node.ID, next); err != nil {
		t.Fatal(err)
	}
	if _, _, err = control.ReportControl(ctx, org, node.ID, session, &d, nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old controller not fenced", err)
	}
	_, ops, err := control.GetControl(ctx, org, node.ID)
	if err != nil || len(ops) != 1 || ops[0].Status != "unknown" {
		t.Fatal("disconnect was treated as known success", err)
	}
	auditCount := func() int {
		t.Helper()
		var count int
		switch p := db.(type) {
		case *Memory:
			for _, event := range p.audit {
				if event.operationID == first.ID {
					count++
				}
			}
		case *Postgres:
			if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM hm_audit_events WHERE organization_id=$1 AND node_id=$2 AND operation_id=$3`, org, node.ID, first.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
		}
		return count
	}
	beforeReplay := auditCount()
	// A previously managed node can later report with observation-only grants.
	// Changing reasons or retrying an unverified success cannot grow history.
	d.ControlAllowed = false
	for i := range 100 {
		result := &domain.OperationResult{OperationID: first.ID, Outcome: "unknown", ErrorCode: "runtime_unavailable", Discovery: &d, CommandDigest: command.CommandDigest}
		if i%2 == 0 {
			result.ErrorCode = "journal_unavailable"
		}
		if i%3 == 0 {
			result.Outcome = "succeeded"
			result.ErrorCode = ""
		}
		if _, _, err = control.ReportControl(ctx, org, node.ID, next, &d, result); err != nil {
			t.Fatal("unknown result replay failed", err)
		}
	}
	if auditCount() != beforeReplay {
		t.Fatal("replayed unknown results appended permanent audit history")
	}
	d.ControlAllowed = true
	d.Servers[0].State = "drain"
	d.ObservedAtMS = time.Now().UnixMilli()
	result := &domain.OperationResult{OperationID: first.ID, Outcome: "succeeded", Discovery: &d, CommandDigest: command.CommandDigest}
	if _, _, err = control.ReportControl(ctx, org, node.ID, next, &d, result); err != nil {
		t.Fatal("reconciliation failed", err)
	}
	_, ops, err = control.GetControl(ctx, org, node.ID)
	if err != nil || ops[0].Status != "succeeded" {
		t.Fatal("verified result lost", err)
	}
	if auditCount() != beforeReplay+1 {
		t.Fatal("verified reconciliation did not append exactly one audit transition")
	}
	// A second drain cannot remove the only remaining normal server.
	input.RequestID = domain.NewID()
	input.Server = "app-b"
	if _, err = control.CreateOperation(ctx, org, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("last serving backend drained", err)
	}
	// A generation change rejects a stale request; fresh discovery permits direct control.
	d.StartedAt++
	d.SetGeneration()
	d.ObservedAtMS = time.Now().UnixMilli()
	if _, _, err = control.ReportControl(ctx, org, node.ID, next, &d, nil); err != nil {
		t.Fatal(err)
	}
	input.RequestID = domain.NewID()
	input.Kind = "restore"
	input.Server = "app-a"
	input.ExpectedState = "drain"
	if _, err = control.CreateOperation(ctx, org, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale generation accepted", err)
	}
	input.ExpectedGeneration = d.Generation
	stale := input
	stale.ExpectedState = "ready"
	if _, err = control.CreateOperation(ctx, org, stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale state accepted", err)
	}
	if _, err = control.CreateOperation(ctx, org, input); err != nil {
		t.Fatal("fresh permission-authorized operation rejected", err)
	}
	if err = db.RevokeNode(ctx, org, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = control.ReportControl(ctx, org, node.ID, next, &d, nil); !errors.Is(err, domain.ErrRevoked) {
		t.Fatal("revoked control stream accepted", err)
	}
}
func TestMemoryControlContract(t *testing.T)   { controlContract(t, NewMemory()) }
func TestPostgresControlContract(t *testing.T) { controlContract(t, postgresForTest(t)) }

func concurrentControlContract(t *testing.T, db Store) {
	t.Helper()
	ctx := context.Background()
	org := domain.NewID()
	cluster := createCluster(t, db, org)
	node, err := db.Enroll(ctx, issueToken(t, db, org, cluster.ID), domain.Node{ID: domain.NewID(), Name: "proxy"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	control := db.(ControlStore)
	session := domain.NewID()
	d := controlDiscovery()
	_, err = control.OpenControl(ctx, org, node.ID, session)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = control.ReportControl(ctx, org, node.ID, session, &d, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	success := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			op, err := control.CreateOperation(ctx, org, domain.Operation{RequestID: domain.NewID(), NodeID: node.ID, Kind: "weight", Backend: "application", Server: "app-a", Weight: 200, ExpectedGeneration: d.Generation, ExpectedState: "ready", ExpectedWeight: 100})
			if err == nil {
				success <- op.ID
			} else if !errors.Is(err, domain.ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	close(success)
	if len(success) != 1 {
		t.Fatalf("accepted %d concurrent mutations", len(success))
	}
}
func TestMemorySerializesRuntimeOperations(t *testing.T) { concurrentControlContract(t, NewMemory()) }
func TestPostgresSerializesRuntimeOperations(t *testing.T) {
	concurrentControlContract(t, postgresForTest(t))
}
