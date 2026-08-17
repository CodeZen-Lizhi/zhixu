-- +goose Up

-- Workspace Analysis is deliberately default-off. A Worker must advertise this
-- complete immutable contract before an API transaction may create a new Run.
-- Leases are evaluated with PostgreSQL time so API and Worker host clocks never
-- participate in rollout readiness.
CREATE TABLE agent.workspace_analysis_worker_capability (
    worker_instance_id uuid PRIMARY KEY,
    definition_key text NOT NULL,
    definition_version bigint NOT NULL,
    definition_hash text NOT NULL,
    tool_catalog_hash text NOT NULL,
    policy_version integer NOT NULL,
    config_revision bigint NOT NULL,
    heartbeat_at timestamptz NOT NULL,
    lease_until timestamptz NOT NULL,
    released_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT workspace_analysis_worker_capability_contract CHECK (
        definition_key='workspace-analysis'
        AND definition_version=1
        AND policy_version=1
        AND definition_hash ~ '^[0-9a-f]{64}$'
        AND tool_catalog_hash ~ '^[0-9a-f]{64}$'
        AND config_revision>=0
    ),
    CONSTRAINT workspace_analysis_worker_capability_lease CHECK (
        heartbeat_at<=lease_until
        AND lease_until-heartbeat_at BETWEEN interval '10 seconds' AND interval '60 seconds'
    ),
    CONSTRAINT workspace_analysis_worker_capability_release CHECK (
        released_at IS NULL OR released_at>=heartbeat_at
    ),
    CONSTRAINT workspace_analysis_worker_capability_version CHECK (version>0),
    CONSTRAINT workspace_analysis_worker_capability_times CHECK (
        created_at<=heartbeat_at AND updated_at>=created_at
    )
);

CREATE INDEX idx_workspace_analysis_worker_capability_ready
    ON agent.workspace_analysis_worker_capability (
        definition_key,definition_version,definition_hash,tool_catalog_hash,
        policy_version,config_revision,lease_until
    )
    WHERE released_at IS NULL;

-- The contract is write-once for one process identity. A renewed heartbeat can
-- only extend the same advertisement; a process that loads another Definition
-- or catalog must release and use a new UUID.
-- +goose StatementBegin
CREATE FUNCTION agent.guard_workspace_analysis_worker_capability()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    database_now timestamptz := clock_timestamp();
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1
           OR NEW.heartbeat_at>database_now
           OR NEW.created_at IS DISTINCT FROM NEW.heartbeat_at
           OR NEW.updated_at IS DISTINCT FROM NEW.heartbeat_at THEN
            RAISE EXCEPTION 'workspace analysis worker capability insert is not database-clock canonical'
                USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    IF ROW(
        NEW.worker_instance_id,NEW.definition_key,NEW.definition_version,NEW.definition_hash,
        NEW.tool_catalog_hash,NEW.policy_version,NEW.config_revision,NEW.created_at
    ) IS DISTINCT FROM ROW(
        OLD.worker_instance_id,OLD.definition_key,OLD.definition_version,OLD.definition_hash,
        OLD.tool_catalog_hash,OLD.policy_version,OLD.config_revision,OLD.created_at
    ) THEN
        RAISE EXCEPTION 'workspace analysis worker capability contract is immutable'
            USING ERRCODE='55000';
    END IF;
    IF OLD.released_at IS NOT NULL THEN
        RAISE EXCEPTION 'released workspace analysis worker capability is immutable'
            USING ERRCODE='55000';
    END IF;
    IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'workspace analysis worker capability version is invalid'
            USING ERRCODE='55000';
    END IF;

    IF NEW.released_at IS NOT NULL THEN
        IF NEW.heartbeat_at IS DISTINCT FROM OLD.heartbeat_at
           OR NEW.lease_until IS DISTINCT FROM OLD.lease_until
           OR NEW.released_at<OLD.updated_at
           OR NEW.released_at>database_now
           OR NEW.updated_at IS DISTINCT FROM NEW.released_at THEN
            RAISE EXCEPTION 'workspace analysis worker capability release is invalid'
                USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.heartbeat_at<OLD.heartbeat_at
       OR NEW.heartbeat_at>database_now
       OR NEW.updated_at IS DISTINCT FROM NEW.heartbeat_at THEN
        RAISE EXCEPTION 'workspace analysis worker capability heartbeat is invalid'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER workspace_analysis_worker_capability_guard
    BEFORE INSERT OR UPDATE ON agent.workspace_analysis_worker_capability
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_worker_capability();

-- +goose StatementBegin
CREATE FUNCTION agent.reject_workspace_analysis_worker_capability_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'workspace analysis worker capability history cannot be deleted'
        USING ERRCODE='55000';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER workspace_analysis_worker_capability_reject_delete
    BEFORE DELETE OR TRUNCATE ON agent.workspace_analysis_worker_capability
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_worker_capability_delete();

INSERT INTO core.schema_meta(key,value)
VALUES (
    'workspace_analysis_worker_capability',
    'workspace-analysis-worker-capability-v1'
)
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();

-- +goose Down

-- A released capability remains rollout history. Never erase it just to make a
-- binary rollback convenient; a forward migration is required once deployed.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM agent.workspace_analysis_worker_capability LIMIT 1
    ) THEN
        RAISE EXCEPTION 'workspace analysis worker capability history exists; rollback is unsafe'
            USING ERRCODE='55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS workspace_analysis_worker_capability_reject_delete
    ON agent.workspace_analysis_worker_capability;
DROP FUNCTION IF EXISTS agent.reject_workspace_analysis_worker_capability_delete();
DROP TRIGGER IF EXISTS workspace_analysis_worker_capability_guard
    ON agent.workspace_analysis_worker_capability;
DROP FUNCTION IF EXISTS agent.guard_workspace_analysis_worker_capability();
DROP INDEX IF EXISTS agent.idx_workspace_analysis_worker_capability_ready;
DROP TABLE agent.workspace_analysis_worker_capability;
