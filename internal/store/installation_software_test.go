package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/software"
)

func installationRequest() InstallationRequest {
	return InstallationRequest{RequestID: domain.NewID(), Action: "upgrade", TargetVersion: "v1.2.0",
		TargetRevision: strings.Repeat("a", 40), ManagerSHA256: strings.Repeat("b", 64), Platform: "linux/amd64",
		ManifestSHA256: strings.Repeat("c", 64), Publisher: "sigstore:ecraela/fleet", ExpectedVersion: "v1.1.0"}
}

func installationSoftwareContract(t *testing.T, db Store) {
	t.Helper()
	ctx := t.Context()
	org, foreign := domain.NewID(), domain.NewID()
	cluster := createCluster(t, db, org)
	node, err := db.Enroll(ctx, issueToken(t, db, org, cluster.ID), domain.Node{ID: domain.NewID(), Name: "a"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := db.(InstallationSoftwareStore)
	updater, err := s.HeartbeatInstallationUpdater(ctx, org, software.Updater{CurrentVersion: "v1.1.0", IngestVersion: "v1.1.0", Ready: true})
	if err != nil || updater.LastSeen.IsZero() {
		t.Fatal("heartbeat", err)
	}
	if _, err := s.InstallationUpdater(ctx, foreign); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign updater", err)
	}
	r := installationRequest()
	first, err := s.CreateInstallationUpdate(ctx, org, r)
	if err != nil || first.Status != "queued" || first.ManagerSHA256 != r.ManagerSHA256 {
		t.Fatal(first, err)
	}
	duplicate, err := s.CreateInstallationUpdate(ctx, org, r)
	if err != nil || duplicate.ID != first.ID {
		t.Fatal("idempotency", err)
	}
	if got, err := s.InstallationUpdateByRequest(ctx, org, r.RequestID); err != nil || got.ID != first.ID {
		t.Fatal("request lookup", err)
	}
	if _, err := s.InstallationUpdateByRequest(ctx, foreign, r.RequestID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign request", err)
	}
	if _, err := s.ActiveInstallationUpdate(ctx, foreign); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign desired state", err)
	}
	if history, err := s.ListInstallationUpdates(ctx, foreign); err != nil || len(history) != 0 {
		t.Fatal("foreign history", err)
	}
	if _, err := db.(SoftwareStore).CreateSoftwareUpdate(ctx, org, node.ID, softwareRequest()); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("agent change while installation active", err)
	}
	changed := r
	changed.TargetVersion = "v1.2.1"
	if _, err := s.CreateInstallationUpdate(ctx, org, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed idempotency target", err)
	}
	if _, err := s.CreateInstallationUpdate(ctx, org, installationRequest()); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("parallel installation changes", err)
	}
	for _, status := range []string{"preparing", "activating", "verifying", "succeeded"} {
		got, err := s.TransitionInstallationUpdate(ctx, org, first.ID, status, "")
		if err != nil || got.Status != status {
			t.Fatal(status, err)
		}
	}
	if _, err := s.TransitionInstallationUpdate(ctx, org, first.ID, "preparing", ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("completed history changed", err)
	}
	if _, err := s.TransitionInstallationUpdate(ctx, foreign, first.ID, "cancelled", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign transition", err)
	}
	if _, err := db.(SoftwareStore).CreateSoftwareUpdate(ctx, org, node.ID, softwareRequest()); err != nil {
		t.Fatal("agent change after installation completion", err)
	}
	if active, err := s.HasActiveSoftwareUpdates(ctx, org); err != nil || !active {
		t.Fatal("active agent changes", err)
	}
	if _, err := s.CreateInstallationUpdate(ctx, org, installationRequest()); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("installation change while agent active", err)
	}
}

func TestMemoryInstallationSoftwareContract(t *testing.T) {
	installationSoftwareContract(t, NewMemory())
}

func TestPostgresInstallationSoftwareContract(t *testing.T) {
	installationSoftwareContract(t, postgresForTest(t))
}

func TestPostgresInstallationSoftwareAuditImmutable(t *testing.T) {
	p := postgresForTest(t)
	org := domain.NewID()
	if _, err := p.pool.Exec(t.Context(), `ALTER TABLE hm_installation_update_events ADD CONSTRAINT hm_test_installation_audit_failure CHECK (status <> 'queued') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	r := installationRequest()
	if _, err := p.CreateInstallationUpdate(t.Context(), org, r); err == nil {
		t.Fatal("installation created without audit")
	}
	if _, err := p.InstallationUpdateByRequest(t.Context(), org, r.RequestID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("orphan installation")
	}
	if _, err := p.pool.Exec(t.Context(), `ALTER TABLE hm_installation_update_events DROP CONSTRAINT hm_test_installation_audit_failure`); err != nil {
		t.Fatal(err)
	}
	u, err := p.CreateInstallationUpdate(t.Context(), org, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(t.Context(), `UPDATE hm_installation_updates SET payload=jsonb_set(payload,'{target_version}','"v9.9.9"') WHERE id=$1`, u.ID); err == nil {
		t.Fatal("target changed")
	}
	if _, err := p.pool.Exec(t.Context(), `DELETE FROM hm_installation_update_events WHERE update_id=$1`, u.ID); err == nil {
		t.Fatal("audit deleted")
	}
}
