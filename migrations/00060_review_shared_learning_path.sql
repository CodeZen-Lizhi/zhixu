-- +goose Up

-- Learning paths are one aggregate shared by Interview and Review.  Keep the
-- previous Interview relation names as simple, automatically-updatable views:
-- deployed Interview repositories can continue to read and write their own
-- columns while the base table owns the cross-origin invariants.
LOCK TABLE learning.interview_learning_path,
           learning.interview_learning_path_step,
           learning.review_answer,
           learning.artifact_visibility_hold,
           learning.interview_completion_reservation,
           learning.memory
    IN ACCESS EXCLUSIVE MODE;

ALTER TABLE learning.interview_learning_path
    RENAME TO learning_path;
ALTER TABLE learning.interview_learning_path_step
    RENAME TO learning_path_step;

ALTER TABLE learning.learning_path
    RENAME COLUMN session_id TO interview_session_id;
ALTER TABLE learning.learning_path
    RENAME COLUMN report_id TO interview_report_id;

ALTER TABLE learning.learning_path
    ALTER COLUMN interview_session_id DROP NOT NULL,
    ALTER COLUMN interview_report_id DROP NOT NULL,
    ADD COLUMN origin_type text NOT NULL DEFAULT 'INTERVIEW',
    ADD COLUMN review_answer_id uuid,
    ADD COLUMN source_policy_version text NOT NULL DEFAULT 'learning-path/v1',
    ADD CONSTRAINT learning_path_origin_type_check
        CHECK (origin_type IN ('INTERVIEW', 'REVIEW')),
    ADD CONSTRAINT learning_path_source_policy_version_check
        CHECK (btrim(source_policy_version) <> '' AND octet_length(source_policy_version) <= 128),
    ADD CONSTRAINT learning_path_origin_shape_check
        CHECK (
            (origin_type = 'INTERVIEW'
                AND interview_session_id IS NOT NULL
                AND interview_report_id IS NOT NULL
                AND review_answer_id IS NULL)
            OR
            (origin_type = 'REVIEW'
                AND interview_session_id IS NULL
                AND interview_report_id IS NULL
                AND review_answer_id IS NOT NULL)
        );

ALTER TABLE learning.review_answer
    ADD CONSTRAINT uq_learning_review_answer_workspace_id UNIQUE (id, workspace_id);

ALTER TABLE learning.learning_path
    ADD CONSTRAINT fk_learning_path_review_answer
        FOREIGN KEY (review_answer_id, workspace_id)
        REFERENCES learning.review_answer(id, workspace_id) ON DELETE RESTRICT,
    ADD CONSTRAINT uq_learning_path_review_answer
        UNIQUE (workspace_id, review_answer_id),
    ADD CONSTRAINT uq_learning_path_review_final_binding
        UNIQUE (
            id, workspace_id, origin_type, review_answer_id,
            artifact_id, artifact_revision_id, artifact_version
        );

ALTER TABLE learning.learning_path_step
    RENAME CONSTRAINT fk_learning_interview_learning_path_step_path
        TO fk_learning_path_step_path;

-- Keep old Interview SQL source-compatible while preventing those repositories
-- from observing or mutating Review-owned paths.  The correlated step filter
-- retains a single base relation, so both views remain automatically updatable.
CREATE VIEW learning.interview_learning_path AS
SELECT id,
       workspace_id,
       interview_session_id AS session_id,
       interview_report_id AS report_id,
       artifact_id,
       artifact_revision_id,
       artifact_version,
       status,
       version,
       created_at,
       updated_at
  FROM learning.learning_path
 WHERE origin_type = 'INTERVIEW'
WITH LOCAL CHECK OPTION;

CREATE VIEW learning.interview_learning_path_step AS
SELECT id,
       workspace_id,
       path_id,
       step_no,
       claim_id,
       topic_id,
       source_version_id,
       source_span_id,
       evidence_hash,
       title,
       rationale,
       status,
       version,
       created_at,
       updated_at
  FROM learning.learning_path_step AS step
 WHERE EXISTS (
       SELECT 1
         FROM learning.learning_path AS path
        WHERE path.workspace_id = step.workspace_id
          AND path.id = step.path_id
          AND path.origin_type = 'INTERVIEW'
 )
WITH LOCAL CHECK OPTION;

-- Review Path creation must identify the immutable Answer query that should be
-- refreshed in another browser tab. Interview Path events retain the original
-- empty payload and therefore do not expose any additional session content.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_shared_learning_path_server_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    row_data jsonb;
    action text;
    event_type text;
    resource_id text;
    resource_version bigint;
    occurred_at timestamptz;
    payload_summary jsonb;
