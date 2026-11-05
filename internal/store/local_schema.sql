CREATE TABLE hm_local_users (
 id uuid PRIMARY KEY,
 username text NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9@._+\-]{3,128}$'),
 organization_id uuid NOT NULL,
 password_hash text NOT NULL,
 role text NOT NULL CHECK (role IN ('administrator','viewer')),
 disabled boolean NOT NULL DEFAULT false
);
CREATE TABLE hm_local_sessions (
 digest text PRIMARY KEY CHECK (digest ~ '^[0-9a-f]{64}$'),
 user_id uuid NOT NULL REFERENCES hm_local_users(id),
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 last_seen_at timestamptz NOT NULL,
 CHECK (expires_at > created_at AND expires_at <= created_at + interval '12 hours')
);
CREATE INDEX hm_local_sessions_user_idx ON hm_local_sessions(user_id);
CREATE INDEX hm_local_sessions_expiry_idx ON hm_local_sessions(expires_at);
CREATE TABLE hm_local_login_limits (
 bucket text PRIMARY KEY,
 window_at timestamptz NOT NULL,
 attempts integer NOT NULL
);
CREATE INDEX hm_local_login_limits_window_idx ON hm_local_login_limits(window_at);
CREATE TABLE hm_local_audit (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES hm_local_users(id),
 organization_id uuid NOT NULL,
 action text NOT NULL CHECK (action IN ('user.created','user.password_changed','user.disabled','session.created','session.deleted')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX hm_local_audit_tenant_idx ON hm_local_audit(organization_id,created_at);
CREATE TRIGGER hm_local_audit_immutable BEFORE UPDATE OR DELETE ON hm_local_audit FOR EACH ROW EXECUTE FUNCTION hm_reject_audit_mutation();
CREATE TRIGGER hm_local_audit_no_truncate BEFORE TRUNCATE ON hm_local_audit FOR EACH STATEMENT EXECUTE FUNCTION hm_reject_audit_mutation();
