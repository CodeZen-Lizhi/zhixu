-- Forward-only owner ledger. No Workflow transitions or publication grants.
CREATE TABLE organizing.synthesis_manuscript_review_decision (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
 attempt_id uuid NOT NULL,
 sequence integer NOT NULL CHECK(sequence BETWEEN 1 AND 2),
 idempotency_key text NOT NULL CHECK(idempotency_key=btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 200),
 payload bytea NOT NULL CHECK(octet_length(payload) BETWEEN 1 AND 16777216),
 payload_hash text NOT NULL CHECK(payload_hash=encode(sha256(payload),'hex')),
 UNIQUE(id,workspace_id), UNIQUE(workspace_id,idempotency_key), UNIQUE(attempt_id,sequence),
 FOREIGN KEY(attempt_id,workspace_id) REFERENCES organizing.synthesis_manuscript_attempt(id,workspace_id) ON DELETE RESTRICT
);
CREATE FUNCTION organizing.validate_synthesis_manuscript_review_decision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; c jsonb; b jsonb; a jsonb; previous jsonb; preview jsonb; decisions jsonb;
BEGIN
 v:=convert_from(NEW.payload,'UTF8')::jsonb; c:=v->'command'; b:=c->'binding';
 SELECT convert_from(payload,'UTF8')::jsonb INTO a FROM organizing.synthesis_manuscript_attempt WHERE id=NEW.attempt_id AND workspace_id=NEW.workspace_id FOR UPDATE;
 IF NEW.sequence=1 THEN previous:=NULL; preview:=a->'preview'; decisions:='[]'::jsonb;
 ELSE
  SELECT convert_from(payload,'UTF8')::jsonb INTO previous FROM organizing.synthesis_manuscript_review_decision WHERE attempt_id=NEW.attempt_id AND workspace_id=NEW.workspace_id AND sequence=1;
  preview:=previous->'result'->'preview'; decisions:=previous->'result'->'resolutions';
 END IF;
 IF (v->>'id'=NEW.id::text AND (v->>'sequence')::integer=NEW.sequence
  AND c->>'attempt_id'=NEW.attempt_id::text AND c->>'capture_id'=a->>'capture_id'
  AND c->>'note_id'=a->'command'->>'note_id' AND c->>'idempotency_key'=NEW.idempotency_key
  AND b->>'workspace_id'=NEW.workspace_id::text AND b->>'processing_id'=a->'command'->>'processing_id'
  AND b->>'run_id'=a->'prepared'->'generation_input'->>'WorkflowRunID'
  AND v->>'previous_hash'=coalesce(previous->>'hash','')
  AND (NEW.sequence=1 OR b=previous->'command'->'binding')
  AND jsonb_typeof(preview->'review')='object'
  AND c->'resolution'->>'stage'=preview->'review'->>'stage'
  AND c->'resolution'->>'preview_fingerprint'=preview->>'fingerprint'
  AND c->'resolution'->'acknowledged_ordinals'=(SELECT jsonb_agg(x->'Ordinal' ORDER BY ord) FROM jsonb_array_elements(preview->'review'->'conflicts') WITH ORDINALITY e(x,ord))
  AND jsonb_array_length(c->'resolution'->'acknowledged_ordinals') BETWEEN 1 AND 1024
  AND octet_length(c->'resolution'->>'final_content')<=1048576
  AND v->'result'->'resolutions'=decisions||jsonb_build_array(c->'resolution')
  AND v->>'hash' ~ '^[0-9a-f]{64}$' AND v->'result'->>'hash' ~ '^[0-9a-f]{64}$'
  AND ((jsonb_typeof(v->'result'->'preview'->'manuscript')='object' AND NOT (v->'result'->'preview' ? 'review')) OR (jsonb_typeof(v->'result'->'preview'->'review')='object' AND NOT (v->'result'->'preview' ? 'manuscript')))
 ) IS NOT TRUE THEN RAISE EXCEPTION 'invalid manuscript stage decision binding' USING ERRCODE='23514'; END IF;
 -- A reserved UUID alone never authorizes a decision. The application additionally
 -- composes current CallerCapabilities/Root/source/model owner authority.
 PERFORM 1 FROM workflow.human_task h JOIN workflow.run r ON r.id=h.run_id
 JOIN workflow.node_run n ON n.id=h.node_run_id AND n.run_id=r.id
 WHERE h.id=(b->>'human_task_id')::uuid AND h.run_id=(b->>'run_id')::uuid
 AND h.node_run_id=(b->>'node_run_id')::uuid AND r.workspace_id=NEW.workspace_id
 AND h.target_version=(b->>'target_version')::bigint AND h.status='pending'
 AND (h.expires_at IS NULL OR h.expires_at>CURRENT_TIMESTAMP)
 AND n.status='waiting_for_human' FOR SHARE OF h;
 IF NOT FOUND THEN RAISE EXCEPTION 'manuscript review lacks actual pending human task' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_manuscript_review_decision_validate BEFORE INSERT ON organizing.synthesis_manuscript_review_decision FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_manuscript_review_decision();
