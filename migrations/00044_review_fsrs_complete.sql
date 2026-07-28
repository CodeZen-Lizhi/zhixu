-- +goose Up

-- Card status is the sole lifecycle fact. A schedule exists only while the
-- corresponding card is APPROVED, and every invalidated card has a durable,
-- observable reason. Existing historical rows are mapped explicitly instead
-- of becoming unreadable after the new contract is installed.
ALTER TABLE learning.review_card
    ADD COLUMN IF NOT EXISTS invalidation_reason text;
ALTER TABLE learning.review_card
    ADD COLUMN IF NOT EXISTS invalidated_at timestamptz;

UPDATE learning.review_card
SET invalidation_reason = CASE
        WHEN status = 'INVALIDATED' THEN COALESCE(NULLIF(btrim(invalidation_reason), ''), 'LEGACY_INVALIDATION')
        ELSE NULL
    END,
    invalidated_at = CASE
        WHEN status = 'INVALIDATED' THEN COALESCE(invalidated_at, updated_at)
        ELSE NULL
    END;

ALTER TABLE learning.review_card
    ADD CONSTRAINT learning_review_card_invalidation_metadata_check
    CHECK (
        (status = 'INVALIDATED'
            AND invalidation_reason IS NOT NULL
            AND btrim(invalidation_reason) <> ''
            AND octet_length(invalidation_reason) <= 1024
            AND invalidated_at IS NOT NULL)
        OR
        (status <> 'INVALIDATED'
            AND invalidation_reason IS NULL
            AND invalidated_at IS NULL)
    ) NOT VALID;

-- Legacy approved rows that predate the schedule contract get an explicit
-- initial state. Non-approved rows cannot retain a schedule.
DELETE FROM learning.review_schedule AS schedule
USING learning.review_card AS card
WHERE card.workspace_id = schedule.workspace_id
  AND card.id = schedule.card_id
  AND card.status <> 'APPROVED';

INSERT INTO learning.review_schedule(
    card_id,workspace_id,due_at,interval_days,stability,difficulty,
    last_reviewed_at,scheduler_version,paused,version
)
SELECT card.id,card.workspace_id,CURRENT_TIMESTAMP,0,0,card.difficulty,
       NULL,deck.scheduler_version,false,1
FROM learning.review_card AS card
JOIN learning.review_deck AS deck
  ON deck.workspace_id = card.workspace_id
 AND deck.id = card.deck_id
WHERE card.status = 'APPROVED'
ON CONFLICT (card_id) DO NOTHING;

ALTER TABLE learning.review_command
    DROP CONSTRAINT IF EXISTS review_command_command_type_check;
ALTER TABLE learning.review_command
    ADD CONSTRAINT review_command_command_type_check
    CHECK (command_type IN (
        'CREATE_DECK','CREATE_CARD','EDIT_CARD','DECIDE_CARD',
        'INVALIDATE_CARDS','CHANGE_DECK_SCHEDULE','COMPLETE_SESSION'
    ));

CREATE INDEX IF NOT EXISTS idx_learning_review_card_active_deck
    ON learning.review_card (workspace_id,deck_id,id)
    WHERE status = 'APPROVED';

-- Answers and command receipts are historical facts used for exact replay.
-- Mutating either row would silently rewrite the score/schedule history or the
-- authoritative response associated with an idempotency key.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.reject_review_history_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '55000';
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_answer_reject_update_delete ON learning.review_answer;
CREATE TRIGGER review_answer_reject_update_delete
BEFORE UPDATE OR DELETE ON learning.review_answer
FOR EACH ROW
EXECUTE FUNCTION learning.reject_review_history_mutation();

DROP TRIGGER IF EXISTS review_command_reject_update_delete ON learning.review_command;
CREATE TRIGGER review_command_reject_update_delete
BEFORE UPDATE OR DELETE ON learning.review_command
FOR EACH ROW
EXECUTE FUNCTION learning.reject_review_history_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.sync_review_card_schedule()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status <> 'APPROVED' THEN
        DELETE FROM learning.review_schedule
        WHERE workspace_id = NEW.workspace_id
          AND card_id = NEW.id;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_card_schedule_sync ON learning.review_card;
