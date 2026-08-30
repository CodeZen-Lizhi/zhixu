LOCK TABLE ops.model_settings_state, ops.model_settings_runtime,
    ops.runtime_mutation_gate, workflow.node_attempt IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM ops.model_settings_state
        WHERE phase IN ('validating','draining','applying','verifying')
    ) THEN
        RAISE EXCEPTION 'legacy model settings rollout must be recovered before hot activation upgrade'
            USING ERRCODE='55000';
    END IF;
    IF EXISTS (
        SELECT 1 FROM ops.model_settings_runtime
        WHERE rollout_id IS NOT NULL OR phase NOT IN ('active','unavailable')
    ) THEN
        RAISE EXCEPTION 'legacy model settings candidate runtime must be recovered before hot activation upgrade'
            USING ERRCODE='55000';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS model_settings_state_guard ON ops.model_settings_state;
DROP TRIGGER IF EXISTS model_settings_runtime_guard ON ops.model_settings_runtime;

ALTER TABLE ops.model_settings_state
    DROP CONSTRAINT ops_model_settings_state_phase,
    DROP CONSTRAINT ops_model_settings_state_rollout_shape,
    ADD CONSTRAINT ops_model_settings_state_phase
        CHECK (phase IN ('idle','preparing','arming','activating','failed')),
    ADD CONSTRAINT ops_model_settings_state_rollout_shape CHECK ((
        (phase='idle' AND rollout_id IS NULL AND target_revision IS NULL
            AND previous_active_revision IS NULL AND lease_expires_at IS NULL AND last_error_code IS NULL)
        OR (phase IN ('preparing','arming','activating') AND rollout_id IS NOT NULL
            AND target_revision IS NOT NULL AND previous_active_revision IS NOT NULL
            AND lease_expires_at IS NOT NULL AND last_error_code IS NULL)
        OR (phase='failed' AND rollout_id IS NOT NULL AND target_revision IS NOT NULL
            AND previous_active_revision IS NOT NULL AND lease_expires_at IS NULL
            AND last_error_code ~ '^[A-Z0-9_]{1,128}$')
    ) IS TRUE);

