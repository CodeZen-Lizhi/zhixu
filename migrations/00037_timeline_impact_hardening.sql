-- +goose Up

-- M7-04 hardens the generic 00034 placeholders into replay-safe Timeline and
-- Impact projections. Knowledge Event remains a projection, never a writable
-- Event Sourcing authority.
DROP TRIGGER IF EXISTS knowledge_event_append_only ON ops.knowledge_event;

ALTER TABLE ops.knowledge_event
    ADD COLUMN IF NOT EXISTS source_event_ref text,
    ADD COLUMN IF NOT EXISTS event_version integer DEFAULT 1,
    ADD COLUMN IF NOT EXISTS schema_version text DEFAULT 'knowledge-event/v1',
    ADD COLUMN IF NOT EXISTS summary text DEFAULT '',
    ADD COLUMN IF NOT EXISTS correlation jsonb DEFAULT '{}'::jsonb;

UPDATE ops.knowledge_event
SET event_type = btrim(event_type),
    aggregate_type = btrim(aggregate_type),
    source_event_ref = COALESCE(NULLIF(btrim(source_event_ref), ''), 'legacy:' || id::text),
    source_ref = COALESCE(NULLIF(btrim(source_ref), ''), lower(btrim(aggregate_type)) || ':' || COALESCE(aggregate_id::text, id::text)),
    event_version = COALESCE(event_version, 1),
    schema_version = COALESCE(NULLIF(schema_version, ''), 'knowledge-event/v1'),
    summary = COALESCE(summary, ''),
    correlation = COALESCE(correlation, '{}'::jsonb)
WHERE event_type <> btrim(event_type)
   OR aggregate_type <> btrim(aggregate_type)
   OR source_event_ref IS NULL
   OR btrim(source_event_ref) = ''
   OR source_ref IS NULL
   OR btrim(source_ref) = ''
   OR event_version IS NULL
   OR schema_version IS NULL
   OR schema_version = ''
   OR summary IS NULL
   OR correlation IS NULL;

ALTER TABLE ops.knowledge_event
    ALTER COLUMN source_event_ref SET NOT NULL,
    ALTER COLUMN source_ref SET NOT NULL,
    ALTER COLUMN event_version SET NOT NULL,
    ALTER COLUMN schema_version SET NOT NULL,
    ALTER COLUMN summary SET NOT NULL,
    ALTER COLUMN correlation SET NOT NULL,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_source_ref,
    ADD CONSTRAINT ops_knowledge_event_source_ref CHECK (
        btrim(source_event_ref) <> ''
        AND source_event_ref = btrim(source_event_ref)
        AND octet_length(source_event_ref) <= 512
        AND source_event_ref !~ E'[\r\n]'
    ),
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_object_ref,
    ADD CONSTRAINT ops_knowledge_event_object_ref CHECK (
        btrim(source_ref) <> ''
        AND source_ref = btrim(source_ref)
        AND octet_length(source_ref) <= 512
        AND source_ref !~ E'[\r\n]'
    ),
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_event_type,
    ADD CONSTRAINT ops_knowledge_event_event_type CHECK (
        event_type = btrim(event_type)
        AND event_type !~ E'[\r\n]'
    ),
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_aggregate_type,
    ADD CONSTRAINT ops_knowledge_event_aggregate_type CHECK (
        aggregate_type = btrim(aggregate_type)
        AND aggregate_type !~ E'[\r\n]'
    ),
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_version,
    ADD CONSTRAINT ops_knowledge_event_version CHECK (event_version > 0),
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_schema,
    ADD CONSTRAINT ops_knowledge_event_schema CHECK (schema_version = 'knowledge-event/v1'),
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_summary,
    ADD CONSTRAINT ops_knowledge_event_summary CHECK (octet_length(summary) <= 4096),
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_payload_size,
    ADD CONSTRAINT ops_knowledge_event_payload_size CHECK (octet_length(payload::text) <= 32768),
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_correlation,
    ADD CONSTRAINT ops_knowledge_event_correlation CHECK (
        jsonb_typeof(correlation) = 'object'
        AND octet_length(correlation::text) <= 8192
    ),
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_time_order,
    ADD CONSTRAINT ops_knowledge_event_time_order CHECK (created_at >= occurred_at);

CREATE UNIQUE INDEX IF NOT EXISTS uq_ops_knowledge_event_source
    ON ops.knowledge_event (workspace_id, source_event_ref);
