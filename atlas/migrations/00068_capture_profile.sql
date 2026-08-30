ALTER TABLE agent.model_run
    DROP CONSTRAINT IF EXISTS agent_model_run_final_result_type_check,
    ADD CONSTRAINT agent_model_run_final_result_type_check CHECK (
        final_result_type IS NULL OR final_result_type IN (
            'relation_assessment', 'rag_answer', 'refusal', 'faithfulness_review',
            'tool_request', 'clarification', 'artifact_section', 'document_knowledge_profile'
        )
    );

ALTER TABLE core.source_version
    DROP CONSTRAINT IF EXISTS uq_source_version_id_workspace,
    ADD CONSTRAINT uq_source_version_id_workspace UNIQUE (id,workspace_id);

ALTER TABLE ingestion.source_span
    ADD COLUMN evidence_kind text NOT NULL DEFAULT 'raw_bytes',
    ADD COLUMN derived_excerpt text NOT NULL DEFAULT '';

ALTER TABLE ingestion.source_span
    ADD CONSTRAINT source_span_evidence_shape CHECK (
        (evidence_kind='raw_bytes' AND derived_excerpt='')
        OR (
            evidence_kind='derived_text'
            AND derived_excerpt<>''
            AND octet_length(derived_excerpt)<=2097152
        )
    );

