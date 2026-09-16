-- One durable source-ready fact may be evaluated against every current anchor
-- scope. This subscription does not consume or mutate synthesis_processing.
CREATE TABLE organizing.anchor_source_discovery (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    processing_id uuid NOT NULL,
    anchor_id uuid NOT NULL,
    scope_version bigint NOT NULL CHECK (scope_version > 0),
    source_id uuid NOT NULL,
    source_version_id uuid NOT NULL,
    content_artifact_id uuid NOT NULL,
    parse_projection_id uuid NOT NULL,
    source_content_hash text NOT NULL CHECK (source_content_hash ~ '^[0-9a-f]{64}$'),
    status text NOT NULL CHECK (status IN ('WAITING_PROFILE','PREPARED','REQUESTED','STALE')),
    profile_revision_id uuid,
    evidence jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(evidence)='array' AND jsonb_array_length(evidence) <= 256),
    request_ids jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(request_ids)='array' AND jsonb_array_length(request_ids) <= 8),
    next_check_at timestamptz NOT NULL,
    error_code text CHECK (error_code IS NULL OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'),
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_anchor_source_discovery_workspace UNIQUE(id,workspace_id),
    CONSTRAINT uq_anchor_source_discovery_subscription UNIQUE(workspace_id,source_version_id,parse_projection_id,anchor_id,scope_version),
    CONSTRAINT fk_anchor_source_discovery_processing FOREIGN KEY(processing_id,workspace_id)
        REFERENCES organizing.synthesis_processing(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_source_discovery_anchor_scope FOREIGN KEY(anchor_id,workspace_id,scope_version)
        REFERENCES organizing.anchor_scope_revision(anchor_id,workspace_id,version) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_source_discovery_source FOREIGN KEY(source_id,workspace_id)
        REFERENCES core.source(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_source_discovery_source_version FOREIGN KEY(source_version_id,source_id)
        REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_source_discovery_artifact FOREIGN KEY(content_artifact_id,workspace_id)
        REFERENCES core.content_artifact(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_source_discovery_projection FOREIGN KEY(source_version_id,parse_projection_id,workspace_id)
        REFERENCES ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_anchor_source_discovery_profile FOREIGN KEY(profile_revision_id,workspace_id,source_version_id)
        REFERENCES learning.document_knowledge_profile_revision(id,workspace_id,source_version_id) ON DELETE RESTRICT,
    CONSTRAINT anchor_source_discovery_lifecycle CHECK (
        updated_at >= created_at AND
        ((status='WAITING_PROFILE' AND profile_revision_id IS NULL AND evidence='[]'::jsonb AND request_ids='[]'::jsonb)
          OR (status='PREPARED' AND profile_revision_id IS NOT NULL AND jsonb_array_length(evidence) BETWEEN 1 AND 256 AND request_ids='[]'::jsonb)
          OR (status='REQUESTED' AND profile_revision_id IS NOT NULL AND jsonb_array_length(evidence) BETWEEN 1 AND 256 AND jsonb_array_length(request_ids) BETWEEN 1 AND 8)
          OR (status='STALE' AND request_ids='[]'::jsonb))
    )
);
CREATE INDEX anchor_source_discovery_due ON organizing.anchor_source_discovery(status,next_check_at,created_at,id)
    WHERE status IN ('WAITING_PROFILE','PREPARED');
CREATE INDEX anchor_source_discovery_anchor ON organizing.anchor_source_discovery(workspace_id,anchor_id,scope_version,status);

CREATE FUNCTION organizing.guard_anchor_source_discovery()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 OR NEW.status<>'WAITING_PROFILE' OR NEW.profile_revision_id IS NOT NULL
           OR NEW.evidence<>'[]'::jsonb OR NEW.request_ids<>'[]'::jsonb OR NEW.error_code IS NOT NULL THEN
            RAISE EXCEPTION 'anchor source discovery must begin waiting for a profile' USING ERRCODE='23514';
        END IF;
        IF NOT EXISTS (
            SELECT 1 FROM organizing.synthesis_processing p
            WHERE p.id=NEW.processing_id AND p.workspace_id=NEW.workspace_id
              AND p.status<>'SKIPPED' AND p.fusion_request_id IS NULL
              AND p.source_id=NEW.source_id AND p.source_version_id=NEW.source_version_id
              AND p.content_artifact_id=NEW.content_artifact_id AND p.parse_projection_id=NEW.parse_projection_id
              AND p.source_content_hash=NEW.source_content_hash
        ) THEN
            RAISE EXCEPTION 'anchor source discovery processing snapshot is invalid' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF TG_OP='DELETE' OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.processing_id<>OLD.processing_id OR NEW.anchor_id<>OLD.anchor_id OR NEW.scope_version<>OLD.scope_version
       OR NEW.source_id<>OLD.source_id OR NEW.source_version_id<>OLD.source_version_id
       OR NEW.content_artifact_id<>OLD.content_artifact_id OR NEW.parse_projection_id<>OLD.parse_projection_id
       OR NEW.source_content_hash<>OLD.source_content_hash OR NEW.created_at<>OLD.created_at
       OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'anchor source discovery transition is invalid' USING ERRCODE='55000';
    END IF;
    IF OLD.status IN ('REQUESTED','STALE') OR (OLD.profile_revision_id IS NOT NULL AND
       (NEW.profile_revision_id IS DISTINCT FROM OLD.profile_revision_id OR NEW.evidence IS DISTINCT FROM OLD.evidence))
       OR (OLD.request_ids<>'[]'::jsonb AND NEW.request_ids IS DISTINCT FROM OLD.request_ids) THEN
        RAISE EXCEPTION 'anchor source discovery snapshot is immutable' USING ERRCODE='55000';
    END IF;
    IF OLD.status='WAITING_PROFILE' AND NEW.status NOT IN ('WAITING_PROFILE','PREPARED','STALE') THEN
        RAISE EXCEPTION 'anchor source discovery transition is invalid' USING ERRCODE='55000';
    END IF;
    IF OLD.status='PREPARED' AND NEW.status NOT IN ('PREPARED','REQUESTED','STALE') THEN
        RAISE EXCEPTION 'anchor source discovery transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.profile_revision_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM learning.document_knowledge_profile_revision r
        WHERE r.id=NEW.profile_revision_id AND r.workspace_id=NEW.workspace_id
          AND r.source_version_id=NEW.source_version_id AND r.parse_projection_id=NEW.parse_projection_id
    ) THEN
        RAISE EXCEPTION 'anchor source discovery profile snapshot is invalid' USING ERRCODE='23514';
    END IF;
    IF jsonb_array_length(NEW.evidence)>0 AND EXISTS (
        SELECT 1 FROM jsonb_array_elements(NEW.evidence) AS item(value)
        LEFT JOIN ingestion.source_span span ON span.id=(item.value->>'source_span_id')::uuid
            AND span.workspace_id=NEW.workspace_id AND span.content_artifact_id=NEW.content_artifact_id
            AND span.parse_projection_id=NEW.parse_projection_id
        WHERE jsonb_typeof(item.value)<>'object' OR jsonb_typeof(item.value->'source')<>'object'
           OR (item.value->'source'->>'workspace_id') IS DISTINCT FROM NEW.workspace_id::text
           OR (item.value->'source'->>'source_id') IS DISTINCT FROM NEW.source_id::text
           OR (item.value->'source'->>'source_version_id') IS DISTINCT FROM NEW.source_version_id::text
           OR (item.value->'source'->>'content_artifact_id') IS DISTINCT FROM NEW.content_artifact_id::text
           OR (item.value->'source'->>'parse_projection_id') IS DISTINCT FROM NEW.parse_projection_id::text
           OR (item.value->'source'->>'content_hash') IS DISTINCT FROM NEW.source_content_hash
           OR span.id IS NULL OR (item.value->>'excerpt_hash') IS DISTINCT FROM span.excerpt_hash
           OR item.value->>'title' IS NULL OR (item.value->>'title') IS DISTINCT FROM btrim(item.value->>'title')
           OR octet_length(item.value->>'title') NOT BETWEEN 1 AND 512 OR item.value->>'title' ~ E'[\\r\\n]'
    ) THEN
        RAISE EXCEPTION 'anchor source discovery evidence snapshot is invalid' USING ERRCODE='23514';
    END IF;
    IF NEW.status='REQUESTED' THEN
        IF EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.request_ids) AS item(value)
            WHERE jsonb_typeof(item.value)<>'string' OR (item.value#>>'{}') !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$') THEN
            RAISE EXCEPTION 'anchor source discovery request identities are invalid' USING ERRCODE='23514';
        END IF;
        IF (SELECT count(*) FROM jsonb_array_elements_text(NEW.request_ids)) <>
           (SELECT count(DISTINCT value) FROM jsonb_array_elements_text(NEW.request_ids)) THEN
            RAISE EXCEPTION 'anchor source discovery request identities are duplicated' USING ERRCODE='23514';
        END IF;
        DECLARE request_item record; expected jsonb; position integer := 0; evidence_count integer;
        BEGIN
            FOR request_item IN SELECT value,ord FROM jsonb_array_elements_text(NEW.request_ids) WITH ORDINALITY AS item(value,ord) ORDER BY ord LOOP
                SELECT r.evidence INTO expected FROM organizing.anchor_recommendation_request r
                WHERE r.id=request_item.value::uuid AND r.workspace_id=NEW.workspace_id
                  AND r.kind='SOURCE_ASSOCIATION' AND r.anchor_id=NEW.anchor_id AND r.expected_scope_version=NEW.scope_version
                  AND r.source=jsonb_build_object('workspace_id',NEW.workspace_id::text,'source_id',NEW.source_id::text,
                       'source_version_id',NEW.source_version_id::text,'content_artifact_id',NEW.content_artifact_id::text,
                       'parse_projection_id',NEW.parse_projection_id::text,'content_hash',NEW.source_content_hash);
                IF expected IS NULL OR jsonb_array_length(expected) NOT BETWEEN 1 AND 32 THEN
                    RAISE EXCEPTION 'anchor source discovery request binding is invalid' USING ERRCODE='23514';
                END IF;
                evidence_count := jsonb_array_length(expected);
                SELECT jsonb_agg(value ORDER BY ord) INTO expected FROM jsonb_array_elements(NEW.evidence) WITH ORDINALITY AS item(value,ord)
                    WHERE ord>position AND ord<=position+evidence_count;
                IF expected IS NULL OR expected IS DISTINCT FROM (SELECT r.evidence FROM organizing.anchor_recommendation_request r WHERE r.id=request_item.value::uuid) THEN
                    RAISE EXCEPTION 'anchor source discovery request evidence is not consecutive' USING ERRCODE='23514';
                END IF;
                position := position+evidence_count;
            END LOOP;
            IF position<>jsonb_array_length(NEW.evidence) THEN
                RAISE EXCEPTION 'anchor source discovery request evidence is incomplete' USING ERRCODE='23514';
            END IF;
        END;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER anchor_source_discovery_guard BEFORE INSERT OR UPDATE OR DELETE ON organizing.anchor_source_discovery
    FOR EACH ROW EXECUTE FUNCTION organizing.guard_anchor_source_discovery();
