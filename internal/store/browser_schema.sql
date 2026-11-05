CREATE TABLE hm_browser_grants (
    jti uuid PRIMARY KEY,
    expires_at timestamptz NOT NULL
);
CREATE INDEX hm_browser_grants_expiry_idx ON hm_browser_grants (expires_at);

CREATE TABLE hm_browser_sessions (
    digest text PRIMARY KEY CHECK (digest ~ '^[0-9a-f]{64}$'),
    user_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    capabilities text[] NOT NULL CHECK (
        cardinality(capabilities) BETWEEN 1 AND 2
        AND capabilities <@ ARRAY['haproxy.read','haproxy.manage']::text[]
        AND 'haproxy.read' = ANY(capabilities)
    ),
    session_kind text NOT NULL CHECK (session_kind IN ('global','organization')),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '60 seconds')
);
CREATE INDEX hm_browser_sessions_expiry_idx ON hm_browser_sessions (expires_at);

ALTER TABLE hm_audit_events ADD COLUMN IF NOT EXISTS actor_user_id uuid;
