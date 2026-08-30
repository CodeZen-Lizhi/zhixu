ALTER TABLE change_control.proposal
    ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 1;

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS proposal_version_positive,
    ADD CONSTRAINT proposal_version_positive CHECK (version > 0);

ALTER TABLE change_control.approval
    ADD COLUMN IF NOT EXISTS approved_git_head text;

ALTER TABLE change_control.approval
    DROP CONSTRAINT IF EXISTS approval_git_head_format,
    ADD CONSTRAINT approval_git_head_format CHECK (
        approved_git_head IS NULL
        OR approved_git_head ~ '^[0-9a-f]{40}$'
        OR approved_git_head ~ '^[0-9a-f]{64}$'
    );

CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'proposal must be inserted at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.request_hash <> OLD.request_hash
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'proposal immutable field or version violation' USING ERRCODE = '23514';
    END IF;

    IF NOT (
        (OLD.status = 'ready_for_review' AND NEW.status IN ('approved','rejected','needs_revision'))
        OR (OLD.status = 'approved' AND NEW.status IN ('applying','needs_revision'))
        OR (OLD.status = 'applying' AND NEW.status IN ('applied','apply_failed','needs_revision'))
        OR (OLD.status = 'applied' AND NEW.status = 'verifying')
        OR (OLD.status = 'verifying' AND NEW.status IN ('completed','verify_failed','rolled_back'))
        OR (OLD.status = 'verify_failed' AND NEW.status IN ('verifying','rolled_back'))
        OR (OLD.status = 'apply_failed' AND NEW.status IN ('applying','rolled_back'))
        OR (OLD.status = 'needs_revision' AND NEW.status IN ('draft','cancelled'))
    ) THEN
        RAISE EXCEPTION 'proposal status transition is not allowed' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS proposal_validate_transition ON change_control.proposal;
CREATE TRIGGER proposal_validate_transition
BEFORE INSERT OR UPDATE ON change_control.proposal
FOR EACH ROW EXECUTE FUNCTION change_control.validate_proposal_transition();