CREATE INDEX IF NOT EXISTS idx_ops_knowledge_event_aggregate_time
    ON ops.knowledge_event (workspace_id, aggregate_type, aggregate_id, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_ops_knowledge_event_type_time
    ON ops.knowledge_event (workspace_id, event_type, occurred_at DESC, id DESC);

-- Recreate the append-only guard after the additive backfill. 00034 owns the
-- shared trigger function; this migration only attaches it to the hardened row.
CREATE TRIGGER knowledge_event_append_only
    BEFORE UPDATE OR DELETE ON ops.knowledge_event
    FOR EACH ROW EXECUTE FUNCTION ops.reject_append_only_mutation();

ALTER TABLE ops.impact_report
    ADD COLUMN IF NOT EXISTS schema_version text DEFAULT 'impact-report/v1',
    ADD COLUMN IF NOT EXISTS source_event_version bigint DEFAULT 1,
    ADD COLUMN IF NOT EXISTS fingerprint text,
    ADD COLUMN IF NOT EXISTS error_code text,
    ADD COLUMN IF NOT EXISTS stale_reason text,
    ADD COLUMN IF NOT EXISTS version bigint DEFAULT 1,
    ADD COLUMN IF NOT EXISTS created_at timestamptz;

UPDATE ops.impact_report AS report
SET schema_version = COALESCE(NULLIF(report.schema_version, ''), 'impact-report/v1'),
    source_event_version = COALESCE(report.source_event_version, event.event_version::bigint, 1),
    fingerprint = COALESCE(
        NULLIF(report.fingerprint, ''),
        md5(report.source_event_id::text || ':' || report.generated_at::text || ':' || report.objects::text)
        || md5('timeline:' || report.source_event_id::text || ':' || report.generated_at::text || ':' || report.objects::text)
    ),
    error_code = CASE
        WHEN report.status = 'FAILED' THEN COALESCE(NULLIF(btrim(report.error_code), ''), 'IMPACT_LEGACY_FAILED')
        ELSE NULL
    END,
    stale_reason = CASE
        WHEN report.status = 'STALE' THEN COALESCE(NULLIF(btrim(report.stale_reason), ''), 'legacy report requires reanalysis')
        ELSE NULL
    END,
    version = COALESCE(report.version, 1),
    created_at = COALESCE(report.created_at, report.generated_at)
FROM ops.knowledge_event AS event
WHERE event.id = report.source_event_id
  AND event.workspace_id = report.workspace_id
  AND (
      report.schema_version IS NULL
      OR report.schema_version = ''
      OR report.source_event_version IS NULL
      OR report.fingerprint IS NULL
      OR report.fingerprint = ''
      OR (report.status = 'FAILED' AND (report.error_code IS NULL OR btrim(report.error_code) = ''))
      OR (report.status <> 'FAILED' AND report.error_code IS NOT NULL)
      OR (report.status = 'STALE' AND (report.stale_reason IS NULL OR btrim(report.stale_reason) = ''))
      OR (report.status <> 'STALE' AND report.stale_reason IS NOT NULL)
      OR report.version IS NULL
      OR report.created_at IS NULL
  );

ALTER TABLE ops.impact_report
    ALTER COLUMN schema_version SET NOT NULL,
    ALTER COLUMN source_event_version SET NOT NULL,
    ALTER COLUMN fingerprint SET NOT NULL,
    ALTER COLUMN version SET NOT NULL,
    ALTER COLUMN created_at SET NOT NULL,
    DROP CONSTRAINT IF EXISTS ops_impact_report_schema,
    ADD CONSTRAINT ops_impact_report_schema CHECK (schema_version = 'impact-report/v1'),
    DROP CONSTRAINT IF EXISTS ops_impact_report_source_version,
    ADD CONSTRAINT ops_impact_report_source_version CHECK (source_event_version > 0),
    DROP CONSTRAINT IF EXISTS ops_impact_report_objects,
    ADD CONSTRAINT ops_impact_report_objects CHECK (
        jsonb_typeof(objects) = 'array'
        AND jsonb_array_length(objects) <= 500
        AND octet_length(objects::text) <= 262144
    ),
    DROP CONSTRAINT IF EXISTS ops_impact_report_summary,
    ADD CONSTRAINT ops_impact_report_summary CHECK (
        jsonb_typeof(summary) = 'object'
        AND octet_length(summary::text) <= 32768
    ),
    DROP CONSTRAINT IF EXISTS ops_impact_report_fingerprint,
    ADD CONSTRAINT ops_impact_report_fingerprint CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    DROP CONSTRAINT IF EXISTS ops_impact_report_failure,
    ADD CONSTRAINT ops_impact_report_failure CHECK (
        (status = 'FAILED' AND error_code IS NOT NULL AND error_code = btrim(error_code)
            AND error_code !~ E'[\r\n]' AND octet_length(error_code) <= 256)
        OR (status <> 'FAILED' AND error_code IS NULL)
    ),
    DROP CONSTRAINT IF EXISTS ops_impact_report_stale,
    ADD CONSTRAINT ops_impact_report_stale CHECK (
        (status = 'STALE' AND stale_reason IS NOT NULL AND stale_reason = btrim(stale_reason)
            AND octet_length(stale_reason) <= 4096)
        OR (status <> 'STALE' AND stale_reason IS NULL)
    ),
    DROP CONSTRAINT IF EXISTS ops_impact_report_version,
    ADD CONSTRAINT ops_impact_report_version CHECK (version > 0),
    DROP CONSTRAINT IF EXISTS ops_impact_report_time_order,
    ADD CONSTRAINT ops_impact_report_time_order CHECK (created_at >= generated_at);

CREATE INDEX IF NOT EXISTS idx_ops_impact_report_workspace_time
    ON ops.impact_report (workspace_id, generated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_ops_impact_report_workspace_status
    ON ops.impact_report (workspace_id, status, generated_at DESC, id DESC);

DROP TRIGGER IF EXISTS impact_report_append_only ON ops.impact_report;
CREATE TRIGGER impact_report_append_only
    BEFORE UPDATE OR DELETE ON ops.impact_report
    FOR EACH ROW EXECUTE FUNCTION ops.reject_append_only_mutation();

CREATE INDEX IF NOT EXISTS idx_knowledge_conflict_workspace_topic
    ON core.conflict (workspace_id, topic_id, id)
    WHERE topic_id IS NOT NULL;

-- Formal transactions append a minimal source row here. The Worker projects
-- it into ops.knowledge_event asynchronously, so a projection outage never
-- rolls back Proposal, Approval, Commit, Conflict or Knowledge state.
CREATE TABLE IF NOT EXISTS ops.timeline_projection_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    event_type text NOT NULL CHECK (
        btrim(event_type) <> '' AND event_type = btrim(event_type)
        AND octet_length(event_type) <= 128 AND event_type !~ E'[\r\n]'
    ),
    aggregate_type text NOT NULL CHECK (
        btrim(aggregate_type) <> '' AND aggregate_type = btrim(aggregate_type)
        AND octet_length(aggregate_type) <= 128 AND aggregate_type !~ E'[\r\n]'
    ),
    aggregate_id uuid,
    source_event_ref text NOT NULL CHECK (
        btrim(source_event_ref) <> '' AND source_event_ref = btrim(source_event_ref)
        AND octet_length(source_event_ref) <= 512 AND source_event_ref !~ E'[\r\n]'
    ),
    source_ref text NOT NULL CHECK (
        btrim(source_ref) <> '' AND source_ref = btrim(source_ref)
        AND octet_length(source_ref) <= 512 AND source_ref !~ E'[\r\n]'
    ),
    event_version integer NOT NULL CHECK (event_version > 0),
    summary text NOT NULL CHECK (octet_length(summary) <= 4096),
    correlation jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(correlation) = 'object' AND octet_length(correlation::text) <= 8192
    ),
    occurred_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'PROJECTED', 'POISONED')),
    error_code text CHECK (
        error_code IS NULL OR (
            btrim(error_code) <> '' AND error_code = btrim(error_code)
            AND octet_length(error_code) <= 256 AND error_code !~ E'[\r\n]'
        )
    ),
    projected_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CONSTRAINT uq_ops_timeline_projection_source UNIQUE (workspace_id, source_event_ref),
    CONSTRAINT uq_ops_timeline_projection_event UNIQUE (workspace_id, event_id),
    CONSTRAINT ops_timeline_projection_time_order CHECK (
        created_at >= occurred_at AND updated_at >= created_at
        AND (projected_at IS NULL OR projected_at >= created_at)
    ),
    CONSTRAINT ops_timeline_projection_status_binding CHECK (
        (status = 'PENDING' AND error_code IS NULL AND projected_at IS NULL)
        OR (status = 'PROJECTED' AND error_code IS NULL AND projected_at IS NOT NULL)
        OR (status = 'POISONED' AND error_code IS NOT NULL AND projected_at IS NULL)
    )
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.guard_timeline_projection_outbox_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'Timeline projection outbox rows cannot be deleted'
            USING ERRCODE = '55000';
    END IF;
    IF OLD.status <> 'PENDING'
       OR NEW.status NOT IN ('PROJECTED', 'POISONED')
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'invalid Timeline projection outbox state transition'
            USING ERRCODE = '55000';
    END IF;
    IF ROW(
        NEW.id, NEW.event_id, NEW.workspace_id, NEW.event_type,
        NEW.aggregate_type, NEW.aggregate_id, NEW.source_event_ref,
        NEW.source_ref, NEW.event_version, NEW.summary, NEW.correlation,
        NEW.occurred_at, NEW.created_at
    ) IS DISTINCT FROM ROW(
        OLD.id, OLD.event_id, OLD.workspace_id, OLD.event_type,
        OLD.aggregate_type, OLD.aggregate_id, OLD.source_event_ref,
        OLD.source_ref, OLD.event_version, OLD.summary, OLD.correlation,
        OLD.occurred_at, OLD.created_at
    ) THEN
        RAISE EXCEPTION 'Timeline projection outbox source binding is immutable'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS timeline_projection_outbox_guard ON ops.timeline_projection_outbox;