CREATE TABLE IF NOT EXISTS core.capture (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    kind text NOT NULL CHECK (kind IN ('TEXT','URL','FILE','IMAGE')),
    display_name text NOT NULL CHECK (
        display_name=btrim(display_name) AND btrim(display_name)<>''
        AND octet_length(display_name)<=512 AND display_name !~ E'[\\r\\n]'
    ),
    original_location text NOT NULL CHECK (
        original_location=btrim(original_location) AND btrim(original_location)<>''
        AND octet_length(original_location)<=4096 AND original_location !~ E'[\\r\\n]'
    ),
    original_url text CHECK (
        original_url IS NULL OR (
            original_url=btrim(original_url) AND btrim(original_url)<>''
            AND octet_length(original_url)<=8192 AND original_url !~ E'[\\r\\n]'
        )
    ),
    original_input_hash text CHECK (
        original_input_hash IS NULL OR original_input_hash ~ '^[0-9a-f]{64}$'
    ),
    source_id uuid NOT NULL,
    latest_source_version_id uuid,
    status text NOT NULL CHECK (status IN (
        'RECEIVED','SOURCE_SAVED','FETCHING','PROCESSING','READY','READY_DEGRADED',
        'FETCH_FAILED','PROCESSING_FAILED'
    )),
    fetch_status text NOT NULL CHECK (fetch_status IN (
        'PENDING','RUNNING','READY','FAILED','CAPABILITY_UNAVAILABLE','NOT_APPLICABLE'
    )),
    ingestion_status text NOT NULL CHECK (ingestion_status IN (
        'PENDING','RUNNING','READY','FAILED','CAPABILITY_UNAVAILABLE','NOT_APPLICABLE'
    )),
    index_status text NOT NULL CHECK (index_status IN (
        'PENDING','RUNNING','READY','FAILED','CAPABILITY_UNAVAILABLE','NOT_APPLICABLE'
    )),
    profile_status text NOT NULL CHECK (profile_status IN (
        'PENDING','RUNNING','READY','FAILED','CAPABILITY_UNAVAILABLE','STALE','NOT_APPLICABLE'
    )),
    failure_stage text NOT NULL DEFAULT '' CHECK (
        octet_length(failure_stage)<=128 AND failure_stage !~ E'[\\r\\n]'
    ),
    error_code text NOT NULL DEFAULT '' CHECK (
        octet_length(error_code)<=128 AND error_code !~ E'[\\r\\n]'
    ),
    retryable boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    captured_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_capture_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_capture_id_workspace_source UNIQUE (id,workspace_id,source_id),
    CONSTRAINT uq_capture_workspace_location UNIQUE (workspace_id,original_location),
    CONSTRAINT fk_capture_source_workspace FOREIGN KEY (source_id,workspace_id)
        REFERENCES core.source(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_capture_version_source FOREIGN KEY (latest_source_version_id,source_id)
        REFERENCES core.source_version(id,source_id) ON DELETE RESTRICT,
    CONSTRAINT capture_input_shape CHECK (
        (kind='URL' AND original_url IS NOT NULL AND original_input_hash IS NULL)
        OR (kind<>'URL' AND original_url IS NULL AND original_input_hash IS NOT NULL AND latest_source_version_id IS NOT NULL)
    ),
    CONSTRAINT capture_unmaterialized_state CHECK (
        latest_source_version_id IS NOT NULL
        OR (kind='URL' AND status IN ('RECEIVED','FETCHING','FETCH_FAILED'))
    ),
    CONSTRAINT capture_failure_shape CHECK (
        ((failure_stage='' AND error_code='' AND retryable=false)
          OR (failure_stage<>'' AND error_code<>''))
        AND (status NOT IN ('FETCH_FAILED','PROCESSING_FAILED') OR error_code<>'')
    ),
    CONSTRAINT capture_times CHECK (updated_at>=captured_at)
);

CREATE INDEX IF NOT EXISTS idx_capture_workspace_captured
    ON core.capture(workspace_id,captured_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_capture_workspace_status
    ON core.capture(workspace_id,status,updated_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_capture_source
    ON core.capture(workspace_id,source_id,id);

CREATE TABLE IF NOT EXISTS core.capture_command (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (
        idempotency_key=btrim(idempotency_key) AND btrim(idempotency_key)<>''
        AND octet_length(idempotency_key)<=128 AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (
        command_type IN ('CREATE_TEXT','CREATE_URL','CREATE_FILE','CREATE_IMAGE','RETRY_CAPTURE','RETRY_PROFILE')
    ),
    capture_id uuid NOT NULL,
    capture_version bigint NOT NULL CHECK (capture_version>0),
    response jsonb NOT NULL CHECK (jsonb_typeof(response)='object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id,idempotency_key),
    CONSTRAINT fk_capture_command_capture FOREIGN KEY (capture_id,workspace_id)
        REFERENCES core.capture(id,workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_capture_command_capture
    ON core.capture_command(workspace_id,capture_id,created_at DESC);

CREATE TABLE IF NOT EXISTS ops.capture_outbox (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    capture_id uuid NOT NULL,
    schema_version text NOT NULL CHECK (schema_version='capture-process-event/v1'),
    event_type text NOT NULL CHECK (event_type='capture.process_requested'),
    event_key text NOT NULL UNIQUE CHECK (
        event_key=btrim(event_key) AND btrim(event_key)<>'' AND octet_length(event_key)<=256
    ),
    payload jsonb NOT NULL CHECK (
        jsonb_typeof(payload)='object'
        AND payload ? 'capture_id' AND payload ? 'workspace_id'
    ),
    available_at timestamptz NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count>=0 AND attempt_count<=1000),
    last_error_code text CHECK (
        last_error_code IS NULL OR (
            btrim(last_error_code)<>'' AND octet_length(last_error_code)<=128
            AND last_error_code !~ E'[\\r\\n]'
        )
    ),
    lease_owner text CHECK (
        lease_owner IS NULL OR (btrim(lease_owner)<>'' AND octet_length(lease_owner)<=256)
    ),
    lease_expires_at timestamptz,
    published_at timestamptz,
    poisoned_at timestamptz,
    manual_recovery_required boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_capture_outbox_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT fk_capture_outbox_capture FOREIGN KEY (capture_id,workspace_id)
        REFERENCES core.capture(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT capture_outbox_lease_pair CHECK (
        (lease_owner IS NULL AND lease_expires_at IS NULL)
        OR (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
    ),
    CONSTRAINT capture_outbox_terminal_shape CHECK (
        NOT (published_at IS NOT NULL AND poisoned_at IS NOT NULL)
        AND (NOT manual_recovery_required OR poisoned_at IS NOT NULL)
        AND (published_at IS NULL OR published_at>=created_at)
        AND (poisoned_at IS NULL OR poisoned_at>=created_at)
        AND updated_at>=created_at
    )
);

CREATE INDEX IF NOT EXISTS idx_capture_outbox_pending
    ON ops.capture_outbox(available_at,id)
    WHERE published_at IS NULL AND poisoned_at IS NULL;

CREATE TABLE IF NOT EXISTS ops.capture_attempt (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    capture_id uuid NOT NULL,
    source_version_id uuid,
    workflow_run_id uuid NOT NULL,
    ingestion_attempt_id uuid,
    index_version_id uuid,
    profile_id uuid,
    attempt_number integer NOT NULL CHECK (attempt_number>0),
    stage text NOT NULL CHECK (stage IN ('FETCH','INGESTION','INDEX','PROFILE','COMPLETE')),
    status text NOT NULL CHECK (status IN ('RUNNING','SUCCEEDED','FAILED','CANCELLED')),
    error_code text NOT NULL DEFAULT '' CHECK (
        octet_length(error_code)<=128 AND error_code !~ E'[\\r\\n]'
    ),
    retryable boolean NOT NULL DEFAULT false,
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    CONSTRAINT uq_capture_attempt_workflow_number UNIQUE (capture_id,workflow_run_id,attempt_number),
    CONSTRAINT uq_capture_attempt_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT fk_capture_attempt_capture FOREIGN KEY (capture_id,workspace_id)
        REFERENCES core.capture(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_capture_attempt_source_version FOREIGN KEY (source_version_id,workspace_id)
        REFERENCES core.source_version(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_capture_attempt_workflow FOREIGN KEY (workflow_run_id,workspace_id)
        REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_capture_attempt_ingestion FOREIGN KEY (ingestion_attempt_id)
        REFERENCES ingestion.attempt(id) ON DELETE RESTRICT,
    CONSTRAINT fk_capture_attempt_index FOREIGN KEY (index_version_id,workspace_id)
        REFERENCES retrieval.index_version(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT capture_attempt_completion CHECK (
        (status='RUNNING' AND completed_at IS NULL AND error_code='' AND retryable=false)
        OR (status='SUCCEEDED' AND completed_at IS NOT NULL AND error_code='' AND retryable=false)
        OR (status IN ('FAILED','CANCELLED') AND completed_at IS NOT NULL AND error_code<>'')
    )
);

CREATE INDEX IF NOT EXISTS idx_capture_attempt_capture
    ON ops.capture_attempt(workspace_id,capture_id,attempt_number DESC);

CREATE TABLE IF NOT EXISTS learning.document_knowledge_profile (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    capture_id uuid NOT NULL,
    source_version_id uuid NOT NULL,
    current_revision_id uuid,
    status text NOT NULL CHECK (status IN (
        'PENDING','RUNNING','READY','FAILED','CAPABILITY_UNAVAILABLE','STALE'
    )),
    error_code text NOT NULL DEFAULT '' CHECK (
        octet_length(error_code)<=128 AND error_code !~ E'[\\r\\n]'
    ),
    retryable boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_document_profile_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_document_profile_revision_binding UNIQUE (id,workspace_id,source_version_id),
    CONSTRAINT uq_document_profile_source_version UNIQUE (workspace_id,source_version_id),
    CONSTRAINT fk_document_profile_capture FOREIGN KEY (capture_id,workspace_id)
        REFERENCES core.capture(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_source_version FOREIGN KEY (source_version_id,workspace_id)
        REFERENCES core.source_version(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT document_profile_state_shape CHECK (
        (status='READY' AND current_revision_id IS NOT NULL AND error_code='' AND retryable=false)
        OR (status IN ('PENDING','RUNNING') AND error_code='' AND retryable=false)
        OR (status='STALE' AND current_revision_id IS NOT NULL AND error_code='' AND retryable=false)
        OR (status='FAILED' AND error_code<>'')
        OR (status='CAPABILITY_UNAVAILABLE' AND error_code<>'' AND retryable=false)
    ),
    CONSTRAINT document_profile_times CHECK (updated_at>=created_at)
);

CREATE TABLE IF NOT EXISTS learning.document_knowledge_profile_revision (
    id uuid PRIMARY KEY,
    profile_id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    source_version_id uuid NOT NULL,
    parse_projection_id uuid NOT NULL,
    index_version_id uuid NOT NULL,
    model_run_id uuid NOT NULL,
    model_settings_revision bigint REFERENCES ops.model_settings_revisions(revision) ON DELETE RESTRICT,
    prompt_version text NOT NULL CHECK (
        btrim(prompt_version)<>'' AND octet_length(prompt_version)<=128
    ),
    schema_version text NOT NULL CHECK (schema_version='document-knowledge-profile/v1'),
    content jsonb NOT NULL CHECK (jsonb_typeof(content)='object'),
    content_digest text NOT NULL CHECK (content_digest ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_document_profile_revision_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_document_profile_revision_evidence UNIQUE (id,workspace_id,source_version_id),
    CONSTRAINT fk_document_profile_revision_profile FOREIGN KEY (profile_id,workspace_id,source_version_id)
        REFERENCES learning.document_knowledge_profile(id,workspace_id,source_version_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_revision_source_version FOREIGN KEY (source_version_id,workspace_id)
        REFERENCES core.source_version(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_revision_projection FOREIGN KEY (parse_projection_id,workspace_id)
        REFERENCES ingestion.parse_projection(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_revision_index FOREIGN KEY (index_version_id,workspace_id)
        REFERENCES retrieval.index_version(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_revision_model_run FOREIGN KEY (model_run_id,workspace_id)
        REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_document_profile_revision_contract
    ON learning.document_knowledge_profile_revision(
        profile_id,source_version_id,parse_projection_id,index_version_id,
        prompt_version,COALESCE(model_settings_revision,0),schema_version
    );

ALTER TABLE learning.document_knowledge_profile
    ADD CONSTRAINT fk_document_profile_current_revision
    FOREIGN KEY (current_revision_id,workspace_id,source_version_id)
    REFERENCES learning.document_knowledge_profile_revision(id,workspace_id,source_version_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE IF NOT EXISTS learning.document_knowledge_profile_attempt (
    id uuid PRIMARY KEY,
    profile_id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    source_version_id uuid NOT NULL,
    parse_projection_id uuid NOT NULL,
    index_version_id uuid NOT NULL,
    model_settings_revision bigint REFERENCES ops.model_settings_revisions(revision) ON DELETE RESTRICT,
    prompt_version text NOT NULL CHECK (
        btrim(prompt_version)<>'' AND octet_length(prompt_version)<=128
    ),
    schema_version text NOT NULL CHECK (schema_version='document-knowledge-profile/v1'),
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_attempt_id uuid NOT NULL,
    model_run_id uuid,
    attempt_number integer NOT NULL CHECK (attempt_number>0),
    status text NOT NULL CHECK (status IN ('RUNNING','SUCCEEDED','FAILED','CAPABILITY_UNAVAILABLE','CANCELLED')),
    error_code text NOT NULL DEFAULT '' CHECK (
        octet_length(error_code)<=128 AND error_code !~ E'[\\r\\n]'
    ),
    retryable boolean NOT NULL DEFAULT false,
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    CONSTRAINT uq_document_profile_attempt_number UNIQUE (profile_id,attempt_number),
    CONSTRAINT uq_document_profile_attempt_node UNIQUE (node_attempt_id),
    CONSTRAINT fk_document_profile_attempt_profile FOREIGN KEY (profile_id,workspace_id,source_version_id)
        REFERENCES learning.document_knowledge_profile(id,workspace_id,source_version_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_attempt_workflow FOREIGN KEY (workflow_run_id,workspace_id)
        REFERENCES workflow.run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_attempt_node FOREIGN KEY (node_run_id,workflow_run_id)
        REFERENCES workflow.node_run(id,run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_attempt_node_attempt FOREIGN KEY (node_attempt_id,node_run_id)
        REFERENCES workflow.node_attempt(id,node_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_attempt_model FOREIGN KEY (model_run_id,workspace_id)
        REFERENCES agent.model_run(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_attempt_projection FOREIGN KEY (parse_projection_id,workspace_id)
        REFERENCES ingestion.parse_projection(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_profile_attempt_index FOREIGN KEY (index_version_id,workspace_id)
        REFERENCES retrieval.index_version(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT document_profile_attempt_completion CHECK (
        (status='RUNNING' AND completed_at IS NULL AND error_code='' AND retryable=false)
        OR (status='SUCCEEDED' AND completed_at IS NOT NULL AND error_code='' AND retryable=false AND model_run_id IS NOT NULL)
        OR (status='CAPABILITY_UNAVAILABLE' AND completed_at IS NOT NULL AND error_code<>'' AND retryable=false AND model_run_id IS NULL)
        OR (status IN ('FAILED','CANCELLED') AND completed_at IS NOT NULL AND error_code<>'')
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_document_profile_attempt_active
    ON learning.document_knowledge_profile_attempt(profile_id)
    WHERE status='RUNNING';

CREATE TABLE IF NOT EXISTS learning.document_knowledge_profile_evidence (
    revision_id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    source_version_id uuid NOT NULL,
    source_span_id uuid NOT NULL REFERENCES ingestion.source_span(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (revision_id,source_span_id),
    CONSTRAINT fk_document_profile_evidence_revision FOREIGN KEY (revision_id,workspace_id,source_version_id)
        REFERENCES learning.document_knowledge_profile_revision(id,workspace_id,source_version_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_document_profile_workspace_updated
    ON learning.document_knowledge_profile(workspace_id,updated_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_document_profile_evidence_span
    ON learning.document_knowledge_profile_evidence(workspace_id,source_span_id,revision_id);

CREATE OR REPLACE FUNCTION core.validate_capture_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 THEN
            RAISE EXCEPTION 'capture must start at version one' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE'
       OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.kind<>OLD.kind
       OR NEW.original_location<>OLD.original_location
       OR NEW.original_url IS DISTINCT FROM OLD.original_url
       OR NEW.original_input_hash IS DISTINCT FROM OLD.original_input_hash
       OR NEW.source_id<>OLD.source_id OR NEW.captured_at<>OLD.captured_at
       OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'capture immutable field or version violation' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER capture_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON core.capture
    FOR EACH ROW EXECUTE FUNCTION core.validate_capture_write();

CREATE OR REPLACE FUNCTION learning.validate_document_profile_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 THEN
            RAISE EXCEPTION 'document profile must start at version one' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE'
       OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.capture_id<>OLD.capture_id OR NEW.source_version_id<>OLD.source_version_id
       OR NEW.created_at<>OLD.created_at OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'document profile immutable field or version violation' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER document_profile_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON learning.document_knowledge_profile
    FOR EACH ROW EXECUTE FUNCTION learning.validate_document_profile_write();

CREATE OR REPLACE FUNCTION learning.reject_capture_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'capture immutable fact cannot be mutated' USING ERRCODE='55000';
END;
$$;

CREATE TRIGGER capture_command_append_only
    BEFORE UPDATE OR DELETE ON core.capture_command
    FOR EACH ROW EXECUTE FUNCTION learning.reject_capture_immutable_mutation();
CREATE TRIGGER capture_command_reject_truncate
    BEFORE TRUNCATE ON core.capture_command
    FOR EACH STATEMENT EXECUTE FUNCTION learning.reject_capture_immutable_mutation();
CREATE TRIGGER document_profile_revision_append_only
    BEFORE UPDATE OR DELETE ON learning.document_knowledge_profile_revision
    FOR EACH ROW EXECUTE FUNCTION learning.reject_capture_immutable_mutation();
CREATE TRIGGER document_profile_revision_reject_truncate
    BEFORE TRUNCATE ON learning.document_knowledge_profile_revision
    FOR EACH STATEMENT EXECUTE FUNCTION learning.reject_capture_immutable_mutation();
CREATE TRIGGER document_profile_evidence_append_only
    BEFORE UPDATE OR DELETE ON learning.document_knowledge_profile_evidence
    FOR EACH ROW EXECUTE FUNCTION learning.reject_capture_immutable_mutation();
CREATE TRIGGER document_profile_evidence_reject_truncate
    BEFORE TRUNCATE ON learning.document_knowledge_profile_evidence
    FOR EACH STATEMENT EXECUTE FUNCTION learning.reject_capture_immutable_mutation();

CREATE OR REPLACE FUNCTION learning.validate_document_profile_evidence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    revision_projection_id uuid;
BEGIN
    SELECT parse_projection_id INTO revision_projection_id
      FROM learning.document_knowledge_profile_revision
     WHERE id=NEW.revision_id AND workspace_id=NEW.workspace_id
       AND source_version_id=NEW.source_version_id;
    IF revision_projection_id IS NULL OR NOT EXISTS (
        SELECT 1 FROM ingestion.source_span
         WHERE id=NEW.source_span_id AND workspace_id=NEW.workspace_id
           AND parse_projection_id=revision_projection_id
    ) THEN
        RAISE EXCEPTION 'document profile evidence is outside the frozen projection'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER document_profile_evidence_validate
    BEFORE INSERT ON learning.document_knowledge_profile_evidence
    FOR EACH ROW EXECUTE FUNCTION learning.validate_document_profile_evidence();

-- A committed model-settings revision changes the frozen profile contract.
-- Keep the old revision readable and surface an explicit rebuild action.
CREATE OR REPLACE FUNCTION learning.mark_document_profiles_stale_on_model_settings_activation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    candidate record;
    stale_at timestamptz := clock_timestamp();
BEGIN
    -- Capture completion locks Capture before reading Profile. Keep the trigger
    -- on the same order, and use the per-Source fence shared with Profile completion.
    FOR candidate IN
        SELECT profile.id,profile.workspace_id,profile.capture_id,profile.source_version_id
          FROM learning.document_knowledge_profile AS profile
          JOIN learning.document_knowledge_profile_revision AS revision
            ON revision.id=profile.current_revision_id
           AND revision.profile_id=profile.id
           AND revision.workspace_id=profile.workspace_id
           AND revision.source_version_id=profile.source_version_id
          JOIN core.capture AS capture
            ON capture.id=profile.capture_id
           AND capture.workspace_id=profile.workspace_id
           AND capture.latest_source_version_id=profile.source_version_id
         WHERE profile.status='READY'
           AND revision.model_settings_revision IS DISTINCT FROM NEW.active_revision
         ORDER BY profile.workspace_id,profile.capture_id,profile.source_version_id,profile.id
    LOOP
        PERFORM id
          FROM core.capture
         WHERE id=candidate.capture_id AND workspace_id=candidate.workspace_id
         FOR UPDATE;
        PERFORM pg_advisory_xact_lock(hashtextextended(
            candidate.workspace_id::text || chr(31) || candidate.source_version_id::text,0
        ));
        PERFORM id
          FROM learning.document_knowledge_profile
         WHERE id=candidate.id AND workspace_id=candidate.workspace_id
         FOR UPDATE;
    END LOOP;

    WITH candidates AS (
        SELECT profile.id,profile.workspace_id,profile.capture_id
          FROM learning.document_knowledge_profile AS profile
          JOIN learning.document_knowledge_profile_revision AS revision
            ON revision.id=profile.current_revision_id
           AND revision.profile_id=profile.id
           AND revision.workspace_id=profile.workspace_id
           AND revision.source_version_id=profile.source_version_id
          JOIN core.capture AS capture
            ON capture.id=profile.capture_id
           AND capture.workspace_id=profile.workspace_id
           AND capture.latest_source_version_id=profile.source_version_id
         WHERE profile.status='READY'
           AND revision.model_settings_revision IS DISTINCT FROM NEW.active_revision
    ), stale_profiles AS (
        UPDATE learning.document_knowledge_profile AS profile
           SET status='STALE',error_code='',retryable=false,
               version=profile.version+1,updated_at=GREATEST(profile.updated_at,stale_at)
          FROM candidates
         WHERE profile.id=candidates.id AND profile.workspace_id=candidates.workspace_id
        RETURNING profile.workspace_id,profile.capture_id
    )
    UPDATE core.capture AS capture
       SET profile_status='STALE',version=capture.version+1,
           updated_at=GREATEST(capture.updated_at,stale_at)
     FROM stale_profiles
     WHERE capture.workspace_id=stale_profiles.workspace_id
       AND capture.id=stale_profiles.capture_id
       AND capture.profile_status='READY';
    RETURN NEW;
END;
$$;

CREATE TRIGGER document_profile_model_settings_stale
    AFTER UPDATE OF active_revision ON ops.model_settings_state
    FOR EACH ROW
    WHEN (OLD.active_revision IS DISTINCT FROM NEW.active_revision)
    EXECUTE FUNCTION learning.mark_document_profiles_stale_on_model_settings_activation();

-- A newly activated manifest only invalidates Profiles whose exact Source Version
-- now resolves to another Parse Projection. Lock RUNNING Profiles on that Source
-- so their completion observes the activation before publishing its pointer.
CREATE OR REPLACE FUNCTION learning.mark_document_profiles_stale_on_index_activation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    candidate record;
    stale_at timestamptz := clock_timestamp();
BEGIN
    FOR candidate IN
        SELECT profile.id,profile.workspace_id,profile.capture_id,profile.source_version_id
          FROM learning.document_knowledge_profile AS profile
          JOIN retrieval.index_manifest_source AS manifest_source
            ON manifest_source.index_version_id=NEW.target_index_version_id
           AND manifest_source.workspace_id=NEW.workspace_id
           AND manifest_source.source_version_id=profile.source_version_id
           AND manifest_source.selection_status='included'
          LEFT JOIN learning.document_knowledge_profile_revision AS revision
            ON revision.id=profile.current_revision_id
           AND revision.profile_id=profile.id
           AND revision.workspace_id=profile.workspace_id
           AND revision.source_version_id=profile.source_version_id
         WHERE profile.workspace_id=NEW.workspace_id
           AND (
                profile.status='RUNNING'
                OR (profile.status='READY' AND revision.parse_projection_id<>manifest_source.parse_projection_id)
           )
         ORDER BY profile.workspace_id,profile.capture_id,profile.source_version_id,profile.id
    LOOP
        PERFORM id
          FROM core.capture
         WHERE id=candidate.capture_id AND workspace_id=candidate.workspace_id
         FOR UPDATE;
        PERFORM pg_advisory_xact_lock(hashtextextended(
            candidate.workspace_id::text || chr(31) || candidate.source_version_id::text,0
        ));
        PERFORM id
          FROM learning.document_knowledge_profile
         WHERE id=candidate.id AND workspace_id=candidate.workspace_id
         FOR UPDATE;
    END LOOP;

    WITH candidates AS (
        SELECT profile.id,profile.workspace_id,profile.capture_id
          FROM learning.document_knowledge_profile AS profile
          JOIN learning.document_knowledge_profile_revision AS revision
            ON revision.id=profile.current_revision_id
           AND revision.profile_id=profile.id
           AND revision.workspace_id=profile.workspace_id
           AND revision.source_version_id=profile.source_version_id
          JOIN retrieval.index_manifest_source AS manifest_source
            ON manifest_source.index_version_id=NEW.target_index_version_id
           AND manifest_source.workspace_id=NEW.workspace_id
           AND manifest_source.source_version_id=profile.source_version_id
           AND manifest_source.selection_status='included'
         WHERE profile.workspace_id=NEW.workspace_id
           AND profile.status='READY'
           AND revision.parse_projection_id<>manifest_source.parse_projection_id
    ), stale_profiles AS (
        UPDATE learning.document_knowledge_profile AS profile
           SET status='STALE',error_code='',retryable=false,
               version=profile.version+1,updated_at=GREATEST(profile.updated_at,stale_at)
          FROM candidates
         WHERE profile.id=candidates.id AND profile.workspace_id=candidates.workspace_id
        RETURNING profile.workspace_id,profile.capture_id,profile.source_version_id
    )
    UPDATE core.capture AS capture
       SET profile_status='STALE',version=capture.version+1,
           updated_at=GREATEST(capture.updated_at,stale_at)
      FROM stale_profiles
     WHERE capture.workspace_id=stale_profiles.workspace_id
       AND capture.id=stale_profiles.capture_id
       AND capture.latest_source_version_id=stale_profiles.source_version_id
       AND capture.profile_status='READY';
    RETURN NEW;
END;
$$;

CREATE TRIGGER document_profile_index_activation_stale
    AFTER INSERT ON retrieval.index_activation
    FOR EACH ROW
    EXECUTE FUNCTION learning.mark_document_profiles_stale_on_index_activation();

INSERT INTO core.schema_meta(key,value)
VALUES ('capture_profile','capture-profile-v1')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();
