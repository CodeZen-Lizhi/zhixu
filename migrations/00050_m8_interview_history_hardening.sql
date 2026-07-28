-- +goose Up

-- Receipts and submitted turns are historical facts. Exact replay depends on
-- retaining their original request binding and response snapshot verbatim.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.reject_m8_append_only_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_memory_command_append_only ON learning.memory_command;
CREATE TRIGGER trg_learning_memory_command_append_only
    BEFORE UPDATE OR DELETE ON learning.memory_command
    FOR EACH ROW EXECUTE FUNCTION learning.reject_m8_append_only_mutation();

DROP TRIGGER IF EXISTS trg_learning_interview_command_append_only ON learning.interview_command;
CREATE TRIGGER trg_learning_interview_command_append_only
    BEFORE UPDATE OR DELETE ON learning.interview_command
    FOR EACH ROW EXECUTE FUNCTION learning.reject_m8_append_only_mutation();

DROP TRIGGER IF EXISTS trg_learning_interview_turn_append_only ON learning.interview_turn;
CREATE TRIGGER trg_learning_interview_turn_append_only
    BEFORE UPDATE OR DELETE ON learning.interview_turn
    FOR EACH ROW EXECUTE FUNCTION learning.reject_m8_append_only_mutation();

-- A frozen question may advance once from PENDING to ANSWERED or SKIPPED.
-- Every identity, prompt, answer point, evidence, and provenance field remains
-- immutable; terminal questions cannot be rewritten or deleted.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.enforce_interview_question_history()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'interview question is retained history' USING ERRCODE = '55000';
    END IF;

    IF ROW(
        NEW.id,
        NEW.workspace_id,
        NEW.session_id,
        NEW.question_no,
        NEW.follow_up_no,
        NEW.parent_question_id,
        NEW.claim_id,
        NEW.topic_id,
        NEW.prompt,
        NEW.answer_points,
        NEW.evidence,
        NEW.fingerprint,
        NEW.created_at
    ) IS DISTINCT FROM ROW(
        OLD.id,
        OLD.workspace_id,
        OLD.session_id,
        OLD.question_no,
        OLD.follow_up_no,
        OLD.parent_question_id,
        OLD.claim_id,
        OLD.topic_id,
        OLD.prompt,
        OLD.answer_points,
        OLD.evidence,
        OLD.fingerprint,
        OLD.created_at
    ) THEN
        RAISE EXCEPTION 'interview question facts are immutable' USING ERRCODE = '55000';
    END IF;

    IF OLD.status <> 'PENDING' OR OLD.answered_at IS NOT NULL THEN
        RAISE EXCEPTION 'terminal interview question is immutable' USING ERRCODE = '55000';
    END IF;

    IF NEW.status = 'ANSWERED'
       AND NEW.answered_at IS NOT NULL
       AND NEW.answered_at >= OLD.created_at THEN
        RETURN NEW;
    END IF;
    IF NEW.status = 'SKIPPED' AND NEW.answered_at IS NULL THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'interview question transition is invalid' USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_learning_interview_question_history ON learning.interview_question;
CREATE TRIGGER trg_learning_interview_question_history
    BEFORE UPDATE OR DELETE ON learning.interview_question
    FOR EACH ROW EXECUTE FUNCTION learning.enforce_interview_question_history();

CREATE INDEX IF NOT EXISTS idx_learning_review_session_interview_started
    ON learning.review_session (workspace_id, started_at DESC, id DESC)
    WHERE session_type = 'INTERVIEW';

-- +goose Down

-- Removing these guards while retained receipts, turns, or questions exist
-- would re-open mutation paths that the running code treats as impossible.
LOCK TABLE learning.memory_command,
           learning.interview_command,
           learning.interview_turn,
           learning.interview_question
    IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.memory_command LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.interview_command LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.interview_turn LIMIT 1)
       OR EXISTS (SELECT 1 FROM learning.interview_question LIMIT 1) THEN
        RAISE EXCEPTION 'M8 retained history exists; archive before removing append-only guards'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP INDEX IF EXISTS learning.idx_learning_review_session_interview_started;
DROP TRIGGER IF EXISTS trg_learning_interview_question_history ON learning.interview_question;
DROP FUNCTION IF EXISTS learning.enforce_interview_question_history();
DROP TRIGGER IF EXISTS trg_learning_interview_turn_append_only ON learning.interview_turn;
DROP TRIGGER IF EXISTS trg_learning_interview_command_append_only ON learning.interview_command;
DROP TRIGGER IF EXISTS trg_learning_memory_command_append_only ON learning.memory_command;
DROP FUNCTION IF EXISTS learning.reject_m8_append_only_mutation();
