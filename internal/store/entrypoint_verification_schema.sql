ALTER TABLE hm_application_revisions DROP CONSTRAINT hm_application_verification_shape;
ALTER TABLE hm_application_revisions ADD CONSTRAINT hm_application_verification_shape CHECK (
 NOT ((payload->'spec') ? 'verification') OR COALESCE((
  jsonb_typeof(payload->'spec'->'verification')='object' AND
  jsonb_typeof(payload->'spec'->'verification'->'http')='boolean' AND
  jsonb_typeof(payload->'spec'->'verification'->'https')='boolean' AND
  ((payload->'spec'->'verification'->>'http')::boolean OR (payload->'spec'->'verification'->>'https')::boolean) AND
  (NOT ((payload->'spec'->'verification') ? 'entrypoint') OR
   jsonb_typeof(payload->'spec'->'verification'->'entrypoint')='boolean')
 ), false)
);