CREATE OR REPLACE FUNCTION ops.enforce_model_settings_state()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    activation_now timestamptz;
    ready_role text;
    ready_roles integer := 0;
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
    IF OLD.phase IN ('preparing','arming','activating')
       AND NEW.desired_revision<>OLD.desired_revision THEN
        RAISE EXCEPTION 'model settings desired revision is fixed during activation' USING ERRCODE='55000';
    END IF;
    IF OLD.active_revision<>NEW.active_revision
       AND NOT (OLD.phase='arming' AND NEW.phase='activating'
           AND NEW.active_revision=OLD.target_revision) THEN
        RAISE EXCEPTION 'model settings active revision transition is invalid' USING ERRCODE='55000';
    END IF;
    IF OLD.phase IN ('preparing','arming') AND NEW.phase='failed'
       AND EXISTS (
            SELECT 1 FROM ops.model_settings_rollout_participant
            WHERE rollout_id=OLD.rollout_id AND phase IN ('preparing','prepared','armed')
       ) THEN
        RAISE EXCEPTION 'model settings participants must be aborted before activation failure' USING ERRCODE='55000';
    END IF;
    IF NOT (
        (OLD.phase=NEW.phase AND OLD.phase IN ('idle','failed'))
        OR (OLD.phase IN ('idle','failed') AND NEW.phase='preparing'
            AND NEW.target_revision=NEW.desired_revision
            AND NEW.previous_active_revision=OLD.active_revision
            AND NEW.active_revision=OLD.active_revision)
        OR (OLD.phase=NEW.phase AND OLD.phase IN ('preparing','arming','activating')
            AND NEW.rollout_id=OLD.rollout_id AND NEW.target_revision=OLD.target_revision
            AND NEW.previous_active_revision=OLD.previous_active_revision
            AND NEW.active_revision=OLD.active_revision)
        OR (OLD.phase='preparing' AND NEW.phase='arming'
            AND NEW.active_revision=OLD.previous_active_revision)
        OR (OLD.phase IN ('preparing','arming') AND NEW.phase='failed'
            AND NEW.rollout_id=OLD.rollout_id AND NEW.target_revision=OLD.target_revision
            AND NEW.previous_active_revision=OLD.previous_active_revision
            AND NEW.active_revision=OLD.previous_active_revision)
        OR (OLD.phase='arming' AND NEW.phase='activating'
            AND NEW.rollout_id=OLD.rollout_id AND NEW.target_revision=OLD.target_revision
            AND NEW.previous_active_revision=OLD.previous_active_revision
            AND NEW.active_revision=OLD.target_revision)
        OR (OLD.phase='activating' AND NEW.phase='idle'
            AND NEW.active_revision=OLD.target_revision)
    ) THEN
        RAISE EXCEPTION 'model settings activation transition is invalid' USING ERRCODE='55000';
    END IF;
    IF OLD.phase='failed' AND NEW.phase='failed'
       AND ROW(NEW.rollout_id,NEW.target_revision,NEW.previous_active_revision,NEW.last_error_code)
           IS DISTINCT FROM ROW(OLD.rollout_id,OLD.target_revision,OLD.previous_active_revision,OLD.last_error_code) THEN
        RAISE EXCEPTION 'failed model settings activation binding is immutable' USING ERRCODE='55000';
    END IF;
    IF OLD.phase IN ('preparing','arming','activating')
       AND NEW.phase IN ('preparing','arming','activating')
       AND ROW(NEW.rollout_id,NEW.target_revision,NEW.previous_active_revision)
           IS DISTINCT FROM ROW(OLD.rollout_id,OLD.target_revision,OLD.previous_active_revision) THEN
        RAISE EXCEPTION 'model settings activation binding is immutable' USING ERRCODE='55000';
    END IF;
    IF OLD.phase='arming' AND NEW.phase='activating' THEN
        -- Lock in the same order as CommitActivation: singleton state, serving
        -- runtimes by role, then the matching participant rows by role.
        activation_now := clock_timestamp();
        FOR ready_role IN
            SELECT runtime.role
              FROM ops.model_settings_runtime AS runtime
              JOIN ops.model_settings_rollout_participant AS participant
                ON participant.rollout_id=NEW.rollout_id AND participant.role=runtime.role
             WHERE runtime.role IN ('api','worker')
               AND runtime.instance_id=participant.instance_id
               AND runtime.rollout_id IS NULL AND runtime.phase='active'
               AND runtime.applied_revision=NEW.previous_active_revision
               AND participant.target_revision=NEW.target_revision AND participant.phase='armed'
               AND runtime.heartbeat_at BETWEEN activation_now-interval '20 seconds' AND activation_now
               AND participant.heartbeat_at BETWEEN activation_now-interval '20 seconds' AND activation_now
             ORDER BY runtime.role
             FOR SHARE OF runtime, participant
        LOOP
            ready_roles := ready_roles+1;
        END LOOP;
        IF ready_roles<>2 THEN
            RAISE EXCEPTION 'both model settings activation roles must be fresh and armed' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER model_settings_state_guard
    BEFORE INSERT OR UPDATE OR DELETE ON ops.model_settings_state
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_model_settings_state();

