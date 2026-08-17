-- +goose Up

-- Drafts are deliberately separate from agent.answer and ops.server_event:
-- they are a short-lived UI projection, never evidence or a published Answer.
ALTER TABLE agent.answer
    ADD CONSTRAINT uq_agent_answer_id_workspace_workflow
        UNIQUE (id, workspace_id, workflow_run_id);

ALTER TABLE workflow.node_attempt
    ADD CONSTRAINT uq_workflow_node_attempt_id_node_no
        UNIQUE (id, node_run_id, attempt_no);

CREATE TABLE IF NOT EXISTS agent.answer_draft_session (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    answer_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_attempt_id uuid NOT NULL,
    attempt_no integer NOT NULL CHECK (attempt_no > 0),
    lease_owner text NOT NULL CHECK (btrim(lease_owner) <> '' AND octet_length(lease_owner) <= 256),
    generation bigint NOT NULL CHECK (generation > 0),
    status text NOT NULL CHECK (status IN ('ACTIVE','COMPLETED','DEGRADED','PUBLISHED','ABORTED','SUPERSEDED')),
    next_sequence bigint NOT NULL DEFAULT 1 CHECK (next_sequence > 0),
    total_bytes integer NOT NULL DEFAULT 0 CHECK (total_bytes >= 0 AND total_bytes <= 4194304),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at timestamptz,
    CONSTRAINT answer_draft_session_expiry_valid CHECK (expires_at > created_at),
    CONSTRAINT answer_draft_session_completed_at_valid CHECK (
        (status IN ('ACTIVE','DEGRADED') AND completed_at IS NULL)
        OR (status IN ('COMPLETED','PUBLISHED','ABORTED','SUPERSEDED') AND completed_at IS NOT NULL)
    ),
    CONSTRAINT fk_answer_draft_session_answer_run_workspace
        FOREIGN KEY (answer_id, workspace_id, workflow_run_id)
        REFERENCES agent.answer(id, workspace_id, workflow_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_answer_draft_session_run_workspace
        FOREIGN KEY (workflow_run_id, workspace_id)
        REFERENCES workflow.run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_answer_draft_session_node_run
        FOREIGN KEY (node_run_id, workflow_run_id)
        REFERENCES workflow.node_run(id, run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_answer_draft_session_attempt_node_no
        FOREIGN KEY (node_attempt_id, node_run_id, attempt_no)
        REFERENCES workflow.node_attempt(id, node_run_id, attempt_no) ON DELETE RESTRICT,
    CONSTRAINT uq_answer_draft_session_generation UNIQUE (workspace_id, answer_id, generation),
    CONSTRAINT uq_answer_draft_session_attempt UNIQUE (node_attempt_id, generation)
);

-- At most one generation may remain visible/recoverable for one pending
-- Answer. A new DB-time-valid Attempt first marks this row SUPERSEDED.
CREATE UNIQUE INDEX IF NOT EXISTS uq_answer_draft_session_current
    ON agent.answer_draft_session (workspace_id, answer_id)
    WHERE status IN ('ACTIVE','COMPLETED','DEGRADED');

CREATE INDEX IF NOT EXISTS idx_answer_draft_session_cleanup
    ON agent.answer_draft_session (expires_at, id);

CREATE TABLE IF NOT EXISTS agent.answer_draft_chunk (
    session_id uuid NOT NULL REFERENCES agent.answer_draft_session(id) ON DELETE CASCADE,
    sequence bigint NOT NULL CHECK (sequence > 0),
    content text NOT NULL CHECK (octet_length(content) > 0 AND octet_length(content) <= 65536),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, sequence)
);

-- Session identity and generation never change. The small transition matrix
-- prevents a retained terminal projection from becoming browser-visible again.
-- Lease ownership is checked by the repository using DB time before every
-- mutable operation; this trigger protects the persistent lifecycle itself.
-- +goose StatementBegin
CREATE FUNCTION agent.enforce_answer_draft_session_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.answer_id IS DISTINCT FROM OLD.answer_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.node_run_id IS DISTINCT FROM OLD.node_run_id
       OR NEW.node_attempt_id IS DISTINCT FROM OLD.node_attempt_id
       OR NEW.attempt_no IS DISTINCT FROM OLD.attempt_no
       OR NEW.lease_owner IS DISTINCT FROM OLD.lease_owner
       OR NEW.generation IS DISTINCT FROM OLD.generation
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'answer draft session identity is immutable' USING ERRCODE = '55000';
    END IF;
    IF NEW.next_sequence < OLD.next_sequence OR NEW.total_bytes < OLD.total_bytes THEN
        RAISE EXCEPTION 'answer draft session counters are monotonic' USING ERRCODE = '55000';
    END IF;
    IF NEW.next_sequence <> OLD.next_sequence OR NEW.total_bytes <> OLD.total_bytes THEN
        IF OLD.status <> 'ACTIVE' OR NEW.status <> 'ACTIVE' OR NEW.next_sequence <> OLD.next_sequence + 1 THEN
            RAISE EXCEPTION 'answer draft chunks may only append to an active session' USING ERRCODE = '55000';
        END IF;
    END IF;
    IF NEW.status = OLD.status THEN
        RETURN NEW;
    END IF;
    IF (OLD.status = 'ACTIVE' AND NEW.status IN ('COMPLETED','DEGRADED','ABORTED','SUPERSEDED'))
       OR (OLD.status IN ('COMPLETED','DEGRADED') AND NEW.status IN ('PUBLISHED','ABORTED','SUPERSEDED')) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'answer draft session transition is invalid' USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS answer_draft_session_transition_guard ON agent.answer_draft_session;
CREATE TRIGGER answer_draft_session_transition_guard
    BEFORE UPDATE ON agent.answer_draft_session
    FOR EACH ROW EXECUTE FUNCTION agent.enforce_answer_draft_session_transition();

-- +goose Down

DROP TABLE IF EXISTS agent.answer_draft_chunk;
DROP TRIGGER IF EXISTS answer_draft_session_transition_guard ON agent.answer_draft_session;
DROP FUNCTION IF EXISTS agent.enforce_answer_draft_session_transition();
DROP INDEX IF EXISTS agent.idx_answer_draft_session_cleanup;
DROP INDEX IF EXISTS agent.uq_answer_draft_session_current;
DROP TABLE IF EXISTS agent.answer_draft_session;
ALTER TABLE workflow.node_attempt
    DROP CONSTRAINT IF EXISTS uq_workflow_node_attempt_id_node_no;
ALTER TABLE agent.answer
    DROP CONSTRAINT IF EXISTS uq_agent_answer_id_workspace_workflow;
