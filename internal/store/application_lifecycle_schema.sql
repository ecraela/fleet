ALTER TABLE hm_application_revisions ADD CONSTRAINT hm_application_lifecycle_shape CHECK(
 (NOT payload ? 'deleted' OR jsonb_typeof(payload->'deleted')='boolean') AND
 (NOT ((payload->'spec') ? 'disabled') OR jsonb_typeof(payload->'spec'->'disabled')='boolean')
);
ALTER TABLE hm_audit_events DROP CONSTRAINT hm_audit_events_action_check;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_events_action_check CHECK(action IN(
 'application.disabled','application.enabled','application.deleted','application.restored',
 'certificate.enabled','certificate.renew_requested','certificate.disabled','certificate.retry_requested','certificate.activated','certificate.activation_failed',
 'listener.deployed','listener.rollback','application.deployed','application.rollback','cluster.created','enrollment.issued','enrollment.revoked','node.enrolled','node.revoked','topology.updated',
 'control.adopted','control.disabled','operation.requested','operation.dispatched','operation.succeeded','operation.failed','operation.unknown','configuration.discovered',
 'rollout.node_withdrawing','rollout.node_draining','rollout.node_maintenance','rollout.node_checking_rejoin','rollout.node_rejoining','rollout.node_completed','rollout.node_attention','rollout.started','rollout.rolling_back','rollout.succeeded','rollout.rolled_back','rollout.rollback_failed',
 'maintenance.requested','maintenance.withdrawing','maintenance.draining','maintenance.maintenance','maintenance.checking_rejoin','maintenance.rejoining','maintenance.attention','maintenance.completed','maintenance.failed'
));
