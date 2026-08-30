-- This file is run only against the disposable Atlas dev database.
SET client_min_messages = warning;

DO $$
DECLARE
    schema_name text;
BEGIN
    FOR schema_name IN
        SELECT nspname
        FROM pg_namespace
        WHERE nspname NOT LIKE 'pg_%'
          AND nspname <> 'information_schema'
          AND nspname <> 'public'
    LOOP
        EXECUTE format('DROP SCHEMA %I CASCADE', schema_name);
    END LOOP;
END
$$;

DROP SCHEMA public CASCADE;
CREATE SCHEMA public AUTHORIZATION pg_database_owner;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO PUBLIC;
