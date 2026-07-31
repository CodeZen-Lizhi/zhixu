-- +goose Up

CREATE TABLE ops.workspace_registry_legacy_snapshot (
    workspace_id uuid PRIMARY KEY REFERENCES core.workspace(id) ON DELETE RESTRICT,
    status text NOT NULL,
    version bigint NOT NULL,
    updated_at timestamptz NOT NULL
);

INSERT INTO ops.workspace_registry_legacy_snapshot(workspace_id,status,version,updated_at)
SELECT id,status,version,updated_at
FROM core.workspace;

ALTER TABLE core.workspace
    ADD COLUMN root_fingerprint text,
    ADD COLUMN binding_version bigint NOT NULL DEFAULT 0,
    ADD COLUMN availability text NOT NULL DEFAULT 'migration_required',
    ADD COLUMN availability_reason text DEFAULT 'WORKSPACE_BINDING_LEGACY_DIRECT',
    ADD COLUMN availability_checked_at timestamptz DEFAULT clock_timestamp(),
    ADD COLUMN last_opened_at timestamptz,
    ADD COLUMN removed_at timestamptz;

ALTER TABLE core.workspace
    DROP CONSTRAINT IF EXISTS workspace_status_check;

UPDATE core.workspace
SET status='inactive',
    availability='migration_required',
    availability_reason='WORKSPACE_BINDING_LEGACY_UNVERIFIED',
    availability_checked_at=GREATEST(created_at,clock_timestamp()),
    version=version+1,
    updated_at=GREATEST(updated_at,created_at,clock_timestamp());

ALTER TABLE core.workspace
    ADD CONSTRAINT workspace_status_lifecycle CHECK (status IN ('active','inactive')),
    ADD CONSTRAINT workspace_root_binding CHECK ((
        (binding_version=0 AND root_fingerprint IS NULL AND availability='migration_required')
        OR (binding_version>0 AND root_fingerprint ~ '^[0-9a-f]{64}$')
    ) IS TRUE),
    ADD CONSTRAINT workspace_availability CHECK (
        availability IN ('available','unavailable','migration_required')
    ),
    ADD CONSTRAINT workspace_availability_shape CHECK ((
        (availability='available' AND availability_reason IS NULL AND availability_checked_at IS NOT NULL)
        OR (availability IN ('unavailable','migration_required')
            AND availability_reason ~ '^[A-Z0-9_]{1,128}$'
            AND availability_checked_at IS NOT NULL)
    ) IS TRUE),
    ADD CONSTRAINT workspace_registry_text_bounds CHECK (
        octet_length(name)<=256
        AND octet_length(root_path)<=4096
        AND octet_length(git_repository_path)<=4096
    ),
    ADD CONSTRAINT workspace_removed_inactive CHECK (removed_at IS NULL OR status='inactive'),
    ADD CONSTRAINT workspace_registry_times CHECK (
        (last_opened_at IS NULL OR last_opened_at>=created_at)
        AND (removed_at IS NULL OR removed_at>=created_at)
    );

CREATE UNIQUE INDEX uq_workspace_root_fingerprint
    ON core.workspace(root_fingerprint)
    WHERE root_fingerprint IS NOT NULL;

CREATE INDEX idx_workspace_registry_recent
    ON core.workspace(removed_at,last_opened_at DESC NULLS LAST,created_at DESC,id);

-- +goose StatementBegin
CREATE FUNCTION core.enforce_workspace_registry()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'workspace registry identities must be soft removed' USING ERRCODE='55000';
    END IF;
    IF TG_OP='INSERT' THEN
        RETURN NEW;
    END IF;
    IF ROW(NEW.root_path,NEW.git_repository_path,NEW.root_fingerprint,NEW.binding_version)
       IS DISTINCT FROM ROW(OLD.root_path,OLD.git_repository_path,OLD.root_fingerprint,OLD.binding_version) THEN
        RAISE EXCEPTION 'workspace root binding is immutable' USING ERRCODE='55000';
    END IF;
    IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'workspace registry version is invalid' USING ERRCODE='40001';
    END IF;
    IF OLD.removed_at IS NOT NULL AND NEW.removed_at IS NULL
       AND NEW.availability<>'available' THEN
        RAISE EXCEPTION 'removed workspace can only be restored after availability verification' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER workspace_registry_guard
    BEFORE INSERT OR UPDATE OR DELETE ON core.workspace
    FOR EACH ROW EXECUTE FUNCTION core.enforce_workspace_registry();

