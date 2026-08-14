-- +goose Up

-- This migration is the durable boundary for the managed local-model
-- supervisor. It is deliberately forward-only for lifecycle rows: releases
-- and terminal transitions are recorded, never implemented as DELETEs.
LOCK TABLE ops.model_settings_revisions IN ACCESS EXCLUSIVE MODE;

ALTER TABLE ops.model_settings_revisions
    DROP CONSTRAINT ops_model_settings_chat_provider,
    DROP CONSTRAINT ops_model_settings_chat_target,
    DROP CONSTRAINT ops_model_settings_disabled_secret,
    ADD CONSTRAINT ops_model_settings_chat_provider
        CHECK (chat_provider IN ('disabled','openai-compatible','ollama')),
    ADD CONSTRAINT ops_model_settings_chat_target CHECK ((
        (chat_provider='disabled' AND chat_base_url='' AND chat_model='' AND chat_model_version='')
        OR (chat_provider IN ('openai-compatible','ollama') AND btrim(chat_base_url)<>''
            AND btrim(chat_model)<>'' AND btrim(chat_model_version)<>'')
    ) IS TRUE),
    ADD CONSTRAINT ops_model_settings_disabled_secret CHECK (
        (chat_provider NOT IN ('disabled','ollama') OR chat_secret_key_id IS NULL)
        AND (embedding_provider NOT IN ('disabled','ollama') OR embedding_secret_key_id IS NULL)
    );

