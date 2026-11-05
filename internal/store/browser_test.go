package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func runBrowserStoreContract(t *testing.T, newStore func(*testing.T) BrowserStore) {
	t.Helper()
	t.Run("atomic single use and refresh", func(t *testing.T) {
		db := newStore(t)
		now := time.Now().UTC()
		jti := domain.NewID()
		session := domain.BrowserSession{Digest: strings.Repeat("a", 64), UserID: domain.NewID(), OrganizationID: domain.NewID(), Capabilities: []string{"haproxy.read"}, SessionKind: "global", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
		var winners atomic.Int32
		var wg sync.WaitGroup
		for range 12 {
			wg.Go(func() {
				if err := db.ConsumeBrowserGrant(context.Background(), jti, now.Add(30*time.Second), session, "", now); err == nil {
					winners.Add(1)
				} else if !errors.Is(err, domain.ErrConflict) {
					t.Errorf("unexpected race result: %v", err)
				}
			})
		}
		wg.Wait()
		if winners.Load() != 1 {
			t.Fatalf("single-use grant had %d winners", winners.Load())
		}
		got, err := db.GetBrowserSession(t.Context(), session.Digest, now)
		if err != nil || got.UserID != session.UserID || got.OrganizationID != session.OrganizationID {
			t.Fatal("persisted browser identity changed")
		}
		got.Capabilities[0] = "haproxy.manage"
		again, err := db.GetBrowserSession(t.Context(), session.Digest, now)
		if err != nil || again.Capabilities[0] != "haproxy.read" {
			t.Fatal("caller mutated stored capability")
		}
		next := session
		next.Digest = strings.Repeat("b", 64)
		next.Capabilities = []string{"haproxy.read", "haproxy.manage"}
		if err := db.ConsumeBrowserGrant(t.Context(), domain.NewID(), now.Add(30*time.Second), next, session.Digest, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetBrowserSession(t.Context(), session.Digest, now); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("refresh did not atomically remove old session")
		}
		if _, err := db.GetBrowserSession(t.Context(), next.Digest, now.Add(61*time.Second)); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("persisted session exceeded one-minute validity")
		}
		if err := db.DeleteBrowserSession(t.Context(), next.Digest); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("reject expired or overlong identity", func(t *testing.T) {
		db := newStore(t)
		now := time.Now().UTC()
		session := domain.BrowserSession{Digest: strings.Repeat("c", 64), UserID: domain.NewID(), OrganizationID: domain.NewID(), Capabilities: []string{"haproxy.read"}, SessionKind: "organization", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := db.ConsumeBrowserGrant(t.Context(), domain.NewID(), now.Add(-time.Second), session, "", now); err == nil {
			t.Fatal("expired grant consumed")
		}
		session.ExpiresAt = now.Add(61 * time.Second)
		if err := db.ConsumeBrowserGrant(t.Context(), domain.NewID(), now.Add(30*time.Second), session, "", now); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatal("overlong session accepted")
		}
	})
}

func TestMemoryBrowserStoreContract(t *testing.T) {
	runBrowserStoreContract(t, func(*testing.T) BrowserStore { return NewMemory() })
}
func TestPostgresBrowserStoreContract(t *testing.T) {
	runBrowserStoreContract(t, func(t *testing.T) BrowserStore { return postgresForTest(t) })
}

func TestPostgresBrowserMigrationUpgradesVersionOne(t *testing.T) {
	p := postgresForTestWithMigration(t, false)
	if _, err := p.pool.Exec(t.Context(), schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(t.Context(), `CREATE TABLE hm_schema_migrations (version bigint PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT clock_timestamp()); INSERT INTO hm_schema_migrations(version)VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := p.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := p.pool.QueryRow(t.Context(), `SELECT count(*) FROM hm_schema_migrations WHERE version IN(1,2)`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("migration upgrade failed count=%d err=%v", count, err)
	}
}

func TestPostgresBrowserExpiryAfterWaitingForCleanupLock(t *testing.T) {
	p := postgresForTest(t)
	now := time.Now().UTC()
	expiredJTI := domain.NewID()
	if _, err := p.pool.Exec(t.Context(), `INSERT INTO hm_browser_grants(jti,expires_at)VALUES($1,$2)`, expiredJTI, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	locked, err := p.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback(context.Background())
	if _, err := locked.Exec(t.Context(), `SELECT jti FROM hm_browser_grants WHERE jti=$1 FOR UPDATE`, expiredJTI); err != nil {
		t.Fatal(err)
	}
	session := domain.BrowserSession{Digest: strings.Repeat("d", 64), UserID: domain.NewID(), OrganizationID: domain.NewID(), Capabilities: []string{"haproxy.read"}, SessionKind: "global", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	result := make(chan error, 1)
	go func() {
		result <- p.ConsumeBrowserGrant(t.Context(), domain.NewID(), now.Add(100*time.Millisecond), session, "", now)
	}()
	time.Sleep(200 * time.Millisecond)
	if err := locked.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired waiting grant published a session: %v", err)
	}
	if _, err := p.GetBrowserSession(t.Context(), session.Digest, time.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("expired grant left a persisted session")
	}
}