CREATE TRIGGER workspace_registry_reject_truncate
    BEFORE TRUNCATE ON core.workspace
    FOR EACH STATEMENT EXECUTE FUNCTION core.enforce_workspace_registry();

CREATE TABLE ops.runtime_mutation_gate (
    singleton boolean PRIMARY KEY DEFAULT true,
    owner_kind text,
    owner_id uuid,
    lease_expires_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT runtime_mutation_gate_singleton CHECK (singleton),
    CONSTRAINT runtime_mutation_gate_owner CHECK ((
        (owner_kind IS NULL AND owner_id IS NULL AND lease_expires_at IS NULL)
        OR (owner_kind IN ('workspace_switch','model_settings')
            AND owner_id IS NOT NULL AND lease_expires_at IS NOT NULL)
    ) IS TRUE),
    CONSTRAINT runtime_mutation_gate_version CHECK (version>0)
);

INSERT INTO ops.runtime_mutation_gate(singleton) VALUES(true);

-- +goose StatementBegin
CREATE FUNCTION ops.enforce_runtime_mutation_gate()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'runtime mutation gate cannot be deleted' USING ERRCODE='55000';
    END IF;
    IF TG_OP='INSERT' THEN
        RETURN NEW;
    END IF;
    IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'runtime mutation gate version is invalid' USING ERRCODE='40001';
    END IF;
    IF OLD.owner_id IS NOT NULL AND NEW.owner_id IS NOT NULL
       AND NEW.owner_id IS DISTINCT FROM OLD.owner_id
       AND OLD.lease_expires_at>NEW.updated_at THEN
        RAISE EXCEPTION 'runtime mutation gate is held' USING ERRCODE='55P03';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER runtime_mutation_gate_guard
    BEFORE INSERT OR UPDATE OR DELETE ON ops.runtime_mutation_gate
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_runtime_mutation_gate();

CREATE TRIGGER runtime_mutation_gate_reject_truncate
    BEFORE TRUNCATE ON ops.runtime_mutation_gate
    FOR EACH STATEMENT EXECUTE FUNCTION ops.enforce_runtime_mutation_gate();

-- +goose StatementBegin
CREATE FUNCTION ops.sync_model_settings_mutation_gate()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    changed_rows integer;
BEGIN
    IF NEW.phase IN ('validating','draining','applying','verifying') THEN
        UPDATE ops.runtime_mutation_gate
        SET owner_kind='model_settings',
            owner_id=NEW.rollout_id,
            lease_expires_at=NEW.lease_expires_at,
            version=version+1,
            updated_at=NEW.updated_at
        WHERE singleton=true
          AND (owner_id IS NULL OR (owner_kind='model_settings' AND owner_id=NEW.rollout_id));
        GET DIAGNOSTICS changed_rows=ROW_COUNT;
        IF changed_rows<>1 THEN
            RAISE EXCEPTION 'runtime mutation gate is held by another operation' USING ERRCODE='55P03';
        END IF;
    ELSIF OLD.phase IN ('validating','draining','applying','verifying')
          AND NEW.phase IN ('idle','failed') THEN
        UPDATE ops.runtime_mutation_gate
        SET owner_kind=NULL,
            owner_id=NULL,
            lease_expires_at=NULL,
            version=version+1,
            updated_at=NEW.updated_at
        WHERE singleton=true AND owner_kind='model_settings' AND owner_id=OLD.rollout_id;
        GET DIAGNOSTICS changed_rows=ROW_COUNT;
        IF changed_rows<>1 THEN
            RAISE EXCEPTION 'model settings mutation gate ownership is corrupt' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER model_settings_runtime_mutation_gate
    AFTER UPDATE ON ops.model_settings_state
    FOR EACH ROW EXECUTE FUNCTION ops.sync_model_settings_mutation_gate();

