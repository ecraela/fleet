package store

import (
	"errors"
	"github.com/ecraela/fleet/internal/domain"
	"testing"
	"time"
)

func enrollmentStatusContract(t *testing.T, db Store) {
	status := db.(EnrollmentStatusStore)
	org, foreign := domain.NewID(), domain.NewID()
	cluster, err := db.CreateCluster(t.Context(), org, "Guided setup")
	if err != nil {
		t.Fatal(err)
	}
	hash := digestFor("synthetic wizard credential")
	expires := time.Now().Add(time.Minute)
	if err := db.CreateEnrollmentToken(t.Context(), org, cluster.ID, hash, expires); err != nil {
		t.Fatal(err)
	}
	if _, err := status.EnrollmentStatus(t.Context(), foreign, hash, time.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign token lookup: %v", err)
	}
	result, err := status.EnrollmentStatus(t.Context(), org, hash, time.Now())
	if err != nil || result.State != "pending" || result.Node != nil {
		t.Fatalf("pending: %+v %v", result, err)
	}
	result, err = status.EnrollmentStatus(t.Context(), org, hash, expires)
	if err != nil || result.State != "expired" {
		t.Fatalf("expiry: %+v %v", result, err)
	}
	// A different enrollment must never satisfy this setup, even with the same name.
	other := digestFor("different synthetic wizard credential")
	if err := db.CreateEnrollmentToken(t.Context(), org, cluster.ID, other, expires); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Enroll(t.Context(), other, domain.Node{ID: domain.NewID(), Name: "same-name"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, _ = status.EnrollmentStatus(t.Context(), org, hash, time.Now())
	if result.State != "pending" {
		t.Fatal("unrelated enrollment confirmed setup")
	}
	node, err := db.Enroll(t.Context(), hash, domain.Node{ID: domain.NewID(), Name: "same-name"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	result, err = status.EnrollmentStatus(t.Context(), org, hash, expires.Add(time.Minute))
	if err != nil || result.State != "connected" || result.Node == nil || result.Node.ID != node.ID {
		t.Fatalf("exact consumed node after token expiry: %+v %v", result, err)
	}
	if err := db.RevokeNode(t.Context(), org, node.ID); err != nil {
		t.Fatal(err)
	}
	result, _ = status.EnrollmentStatus(t.Context(), org, hash, time.Now())
	if result.Node == nil || result.Node.RevokedAt == nil {
		t.Fatal("node revocation hidden")
	}
	if err := db.RevokeEnrollmentToken(t.Context(), org, hash); err != nil {
		t.Fatal(err)
	}
	result, _ = status.EnrollmentStatus(t.Context(), org, hash, time.Now())
	if result.State != "revoked" || result.Node != nil {
		t.Fatal("token revocation hidden")
	}
}
func TestMemoryEnrollmentStatus(t *testing.T)   { enrollmentStatusContract(t, NewMemory()) }
func TestPostgresEnrollmentStatus(t *testing.T) { enrollmentStatusContract(t, postgresForTest(t)) }
