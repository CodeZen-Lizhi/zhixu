CREATE SEQUENCE ops.model_settings_revision_seq AS bigint START WITH 1 INCREMENT BY 1 NO CYCLE;

CREATE TABLE ops.model_settings_revisions (
    revision bigint PRIMARY KEY DEFAULT nextval('ops.model_settings_revision_seq'),
    chat_provider text NOT NULL,
    chat_base_url text NOT NULL,
    chat_model text NOT NULL,
    chat_model_version text NOT NULL,
    chat_adapter_version text NOT NULL,
    chat_timeout_microseconds bigint NOT NULL,
    chat_max_request_bytes bigint NOT NULL,
    chat_max_response_bytes bigint NOT NULL,
    chat_secret_key_id text,
    chat_secret_nonce bytea,
    chat_secret_ciphertext bytea,
    embedding_provider text NOT NULL,
    embedding_base_url text NOT NULL,
    embedding_model text NOT NULL,
    embedding_dimensions integer NOT NULL,
    embedding_normalization text NOT NULL,
    embedding_distance_metric text NOT NULL,
    embedding_max_batch_size integer NOT NULL,
    embedding_max_input_bytes integer NOT NULL,
    embedding_max_batch_input_bytes bigint NOT NULL,
    embedding_timeout_microseconds bigint NOT NULL,
    embedding_max_response_bytes bigint NOT NULL,
    embedding_secret_key_id text,
    embedding_secret_nonce bytea,
    embedding_secret_ciphertext bytea,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by text NOT NULL,
    CONSTRAINT ops_model_settings_revision_positive CHECK (revision>0),
    CONSTRAINT ops_model_settings_chat_provider CHECK (chat_provider IN ('disabled','openai-compatible')),
    CONSTRAINT ops_model_settings_embedding_provider CHECK (embedding_provider IN ('disabled','openai-compatible','ollama')),
    CONSTRAINT ops_model_settings_chat_target CHECK ((
        (chat_provider='disabled' AND chat_base_url='' AND chat_model='' AND chat_model_version='')
        OR (chat_provider='openai-compatible' AND btrim(chat_base_url)<>'' AND btrim(chat_model)<>'' AND btrim(chat_model_version)<>'')
    ) IS TRUE),
    CONSTRAINT ops_model_settings_embedding_target CHECK ((
        (embedding_provider='disabled' AND embedding_base_url='' AND embedding_model='' AND embedding_dimensions=0)
        OR (embedding_provider IN ('openai-compatible','ollama') AND btrim(embedding_base_url)<>'' AND btrim(embedding_model)<>'' AND embedding_dimensions BETWEEN 1 AND 16000)
    ) IS TRUE),
    CONSTRAINT ops_model_settings_embedding_contract CHECK (
        embedding_normalization IN ('none','l2')
        AND embedding_distance_metric IN ('cosine','inner_product','euclidean')
    ),
    CONSTRAINT ops_model_settings_limits CHECK (
        chat_timeout_microseconds BETWEEN 1 AND 300000000
        AND chat_max_request_bytes BETWEEN 1 AND 16777216
        AND chat_max_response_bytes BETWEEN 1 AND 16777216
        AND embedding_max_batch_size BETWEEN 1 AND 1000
        AND embedding_max_input_bytes BETWEEN 1 AND 10485760
        AND embedding_max_batch_input_bytes BETWEEN embedding_max_input_bytes AND 67108864
        AND embedding_timeout_microseconds BETWEEN 1 AND 300000000
        AND embedding_max_response_bytes BETWEEN 1 AND 134217728
    ),
    CONSTRAINT ops_model_settings_chat_secret_envelope CHECK ((
        (chat_secret_key_id IS NULL AND chat_secret_nonce IS NULL AND chat_secret_ciphertext IS NULL)
        OR (chat_secret_key_id ~ '^[0-9a-f]{64}$' AND octet_length(chat_secret_nonce)=12
            AND octet_length(chat_secret_ciphertext) BETWEEN 17 AND 20480)
    ) IS TRUE),
    CONSTRAINT ops_model_settings_embedding_secret_envelope CHECK ((
        (embedding_secret_key_id IS NULL AND embedding_secret_nonce IS NULL AND embedding_secret_ciphertext IS NULL)
        OR (embedding_secret_key_id ~ '^[0-9a-f]{64}$' AND octet_length(embedding_secret_nonce)=12
            AND octet_length(embedding_secret_ciphertext) BETWEEN 17 AND 20480)
    ) IS TRUE),
    CONSTRAINT ops_model_settings_disabled_secret CHECK (
        (chat_provider<>'disabled' OR chat_secret_key_id IS NULL)
        AND (embedding_provider<>'disabled' OR embedding_secret_key_id IS NULL)
        AND (embedding_provider<>'ollama' OR embedding_secret_key_id IS NULL)
    ),
    CONSTRAINT ops_model_settings_text_bounds CHECK (
        octet_length(chat_base_url)<=2048 AND octet_length(chat_model)<=128
        AND octet_length(chat_model_version)<=64 AND btrim(chat_adapter_version)<>''
        AND octet_length(chat_adapter_version)<=64
        AND octet_length(embedding_base_url)<=2048 AND octet_length(embedding_model)<=128
        AND btrim(created_by)<>'' AND octet_length(created_by)<=256
        AND chat_base_url=btrim(chat_base_url) AND chat_model=btrim(chat_model)
        AND chat_model_version=btrim(chat_model_version) AND chat_adapter_version=btrim(chat_adapter_version)
        AND embedding_base_url=btrim(embedding_base_url) AND embedding_model=btrim(embedding_model)
        AND created_by=btrim(created_by)
    )
);

