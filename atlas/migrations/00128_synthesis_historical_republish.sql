-- Exact human selection of a persisted synthesis revision. No new model proof.
CREATE TABLE organizing.synthesis_historical_republish_event (
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
 FOREIGN KEY(attempt_id,workspace_id) REFERENCES organizing.synthesis_historical_republish_event(id,workspace_id) ON DELETE RESTRICT
);
CREATE TRIGGER synthesis_historical_republish_immutable BEFORE UPDATE OR DELETE ON organizing.synthesis_historical_republish_event FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_historical_republish_no_truncate BEFORE TRUNCATE ON organizing.synthesis_historical_republish_event FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
ALTER TABLE organizing.synthesis_revision
 ADD COLUMN historical_republish jsonb,
 ADD COLUMN historical_republish_id uuid,
 ADD CONSTRAINT synthesis_historical_republish_fk FOREIGN KEY(historical_republish_id,workspace_id) REFERENCES organizing.synthesis_historical_republish_event(id,workspace_id) ON DELETE RESTRICT,
 ADD CONSTRAINT synthesis_historical_republish_shape CHECK (((historical_republish IS NULL AND historical_republish_id IS NULL) OR
 (historical_republish_id IS NOT NULL AND jsonb_typeof(historical_republish)='object' AND candidate_remerge_id IS NULL AND remerge IS NULL AND revision_no>1
 AND historical_republish->>'attempt_id' ~ '^[0-9a-f-]{36}$' AND historical_republish->>'selected_revision_id' ~ '^[0-9a-f-]{36}$')) IS TRUE),
 ADD CONSTRAINT synthesis_historical_republish_apply_unique UNIQUE(historical_republish_id);
ALTER TABLE authoring.document_publication_reservation
 ADD COLUMN historical_republish_id uuid,
 ADD CONSTRAINT publication_historical_republish_fk FOREIGN KEY(historical_republish_id,workspace_id) REFERENCES organizing.synthesis_historical_republish_event(id,workspace_id) ON DELETE RESTRICT,
 DROP CONSTRAINT publication_merge_shape,
 ADD CONSTRAINT publication_merge_shape CHECK (
 (merge_receipt_id IS NULL AND historical_republish_id IS NULL AND merge_capture_id IS NULL AND merge_published_revision_id IS NULL AND merge_published_content_hash IS NULL)
 OR (((merge_receipt_id IS NOT NULL AND historical_republish_id IS NULL) OR (merge_receipt_id IS NULL AND historical_republish_id IS NOT NULL))
 AND merge_capture_id IS NOT NULL AND ((target_mode='CREATE_ONLY' AND merge_published_revision_id IS NULL AND merge_published_content_hash IS NULL)
 OR (target_mode='REPLACE' AND merge_published_revision_id IS NOT NULL AND merge_published_content_hash ~ '^[0-9a-f]{64}$'))));

-- Persisted generated identity plus exact old receipt, or a strictly descending
-- human-copy chain. Publication is deliberately not a prerequisite.
CREATE FUNCTION organizing.synthesis_historical_revision_proven(w uuid,n uuid,rid uuid) RETURNS boolean LANGUAGE plpgsql STABLE AS $$
DECLARE r organizing.synthesis_revision%ROWTYPE; selected organizing.synthesis_revision%ROWTYPE; e jsonb;
BEGIN
 SELECT * INTO r FROM organizing.synthesis_revision WHERE id=rid AND workspace_id=w AND note_id=n;
 IF NOT FOUND THEN RETURN false; END IF;
 IF NOT EXISTS(SELECT 1 FROM authoring.generated_article_revision g JOIN core.article_revision ar ON ar.id=g.article_revision_id AND ar.workspace_id=g.workspace_id
 WHERE g.workspace_id=w AND g.document_id=r.document_id AND g.article_revision_id=r.article_revision_id AND g.origin_kind='SYNTHESIS_NOTE' AND g.origin_id=n AND g.origin_revision_id=r.id
 AND g.projection_hash=r.projection_hash AND g.content_hash=r.content_hash AND ar.created_by_type='AGENT' AND ar.content_hash=r.content_hash
 AND encode(sha256(convert_to(ar.content,'UTF8')),'hex')=r.content_hash) THEN RETURN false; END IF;
 IF r.historical_republish_id IS NOT NULL THEN
  SELECT convert_from(payload,'UTF8')::jsonb INTO e FROM organizing.synthesis_historical_republish_event WHERE id=r.historical_republish_id AND workspace_id=w AND note_id=n AND kind='APPLY';
  SELECT * INTO selected FROM organizing.synthesis_revision WHERE id=(r.historical_republish->>'selected_revision_id')::uuid AND workspace_id=w AND note_id=n;
  RETURN COALESCE(e->'revision'->>'id'=r.id::text AND selected.revision_no<r.revision_no AND selected.items=r.items AND selected.manuscript IS NOT DISTINCT FROM r.manuscript
   AND selected.content_hash=r.content_hash AND organizing.synthesis_historical_revision_proven(w,n,selected.id),false);
 END IF;
 IF r.candidate_remerge_id IS NOT NULL THEN
  RETURN EXISTS(SELECT 1 FROM organizing.synthesis_candidate_remerge_event e WHERE e.id=r.candidate_remerge_id AND e.workspace_id=w AND e.note_id=n AND e.kind='APPLY'
   AND convert_from(e.payload,'UTF8')::jsonb->'revision'->>'id'=r.id::text AND convert_from(e.payload,'UTF8')::jsonb->'revision'->>'hash'=r.projection_hash);
 END IF;
 RETURN EXISTS(SELECT 1 FROM organizing.synthesis_apply_receipt a WHERE a.workspace_id=w AND a.workflow_run_id=r.workflow_run_id AND a.model_run_id=r.model_run_id AND a.source_event_id=r.source_event_id AND a.result->'revision_ids' @> jsonb_build_array(r.id::text));
