-- atlas:txmode none

-- Due and answer eligibility share the same Workspace-local UTC-day count.
-- Rebuild a failed concurrent attempt instead of accepting an invalid index.
DROP INDEX CONCURRENTLY IF EXISTS learning.idx_learning_review_answer_workspace_created;
CREATE INDEX CONCURRENTLY idx_learning_review_answer_workspace_created
    ON learning.review_answer (workspace_id, created_at)
    INCLUDE (card_id, session_id);
