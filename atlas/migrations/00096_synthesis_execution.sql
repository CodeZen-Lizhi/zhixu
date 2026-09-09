-- Source processing belongs to Organizing; notification delivery and durable
-- jobs remain Workflow outbox / River. No source text enters these inputs.
CREATE TABLE organizing.synthesis_processing (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    source_event_id uuid NOT NULL REFERENCES workflow.outbox_event(id) ON DELETE RESTRICT,
    source_id uuid NOT NULL,
    source_version_id uuid NOT NULL,
    content_artifact_id uuid NOT NULL,
    parse_projection_id uuid NOT NULL,
    source_content_hash text NOT NULL CHECK (source_content_hash ~ '^[0-9a-f]{64}$'),
    ingestion_attempt_id uuid NOT NULL REFERENCES ingestion.attempt(id) ON DELETE RESTRICT,
    processor_version text NOT NULL CHECK (processor_version='synthesis/v1'),
    source_occurred_at timestamptz NOT NULL,
    processing_key text NOT NULL CHECK (processing_key ~ '^synthesis:[0-9a-f]{64}$'),
    workflow_run_id uuid,
    model_run_id uuid,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    status text NOT NULL CHECK (status IN ('PENDING','RUNNING','SUCCEEDED','NO_CHANGE','SKIPPED','FAILED','RECOVERY_REQUIRED')),
    revision_ids jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(revision_ids)='array' AND jsonb_array_length(revision_ids)<=8),
    error_code text CHECK (error_code IS NULL OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'),
    retryable boolean NOT NULL DEFAULT false,
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_synthesis_processing_workspace UNIQUE(id,workspace_id),
    CONSTRAINT uq_synthesis_processing_source UNIQUE(workspace_id,processing_key),
    CONSTRAINT uq_synthesis_processing_event UNIQUE(source_event_id,processor_version),
    CONSTRAINT fk_synthesis_processing_source FOREIGN KEY(source_id,workspace_id) REFERENCES core.source(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_processing_version FOREIGN KEY(source_version_id,source_id) REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_processing_artifact FOREIGN KEY(workspace_id,content_artifact_id) REFERENCES core.content_artifact(workspace_id,id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_processing_projection FOREIGN KEY(source_version_id,parse_projection_id,workspace_id) REFERENCES ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_processing_workflow FOREIGN KEY(workflow_run_id,workspace_id) REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_processing_model FOREIGN KEY(model_run_id,workspace_id) REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT synthesis_processing_lifecycle CHECK (
        updated_at>=created_at AND (completed_at IS NULL OR completed_at=updated_at)
        AND ((status IN ('PENDING','RUNNING') AND workflow_run_id IS NOT NULL AND completed_at IS NULL AND error_code IS NULL AND NOT retryable AND revision_ids='[]'::jsonb)
          OR (status='SKIPPED' AND workflow_run_id IS NULL AND model_run_id IS NULL AND completed_at IS NOT NULL AND error_code IS NULL AND NOT retryable AND revision_ids='[]'::jsonb)
          OR (status='SUCCEEDED' AND workflow_run_id IS NOT NULL AND completed_at IS NOT NULL AND error_code IS NULL AND NOT retryable AND jsonb_array_length(revision_ids)>0)
          OR (status='NO_CHANGE' AND workflow_run_id IS NOT NULL AND completed_at IS NOT NULL AND error_code IS NULL AND NOT retryable AND revision_ids='[]'::jsonb)
          OR (status IN ('FAILED','RECOVERY_REQUIRED') AND workflow_run_id IS NOT NULL AND completed_at IS NOT NULL AND error_code IS NOT NULL AND revision_ids='[]'::jsonb AND (status<>'RECOVERY_REQUIRED' OR NOT retryable)))
    )
);

CREATE INDEX idx_synthesis_processing_list ON organizing.synthesis_processing(workspace_id,updated_at DESC,id DESC);
CREATE UNIQUE INDEX uq_synthesis_processing_active_workspace ON organizing.synthesis_processing(workspace_id) WHERE status IN ('PENDING','RUNNING');

CREATE TABLE organizing.synthesis_execution (
    workflow_run_id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    processing_id uuid NOT NULL,
    execution_no integer NOT NULL CHECK (execution_no BETWEEN 1 AND 10),
    apply_recovery boolean NOT NULL DEFAULT false,
    input_document jsonb,
    input_hash text CHECK (input_hash IS NULL OR input_hash ~ '^[0-9a-f]{64}$'),
    apply_node_run_id uuid,
    apply_node_attempt_id uuid,
    applied_result jsonb,
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL CHECK (updated_at>=created_at),
    CONSTRAINT uq_synthesis_execution_workspace UNIQUE(workflow_run_id,workspace_id),
    CONSTRAINT uq_synthesis_execution_processing UNIQUE(processing_id,execution_no),
    CONSTRAINT uq_synthesis_execution_binding UNIQUE(workflow_run_id,workspace_id,processing_id),
    CONSTRAINT fk_synthesis_execution_processing FOREIGN KEY(processing_id,workspace_id) REFERENCES organizing.synthesis_processing(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_execution_workflow FOREIGN KEY(workflow_run_id,workspace_id) REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_execution_apply_node FOREIGN KEY(apply_node_run_id,workflow_run_id) REFERENCES workflow.node_run(id,run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_execution_apply_attempt FOREIGN KEY(apply_node_attempt_id,apply_node_run_id) REFERENCES workflow.node_attempt(id,node_run_id) ON DELETE RESTRICT,
    CONSTRAINT synthesis_execution_input_shape CHECK ((
        (input_document IS NULL AND input_hash IS NULL)
        OR (NOT apply_recovery AND input_document IS NOT NULL AND jsonb_typeof(input_document)='object' AND input_hash IS NOT NULL
            AND input_document ?& ARRAY['processing_id','workflow_run_id','source_event','notes','sources','request_hash']
            AND input_document->>'processing_id'=processing_id::text
            AND input_document->>'workflow_run_id'=workflow_run_id::text
            AND input_document->>'request_hash'=input_hash
            AND jsonb_typeof(input_document->'notes')='array' AND jsonb_array_length(input_document->'notes')<=24
            AND jsonb_typeof(input_document->'sources')='array' AND jsonb_array_length(input_document->'sources') BETWEEN 1 AND 256
            AND octet_length(input_document::text)<=1048576)
    ) IS TRUE),
    CONSTRAINT synthesis_execution_apply_shape CHECK ((
        (apply_node_run_id IS NULL AND apply_node_attempt_id IS NULL AND applied_result IS NULL)
        OR (apply_node_run_id IS NOT NULL AND apply_node_attempt_id IS NOT NULL AND (
            applied_result IS NULL OR (jsonb_typeof(applied_result)='object' AND applied_result ?& ARRAY['processing_id','revision_ids','changed','publications','replayed']
                AND applied_result->>'processing_id'=processing_id::text AND jsonb_typeof(applied_result->'revision_ids')='array'
                AND jsonb_typeof(applied_result->'publications')='array' AND jsonb_typeof(applied_result->'changed')='boolean'
                AND applied_result->'replayed'='false'::jsonb)))
    ) IS TRUE)
);

CREATE TABLE organizing.synthesis_model_step (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    processing_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_attempt_id uuid NOT NULL,
    stage text NOT NULL CHECK (stage IN ('GENERATE','VALIDATE')),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    input_request_hash text NOT NULL CHECK (input_request_hash ~ '^[0-9a-f]{64}$'),
    generation_output_hash text CHECK (generation_output_hash IS NULL OR generation_output_hash ~ '^[0-9a-f]{64}$'),
    model_settings_revision bigint CHECK (model_settings_revision IS NULL OR model_settings_revision>=0),
    model_run_id uuid,
    status text NOT NULL CHECK (status IN ('RUNNING','READY','FAILED','RECOVERY_REQUIRED')),
    output_document bytea,
    output_hash text CHECK (output_hash IS NULL OR output_hash ~ '^[0-9a-f]{64}$'),
    generation_result jsonb,
    semantic_receipt jsonb,
    error_code text CHECK (error_code IS NULL OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'),
    retryable boolean NOT NULL DEFAULT false,
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_synthesis_model_step_workspace UNIQUE(id,workspace_id),
    CONSTRAINT uq_synthesis_model_step_attempt UNIQUE(node_attempt_id),
    CONSTRAINT uq_synthesis_model_step_model UNIQUE(model_run_id),
    CONSTRAINT fk_synthesis_model_step_execution FOREIGN KEY(workflow_run_id,workspace_id,processing_id) REFERENCES organizing.synthesis_execution(workflow_run_id,workspace_id,processing_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_model_step_node FOREIGN KEY(node_run_id,workflow_run_id) REFERENCES workflow.node_run(id,run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_model_step_attempt FOREIGN KEY(node_attempt_id,node_run_id) REFERENCES workflow.node_attempt(id,node_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_model_step_model FOREIGN KEY(model_run_id,workspace_id) REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT synthesis_model_step_stage CHECK ((stage='GENERATE' AND generation_output_hash IS NULL) OR (stage='VALIDATE' AND generation_output_hash IS NOT NULL)),
    CONSTRAINT synthesis_model_step_lifecycle CHECK ((
        updated_at>=created_at AND (completed_at IS NULL OR completed_at=updated_at)
        AND ((status='RUNNING' AND completed_at IS NULL AND output_document IS NULL AND output_hash IS NULL AND generation_result IS NULL AND semantic_receipt IS NULL AND error_code IS NULL AND NOT retryable)
          OR (status='READY' AND model_run_id IS NOT NULL AND completed_at IS NOT NULL AND output_document IS NOT NULL AND output_hash IS NOT NULL AND octet_length(output_document) BETWEEN 1 AND 262144
              AND encode(sha256(output_document),'hex')=output_hash AND error_code IS NULL AND NOT retryable
              AND ((stage='GENERATE' AND generation_result IS NOT NULL AND jsonb_typeof(generation_result)='object' AND semantic_receipt IS NULL
                    AND generation_result ?& ARRAY['ModelRunID','RequestHash','OutputHash','Notes']
                    AND generation_result->>'ModelRunID'=model_run_id::text AND generation_result->>'RequestHash'=input_request_hash AND generation_result->>'OutputHash'=output_hash)
                OR (stage='VALIDATE' AND generation_result IS NULL AND semantic_receipt IS NOT NULL AND jsonb_typeof(semantic_receipt)='object'
                    AND semantic_receipt ?& ARRAY['model_run_id','request_hash','generation_model_run_id','generation_output_hash','output_hash','accepted','check_count']
                    AND semantic_receipt->>'model_run_id'=model_run_id::text AND semantic_receipt->>'request_hash'=input_request_hash
                    AND semantic_receipt->>'generation_output_hash'=generation_output_hash AND semantic_receipt->>'output_hash'=output_hash
                    AND jsonb_typeof(semantic_receipt->'accepted')='boolean' AND (semantic_receipt->>'check_count')::integer BETWEEN 1 AND 512)))
          OR (status IN ('FAILED','RECOVERY_REQUIRED') AND completed_at IS NOT NULL AND output_document IS NULL AND output_hash IS NULL AND generation_result IS NULL AND semantic_receipt IS NULL AND error_code IS NOT NULL AND (status<>'RECOVERY_REQUIRED' OR NOT retryable)))
    ) IS TRUE)
);

CREATE UNIQUE INDEX uq_synthesis_model_step_active ON organizing.synthesis_model_step(workflow_run_id,stage) WHERE status IN ('RUNNING','READY','RECOVERY_REQUIRED');

CREATE TABLE organizing.synthesis_retry_receipt (
    workspace_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128 AND idempotency_key=btrim(idempotency_key)),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    processing_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    response jsonb NOT NULL CHECK (jsonb_typeof(response)='object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY(workspace_id,idempotency_key),
    FOREIGN KEY(processing_id,workspace_id) REFERENCES organizing.synthesis_processing(id,workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY(workflow_run_id,workspace_id,processing_id) REFERENCES organizing.synthesis_execution(workflow_run_id,workspace_id,processing_id) ON DELETE RESTRICT
);

CREATE TRIGGER synthesis_retry_receipt_immutable BEFORE UPDATE OR DELETE ON organizing.synthesis_retry_receipt
    FOR EACH ROW EXECUTE FUNCTION organizing.reject_immutable_mutation();

CREATE FUNCTION organizing.guard_synthesis_processing()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 OR NEW.status NOT IN ('PENDING','SKIPPED') OR NEW.model_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'synthesis processing must begin pending or skipped' USING ERRCODE='23514';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM workflow.outbox_event e
            JOIN core.source_version v ON v.id=NEW.source_version_id AND v.source_id=NEW.source_id
            JOIN ingestion.parse_projection p ON p.id=NEW.parse_projection_id AND p.workspace_id=NEW.workspace_id
            WHERE e.id=NEW.source_event_id AND e.workspace_id=NEW.workspace_id AND e.event_type='ingestion.source.ready'
              AND e.payload->>'source_id'=NEW.source_id::text AND e.payload->>'source_version_id'=NEW.source_version_id::text
              AND e.payload->>'content_artifact_id'=NEW.content_artifact_id::text AND e.payload->>'parse_projection_id'=NEW.parse_projection_id::text
              AND e.payload->>'content_hash'=NEW.source_content_hash
              AND e.payload->>'ingestion_attempt_id'=NEW.ingestion_attempt_id::text AND e.occurred_at=NEW.source_occurred_at
              AND v.content_hash=NEW.source_content_hash AND v.content_artifact_id=NEW.content_artifact_id
              AND p.content_artifact_id=NEW.content_artifact_id) THEN
            RAISE EXCEPTION 'synthesis processing source event binding is invalid' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE' OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.source_event_id<>OLD.source_event_id OR NEW.source_id<>OLD.source_id OR NEW.source_version_id<>OLD.source_version_id
       OR NEW.content_artifact_id<>OLD.content_artifact_id OR NEW.parse_projection_id<>OLD.parse_projection_id
       OR NEW.source_content_hash<>OLD.source_content_hash OR NEW.ingestion_attempt_id<>OLD.ingestion_attempt_id
       OR NEW.processor_version<>OLD.processor_version OR NEW.source_occurred_at<>OLD.source_occurred_at
       OR NEW.processing_key<>OLD.processing_key OR NEW.request_hash<>OLD.request_hash OR NEW.created_at<>OLD.created_at
       OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at OR OLD.status IN ('SUCCEEDED','NO_CHANGE','SKIPPED','RECOVERY_REQUIRED') THEN
        RAISE EXCEPTION 'synthesis processing transition is invalid' USING ERRCODE='55000';
    END IF;
    IF OLD.status='FAILED' THEN
        IF NOT OLD.retryable OR NEW.status<>'PENDING' OR NEW.workflow_run_id=OLD.workflow_run_id OR NEW.model_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'synthesis processing retry is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id THEN
        RAISE EXCEPTION 'synthesis processing active run is immutable' USING ERRCODE='55000';
    END IF;
    IF NEW.status IN ('SUCCEEDED','NO_CHANGE') AND NOT EXISTS (
        SELECT 1 FROM organizing.synthesis_execution e JOIN workflow.run r ON r.id=e.workflow_run_id AND r.workspace_id=e.workspace_id
        WHERE e.processing_id=NEW.id AND e.workspace_id=NEW.workspace_id AND e.workflow_run_id=NEW.workflow_run_id
          AND r.status='succeeded' AND e.applied_result IS NOT NULL
          AND e.applied_result->'revision_ids'=NEW.revision_ids
          AND (e.applied_result->>'changed')::boolean=(NEW.status='SUCCEEDED')
    ) THEN
        RAISE EXCEPTION 'synthesis processing success requires Workflow application proof' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_processing_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.synthesis_processing
    FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_processing();

CREATE FUNCTION organizing.guard_synthesis_execution()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 OR NEW.input_document IS NOT NULL OR NEW.apply_node_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'synthesis execution must start without results' USING ERRCODE='23514';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM workflow.run r JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id
            WHERE r.id=NEW.workflow_run_id AND r.workspace_id=NEW.workspace_id AND d.key='organizing.synthesis-note' AND d.version=1
              AND r.input->>'processing_id'=NEW.processing_id::text AND (r.input->>'execution_no')::integer=NEW.execution_no
              AND (r.input->>'apply_recovery')::boolean=NEW.apply_recovery) THEN
            RAISE EXCEPTION 'synthesis execution workflow binding is invalid' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE' OR NEW.workflow_run_id<>OLD.workflow_run_id OR NEW.workspace_id<>OLD.workspace_id OR NEW.processing_id<>OLD.processing_id
       OR NEW.execution_no<>OLD.execution_no OR NEW.apply_recovery<>OLD.apply_recovery OR NEW.created_at<>OLD.created_at
       OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
       OR (OLD.input_document IS NOT NULL AND (NEW.input_document IS DISTINCT FROM OLD.input_document OR NEW.input_hash IS DISTINCT FROM OLD.input_hash))
       OR OLD.applied_result IS NOT NULL THEN
        RAISE EXCEPTION 'synthesis execution immutable result changed' USING ERRCODE='55000';
    END IF;
    IF NEW.applied_result IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM organizing.synthesis_apply_receipt receipt
        WHERE receipt.workspace_id=NEW.workspace_id AND receipt.processing_id=NEW.processing_id
          AND (NEW.apply_recovery OR receipt.workflow_run_id=NEW.workflow_run_id)
          AND receipt.result=NEW.applied_result
    ) THEN
        RAISE EXCEPTION 'synthesis execution result requires its immutable application receipt' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_execution_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.synthesis_execution
    FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_execution();

CREATE FUNCTION organizing.guard_synthesis_model_step()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.status<>'RUNNING' OR NEW.version<>1 OR NEW.model_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'synthesis model step must start unbound' USING ERRCODE='23514';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM organizing.synthesis_execution e JOIN workflow.node_run n ON n.run_id=e.workflow_run_id
            WHERE e.workflow_run_id=NEW.workflow_run_id AND e.workspace_id=NEW.workspace_id AND e.processing_id=NEW.processing_id
              AND NOT e.apply_recovery AND e.input_hash=NEW.input_request_hash AND n.id=NEW.node_run_id
              AND ((NEW.stage='GENERATE' AND n.node_type='organizing.synthesis.generate') OR (NEW.stage='VALIDATE' AND n.node_type='organizing.synthesis.validate'))) THEN
            RAISE EXCEPTION 'synthesis model step input binding is invalid' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE' OR OLD.status<>'RUNNING' OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.processing_id<>OLD.processing_id
       OR NEW.workflow_run_id<>OLD.workflow_run_id OR NEW.node_run_id<>OLD.node_run_id OR NEW.node_attempt_id<>OLD.node_attempt_id
       OR NEW.stage<>OLD.stage OR NEW.request_hash<>OLD.request_hash OR NEW.input_request_hash<>OLD.input_request_hash
       OR NEW.generation_output_hash IS DISTINCT FROM OLD.generation_output_hash OR NEW.model_settings_revision IS DISTINCT FROM OLD.model_settings_revision
       OR NEW.created_at<>OLD.created_at OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
       OR (OLD.model_run_id IS NOT NULL AND NEW.model_run_id IS DISTINCT FROM OLD.model_run_id)
       OR (NEW.status='RUNNING' AND (OLD.model_run_id IS NOT NULL OR NEW.model_run_id IS NULL)) THEN
        RAISE EXCEPTION 'synthesis model step transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.model_run_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM agent.model_run r WHERE r.id=NEW.model_run_id AND r.workspace_id=NEW.workspace_id
          AND r.workflow_run_id=NEW.workflow_run_id AND r.node_run_id=NEW.node_run_id AND r.node_attempt_id=NEW.node_attempt_id
          AND r.model_settings_revision IS NOT DISTINCT FROM NEW.model_settings_revision
          AND ((NEW.stage='GENERATE' AND r.output_schema_id='agent.synthesis-delta') OR (NEW.stage='VALIDATE' AND r.output_schema_id='agent.synthesis-semantic-review'))
          AND r.output_schema_version='v1'
          AND ((NEW.status='RUNNING' AND r.status='RUNNING')
            OR (NEW.status='FAILED' AND r.status='FAILED')
            OR (NEW.status='RECOVERY_REQUIRED' AND r.status='UNKNOWN')
            OR (NEW.status='READY' AND r.status='SUCCEEDED' AND ((NEW.stage='GENERATE' AND r.final_result_type='synthesis_delta') OR (NEW.stage='VALIDATE' AND r.final_result_type='synthesis_semantic_review'))))
    ) THEN
        RAISE EXCEPTION 'synthesis model step requires its exact ModelRun proof' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_model_step_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.synthesis_model_step
    FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_model_step();

CREATE TRIGGER synthesis_processing_no_truncate BEFORE TRUNCATE ON organizing.synthesis_processing
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER synthesis_execution_no_truncate BEFORE TRUNCATE ON organizing.synthesis_execution
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER synthesis_model_step_no_truncate BEFORE TRUNCATE ON organizing.synthesis_model_step
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER synthesis_retry_receipt_no_truncate BEFORE TRUNCATE ON organizing.synthesis_retry_receipt
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();

ALTER TABLE agent.model_run DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (final_result_type IS NULL OR final_result_type IN (
        'relation_assessment','rag_answer','refusal','faithfulness_review','tool_request','clarification',
        'artifact_section','document_knowledge_profile','organizing_outline','organizing_document',
        'workspace_analysis_plan','workspace_analysis_answer','synthesis_delta','synthesis_semantic_review'));

ALTER TABLE agent.model_run DROP CONSTRAINT IF EXISTS agent_model_run_retrieval_binding,
    ADD CONSTRAINT agent_model_run_retrieval_binding CHECK (
        retrieval_index_version_id IS NOT NULL OR (embedding_version_id IS NULL AND rerank_model_version IS NULL AND (
            (output_schema_id='agent.rag-answer' AND NOT (status='SUCCEEDED' AND final_result_type='rag_answer'))
            OR output_schema_id IN ('organizing.outline-generation','organizing.document-generation','agent.synthesis-delta','agent.synthesis-semantic-review'))));

-- Preserve every historical ModelRun guard; allow only the bound synthesis stages.
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
               'agent.synthesis-delta','agent.synthesis-semantic-review'
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