CREATE TABLE ops.managed_ollama_runtime (
    singleton boolean PRIMARY KEY DEFAULT true,
    mode text NOT NULL DEFAULT 'managed',
    owner_id uuid,
    owner_epoch bigint NOT NULL DEFAULT 0,
    heartbeat_at timestamptz,
    lease_expires_at timestamptz,
    requirement_version bigint NOT NULL DEFAULT 0,
    requirement_hash text NOT NULL DEFAULT '',
    required_models jsonb NOT NULL DEFAULT '[]'::jsonb,
    observed_phase text NOT NULL DEFAULT 'stopped',
    ready_requirement_hash text NOT NULL DEFAULT '',
    child_epoch bigint NOT NULL DEFAULT 0,
    last_error_code text,
    last_error_retryable boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT ops_managed_ollama_runtime_singleton CHECK (singleton),
    CONSTRAINT ops_managed_ollama_runtime_mode CHECK (mode IN ('managed','external-static')),
    CONSTRAINT ops_managed_ollama_runtime_owner_shape CHECK ((
        (owner_id IS NULL AND heartbeat_at IS NULL AND lease_expires_at IS NULL AND owner_epoch=0)
        OR (owner_id IS NOT NULL AND heartbeat_at IS NOT NULL AND lease_expires_at IS NOT NULL
            AND owner_epoch>0 AND lease_expires_at>=heartbeat_at)
    ) IS TRUE),
    CONSTRAINT ops_managed_ollama_runtime_phase CHECK (observed_phase IN ('stopped','starting','pulling','checking','ready','stopping','failed')),
    CONSTRAINT ops_managed_ollama_runtime_phase_shape CHECK ((
        (observed_phase IN ('stopped','ready') AND last_error_code IS NULL AND last_error_retryable=false)
        OR (observed_phase IN ('starting','pulling','checking','stopping') AND last_error_code IS NULL AND last_error_retryable=false)
        OR (observed_phase='failed' AND last_error_code IS NOT NULL)
    ) IS TRUE),
    CONSTRAINT ops_managed_ollama_runtime_hash CHECK (requirement_hash='' OR requirement_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT ops_managed_ollama_runtime_ready_hash CHECK (ready_requirement_hash='' OR ready_requirement_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT ops_managed_ollama_runtime_version CHECK (version>0 AND requirement_version>=0 AND child_epoch>=0),
    CONSTRAINT ops_managed_ollama_runtime_models CHECK (
        jsonb_typeof(required_models)='array'
        AND NOT jsonb_path_exists(required_models, '$[*] ? (@.type() != "string")')
        AND NOT jsonb_path_exists(required_models, '$[*] ? (@ == "" || @.size() > 128)')
    ),
    CONSTRAINT ops_managed_ollama_runtime_hash_empty_shape CHECK (
        (jsonb_array_length(required_models)=0 AND requirement_hash='')
        OR (jsonb_array_length(required_models)>0 AND requirement_hash<>'')
    )
);

CREATE TABLE ops.managed_ollama_holds (
    hold_id uuid PRIMARY KEY,
    owner_kind text NOT NULL,
    owner_id uuid NOT NULL,
    owner_epoch bigint NOT NULL,
    role text,
    instance_id uuid,
    revision bigint,
    rollout_id uuid,
    operation_id uuid,
    requirement_hash text NOT NULL,
    models jsonb NOT NULL,
    lease_expires_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    released_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT ops_managed_ollama_hold_kind CHECK (owner_kind IN ('generation','test','preparation')),
    CONSTRAINT ops_managed_ollama_hold_owner CHECK (owner_epoch>0),
    CONSTRAINT ops_managed_ollama_hold_role CHECK ((role IS NULL AND instance_id IS NULL) OR (role IN ('api','worker') AND instance_id IS NOT NULL)),
    CONSTRAINT ops_managed_ollama_hold_revision CHECK (revision IS NULL OR revision>=0),
    CONSTRAINT ops_managed_ollama_hold_hash CHECK (requirement_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT ops_managed_ollama_hold_models CHECK (
        jsonb_typeof(models)='array'
        AND jsonb_array_length(models)>0
        AND NOT jsonb_path_exists(models, '$[*] ? (@.type() != "string")')
        AND NOT jsonb_path_exists(models, '$[*] ? (@ == "" || @.size() > 128)')
    ),
    CONSTRAINT ops_managed_ollama_hold_release CHECK (released_at IS NULL OR released_at>=created_at),
    CONSTRAINT ops_managed_ollama_hold_version CHECK (version>0)
);

CREATE INDEX ops_managed_ollama_holds_active_idx
    ON ops.managed_ollama_holds (lease_expires_at, requirement_hash)
    WHERE released_at IS NULL;

CREATE TABLE ops.managed_ollama_operations (
    operation_id uuid PRIMARY KEY,
    kind text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    target_revision bigint,
    rollout_id uuid,
    requirement_hash text NOT NULL,
    required_models jsonb NOT NULL DEFAULT '[]'::jsonb,
    phase text NOT NULL DEFAULT 'queued',
    claim_owner_id uuid,
    claim_owner_epoch bigint,
    claim_expires_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    completed_models jsonb NOT NULL DEFAULT '[]'::jsonb,
    resolved_models jsonb NOT NULL DEFAULT '[]'::jsonb,
    completed_bytes bigint NOT NULL DEFAULT 0,
    total_bytes bigint,
    progress_known boolean NOT NULL DEFAULT false,
    attempt_no integer NOT NULL DEFAULT 0,
    last_progress_at timestamptz,
    error_code text,
    error_retryable boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    terminal_at timestamptz,
    CONSTRAINT ops_managed_ollama_operation_kind CHECK (kind IN ('activation','active_recovery','test')),
    CONSTRAINT ops_managed_ollama_operation_idempotency CHECK (idempotency_key=btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 256 AND idempotency_key !~ '[[:cntrl:]]'),
    CONSTRAINT ops_managed_ollama_operation_hash CHECK (request_hash ~ '^[0-9a-f]{64}$' AND requirement_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT ops_managed_ollama_operation_required_models CHECK (jsonb_typeof(required_models)='array' AND jsonb_array_length(required_models)>0 AND NOT jsonb_path_exists(required_models, '$[*] ? (@.type() != "string")') AND NOT jsonb_path_exists(required_models, '$[*] ? (@ == "" || @.size() > 128)')),
    CONSTRAINT ops_managed_ollama_operation_phase CHECK (phase IN ('queued','starting','checking','pulling','verifying','ready','probing','succeeded','failed','superseded')),
    CONSTRAINT ops_managed_ollama_operation_claim_shape CHECK ((claim_owner_id IS NULL AND claim_owner_epoch IS NULL AND claim_expires_at IS NULL) OR (claim_owner_id IS NOT NULL AND claim_owner_epoch>0 AND claim_expires_at IS NOT NULL)),
    CONSTRAINT ops_managed_ollama_operation_probe_shape CHECK (phase<>'probing' OR (kind='test' AND claim_owner_id IS NOT NULL AND claim_owner_epoch>0 AND claim_expires_at IS NOT NULL)),
    CONSTRAINT ops_managed_ollama_operation_unclaimed_shape CHECK (phase NOT IN ('ready','succeeded','failed','superseded') OR claim_owner_id IS NULL),
    CONSTRAINT ops_managed_ollama_operation_progress CHECK (completed_bytes>=0 AND (total_bytes IS NULL OR total_bytes>=completed_bytes)),
    CONSTRAINT ops_managed_ollama_operation_models CHECK (jsonb_typeof(completed_models)='array' AND NOT jsonb_path_exists(completed_models, '$[*] ? (@.type() != "string")') AND NOT jsonb_path_exists(completed_models, '$[*] ? (@ == "" || @.size() > 128)')),
    CONSTRAINT ops_managed_ollama_operation_resolved_models CHECK (jsonb_typeof(resolved_models)='array' AND NOT jsonb_path_exists(resolved_models, '$[*] ? (@.model.type() != "string" || @.digest.type() != "string" || @.bytes.type() != "number")')),
    CONSTRAINT ops_managed_ollama_operation_error CHECK ((error_code IS NULL AND error_retryable=false) OR (error_code IS NOT NULL AND error_code ~ '^[A-Z0-9_]{1,128}$')),
    CONSTRAINT ops_managed_ollama_operation_terminal CHECK ((phase IN ('succeeded','failed','superseded')) = (terminal_at IS NOT NULL)),
    CONSTRAINT ops_managed_ollama_operation_version CHECK (version>0 AND attempt_no>=0)
);

CREATE UNIQUE INDEX ops_managed_ollama_operations_idempotency_idx ON ops.managed_ollama_operations(kind,idempotency_key);
CREATE UNIQUE INDEX ops_managed_ollama_holds_operation_idx
    ON ops.managed_ollama_holds(operation_id) WHERE operation_id IS NOT NULL;

-- The trigger is intentionally strict. Reconciler code can only move through
-- the documented graph and can never resurrect terminal rows or change an
-- operation's immutable identity.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enforce_managed_ollama_operation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'managed Ollama operations cannot be deleted' USING ERRCODE='55000';
    END IF;
    IF TG_OP='UPDATE' THEN
        IF NEW.operation_id<>OLD.operation_id OR NEW.kind<>OLD.kind OR NEW.idempotency_key<>OLD.idempotency_key
           OR NEW.request_hash<>OLD.request_hash OR NEW.requirement_hash<>OLD.requirement_hash
           OR NEW.version<>OLD.version+1 OR NEW.created_at<>OLD.created_at OR NEW.updated_at<OLD.updated_at THEN
            RAISE EXCEPTION 'managed Ollama operation identity/version is immutable' USING ERRCODE='55000';
        END IF;
        IF OLD.terminal_at IS NOT NULL AND ROW(NEW.phase,NEW.terminal_at,NEW.error_code,NEW.error_retryable)
             IS DISTINCT FROM ROW(OLD.phase,OLD.terminal_at,OLD.error_code,OLD.error_retryable) THEN
            RAISE EXCEPTION 'managed Ollama terminal operation is immutable' USING ERRCODE='55000';
        END IF;
        IF NOT (NEW.phase=OLD.phase OR
            (OLD.phase='queued' AND NEW.phase IN ('starting','failed')) OR
            (OLD.phase='starting' AND NEW.phase IN ('checking','pulling','failed')) OR
            (OLD.phase='checking' AND NEW.phase IN ('pulling','verifying','failed')) OR
            (OLD.phase='pulling' AND NEW.phase IN ('pulling','verifying','failed')) OR
            (OLD.phase='verifying' AND NEW.phase IN ('ready','failed')) OR
            (OLD.kind='active_recovery' AND OLD.phase='verifying' AND NEW.phase='succeeded') OR
            (OLD.phase='ready' AND NEW.phase IN ('probing','succeeded','failed')) OR
            (OLD.phase='probing' AND NEW.phase IN ('ready','succeeded','failed')) OR
            (OLD.phase NOT IN ('succeeded','failed','superseded') AND NEW.phase='superseded')) THEN
            RAISE EXCEPTION 'managed Ollama operation phase transition is invalid' USING ERRCODE='55000';
        END IF;
        IF OLD.kind='test' AND OLD.phase='ready' AND NEW.phase='succeeded' THEN
            RAISE EXCEPTION 'managed Ollama test production probe must be claimed' USING ERRCODE='55000';
        END IF;
        IF OLD.kind='test' AND OLD.phase='ready' AND NEW.phase='failed'
           AND NEW.error_code<>'LOCAL_MODEL_RUNTIME_PREPARATION_TIMEOUT' THEN
            RAISE EXCEPTION 'managed Ollama test production probe must be claimed' USING ERRCODE='55000';
        END IF;
        IF OLD.phase='ready' AND NEW.phase='probing'
           AND (NEW.claim_owner_id IS NULL OR NEW.claim_owner_epoch IS NULL OR NEW.claim_expires_at IS NULL) THEN
            RAISE EXCEPTION 'managed Ollama production probe must be claimed' USING ERRCODE='55000';
        END IF;
        IF OLD.phase='ready' AND NEW.phase='probing'
           AND NOT EXISTS (
               SELECT 1 FROM ops.managed_ollama_holds
               WHERE owner_kind='test' AND operation_id=OLD.operation_id
                 AND released_at IS NULL AND owner_id<>NEW.claim_owner_id
           ) THEN
            RAISE EXCEPTION 'managed Ollama production probe owner conflicts with its hold' USING ERRCODE='55000';
        END IF;
        IF OLD.phase='probing' AND NEW.phase='probing'
           AND ROW(NEW.claim_owner_id,NEW.claim_owner_epoch) IS DISTINCT FROM ROW(OLD.claim_owner_id,OLD.claim_owner_epoch)
           AND OLD.claim_expires_at>clock_timestamp() THEN
            RAISE EXCEPTION 'managed Ollama production probe owner is fresh' USING ERRCODE='55000';
        END IF;
        IF OLD.phase='probing' AND NEW.phase IN ('ready','succeeded','failed')
           AND (OLD.claim_owner_id IS NULL OR OLD.claim_owner_epoch IS NULL OR OLD.claim_expires_at IS NULL) THEN
            RAISE EXCEPTION 'managed Ollama production probe completion is unowned' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER managed_ollama_operation_guard BEFORE INSERT OR UPDATE OR DELETE ON ops.managed_ollama_operations
FOR EACH ROW EXECUTE FUNCTION ops.enforce_managed_ollama_operation();
CREATE TRIGGER managed_ollama_operation_reject_truncate BEFORE TRUNCATE ON ops.managed_ollama_operations
FOR EACH STATEMENT EXECUTE FUNCTION ops.enforce_managed_ollama_operation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.reject_managed_ollama_delete()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'managed Ollama lifecycle facts cannot be deleted' USING ERRCODE='55000';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER managed_ollama_runtime_reject_delete BEFORE DELETE ON ops.managed_ollama_runtime FOR EACH ROW EXECUTE FUNCTION ops.reject_managed_ollama_delete();
CREATE TRIGGER managed_ollama_runtime_reject_truncate BEFORE TRUNCATE ON ops.managed_ollama_runtime FOR EACH STATEMENT EXECUTE FUNCTION ops.reject_managed_ollama_delete();
CREATE TRIGGER managed_ollama_holds_reject_delete BEFORE DELETE ON ops.managed_ollama_holds FOR EACH ROW EXECUTE FUNCTION ops.reject_managed_ollama_delete();
CREATE TRIGGER managed_ollama_holds_reject_truncate BEFORE TRUNCATE ON ops.managed_ollama_holds FOR EACH STATEMENT EXECUTE FUNCTION ops.reject_managed_ollama_delete();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enforce_managed_ollama_runtime()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='UPDATE' THEN
        IF NEW.singleton<>OLD.singleton OR NEW.mode<>OLD.mode
           OR NEW.owner_epoch<OLD.owner_epoch OR NEW.requirement_version<OLD.requirement_version
           OR NEW.child_epoch<OLD.child_epoch OR NEW.version<>OLD.version+1
           OR NEW.updated_at<OLD.updated_at THEN
            RAISE EXCEPTION 'managed Ollama runtime fence/version is invalid' USING ERRCODE='55000';
        END IF;
        IF NEW.owner_id IS DISTINCT FROM OLD.owner_id AND NEW.owner_epoch<>OLD.owner_epoch+1 THEN
            RAISE EXCEPTION 'managed Ollama runtime takeover must advance owner epoch' USING ERRCODE='55000';
        END IF;
        IF NEW.owner_id IS NOT DISTINCT FROM OLD.owner_id AND NEW.owner_epoch<>OLD.owner_epoch
           AND NOT (OLD.owner_id IS NULL AND NEW.owner_id IS NOT NULL AND NEW.owner_epoch=1) THEN
            RAISE EXCEPTION 'managed Ollama runtime owner epoch changed unexpectedly' USING ERRCODE='55000';
        END IF;
        IF NOT (NEW.observed_phase=OLD.observed_phase OR
            (OLD.observed_phase='stopped' AND NEW.observed_phase IN ('starting','failed')) OR
            (OLD.observed_phase='starting' AND NEW.observed_phase IN ('pulling','checking','ready','failed','stopping')) OR
            (OLD.observed_phase='pulling' AND NEW.observed_phase IN ('pulling','checking','ready','failed','stopping')) OR
            (OLD.observed_phase='checking' AND NEW.observed_phase IN ('pulling','ready','failed','stopping')) OR
            (OLD.observed_phase='ready' AND NEW.observed_phase IN ('starting','pulling','checking','stopping','failed')) OR
            (OLD.observed_phase='stopping' AND NEW.observed_phase IN ('stopped','starting','failed')) OR
            (OLD.observed_phase='failed' AND NEW.observed_phase IN ('stopped','starting','pulling','checking','ready','stopping'))) THEN
            RAISE EXCEPTION 'managed Ollama runtime phase transition is invalid' USING ERRCODE='55000';
        END IF;
        IF NEW.observed_phase='ready' AND (NEW.ready_requirement_hash='' OR NEW.ready_requirement_hash<>NEW.requirement_hash) THEN
            RAISE EXCEPTION 'managed Ollama ready phase must bind the current requirement' USING ERRCODE='55000';
        END IF;
        IF NEW.observed_phase IN ('stopped','starting','stopping') AND NEW.ready_requirement_hash<>'' THEN
            RAISE EXCEPTION 'managed Ollama non-ready phase retained ready binding' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER managed_ollama_runtime_guard BEFORE INSERT OR UPDATE ON ops.managed_ollama_runtime
FOR EACH ROW EXECUTE FUNCTION ops.enforce_managed_ollama_runtime();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enforce_managed_ollama_hold()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='UPDATE' THEN
        IF ROW(NEW.hold_id,NEW.owner_kind,NEW.owner_id,NEW.owner_epoch,NEW.role,NEW.instance_id,
               NEW.revision,NEW.rollout_id,NEW.operation_id,NEW.requirement_hash,NEW.models,NEW.created_at)
           IS DISTINCT FROM
           ROW(OLD.hold_id,OLD.owner_kind,OLD.owner_id,OLD.owner_epoch,OLD.role,OLD.instance_id,
               OLD.revision,OLD.rollout_id,OLD.operation_id,OLD.requirement_hash,OLD.models,OLD.created_at)
           OR NEW.updated_at<OLD.updated_at THEN
            RAISE EXCEPTION 'managed Ollama hold identity is immutable' USING ERRCODE='55000';
        END IF;
        IF OLD.released_at IS NULL AND NEW.released_at IS NULL THEN
            IF NEW.version<>OLD.version+1 OR NEW.lease_expires_at<OLD.lease_expires_at THEN
                RAISE EXCEPTION 'managed Ollama hold renewal is invalid' USING ERRCODE='55000';
            END IF;
        ELSIF OLD.released_at IS NULL AND NEW.released_at IS NOT NULL THEN
            IF NEW.version<>OLD.version+1 THEN
                RAISE EXCEPTION 'managed Ollama hold release version is invalid' USING ERRCODE='55000';
            END IF;
        ELSIF ROW(NEW.version,NEW.released_at) IS DISTINCT FROM ROW(OLD.version,OLD.released_at) THEN
            RAISE EXCEPTION 'managed Ollama released hold is immutable' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER managed_ollama_hold_guard BEFORE INSERT OR UPDATE ON ops.managed_ollama_holds
FOR EACH ROW EXECUTE FUNCTION ops.enforce_managed_ollama_hold();

INSERT INTO ops.managed_ollama_runtime(singleton) VALUES (true) ON CONFLICT (singleton) DO NOTHING;

-- The local-model manager has a distinct database login.  It can read its
-- durable snapshot, but it writes lifecycle facts only through these fixed
-- command functions.  The functions deliberately use PostgreSQL time and
-- retain every owner/epoch/version/phase fence from the adapter's former
-- direct statements.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.managed_ollama_claim_manager(
    p_owner_id uuid, p_lease_duration interval, p_stale_after interval
) RETURNS SETOF ops.managed_ollama_runtime
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ops
AS $$
BEGIN
    RETURN QUERY
    UPDATE ops.managed_ollama_runtime
    SET owner_id=p_owner_id,
        owner_epoch=CASE WHEN owner_id=p_owner_id THEN owner_epoch ELSE owner_epoch+1 END,
        heartbeat_at=clock_timestamp(), lease_expires_at=clock_timestamp()+p_lease_duration,
        version=version+1, updated_at=clock_timestamp()
    WHERE singleton=true
      AND p_lease_duration>interval '0' AND p_lease_duration<=interval '10 minutes'
      AND p_stale_after>=p_lease_duration AND p_stale_after<=interval '10 minutes'
      AND (owner_id IS NULL OR owner_id=p_owner_id
      OR lease_expires_at<=clock_timestamp()
      OR heartbeat_at<=clock_timestamp()-p_stale_after)
    RETURNING *;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.managed_ollama_heartbeat_manager(
    p_owner_id uuid, p_owner_epoch bigint, p_expected_version bigint, p_lease_duration interval
) RETURNS SETOF ops.managed_ollama_runtime
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ops
AS $$
BEGIN
    RETURN QUERY
    UPDATE ops.managed_ollama_runtime
    SET heartbeat_at=clock_timestamp(), lease_expires_at=clock_timestamp()+p_lease_duration,
        version=version+1, updated_at=clock_timestamp()
    WHERE singleton=true AND owner_id=p_owner_id AND owner_epoch=p_owner_epoch AND version=p_expected_version
      AND p_lease_duration>interval '0' AND p_lease_duration<=interval '10 minutes'
      AND lease_expires_at>clock_timestamp()
    RETURNING *;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.managed_ollama_publish_demand(
    p_owner_id uuid, p_owner_epoch bigint, p_expected_version bigint,
    p_expected_requirement_version bigint, p_requirement_hash text,
    p_required_models jsonb, p_expected_settings_state_version bigint
) RETURNS SETOF ops.managed_ollama_runtime
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ops
AS $$
BEGIN
    RETURN QUERY
    UPDATE ops.managed_ollama_runtime
    SET requirement_version=requirement_version+1,
        requirement_hash=p_requirement_hash,
        required_models=p_required_models,
        observed_phase=CASE
            WHEN requirement_hash IS DISTINCT FROM p_requirement_hash
                 AND jsonb_array_length(p_required_models)=0
                 AND observed_phase IN ('ready','starting','pulling','checking','failed') THEN 'stopping'
            WHEN requirement_hash IS DISTINCT FROM p_requirement_hash
                 AND jsonb_array_length(p_required_models)>0
                 AND observed_phase='ready' THEN 'starting'
            ELSE observed_phase
        END,
        ready_requirement_hash=CASE WHEN requirement_hash IS DISTINCT FROM p_requirement_hash THEN '' ELSE ready_requirement_hash END,
        version=version+1, updated_at=clock_timestamp()
    WHERE singleton=true AND owner_id=p_owner_id AND owner_epoch=p_owner_epoch
      AND version=p_expected_version AND requirement_version=p_expected_requirement_version
      AND lease_expires_at>clock_timestamp()
      AND (SELECT version FROM ops.model_settings_state WHERE singleton=true)=p_expected_settings_state_version
    RETURNING *;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.managed_ollama_seed_active_recovery(
    p_owner_id uuid, p_owner_epoch bigint, p_expected_settings_state_version bigint,
    p_lease_duration interval
) RETURNS SETOF ops.managed_ollama_operations
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ops
AS $$
DECLARE
    v_active_revision bigint;
    v_required_models jsonb;
    v_model_lines text;
    v_requirement_hash text;
    v_request_hash text;
    v_idempotency_key text;
    v_generation bigint;
    v_result ops.managed_ollama_operations;
BEGIN
    IF p_lease_duration<=interval '0' OR p_lease_duration>interval '1 hour' THEN
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM ops.managed_ollama_runtime runtime
        WHERE runtime.singleton=true AND runtime.owner_id=p_owner_id
          AND runtime.owner_epoch=p_owner_epoch AND runtime.lease_expires_at>clock_timestamp()
    ) THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM ops.model_settings_state state
        JOIN ops.model_settings_revisions revision ON revision.revision=state.active_revision
        CROSS JOIN LATERAL (
            VALUES
                (CASE WHEN revision.chat_provider='ollama'
                            OR (revision.chat_provider='openai-compatible'
                                AND revision.chat_base_url='http://127.0.0.1:11434')
                      THEN revision.chat_model END),
                (CASE WHEN revision.embedding_provider='ollama' THEN revision.embedding_model END)
        ) local_model(model)
        WHERE state.singleton=true AND state.version=p_expected_settings_state_version
          AND local_model.model IS NOT NULL
          AND (local_model.model<>btrim(local_model.model)
               OR octet_length(local_model.model) NOT BETWEEN 1 AND 128
               OR local_model.model ~ '[[:cntrl:]]'
               OR local_model.model ~ U&'[\0080-\009F]')
    ) THEN
        RAISE EXCEPTION 'active managed Ollama recovery model is invalid' USING ERRCODE='55000';
    END IF;

    SELECT state.active_revision,
           jsonb_agg(local_model.model ORDER BY local_model.model),
           string_agg(local_model.model,E'\n' ORDER BY local_model.model)
    INTO v_active_revision,v_required_models,v_model_lines
    FROM ops.model_settings_state state
    JOIN ops.model_settings_revisions revision ON revision.revision=state.active_revision
    CROSS JOIN LATERAL (
        SELECT DISTINCT candidate.model
        FROM (VALUES
            (CASE WHEN revision.chat_provider='ollama'
                        OR (revision.chat_provider='openai-compatible'
                            AND revision.chat_base_url='http://127.0.0.1:11434')
                  THEN revision.chat_model END),
            (CASE WHEN revision.embedding_provider='ollama' THEN revision.embedding_model END)
        ) candidate(model)
        WHERE candidate.model IS NOT NULL
    ) local_model
    WHERE state.singleton=true AND state.version=p_expected_settings_state_version
    GROUP BY state.active_revision;
    IF v_active_revision IS NULL OR jsonb_array_length(v_required_models)=0 THEN
        RETURN;
    END IF;

    v_requirement_hash := encode(sha256(convert_to(
        'managed-ollama-requirement/v1'||E'\n'||v_model_lines,'UTF8')),'hex');
    v_request_hash := encode(sha256(convert_to(
        'managed-ollama-active-recovery/v1'||E'\n'||v_active_revision::text||E'\n'||v_requirement_hash,
        'UTF8')),'hex');
    SELECT count(*)
    INTO v_generation
    FROM ops.managed_ollama_operations operation
    WHERE operation.kind='active_recovery'
      AND operation.target_revision=v_active_revision
      AND operation.requirement_hash=v_requirement_hash
      AND operation.idempotency_key LIKE 'active-recovery:'||v_active_revision::text||':'||v_requirement_hash||':%';

    SELECT * INTO v_result
    FROM ops.managed_ollama_operations operation
    WHERE operation.kind='active_recovery'
      AND operation.target_revision=v_active_revision
      AND operation.requirement_hash=v_requirement_hash
    ORDER BY operation.created_at DESC,operation.operation_id DESC
    LIMIT 1;
    IF FOUND AND v_result.terminal_at IS NULL THEN
        v_idempotency_key := v_result.idempotency_key;
    ELSIF FOUND AND v_result.phase='failed' THEN
        RETURN NEXT v_result;
        RETURN;
    ELSE
        v_generation := v_generation+1;
        v_idempotency_key := 'active-recovery:'||v_active_revision::text||':'||v_requirement_hash||':'||v_generation::text;
        v_result := NULL;
    END IF;

    IF v_result.operation_id IS NULL THEN
        INSERT INTO ops.managed_ollama_operations(
            operation_id,kind,idempotency_key,request_hash,target_revision,
            requirement_hash,required_models
        ) VALUES(
            gen_random_uuid(),'active_recovery',v_idempotency_key,v_request_hash,v_active_revision,
            v_requirement_hash,v_required_models
        ) ON CONFLICT (kind,idempotency_key) DO NOTHING;
    END IF;

    SELECT * INTO v_result
    FROM ops.managed_ollama_operations operation
    WHERE operation.kind='active_recovery' AND operation.idempotency_key=v_idempotency_key;
    IF v_result.request_hash IS DISTINCT FROM v_request_hash
       OR v_result.target_revision IS DISTINCT FROM v_active_revision
       OR v_result.requirement_hash IS DISTINCT FROM v_requirement_hash
       OR v_result.required_models IS DISTINCT FROM v_required_models THEN
        RAISE EXCEPTION 'active managed Ollama recovery replay conflicts with current settings' USING ERRCODE='55000';
    END IF;

    IF v_result.terminal_at IS NULL THEN
        INSERT INTO ops.managed_ollama_holds(
            hold_id,owner_kind,owner_id,owner_epoch,revision,operation_id,
            requirement_hash,models,lease_expires_at
        ) VALUES(
            gen_random_uuid(),'preparation',v_result.operation_id,1,v_active_revision,v_result.operation_id,
            v_requirement_hash,v_required_models,clock_timestamp()+p_lease_duration
        ) ON CONFLICT (operation_id) WHERE operation_id IS NOT NULL DO NOTHING;
        IF NOT EXISTS (
            SELECT 1 FROM ops.managed_ollama_holds hold
            WHERE hold.operation_id=v_result.operation_id AND hold.owner_kind='preparation'
              AND hold.owner_id=v_result.operation_id AND hold.owner_epoch=1
              AND hold.revision=v_active_revision AND hold.requirement_hash=v_requirement_hash
              AND hold.models=v_required_models AND hold.released_at IS NULL
        ) THEN
            RAISE EXCEPTION 'active managed Ollama recovery hold conflicts with current settings' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEXT v_result;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.managed_ollama_claim_operation(
    p_operation_id uuid, p_kind text, p_idempotency_key text, p_request_hash text,
    p_target_revision bigint, p_rollout_id uuid, p_requirement_hash text, p_required_models jsonb,
    p_owner_id uuid, p_owner_epoch bigint, p_lease_duration interval
) RETURNS SETOF ops.managed_ollama_operations
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ops
AS $$
BEGIN
    RETURN QUERY
    UPDATE ops.managed_ollama_operations
    SET claim_owner_id=p_owner_id, claim_owner_epoch=p_owner_epoch,
        claim_expires_at=clock_timestamp()+p_lease_duration, version=version+1,
        updated_at=clock_timestamp()
    WHERE operation_id=p_operation_id AND kind=p_kind AND idempotency_key=p_idempotency_key
      AND request_hash=p_request_hash AND requirement_hash=p_requirement_hash
      AND required_models=p_required_models
      AND target_revision IS NOT DISTINCT FROM p_target_revision
      AND rollout_id IS NOT DISTINCT FROM p_rollout_id
      AND p_lease_duration>interval '0' AND p_lease_duration<=interval '1 hour'
      AND created_at+interval '6 hours'>clock_timestamp()
      AND phase IN ('queued','starting','checking','pulling','verifying')
      AND (kind<>'active_recovery' OR EXISTS (
          SELECT 1 FROM ops.model_settings_state state
          WHERE state.singleton=true AND state.active_revision=ops.managed_ollama_operations.target_revision
      ))
      AND EXISTS (
          SELECT 1 FROM ops.managed_ollama_runtime runtime
          WHERE runtime.singleton=true AND runtime.owner_id=p_owner_id
            AND runtime.owner_epoch=p_owner_epoch AND runtime.lease_expires_at>clock_timestamp()
      )
      AND (claim_owner_id IS NULL
           OR (claim_owner_id=p_owner_id AND claim_owner_epoch=p_owner_epoch)
           OR claim_expires_at<=clock_timestamp())
    RETURNING *;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.managed_ollama_begin_pull_attempt(
    p_operation_id uuid, p_owner_id uuid, p_owner_epoch bigint,
    p_expected_version bigint, p_lease_duration interval
) RETURNS SETOF ops.managed_ollama_operations
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ops
AS $$
DECLARE
    result ops.managed_ollama_operations;
BEGIN
    UPDATE ops.managed_ollama_operations
    SET attempt_no=CASE WHEN attempt_no>=3 THEN attempt_no ELSE attempt_no+1 END,
        phase=CASE WHEN attempt_no>=3 THEN 'failed' ELSE phase END,
        error_code=CASE WHEN attempt_no>=3 THEN 'LOCAL_MODEL_RUNTIME_PULL_ATTEMPTS_EXHAUSTED' ELSE NULL END,
        error_retryable=false,
        terminal_at=CASE WHEN attempt_no>=3 THEN clock_timestamp() ELSE NULL END,
        claim_owner_id=CASE WHEN attempt_no>=3 THEN NULL ELSE claim_owner_id END,
        claim_owner_epoch=CASE WHEN attempt_no>=3 THEN NULL ELSE claim_owner_epoch END,
        claim_expires_at=CASE WHEN attempt_no>=3 THEN NULL ELSE clock_timestamp()+p_lease_duration END,
        version=version+1, updated_at=clock_timestamp()
    WHERE operation_id=p_operation_id AND claim_owner_id=p_owner_id AND claim_owner_epoch=p_owner_epoch
      AND version=p_expected_version AND terminal_at IS NULL
      AND phase IN ('starting','checking','pulling')
      AND (kind<>'active_recovery' OR EXISTS (
          SELECT 1 FROM ops.model_settings_state state
          WHERE state.singleton=true AND state.active_revision=ops.managed_ollama_operations.target_revision
      ))
      AND created_at+interval '6 hours'>clock_timestamp()
      AND claim_expires_at>clock_timestamp()
      AND p_lease_duration>interval '0' AND p_lease_duration<=interval '1 hour'
      AND EXISTS (
          SELECT 1 FROM ops.managed_ollama_runtime runtime
          WHERE runtime.singleton=true AND runtime.owner_id=p_owner_id
            AND runtime.owner_epoch=p_owner_epoch AND runtime.lease_expires_at>clock_timestamp()
      )
    RETURNING * INTO result;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    IF result.phase='failed' THEN
        UPDATE ops.managed_ollama_holds
        SET released_at=clock_timestamp(), version=version+1, updated_at=clock_timestamp()
        WHERE operation_id=result.operation_id AND released_at IS NULL;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'managed Ollama exhausted operation hold is missing' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEXT result;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.managed_ollama_record_operation_progress(
    p_operation_id uuid, p_owner_id uuid, p_owner_epoch bigint, p_expected_version bigint,
    p_next_phase text, p_completed_models jsonb, p_resolved_models jsonb, p_completed_bytes bigint,
    p_total_bytes bigint, p_progress_known boolean, p_lease_duration interval, p_expected_phase text
) RETURNS SETOF ops.managed_ollama_operations
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ops
AS $$
BEGIN
    RETURN QUERY
    UPDATE ops.managed_ollama_operations
    SET phase=p_next_phase, completed_models=p_completed_models, resolved_models=p_resolved_models,
        completed_bytes=p_completed_bytes, total_bytes=p_total_bytes, progress_known=p_progress_known,
        last_progress_at=clock_timestamp(), claim_expires_at=clock_timestamp()+p_lease_duration,
        version=version+1, updated_at=clock_timestamp()
    WHERE operation_id=p_operation_id AND claim_owner_id=p_owner_id AND claim_owner_epoch=p_owner_epoch
      AND version=p_expected_version AND phase=p_expected_phase AND terminal_at IS NULL
      AND claim_expires_at>clock_timestamp()
      AND created_at+interval '6 hours'>clock_timestamp()
      AND (kind<>'active_recovery' OR EXISTS (
          SELECT 1 FROM ops.model_settings_state state
          WHERE state.singleton=true AND state.active_revision=ops.managed_ollama_operations.target_revision
      ))
      AND p_lease_duration>interval '0' AND p_lease_duration<=interval '1 hour'
      AND EXISTS (
          SELECT 1 FROM ops.managed_ollama_runtime runtime
          WHERE runtime.singleton=true AND runtime.owner_id=p_owner_id
            AND runtime.owner_epoch=p_owner_epoch AND runtime.lease_expires_at>clock_timestamp()
      )
    RETURNING *;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.managed_ollama_complete_operation(
    p_operation_id uuid, p_owner_id uuid, p_owner_epoch bigint, p_expected_version bigint,
    p_phase text, p_error_code text, p_error_retryable boolean, p_completed_models jsonb,
    p_completed_bytes bigint, p_total_bytes bigint
) RETURNS SETOF ops.managed_ollama_operations
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ops
AS $$
DECLARE
    result ops.managed_ollama_operations;
BEGIN
    UPDATE ops.managed_ollama_operations
    SET phase=CASE WHEN created_at+interval '6 hours'<=clock_timestamp()
                   THEN 'failed'
                   WHEN kind='active_recovery' AND p_phase='ready' THEN 'succeeded'
                   ELSE p_phase END,
        error_code=CASE WHEN created_at+interval '6 hours'<=clock_timestamp()
                        THEN 'LOCAL_MODEL_RUNTIME_PREPARATION_TIMEOUT' ELSE p_error_code END,
        error_retryable=CASE WHEN created_at+interval '6 hours'<=clock_timestamp()
                             THEN false ELSE p_error_retryable END,
        completed_models=p_completed_models, completed_bytes=p_completed_bytes, total_bytes=p_total_bytes,
        terminal_at=CASE WHEN p_phase='ready' AND kind<>'active_recovery'
                              AND created_at+interval '6 hours'>clock_timestamp()
                         THEN NULL ELSE clock_timestamp() END,
        claim_owner_id=NULL, claim_owner_epoch=NULL, claim_expires_at=NULL,
        version=version+1, updated_at=clock_timestamp()
    WHERE operation_id=p_operation_id AND claim_owner_id=p_owner_id AND claim_owner_epoch=p_owner_epoch
      AND version=p_expected_version AND terminal_at IS NULL
      AND claim_expires_at>clock_timestamp()
      AND p_phase IN ('ready','failed')
      AND (kind<>'active_recovery' OR EXISTS (
          SELECT 1 FROM ops.model_settings_state state
          WHERE state.singleton=true AND state.active_revision=ops.managed_ollama_operations.target_revision
      ))
      AND EXISTS (
          SELECT 1 FROM ops.managed_ollama_runtime runtime
          WHERE runtime.singleton=true AND runtime.owner_id=p_owner_id
            AND runtime.owner_epoch=p_owner_epoch AND runtime.lease_expires_at>clock_timestamp()
      )
    RETURNING * INTO result;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    IF result.phase IN ('failed','succeeded') THEN
        UPDATE ops.managed_ollama_holds
        SET released_at=clock_timestamp(), version=version+1, updated_at=clock_timestamp()
        WHERE operation_id=result.operation_id AND released_at IS NULL;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'managed Ollama terminal operation hold is missing' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEXT result;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.managed_ollama_expire_operations(
    p_owner_id uuid, p_owner_epoch bigint
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ops
AS $$
DECLARE
    expired_count bigint;
BEGIN
    WITH terminalized AS (
        UPDATE ops.managed_ollama_operations operation
        SET phase=CASE WHEN operation.created_at+interval '6 hours'<=clock_timestamp()
                       THEN 'failed' ELSE 'superseded' END,
            error_code=CASE WHEN operation.created_at+interval '6 hours'<=clock_timestamp()
                            THEN 'LOCAL_MODEL_RUNTIME_PREPARATION_TIMEOUT' ELSE NULL END,
            error_retryable=false, terminal_at=clock_timestamp(),
            claim_owner_id=NULL, claim_owner_epoch=NULL, claim_expires_at=NULL,
            version=operation.version+1, updated_at=clock_timestamp()
        WHERE operation.terminal_at IS NULL
          AND (
              operation.created_at+interval '6 hours'<=clock_timestamp()
              OR (operation.kind='active_recovery' AND NOT EXISTS (
                  SELECT 1 FROM ops.model_settings_state state
                  WHERE state.singleton=true AND state.active_revision=operation.target_revision
              ))
          )
          AND EXISTS (
              SELECT 1 FROM ops.managed_ollama_runtime runtime
              WHERE runtime.singleton=true AND runtime.owner_id=p_owner_id
                AND runtime.owner_epoch=p_owner_epoch AND runtime.lease_expires_at>clock_timestamp()
          )
        RETURNING operation.operation_id
    ), released AS (
        UPDATE ops.managed_ollama_holds hold
        SET released_at=clock_timestamp(), version=hold.version+1, updated_at=clock_timestamp()
        WHERE hold.released_at IS NULL
          AND hold.operation_id IN (SELECT operation_id FROM terminalized)
        RETURNING hold.operation_id
    )
    SELECT count(*) INTO expired_count FROM terminalized;
    RETURN expired_count;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.managed_ollama_compare_runtime_phase(
    p_owner_id uuid, p_owner_epoch bigint, p_expected_version bigint, p_expected_phase text,
    p_next_phase text, p_requirement_hash text, p_ready_requirement_hash text, p_child_epoch bigint,
    p_error_code text, p_error_retryable boolean, p_expected_settings_state_version bigint
) RETURNS SETOF ops.managed_ollama_runtime
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ops
AS $$
BEGIN
    RETURN QUERY
    UPDATE ops.managed_ollama_runtime
    SET observed_phase=p_next_phase, requirement_hash=p_requirement_hash,
        ready_requirement_hash=p_ready_requirement_hash, child_epoch=p_child_epoch,
        last_error_code=p_error_code, last_error_retryable=p_error_retryable,
        version=version+1, updated_at=clock_timestamp()
    WHERE singleton=true AND owner_id=p_owner_id AND owner_epoch=p_owner_epoch AND version=p_expected_version
      AND observed_phase=p_expected_phase AND lease_expires_at>clock_timestamp()
      AND (SELECT version FROM ops.model_settings_state WHERE singleton=true)=p_expected_settings_state_version
    RETURNING *;
END
$$;
-- +goose StatementEnd

-- This projection intentionally cannot return endpoints or secret references.
-- The legacy exact loopback relay remains a local chat source, but that
-- compatibility decision is made inside the view rather than in the manager.
CREATE OR REPLACE VIEW ops.managed_ollama_revision_requirements
WITH (security_barrier=true) AS
SELECT revision,
    CASE WHEN chat_provider='ollama'
              OR (chat_provider='openai-compatible' AND chat_base_url='http://127.0.0.1:11434')
         THEN chat_model END AS local_chat_model,
    CASE WHEN embedding_provider='ollama' THEN embedding_model END AS local_embedding_model
FROM ops.model_settings_revisions;

-- Manager credentials are provisioned by a separate privileged one-shot. The
-- migration only creates a NOLOGIN role; the one-shot revalidates that it has
-- no privilege-bearing attributes or membership edges before enabling LOGIN.
-- +goose StatementBegin
DO $$
DECLARE
    role_has_dangerous_attribute boolean;
    role_has_membership boolean;
	role_has_unsafe_connection_state boolean;
    role_marker text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='zhixu_local_model_runtime') THEN
        CREATE ROLE zhixu_local_model_runtime NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE
            NOINHERIT NOREPLICATION NOBYPASSRLS;
    ELSE
        SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls
        INTO role_has_dangerous_attribute
        FROM pg_roles WHERE rolname='zhixu_local_model_runtime';
        SELECT EXISTS (
            SELECT 1 FROM pg_auth_members membership
            JOIN pg_roles runtime_role ON runtime_role.rolname='zhixu_local_model_runtime'
            WHERE membership.member=runtime_role.oid OR membership.roleid=runtime_role.oid
        ) INTO role_has_membership;
		SELECT rolconfig IS NOT NULL OR rolconnlimit<>-1
		       OR (rolvaliduntil IS NOT NULL AND rolvaliduntil<>'infinity'::timestamptz)
		INTO role_has_unsafe_connection_state
		FROM pg_roles WHERE rolname='zhixu_local_model_runtime';
        SELECT shobj_description(oid, 'pg_authid')
        INTO role_marker
        FROM pg_roles WHERE rolname='zhixu_local_model_runtime';
        IF role_marker IS DISTINCT FROM 'zhixu-managed-local-model-runtime/v1'
		   OR role_has_dangerous_attribute OR role_has_membership OR role_has_unsafe_connection_state THEN
            RAISE EXCEPTION 'managed Ollama runtime role is untrusted or unsafe' USING ERRCODE='55000';
        END IF;
    END IF;
END
$$;
-- +goose StatementEnd
ALTER ROLE zhixu_local_model_runtime NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE
    NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT -1 VALID UNTIL 'infinity';
COMMENT ON ROLE zhixu_local_model_runtime IS 'zhixu-managed-local-model-runtime/v1';
REVOKE ALL ON SCHEMA ops FROM zhixu_local_model_runtime;
GRANT USAGE ON SCHEMA ops TO zhixu_local_model_runtime;
REVOKE ALL ON ops.model_settings_state,ops.model_settings_revisions,
    ops.managed_ollama_runtime,ops.managed_ollama_holds,
    ops.managed_ollama_operations,ops.managed_ollama_revision_requirements
    FROM zhixu_local_model_runtime;
GRANT SELECT(singleton,phase,active_revision,target_revision,previous_active_revision,version)
    ON ops.model_settings_state TO zhixu_local_model_runtime;
GRANT SELECT ON ops.managed_ollama_revision_requirements TO zhixu_local_model_runtime;
GRANT SELECT ON ops.managed_ollama_runtime,ops.managed_ollama_holds,
    ops.managed_ollama_operations TO zhixu_local_model_runtime;
REVOKE ALL ON FUNCTION
    ops.managed_ollama_claim_manager(uuid,interval,interval),
    ops.managed_ollama_heartbeat_manager(uuid,bigint,bigint,interval),
    ops.managed_ollama_publish_demand(uuid,bigint,bigint,bigint,text,jsonb,bigint),
    ops.managed_ollama_seed_active_recovery(uuid,bigint,bigint,interval),
    ops.managed_ollama_claim_operation(uuid,text,text,text,bigint,uuid,text,jsonb,uuid,bigint,interval),
    ops.managed_ollama_begin_pull_attempt(uuid,uuid,bigint,bigint,interval),
    ops.managed_ollama_record_operation_progress(uuid,uuid,bigint,bigint,text,jsonb,jsonb,bigint,bigint,boolean,interval,text),
    ops.managed_ollama_complete_operation(uuid,uuid,bigint,bigint,text,text,boolean,jsonb,bigint,bigint),
    ops.managed_ollama_expire_operations(uuid,bigint),
    ops.managed_ollama_compare_runtime_phase(uuid,bigint,bigint,text,text,text,text,bigint,text,boolean,bigint)
FROM PUBLIC, zhixu_local_model_runtime;
GRANT EXECUTE ON FUNCTION
    ops.managed_ollama_claim_manager(uuid,interval,interval),
    ops.managed_ollama_heartbeat_manager(uuid,bigint,bigint,interval),
    ops.managed_ollama_publish_demand(uuid,bigint,bigint,bigint,text,jsonb,bigint),
    ops.managed_ollama_seed_active_recovery(uuid,bigint,bigint,interval),
    ops.managed_ollama_claim_operation(uuid,text,text,text,bigint,uuid,text,jsonb,uuid,bigint,interval),
    ops.managed_ollama_begin_pull_attempt(uuid,uuid,bigint,bigint,interval),
    ops.managed_ollama_record_operation_progress(uuid,uuid,bigint,bigint,text,jsonb,jsonb,bigint,bigint,boolean,interval,text),
    ops.managed_ollama_complete_operation(uuid,uuid,bigint,bigint,text,text,boolean,jsonb,bigint,bigint),
    ops.managed_ollama_expire_operations(uuid,bigint),
    ops.managed_ollama_compare_runtime_phase(uuid,bigint,bigint,text,text,text,text,bigint,text,boolean,bigint)
TO zhixu_local_model_runtime;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ops.managed_ollama_holds WHERE released_at IS NULL)
       OR EXISTS (SELECT 1 FROM ops.managed_ollama_operations WHERE terminal_at IS NULL) THEN
        RAISE EXCEPTION 'managed Ollama lifecycle has live facts; use a forward cleanup' USING ERRCODE='55000';
    END IF;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON ops.managed_ollama_runtime,ops.managed_ollama_holds,
    ops.managed_ollama_operations FROM zhixu_local_model_runtime;
REVOKE SELECT ON ops.managed_ollama_revision_requirements FROM zhixu_local_model_runtime;
REVOKE SELECT(singleton,phase,active_revision,target_revision,previous_active_revision,version)
    ON ops.model_settings_state FROM zhixu_local_model_runtime;
REVOKE USAGE ON SCHEMA ops FROM zhixu_local_model_runtime;
REVOKE ALL ON FUNCTION
    ops.managed_ollama_claim_manager(uuid,interval,interval),
    ops.managed_ollama_heartbeat_manager(uuid,bigint,bigint,interval),
    ops.managed_ollama_publish_demand(uuid,bigint,bigint,bigint,text,jsonb,bigint),
    ops.managed_ollama_seed_active_recovery(uuid,bigint,bigint,interval),
    ops.managed_ollama_claim_operation(uuid,text,text,text,bigint,uuid,text,jsonb,uuid,bigint,interval),
    ops.managed_ollama_begin_pull_attempt(uuid,uuid,bigint,bigint,interval),
    ops.managed_ollama_record_operation_progress(uuid,uuid,bigint,bigint,text,jsonb,jsonb,bigint,bigint,boolean,interval,text),
    ops.managed_ollama_complete_operation(uuid,uuid,bigint,bigint,text,text,boolean,jsonb,bigint,bigint),
    ops.managed_ollama_expire_operations(uuid,bigint),
    ops.managed_ollama_compare_runtime_phase(uuid,bigint,bigint,text,text,text,text,bigint,text,boolean,bigint)
FROM zhixu_local_model_runtime;
DROP VIEW ops.managed_ollama_revision_requirements;
DROP FUNCTION ops.managed_ollama_compare_runtime_phase(uuid,bigint,bigint,text,text,text,text,bigint,text,boolean,bigint);
DROP FUNCTION ops.managed_ollama_complete_operation(uuid,uuid,bigint,bigint,text,text,boolean,jsonb,bigint,bigint);
DROP FUNCTION ops.managed_ollama_record_operation_progress(uuid,uuid,bigint,bigint,text,jsonb,jsonb,bigint,bigint,boolean,interval,text);
DROP FUNCTION ops.managed_ollama_expire_operations(uuid,bigint);
DROP FUNCTION ops.managed_ollama_begin_pull_attempt(uuid,uuid,bigint,bigint,interval);
DROP FUNCTION ops.managed_ollama_claim_operation(uuid,text,text,text,bigint,uuid,text,jsonb,uuid,bigint,interval);
DROP FUNCTION ops.managed_ollama_seed_active_recovery(uuid,bigint,bigint,interval);
DROP FUNCTION ops.managed_ollama_publish_demand(uuid,bigint,bigint,bigint,text,jsonb,bigint);
DROP FUNCTION ops.managed_ollama_heartbeat_manager(uuid,bigint,bigint,interval);
DROP FUNCTION ops.managed_ollama_claim_manager(uuid,interval,interval);
DROP TABLE ops.managed_ollama_operations;
DROP TABLE ops.managed_ollama_holds;
DROP TABLE ops.managed_ollama_runtime;
DROP FUNCTION ops.enforce_managed_ollama_operation();
DROP FUNCTION ops.enforce_managed_ollama_hold();
DROP FUNCTION ops.enforce_managed_ollama_runtime();
DROP FUNCTION ops.reject_managed_ollama_delete();

LOCK TABLE ops.model_settings_revisions IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ops.model_settings_revisions WHERE chat_provider='ollama') THEN
        RAISE EXCEPTION 'ollama chat model settings revisions exist; use a forward migration' USING ERRCODE='55000';
    END IF;
END
$$;
-- +goose StatementEnd
ALTER TABLE ops.model_settings_revisions
    DROP CONSTRAINT ops_model_settings_chat_provider,
    DROP CONSTRAINT ops_model_settings_chat_target,
    DROP CONSTRAINT ops_model_settings_disabled_secret,
    ADD CONSTRAINT ops_model_settings_chat_provider CHECK (chat_provider IN ('disabled','openai-compatible')),
    ADD CONSTRAINT ops_model_settings_chat_target CHECK ((
        (chat_provider='disabled' AND chat_base_url='' AND chat_model='' AND chat_model_version='')
        OR (chat_provider='openai-compatible' AND btrim(chat_base_url)<>'' AND btrim(chat_model)<>'' AND btrim(chat_model_version)<>'')
    ) IS TRUE),
    ADD CONSTRAINT ops_model_settings_disabled_secret CHECK (
        (chat_provider<>'disabled' OR chat_secret_key_id IS NULL)
        AND (embedding_provider<>'disabled' OR embedding_secret_key_id IS NULL)
        AND (embedding_provider<>'ollama' OR embedding_secret_key_id IS NULL)
    );