UPDATE ops.runtime_mutation_gate AS gate
SET owner_kind='model_settings',
    owner_id=state.rollout_id,
    lease_expires_at=state.lease_expires_at,
    version=gate.version+1,
    updated_at=GREATEST(gate.updated_at,state.updated_at)
FROM ops.model_settings_state AS state
WHERE gate.singleton=true
  AND state.singleton=true
  AND state.phase IN ('validating','draining','applying','verifying');

CREATE TABLE ops.workspace_switch (
    id uuid PRIMARY KEY,
    controller_instance_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    previous_workspace_id uuid REFERENCES core.workspace(id) ON DELETE RESTRICT,
    target_workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    target_root_fingerprint text NOT NULL,
    target_binding_version bigint NOT NULL,
    grant_generation bigint NOT NULL,
    recovery_generation bigint NOT NULL,
    expected_state_version bigint NOT NULL,
    phase text NOT NULL,
    result text,
    lease_owner_id uuid,
    lease_expires_at timestamptz,
    heartbeat_at timestamptz NOT NULL,
    deadline_at timestamptz NOT NULL,
    error_code text,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    CONSTRAINT workspace_switch_idempotency UNIQUE(controller_instance_id,idempotency_key),
    CONSTRAINT workspace_switch_request CHECK (
        octet_length(idempotency_key) BETWEEN 1 AND 256
        AND request_hash ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT workspace_switch_binding CHECK (
        target_root_fingerprint ~ '^[0-9a-f]{64}$'
        AND target_binding_version>0
        AND grant_generation>0
        AND recovery_generation>grant_generation
        AND expected_state_version>0
    ),
    CONSTRAINT workspace_switch_phase CHECK (phase IN (
        'validating','quiescing','revoking','applying_grant','preparing','verifying',
        'committing','activating','rolling_back','recovering'
    )),
    CONSTRAINT workspace_switch_result CHECK (
        result IS NULL OR result IN ('succeeded','rejected','cancelled','rolled_back','failed')
    ),
    CONSTRAINT workspace_switch_terminal_shape CHECK ((
        (result IS NULL AND lease_owner_id IS NOT NULL AND lease_expires_at IS NOT NULL
            AND completed_at IS NULL AND error_code IS NULL)
        OR (result IN ('succeeded','cancelled') AND lease_owner_id IS NULL AND lease_expires_at IS NULL
            AND completed_at IS NOT NULL AND error_code IS NULL)
        OR (result IN ('rejected','rolled_back','failed') AND lease_owner_id IS NULL
            AND lease_expires_at IS NULL AND completed_at IS NOT NULL
            AND error_code ~ '^[A-Z0-9_]{1,128}$')
    ) IS TRUE),
    CONSTRAINT workspace_switch_times CHECK (
        heartbeat_at>=created_at
        AND deadline_at>created_at
        AND updated_at>=created_at
        AND (lease_expires_at IS NULL OR lease_expires_at>heartbeat_at)
        AND (completed_at IS NULL OR completed_at>=created_at)
    ),
    CONSTRAINT workspace_switch_version CHECK (version>0)
);

CREATE UNIQUE INDEX uq_workspace_switch_inflight
    ON ops.workspace_switch((true))
    WHERE result IS NULL;

CREATE INDEX idx_workspace_switch_recent
    ON ops.workspace_switch(created_at DESC,id DESC);

-- +goose StatementBegin
CREATE FUNCTION ops.enforce_workspace_switch()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'workspace switch history is append-preserving' USING ERRCODE='55000';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 OR NEW.result IS NOT NULL THEN
            RAISE EXCEPTION 'workspace switch initial state is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.result IS NOT NULL THEN
        RAISE EXCEPTION 'terminal workspace switch is immutable' USING ERRCODE='55000';
    END IF;
    IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
       OR NEW.heartbeat_at<OLD.heartbeat_at THEN
        RAISE EXCEPTION 'workspace switch version is invalid' USING ERRCODE='40001';
    END IF;
    IF ROW(NEW.controller_instance_id,NEW.idempotency_key,NEW.request_hash,
           NEW.previous_workspace_id,NEW.target_workspace_id,NEW.target_root_fingerprint,
           NEW.target_binding_version,NEW.grant_generation,NEW.recovery_generation,
           NEW.expected_state_version,NEW.deadline_at,NEW.created_at)
       IS DISTINCT FROM
       ROW(OLD.controller_instance_id,OLD.idempotency_key,OLD.request_hash,
           OLD.previous_workspace_id,OLD.target_workspace_id,OLD.target_root_fingerprint,
           OLD.target_binding_version,OLD.grant_generation,OLD.recovery_generation,
           OLD.expected_state_version,OLD.deadline_at,OLD.created_at) THEN
        RAISE EXCEPTION 'workspace switch binding is immutable' USING ERRCODE='55000';
    END IF;
    IF NEW.result IS NULL
       AND NEW.lease_owner_id IS DISTINCT FROM OLD.lease_owner_id
       AND OLD.lease_expires_at>NEW.updated_at THEN
        RAISE EXCEPTION 'workspace switch lease is held' USING ERRCODE='55P03';
    END IF;
    IF NOT (
        NEW.phase=OLD.phase
        OR (OLD.phase='validating' AND NEW.phase='quiescing')
        OR (OLD.phase='quiescing' AND NEW.phase='revoking')
        OR (OLD.phase='revoking' AND NEW.phase='applying_grant')
        OR (OLD.phase='applying_grant' AND NEW.phase='preparing')
        OR (OLD.phase='preparing' AND NEW.phase='verifying')
        OR (OLD.phase='verifying' AND NEW.phase='committing')
        OR (OLD.phase='committing' AND NEW.phase='activating')
        OR (OLD.phase IN ('revoking','applying_grant','preparing','verifying','committing','activating')
            AND NEW.phase='rolling_back')
        OR (OLD.phase='rolling_back' AND NEW.phase='recovering')
    ) THEN
        RAISE EXCEPTION 'workspace switch phase transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.result IS NOT NULL AND NOT (
        (NEW.result='succeeded' AND NEW.phase='activating')
        OR (NEW.result='rejected' AND NEW.phase='validating')
        OR (NEW.result='cancelled' AND NEW.phase IN ('validating','quiescing'))
        OR (NEW.result IN ('rolled_back','failed') AND NEW.phase='recovering')
    ) THEN
        RAISE EXCEPTION 'workspace switch terminal transition is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER workspace_switch_guard
    BEFORE INSERT OR UPDATE OR DELETE ON ops.workspace_switch
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_workspace_switch();