CREATE TRIGGER timeline_projection_outbox_guard
    BEFORE UPDATE OR DELETE ON ops.timeline_projection_outbox
    FOR EACH ROW EXECUTE FUNCTION ops.guard_timeline_projection_outbox_mutation();

CREATE INDEX IF NOT EXISTS idx_ops_timeline_projection_pending
    ON ops.timeline_projection_outbox (occurred_at, id)
    WHERE status = 'PENDING';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enqueue_timeline_projection(
    projection_workspace_id uuid,
    projection_event_type text,
    projection_aggregate_type text,
    projection_aggregate_id uuid,
    projection_source_event_ref text,
    projection_source_ref text,
    projection_event_version bigint,
    projection_summary text,
    projection_correlation jsonb,
    projection_occurred_at timestamptz
)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    stable_event_id uuid;
    stable_event_created_at timestamptz;
    inserted_rows bigint;
BEGIN
    IF projection_event_version < 1 OR projection_event_version > 2147483647 THEN
        RAISE EXCEPTION 'timeline projection event version is outside integer range'
            USING ERRCODE = '23514';
    END IF;
    SELECT id, created_at INTO stable_event_id, stable_event_created_at
      FROM ops.knowledge_event
     WHERE workspace_id = projection_workspace_id
       AND source_event_ref = projection_source_event_ref;
    stable_event_id := COALESCE(stable_event_id, gen_random_uuid());
    stable_event_created_at := COALESCE(
        stable_event_created_at,
        GREATEST(projection_occurred_at, CURRENT_TIMESTAMP)
    );

    INSERT INTO ops.timeline_projection_outbox(
        event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,
        event_version,summary,correlation,occurred_at,status,created_at,updated_at
    ) VALUES(
        stable_event_id,projection_workspace_id,projection_event_type,projection_aggregate_type,
        projection_aggregate_id,projection_source_event_ref,projection_source_ref,
        projection_event_version::integer,projection_summary,COALESCE(projection_correlation, '{}'::jsonb),
        projection_occurred_at,'PENDING',stable_event_created_at,
        GREATEST(stable_event_created_at,CURRENT_TIMESTAMP)
    )
    ON CONFLICT (workspace_id, source_event_ref) DO NOTHING;
    GET DIAGNOSTICS inserted_rows = ROW_COUNT;

    IF inserted_rows = 0 AND NOT EXISTS (
        SELECT 1
          FROM ops.timeline_projection_outbox queued
         WHERE queued.workspace_id = projection_workspace_id
           AND queued.source_event_ref = projection_source_event_ref
           AND queued.event_type = projection_event_type
           AND queued.aggregate_type = projection_aggregate_type
           AND queued.aggregate_id IS NOT DISTINCT FROM projection_aggregate_id
           AND queued.source_ref = projection_source_ref
           AND queued.event_version = projection_event_version
           AND queued.summary = projection_summary
           AND queued.correlation = COALESCE(projection_correlation, '{}'::jsonb)
           AND queued.occurred_at = projection_occurred_at
    ) THEN
        RAISE EXCEPTION 'timeline projection source is bound to different content'
            USING ERRCODE = '23514';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enqueue_knowledge_command_timeline(
    receipt core.knowledge_command_receipt
)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    projected_event_type text;
    projected_summary text;
    aggregate_status text;
    projected_correlation jsonb := '{}'::jsonb;
    relation_confirmation_ref text;
    related_proposal_id uuid;
    related_approval_id uuid;
