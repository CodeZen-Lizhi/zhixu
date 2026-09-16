-- A source promotion pins exactly one immutable source/profile catalog before
-- workers can discover a broader workspace catalog for the resulting goal.
CREATE TABLE organizing.synthesis_source_promotion (
 workspace_id uuid NOT NULL,
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128 AND idempotency_key=btrim(idempotency_key)),
 request_id uuid NOT NULL,
 source_id uuid NOT NULL,
 source_version_id uuid NOT NULL,
 profile_revision_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace_id,idempotency_key), UNIQUE(request_id),
 FOREIGN KEY(request_id,workspace_id) REFERENCES organizing.synthesis_goal_request(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(source_id,workspace_id) REFERENCES core.source(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(source_version_id,source_id) REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
 FOREIGN KEY(profile_revision_id,workspace_id,source_version_id) REFERENCES learning.document_knowledge_profile_revision(id,workspace_id,source_version_id) ON DELETE RESTRICT
);
CREATE FUNCTION organizing.guard_synthesis_source_promotion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM organizing.synthesis_goal_request g
  JOIN organizing.synthesis_goal_catalog_batch b ON b.request_id=g.id AND b.workspace_id=g.workspace_id
  JOIN organizing.synthesis_goal_catalog_item i ON i.batch_id=b.id AND i.workspace_id=b.workspace_id
  WHERE g.id=NEW.request_id AND g.workspace_id=NEW.workspace_id AND g.status='CATALOG_READY' AND g.catalog_batches=1
   AND b.batch_no=1 AND b.item_count=1 AND b.after_source_id IS NULL AND b.next_after_source_id IS NULL
   AND i.ordinal=1 AND i.source_id=NEW.source_id AND i.source_version_id=NEW.source_version_id AND i.profile_revision_id=NEW.profile_revision_id)
 THEN RAISE EXCEPTION 'source promotion must bind its complete single-source catalog' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER synthesis_source_promotion_guard BEFORE INSERT ON organizing.synthesis_source_promotion FOR EACH ROW EXECUTE FUNCTION organizing.guard_synthesis_source_promotion();
CREATE TRIGGER synthesis_source_promotion_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_source_promotion FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_source_promotion_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_source_promotion FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
