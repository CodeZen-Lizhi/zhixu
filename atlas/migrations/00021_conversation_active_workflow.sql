DROP INDEX IF EXISTS agent.uq_agent_answer_conversation_pending;

CREATE OR REPLACE FUNCTION agent.guard_answer_active_workflow()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    conversation_status text;
BEGIN
    SELECT status
      INTO conversation_status
      FROM agent.conversation
     WHERE id = NEW.conversation_id
       AND workspace_id = NEW.workspace_id
     FOR UPDATE;

    IF conversation_status IS NULL THEN
        RETURN NEW;
    END IF;
    IF conversation_status <> 'open' THEN
        RAISE EXCEPTION 'archived conversation does not accept an answer workflow'
            USING ERRCODE = '23514', CONSTRAINT = 'agent_answer_conversation_open';
    END IF;
    IF EXISTS (
        SELECT 1
          FROM agent.answer AS existing_answer
          JOIN workflow.run AS existing_run
            ON existing_run.id = existing_answer.workflow_run_id
           AND existing_run.workspace_id = existing_answer.workspace_id
         WHERE existing_answer.workspace_id = NEW.workspace_id
           AND existing_answer.conversation_id = NEW.conversation_id
           AND existing_run.status NOT IN ('succeeded', 'failed', 'cancelled')
    ) THEN
        RAISE EXCEPTION 'conversation already has an active answer workflow'
            USING ERRCODE = '23505', CONSTRAINT = 'agent_answer_active_workflow';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS agent_answer_active_workflow ON agent.answer;
DROP TRIGGER IF EXISTS agent_answer_validate_active_workflow ON agent.answer;
CREATE TRIGGER agent_answer_validate_active_workflow
    BEFORE INSERT ON agent.answer
    FOR EACH ROW EXECUTE FUNCTION agent.guard_answer_active_workflow();

INSERT INTO core.schema_meta (key, value)
VALUES ('conversation_active_workflow', 'm6-04-t06')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