BEGIN
    CASE receipt.command_type
        WHEN 'topic.create' THEN
            projected_event_type := 'VERSION_PUBLISHED';
            projected_summary := 'Topic version published';
        WHEN 'claim.confirm' THEN
            projected_event_type := 'VERSION_PUBLISHED';
            projected_summary := 'Claim version published';
        WHEN 'claim.transition' THEN
            SELECT status INTO aggregate_status
              FROM core.claim
             WHERE workspace_id = receipt.workspace_id AND id = receipt.aggregate_id;
            IF aggregate_status = 'CONFIRMED' THEN
                projected_event_type := 'VERSION_PUBLISHED';
            ELSIF aggregate_status IN ('SUPERSEDED', 'DEPRECATED', 'INVALID') THEN
                projected_event_type := 'VERSION_SUPERSEDED';
            ELSE
                projected_event_type := 'CORRECTIVE_EVENT';
            END IF;
            projected_summary := 'Claim status changed to ' || aggregate_status;
        WHEN 'relation.confirm' THEN
            projected_event_type := 'RELATION_CONFIRMED';
            projected_summary := 'Relation confirmed';
        WHEN 'relation.transition' THEN
            SELECT status INTO aggregate_status
              FROM core.relation
             WHERE workspace_id = receipt.workspace_id AND id = receipt.aggregate_id;
            IF aggregate_status = 'CONFIRMED' THEN
                projected_event_type := 'RELATION_CONFIRMED';
            ELSIF aggregate_status = 'DEPRECATED' THEN
                projected_event_type := 'RELATION_DEPRECATED';
            ELSE
                projected_event_type := 'CORRECTIVE_EVENT';
            END IF;
            projected_summary := 'Relation status changed to ' || aggregate_status;
        WHEN 'conflict.open' THEN
            projected_event_type := 'CONFLICT_OPENED';
            projected_summary := 'Conflict opened';
        WHEN 'conflict.transition' THEN
            SELECT status INTO aggregate_status
              FROM core.conflict
             WHERE workspace_id = receipt.workspace_id AND id = receipt.aggregate_id;
            IF aggregate_status IN ('RESOLVED', 'ACCEPTED_DIVERGENCE') THEN
                projected_event_type := 'CONFLICT_RESOLVED';
            ELSE
                projected_event_type := 'CONFLICT_TRANSITIONED';
            END IF;
            projected_summary := 'Conflict status changed to ' || aggregate_status;
        ELSE
            RETURN;
    END CASE;

    IF receipt.aggregate_type = 'RELATION' THEN
        SELECT relation.confirmation_ref
          INTO relation_confirmation_ref
          FROM core.relation relation
         WHERE relation.workspace_id = receipt.workspace_id
           AND relation.id = receipt.aggregate_id;
        IF relation_confirmation_ref IS NOT NULL THEN
            SELECT approval.proposal_id, approval.id
              INTO related_proposal_id, related_approval_id
              FROM change_control.approval approval
             WHERE approval.id::text = relation_confirmation_ref;
            IF related_approval_id IS NOT NULL THEN
                projected_correlation := jsonb_build_object(
                    'proposal_id', related_proposal_id::text,
                    'approval_id', related_approval_id::text
                );
            END IF;
        END IF;
    END IF;

    PERFORM ops.enqueue_timeline_projection(
        receipt.workspace_id,
        projected_event_type,
        receipt.aggregate_type,
        receipt.aggregate_id,
        'knowledge-command:' || receipt.command_type || ':' || receipt.aggregate_id::text || ':v' || receipt.aggregate_version::text,
        lower(receipt.aggregate_type) || ':' || receipt.aggregate_id::text,
        receipt.aggregate_version,
        projected_summary,
        projected_correlation,
        receipt.created_at
    );
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_proposal_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,'PROPOSAL_CREATED','PROPOSAL',NEW.id,
        'proposal.created:' || NEW.id::text || ':v1','proposal:' || NEW.id::text,
        1,'Proposal created',jsonb_build_object('proposal_id',NEW.id::text),NEW.created_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_approval_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    approval_workspace_id uuid;
    projected_event_type text;