END; $$;

CREATE FUNCTION organizing.validate_synthesis_historical_republish_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; a jsonb; selected organizing.synthesis_revision%ROWTYPE; latest organizing.synthesis_revision%ROWTYPE; doc core.document%ROWTYPE; cp jsonb; actual_scope jsonb; binding authoring.document_publication_binding%ROWTYPE; proposal change_control.proposal%ROWTYPE;
BEGIN
 v:=convert_from(NEW.payload,'UTF8')::jsonb;
 IF (v->'command'->>'workspace_id'=NEW.workspace_id::text AND v->'command'->>'note_id'=NEW.note_id::text AND v->'command'->>'idempotency_key'=NEW.idempotency_key) IS NOT TRUE THEN RAISE EXCEPTION 'historical command scope differs' USING ERRCODE='23514'; END IF;
 IF NEW.kind='BEGIN' THEN
  a:=v;
  IF (NEW.id=NEW.attempt_id AND a->>'id'=NEW.id::text) IS NOT TRUE THEN RAISE EXCEPTION 'invalid historical begin' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT convert_from(payload,'UTF8')::jsonb INTO a FROM organizing.synthesis_historical_republish_event WHERE id=NEW.attempt_id AND workspace_id=NEW.workspace_id AND note_id=NEW.note_id AND kind='BEGIN';
  IF a IS NULL OR (v->'command'->>'attempt_id'=NEW.attempt_id::text AND v->>'apply_id'=NEW.id::text AND (v->'command'->>'confirm_exact_restore')::boolean
   AND v->'command'->>'preview_fingerprint'=a->>'fingerprint' AND v->'command'->'retire_current_candidate'=a->'command'->'requires_retirement') IS NOT TRUE THEN RAISE EXCEPTION 'historical confirmation differs' USING ERRCODE='23514'; END IF;
 END IF;
 SELECT * INTO selected FROM organizing.synthesis_revision WHERE workspace_id=NEW.workspace_id AND note_id=NEW.note_id AND id=(a->'command'->>'selected_revision_id')::uuid;
 SELECT * INTO latest FROM organizing.synthesis_revision WHERE workspace_id=NEW.workspace_id AND note_id=NEW.note_id AND id=(a->'command'->>'expected_revision_id')::uuid;
 SELECT * INTO doc FROM core.document WHERE workspace_id=NEW.workspace_id AND id=latest.document_id FOR UPDATE;
 SELECT convert_from(payload,'UTF8')::jsonb INTO cp FROM organizing.synthesis_manuscript_capture WHERE id=(a->'capture'->>'id')::uuid AND workspace_id=NEW.workspace_id;
 SELECT COALESCE((SELECT jsonb_build_object('id',k.id,'scope_version',k.scope_version,'scope',s.scope) FROM organizing.knowledge_anchor k JOIN organizing.anchor_scope_revision s ON s.anchor_id=k.id AND s.workspace_id=k.workspace_id AND s.version=k.scope_version WHERE k.workspace_id=NEW.workspace_id AND k.note_id=NEW.note_id),'null'::jsonb) INTO actual_scope;
 IF (selected.id IS NOT NULL AND selected.document_id=latest.document_id AND selected.revision_no<=latest.revision_no
 AND organizing.synthesis_historical_revision_proven(NEW.workspace_id,NEW.note_id,selected.id)
 AND selected.projection_hash=a->'command'->>'selected_projection_hash' AND selected.projection_hash=a->'selected'->>'hash'
 AND selected.id::text=a->'selected'->>'id' AND latest.id::text=a->'latest'->>'id' AND latest.projection_hash=a->'latest'->>'hash'
 AND doc.id::text=a->'command'->>'expected_document_id' AND doc.version=(a->'command'->>'expected_document_version')::bigint
 AND doc.current_published_revision_id IS NOT DISTINCT FROM NULLIF(a->'command'->>'expected_published_revision_id','')::uuid
 AND NOT EXISTS(SELECT 1 FROM authoring.document_publication_reservation pending WHERE pending.workspace_id=NEW.workspace_id AND pending.document_id=doc.id AND pending.status='PENDING')
 AND NOT EXISTS(SELECT 1 FROM authoring.document_publication_binding busy WHERE busy.workspace_id=NEW.workspace_id AND busy.document_id=doc.id AND busy.status='RECOVERY_REQUIRED')
 AND actual_scope=a->'scope' AND cp=a->'capture' AND ((doc.current_published_revision_id IS NULL AND NOT (cp->>'exists')::boolean) OR (doc.current_published_revision_id IS NOT NULL AND (cp->>'exists')::boolean))
 AND cp->>'target_path'=doc.canonical_path AND cp->>'workspace_id'=NEW.workspace_id::text
 AND EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_root_identity ri JOIN core.workspace w ON w.id=ri.workspace_id WHERE ri.id::text=cp->>'root_grant_id' AND ri.workspace_id=NEW.workspace_id AND ri.root_fingerprint=cp->>'root_fingerprint' AND ri.binding_version=(cp->>'workspace_binding_version')::bigint AND w.root_fingerprint=ri.root_fingerprint AND w.binding_version=ri.binding_version AND w.status='active' AND w.availability='available' AND w.removed_at IS NULL)
 AND EXISTS(SELECT 1 FROM organizing.synthesis_note n WHERE n.id=NEW.note_id AND n.workspace_id=NEW.workspace_id AND n.current_revision_id=latest.id AND n.status NOT IN ('GENERATING','QUEUED','RECOVERY_REQUIRED') AND n.version=(a->'command'->>'expected_note_version')::bigint)
 AND EXISTS(SELECT 1 FROM core.article_revision ar WHERE ar.id=selected.article_revision_id AND ar.workspace_id=NEW.workspace_id AND ar.content=a->>'candidate' AND ar.content_hash=selected.content_hash)
 AND (doc.current_published_revision_id IS NULL OR EXISTS(SELECT 1 FROM core.article_revision pub WHERE pub.id=doc.current_published_revision_id AND pub.workspace_id=NEW.workspace_id AND pub.content=a->>'published_content' AND pub.content_hash=a->>'published_content_hash' AND authoring.publication_is_exact_current(NEW.workspace_id,doc.id,pub.id)))
 AND ((NOT (a->'command' ? 'selected_publication_id') AND NOT EXISTS(SELECT 1 FROM organizing.synthesis_proven_publication pp WHERE pp.workspace_id=NEW.workspace_id AND pp.note_id=NEW.note_id AND pp.revision_id=selected.id))
 OR EXISTS(SELECT 1 FROM organizing.synthesis_proven_publication pp WHERE pp.workspace_id=NEW.workspace_id AND pp.note_id=NEW.note_id AND pp.revision_id=selected.id AND pp.publication_id::text=a->'command'->>'selected_publication_id' AND pp.proposal_commit_id::text=a->'command'->>'selected_proposal_commit_id'))) IS NOT TRUE THEN RAISE EXCEPTION 'historical selected or current owner differs' USING ERRCODE='23514'; END IF;
 IF latest.article_revision_id IS DISTINCT FROM doc.current_published_revision_id THEN
  SELECT * INTO binding FROM authoring.document_publication_binding WHERE workspace_id=NEW.workspace_id AND id=(a->'command'->>'expected_publication_id')::uuid FOR UPDATE;
  SELECT * INTO proposal FROM change_control.proposal WHERE workspace_id=NEW.workspace_id AND id=binding.proposal_id FOR UPDATE;
  IF ((a->'command'->>'requires_retirement')::boolean AND binding.document_id=doc.id AND binding.article_revision_id=latest.article_revision_id
   AND (binding.status='PENDING' OR (binding.status='CLOSED' AND binding.error_code='AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION'))
   AND binding.proposal_id::text=a->'command'->>'expected_proposal_id' AND binding.proposal_revision_id::text=a->'command'->>'expected_proposal_revision_id'
   AND proposal.current_revision_id=binding.proposal_revision_id AND proposal.version=(a->'command'->>'expected_proposal_version')::bigint AND proposal.status IN ('ready_for_review','needs_revision') AND proposal.workflow_run_id IS NULL
   AND NOT EXISTS(SELECT 1 FROM change_control.approval WHERE proposal_id=proposal.id)
   AND NOT EXISTS(SELECT 1 FROM change_control.proposal_revision_dispatch WHERE proposal_id=proposal.id)
   AND NOT EXISTS(SELECT 1 FROM change_control.tool_authorization WHERE proposal_id=proposal.id)
   AND NOT EXISTS(SELECT 1 FROM change_control.writeback_execution WHERE proposal_id=proposal.id)
   AND NOT EXISTS(SELECT 1 FROM change_control.proposal_commit WHERE proposal_id=proposal.id)) IS NOT TRUE THEN RAISE EXCEPTION 'historical pending candidate cannot be retired' USING ERRCODE='23514'; END IF;
 ELSIF (a->'command'->>'requires_retirement')::boolean IS DISTINCT FROM false THEN RAISE EXCEPTION 'historical retirement does not match current owner' USING ERRCODE='23514'; END IF;
 IF NEW.kind='APPLY' AND (v->'revision'->>'workspace_id'=NEW.workspace_id::text AND v->'revision'->>'note_id'=NEW.note_id::text
 AND v->'revision'->>'document_id'=doc.id::text AND v->'revision'->>'parent_revision_id'=latest.id::text
 AND (v->'revision'->>'revision_no')::bigint=latest.revision_no+1 AND (v->'revision'->>'article_revision_no')::bigint=latest.article_revision_no+1
 AND v->'revision'->>'title'=selected.title AND v->'revision'->>'renderer_version'=selected.renderer_version
 AND v->'revision'->>'content_hash'=selected.content_hash AND v->'revision'->'items'=selected.items AND v->'revision'->'delta'=selected.delta
 AND v->'revision'->'manuscript' IS NOT DISTINCT FROM selected.manuscript AND NOT (v->'revision' ? 'remerge')
 AND v->'revision'->>'source_event_id'=selected.source_event_id::text AND v->'revision'->>'workflow_run_id'=selected.workflow_run_id::text AND v->'revision'->>'model_run_id'=selected.model_run_id::text
 AND v->'revision'->'historical_republish'=jsonb_strip_nulls(jsonb_build_object('attempt_id',NEW.attempt_id::text,'selected_revision_id',selected.id::text,'selected_publication_id',a->'command'->>'selected_publication_id','selected_proposal_commit_id',a->'command'->>'selected_proposal_commit_id'))) IS NOT TRUE THEN RAISE EXCEPTION 'historical copy differs from selected revision' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_historical_republish_validate BEFORE INSERT ON organizing.synthesis_historical_republish_event FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_historical_republish_event();

