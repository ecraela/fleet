package store

import (
	"errors"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func clusterNodesContract(t *testing.T, db Store) {
	t.Helper()
	org, otherOrg := domain.NewID(), domain.NewID()
	cluster, _ := db.CreateCluster(t.Context(), org, "Pair")
	otherCluster, _ := db.CreateCluster(t.Context(), org, "Other pair")
	foreign, _ := db.CreateCluster(t.Context(), otherOrg, "Foreign")
	enroll := func(org, cluster string) domain.Node {
		digest := digestFor(domain.NewID())
		if err := db.CreateEnrollmentToken(t.Context(), org, cluster, digest, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		node, err := db.Enroll(t.Context(), digest, domain.Node{ID: domain.NewID(), Name: "proxy"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	a, b := enroll(org, cluster.ID), enroll(org, cluster.ID)
	other, elsewhere := enroll(org, otherCluster.ID), enroll(otherOrg, foreign.ID)
	reader := db.(ClusterNodesStore)
	nodes, err := reader.GetClusterNodes(t.Context(), org, cluster.ID, []string{b.ID, a.ID})
	if err != nil || len(nodes) != 2 || nodes[0].ID >= nodes[1].ID {
		t.Fatal("bulk node lookup failed", err)
	}
	for _, bad := range []string{other.ID, elsewhere.ID, domain.NewID()} {
		got, err := reader.GetClusterNodes(t.Context(), org, cluster.ID, []string{a.ID, bad})
		if !errors.Is(err, domain.ErrNotFound) || got != nil {
			t.Fatal("partial or foreign inventory returned", err)
		}
	}
	if _, err := reader.GetClusterNodes(t.Context(), org, cluster.ID, []string{a.ID, a.ID}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal("duplicate selection accepted", err)
	}
	_ = db.RevokeNode(t.Context(), org, a.ID)
	nodes, err = reader.GetClusterNodes(t.Context(), org, cluster.ID, []string{a.ID})
	if err != nil || len(nodes) != 1 || nodes[0].RevokedAt == nil {
		t.Fatal("revocation was not visible to publisher", err)
	}
}
func TestMemoryClusterNodes(t *testing.T)   { clusterNodesContract(t, NewMemory()) }
func TestPostgresClusterNodes(t *testing.T) { clusterNodesContract(t, postgresForTest(t)) }