CREATE TRIGGER workspace_switch_reject_truncate
    BEFORE TRUNCATE ON ops.workspace_switch
    FOR EACH STATEMENT EXECUTE FUNCTION ops.enforce_workspace_switch();

CREATE TABLE ops.workspace_control_state (
    singleton boolean PRIMARY KEY DEFAULT true,
    active_workspace_id uuid REFERENCES core.workspace(id) ON DELETE RESTRICT,
    resume_workspace_id uuid REFERENCES core.workspace(id) ON DELETE RESTRICT,
    grant_generation bigint NOT NULL DEFAULT 0,
    state_version bigint NOT NULL DEFAULT 1,
    operation_id uuid REFERENCES ops.workspace_switch(id) ON DELETE RESTRICT,
    operation_phase text,
    target_workspace_id uuid REFERENCES core.workspace(id) ON DELETE RESTRICT,
    previous_active_workspace_id uuid REFERENCES core.workspace(id) ON DELETE RESTRICT,
    controller_lease_owner_id uuid,
    controller_lease_expires_at timestamptz,
    last_error_code text,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT workspace_control_singleton CHECK (singleton),
    CONSTRAINT workspace_control_versions CHECK (grant_generation>=0 AND state_version>0),
    CONSTRAINT workspace_control_operation_phase CHECK (
        operation_phase IS NULL OR operation_phase IN (
            'validating','quiescing','revoking','applying_grant','preparing','verifying',
            'committing','activating','rolling_back','recovering'
        )
    ),
    CONSTRAINT workspace_control_operation_shape CHECK ((
        (operation_id IS NULL AND operation_phase IS NULL AND target_workspace_id IS NULL
            AND previous_active_workspace_id IS NULL AND controller_lease_owner_id IS NULL
            AND controller_lease_expires_at IS NULL)
        OR (operation_id IS NOT NULL AND operation_phase IS NOT NULL AND target_workspace_id IS NOT NULL
            AND controller_lease_owner_id IS NOT NULL AND controller_lease_expires_at IS NOT NULL)
    ) IS TRUE),
    CONSTRAINT workspace_control_error CHECK (
        last_error_code IS NULL OR last_error_code ~ '^[A-Z0-9_]{1,128}$'
    )
);

