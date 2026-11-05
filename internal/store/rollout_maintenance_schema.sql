-- A rollout reserves both the traffic cluster and its managed entrypoint.
ALTER TABLE hm_rollouts ADD COLUMN controller_cluster_id uuid;
ALTER TABLE hm_rollouts ADD CONSTRAINT hm_rollout_controller_fk FOREIGN KEY(controller_cluster_id,organization_id) REFERENCES hm_clusters(id,organization_id);
ALTER TABLE hm_rollouts ADD CONSTRAINT hm_rollout_controller_check CHECK(controller_cluster_id IS NULL OR (controller_cluster_id<>cluster_id AND payload->'entrypoint'->>'cluster_id'=controller_cluster_id::text));
CREATE UNIQUE INDEX hm_rollouts_controller_active ON hm_rollouts(organization_id,controller_cluster_id) WHERE status IN ('running','rolling_back') AND controller_cluster_id IS NOT NULL;
ALTER TABLE hm_rollouts DROP CONSTRAINT hm_rollouts_check;
ALTER TABLE hm_rollouts ADD CONSTRAINT hm_rollouts_payload_check CHECK(octet_length(payload::text)<=1048576 AND payload->>'id'=id::text AND payload->>'organization_id'=organization_id::text AND payload->>'cluster_id'=cluster_id::text AND payload->>'status'=status);
CREATE OR REPLACE FUNCTION hm_preserve_rollout() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'UPDATE' THEN RAISE EXCEPTION 'rollout history is append-only'; END IF;
 IF NEW.id<>OLD.id OR NEW.organization_id<>OLD.organization_id OR NEW.cluster_id<>OLD.cluster_id OR NEW.controller_cluster_id IS DISTINCT FROM OLD.controller_cluster_id OR NEW.request_id<>OLD.request_id OR NEW.created_at<>OLD.created_at
 OR (NEW.payload->'document') IS DISTINCT FROM (OLD.payload->'document')
 OR (NEW.payload->'actor_user_id') IS DISTINCT FROM (OLD.payload->'actor_user_id')
 OR (NEW.payload->'drain_timeout_seconds') IS DISTINCT FROM (OLD.payload->'drain_timeout_seconds')
 OR ((NEW.payload->'entrypoint') - 'owned_artifact_id' - 'owned_version') IS DISTINCT FROM ((OLD.payload->'entrypoint') - 'owned_artifact_id' - 'owned_version')
 OR (OLD.status IN ('succeeded','rolled_back','rollback_failed') AND NEW.payload IS DISTINCT FROM OLD.payload)
 THEN RAISE EXCEPTION 'rollout identity, desired configuration and completed history are immutable'; END IF;
 RETURN NEW;
END $$;
ALTER TABLE hm_audit_events DROP CONSTRAINT hm_audit_events_action_check;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_events_action_check CHECK(action IN(
 'cluster.created','enrollment.issued','enrollment.revoked','node.enrolled','node.revoked','topology.updated',
 'control.adopted','control.disabled','operation.requested','operation.dispatched','operation.succeeded','operation.failed','operation.unknown','configuration.discovered',
 'rollout.node_withdrawing','rollout.node_draining','rollout.node_maintenance','rollout.node_checking_rejoin','rollout.node_rejoining','rollout.node_completed','rollout.node_attention','rollout.started','rollout.rolling_back','rollout.succeeded','rollout.rolled_back','rollout.rollback_failed',
 'maintenance.requested','maintenance.withdrawing','maintenance.draining','maintenance.maintenance','maintenance.checking_rejoin','maintenance.rejoining','maintenance.attention','maintenance.completed','maintenance.failed'
));
