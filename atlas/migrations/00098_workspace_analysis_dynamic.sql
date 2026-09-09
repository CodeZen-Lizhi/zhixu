-- workspace-analysis@2 owns a durable model-directed loop. Version 1 facts,
-- slots, result schemas and publication proof remain immutable and readable.
ALTER TABLE agent.workspace_analysis_run DROP CONSTRAINT workspace_analysis_run_contract,
    ADD CONSTRAINT workspace_analysis_run_contract CHECK (
        definition_key='workspace-analysis' AND max_tool_concurrency=1 AND max_cost_microunits IS NULL AND (
            (definition_version=1 AND policy_version=1 AND max_nodes=6 AND max_model_calls=3
             AND max_tool_calls=6 AND max_source_reads=3 AND max_input_tokens=196608 AND max_output_tokens BETWEEN 1281 AND 5376)
            OR (definition_version=2 AND policy_version=2 AND max_nodes=4 AND max_model_calls=14
             AND max_tool_calls=13 AND max_source_reads=8 AND max_input_tokens=917504 AND max_output_tokens BETWEEN 7169 AND 11264)
        )
    ),
    DROP CONSTRAINT workspace_analysis_run_lifecycle,
    ADD CONSTRAINT workspace_analysis_run_lifecycle CHECK (
        updated_at>=created_at AND deadline_at>created_at AND deadline_at<=created_at+interval '1 hour'
        AND deadline_at=created_at+(CASE definition_version WHEN 1 THEN
            git_tool_timeout_ms+5000+plan_model_timeout_ms+search_tool_timeout_ms+15000
            +3*source_read_tool_timeout_ms+10000+synthesis_model_timeout_ms+15000
            +validate_citation_tool_timeout_ms+10000+review_model_timeout_ms+15000
        WHEN 2 THEN
            12*(plan_model_timeout_ms+GREATEST(git_tool_timeout_ms,search_tool_timeout_ms,source_read_tool_timeout_ms,validate_citation_tool_timeout_ms)+5000)+15000
            +synthesis_model_timeout_ms+15000+validate_citation_tool_timeout_ms+10000+review_model_timeout_ms+15000
        END)*interval '1 millisecond'
        AND ((status IN ('queued','running') AND termination_reason IS NULL AND completed_at IS NULL)
          OR (status='succeeded' AND termination_reason='COMPLETED' AND validation_receipt_id IS NOT NULL
              AND review_model_run_id IS NOT NULL AND completed_at IS NOT NULL AND completed_at=updated_at AND completed_at<=deadline_at)
          OR (status IN ('refused','clarification_required','failed','cancelled') AND termination_reason IS NOT NULL AND completed_at IS NOT NULL AND completed_at=updated_at))
    );

ALTER TABLE agent.workspace_analysis_operation
    DROP CONSTRAINT workspace_analysis_operation_ordinal_check,
    ADD CONSTRAINT workspace_analysis_operation_ordinal_check CHECK (ordinal BETWEEN 1 AND 13),
    DROP CONSTRAINT workspace_analysis_operation_result_kind_check,
    ADD CONSTRAINT workspace_analysis_operation_result_kind_check CHECK (
        result_kind IS NULL OR result_kind IN ('MODEL_RESULT_RECEIPT','TOOL_RESULT_RECEIPT','SYNTHESIS_CANDIDATE','DECISION_RECEIPT')),
    DROP CONSTRAINT workspace_analysis_operation_slot,
    ADD CONSTRAINT workspace_analysis_operation_slot CHECK (
        (node_key='inspect_workspace' AND operation_kind='GIT_STATUS' AND ordinal=1 AND call_kind='TOOL')
        OR (node_key='retrieve_evidence' AND operation_kind='RETRIEVAL_PLAN' AND ordinal=1 AND call_kind='MODEL')
        OR (node_key='retrieve_evidence' AND operation_kind='KNOWLEDGE_SEARCH' AND ordinal=1 AND call_kind='TOOL')
        OR (node_key='read_evidence' AND operation_kind='SOURCE_READ' AND ordinal BETWEEN 1 AND 3 AND call_kind='TOOL')
        OR (node_key='synthesize_answer' AND operation_kind='ANSWER_SYNTHESIS' AND ordinal=1 AND call_kind='MODEL')
        OR (node_key='validate_citations' AND operation_kind='CITATION_VALIDATION' AND ordinal=1 AND call_kind='TOOL')
        OR (node_key='review_publish' AND operation_kind='FAITHFULNESS_REVIEW' AND ordinal=1 AND call_kind='MODEL')
        OR (node_key='decide_next' AND operation_kind='DECISION' AND ordinal BETWEEN 1 AND 13 AND call_kind='MODEL')
        OR (node_key='decide_next' AND operation_kind IN ('GIT_STATUS','KNOWLEDGE_SEARCH','SOURCE_READ','CITATION_VALIDATION')
            AND ordinal BETWEEN 1 AND 12 AND call_kind='TOOL')
    ),
    DROP CONSTRAINT workspace_analysis_operation_result_kind,
    ADD CONSTRAINT workspace_analysis_operation_result_kind CHECK (
        result_kind IS NULL OR (call_kind='TOOL' AND result_kind='TOOL_RESULT_RECEIPT')
        OR (operation_kind IN ('RETRIEVAL_PLAN','FAITHFULNESS_REVIEW') AND result_kind='MODEL_RESULT_RECEIPT')
        OR (operation_kind='ANSWER_SYNTHESIS' AND result_kind='SYNTHESIS_CANDIDATE')
        OR (operation_kind='DECISION' AND result_kind='DECISION_RECEIPT')
    );

ALTER TABLE agent.workspace_analysis_candidate
    DROP CONSTRAINT workspace_analysis_candidate_schema_version_check,
    ADD CONSTRAINT workspace_analysis_candidate_schema_version_check CHECK (schema_version IN (1,2));
ALTER TABLE agent.workspace_analysis_publication_proof
    ALTER COLUMN git_receipt_id DROP NOT NULL,
    ALTER COLUMN git_receipt_hash DROP NOT NULL,
    ADD CONSTRAINT workspace_analysis_publication_git_pair CHECK ((git_receipt_id IS NULL)=(git_receipt_hash IS NULL));
ALTER TABLE agent.workspace_analysis_termination_proof
    DROP CONSTRAINT workspace_analysis_termination_proof_artifact_kind_check,
    ADD CONSTRAINT workspace_analysis_termination_proof_artifact_kind_check CHECK (
        artifact_kind IS NULL OR artifact_kind IN ('TOOL_RECEIPT','MODEL_RESULT','DECISION_RECEIPT'));
ALTER TABLE agent.workspace_analysis_worker_capability
    DROP CONSTRAINT workspace_analysis_worker_capability_contract,
    ADD CONSTRAINT workspace_analysis_worker_capability_contract CHECK (
        definition_key='workspace-analysis' AND ((definition_version=1 AND policy_version=1) OR (definition_version=2 AND policy_version=2))
        AND definition_hash ~ '^[0-9a-f]{64}$' AND tool_catalog_hash ~ '^[0-9a-f]{64}$' AND config_revision>=0);

