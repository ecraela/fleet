CREATE TABLE hm_incident_evaluations (
 cluster_id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 evaluated_at_ms bigint NOT NULL DEFAULT 0 CHECK(evaluated_at_ms >= 0),
 cleaned_at_ms bigint NOT NULL DEFAULT 0 CHECK(cleaned_at_ms >= 0),
 FOREIGN KEY(cluster_id,organization_id) REFERENCES hm_clusters(id,organization_id)
);
CREATE TABLE hm_incidents (
 id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 group_key text NOT NULL CHECK(octet_length(group_key) BETWEEN 1 AND 256),
 status text NOT NULL CHECK(status IN ('pending','active','resolved')),
 updated_at_ms bigint NOT NULL,
 payload jsonb NOT NULL CHECK(octet_length(payload::text)<=8192 AND payload->'incident'->>'id'=id::text AND payload->'incident'->>'organization_id'=organization_id::text AND payload->'incident'->>'cluster_id'=cluster_id::text AND payload->'incident'->>'status'=status),
 UNIQUE(cluster_id,organization_id,group_key),
 UNIQUE(id,cluster_id,organization_id),
 FOREIGN KEY(cluster_id,organization_id) REFERENCES hm_clusters(id,organization_id)
);
CREATE INDEX hm_incidents_current ON hm_incidents(organization_id,cluster_id,status,updated_at_ms DESC);
CREATE TABLE hm_incident_events (
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 incident_id uuid NOT NULL,
 organization_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 at_ms bigint NOT NULL,
 payload jsonb NOT NULL CHECK(octet_length(payload::text)<=8192),
 FOREIGN KEY(incident_id,cluster_id,organization_id) REFERENCES hm_incidents(id,cluster_id,organization_id)
);
CREATE INDEX hm_incident_timeline ON hm_incident_events(organization_id,cluster_id,sequence DESC);
CREATE INDEX hm_incident_resource_timeline ON hm_incident_events(organization_id,cluster_id,incident_id,sequence DESC);
CREATE TRIGGER hm_incident_event_immutable BEFORE UPDATE ON hm_incident_events FOR EACH ROW EXECUTE FUNCTION hm_reject_audit_mutation();

CREATE TABLE hm_notification_destinations (
 id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 config_digest text NOT NULL CHECK(config_digest ~ '^[0-9a-f]{64}$'),
 last_sequence bigint NOT NULL CHECK(last_sequence>=0),
 UNIQUE(id,organization_id)
);
CREATE TABLE hm_incident_deliveries (
 destination_id uuid NOT NULL,
 incident_id uuid NOT NULL,
 organization_id uuid NOT NULL,
 cluster_id uuid NOT NULL,
 sequence bigint NOT NULL,
 sent_sequence bigint NOT NULL DEFAULT 0,
 due_ms bigint NOT NULL,
 last_sent_ms bigint NOT NULL DEFAULT 0,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 8),
 lease_id uuid,
 lease_until_ms bigint NOT NULL DEFAULT 0,
 error_code text NOT NULL DEFAULT '' CHECK(error_code IN ('','delivery_failed')),
 payload jsonb NOT NULL CHECK(octet_length(payload::text)<=8192),
 PRIMARY KEY(destination_id,incident_id),
 FOREIGN KEY(destination_id,organization_id) REFERENCES hm_notification_destinations(id,organization_id),
 FOREIGN KEY(incident_id,cluster_id,organization_id) REFERENCES hm_incidents(id,cluster_id,organization_id)
);
CREATE INDEX hm_incident_deliveries_due ON hm_incident_deliveries(destination_id,due_ms) WHERE sequence>sent_sequence AND attempts<8;
