-- Completion reservation freezes one Interview snapshot before Artifact work
-- starts. The final composite bindings prove that the released Artifacts are
-- exactly those persisted by the Report and Learning Path rows.
ALTER TABLE learning.interview_report
    ADD CONSTRAINT uq_learning_interview_report_completion_binding
        UNIQUE (id, workspace_id, session_id, artifact_id, artifact_revision_id, artifact_version);

ALTER TABLE learning.interview_learning_path
    ADD CONSTRAINT uq_learning_interview_path_completion_binding
        UNIQUE (id, workspace_id, session_id, artifact_id, artifact_revision_id, artifact_version);

CREATE TABLE learning.interview_completion_reservation (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    session_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    manual_end boolean NOT NULL,
    snapshot_version bigint NOT NULL CHECK (snapshot_version > 0),
    artifact_digest text CHECK (artifact_digest IS NULL OR artifact_digest ~ '^[0-9a-f]{64}$'),
    report_id uuid,
    report_artifact_id uuid,
    report_artifact_revision_id uuid,
    report_artifact_version bigint,
    path_id uuid,
    path_artifact_id uuid,
    path_artifact_revision_id uuid,
    path_artifact_version bigint,
    status text NOT NULL CHECK (status IN ('PENDING', 'COMPLETED', 'ABANDONED')),
    created_at timestamptz NOT NULL,
    prepared_at timestamptz,
    completed_at timestamptz,
    abandoned_at timestamptz,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, session_id),
    CONSTRAINT uq_learning_interview_completion_key
        UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT fk_learning_interview_completion_session
        FOREIGN KEY (session_id, workspace_id)
        REFERENCES learning.interview_session(session_id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_completion_report
        FOREIGN KEY (
            report_id, workspace_id, session_id,
            report_artifact_id, report_artifact_revision_id, report_artifact_version
        ) REFERENCES learning.interview_report(
            id, workspace_id, session_id,
            artifact_id, artifact_revision_id, artifact_version
        ) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_interview_completion_path
        FOREIGN KEY (
            path_id, workspace_id, session_id,
            path_artifact_id, path_artifact_revision_id, path_artifact_version
        ) REFERENCES learning.interview_learning_path(
            id, workspace_id, session_id,
            artifact_id, artifact_revision_id, artifact_version
        ) ON DELETE RESTRICT,
    CONSTRAINT learning_interview_completion_key_check
        CHECK (btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128),
    CONSTRAINT learning_interview_completion_digest_time_check
        CHECK (
            (artifact_digest IS NULL AND prepared_at IS NULL)
            OR (artifact_digest IS NOT NULL AND prepared_at IS NOT NULL)
        ),
    CONSTRAINT learning_interview_completion_binding_shape_check
        CHECK (
            (report_id IS NULL
                AND report_artifact_id IS NULL
                AND report_artifact_revision_id IS NULL
                AND report_artifact_version IS NULL
                AND path_id IS NULL
                AND path_artifact_id IS NULL
                AND path_artifact_revision_id IS NULL
                AND path_artifact_version IS NULL)
            OR
            (report_id IS NOT NULL
                AND report_artifact_id IS NOT NULL
                AND report_artifact_revision_id IS NOT NULL
                AND report_artifact_version > 0
                AND path_id IS NOT NULL
                AND path_artifact_id IS NOT NULL
                AND path_artifact_revision_id IS NOT NULL
                AND path_artifact_version > 0
                AND report_artifact_id <> path_artifact_id)
        ),
    CONSTRAINT learning_interview_completion_status_shape_check
        CHECK (
            (status = 'PENDING'
                AND completed_at IS NULL
                AND abandoned_at IS NULL
                AND report_id IS NULL)
            OR
            (status = 'ABANDONED'
                AND completed_at IS NULL
                AND abandoned_at IS NOT NULL
                AND report_id IS NULL)
            OR
            (status = 'COMPLETED'
                AND artifact_digest IS NOT NULL
                AND completed_at IS NOT NULL
                AND abandoned_at IS NULL
                AND report_id IS NOT NULL)
        ),
    CONSTRAINT learning_interview_completion_time_order_check
        CHECK (
            updated_at >= created_at
            AND (prepared_at IS NULL OR (prepared_at >= created_at AND updated_at >= prepared_at))
            AND (completed_at IS NULL OR (completed_at >= created_at AND updated_at >= completed_at))
            AND (abandoned_at IS NULL OR (abandoned_at >= created_at AND updated_at >= abandoned_at))
        )
);

CREATE INDEX idx_learning_interview_completion_pending_maintenance
    ON learning.interview_completion_reservation (updated_at, workspace_id, session_id)
    WHERE status = 'PENDING';