BEGIN
    SELECT workspace_id INTO approval_workspace_id
      FROM change_control.proposal
     WHERE id = NEW.proposal_id;
    projected_event_type := CASE WHEN NEW.decision = 'approved' THEN 'APPROVAL_GRANTED' ELSE 'APPROVAL_REJECTED' END;
    PERFORM ops.enqueue_timeline_projection(
        approval_workspace_id,projected_event_type,'APPROVAL',NEW.id,
        'approval:' || NEW.id::text || ':v1','approval:' || NEW.id::text,
        1,CASE WHEN NEW.decision = 'approved' THEN 'Approval granted' ELSE 'Approval rejected' END,
        jsonb_build_object('proposal_id',NEW.proposal_id::text,'approval_id',NEW.id::text),NEW.decided_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_proposal_commit_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    revision_version integer;
    projected_correlation jsonb;
BEGIN
    SELECT revision_no INTO revision_version
      FROM change_control.proposal_revision
     WHERE id = NEW.revision_id AND proposal_id = NEW.proposal_id;
    projected_correlation := jsonb_build_object(
        'proposal_id',NEW.proposal_id::text,
        'approval_id',NEW.approval_id::text,
        'git_commit_ref',NEW.git_commit
    );
    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,'GIT_COMMITTED','GIT_COMMIT',NEW.id,
        'proposal-commit:' || NEW.id::text || ':v1','git_commit:' || NEW.git_commit,
        1,'Git commit created',projected_correlation,NEW.created_at
    );
    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,'VERSION_PUBLISHED','ARTICLE_REVISION',NEW.revision_id,
        'proposal-revision-published:' || NEW.id::text || ':v1','article_revision:' || NEW.revision_id::text,
        revision_version,'Proposal revision published',projected_correlation,NEW.created_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_knowledge_command_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM ops.enqueue_knowledge_command_timeline(NEW);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_health_issue_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    projected_event_type text;
    projected_source_kind text;
    projected_summary text;
    projected_occurred_at timestamptz;