ALTER SEQUENCE ops.model_settings_revision_seq OWNED BY ops.model_settings_revisions.revision;

CREATE FUNCTION ops.reject_model_settings_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'model settings revisions are append-only' USING ERRCODE='55000';
END
$$;

CREATE TRIGGER model_settings_revisions_append_only
    BEFORE UPDATE OR DELETE ON ops.model_settings_revisions
    FOR EACH ROW EXECUTE FUNCTION ops.reject_model_settings_revision_mutation();

CREATE TRIGGER model_settings_revisions_reject_truncate
    BEFORE TRUNCATE ON ops.model_settings_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION ops.reject_model_settings_revision_mutation();

CREATE TABLE ops.model_settings_state (
    singleton boolean PRIMARY KEY DEFAULT true,
    desired_revision bigint NOT NULL DEFAULT 0,
    active_revision bigint NOT NULL DEFAULT 0,
    rollout_id uuid,
    target_revision bigint,
    previous_active_revision bigint,
    phase text NOT NULL DEFAULT 'idle',
    lease_expires_at timestamptz,
    last_error_code text,
    version bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT ops_model_settings_state_singleton CHECK (singleton),
    CONSTRAINT ops_model_settings_state_revisions CHECK (
        desired_revision>=0 AND active_revision>=0
        AND (target_revision IS NULL OR target_revision>=0)
        AND (previous_active_revision IS NULL OR previous_active_revision>=0)
    ),
    CONSTRAINT ops_model_settings_state_phase CHECK (phase IN ('idle','validating','draining','applying','verifying','failed')),
    CONSTRAINT ops_model_settings_state_rollout_shape CHECK ((
        (phase='idle' AND rollout_id IS NULL AND target_revision IS NULL
            AND previous_active_revision IS NULL AND lease_expires_at IS NULL AND last_error_code IS NULL)
        OR (phase IN ('validating','draining','applying','verifying') AND rollout_id IS NOT NULL
            AND target_revision IS NOT NULL AND previous_active_revision IS NOT NULL
            AND lease_expires_at IS NOT NULL AND last_error_code IS NULL)
        OR (phase='failed' AND rollout_id IS NOT NULL AND target_revision IS NOT NULL
            AND previous_active_revision IS NOT NULL AND lease_expires_at IS NULL
            AND last_error_code ~ '^[A-Z0-9_]{1,128}$')
    ) IS TRUE),
    CONSTRAINT ops_model_settings_state_version CHECK (version>0)
);

