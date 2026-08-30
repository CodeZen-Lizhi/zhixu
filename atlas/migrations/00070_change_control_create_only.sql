-- CREATE_ONLY 是新的不可变目标存在性契约。所有历史行明确保持 REPLACE，旧请求/变更哈希不重算。
ALTER TABLE change_control.proposal_revision
    ADD COLUMN IF NOT EXISTS target_mode text NOT NULL DEFAULT 'REPLACE';
ALTER TABLE change_control.writeback_execution
    ADD COLUMN IF NOT EXISTS target_mode text NOT NULL DEFAULT 'REPLACE';
ALTER TABLE change_control.proposal_commit
    ADD COLUMN IF NOT EXISTS target_mode text NOT NULL DEFAULT 'REPLACE';
ALTER TABLE change_control.tool_authorization
    ADD COLUMN IF NOT EXISTS target_mode text NOT NULL DEFAULT 'REPLACE';

-- 迁移事实显式化；DEFAULT 仅服务旧二进制的短暂兼容窗口。
UPDATE change_control.proposal_revision SET target_mode='REPLACE' WHERE target_mode IS NULL;
UPDATE change_control.writeback_execution SET target_mode='REPLACE' WHERE target_mode IS NULL;
UPDATE change_control.proposal_commit SET target_mode='REPLACE' WHERE target_mode IS NULL;
UPDATE change_control.tool_authorization SET target_mode='REPLACE' WHERE target_mode IS NULL;

ALTER TABLE change_control.proposal_revision
    DROP CONSTRAINT IF EXISTS proposal_revision_base_hash_check,
    ADD CONSTRAINT proposal_revision_target_mode_check CHECK (target_mode IN ('REPLACE','CREATE_ONLY')),
    ADD CONSTRAINT proposal_revision_base_version_check CHECK (
        (target_mode='REPLACE' AND base_hash ~ '^[0-9a-f]{64}$')
        OR (target_mode='CREATE_ONLY' AND base_hash ~ '^workspace-target-absent/v1:[0-9a-f]{64}$')
    );

ALTER TABLE change_control.writeback_execution
    DROP CONSTRAINT IF EXISTS writeback_execution_base_hash_check,
    ADD CONSTRAINT writeback_execution_target_mode_check CHECK (target_mode IN ('REPLACE','CREATE_ONLY')),
    ADD CONSTRAINT writeback_execution_base_version_check CHECK (
        (target_mode='REPLACE' AND base_hash ~ '^[0-9a-f]{64}$')
        OR (target_mode='CREATE_ONLY' AND base_hash ~ '^workspace-target-absent/v1:[0-9a-f]{64}$')
    ),
    ADD CONSTRAINT writeback_execution_create_only_file_fields CHECK (
        target_mode <> 'CREATE_ONLY' OR backup_ref IS NULL
    ),
    ADD CONSTRAINT writeback_execution_create_only_lock_fields CHECK (
        target_mode <> 'CREATE_ONLY' OR file_backup_lock_token IS NULL
    ),
    ADD CONSTRAINT writeback_execution_create_only_git_intent CHECK (
        target_mode <> 'CREATE_ONLY' OR base_blob_id IS NULL
    );