-- A decision belongs to an actual Call, not to a ModelRun's one-off final result.
-- This keeps the existing per-Attempt ModelRun and model_result uniqueness rules.
CREATE TABLE agent.workspace_analysis_decision (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    analysis_run_id uuid NOT NULL,
    operation_id uuid NOT NULL UNIQUE,
    node_attempt_id uuid NOT NULL REFERENCES workflow.node_attempt(id) ON DELETE RESTRICT,
    model_run_id uuid NOT NULL,
    model_call_id uuid NOT NULL UNIQUE REFERENCES agent.model_call(id) ON DELETE RESTRICT,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 1 AND 12),
    document bytea NOT NULL,
    document_hash text NOT NULL,
    document_bytes bigint NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT workspace_analysis_decision_integrity CHECK (
        document_bytes=octet_length(document) AND document_bytes BETWEEN 1 AND 4096
        AND document_hash=encode(sha256(document),'hex')),
    CONSTRAINT uq_workspace_analysis_decision_scope UNIQUE (id,analysis_run_id),
    CONSTRAINT uq_workspace_analysis_decision_ordinal UNIQUE (analysis_run_id,ordinal),
    CONSTRAINT fk_workspace_analysis_decision_run FOREIGN KEY (analysis_run_id,workspace_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_decision_operation FOREIGN KEY (operation_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_operation(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_decision_model_run FOREIGN KEY (model_run_id,workspace_id)
        REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT
);

CREATE TABLE agent.workspace_analysis_journal (
    workspace_id uuid NOT NULL,
    analysis_run_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence BETWEEN 1 AND 27),
    operation_id uuid NOT NULL UNIQUE,
    decision_id uuid,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (analysis_run_id,sequence),
    CONSTRAINT fk_workspace_analysis_journal_run FOREIGN KEY (analysis_run_id,workspace_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_journal_operation FOREIGN KEY (operation_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_operation(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_journal_decision FOREIGN KEY (decision_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_decision(id,analysis_run_id) ON DELETE RESTRICT
);

-- Global aliases only point to an immutable receipt-local tuple. The Tool owner
-- adds its version-4 private-document guards in the following migration.
CREATE TABLE agent.workspace_analysis_evidence (
    workspace_id uuid NOT NULL,
    analysis_run_id uuid NOT NULL,
    reference_no integer NOT NULL CHECK (reference_no BETWEEN 1 AND 32),
    search_operation_id uuid NOT NULL,
    search_receipt_id uuid NOT NULL,
    search_receipt_hash text NOT NULL CHECK (search_receipt_hash ~ '^[0-9a-f]{64}$'),
    local_evidence_ref text NOT NULL CHECK (local_evidence_ref ~ '^E[1-5]$'),
    citation_id text NOT NULL CHECK (length(citation_id) BETWEEN 1 AND 256),
    index_version_id uuid NOT NULL,
    chunk_id uuid NOT NULL,
    source_version_id uuid NOT NULL,
    source_span_id uuid NOT NULL,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (analysis_run_id,reference_no),
    CONSTRAINT uq_workspace_analysis_evidence_local UNIQUE (analysis_run_id,search_receipt_id,local_evidence_ref),
    CONSTRAINT fk_workspace_analysis_evidence_run FOREIGN KEY (analysis_run_id,workspace_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_evidence_operation FOREIGN KEY (search_operation_id,analysis_run_id)
        REFERENCES agent.workspace_analysis_operation(id,analysis_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_evidence_receipt FOREIGN KEY (search_receipt_id,workspace_id)
        REFERENCES workflow.tool_result_receipt(id,workspace_id) ON DELETE RESTRICT
);

CREATE FUNCTION agent.workspace_analysis_v2_decision_action_kind(action_value text)
RETURNS text LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT CASE action_value WHEN 'git_status' THEN 'GIT_STATUS' WHEN 'knowledge_search' THEN 'KNOWLEDGE_SEARCH'
        WHEN 'source_read' THEN 'SOURCE_READ' WHEN 'citation_validation' THEN 'CITATION_VALIDATION' END
$$;

CREATE FUNCTION agent.workspace_analysis_v2_refs_valid(refs jsonb, maximum_count integer)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
    SELECT COALESCE(jsonb_typeof(refs)='array' AND jsonb_array_length(refs) BETWEEN 1 AND maximum_count
        AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(refs) item
                        WHERE jsonb_typeof(item)<>'string' OR item#>>'{}' !~ '^E([1-9]|[12][0-9]|3[0-2])$')
        AND (SELECT count(DISTINCT item#>>'{}') FROM jsonb_array_elements(refs) item)=jsonb_array_length(refs),false)
$$;

CREATE FUNCTION agent.workspace_analysis_v2_operation_predecessor(target_operation_id uuid)
RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE
    op agent.workspace_analysis_operation%ROWTYPE;
    previous agent.workspace_analysis_operation%ROWTYPE;
    decision agent.workspace_analysis_decision%ROWTYPE;
    seq bigint;
    action_value text;
BEGIN
    SELECT * INTO op FROM agent.workspace_analysis_operation WHERE id=target_operation_id;
    IF op.id IS NULL OR NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_run WHERE id=op.analysis_run_id AND definition_version=2) THEN RETURN false; END IF;
    SELECT sequence INTO seq FROM agent.workspace_analysis_journal WHERE operation_id=op.id;
    IF seq IS NULL THEN SELECT COALESCE(max(sequence),0)+1 INTO seq FROM agent.workspace_analysis_journal WHERE analysis_run_id=op.analysis_run_id; END IF;
    IF seq=1 THEN RETURN op.node_key='decide_next' AND op.operation_kind='DECISION' AND op.ordinal=1; END IF;
    SELECT operation.* INTO previous FROM agent.workspace_analysis_journal journal
        JOIN agent.workspace_analysis_operation operation ON operation.id=journal.operation_id
        WHERE journal.analysis_run_id=op.analysis_run_id AND journal.sequence=seq-1;
    IF previous.id IS NULL OR previous.status<>'SUCCEEDED' THEN RETURN false; END IF;
    IF op.node_key='decide_next' AND op.operation_kind='DECISION' THEN
        RETURN previous.node_key='decide_next' AND previous.call_kind='TOOL' AND op.ordinal=previous.ordinal+1;
    ELSIF op.node_key='decide_next' AND op.call_kind='TOOL' THEN
        SELECT * INTO decision FROM agent.workspace_analysis_decision WHERE operation_id=previous.id;
        action_value:=convert_from(decision.document,'UTF8')::jsonb->>'action';
        RETURN previous.node_key='decide_next' AND previous.operation_kind='DECISION' AND previous.ordinal=op.ordinal
            AND decision.id IS NOT NULL AND agent.workspace_analysis_v2_decision_action_kind(action_value)=op.operation_kind;
    ELSIF op.node_key='synthesize_answer' AND op.operation_kind='ANSWER_SYNTHESIS' THEN
        SELECT * INTO decision FROM agent.workspace_analysis_decision WHERE operation_id=previous.id;
        RETURN previous.node_key='decide_next' AND previous.operation_kind='DECISION' AND decision.id IS NOT NULL
            AND convert_from(decision.document,'UTF8')::jsonb->>'action'='finish'
            AND EXISTS (SELECT 1 FROM agent.workspace_analysis_operation WHERE analysis_run_id=op.analysis_run_id
                AND node_key='decide_next' AND operation_kind='SOURCE_READ' AND status='SUCCEEDED');
    ELSIF op.node_key='validate_citations' AND op.operation_kind='CITATION_VALIDATION' THEN
        RETURN previous.node_key='synthesize_answer' AND previous.operation_kind='ANSWER_SYNTHESIS'
            AND EXISTS (SELECT 1 FROM agent.workspace_analysis_candidate WHERE synthesis_operation_id=previous.id AND schema_version=2);
    ELSIF op.node_key='review_publish' AND op.operation_kind='FAITHFULNESS_REVIEW' THEN
        RETURN previous.node_key='validate_citations' AND previous.operation_kind='CITATION_VALIDATION'
            AND agent.workspace_analysis_validation_all_valid(op.analysis_run_id);
    END IF;
    RETURN false;
END;
$$;

CREATE FUNCTION agent.guard_workspace_analysis_journal_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    op agent.workspace_analysis_operation%ROWTYPE;
    expected_sequence bigint;
    selected_decision_id uuid;
BEGIN
    PERFORM 1 FROM agent.workspace_analysis_run WHERE id=NEW.analysis_run_id AND workspace_id=NEW.workspace_id AND definition_version=2 FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'dynamic journal requires a version two run' USING ERRCODE='23514'; END IF;
    SELECT * INTO op FROM agent.workspace_analysis_operation WHERE id=NEW.operation_id AND analysis_run_id=NEW.analysis_run_id;
    SELECT COALESCE(max(sequence),0)+1 INTO expected_sequence FROM agent.workspace_analysis_journal WHERE analysis_run_id=NEW.analysis_run_id;
    IF op.node_key='decide_next' AND op.call_kind='TOOL' THEN
        SELECT id INTO selected_decision_id FROM agent.workspace_analysis_decision WHERE analysis_run_id=NEW.analysis_run_id AND ordinal=op.ordinal;
    END IF;
    IF op.id IS NULL OR op.workspace_id<>NEW.workspace_id OR op.status<>'PENDING' OR NEW.sequence<>expected_sequence
       OR NEW.created_at IS DISTINCT FROM op.created_at OR NEW.decision_id IS DISTINCT FROM selected_decision_id
       OR NOT agent.workspace_analysis_v2_operation_predecessor(op.id) THEN
        RAISE EXCEPTION 'dynamic journal is not the next exact operation' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION agent.append_workspace_analysis_operation_journal()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE seq bigint; selected_decision_id uuid;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_run WHERE id=NEW.analysis_run_id AND definition_version=2) THEN RETURN NEW; END IF;
    PERFORM 1 FROM agent.workspace_analysis_run WHERE id=NEW.analysis_run_id FOR UPDATE;
    SELECT COALESCE(max(sequence),0)+1 INTO seq FROM agent.workspace_analysis_journal WHERE analysis_run_id=NEW.analysis_run_id;
    IF NEW.node_key='decide_next' AND NEW.call_kind='TOOL' THEN
        SELECT id INTO selected_decision_id FROM agent.workspace_analysis_decision WHERE analysis_run_id=NEW.analysis_run_id AND ordinal=NEW.ordinal;
    END IF;
    INSERT INTO agent.workspace_analysis_journal(workspace_id,analysis_run_id,sequence,operation_id,decision_id,created_at)
        VALUES(NEW.workspace_id,NEW.analysis_run_id,seq,NEW.id,selected_decision_id,NEW.created_at);
    RETURN NEW;
END;
$$;

CREATE TRIGGER workspace_analysis_journal_guard_insert BEFORE INSERT ON agent.workspace_analysis_journal
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_journal_insert();
CREATE TRIGGER workspace_analysis_operation_append_journal AFTER INSERT ON agent.workspace_analysis_operation
    FOR EACH ROW EXECUTE FUNCTION agent.append_workspace_analysis_operation_journal();
CREATE TRIGGER workspace_analysis_journal_append_only BEFORE UPDATE OR DELETE ON agent.workspace_analysis_journal
    FOR EACH ROW EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER workspace_analysis_journal_reject_truncate BEFORE TRUNCATE ON agent.workspace_analysis_journal
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER workspace_analysis_decision_append_only BEFORE UPDATE OR DELETE ON agent.workspace_analysis_decision
    FOR EACH ROW EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER workspace_analysis_decision_reject_truncate BEFORE TRUNCATE ON agent.workspace_analysis_decision
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER workspace_analysis_evidence_append_only BEFORE UPDATE OR DELETE ON agent.workspace_analysis_evidence
    FOR EACH ROW EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();
CREATE TRIGGER workspace_analysis_evidence_reject_truncate BEFORE TRUNCATE ON agent.workspace_analysis_evidence
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_fact_mutation();


-- Versioned extensions preserve the full historical guards.
ALTER TABLE agent.model_run DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (final_result_type IS NULL OR final_result_type IN (
        'relation_assessment','rag_answer','refusal','faithfulness_review','tool_request','clarification',
        'artifact_section','document_knowledge_profile','organizing_outline','organizing_document',
        'workspace_analysis_plan','workspace_analysis_answer','synthesis_delta','synthesis_semantic_review','synthesis_note_interview_plan','workspace_analysis_decision'));

ALTER TABLE agent.model_run DROP CONSTRAINT IF EXISTS agent_model_run_retrieval_binding,
    ADD CONSTRAINT agent_model_run_retrieval_binding CHECK (
        retrieval_index_version_id IS NOT NULL OR (embedding_version_id IS NULL AND rerank_model_version IS NULL AND (
            (output_schema_id='agent.rag-answer' AND NOT (status='SUCCEEDED' AND final_result_type='rag_answer'))
            OR output_schema_id IN ('organizing.outline-generation','organizing.document-generation','agent.synthesis-delta','agent.synthesis-semantic-review','agent.synthesis-note-interview-plan','agent.workspace-analysis-decision'))));


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
               'agent.synthesis-delta','agent.synthesis-semantic-review','agent.synthesis-note-interview-plan','agent.workspace-analysis-decision'
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

CREATE OR REPLACE FUNCTION agent.is_workspace_analysis_operation_next(target_operation_id uuid)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, agent, workflow
AS $$
DECLARE
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    plan_json jsonb;
    selected_refs jsonb;
BEGIN
    SELECT * INTO operation_row
      FROM agent.workspace_analysis_operation
     WHERE id=target_operation_id
       AND status='PENDING'
       AND first_node_attempt_id IS NULL
       AND latest_node_attempt_id IS NULL;
    IF operation_row.id IS NULL
       OR EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation AS terminal
             WHERE terminal.analysis_run_id=operation_row.analysis_run_id
               AND terminal.id<>operation_row.id
               AND terminal.status IN ('FAILED','UNKNOWN')
       ) THEN
        RETURN false;
    END IF;

    IF EXISTS (SELECT 1 FROM agent.workspace_analysis_run WHERE id=operation_row.analysis_run_id AND definition_version=2) THEN
        RETURN agent.workspace_analysis_v2_operation_predecessor(target_operation_id);
    END IF;
    CASE operation_row.operation_kind
        WHEN 'GIT_STATUS' THEN
            RETURN NOT EXISTS (
                SELECT 1 FROM agent.workspace_analysis_operation AS predecessor
                 WHERE predecessor.analysis_run_id=operation_row.analysis_run_id
                   AND predecessor.id<>operation_row.id
                   AND predecessor.status<>'PENDING'
            );
        WHEN 'RETRIEVAL_PLAN' THEN
            RETURN EXISTS (
                SELECT 1 FROM agent.workspace_analysis_operation
                 WHERE analysis_run_id=operation_row.analysis_run_id
                   AND operation_kind='GIT_STATUS'
                   AND status='SUCCEEDED'
            );
        WHEN 'KNOWLEDGE_SEARCH' THEN
            SELECT convert_from(result.document,'UTF8')::jsonb INTO plan_json
              FROM agent.workspace_analysis_operation AS plan
              JOIN agent.workspace_analysis_model_result AS result
                ON result.id=plan.result_id
               AND result.operation_id=plan.id
             WHERE plan.analysis_run_id=operation_row.analysis_run_id
               AND plan.operation_kind='RETRIEVAL_PLAN'
               AND plan.status='SUCCEEDED';
            RETURN plan_json#>'{payload,requires_clarification}' IS NOT DISTINCT FROM 'false'::jsonb;
        WHEN 'SOURCE_READ' THEN
            selected_refs := agent.workspace_analysis_selected_refs(operation_row.analysis_run_id);
            RETURN selected_refs IS NOT NULL
               AND operation_row.ordinal<=jsonb_array_length(selected_refs)
               AND agent.workspace_analysis_source_prefix_matches(
                    operation_row.analysis_run_id,selected_refs,operation_row.ordinal-1
               );
        WHEN 'ANSWER_SYNTHESIS' THEN
            selected_refs := agent.workspace_analysis_selected_refs(operation_row.analysis_run_id);
            RETURN selected_refs IS NOT NULL
               AND agent.workspace_analysis_source_prefix_matches(
                    operation_row.analysis_run_id,selected_refs,jsonb_array_length(selected_refs)
               );
        WHEN 'CITATION_VALIDATION' THEN
            RETURN EXISTS (
                SELECT 1
                  FROM agent.workspace_analysis_operation AS synthesis
                  JOIN agent.workspace_analysis_candidate AS candidate
                    ON candidate.id=synthesis.result_id
                   AND candidate.synthesis_operation_id=synthesis.id
                   AND candidate.analysis_run_id=synthesis.analysis_run_id
                 WHERE synthesis.analysis_run_id=operation_row.analysis_run_id
                   AND synthesis.operation_kind='ANSWER_SYNTHESIS'
                   AND synthesis.status='SUCCEEDED'
                   AND synthesis.result_hash=candidate.document_hash
            );
        WHEN 'FAITHFULNESS_REVIEW' THEN
            RETURN agent.workspace_analysis_validation_all_valid(operation_row.analysis_run_id);
        ELSE
            RETURN false;
    END CASE;
END;
$$;

CREATE OR REPLACE FUNCTION agent.guard_workspace_analysis_operation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    run_status_value text;
    node_key_value text;
    node_status text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    workflow_status text;
    workflow_cancel_requested_at timestamptz;
    attempt_status text;
    attempt_no integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
    call_status text;
    model_run_status text;
    model_run_final_result_type text;
    call_request_hash text;
    call_response_hash text;
    call_workspace_id uuid;
    call_workflow_run_id uuid;
    call_node_run_id uuid;
    call_node_attempt_id uuid;
    tool_name_value text;
    tool_version_value bigint;
    model_phase_value text;
    model_result_hash text;
    receipt_hash text;
    candidate_hash text;
    reservation_status text;
    terminal_replay boolean;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'workspace analysis operation is append-only' USING ERRCODE='55000';
    END IF;
    SELECT * INTO run_row FROM agent.workspace_analysis_run
      WHERE id=NEW.analysis_run_id AND workspace_id=NEW.workspace_id AND workflow_run_id=NEW.workflow_run_id FOR SHARE;
    IF run_row.id IS NULL
       OR (run_row.definition_version=1 AND NEW.node_key='decide_next')
       OR (run_row.definition_version=2 AND NEW.node_key IN ('inspect_workspace','retrieve_evidence','read_evidence'))
       OR (run_row.definition_version=2 AND NEW.operation_kind='DECISION' AND NEW.status<>'PENDING' AND NEW.ordinal>12) THEN
        RAISE EXCEPTION 'workspace analysis operation does not belong to its definition version' USING ERRCODE='23514';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.status<>'PENDING' OR NEW.version<>1 THEN
            RAISE EXCEPTION 'workspace analysis operation must start pending at version one' USING ERRCODE='23514';
        END IF;
        SELECT analysis.status,node.node_key,node.status,workflow.status,workflow.cancel_requested_at
          INTO run_status_value,node_key_value,node_status,workflow_status,workflow_cancel_requested_at
          FROM agent.workspace_analysis_run AS analysis
          JOIN workflow.run AS workflow
            ON workflow.id=analysis.workflow_run_id
           AND workflow.workspace_id=analysis.workspace_id
          JOIN workflow.node_run AS node
            ON node.run_id=workflow.id
         WHERE analysis.id=NEW.analysis_run_id
           AND analysis.workspace_id=NEW.workspace_id
           AND analysis.workflow_run_id=NEW.workflow_run_id
           AND node.id=NEW.node_run_id
         FOR SHARE OF analysis,workflow,node;
        IF run_status_value IS NULL
           OR run_status_value NOT IN ('queued','running')
           OR workflow_status NOT IN ('pending','running','retry_wait')
           OR workflow_cancel_requested_at IS NOT NULL
           OR node_status NOT IN ('pending','running','retry_wait')
           OR node_key_value IS DISTINCT FROM NEW.node_key THEN
            RAISE EXCEPTION 'workspace analysis operation node binding is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    terminal_replay := OLD.status IN ('SUCCEEDED','FAILED','UNKNOWN') AND NEW.status=OLD.status;
    IF (OLD.status NOT IN ('PENDING','STARTED') AND NOT terminal_replay)
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.analysis_run_id IS DISTINCT FROM OLD.analysis_run_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.node_key IS DISTINCT FROM OLD.node_key
       OR NEW.operation_kind IS DISTINCT FROM OLD.operation_kind
       OR NEW.ordinal IS DISTINCT FROM OLD.ordinal
       OR NEW.call_kind IS DISTINCT FROM OLD.call_kind
       OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at
       OR (OLD.first_node_attempt_id IS NOT NULL AND NEW.first_node_attempt_id IS DISTINCT FROM OLD.first_node_attempt_id)
       OR (OLD.model_call_id IS NOT NULL AND NEW.model_call_id IS DISTINCT FROM OLD.model_call_id)
       OR (OLD.tool_call_id IS NOT NULL AND NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id) THEN
        RAISE EXCEPTION 'workspace analysis operation immutable binding changed' USING ERRCODE='55000';
    END IF;
    IF terminal_replay AND (
            NEW.latest_node_attempt_id IS NOT DISTINCT FROM OLD.latest_node_attempt_id
            OR NEW.result_kind IS DISTINCT FROM OLD.result_kind
            OR NEW.result_id IS DISTINCT FROM OLD.result_id
            OR NEW.result_hash IS DISTINCT FROM OLD.result_hash
            OR NEW.error_code IS DISTINCT FROM OLD.error_code
            OR NEW.started_at IS DISTINCT FROM OLD.started_at
            OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
       ) THEN
        RAISE EXCEPTION 'workspace analysis terminal replay changed durable result facts' USING ERRCODE='55000';
    END IF;

    IF OLD.status='PENDING' AND NEW.status<>'STARTED' THEN
        RAISE EXCEPTION 'workspace analysis operation must authorize before completion' USING ERRCODE='55000';
    END IF;
    IF OLD.status='PENDING'
       AND NOT agent.is_workspace_analysis_operation_next(NEW.id) THEN
        RAISE EXCEPTION 'workspace analysis operation is not the next v1 graph step' USING ERRCODE='55000';
    END IF;
    IF OLD.status='PENDING'
       AND NEW.first_node_attempt_id IS DISTINCT FROM NEW.latest_node_attempt_id THEN
        RAISE EXCEPTION 'workspace analysis first authorization attempt is inconsistent' USING ERRCODE='55000';
    END IF;
    IF OLD.status='STARTED' AND NEW.status NOT IN ('SUCCEEDED','FAILED','UNKNOWN') THEN
        RAISE EXCEPTION 'workspace analysis operation terminal transition is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.status IN ('STARTED','SUCCEEDED','FAILED','UNKNOWN') THEN
        SELECT workflow.status,workflow.cancel_requested_at,
               node.status,node.attempt,node.lease_owner,node.lease_until,
               attempt.status,attempt.attempt_no,attempt.lease_owner,attempt.lease_until
          INTO workflow_status,workflow_cancel_requested_at,
               node_status,node_attempt_no,node_lease_owner,node_lease_until,
               attempt_status,attempt_no,attempt_lease_owner,attempt_lease_until
          FROM workflow.run AS workflow
          JOIN workflow.node_run AS node
            ON node.run_id=workflow.id
           AND node.id=NEW.node_run_id
          JOIN workflow.node_attempt AS attempt
            ON attempt.node_run_id=node.id
           AND attempt.id=NEW.latest_node_attempt_id
         WHERE workflow.id=NEW.workflow_run_id
           AND workflow.workspace_id=NEW.workspace_id
         FOR SHARE OF workflow,node,attempt;
        SELECT * INTO run_row
          FROM agent.workspace_analysis_run
         WHERE id=NEW.analysis_run_id
           AND workspace_id=NEW.workspace_id
           AND workflow_run_id=NEW.workflow_run_id
         FOR SHARE;
        IF run_row.status<>'running'
           OR workflow_status<>'running'
           OR node_status<>'running'
           OR node_attempt_no IS DISTINCT FROM attempt_no
           OR node_lease_owner IS NULL
           OR node_lease_owner IS DISTINCT FROM btrim(node_lease_owner)
           OR node_lease_owner=''
           OR attempt_lease_owner IS DISTINCT FROM node_lease_owner
           OR node_lease_until IS NULL
           OR node_lease_until IS DISTINCT FROM attempt_lease_until
           OR attempt_status<>'running'
           OR attempt_lease_until IS NULL
           OR attempt_lease_until<=clock_timestamp() THEN
            RAISE EXCEPTION 'workspace analysis operation attempt fence is not active' USING ERRCODE='55000';
        END IF;
        IF NEW.status='STARTED'
           AND (run_row.deadline_at<=clock_timestamp() OR workflow_cancel_requested_at IS NOT NULL) THEN
            RAISE EXCEPTION 'workspace analysis operation authorization is not active' USING ERRCODE='55000';
        END IF;
    END IF;

    IF NEW.call_kind='MODEL' THEN
        SELECT call.status,model.status,model.final_result_type,call.request_hash,call.response_hash,call.phase,
               model.workspace_id,model.workflow_run_id,model.node_run_id,model.node_attempt_id
          INTO call_status,model_run_status,model_run_final_result_type,call_request_hash,call_response_hash,model_phase_value,
               call_workspace_id,call_workflow_run_id,call_node_run_id,call_node_attempt_id
          FROM agent.model_call AS call
          JOIN agent.model_run AS model ON model.id=call.model_run_id
         WHERE call.id=NEW.model_call_id
         FOR SHARE OF call,model;
    ELSE
        SELECT status,request_hash,response_hash,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
               requested_tool_name,tool_version
          INTO call_status,call_request_hash,call_response_hash,
               call_workspace_id,call_workflow_run_id,call_node_run_id,call_node_attempt_id,
               tool_name_value,tool_version_value
          FROM workflow.tool_call
         WHERE id=NEW.tool_call_id
         FOR SHARE;
    END IF;
    IF call_request_hash IS DISTINCT FROM NEW.request_hash
       OR call_workspace_id IS DISTINCT FROM NEW.workspace_id
       OR call_workflow_run_id IS DISTINCT FROM NEW.workflow_run_id
       OR call_node_run_id IS DISTINCT FROM NEW.node_run_id
       OR call_node_attempt_id IS DISTINCT FROM NEW.first_node_attempt_id
       OR (
            NEW.call_kind='MODEL'
            AND NOT (
                (NEW.operation_kind='RETRIEVAL_PLAN' AND model_phase_value='PLAN')
                OR (run_row.definition_version=2 AND NEW.operation_kind='DECISION' AND model_phase_value='AGENT')
                OR (NEW.operation_kind='ANSWER_SYNTHESIS' AND model_phase_value='ANSWER')
                OR (NEW.operation_kind='FAITHFULNESS_REVIEW' AND model_phase_value='REVIEW')
            )
       )
       OR (
            NEW.call_kind='TOOL'
            AND NOT (
                (NEW.operation_kind='GIT_STATUS' AND tool_name_value='ReadGitStatus' AND tool_version_value=CASE run_row.definition_version WHEN 2 THEN 3 ELSE 2 END)
                OR (NEW.operation_kind='KNOWLEDGE_SEARCH' AND tool_name_value='SearchKnowledge' AND tool_version_value=CASE run_row.definition_version WHEN 2 THEN 3 ELSE 2 END)
                OR (NEW.operation_kind='SOURCE_READ' AND tool_name_value='ReadSource' AND tool_version_value=CASE run_row.definition_version WHEN 2 THEN 4 ELSE 3 END)
                OR (NEW.operation_kind='CITATION_VALIDATION' AND tool_name_value='ValidateCitation' AND tool_version_value=CASE run_row.definition_version WHEN 2 THEN 4 ELSE 3 END)
            )
       ) THEN
        RAISE EXCEPTION 'workspace analysis operation call binding is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.status='STARTED' AND call_status<>'STARTED' THEN
        RAISE EXCEPTION 'workspace analysis operation requires a started call' USING ERRCODE='55000';
    ELSIF NEW.status='FAILED' THEN
        IF NEW.call_kind='MODEL' AND NEW.error_code='WORKSPACE_ANALYSIS_MODEL_REFUSED' THEN
            IF call_status<>'SUCCEEDED'
               OR model_run_status<>'REFUSED'
               OR model_run_final_result_type<>'refusal' THEN
                RAISE EXCEPTION 'workspace analysis model refusal proof is invalid' USING ERRCODE='55000';
            END IF;
        ELSIF NEW.error_code='WORKSPACE_ANALYSIS_RECEIPT_INVALID' THEN
            IF NEW.call_kind<>'TOOL'
               OR call_status<>'SUCCEEDED'
               OR NOT EXISTS (
                    SELECT 1
                      FROM workflow.tool_result_receipt_failure
                     WHERE operation_id=NEW.id
                       AND tool_call_id=NEW.tool_call_id
               )
               OR EXISTS (
                    SELECT 1
                      FROM workflow.tool_result_receipt
                     WHERE tool_call_id=NEW.tool_call_id
               ) THEN
                RAISE EXCEPTION 'workspace analysis receipt failure has no completed call' USING ERRCODE='55000';
            END IF;
        ELSIF call_status NOT IN ('FAILED','REFUSED') THEN
            RAISE EXCEPTION 'workspace analysis failed operation has no failed call' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.status='UNKNOWN' AND call_status<>'UNKNOWN' THEN
        RAISE EXCEPTION 'workspace analysis unknown operation has no unknown call' USING ERRCODE='55000';
    ELSIF NEW.status='SUCCEEDED' THEN
        IF call_status<>'SUCCEEDED' THEN
            RAISE EXCEPTION 'workspace analysis successful operation has no successful call' USING ERRCODE='55000';
        END IF;
        IF run_row.definition_version=2 AND NOT terminal_replay AND workflow_cancel_requested_at IS NOT NULL THEN
            RAISE EXCEPTION 'cancelled dynamic operation cannot publish a successful result' USING ERRCODE='55000';
        END IF;
        IF NEW.result_kind='DECISION_RECEIPT' THEN
            SELECT document_hash INTO model_result_hash FROM agent.workspace_analysis_decision
              WHERE id=NEW.result_id AND operation_id=NEW.id AND model_call_id=NEW.model_call_id FOR SHARE;
            IF model_result_hash IS DISTINCT FROM NEW.result_hash OR model_result_hash IS DISTINCT FROM call_response_hash
               OR run_row.definition_version<>2 OR model_run_status NOT IN ('RUNNING','SUCCEEDED','FAILED','UNKNOWN') THEN
                RAISE EXCEPTION 'dynamic decision result binding is invalid' USING ERRCODE='55000';
            END IF;
        ELSIF NEW.result_kind='MODEL_RESULT_RECEIPT' THEN
            SELECT document_hash INTO model_result_hash
              FROM agent.workspace_analysis_model_result
             WHERE id=NEW.result_id
               AND operation_id=NEW.id
               AND model_call_id=NEW.model_call_id
             FOR SHARE;
            IF model_result_hash IS DISTINCT FROM NEW.result_hash THEN
                RAISE EXCEPTION 'workspace analysis model result binding is invalid' USING ERRCODE='55000';
            END IF;
        ELSIF NEW.result_kind='TOOL_RESULT_RECEIPT' THEN
            SELECT output_hash INTO receipt_hash
              FROM workflow.tool_result_receipt
             WHERE id=NEW.result_id AND tool_call_id=NEW.tool_call_id
             FOR SHARE;
            IF receipt_hash IS DISTINCT FROM NEW.result_hash THEN
                RAISE EXCEPTION 'workspace analysis receipt result binding is invalid' USING ERRCODE='55000';
            END IF;
        ELSIF NEW.result_kind='SYNTHESIS_CANDIDATE' THEN
            SELECT document_hash INTO candidate_hash
              FROM agent.workspace_analysis_candidate
             WHERE id=NEW.result_id AND synthesis_operation_id=NEW.id
             FOR SHARE;
            IF candidate_hash IS DISTINCT FROM NEW.result_hash THEN
                RAISE EXCEPTION 'workspace analysis candidate result binding is invalid' USING ERRCODE='55000';
            END IF;
        END IF;
    END IF;

    IF NEW.status IN ('SUCCEEDED','FAILED','UNKNOWN') THEN
        SELECT status INTO reservation_status
          FROM agent.workspace_analysis_budget_reservation
         WHERE operation_id=NEW.id
         FOR SHARE;
        IF reservation_status IS NULL OR reservation_status='RESERVED'
           OR (NEW.status='UNKNOWN' AND reservation_status<>'UNKNOWN_CHARGED') THEN
            RAISE EXCEPTION 'workspace analysis operation budget is not settled' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION agent.guard_workspace_analysis_reservation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    run_row agent.workspace_analysis_run%ROWTYPE;
    call_status_value text;
    call_input_tokens bigint;
    call_output_tokens bigint;
    call_max_output_tokens integer;
    call_timeout_ms bigint;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'workspace analysis reservation is append-only' USING ERRCODE='55000';
    END IF;

    IF TG_OP='INSERT' THEN
        IF NEW.status<>'RESERVED' THEN
            RAISE EXCEPTION 'workspace analysis reservation must start reserved' USING ERRCODE='23514';
        END IF;
        SELECT * INTO operation_row
          FROM agent.workspace_analysis_operation
         WHERE id=NEW.operation_id AND analysis_run_id=NEW.analysis_run_id
         FOR SHARE;
        SELECT * INTO run_row
          FROM agent.workspace_analysis_run
         WHERE id=NEW.analysis_run_id AND workspace_id=NEW.workspace_id
         FOR SHARE;
        IF NEW.call_kind='MODEL' THEN
            SELECT max_output_tokens INTO call_max_output_tokens
              FROM agent.model_call
             WHERE id=NEW.model_call_id
             FOR SHARE;
        END IF;
        call_timeout_ms := CASE operation_row.operation_kind
            WHEN 'GIT_STATUS' THEN run_row.git_tool_timeout_ms
            WHEN 'RETRIEVAL_PLAN' THEN run_row.plan_model_timeout_ms
            WHEN 'DECISION' THEN run_row.plan_model_timeout_ms
            WHEN 'KNOWLEDGE_SEARCH' THEN run_row.search_tool_timeout_ms
            WHEN 'SOURCE_READ' THEN run_row.source_read_tool_timeout_ms
            WHEN 'ANSWER_SYNTHESIS' THEN run_row.synthesis_model_timeout_ms
            WHEN 'CITATION_VALIDATION' THEN run_row.validate_citation_tool_timeout_ms
            WHEN 'FAITHFULNESS_REVIEW' THEN run_row.review_model_timeout_ms
        END;
        IF operation_row.id IS NULL
           OR operation_row.status<>'STARTED'
           OR operation_row.call_kind<>NEW.call_kind
           OR operation_row.model_call_id IS DISTINCT FROM NEW.model_call_id
           OR operation_row.tool_call_id IS DISTINCT FROM NEW.tool_call_id
           OR run_row.id IS NULL
           OR run_row.status<>'running'
           OR call_timeout_ms IS NULL
           OR clock_timestamp()+(call_timeout_ms+run_row.durable_completion_margin_ms)*interval '1 millisecond'>run_row.deadline_at
           OR (NEW.call_kind='MODEL' AND NEW.reserved_output_tokens IS DISTINCT FROM call_max_output_tokens)
           OR NEW.reserved_cost_microunits IS NOT NULL
           OR (NEW.call_kind='MODEL' AND NEW.reserved_input_tokens<>65536)
           OR (operation_row.operation_kind='RETRIEVAL_PLAN' AND NEW.reserved_output_tokens<>256)
           OR (operation_row.operation_kind='ANSWER_SYNTHESIS' AND NEW.reserved_output_tokens<>
               run_row.max_output_tokens-CASE run_row.definition_version WHEN 2 THEN 7168 ELSE 1280 END)
           OR (operation_row.operation_kind='DECISION' AND (run_row.definition_version<>2 OR NEW.reserved_output_tokens<>512 OR operation_row.ordinal>12
               OR run_row.reserved_model_calls+run_row.settled_model_calls+2>run_row.max_model_calls
               OR run_row.reserved_input_tokens+run_row.settled_input_tokens+131072>run_row.max_input_tokens
               OR run_row.reserved_output_tokens+run_row.settled_output_tokens+run_row.max_output_tokens-6144>run_row.max_output_tokens))
           OR (run_row.definition_version=2 AND operation_row.node_key='decide_next' AND NEW.call_kind='TOOL'
               AND run_row.reserved_tool_calls+run_row.settled_tool_calls+1>run_row.max_tool_calls)
           OR (run_row.definition_version=2 AND operation_row.operation_kind='KNOWLEDGE_SEARCH'
               AND (SELECT count(*) FROM agent.workspace_analysis_evidence WHERE analysis_run_id=run_row.id)>=32)
           OR (operation_row.operation_kind='FAITHFULNESS_REVIEW' AND NEW.reserved_output_tokens<>1024)
           OR (NEW.reserved_source_reads=1)<>(operation_row.operation_kind='SOURCE_READ') THEN
            RAISE EXCEPTION 'workspace analysis reservation binding is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.status<>'RESERVED'
       OR NEW.status NOT IN ('SETTLED','UNKNOWN_CHARGED')
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.analysis_run_id IS DISTINCT FROM OLD.analysis_run_id
       OR NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.call_kind IS DISTINCT FROM OLD.call_kind
       OR NEW.model_call_id IS DISTINCT FROM OLD.model_call_id
       OR NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id
       OR NEW.reserved_model_calls IS DISTINCT FROM OLD.reserved_model_calls
       OR NEW.reserved_tool_calls IS DISTINCT FROM OLD.reserved_tool_calls
       OR NEW.reserved_source_reads IS DISTINCT FROM OLD.reserved_source_reads
       OR NEW.reserved_input_tokens IS DISTINCT FROM OLD.reserved_input_tokens
       OR NEW.reserved_output_tokens IS DISTINCT FROM OLD.reserved_output_tokens
       OR NEW.reserved_cost_microunits IS DISTINCT FROM OLD.reserved_cost_microunits
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'workspace analysis reservation transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.status='SETTLED' AND NEW.call_kind='MODEL' THEN
        SELECT status,input_tokens,output_tokens
          INTO call_status_value,call_input_tokens,call_output_tokens
          FROM agent.model_call
         WHERE id=NEW.model_call_id
         FOR SHARE;
        IF call_status_value NOT IN ('SUCCEEDED','FAILED')
           OR NEW.settled_model_calls<>1
           OR NEW.settled_tool_calls<>0
           OR NEW.settled_source_reads<>0
           OR NEW.settled_input_tokens IS DISTINCT FROM call_input_tokens
           OR NEW.settled_output_tokens IS DISTINCT FROM call_output_tokens THEN
            RAISE EXCEPTION 'workspace analysis model settlement does not match actual call usage' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.status='SETTLED' AND NEW.call_kind='TOOL' THEN
        SELECT status INTO call_status_value
          FROM workflow.tool_call
         WHERE id=NEW.tool_call_id
         FOR SHARE;
        IF call_status_value NOT IN ('SUCCEEDED','FAILED')
           OR NEW.settled_model_calls<>0
           OR NEW.settled_tool_calls<>1
           OR NEW.settled_source_reads<>NEW.reserved_source_reads
           OR NEW.settled_input_tokens<>0
           OR NEW.settled_output_tokens<>0 THEN
            RAISE EXCEPTION 'workspace analysis tool settlement does not match actual call usage' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

-- Dynamic references are usable only after the exact Search tuple has actually
-- been read successfully. The following migration owns receipt shape checking.
CREATE FUNCTION agent.workspace_analysis_v2_ref_read(target_run_id uuid, reference_value text)
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM agent.workspace_analysis_evidence evidence
        JOIN agent.workspace_analysis_run analysis ON analysis.id=evidence.analysis_run_id AND analysis.definition_version=2
        JOIN agent.workspace_analysis_operation operation ON operation.analysis_run_id=analysis.id AND operation.workspace_id=analysis.workspace_id
            AND operation.node_key='decide_next' AND operation.operation_kind='SOURCE_READ' AND operation.status='SUCCEEDED'
        JOIN workflow.tool_result_receipt receipt ON receipt.id=operation.result_id AND receipt.tool_call_id=operation.tool_call_id
            AND receipt.workspace_id=analysis.workspace_id AND receipt.workflow_run_id=analysis.workflow_run_id
            AND receipt.tool_name='ReadSource' AND receipt.tool_version=4 AND receipt.output_hash=operation.result_hash
        WHERE evidence.analysis_run_id=target_run_id AND 'E'||evidence.reference_no::text=reference_value
          AND convert_from(receipt.server_binding_document,'UTF8')::jsonb=jsonb_build_object(
            'search_receipt_id',evidence.search_receipt_id::text,'search_receipt_hash',evidence.search_receipt_hash,
            'search_evidence_ref',evidence.local_evidence_ref,'evidence_ref',reference_value,'citation_id',evidence.citation_id,
            'index_version_id',evidence.index_version_id::text,'chunk_id',evidence.chunk_id::text,
            'source_version_id',evidence.source_version_id::text,'source_span_id',evidence.source_span_id::text,'content_hash',evidence.content_hash)
          AND convert_from(receipt.output_document,'UTF8')::jsonb->>'evidence_ref'=reference_value
    )
$$;

CREATE FUNCTION agent.guard_workspace_analysis_decision_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    op agent.workspace_analysis_operation%ROWTYPE;
    model agent.model_run%ROWTYPE;
    call_row agent.model_call%ROWTYPE;
    document_json jsonb;
    action_value text;
    canonical text;
    refs_canonical text;
BEGIN
    SELECT * INTO op FROM agent.workspace_analysis_operation WHERE id=NEW.operation_id AND analysis_run_id=NEW.analysis_run_id FOR SHARE;
    SELECT * INTO model FROM agent.model_run WHERE id=NEW.model_run_id AND workspace_id=NEW.workspace_id FOR SHARE;
    SELECT * INTO call_row FROM agent.model_call WHERE id=NEW.model_call_id AND model_run_id=NEW.model_run_id FOR SHARE;
    IF op.id IS NULL OR model.id IS NULL OR call_row.id IS NULL
       OR op.workspace_id<>NEW.workspace_id OR op.node_key<>'decide_next' OR op.operation_kind<>'DECISION' OR op.call_kind<>'MODEL'
       OR op.ordinal<>NEW.ordinal OR op.status<>'STARTED' OR op.model_call_id IS DISTINCT FROM NEW.model_call_id
       OR op.first_node_attempt_id IS DISTINCT FROM NEW.node_attempt_id OR op.latest_node_attempt_id IS DISTINCT FROM NEW.node_attempt_id
       OR model.workflow_run_id<>op.workflow_run_id OR model.node_run_id<>op.node_run_id OR model.node_attempt_id<>NEW.node_attempt_id
       OR model.output_schema_id<>'agent.workspace-analysis-decision' OR model.output_schema_version<>'1' OR model.status<>'RUNNING'
       OR call_row.status<>'SUCCEEDED' OR call_row.phase<>'AGENT' OR call_row.request_hash<>op.request_hash
       OR call_row.response_hash IS DISTINCT FROM NEW.document_hash OR call_row.response_bytes<>NEW.document_bytes
       OR NEW.created_at IS DISTINCT FROM call_row.completed_at OR NEW.created_at<model.started_at
       OR NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_run WHERE id=NEW.analysis_run_id AND workspace_id=NEW.workspace_id AND definition_version=2)
       OR NOT agent.workspace_analysis_v2_operation_predecessor(op.id) THEN
        RAISE EXCEPTION 'dynamic decision receipt has no exact successful call' USING ERRCODE='55000';
    END IF;
    document_json:=convert_from(NEW.document,'UTF8')::jsonb;
    IF NOT workflow.workspace_analysis_receipt_exact_object_keys(document_json,ARRAY['action','query','evidence_ref','evidence_refs'])
       OR jsonb_typeof(document_json->'action') IS DISTINCT FROM 'string' THEN
        RAISE EXCEPTION 'dynamic decision document keys are invalid' USING ERRCODE='23514';
    END IF;
    action_value:=document_json->>'action';
    IF action_value IN ('git_status','finish') THEN
        IF document_json->'query'<>'null'::jsonb OR document_json->'evidence_ref'<>'null'::jsonb OR document_json->'evidence_refs'<>'null'::jsonb THEN
            RAISE EXCEPTION 'dynamic parameterless decision has arguments' USING ERRCODE='23514';
        END IF;
    ELSIF action_value='knowledge_search' THEN
        IF jsonb_typeof(document_json->'query') IS DISTINCT FROM 'string'
           OR NOT workflow.workspace_analysis_receipt_text(document_json->>'query',1024)
           OR document_json->'evidence_ref'<>'null'::jsonb OR document_json->'evidence_refs'<>'null'::jsonb THEN
            RAISE EXCEPTION 'dynamic search decision is invalid' USING ERRCODE='23514';
        END IF;
    ELSIF action_value='source_read' THEN
        IF document_json->'query'<>'null'::jsonb OR document_json->'evidence_refs'<>'null'::jsonb
           OR jsonb_typeof(document_json->'evidence_ref') IS DISTINCT FROM 'string'
           OR NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_evidence evidence WHERE evidence.analysis_run_id=NEW.analysis_run_id
                AND 'E'||evidence.reference_no::text=document_json->>'evidence_ref') THEN
            RAISE EXCEPTION 'dynamic source decision has no run-owned search reference' USING ERRCODE='23514';
        END IF;
    ELSIF action_value='citation_validation' THEN
        IF document_json->'query'<>'null'::jsonb OR document_json->'evidence_ref'<>'null'::jsonb
           OR NOT agent.workspace_analysis_v2_refs_valid(document_json->'evidence_refs',8) THEN
            RAISE EXCEPTION 'dynamic citation decision is invalid' USING ERRCODE='23514';
        END IF;
        IF EXISTS (SELECT 1 FROM jsonb_array_elements_text(document_json->'evidence_refs') ref
                   WHERE NOT agent.workspace_analysis_v2_ref_read(NEW.analysis_run_id,ref)) THEN
            RAISE EXCEPTION 'dynamic citation decision must use completed reads' USING ERRCODE='23514';
        END IF;
    ELSE
        RAISE EXCEPTION 'dynamic decision action is outside the frozen catalog' USING ERRCODE='23514';
    END IF;
    IF jsonb_typeof(document_json->'evidence_refs')='array' THEN
        SELECT '['||string_agg(workflow.workspace_analysis_receipt_json_string(item#>>'{}'),',' ORDER BY ordinal)||']'
          INTO refs_canonical FROM jsonb_array_elements(document_json->'evidence_refs') WITH ORDINALITY refs(item,ordinal);
    ELSE refs_canonical:='null'; END IF;
    canonical:='{"action":'||workflow.workspace_analysis_receipt_json_string(action_value)
        ||',"query":'||COALESCE(workflow.workspace_analysis_receipt_json_string(document_json->>'query'),'null')
        ||',"evidence_ref":'||COALESCE(workflow.workspace_analysis_receipt_json_string(document_json->>'evidence_ref'),'null')
        ||',"evidence_refs":'||refs_canonical||'}';
    IF NEW.document IS DISTINCT FROM convert_to(canonical,'UTF8') THEN
        RAISE EXCEPTION 'dynamic decision document must be canonical without duplicate keys' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER workspace_analysis_decision_guard_insert BEFORE INSERT ON agent.workspace_analysis_decision
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_decision_insert();

CREATE FUNCTION agent.workspace_analysis_v2_decision_call_bound(target_call_id uuid)
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT COALESCE((SELECT
        operation.node_key='decide_next' AND operation.operation_kind='DECISION' AND operation.ordinal BETWEEN 1 AND 12
        AND operation.call_kind='MODEL' AND operation.request_hash=call_row.request_hash AND operation.first_node_attempt_id=model.node_attempt_id
        AND operation.workspace_id=model.workspace_id AND operation.workflow_run_id=model.workflow_run_id AND operation.node_run_id=model.node_run_id
        AND call_row.phase='AGENT' AND call_row.output_schema_id='agent.workspace-analysis-decision' AND call_row.output_schema_version='1'
        AND call_row.max_output_tokens=512 AND reservation.model_call_id=call_row.id
        AND reservation.reserved_model_calls=1 AND reservation.reserved_input_tokens=65536 AND reservation.reserved_output_tokens=512
        AND ((call_row.status='STARTED' AND model.status='RUNNING' AND operation.status='STARTED' AND reservation.status='RESERVED' AND decision.id IS NULL)
          OR (call_row.status='SUCCEEDED' AND operation.status='SUCCEEDED' AND reservation.status='SETTLED'
              AND reservation.settled_input_tokens=call_row.input_tokens AND reservation.settled_output_tokens=call_row.output_tokens
              AND decision.id=operation.result_id AND operation.result_kind='DECISION_RECEIPT' AND operation.result_hash=decision.document_hash
              AND decision.model_run_id=model.id AND decision.model_call_id=call_row.id AND decision.node_attempt_id=model.node_attempt_id
              AND decision.workspace_id=model.workspace_id AND decision.analysis_run_id=operation.analysis_run_id AND decision.ordinal=operation.ordinal
              AND decision.document_hash=call_row.response_hash AND decision.document_bytes=call_row.response_bytes AND decision.created_at=call_row.completed_at)
          OR (call_row.status='FAILED' AND operation.status='FAILED' AND reservation.status='SETTLED' AND decision.id IS NULL
              AND operation.error_code=call_row.error_code AND reservation.settled_input_tokens=call_row.input_tokens AND reservation.settled_output_tokens=call_row.output_tokens)
          OR (call_row.status='UNKNOWN' AND operation.status='UNKNOWN' AND reservation.status='UNKNOWN_CHARGED' AND decision.id IS NULL
              AND operation.error_code=call_row.error_code AND reservation.settled_input_tokens=reservation.reserved_input_tokens
              AND reservation.settled_output_tokens=reservation.reserved_output_tokens))
      FROM agent.model_call call_row
      JOIN agent.model_run model ON model.id=call_row.model_run_id
      JOIN agent.workspace_analysis_operation operation ON operation.model_call_id=call_row.id
      JOIN agent.workspace_analysis_run analysis ON analysis.id=operation.analysis_run_id AND analysis.definition_version=2
      JOIN agent.workspace_analysis_journal journal ON journal.operation_id=operation.id AND journal.analysis_run_id=analysis.id
      JOIN agent.workspace_analysis_budget_reservation reservation ON reservation.operation_id=operation.id AND reservation.analysis_run_id=analysis.id
      LEFT JOIN agent.workspace_analysis_decision decision ON decision.operation_id=operation.id
      WHERE call_row.id=target_call_id),false)
$$;

CREATE FUNCTION agent.verify_workspace_analysis_v2_model_run(target_model_run_id uuid)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    model agent.model_run%ROWTYPE;
    last_call agent.model_call%ROWTYPE;
    call_count integer;
    maximum_call integer;
    last_action text;
BEGIN
    SELECT * INTO model FROM agent.model_run WHERE id=target_model_run_id;
    IF model.id IS NULL OR model.output_schema_id<>'agent.workspace-analysis-decision' THEN RETURN; END IF;
    SELECT count(*),max(call_no) INTO call_count,maximum_call FROM agent.model_call WHERE model_run_id=model.id;
    IF call_count NOT BETWEEN 1 AND 12 OR call_count IS DISTINCT FROM maximum_call
       OR EXISTS (SELECT 1 FROM agent.model_call WHERE model_run_id=model.id AND NOT agent.workspace_analysis_v2_decision_call_bound(id)) THEN
        RAISE EXCEPTION 'dynamic model run requires a complete nonempty decision call prefix' USING ERRCODE='55000';
    END IF;
    SELECT * INTO last_call FROM agent.model_call WHERE model_run_id=model.id AND call_no=maximum_call;
    SELECT convert_from(document,'UTF8')::jsonb->>'action' INTO last_action FROM agent.workspace_analysis_decision WHERE model_call_id=last_call.id;
    IF EXISTS (SELECT 1 FROM agent.model_call WHERE model_run_id=model.id AND call_no<maximum_call AND status<>'SUCCEEDED')
       OR (model.status='RUNNING' AND (last_call.status NOT IN ('STARTED','SUCCEEDED') OR last_action='finish'))
       OR (model.status='SUCCEEDED' AND (last_call.status<>'SUCCEEDED' OR model.final_result_type IS DISTINCT FROM 'workspace_analysis_decision'))
       OR (model.status='FAILED' AND (last_call.status<>'FAILED' OR model.error_code IS DISTINCT FROM last_call.error_code))
       OR (model.status='UNKNOWN' AND (last_call.status<>'UNKNOWN' OR model.error_code IS DISTINCT FROM last_call.error_code))
       OR model.status NOT IN ('RUNNING','SUCCEEDED','FAILED','UNKNOWN')
       OR (model.status<>'RUNNING' AND (model.completed_at IS NULL OR model.completed_at<last_call.completed_at)) THEN
        RAISE EXCEPTION 'dynamic model run terminal result disagrees with its calls' USING ERRCODE='55000';
    END IF;
    IF model.status='SUCCEEDED' AND last_action IS DISTINCT FROM 'finish'
       AND NOT EXISTS (SELECT 1 FROM workflow.node_attempt WHERE id=model.node_attempt_id AND status IN ('lease_lost','retry_scheduled','failed','cancelled','manual_recovery'))
       AND NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_run analysis
           JOIN agent.workspace_analysis_termination_proof proof ON proof.analysis_run_id=analysis.id
           WHERE analysis.workflow_run_id=model.workflow_run_id AND analysis.workspace_id=model.workspace_id) THEN
        RAISE EXCEPTION 'dynamic model checkpoint requires replacement or a proven analysis termination' USING ERRCODE='55000';
    END IF;
END;
$$;

CREATE FUNCTION agent.guard_workspace_analysis_v2_model_run_closure()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='model_run' THEN PERFORM agent.verify_workspace_analysis_v2_model_run(NEW.id);
    ELSE PERFORM agent.verify_workspace_analysis_v2_model_run(NEW.model_run_id); END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER workspace_analysis_v2_model_run_closure AFTER INSERT OR UPDATE ON agent.model_run
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_v2_model_run_closure();
CREATE CONSTRAINT TRIGGER workspace_analysis_v2_model_call_closure AFTER INSERT OR UPDATE ON agent.model_call
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_v2_model_run_closure();
CREATE CONSTRAINT TRIGGER workspace_analysis_v2_decision_closure AFTER INSERT ON agent.workspace_analysis_decision
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_v2_model_run_closure();

-- Used only after the owning transaction has locked the Workflow and Analysis
-- Run. Completed model decisions are checkpoints, never published answers.
CREATE FUNCTION agent.close_workspace_analysis_v2_decision_model(target_model_id uuid, completed_at_value timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE model agent.model_run%ROWTYPE;
BEGIN
    SELECT * INTO model FROM agent.model_run WHERE id=target_model_id FOR UPDATE;
    IF model.id IS NULL OR model.output_schema_id<>'agent.workspace-analysis-decision' OR model.status<>'RUNNING' THEN
        RAISE EXCEPTION 'dynamic checkpoint model is not running' USING ERRCODE='55000';
    END IF;
    PERFORM agent.verify_workspace_analysis_v2_model_run(model.id);
    IF completed_at_value IS NULL OR completed_at_value<model.updated_at
       OR EXISTS (SELECT 1 FROM agent.model_call WHERE model_run_id=model.id AND (status<>'SUCCEEDED' OR completed_at>completed_at_value)) THEN
        RAISE EXCEPTION 'dynamic checkpoint cannot close an incomplete call' USING ERRCODE='55000';
    END IF;
    UPDATE agent.model_run SET status='SUCCEEDED',final_result_type='workspace_analysis_decision',error_code=NULL,
        version=version+1,updated_at=completed_at_value,completed_at=completed_at_value WHERE id=model.id AND status='RUNNING';
END;
$$;

CREATE FUNCTION agent.close_workspace_analysis_v2_prior_decision_runs(target_run_id uuid, current_attempt_id uuid, completed_at_value timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE checkpoint_model_id uuid;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_run analysis JOIN workflow.node_run node ON node.run_id=analysis.workflow_run_id
        JOIN workflow.node_attempt attempt ON attempt.node_run_id=node.id
        WHERE analysis.id=target_run_id AND analysis.definition_version=2 AND analysis.status IN ('queued','running')
          AND node.node_key='decide_next' AND node.status='running' AND attempt.id=current_attempt_id AND attempt.status='running'
          AND attempt.attempt_no=node.attempt AND attempt.lease_owner=node.lease_owner AND attempt.lease_until=node.lease_until
          AND attempt.lease_until>clock_timestamp()) THEN
        RAISE EXCEPTION 'dynamic replacement checkpoint has no current execution fence' USING ERRCODE='55000';
    END IF;
    FOR checkpoint_model_id IN SELECT model.id FROM agent.model_run model JOIN agent.workspace_analysis_run analysis
        ON analysis.workflow_run_id=model.workflow_run_id AND analysis.workspace_id=model.workspace_id
        WHERE analysis.id=target_run_id AND model.output_schema_id='agent.workspace-analysis-decision'
          AND model.node_attempt_id<>current_attempt_id AND model.status='RUNNING' ORDER BY model.started_at,model.id FOR UPDATE OF model
    LOOP
        IF NOT EXISTS (SELECT 1 FROM agent.model_run model JOIN workflow.node_attempt attempt ON attempt.id=model.node_attempt_id
            JOIN workflow.node_attempt current_attempt ON current_attempt.node_run_id=attempt.node_run_id AND current_attempt.id=current_attempt_id
            WHERE model.id=checkpoint_model_id AND attempt.status IN ('lease_lost','retry_scheduled','failed','cancelled','manual_recovery') AND attempt.attempt_no<current_attempt.attempt_no) THEN
            RAISE EXCEPTION 'dynamic prior attempt is still live' USING ERRCODE='55000';
        END IF;
        PERFORM agent.close_workspace_analysis_v2_decision_model(checkpoint_model_id,completed_at_value);
    END LOOP;
END;
$$;

CREATE FUNCTION agent.close_workspace_analysis_v2_decision_runs(target_run_id uuid, completed_at_value timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE checkpoint_model_id uuid;
BEGIN
    PERFORM 1 FROM agent.workspace_analysis_run WHERE id=target_run_id AND definition_version=2 FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    FOR checkpoint_model_id IN SELECT model.id FROM agent.model_run model JOIN agent.workspace_analysis_run analysis
        ON analysis.workflow_run_id=model.workflow_run_id AND analysis.workspace_id=model.workspace_id
        WHERE analysis.id=target_run_id AND model.output_schema_id='agent.workspace-analysis-decision' AND model.status='RUNNING'
        ORDER BY model.started_at,model.id FOR UPDATE OF model
    LOOP PERFORM agent.close_workspace_analysis_v2_decision_model(checkpoint_model_id,completed_at_value); END LOOP;
END;
$$;

CREATE FUNCTION agent.workspace_analysis_v2_decisions_complete(target_run_id uuid)
RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE total_decisions integer; total_tools integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_run WHERE id=target_run_id AND definition_version=2) THEN RETURN false; END IF;
    SELECT count(*) FILTER (WHERE operation_kind='DECISION'),count(*) FILTER (WHERE call_kind='TOOL') INTO total_decisions,total_tools
      FROM agent.workspace_analysis_operation WHERE analysis_run_id=target_run_id AND node_key='decide_next';
    IF total_decisions NOT BETWEEN 1 AND 12 OR total_decisions<>total_tools+1
       OR (SELECT count(*) FROM agent.workspace_analysis_decision WHERE analysis_run_id=target_run_id AND convert_from(document,'UTF8')::jsonb->>'action'='finish')<>1
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_operation WHERE analysis_run_id=target_run_id AND node_key='decide_next'
           AND (status<>'SUCCEEDED' OR NOT agent.workspace_analysis_v2_operation_predecessor(id)))
       OR EXISTS (SELECT 1 FROM agent.model_run model JOIN agent.workspace_analysis_run analysis
            ON analysis.workflow_run_id=model.workflow_run_id AND analysis.workspace_id=model.workspace_id
            WHERE analysis.id=target_run_id AND model.output_schema_id='agent.workspace-analysis-decision'
                AND (model.status<>'SUCCEEDED' OR model.final_result_type IS DISTINCT FROM 'workspace_analysis_decision')) THEN RETURN false; END IF;
    RETURN NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_operation operation WHERE analysis_run_id=target_run_id AND operation_kind='DECISION'
        AND NOT agent.workspace_analysis_v2_decision_call_bound(operation.model_call_id));
END;
$$;

CREATE FUNCTION agent.workspace_analysis_v2_validation_all_valid(target_run_id uuid)
RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE
    candidate agent.workspace_analysis_candidate%ROWTYPE;
    receipt workflow.tool_result_receipt%ROWTYPE;
    refs jsonb; output_json jsonb; binding_json jsonb;
BEGIN
    SELECT * INTO candidate FROM agent.workspace_analysis_candidate WHERE analysis_run_id=target_run_id AND schema_version=2;
    SELECT result.* INTO receipt FROM agent.workspace_analysis_operation operation
      JOIN workflow.tool_result_receipt result ON result.id=operation.result_id AND result.tool_call_id=operation.tool_call_id AND result.output_hash=operation.result_hash
      WHERE operation.analysis_run_id=target_run_id AND operation.node_key='validate_citations' AND operation.operation_kind='CITATION_VALIDATION'
        AND operation.ordinal=1 AND operation.status='SUCCEEDED' AND result.workspace_id=operation.workspace_id AND result.workflow_run_id=operation.workflow_run_id
        AND result.tool_name='ValidateCitation' AND result.tool_version=4;
    IF candidate.id IS NULL OR receipt.id IS NULL OR NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_operation
        WHERE id=candidate.synthesis_operation_id AND status='SUCCEEDED' AND result_id=candidate.id AND result_hash=candidate.document_hash) THEN RETURN false; END IF;
    refs:=convert_from(candidate.document,'UTF8')::jsonb#>'{payload,citation_refs}';
    output_json:=convert_from(receipt.output_document,'UTF8')::jsonb;
    binding_json:=convert_from(receipt.server_binding_document,'UTF8')::jsonb;
    IF NOT agent.workspace_analysis_v2_refs_valid(refs,8)
       OR jsonb_typeof(output_json->'results') IS DISTINCT FROM 'array' OR jsonb_typeof(binding_json->'results') IS DISTINCT FROM 'array'
       OR binding_json->>'candidate_id' IS DISTINCT FROM candidate.id::text OR binding_json->>'candidate_hash' IS DISTINCT FROM candidate.document_hash THEN RETURN false; END IF;
    IF jsonb_array_length(output_json->'results')<>jsonb_array_length(refs) OR jsonb_array_length(binding_json->'results')<>jsonb_array_length(refs)
       OR refs IS DISTINCT FROM (SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal) FROM jsonb_array_elements(output_json->'results') WITH ORDINALITY rows(item,ordinal))
       OR refs IS DISTINCT FROM (SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal) FROM jsonb_array_elements(binding_json->'results') WITH ORDINALITY rows(item,ordinal))
       OR EXISTS (SELECT 1 FROM jsonb_array_elements(output_json->'results') item WHERE item->'valid' IS DISTINCT FROM 'true'::jsonb OR item->>'reason_code' IS DISTINCT FROM 'OK')
       OR EXISTS (SELECT 1 FROM jsonb_array_elements_text(refs) ref WHERE NOT agent.workspace_analysis_v2_ref_read(target_run_id,ref))
       OR EXISTS (SELECT 1 FROM jsonb_array_elements(binding_json->'results') item WHERE NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation operation JOIN workflow.tool_result_receipt source ON source.id=operation.result_id AND source.tool_call_id=operation.tool_call_id
            WHERE operation.analysis_run_id=target_run_id AND operation.node_key='decide_next' AND operation.operation_kind='SOURCE_READ' AND operation.status='SUCCEEDED'
              AND source.id::text=item->>'read_receipt_id' AND source.output_hash=item->>'read_receipt_hash' AND source.output_hash=operation.result_hash
              AND source.workspace_id=receipt.workspace_id AND source.workflow_run_id=receipt.workflow_run_id AND source.tool_name='ReadSource' AND source.tool_version=4
              AND convert_from(source.server_binding_document,'UTF8')::jsonb=item-ARRAY['read_receipt_id','read_receipt_hash']
       )) THEN RETURN false; END IF;
    RETURN true;
