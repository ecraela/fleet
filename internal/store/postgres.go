package store

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

type Postgres struct {
	pool *pgxpool.Pool
}

var _ Store = (*Postgres)(nil)

// NewPostgres connects to the dedicated HAProxy manager database. Migration is
// explicit so deployments can use a separate migration credential.
func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, fmt.Errorf("dedicated database URL is required: %w", domain.ErrInvalidInput)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("configure database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) Migrate(ctx context.Context) error {
	return p.transaction(ctx, func(tx pgx.Tx) error {
		// All replicas serialize migration discovery and application. The lock is
		// transaction scoped, including when startup or migration fails.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(492584126124)"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS hm_schema_migrations (version bigint PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
			return err
		}
		for _, migration := range []struct {
			version int
			sql     string
		}{{1, schemaSQL}, {2, browserSchemaSQL}, {3, topologySchemaSQL}, {4, controlSchemaSQL}, {5, directControlSchemaSQL}, {6, configurationSchemaSQL}, {7, rolloutSchemaSQL}, {8, maintenanceSchemaSQL}, {9, rolloutMaintenanceSchemaSQL}, {10, applicationSchemaSQL}, {11, listenerSchemaSQL}, {12, acmeSchemaSQL}, {13, applicationLifecycleSchemaSQL}, {14, applicationVerificationSchemaSQL}, {15, entrypointVerificationSchemaSQL}, {16, incidentSchemaSQL}, {17, localSchemaSQL}, {18, connectorSchemaSQL}, {19, `DROP TRIGGER IF EXISTS hm_connector_audit_no_truncate ON hm_connector_audit; CREATE TRIGGER hm_connector_audit_no_truncate BEFORE TRUNCATE ON hm_connector_audit FOR EACH STATEMENT EXECUTE FUNCTION hm_connector_audit_immutable();`}, {20, liveSchemaSQL}, {21, softwareSchemaSQL}} {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM hm_schema_migrations WHERE version=$1)", migration.version).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			if _, err := tx.Exec(ctx, migration.sql); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO hm_schema_migrations (version) VALUES($1)", migration.version); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *Postgres) CreateCluster(ctx context.Context, organizationID, name string) (domain.Cluster, error) {
	if !domain.ValidID(organizationID) || !validName(name) {
		return domain.Cluster{}, domain.ErrInvalidInput
	}
	cluster := domain.Cluster{ID: domain.NewID(), OrganizationID: organizationID, Name: name, CreatedAt: time.Now().UTC()}
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO hm_clusters (id, organization_id, name, created_at) VALUES ($1,$2,$3,$4)`, cluster.ID, cluster.OrganizationID, cluster.Name, cluster.CreatedAt); err != nil {
			return err
		}
		return appendAudit(ctx, tx, organizationID, cluster.ID, "", "cluster.created", cluster.CreatedAt)
	})
	if err != nil {
		return domain.Cluster{}, err
	}
	return cluster, nil
}

func (p *Postgres) GetCluster(ctx context.Context, organizationID, clusterID string) (domain.Cluster, error) {
	if !domain.ValidID(organizationID) || !domain.ValidID(clusterID) {
		return domain.Cluster{}, domain.ErrInvalidInput
	}
	var cluster domain.Cluster
	err := p.pool.QueryRow(ctx, `SELECT id::text,organization_id::text,name,created_at,topology FROM hm_clusters WHERE organization_id=$1 AND id=$2`, organizationID, clusterID).
		Scan(&cluster.ID, &cluster.OrganizationID, &cluster.Name, &cluster.CreatedAt, &cluster.Topology)
	return cluster, normalizeError(err)
}

func (p *Postgres) CreateEnrollmentToken(ctx context.Context, organizationID, clusterID, digest string, expiresAt time.Time) error {
	if !domain.ValidID(organizationID) || !domain.ValidID(clusterID) || !domain.ValidDigest(digest) || !expiresAt.After(time.Now()) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `INSERT INTO hm_enrollment_tokens (digest,organization_id,cluster_id,created_at,expires_at)
            SELECT $3,organization_id,id,clock_timestamp(),$4 FROM hm_clusters WHERE organization_id=$1 AND id=$2`, organizationID, clusterID, digest, expiresAt.UTC())
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return appendAudit(ctx, tx, organizationID, clusterID, "", "enrollment.issued", time.Now().UTC())
	})
}

func (p *Postgres) LookupEnrollment(ctx context.Context, digest string, now time.Time) (domain.Enrollment, error) {
	if !domain.ValidDigest(digest) {
		return domain.Enrollment{}, domain.ErrInvalidEnrollment
	}
	var enrollment domain.Enrollment
	err := p.pool.QueryRow(ctx, `SELECT organization_id::text,cluster_id::text,expires_at FROM hm_enrollment_tokens
        WHERE digest=$1 AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > GREATEST($2::timestamptz,clock_timestamp())`, digest, now).
		Scan(&enrollment.OrganizationID, &enrollment.ClusterID, &enrollment.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Enrollment{}, domain.ErrInvalidEnrollment
	}
	return enrollment, normalizeError(err)
}

func (p *Postgres) RevokeEnrollmentToken(ctx context.Context, organizationID, digest string) error {
	if !domain.ValidID(organizationID) || !domain.ValidDigest(digest) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		var clusterID string
		var revokedAt *time.Time
		if err := tx.QueryRow(ctx, `SELECT cluster_id::text,revoked_at FROM hm_enrollment_tokens WHERE organization_id=$1 AND digest=$2 FOR UPDATE`, organizationID, digest).
			Scan(&clusterID, &revokedAt); err != nil {
			return err
		}
		if revokedAt != nil {
			return nil
		}
		now := time.Now().UTC()
		if _, err := tx.Exec(ctx, `UPDATE hm_enrollment_tokens SET revoked_at=$3 WHERE organization_id=$1 AND digest=$2`, organizationID, digest, now); err != nil {
			return err
		}
		return appendAudit(ctx, tx, organizationID, clusterID, "", "enrollment.revoked", now)
	})
}

func (p *Postgres) Enroll(ctx context.Context, digest string, node domain.Node, now time.Time) (domain.Node, error) {
	if err := validateNode(node); err != nil {
		return domain.Node{}, err
	}
	if !domain.ValidDigest(digest) {
		return domain.Node{}, domain.ErrInvalidEnrollment
	}
	var bound domain.Node
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		var enrollment domain.Enrollment
		var consumedAt, revokedAt *time.Time
		err := tx.QueryRow(ctx, `SELECT organization_id::text,cluster_id::text,expires_at,consumed_at,revoked_at
            FROM hm_enrollment_tokens WHERE digest=$1 FOR UPDATE`, digest).
			Scan(&enrollment.OrganizationID, &enrollment.ClusterID, &enrollment.ExpiresAt, &consumedAt, &revokedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidEnrollment
		}
		if err != nil {
			return err
		}
		var databaseNow time.Time
		if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); err != nil {
			return err
		}
		// Read the clock after acquiring the row lock: a token may have expired
		// while this enrollment waited for a competing transaction.
		if databaseNow.After(now) {
			now = databaseNow
		}
		if consumedAt != nil || revokedAt != nil || !now.Before(enrollment.ExpiresAt) {
			return domain.ErrInvalidEnrollment
		}
		bound, err = bindNode(node, enrollment, now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hm_nodes (id,organization_id,cluster_id,name,created_at) VALUES ($1,$2,$3,$4,$5)`, bound.ID, bound.OrganizationID, bound.ClusterID, bound.Name, bound.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE hm_enrollment_tokens SET consumed_at=$2,consumed_node_id=$3 WHERE digest=$1`, digest, now, bound.ID); err != nil {
			return err
		}
		return appendAudit(ctx, tx, bound.OrganizationID, bound.ClusterID, bound.ID, "node.enrolled", now)
	})
	if err != nil {
		return domain.Node{}, err
	}
	return bound, nil
}

const nodeColumns = "id::text,organization_id::text,cluster_id::text,name,created_at,revoked_at"

func (p *Postgres) GetNode(ctx context.Context, organizationID, nodeID string) (domain.Node, error) {
	if !domain.ValidID(organizationID) || !domain.ValidID(nodeID) {
		return domain.Node{}, domain.ErrInvalidInput
	}
	return scanNode(p.pool.QueryRow(ctx, "SELECT "+nodeColumns+" FROM hm_nodes WHERE organization_id=$1 AND id=$2", organizationID, nodeID))
}

// AuthenticateNode is only for a cryptographically verified machine principal;
// HTTP operator endpoints must use GetNode with their resolved organization.
func (p *Postgres) AuthenticateNode(ctx context.Context, nodeID string) (domain.Node, error) {
	if !domain.ValidID(nodeID) {
		return domain.Node{}, domain.ErrNotFound
	}
	node, err := scanNode(p.pool.QueryRow(ctx, "SELECT "+nodeColumns+" FROM hm_nodes WHERE id=$1", nodeID))
	if err != nil {
		return domain.Node{}, err
	}
	if node.RevokedAt != nil {
		return domain.Node{}, domain.ErrRevoked
	}
	return node, nil
}

func (p *Postgres) ListNodes(ctx context.Context, organizationID string) ([]domain.Node, error) {
	if !domain.ValidID(organizationID) {
		return nil, domain.ErrInvalidInput
	}
	rows, err := p.pool.Query(ctx, "SELECT "+nodeColumns+" FROM hm_nodes WHERE organization_id=$1 ORDER BY created_at,id", organizationID)
	if err != nil {
		return nil, normalizeError(err)
	}
	defer rows.Close()
	nodes := make([]domain.Node, 0)
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, normalizeError(rows.Err())
}

func (p *Postgres) RevokeNode(ctx context.Context, organizationID, nodeID string) error {
	if !domain.ValidID(organizationID) || !domain.ValidID(nodeID) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		var cluster string
		if err := tx.QueryRow(ctx, `SELECT cluster_id::text FROM hm_nodes WHERE organization_id=$1 AND id=$2`, organizationID, nodeID).Scan(&cluster); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM hm_clusters WHERE organization_id=$1 AND id=$2 FOR UPDATE`, organizationID, cluster).Scan(&cluster); err != nil {
			return err
		}
		node, err := scanNode(tx.QueryRow(ctx, "SELECT "+nodeColumns+" FROM hm_nodes WHERE organization_id=$1 AND id=$2 FOR UPDATE", organizationID, nodeID))
		if err != nil {
			return err
		}
		if node.RevokedAt != nil {
			return nil
		}
		now := time.Now().UTC()
		if _, err := tx.Exec(ctx, `UPDATE hm_nodes SET revoked_at=$3 WHERE organization_id=$1 AND id=$2`, organizationID, nodeID, now); err != nil {
			return err
		}
		return appendAudit(ctx, tx, organizationID, node.ClusterID, nodeID, "node.revoked", now)
	})
}

