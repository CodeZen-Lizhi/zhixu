-- Note interviews retain their own immutable published material. They reuse the
-- existing Interview state machine and do not create Claim or Index identities.
CREATE TABLE learning.note_interview_preparation (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
 note_id uuid NOT NULL,
 revision_id uuid NOT NULL,
 document_id uuid NOT NULL,
 article_revision_id uuid NOT NULL,
 projection_hash text NOT NULL CHECK (projection_hash ~ '^[0-9a-f]{64}$'),
 snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot)='object' AND octet_length(snapshot::text)<=2097152),
 options jsonb NOT NULL CHECK (jsonb_typeof(options)='object'),
 status text NOT NULL CHECK (status IN ('QUEUED','GENERATING','READY','FAILED','CAPABILITY_UNAVAILABLE','RECOVERY_REQUIRED')),
 workflow_run_id uuid NOT NULL,
 node_run_id uuid NOT NULL,
 node_attempt_id uuid,
 model_run_id uuid,
 model_settings_revision bigint CHECK (model_settings_revision IS NULL OR model_settings_revision>=0),
 model_output bytea CHECK (model_output IS NULL OR octet_length(model_output) BETWEEN 1 AND 262144),
 session_id uuid,
 failure_code text CHECK (failure_code IS NULL OR failure_code ~ '^[A-Z][A-Z0-9_]{0,127}$'),
 failure_retryable boolean NOT NULL DEFAULT false,
 version bigint NOT NULL CHECK (version>0),
 idempotency_key text NOT NULL CHECK (btrim(idempotency_key)<>'' AND octet_length(idempotency_key)<=128),
 request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
 retry_of uuid,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL CHECK (updated_at>=created_at),
 CONSTRAINT uq_note_interview_preparation_workspace UNIQUE(id,workspace_id),
 CONSTRAINT uq_note_interview_preparation_key UNIQUE(workspace_id,idempotency_key),
 CONSTRAINT uq_note_interview_preparation_run UNIQUE(workflow_run_id),
 CONSTRAINT uq_note_interview_preparation_session UNIQUE(session_id),
 CONSTRAINT uq_note_interview_preparation_retry UNIQUE(retry_of),
 CONSTRAINT fk_note_interview_preparation_revision FOREIGN KEY(revision_id,workspace_id,note_id,document_id,article_revision_id,projection_hash)
  REFERENCES organizing.synthesis_revision(id,workspace_id,note_id,document_id,article_revision_id,projection_hash) ON DELETE RESTRICT,
 CONSTRAINT fk_note_interview_preparation_run FOREIGN KEY(workflow_run_id,workspace_id) REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
 CONSTRAINT fk_note_interview_preparation_node FOREIGN KEY(node_run_id,workflow_run_id) REFERENCES workflow.node_run(id,run_id) ON DELETE RESTRICT,
 CONSTRAINT fk_note_interview_preparation_attempt FOREIGN KEY(node_attempt_id,node_run_id) REFERENCES workflow.node_attempt(id,node_run_id) ON DELETE RESTRICT,
 CONSTRAINT fk_note_interview_preparation_model FOREIGN KEY(model_run_id,workspace_id) REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT,
 CONSTRAINT fk_note_interview_preparation_session FOREIGN KEY(session_id,workspace_id) REFERENCES learning.interview_session(session_id,workspace_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
 CONSTRAINT fk_note_interview_preparation_retry FOREIGN KEY(retry_of,workspace_id) REFERENCES learning.note_interview_preparation(id,workspace_id) ON DELETE RESTRICT,
 CONSTRAINT note_interview_preparation_lifecycle CHECK (
  (status='QUEUED' AND node_attempt_id IS NULL AND model_run_id IS NULL AND model_settings_revision IS NULL AND model_output IS NULL AND session_id IS NULL AND failure_code IS NULL AND NOT failure_retryable)
  OR (status='GENERATING' AND node_attempt_id IS NOT NULL AND model_output IS NULL AND session_id IS NULL AND failure_code IS NULL AND NOT failure_retryable)
  OR (status='READY' AND node_attempt_id IS NOT NULL AND model_run_id IS NOT NULL AND model_output IS NOT NULL AND session_id IS NOT NULL AND failure_code IS NULL AND NOT failure_retryable)
  OR (status IN ('FAILED','CAPABILITY_UNAVAILABLE','RECOVERY_REQUIRED') AND model_output IS NULL AND session_id IS NULL AND failure_code IS NOT NULL AND (status<>'RECOVERY_REQUIRED' OR NOT failure_retryable))
 )
);
CREATE INDEX idx_note_interview_preparation_note ON learning.note_interview_preparation(workspace_id,note_id,created_at DESC,id DESC);

