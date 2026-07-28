-- +goose Up

-- M8 writes below are retained user facts. Serialize the one-time provenance
-- projection so no lifecycle write can race the schema-only backfill.
LOCK TABLE learning.review_session,
           learning.interview_session,
           learning.interview_learning_path,
           learning.interview_learning_path_step,
           learning.memory,
           learning.memory_audit,
           learning.interview_completion_reservation
    IN ACCESS EXCLUSIVE MODE;

ALTER TABLE learning.interview_learning_path
    ADD CONSTRAINT uq_learning_interview_path_provenance
        UNIQUE (id, workspace_id, session_id);

ALTER TABLE learning.interview_learning_path_step
    ADD CONSTRAINT uq_learning_interview_path_step_provenance
        UNIQUE (id, workspace_id, path_id);

ALTER TABLE learning.memory
    ADD COLUMN interview_session_id uuid,
    ADD COLUMN interview_path_id uuid,
    ADD COLUMN interview_step_id uuid;

-- Existing opaque references must be canonical before any UUID cast. A
-- malformed reference is retained corruption, not something a migration may
-- guess how to repair.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM learning.memory
         WHERE source_type = 'INTERVIEW'
           AND source_ref !~ '^interview:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:learning-path:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:step:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    ) THEN
        RAISE EXCEPTION 'legacy interview memory source_ref is not canonical'
            USING ERRCODE = '23514';
    END IF;
END
$$;
-- +goose StatementEnd

-- These columns are a schema projection, not a Memory edit: keep version,
-- audit history and SSE quiet while the table is exclusively locked.
ALTER TABLE learning.memory DISABLE TRIGGER trg_learning_memory_lifecycle;
ALTER TABLE learning.memory DISABLE TRIGGER memory_project_server_event;

UPDATE learning.memory
   SET interview_session_id = split_part(source_ref, ':', 2)::uuid,
       interview_path_id = split_part(source_ref, ':', 4)::uuid,
       interview_step_id = split_part(source_ref, ':', 6)::uuid
 WHERE source_type = 'INTERVIEW';

ALTER TABLE learning.memory ENABLE TRIGGER memory_project_server_event;
ALTER TABLE learning.memory ENABLE TRIGGER trg_learning_memory_lifecycle;

