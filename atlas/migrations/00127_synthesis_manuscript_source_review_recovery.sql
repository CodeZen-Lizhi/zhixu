-- Explicit recovery preserves the original model identity. New attempts are
-- append-only and each result names every evidence fact, including reused facts.
DO $$ DECLARE name text; BEGIN
 SELECT conname INTO name FROM pg_constraint WHERE conrelid='organizing.synthesis_manuscript_source_review'::regclass AND contype='u' AND array_length(conkey,1)=3;
 EXECUTE format('ALTER TABLE organizing.synthesis_manuscript_source_review DROP CONSTRAINT %I',name);
END $$;
ALTER TABLE organizing.synthesis_manuscript_source_review ADD COLUMN attempt_no integer NOT NULL DEFAULT 1 CHECK(attempt_no BETWEEN 1 AND 10), ADD COLUMN supersedes_id uuid;
ALTER TABLE organizing.synthesis_manuscript_source_review ADD FOREIGN KEY(supersedes_id,workspace_id) REFERENCES organizing.synthesis_manuscript_source_review(id,workspace_id), ADD UNIQUE(workspace_id,origin_processing_id,origin_workflow_run_id,attempt_no), ADD UNIQUE(supersedes_id);
CREATE UNIQUE INDEX synthesis_source_review_one_active ON organizing.synthesis_manuscript_source_review(workspace_id,origin_processing_id,origin_workflow_run_id) WHERE status IN ('PENDING','PREPARED','RUNNING','REVIEWED','RECOVERY_REQUIRED');
CREATE TABLE organizing.synthesis_manuscript_source_review_command(
 workspace_id uuid NOT NULL, idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128), operation text NOT NULL CHECK(operation IN ('RECHECK','RECOVER')),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),review_id uuid NOT NULL,result_review_id uuid NOT NULL,expected_version bigint NOT NULL CHECK(expected_version>0),created_at timestamptz NOT NULL,
 PRIMARY KEY(workspace_id,idempotency_key),FOREIGN KEY(review_id,workspace_id) REFERENCES organizing.synthesis_manuscript_source_review(id,workspace_id),FOREIGN KEY(result_review_id,workspace_id) REFERENCES organizing.synthesis_manuscript_source_review(id,workspace_id));
CREATE TABLE organizing.synthesis_manuscript_source_review_recovery(
 id uuid PRIMARY KEY,workspace_id uuid NOT NULL,review_id uuid NOT NULL,command_key text NOT NULL,workflow_run_id uuid NOT NULL UNIQUE REFERENCES workflow.run(id),created_at timestamptz NOT NULL,
 FOREIGN KEY(review_id,workspace_id) REFERENCES organizing.synthesis_manuscript_source_review(id,workspace_id), FOREIGN KEY(workspace_id,command_key) REFERENCES organizing.synthesis_manuscript_source_review_command(workspace_id,idempotency_key) DEFERRABLE INITIALLY DEFERRED);
CREATE TABLE organizing.synthesis_manuscript_source_review_recovery_receipt(
 review_id uuid PRIMARY KEY,workspace_id uuid NOT NULL,workflow_run_id uuid NOT NULL UNIQUE REFERENCES organizing.synthesis_manuscript_source_review_recovery(workflow_run_id),receipt_hash text NOT NULL CHECK(receipt_hash ~ '^[0-9a-f]{64}$'),created_at timestamptz NOT NULL,
 FOREIGN KEY(review_id,workspace_id) REFERENCES organizing.synthesis_manuscript_source_review(id,workspace_id));
CREATE FUNCTION organizing.source_review_recovery_live(w uuid,v uuid,r uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_recovery x JOIN workflow.run run ON run.id=x.workflow_run_id JOIN workflow.node_run n ON n.run_id=run.id JOIN workflow.node_attempt a ON a.node_run_id=n.id WHERE x.workspace_id=w AND x.review_id=v AND x.workflow_run_id=r AND run.status='running' AND run.cancel_requested_at IS NULL AND run.pause_requested_at IS NULL AND n.node_key='organizing.synthesis-manuscript-source-review-recovery.apply' AND n.status='running' AND a.status='running' AND a.attempt_no=n.attempt AND a.lease_owner=n.lease_owner AND a.lease_until=n.lease_until AND a.lease_until>clock_timestamp() AND x.created_at>clock_timestamp()-interval '30 minutes')
$$;
CREATE TABLE organizing.synthesis_manuscript_source_review_result(
 workspace_id uuid NOT NULL,review_id uuid NOT NULL,obligation text NOT NULL,paragraph text NOT NULL,evidence_id uuid NOT NULL REFERENCES organizing.synthesis_manuscript_source_evidence(id),proof_review_id uuid NOT NULL,
 PRIMARY KEY(review_id,obligation,paragraph),FOREIGN KEY(review_id,workspace_id) REFERENCES organizing.synthesis_manuscript_source_review(id,workspace_id),FOREIGN KEY(proof_review_id,workspace_id) REFERENCES organizing.synthesis_manuscript_source_review(id,workspace_id));