CREATE TABLE IF NOT EXISTS change_control.writeback_execution (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    workflow_run_id uuid NOT NULL REFERENCES workflow.run(id) ON DELETE RESTRICT,
    node_run_id uuid NOT NULL REFERENCES workflow.node_run(id) ON DELETE RESTRICT,
    proposal_id uuid NOT NULL REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    revision_id uuid NOT NULL REFERENCES change_control.proposal_revision(id) ON DELETE RESTRICT,
    approval_id uuid NOT NULL REFERENCES change_control.approval(id) ON DELETE RESTRICT,
    write_authorization_id uuid NOT NULL REFERENCES change_control.tool_authorization(id) ON DELETE RESTRICT,
    git_authorization_id uuid NOT NULL REFERENCES change_control.tool_authorization(id) ON DELETE RESTRICT,
    target_path text NOT NULL CHECK (
        btrim(target_path) <> '' AND
        left(target_path, 1) <> '/' AND
        target_path !~ '(^|/)\.\.(/|$)' AND
        position(E'\\\\' in target_path) = 0
    ),
    base_hash text NOT NULL CHECK (base_hash ~ '^[0-9a-f]{64}$'),
    result_hash text NOT NULL CHECK (result_hash ~ '^[0-9a-f]{64}$'),
    approved_change_hash text NOT NULL CHECK (approved_change_hash ~ '^[0-9a-f]{64}$'),
    approved_git_head text NOT NULL CHECK (
        approved_git_head ~ '^[0-9a-f]{40}$'
        OR approved_git_head ~ '^[0-9a-f]{64}$'
    ),
    git_commit text CHECK (
        git_commit IS NULL OR git_commit ~ '^[0-9a-f]{40}$' OR git_commit ~ '^[0-9a-f]{64}$'
    ),
    parent_git_commit text CHECK (
        parent_git_commit IS NULL OR parent_git_commit ~ '^[0-9a-f]{40}$' OR parent_git_commit ~ '^[0-9a-f]{64}$'
    ),
    diff_hash text CHECK (diff_hash IS NULL OR diff_hash ~ '^[0-9a-f]{64}$'),
    status text NOT NULL CHECK (status IN (
        'prepared','file_applied','git_committed','verifying','needs_revision','apply_failed',
        'compensating_file','compensated','manual_recovery_required','publish_recovery_required',
        'verify_failed','rolled_back','completed'
    )),
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> '' AND char_length(idempotency_key) <= 128),
    failure_code text CHECK (failure_code IS NULL OR (btrim(failure_code) <> '' AND char_length(failure_code) <= 128)),
    manual_recovery_required boolean NOT NULL DEFAULT false,
    temporary_ref text CHECK (
        temporary_ref IS NULL OR (
            btrim(temporary_ref) <> '' AND left(temporary_ref, 1) <> '/'
            AND temporary_ref !~ '(^|/)\.\.(/|$)' AND position(E'\\\\' in temporary_ref) = 0
        )
    ),
    backup_ref text CHECK (
        backup_ref IS NULL OR (
            btrim(backup_ref) <> '' AND left(backup_ref, 1) <> '/'
            AND backup_ref !~ '(^|/)\.\.(/|$)' AND position(E'\\\\' in backup_ref) = 0
        )
    ),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT writeback_execution_time_order CHECK (
        updated_at >= created_at AND (completed_at IS NULL OR completed_at >= created_at)
    ),
    CONSTRAINT writeback_execution_git_fields CHECK (
        (
            status IN ('git_committed','verifying','publish_recovery_required','verify_failed','rolled_back','completed')
            AND git_commit IS NOT NULL AND parent_git_commit IS NOT NULL AND diff_hash IS NOT NULL
        ) OR (
            status NOT IN ('git_committed','verifying','publish_recovery_required','verify_failed','rolled_back','completed')
            AND git_commit IS NULL AND parent_git_commit IS NULL AND diff_hash IS NULL
        )
    ),
    CONSTRAINT writeback_execution_failure_code CHECK (
        status NOT IN ('needs_revision','apply_failed','publish_recovery_required','manual_recovery_required','verify_failed')
        OR failure_code IS NOT NULL
    ),
    CONSTRAINT writeback_execution_manual_recovery CHECK (
        manual_recovery_required = (status = 'manual_recovery_required')
    ),
    CONSTRAINT uq_writeback_execution_idempotency UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT uq_writeback_execution_proposal_revision UNIQUE (proposal_id, revision_id),
    CONSTRAINT uq_writeback_execution_write_authorization UNIQUE (write_authorization_id),
    CONSTRAINT uq_writeback_execution_git_authorization UNIQUE (git_authorization_id)
);

CREATE INDEX IF NOT EXISTS idx_writeback_execution_recovery
    ON change_control.writeback_execution (workspace_id, status, updated_at, id);
CREATE INDEX IF NOT EXISTS idx_writeback_execution_workflow
    ON change_control.writeback_execution (workflow_run_id, node_run_id, status);

CREATE OR REPLACE FUNCTION change_control.verify_writeback_execution_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_workspace uuid;
    proposal_status text;
    revision_proposal uuid;
    revision_target text;
    revision_base_hash text;
    revision_change_hash text;
    approval_proposal uuid;
    approval_revision uuid;
    approval_change_hash text;
    approval_decision text;
    approval_git_head text;
    run_workspace uuid;
    bound_node uuid;
    write_authorization change_control.tool_authorization%ROWTYPE;
    git_authorization change_control.tool_authorization%ROWTYPE;
    existing_execution change_control.writeback_execution%ROWTYPE;