CREATE TABLE ops.model_settings_rollout_participant (
    rollout_id uuid NOT NULL,
    role text NOT NULL,
    instance_id uuid NOT NULL,
    target_revision bigint NOT NULL,
    phase text NOT NULL,
    heartbeat_at timestamptz NOT NULL,
    last_error_code text,
    last_error_retryable boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1,
    prepared_at timestamptz,
    activated_at timestamptz,
    retired_at timestamptz,
    PRIMARY KEY (rollout_id,role),
    CONSTRAINT ops_model_settings_participant_role CHECK (role IN ('api','worker')),
    CONSTRAINT ops_model_settings_participant_revision CHECK (target_revision>=0),
    CONSTRAINT ops_model_settings_participant_phase CHECK (
        phase IN ('preparing','prepared','armed','activated','failed','aborted','retired')
    ),
    CONSTRAINT ops_model_settings_participant_error CHECK ((
        (phase='failed' AND last_error_code ~ '^[A-Z0-9_]{1,128}$')
        OR (phase<>'failed' AND last_error_code IS NULL AND last_error_retryable=false)
    ) IS TRUE),
    CONSTRAINT ops_model_settings_participant_timestamps CHECK ((
        (phase='preparing' AND prepared_at IS NULL AND activated_at IS NULL AND retired_at IS NULL)
        OR (phase IN ('prepared','armed') AND prepared_at IS NOT NULL AND activated_at IS NULL AND retired_at IS NULL)
        OR (phase='activated' AND prepared_at IS NOT NULL AND activated_at IS NOT NULL AND retired_at IS NULL)
        OR (phase='retired' AND prepared_at IS NOT NULL AND activated_at IS NOT NULL AND retired_at IS NOT NULL)
        OR (phase IN ('failed','aborted') AND activated_at IS NULL AND retired_at IS NULL)
    ) IS TRUE),
    CONSTRAINT ops_model_settings_participant_version CHECK (version>0)
);

CREATE FUNCTION ops.enforce_model_settings_participant()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    state_rollout_id uuid;
    state_target_revision bigint;
    state_phase text;
    runtime_instance_id uuid;
    runtime_applied_revision bigint;
    participant_now timestamptz;
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'model settings participant history cannot be deleted' USING ERRCODE='55000';
    END IF;
    IF NOT ops.model_settings_revision_exists(NEW.target_revision) THEN
        RAISE EXCEPTION 'model settings participant references an unknown revision' USING ERRCODE='23503';
    END IF;
    IF TG_OP='UPDATE' AND (NEW.rollout_id<>OLD.rollout_id OR NEW.role<>OLD.role
       OR NEW.target_revision<>OLD.target_revision OR NEW.heartbeat_at<OLD.heartbeat_at
       OR NEW.version<>OLD.version+1) THEN
        RAISE EXCEPTION 'model settings participant binding or version is invalid' USING ERRCODE='55000';
    END IF;
    SELECT rollout_id,target_revision,phase
      INTO state_rollout_id,state_target_revision,state_phase
      FROM ops.model_settings_state WHERE singleton=true;
    IF NEW.phase NOT IN ('failed','aborted','retired') THEN
        IF state_rollout_id IS DISTINCT FROM NEW.rollout_id
           OR state_target_revision IS DISTINCT FROM NEW.target_revision THEN
            RAISE EXCEPTION 'model settings participant does not match the live activation' USING ERRCODE='23514';
        END IF;
        SELECT instance_id,applied_revision INTO runtime_instance_id,runtime_applied_revision
          FROM ops.model_settings_runtime WHERE role=NEW.role;
        IF runtime_instance_id IS DISTINCT FROM NEW.instance_id THEN
            RAISE EXCEPTION 'model settings participant does not match the serving owner' USING ERRCODE='23514';
        END IF;
        IF NEW.phase='activated' AND runtime_applied_revision IS DISTINCT FROM NEW.target_revision THEN
            RAISE EXCEPTION 'activated model settings participant does not match serving revision' USING ERRCODE='23514';
        END IF;
    END IF;
    IF TG_OP='INSERT' THEN
        IF NOT ((state_phase IN ('preparing','arming') AND NEW.phase='preparing')
            OR (state_phase='activating' AND NEW.phase='activated')) THEN
            RAISE EXCEPTION 'model settings participant initial phase is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.instance_id<>OLD.instance_id THEN
        participant_now := clock_timestamp();
        IF NEW.heartbeat_at>participant_now THEN
            RAISE EXCEPTION 'model settings participant takeover heartbeat is invalid' USING ERRCODE='55000';
        END IF;
        IF NOT OLD.heartbeat_at < participant_now-interval '20 seconds' THEN
            RAISE EXCEPTION 'model settings participant owner is still fresh' USING ERRCODE='55000';
        END IF;
        IF NOT ((state_phase IN ('preparing','arming') AND NEW.phase='preparing')
            OR (state_phase='activating' AND NEW.phase='activated')) THEN
            RAISE EXCEPTION 'model settings participant takeover is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF NOT (
        NEW.phase=OLD.phase
        OR (OLD.phase='preparing' AND NEW.phase IN ('prepared','failed','aborted'))
        OR (OLD.phase='prepared' AND NEW.phase IN ('armed','failed','aborted'))
        OR (OLD.phase='armed' AND NEW.phase IN ('activated','failed','aborted'))
        OR (OLD.phase='activated' AND NEW.phase='retired')
    ) THEN
        RAISE EXCEPTION 'model settings participant transition is invalid' USING ERRCODE='55000';
    END IF;
    IF NEW.phase IN ('failed','aborted') AND state_phase NOT IN ('preparing','arming','failed') THEN
        RAISE EXCEPTION 'model settings participant cannot fail after commit' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER model_settings_participant_guard
    BEFORE INSERT OR UPDATE OR DELETE ON ops.model_settings_rollout_participant
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_model_settings_participant();
CREATE TRIGGER model_settings_participant_reject_truncate
    BEFORE TRUNCATE ON ops.model_settings_rollout_participant
    FOR EACH STATEMENT EXECUTE FUNCTION ops.enforce_model_settings_participant();

