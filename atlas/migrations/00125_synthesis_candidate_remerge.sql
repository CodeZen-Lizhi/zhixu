-- Candidate-only file remerge. No processing/model/history mutation.
CREATE TABLE organizing.synthesis_candidate_remerge_event (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
 note_id uuid NOT NULL,
 attempt_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('BEGIN','APPLY')),
 idempotency_key text NOT NULL CHECK(idempotency_key=btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 200),
 payload bytea NOT NULL CHECK(octet_length(payload) BETWEEN 1 AND 16777216),
 payload_hash text NOT NULL CHECK(payload_hash=encode(sha256(payload),'hex')),
 UNIQUE(id,workspace_id), UNIQUE(workspace_id,note_id,kind,idempotency_key), UNIQUE(attempt_id,kind),
 FOREIGN KEY(note_id,workspace_id) REFERENCES organizing.synthesis_note(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(attempt_id,workspace_id) REFERENCES organizing.synthesis_candidate_remerge_event(id,workspace_id) ON DELETE RESTRICT
);
CREATE TRIGGER synthesis_candidate_remerge_immutable BEFORE UPDATE OR DELETE ON organizing.synthesis_candidate_remerge_event FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_candidate_remerge_no_truncate BEFORE TRUNCATE ON organizing.synthesis_candidate_remerge_event FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
ALTER TABLE organizing.synthesis_revision
 ADD COLUMN remerge jsonb,
 ADD COLUMN candidate_remerge_id uuid,
 ADD CONSTRAINT synthesis_revision_remerge_fk FOREIGN KEY(candidate_remerge_id,workspace_id) REFERENCES organizing.synthesis_candidate_remerge_event(id,workspace_id) ON DELETE RESTRICT,
 ADD CONSTRAINT synthesis_revision_remerge_shape CHECK (((remerge IS NULL AND candidate_remerge_id IS NULL) OR
  (candidate_remerge_id IS NOT NULL AND renderer_version='synthesis-markdown/v2' AND jsonb_typeof(remerge)='object' AND remerge->>'source_revision_id'=parent_revision_id::text AND remerge->>'attempt_id' ~ '^[0-9a-f-]{36}$')) IS TRUE);