BEGIN
    IF NEW.status <> 'prepared' OR NEW.version <> 1
       OR NEW.git_commit IS NOT NULL OR NEW.parent_git_commit IS NOT NULL OR NEW.diff_hash IS NOT NULL
       OR NEW.failure_code IS NOT NULL OR NEW.manual_recovery_required OR NEW.completed_at IS NOT NULL THEN
        RAISE EXCEPTION 'writeback execution must be inserted as prepared version one' USING ERRCODE = '23514';
    END IF;
    IF NEW.write_authorization_id = NEW.git_authorization_id THEN
        RAISE EXCEPTION 'writeback requires two distinct authorizations' USING ERRCODE = '23514';
    END IF;

    -- 与授权消费保持 Authorization -> Proposal -> Workflow 的锁顺序，避免交叉死锁。
    PERFORM id
      FROM change_control.tool_authorization
     WHERE id IN (NEW.write_authorization_id, NEW.git_authorization_id)
     ORDER BY id
     FOR UPDATE;

    SELECT p.workspace_id, p.status, r.proposal_id, r.target_path, r.base_hash, r.change_hash,
           a.proposal_id, a.revision_id, a.change_hash, a.decision, a.approved_git_head
      INTO proposal_workspace, proposal_status, revision_proposal, revision_target, revision_base_hash, revision_change_hash,
           approval_proposal, approval_revision, approval_change_hash, approval_decision, approval_git_head
      FROM change_control.proposal p
      JOIN change_control.proposal_revision r ON r.id = NEW.revision_id
      JOIN change_control.approval a ON a.id = NEW.approval_id
     WHERE p.id = NEW.proposal_id
     FOR UPDATE OF p, r, a;
    IF proposal_status <> 'approved' THEN
        SELECT * INTO existing_execution
          FROM change_control.writeback_execution
         WHERE (workspace_id = NEW.workspace_id AND idempotency_key = NEW.idempotency_key)
            OR (proposal_id = NEW.proposal_id AND revision_id = NEW.revision_id)
         ORDER BY id
         LIMIT 1
         FOR UPDATE;
        IF existing_execution.id IS NOT NULL
           AND existing_execution.workspace_id = NEW.workspace_id
           AND existing_execution.workflow_run_id = NEW.workflow_run_id
           AND existing_execution.node_run_id = NEW.node_run_id
           AND existing_execution.proposal_id = NEW.proposal_id
           AND existing_execution.revision_id = NEW.revision_id
           AND existing_execution.approval_id = NEW.approval_id
           AND existing_execution.write_authorization_id = NEW.write_authorization_id
           AND existing_execution.git_authorization_id = NEW.git_authorization_id
           AND existing_execution.target_path = NEW.target_path
           AND existing_execution.base_hash = NEW.base_hash
           AND existing_execution.result_hash = NEW.result_hash
           AND existing_execution.approved_change_hash = NEW.approved_change_hash
           AND existing_execution.approved_git_head = NEW.approved_git_head
           AND existing_execution.idempotency_key = NEW.idempotency_key THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'writeback proposal is no longer approved' USING ERRCODE = '23514';
    END IF;
    IF proposal_workspace IS NULL OR proposal_workspace <> NEW.workspace_id OR proposal_status <> 'approved'
       OR revision_proposal <> NEW.proposal_id OR revision_target <> NEW.target_path
       OR revision_base_hash <> NEW.base_hash OR revision_change_hash <> NEW.approved_change_hash
       OR approval_proposal <> NEW.proposal_id OR approval_revision <> NEW.revision_id
       OR approval_change_hash <> NEW.approved_change_hash OR approval_decision <> 'approved'
       OR approval_git_head IS NULL OR approval_git_head <> NEW.approved_git_head THEN
        RAISE EXCEPTION 'writeback proposal approval binding mismatch' USING ERRCODE = '23514';
    END IF;

    SELECT r.workspace_id, n.id INTO run_workspace, bound_node
      FROM workflow.run r
      JOIN workflow.node_run n ON n.run_id = r.id
     WHERE r.id = NEW.workflow_run_id AND n.id = NEW.node_run_id
       AND r.status IN ('pending','running','waiting_for_human')
       AND n.status IN ('pending','running','waiting_for_human')
     FOR UPDATE OF r, n;
    IF run_workspace IS NULL OR run_workspace <> NEW.workspace_id OR bound_node IS NULL THEN
        RAISE EXCEPTION 'writeback workflow binding mismatch' USING ERRCODE = '23514';
    END IF;

    SELECT * INTO write_authorization FROM change_control.tool_authorization WHERE id = NEW.write_authorization_id FOR UPDATE;
    SELECT * INTO git_authorization FROM change_control.tool_authorization WHERE id = NEW.git_authorization_id FOR UPDATE;
    IF write_authorization.id IS NULL OR git_authorization.id IS NULL
       OR write_authorization.workspace_id <> NEW.workspace_id OR git_authorization.workspace_id <> NEW.workspace_id
       OR write_authorization.workflow_run_id <> NEW.workflow_run_id OR git_authorization.workflow_run_id <> NEW.workflow_run_id
       OR write_authorization.node_run_id <> NEW.node_run_id OR git_authorization.node_run_id <> NEW.node_run_id
       OR write_authorization.proposal_id <> NEW.proposal_id OR git_authorization.proposal_id <> NEW.proposal_id
       OR write_authorization.revision_id <> NEW.revision_id OR git_authorization.revision_id <> NEW.revision_id
       OR write_authorization.approval_id <> NEW.approval_id OR git_authorization.approval_id <> NEW.approval_id
       OR write_authorization.tool_name <> 'ApplyApprovedPatch' OR write_authorization.capability <> 'WRITE_KNOWLEDGE'
       OR git_authorization.tool_name <> 'CreateGitCommit' OR git_authorization.capability <> 'GIT_WRITE'
       OR write_authorization.scope <> 'target:' || NEW.target_path OR git_authorization.scope <> 'target:' || NEW.target_path
       OR write_authorization.approved_change_hash <> NEW.approved_change_hash OR git_authorization.approved_change_hash <> NEW.approved_change_hash
       OR write_authorization.target_version <> NEW.base_hash OR git_authorization.target_version <> NEW.base_hash
       OR NOT (write_authorization.status = 'consumed' OR (write_authorization.status = 'issued' AND CURRENT_TIMESTAMP < write_authorization.expires_at))
       OR NOT (git_authorization.status = 'consumed' OR (git_authorization.status = 'issued' AND CURRENT_TIMESTAMP < git_authorization.expires_at)) THEN
        RAISE EXCEPTION 'writeback authorization binding mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

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

