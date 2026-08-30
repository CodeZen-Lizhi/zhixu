CREATE INDEX IF NOT EXISTS idx_ops_server_event_rag_stage_lookup
    ON ops.server_event (workspace_id, resource_ref, seq DESC)
    WHERE event_type IN (
        'rag.plan.started',
        'rag.plan.completed',
        'rag.retrieval.started',
        'rag.retrieval.completed',
        'rag.validation.started',
        'rag.validation.completed'
    );
