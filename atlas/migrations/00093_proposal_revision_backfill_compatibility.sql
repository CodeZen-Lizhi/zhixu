-- 00082 backfills current_revision_id before replacing the transition guard
-- from 00062. That guard rejects every update without a business transition and
-- version increment. The Atlas runner executes this compatibility block before
-- and after 00082, in the SAME transaction as the unchanged historical file.
-- Its normal version-ordered execution on an upgraded database is a no-op.
-- No business state, version, timestamp or immutable history may be rewritten.
DO $compatibility$
DECLARE
    pointer_exists boolean;
    transition_hash text;
    transition_guard_valid boolean;
    completion_guard_valid boolean;
    upgrade_complete boolean;
BEGIN
    IF pg_catalog.to_regclass('change_control.proposal') IS NULL
       OR pg_catalog.to_regclass('change_control.proposal_revision') IS NULL THEN
        RAISE EXCEPTION 'MIGRATION_PROPOSAL_REVISION_BACKFILL_UNEXPECTED_SCHEMA'
            USING ERRCODE='55000';
    END IF;

    SELECT EXISTS (
        SELECT 1 FROM pg_catalog.pg_attribute
         WHERE attrelid='change_control.proposal'::regclass
           AND attname='current_revision_id' AND NOT attisdropped
    ) INTO pointer_exists;
    SELECT EXISTS (
        SELECT 1 FROM core.schema_meta
         WHERE key='change_control_revision' AND value='proposal-revision/v1'
    ) INTO upgrade_complete;

    IF pointer_exists AND pg_catalog.to_regclass('pg_temp.zhixu_proposal_revision_backfill') IS NULL THEN
        IF NOT upgrade_complete THEN
            RAISE EXCEPTION 'MIGRATION_PROPOSAL_REVISION_BACKFILL_UNEXPECTED_SCHEMA'
                USING ERRCODE='55000';
        END IF;
        -- A normal 00093 apply must not replace or pin later legitimate guards.
        RETURN;
    END IF;

    IF NOT pointer_exists THEN
        -- Do not expose the intermediate guard to other sessions, or allow a
        -- concurrently appended Revision to change the deterministic latest row.
        LOCK TABLE change_control.proposal, change_control.proposal_revision IN ACCESS EXCLUSIVE MODE;
    END IF;

    SELECT pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(prosrc,'UTF8')),'hex')
      INTO transition_hash
      FROM pg_catalog.pg_proc
     WHERE oid=pg_catalog.to_regprocedure('change_control.validate_proposal_transition()')
       AND prolang=(SELECT oid FROM pg_catalog.pg_language WHERE lanname='plpgsql')
       AND prorettype='trigger'::regtype AND NOT prosecdef AND NOT proisstrict
       AND provolatile='v' AND proconfig IS NULL;
    SELECT EXISTS (
        SELECT 1 FROM pg_catalog.pg_trigger
         WHERE tgrelid='change_control.proposal'::regclass
           AND tgname='proposal_validate_transition'
           AND tgfoid=pg_catalog.to_regprocedure('change_control.validate_proposal_transition()')
           AND tgtype=23 AND tgenabled='O' AND NOT tgisinternal
           AND tgnargs=0 AND tgqual IS NULL AND tgattr=''::int2vector
    ) INTO transition_guard_valid;
    SELECT EXISTS (
        SELECT 1 FROM pg_catalog.pg_trigger AS completion_trigger
        JOIN pg_catalog.pg_constraint AS completion_constraint
          ON completion_constraint.oid=completion_trigger.tgconstraint
        JOIN pg_catalog.pg_proc AS completion_function
          ON completion_function.oid=completion_trigger.tgfoid
         WHERE completion_trigger.tgrelid='change_control.proposal'::regclass
           AND completion_trigger.tgname='proposal_verify_reindex_completion'
           AND completion_trigger.tgfoid=pg_catalog.to_regprocedure('retrieval.verify_reindex_completion_trigger()')
           AND completion_trigger.tgtype=21 AND completion_trigger.tgenabled='O' AND NOT completion_trigger.tgisinternal
           AND completion_trigger.tgdeferrable AND completion_trigger.tginitdeferred
           AND completion_trigger.tgnargs=0 AND completion_trigger.tgqual IS NULL AND completion_trigger.tgattr=''::int2vector
           AND completion_constraint.contype='t' AND completion_constraint.condeferrable AND completion_constraint.condeferred
           AND completion_function.prolang=(SELECT oid FROM pg_catalog.pg_language WHERE lanname='plpgsql')
           AND completion_function.prorettype='trigger'::regtype AND NOT completion_function.prosecdef AND NOT completion_function.proisstrict
           AND completion_function.provolatile='v' AND completion_function.proconfig IS NULL
           AND pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(completion_function.prosrc,'UTF8')),'hex')
               ='5733337d81cbb16e6c49a6133f8916b223cbbab52153a259e248697e0c9978a5'
    ) INTO completion_guard_valid;

    IF pointer_exists THEN
        -- The transaction-private witness exists only during the 00082 bridge.
        -- Verify the exact permanent function from 00082 before committing.
        IF NOT upgrade_complete OR NOT transition_guard_valid OR NOT completion_guard_valid
           OR transition_hash IS DISTINCT FROM '60500b092acadc6d9a10ca72a2beeb2e7366659767d4565d5348f2817ab1f8e0'
           OR (SELECT count(*) FROM pg_temp.zhixu_proposal_revision_backfill
                WHERE transaction_id=pg_catalog.pg_current_xact_id())<>1 THEN
            RAISE EXCEPTION 'MIGRATION_PROPOSAL_REVISION_BACKFILL_FINAL_GUARD_MISMATCH'
                USING ERRCODE='55000';
        END IF;
        SET CONSTRAINTS change_control.proposal_verify_reindex_completion DEFERRED;
        DROP TABLE pg_temp.zhixu_proposal_revision_backfill;
        RETURN;
    END IF;

    IF upgrade_complete OR NOT transition_guard_valid OR NOT completion_guard_valid
       OR transition_hash IS DISTINCT FROM 'ce38e1300b22768b6e13237badfd7d5a8898231ed1dbc042c6b5b9871e9a069f'
       OR pg_catalog.to_regclass('pg_temp.zhixu_proposal_revision_backfill') IS NOT NULL THEN
        RAISE EXCEPTION 'MIGRATION_PROPOSAL_REVISION_BACKFILL_UNEXPECTED_SCHEMA'
            USING ERRCODE='55000';
    END IF;

    CREATE TEMPORARY TABLE zhixu_proposal_revision_backfill (
        transaction_id xid8 PRIMARY KEY
    ) ON COMMIT DROP;
    INSERT INTO pg_temp.zhixu_proposal_revision_backfill VALUES (pg_catalog.pg_current_xact_id());

    -- The backfill queues this existing deferred constraint before 00082 adds
    -- its FK. Validate it immediately so ALTER TABLE has no pending trigger
    -- events; never disable the completion check or manufacture completion.
    SET CONSTRAINTS change_control.proposal_verify_reindex_completion IMMEDIATE;

    -- Bind the exception to this server-assigned transaction, not a role or a
    -- caller-settable session flag. Every other mutation remains forbidden.
    -- 00082 creates the pointer column before this function is first invoked.
    EXECUTE pg_catalog.format($definition$
CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
RETURNS trigger LANGUAGE plpgsql AS $guard$
DECLARE
    latest_revision uuid;
BEGIN
    IF pg_catalog.pg_current_xact_id()::text<>%L
       OR TG_OP<>'UPDATE'
       OR OLD.current_revision_id IS NOT NULL OR NEW.current_revision_id IS NULL
       OR (pg_catalog.to_jsonb(NEW)-'current_revision_id')
            IS DISTINCT FROM (pg_catalog.to_jsonb(OLD)-'current_revision_id') THEN
        RAISE EXCEPTION 'proposal revision migration permits only an exact historical pointer backfill'
            USING ERRCODE='23514';
    END IF;
    SELECT id INTO latest_revision
      FROM change_control.proposal_revision
     WHERE proposal_id=OLD.id
     ORDER BY revision_no DESC,id DESC LIMIT 1;
    IF NEW.current_revision_id IS DISTINCT FROM latest_revision THEN
        RAISE EXCEPTION 'proposal revision migration pointer must reference the latest owned revision'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$guard$;
$definition$, pg_catalog.pg_current_xact_id()::text);
END;
$compatibility$;