INSERT INTO ops.workspace_control_state(singleton) VALUES(true);

-- +goose StatementBegin
CREATE FUNCTION ops.enforce_workspace_control_state()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'workspace control singleton cannot be deleted' USING ERRCODE='55000';
    END IF;
    IF TG_OP='INSERT' THEN
        RETURN NEW;
    END IF;
    IF NEW.state_version<>OLD.state_version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'workspace control state version is invalid' USING ERRCODE='40001';
    END IF;
    IF NEW.grant_generation<OLD.grant_generation THEN
        RAISE EXCEPTION 'workspace grant generation cannot move backwards' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER workspace_control_state_guard
    BEFORE INSERT OR UPDATE OR DELETE ON ops.workspace_control_state
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_workspace_control_state();

CREATE TRIGGER workspace_control_state_reject_truncate
    BEFORE TRUNCATE ON ops.workspace_control_state
    FOR EACH STATEMENT EXECUTE FUNCTION ops.enforce_workspace_control_state();

CREATE TABLE ops.workspace_runtime (
    role text PRIMARY KEY,
    instance_id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    operation_id uuid REFERENCES ops.workspace_switch(id) ON DELETE RESTRICT,
    grant_generation bigint NOT NULL,
    root_fingerprint text NOT NULL,
    binding_version bigint NOT NULL,
    phase text NOT NULL,
    started_at timestamptz NOT NULL,
    heartbeat_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    CONSTRAINT workspace_runtime_role CHECK (role IN ('api','worker')),
    CONSTRAINT workspace_runtime_binding CHECK (
        grant_generation>0 AND root_fingerprint ~ '^[0-9a-f]{64}$' AND binding_version>0
    ),
    CONSTRAINT workspace_runtime_phase CHECK (
        phase IN ('active','quiescing','quiesced','prepared','verifying','unavailable')
    ),
    CONSTRAINT workspace_runtime_shape CHECK ((
        (phase IN ('active','unavailable') AND operation_id IS NULL)
        OR (phase IN ('quiescing','quiesced','prepared','verifying') AND operation_id IS NOT NULL)
    ) IS TRUE),
    CONSTRAINT workspace_runtime_times CHECK (heartbeat_at>=started_at),
    CONSTRAINT workspace_runtime_version CHECK (version>0)
);

