CREATE TABLE hm_connector_links (
  id uuid PRIMARY KEY,
  organization_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
  pair_digest text NOT NULL CHECK (pair_digest ~ '^[0-9a-f]{64}$'),
  token_digest text CHECK (token_digest ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL,
  pair_expires_at timestamptz NOT NULL,
  claimed_at timestamptz,
  revoked_at timestamptz,
  CHECK (pair_expires_at > created_at AND pair_expires_at <= created_at + interval '10 minutes'),
  CHECK ((claimed_at IS NULL) = (token_digest IS NULL))
);
CREATE UNIQUE INDEX hm_connector_active_org ON hm_connector_links(organization_id) WHERE revoked_at IS NULL;
CREATE INDEX hm_connector_created ON hm_connector_links(organization_id,created_at);
CREATE TABLE hm_connector_audit (
  sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  organization_id uuid NOT NULL,
  link_id uuid NOT NULL,
  actor_user_id uuid,
  action text NOT NULL CHECK (action IN ('connector.created','connector.claimed','connector.revoked','connector.local_linked','connector.local_unlinked')),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION hm_connector_audit_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'connector audit is append-only'; END $$;
CREATE TRIGGER hm_connector_audit_immutable BEFORE UPDATE OR DELETE ON hm_connector_audit FOR EACH ROW EXECUTE FUNCTION hm_connector_audit_immutable();

CREATE TRIGGER hm_connector_audit_no_truncate BEFORE TRUNCATE ON hm_connector_audit FOR EACH STATEMENT EXECUTE FUNCTION hm_connector_audit_immutable();