BEGIN
    IF TG_OP = 'INSERT' THEN
        projected_event_type := 'HEALTH_ISSUE_DETECTED';
        projected_source_kind := 'detected';
        projected_summary := 'Health issue detected';
        projected_occurred_at := NEW.first_detected_at;
    ELSIF OLD.status IS DISTINCT FROM NEW.status AND NEW.status = 'RESOLVED' THEN
        projected_event_type := 'HEALTH_ISSUE_RESOLVED';
        projected_source_kind := 'resolved';
        projected_summary := 'Health issue resolved';
        projected_occurred_at := NEW.resolved_at;
    ELSIF OLD.status = 'RESOLVED' AND NEW.status = 'REOPENED' THEN
        projected_event_type := 'HEALTH_ISSUE_DETECTED';
        projected_source_kind := 'detected';
        projected_summary := 'Health issue detected';
        projected_occurred_at := NEW.last_detected_at;
    ELSE
        RETURN NEW;
    END IF;

    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,
        projected_event_type,
        'HEALTH_ISSUE',
        NEW.id,
        'health-issue.' || projected_source_kind || ':' || NEW.id::text || ':v' || NEW.version::text,
        'health_issue:' || NEW.id::text,
        NEW.version,
        projected_summary,
        '{}'::jsonb,
        projected_occurred_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_impact_report_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_correlation jsonb;
BEGIN
    SELECT correlation INTO source_correlation
      FROM ops.knowledge_event
     WHERE id = NEW.source_event_id AND workspace_id = NEW.workspace_id;
    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,'IMPACT_ANALYZED','IMPACT_REPORT',NEW.id,
        'impact-report:' || NEW.id::text || ':v' || NEW.version::text,'impact_report:' || NEW.id::text,
        NEW.version,'Impact analysis completed',COALESCE(source_correlation,'{}'::jsonb),NEW.generated_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS timeline_project_proposal_source ON change_control.proposal;
CREATE TRIGGER timeline_project_proposal_source
    AFTER INSERT ON change_control.proposal
    FOR EACH ROW EXECUTE FUNCTION ops.project_proposal_timeline_source();
DROP TRIGGER IF EXISTS timeline_project_approval_source ON change_control.approval;
CREATE TRIGGER timeline_project_approval_source
    AFTER INSERT ON change_control.approval
    FOR EACH ROW EXECUTE FUNCTION ops.project_approval_timeline_source();
DROP TRIGGER IF EXISTS timeline_project_proposal_commit_source ON change_control.proposal_commit;
CREATE TRIGGER timeline_project_proposal_commit_source
    AFTER INSERT ON change_control.proposal_commit
    FOR EACH ROW EXECUTE FUNCTION ops.project_proposal_commit_timeline_source();
DROP TRIGGER IF EXISTS timeline_project_knowledge_command_source ON core.knowledge_command_receipt;
CREATE TRIGGER timeline_project_knowledge_command_source
    AFTER INSERT ON core.knowledge_command_receipt
    FOR EACH ROW EXECUTE FUNCTION ops.project_knowledge_command_timeline_source();
DROP TRIGGER IF EXISTS timeline_project_health_issue_source ON ops.health_issue;
CREATE TRIGGER timeline_project_health_issue_source
    AFTER INSERT OR UPDATE OF status ON ops.health_issue
    FOR EACH ROW EXECUTE FUNCTION ops.project_health_issue_timeline_source();
DROP TRIGGER IF EXISTS timeline_project_impact_report_source ON ops.impact_report;
CREATE TRIGGER timeline_project_impact_report_source
    AFTER INSERT ON ops.impact_report
    FOR EACH ROW EXECUTE FUNCTION ops.project_impact_report_timeline_source();

