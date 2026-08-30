-- Goose retirement: legacy history has been adopted into
-- atlas_schema_revisions by the migration runner before this file executes.
-- IF EXISTS keeps fresh databases (which never had Goose) on the same path.
DROP TABLE IF EXISTS public.goose_db_version;
