-- +goose Up
-- M5-04D 只做前向扩展：旧 00009 已应用的数据保持可读，新 Saga 通过检查点触发器收紧契约。
ALTER TABLE change_control.writeback_execution
    ADD COLUMN IF NOT EXISTS file_byte_size bigint,
    ADD COLUMN IF NOT EXISTS file_mode integer,
    ADD COLUMN IF NOT EXISTS file_lock_token text,
    ADD COLUMN IF NOT EXISTS file_result_lock_token text,
    ADD COLUMN IF NOT EXISTS file_backup_lock_token text,
    ADD COLUMN IF NOT EXISTS base_blob_id text,
    ADD COLUMN IF NOT EXISTS result_blob_id text,
    ADD COLUMN IF NOT EXISTS base_mode text,
    ADD COLUMN IF NOT EXISTS cleanup_completed_at timestamptz;

ALTER TABLE change_control.writeback_execution
    DROP CONSTRAINT IF EXISTS writeback_execution_status_check,
    ADD CONSTRAINT writeback_execution_status_check CHECK (status IN (
        'prepared','file_prepared','file_applied','git_prepared','git_committed','verifying',
        'needs_revision','apply_failed','compensating_file','compensated','manual_recovery_required',
        'publish_recovery_required','verify_failed','rolled_back','completed'
    )),
    DROP CONSTRAINT IF EXISTS writeback_file_size_valid,
    ADD CONSTRAINT writeback_file_size_valid CHECK (file_byte_size IS NULL OR (file_byte_size > 0 AND file_byte_size <= 10485760)),
    DROP CONSTRAINT IF EXISTS writeback_file_mode_valid,
    ADD CONSTRAINT writeback_file_mode_valid CHECK (file_mode IS NULL OR (file_mode >= 0 AND file_mode <= 4095)),
    DROP CONSTRAINT IF EXISTS writeback_file_lock_token_valid,
    ADD CONSTRAINT writeback_file_lock_token_valid CHECK (file_lock_token IS NULL OR file_lock_token ~ '^[0-9a-f]{64}$'),
    DROP CONSTRAINT IF EXISTS writeback_file_managed_lock_tokens_valid,
    ADD CONSTRAINT writeback_file_managed_lock_tokens_valid CHECK (
        (file_result_lock_token IS NULL AND file_backup_lock_token IS NULL)
        OR (file_lock_token IS NOT NULL AND file_result_lock_token IS NOT NULL AND file_backup_lock_token IS NOT NULL
            AND file_result_lock_token ~ '^[0-9a-f]{64}$' AND file_backup_lock_token ~ '^[0-9a-f]{64}$'
            AND file_result_lock_token <> file_backup_lock_token
            AND file_result_lock_token <> file_lock_token AND file_backup_lock_token <> file_lock_token)
    ),
    DROP CONSTRAINT IF EXISTS writeback_git_object_ids_valid,
    ADD CONSTRAINT writeback_git_object_ids_valid CHECK (
        (base_blob_id IS NULL OR base_blob_id ~ '^[0-9a-f]{40}$' OR base_blob_id ~ '^[0-9a-f]{64}$')
        AND (result_blob_id IS NULL OR result_blob_id ~ '^[0-9a-f]{40}$' OR result_blob_id ~ '^[0-9a-f]{64}$')
        AND (base_mode IS NULL OR base_mode IN ('100644','100755'))
    ),
    DROP CONSTRAINT IF EXISTS writeback_cleanup_time_valid,
    ADD CONSTRAINT writeback_cleanup_time_valid CHECK (cleanup_completed_at IS NULL OR cleanup_completed_at >= created_at),
    DROP CONSTRAINT IF EXISTS writeback_cleanup_status_valid,
    ADD CONSTRAINT writeback_cleanup_status_valid CHECK (
        cleanup_completed_at IS NULL OR status IN ('verifying','verify_failed','rolled_back','completed')
    );

