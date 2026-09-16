-- A publication fact survives later publications. Shared sources and pending
-- candidates are not publication facts or body dependencies.
CREATE INDEX idx_authoring_publication_completed_history
    ON authoring.document_publication_binding(published_at,id) WHERE status='PUBLISHED';

CREATE VIEW organizing.synthesis_proven_publication AS
SELECT r.workspace_id,r.note_id,r.id AS revision_id,r.document_id,r.article_revision_id,
       r.projection_hash,r.content_hash,b.id AS publication_id,pc.id AS proposal_commit_id,
       b.git_commit,b.published_at
FROM organizing.synthesis_revision r
JOIN authoring.generated_article_revision gr
  ON gr.workspace_id=r.workspace_id AND gr.document_id=r.document_id
 AND gr.article_revision_id=r.article_revision_id AND gr.origin_id=r.note_id
 AND gr.origin_revision_id=r.id AND gr.projection_hash=r.projection_hash AND gr.content_hash=r.content_hash
JOIN core.article_revision ar ON ar.id=r.article_revision_id AND ar.workspace_id=r.workspace_id
 AND ar.document_id=r.document_id AND ar.content_hash=r.content_hash AND ar.created_by_type='AGENT'
 AND ar.status IN ('PUBLISHED','SUPERSEDED')
JOIN authoring.document_publication_binding b
  ON b.article_revision_id=r.article_revision_id AND b.workspace_id=r.workspace_id
 AND b.document_id=r.document_id AND b.content_hash=r.content_hash
 AND b.status='PUBLISHED' AND b.git_commit=ar.git_commit
JOIN change_control.proposal_commit pc
  ON pc.workspace_id=b.workspace_id AND pc.proposal_id=b.proposal_id AND pc.revision_id=b.proposal_revision_id
 AND pc.target_path=b.target_path AND pc.target_mode=b.target_mode
 AND pc.result_hash=b.content_hash AND pc.git_commit=b.git_commit;

CREATE FUNCTION organizing.validate_synthesis_publication_event()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE proof record;
BEGIN
    IF TG_OP='TRUNCATE' THEN
        IF EXISTS (SELECT 1 FROM workflow.outbox_event WHERE event_type='organizing.synthesis.published') THEN
            RAISE EXCEPTION 'synthesis publication events are immutable' USING ERRCODE='55000';
        END IF;
        RETURN NULL;
    END IF;
    IF TG_OP<>'INSERT' AND OLD.event_type='organizing.synthesis.published' THEN
        IF TG_OP='DELETE' THEN
            RAISE EXCEPTION 'synthesis publication events are immutable' USING ERRCODE='55000';
        END IF;
        IF (to_jsonb(NEW)-'published_at') IS DISTINCT FROM (to_jsonb(OLD)-'published_at')
           OR OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at
           OR NEW.published_at < OLD.occurred_at THEN
            RAISE EXCEPTION 'synthesis publication event binding is immutable' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    IF NEW.event_type<>'organizing.synthesis.published' THEN RETURN NEW; END IF;
    IF TG_OP<>'INSERT' THEN
        RAISE EXCEPTION 'cannot convert an event to a synthesis publication' USING ERRCODE='55000';
    END IF;
    SELECT * INTO proof FROM organizing.synthesis_proven_publication
     WHERE workspace_id=NEW.workspace_id AND revision_id=(NEW.payload->>'revision_id')::uuid;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'synthesis publication lacks committed owner evidence' USING ERRCODE='23514';
    END IF;
    IF (NEW.payload=jsonb_build_object(
            'workspace_id',proof.workspace_id,'note_id',proof.note_id,'revision_id',proof.revision_id,
            'document_id',proof.document_id,'article_revision_id',proof.article_revision_id,
            'projection_hash',proof.projection_hash,'content_hash',proof.content_hash,
            'publication_id',proof.publication_id,'proposal_commit_id',proof.proposal_commit_id,
            'git_commit',proof.git_commit)
        AND NEW.run_id IS NULL AND NEW.published_at IS NULL
        AND NEW.schema_version=1 AND NEW.event_version=1
        AND NEW.occurred_at=proof.published_at
        AND NEW.event_key='synthesis.note-published:v1:'||proof.revision_id::text
        AND NEW.idempotency_key=NEW.event_key) IS NOT TRUE THEN
        RAISE EXCEPTION 'synthesis publication event binding is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_publication_event_validate
    BEFORE INSERT OR UPDATE OR DELETE ON workflow.outbox_event
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_publication_event();
CREATE TRIGGER synthesis_publication_event_reject_truncate
    BEFORE TRUNCATE ON workflow.outbox_event
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.validate_synthesis_publication_event();
