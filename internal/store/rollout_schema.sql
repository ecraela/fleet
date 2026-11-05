CREATE TABLE hm_rollouts (
 id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 request_id uuid NOT NULL,
 status text NOT NULL CHECK (status IN ('running','rolling_back','succeeded','rolled_back','rollback_failed')),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 payload jsonb NOT NULL CHECK (octet_length(payload::text)<=65536 AND payload->>'id'=id::text AND payload->>'organization_id'=organization_id::text AND payload->>'cluster_id'=cluster_id::text AND payload->>'status'=status),
 UNIQUE (organization_id,request_id),
 UNIQUE (id,cluster_id,organization_id),
 FOREIGN KEY (cluster_id,organization_id) REFERENCES hm_clusters(id,organization_id)
);
CREATE UNIQUE INDEX hm_rollouts_one_active ON hm_rollouts(organization_id,cluster_id) WHERE status IN ('running','rolling_back');
CREATE INDEX hm_rollouts_work ON hm_rollouts(updated_at) WHERE status IN ('running','rolling_back');
CREATE INDEX hm_rollouts_history ON hm_rollouts(organization_id,cluster_id,created_at DESC);
CREATE FUNCTION hm_preserve_rollout() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'UPDATE' THEN RAISE EXCEPTION 'rollout history is append-only'; END IF;
 IF NEW.id<>OLD.id OR NEW.organization_id<>OLD.organization_id OR NEW.cluster_id<>OLD.cluster_id OR NEW.request_id<>OLD.request_id OR NEW.created_at<>OLD.created_at
 OR (NEW.payload->'document') IS DISTINCT FROM (OLD.payload->'document')
 OR (NEW.payload->'actor_user_id') IS DISTINCT FROM (OLD.payload->'actor_user_id')
 OR (OLD.status IN ('succeeded','rolled_back','rollback_failed') AND NEW.payload IS DISTINCT FROM OLD.payload)
 THEN RAISE EXCEPTION 'rollout identity, desired configuration and completed history are immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hm_rollouts_immutable BEFORE UPDATE OR DELETE ON hm_rollouts FOR EACH ROW EXECUTE FUNCTION hm_preserve_rollout();
CREATE TRIGGER hm_rollouts_no_truncate BEFORE TRUNCATE ON hm_rollouts FOR EACH STATEMENT EXECUTE FUNCTION hm_preserve_rollout();
ALTER TABLE hm_audit_events DROP CONSTRAINT hm_audit_events_action_check;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_events_action_check CHECK (action IN (
 'cluster.created','enrollment.issued','enrollment.revoked','node.enrolled','node.revoked','topology.updated',
 'control.adopted','control.disabled','operation.requested','operation.dispatched','operation.succeeded','operation.failed','operation.unknown','configuration.discovered',
 'rollout.started','rollout.rolling_back','rollout.succeeded','rollout.rolled_back','rollout.rollback_failed'
));
ALTER TABLE hm_audit_events ADD COLUMN rollout_id uuid;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_rollout_fk FOREIGN KEY (rollout_id,cluster_id,organization_id) REFERENCES hm_rollouts(id,cluster_id,organization_id);