CREATE FUNCTION organizing.validate_synthesis_candidate_remerge_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; a jsonb; oldr organizing.synthesis_revision%ROWTYPE; doc core.document%ROWTYPE; binding authoring.document_publication_binding%ROWTYPE; proposal change_control.proposal%ROWTYPE; cp jsonb; original_capture jsonb; original_attempt jsonb; original_receipt jsonb; trusted jsonb;
BEGIN
 v:=convert_from(NEW.payload,'UTF8')::jsonb;
 IF (v->'command'->>'workspace_id'=NEW.workspace_id::text AND v->'command'->>'note_id'=NEW.note_id::text AND v->'command'->>'idempotency_key'=NEW.idempotency_key) IS NOT TRUE THEN
  RAISE EXCEPTION 'remerge event scope mismatch' USING ERRCODE='23514';
 END IF;
 IF NEW.kind='BEGIN' THEN
  a:=v;
  IF (NEW.id=NEW.attempt_id AND a->>'id'=NEW.id::text) IS NOT TRUE THEN RAISE EXCEPTION 'invalid remerge begin' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT convert_from(payload,'UTF8')::jsonb INTO a FROM organizing.synthesis_candidate_remerge_event WHERE id=NEW.attempt_id AND workspace_id=NEW.workspace_id AND note_id=NEW.note_id AND kind='BEGIN';
  IF (v->'command'->>'attempt_id'=NEW.attempt_id::text) IS NOT TRUE OR a IS NULL THEN RAISE EXCEPTION 'invalid remerge application' USING ERRCODE='23514'; END IF;
 END IF;
 SELECT * INTO oldr FROM organizing.synthesis_revision WHERE workspace_id=NEW.workspace_id AND note_id=NEW.note_id AND id=(a->'command'->>'expected_revision_id')::uuid;
 SELECT * INTO doc FROM core.document WHERE workspace_id=NEW.workspace_id AND id=oldr.document_id FOR UPDATE;
 SELECT * INTO binding FROM authoring.document_publication_binding WHERE workspace_id=NEW.workspace_id AND id=(a->'command'->>'expected_publication_id')::uuid FOR UPDATE;
 SELECT * INTO proposal FROM change_control.proposal WHERE workspace_id=NEW.workspace_id AND id=binding.proposal_id FOR UPDATE;
 SELECT convert_from(mr.payload,'UTF8')::jsonb,convert_from(ma.payload,'UTF8')::jsonb,convert_from(mc.payload,'UTF8')::jsonb INTO original_receipt,original_attempt,original_capture
 FROM organizing.synthesis_manuscript_receipt mr JOIN organizing.synthesis_manuscript_attempt ma ON ma.id=mr.attempt_id AND ma.workspace_id=mr.workspace_id JOIN organizing.synthesis_manuscript_capture mc ON mc.id=ma.capture_id AND mc.workspace_id=mr.workspace_id WHERE mr.id=oldr.manuscript_receipt_id AND mr.workspace_id=NEW.workspace_id;
 IF oldr.remerge IS NOT NULL THEN
  SELECT convert_from(payload,'UTF8')::jsonb->'capture' INTO original_capture FROM organizing.synthesis_candidate_remerge_event WHERE id=(oldr.remerge->>'attempt_id')::uuid AND workspace_id=NEW.workspace_id AND note_id=NEW.note_id AND kind='BEGIN';
 END IF;
 SELECT convert_from(payload,'UTF8')::jsonb INTO cp FROM organizing.synthesis_manuscript_capture WHERE id=(a->'capture'->>'id')::uuid AND workspace_id=NEW.workspace_id;
 IF (oldr.renderer_version='synthesis-markdown/v2' AND oldr.document_id::text=a->'command'->>'expected_document_id'
  AND oldr.projection_hash=a->'original'->>'hash' AND oldr.manuscript=a->'original'->'manuscript'
  AND oldr.manuscript_receipt_id::text=a->>'receipt_id'
  AND original_capture=a->'base_capture' AND cp=a->'capture' AND (cp->>'exists')::boolean
  AND cp->>'target_path'=doc.canonical_path AND cp->>'workspace_id'=NEW.workspace_id::text
  AND EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_root_identity root_identity JOIN core.workspace w ON w.id=root_identity.workspace_id
    WHERE root_identity.id::text=cp->>'root_grant_id' AND root_identity.workspace_id=NEW.workspace_id
    AND root_identity.root_fingerprint=cp->>'root_fingerprint' AND root_identity.binding_version=(cp->>'workspace_binding_version')::bigint
    AND w.root_fingerprint=root_identity.root_fingerprint AND w.binding_version=root_identity.binding_version AND w.status='active' AND w.availability='available' AND w.removed_at IS NULL)
  AND doc.version=(a->'command'->>'expected_document_version')::bigint
  AND doc.current_published_revision_id::text=a->>'published_revision_id'
  AND authoring.publication_is_exact_current(NEW.workspace_id,doc.id,doc.current_published_revision_id)
  AND EXISTS(SELECT 1 FROM core.article_revision pub WHERE pub.workspace_id=NEW.workspace_id AND pub.document_id=doc.id AND pub.id=doc.current_published_revision_id AND pub.content_hash=a->>'published_content_hash')
  AND EXISTS(SELECT 1 FROM organizing.synthesis_note n WHERE n.workspace_id=NEW.workspace_id AND n.id=NEW.note_id AND n.document_id=doc.id AND n.current_revision_id=oldr.id AND n.version=(a->'command'->>'expected_note_version')::bigint)
  AND EXISTS(SELECT 1 FROM organizing.synthesis_processing p WHERE p.workspace_id=NEW.workspace_id AND p.id=(original_attempt->'command'->>'processing_id')::uuid AND p.status='SUCCEEDED')
  AND binding.document_id=doc.id AND binding.article_revision_id=oldr.article_revision_id AND (binding.status='PENDING' OR (binding.status='CLOSED' AND binding.error_code='AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION' AND proposal.status='needs_revision'))
  AND binding.proposal_id::text=a->'command'->>'expected_proposal_id' AND binding.proposal_revision_id::text=a->'command'->>'expected_proposal_revision_id'
  AND proposal.current_revision_id=binding.proposal_revision_id AND proposal.version=(a->'command'->>'expected_proposal_version')::bigint AND proposal.status IN ('ready_for_review','needs_revision') AND proposal.workflow_run_id IS NULL
  AND NOT EXISTS(SELECT 1 FROM change_control.approval WHERE proposal_id=proposal.id)
  AND NOT EXISTS(SELECT 1 FROM change_control.proposal_revision_dispatch WHERE proposal_id=proposal.id)
  AND NOT EXISTS(SELECT 1 FROM change_control.tool_authorization WHERE proposal_id=proposal.id)
  AND NOT EXISTS(SELECT 1 FROM change_control.writeback_execution WHERE proposal_id=proposal.id)
  AND NOT EXISTS(SELECT 1 FROM change_control.proposal_commit WHERE proposal_id=proposal.id)) IS NOT TRUE THEN
  RAISE EXCEPTION 'remerge owner baseline changed' USING ERRCODE='23514';
 END IF;
 IF NEW.kind='APPLY' THEN
  SELECT COALESCE(jsonb_agg(item ORDER BY ord),'[]'::jsonb) INTO trusted FROM jsonb_array_elements(v->'revision'->'manuscript'->'machine'->'machine_items') WITH ORDINALITY AS machine(item,ord) WHERE EXISTS(SELECT 1 FROM jsonb_array_elements(v->'revision'->'manuscript'->'assessment'->'mappings') m WHERE m->>'item_id'=item->>'id');
  IF (v->'revision'->>'workspace_id'=NEW.workspace_id::text AND v->'revision'->>'note_id'=NEW.note_id::text
   AND v->'revision'->>'document_id'=doc.id::text AND v->'revision'->>'parent_revision_id'=oldr.id::text
   AND v->'revision'->'remerge'=jsonb_build_object('attempt_id',NEW.attempt_id::text,'source_revision_id',oldr.id::text)
   AND v->'revision'->>'source_event_id'=oldr.source_event_id::text AND v->'revision'->>'workflow_run_id'=oldr.workflow_run_id::text AND v->'revision'->>'model_run_id'=oldr.model_run_id::text
   AND v->'revision'->'delta'=oldr.delta AND v->'revision'->>'title'=oldr.title
   AND (v->'revision'->>'revision_no')::bigint=oldr.revision_no+1 AND (v->'revision'->>'article_revision_no')::bigint=oldr.article_revision_no+1
   AND v->'revision'->'manuscript'->'machine'->'machine_items'=oldr.manuscript->'machine'->'machine_items'
   AND v->'revision'->'manuscript'->'machine'->>'machine_title'=oldr.manuscript->'machine'->>'machine_title'
   AND v->'revision'->'manuscript'->'machine'->'ineligible_item_ids'=(SELECT COALESCE(jsonb_agg(id ORDER BY id),'[]'::jsonb) FROM (
    SELECT jsonb_array_elements_text(oldr.manuscript->'machine'->'ineligible_item_ids') AS id
    UNION SELECT item->>'item_id' FROM jsonb_array_elements(oldr.manuscript->'assessment'->'review_items') item) excluded)
   AND v->'revision'->'manuscript'->'machine'->>'workspace_id'=NEW.workspace_id::text AND v->'revision'->'manuscript'->'machine'->>'note_id'=NEW.note_id::text
   AND v->'revision'->'items'=trusted
   AND v->'revision'->>'content_hash'=v->'revision'->'manuscript'->>'content_hash'
   AND v->'revision'->>'content_hash'=encode(sha256(convert_to(v->'revision'->'manuscript'->>'full_content','UTF8')),'hex')
   AND ((a->'preview'->'review' IS NULL AND v->'command'->'resolution' IS NULL AND v->'revision'->'manuscript'=a->'preview'->'manuscript')
    OR (a->'preview'->'review' IS NOT NULL AND v->'command'->'resolution'->>'stage'=a->'preview'->'review'->>'stage'
     AND v->'command'->'resolution'->>'preview_fingerprint'=a->'preview'->>'fingerprint'
     AND v->'command'->'resolution'->>'final_content'=v->'revision'->'manuscript'->>'full_content'
     AND v->'command'->'resolution'->'acknowledged_ordinals'=(SELECT jsonb_agg(c->'Ordinal' ORDER BY ord) FROM jsonb_array_elements(a->'preview'->'review'->'conflicts') WITH ORDINALITY AS conflicts(c,ord))))) IS NOT TRUE THEN
   RAISE EXCEPTION 'remerge resolution or provenance mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_candidate_remerge_validate BEFORE INSERT ON organizing.synthesis_candidate_remerge_event FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_candidate_remerge_event();