CREATE FUNCTION organizing.verify_historical_synthesis_revision(r organizing.synthesis_revision) RETURNS void LANGUAGE plpgsql AS $$
DECLARE e jsonb; a jsonb; selected organizing.synthesis_revision%ROWTYPE;
BEGIN
 SELECT convert_from(applied.payload,'UTF8')::jsonb,convert_from(b.payload,'UTF8')::jsonb INTO e,a
 FROM organizing.synthesis_historical_republish_event applied JOIN organizing.synthesis_historical_republish_event b ON b.id=applied.attempt_id AND b.workspace_id=applied.workspace_id AND b.note_id=applied.note_id AND b.kind='BEGIN'
 WHERE applied.id=r.historical_republish_id AND applied.workspace_id=r.workspace_id AND applied.note_id=r.note_id AND applied.kind='APPLY';
 SELECT * INTO selected FROM organizing.synthesis_revision WHERE id=(a->'command'->>'selected_revision_id')::uuid AND workspace_id=r.workspace_id AND note_id=r.note_id;
 IF (r.id::text=e->'revision'->>'id' AND r.article_revision_id::text=e->'revision'->>'article_revision_id' AND r.document_id=selected.document_id
 AND r.parent_revision_id::text=a->'command'->>'expected_revision_id' AND r.revision_no=(e->'revision'->>'revision_no')::bigint AND r.article_revision_no=(e->'revision'->>'article_revision_no')::bigint
 AND r.projection_hash=e->'revision'->>'hash' AND r.content_hash=selected.content_hash AND r.title=selected.title AND r.renderer_version=selected.renderer_version
 AND r.items=selected.items AND r.delta=selected.delta AND r.manuscript IS NOT DISTINCT FROM selected.manuscript
 AND r.manuscript_receipt_id IS NOT DISTINCT FROM selected.manuscript_receipt_id AND r.source_event_id=selected.source_event_id AND r.workflow_run_id=selected.workflow_run_id AND r.model_run_id=selected.model_run_id
 AND r.historical_republish=e->'revision'->'historical_republish' AND r.created_at=(e->'revision'->>'created_at')::timestamptz
 AND r.remerge IS NULL AND r.candidate_remerge_id IS NULL) IS NOT TRUE THEN RAISE EXCEPTION 'historical revision lacks exact selected copy and application' USING ERRCODE='23514'; END IF;
