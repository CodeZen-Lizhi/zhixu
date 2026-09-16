-- Preparation-only storage. No workflow graph or publication authorization is
-- changed. Goldmark/model/source proofs remain trusted scoped owner operations.
CREATE TABLE organizing.synthesis_manuscript_capture (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
 payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 1500000),
 payload_hash text NOT NULL CHECK (payload_hash=encode(sha256(payload),'hex')),
 UNIQUE(id,workspace_id)
);
CREATE TABLE organizing.synthesis_manuscript_attempt (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
 capture_id uuid NOT NULL,
 idempotency_key text NOT NULL CHECK (idempotency_key=btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 200),
 payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 16777216),
 payload_hash text NOT NULL CHECK (payload_hash=encode(sha256(payload),'hex')),
 UNIQUE(id,workspace_id), UNIQUE(workspace_id,idempotency_key),
 FOREIGN KEY(capture_id,workspace_id) REFERENCES organizing.synthesis_manuscript_capture(id,workspace_id) ON DELETE RESTRICT
);
CREATE TABLE organizing.synthesis_manuscript_receipt (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
 attempt_id uuid NOT NULL,
 payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 16777216),
 payload_hash text NOT NULL CHECK (payload_hash=encode(sha256(payload),'hex')),
 UNIQUE(id,workspace_id), UNIQUE(attempt_id,workspace_id),
 FOREIGN KEY(attempt_id,workspace_id) REFERENCES organizing.synthesis_manuscript_attempt(id,workspace_id) ON DELETE RESTRICT
);

CREATE FUNCTION organizing.validate_synthesis_manuscript_capture() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; content bytea; absent text;
BEGIN
 v:=convert_from(NEW.payload,'UTF8')::jsonb;
 content:=decode(v->>'bytes','base64');
 IF (v->>'id'=NEW.id::text AND v->>'workspace_id'=NEW.workspace_id::text
     AND jsonb_typeof(v->'exists')='boolean' AND octet_length(content)<=1048576
     AND v->>'root_grant_id' ~ '^[0-9a-f-]{36}$' AND v->>'root_fingerprint' ~ '^[0-9a-f]{64}$'
     AND (v->>'workspace_binding_version')::bigint>0
     AND v->>'target_path'<>'' AND v->>'target_path' !~ '(^/|(^|/)\.\.(/|$))'
     AND (v->>'created_at')::timestamptz IS NOT NULL) IS NOT TRUE THEN
  RAISE EXCEPTION 'invalid manuscript capture identity' USING ERRCODE='23514';
 END IF;
 -- convert_from also rejects invalid UTF-8 and zero bytes, including base64 data.
 PERFORM convert_from(content,'UTF8');
 IF (v->>'exists')::boolean THEN
  IF (v->>'content_hash'=encode(sha256(content),'hex') AND v->>'absence_token'='') IS NOT TRUE THEN
   RAISE EXCEPTION 'invalid manuscript capture hash' USING ERRCODE='23514';
  END IF;
 ELSE
  IF (octet_length(content)=0 AND v->>'content_hash'='' AND v->>'absence_token' ~ '^workspace-target-absent/v1:[0-9a-f]{64}$') IS NOT TRUE THEN
   RAISE EXCEPTION 'invalid manuscript absence capture' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_manuscript_capture_validate BEFORE INSERT ON organizing.synthesis_manuscript_capture
 FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_manuscript_capture();

