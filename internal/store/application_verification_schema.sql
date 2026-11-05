ALTER TABLE hm_application_revisions ADD CONSTRAINT hm_application_verification_shape CHECK (
 NOT ((payload->'spec') ? 'verification') OR COALESCE((
  jsonb_typeof(payload->'spec'->'verification')='object' AND
  jsonb_typeof(payload->'spec'->'verification'->'http')='boolean' AND
  jsonb_typeof(payload->'spec'->'verification'->'https')='boolean' AND
  ((payload->'spec'->'verification'->>'http')::boolean OR (payload->'spec'->'verification'->>'https')::boolean)
 ), false)
);
CREATE FUNCTION hm_preserve_application_checks() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (SELECT jsonb_agg(jsonb_build_object('node_id',n->'node_id','checks',n->'application_checks','rollback_checks',n->'rollback_application_checks') ORDER BY n->>'node_id') FROM jsonb_array_elements(NEW.payload->'nodes') n)
 IS DISTINCT FROM
 (SELECT jsonb_agg(jsonb_build_object('node_id',n->'node_id','checks',n->'application_checks','rollback_checks',n->'rollback_application_checks') ORDER BY n->>'node_id') FROM jsonb_array_elements(OLD.payload->'nodes') n)
 THEN RAISE EXCEPTION 'rollout application check authority is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hm_rollout_application_checks_immutable BEFORE UPDATE ON hm_rollouts FOR EACH ROW EXECUTE FUNCTION hm_preserve_application_checks();
