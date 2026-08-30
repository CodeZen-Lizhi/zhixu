CREATE TABLE IF NOT EXISTS retrieval.embedding_cache (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    embedding_version_id uuid NOT NULL REFERENCES retrieval.embedding_version(id) ON DELETE RESTRICT,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    embedding vector NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, embedding_version_id, content_hash)
);

CREATE OR REPLACE FUNCTION retrieval.validate_embedding_cache_statement()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM new_embedding_cache cache
          LEFT JOIN retrieval.embedding_version embedding_version
            ON embedding_version.id = cache.embedding_version_id
         WHERE embedding_version.id IS NULL
            OR vector_dims(cache.embedding) <> embedding_version.dimensions
            OR NOT (
                vector_norm(cache.embedding) > 0
                AND vector_norm(cache.embedding) < 'Infinity'::double precision
            )
            OR (
                embedding_version.normalization = 'l2'
                AND abs(vector_norm(cache.embedding) - 1.0) > 0.00001
            )
    ) THEN
        RAISE EXCEPTION 'retrieval embedding cache vector violates embedding version' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER retrieval_embedding_cache_validate_insert
    AFTER INSERT ON retrieval.embedding_cache
    REFERENCING NEW TABLE AS new_embedding_cache
    FOR EACH STATEMENT EXECUTE FUNCTION retrieval.validate_embedding_cache_statement();

CREATE TRIGGER retrieval_embedding_cache_reject_mutation
    BEFORE UPDATE OR DELETE ON retrieval.embedding_cache
    FOR EACH ROW EXECUTE FUNCTION retrieval.reject_immutable_mutation();

ALTER TABLE retrieval.reindex_delivery
    DROP CONSTRAINT IF EXISTS reindex_delivery_regression_code_check,
    ADD CONSTRAINT reindex_delivery_regression_code_check CHECK (
        regression_code IS NULL OR regression_code IN ('SNAPSHOT_STRUCTURE_V1', 'SNAPSHOT_STRUCTURE_V2')
    );

CREATE OR REPLACE FUNCTION retrieval.assert_reindex_completion(delivery_identity uuid)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM retrieval.reindex_delivery delivery
          JOIN workflow.outbox_event event
            ON event.id = delivery.outbox_event_id AND event.workspace_id = delivery.workspace_id
          JOIN change_control.writeback_execution execution
            ON execution.id = delivery.writeback_execution_id AND execution.workspace_id = delivery.workspace_id
          JOIN change_control.proposal proposal ON proposal.id = execution.proposal_id
          JOIN change_control.proposal_commit commit_mapping
            ON commit_mapping.writeback_execution_id = execution.id
          JOIN workflow.run workflow_run ON workflow_run.id = execution.workflow_run_id
          JOIN workflow.node_run workflow_node
            ON workflow_node.id = execution.node_run_id AND workflow_node.run_id = workflow_run.id
          JOIN core.source_version source_version ON source_version.id = delivery.source_version_id
          JOIN core.source source ON source.id = source_version.source_id AND source.workspace_id = delivery.workspace_id
          JOIN retrieval.index_version index_version
            ON index_version.id = delivery.index_version_id AND index_version.workspace_id = delivery.workspace_id
          JOIN retrieval.index_manifest_source source_manifest
            ON source_manifest.index_version_id = index_version.id
           AND source_manifest.source_id = source.id
           AND source_manifest.source_version_id = source_version.id
           AND source_manifest.parse_projection_id = delivery.parse_projection_id
           AND source_manifest.selection_status = 'included'
          JOIN retrieval.index_activation activation
            ON activation.id = delivery.activation_id
           AND activation.workspace_id = delivery.workspace_id
           AND activation.target_index_version_id = index_version.id
          JOIN retrieval.reindex_delivery_attempt delivery_attempt
            ON delivery_attempt.id = delivery.current_attempt_id
           AND delivery_attempt.delivery_id = delivery.id
           AND delivery_attempt.attempt_no = delivery.attempt_no
           AND delivery_attempt.dispatch_no = delivery.dispatch_no
           AND delivery_attempt.status = 'succeeded'
          JOIN ingestion.attempt ingestion_attempt
            ON ingestion_attempt.id = delivery_attempt.ingestion_attempt_id
           AND ingestion_attempt.workspace_id = delivery.workspace_id
           AND ingestion_attempt.source_version_id = delivery.source_version_id
           AND ingestion_attempt.parse_projection_id = delivery.parse_projection_id
         WHERE delivery.id = delivery_identity
           AND delivery.status = 'succeeded'
           AND (
                (delivery.regression_code = 'SNAPSHOT_STRUCTURE_V1' AND index_version.embedding_version_id IS NULL)
                OR
                (delivery.regression_code = 'SNAPSHOT_STRUCTURE_V2' AND index_version.embedding_version_id IS NOT NULL)
           )
           AND delivery.regression_hash IS NOT NULL
           AND delivery.regression_passed_at IS NOT NULL
           AND event.event_type = 'retrieval.revision.reindex_requested'
           AND event.published_at IS NOT NULL
           AND event.payload->>'workspace_id' = delivery.workspace_id::text
           AND event.payload->>'workflow_run_id' = execution.workflow_run_id::text
           AND event.payload->>'node_run_id' = execution.node_run_id::text
           AND event.payload->>'proposal_id' = execution.proposal_id::text
           AND event.payload->>'revision_id' = execution.revision_id::text
           AND event.payload->>'approval_id' = execution.approval_id::text
           AND event.payload->>'writeback_execution_id' = execution.id::text
           AND event.payload->>'target_path' = execution.target_path
           AND event.payload->>'result_hash' = execution.result_hash
           AND event.payload->>'git_commit' = execution.git_commit
           AND commit_mapping.proposal_id = execution.proposal_id
           AND commit_mapping.revision_id = execution.revision_id
           AND commit_mapping.approval_id = execution.approval_id
           AND commit_mapping.git_commit = execution.git_commit
           AND commit_mapping.target_path = execution.target_path
           AND commit_mapping.result_hash = execution.result_hash
           AND source_version.content_hash = execution.result_hash
           AND ingestion_attempt.status = 'chunked'
           AND ingestion_attempt.security_status = 'passed'
           AND execution.status = 'completed'
           AND execution.completed_at IS NOT NULL
           AND execution.cleanup_completed_at IS NOT NULL
           AND proposal.status = 'completed'
           AND proposal.workflow_run_id = workflow_run.id
           AND workflow_run.status = 'succeeded'
           AND workflow_node.status = 'succeeded'
           AND index_version.status = 'active'
           AND index_version.source_manifest_hash IS NOT NULL
           AND index_version.expected_source_count IS NOT NULL
           AND activation.kind = 'activate'
           AND activation.target_version = index_version.version
           AND delivery.excluded_source_count = (
                SELECT count(*) FROM retrieval.index_manifest_source excluded
                 WHERE excluded.index_version_id = index_version.id
                   AND excluded.selection_status = 'excluded'
           )
    ) THEN
        RAISE EXCEPTION 'reindex completion invariant is not closed' USING ERRCODE = '55000';
    END IF;
END;
$$;

INSERT INTO core.schema_meta(key, value)
VALUES ('embedding_hybrid_search', 'm6-c')
ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
