package store

import (
	"context"
	"testing"

	"github.com/ecraela/fleet/internal/domain"
)

func TestMemoryAuditPreservesBrowserActorAndStandalonePrincipal(t *testing.T) {
	m := NewMemory()
	org, user := domain.NewID(), domain.NewID()
	if _, err := m.CreateCluster(domain.WithActor(t.Context(), user), org, "Browser-created"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateCluster(context.Background(), org, "Operator-created"); err != nil {
		t.Fatal(err)
	}
	if len(m.audit) != 2 || m.audit[0].actorUserID != user || m.audit[1].actorUserID != "" {
		t.Fatal("memory audit did not distinguish verified browser actor from standalone operator")
	}
}

func TestPostgresAuditPreservesBrowserActorAndStandalonePrincipal(t *testing.T) {
	p := postgresForTest(t)
	org, user := domain.NewID(), domain.NewID()
	first, err := p.CreateCluster(domain.WithActor(t.Context(), user), org, "Browser-created")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.CreateCluster(t.Context(), org, "Operator-created")
	if err != nil {
		t.Fatal(err)
	}
	var actor *string
	if err := p.pool.QueryRow(t.Context(), `SELECT actor_user_id::text FROM hm_audit_events WHERE cluster_id=$1`, first.ID).Scan(&actor); err != nil || actor == nil || *actor != user {
		t.Fatalf("browser audit actor missing: %v", err)
	}
	if err := p.pool.QueryRow(t.Context(), `SELECT actor_user_id::text FROM hm_audit_events WHERE cluster_id=$1`, second.ID).Scan(&actor); err != nil || actor != nil {
		t.Fatalf("standalone operator inherited a browser actor: %v", err)
	}
}