ALTER TABLE change_control.writeback_execution
    DROP CONSTRAINT IF EXISTS writeback_execution_git_fields,
    ADD CONSTRAINT writeback_execution_git_fields CHECK (
        (
            status IN ('git_prepared','git_committed','verifying','publish_recovery_required','verify_failed','rolled_back','completed')
            AND diff_hash IS NOT NULL
        ) OR (
            status IN ('prepared','file_prepared','file_applied','needs_revision','apply_failed')
            AND diff_hash IS NULL
        ) OR status IN ('compensating_file','compensated','manual_recovery_required')
    ),
    DROP CONSTRAINT IF EXISTS writeback_execution_git_result,
    ADD CONSTRAINT writeback_execution_git_result CHECK (
        status IN ('git_committed','verifying','publish_recovery_required','verify_failed','rolled_back','completed')
        AND git_commit IS NOT NULL AND parent_git_commit IS NOT NULL
        OR status NOT IN ('git_committed','verifying','publish_recovery_required','verify_failed','rolled_back','completed')
        AND git_commit IS NULL AND parent_git_commit IS NULL
    ),
    DROP CONSTRAINT IF EXISTS writeback_file_prepared_fields,
    ADD CONSTRAINT writeback_file_prepared_fields CHECK (
        status <> 'file_prepared' OR (
            temporary_ref IS NOT NULL AND backup_ref IS NOT NULL AND file_byte_size IS NOT NULL
            AND file_mode IS NOT NULL AND file_lock_token IS NOT NULL
            AND file_result_lock_token IS NOT NULL AND file_backup_lock_token IS NOT NULL
        )
    ),
    DROP CONSTRAINT IF EXISTS writeback_git_prepared_fields,
    ADD CONSTRAINT writeback_git_prepared_fields CHECK (
        (base_blob_id IS NULL AND result_blob_id IS NULL AND base_mode IS NULL)
        OR (base_blob_id IS NOT NULL AND result_blob_id IS NOT NULL AND base_mode IS NOT NULL
            AND length(base_blob_id) = length(approved_git_head) AND length(result_blob_id) = length(approved_git_head))
    ),
    DROP CONSTRAINT IF EXISTS writeback_git_intent_status_valid,
    ADD CONSTRAINT writeback_git_intent_status_valid CHECK (
        base_blob_id IS NULL OR status IN (
            'git_prepared','git_committed','verifying','publish_recovery_required',
            'compensating_file','compensated','manual_recovery_required',
            'verify_failed','rolled_back','completed'
        )
    ),
    DROP CONSTRAINT IF EXISTS writeback_git_prepared_status_fields,
    ADD CONSTRAINT writeback_git_prepared_status_fields CHECK (
        status <> 'git_prepared' OR (base_blob_id IS NOT NULL AND result_blob_id IS NOT NULL AND base_mode IS NOT NULL AND diff_hash IS NOT NULL)
    );

