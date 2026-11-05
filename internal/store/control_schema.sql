CREATE TABLE hm_node_controls (
 node_id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 session_id uuid,
 connected_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_seen timestamptz NOT NULL DEFAULT '-infinity',
 discovery jsonb CHECK (discovery IS NULL OR octet_length(discovery::text)<=131072),
 adopted_generation text NOT NULL DEFAULT '',
 pending_operation_id uuid,
 FOREIGN KEY (node_id,cluster_id,organization_id) REFERENCES hm_nodes(id,cluster_id,organization_id)
);
CREATE TABLE hm_runtime_operations (
 id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 node_id uuid NOT NULL,
 request_id uuid NOT NULL,
 status text NOT NULL CHECK (status IN ('queued','running','succeeded','failed','unknown')),
 created_at timestamptz NOT NULL,
 payload jsonb NOT NULL CHECK (octet_length(payload::text)<=8192 AND payload->>'status'=status),
 UNIQUE (organization_id,request_id),
 FOREIGN KEY (node_id,cluster_id,organization_id) REFERENCES hm_nodes(id,cluster_id,organization_id)
);
CREATE INDEX hm_runtime_operations_node_idx ON hm_runtime_operations(organization_id,node_id,created_at DESC,id DESC);
ALTER TABLE hm_audit_events ADD COLUMN operation_id uuid REFERENCES hm_runtime_operations(id);
ALTER TABLE hm_audit_events DROP CONSTRAINT hm_audit_events_action_check;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_events_action_check CHECK (action IN (
 'cluster.created','enrollment.issued','enrollment.revoked','node.enrolled','node.revoked','topology.updated',
 'control.adopted','control.disabled','operation.requested','operation.dispatched','operation.succeeded','operation.failed','operation.unknown'
));
