package store

import (
	"context"
	_ "embed"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed connector_schema.sql
var connectorSchemaSQL string

const connectorColumns = `id::text,organization_id::text,name,pair_digest,COALESCE(token_digest,''),created_at,pair_expires_at,claimed_at,revoked_at`

func scanConnector(row pgx.Row) (domain.ConnectorLink, error) {
	var l domain.ConnectorLink
	err := row.Scan(&l.ID, &l.OrganizationID, &l.Name, &l.PairDigest, &l.TokenDigest, &l.CreatedAt, &l.PairExpiresAt, &l.ClaimedAt, &l.RevokedAt)
	return l, normalizeError(err)
}

func connectorAudit(ctx context.Context, tx pgx.Tx, org, id, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO hm_connector_audit(organization_id,link_id,actor_user_id,action) VALUES($1,$2,NULLIF($3,'')::uuid,$4)`, org, id, domain.Actor(ctx), action)
	return err
}

func (p *Postgres) CreateConnector(ctx context.Context, l domain.ConnectorLink) error {
	if !validConnector(l) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		// Tenant lock serializes rate limits, expiry and publication on replicas.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,84921))`, l.OrganizationID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM hm_connector_links WHERE organization_id=$1 AND created_at>clock_timestamp()-interval '1 hour'`, l.OrganizationID).Scan(&count); err != nil {
			return err
		}
		if count >= 10 {
			return domain.ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE hm_connector_links SET revoked_at=clock_timestamp() WHERE organization_id=$1 AND revoked_at IS NULL AND claimed_at IS NULL AND pair_expires_at<=clock_timestamp()`, l.OrganizationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hm_connector_links(id,organization_id,name,pair_digest,created_at,pair_expires_at) VALUES($1,$2,$3,$4,$5,$6)`, l.ID, l.OrganizationID, l.Name, l.PairDigest, l.CreatedAt, l.PairExpiresAt); err != nil {
			return normalizeError(err)
		}
		return connectorAudit(ctx, tx, l.OrganizationID, l.ID, "connector.created")
	})
}
func (p *Postgres) ConnectorForOrganization(ctx context.Context, org string) (domain.ConnectorLink, error) {
	if !domain.ValidID(org) {
		return domain.ConnectorLink{}, domain.ErrInvalidInput
	}
	return scanConnector(p.pool.QueryRow(ctx, `SELECT `+connectorColumns+` FROM hm_connector_links WHERE organization_id=$1 AND revoked_at IS NULL`, org))
}
func (p *Postgres) ClaimConnector(ctx context.Context, id, pair, token string, now time.Time) (domain.ConnectorLink, error) {
	if !domain.ValidID(id) || !domain.ValidDigest(pair) || !domain.ValidDigest(token) {
		return domain.ConnectorLink{}, domain.ErrInvalidInput
	}
	var link domain.ConnectorLink
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		var err error
		link, err = scanConnector(tx.QueryRow(ctx, `UPDATE hm_connector_links SET token_digest=$3,claimed_at=COALESCE(claimed_at,clock_timestamp()) WHERE id=$1 AND pair_digest=$2 AND revoked_at IS NULL AND pair_expires_at>GREATEST($4::timestamptz,clock_timestamp()) AND (token_digest IS NULL OR token_digest=$3) RETURNING `+connectorColumns, id, pair, token, now))
		if err != nil {
			return err
		}
		return connectorAudit(ctx, tx, link.OrganizationID, id, "connector.claimed")
	})
	return link, err
}
func (p *Postgres) AuthenticateConnector(ctx context.Context, id, token string) (domain.ConnectorLink, error) {
	if !domain.ValidID(id) || !domain.ValidDigest(token) {
		return domain.ConnectorLink{}, domain.ErrNotFound
	}
	return scanConnector(p.pool.QueryRow(ctx, `SELECT `+connectorColumns+` FROM hm_connector_links WHERE id=$1 AND token_digest=$2 AND revoked_at IS NULL AND claimed_at IS NOT NULL`, id, token))
}
func (p *Postgres) RevokeConnector(ctx context.Context, org, id string) error {
	if !domain.ValidID(org) || !domain.ValidID(id) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `UPDATE hm_connector_links SET revoked_at=clock_timestamp() WHERE organization_id=$1 AND id=$2 AND revoked_at IS NULL`, org, id)
		if err != nil {
			return err
		}
		if res.RowsAffected() != 1 {
			return domain.ErrNotFound
		}
		return connectorAudit(ctx, tx, org, id, "connector.revoked")
	})
}
func (p *Postgres) RecordConnectorAudit(ctx context.Context, org, id, action string) error {
	if !domain.ValidID(org) || !domain.ValidID(id) || (action != "connector.local_linked" && action != "connector.local_unlinked") {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error { return connectorAudit(ctx, tx, org, id, action) })
}
