package store

import (
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func inventoryContract(t *testing.T, storage Store) {
	t.Helper()
	inventory := storage.(InventoryStore)
	org, other := domain.NewID(), domain.NewID()
	cluster, err := storage.CreateCluster(t.Context(), org, "Production")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.CreateCluster(t.Context(), other, "Other"); err != nil {
		t.Fatal(err)
	}
	clusters, err := inventory.ListClusters(t.Context(), org)
	if err != nil || len(clusters) != 1 || clusters[0].ID != cluster.ID {
		t.Fatalf("tenant clusters: %+v %v", clusters, err)
	}
	for i := 0; i < 3; i++ {
		token := domain.NewID() + domain.NewID()
		digest := digestFor(token)
		if err := storage.CreateEnrollmentToken(t.Context(), org, cluster.ID, digest, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.Enroll(t.Context(), digest, domain.Node{ID: domain.NewID(), Name: "node"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	page, cursor, err := inventory.ListNodesPage(t.Context(), org, "", 2)
	if err != nil || len(page) != 2 || cursor == "" {
		t.Fatalf("page one: %d %q %v", len(page), cursor, err)
	}
	second, last, err := inventory.ListNodesPage(t.Context(), org, cursor, 2)
	if err != nil || len(second) != 1 || last != "" || second[0].ID <= cursor {
		t.Fatalf("page two: %d %q %v", len(second), last, err)
	}
	foreign, _, err := inventory.ListNodesPage(t.Context(), other, "", 2)
	if err != nil || len(foreign) != 0 {
		t.Fatal("inventory leaked another tenant")
	}
	if _, _, err := inventory.ListNodesPage(t.Context(), org, "bad cursor", 2); err == nil {
		t.Fatal("accepted invalid cursor")
	}
}

func TestMemoryInventory(t *testing.T)   { inventoryContract(t, NewMemory()) }
func TestPostgresInventory(t *testing.T) { inventoryContract(t, postgresForTest(t)) }
