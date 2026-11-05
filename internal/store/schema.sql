CREATE TABLE IF NOT EXISTS hm_clusters (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 255 AND btrim(name) <> ''),
    created_at timestamptz NOT NULL,
    UNIQUE (id, organization_id)
);

CREATE TABLE IF NOT EXISTS hm_nodes (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    cluster_id uuid NOT NULL,
    name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 255 AND btrim(name) <> ''),
    created_at timestamptz NOT NULL,
    revoked_at timestamptz,
    UNIQUE (id, cluster_id, organization_id),
    FOREIGN KEY (cluster_id, organization_id) REFERENCES hm_clusters (id, organization_id)
);
CREATE INDEX IF NOT EXISTS hm_nodes_organization_created_idx ON hm_nodes (organization_id, created_at, id);

CREATE TABLE IF NOT EXISTS hm_enrollment_tokens (
    digest text PRIMARY KEY CHECK (digest ~ '^[0-9a-f]{64}$'),
    organization_id uuid NOT NULL,
    cluster_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    consumed_node_id uuid,
    revoked_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK ((consumed_at IS NULL) = (consumed_node_id IS NULL)),
    FOREIGN KEY (cluster_id, organization_id) REFERENCES hm_clusters (id, organization_id),
    FOREIGN KEY (consumed_node_id, cluster_id, organization_id) REFERENCES hm_nodes (id, cluster_id, organization_id)
);

CREATE TABLE IF NOT EXISTS hm_audit_events (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    cluster_id uuid NOT NULL,
    node_id uuid,
    action text NOT NULL CHECK (action IN ('cluster.created', 'enrollment.issued', 'enrollment.revoked', 'node.enrolled', 'node.revoked')),
    created_at timestamptz NOT NULL,
    FOREIGN KEY (cluster_id, organization_id) REFERENCES hm_clusters (id, organization_id),
    FOREIGN KEY (node_id, cluster_id, organization_id) REFERENCES hm_nodes (id, cluster_id, organization_id)
);
CREATE INDEX IF NOT EXISTS hm_audit_organization_created_idx ON hm_audit_events (organization_id, created_at, id);

CREATE OR REPLACE FUNCTION hm_reject_audit_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'HAProxy manager audit history is append-only' USING ERRCODE = '42501';
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS hm_audit_immutable ON hm_audit_events;
CREATE TRIGGER hm_audit_immutable BEFORE UPDATE OR DELETE ON hm_audit_events
    FOR EACH ROW EXECUTE FUNCTION hm_reject_audit_mutation();
DROP TRIGGER IF EXISTS hm_audit_no_truncate ON hm_audit_events;
CREATE TRIGGER hm_audit_no_truncate BEFORE TRUNCATE ON hm_audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION hm_reject_audit_mutation();
