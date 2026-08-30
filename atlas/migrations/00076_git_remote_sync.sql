CREATE TABLE IF NOT EXISTS ops.git_remote_config (
    workspace_id uuid PRIMARY KEY REFERENCES core.workspace(id) ON DELETE RESTRICT,
    configured boolean NOT NULL,
    normalized_url text,
    branch text,
    auto_sync boolean NOT NULL DEFAULT false,
    token_configured boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL CHECK (revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_git_remote_config_workspace_revision UNIQUE (workspace_id, revision),
    CONSTRAINT git_remote_config_shape CHECK (
        (
            configured
            AND normalized_url IS NOT NULL
            AND normalized_url ~ '^https://[^/?#@]+[^?#]*$'
            AND position('@' in normalized_url)=0
            AND normalized_url !~ E'[\\x00-\\x20\\x7f]'
            AND octet_length(normalized_url) <= 2048
            AND branch IS NOT NULL AND branch=btrim(branch) AND branch<>''
            AND octet_length(branch) <= 255 AND branch !~ E'[\\x00-\\x20\\x7f]'
        ) OR (
            NOT configured AND normalized_url IS NULL AND branch IS NULL
            AND NOT auto_sync AND NOT token_configured
        )
    ),
    CONSTRAINT git_remote_config_timestamps CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS ops.git_remote_config_revision (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    revision bigint NOT NULL CHECK (revision > 0),
    configured boolean NOT NULL,
    normalized_url text,
    branch text,
    auto_sync boolean NOT NULL,
    token_configured boolean NOT NULL,
    actor text NOT NULL CHECK (
        actor=btrim(actor) AND actor<>'' AND octet_length(actor)<=256 AND actor !~ E'[\\r\\n]'
    ),
    config_created_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, revision),
    CONSTRAINT git_remote_config_revision_shape CHECK (
        (
            configured
            AND normalized_url IS NOT NULL
            AND normalized_url ~ '^https://[^/?#@]+[^?#]*$'
            AND position('@' in normalized_url)=0
            AND normalized_url !~ E'[\\x00-\\x20\\x7f]'
            AND octet_length(normalized_url) <= 2048
            AND branch IS NOT NULL AND branch=btrim(branch) AND branch<>''
            AND octet_length(branch) <= 255 AND branch !~ E'[\\x00-\\x20\\x7f]'
        ) OR (
            NOT configured AND normalized_url IS NULL AND branch IS NULL
            AND NOT auto_sync AND NOT token_configured
        )
    ),
    CONSTRAINT git_remote_config_revision_timestamps CHECK (created_at>=config_created_at)
);

CREATE OR REPLACE FUNCTION ops.reject_git_remote_config_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'Git remote configuration revisions are append-only' USING ERRCODE='55000';
END;
$$;

DROP TRIGGER IF EXISTS git_remote_config_revision_append_only ON ops.git_remote_config_revision;
CREATE TRIGGER git_remote_config_revision_append_only
    BEFORE UPDATE OR DELETE ON ops.git_remote_config_revision
    FOR EACH ROW EXECUTE FUNCTION ops.reject_git_remote_config_revision_mutation();

CREATE OR REPLACE FUNCTION ops.validate_git_remote_config_projection()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'Git remote current configuration cannot be deleted' USING ERRCODE='55000';
    END IF;
    IF TG_OP='INSERT' AND NEW.revision<>1 THEN
        RAISE EXCEPTION 'Git remote initial configuration revision is invalid' USING ERRCODE='55000';
    END IF;
    IF TG_OP='UPDATE' AND (
        NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
        OR NEW.created_at IS DISTINCT FROM OLD.created_at
        OR NEW.revision<>OLD.revision+1
        OR NEW.updated_at<OLD.updated_at
    ) THEN
        RAISE EXCEPTION 'Git remote current configuration CAS is invalid' USING ERRCODE='55000';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM ops.git_remote_config_revision AS revision
        WHERE revision.workspace_id=NEW.workspace_id AND revision.revision=NEW.revision
          AND revision.configured=NEW.configured
          AND revision.normalized_url IS NOT DISTINCT FROM NEW.normalized_url
          AND revision.branch IS NOT DISTINCT FROM NEW.branch
          AND revision.auto_sync=NEW.auto_sync
          AND revision.token_configured=NEW.token_configured
          AND revision.config_created_at=NEW.created_at
          AND revision.created_at=NEW.updated_at
    ) THEN
        RAISE EXCEPTION 'Git remote current configuration lacks an exact revision fact' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS git_remote_config_validate_projection ON ops.git_remote_config;
