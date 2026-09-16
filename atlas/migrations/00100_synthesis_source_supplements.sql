-- Supplementary evidence belongs to the note ledger, never to a historical
-- revision's immutable items or projection hash.
CREATE TABLE organizing.synthesis_source_supplement (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    note_id uuid NOT NULL,
    base_revision_id uuid NOT NULL,
    item_id uuid NOT NULL,
    slot text NOT NULL CHECK (slot IN ('FACT','CONFLICT','GAP_CONTEXT','GAP_RESOLUTION')),
    alternative_index integer NOT NULL CHECK ((slot='CONFLICT' AND alternative_index BETWEEN 0 AND 3) OR (slot<>'CONFLICT' AND alternative_index=-1)),
    processing_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_version_id uuid NOT NULL,
    content_artifact_id uuid NOT NULL,
    parse_projection_id uuid NOT NULL,
    source_span_id uuid NOT NULL REFERENCES ingestion.source_span(id) ON DELETE RESTRICT,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    excerpt_hash text NOT NULL CHECK (excerpt_hash ~ '^[0-9a-f]{64}$'),
    title text NOT NULL CHECK (title<>'' AND octet_length(title)<=512),
    created_at timestamptz NOT NULL,
    CONSTRAINT fk_synthesis_supplement_revision FOREIGN KEY (base_revision_id,workspace_id,note_id)
        REFERENCES organizing.synthesis_revision(id,workspace_id,note_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_supplement_receipt FOREIGN KEY (workspace_id,processing_id)
        REFERENCES organizing.synthesis_apply_receipt(workspace_id,processing_id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT fk_synthesis_supplement_source FOREIGN KEY (source_id,workspace_id)
        REFERENCES core.source(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_supplement_version FOREIGN KEY (source_version_id,source_id)
        REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_supplement_projection FOREIGN KEY (source_version_id,parse_projection_id,workspace_id)
        REFERENCES ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT uq_synthesis_supplement_support UNIQUE (workspace_id,note_id,item_id,slot,alternative_index,source_version_id,parse_projection_id,source_span_id)
);
CREATE INDEX idx_synthesis_supplement_page ON organizing.synthesis_source_supplement(workspace_id,note_id,created_at DESC,id DESC);
CREATE TRIGGER synthesis_supplement_source_validate BEFORE INSERT ON organizing.synthesis_source_supplement
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_source();

CREATE FUNCTION organizing.validate_synthesis_supplement_item()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE item jsonb;
BEGIN
    SELECT element INTO item FROM organizing.synthesis_revision r, jsonb_array_elements(r.items) element
      WHERE r.id=NEW.base_revision_id AND r.note_id=NEW.note_id AND r.workspace_id=NEW.workspace_id AND element->>'id'=NEW.item_id::text;
    IF item IS NULL OR NOT (CASE NEW.slot
        WHEN 'FACT' THEN item->>'kind'='FACT'
        WHEN 'CONFLICT' THEN item->>'kind'='CONFLICT' AND jsonb_array_length(item->'conflict'->'alternatives')>NEW.alternative_index
        WHEN 'GAP_CONTEXT' THEN item->>'kind'='GAP'
        WHEN 'GAP_RESOLUTION' THEN item->>'kind'='GAP' AND jsonb_typeof(item->'gap'->'resolution')='object'
        ELSE false END) IS TRUE THEN
        RAISE EXCEPTION 'synthesis supplement item binding changed' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_supplement_item_validate BEFORE INSERT ON organizing.synthesis_source_supplement
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_supplement_item();
CREATE TRIGGER synthesis_supplement_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_source_supplement
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_supplement_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_source_supplement
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
