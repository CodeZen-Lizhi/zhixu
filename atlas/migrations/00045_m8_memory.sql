-- Memory is not a Knowledge fact and must be owned by one authenticated
-- principal. Historical rows had no owner/confirmation data, so they are
-- retained as LEGACY records that cannot match a current principal.
ALTER TABLE learning.memory
    ADD COLUMN IF NOT EXISTS owner_principal_kind text;
ALTER TABLE learning.memory
    ADD COLUMN IF NOT EXISTS owner_principal_id uuid;
ALTER TABLE learning.memory
    ADD COLUMN IF NOT EXISTS source_type text;
ALTER TABLE learning.memory
    ADD COLUMN IF NOT EXISTS task_scope_id uuid;
ALTER TABLE learning.memory
    ADD COLUMN IF NOT EXISTS confirmed_at timestamptz;
ALTER TABLE learning.memory
    ADD COLUMN IF NOT EXISTS confirmed_by_principal_kind text;
ALTER TABLE learning.memory
    ADD COLUMN IF NOT EXISTS confirmed_by_principal_id uuid;

ALTER TABLE learning.memory
    DROP CONSTRAINT IF EXISTS memory_status_check;
ALTER TABLE learning.memory
    DROP CONSTRAINT IF EXISTS learning_memory_status_check;

UPDATE learning.memory
SET status = CASE status
        WHEN 'CONFIRMED' THEN 'ACTIVE'
        WHEN 'REVOKED' THEN 'DELETED'
        ELSE status
    END,
    owner_principal_kind = COALESCE(owner_principal_kind, 'LEGACY'),
    owner_principal_id = COALESCE(owner_principal_id, id),
    source_type = COALESCE(source_type, 'LEGACY'),
    confirmed_at = CASE
        WHEN status = 'CONFIRMED' THEN COALESCE(confirmed_at, updated_at)
        ELSE confirmed_at
    END,
    confirmed_by_principal_kind = CASE
        WHEN status = 'CONFIRMED' THEN COALESCE(confirmed_by_principal_kind, 'LEGACY')
        ELSE confirmed_by_principal_kind
    END,
    confirmed_by_principal_id = CASE
        WHEN status = 'CONFIRMED' THEN COALESCE(confirmed_by_principal_id, id)
        ELSE confirmed_by_principal_id
    END;

ALTER TABLE learning.memory
    ALTER COLUMN owner_principal_kind SET NOT NULL;
ALTER TABLE learning.memory
    ALTER COLUMN owner_principal_id SET NOT NULL;
