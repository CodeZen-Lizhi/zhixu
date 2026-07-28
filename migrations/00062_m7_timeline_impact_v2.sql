-- +goose Up

-- M7 v2 adds owner-backed Timeline and Impact projections without rewriting
-- existing v1 events or reports. New facts remain append-only.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.valid_timeline_owner_binding(
    projected_event_type text,
    projected_aggregate_type text,
    projected_aggregate_id uuid,
    projected_owner_binding jsonb
)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
    artifact_binding jsonb;
    review_binding jsonb;
BEGIN
    IF projected_event_type = 'ARTIFACT_GENERATED' THEN
        artifact_binding := projected_owner_binding->'artifact';
        RETURN COALESCE(projected_aggregate_type = 'ARTIFACT'
           AND projected_aggregate_id IS NOT NULL
           AND jsonb_typeof(projected_owner_binding) = 'object'
           AND projected_owner_binding - 'artifact' = '{}'::jsonb
           AND jsonb_typeof(artifact_binding) = 'object'
           AND artifact_binding - ARRAY[
               'artifact_id','artifact_version','revision_id','revision_no','content_hash'
           ] = '{}'::jsonb
           AND artifact_binding->>'artifact_id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           AND (artifact_binding->>'artifact_id')::uuid = projected_aggregate_id
           AND (artifact_binding->>'artifact_version')::bigint > 0
           AND artifact_binding->>'revision_id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           AND (artifact_binding->>'revision_id')::uuid <> projected_aggregate_id
           AND (artifact_binding->>'revision_no')::bigint > 0
           AND artifact_binding->>'content_hash' ~ '^[0-9a-f]{64}$', false);
    END IF;

    IF projected_event_type = 'REVIEW_CARD_INVALIDATED' THEN
        review_binding := projected_owner_binding->'review_card';
        RETURN COALESCE(projected_aggregate_type = 'REVIEW_CARD'
           AND projected_aggregate_id IS NOT NULL
           AND jsonb_typeof(projected_owner_binding) = 'object'
           AND projected_owner_binding - 'review_card' = '{}'::jsonb
           AND jsonb_typeof(review_binding) = 'object'
           AND review_binding - ARRAY[
               'card_id','card_version','status','fingerprint','claim_id','evidence_binding_fingerprint'
           ] = '{}'::jsonb
           AND review_binding->>'card_id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           AND (review_binding->>'card_id')::uuid = projected_aggregate_id
           AND (review_binding->>'card_version')::bigint > 0
           AND review_binding->>'status' = 'INVALIDATED'
           AND review_binding->>'fingerprint' ~ '^[0-9a-f]{64}$'
           AND review_binding->>'claim_id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           AND (review_binding->>'claim_id')::uuid <> projected_aggregate_id
           AND review_binding->>'evidence_binding_fingerprint' ~ '^[0-9a-f]{64}$', false);
    END IF;

    RETURN projected_owner_binding IS NULL;
EXCEPTION
    WHEN invalid_text_representation OR numeric_value_out_of_range THEN
        RETURN false;
END
$$;
-- +goose StatementEnd

ALTER TABLE ops.knowledge_event
    ADD COLUMN IF NOT EXISTS operator_type text,
    ADD COLUMN IF NOT EXISTS operator_id uuid,
    ADD COLUMN IF NOT EXISTS owner_binding jsonb;

ALTER TABLE ops.knowledge_event
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_schema,
    ADD CONSTRAINT ops_knowledge_event_schema CHECK (
        schema_version IN ('knowledge-event/v1','knowledge-event/v2')
    ),
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_v2_wire,
    ADD CONSTRAINT ops_knowledge_event_v2_wire CHECK (
        (
            schema_version = 'knowledge-event/v1'
            AND operator_type IS NULL
            AND operator_id IS NULL
            AND owner_binding IS NULL
            AND event_type NOT IN ('ARTIFACT_GENERATED','REVIEW_CARD_INVALIDATED')
        )
        OR (
            schema_version = 'knowledge-event/v2'
            AND operator_type IS NOT NULL
            AND operator_type IN ('USER','API_TOKEN','SYSTEM','UNKNOWN')
            AND (operator_type NOT IN ('SYSTEM','UNKNOWN') OR operator_id IS NULL)
            AND ops.valid_timeline_owner_binding(event_type,aggregate_type,aggregate_id,owner_binding)
        )
    );

ALTER TABLE ops.timeline_projection_outbox
    ADD COLUMN IF NOT EXISTS schema_version text NOT NULL DEFAULT 'knowledge-event/v1',
    ADD COLUMN IF NOT EXISTS operator_type text,
    ADD COLUMN IF NOT EXISTS operator_id uuid,
    ADD COLUMN IF NOT EXISTS owner_binding jsonb;