END; $$;

CREATE FUNCTION organizing.verify_synthesis_historical_republish_closure() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; a jsonb; r organizing.synthesis_revision%ROWTYPE; expected_sources jsonb; actual_sources jsonb;
BEGIN
 IF NEW.kind<>'APPLY' THEN RETURN NULL; END IF;
 v:=convert_from(NEW.payload,'UTF8')::jsonb;
 SELECT convert_from(payload,'UTF8')::jsonb INTO a FROM organizing.synthesis_historical_republish_event WHERE id=NEW.attempt_id AND workspace_id=NEW.workspace_id;
 SELECT * INTO r FROM organizing.synthesis_revision WHERE historical_republish_id=NEW.id AND workspace_id=NEW.workspace_id AND note_id=NEW.note_id;
 PERFORM organizing.verify_historical_synthesis_revision(r);
 SELECT COALESCE(jsonb_agg(to_jsonb(s)-'revision_id' ORDER BY (to_jsonb(s)-'revision_id')::text),'[]'::jsonb) INTO expected_sources FROM organizing.synthesis_revision_source s WHERE s.workspace_id=NEW.workspace_id AND s.revision_id=(a->'command'->>'selected_revision_id')::uuid;
 SELECT COALESCE(jsonb_agg(to_jsonb(s)-'revision_id' ORDER BY (to_jsonb(s)-'revision_id')::text),'[]'::jsonb) INTO actual_sources FROM organizing.synthesis_revision_source s WHERE s.workspace_id=NEW.workspace_id AND s.revision_id=r.id;
 IF actual_sources<>expected_sources OR NOT EXISTS(SELECT 1 FROM organizing.synthesis_note n
 JOIN authoring.generated_article_revision g ON g.workspace_id=n.workspace_id AND g.document_id=n.document_id AND g.article_revision_id=r.article_revision_id AND g.origin_kind='SYNTHESIS_NOTE' AND g.origin_id=n.id AND g.origin_revision_id=r.id AND g.projection_hash=r.projection_hash AND g.content_hash=r.content_hash
 JOIN core.article_revision ar ON ar.id=r.article_revision_id AND ar.workspace_id=r.workspace_id AND ar.document_id=r.document_id AND ar.content=a->>'candidate' AND ar.content_hash=r.content_hash
 JOIN authoring.document_publication_reservation p ON p.workspace_id=r.workspace_id AND p.document_id=r.document_id AND p.article_revision_id=r.article_revision_id AND p.content_hash=r.content_hash AND p.id=(v->>'reservation_id')::uuid AND p.idempotency_key=v->'publication'->>'idempotency_key'
 WHERE n.workspace_id=r.workspace_id AND n.id=r.note_id AND n.current_revision_id=r.id AND n.version=(a->'command'->>'expected_note_version')::bigint+1
 AND p.historical_republish_id=NEW.id AND p.merge_receipt_id IS NULL AND p.merge_capture_id::text=a->'capture'->>'id' AND p.base_version=CASE WHEN (a->'capture'->>'exists')::boolean THEN a->'capture'->>'content_hash' ELSE a->'capture'->>'absence_token' END
 AND p.merge_published_revision_id IS NOT DISTINCT FROM NULLIF(a->'command'->>'expected_published_revision_id','')::uuid
 AND p.merge_published_content_hash IS NOT DISTINCT FROM NULLIF(a->>'published_content_hash','')
 AND v->'publication'->>'note_id'=r.note_id::text AND v->'publication'->>'revision_id'=r.id::text AND v->'publication'->>'document_id'=r.document_id::text AND v->'publication'->>'article_revision_id'=r.article_revision_id::text AND v->'publication'->>'content_hash'=r.content_hash
 AND v->'publication'->>'idempotency_key'='synthesis-publish:'||r.id::text
 AND (NOT (a->'command'->>'requires_retirement')::boolean OR EXISTS(SELECT 1 FROM authoring.document_publication_binding oldb JOIN change_control.proposal oldp ON oldp.id=oldb.proposal_id AND oldp.workspace_id=oldb.workspace_id
 JOIN authoring.generated_publication_retirement retire ON retire.publication_id=oldb.id AND retire.workspace_id=oldb.workspace_id
 WHERE oldb.id=(a->'command'->>'expected_publication_id')::uuid AND oldb.workspace_id=r.workspace_id AND oldb.status='CLOSED' AND oldp.status='needs_revision' AND retire.origin_revision_id::text=a->'latest'->>'id')))
 THEN RAISE EXCEPTION 'historical republish candidate transaction is incomplete' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END; $$;
