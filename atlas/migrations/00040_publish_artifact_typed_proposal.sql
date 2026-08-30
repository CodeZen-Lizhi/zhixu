-- publish_artifact is a third, explicit Proposal union member. It freezes an
-- Artifact revision for review only; it is not a file patch or a Document write.
ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_type,
    ADD CONSTRAINT ck_proposal_type CHECK (
        proposal_type IN ('file_patch', 'knowledge_change', 'publish_artifact')
    );

ALTER TABLE change_control.proposal_revision
    ADD COLUMN IF NOT EXISTS artifact_id uuid,
    ADD COLUMN IF NOT EXISTS artifact_revision_id uuid,
    ADD COLUMN IF NOT EXISTS artifact_revision_no bigint,
    ADD COLUMN IF NOT EXISTS artifact_version bigint,
    ADD COLUMN IF NOT EXISTS artifact_content_hash text,
    ADD COLUMN IF NOT EXISTS artifact_source_coverage jsonb;

ALTER TABLE change_control.proposal_revision
    ADD CONSTRAINT ck_proposal_revision_artifact_revision_no CHECK (
        artifact_revision_no IS NULL OR artifact_revision_no > 0
    ),
    ADD CONSTRAINT ck_proposal_revision_artifact_version CHECK (
        artifact_version IS NULL OR artifact_version > 0
    ),
    ADD CONSTRAINT ck_proposal_revision_artifact_content_hash CHECK (
        artifact_content_hash IS NULL OR artifact_content_hash ~ '^[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT ck_proposal_revision_artifact_source_coverage CHECK (
        CASE
            WHEN artifact_source_coverage IS NULL THEN true
            ELSE jsonb_typeof(artifact_source_coverage) = 'array'
                AND jsonb_array_length(artifact_source_coverage) > 0
                AND octet_length(artifact_source_coverage::text) <= 65536
        END
    );

-- Keep the payload union mutually exclusive even for direct SQL callers.
-- Detailed canonical validation (IDs, coverage states, sorting and hashes) is
-- repeated by the Go domain decoder before a value becomes observable.
CREATE OR REPLACE FUNCTION change_control.validate_proposal_revision_payload()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_type_value text;
BEGIN
    SELECT proposal_type
      INTO proposal_type_value
      FROM change_control.proposal
     WHERE id = NEW.proposal_id
     FOR SHARE;

    IF proposal_type_value IS NULL THEN
        RETURN NEW;
    END IF;

    IF proposal_type_value = 'file_patch' THEN
        IF NEW.target_path IS NULL
           OR NEW.base_hash IS NULL
           OR NEW.content IS NULL
           OR NEW.evidence_summary IS NULL
           OR NEW.target_refs IS NOT NULL
           OR NEW.base_versions IS NOT NULL
           OR NEW.change_set IS NOT NULL
           OR NEW.evidence_refs IS NOT NULL
           OR NEW.artifact_id IS NOT NULL
           OR NEW.artifact_revision_id IS NOT NULL
           OR NEW.artifact_revision_no IS NOT NULL
           OR NEW.artifact_version IS NOT NULL
           OR NEW.artifact_content_hash IS NOT NULL
           OR NEW.artifact_source_coverage IS NOT NULL
           OR NEW.schema_version IS NOT NULL THEN
            RAISE EXCEPTION 'file patch proposal revision requires file fields only' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF proposal_type_value = 'knowledge_change' THEN
        IF NEW.target_path IS NOT NULL
           OR NEW.base_hash IS NOT NULL
           OR NEW.content IS NOT NULL
           OR NEW.evidence_summary IS NOT NULL
           OR NEW.target_refs IS NULL
           OR NEW.base_versions IS NULL
           OR NEW.change_set IS NULL
           OR NEW.evidence_refs IS NULL
           OR NEW.artifact_id IS NOT NULL
           OR NEW.artifact_revision_id IS NOT NULL
           OR NEW.artifact_revision_no IS NOT NULL
           OR NEW.artifact_version IS NOT NULL
           OR NEW.artifact_content_hash IS NOT NULL
           OR NEW.artifact_source_coverage IS NOT NULL
           OR NEW.schema_version IS DISTINCT FROM 'knowledge-relation-change/v1' THEN
            RAISE EXCEPTION 'knowledge change proposal revision requires typed fields only' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.target_path IS NOT NULL
       OR NEW.base_hash IS NOT NULL
       OR NEW.content IS NOT NULL
       OR NEW.evidence_summary IS NOT NULL
       OR NEW.target_refs IS NOT NULL
       OR NEW.base_versions IS NOT NULL
       OR NEW.change_set IS NOT NULL
       OR NEW.evidence_refs IS NOT NULL
       OR NEW.artifact_id IS NULL
       OR NEW.artifact_revision_id IS NULL
       OR NEW.artifact_revision_no IS NULL
       OR NEW.artifact_version IS NULL
       OR NEW.artifact_content_hash IS NULL
       OR NEW.artifact_source_coverage IS NULL
       OR NEW.schema_version IS DISTINCT FROM 'artifact-publication/v1' THEN
        RAISE EXCEPTION 'publish artifact proposal revision requires frozen artifact fields only' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS proposal_revision_validate_payload ON change_control.proposal_revision;
CREATE TRIGGER proposal_revision_validate_payload
    BEFORE INSERT ON change_control.proposal_revision
    FOR EACH ROW EXECUTE FUNCTION change_control.validate_proposal_revision_payload();

-- A formal execution port does not exist yet. Any accidental workflow binding
-- or file/Git approval baseline for this type is therefore inconsistent.
CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    workflow_binding_added boolean;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'proposal must start at version one' USING ERRCODE = '23514';
        END IF;
        IF NEW.proposal_type = 'publish_artifact' AND NEW.workflow_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'publish artifact proposal cannot bind a writeback workflow' USING ERRCODE = '23514';
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
    IF NEW.proposal_type = 'publish_artifact' AND NEW.workflow_run_id IS NOT NULL THEN
        RAISE EXCEPTION 'publish artifact proposal cannot bind a writeback workflow' USING ERRCODE = '23514';
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

CREATE OR REPLACE FUNCTION change_control.validate_typed_proposal_approval()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_type_value text;
BEGIN
    SELECT proposal_type
      INTO proposal_type_value
      FROM change_control.proposal
     WHERE id = NEW.proposal_id
     FOR SHARE;

    IF proposal_type_value IN ('knowledge_change', 'publish_artifact')
       AND NEW.approved_git_head IS NOT NULL THEN
        RAISE EXCEPTION 'typed proposal approval cannot carry a git baseline' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS approval_validate_typed_proposal ON change_control.approval;
CREATE TRIGGER approval_validate_typed_proposal
    BEFORE INSERT ON change_control.approval
    FOR EACH ROW EXECUTE FUNCTION change_control.validate_typed_proposal_approval();
