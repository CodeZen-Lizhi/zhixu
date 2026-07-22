-- +goose NO TRANSACTION
-- +goose Up

-- A failed CREATE INDEX CONCURRENTLY can leave an invalid relation behind.
-- Drop migration-owned names first so retry always rebuilds a valid index.
DROP INDEX CONCURRENTLY IF EXISTS change_control.idx_proposal_workspace_updated_id;
CREATE INDEX CONCURRENTLY idx_proposal_workspace_updated_id
    ON change_control.proposal (workspace_id, updated_at DESC, id DESC);

DROP INDEX CONCURRENTLY IF EXISTS workflow.idx_workflow_run_workspace_updated_id;
CREATE INDEX CONCURRENTLY idx_workflow_run_workspace_updated_id
    ON workflow.run (workspace_id, updated_at DESC, id DESC);

DROP INDEX CONCURRENTLY IF EXISTS workflow.idx_workflow_run_workspace_status_updated_id;
CREATE INDEX CONCURRENTLY idx_workflow_run_workspace_status_updated_id
    ON workflow.run (workspace_id, status, updated_at DESC, id DESC);

DROP INDEX CONCURRENTLY IF EXISTS core.idx_knowledge_topic_workspace_status_id;
CREATE INDEX CONCURRENTLY idx_knowledge_topic_workspace_status_id
    ON core.topic (workspace_id, status, id);

DROP INDEX CONCURRENTLY IF EXISTS core.idx_knowledge_claim_workspace_status_id;
CREATE INDEX CONCURRENTLY idx_knowledge_claim_workspace_status_id
    ON core.claim (workspace_id, status, id);

DROP INDEX CONCURRENTLY IF EXISTS core.idx_source_version_workspace_captured_id;
CREATE INDEX CONCURRENTLY idx_source_version_workspace_captured_id
    ON core.source_version (workspace_id, captured_at DESC, id DESC);

DROP INDEX CONCURRENTLY IF EXISTS ingestion.idx_ingestion_attempt_source_started_id;
CREATE INDEX CONCURRENTLY idx_ingestion_attempt_source_started_id
    ON ingestion.attempt (source_version_id, started_at DESC, id DESC)
    INCLUDE (status, security_status, workflow_run_id);

-- Keep this as a standalone unique index. The composite foreign key can use
-- it without transferring index ownership to a UNIQUE constraint.
DROP INDEX CONCURRENTLY IF EXISTS workflow.uq_workflow_definition_id_workspace;
CREATE UNIQUE INDEX CONCURRENTLY uq_workflow_definition_id_workspace
    ON workflow.definition (id, workspace_id);

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM change_control.proposal)
       OR EXISTS (SELECT 1 FROM core.source_version) THEN
        RAISE EXCEPTION 'cannot downgrade M9 business contract hardening while Proposal or Source Version data exists'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS workflow.uq_workflow_definition_id_workspace;
DROP INDEX CONCURRENTLY IF EXISTS ingestion.idx_ingestion_attempt_source_started_id;
DROP INDEX CONCURRENTLY IF EXISTS core.idx_source_version_workspace_captured_id;
DROP INDEX CONCURRENTLY IF EXISTS core.idx_knowledge_claim_workspace_status_id;
DROP INDEX CONCURRENTLY IF EXISTS core.idx_knowledge_topic_workspace_status_id;
DROP INDEX CONCURRENTLY IF EXISTS workflow.idx_workflow_run_workspace_status_updated_id;
DROP INDEX CONCURRENTLY IF EXISTS workflow.idx_workflow_run_workspace_updated_id;
DROP INDEX CONCURRENTLY IF EXISTS change_control.idx_proposal_workspace_updated_id;