INSERT INTO organizing.synthesis_manuscript_source_review_result(workspace_id,review_id,obligation,paragraph,evidence_id,proof_review_id) SELECT workspace_id,review_id,obligation,paragraph,id,review_id FROM organizing.synthesis_manuscript_source_evidence;
CREATE OR REPLACE FUNCTION organizing.guard_synthesis_source_review() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE document jsonb;target jsonb;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'source review history is immutable' USING ERRCODE='55000'; END IF;
 IF TG_OP='INSERT' THEN
  PERFORM 1 FROM organizing.synthesis_processing WHERE id=NEW.origin_processing_id AND workspace_id=NEW.workspace_id FOR UPDATE;
  IF NEW.status<>'PENDING' OR NEW.version<>1 OR NEW.workflow_run_id IS NOT NULL OR NEW.snapshot IS NOT NULL OR NEW.model_run_id IS NOT NULL OR NEW.output IS NOT NULL OR NEW.receipt_hash IS NOT NULL OR NEW.completed_at IS NOT NULL
  OR NOT EXISTS(SELECT 1 FROM organizing.synthesis_processing p JOIN workflow.run r ON r.id=p.workflow_run_id
    WHERE p.workspace_id=NEW.workspace_id AND p.id=NEW.origin_processing_id AND p.workflow_run_id=NEW.origin_workflow_run_id AND p.status='RECOVERY_REQUIRED' AND p.error_code='SYNTHESIS_MANUSCRIPT_SOURCE_REVIEW_REQUIRED' AND r.status='failed') THEN
   RAISE EXCEPTION 'source review requires exact failed source-only origin' USING ERRCODE='23514'; END IF;
  IF EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_recovery x JOIN workflow.run r ON r.id=x.workflow_run_id WHERE x.review_id=NEW.supersedes_id AND r.status NOT IN ('succeeded','failed','cancelled')) THEN RAISE EXCEPTION 'successor cannot race active recovery' USING ERRCODE='23514'; END IF;
  IF NEW.attempt_no=1 AND NEW.supersedes_id IS NOT NULL THEN RAISE EXCEPTION 'initial review predecessor invalid' USING ERRCODE='23514'; END IF;
  IF NEW.attempt_no>1 AND NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review p WHERE p.id=NEW.supersedes_id AND p.workspace_id=NEW.workspace_id AND p.origin_processing_id=NEW.origin_processing_id AND p.origin_workflow_run_id=NEW.origin_workflow_run_id AND p.attempt_no=NEW.attempt_no-1 AND (p.status IN ('STALE','REJECTED','SUCCEEDED') OR (p.status='FAILED' AND p.retryable))) THEN RAISE EXCEPTION 'successor predecessor invalid' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id OR NEW.origin_processing_id IS DISTINCT FROM OLD.origin_processing_id OR NEW.origin_workflow_run_id IS DISTINCT FROM OLD.origin_workflow_run_id OR NEW.attempt_no IS DISTINCT FROM OLD.attempt_no OR NEW.supersedes_id IS DISTINCT FROM OLD.supersedes_id OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
 OR (OLD.workflow_run_id IS NOT NULL AND NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id)
 OR (OLD.snapshot IS NOT NULL AND (NEW.snapshot IS DISTINCT FROM OLD.snapshot OR NEW.snapshot_hash IS DISTINCT FROM OLD.snapshot_hash))
 OR (OLD.model_run_id IS NOT NULL AND (NEW.model_run_id IS DISTINCT FROM OLD.model_run_id OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id OR NEW.node_attempt_id IS DISTINCT FROM OLD.node_attempt_id OR NEW.request_hash IS DISTINCT FROM OLD.request_hash))
 OR (OLD.output IS NOT NULL AND NEW.output IS DISTINCT FROM OLD.output)
 OR OLD.status IN ('SUCCEEDED','REJECTED','STALE','FAILED')
 OR NOT ((OLD.status='PENDING' AND NEW.status IN ('PENDING','PREPARED','FAILED','STALE')) OR (OLD.status='PREPARED' AND NEW.status IN ('RUNNING','FAILED','STALE')) OR (OLD.status='RUNNING' AND NEW.status IN ('REVIEWED','REJECTED','RECOVERY_REQUIRED','STALE','FAILED')) OR (OLD.status='REVIEWED' AND NEW.status IN ('SUCCEEDED','STALE')) OR (OLD.status='RECOVERY_REQUIRED' AND NEW.status='FAILED')) THEN
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
 IF NEW.status='PREPARED' AND OLD.status='PENDING' AND NEW.supersedes_id IS NOT NULL AND EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review p WHERE p.id=NEW.supersedes_id AND p.status<>'FAILED' AND p.snapshot=NEW.snapshot) THEN RAISE EXCEPTION 'unchanged baseline cannot draw another answer' USING ERRCODE='23514'; END IF;
 IF NEW.status='SUCCEEDED' AND NOT EXISTS(SELECT 1 FROM workflow.node_run n JOIN workflow.node_attempt a ON a.node_run_id=n.id WHERE n.run_id=NEW.workflow_run_id AND organizing.source_review_live(NEW.workspace_id,NEW.workflow_run_id,n.id,a.id,'organizing.synthesis-manuscript-source-review.apply')) AND NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_recovery x JOIN workflow.run r ON r.id=x.workflow_run_id JOIN workflow.node_run n ON n.run_id=r.id JOIN workflow.node_attempt a ON a.node_run_id=n.id WHERE x.workspace_id=NEW.workspace_id AND x.review_id=NEW.id AND r.status='running' AND r.cancel_requested_at IS NULL AND r.pause_requested_at IS NULL AND n.node_key='organizing.synthesis-manuscript-source-review-recovery.apply' AND n.status='running' AND a.status='running' AND a.attempt_no=n.attempt AND a.lease_owner=n.lease_owner AND a.lease_until=n.lease_until AND a.lease_until>clock_timestamp() AND x.created_at>clock_timestamp()-interval '30 minutes') THEN RAISE EXCEPTION 'source review apply requires real execution' USING ERRCODE='23514'; END IF;
 IF NEW.status='FAILED' AND OLD.model_run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM agent.model_run m WHERE m.id=OLD.model_run_id AND m.workspace_id=OLD.workspace_id AND m.workflow_run_id=OLD.workflow_run_id AND m.node_attempt_id=OLD.node_attempt_id AND m.status='FAILED') THEN RAISE EXCEPTION 'failed review requires failed model ledger' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE OR REPLACE FUNCTION organizing.verify_synthesis_source_review_closure() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE review organizing.synthesis_manuscript_source_review%ROWTYPE;model agent.model_run%ROWTYPE;document jsonb;checks jsonb;review_obligation jsonb;check_result jsonb;idx integer;total integer;last_call agent.model_call%ROWTYPE;