CREATE TRIGGER synthesis_manuscript_review_decision_immutable BEFORE UPDATE OR DELETE ON organizing.synthesis_manuscript_review_decision FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_manuscript_review_decision_no_truncate BEFORE TRUNCATE ON organizing.synthesis_manuscript_review_decision FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION organizing.validate_synthesis_manuscript_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; a jsonb; preview jsonb; decision jsonb; ledger jsonb; last_decision jsonb; total integer;
BEGIN
 v:=convert_from(NEW.payload,'UTF8')::jsonb;
 SELECT convert_from(payload,'UTF8')::jsonb INTO a FROM organizing.synthesis_manuscript_attempt WHERE id=NEW.attempt_id AND workspace_id=NEW.workspace_id;
 preview:=a->'preview';
 IF v ? 'review' THEN
  ledger:=v->'review'->'decisions';
  IF (v->'review'->>'version'='synthesis-manuscript-review/v1' AND jsonb_typeof(ledger)='array' AND jsonb_array_length(ledger) BETWEEN 1 AND 2) IS NOT TRUE THEN RAISE EXCEPTION 'invalid receipt review identity' USING ERRCODE='23514'; END IF;
  SELECT count(*) INTO total FROM organizing.synthesis_manuscript_review_decision WHERE attempt_id=NEW.attempt_id AND workspace_id=NEW.workspace_id;
  IF total<>jsonb_array_length(ledger) THEN RAISE EXCEPTION 'receipt omits stage ledger' USING ERRCODE='23514'; END IF;
  IF ledger IS DISTINCT FROM (SELECT jsonb_agg(convert_from(d.payload,'UTF8')::jsonb ORDER BY sequence) FROM organizing.synthesis_manuscript_review_decision d WHERE d.workspace_id=NEW.workspace_id AND d.attempt_id=NEW.attempt_id) THEN
   RAISE EXCEPTION 'receipt differs from ordered immutable ledger' USING ERRCODE='23514';
  END IF;
  last_decision:=ledger->(jsonb_array_length(ledger)-1); preview:=last_decision->'result'->'preview';
 END IF;
 IF (v->>'id'=NEW.id::text AND v->>'workspace_id'=NEW.workspace_id::text AND v->>'attempt_id'=NEW.attempt_id::text
     AND v->>'attempt_hash'=a->>'hash' AND NOT (preview ? 'review')
     AND jsonb_typeof(preview->'manuscript')='object'
     AND v->'manuscript'=preview->'manuscript'
     AND v->'manuscript'->>'version'='synthesis-manuscript/v2'
     AND v->'manuscript'->>'content_hash'=encode(sha256(convert_to(v->'manuscript'->>'full_content','UTF8')),'hex')
     AND v->>'hash' ~ '^[0-9a-f]{64}$') IS NOT TRUE THEN
  RAISE EXCEPTION 'receipt lacks exact completed manuscript attempt' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END; $$;
-- Existing 119 revision, apply, source, model and Article closure stays intact.

CREATE FUNCTION organizing.verify_synthesis_manuscript_review_closure() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb;
BEGIN
 v:=convert_from(NEW.payload,'UTF8')::jsonb;
 IF jsonb_typeof(v->'result'->'preview'->'manuscript')='object' AND NOT EXISTS(
  SELECT 1 FROM organizing.synthesis_manuscript_receipt r WHERE r.workspace_id=NEW.workspace_id AND r.attempt_id=NEW.attempt_id
  AND convert_from(r.payload,'UTF8')::jsonb->'review'->'decisions'->(NEW.sequence-1)=v
 ) THEN RAISE EXCEPTION 'completed review lacks atomic receipt' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END; $$;
CREATE CONSTRAINT TRIGGER synthesis_manuscript_review_closure AFTER INSERT ON organizing.synthesis_manuscript_review_decision DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_manuscript_review_closure();
