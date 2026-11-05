ALTER TABLE hm_clusters ADD COLUMN topology jsonb
    CHECK (topology IS NULL OR (jsonb_typeof(topology) = 'object' AND octet_length(topology::text) <= 16384));
ALTER TABLE hm_audit_events DROP CONSTRAINT hm_audit_events_action_check;
ALTER TABLE hm_audit_events ADD CONSTRAINT hm_audit_events_action_check
    CHECK (action IN ('cluster.created', 'enrollment.issued', 'enrollment.revoked', 'node.enrolled', 'node.revoked', 'topology.updated'));