CREATE OR REPLACE FUNCTION ops.enforce_model_settings_runtime()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    activation_allowed boolean;
    state_active_revision bigint;
    state_previous_active_revision bigint;
    state_target_revision bigint;
    state_phase text;
    runtime_now timestamptz;
BEGIN
    IF TG_OP IN ('DELETE','TRUNCATE') THEN
        RAISE EXCEPTION 'model settings runtime ownership cannot be deleted' USING ERRCODE='55000';
    END IF;
    IF NOT ops.model_settings_revision_exists(NEW.applied_revision) THEN
        RAISE EXCEPTION 'model settings runtime references an unknown revision' USING ERRCODE='23503';
    END IF;
    IF TG_OP='INSERT' OR NEW.instance_id<>OLD.instance_id THEN
        runtime_now := clock_timestamp();
        IF NEW.phase NOT IN ('active','unavailable') OR NEW.rollout_id IS NOT NULL
           OR NEW.heartbeat_at>runtime_now OR NEW.applied_at>runtime_now THEN
            RAISE EXCEPTION 'model settings runtime serving registration is invalid' USING ERRCODE='55000';
        END IF;
        SELECT active_revision,previous_active_revision,target_revision,phase
          INTO state_active_revision,state_previous_active_revision,state_target_revision,state_phase
          FROM ops.model_settings_state WHERE singleton=true;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'model settings singleton state is missing' USING ERRCODE='23503';
        END IF;
        IF state_phase IN ('idle','failed') THEN
            IF NEW.applied_revision<>state_active_revision THEN
                RAISE EXCEPTION 'model settings runtime revision is not active' USING ERRCODE='55000';
            END IF;
        ELSIF state_phase IN ('preparing','arming') THEN
            IF NEW.applied_revision<>state_active_revision
               OR state_previous_active_revision IS DISTINCT FROM state_active_revision THEN
                RAISE EXCEPTION 'model settings runtime revision is not the pre-commit active revision' USING ERRCODE='55000';
            END IF;
        ELSIF state_phase='activating' THEN
            IF NEW.applied_revision IS DISTINCT FROM state_target_revision THEN
                RAISE EXCEPTION 'model settings runtime revision is not the committed target' USING ERRCODE='55000';
            END IF;
        ELSE
            RAISE EXCEPTION 'model settings runtime registration phase is invalid' USING ERRCODE='55000';
        END IF;
        IF TG_OP='UPDATE' THEN
            IF NOT OLD.heartbeat_at < runtime_now-interval '20 seconds' THEN
                RAISE EXCEPTION 'model settings runtime owner is still fresh' USING ERRCODE='55000';
            END IF;
            IF NEW.applied_at<OLD.applied_at THEN
                RAISE EXCEPTION 'model settings runtime applied time cannot move backwards' USING ERRCODE='55000';
            END IF;
            -- A replacement may advance only as post-commit recovery from the
            -- persisted previous serving revision to the committed target.
            IF NEW.applied_revision<>OLD.applied_revision
               AND NOT (state_phase='activating'
                   AND OLD.applied_revision IS NOT DISTINCT FROM state_previous_active_revision
                   AND NEW.applied_revision IS NOT DISTINCT FROM state_target_revision) THEN
                RAISE EXCEPTION 'model settings runtime takeover cannot change revision' USING ERRCODE='55000';
            END IF;
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.heartbeat_at<OLD.heartbeat_at THEN
        RAISE EXCEPTION 'model settings runtime heartbeat cannot move backwards' USING ERRCODE='55000';
    END IF;
    IF NEW.applied_revision<>OLD.applied_revision THEN
        SELECT EXISTS (
            SELECT 1
              FROM ops.model_settings_state AS state
              JOIN ops.model_settings_rollout_participant AS participant
                ON participant.rollout_id=state.rollout_id AND participant.role=NEW.role
             WHERE state.singleton=true AND state.phase='activating'
               AND state.target_revision=NEW.applied_revision
               AND participant.instance_id=NEW.instance_id
               AND participant.target_revision=NEW.applied_revision
               AND participant.phase='armed'
        ) INTO activation_allowed;
        IF NOT activation_allowed OR NEW.phase<>'active' OR NEW.rollout_id IS NOT NULL
           OR NEW.applied_at<OLD.applied_at THEN
            RAISE EXCEPTION 'model settings runtime activation binding is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.applied_at<>OLD.applied_at THEN
        RAISE EXCEPTION 'model settings runtime applied time is immutable without activation' USING ERRCODE='55000';
    END IF;
    IF OLD.phase='unavailable' AND NEW.phase='active'
       AND OLD.rollout_id IS NULL AND NEW.rollout_id IS NULL THEN
        SELECT EXISTS (
            SELECT 1 FROM ops.model_settings_state AS state
             WHERE state.singleton=true
               AND ((state.phase IN ('idle','failed') AND state.active_revision=NEW.applied_revision)
                    OR (state.phase IN ('preparing','arming')
                        AND state.active_revision=NEW.applied_revision
                        AND state.previous_active_revision=NEW.applied_revision)
                    OR (state.phase='activating' AND state.active_revision=NEW.applied_revision
                        AND state.target_revision=NEW.applied_revision))
        ) INTO activation_allowed;
        IF NOT activation_allowed THEN
            RAISE EXCEPTION 'model settings runtime availability restore is invalid' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF NOT (NEW.phase=OLD.phase AND NEW.rollout_id IS NOT DISTINCT FROM OLD.rollout_id)
       AND NOT (NEW.phase='unavailable' AND NEW.rollout_id IS NULL) THEN
        RAISE EXCEPTION 'model settings runtime phase transition is invalid' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER model_settings_runtime_guard
    BEFORE INSERT OR UPDATE OR DELETE ON ops.model_settings_runtime
    FOR EACH ROW EXECUTE FUNCTION ops.enforce_model_settings_runtime();