CREATE CONSTRAINT TRIGGER synthesis_historical_republish_closure AFTER INSERT ON organizing.synthesis_historical_republish_event DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_historical_republish_closure();


CREATE OR REPLACE FUNCTION organizing.validate_synthesis_revision_manuscript() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE r jsonb; a jsonb; p jsonb; l jsonb; trusted jsonb; generated jsonb;
BEGIN
 IF NEW.historical_republish_id IS NOT NULL THEN
 PERFORM organizing.verify_historical_synthesis_revision(NEW); RETURN NEW; END IF;

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


--
-- Name: validate_synthesis_source(); Type: FUNCTION; Schema: organizing; Owner: -
--



CREATE OR REPLACE FUNCTION organizing.verify_synthesis_manuscript_closure() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE a jsonb; p jsonb;
BEGIN
 IF NEW.historical_republish_id IS NOT NULL THEN
 PERFORM organizing.verify_historical_synthesis_revision(NEW); RETURN NEW; END IF;

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


--
-- Name: verify_synthesis_manuscript_review_closure(); Type: FUNCTION; Schema: organizing; Owner: -
--




--
-- Name: verify_synthesis_manuscript_runtime_closure(); Type: FUNCTION; Schema: organizing; Owner: -
--



CREATE OR REPLACE FUNCTION organizing.verify_synthesis_closure() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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
        IF NEW.historical_republish_id IS NOT NULL THEN
            PERFORM organizing.verify_historical_synthesis_revision(NEW);
            IF actual_sources<>expected_sources OR jsonb_array_length(actual_sources)>256 THEN RAISE EXCEPTION 'historical sources differ' USING ERRCODE='23514'; END IF;
            RETURN NULL;
        END IF;
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

