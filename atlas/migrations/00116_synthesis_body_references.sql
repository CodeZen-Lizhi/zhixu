CREATE FUNCTION organizing.verify_synthesis_published_bindings(bindings jsonb)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF (jsonb_typeof(bindings)='array' AND jsonb_array_length(bindings) <= 24) IS NOT TRUE THEN
        RAISE EXCEPTION 'invalid synthesis publication bindings' USING ERRCODE='23514';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(bindings) e WHERE NOT EXISTS (
        SELECT 1 FROM organizing.synthesis_proven_publication p
        WHERE p.publication_id=(e->>'publication_id')::uuid
          AND e=jsonb_build_object('workspace_id',p.workspace_id,'note_id',p.note_id,
            'revision_id',p.revision_id,'publication_id',p.publication_id,'projection_hash',p.projection_hash))) THEN
        RAISE EXCEPTION 'synthesis input lacks published revision evidence' USING ERRCODE='23514';
    END IF;
END;
$$;

CREATE TABLE organizing.synthesis_revision_body_reference (
    workspace_id uuid NOT NULL,
    note_id uuid NOT NULL,
    revision_id uuid NOT NULL,
    item_id uuid NOT NULL,
    upstream_note_id uuid NOT NULL,
    upstream_revision_id uuid NOT NULL,
    upstream_item_id uuid NOT NULL,
    publication_id uuid NOT NULL,
    upstream_projection_hash text NOT NULL CHECK (upstream_projection_hash ~ '^[0-9a-f]{64}$'),
    PRIMARY KEY (revision_id,item_id),
    FOREIGN KEY (revision_id,workspace_id,note_id)
        REFERENCES organizing.synthesis_revision(id,workspace_id,note_id) ON DELETE RESTRICT,
    FOREIGN KEY (upstream_revision_id,workspace_id,upstream_note_id)
        REFERENCES organizing.synthesis_revision(id,workspace_id,note_id) ON DELETE RESTRICT,
    FOREIGN KEY (publication_id,workspace_id)
        REFERENCES authoring.document_publication_binding(id,workspace_id) ON DELETE RESTRICT,
    CHECK (note_id<>upstream_note_id)
);
CREATE INDEX idx_synthesis_body_reference_upstream
    ON organizing.synthesis_revision_body_reference(workspace_id,upstream_note_id,upstream_revision_id);

CREATE FUNCTION organizing.validate_synthesis_revision_body_reference()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE current_item jsonb; upstream_item jsonb; expected_ref jsonb; parent_id uuid;
BEGIN
    SELECT item,r.parent_revision_id INTO current_item,parent_id
      FROM organizing.synthesis_revision r CROSS JOIN LATERAL jsonb_array_elements(r.items) item
     WHERE r.id=NEW.revision_id AND r.workspace_id=NEW.workspace_id AND r.note_id=NEW.note_id
       AND item->>'id'=NEW.item_id::text;
    expected_ref := jsonb_build_object('workspace_id',NEW.workspace_id,'note_id',NEW.upstream_note_id,
      'revision_id',NEW.upstream_revision_id,'item_id',NEW.upstream_item_id,
      'publication_id',NEW.publication_id,'projection_hash',NEW.upstream_projection_hash);
    IF (current_item->'body_reference'=expected_ref) IS NOT TRUE THEN
        RAISE EXCEPTION 'body reference must be saved in its exact revision item' USING ERRCODE='23514';
    END IF;
    PERFORM organizing.verify_synthesis_published_bindings(jsonb_build_array(expected_ref-'item_id'));
    SELECT item INTO upstream_item
      FROM organizing.synthesis_revision r CROSS JOIN LATERAL jsonb_array_elements(r.items) item
     WHERE r.id=NEW.upstream_revision_id AND r.workspace_id=NEW.workspace_id AND r.note_id=NEW.upstream_note_id
       AND item->>'id'=NEW.upstream_item_id::text;
    IF upstream_item IS NULL THEN
        RAISE EXCEPTION 'referenced published item is missing' USING ERRCODE='23514';
    END IF;
    -- A local resolution or additional evidence may extend an already-cited
    -- item. The original published reference remains an immutable snapshot.
    IF NOT EXISTS (SELECT 1 FROM organizing.synthesis_revision_body_reference prior
      WHERE prior.revision_id=parent_id AND prior.workspace_id=NEW.workspace_id AND prior.note_id=NEW.note_id
        AND prior.item_id=NEW.item_id AND prior.upstream_revision_id=NEW.upstream_revision_id
        AND prior.upstream_note_id=NEW.upstream_note_id AND prior.upstream_item_id=NEW.upstream_item_id
        AND prior.publication_id=NEW.publication_id AND prior.upstream_projection_hash=NEW.upstream_projection_hash)
      AND (current_item-ARRAY['id','body_reference']) IS DISTINCT FROM (upstream_item-ARRAY['id','body_reference']) THEN
        RAISE EXCEPTION 'new body inclusion must retain the complete published item' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION organizing.project_synthesis_revision_body_references()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO organizing.synthesis_revision_body_reference(workspace_id,note_id,revision_id,item_id,
        upstream_note_id,upstream_revision_id,upstream_item_id,publication_id,upstream_projection_hash)
    SELECT NEW.workspace_id,NEW.note_id,NEW.id,(item->>'id')::uuid,
        (item->'body_reference'->>'note_id')::uuid,(item->'body_reference'->>'revision_id')::uuid,
        (item->'body_reference'->>'item_id')::uuid,(item->'body_reference'->>'publication_id')::uuid,
        item->'body_reference'->>'projection_hash'
    FROM jsonb_array_elements(NEW.items) item WHERE item ? 'body_reference';
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_body_reference_validate BEFORE INSERT ON organizing.synthesis_revision_body_reference
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_revision_body_reference();
CREATE TRIGGER synthesis_body_reference_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_revision_body_reference
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_body_reference_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_revision_body_reference
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_revision_project_body_references AFTER INSERT ON organizing.synthesis_revision
    FOR EACH ROW EXECUTE FUNCTION organizing.project_synthesis_revision_body_references();