CREATE OR REPLACE FUNCTION organizing.validate_synthesis_revision_manuscript() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r jsonb; a jsonb; p jsonb; l jsonb; trusted jsonb; generated jsonb;
BEGIN

 IF NEW.candidate_remerge_id IS NOT NULL THEN
  IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_candidate_remerge_event e
    JOIN organizing.synthesis_candidate_remerge_event b ON b.id=e.attempt_id AND b.workspace_id=e.workspace_id AND b.note_id=e.note_id AND b.kind='BEGIN'
    CROSS JOIN LATERAL(SELECT convert_from(e.payload,'UTF8')::jsonb v,convert_from(b.payload,'UTF8')::jsonb a) x
    WHERE e.id=NEW.candidate_remerge_id AND e.workspace_id=NEW.workspace_id AND e.note_id=NEW.note_id AND e.kind='APPLY'
    AND v->'revision'->>'id'=NEW.id::text AND v->'revision'->>'article_revision_id'=NEW.article_revision_id::text
    AND v->'revision'->>'document_id'=NEW.document_id::text AND v->'revision'->>'parent_revision_id'=NEW.parent_revision_id::text
    AND (v->'revision'->>'revision_no')::bigint=NEW.revision_no AND (v->'revision'->>'article_revision_no')::bigint=NEW.article_revision_no
    AND v->'revision'->>'title'=NEW.title AND v->'revision'->>'renderer_version'=NEW.renderer_version
    AND v->'revision'->>'hash'=NEW.projection_hash AND v->'revision'->>'content_hash'=NEW.content_hash
    AND v->'revision'->'manuscript'=NEW.manuscript AND v->'revision'->'items'=NEW.items AND v->'revision'->'delta'=NEW.delta AND v->'revision'->'remerge'=NEW.remerge
    AND v->'revision'->>'source_event_id'=NEW.source_event_id::text AND v->'revision'->>'workflow_run_id'=NEW.workflow_run_id::text AND v->'revision'->>'model_run_id'=NEW.model_run_id::text
    AND (v->'revision'->>'created_at')::timestamptz=NEW.created_at AND x.a->>'receipt_id'=NEW.manuscript_receipt_id::text) THEN
   RAISE EXCEPTION 'remerge revision lacks exact application' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
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