BEGIN
    IF TG_OP = 'DELETE' THEN
        row_data := to_jsonb(OLD);
        action := 'deleted';
    ELSIF TG_OP = 'UPDATE' THEN
        row_data := to_jsonb(NEW);
        action := 'updated';
    ELSE
        row_data := to_jsonb(NEW);
        action := 'created';
    END IF;

    resource_id := row_data ->> 'id';
    resource_version := NULLIF(row_data ->> 'version', '')::bigint;
    IF resource_id IS NULL OR resource_id = ''
       OR resource_version IS NULL OR resource_version < 1 THEN
        RAISE EXCEPTION 'learning path SSE identity or version is invalid'
            USING ERRCODE = '23514';
    END IF;

    payload_summary := '{}'::jsonb;
    IF row_data ->> 'origin_type' = 'REVIEW' THEN
        IF NULLIF(row_data ->> 'review_answer_id', '') IS NULL THEN
            RAISE EXCEPTION 'review learning path SSE answer identity is missing'
                USING ERRCODE = '23514';
        END IF;
        payload_summary := jsonb_build_object(
            'answer_id', row_data ->> 'review_answer_id'
        );
    END IF;

    occurred_at := COALESCE(
        NULLIF(row_data ->> 'updated_at', '')::timestamptz,
        CURRENT_TIMESTAMP
    );
    event_type := 'learning_path.' || action;

    INSERT INTO ops.server_event(
        workspace_id,conversation_id,workflow_run_id,event_type,resource_ref,
        resource_version,payload_summary,schema_version,source_event_ref,
        occurred_at,expires_at
    ) VALUES (
        (row_data ->> 'workspace_id')::uuid,NULL,NULL,event_type,
        'learning_path:' || resource_id,resource_version,payload_summary,1,
        event_type || ':' || resource_id || ':v' || resource_version::text ||
            ':tx' || txid_current()::text,
        occurred_at,occurred_at + INTERVAL '24 hours'
    )
    ON CONFLICT (workspace_id,source_event_ref) DO NOTHING;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS interview_learning_path_project_server_event
    ON learning.learning_path;
CREATE TRIGGER interview_learning_path_project_server_event
    AFTER INSERT OR UPDATE ON learning.learning_path
    FOR EACH ROW EXECUTE FUNCTION ops.project_shared_learning_path_server_event();