END;
$$;


CREATE OR REPLACE FUNCTION agent.guard_workspace_analysis_candidate_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    model_run_row agent.model_run%ROWTYPE;
    model_call_row agent.model_call%ROWTYPE;
    candidate_json jsonb;
    candidate_payload jsonb;
    proposal_json jsonb;
    definition_version_value bigint;
BEGIN
    SELECT definition_version INTO definition_version_value FROM agent.workspace_analysis_run
      WHERE id=NEW.analysis_run_id AND workspace_id=NEW.workspace_id AND answer_id=NEW.answer_id FOR SHARE;
    IF definition_version_value IS NULL OR NEW.schema_version<>definition_version_value THEN
        RAISE EXCEPTION 'candidate schema differs from the frozen analysis definition' USING ERRCODE='23514';
    END IF;
    SELECT * INTO operation_row
     FROM agent.workspace_analysis_operation
     WHERE id=NEW.synthesis_operation_id
       AND analysis_run_id=NEW.analysis_run_id
     FOR SHARE;
    SELECT * INTO model_run_row
     FROM agent.model_run
     WHERE id=NEW.synthesis_model_run_id
       AND workspace_id=NEW.workspace_id
     FOR SHARE;
    SELECT * INTO model_call_row
      FROM agent.model_call
     WHERE id=operation_row.model_call_id
       AND model_run_id=NEW.synthesis_model_run_id
     FOR SHARE;

    IF operation_row.id IS NULL
       OR operation_row.operation_kind<>'ANSWER_SYNTHESIS'
       OR operation_row.status<>'STARTED'
       OR operation_row.first_node_attempt_id<>NEW.node_attempt_id
       OR model_run_row.id IS NULL
       OR model_run_row.workflow_run_id<>operation_row.workflow_run_id
       OR model_run_row.node_run_id<>operation_row.node_run_id
       OR model_run_row.node_attempt_id<>NEW.node_attempt_id
       OR model_run_row.status<>'SUCCEEDED'
       OR model_run_row.final_result_type<>'workspace_analysis_answer'
       OR model_run_row.completed_at IS NULL
       OR model_run_row.output_schema_id<>NEW.schema_id
       OR model_run_row.output_schema_version<>NEW.schema_version::text
       OR model_call_row.id IS NULL
       OR model_call_row.status<>'SUCCEEDED'
       OR model_call_row.response_hash<>NEW.document_hash
       OR model_call_row.response_bytes<>NEW.document_bytes
       OR model_call_row.completed_at IS NULL
       OR NEW.created_at<model_run_row.completed_at
       OR NEW.created_at<model_call_row.completed_at THEN
        RAISE EXCEPTION 'workspace analysis candidate binding is invalid' USING ERRCODE='55000';
    END IF;

    candidate_json := convert_from(NEW.document,'UTF8')::jsonb;
    candidate_payload := candidate_json->'payload';
    proposal_json := candidate_payload->'proposal_suggestion';
    IF jsonb_typeof(candidate_json) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(candidate_json))<>5
       OR candidate_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_candidate'
       OR candidate_json->>'schema_id' IS DISTINCT FROM NEW.schema_id
       OR candidate_json->>'schema_version' IS DISTINCT FROM NEW.schema_version::text
       OR candidate_json->>'model_run_ref' IS DISTINCT FROM NEW.synthesis_model_run_id::text
       OR jsonb_typeof(candidate_payload) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(candidate_payload))<>3
       OR jsonb_typeof(candidate_payload->'answer_markdown') IS DISTINCT FROM 'string'
       OR octet_length(convert_to(candidate_payload->>'answer_markdown','UTF8')) NOT BETWEEN 1 AND 65536
       OR candidate_payload->>'answer_markdown' IS DISTINCT FROM btrim(candidate_payload->>'answer_markdown')
       OR jsonb_typeof(candidate_payload->'citation_refs') IS DISTINCT FROM 'array'
       OR jsonb_array_length(candidate_payload->'citation_refs') NOT BETWEEN 1 AND (CASE NEW.schema_version WHEN 2 THEN 8 ELSE 3 END)
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(candidate_payload->'citation_refs') AS item(value)
             WHERE jsonb_typeof(value) IS DISTINCT FROM 'string'
                OR value#>>'{}' !~ (CASE NEW.schema_version WHEN 2 THEN '^E([1-9]|[12][0-9]|3[0-2])$' ELSE '^E[1-3]$' END)
       )
       OR (
            SELECT count(DISTINCT value#>>'{}')
              FROM jsonb_array_elements(candidate_payload->'citation_refs') AS item(value)
       )<>jsonb_array_length(candidate_payload->'citation_refs')
       OR proposal_json IS NULL
       OR jsonb_typeof(proposal_json) NOT IN ('null','object') THEN
        RAISE EXCEPTION 'workspace analysis candidate document is invalid' USING ERRCODE='23514';
    END IF;
    IF jsonb_typeof(proposal_json)='object'
       AND (
            (SELECT count(*) FROM jsonb_object_keys(proposal_json))<>2
            OR jsonb_typeof(proposal_json->'summary') IS DISTINCT FROM 'string'
            OR octet_length(convert_to(proposal_json->>'summary','UTF8')) NOT BETWEEN 1 AND 4096
            OR proposal_json->>'summary' IS DISTINCT FROM btrim(proposal_json->>'summary')
            OR jsonb_typeof(proposal_json->'citation_refs') IS DISTINCT FROM 'array'
            OR jsonb_array_length(proposal_json->'citation_refs') NOT BETWEEN 1 AND (CASE NEW.schema_version WHEN 2 THEN 8 ELSE 3 END)
            OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(proposal_json->'citation_refs') AS item(value)
                 WHERE jsonb_typeof(value) IS DISTINCT FROM 'string'
                    OR value#>>'{}' !~ (CASE NEW.schema_version WHEN 2 THEN '^E([1-9]|[12][0-9]|3[0-2])$' ELSE '^E[1-3]$' END)
                    OR NOT (candidate_payload->'citation_refs' @> jsonb_build_array(value))
            )
            OR (
                SELECT count(DISTINCT value#>>'{}')
                  FROM jsonb_array_elements(proposal_json->'citation_refs') AS item(value)
            )<>jsonb_array_length(proposal_json->'citation_refs')
       ) THEN
        RAISE EXCEPTION 'workspace analysis candidate proposal is invalid' USING ERRCODE='23514';
    END IF;
    IF NEW.schema_version=2 AND EXISTS (SELECT 1 FROM jsonb_array_elements_text(candidate_payload->'citation_refs') ref
        WHERE NOT agent.workspace_analysis_v2_ref_read(NEW.analysis_run_id,ref)) THEN
        RAISE EXCEPTION 'dynamic candidate references require exact completed source reads' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION agent.workspace_analysis_validation_all_valid(target_run_id uuid)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, agent, workflow
AS $$
DECLARE
    candidate_row agent.workspace_analysis_candidate%ROWTYPE;
    validation_receipt workflow.tool_result_receipt%ROWTYPE;
    candidate_json jsonb;
    output_json jsonb;
    binding_json jsonb;
    search_binding_json jsonb;
    candidate_refs jsonb;
    selected_refs jsonb;
BEGIN
    IF EXISTS (SELECT 1 FROM agent.workspace_analysis_run WHERE id=target_run_id AND definition_version=2) THEN
        RETURN agent.workspace_analysis_v2_validation_all_valid(target_run_id);
    END IF;
    SELECT * INTO candidate_row
      FROM agent.workspace_analysis_candidate
     WHERE analysis_run_id=target_run_id;
    SELECT receipt.* INTO validation_receipt
      FROM agent.workspace_analysis_operation AS operation
      JOIN workflow.tool_result_receipt AS receipt
        ON receipt.id=operation.result_id
       AND receipt.tool_call_id=operation.tool_call_id
     WHERE operation.analysis_run_id=target_run_id
       AND operation.operation_kind='CITATION_VALIDATION'
       AND operation.status='SUCCEEDED'
       AND receipt.tool_name='ValidateCitation'
       AND receipt.tool_version=3;
    SELECT convert_from(receipt.server_binding_document,'UTF8')::jsonb
      INTO search_binding_json
      FROM agent.workspace_analysis_operation AS operation
      JOIN workflow.tool_result_receipt AS receipt
        ON receipt.id=operation.result_id
       AND receipt.tool_call_id=operation.tool_call_id
     WHERE operation.analysis_run_id=target_run_id
       AND operation.operation_kind='KNOWLEDGE_SEARCH'
       AND operation.status='SUCCEEDED';
    IF candidate_row.id IS NULL OR validation_receipt.id IS NULL OR search_binding_json IS NULL
       OR NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation
             WHERE id=candidate_row.synthesis_operation_id
               AND analysis_run_id=target_run_id
               AND operation_kind='ANSWER_SYNTHESIS'
               AND status='SUCCEEDED'
               AND result_id=candidate_row.id
               AND result_hash=candidate_row.document_hash
       ) THEN
        RETURN false;
    END IF;

    selected_refs := agent.workspace_analysis_selected_refs(target_run_id);
    IF selected_refs IS NULL
       OR NOT agent.workspace_analysis_source_prefix_matches(
            target_run_id,selected_refs,jsonb_array_length(selected_refs)
       ) THEN
        RETURN false;
    END IF;
    candidate_json := convert_from(candidate_row.document,'UTF8')::jsonb;
    output_json := convert_from(validation_receipt.output_document,'UTF8')::jsonb;
    binding_json := convert_from(validation_receipt.server_binding_document,'UTF8')::jsonb;
    candidate_refs := candidate_json#>'{payload,citation_refs}';
    IF jsonb_typeof(candidate_refs) IS DISTINCT FROM 'array'
       OR jsonb_typeof(output_json->'results') IS DISTINCT FROM 'array'
       OR jsonb_typeof(binding_json->'results') IS DISTINCT FROM 'array'
       OR binding_json->>'candidate_id' IS DISTINCT FROM candidate_row.id::text
       OR binding_json->>'candidate_hash' IS DISTINCT FROM candidate_row.document_hash THEN
        RETURN false;
    END IF;
    IF jsonb_array_length(candidate_refs)<1
       OR jsonb_array_length(candidate_refs)>jsonb_array_length(selected_refs)
       OR jsonb_array_length(output_json->'results')<>jsonb_array_length(candidate_refs)
       OR jsonb_array_length(binding_json->'results')<>jsonb_array_length(candidate_refs)
       OR candidate_refs IS DISTINCT FROM (
            SELECT COALESCE(jsonb_agg(item->'evidence_ref' ORDER BY ordinal),'[]'::jsonb)
              FROM jsonb_array_elements(output_json->'results') WITH ORDINALITY AS entry(item,ordinal)
       )
       OR candidate_refs IS DISTINCT FROM (
            SELECT COALESCE(jsonb_agg(item->'evidence_ref' ORDER BY ordinal),'[]'::jsonb)
              FROM jsonb_array_elements(binding_json->'results') WITH ORDINALITY AS entry(item,ordinal)
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(candidate_refs) AS candidate_ref(value)
             WHERE NOT (selected_refs @> jsonb_build_array(value))
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(output_json->'results') AS entry(item)
             WHERE item->'valid' IS DISTINCT FROM 'true'::jsonb
                OR item->>'reason_code' IS DISTINCT FROM 'OK'
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(binding_json->'results') AS validation_entry(item)
             WHERE NOT EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(search_binding_json->'items') AS search_entry(item)
                 WHERE search_entry.item=validation_entry.item
             )
       ) THEN
        RETURN false;
    END IF;
    RETURN true;
END;
$$;