CREATE FUNCTION organizing.validate_synthesis_manuscript_attempt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; c jsonb; p jsonb; i jsonb; l jsonb;
BEGIN
 v:=convert_from(NEW.payload,'UTF8')::jsonb;
 SELECT convert_from(payload,'UTF8')::jsonb INTO c FROM organizing.synthesis_manuscript_capture WHERE id=NEW.capture_id AND workspace_id=NEW.workspace_id;
 p:=v->'prepared'; i:=p->'merge_input'; l:=i->'latest';
 IF (v->>'id'=NEW.id::text AND v->'command'->>'workspace_id'=NEW.workspace_id::text
     AND v->'command'->>'idempotency_key'=NEW.idempotency_key AND v->>'capture_id'=NEW.capture_id::text
     AND v->'command'->>'note_id'=l->>'note_id' AND l->>'workspace_id'=NEW.workspace_id::text
     AND v->'command'->>'processing_id'=p->'generation_input'->>'ProcessingID'
     AND i->>'file_content'=convert_from(decode(c->>'bytes','base64'),'UTF8') AND i->'file_exists'=c->'exists'
     AND p->'authority'->>'target_path'=c->>'target_path'
     AND p->'authority'->>'root_grant_id'=c->>'root_grant_id'
     AND p->'authority'->>'root_fingerprint'=c->>'root_fingerprint'
     AND p->'authority'->'workspace_binding_version'=c->'workspace_binding_version'
     AND p->'authority'->>'document_id'=l->>'document_id'
     AND p->'authority'->>'latest_article_id'=l->>'article_revision_id'
     AND v->'preview'->>'version'='synthesis-manuscript-merge/v1'
     AND v->'preview'->>'fingerprint' ~ '^[0-9a-f]{64}$' AND v->>'hash' ~ '^[0-9a-f]{64}$') IS NOT TRUE THEN
  RAISE EXCEPTION 'invalid manuscript attempt binding' USING ERRCODE='23514';
 END IF;
 -- Existing immutable L must match the stored identities. Currentness and
 -- publication/model/source/grant fences are rechecked by the scoped owner.
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_revision r WHERE r.id=(l->>'id')::uuid
  AND r.workspace_id=NEW.workspace_id AND r.note_id=(l->>'note_id')::uuid
  AND r.document_id=(l->>'document_id')::uuid AND r.article_revision_id=(l->>'article_revision_id')::uuid
  AND r.projection_hash=l->>'hash' AND r.content_hash=l->>'content_hash') THEN
  RAISE EXCEPTION 'manuscript latest revision is not stored' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_manuscript_attempt_validate BEFORE INSERT ON organizing.synthesis_manuscript_attempt
 FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_manuscript_attempt();

CREATE FUNCTION organizing.validate_synthesis_manuscript_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; a jsonb;
BEGIN
 v:=convert_from(NEW.payload,'UTF8')::jsonb;
 SELECT convert_from(payload,'UTF8')::jsonb INTO a FROM organizing.synthesis_manuscript_attempt WHERE id=NEW.attempt_id AND workspace_id=NEW.workspace_id;
 IF (v->>'id'=NEW.id::text AND v->>'workspace_id'=NEW.workspace_id::text AND v->>'attempt_id'=NEW.attempt_id::text
     AND v->>'attempt_hash'=a->>'hash' AND NOT (a->'preview' ? 'review')
     AND jsonb_typeof(a->'preview'->'manuscript')='object'
     AND v->'manuscript'=a->'preview'->'manuscript'
     AND v->'manuscript'->>'version'='synthesis-manuscript/v2'
     AND v->'manuscript'->>'content_hash'=encode(sha256(convert_to(v->'manuscript'->>'full_content','UTF8')),'hex')
     AND v->>'hash' ~ '^[0-9a-f]{64}$') IS NOT TRUE THEN
  RAISE EXCEPTION 'receipt lacks exact clean manuscript attempt' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_manuscript_receipt_validate BEFORE INSERT ON organizing.synthesis_manuscript_receipt
 FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_manuscript_receipt();

CREATE TRIGGER synthesis_manuscript_capture_immutable BEFORE UPDATE OR DELETE ON organizing.synthesis_manuscript_capture FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_manuscript_capture_no_truncate BEFORE TRUNCATE ON organizing.synthesis_manuscript_capture FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_manuscript_attempt_immutable BEFORE UPDATE OR DELETE ON organizing.synthesis_manuscript_attempt FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_manuscript_attempt_no_truncate BEFORE TRUNCATE ON organizing.synthesis_manuscript_attempt FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_manuscript_receipt_immutable BEFORE UPDATE OR DELETE ON organizing.synthesis_manuscript_receipt FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_manuscript_receipt_no_truncate BEFORE TRUNCATE ON organizing.synthesis_manuscript_receipt FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

ALTER TABLE organizing.synthesis_revision
 ADD COLUMN manuscript jsonb,
 ADD COLUMN manuscript_receipt_id uuid,
 DROP CONSTRAINT synthesis_revision_renderer_version_check,
 DROP CONSTRAINT synthesis_revision_items_check,
 DROP CONSTRAINT synthesis_revision_item_count_check,
 ADD CONSTRAINT synthesis_revision_renderer_version_check CHECK (renderer_version IN ('synthesis-markdown/v1','synthesis-markdown/v2')),
 ADD CONSTRAINT synthesis_revision_items_check CHECK (jsonb_typeof(items)='array' AND jsonb_array_length(items) BETWEEN 0 AND 128),
 ADD CONSTRAINT synthesis_revision_item_count_check CHECK (item_count BETWEEN 0 AND 128),
 ADD CONSTRAINT synthesis_revision_manuscript_shape CHECK (
  (renderer_version='synthesis-markdown/v1' AND manuscript IS NULL AND manuscript_receipt_id IS NULL AND item_count>=1)
  OR (renderer_version='synthesis-markdown/v2' AND jsonb_typeof(manuscript)='object' AND manuscript_receipt_id IS NOT NULL AND octet_length(manuscript::text)<=16777216)),
 ADD CONSTRAINT synthesis_revision_manuscript_receipt_fk FOREIGN KEY(manuscript_receipt_id,workspace_id) REFERENCES organizing.synthesis_manuscript_receipt(id,workspace_id) ON DELETE RESTRICT;