BEGIN
 IF TG_TABLE_NAME='synthesis_manuscript_source_review_recovery_receipt' THEN
  SELECT * INTO review FROM organizing.synthesis_manuscript_source_review WHERE id=NEW.review_id;
 ELSE
  SELECT * INTO review FROM organizing.synthesis_manuscript_source_review WHERE id=NEW.id;
 END IF;
 IF EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_recovery_receipt x WHERE x.review_id=review.id AND x.workspace_id=review.workspace_id) THEN
  review.status:='SUCCEEDED';SELECT receipt_hash INTO review.receipt_hash FROM organizing.synthesis_manuscript_source_review_recovery_receipt WHERE review_id=review.id;
 END IF;
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
  IF review.status='SUCCEEDED' AND (SELECT count(*) FROM organizing.synthesis_manuscript_source_review_result e WHERE e.review_id=review.id AND e.obligation=review_obligation->>'label')<>jsonb_array_length(check_result->'targets') THEN RAISE EXCEPTION 'source review evidence closure incomplete' USING ERRCODE='23514'; END IF;
 END LOOP;
 IF review.status='SUCCEEDED' AND review.receipt_hash IS NULL THEN RAISE EXCEPTION 'source review missing receipt' USING ERRCODE='23514'; END IF;
 IF review.status<>'SUCCEEDED' AND EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_evidence e WHERE e.review_id=review.id) THEN RAISE EXCEPTION 'uncompleted review cannot retain evidence' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END; $$;

