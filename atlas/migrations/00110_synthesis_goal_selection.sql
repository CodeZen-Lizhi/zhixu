-- Each catalog batch is partitioned completely before any model can claim it.
CREATE TABLE organizing.synthesis_goal_selection_manifest (
 batch_id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL,
 selection_count integer NOT NULL CHECK(selection_count BETWEEN 0 AND 16384),
 sealed boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(batch_id,workspace_id) REFERENCES organizing.synthesis_goal_catalog_batch(id,workspace_id),
 UNIQUE(batch_id,workspace_id)
);
CREATE TABLE organizing.synthesis_goal_selection (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL,
 request_id uuid NOT NULL,
 catalog_batch_id uuid NOT NULL,
 source_ordinal integer NOT NULL CHECK(source_ordinal BETWEEN 0 AND 31),
 point_offset integer NOT NULL CHECK(point_offset BETWEEN 0 AND 511),
 point_count integer NOT NULL CHECK(point_count BETWEEN 1 AND 32 AND point_offset+point_count<=512),
 payload_hash text NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','RECOVERY_REQUIRED')),
 scheduled_workflow_id uuid,
 workflow_run_id uuid,
 node_run_id uuid,
 node_attempt_id uuid,
 model_input_hash text,
 model_run_id uuid,
 model_output bytea,
 error_code text CHECK(error_code IS NULL OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'),
 retryable boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK(updated_at>=created_at),
 FOREIGN KEY(catalog_batch_id,workspace_id) REFERENCES organizing.synthesis_goal_selection_manifest(batch_id,workspace_id),
 FOREIGN KEY(request_id,workspace_id) REFERENCES organizing.synthesis_goal_request(id,workspace_id),
 FOREIGN KEY(scheduled_workflow_id,workspace_id) REFERENCES workflow.run(id,workspace_id),
 FOREIGN KEY(model_run_id,workspace_id) REFERENCES agent.model_run(id,workspace_id),
 UNIQUE(workspace_id,id), UNIQUE(catalog_batch_id,source_ordinal,point_offset),
 UNIQUE(workspace_id,node_attempt_id), UNIQUE(workspace_id,scheduled_workflow_id),
 CHECK ((workflow_run_id IS NULL AND node_run_id IS NULL AND node_attempt_id IS NULL AND model_input_hash IS NULL) OR
        (workflow_run_id IS NOT NULL AND workflow_run_id=scheduled_workflow_id AND node_run_id IS NOT NULL AND node_attempt_id IS NOT NULL AND (model_input_hash ~ '^[0-9a-f]{64}$') IS TRUE)),
 CHECK ((status='PENDING' AND workflow_run_id IS NULL AND model_run_id IS NULL AND model_output IS NULL AND error_code IS NULL AND NOT retryable) OR
        (status='RUNNING' AND workflow_run_id IS NOT NULL AND model_run_id IS NULL AND model_output IS NULL AND error_code IS NULL AND NOT retryable) OR
        (status='SUCCEEDED' AND workflow_run_id IS NOT NULL AND model_run_id IS NOT NULL AND model_output IS NOT NULL AND octet_length(model_output) BETWEEN 1 AND 131072 AND error_code IS NULL AND NOT retryable) OR
        (status IN ('FAILED','RECOVERY_REQUIRED') AND model_run_id IS NULL AND model_output IS NULL AND error_code IS NOT NULL AND (status<>'RECOVERY_REQUIRED' OR NOT retryable)))
);
CREATE INDEX synthesis_goal_selection_pending ON organizing.synthesis_goal_selection(workspace_id,created_at,id) WHERE status='PENDING' AND scheduled_workflow_id IS NULL;

CREATE FUNCTION organizing.guard_goal_selection_manifest() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.sealed THEN RAISE EXCEPTION 'selection manifest must start open' USING ERRCODE='23514'; END IF;
 ELSIF TG_OP='UPDATE' THEN
  IF OLD.sealed OR NOT NEW.sealed OR (to_jsonb(NEW)-'sealed') IS DISTINCT FROM (to_jsonb(OLD)-'sealed') THEN
   RAISE EXCEPTION 'selection manifest is immutable after sealing' USING ERRCODE='55000'; END IF;
 ELSE RAISE EXCEPTION 'selection manifest is immutable' USING ERRCODE='55000';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER goal_selection_manifest_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.synthesis_goal_selection_manifest FOR EACH ROW EXECUTE FUNCTION organizing.guard_goal_selection_manifest();
CREATE TRIGGER goal_selection_manifest_truncate BEFORE TRUNCATE ON organizing.synthesis_goal_selection_manifest FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

CREATE FUNCTION organizing.check_goal_selection_manifest() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE manifest organizing.synthesis_goal_selection_manifest%ROWTYPE;
BEGIN
 SELECT * INTO STRICT manifest FROM organizing.synthesis_goal_selection_manifest WHERE batch_id=NEW.batch_id;
 IF NOT manifest.sealed OR (SELECT count(*) FROM organizing.synthesis_goal_selection WHERE catalog_batch_id=manifest.batch_id)<>manifest.selection_count
 OR EXISTS (
  SELECT 1 FROM organizing.synthesis_goal_catalog_item item
  JOIN learning.document_knowledge_profile_revision revision ON revision.id=item.profile_revision_id AND revision.workspace_id=item.workspace_id
  WHERE item.batch_id=manifest.batch_id AND (
   COALESCE((SELECT sum(point_count) FROM organizing.synthesis_goal_selection s WHERE s.catalog_batch_id=item.batch_id AND s.source_ordinal=item.ordinal-1),0)
    <> jsonb_array_length(COALESCE(NULLIF(revision.content->'knowledge_points','null'::jsonb),'[]'::jsonb))+jsonb_array_length(COALESCE(NULLIF(revision.content->'examples','null'::jsonb),'[]'::jsonb))
   OR EXISTS(SELECT 1 FROM (
     SELECT point_offset,COALESCE(sum(point_count) OVER(ORDER BY point_offset ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING),0) expected
     FROM organizing.synthesis_goal_selection s WHERE s.catalog_batch_id=item.batch_id AND s.source_ordinal=item.ordinal-1
    ) coverage WHERE point_offset<>expected))) THEN
  RAISE EXCEPTION 'selection manifest must cover every catalog point exactly once' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER goal_selection_manifest_complete AFTER INSERT ON organizing.synthesis_goal_selection_manifest DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.check_goal_selection_manifest();

CREATE FUNCTION organizing.guard_goal_selection() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE is_sealed boolean;
BEGIN
 IF TG_OP='INSERT' THEN
  SELECT sealed INTO STRICT is_sealed FROM organizing.synthesis_goal_selection_manifest WHERE batch_id=NEW.catalog_batch_id AND workspace_id=NEW.workspace_id FOR UPDATE;
  IF is_sealed OR NEW.status<>'PENDING' OR NEW.version<>1 OR NEW.scheduled_workflow_id IS NOT NULL OR NOT EXISTS(
   SELECT 1 FROM organizing.synthesis_goal_catalog_batch b JOIN organizing.synthesis_goal_catalog_item i ON i.batch_id=b.id
   WHERE b.id=NEW.catalog_batch_id AND b.workspace_id=NEW.workspace_id AND b.request_id=NEW.request_id AND i.ordinal=NEW.source_ordinal+1) THEN
   RAISE EXCEPTION 'selection must originate in an open exact catalog manifest' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'selection is persistent' USING ERRCODE='55000'; END IF;
 IF ROW(NEW.id,NEW.workspace_id,NEW.request_id,NEW.catalog_batch_id,NEW.source_ordinal,NEW.point_offset,NEW.point_count,NEW.payload_hash,NEW.created_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.workspace_id,OLD.request_id,OLD.catalog_batch_id,OLD.source_ordinal,OLD.point_offset,OLD.point_count,OLD.payload_hash,OLD.created_at)
 OR NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'selection identity is immutable' USING ERRCODE='55000'; END IF;
 -- Scheduling is atomic with Workflow creation and preserves the input version.
 IF OLD.status='PENDING' AND NEW.status='PENDING' AND OLD.scheduled_workflow_id IS NULL AND NEW.scheduled_workflow_id IS NOT NULL AND NEW.version=OLD.version THEN
  IF NOT EXISTS(SELECT 1 FROM workflow.run r JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id WHERE r.id=NEW.scheduled_workflow_id AND r.workspace_id=NEW.workspace_id
   AND d.key='organizing.goal-point-selection' AND d.version=1
   AND r.input->>'selection_id'=NEW.id::text AND r.input->>'expected_version'=NEW.version::text) THEN
   RAISE EXCEPTION 'selection workflow must bind exact request' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF NEW.version<>OLD.version+1 OR NEW.scheduled_workflow_id IS DISTINCT FROM OLD.scheduled_workflow_id THEN
  RAISE EXCEPTION 'selection version or workflow differs' USING ERRCODE='55000'; END IF;
 IF OLD.status='PENDING' AND NEW.status='RUNNING' THEN
  IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_goal_selection_manifest m WHERE m.batch_id=NEW.catalog_batch_id AND m.sealed) THEN
   RAISE EXCEPTION 'selection manifest is not complete' USING ERRCODE='23514'; END IF;
 ELSIF OLD.status='RUNNING' AND NEW.status IN ('SUCCEEDED','FAILED','RECOVERY_REQUIRED') THEN
  IF ROW(NEW.workflow_run_id,NEW.node_run_id,NEW.node_attempt_id,NEW.model_input_hash)
   IS DISTINCT FROM ROW(OLD.workflow_run_id,OLD.node_run_id,OLD.node_attempt_id,OLD.model_input_hash) THEN
   RAISE EXCEPTION 'selection attempt is immutable' USING ERRCODE='55000'; END IF;
 ELSIF OLD.status='PENDING' AND NEW.status='FAILED' THEN NULL;
 ELSE RAISE EXCEPTION 'selection transition is invalid' USING ERRCODE='55000'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER goal_selection_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.synthesis_goal_selection FOR EACH ROW EXECUTE FUNCTION organizing.guard_goal_selection();
