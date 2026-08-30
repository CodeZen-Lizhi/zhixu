ALTER TABLE change_control.proposal
    VALIDATE CONSTRAINT ck_proposal_risk_level;

ALTER TABLE change_control.proposal
    ALTER COLUMN risk_level DROP DEFAULT,
    ALTER COLUMN risk_level SET NOT NULL;

ALTER TABLE core.source_version
    VALIDATE CONSTRAINT ck_source_version_workspace_required;

ALTER TABLE core.source_version
    ALTER COLUMN workspace_id SET NOT NULL;

-- The expand-phase backfill seam is closed; only the pre-existing verified
-- Content Artifact legacy binding remains mutable.
CREATE OR REPLACE FUNCTION core.reject_source_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.content_artifact_id IS NULL
       AND NEW.content_artifact_id IS NOT NULL
       AND (to_jsonb(NEW) - 'content_artifact_id') = (to_jsonb(OLD) - 'content_artifact_id') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'source_version is immutable' USING ERRCODE = '55000';
END;
$$;

-- risk_level is a Proposal-level approval and filtering fact. It cannot drift
-- with later status transitions or free-text revision risk descriptions.
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
       OR NEW.proposal_type <> OLD.proposal_type
       OR NEW.risk_level <> OLD.risk_level
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

-- Add the Workspace-safe relation without scanning workflow.run while holding
-- the initial metadata lock, then validate all historical rows atomically.
ALTER TABLE workflow.run
    DROP CONSTRAINT IF EXISTS fk_workflow_run_definition_workspace,
    ADD CONSTRAINT fk_workflow_run_definition_workspace
        FOREIGN KEY (definition_id, workspace_id)
        REFERENCES workflow.definition(id, workspace_id)
        ON DELETE RESTRICT
        NOT VALID;

ALTER TABLE workflow.run
    VALIDATE CONSTRAINT fk_workflow_run_definition_workspace;

ALTER TABLE core.source_version
    DROP CONSTRAINT IF EXISTS fk_source_version_source_workspace,
    ADD CONSTRAINT fk_source_version_source_workspace
        FOREIGN KEY (source_id, workspace_id)
        REFERENCES core.source(id, workspace_id)
        ON DELETE RESTRICT
        NOT VALID,
    DROP CONSTRAINT IF EXISTS fk_source_version_artifact_workspace,
    ADD CONSTRAINT fk_source_version_artifact_workspace
        FOREIGN KEY (workspace_id, content_artifact_id)
        REFERENCES core.content_artifact(workspace_id, id)
        ON DELETE RESTRICT
        NOT VALID;

ALTER TABLE core.source_version
    VALIDATE CONSTRAINT fk_source_version_source_workspace;

ALTER TABLE core.source_version
    VALIDATE CONSTRAINT fk_source_version_artifact_workspace;

INSERT INTO core.schema_meta(key, value)
VALUES ('m9_business_contract_hardening', 'm9')
ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