-- Backfill formal facts that predate the projector. Exact source identities
-- make the statements replay-safe and preserve existing Timeline rows.
SELECT ops.enqueue_timeline_projection(
    proposal.workspace_id,'PROPOSAL_CREATED','PROPOSAL',proposal.id,
    'proposal.created:' || proposal.id::text || ':v1','proposal:' || proposal.id::text,
    1,'Proposal created',jsonb_build_object('proposal_id',proposal.id::text),proposal.created_at
)
FROM change_control.proposal proposal;

SELECT ops.enqueue_timeline_projection(
    proposal.workspace_id,
    CASE WHEN approval.decision = 'approved' THEN 'APPROVAL_GRANTED' ELSE 'APPROVAL_REJECTED' END,
    'APPROVAL',approval.id,'approval:' || approval.id::text || ':v1','approval:' || approval.id::text,
    1,CASE WHEN approval.decision = 'approved' THEN 'Approval granted' ELSE 'Approval rejected' END,
    jsonb_build_object('proposal_id',approval.proposal_id::text,'approval_id',approval.id::text),approval.decided_at
)
FROM change_control.approval approval
JOIN change_control.proposal proposal ON proposal.id = approval.proposal_id;

SELECT ops.enqueue_timeline_projection(
    commit_mapping.workspace_id,'GIT_COMMITTED','GIT_COMMIT',commit_mapping.id,
    'proposal-commit:' || commit_mapping.id::text || ':v1','git_commit:' || commit_mapping.git_commit,
    1,'Git commit created',jsonb_build_object(
        'proposal_id',commit_mapping.proposal_id::text,
        'approval_id',commit_mapping.approval_id::text,
        'git_commit_ref',commit_mapping.git_commit
    ),commit_mapping.created_at
)
FROM change_control.proposal_commit commit_mapping;

SELECT ops.enqueue_timeline_projection(
    commit_mapping.workspace_id,'VERSION_PUBLISHED','ARTICLE_REVISION',commit_mapping.revision_id,
    'proposal-revision-published:' || commit_mapping.id::text || ':v1','article_revision:' || commit_mapping.revision_id::text,
    revision.revision_no,'Proposal revision published',jsonb_build_object(
        'proposal_id',commit_mapping.proposal_id::text,
        'approval_id',commit_mapping.approval_id::text,
        'git_commit_ref',commit_mapping.git_commit
    ),commit_mapping.created_at
)
FROM change_control.proposal_commit commit_mapping
JOIN change_control.proposal_revision revision
  ON revision.id = commit_mapping.revision_id AND revision.proposal_id = commit_mapping.proposal_id;

SELECT ops.enqueue_knowledge_command_timeline(receipt)
FROM core.knowledge_command_receipt receipt;

SELECT ops.enqueue_timeline_projection(
    issue.workspace_id,'HEALTH_ISSUE_DETECTED','HEALTH_ISSUE',issue.id,
    'health-issue.detected:' || issue.id::text || ':v1','health_issue:' || issue.id::text,
    1,'Health issue detected','{}'::jsonb,issue.first_detected_at
)
FROM ops.health_issue issue;

SELECT ops.enqueue_timeline_projection(
    issue.workspace_id,'HEALTH_ISSUE_RESOLVED','HEALTH_ISSUE',issue.id,
    'health-issue.resolved:' || issue.id::text || ':v' || issue.version::text,'health_issue:' || issue.id::text,
    issue.version,'Health issue resolved','{}'::jsonb,issue.resolved_at
)
FROM ops.health_issue issue
WHERE issue.status = 'RESOLVED';

SELECT ops.enqueue_timeline_projection(
    issue.workspace_id,'HEALTH_ISSUE_DETECTED','HEALTH_ISSUE',issue.id,
    'health-issue.detected:' || issue.id::text || ':v' || issue.version::text,'health_issue:' || issue.id::text,
    issue.version,'Health issue detected','{}'::jsonb,issue.last_detected_at
)
FROM ops.health_issue issue
WHERE issue.status = 'REOPENED'
  AND issue.version > 1;

SELECT ops.enqueue_timeline_projection(
    report.workspace_id,'IMPACT_ANALYZED','IMPACT_REPORT',report.id,
    'impact-report:' || report.id::text || ':v' || report.version::text,'impact_report:' || report.id::text,
    report.version,'Impact analysis completed',event.correlation,report.generated_at
)
FROM ops.impact_report report
JOIN ops.knowledge_event event
  ON event.id = report.source_event_id AND event.workspace_id = report.workspace_id;

INSERT INTO core.schema_meta(key, value)
VALUES ('timeline_impact', 'm7-04')
ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    LOCK TABLE ops.impact_report IN ACCESS EXCLUSIVE MODE;
    LOCK TABLE ops.knowledge_event IN ACCESS EXCLUSIVE MODE;
    LOCK TABLE ops.timeline_projection_outbox IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM ops.impact_report)
       OR EXISTS (SELECT 1 FROM ops.knowledge_event)
       OR EXISTS (SELECT 1 FROM ops.timeline_projection_outbox) THEN
        RAISE EXCEPTION 'cannot downgrade Timeline/Impact while projection data exists'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

