-- Reviewed source associations are consumed through a separate durable queue.
-- They retain the original source-ready event rather than reopening it.
CREATE TABLE organizing.anchor_fusion_request (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    anchor_id uuid NOT NULL,
    note_id uuid NOT NULL,
    proposal_id uuid NOT NULL,
    scope_version bigint NOT NULL CHECK (scope_version>0),
    source_event_id uuid NOT NULL REFERENCES workflow.outbox_event(id) ON DELETE RESTRICT,
    source_id uuid NOT NULL,
    source_version_id uuid NOT NULL,
    content_artifact_id uuid NOT NULL,
    parse_projection_id uuid NOT NULL,
    source_content_hash text NOT NULL CHECK (source_content_hash ~ '^[0-9a-f]{64}$'),
    ingestion_attempt_id uuid NOT NULL REFERENCES ingestion.attempt(id) ON DELETE RESTRICT,
    source_occurred_at timestamptz NOT NULL,
    allowed_sources jsonb NOT NULL CHECK (jsonb_typeof(allowed_sources)='array' AND jsonb_array_length(allowed_sources) BETWEEN 1 AND 256),
    processing_id uuid,
    status text NOT NULL CHECK (status IN ('PENDING','DISPATCHED','STALE')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL CHECK (updated_at>=created_at),
    CONSTRAINT uq_anchor_fusion_request_workspace UNIQUE(id,workspace_id),
    CONSTRAINT uq_anchor_fusion_request_proposal_source UNIQUE(workspace_id,proposal_id,source_version_id,parse_projection_id),
    CONSTRAINT uq_anchor_fusion_request_processing UNIQUE(processing_id),
    CONSTRAINT fk_anchor_fusion_anchor FOREIGN KEY(anchor_id,workspace_id) REFERENCES organizing.knowledge_anchor(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_fusion_note FOREIGN KEY(note_id,workspace_id) REFERENCES organizing.synthesis_note(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_fusion_proposal FOREIGN KEY(proposal_id,workspace_id,anchor_id) REFERENCES organizing.anchor_proposal(id,workspace_id,anchor_id) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_fusion_source FOREIGN KEY(source_version_id,source_id) REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_fusion_projection FOREIGN KEY(source_version_id,parse_projection_id,workspace_id) REFERENCES ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_fusion_processing FOREIGN KEY(processing_id,workspace_id) REFERENCES organizing.synthesis_processing(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT anchor_fusion_lifecycle CHECK ((status='PENDING' AND processing_id IS NULL) OR (status='DISPATCHED' AND processing_id IS NOT NULL) OR (status='STALE' AND processing_id IS NULL))
);
CREATE INDEX idx_anchor_fusion_request_pending ON organizing.anchor_fusion_request(created_at,id) WHERE status='PENDING';

ALTER TABLE organizing.synthesis_processing ADD COLUMN fusion_request_id uuid;
ALTER TABLE organizing.synthesis_processing ADD COLUMN fusion_trigger jsonb;
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT fk_synthesis_processing_fusion_request
    FOREIGN KEY(fusion_request_id,workspace_id) REFERENCES organizing.anchor_fusion_request(id,workspace_id) ON DELETE RESTRICT;
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT uq_synthesis_processing_fusion_request UNIQUE(fusion_request_id);
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT synthesis_processing_fusion_shape CHECK ((fusion_request_id IS NULL AND fusion_trigger IS NULL) OR (fusion_request_id IS NOT NULL AND jsonb_typeof(fusion_trigger)='object' AND fusion_trigger->>'request_id'=fusion_request_id::text));
ALTER TABLE organizing.synthesis_processing DROP CONSTRAINT uq_synthesis_processing_event;
ALTER TABLE organizing.synthesis_processing DROP CONSTRAINT synthesis_processing_processing_key_check;
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT synthesis_processing_processing_key_check CHECK (processing_key ~ '^synthesis(-fusion)?:[0-9a-f]{64}$');
ALTER TABLE organizing.synthesis_processing DROP CONSTRAINT synthesis_processing_lifecycle;
ALTER TABLE organizing.synthesis_processing ADD CONSTRAINT synthesis_processing_lifecycle CHECK (
    updated_at>=created_at AND (completed_at IS NULL OR completed_at=updated_at)
    AND ((status IN ('PENDING','RUNNING') AND workflow_run_id IS NOT NULL AND completed_at IS NULL AND error_code IS NULL AND NOT retryable AND revision_ids='[]'::jsonb)
      OR (status='SKIPPED' AND workflow_run_id IS NULL AND model_run_id IS NULL AND completed_at IS NOT NULL AND error_code IS NULL AND NOT retryable AND revision_ids='[]'::jsonb)
      OR (status='SUCCEEDED' AND workflow_run_id IS NOT NULL AND completed_at IS NOT NULL AND error_code IS NULL AND NOT retryable AND jsonb_array_length(revision_ids)>0)
      OR (status='NO_CHANGE' AND workflow_run_id IS NOT NULL AND completed_at IS NOT NULL AND error_code IS NULL AND NOT retryable AND revision_ids='[]'::jsonb)
      OR (status IN ('FAILED','RECOVERY_REQUIRED') AND workflow_run_id IS NOT NULL AND completed_at IS NOT NULL AND error_code IS NOT NULL AND revision_ids='[]'::jsonb AND (status<>'RECOVERY_REQUIRED' OR NOT retryable)))
);

CREATE OR REPLACE FUNCTION organizing.guard_anchor_fusion_request()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.status<>'PENDING' OR NEW.processing_id IS NOT NULL OR NOT EXISTS (
            SELECT 1 FROM organizing.anchor_proposal p JOIN organizing.anchor_decision d ON d.proposal_id=p.id AND d.workspace_id=p.workspace_id
            JOIN organizing.knowledge_anchor a ON a.id=p.anchor_id AND a.workspace_id=p.workspace_id
            JOIN workflow.outbox_event e ON e.id=NEW.source_event_id AND e.workspace_id=NEW.workspace_id
            WHERE p.id=NEW.proposal_id AND p.workspace_id=NEW.workspace_id AND p.anchor_id=NEW.anchor_id AND p.kind='SOURCE_ASSOCIATION'
              AND p.scope_version=NEW.scope_version AND d.decision='ACCEPTED' AND a.note_id=NEW.note_id
              AND e.event_type='ingestion.source.ready' AND e.payload->>'source_id'=NEW.source_id::text
              AND e.payload->>'source_version_id'=NEW.source_version_id::text AND e.payload->>'content_artifact_id'=NEW.content_artifact_id::text
              AND e.payload->>'parse_projection_id'=NEW.parse_projection_id::text AND e.payload->>'content_hash'=NEW.source_content_hash
              AND e.payload->>'ingestion_attempt_id'=NEW.ingestion_attempt_id::text AND e.occurred_at=NEW.source_occurred_at
        ) THEN RAISE EXCEPTION 'anchor fusion request binding is invalid' USING ERRCODE='23514'; END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='UPDATE' AND NEW.status='DISPATCHED' AND NOT EXISTS (SELECT 1 FROM organizing.synthesis_processing p WHERE p.id=NEW.processing_id AND p.workspace_id=NEW.workspace_id AND p.fusion_request_id=NEW.id) THEN RAISE EXCEPTION 'anchor fusion processing is missing' USING ERRCODE='23514'; END IF;
    IF TG_OP='DELETE' OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.anchor_id<>OLD.anchor_id OR NEW.note_id<>OLD.note_id
       OR NEW.proposal_id<>OLD.proposal_id OR NEW.scope_version<>OLD.scope_version OR NEW.source_event_id<>OLD.source_event_id
       OR NEW.source_id<>OLD.source_id OR NEW.source_version_id<>OLD.source_version_id OR NEW.content_artifact_id<>OLD.content_artifact_id
       OR NEW.parse_projection_id<>OLD.parse_projection_id OR NEW.source_content_hash<>OLD.source_content_hash OR NEW.ingestion_attempt_id<>OLD.ingestion_attempt_id
       OR NEW.source_occurred_at<>OLD.source_occurred_at OR NEW.allowed_sources<>OLD.allowed_sources OR NEW.created_at<>OLD.created_at OR NEW.updated_at<OLD.updated_at
       OR (OLD.status<>'PENDING') THEN RAISE EXCEPTION 'anchor fusion request is immutable' USING ERRCODE='55000'; END IF;
    RETURN NEW;
END; $$;
CREATE TRIGGER anchor_fusion_request_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.anchor_fusion_request FOR EACH ROW EXECUTE FUNCTION organizing.guard_anchor_fusion_request();

CREATE FUNCTION organizing.guard_synthesis_fusion_processing()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.fusion_request_id IS NULL AND NEW.fusion_trigger IS NULL THEN RETURN NEW; END IF;
    IF TG_OP='UPDATE' AND (NEW.fusion_request_id IS DISTINCT FROM OLD.fusion_request_id OR NEW.fusion_trigger IS DISTINCT FROM OLD.fusion_trigger) THEN
        RAISE EXCEPTION 'synthesis fusion binding is immutable' USING ERRCODE='55000';
    END IF;
    IF NOT (
        NEW.fusion_request_id IS NOT NULL AND NEW.fusion_trigger IS NOT NULL
        AND (NEW.processing_key ~ '^synthesis-fusion:[0-9a-f]{64}$') IS TRUE
        AND (jsonb_typeof(NEW.fusion_trigger)='object') IS TRUE
        AND (jsonb_typeof(NEW.fusion_trigger->'allowed_sources')='array') IS TRUE
        AND (NEW.fusion_trigger->>'request_id'=NEW.fusion_request_id::text) IS TRUE
        AND EXISTS (
        SELECT 1 FROM organizing.anchor_fusion_request r
        WHERE r.id=NEW.fusion_request_id AND r.workspace_id=NEW.workspace_id
          AND ((TG_OP='INSERT' AND r.status='PENDING' AND r.processing_id IS NULL) OR (TG_OP='UPDATE' AND r.status='DISPATCHED' AND r.processing_id=NEW.id))
          AND (r.anchor_id=(NEW.fusion_trigger->>'anchor_id')::uuid) IS TRUE AND (r.note_id=(NEW.fusion_trigger->>'note_id')::uuid) IS TRUE
          AND (r.proposal_id=(NEW.fusion_trigger->>'proposal_id')::uuid) IS TRUE AND (r.scope_version=(NEW.fusion_trigger->>'scope_version')::bigint) IS TRUE
          AND r.source_event_id=NEW.source_event_id AND r.source_id=NEW.source_id AND r.source_version_id=NEW.source_version_id
          AND r.content_artifact_id=NEW.content_artifact_id AND r.parse_projection_id=NEW.parse_projection_id
          AND r.source_content_hash=NEW.source_content_hash AND r.ingestion_attempt_id=NEW.ingestion_attempt_id AND r.source_occurred_at=NEW.source_occurred_at
          AND r.allowed_sources=NEW.fusion_trigger->'allowed_sources'
        )
    ) THEN RAISE EXCEPTION 'synthesis fusion processing binding is invalid' USING ERRCODE='23514'; END IF;
    RETURN NEW;
END; $$;
CREATE TRIGGER synthesis_fusion_processing_guard BEFORE INSERT OR UPDATE ON organizing.synthesis_processing FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_fusion_processing();
