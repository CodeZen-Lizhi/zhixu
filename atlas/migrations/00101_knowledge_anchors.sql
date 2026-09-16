-- A stable synthesis target has user-confirmed scope. Body publication remains
-- exclusively owned by Authoring; none of these tables alters that pointer.
CREATE TABLE organizing.knowledge_anchor (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
 note_id uuid NOT NULL,
 basis_revision_id uuid NOT NULL,
 title text NOT NULL CHECK(title=btrim(title) AND octet_length(title) BETWEEN 1 AND 512),
 scope_version bigint NOT NULL CHECK(scope_version>0),
 version bigint NOT NULL CHECK(version>0),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL CHECK(updated_at>=created_at),
 UNIQUE(id,workspace_id), UNIQUE(note_id,workspace_id),
 FOREIGN KEY(note_id,workspace_id) REFERENCES organizing.synthesis_note(id,workspace_id),
 FOREIGN KEY(basis_revision_id,workspace_id,note_id) REFERENCES organizing.synthesis_revision(id,workspace_id,note_id)
);
CREATE INDEX knowledge_anchor_workspace_page ON organizing.knowledge_anchor(workspace_id,id);
CREATE TABLE organizing.anchor_scope_revision (
 anchor_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 version bigint NOT NULL CHECK(version>0),
 scope jsonb NOT NULL CHECK((jsonb_typeof(scope)='object' AND jsonb_typeof(scope->'topics')='array' AND jsonb_array_length(scope->'topics') BETWEEN 1 AND 64
 AND jsonb_typeof(scope->'audiences')='array' AND jsonb_array_length(scope->'audiences') BETWEEN 1 AND 32 AND jsonb_typeof(scope->'description')='string'
 AND octet_length(scope->>'description') BETWEEN 1 AND 2048 AND (scope-'topics'-'audiences'-'description')='{}'::jsonb) IS TRUE),
 proposal_id uuid,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(anchor_id,version), UNIQUE(anchor_id,workspace_id,version),
 FOREIGN KEY(anchor_id,workspace_id) REFERENCES organizing.knowledge_anchor(id,workspace_id),
 CHECK((version=1 AND proposal_id IS NULL) OR (version>1 AND proposal_id IS NOT NULL))
);
ALTER TABLE organizing.knowledge_anchor ADD CONSTRAINT knowledge_anchor_current_scope FOREIGN KEY(id,workspace_id,scope_version)
 REFERENCES organizing.anchor_scope_revision(anchor_id,workspace_id,version) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE organizing.anchor_proposal (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, anchor_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('SCOPE_ADJUSTMENT','SOURCE_ASSOCIATION')),
 scope_version bigint NOT NULL, suggested jsonb,
 reason text NOT NULL CHECK(reason=btrim(reason) AND octet_length(reason) BETWEEN 1 AND 2048),
 model_run_id uuid NOT NULL,
 created_at timestamptz NOT NULL,
 UNIQUE(id,workspace_id,anchor_id),
 FOREIGN KEY(anchor_id,workspace_id,scope_version) REFERENCES organizing.anchor_scope_revision(anchor_id,workspace_id,version),
 FOREIGN KEY(model_run_id,workspace_id) REFERENCES agent.model_run(id,workspace_id),
 CHECK((kind='SCOPE_ADJUSTMENT' AND suggested IS NOT NULL AND jsonb_typeof(suggested)='object') OR (kind='SOURCE_ASSOCIATION' AND suggested IS NULL))
);
CREATE INDEX anchor_proposal_page ON organizing.anchor_proposal(workspace_id,anchor_id,kind,id);
ALTER TABLE organizing.anchor_scope_revision ADD FOREIGN KEY(proposal_id,workspace_id,anchor_id)
 REFERENCES organizing.anchor_proposal(id,workspace_id,anchor_id);