CREATE OR REPLACE FUNCTION agent.verify_workspace_analysis_operation_closure(target_operation_id uuid)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    reservation_row agent.workspace_analysis_budget_reservation%ROWTYPE;
BEGIN
    SELECT * INTO operation_row
      FROM agent.workspace_analysis_operation
     WHERE id=target_operation_id;
    IF operation_row.id IS NULL THEN
        RETURN;
    END IF;
    SELECT * INTO reservation_row
      FROM agent.workspace_analysis_budget_reservation
     WHERE operation_id=target_operation_id;

    IF EXISTS (SELECT 1 FROM agent.workspace_analysis_run WHERE id=operation_row.analysis_run_id AND definition_version=2) THEN
        IF NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_journal WHERE operation_id=operation_row.id
            AND analysis_run_id=operation_row.analysis_run_id AND workspace_id=operation_row.workspace_id)
           OR NOT agent.workspace_analysis_v2_operation_predecessor(operation_row.id) THEN
            RAISE EXCEPTION 'dynamic operation requires its exact journal predecessor' USING ERRCODE='55000';
        END IF;
        IF operation_row.operation_kind='DECISION' AND operation_row.status<>'PENDING'
           AND NOT agent.workspace_analysis_v2_decision_call_bound(operation_row.model_call_id) THEN
            RAISE EXCEPTION 'dynamic decision operation has an incomplete call closure' USING ERRCODE='55000';
        END IF;
    END IF;
    IF operation_row.status='PENDING' THEN
        IF reservation_row.id IS NOT NULL THEN
            RAISE EXCEPTION 'pending workspace analysis operation has a reservation' USING ERRCODE='55000';
        END IF;
        RETURN;
    END IF;
    IF reservation_row.id IS NULL
       OR reservation_row.analysis_run_id<>operation_row.analysis_run_id
       OR reservation_row.workspace_id<>operation_row.workspace_id
       OR reservation_row.call_kind<>operation_row.call_kind
       OR reservation_row.model_call_id IS DISTINCT FROM operation_row.model_call_id
       OR reservation_row.tool_call_id IS DISTINCT FROM operation_row.tool_call_id
       OR (operation_row.status='STARTED' AND reservation_row.status<>'RESERVED')
       OR (operation_row.status IN ('SUCCEEDED','FAILED') AND reservation_row.status<>'SETTLED')
       OR (operation_row.status='UNKNOWN' AND reservation_row.status<>'UNKNOWN_CHARGED') THEN
        RAISE EXCEPTION 'workspace analysis operation reservation closure is invalid' USING ERRCODE='55000';
    END IF;
END;
$$;

-- BEGIN TODO2 V2 CONVERSATION FINALIZER CONTRACTS
-- TODO2 v2 Conversation finalizer contracts. Append to 00098 after Agent helpers.
-- The v1 trigger bodies below remain intact except explicit persisted-version branches.
-- Runtime-failure trigger routes from 00091 are retained.

ALTER TABLE agent.workspace_analysis_termination_proof
    DROP CONSTRAINT workspace_analysis_termination_proof_deadline_slot,
    ADD CONSTRAINT workspace_analysis_termination_proof_deadline_slot CHECK (
        ((reason='WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED' AND operation_id IS NULL)
            =(deadline_operation_kind IS NOT NULL AND deadline_operation_ordinal IS NOT NULL))
        AND (deadline_operation_kind IS NULL OR
             (deadline_operation_kind IN ('GIT_STATUS','KNOWLEDGE_SEARCH','SOURCE_READ','CITATION_VALIDATION') AND deadline_operation_ordinal BETWEEN 1 AND 12)
             OR (deadline_operation_kind='DECISION' AND deadline_operation_ordinal BETWEEN 1 AND 13)
             OR (deadline_operation_kind IN ('RETRIEVAL_PLAN','ANSWER_SYNTHESIS','FAITHFULNESS_REVIEW') AND deadline_operation_ordinal=1))
    ),
    DROP CONSTRAINT workspace_analysis_termination_proof_runtime_terminal,
    ADD CONSTRAINT workspace_analysis_termination_proof_runtime_terminal CHECK (
        (runtime_terminal_at IS NULL AND terminal_node_attempt_id IS NOT NULL)
        OR (runtime_terminal_at IS NOT NULL AND reason IN ('WORKSPACE_ANALYSIS_CANCELLED','WORKSPACE_ANALYSIS_RUNTIME_FAILED'))
    );

-- Only the last journal entry, with no Call or reservation, may be left pending
-- by a real cancellation/runtime terminal checkpoint. It never represents usage.
CREATE FUNCTION agent.workspace_analysis_v2_uncalled_pending(target_operation_id uuid)
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM agent.workspace_analysis_operation operation
        JOIN agent.workspace_analysis_run analysis ON analysis.id=operation.analysis_run_id AND analysis.definition_version=2
        JOIN agent.workspace_analysis_journal journal ON journal.operation_id=operation.id
        WHERE operation.id=target_operation_id AND operation.status='PENDING'
          AND operation.model_call_id IS NULL AND operation.tool_call_id IS NULL
          AND operation.first_node_attempt_id IS NULL AND operation.latest_node_attempt_id IS NULL
          AND NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_budget_reservation WHERE operation_id=operation.id)
          AND journal.sequence=(SELECT max(last.sequence) FROM agent.workspace_analysis_journal last WHERE last.analysis_run_id=operation.analysis_run_id)
          AND NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_operation prior
              WHERE prior.analysis_run_id=operation.analysis_run_id AND prior.id<>operation.id AND prior.status<>'SUCCEEDED')
    )
$$;

CREATE FUNCTION agent.workspace_analysis_v2_has_readable_evidence(target_run_id uuid)
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM agent.workspace_analysis_operation operation
        JOIN workflow.tool_result_receipt receipt ON receipt.id=operation.result_id AND receipt.tool_call_id=operation.tool_call_id
          AND receipt.output_hash=operation.result_hash AND receipt.workspace_id=operation.workspace_id AND receipt.workflow_run_id=operation.workflow_run_id
        WHERE operation.analysis_run_id=target_run_id AND operation.node_key='decide_next'
          AND operation.operation_kind='SOURCE_READ' AND operation.status='SUCCEEDED'
          AND receipt.tool_name='ReadSource' AND receipt.tool_version=4
          AND convert_from(receipt.output_document,'UTF8')::jsonb->'truncated'='false'::jsonb
          AND agent.workspace_analysis_v2_ref_read(target_run_id,convert_from(receipt.output_document,'UTF8')::jsonb->>'evidence_ref')
    )
$$;

-- This proves the identity of the final gate regardless of valid/invalid result.
-- NOT all_valid alone cannot prove a valid citation rejection.
CREATE FUNCTION agent.workspace_analysis_v2_final_validation_bound(target_run_id uuid)
RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE
    candidate agent.workspace_analysis_candidate%ROWTYPE;
    receipt workflow.tool_result_receipt%ROWTYPE;
    refs jsonb; output_json jsonb; binding_json jsonb;
BEGIN
    SELECT * INTO candidate FROM agent.workspace_analysis_candidate WHERE analysis_run_id=target_run_id AND schema_version=2;
    SELECT result.* INTO receipt FROM agent.workspace_analysis_operation operation
      JOIN workflow.tool_result_receipt result ON result.id=operation.result_id AND result.tool_call_id=operation.tool_call_id AND result.output_hash=operation.result_hash
      WHERE operation.analysis_run_id=target_run_id AND operation.node_key='validate_citations' AND operation.operation_kind='CITATION_VALIDATION'
        AND operation.ordinal=1 AND operation.status='SUCCEEDED' AND result.workspace_id=operation.workspace_id AND result.workflow_run_id=operation.workflow_run_id
        AND result.tool_name='ValidateCitation' AND result.tool_version=4;
    IF candidate.id IS NULL OR receipt.id IS NULL OR NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_operation
        WHERE id=candidate.synthesis_operation_id AND status='SUCCEEDED' AND result_id=candidate.id AND result_hash=candidate.document_hash) THEN RETURN false; END IF;
    refs:=convert_from(candidate.document,'UTF8')::jsonb#>'{payload,citation_refs}';
    output_json:=convert_from(receipt.output_document,'UTF8')::jsonb;
    binding_json:=convert_from(receipt.server_binding_document,'UTF8')::jsonb;
    IF NOT agent.workspace_analysis_v2_refs_valid(refs,8)
       OR jsonb_typeof(output_json->'results') IS DISTINCT FROM 'array' OR jsonb_typeof(binding_json->'results') IS DISTINCT FROM 'array'
       OR binding_json->>'candidate_id' IS DISTINCT FROM candidate.id::text OR binding_json->>'candidate_hash' IS DISTINCT FROM candidate.document_hash THEN RETURN false; END IF;
    IF jsonb_array_length(output_json->'results')<>jsonb_array_length(refs) OR jsonb_array_length(binding_json->'results')<>jsonb_array_length(refs)
       OR refs IS DISTINCT FROM (SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal) FROM jsonb_array_elements(output_json->'results') WITH ORDINALITY rows(item,ordinal))
       OR refs IS DISTINCT FROM (SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal) FROM jsonb_array_elements(binding_json->'results') WITH ORDINALITY rows(item,ordinal))
       OR EXISTS (SELECT 1 FROM jsonb_array_elements_text(refs) ref WHERE NOT agent.workspace_analysis_v2_ref_read(target_run_id,ref))
       OR EXISTS (SELECT 1 FROM jsonb_array_elements(binding_json->'results') item WHERE NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation operation JOIN workflow.tool_result_receipt source ON source.id=operation.result_id AND source.tool_call_id=operation.tool_call_id
            WHERE operation.analysis_run_id=target_run_id AND operation.node_key='decide_next' AND operation.operation_kind='SOURCE_READ' AND operation.status='SUCCEEDED'
              AND source.id::text=item->>'read_receipt_id' AND source.output_hash=item->>'read_receipt_hash' AND source.output_hash=operation.result_hash
              AND source.workspace_id=receipt.workspace_id AND source.workflow_run_id=receipt.workflow_run_id AND source.tool_name='ReadSource' AND source.tool_version=4
              AND convert_from(source.server_binding_document,'UTF8')::jsonb=item-ARRAY['read_receipt_id','read_receipt_hash']
              AND convert_from(source.output_document,'UTF8')::jsonb->'truncated'='false'::jsonb
       )) THEN RETURN false; END IF;
    RETURN true;
END;
$$;

CREATE FUNCTION agent.workspace_analysis_v2_budget_denied(
    analysis agent.workspace_analysis_run, operation agent.workspace_analysis_operation,
    requested_models integer, requested_tools integer, requested_reads integer,
    requested_input bigint, requested_output bigint
) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE
    expected_models integer:=0; expected_tools integer:=0; expected_reads integer:=0;
    expected_input bigint:=0; expected_output bigint:=0;
    retained_models integer:=0; retained_tools integer:=0; retained_input bigint:=0; retained_output bigint:=0;
BEGIN
    IF analysis.definition_version<>2 OR operation.status<>'PENDING' OR operation.analysis_run_id<>analysis.id THEN RETURN false; END IF;
    CASE operation.operation_kind
        WHEN 'DECISION' THEN
            expected_models:=1; expected_input:=65536; expected_output:=512;
            retained_models:=2; retained_input:=131072; retained_output:=analysis.max_output_tokens-6144;
        WHEN 'ANSWER_SYNTHESIS' THEN expected_models:=1; expected_input:=65536; expected_output:=analysis.max_output_tokens-7168;
        WHEN 'FAITHFULNESS_REVIEW' THEN expected_models:=1; expected_input:=65536; expected_output:=1024;
        WHEN 'SOURCE_READ' THEN expected_tools:=1; expected_reads:=1;
        WHEN 'GIT_STATUS','KNOWLEDGE_SEARCH','CITATION_VALIDATION' THEN expected_tools:=1;
        ELSE RETURN false;
    END CASE;
    IF requested_models IS DISTINCT FROM expected_models OR requested_tools IS DISTINCT FROM expected_tools
       OR requested_reads IS DISTINCT FROM expected_reads OR requested_input IS DISTINCT FROM expected_input
       OR requested_output IS DISTINCT FROM expected_output THEN RETURN false; END IF;
    IF operation.node_key='decide_next' AND operation.call_kind='TOOL' THEN retained_tools:=1; END IF;
    RETURN (operation.operation_kind='DECISION' AND operation.ordinal>12)
        OR (operation.operation_kind='KNOWLEDGE_SEARCH' AND (SELECT count(*) FROM agent.workspace_analysis_evidence WHERE analysis_run_id=analysis.id)>=32)
        OR requested_models+retained_models>analysis.max_model_calls-analysis.reserved_model_calls-analysis.settled_model_calls
        OR requested_tools+retained_tools>analysis.max_tool_calls-analysis.reserved_tool_calls-analysis.settled_tool_calls
        OR requested_reads>analysis.max_source_reads-analysis.reserved_source_reads-analysis.settled_source_reads
        OR requested_input+retained_input>analysis.max_input_tokens-analysis.reserved_input_tokens-analysis.settled_input_tokens
        OR requested_output+retained_output>analysis.max_output_tokens-analysis.reserved_output_tokens-analysis.settled_output_tokens;
END;
$$;

CREATE FUNCTION agent.verify_workspace_analysis_v2_success_facts(
    analysis agent.workspace_analysis_run, validation_id uuid, review_id uuid
) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    candidate agent.workspace_analysis_candidate%ROWTYPE;
    review agent.workspace_analysis_model_result%ROWTYPE;
    decision_count bigint; model_count bigint; tool_count bigint; read_count bigint;
BEGIN
    SELECT * INTO candidate FROM agent.workspace_analysis_candidate WHERE analysis_run_id=analysis.id AND answer_id=analysis.answer_id AND schema_version=2;
    SELECT result.* INTO review FROM agent.workspace_analysis_operation operation
      JOIN agent.workspace_analysis_model_result result ON result.id=operation.result_id AND result.operation_id=operation.id
        AND result.document_hash=operation.result_hash AND result.model_call_id=operation.model_call_id
      WHERE operation.analysis_run_id=analysis.id AND operation.node_key='review_publish' AND operation.operation_kind='FAITHFULNESS_REVIEW'
        AND operation.ordinal=1 AND operation.status='SUCCEEDED' AND result.model_run_id=review_id;
    SELECT count(*) FILTER (WHERE operation_kind='DECISION'),count(*) FILTER (WHERE call_kind='MODEL'),
        count(*) FILTER (WHERE call_kind='TOOL'),count(*) FILTER (WHERE operation_kind='SOURCE_READ')
      INTO decision_count,model_count,tool_count,read_count FROM agent.workspace_analysis_operation WHERE analysis_run_id=analysis.id;
    IF analysis.definition_version<>2 OR analysis.policy_version<>2 OR candidate.id IS NULL OR review.id IS NULL
       OR candidate.workspace_id<>analysis.workspace_id OR review.workspace_id<>analysis.workspace_id
       OR candidate.schema_version<>2 OR convert_from(candidate.document,'UTF8')::jsonb->>'schema_version' IS DISTINCT FROM '2'
       OR review.subject_candidate_id IS DISTINCT FROM candidate.id OR review.subject_candidate_hash IS DISTINCT FROM candidate.document_hash
       OR review.model_run_id=candidate.synthesis_model_run_id OR convert_from(review.document,'UTF8')::jsonb#>'{payload,passed}' IS DISTINCT FROM 'true'::jsonb
       OR NOT agent.workspace_analysis_v2_decisions_complete(analysis.id)
       OR NOT agent.workspace_analysis_v2_final_validation_bound(analysis.id)
       OR NOT agent.workspace_analysis_v2_validation_all_valid(analysis.id)
       OR NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_operation WHERE analysis_run_id=analysis.id
            AND node_key='validate_citations' AND operation_kind='CITATION_VALIDATION' AND ordinal=1 AND status='SUCCEEDED' AND result_id=validation_id)
       OR NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_operation WHERE id=candidate.synthesis_operation_id AND analysis_run_id=analysis.id
            AND node_key='synthesize_answer' AND operation_kind='ANSWER_SYNTHESIS' AND ordinal=1 AND status='SUCCEEDED'
            AND result_id=candidate.id AND result_hash=candidate.document_hash)
       OR NOT EXISTS (SELECT 1 FROM agent.model_run WHERE id=candidate.synthesis_model_run_id AND workspace_id=analysis.workspace_id
            AND workflow_run_id=analysis.workflow_run_id AND status='SUCCEEDED' AND final_result_type='workspace_analysis_answer')
       OR NOT EXISTS (SELECT 1 FROM agent.model_run WHERE id=review_id AND workspace_id=analysis.workspace_id
            AND workflow_run_id=analysis.workflow_run_id AND status='SUCCEEDED' AND final_result_type='faithfulness_review')
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_operation WHERE analysis_run_id=analysis.id AND (status<>'SUCCEEDED' OR NOT agent.workspace_analysis_v2_operation_predecessor(id)))
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_budget_reservation WHERE analysis_run_id=analysis.id AND status<>'SETTLED')
       OR EXISTS (SELECT 1 FROM agent.model_run WHERE workspace_id=analysis.workspace_id AND workflow_run_id=analysis.workflow_run_id AND status<>'SUCCEEDED')
       OR EXISTS (SELECT 1 FROM agent.model_call call JOIN agent.model_run model ON model.id=call.model_run_id
            WHERE model.workspace_id=analysis.workspace_id AND model.workflow_run_id=analysis.workflow_run_id AND call.status<>'SUCCEEDED')
       OR EXISTS (SELECT 1 FROM workflow.tool_call WHERE workspace_id=analysis.workspace_id AND workflow_run_id=analysis.workflow_run_id AND status<>'SUCCEEDED')
       OR model_count<>decision_count+2 OR model_count<>tool_count+2 OR decision_count NOT BETWEEN 3 AND 12
       OR read_count NOT BETWEEN 1 AND 8 OR tool_count NOT BETWEEN 3 AND 13
       OR (SELECT count(*) FROM agent.workspace_analysis_operation WHERE analysis_run_id=analysis.id AND node_key='synthesize_answer')<>1
       OR (SELECT count(*) FROM agent.workspace_analysis_operation WHERE analysis_run_id=analysis.id AND node_key='validate_citations')<>1
       OR (SELECT count(*) FROM agent.workspace_analysis_operation WHERE analysis_run_id=analysis.id AND node_key='review_publish')<>1
       OR model_count<>(SELECT count(*) FROM agent.model_call call JOIN agent.model_run model ON model.id=call.model_run_id WHERE model.workspace_id=analysis.workspace_id AND model.workflow_run_id=analysis.workflow_run_id)
       OR tool_count<>(SELECT count(*) FROM workflow.tool_call WHERE workspace_id=analysis.workspace_id AND workflow_run_id=analysis.workflow_run_id)
       OR analysis.settled_model_calls<>model_count OR analysis.settled_tool_calls<>tool_count OR analysis.settled_source_reads<>read_count
       OR analysis.reserved_model_calls<>0 OR analysis.reserved_tool_calls<>0 OR analysis.reserved_source_reads<>0
       OR analysis.reserved_input_tokens<>0 OR analysis.reserved_output_tokens<>0 OR COALESCE(analysis.reserved_cost_microunits,0)<>0 THEN
        RAISE EXCEPTION 'dynamic workspace analysis success facts are incomplete' USING ERRCODE='55000';
    END IF;
END;
$$;

CREATE FUNCTION agent.validate_workspace_analysis_v2_publication_proof(proof agent.workspace_analysis_publication_proof)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    analysis agent.workspace_analysis_run%ROWTYPE;
    runtime workflow.run%ROWTYPE; node workflow.node_run%ROWTYPE; attempt workflow.node_attempt%ROWTYPE;
    candidate agent.workspace_analysis_candidate%ROWTYPE;
    git_receipt workflow.tool_result_receipt%ROWTYPE; citation_receipt workflow.tool_result_receipt%ROWTYPE;
    review agent.workspace_analysis_model_result%ROWTYPE;
    latest_git_id uuid; latest_git_hash text;
    candidate_json jsonb; binding_json jsonb; published_json jsonb; expected_git jsonb:='null'::jsonb;
    expected_citations jsonb; expected_proposal jsonb; expected_document jsonb; git_json jsonb;