CREATE TRIGGER git_remote_config_validate_projection
    BEFORE INSERT OR UPDATE OR DELETE ON ops.git_remote_config
    FOR EACH ROW EXECUTE FUNCTION ops.validate_git_remote_config_projection();

CREATE TABLE IF NOT EXISTS ops.git_remote_credential (
    workspace_id uuid PRIMARY KEY,
    config_revision bigint NOT NULL CHECK (config_revision > 0),
    key_id text NOT NULL CHECK (key_id=btrim(key_id) AND key_id<>'' AND octet_length(key_id)<=128),
    nonce bytea NOT NULL CHECK (octet_length(nonce) BETWEEN 12 AND 32),
    ciphertext bytea NOT NULL CHECK (octet_length(ciphertext) BETWEEN 17 AND 16640),
    aad_digest text NOT NULL CHECK (aad_digest ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT fk_git_remote_credential_config
        FOREIGN KEY (workspace_id, config_revision)
        REFERENCES ops.git_remote_config(workspace_id, revision) ON DELETE CASCADE,
    CONSTRAINT fk_git_remote_credential_revision
        FOREIGN KEY (workspace_id, config_revision)
        REFERENCES ops.git_remote_config_revision(workspace_id, revision) ON DELETE RESTRICT,
    CONSTRAINT git_remote_credential_timestamps CHECK (updated_at >= created_at)
);

CREATE OR REPLACE FUNCTION ops.validate_git_remote_credential_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_workspace uuid;
    expects_credential boolean;
    has_credential boolean;
BEGIN
    target_workspace := COALESCE(NEW.workspace_id, OLD.workspace_id);
    SELECT token_configured INTO expects_credential
    FROM ops.git_remote_config
    WHERE workspace_id=target_workspace;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    SELECT EXISTS (
        SELECT 1
        FROM ops.git_remote_credential AS credential
        JOIN ops.git_remote_config AS config
          ON config.workspace_id=credential.workspace_id
         AND config.revision=credential.config_revision
        WHERE credential.workspace_id=target_workspace
    ) INTO has_credential;
    IF expects_credential IS DISTINCT FROM has_credential THEN
        RAISE EXCEPTION 'Git remote token projection and credential disagree' USING ERRCODE='55000';
    END IF;
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS git_remote_config_validate_credential ON ops.git_remote_config;
CREATE CONSTRAINT TRIGGER git_remote_config_validate_credential
    AFTER INSERT OR UPDATE ON ops.git_remote_config
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ops.validate_git_remote_credential_binding();

DROP TRIGGER IF EXISTS git_remote_credential_validate_binding ON ops.git_remote_credential;
CREATE CONSTRAINT TRIGGER git_remote_credential_validate_binding
    AFTER INSERT OR UPDATE OR DELETE ON ops.git_remote_credential
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ops.validate_git_remote_credential_binding();

CREATE TABLE IF NOT EXISTS ops.git_remote_command_receipt (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (
        idempotency_key=btrim(idempotency_key) AND idempotency_key<>''
        AND octet_length(idempotency_key)<=128 AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (command_type IN ('SAVE','DELETE')),
    expected_revision bigint NOT NULL CHECK (expected_revision>=0),
    result_revision bigint NOT NULL CHECK (result_revision>0),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key),
    CONSTRAINT fk_git_remote_command_result
        FOREIGN KEY (workspace_id, result_revision)
        REFERENCES ops.git_remote_config_revision(workspace_id, revision) ON DELETE RESTRICT,
    CONSTRAINT git_remote_command_revision CHECK (result_revision=expected_revision+1)
);

CREATE OR REPLACE FUNCTION ops.valid_git_sync_path(value text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT octet_length(value) BETWEEN 1 AND 4096
       AND value=btrim(value)
       AND value !~ E'[\\x00-\\x1f\\x7f]'
       AND left(value,1)<>'/'
       AND right(value,1)<>'/'
       AND value<>'.'
       AND value !~ '(^|/)([.]|[.][.])(/|$)'
       AND value !~ '//';
$$;

CREATE OR REPLACE FUNCTION ops.valid_git_sync_changed_files(value jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    item jsonb;
    kind text;
    path_value text;
    old_path_value text;
BEGIN
    IF jsonb_typeof(value)<>'array' OR jsonb_array_length(value)>500
       OR octet_length(value::text)>1048576 THEN
        RETURN false;
    END IF;
    FOR item IN SELECT item_value FROM jsonb_array_elements(value) AS elements(item_value) LOOP
        IF jsonb_typeof(item)<>'object' OR item - ARRAY['path','old_path','kind']::text[] <> '{}'::jsonb
           OR jsonb_typeof(item->'path') IS DISTINCT FROM 'string'
           OR jsonb_typeof(item->'kind') IS DISTINCT FROM 'string' THEN
            RETURN false;
        END IF;
        kind := item->>'kind';
        path_value := item->>'path';
        IF kind NOT IN ('ADDED','MODIFIED','DELETED','RENAMED') OR NOT ops.valid_git_sync_path(path_value) THEN
            RETURN false;
        END IF;
        IF kind='RENAMED' THEN
            IF jsonb_typeof(item->'old_path') IS DISTINCT FROM 'string' THEN
                RETURN false;
            END IF;
            old_path_value := item->>'old_path';
            IF NOT ops.valid_git_sync_path(old_path_value) OR old_path_value=path_value THEN
                RETURN false;
            END IF;
        ELSIF item ? 'old_path' THEN
            RETURN false;
        END IF;
    END LOOP;
    RETURN true;
END;
$$;

CREATE TABLE IF NOT EXISTS ops.git_sync_run (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    config_revision bigint NOT NULL CHECK (config_revision>0),
    remote_url text NOT NULL CHECK (
        remote_url ~ '^https://[^/?#@]+[^?#]*$' AND position('@' in remote_url)=0
        AND remote_url !~ E'[\\x00-\\x20\\x7f]'
        AND octet_length(remote_url)<=2048
    ),
    branch text NOT NULL CHECK (
        branch=btrim(branch) AND branch<>'' AND octet_length(branch)<=255
        AND branch !~ E'[\\x00-\\x20\\x7f]'
    ),
    trigger text NOT NULL CHECK (trigger IN ('MANUAL','AUTOMATIC','RETRY')),
    retry_of_run_id uuid,
    idempotency_key text NOT NULL CHECK (
        idempotency_key=btrim(idempotency_key) AND idempotency_key<>''
        AND octet_length(idempotency_key)<=128 AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    status text NOT NULL CHECK (status IN (
        'PENDING','FETCHING','COMPARING','FAST_FORWARDING','PUSHING','VERIFYING',
        'SUCCEEDED','CONFLICT','FAILED','STALE','MANUAL_RECOVERY_REQUIRED'
    )),
    direction text NOT NULL CHECK (direction IN ('UNKNOWN','NONE','PULL','PUSH')),
    failure_class text NOT NULL CHECK (failure_class IN (
        'NONE','DIRTY','DETACHED','DIVERGED','AUTHENTICATION','OFFLINE','REF_DRIFT',
        'NON_FAST_FORWARD','STALE_CONFIG','RESULT_UNKNOWN','DEPENDENCY','INTERNAL'
    )),
    error_code text NOT NULL DEFAULT '' CHECK (
        octet_length(error_code)<=128 AND error_code !~ E'[\\r\\n]'
    ),
    retryable boolean NOT NULL DEFAULT false,
    expected_head_oid text CHECK (expected_head_oid IS NULL OR expected_head_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    expected_remote_oid text CHECK (expected_remote_oid IS NULL OR expected_remote_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    verified_head_oid text CHECK (verified_head_oid IS NULL OR verified_head_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    verified_remote_oid text CHECK (verified_remote_oid IS NULL OR verified_remote_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    changed_files jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (ops.valid_git_sync_changed_files(changed_files)),
    index_status text NOT NULL CHECK (index_status IN ('NOT_REQUIRED','PENDING','RUNNING','SUCCEEDED','FAILED')),
    index_error_code text NOT NULL DEFAULT '' CHECK (
        octet_length(index_error_code)<=128 AND index_error_code !~ E'[\\r\\n]'
    ),
    index_retryable boolean NOT NULL DEFAULT false,
    index_version_id uuid,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 1000),
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_git_sync_run_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_git_sync_run_command UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT fk_git_sync_run_config_revision
        FOREIGN KEY (workspace_id, config_revision)
        REFERENCES ops.git_remote_config_revision(workspace_id, revision) ON DELETE RESTRICT,
    CONSTRAINT fk_git_sync_run_retry
        FOREIGN KEY (retry_of_run_id, workspace_id)
        REFERENCES ops.git_sync_run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_git_sync_run_index
        FOREIGN KEY (index_version_id, workspace_id)
        REFERENCES retrieval.index_version(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT git_sync_run_retry_shape CHECK (
        (trigger='RETRY' AND retry_of_run_id IS NOT NULL)
        OR (trigger<>'RETRY' AND retry_of_run_id IS NULL)
    ),
    CONSTRAINT git_sync_run_ref_lengths CHECK (
        (expected_head_oid IS NULL OR expected_remote_oid IS NULL OR length(expected_head_oid)=length(expected_remote_oid))
        AND (verified_head_oid IS NULL OR verified_remote_oid IS NULL OR length(verified_head_oid)=length(verified_remote_oid))
    ),
    CONSTRAINT git_sync_run_terminal_shape CHECK (
        (
            status IN ('PENDING','FETCHING','COMPARING','FAST_FORWARDING','PUSHING','VERIFYING')
            AND completed_at IS NULL AND failure_class='NONE' AND error_code='' AND NOT retryable
        ) OR (
            status='SUCCEEDED' AND completed_at IS NOT NULL AND failure_class='NONE'
            AND error_code='' AND NOT retryable
            AND verified_head_oid IS NOT NULL AND verified_remote_oid IS NOT NULL
            AND verified_head_oid=verified_remote_oid
        ) OR (
            status IN ('CONFLICT','FAILED','STALE','MANUAL_RECOVERY_REQUIRED')
            AND completed_at IS NOT NULL AND failure_class<>'NONE' AND error_code<>''
        )
    ),
    CONSTRAINT git_sync_run_index_shape CHECK (
        (index_status IN ('NOT_REQUIRED','PENDING','RUNNING') AND index_error_code='' AND NOT index_retryable AND index_version_id IS NULL)
        OR (index_status='SUCCEEDED' AND index_error_code='' AND NOT index_retryable)
        OR (index_status='FAILED' AND index_error_code<>'' AND index_version_id IS NULL)
    ),
    CONSTRAINT git_sync_run_index_direction CHECK (
        (status='SUCCEEDED' AND direction='PULL' AND index_status IN ('PENDING','RUNNING','SUCCEEDED','FAILED'))
        OR (NOT (status='SUCCEEDED' AND direction='PULL') AND index_status='NOT_REQUIRED')
    ),
    CONSTRAINT git_sync_run_timestamps CHECK (
        updated_at>=created_at AND (completed_at IS NULL OR completed_at>=created_at)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_git_sync_run_workspace_active
    ON ops.git_sync_run(workspace_id)
    WHERE status IN ('PENDING','FETCHING','COMPARING','FAST_FORWARDING','PUSHING','VERIFYING');

CREATE INDEX IF NOT EXISTS idx_git_sync_run_workspace_created
    ON ops.git_sync_run(workspace_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS ops.git_sync_attempt (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    run_id uuid NOT NULL,
    attempt_no integer NOT NULL CHECK (attempt_no>0),
    status text NOT NULL CHECK (status IN ('RUNNING','SUCCEEDED','FAILED')),
    phase text NOT NULL CHECK (phase IN ('FETCHING','COMPARING','FAST_FORWARDING','PUSHING','VERIFYING','SUCCEEDED','CONFLICT','FAILED','STALE','MANUAL_RECOVERY_REQUIRED')),
    expected_head_oid text CHECK (expected_head_oid IS NULL OR expected_head_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    expected_remote_oid text CHECK (expected_remote_oid IS NULL OR expected_remote_oid ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    result_known boolean,
    error_code text NOT NULL DEFAULT '' CHECK (
        octet_length(error_code)<=128 AND error_code !~ E'[\\r\\n]'
    ),
    retryable boolean NOT NULL DEFAULT false,
    lease_owner text CHECK (
        lease_owner IS NULL OR (lease_owner=btrim(lease_owner) AND lease_owner<>'' AND octet_length(lease_owner)<=256)
    ),
    lease_expires_at timestamptz,
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    version bigint NOT NULL CHECK (version>0),
    CONSTRAINT uq_git_sync_attempt_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_git_sync_attempt_run_no UNIQUE (run_id, attempt_no),
    CONSTRAINT fk_git_sync_attempt_run
        FOREIGN KEY (run_id, workspace_id)
        REFERENCES ops.git_sync_run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT git_sync_attempt_ref_lengths CHECK (
        expected_head_oid IS NULL OR expected_remote_oid IS NULL OR length(expected_head_oid)=length(expected_remote_oid)
    ),
    CONSTRAINT git_sync_attempt_shape CHECK (
        (
            status='RUNNING' AND completed_at IS NULL AND error_code='' AND NOT retryable
            AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL
        ) OR (
            status='SUCCEEDED' AND completed_at IS NOT NULL AND error_code='' AND NOT retryable
            AND lease_owner IS NULL AND lease_expires_at IS NULL
        ) OR (
            status='FAILED' AND completed_at IS NOT NULL AND error_code<>''
            AND lease_owner IS NULL AND lease_expires_at IS NULL
        )
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_git_sync_attempt_run_active
    ON ops.git_sync_attempt(run_id) WHERE status='RUNNING';

CREATE TABLE IF NOT EXISTS ops.git_sync_outbox (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    run_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('EXECUTE_RUN','INDEX_FOLLOWUP')),
    event_key text NOT NULL UNIQUE CHECK (
        event_key=btrim(event_key) AND event_key<>'' AND octet_length(event_key)<=256 AND event_key !~ E'[\\r\\n]'
    ),
    available_at timestamptz NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 1000),
    last_error_code text CHECK (
        last_error_code IS NULL OR (last_error_code=btrim(last_error_code) AND last_error_code<>'' AND octet_length(last_error_code)<=128)
    ),
    lease_owner text CHECK (
        lease_owner IS NULL OR (lease_owner=btrim(lease_owner) AND lease_owner<>'' AND octet_length(lease_owner)<=256)
    ),
    lease_expires_at timestamptz,
    published_at timestamptz,
    poisoned_at timestamptz,
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_git_sync_outbox_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT fk_git_sync_outbox_run
        FOREIGN KEY (run_id, workspace_id)
        REFERENCES ops.git_sync_run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT git_sync_outbox_lease_shape CHECK (
        (lease_owner IS NULL AND lease_expires_at IS NULL)
        OR (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
    ),
    CONSTRAINT git_sync_outbox_terminal_shape CHECK (
        NOT (published_at IS NOT NULL AND poisoned_at IS NOT NULL)
        AND updated_at>=created_at
    )
);

CREATE INDEX IF NOT EXISTS idx_git_sync_outbox_pending
    ON ops.git_sync_outbox(available_at, id)
    WHERE published_at IS NULL AND poisoned_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_git_sync_outbox_run_pending_index
    ON ops.git_sync_outbox(run_id)
    WHERE kind='INDEX_FOLLOWUP' AND published_at IS NULL AND poisoned_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_writeback_execution_auto_sync_candidates
    ON change_control.writeback_execution(completed_at, id)
    WHERE status='completed' AND completed_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS ops.git_sync_index_retry_receipt (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (
        idempotency_key=btrim(idempotency_key) AND idempotency_key<>''
        AND octet_length(idempotency_key)<=128 AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    run_id uuid NOT NULL,
    result_version bigint NOT NULL CHECK (result_version>0),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key),
    CONSTRAINT fk_git_sync_index_retry_run
        FOREIGN KEY (run_id, workspace_id)
        REFERENCES ops.git_sync_run(id, workspace_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION ops.validate_git_sync_run_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.config_revision<>OLD.config_revision
       OR NEW.remote_url<>OLD.remote_url OR NEW.branch<>OLD.branch OR NEW.trigger<>OLD.trigger
       OR NEW.retry_of_run_id IS DISTINCT FROM OLD.retry_of_run_id OR NEW.idempotency_key<>OLD.idempotency_key
       OR NEW.request_hash<>OLD.request_hash OR NEW.created_at<>OLD.created_at
       OR NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'Git sync run immutable binding or version changed' USING ERRCODE='55000';
    END IF;

    IF OLD.status IN ('PENDING','FETCHING','COMPARING','FAST_FORWARDING','PUSHING','VERIFYING') THEN
        IF NOT (
	            (OLD.status='PENDING' AND NEW.status IN ('FETCHING','FAILED','STALE'))
            OR (OLD.status='FETCHING' AND NEW.status IN ('COMPARING','FAILED','STALE','MANUAL_RECOVERY_REQUIRED'))
            OR (OLD.status='COMPARING' AND NEW.status IN ('FAST_FORWARDING','PUSHING','SUCCEEDED','CONFLICT','FAILED','STALE','MANUAL_RECOVERY_REQUIRED'))
            OR (OLD.status='FAST_FORWARDING' AND NEW.status IN ('VERIFYING','FAILED','CONFLICT','MANUAL_RECOVERY_REQUIRED'))
            OR (OLD.status='PUSHING' AND NEW.status IN ('VERIFYING','FAILED','CONFLICT','MANUAL_RECOVERY_REQUIRED'))
            OR (OLD.status='VERIFYING' AND NEW.status IN ('SUCCEEDED','FAILED','CONFLICT','MANUAL_RECOVERY_REQUIRED'))
        ) THEN
            RAISE EXCEPTION 'Git sync run transition is invalid' USING ERRCODE='55000';
        END IF;
    ELSIF OLD.status='SUCCEEDED' AND NEW.status='SUCCEEDED' THEN
        IF NEW.id IS DISTINCT FROM OLD.id OR NEW.expected_head_oid IS DISTINCT FROM OLD.expected_head_oid
           OR NEW.expected_remote_oid IS DISTINCT FROM OLD.expected_remote_oid
           OR NEW.verified_head_oid IS DISTINCT FROM OLD.verified_head_oid
           OR NEW.verified_remote_oid IS DISTINCT FROM OLD.verified_remote_oid
           OR NEW.changed_files IS DISTINCT FROM OLD.changed_files
           OR NEW.direction<>OLD.direction OR NEW.failure_class<>OLD.failure_class
           OR NEW.error_code<>OLD.error_code OR NEW.retryable<>OLD.retryable
           OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
           OR NOT (
	               (OLD.index_status='PENDING' AND NEW.index_status IN ('RUNNING','FAILED'))
               OR (OLD.index_status='RUNNING' AND NEW.index_status IN ('SUCCEEDED','FAILED'))
               OR (OLD.index_status='FAILED' AND NEW.index_status='PENDING')
           ) THEN
            RAISE EXCEPTION 'Git sync index follow-up transition is invalid' USING ERRCODE='55000';
        END IF;
    ELSE
        RAISE EXCEPTION 'terminal Git sync result is immutable' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS git_sync_run_validate_update ON ops.git_sync_run;
CREATE TRIGGER git_sync_run_validate_update
    BEFORE UPDATE ON ops.git_sync_run
    FOR EACH ROW EXECUTE FUNCTION ops.validate_git_sync_run_update();

CREATE OR REPLACE FUNCTION ops.validate_git_sync_attempt_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id OR NEW.run_id<>OLD.run_id
       OR NEW.attempt_no<>OLD.attempt_no OR NEW.started_at<>OLD.started_at
       OR NEW.version<>OLD.version+1 OR OLD.status<>'RUNNING' THEN
        RAISE EXCEPTION 'Git sync attempt immutable binding or transition changed' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS git_sync_attempt_validate_update ON ops.git_sync_attempt;
CREATE TRIGGER git_sync_attempt_validate_update
    BEFORE UPDATE ON ops.git_sync_attempt
    FOR EACH ROW EXECUTE FUNCTION ops.validate_git_sync_attempt_update();

CREATE OR REPLACE FUNCTION ops.publish_git_remote_server_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO ops.server_event(
        workspace_id,event_type,resource_ref,resource_version,payload_summary,
        schema_version,source_event_ref,occurred_at,expires_at
    ) VALUES(
        NEW.workspace_id,'git.remote.updated','git_remote:' || NEW.workspace_id::text,NEW.revision,
        jsonb_build_object('status',CASE WHEN NEW.configured THEN 'configured' ELSE 'not-configured' END),
        1,'git.remote.updated:' || NEW.workspace_id::text || ':v' || NEW.revision::text,
        NEW.updated_at,NEW.updated_at + INTERVAL '24 hours'
    ) ON CONFLICT (workspace_id,source_event_ref) DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS git_remote_publish_server_event ON ops.git_remote_config;
CREATE TRIGGER git_remote_publish_server_event
    AFTER INSERT OR UPDATE ON ops.git_remote_config
    FOR EACH ROW EXECUTE FUNCTION ops.publish_git_remote_server_event();

CREATE OR REPLACE FUNCTION ops.publish_git_sync_run_server_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO ops.server_event(
        workspace_id,event_type,resource_ref,resource_version,payload_summary,
        schema_version,source_event_ref,occurred_at,expires_at
    ) VALUES(
        NEW.workspace_id,'git.sync.run.updated','git_sync_run:' || NEW.id::text,NEW.version,
        jsonb_build_object(
            'status',replace(lower(NEW.status),'_','-'),
            'stage',replace(lower(NEW.index_status),'_','-')
        ),
        1,'git.sync.run.updated:' || NEW.id::text || ':v' || NEW.version::text,
        NEW.updated_at,NEW.updated_at + INTERVAL '24 hours'
    ) ON CONFLICT (workspace_id,source_event_ref) DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS git_sync_run_publish_server_event ON ops.git_sync_run;
CREATE TRIGGER git_sync_run_publish_server_event
    AFTER INSERT OR UPDATE ON ops.git_sync_run
    FOR EACH ROW EXECUTE FUNCTION ops.publish_git_sync_run_server_event();

INSERT INTO core.schema_meta(key,value) VALUES ('git_remote_sync','v1')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();