func scanNode(row pgx.Row) (domain.Node, error) {
	var node domain.Node
	err := row.Scan(&node.ID, &node.OrganizationID, &node.ClusterID, &node.Name, &node.CreatedAt, &node.RevokedAt)
	return node, normalizeError(err)
}

func appendAudit(ctx context.Context, tx pgx.Tx, organizationID, clusterID, nodeID, action string, at time.Time) error {
	var node any
	if nodeID != "" {
		node = nodeID
	}
	var actor any
	if id := domain.Actor(ctx); id != "" {
		actor = id
	}
	_, err := tx.Exec(ctx, `INSERT INTO hm_audit_events (id,organization_id,cluster_id,node_id,action,created_at,actor_user_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`, domain.NewID(), organizationID, clusterID, node, action, at, actor)
	return err
}

func (p *Postgres) transaction(ctx context.Context, action func(pgx.Tx) error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return normalizeError(err)
	}
	defer tx.Rollback(context.Background()) // no-op after commit; releases locks on every error path
	if err := action(tx); err != nil {
		return normalizeError(err)
	}
	return normalizeError(tx.Commit(ctx))
}

func normalizeError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return domain.ErrConflict
		case "23503", "23514", "22P02":
			return domain.ErrInvalidInput
		}
	}
	return err
}