BEGIN
    SELECT * INTO runtime FROM workflow.run WHERE id=proof.workflow_run_id AND workspace_id=proof.workspace_id FOR UPDATE;
    SELECT * INTO node FROM workflow.node_run WHERE id=proof.finalization_node_run_id AND run_id=proof.workflow_run_id FOR UPDATE;
    SELECT * INTO attempt FROM workflow.node_attempt WHERE id=proof.finalization_node_attempt_id AND node_run_id=node.id FOR UPDATE;
    SELECT * INTO analysis FROM agent.workspace_analysis_run WHERE id=proof.analysis_run_id AND workspace_id=proof.workspace_id AND answer_id=proof.answer_id AND workflow_run_id=proof.workflow_run_id FOR UPDATE;
    SELECT * INTO candidate FROM agent.workspace_analysis_candidate WHERE id=proof.candidate_id AND analysis_run_id=analysis.id FOR SHARE;
    SELECT * INTO citation_receipt FROM workflow.tool_result_receipt WHERE id=proof.validation_receipt_id AND workspace_id=proof.workspace_id FOR SHARE;
    SELECT * INTO review FROM agent.workspace_analysis_model_result WHERE id=proof.review_model_result_id AND analysis_run_id=analysis.id FOR SHARE;
    SELECT result_id,result_hash INTO latest_git_id,latest_git_hash FROM agent.workspace_analysis_operation
        WHERE analysis_run_id=analysis.id AND node_key='decide_next' AND operation_kind='GIT_STATUS' AND status='SUCCEEDED' ORDER BY ordinal DESC LIMIT 1;
    IF proof.git_receipt_id IS NOT NULL THEN SELECT * INTO git_receipt FROM workflow.tool_result_receipt WHERE id=proof.git_receipt_id AND workspace_id=proof.workspace_id FOR SHARE; END IF;
    IF analysis.id IS NULL OR analysis.definition_version<>2 OR analysis.status<>'running' OR analysis.termination_reason IS NOT NULL
       OR runtime.id IS NULL OR runtime.status<>'running' OR runtime.cancel_requested_at IS NOT NULL
       OR node.id IS NULL OR node.node_key<>'review_publish' OR node.node_type<>'agent.workspace-analysis.v2.review_publish' OR node.status<>'running'
       OR attempt.id IS NULL OR attempt.status<>'running' OR node.attempt IS DISTINCT FROM attempt.attempt_no
       OR node.lease_owner IS NULL OR node.lease_owner='' OR node.lease_owner IS DISTINCT FROM btrim(node.lease_owner)
       OR node.lease_owner IS DISTINCT FROM attempt.lease_owner OR node.lease_until IS NULL OR node.lease_until IS DISTINCT FROM attempt.lease_until
       OR attempt.lease_until<=clock_timestamp() OR proof.created_at>clock_timestamp() OR proof.created_at<analysis.updated_at
       OR analysis.deadline_at<proof.created_at OR analysis.deadline_at<=clock_timestamp()
       OR candidate.id IS NULL OR candidate.answer_id<>proof.answer_id OR candidate.document_hash<>proof.candidate_hash OR candidate.schema_version<>2
       OR citation_receipt.id IS NULL OR citation_receipt.workflow_run_id<>proof.workflow_run_id OR citation_receipt.output_hash<>proof.validation_receipt_hash
       OR citation_receipt.tool_name<>'ValidateCitation' OR citation_receipt.tool_version<>4
       OR review.id IS NULL OR review.operation_kind<>'FAITHFULNESS_REVIEW' OR review.document_hash<>proof.review_document_hash
       OR review.subject_candidate_id IS DISTINCT FROM candidate.id OR review.subject_candidate_hash IS DISTINCT FROM candidate.document_hash
       OR NOT EXISTS (SELECT 1 FROM agent.workspace_analysis_operation WHERE id=review.operation_id AND analysis_run_id=analysis.id
            AND node_run_id=node.id AND result_id=review.id AND result_hash=review.document_hash AND status='SUCCEEDED')
       OR proof.git_receipt_id IS DISTINCT FROM latest_git_id OR proof.git_receipt_hash IS DISTINCT FROM latest_git_hash
       OR (proof.git_receipt_id IS NOT NULL AND (git_receipt.id IS NULL OR git_receipt.output_hash<>proof.git_receipt_hash
            OR git_receipt.tool_name<>'ReadGitStatus' OR git_receipt.tool_version<>3 OR git_receipt.workflow_run_id<>proof.workflow_run_id))
       OR proof.created_at<candidate.created_at OR proof.created_at<citation_receipt.created_at OR proof.created_at<review.created_at
       OR (git_receipt.id IS NOT NULL AND proof.created_at<git_receipt.created_at)
       OR EXISTS (SELECT 1 FROM agent.workspace_analysis_operation WHERE analysis_run_id=analysis.id AND updated_at>proof.created_at) THEN
        RAISE EXCEPTION 'dynamic workspace analysis publication fence or authority binding is invalid' USING ERRCODE='55000';
    END IF;
    PERFORM agent.verify_workspace_analysis_v2_success_facts(analysis,citation_receipt.id,review.model_run_id);
    candidate_json:=convert_from(candidate.document,'UTF8')::jsonb;
    binding_json:=convert_from(citation_receipt.server_binding_document,'UTF8')::jsonb;
    -- Alias references may describe the same immutable source. They retain their
    -- private Search/Read authority and map to one public Citation in first-use order.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(binding_json->'results') item GROUP BY item->>'citation_id'
            HAVING count(DISTINCT jsonb_build_array(item->'index_version_id',item->'chunk_id',item->'source_version_id',item->'source_span_id'))<>1)
       OR EXISTS (SELECT 1 FROM jsonb_array_elements(binding_json->'results') item
            GROUP BY item->'index_version_id',item->'chunk_id',item->'source_version_id',item->'source_span_id'
            HAVING count(DISTINCT item->>'citation_id')<>1) THEN
        RAISE EXCEPTION 'dynamic citation aliases disagree on public identity' USING ERRCODE='55000';
    END IF;
    SELECT jsonb_agg(citation ORDER BY first_ordinal) INTO expected_citations FROM (
        SELECT jsonb_build_object('id',item->>'citation_id','workspace_id',proof.workspace_id::text,
            'index_version_id',item->>'index_version_id','chunk_id',item->>'chunk_id',
            'source_version_id',item->>'source_version_id','source_span_id',item->>'source_span_id') citation, min(ordinal) first_ordinal
        FROM jsonb_array_elements(binding_json->'results') WITH ORDINALITY entry(item,ordinal)
        GROUP BY 1
    ) projected;
    IF candidate_json#>'{payload,proposal_suggestion}'='null'::jsonb THEN expected_proposal:='null'::jsonb;
    ELSE
        SELECT jsonb_build_object('summary',candidate_json#>>'{payload,proposal_suggestion,summary}',
            'citation_ids',jsonb_agg(citation_id ORDER BY first_ordinal),'href','/proposals')
        INTO expected_proposal FROM (
            SELECT item->'citation_id' citation_id,min(proposal.ordinal) first_ordinal
            FROM jsonb_array_elements(candidate_json#>'{payload,proposal_suggestion,citation_refs}') WITH ORDINALITY proposal(ref,ordinal)
            JOIN jsonb_array_elements(binding_json->'results') item ON item->'evidence_ref'=proposal.ref
            GROUP BY 1
        ) projected;
    END IF;
    IF git_receipt.id IS NOT NULL THEN
        git_json:=convert_from(git_receipt.output_document,'UTF8')::jsonb;
        expected_git:=jsonb_build_object('branch',git_json->>'branch','head',git_json->>'head','clean',git_json->'clean',
            'staged_count',git_json->'staged_count','unstaged_count',git_json->'unstaged_count',
            'untracked_count',git_json->'untracked_count','conflict_count',git_json->'conflict_count');
    END IF;
    expected_document:=jsonb_build_object('result_type','workspace_analysis','schema_id','conversation.workspace_analysis_answer',
        'schema_version','v2','model_run_ref',candidate.synthesis_model_run_id::text,
        'payload',jsonb_build_object('answer_markdown',candidate_json#>>'{payload,answer_markdown}',
            'citations',expected_citations,'git_status',expected_git,'proposal_suggestion',expected_proposal,'termination_reason','COMPLETED',
            'budget',jsonb_build_object('model_calls',analysis.settled_model_calls,'tool_calls',analysis.settled_tool_calls,
                'input_tokens',analysis.settled_input_tokens,'output_tokens',analysis.settled_output_tokens,'estimated_cost_microunits',analysis.settled_cost_microunits)));
    published_json:=convert_from(proof.published_document,'UTF8')::jsonb;
    IF published_json IS DISTINCT FROM expected_document THEN
        RAISE EXCEPTION 'dynamic workspace analysis publication is not the authoritative projection' USING ERRCODE='55000';
    END IF;
END;
$$;


-- Version-aware trigger bodies.

CREATE OR REPLACE FUNCTION agent.guard_workspace_analysis_publication_proof_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    candidate_row agent.workspace_analysis_candidate%ROWTYPE;
    git_receipt_row workflow.tool_result_receipt%ROWTYPE;
    search_receipt_row workflow.tool_result_receipt%ROWTYPE;
    validation_receipt_row workflow.tool_result_receipt%ROWTYPE;
    review_result_row agent.workspace_analysis_model_result%ROWTYPE;
    candidate_json jsonb;
    git_json jsonb;
    search_output_json jsonb;
    search_binding_json jsonb;
    validation_output_json jsonb;
    validation_binding_json jsonb;
    review_json jsonb;
    published_json jsonb;
    candidate_refs jsonb;
    selected_refs jsonb;
    expected_citations jsonb;
    expected_proposal jsonb;
    expected_git jsonb;
    expected_budget jsonb;
    evidence_ref_value text;
    search_identity jsonb;
    source_match_count integer;
    source_operation_count integer;
    workflow_status_value text;
    workflow_cancel_requested_at timestamptz;
    node_status_value text;
    node_key_value text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    attempt_status_value text;
    attempt_no integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
BEGIN
    SELECT status,cancel_requested_at
      INTO workflow_status_value,workflow_cancel_requested_at
      FROM workflow.run
     WHERE id=NEW.workflow_run_id
       AND workspace_id=NEW.workspace_id
     FOR UPDATE;
    SELECT status,node_key,attempt,lease_owner,lease_until
      INTO node_status_value,node_key_value,node_attempt_no,node_lease_owner,node_lease_until
      FROM workflow.node_run
     WHERE id=NEW.finalization_node_run_id
       AND run_id=NEW.workflow_run_id
     FOR UPDATE;
    SELECT attempt.status,attempt.attempt_no,attempt.lease_owner,attempt.lease_until
      INTO attempt_status_value,attempt_no,attempt_lease_owner,attempt_lease_until
      FROM workflow.node_attempt AS attempt
     WHERE attempt.id=NEW.finalization_node_attempt_id
       AND attempt.node_run_id=NEW.finalization_node_run_id
     FOR UPDATE;
    SELECT * INTO run_row
      FROM agent.workspace_analysis_run
     WHERE id=NEW.analysis_run_id
       AND workspace_id=NEW.workspace_id
       AND answer_id=NEW.answer_id
     FOR UPDATE;
    SELECT * INTO candidate_row
      FROM agent.workspace_analysis_candidate
     WHERE id=NEW.candidate_id
       AND analysis_run_id=NEW.analysis_run_id
     FOR SHARE;
    SELECT * INTO git_receipt_row
      FROM workflow.tool_result_receipt
     WHERE id=NEW.git_receipt_id
       AND workspace_id=NEW.workspace_id
     FOR SHARE;
    SELECT receipt.* INTO search_receipt_row
      FROM agent.workspace_analysis_operation AS operation
      JOIN workflow.tool_result_receipt AS receipt
        ON receipt.id=operation.result_id
       AND receipt.tool_call_id=operation.tool_call_id
     WHERE operation.analysis_run_id=NEW.analysis_run_id
       AND operation.operation_kind='KNOWLEDGE_SEARCH'
       AND operation.status='SUCCEEDED'
     FOR SHARE OF operation,receipt;
    SELECT * INTO validation_receipt_row
      FROM workflow.tool_result_receipt
     WHERE id=NEW.validation_receipt_id
       AND workspace_id=NEW.workspace_id
     FOR SHARE;
    SELECT * INTO review_result_row
      FROM agent.workspace_analysis_model_result
     WHERE id=NEW.review_model_result_id
       AND analysis_run_id=NEW.analysis_run_id
     FOR SHARE;

    IF run_row.definition_version=2 THEN
        PERFORM agent.validate_workspace_analysis_v2_publication_proof(NEW);
        RETURN NEW;
    END IF;

    IF run_row.id IS NULL
       OR run_row.status<>'running'
       OR run_row.workflow_run_id<>NEW.workflow_run_id
       OR workflow_status_value<>'running'
       OR workflow_cancel_requested_at IS NOT NULL
       OR node_status_value<>'running'
       OR node_key_value<>'review_publish'
       OR node_attempt_no IS DISTINCT FROM attempt_no
       OR node_lease_owner IS NULL
       OR node_lease_owner IS DISTINCT FROM btrim(node_lease_owner)
       OR node_lease_owner=''
       OR attempt_lease_owner IS DISTINCT FROM node_lease_owner
       OR node_lease_until IS NULL
       OR node_lease_until IS DISTINCT FROM attempt_lease_until
       OR attempt_status_value<>'running'
       OR attempt_lease_until IS NULL
       OR attempt_lease_until<=clock_timestamp()
       OR NEW.created_at>clock_timestamp()
       OR run_row.deadline_at<NEW.created_at
       OR run_row.deadline_at<=clock_timestamp()
       OR candidate_row.id IS NULL
       OR candidate_row.answer_id<>NEW.answer_id
       OR candidate_row.document_hash<>NEW.candidate_hash
       OR git_receipt_row.id IS NULL
       OR git_receipt_row.output_hash<>NEW.git_receipt_hash
       OR git_receipt_row.tool_name<>'ReadGitStatus'
       OR git_receipt_row.tool_version<>2
       OR search_receipt_row.id IS NULL
       OR search_receipt_row.tool_name<>'SearchKnowledge'
       OR search_receipt_row.tool_version<>2
       OR validation_receipt_row.id IS NULL
       OR validation_receipt_row.output_hash<>NEW.validation_receipt_hash
       OR validation_receipt_row.tool_name<>'ValidateCitation'
       OR validation_receipt_row.tool_version<>3
       OR review_result_row.id IS NULL
       OR review_result_row.operation_kind<>'FAITHFULNESS_REVIEW'
       OR review_result_row.document_hash<>NEW.review_document_hash
       OR review_result_row.subject_candidate_id<>NEW.candidate_id
       OR review_result_row.subject_candidate_hash<>NEW.candidate_hash
       OR NEW.created_at<candidate_row.created_at
       OR NEW.created_at<git_receipt_row.created_at
       OR NEW.created_at<search_receipt_row.created_at
       OR NEW.created_at<validation_receipt_row.created_at
       OR NEW.created_at<review_result_row.created_at
       OR NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation
             WHERE analysis_run_id=NEW.analysis_run_id
               AND operation_kind='ANSWER_SYNTHESIS'
               AND status='SUCCEEDED'
               AND result_id=NEW.candidate_id
               AND result_hash=NEW.candidate_hash
       )
       OR NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation
             WHERE analysis_run_id=NEW.analysis_run_id
               AND operation_kind='GIT_STATUS'
               AND status='SUCCEEDED'
               AND result_id=NEW.git_receipt_id
               AND result_hash=NEW.git_receipt_hash
       )
       OR NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation
             WHERE analysis_run_id=NEW.analysis_run_id
               AND operation_kind='CITATION_VALIDATION'
               AND status='SUCCEEDED'
               AND result_id=NEW.validation_receipt_id
               AND result_hash=NEW.validation_receipt_hash
       )
       OR NOT EXISTS (
            SELECT 1 FROM agent.workspace_analysis_operation
             WHERE id=review_result_row.operation_id
               AND analysis_run_id=NEW.analysis_run_id
               AND operation_kind='FAITHFULNESS_REVIEW'
               AND status='SUCCEEDED'
               AND node_run_id=NEW.finalization_node_run_id
               AND result_id=NEW.review_model_result_id
               AND result_hash=NEW.review_document_hash
       ) THEN
        RAISE EXCEPTION 'workspace analysis publication authority binding is invalid' USING ERRCODE='55000';
    END IF;

    candidate_json := convert_from(candidate_row.document,'UTF8')::jsonb;
    git_json := convert_from(git_receipt_row.output_document,'UTF8')::jsonb;
    search_output_json := convert_from(search_receipt_row.output_document,'UTF8')::jsonb;
    search_binding_json := convert_from(search_receipt_row.server_binding_document,'UTF8')::jsonb;
    validation_output_json := convert_from(validation_receipt_row.output_document,'UTF8')::jsonb;
    validation_binding_json := convert_from(validation_receipt_row.server_binding_document,'UTF8')::jsonb;
    review_json := convert_from(review_result_row.document,'UTF8')::jsonb;
    published_json := convert_from(NEW.published_document,'UTF8')::jsonb;
    candidate_refs := candidate_json#>'{payload,citation_refs}';
    selected_refs := search_binding_json->'selected_refs';

    IF jsonb_typeof(candidate_refs) IS DISTINCT FROM 'array'
       OR jsonb_typeof(search_output_json->'items') IS DISTINCT FROM 'array'
       OR jsonb_typeof(search_binding_json->'items') IS DISTINCT FROM 'array'
       OR jsonb_typeof(selected_refs) IS DISTINCT FROM 'array'
       OR jsonb_array_length(selected_refs) NOT BETWEEN 1 AND 3
       OR jsonb_array_length(selected_refs)<>LEAST(3,jsonb_array_length(search_binding_json->'items'))
       OR jsonb_array_length(search_output_json->'items')<>jsonb_array_length(search_binding_json->'items')
       OR selected_refs IS DISTINCT FROM (
            SELECT COALESCE(jsonb_agg(item->'evidence_ref' ORDER BY ordinal),'[]'::jsonb)
              FROM jsonb_array_elements(search_binding_json->'items') WITH ORDINALITY AS entry(item,ordinal)
             WHERE ordinal<=3
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(candidate_refs) AS candidate_ref(value)
             WHERE NOT (selected_refs @> jsonb_build_array(value))
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(search_output_json->'items') WITH ORDINALITY AS output_entry(item,ordinal)
              JOIN jsonb_array_elements(search_binding_json->'items') WITH ORDINALITY AS binding_entry(item,ordinal)
                USING (ordinal)
             WHERE output_entry.item->>'evidence_ref' IS DISTINCT FROM binding_entry.item->>'evidence_ref'
       ) THEN
        RAISE EXCEPTION 'workspace analysis search evidence binding is invalid' USING ERRCODE='55000';
    END IF;

    SELECT count(*) INTO source_operation_count
      FROM agent.workspace_analysis_operation
     WHERE analysis_run_id=NEW.analysis_run_id
       AND operation_kind='SOURCE_READ'
       AND status='SUCCEEDED';
    IF source_operation_count<>jsonb_array_length(selected_refs)
       OR run_row.settled_source_reads<>source_operation_count THEN
        RAISE EXCEPTION 'workspace analysis source read count is invalid' USING ERRCODE='55000';
    END IF;

    FOR evidence_ref_value IN
        SELECT value#>>'{}' FROM jsonb_array_elements(selected_refs) AS selected(value)
    LOOP
        SELECT item INTO search_identity
          FROM jsonb_array_elements(search_binding_json->'items') AS entry(item)
         WHERE item->>'evidence_ref'=evidence_ref_value;
        SELECT count(*) INTO source_match_count
          FROM agent.workspace_analysis_operation AS operation
          JOIN workflow.tool_result_receipt AS receipt
            ON receipt.id=operation.result_id
           AND receipt.tool_call_id=operation.tool_call_id
         WHERE operation.analysis_run_id=NEW.analysis_run_id
           AND operation.operation_kind='SOURCE_READ'
           AND operation.status='SUCCEEDED'
           AND receipt.tool_name='ReadSource'
           AND receipt.tool_version=3
           AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'evidence_ref'=evidence_ref_value
           AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'search_receipt_id'=search_receipt_row.id::text
           AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'search_receipt_hash'=search_receipt_row.output_hash
           AND (
                convert_from(receipt.server_binding_document,'UTF8')::jsonb
                - 'search_receipt_id' - 'search_receipt_hash'
           )=search_identity;
        IF search_identity IS NULL OR source_match_count<>1 THEN
            RAISE EXCEPTION 'workspace analysis source evidence binding is invalid' USING ERRCODE='55000';
        END IF;
    END LOOP;

    IF jsonb_typeof(validation_output_json->'results') IS DISTINCT FROM 'array'
       OR jsonb_typeof(validation_binding_json->'results') IS DISTINCT FROM 'array'
       OR validation_binding_json->>'candidate_id' IS DISTINCT FROM NEW.candidate_id::text
       OR validation_binding_json->>'candidate_hash' IS DISTINCT FROM NEW.candidate_hash
       OR jsonb_array_length(validation_output_json->'results')<>jsonb_array_length(candidate_refs)
       OR jsonb_array_length(validation_binding_json->'results')<>jsonb_array_length(candidate_refs)
       OR candidate_refs IS DISTINCT FROM (
            SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal)
              FROM jsonb_array_elements(validation_output_json->'results') WITH ORDINALITY AS entry(item,ordinal)
       )
       OR candidate_refs IS DISTINCT FROM (
            SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal)
              FROM jsonb_array_elements(validation_binding_json->'results') WITH ORDINALITY AS entry(item,ordinal)
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(validation_output_json->'results') AS entry(item)
             WHERE item->'valid' IS DISTINCT FROM 'true'::jsonb
                OR item->>'reason_code' IS DISTINCT FROM 'OK'
       )
       OR EXISTS (
            SELECT 1
              FROM jsonb_array_elements(validation_binding_json->'results') AS validation_entry(item)
             WHERE NOT EXISTS (
                SELECT 1 FROM jsonb_array_elements(search_binding_json->'items') AS search_entry(item)
                 WHERE search_entry.item->>'evidence_ref'=validation_entry.item->>'evidence_ref'
                   AND search_entry.item=validation_entry.item
             )
       )
       OR review_json#>'{payload,passed}' IS DISTINCT FROM 'true'::jsonb THEN
        RAISE EXCEPTION 'workspace analysis validation or review proof is invalid' USING ERRCODE='55000';
    END IF;

    SELECT jsonb_agg(jsonb_build_object(
               'id',item->>'citation_id',
               'workspace_id',NEW.workspace_id::text,
               'index_version_id',item->>'index_version_id',
               'chunk_id',item->>'chunk_id',
               'source_version_id',item->>'source_version_id',
               'source_span_id',item->>'source_span_id'
           ) ORDER BY ordinal)
      INTO expected_citations
      FROM jsonb_array_elements(validation_binding_json->'results') WITH ORDINALITY AS entry(item,ordinal);

    IF jsonb_typeof(candidate_json#>'{payload,proposal_suggestion}')='null' THEN
        expected_proposal := 'null'::jsonb;
    ELSE
        SELECT jsonb_build_object(
            'summary',candidate_json#>>'{payload,proposal_suggestion,summary}',
            'citation_ids',jsonb_agg(binding_item->'citation_id' ORDER BY proposal_entry.ordinal),
            'href','/proposals'
        ) INTO expected_proposal
          FROM jsonb_array_elements(candidate_json#>'{payload,proposal_suggestion,citation_refs}')
               WITH ORDINALITY AS proposal_entry(reference,ordinal)
          JOIN jsonb_array_elements(validation_binding_json->'results') AS validation_entry(binding_item)
            ON binding_item->>'evidence_ref'=proposal_entry.reference#>>'{}';
    END IF;

    expected_git := jsonb_build_object(
        'branch',git_json->>'branch','head',git_json->>'head','clean',git_json->'clean',
        'staged_count',git_json->'staged_count','unstaged_count',git_json->'unstaged_count',
        'untracked_count',git_json->'untracked_count','conflict_count',git_json->'conflict_count'
    );
    expected_budget := jsonb_build_object(
        'model_calls',run_row.settled_model_calls,'tool_calls',run_row.settled_tool_calls,
        'input_tokens',run_row.settled_input_tokens,'output_tokens',run_row.settled_output_tokens,
        'estimated_cost_microunits',run_row.settled_cost_microunits
    );

    IF jsonb_typeof(published_json) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_json))<>5
       OR published_json->>'result_type' IS DISTINCT FROM 'workspace_analysis'
       OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_answer'
       OR published_json->>'schema_version' IS DISTINCT FROM 'v1'
       OR published_json->>'model_run_ref' IS DISTINCT FROM candidate_row.synthesis_model_run_id::text
       OR jsonb_typeof(published_json->'payload') IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_json->'payload'))<>6
       OR published_json#>>'{payload,answer_markdown}' IS DISTINCT FROM candidate_json#>>'{payload,answer_markdown}'
       OR published_json#>'{payload,citations}' IS DISTINCT FROM expected_citations
       OR published_json#>'{payload,git_status}' IS DISTINCT FROM expected_git
       OR published_json#>'{payload,budget}' IS DISTINCT FROM expected_budget
       OR published_json#>'{payload,proposal_suggestion}' IS DISTINCT FROM expected_proposal
       OR published_json#>>'{payload,termination_reason}' IS DISTINCT FROM 'COMPLETED' THEN
        RAISE EXCEPTION 'workspace analysis published document is not the authoritative projection' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION agent.guard_workspace_analysis_termination_proof_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    decision_row agent.workspace_analysis_decision%ROWTYPE;
    operation_row agent.workspace_analysis_operation%ROWTYPE;
    reservation_row agent.workspace_analysis_budget_reservation%ROWTYPE;
    model_call_row agent.model_call%ROWTYPE;
    model_run_row agent.model_run%ROWTYPE;
    tool_call_row workflow.tool_call%ROWTYPE;
    tool_receipt_row workflow.tool_result_receipt%ROWTYPE;
    receipt_failure_row workflow.tool_result_receipt_failure%ROWTYPE;
    model_result_row agent.workspace_analysis_model_result%ROWTYPE;
    candidate_row agent.workspace_analysis_candidate%ROWTYPE;
    search_receipt_row workflow.tool_result_receipt%ROWTYPE;
    artifact_json jsonb;
    candidate_json jsonb;
    search_output_json jsonb;
    search_binding_json jsonb;
    validation_binding_json jsonb;
    candidate_refs jsonb;
    selected_refs jsonb;
    evidence_ref_value text;
    search_identity jsonb;
    source_match_count integer;
    source_operation_count integer;
    workflow_status_value text;
    workflow_cancel_requested_at timestamptz;
    terminal_node_key_value text;
    node_status_value text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    attempt_status_value text;
    attempt_no integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
    expected_model_calls integer;
    expected_tool_calls integer;
    expected_source_reads integer;
    expected_input_tokens bigint;
    expected_output_tokens bigint;
    call_timeout_ms bigint;
    published_json jsonb;
    published_payload jsonb;
    published_summary text;
