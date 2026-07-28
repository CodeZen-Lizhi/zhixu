-- +goose Up

-- Interview reuses learning.review_session only as its lifecycle shell.  The
-- question, turn, report and path facts are deliberately separate from
-- review_answer/review_schedule so an interview can never advance FSRS.
CREATE TABLE IF NOT EXISTS learning.interview_session (
    session_id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    domain_schema_version text NOT NULL CHECK (domain_schema_version = 'interview/v1'),
    version bigint NOT NULL CHECK (version > 0),
    follow_up_count integer NOT NULL DEFAULT 0 CHECK (follow_up_count >= 0 AND follow_up_count <= 20),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_interview_session_workspace UNIQUE (session_id, workspace_id),
    CONSTRAINT fk_learning_interview_session_review_shell
        FOREIGN KEY (session_id, workspace_id)
        REFERENCES learning.review_session(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT learning_interview_session_time_order CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS learning.interview_question (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    session_id uuid NOT NULL,
    question_no integer NOT NULL CHECK (question_no > 0),
    follow_up_no integer NOT NULL DEFAULT 0 CHECK (follow_up_no >= 0),
    parent_question_id uuid,
    claim_id uuid NOT NULL,
    topic_id uuid,
    prompt text NOT NULL CHECK (btrim(prompt) <> '' AND octet_length(prompt) <= 8192),
    answer_points jsonb NOT NULL CHECK (jsonb_typeof(answer_points) = 'array' AND jsonb_array_length(answer_points) > 0),
    evidence jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'array' AND jsonb_array_length(evidence) > 0),
    status text NOT NULL CHECK (status IN ('PENDING', 'ANSWERED', 'SKIPPED')),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    answered_at timestamptz,
    CONSTRAINT uq_learning_interview_question_workspace_session_id UNIQUE (id, workspace_id, session_id),
    CONSTRAINT uq_learning_interview_question_order UNIQUE (workspace_id, session_id, question_no, follow_up_no),
    CONSTRAINT fk_learning_interview_question_session
        FOREIGN KEY (session_id, workspace_id)
        REFERENCES learning.interview_session(session_id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_question_claim
        FOREIGN KEY (claim_id, workspace_id)
        REFERENCES core.claim(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_question_topic
        FOREIGN KEY (topic_id, workspace_id)
        REFERENCES core.topic(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_question_parent
        FOREIGN KEY (parent_question_id, workspace_id, session_id)
        REFERENCES learning.interview_question(id, workspace_id, session_id) ON DELETE RESTRICT,
    CONSTRAINT learning_interview_question_parent_check CHECK (
        (follow_up_no = 0 AND parent_question_id IS NULL)
        OR (follow_up_no > 0 AND parent_question_id IS NOT NULL)
    ),
    CONSTRAINT learning_interview_question_answer_time_check CHECK (
        (status = 'ANSWERED' AND answered_at IS NOT NULL)
        OR (status <> 'ANSWERED' AND answered_at IS NULL)
    )
);

CREATE TABLE IF NOT EXISTS learning.interview_turn (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    session_id uuid NOT NULL,
    question_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    user_answer text NOT NULL DEFAULT '' CHECK (octet_length(user_answer) <= 65536),
    score jsonb NOT NULL CHECK (jsonb_typeof(score) = 'object'),
    decision jsonb NOT NULL CHECK (jsonb_typeof(decision) = 'object'),
    scorer_version text NOT NULL CHECK (btrim(scorer_version) <> '' AND octet_length(scorer_version) <= 128),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_interview_turn_question UNIQUE (workspace_id, session_id, question_id),
    CONSTRAINT uq_learning_interview_turn_idempotency UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT fk_learning_interview_turn_session
        FOREIGN KEY (session_id, workspace_id)
        REFERENCES learning.interview_session(session_id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_turn_question
        FOREIGN KEY (question_id, workspace_id, session_id)
        REFERENCES learning.interview_question(id, workspace_id, session_id) ON DELETE RESTRICT
);

-- Report and Learning Path retain only a reference to the Artifact module's
-- immutable revision.  They never copy Artifact content or own a revision.
CREATE TABLE IF NOT EXISTS learning.interview_report (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    session_id uuid NOT NULL,
    domain_schema_version text NOT NULL CHECK (domain_schema_version = 'interview-report/v1'),
    report jsonb NOT NULL CHECK (jsonb_typeof(report) = 'object'),
    report_hash text NOT NULL CHECK (report_hash ~ '^[0-9a-f]{64}$'),
    artifact_id uuid NOT NULL,
    artifact_revision_id uuid NOT NULL,
    artifact_version bigint NOT NULL CHECK (artifact_version > 0),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_interview_report_session UNIQUE (workspace_id, session_id),
    CONSTRAINT uq_learning_interview_report_workspace_session_id UNIQUE (id, workspace_id, session_id),
    CONSTRAINT fk_learning_interview_report_session
        FOREIGN KEY (session_id, workspace_id)
        REFERENCES learning.interview_session(session_id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_report_artifact
        FOREIGN KEY (artifact_id, workspace_id)
        REFERENCES learning.artifact(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_report_artifact_revision
        FOREIGN KEY (artifact_revision_id, workspace_id, artifact_id)
        REFERENCES learning.artifact_revision(id, workspace_id, artifact_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS learning.interview_learning_path (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    session_id uuid NOT NULL,
    report_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    artifact_revision_id uuid NOT NULL,
    artifact_version bigint NOT NULL CHECK (artifact_version > 0),
    status text NOT NULL CHECK (status IN ('ACTIVE', 'PAUSED', 'COMPLETED')),
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_interview_learning_path_session UNIQUE (workspace_id, session_id),
    CONSTRAINT uq_learning_interview_learning_path_workspace_id UNIQUE (id, workspace_id),
    CONSTRAINT fk_learning_interview_learning_path_session
        FOREIGN KEY (session_id, workspace_id)
        REFERENCES learning.interview_session(session_id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_learning_path_report
        FOREIGN KEY (report_id, workspace_id, session_id)
        REFERENCES learning.interview_report(id, workspace_id, session_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_learning_path_artifact
        FOREIGN KEY (artifact_id, workspace_id)
        REFERENCES learning.artifact(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_learning_path_artifact_revision
        FOREIGN KEY (artifact_revision_id, workspace_id, artifact_id)
        REFERENCES learning.artifact_revision(id, workspace_id, artifact_id) ON DELETE RESTRICT,
    CONSTRAINT learning_interview_learning_path_time_order CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS learning.interview_learning_path_step (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    path_id uuid NOT NULL,
    step_no integer NOT NULL CHECK (step_no > 0),
    claim_id uuid NOT NULL,
    topic_id uuid,
    source_version_id uuid NOT NULL,
    source_span_id uuid NOT NULL,
    evidence_hash text NOT NULL CHECK (evidence_hash ~ '^[0-9a-f]{64}$'),
    title text NOT NULL CHECK (btrim(title) <> '' AND octet_length(title) <= 512),
    rationale text NOT NULL CHECK (btrim(rationale) <> '' AND octet_length(rationale) <= 4096),
    status text NOT NULL CHECK (status IN ('PENDING', 'IN_PROGRESS', 'COMPLETED', 'SKIPPED')),
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_interview_learning_path_step_order UNIQUE (workspace_id, path_id, step_no),
    CONSTRAINT uq_learning_interview_learning_path_step_workspace_id UNIQUE (id, workspace_id),
    CONSTRAINT fk_learning_interview_learning_path_step_path
        FOREIGN KEY (path_id, workspace_id)
        REFERENCES learning.interview_learning_path(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_learning_path_step_claim
        FOREIGN KEY (claim_id, workspace_id)
        REFERENCES core.claim(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_learning_path_step_topic
        FOREIGN KEY (topic_id, workspace_id)
        REFERENCES core.topic(id, workspace_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS learning.interview_command (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (command_type IN ('START', 'SUBMIT', 'COMPLETE', 'PATH_STEP', 'PATH_STATUS')),
    session_id uuid NOT NULL,
    response jsonb NOT NULL CHECK (jsonb_typeof(response) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key),
    CONSTRAINT fk_learning_interview_command_session
        FOREIGN KEY (session_id, workspace_id)
        REFERENCES learning.interview_session(session_id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_learning_interview_session_workspace_updated
    ON learning.interview_session (workspace_id, updated_at DESC, session_id DESC);
CREATE INDEX IF NOT EXISTS idx_learning_interview_question_session_order
    ON learning.interview_question (workspace_id, session_id, question_no, follow_up_no, id);
CREATE INDEX IF NOT EXISTS idx_learning_interview_turn_session_created
    ON learning.interview_turn (workspace_id, session_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_learning_interview_path_workspace_updated
    ON learning.interview_learning_path (workspace_id, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_learning_interview_path_step_path_order
    ON learning.interview_learning_path_step (workspace_id, path_id, step_no, id);
CREATE INDEX IF NOT EXISTS idx_learning_interview_command_session_created
    ON learning.interview_command (workspace_id, session_id, created_at DESC);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.assert_interview_session_shell()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    shell_type text;
    shell_config jsonb;
BEGIN
    SELECT session_type, config
      INTO shell_type, shell_config
      FROM learning.review_session
     WHERE id = NEW.session_id
       AND workspace_id = NEW.workspace_id;

    IF shell_type IS DISTINCT FROM 'INTERVIEW'
       OR shell_config ->> 'schema_version' IS DISTINCT FROM 'interview/v1' THEN
        RAISE EXCEPTION 'interview session must bind an interview review shell'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_interview_session_shell ON learning.interview_session;
CREATE TRIGGER trg_learning_interview_session_shell
    BEFORE INSERT OR UPDATE OF session_id, workspace_id ON learning.interview_session
    FOR EACH ROW EXECUTE FUNCTION learning.assert_interview_session_shell();

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

DROP TRIGGER IF EXISTS trg_learning_interview_report_artifact_type ON learning.interview_report;
CREATE TRIGGER trg_learning_interview_report_artifact_type
    BEFORE INSERT OR UPDATE OF artifact_id, artifact_revision_id, artifact_version, workspace_id ON learning.interview_report
    FOR EACH ROW EXECUTE FUNCTION learning.assert_interview_artifact_binding();

DROP TRIGGER IF EXISTS trg_learning_interview_learning_path_artifact_type ON learning.interview_learning_path;
CREATE TRIGGER trg_learning_interview_learning_path_artifact_type
    BEFORE INSERT OR UPDATE OF artifact_id, artifact_revision_id, artifact_version, workspace_id ON learning.interview_learning_path
    FOR EACH ROW EXECUTE FUNCTION learning.assert_interview_artifact_binding();

-- A path step is an actionable view of a scored gap, not an unverified pointer
-- to a Source. Its Claim/Evidence tuple must continue to name one immutable
-- SUPPORTS Claim Source in the same Workspace at the time the step is created.
-- Historical steps remain readable if the Claim later changes lifecycle state.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.assert_interview_path_step_evidence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM core.claim AS claim
          JOIN core.claim_source AS source
            ON source.workspace_id = claim.workspace_id
           AND source.claim_id = claim.id
         WHERE claim.workspace_id = NEW.workspace_id
           AND claim.id = NEW.claim_id
           AND claim.status = 'CONFIRMED'
           AND source.source_version_id = NEW.source_version_id
           AND source.source_span_id = NEW.source_span_id
           AND source.evidence_hash = NEW.evidence_hash
           AND source.support_type = 'SUPPORTS'
    ) THEN
        RAISE EXCEPTION 'learning path step evidence is not a confirmed claim support'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_interview_path_step_evidence ON learning.interview_learning_path_step;
CREATE TRIGGER trg_learning_interview_path_step_evidence
    BEFORE INSERT OR UPDATE OF workspace_id, claim_id, source_version_id, source_span_id, evidence_hash
    ON learning.interview_learning_path_step
    FOR EACH ROW EXECUTE FUNCTION learning.assert_interview_path_step_evidence();

-- A completed report is a one-time evidence-backed summary. Progress belongs
-- in interview_learning_path(_step), never in a rewritten report row.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.reject_interview_report_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'interview report is immutable' USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_interview_report_immutable ON learning.interview_report;
CREATE TRIGGER trg_learning_interview_report_immutable
    BEFORE UPDATE OR DELETE ON learning.interview_report
    FOR EACH ROW EXECUTE FUNCTION learning.reject_interview_report_mutation();

-- +goose Down

-- Historical interview, report and path facts must be archived explicitly; a
-- rollback must never silently drop a user answer or an Artifact binding.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.interview_command LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.interview_learning_path_step LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.interview_learning_path LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.interview_report LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.interview_turn LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.interview_question LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.interview_session LIMIT 1) THEN
        RAISE EXCEPTION 'interview history is non-empty; archive before rollback'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_interview_learning_path_artifact_type ON learning.interview_learning_path;
DROP TRIGGER IF EXISTS trg_learning_interview_report_artifact_type ON learning.interview_report;
DROP FUNCTION IF EXISTS learning.assert_interview_artifact_binding();
DROP TRIGGER IF EXISTS trg_learning_interview_path_step_evidence ON learning.interview_learning_path_step;
DROP FUNCTION IF EXISTS learning.assert_interview_path_step_evidence();
DROP TRIGGER IF EXISTS trg_learning_interview_report_immutable ON learning.interview_report;
DROP FUNCTION IF EXISTS learning.reject_interview_report_mutation();
DROP TRIGGER IF EXISTS trg_learning_interview_session_shell ON learning.interview_session;
DROP FUNCTION IF EXISTS learning.assert_interview_session_shell();
DROP INDEX IF EXISTS learning.idx_learning_interview_command_session_created;
DROP INDEX IF EXISTS learning.idx_learning_interview_path_step_path_order;
DROP INDEX IF EXISTS learning.idx_learning_interview_path_workspace_updated;
DROP INDEX IF EXISTS learning.idx_learning_interview_turn_session_created;
DROP INDEX IF EXISTS learning.idx_learning_interview_question_session_order;
DROP INDEX IF EXISTS learning.idx_learning_interview_session_workspace_updated;
DROP TABLE IF EXISTS learning.interview_command;
DROP TABLE IF EXISTS learning.interview_learning_path_step;
DROP TABLE IF EXISTS learning.interview_learning_path;
DROP TABLE IF EXISTS learning.interview_report;
DROP TABLE IF EXISTS learning.interview_turn;
DROP TABLE IF EXISTS learning.interview_question;
DROP TABLE IF EXISTS learning.interview_session;