CREATE TRIGGER review_card_schedule_sync
AFTER INSERT OR UPDATE OF status ON learning.review_card
FOR EACH ROW
EXECUTE FUNCTION learning.sync_review_card_schedule();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.assert_review_card_schedule()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status = 'APPROVED' AND NOT EXISTS (
        SELECT 1 FROM learning.review_schedule
        WHERE workspace_id = NEW.workspace_id AND card_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'approved review card requires one schedule' USING ERRCODE = '23514';
    END IF;
    IF NEW.status <> 'APPROVED' AND EXISTS (
        SELECT 1 FROM learning.review_schedule
        WHERE workspace_id = NEW.workspace_id AND card_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'inactive review card cannot retain a schedule' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_card_schedule_guard ON learning.review_card;
CREATE CONSTRAINT TRIGGER review_card_schedule_guard
AFTER INSERT OR UPDATE OF status ON learning.review_card
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION learning.assert_review_card_schedule();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.assert_review_schedule_card_status()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_workspace uuid;
    target_card uuid;
    card_status text;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target_workspace := OLD.workspace_id;
        target_card := OLD.card_id;
    ELSE
        target_workspace := NEW.workspace_id;
        target_card := NEW.card_id;
    END IF;
    SELECT status INTO card_status
    FROM learning.review_card
    WHERE workspace_id = target_workspace AND id = target_card;

    IF card_status = 'APPROVED' AND NOT EXISTS (
        SELECT 1 FROM learning.review_schedule
        WHERE workspace_id = target_workspace AND card_id = target_card
    ) THEN
        RAISE EXCEPTION 'approved review card requires one schedule' USING ERRCODE = '23514';
    END IF;
    IF card_status IS NOT NULL AND card_status <> 'APPROVED' AND EXISTS (
        SELECT 1 FROM learning.review_schedule
        WHERE workspace_id = target_workspace AND card_id = target_card
    ) THEN
        RAISE EXCEPTION 'inactive review card cannot retain a schedule' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_schedule_card_status_guard ON learning.review_schedule;
CREATE CONSTRAINT TRIGGER review_schedule_card_status_guard
AFTER INSERT OR UPDATE OR DELETE ON learning.review_schedule
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION learning.assert_review_schedule_card_status();

-- Formal knowledge lifecycle changes must invalidate cards durably rather
-- than merely filtering them from a read query. The card transition invokes
-- review_card_schedule_sync above, so the associated schedule disappears in
-- the same transaction.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.invalidate_review_cards_for_claim_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM 1 FROM core.workspace WHERE id = NEW.workspace_id FOR UPDATE;

    IF NEW.status IN ('SUPERSEDED', 'DEPRECATED', 'INVALID')
       AND NEW.status IS DISTINCT FROM OLD.status THEN
        UPDATE learning.review_card
        SET status = 'INVALIDATED',
            invalidation_reason = 'CLAIM_' || NEW.status,
            invalidated_at = NEW.updated_at,
            version = version + 1,
            updated_at = NEW.updated_at
        WHERE workspace_id = NEW.workspace_id
          AND claim_id = NEW.id
          AND status = 'APPROVED';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_card_claim_lifecycle_invalidation ON core.claim;
CREATE TRIGGER review_card_claim_lifecycle_invalidation
AFTER UPDATE OF status ON core.claim
FOR EACH ROW
EXECUTE FUNCTION learning.invalidate_review_cards_for_claim_lifecycle();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.invalidate_review_cards_for_high_conflict()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM 1 FROM core.workspace WHERE id = NEW.workspace_id FOR UPDATE;

    IF NEW.severity IN ('CRITICAL', 'HIGH')
       AND NEW.status NOT IN ('RESOLVED', 'ACCEPTED_DIVERGENCE') THEN
        UPDATE learning.review_card AS card
        SET status = 'INVALIDATED',
            invalidation_reason = 'HIGH_SEVERITY_CONFLICT:' || NEW.id::text,
            invalidated_at = NEW.updated_at,
            version = card.version + 1,
            updated_at = NEW.updated_at
        WHERE card.workspace_id = NEW.workspace_id
          AND card.status = 'APPROVED'
          AND EXISTS (
              SELECT 1
              FROM core.conflict_member member
              WHERE member.workspace_id = NEW.workspace_id
                AND member.conflict_id = NEW.id
                AND member.claim_id = card.claim_id
          );
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_card_high_conflict_invalidation ON core.conflict;
CREATE TRIGGER review_card_high_conflict_invalidation
AFTER INSERT OR UPDATE OF status,severity ON core.conflict
FOR EACH ROW
EXECUTE FUNCTION learning.invalidate_review_cards_for_high_conflict();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.invalidate_review_cards_for_high_conflict_member()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    conflict_status text;
    conflict_severity text;
    occurred_at timestamptz;
BEGIN
    PERFORM 1 FROM core.workspace WHERE id = NEW.workspace_id FOR UPDATE;

    SELECT status,severity,updated_at
      INTO conflict_status,conflict_severity,occurred_at
      FROM core.conflict
     WHERE workspace_id = NEW.workspace_id
       AND id = NEW.conflict_id;

    IF conflict_severity IN ('CRITICAL', 'HIGH')
       AND conflict_status NOT IN ('RESOLVED', 'ACCEPTED_DIVERGENCE') THEN
        UPDATE learning.review_card
        SET status = 'INVALIDATED',
            invalidation_reason = 'HIGH_SEVERITY_CONFLICT:' || NEW.conflict_id::text,
            invalidated_at = occurred_at,
            version = version + 1,
            updated_at = occurred_at
        WHERE workspace_id = NEW.workspace_id
          AND claim_id = NEW.claim_id
          AND status = 'APPROVED';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_card_high_conflict_member_invalidation ON core.conflict_member;
CREATE TRIGGER review_card_high_conflict_member_invalidation
AFTER INSERT ON core.conflict_member
FOR EACH ROW
EXECUTE FUNCTION learning.invalidate_review_cards_for_high_conflict_member();

-- A quarantined ingestion attempt is the source lifecycle signal available to
-- the evidence model. Existing cards are retired and later recovery requires
-- an explicit edit plus reapproval; a subsequent passed attempt never revives
-- a stale learning fact implicitly.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.invalidate_review_cards_for_quarantined_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    occurred_at timestamptz;
BEGIN
    PERFORM 1 FROM core.workspace WHERE id = NEW.workspace_id FOR UPDATE;

    IF NEW.security_status = 'quarantined'
       AND (TG_OP = 'INSERT' OR NEW.security_status IS DISTINCT FROM OLD.security_status) THEN
        occurred_at := COALESCE(NEW.completed_at, CURRENT_TIMESTAMP);
        UPDATE learning.review_card AS card
        SET status = 'INVALIDATED',
            invalidation_reason = 'SOURCE_QUARANTINED:' || NEW.source_version_id::text,
            invalidated_at = occurred_at,
            version = card.version + 1,
            updated_at = occurred_at
        WHERE card.workspace_id = NEW.workspace_id
          AND card.status = 'APPROVED'
          AND EXISTS (
              SELECT 1
              FROM jsonb_array_elements(card.evidence) AS evidence(item)
              WHERE evidence.item->>'source_version_id' = NEW.source_version_id::text
          );
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_card_quarantined_source_invalidation ON ingestion.attempt;
CREATE TRIGGER review_card_quarantined_source_invalidation
AFTER INSERT OR UPDATE OF security_status ON ingestion.attempt
FOR EACH ROW
WHEN (NEW.security_status = 'quarantined')
EXECUTE FUNCTION learning.invalidate_review_cards_for_quarantined_source();

-- Bring already persisted cards to the same lifecycle invariant before the
-- triggers begin handling future knowledge and source changes.
UPDATE learning.review_card AS card
SET status = 'INVALIDATED',
    invalidation_reason = 'CLAIM_' || claim.status,
    invalidated_at = claim.updated_at,
    version = card.version + 1,
    updated_at = claim.updated_at
FROM core.claim AS claim
WHERE card.workspace_id = claim.workspace_id
  AND card.claim_id = claim.id
  AND card.status = 'APPROVED'
  AND claim.status IN ('SUPERSEDED', 'DEPRECATED', 'INVALID');

WITH high_conflict_claim AS (
    SELECT DISTINCT ON (member.workspace_id, member.claim_id)
        member.workspace_id,
        member.claim_id,
        conflict.id AS conflict_id,
        conflict.updated_at
    FROM core.conflict_member AS member
    JOIN core.conflict AS conflict
      ON conflict.workspace_id = member.workspace_id
     AND conflict.id = member.conflict_id
    WHERE conflict.severity IN ('CRITICAL', 'HIGH')
      AND conflict.status NOT IN ('RESOLVED', 'ACCEPTED_DIVERGENCE')
    ORDER BY member.workspace_id, member.claim_id, conflict.updated_at DESC, conflict.id DESC
)
UPDATE learning.review_card AS card
SET status = 'INVALIDATED',
    invalidation_reason = 'HIGH_SEVERITY_CONFLICT:' || conflict.conflict_id::text,
    invalidated_at = conflict.updated_at,
    version = card.version + 1,
    updated_at = conflict.updated_at
FROM high_conflict_claim AS conflict
WHERE card.workspace_id = conflict.workspace_id
  AND card.claim_id = conflict.claim_id
  AND card.status = 'APPROVED';

WITH quarantined_card_source AS (
    SELECT
        card.workspace_id,
        card.id,
        min(evidence.item->>'source_version_id') AS source_version_id
    FROM learning.review_card AS card
    CROSS JOIN LATERAL jsonb_array_elements(card.evidence) AS evidence(item)
    LEFT JOIN core.source_version AS source_version
      ON source_version.id::text = evidence.item->>'source_version_id'
    LEFT JOIN core.source AS source
      ON source.id = source_version.source_id
     AND source.workspace_id = card.workspace_id
    LEFT JOIN LATERAL (
        SELECT attempt.security_status
        FROM ingestion.attempt AS attempt
        WHERE attempt.workspace_id = card.workspace_id
          AND attempt.source_version_id::text = evidence.item->>'source_version_id'
        ORDER BY attempt.started_at DESC,attempt.id DESC
        LIMIT 1
    ) AS latest_attempt ON true
    WHERE card.status = 'APPROVED'
      AND (
          (source.id IS NOT NULL AND source_version.security_status = 'quarantined')
          OR latest_attempt.security_status = 'quarantined'
      )
    GROUP BY card.workspace_id,card.id
)
UPDATE learning.review_card AS card
SET status = 'INVALIDATED',
    invalidation_reason = 'SOURCE_QUARANTINED:' || source.source_version_id,
    invalidated_at = CURRENT_TIMESTAMP,
    version = card.version + 1,
    updated_at = CURRENT_TIMESTAMP
FROM quarantined_card_source AS source
WHERE card.workspace_id = source.workspace_id
  AND card.id = source.id
  AND card.status = 'APPROVED';

-- +goose Down

-- Do not silently lose command receipts or observable invalidation history.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM learning.review_command
        WHERE command_type IN ('EDIT_CARD', 'INVALIDATE_CARDS')
    ) OR EXISTS (
        SELECT 1 FROM learning.review_card
        WHERE invalidation_reason IS NOT NULL OR invalidated_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'review FSRS completion history exists; archive before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_schedule_card_status_guard ON learning.review_schedule;
DROP TRIGGER IF EXISTS review_card_schedule_guard ON learning.review_card;
DROP TRIGGER IF EXISTS review_card_schedule_sync ON learning.review_card;
DROP TRIGGER IF EXISTS review_card_quarantined_source_invalidation ON ingestion.attempt;
DROP TRIGGER IF EXISTS review_card_high_conflict_member_invalidation ON core.conflict_member;
DROP TRIGGER IF EXISTS review_card_high_conflict_invalidation ON core.conflict;
DROP TRIGGER IF EXISTS review_card_claim_lifecycle_invalidation ON core.claim;
DROP TRIGGER IF EXISTS review_command_reject_update_delete ON learning.review_command;
DROP TRIGGER IF EXISTS review_answer_reject_update_delete ON learning.review_answer;
DROP FUNCTION IF EXISTS learning.invalidate_review_cards_for_quarantined_source();
DROP FUNCTION IF EXISTS learning.invalidate_review_cards_for_high_conflict_member();
DROP FUNCTION IF EXISTS learning.invalidate_review_cards_for_high_conflict();
DROP FUNCTION IF EXISTS learning.invalidate_review_cards_for_claim_lifecycle();
DROP FUNCTION IF EXISTS learning.assert_review_schedule_card_status();
DROP FUNCTION IF EXISTS learning.assert_review_card_schedule();
DROP FUNCTION IF EXISTS learning.sync_review_card_schedule();
DROP FUNCTION IF EXISTS learning.reject_review_history_mutation();
DROP INDEX IF EXISTS learning.idx_learning_review_card_active_deck;

ALTER TABLE learning.review_command
    DROP CONSTRAINT IF EXISTS review_command_command_type_check;
ALTER TABLE learning.review_command
    ADD CONSTRAINT review_command_command_type_check
    CHECK (command_type IN (
        'CREATE_DECK','CREATE_CARD','DECIDE_CARD',
        'CHANGE_DECK_SCHEDULE','COMPLETE_SESSION'
    ));

ALTER TABLE learning.review_card
    DROP CONSTRAINT IF EXISTS learning_review_card_invalidation_metadata_check;
ALTER TABLE learning.review_card
    DROP COLUMN IF EXISTS invalidated_at;
ALTER TABLE learning.review_card
    DROP COLUMN IF EXISTS invalidation_reason;