CREATE OR REPLACE FUNCTION organizing.verify_synthesis_manuscript_closure() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a jsonb; p jsonb;
BEGIN

 IF NEW.candidate_remerge_id IS NOT NULL THEN
  IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_candidate_remerge_event e
    JOIN organizing.synthesis_candidate_remerge_event b ON b.id=e.attempt_id AND b.workspace_id=e.workspace_id AND b.note_id=e.note_id AND b.kind='BEGIN'
    CROSS JOIN LATERAL(SELECT convert_from(e.payload,'UTF8')::jsonb v,convert_from(b.payload,'UTF8')::jsonb a) x
    WHERE e.id=NEW.candidate_remerge_id AND e.workspace_id=NEW.workspace_id AND e.note_id=NEW.note_id AND e.kind='APPLY'
    AND v->'revision'->>'id'=NEW.id::text AND v->'revision'->>'article_revision_id'=NEW.article_revision_id::text
    AND v->'revision'->>'document_id'=NEW.document_id::text AND v->'revision'->>'parent_revision_id'=NEW.parent_revision_id::text
    AND (v->'revision'->>'revision_no')::bigint=NEW.revision_no AND (v->'revision'->>'article_revision_no')::bigint=NEW.article_revision_no
    AND v->'revision'->>'title'=NEW.title AND v->'revision'->>'renderer_version'=NEW.renderer_version
    AND v->'revision'->>'hash'=NEW.projection_hash AND v->'revision'->>'content_hash'=NEW.content_hash
    AND v->'revision'->'manuscript'=NEW.manuscript AND v->'revision'->'items'=NEW.items AND v->'revision'->'delta'=NEW.delta AND v->'revision'->'remerge'=NEW.remerge
    AND v->'revision'->>'source_event_id'=NEW.source_event_id::text AND v->'revision'->>'workflow_run_id'=NEW.workflow_run_id::text AND v->'revision'->>'model_run_id'=NEW.model_run_id::text
    AND (v->'revision'->>'created_at')::timestamptz=NEW.created_at AND x.a->>'receipt_id'=NEW.manuscript_receipt_id::text) THEN
   RAISE EXCEPTION 'remerge revision lacks exact application' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
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