ALTER TABLE ops.timeline_projection_outbox
    DROP CONSTRAINT IF EXISTS ops_timeline_projection_schema,
    ADD CONSTRAINT ops_timeline_projection_schema CHECK (
        schema_version IN ('knowledge-event/v1','knowledge-event/v2')
    ),
    DROP CONSTRAINT IF EXISTS ops_timeline_projection_v2_wire,
    ADD CONSTRAINT ops_timeline_projection_v2_wire CHECK (
        (
            schema_version = 'knowledge-event/v1'
            AND operator_type IS NULL
            AND operator_id IS NULL
            AND owner_binding IS NULL
            AND event_type NOT IN ('ARTIFACT_GENERATED','REVIEW_CARD_INVALIDATED')
        )
        OR (
            schema_version = 'knowledge-event/v2'
            AND operator_type IS NOT NULL
            AND operator_type IN ('USER','API_TOKEN','SYSTEM','UNKNOWN')
            AND (operator_type NOT IN ('SYSTEM','UNKNOWN') OR operator_id IS NULL)
            AND ops.valid_timeline_owner_binding(event_type,aggregate_type,aggregate_id,owner_binding)
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
        NEW.source_ref, NEW.event_version, NEW.schema_version, NEW.summary,
        NEW.correlation, NEW.operator_type, NEW.operator_id, NEW.owner_binding,
        NEW.occurred_at, NEW.created_at
    ) IS DISTINCT FROM ROW(
        OLD.id, OLD.event_id, OLD.workspace_id, OLD.event_type,
        OLD.aggregate_type, OLD.aggregate_id, OLD.source_event_ref,
        OLD.source_ref, OLD.event_version, OLD.schema_version, OLD.summary,
        OLD.correlation, OLD.operator_type, OLD.operator_id, OLD.owner_binding,
        OLD.occurred_at, OLD.created_at
    ) THEN
        RAISE EXCEPTION 'Timeline projection outbox source binding is immutable'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- New owner events use a separate v2 enqueue function, so every existing v1
-- producer keeps its original signature and payload contract.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.enqueue_timeline_projection_v2(
    projection_workspace_id uuid,
    projection_event_type text,
    projection_aggregate_type text,
    projection_aggregate_id uuid,
    projection_source_event_ref text,
    projection_source_ref text,
    projection_event_version bigint,
    projection_summary text,
    projection_correlation jsonb,
    projection_operator_type text,
    projection_operator_id uuid,
    projection_owner_binding jsonb,
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
    IF projection_operator_type IS NULL
       OR projection_operator_type NOT IN ('USER','API_TOKEN','SYSTEM','UNKNOWN')
       OR (projection_operator_type IN ('SYSTEM','UNKNOWN') AND projection_operator_id IS NOT NULL)
       OR NOT ops.valid_timeline_owner_binding(
           projection_event_type,projection_aggregate_type,projection_aggregate_id,projection_owner_binding
       ) THEN
        RAISE EXCEPTION 'timeline v2 projection binding is invalid'
            USING ERRCODE = '23514';
    END IF;

    SELECT id,created_at
      INTO stable_event_id,stable_event_created_at
      FROM ops.knowledge_event
     WHERE workspace_id=projection_workspace_id
       AND source_event_ref=projection_source_event_ref;
    stable_event_id := COALESCE(stable_event_id,gen_random_uuid());
    stable_event_created_at := COALESCE(
        stable_event_created_at,GREATEST(projection_occurred_at,CURRENT_TIMESTAMP)
    );

    INSERT INTO ops.timeline_projection_outbox(
        event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,
        event_version,schema_version,summary,correlation,operator_type,operator_id,owner_binding,
        occurred_at,status,created_at,updated_at
    ) VALUES(
        stable_event_id,projection_workspace_id,projection_event_type,projection_aggregate_type,
        projection_aggregate_id,projection_source_event_ref,projection_source_ref,
        projection_event_version::integer,'knowledge-event/v2',projection_summary,
        COALESCE(projection_correlation,'{}'::jsonb),projection_operator_type,
        projection_operator_id,projection_owner_binding,projection_occurred_at,'PENDING',
        stable_event_created_at,GREATEST(stable_event_created_at,CURRENT_TIMESTAMP)
    )
    ON CONFLICT (workspace_id,source_event_ref) DO NOTHING;
    GET DIAGNOSTICS inserted_rows = ROW_COUNT;

    IF inserted_rows = 0 AND NOT EXISTS (
        SELECT 1
          FROM ops.timeline_projection_outbox AS queued
         WHERE queued.workspace_id=projection_workspace_id
           AND queued.source_event_ref=projection_source_event_ref
           AND queued.event_type=projection_event_type
           AND queued.aggregate_type=projection_aggregate_type
           AND queued.aggregate_id IS NOT DISTINCT FROM projection_aggregate_id
           AND queued.source_ref=projection_source_ref
           AND queued.event_version=projection_event_version
           AND queued.schema_version='knowledge-event/v2'
           AND queued.summary=projection_summary
           AND queued.correlation=COALESCE(projection_correlation,'{}'::jsonb)
           AND queued.operator_type=projection_operator_type
           AND queued.operator_id IS NOT DISTINCT FROM projection_operator_id
           AND queued.owner_binding=projection_owner_binding
           AND queued.occurred_at=projection_occurred_at
    ) THEN
        RAISE EXCEPTION 'timeline projection source is bound to different content'
            USING ERRCODE = '23514';
    END IF;
END;
$$;
-- +goose StatementEnd

-- Artifact citations are projected as exact Source Version/Span tuples. The
-- immutable Artifact Revision remains the owner and contains the full citation.
CREATE TABLE IF NOT EXISTS learning.artifact_revision_citation_selector (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    artifact_id uuid NOT NULL,
    revision_id uuid NOT NULL,
    source_version_id uuid NOT NULL REFERENCES core.source_version(id) ON DELETE RESTRICT,
    source_span_id uuid NOT NULL REFERENCES ingestion.source_span(id) ON DELETE RESTRICT,
    CONSTRAINT pk_learning_artifact_revision_citation_selector
        PRIMARY KEY (workspace_id,revision_id,source_version_id,source_span_id),
    CONSTRAINT fk_learning_artifact_revision_citation_selector_revision
        FOREIGN KEY (revision_id,workspace_id,artifact_id)
        REFERENCES learning.artifact_revision(id,workspace_id,artifact_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_learning_artifact_revision_citation_selector_lookup
    ON learning.artifact_revision_citation_selector(
        workspace_id,source_version_id,source_span_id,artifact_id,revision_id
    );

CREATE INDEX IF NOT EXISTS idx_learning_artifact_revision_citation_selector_revision
    ON learning.artifact_revision_citation_selector(
        workspace_id,revision_id,artifact_id,source_version_id,source_span_id
    );

CREATE INDEX IF NOT EXISTS idx_learning_artifact_revision_backfill_seek
    ON learning.artifact_revision(workspace_id,domain_schema_version,created_at,id);

CREATE INDEX IF NOT EXISTS idx_learning_review_card_claim_impact_seek
    ON learning.review_card(workspace_id,claim_id,id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.verify_artifact_citation_selector()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_workspace_id uuid;
    span_workspace_id uuid;
    source_projection_id uuid;
    span_projection_id uuid;
BEGIN
    SELECT source.workspace_id
      INTO source_workspace_id
      FROM core.source_version AS version
      JOIN core.source AS source ON source.id=version.source_id
     WHERE version.id=NEW.source_version_id;

    SELECT span.workspace_id,span.parse_projection_id
      INTO span_workspace_id,span_projection_id
      FROM ingestion.source_span AS span
     WHERE span.id=NEW.source_span_id;

    SELECT projection.parse_projection_id
      INTO source_projection_id
      FROM ingestion.source_version_projection AS projection
     WHERE projection.source_version_id=NEW.source_version_id
       AND projection.parse_projection_id=span_projection_id;

    IF source_workspace_id IS NULL
       OR source_workspace_id <> NEW.workspace_id
       OR span_workspace_id IS NULL
       OR span_workspace_id <> NEW.workspace_id
       OR source_projection_id IS NULL THEN
        RAISE EXCEPTION 'artifact citation selector source binding mismatch'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER artifact_citation_selector_verify
    BEFORE INSERT ON learning.artifact_revision_citation_selector
    FOR EACH ROW EXECUTE FUNCTION learning.verify_artifact_citation_selector();

CREATE TRIGGER artifact_citation_selector_append_only
    BEFORE UPDATE OR DELETE ON learning.artifact_revision_citation_selector
    FOR EACH ROW EXECUTE FUNCTION ops.reject_append_only_mutation();

-- During the migration-to-binary rollout, old writers only persist the
-- immutable Revision. Project exact selectors in the same transaction until
-- every binary also performs its idempotent selector write.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.project_artifact_revision_citation_selectors()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.domain_schema_version IS DISTINCT FROM 'artifact-revision/v1' THEN
        RETURN NEW;
    END IF;

    IF jsonb_typeof(NEW.sections) IS DISTINCT FROM 'array'
       OR EXISTS (
           SELECT 1
           FROM jsonb_array_elements(NEW.sections) AS section(value)
           WHERE jsonb_typeof(section.value) IS DISTINCT FROM 'object'
              OR jsonb_typeof(section.value->'Citations') IS DISTINCT FROM 'array'
       ) THEN
        RAISE EXCEPTION 'artifact revision citation sections are invalid'
            USING ERRCODE='23514';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM jsonb_array_elements(NEW.sections) AS section(value)
        CROSS JOIN LATERAL jsonb_array_elements(section.value->'Citations') AS citation(value)
        WHERE jsonb_typeof(citation.value) IS DISTINCT FROM 'object'
           OR jsonb_typeof(citation.value->'SourceVersionID') IS DISTINCT FROM 'string'
           OR citation.value->>'SourceVersionID' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           OR jsonb_typeof(citation.value->'SourceSpanID') IS DISTINCT FROM 'string'
           OR citation.value->>'SourceSpanID' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           OR citation.value->>'SourceVersionID'=citation.value->>'SourceSpanID'
           OR citation.value->'Verified' IS DISTINCT FROM 'true'::jsonb
    ) THEN
        RAISE EXCEPTION 'artifact revision citation binding is invalid'
            USING ERRCODE='23514';
    END IF;

    INSERT INTO learning.artifact_revision_citation_selector(
        workspace_id,artifact_id,revision_id,source_version_id,source_span_id
    )
    SELECT DISTINCT NEW.workspace_id,NEW.artifact_id,NEW.id,
           (citation.value->>'SourceVersionID')::uuid,
           (citation.value->>'SourceSpanID')::uuid
    FROM jsonb_array_elements(NEW.sections) AS section(value)
    CROSS JOIN LATERAL jsonb_array_elements(section.value->'Citations') AS citation(value);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER artifact_revision_project_citation_selectors
    AFTER INSERT ON learning.artifact_revision
    FOR EACH ROW EXECUTE FUNCTION learning.project_artifact_revision_citation_selectors();

CREATE TABLE IF NOT EXISTS learning.artifact_citation_selector_backfill (
    workspace_id uuid PRIMARY KEY REFERENCES core.workspace(id) ON DELETE RESTRICT,
    contract_version text NOT NULL DEFAULT 'artifact-citation-selector-backfill/v1'
        CHECK (contract_version = 'artifact-citation-selector-backfill/v1'),
    high_water_created_at timestamptz,
    high_water_revision_id uuid,
    cursor_created_at timestamptz,
    cursor_revision_id uuid,
    expected_revision_count bigint NOT NULL CHECK (expected_revision_count >= 0),
    processed_revision_count bigint NOT NULL DEFAULT 0 CHECK (processed_revision_count >= 0),
    expected_selector_count bigint NOT NULL DEFAULT 0 CHECK (expected_selector_count >= 0),
    processed_selector_count bigint NOT NULL DEFAULT 0 CHECK (processed_selector_count >= 0),
    validation_cursor_created_at timestamptz,
    validation_cursor_revision_id uuid,
    validated_revision_count bigint NOT NULL DEFAULT 0 CHECK (validated_revision_count >= 0),
    validated_selector_count bigint NOT NULL DEFAULT 0 CHECK (validated_selector_count >= 0),
    status text NOT NULL CHECK (status IN ('PENDING','RUNNING','FAILED','COMPLETED')),
    error_code text CHECK (
        error_code IS NULL OR (
            btrim(error_code) <> '' AND error_code=btrim(error_code)
            AND octet_length(error_code) <= 256 AND error_code !~ E'[\r\n]'
        )
    ),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT learning_artifact_citation_selector_backfill_shape CHECK (
        expected_revision_count >= processed_revision_count
        AND expected_selector_count >= processed_selector_count
        AND expected_revision_count >= validated_revision_count
        AND expected_selector_count >= validated_selector_count
        AND (
            (expected_revision_count = 0 AND high_water_created_at IS NULL AND high_water_revision_id IS NULL)
            OR (expected_revision_count > 0 AND high_water_created_at IS NOT NULL AND high_water_revision_id IS NOT NULL)
        )
        AND (
            (processed_revision_count = 0 AND cursor_created_at IS NULL AND cursor_revision_id IS NULL)
            OR (processed_revision_count > 0 AND cursor_created_at IS NOT NULL AND cursor_revision_id IS NOT NULL)
        )
        AND (
            (validated_revision_count = 0 AND validation_cursor_created_at IS NULL AND validation_cursor_revision_id IS NULL)
            OR (validated_revision_count > 0 AND validation_cursor_created_at IS NOT NULL AND validation_cursor_revision_id IS NOT NULL)
        )
        AND (
            (status IN ('PENDING','RUNNING') AND error_code IS NULL AND completed_at IS NULL)
            OR (status = 'FAILED' AND error_code IS NOT NULL AND completed_at IS NULL)
            OR (
                status = 'COMPLETED' AND error_code IS NULL AND completed_at IS NOT NULL
                AND expected_revision_count=processed_revision_count
                AND expected_selector_count=processed_selector_count
                AND expected_revision_count=validated_revision_count
                AND expected_selector_count=validated_selector_count
            )
        )
        AND updated_at >= created_at
        AND (completed_at IS NULL OR completed_at >= created_at)
    )
);

CREATE INDEX IF NOT EXISTS idx_learning_artifact_citation_backfill_pending_seek
    ON learning.artifact_citation_selector_backfill((status='FAILED'),created_at,workspace_id)
    WHERE status<>'COMPLETED';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.guard_artifact_citation_selector_backfill()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' OR OLD.status = 'COMPLETED' THEN
        RAISE EXCEPTION 'artifact citation selector backfill marker is irreversible'
            USING ERRCODE = '55000';
    END IF;
    IF NEW.workspace_id <> OLD.workspace_id
       OR NEW.contract_version <> OLD.contract_version
       OR NEW.high_water_created_at IS DISTINCT FROM OLD.high_water_created_at
       OR NEW.high_water_revision_id IS DISTINCT FROM OLD.high_water_revision_id
       OR NEW.expected_revision_count <> OLD.expected_revision_count
       OR NEW.expected_selector_count < OLD.expected_selector_count
       OR NEW.processed_revision_count < OLD.processed_revision_count
       OR NEW.processed_selector_count < OLD.processed_selector_count
       OR NEW.validated_revision_count < OLD.validated_revision_count
       OR NEW.validated_selector_count < OLD.validated_selector_count
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at
       OR NOT (
           (OLD.status='PENDING' AND NEW.status IN ('RUNNING','FAILED'))
           OR (OLD.status='RUNNING' AND NEW.status IN ('RUNNING','FAILED','COMPLETED'))
           OR (OLD.status='FAILED' AND NEW.status='RUNNING')
       ) THEN
        RAISE EXCEPTION 'artifact citation selector backfill transition is invalid'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER artifact_citation_selector_backfill_guard
    BEFORE UPDATE OR DELETE ON learning.artifact_citation_selector_backfill
    FOR EACH ROW EXECUTE FUNCTION learning.guard_artifact_citation_selector_backfill();

WITH frozen AS MATERIALIZED (
    SELECT workspace.id AS workspace_id,
           high_water.created_at AS high_water_created_at,
           high_water.id AS high_water_revision_id,
           COALESCE(revision_count.value,0) AS expected_revision_count
    FROM core.workspace AS workspace
    LEFT JOIN LATERAL (
        SELECT revision.created_at,revision.id
        FROM learning.artifact_revision AS revision
        WHERE revision.workspace_id=workspace.id
          AND revision.domain_schema_version='artifact-revision/v1'
        ORDER BY revision.created_at DESC,revision.id DESC
        LIMIT 1
    ) AS high_water ON true
    LEFT JOIN LATERAL (
        SELECT count(*)::bigint AS value
        FROM learning.artifact_revision AS revision
        WHERE revision.workspace_id=workspace.id
          AND revision.domain_schema_version='artifact-revision/v1'
    ) AS revision_count ON true
)
INSERT INTO learning.artifact_citation_selector_backfill(
    workspace_id,high_water_created_at,high_water_revision_id,
    cursor_created_at,cursor_revision_id,expected_revision_count,processed_revision_count,
    expected_selector_count,processed_selector_count,status,version,created_at,updated_at,completed_at
)
SELECT frozen.workspace_id,frozen.high_water_created_at,frozen.high_water_revision_id,
       NULL,NULL,frozen.expected_revision_count,0,0,0,
       CASE WHEN frozen.expected_revision_count=0 THEN 'COMPLETED' ELSE 'PENDING' END,
       1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,
       CASE WHEN frozen.expected_revision_count=0 THEN CURRENT_TIMESTAMP ELSE NULL END
FROM frozen
ON CONFLICT (workspace_id) DO NOTHING;

-- Workspaces created after this migration have no historical Revision range;
-- synchronous selector writes cover every Revision created from that point on.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.initialize_artifact_citation_selector_backfill()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO learning.artifact_citation_selector_backfill(
        workspace_id,expected_revision_count,processed_revision_count,
        expected_selector_count,processed_selector_count,
        validated_revision_count,validated_selector_count,
        status,version,created_at,updated_at,completed_at
    ) VALUES(
        NEW.id,0,0,0,0,0,0,'COMPLETED',1,
        CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
    )
    ON CONFLICT (workspace_id) DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER artifact_citation_selector_backfill_initialize
    AFTER INSERT ON core.workspace
    FOR EACH ROW EXECUTE FUNCTION learning.initialize_artifact_citation_selector_backfill();

-- Version Impact reports by analysis policy. Existing rows are classified as
-- v1 without changing their immutable IDs, objects, fingerprints or times.
DROP TRIGGER IF EXISTS impact_report_append_only ON ops.impact_report;

ALTER TABLE ops.impact_report
    ADD COLUMN IF NOT EXISTS analysis_version text NOT NULL DEFAULT 'impact-analysis/v1',
    ADD COLUMN IF NOT EXISTS supersedes_report_id uuid;

ALTER TABLE ops.impact_report
    DROP CONSTRAINT IF EXISTS uq_ops_impact_report_source,
    DROP CONSTRAINT IF EXISTS ops_impact_report_schema,
    ADD CONSTRAINT ops_impact_report_schema CHECK (
        (schema_version='impact-report/v1' AND analysis_version='impact-analysis/v1' AND supersedes_report_id IS NULL)
        OR (schema_version='impact-report/v2' AND analysis_version='impact-analysis/v2')
    ),
    ADD CONSTRAINT uq_ops_impact_report_id_workspace_source
        UNIQUE (id,workspace_id,source_event_id),
    ADD CONSTRAINT fk_ops_impact_report_supersedes_source
        FOREIGN KEY (supersedes_report_id,workspace_id,source_event_id)
        REFERENCES ops.impact_report(id,workspace_id,source_event_id) ON DELETE RESTRICT,
    ADD CONSTRAINT ops_impact_report_not_self_superseding CHECK (
        supersedes_report_id IS NULL OR supersedes_report_id <> id
    );

CREATE UNIQUE INDEX IF NOT EXISTS uq_ops_impact_report_source_analysis
    ON ops.impact_report(workspace_id,source_event_id,analysis_version);

CREATE UNIQUE INDEX IF NOT EXISTS uq_ops_impact_report_direct_successor
    ON ops.impact_report(workspace_id,supersedes_report_id)
    WHERE supersedes_report_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_impact_report_current
    ON ops.impact_report(workspace_id,source_event_id,analysis_version,id);

CREATE TRIGGER impact_report_append_only
    BEFORE UPDATE OR DELETE ON ops.impact_report
    FOR EACH ROW EXECUTE FUNCTION ops.reject_append_only_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.review_card_evidence_binding_fingerprint(card_evidence jsonb)
RETURNS text
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT encode(sha256(convert_to(COALESCE(string_agg(
        binding.source_version_id::text || ':' || binding.source_span_id::text,
        '|' ORDER BY binding.source_version_id,binding.source_span_id
    ),''),'UTF8')),'hex')
    FROM (
        SELECT DISTINCT
               learning.try_review_evidence_uuid(item->>'source_version_id') AS source_version_id,
               learning.try_review_evidence_uuid(item->>'source_span_id') AS source_span_id
        FROM jsonb_array_elements(
            CASE WHEN jsonb_typeof(card_evidence)='array' THEN card_evidence ELSE '[]'::jsonb END
        ) AS evidence(item)
        WHERE jsonb_typeof(item)='object'
          AND item->>'schema_version'='review-evidence/v1'
        LIMIT 128
    ) AS binding
    WHERE binding.source_version_id IS NOT NULL
      AND binding.source_span_id IS NOT NULL
$$;
-- +goose StatementEnd

-- Owner transactions enqueue v2 sources only after their durable owner facts
-- exist in the same transaction.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_artifact_generated_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    revision_id uuid;
    revision_no integer;
    revision_content_hash text;
    revision_created_by_type text;
    binding jsonb;
BEGIN
    IF OLD.current_revision_id IS NOT DISTINCT FROM NEW.current_revision_id
       OR NEW.domain_schema_version <> 'artifact/v1' THEN
        RETURN NEW;
    END IF;

    SELECT candidate.id,candidate.revision_no,candidate.content_hash,candidate.created_by_type
      INTO revision_id,revision_no,revision_content_hash,revision_created_by_type
      FROM learning.artifact_revision
      AS candidate
     WHERE candidate.id=NEW.current_revision_id
       AND candidate.workspace_id=NEW.workspace_id
       AND candidate.artifact_id=NEW.id;

    IF revision_id IS NULL OR revision_created_by_type <> 'AGENT' THEN
        RETURN NEW;
    END IF;

    binding := jsonb_build_object('artifact',jsonb_build_object(
        'artifact_id',NEW.id,
        'artifact_version',NEW.version,
        'revision_id',revision_id,
        'revision_no',revision_no,
        'content_hash',revision_content_hash
    ));

    PERFORM ops.enqueue_timeline_projection_v2(
        NEW.workspace_id,'ARTIFACT_GENERATED','ARTIFACT',NEW.id,
        'artifact-generated:' || NEW.id::text || ':' || revision_id::text || ':v' || NEW.version::text,
        'artifact:' || NEW.id::text,NEW.version,
        'Artifact revision generated','{}'::jsonb,'SYSTEM',NULL,binding,NEW.updated_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER timeline_project_artifact_generated_source
    AFTER UPDATE ON learning.artifact
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ops.project_artifact_generated_timeline_source();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_review_card_invalidated_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    binding jsonb;
BEGIN
    IF OLD.status='INVALIDATED' OR NEW.status <> 'INVALIDATED' THEN
        RETURN NEW;
    END IF;
    IF NEW.claim_id IS NULL THEN
        RAISE EXCEPTION 'invalidated review card is missing its claim owner'
            USING ERRCODE = '23514';
    END IF;

    binding := jsonb_build_object('review_card',jsonb_build_object(
        'card_id',NEW.id,
        'card_version',NEW.version,
        'status',NEW.status,
        'fingerprint',NEW.fingerprint,
        'claim_id',NEW.claim_id,
        'evidence_binding_fingerprint',learning.review_card_evidence_binding_fingerprint(NEW.evidence)
    ));

    PERFORM ops.enqueue_timeline_projection_v2(
        NEW.workspace_id,'REVIEW_CARD_INVALIDATED','REVIEW_CARD',NEW.id,
        'review-card-invalidated:' || NEW.id::text || ':v' || NEW.version::text,
        'review-card:' || NEW.id::text,NEW.version,
        'Review card invalidated','{}'::jsonb,'SYSTEM',NULL,binding,NEW.updated_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER timeline_project_review_card_invalidated_source
    AFTER UPDATE OF status ON learning.review_card
    FOR EACH ROW EXECUTE FUNCTION ops.project_review_card_invalidated_timeline_source();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.valid_impact_owner_binding(
    target_type text,
    target_id uuid,
    target_version bigint,
    owner_binding jsonb
)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
    artifact_binding jsonb;
    review_binding jsonb;
BEGIN
    IF target_type='ARTIFACT' THEN
        artifact_binding := owner_binding->'artifact';
        RETURN COALESCE(target_id IS NOT NULL
           AND target_version > 0
           AND jsonb_typeof(owner_binding)='object'
           AND owner_binding - 'artifact'='{}'::jsonb
           AND jsonb_typeof(artifact_binding)='object'
           AND artifact_binding - ARRAY[
               'artifact_id','artifact_version','revision_id','revision_no','content_hash'
           ]='{}'::jsonb
           AND artifact_binding->>'artifact_id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           AND (artifact_binding->>'artifact_id')::uuid=target_id
           AND (artifact_binding->>'artifact_version')::bigint=target_version
           AND artifact_binding->>'revision_id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           AND (artifact_binding->>'revision_id')::uuid<>target_id
           AND (artifact_binding->>'revision_no')::bigint>0
           AND artifact_binding->>'content_hash' ~ '^[0-9a-f]{64}$', false);
    END IF;

    IF target_type='REVIEW_CARD' THEN
        review_binding := owner_binding->'review_card';
        RETURN COALESCE(target_id IS NOT NULL
           AND target_version > 0
           AND jsonb_typeof(owner_binding)='object'
           AND owner_binding - 'review_card'='{}'::jsonb
           AND jsonb_typeof(review_binding)='object'
           AND review_binding - ARRAY[
               'card_id','card_version','status','fingerprint','claim_id','evidence_binding_fingerprint'
           ]='{}'::jsonb
           AND review_binding->>'card_id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           AND (review_binding->>'card_id')::uuid=target_id
           AND (review_binding->>'card_version')::bigint=target_version
           AND review_binding->>'status' IN ('DRAFT','APPROVED','INVALIDATED','REJECTED')
           AND review_binding->>'fingerprint' ~ '^[0-9a-f]{64}$'
           AND review_binding->>'claim_id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           AND (review_binding->>'claim_id')::uuid<>target_id
           AND review_binding->>'evidence_binding_fingerprint' ~ '^[0-9a-f]{64}$', false);
    END IF;

    RETURN false;
EXCEPTION
    WHEN invalid_text_representation OR numeric_value_out_of_range THEN
        RETURN false;
END
$$;
-- +goose StatementEnd

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_type,
    ADD CONSTRAINT ck_proposal_type CHECK (
        proposal_type IN ('file_patch','knowledge_change','publish_artifact','downstream_update')
    );

ALTER TABLE change_control.proposal_revision
    ADD COLUMN IF NOT EXISTS downstream_workspace_id uuid,
    ADD COLUMN IF NOT EXISTS downstream_report_id uuid,
    ADD COLUMN IF NOT EXISTS downstream_analysis_version text,
    ADD COLUMN IF NOT EXISTS downstream_report_fingerprint text,
    ADD COLUMN IF NOT EXISTS downstream_source_event_id uuid,
    ADD COLUMN IF NOT EXISTS downstream_source_event_version bigint,
    ADD COLUMN IF NOT EXISTS downstream_target_type text,
    ADD COLUMN IF NOT EXISTS downstream_target_id uuid,
    ADD COLUMN IF NOT EXISTS downstream_base_version bigint,
    ADD COLUMN IF NOT EXISTS downstream_action text,
    ADD COLUMN IF NOT EXISTS downstream_owner_binding jsonb,
    ADD COLUMN IF NOT EXISTS downstream_reason text;

ALTER TABLE change_control.proposal_revision
    ADD CONSTRAINT ck_proposal_revision_downstream_report_fingerprint CHECK (
        downstream_report_fingerprint IS NULL OR downstream_report_fingerprint ~ '^[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT ck_proposal_revision_downstream_source_version CHECK (
        downstream_source_event_version IS NULL OR downstream_source_event_version > 0
    ),
    ADD CONSTRAINT ck_proposal_revision_downstream_base_version CHECK (
        downstream_base_version IS NULL OR downstream_base_version > 0
    ),
    ADD CONSTRAINT ck_proposal_revision_downstream_reason CHECK (
        downstream_reason IS NULL OR (
            btrim(downstream_reason) <> '' AND downstream_reason=btrim(downstream_reason)
            AND octet_length(downstream_reason) <= 4096 AND downstream_reason !~ E'[\r\n]'
        )
    ),
    ADD CONSTRAINT fk_proposal_revision_downstream_report
        FOREIGN KEY (downstream_report_id,downstream_workspace_id,downstream_source_event_id)
        REFERENCES ops.impact_report(id,workspace_id,source_event_id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_proposal_revision_downstream_event
        FOREIGN KEY (downstream_source_event_id,downstream_workspace_id)
        REFERENCES ops.knowledge_event(id,workspace_id) ON DELETE RESTRICT;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.validate_proposal_revision_payload()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_type_value text;
    proposal_workspace_id uuid;
    proposal_risk_level text;
    downstream_fields_present boolean;
BEGIN
    SELECT proposal_type,workspace_id,risk_level
      INTO proposal_type_value,proposal_workspace_id,proposal_risk_level
      FROM change_control.proposal
     WHERE id=NEW.proposal_id
     FOR SHARE;

    IF proposal_type_value IS NULL THEN
        RETURN NEW;
    END IF;

    downstream_fields_present := NEW.downstream_workspace_id IS NOT NULL
        OR NEW.downstream_report_id IS NOT NULL
        OR NEW.downstream_analysis_version IS NOT NULL
        OR NEW.downstream_report_fingerprint IS NOT NULL
        OR NEW.downstream_source_event_id IS NOT NULL
        OR NEW.downstream_source_event_version IS NOT NULL
        OR NEW.downstream_target_type IS NOT NULL
        OR NEW.downstream_target_id IS NOT NULL
        OR NEW.downstream_base_version IS NOT NULL
        OR NEW.downstream_action IS NOT NULL
        OR NEW.downstream_owner_binding IS NOT NULL
        OR NEW.downstream_reason IS NOT NULL;

    IF proposal_type_value='file_patch' THEN
        IF NEW.target_path IS NULL
           OR NEW.base_hash IS NULL
           OR NEW.content IS NULL
           OR NEW.evidence_summary IS NULL
           OR NEW.target_refs IS NOT NULL
           OR NEW.base_versions IS NOT NULL
           OR NEW.change_set IS NOT NULL
           OR NEW.evidence_refs IS NOT NULL
           OR NEW.artifact_id IS NOT NULL
           OR NEW.artifact_revision_id IS NOT NULL
           OR NEW.artifact_revision_no IS NOT NULL
           OR NEW.artifact_version IS NOT NULL
           OR NEW.artifact_content_hash IS NOT NULL
           OR NEW.artifact_source_coverage IS NOT NULL
           OR downstream_fields_present
           OR NEW.schema_version IS NOT NULL THEN
            RAISE EXCEPTION 'file patch proposal revision requires file fields only'
                USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF proposal_type_value='knowledge_change' THEN
        IF NEW.target_path IS NOT NULL
           OR NEW.base_hash IS NOT NULL
           OR NEW.content IS NOT NULL
           OR NEW.evidence_summary IS NOT NULL
           OR NEW.target_refs IS NULL
           OR NEW.base_versions IS NULL
           OR NEW.change_set IS NULL
           OR NEW.evidence_refs IS NULL
           OR NEW.artifact_id IS NOT NULL
           OR NEW.artifact_revision_id IS NOT NULL
           OR NEW.artifact_revision_no IS NOT NULL
           OR NEW.artifact_version IS NOT NULL
           OR NEW.artifact_content_hash IS NOT NULL
           OR NEW.artifact_source_coverage IS NOT NULL
           OR downstream_fields_present
           OR NEW.schema_version IS DISTINCT FROM 'knowledge-relation-change/v1' THEN
            RAISE EXCEPTION 'knowledge change proposal revision requires typed fields only'
                USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF proposal_type_value='publish_artifact' THEN
        IF NEW.target_path IS NOT NULL
           OR NEW.base_hash IS NOT NULL
           OR NEW.content IS NOT NULL
           OR NEW.evidence_summary IS NOT NULL
           OR NEW.target_refs IS NOT NULL
           OR NEW.base_versions IS NOT NULL
           OR NEW.change_set IS NOT NULL
           OR NEW.evidence_refs IS NOT NULL
           OR NEW.artifact_id IS NULL
           OR NEW.artifact_revision_id IS NULL
           OR NEW.artifact_revision_no IS NULL
           OR NEW.artifact_version IS NULL
           OR NEW.artifact_content_hash IS NULL
           OR NEW.artifact_source_coverage IS NULL
           OR downstream_fields_present
           OR NEW.schema_version IS DISTINCT FROM 'artifact-publication/v1' THEN
            RAISE EXCEPTION 'publish artifact proposal revision requires frozen artifact fields only'
                USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF proposal_type_value <> 'downstream_update'
       OR NEW.target_path IS NOT NULL
       OR NEW.base_hash IS NOT NULL
       OR NEW.content IS NOT NULL
       OR NEW.evidence_summary IS NOT NULL
       OR NEW.target_refs IS NOT NULL
       OR NEW.base_versions IS NOT NULL
       OR NEW.change_set IS NOT NULL
       OR NEW.evidence_refs IS NOT NULL
       OR NEW.artifact_id IS NOT NULL
       OR NEW.artifact_revision_id IS NOT NULL
       OR NEW.artifact_revision_no IS NOT NULL
       OR NEW.artifact_version IS NOT NULL
       OR NEW.artifact_content_hash IS NOT NULL
       OR NEW.artifact_source_coverage IS NOT NULL
       OR NEW.downstream_workspace_id IS NULL
       OR NEW.downstream_report_id IS NULL
       OR NEW.downstream_analysis_version IS DISTINCT FROM 'impact-analysis/v2'
       OR NEW.downstream_report_fingerprint IS NULL
       OR NEW.downstream_source_event_id IS NULL
       OR NEW.downstream_source_event_version IS NULL
       OR NEW.downstream_target_type IS NULL
       OR NEW.downstream_target_type NOT IN ('ARTIFACT','REVIEW_CARD')
       OR NEW.downstream_target_id IS NULL
       OR NEW.downstream_base_version IS NULL
       OR NEW.downstream_action IS NULL
       OR NEW.downstream_action NOT IN ('REGENERATE_ARTIFACT','REVALIDATE_REVIEW_CARD')
       OR NEW.downstream_owner_binding IS NULL
       OR NEW.downstream_reason IS NULL
       OR NEW.schema_version IS DISTINCT FROM 'impact-downstream-update/v1'
       OR proposal_workspace_id IS DISTINCT FROM NEW.downstream_workspace_id
       OR proposal_risk_level IS DISTINCT FROM 'HIGH'
       OR (NEW.downstream_target_type='ARTIFACT' AND NEW.downstream_action IS DISTINCT FROM 'REGENERATE_ARTIFACT')
       OR (NEW.downstream_target_type='REVIEW_CARD' AND NEW.downstream_action IS DISTINCT FROM 'REVALIDATE_REVIEW_CARD')
       OR NOT COALESCE(ops.valid_impact_owner_binding(
           NEW.downstream_target_type,NEW.downstream_target_id,
           NEW.downstream_base_version,NEW.downstream_owner_binding
       ),false) THEN
        RAISE EXCEPTION 'downstream update proposal revision requires frozen owner fields only'
            USING ERRCODE='23514';
    END IF;

    PERFORM report.id
        FROM ops.impact_report AS report
        WHERE report.id=NEW.downstream_report_id
          AND report.workspace_id=NEW.downstream_workspace_id
          AND report.source_event_id=NEW.downstream_source_event_id
          AND report.source_event_version=NEW.downstream_source_event_version
          AND report.analysis_version=NEW.downstream_analysis_version
          AND report.fingerprint=NEW.downstream_report_fingerprint
          AND report.schema_version='impact-report/v2'
          AND report.status='READY'
          AND NOT EXISTS (
              SELECT 1
              FROM ops.impact_report AS successor
              WHERE successor.workspace_id=report.workspace_id
                AND successor.supersedes_report_id=report.id
          )
          AND (
              SELECT count(*)
              FROM jsonb_array_elements(report.objects) AS candidate(value)
              WHERE candidate.value->>'type'=NEW.downstream_target_type
                AND candidate.value->>'id'=NEW.downstream_target_id::text
          )=1
          AND EXISTS (
              SELECT 1
              FROM jsonb_array_elements(report.objects) AS candidate(value)
              WHERE jsonb_typeof(candidate.value)='object'
                AND candidate.value->>'type'=NEW.downstream_target_type
                AND candidate.value->>'id'=NEW.downstream_target_id::text
                AND candidate.value->>'workspace_id'=NEW.downstream_workspace_id::text
                AND candidate.value->'version'=to_jsonb(NEW.downstream_base_version)
                AND candidate.value->>'action'=NEW.downstream_action
                AND candidate.value->'reason'=to_jsonb(NEW.downstream_reason)
                AND candidate.value->'requires_proposal'='true'::jsonb
                AND (
                    (
                        NEW.downstream_target_type='ARTIFACT'
                        AND candidate.value - ARRAY[
                            'type','id','workspace_id','version','action','reason',
                            'requires_proposal','artifact_binding'
                        ]='{}'::jsonb
                        AND candidate.value->'artifact_binding'=NEW.downstream_owner_binding->'artifact'
                    )
                    OR (
                        NEW.downstream_target_type='REVIEW_CARD'
                        AND candidate.value - ARRAY[
                            'type','id','workspace_id','version','action','reason',
                            'requires_proposal','review_card_binding'
                        ]='{}'::jsonb
                        AND candidate.value->'review_card_binding'=NEW.downstream_owner_binding->'review_card'
                    )
                )
          )
        FOR SHARE OF report;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'downstream update report binding is not current and ready'
            USING ERRCODE='23514';
    END IF;

    IF NEW.downstream_target_type='ARTIFACT' THEN
        PERFORM artifact.id
        FROM learning.artifact AS artifact
        JOIN learning.artifact_revision AS revision
          ON revision.id=artifact.current_revision_id
         AND revision.workspace_id=artifact.workspace_id
         AND revision.artifact_id=artifact.id
         AND revision.domain_schema_version='artifact-revision/v1'
        WHERE artifact.workspace_id=NEW.downstream_workspace_id
          AND artifact.id=NEW.downstream_target_id
          AND artifact.domain_schema_version='artifact/v1'
          AND artifact.version=NEW.downstream_base_version
          AND artifact.current_revision_id::text=NEW.downstream_owner_binding->'artifact'->>'revision_id'
          AND revision.revision_no=(NEW.downstream_owner_binding->'artifact'->>'revision_no')::bigint
          AND revision.content_hash=NEW.downstream_owner_binding->'artifact'->>'content_hash'
        FOR SHARE OF artifact,revision;
    ELSE
        PERFORM card.id
        FROM learning.review_card AS card
        WHERE card.workspace_id=NEW.downstream_workspace_id
          AND card.id=NEW.downstream_target_id
          AND card.version=NEW.downstream_base_version
          AND card.status=NEW.downstream_owner_binding->'review_card'->>'status'
          AND card.fingerprint=NEW.downstream_owner_binding->'review_card'->>'fingerprint'
          AND card.claim_id::text=NEW.downstream_owner_binding->'review_card'->>'claim_id'
          AND learning.review_card_evidence_binding_fingerprint(card.evidence)=
              NEW.downstream_owner_binding->'review_card'->>'evidence_binding_fingerprint'
        FOR SHARE OF card;
    END IF;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'downstream update owner binding is stale'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- A downstream update is an approval-only intent. It can never acquire a
-- Workflow binding or transition into any apply/verify/completed state.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    workflow_binding_added boolean;
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 THEN
            RAISE EXCEPTION 'proposal must start at version one' USING ERRCODE='23514';
        END IF;
        IF NEW.proposal_type='publish_artifact' AND NEW.workflow_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'publish artifact proposal cannot bind a writeback workflow' USING ERRCODE='23514';
        END IF;
        IF NEW.proposal_type='downstream_update'
           AND (NEW.workflow_run_id IS NOT NULL OR NEW.status<>'ready_for_review' OR NEW.risk_level<>'HIGH') THEN
            RAISE EXCEPTION 'downstream update proposal must start approval-only' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id<>OLD.id
       OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.idempotency_key<>OLD.idempotency_key
       OR NEW.request_hash<>OLD.request_hash
       OR NEW.proposal_type<>OLD.proposal_type
       OR NEW.risk_level<>OLD.risk_level
       OR NEW.created_at<>OLD.created_at
       OR NEW.updated_at<OLD.updated_at
       OR NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'proposal immutable field or version violation' USING ERRCODE='23514';
    END IF;

    IF NEW.proposal_type='publish_artifact' AND NEW.workflow_run_id IS NOT NULL THEN
        RAISE EXCEPTION 'publish artifact proposal cannot bind a writeback workflow' USING ERRCODE='23514';
    END IF;
    IF NEW.proposal_type='downstream_update' THEN
        IF NEW.workflow_run_id IS NOT NULL
           OR NOT (OLD.status='ready_for_review' AND NEW.status IN ('approved','rejected')) THEN
            RAISE EXCEPTION 'downstream update apply is unavailable' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    workflow_binding_added := OLD.workflow_run_id IS NULL AND NEW.workflow_run_id IS NOT NULL;
    IF workflow_binding_added
       AND NEW.status='approved'
       AND OLD.status IN ('ready_for_review','approved') THEN
        RETURN NEW;
    END IF;

    IF NOT (
        (OLD.status='ready_for_review' AND NEW.status IN ('approved','rejected','needs_revision'))
        OR (OLD.status='approved' AND NEW.status IN ('applying','needs_revision'))
        OR (OLD.status='applying' AND NEW.status IN ('applied','apply_failed','needs_revision'))
        OR (OLD.status='applied' AND NEW.status='verifying')
        OR (OLD.status='verifying' AND NEW.status IN ('completed','verify_failed','rolled_back'))
        OR (OLD.status='verify_failed' AND NEW.status IN ('verifying','rolled_back'))
        OR (OLD.status='apply_failed' AND NEW.status IN ('applying','rolled_back'))
        OR (OLD.status='needs_revision' AND NEW.status IN ('draft','cancelled'))
    ) THEN
        RAISE EXCEPTION 'proposal status transition is not allowed' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.validate_typed_proposal_approval()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_type_value text;
BEGIN
    SELECT proposal_type
      INTO proposal_type_value
      FROM change_control.proposal
     WHERE id=NEW.proposal_id
     FOR SHARE;

    IF proposal_type_value IN ('knowledge_change','publish_artifact','downstream_update')
       AND NEW.approved_git_head IS NOT NULL THEN
        RAISE EXCEPTION 'typed proposal approval cannot carry a git baseline'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.guard_proposal_workflow_run_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_workspace_id uuid;
BEGIN
    IF NEW.proposal_type='downstream_update' AND NEW.workflow_run_id IS NOT NULL THEN
        RAISE EXCEPTION 'downstream update proposal cannot bind a workflow run'
            USING ERRCODE='23514';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.workflow_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'proposal workflow run must be bound after proposal creation'
                USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.workflow_run_id IS NOT NULL
       AND NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id THEN
        RAISE EXCEPTION 'proposal workflow run binding is immutable' USING ERRCODE='55000';
    END IF;
    IF OLD.workflow_run_id IS NULL AND NEW.workflow_run_id IS NOT NULL THEN
        IF NEW.status<>'approved' OR OLD.status NOT IN ('ready_for_review','approved') THEN
            RAISE EXCEPTION 'only an approved proposal can bind a workflow run' USING ERRCODE='23514';
        END IF;
        SELECT workspace_id INTO run_workspace_id FROM workflow.run WHERE id=NEW.workflow_run_id;
        IF FOUND AND run_workspace_id<>NEW.workspace_id THEN
            RAISE EXCEPTION 'proposal and workflow run must belong to the same workspace'
                USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.reject_downstream_update_execution()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM change_control.proposal
        WHERE id=NEW.proposal_id AND proposal_type='downstream_update'
    ) THEN
        RAISE EXCEPTION 'downstream update apply is unavailable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tool_authorization_reject_downstream_update
    BEFORE INSERT ON change_control.tool_authorization
    FOR EACH ROW EXECUTE FUNCTION change_control.reject_downstream_update_execution();

CREATE TRIGGER writeback_execution_reject_downstream_update
    BEFORE INSERT ON change_control.writeback_execution
    FOR EACH ROW EXECUTE FUNCTION change_control.reject_downstream_update_execution();

INSERT INTO core.schema_meta(key,value)
VALUES ('timeline_impact','m7-v2')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    LOCK TABLE ops.impact_report IN ACCESS EXCLUSIVE MODE;
    LOCK TABLE ops.knowledge_event IN ACCESS EXCLUSIVE MODE;
    LOCK TABLE ops.timeline_projection_outbox IN ACCESS EXCLUSIVE MODE;
    LOCK TABLE learning.artifact_revision_citation_selector IN ACCESS EXCLUSIVE MODE;
    LOCK TABLE learning.artifact_citation_selector_backfill IN ACCESS EXCLUSIVE MODE;
    LOCK TABLE change_control.proposal IN ACCESS EXCLUSIVE MODE;

    IF EXISTS (
        SELECT 1 FROM ops.impact_report
        WHERE schema_version='impact-report/v2'
           OR analysis_version<>'impact-analysis/v1'
           OR supersedes_report_id IS NOT NULL
    )
       OR EXISTS (SELECT 1 FROM learning.artifact_revision_citation_selector)
       OR EXISTS (
           SELECT 1
           FROM learning.artifact_citation_selector_backfill
           WHERE expected_revision_count <> 0
              OR processed_revision_count <> 0
              OR expected_selector_count <> 0
              OR processed_selector_count <> 0
              OR validated_revision_count <> 0
              OR validated_selector_count <> 0
              OR high_water_created_at IS NOT NULL
              OR high_water_revision_id IS NOT NULL
              OR cursor_created_at IS NOT NULL
              OR cursor_revision_id IS NOT NULL
              OR validation_cursor_created_at IS NOT NULL
              OR validation_cursor_revision_id IS NOT NULL
              OR status <> 'COMPLETED'
              OR error_code IS NOT NULL
              OR version <> 1
       )
       OR EXISTS (
           SELECT 1 FROM ops.knowledge_event
           WHERE schema_version='knowledge-event/v2'
              OR event_type IN ('ARTIFACT_GENERATED','REVIEW_CARD_INVALIDATED')
       )
       OR EXISTS (
           SELECT 1 FROM ops.timeline_projection_outbox
           WHERE schema_version='knowledge-event/v2'
              OR event_type IN ('ARTIFACT_GENERATED','REVIEW_CARD_INVALIDATED')
       )
       OR EXISTS (
           SELECT 1 FROM change_control.proposal
           WHERE proposal_type='downstream_update'
       ) THEN
        RAISE EXCEPTION 'cannot downgrade Timeline/Impact v2 while new facts exist'
            USING ERRCODE='55000';
    END IF;
END
$$;
-- +goose StatementEnd

UPDATE core.schema_meta
SET value='m7-04',updated_at=now()
WHERE key='timeline_impact';

DROP TRIGGER IF EXISTS writeback_execution_reject_downstream_update
    ON change_control.writeback_execution;
DROP TRIGGER IF EXISTS tool_authorization_reject_downstream_update
    ON change_control.tool_authorization;
DROP FUNCTION IF EXISTS change_control.reject_downstream_update_execution();

-- Restore the proposal workflow guard from 00013.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.guard_proposal_workflow_run_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_workspace_id uuid;
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.workflow_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'proposal workflow run must be bound after proposal creation'
                USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.workflow_run_id IS NOT NULL
       AND NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id THEN
        RAISE EXCEPTION 'proposal workflow run binding is immutable' USING ERRCODE='55000';
    END IF;

    IF OLD.workflow_run_id IS NULL AND NEW.workflow_run_id IS NOT NULL THEN
        IF NEW.status<>'approved' OR OLD.status NOT IN ('ready_for_review','approved') THEN
            RAISE EXCEPTION 'only an approved proposal can bind a workflow run' USING ERRCODE='23514';
        END IF;

        SELECT workspace_id
          INTO run_workspace_id
          FROM workflow.run
         WHERE id=NEW.workflow_run_id;

        IF FOUND AND run_workspace_id<>NEW.workspace_id THEN
            RAISE EXCEPTION 'proposal and workflow run must belong to the same workspace'
                USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Restore the typed approval and transition contracts from 00040.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.validate_typed_proposal_approval()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_type_value text;
BEGIN
    SELECT proposal_type
      INTO proposal_type_value
      FROM change_control.proposal
     WHERE id=NEW.proposal_id
     FOR SHARE;

    IF proposal_type_value IN ('knowledge_change','publish_artifact')
       AND NEW.approved_git_head IS NOT NULL THEN
        RAISE EXCEPTION 'typed proposal approval cannot carry a git baseline'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    workflow_binding_added boolean;
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 THEN
            RAISE EXCEPTION 'proposal must start at version one' USING ERRCODE='23514';
        END IF;
        IF NEW.proposal_type='publish_artifact' AND NEW.workflow_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'publish artifact proposal cannot bind a writeback workflow' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id<>OLD.id
       OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.idempotency_key<>OLD.idempotency_key
       OR NEW.request_hash<>OLD.request_hash
       OR NEW.proposal_type<>OLD.proposal_type
       OR NEW.risk_level<>OLD.risk_level
       OR NEW.created_at<>OLD.created_at
       OR NEW.updated_at<OLD.updated_at
       OR NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'proposal immutable field or version violation' USING ERRCODE='23514';
    END IF;
    IF NEW.proposal_type='publish_artifact' AND NEW.workflow_run_id IS NOT NULL THEN
        RAISE EXCEPTION 'publish artifact proposal cannot bind a writeback workflow' USING ERRCODE='23514';
    END IF;

    workflow_binding_added := OLD.workflow_run_id IS NULL AND NEW.workflow_run_id IS NOT NULL;
    IF workflow_binding_added
       AND NEW.status='approved'
       AND OLD.status IN ('ready_for_review','approved') THEN
        RETURN NEW;
    END IF;

    IF NOT (
        (OLD.status='ready_for_review' AND NEW.status IN ('approved','rejected','needs_revision'))
        OR (OLD.status='approved' AND NEW.status IN ('applying','needs_revision'))
        OR (OLD.status='applying' AND NEW.status IN ('applied','apply_failed','needs_revision'))
        OR (OLD.status='applied' AND NEW.status='verifying')
        OR (OLD.status='verifying' AND NEW.status IN ('completed','verify_failed','rolled_back'))
        OR (OLD.status='verify_failed' AND NEW.status IN ('verifying','rolled_back'))
        OR (OLD.status='apply_failed' AND NEW.status IN ('applying','rolled_back'))
        OR (OLD.status='needs_revision' AND NEW.status IN ('draft','cancelled'))
    ) THEN
        RAISE EXCEPTION 'proposal status transition is not allowed' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS proposal_revision_validate_payload
    ON change_control.proposal_revision;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.validate_proposal_revision_payload()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_type_value text;
BEGIN
    SELECT proposal_type
      INTO proposal_type_value
      FROM change_control.proposal
     WHERE id=NEW.proposal_id
     FOR SHARE;

    IF proposal_type_value IS NULL THEN
        RETURN NEW;
    END IF;

    IF proposal_type_value='file_patch' THEN
        IF NEW.target_path IS NULL
           OR NEW.base_hash IS NULL
           OR NEW.content IS NULL
           OR NEW.evidence_summary IS NULL
           OR NEW.target_refs IS NOT NULL
           OR NEW.base_versions IS NOT NULL
           OR NEW.change_set IS NOT NULL
           OR NEW.evidence_refs IS NOT NULL
           OR NEW.artifact_id IS NOT NULL
           OR NEW.artifact_revision_id IS NOT NULL
           OR NEW.artifact_revision_no IS NOT NULL
           OR NEW.artifact_version IS NOT NULL
           OR NEW.artifact_content_hash IS NOT NULL
           OR NEW.artifact_source_coverage IS NOT NULL
           OR NEW.schema_version IS NOT NULL THEN
            RAISE EXCEPTION 'file patch proposal revision requires file fields only'
                USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF proposal_type_value='knowledge_change' THEN
        IF NEW.target_path IS NOT NULL
           OR NEW.base_hash IS NOT NULL
           OR NEW.content IS NOT NULL
           OR NEW.evidence_summary IS NOT NULL
           OR NEW.target_refs IS NULL
           OR NEW.base_versions IS NULL
           OR NEW.change_set IS NULL
           OR NEW.evidence_refs IS NULL
           OR NEW.artifact_id IS NOT NULL
           OR NEW.artifact_revision_id IS NOT NULL
           OR NEW.artifact_revision_no IS NOT NULL
           OR NEW.artifact_version IS NOT NULL
           OR NEW.artifact_content_hash IS NOT NULL
           OR NEW.artifact_source_coverage IS NOT NULL
           OR NEW.schema_version IS DISTINCT FROM 'knowledge-relation-change/v1' THEN
            RAISE EXCEPTION 'knowledge change proposal revision requires typed fields only'
                USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.target_path IS NOT NULL
       OR NEW.base_hash IS NOT NULL
       OR NEW.content IS NOT NULL
       OR NEW.evidence_summary IS NOT NULL
       OR NEW.target_refs IS NOT NULL
       OR NEW.base_versions IS NOT NULL
       OR NEW.change_set IS NOT NULL
       OR NEW.evidence_refs IS NOT NULL
       OR NEW.artifact_id IS NULL
       OR NEW.artifact_revision_id IS NULL
       OR NEW.artifact_revision_no IS NULL
       OR NEW.artifact_version IS NULL
       OR NEW.artifact_content_hash IS NULL
       OR NEW.artifact_source_coverage IS NULL
       OR NEW.schema_version IS DISTINCT FROM 'artifact-publication/v1' THEN
        RAISE EXCEPTION 'publish artifact proposal revision requires frozen artifact fields only'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE change_control.proposal_revision
    DROP CONSTRAINT IF EXISTS fk_proposal_revision_downstream_event,
    DROP CONSTRAINT IF EXISTS fk_proposal_revision_downstream_report,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_downstream_reason,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_downstream_base_version,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_downstream_source_version,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_downstream_report_fingerprint,
    DROP COLUMN IF EXISTS downstream_reason,
    DROP COLUMN IF EXISTS downstream_owner_binding,
    DROP COLUMN IF EXISTS downstream_action,
    DROP COLUMN IF EXISTS downstream_base_version,
    DROP COLUMN IF EXISTS downstream_target_id,
    DROP COLUMN IF EXISTS downstream_target_type,
    DROP COLUMN IF EXISTS downstream_source_event_version,
    DROP COLUMN IF EXISTS downstream_source_event_id,
    DROP COLUMN IF EXISTS downstream_report_fingerprint,
    DROP COLUMN IF EXISTS downstream_analysis_version,
    DROP COLUMN IF EXISTS downstream_report_id,
    DROP COLUMN IF EXISTS downstream_workspace_id;

CREATE TRIGGER proposal_revision_validate_payload
    BEFORE INSERT ON change_control.proposal_revision
    FOR EACH ROW EXECUTE FUNCTION change_control.validate_proposal_revision_payload();

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_type,
    ADD CONSTRAINT ck_proposal_type CHECK (
        proposal_type IN ('file_patch','knowledge_change','publish_artifact')
    );

DROP FUNCTION IF EXISTS ops.valid_impact_owner_binding(text,uuid,bigint,jsonb);

DROP TRIGGER IF EXISTS timeline_project_review_card_invalidated_source
    ON learning.review_card;
DROP FUNCTION IF EXISTS ops.project_review_card_invalidated_timeline_source();
DROP TRIGGER IF EXISTS timeline_project_artifact_generated_source
    ON learning.artifact;
DROP FUNCTION IF EXISTS ops.project_artifact_generated_timeline_source();
DROP FUNCTION IF EXISTS learning.review_card_evidence_binding_fingerprint(jsonb);

DROP TRIGGER IF EXISTS impact_report_append_only ON ops.impact_report;
DROP INDEX IF EXISTS ops.idx_ops_impact_report_current;
DROP INDEX IF EXISTS ops.uq_ops_impact_report_direct_successor;
DROP INDEX IF EXISTS ops.uq_ops_impact_report_source_analysis;

ALTER TABLE ops.impact_report
    DROP CONSTRAINT IF EXISTS ops_impact_report_not_self_superseding,
    DROP CONSTRAINT IF EXISTS fk_ops_impact_report_supersedes_source,
    DROP CONSTRAINT IF EXISTS uq_ops_impact_report_id_workspace_source,
    DROP CONSTRAINT IF EXISTS ops_impact_report_schema,
    DROP COLUMN IF EXISTS supersedes_report_id,
    DROP COLUMN IF EXISTS analysis_version,
    ADD CONSTRAINT ops_impact_report_schema CHECK (schema_version='impact-report/v1'),
    ADD CONSTRAINT uq_ops_impact_report_source UNIQUE (workspace_id,source_event_id);

CREATE TRIGGER impact_report_append_only
    BEFORE UPDATE OR DELETE ON ops.impact_report
    FOR EACH ROW EXECUTE FUNCTION ops.reject_append_only_mutation();

DROP TRIGGER IF EXISTS artifact_citation_selector_backfill_guard
    ON learning.artifact_citation_selector_backfill;
DROP TRIGGER IF EXISTS artifact_citation_selector_backfill_initialize
    ON core.workspace;
DROP INDEX IF EXISTS learning.idx_learning_artifact_citation_backfill_pending_seek;
DROP TABLE IF EXISTS learning.artifact_citation_selector_backfill;
DROP FUNCTION IF EXISTS learning.guard_artifact_citation_selector_backfill();
DROP FUNCTION IF EXISTS learning.initialize_artifact_citation_selector_backfill();

DROP TRIGGER IF EXISTS artifact_revision_project_citation_selectors
    ON learning.artifact_revision;
DROP FUNCTION IF EXISTS learning.project_artifact_revision_citation_selectors();
DROP TRIGGER IF EXISTS artifact_citation_selector_append_only
    ON learning.artifact_revision_citation_selector;
DROP TRIGGER IF EXISTS artifact_citation_selector_verify
    ON learning.artifact_revision_citation_selector;
DROP TABLE IF EXISTS learning.artifact_revision_citation_selector;
DROP FUNCTION IF EXISTS learning.verify_artifact_citation_selector();
DROP INDEX IF EXISTS learning.idx_learning_artifact_revision_backfill_seek;
DROP INDEX IF EXISTS learning.idx_learning_review_card_claim_impact_seek;

DROP FUNCTION IF EXISTS ops.enqueue_timeline_projection_v2(
    uuid,text,text,uuid,text,text,bigint,text,jsonb,text,uuid,jsonb,timestamptz
);

ALTER TABLE ops.timeline_projection_outbox
    DROP CONSTRAINT IF EXISTS ops_timeline_projection_v2_wire,
    DROP CONSTRAINT IF EXISTS ops_timeline_projection_schema,
    DROP COLUMN IF EXISTS owner_binding,
    DROP COLUMN IF EXISTS operator_id,
    DROP COLUMN IF EXISTS operator_type,
    DROP COLUMN IF EXISTS schema_version;

-- Restore the v1 outbox guard from 00037.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.guard_timeline_projection_outbox_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'Timeline projection outbox rows cannot be deleted'
            USING ERRCODE='55000';
    END IF;
    IF OLD.status<>'PENDING'
       OR NEW.status NOT IN ('PROJECTED','POISONED')
       OR NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'invalid Timeline projection outbox state transition'
            USING ERRCODE='55000';
    END IF;
    IF ROW(
        NEW.id,NEW.event_id,NEW.workspace_id,NEW.event_type,
        NEW.aggregate_type,NEW.aggregate_id,NEW.source_event_ref,
        NEW.source_ref,NEW.event_version,NEW.summary,NEW.correlation,
        NEW.occurred_at,NEW.created_at
    ) IS DISTINCT FROM ROW(
        OLD.id,OLD.event_id,OLD.workspace_id,OLD.event_type,
        OLD.aggregate_type,OLD.aggregate_id,OLD.source_event_ref,
        OLD.source_ref,OLD.event_version,OLD.summary,OLD.correlation,
        OLD.occurred_at,OLD.created_at
    ) THEN
        RAISE EXCEPTION 'Timeline projection outbox source binding is immutable'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE ops.knowledge_event
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_v2_wire,
    DROP CONSTRAINT IF EXISTS ops_knowledge_event_schema,
    DROP COLUMN IF EXISTS owner_binding,
    DROP COLUMN IF EXISTS operator_id,
    DROP COLUMN IF EXISTS operator_type,
    ADD CONSTRAINT ops_knowledge_event_schema CHECK (schema_version='knowledge-event/v1');

DROP FUNCTION IF EXISTS ops.valid_timeline_owner_binding(text,text,uuid,jsonb);