CREATE TRIGGER goal_selection_truncate BEFORE TRUNCATE ON organizing.synthesis_goal_selection FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

-- Preserve all prior ModelRun constraints and admit only claimed anchor requests.
ALTER TABLE agent.model_run DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (final_result_type IS NULL OR final_result_type IN (
        'relation_assessment','rag_answer','refusal','faithfulness_review','tool_request','clarification',
        'artifact_section','document_knowledge_profile','organizing_outline','organizing_document',
        'workspace_analysis_plan','workspace_analysis_answer','synthesis_delta','synthesis_semantic_review','synthesis_note_interview_plan','workspace_analysis_decision','anchor_recommendation','goal_point_selection'));

ALTER TABLE agent.model_run DROP CONSTRAINT IF EXISTS agent_model_run_retrieval_binding,
    ADD CONSTRAINT agent_model_run_retrieval_binding CHECK (
        retrieval_index_version_id IS NOT NULL OR (embedding_version_id IS NULL AND rerank_model_version IS NULL AND (
            (output_schema_id='agent.rag-answer' AND NOT (status='SUCCEEDED' AND final_result_type='rag_answer'))
            OR output_schema_id IN ('organizing.outline-generation','organizing.document-generation','agent.synthesis-delta','agent.synthesis-semantic-review','agent.synthesis-note-interview-plan','agent.workspace-analysis-decision','organizing.anchor-recommendation','organizing.goal-point-selection'))));


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