BEGIN
    SELECT status,cancel_requested_at
      INTO workflow_status_value,workflow_cancel_requested_at
      FROM workflow.run
     WHERE id=NEW.workflow_run_id
       AND workspace_id=NEW.workspace_id
     FOR UPDATE;
    SELECT node_key,status,attempt,lease_owner,lease_until
      INTO terminal_node_key_value,node_status_value,node_attempt_no,node_lease_owner,node_lease_until
      FROM workflow.node_run
     WHERE id=NEW.terminal_node_run_id
       AND run_id=NEW.workflow_run_id
     FOR UPDATE;
    SELECT attempt.status,attempt.attempt_no,attempt.lease_owner,attempt.lease_until
      INTO attempt_status_value,attempt_no,attempt_lease_owner,attempt_lease_until
      FROM workflow.node_attempt AS attempt
     WHERE attempt.id=NEW.terminal_node_attempt_id
       AND attempt.node_run_id=NEW.terminal_node_run_id
     FOR UPDATE;
    SELECT * INTO run_row
      FROM agent.workspace_analysis_run
     WHERE id=NEW.analysis_run_id
       AND workspace_id=NEW.workspace_id
       AND answer_id=NEW.answer_id
       AND workflow_run_id=NEW.workflow_run_id
     FOR UPDATE;

    IF run_row.id IS NULL
       OR run_row.status<>'running'
       OR workflow_status_value<>'running'
       OR node_status_value<>'running'
       OR node_attempt_no IS DISTINCT FROM attempt_no
       OR node_lease_owner IS NULL
       OR node_lease_owner IS DISTINCT FROM btrim(node_lease_owner)
       OR node_lease_owner=''
       OR attempt_lease_owner IS DISTINCT FROM node_lease_owner
       OR node_lease_until IS NULL
       OR node_lease_until IS DISTINCT FROM attempt_lease_until
       OR attempt_status_value<>'running'
       OR attempt_lease_until IS NULL
       OR attempt_lease_until<=clock_timestamp()
       OR NEW.checked_at>clock_timestamp()
       OR NEW.created_at>clock_timestamp()
       OR (NEW.reason<>'WORKSPACE_ANALYSIS_CANCELLED' AND workflow_cancel_requested_at IS NOT NULL)
       OR (NEW.reason='WORKSPACE_ANALYSIS_CANCELLED' AND (
            workflow_cancel_requested_at IS NULL OR workflow_cancel_requested_at>NEW.checked_at
       ))
       OR (NEW.reason NOT IN ('WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED','WORKSPACE_ANALYSIS_CANCELLED')
           AND NEW.checked_at>run_row.deadline_at) THEN
        RAISE EXCEPTION 'workspace analysis termination fence is invalid' USING ERRCODE='55000';
    END IF;
    IF (NEW.reason='WORKSPACE_ANALYSIS_CANCELLED')<>(NEW.operation_id IS NULL)
       OR (NEW.reason<>'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED' AND (
            NEW.requested_model_calls IS NOT NULL OR NEW.requested_tool_calls IS NOT NULL
            OR NEW.requested_source_reads IS NOT NULL OR NEW.requested_input_tokens IS NOT NULL
            OR NEW.requested_output_tokens IS NOT NULL OR NEW.requested_cost_microunits IS NOT NULL
       ))
       OR (NEW.reason<>'WORKSPACE_ANALYSIS_RECEIPT_INVALID' AND (
            NEW.expected_hash IS NOT NULL OR NEW.actual_hash IS NOT NULL
       )) THEN
        RAISE EXCEPTION 'workspace analysis termination proof shape is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.operation_id IS NOT NULL THEN
        SELECT * INTO operation_row
         FROM agent.workspace_analysis_operation
         WHERE id=NEW.operation_id
           AND analysis_run_id=NEW.analysis_run_id
           AND workspace_id=NEW.workspace_id
           AND workflow_run_id=NEW.workflow_run_id
           AND (
                (
                    run_row.definition_version=1
                    AND NEW.reason='WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT'
                    AND terminal_node_key_value='read_evidence'
                    AND node_key='retrieve_evidence'
                    AND operation_kind='KNOWLEDGE_SEARCH'
                    AND ordinal=1
                )
                OR (
                    run_row.definition_version=2
                    AND NEW.reason='WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT'
                    AND terminal_node_key_value='synthesize_answer'
                    AND node_key='decide_next' AND operation_kind='DECISION'
                )
                OR (
                    NEW.reason='WORKSPACE_ANALYSIS_CITATION_INVALID'
                    AND terminal_node_key_value='review_publish'
                    AND node_key='validate_citations'
                    AND operation_kind='CITATION_VALIDATION'
                    AND ordinal=1
                )
                OR (
                    NEW.reason NOT IN (
                        'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
                        'WORKSPACE_ANALYSIS_CITATION_INVALID'
                    )
                    AND node_run_id=NEW.terminal_node_run_id
                )
           )
           AND (
                (
                    NEW.reason IN ('WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED','WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED')
                    AND status='PENDING'
                    AND first_node_attempt_id IS NULL
                    AND latest_node_attempt_id IS NULL
                )
                OR (
                    NEW.reason='WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'
                    AND status='SUCCEEDED'
                    AND completed_at IS NOT NULL
                )
                OR (
                    NEW.reason NOT IN ('WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED','WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED')
                    AND status IN ('SUCCEEDED','FAILED','UNKNOWN')
                )
           )
         FOR SHARE;
        SELECT * INTO reservation_row
          FROM agent.workspace_analysis_budget_reservation
         WHERE operation_id=NEW.operation_id
         FOR SHARE;
        IF operation_row.id IS NULL THEN
            RAISE EXCEPTION 'workspace analysis termination operation binding is invalid' USING ERRCODE='55000';
        END IF;
        IF NEW.reason IN ('WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED','WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED')
           AND operation_row.status='PENDING'
           AND NOT agent.is_workspace_analysis_operation_next(NEW.operation_id) THEN
            RAISE EXCEPTION 'workspace analysis termination proof does not target the next v1 operation' USING ERRCODE='55000';
        END IF;
        IF (
                operation_row.status='PENDING'
                AND NEW.checked_at<operation_row.created_at
           ) OR (
                operation_row.status<>'PENDING'
                AND (
                    operation_row.completed_at IS NULL
                    OR NEW.checked_at<operation_row.completed_at
                )
           ) THEN
            RAISE EXCEPTION 'workspace analysis termination operation chronology is invalid' USING ERRCODE='55000';
        END IF;
        IF operation_row.call_kind='MODEL' THEN
            SELECT * INTO model_call_row
              FROM agent.model_call
             WHERE id=operation_row.model_call_id
             FOR SHARE;
            SELECT * INTO model_run_row
              FROM agent.model_run
             WHERE id=model_call_row.model_run_id
             FOR SHARE;
        ELSE
            SELECT * INTO tool_call_row
              FROM workflow.tool_call
             WHERE id=operation_row.tool_call_id
             FOR SHARE;
        END IF;
    END IF;

    IF NEW.artifact_kind='TOOL_RECEIPT' THEN
        SELECT * INTO tool_receipt_row
          FROM workflow.tool_result_receipt
         WHERE id=NEW.artifact_id
           AND workspace_id=NEW.workspace_id
         FOR SHARE;
        IF tool_receipt_row.id IS NULL
           OR tool_receipt_row.output_hash<>NEW.artifact_hash
           OR operation_row.result_id<>NEW.artifact_id
           OR operation_row.result_hash<>NEW.artifact_hash
           OR NEW.checked_at<tool_receipt_row.created_at THEN
            RAISE EXCEPTION 'workspace analysis termination receipt binding is invalid' USING ERRCODE='55000';
        END IF;
        artifact_json := convert_from(tool_receipt_row.output_document,'UTF8')::jsonb;
    ELSIF NEW.artifact_kind='MODEL_RESULT' THEN
        SELECT * INTO model_result_row
          FROM agent.workspace_analysis_model_result
         WHERE id=NEW.artifact_id
           AND analysis_run_id=NEW.analysis_run_id
         FOR SHARE;
        IF model_result_row.id IS NULL
           OR model_result_row.document_hash<>NEW.artifact_hash
           OR operation_row.result_id<>NEW.artifact_id
           OR operation_row.result_hash<>NEW.artifact_hash
           OR NEW.checked_at<model_result_row.created_at THEN
            RAISE EXCEPTION 'workspace analysis termination model result binding is invalid' USING ERRCODE='55000';
        END IF;
        artifact_json := convert_from(model_result_row.document,'UTF8')::jsonb;
    END IF;

    IF NEW.artifact_kind='DECISION_RECEIPT' THEN
        SELECT * INTO decision_row FROM agent.workspace_analysis_decision
          WHERE id=NEW.artifact_id AND workspace_id=NEW.workspace_id AND analysis_run_id=NEW.analysis_run_id
            AND operation_id=operation_row.id AND model_call_id=operation_row.model_call_id FOR SHARE;
        IF run_row.definition_version<>2 OR NEW.reason<>'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT'
           OR decision_row.id IS NULL OR decision_row.document_hash IS DISTINCT FROM NEW.artifact_hash
           OR operation_row.result_id IS DISTINCT FROM decision_row.id OR operation_row.result_hash IS DISTINCT FROM decision_row.document_hash
           OR decision_row.model_run_id IS DISTINCT FROM model_run_row.id OR NEW.checked_at<decision_row.created_at THEN
            RAISE EXCEPTION 'dynamic finish decision artifact binding is invalid' USING ERRCODE='55000';
        END IF;
        artifact_json:=convert_from(decision_row.document,'UTF8')::jsonb;
    END IF;
    IF run_row.definition_version=2 THEN
        IF NOT EXISTS (SELECT 1 FROM workflow.node_run WHERE id=NEW.terminal_node_run_id
            AND node_type='agent.workspace-analysis.v2.'||node_key)
           OR EXISTS (SELECT 1 FROM agent.model_run WHERE workspace_id=run_row.workspace_id
                AND workflow_run_id=run_row.workflow_run_id AND status='RUNNING')
           OR (operation_row.status='PENDING' AND (reservation_row.id IS NOT NULL OR operation_row.model_call_id IS NOT NULL OR operation_row.tool_call_id IS NOT NULL))
           OR (operation_row.status IN ('SUCCEEDED','FAILED') AND reservation_row.status IS DISTINCT FROM 'SETTLED') THEN
            RAISE EXCEPTION 'dynamic terminal model or reservation closure is invalid' USING ERRCODE='55000';
        END IF;
    END IF;

    IF NEW.reason='WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT' THEN
        IF run_row.definition_version=2 THEN
            IF operation_row.node_key<>'decide_next' OR operation_row.operation_kind<>'DECISION' OR operation_row.status<>'SUCCEEDED'
               OR operation_row.call_kind<>'MODEL' OR operation_row.result_kind<>'DECISION_RECEIPT'
               OR NEW.artifact_kind IS DISTINCT FROM 'DECISION_RECEIPT' OR artifact_json->>'action' IS DISTINCT FROM 'finish'
               OR model_call_row.status IS DISTINCT FROM 'SUCCEEDED' OR model_run_row.status IS DISTINCT FROM 'SUCCEEDED'
               OR NOT agent.workspace_analysis_v2_decisions_complete(run_row.id)
               OR agent.workspace_analysis_v2_has_readable_evidence(run_row.id) THEN
                RAISE EXCEPTION 'dynamic finish does not prove insufficient evidence' USING ERRCODE='55000';
            END IF;
        ELSE
        IF operation_row.node_key<>'retrieve_evidence'
           OR operation_row.operation_kind<>'KNOWLEDGE_SEARCH'
           OR operation_row.ordinal<>1
           OR operation_row.call_kind<>'TOOL'
           OR operation_row.status<>'SUCCEEDED'
           OR operation_row.result_kind<>'TOOL_RESULT_RECEIPT'
           OR NEW.artifact_kind<>'TOOL_RECEIPT'
           OR tool_receipt_row.tool_name<>'SearchKnowledge'
           OR tool_receipt_row.tool_version<>2
           OR jsonb_typeof(artifact_json->'items') IS DISTINCT FROM 'array'
           OR jsonb_array_length(artifact_json->'items')<>0 THEN
            RAISE EXCEPTION 'workspace analysis evidence insufficiency proof is invalid' USING ERRCODE='55000';
        END IF;
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_CITATION_INVALID' THEN
        IF run_row.definition_version=2 THEN
            IF operation_row.node_key<>'validate_citations' OR operation_row.operation_kind<>'CITATION_VALIDATION'
               OR operation_row.ordinal<>1 OR operation_row.call_kind<>'TOOL' OR operation_row.status<>'SUCCEEDED'
               OR operation_row.result_kind<>'TOOL_RESULT_RECEIPT' OR NEW.artifact_kind IS DISTINCT FROM 'TOOL_RECEIPT'
               OR tool_receipt_row.tool_name<>'ValidateCitation' OR tool_receipt_row.tool_version<>4
               OR tool_call_row.status IS DISTINCT FROM 'SUCCEEDED'
               OR NOT agent.workspace_analysis_v2_final_validation_bound(run_row.id)
               OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(artifact_json->'results') item WHERE item->'valid'='false'::jsonb) THEN
                RAISE EXCEPTION 'dynamic final citation rejection proof is invalid' USING ERRCODE='55000';
            END IF;
        ELSE
        IF operation_row.node_key<>'validate_citations'
           OR operation_row.operation_kind<>'CITATION_VALIDATION'
           OR operation_row.ordinal<>1
           OR operation_row.call_kind<>'TOOL'
           OR operation_row.status<>'SUCCEEDED'
           OR operation_row.result_kind<>'TOOL_RESULT_RECEIPT'
           OR NEW.artifact_kind<>'TOOL_RECEIPT'
           OR tool_receipt_row.tool_name<>'ValidateCitation'
           OR tool_receipt_row.tool_version<>3 THEN
            RAISE EXCEPTION 'workspace analysis citation rejection proof is invalid' USING ERRCODE='55000';
        END IF;
        SELECT * INTO candidate_row
          FROM agent.workspace_analysis_candidate
         WHERE analysis_run_id=NEW.analysis_run_id
           AND answer_id=NEW.answer_id
         FOR SHARE;
        SELECT receipt.* INTO search_receipt_row
          FROM agent.workspace_analysis_operation AS operation
          JOIN workflow.tool_result_receipt AS receipt
            ON receipt.id=operation.result_id
           AND receipt.tool_call_id=operation.tool_call_id
         WHERE operation.analysis_run_id=NEW.analysis_run_id
           AND operation.operation_kind='KNOWLEDGE_SEARCH'
           AND operation.status='SUCCEEDED'
         FOR SHARE OF operation,receipt;
        IF candidate_row.id IS NULL
           OR search_receipt_row.id IS NULL
           OR search_receipt_row.tool_name<>'SearchKnowledge'
           OR search_receipt_row.tool_version<>2 THEN
            RAISE EXCEPTION 'workspace analysis citation rejection authority is invalid' USING ERRCODE='55000';
        END IF;

        candidate_json := convert_from(candidate_row.document,'UTF8')::jsonb;
        search_output_json := convert_from(search_receipt_row.output_document,'UTF8')::jsonb;
        search_binding_json := convert_from(search_receipt_row.server_binding_document,'UTF8')::jsonb;
        validation_binding_json := convert_from(tool_receipt_row.server_binding_document,'UTF8')::jsonb;
        candidate_refs := candidate_json#>'{payload,citation_refs}';
        selected_refs := search_binding_json->'selected_refs';
        IF jsonb_typeof(candidate_refs) IS DISTINCT FROM 'array'
           OR jsonb_typeof(search_output_json->'items') IS DISTINCT FROM 'array'
           OR jsonb_typeof(search_binding_json->'items') IS DISTINCT FROM 'array'
           OR jsonb_typeof(selected_refs) IS DISTINCT FROM 'array'
           OR jsonb_array_length(selected_refs) NOT BETWEEN 1 AND 3
           OR jsonb_array_length(selected_refs)<>LEAST(3,jsonb_array_length(search_binding_json->'items'))
           OR jsonb_array_length(search_output_json->'items')<>jsonb_array_length(search_binding_json->'items')
           OR selected_refs IS DISTINCT FROM (
                SELECT COALESCE(jsonb_agg(item->'evidence_ref' ORDER BY ordinal),'[]'::jsonb)
                  FROM jsonb_array_elements(search_binding_json->'items') WITH ORDINALITY AS entry(item,ordinal)
                 WHERE ordinal<=3
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(candidate_refs) AS candidate_ref(value)
                 WHERE NOT (selected_refs @> jsonb_build_array(value))
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(search_output_json->'items') WITH ORDINALITY AS output_entry(item,ordinal)
                  JOIN jsonb_array_elements(search_binding_json->'items') WITH ORDINALITY AS binding_entry(item,ordinal)
                    USING (ordinal)
                 WHERE output_entry.item->>'evidence_ref' IS DISTINCT FROM binding_entry.item->>'evidence_ref'
           ) THEN
            RAISE EXCEPTION 'workspace analysis citation rejection search binding is invalid' USING ERRCODE='55000';
        END IF;

        SELECT count(*) INTO source_operation_count
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.analysis_run_id
           AND operation_kind='SOURCE_READ'
           AND status='SUCCEEDED';
        IF source_operation_count<>jsonb_array_length(selected_refs)
           OR run_row.settled_source_reads<>source_operation_count THEN
            RAISE EXCEPTION 'workspace analysis citation rejection source count is invalid' USING ERRCODE='55000';
        END IF;
        FOR evidence_ref_value IN
            SELECT value#>>'{}' FROM jsonb_array_elements(selected_refs) AS selected(value)
        LOOP
            SELECT item INTO search_identity
              FROM jsonb_array_elements(search_binding_json->'items') AS entry(item)
             WHERE item->>'evidence_ref'=evidence_ref_value;
            SELECT count(*) INTO source_match_count
              FROM agent.workspace_analysis_operation AS operation
              JOIN workflow.tool_result_receipt AS receipt
                ON receipt.id=operation.result_id
               AND receipt.tool_call_id=operation.tool_call_id
             WHERE operation.analysis_run_id=NEW.analysis_run_id
               AND operation.operation_kind='SOURCE_READ'
               AND operation.status='SUCCEEDED'
               AND receipt.tool_name='ReadSource'
               AND receipt.tool_version=3
               AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'evidence_ref'=evidence_ref_value
               AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'search_receipt_id'=search_receipt_row.id::text
               AND convert_from(receipt.server_binding_document,'UTF8')::jsonb->>'search_receipt_hash'=search_receipt_row.output_hash
               AND (
                    convert_from(receipt.server_binding_document,'UTF8')::jsonb
                    - 'search_receipt_id' - 'search_receipt_hash'
               )=search_identity;
            IF search_identity IS NULL OR source_match_count<>1 THEN
                RAISE EXCEPTION 'workspace analysis citation rejection source binding is invalid' USING ERRCODE='55000';
            END IF;
        END LOOP;

        IF jsonb_typeof(artifact_json->'results') IS DISTINCT FROM 'array'
           OR jsonb_typeof(validation_binding_json->'results') IS DISTINCT FROM 'array'
           OR validation_binding_json->>'candidate_id' IS DISTINCT FROM candidate_row.id::text
           OR validation_binding_json->>'candidate_hash' IS DISTINCT FROM candidate_row.document_hash
           OR jsonb_array_length(artifact_json->'results')<>jsonb_array_length(candidate_refs)
           OR jsonb_array_length(validation_binding_json->'results')<>jsonb_array_length(candidate_refs)
           OR candidate_refs IS DISTINCT FROM (
                SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal)
                  FROM jsonb_array_elements(artifact_json->'results') WITH ORDINALITY AS entry(item,ordinal)
           )
           OR candidate_refs IS DISTINCT FROM (
                SELECT jsonb_agg(item->'evidence_ref' ORDER BY ordinal)
                  FROM jsonb_array_elements(validation_binding_json->'results') WITH ORDINALITY AS entry(item,ordinal)
           )
           OR NOT EXISTS (
                SELECT 1 FROM jsonb_array_elements(artifact_json->'results') AS entry(item)
                 WHERE item->'valid' IS NOT DISTINCT FROM 'false'::jsonb
           )
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(validation_binding_json->'results') AS validation_entry(item)
                 WHERE NOT EXISTS (
                    SELECT 1 FROM jsonb_array_elements(search_binding_json->'items') AS search_entry(item)
                     WHERE search_entry.item->>'evidence_ref'=validation_entry.item->>'evidence_ref'
                       AND search_entry.item=validation_entry.item
                 )
           ) THEN
            RAISE EXCEPTION 'workspace analysis citation rejection binding is invalid' USING ERRCODE='55000';
        END IF;
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED' THEN
        IF operation_row.operation_kind<>'FAITHFULNESS_REVIEW'
           OR operation_row.status<>'SUCCEEDED'
           OR NEW.artifact_kind<>'MODEL_RESULT'
           OR model_result_row.operation_kind<>'FAITHFULNESS_REVIEW'
           OR artifact_json#>'{payload,passed}' IS DISTINCT FROM 'false'::jsonb THEN
            RAISE EXCEPTION 'workspace analysis faithfulness rejection proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_MODEL_REFUSED' THEN
        IF operation_row.call_kind<>'MODEL'
           OR operation_row.status<>'FAILED'
           OR operation_row.error_code<>'WORKSPACE_ANALYSIS_MODEL_REFUSED'
           OR model_call_row.status<>'SUCCEEDED'
           OR model_run_row.status<>'REFUSED'
           OR model_run_row.final_result_type<>'refusal'
           OR NEW.artifact_kind IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis model refusal termination proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED' THEN
        IF operation_row.operation_kind<>'RETRIEVAL_PLAN'
           OR operation_row.status<>'SUCCEEDED'
           OR NEW.artifact_kind<>'MODEL_RESULT'
           OR model_result_row.operation_kind<>'RETRIEVAL_PLAN'
           OR artifact_json#>'{payload,requires_clarification}' IS DISTINCT FROM 'true'::jsonb THEN
            RAISE EXCEPTION 'workspace analysis clarification proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED' THEN
        CASE operation_row.operation_kind
            WHEN 'DECISION' THEN
                expected_model_calls:=1; expected_tool_calls:=0; expected_source_reads:=0;
                expected_input_tokens:=65536; expected_output_tokens:=512;
            WHEN 'RETRIEVAL_PLAN' THEN
                expected_model_calls := 1;
                expected_tool_calls := 0;
                expected_source_reads := 0;
                expected_input_tokens := 65536;
                expected_output_tokens := 256;
            WHEN 'ANSWER_SYNTHESIS' THEN
                expected_model_calls := 1;
                expected_tool_calls := 0;
                expected_source_reads := 0;
                expected_input_tokens := 65536;
                expected_output_tokens := run_row.max_output_tokens-CASE WHEN run_row.definition_version=2 THEN 7168 ELSE 1280 END;
            WHEN 'FAITHFULNESS_REVIEW' THEN
                expected_model_calls := 1;
                expected_tool_calls := 0;
                expected_source_reads := 0;
                expected_input_tokens := 65536;
                expected_output_tokens := 1024;
            WHEN 'SOURCE_READ' THEN
                expected_model_calls := 0;
                expected_tool_calls := 1;
                expected_source_reads := 1;
                expected_input_tokens := 0;
                expected_output_tokens := 0;
            ELSE
                expected_model_calls := 0;
                expected_tool_calls := 1;
                expected_source_reads := 0;
                expected_input_tokens := 0;
                expected_output_tokens := 0;
        END CASE;
        IF NEW.artifact_kind IS NOT NULL
           OR NEW.requested_model_calls IS DISTINCT FROM expected_model_calls
           OR NEW.requested_tool_calls IS DISTINCT FROM expected_tool_calls
           OR NEW.requested_source_reads IS DISTINCT FROM expected_source_reads
           OR NEW.requested_input_tokens IS DISTINCT FROM expected_input_tokens
           OR NEW.requested_output_tokens IS DISTINCT FROM expected_output_tokens
           OR NEW.requested_cost_microunits IS NOT NULL
           OR NEW.expected_hash IS NOT NULL OR NEW.actual_hash IS NOT NULL
           OR EXISTS (
                SELECT 1
                  FROM agent.workspace_analysis_operation AS prior
                 WHERE prior.analysis_run_id=NEW.analysis_run_id
                   AND prior.id<>NEW.operation_id
                   AND prior.status IN ('FAILED','UNKNOWN')
           )
           OR (run_row.definition_version=2 AND NOT agent.workspace_analysis_v2_budget_denied(
                run_row,operation_row,NEW.requested_model_calls,NEW.requested_tool_calls,NEW.requested_source_reads,NEW.requested_input_tokens,NEW.requested_output_tokens))
           OR (run_row.definition_version=1 AND (
                NEW.requested_model_calls<=run_row.max_model_calls-run_row.reserved_model_calls-run_row.settled_model_calls
                AND NEW.requested_tool_calls<=run_row.max_tool_calls-run_row.reserved_tool_calls-run_row.settled_tool_calls
                AND NEW.requested_source_reads<=run_row.max_source_reads-run_row.reserved_source_reads-run_row.settled_source_reads
                AND NEW.requested_input_tokens<=run_row.max_input_tokens-run_row.reserved_input_tokens-run_row.settled_input_tokens
                AND NEW.requested_output_tokens<=run_row.max_output_tokens-run_row.reserved_output_tokens-run_row.settled_output_tokens
           )) THEN
            RAISE EXCEPTION 'workspace analysis budget request proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_RECEIPT_INVALID' THEN
        SELECT * INTO receipt_failure_row
          FROM workflow.tool_result_receipt_failure
         WHERE id=NEW.receipt_failure_id
           AND analysis_run_id=NEW.analysis_run_id
           AND operation_id=NEW.operation_id
           AND tool_call_id=operation_row.tool_call_id
         FOR SHARE;
        IF operation_row.call_kind<>'TOOL'
           OR operation_row.status<>'FAILED'
           OR operation_row.error_code<>'WORKSPACE_ANALYSIS_RECEIPT_INVALID'
           OR tool_call_row.status<>'SUCCEEDED'
           OR NEW.artifact_kind IS NOT NULL
           OR receipt_failure_row.id IS NULL
           OR receipt_failure_row.failure_code IS DISTINCT FROM NEW.receipt_failure_code
           OR receipt_failure_row.expected_output_hash IS DISTINCT FROM NEW.expected_hash
           OR receipt_failure_row.observed_output_hash IS DISTINCT FROM NEW.actual_hash
           OR NEW.checked_at<receipt_failure_row.created_at
           OR EXISTS (
                SELECT 1 FROM workflow.tool_result_receipt
                 WHERE tool_call_id=operation_row.tool_call_id
           ) THEN
            RAISE EXCEPTION 'workspace analysis receipt invalid proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_RESULT_UNKNOWN' THEN
        IF operation_row.status<>'UNKNOWN'
           OR reservation_row.status<>'UNKNOWN_CHARGED'
           OR COALESCE(model_call_row.status,tool_call_row.status)<>'UNKNOWN'
           OR NEW.artifact_kind IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis unknown result proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED' THEN
        call_timeout_ms := CASE operation_row.operation_kind
            WHEN 'GIT_STATUS' THEN run_row.git_tool_timeout_ms
            WHEN 'RETRIEVAL_PLAN' THEN run_row.plan_model_timeout_ms
            WHEN 'DECISION' THEN run_row.plan_model_timeout_ms
            WHEN 'KNOWLEDGE_SEARCH' THEN run_row.search_tool_timeout_ms
            WHEN 'SOURCE_READ' THEN run_row.source_read_tool_timeout_ms
            WHEN 'ANSWER_SYNTHESIS' THEN run_row.synthesis_model_timeout_ms
            WHEN 'CITATION_VALIDATION' THEN run_row.validate_citation_tool_timeout_ms
            WHEN 'FAITHFULNESS_REVIEW' THEN run_row.review_model_timeout_ms
        END;
        IF NEW.artifact_kind IS NOT NULL
           OR call_timeout_ms IS NULL
           OR EXISTS (
                SELECT 1 FROM agent.workspace_analysis_operation AS prior
                 WHERE prior.analysis_run_id=NEW.analysis_run_id
                   AND prior.id<>NEW.operation_id
                   AND prior.status IN ('FAILED','UNKNOWN')
           )
           OR (
                operation_row.status='PENDING'
                AND NEW.checked_at+(call_timeout_ms+run_row.durable_completion_margin_ms)*interval '1 millisecond'<=run_row.deadline_at
           )
           OR (
                operation_row.status='SUCCEEDED'
                AND (
                    NEW.checked_at<run_row.deadline_at
                    OR EXISTS (
                        SELECT 1
                          FROM agent.workspace_analysis_operation AS later
                         WHERE later.analysis_run_id=NEW.analysis_run_id
                           AND later.status='SUCCEEDED'
                           AND ROW(later.completed_at,later.id)>ROW(operation_row.completed_at,operation_row.id)
                    )
                )
           ) THEN
            RAISE EXCEPTION 'workspace analysis deadline proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_MODEL_FAILED' THEN
        IF operation_row.call_kind<>'MODEL'
           OR operation_row.status<>'FAILED'
           OR model_call_row.status<>'FAILED'
           OR model_run_row.status<>'FAILED'
           OR NEW.artifact_kind IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis model failure proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_TOOL_FAILED' THEN
        IF operation_row.call_kind<>'TOOL'
           OR operation_row.status<>'FAILED'
           OR tool_call_row.status<>'FAILED'
           OR NEW.artifact_kind IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis tool failure proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_CANCELLED' THEN
        IF NEW.operation_id IS NOT NULL OR NEW.artifact_kind IS NOT NULL
           OR NEW.expected_hash IS NOT NULL OR NEW.actual_hash IS NOT NULL
           OR NEW.requested_model_calls IS NOT NULL OR NEW.requested_tool_calls IS NOT NULL
           OR NEW.requested_source_reads IS NOT NULL OR NEW.requested_input_tokens IS NOT NULL
           OR NEW.requested_output_tokens IS NOT NULL OR NEW.requested_cost_microunits IS NOT NULL THEN
            RAISE EXCEPTION 'workspace analysis cancellation proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSE
        RAISE EXCEPTION 'workspace analysis termination reason is unsupported' USING ERRCODE='55000';
    END IF;

    IF run_row.definition_version=2 AND NEW.reason='WORKSPACE_ANALYSIS_CANCELLED' AND EXISTS (
        SELECT 1 FROM agent.workspace_analysis_operation WHERE analysis_run_id=run_row.id AND status<>'SUCCEEDED'
            AND NOT agent.workspace_analysis_v2_uncalled_pending(id)
    ) THEN RAISE EXCEPTION 'dynamic cancellation cannot mask external work' USING ERRCODE='55000'; END IF;

    published_json := convert_from(NEW.published_document,'UTF8')::jsonb;
    published_payload := published_json->'payload';
    IF jsonb_typeof(published_json) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_json))<>5
       OR published_json->>'schema_version' IS DISTINCT FROM 'v1'
       OR NOT (published_json ? 'model_run_ref')
       OR jsonb_typeof(published_payload) IS DISTINCT FROM 'object'
       OR (
            NEW.published_model_run_id IS NULL
            AND jsonb_typeof(published_json->'model_run_ref') IS DISTINCT FROM 'null'
       )
       OR (
            NEW.published_model_run_id IS NOT NULL
            AND (
                jsonb_typeof(published_json->'model_run_ref') IS DISTINCT FROM 'string'
                OR published_json->>'model_run_ref' IS DISTINCT FROM NEW.published_model_run_id::text
            )
       ) THEN
        RAISE EXCEPTION 'workspace analysis termination publication envelope is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.reason IN (
        'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
        'WORKSPACE_ANALYSIS_CITATION_INVALID',
        'WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED',
        'WORKSPACE_ANALYSIS_MODEL_REFUSED'
    ) THEN
        published_summary := published_payload->>'summary';
        IF published_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_refusal'
           OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_refusal'
           OR (SELECT count(*) FROM jsonb_object_keys(published_payload))<>2
           OR published_payload->>'reason_code' IS DISTINCT FROM NEW.reason
           OR jsonb_typeof(published_payload->'summary') IS DISTINCT FROM 'string'
           OR published_summary=''
           OR published_summary IS DISTINCT FROM btrim(published_summary)
           OR octet_length(convert_to(published_summary,'UTF8'))>4096
           OR (
                NEW.reason='WORKSPACE_ANALYSIS_MODEL_REFUSED'
                AND NEW.published_model_run_id IS DISTINCT FROM model_run_row.id
           )
           OR (
                NEW.reason<>'WORKSPACE_ANALYSIS_MODEL_REFUSED'
                AND NEW.published_model_run_id IS NOT NULL
           ) THEN
            RAISE EXCEPTION 'workspace analysis refusal publication is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.reason='WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED' THEN
        IF published_json->>'result_type' IS DISTINCT FROM 'clarification'
           OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.clarification'
           OR NEW.published_model_run_id IS DISTINCT FROM model_result_row.model_run_id
           OR (SELECT count(*) FROM jsonb_object_keys(published_payload))<>3
           OR published_payload->'reason' IS DISTINCT FROM artifact_json#>'{payload,clarification_reason}'
           OR published_payload->'question' IS DISTINCT FROM artifact_json#>'{payload,clarification_question}'
           OR published_payload->'suggested_scopes' IS DISTINCT FROM artifact_json#>'{payload,suggested_scopes}' THEN
            RAISE EXCEPTION 'workspace analysis clarification publication is invalid' USING ERRCODE='55000';
        END IF;
    ELSE
        published_summary := published_payload->>'summary';
        IF published_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_termination'
           OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_termination'
           OR (SELECT count(*) FROM jsonb_object_keys(published_payload))<>2
           OR published_payload->>'termination_reason' IS DISTINCT FROM NEW.reason
           OR jsonb_typeof(published_payload->'summary') IS DISTINCT FROM 'string'
           OR published_summary=''
           OR published_summary IS DISTINCT FROM btrim(published_summary)
           OR octet_length(convert_to(published_summary,'UTF8'))>4096
           OR (
                NEW.published_model_run_id IS NOT NULL
                AND (
                    NEW.reason NOT IN ('WORKSPACE_ANALYSIS_RESULT_UNKNOWN','WORKSPACE_ANALYSIS_MODEL_FAILED')
                    OR operation_row.call_kind<>'MODEL'
                    OR NEW.published_model_run_id IS DISTINCT FROM model_run_row.id
                )
           ) THEN
            RAISE EXCEPTION 'workspace analysis failure publication is invalid' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION agent.guard_workspace_analysis_run_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    question_mode text;
    answer_status text;
    answer_question_id uuid;
    answer_workflow_run_id uuid;
    definition_key_value text;
    definition_version_value bigint;
    active_operation_count bigint;
    active_reservation_count bigint;
    candidate_row agent.workspace_analysis_candidate%ROWTYPE;
    review_model_result_row agent.workspace_analysis_model_result%ROWTYPE;
    receipt_row workflow.tool_result_receipt%ROWTYPE;
    validation_json jsonb;
    validation_binding_json jsonb;
    review_json jsonb;
    review_status text;
    review_result_type text;
    git_operation_count bigint;
    plan_operation_count bigint;
    search_operation_count bigint;
    source_operation_count bigint;
    synthesis_operation_count bigint;
    validation_operation_count bigint;
    review_operation_count bigint;
    invalid_operation_count bigint;
    model_call_count bigint;
    tool_call_count bigint;
    source_min_ordinal integer;
    source_max_ordinal integer;
    synthesis_operation_id_value uuid;
    validation_operation_receipt_id uuid;
    review_operation_id_value uuid;
    review_operation_result_id uuid;
    review_operation_model_run_id uuid;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'workspace analysis run is append-only' USING ERRCODE='55000';
    END IF;

    IF TG_OP='INSERT' THEN
        IF NEW.status<>'queued'
           OR NEW.version<>1
           OR NEW.reserved_model_calls<>0
           OR NEW.reserved_tool_calls<>0
           OR NEW.reserved_source_reads<>0
           OR NEW.reserved_input_tokens<>0
           OR NEW.reserved_output_tokens<>0
           OR COALESCE(NEW.reserved_cost_microunits,0)<>0
           OR NEW.settled_model_calls<>0
           OR NEW.settled_tool_calls<>0
           OR NEW.settled_source_reads<>0
           OR NEW.settled_input_tokens<>0
           OR NEW.settled_output_tokens<>0
           OR COALESCE(NEW.settled_cost_microunits,0)<>0 THEN
            RAISE EXCEPTION 'workspace analysis run must start queued with an empty budget' USING ERRCODE='23514';
        END IF;
        SELECT mode INTO question_mode
          FROM agent.question
         WHERE id=NEW.question_id
           AND conversation_id=NEW.conversation_id
           AND workspace_id=NEW.workspace_id
         FOR SHARE;
        SELECT publication_status,question_id,workflow_run_id
          INTO answer_status,answer_question_id,answer_workflow_run_id
          FROM agent.answer
         WHERE id=NEW.answer_id AND workspace_id=NEW.workspace_id
         FOR SHARE;
        SELECT definition.key,definition.version
          INTO definition_key_value,definition_version_value
          FROM workflow.run AS run
          JOIN workflow.definition AS definition ON definition.id=run.definition_id
         WHERE run.id=NEW.workflow_run_id AND run.workspace_id=NEW.workspace_id
         FOR SHARE OF run,definition;
        IF question_mode IS DISTINCT FROM 'workspace_analysis'
           OR answer_status IS DISTINCT FROM 'pending'
           OR answer_question_id IS DISTINCT FROM NEW.question_id
           OR answer_workflow_run_id IS DISTINCT FROM NEW.workflow_run_id
           OR definition_key_value IS DISTINCT FROM NEW.definition_key
           OR definition_version_value IS DISTINCT FROM NEW.definition_version THEN
            RAISE EXCEPTION 'workspace analysis run dispatch binding is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.status NOT IN ('queued','running')
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
       OR NEW.question_id IS DISTINCT FROM OLD.question_id
       OR NEW.answer_id IS DISTINCT FROM OLD.answer_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.definition_key IS DISTINCT FROM OLD.definition_key
       OR NEW.definition_version IS DISTINCT FROM OLD.definition_version
       OR NEW.definition_hash IS DISTINCT FROM OLD.definition_hash
       OR NEW.tool_catalog_hash IS DISTINCT FROM OLD.tool_catalog_hash
       OR NEW.policy_version IS DISTINCT FROM OLD.policy_version
       OR NEW.config_revision IS DISTINCT FROM OLD.config_revision
       OR NEW.deadline_at IS DISTINCT FROM OLD.deadline_at
       OR NEW.plan_model_timeout_ms IS DISTINCT FROM OLD.plan_model_timeout_ms
       OR NEW.synthesis_model_timeout_ms IS DISTINCT FROM OLD.synthesis_model_timeout_ms
       OR NEW.review_model_timeout_ms IS DISTINCT FROM OLD.review_model_timeout_ms
       OR NEW.git_tool_timeout_ms IS DISTINCT FROM OLD.git_tool_timeout_ms
       OR NEW.search_tool_timeout_ms IS DISTINCT FROM OLD.search_tool_timeout_ms
       OR NEW.source_read_tool_timeout_ms IS DISTINCT FROM OLD.source_read_tool_timeout_ms
       OR NEW.validate_citation_tool_timeout_ms IS DISTINCT FROM OLD.validate_citation_tool_timeout_ms
       OR NEW.durable_completion_margin_ms IS DISTINCT FROM OLD.durable_completion_margin_ms
       OR NEW.max_nodes IS DISTINCT FROM OLD.max_nodes
       OR NEW.max_model_calls IS DISTINCT FROM OLD.max_model_calls
       OR NEW.max_tool_calls IS DISTINCT FROM OLD.max_tool_calls
       OR NEW.max_source_reads IS DISTINCT FROM OLD.max_source_reads
       OR NEW.max_tool_concurrency IS DISTINCT FROM OLD.max_tool_concurrency
       OR NEW.max_input_tokens IS DISTINCT FROM OLD.max_input_tokens
       OR NEW.max_output_tokens IS DISTINCT FROM OLD.max_output_tokens
       OR NEW.max_cost_microunits IS DISTINCT FROM OLD.max_cost_microunits
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at
       OR NEW.settled_model_calls<OLD.settled_model_calls
       OR NEW.settled_tool_calls<OLD.settled_tool_calls
       OR NEW.settled_source_reads<OLD.settled_source_reads
       OR NEW.settled_input_tokens<OLD.settled_input_tokens
       OR NEW.settled_output_tokens<OLD.settled_output_tokens
       OR COALESCE(NEW.settled_cost_microunits,0)<COALESCE(OLD.settled_cost_microunits,0) THEN
        RAISE EXCEPTION 'workspace analysis run immutable binding or counters changed' USING ERRCODE='55000';
    END IF;

    IF NOT (
        NEW.status=OLD.status
        OR (OLD.status='queued' AND NEW.status='running')
        OR (NEW.status IN ('succeeded','refused','clarification_required','failed','cancelled'))
    ) THEN
        RAISE EXCEPTION 'workspace analysis run transition is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.status IN ('succeeded','refused','clarification_required','failed','cancelled') THEN
        SELECT count(*) INTO active_operation_count
          FROM agent.workspace_analysis_operation AS operation
         WHERE operation.analysis_run_id=NEW.id
           AND (
                operation.status='STARTED'
                OR (
                    operation.status='PENDING'
                    AND NOT EXISTS (
                        SELECT 1
                          FROM agent.workspace_analysis_termination_proof AS proof
                         WHERE proof.analysis_run_id=NEW.id
                           AND ((proof.operation_id=operation.id AND proof.reason IN (
                                'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED','WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'))
                              OR (NEW.definition_version=2 AND proof.reason IN ('WORKSPACE_ANALYSIS_CANCELLED','WORKSPACE_ANALYSIS_RUNTIME_FAILED')
                                  AND agent.workspace_analysis_v2_uncalled_pending(operation.id)))
                    )
                )
           );
        SELECT count(*) INTO active_reservation_count
          FROM agent.workspace_analysis_budget_reservation
         WHERE analysis_run_id=NEW.id AND status='RESERVED';
        IF active_operation_count<>0 OR active_reservation_count<>0 THEN
            RAISE EXCEPTION 'workspace analysis run cannot terminate with active work' USING ERRCODE='55000';
        END IF;
    END IF;

    IF NEW.definition_version=2 AND NEW.status IN ('succeeded','refused','failed','cancelled','clarification_required') THEN
        IF EXISTS (SELECT 1 FROM agent.model_run WHERE workspace_id=NEW.workspace_id AND workflow_run_id=NEW.workflow_run_id AND status='RUNNING') THEN
            RAISE EXCEPTION 'dynamic analysis terminal model runs are not closed' USING ERRCODE='55000';
        END IF;
    END IF;
    IF NEW.status='succeeded' AND NEW.definition_version=2 THEN
        PERFORM agent.verify_workspace_analysis_v2_success_facts(NEW,NEW.validation_receipt_id,NEW.review_model_run_id);
    ELSIF NEW.status='succeeded' THEN
        SELECT
            count(*) FILTER (WHERE operation_kind='GIT_STATUS'),
            count(*) FILTER (WHERE operation_kind='RETRIEVAL_PLAN'),
            count(*) FILTER (WHERE operation_kind='KNOWLEDGE_SEARCH'),
            count(*) FILTER (WHERE operation_kind='SOURCE_READ'),
            count(*) FILTER (WHERE operation_kind='ANSWER_SYNTHESIS'),
            count(*) FILTER (WHERE operation_kind='CITATION_VALIDATION'),
            count(*) FILTER (WHERE operation_kind='FAITHFULNESS_REVIEW'),
            count(*) FILTER (WHERE status<>'SUCCEEDED'),
            min(ordinal) FILTER (WHERE operation_kind='SOURCE_READ'),
            max(ordinal) FILTER (WHERE operation_kind='SOURCE_READ')
          INTO git_operation_count,plan_operation_count,search_operation_count,
               source_operation_count,synthesis_operation_count,validation_operation_count,
               review_operation_count,invalid_operation_count,source_min_ordinal,source_max_ordinal
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.id;
        SELECT id INTO synthesis_operation_id_value
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.id
           AND operation_kind='ANSWER_SYNTHESIS'
           AND status='SUCCEEDED';
        SELECT result_id INTO validation_operation_receipt_id
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.id
           AND operation_kind='CITATION_VALIDATION'
           AND status='SUCCEEDED';
        SELECT operation.id,operation.result_id,model_call.model_run_id
          INTO review_operation_id_value,review_operation_result_id,review_operation_model_run_id
          FROM agent.workspace_analysis_operation AS operation
          JOIN agent.model_call AS model_call ON model_call.id=operation.model_call_id
         WHERE operation.analysis_run_id=NEW.id
           AND operation.operation_kind='FAITHFULNESS_REVIEW'
           AND operation.status='SUCCEEDED';
        SELECT * INTO candidate_row
          FROM agent.workspace_analysis_candidate
         WHERE analysis_run_id=NEW.id AND answer_id=NEW.answer_id
         FOR SHARE;
        SELECT * INTO review_model_result_row
          FROM agent.workspace_analysis_model_result
         WHERE id=review_operation_result_id
           AND workspace_id=NEW.workspace_id
           AND analysis_run_id=NEW.id
           AND operation_kind='FAITHFULNESS_REVIEW'
         FOR SHARE;
        SELECT * INTO receipt_row
          FROM workflow.tool_result_receipt
         WHERE id=NEW.validation_receipt_id
           AND workspace_id=NEW.workspace_id
           AND workflow_run_id=NEW.workflow_run_id
         FOR SHARE;
        SELECT status,final_result_type INTO review_status,review_result_type
          FROM agent.model_run
         WHERE id=NEW.review_model_run_id
           AND workspace_id=NEW.workspace_id
           AND workflow_run_id=NEW.workflow_run_id
         FOR SHARE;
        SELECT count(*) INTO model_call_count
          FROM agent.model_call AS model_call
          JOIN agent.model_run AS model_run ON model_run.id=model_call.model_run_id
         WHERE model_run.workspace_id=NEW.workspace_id
           AND model_run.workflow_run_id=NEW.workflow_run_id;
        SELECT count(*) INTO tool_call_count
          FROM workflow.tool_call
         WHERE workspace_id=NEW.workspace_id
           AND workflow_run_id=NEW.workflow_run_id;
        IF candidate_row.id IS NULL
           OR receipt_row.id IS NULL
           OR receipt_row.tool_name<>'ValidateCitation'
           OR receipt_row.tool_version<>3
           OR candidate_row.synthesis_operation_id IS DISTINCT FROM synthesis_operation_id_value
           OR validation_operation_receipt_id IS DISTINCT FROM NEW.validation_receipt_id
           OR review_model_result_row.id IS NULL
           OR review_model_result_row.operation_id IS DISTINCT FROM review_operation_id_value
           OR review_model_result_row.model_run_id IS DISTINCT FROM NEW.review_model_run_id
           OR review_model_result_row.subject_candidate_id IS DISTINCT FROM candidate_row.id
           OR review_model_result_row.subject_candidate_hash IS DISTINCT FROM candidate_row.document_hash
           OR review_operation_model_run_id IS DISTINCT FROM NEW.review_model_run_id
           OR review_status IS DISTINCT FROM 'SUCCEEDED'
           OR review_result_type IS DISTINCT FROM 'faithfulness_review'
           OR git_operation_count<>1
           OR plan_operation_count<>1
           OR search_operation_count<>1
           OR source_operation_count NOT BETWEEN 1 AND 3
           OR synthesis_operation_count<>1
           OR validation_operation_count<>1
           OR review_operation_count<>1
           OR invalid_operation_count<>0
           OR model_call_count<>3
           OR tool_call_count<>source_operation_count+3
           OR source_min_ordinal<>1
           OR source_max_ordinal<>source_operation_count
           OR NEW.reserved_model_calls<>0
           OR NEW.reserved_tool_calls<>0
           OR NEW.reserved_source_reads<>0
           OR NEW.reserved_input_tokens<>0
           OR NEW.reserved_output_tokens<>0
           OR COALESCE(NEW.reserved_cost_microunits,0)<>0
           OR NEW.settled_model_calls<>3
           OR NEW.settled_tool_calls<>source_operation_count+3
           OR NEW.settled_source_reads<>source_operation_count THEN
            RAISE EXCEPTION 'workspace analysis success proof is incomplete' USING ERRCODE='55000';
        END IF;
        validation_json := convert_from(receipt_row.output_document,'UTF8')::jsonb;
        validation_binding_json := convert_from(receipt_row.server_binding_document,'UTF8')::jsonb;
        review_json := convert_from(review_model_result_row.document,'UTF8')::jsonb;
        IF jsonb_typeof(validation_json->'results') IS DISTINCT FROM 'array'
           OR jsonb_array_length(validation_json->'results')=0
           OR validation_binding_json->>'candidate_id' IS DISTINCT FROM candidate_row.id::text
           OR validation_binding_json->>'candidate_hash' IS DISTINCT FROM candidate_row.document_hash
           OR EXISTS (
                SELECT 1
                  FROM jsonb_array_elements(validation_json->'results') AS result(value)
                 WHERE result.value->'valid' IS DISTINCT FROM 'true'::jsonb
           ) THEN
            RAISE EXCEPTION 'workspace analysis citation validation is not all-valid' USING ERRCODE='55000';
        END IF;
        IF review_json->'payload'->'passed' IS DISTINCT FROM 'true'::jsonb THEN
            RAISE EXCEPTION 'workspace analysis faithfulness review did not pass' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION agent.guard_answer_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    question_mode text;
    model_status text;
    model_result_type text;
    model_workflow_run_id uuid;
    analysis_run_id_value uuid;
    analysis_definition_version bigint;
    analysis_status text;
    analysis_reason text;
    refusal_reason text;
    termination_reason_value text;
    candidate_model_run_id uuid;
    planner_model_run_id uuid;
    planner_result_json jsonb;
    termination_proof_row agent.workspace_analysis_termination_proof%ROWTYPE;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'agent answer is append-only' USING ERRCODE='55000';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.publication_status<>'pending'
           OR NEW.version<>1
           OR NEW.model_run_id IS NOT NULL
           OR NEW.result_type IS NOT NULL
           OR NEW.result IS NOT NULL
           OR NEW.result_hash IS NOT NULL
           OR NEW.retrieval_summary IS NOT NULL
           OR NEW.published_at IS NOT NULL THEN
            RAISE EXCEPTION 'agent answer must be inserted pending at version one' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.publication_status<>'pending'
       OR NEW.publication_status='pending'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
       OR NEW.question_id IS DISTINCT FROM OLD.question_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at
       OR NEW.result_type IS NULL
       OR NEW.result IS NULL
       OR NEW.result_hash IS NULL
       OR NEW.published_at IS NULL
       OR NEW.updated_at<>NEW.published_at THEN
        RAISE EXCEPTION 'agent answer transition or immutable binding is invalid' USING ERRCODE='55000';
    END IF;

    SELECT mode INTO question_mode
      FROM agent.question
     WHERE id=NEW.question_id
       AND workspace_id=NEW.workspace_id
       AND conversation_id=NEW.conversation_id
     FOR SHARE;
    IF question_mode IS NULL THEN
        RAISE EXCEPTION 'agent answer question mode binding is missing' USING ERRCODE='55000';
    END IF;

    IF question_mode='rag' THEN
        IF NEW.model_run_id IS NULL OR NEW.retrieval_summary IS NULL THEN
            RAISE EXCEPTION 'rag answer publication bundle is incomplete' USING ERRCODE='55000';
        END IF;
        SELECT status,final_result_type,workflow_run_id
          INTO model_status,model_result_type,model_workflow_run_id
          FROM agent.model_run
         WHERE id=NEW.model_run_id
           AND workspace_id=NEW.workspace_id
         FOR SHARE;
        IF model_workflow_run_id IS NULL
           OR model_workflow_run_id<>NEW.workflow_run_id
           OR NEW.result->>'result_type' IS DISTINCT FROM NEW.result_type
           OR NEW.result->>'model_run_ref' IS DISTINCT FROM NEW.model_run_id::text
           OR (
                NEW.publication_status='completed'
                AND (
                    model_status<>'SUCCEEDED'
                    OR model_result_type<>'rag_answer'
                    OR NEW.result_type<>'rag_answer'
                    OR NEW.result->>'schema_id' IS DISTINCT FROM 'agent.rag-answer'
                    OR NEW.result->>'schema_version' IS DISTINCT FROM 'v2'
                )
           )
           OR (
                NEW.publication_status='refused'
                AND (
                    model_status<>'REFUSED'
                    OR model_result_type<>'refusal'
                    OR NEW.result_type<>'refusal'
                    OR NEW.result->>'schema_id' IS DISTINCT FROM 'agent.refusal'
                    OR NEW.result->>'schema_version' IS DISTINCT FROM 'v1'
                )
           )
           OR (
                NEW.publication_status='clarification_required'
                AND (
                    model_status<>'SUCCEEDED'
                    OR model_result_type<>'clarification'
                    OR NEW.result_type<>'clarification'
                    OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.clarification'
                    OR NEW.result->>'schema_version' IS DISTINCT FROM 'v1'
                )
           ) THEN
            RAISE EXCEPTION 'agent answer model result binding is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    SELECT id,status,termination_reason,definition_version
      INTO analysis_run_id_value,analysis_status,analysis_reason,analysis_definition_version
      FROM agent.workspace_analysis_run
     WHERE workspace_id=NEW.workspace_id
       AND answer_id=NEW.id
       AND question_id=NEW.question_id
       AND workflow_run_id=NEW.workflow_run_id
     FOR SHARE;
    IF analysis_run_id_value IS NULL THEN
        RAISE EXCEPTION 'workspace analysis answer has no analysis run' USING ERRCODE='55000';
    END IF;

    IF question_mode<>'workspace_analysis'
       OR NEW.retrieval_summary IS NOT NULL
       OR NOT (NEW.result ? 'model_run_ref')
       OR NEW.result->>'result_type' IS DISTINCT FROM NEW.result_type
       OR NEW.result->>'schema_version' IS DISTINCT FROM (CASE WHEN NEW.publication_status='completed' AND analysis_definition_version=2 THEN 'v2' ELSE 'v1' END)
       OR (
            NEW.model_run_id IS NULL
            AND NEW.result->'model_run_ref' IS DISTINCT FROM 'null'::jsonb
       )
       OR (
            NEW.model_run_id IS NOT NULL
            AND NEW.result->>'model_run_ref' IS DISTINCT FROM NEW.model_run_id::text
       ) THEN
        RAISE EXCEPTION 'workspace analysis answer envelope binding is invalid' USING ERRCODE='55000';
    END IF;

    IF NEW.model_run_id IS NOT NULL THEN
        SELECT status,final_result_type,workflow_run_id
          INTO model_status,model_result_type,model_workflow_run_id
          FROM agent.model_run
         WHERE id=NEW.model_run_id
           AND workspace_id=NEW.workspace_id
         FOR SHARE;
        IF model_workflow_run_id IS NULL
           OR model_workflow_run_id<>NEW.workflow_run_id
           OR model_status='RUNNING' THEN
            RAISE EXCEPTION 'workspace analysis answer model binding is invalid' USING ERRCODE='55000';
        END IF;
    END IF;

    IF NEW.publication_status<>'completed' THEN
        SELECT * INTO termination_proof_row
          FROM agent.workspace_analysis_termination_proof
         WHERE analysis_run_id=analysis_run_id_value
           AND answer_id=NEW.id
         FOR SHARE;
        IF termination_proof_row.id IS NULL
           OR NEW.result_hash IS DISTINCT FROM termination_proof_row.published_result_hash
           OR NEW.result IS DISTINCT FROM convert_from(termination_proof_row.published_document,'UTF8')::jsonb
           OR NEW.model_run_id IS DISTINCT FROM termination_proof_row.published_model_run_id THEN
            RAISE EXCEPTION 'workspace analysis terminal answer does not match its publication proof' USING ERRCODE='55000';
        END IF;
    END IF;

    IF NEW.publication_status='completed' THEN
        SELECT synthesis_model_run_id INTO candidate_model_run_id
          FROM agent.workspace_analysis_candidate
         WHERE analysis_run_id=analysis_run_id_value AND answer_id=NEW.id
         FOR SHARE;
        IF NEW.result_type<>'workspace_analysis'
           OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_answer'
           OR NEW.model_run_id IS NULL
           OR model_status<>'SUCCEEDED'
           OR model_result_type<>'workspace_analysis_answer'
           OR candidate_model_run_id IS DISTINCT FROM NEW.model_run_id
           OR analysis_status IS DISTINCT FROM 'succeeded'
           OR analysis_reason IS DISTINCT FROM 'COMPLETED' THEN
            RAISE EXCEPTION 'workspace analysis completed answer proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.publication_status='refused' THEN
        refusal_reason := NEW.result->'payload'->>'reason_code';
        IF NEW.result_type<>'workspace_analysis_refusal'
           OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_refusal'
           OR refusal_reason NOT IN (
                'WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT',
                'WORKSPACE_ANALYSIS_CITATION_INVALID',
                'WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED',
                'WORKSPACE_ANALYSIS_MODEL_REFUSED'
           )
           OR analysis_status IS DISTINCT FROM 'refused'
           OR analysis_reason IS DISTINCT FROM refusal_reason
           OR (
                refusal_reason='WORKSPACE_ANALYSIS_MODEL_REFUSED'
                AND (
                    NEW.model_run_id IS NULL
                    OR model_status<>'REFUSED'
                    OR model_result_type<>'refusal'
                    OR NOT EXISTS (
                        SELECT 1
                          FROM agent.workspace_analysis_operation AS operation
                          JOIN agent.model_call AS model_call
                            ON model_call.id=operation.model_call_id
                           AND model_call.model_run_id=NEW.model_run_id
                         WHERE operation.analysis_run_id=analysis_run_id_value
                           AND operation.call_kind='MODEL'
                           AND operation.status='FAILED'
                           AND operation.error_code='WORKSPACE_ANALYSIS_MODEL_REFUSED'
                           AND model_call.status='SUCCEEDED'
                    )
                )
           )
           OR (
                refusal_reason<>'WORKSPACE_ANALYSIS_MODEL_REFUSED'
                AND NEW.model_run_id IS NOT NULL
           ) THEN
            RAISE EXCEPTION 'workspace analysis refusal proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.publication_status='clarification_required' THEN
        SELECT model_result.model_run_id,convert_from(model_result.document,'UTF8')::jsonb
          INTO planner_model_run_id,planner_result_json
          FROM agent.workspace_analysis_operation AS operation
          JOIN agent.workspace_analysis_model_result AS model_result
            ON model_result.id=operation.result_id
           AND model_result.operation_id=operation.id
           AND model_result.analysis_run_id=operation.analysis_run_id
         WHERE operation.analysis_run_id=analysis_run_id_value
           AND operation.operation_kind='RETRIEVAL_PLAN'
           AND operation.status='SUCCEEDED'
           AND operation.result_kind='MODEL_RESULT_RECEIPT'
         FOR SHARE OF operation,model_result;
        IF NEW.result_type<>'clarification'
           OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.clarification'
           OR NEW.model_run_id IS NULL
           OR model_status<>'SUCCEEDED'
           OR model_result_type<>'workspace_analysis_plan'
           OR planner_model_run_id IS DISTINCT FROM NEW.model_run_id
           OR planner_result_json->'payload'->'requires_clarification' IS DISTINCT FROM 'true'::jsonb
           OR NEW.result->'payload'->>'reason' IS DISTINCT FROM planner_result_json->'payload'->>'clarification_reason'
           OR NEW.result->'payload'->>'question' IS DISTINCT FROM planner_result_json->'payload'->>'clarification_question'
           OR NEW.result->'payload'->'suggested_scopes' IS DISTINCT FROM planner_result_json->'payload'->'suggested_scopes'
           OR analysis_status IS DISTINCT FROM 'clarification_required'
           OR analysis_reason IS DISTINCT FROM 'WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED' THEN
            RAISE EXCEPTION 'workspace analysis clarification proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.publication_status IN ('failed','cancelled') THEN
        termination_reason_value := NEW.result->'payload'->>'termination_reason';
        IF NEW.result_type<>'workspace_analysis_termination'
           OR NEW.result->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_termination'
           OR analysis_status IS DISTINCT FROM NEW.publication_status
           OR analysis_reason IS DISTINCT FROM termination_reason_value
           OR (
                NEW.publication_status='cancelled'
                AND termination_reason_value<>'WORKSPACE_ANALYSIS_CANCELLED'
           )
           OR (
                NEW.publication_status='failed'
                AND termination_reason_value NOT IN (
                    'WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED',
                    'WORKSPACE_ANALYSIS_RECEIPT_INVALID',
                    'WORKSPACE_ANALYSIS_RESULT_UNKNOWN',
                    'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED',
                    'WORKSPACE_ANALYSIS_MODEL_FAILED',
                    'WORKSPACE_ANALYSIS_TOOL_FAILED'
                )
           ) THEN
            RAISE EXCEPTION 'workspace analysis termination proof is invalid' USING ERRCODE='55000';
        END IF;
    ELSE
        RAISE EXCEPTION 'workspace analysis publication status is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION agent.workspace_analysis_deadline_next_slot(target_run_id uuid)