-- Validate the real Path chain and the immutable v1 creation receipt. Current
-- content/scope may differ after a legitimate audited user edit, so the
-- initial receipt is the source used for migration validation.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM learning.memory AS memory
          LEFT JOIN learning.interview_learning_path AS path
            ON path.id = memory.interview_path_id
           AND path.workspace_id = memory.workspace_id
           AND path.session_id = memory.interview_session_id
          LEFT JOIN learning.interview_learning_path_step AS step
            ON step.id = memory.interview_step_id
           AND step.workspace_id = memory.workspace_id
           AND step.path_id = memory.interview_path_id
         WHERE memory.source_type = 'INTERVIEW'
           AND (
               path.id IS NULL
               OR step.id IS NULL
               OR memory.owner_principal_kind IS DISTINCT FROM 'USER'
               OR memory.owner_principal_id IS DISTINCT FROM '00000000-0000-5000-8000-000000000001'::uuid
               OR memory.memory_type IS DISTINCT FROM 'GOAL'
               OR NOT EXISTS (
                   SELECT 1
                     FROM learning.memory_command AS command
                    WHERE command.workspace_id = memory.workspace_id
                      AND command.memory_id = memory.id
                      AND command.command_type = 'CREATE_CANDIDATE'
                      AND command.expected_version = 0
                      AND command.memory_version = 1
                      AND command.response ->> 'id' = memory.id::text
                      AND command.response ->> 'workspace_id' = memory.workspace_id::text
                      AND command.response -> 'owner' ->> 'kind' = 'USER'
                      AND command.response -> 'owner' ->> 'id' = '00000000-0000-5000-8000-000000000001'
                      AND command.response ->> 'type' = 'GOAL'
                      AND command.response -> 'source' ->> 'type' = 'INTERVIEW'
                      AND command.response -> 'source' ->> 'ref' = memory.source_ref
                      AND command.response ->> 'task_scope_id' = memory.interview_session_id::text
                      AND command.response ->> 'status' = 'CANDIDATE'
                      AND command.response ->> 'version' = '1'
                      AND command.response -> 'content' = jsonb_build_object(
                          'goal', step.title,
                          'rationale', step.rationale
                      )
                      AND NOT (command.response ? 'expires_at')
                      AND NOT (command.response ? 'confirmed_at')
                      AND NOT (command.response ? 'confirmed_by')
               )
               OR EXISTS (
                   SELECT 1
                     FROM learning.memory_command AS command
                    WHERE command.workspace_id = memory.workspace_id
                      AND command.memory_id = memory.id
                      AND command.command_type = 'CREATE_CANDIDATE'
                      AND (
                          command.expected_version IS DISTINCT FROM 0
                          OR command.memory_version IS DISTINCT FROM 1
                          OR command.response ->> 'id' IS DISTINCT FROM memory.id::text
                          OR command.response ->> 'workspace_id' IS DISTINCT FROM memory.workspace_id::text
                          OR command.response -> 'owner' ->> 'kind' IS DISTINCT FROM 'USER'
                          OR command.response -> 'owner' ->> 'id' IS DISTINCT FROM '00000000-0000-5000-8000-000000000001'
                          OR command.response ->> 'type' IS DISTINCT FROM 'GOAL'
                          OR command.response -> 'source' ->> 'type' IS DISTINCT FROM 'INTERVIEW'
                          OR command.response -> 'source' ->> 'ref' IS DISTINCT FROM memory.source_ref
                          OR command.response ->> 'task_scope_id' IS DISTINCT FROM memory.interview_session_id::text
                          OR command.response ->> 'status' IS DISTINCT FROM 'CANDIDATE'
                          OR command.response ->> 'version' IS DISTINCT FROM '1'
                          OR command.response -> 'content' IS DISTINCT FROM jsonb_build_object(
                              'goal', step.title,
                              'rationale', step.rationale
                          )
                          OR command.response ? 'expires_at'
                          OR command.response ? 'confirmed_at'
                          OR command.response ? 'confirmed_by'
                      )
               )
           )
    ) THEN
        RAISE EXCEPTION 'legacy interview memory provenance does not bind its original path step'
            USING ERRCODE = '23514';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_interview_provenance_shape_check
        CHECK (
            (source_type = 'INTERVIEW'
                AND interview_session_id IS NOT NULL
                AND interview_path_id IS NOT NULL
                AND interview_step_id IS NOT NULL)
            OR
            (source_type <> 'INTERVIEW'
                AND interview_session_id IS NULL
                AND interview_path_id IS NULL
                AND interview_step_id IS NULL)
        ) NOT VALID,
    ADD CONSTRAINT fk_learning_memory_interview_path
        FOREIGN KEY (interview_path_id, workspace_id, interview_session_id)
        REFERENCES learning.interview_learning_path(id, workspace_id, session_id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT fk_learning_memory_interview_step
        FOREIGN KEY (interview_step_id, workspace_id, interview_path_id)
        REFERENCES learning.interview_learning_path_step(id, workspace_id, path_id)
        ON DELETE RESTRICT NOT VALID;

ALTER TABLE learning.memory
    VALIDATE CONSTRAINT learning_memory_interview_provenance_shape_check;
ALTER TABLE learning.memory
    VALIDATE CONSTRAINT fk_learning_memory_interview_path;
ALTER TABLE learning.memory
    VALIDATE CONSTRAINT fk_learning_memory_interview_step;

DROP INDEX learning.uq_learning_memory_interview_provenance;
CREATE UNIQUE INDEX uq_learning_memory_interview_step
    ON learning.memory (
        workspace_id,
        owner_principal_kind,
        owner_principal_id,
        interview_step_id
    )
    WHERE source_type = 'INTERVIEW';

-- Populate and validate structured Interview provenance at the database
-- boundary. Later user edits may change content, task scope or expiry, but the
-- source identity remains immutable.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.guard_memory_interview_provenance()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    parsed_session_id uuid;
    parsed_path_id uuid;
    parsed_step_id uuid;
    step_title text;
    step_rationale text;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF ROW(
            NEW.interview_session_id,
            NEW.interview_path_id,
            NEW.interview_step_id
        ) IS DISTINCT FROM ROW(
            OLD.interview_session_id,
            OLD.interview_path_id,
            OLD.interview_step_id
        ) THEN
            RAISE EXCEPTION 'memory interview provenance is immutable'
                USING ERRCODE = '23514';
        END IF;

        IF NEW.source_type = 'INTERVIEW'
           AND NEW.source_ref IS DISTINCT FROM format(
               'interview:%s:learning-path:%s:step:%s',
               NEW.interview_session_id,
               NEW.interview_path_id,
               NEW.interview_step_id
           ) THEN
            RAISE EXCEPTION 'memory interview source_ref is not canonical'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.source_type <> 'INTERVIEW' THEN
        IF NEW.interview_session_id IS NOT NULL
           OR NEW.interview_path_id IS NOT NULL
           OR NEW.interview_step_id IS NOT NULL THEN
            RAISE EXCEPTION 'non-interview memory cannot carry interview provenance'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.source_ref !~ '^interview:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:learning-path:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:step:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
        RAISE EXCEPTION 'memory interview source_ref is not canonical'
            USING ERRCODE = '23514';
    END IF;

    parsed_session_id := split_part(NEW.source_ref, ':', 2)::uuid;
    parsed_path_id := split_part(NEW.source_ref, ':', 4)::uuid;
    parsed_step_id := split_part(NEW.source_ref, ':', 6)::uuid;

    IF (NEW.interview_session_id IS NOT NULL AND NEW.interview_session_id IS DISTINCT FROM parsed_session_id)
       OR (NEW.interview_path_id IS NOT NULL AND NEW.interview_path_id IS DISTINCT FROM parsed_path_id)
       OR (NEW.interview_step_id IS NOT NULL AND NEW.interview_step_id IS DISTINCT FROM parsed_step_id) THEN
        RAISE EXCEPTION 'memory interview columns disagree with source_ref'
            USING ERRCODE = '23514';
    END IF;

    SELECT step.title, step.rationale
      INTO step_title, step_rationale
      FROM learning.interview_learning_path AS path
      JOIN learning.interview_learning_path_step AS step
        ON step.id = parsed_step_id
       AND step.workspace_id = path.workspace_id
       AND step.path_id = path.id
     WHERE path.id = parsed_path_id
       AND path.workspace_id = NEW.workspace_id
       AND path.session_id = parsed_session_id
     FOR KEY SHARE OF path, step;

    IF NOT FOUND
       OR NEW.owner_principal_kind IS DISTINCT FROM 'USER'
       OR NEW.owner_principal_id IS DISTINCT FROM '00000000-0000-5000-8000-000000000001'::uuid
       OR NEW.memory_type IS DISTINCT FROM 'GOAL'
       OR NEW.task_scope_id IS DISTINCT FROM parsed_session_id
       OR NEW.expires_at IS NOT NULL
       OR NEW.content IS DISTINCT FROM jsonb_build_object(
           'goal', step_title,
           'rationale', step_rationale
       ) THEN
        RAISE EXCEPTION 'interview memory candidate does not match its path step'
            USING ERRCODE = '23514';
    END IF;

    NEW.interview_session_id := parsed_session_id;
    NEW.interview_path_id := parsed_path_id;
    NEW.interview_step_id := parsed_step_id;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_memory_interview_provenance ON learning.memory;
CREATE TRIGGER trg_learning_memory_interview_provenance
    BEFORE INSERT OR UPDATE OF source_type, source_ref,
        interview_session_id, interview_path_id, interview_step_id
    ON learning.memory
    FOR EACH ROW EXECUTE FUNCTION learning.guard_memory_interview_provenance();

-- One aggregate version has exactly one lifecycle audit fact. Existing audit
-- histories are retained as-is, but duplicate version identities are unsafe.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM learning.memory_audit
         GROUP BY workspace_id, memory_id, memory_version
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'memory audit contains duplicate aggregate versions'
            USING ERRCODE = '23514';
    END IF;
END
$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX uq_learning_memory_audit_version
    ON learning.memory_audit (workspace_id, memory_id, memory_version);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.guard_memory_audit_aggregate()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    aggregate_owner_kind text;
    aggregate_owner_id uuid;
    aggregate_status text;
    aggregate_version bigint;
BEGIN
    SELECT owner_principal_kind, owner_principal_id, status, version
      INTO aggregate_owner_kind, aggregate_owner_id, aggregate_status, aggregate_version
      FROM learning.memory
     WHERE id = NEW.memory_id
       AND workspace_id = NEW.workspace_id
     FOR KEY SHARE;

    IF NOT FOUND
       OR NEW.owner_principal_kind IS DISTINCT FROM aggregate_owner_kind
       OR NEW.owner_principal_id IS DISTINCT FROM aggregate_owner_id
       OR NEW.memory_version IS DISTINCT FROM aggregate_version
       OR NEW.to_status IS DISTINCT FROM aggregate_status THEN
        RAISE EXCEPTION 'memory audit does not match the aggregate version'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.action = 'CANDIDATE_CREATED'
       AND NEW.memory_version = 1
       AND NEW.from_status IS NULL
       AND NEW.to_status = 'CANDIDATE' THEN
        RETURN NEW;
    ELSIF NEW.action = 'CONFIRMED'
       AND NEW.from_status = 'CANDIDATE'
       AND NEW.to_status = 'ACTIVE' THEN
        RETURN NEW;
    ELSIF NEW.action = 'UPDATED'
       AND NEW.from_status = NEW.to_status
       AND NEW.to_status IN ('CANDIDATE', 'ACTIVE', 'PAUSED') THEN
        RETURN NEW;
    ELSIF NEW.action = 'PAUSED'
       AND NEW.from_status = 'ACTIVE'
       AND NEW.to_status = 'PAUSED' THEN
        RETURN NEW;
    ELSIF NEW.action = 'RESUMED'
       AND NEW.from_status = 'PAUSED'
       AND NEW.to_status = 'ACTIVE' THEN
        RETURN NEW;
    ELSIF NEW.action = 'EXPIRED'
       AND NEW.from_status IN ('CANDIDATE', 'ACTIVE', 'PAUSED')
       AND NEW.to_status = 'EXPIRED' THEN
        RETURN NEW;
    ELSIF NEW.action = 'DELETED'
       AND NEW.from_status IN ('CANDIDATE', 'ACTIVE', 'PAUSED', 'EXPIRED')
       AND NEW.to_status = 'DELETED' THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'memory audit action does not match its lifecycle transition'
        USING ERRCODE = '23514';
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_memory_audit_aggregate_guard ON learning.memory_audit;
CREATE TRIGGER trg_learning_memory_audit_aggregate_guard
    BEFORE INSERT ON learning.memory_audit
    FOR EACH ROW EXECUTE FUNCTION learning.guard_memory_audit_aggregate();

-- A repeated idempotency request must observe the original reservation. It
-- cannot refresh the maintenance clock without advancing durable state.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.reject_interview_completion_keepalive()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.updated_at IS DISTINCT FROM OLD.updated_at
       AND (to_jsonb(NEW) - 'updated_at') = (to_jsonb(OLD) - 'updated_at') THEN
        RAISE EXCEPTION 'interview completion reservation clock cannot advance without progress'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_interview_completion_no_keepalive
    ON learning.interview_completion_reservation;
CREATE TRIGGER trg_learning_interview_completion_no_keepalive
    BEFORE UPDATE OF updated_at ON learning.interview_completion_reservation
    FOR EACH ROW EXECUTE FUNCTION learning.reject_interview_completion_keepalive();

-- Once the Interview child exists, the complete frozen config and started_at
-- are deadline/scoring inputs. Completion still updates only status/ended_at.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.guard_review_session_learning_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM learning.interview_session AS interview
         WHERE interview.session_id = OLD.id
           AND interview.workspace_id = OLD.workspace_id
    ) AND (
        NEW.session_type IS DISTINCT FROM 'INTERVIEW'
        OR NEW.deck_id IS NOT NULL
        OR NEW.config IS DISTINCT FROM OLD.config
        OR NEW.started_at IS DISTINCT FROM OLD.started_at
    ) THEN
        RAISE EXCEPTION 'interview review shell binding is immutable'
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM learning.review_answer AS answer
         WHERE answer.session_id = OLD.id
           AND answer.workspace_id = OLD.workspace_id
    ) AND (
        NEW.session_type IS DISTINCT FROM 'REVIEW'
        OR NEW.deck_id IS NULL
    ) THEN
        RAISE EXCEPTION 'answered review session must remain deck-bound review'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_review_session_learning_binding ON learning.review_session;
