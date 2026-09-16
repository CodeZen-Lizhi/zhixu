-- A goal has one durable generation identity, independent of the real source
-- event retained as provenance. Existing source and fusion keys remain valid.
ALTER TABLE organizing.synthesis_processing ADD COLUMN goal_request_id uuid;
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT fk_synthesis_processing_goal
 FOREIGN KEY(goal_request_id,workspace_id) REFERENCES organizing.synthesis_goal_request(id,workspace_id) ON DELETE RESTRICT;
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT uq_synthesis_processing_goal UNIQUE(goal_request_id);
ALTER TABLE organizing.synthesis_processing DROP CONSTRAINT synthesis_processing_processing_key_check;
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT synthesis_processing_processing_key_check
 CHECK(processing_key ~ '^synthesis(-(fusion|goal))?:[0-9a-f]{64}$');
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT synthesis_processing_goal_shape CHECK (
 (goal_request_id IS NULL AND processing_key !~ '^synthesis-goal:') OR
 (goal_request_id IS NOT NULL AND fusion_request_id IS NULL AND fusion_trigger IS NULL
  AND processing_key='synthesis-goal:' || encode(sha256(convert_to(workspace_id::text || ':' || goal_request_id::text,'UTF8')),'hex')
  AND request_hash=substring(processing_key FROM 16)
  AND status NOT IN ('SKIPPED','NO_CHANGE') AND jsonb_array_length(revision_ids)<=1));

CREATE FUNCTION organizing.guard_synthesis_goal_processing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.goal_request_id IS DISTINCT FROM OLD.goal_request_id THEN
  RAISE EXCEPTION 'synthesis goal identity is immutable' USING ERRCODE='55000';
 END IF;
 IF NEW.goal_request_id IS NULL THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_goal_request r
   WHERE r.id=NEW.goal_request_id AND r.workspace_id=NEW.workspace_id AND r.status='CATALOG_READY'
     AND r.catalog_batches=(SELECT count(*) FROM organizing.synthesis_goal_catalog_batch b
       JOIN organizing.synthesis_goal_selection_manifest m ON m.batch_id=b.id AND m.workspace_id=b.workspace_id AND m.sealed
       WHERE b.request_id=r.id AND b.workspace_id=r.workspace_id))
 OR EXISTS(SELECT 1 FROM organizing.synthesis_goal_selection s
   WHERE s.request_id=NEW.goal_request_id AND s.workspace_id=NEW.workspace_id AND s.status<>'SUCCEEDED')
 OR NOT EXISTS(SELECT 1 FROM organizing.synthesis_goal_selection s
   JOIN organizing.synthesis_goal_catalog_item i ON i.batch_id=s.catalog_batch_id AND i.workspace_id=s.workspace_id AND i.ordinal=s.source_ordinal+1
   WHERE s.request_id=NEW.goal_request_id AND s.workspace_id=NEW.workspace_id AND s.status='SUCCEEDED'
    AND i.source_id=NEW.source_id AND i.source_version_id=NEW.source_version_id
    AND i.content_artifact_id=NEW.content_artifact_id AND i.parse_projection_id=NEW.parse_projection_id AND i.content_hash=NEW.source_content_hash
    AND jsonb_array_length(convert_from(s.model_output,'UTF8')::jsonb->'selections')>0)
 THEN RAISE EXCEPTION 'synthesis goal requires complete selections and a selected real seed' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER synthesis_goal_processing_guard BEFORE INSERT OR UPDATE ON organizing.synthesis_processing
 FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_goal_processing();

-- Queue identity and frozen identity must agree with the processing owner,
-- including on explicit retries and publication recovery runs.
CREATE FUNCTION organizing.guard_synthesis_goal_execution() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE goal_id uuid; queued jsonb; goal_text text;
BEGIN
 SELECT p.goal_request_id,r.input,g.goal_text INTO STRICT goal_id,queued,goal_text
 FROM organizing.synthesis_processing p JOIN workflow.run r ON r.id=NEW.workflow_run_id AND r.workspace_id=p.workspace_id
 LEFT JOIN organizing.synthesis_goal_request g ON g.id=p.goal_request_id AND g.workspace_id=p.workspace_id
 WHERE p.id=NEW.processing_id AND p.workspace_id=NEW.workspace_id;
 IF goal_id IS NULL THEN
  IF queued ? 'goal_request_id' OR (NEW.input_document IS NOT NULL AND NEW.input_document ? 'goal') THEN
   RAISE EXCEPTION 'ordinary synthesis cannot acquire a goal binding' USING ERRCODE='23514'; END IF;
 ELSE
  IF (queued->>'goal_request_id'=goal_id::text) IS NOT TRUE
   OR (NEW.input_document IS NOT NULL AND (
    (NEW.input_document->'goal'->>'request_id'=goal_id::text) IS NOT TRUE
    OR (NEW.input_document->'goal'->>'text'=goal_text) IS NOT TRUE
    OR (jsonb_typeof(NEW.input_document->'goal'->'materials')='array') IS NOT TRUE
    OR (jsonb_array_length(NEW.input_document->'goal'->'materials') BETWEEN 1 AND 256) IS NOT TRUE
    OR (jsonb_array_length(NEW.input_document->'notes')=0) IS NOT TRUE))
  THEN RAISE EXCEPTION 'synthesis goal execution binding changed' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER synthesis_goal_execution_guard BEFORE INSERT OR UPDATE ON organizing.synthesis_execution
 FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_goal_execution();

-- The existing (workspace,processing) receipt and unique goal processing imply
-- one candidate per goal. Do not duplicate the goal foreign key in the receipt.
CREATE FUNCTION organizing.guard_synthesis_goal_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM organizing.synthesis_processing p WHERE p.id=NEW.processing_id AND p.workspace_id=NEW.workspace_id AND p.goal_request_id IS NOT NULL)
 OR NEW.processing_key ~ '^synthesis-goal:' THEN
  IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_processing p
    JOIN organizing.synthesis_execution e ON e.processing_id=p.id AND e.workspace_id=p.workspace_id AND e.workflow_run_id=NEW.workflow_run_id
    WHERE p.id=NEW.processing_id AND p.workspace_id=NEW.workspace_id AND p.goal_request_id IS NOT NULL
      AND p.processing_key=NEW.processing_key AND p.source_event_id=NEW.source_event_id
      AND e.input_document->'goal'->>'request_id'=p.goal_request_id::text
      AND NEW.changed AND jsonb_array_length(NEW.result->'revision_ids')=1)
  THEN RAISE EXCEPTION 'synthesis goal receipt requires its exact generation identity' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER synthesis_goal_receipt_guard BEFORE INSERT ON organizing.synthesis_apply_receipt
 FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_goal_receipt();
