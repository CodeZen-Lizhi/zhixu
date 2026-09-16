-- Requests group immutable observations, not their visible P/C base revisions.
-- A request is not an authorization and does not imply a generated candidate.
CREATE TABLE organizing.synthesis_body_refresh_request (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    note_id uuid NOT NULL,
    publication_id uuid NOT NULL,
    impact_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (id,workspace_id),
    UNIQUE (workspace_id,note_id,publication_id),
    FOREIGN KEY (note_id,workspace_id) REFERENCES organizing.synthesis_note(id,workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY (publication_id,workspace_id) REFERENCES authoring.document_publication_binding(id,workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY (impact_id,workspace_id) REFERENCES organizing.synthesis_body_impact(id,workspace_id) ON DELETE RESTRICT
);

CREATE FUNCTION organizing.validate_synthesis_body_refresh_request()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM organizing.synthesis_body_impact i
        WHERE i.id=NEW.impact_id AND i.workspace_id=NEW.workspace_id
          AND i.note_id=NEW.note_id AND i.publication_id=NEW.publication_id
          AND NEW.created_at>=i.detected_at
    ) THEN
        RAISE EXCEPTION 'synthesis body refresh request lacks saved impact binding' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_body_refresh_request_validate BEFORE INSERT ON organizing.synthesis_body_refresh_request
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_body_refresh_request();
CREATE TRIGGER synthesis_body_refresh_request_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_body_refresh_request
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_body_refresh_request_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_body_refresh_request
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

ALTER TABLE organizing.synthesis_processing ADD COLUMN body_refresh_request_id uuid;
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT fk_synthesis_processing_body_refresh
 FOREIGN KEY(body_refresh_request_id,workspace_id) REFERENCES organizing.synthesis_body_refresh_request(id,workspace_id) ON DELETE RESTRICT;
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT uq_synthesis_processing_body_refresh UNIQUE(body_refresh_request_id);
ALTER TABLE organizing.synthesis_processing DROP CONSTRAINT synthesis_processing_processing_key_check;
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT synthesis_processing_processing_key_check
 CHECK(processing_key ~ '^synthesis(-(fusion|goal|body-refresh))?:[0-9a-f]{64}$');
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT synthesis_processing_body_refresh_shape CHECK (
 (body_refresh_request_id IS NULL AND processing_key !~ '^synthesis-body-refresh:') OR
 (body_refresh_request_id IS NOT NULL AND goal_request_id IS NULL AND fusion_request_id IS NULL AND fusion_trigger IS NULL
  AND processing_key='synthesis-body-refresh:' || encode(sha256(convert_to(workspace_id::text || ':' || body_refresh_request_id::text,'UTF8')),'hex')
  AND request_hash=substring(processing_key FROM 24)
  AND status<>'SKIPPED' AND jsonb_array_length(revision_ids)<=1));

CREATE FUNCTION organizing.guard_synthesis_body_refresh_processing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.body_refresh_request_id IS DISTINCT FROM OLD.body_refresh_request_id THEN
  RAISE EXCEPTION 'synthesis body refresh identity is immutable' USING ERRCODE='55000';
 END IF;
 IF NEW.body_refresh_request_id IS NULL THEN RETURN NEW; END IF;
 IF NOT EXISTS (
  SELECT 1 FROM organizing.synthesis_body_refresh_request r
  JOIN organizing.synthesis_body_impact i ON i.id=r.impact_id AND i.workspace_id=r.workspace_id
  JOIN organizing.synthesis_revision upstream ON upstream.id=i.published_revision_id AND upstream.workspace_id=i.workspace_id
  WHERE r.id=NEW.body_refresh_request_id AND r.workspace_id=NEW.workspace_id
    AND upstream.source_event_id=NEW.source_event_id
 ) THEN RAISE EXCEPTION 'synthesis refresh provenance is not the requested upstream revision' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER synthesis_body_refresh_processing_guard BEFORE INSERT OR UPDATE ON organizing.synthesis_processing
 FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_body_refresh_processing();

CREATE FUNCTION organizing.guard_synthesis_body_refresh_execution() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE request_id uuid; queued jsonb;
BEGIN
 SELECT p.body_refresh_request_id,r.input INTO STRICT request_id,queued
 FROM organizing.synthesis_processing p JOIN workflow.run r ON r.id=NEW.workflow_run_id AND r.workspace_id=p.workspace_id
 WHERE p.id=NEW.processing_id AND p.workspace_id=NEW.workspace_id;
 IF request_id IS NULL THEN
  IF queued ? 'body_refresh_request_id' OR (NEW.input_document IS NOT NULL AND NEW.input_document ? 'body_refresh') THEN
   RAISE EXCEPTION 'ordinary synthesis cannot acquire refresh identity' USING ERRCODE='23514'; END IF;
 ELSE
  IF (queued->>'body_refresh_request_id'=request_id::text) IS NOT TRUE
   OR (NEW.input_document IS NOT NULL AND (NEW.input_document->'body_refresh'->'request'->>'id'=request_id::text) IS NOT TRUE)
  THEN RAISE EXCEPTION 'synthesis refresh execution identity changed' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER synthesis_body_refresh_execution_guard BEFORE INSERT OR UPDATE ON organizing.synthesis_execution
 FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_body_refresh_execution();

CREATE FUNCTION organizing.verify_synthesis_body_refresh_input(input jsonb)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE binding jsonb; requested record; item jsonb; base_id uuid; expected_count bigint;
BEGIN
 binding:=input->'body_refresh';
 IF binding IS NULL THEN RETURN; END IF;
 SELECT * INTO STRICT requested FROM organizing.synthesis_body_refresh_request
 WHERE id=(binding->'request'->>'id')::uuid AND workspace_id=(input->'source_event'->'source'->>'workspace_id')::uuid;
 IF (binding->'request'->>'workspace_id'=requested.workspace_id::text) IS NOT TRUE
 OR (binding->'request'->>'note_id'=requested.note_id::text) IS NOT TRUE
 OR (binding->'request'->>'publication_id'=requested.publication_id::text) IS NOT TRUE
 OR (binding->'request'->>'impact_id'=requested.impact_id::text) IS NOT TRUE
 OR (jsonb_array_length(input->'notes')=1) IS NOT TRUE
 OR (input->'notes'->0->'note'->>'id'=requested.note_id::text) IS NOT TRUE
 OR (jsonb_typeof(binding->'items')='array') IS NOT TRUE
 OR (jsonb_array_length(binding->'items') BETWEEN 1 AND 256) IS NOT TRUE
 THEN RAISE EXCEPTION 'refresh frozen request changed' USING ERRCODE='23514'; END IF;
 base_id:=(input->'notes'->0->>'revision_id')::uuid;
 SELECT count(*) INTO expected_count FROM organizing.synthesis_body_impact i
 WHERE i.workspace_id=requested.workspace_id AND i.note_id=requested.note_id AND i.base_revision_id=base_id AND i.publication_id=requested.publication_id;
 IF expected_count<>jsonb_array_length(binding->'items') OR expected_count<>(SELECT count(DISTINCT value->>'impact_id') FROM jsonb_array_elements(binding->'items'))
 THEN RAISE EXCEPTION 'refresh frozen impact group changed' USING ERRCODE='23514'; END IF;
 FOR item IN SELECT value FROM jsonb_array_elements(binding->'items') LOOP
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_body_impact i
 JOIN organizing.synthesis_revision old ON old.id=i.upstream_revision_id AND old.workspace_id=i.workspace_id
 JOIN organizing.synthesis_revision updated ON updated.id=i.published_revision_id AND updated.workspace_id=i.workspace_id
 WHERE i.id=(item->>'impact_id')::uuid AND i.workspace_id=requested.workspace_id AND i.note_id=requested.note_id
 AND i.base_revision_id=base_id AND i.item_id=(item->>'item_id')::uuid AND i.publication_id=requested.publication_id
 AND item->'original'=jsonb_build_object('workspace_id',i.workspace_id,'note_id',i.upstream_note_id,'revision_id',i.upstream_revision_id,'publication_id',i.upstream_publication_id,'item_id',i.upstream_item_id,'projection_hash',old.projection_hash)
 AND item->'updated'=jsonb_build_object('workspace_id',i.workspace_id,'note_id',i.upstream_note_id,'revision_id',i.published_revision_id,'publication_id',i.publication_id,'item_id',i.upstream_item_id,'projection_hash',updated.projection_hash))
 THEN RAISE EXCEPTION 'refresh frozen historical impact binding changed' USING ERRCODE='23514'; END IF;
 END LOOP;
