CREATE TABLE hm_software_updates (
 id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 node_id uuid NOT NULL,
 request_id uuid NOT NULL,
 status text NOT NULL CHECK (status IN ('queued','preparing','activating','verifying','needs_attention','succeeded','rolled_back','cancelled','recovered')),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 completed_at timestamptz,
 payload jsonb NOT NULL CHECK (octet_length(payload::text)<=8192 AND payload->>'id'=id::text AND payload->>'organization_id'=organization_id::text AND payload->>'node_id'=node_id::text AND payload->>'status'=status),
 UNIQUE (organization_id,request_id),
 FOREIGN KEY (node_id,cluster_id,organization_id) REFERENCES hm_nodes(id,cluster_id,organization_id)
);
CREATE UNIQUE INDEX hm_software_one_active_cluster ON hm_software_updates (organization_id,cluster_id)
 WHERE status IN ('queued','preparing','activating','verifying','needs_attention');
CREATE INDEX hm_software_node_history ON hm_software_updates (organization_id,node_id,created_at DESC,id DESC);

CREATE TABLE hm_software_updaters (
 node_id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 current_version text NOT NULL,
 previous_version text NOT NULL,
 ready boolean NOT NULL,
 last_seen timestamptz NOT NULL,
 FOREIGN KEY (node_id,cluster_id,organization_id) REFERENCES hm_nodes(id,cluster_id,organization_id)
);

CREATE TABLE hm_software_update_events (
 id uuid PRIMARY KEY,
 update_id uuid NOT NULL REFERENCES hm_software_updates(id),
 organization_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 node_id uuid NOT NULL,
 actor_user_id uuid,
 status text NOT NULL,
 error_code text NOT NULL,
 created_at timestamptz NOT NULL,
 FOREIGN KEY (node_id,cluster_id,organization_id) REFERENCES hm_nodes(id,cluster_id,organization_id)
);
CREATE INDEX hm_software_update_events_history ON hm_software_update_events (organization_id,node_id,created_at DESC,id DESC);
CREATE TRIGGER hm_software_update_events_immutable BEFORE UPDATE OR DELETE ON hm_software_update_events
 FOR EACH ROW EXECUTE FUNCTION hm_reject_audit_mutation();
CREATE TRIGGER hm_software_update_events_no_truncate BEFORE TRUNCATE ON hm_software_update_events
 FOR EACH STATEMENT EXECUTE FUNCTION hm_reject_audit_mutation();

CREATE FUNCTION hm_preserve_software_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'software update history is append-only'; END IF;
 IF NEW.id <> OLD.id OR NEW.organization_id <> OLD.organization_id OR NEW.cluster_id <> OLD.cluster_id
    OR NEW.node_id <> OLD.node_id OR NEW.request_id <> OLD.request_id OR NEW.created_at <> OLD.created_at
    OR (NEW.payload - 'status' - 'error_code' - 'updated_at' - 'completed_at')
       IS DISTINCT FROM (OLD.payload - 'status' - 'error_code' - 'updated_at' - 'completed_at')
    OR (OLD.status IN ('succeeded','rolled_back','cancelled','recovered') AND NEW.payload IS DISTINCT FROM OLD.payload)
 THEN RAISE EXCEPTION 'software update target and completed history are immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hm_software_updates_immutable BEFORE UPDATE OR DELETE ON hm_software_updates
 FOR EACH ROW EXECUTE FUNCTION hm_preserve_software_update();
CREATE TRIGGER hm_software_updates_no_truncate BEFORE TRUNCATE ON hm_software_updates
 FOR EACH STATEMENT EXECUTE FUNCTION hm_preserve_software_update();

CREATE TABLE hm_installation_updates (
 id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 request_id uuid NOT NULL,
 status text NOT NULL CHECK (status IN ('queued','preparing','activating','verifying','needs_attention','succeeded','rolled_back','cancelled','recovered')),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 completed_at timestamptz,
 payload jsonb NOT NULL CHECK (octet_length(payload::text)<=8192 AND payload->>'id'=id::text AND payload->>'organization_id'=organization_id::text AND payload->>'status'=status),
 UNIQUE (organization_id,request_id)
);
CREATE UNIQUE INDEX hm_one_active_installation ON hm_installation_updates ((true))
 WHERE status IN ('queued','preparing','activating','verifying','needs_attention');
CREATE INDEX hm_installation_updates_history ON hm_installation_updates (organization_id,created_at DESC,id DESC);

CREATE TABLE hm_installation_update_events (
 id uuid PRIMARY KEY,
 update_id uuid NOT NULL REFERENCES hm_installation_updates(id),
 organization_id uuid NOT NULL,
 actor_user_id uuid,
 status text NOT NULL,
 error_code text NOT NULL,
 created_at timestamptz NOT NULL
);
CREATE TRIGGER hm_installation_update_events_immutable BEFORE UPDATE OR DELETE ON hm_installation_update_events
 FOR EACH ROW EXECUTE FUNCTION hm_reject_audit_mutation();
CREATE TRIGGER hm_installation_update_events_no_truncate BEFORE TRUNCATE ON hm_installation_update_events
 FOR EACH STATEMENT EXECUTE FUNCTION hm_reject_audit_mutation();

CREATE TABLE hm_installation_updater (
 id boolean PRIMARY KEY DEFAULT true CHECK (id),
 organization_id uuid NOT NULL,
 current_version text NOT NULL,
 ingest_version text NOT NULL,
 previous_version text NOT NULL,
 ready boolean NOT NULL,
 last_seen timestamptz NOT NULL
);

CREATE FUNCTION hm_preserve_installation_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'installation update history is append-only'; END IF;
 IF NEW.id <> OLD.id OR NEW.organization_id <> OLD.organization_id OR NEW.request_id <> OLD.request_id
    OR NEW.created_at <> OLD.created_at
    OR (NEW.payload - 'status' - 'error_code' - 'updated_at' - 'completed_at')
       IS DISTINCT FROM (OLD.payload - 'status' - 'error_code' - 'updated_at' - 'completed_at')
    OR (OLD.status IN ('succeeded','rolled_back','cancelled','recovered') AND NEW.payload IS DISTINCT FROM OLD.payload)
 THEN RAISE EXCEPTION 'installation update target and completed history are immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hm_installation_updates_immutable BEFORE UPDATE OR DELETE ON hm_installation_updates
 FOR EACH ROW EXECUTE FUNCTION hm_preserve_installation_update();
CREATE TRIGGER hm_installation_updates_no_truncate BEFORE TRUNCATE ON hm_installation_updates
 FOR EACH STATEMENT EXECUTE FUNCTION hm_preserve_installation_update();