-- +goose StatementBegin
CREATE FUNCTION ops.enforce_workspace_runtime()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'workspace runtime ownership cannot be deleted' USING ERRCODE='55000';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM core.workspace AS workspace
        WHERE workspace.id=NEW.workspace_id
          AND workspace.root_fingerprint=NEW.root_fingerprint
          AND workspace.binding_version=NEW.binding_version
    ) THEN
        RAISE EXCEPTION 'workspace runtime binding does not match registry identity' USING ERRCODE='23514';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 THEN
            RAISE EXCEPTION 'workspace runtime initial version is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.version<>OLD.version+1 OR NEW.heartbeat_at<OLD.heartbeat_at THEN
        RAISE EXCEPTION 'workspace runtime version is invalid' USING ERRCODE='40001';
    END IF;
    IF NEW.instance_id<>OLD.instance_id THEN
        RETURN NEW;
    END IF;
    IF ROW(NEW.workspace_id,NEW.grant_generation,NEW.root_fingerprint,
           NEW.binding_version,NEW.started_at)
       IS DISTINCT FROM ROW(OLD.workspace_id,OLD.grant_generation,OLD.root_fingerprint,
           OLD.binding_version,OLD.started_at) THEN
        RAISE EXCEPTION 'workspace runtime binding is immutable for an instance' USING ERRCODE='55000';
    END IF;
    IF NOT (
        (NEW.phase=OLD.phase AND NEW.operation_id IS NOT DISTINCT FROM OLD.operation_id)
        OR (OLD.phase='active' AND NEW.phase='quiescing' AND NEW.operation_id IS NOT NULL)
        OR (OLD.phase='quiescing' AND NEW.phase='quiesced' AND NEW.operation_id=OLD.operation_id)
        OR (OLD.phase IN ('quiescing','quiesced') AND NEW.phase='active' AND NEW.operation_id IS NULL)
        OR (OLD.phase='prepared' AND NEW.phase='verifying' AND NEW.operation_id=OLD.operation_id)
        OR (OLD.phase IN ('prepared','verifying') AND NEW.phase='active' AND NEW.operation_id IS NULL)
        OR (NEW.phase='unavailable' AND NEW.operation_id IS NULL)
    ) THEN
        RAISE EXCEPTION 'workspace runtime phase transition is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER workspace_runtime_guard
    BEFORE INSERT OR UPDATE OR DELETE ON ops.workspace_runtime
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_workspace_runtime();

CREATE TRIGGER workspace_runtime_reject_truncate
    BEFORE TRUNCATE ON ops.workspace_runtime
    FOR EACH STATEMENT EXECUTE FUNCTION ops.enforce_workspace_runtime();

-- +goose Down

LOCK TABLE core.workspace, ops.workspace_registry_legacy_snapshot,
    ops.runtime_mutation_gate, ops.workspace_switch, ops.workspace_control_state,
    ops.workspace_runtime, ops.model_settings_state
    IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ops.workspace_switch LIMIT 1)
       OR EXISTS (SELECT 1 FROM ops.workspace_runtime LIMIT 1)
       OR EXISTS (
            SELECT 1 FROM ops.workspace_control_state
            WHERE active_workspace_id IS NOT NULL OR resume_workspace_id IS NOT NULL
               OR grant_generation<>0 OR state_version<>1 OR operation_id IS NOT NULL
               OR last_error_code IS NOT NULL
            LIMIT 1
       )
       OR EXISTS (
            SELECT 1 FROM ops.runtime_mutation_gate
            WHERE owner_kind='workspace_switch'
            LIMIT 1
       )
       OR EXISTS (
            SELECT 1
            FROM core.workspace AS workspace
            LEFT JOIN ops.workspace_registry_legacy_snapshot AS legacy
              ON legacy.workspace_id=workspace.id
            WHERE legacy.workspace_id IS NULL
               OR workspace.status<>'inactive'
               OR workspace.root_fingerprint IS NOT NULL
               OR workspace.binding_version<>0
               OR workspace.availability<>'migration_required'
               OR workspace.availability_reason<>'WORKSPACE_BINDING_LEGACY_UNVERIFIED'
               OR workspace.version<>legacy.version+1
               OR workspace.last_opened_at IS NOT NULL
               OR workspace.removed_at IS NOT NULL
            LIMIT 1
       )
       OR EXISTS (
            SELECT 1
            FROM ops.workspace_registry_legacy_snapshot AS legacy
            LEFT JOIN core.workspace AS workspace ON workspace.id=legacy.workspace_id
            WHERE workspace.id IS NULL
            LIMIT 1
       ) THEN
        RAISE EXCEPTION 'workspace registry or switch history must be archived before rollback'
            USING ERRCODE='55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS model_settings_runtime_mutation_gate ON ops.model_settings_state;
