-- Current-text support is independent from immutable body revisions and old
-- processing history. No write to a manuscript, Article or publication owner.
CREATE TABLE organizing.synthesis_manuscript_source_review (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
 origin_processing_id uuid NOT NULL,
 origin_workflow_run_id uuid NOT NULL REFERENCES workflow.run(id) ON DELETE RESTRICT,
 workflow_run_id uuid UNIQUE REFERENCES workflow.run(id) ON DELETE RESTRICT,
 status text NOT NULL CHECK(status IN ('PENDING','PREPARED','RUNNING','REVIEWED','SUCCEEDED','REJECTED','STALE','FAILED','RECOVERY_REQUIRED')),
 version bigint NOT NULL CHECK(version>0),
 snapshot bytea, snapshot_hash text,
 node_run_id uuid REFERENCES workflow.node_run(id) ON DELETE RESTRICT,
 node_attempt_id uuid REFERENCES workflow.node_attempt(id) ON DELETE RESTRICT,
 model_run_id uuid UNIQUE, request_hash text,
 output bytea, receipt_hash text, error_code text, retryable boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,completed_at timestamptz,
 UNIQUE(id,workspace_id), UNIQUE(workspace_id,origin_processing_id,origin_workflow_run_id),
 FOREIGN KEY(origin_processing_id,workspace_id) REFERENCES organizing.synthesis_processing(id,workspace_id) ON DELETE RESTRICT,
 CHECK((snapshot IS NULL AND snapshot_hash IS NULL) OR (octet_length(snapshot) BETWEEN 1 AND 16777216 AND snapshot_hash=encode(sha256(snapshot),'hex'))),
 CHECK(request_hash IS NULL OR request_hash ~ '^[0-9a-f]{64}$'),
 CHECK(output IS NULL OR octet_length(output) BETWEEN 1 AND 262144),
 CHECK(receipt_hash IS NULL OR receipt_hash ~ '^[0-9a-f]{64}$'),
 CHECK(updated_at>=created_at AND (completed_at IS NULL OR completed_at=updated_at))
);
CREATE INDEX synthesis_source_review_origin ON organizing.synthesis_manuscript_source_review(workspace_id,origin_processing_id,created_at,id);

CREATE FUNCTION organizing.source_review_live(w uuid,r uuid,n uuid,a uuid,k text) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM workflow.run run JOIN workflow.definition d ON d.id=run.definition_id AND d.workspace_id=run.workspace_id
 JOIN workflow.node_run node ON node.run_id=run.id JOIN workflow.node_attempt attempt ON attempt.node_run_id=node.id
 WHERE run.workspace_id=w AND run.id=r AND d.key='organizing.synthesis-manuscript-source-review' AND d.version=1
 AND run.status='running' AND run.cancel_requested_at IS NULL AND run.pause_requested_at IS NULL
 AND node.id=n AND node.node_key=k AND node.node_type=k AND node.status='running'
 AND attempt.id=a AND attempt.status='running' AND attempt.attempt_no=node.attempt
 AND attempt.lease_owner=node.lease_owner AND attempt.lease_until=node.lease_until AND attempt.lease_until>clock_timestamp())
