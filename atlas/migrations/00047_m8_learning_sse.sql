-- Learning mutations must invalidate browser queries from the same durable
-- transaction. The projection contains only stable identities and versions;
-- answers, Memory content, evidence, and report text never enter SSE payloads.
CREATE OR REPLACE FUNCTION ops.project_learning_server_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    row_data jsonb;
    action text;
    event_type text;
    resource_id text;
    resource_version bigint;
    occurred_at timestamptz;
BEGIN
    IF TG_OP = 'DELETE' THEN
        row_data := to_jsonb(OLD);
        action := 'deleted';
    ELSIF TG_OP = 'UPDATE' THEN
        row_data := to_jsonb(NEW);
        action := 'updated';
    ELSE
        row_data := to_jsonb(NEW);
        action := 'created';
    END IF;

    resource_id := row_data ->> TG_ARGV[2];
    IF resource_id IS NULL OR resource_id = '' THEN
        RAISE EXCEPTION 'learning SSE resource identity is missing'
            USING ERRCODE = '23514';
    END IF;

    IF TG_ARGV[3] <> '' THEN
        resource_version := NULLIF(row_data ->> TG_ARGV[3], '')::bigint;
    ELSE
        -- Immutable rows are version one. The only unversioned mutable rows
        -- (review sessions) have a single ACTIVE -> terminal transition.
        resource_version := CASE WHEN TG_OP = 'UPDATE' THEN 2 ELSE 1 END;
    END IF;
    IF resource_version IS NULL OR resource_version < 1 THEN
        RAISE EXCEPTION 'learning SSE resource version is invalid'
            USING ERRCODE = '23514';
    END IF;

    IF TG_OP = 'UPDATE' AND TG_NARGS > 5 THEN
        IF TG_ARGV[5] <> '' AND NULLIF(row_data ->> TG_ARGV[5], '') IS NOT NULL THEN
            occurred_at := (row_data ->> TG_ARGV[5])::timestamptz;
        ELSE
            occurred_at := CURRENT_TIMESTAMP;
        END IF;
    ELSIF TG_ARGV[4] <> '' AND NULLIF(row_data ->> TG_ARGV[4], '') IS NOT NULL THEN
        occurred_at := (row_data ->> TG_ARGV[4])::timestamptz;
    ELSE
        occurred_at := CURRENT_TIMESTAMP;
    END IF;
    event_type := TG_ARGV[0] || '.' || action;

    INSERT INTO ops.server_event(
        workspace_id,conversation_id,workflow_run_id,event_type,resource_ref,
        resource_version,payload_summary,schema_version,source_event_ref,
        occurred_at,expires_at
    ) VALUES (
        (row_data ->> 'workspace_id')::uuid,NULL,NULL,event_type,
        TG_ARGV[1] || ':' || resource_id,resource_version,'{}'::jsonb,1,
        event_type || ':' || resource_id || ':v' || resource_version::text || ':tx' || txid_current()::text,
        occurred_at,occurred_at + INTERVAL '24 hours'
    )
    ON CONFLICT (workspace_id,source_event_ref) DO NOTHING;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS review_deck_project_server_event ON learning.review_deck;
CREATE TRIGGER review_deck_project_server_event
    AFTER INSERT OR UPDATE OR DELETE ON learning.review_deck
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('review.deck','review_deck','id','version','updated_at');

DROP TRIGGER IF EXISTS review_card_project_server_event ON learning.review_card;
CREATE TRIGGER review_card_project_server_event
    AFTER INSERT OR UPDATE OR DELETE ON learning.review_card
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('review.card','review_card','id','version','updated_at');

DROP TRIGGER IF EXISTS review_schedule_project_server_event ON learning.review_schedule;
CREATE TRIGGER review_schedule_project_server_event
    AFTER INSERT OR UPDATE OR DELETE ON learning.review_schedule
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('review.schedule','review_schedule','card_id','version','');

DROP TRIGGER IF EXISTS review_session_project_server_event ON learning.review_session;
CREATE TRIGGER review_session_project_server_event
    AFTER INSERT OR UPDATE ON learning.review_session
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('review.session','review_session','id','','started_at','ended_at');

DROP TRIGGER IF EXISTS review_answer_project_server_event ON learning.review_answer;
CREATE TRIGGER review_answer_project_server_event
    AFTER INSERT ON learning.review_answer
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('review.answer','review_answer','id','','created_at');

DROP TRIGGER IF EXISTS memory_project_server_event ON learning.memory;
CREATE TRIGGER memory_project_server_event
    AFTER INSERT OR UPDATE ON learning.memory
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('memory','memory','id','version','updated_at');

DROP TRIGGER IF EXISTS interview_session_project_server_event ON learning.interview_session;
CREATE TRIGGER interview_session_project_server_event
    AFTER INSERT OR UPDATE ON learning.interview_session
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('interview.session','interview_session','session_id','version','updated_at');

DROP TRIGGER IF EXISTS interview_turn_project_server_event ON learning.interview_turn;
CREATE TRIGGER interview_turn_project_server_event
    AFTER INSERT ON learning.interview_turn
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('interview.turn','interview_turn','id','','created_at');

DROP TRIGGER IF EXISTS interview_report_project_server_event ON learning.interview_report;
CREATE TRIGGER interview_report_project_server_event
    AFTER INSERT ON learning.interview_report
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('interview.report','interview_report','id','','created_at');

DROP TRIGGER IF EXISTS interview_learning_path_project_server_event ON learning.interview_learning_path;
CREATE TRIGGER interview_learning_path_project_server_event
    AFTER INSERT OR UPDATE ON learning.interview_learning_path
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('learning_path','learning_path','id','version','updated_at');

DROP TRIGGER IF EXISTS interview_learning_path_step_project_server_event ON learning.interview_learning_path_step;
CREATE TRIGGER interview_learning_path_step_project_server_event
    AFTER INSERT OR UPDATE ON learning.interview_learning_path_step
    FOR EACH ROW EXECUTE FUNCTION ops.project_learning_server_event('learning_path.step','learning_path_step','id','version','updated_at');
