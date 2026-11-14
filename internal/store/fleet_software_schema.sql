CREATE TABLE hm_fleet_updates (
 id uuid PRIMARY KEY,
 organization_id uuid NOT NULL,
 request_id uuid NOT NULL,
 status text NOT NULL CHECK (status IN ('queued','running','paused','succeeded')),
 next_step integer NOT NULL CHECK (next_step>=0 AND next_step<=5001),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 completed_at timestamptz,
 payload jsonb NOT NULL CHECK (octet_length(payload::text)<=4194304 AND payload->>'id'=id::text
   AND payload->>'organization_id'=organization_id::text AND payload->>'status'=status
   AND (payload->>'next_step')::integer=next_step),
 UNIQUE (organization_id,request_id)
);
CREATE UNIQUE INDEX hm_one_active_fleet_update ON hm_fleet_updates ((true))
 WHERE status IN ('queued','running');
CREATE INDEX hm_fleet_updates_history ON hm_fleet_updates (organization_id,created_at DESC,id DESC);

CREATE TABLE hm_fleet_update_events (
 id uuid PRIMARY KEY,
 update_id uuid NOT NULL REFERENCES hm_fleet_updates(id),
 organization_id uuid NOT NULL,
 actor_user_id uuid,
 status text NOT NULL,
 next_step integer NOT NULL,
 reason text NOT NULL,
 created_at timestamptz NOT NULL
);
CREATE INDEX hm_fleet_update_events_history ON hm_fleet_update_events (organization_id,update_id,created_at);
CREATE TRIGGER hm_fleet_update_events_immutable BEFORE UPDATE OR DELETE ON hm_fleet_update_events
 FOR EACH ROW EXECUTE FUNCTION hm_reject_audit_mutation();
CREATE TRIGGER hm_fleet_update_events_no_truncate BEFORE TRUNCATE ON hm_fleet_update_events
 FOR EACH STATEMENT EXECUTE FUNCTION hm_reject_audit_mutation();

CREATE FUNCTION hm_preserve_fleet_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'fleet update history is append-only'; END IF;
 IF NEW.id <> OLD.id OR NEW.organization_id <> OLD.organization_id OR NEW.request_id <> OLD.request_id
    OR NEW.created_at <> OLD.created_at
    OR (NEW.payload - 'status' - 'next_step' - 'reason' - 'updated_at' - 'completed_at')
       IS DISTINCT FROM (OLD.payload - 'status' - 'next_step' - 'reason' - 'updated_at' - 'completed_at')
    OR (OLD.status IN ('paused','succeeded') AND NEW.payload IS DISTINCT FROM OLD.payload)
 THEN RAISE EXCEPTION 'fleet update target and completed history are immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hm_fleet_updates_immutable BEFORE UPDATE OR DELETE ON hm_fleet_updates
 FOR EACH ROW EXECUTE FUNCTION hm_preserve_fleet_update();
CREATE TRIGGER hm_fleet_updates_no_truncate BEFORE TRUNCATE ON hm_fleet_updates
 FOR EACH STATEMENT EXECUTE FUNCTION hm_preserve_fleet_update();