ALTER TABLE change_control.writeback_execution
    DROP CONSTRAINT IF EXISTS writeback_git_object_ids_valid,
    ADD CONSTRAINT writeback_git_object_ids_valid CHECK (
        ((target_mode='REPLACE' AND (base_blob_id IS NULL OR base_blob_id ~ '^[0-9a-f]{40}$' OR base_blob_id ~ '^[0-9a-f]{64}$'))
         OR (target_mode='CREATE_ONLY' AND base_blob_id IS NULL))
        AND (result_blob_id IS NULL OR result_blob_id ~ '^[0-9a-f]{40}$' OR result_blob_id ~ '^[0-9a-f]{64}$')
        AND (base_mode IS NULL OR base_mode IN ('100644','100755'))
    ),
    DROP CONSTRAINT IF EXISTS writeback_file_managed_lock_tokens_valid,
    ADD CONSTRAINT writeback_file_managed_lock_tokens_valid CHECK (
        (target_mode='REPLACE' AND (
            (file_result_lock_token IS NULL AND file_backup_lock_token IS NULL)
            OR (file_lock_token IS NOT NULL AND file_result_lock_token IS NOT NULL AND file_backup_lock_token IS NOT NULL
                AND file_result_lock_token ~ '^[0-9a-f]{64}$' AND file_backup_lock_token ~ '^[0-9a-f]{64}$'
                AND file_result_lock_token<>file_backup_lock_token AND file_result_lock_token<>file_lock_token AND file_backup_lock_token<>file_lock_token)
        ))
        OR (target_mode='CREATE_ONLY' AND (
            (file_result_lock_token IS NULL AND file_backup_lock_token IS NULL)
            OR (file_lock_token IS NOT NULL AND file_result_lock_token IS NOT NULL AND file_backup_lock_token IS NULL
                AND file_result_lock_token ~ '^[0-9a-f]{64}$' AND file_result_lock_token<>file_lock_token)
        ))
    ),
    DROP CONSTRAINT IF EXISTS writeback_file_prepared_fields,
    ADD CONSTRAINT writeback_file_prepared_fields CHECK (
        status<>'file_prepared' OR (
            temporary_ref IS NOT NULL AND file_byte_size IS NOT NULL AND file_mode IS NOT NULL AND file_lock_token IS NOT NULL AND file_result_lock_token IS NOT NULL
            AND ((target_mode='REPLACE' AND backup_ref IS NOT NULL AND file_backup_lock_token IS NOT NULL)
                 OR (target_mode='CREATE_ONLY' AND backup_ref IS NULL AND file_backup_lock_token IS NULL))
        )
    ),
    DROP CONSTRAINT IF EXISTS writeback_git_prepared_fields,
    ADD CONSTRAINT writeback_git_prepared_fields CHECK (
        (target_mode='REPLACE' AND ((base_blob_id IS NULL AND result_blob_id IS NULL AND base_mode IS NULL)
            OR (base_blob_id IS NOT NULL AND result_blob_id IS NOT NULL AND base_mode IS NOT NULL
                AND length(base_blob_id)=length(approved_git_head) AND length(result_blob_id)=length(approved_git_head))))
        OR (target_mode='CREATE_ONLY' AND base_blob_id IS NULL AND ((result_blob_id IS NULL AND base_mode IS NULL)
            OR (result_blob_id IS NOT NULL AND base_mode IS NOT NULL
                AND length(result_blob_id)=length(approved_git_head))))
    ),
    DROP CONSTRAINT IF EXISTS writeback_git_intent_status_valid,
    ADD CONSTRAINT writeback_git_intent_status_valid CHECK (
        result_blob_id IS NULL OR status IN (
            'git_prepared','git_committed','verifying','publish_recovery_required',
            'compensating_file','compensated','manual_recovery_required',
            'verify_failed','rolled_back','completed'
        )
    ),
    DROP CONSTRAINT IF EXISTS writeback_git_prepared_status_fields,
    ADD CONSTRAINT writeback_git_prepared_status_fields CHECK (
        status<>'git_prepared' OR (result_blob_id IS NOT NULL AND base_mode IS NOT NULL AND diff_hash IS NOT NULL
            AND ((target_mode='REPLACE' AND base_blob_id IS NOT NULL)
                 OR (target_mode='CREATE_ONLY' AND base_blob_id IS NULL)))
    );

ALTER TABLE change_control.proposal_commit
    DROP CONSTRAINT IF EXISTS proposal_commit_target_mode_check,
    ADD CONSTRAINT proposal_commit_target_mode_check CHECK (target_mode IN ('REPLACE','CREATE_ONLY'));
ALTER TABLE change_control.tool_authorization
    DROP CONSTRAINT IF EXISTS tool_authorization_target_mode_check,
    ADD CONSTRAINT tool_authorization_target_mode_check CHECK (target_mode IN ('REPLACE','CREATE_ONLY'));

CREATE OR REPLACE FUNCTION change_control.validate_create_only_revision_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_type_value text;
    workspace_value uuid;
BEGIN
    SELECT proposal_type,workspace_id INTO proposal_type_value,workspace_value
      FROM change_control.proposal WHERE id=NEW.proposal_id FOR SHARE;
    IF proposal_type_value IS NULL THEN
        RAISE EXCEPTION 'proposal revision proposal is missing' USING ERRCODE='23514';
    END IF;
    IF proposal_type_value <> 'file_patch' AND NEW.target_mode <> 'REPLACE' THEN
        RAISE EXCEPTION 'only file patch revisions can use create only target mode' USING ERRCODE='23514';
    END IF;
    IF proposal_type_value = 'file_patch' AND NEW.target_mode = 'CREATE_ONLY'
       AND NEW.base_hash !~ '^workspace-target-absent/v1:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION 'create only revision requires a versioned absence token' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS proposal_revision_validate_create_only_binding ON change_control.proposal_revision;