$$;
CREATE FUNCTION organizing.guard_synthesis_source_review() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE document jsonb;target jsonb;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'source review history is immutable' USING ERRCODE='55000'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.status<>'PENDING' OR NEW.version<>1 OR NEW.workflow_run_id IS NOT NULL OR NEW.snapshot IS NOT NULL OR NEW.model_run_id IS NOT NULL OR NEW.output IS NOT NULL OR NEW.receipt_hash IS NOT NULL OR NEW.completed_at IS NOT NULL
  OR NOT EXISTS(SELECT 1 FROM organizing.synthesis_processing p JOIN workflow.run r ON r.id=p.workflow_run_id
    WHERE p.workspace_id=NEW.workspace_id AND p.id=NEW.origin_processing_id AND p.workflow_run_id=NEW.origin_workflow_run_id AND p.status='RECOVERY_REQUIRED' AND p.error_code='SYNTHESIS_MANUSCRIPT_SOURCE_REVIEW_REQUIRED' AND r.status='failed') THEN
   RAISE EXCEPTION 'source review requires exact failed source-only origin' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id OR NEW.origin_processing_id IS DISTINCT FROM OLD.origin_processing_id OR NEW.origin_workflow_run_id IS DISTINCT FROM OLD.origin_workflow_run_id OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
 OR (OLD.workflow_run_id IS NOT NULL AND NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id)
 OR (OLD.snapshot IS NOT NULL AND (NEW.snapshot IS DISTINCT FROM OLD.snapshot OR NEW.snapshot_hash IS DISTINCT FROM OLD.snapshot_hash))
 OR (OLD.model_run_id IS NOT NULL AND (NEW.model_run_id IS DISTINCT FROM OLD.model_run_id OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id OR NEW.node_attempt_id IS DISTINCT FROM OLD.node_attempt_id OR NEW.request_hash IS DISTINCT FROM OLD.request_hash))
 OR (OLD.output IS NOT NULL AND NEW.output IS DISTINCT FROM OLD.output)
 OR OLD.status IN ('SUCCEEDED','REJECTED','STALE','FAILED','RECOVERY_REQUIRED')
 OR NOT ((OLD.status='PENDING' AND NEW.status IN ('PENDING','PREPARED','FAILED','STALE')) OR (OLD.status='PREPARED' AND NEW.status IN ('RUNNING','FAILED','STALE')) OR (OLD.status='RUNNING' AND NEW.status IN ('REVIEWED','REJECTED','RECOVERY_REQUIRED','STALE')) OR (OLD.status='REVIEWED' AND NEW.status IN ('SUCCEEDED','STALE'))) THEN
  RAISE EXCEPTION 'source review transition or immutable binding invalid' USING ERRCODE='55000'; END IF;
 IF NEW.workflow_run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM workflow.run r JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id WHERE r.id=NEW.workflow_run_id AND r.workspace_id=NEW.workspace_id AND d.key='organizing.synthesis-manuscript-source-review' AND d.version=1 AND r.input->>'review_id'=NEW.id::text) THEN RAISE EXCEPTION 'source review scheduled workflow mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.status IN ('PREPARED','RUNNING','REVIEWED','REJECTED','SUCCEEDED') THEN
  document:=convert_from(NEW.snapshot,'UTF8')::jsonb;
  IF (jsonb_typeof(document->'targets')='array' AND jsonb_array_length(document->'targets') BETWEEN 1 AND 8 AND jsonb_typeof(document->'obligations')='array' AND jsonb_array_length(document->'obligations') BETWEEN 1 AND 1024) IS NOT TRUE THEN RAISE EXCEPTION 'source review manifest incomplete' USING ERRCODE='23514'; END IF;
  FOR target IN SELECT value FROM jsonb_array_elements(document->'targets') LOOP
   IF (target->>'full_content_hash'=encode(sha256(convert_to(target->>'full_content','UTF8')),'hex') AND target->>'target_kind' IN ('REVISION','LOCAL_FILE') AND EXISTS(SELECT 1 FROM organizing.synthesis_revision r WHERE r.workspace_id=NEW.workspace_id AND r.note_id=(target->>'note_id')::uuid AND r.id=(target->>'base_revision_id')::uuid AND r.projection_hash=target->>'projection_hash')) IS NOT TRUE THEN RAISE EXCEPTION 'source review target invalid' USING ERRCODE='23514'; END IF;
  END LOOP;
 END IF;
 IF NEW.status='RUNNING' AND (NEW.model_run_id IS NULL OR NEW.request_hash IS NULL OR NOT organizing.source_review_live(NEW.workspace_id,NEW.workflow_run_id,NEW.node_run_id,NEW.node_attempt_id,'organizing.synthesis-manuscript-source-review.review')) THEN RAISE EXCEPTION 'source review requires real running model claim' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_source_review_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.synthesis_manuscript_source_review FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_source_review();
CREATE TRIGGER synthesis_source_review_no_truncate BEFORE TRUNCATE ON organizing.synthesis_manuscript_source_review FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

