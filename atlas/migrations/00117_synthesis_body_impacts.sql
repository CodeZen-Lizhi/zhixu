-- A body impact is an immutable observation, never permission to edit a note.
CREATE TABLE organizing.synthesis_body_impact (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    note_id uuid NOT NULL,
    base_revision_id uuid NOT NULL,
    item_id uuid NOT NULL,
    upstream_note_id uuid NOT NULL,
    upstream_revision_id uuid NOT NULL,
    upstream_item_id uuid NOT NULL,
    upstream_publication_id uuid NOT NULL,
    publication_id uuid NOT NULL,
    published_revision_id uuid NOT NULL,
    event_id uuid NOT NULL,
    reason text NOT NULL CHECK (reason IN ('CONTENT_CHANGED','ITEM_MISSING')),
    detected_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (id,workspace_id),
    FOREIGN KEY (event_id,workspace_id) REFERENCES workflow.outbox_event(id,workspace_id) ON DELETE RESTRICT,
    UNIQUE (base_revision_id,item_id,publication_id),
    FOREIGN KEY (base_revision_id,item_id)
        REFERENCES organizing.synthesis_revision_body_reference(revision_id,item_id) ON DELETE RESTRICT,
    FOREIGN KEY (base_revision_id,workspace_id,note_id)
        REFERENCES organizing.synthesis_revision(id,workspace_id,note_id) ON DELETE RESTRICT,
    FOREIGN KEY (upstream_revision_id,workspace_id,upstream_note_id)
        REFERENCES organizing.synthesis_revision(id,workspace_id,note_id) ON DELETE RESTRICT,
    FOREIGN KEY (published_revision_id,workspace_id,upstream_note_id)
        REFERENCES organizing.synthesis_revision(id,workspace_id,note_id) ON DELETE RESTRICT,
    FOREIGN KEY (upstream_publication_id,workspace_id)
        REFERENCES authoring.document_publication_binding(id,workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY (publication_id,workspace_id)
        REFERENCES authoring.document_publication_binding(id,workspace_id) ON DELETE RESTRICT
);
CREATE INDEX idx_synthesis_body_impact_base
    ON organizing.synthesis_body_impact(workspace_id,note_id,base_revision_id,id);

-- Filter actual changed referenced items before applying any batch limit.
-- Publication identity alone, shared sources and changes to other items are
-- deliberately absent from the content comparison. Missing items require review.
CREATE VIEW organizing.synthesis_pending_body_impact AS
WITH visible_base AS (
    -- The reader defaults to the Authoring-published revision even when a
    -- newer synthesis candidate is pending. Keep both identities visible,
    -- but prove the published one through the exact current document pointer
    -- and the immutable publication evidence view.
    SELECT n.workspace_id,n.id AS note_id,n.current_revision_id AS revision_id
    FROM organizing.synthesis_note n
    UNION
    SELECT p.workspace_id,p.note_id,p.revision_id
    FROM organizing.synthesis_proven_publication p
    JOIN core.document d ON d.id=p.document_id AND d.workspace_id=p.workspace_id
     AND d.current_published_revision_id=p.article_revision_id
     AND d.lifecycle_status='PUBLISHED'
    JOIN organizing.synthesis_note n ON n.id=p.note_id AND n.workspace_id=p.workspace_id
     AND n.document_id=p.document_id
)
SELECT ref.workspace_id,ref.note_id,ref.revision_id AS base_revision_id,ref.item_id,
       ref.upstream_note_id,ref.upstream_revision_id,ref.upstream_item_id,
       ref.publication_id AS upstream_publication_id,p.publication_id,
       p.revision_id AS published_revision_id,e.id AS event_id,p.published_at,
       CASE WHEN changed.item IS NULL THEN 'ITEM_MISSING' ELSE 'CONTENT_CHANGED' END AS reason
FROM organizing.synthesis_revision_body_reference ref
JOIN visible_base base ON base.workspace_id=ref.workspace_id AND base.note_id=ref.note_id
 AND base.revision_id=ref.revision_id
JOIN organizing.synthesis_revision original ON original.id=ref.upstream_revision_id
 AND original.workspace_id=ref.workspace_id AND original.note_id=ref.upstream_note_id
JOIN organizing.synthesis_proven_publication p ON p.workspace_id=ref.workspace_id AND p.note_id=ref.upstream_note_id
JOIN organizing.synthesis_revision updated ON updated.id=p.revision_id
 AND updated.workspace_id=p.workspace_id AND updated.note_id=p.note_id AND updated.revision_no>original.revision_no
JOIN workflow.outbox_event e ON e.workspace_id=p.workspace_id
 AND e.event_type='organizing.synthesis.published'
 AND e.idempotency_key='synthesis.note-published:v1:'||p.revision_id::text
CROSS JOIN LATERAL (SELECT source_item.value AS item FROM jsonb_array_elements(original.items) AS source_item(value) WHERE source_item.value->>'id'=ref.upstream_item_id::text) prior
LEFT JOIN LATERAL (SELECT source_item.value AS item FROM jsonb_array_elements(updated.items) AS source_item(value) WHERE source_item.value->>'id'=ref.upstream_item_id::text) changed ON true
WHERE (prior.item-ARRAY['id','body_reference']) IS DISTINCT FROM (changed.item-ARRAY['id','body_reference']);

CREATE FUNCTION organizing.validate_synthesis_body_impact()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    downstream_document_id uuid;
BEGIN
    -- Follow candidate append's note -> document lock order. The note lock
    -- protects the editable base; Authoring finalization does not lock the
    -- note, so its document's published pointer needs a lock before recheck.
    SELECT document_id INTO downstream_document_id FROM organizing.synthesis_note
    WHERE id=NEW.note_id AND workspace_id=NEW.workspace_id FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'synthesis body impact lacks downstream note' USING ERRCODE='23514';
    END IF;
    PERFORM 1 FROM core.document
    WHERE id=downstream_document_id AND workspace_id=NEW.workspace_id FOR SHARE;
    IF NOT FOUND OR NOT EXISTS (
        SELECT 1 FROM organizing.synthesis_pending_body_impact p
        WHERE p.workspace_id=NEW.workspace_id AND p.note_id=NEW.note_id
          AND p.base_revision_id=NEW.base_revision_id AND p.item_id=NEW.item_id
          AND p.upstream_note_id=NEW.upstream_note_id AND p.upstream_revision_id=NEW.upstream_revision_id
          AND p.upstream_item_id=NEW.upstream_item_id AND p.upstream_publication_id=NEW.upstream_publication_id
          AND p.publication_id=NEW.publication_id AND p.published_revision_id=NEW.published_revision_id
          AND p.event_id=NEW.event_id AND p.reason=NEW.reason AND NEW.detected_at>=p.published_at) THEN
        RAISE EXCEPTION 'synthesis body impact lacks current reference and changed published owner evidence' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_body_impact_validate BEFORE INSERT ON organizing.synthesis_body_impact
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_body_impact();
CREATE TRIGGER synthesis_body_impact_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_body_impact
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_body_impact_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_body_impact
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