CREATE TRIGGER proposal_revision_validate_create_only_binding
BEFORE INSERT ON change_control.proposal_revision
FOR EACH ROW EXECUTE FUNCTION change_control.validate_create_only_revision_binding();

CREATE OR REPLACE FUNCTION change_control.verify_tool_authorization_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_workspace uuid;
    proposal_status text;
    proposal_target_path text;
    proposal_target_mode text;
    revision_proposal uuid;
    revision_base_hash text;
    revision_change_hash text;
    approval_proposal uuid;
    approval_revision uuid;
    approval_change_hash text;
    approval_decision text;
    run_workspace uuid;
    node_run_id uuid;
    expected_scope text;
BEGIN
    IF TG_OP='INSERT' AND (NEW.status<>'issued' OR NEW.version<>1 OR NEW.consumed_at IS NOT NULL OR NEW.revoked_at IS NOT NULL) THEN
        RAISE EXCEPTION 'tool authorization must be inserted as issued version one' USING ERRCODE='23514';
    END IF;
    IF (NEW.capability='WRITE_KNOWLEDGE' AND NEW.tool_name<>'ApplyApprovedPatch')
       OR (NEW.capability='GIT_WRITE' AND NEW.tool_name<>'CreateGitCommit') THEN
        RAISE EXCEPTION 'tool authorization capability/tool mismatch' USING ERRCODE='23514';
    END IF;
    SELECT p.workspace_id,p.status,r.target_path,r.target_mode,r.proposal_id,r.base_hash,r.change_hash,
           a.proposal_id,a.revision_id,a.change_hash,a.decision
      INTO proposal_workspace,proposal_status,proposal_target_path,proposal_target_mode,revision_proposal,revision_base_hash,revision_change_hash,
           approval_proposal,approval_revision,approval_change_hash,approval_decision
      FROM change_control.proposal p
      JOIN change_control.proposal_revision r ON r.id=NEW.revision_id
      JOIN change_control.approval a ON a.id=NEW.approval_id
     WHERE p.id=NEW.proposal_id
     FOR UPDATE OF p,r,a;
    expected_scope := CASE proposal_target_mode
        WHEN 'CREATE_ONLY' THEN 'target:create-only:' || proposal_target_path
        ELSE 'target:' || proposal_target_path
    END;
    IF proposal_workspace IS NULL OR proposal_workspace<>NEW.workspace_id OR proposal_status<>'approved'
       OR NEW.target_mode<>proposal_target_mode
       OR NEW.scope<>expected_scope
       OR revision_proposal<>NEW.proposal_id OR approval_proposal<>NEW.proposal_id OR approval_revision<>NEW.revision_id
       OR approval_decision<>'approved' OR revision_change_hash<>NEW.approved_change_hash
       OR approval_change_hash<>NEW.approved_change_hash OR revision_base_hash<>NEW.target_version THEN
        RAISE EXCEPTION 'tool authorization approval binding mismatch' USING ERRCODE='23514';
    END IF;
    SELECT r.workspace_id,n.id INTO run_workspace,node_run_id
      FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id
     WHERE r.id=NEW.workflow_run_id AND n.id=NEW.node_run_id
       AND r.status IN ('pending','running','waiting_for_human')
       AND n.status IN ('pending','running','waiting_for_human')
     FOR UPDATE OF r,n;
    IF run_workspace IS NULL OR run_workspace<>NEW.workspace_id OR node_run_id IS NULL THEN
        RAISE EXCEPTION 'tool authorization workflow binding mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION change_control.validate_tool_authorization_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.workflow_run_id<>OLD.workflow_run_id
       OR NEW.node_run_id<>OLD.node_run_id OR NEW.proposal_id<>OLD.proposal_id OR NEW.revision_id<>OLD.revision_id
       OR NEW.approval_id<>OLD.approval_id OR NEW.tool_name<>OLD.tool_name OR NEW.capability<>OLD.capability
       OR NEW.scope<>OLD.scope OR NEW.approved_change_hash<>OLD.approved_change_hash OR NEW.target_mode<>OLD.target_mode
       OR NEW.target_version<>OLD.target_version OR NEW.token_hash<>OLD.token_hash OR NEW.idempotency_key<>OLD.idempotency_key
       OR NEW.issued_at<>OLD.issued_at OR NEW.expires_at<>OLD.expires_at OR NEW.version<>OLD.version+1
       OR OLD.status<>'issued' OR NEW.status NOT IN ('consumed','revoked','expired') THEN
        RAISE EXCEPTION 'tool authorization immutable field or transition violation' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS tool_authorization_verify_binding ON change_control.tool_authorization;