CREATE FUNCTION learning.guard_note_interview_preparation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected jsonb; run_status text;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'note interview preparation is retained history' USING ERRCODE='55000'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.status<>'QUEUED' OR NEW.version<>1 THEN RAISE EXCEPTION 'note interview preparation must start queued' USING ERRCODE='23514'; END IF;
  SELECT jsonb_build_object('workspace_id',r.workspace_id,'note_id',r.note_id,'revision_id',r.id,'revision_no',r.revision_no,
   'document_id',r.document_id,'article_revision_id',r.article_revision_id,'article_revision_no',r.article_revision_no,
   'content_hash',r.content_hash,'projection_hash',r.projection_hash,'title',r.title,'renderer_version',r.renderer_version,'items',r.items)
   INTO expected FROM organizing.synthesis_revision r WHERE r.id=NEW.revision_id AND r.workspace_id=NEW.workspace_id;
  IF expected IS NULL OR NEW.snapshot IS DISTINCT FROM expected THEN RAISE EXCEPTION 'note interview snapshot differs from immutable revision' USING ERRCODE='23514'; END IF;
  IF NEW.retry_of IS NULL THEN
   IF NOT EXISTS (
    SELECT 1 FROM core.document d JOIN core.article_revision ar ON ar.id=d.current_published_revision_id AND ar.workspace_id=d.workspace_id AND ar.document_id=d.id
    JOIN authoring.generated_article_revision gr ON gr.article_revision_id=ar.id AND gr.workspace_id=ar.workspace_id AND gr.document_id=ar.document_id AND gr.content_hash=ar.content_hash
    JOIN authoring.document_publication_binding b ON b.article_revision_id=ar.id AND b.workspace_id=ar.workspace_id AND b.document_id=d.id AND b.status='PUBLISHED'
    JOIN change_control.proposal_commit pc ON pc.proposal_id=b.proposal_id AND pc.revision_id=b.proposal_revision_id AND pc.workspace_id=b.workspace_id
      AND pc.target_path=b.target_path AND pc.target_mode=b.target_mode AND pc.result_hash=b.content_hash AND pc.git_commit=b.git_commit
    WHERE d.id=NEW.document_id AND d.workspace_id=NEW.workspace_id AND ar.id=NEW.article_revision_id AND ar.status='PUBLISHED' AND ar.created_by_type='AGENT'
     AND ar.content_hash=NEW.snapshot->>'content_hash' AND ar.content_hash=b.content_hash AND ar.git_commit=b.git_commit AND d.canonical_path=b.target_path AND d.lifecycle_status='PUBLISHED'
   ) THEN RAISE EXCEPTION 'note interview requires an actually published generated revision' USING ERRCODE='23514'; END IF;
  ELSIF NOT EXISTS (SELECT 1 FROM learning.note_interview_preparation p WHERE p.id=NEW.retry_of AND p.workspace_id=NEW.workspace_id
   AND p.status IN ('FAILED','CAPABILITY_UNAVAILABLE') AND p.failure_retryable AND p.snapshot=NEW.snapshot AND p.options=NEW.options) THEN
   RAISE EXCEPTION 'note interview retry changed original material or is not allowed' USING ERRCODE='23514';
  END IF;
  IF NOT (NEW.options ?& ARRAY['role','difficulty','duration_minutes','question_count','max_follow_ups']) OR (SELECT count(*) FROM jsonb_object_keys(NEW.options))<>5
   OR jsonb_typeof(NEW.options->'role')<>'string' OR btrim(NEW.options->>'role')='' OR octet_length(NEW.options->>'role')>256
   OR NEW.options->>'difficulty' NOT IN ('FOUNDATION','INTERMEDIATE','ADVANCED')
   OR (NEW.options->>'duration_minutes')::integer NOT BETWEEN 1 AND 240 OR (NEW.options->>'question_count')::integer NOT BETWEEN 1 AND 20
   OR (NEW.options->>'max_follow_ups')::integer NOT BETWEEN 0 AND 20 THEN RAISE EXCEPTION 'note interview options are invalid' USING ERRCODE='23514'; END IF;
 ELSE
  IF OLD.status NOT IN ('QUEUED','GENERATING') OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
   OR ROW(NEW.id,NEW.workspace_id,NEW.note_id,NEW.revision_id,NEW.document_id,NEW.article_revision_id,NEW.projection_hash,NEW.snapshot,NEW.options,NEW.workflow_run_id,NEW.node_run_id,NEW.idempotency_key,NEW.request_hash,NEW.retry_of,NEW.created_at)
    IS DISTINCT FROM ROW(OLD.id,OLD.workspace_id,OLD.note_id,OLD.revision_id,OLD.document_id,OLD.article_revision_id,OLD.projection_hash,OLD.snapshot,OLD.options,OLD.workflow_run_id,OLD.node_run_id,OLD.idempotency_key,OLD.request_hash,OLD.retry_of,OLD.created_at)
   OR (OLD.node_attempt_id IS NOT NULL AND ROW(NEW.node_attempt_id,NEW.model_settings_revision) IS DISTINCT FROM ROW(OLD.node_attempt_id,OLD.model_settings_revision))
   OR (OLD.model_run_id IS NOT NULL AND NEW.model_run_id IS DISTINCT FROM OLD.model_run_id)
   OR (OLD.status='GENERATING' AND NEW.status='QUEUED') OR (OLD.status='QUEUED' AND NEW.status NOT IN ('GENERATING','FAILED','CAPABILITY_UNAVAILABLE','RECOVERY_REQUIRED')) THEN
   RAISE EXCEPTION 'note interview preparation transition or frozen binding is invalid' USING ERRCODE='55000';
  END IF;
 END IF;
 IF NEW.model_run_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM agent.model_run m WHERE m.id=NEW.model_run_id AND m.workspace_id=NEW.workspace_id
  AND m.workflow_run_id=NEW.workflow_run_id AND m.node_run_id=NEW.node_run_id AND m.node_attempt_id=NEW.node_attempt_id
  AND m.model_settings_revision IS NOT DISTINCT FROM NEW.model_settings_revision AND m.output_schema_id='agent.synthesis-note-interview-plan'
  AND m.output_schema_version='1' AND m.prompt_template_id='interview.synthesis-note-plan' AND m.prompt_template_version='1'
  AND m.reduced_schema_id=m.output_schema_id AND m.reduced_schema_version=m.output_schema_version AND m.retrieval_index_version_id IS NULL) THEN
  RAISE EXCEPTION 'note interview model binding is invalid' USING ERRCODE='23514';
 END IF;
 IF NEW.status='READY' AND NOT EXISTS (SELECT 1 FROM agent.model_run m JOIN LATERAL (SELECT response_hash,response_bytes,status FROM agent.model_call c WHERE c.model_run_id=m.id ORDER BY call_no DESC LIMIT 1) last_call ON true
  WHERE m.id=NEW.model_run_id AND m.status='SUCCEEDED' AND m.final_result_type='synthesis_note_interview_plan' AND last_call.status='SUCCEEDED'
  AND last_call.response_hash=encode(sha256(NEW.model_output),'hex') AND last_call.response_bytes=octet_length(NEW.model_output)) THEN
  RAISE EXCEPTION 'ready note interview has no accepted model response proof' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER trg_note_interview_preparation BEFORE INSERT OR UPDATE OR DELETE ON learning.note_interview_preparation FOR EACH ROW EXECUTE FUNCTION learning.guard_note_interview_preparation();