CREATE OR REPLACE FUNCTION change_control.validate_writeback_execution_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.workflow_run_id <> OLD.workflow_run_id OR NEW.node_run_id <> OLD.node_run_id
       OR NEW.proposal_id <> OLD.proposal_id OR NEW.revision_id <> OLD.revision_id OR NEW.approval_id <> OLD.approval_id
       OR NEW.write_authorization_id <> OLD.write_authorization_id OR NEW.git_authorization_id <> OLD.git_authorization_id
       OR NEW.target_path <> OLD.target_path OR NEW.base_hash <> OLD.base_hash OR NEW.result_hash <> OLD.result_hash
       OR NEW.approved_change_hash <> OLD.approved_change_hash OR NEW.approved_git_head <> OLD.approved_git_head
       OR NEW.idempotency_key <> OLD.idempotency_key OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'writeback immutable field or version violation' USING ERRCODE = '23514';
    END IF;

    IF NOT (
        (OLD.status = 'verifying' AND NEW.status = 'verifying'
         AND OLD.cleanup_completed_at IS NULL AND NEW.cleanup_completed_at IS NOT NULL
         AND NEW.failure_code IS NOT DISTINCT FROM OLD.failure_code
         AND NEW.manual_recovery_required = OLD.manual_recovery_required
         AND NEW.temporary_ref IS NOT DISTINCT FROM OLD.temporary_ref
         AND NEW.backup_ref IS NOT DISTINCT FROM OLD.backup_ref
         AND NEW.file_byte_size IS NOT DISTINCT FROM OLD.file_byte_size
         AND NEW.file_mode IS NOT DISTINCT FROM OLD.file_mode
         AND NEW.file_lock_token IS NOT DISTINCT FROM OLD.file_lock_token
         AND NEW.file_result_lock_token IS NOT DISTINCT FROM OLD.file_result_lock_token
         AND NEW.file_backup_lock_token IS NOT DISTINCT FROM OLD.file_backup_lock_token
         AND NEW.base_blob_id IS NOT DISTINCT FROM OLD.base_blob_id
         AND NEW.result_blob_id IS NOT DISTINCT FROM OLD.result_blob_id
         AND NEW.base_mode IS NOT DISTINCT FROM OLD.base_mode
         AND NEW.git_commit IS NOT DISTINCT FROM OLD.git_commit
         AND NEW.parent_git_commit IS NOT DISTINCT FROM OLD.parent_git_commit
         AND NEW.diff_hash IS NOT DISTINCT FROM OLD.diff_hash
         AND NEW.completed_at IS NOT DISTINCT FROM OLD.completed_at)
        OR
        (OLD.status = 'prepared' AND (
            NEW.status IN ('file_prepared','needs_revision','apply_failed')
            OR (NEW.status = 'file_applied' AND OLD.temporary_ref IS NOT NULL AND OLD.backup_ref IS NOT NULL)
        ))
        OR (OLD.status = 'file_prepared' AND NEW.status IN ('file_applied','needs_revision','apply_failed','manual_recovery_required'))
        OR (OLD.status = 'file_applied' AND (
            NEW.status IN ('git_prepared','compensating_file','manual_recovery_required')
            OR (NEW.status = 'git_committed' AND OLD.file_lock_token IS NULL)
        ))
        OR (OLD.status = 'git_prepared' AND NEW.status IN ('git_committed','compensating_file','manual_recovery_required'))
        OR (OLD.status = 'compensating_file' AND NEW.status IN ('compensated','manual_recovery_required'))
        OR (OLD.status = 'git_committed' AND NEW.status IN ('verifying','publish_recovery_required'))
        OR (OLD.status = 'publish_recovery_required' AND NEW.status IN ('verifying','manual_recovery_required'))
        OR (OLD.status = 'verifying' AND NEW.status IN ('completed','verify_failed','rolled_back'))
        OR (OLD.status = 'verify_failed' AND NEW.status IN ('verifying','rolled_back'))
    ) THEN
        RAISE EXCEPTION 'writeback status transition is not allowed' USING ERRCODE = '23514';
    END IF;

    IF OLD.git_commit IS NOT NULL AND (
        NEW.git_commit IS DISTINCT FROM OLD.git_commit
        OR NEW.parent_git_commit IS DISTINCT FROM OLD.parent_git_commit
        OR NEW.diff_hash IS DISTINCT FROM OLD.diff_hash
    ) THEN
        RAISE EXCEPTION 'writeback git result is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.base_blob_id IS NOT NULL AND (
        NEW.base_blob_id IS DISTINCT FROM OLD.base_blob_id
        OR NEW.result_blob_id IS DISTINCT FROM OLD.result_blob_id
        OR NEW.base_mode IS DISTINCT FROM OLD.base_mode
        OR NEW.diff_hash IS DISTINCT FROM OLD.diff_hash
    ) THEN
        RAISE EXCEPTION 'writeback git intent is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.file_lock_token IS NOT NULL AND (
        NEW.temporary_ref IS DISTINCT FROM OLD.temporary_ref
        OR NEW.backup_ref IS DISTINCT FROM OLD.backup_ref
        OR NEW.file_byte_size IS DISTINCT FROM OLD.file_byte_size
        OR NEW.file_mode IS DISTINCT FROM OLD.file_mode
        OR NEW.file_lock_token IS DISTINCT FROM OLD.file_lock_token
        OR NEW.file_result_lock_token IS DISTINCT FROM OLD.file_result_lock_token
        OR NEW.file_backup_lock_token IS DISTINCT FROM OLD.file_backup_lock_token
    ) THEN
        RAISE EXCEPTION 'writeback file lock binding is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.cleanup_completed_at IS NOT NULL AND NEW.cleanup_completed_at IS DISTINCT FROM OLD.cleanup_completed_at THEN
        RAISE EXCEPTION 'writeback cleanup marker is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.manual_recovery_required AND NOT NEW.manual_recovery_required THEN
        RAISE EXCEPTION 'writeback manual recovery flag is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.completed_at IS NOT NULL AND NEW.completed_at IS DISTINCT FROM OLD.completed_at THEN
        RAISE EXCEPTION 'writeback completion time is immutable' USING ERRCODE = '23514';
    END IF;

    IF NEW.status = 'file_prepared' AND (
        NEW.temporary_ref IS NULL OR NEW.backup_ref IS NULL OR NEW.file_byte_size IS NULL
        OR NEW.file_mode IS NULL OR NEW.file_lock_token IS NULL
        OR NEW.file_result_lock_token IS NULL OR NEW.file_backup_lock_token IS NULL
    ) THEN
        RAISE EXCEPTION 'file_prepared requires durable file intent' USING ERRCODE = '23514';
    END IF;
    IF NEW.status = 'git_prepared' AND (
        NEW.base_blob_id IS NULL OR NEW.result_blob_id IS NULL OR NEW.base_mode IS NULL OR NEW.diff_hash IS NULL
    ) THEN
        RAISE EXCEPTION 'git_prepared requires durable git intent' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS writeback_execution_validate_transition ON change_control.writeback_execution;
