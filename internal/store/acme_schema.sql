CREATE TABLE hm_acme_plans (
 id uuid PRIMARY KEY, organization_id uuid NOT NULL, cluster_id uuid NOT NULL,
 enabled boolean NOT NULL, status text NOT NULL, next_run_ms bigint NOT NULL, updated_at timestamptz NOT NULL,
 payload jsonb NOT NULL CHECK(octet_length(payload::text)<=1048576 AND payload->>'id'=id::text AND payload->>'organization_id'=organization_id::text AND payload->>'cluster_id'=cluster_id::text),
 UNIQUE(cluster_id), UNIQUE(id,cluster_id,organization_id),
 FOREIGN KEY(cluster_id,organization_id) REFERENCES hm_clusters(id,organization_id)
);
CREATE INDEX hm_acme_due ON hm_acme_plans(next_run_ms,updated_at) WHERE enabled OR status='deploying';
ALTER TABLE hm_listener_revisions DROP CONSTRAINT hm_listener_revisions_number_check;
ALTER TABLE hm_listener_revisions ADD CONSTRAINT hm_listener_revisions_number_check CHECK(number BETWEEN 1 AND 1000000);
ALTER TABLE hm_audit_events ADD COLUMN acme_plan_id uuid;
ALTER TABLE hm_audit_events ADD FOREIGN KEY(acme_plan_id,cluster_id,organization_id) REFERENCES hm_acme_plans(id,cluster_id,organization_id);
ALTER TABLE hm_audit_events DROP CONSTRAINT hm_audit_events_action_check;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_events_action_check CHECK(action IN(
 'certificate.enabled','certificate.renew_requested','certificate.disabled','certificate.retry_requested','certificate.activated','certificate.activation_failed',
 'listener.deployed','listener.rollback','application.deployed','application.rollback','cluster.created','enrollment.issued','enrollment.revoked','node.enrolled','node.revoked','topology.updated',
 'control.adopted','control.disabled','operation.requested','operation.dispatched','operation.succeeded','operation.failed','operation.unknown','configuration.discovered',
 'rollout.node_withdrawing','rollout.node_draining','rollout.node_maintenance','rollout.node_checking_rejoin','rollout.node_rejoining','rollout.node_completed','rollout.node_attention','rollout.started','rollout.rolling_back','rollout.succeeded','rollout.rolled_back','rollout.rollback_failed',
 'maintenance.requested','maintenance.withdrawing','maintenance.draining','maintenance.maintenance','maintenance.checking_rejoin','maintenance.rejoining','maintenance.attention','maintenance.completed','maintenance.failed'
));