END $$;

CREATE FUNCTION organizing.guard_synthesis_body_refresh_frozen() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.input_document IS NOT NULL THEN PERFORM organizing.verify_synthesis_body_refresh_input(NEW.input_document); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER synthesis_body_refresh_frozen_guard BEFORE INSERT OR UPDATE ON organizing.synthesis_execution
 FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_body_refresh_frozen();

CREATE FUNCTION organizing.guard_synthesis_body_refresh_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.processing_key ~ '^synthesis-body-refresh:' OR EXISTS(
 SELECT 1 FROM organizing.synthesis_processing WHERE id=NEW.processing_id AND workspace_id=NEW.workspace_id AND body_refresh_request_id IS NOT NULL) THEN
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_processing p
 JOIN organizing.synthesis_body_refresh_request r ON r.id=p.body_refresh_request_id AND r.workspace_id=p.workspace_id
 JOIN organizing.synthesis_execution e ON e.processing_id=p.id AND e.workspace_id=p.workspace_id AND e.workflow_run_id=NEW.workflow_run_id
 WHERE p.id=NEW.processing_id AND p.workspace_id=NEW.workspace_id AND p.processing_key=NEW.processing_key AND p.source_event_id=NEW.source_event_id
 AND e.input_document->'body_refresh'->'request'->>'id'=r.id::text
 AND jsonb_array_length(NEW.result->'revision_ids')<=1
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.result->'publications') publication WHERE publication->>'note_id'<>r.note_id::text))
 THEN RAISE EXCEPTION 'refresh receipt is outside its frozen processing' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER synthesis_body_refresh_receipt_guard BEFORE INSERT ON organizing.synthesis_apply_receipt
 FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_body_refresh_receipt();

