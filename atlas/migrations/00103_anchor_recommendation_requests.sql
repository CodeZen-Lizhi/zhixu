CREATE TABLE organizing.anchor_recommendation_request (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
 note_id uuid NOT NULL,
 anchor_id uuid,
 basis_revision_id uuid NOT NULL,
 expected_scope_version bigint,
 kind text NOT NULL CHECK(kind IN ('INITIAL_SCOPE','SOURCE_ASSOCIATION','SCOPE_ADJUSTMENT')),
 source jsonb,
 evidence jsonb NOT NULL CHECK(jsonb_typeof(evidence)='array' AND jsonb_array_length(evidence) BETWEEN 1 AND 32),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 workflow_run_id uuid,
 node_run_id uuid,
 node_attempt_id uuid,
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128),
 status text NOT NULL CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','NO_RECOMMENDATION','FAILED','RECOVERY_REQUIRED')),
 model_run_id uuid,
	model_input_hash text,
	model_output bytea,
 proposal_id uuid,
 error_code text,
 retryable boolean NOT NULL DEFAULT false,
	retry_idempotency_key text,
	retry_request_hash text,
 version bigint NOT NULL CHECK(version>0),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL CHECK(updated_at>=created_at),
 UNIQUE(workspace_id,idempotency_key),
	UNIQUE(workspace_id,retry_idempotency_key),
	UNIQUE(workspace_id,node_attempt_id),
 FOREIGN KEY(note_id,workspace_id) REFERENCES organizing.synthesis_note(id,workspace_id),
 FOREIGN KEY(basis_revision_id,workspace_id,note_id) REFERENCES organizing.synthesis_revision(id,workspace_id,note_id),
 FOREIGN KEY(anchor_id,workspace_id) REFERENCES organizing.knowledge_anchor(id,workspace_id),
 FOREIGN KEY(model_run_id,workspace_id) REFERENCES agent.model_run(id,workspace_id),
 FOREIGN KEY(proposal_id,workspace_id,anchor_id) REFERENCES organizing.anchor_proposal(id,workspace_id,anchor_id),
 CHECK((kind='INITIAL_SCOPE' AND anchor_id IS NULL AND expected_scope_version IS NULL AND source IS NULL) OR
       (kind<>'INITIAL_SCOPE' AND anchor_id IS NOT NULL AND expected_scope_version IS NOT NULL AND source IS NOT NULL)),
	CHECK((status='PENDING' AND workflow_run_id IS NULL AND node_run_id IS NULL AND node_attempt_id IS NULL AND model_input_hash IS NULL AND model_output IS NULL AND model_run_id IS NULL AND proposal_id IS NULL AND error_code IS NULL AND ((retry_idempotency_key IS NULL AND retry_request_hash IS NULL) OR (retry_idempotency_key IS NOT NULL AND retry_request_hash ~ '^[0-9a-f]{64}$'))) IS TRUE OR
	       (status='RUNNING' AND workflow_run_id IS NOT NULL AND node_run_id IS NOT NULL AND node_attempt_id IS NOT NULL AND (model_input_hash ~ '^[0-9a-f]{64}$') IS TRUE AND model_output IS NULL AND model_run_id IS NULL AND proposal_id IS NULL AND error_code IS NULL) IS TRUE OR
	       (status='SUCCEEDED' AND (model_input_hash ~ '^[0-9a-f]{64}$') IS TRUE AND model_output IS NOT NULL AND model_run_id IS NOT NULL AND error_code IS NULL AND ((kind='INITIAL_SCOPE' AND proposal_id IS NULL) OR (kind<>'INITIAL_SCOPE' AND proposal_id IS NOT NULL))) IS TRUE OR
	       (status='NO_RECOMMENDATION' AND (model_input_hash ~ '^[0-9a-f]{64}$') IS TRUE AND model_output IS NOT NULL AND model_run_id IS NOT NULL AND proposal_id IS NULL AND error_code IS NULL) IS TRUE OR
       (status IN ('FAILED','RECOVERY_REQUIRED') AND proposal_id IS NULL AND error_code IS NOT NULL))
);
CREATE INDEX anchor_recommendation_request_page ON organizing.anchor_recommendation_request(workspace_id,note_id,id);
CREATE INDEX anchor_recommendation_request_anchor_page ON organizing.anchor_recommendation_request(workspace_id,anchor_id,id) WHERE anchor_id IS NOT NULL;

