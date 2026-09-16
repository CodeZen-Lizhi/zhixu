-- Goal-driven initial notes own a request identity; they do not replay a
-- source-ready event. Catalog completion is not generation/publication success.
CREATE TABLE organizing.synthesis_goal_request (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
 idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128 AND idempotency_key=btrim(idempotency_key)),
 request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
 goal_text text NOT NULL CHECK (octet_length(goal_text) BETWEEN 1 AND 2048 AND goal_text=btrim(goal_text)),
 status text NOT NULL DEFAULT 'DISCOVERING' CHECK (status IN ('DISCOVERING','CATALOG_READY')),
 after_source_id uuid,
 catalog_batches bigint NOT NULL DEFAULT 0 CHECK (catalog_batches>=0),
 next_check_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 error_code text CHECK(error_code IS NULL OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'),
 version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(id,workspace_id), UNIQUE(workspace_id,idempotency_key),
 CHECK (updated_at>=created_at)
);
CREATE INDEX synthesis_goal_discovering ON organizing.synthesis_goal_request(workspace_id,next_check_at,created_at,id) WHERE status='DISCOVERING';

CREATE TABLE organizing.synthesis_goal_catalog_batch (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL,
 request_id uuid NOT NULL,
 batch_no bigint NOT NULL CHECK(batch_no>0),
 after_source_id uuid,
 next_after_source_id uuid,
 item_count integer NOT NULL CHECK(item_count BETWEEN 0 AND 32),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(request_id,workspace_id) REFERENCES organizing.synthesis_goal_request(id,workspace_id) ON DELETE RESTRICT,
 UNIQUE(id,workspace_id), UNIQUE(request_id,batch_no),
 CHECK(next_after_source_id IS NULL OR after_source_id IS NULL OR next_after_source_id>after_source_id)
);
CREATE TABLE organizing.synthesis_goal_catalog_item (
 batch_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 ordinal integer NOT NULL CHECK(ordinal BETWEEN 1 AND 32),
 source_id uuid NOT NULL,
 source_version_id uuid NOT NULL,
 content_artifact_id uuid NOT NULL,
 parse_projection_id uuid NOT NULL,
 content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
 profile_revision_id uuid NOT NULL,
 title text NOT NULL CHECK(octet_length(title) BETWEEN 1 AND 512 AND title=btrim(title)),
 PRIMARY KEY(batch_id,ordinal), UNIQUE(batch_id,source_id),
 FOREIGN KEY(batch_id,workspace_id) REFERENCES organizing.synthesis_goal_catalog_batch(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(source_id,workspace_id) REFERENCES core.source(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(source_version_id,source_id) REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
 FOREIGN KEY(content_artifact_id,workspace_id) REFERENCES core.content_artifact(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(source_version_id,parse_projection_id,workspace_id) REFERENCES ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(profile_revision_id,workspace_id,source_version_id) REFERENCES learning.document_knowledge_profile_revision(id,workspace_id,source_version_id) ON DELETE RESTRICT
);

CREATE FUNCTION organizing.guard_synthesis_goal_catalog_batch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE request organizing.synthesis_goal_request; parent organizing.synthesis_goal_catalog_batch;
BEGIN
 IF TG_TABLE_NAME='synthesis_goal_catalog_batch' THEN
  SELECT * INTO request FROM organizing.synthesis_goal_request WHERE id=NEW.request_id AND workspace_id=NEW.workspace_id FOR UPDATE;
  IF request.status IS DISTINCT FROM 'DISCOVERING' OR NEW.batch_no<>request.catalog_batches+1 OR NEW.after_source_id IS DISTINCT FROM request.after_source_id THEN
   RAISE EXCEPTION 'goal catalog does not continue the current request' USING ERRCODE='23514';
  END IF;
 ELSE
  SELECT * INTO parent FROM organizing.synthesis_goal_catalog_batch WHERE id=NEW.batch_id AND workspace_id=NEW.workspace_id;
  SELECT * INTO request FROM organizing.synthesis_goal_request WHERE id=parent.request_id AND workspace_id=NEW.workspace_id FOR UPDATE;
  IF request.status IS DISTINCT FROM 'DISCOVERING' OR parent.batch_no<>request.catalog_batches+1 OR NEW.ordinal>parent.item_count
   OR (parent.after_source_id IS NOT NULL AND NEW.source_id<=parent.after_source_id)
   OR (parent.next_after_source_id IS NOT NULL AND NEW.source_id>parent.next_after_source_id)
   OR NOT EXISTS(SELECT 1 FROM core.source s JOIN core.source_version v ON v.source_id=s.id AND v.workspace_id=s.workspace_id
    JOIN learning.document_knowledge_profile_revision r ON r.id=NEW.profile_revision_id AND r.workspace_id=s.workspace_id AND r.source_version_id=v.id
    WHERE s.id=NEW.source_id AND s.workspace_id=NEW.workspace_id AND s.removed_at IS NULL AND s.logical_name=NEW.title
      AND v.id=NEW.source_version_id AND v.content_artifact_id=NEW.content_artifact_id AND v.content_hash=NEW.content_hash
      AND r.parse_projection_id=NEW.parse_projection_id AND v.security_status<>'quarantined'
      AND NOT EXISTS(SELECT 1 FROM core.source_version later WHERE later.source_id=s.id AND later.workspace_id=s.workspace_id AND (later.captured_at,later.id)>(v.captured_at,v.id))
      AND NOT EXISTS(SELECT 1 FROM ingestion.attempt ia WHERE ia.workspace_id=s.workspace_id AND ia.source_version_id=v.id AND ia.security_status='quarantined')) THEN
   RAISE EXCEPTION 'goal catalog item binding is invalid' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_goal_catalog_batch_guard BEFORE INSERT ON organizing.synthesis_goal_catalog_batch FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_goal_catalog_batch();
CREATE TRIGGER synthesis_goal_catalog_item_guard BEFORE INSERT ON organizing.synthesis_goal_catalog_item FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_goal_catalog_batch();

CREATE FUNCTION organizing.guard_synthesis_goal_request() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE batch organizing.synthesis_goal_catalog_batch;
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.status<>'DISCOVERING' OR NEW.version<>1 OR NEW.catalog_batches<>0 OR NEW.after_source_id IS NOT NULL OR NEW.error_code IS NOT NULL THEN
   RAISE EXCEPTION 'goal request must begin discovery' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 IF TG_OP='DELETE' OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.goal_text<>OLD.goal_text
  OR NEW.idempotency_key<>OLD.idempotency_key OR NEW.request_hash<>OLD.request_hash OR NEW.created_at<>OLD.created_at
  OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at OR OLD.status<>'DISCOVERING' THEN
  RAISE EXCEPTION 'goal request transition is invalid' USING ERRCODE='55000';
 END IF;
 IF NEW.catalog_batches=OLD.catalog_batches THEN
  IF NEW.status<>OLD.status OR NEW.after_source_id IS DISTINCT FROM OLD.after_source_id OR NEW.error_code IS NULL OR NEW.next_check_at<=OLD.next_check_at THEN
   RAISE EXCEPTION 'goal discovery deferral is invalid' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 IF NEW.catalog_batches<>OLD.catalog_batches+1 OR NEW.error_code IS NOT NULL THEN
  RAISE EXCEPTION 'goal catalog progress is invalid' USING ERRCODE='23514';
 END IF;
 SELECT * INTO batch FROM organizing.synthesis_goal_catalog_batch WHERE request_id=OLD.id AND batch_no=NEW.catalog_batches;
 IF batch.id IS NULL OR batch.after_source_id IS DISTINCT FROM OLD.after_source_id
  OR NEW.after_source_id IS DISTINCT FROM batch.next_after_source_id
  OR (batch.next_after_source_id IS NULL AND NEW.status<>'CATALOG_READY')
  OR (batch.next_after_source_id IS NOT NULL AND NEW.status<>'DISCOVERING')
  OR (SELECT count(*) FROM organizing.synthesis_goal_catalog_item WHERE batch_id=batch.id)<>batch.item_count
  OR EXISTS(SELECT 1 FROM (SELECT ordinal,source_id,lag(source_id) OVER(ORDER BY ordinal) AS previous FROM organizing.synthesis_goal_catalog_item WHERE batch_id=batch.id) i WHERE previous IS NOT NULL AND source_id<=previous) THEN
  RAISE EXCEPTION 'goal request lacks its complete catalog batch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_goal_request_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.synthesis_goal_request FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_goal_request();
CREATE TRIGGER synthesis_goal_request_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_goal_request FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_goal_catalog_batch_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_goal_catalog_batch FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_goal_catalog_batch_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_goal_catalog_batch FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_goal_catalog_item_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_goal_catalog_item FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_goal_catalog_item_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_goal_catalog_item FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

-- Header, complete item set, and parent cursor advance must commit together.
CREATE FUNCTION organizing.check_synthesis_goal_catalog_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_goal_request r WHERE r.id=NEW.request_id AND r.workspace_id=NEW.workspace_id AND r.catalog_batches>=NEW.batch_no)
  OR (SELECT count(*) FROM organizing.synthesis_goal_catalog_item WHERE batch_id=NEW.id)<>NEW.item_count THEN
  RAISE EXCEPTION 'goal catalog batch cannot commit without its request progress' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER synthesis_goal_catalog_commit AFTER INSERT ON organizing.synthesis_goal_catalog_batch
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.check_synthesis_goal_catalog_commit();
