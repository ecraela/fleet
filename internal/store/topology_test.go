package store

import (
	"errors"
	"github.com/ecraela/fleet/internal/domain"
	"reflect"
	"testing"
	"time"
)

func topologyContract(t *testing.T, db Store) {
	t.Helper()
	org, foreign := domain.NewID(), domain.NewID()
	cluster, _ := db.CreateCluster(t.Context(), org, "Pair")
	other, _ := db.CreateCluster(t.Context(), foreign, "Foreign")
	sameOrg, _ := db.CreateCluster(t.Context(), org, "Other pair")
	enroll := func(org, cluster string) domain.Node {
		token := digestFor(domain.NewID())
		if err := db.CreateEnrollmentToken(t.Context(), org, cluster, token, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		n, err := db.Enroll(t.Context(), token, domain.Node{ID: domain.NewID(), Name: "proxy"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	node := enroll(org, cluster.ID)
	foreignNode := enroll(foreign, other.ID)
	sameOrgNode := enroll(org, sameOrg.ID)
	topology := domain.Topology{Entrypoint: "http://localhost:18081/", Strategy: "roundrobin", Frontend: "public", Backend: "application:8080", Nodes: []domain.TopologyNode{{NodeID: node.ID, Address: "haproxy-a:8080"}}}
	setter := db.(TopologyStore)
	actor := domain.NewID()
	if err := setter.SetTopology(domain.WithActor(t.Context(), actor), org, cluster.ID, topology); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetCluster(t.Context(), org, cluster.ID)
	if err != nil || !reflect.DeepEqual(got.Topology, &topology) {
		t.Fatalf("stored topology mismatch %+v %v", got, err)
	}
	got.Topology.Nodes[0].Address = "modified"
	again, _ := db.GetCluster(t.Context(), org, cluster.ID)
	if again.Topology.Nodes[0].Address != "haproxy-a:8080" {
		t.Fatal("mutable topology escaped store")
	}
	clusters, _ := db.(InventoryStore).ListClusters(t.Context(), org)
	if len(clusters) != 2 {
		t.Fatal("cluster inventory changed")
	}
	for _, c := range clusters {
		if c.ID == cluster.ID && !reflect.DeepEqual(c.Topology, &topology) {
			t.Fatal("inventory lost topology")
		}
	}
	if err := setter.SetTopology(t.Context(), foreign, cluster.ID, topology); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign topology write succeeded", err)
	}
	for _, invalidNode := range []domain.Node{foreignNode, sameOrgNode, {ID: domain.NewID()}} {
		bad := topology
		bad.Nodes = []domain.TopologyNode{{NodeID: invalidNode.ID, Address: "proxy:8080"}}
		if err := setter.SetTopology(t.Context(), org, cluster.ID, bad); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("cross-cluster binding succeeded", err)
		}
	}
	for _, entry := range []string{"javascript:alert(1)", "https://user:password@host/", "http://localhost/?token=private"} {
		bad := topology
		bad.Entrypoint = entry
		if err := setter.SetTopology(t.Context(), org, cluster.ID, bad); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatal("invalid entrypoint accepted", err)
		}
	}
	bad := topology
	bad.Nodes = append(append([]domain.TopologyNode{}, topology.Nodes...), topology.Nodes[0])
	if err := setter.SetTopology(t.Context(), org, cluster.ID, bad); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal("duplicate node accepted", err)
	}
	_ = db.RevokeNode(t.Context(), org, node.ID)
	if err := setter.SetTopology(t.Context(), org, cluster.ID, topology); !errors.Is(err, domain.ErrRevoked) {
		t.Fatal("revoked node accepted", err)
	}
	switch store := db.(type) {
	case *Memory:
		count := 0
		for _, e := range store.audit {
			if e.action == "topology.updated" {
				count++
				if e.actorUserID != actor {
					t.Fatal("missing actor")
				}
			}
		}
		if count != 1 {
			t.Fatal("failed write audited")
		}
	case *Postgres:
		var count int
		var storedActor string
		if err := store.pool.QueryRow(t.Context(), `SELECT count(*), min(actor_user_id::text) FROM hm_audit_events WHERE cluster_id=$1 AND action='topology.updated'`, cluster.ID).Scan(&count, &storedActor); err != nil || count != 1 || storedActor != actor {
			t.Fatal("topology audit mismatch", err)
		}
	}
}
func TestMemoryTopology(t *testing.T)   { topologyContract(t, NewMemory()) }
func TestPostgresTopology(t *testing.T) { topologyContract(t, postgresForTest(t)) }
