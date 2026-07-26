-- +goose Up

-- 00034 created a placeholder Artifact shape before the domain state machine
-- existed. Keep legacy rows intact, but mark them explicitly so the v1 adapter
-- never guesses how an old state maps to the immutable-revision lifecycle.
ALTER TABLE learning.artifact
    ADD COLUMN IF NOT EXISTS domain_schema_version text NOT NULL DEFAULT 'legacy/artifact-v0',
    ADD COLUMN IF NOT EXISTS scope_definition text,
    ADD COLUMN IF NOT EXISTS source_coverage jsonb,
    ADD COLUMN IF NOT EXISTS current_revision_id uuid;

ALTER TABLE learning.artifact_revision
    ADD COLUMN IF NOT EXISTS domain_schema_version text NOT NULL DEFAULT 'legacy/artifact-revision-v0',
    ADD COLUMN IF NOT EXISTS content_hash text,
    ADD COLUMN IF NOT EXISTS created_by_type text,
    ADD COLUMN IF NOT EXISTS generation_metadata jsonb;

-- Replace only the placeholder enum checks from 00034. The old values remain
-- valid for legacy rows while v1 rows are constrained by their schema marker.
-- +goose StatementBegin
DO $$
DECLARE
    constraint_name text;
BEGIN
    FOR constraint_name IN
        SELECT conname
        FROM pg_constraint
        WHERE conrelid = 'learning.artifact'::regclass
          AND contype = 'c'
          AND (
              pg_get_constraintdef(oid) LIKE '%artifact_type%'
              OR pg_get_constraintdef(oid) LIKE '%status%'
              OR pg_get_constraintdef(oid) LIKE '%domain_schema_version%'
          )
    LOOP
        EXECUTE format('ALTER TABLE learning.artifact DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END
$$;
-- +goose StatementEnd

ALTER TABLE learning.artifact
    ADD CONSTRAINT learning_artifact_type_check
        CHECK (btrim(artifact_type) <> '' AND octet_length(artifact_type) <= 128),
    ADD CONSTRAINT learning_artifact_status_check
        CHECK (status IN (
            'DRAFT', 'OUTLINE_APPROVED', 'GENERATING', 'READY', 'ARCHIVED',
            'PLANNING', 'OUTLINE_REVIEW', 'APPROVED', 'EXPORTED',
            'PUBLISH_PROPOSED', 'PUBLISHED'
        )),
    ADD CONSTRAINT learning_artifact_domain_schema_check
        CHECK (domain_schema_version IN ('legacy/artifact-v0', 'artifact/v1')),
    ADD CONSTRAINT learning_artifact_v1_shape_check
        CHECK (
            domain_schema_version <> 'artifact/v1'
            OR (
                status IN (
                    'PLANNING', 'OUTLINE_REVIEW', 'GENERATING', 'DRAFT', 'APPROVED',
                    'EXPORTED', 'PUBLISH_PROPOSED', 'PUBLISHED', 'ARCHIVED'
                )
                AND scope_definition IS NOT NULL
                AND btrim(scope_definition) <> ''
                AND octet_length(scope_definition) <= 16384
                AND source_coverage IS NOT NULL
                AND jsonb_typeof(source_coverage) = 'array'
                AND current_revision_id IS NOT NULL
            )
        );

-- +goose StatementBegin
DO $$
DECLARE
    constraint_name text;
BEGIN
    FOR constraint_name IN
        SELECT conname
        FROM pg_constraint
        WHERE conrelid = 'learning.artifact_revision'::regclass
          AND contype = 'c'
          AND (
              pg_get_constraintdef(oid) LIKE '%status%'
              OR pg_get_constraintdef(oid) LIKE '%domain_schema_version%'
              OR pg_get_constraintdef(oid) LIKE '%created_by_type%'
              OR pg_get_constraintdef(oid) LIKE '%content_hash%'
			  OR pg_get_constraintdef(oid) LIKE '%coverage%'
          )
    LOOP
        EXECUTE format('ALTER TABLE learning.artifact_revision DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END
$$;
-- +goose StatementEnd

ALTER TABLE learning.artifact_revision
    ADD CONSTRAINT learning_artifact_revision_status_check
        CHECK (status IN ('OUTLINE', 'DRAFT', 'REVIEWED', 'EXPORTED', 'PUBLISHED', 'SUPERSEDED', 'SNAPSHOT')),
    ADD CONSTRAINT learning_artifact_revision_domain_schema_check
        CHECK (domain_schema_version IN ('legacy/artifact-revision-v0', 'artifact-revision/v1')),
	ADD CONSTRAINT learning_artifact_revision_coverage_type_check
		CHECK (
			domain_schema_version <> 'artifact-revision/v1'
			OR jsonb_typeof(coverage) = 'array'
		),
    ADD CONSTRAINT learning_artifact_revision_v1_shape_check
        CHECK (
            domain_schema_version <> 'artifact-revision/v1'
            OR (
                status = 'SNAPSHOT'
				AND jsonb_typeof(coverage) = 'array'
                AND content_hash ~ '^[0-9a-f]{64}$'
                AND created_by_type IN ('HUMAN', 'AGENT')
                AND (
                    (created_by_type = 'HUMAN' AND generation_metadata IS NULL)
                    OR (created_by_type = 'AGENT' AND jsonb_typeof(generation_metadata) = 'object')
                )
            )
        );

-- The composite key makes the current revision pointer prove both the
-- Workspace and owning Artifact. It is deferred so a create transaction can
-- insert the Artifact and its first immutable Revision together.
ALTER TABLE learning.artifact_revision
    ADD CONSTRAINT uq_learning_artifact_revision_current_binding
        UNIQUE (id, workspace_id, artifact_id);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'learning.artifact'::regclass
          AND conname = 'fk_learning_artifact_current_revision_binding'
    ) THEN
        ALTER TABLE learning.artifact
            ADD CONSTRAINT fk_learning_artifact_current_revision_binding
            FOREIGN KEY (current_revision_id, workspace_id, id)
            REFERENCES learning.artifact_revision(id, workspace_id, artifact_id)
            ON DELETE RESTRICT
            DEFERRABLE INITIALLY DEFERRED;
    END IF;
END
$$;
-- +goose StatementEnd

-- Immutable Revision rows are never overwritten or deleted. Artifact state
-- changes point at a new row through optimistic locking instead.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.reject_artifact_v1_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.domain_schema_version = 'artifact-revision/v1' THEN
        RAISE EXCEPTION 'artifact v1 revisions are immutable'
            USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_artifact_revision_v1_immutable ON learning.artifact_revision;
CREATE TRIGGER trg_learning_artifact_revision_v1_immutable
    BEFORE UPDATE OR DELETE ON learning.artifact_revision
    FOR EACH ROW EXECUTE FUNCTION learning.reject_artifact_v1_revision_mutation();

CREATE TABLE IF NOT EXISTS learning.artifact_command (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    command_type text NOT NULL,
    artifact_id uuid NOT NULL,
    artifact_version bigint NOT NULL CHECK (artifact_version > 0),
    response jsonb NOT NULL CHECK (jsonb_typeof(response) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key),
    CONSTRAINT learning_artifact_command_hash_check
        CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT learning_artifact_command_text_check
        CHECK (
            btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128
            AND btrim(command_type) <> '' AND octet_length(command_type) <= 64
        ),
    CONSTRAINT fk_learning_artifact_command_artifact_workspace
        FOREIGN KEY (artifact_id, workspace_id)
        REFERENCES learning.artifact(id, workspace_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS learning.artifact_export (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    artifact_id uuid NOT NULL,
    revision_id uuid NOT NULL,
    artifact_version bigint NOT NULL CHECK (artifact_version > 0),
    revision_no integer NOT NULL CHECK (revision_no > 0),
    revision_hash text NOT NULL CHECK (revision_hash ~ '^[0-9a-f]{64}$'),
    output_path text NOT NULL,
    output_hash text NOT NULL CHECK (output_hash ~ '^[0-9a-f]{64}$'),
    output_size bigint NOT NULL CHECK (output_size >= 0),
    exported_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_artifact_export_path UNIQUE (workspace_id, output_path),
    CONSTRAINT learning_artifact_export_path_check
        CHECK (
            output_path ~ '^\.knowledge/exports/artifacts/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.md$'
        ),
    CONSTRAINT fk_learning_artifact_export_artifact_workspace
        FOREIGN KEY (artifact_id, workspace_id)
        REFERENCES learning.artifact(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_export_revision_binding
        FOREIGN KEY (revision_id, workspace_id, artifact_id)
        REFERENCES learning.artifact_revision(id, workspace_id, artifact_id) ON DELETE RESTRICT
);

-- Change Control owns Proposal creation. This binding is deliberately a
-- separate Artifact fact so the future typed Proposal migration can add its
-- cross-module FK without making Artifact invent a Document write path.
CREATE TABLE IF NOT EXISTS learning.artifact_publication (
    artifact_id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    revision_id uuid NOT NULL,
    artifact_version bigint NOT NULL CHECK (artifact_version > 0),
    revision_no integer NOT NULL CHECK (revision_no > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    proposal_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, artifact_id, revision_id),
    CONSTRAINT uq_learning_artifact_publication_key UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT uq_learning_artifact_publication_proposal UNIQUE (workspace_id, proposal_id),
    CONSTRAINT learning_artifact_publication_key_check
        CHECK (btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128),
    CONSTRAINT fk_learning_artifact_publication_artifact_workspace
        FOREIGN KEY (artifact_id, workspace_id)
        REFERENCES learning.artifact(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_publication_revision_binding
        FOREIGN KEY (revision_id, workspace_id, artifact_id)
        REFERENCES learning.artifact_revision(id, workspace_id, artifact_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_learning_artifact_v1_workspace_updated
    ON learning.artifact (workspace_id, updated_at DESC, id DESC)
    WHERE domain_schema_version = 'artifact/v1';
CREATE INDEX IF NOT EXISTS idx_learning_artifact_command_artifact
    ON learning.artifact_command (workspace_id, artifact_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_learning_artifact_export_artifact
    ON learning.artifact_export (workspace_id, artifact_id, exported_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_learning_artifact_publication_artifact
    ON learning.artifact_publication (workspace_id, artifact_id, created_at DESC);

-- +goose Down

-- New immutable revisions, receipts, exports and publication bindings cannot
-- be represented by 00034. Refuse to discard them during a downgrade.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.artifact WHERE domain_schema_version = 'artifact/v1')
       OR EXISTS (SELECT 1 FROM learning.artifact_revision WHERE domain_schema_version = 'artifact-revision/v1')
       OR EXISTS (SELECT 1 FROM learning.artifact_command LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.artifact_export LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.artifact_publication LIMIT 1) THEN
        RAISE EXCEPTION 'artifact v1 history exists; archive before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP INDEX IF EXISTS learning.idx_learning_artifact_publication_artifact;
DROP INDEX IF EXISTS learning.idx_learning_artifact_export_artifact;
DROP INDEX IF EXISTS learning.idx_learning_artifact_command_artifact;
DROP INDEX IF EXISTS learning.idx_learning_artifact_v1_workspace_updated;
DROP TABLE IF EXISTS learning.artifact_publication;
DROP TABLE IF EXISTS learning.artifact_export;
DROP TABLE IF EXISTS learning.artifact_command;
DROP TRIGGER IF EXISTS trg_learning_artifact_revision_v1_immutable ON learning.artifact_revision;
DROP FUNCTION IF EXISTS learning.reject_artifact_v1_revision_mutation();

ALTER TABLE learning.artifact
    DROP CONSTRAINT IF EXISTS fk_learning_artifact_current_revision_binding;
ALTER TABLE learning.artifact_revision
    DROP CONSTRAINT IF EXISTS uq_learning_artifact_revision_current_binding;

ALTER TABLE learning.artifact
    DROP CONSTRAINT IF EXISTS learning_artifact_v1_shape_check,
    DROP CONSTRAINT IF EXISTS learning_artifact_domain_schema_check,
    DROP CONSTRAINT IF EXISTS learning_artifact_status_check,
    DROP CONSTRAINT IF EXISTS learning_artifact_type_check,
    ADD CONSTRAINT learning_artifact_type_check
        CHECK (artifact_type IN ('INTERVIEW_DOC', 'OUTLINE', 'LEARNING_PATH', 'SUMMARY', 'CUSTOM')),
    ADD CONSTRAINT learning_artifact_status_check
        CHECK (status IN ('DRAFT', 'OUTLINE_APPROVED', 'GENERATING', 'READY', 'ARCHIVED')),
    DROP COLUMN IF EXISTS current_revision_id,
    DROP COLUMN IF EXISTS source_coverage,
    DROP COLUMN IF EXISTS scope_definition,
    DROP COLUMN IF EXISTS domain_schema_version;

ALTER TABLE learning.artifact_revision
    DROP CONSTRAINT IF EXISTS learning_artifact_revision_v1_shape_check,
	DROP CONSTRAINT IF EXISTS learning_artifact_revision_coverage_type_check,
    DROP CONSTRAINT IF EXISTS learning_artifact_revision_domain_schema_check,
    DROP CONSTRAINT IF EXISTS learning_artifact_revision_status_check,
    ADD CONSTRAINT learning_artifact_revision_status_check
        CHECK (status IN ('OUTLINE', 'DRAFT', 'REVIEWED', 'EXPORTED', 'PUBLISHED', 'SUPERSEDED')),
	ADD CONSTRAINT learning_artifact_revision_coverage_check
		CHECK (jsonb_typeof(coverage) = 'object'),
    DROP COLUMN IF EXISTS generation_metadata,
    DROP COLUMN IF EXISTS created_by_type,
    DROP COLUMN IF EXISTS content_hash,
    DROP COLUMN IF EXISTS domain_schema_version;