-- Preserve all prior ModelRun constraints and admit only claimed anchor requests.
ALTER TABLE agent.model_run DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (final_result_type IS NULL OR final_result_type IN (
        'relation_assessment','rag_answer','refusal','faithfulness_review','tool_request','clarification',
        'artifact_section','document_knowledge_profile','organizing_outline','organizing_document',
        'workspace_analysis_plan','workspace_analysis_answer','synthesis_delta','synthesis_semantic_review','synthesis_note_interview_plan','workspace_analysis_decision','anchor_recommendation'));

ALTER TABLE agent.model_run DROP CONSTRAINT IF EXISTS agent_model_run_retrieval_binding,
    ADD CONSTRAINT agent_model_run_retrieval_binding CHECK (
        retrieval_index_version_id IS NOT NULL OR (embedding_version_id IS NULL AND rerank_model_version IS NULL AND (
            (output_schema_id='agent.rag-answer' AND NOT (status='SUCCEEDED' AND final_result_type='rag_answer'))
            OR output_schema_id IN ('organizing.outline-generation','organizing.document-generation','agent.synthesis-delta','agent.synthesis-semantic-review','agent.synthesis-note-interview-plan','agent.workspace-analysis-decision','organizing.anchor-recommendation'))));


CREATE OR REPLACE FUNCTION agent.guard_model_run_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent model run is append-only' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'RUNNING' OR NEW.version <> 1 THEN
            RAISE EXCEPTION 'agent model run must start running at version one' USING ERRCODE = '23514';
        END IF;
        IF NEW.retrieval_index_version_id IS NULL
           AND NEW.output_schema_id NOT IN (
               'agent.rag-answer','organizing.outline-generation','organizing.document-generation',
               'agent.synthesis-delta','agent.synthesis-semantic-review','agent.synthesis-note-interview-plan','agent.workspace-analysis-decision','organizing.anchor-recommendation'
           ) THEN
            RAISE EXCEPTION 'only rag or organizing model run may defer retrieval binding' USING ERRCODE = '23514';
        END IF;
        IF NEW.retrieval_index_version_id IS NULL
           AND NEW.output_schema_id IN ('organizing.outline-generation','organizing.document-generation')
           AND NOT EXISTS (
               SELECT 1
               FROM organizing.generation AS generation
               WHERE generation.workspace_id = NEW.workspace_id
                 AND generation.workflow_run_id = NEW.workflow_run_id
                 AND generation.node_run_id = NEW.node_run_id
                 AND generation.node_attempt_id = NEW.node_attempt_id
                 AND generation.status = 'RUNNING'
                 AND (
                     (NEW.output_schema_id = 'organizing.outline-generation'
                         AND generation.generation_kind = 'OUTLINE')
                     OR (NEW.output_schema_id = 'organizing.document-generation'
                         AND generation.generation_kind = 'DOCUMENT')
                 )
           ) THEN
            RAISE EXCEPTION 'organizing model run requires an exact running generation binding'
                USING ERRCODE = '55000';
        END IF;
        IF NEW.output_schema_id IN ('agent.synthesis-delta','agent.synthesis-semantic-review')
           AND (NEW.output_schema_version<>'v1' OR NEW.retrieval_index_version_id IS NOT NULL
                OR NOT EXISTS (
                    SELECT 1 FROM organizing.synthesis_model_step step
                    WHERE step.workspace_id=NEW.workspace_id AND step.workflow_run_id=NEW.workflow_run_id
                      AND step.node_run_id=NEW.node_run_id AND step.node_attempt_id=NEW.node_attempt_id
                      AND step.status='RUNNING'
                      AND ((NEW.output_schema_id='agent.synthesis-delta' AND step.stage='GENERATE')
                        OR (NEW.output_schema_id='agent.synthesis-semantic-review' AND step.stage='VALIDATE'))
                )) THEN
            RAISE EXCEPTION 'synthesis model run requires an exact running step' USING ERRCODE='23514';
        END IF;
        IF NEW.output_schema_id='organizing.anchor-recommendation' AND (
           NEW.output_schema_version<>'v1' OR NEW.reduced_schema_id<>NEW.output_schema_id OR NEW.reduced_schema_version<>NEW.output_schema_version
           OR NEW.retrieval_index_version_id IS NOT NULL OR NEW.memory_snapshot_id IS NOT NULL
           OR NEW.prompt_template_id<>'organizing.anchor-recommendation' OR NEW.prompt_template_version<>'v1'
           OR NOT EXISTS (SELECT 1 FROM organizing.anchor_recommendation_request request
              WHERE request.workspace_id=NEW.workspace_id AND request.workflow_run_id=NEW.workflow_run_id
                AND request.node_run_id=NEW.node_run_id AND request.node_attempt_id=NEW.node_attempt_id
                AND request.status='RUNNING' AND request.model_input_hash IS NOT NULL)) THEN
           RAISE EXCEPTION 'anchor recommendation model requires an exact claimed request' USING ERRCODE='23514';
        END IF;
        IF NEW.output_schema_id='agent.synthesis-note-interview-plan' AND (
           NEW.output_schema_version<>'1' OR NEW.reduced_schema_id<>NEW.output_schema_id OR NEW.reduced_schema_version<>NEW.output_schema_version
           OR NEW.retrieval_index_version_id IS NOT NULL OR NEW.memory_snapshot_id IS NOT NULL
           OR NEW.prompt_template_id<>'interview.synthesis-note-plan' OR NEW.prompt_template_version<>'1'
           OR NOT EXISTS (SELECT 1 FROM learning.note_interview_preparation p WHERE p.workspace_id=NEW.workspace_id AND p.workflow_run_id=NEW.workflow_run_id
              AND p.node_run_id=NEW.node_run_id AND p.node_attempt_id=NEW.node_attempt_id AND p.status='GENERATING'
              AND p.model_settings_revision IS NOT DISTINCT FROM NEW.model_settings_revision)) THEN
           RAISE EXCEPTION 'note interview model requires an exact generating preparation' USING ERRCODE='23514';
        END IF;
        IF EXISTS (SELECT 1 FROM agent.workspace_analysis_run analysis JOIN workflow.node_run node ON node.run_id=analysis.workflow_run_id
            WHERE analysis.workspace_id=NEW.workspace_id AND analysis.workflow_run_id=NEW.workflow_run_id AND analysis.definition_version=2 AND node.id=NEW.node_run_id
              AND NOT ((node.node_key='decide_next' AND NEW.output_schema_id='agent.workspace-analysis-decision' AND NEW.output_schema_version='1')
                OR (node.node_key='synthesize_answer' AND NEW.output_schema_id='agent.workspace-analysis-candidate' AND NEW.output_schema_version='2')
                OR (node.node_key='review_publish' AND NEW.output_schema_id='agent.faithfulness-review' AND NEW.output_schema_version='v1'))) THEN
            RAISE EXCEPTION 'dynamic model schema differs from its frozen node contract' USING ERRCODE='23514';
        END IF;
        IF NEW.output_schema_id='agent.workspace-analysis-decision' AND (
            NEW.output_schema_version<>'1' OR NEW.reduced_schema_id<>NEW.output_schema_id OR NEW.reduced_schema_version<>NEW.output_schema_version
            OR NEW.retrieval_index_version_id IS NOT NULL OR NEW.memory_snapshot_id IS NOT NULL
            OR NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_run analysis
                JOIN workflow.node_run node ON node.run_id=analysis.workflow_run_id AND node.id=NEW.node_run_id AND node.node_key='decide_next'
                JOIN workflow.node_attempt attempt ON attempt.node_run_id=node.id AND attempt.id=NEW.node_attempt_id
                JOIN agent.workspace_analysis_operation operation ON operation.analysis_run_id=analysis.id AND operation.node_run_id=node.id
                    AND operation.node_key='decide_next' AND operation.operation_kind='DECISION' AND operation.status='PENDING'
                WHERE analysis.workspace_id=NEW.workspace_id AND analysis.workflow_run_id=NEW.workflow_run_id AND analysis.definition_version=2
                    AND analysis.status IN ('queued','running') AND node.status='running' AND attempt.status='running'
                    AND attempt.attempt_no=node.attempt AND attempt.lease_owner=node.lease_owner AND attempt.lease_until=node.lease_until
                    AND attempt.lease_until>clock_timestamp())) THEN
            RAISE EXCEPTION 'decision model run requires an exact dynamic pending operation and active attempt' USING ERRCODE='23514';
        END IF;
        IF NEW.memory_snapshot_id IS NULL AND EXISTS (
            SELECT 1
              FROM workflow.node_run node
             WHERE node.id = NEW.node_run_id
               AND node.run_id = NEW.workflow_run_id
               AND node.node_type = 'agent.rag-answer'
        ) THEN
            RAISE EXCEPTION 'conversation rag model run requires a memory snapshot binding' USING ERRCODE = '23514';
        END IF;
        IF NEW.memory_snapshot_id IS NOT NULL AND (
            NEW.output_schema_id <> 'agent.rag-answer'
            OR NOT EXISTS (
                SELECT 1
                  FROM workflow.node_run node
                 WHERE node.id = NEW.node_run_id
                   AND node.run_id = NEW.workflow_run_id
                   AND node.node_type = 'agent.rag-answer'
            )
        ) THEN
            RAISE EXCEPTION 'only conversation rag model run may bind a memory snapshot' USING ERRCODE = '23514';
        END IF;
        IF NEW.memory_snapshot_id IS NOT NULL AND NOT EXISTS (
            SELECT 1
              FROM agent.rag_memory_snapshot snapshot
             WHERE snapshot.id = NEW.memory_snapshot_id
               AND snapshot.workspace_id = NEW.workspace_id
               AND snapshot.workflow_run_id = NEW.workflow_run_id
               AND snapshot.node_run_id = NEW.node_run_id
               AND snapshot.node_attempt_id = NEW.node_attempt_id
               AND snapshot.status = 'PREPARING'
        ) THEN
            RAISE EXCEPTION 'rag model run memory snapshot is not preparing for this attempt' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status <> 'RUNNING'
       OR NEW.status = 'RUNNING'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.node_attempt_id IS DISTINCT FROM OLD.node_attempt_id
       OR NEW.adapter_name IS DISTINCT FROM OLD.adapter_name
       OR NEW.adapter_version IS DISTINCT FROM OLD.adapter_version
       OR NEW.model_id IS DISTINCT FROM OLD.model_id
       OR NEW.model_version IS DISTINCT FROM OLD.model_version
       OR NEW.profile_id IS DISTINCT FROM OLD.profile_id
       OR NEW.profile_version IS DISTINCT FROM OLD.profile_version
       OR NEW.prompt_template_id IS DISTINCT FROM OLD.prompt_template_id
       OR NEW.prompt_template_version IS DISTINCT FROM OLD.prompt_template_version
       OR NEW.output_schema_id IS DISTINCT FROM OLD.output_schema_id
       OR NEW.output_schema_version IS DISTINCT FROM OLD.output_schema_version
       OR NEW.reduced_schema_id IS DISTINCT FROM OLD.reduced_schema_id
       OR NEW.reduced_schema_version IS DISTINCT FROM OLD.reduced_schema_version
       OR NEW.memory_snapshot_id IS DISTINCT FROM OLD.memory_snapshot_id
       OR NEW.memory_context_schema_version IS DISTINCT FROM OLD.memory_context_schema_version
       OR NEW.memory_context_digest IS DISTINCT FROM OLD.memory_context_digest
       OR NEW.memory_context_item_count IS DISTINCT FROM OLD.memory_context_item_count
       OR NEW.memory_context_bytes IS DISTINCT FROM OLD.memory_context_bytes
       OR (OLD.retrieval_index_version_id IS NOT NULL AND (
            NEW.retrieval_index_version_id IS DISTINCT FROM OLD.retrieval_index_version_id
            OR NEW.embedding_version_id IS DISTINCT FROM OLD.embedding_version_id
            OR NEW.rerank_model_version IS DISTINCT FROM OLD.rerank_model_version
       ))
       OR (OLD.retrieval_index_version_id IS NULL AND (
            OLD.embedding_version_id IS NOT NULL
            OR OLD.rerank_model_version IS NOT NULL
            OR (NEW.retrieval_index_version_id IS NULL AND (
                NEW.embedding_version_id IS NOT NULL OR NEW.rerank_model_version IS NOT NULL
            ))
       ))
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'agent model run transition or immutable binding is invalid' USING ERRCODE = '55000';
    END IF;
    IF EXISTS (
        SELECT 1 FROM agent.model_call
        WHERE model_run_id = NEW.id AND status = 'STARTED'
    ) THEN
        RAISE EXCEPTION 'agent model run cannot finalize with an active call' USING ERRCODE = '55000';
    END IF;
    IF NEW.status = 'SUCCEEDED' AND NOT EXISTS (
        SELECT 1 FROM agent.model_call
        WHERE model_run_id = NEW.id AND status = 'SUCCEEDED'
    ) THEN
        RAISE EXCEPTION 'agent successful model run requires a succeeded call' USING ERRCODE = '55000';
    END IF;
    IF NEW.status = 'REFUSED'
       AND EXISTS (SELECT 1 FROM agent.model_call WHERE model_run_id = NEW.id)
       AND NOT EXISTS (
            SELECT 1 FROM agent.model_call
            WHERE model_run_id = NEW.id AND status = 'SUCCEEDED'
       ) THEN
        RAISE EXCEPTION 'agent refused model run with calls requires a succeeded call' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

