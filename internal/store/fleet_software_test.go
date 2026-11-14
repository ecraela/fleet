package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
)

func fleetSoftwareContract(t *testing.T, db Store) {
	t.Helper()
	ctx := t.Context()
	org, foreign := domain.NewID(), domain.NewID()
	cluster := createCluster(t, db, org)
	node, err := db.Enroll(ctx, issueToken(t, db, org, cluster.ID), domain.Node{ID: domain.NewID(), Name: strings.Repeat("n", 255)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fleet := db.(FleetSoftwareStore)
	request := FleetSoftwareRequest{RequestID: domain.NewID(), TargetVersion: "v1.2.0",
		ManifestSHA256: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64),
		Steps: []software.FleetStep{
			{Role: "manager", Name: "Manager and ingest", ExpectedVersion: "v1.1.0", RequestID: domain.NewID()},
			{Role: "agent", Name: node.Name, NodeID: node.ID, ClusterID: cluster.ID, ExpectedVersion: "v1.1.0", RequestID: domain.NewID()},
		}}
	foreignCluster := createCluster(t, db, foreign)
	foreignNode, err := db.Enroll(ctx, issueToken(t, db, foreign, foreignCluster.ID), domain.Node{ID: domain.NewID(), Name: "foreign"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	invalid := request
	invalid.RequestID = domain.NewID()
	invalid.Steps = append([]software.FleetStep(nil), request.Steps...)
	invalid.Steps[1].NodeID, invalid.Steps[1].ClusterID = foreignNode.ID, foreignCluster.ID
	if _, err := fleet.CreateFleetUpdate(ctx, org, invalid); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign node accepted into installation", err)
	}
	root, err := fleet.CreateFleetUpdate(ctx, org, request)
	if err != nil || root.Status != "queued" || len(root.Steps) != 2 {
		t.Fatal("create fleet", err)
	}
	if duplicate, err := fleet.CreateFleetUpdate(ctx, org, request); err != nil || duplicate.ID != root.ID {
		t.Fatal("idempotency", err)
	}
	if got, err := fleet.FleetUpdateByRequest(ctx, org, request.RequestID); err != nil || got.ID != root.ID {
		t.Fatal("request lookup", err)
	}
	if _, err := fleet.FleetUpdateByRequest(ctx, foreign, request.RequestID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign lookup", err)
	}
	if got, err := fleet.ListFleetUpdates(ctx, foreign); err != nil || len(got) != 0 {
		t.Fatal("foreign history", err)
	}
	changed := request
	changed.TargetVersion = "v1.3.0"
	if _, err := fleet.CreateFleetUpdate(ctx, org, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed request accepted", err)
	}
	if _, err := fleet.CreateFleetUpdate(ctx, org, FleetSoftwareRequest{RequestID: domain.NewID(), TargetVersion: request.TargetVersion,
		ManifestSHA256: request.ManifestSHA256, Fingerprint: request.Fingerprint, Steps: request.Steps}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("parallel fleet accepted", err)
	}
	if _, err := db.(InstallationSoftwareStore).CreateInstallationUpdate(ctx, org, installationRequest()); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("uncoordinated manager update", err)
	}
	if _, err := db.(SoftwareStore).CreateSoftwareUpdate(ctx, org, node.ID, softwareRequest()); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("uncoordinated agent update", err)
	}
	managerRequest := installationRequest()
	managerRequest.RequestID = request.Steps[0].RequestID
	if _, err := db.(InstallationSoftwareStore).CreateInstallationUpdate(FleetStepWriter(ctx, root.ID), org, managerRequest); err != nil {
		t.Fatal("coordinated manager update", err)
	}
	agentRequest := softwareRequest()
	agentRequest.RequestID = request.Steps[1].RequestID
	if _, err := db.(SoftwareStore).CreateSoftwareUpdate(FleetStepWriter(ctx, root.ID), org, node.ID, agentRequest); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("out-of-order child accepted", err)
	}
	if _, err := fleet.AdvanceFleetUpdate(ctx, foreign, root.ID, 0, 1, "running", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign checkpoint", err)
	}
	if _, err := fleet.AdvanceFleetUpdate(ctx, org, root.ID, 0, 1, "running", ""); err != nil {
		t.Fatal("checkpoint", err)
	}
	if _, err := fleet.AdvanceFleetUpdate(ctx, org, root.ID, 0, 1, "running", ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale checkpoint", err)
	}
	if _, err := fleet.AdvanceFleetUpdate(ctx, org, root.ID, 1, 1, "paused", "agent failed"); err != nil {
		t.Fatal("pause", err)
	}
	if active, err := fleet.ActiveFleetUpdates(ctx); err != nil || len(active) != 0 {
		t.Fatal("paused update active", err)
	}
	if _, err := fleet.AdvanceFleetUpdate(ctx, org, root.ID, 1, 2, "succeeded", ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("paused update advanced", err)
	}
}

func TestMemoryFleetSoftwareContract(t *testing.T)   { fleetSoftwareContract(t, NewMemory()) }
func TestPostgresFleetSoftwareContract(t *testing.T) { fleetSoftwareContract(t, postgresForTest(t)) }

func TestPostgresFleetSoftwareAuditImmutable(t *testing.T) {
	p := postgresForTest(t)
	org := domain.NewID()
	request := FleetSoftwareRequest{RequestID: domain.NewID(), TargetVersion: "v1.2.0",
		ManifestSHA256: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64),
		Steps: []software.FleetStep{{Role: "manager", Name: "Manager and ingest", ExpectedVersion: "v1.1.0", RequestID: domain.NewID()}}}
	if _, err := p.pool.Exec(t.Context(), `ALTER TABLE hm_fleet_update_events ADD CONSTRAINT hm_test_fleet_audit_failure CHECK (status <> 'queued') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateFleetUpdate(t.Context(), org, request); err == nil {
		t.Fatal("root created without audit")
	}
	if _, err := p.FleetUpdateByRequest(t.Context(), org, request.RequestID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("orphan root", err)
	}
	if _, err := p.pool.Exec(t.Context(), `ALTER TABLE hm_fleet_update_events DROP CONSTRAINT hm_test_fleet_audit_failure`); err != nil {
		t.Fatal(err)
	}
	root, err := p.CreateFleetUpdate(t.Context(), org, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(t.Context(), `UPDATE hm_fleet_updates SET payload=jsonb_set(payload,'{target_version}','"v9.9.9"') WHERE id=$1`, root.ID); err == nil {
		t.Fatal("target mutated")
	}
	if _, err := p.pool.Exec(t.Context(), `DELETE FROM hm_fleet_update_events WHERE update_id=$1`, root.ID); err == nil {
		t.Fatal("audit deleted")
	}
}
