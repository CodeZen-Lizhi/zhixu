-- +goose Up

-- Memory belongs to the stable single-user subject, not to a revocable
-- Session or API Token credential. Credential identities remain auth facts.
DROP TRIGGER IF EXISTS memory_project_server_event ON learning.memory;
DROP TRIGGER IF EXISTS trg_learning_memory_lifecycle ON learning.memory;
DROP TRIGGER IF EXISTS trg_learning_memory_audit_append_only ON learning.memory_audit;

ALTER TABLE learning.memory
    DROP CONSTRAINT IF EXISTS learning_memory_owner_principal_check;
ALTER TABLE learning.memory
    DROP CONSTRAINT IF EXISTS learning_memory_confirmation_pair_check;
ALTER TABLE learning.memory_command
    DROP CONSTRAINT IF EXISTS memory_command_owner_principal_kind_check;
ALTER TABLE learning.memory_audit
    DROP CONSTRAINT IF EXISTS memory_audit_owner_principal_kind_check;
ALTER TABLE learning.memory_audit
    DROP CONSTRAINT IF EXISTS memory_audit_actor_principal_kind_check;
ALTER TABLE learning.memory_audit
    DROP CONSTRAINT IF EXISTS learning_memory_audit_actor_binding_check;

UPDATE learning.memory
SET owner_principal_kind = 'USER',
    owner_principal_id = '00000000-0000-5000-8000-000000000001'::uuid,
    confirmed_by_principal_kind = CASE WHEN confirmed_at IS NULL THEN NULL ELSE 'USER' END,
    confirmed_by_principal_id = CASE
        WHEN confirmed_at IS NULL THEN NULL
        ELSE '00000000-0000-5000-8000-000000000001'::uuid
    END
WHERE owner_principal_kind IN ('SESSION', 'API_TOKEN');

UPDATE learning.memory_command
SET owner_principal_kind = 'USER',
    owner_principal_id = '00000000-0000-5000-8000-000000000001'::uuid,
    response = jsonb_set(
        jsonb_set(
            CASE
                WHEN response ? 'confirmed_by' THEN jsonb_set(
                    jsonb_set(response, '{confirmed_by,kind}', to_jsonb('USER'::text), false),
                    '{confirmed_by,id}', to_jsonb('00000000-0000-5000-8000-000000000001'::text), false
                )
                ELSE response
            END,
            '{owner,kind}', to_jsonb('USER'::text), false
        ),
        '{owner,id}', to_jsonb('00000000-0000-5000-8000-000000000001'::text), false
    )
WHERE owner_principal_kind IN ('SESSION', 'API_TOKEN');

UPDATE learning.memory_audit
SET owner_principal_kind = 'USER',
    owner_principal_id = '00000000-0000-5000-8000-000000000001'::uuid
WHERE owner_principal_kind IN ('SESSION', 'API_TOKEN');

ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_owner_principal_check
    CHECK (owner_principal_kind IN ('USER', 'LEGACY'));
ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_confirmation_pair_check
    CHECK (
        (confirmed_at IS NULL AND confirmed_by_principal_kind IS NULL AND confirmed_by_principal_id IS NULL)
        OR (
            confirmed_at IS NOT NULL
            AND confirmed_by_principal_kind IN ('USER', 'LEGACY')
            AND confirmed_by_principal_id IS NOT NULL
        )
    ) NOT VALID;
ALTER TABLE learning.memory_command
    ADD CONSTRAINT memory_command_owner_principal_kind_check
    CHECK (owner_principal_kind = 'USER');
ALTER TABLE learning.memory_audit
    ADD CONSTRAINT memory_audit_owner_principal_kind_check
    CHECK (owner_principal_kind IN ('USER', 'LEGACY'));
ALTER TABLE learning.memory_audit
    ADD CONSTRAINT memory_audit_actor_principal_kind_check
    CHECK (actor_principal_kind IN ('USER', 'SESSION', 'API_TOKEN', 'AGENT', 'SYSTEM'));
ALTER TABLE learning.memory_audit
    ADD CONSTRAINT learning_memory_audit_actor_binding_check
    CHECK (
        (actor_principal_kind IN ('USER', 'SESSION', 'API_TOKEN') AND actor_principal_id IS NOT NULL)
        OR (actor_principal_kind IN ('AGENT', 'SYSTEM') AND actor_principal_id IS NULL)
    );

-- +goose StatementBegin
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
               AND NEW.confirmed_by_principal_kind = 'USER'
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
-- +goose StatementEnd