CREATE TABLE organizing.synthesis_manuscript_source_evidence (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, review_id uuid NOT NULL,
 note_id uuid NOT NULL, base_revision_id uuid NOT NULL,target_hash text NOT NULL CHECK(target_hash ~ '^[0-9a-f]{64}$'),
 full_content_hash text NOT NULL CHECK(full_content_hash ~ '^[0-9a-f]{64}$'), obligation text NOT NULL,paragraph text NOT NULL,
 start_byte integer NOT NULL CHECK(start_byte>=0),end_byte integer NOT NULL CHECK(end_byte>start_byte),paragraph_hash text NOT NULL,
 source_id uuid NOT NULL,source_version_id uuid NOT NULL,content_artifact_id uuid NOT NULL,parse_projection_id uuid NOT NULL,
 source_span_id uuid NOT NULL REFERENCES ingestion.source_span(id) ON DELETE RESTRICT,
 content_hash text NOT NULL,excerpt_hash text NOT NULL,title text NOT NULL,created_at timestamptz NOT NULL,
 FOREIGN KEY(review_id,workspace_id) REFERENCES organizing.synthesis_manuscript_source_review(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(base_revision_id,workspace_id,note_id) REFERENCES organizing.synthesis_revision(id,workspace_id,note_id) ON DELETE RESTRICT,
 FOREIGN KEY(source_id,workspace_id) REFERENCES core.source(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(source_version_id,source_id) REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
 FOREIGN KEY(source_version_id,parse_projection_id,workspace_id) REFERENCES ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id) ON DELETE RESTRICT,
 UNIQUE(review_id,obligation,paragraph),
 UNIQUE(workspace_id,note_id,target_hash,paragraph,source_version_id,parse_projection_id,source_span_id)
);
CREATE FUNCTION organizing.validate_synthesis_source_review_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE review organizing.synthesis_manuscript_source_review%ROWTYPE; document jsonb;target jsonb;part jsonb;review_obligation jsonb;check_result jsonb;ref jsonb;
BEGIN
 SELECT * INTO review FROM organizing.synthesis_manuscript_source_review WHERE workspace_id=NEW.workspace_id AND id=NEW.review_id FOR UPDATE;
 document:=convert_from(review.snapshot,'UTF8')::jsonb;
 SELECT t INTO target FROM jsonb_array_elements(document->'targets') t WHERE t->>'note_id'=NEW.note_id::text AND t->>'base_revision_id'=NEW.base_revision_id::text;
 SELECT p INTO part FROM jsonb_array_elements(target->'paragraphs') p WHERE p->>'label'=NEW.paragraph;
 SELECT o INTO review_obligation FROM jsonb_array_elements(document->'obligations') o WHERE o->>'label'=NEW.obligation AND o->>'note'=target->>'label';ref:=review_obligation->'reference';
 SELECT c INTO check_result FROM jsonb_array_elements(convert_from(review.output,'UTF8')::jsonb->'checks') c WHERE c->>'obligation'=NEW.obligation;
 IF (review.status='REVIEWED' AND check_result->>'verdict'='SUPPORTED' AND check_result->>'source'=review_obligation->>'source' AND check_result->'targets' ? NEW.paragraph
 AND NEW.full_content_hash=target->>'full_content_hash' AND NEW.start_byte=(part->>'start_byte')::integer AND NEW.end_byte=(part->>'end_byte')::integer
 AND NEW.paragraph_hash=part->>'hash' AND NEW.paragraph_hash=encode(sha256(substring(convert_to(target->>'full_content','UTF8') FROM NEW.start_byte+1 FOR NEW.end_byte-NEW.start_byte)),'hex')
 AND ref->'source'->>'workspace_id'=NEW.workspace_id::text AND ref->'source'->>'source_id'=NEW.source_id::text
 AND ref->'source'->>'source_version_id'=NEW.source_version_id::text AND ref->'source'->>'content_artifact_id'=NEW.content_artifact_id::text AND ref->'source'->>'parse_projection_id'=NEW.parse_projection_id::text
 AND ref->'source'->>'content_hash'=NEW.content_hash AND ref->>'source_span_id'=NEW.source_span_id::text AND ref->>'excerpt_hash'=NEW.excerpt_hash AND ref->>'title'=NEW.title
 ) IS NOT TRUE THEN RAISE EXCEPTION 'source evidence lacks exact accepted current-text obligation' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_source_review_evidence_validate BEFORE INSERT ON organizing.synthesis_manuscript_source_evidence FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_source_review_evidence();
CREATE TRIGGER synthesis_source_review_evidence_source BEFORE INSERT ON organizing.synthesis_manuscript_source_evidence FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_source();
CREATE TRIGGER synthesis_source_review_evidence_immutable BEFORE UPDATE OR DELETE ON organizing.synthesis_manuscript_source_evidence FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_source_review_evidence_no_truncate BEFORE TRUNCATE ON organizing.synthesis_manuscript_source_evidence FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

CREATE FUNCTION organizing.verify_synthesis_source_review_closure() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE review organizing.synthesis_manuscript_source_review%ROWTYPE;model agent.model_run%ROWTYPE;document jsonb;checks jsonb;review_obligation jsonb;check_result jsonb;idx integer;total integer;last_call agent.model_call%ROWTYPE;
BEGIN
 SELECT * INTO review FROM organizing.synthesis_manuscript_source_review WHERE id=NEW.id;
 IF review.status NOT IN ('REVIEWED','REJECTED','SUCCEEDED') THEN RETURN NULL; END IF;
 SELECT * INTO model FROM agent.model_run WHERE id=review.model_run_id;
 SELECT count(*) INTO total FROM agent.model_call WHERE model_run_id=model.id;
 SELECT * INTO last_call FROM agent.model_call WHERE model_run_id=model.id ORDER BY call_no DESC LIMIT 1;
 IF (model.workspace_id=review.workspace_id AND model.workflow_run_id=review.workflow_run_id AND model.node_run_id=review.node_run_id AND model.node_attempt_id=review.node_attempt_id AND model.status='SUCCEEDED' AND model.final_result_type='synthesis_manuscript_source_review' AND model.output_schema_id='agent.synthesis-manuscript-source-review' AND total BETWEEN 1 AND 3
 AND NOT EXISTS(SELECT 1 FROM agent.model_call c WHERE c.model_run_id=model.id AND (c.status<>'SUCCEEDED' OR c.phase IS DISTINCT FROM (ARRAY['INITIAL','REPAIR','REDUCED'])[c.call_no] OR c.output_schema_id<>model.output_schema_id OR c.output_schema_version<>model.output_schema_version OR c.prompt_template_id<>model.prompt_template_id OR c.prompt_template_version<>model.prompt_template_version))
 AND EXISTS(SELECT 1 FROM agent.model_call c WHERE c.model_run_id=model.id AND c.call_no=1 AND c.request_hash=review.request_hash)
 AND last_call.response_hash=encode(sha256(review.output),'hex') AND last_call.response_bytes=octet_length(review.output)) IS NOT TRUE THEN RAISE EXCEPTION 'source review model proof incomplete' USING ERRCODE='23514'; END IF;
 document:=convert_from(review.snapshot,'UTF8')::jsonb;checks:=convert_from(review.output,'UTF8')::jsonb->'checks';
 IF jsonb_array_length(checks) IS DISTINCT FROM jsonb_array_length(document->'obligations') THEN RAISE EXCEPTION 'source review omitted obligation' USING ERRCODE='23514'; END IF;
 FOR idx IN 0..jsonb_array_length(checks)-1 LOOP
  review_obligation:=document->'obligations'->idx;check_result:=checks->idx;
  IF (check_result->>'obligation'=review_obligation->>'label' AND check_result->>'source'=review_obligation->>'source' AND check_result->>'verdict' IN ('SUPPORTED','UNSUPPORTED','UNCERTAIN') AND jsonb_typeof(check_result->'targets')='array') IS NOT TRUE THEN RAISE EXCEPTION 'source review output binding invalid' USING ERRCODE='23514'; END IF;
  IF review.status IN ('REVIEWED','SUCCEEDED') AND (check_result->>'verdict'<>'SUPPORTED' OR check_result->>'reason_code'<>'CURRENT_TEXT_SUPPORTED' OR jsonb_array_length(check_result->'targets')<1) THEN RAISE EXCEPTION 'source review is not supported' USING ERRCODE='23514'; END IF;
  IF review.status='SUCCEEDED' AND (SELECT count(*) FROM organizing.synthesis_manuscript_source_evidence e WHERE e.review_id=review.id AND e.obligation=review_obligation->>'label')<>jsonb_array_length(check_result->'targets') THEN RAISE EXCEPTION 'source review evidence closure incomplete' USING ERRCODE='23514'; END IF;
 END LOOP;
 IF review.status='SUCCEEDED' AND review.receipt_hash IS NULL THEN RAISE EXCEPTION 'source review missing receipt' USING ERRCODE='23514'; END IF;
 IF review.status<>'SUCCEEDED' AND EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_evidence e WHERE e.review_id=review.id) THEN RAISE EXCEPTION 'uncompleted review cannot retain evidence' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END; $$;
