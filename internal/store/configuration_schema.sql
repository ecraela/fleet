ALTER TABLE hm_node_controls ADD COLUMN current_revision_id uuid;
ALTER TABLE hm_runtime_operations DROP CONSTRAINT hm_runtime_operations_check;
ALTER TABLE hm_runtime_operations ADD CONSTRAINT hm_runtime_operations_payload_check CHECK (octet_length(payload::text)<=65536 AND payload->>'status'=status);
CREATE TABLE hm_configuration_revisions (
 id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 node_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 number bigint NOT NULL CHECK (number>0),
 created_at timestamptz NOT NULL,
 payload jsonb NOT NULL CHECK (octet_length(payload::text)<=32768 AND payload->>'id'=id::text AND payload->>'node_id'=node_id::text AND payload->>'organization_id'=organization_id::text),
 UNIQUE (node_id,number),
 UNIQUE (id,node_id,organization_id),
 FOREIGN KEY (node_id,cluster_id,organization_id) REFERENCES hm_nodes(id,cluster_id,organization_id)
);
ALTER TABLE hm_node_controls ADD CONSTRAINT hm_node_controls_revision_fk FOREIGN KEY (current_revision_id,node_id,organization_id) REFERENCES hm_configuration_revisions(id,node_id,organization_id) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX hm_configuration_revisions_history_idx ON hm_configuration_revisions(organization_id,node_id,number DESC);
CREATE FUNCTION hm_preserve_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'configuration revision history is append-only'; END IF;
 IF NEW.id<>OLD.id OR NEW.organization_id<>OLD.organization_id OR NEW.cluster_id<>OLD.cluster_id OR NEW.node_id<>OLD.node_id OR NEW.number<>OLD.number OR NEW.created_at<>OLD.created_at
 OR (NEW.payload - ARRAY['status','error_code','completed_at','artifact_id']) IS DISTINCT FROM (OLD.payload - ARRAY['status','error_code','completed_at','artifact_id'])
 OR (COALESCE(OLD.payload->>'artifact_id','')<>'' AND NEW.payload->>'artifact_id' IS DISTINCT FROM OLD.payload->>'artifact_id')
 THEN RAISE EXCEPTION 'configuration revision content is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hm_configuration_revision_immutable BEFORE UPDATE OR DELETE ON hm_configuration_revisions FOR EACH ROW EXECUTE FUNCTION hm_preserve_configuration_revision();
CREATE TRIGGER hm_configuration_revision_no_truncate BEFORE TRUNCATE ON hm_configuration_revisions FOR EACH STATEMENT EXECUTE FUNCTION hm_preserve_configuration_revision();
ALTER TABLE hm_audit_events DROP CONSTRAINT hm_audit_events_action_check;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_events_action_check CHECK (action IN (
 'cluster.created','enrollment.issued','enrollment.revoked','node.enrolled','node.revoked','topology.updated',
 'control.adopted','control.disabled','operation.requested','operation.dispatched','operation.succeeded','operation.failed','operation.unknown','configuration.discovered'
));