RETURNS TABLE(node_key text,operation_kind text,ordinal integer)
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
SET search_path = pg_catalog, agent, workflow
AS $$
DECLARE
    total_count bigint;
    git_count bigint;
    plan_count bigint;
    search_count bigint;
    source_count bigint;
    synthesis_count bigint;
    validation_count bigint;
    review_count bigint;
    plan_json jsonb;
    selected_refs jsonb;
    selected_count integer;
    last_operation agent.workspace_analysis_operation%ROWTYPE;
    last_decision jsonb;
BEGIN
    IF EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=target_run_id
           AND status<>'SUCCEEDED'
    ) THEN
        RETURN;
    END IF;

    IF EXISTS (SELECT 1 FROM agent.workspace_analysis_run WHERE id=target_run_id AND definition_version=2) THEN
        IF EXISTS (SELECT 1 FROM agent.workspace_analysis_operation operation WHERE operation.analysis_run_id=target_run_id
            AND NOT agent.workspace_analysis_v2_operation_predecessor(operation.id)) THEN RETURN; END IF;
        SELECT operation.* INTO last_operation FROM agent.workspace_analysis_journal journal
          JOIN agent.workspace_analysis_operation operation ON operation.id=journal.operation_id
          WHERE journal.analysis_run_id=target_run_id ORDER BY journal.sequence DESC LIMIT 1;
        IF last_operation.id IS NULL THEN RETURN QUERY SELECT 'decide_next'::text,'DECISION'::text,1; RETURN; END IF;
        IF last_operation.node_key='decide_next' AND last_operation.operation_kind='DECISION' THEN
            SELECT convert_from(document,'UTF8')::jsonb INTO last_decision FROM agent.workspace_analysis_decision WHERE operation_id=last_operation.id;
            IF last_decision->>'action'='finish' THEN
                IF agent.workspace_analysis_v2_has_readable_evidence(target_run_id) THEN RETURN QUERY SELECT 'synthesize_answer'::text,'ANSWER_SYNTHESIS'::text,1; END IF;
            ELSIF agent.workspace_analysis_v2_decision_action_kind(last_decision->>'action') IS NOT NULL THEN
                RETURN QUERY SELECT 'decide_next'::text,agent.workspace_analysis_v2_decision_action_kind(last_decision->>'action'),last_operation.ordinal;
            END IF;
        ELSIF last_operation.node_key='decide_next' AND last_operation.call_kind='TOOL' THEN
            RETURN QUERY SELECT 'decide_next'::text,'DECISION'::text,last_operation.ordinal+1;
        ELSIF last_operation.node_key='synthesize_answer' AND last_operation.operation_kind='ANSWER_SYNTHESIS' THEN
            RETURN QUERY SELECT 'validate_citations'::text,'CITATION_VALIDATION'::text,1;
        ELSIF last_operation.node_key='validate_citations' AND last_operation.operation_kind='CITATION_VALIDATION'
            AND agent.workspace_analysis_v2_validation_all_valid(target_run_id) THEN
            RETURN QUERY SELECT 'review_publish'::text,'FAITHFULNESS_REVIEW'::text,1;
        END IF;
        RETURN;
    END IF;

    SELECT count(*),
           count(*) FILTER (WHERE operation.operation_kind='GIT_STATUS'),
           count(*) FILTER (WHERE operation.operation_kind='RETRIEVAL_PLAN'),
           count(*) FILTER (WHERE operation.operation_kind='KNOWLEDGE_SEARCH'),
           count(*) FILTER (WHERE operation.operation_kind='SOURCE_READ'),
           count(*) FILTER (WHERE operation.operation_kind='ANSWER_SYNTHESIS'),
           count(*) FILTER (WHERE operation.operation_kind='CITATION_VALIDATION'),
           count(*) FILTER (WHERE operation.operation_kind='FAITHFULNESS_REVIEW')
      INTO total_count,git_count,plan_count,search_count,source_count,
           synthesis_count,validation_count,review_count
      FROM agent.workspace_analysis_operation AS operation
     WHERE operation.analysis_run_id=target_run_id;

    IF total_count=0 THEN
        RETURN QUERY SELECT 'inspect_workspace'::text,'GIT_STATUS'::text,1;
        RETURN;
    END IF;
    IF git_count<>1 THEN
        RETURN;
    END IF;

    IF plan_count=0 THEN
        IF total_count=1 THEN
            RETURN QUERY SELECT 'retrieve_evidence'::text,'RETRIEVAL_PLAN'::text,1;
        END IF;
        RETURN;
    ELSIF plan_count<>1 THEN
        RETURN;
    END IF;

    SELECT convert_from(result.document,'UTF8')::jsonb
      INTO plan_json
      FROM agent.workspace_analysis_operation AS plan
      JOIN agent.workspace_analysis_model_result AS result
        ON result.id=plan.result_id
       AND result.operation_id=plan.id
       AND result.analysis_run_id=plan.analysis_run_id
     WHERE plan.analysis_run_id=target_run_id
       AND plan.operation_kind='RETRIEVAL_PLAN'
       AND plan.status='SUCCEEDED';
    IF plan_json#>'{payload,requires_clarification}' IS NOT DISTINCT FROM 'true'::jsonb THEN
        RETURN;
    ELSIF plan_json#>'{payload,requires_clarification}' IS DISTINCT FROM 'false'::jsonb THEN
        RETURN;
    END IF;

    IF search_count=0 THEN
        IF total_count=2 THEN
            RETURN QUERY SELECT 'retrieve_evidence'::text,'KNOWLEDGE_SEARCH'::text,1;
        END IF;
        RETURN;
    ELSIF search_count<>1 THEN
        RETURN;
    END IF;

    selected_refs := agent.workspace_analysis_selected_refs(target_run_id);
    IF selected_refs IS NULL THEN
        RETURN;
    END IF;
    selected_count := jsonb_array_length(selected_refs);
    IF selected_count=0 THEN
        RETURN;
    END IF;
    IF source_count<selected_count THEN
        IF total_count=3+source_count
           AND agent.workspace_analysis_source_prefix_matches(
                target_run_id,selected_refs,source_count::integer
           ) THEN
            RETURN QUERY
                SELECT 'read_evidence'::text,'SOURCE_READ'::text,(source_count+1)::integer;
        END IF;
        RETURN;
    ELSIF source_count<>selected_count
       OR NOT agent.workspace_analysis_source_prefix_matches(
            target_run_id,selected_refs,selected_count
       ) THEN
        RETURN;
    END IF;

    IF synthesis_count=0 THEN
        IF total_count=3+source_count THEN
            RETURN QUERY SELECT 'synthesize_answer'::text,'ANSWER_SYNTHESIS'::text,1;
        END IF;
        RETURN;
    ELSIF synthesis_count<>1
       OR NOT EXISTS (
            SELECT 1
              FROM agent.workspace_analysis_operation AS synthesis
              JOIN agent.workspace_analysis_candidate AS candidate
                ON candidate.id=synthesis.result_id
               AND candidate.synthesis_operation_id=synthesis.id
               AND candidate.analysis_run_id=synthesis.analysis_run_id
             WHERE synthesis.analysis_run_id=target_run_id
               AND synthesis.operation_kind='ANSWER_SYNTHESIS'
               AND synthesis.status='SUCCEEDED'
               AND synthesis.result_hash=candidate.document_hash
       ) THEN
        RETURN;
    END IF;

    IF validation_count=0 THEN
        IF total_count=4+source_count THEN
            RETURN QUERY SELECT 'validate_citations'::text,'CITATION_VALIDATION'::text,1;
        END IF;
        RETURN;
    ELSIF validation_count<>1
       OR NOT agent.workspace_analysis_validation_all_valid(target_run_id) THEN
        RETURN;
    END IF;

    IF review_count=0 AND total_count=5+source_count THEN
        RETURN QUERY SELECT 'review_publish'::text,'FAITHFULNESS_REVIEW'::text,1;
    END IF;
    RETURN;
