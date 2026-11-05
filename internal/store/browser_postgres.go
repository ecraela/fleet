package store

import (
	"context"
	_ "embed"
	"time"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed browser_schema.sql
var browserSchemaSQL string

var _ BrowserStore = (*Postgres)(nil)

func (p *Postgres) ConsumeBrowserGrant(ctx context.Context, jti string, grantExpires time.Time, session domain.BrowserSession, replace string, now time.Time) error {
	if !domain.ValidID(jti) || !validBrowserSession(session) || (replace != "" && !domain.ValidDigest(replace)) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		var current time.Time
		if err := tx.QueryRow(ctx, "SELECT GREATEST($1::timestamptz,clock_timestamp())", now).Scan(&current); err != nil {
			return err
		}
		if !current.Before(grantExpires) || !current.Before(session.ExpiresAt) {
			return domain.ErrNotFound
		}
		// Cleanup is indexed and bounded; a login cannot acquire an unbounded
		// deletion workload. Uniqueness makes the handoff single-use on replicas.
		if _, err := tx.Exec(ctx, `DELETE FROM hm_browser_grants WHERE jti IN (SELECT jti FROM hm_browser_grants WHERE expires_at <= $1 ORDER BY expires_at LIMIT 1000)`, current); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM hm_browser_sessions WHERE digest IN (SELECT digest FROM hm_browser_sessions WHERE expires_at <= $1 ORDER BY expires_at LIMIT 1000)`, current); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hm_browser_grants (jti,expires_at) VALUES($1,$2)`, jti, grantExpires); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hm_browser_sessions (digest,user_id,organization_id,capabilities,session_kind,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, session.Digest, session.UserID, session.OrganizationID, session.Capabilities, session.SessionKind, session.CreatedAt, session.ExpiresAt); err != nil {
			return err
		}
		if replace != "" {
			if _, err := tx.Exec(ctx, `DELETE FROM hm_browser_sessions WHERE digest=$1 AND digest<>$2`, replace, session.Digest); err != nil {
				return err
			}
		}
		// Cleanup or a competing refresh can wait on row locks. Recheck the
		// database clock after those waits, before publishing any new session.
		if err := tx.QueryRow(ctx, "SELECT GREATEST($1::timestamptz,clock_timestamp())", now).Scan(&current); err != nil {
			return err
		}
		if !current.Before(grantExpires) || !current.Before(session.ExpiresAt) {
			return domain.ErrNotFound
		}
		return nil
	})
}

func (p *Postgres) GetBrowserSession(ctx context.Context, hash string, now time.Time) (domain.BrowserSession, error) {
	if !domain.ValidDigest(hash) {
		return domain.BrowserSession{}, domain.ErrNotFound
	}
	var s domain.BrowserSession
	err := p.pool.QueryRow(ctx, `SELECT digest,user_id::text,organization_id::text,capabilities,session_kind,created_at,expires_at FROM hm_browser_sessions WHERE digest=$1 AND expires_at>GREATEST($2::timestamptz,clock_timestamp())`, hash, now).Scan(&s.Digest, &s.UserID, &s.OrganizationID, &s.Capabilities, &s.SessionKind, &s.CreatedAt, &s.ExpiresAt)
	return s, normalizeError(err)
}

func (p *Postgres) DeleteBrowserSession(ctx context.Context, hash string) error {
	if !domain.ValidDigest(hash) {
		return domain.ErrInvalidInput
	}
	_, err := p.pool.Exec(ctx, `DELETE FROM hm_browser_sessions WHERE digest=$1`, hash)
	return normalizeError(err)
}
