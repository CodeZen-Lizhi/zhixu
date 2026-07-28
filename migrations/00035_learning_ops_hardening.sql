-- +goose Up

-- Review commands without a durable receipt can create duplicate cards or
-- silently bind one idempotency key to two requests.  Keep the receipt in the
-- learning schema so replay is scoped by Workspace and request hash.
CREATE TABLE IF NOT EXISTS learning.review_command (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (command_type IN ('CREATE_DECK', 'CREATE_CARD', 'DECIDE_CARD', 'CHANGE_DECK_SCHEDULE')),
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
    response jsonb NOT NULL CHECK (jsonb_typeof(response) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key)
);

ALTER TABLE learning.review_session
    ADD COLUMN IF NOT EXISTS request_hash text;
ALTER TABLE learning.review_answer
    ADD COLUMN IF NOT EXISTS request_hash text;
ALTER TABLE learning.review_answer
    ADD COLUMN IF NOT EXISTS rating smallint;
ALTER TABLE learning.review_answer
    ADD COLUMN IF NOT EXISTS schedule_snapshot jsonb;

-- Existing installations created by 00034 have no request hash/rating.  A
-- deterministic legacy marker reserves the historical idempotency key.  The
-- unknown rating and schedule snapshot stay NULL: inventing either would turn
-- an unreplayable historical answer into a false learning fact.
UPDATE learning.review_session
SET request_hash = md5(id::text) || md5(id::text || ':legacy-session')
WHERE request_hash IS NULL;
UPDATE learning.review_answer
SET request_hash = md5(id::text) || md5(id::text || ':legacy-answer')
WHERE request_hash IS NULL;

ALTER TABLE learning.review_session
    ALTER COLUMN request_hash SET NOT NULL;
ALTER TABLE learning.review_session
    ADD CONSTRAINT learning_review_session_request_hash_check
    CHECK (request_hash ~ '^[0-9a-f]{64}$');
ALTER TABLE learning.review_answer
    ALTER COLUMN request_hash SET NOT NULL;
ALTER TABLE learning.review_answer
    ADD CONSTRAINT learning_review_answer_request_hash_check
    CHECK (request_hash ~ '^[0-9a-f]{64}$');
ALTER TABLE learning.review_answer
    ADD CONSTRAINT learning_review_answer_rating_check
    CHECK (rating IS NOT NULL AND rating BETWEEN 1 AND 4) NOT VALID;
ALTER TABLE learning.review_answer
    ADD CONSTRAINT learning_review_answer_schedule_snapshot_check
    CHECK (schedule_snapshot IS NOT NULL AND jsonb_typeof(schedule_snapshot) = 'object') NOT VALID;
ALTER TABLE learning.review_session
    ADD CONSTRAINT learning_review_session_idempotency_key_size_check
    CHECK (octet_length(idempotency_key) <= 128);
ALTER TABLE learning.review_answer
    ADD CONSTRAINT learning_review_answer_question_ref_size_check
    CHECK (octet_length(question_ref) <= 512);
ALTER TABLE learning.review_answer
    ADD CONSTRAINT learning_review_answer_idempotency_key_size_check
    CHECK (octet_length(idempotency_key) <= 128);
ALTER TABLE learning.review_answer
    ADD CONSTRAINT learning_review_answer_user_answer_size_check
    CHECK (octet_length(user_answer) <= 65536);
ALTER TABLE learning.review_answer
    ADD CONSTRAINT learning_review_answer_feedback_size_check
    CHECK (octet_length(feedback::text) <= 32768);

-- 00034 allowed legacy cards without a Claim or non-empty payloads.  NOT VALID
-- keeps those historical rows readable while enforcing the v1 contract for
-- every new INSERT/UPDATE.  A later backfill may validate the constraints.
ALTER TABLE learning.review_card
    ADD CONSTRAINT learning_review_card_claim_required
    CHECK (claim_id IS NOT NULL) NOT VALID;
ALTER TABLE learning.review_card
    ADD CONSTRAINT learning_review_card_answer_points_nonempty
    CHECK (jsonb_array_length(answer_points) > 0) NOT VALID;
ALTER TABLE learning.review_card
    ADD CONSTRAINT learning_review_card_evidence_nonempty
    CHECK (jsonb_array_length(evidence) > 0) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_learning_review_command_aggregate
    ON learning.review_command (workspace_id, aggregate_id, created_at DESC);

-- +goose Down

-- Do not silently discard replay/answer history while rolling back the
-- hardening layer.  An explicit archival/backup step must precede the Down.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.review_command LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.review_answer LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.review_session LIMIT 1) THEN
        RAISE EXCEPTION 'learning review history is non-empty; archive before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP INDEX IF EXISTS learning.idx_learning_review_command_aggregate;
DROP TABLE IF EXISTS learning.review_command;
ALTER TABLE IF EXISTS learning.review_card
    DROP CONSTRAINT IF EXISTS learning_review_card_evidence_nonempty;
ALTER TABLE IF EXISTS learning.review_card
    DROP CONSTRAINT IF EXISTS learning_review_card_answer_points_nonempty;
ALTER TABLE IF EXISTS learning.review_card
    DROP CONSTRAINT IF EXISTS learning_review_card_claim_required;
ALTER TABLE IF EXISTS learning.review_answer
    DROP CONSTRAINT IF EXISTS learning_review_answer_feedback_size_check;
ALTER TABLE IF EXISTS learning.review_answer
    DROP CONSTRAINT IF EXISTS learning_review_answer_user_answer_size_check;
ALTER TABLE IF EXISTS learning.review_answer
    DROP CONSTRAINT IF EXISTS learning_review_answer_idempotency_key_size_check;
ALTER TABLE IF EXISTS learning.review_answer
    DROP CONSTRAINT IF EXISTS learning_review_answer_question_ref_size_check;
ALTER TABLE IF EXISTS learning.review_session
    DROP CONSTRAINT IF EXISTS learning_review_session_idempotency_key_size_check;
ALTER TABLE IF EXISTS learning.review_answer
    DROP CONSTRAINT IF EXISTS learning_review_answer_schedule_snapshot_check;
ALTER TABLE IF EXISTS learning.review_answer
    DROP CONSTRAINT IF EXISTS learning_review_answer_rating_check;
ALTER TABLE IF EXISTS learning.review_answer
    DROP CONSTRAINT IF EXISTS learning_review_answer_request_hash_check;
ALTER TABLE IF EXISTS learning.review_session
    DROP CONSTRAINT IF EXISTS learning_review_session_request_hash_check;
ALTER TABLE IF EXISTS learning.review_answer DROP COLUMN IF EXISTS schedule_snapshot;
ALTER TABLE IF EXISTS learning.review_answer DROP COLUMN IF EXISTS rating;
ALTER TABLE IF EXISTS learning.review_answer DROP COLUMN IF EXISTS request_hash;
ALTER TABLE IF EXISTS learning.review_session DROP COLUMN IF EXISTS request_hash;