CREATE TABLE organizing.anchor_proposal_evidence (
 workspace_id uuid NOT NULL, anchor_id uuid NOT NULL, proposal_id uuid NOT NULL,
 source_id uuid NOT NULL, source_version_id uuid NOT NULL, content_artifact_id uuid NOT NULL,
 parse_projection_id uuid NOT NULL, source_span_id uuid NOT NULL REFERENCES ingestion.source_span(id),
 content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'), excerpt_hash text NOT NULL CHECK(excerpt_hash ~ '^[0-9a-f]{64}$'),
 title text NOT NULL CHECK(octet_length(title) BETWEEN 1 AND 512),
 PRIMARY KEY(proposal_id,source_span_id),
 FOREIGN KEY(proposal_id,workspace_id,anchor_id) REFERENCES organizing.anchor_proposal(id,workspace_id,anchor_id),
 FOREIGN KEY(source_id,workspace_id) REFERENCES core.source(id,workspace_id),
 FOREIGN KEY(source_version_id,source_id) REFERENCES core.source_version(id,source_id),
 FOREIGN KEY(source_version_id,parse_projection_id,workspace_id) REFERENCES ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id)
);
CREATE TRIGGER anchor_evidence_tuple BEFORE INSERT ON organizing.anchor_proposal_evidence FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_source();
CREATE TABLE organizing.anchor_decision (
 proposal_id uuid PRIMARY KEY, workspace_id uuid NOT NULL, anchor_id uuid NOT NULL,
 decision text NOT NULL CHECK(decision IN ('ACCEPTED','REJECTED')),
 anchor_version bigint NOT NULL CHECK(anchor_version>1),
 receipt_key text NOT NULL,
 created_at timestamptz NOT NULL,
 FOREIGN KEY(proposal_id,workspace_id,anchor_id) REFERENCES organizing.anchor_proposal(id,workspace_id,anchor_id)
);
CREATE TABLE organizing.anchor_receipt (
 workspace_id uuid NOT NULL REFERENCES core.workspace(id), idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 operation text NOT NULL CHECK(operation IN ('CREATE','RECOMMEND','DECIDE')),
 anchor_id uuid NOT NULL, result jsonb NOT NULL CHECK(jsonb_typeof(result)='object'), created_at timestamptz NOT NULL,
 PRIMARY KEY(workspace_id,idempotency_key),
 FOREIGN KEY(anchor_id,workspace_id) REFERENCES organizing.knowledge_anchor(id,workspace_id)
);
ALTER TABLE organizing.anchor_decision ADD FOREIGN KEY(workspace_id,receipt_key) REFERENCES organizing.anchor_receipt(workspace_id,idempotency_key) DEFERRABLE INITIALLY DEFERRED;
CREATE FUNCTION organizing.guard_anchor_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'anchor history is immutable' USING ERRCODE='55000'; END; $$;
CREATE TRIGGER anchor_scope_immutable BEFORE UPDATE OR DELETE ON organizing.anchor_scope_revision FOR EACH ROW EXECUTE FUNCTION organizing.guard_anchor_history();
CREATE TRIGGER anchor_proposal_immutable BEFORE UPDATE OR DELETE ON organizing.anchor_proposal FOR EACH ROW EXECUTE FUNCTION organizing.guard_anchor_history();
CREATE TRIGGER anchor_evidence_immutable BEFORE UPDATE OR DELETE ON organizing.anchor_proposal_evidence FOR EACH ROW EXECUTE FUNCTION organizing.guard_anchor_history();
CREATE TRIGGER anchor_decision_immutable BEFORE UPDATE OR DELETE ON organizing.anchor_decision FOR EACH ROW EXECUTE FUNCTION organizing.guard_anchor_history();
CREATE TRIGGER anchor_receipt_immutable BEFORE UPDATE OR DELETE ON organizing.anchor_receipt FOR EACH ROW EXECUTE FUNCTION organizing.guard_anchor_history();
CREATE FUNCTION organizing.guard_anchor_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'anchor cannot be deleted' USING ERRCODE='55000'; END IF;
 IF NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.note_id<>OLD.note_id OR NEW.basis_revision_id<>OLD.basis_revision_id
 OR NEW.title<>OLD.title OR NEW.created_at<>OLD.created_at OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
 OR NEW.scope_version NOT IN (OLD.scope_version,OLD.scope_version+1) THEN RAISE EXCEPTION 'anchor transition is invalid' USING ERRCODE='55000'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER anchor_transition BEFORE UPDATE OR DELETE ON organizing.knowledge_anchor FOR EACH ROW EXECUTE FUNCTION organizing.guard_anchor_transition();