ALTER TABLE learning.interview_question
 ALTER COLUMN claim_id DROP NOT NULL,
 DROP CONSTRAINT interview_question_evidence_check,
 ADD COLUMN source_kind text NOT NULL DEFAULT 'CLAIM',
 ADD COLUMN note_source jsonb,
 ADD COLUMN follow_up_plan jsonb,
 ADD CONSTRAINT interview_question_source_union CHECK (
  (source_kind='CLAIM' AND claim_id IS NOT NULL AND note_source IS NULL AND follow_up_plan IS NULL AND jsonb_typeof(evidence)='array' AND jsonb_array_length(evidence)>0)
  OR (source_kind='NOTE_REVISION' AND claim_id IS NULL AND topic_id IS NULL AND jsonb_typeof(note_source)='object' AND note_source IS NOT NULL
    AND evidence='[]'::jsonb AND follow_up_plan IS NOT NULL AND jsonb_typeof(follow_up_plan)='array' AND jsonb_array_length(follow_up_plan)<=3)
 );
ALTER TABLE learning.learning_path_step
 ALTER COLUMN claim_id DROP NOT NULL,
 ALTER COLUMN source_version_id DROP NOT NULL,
 ALTER COLUMN source_span_id DROP NOT NULL,
 ALTER COLUMN evidence_hash DROP NOT NULL,
 ADD COLUMN source_kind text NOT NULL DEFAULT 'CLAIM',
 ADD COLUMN note_source jsonb,
 ADD CONSTRAINT learning_path_step_source_union CHECK (
  (source_kind='CLAIM' AND claim_id IS NOT NULL AND source_version_id IS NOT NULL AND source_span_id IS NOT NULL AND evidence_hash IS NOT NULL AND note_source IS NULL)
  OR (source_kind='NOTE_REVISION' AND claim_id IS NULL AND topic_id IS NULL AND source_version_id IS NULL AND source_span_id IS NULL AND evidence_hash IS NULL AND note_source IS NOT NULL AND jsonb_typeof(note_source)='object')
 );