-- Derive the exact model protocol from the immutable execution, rather than
-- letting a model run opt itself into a newer output schema.
CREATE FUNCTION organizing.synthesis_model_contract_matches(
 workspace uuid, run uuid, stage text, prompt text, prompt_version text, schema_id text, schema_version text)
RETURNS boolean LANGUAGE sql STABLE AS $$
 WITH expected AS (
 SELECT CASE
 WHEN jsonb_typeof(e.input_document->'body_refresh')='object' AND p.body_refresh_request_id IS NOT NULL THEN 'v5'
 WHEN jsonb_typeof(e.input_document->'goal')='object' THEN 'v3'
 WHEN EXISTS(SELECT 1 FROM jsonb_array_elements(e.input_document->'notes') note WHERE NULLIF(note->>'publication_id','') IS NOT NULL) THEN 'v4'
 WHEN EXISTS(SELECT 1 FROM jsonb_array_elements(e.input_document->'notes') note WHERE jsonb_typeof(note->'anchor')='object') THEN 'v2'
 ELSE 'v1' END AS version
 FROM organizing.synthesis_execution e JOIN organizing.synthesis_processing p ON p.id=e.processing_id AND p.workspace_id=e.workspace_id
 WHERE e.workspace_id=workspace AND e.workflow_run_id=run AND e.input_document IS NOT NULL
 ) SELECT EXISTS(SELECT 1 FROM expected WHERE prompt_version=version AND (
 (stage='GENERATE' AND prompt='synthesis-delta' AND schema_id='agent.synthesis-delta'
 AND schema_version=CASE version WHEN 'v5' THEN 'v3' WHEN 'v4' THEN 'v2' ELSE 'v1' END)
 OR (stage='VALIDATE' AND prompt='synthesis-semantic-review' AND schema_id='agent.synthesis-semantic-review' AND schema_version='v1')))
$$;

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
               'agent.synthesis-delta','agent.synthesis-semantic-review','agent.synthesis-note-interview-plan','agent.workspace-analysis-decision','organizing.anchor-recommendation','organizing.goal-point-selection'
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
           AND (NOT organizing.synthesis_model_contract_matches(NEW.workspace_id,NEW.workflow_run_id,CASE NEW.output_schema_id WHEN 'agent.synthesis-delta' THEN 'GENERATE' ELSE 'VALIDATE' END,NEW.prompt_template_id,NEW.prompt_template_version,NEW.output_schema_id,NEW.output_schema_version) OR NEW.retrieval_index_version_id IS NOT NULL
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
        IF NEW.output_schema_id='organizing.goal-point-selection' AND (
           NEW.output_schema_version<>'v1' OR NEW.reduced_schema_id<>NEW.output_schema_id OR NEW.reduced_schema_version<>NEW.output_schema_version
           OR NEW.retrieval_index_version_id IS NOT NULL OR NEW.memory_snapshot_id IS NOT NULL
           OR NEW.prompt_template_id<>'organizing.goal-point-selection' OR NEW.prompt_template_version<>'v1'
           OR NOT EXISTS (SELECT 1 FROM organizing.synthesis_goal_selection selection
              WHERE selection.workspace_id=NEW.workspace_id AND selection.workflow_run_id=NEW.workflow_run_id
                AND selection.node_run_id=NEW.node_run_id AND selection.node_attempt_id=NEW.node_attempt_id
                AND selection.status='RUNNING' AND selection.model_input_hash IS NOT NULL)) THEN
           RAISE EXCEPTION 'goal selection model requires an exact claimed selection' USING ERRCODE='23514';
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

CREATE OR REPLACE FUNCTION organizing.guard_synthesis_model_step()
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
          AND organizing.synthesis_model_contract_matches(r.workspace_id,r.workflow_run_id,NEW.stage,r.prompt_template_id,r.prompt_template_version,r.output_schema_id,r.output_schema_version)
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
