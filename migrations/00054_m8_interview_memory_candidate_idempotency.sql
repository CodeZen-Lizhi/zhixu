-- +goose Up

-- A persisted Interview learning-path step has one semantic Memory Candidate.
-- Command receipts remain per client key; this index prevents a concurrent or
-- out-of-band writer from creating a second durable candidate for the same
-- bounded Interview provenance.
CREATE UNIQUE INDEX IF NOT EXISTS uq_learning_memory_interview_provenance
    ON learning.memory (
        workspace_id,
        owner_principal_kind,
        owner_principal_id,
        source_type,
        source_ref
    )
    WHERE source_type = 'INTERVIEW';

-- +goose Down

DROP INDEX IF EXISTS learning.uq_learning_memory_interview_provenance;
