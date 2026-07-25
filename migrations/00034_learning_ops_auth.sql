-- +goose Up

CREATE SCHEMA IF NOT EXISTS learning;
CREATE SCHEMA IF NOT EXISTS ops;
CREATE SCHEMA IF NOT EXISTS auth;

-- Formal Document identity and immutable Article revisions.
CREATE TABLE IF NOT EXISTS core.document (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    canonical_path text NOT NULL CHECK (
        btrim(canonical_path) <> ''
        AND left(canonical_path, 1) <> '/'
        AND canonical_path !~ '(^|/)\.\.(/|$)'
        AND position(E'\\' in canonical_path) = 0
    ),
    title text NOT NULL CHECK (btrim(title) <> '' AND octet_length(title) <= 512),
    lifecycle_status text NOT NULL CHECK (lifecycle_status IN ('DRAFT', 'PUBLISHED', 'ARCHIVED', 'DELETED')),
    current_published_revision_id uuid,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_core_document_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_core_document_workspace_path UNIQUE (workspace_id, canonical_path),
    CONSTRAINT core_document_time_order CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS core.article_revision (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    document_id uuid NOT NULL,
    source_version_id uuid REFERENCES core.source_version(id) ON DELETE RESTRICT,
    parent_revision_id uuid,
    revision_no integer NOT NULL CHECK (revision_no > 0),
    content text NOT NULL CHECK (btrim(content) <> ''),
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    status text NOT NULL CHECK (status IN ('DRAFT', 'REVIEW', 'APPROVED', 'PUBLISHED', 'SUPERSEDED', 'ARCHIVED')),
    optimization_mode text NOT NULL DEFAULT 'NONE' CHECK (optimization_mode IN ('NONE', 'CLARITY', 'STRUCTURE', 'COMPLETENESS')),
    git_commit text CHECK (git_commit IS NULL OR git_commit ~ '^[0-9a-f]{40,64}$'),
    created_by_type text NOT NULL CHECK (created_by_type IN ('USER', 'AGENT', 'SYSTEM')),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_core_article_revision_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_core_article_revision_no UNIQUE (document_id, revision_no),
    CONSTRAINT fk_core_article_revision_document_workspace
        FOREIGN KEY (document_id, workspace_id)
        REFERENCES core.document(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_core_article_revision_parent_workspace
        FOREIGN KEY (parent_revision_id, workspace_id)
        REFERENCES core.article_revision(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT core_article_revision_publish_binding CHECK (
        status <> 'PUBLISHED' OR git_commit IS NOT NULL
    )
);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'fk_core_document_current_revision_workspace'
          AND conrelid = 'core.document'::regclass
    ) THEN
        ALTER TABLE core.document
            ADD CONSTRAINT fk_core_document_current_revision_workspace
            FOREIGN KEY (current_published_revision_id, workspace_id)
            REFERENCES core.article_revision(id, workspace_id) ON DELETE RESTRICT;
    END IF;
END
$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX IF NOT EXISTS uq_core_document_published_revision
    ON core.article_revision (document_id)
    WHERE status = 'PUBLISHED';
CREATE INDEX IF NOT EXISTS idx_core_document_workspace_updated
    ON core.document (workspace_id, updated_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_core_article_revision_workspace_created
    ON core.article_revision (workspace_id, document_id, created_at DESC, id);

-- Artifact drafts stay outside the formal knowledge graph until a Proposal is approved.
CREATE TABLE IF NOT EXISTS learning.artifact (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    artifact_type text NOT NULL CHECK (artifact_type IN ('INTERVIEW_DOC', 'OUTLINE', 'LEARNING_PATH', 'SUMMARY', 'CUSTOM')),
    title text NOT NULL CHECK (btrim(title) <> '' AND octet_length(title) <= 512),
    scope jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(scope) = 'object'),
    status text NOT NULL CHECK (status IN ('DRAFT', 'OUTLINE_APPROVED', 'GENERATING', 'READY', 'ARCHIVED')),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_artifact_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT learning_artifact_time_order CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS learning.artifact_revision (
    id uuid PRIMARY KEY,
    artifact_id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    revision_no integer NOT NULL CHECK (revision_no > 0),
    status text NOT NULL CHECK (status IN ('OUTLINE', 'DRAFT', 'REVIEWED', 'EXPORTED', 'PUBLISHED', 'SUPERSEDED')),
    outline jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(outline) = 'array'),
    sections jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(sections) = 'array'),
    coverage jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(coverage) = 'object'),
    missing jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(missing) = 'array'),
    conflicts jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(conflicts) = 'array'),
    content_markdown text NOT NULL DEFAULT '',
    provenance jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(provenance) = 'object'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_artifact_revision_no UNIQUE (artifact_id, revision_no),
    CONSTRAINT uq_learning_artifact_revision_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT fk_learning_artifact_revision_artifact_workspace
        FOREIGN KEY (artifact_id, workspace_id)
        REFERENCES learning.artifact(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_learning_artifact_workspace_updated
    ON learning.artifact (workspace_id, updated_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_learning_artifact_revision_workspace_created
    ON learning.artifact_revision (workspace_id, artifact_id, created_at DESC, id);

-- Review/FSRS facts.
CREATE TABLE IF NOT EXISTS learning.review_deck (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (btrim(name) <> '' AND octet_length(name) <= 256),
    scope jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(scope) = 'object'),
    status text NOT NULL CHECK (status IN ('ACTIVE', 'PAUSED', 'ARCHIVED')),
    daily_limit integer NOT NULL DEFAULT 20 CHECK (daily_limit > 0 AND daily_limit <= 1000),
    scheduler_version text NOT NULL CHECK (btrim(scheduler_version) <> ''),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_review_deck_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_learning_review_deck_name UNIQUE (workspace_id, name)
);

CREATE TABLE IF NOT EXISTS learning.review_card (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    deck_id uuid NOT NULL,
    claim_id uuid,
    question text NOT NULL CHECK (btrim(question) <> '' AND octet_length(question) <= 8192),
    answer_points jsonb NOT NULL CHECK (jsonb_typeof(answer_points) = 'array'),
    evidence jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'array'),
    card_type text NOT NULL CHECK (card_type IN ('SHORT_ANSWER', 'CLOZE', 'COMPARISON', 'SCENARIO', 'CODE_READING', 'DESIGN')),
    difficulty numeric(4,3) NOT NULL DEFAULT 0.5 CHECK (difficulty >= 0 AND difficulty <= 1),
    status text NOT NULL CHECK (status IN ('DRAFT', 'APPROVED', 'INVALIDATED', 'REJECTED')),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    model_version text NOT NULL DEFAULT 'manual',
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_review_card_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_learning_review_card_fingerprint UNIQUE (workspace_id, fingerprint),
    CONSTRAINT fk_learning_review_card_deck_workspace
        FOREIGN KEY (deck_id, workspace_id)
        REFERENCES learning.review_deck(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_review_card_claim_workspace
        FOREIGN KEY (claim_id, workspace_id)
        REFERENCES core.claim(id, workspace_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS learning.review_schedule (
    card_id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    due_at timestamptz NOT NULL,
    interval_days numeric(12,4) NOT NULL DEFAULT 0 CHECK (interval_days >= 0),
    stability numeric(12,4) NOT NULL DEFAULT 0 CHECK (stability >= 0),
    difficulty numeric(4,3) NOT NULL DEFAULT 0.5 CHECK (difficulty >= 0 AND difficulty <= 1),
    last_reviewed_at timestamptz,
    scheduler_version text NOT NULL,
    paused boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CONSTRAINT fk_learning_review_schedule_card_workspace
        FOREIGN KEY (card_id, workspace_id)
        REFERENCES learning.review_card(id, workspace_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS learning.review_session (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    deck_id uuid,
    session_type text NOT NULL CHECK (session_type IN ('REVIEW', 'INTERVIEW')),
    status text NOT NULL CHECK (status IN ('ACTIVE', 'COMPLETED', 'CANCELLED')),
    config jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> ''),
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    CONSTRAINT uq_learning_review_session_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_learning_review_session_key UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT fk_learning_review_session_deck_workspace
        FOREIGN KEY (deck_id, workspace_id)
        REFERENCES learning.review_deck(id, workspace_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS learning.review_answer (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    session_id uuid NOT NULL,
    card_id uuid,
    question_ref text NOT NULL CHECK (btrim(question_ref) <> ''),
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> ''),
    user_answer text NOT NULL DEFAULT '',
    score jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(score) = 'object'),
    feedback jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(feedback) = 'object'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_review_answer_key UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT fk_learning_review_answer_session_workspace
        FOREIGN KEY (session_id, workspace_id)
        REFERENCES learning.review_session(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_review_answer_card_workspace
        FOREIGN KEY (card_id, workspace_id)
        REFERENCES learning.review_card(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_learning_review_due
    ON learning.review_schedule (workspace_id, paused, due_at, card_id);

CREATE TABLE IF NOT EXISTS learning.memory (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    memory_type text NOT NULL CHECK (memory_type IN ('PREFERENCE', 'EPISODIC', 'GOAL', 'FEEDBACK')),
    content jsonb NOT NULL CHECK (jsonb_typeof(content) = 'object'),
    status text NOT NULL CHECK (status IN ('CANDIDATE', 'CONFIRMED', 'REVOKED', 'EXPIRED')),
    source_ref text NOT NULL CHECK (btrim(source_ref) <> ''),
    expires_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_memory_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT learning_memory_time_order CHECK (updated_at >= created_at)
);

-- User-facing timeline, impact, audit and export facts.
CREATE TABLE IF NOT EXISTS ops.knowledge_event (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    event_type text NOT NULL CHECK (btrim(event_type) <> '' AND octet_length(event_type) <= 128),
    aggregate_type text NOT NULL CHECK (btrim(aggregate_type) <> '' AND octet_length(aggregate_type) <= 128),
    aggregate_id uuid,
    source_ref text,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object'),
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_ops_knowledge_event_id_workspace UNIQUE (id, workspace_id)
);

CREATE TABLE IF NOT EXISTS ops.impact_report (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    source_event_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN ('READY', 'STALE', 'FAILED')),
    objects jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(objects) = 'array'),
    summary jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(summary) = 'object'),
    generated_at timestamptz NOT NULL,
    CONSTRAINT uq_ops_impact_report_source UNIQUE (workspace_id, source_event_id),
    CONSTRAINT fk_ops_impact_report_event_workspace
        FOREIGN KEY (source_event_id, workspace_id)
        REFERENCES ops.knowledge_event(id, workspace_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS ops.audit_event (
    id uuid PRIMARY KEY,
    workspace_id uuid REFERENCES core.workspace(id) ON DELETE RESTRICT,
    actor_type text NOT NULL CHECK (btrim(actor_type) <> ''),
    actor_ref text,
    action text NOT NULL CHECK (btrim(action) <> ''),
    resource_type text,
    resource_ref text,
    idempotency_key text,
    correlation jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(correlation) = 'object'),
    payload jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object'),
    occurred_at timestamptz NOT NULL,
    CONSTRAINT uq_ops_audit_idempotency UNIQUE (workspace_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS ops.export_job (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    kind text NOT NULL CHECK (kind IN ('MARKDOWN', 'METADATA_JSON', 'EVALUATION_JSON', 'AUDIT_JSON')),
    schema_version text NOT NULL CHECK (btrim(schema_version) <> ''),
    query_definition jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(query_definition) = 'object'),
    fields jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(fields) = 'array'),
    status text NOT NULL CHECK (status IN ('PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED', 'EXPIRED', 'CANCELLED')),
    file_path text,
    file_hash text CHECK (file_hash IS NULL OR file_hash ~ '^[0-9a-f]{64}$'),
    error_code text,
    idempotency_key text NOT NULL,
    expires_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_ops_export_job_key UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT ops_export_job_success_binding CHECK (
        (status = 'SUCCEEDED' AND file_path IS NOT NULL AND file_hash IS NOT NULL)
        OR (status <> 'SUCCEEDED')
    )
);

CREATE INDEX IF NOT EXISTS idx_ops_knowledge_event_workspace_time
    ON ops.knowledge_event (workspace_id, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_ops_audit_workspace_time
    ON ops.audit_event (workspace_id, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_ops_export_workspace_status
    ON ops.export_job (workspace_id, status, created_at DESC, id);

-- Session and API token hashes only; plaintext credentials never persist.
CREATE TABLE IF NOT EXISTS auth.session (
    id uuid PRIMARY KEY,
    token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    csrf_hash text NOT NULL CHECK (csrf_hash ~ '^[0-9a-f]{64}$'),
    user_label text NOT NULL DEFAULT 'owner' CHECK (btrim(user_label) <> ''),
    scopes jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(scopes) = 'array'),
    created_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CONSTRAINT auth_session_distinct_credential_hashes CHECK (token_hash <> csrf_hash),
    CONSTRAINT auth_session_expiry_order CHECK (expires_at > created_at),
    CONSTRAINT auth_session_last_seen_order CHECK (last_seen_at >= created_at),
    CONSTRAINT auth_session_revoked_order CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE TABLE IF NOT EXISTS auth.api_token (
    id uuid PRIMARY KEY,
    token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    name text NOT NULL CHECK (btrim(name) <> '' AND octet_length(name) <= 256),
    scopes jsonb NOT NULL CHECK (jsonb_typeof(scopes) = 'array'),
    created_at timestamptz NOT NULL,
    last_used_at timestamptz,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CONSTRAINT auth_api_token_expiry_order CHECK (expires_at > created_at),
    CONSTRAINT auth_api_token_last_used_order CHECK (last_used_at IS NULL OR last_used_at >= created_at),
    CONSTRAINT auth_api_token_revoked_order CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

-- Existing installations may have created the tables from an earlier draft;
-- add the temporal checks idempotently so a repeated Up still converges.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('auth.session') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_session_distinct_credential_hashes'
          AND conrelid = 'auth.session'::regclass
    ) THEN
        ALTER TABLE auth.session
            ADD CONSTRAINT auth_session_distinct_credential_hashes CHECK (token_hash <> csrf_hash);
    END IF;
    IF to_regclass('auth.session') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_session_last_seen_order'
          AND conrelid = 'auth.session'::regclass
    ) THEN
        ALTER TABLE auth.session
            ADD CONSTRAINT auth_session_last_seen_order CHECK (last_seen_at >= created_at);
    END IF;
    IF to_regclass('auth.session') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_session_revoked_order'
          AND conrelid = 'auth.session'::regclass
    ) THEN
        ALTER TABLE auth.session
            ADD CONSTRAINT auth_session_revoked_order CHECK (revoked_at IS NULL OR revoked_at >= created_at);
    END IF;
    IF to_regclass('auth.api_token') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_api_token_last_used_order'
          AND conrelid = 'auth.api_token'::regclass
    ) THEN
        ALTER TABLE auth.api_token
            ADD CONSTRAINT auth_api_token_last_used_order CHECK (last_used_at IS NULL OR last_used_at >= created_at);
    END IF;
    IF to_regclass('auth.api_token') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_api_token_revoked_order'
          AND conrelid = 'auth.api_token'::regclass
    ) THEN
        ALTER TABLE auth.api_token
            ADD CONSTRAINT auth_api_token_revoked_order CHECK (revoked_at IS NULL OR revoked_at >= created_at);
    END IF;
END
$$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS idx_auth_session_active
    ON auth.session (expires_at, revoked_at);
CREATE INDEX IF NOT EXISTS idx_auth_api_token_active
    ON auth.api_token (expires_at, revoked_at);
CREATE INDEX IF NOT EXISTS idx_auth_api_token_created
    ON auth.api_token (created_at DESC, id DESC);

-- Append-only facts cannot be silently edited or deleted.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.reject_append_only_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME
        USING ERRCODE = '55000', HINT = 'create a corrective event instead';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS knowledge_event_append_only ON ops.knowledge_event;
CREATE TRIGGER knowledge_event_append_only
    BEFORE UPDATE OR DELETE ON ops.knowledge_event
    FOR EACH ROW EXECUTE FUNCTION ops.reject_append_only_mutation();
DROP TRIGGER IF EXISTS audit_event_append_only ON ops.audit_event;
CREATE TRIGGER audit_event_append_only
    BEFORE UPDATE OR DELETE ON ops.audit_event
    FOR EACH ROW EXECUTE FUNCTION ops.reject_append_only_mutation();

-- +goose Down

-- Refuse to silently destroy authentication, learning, or operational facts.
-- ACCESS EXCLUSIVE keeps the emptiness check true until all owned tables have
-- been removed by this same migration transaction.
LOCK TABLE
    auth.api_token,
    auth.session,
    ops.export_job,
    ops.audit_event,
    ops.impact_report,
    ops.knowledge_event,
    learning.memory,
    learning.review_answer,
    learning.review_session,
    learning.review_schedule,
    learning.review_card,
    learning.review_deck,
    learning.artifact_revision,
    learning.artifact,
    core.article_revision,
    core.document
IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM auth.api_token LIMIT 1)
       OR EXISTS (SELECT 1 FROM auth.session LIMIT 1)
       OR EXISTS (SELECT 1 FROM ops.export_job LIMIT 1)
       OR EXISTS (SELECT 1 FROM ops.audit_event LIMIT 1)
       OR EXISTS (SELECT 1 FROM ops.impact_report LIMIT 1)
       OR EXISTS (SELECT 1 FROM ops.knowledge_event LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.memory LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.review_answer LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.review_session LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.review_schedule LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.review_card LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.review_deck LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.artifact_revision LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.artifact LIMIT 1)
       OR EXISTS (SELECT 1 FROM core.article_revision LIMIT 1)
       OR EXISTS (SELECT 1 FROM core.document LIMIT 1) THEN
        RAISE EXCEPTION 'learning, ops, or auth data is non-empty; archive before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS audit_event_append_only ON ops.audit_event;
DROP TRIGGER IF EXISTS knowledge_event_append_only ON ops.knowledge_event;
DROP FUNCTION IF EXISTS ops.reject_append_only_mutation();
DROP TABLE IF EXISTS auth.api_token;
DROP TABLE IF EXISTS auth.session;
DROP TABLE IF EXISTS ops.export_job;
DROP TABLE IF EXISTS ops.audit_event;
DROP TABLE IF EXISTS ops.impact_report;
DROP TABLE IF EXISTS ops.knowledge_event;
DROP TABLE IF EXISTS learning.memory;
DROP TABLE IF EXISTS learning.review_answer;
DROP TABLE IF EXISTS learning.review_session;
DROP TABLE IF EXISTS learning.review_schedule;
DROP TABLE IF EXISTS learning.review_card;
DROP TABLE IF EXISTS learning.review_deck;
DROP TABLE IF EXISTS learning.artifact_revision;
DROP TABLE IF EXISTS learning.artifact;
ALTER TABLE IF EXISTS core.document
    DROP CONSTRAINT IF EXISTS fk_core_document_current_revision_workspace;
DROP TABLE IF EXISTS core.article_revision;
DROP TABLE IF EXISTS core.document;