CREATE OR REPLACE VIEW organizing.synthesis_publication_merge_baseline AS
SELECT r.workspace_id,r.document_id,r.article_revision_id,r.content_hash,r.projection_hash,
       receipt.id AS receipt_id,capture.id AS capture_id,
       cp->>'target_path' AS target_path,
       CASE WHEN (cp->>'exists')::boolean THEN cp->>'content_hash' ELSE cp->>'absence_token' END AS file_base,
       pub.article_revision_id AS published_revision_id,pub.content_hash AS published_content_hash,
       generated.document_version,
       COALESCE(
           r.manuscript=rp->'manuscript'
           AND r.content_hash=rp->'manuscript'->>'content_hash'
           AND ar.content=rp->'manuscript'->>'full_content'
           AND generated.expected_document_version=(ap->'prepared'->'authority'->>'document_version')::bigint
           AND generated.parent_revision_id::text=ap->'prepared'->'authority'->>'latest_article_id'
           AND cp->>'target_path'=ap->'prepared'->'authority'->>'target_path'
           AND ((ap->'prepared'->'merge_input'->'published' IS NULL
                 AND NOT (cp->>'exists')::boolean
                 AND NOT (ap->'prepared'->'authority' ? 'published_publication_id')
                 AND NOT (ap->'prepared'->'authority' ? 'published_proposal_commit_id')
                 AND NOT (ap->'prepared'->'authority' ? 'published_git_commit'))
                OR ((cp->>'exists')::boolean
                    AND pub.revision_id::text=ap->'prepared'->'merge_input'->'published'->>'id'
                    AND pub.note_id=r.note_id AND pub.document_id=r.document_id
                    AND pub.article_revision_id::text=ap->'prepared'->'merge_input'->'published'->>'article_revision_id'
                    AND pub.content_hash=ap->'prepared'->'merge_input'->'published'->>'content_hash'
                    AND pub.projection_hash=ap->'prepared'->'merge_input'->'published'->>'hash'
                    AND pub.publication_id::text=ap->'prepared'->'authority'->>'published_publication_id'
                    AND pub.proposal_commit_id::text=ap->'prepared'->'authority'->>'published_proposal_commit_id'
                    AND pub.git_commit=ap->'prepared'->'authority'->>'published_git_commit')),
           false) AS valid
FROM organizing.synthesis_revision r
JOIN organizing.synthesis_manuscript_receipt receipt ON receipt.id=r.manuscript_receipt_id AND receipt.workspace_id=r.workspace_id
JOIN organizing.synthesis_manuscript_attempt attempt ON attempt.id=receipt.attempt_id AND attempt.workspace_id=r.workspace_id
JOIN organizing.synthesis_manuscript_capture capture ON capture.id=attempt.capture_id AND capture.workspace_id=r.workspace_id
CROSS JOIN LATERAL (SELECT convert_from(receipt.payload,'UTF8')::jsonb rp,
                          convert_from(attempt.payload,'UTF8')::jsonb ap,
                          convert_from(capture.payload,'UTF8')::jsonb cp) payloads
JOIN authoring.generated_article_revision generated ON generated.article_revision_id=r.article_revision_id
 AND generated.workspace_id=r.workspace_id AND generated.document_id=r.document_id
 AND generated.origin_id=r.note_id AND generated.origin_revision_id=r.id
 AND generated.projection_hash=r.projection_hash AND generated.content_hash=r.content_hash
JOIN core.article_revision ar ON ar.id=r.article_revision_id AND ar.workspace_id=r.workspace_id
 AND ar.document_id=r.document_id AND ar.content_hash=r.content_hash AND ar.created_by_type='AGENT'
LEFT JOIN organizing.synthesis_proven_publication pub ON pub.workspace_id=r.workspace_id
 AND pub.revision_id::text=ap->'prepared'->'merge_input'->'published'->>'id'