CREATE TRIGGER tool_authorization_verify_binding
BEFORE INSERT OR UPDATE OF workspace_id,workflow_run_id,node_run_id,proposal_id,revision_id,approval_id,approved_change_hash,target_mode,target_version
ON change_control.tool_authorization
FOR EACH ROW EXECUTE FUNCTION change_control.verify_tool_authorization_binding();

CREATE OR REPLACE FUNCTION change_control.verify_writeback_execution_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_workspace uuid;
    proposal_status text;
    revision_proposal uuid;
    revision_target text;
    revision_target_mode text;
    revision_base_hash text;
    revision_change_hash text;
    approval_proposal uuid;
    approval_revision uuid;
    approval_change_hash text;
    approval_decision text;
    approval_git_head text;
    run_workspace uuid;
    bound_node uuid;
    expected_scope text;
    write_authorization change_control.tool_authorization%ROWTYPE;
    git_authorization change_control.tool_authorization%ROWTYPE;
    existing_execution change_control.writeback_execution%ROWTYPE;
BEGIN
    IF NEW.status<>'prepared' OR NEW.version<>1
       OR NEW.git_commit IS NOT NULL OR NEW.parent_git_commit IS NOT NULL OR NEW.diff_hash IS NOT NULL
       OR NEW.failure_code IS NOT NULL OR NEW.manual_recovery_required OR NEW.completed_at IS NOT NULL THEN
        RAISE EXCEPTION 'writeback execution must be inserted as prepared version one' USING ERRCODE='23514';
    END IF;
    IF NEW.write_authorization_id=NEW.git_authorization_id THEN
        RAISE EXCEPTION 'writeback requires two distinct authorizations' USING ERRCODE='23514';
    END IF;
    PERFORM id FROM change_control.tool_authorization
     WHERE id IN (NEW.write_authorization_id,NEW.git_authorization_id)
     ORDER BY id FOR UPDATE;

    SELECT p.workspace_id,p.status,r.proposal_id,r.target_path,r.target_mode,r.base_hash,r.change_hash,
           a.proposal_id,a.revision_id,a.change_hash,a.decision,a.approved_git_head
      INTO proposal_workspace,proposal_status,revision_proposal,revision_target,revision_target_mode,revision_base_hash,revision_change_hash,
           approval_proposal,approval_revision,approval_change_hash,approval_decision,approval_git_head
      FROM change_control.proposal p
      JOIN change_control.proposal_revision r ON r.id=NEW.revision_id
      JOIN change_control.approval a ON a.id=NEW.approval_id
     WHERE p.id=NEW.proposal_id
     FOR UPDATE OF p,r,a;
    IF proposal_status<>'approved' THEN
        SELECT * INTO existing_execution FROM change_control.writeback_execution
         WHERE (workspace_id=NEW.workspace_id AND idempotency_key=NEW.idempotency_key)
            OR (proposal_id=NEW.proposal_id AND revision_id=NEW.revision_id)
         ORDER BY id LIMIT 1 FOR UPDATE;
        IF existing_execution.id IS NOT NULL
           AND existing_execution.workspace_id=NEW.workspace_id
           AND existing_execution.workflow_run_id=NEW.workflow_run_id
           AND existing_execution.node_run_id=NEW.node_run_id
           AND existing_execution.proposal_id=NEW.proposal_id
           AND existing_execution.revision_id=NEW.revision_id
           AND existing_execution.approval_id=NEW.approval_id
           AND existing_execution.write_authorization_id=NEW.write_authorization_id
           AND existing_execution.git_authorization_id=NEW.git_authorization_id
           AND existing_execution.target_path=NEW.target_path
           AND existing_execution.target_mode=NEW.target_mode
           AND existing_execution.base_hash=NEW.base_hash
           AND existing_execution.result_hash=NEW.result_hash
           AND existing_execution.approved_change_hash=NEW.approved_change_hash
           AND existing_execution.approved_git_head=NEW.approved_git_head
           AND existing_execution.idempotency_key=NEW.idempotency_key THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'writeback proposal is no longer approved' USING ERRCODE='23514';
    END IF;
    IF proposal_workspace IS NULL OR proposal_workspace<>NEW.workspace_id OR proposal_status<>'approved'
       OR revision_proposal<>NEW.proposal_id OR revision_target<>NEW.target_path OR revision_target_mode<>NEW.target_mode
       OR revision_base_hash<>NEW.base_hash OR revision_change_hash<>NEW.approved_change_hash
       OR approval_proposal<>NEW.proposal_id OR approval_revision<>NEW.revision_id
       OR approval_change_hash<>NEW.approved_change_hash OR approval_decision<>'approved'
       OR approval_git_head IS NULL OR approval_git_head<>NEW.approved_git_head THEN
        RAISE EXCEPTION 'writeback proposal approval binding mismatch' USING ERRCODE='23514';
    END IF;
    SELECT r.workspace_id,n.id INTO run_workspace,bound_node
      FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id
     WHERE r.id=NEW.workflow_run_id AND n.id=NEW.node_run_id
       AND r.status IN ('pending','running','waiting_for_human')
       AND n.status IN ('pending','running','waiting_for_human')
     FOR UPDATE OF r,n;
    IF run_workspace IS NULL OR run_workspace<>NEW.workspace_id OR bound_node IS NULL THEN
        RAISE EXCEPTION 'writeback workflow binding mismatch' USING ERRCODE='23514';
    END IF;
    SELECT * INTO write_authorization FROM change_control.tool_authorization WHERE id=NEW.write_authorization_id FOR UPDATE;
    SELECT * INTO git_authorization FROM change_control.tool_authorization WHERE id=NEW.git_authorization_id FOR UPDATE;
    expected_scope := CASE NEW.target_mode WHEN 'CREATE_ONLY' THEN 'target:create-only:' || NEW.target_path ELSE 'target:' || NEW.target_path END;
    IF write_authorization.id IS NULL OR git_authorization.id IS NULL
       OR write_authorization.workspace_id<>NEW.workspace_id OR git_authorization.workspace_id<>NEW.workspace_id
       OR write_authorization.workflow_run_id<>NEW.workflow_run_id OR git_authorization.workflow_run_id<>NEW.workflow_run_id
       OR write_authorization.node_run_id<>NEW.node_run_id OR git_authorization.node_run_id<>NEW.node_run_id
       OR write_authorization.proposal_id<>NEW.proposal_id OR git_authorization.proposal_id<>NEW.proposal_id
       OR write_authorization.revision_id<>NEW.revision_id OR git_authorization.revision_id<>NEW.revision_id
       OR write_authorization.approval_id<>NEW.approval_id OR git_authorization.approval_id<>NEW.approval_id
       OR write_authorization.tool_name<>'ApplyApprovedPatch' OR write_authorization.capability<>'WRITE_KNOWLEDGE'
       OR git_authorization.tool_name<>'CreateGitCommit' OR git_authorization.capability<>'GIT_WRITE'
       OR write_authorization.target_mode<>NEW.target_mode OR git_authorization.target_mode<>NEW.target_mode
       OR write_authorization.scope<>expected_scope OR git_authorization.scope<>expected_scope
       OR write_authorization.approved_change_hash<>NEW.approved_change_hash OR git_authorization.approved_change_hash<>NEW.approved_change_hash
       OR write_authorization.target_version<>NEW.base_hash OR git_authorization.target_version<>NEW.base_hash
       OR NOT (write_authorization.status='consumed' OR (write_authorization.status='issued' AND CURRENT_TIMESTAMP<write_authorization.expires_at))
       OR NOT (git_authorization.status='consumed' OR (git_authorization.status='issued' AND CURRENT_TIMESTAMP<git_authorization.expires_at)) THEN
        RAISE EXCEPTION 'writeback authorization binding mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION change_control.verify_proposal_commit_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    execution change_control.writeback_execution%ROWTYPE;
    proposal_status text;
    revision_proposal uuid;
    revision_target text;
    revision_target_mode text;
    revision_change_hash text;
    approval_proposal uuid;
    approval_revision uuid;
    approval_change_hash text;
    approval_decision text;
    approval_git_head text;
