LOCK TABLE agent.model_call IN ACCESS EXCLUSIVE MODE;

ALTER TABLE agent.model_call
    DROP CONSTRAINT IF EXISTS agent_model_call_phase_check,
    ADD CONSTRAINT agent_model_call_phase_check CHECK (
        phase IN ('PLAN', 'AGENT', 'ANSWER', 'INITIAL', 'REPAIR', 'REDUCED', 'REVIEW')
    ),
    DROP CONSTRAINT IF EXISTS agent_model_call_phase_order,
    ADD CONSTRAINT agent_model_call_phase_order CHECK (
        (phase = 'PLAN' AND call_no = 1)
        OR (phase = 'AGENT' AND call_no >= 1)
        OR (phase = 'ANSWER' AND call_no >= 1)
        OR (phase = 'INITIAL' AND call_no >= 1)
        OR (phase = 'REPAIR' AND call_no >= 2)
        OR (phase = 'REDUCED' AND call_no >= 3)
        OR (phase = 'REVIEW' AND call_no >= 2)
    );

DROP INDEX IF EXISTS agent.uq_agent_model_call_generation_phase;
CREATE UNIQUE INDEX uq_agent_model_call_generation_phase
    ON agent.model_call (model_run_id, phase)
    WHERE phase IN ('ANSWER', 'INITIAL', 'REPAIR', 'REDUCED');

CREATE OR REPLACE FUNCTION agent.guard_model_call_phase_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    previous_phase text;
    previous_status text;
BEGIN
    IF NEW.call_no = 1 THEN
        IF NEW.phase NOT IN ('PLAN', 'AGENT', 'ANSWER', 'INITIAL') THEN
            RAISE EXCEPTION 'agent model call first phase is invalid' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    SELECT phase, status
      INTO previous_phase, previous_status
      FROM agent.model_call
     WHERE model_run_id = NEW.model_run_id
       AND call_no = NEW.call_no - 1
     FOR SHARE;

    IF previous_status IS DISTINCT FROM 'SUCCEEDED'
       OR NOT (
            (previous_phase = 'PLAN' AND NEW.phase IN ('AGENT', 'ANSWER', 'INITIAL'))
            OR (previous_phase = 'AGENT' AND NEW.phase IN ('AGENT', 'ANSWER'))
            OR (previous_phase = 'ANSWER' AND NEW.phase = 'INITIAL')
            OR (previous_phase = 'INITIAL' AND NEW.phase IN ('REPAIR', 'REVIEW'))
            OR (previous_phase = 'REPAIR' AND NEW.phase IN ('REDUCED', 'REVIEW'))
            OR (previous_phase = 'REDUCED' AND NEW.phase = 'REVIEW')
       ) THEN
        RAISE EXCEPTION 'agent model call phase predecessor is invalid' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