INSERT INTO ops.model_settings_state(singleton) VALUES(true);

CREATE FUNCTION ops.model_settings_revision_exists(candidate bigint)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT candidate=0 OR EXISTS (
        SELECT 1 FROM ops.model_settings_revisions AS revision WHERE revision.revision=candidate
    )
$$;

CREATE FUNCTION ops.enforce_model_settings_state()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'model settings singleton state cannot be deleted' USING ERRCODE='55000';
    END IF;
    IF NOT ops.model_settings_revision_exists(NEW.desired_revision)
       OR NOT ops.model_settings_revision_exists(NEW.active_revision)
       OR (NEW.target_revision IS NOT NULL AND NOT ops.model_settings_revision_exists(NEW.target_revision))
       OR (NEW.previous_active_revision IS NOT NULL AND NOT ops.model_settings_revision_exists(NEW.previous_active_revision)) THEN
        RAISE EXCEPTION 'model settings state references an unknown revision' USING ERRCODE='23503';
    END IF;
    IF TG_OP='INSERT' THEN
        RETURN NEW;
    END IF;
    IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'model settings state version is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.desired_revision<>OLD.desired_revision AND NEW.desired_revision<=OLD.desired_revision THEN
        RAISE EXCEPTION 'model settings desired revision cannot move backwards' USING ERRCODE='55000';
    END IF;
    IF OLD.phase IN ('validating','draining','applying','verifying')
       AND NEW.desired_revision<>OLD.desired_revision THEN
        RAISE EXCEPTION 'model settings desired revision is fixed during rollout' USING ERRCODE='55000';
    END IF;
    IF OLD.phase='failed' AND NEW.phase='failed'
       AND ROW(NEW.rollout_id,NEW.target_revision,NEW.previous_active_revision,NEW.last_error_code)
           IS DISTINCT FROM ROW(OLD.rollout_id,OLD.target_revision,OLD.previous_active_revision,OLD.last_error_code) THEN
        RAISE EXCEPTION 'failed model settings rollout binding is immutable' USING ERRCODE='55000';
    END IF;
    IF OLD.active_revision<>NEW.active_revision
       AND NOT (OLD.phase='verifying' AND NEW.phase='idle' AND NEW.active_revision=OLD.target_revision) THEN
        RAISE EXCEPTION 'model settings active revision transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NOT (
        (OLD.phase=NEW.phase AND OLD.phase IN ('idle','failed'))
        OR (OLD.phase IN ('idle','failed') AND NEW.phase='validating'
            AND NEW.target_revision=NEW.desired_revision AND NEW.previous_active_revision=OLD.active_revision)
        OR (OLD.phase=NEW.phase AND OLD.phase IN ('validating','draining','applying','verifying')
            AND NEW.rollout_id=OLD.rollout_id AND NEW.target_revision=OLD.target_revision
            AND NEW.previous_active_revision=OLD.previous_active_revision)
        OR (OLD.phase='validating' AND NEW.phase='draining')
        OR (OLD.phase='draining' AND NEW.phase='applying')
        OR (OLD.phase='applying' AND NEW.phase='verifying')
        OR (OLD.phase IN ('validating','draining','applying','verifying') AND NEW.phase='failed'
            AND NEW.rollout_id=OLD.rollout_id AND NEW.target_revision=OLD.target_revision
            AND NEW.previous_active_revision=OLD.previous_active_revision)
        OR (OLD.phase='verifying' AND NEW.phase='idle'
            AND NEW.active_revision=OLD.target_revision)
    ) THEN
        RAISE EXCEPTION 'model settings rollout transition is invalid' USING ERRCODE='55000';
    END IF;
    IF OLD.phase IN ('validating','draining','applying','verifying')
       AND NEW.phase IN ('draining','applying','verifying')
       AND ROW(NEW.rollout_id,NEW.target_revision,NEW.previous_active_revision)
           IS DISTINCT FROM ROW(OLD.rollout_id,OLD.target_revision,OLD.previous_active_revision) THEN
        RAISE EXCEPTION 'model settings rollout binding is immutable' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER model_settings_state_guard
    BEFORE INSERT OR UPDATE OR DELETE ON ops.model_settings_state
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_model_settings_state();