CREATE TRIGGER trg_learning_memory_lifecycle
BEFORE INSERT OR UPDATE ON learning.memory
FOR EACH ROW EXECUTE FUNCTION learning.enforce_memory_lifecycle();

CREATE TRIGGER trg_learning_memory_audit_append_only
BEFORE UPDATE OR DELETE ON learning.memory_audit
FOR EACH ROW EXECUTE FUNCTION learning.reject_memory_audit_mutation();

CREATE TRIGGER memory_project_server_event
AFTER INSERT OR UPDATE ON learning.memory
FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('memory','memory','id','version','updated_at');

-- +goose Down

-- Stable USER ownership cannot be mapped back to the credential that happened
-- to create it. Refuse downgrade once any such durable fact exists.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.memory WHERE owner_principal_kind = 'USER' LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.memory_command WHERE owner_principal_kind = 'USER' LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.memory_audit WHERE owner_principal_kind = 'USER' OR actor_principal_kind = 'USER' LIMIT 1) THEN
        RAISE EXCEPTION 'stable memory ownership exists; archive before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS memory_project_server_event ON learning.memory;
DROP TRIGGER IF EXISTS trg_learning_memory_lifecycle ON learning.memory;
DROP TRIGGER IF EXISTS trg_learning_memory_audit_append_only ON learning.memory_audit;

ALTER TABLE learning.memory
    DROP CONSTRAINT IF EXISTS learning_memory_owner_principal_check;
ALTER TABLE learning.memory
    DROP CONSTRAINT IF EXISTS learning_memory_confirmation_pair_check;
ALTER TABLE learning.memory_command
    DROP CONSTRAINT IF EXISTS memory_command_owner_principal_kind_check;
ALTER TABLE learning.memory_audit
    DROP CONSTRAINT IF EXISTS memory_audit_owner_principal_kind_check;
ALTER TABLE learning.memory_audit
    DROP CONSTRAINT IF EXISTS memory_audit_actor_principal_kind_check;
ALTER TABLE learning.memory_audit
    DROP CONSTRAINT IF EXISTS learning_memory_audit_actor_binding_check;

ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_owner_principal_check
    CHECK (owner_principal_kind IN ('SESSION', 'API_TOKEN', 'LEGACY'));
ALTER TABLE learning.memory
    ADD CONSTRAINT learning_memory_confirmation_pair_check
    CHECK (
        (confirmed_at IS NULL AND confirmed_by_principal_kind IS NULL AND confirmed_by_principal_id IS NULL)
        OR (
            confirmed_at IS NOT NULL
            AND confirmed_by_principal_kind IN ('SESSION', 'API_TOKEN', 'LEGACY')
            AND confirmed_by_principal_id IS NOT NULL
        )
    ) NOT VALID;
ALTER TABLE learning.memory_command
    ADD CONSTRAINT memory_command_owner_principal_kind_check
    CHECK (owner_principal_kind IN ('SESSION', 'API_TOKEN'));
ALTER TABLE learning.memory_audit
    ADD CONSTRAINT memory_audit_owner_principal_kind_check
    CHECK (owner_principal_kind IN ('SESSION', 'API_TOKEN', 'LEGACY'));
ALTER TABLE learning.memory_audit
    ADD CONSTRAINT memory_audit_actor_principal_kind_check
    CHECK (actor_principal_kind IN ('SESSION', 'API_TOKEN', 'AGENT', 'SYSTEM'));
ALTER TABLE learning.memory_audit
    ADD CONSTRAINT learning_memory_audit_actor_binding_check
    CHECK (
        (actor_principal_kind IN ('SESSION', 'API_TOKEN') AND actor_principal_id IS NOT NULL)
        OR (actor_principal_kind IN ('AGENT', 'SYSTEM') AND actor_principal_id IS NULL)
    );

-- +goose StatementBegin
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
-- +goose StatementEnd

CREATE TRIGGER trg_learning_memory_lifecycle
BEFORE INSERT OR UPDATE ON learning.memory
FOR EACH ROW EXECUTE FUNCTION learning.enforce_memory_lifecycle();

CREATE TRIGGER trg_learning_memory_audit_append_only
BEFORE UPDATE OR DELETE ON learning.memory_audit
FOR EACH ROW EXECUTE FUNCTION learning.reject_memory_audit_mutation();

CREATE TRIGGER memory_project_server_event
AFTER INSERT OR UPDATE ON learning.memory
FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('memory','memory','id','version','updated_at');