WHERE r.renderer_version='synthesis-markdown/v2' AND r.candidate_remerge_id IS NULL
UNION ALL
SELECT r.workspace_id,r.document_id,r.article_revision_id,r.content_hash,r.projection_hash,
 (a->>'receipt_id')::uuid AS receipt_id,(a->'capture'->>'id')::uuid AS capture_id,
 a->'capture'->>'target_path' AS target_path,a->'capture'->>'content_hash' AS file_base,
 (a->>'published_revision_id')::uuid AS published_revision_id,a->>'published_content_hash' AS published_content_hash,
 generated.document_version,
 COALESCE(r.manuscript=v->'revision'->'manuscript' AND r.content_hash=v->'revision'->>'content_hash'
  AND r.projection_hash=v->'revision'->>'hash' AND r.remerge=v->'revision'->'remerge'
  AND ar.content=r.manuscript->>'full_content'
  AND generated.expected_document_version=(a->'command'->>'expected_document_version')::bigint
  AND generated.parent_revision_id::text=a->'original'->>'article_revision_id'
  AND pub.content_hash=a->>'published_content_hash',false) AS valid
 FROM organizing.synthesis_revision r
 JOIN organizing.synthesis_candidate_remerge_event e ON e.id=r.candidate_remerge_id AND e.workspace_id=r.workspace_id AND e.note_id=r.note_id AND e.kind='APPLY'
 JOIN organizing.synthesis_candidate_remerge_event b ON b.id=e.attempt_id AND b.workspace_id=e.workspace_id AND b.note_id=e.note_id AND b.kind='BEGIN'
 CROSS JOIN LATERAL(SELECT convert_from(e.payload,'UTF8')::jsonb v,convert_from(b.payload,'UTF8')::jsonb a) x
 JOIN authoring.generated_article_revision generated ON generated.article_revision_id=r.article_revision_id AND generated.workspace_id=r.workspace_id AND generated.document_id=r.document_id AND generated.origin_id=r.note_id AND generated.origin_revision_id=r.id AND generated.projection_hash=r.projection_hash AND generated.content_hash=r.content_hash
 JOIN core.article_revision ar ON ar.id=r.article_revision_id AND ar.workspace_id=r.workspace_id AND ar.document_id=r.document_id AND ar.content_hash=r.content_hash AND ar.created_by_type='AGENT'
 JOIN core.article_revision pub ON pub.workspace_id=r.workspace_id AND pub.document_id=r.document_id AND pub.id=(a->>'published_revision_id')::uuid
 WHERE r.renderer_version='synthesis-markdown/v2';

CREATE FUNCTION organizing.verify_synthesis_candidate_remerge_closure() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; a jsonb;
BEGIN
 IF NEW.kind<>'APPLY' THEN RETURN NULL; END IF;
 v:=convert_from(NEW.payload,'UTF8')::jsonb;
 SELECT convert_from(payload,'UTF8')::jsonb INTO a FROM organizing.synthesis_candidate_remerge_event WHERE id=NEW.attempt_id AND workspace_id=NEW.workspace_id;
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_revision r
 JOIN organizing.synthesis_note n ON n.id=r.note_id AND n.workspace_id=r.workspace_id AND n.current_revision_id=r.id
 JOIN authoring.generated_article_revision g ON g.workspace_id=r.workspace_id AND g.document_id=r.document_id AND g.article_revision_id=r.article_revision_id AND g.origin_kind='SYNTHESIS_NOTE' AND g.origin_id=r.note_id AND g.origin_revision_id=r.id AND g.projection_hash=r.projection_hash AND g.content_hash=r.content_hash
 JOIN core.article_revision ar ON ar.id=r.article_revision_id AND ar.workspace_id=r.workspace_id AND ar.document_id=r.document_id AND ar.content=r.manuscript->>'full_content' AND ar.content_hash=r.content_hash
 JOIN authoring.document_publication_reservation p ON p.workspace_id=r.workspace_id AND p.document_id=r.document_id AND p.article_revision_id=r.article_revision_id AND p.content_hash=r.content_hash AND p.id=(v->>'reservation_id')::uuid AND p.idempotency_key=v->'publication'->>'idempotency_key'
 JOIN authoring.document_publication_binding oldb ON oldb.id=(a->'command'->>'expected_publication_id')::uuid AND oldb.workspace_id=r.workspace_id AND oldb.status='CLOSED'
 JOIN change_control.proposal oldp ON oldp.id=oldb.proposal_id AND oldp.workspace_id=r.workspace_id AND oldp.status='needs_revision'
 WHERE r.candidate_remerge_id=NEW.id AND r.id::text=v->'revision'->>'id'
 AND n.version=(a->'command'->>'expected_note_version')::bigint+1
 AND p.base_version=a->'capture'->>'content_hash' AND p.merge_capture_id::text=a->'capture'->>'id' AND p.merge_receipt_id::text=a->>'receipt_id'
 AND v->'publication'->>'note_id'=r.note_id::text AND v->'publication'->>'revision_id'=r.id::text
 AND v->'publication'->>'document_id'=r.document_id::text AND v->'publication'->>'article_revision_id'=r.article_revision_id::text AND v->'publication'->>'content_hash'=r.content_hash) THEN
 RAISE EXCEPTION 'remerge candidate transaction is not closed' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END; $$;
