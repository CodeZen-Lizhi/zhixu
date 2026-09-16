-- Preparation failures retain their own bounded retry schedule, without
-- mutating the frozen catalog or claiming that point selection completed.
CREATE TABLE organizing.synthesis_goal_selection_preparation (
 batch_id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL,
 next_check_at timestamptz NOT NULL,
 error_code text NOT NULL CHECK(error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 FOREIGN KEY(batch_id,workspace_id) REFERENCES organizing.synthesis_goal_catalog_batch(id,workspace_id)
);
CREATE TABLE organizing.synthesis_goal_selection_retry (
 workspace_id uuid NOT NULL,
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128),
 selection_id uuid NOT NULL,
 expected_version bigint NOT NULL CHECK(expected_version>0),
 result_version bigint NOT NULL CHECK(result_version=expected_version+1),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace_id,idempotency_key),
 FOREIGN KEY(selection_id,workspace_id) REFERENCES organizing.synthesis_goal_selection(id,workspace_id)
);
CREATE TRIGGER goal_selection_retry_immutable BEFORE UPDATE OR DELETE ON organizing.synthesis_goal_selection_retry FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER goal_selection_retry_truncate BEFORE TRUNCATE ON organizing.synthesis_goal_selection_retry FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION organizing.guard_goal_selection() RETURNS trigger LANGUAGE plpgsql AS $$
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
 -- Explicit retry only after a known failed attempt; uncertain execution
 -- never replays a provider automatically. A receipt makes retries idempotent.
 IF OLD.status='FAILED' AND OLD.retryable AND NEW.status='PENDING' AND NEW.version=OLD.version+1
 AND NEW.scheduled_workflow_id IS NULL AND NEW.workflow_run_id IS NULL AND NEW.node_run_id IS NULL
 AND NEW.node_attempt_id IS NULL AND NEW.model_input_hash IS NULL THEN
  IF OLD.scheduled_workflow_id IS NOT NULL AND NOT EXISTS(
   SELECT 1 FROM workflow.run WHERE id=OLD.scheduled_workflow_id AND workspace_id=OLD.workspace_id AND status IN ('succeeded','failed','cancelled')) THEN
   RAISE EXCEPTION 'selection retry waits for its old workflow to terminate' USING ERRCODE='55000'; END IF;
  IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_goal_selection_retry receipt
   WHERE receipt.workspace_id=OLD.workspace_id AND receipt.selection_id=OLD.id AND receipt.expected_version=OLD.version AND receipt.result_version=NEW.version) THEN
   RAISE EXCEPTION 'selection retry requires an exact receipt' USING ERRCODE='23514'; END IF;
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
