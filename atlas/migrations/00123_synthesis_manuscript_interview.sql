-- Freeze the complete v2 manuscript while selecting only its trusted items.
-- The explicit revision identity is unchanged for historical v1 sessions.

CREATE OR REPLACE FUNCTION learning.guard_note_interview_preparation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected jsonb; run_status text;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'note interview preparation is retained history' USING ERRCODE='55000'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.status<>'QUEUED' OR NEW.version<>1 THEN RAISE EXCEPTION 'note interview preparation must start queued' USING ERRCODE='23514'; END IF;
  SELECT jsonb_build_object('workspace_id',r.workspace_id,'note_id',r.note_id,'revision_id',r.id,'revision_no',r.revision_no,
   'document_id',r.document_id,'article_revision_id',r.article_revision_id,'article_revision_no',r.article_revision_no,
   'content_hash',r.content_hash,'projection_hash',r.projection_hash,'title',r.title,'renderer_version',r.renderer_version,'items',r.items)
   || CASE WHEN r.renderer_version='synthesis-markdown/v2' THEN jsonb_build_object('manuscript',r.manuscript) ELSE '{}'::jsonb END
   INTO expected FROM organizing.synthesis_revision r WHERE r.id=NEW.revision_id AND r.workspace_id=NEW.workspace_id;
  IF expected IS NULL OR NEW.snapshot IS DISTINCT FROM expected THEN RAISE EXCEPTION 'note interview snapshot differs from immutable revision' USING ERRCODE='23514'; END IF;
  IF jsonb_array_length(expected->'items')=0 THEN
   RAISE EXCEPTION 'note interview has no trusted material available' USING ERRCODE='23514';
  END IF;
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

CREATE OR REPLACE FUNCTION learning.guard_note_interview_question() RETURNS trigger LANGUAGE plpgsql AS $$
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
 ref:=jsonb_build_object('workspace_id',p.snapshot->'workspace_id',
   'note_id',p.snapshot->'note_id',
   'revision_id',p.snapshot->'revision_id',
   'revision_no',p.snapshot->'revision_no',
   'document_id',p.snapshot->'document_id',
   'article_revision_id',p.snapshot->'article_revision_id',
   'article_revision_no',p.snapshot->'article_revision_no',
   'content_hash',p.snapshot->'content_hash',
   'projection_hash',p.snapshot->'projection_hash',
   'title',p.snapshot->'title');
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
