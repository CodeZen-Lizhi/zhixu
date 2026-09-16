ALTER TABLE organizing.anchor_recommendation_request ADD COLUMN scheduled_workflow_run_id uuid;
ALTER TABLE organizing.anchor_recommendation_request ADD CONSTRAINT uq_anchor_recommendation_scheduled_run UNIQUE(scheduled_workflow_run_id);
ALTER TABLE organizing.anchor_recommendation_request ADD CONSTRAINT fk_anchor_recommendation_scheduled_run
  FOREIGN KEY(scheduled_workflow_run_id,workspace_id) REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT;

CREATE FUNCTION organizing.guard_anchor_recommendation_schedule() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='UPDATE' AND NEW.status='PENDING' AND OLD.status IN ('FAILED','RECOVERY_REQUIRED') THEN
    NEW.scheduled_workflow_run_id := NULL;
  END IF;
  IF TG_OP='UPDATE' AND OLD.scheduled_workflow_run_id IS NOT NULL
     AND NEW.scheduled_workflow_run_id IS DISTINCT FROM OLD.scheduled_workflow_run_id
     AND NOT (NEW.scheduled_workflow_run_id IS NULL AND NEW.status='PENDING') THEN
    RAISE EXCEPTION 'anchor recommendation schedule is immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.scheduled_workflow_run_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM workflow.run r WHERE r.id=NEW.scheduled_workflow_run_id AND r.workspace_id=NEW.workspace_id
      AND r.input->>'request_id'=NEW.id::text AND r.idempotency_key='anchor-recommendation-start:'||NEW.id::text||':'||(r.input->>'expected_version')
      AND (r.input->>'expected_version')::bigint>0
  ) THEN RAISE EXCEPTION 'anchor recommendation schedule binding is invalid' USING ERRCODE='23514'; END IF;
  RETURN NEW;
END; $$;
CREATE TRIGGER anchor_recommendation_schedule_guard BEFORE INSERT OR UPDATE ON organizing.anchor_recommendation_request
FOR EACH ROW EXECUTE FUNCTION organizing.guard_anchor_recommendation_schedule();
