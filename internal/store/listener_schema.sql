CREATE TABLE hm_listener_revisions (
 id uuid PRIMARY KEY, organization_id uuid NOT NULL, cluster_id uuid NOT NULL,
 number integer NOT NULL CHECK(number BETWEEN 1 AND 64), request_id uuid NOT NULL,
 request_digest text NOT NULL CHECK(request_digest ~ '^[0-9a-f]{64}$'),
 rollout_id uuid NOT NULL, created_at timestamptz NOT NULL,
 payload jsonb NOT NULL CHECK(octet_length(payload::text)<=16384 AND payload->>'id'=id::text AND payload->>'organization_id'=organization_id::text AND payload->>'cluster_id'=cluster_id::text AND payload->>'rollout_id'=rollout_id::text AND (payload->>'number')::integer=number),
 UNIQUE(organization_id,request_id), UNIQUE(cluster_id,number),
 FOREIGN KEY(cluster_id,organization_id) REFERENCES hm_clusters(id,organization_id),
 FOREIGN KEY(rollout_id,cluster_id,organization_id) REFERENCES hm_rollouts(id,cluster_id,organization_id)
);
CREATE TRIGGER hm_listener_revisions_immutable BEFORE UPDATE OR DELETE ON hm_listener_revisions FOR EACH ROW EXECUTE FUNCTION hm_reject_audit_mutation();
CREATE TRIGGER hm_listener_revisions_no_truncate BEFORE TRUNCATE ON hm_listener_revisions FOR EACH STATEMENT EXECUTE FUNCTION hm_reject_audit_mutation();
ALTER TABLE hm_audit_events DROP CONSTRAINT hm_audit_events_action_check;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_events_action_check CHECK(action IN(
 'listener.deployed','listener.rollback','application.deployed','application.rollback','cluster.created','enrollment.issued','enrollment.revoked','node.enrolled','node.revoked','topology.updated',
 'control.adopted','control.disabled','operation.requested','operation.dispatched','operation.succeeded','operation.failed','operation.unknown','configuration.discovered',
 'rollout.node_withdrawing','rollout.node_draining','rollout.node_maintenance','rollout.node_checking_rejoin','rollout.node_rejoining','rollout.node_completed','rollout.node_attention','rollout.started','rollout.rolling_back','rollout.succeeded','rollout.rolled_back','rollout.rollback_failed',
 'maintenance.requested','maintenance.withdrawing','maintenance.draining','maintenance.maintenance','maintenance.checking_rejoin','maintenance.rejoining','maintenance.attention','maintenance.completed','maintenance.failed'
));