CREATE OR REPLACE FUNCTION organizing.freeze_synthesis_source_profile() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    frozen_profile uuid;
BEGIN
    IF EXISTS(SELECT 1 FROM organizing.synthesis_revision WHERE id=NEW.revision_id AND workspace_id=NEW.workspace_id AND historical_republish_id IS NOT NULL) THEN
      SELECT prior.profile_revision_id INTO frozen_profile FROM organizing.synthesis_revision r
      JOIN organizing.synthesis_revision_source prior ON prior.revision_id=(r.historical_republish->>'selected_revision_id')::uuid AND prior.workspace_id=r.workspace_id AND prior.note_id=r.note_id
      WHERE r.id=NEW.revision_id AND r.workspace_id=NEW.workspace_id AND r.note_id=NEW.note_id
       AND prior.source_id=NEW.source_id AND prior.source_version_id=NEW.source_version_id AND prior.content_artifact_id=NEW.content_artifact_id
       AND prior.parse_projection_id=NEW.parse_projection_id AND prior.source_span_id=NEW.source_span_id AND prior.content_hash=NEW.content_hash AND prior.excerpt_hash=NEW.excerpt_hash AND prior.title=NEW.title;
      IF NOT FOUND OR NEW.profile_revision_id IS DISTINCT FROM frozen_profile THEN RAISE EXCEPTION 'historical source profile must equal selected snapshot including NULL' USING ERRCODE='23514'; END IF;
      RETURN NEW;
    END IF;
    -- An inherited exact source keeps its prior knowledge-point identities.
    SELECT prior.profile_revision_id INTO frozen_profile
    FROM organizing.synthesis_revision revision
    JOIN organizing.synthesis_revision_source prior
      ON prior.revision_id=revision.parent_revision_id
     AND prior.workspace_id=revision.workspace_id AND prior.note_id=revision.note_id
    WHERE revision.id=NEW.revision_id AND revision.workspace_id=NEW.workspace_id
      AND revision.note_id=NEW.note_id AND prior.source_id=NEW.source_id
      AND prior.source_version_id=NEW.source_version_id
      AND prior.parse_projection_id=NEW.parse_projection_id
      AND prior.source_span_id=NEW.source_span_id
      AND prior.content_artifact_id=NEW.content_artifact_id
      AND prior.content_hash=NEW.content_hash AND prior.excerpt_hash=NEW.excerpt_hash;

    IF frozen_profile IS NULL THEN
        -- Any committed immutable revision remains valid for its exact source
        -- and parser projection, even while the mutable profile is rebuilding.
        SELECT revision.id INTO frozen_profile
        FROM learning.document_knowledge_profile_revision revision
        WHERE revision.workspace_id=NEW.workspace_id
          AND revision.source_version_id=NEW.source_version_id
          AND revision.parse_projection_id=NEW.parse_projection_id
        ORDER BY revision.created_at DESC,revision.id DESC LIMIT 1;
    END IF;
    IF NEW.profile_revision_id IS NOT NULL
       AND NEW.profile_revision_id IS DISTINCT FROM frozen_profile THEN
        RAISE EXCEPTION 'synthesis knowledge snapshot differs from owner profile'
            USING ERRCODE='23514';
    END IF;
    NEW.profile_revision_id := frozen_profile;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION organizing.validate_synthesis_revision_body_reference() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE current_item jsonb; upstream_item jsonb; expected_ref jsonb; parent_id uuid;