CREATE TRIGGER trg_learning_review_session_learning_binding
    BEFORE UPDATE OF session_type, deck_id, config, started_at ON learning.review_session
    FOR EACH ROW EXECUTE FUNCTION learning.guard_review_session_learning_binding();

-- +goose Down

LOCK TABLE learning.review_session,
           learning.interview_session,
           learning.interview_learning_path,
           learning.interview_learning_path_step,
           learning.memory,
           learning.memory_audit,
           learning.interview_completion_reservation
    IN ACCESS EXCLUSIVE MODE;

-- Removing these guards while facts exist would discard structured
-- provenance or reopen immutable deadline/audit/maintenance inputs.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.interview_session LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.memory WHERE source_type = 'INTERVIEW' LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.memory_audit LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.interview_completion_reservation LIMIT 1) THEN
        RAISE EXCEPTION 'M8 interview or memory integrity facts exist; rollback is unsafe'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_review_session_learning_binding ON learning.review_session;

-- Restore the immediately preceding 00056 guard for an empty database.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.guard_review_session_learning_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM learning.interview_session AS interview
         WHERE interview.session_id = OLD.id
           AND interview.workspace_id = OLD.workspace_id
    ) AND (
        NEW.session_type IS DISTINCT FROM 'INTERVIEW'
        OR NEW.deck_id IS NOT NULL
        OR NEW.config ->> 'schema_version' IS DISTINCT FROM 'interview/v1'
    ) THEN
        RAISE EXCEPTION 'interview review shell binding is immutable'
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM learning.review_answer AS answer
         WHERE answer.session_id = OLD.id
           AND answer.workspace_id = OLD.workspace_id
    ) AND (
        NEW.session_type IS DISTINCT FROM 'REVIEW'
        OR NEW.deck_id IS NULL
    ) THEN
        RAISE EXCEPTION 'answered review session must remain deck-bound review'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_learning_review_session_learning_binding
    BEFORE UPDATE OF session_type, deck_id, config ON learning.review_session
    FOR EACH ROW EXECUTE FUNCTION learning.guard_review_session_learning_binding();