CREATE CONSTRAINT TRIGGER synthesis_source_review_closure AFTER INSERT OR UPDATE ON organizing.synthesis_manuscript_source_review DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_source_review_closure();

-- Additive allowlist; every new run must bind the real running owner claim.
CREATE OR REPLACE FUNCTION agent.guard_model_run_mutation() RETURNS trigger
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
               'agent.synthesis-delta','agent.synthesis-semantic-review','agent.synthesis-note-interview-plan','agent.workspace-analysis-decision','organizing.anchor-recommendation','organizing.goal-point-selection','agent.synthesis-manuscript-source-review'
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
        IF NEW.output_schema_id='agent.synthesis-manuscript-source-review' AND (
           NEW.output_schema_version<>'v1' OR NEW.reduced_schema_id<>NEW.output_schema_id OR NEW.reduced_schema_version<>NEW.output_schema_version
           OR NEW.retrieval_index_version_id IS NOT NULL OR NEW.memory_snapshot_id IS NOT NULL
           OR NEW.prompt_template_id<>NEW.output_schema_id OR NEW.prompt_template_version<>'v1'
           OR NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review review
             WHERE review.workspace_id=NEW.workspace_id AND review.workflow_run_id=NEW.workflow_run_id AND review.model_run_id=NEW.id
             AND review.node_run_id=NEW.node_run_id AND review.node_attempt_id=NEW.node_attempt_id AND review.status='RUNNING' AND review.request_hash IS NOT NULL
             AND organizing.source_review_live(NEW.workspace_id,NEW.workflow_run_id,NEW.node_run_id,NEW.node_attempt_id,'organizing.synthesis-manuscript-source-review.review'))) THEN
           RAISE EXCEPTION 'source review model requires exact running review claim' USING ERRCODE='23514';
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
ALTER TABLE agent.model_run DROP CONSTRAINT agent_model_run_final_result_type_check;
ALTER TABLE agent.model_run ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (((final_result_type IS NULL) OR (final_result_type = ANY (ARRAY['relation_assessment'::text, 'rag_answer'::text, 'refusal'::text, 'faithfulness_review'::text, 'tool_request'::text, 'clarification'::text, 'artifact_section'::text, 'document_knowledge_profile'::text, 'organizing_outline'::text, 'organizing_document'::text, 'workspace_analysis_plan'::text, 'workspace_analysis_answer'::text, 'synthesis_delta'::text, 'synthesis_semantic_review'::text, 'synthesis_note_interview_plan'::text, 'workspace_analysis_decision'::text, 'anchor_recommendation'::text, 'goal_point_selection'::text, 'synthesis_manuscript_source_review'::text]))));
ALTER TABLE agent.model_run DROP CONSTRAINT agent_model_run_retrieval_binding;
ALTER TABLE agent.model_run ADD CONSTRAINT agent_model_run_retrieval_binding CHECK (((retrieval_index_version_id IS NOT NULL) OR ((embedding_version_id IS NULL) AND (rerank_model_version IS NULL) AND (((output_schema_id = 'agent.rag-answer'::text) AND (NOT ((status = 'SUCCEEDED'::text) AND (final_result_type = 'rag_answer'::text)))) OR (output_schema_id = ANY (ARRAY['organizing.outline-generation'::text, 'organizing.document-generation'::text, 'agent.synthesis-delta'::text, 'agent.synthesis-semantic-review'::text, 'agent.synthesis-note-interview-plan'::text, 'agent.workspace-analysis-decision'::text, 'organizing.anchor-recommendation'::text, 'organizing.goal-point-selection'::text, 'agent.synthesis-manuscript-source-review'::text]))))));

-- Evidence inserts also require the complete review receipt in the same
-- transaction; a previously REVIEWED row alone cannot authorize orphan proofs.
CREATE FUNCTION organizing.verify_synthesis_source_evidence_closure() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review r WHERE r.id=NEW.review_id AND r.workspace_id=NEW.workspace_id AND r.status='SUCCEEDED' AND r.receipt_hash IS NOT NULL) THEN
  RAISE EXCEPTION 'source evidence requires atomic completed review receipt' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END; $$;
CREATE CONSTRAINT TRIGGER synthesis_source_evidence_closure AFTER INSERT ON organizing.synthesis_manuscript_source_evidence DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_source_evidence_closure();