END;
$$;

CREATE OR REPLACE FUNCTION agent.guard_workspace_analysis_preoperation_deadline_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    workflow_status_value text;
    workflow_cancel_requested_at timestamptz;
    terminal_node_key_value text;
    node_status_value text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    attempt_status_value text;
    attempt_no integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
    database_now timestamptz;
    derived_node_key text;
    derived_operation_kind text;
    derived_ordinal integer;
    call_timeout_ms bigint;
    published_json jsonb;
    published_payload jsonb;
    published_summary text;
BEGIN
    IF NEW.reason<>'WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED'
       OR NEW.operation_id IS NOT NULL
       OR NEW.deadline_operation_kind IS NOT NULL
       OR NEW.deadline_operation_ordinal IS NOT NULL
       OR NEW.artifact_kind IS NOT NULL
       OR NEW.artifact_id IS NOT NULL
       OR NEW.artifact_hash IS NOT NULL
       OR NEW.requested_model_calls IS NOT NULL
       OR NEW.requested_tool_calls IS NOT NULL
       OR NEW.requested_source_reads IS NOT NULL
       OR NEW.requested_input_tokens IS NOT NULL
       OR NEW.requested_output_tokens IS NOT NULL
       OR NEW.requested_cost_microunits IS NOT NULL
       OR NEW.receipt_failure_code IS NOT NULL
       OR NEW.receipt_failure_id IS NOT NULL
       OR NEW.expected_hash IS NOT NULL
       OR NEW.actual_hash IS NOT NULL
       OR NEW.published_model_run_id IS NOT NULL THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline proof shape is invalid'
            USING ERRCODE='55000';
    END IF;

    SELECT status,cancel_requested_at
      INTO workflow_status_value,workflow_cancel_requested_at
      FROM workflow.run
     WHERE id=NEW.workflow_run_id
       AND workspace_id=NEW.workspace_id
     FOR UPDATE;
    SELECT node_key,status,attempt,lease_owner,lease_until
      INTO terminal_node_key_value,node_status_value,node_attempt_no,node_lease_owner,node_lease_until
      FROM workflow.node_run
     WHERE id=NEW.terminal_node_run_id
       AND run_id=NEW.workflow_run_id
     FOR UPDATE;
    SELECT attempt.status,attempt.attempt_no,attempt.lease_owner,attempt.lease_until
      INTO attempt_status_value,attempt_no,attempt_lease_owner,attempt_lease_until
      FROM workflow.node_attempt AS attempt
     WHERE attempt.id=NEW.terminal_node_attempt_id
       AND attempt.node_run_id=NEW.terminal_node_run_id
     FOR UPDATE;
    SELECT * INTO run_row
      FROM agent.workspace_analysis_run
     WHERE id=NEW.analysis_run_id
       AND workspace_id=NEW.workspace_id
       AND answer_id=NEW.answer_id
       AND workflow_run_id=NEW.workflow_run_id
     FOR UPDATE;

    PERFORM id
      FROM agent.workspace_analysis_operation
     WHERE analysis_run_id=NEW.analysis_run_id
     ORDER BY id
     FOR UPDATE;
    PERFORM id
      FROM agent.workspace_analysis_budget_reservation
     WHERE analysis_run_id=NEW.analysis_run_id
     ORDER BY id
     FOR UPDATE;
    database_now := clock_timestamp();

    IF run_row.id IS NULL
       OR run_row.status NOT IN ('queued','running')
       OR run_row.termination_reason IS NOT NULL
       OR workflow_status_value<>'running'
       OR workflow_cancel_requested_at IS NOT NULL
       OR node_status_value<>'running'
       OR node_attempt_no IS DISTINCT FROM attempt_no
       OR node_lease_owner IS NULL
       OR node_lease_owner IS DISTINCT FROM btrim(node_lease_owner)
       OR node_lease_owner=''
       OR attempt_lease_owner IS DISTINCT FROM node_lease_owner
       OR node_lease_until IS NULL
       OR node_lease_until IS DISTINCT FROM attempt_lease_until
       OR attempt_status_value<>'running'
       OR attempt_lease_until IS NULL
       OR attempt_lease_until<=database_now
       OR NEW.checked_at<run_row.created_at
       OR NEW.checked_at<run_row.updated_at
       OR NEW.checked_at>database_now
       OR NEW.created_at>database_now THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline fence is invalid'
            USING ERRCODE='55000';
    END IF;
    IF EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.analysis_run_id
           AND status<>'SUCCEEDED'
    ) OR EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_budget_reservation
         WHERE analysis_run_id=NEW.analysis_run_id
           AND status='RESERVED'
    ) THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline has active or failed work'
            USING ERRCODE='55000';
    END IF;

    SELECT slot.node_key,slot.operation_kind,slot.ordinal
      INTO derived_node_key,derived_operation_kind,derived_ordinal
      FROM agent.workspace_analysis_deadline_next_slot(NEW.analysis_run_id) AS slot;
    IF derived_operation_kind IS NULL
       OR derived_node_key IS DISTINCT FROM terminal_node_key_value THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline has no unique next v1 slot'
            USING ERRCODE='55000';
    END IF;
    IF run_row.status='queued'
       AND (
            derived_node_key<>(CASE WHEN run_row.definition_version=2 THEN 'decide_next' ELSE 'inspect_workspace' END)
            OR derived_operation_kind<>(CASE WHEN run_row.definition_version=2 THEN 'DECISION' ELSE 'GIT_STATUS' END)
            OR derived_ordinal<>1
            OR EXISTS (
                SELECT 1
                  FROM agent.workspace_analysis_operation
                 WHERE analysis_run_id=NEW.analysis_run_id
            )
            OR EXISTS (
                SELECT 1
                  FROM agent.workspace_analysis_budget_reservation
                 WHERE analysis_run_id=NEW.analysis_run_id
            )
       ) THEN
        RAISE EXCEPTION 'queued workspace analysis deadline is not the pristine initial slot'
            USING ERRCODE='55000';
    END IF;

    call_timeout_ms := CASE derived_operation_kind
        WHEN 'GIT_STATUS' THEN run_row.git_tool_timeout_ms
        WHEN 'RETRIEVAL_PLAN' THEN run_row.plan_model_timeout_ms
        WHEN 'DECISION' THEN run_row.plan_model_timeout_ms
        WHEN 'KNOWLEDGE_SEARCH' THEN run_row.search_tool_timeout_ms
        WHEN 'SOURCE_READ' THEN run_row.source_read_tool_timeout_ms
        WHEN 'ANSWER_SYNTHESIS' THEN run_row.synthesis_model_timeout_ms
        WHEN 'CITATION_VALIDATION' THEN run_row.validate_citation_tool_timeout_ms
        WHEN 'FAITHFULNESS_REVIEW' THEN run_row.review_model_timeout_ms
    END;
    IF call_timeout_ms IS NULL
       OR database_now+(call_timeout_ms+run_row.durable_completion_margin_ms)*interval '1 millisecond'
            <=run_row.deadline_at THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline still has execution time'
            USING ERRCODE='55000';
    END IF;

    IF run_row.definition_version=2 AND EXISTS (SELECT 1 FROM agent.model_run
        WHERE workspace_id=run_row.workspace_id AND workflow_run_id=run_row.workflow_run_id AND status='RUNNING') THEN
        RAISE EXCEPTION 'dynamic runtime terminal model prefix is not closed' USING ERRCODE='55000';
    END IF;

    published_json := convert_from(NEW.published_document,'UTF8')::jsonb;
    published_payload := published_json->'payload';
    published_summary := published_payload->>'summary';
    IF jsonb_typeof(published_json) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_json))<>5
       OR published_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_termination'
       OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_termination'
       OR published_json->>'schema_version' IS DISTINCT FROM 'v1'
       OR NOT (published_json ? 'model_run_ref')
       OR jsonb_typeof(published_json->'model_run_ref') IS DISTINCT FROM 'null'
       OR jsonb_typeof(published_payload) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_payload))<>2
       OR published_payload->>'termination_reason' IS DISTINCT FROM NEW.reason
       OR jsonb_typeof(published_payload->'summary') IS DISTINCT FROM 'string'
       OR published_summary=''
       OR published_summary IS DISTINCT FROM btrim(published_summary)
       OR octet_length(convert_to(published_summary,'UTF8'))>4096 THEN
        RAISE EXCEPTION 'workspace analysis pre-operation deadline publication is invalid'
            USING ERRCODE='55000';
    END IF;

    NEW.deadline_operation_kind := derived_operation_kind;
    NEW.deadline_operation_ordinal := derived_ordinal;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION agent.guard_workspace_analysis_runtime_cancellation_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    workflow_status_value text;
    workflow_cancel_requested_at timestamptz;
    workflow_completed_at timestamptz;
    node_key_value text;
    node_status_value text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    node_completed_at timestamptz;
    attempt_status_value text;
    attempt_number integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
    attempt_ended_at timestamptz;
    database_now timestamptz;
    published_json jsonb;
    published_payload jsonb;
BEGIN
    IF NEW.reason<>'WORKSPACE_ANALYSIS_CANCELLED'
       OR NEW.runtime_terminal_at IS NULL
       OR NEW.runtime_terminal_at IS DISTINCT FROM NEW.checked_at
       OR NEW.operation_id IS NOT NULL
       OR NEW.artifact_kind IS NOT NULL
       OR NEW.artifact_id IS NOT NULL
       OR NEW.artifact_hash IS NOT NULL
       OR NEW.requested_model_calls IS NOT NULL
       OR NEW.requested_tool_calls IS NOT NULL
       OR NEW.requested_source_reads IS NOT NULL
       OR NEW.requested_input_tokens IS NOT NULL
       OR NEW.requested_output_tokens IS NOT NULL
       OR NEW.requested_cost_microunits IS NOT NULL
       OR NEW.receipt_failure_code IS NOT NULL
       OR NEW.receipt_failure_id IS NOT NULL
       OR NEW.expected_hash IS NOT NULL
       OR NEW.actual_hash IS NOT NULL
       OR NEW.published_model_run_id IS NOT NULL
       OR NEW.deadline_operation_kind IS NOT NULL
       OR NEW.deadline_operation_ordinal IS NOT NULL THEN
        RAISE EXCEPTION 'workspace analysis runtime cancellation proof shape is invalid'
            USING ERRCODE='55000';
    END IF;

    SELECT status,cancel_requested_at,completed_at
      INTO workflow_status_value,workflow_cancel_requested_at,workflow_completed_at
      FROM workflow.run
     WHERE id=NEW.workflow_run_id
       AND workspace_id=NEW.workspace_id
     FOR UPDATE;
    SELECT node_key,status,attempt,lease_owner,lease_until,completed_at
      INTO node_key_value,node_status_value,node_attempt_no,node_lease_owner,node_lease_until,node_completed_at
      FROM workflow.node_run
     WHERE id=NEW.terminal_node_run_id
       AND run_id=NEW.workflow_run_id
     FOR UPDATE;
    IF NEW.terminal_node_attempt_id IS NOT NULL THEN
        SELECT status,attempt_no,lease_owner,lease_until,ended_at
          INTO attempt_status_value,attempt_number,attempt_lease_owner,attempt_lease_until,attempt_ended_at
          FROM workflow.node_attempt
         WHERE id=NEW.terminal_node_attempt_id
           AND node_run_id=NEW.terminal_node_run_id
         FOR UPDATE;
    END IF;
    SELECT * INTO run_row
      FROM agent.workspace_analysis_run
     WHERE id=NEW.analysis_run_id
       AND workspace_id=NEW.workspace_id
       AND answer_id=NEW.answer_id
       AND workflow_run_id=NEW.workflow_run_id
     FOR UPDATE;

    PERFORM id
      FROM agent.workspace_analysis_operation
     WHERE analysis_run_id=NEW.analysis_run_id
     ORDER BY id
     FOR UPDATE;
    PERFORM id
      FROM agent.workspace_analysis_budget_reservation
     WHERE analysis_run_id=NEW.analysis_run_id
     ORDER BY id
     FOR UPDATE;
    database_now := clock_timestamp();

    IF run_row.id IS NULL
       OR run_row.status NOT IN ('queued','running')
       OR run_row.termination_reason IS NOT NULL
       OR workflow_status_value<>'cancelled'
       OR workflow_cancel_requested_at IS NULL
       OR workflow_cancel_requested_at>NEW.runtime_terminal_at
       OR workflow_completed_at IS DISTINCT FROM NEW.runtime_terminal_at
       OR (run_row.definition_version=1 AND node_key_value NOT IN (
            'inspect_workspace','retrieve_evidence','read_evidence','synthesize_answer','validate_citations','review_publish'))
       OR (run_row.definition_version=2 AND (node_key_value NOT IN ('decide_next','synthesize_answer','validate_citations','review_publish')
            OR NOT EXISTS (SELECT 1 FROM workflow.node_run WHERE id=NEW.terminal_node_run_id AND node_type='agent.workspace-analysis.v2.'||node_key)))
       OR node_status_value<>'cancelled'
       OR node_lease_owner IS NOT NULL
       OR node_lease_until IS NOT NULL
       OR node_completed_at IS DISTINCT FROM NEW.runtime_terminal_at
       OR NEW.runtime_terminal_at>database_now
       OR NEW.created_at>database_now
       OR NEW.created_at<NEW.runtime_terminal_at
       OR NEW.created_at<run_row.updated_at THEN
        RAISE EXCEPTION 'workspace analysis runtime cancellation fence is invalid'
            USING ERRCODE='55000';
    END IF;

    IF NEW.terminal_node_attempt_id IS NULL THEN
        IF node_attempt_no<>0 OR EXISTS (
            SELECT 1
              FROM workflow.node_attempt
             WHERE node_run_id=NEW.terminal_node_run_id
        ) THEN
            RAISE EXCEPTION 'workspace analysis direct cancellation attempt binding is invalid'
                USING ERRCODE='55000';
        END IF;
    ELSIF attempt_status_value IS NULL
       OR attempt_status_value NOT IN (
            'succeeded','waiting_for_human','retry_scheduled','failed',
            'manual_recovery','lease_lost','cancelled'
       )
       OR attempt_number IS DISTINCT FROM node_attempt_no
       OR attempt_lease_owner IS NOT NULL
       OR attempt_lease_until IS NOT NULL
       OR attempt_ended_at IS NULL
       OR attempt_ended_at>NEW.runtime_terminal_at THEN
        RAISE EXCEPTION 'workspace analysis cancellation attempt binding is invalid'
            USING ERRCODE='55000';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.analysis_run_id
           AND status<>'SUCCEEDED'
           AND NOT (run_row.definition_version=2 AND agent.workspace_analysis_v2_uncalled_pending(id))
    ) OR EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_budget_reservation
         WHERE analysis_run_id=NEW.analysis_run_id
           AND status='RESERVED'
    ) THEN
        RAISE EXCEPTION 'workspace analysis cancellation cannot mask unfinished or failed work'
            USING ERRCODE='55000';
    END IF;

    IF run_row.definition_version=2 AND EXISTS (SELECT 1 FROM agent.model_run
        WHERE workspace_id=run_row.workspace_id AND workflow_run_id=run_row.workflow_run_id AND status='RUNNING') THEN
        RAISE EXCEPTION 'dynamic runtime terminal model prefix is not closed' USING ERRCODE='55000';
    END IF;

    published_json := convert_from(NEW.published_document,'UTF8')::jsonb;
    published_payload := published_json->'payload';
    IF jsonb_typeof(published_json) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_json))<>5
       OR published_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_termination'
       OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_termination'
       OR published_json->>'schema_version' IS DISTINCT FROM 'v1'
       OR NOT (published_json ? 'model_run_ref')
       OR jsonb_typeof(published_json->'model_run_ref') IS DISTINCT FROM 'null'
       OR jsonb_typeof(published_payload) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_payload))<>2
       OR published_payload->>'termination_reason' IS DISTINCT FROM NEW.reason
       OR published_payload->>'summary' IS DISTINCT FROM 'The workspace analysis was cancelled.' THEN
        RAISE EXCEPTION 'workspace analysis runtime cancellation publication is invalid'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION agent.guard_workspace_analysis_runtime_failure_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_row agent.workspace_analysis_run%ROWTYPE;
    workflow_status_value text;
    workflow_cancel_requested_at timestamptz;
    workflow_completed_at timestamptz;
    node_key_value text;
    node_status_value text;
    node_attempt_no integer;
    node_lease_owner text;
    node_lease_until timestamptz;
    node_completed_at timestamptz;
    node_failure_class text;
    node_error_code text;
    node_error_summary text;
    attempt_status_value text;
    attempt_number integer;
    attempt_lease_owner text;
    attempt_lease_until timestamptz;
    attempt_ended_at timestamptz;
    attempt_failure_class text;
    attempt_error_code text;
    attempt_error_summary text;
    expected_summary text;
    database_now timestamptz;
    published_json jsonb;
    published_payload jsonb;
BEGIN
    IF NEW.reason<>'WORKSPACE_ANALYSIS_RUNTIME_FAILED'
       OR NEW.runtime_terminal_at IS NULL
       OR NEW.runtime_terminal_at IS DISTINCT FROM NEW.checked_at
       OR NEW.operation_id IS NOT NULL
       OR NEW.artifact_kind IS NOT NULL
       OR NEW.artifact_id IS NOT NULL
       OR NEW.artifact_hash IS NOT NULL
       OR NEW.requested_model_calls IS NOT NULL
       OR NEW.requested_tool_calls IS NOT NULL
       OR NEW.requested_source_reads IS NOT NULL
       OR NEW.requested_input_tokens IS NOT NULL
       OR NEW.requested_output_tokens IS NOT NULL
       OR NEW.requested_cost_microunits IS NOT NULL
       OR NEW.receipt_failure_code IS NOT NULL
       OR NEW.receipt_failure_id IS NOT NULL
       OR NEW.expected_hash IS NOT NULL
       OR NEW.actual_hash IS NOT NULL
       OR NEW.published_model_run_id IS NOT NULL
       OR NEW.deadline_operation_kind IS NOT NULL
       OR NEW.deadline_operation_ordinal IS NOT NULL THEN
        RAISE EXCEPTION 'workspace analysis runtime failure proof shape is invalid'
            USING ERRCODE='55000';
    END IF;

    SELECT status,cancel_requested_at,completed_at
      INTO workflow_status_value,workflow_cancel_requested_at,workflow_completed_at
      FROM workflow.run
     WHERE id=NEW.workflow_run_id
       AND workspace_id=NEW.workspace_id
     FOR UPDATE;
    SELECT node_key,status,attempt,lease_owner,lease_until,completed_at,
           failure_class,error_code,error_summary
      INTO node_key_value,node_status_value,node_attempt_no,node_lease_owner,node_lease_until,node_completed_at,
           node_failure_class,node_error_code,node_error_summary
      FROM workflow.node_run
     WHERE id=NEW.terminal_node_run_id
       AND run_id=NEW.workflow_run_id
     FOR UPDATE;
    SELECT status,attempt_no,lease_owner,lease_until,ended_at,
           failure_class,error_code,error_summary
      INTO attempt_status_value,attempt_number,attempt_lease_owner,attempt_lease_until,attempt_ended_at,
           attempt_failure_class,attempt_error_code,attempt_error_summary
      FROM workflow.node_attempt
     WHERE id=NEW.terminal_node_attempt_id
       AND node_run_id=NEW.terminal_node_run_id
     FOR UPDATE;
    SELECT * INTO run_row
      FROM agent.workspace_analysis_run
     WHERE id=NEW.analysis_run_id
       AND workspace_id=NEW.workspace_id
       AND answer_id=NEW.answer_id
       AND workflow_run_id=NEW.workflow_run_id
     FOR UPDATE;

    PERFORM id
      FROM agent.workspace_analysis_operation
     WHERE analysis_run_id=NEW.analysis_run_id
     ORDER BY id
     FOR UPDATE;
    PERFORM id
      FROM agent.workspace_analysis_budget_reservation
     WHERE analysis_run_id=NEW.analysis_run_id
     ORDER BY id
     FOR UPDATE;
    database_now := clock_timestamp();

    IF run_row.id IS NULL
       OR run_row.status NOT IN ('queued','running')
       OR run_row.termination_reason IS NOT NULL
       OR workflow_status_value<>'failed'
       OR workflow_cancel_requested_at IS NOT NULL
       OR workflow_completed_at IS DISTINCT FROM NEW.runtime_terminal_at
       OR (run_row.definition_version=1 AND node_key_value NOT IN (
            'inspect_workspace','retrieve_evidence','read_evidence','synthesize_answer','validate_citations','review_publish'))
       OR (run_row.definition_version=2 AND (node_key_value NOT IN ('decide_next','synthesize_answer','validate_citations','review_publish')
            OR NOT EXISTS (SELECT 1 FROM workflow.node_run WHERE id=NEW.terminal_node_run_id AND node_type='agent.workspace-analysis.v2.'||node_key)))
       OR node_status_value<>'failed'
       OR node_lease_owner IS NOT NULL
       OR node_lease_until IS NOT NULL
       OR node_completed_at IS DISTINCT FROM NEW.runtime_terminal_at
       OR node_failure_class IS NULL
       OR node_failure_class='cancelled'
       OR node_error_code IS NULL OR btrim(node_error_code)=''
       OR node_error_summary IS NULL OR btrim(node_error_summary)=''
       OR (NEW.terminal_node_attempt_id IS NULL AND (run_row.definition_version<>2 OR node_attempt_no<>0
            OR EXISTS (SELECT 1 FROM workflow.node_attempt WHERE node_run_id=NEW.terminal_node_run_id)))
       OR (NEW.terminal_node_attempt_id IS NOT NULL AND (
            attempt_status_value IS NULL
       OR attempt_status_value NOT IN ('failed','manual_recovery','lease_lost')
       OR attempt_number IS DISTINCT FROM node_attempt_no
       OR attempt_lease_owner IS NOT NULL
       OR attempt_lease_until IS NOT NULL
       OR attempt_ended_at IS DISTINCT FROM NEW.runtime_terminal_at
       OR attempt_failure_class IS NULL
       OR attempt_failure_class='cancelled'
       OR attempt_error_code IS NULL OR btrim(attempt_error_code)=''
       OR attempt_error_summary IS NULL OR btrim(attempt_error_summary)=''
       ))
       OR NEW.runtime_terminal_at>database_now
       OR NEW.created_at>database_now
       OR NEW.created_at<NEW.runtime_terminal_at
       OR NEW.created_at<run_row.updated_at THEN
        RAISE EXCEPTION 'workspace analysis runtime failure fence is invalid'
            USING ERRCODE='55000';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_operation
         WHERE analysis_run_id=NEW.analysis_run_id
           AND status<>'SUCCEEDED'
           AND NOT (run_row.definition_version=2 AND agent.workspace_analysis_v2_uncalled_pending(id))
    ) OR EXISTS (
        SELECT 1
          FROM agent.workspace_analysis_budget_reservation
         WHERE analysis_run_id=NEW.analysis_run_id
           AND status='RESERVED'
    ) THEN
        RAISE EXCEPTION 'workspace analysis runtime failure cannot mask unfinished or failed work'
            USING ERRCODE='55000';
    END IF;

    expected_summary := 'The workspace analysis stopped because its runtime could not complete safely.';
    IF run_row.definition_version=2 AND EXISTS (SELECT 1 FROM agent.model_run
        WHERE workspace_id=run_row.workspace_id AND workflow_run_id=run_row.workflow_run_id AND status='RUNNING') THEN
        RAISE EXCEPTION 'dynamic runtime terminal model prefix is not closed' USING ERRCODE='55000';
    END IF;

    published_json := convert_from(NEW.published_document,'UTF8')::jsonb;
    published_payload := published_json->'payload';
    IF jsonb_typeof(published_json) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_json))<>5
       OR published_json->>'result_type' IS DISTINCT FROM 'workspace_analysis_termination'
       OR published_json->>'schema_id' IS DISTINCT FROM 'conversation.workspace_analysis_termination'
       OR published_json->>'schema_version' IS DISTINCT FROM 'v1'
       OR NOT (published_json ? 'model_run_ref')
       OR jsonb_typeof(published_json->'model_run_ref') IS DISTINCT FROM 'null'
       OR jsonb_typeof(published_payload) IS DISTINCT FROM 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(published_payload))<>2
       OR published_payload->>'termination_reason' IS DISTINCT FROM NEW.reason
       OR published_payload->>'summary' IS DISTINCT FROM expected_summary THEN
        RAISE EXCEPTION 'workspace analysis runtime failure publication is invalid'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;
-- END TODO2 V2 CONVERSATION FINALIZER CONTRACTS