CREATE TRIGGER writeback_execution_validate_transition
BEFORE UPDATE ON change_control.writeback_execution
FOR EACH ROW EXECUTE FUNCTION change_control.validate_writeback_execution_transition();

CREATE INDEX IF NOT EXISTS idx_writeback_execution_lease_recovery
    ON change_control.writeback_execution (node_run_id, status, updated_at, id);

INSERT INTO core.schema_meta (key, value)
VALUES ('safe_writeback_saga', 'm5-04d')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
INSERT INTO core.schema_meta (key, value)
VALUES ('safe_writeback', 'm5-04d')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- +goose Down
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM change_control.writeback_execution
         WHERE status IN ('file_prepared','git_prepared','publish_recovery_required','manual_recovery_required')
            OR (
                file_lock_token IS NOT NULL
                AND cleanup_completed_at IS NULL
                AND status <> 'compensated'
            )
    ) THEN
        RAISE EXCEPTION 'cannot downgrade safe writeback while durable saga checkpoints or recovery evidence require M5-04D'
            USING ERRCODE = '55000';
    END IF;
END;
$$;

DELETE FROM core.schema_meta WHERE key = 'safe_writeback_saga';
UPDATE core.schema_meta SET value = 'm5-04a', updated_at = now() WHERE key = 'safe_writeback';
DROP INDEX IF EXISTS change_control.idx_writeback_execution_lease_recovery;
ALTER TABLE change_control.writeback_execution
    DROP CONSTRAINT IF EXISTS writeback_git_prepared_fields,
    DROP CONSTRAINT IF EXISTS writeback_git_intent_status_valid,
    DROP CONSTRAINT IF EXISTS writeback_git_prepared_status_fields,
    DROP CONSTRAINT IF EXISTS writeback_file_prepared_fields,
    DROP CONSTRAINT IF EXISTS writeback_execution_git_result,
    DROP CONSTRAINT IF EXISTS writeback_execution_git_fields,
    DROP CONSTRAINT IF EXISTS writeback_cleanup_status_valid,
    DROP CONSTRAINT IF EXISTS writeback_cleanup_time_valid,
    DROP CONSTRAINT IF EXISTS writeback_git_object_ids_valid,
    DROP CONSTRAINT IF EXISTS writeback_file_managed_lock_tokens_valid,
    DROP CONSTRAINT IF EXISTS writeback_file_lock_token_valid,
    DROP CONSTRAINT IF EXISTS writeback_file_mode_valid,
    DROP CONSTRAINT IF EXISTS writeback_file_size_valid,
    DROP CONSTRAINT IF EXISTS writeback_execution_status_check;
