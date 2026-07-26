-- +goose Up

-- An external Export or Publish must own the current Artifact transition before
-- it touches the filesystem or Change Control. The row is a short durable Saga
-- reservation: it is deleted only by the same transaction that writes the
-- terminal Artifact receipt and side-effect binding.
CREATE TABLE learning.artifact_external_transition_reservation (
    workspace_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    current_revision_id uuid NOT NULL,
    expected_version bigint NOT NULL CHECK (expected_version > 0),
    command_type text NOT NULL CHECK (command_type IN ('EXPORT_MARKDOWN', 'PUBLISH')),
    idempotency_key text NOT NULL,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, artifact_id),
    CONSTRAINT uq_learning_artifact_external_transition_reservation_key
        UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT learning_artifact_external_transition_reservation_key_check
        CHECK (idempotency_key = btrim(idempotency_key) AND btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128),
    CONSTRAINT fk_learning_artifact_external_transition_reservation_artifact
        FOREIGN KEY (artifact_id, workspace_id)
        REFERENCES learning.artifact(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_external_transition_reservation_revision
        FOREIGN KEY (current_revision_id, workspace_id, artifact_id)
        REFERENCES learning.artifact_revision(id, workspace_id, artifact_id) ON DELETE RESTRICT
);

-- +goose StatementBegin
CREATE FUNCTION learning.guard_artifact_external_transition_reservation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'artifact external transition reservation is immutable' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'DELETE' AND NOT EXISTS (
        SELECT 1
          FROM learning.artifact_command c
          JOIN learning.artifact a ON a.id = OLD.artifact_id AND a.workspace_id = OLD.workspace_id
         WHERE c.workspace_id = OLD.workspace_id
           AND c.idempotency_key = OLD.idempotency_key
           AND c.request_hash = OLD.request_hash
           AND c.command_type = OLD.command_type
           AND c.artifact_id = OLD.artifact_id
           AND c.artifact_version = OLD.expected_version + 1
           AND a.version = OLD.expected_version + 1
           AND (
                (OLD.command_type = 'EXPORT_MARKDOWN' AND a.status = 'EXPORTED' AND EXISTS (
                    SELECT 1 FROM learning.artifact_export e
                     WHERE e.workspace_id = OLD.workspace_id AND e.artifact_id = OLD.artifact_id
                       AND e.revision_id = OLD.current_revision_id AND e.artifact_version = OLD.expected_version + 1
                )) OR
                (OLD.command_type = 'PUBLISH' AND a.status = 'PUBLISH_PROPOSED' AND EXISTS (
                    SELECT 1 FROM learning.artifact_publication p
                     WHERE p.workspace_id = OLD.workspace_id AND p.artifact_id = OLD.artifact_id
                       AND p.revision_id = OLD.current_revision_id AND p.artifact_version = OLD.expected_version + 1
                ))
           )
    ) THEN
        RAISE EXCEPTION 'artifact external transition reservation cannot be deleted before its terminal receipt and side fact' USING ERRCODE = '55000';
    END IF;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_learning_artifact_external_transition_reservation_guard
BEFORE UPDATE OR DELETE ON learning.artifact_external_transition_reservation
FOR EACH ROW EXECUTE FUNCTION learning.guard_artifact_external_transition_reservation_mutation();

-- +goose Down

-- Do not partially downgrade away the cross-store consistency guard while any
-- Artifact business state or an unfinished reservation exists.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.artifact LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.artifact_revision LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.artifact_command LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.artifact_export LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.artifact_publication LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.artifact_section_generation LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.artifact_external_transition_reservation LIMIT 1) THEN
        RAISE EXCEPTION 'cannot remove artifact external transition reservation with Artifact business data' USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_artifact_external_transition_reservation_guard
    ON learning.artifact_external_transition_reservation;
DROP FUNCTION IF EXISTS learning.guard_artifact_external_transition_reservation_mutation();
DROP TABLE IF EXISTS learning.artifact_external_transition_reservation;