CREATE FUNCTION organizing.validate_synthesis_revision_manuscript() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r jsonb; a jsonb; p jsonb; l jsonb; trusted jsonb; generated jsonb;
BEGIN
 IF NEW.renderer_version='synthesis-markdown/v1' THEN RETURN NEW; END IF;
 SELECT convert_from(receipt.payload,'UTF8')::jsonb,convert_from(attempt.payload,'UTF8')::jsonb INTO r,a
 FROM organizing.synthesis_manuscript_receipt receipt JOIN organizing.synthesis_manuscript_attempt attempt ON attempt.id=receipt.attempt_id AND attempt.workspace_id=receipt.workspace_id
 WHERE receipt.id=NEW.manuscript_receipt_id AND receipt.workspace_id=NEW.workspace_id;
 p:=a->'prepared'; l:=p->'merge_input'->'latest';
 SELECT COALESCE(jsonb_agg(item ORDER BY ord),'[]'::jsonb) INTO trusted
 FROM jsonb_array_elements(NEW.manuscript->'machine'->'machine_items') WITH ORDINALITY AS machine(item,ord)
 WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.manuscript->'assessment'->'mappings') m WHERE m->>'item_id'=item->>'id');
 SELECT note INTO generated FROM jsonb_array_elements(p->'generation'->'Notes') note WHERE note->>'NoteID'=NEW.note_id::text;
 IF (NEW.manuscript=r->'manuscript' AND NEW.items=trusted
     AND NEW.content_hash=r->'manuscript'->>'content_hash'
     AND NEW.content_hash=encode(sha256(convert_to(NEW.manuscript->>'full_content','UTF8')),'hex')
     AND NEW.parent_revision_id::text=l->>'id' AND NEW.note_id::text=l->>'note_id' AND NEW.document_id::text=l->>'document_id'
     AND NEW.revision_no=(l->>'revision_no')::integer+1 AND NEW.article_revision_no=(l->>'article_revision_no')::integer+1
     AND NEW.source_event_id::text=p->'generation_input'->'SourceEvent'->>'id'
     AND NEW.workflow_run_id::text=p->'generation_input'->>'WorkflowRunID'
     AND NEW.model_run_id::text=p->'generation'->>'ModelRunID'
     AND NEW.delta=generated->'Delta') IS NOT TRUE THEN
  RAISE EXCEPTION 'v2 revision lacks exact verified manuscript receipt' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_revision_manuscript_validate BEFORE INSERT ON organizing.synthesis_revision
 FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_revision_manuscript();

-- A receipt is never a substitute for the existing candidate/model/publication
-- closure. Join its full generation identities to the actual apply receipt.
CREATE FUNCTION organizing.verify_synthesis_manuscript_closure() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a jsonb; p jsonb;
BEGIN
 IF NEW.renderer_version='synthesis-markdown/v1' THEN RETURN NULL; END IF;
 SELECT convert_from(attempt.payload,'UTF8')::jsonb INTO a
 FROM organizing.synthesis_manuscript_receipt receipt
 JOIN organizing.synthesis_manuscript_attempt attempt ON attempt.id=receipt.attempt_id AND attempt.workspace_id=receipt.workspace_id
 WHERE receipt.id=NEW.manuscript_receipt_id AND receipt.workspace_id=NEW.workspace_id;
 p:=a->'prepared';
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_apply_receipt applied
  JOIN authoring.generated_article_revision generated ON generated.workspace_id=applied.workspace_id
  AND generated.article_revision_id=NEW.article_revision_id AND generated.content_hash=NEW.content_hash
  AND generated.projection_hash=NEW.projection_hash
  WHERE applied.workspace_id=NEW.workspace_id AND applied.processing_id::text=a->'command'->>'processing_id'
  AND applied.model_run_id=NEW.model_run_id AND applied.workflow_run_id=NEW.workflow_run_id
  AND applied.source_event_id=NEW.source_event_id
  AND applied.request_hash=p->'generation_input'->>'RequestHash'
  AND applied.output_hash=p->'generation'->>'OutputHash'
  AND applied.result->'revision_ids' @> jsonb_build_array(NEW.id::text)) THEN
  RAISE EXCEPTION 'manuscript candidate lacks exact model and generated receipt closure' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END; $$;
CREATE CONSTRAINT TRIGGER synthesis_manuscript_verify_closure AFTER INSERT ON organizing.synthesis_revision
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_manuscript_closure();