BEGIN
    SELECT * INTO execution FROM change_control.writeback_execution WHERE id=NEW.writeback_execution_id FOR UPDATE;
    SELECT p.status,r.proposal_id,r.target_path,r.target_mode,r.change_hash,a.proposal_id,a.revision_id,a.change_hash,a.decision,a.approved_git_head
      INTO proposal_status,revision_proposal,revision_target,revision_target_mode,revision_change_hash,
           approval_proposal,approval_revision,approval_change_hash,approval_decision,approval_git_head
      FROM change_control.proposal p
      JOIN change_control.proposal_revision r ON r.id=execution.revision_id
      JOIN change_control.approval a ON a.id=execution.approval_id
     WHERE p.id=execution.proposal_id
     FOR UPDATE OF p,r,a;
    IF execution.id IS NULL OR execution.status NOT IN ('git_committed','publish_recovery_required','verifying')
       OR NEW.workspace_id<>execution.workspace_id OR NEW.proposal_id<>execution.proposal_id
       OR NEW.revision_id<>execution.revision_id OR NEW.approval_id<>execution.approval_id
       OR NEW.git_commit<>execution.git_commit OR NEW.parent_git_commit<>execution.parent_git_commit
       OR NEW.target_path<>execution.target_path OR NEW.target_mode<>execution.target_mode
       OR NEW.diff_hash<>execution.diff_hash OR NEW.result_hash<>execution.result_hash
       OR proposal_status NOT IN ('applied','verifying') OR revision_proposal<>execution.proposal_id
       OR revision_target<>execution.target_path OR revision_target_mode<>execution.target_mode
       OR revision_change_hash<>execution.approved_change_hash
       OR approval_proposal<>execution.proposal_id OR approval_revision<>execution.revision_id
       OR approval_change_hash<>execution.approved_change_hash OR approval_decision<>'approved'
       OR approval_git_head IS NULL OR approval_git_head<>execution.approved_git_head THEN
        RAISE EXCEPTION 'proposal commit binding mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION change_control.validate_writeback_execution_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.workflow_run_id<>OLD.workflow_run_id
       OR NEW.node_run_id<>OLD.node_run_id OR NEW.proposal_id<>OLD.proposal_id OR NEW.revision_id<>OLD.revision_id
       OR NEW.approval_id<>OLD.approval_id OR NEW.write_authorization_id<>OLD.write_authorization_id
       OR NEW.git_authorization_id<>OLD.git_authorization_id OR NEW.target_path<>OLD.target_path
       OR NEW.target_mode<>OLD.target_mode OR NEW.base_hash<>OLD.base_hash OR NEW.result_hash<>OLD.result_hash
       OR NEW.approved_change_hash<>OLD.approved_change_hash OR NEW.approved_git_head<>OLD.approved_git_head
       OR NEW.idempotency_key<>OLD.idempotency_key OR NEW.created_at<>OLD.created_at
       OR NEW.updated_at<OLD.updated_at OR NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'writeback immutable field or version violation' USING ERRCODE='23514';
    END IF;
    IF NOT (
        (OLD.status='verifying' AND NEW.status='verifying'
         AND OLD.cleanup_completed_at IS NULL AND NEW.cleanup_completed_at IS NOT NULL
         AND NEW.failure_code IS NOT DISTINCT FROM OLD.failure_code
         AND NEW.manual_recovery_required=OLD.manual_recovery_required
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
        OR (OLD.status='prepared' AND (
            NEW.status IN ('file_prepared','needs_revision','apply_failed')
            OR (OLD.target_mode='REPLACE' AND NEW.status='file_applied' AND OLD.temporary_ref IS NOT NULL AND OLD.backup_ref IS NOT NULL)
        ))
        OR (OLD.status='file_prepared' AND NEW.status IN ('file_applied','needs_revision','apply_failed','manual_recovery_required'))
        OR (OLD.status='file_applied' AND (
            NEW.status IN ('git_prepared','compensating_file','manual_recovery_required')
            OR (OLD.target_mode='REPLACE' AND NEW.status='git_committed' AND OLD.file_lock_token IS NULL)
        ))
        OR (OLD.status='git_prepared' AND NEW.status IN ('git_committed','compensating_file','manual_recovery_required'))
        OR (OLD.status='compensating_file' AND NEW.status IN ('compensated','manual_recovery_required'))
        OR (OLD.status='git_committed' AND NEW.status IN ('verifying','publish_recovery_required'))
        OR (OLD.status='publish_recovery_required' AND NEW.status IN ('verifying','manual_recovery_required'))
        OR (OLD.status='verifying' AND NEW.status IN ('completed','verify_failed','rolled_back'))
        OR (OLD.status='verify_failed' AND NEW.status IN ('verifying','rolled_back'))
    ) THEN
        RAISE EXCEPTION 'writeback status transition is not allowed' USING ERRCODE='23514';
    END IF;
    IF OLD.git_commit IS NOT NULL AND (NEW.git_commit IS DISTINCT FROM OLD.git_commit OR NEW.parent_git_commit IS DISTINCT FROM OLD.parent_git_commit OR NEW.diff_hash IS DISTINCT FROM OLD.diff_hash) THEN
        RAISE EXCEPTION 'writeback git result is immutable' USING ERRCODE='23514';
    END IF;
    IF OLD.result_blob_id IS NOT NULL AND (NEW.base_blob_id IS DISTINCT FROM OLD.base_blob_id OR NEW.result_blob_id IS DISTINCT FROM OLD.result_blob_id OR NEW.base_mode IS DISTINCT FROM OLD.base_mode OR NEW.diff_hash IS DISTINCT FROM OLD.diff_hash) THEN
        RAISE EXCEPTION 'writeback git intent is immutable' USING ERRCODE='23514';
    END IF;
    IF OLD.file_lock_token IS NOT NULL AND (NEW.temporary_ref IS DISTINCT FROM OLD.temporary_ref OR NEW.backup_ref IS DISTINCT FROM OLD.backup_ref OR NEW.file_byte_size IS DISTINCT FROM OLD.file_byte_size OR NEW.file_mode IS DISTINCT FROM OLD.file_mode OR NEW.file_lock_token IS DISTINCT FROM OLD.file_lock_token OR NEW.file_result_lock_token IS DISTINCT FROM OLD.file_result_lock_token OR NEW.file_backup_lock_token IS DISTINCT FROM OLD.file_backup_lock_token) THEN
        RAISE EXCEPTION 'writeback file lock binding is immutable' USING ERRCODE='23514';
    END IF;
    IF OLD.cleanup_completed_at IS NOT NULL AND NEW.cleanup_completed_at IS DISTINCT FROM OLD.cleanup_completed_at THEN
        RAISE EXCEPTION 'writeback cleanup marker is immutable' USING ERRCODE='23514';
    END IF;
    IF OLD.manual_recovery_required AND NOT NEW.manual_recovery_required THEN
        RAISE EXCEPTION 'writeback manual recovery flag is immutable' USING ERRCODE='23514';
    END IF;
    IF OLD.completed_at IS NOT NULL AND NEW.completed_at IS DISTINCT FROM OLD.completed_at THEN
        RAISE EXCEPTION 'writeback completion time is immutable' USING ERRCODE='23514';
    END IF;
    IF NEW.status='file_prepared' AND (
        NEW.temporary_ref IS NULL OR NEW.file_byte_size IS NULL OR NEW.file_mode IS NULL OR NEW.file_lock_token IS NULL OR NEW.file_result_lock_token IS NULL
        OR (NEW.target_mode='REPLACE' AND (NEW.backup_ref IS NULL OR NEW.file_backup_lock_token IS NULL))
        OR (NEW.target_mode='CREATE_ONLY' AND (NEW.backup_ref IS NOT NULL OR NEW.file_backup_lock_token IS NOT NULL))
    ) THEN
        RAISE EXCEPTION 'file_prepared requires durable file intent' USING ERRCODE='23514';
    END IF;
    IF NEW.status='git_prepared' AND (
        NEW.result_blob_id IS NULL OR NEW.base_mode IS NULL OR NEW.diff_hash IS NULL
        OR (NEW.target_mode='REPLACE' AND NEW.base_blob_id IS NULL)
        OR (NEW.target_mode='CREATE_ONLY' AND NEW.base_blob_id IS NOT NULL)
    ) THEN
        RAISE EXCEPTION 'git_prepared requires durable git intent' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
