package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
)

func softwareRequest() SoftwareRequest {
	return SoftwareRequest{RequestID: domain.NewID(), Action: "upgrade", TargetVersion: "v1.2.0",
		TargetRevision: strings.Repeat("a", 40), AgentSHA256: strings.Repeat("b", 64), Platform: "linux/amd64",
		ManifestSHA256: strings.Repeat("c", 64), Publisher: "sigstore:ecraela/fleet", ExpectedVersion: "v1.1.0"}
}

func softwareContract(t *testing.T, db Store) {
	t.Helper()
	ctx := t.Context()
	org, foreign := domain.NewID(), domain.NewID()
	cluster := createCluster(t, db, org)
	node, err := db.Enroll(ctx, issueToken(t, db, org, cluster.ID), domain.Node{ID: domain.NewID(), Name: "a"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	peer, err := db.Enroll(ctx, issueToken(t, db, org, cluster.ID), domain.Node{ID: domain.NewID(), Name: "b"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := db.(SoftwareStore)
	updater, err := s.HeartbeatSoftwareUpdater(ctx, org, node.ID, software.Updater{CurrentVersion: "v1.1.0", Ready: true})
	if err != nil || updater.LastSeen.IsZero() {
		t.Fatal("heartbeat", err)
	}
	if _, err := s.SoftwareUpdater(ctx, foreign, node.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign updater", err)
	}
	if got, err := s.SoftwareUpdater(ctx, org, node.ID); err != nil || !got.Ready {
		t.Fatal("updater lookup", err)
	}
	r := softwareRequest()
	first, err := s.CreateSoftwareUpdate(ctx, org, node.ID, r)
	if err != nil || first.Status != "queued" || first.AgentSHA256 != r.AgentSHA256 {
		t.Fatal(first, err)
	}
	page, err := db.(SoftwareInventoryStore).SoftwareInventory(ctx, org, []string{node.ID, peer.ID})
	if err != nil || page[node.ID].Updater == nil || page[node.ID].Latest == nil || page[node.ID].Latest.ID != first.ID {
		t.Fatal("software inventory missing state", page, err)
	}
	foreignPage, err := db.(SoftwareInventoryStore).SoftwareInventory(ctx, foreign, []string{node.ID})
	if err != nil || len(foreignPage) != 0 {
		t.Fatal("software inventory leaked another tenant", foreignPage, err)
	}
	duplicate, err := s.CreateSoftwareUpdate(ctx, org, node.ID, r)
	if err != nil || duplicate.ID != first.ID {
		t.Fatal("idempotency", err)
	}
	got, err := s.SoftwareUpdateByRequest(ctx, org, r.RequestID)
	if err != nil || got.ID != first.ID {
		t.Fatal("request lookup", err)
	}
	if _, err := s.SoftwareUpdateByRequest(ctx, foreign, r.RequestID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign request", err)
	}
	if _, err := s.ActiveSoftwareUpdate(ctx, foreign, node.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign desired state", err)
	}
	if _, err := s.ListSoftwareUpdates(ctx, foreign, node.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign history", err)
	}
	changed := r
	changed.TargetVersion = "v1.2.1"
	if _, err := s.CreateSoftwareUpdate(ctx, org, node.ID, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("reused request changed target", err)
	}
	other := softwareRequest()
	if _, err := s.CreateSoftwareUpdate(ctx, org, peer.ID, other); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("parallel cluster activation", err)
	}
	for _, status := range []string{"preparing", "activating", "verifying", "succeeded"} {
		got, err = s.TransitionSoftwareUpdate(ctx, org, node.ID, first.ID, status, "")
		if err != nil || got.Status != status {
			t.Fatal(status, err)
		}
	}
	if got.CompletedAt == nil {
		t.Fatal("no completion timestamp")
	}
	if _, err := s.TransitionSoftwareUpdate(ctx, org, node.ID, first.ID, "preparing", ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("terminal history mutated", err)
	}
	if _, err := s.TransitionSoftwareUpdate(ctx, foreign, node.ID, first.ID, "cancelled", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign transition", err)
	}
	if _, err := s.CreateSoftwareUpdate(ctx, org, peer.ID, other); err != nil {
		t.Fatal("next host after completion", err)
	}
}

func TestMemorySoftwareContract(t *testing.T)   { softwareContract(t, NewMemory()) }
func TestPostgresSoftwareContract(t *testing.T) { softwareContract(t, postgresForTest(t)) }

func TestPostgresSoftwareAuditAtomicAndImmutable(t *testing.T) {
	p := postgresForTest(t)
	org := domain.NewID()
	cluster := createCluster(t, p, org)
	node, err := p.Enroll(t.Context(), issueToken(t, p, org, cluster.ID), domain.Node{ID: domain.NewID(), Name: "a"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(t.Context(), `ALTER TABLE hm_software_update_events ADD CONSTRAINT hm_test_software_audit_failure CHECK (status <> 'queued') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	r := softwareRequest()
	if _, err := p.CreateSoftwareUpdate(t.Context(), org, node.ID, r); err == nil {
		t.Fatal("update created without audit")
	}
	if _, err := p.SoftwareUpdateByRequest(t.Context(), org, r.RequestID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("orphan update")
	}
	if _, err := p.pool.Exec(t.Context(), `ALTER TABLE hm_software_update_events DROP CONSTRAINT hm_test_software_audit_failure`); err != nil {
		t.Fatal(err)
	}
	u, err := p.CreateSoftwareUpdate(t.Context(), org, node.ID, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(t.Context(), `UPDATE hm_software_updates SET payload=jsonb_set(payload,'{target_version}','"v9.9.9"') WHERE id=$1`, u.ID); err == nil {
		t.Fatal("target mutated")
	}
	if _, err := p.pool.Exec(t.Context(), `DELETE FROM hm_software_update_events WHERE update_id=$1`, u.ID); err == nil {
		t.Fatal("audit deleted")
	}
	if _, err := p.pool.Exec(t.Context(), `TRUNCATE hm_software_updates`); err == nil {
		t.Fatal("update truncated")
	}
}