CREATE FUNCTION organizing.check_anchor_scope_acceptance() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.version>1 AND NOT EXISTS(SELECT 1 FROM organizing.anchor_proposal p JOIN organizing.anchor_decision d ON d.proposal_id=p.id AND d.workspace_id=p.workspace_id AND d.anchor_id=p.anchor_id
 WHERE p.id=NEW.proposal_id AND p.workspace_id=NEW.workspace_id AND p.anchor_id=NEW.anchor_id AND p.kind='SCOPE_ADJUSTMENT'
 AND p.scope_version=NEW.version-1 AND p.suggested=NEW.scope AND d.decision='ACCEPTED') THEN
 RAISE EXCEPTION 'scope expansion requires an accepted matching proposal' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE CONSTRAINT TRIGGER anchor_scope_accepted AFTER INSERT ON organizing.anchor_scope_revision DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.check_anchor_scope_acceptance();
CREATE FUNCTION organizing.check_anchor_proposal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM agent.model_run WHERE id=NEW.model_run_id AND workspace_id=NEW.workspace_id AND status='SUCCEEDED')
 OR (SELECT count(*) FROM organizing.anchor_proposal_evidence WHERE proposal_id=NEW.id) NOT BETWEEN 1 AND 32 THEN
 RAISE EXCEPTION 'recommendation needs successful model and exact evidence' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE CONSTRAINT TRIGGER anchor_proposal_valid AFTER INSERT ON organizing.anchor_proposal DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.check_anchor_proposal();
CREATE FUNCTION organizing.check_anchor_creation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.version<>1 OR NEW.scope_version<>1 OR NOT EXISTS(SELECT 1 FROM organizing.synthesis_note WHERE id=NEW.note_id AND workspace_id=NEW.workspace_id AND current_revision_id=NEW.basis_revision_id) THEN
 RAISE EXCEPTION 'anchor must start from current note revision at version one' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER anchor_creation BEFORE INSERT ON organizing.knowledge_anchor FOR EACH ROW EXECUTE FUNCTION organizing.check_anchor_creation();
CREATE FUNCTION organizing.check_anchor_decision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p organizing.anchor_proposal%ROWTYPE; a organizing.knowledge_anchor%ROWTYPE; r organizing.anchor_receipt%ROWTYPE;
BEGIN
 SELECT * INTO p FROM organizing.anchor_proposal WHERE id=NEW.proposal_id AND workspace_id=NEW.workspace_id AND anchor_id=NEW.anchor_id;
 SELECT * INTO a FROM organizing.knowledge_anchor WHERE id=NEW.anchor_id AND workspace_id=NEW.workspace_id;
 SELECT * INTO r FROM organizing.anchor_receipt WHERE workspace_id=NEW.workspace_id AND idempotency_key=NEW.receipt_key;
 IF p.id IS NULL OR a.id IS NULL OR r.operation IS DISTINCT FROM 'DECIDE' OR r.anchor_id IS DISTINCT FROM NEW.anchor_id OR a.version<>NEW.anchor_version
 OR (r.result->'anchor'->>'version')::bigint IS DISTINCT FROM a.version
 OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(r.result->'items') i WHERE i->>'id'=NEW.proposal_id::text AND i->>'status'=NEW.decision)
 OR (p.kind='SOURCE_ASSOCIATION' AND a.scope_version<>p.scope_version)
 OR (p.kind='SCOPE_ADJUSTMENT' AND NEW.decision='REJECTED' AND a.scope_version<>p.scope_version)
 OR (p.kind='SCOPE_ADJUSTMENT' AND NEW.decision='ACCEPTED' AND NOT EXISTS(SELECT 1 FROM organizing.anchor_scope_revision s WHERE s.anchor_id=a.id AND s.workspace_id=a.workspace_id AND s.version=a.scope_version AND s.version=p.scope_version+1 AND s.proposal_id=p.id AND s.scope=p.suggested)) THEN
 RAISE EXCEPTION 'anchor decision requires matching current scope and receipt' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
CREATE CONSTRAINT TRIGGER anchor_decision_valid AFTER INSERT ON organizing.anchor_decision DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.check_anchor_decision();