CREATE FUNCTION organizing.guard_synthesis_source_review_recovery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- Serialize all attempts and successor commands on their shared origin.
 PERFORM 1 FROM organizing.synthesis_processing p JOIN organizing.synthesis_manuscript_source_review v ON v.origin_processing_id=p.id WHERE v.id=NEW.review_id FOR UPDATE OF p;
 IF EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_recovery x JOIN workflow.run r ON r.id=x.workflow_run_id WHERE x.review_id=NEW.review_id AND r.status NOT IN ('succeeded','failed','cancelled'))
 OR EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_recovery_receipt x WHERE x.review_id=NEW.review_id)
 OR EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review v WHERE v.supersedes_id=NEW.review_id) THEN RAISE EXCEPTION 'recovery already active, completed or superseded' USING ERRCODE='23514'; END IF;

 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review v JOIN workflow.run prior_run ON prior_run.id=v.workflow_run_id JOIN workflow.run r ON r.id=NEW.workflow_run_id JOIN workflow.definition d ON d.id=r.definition_id WHERE v.id=NEW.review_id AND v.workspace_id=NEW.workspace_id AND v.status IN ('REVIEWED','STALE') AND v.output IS NOT NULL AND prior_run.status IN ('succeeded','failed','cancelled') AND r.workspace_id=v.workspace_id AND r.input->>'review_id'=v.id::text AND d.key='organizing.synthesis-manuscript-source-review-recovery' AND d.version=1 AND r.status IN ('pending','running')) THEN RAISE EXCEPTION 'recovery requires accepted proof and independent execution' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_source_review_recovery_guard BEFORE INSERT ON organizing.synthesis_manuscript_source_review_recovery FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_source_review_recovery();
-- Protocol: SHA-256 of the exact target JSON token inside the immutable
-- snapshot bytes. json (NOT jsonb) preserves field order, escapes and spacing.
-- Existing 126 snapshots were encoded by the same Go encoder as each target;
-- this independently recomputes their digest without rewriting old evidence.
CREATE FUNCTION organizing.source_review_frozen_target_hash(snapshot bytea,note uuid) RETURNS text LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT encode(sha256(convert_to(t::text,'UTF8')),'hex')
 FROM json_array_elements(convert_from(snapshot,'UTF8')::json->'targets') t
 WHERE t->>'note_id'=note::text
$$;
CREATE FUNCTION organizing.verify_synthesis_source_review_result() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE review organizing.synthesis_manuscript_source_review%ROWTYPE;proof organizing.synthesis_manuscript_source_review%ROWTYPE;e organizing.synthesis_manuscript_source_evidence%ROWTYPE;target jsonb;obligation jsonb;part jsonb;check_result jsonb;proof_target jsonb;
BEGIN
 SELECT * INTO review FROM organizing.synthesis_manuscript_source_review WHERE id=NEW.review_id;
 SELECT * INTO proof FROM organizing.synthesis_manuscript_source_review WHERE id=NEW.proof_review_id;
 SELECT * INTO e FROM organizing.synthesis_manuscript_source_evidence WHERE id=NEW.evidence_id;
 SELECT o INTO obligation FROM jsonb_array_elements(convert_from(review.snapshot,'UTF8')::jsonb->'obligations') o WHERE o->>'label'=NEW.obligation;
 SELECT t INTO target FROM jsonb_array_elements(convert_from(review.snapshot,'UTF8')::jsonb->'targets') t WHERE t->>'label'=obligation->>'note';
 SELECT t INTO proof_target FROM jsonb_array_elements(convert_from(proof.snapshot,'UTF8')::jsonb->'targets') t WHERE t->>'note_id'=e.note_id::text;
 SELECT p INTO part FROM jsonb_array_elements(target->'paragraphs') p WHERE p->>'label'=NEW.paragraph;
 SELECT c INTO check_result FROM jsonb_array_elements(convert_from(review.output,'UTF8')::jsonb->'checks') c WHERE c->>'obligation'=NEW.obligation;
 IF (review.workspace_id=NEW.workspace_id AND proof.workspace_id=NEW.workspace_id AND e.workspace_id=NEW.workspace_id AND e.review_id=proof.id AND (review.status='SUCCEEDED' OR EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_recovery_receipt x WHERE x.review_id=review.id AND x.workspace_id=review.workspace_id)) AND (proof.status='SUCCEEDED' OR EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_recovery_receipt x WHERE x.review_id=proof.id AND x.workspace_id=proof.workspace_id)) AND target=proof_target AND e.target_hash=organizing.source_review_frozen_target_hash(proof.snapshot,e.note_id) AND e.paragraph=NEW.paragraph AND check_result->>'verdict'='SUPPORTED' AND check_result->'targets' ? NEW.paragraph AND e.full_content_hash=target->>'full_content_hash' AND e.start_byte=(part->>'start_byte')::integer AND e.end_byte=(part->>'end_byte')::integer AND e.paragraph_hash=part->>'hash' AND EXISTS(SELECT 1 FROM jsonb_array_elements(convert_from(proof.snapshot,'UTF8')::jsonb->'obligations') o WHERE o->>'label'=e.obligation AND o->'reference'=obligation->'reference')) IS NOT TRUE THEN RAISE EXCEPTION 'result does not close exact current and original proof' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END; $$;
