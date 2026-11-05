CREATE TABLE hm_node_live (
    node_id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    cluster_id uuid NOT NULL,
    observed_at_ms bigint NOT NULL CHECK (observed_at_ms > 0),
    current_sample jsonb NOT NULL CHECK (jsonb_typeof(current_sample) = 'object' AND octet_length(current_sample::text) <= 131072),
    previous_sample jsonb CHECK (jsonb_typeof(previous_sample) = 'object' AND octet_length(previous_sample::text) <= 131072),
    FOREIGN KEY (node_id, cluster_id, organization_id) REFERENCES hm_nodes (id, cluster_id, organization_id) ON DELETE CASCADE
);