CREATE CONSTRAINT TRIGGER synthesis_candidate_remerge_closure AFTER INSERT ON organizing.synthesis_candidate_remerge_event DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_candidate_remerge_closure();

CREATE OR REPLACE FUNCTION organizing.verify_synthesis_closure()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected_sources jsonb; actual_sources jsonb;
BEGIN
    IF TG_TABLE_NAME='synthesis_note' THEN
        IF (SELECT count(*) FROM organizing.synthesis_topic_key WHERE workspace_id=NEW.workspace_id AND note_id=NEW.id)
           <> cardinality(NEW.aliases)+1 OR EXISTS (
            SELECT 1 FROM organizing.synthesis_topic_key WHERE workspace_id=NEW.workspace_id AND note_id=NEW.id
             AND topic_key<>NEW.topic_key AND NOT topic_key=ANY(NEW.aliases)
        ) THEN RAISE EXCEPTION 'synthesis topic namespace is incomplete' USING ERRCODE='23514'; END IF;
    ELSE
        SELECT COALESCE(jsonb_agg(source ORDER BY source::text),'[]'::jsonb) INTO expected_sources
          FROM organizing.synthesis_item_sources(NEW.items) source;
        SELECT COALESCE(jsonb_agg(reference ORDER BY reference::text),'[]'::jsonb) INTO actual_sources FROM (
            SELECT jsonb_build_object('source',jsonb_build_object(
                'workspace_id',s.workspace_id,'source_id',s.source_id,'source_version_id',s.source_version_id,
                'content_artifact_id',s.content_artifact_id,'parse_projection_id',s.parse_projection_id,'content_hash',s.content_hash),
                'source_span_id',s.source_span_id,'excerpt_hash',s.excerpt_hash,'title',s.title) AS reference
            FROM organizing.synthesis_revision_source s WHERE s.revision_id=NEW.id AND s.workspace_id=NEW.workspace_id AND s.note_id=NEW.note_id
        ) source;
        IF NEW.candidate_remerge_id IS NOT NULL THEN
            IF actual_sources<>expected_sources OR jsonb_array_length(actual_sources)>256 OR NOT EXISTS (
                SELECT 1 FROM organizing.synthesis_candidate_remerge_event e WHERE e.id=NEW.candidate_remerge_id AND e.workspace_id=NEW.workspace_id AND e.note_id=NEW.note_id AND e.kind='APPLY' AND convert_from(e.payload,'UTF8')::jsonb->'revision'->>'id'=NEW.id::text
            ) THEN RAISE EXCEPTION 'remerge projection lacks exact sources and application' USING ERRCODE='23514'; END IF;
            RETURN NULL;
        END IF;
        IF actual_sources<>expected_sources OR jsonb_array_length(actual_sources)>256 OR NOT EXISTS (
            SELECT 1 FROM organizing.synthesis_apply_receipt receipt
             WHERE receipt.workspace_id=NEW.workspace_id AND receipt.workflow_run_id=NEW.workflow_run_id
               AND receipt.model_run_id=NEW.model_run_id AND receipt.source_event_id=NEW.source_event_id
               AND receipt.result->'revision_ids' @> jsonb_build_array(NEW.id::text)
        ) THEN RAISE EXCEPTION 'synthesis projection lacks its exact sources and receipt' USING ERRCODE='23514'; END IF;
    END IF;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION authoring.validate_generated_retirement_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    binding_record authoring.document_publication_binding%ROWTYPE;
    proposal_record change_control.proposal%ROWTYPE;
    document_version bigint;
    latest_revision uuid;
