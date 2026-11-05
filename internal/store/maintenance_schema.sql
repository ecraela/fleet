CREATE TABLE hm_maintenance (
 id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 node_id uuid NOT NULL,
 controller_cluster_id uuid NOT NULL,
 controller_node_id uuid NOT NULL,
 status text NOT NULL CHECK(status IN ('withdrawing','draining','maintenance','checking_rejoin','rejoining','attention','completed','failed')),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 payload jsonb NOT NULL CHECK(octet_length(payload::text)<=131072 AND payload->>'id'=id::text AND payload->>'organization_id'=organization_id::text AND payload->>'cluster_id'=cluster_id::text AND payload->>'node_id'=node_id::text AND payload->>'controller_cluster_id'=controller_cluster_id::text AND payload->>'controller_node_id'=controller_node_id::text AND payload->>'status'=status),
 UNIQUE(id,cluster_id,organization_id),
 FOREIGN KEY(node_id,cluster_id,organization_id) REFERENCES hm_nodes(id,cluster_id,organization_id),
 FOREIGN KEY(controller_node_id,controller_cluster_id,organization_id) REFERENCES hm_nodes(id,cluster_id,organization_id),
 CHECK(cluster_id<>controller_cluster_id)
);
CREATE UNIQUE INDEX hm_maintenance_cluster_active ON hm_maintenance(organization_id,cluster_id) WHERE status NOT IN ('completed','failed');
CREATE UNIQUE INDEX hm_maintenance_controller_active ON hm_maintenance(organization_id,controller_node_id) WHERE status NOT IN ('completed','failed');
CREATE INDEX hm_maintenance_controller_cluster ON hm_maintenance(organization_id,controller_cluster_id) WHERE status NOT IN ('completed','failed');
CREATE INDEX hm_maintenance_history ON hm_maintenance(organization_id,node_id,created_at DESC);
CREATE INDEX hm_maintenance_work ON hm_maintenance(updated_at) WHERE status NOT IN ('completed','failed');
CREATE TABLE hm_maintenance_requests (
 request_id uuid NOT NULL,
 organization_id uuid NOT NULL,
 maintenance_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 digest text NOT NULL CHECK(length(digest)=64),
 PRIMARY KEY(organization_id,request_id),
 FOREIGN KEY(maintenance_id,cluster_id,organization_id) REFERENCES hm_maintenance(id,cluster_id,organization_id)
);
CREATE FUNCTION hm_preserve_maintenance() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'UPDATE' THEN RAISE EXCEPTION 'maintenance history is append-only'; END IF;
 IF NEW.id<>OLD.id OR NEW.organization_id<>OLD.organization_id OR NEW.cluster_id<>OLD.cluster_id OR NEW.node_id<>OLD.node_id OR NEW.controller_cluster_id<>OLD.controller_cluster_id OR NEW.controller_node_id<>OLD.controller_node_id OR NEW.created_at<>OLD.created_at
 OR (NEW.payload->'baseline') IS DISTINCT FROM (OLD.payload->'baseline')
 OR (NEW.payload->'topology') IS DISTINCT FROM (OLD.payload->'topology')
 OR (NEW.payload->'binding_id') IS DISTINCT FROM (OLD.payload->'binding_id')
 OR (NEW.payload->'backend') IS DISTINCT FROM (OLD.payload->'backend')
 OR (NEW.payload->'server') IS DISTINCT FROM (OLD.payload->'server')
 OR (NEW.payload->'address') IS DISTINCT FROM (OLD.payload->'address')
 OR (NEW.payload->'frontend') IS DISTINCT FROM (OLD.payload->'frontend')
 OR (OLD.status IN ('completed','failed') AND NEW.payload IS DISTINCT FROM OLD.payload)
 THEN RAISE EXCEPTION 'maintenance identity, baseline and completed history are immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hm_maintenance_immutable BEFORE UPDATE OR DELETE ON hm_maintenance FOR EACH ROW EXECUTE FUNCTION hm_preserve_maintenance();
CREATE TRIGGER hm_maintenance_no_truncate BEFORE TRUNCATE ON hm_maintenance FOR EACH STATEMENT EXECUTE FUNCTION hm_preserve_maintenance();
CREATE FUNCTION hm_preserve_maintenance_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'maintenance request identities are immutable'; END $$;
CREATE TRIGGER hm_maintenance_requests_immutable BEFORE UPDATE OR DELETE ON hm_maintenance_requests FOR EACH ROW EXECUTE FUNCTION hm_preserve_maintenance_request();
CREATE TRIGGER hm_maintenance_requests_no_truncate BEFORE TRUNCATE ON hm_maintenance_requests FOR EACH STATEMENT EXECUTE FUNCTION hm_preserve_maintenance_request();
ALTER TABLE hm_audit_events DROP CONSTRAINT hm_audit_events_action_check;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_events_action_check CHECK(action IN(
 'cluster.created','enrollment.issued','enrollment.revoked','node.enrolled','node.revoked','topology.updated',
 'control.adopted','control.disabled','operation.requested','operation.dispatched','operation.succeeded','operation.failed','operation.unknown','configuration.discovered',
 'rollout.started','rollout.rolling_back','rollout.succeeded','rollout.rolled_back','rollout.rollback_failed',
 'maintenance.requested','maintenance.withdrawing','maintenance.draining','maintenance.maintenance','maintenance.checking_rejoin','maintenance.rejoining','maintenance.attention','maintenance.completed','maintenance.failed'
));
ALTER TABLE hm_audit_events ADD COLUMN maintenance_id uuid;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_maintenance_fk FOREIGN KEY(maintenance_id,cluster_id,organization_id) REFERENCES hm_maintenance(id,cluster_id,organization_id);
