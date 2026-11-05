package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewPostgresRejectsMissingDedicatedDatabaseURL(t *testing.T) {
	// These generic PostgreSQL settings must never become a fallback database.
	t.Setenv("PGHOST", "unrelated-postgres.invalid")
	t.Setenv("PGDATABASE", "unrelated-database")
	t.Setenv("PGUSER", "unrelated-user")
	t.Setenv("PGPASSWORD", "synthetic-private-test-password")
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // A regression must not attempt a real database connection.
	for _, databaseURL := range []string{"", " ", "\t\r\n"} {
		db, err := NewPostgres(ctx, databaseURL)
		if db != nil {
			db.Close()
			t.Fatal("missing URL created a database pool")
		}
		if !errors.Is(err, domain.ErrInvalidInput) || !strings.Contains(err.Error(), "dedicated database URL is required") {
			t.Fatalf("missing URL was not rejected before PostgreSQL fallback: %v", err)
		}
		for _, value := range []string{"unrelated-postgres.invalid", "unrelated-database", "unrelated-user", "synthetic-private-test-password"} {
			if strings.Contains(err.Error(), value) {
				t.Fatal("configuration error exposed PostgreSQL environment values")
			}
		}
	}
}

// Each test owns one isolated schema. A supplied database may contain other
// applications or concurrent test runs; no shared table is truncated or dropped.
func postgresForTest(t *testing.T) *Postgres {
	return postgresForTestWithMigration(t, true)
}

func postgresForTestWithMigration(t *testing.T, migrate bool) *Postgres {
	t.Helper()
	databaseURL := os.Getenv("HAPROXY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set HAPROXY_TEST_DATABASE_URL to run real PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "hm_test_" + strings.ReplaceAll(domain.NewID(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		defer admin.Close(cleanupCtx)
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop isolated test schema: %v", err)
		}
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	p := &Postgres{pool: pool}
	t.Cleanup(p.Close)
	if migrate {
		if err := p.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestPostgresContract(t *testing.T) {
	runStoreContract(t, func(t *testing.T) Store { return postgresForTest(t) })
}

func TestPostgresConcurrentMigration(t *testing.T) {
	p := postgresForTestWithMigration(t, false)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := p.Migrate(t.Context()); err != nil {
				t.Errorf("concurrent migration: %v", err)
			}
		})
	}
	wg.Wait()
	var count int
	if err := p.pool.QueryRow(t.Context(), "SELECT count(*) FROM hm_schema_migrations WHERE version=1").Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration count %d: %v", count, err)
	}
}

func TestPostgresEnrollmentAndAuditAreAtomic(t *testing.T) {
	p := postgresForTest(t)
	organizationID := domain.NewID()
	cluster := createCluster(t, p, organizationID)
	digest := issueToken(t, p, organizationID, cluster.ID)
	node := domain.Node{ID: domain.NewID(), Name: "haproxy-1"}
	// Simulate unavailable audit persistence. Neither a consumed token nor an
	// authorized node may survive an audit write failure.
	if _, err := p.pool.Exec(t.Context(), `ALTER TABLE hm_audit_events ADD CONSTRAINT hm_test_audit_failure CHECK (action <> 'node.enrolled') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Enroll(t.Context(), digest, node, time.Now()); err == nil {
		t.Fatal("enrollment succeeded without its audit event")
	}
	if _, err := p.LookupEnrollment(t.Context(), digest, time.Now()); err != nil {
		t.Fatalf("audit failure consumed token: %v", err)
	}
	if _, err := p.AuthenticateNode(t.Context(), node.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("audit failure left authorized node: %v", err)
	}
	if _, err := p.pool.Exec(t.Context(), `ALTER TABLE hm_audit_events DROP CONSTRAINT hm_test_audit_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Enroll(t.Context(), digest, node, time.Now()); err != nil {
		t.Fatal(err)
	}
	var auditCount int
	if err := p.pool.QueryRow(t.Context(), `SELECT count(*) FROM hm_audit_events WHERE organization_id=$1 AND node_id=$2 AND action='node.enrolled'`, organizationID, node.ID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("enrollment audit count %d: %v", auditCount, err)
	}
	if _, err := p.pool.Exec(t.Context(), `UPDATE hm_audit_events SET action='node.revoked' WHERE node_id=$1`, node.ID); err == nil {
		t.Fatal("audit row was mutable")
	}
	if _, err := p.pool.Exec(t.Context(), `DELETE FROM hm_audit_events WHERE node_id=$1`, node.ID); err == nil {
		t.Fatal("audit row was deletable")
	}
	if _, err := p.pool.Exec(t.Context(), `TRUNCATE hm_audit_events`); err == nil {
		t.Fatal("audit history was truncatable")
	}
}

func TestPostgresEnrollmentRechecksExpiryAfterLockWait(t *testing.T) {
	p := postgresForTest(t)
	organizationID := domain.NewID()
	cluster := createCluster(t, p, organizationID)
	digest := digestFor(domain.NewID())
	requestTime := time.Now().UTC()
	expiresAt := requestTime.Add(250 * time.Millisecond)
	if err := p.CreateEnrollmentToken(t.Context(), organizationID, cluster.ID, digest, expiresAt); err != nil {
		t.Fatal(err)
	}
	tx, err := p.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), "SELECT digest FROM hm_enrollment_tokens WHERE digest=$1 FOR UPDATE", digest); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := p.Enroll(t.Context(), digest, domain.Node{ID: domain.NewID(), Name: "waiter"}, requestTime)
		result <- err
	}()
	<-time.After(time.Until(expiresAt) + 20*time.Millisecond)
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, domain.ErrInvalidEnrollment) {
			t.Fatalf("token expired while waiting for lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("enrollment did not finish after releasing lock")
	}
}

func TestPostgresTenantForeignKeys(t *testing.T) {
	p := postgresForTest(t)
	organizationID := domain.NewID()
	cluster := createCluster(t, p, organizationID)
	if _, err := p.pool.Exec(t.Context(), `INSERT INTO hm_nodes (id,organization_id,cluster_id,name,created_at) VALUES ($1,$2,$3,'forged',clock_timestamp())`, domain.NewID(), domain.NewID(), cluster.ID); err == nil {
		t.Fatal("database accepted a node under a different tenant's cluster")
	}
}