CREATE OR REPLACE VIEW learning.interview_learning_path_step AS
 SELECT id,workspace_id,path_id,step_no,claim_id,topic_id,source_version_id,source_span_id,evidence_hash,title,rationale,status,version,created_at,updated_at,source_kind,note_source
 FROM learning.learning_path_step step WHERE EXISTS (SELECT 1 FROM learning.learning_path path WHERE path.workspace_id=step.workspace_id AND path.id=step.path_id AND path.origin_type='INTERVIEW')
 WITH LOCAL CHECK OPTION;

-- The original formal Claim verifiers remain unchanged and are still required
-- for every CLAIM row. A separate note verifier handles the other union branch.
DROP TRIGGER trg_learning_interview_question_evidence ON learning.interview_question;
CREATE TRIGGER trg_learning_interview_question_evidence BEFORE INSERT OR UPDATE OF workspace_id,session_id,parent_question_id,follow_up_no,claim_id,evidence,source_kind
 ON learning.interview_question FOR EACH ROW WHEN (NEW.source_kind='CLAIM') EXECUTE FUNCTION learning.assert_interview_question_evidence();
DROP TRIGGER trg_learning_interview_path_step_evidence ON learning.learning_path_step;
CREATE TRIGGER trg_learning_interview_path_step_evidence BEFORE INSERT OR UPDATE OF workspace_id,claim_id,source_version_id,source_span_id,evidence_hash,source_kind
 ON learning.learning_path_step FOR EACH ROW WHEN (NEW.source_kind='CLAIM') EXECUTE FUNCTION learning.assert_interview_path_step_evidence();