BEGIN
    SELECT item,COALESCE((r.historical_republish->>'selected_revision_id')::uuid,r.parent_revision_id) INTO current_item,parent_id
      FROM organizing.synthesis_revision r CROSS JOIN LATERAL jsonb_array_elements(r.items) item
     WHERE r.id=NEW.revision_id AND r.workspace_id=NEW.workspace_id AND r.note_id=NEW.note_id
       AND item->>'id'=NEW.item_id::text;
    expected_ref := jsonb_build_object('workspace_id',NEW.workspace_id,'note_id',NEW.upstream_note_id,
      'revision_id',NEW.upstream_revision_id,'item_id',NEW.upstream_item_id,
      'publication_id',NEW.publication_id,'projection_hash',NEW.upstream_projection_hash);
    IF (current_item->'body_reference'=expected_ref) IS NOT TRUE THEN
        RAISE EXCEPTION 'body reference must be saved in its exact revision item' USING ERRCODE='23514';
    END IF;
    PERFORM organizing.verify_synthesis_published_bindings(jsonb_build_array(expected_ref-'item_id'));
    SELECT item INTO upstream_item
      FROM organizing.synthesis_revision r CROSS JOIN LATERAL jsonb_array_elements(r.items) item
     WHERE r.id=NEW.upstream_revision_id AND r.workspace_id=NEW.workspace_id AND r.note_id=NEW.upstream_note_id
       AND item->>'id'=NEW.upstream_item_id::text;
    IF upstream_item IS NULL THEN
        RAISE EXCEPTION 'referenced published item is missing' USING ERRCODE='23514';
    END IF;
    -- A local resolution or additional evidence may extend an already-cited
    -- item. The original published reference remains an immutable snapshot.
    IF NOT EXISTS (SELECT 1 FROM organizing.synthesis_revision_body_reference prior
      WHERE prior.revision_id=parent_id AND prior.workspace_id=NEW.workspace_id AND prior.note_id=NEW.note_id
        AND prior.item_id=NEW.item_id AND prior.upstream_revision_id=NEW.upstream_revision_id
        AND prior.upstream_note_id=NEW.upstream_note_id AND prior.upstream_item_id=NEW.upstream_item_id
        AND prior.publication_id=NEW.publication_id AND prior.upstream_projection_hash=NEW.upstream_projection_hash)
      AND (current_item-ARRAY['id','body_reference']) IS DISTINCT FROM (upstream_item-ARRAY['id','body_reference']) THEN
        RAISE EXCEPTION 'new body inclusion must retain the complete published item' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION authoring.validate_publication_merge_baseline() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE proof record; document_record core.document%ROWTYPE;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.historical_republish_id IS DISTINCT FROM OLD.historical_republish_id OR NEW.merge_receipt_id IS DISTINCT FROM OLD.merge_receipt_id
     OR NEW.merge_capture_id IS DISTINCT FROM OLD.merge_capture_id
     OR NEW.merge_published_revision_id IS DISTINCT FROM OLD.merge_published_revision_id
     OR NEW.merge_published_content_hash IS DISTINCT FROM OLD.merge_published_content_hash THEN
   RAISE EXCEPTION 'publication merge baseline is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO proof FROM organizing.synthesis_publication_merge_baseline
  WHERE workspace_id=NEW.workspace_id AND document_id=NEW.document_id AND article_revision_id=NEW.article_revision_id;
 IF NOT FOUND THEN
  IF NEW.historical_republish_id IS NOT NULL OR NEW.merge_receipt_id IS NOT NULL OR EXISTS(SELECT 1 FROM organizing.synthesis_revision
       WHERE workspace_id=NEW.workspace_id AND article_revision_id=NEW.article_revision_id AND renderer_version='synthesis-markdown/v2') THEN
   RAISE EXCEPTION 'publication merge proof is missing' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO document_record FROM core.document WHERE id=NEW.document_id AND workspace_id=NEW.workspace_id FOR UPDATE;
 IF (proof.valid AND NEW.merge_receipt_id IS NOT DISTINCT FROM proof.receipt_id AND NEW.historical_republish_id IS NOT DISTINCT FROM proof.historical_republish_id AND NEW.merge_capture_id=proof.capture_id
     AND NEW.merge_published_revision_id IS NOT DISTINCT FROM proof.published_revision_id
     AND NEW.merge_published_content_hash IS NOT DISTINCT FROM proof.published_content_hash
     AND NEW.base_version=proof.file_base AND NEW.target_path=proof.target_path AND NEW.content_hash=proof.content_hash
     AND document_record.canonical_path=proof.target_path AND document_record.version=proof.document_version
     AND document_record.current_published_revision_id IS NOT DISTINCT FROM proof.published_revision_id
     AND NOT EXISTS(SELECT 1 FROM core.article_revision newer JOIN core.article_revision candidate ON candidate.id=NEW.article_revision_id
         WHERE newer.document_id=NEW.document_id AND newer.workspace_id=NEW.workspace_id AND newer.revision_no>candidate.revision_no)
     AND ((NEW.target_mode='CREATE_ONLY' AND proof.published_revision_id IS NULL AND document_record.lifecycle_status='DRAFT')
          OR (NEW.target_mode='REPLACE' AND proof.published_revision_id IS NOT NULL AND document_record.lifecycle_status='PUBLISHED'
              AND authoring.publication_is_exact_current(NEW.workspace_id,NEW.document_id,proof.published_revision_id)))) IS NOT TRUE THEN
  RAISE EXCEPTION 'publication merge baseline differs from proven owner facts' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END; $$;


--
-- Name: validate_publication_reservation_write(); Type: FUNCTION; Schema: authoring; Owner: -
--



CREATE OR REPLACE FUNCTION authoring.publication_replaces_revision(checked_reservation_id uuid, checked_workspace_id uuid, checked_document_id uuid, checked_revision_id uuid, checked_content_hash text) RETURNS boolean
    LANGUAGE sql STABLE
    AS $$
 SELECT EXISTS(SELECT 1 FROM authoring.document_publication_reservation r
 WHERE r.id=checked_reservation_id AND r.workspace_id=checked_workspace_id AND r.document_id=checked_document_id
 AND r.target_mode='REPLACE' AND
 ((r.merge_receipt_id IS NULL AND r.historical_republish_id IS NULL AND r.base_version=checked_content_hash)
  OR ((r.merge_receipt_id IS NOT NULL OR r.historical_republish_id IS NOT NULL) AND r.merge_published_revision_id=checked_revision_id AND r.merge_published_content_hash=checked_content_hash)));
$$;

