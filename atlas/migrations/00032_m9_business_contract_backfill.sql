UPDATE core.source_version AS source_version
   SET workspace_id = source.workspace_id
  FROM core.source AS source
 WHERE source.id = source_version.source_id
   AND source_version.workspace_id IS NULL;

-- Fail the whole backfill transaction if a pre-existing non-NULL binding was
-- forged or if Source/Content Artifact ownership is otherwise inconsistent.
-- The preceding UPDATE is intentionally in the same migration transaction so no
-- partially backfilled Source Version can commit on failure.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM core.source_version AS source_version
          LEFT JOIN core.source AS source
            ON source.id = source_version.source_id
          LEFT JOIN core.content_artifact AS artifact
            ON artifact.id = source_version.content_artifact_id
         WHERE source_version.workspace_id IS DISTINCT FROM source.workspace_id
            OR (
                source_version.content_artifact_id IS NOT NULL
                AND source_version.workspace_id IS DISTINCT FROM artifact.workspace_id
            )
    ) THEN
        RAISE EXCEPTION 'source version workspace backfill found inconsistent ownership'
            USING ERRCODE = '23514';
    END IF;
END;
$$;

-- Temporarily allow only this transaction's risk-only historical update. The
-- final function definition is restored before commit, so concurrent sessions
-- never observe a weakened Proposal transition contract.
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

    IF NEW.id = OLD.id
       AND NEW.workspace_id = OLD.workspace_id
       AND NEW.idempotency_key = OLD.idempotency_key
       AND NEW.request_hash = OLD.request_hash
       AND NEW.proposal_type = OLD.proposal_type
       AND NEW.workflow_run_id IS NOT DISTINCT FROM OLD.workflow_run_id
       AND NEW.status = OLD.status
       AND NEW.created_at = OLD.created_at
       AND NEW.updated_at = OLD.updated_at
       AND NEW.version = OLD.version
       AND NEW.risk_level IN ('CRITICAL', 'HIGH', 'MEDIUM', 'LOW') THEN
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.request_hash <> OLD.request_hash
       OR NEW.proposal_type <> OLD.proposal_type
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

WITH latest_revision AS MATERIALIZED (
    -- Historical free text is not normalized into an approval level. Only an
    -- exact canonical token is preserved; every other value fails closed HIGH.
    SELECT DISTINCT ON (revision.proposal_id)
           revision.proposal_id,
           CASE revision.risk
               WHEN 'CRITICAL' THEN 'CRITICAL'
               WHEN 'HIGH' THEN 'HIGH'
               WHEN 'MEDIUM' THEN 'MEDIUM'
               WHEN 'LOW' THEN 'LOW'
               ELSE 'HIGH'
           END AS risk_level
      FROM change_control.proposal_revision AS revision
     ORDER BY revision.proposal_id, revision.revision_no DESC
), projected_risk AS (
    SELECT proposal.id,
           CASE
               -- knowledge_change has always represented a typed knowledge
               -- write and the post-M9 domain contract fixes it at HIGH,
               -- regardless of whether a historical Candidate backlink is
               -- still present.
               WHEN proposal.proposal_type = 'knowledge_change' THEN 'HIGH'
               ELSE COALESCE(latest_revision.risk_level, 'HIGH')
           END AS risk_level
      FROM change_control.proposal AS proposal
      LEFT JOIN latest_revision
        ON latest_revision.proposal_id = proposal.id
)
UPDATE change_control.proposal AS proposal
   SET risk_level = projected_risk.risk_level
  FROM projected_risk
 WHERE proposal.id = projected_risk.id
   AND proposal.risk_level IS DISTINCT FROM projected_risk.risk_level;

-- Restore the pre-contract Proposal transition function. Migration 00033 adds
-- risk_level to its immutable binding after the backfill transaction commits.
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