ALTER TABLE change_control.writeback_execution
    DROP COLUMN IF EXISTS cleanup_completed_at,
    DROP COLUMN IF EXISTS base_mode,
    DROP COLUMN IF EXISTS result_blob_id,
    DROP COLUMN IF EXISTS base_blob_id,
    DROP COLUMN IF EXISTS file_backup_lock_token,
    DROP COLUMN IF EXISTS file_result_lock_token,
    DROP COLUMN IF EXISTS file_lock_token,
    DROP COLUMN IF EXISTS file_mode,
    DROP COLUMN IF EXISTS file_byte_size;

ALTER TABLE change_control.writeback_execution
    ADD CONSTRAINT writeback_execution_status_check CHECK (status IN (
        'prepared','file_applied','git_committed','verifying','needs_revision','apply_failed',
        'compensating_file','compensated','manual_recovery_required','publish_recovery_required',
        'verify_failed','rolled_back','completed'
    )),
    ADD CONSTRAINT writeback_execution_git_fields CHECK (
        (
            status IN ('git_committed','verifying','publish_recovery_required','verify_failed','rolled_back','completed')
            AND git_commit IS NOT NULL AND parent_git_commit IS NOT NULL AND diff_hash IS NOT NULL
        ) OR (
            status NOT IN ('git_committed','verifying','publish_recovery_required','verify_failed','rolled_back','completed')
            AND git_commit IS NULL AND parent_git_commit IS NULL AND diff_hash IS NULL
        )
    );

CREATE OR REPLACE FUNCTION change_control.validate_writeback_execution_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.workflow_run_id <> OLD.workflow_run_id OR NEW.node_run_id <> OLD.node_run_id
       OR NEW.proposal_id <> OLD.proposal_id OR NEW.revision_id <> OLD.revision_id OR NEW.approval_id <> OLD.approval_id
       OR NEW.write_authorization_id <> OLD.write_authorization_id OR NEW.git_authorization_id <> OLD.git_authorization_id
       OR NEW.target_path <> OLD.target_path OR NEW.base_hash <> OLD.base_hash OR NEW.result_hash <> OLD.result_hash
       OR NEW.approved_change_hash <> OLD.approved_change_hash OR NEW.approved_git_head <> OLD.approved_git_head
       OR NEW.idempotency_key <> OLD.idempotency_key OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'writeback immutable field or version violation' USING ERRCODE = '23514';
    END IF;
    IF NOT (
        (OLD.status = 'prepared' AND NEW.status IN ('file_applied','needs_revision','apply_failed'))
        OR (OLD.status = 'file_applied' AND NEW.status IN ('git_committed','compensating_file'))
        OR (OLD.status = 'compensating_file' AND NEW.status IN ('compensated','manual_recovery_required'))
        OR (OLD.status = 'git_committed' AND NEW.status IN ('verifying','publish_recovery_required'))
        OR (OLD.status = 'publish_recovery_required' AND NEW.status IN ('verifying','manual_recovery_required'))
        OR (OLD.status = 'verifying' AND NEW.status IN ('completed','verify_failed','rolled_back'))
        OR (OLD.status = 'verify_failed' AND NEW.status IN ('verifying','rolled_back'))
    ) THEN
        RAISE EXCEPTION 'writeback status transition is not allowed' USING ERRCODE = '23514';
    END IF;
    IF OLD.git_commit IS NOT NULL AND (
        NEW.git_commit IS DISTINCT FROM OLD.git_commit
        OR NEW.parent_git_commit IS DISTINCT FROM OLD.parent_git_commit
        OR NEW.diff_hash IS DISTINCT FROM OLD.diff_hash
    ) THEN
        RAISE EXCEPTION 'writeback git result is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.manual_recovery_required AND NOT NEW.manual_recovery_required THEN
        RAISE EXCEPTION 'writeback manual recovery flag is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.completed_at IS NOT NULL AND NEW.completed_at IS DISTINCT FROM OLD.completed_at THEN
        RAISE EXCEPTION 'writeback completion time is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
