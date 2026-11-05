package store

import (
	"context"
	_ "embed"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/localauth"
	"github.com/jackc/pgx/v5"
	"time"
)

//go:embed local_schema.sql
var localSchemaSQL string
var _ LocalStore = (*Postgres)(nil)

func localAudit(ctx context.Context, tx pgx.Tx, u domain.LocalUser, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO hm_local_audit(id,user_id,organization_id,action) VALUES($1,$2,$3,$4)`, domain.NewID(), u.ID, u.OrganizationID, action)
	return err
}
func (p *Postgres) CreateLocalUser(ctx context.Context, u domain.LocalUser) error {
	if !validLocalUser(u) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO hm_local_users(id,username,organization_id,password_hash,role) VALUES($1,$2,$3,$4,$5)`, u.ID, u.Username, u.OrganizationID, u.PasswordHash, u.Role); err != nil {
			return err
		}
		return localAudit(ctx, tx, u, "user.created")
	})
}
func (p *Postgres) GetLocalUser(ctx context.Context, name string) (domain.LocalUser, error) {
	var u domain.LocalUser
	err := p.pool.QueryRow(ctx, `SELECT id::text,username,organization_id::text,password_hash,role,disabled FROM hm_local_users WHERE username=$1`, name).Scan(&u.ID, &u.Username, &u.OrganizationID, &u.PasswordHash, &u.Role, &u.Disabled)
	return u, normalizeError(err)
}
func (p *Postgres) ChangeLocalPassword(ctx context.Context, id, previous, next string) error {
	if !domain.ValidID(id) || !localauth.ValidHash(next) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		var u domain.LocalUser
		err := tx.QueryRow(ctx, `UPDATE hm_local_users SET password_hash=$3 WHERE id=$1 AND password_hash=$2 AND NOT disabled RETURNING id::text,organization_id::text`, id, previous, next).Scan(&u.ID, &u.OrganizationID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM hm_local_sessions WHERE user_id=$1`, id); err != nil {
			return err
		}
		return localAudit(ctx, tx, u, "user.password_changed")
	})
}
func (p *Postgres) DisableLocalUser(ctx context.Context, id string) error {
	if !domain.ValidID(id) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		var u domain.LocalUser
		if err := tx.QueryRow(ctx, `UPDATE hm_local_users SET disabled=true WHERE id=$1 RETURNING id::text,organization_id::text`, id).Scan(&u.ID, &u.OrganizationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM hm_local_sessions WHERE user_id=$1`, id); err != nil {
			return err
		}
		return localAudit(ctx, tx, u, "user.disabled")
	})
}
func (p *Postgres) AdmitLocalLogin(ctx context.Context, source, identity string, now time.Time) (bool, error) {
	if !domain.ValidDigest(source) || !domain.ValidDigest(identity) {
		return false, domain.ErrInvalidInput
	}
	allowed := true
	err := p.transaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM hm_local_login_limits WHERE bucket IN (SELECT bucket FROM hm_local_login_limits WHERE window_at<clock_timestamp()-interval '2 minutes' LIMIT 256)`); err != nil {
			return err
		}
		// Charge shared hash capacity only after the caller's narrower limits pass.
		// Keep this order fixed across replicas, including rejected transactions.
		for _, b := range []struct {
			key string
			max int
		}{{"source:" + source, 20}, {"identity:" + identity, 5}, {"global", 120}} {
			var attempts int
			err := tx.QueryRow(ctx, `INSERT INTO hm_local_login_limits(bucket,window_at,attempts) VALUES($1,date_trunc('minute',GREATEST($2::timestamptz,clock_timestamp())),1) ON CONFLICT(bucket) DO UPDATE SET attempts=CASE WHEN hm_local_login_limits.window_at=EXCLUDED.window_at THEN LEAST(hm_local_login_limits.attempts+1,100000) ELSE 1 END,window_at=EXCLUDED.window_at RETURNING attempts`, b.key, now).Scan(&attempts)
			if err != nil {
				return err
			}
			if attempts > b.max {
				allowed = false
				return nil
			}
		}
		return nil
	})
	return allowed, err
}
func (p *Postgres) CreateLocalSession(ctx context.Context, s domain.LocalSession, hash string) error {
	if !validLocalSession(s) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		var u domain.LocalUser
		// Serialize with credential updates so a concurrent reset cannot publish an old login.
		if err := tx.QueryRow(ctx, `SELECT id::text,organization_id::text FROM hm_local_users WHERE id=$1 AND password_hash=$2 AND NOT disabled FOR UPDATE`, s.UserID, hash).Scan(&u.ID, &u.OrganizationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM hm_local_sessions WHERE digest IN (SELECT digest FROM hm_local_sessions WHERE expires_at<=clock_timestamp() OR last_seen_at<=clock_timestamp()-interval '30 minutes' ORDER BY expires_at LIMIT 256)`); err != nil {
			return err
		}
		// Keep a bounded number of sessions per account, including parallel browsers.
		if _, err := tx.Exec(ctx, `DELETE FROM hm_local_sessions WHERE digest IN (SELECT digest FROM hm_local_sessions WHERE user_id=$1 ORDER BY created_at DESC OFFSET 9)`, u.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hm_local_sessions(digest,user_id,created_at,expires_at,last_seen_at) VALUES($1,$2,$3,$4,$5)`, s.Digest, s.UserID, s.CreatedAt, s.ExpiresAt, s.LastSeenAt); err != nil {
			return err
		}
		return localAudit(ctx, tx, u, "session.created")
	})
}
func (p *Postgres) GetLocalSession(ctx context.Context, hash string, now time.Time) (domain.LocalUser, domain.LocalSession, error) {
	var u domain.LocalUser
	var s domain.LocalSession
	if !domain.ValidDigest(hash) {
		return u, s, domain.ErrNotFound
	}
	err := p.pool.QueryRow(ctx, `WITH live AS (UPDATE hm_local_sessions SET last_seen_at=GREATEST($2::timestamptz,clock_timestamp()) WHERE digest=$1 AND expires_at>GREATEST($2::timestamptz,clock_timestamp()) AND last_seen_at>GREATEST($2::timestamptz,clock_timestamp())-interval '30 minutes' RETURNING *) SELECT u.id::text,u.username,u.organization_id::text,u.password_hash,u.role,u.disabled,s.digest,s.user_id::text,s.created_at,s.expires_at,s.last_seen_at FROM live s JOIN hm_local_users u ON u.id=s.user_id WHERE NOT u.disabled`, hash, now).Scan(&u.ID, &u.Username, &u.OrganizationID, &u.PasswordHash, &u.Role, &u.Disabled, &s.Digest, &s.UserID, &s.CreatedAt, &s.ExpiresAt, &s.LastSeenAt)
	return u, s, normalizeError(err)
}
func (p *Postgres) DeleteLocalSession(ctx context.Context, hash string) error {
	if !domain.ValidDigest(hash) {
		return domain.ErrInvalidInput
	}
	return p.transaction(ctx, func(tx pgx.Tx) error {
		var u domain.LocalUser
		err := tx.QueryRow(ctx, `DELETE FROM hm_local_sessions s USING hm_local_users u WHERE s.digest=$1 AND u.id=s.user_id RETURNING u.id::text,u.organization_id::text`, hash).Scan(&u.ID, &u.OrganizationID)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		return localAudit(ctx, tx, u, "session.deleted")
	})
}
