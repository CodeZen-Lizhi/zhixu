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