CREATE CONSTRAINT TRIGGER synthesis_source_review_result_verify AFTER INSERT ON organizing.synthesis_manuscript_source_review_result DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_source_review_result();
DO $$ DECLARE table_name text; BEGIN
 FOREACH table_name IN ARRAY ARRAY['synthesis_manuscript_source_review_command','synthesis_manuscript_source_review_recovery','synthesis_manuscript_source_review_result','synthesis_manuscript_source_review_recovery_receipt'] LOOP
 EXECUTE format('CREATE TRIGGER immutable BEFORE UPDATE OR DELETE ON organizing.%I FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation()',table_name);
 EXECUTE format('CREATE TRIGGER no_truncate BEFORE TRUNCATE ON organizing.%I FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation()',table_name);
 END LOOP;
END $$;

CREATE FUNCTION organizing.guard_synthesis_source_review_command() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review prior_review JOIN organizing.synthesis_manuscript_source_review result ON result.id=NEW.result_review_id AND result.workspace_id=prior_review.workspace_id AND result.origin_processing_id=prior_review.origin_processing_id AND result.origin_workflow_run_id=prior_review.origin_workflow_run_id WHERE prior_review.id=NEW.review_id AND prior_review.workspace_id=NEW.workspace_id AND prior_review.version=NEW.expected_version AND (result.id=prior_review.id OR (NEW.operation='RECHECK' AND result.supersedes_id=prior_review.id))) THEN RAISE EXCEPTION 'command winner binding invalid' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_source_review_command_guard BEFORE INSERT ON organizing.synthesis_manuscript_source_review_command FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_source_review_command();

CREATE OR REPLACE FUNCTION organizing.validate_synthesis_source_review_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE review organizing.synthesis_manuscript_source_review%ROWTYPE; document jsonb;target jsonb;part jsonb;review_obligation jsonb;check_result jsonb;ref jsonb;
BEGIN
 SELECT * INTO review FROM organizing.synthesis_manuscript_source_review WHERE workspace_id=NEW.workspace_id AND id=NEW.review_id FOR UPDATE;
 document:=convert_from(review.snapshot,'UTF8')::jsonb;
 SELECT t INTO target FROM jsonb_array_elements(document->'targets') t WHERE t->>'note_id'=NEW.note_id::text AND t->>'base_revision_id'=NEW.base_revision_id::text;
 SELECT p INTO part FROM jsonb_array_elements(target->'paragraphs') p WHERE p->>'label'=NEW.paragraph;
 SELECT o INTO review_obligation FROM jsonb_array_elements(document->'obligations') o WHERE o->>'label'=NEW.obligation AND o->>'note'=target->>'label';ref:=review_obligation->'reference';
 SELECT c INTO check_result FROM jsonb_array_elements(convert_from(review.output,'UTF8')::jsonb->'checks') c WHERE c->>'obligation'=NEW.obligation;
 IF ((review.status='REVIEWED' OR (review.status='STALE' AND EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_recovery x WHERE x.review_id=review.id AND organizing.source_review_recovery_live(review.workspace_id,review.id,x.workflow_run_id)))) AND check_result->>'verdict'='SUPPORTED' AND check_result->>'source'=review_obligation->>'source' AND check_result->'targets' ? NEW.paragraph
 AND NEW.target_hash=organizing.source_review_frozen_target_hash(review.snapshot,NEW.note_id)
 AND NEW.full_content_hash=target->>'full_content_hash' AND NEW.start_byte=(part->>'start_byte')::integer AND NEW.end_byte=(part->>'end_byte')::integer
 AND NEW.paragraph_hash=part->>'hash' AND NEW.paragraph_hash=encode(sha256(substring(convert_to(target->>'full_content','UTF8') FROM NEW.start_byte+1 FOR NEW.end_byte-NEW.start_byte)),'hex')
 AND ref->'source'->>'workspace_id'=NEW.workspace_id::text AND ref->'source'->>'source_id'=NEW.source_id::text
 AND ref->'source'->>'source_version_id'=NEW.source_version_id::text AND ref->'source'->>'content_artifact_id'=NEW.content_artifact_id::text AND ref->'source'->>'parse_projection_id'=NEW.parse_projection_id::text
 AND ref->'source'->>'content_hash'=NEW.content_hash AND ref->>'source_span_id'=NEW.source_span_id::text AND ref->>'excerpt_hash'=NEW.excerpt_hash AND ref->>'title'=NEW.title
 ) IS NOT TRUE THEN RAISE EXCEPTION 'source evidence lacks exact accepted current-text obligation' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE OR REPLACE FUNCTION organizing.verify_synthesis_source_evidence_closure() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review r WHERE r.id=NEW.review_id AND r.workspace_id=NEW.workspace_id AND ((r.status='SUCCEEDED' AND r.receipt_hash IS NOT NULL) OR EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_recovery_receipt x WHERE x.review_id=r.id AND x.workspace_id=r.workspace_id))) THEN
  RAISE EXCEPTION 'source evidence requires atomic completed review receipt' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END; $$;
