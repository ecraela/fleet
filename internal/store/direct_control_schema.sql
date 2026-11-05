-- HAProxy operations are authorized directly by capabilities and local agent policy.
-- Keep previous audit entries while removing the configuration adoption gate.
ALTER TABLE hm_node_controls DROP COLUMN adopted_generation;