CREATE OR REPLACE FUNCTION change_control.reject_writeback_execution_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'writeback execution is append-preserving' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS writeback_execution_verify_binding ON change_control.writeback_execution;
CREATE TRIGGER writeback_execution_verify_binding
BEFORE INSERT ON change_control.writeback_execution
FOR EACH ROW EXECUTE FUNCTION change_control.verify_writeback_execution_binding();

DROP TRIGGER IF EXISTS writeback_execution_validate_transition ON change_control.writeback_execution;
CREATE TRIGGER writeback_execution_validate_transition
BEFORE UPDATE ON change_control.writeback_execution
FOR EACH ROW EXECUTE FUNCTION change_control.validate_writeback_execution_transition();

DROP TRIGGER IF EXISTS writeback_execution_reject_delete ON change_control.writeback_execution;
CREATE TRIGGER writeback_execution_reject_delete
BEFORE DELETE ON change_control.writeback_execution
FOR EACH ROW EXECUTE FUNCTION change_control.reject_writeback_execution_delete();

CREATE TABLE IF NOT EXISTS change_control.proposal_commit (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    writeback_execution_id uuid NOT NULL REFERENCES change_control.writeback_execution(id) ON DELETE RESTRICT,
    proposal_id uuid NOT NULL REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    revision_id uuid NOT NULL REFERENCES change_control.proposal_revision(id) ON DELETE RESTRICT,
    approval_id uuid NOT NULL REFERENCES change_control.approval(id) ON DELETE RESTRICT,
    git_commit text NOT NULL CHECK (git_commit ~ '^[0-9a-f]{40}$' OR git_commit ~ '^[0-9a-f]{64}$'),
    parent_git_commit text NOT NULL CHECK (parent_git_commit ~ '^[0-9a-f]{40}$' OR parent_git_commit ~ '^[0-9a-f]{64}$'),
    target_path text NOT NULL CHECK (
        btrim(target_path) <> '' AND left(target_path, 1) <> '/'
        AND target_path !~ '(^|/)\.\.(/|$)' AND position(E'\\\\' in target_path) = 0
    ),
    diff_hash text NOT NULL CHECK (diff_hash ~ '^[0-9a-f]{64}$'),
    result_hash text NOT NULL CHECK (result_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_proposal_commit_execution UNIQUE (writeback_execution_id),
    CONSTRAINT uq_proposal_commit_revision UNIQUE (proposal_id, revision_id),
    CONSTRAINT uq_proposal_commit_git UNIQUE (workspace_id, git_commit)
);

CREATE INDEX IF NOT EXISTS idx_proposal_commit_proposal
    ON change_control.proposal_commit (proposal_id, revision_id, created_at, id);

CREATE OR REPLACE FUNCTION change_control.verify_proposal_commit_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    execution change_control.writeback_execution%ROWTYPE;
    proposal_status text;
    revision_proposal uuid;
    revision_target text;
    revision_change_hash text;
    approval_proposal uuid;
    approval_revision uuid;
    approval_change_hash text;
    approval_decision text;
    approval_git_head text;
BEGIN
    SELECT * INTO execution
      FROM change_control.writeback_execution
     WHERE id = NEW.writeback_execution_id
     FOR UPDATE;
    SELECT p.status, r.proposal_id, r.target_path, r.change_hash,
           a.proposal_id, a.revision_id, a.change_hash, a.decision, a.approved_git_head
      INTO proposal_status, revision_proposal, revision_target, revision_change_hash,
           approval_proposal, approval_revision, approval_change_hash, approval_decision, approval_git_head
      FROM change_control.proposal p
      JOIN change_control.proposal_revision r ON r.id = execution.revision_id
      JOIN change_control.approval a ON a.id = execution.approval_id
     WHERE p.id = execution.proposal_id
     FOR UPDATE OF p, r, a;
    IF execution.id IS NULL OR execution.status NOT IN ('git_committed','publish_recovery_required','verifying')
       OR NEW.workspace_id <> execution.workspace_id OR NEW.proposal_id <> execution.proposal_id
       OR NEW.revision_id <> execution.revision_id OR NEW.approval_id <> execution.approval_id
       OR NEW.git_commit <> execution.git_commit OR NEW.parent_git_commit <> execution.parent_git_commit
       OR NEW.target_path <> execution.target_path OR NEW.diff_hash <> execution.diff_hash
       OR NEW.result_hash <> execution.result_hash
       OR proposal_status NOT IN ('applied','verifying')
       OR revision_proposal <> execution.proposal_id OR revision_target <> execution.target_path
       OR revision_change_hash <> execution.approved_change_hash
       OR approval_proposal <> execution.proposal_id OR approval_revision <> execution.revision_id
       OR approval_change_hash <> execution.approved_change_hash OR approval_decision <> 'approved'
       OR approval_git_head IS NULL OR approval_git_head <> execution.approved_git_head THEN
        RAISE EXCEPTION 'proposal commit binding mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS proposal_commit_verify_binding ON change_control.proposal_commit;
CREATE TRIGGER proposal_commit_verify_binding
BEFORE INSERT ON change_control.proposal_commit
FOR EACH ROW EXECUTE FUNCTION change_control.verify_proposal_commit_binding();

DROP TRIGGER IF EXISTS proposal_commit_reject_update_delete ON change_control.proposal_commit;
CREATE TRIGGER proposal_commit_reject_update_delete
BEFORE UPDATE OR DELETE ON change_control.proposal_commit
FOR EACH ROW EXECUTE FUNCTION change_control.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION change_control.validate_writeback_reindex_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    execution change_control.writeback_execution%ROWTYPE;
    payload_key_count integer;
BEGIN
    IF NEW.event_type <> 'retrieval.revision.reindex_requested' THEN
        RETURN NEW;
    END IF;
    SELECT count(*) INTO payload_key_count FROM jsonb_object_keys(NEW.payload);
    IF NEW.run_id IS NULL OR payload_key_count <> 11
       OR NOT (NEW.payload ?& ARRAY[
            'schema_version','workspace_id','workflow_run_id','node_run_id','proposal_id','revision_id',
            'approval_id','writeback_execution_id','target_path','result_hash','git_commit'
       ])
       OR NEW.payload->'schema_version' <> '1'::jsonb THEN
        RAISE EXCEPTION 'writeback reindex payload schema mismatch' USING ERRCODE = '23514';
    END IF;

    SELECT * INTO execution
      FROM change_control.writeback_execution
     WHERE id::text = NEW.payload->>'writeback_execution_id'
     FOR UPDATE;
    IF execution.id IS NULL OR execution.status NOT IN ('git_committed','publish_recovery_required','verifying')
       OR NEW.workspace_id::text <> NEW.payload->>'workspace_id'
       OR NEW.run_id::text <> NEW.payload->>'workflow_run_id'
       OR execution.workspace_id <> NEW.workspace_id OR execution.workflow_run_id <> NEW.run_id
       OR execution.node_run_id::text <> NEW.payload->>'node_run_id'
       OR execution.proposal_id::text <> NEW.payload->>'proposal_id'
       OR execution.revision_id::text <> NEW.payload->>'revision_id'
       OR execution.approval_id::text <> NEW.payload->>'approval_id'
       OR execution.target_path <> NEW.payload->>'target_path'
       OR execution.result_hash <> NEW.payload->>'result_hash'
       OR execution.git_commit <> NEW.payload->>'git_commit'
       OR NEW.idempotency_key <> 'reindex:' || execution.workspace_id::text || ':' || execution.proposal_id::text || ':' || execution.revision_id::text || ':' || execution.git_commit THEN
        RAISE EXCEPTION 'writeback reindex event binding mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION change_control.guard_writeback_reindex_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.event_type = 'retrieval.revision.reindex_requested' THEN
            RAISE EXCEPTION 'writeback reindex event is append-preserving' USING ERRCODE = '55000';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.event_type <> 'retrieval.revision.reindex_requested'
       AND NEW.event_type = 'retrieval.revision.reindex_requested' THEN
        RAISE EXCEPTION 'writeback reindex event must be inserted through its validated boundary' USING ERRCODE = '55000';
    END IF;
    IF OLD.event_type = 'retrieval.revision.reindex_requested' AND (
        NEW.id <> OLD.id OR NEW.workspace_id <> OLD.workspace_id OR NEW.run_id IS DISTINCT FROM OLD.run_id
        OR NEW.event_type <> OLD.event_type OR NEW.idempotency_key <> OLD.idempotency_key
        OR NEW.payload <> OLD.payload OR NEW.occurred_at <> OLD.occurred_at
        OR (OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at)
    ) THEN
        RAISE EXCEPTION 'writeback reindex event identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS writeback_reindex_validate_insert ON workflow.outbox_event;
CREATE TRIGGER writeback_reindex_validate_insert
BEFORE INSERT ON workflow.outbox_event
FOR EACH ROW EXECUTE FUNCTION change_control.validate_writeback_reindex_insert();

DROP TRIGGER IF EXISTS writeback_reindex_guard_update ON workflow.outbox_event;
CREATE TRIGGER writeback_reindex_guard_update
BEFORE UPDATE ON workflow.outbox_event
FOR EACH ROW EXECUTE FUNCTION change_control.guard_writeback_reindex_mutation();

DROP TRIGGER IF EXISTS writeback_reindex_guard_delete ON workflow.outbox_event;
CREATE TRIGGER writeback_reindex_guard_delete
BEFORE DELETE ON workflow.outbox_event
FOR EACH ROW EXECUTE FUNCTION change_control.guard_writeback_reindex_mutation();

INSERT INTO core.schema_meta (key, value)
VALUES ('safe_writeback', 'm5-04a')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