CREATE OR REPLACE FUNCTION ops.sync_model_settings_mutation_gate()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    changed_rows integer;
BEGIN
    IF NEW.phase IN ('preparing','arming','activating') THEN
        UPDATE ops.runtime_mutation_gate
        SET owner_kind='model_settings',owner_id=NEW.rollout_id,
            lease_expires_at=NEW.lease_expires_at,version=version+1,updated_at=NEW.updated_at
        WHERE singleton=true
          AND (owner_id IS NULL OR (owner_kind='model_settings' AND owner_id=NEW.rollout_id));
        GET DIAGNOSTICS changed_rows=ROW_COUNT;
        IF changed_rows<>1 THEN
            RAISE EXCEPTION 'runtime mutation gate is held by another operation' USING ERRCODE='55P03';
        END IF;
    ELSIF OLD.phase IN ('preparing','arming','activating') AND NEW.phase IN ('idle','failed') THEN
        UPDATE ops.runtime_mutation_gate
        SET owner_kind=NULL,owner_id=NULL,lease_expires_at=NULL,
            version=version+1,updated_at=NEW.updated_at
        WHERE singleton=true AND owner_kind='model_settings' AND owner_id=OLD.rollout_id;
        GET DIAGNOSTICS changed_rows=ROW_COUNT;
        IF changed_rows<>1 THEN
            RAISE EXCEPTION 'model settings mutation gate ownership is corrupt' USING ERRCODE='55000';
        END IF;
    END IF;
    RETURN NEW;