ALTER TABLE learning.memory
    ALTER COLUMN source_type SET NOT NULL;

ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_status_check
    CHECK (status IN ('CANDIDATE', 'ACTIVE', 'PAUSED', 'EXPIRED', 'DELETED'));
ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_owner_principal_check
    CHECK (owner_principal_kind IN ('SESSION', 'API_TOKEN', 'LEGACY'));
ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_source_type_check
    CHECK (source_type IN ('USER', 'AGENT', 'INTERVIEW', 'LEGACY'));
ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_source_ref_check
    CHECK (btrim(source_ref) <> '' AND octet_length(source_ref) <= 512) NOT VALID;
ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_confirmation_pair_check
    CHECK (
        (confirmed_at IS NULL AND confirmed_by_principal_kind IS NULL AND confirmed_by_principal_id IS NULL)
        OR (
            confirmed_at IS NOT NULL
            AND confirmed_by_principal_kind IS NOT NULL
            AND confirmed_by_principal_kind IN ('SESSION', 'API_TOKEN', 'LEGACY')
            AND confirmed_by_principal_id IS NOT NULL
        )
    ) NOT VALID;
ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_confirmation_owner_check
    CHECK (
        confirmed_at IS NULL
        OR (
            confirmed_by_principal_kind IS NOT NULL
            AND confirmed_by_principal_id IS NOT NULL
            AND confirmed_by_principal_kind = owner_principal_kind
            AND confirmed_by_principal_id = owner_principal_id
        )
    ) NOT VALID;
ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_effective_confirmation_check
    CHECK (
        status NOT IN ('ACTIVE', 'PAUSED')
        OR (confirmed_at IS NOT NULL AND confirmed_by_principal_kind IS NOT NULL AND confirmed_by_principal_id IS NOT NULL)
    ) NOT VALID;
ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_expiry_order_check
    CHECK (expires_at IS NULL OR expires_at > created_at) NOT VALID;
ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_episodic_expiry_check
    CHECK (memory_type <> 'EPISODIC' OR expires_at IS NOT NULL) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_learning_memory_owner_updated
    ON learning.memory (workspace_id, owner_principal_kind, owner_principal_id, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_learning_memory_effective_context
    ON learning.memory (workspace_id, owner_principal_kind, owner_principal_id, task_scope_id, updated_at DESC, id DESC)
    WHERE status = 'ACTIVE' AND confirmed_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_learning_memory_expiry_due
    ON learning.memory (expires_at, id)
    WHERE status IN ('CANDIDATE', 'ACTIVE', 'PAUSED') AND expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS learning.memory_command (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL,
    owner_principal_kind text NOT NULL CHECK (owner_principal_kind IN ('SESSION', 'API_TOKEN')),
    owner_principal_id uuid NOT NULL,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (command_type IN ('CREATE_CANDIDATE', 'CONFIRM', 'UPDATE', 'PAUSE', 'RESUME', 'DELETE')),
    memory_id uuid NOT NULL,
    expected_version bigint NOT NULL CHECK (expected_version >= 0),
    memory_version bigint NOT NULL CHECK (memory_version > 0),
    response jsonb NOT NULL CHECK (jsonb_typeof(response) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key),
    CONSTRAINT learning_memory_command_key_check
        CHECK (idempotency_key = btrim(idempotency_key) AND btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128),
    CONSTRAINT learning_memory_command_create_version_check
        CHECK ((command_type = 'CREATE_CANDIDATE' AND expected_version = 0) OR (command_type <> 'CREATE_CANDIDATE' AND expected_version > 0)),
    CONSTRAINT fk_learning_memory_command_memory_workspace
        FOREIGN KEY (memory_id, workspace_id)
        REFERENCES learning.memory(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_learning_memory_command_memory
    ON learning.memory_command (workspace_id, memory_id, created_at DESC);

CREATE TABLE IF NOT EXISTS learning.memory_audit (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    memory_id uuid NOT NULL,
    owner_principal_kind text NOT NULL CHECK (owner_principal_kind IN ('SESSION', 'API_TOKEN', 'LEGACY')),
    owner_principal_id uuid NOT NULL,
    actor_principal_kind text NOT NULL CHECK (actor_principal_kind IN ('SESSION', 'API_TOKEN', 'AGENT', 'SYSTEM')),
    actor_principal_id uuid,
    action text NOT NULL CHECK (action IN ('CANDIDATE_CREATED', 'CONFIRMED', 'UPDATED', 'PAUSED', 'RESUMED', 'EXPIRED', 'DELETED')),
    from_status text,
    to_status text NOT NULL CHECK (to_status IN ('CANDIDATE', 'ACTIVE', 'PAUSED', 'EXPIRED', 'DELETED')),
    memory_version bigint NOT NULL CHECK (memory_version > 0),
    idempotency_key text,
    request_hash text,
    occurred_at timestamptz NOT NULL,
    CONSTRAINT learning_memory_audit_actor_binding_check
        CHECK (
            (actor_principal_kind IN ('SESSION', 'API_TOKEN') AND actor_principal_id IS NOT NULL)
            OR (actor_principal_kind IN ('AGENT', 'SYSTEM') AND actor_principal_id IS NULL)
        ),
    CONSTRAINT learning_memory_audit_from_status_check
        CHECK (from_status IS NULL OR from_status IN ('CANDIDATE', 'ACTIVE', 'PAUSED', 'EXPIRED', 'DELETED')),
    CONSTRAINT learning_memory_audit_receipt_binding_check
        CHECK (
            (idempotency_key IS NULL AND request_hash IS NULL)
            OR (
                idempotency_key = btrim(idempotency_key)
                AND btrim(idempotency_key) <> ''
                AND octet_length(idempotency_key) <= 128
                AND request_hash ~ '^[0-9a-f]{64}$'
            )
        ),
    CONSTRAINT fk_learning_memory_audit_memory_workspace
        FOREIGN KEY (memory_id, workspace_id)
        REFERENCES learning.memory(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_learning_memory_audit_memory
    ON learning.memory_audit (workspace_id, memory_id, id);

CREATE OR REPLACE FUNCTION learning.enforce_memory_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'CANDIDATE' THEN
            RAISE EXCEPTION 'memory must be created as a candidate' USING ERRCODE = '23514';
        END IF;
        IF NEW.confirmed_at IS NOT NULL OR NEW.confirmed_by_principal_kind IS NOT NULL OR NEW.confirmed_by_principal_id IS NOT NULL THEN
            RAISE EXCEPTION 'candidate memory cannot be confirmed on insert' USING ERRCODE = '23514';
        END IF;
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'new memory must start at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.status = 'DELETED' THEN
        RAISE EXCEPTION 'deleted memory is a tombstone' USING ERRCODE = '55000';
    END IF;
    IF NEW.id <> OLD.id OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.owner_principal_kind <> OLD.owner_principal_kind OR NEW.owner_principal_id <> OLD.owner_principal_id
       OR NEW.memory_type <> OLD.memory_type
       OR NEW.source_type <> OLD.source_type OR NEW.source_ref <> OLD.source_ref THEN
        RAISE EXCEPTION 'memory identity, owner, type, and source provenance are immutable' USING ERRCODE = '23514';
    END IF;
    IF NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'memory update must advance version and timestamp' USING ERRCODE = '23514';
    END IF;

    IF OLD.status <> 'CANDIDATE'
       AND (NEW.confirmed_at, NEW.confirmed_by_principal_kind, NEW.confirmed_by_principal_id)
           IS DISTINCT FROM (OLD.confirmed_at, OLD.confirmed_by_principal_kind, OLD.confirmed_by_principal_id) THEN
        RAISE EXCEPTION 'memory confirmation is immutable after confirmation' USING ERRCODE = '23514';
    END IF;

    IF NEW.status = OLD.status THEN
        IF OLD.status = 'CANDIDATE'
           AND (NEW.confirmed_at, NEW.confirmed_by_principal_kind, NEW.confirmed_by_principal_id)
               IS DISTINCT FROM (OLD.confirmed_at, OLD.confirmed_by_principal_kind, OLD.confirmed_by_principal_id) THEN
            RAISE EXCEPTION 'candidate confirmation requires active transition' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    CASE OLD.status
        WHEN 'CANDIDATE' THEN
            IF NEW.status = 'ACTIVE'
               AND NEW.confirmed_at IS NOT NULL
               AND NEW.confirmed_by_principal_kind IN ('SESSION', 'API_TOKEN')
               AND NEW.confirmed_by_principal_id IS NOT NULL THEN
                RETURN NEW;
            END IF;
            IF NEW.status IN ('EXPIRED', 'DELETED')
               AND NEW.confirmed_at IS NULL
               AND NEW.confirmed_by_principal_kind IS NULL
               AND NEW.confirmed_by_principal_id IS NULL THEN
                RETURN NEW;
            END IF;
        WHEN 'ACTIVE' THEN
            IF NEW.status IN ('PAUSED', 'EXPIRED', 'DELETED') THEN
                RETURN NEW;
            END IF;
        WHEN 'PAUSED' THEN
            IF NEW.status IN ('ACTIVE', 'EXPIRED', 'DELETED') THEN
                RETURN NEW;
            END IF;
        WHEN 'EXPIRED' THEN
            IF NEW.status = 'DELETED' THEN
                RETURN NEW;
            END IF;
    END CASE;

    RAISE EXCEPTION 'illegal memory lifecycle transition from % to %', OLD.status, NEW.status USING ERRCODE = '23514';
END;
$$;

DROP TRIGGER IF EXISTS trg_learning_memory_lifecycle ON learning.memory;
CREATE TRIGGER trg_learning_memory_lifecycle
BEFORE INSERT OR UPDATE ON learning.memory
FOR EACH ROW EXECUTE FUNCTION learning.enforce_memory_lifecycle();

CREATE OR REPLACE FUNCTION learning.reject_memory_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'memory audit is append-only' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS trg_learning_memory_audit_append_only ON learning.memory_audit;
CREATE TRIGGER trg_learning_memory_audit_append_only
BEFORE UPDATE OR DELETE ON learning.memory_audit
FOR EACH ROW EXECUTE FUNCTION learning.reject_memory_audit_mutation();