-- A Review answer is immutable history.  The composite key gives the shared
-- path a workspace-scoped binding without allowing a path to cross tenants.
CREATE TABLE learning.learning_path_command (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (command_type IN ('CREATE_REVIEW_PATH', 'PATH_STEP', 'PATH_STATUS')),
    path_id uuid NOT NULL,
    expected_version bigint NOT NULL CHECK (expected_version >= 0),
    path_version bigint NOT NULL CHECK (path_version > 0),
    response jsonb NOT NULL CHECK (jsonb_typeof(response) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key),
    CONSTRAINT fk_learning_path_command_path
        FOREIGN KEY (path_id, workspace_id)
        REFERENCES learning.learning_path(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX idx_learning_path_command_path_created
    ON learning.learning_path_command (workspace_id, path_id, created_at DESC);

DROP TRIGGER IF EXISTS trg_learning_path_command_append_only ON learning.learning_path_command;
CREATE TRIGGER trg_learning_path_command_append_only
    BEFORE UPDATE OR DELETE ON learning.learning_path_command
    FOR EACH ROW EXECUTE FUNCTION learning.reject_m8_append_only_mutation();

-- A Review path derives from one frozen answer snapshot.  Source and Artifact
-- digests fence retries; the completed binding points at the only path whose
-- Review-answer and Artifact tuple agrees with the reservation.
CREATE TABLE learning.learning_path_creation_reservation (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    review_answer_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    source_snapshot jsonb NOT NULL CHECK (jsonb_typeof(source_snapshot) = 'object'),
    source_snapshot_digest text NOT NULL CHECK (source_snapshot_digest ~ '^[0-9a-f]{64}$'),
    attempt_no bigint NOT NULL DEFAULT 1 CHECK (attempt_no > 0),
    origin_type text NOT NULL DEFAULT 'REVIEW',
    artifact_digest text CHECK (artifact_digest IS NULL OR artifact_digest ~ '^[0-9a-f]{64}$'),
    path_id uuid,
    artifact_id uuid,
    artifact_revision_id uuid,
    artifact_version bigint,
    status text NOT NULL CHECK (status IN ('PENDING', 'COMPLETED', 'ABANDONED')),
    created_at timestamptz NOT NULL,
    prepared_at timestamptz,
    completed_at timestamptz,
    abandoned_at timestamptz,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, review_answer_id),
    CONSTRAINT uq_learning_path_creation_reservation_key
        UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT fk_learning_path_creation_reservation_answer
        FOREIGN KEY (review_answer_id, workspace_id)
        REFERENCES learning.review_answer(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_path_creation_reservation_final_path
        FOREIGN KEY (
            path_id, workspace_id, origin_type, review_answer_id,
            artifact_id, artifact_revision_id, artifact_version
        ) REFERENCES learning.learning_path (
            id, workspace_id, origin_type, review_answer_id,
            artifact_id, artifact_revision_id, artifact_version
        ) ON DELETE RESTRICT,
    CONSTRAINT learning_path_creation_reservation_origin_check
        CHECK (origin_type = 'REVIEW'),
    CONSTRAINT learning_path_creation_reservation_digest_time_check
        CHECK (
            (artifact_digest IS NULL AND prepared_at IS NULL)
            OR (artifact_digest IS NOT NULL AND prepared_at IS NOT NULL)
        ),
    CONSTRAINT learning_path_creation_reservation_binding_shape_check
        CHECK (
            (path_id IS NULL
                AND artifact_id IS NULL
                AND artifact_revision_id IS NULL
                AND artifact_version IS NULL)
            OR
            (path_id IS NOT NULL
                AND artifact_id IS NOT NULL
                AND artifact_revision_id IS NOT NULL
                AND artifact_version > 0)
        ),
    CONSTRAINT learning_path_creation_reservation_status_shape_check
        CHECK (
            (status = 'PENDING'
                AND completed_at IS NULL
                AND abandoned_at IS NULL
                AND path_id IS NULL)
            OR
            (status = 'ABANDONED'
                AND completed_at IS NULL
                AND abandoned_at IS NOT NULL
                AND path_id IS NULL)
            OR
            (status = 'COMPLETED'
                AND artifact_digest IS NOT NULL
                AND completed_at IS NOT NULL
                AND abandoned_at IS NULL
                AND path_id IS NOT NULL)
        ),
    CONSTRAINT learning_path_creation_reservation_time_order_check
        CHECK (
            updated_at >= created_at
            AND (prepared_at IS NULL OR (prepared_at >= created_at AND updated_at >= prepared_at))
            AND (completed_at IS NULL OR (completed_at >= created_at AND updated_at >= completed_at))
            AND (abandoned_at IS NULL OR (abandoned_at >= created_at AND updated_at >= abandoned_at))
        )
);

CREATE INDEX idx_learning_path_creation_pending_maintenance
    ON learning.learning_path_creation_reservation (updated_at, workspace_id, review_answer_id)
    WHERE status = 'PENDING';

-- Replays may read a pending reservation, but cannot extend the 24 hour
-- maintenance window unless they also advance a durable reservation field.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.reject_learning_path_creation_keepalive()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.updated_at IS DISTINCT FROM OLD.updated_at
       AND (to_jsonb(NEW) - 'updated_at') = (to_jsonb(OLD) - 'updated_at') THEN
        RAISE EXCEPTION 'learning path creation reservation clock cannot advance without progress'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_learning_path_creation_no_keepalive
    BEFORE UPDATE OF updated_at ON learning.learning_path_creation_reservation
    FOR EACH ROW EXECUTE FUNCTION learning.reject_learning_path_creation_keepalive();

-- The legacy hold retained owner_id for compatibility.  The two structured
-- owner columns are the database facts used by the corresponding reservation
-- FKs and prevent a Review answer from masquerading as an Interview session.
ALTER TABLE learning.artifact_visibility_hold
    DROP CONSTRAINT artifact_visibility_hold_owner_type_check,
    DROP CONSTRAINT fk_learning_artifact_visibility_hold_interview,
    ADD COLUMN interview_session_id uuid,
    ADD COLUMN review_answer_id uuid;

UPDATE learning.artifact_visibility_hold
   SET interview_session_id = owner_id
 WHERE owner_type = 'INTERVIEW_COMPLETE';

ALTER TABLE learning.artifact_visibility_hold
    ADD CONSTRAINT learning_artifact_visibility_hold_owner_type_check
        CHECK (owner_type IN ('INTERVIEW_COMPLETE', 'LEARNING_PATH_CREATE')),
    ADD CONSTRAINT learning_artifact_visibility_hold_owner_shape_check
        CHECK (
            (owner_type = 'INTERVIEW_COMPLETE'
                AND owner_id = interview_session_id
                AND interview_session_id IS NOT NULL
                AND review_answer_id IS NULL
                AND owner_role IN ('REPORT', 'PATH'))
            OR
            (owner_type = 'LEARNING_PATH_CREATE'
                AND owner_id = review_answer_id
                AND interview_session_id IS NULL
                AND review_answer_id IS NOT NULL
                AND owner_role = 'PATH')
        ),
    ADD CONSTRAINT fk_learning_artifact_visibility_hold_interview
        FOREIGN KEY (interview_session_id, workspace_id)
        REFERENCES learning.interview_session(session_id, workspace_id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_learning_artifact_visibility_hold_review_answer
        FOREIGN KEY (review_answer_id, workspace_id)
        REFERENCES learning.review_answer(id, workspace_id) ON DELETE RESTRICT;

DROP TRIGGER IF EXISTS trg_learning_interview_artifact_visibility_hold_guard
    ON learning.artifact_visibility_hold;

-- ACTIVE holds must be anchored in a pending reservation and its exact PLAN
-- receipt.  Interview keeps its two-role completion protocol; Review creates
-- only the LEARNING_PATH Artifact with a distinct stage-key namespace.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.guard_interview_artifact_visibility_hold()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    reservation_status text;
    reservation_digest text;
    held_artifact_type text;
    plan_binding_exists boolean;
BEGIN
    -- Existing Interview writers still submit owner_id.  Project that legacy
    -- identity into the structured FK column before the row-level shape check;
    -- a caller that supplies both must supply the same identifier.
    IF NEW.owner_type = 'INTERVIEW_COMPLETE' THEN
        IF NEW.interview_session_id IS NULL THEN
            NEW.interview_session_id := NEW.owner_id;
        ELSIF NEW.interview_session_id IS DISTINCT FROM NEW.owner_id THEN
            RAISE EXCEPTION 'interview artifact hold owner identifiers disagree'
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.owner_type = 'LEARNING_PATH_CREATE' THEN
        IF NEW.review_answer_id IS NULL THEN
            NEW.review_answer_id := NEW.owner_id;
        ELSIF NEW.review_answer_id IS DISTINCT FROM NEW.owner_id THEN
            RAISE EXCEPTION 'review path hold owner identifiers disagree'
                USING ERRCODE = '23514';
        END IF;
    END IF;

    IF NEW.disposition <> 'ACTIVE' THEN
        RETURN NEW;
    END IF;

    IF NEW.owner_type = 'INTERVIEW_COMPLETE' THEN
        PERFORM 1
          FROM learning.interview_session AS session
         WHERE session.workspace_id = NEW.workspace_id
           AND session.session_id = NEW.interview_session_id
         FOR UPDATE;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'active interview artifact hold must bind an existing interview session'
                USING ERRCODE = '23514';
        END IF;

        SELECT reservation.status,
               reservation.artifact_digest,
               artifact.artifact_type,
               EXISTS (
                   SELECT 1
                     FROM learning.artifact_command AS command
                    WHERE command.workspace_id = NEW.workspace_id
                      AND command.artifact_id = NEW.artifact_id
                      AND command.command_type = 'PLAN'
                      AND command.idempotency_key =
                          'iv1:' || NEW.interview_session_id::text || ':' || artifact.artifact_type || ':' ||
                          reservation.artifact_digest || ':p'
               )
          INTO reservation_status, reservation_digest, held_artifact_type, plan_binding_exists
          FROM learning.interview_completion_reservation AS reservation
          JOIN learning.artifact AS artifact
            ON artifact.workspace_id = NEW.workspace_id
           AND artifact.id = NEW.artifact_id
         WHERE reservation.workspace_id = NEW.workspace_id
           AND reservation.session_id = NEW.interview_session_id
         FOR UPDATE OF reservation;

        IF NOT FOUND
           OR reservation_status IS DISTINCT FROM 'PENDING'
           OR reservation_digest IS NULL
           OR (NEW.attempt_digest IS NOT NULL AND NEW.attempt_digest IS DISTINCT FROM reservation_digest)
           OR plan_binding_exists IS DISTINCT FROM true
           OR NOT (
               (NEW.owner_role = 'REPORT' AND held_artifact_type = 'INTERVIEW_DOC')
               OR (NEW.owner_role = 'PATH' AND held_artifact_type = 'LEARNING_PATH')
           ) THEN
            RAISE EXCEPTION 'active interview artifact hold must bind the pending completion digest'
                USING ERRCODE = '23514';
        END IF;

        NEW.attempt_digest := reservation_digest;
        RETURN NEW;
    END IF;

    IF NEW.owner_type = 'LEARNING_PATH_CREATE' THEN
        PERFORM 1
          FROM learning.review_answer AS answer
         WHERE answer.workspace_id = NEW.workspace_id
           AND answer.id = NEW.review_answer_id
         FOR UPDATE;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'active review path hold must bind an existing review answer'
                USING ERRCODE = '23514';
        END IF;

        SELECT reservation.status,
               reservation.artifact_digest,
               artifact.artifact_type,
               EXISTS (
                   SELECT 1
                     FROM learning.artifact_command AS command
                    WHERE command.workspace_id = NEW.workspace_id
                      AND command.artifact_id = NEW.artifact_id
                      AND command.command_type = 'PLAN'
                      AND command.idempotency_key =
                          'lp1:review:' || NEW.review_answer_id::text || ':LEARNING_PATH:' ||
                          reservation.artifact_digest || ':p'
               )
          INTO reservation_status, reservation_digest, held_artifact_type, plan_binding_exists
          FROM learning.learning_path_creation_reservation AS reservation
          JOIN learning.artifact AS artifact
            ON artifact.workspace_id = NEW.workspace_id
           AND artifact.id = NEW.artifact_id
         WHERE reservation.workspace_id = NEW.workspace_id
           AND reservation.review_answer_id = NEW.review_answer_id
         FOR UPDATE OF reservation;

        IF NOT FOUND
           OR reservation_status IS DISTINCT FROM 'PENDING'
           OR reservation_digest IS NULL
           OR (NEW.attempt_digest IS NOT NULL AND NEW.attempt_digest IS DISTINCT FROM reservation_digest)
           OR held_artifact_type IS DISTINCT FROM 'LEARNING_PATH'
           OR NEW.owner_role IS DISTINCT FROM 'PATH'
           OR plan_binding_exists IS DISTINCT FROM true THEN
            RAISE EXCEPTION 'active review path hold must bind the pending creation digest'
                USING ERRCODE = '23514';
        END IF;

        NEW.attempt_digest := reservation_digest;
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'active artifact hold owner type is unsupported'
        USING ERRCODE = '23514';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_learning_interview_artifact_visibility_hold_guard
    BEFORE INSERT OR UPDATE OF workspace_id, artifact_id, owner_type, owner_id, owner_role,
        interview_session_id, review_answer_id, attempt_digest, disposition
    ON learning.artifact_visibility_hold
    FOR EACH ROW EXECUTE FUNCTION learning.guard_interview_artifact_visibility_hold();

-- The old binding trigger follows the physical table through ALTER ... RENAME.
-- Teach it the shared base-table name so both origins continue to require a
-- current LEARNING_PATH Artifact revision.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.assert_interview_artifact_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    expected_type text;
    actual_type text;
    artifact_schema_version text;
    revision_schema_version text;
    current_revision_id uuid;
    current_artifact_version bigint;
BEGIN
    expected_type := CASE TG_TABLE_NAME
        WHEN 'interview_report' THEN 'INTERVIEW_DOC'
        WHEN 'learning_path' THEN 'LEARNING_PATH'
        ELSE NULL
    END;
    SELECT artifact.artifact_type,
           artifact.domain_schema_version,
           artifact.current_revision_id,
           artifact.version,
           revision.domain_schema_version
      INTO actual_type,
           artifact_schema_version,
           current_revision_id,
           current_artifact_version,
           revision_schema_version
      FROM learning.artifact AS artifact
      JOIN learning.artifact_revision AS revision
        ON revision.id = NEW.artifact_revision_id
       AND revision.workspace_id = NEW.workspace_id
       AND revision.artifact_id = NEW.artifact_id
     WHERE artifact.id = NEW.artifact_id
       AND artifact.workspace_id = NEW.workspace_id;
    IF actual_type IS DISTINCT FROM expected_type
       OR artifact_schema_version IS DISTINCT FROM 'artifact/v1'
       OR revision_schema_version IS DISTINCT FROM 'artifact-revision/v1'
       OR current_revision_id IS DISTINCT FROM NEW.artifact_revision_id
       OR current_artifact_version IS DISTINCT FROM NEW.artifact_version THEN
        RAISE EXCEPTION 'learning path artifact binding is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Path and step evidence are historical facts.  Only their explicit workflow
-- status may advance, always with a single optimistic-lock version increment.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.enforce_learning_path_history()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'learning path is retained history' USING ERRCODE = '55000';
    END IF;

    IF ROW(
        NEW.id, NEW.workspace_id, NEW.interview_session_id,
        NEW.interview_report_id, NEW.review_answer_id, NEW.origin_type,
        NEW.source_policy_version, NEW.artifact_id, NEW.artifact_revision_id,
        NEW.artifact_version, NEW.created_at
    ) IS DISTINCT FROM ROW(
        OLD.id, OLD.workspace_id, OLD.interview_session_id,
        OLD.interview_report_id, OLD.review_answer_id, OLD.origin_type,
        OLD.source_policy_version, OLD.artifact_id, OLD.artifact_revision_id,
        OLD.artifact_version, OLD.created_at
    ) THEN
        RAISE EXCEPTION 'learning path facts are immutable' USING ERRCODE = '55000';
    END IF;

    IF NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'learning path version progression is invalid' USING ERRCODE = '23514';
    END IF;

    -- A step transition also advances the aggregate version while the path
    -- remains ACTIVE. The established command contract also permits an
    -- explicit same-status, new-version receipt. Keep those legal optimistic-
    -- lock progressions, but do not permit a terminal path to be reopened or
    -- a paused path to complete without first being resumed.
    IF NEW.status = OLD.status
       OR (OLD.status = 'ACTIVE' AND NEW.status IN ('PAUSED', 'COMPLETED'))
       OR (OLD.status = 'PAUSED' AND NEW.status = 'ACTIVE') THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'learning path status transition is invalid' USING ERRCODE = '23514';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_learning_path_history
    BEFORE UPDATE OR DELETE ON learning.learning_path
    FOR EACH ROW EXECUTE FUNCTION learning.enforce_learning_path_history();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.enforce_learning_path_step_history()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'learning path step is retained history' USING ERRCODE = '55000';
    END IF;

    IF ROW(
        NEW.id, NEW.workspace_id, NEW.path_id, NEW.step_no, NEW.claim_id,
        NEW.topic_id, NEW.source_version_id, NEW.source_span_id,
        NEW.evidence_hash, NEW.title, NEW.rationale, NEW.created_at
    ) IS DISTINCT FROM ROW(
        OLD.id, OLD.workspace_id, OLD.path_id, OLD.step_no, OLD.claim_id,
        OLD.topic_id, OLD.source_version_id, OLD.source_span_id,
        OLD.evidence_hash, OLD.title, OLD.rationale, OLD.created_at
    ) THEN
        RAISE EXCEPTION 'learning path step facts are immutable' USING ERRCODE = '55000';
    END IF;

    IF NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'learning path step version progression is invalid' USING ERRCODE = '23514';
    END IF;

    IF NEW.status = OLD.status
       OR (OLD.status = 'PENDING' AND NEW.status IN ('IN_PROGRESS', 'COMPLETED', 'SKIPPED'))
       OR (OLD.status = 'IN_PROGRESS' AND NEW.status IN ('COMPLETED', 'SKIPPED')) THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'learning path step status transition is invalid' USING ERRCODE = '23514';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_learning_path_step_history
    BEFORE UPDATE OR DELETE ON learning.learning_path_step
    FOR EACH ROW EXECUTE FUNCTION learning.enforce_learning_path_step_history();

-- 00059 structured Memory provenance keeps the Interview identifiers even
-- though the path aggregate is now shared.  Resolve only through the base
-- tables; the compatibility views remain for deployed Interview SQL, not as a
-- second persistence owner.
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

    IF NEW.source_ref !~ '^interview:[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}:learning-path:[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}:step:[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' THEN
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
      FROM learning.learning_path AS path
      JOIN learning.learning_path_step AS step
        ON step.id = parsed_step_id
       AND step.workspace_id = path.workspace_id
       AND step.path_id = path.id
     WHERE path.id = parsed_path_id
       AND path.workspace_id = NEW.workspace_id
       AND path.origin_type = 'INTERVIEW'
       AND path.interview_session_id = parsed_session_id
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
END;
$$;
-- +goose StatementEnd

-- +goose Down

LOCK TABLE learning.learning_path,
           learning.learning_path_step,
           learning.learning_path_command,
           learning.learning_path_creation_reservation,
           learning.artifact_visibility_hold
    IN ACCESS EXCLUSIVE MODE;

-- Review facts are not representable by the pre-shared Interview schema.
-- Never silently drop them during a rollback; a forward migration must retain
-- the completed path, reservation, command receipt and hidden Artifact state.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM learning.learning_path
         WHERE origin_type = 'REVIEW'
         LIMIT 1
    ) OR EXISTS (
        SELECT 1
          FROM learning.learning_path_command
         LIMIT 1
    ) OR EXISTS (
        SELECT 1
          FROM learning.learning_path_creation_reservation
         LIMIT 1
    ) OR EXISTS (
        SELECT 1
          FROM learning.artifact_visibility_hold
         WHERE owner_type = 'LEARNING_PATH_CREATE'
         LIMIT 1
    ) THEN
        RAISE EXCEPTION 'review learning path facts exist; rollback is unsafe'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

-- Restore the 00047 generic projection before the base table returns to its
-- Interview-only name. ALTER TABLE ... RENAME keeps this trigger attached.
DROP TRIGGER IF EXISTS interview_learning_path_project_server_event
    ON learning.learning_path;
DROP FUNCTION IF EXISTS ops.project_shared_learning_path_server_event();
CREATE TRIGGER interview_learning_path_project_server_event
    AFTER INSERT OR UPDATE ON learning.learning_path
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event(
        'learning_path','learning_path','id','version','updated_at'
    );

DROP TRIGGER IF EXISTS trg_learning_path_step_history ON learning.learning_path_step;
DROP FUNCTION IF EXISTS learning.enforce_learning_path_step_history();
DROP TRIGGER IF EXISTS trg_learning_path_history ON learning.learning_path;
DROP FUNCTION IF EXISTS learning.enforce_learning_path_history();

DROP TRIGGER IF EXISTS trg_learning_path_creation_no_keepalive
    ON learning.learning_path_creation_reservation;
DROP FUNCTION IF EXISTS learning.reject_learning_path_creation_keepalive();
DROP INDEX learning.idx_learning_path_creation_pending_maintenance;
DROP TABLE learning.learning_path_creation_reservation;

DROP TRIGGER IF EXISTS trg_learning_path_command_append_only ON learning.learning_path_command;
DROP INDEX learning.idx_learning_path_command_path_created;
DROP TABLE learning.learning_path_command;

DROP TRIGGER IF EXISTS trg_learning_interview_artifact_visibility_hold_guard
    ON learning.artifact_visibility_hold;
ALTER TABLE learning.artifact_visibility_hold
    DROP CONSTRAINT fk_learning_artifact_visibility_hold_review_answer,
    DROP CONSTRAINT learning_artifact_visibility_hold_owner_shape_check,
    DROP CONSTRAINT learning_artifact_visibility_hold_owner_type_check,
    DROP CONSTRAINT fk_learning_artifact_visibility_hold_interview,
    DROP COLUMN review_answer_id,
    DROP COLUMN interview_session_id,
    ADD CONSTRAINT artifact_visibility_hold_owner_type_check
        CHECK (owner_type = 'INTERVIEW_COMPLETE'),
    ADD CONSTRAINT fk_learning_artifact_visibility_hold_interview
        FOREIGN KEY (owner_id, workspace_id)
        REFERENCES learning.interview_session(session_id, workspace_id) ON DELETE RESTRICT;

-- Restore the 00057 Interview-only hold guard after the Review-only columns
-- are gone.  The trigger name remains stable for existing operational checks.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.guard_interview_artifact_visibility_hold()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    reservation_status text;
    reservation_digest text;
    held_artifact_type text;
    plan_binding_exists boolean;
BEGIN
    IF NEW.disposition <> 'ACTIVE' THEN
        RETURN NEW;
    END IF;

    PERFORM 1
      FROM learning.interview_session AS session
     WHERE session.workspace_id = NEW.workspace_id
       AND session.session_id = NEW.owner_id
     FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'active interview artifact hold must bind an existing interview session'
            USING ERRCODE = '23514';
    END IF;

    SELECT reservation.status,
           reservation.artifact_digest,
           artifact.artifact_type,
           EXISTS (
               SELECT 1
                 FROM learning.artifact_command AS command
                WHERE command.workspace_id = NEW.workspace_id
                  AND command.artifact_id = NEW.artifact_id
                  AND command.command_type = 'PLAN'
                  AND command.idempotency_key =
                      'iv1:' || NEW.owner_id::text || ':' || artifact.artifact_type || ':' ||
                      reservation.artifact_digest || ':p'
           )
      INTO reservation_status, reservation_digest, held_artifact_type, plan_binding_exists
      FROM learning.interview_completion_reservation AS reservation
      JOIN learning.artifact AS artifact
        ON artifact.workspace_id = NEW.workspace_id
       AND artifact.id = NEW.artifact_id
     WHERE reservation.workspace_id = NEW.workspace_id
       AND reservation.session_id = NEW.owner_id
     FOR UPDATE OF reservation;

    IF NEW.owner_type IS DISTINCT FROM 'INTERVIEW_COMPLETE'
       OR NOT FOUND
       OR reservation_status IS DISTINCT FROM 'PENDING'
       OR reservation_digest IS NULL
       OR (NEW.attempt_digest IS NOT NULL AND NEW.attempt_digest IS DISTINCT FROM reservation_digest)
       OR plan_binding_exists IS DISTINCT FROM true
       OR NOT (
           (NEW.owner_role = 'REPORT' AND held_artifact_type = 'INTERVIEW_DOC')
           OR (NEW.owner_role = 'PATH' AND held_artifact_type = 'LEARNING_PATH')
       ) THEN
        RAISE EXCEPTION 'active interview artifact hold must bind the pending completion digest'
            USING ERRCODE = '23514';
    END IF;

    NEW.attempt_digest := reservation_digest;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_learning_interview_artifact_visibility_hold_guard
    BEFORE INSERT OR UPDATE OF workspace_id, artifact_id, owner_type, owner_id, owner_role, attempt_digest, disposition
    ON learning.artifact_visibility_hold
    FOR EACH ROW EXECUTE FUNCTION learning.guard_interview_artifact_visibility_hold();

DROP VIEW learning.interview_learning_path_step;
DROP VIEW learning.interview_learning_path;

ALTER TABLE learning.learning_path
    DROP CONSTRAINT uq_learning_path_review_final_binding,
    DROP CONSTRAINT uq_learning_path_review_answer,
    DROP CONSTRAINT fk_learning_path_review_answer,
    DROP CONSTRAINT learning_path_origin_shape_check,
    DROP CONSTRAINT learning_path_source_policy_version_check,
    DROP CONSTRAINT learning_path_origin_type_check,
    ALTER COLUMN interview_session_id SET NOT NULL,
    ALTER COLUMN interview_report_id SET NOT NULL,
    DROP COLUMN review_answer_id,
    DROP COLUMN source_policy_version,
    DROP COLUMN origin_type;

ALTER TABLE learning.review_answer
    DROP CONSTRAINT uq_learning_review_answer_workspace_id;

ALTER TABLE learning.learning_path_step
    RENAME CONSTRAINT fk_learning_path_step_path
        TO fk_learning_interview_learning_path_step_path;

ALTER TABLE learning.learning_path
    RENAME COLUMN interview_session_id TO session_id;
ALTER TABLE learning.learning_path
    RENAME COLUMN interview_report_id TO report_id;
ALTER TABLE learning.learning_path
    RENAME TO interview_learning_path;
ALTER TABLE learning.learning_path_step
    RENAME TO interview_learning_path_step;

-- 00059 must return to the physical Interview table after rollback.  The
-- source_ref and structured FK columns themselves are retained unchanged.
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

    IF NEW.source_ref !~ '^interview:[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}:learning-path:[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}:step:[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' THEN
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
END;
$$;
-- +goose StatementEnd

-- Restore the original artifact-binding dispatch after the physical table name
-- is back in place.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.assert_interview_artifact_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    expected_type text;
    actual_type text;
    artifact_schema_version text;
    revision_schema_version text;
    current_revision_id uuid;
    current_artifact_version bigint;
BEGIN
    expected_type := CASE TG_TABLE_NAME
        WHEN 'interview_report' THEN 'INTERVIEW_DOC'
        WHEN 'interview_learning_path' THEN 'LEARNING_PATH'
        ELSE NULL
    END;
    SELECT artifact.artifact_type,
           artifact.domain_schema_version,
           artifact.current_revision_id,
           artifact.version,
           revision.domain_schema_version
      INTO actual_type,
           artifact_schema_version,
           current_revision_id,
           current_artifact_version,
           revision_schema_version
      FROM learning.artifact AS artifact
      JOIN learning.artifact_revision AS revision
        ON revision.id = NEW.artifact_revision_id
       AND revision.workspace_id = NEW.workspace_id
       AND revision.artifact_id = NEW.artifact_id
     WHERE artifact.id = NEW.artifact_id
       AND artifact.workspace_id = NEW.workspace_id;
    IF actual_type IS DISTINCT FROM expected_type
       OR artifact_schema_version IS DISTINCT FROM 'artifact/v1'
       OR revision_schema_version IS DISTINCT FROM 'artifact-revision/v1'
       OR current_revision_id IS DISTINCT FROM NEW.artifact_revision_id
       OR current_artifact_version IS DISTINCT FROM NEW.artifact_version THEN
        RAISE EXCEPTION 'interview artifact binding is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
