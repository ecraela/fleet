package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func digestFor(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func issueToken(t *testing.T, s Store, organizationID, clusterID string) string {
	t.Helper()
	digest := digestFor(domain.NewID())
	if err := s.CreateEnrollmentToken(t.Context(), organizationID, clusterID, digest, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return digest
}

func createCluster(t *testing.T, s Store, organizationID string) domain.Cluster {
	t.Helper()
	cluster, err := s.CreateCluster(t.Context(), organizationID, "production")
	if err != nil {
		t.Fatal(err)
	}
	return cluster
}

func TestMemoryContract(t *testing.T) {
	runStoreContract(t, func(t *testing.T) Store { return NewMemory() })
}

func runStoreContract(t *testing.T, factory func(*testing.T) Store) {
	t.Helper()
	t.Run("enrollment is single-use under contention", func(t *testing.T) {
		s := factory(t)
		organizationID := domain.NewID()
		cluster := createCluster(t, s, organizationID)
		digest := issueToken(t, s, organizationID, cluster.ID)
		var successes atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 24 {
			wg.Go(func() {
				<-start
				node, err := s.Enroll(t.Context(), digest, domain.Node{ID: domain.NewID(), Name: "haproxy-1"}, time.Now())
				if err == nil {
					successes.Add(1)
					if node.OrganizationID != organizationID || node.ClusterID != cluster.ID {
						t.Error("enrollment was not bound to the token tenant and cluster")
					}
				} else if !errors.Is(err, domain.ErrInvalidEnrollment) {
					t.Errorf("concurrent enrollment: %v", err)
				}
			})
		}
		close(start)
		wg.Wait()
		if successes.Load() != 1 {
			t.Fatalf("got %d successful enrollments; want exactly one", successes.Load())
		}
		nodes, err := s.ListNodes(t.Context(), organizationID)
		if err != nil || len(nodes) != 1 {
			t.Fatalf("persisted nodes: %v, %v", nodes, err)
		}
		if _, err := s.LookupEnrollment(t.Context(), digest, time.Now()); !errors.Is(err, domain.ErrInvalidEnrollment) {
			t.Fatalf("consumed token lookup: %v", err)
		}
	})

	t.Run("tenant boundaries and revocation", func(t *testing.T) {
		s := factory(t)
		organizationID, otherOrganizationID := domain.NewID(), domain.NewID()
		cluster := createCluster(t, s, organizationID)
		digest := issueToken(t, s, organizationID, cluster.ID)
		if _, err := s.GetCluster(t.Context(), otherOrganizationID, cluster.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("foreign cluster read: %v", err)
		}
		if err := s.CreateEnrollmentToken(t.Context(), otherOrganizationID, cluster.ID, digestFor("foreign"), time.Now().Add(time.Hour)); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("foreign cluster token creation: %v", err)
		}
		if err := s.RevokeEnrollmentToken(t.Context(), otherOrganizationID, digest); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("foreign enrollment token revoke: %v", err)
		}
		if _, err := s.Enroll(t.Context(), digest, domain.Node{ID: domain.NewID(), Name: "wrong-tenant", OrganizationID: otherOrganizationID}, time.Now()); !errors.Is(err, domain.ErrInvalidEnrollment) {
			t.Fatalf("caller tenant override: %v", err)
		}
		otherCluster := createCluster(t, s, organizationID)
		if _, err := s.Enroll(t.Context(), digest, domain.Node{ID: domain.NewID(), Name: "wrong-cluster", ClusterID: otherCluster.ID}, time.Now()); !errors.Is(err, domain.ErrInvalidEnrollment) {
			t.Fatalf("caller cluster override: %v", err)
		}
		node, err := s.Enroll(t.Context(), digest, domain.Node{ID: domain.NewID(), Name: "haproxy-1"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetNode(t.Context(), otherOrganizationID, node.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("foreign node read: %v", err)
		}
		if err := s.RevokeNode(t.Context(), otherOrganizationID, node.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("foreign node revoke: %v", err)
		}
		if nodes, err := s.ListNodes(t.Context(), otherOrganizationID); err != nil || len(nodes) != 0 {
			t.Fatalf("foreign list: %v %v", nodes, err)
		}
		if _, err := s.AuthenticateNode(t.Context(), node.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeNode(t.Context(), organizationID, node.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeNode(t.Context(), organizationID, node.ID); err != nil {
			t.Fatalf("idempotent revoke: %v", err)
		}
		if _, err := s.AuthenticateNode(t.Context(), node.ID); !errors.Is(err, domain.ErrRevoked) {
			t.Fatalf("revoked node authentication: %v", err)
		}
		stored, err := s.GetNode(t.Context(), organizationID, node.ID)
		if err != nil || stored.RevokedAt == nil {
			t.Fatalf("revocation state: %v %v", stored, err)
		}
		// A caller cannot mutate Memory's stored revocation time through a pointer.
		*stored.RevokedAt = time.Time{}
		stored, err = s.GetNode(t.Context(), organizationID, node.ID)
		if err != nil || stored.RevokedAt.IsZero() {
			t.Fatal("returned node aliases persisted state")
		}
	})

	t.Run("expiry and enrollment revocation", func(t *testing.T) {
		s := factory(t)
		organizationID := domain.NewID()
		cluster := createCluster(t, s, organizationID)
		digest := digestFor(domain.NewID())
		expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
		if err := s.CreateEnrollmentToken(t.Context(), organizationID, cluster.ID, digest, expiresAt); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LookupEnrollment(t.Context(), digest, expiresAt); !errors.Is(err, domain.ErrInvalidEnrollment) {
			t.Fatalf("token valid at expiry: %v", err)
		}
		if _, err := s.Enroll(t.Context(), digest, domain.Node{ID: domain.NewID(), Name: "late"}, expiresAt); !errors.Is(err, domain.ErrInvalidEnrollment) {
			t.Fatalf("token consumed at expiry: %v", err)
		}
		if err := s.RevokeEnrollmentToken(t.Context(), organizationID, digest); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeEnrollmentToken(t.Context(), organizationID, digest); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LookupEnrollment(t.Context(), digest, time.Now()); !errors.Is(err, domain.ErrInvalidEnrollment) {
			t.Fatalf("revoked token lookup: %v", err)
		}
		if _, err := s.Enroll(t.Context(), digest, domain.Node{ID: domain.NewID(), Name: "revoked"}, time.Now()); !errors.Is(err, domain.ErrInvalidEnrollment) {
			t.Fatalf("revoked token enrollment: %v", err)
		}
		if _, err := s.LookupEnrollment(t.Context(), digestFor("missing"), time.Now()); !errors.Is(err, domain.ErrInvalidEnrollment) {
			t.Fatalf("unknown token: %v", err)
		}
	})

	t.Run("failed node insert leaves token usable", func(t *testing.T) {
		s := factory(t)
		organizationID := domain.NewID()
		cluster := createCluster(t, s, organizationID)
		firstDigest := issueToken(t, s, organizationID, cluster.ID)
		nodeID := domain.NewID()
		if _, err := s.Enroll(t.Context(), firstDigest, domain.Node{ID: nodeID, Name: "first"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		secondDigest := issueToken(t, s, organizationID, cluster.ID)
		if _, err := s.Enroll(t.Context(), secondDigest, domain.Node{ID: nodeID, Name: "duplicate"}, time.Now()); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate ID: %v", err)
		}
		if _, err := s.Enroll(t.Context(), secondDigest, domain.Node{ID: domain.NewID(), Name: "retry"}, time.Now()); err != nil {
			t.Fatalf("token consumed by rejected enrollment: %v", err)
		}
	})

	t.Run("rejects malformed identities and raw secrets", func(t *testing.T) {
		s := factory(t)
		organizationID := domain.NewID()
		cluster := createCluster(t, s, organizationID)
		if _, err := s.CreateCluster(t.Context(), "untrusted-org", "production"); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("malformed organization: %v", err)
		}
		if _, err := s.CreateCluster(t.Context(), organizationID, "  "); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("empty name: %v", err)
		}
		if err := s.CreateEnrollmentToken(t.Context(), organizationID, cluster.ID, "raw-enrollment-secret", time.Now().Add(time.Hour)); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("raw secret persisted: %v", err)
		}
		if _, err := s.Enroll(t.Context(), issueToken(t, s, organizationID, cluster.ID), domain.Node{ID: "client-chosen", Name: "node"}, time.Now()); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("malformed node ID: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := s.ListNodes(ctx, organizationID); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled request: %v", err)
		}
	})
}
