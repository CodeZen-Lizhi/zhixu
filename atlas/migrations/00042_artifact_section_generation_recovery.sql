-- Preserve 00041 rows while expanding terminal outcomes. Failure details are
-- bounded, stable summaries copied from the Workflow terminal fact; raw model
-- or provider errors never belong in this receipt.
ALTER TABLE learning.artifact_section_generation
    ADD COLUMN IF NOT EXISTS failure_class text,
    ADD COLUMN IF NOT EXISTS error_code text,
    ADD COLUMN IF NOT EXISTS error_summary text,
    ADD COLUMN IF NOT EXISTS terminal_at timestamptz;

ALTER TABLE learning.artifact_section_generation
    DROP CONSTRAINT IF EXISTS artifact_section_generation_status_check,
    DROP CONSTRAINT IF EXISTS learning_artifact_section_generation_time_check,
    DROP CONSTRAINT IF EXISTS learning_artifact_section_generation_terminal_check,
    DROP CONSTRAINT IF EXISTS uq_learning_artifact_section_generation_source;

-- The 00041 trigger correctly makes completed rows immutable, so remove it
-- only inside this migration transaction while backfilling the newly added,
-- redundant terminal timestamp. It is recreated below before commit.
DROP TRIGGER IF EXISTS trg_learning_artifact_section_generation_guard
    ON learning.artifact_section_generation;

UPDATE learning.artifact_section_generation
   SET terminal_at = completed_at
 WHERE status = 'COMPLETED'
   AND terminal_at IS NULL;

ALTER TABLE learning.artifact_section_generation
    ADD CONSTRAINT artifact_section_generation_status_check CHECK (
        status IN ('PENDING', 'COMPLETED', 'FAILED', 'CANCELLED', 'RECOVERY_REQUIRED')
    ),
    ADD CONSTRAINT learning_artifact_section_generation_failure_class_check CHECK (
        failure_class IS NULL
        OR failure_class IN ('retryable', 'non_retryable', 'manual_recovery', 'lease_lost', 'cancelled')
    ),
    ADD CONSTRAINT learning_artifact_section_generation_error_code_check CHECK (
        error_code IS NULL
        OR (
            octet_length(error_code) BETWEEN 1 AND 128
            AND error_code ~ '^[A-Z0-9_]+$'
        )
    ),
    ADD CONSTRAINT learning_artifact_section_generation_error_summary_check CHECK (
        error_summary IS NULL
        OR (
            char_length(error_summary) BETWEEN 1 AND 256
            AND error_summary !~ '^[[:space:]]|[[:space:]]$'
            AND left(error_summary, 1) NOT IN (U&'\0085', U&'\00A0', U&'\2007', U&'\202F')
            AND right(error_summary, 1) NOT IN (U&'\0085', U&'\00A0', U&'\2007', U&'\202F')
        )
    ),
    ADD CONSTRAINT learning_artifact_section_generation_time_check CHECK (
        updated_at >= created_at
        AND (completed_at IS NULL OR completed_at = updated_at)
        AND (terminal_at IS NULL OR terminal_at = updated_at)
    ),
    ADD CONSTRAINT learning_artifact_section_generation_terminal_check CHECK (
        (
            status = 'PENDING'
            AND version = 1
            AND model_run_id IS NULL
            AND recorded_base_revision_id IS NULL
            AND recorded_revision_id IS NULL
            AND recorded_artifact_version IS NULL
            AND content_hash IS NULL
            AND failure_class IS NULL
            AND error_code IS NULL
            AND error_summary IS NULL
            AND completed_at IS NULL
            AND terminal_at IS NULL
        )
        OR (
            status = 'COMPLETED'
            AND version = 2
            AND model_run_id IS NOT NULL
            AND recorded_base_revision_id IS NOT NULL
            AND recorded_revision_id IS NOT NULL
            AND recorded_artifact_version IS NOT NULL
            AND recorded_artifact_version > source_artifact_version
            AND content_hash IS NOT NULL
            AND failure_class IS NULL
            AND error_code IS NULL
            AND error_summary IS NULL
            AND completed_at IS NOT NULL
            AND terminal_at IS NOT NULL
            AND completed_at = updated_at
            AND terminal_at = updated_at
        )
        OR (
            status = 'FAILED'
            AND version = 2
            AND recorded_base_revision_id IS NULL
            AND recorded_revision_id IS NULL
            AND recorded_artifact_version IS NULL
            AND content_hash IS NULL
            AND failure_class IS NOT NULL
            AND failure_class = 'non_retryable'
            AND error_code IS NOT NULL
            AND error_summary IS NOT NULL
            AND completed_at IS NULL
            AND terminal_at IS NOT NULL
            AND terminal_at = updated_at
        )
        OR (
            status = 'CANCELLED'
            AND version = 2
            AND recorded_base_revision_id IS NULL
            AND recorded_revision_id IS NULL
            AND recorded_artifact_version IS NULL
            AND content_hash IS NULL
            AND failure_class IS NOT NULL
            AND failure_class = 'cancelled'
            AND error_code IS NOT NULL
            AND error_summary IS NOT NULL
            AND completed_at IS NULL
            AND terminal_at IS NOT NULL
            AND terminal_at = updated_at
        )
        OR (
            status = 'RECOVERY_REQUIRED'
            AND version = 2
            AND recorded_base_revision_id IS NULL
            AND recorded_revision_id IS NULL
            AND recorded_artifact_version IS NULL
            AND content_hash IS NULL
            AND failure_class IS NOT NULL
            AND failure_class = 'manual_recovery'
            AND error_code IS NOT NULL
            AND error_summary IS NOT NULL
            AND completed_at IS NULL
            AND terminal_at IS NOT NULL
            AND terminal_at = updated_at
        )
    );