CREATE FUNCTION learning.note_interview_item_sources(item jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE item->>'kind'
  WHEN 'FACT' THEN item->'fact'->'sources'
  WHEN 'CONFLICT' THEN COALESCE((SELECT jsonb_agg(source ORDER BY alternative_no,source_no) FROM jsonb_array_elements(item->'conflict'->'alternatives') WITH ORDINALITY a(alternative,alternative_no)
    CROSS JOIN LATERAL jsonb_array_elements(alternative->'sources') WITH ORDINALITY s(source,source_no)),'[]'::jsonb)
  WHEN 'GAP' THEN COALESCE(item->'gap'->'sources','[]'::jsonb)||COALESCE(item->'gap'->'resolution'->'sources','[]'::jsonb)
 END
$$;
CREATE FUNCTION learning.note_interview_item_points(item jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 WITH statements AS (
  SELECT 1::bigint n,item->'fact' statement WHERE item->>'kind'='FACT'
  UNION ALL SELECT n,statement FROM jsonb_array_elements(CASE WHEN item->>'kind'='CONFLICT' THEN item->'conflict'->'alternatives' ELSE '[]'::jsonb END) WITH ORDINALITY a(statement,n)
  UNION ALL SELECT 1,item->'gap'->'resolution' WHERE item->>'kind'='GAP' AND item->'gap'->'resolution'<>'null'::jsonb
 ), points AS (
  SELECT n*2 n,statement->>'text' value FROM statements
  UNION ALL SELECT n*2+1,statement->>'applicability' FROM statements WHERE statement->>'applicability'<>''
  UNION ALL SELECT 1,'Insufficient evidence; 不足以判断，需要补充资料。' WHERE item->>'kind'='GAP' AND item->'gap'->'resolution'='null'::jsonb
 ), unique_points AS (SELECT value,min(n) n FROM points GROUP BY value)
 SELECT jsonb_agg(value ORDER BY n) FROM unique_points
$$;
CREATE FUNCTION learning.guard_note_interview_question() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p learning.note_interview_preparation; item jsonb; item_no integer; plan jsonb; points jsonb; expected_plan jsonb; ref jsonb; parent learning.interview_question;
BEGIN
 IF TG_OP='UPDATE' AND ROW(NEW.source_kind,NEW.note_source,NEW.follow_up_plan) IS DISTINCT FROM ROW(OLD.source_kind,OLD.note_source,OLD.follow_up_plan) THEN
  RAISE EXCEPTION 'interview question source and model plan are immutable' USING ERRCODE='55000';
 END IF;
 IF NEW.source_kind<>'NOTE_REVISION' THEN
  IF EXISTS (SELECT 1 FROM learning.review_session s WHERE s.id=NEW.session_id AND s.workspace_id=NEW.workspace_id AND s.config->'scope' ? 'note_revision') THEN RAISE EXCEPTION 'note interview cannot contain Claim questions' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO p FROM learning.note_interview_preparation WHERE session_id=NEW.session_id AND workspace_id=NEW.workspace_id AND status='READY';
 IF p.id IS NULL THEN RAISE EXCEPTION 'note question has no ready preparation' USING ERRCODE='23514'; END IF;
 ref:=p.snapshot-'items'-'renderer_version';
 IF NOT EXISTS(SELECT 1 FROM learning.review_session s WHERE s.id=NEW.session_id AND s.workspace_id=NEW.workspace_id AND s.config->'scope'=jsonb_build_object('note_revision',ref)
  AND s.config-'scope'-'schema_version'=p.options) THEN RAISE EXCEPTION 'note question scope differs from preparation' USING ERRCODE='23514'; END IF;
 SELECT value,ordinality::integer INTO item,item_no FROM jsonb_array_elements(p.snapshot->'items') WITH ORDINALITY WHERE value->>'id'=NEW.note_source->>'item_id';
 IF item IS NULL OR NEW.note_source IS DISTINCT FROM jsonb_build_object('revision',ref,'item_id',item->'id','item_kind',item->'kind','sources',learning.note_interview_item_sources(item)) THEN
  RAISE EXCEPTION 'note question source differs from frozen material' USING ERRCODE='23514'; END IF;
 points:=learning.note_interview_item_points(item);
 plan:=convert_from(p.model_output,'UTF8')::jsonb->'questions'->(NEW.question_no-1);
 SELECT COALESCE(jsonb_agg(jsonb_build_object('condition',f->'condition','prompt',f->'prompt','answer_points',points) ORDER BY n),'[]'::jsonb) INTO expected_plan
  FROM jsonb_array_elements(plan->'follow_ups') WITH ORDINALITY q(f,n);
 IF plan IS NULL OR plan->>'item_label' IS DISTINCT FROM 'I'||lpad(item_no::text,3,'0') OR NEW.answer_points IS DISTINCT FROM points OR NEW.follow_up_plan IS DISTINCT FROM expected_plan THEN
  RAISE EXCEPTION 'note question differs from accepted model plan' USING ERRCODE='23514'; END IF;
 IF NEW.follow_up_no=0 THEN
  IF NEW.prompt IS DISTINCT FROM plan->>'prompt' THEN RAISE EXCEPTION 'note question prompt differs from model response' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT * INTO parent FROM learning.interview_question WHERE id=NEW.parent_question_id AND workspace_id=NEW.workspace_id AND session_id=NEW.session_id;
  IF parent.id IS NULL OR parent.source_kind<>'NOTE_REVISION' OR parent.note_source IS DISTINCT FROM NEW.note_source OR parent.question_no<>NEW.question_no OR parent.follow_up_no+1<>NEW.follow_up_no
   OR NEW.prompt IS DISTINCT FROM (plan->'follow_ups'->(NEW.follow_up_no-1)->>'prompt') THEN RAISE EXCEPTION 'note follow-up does not extend its frozen question chain' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER trg_note_interview_question BEFORE INSERT OR UPDATE ON learning.interview_question FOR EACH ROW EXECUTE FUNCTION learning.guard_note_interview_question();

CREATE FUNCTION learning.guard_note_interview_step() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND ROW(NEW.source_kind,NEW.note_source) IS DISTINCT FROM ROW(OLD.source_kind,OLD.note_source) THEN RAISE EXCEPTION 'learning step source is immutable' USING ERRCODE='55000'; END IF;
 IF NEW.source_kind='NOTE_REVISION' AND NOT EXISTS (SELECT 1 FROM learning.learning_path p JOIN learning.interview_question q ON q.workspace_id=p.workspace_id AND q.session_id=p.interview_session_id
  WHERE p.id=NEW.path_id AND p.workspace_id=NEW.workspace_id AND p.origin_type='INTERVIEW' AND q.source_kind='NOTE_REVISION' AND q.note_source=NEW.note_source) THEN
  RAISE EXCEPTION 'note learning step has no exact interview source' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER trg_note_interview_step BEFORE INSERT OR UPDATE ON learning.learning_path_step FOR EACH ROW EXECUTE FUNCTION learning.guard_note_interview_step();

CREATE FUNCTION learning.guard_note_interview_turn() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE q learning.interview_question;
BEGIN
 SELECT * INTO q FROM learning.interview_question WHERE id=NEW.question_id AND session_id=NEW.session_id AND workspace_id=NEW.workspace_id;
 IF q.source_kind='NOTE_REVISION' THEN
  IF NEW.score->'note_source' IS DISTINCT FROM q.note_source OR NEW.score->'evidence' IS DISTINCT FROM '[]'::jsonb THEN RAISE EXCEPTION 'note turn source differs from question' USING ERRCODE='23514'; END IF;
 ELSIF NEW.score ? 'note_source' THEN RAISE EXCEPTION 'Claim turn cannot carry a note source' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER trg_note_interview_turn BEFORE INSERT ON learning.interview_turn FOR EACH ROW EXECUTE FUNCTION learning.guard_note_interview_turn();

ALTER TABLE agent.model_run DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (final_result_type IS NULL OR final_result_type IN (
        'relation_assessment','rag_answer','refusal','faithfulness_review','tool_request','clarification',
        'artifact_section','document_knowledge_profile','organizing_outline','organizing_document',
        'workspace_analysis_plan','workspace_analysis_answer','synthesis_delta','synthesis_semantic_review','synthesis_note_interview_plan'));

ALTER TABLE agent.model_run DROP CONSTRAINT IF EXISTS agent_model_run_retrieval_binding,
    ADD CONSTRAINT agent_model_run_retrieval_binding CHECK (
        retrieval_index_version_id IS NOT NULL OR (embedding_version_id IS NULL AND rerank_model_version IS NULL AND (
            (output_schema_id='agent.rag-answer' AND NOT (status='SUCCEEDED' AND final_result_type='rag_answer'))
            OR output_schema_id IN ('organizing.outline-generation','organizing.document-generation','agent.synthesis-delta','agent.synthesis-semantic-review','agent.synthesis-note-interview-plan'))));

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
               'agent.synthesis-delta','agent.synthesis-semantic-review','agent.synthesis-note-interview-plan'
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

CREATE FUNCTION learning.guard_note_interview_report() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected jsonb; finding jsonb; group_key text;
BEGIN
 SELECT jsonb_agg(note_source ORDER BY first_question) INTO expected FROM (
  SELECT note_source,min(question_no) first_question FROM learning.interview_question
  WHERE workspace_id=NEW.workspace_id AND session_id=NEW.session_id AND source_kind='NOTE_REVISION' AND follow_up_no=0 GROUP BY note_source
 ) source_items;
 IF expected IS NULL THEN
  IF NEW.report ? 'note_sources' THEN RAISE EXCEPTION 'Claim report cannot carry note sources' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF NEW.report->'note_sources' IS DISTINCT FROM expected OR NEW.report->'evidence' IS DISTINCT FROM '[]'::jsonb THEN
  RAISE EXCEPTION 'note report sources differ from its frozen questions' USING ERRCODE='23514'; END IF;
 FOREACH group_key IN ARRAY ARRAY['strengths','gaps','expression'] LOOP
  FOR finding IN SELECT value FROM jsonb_array_elements(CASE WHEN jsonb_typeof(NEW.report->group_key)='array' THEN NEW.report->group_key ELSE '[]'::jsonb END) LOOP
   IF finding->>'source_kind' IS DISTINCT FROM 'NOTE_REVISION' OR finding->>'claim_id' IS DISTINCT FROM '' OR finding->'evidence' IS DISTINCT FROM '[]'::jsonb
    OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(expected) source WHERE source=finding->'note_source') THEN
    RAISE EXCEPTION 'note report finding changed its original source' USING ERRCODE='23514'; END IF;
  END LOOP;
 END LOOP;
 RETURN NEW;
END $$;
CREATE TRIGGER trg_note_interview_report BEFORE INSERT ON learning.interview_report FOR EACH ROW EXECUTE FUNCTION learning.guard_note_interview_report();

-- Document-backed note reports use the existing Artifact v2 schema. Retain the
-- original v1 contract for Claim/Review and require the note's exact immutable
-- ArticleRevision for both its report and learning path.
CREATE OR REPLACE FUNCTION learning.assert_interview_artifact_binding()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 expected_type text; actual_type text; artifact_schema_version text; revision_schema_version text;
 current_revision_id uuid; current_artifact_version bigint; interview_id uuid; sections jsonb;
 preparation learning.note_interview_preparation;
BEGIN
 expected_type:=CASE TG_TABLE_NAME WHEN 'interview_report' THEN 'INTERVIEW_DOC' WHEN 'learning_path' THEN 'LEARNING_PATH' ELSE NULL END;
 SELECT artifact.artifact_type,artifact.domain_schema_version,artifact.current_revision_id,artifact.version,revision.domain_schema_version,revision.sections
  INTO actual_type,artifact_schema_version,current_revision_id,current_artifact_version,revision_schema_version,sections
  FROM learning.artifact artifact JOIN learning.artifact_revision revision
   ON revision.id=NEW.artifact_revision_id AND revision.workspace_id=NEW.workspace_id AND revision.artifact_id=NEW.artifact_id
  WHERE artifact.id=NEW.artifact_id AND artifact.workspace_id=NEW.workspace_id;
 IF actual_type IS DISTINCT FROM expected_type OR artifact_schema_version IS DISTINCT FROM 'artifact/v1'
  OR current_revision_id IS DISTINCT FROM NEW.artifact_revision_id OR current_artifact_version IS DISTINCT FROM NEW.artifact_version THEN
  RAISE EXCEPTION 'learning path artifact binding is invalid' USING ERRCODE='23514';
 END IF;
 IF TG_TABLE_NAME='interview_report' THEN
  interview_id:=NEW.session_id;
 ELSIF TG_TABLE_NAME='learning_path' AND NEW.origin_type='INTERVIEW' THEN
  interview_id:=NEW.interview_session_id;
 END IF;
 SELECT * INTO preparation FROM learning.note_interview_preparation
  WHERE workspace_id=NEW.workspace_id AND session_id=interview_id AND status='READY';
 IF preparation.id IS NULL THEN
  IF revision_schema_version IS DISTINCT FROM 'artifact-revision/v1' THEN
   RAISE EXCEPTION 'Claim and Review artifacts require their original revision schema' USING ERRCODE='23514';
  END IF;
 ELSE
  IF revision_schema_version IS DISTINCT FROM 'artifact-revision/v2'
   OR (SELECT count(*) FROM learning.artifact_revision_document_source source WHERE source.workspace_id=NEW.workspace_id AND source.revision_id=NEW.artifact_revision_id AND source.artifact_id=NEW.artifact_id)<>1
   OR NOT EXISTS (SELECT 1 FROM learning.artifact_revision_document_source source
    WHERE source.workspace_id=NEW.workspace_id AND source.revision_id=NEW.artifact_revision_id AND source.artifact_id=NEW.artifact_id
     AND source.document_id=preparation.document_id AND source.article_revision_id=preparation.article_revision_id
     AND source.revision_no=(preparation.snapshot->>'article_revision_no')::bigint AND source.content_hash=preparation.snapshot->>'content_hash')
   OR EXISTS (SELECT 1 FROM jsonb_array_elements(sections) section WHERE COALESCE(section->'Citations','null'::jsonb) NOT IN ('[]'::jsonb,'null'::jsonb)) THEN
   RAISE EXCEPTION 'note interview artifact differs from its frozen document source' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