CREATE TRIGGER model_settings_state_reject_truncate
    BEFORE TRUNCATE ON ops.model_settings_state
    FOR EACH STATEMENT EXECUTE FUNCTION ops.enforce_model_settings_state();

CREATE TABLE ops.model_settings_runtime (
    role text PRIMARY KEY,
    instance_id uuid NOT NULL,
    applied_revision bigint NOT NULL,
    rollout_id uuid,
    phase text NOT NULL,
    applied_at timestamptz NOT NULL,
    heartbeat_at timestamptz NOT NULL,
    CONSTRAINT ops_model_settings_runtime_role CHECK (role IN ('api','worker')),
    CONSTRAINT ops_model_settings_runtime_revision CHECK (applied_revision>=0),
    CONSTRAINT ops_model_settings_runtime_phase CHECK (phase IN ('active','quiescing','quiesced','prepared','verifying','unavailable')),
    CONSTRAINT ops_model_settings_runtime_shape CHECK ((
        (phase IN ('active','unavailable') AND rollout_id IS NULL)
        OR (phase IN ('quiescing','quiesced','prepared','verifying') AND rollout_id IS NOT NULL)
    ) IS TRUE),
    CONSTRAINT ops_model_settings_runtime_time CHECK (heartbeat_at>=applied_at)
);

CREATE FUNCTION ops.enforce_model_settings_runtime()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'model settings runtime ownership cannot be deleted' USING ERRCODE='55000';
    END IF;
    IF NOT ops.model_settings_revision_exists(NEW.applied_revision) THEN
        RAISE EXCEPTION 'model settings runtime references an unknown revision' USING ERRCODE='23503';
    END IF;
    IF TG_OP='INSERT' OR NEW.instance_id<>OLD.instance_id THEN
        RETURN NEW;
    END IF;
    IF NEW.applied_revision<>OLD.applied_revision OR NEW.applied_at<>OLD.applied_at
       OR NEW.heartbeat_at<OLD.heartbeat_at THEN
        RAISE EXCEPTION 'model settings runtime binding is invalid' USING ERRCODE='55000';
    END IF;
    IF NOT (
        (NEW.phase=OLD.phase AND NEW.rollout_id IS NOT DISTINCT FROM OLD.rollout_id)
        OR (OLD.phase='active' AND NEW.phase='quiescing' AND OLD.rollout_id IS NULL AND NEW.rollout_id IS NOT NULL)
        OR (OLD.phase='quiescing' AND NEW.phase='quiesced' AND NEW.rollout_id=OLD.rollout_id)
        OR (OLD.phase='quiescing' AND NEW.phase='active' AND NEW.rollout_id IS NULL)
        OR (OLD.phase='quiesced' AND NEW.phase='active' AND NEW.rollout_id IS NULL)
        OR (OLD.phase='prepared' AND NEW.phase='verifying' AND NEW.rollout_id=OLD.rollout_id)
        OR (OLD.phase IN ('prepared','verifying') AND NEW.phase='active' AND NEW.rollout_id IS NULL)
        OR (NEW.phase='unavailable' AND NEW.rollout_id IS NULL)
    ) THEN
        RAISE EXCEPTION 'model settings runtime phase transition is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER model_settings_runtime_guard
    BEFORE INSERT OR UPDATE OR DELETE ON ops.model_settings_runtime
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_model_settings_runtime();

CREATE TRIGGER model_settings_runtime_reject_truncate
    BEFORE TRUNCATE ON ops.model_settings_runtime
    FOR EACH STATEMENT EXECUTE FUNCTION ops.enforce_model_settings_runtime();