-- Safe failures and cancellations release the Artifact section for a new
-- idempotency key. A pending or unknown outcome remains the sole owner even
-- when another section advances the current Artifact Revision.
CREATE UNIQUE INDEX IF NOT EXISTS uq_learning_artifact_section_generation_active_section
    ON learning.artifact_section_generation (
        workspace_id, artifact_id, section_key
    )
    WHERE status IN ('PENDING', 'RECOVERY_REQUIRED');

CREATE OR REPLACE FUNCTION learning.guard_artifact_section_generation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_revision_no integer;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'artifact section generation is append-only'
            USING ERRCODE = '55000';
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'PENDING' OR NEW.version <> 1 THEN
            RAISE EXCEPTION 'artifact section generation must start pending at version one'
                USING ERRCODE = '23514';
        END IF;
        SELECT revision_no
          INTO actual_revision_no
          FROM learning.artifact_revision
         WHERE id = NEW.source_revision_id
           AND workspace_id = NEW.workspace_id
           AND artifact_id = NEW.artifact_id;
        IF actual_revision_no IS NULL OR actual_revision_no <> NEW.source_revision_no THEN
            RAISE EXCEPTION 'artifact section generation source revision binding is invalid'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.status <> 'PENDING'
       OR OLD.version <> 1
       OR NEW.status NOT IN ('COMPLETED', 'FAILED', 'CANCELLED', 'RECOVERY_REQUIRED')
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.artifact_id IS DISTINCT FROM OLD.artifact_id
       OR NEW.source_revision_id IS DISTINCT FROM OLD.source_revision_id
       OR NEW.source_revision_no IS DISTINCT FROM OLD.source_revision_no
       OR NEW.source_artifact_version IS DISTINCT FROM OLD.source_artifact_version
       OR NEW.section_key IS DISTINCT FROM OLD.section_key
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version <> 2
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'artifact section generation transition is invalid'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

-- Reinstall the append-only guard after the compatibility backfill and bind
-- it to the expanded terminal transition function before the migration commits.
DROP TRIGGER IF EXISTS trg_learning_artifact_section_generation_guard
    ON learning.artifact_section_generation;
CREATE TRIGGER trg_learning_artifact_section_generation_guard
    BEFORE INSERT OR UPDATE OR DELETE ON learning.artifact_section_generation
    FOR EACH ROW EXECUTE FUNCTION learning.guard_artifact_section_generation_mutation();