ALTER TABLE learning.artifact_visibility_hold
    ADD COLUMN attempt_digest text,
    ADD COLUMN disposition text NOT NULL DEFAULT 'ACTIVE',
    ADD CONSTRAINT learning_artifact_visibility_hold_digest_check
        CHECK (attempt_digest IS NULL OR attempt_digest ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT learning_artifact_visibility_hold_disposition_check
        CHECK (disposition IN ('ACTIVE', 'ORPHANED'));

-- Every pre-existing hold predates reservation ownership and is therefore an
-- unfinished legacy attempt. Recover a digest only when the PLAN receipt has
-- the exact Interview stage-key shape and matches the held Artifact role.
UPDATE learning.artifact_visibility_hold AS hold
   SET attempt_digest = CASE
           WHEN command.idempotency_key =
                'iv1:' || hold.owner_id::text || ':' || artifact.artifact_type || ':' ||
                split_part(command.idempotency_key, ':', 4) || ':p'
            AND split_part(command.idempotency_key, ':', 4) ~ '^[0-9a-f]{64}$'
            AND (
                (hold.owner_role = 'REPORT' AND artifact.artifact_type = 'INTERVIEW_DOC')
                OR (hold.owner_role = 'PATH' AND artifact.artifact_type = 'LEARNING_PATH')
            )
           THEN split_part(command.idempotency_key, ':', 4)
           ELSE NULL
       END,
       disposition = 'ORPHANED'
  FROM learning.artifact AS artifact
  LEFT JOIN learning.artifact_command AS command
    ON command.workspace_id = artifact.workspace_id
   AND command.artifact_id = artifact.id
   AND command.command_type = 'PLAN'
 WHERE artifact.workspace_id = hold.workspace_id
   AND artifact.id = hold.artifact_id;

-- A hold without a matching Artifact row is already prevented by the FK, but
-- keep the migration result explicit if a future repair bypassed it.
UPDATE learning.artifact_visibility_hold
   SET disposition = 'ORPHANED'
 WHERE disposition <> 'ORPHANED';

ALTER TABLE learning.artifact_visibility_hold
    ADD CONSTRAINT learning_artifact_visibility_hold_active_digest_check
        CHECK (disposition <> 'ACTIVE' OR attempt_digest IS NOT NULL);

-- ACTIVE holds are children of the exact prepared Completion attempt and PLAN
-- receipt. Lock Session before reservation to match Begin/Prepare/Complete;
-- the hold INSERT FK can then acquire its Session key lock without reversing
-- the application lock order. Reservation locking still serializes Artifact
-- creation with timeout maintenance.
CREATE OR REPLACE FUNCTION learning.guard_interview_artifact_visibility_hold()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    reservation_status text;
    reservation_digest text;
    held_artifact_type text;
    plan_binding_exists boolean;
BEGIN
    -- Maintenance only moves a hold to ORPHANED and returns before this lock,
    -- so its reservation -> hold path never waits for Session.
    IF NEW.disposition <> 'ACTIVE' THEN
        RETURN NEW;
    END IF;

    PERFORM 1
      FROM learning.interview_session AS session
     WHERE session.workspace_id = NEW.workspace_id
       AND session.session_id = NEW.owner_id
     FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'active interview artifact hold must bind an existing interview session'
            USING ERRCODE = '23514';
    END IF;

    SELECT reservation.status,
           reservation.artifact_digest,
           artifact.artifact_type,
           EXISTS (
               SELECT 1
                 FROM learning.artifact_command AS command
                WHERE command.workspace_id = NEW.workspace_id
                  AND command.artifact_id = NEW.artifact_id
                  AND command.command_type = 'PLAN'
                  AND command.idempotency_key =
                      'iv1:' || NEW.owner_id::text || ':' || artifact.artifact_type || ':' ||
                      reservation.artifact_digest || ':p'
           )
      INTO reservation_status, reservation_digest, held_artifact_type, plan_binding_exists
      FROM learning.interview_completion_reservation AS reservation
      JOIN learning.artifact AS artifact
        ON artifact.workspace_id = NEW.workspace_id
       AND artifact.id = NEW.artifact_id
     WHERE reservation.workspace_id = NEW.workspace_id
       AND reservation.session_id = NEW.owner_id
     FOR UPDATE OF reservation;

    IF NEW.owner_type IS DISTINCT FROM 'INTERVIEW_COMPLETE'
       OR NOT FOUND
       OR reservation_status IS DISTINCT FROM 'PENDING'
       OR reservation_digest IS NULL
       OR (NEW.attempt_digest IS NOT NULL AND NEW.attempt_digest IS DISTINCT FROM reservation_digest)
       OR plan_binding_exists IS DISTINCT FROM true
       OR NOT (
           (NEW.owner_role = 'REPORT' AND held_artifact_type = 'INTERVIEW_DOC')
           OR (NEW.owner_role = 'PATH' AND held_artifact_type = 'LEARNING_PATH')
       ) THEN
        RAISE EXCEPTION 'active interview artifact hold must bind the pending completion digest'
            USING ERRCODE = '23514';
    END IF;

    NEW.attempt_digest := reservation_digest;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS trg_learning_interview_artifact_visibility_hold_guard
    ON learning.artifact_visibility_hold;
CREATE TRIGGER trg_learning_interview_artifact_visibility_hold_guard
    BEFORE INSERT OR UPDATE OF workspace_id, artifact_id, owner_type, owner_id, owner_role, attempt_digest, disposition
    ON learning.artifact_visibility_hold
    FOR EACH ROW EXECUTE FUNCTION learning.guard_interview_artifact_visibility_hold();

ALTER TABLE learning.artifact_visibility_hold
    DROP CONSTRAINT uq_learning_artifact_visibility_hold_owner_role;

CREATE UNIQUE INDEX uq_learning_artifact_visibility_hold_active_owner_role
    ON learning.artifact_visibility_hold (workspace_id, owner_type, owner_id, owner_role)
    WHERE disposition = 'ACTIVE';

CREATE INDEX idx_learning_artifact_visibility_hold_orphan_recovery
    ON learning.artifact_visibility_hold (workspace_id, owner_type, owner_id, attempt_digest, owner_role)
    WHERE disposition = 'ORPHANED';