BEGIN
    SELECT * INTO binding_record FROM authoring.document_publication_binding
     WHERE id=NEW.publication_id AND workspace_id=NEW.workspace_id FOR SHARE;
    SELECT * INTO proposal_record FROM change_control.proposal
     WHERE id=binding_record.proposal_id AND workspace_id=NEW.workspace_id FOR SHARE;
    SELECT version INTO document_version FROM core.document
     WHERE id=NEW.document_id AND workspace_id=NEW.workspace_id FOR SHARE;
    SELECT id INTO latest_revision FROM core.article_revision
     WHERE document_id=NEW.document_id AND workspace_id=NEW.workspace_id
     ORDER BY revision_no DESC LIMIT 1;
    IF proposal_record.version=NEW.expected_proposal_version AND EXISTS (
      SELECT 1 FROM organizing.synthesis_candidate_remerge_event e
      JOIN organizing.synthesis_candidate_remerge_event attempt ON attempt.id=e.attempt_id AND attempt.workspace_id=e.workspace_id AND attempt.note_id=e.note_id AND attempt.kind='BEGIN'
      CROSS JOIN LATERAL(SELECT convert_from(attempt.payload,'UTF8')::jsonb a) payload
      WHERE e.kind='APPLY' AND e.workspace_id=NEW.workspace_id AND e.note_id=NEW.origin_id
       AND NEW.origin_kind='SYNTHESIS_NOTE' AND a->'command'->>'expected_publication_id'=NEW.publication_id::text
       AND a->'command'->>'expected_revision_id'=NEW.origin_revision_id::text
       AND a->'command'->>'expected_document_id'=NEW.document_id::text
       AND (a->'command'->>'expected_document_version')::bigint=NEW.expected_document_version
       AND (a->'command'->>'expected_proposal_version')::bigint=NEW.expected_proposal_version
       AND a->'original'->>'article_revision_id'=NEW.article_revision_id::text
       AND a->'original'->>'hash'=NEW.projection_hash
    ) THEN
    IF binding_record.id IS NULL OR proposal_record.id IS NULL
       OR binding_record.document_id<>NEW.document_id OR binding_record.article_revision_id<>NEW.article_revision_id
       OR latest_revision IS DISTINCT FROM NEW.article_revision_id
       OR document_version IS DISTINCT FROM NEW.expected_document_version
       OR binding_record.status<>'CLOSED' OR binding_record.error_code<>'AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION'
       OR binding_record.git_commit IS NOT NULL OR binding_record.published_at IS NOT NULL
       OR proposal_record.proposal_type<>'file_patch' OR proposal_record.status<>'needs_revision'
       OR proposal_record.current_revision_id IS DISTINCT FROM binding_record.proposal_revision_id
       OR proposal_record.version<>NEW.expected_proposal_version OR proposal_record.workflow_run_id IS NOT NULL
       OR NEW.created_at<binding_record.updated_at OR NEW.created_at<proposal_record.updated_at
       OR EXISTS (SELECT 1 FROM change_control.approval WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.proposal_revision_dispatch WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.tool_authorization WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.writeback_execution WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.proposal_commit WHERE proposal_id=proposal_record.id) THEN
        RAISE EXCEPTION 'generated publication retirement lacks an exact unapproved terminal binding' USING ERRCODE='23514';
    END IF;
      RETURN NEW;
    END IF;
    IF binding_record.id IS NULL OR proposal_record.id IS NULL
       OR binding_record.document_id<>NEW.document_id OR binding_record.article_revision_id<>NEW.article_revision_id
       OR latest_revision IS DISTINCT FROM NEW.article_revision_id
       OR document_version IS DISTINCT FROM NEW.expected_document_version
       OR binding_record.status<>'CLOSED' OR binding_record.error_code<>'AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION'
       OR binding_record.git_commit IS NOT NULL OR binding_record.published_at IS NOT NULL
       OR proposal_record.proposal_type<>'file_patch' OR proposal_record.status<>'needs_revision'
       OR proposal_record.current_revision_id IS DISTINCT FROM binding_record.proposal_revision_id
       OR proposal_record.version<>NEW.expected_proposal_version+1 OR proposal_record.workflow_run_id IS NOT NULL
       OR NEW.created_at<binding_record.updated_at OR NEW.created_at<proposal_record.updated_at
       OR EXISTS (SELECT 1 FROM change_control.approval WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.proposal_revision_dispatch WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.tool_authorization WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.writeback_execution WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.proposal_commit WHERE proposal_id=proposal_record.id) THEN
        RAISE EXCEPTION 'generated publication retirement lacks an exact unapproved terminal binding' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

