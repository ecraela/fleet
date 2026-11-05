package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/localauth"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func localContract(t *testing.T, db LocalStore) {
	t.Helper()
	ctx := context.Background()
	hash, err := localauth.Hash("synthetic password for local tests")
	if err != nil {
		t.Fatal(err)
	}
	u := domain.LocalUser{ID: domain.NewID(), Username: "local-admin", OrganizationID: domain.NewID(), PasswordHash: hash, Role: "administrator"}
	if err = db.CreateLocalUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateLocalUser(ctx, u); err == nil {
		t.Fatal("duplicate local user")
	}
	v, err := db.GetLocalUser(ctx, u.Username)
	if err != nil || v.OrganizationID != u.OrganizationID {
		t.Fatal("tenant identity lost")
	}
	now := time.Now().UTC()
	s := domain.LocalSession{Digest: strings.Repeat("a", 64), UserID: u.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(12 * time.Hour)}
	if err = db.CreateLocalSession(ctx, s, "stale hash"); err == nil {
		t.Fatal("stale password published a session")
	}
	if err = db.CreateLocalSession(ctx, s, hash); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.GetLocalSession(ctx, s.Digest, now.Add(31*time.Minute)); err == nil {
		t.Fatal("idle session accepted")
	}
	s.Digest = strings.Repeat("b", 64)
	if err = db.CreateLocalSession(ctx, s, hash); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.GetLocalSession(ctx, s.Digest, now.Add(13*time.Hour)); err == nil {
		t.Fatal("absolute expiry accepted")
	}
	s.Digest = strings.Repeat("c", 64)
	if err = db.CreateLocalSession(ctx, s, hash); err != nil {
		t.Fatal(err)
	}
	if err = db.ChangeLocalPassword(ctx, u.ID, "stale hash", hash); err == nil {
		t.Fatal("stale password update")
	}
	if err = db.ChangeLocalPassword(ctx, u.ID, hash, hash); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.GetLocalSession(ctx, s.Digest, now); err == nil {
		t.Fatal("password reset did not revoke")
	}
	if err = db.CreateLocalSession(ctx, s, hash); err != nil {
		t.Fatal(err)
	}
	if err = db.DisableLocalUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.GetLocalSession(ctx, s.Digest, now); err == nil {
		t.Fatal("disabled user retained session")
	}
	if err = db.CreateLocalSession(ctx, s, hash); err == nil {
		t.Fatal("disabled user signed in")
	}
	for i := 0; i < 6; i++ {
		ok, e := db.AdmitLocalLogin(ctx, strings.Repeat("d", 64), strings.Repeat("e", 64), now)
		if e != nil || ok != (i < 5) {
			t.Fatalf("rate limit attempt %d: %v %v", i, ok, e)
		}
	}
	// Requests rejected for one identity must not consume capacity for others.
	for range 150 {
		if ok, err := db.AdmitLocalLogin(ctx, strings.Repeat("d", 64), strings.Repeat("e", 64), now); err != nil || ok {
			t.Fatal("exhausted identity admitted", ok, err)
		}
	}
	if ok, err := db.AdmitLocalLogin(ctx, strings.Repeat("f", 64), strings.Repeat("a", 64), now); err != nil || !ok {
		t.Fatal("rejected caller exhausted another user's login capacity", ok, err)
	}
}
func TestLocalMemoryContract(t *testing.T)   { localContract(t, NewMemory()) }
func TestLocalPostgresContract(t *testing.T) { localContract(t, postgresForTest(t)) }

func localAdmissionCapacityContract(t *testing.T, db LocalStore) {
	t.Helper()
	ctx := t.Context()
	key := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	now := time.Now().UTC().Add(2 * time.Minute)
	for _, attack := range []string{"source", "identity"} {
		for i := 0; i < 150; i++ {
			source, identity := key(fmt.Sprint(attack, "source", i)), key(fmt.Sprint(attack, "identity", i))
			limit := 20
			if attack == "source" {
				source = key("same source")
			} else {
				identity = key("same identity")
				limit = 5
			}
			ok, err := db.AdmitLocalLogin(ctx, source, identity, now)
			if err != nil || ok != (i < limit) {
				t.Fatalf("%s spray %d: %v %v", attack, i, ok, err)
			}
		}
		if ok, err := db.AdmitLocalLogin(ctx, key(attack+" fresh source"), key(attack+" fresh identity"), now); err != nil || !ok {
			t.Fatal("denied spray poisoned shared capacity", err)
		}
		now = now.Add(time.Minute)
	}
	var admitted atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 150; i++ {
		workers.Go(func() {
			ok, err := db.AdmitLocalLogin(ctx, key(fmt.Sprint("global source", i)), key(fmt.Sprint("global identity", i)), now)
			if err != nil {
				t.Error(err)
			} else if ok {
				admitted.Add(1)
			}
		})
	}
	workers.Wait()
	if admitted.Load() != 120 {
		t.Fatalf("global capacity under concurrency: %d", admitted.Load())
	}
	if ok, err := db.AdmitLocalLogin(ctx, key("rollover source"), key("rollover identity"), now.Add(time.Minute)); err != nil || !ok {
		t.Fatal("global capacity did not reset", err)
	}
}

func TestLocalMemoryAdmissionCapacity(t *testing.T) { localAdmissionCapacityContract(t, NewMemory()) }
func TestLocalPostgresAdmissionCapacity(t *testing.T) {
	localAdmissionCapacityContract(t, postgresForTest(t))
}
