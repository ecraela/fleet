package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func liveContract(t *testing.T, db Store) {
	t.Helper()
	org := domain.NewID()
	cluster, err := db.CreateCluster(t.Context(), org, "Live")
	if err != nil {
		t.Fatal(err)
	}
	token := digestFor(domain.NewID())
	if err := db.CreateEnrollmentToken(t.Context(), org, cluster.ID, token, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	node, err := db.Enroll(t.Context(), token, domain.Node{ID: domain.NewID(), Name: "proxy"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	live := db.(LiveStore)
	observation := domain.LiveObservation{AtMS: time.Now().UnixMilli(), ScrapeOK: true, Frontends: map[string]domain.LiveFrontend{"public": {Responses: map[string]float64{"2xx": 10}}}}
	if err := live.RecordLive(t.Context(), node, observation); err != nil {
		t.Fatal(err)
	}
	observation.Frontends["public"].Responses["2xx"] = 999
	read := func() domain.LivePair {
		rows, err := live.GetLive(t.Context(), org, cluster.ID, []string{node.ID})
		if err != nil {
			t.Fatal(err)
		}
		return rows[node.ID]
	}
	first := read()
	if first.Current.Frontends["public"].Responses["2xx"] != 10 || first.Previous != nil {
		t.Fatal("input alias or unexpected previous sample")
	}
	first.Current.Frontends["public"].Responses["2xx"] = 888
	if read().Current.Frontends["public"].Responses["2xx"] != 10 {
		t.Fatal("read mutated stored observation")
	}
	if err := live.RecordLive(t.Context(), node, observation); err != nil {
		t.Fatal(err)
	}
	if read().Current.Frontends["public"].Responses["2xx"] != 10 {
		t.Fatal("duplicate replaced an observation")
	}
	var wg sync.WaitGroup
	for i := int64(1); i <= 8; i++ {
		wg.Go(func() {
			sample := domain.LiveObservation{AtMS: observation.AtMS + i, ScrapeOK: true}
			if err := live.RecordLive(t.Context(), node, sample); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	pair := read()
	if pair.Current.AtMS != observation.AtMS+8 || pair.Previous == nil || pair.Previous.AtMS >= pair.Current.AtMS {
		t.Fatal("concurrent samples were reordered")
	}
	foreign := node
	foreign.OrganizationID = domain.NewID()
	if err := live.RecordLive(t.Context(), foreign, observation); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign write accepted", err)
	}
	if _, err := live.GetLive(t.Context(), foreign.OrganizationID, cluster.ID, []string{node.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign read accepted", err)
	}
	if _, err := live.GetLive(t.Context(), org, cluster.ID, []string{node.ID, domain.NewID()}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("partial selection accepted", err)
	}
	if err := db.RevokeNode(t.Context(), org, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := live.RecordLive(t.Context(), node, observation); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("revoked write accepted", err)
	}
	rows, err := live.GetLive(t.Context(), org, cluster.ID, []string{node.ID})
	if err != nil || len(rows) != 0 {
		t.Fatal("revoked observation exposed", err)
	}
}

func TestMemoryLive(t *testing.T)   { liveContract(t, NewMemory()) }
func TestPostgresLive(t *testing.T) { liveContract(t, postgresForTest(t)) }