DELETE FROM core.schema_meta WHERE key = 'timeline_impact';

DROP TRIGGER IF EXISTS timeline_project_impact_report_source ON ops.impact_report;
DROP TRIGGER IF EXISTS timeline_project_health_issue_source ON ops.health_issue;
DROP TRIGGER IF EXISTS timeline_project_knowledge_command_source ON core.knowledge_command_receipt;
DROP TRIGGER IF EXISTS timeline_project_proposal_commit_source ON change_control.proposal_commit;
DROP TRIGGER IF EXISTS timeline_project_approval_source ON change_control.approval;
DROP TRIGGER IF EXISTS timeline_project_proposal_source ON change_control.proposal;
DROP FUNCTION IF EXISTS ops.project_impact_report_timeline_source();
DROP FUNCTION IF EXISTS ops.project_health_issue_timeline_source();
DROP FUNCTION IF EXISTS ops.project_knowledge_command_timeline_source();
DROP FUNCTION IF EXISTS ops.project_proposal_commit_timeline_source();
DROP FUNCTION IF EXISTS ops.project_approval_timeline_source();
DROP FUNCTION IF EXISTS ops.project_proposal_timeline_source();
DROP FUNCTION IF EXISTS ops.enqueue_knowledge_command_timeline(core.knowledge_command_receipt);
DROP FUNCTION IF EXISTS ops.enqueue_timeline_projection(uuid,text,text,uuid,text,text,bigint,text,jsonb,timestamptz);
DROP INDEX IF EXISTS ops.idx_ops_timeline_projection_pending;
DROP TRIGGER IF EXISTS timeline_projection_outbox_guard ON ops.timeline_projection_outbox;
DROP TABLE IF EXISTS ops.timeline_projection_outbox;
DROP FUNCTION IF EXISTS ops.guard_timeline_projection_outbox_mutation();

DROP INDEX IF EXISTS core.idx_knowledge_conflict_workspace_topic;
DROP INDEX IF EXISTS ops.idx_ops_impact_report_workspace_status;
DROP INDEX IF EXISTS ops.idx_ops_impact_report_workspace_time;
DROP TRIGGER IF EXISTS impact_report_append_only ON ops.impact_report;

ALTER TABLE ops.impact_report
    DROP CONSTRAINT IF EXISTS ops_impact_report_time_order,
    DROP CONSTRAINT IF EXISTS ops_impact_report_version,
    DROP CONSTRAINT IF EXISTS ops_impact_report_stale,
    DROP CONSTRAINT IF EXISTS ops_impact_report_failure,
    DROP CONSTRAINT IF EXISTS ops_impact_report_fingerprint,
    DROP CONSTRAINT IF EXISTS ops_impact_report_summary,
    DROP CONSTRAINT IF EXISTS ops_impact_report_objects,
    DROP CONSTRAINT IF EXISTS ops_impact_report_source_version,
    DROP CONSTRAINT IF EXISTS ops_impact_report_schema,
    DROP COLUMN IF EXISTS created_at,
    DROP COLUMN IF EXISTS version,
    DROP COLUMN IF EXISTS stale_reason,
    DROP COLUMN IF EXISTS error_code,
    DROP COLUMN IF EXISTS fingerprint,
    DROP COLUMN IF EXISTS source_event_version,
    DROP COLUMN IF EXISTS schema_version;

DROP INDEX IF EXISTS ops.idx_ops_knowledge_event_type_time;
DROP INDEX IF EXISTS ops.idx_ops_knowledge_event_aggregate_time;
DROP INDEX IF EXISTS ops.uq_ops_knowledge_event_source;

DROP TRIGGER IF EXISTS knowledge_event_append_only ON ops.knowledge_event;

ALTER TABLE ops.knowledge_event
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_time_order,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_correlation,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_payload_size,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_summary,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_schema,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_version,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_source_ref,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_object_ref,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_event_type,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_aggregate_type,
    ALTER COLUMN source_ref DROP NOT NULL,
    DROP COLUMN IF EXISTS correlation,
    DROP COLUMN IF EXISTS summary,
    DROP COLUMN IF EXISTS schema_version,
    DROP COLUMN IF EXISTS event_version,
    DROP COLUMN IF EXISTS source_event_ref;

CREATE TRIGGER knowledge_event_append_only
    BEFORE UPDATE OR DELETE ON ops.knowledge_event
    FOR EACH ROW EXECUTE FUNCTION ops.reject_append_only_mutation();