DROP FUNCTION IF EXISTS ops.sync_model_settings_mutation_gate();

DROP TRIGGER IF EXISTS workspace_runtime_reject_truncate ON ops.workspace_runtime;
DROP TRIGGER IF EXISTS workspace_runtime_guard ON ops.workspace_runtime;
DROP FUNCTION IF EXISTS ops.enforce_workspace_runtime();
DROP TABLE IF EXISTS ops.workspace_runtime;

DROP TRIGGER IF EXISTS workspace_control_state_reject_truncate ON ops.workspace_control_state;
DROP TRIGGER IF EXISTS workspace_control_state_guard ON ops.workspace_control_state;
DROP FUNCTION IF EXISTS ops.enforce_workspace_control_state();
DROP TABLE IF EXISTS ops.workspace_control_state;

DROP TRIGGER IF EXISTS workspace_switch_reject_truncate ON ops.workspace_switch;
DROP TRIGGER IF EXISTS workspace_switch_guard ON ops.workspace_switch;
DROP FUNCTION IF EXISTS ops.enforce_workspace_switch();
DROP TABLE IF EXISTS ops.workspace_switch;

DROP TRIGGER IF EXISTS runtime_mutation_gate_reject_truncate ON ops.runtime_mutation_gate;
DROP TRIGGER IF EXISTS runtime_mutation_gate_guard ON ops.runtime_mutation_gate;
DROP FUNCTION IF EXISTS ops.enforce_runtime_mutation_gate();
DROP TABLE IF EXISTS ops.runtime_mutation_gate;

DROP TRIGGER IF EXISTS workspace_registry_reject_truncate ON core.workspace;
DROP TRIGGER IF EXISTS workspace_registry_guard ON core.workspace;
DROP FUNCTION IF EXISTS core.enforce_workspace_registry();

ALTER TABLE core.workspace
    DROP CONSTRAINT IF EXISTS workspace_registry_times,
    DROP CONSTRAINT IF EXISTS workspace_removed_inactive,
    DROP CONSTRAINT IF EXISTS workspace_registry_text_bounds,
    DROP CONSTRAINT IF EXISTS workspace_availability_shape,
    DROP CONSTRAINT IF EXISTS workspace_availability,
    DROP CONSTRAINT IF EXISTS workspace_root_binding,
    DROP CONSTRAINT IF EXISTS workspace_status_lifecycle;

DROP INDEX IF EXISTS core.idx_workspace_registry_recent;
DROP INDEX IF EXISTS core.uq_workspace_root_fingerprint;

UPDATE core.workspace AS workspace
SET status=legacy.status,
    version=legacy.version,
    updated_at=legacy.updated_at
FROM ops.workspace_registry_legacy_snapshot AS legacy
WHERE legacy.workspace_id=workspace.id;

ALTER TABLE core.workspace
    DROP COLUMN IF EXISTS removed_at,
    DROP COLUMN IF EXISTS last_opened_at,
    DROP COLUMN IF EXISTS availability_checked_at,
    DROP COLUMN IF EXISTS availability_reason,
    DROP COLUMN IF EXISTS availability,
    DROP COLUMN IF EXISTS binding_version,
    DROP COLUMN IF EXISTS root_fingerprint,
    ADD CONSTRAINT workspace_status_check CHECK (btrim(status)<> '');

DROP TABLE IF EXISTS ops.workspace_registry_legacy_snapshot;
