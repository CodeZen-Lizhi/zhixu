-- +goose Up

ALTER TABLE change_control.proposal
    ADD COLUMN IF NOT EXISTS workflow_run_id uuid;

ALTER TABLE workflow.run
    DROP CONSTRAINT IF EXISTS uq_workflow_run_id_workspace,
    ADD CONSTRAINT uq_workflow_run_id_workspace UNIQUE (id, workspace_id);

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS fk_proposal_workflow_run,
    ADD CONSTRAINT fk_proposal_workflow_run
        FOREIGN KEY (workflow_run_id, workspace_id) REFERENCES workflow.run(id, workspace_id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX IF NOT EXISTS uq_proposal_workflow_run
    ON change_control.proposal (workflow_run_id)
    WHERE workflow_run_id IS NOT NULL;

-- Extend the existing Proposal transition contract so first dispatch can bind
-- ready_for_review -> approved atomically and a historical approved Proposal
-- can receive its missing dispatch binding without changing business status.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    workflow_binding_added boolean;
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

    workflow_binding_added := OLD.workflow_run_id IS NULL AND NEW.workflow_run_id IS NOT NULL;
    IF workflow_binding_added
       AND NEW.status = 'approved'
       AND OLD.status IN ('ready_for_review', 'approved') THEN
        RETURN NEW;
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
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.guard_proposal_workflow_run_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_workspace_id uuid;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.workflow_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'proposal workflow run must be bound after proposal creation' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.workflow_run_id IS NOT NULL
       AND NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id THEN
        RAISE EXCEPTION 'proposal workflow run binding is immutable' USING ERRCODE = '55000';
    END IF;

    IF OLD.workflow_run_id IS NULL AND NEW.workflow_run_id IS NOT NULL THEN
        IF NEW.status <> 'approved'
           OR OLD.status NOT IN ('ready_for_review', 'approved') THEN
            RAISE EXCEPTION 'only an approved proposal can bind a workflow run' USING ERRCODE = '23514';
        END IF;

        SELECT workspace_id
          INTO run_workspace_id
          FROM workflow.run
         WHERE id = NEW.workflow_run_id;

        IF FOUND AND run_workspace_id <> NEW.workspace_id THEN
            RAISE EXCEPTION 'proposal and workflow run must belong to the same workspace' USING ERRCODE = '23514';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS proposal_guard_workflow_run_binding ON change_control.proposal;
CREATE TRIGGER proposal_guard_workflow_run_binding
BEFORE INSERT OR UPDATE OF workflow_run_id ON change_control.proposal
FOR EACH ROW EXECUTE FUNCTION change_control.guard_proposal_workflow_run_binding();

INSERT INTO core.schema_meta(key,value) VALUES ('approval_writeback_dispatch','m4-c')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    LOCK TABLE change_control.proposal IN ACCESS EXCLUSIVE MODE;

    IF EXISTS (
        SELECT 1
          FROM change_control.proposal
         WHERE workflow_run_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'cannot downgrade Approval Writeback Dispatch while Proposal bindings exist' USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DELETE FROM core.schema_meta WHERE key = 'approval_writeback_dispatch';

-- Restore the M5-04 Proposal transition contract after the binding column is
-- proven unused and before removing it.
-- +goose StatementBegin
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
-- +goose StatementEnd

DROP TRIGGER IF EXISTS proposal_guard_workflow_run_binding ON change_control.proposal;
DROP FUNCTION IF EXISTS change_control.guard_proposal_workflow_run_binding();
DROP INDEX IF EXISTS change_control.uq_proposal_workflow_run;

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS fk_proposal_workflow_run,
    DROP COLUMN IF EXISTS workflow_run_id;

ALTER TABLE workflow.run
    DROP CONSTRAINT IF EXISTS uq_workflow_run_id_workspace;