CREATE FUNCTION organizing.guard_synthesis_source_review_recovery_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT organizing.source_review_recovery_live(NEW.workspace_id,NEW.review_id,NEW.workflow_run_id) THEN RAISE EXCEPTION 'recovery receipt requires current execution' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_source_review_recovery_receipt_guard BEFORE INSERT ON organizing.synthesis_manuscript_source_review_recovery_receipt FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_source_review_recovery_receipt();
CREATE CONSTRAINT TRIGGER synthesis_source_review_recovery_receipt_closure AFTER INSERT ON organizing.synthesis_manuscript_source_review_recovery_receipt DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_source_review_closure();

CREATE FUNCTION organizing.verify_synthesis_source_review_successor_command() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.supersedes_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_source_review_command c WHERE c.workspace_id=NEW.workspace_id AND c.review_id=NEW.supersedes_id AND c.result_review_id=NEW.id AND c.operation='RECHECK') THEN RAISE EXCEPTION 'successor requires atomic explicit command' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END; $$;
CREATE CONSTRAINT TRIGGER synthesis_source_review_successor_command AFTER INSERT ON organizing.synthesis_manuscript_source_review DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_source_review_successor_command();

-- Close only this owner's abandoned calls. An expired real attempt is not a
-- provider failure; the immutable result remains UNKNOWN and cannot be rerun.
CREATE FUNCTION organizing.close_synthesis_source_review_abandoned_calls(w uuid,v uuid) RETURNS void LANGUAGE plpgsql AS $$
DECLARE review organizing.synthesis_manuscript_source_review%ROWTYPE;ended timestamptz;
BEGIN
 SELECT * INTO review FROM organizing.synthesis_manuscript_source_review WHERE workspace_id=w AND id=v FOR UPDATE;
 IF review.id IS NULL OR review.model_run_id IS NULL OR NOT EXISTS(SELECT 1 FROM workflow.run r JOIN workflow.node_attempt a ON a.id=review.node_attempt_id JOIN workflow.node_run n ON n.id=a.node_run_id AND n.run_id=r.id WHERE r.id=review.workflow_run_id AND r.workspace_id=w AND (r.status IN ('failed','cancelled','succeeded') OR r.cancel_requested_at IS NOT NULL OR a.status<>'running' OR a.lease_until<=clock_timestamp() OR review.created_at<clock_timestamp()-interval '30 minutes')) THEN RETURN; END IF;
 PERFORM 1 FROM agent.model_run m WHERE m.id=review.model_run_id AND m.workspace_id=w AND m.workflow_run_id=review.workflow_run_id AND m.node_attempt_id=review.node_attempt_id AND m.status='RUNNING' FOR UPDATE;
 IF NOT FOUND THEN RETURN; END IF;
 ended:=clock_timestamp();
 UPDATE agent.model_call SET status='UNKNOWN',error_code='SYNTHESIS_SOURCE_REVIEW_RECOVERY_REQUIRED',version=version+1,completed_at=GREATEST(ended,started_at),latency_ms=LEAST(600000,GREATEST(0,(extract(epoch FROM ended-started_at)*1000)::bigint))
 WHERE model_run_id=review.model_run_id AND status='STARTED';
END; $$;