CREATE OR REPLACE FUNCTION authoring.validate_generated_retirement_insert() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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
      SELECT 1 FROM (SELECT workspace_id,note_id,attempt_id,kind FROM organizing.synthesis_candidate_remerge_event UNION ALL SELECT workspace_id,note_id,attempt_id,kind FROM organizing.synthesis_historical_republish_event) e
      JOIN (SELECT id,workspace_id,note_id,kind,payload FROM organizing.synthesis_candidate_remerge_event UNION ALL SELECT id,workspace_id,note_id,kind,payload FROM organizing.synthesis_historical_republish_event) attempt ON attempt.id=e.attempt_id AND attempt.workspace_id=e.workspace_id AND attempt.note_id=e.note_id AND attempt.kind='BEGIN'
      CROSS JOIN LATERAL(SELECT convert_from(attempt.payload,'UTF8')::jsonb a) payload
      WHERE e.kind='APPLY' AND e.workspace_id=NEW.workspace_id AND e.note_id=NEW.origin_id
       AND NEW.origin_kind='SYNTHESIS_NOTE' AND a->'command'->>'expected_publication_id'=NEW.publication_id::text
       AND a->'command'->>'expected_revision_id'=NEW.origin_revision_id::text
       AND a->'command'->>'expected_document_id'=NEW.document_id::text
       AND (a->'command'->>'expected_document_version')::bigint=NEW.expected_document_version
       AND (a->'command'->>'expected_proposal_version')::bigint=NEW.expected_proposal_version
       AND COALESCE(a->'original',a->'latest')->>'article_revision_id'=NEW.article_revision_id::text
       AND COALESCE(a->'original',a->'latest')->>'hash'=NEW.projection_hash
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
           false) AS valid, NULL::uuid AS historical_republish_id
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
WHERE r.renderer_version='synthesis-markdown/v2' AND r.candidate_remerge_id IS NULL AND r.historical_republish_id IS NULL
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
  AND pub.content_hash=a->>'published_content_hash',false) AS valid, NULL::uuid AS historical_republish_id
 FROM organizing.synthesis_revision r
 JOIN organizing.synthesis_candidate_remerge_event e ON e.id=r.candidate_remerge_id AND e.workspace_id=r.workspace_id AND e.note_id=r.note_id AND e.kind='APPLY'
 JOIN organizing.synthesis_candidate_remerge_event b ON b.id=e.attempt_id AND b.workspace_id=e.workspace_id AND b.note_id=e.note_id AND b.kind='BEGIN'
 CROSS JOIN LATERAL(SELECT convert_from(e.payload,'UTF8')::jsonb v,convert_from(b.payload,'UTF8')::jsonb a) x
 JOIN authoring.generated_article_revision generated ON generated.article_revision_id=r.article_revision_id AND generated.workspace_id=r.workspace_id AND generated.document_id=r.document_id AND generated.origin_id=r.note_id AND generated.origin_revision_id=r.id AND generated.projection_hash=r.projection_hash AND generated.content_hash=r.content_hash
 JOIN core.article_revision ar ON ar.id=r.article_revision_id AND ar.workspace_id=r.workspace_id AND ar.document_id=r.document_id AND ar.content_hash=r.content_hash AND ar.created_by_type='AGENT'
 JOIN core.article_revision pub ON pub.workspace_id=r.workspace_id AND pub.document_id=r.document_id AND pub.id=(a->>'published_revision_id')::uuid
 WHERE r.renderer_version='synthesis-markdown/v2'
UNION ALL
SELECT r.workspace_id,r.document_id,r.article_revision_id,r.content_hash,r.projection_hash,
 NULL::uuid AS receipt_id,(a->'capture'->>'id')::uuid AS capture_id,
 a->'capture'->>'target_path' AS target_path,CASE WHEN (a->'capture'->>'exists')::boolean THEN a->'capture'->>'content_hash' ELSE a->'capture'->>'absence_token' END AS file_base,
 NULLIF(a->'command'->>'expected_published_revision_id','')::uuid AS published_revision_id,NULLIF(a->>'published_content_hash','') AS published_content_hash,
 g.document_version,
 COALESCE(r.projection_hash=v->'revision'->>'hash' AND r.content_hash=v->'revision'->>'content_hash' AND ar.content=a->>'candidate'
  AND g.expected_document_version=(a->'command'->>'expected_document_version')::bigint AND g.parent_revision_id::text=a->'latest'->>'article_revision_id'
  AND r.historical_republish=v->'revision'->'historical_republish',false) AS valid,e.id AS historical_republish_id
FROM organizing.synthesis_revision r
JOIN organizing.synthesis_historical_republish_event e ON e.id=r.historical_republish_id AND e.workspace_id=r.workspace_id AND e.note_id=r.note_id AND e.kind='APPLY'
JOIN organizing.synthesis_historical_republish_event b ON b.id=e.attempt_id AND b.workspace_id=e.workspace_id AND b.note_id=e.note_id AND b.kind='BEGIN'
CROSS JOIN LATERAL(SELECT convert_from(e.payload,'UTF8')::jsonb v,convert_from(b.payload,'UTF8')::jsonb a) x
JOIN authoring.generated_article_revision g ON g.workspace_id=r.workspace_id AND g.document_id=r.document_id AND g.article_revision_id=r.article_revision_id AND g.origin_id=r.note_id AND g.origin_revision_id=r.id AND g.projection_hash=r.projection_hash AND g.content_hash=r.content_hash
JOIN core.article_revision ar ON ar.workspace_id=r.workspace_id AND ar.id=r.article_revision_id AND ar.created_by_type='AGENT';