DROP TRIGGER IF EXISTS trg_learning_interview_completion_no_keepalive
    ON learning.interview_completion_reservation;
DROP FUNCTION IF EXISTS learning.reject_interview_completion_keepalive();

DROP TRIGGER IF EXISTS trg_learning_memory_audit_aggregate_guard ON learning.memory_audit;
DROP FUNCTION IF EXISTS learning.guard_memory_audit_aggregate();
DROP INDEX learning.uq_learning_memory_audit_version;

DROP TRIGGER IF EXISTS trg_learning_memory_interview_provenance ON learning.memory;
DROP FUNCTION IF EXISTS learning.guard_memory_interview_provenance();

DROP INDEX learning.uq_learning_memory_interview_step;
CREATE UNIQUE INDEX uq_learning_memory_interview_provenance
    ON learning.memory (
        workspace_id,
        owner_principal_kind,
        owner_principal_id,
        source_type,
        source_ref
    )
    WHERE source_type = 'INTERVIEW';

ALTER TABLE learning.memory
    DROP CONSTRAINT fk_learning_memory_interview_step,
    DROP CONSTRAINT fk_learning_memory_interview_path,
    DROP CONSTRAINT learning_memory_interview_provenance_shape_check,
    DROP COLUMN interview_step_id,
    DROP COLUMN interview_path_id,
    DROP COLUMN interview_session_id;

ALTER TABLE learning.interview_learning_path_step
    DROP CONSTRAINT uq_learning_interview_path_step_provenance;
ALTER TABLE learning.interview_learning_path
    DROP CONSTRAINT uq_learning_interview_path_provenance;
