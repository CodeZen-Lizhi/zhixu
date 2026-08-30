ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (
        final_result_type IS NULL OR final_result_type IN (
            'relation_assessment','rag_answer','refusal','faithfulness_review',
            'tool_request','clarification','artifact_section','document_knowledge_profile',
            'organizing_outline','organizing_document'
        )
    );

CREATE SCHEMA IF NOT EXISTS organizing;

-- Evidence is a bounded frozen Citation tuple list. The owning modules still
-- revalidate reachability; this check prevents malformed JSON from becoming a
-- second, weak material identity.
CREATE OR REPLACE FUNCTION organizing.valid_evidence(value jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
    item jsonb;
    key_count integer;
    index_version text := NULL;
    item_key text;
    seen_keys text[] := ARRAY[]::text[];
    uuid_pattern constant text := '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
    hash_pattern constant text := '^[0-9a-f]{64}$';
BEGIN
    IF value IS NULL OR jsonb_typeof(value)<>'array' OR jsonb_array_length(value)>100 THEN
        RETURN false;
    END IF;
    FOR item IN SELECT element FROM jsonb_array_elements(value) AS elements(element)
    LOOP
        IF jsonb_typeof(item)<>'object' THEN
            RETURN false;
        END IF;
        SELECT count(*) INTO key_count FROM jsonb_object_keys(item);
        IF key_count<>6
           OR NOT (item ?& ARRAY[
                'index_version_id','chunk_id','source_version_id','source_span_id',
                'content_hash','excerpt_hash'
           ])
           OR jsonb_typeof(item->'index_version_id')<>'string'
           OR jsonb_typeof(item->'chunk_id')<>'string'
           OR jsonb_typeof(item->'source_version_id')<>'string'
           OR jsonb_typeof(item->'source_span_id')<>'string'
           OR jsonb_typeof(item->'content_hash')<>'string'
           OR jsonb_typeof(item->'excerpt_hash')<>'string'
           OR item->>'index_version_id' !~ uuid_pattern
           OR item->>'chunk_id' !~ uuid_pattern
           OR item->>'source_version_id' !~ uuid_pattern
           OR item->>'source_span_id' !~ uuid_pattern
           OR item->>'content_hash' !~ hash_pattern
           OR item->>'excerpt_hash' !~ hash_pattern THEN
            RETURN false;
        END IF;
        IF index_version IS NULL THEN
            index_version := item->>'index_version_id';
        ELSIF index_version<>item->>'index_version_id' THEN
            RETURN false;
        END IF;
        item_key := item::text;
        IF item_key=ANY(seen_keys) THEN
            RETURN false;
        END IF;
        seen_keys := array_append(seen_keys,item_key);
    END LOOP;
    RETURN true;
END;
$$;

-- Material identity is stored in typed columns. origin_collection_id is only
-- legal on expanded Snapshot members, never on a Draft row or collection marker.
CREATE OR REPLACE FUNCTION organizing.valid_material_shape(
    material_kind text,
    source_version_id uuid,
    document_id uuid,
    article_revision_id uuid,
    claim_id uuid,
    collection_id uuid,
    origin_collection_id uuid,
    profile_revision_id uuid,
    material_version bigint,
    content_hash text,
    query_hash text,
    read_model_revision text,
    evidence jsonb,
    allow_collection_origin boolean
)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
    hash_pattern constant text := '^[0-9a-f]{64}$';
BEGIN
    IF NOT organizing.valid_evidence(evidence)
       OR (origin_collection_id IS NOT NULL AND NOT allow_collection_origin)
       OR (origin_collection_id IS NOT NULL AND material_kind='SMART_COLLECTION') THEN
        RETURN false;
    END IF;
    RETURN COALESCE(CASE material_kind
        WHEN 'SOURCE_VERSION' THEN
            source_version_id IS NOT NULL AND material_version=0
            AND content_hash ~ hash_pattern
            AND document_id IS NULL AND article_revision_id IS NULL
            AND claim_id IS NULL AND collection_id IS NULL
            AND query_hash IS NULL AND read_model_revision IS NULL
        WHEN 'DOCUMENT_REVISION' THEN
            document_id IS NOT NULL AND article_revision_id IS NOT NULL
            AND material_version>0 AND content_hash ~ hash_pattern
            AND source_version_id IS NULL AND claim_id IS NULL
            AND collection_id IS NULL AND profile_revision_id IS NULL
            AND query_hash IS NULL AND read_model_revision IS NULL
        WHEN 'CLAIM' THEN
            claim_id IS NOT NULL AND material_version>0
            AND content_hash ~ hash_pattern AND jsonb_array_length(evidence)>0
            AND source_version_id IS NULL AND document_id IS NULL
            AND article_revision_id IS NULL AND collection_id IS NULL
            AND profile_revision_id IS NULL AND query_hash IS NULL
            AND read_model_revision IS NULL
        WHEN 'SMART_COLLECTION' THEN
            collection_id IS NOT NULL AND material_version>0
            AND query_hash ~ hash_pattern AND read_model_revision ~ hash_pattern
            AND source_version_id IS NULL AND document_id IS NULL
            AND article_revision_id IS NULL AND claim_id IS NULL
            AND origin_collection_id IS NULL AND profile_revision_id IS NULL
            AND content_hash IS NULL AND jsonb_array_length(evidence)=0
        ELSE false
    END,false);
END;
$$;

CREATE OR REPLACE FUNCTION organizing.valid_reason_codes(value text[])
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
    reason text;
    seen text[] := ARRAY[]::text[];
BEGIN
    IF value IS NULL OR cardinality(value)<1 OR cardinality(value)>8 THEN
        RETURN false;
    END IF;
    FOREACH reason IN ARRAY value
    LOOP
        IF reason IS NULL OR reason NOT IN (
            'HYBRID_MATCH','PROFILE_MATCH','ALIAS_MATCH','FORMAL_KNOWLEDGE',
            'COLLECTION_MEMBER','USER_ADDED'
        ) OR reason=ANY(seen) THEN
            RETURN false;
        END IF;
        seen := array_append(seen,reason);
    END LOOP;
    RETURN true;
END;
$$;

CREATE TABLE IF NOT EXISTS organizing.draft (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    intent text NOT NULL CHECK (
        intent=btrim(intent) AND intent<>'' AND octet_length(intent)<=4096
    ),
    status text NOT NULL CHECK (status IN ('EDITING','CONFIRMED')),
    template_revision_id uuid,
    confirmed_snapshot_id uuid,
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_organizing_draft_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT organizing_draft_status_shape CHECK (
        (status='EDITING' AND confirmed_snapshot_id IS NULL)
        OR (status='CONFIRMED' AND confirmed_snapshot_id IS NOT NULL)
    ),
    CONSTRAINT organizing_draft_time_order CHECK (updated_at>=created_at)
);

CREATE INDEX IF NOT EXISTS idx_organizing_draft_workspace_updated
    ON organizing.draft(workspace_id,updated_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS organizing.draft_version (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    draft_id uuid NOT NULL,
    version bigint NOT NULL CHECK (version>0),
    intent text NOT NULL CHECK (
        intent=btrim(intent) AND intent<>'' AND octet_length(intent)<=4096
    ),
    status text NOT NULL CHECK (status IN ('EDITING','CONFIRMED')),
    template_revision_id uuid,
    confirmed_snapshot_id uuid,
    draft_created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id,draft_id,version),
    CONSTRAINT fk_organizing_draft_version_draft
        FOREIGN KEY (draft_id,workspace_id)
        REFERENCES organizing.draft(id,workspace_id) ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT organizing_draft_version_status_shape CHECK (
        (status='EDITING' AND confirmed_snapshot_id IS NULL)
        OR (status='CONFIRMED' AND confirmed_snapshot_id IS NOT NULL)
    ),
    CONSTRAINT organizing_draft_version_time_order CHECK (updated_at>=draft_created_at)
);

ALTER TABLE organizing.draft
    ADD CONSTRAINT fk_organizing_draft_current_version
    FOREIGN KEY (workspace_id,id,version)
    REFERENCES organizing.draft_version(workspace_id,draft_id,version)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

-- Template and Revision have deferred circular references so one transaction
-- can insert a stable identity and immutable Revision 1 without a nullable gap.
CREATE TABLE IF NOT EXISTS organizing.template (
    id uuid PRIMARY KEY,
    workspace_id uuid REFERENCES core.workspace(id) ON DELETE RESTRICT,
    owner text NOT NULL CHECK (owner IN ('BUILT_IN','CUSTOM')),
    kind text NOT NULL CHECK (kind IN (
        'TOPIC_ARTICLE','MERGE_DOCUMENTS','KNOWLEDGE_REPORT','INTERVIEW_REVIEW'
    )),
    current_revision_id uuid NOT NULL,
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_organizing_template_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT organizing_template_owner_shape CHECK (
        (owner='BUILT_IN' AND workspace_id IS NULL)
        OR (owner='CUSTOM' AND workspace_id IS NOT NULL)
    ),
    CONSTRAINT organizing_template_time_order CHECK (updated_at>=created_at)
);

CREATE INDEX IF NOT EXISTS idx_organizing_template_visible
    ON organizing.template(workspace_id,kind,updated_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS organizing.template_revision (
    id uuid PRIMARY KEY,
    template_id uuid NOT NULL,
    workspace_id uuid REFERENCES core.workspace(id) ON DELETE RESTRICT,
    owner text NOT NULL CHECK (owner IN ('BUILT_IN','CUSTOM')),
    revision_no bigint NOT NULL CHECK (revision_no>0),
    kind text NOT NULL CHECK (kind IN (
        'TOPIC_ARTICLE','MERGE_DOCUMENTS','KNOWLEDGE_REPORT','INTERVIEW_REVIEW'
    )),
    schema_version text NOT NULL CHECK (schema_version='organizing-template/v1'),
    declaration jsonb NOT NULL CHECK (
        jsonb_typeof(declaration)='object'
        AND jsonb_typeof(declaration->'schema_version')='string'
        AND jsonb_typeof(declaration->'kind')='string'
        AND declaration->>'schema_version'='organizing-template/v1'
        AND declaration->>'kind'=kind
    ),
    declaration_hash text NOT NULL CHECK (declaration_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_organizing_template_revision_template_no UNIQUE (template_id,revision_no),
    CONSTRAINT uq_organizing_template_revision_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT fk_organizing_template_revision_template
        FOREIGN KEY (template_id) REFERENCES organizing.template(id)
        ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT organizing_template_revision_owner_shape CHECK (
        (owner='BUILT_IN' AND workspace_id IS NULL)
        OR (owner='CUSTOM' AND workspace_id IS NOT NULL)
    )
);

ALTER TABLE organizing.template
    ADD CONSTRAINT fk_organizing_template_current_revision
    FOREIGN KEY (current_revision_id) REFERENCES organizing.template_revision(id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE organizing.draft
    ADD CONSTRAINT fk_organizing_draft_template_revision
    FOREIGN KEY (template_revision_id) REFERENCES organizing.template_revision(id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE organizing.draft_version
    ADD CONSTRAINT fk_organizing_draft_version_template_revision
    FOREIGN KEY (template_revision_id) REFERENCES organizing.template_revision(id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE IF NOT EXISTS organizing.workflow_input_snapshot (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    draft_id uuid NOT NULL,
    draft_version bigint NOT NULL CHECK (draft_version>0),
    template_id uuid NOT NULL REFERENCES organizing.template(id) ON DELETE RESTRICT,
    template_revision_id uuid NOT NULL REFERENCES organizing.template_revision(id) ON DELETE RESTRICT,
    template_hash text NOT NULL CHECK (template_hash ~ '^[0-9a-f]{64}$'),
    intent text NOT NULL CHECK (
        intent=btrim(intent) AND intent<>'' AND octet_length(intent)<=4096
    ),
    snapshot_hash text NOT NULL CHECK (snapshot_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_organizing_snapshot_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_organizing_snapshot_draft UNIQUE (workspace_id,draft_id),
    CONSTRAINT fk_organizing_snapshot_draft
        FOREIGN KEY (draft_id,workspace_id)
        REFERENCES organizing.draft(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_snapshot_draft_version
        FOREIGN KEY (workspace_id,draft_id,draft_version)
        REFERENCES organizing.draft_version(workspace_id,draft_id,version) ON DELETE RESTRICT
);

ALTER TABLE organizing.draft
    ADD CONSTRAINT fk_organizing_draft_confirmed_snapshot
    FOREIGN KEY (confirmed_snapshot_id,workspace_id)
    REFERENCES organizing.workflow_input_snapshot(id,workspace_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE organizing.draft_version
    ADD CONSTRAINT fk_organizing_draft_version_confirmed_snapshot
    FOREIGN KEY (confirmed_snapshot_id,workspace_id)
    REFERENCES organizing.workflow_input_snapshot(id,workspace_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE IF NOT EXISTS organizing.draft_material (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    draft_id uuid NOT NULL,
    draft_version bigint NOT NULL CHECK (draft_version>0),
    position integer NOT NULL CHECK (position>=0 AND position<100),
    kind text NOT NULL,
    source_version_id uuid,
    document_id uuid,
    article_revision_id uuid,
    claim_id uuid,
    collection_id uuid,
    origin_collection_id uuid,
    profile_revision_id uuid,
    material_version bigint NOT NULL CHECK (material_version>=0),
    content_hash text,
    query_hash text,
    read_model_revision text,
    evidence jsonb NOT NULL,
    title text NOT NULL CHECK (
        title=btrim(title) AND title<>'' AND octet_length(title)<=512
    ),
    reasons text[] NOT NULL CHECK (organizing.valid_reason_codes(reasons)),
    origin text NOT NULL CHECK (origin IN ('SUGGESTED','USER')),
    availability text NOT NULL CHECK (availability IN ('AVAILABLE','STALE','UNAVAILABLE')),
    score double precision NOT NULL CHECK (
        score>=0 AND score<=1 AND score<>'NaN'::double precision
        AND score<>'Infinity'::double precision AND score<>'-Infinity'::double precision
    ),
    selected boolean NOT NULL,
    identity_key text GENERATED ALWAYS AS (
        CASE kind
            WHEN 'SOURCE_VERSION' THEN kind||':'||source_version_id::text
            WHEN 'DOCUMENT_REVISION' THEN kind||':'||document_id::text||':'||article_revision_id::text
            WHEN 'CLAIM' THEN kind||':'||claim_id::text
            WHEN 'SMART_COLLECTION' THEN kind||':'||collection_id::text
            ELSE NULL
        END
    ) STORED,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id,draft_id,draft_version,position),
    CONSTRAINT uq_organizing_draft_material_id UNIQUE (workspace_id,draft_id,draft_version,id),
    CONSTRAINT uq_organizing_draft_material_identity UNIQUE (workspace_id,draft_id,draft_version,identity_key),
    CONSTRAINT fk_organizing_draft_material_draft
        FOREIGN KEY (workspace_id,draft_id,draft_version)
        REFERENCES organizing.draft_version(workspace_id,draft_id,version) ON DELETE RESTRICT,
    CONSTRAINT organizing_draft_material_shape CHECK (organizing.valid_material_shape(
        kind,source_version_id,document_id,article_revision_id,claim_id,collection_id,
        origin_collection_id,profile_revision_id,material_version,content_hash,query_hash,
        read_model_revision,evidence,false
    ))
);

CREATE INDEX IF NOT EXISTS idx_organizing_draft_material_version
    ON organizing.draft_material(workspace_id,draft_id,draft_version,position);

CREATE TABLE IF NOT EXISTS organizing.workflow_input_material (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    snapshot_id uuid NOT NULL,
    position integer NOT NULL CHECK (position>=0 AND position<500),
    kind text NOT NULL,
    source_version_id uuid,
    document_id uuid,
    article_revision_id uuid,
    claim_id uuid,
    collection_id uuid,
    origin_collection_id uuid,
    profile_revision_id uuid,
    material_version bigint NOT NULL CHECK (material_version>=0),
    content_hash text,
    query_hash text,
    read_model_revision text,
    evidence jsonb NOT NULL,
    identity_key text GENERATED ALWAYS AS (
        CASE kind
            WHEN 'SOURCE_VERSION' THEN kind||':'||source_version_id::text
            WHEN 'DOCUMENT_REVISION' THEN kind||':'||document_id::text||':'||article_revision_id::text
            WHEN 'CLAIM' THEN kind||':'||claim_id::text
            WHEN 'SMART_COLLECTION' THEN kind||':'||collection_id::text
            ELSE NULL
        END
    ) STORED,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_organizing_snapshot_material_position UNIQUE (workspace_id,snapshot_id,position),
    CONSTRAINT uq_organizing_snapshot_material_identity UNIQUE (workspace_id,snapshot_id,identity_key),
    CONSTRAINT fk_organizing_snapshot_material_snapshot
        FOREIGN KEY (snapshot_id,workspace_id)
        REFERENCES organizing.workflow_input_snapshot(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT organizing_snapshot_material_shape CHECK (organizing.valid_material_shape(
        kind,source_version_id,document_id,article_revision_id,claim_id,collection_id,
        origin_collection_id,profile_revision_id,material_version,content_hash,query_hash,
        read_model_revision,evidence,true
    ))
);

CREATE INDEX IF NOT EXISTS idx_organizing_snapshot_material_order
    ON organizing.workflow_input_material(workspace_id,snapshot_id,position);

-- Expanded Smart Collection members must retain a marker for their selected
-- collection in the same immutable Snapshot. The trigger is deferred because
-- one batch may insert a member before its marker.
CREATE OR REPLACE FUNCTION organizing.validate_snapshot_material_origin()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.origin_collection_id IS NOT NULL AND NOT EXISTS (
        SELECT 1
          FROM organizing.workflow_input_material AS marker
         WHERE marker.workspace_id=NEW.workspace_id
           AND marker.snapshot_id=NEW.snapshot_id
           AND marker.kind='SMART_COLLECTION'
           AND marker.collection_id=NEW.origin_collection_id
           AND marker.origin_collection_id IS NULL
    ) THEN
        RAISE EXCEPTION 'organizing snapshot material collection origin is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER organizing_snapshot_material_origin_validate
    AFTER INSERT ON organizing.workflow_input_material
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_snapshot_material_origin();

CREATE TABLE IF NOT EXISTS organizing.run_binding (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    snapshot_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    definition_key text NOT NULL CHECK (
        definition_key=btrim(definition_key) AND definition_key<>''
        AND octet_length(definition_key)<=128 AND definition_key !~ E'[\\r\\n]'
    ),
    definition_version bigint NOT NULL CHECK (definition_version>0),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_organizing_run_binding_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_organizing_run_binding_snapshot UNIQUE (workspace_id,snapshot_id),
    CONSTRAINT uq_organizing_run_binding_workflow UNIQUE (workspace_id,workflow_run_id),
    CONSTRAINT fk_organizing_run_binding_snapshot
        FOREIGN KEY (snapshot_id,workspace_id)
        REFERENCES organizing.workflow_input_snapshot(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_run_binding_workflow
        FOREIGN KEY (workflow_run_id,workspace_id)
        REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION organizing.validate_run_binding_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM workflow.run AS run
          JOIN workflow.definition AS definition
            ON definition.id=run.definition_id
           AND definition.workspace_id=run.workspace_id
          JOIN organizing.workflow_input_snapshot AS snapshot
            ON snapshot.id=NEW.snapshot_id
           AND snapshot.workspace_id=NEW.workspace_id
          JOIN organizing.template_revision AS revision
            ON revision.id=snapshot.template_revision_id
         WHERE run.id=NEW.workflow_run_id
           AND run.workspace_id=NEW.workspace_id
           AND definition.key=NEW.definition_key
           AND definition.version=NEW.definition_version
           AND run.idempotency_key='snapshot:'||NEW.snapshot_id::text
           AND run.input=jsonb_build_object('snapshot_id',NEW.snapshot_id::text)
           AND NEW.definition_key=CASE revision.kind
                WHEN 'TOPIC_ARTICLE' THEN 'organizing.topic-article'
                WHEN 'MERGE_DOCUMENTS' THEN 'organizing.merge-documents'
                WHEN 'KNOWLEDGE_REPORT' THEN 'organizing.knowledge-report'
                WHEN 'INTERVIEW_REVIEW' THEN 'organizing.interview-review'
                ELSE NULL
           END
           AND NEW.definition_version=1
    ) THEN
        RAISE EXCEPTION 'organizing run binding definition is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER organizing_run_binding_validate_insert
    BEFORE INSERT ON organizing.run_binding
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_run_binding_insert();

-- A Generation is a response-loss fence between one Workflow node attempt,
-- its recorded Agent Model Run, and the exact strict JSON bytes consumed by
-- Artifact. output_document is bytea so its audited SHA-256 is never changed
-- by json/jsonb lexical normalization.
CREATE TABLE IF NOT EXISTS organizing.generation (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    snapshot_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_attempt_id uuid NOT NULL,
    generation_kind text NOT NULL CHECK (generation_kind IN ('OUTLINE','DOCUMENT')),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    model_settings_revision bigint CHECK (model_settings_revision IS NULL OR model_settings_revision>=0),
    model_run_id uuid,
    status text NOT NULL CHECK (status IN ('RUNNING','READY','FAILED','RECOVERY_REQUIRED')),
    output_document bytea,
    output_hash text CHECK (output_hash IS NULL OR output_hash ~ '^[0-9a-f]{64}$'),
    error_code text CHECK (
        error_code IS NULL OR (
            error_code=btrim(error_code) AND error_code<>''
            AND error_code ~ '^[A-Z0-9_]{1,128}$'
        )
    ),
    retryable boolean NOT NULL,
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_organizing_generation_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_organizing_generation_attempt UNIQUE (node_attempt_id),
    CONSTRAINT uq_organizing_generation_model_run UNIQUE (model_run_id),
    CONSTRAINT fk_organizing_generation_snapshot
        FOREIGN KEY (snapshot_id,workspace_id)
        REFERENCES organizing.workflow_input_snapshot(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_generation_workflow
        FOREIGN KEY (workflow_run_id,workspace_id)
        REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_generation_node
        FOREIGN KEY (node_run_id,workflow_run_id)
        REFERENCES workflow.node_run(id,run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_generation_attempt
        FOREIGN KEY (node_attempt_id,node_run_id)
        REFERENCES workflow.node_attempt(id,node_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_generation_model_run
        FOREIGN KEY (model_run_id,workspace_id)
        REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT organizing_generation_time_order CHECK (
        updated_at>=created_at AND (completed_at IS NULL OR completed_at=updated_at)
    ),
    CONSTRAINT organizing_generation_state_shape CHECK (
        CASE status
            WHEN 'RUNNING' THEN
                completed_at IS NULL AND output_document IS NULL AND output_hash IS NULL
                AND error_code IS NULL AND retryable=false
            WHEN 'READY' THEN
                model_run_id IS NOT NULL AND completed_at IS NOT NULL
                AND output_document IS NOT NULL AND octet_length(output_document)>0
                AND octet_length(output_document)<=524288 AND output_hash IS NOT NULL
                AND error_code IS NULL AND retryable=false
            WHEN 'FAILED' THEN
                model_run_id IS NOT NULL AND completed_at IS NOT NULL
                AND output_document IS NULL AND output_hash IS NULL AND error_code IS NOT NULL
            WHEN 'RECOVERY_REQUIRED' THEN
                model_run_id IS NOT NULL AND completed_at IS NOT NULL
                AND output_document IS NULL AND output_hash IS NULL
                AND error_code IS NOT NULL AND retryable=false
            ELSE false
        END
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_organizing_generation_ready_node
    ON organizing.generation(workspace_id,node_run_id)
    WHERE status='READY';

CREATE UNIQUE INDEX IF NOT EXISTS uq_organizing_generation_open_node
    ON organizing.generation(workspace_id,node_run_id)
    WHERE status='RUNNING';

CREATE INDEX IF NOT EXISTS idx_organizing_generation_workflow
    ON organizing.generation(workspace_id,workflow_run_id,node_run_id,created_at);

-- Artifact v2 keeps the v1 immutable Revision contract and adds exact,
-- verified document-revision provenance for every referenced document source.
ALTER TABLE learning.artifact_revision
    DROP CONSTRAINT IF EXISTS learning_artifact_revision_domain_schema_check,
    DROP CONSTRAINT IF EXISTS learning_artifact_revision_coverage_type_check,
    DROP CONSTRAINT IF EXISTS learning_artifact_revision_v1_shape_check,
    ADD CONSTRAINT learning_artifact_revision_domain_schema_check
        CHECK (domain_schema_version IN (
            'legacy/artifact-revision-v0','artifact-revision/v1','artifact-revision/v2'
        )),
    ADD CONSTRAINT learning_artifact_revision_coverage_type_check
        CHECK (
            domain_schema_version NOT IN ('artifact-revision/v1','artifact-revision/v2')
            OR jsonb_typeof(coverage) = 'array'
        ),
    ADD CONSTRAINT learning_artifact_revision_v1_shape_check
        CHECK (
            domain_schema_version NOT IN ('artifact-revision/v1','artifact-revision/v2')
            OR (
                status = 'SNAPSHOT'
                AND jsonb_typeof(coverage) = 'array'
                AND content_hash ~ '^[0-9a-f]{64}$'
                AND created_by_type IN ('HUMAN', 'AGENT')
                AND (
                    (created_by_type = 'HUMAN' AND generation_metadata IS NULL)
                    OR (created_by_type = 'AGENT' AND jsonb_typeof(generation_metadata) = 'object')
                )
            )
        );

CREATE OR REPLACE FUNCTION learning.reject_artifact_v1_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.domain_schema_version IN ('artifact-revision/v1','artifact-revision/v2') THEN
        RAISE EXCEPTION 'artifact v1 and v2 revisions are immutable'
            USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TABLE IF NOT EXISTS learning.artifact_revision_document_source (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    artifact_id uuid NOT NULL,
    revision_id uuid NOT NULL,
    document_id uuid NOT NULL,
    article_revision_id uuid NOT NULL,
    revision_no integer NOT NULL CHECK (revision_no > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    PRIMARY KEY (workspace_id,revision_id,document_id,article_revision_id),
    CONSTRAINT fk_learning_artifact_document_source_revision
        FOREIGN KEY (revision_id,workspace_id,artifact_id)
        REFERENCES learning.artifact_revision(id,workspace_id,artifact_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_document_source_article_revision
        FOREIGN KEY (article_revision_id,workspace_id,document_id)
        REFERENCES core.article_revision(id,workspace_id,document_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_learning_artifact_document_source_document
    ON learning.artifact_revision_document_source(workspace_id,document_id,article_revision_id);

CREATE OR REPLACE FUNCTION learning.reject_artifact_revision_document_source_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'artifact revision document sources are append-only'
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER artifact_revision_document_source_append_only
    BEFORE UPDATE OR DELETE ON learning.artifact_revision_document_source
    FOR EACH ROW EXECUTE FUNCTION learning.reject_artifact_revision_document_source_mutation();
CREATE TRIGGER artifact_revision_document_source_reject_truncate
    BEFORE TRUNCATE ON learning.artifact_revision_document_source
    FOR EACH STATEMENT EXECUTE FUNCTION learning.reject_artifact_revision_document_source_mutation();

CREATE OR REPLACE FUNCTION learning.project_artifact_revision_document_sources()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    document_source_count bigint;
BEGIN
    IF NEW.domain_schema_version IS DISTINCT FROM 'artifact-revision/v2' THEN
        RETURN NEW;
    END IF;

    IF jsonb_typeof(NEW.sections) IS DISTINCT FROM 'array'
       OR EXISTS (
           SELECT 1
           FROM jsonb_array_elements(NEW.sections) AS section(value)
           WHERE jsonb_typeof(section.value) IS DISTINCT FROM 'object'
              OR (
                  section.value ? 'DocumentSources'
                  AND jsonb_typeof(section.value->'DocumentSources') IS DISTINCT FROM 'array'
              )
              OR CASE
                  WHEN jsonb_typeof(section.value->'DocumentSources') = 'array'
                  THEN jsonb_array_length(section.value->'DocumentSources') > 64
                  ELSE false
              END
       ) THEN
        RAISE EXCEPTION 'artifact revision document source sections are invalid'
            USING ERRCODE='23514';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM jsonb_array_elements(NEW.sections) AS section(value)
        CROSS JOIN LATERAL jsonb_array_elements(
            COALESCE(section.value->'DocumentSources','[]'::jsonb)
        ) AS document_source(value)
        WHERE jsonb_typeof(document_source.value) IS DISTINCT FROM 'object'
           OR NOT (document_source.value ?& ARRAY[
               'DocumentID','ArticleRevisionID','RevisionNo','VerifiedContentHash','Verified'
           ])
           OR document_source.value - ARRAY[
               'DocumentID','ArticleRevisionID','RevisionNo','VerifiedContentHash','Verified'
           ] <> '{}'::jsonb
           OR jsonb_typeof(document_source.value->'DocumentID') IS DISTINCT FROM 'string'
           OR document_source.value->>'DocumentID' !~
                '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           OR jsonb_typeof(document_source.value->'ArticleRevisionID') IS DISTINCT FROM 'string'
           OR document_source.value->>'ArticleRevisionID' !~
                '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           OR document_source.value->>'DocumentID'=document_source.value->>'ArticleRevisionID'
           OR jsonb_typeof(document_source.value->'RevisionNo') IS DISTINCT FROM 'number'
           OR document_source.value->>'RevisionNo' !~ '^[1-9][0-9]{0,9}$'
           OR CASE
               WHEN document_source.value->>'RevisionNo' ~ '^[1-9][0-9]{0,9}$'
               THEN (document_source.value->>'RevisionNo')::bigint > 2147483647
               ELSE false
           END
           OR jsonb_typeof(document_source.value->'VerifiedContentHash') IS DISTINCT FROM 'string'
           OR document_source.value->>'VerifiedContentHash' !~ '^[0-9a-f]{64}$'
           OR document_source.value->'Verified' IS DISTINCT FROM 'true'::jsonb
    ) THEN
        RAISE EXCEPTION 'artifact revision document source is invalid'
            USING ERRCODE='23514';
    END IF;

    SELECT count(*) INTO document_source_count
    FROM jsonb_array_elements(NEW.sections) AS section(value)
    CROSS JOIN LATERAL jsonb_array_elements(
        COALESCE(section.value->'DocumentSources','[]'::jsonb)
    ) AS document_source(value);
    IF document_source_count = 0 THEN
        RAISE EXCEPTION 'artifact revision v2 requires at least one document source'
            USING ERRCODE='23514';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM jsonb_array_elements(NEW.sections) WITH ORDINALITY AS section(value,section_ordinality)
        CROSS JOIN LATERAL jsonb_array_elements(
            COALESCE(section.value->'DocumentSources','[]'::jsonb)
        ) AS document_source(value)
        GROUP BY section.section_ordinality,
                 document_source.value->>'DocumentID',
                 document_source.value->>'ArticleRevisionID',
                 document_source.value->>'VerifiedContentHash'
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'artifact revision section document source is duplicated'
            USING ERRCODE='23514';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM jsonb_array_elements(NEW.sections) AS section(value)
        CROSS JOIN LATERAL jsonb_array_elements(
            COALESCE(section.value->'DocumentSources','[]'::jsonb)
        ) AS document_source(value)
        LEFT JOIN core.document AS document
          ON document.id=(document_source.value->>'DocumentID')::uuid
         AND document.workspace_id=NEW.workspace_id
         AND document.lifecycle_status<>'DELETED'
        LEFT JOIN core.article_revision AS article_revision
          ON article_revision.id=(document_source.value->>'ArticleRevisionID')::uuid
         AND article_revision.workspace_id=NEW.workspace_id
         AND article_revision.document_id=(document_source.value->>'DocumentID')::uuid
         AND article_revision.revision_no=(document_source.value->>'RevisionNo')::integer
         AND article_revision.content_hash=document_source.value->>'VerifiedContentHash'
        WHERE document.id IS NULL OR article_revision.id IS NULL
    ) THEN
        RAISE EXCEPTION 'artifact revision document source is not an exact verified article revision'
            USING ERRCODE='23514';
    END IF;

    INSERT INTO learning.artifact_revision_document_source(
        workspace_id,artifact_id,revision_id,document_id,article_revision_id,revision_no,content_hash
    )
    SELECT DISTINCT NEW.workspace_id,NEW.artifact_id,NEW.id,
           (document_source.value->>'DocumentID')::uuid,
           (document_source.value->>'ArticleRevisionID')::uuid,
           (document_source.value->>'RevisionNo')::integer,
           document_source.value->>'VerifiedContentHash'
    FROM jsonb_array_elements(NEW.sections) AS section(value)
    CROSS JOIN LATERAL jsonb_array_elements(
        COALESCE(section.value->'DocumentSources','[]'::jsonb)
    ) AS document_source(value);
    RETURN NEW;
END;
$$;

CREATE TRIGGER artifact_revision_project_document_sources
    AFTER INSERT ON learning.artifact_revision
    FOR EACH ROW EXECUTE FUNCTION learning.project_artifact_revision_document_sources();

-- Citation selectors remain a required projection for both immutable Artifact
-- schemas. Their established v1 JSON contract is intentionally unchanged.
CREATE OR REPLACE FUNCTION learning.project_artifact_revision_citation_selectors()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.domain_schema_version NOT IN ('artifact-revision/v1','artifact-revision/v2') THEN
        RETURN NEW;
    END IF;

    IF jsonb_typeof(NEW.sections) IS DISTINCT FROM 'array'
       OR EXISTS (
           SELECT 1
           FROM jsonb_array_elements(NEW.sections) AS section(value)
           WHERE jsonb_typeof(section.value) IS DISTINCT FROM 'object'
              OR jsonb_typeof(section.value->'Citations') IS DISTINCT FROM 'array'
       ) THEN
        RAISE EXCEPTION 'artifact revision citation sections are invalid'
            USING ERRCODE='23514';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM jsonb_array_elements(NEW.sections) AS section(value)
        CROSS JOIN LATERAL jsonb_array_elements(section.value->'Citations') AS citation(value)
        WHERE jsonb_typeof(citation.value) IS DISTINCT FROM 'object'
           OR jsonb_typeof(citation.value->'SourceVersionID') IS DISTINCT FROM 'string'
           OR citation.value->>'SourceVersionID' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           OR jsonb_typeof(citation.value->'SourceSpanID') IS DISTINCT FROM 'string'
           OR citation.value->>'SourceSpanID' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           OR citation.value->>'SourceVersionID'=citation.value->>'SourceSpanID'
           OR citation.value->'Verified' IS DISTINCT FROM 'true'::jsonb
    ) THEN
        RAISE EXCEPTION 'artifact revision citation binding is invalid'
            USING ERRCODE='23514';
    END IF;

    INSERT INTO learning.artifact_revision_citation_selector(
        workspace_id,artifact_id,revision_id,source_version_id,source_span_id
    )
    SELECT DISTINCT NEW.workspace_id,NEW.artifact_id,NEW.id,
           (citation.value->>'SourceVersionID')::uuid,
           (citation.value->>'SourceSpanID')::uuid
    FROM jsonb_array_elements(NEW.sections) AS section(value)
    CROSS JOIN LATERAL jsonb_array_elements(section.value->'Citations') AS citation(value);
    RETURN NEW;
END;
$$;

-- Organizing generation creates the Model Run first and binds it immediately
-- afterwards. Its retrieval fields may be deferred only while the exact
-- matching Generation remains RUNNING.
ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS agent_model_run_retrieval_binding,
    ADD CONSTRAINT agent_model_run_retrieval_binding CHECK (
        retrieval_index_version_id IS NOT NULL
        OR (
            embedding_version_id IS NULL
            AND rerank_model_version IS NULL
            AND (
                (
                    output_schema_id = 'agent.rag-answer'
                    AND NOT (status = 'SUCCEEDED' AND final_result_type = 'rag_answer')
                )
                OR output_schema_id IN ('organizing.outline-generation','organizing.document-generation')
            )
        )
    );

CREATE OR REPLACE FUNCTION agent.guard_model_run_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent model run is append-only' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'RUNNING' OR NEW.version <> 1 THEN
            RAISE EXCEPTION 'agent model run must start running at version one' USING ERRCODE = '23514';
        END IF;
        IF NEW.retrieval_index_version_id IS NULL
           AND NEW.output_schema_id NOT IN (
               'agent.rag-answer','organizing.outline-generation','organizing.document-generation'
           ) THEN
            RAISE EXCEPTION 'only rag or organizing model run may defer retrieval binding' USING ERRCODE = '23514';
        END IF;
        IF NEW.retrieval_index_version_id IS NULL
           AND NEW.output_schema_id IN ('organizing.outline-generation','organizing.document-generation')
           AND NOT EXISTS (
               SELECT 1
               FROM organizing.generation AS generation
               WHERE generation.workspace_id = NEW.workspace_id
                 AND generation.workflow_run_id = NEW.workflow_run_id
                 AND generation.node_run_id = NEW.node_run_id
                 AND generation.node_attempt_id = NEW.node_attempt_id
                 AND generation.status = 'RUNNING'
                 AND (
                     (NEW.output_schema_id = 'organizing.outline-generation'
                         AND generation.generation_kind = 'OUTLINE')
                     OR (NEW.output_schema_id = 'organizing.document-generation'
                         AND generation.generation_kind = 'DOCUMENT')
                 )
           ) THEN
            RAISE EXCEPTION 'organizing model run requires an exact running generation binding'
                USING ERRCODE = '55000';
        END IF;
        IF NEW.memory_snapshot_id IS NULL AND EXISTS (
            SELECT 1
              FROM workflow.node_run node
             WHERE node.id = NEW.node_run_id
               AND node.run_id = NEW.workflow_run_id
               AND node.node_type = 'agent.rag-answer'
        ) THEN
            RAISE EXCEPTION 'conversation rag model run requires a memory snapshot binding' USING ERRCODE = '23514';
        END IF;
        IF NEW.memory_snapshot_id IS NOT NULL AND (
            NEW.output_schema_id <> 'agent.rag-answer'
            OR NOT EXISTS (
                SELECT 1
                  FROM workflow.node_run node
                 WHERE node.id = NEW.node_run_id
                   AND node.run_id = NEW.workflow_run_id
                   AND node.node_type = 'agent.rag-answer'
            )
        ) THEN
            RAISE EXCEPTION 'only conversation rag model run may bind a memory snapshot' USING ERRCODE = '23514';
        END IF;
        IF NEW.memory_snapshot_id IS NOT NULL AND NOT EXISTS (
            SELECT 1
              FROM agent.rag_memory_snapshot snapshot
             WHERE snapshot.id = NEW.memory_snapshot_id
               AND snapshot.workspace_id = NEW.workspace_id
               AND snapshot.workflow_run_id = NEW.workflow_run_id
               AND snapshot.node_run_id = NEW.node_run_id
               AND snapshot.node_attempt_id = NEW.node_attempt_id
               AND snapshot.status = 'PREPARING'
        ) THEN
            RAISE EXCEPTION 'rag model run memory snapshot is not preparing for this attempt' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status <> 'RUNNING'
       OR NEW.status = 'RUNNING'
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.node_attempt_id IS DISTINCT FROM OLD.node_attempt_id
       OR NEW.adapter_name IS DISTINCT FROM OLD.adapter_name
       OR NEW.adapter_version IS DISTINCT FROM OLD.adapter_version
       OR NEW.model_id IS DISTINCT FROM OLD.model_id
       OR NEW.model_version IS DISTINCT FROM OLD.model_version
       OR NEW.profile_id IS DISTINCT FROM OLD.profile_id
       OR NEW.profile_version IS DISTINCT FROM OLD.profile_version
       OR NEW.prompt_template_id IS DISTINCT FROM OLD.prompt_template_id
       OR NEW.prompt_template_version IS DISTINCT FROM OLD.prompt_template_version
       OR NEW.output_schema_id IS DISTINCT FROM OLD.output_schema_id
       OR NEW.output_schema_version IS DISTINCT FROM OLD.output_schema_version
       OR NEW.reduced_schema_id IS DISTINCT FROM OLD.reduced_schema_id
       OR NEW.reduced_schema_version IS DISTINCT FROM OLD.reduced_schema_version
       OR NEW.memory_snapshot_id IS DISTINCT FROM OLD.memory_snapshot_id
       OR NEW.memory_context_schema_version IS DISTINCT FROM OLD.memory_context_schema_version
       OR NEW.memory_context_digest IS DISTINCT FROM OLD.memory_context_digest
       OR NEW.memory_context_item_count IS DISTINCT FROM OLD.memory_context_item_count
       OR NEW.memory_context_bytes IS DISTINCT FROM OLD.memory_context_bytes
       OR (OLD.retrieval_index_version_id IS NOT NULL AND (
            NEW.retrieval_index_version_id IS DISTINCT FROM OLD.retrieval_index_version_id
            OR NEW.embedding_version_id IS DISTINCT FROM OLD.embedding_version_id
            OR NEW.rerank_model_version IS DISTINCT FROM OLD.rerank_model_version
       ))
       OR (OLD.retrieval_index_version_id IS NULL AND (
            OLD.embedding_version_id IS NOT NULL
            OR OLD.rerank_model_version IS NOT NULL
            OR (NEW.retrieval_index_version_id IS NULL AND (
                NEW.embedding_version_id IS NOT NULL OR NEW.rerank_model_version IS NOT NULL
            ))
       ))
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'agent model run transition or immutable binding is invalid' USING ERRCODE = '55000';
    END IF;
    IF EXISTS (
        SELECT 1 FROM agent.model_call
        WHERE model_run_id = NEW.id AND status = 'STARTED'
    ) THEN
        RAISE EXCEPTION 'agent model run cannot finalize with an active call' USING ERRCODE = '55000';
    END IF;
    IF NEW.status = 'SUCCEEDED' AND NOT EXISTS (
        SELECT 1 FROM agent.model_call
        WHERE model_run_id = NEW.id AND status = 'SUCCEEDED'
    ) THEN
        RAISE EXCEPTION 'agent successful model run requires a succeeded call' USING ERRCODE = '55000';
    END IF;
    IF NEW.status = 'REFUSED'
       AND EXISTS (SELECT 1 FROM agent.model_call WHERE model_run_id = NEW.id)
       AND NOT EXISTS (
            SELECT 1 FROM agent.model_call
            WHERE model_run_id = NEW.id AND status = 'SUCCEEDED'
       ) THEN
        RAISE EXCEPTION 'agent refused model run with calls requires a succeeded call' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TABLE IF NOT EXISTS organizing.workflow_start_outbox (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    snapshot_id uuid NOT NULL,
    template_revision_id uuid NOT NULL REFERENCES organizing.template_revision(id) ON DELETE RESTRICT,
    template_kind text NOT NULL CHECK (template_kind IN (
        'TOPIC_ARTICLE','MERGE_DOCUMENTS','KNOWLEDGE_REPORT','INTERVIEW_REVIEW'
    )),
    status text NOT NULL CHECK (status IN ('PENDING','STARTED','POISONED')),
    available_at timestamptz NOT NULL,
    attempt_count integer NOT NULL CHECK (attempt_count>=0 AND attempt_count<=1000),
    last_error_code text CHECK (
        last_error_code IS NULL OR (
            last_error_code=btrim(last_error_code) AND last_error_code<>''
            AND last_error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
        )
    ),
    lease_owner text CHECK (
        lease_owner IS NULL OR (
            lease_owner=btrim(lease_owner) AND lease_owner<>''
            AND octet_length(lease_owner)<=256 AND lease_owner !~ E'[\\r\\n]'
        )
    ),
    lease_until timestamptz,
    run_binding_id uuid,
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    started_at timestamptz,
    poisoned_at timestamptz,
    CONSTRAINT uq_organizing_start_outbox_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_organizing_start_outbox_snapshot UNIQUE (workspace_id,snapshot_id),
    CONSTRAINT fk_organizing_start_outbox_snapshot
        FOREIGN KEY (snapshot_id,workspace_id)
        REFERENCES organizing.workflow_input_snapshot(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_start_outbox_run_binding
        FOREIGN KEY (run_binding_id,workspace_id)
        REFERENCES organizing.run_binding(id,workspace_id) ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT organizing_start_outbox_lease_pair CHECK (
        (lease_owner IS NULL AND lease_until IS NULL)
        OR (lease_owner IS NOT NULL AND lease_until IS NOT NULL)
    ),
    CONSTRAINT organizing_start_outbox_state_shape CHECK (
        updated_at>=created_at
        AND (
            (status='PENDING' AND run_binding_id IS NULL AND started_at IS NULL AND poisoned_at IS NULL)
            OR (status='STARTED' AND run_binding_id IS NOT NULL AND started_at IS NOT NULL
                AND poisoned_at IS NULL AND lease_owner IS NULL AND lease_until IS NULL)
            OR (status='POISONED' AND run_binding_id IS NULL AND started_at IS NULL
                AND poisoned_at IS NOT NULL AND last_error_code IS NOT NULL
                AND lease_owner IS NULL AND lease_until IS NULL)
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_organizing_start_outbox_due
    ON organizing.workflow_start_outbox(available_at,id)
    WHERE status='PENDING';

CREATE INDEX IF NOT EXISTS idx_organizing_start_outbox_exhausted
    ON organizing.workflow_start_outbox(lease_until,id)
    WHERE status='PENDING' AND attempt_count=1000;

-- A RunBinding is only complete when the same transaction has closed the
-- unique Snapshot start outbox around that binding.
CREATE OR REPLACE FUNCTION organizing.validate_run_binding_completion()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM organizing.workflow_start_outbox AS outbox
          JOIN organizing.workflow_input_snapshot AS snapshot
            ON snapshot.id=outbox.snapshot_id
           AND snapshot.workspace_id=outbox.workspace_id
          JOIN organizing.template_revision AS revision
            ON revision.id=snapshot.template_revision_id
         WHERE outbox.workspace_id=NEW.workspace_id
           AND outbox.snapshot_id=NEW.snapshot_id
           AND outbox.run_binding_id=NEW.id
           AND outbox.status='STARTED'
           AND outbox.template_revision_id=snapshot.template_revision_id
           AND outbox.template_kind=revision.kind
    ) THEN
        RAISE EXCEPTION 'organizing run binding has no completed start outbox' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER organizing_run_binding_completion_validate
    AFTER INSERT ON organizing.run_binding
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_run_binding_completion();

CREATE TABLE IF NOT EXISTS organizing.run_result (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    run_binding_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL REFERENCES workflow.node_run(id) ON DELETE RESTRICT,
    result_kind text NOT NULL CHECK (result_kind IN ('MERGE_PROPOSAL','ARTIFACT')),
    result_ref uuid NOT NULL,
    result_hash text NOT NULL CHECK (result_hash ~ '^[0-9a-f]{64}$'),
    artifact_result_ref uuid GENERATED ALWAYS AS (
        CASE WHEN result_kind='ARTIFACT' THEN result_ref ELSE NULL END
    ) STORED,
    proposal_result_ref uuid GENERATED ALWAYS AS (
        CASE WHEN result_kind='MERGE_PROPOSAL' THEN result_ref ELSE NULL END
    ) STORED,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_organizing_run_result_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_organizing_run_result_binding UNIQUE (workspace_id,run_binding_id),
    CONSTRAINT uq_organizing_run_result_workflow UNIQUE (workspace_id,workflow_run_id),
    CONSTRAINT fk_organizing_run_result_binding
        FOREIGN KEY (run_binding_id,workspace_id)
        REFERENCES organizing.run_binding(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_run_result_snapshot
        FOREIGN KEY (snapshot_id,workspace_id)
        REFERENCES organizing.workflow_input_snapshot(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_run_result_workflow
        FOREIGN KEY (workflow_run_id,workspace_id)
        REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_run_result_artifact
        FOREIGN KEY (artifact_result_ref,workspace_id)
        REFERENCES learning.artifact(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_run_result_proposal
        FOREIGN KEY (proposal_result_ref)
        REFERENCES change_control.proposal(id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION organizing.validate_run_result_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM organizing.run_binding AS binding
          JOIN workflow.run AS run
            ON run.id=binding.workflow_run_id AND run.workspace_id=binding.workspace_id
          JOIN workflow.node_run AS node
            ON node.id=NEW.node_run_id AND node.run_id=run.id
          JOIN organizing.workflow_input_snapshot AS snapshot
            ON snapshot.id=binding.snapshot_id
           AND snapshot.workspace_id=binding.workspace_id
          JOIN organizing.template_revision AS revision
            ON revision.id=snapshot.template_revision_id
         WHERE binding.id=NEW.run_binding_id AND binding.workspace_id=NEW.workspace_id
           AND binding.snapshot_id=NEW.snapshot_id AND binding.workflow_run_id=NEW.workflow_run_id
           AND run.status='succeeded' AND node.status='succeeded'
           AND NEW.result_kind=CASE revision.kind
                WHEN 'MERGE_DOCUMENTS' THEN 'MERGE_PROPOSAL'
                WHEN 'TOPIC_ARTICLE' THEN 'ARTIFACT'
                WHEN 'KNOWLEDGE_REPORT' THEN 'ARTIFACT'
                WHEN 'INTERVIEW_REVIEW' THEN 'ARTIFACT'
                ELSE NULL
           END
           AND CASE NEW.result_kind
                WHEN 'ARTIFACT' THEN EXISTS (
                    SELECT 1
                      FROM learning.artifact AS artifact
                      JOIN learning.artifact_revision AS artifact_revision
                        ON artifact_revision.artifact_id=artifact.id
                       AND artifact_revision.workspace_id=artifact.workspace_id
                     WHERE artifact.id=NEW.result_ref
                       AND artifact.workspace_id=NEW.workspace_id
                       AND artifact_revision.content_hash=NEW.result_hash
                )
                WHEN 'MERGE_PROPOSAL' THEN EXISTS (
                    SELECT 1
                      FROM change_control.proposal AS proposal
                      JOIN change_control.proposal_revision AS proposal_revision
                        ON proposal_revision.proposal_id=proposal.id
                     WHERE proposal.id=NEW.result_ref
                       AND proposal.workspace_id=NEW.workspace_id
                       AND proposal.proposal_type='file_patch'
                       AND proposal_revision.change_hash=NEW.result_hash
                )
                ELSE false
           END
    ) THEN
        RAISE EXCEPTION 'organizing run result binding is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER organizing_run_result_validate_insert
    BEFORE INSERT ON organizing.run_result
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_run_result_insert();

CREATE TABLE IF NOT EXISTS organizing.command_receipt (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (
        idempotency_key=btrim(idempotency_key) AND idempotency_key<>''
        AND octet_length(idempotency_key)<=128 AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (command_type IN (
		'CREATE_DRAFT','UPDATE_DRAFT','ADD_MATERIAL','REMOVE_MATERIAL','SET_MATERIAL_SELECTION',
        'REPLACE_SUGGESTIONS','CONFIRM_DRAFT','CREATE_TEMPLATE','CLONE_TEMPLATE','REVISE_TEMPLATE'
    )),
    aggregate_id uuid NOT NULL,
    expected_version bigint NOT NULL CHECK (expected_version>=0),
    draft_id uuid,
    result_draft_version bigint,
    snapshot_id uuid,
    outbox_id uuid,
    template_id uuid,
    result_template_version bigint,
    template_revision_id uuid,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id,idempotency_key),
    CONSTRAINT fk_organizing_receipt_draft
        FOREIGN KEY (draft_id,workspace_id)
        REFERENCES organizing.draft(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_receipt_snapshot
        FOREIGN KEY (snapshot_id,workspace_id)
        REFERENCES organizing.workflow_input_snapshot(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_receipt_outbox
        FOREIGN KEY (outbox_id,workspace_id)
        REFERENCES organizing.workflow_start_outbox(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_receipt_template
        FOREIGN KEY (template_id,workspace_id)
        REFERENCES organizing.template(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_organizing_receipt_template_revision
        FOREIGN KEY (template_revision_id,workspace_id)
        REFERENCES organizing.template_revision(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT organizing_receipt_required_result CHECK (
        (
            command_type IN (
				'CREATE_DRAFT','UPDATE_DRAFT','ADD_MATERIAL','REMOVE_MATERIAL','SET_MATERIAL_SELECTION',
                'REPLACE_SUGGESTIONS','CONFIRM_DRAFT'
            )
            AND draft_id IS NOT NULL AND result_draft_version IS NOT NULL
        ) OR (
            command_type IN ('CREATE_TEMPLATE','CLONE_TEMPLATE','REVISE_TEMPLATE')
            AND template_id IS NOT NULL AND result_template_version IS NOT NULL
            AND template_revision_id IS NOT NULL
        )
    ),
    CONSTRAINT organizing_receipt_shape CHECK (
        CASE command_type
            WHEN 'CREATE_DRAFT' THEN
                expected_version=0 AND aggregate_id=draft_id AND result_draft_version=1
                AND snapshot_id IS NULL AND outbox_id IS NULL
                AND template_id IS NULL AND result_template_version IS NULL AND template_revision_id IS NULL
            WHEN 'UPDATE_DRAFT' THEN
                expected_version>0 AND aggregate_id=draft_id AND result_draft_version=expected_version+1
                AND snapshot_id IS NULL AND outbox_id IS NULL
                AND template_id IS NULL AND result_template_version IS NULL AND template_revision_id IS NULL
            WHEN 'ADD_MATERIAL' THEN
                expected_version>0 AND aggregate_id=draft_id AND result_draft_version=expected_version+1
                AND snapshot_id IS NULL AND outbox_id IS NULL
                AND template_id IS NULL AND result_template_version IS NULL AND template_revision_id IS NULL
            WHEN 'REMOVE_MATERIAL' THEN
                expected_version>0 AND aggregate_id=draft_id AND result_draft_version=expected_version+1
                AND snapshot_id IS NULL AND outbox_id IS NULL
                AND template_id IS NULL AND result_template_version IS NULL AND template_revision_id IS NULL
			WHEN 'SET_MATERIAL_SELECTION' THEN
				expected_version>0 AND aggregate_id=draft_id AND result_draft_version=expected_version+1
				AND snapshot_id IS NULL AND outbox_id IS NULL
				AND template_id IS NULL AND result_template_version IS NULL AND template_revision_id IS NULL
            WHEN 'REPLACE_SUGGESTIONS' THEN
                expected_version>0 AND aggregate_id=draft_id AND result_draft_version=expected_version+1
                AND snapshot_id IS NULL AND outbox_id IS NULL
                AND template_id IS NULL AND result_template_version IS NULL AND template_revision_id IS NULL
            WHEN 'CONFIRM_DRAFT' THEN
                expected_version>0 AND aggregate_id=draft_id AND result_draft_version=expected_version+1
                AND snapshot_id IS NOT NULL AND outbox_id IS NOT NULL
                AND template_id IS NULL AND result_template_version IS NULL AND template_revision_id IS NULL
            WHEN 'CREATE_TEMPLATE' THEN
                expected_version=0 AND aggregate_id=template_id AND result_template_version=1
                AND template_revision_id IS NOT NULL
                AND draft_id IS NULL AND result_draft_version IS NULL AND snapshot_id IS NULL AND outbox_id IS NULL
            WHEN 'CLONE_TEMPLATE' THEN
                expected_version=0 AND aggregate_id=template_id AND result_template_version=1
                AND template_revision_id IS NOT NULL
                AND draft_id IS NULL AND result_draft_version IS NULL AND snapshot_id IS NULL AND outbox_id IS NULL
            WHEN 'REVISE_TEMPLATE' THEN
                expected_version>0 AND aggregate_id=template_id AND result_template_version=expected_version+1
                AND template_revision_id IS NOT NULL
                AND draft_id IS NULL AND result_draft_version IS NULL AND snapshot_id IS NULL AND outbox_id IS NULL
            ELSE false
        END
    )
);

CREATE INDEX IF NOT EXISTS idx_organizing_receipt_aggregate
    ON organizing.command_receipt(workspace_id,aggregate_id,created_at DESC);

CREATE OR REPLACE FUNCTION organizing.reject_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'organizing immutable fact cannot be mutated' USING ERRCODE='55000';
END;
$$;

CREATE OR REPLACE FUNCTION organizing.validate_draft_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 OR NEW.status<>'EDITING' OR NEW.confirmed_snapshot_id IS NOT NULL THEN
            RAISE EXCEPTION 'organizing draft must start editable at version one' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE'
       OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.created_at<>OLD.created_at OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at OR OLD.status<>'EDITING'
       OR NEW.status NOT IN ('EDITING','CONFIRMED') THEN
        RAISE EXCEPTION 'organizing draft transition is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER organizing_draft_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON organizing.draft
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_draft_write();

CREATE OR REPLACE FUNCTION organizing.validate_generation_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.status<>'RUNNING' OR NEW.version<>1 OR NEW.model_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'organizing generation must start unbound and running' USING ERRCODE='23514';
        END IF;
        IF NOT EXISTS (
            SELECT 1
              FROM organizing.run_binding
             WHERE workspace_id=NEW.workspace_id
               AND snapshot_id=NEW.snapshot_id
               AND workflow_run_id=NEW.workflow_run_id
        ) THEN
            RAISE EXCEPTION 'organizing generation run binding is invalid' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE' OR OLD.status<>'RUNNING'
       OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.snapshot_id<>OLD.snapshot_id OR NEW.workflow_run_id<>OLD.workflow_run_id
       OR NEW.node_run_id<>OLD.node_run_id OR NEW.node_attempt_id<>OLD.node_attempt_id
       OR NEW.generation_kind<>OLD.generation_kind OR NEW.request_hash<>OLD.request_hash
       OR NEW.model_settings_revision IS DISTINCT FROM OLD.model_settings_revision
       OR NEW.created_at<>OLD.created_at OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'organizing generation transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.status='RUNNING' THEN
        IF OLD.model_run_id IS NOT NULL OR NEW.model_run_id IS NULL
           OR NEW.completed_at IS NOT NULL OR NEW.output_document IS NOT NULL
           OR NEW.output_hash IS NOT NULL OR NEW.error_code IS NOT NULL OR NEW.retryable THEN
            RAISE EXCEPTION 'organizing generation model binding is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.status IN ('READY','FAILED','RECOVERY_REQUIRED') THEN
        IF OLD.model_run_id IS NULL OR NEW.model_run_id<>OLD.model_run_id OR NEW.completed_at IS NULL THEN
            RAISE EXCEPTION 'organizing generation terminal transition is invalid' USING ERRCODE='55000';
        END IF;
    ELSE
        RAISE EXCEPTION 'organizing generation status is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER organizing_generation_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON organizing.generation
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_generation_write();

CREATE OR REPLACE FUNCTION organizing.validate_draft_version_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_version bigint;
    current_status text;
    current_created_at timestamptz;
    previous_updated_at timestamptz;
BEGIN
    SELECT version,status,created_at INTO current_version,current_status,current_created_at
      FROM organizing.draft
     WHERE id=NEW.draft_id AND workspace_id=NEW.workspace_id;
    IF NOT FOUND OR current_created_at<>NEW.draft_created_at THEN
        RAISE EXCEPTION 'organizing draft version owner is invalid' USING ERRCODE='23514';
    END IF;
    IF NEW.template_revision_id IS NOT NULL AND NOT EXISTS (
        SELECT 1
          FROM organizing.template_revision AS revision
          JOIN organizing.template AS template ON template.id=revision.template_id
         WHERE revision.id=NEW.template_revision_id
           AND (template.workspace_id=NEW.workspace_id OR template.workspace_id IS NULL)
    ) THEN
        RAISE EXCEPTION 'organizing draft template revision is not visible' USING ERRCODE='23514';
    END IF;
    IF NEW.version=1 THEN
        IF current_version<>1 OR NEW.status<>'EDITING' OR NEW.confirmed_snapshot_id IS NOT NULL THEN
            RAISE EXCEPTION 'organizing draft version one is invalid' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    SELECT updated_at INTO previous_updated_at
      FROM organizing.draft_version
     WHERE workspace_id=NEW.workspace_id AND draft_id=NEW.draft_id AND version=NEW.version-1
       AND status='EDITING';
    IF NOT FOUND OR current_version<>NEW.version-1 OR current_status<>'EDITING'
       OR NEW.updated_at<previous_updated_at THEN
        RAISE EXCEPTION 'organizing draft version sequence is invalid' USING ERRCODE='23514';
    END IF;
    IF NEW.status='CONFIRMED' AND NOT EXISTS (
        SELECT 1
          FROM organizing.workflow_input_snapshot AS snapshot
         WHERE snapshot.id=NEW.confirmed_snapshot_id
           AND snapshot.workspace_id=NEW.workspace_id
           AND snapshot.draft_id=NEW.draft_id
           AND snapshot.draft_version=NEW.version-1
           AND snapshot.template_revision_id=NEW.template_revision_id
    ) THEN
        RAISE EXCEPTION 'organizing confirmed draft snapshot is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER organizing_draft_version_validate_insert
    BEFORE INSERT ON organizing.draft_version
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_draft_version_insert();

CREATE OR REPLACE FUNCTION organizing.validate_draft_current_version()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM organizing.draft_version AS version
         WHERE version.workspace_id=NEW.workspace_id AND version.draft_id=NEW.id
           AND version.version=NEW.version AND version.intent=NEW.intent
           AND version.status=NEW.status
           AND version.template_revision_id IS NOT DISTINCT FROM NEW.template_revision_id
           AND version.confirmed_snapshot_id IS NOT DISTINCT FROM NEW.confirmed_snapshot_id
           AND version.draft_created_at=NEW.created_at AND version.updated_at=NEW.updated_at
    ) THEN
        RAISE EXCEPTION 'organizing draft current version is inconsistent' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER organizing_draft_current_version_validate
    AFTER INSERT OR UPDATE ON organizing.draft
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_draft_current_version();

CREATE OR REPLACE FUNCTION organizing.validate_template_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 THEN
            RAISE EXCEPTION 'organizing template must start at version one' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE' OR OLD.owner='BUILT_IN'
       OR NEW.id<>OLD.id OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.owner<>OLD.owner OR NEW.kind<>OLD.kind OR NEW.created_at<>OLD.created_at
       OR NEW.current_revision_id=OLD.current_revision_id
       OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'organizing template transition is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER organizing_template_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON organizing.template
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_template_write();

CREATE OR REPLACE FUNCTION organizing.validate_template_revision_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    template_row organizing.template%ROWTYPE;
BEGIN
    SELECT * INTO template_row FROM organizing.template WHERE id=NEW.template_id;
    IF NOT FOUND OR template_row.owner<>NEW.owner OR template_row.kind<>NEW.kind
       OR template_row.workspace_id IS DISTINCT FROM NEW.workspace_id THEN
        RAISE EXCEPTION 'organizing template revision binding is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER organizing_template_revision_validate_insert
    BEFORE INSERT ON organizing.template_revision
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_template_revision_insert();

CREATE OR REPLACE FUNCTION organizing.validate_template_current_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM organizing.template_revision AS revision
        WHERE revision.id=NEW.current_revision_id
          AND revision.template_id=NEW.id
          AND revision.workspace_id IS NOT DISTINCT FROM NEW.workspace_id
          AND revision.owner=NEW.owner AND revision.kind=NEW.kind
          AND revision.revision_no=NEW.version
    ) THEN
        RAISE EXCEPTION 'organizing template current revision is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER organizing_template_current_revision_validate
    AFTER INSERT OR UPDATE ON organizing.template
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_template_current_revision();

CREATE OR REPLACE FUNCTION organizing.validate_snapshot_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM template.id
      FROM organizing.template AS template
      JOIN organizing.template_revision AS revision
        ON revision.id=NEW.template_revision_id
       AND revision.template_id=template.id
       AND revision.declaration_hash=NEW.template_hash
     WHERE template.id=NEW.template_id
       AND template.current_revision_id=revision.id
       AND (template.workspace_id=NEW.workspace_id OR template.workspace_id IS NULL)
       AND revision.workspace_id IS NOT DISTINCT FROM template.workspace_id
     FOR SHARE OF template;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'organizing snapshot template binding is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER organizing_snapshot_validate_insert
    BEFORE INSERT ON organizing.workflow_input_snapshot
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_snapshot_insert();

CREATE OR REPLACE FUNCTION organizing.validate_start_outbox_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.status<>'PENDING' OR NEW.version<>1 OR NEW.attempt_count<>0
           OR NEW.lease_owner IS NOT NULL OR NEW.lease_until IS NOT NULL
           OR NEW.last_error_code IS NOT NULL OR NOT EXISTS (
                SELECT 1
                  FROM organizing.workflow_input_snapshot AS snapshot
                  JOIN organizing.template_revision AS revision
                    ON revision.id=snapshot.template_revision_id
                 WHERE snapshot.id=NEW.snapshot_id
                   AND snapshot.workspace_id=NEW.workspace_id
                   AND snapshot.template_revision_id=NEW.template_revision_id
                   AND revision.kind=NEW.template_kind
           ) THEN
            RAISE EXCEPTION 'organizing start outbox insert is invalid' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE' OR OLD.status<>'PENDING'
       OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.snapshot_id<>OLD.snapshot_id OR NEW.template_revision_id<>OLD.template_revision_id
       OR NEW.template_kind<>OLD.template_kind OR NEW.created_at<>OLD.created_at
       OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
       OR NEW.attempt_count<OLD.attempt_count OR NEW.attempt_count>OLD.attempt_count+1 THEN
        RAISE EXCEPTION 'organizing start outbox transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.status='PENDING' AND NEW.lease_owner IS NOT NULL THEN
        IF NEW.attempt_count<>OLD.attempt_count+1
           OR (OLD.lease_until IS NOT NULL AND OLD.lease_until>clock_timestamp()) THEN
            RAISE EXCEPTION 'organizing start outbox claim is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.status='PENDING' AND NEW.lease_owner IS NULL THEN
        IF OLD.lease_owner IS NULL OR NEW.attempt_count<>OLD.attempt_count
           OR NEW.available_at<OLD.available_at OR NEW.last_error_code IS NULL THEN
            RAISE EXCEPTION 'organizing start outbox retry is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.status='STARTED' THEN
        IF OLD.lease_owner IS NULL OR NEW.attempt_count<>OLD.attempt_count THEN
            RAISE EXCEPTION 'organizing start outbox completion is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF NEW.status='POISONED' THEN
        IF NEW.attempt_count<>OLD.attempt_count
           OR (OLD.lease_owner IS NULL AND OLD.attempt_count<1000) THEN
            RAISE EXCEPTION 'organizing start outbox poison is invalid' USING ERRCODE='55000';
        END IF;
    ELSE
        RAISE EXCEPTION 'organizing start outbox state is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER organizing_start_outbox_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON organizing.workflow_start_outbox
    FOR EACH ROW EXECUTE FUNCTION organizing.validate_start_outbox_write();

CREATE TRIGGER organizing_template_revision_append_only
    BEFORE UPDATE OR DELETE ON organizing.template_revision
    FOR EACH ROW EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_template_revision_reject_truncate
    BEFORE TRUNCATE ON organizing.template_revision
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_draft_version_append_only
    BEFORE UPDATE OR DELETE ON organizing.draft_version
    FOR EACH ROW EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_draft_version_reject_truncate
    BEFORE TRUNCATE ON organizing.draft_version
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_draft_material_append_only
    BEFORE UPDATE OR DELETE ON organizing.draft_material
    FOR EACH ROW EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_draft_material_reject_truncate
    BEFORE TRUNCATE ON organizing.draft_material
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_snapshot_append_only
    BEFORE UPDATE OR DELETE ON organizing.workflow_input_snapshot
    FOR EACH ROW EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_snapshot_reject_truncate
    BEFORE TRUNCATE ON organizing.workflow_input_snapshot
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_snapshot_material_append_only
    BEFORE UPDATE OR DELETE ON organizing.workflow_input_material
    FOR EACH ROW EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_snapshot_material_reject_truncate
    BEFORE TRUNCATE ON organizing.workflow_input_material
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_run_binding_append_only
    BEFORE UPDATE OR DELETE ON organizing.run_binding
    FOR EACH ROW EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_run_binding_reject_truncate
    BEFORE TRUNCATE ON organizing.run_binding
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_run_result_append_only
    BEFORE UPDATE OR DELETE ON organizing.run_result
    FOR EACH ROW EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_run_result_reject_truncate
    BEFORE TRUNCATE ON organizing.run_result
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_generation_reject_truncate
    BEFORE TRUNCATE ON organizing.generation
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_receipt_append_only
    BEFORE UPDATE OR DELETE ON organizing.command_receipt
    FOR EACH ROW EXECUTE FUNCTION organizing.reject_immutable_mutation();
CREATE TRIGGER organizing_receipt_reject_truncate
    BEFORE TRUNCATE ON organizing.command_receipt
    FOR EACH STATEMENT EXECUTE FUNCTION organizing.reject_immutable_mutation();

INSERT INTO core.schema_meta(key,value)
VALUES ('organizing_material_templates','organizing-material-templates-v1')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();
