-- +goose Up

-- Historical Review Answers must retain the implementation version that
-- produced their score. Existing rows predate that contract and receive an
-- explicit unknown marker instead of being attributed to the current scorer.
ALTER TABLE learning.review_answer
    ADD COLUMN scorer_version text NOT NULL DEFAULT 'legacy/unknown';
ALTER TABLE learning.review_answer
    ALTER COLUMN scorer_version DROP DEFAULT;
ALTER TABLE learning.review_answer
    ADD CONSTRAINT learning_review_answer_scorer_version_check
    CHECK (btrim(scorer_version) = scorer_version
        AND btrim(scorer_version) <> ''
        AND octet_length(scorer_version) <= 128);

-- +goose Down

-- Scorer identity is part of the immutable Review Answer audit fact. Require
-- an explicit archival step before removing it, and hold the table lock so a
-- concurrent Answer cannot appear between the guard and the schema change.
LOCK TABLE learning.review_answer IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.review_answer LIMIT 1) THEN
        RAISE EXCEPTION 'review Answer scorer history exists; archive before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE learning.review_answer
    DROP CONSTRAINT IF EXISTS learning_review_answer_scorer_version_check;
ALTER TABLE learning.review_answer
    DROP COLUMN IF EXISTS scorer_version;
