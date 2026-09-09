-- Persistent semantic projections of generated Authoring revisions. Authoring
-- remains the only published pointer and Change Control owns every approval.
CREATE TABLE organizing.synthesis_note (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    document_id uuid NOT NULL UNIQUE,
    topic_key text NOT NULL CHECK (topic_key=btrim(topic_key) AND topic_key<>'' AND octet_length(topic_key)<=256),
    title text NOT NULL CHECK (title=btrim(title) AND title<>'' AND octet_length(title)<=512),
    aliases text[] NOT NULL DEFAULT '{}' CHECK (cardinality(aliases)<=16),
    current_revision_id uuid NOT NULL,
    version bigint NOT NULL CHECK (version>0),
    status text NOT NULL CHECK (status IN ('QUEUED','GENERATING','PENDING_APPROVAL','READY','FAILED','CONFLICT','CAPABILITY_UNAVAILABLE','RECOVERY_REQUIRED')),
    workflow_run_id uuid,
    failure jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL CHECK (updated_at>=created_at),
    CONSTRAINT uq_synthesis_note_scope UNIQUE (id,workspace_id,document_id),
    CONSTRAINT uq_synthesis_note_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_synthesis_note_topic UNIQUE (workspace_id,topic_key),
    CONSTRAINT fk_synthesis_note_document FOREIGN KEY (document_id,workspace_id)
        REFERENCES core.document(id,workspace_id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT fk_synthesis_note_workflow FOREIGN KEY (workflow_run_id,workspace_id)
        REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT synthesis_note_failure_shape CHECK ((
        (status IN ('FAILED','CONFLICT','CAPABILITY_UNAVAILABLE','RECOVERY_REQUIRED')
         AND failure IS NOT NULL AND jsonb_typeof(failure)='object'
         AND jsonb_typeof(failure->'code')='string' AND length(failure->>'code') BETWEEN 1 AND 128
         AND jsonb_typeof(failure->'retryable')='boolean'
         AND (failure - 'code' - 'retryable')='{}'::jsonb
         AND (status<>'RECOVERY_REQUIRED' OR failure->>'retryable'='false'))
        OR (status NOT IN ('FAILED','CONFLICT','CAPABILITY_UNAVAILABLE','RECOVERY_REQUIRED') AND failure IS NULL)
    ) IS TRUE),
    CONSTRAINT synthesis_note_generating_binding CHECK (status<>'GENERATING' OR workflow_run_id IS NOT NULL)
);

CREATE INDEX idx_synthesis_note_workspace_updated ON organizing.synthesis_note(workspace_id,updated_at DESC,id DESC);

-- Topic key and every alias share a single uniqueness namespace.
CREATE TABLE organizing.synthesis_topic_key (
    workspace_id uuid NOT NULL,
    topic_key text NOT NULL CHECK (topic_key=btrim(topic_key) AND topic_key<>'' AND octet_length(topic_key)<=256),
    note_id uuid NOT NULL,
    PRIMARY KEY (workspace_id,topic_key),
    CONSTRAINT fk_synthesis_topic_note FOREIGN KEY (note_id,workspace_id)
        REFERENCES organizing.synthesis_note(id,workspace_id) ON DELETE RESTRICT
);

CREATE TABLE organizing.synthesis_revision (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    note_id uuid NOT NULL,
    document_id uuid NOT NULL,
    article_revision_id uuid NOT NULL UNIQUE,
    revision_no integer NOT NULL CHECK (revision_no>0),
    article_revision_no integer NOT NULL CHECK (article_revision_no>0),
    parent_revision_id uuid,
    title text NOT NULL CHECK (title=btrim(title) AND title<>'' AND octet_length(title)<=512),
    renderer_version text NOT NULL CHECK (renderer_version='synthesis-markdown/v1'),
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    projection_hash text NOT NULL CHECK (projection_hash ~ '^[0-9a-f]{64}$'),
    items jsonb NOT NULL CHECK (jsonb_typeof(items)='array' AND jsonb_array_length(items) BETWEEN 1 AND 128),
    delta jsonb NOT NULL CHECK ((jsonb_typeof(delta)='object' AND jsonb_typeof(delta->'operations')='array'
        AND jsonb_array_length(delta->'operations') BETWEEN 1 AND 64 AND (delta - 'operations')='{}'::jsonb) IS TRUE),
    item_count integer NOT NULL CHECK (item_count BETWEEN 1 AND 128),
    conflict_count integer NOT NULL CHECK (conflict_count BETWEEN 0 AND 128),
    gap_count integer NOT NULL CHECK (gap_count BETWEEN 0 AND 128),
    open_gap_count integer NOT NULL CHECK (open_gap_count BETWEEN 0 AND gap_count),
    source_event_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    model_run_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_synthesis_revision_scope UNIQUE (id,workspace_id,note_id,document_id),
    CONSTRAINT uq_synthesis_revision_note UNIQUE (id,workspace_id,note_id),
    CONSTRAINT uq_synthesis_revision_no UNIQUE (note_id,revision_no),
    CONSTRAINT uq_synthesis_revision_projection UNIQUE (id,workspace_id,note_id,document_id,article_revision_id,projection_hash),
    CONSTRAINT fk_synthesis_revision_note FOREIGN KEY (note_id,workspace_id,document_id)
        REFERENCES organizing.synthesis_note(id,workspace_id,document_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_revision_parent FOREIGN KEY (parent_revision_id,workspace_id,note_id)
        REFERENCES organizing.synthesis_revision(id,workspace_id,note_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_revision_generated FOREIGN KEY (article_revision_id,workspace_id,document_id,note_id,id,projection_hash)
        REFERENCES authoring.generated_article_revision(article_revision_id,workspace_id,document_id,origin_id,origin_revision_id,projection_hash)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT fk_synthesis_revision_workflow FOREIGN KEY (workflow_run_id,workspace_id)
        REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_revision_model FOREIGN KEY (model_run_id,workspace_id)
        REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_revision_event FOREIGN KEY (source_event_id,workspace_id)
        REFERENCES workflow.outbox_event(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT synthesis_revision_parent_shape CHECK (
        (revision_no=1 AND parent_revision_id IS NULL) OR (revision_no>1 AND parent_revision_id IS NOT NULL AND parent_revision_id<>id)
    ),
    CONSTRAINT synthesis_revision_counts CHECK (item_count=jsonb_array_length(items) AND conflict_count+gap_count<=item_count)
);

ALTER TABLE organizing.synthesis_note ADD CONSTRAINT fk_synthesis_note_current
    FOREIGN KEY (current_revision_id,workspace_id,id,document_id)
    REFERENCES organizing.synthesis_revision(id,workspace_id,note_id,document_id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE authoring.generated_document ADD CONSTRAINT fk_generated_synthesis_note
    FOREIGN KEY (origin_id,workspace_id,document_id)
    REFERENCES organizing.synthesis_note(id,workspace_id,document_id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE authoring.generated_article_revision ADD CONSTRAINT fk_generated_synthesis_projection
    FOREIGN KEY (origin_revision_id,workspace_id,origin_id,document_id,article_revision_id,projection_hash)
    REFERENCES organizing.synthesis_revision(id,workspace_id,note_id,document_id,article_revision_id,projection_hash) DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE organizing.synthesis_revision_source (
    workspace_id uuid NOT NULL,
    revision_id uuid NOT NULL,
    note_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_version_id uuid NOT NULL,
    content_artifact_id uuid NOT NULL,
    parse_projection_id uuid NOT NULL,
    source_span_id uuid NOT NULL REFERENCES ingestion.source_span(id) ON DELETE RESTRICT,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    excerpt_hash text NOT NULL CHECK (excerpt_hash ~ '^[0-9a-f]{64}$'),
    title text NOT NULL CHECK (title<>'' AND octet_length(title)<=512),
    PRIMARY KEY (revision_id,source_version_id,parse_projection_id,source_span_id),
    CONSTRAINT fk_synthesis_source_revision FOREIGN KEY (revision_id,workspace_id,note_id)
        REFERENCES organizing.synthesis_revision(id,workspace_id,note_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_source_source FOREIGN KEY (source_id,workspace_id)
        REFERENCES core.source(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_source_version FOREIGN KEY (source_version_id,source_id)
        REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_source_projection FOREIGN KEY (source_version_id,parse_projection_id,workspace_id)
        REFERENCES ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id) ON DELETE RESTRICT
);
CREATE INDEX idx_synthesis_revision_source_version ON organizing.synthesis_revision_source(workspace_id,source_version_id,note_id);

CREATE TABLE organizing.synthesis_apply_receipt (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    processing_id uuid NOT NULL,
    processing_key text NOT NULL CHECK (processing_key<>'' AND octet_length(processing_key)<=256),
    source_event_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    model_run_id uuid NOT NULL,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    output_hash text NOT NULL CHECK (output_hash ~ '^[0-9a-f]{64}$'),
    binding_hash text NOT NULL CHECK (binding_hash ~ '^[0-9a-f]{64}$'),
    changed boolean NOT NULL,
    result jsonb NOT NULL CHECK ((jsonb_typeof(result)='object'
        AND jsonb_typeof(result->'revision_ids')='array'
        AND jsonb_typeof(result->'publications')='array'
        AND jsonb_array_length(result->'revision_ids')=jsonb_array_length(result->'publications')
        AND jsonb_array_length(result->'revision_ids')<=8
        AND (result->>'changed')::boolean=changed
        AND (result->>'replayed')::boolean=false
        AND result->>'processing_id'=processing_id::text
        AND changed=(jsonb_array_length(result->'revision_ids')>0)) IS TRUE),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id,processing_id),
    CONSTRAINT uq_synthesis_apply_source UNIQUE (workspace_id,processing_key),
    CONSTRAINT fk_synthesis_apply_event FOREIGN KEY (source_event_id,workspace_id)
        REFERENCES workflow.outbox_event(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_apply_workflow FOREIGN KEY (workflow_run_id,workspace_id)
        REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_synthesis_apply_model FOREIGN KEY (model_run_id,workspace_id)
        REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT
);

CREATE FUNCTION organizing.validate_synthesis_revision()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE parent_record organizing.synthesis_revision%ROWTYPE;
BEGIN
    PERFORM 1 FROM organizing.synthesis_note
     WHERE id=NEW.note_id AND workspace_id=NEW.workspace_id AND document_id=NEW.document_id AND title=NEW.title FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'synthesis note binding changed' USING ERRCODE='23514'; END IF;
    IF NEW.parent_revision_id IS NOT NULL THEN
        SELECT * INTO parent_record FROM organizing.synthesis_revision WHERE id=NEW.parent_revision_id;
        IF parent_record.revision_no<>NEW.revision_no-1 OR parent_record.article_revision_no<>NEW.article_revision_no-1
           OR parent_record.created_at>NEW.created_at THEN
            RAISE EXCEPTION 'synthesis revision parent changed' USING ERRCODE='23514';
        END IF;
    END IF;
    IF NEW.item_count<>jsonb_array_length(NEW.items)
       OR NEW.conflict_count<>(SELECT count(*) FROM jsonb_array_elements(NEW.items) item WHERE item->>'kind'='CONFLICT')
       OR NEW.gap_count<>(SELECT count(*) FROM jsonb_array_elements(NEW.items) item WHERE item->>'kind'='GAP')
       OR NEW.open_gap_count<>(SELECT count(*) FROM jsonb_array_elements(NEW.items) item WHERE item->>'kind'='GAP' AND item->'gap'->'resolution'='null'::jsonb) THEN
        RAISE EXCEPTION 'synthesis revision summary does not match its items' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_revision_validate BEFORE INSERT ON organizing.synthesis_revision
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_revision();

CREATE FUNCTION organizing.validate_synthesis_source()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM core.source_version v
        JOIN core.content_artifact a ON a.id=v.content_artifact_id AND a.workspace_id=NEW.workspace_id AND a.content_hash=v.content_hash
        JOIN ingestion.parse_projection pp ON pp.id=NEW.parse_projection_id AND pp.workspace_id=NEW.workspace_id AND pp.content_artifact_id=a.id
        JOIN ingestion.source_span sp ON sp.id=NEW.source_span_id AND sp.workspace_id=NEW.workspace_id AND sp.content_artifact_id=a.id
         AND sp.parse_projection_id=pp.id AND sp.parser_version=pp.parser_version AND sp.schema_version=pp.schema_version
        WHERE v.id=NEW.source_version_id AND v.source_id=NEW.source_id AND v.workspace_id=NEW.workspace_id
          AND a.id=NEW.content_artifact_id AND v.content_hash=NEW.content_hash AND sp.excerpt_hash=NEW.excerpt_hash
    ) THEN RAISE EXCEPTION 'synthesis source tuple changed' USING ERRCODE='23514'; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_source_validate BEFORE INSERT ON organizing.synthesis_revision_source
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_source();

CREATE FUNCTION organizing.validate_synthesis_note_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'synthesis history cannot be deleted' USING ERRCODE='55000'; END IF;
    IF (NEW.id,NEW.workspace_id,NEW.document_id,NEW.topic_key,NEW.title,NEW.aliases,NEW.created_at)
       IS DISTINCT FROM (OLD.id,OLD.workspace_id,OLD.document_id,OLD.topic_key,OLD.title,OLD.aliases,OLD.created_at)
       OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'synthesis identity or version changed' USING ERRCODE='23514';
    END IF;
    IF NEW.current_revision_id<>OLD.current_revision_id AND NOT EXISTS (
        SELECT 1 FROM organizing.synthesis_revision WHERE id=NEW.current_revision_id AND workspace_id=NEW.workspace_id
        AND note_id=NEW.id AND parent_revision_id=OLD.current_revision_id
    ) THEN RAISE EXCEPTION 'synthesis candidate does not extend current revision' USING ERRCODE='23514'; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_note_transition BEFORE UPDATE OR DELETE ON organizing.synthesis_note
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_note_transition();

CREATE TRIGGER synthesis_revision_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_revision
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_source_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_revision_source
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_topic_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_topic_key
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_apply_append_only BEFORE UPDATE OR DELETE ON organizing.synthesis_apply_receipt
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_note_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_note
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_revision_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_revision
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_source_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_revision_source
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_topic_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_topic_key
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER synthesis_apply_reject_truncate BEFORE TRUNCATE ON organizing.synthesis_apply_receipt
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

-- The semantic projection, original-source index and application receipt close
-- together. Same-Workspace identities alone are insufficient to prove a run.
ALTER TABLE agent.model_run ADD CONSTRAINT uq_synthesis_model_workflow
    UNIQUE (id,workspace_id,workflow_run_id);
ALTER TABLE organizing.synthesis_revision ADD CONSTRAINT fk_synthesis_revision_model_workflow
    FOREIGN KEY (model_run_id,workspace_id,workflow_run_id)
    REFERENCES agent.model_run(id,workspace_id,workflow_run_id) ON DELETE RESTRICT;
ALTER TABLE organizing.synthesis_apply_receipt ADD CONSTRAINT fk_synthesis_apply_model_workflow
    FOREIGN KEY (model_run_id,workspace_id,workflow_run_id)
    REFERENCES agent.model_run(id,workspace_id,workflow_run_id) ON DELETE RESTRICT;

CREATE FUNCTION organizing.synthesis_item_sources(items jsonb)
RETURNS SETOF jsonb LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT DISTINCT refs.source
    FROM jsonb_array_elements(items) item
    CROSS JOIN LATERAL (
        SELECT value AS source FROM jsonb_array_elements(COALESCE(item->'fact'->'sources','[]'::jsonb))
        UNION ALL
        SELECT source.value FROM jsonb_array_elements(COALESCE(item->'conflict'->'alternatives','[]'::jsonb)) alternative
            CROSS JOIN LATERAL jsonb_array_elements(COALESCE(alternative->'sources','[]'::jsonb)) source
        UNION ALL
        SELECT value FROM jsonb_array_elements(COALESCE(item->'gap'->'sources','[]'::jsonb))
        UNION ALL
        SELECT value FROM jsonb_array_elements(COALESCE(item->'gap'->'resolution'->'sources','[]'::jsonb))
    ) refs;
$$;

-- The closure triggers also have to hold after the creating transaction.
-- Append-only child tables must not admit extra historical sources or aliases.
CREATE FUNCTION organizing.validate_synthesis_membership()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='synthesis_topic_key' THEN
        PERFORM 1 FROM organizing.synthesis_note
         WHERE id=NEW.note_id AND workspace_id=NEW.workspace_id
           AND (topic_key=NEW.topic_key OR NEW.topic_key=ANY(aliases));
    ELSE
        PERFORM 1 FROM organizing.synthesis_revision r
        CROSS JOIN LATERAL organizing.synthesis_item_sources(r.items) reference
         WHERE r.id=NEW.revision_id AND r.workspace_id=NEW.workspace_id AND r.note_id=NEW.note_id
           AND reference=jsonb_build_object('source',jsonb_build_object(
                'workspace_id',NEW.workspace_id,'source_id',NEW.source_id,'source_version_id',NEW.source_version_id,
                'content_artifact_id',NEW.content_artifact_id,'parse_projection_id',NEW.parse_projection_id,'content_hash',NEW.content_hash),
                'source_span_id',NEW.source_span_id,'excerpt_hash',NEW.excerpt_hash,'title',NEW.title);
    END IF;
    IF NOT FOUND THEN RAISE EXCEPTION 'synthesis child is outside its immutable projection' USING ERRCODE='23514'; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER synthesis_topic_membership BEFORE INSERT ON organizing.synthesis_topic_key
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_membership();
CREATE TRIGGER synthesis_source_membership BEFORE INSERT ON organizing.synthesis_revision_source
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_synthesis_membership();

CREATE FUNCTION organizing.verify_synthesis_closure()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected_sources jsonb; actual_sources jsonb;
BEGIN
    IF TG_TABLE_NAME='synthesis_note' THEN
        IF (SELECT count(*) FROM organizing.synthesis_topic_key WHERE workspace_id=NEW.workspace_id AND note_id=NEW.id)
           <> cardinality(NEW.aliases)+1 OR EXISTS (
            SELECT 1 FROM organizing.synthesis_topic_key WHERE workspace_id=NEW.workspace_id AND note_id=NEW.id
             AND topic_key<>NEW.topic_key AND NOT topic_key=ANY(NEW.aliases)
        ) THEN RAISE EXCEPTION 'synthesis topic namespace is incomplete' USING ERRCODE='23514'; END IF;
    ELSE
        SELECT COALESCE(jsonb_agg(source ORDER BY source::text),'[]'::jsonb) INTO expected_sources
          FROM organizing.synthesis_item_sources(NEW.items) source;
        SELECT COALESCE(jsonb_agg(reference ORDER BY reference::text),'[]'::jsonb) INTO actual_sources FROM (
            SELECT jsonb_build_object('source',jsonb_build_object(
                'workspace_id',s.workspace_id,'source_id',s.source_id,'source_version_id',s.source_version_id,
                'content_artifact_id',s.content_artifact_id,'parse_projection_id',s.parse_projection_id,'content_hash',s.content_hash),
                'source_span_id',s.source_span_id,'excerpt_hash',s.excerpt_hash,'title',s.title) AS reference
            FROM organizing.synthesis_revision_source s WHERE s.revision_id=NEW.id AND s.workspace_id=NEW.workspace_id AND s.note_id=NEW.note_id
        ) source;
        IF actual_sources<>expected_sources OR jsonb_array_length(actual_sources)>256 OR NOT EXISTS (
            SELECT 1 FROM organizing.synthesis_apply_receipt receipt
             WHERE receipt.workspace_id=NEW.workspace_id AND receipt.workflow_run_id=NEW.workflow_run_id
               AND receipt.model_run_id=NEW.model_run_id AND receipt.source_event_id=NEW.source_event_id
               AND receipt.result->'revision_ids' @> jsonb_build_array(NEW.id::text)
        ) THEN RAISE EXCEPTION 'synthesis projection lacks its exact sources and receipt' USING ERRCODE='23514'; END IF;
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER synthesis_note_verify_closure AFTER INSERT ON organizing.synthesis_note
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_closure();
CREATE CONSTRAINT TRIGGER synthesis_revision_verify_closure AFTER INSERT ON organizing.synthesis_revision
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_closure();

CREATE FUNCTION organizing.verify_synthesis_receipt_publications()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE command jsonb; matched integer;
BEGIN
    IF (SELECT count(DISTINCT value) FROM jsonb_array_elements(NEW.result->'revision_ids'))
          <>jsonb_array_length(NEW.result->'revision_ids')
       OR (SELECT count(DISTINCT value->>'revision_id') FROM jsonb_array_elements(NEW.result->'publications'))
          <>jsonb_array_length(NEW.result->'publications') THEN
        RAISE EXCEPTION 'synthesis receipt repeats a revision publication' USING ERRCODE='23514';
    END IF;
    FOR command IN SELECT value FROM jsonb_array_elements(NEW.result->'publications') LOOP
        SELECT count(*) INTO matched FROM organizing.synthesis_revision r
        JOIN authoring.document_publication_reservation reservation
          ON reservation.workspace_id=r.workspace_id AND reservation.document_id=r.document_id
         AND reservation.article_revision_id=r.article_revision_id AND reservation.content_hash=r.content_hash
         AND reservation.idempotency_key=command->>'idempotency_key'
        WHERE r.workspace_id=NEW.workspace_id AND r.workflow_run_id=NEW.workflow_run_id
          AND r.model_run_id=NEW.model_run_id AND r.source_event_id=NEW.source_event_id
          AND r.id::text=command->>'revision_id' AND r.note_id::text=command->>'note_id'
          AND r.document_id::text=command->>'document_id' AND r.article_revision_id::text=command->>'article_revision_id'
          AND r.content_hash=command->>'content_hash'
          AND NEW.result->'revision_ids' @> jsonb_build_array(r.id::text);
        IF matched<>1 THEN RAISE EXCEPTION 'synthesis receipt publication binding changed' USING ERRCODE='23514'; END IF;
    END LOOP;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER synthesis_receipt_verify_publications AFTER INSERT ON organizing.synthesis_apply_receipt
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION organizing.verify_synthesis_receipt_publications();