END
$$;

UPDATE ops.runtime_mutation_gate AS gate
SET owner_kind=NULL,owner_id=NULL,lease_expires_at=NULL,
    version=gate.version+1,updated_at=clock_timestamp()
WHERE singleton=true AND owner_kind='model_settings';

CREATE OR REPLACE FUNCTION workflow.enforce_node_attempt_model_runtime_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    active_revision bigint;
    activation_phase text;
    worker_instance_id uuid;
    worker_revision bigint;
    worker_rollout_id uuid;
    worker_phase text;
BEGIN
    IF TG_OP='UPDATE' THEN
        IF NEW.model_settings_revision IS DISTINCT FROM OLD.model_settings_revision
           OR NEW.model_runtime_instance_id IS DISTINCT FROM OLD.model_runtime_instance_id THEN
            RAISE EXCEPTION 'workflow node attempt model runtime binding is immutable' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.model_settings_revision IS NULL THEN
        RETURN NEW;
    END IF;
    IF NOT ops.model_settings_revision_exists(NEW.model_settings_revision) THEN
        RAISE EXCEPTION 'workflow node attempt references an unknown model settings revision' USING ERRCODE='23503';
    END IF;
    SELECT state.active_revision,state.phase INTO active_revision,activation_phase
      FROM ops.model_settings_state AS state WHERE singleton=true FOR SHARE;
    IF activation_phase NOT IN ('idle','failed','preparing') THEN
        RAISE EXCEPTION 'workflow node attempt claim is fenced during model activation' USING ERRCODE='55P03';
    END IF;
    SELECT runtime.instance_id,runtime.applied_revision,runtime.rollout_id,runtime.phase
      INTO worker_instance_id,worker_revision,worker_rollout_id,worker_phase
      FROM ops.model_settings_runtime AS runtime WHERE runtime.role='worker' FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'workflow node attempt requires an active worker model runtime' USING ERRCODE='23503';
    END IF;
    IF NEW.model_runtime_instance_id IS DISTINCT FROM worker_instance_id
       OR NEW.model_settings_revision IS DISTINCT FROM active_revision
       OR worker_revision IS DISTINCT FROM active_revision
       OR worker_rollout_id IS NOT NULL OR worker_phase<>'active' THEN
        RAISE EXCEPTION 'workflow node attempt model runtime binding does not match the serving worker runtime' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END
$$;
