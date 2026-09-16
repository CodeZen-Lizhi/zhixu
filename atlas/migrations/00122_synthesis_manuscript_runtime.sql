-- A receipt identity for a proven existing root grant, never an authorization source.
CREATE TABLE organizing.synthesis_manuscript_root_identity (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES core.workspace(id),
 grant_generation bigint NOT NULL CHECK(grant_generation>0),
 root_fingerprint text NOT NULL CHECK(root_fingerprint ~ '^[0-9a-f]{64}$'),
 binding_version bigint NOT NULL CHECK(binding_version>0),
 UNIQUE(workspace_id,grant_generation,root_fingerprint,binding_version),
 UNIQUE(workspace_id,id)
);
CREATE FUNCTION organizing.guard_synthesis_manuscript_root_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'manuscript root identity is immutable' USING ERRCODE='55000'; END IF;
 IF NOT EXISTS(SELECT 1 FROM core.workspace WHERE id=NEW.workspace_id AND status='active' AND availability='available' AND removed_at IS NULL AND root_fingerprint=NEW.root_fingerprint AND binding_version=NEW.binding_version) THEN
  RAISE EXCEPTION 'manuscript root identity has no current workspace binding' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_manuscript_root_identity_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.synthesis_manuscript_root_identity FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_manuscript_root_identity();
CREATE TRIGGER synthesis_manuscript_root_identity_truncate BEFORE TRUNCATE ON organizing.synthesis_manuscript_root_identity FOR EACH STATEMENT EXECUTE FUNCTION organizing.guard_synthesis_manuscript_root_identity();

-- The old graph stays immutable; a new definition may use a merge node.
CREATE OR REPLACE FUNCTION organizing.guard_synthesis_execution()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 OR NEW.input_document IS NOT NULL OR NEW.apply_node_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'synthesis execution must start without results' USING ERRCODE='23514';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM workflow.run r JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id
            WHERE r.id=NEW.workflow_run_id AND r.workspace_id=NEW.workspace_id AND d.key='organizing.synthesis-note' AND d.version IN (1,2)
              AND r.input->>'processing_id'=NEW.processing_id::text AND (r.input->>'execution_no')::integer=NEW.execution_no
              AND (r.input->>'apply_recovery')::boolean=NEW.apply_recovery) THEN
            RAISE EXCEPTION 'synthesis execution workflow binding is invalid' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE' OR NEW.workflow_run_id<>OLD.workflow_run_id OR NEW.workspace_id<>OLD.workspace_id OR NEW.processing_id<>OLD.processing_id
       OR NEW.execution_no<>OLD.execution_no OR NEW.apply_recovery<>OLD.apply_recovery OR NEW.created_at<>OLD.created_at
       OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
       OR (OLD.input_document IS NOT NULL AND (NEW.input_document IS DISTINCT FROM OLD.input_document OR NEW.input_hash IS DISTINCT FROM OLD.input_hash))
       OR OLD.applied_result IS NOT NULL THEN
        RAISE EXCEPTION 'synthesis execution immutable result changed' USING ERRCODE='55000';
    END IF;
    IF NEW.applied_result IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM organizing.synthesis_apply_receipt receipt
        WHERE receipt.workspace_id=NEW.workspace_id AND receipt.processing_id=NEW.processing_id
          AND (NEW.apply_recovery OR receipt.workflow_run_id=NEW.workflow_run_id)
          AND receipt.result=NEW.applied_result
    ) THEN
        RAISE EXCEPTION 'synthesis execution result requires its immutable application receipt' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TABLE organizing.synthesis_manuscript_application (
 workspace_id uuid NOT NULL,
 processing_id uuid NOT NULL,
 note_id uuid NOT NULL,
 receipt_id uuid NOT NULL,
 receipt_hash text NOT NULL CHECK(receipt_hash ~ '^[0-9a-f]{64}$'),
 binding_hash text NOT NULL CHECK(binding_hash ~ '^[0-9a-f]{64}$'),
 PRIMARY KEY(workspace_id,processing_id,note_id),
 FOREIGN KEY(workspace_id,processing_id) REFERENCES organizing.synthesis_apply_receipt(workspace_id,processing_id),
 FOREIGN KEY(receipt_id,workspace_id) REFERENCES organizing.synthesis_manuscript_receipt(id,workspace_id)
);
CREATE FUNCTION organizing.guard_synthesis_manuscript_application() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p jsonb; a jsonb; c jsonb;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'manuscript application is immutable' USING ERRCODE='55000'; END IF;
 SELECT convert_from(receipt.payload,'UTF8')::jsonb,convert_from(attempt.payload,'UTF8')::jsonb,convert_from(capture.payload,'UTF8')::jsonb INTO p,a,c
 FROM organizing.synthesis_manuscript_receipt receipt JOIN organizing.synthesis_manuscript_attempt attempt ON attempt.workspace_id=receipt.workspace_id AND attempt.id=receipt.attempt_id
 JOIN organizing.synthesis_manuscript_capture capture ON capture.workspace_id=attempt.workspace_id AND capture.id=attempt.capture_id
 WHERE receipt.workspace_id=NEW.workspace_id AND receipt.id=NEW.receipt_id;
 IF (p->>'hash'=NEW.receipt_hash AND a->'command'->>'processing_id'=NEW.processing_id::text AND a->'command'->>'note_id'=NEW.note_id::text) IS NOT TRUE
 OR NOT EXISTS(SELECT 1 FROM organizing.synthesis_apply_receipt applied JOIN organizing.synthesis_revision revision ON revision.workspace_id=applied.workspace_id AND revision.note_id=NEW.note_id AND revision.manuscript_receipt_id=NEW.receipt_id
 WHERE applied.workspace_id=NEW.workspace_id AND applied.processing_id=NEW.processing_id AND applied.binding_hash=NEW.binding_hash AND applied.result->'revision_ids' @> jsonb_build_array(revision.id::text))
 OR NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_root_identity root WHERE root.workspace_id=NEW.workspace_id AND root.id::text=c->>'root_grant_id' AND root.root_fingerprint=c->>'root_fingerprint' AND root.binding_version=(c->>'workspace_binding_version')::bigint) THEN
  RAISE EXCEPTION 'manuscript application differs from exact candidate/root receipt' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_manuscript_application_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.synthesis_manuscript_application FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_manuscript_application();
CREATE TRIGGER synthesis_manuscript_application_truncate BEFORE TRUNCATE ON organizing.synthesis_manuscript_application FOR EACH STATEMENT EXECUTE FUNCTION organizing.guard_synthesis_manuscript_application();

CREATE FUNCTION organizing.verify_synthesis_manuscript_runtime_closure() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.parent_revision_id IS NOT NULL AND EXISTS(SELECT 1 FROM workflow.run r JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id WHERE r.id=NEW.workflow_run_id AND r.workspace_id=NEW.workspace_id AND d.key='organizing.synthesis-note' AND d.version=2)
 AND (NEW.renderer_version<>'synthesis-markdown/v2' OR NOT EXISTS(SELECT 1 FROM organizing.synthesis_manuscript_application a WHERE a.workspace_id=NEW.workspace_id AND a.note_id=NEW.note_id AND a.receipt_id=NEW.manuscript_receipt_id)) THEN
  RAISE EXCEPTION 'v2 runtime candidate lacks manuscript application binding' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER synthesis_manuscript_runtime_closure AFTER INSERT ON organizing.synthesis_revision DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_manuscript_runtime_closure();
