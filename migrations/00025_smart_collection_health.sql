-- +goose Up

CREATE TABLE IF NOT EXISTS learning.smart_collection (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (btrim(name) <> '' AND octet_length(name) <= 256),
    normalized_name text NOT NULL CHECK (btrim(normalized_name) <> '' AND octet_length(normalized_name) <= 256),
    description text NOT NULL DEFAULT '' CHECK (octet_length(description) <= 4096),
    query_schema_version text NOT NULL CHECK (query_schema_version = 'collection-query/v1'),
    query_version bigint NOT NULL CHECK (query_version > 0),
    query_definition jsonb NOT NULL CHECK (
        jsonb_typeof(query_definition) = 'object'
        AND octet_length(query_definition::text) <= 32768
    ),
    query_hash text NOT NULL CHECK (query_hash ~ '^[0-9a-f]{64}$'),
    view_type text NOT NULL CHECK (view_type IN ('LIST', 'TABLE', 'COMPACT_CARD')),
    view_config jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(view_config) = 'object'
        AND octet_length(view_config::text) <= 16384
    ),
    status text NOT NULL CHECK (status IN ('ACTIVE', 'ARCHIVED')),
    cached_result_version text CHECK (
        cached_result_version IS NULL OR (
            btrim(cached_result_version) <> '' AND octet_length(cached_result_version) <= 512
        )
    ),
    last_executed_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_learning_smart_collection_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT learning_smart_collection_time_order CHECK (
        updated_at >= created_at
        AND (last_executed_at IS NULL OR last_executed_at >= created_at)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_learning_smart_collection_active_name
    ON learning.smart_collection (workspace_id, normalized_name)
    WHERE status = 'ACTIVE';

CREATE INDEX IF NOT EXISTS idx_learning_smart_collection_workspace_status_updated
    ON learning.smart_collection (workspace_id, status, updated_at DESC, id);

ALTER TABLE graph.semantic_link_scan
    ADD COLUMN IF NOT EXISTS scope_hash text,
    ADD COLUMN IF NOT EXISTS read_model_revision text;

ALTER TABLE graph.semantic_link_scan
    DROP CONSTRAINT IF EXISTS graph_semantic_link_scan_smart_collection_binding,
    ADD CONSTRAINT graph_semantic_link_scan_smart_collection_binding CHECK (
        (
            scope_type = 'SMART_COLLECTION'
            AND scope_hash IS NOT NULL
            AND scope_hash ~ '^[0-9a-f]{64}$'
            AND read_model_revision IS NOT NULL
            AND read_model_revision ~ '^[0-9a-f]{64}$'
        ) OR (
            scope_type <> 'SMART_COLLECTION'
            AND scope_hash IS NULL
            AND read_model_revision IS NULL
        )
    );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.validate_semantic_link_smart_collection_scope()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' AND NEW.scope_type = 'SMART_COLLECTION' THEN
        IF NEW.scope_ref !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
           OR NOT EXISTS (
                SELECT 1
                FROM learning.smart_collection collection
                WHERE collection.id = CASE
                    WHEN NEW.scope_ref ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
                    THEN NEW.scope_ref::uuid
                    ELSE NULL
                END
                  AND collection.workspace_id = NEW.workspace_id
                  AND collection.status = 'ACTIVE'
                  AND collection.version = NEW.scope_version
                  AND collection.query_hash = NEW.scope_hash
           ) THEN
            RAISE EXCEPTION 'semantic link smart collection scope binding is invalid' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF TG_OP = 'UPDATE'
       AND (NEW.scope_hash IS DISTINCT FROM OLD.scope_hash
            OR NEW.read_model_revision IS DISTINCT FROM OLD.read_model_revision) THEN
        RAISE EXCEPTION 'semantic link smart collection scope binding is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS semantic_link_smart_collection_scope_validate_write ON graph.semantic_link_scan;
CREATE TRIGGER semantic_link_smart_collection_scope_validate_write
    BEFORE INSERT OR UPDATE ON graph.semantic_link_scan
    FOR EACH ROW EXECUTE FUNCTION graph.validate_semantic_link_smart_collection_scope();

CREATE TABLE IF NOT EXISTS learning.smart_collection_command (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (
        btrim(idempotency_key) <> ''
        AND octet_length(idempotency_key) <= 128
        AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (command_type IN ('CREATE', 'UPDATE', 'ARCHIVE')),
    collection_id uuid NOT NULL,
    collection_version bigint NOT NULL CHECK (collection_version > 0),
    receipt jsonb NOT NULL CHECK (
        jsonb_typeof(receipt) = 'object'
        AND octet_length(receipt::text) <= 131072
    ),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key),
    CONSTRAINT fk_learning_smart_collection_command_collection
        FOREIGN KEY (collection_id, workspace_id)
        REFERENCES learning.smart_collection(id, workspace_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS ops.health_scan (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    scope_type text NOT NULL CHECK (scope_type IN ('WORKSPACE', 'TOPIC', 'SMART_COLLECTION')),
    scope_ref uuid NOT NULL,
    scope_version bigint NOT NULL CHECK (scope_version > 0),
    scope_schema_version text NOT NULL CHECK (
        btrim(scope_schema_version) <> '' AND octet_length(scope_schema_version) <= 128
    ),
    scope_hash text CHECK (scope_hash IS NULL OR scope_hash ~ '^[0-9a-f]{64}$'),
    scope_read_model_revision text CHECK (
        scope_read_model_revision IS NULL OR scope_read_model_revision ~ '^[0-9a-f]{64}$'
    ),
    scope_exact_count bigint CHECK (scope_exact_count IS NULL OR scope_exact_count >= 0),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    idempotency_key text NOT NULL CHECK (
        btrim(idempotency_key) <> ''
        AND octet_length(idempotency_key) <= 128
        AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    workflow_run_id uuid NOT NULL,
    max_items bigint NOT NULL CHECK (max_items BETWEEN 1 AND 50000),
    status text NOT NULL CHECK (status IN ('PENDING', 'RUNNING', 'SUCCEEDED', 'PARTIAL', 'FAILED', 'CANCELLED')),
    checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(checkpoint) = 'object' AND octet_length(checkpoint::text) <= 16384
    ),
    last_error jsonb CHECK (
        last_error IS NULL OR (jsonb_typeof(last_error) = 'object' AND octet_length(last_error::text) <= 4096)
    ),
    processed_count bigint NOT NULL DEFAULT 0 CHECK (processed_count >= 0),
    created_count bigint NOT NULL DEFAULT 0 CHECK (created_count >= 0),
    reopened_count bigint NOT NULL DEFAULT 0 CHECK (reopened_count >= 0),
    resolved_count bigint NOT NULL DEFAULT 0 CHECK (resolved_count >= 0),
    unchanged_count bigint NOT NULL DEFAULT 0 CHECK (unchanged_count >= 0),
    failed_count bigint NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_ops_health_scan_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_ops_health_scan_idempotency UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT uq_ops_health_scan_workflow_run UNIQUE (workflow_run_id),
    CONSTRAINT fk_ops_health_scan_workflow_run
        FOREIGN KEY (workflow_run_id, workspace_id)
        REFERENCES workflow.run(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT ops_health_scan_scope_binding CHECK (
        (
            scope_type = 'SMART_COLLECTION'
            AND scope_hash IS NOT NULL
            AND scope_hash ~ '^[0-9a-f]{64}$'
            AND scope_read_model_revision IS NOT NULL
            AND scope_read_model_revision ~ '^[0-9a-f]{64}$'
            AND scope_exact_count IS NOT NULL
            AND scope_exact_count >= 0
        ) OR (
            scope_type <> 'SMART_COLLECTION'
            AND scope_hash IS NULL
            AND scope_read_model_revision IS NULL
            AND scope_exact_count IS NULL
        )
    ),
    CONSTRAINT ops_health_scan_error_binding CHECK (
        (status = 'FAILED' AND last_error IS NOT NULL)
        OR status = 'PARTIAL'
        OR (status NOT IN ('FAILED', 'PARTIAL') AND last_error IS NULL)
    ),
    CONSTRAINT ops_health_scan_completion CHECK (
        (status IN ('SUCCEEDED', 'PARTIAL', 'FAILED', 'CANCELLED') AND completed_at IS NOT NULL)
        OR (status IN ('PENDING', 'RUNNING') AND completed_at IS NULL)
    ),
    CONSTRAINT ops_health_scan_time_order CHECK (
        updated_at >= created_at AND (completed_at IS NULL OR completed_at >= created_at)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_ops_health_scan_active_scope_fingerprint
    ON ops.health_scan (workspace_id, scope_type, scope_ref, fingerprint)
    WHERE status IN ('PENDING', 'RUNNING');

CREATE INDEX IF NOT EXISTS idx_ops_health_scan_workspace_status_updated
    ON ops.health_scan (workspace_id, status, updated_at DESC, id);

CREATE TABLE IF NOT EXISTS ops.health_issue (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    type text NOT NULL CHECK (type IN (
        'ORPHAN', 'DUPLICATE', 'CONFLICT', 'STALE', 'MISSING_SOURCE',
        'LOW_CONFIDENCE', 'BROKEN_REFERENCE', 'INDEX_ERROR', 'SUPERSEDED_USAGE'
    )),
    target_type text NOT NULL CHECK (target_type IN (
        'TOPIC', 'CLAIM', 'RELATION', 'CONFLICT', 'SOURCE_VERSION', 'INDEX_VERSION'
    )),
    target_id uuid NOT NULL,
    detector_id text NOT NULL CHECK (btrim(detector_id) <> '' AND octet_length(detector_id) <= 128),
    identity_hash text NOT NULL CHECK (identity_hash ~ '^[0-9a-f]{64}$'),
    fingerprint_schema_version text NOT NULL CHECK (
        btrim(fingerprint_schema_version) <> '' AND octet_length(fingerprint_schema_version) <= 128
    ),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    detector_version text NOT NULL CHECK (btrim(detector_version) <> '' AND octet_length(detector_version) <= 128),
    severity text NOT NULL CHECK (severity IN ('CRITICAL', 'HIGH', 'MEDIUM', 'LOW')),
    evidence_summary text NOT NULL CHECK (btrim(evidence_summary) <> '' AND octet_length(evidence_summary) <= 4096),
    status text NOT NULL CHECK (status IN (
        'OPEN', 'ACKNOWLEDGED', 'DEFERRED', 'PROPOSAL_CREATED',
        'RESOLVED', 'IGNORED', 'FALSE_POSITIVE', 'REOPENED'
    )),
    status_reason text,
    deferred_until timestamptz,
    repair_proposal_id uuid REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    repair_option_code text,
    repair_binding_fingerprint text,
    repair_binding_object_versions jsonb,
    repair_proposal_created_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    first_detected_at timestamptz NOT NULL,
    last_detected_at timestamptz NOT NULL,
    last_verified_at timestamptz NOT NULL,
    resolved_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_ops_health_issue_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_ops_health_issue_identity UNIQUE (workspace_id, identity_hash),
    CONSTRAINT ops_health_issue_status_binding CHECK (
        (
            status IN ('IGNORED', 'FALSE_POSITIVE')
            AND status_reason IS NOT NULL
            AND btrim(status_reason) <> ''
            AND octet_length(status_reason) <= 4096
            AND deferred_until IS NULL
            AND repair_proposal_id IS NULL
            AND repair_option_code IS NULL
            AND repair_binding_fingerprint IS NULL
            AND repair_binding_object_versions IS NULL
            AND repair_proposal_created_at IS NULL
        ) OR (
            status = 'DEFERRED'
            AND status_reason IS NOT NULL
            AND btrim(status_reason) <> ''
            AND octet_length(status_reason) <= 4096
            AND deferred_until IS NOT NULL
            AND repair_proposal_id IS NULL
            AND repair_option_code IS NULL
            AND repair_binding_fingerprint IS NULL
            AND repair_binding_object_versions IS NULL
            AND repair_proposal_created_at IS NULL
        ) OR (
            status = 'PROPOSAL_CREATED'
            AND status_reason IS NULL
            AND deferred_until IS NULL
            AND repair_proposal_id IS NOT NULL
            AND repair_option_code IS NOT NULL
            AND btrim(repair_option_code) <> ''
            AND octet_length(repair_option_code) <= 128
            AND repair_binding_fingerprint IS NOT NULL
            AND repair_binding_fingerprint ~ '^[0-9a-f]{64}$'
            AND repair_binding_object_versions IS NOT NULL
            AND jsonb_typeof(repair_binding_object_versions) = 'array'
            AND jsonb_array_length(repair_binding_object_versions) > 0
            AND octet_length(repair_binding_object_versions::text) <= 16384
            AND repair_proposal_created_at IS NOT NULL
        ) OR (
            status IN ('OPEN', 'ACKNOWLEDGED', 'RESOLVED', 'REOPENED')
            AND status_reason IS NULL
            AND deferred_until IS NULL
            AND repair_proposal_id IS NULL
            AND repair_option_code IS NULL
            AND repair_binding_fingerprint IS NULL
            AND repair_binding_object_versions IS NULL
            AND repair_proposal_created_at IS NULL
        )
    ),
    CONSTRAINT ops_health_issue_resolution_binding CHECK (
        (status = 'RESOLVED' AND resolved_at IS NOT NULL)
        OR (status <> 'RESOLVED' AND resolved_at IS NULL)
    ),
    CONSTRAINT ops_health_issue_time_order CHECK (
        last_detected_at >= first_detected_at
        AND last_verified_at >= first_detected_at
        AND updated_at >= created_at
        AND (resolved_at IS NULL OR resolved_at >= first_detected_at)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_ops_health_issue_active_fingerprint
    ON ops.health_issue (workspace_id, fingerprint)
    WHERE status <> 'RESOLVED';

CREATE INDEX IF NOT EXISTS idx_ops_health_issue_workspace_status_severity_updated
    ON ops.health_issue (workspace_id, status, severity, updated_at DESC, id);

CREATE TABLE IF NOT EXISTS ops.health_issue_observation (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    issue_id uuid NOT NULL,
    issue_version bigint NOT NULL CHECK (issue_version > 0),
    scan_id uuid NOT NULL,
    detector_version text NOT NULL CHECK (btrim(detector_version) <> '' AND octet_length(detector_version) <= 128),
    fingerprint_schema_version text NOT NULL CHECK (
        btrim(fingerprint_schema_version) <> '' AND octet_length(fingerprint_schema_version) <= 128
    ),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    evidence_fingerprint text NOT NULL CHECK (evidence_fingerprint ~ '^[0-9a-f]{64}$'),
    target_versions jsonb NOT NULL CHECK (
        jsonb_typeof(target_versions) = 'array'
        AND jsonb_array_length(target_versions) > 0
        AND octet_length(target_versions::text) <= 16384
    ),
    severity text NOT NULL CHECK (severity IN ('CRITICAL', 'HIGH', 'MEDIUM', 'LOW')),
    observed_at timestamptz NOT NULL,
    CONSTRAINT uq_ops_health_issue_observation_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_ops_health_issue_observation_fingerprint UNIQUE (issue_id, fingerprint),
    CONSTRAINT fk_ops_health_issue_observation_issue
        FOREIGN KEY (issue_id, workspace_id)
        REFERENCES ops.health_issue(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_ops_health_issue_observation_scan
        FOREIGN KEY (scan_id, workspace_id)
        REFERENCES ops.health_scan(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_ops_health_issue_observation_issue_observed
    ON ops.health_issue_observation (issue_id, observed_at DESC, id);

CREATE TABLE IF NOT EXISTS ops.health_issue_evidence (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    observation_id uuid NOT NULL,
    evidence_no integer NOT NULL CHECK (evidence_no > 0),
    ref_type text NOT NULL CHECK (ref_type IN (
        'TOPIC', 'CLAIM', 'RELATION', 'CONFLICT', 'SOURCE_VERSION', 'INDEX_VERSION'
    )),
    ref_id uuid NOT NULL,
    summary text NOT NULL CHECK (btrim(summary) <> '' AND octet_length(summary) <= 4096),
    hash text NOT NULL CHECK (hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_ops_health_issue_evidence_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_ops_health_issue_evidence_no UNIQUE (observation_id, evidence_no),
    CONSTRAINT uq_ops_health_issue_evidence_hash UNIQUE (observation_id, hash),
    CONSTRAINT fk_ops_health_issue_evidence_observation
        FOREIGN KEY (observation_id, workspace_id)
        REFERENCES ops.health_issue_observation(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_ops_health_issue_evidence_observation
    ON ops.health_issue_evidence (observation_id, evidence_no, id);

CREATE TABLE IF NOT EXISTS ops.health_issue_decision (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    issue_id uuid NOT NULL,
    issue_version bigint NOT NULL CHECK (issue_version > 0),
    proposal_id uuid REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    repair_option_code text CHECK (
        repair_option_code IS NULL OR (
            btrim(repair_option_code) <> '' AND octet_length(repair_option_code) <= 128
        )
    ),
    idempotency_key text NOT NULL CHECK (
        btrim(idempotency_key) <> ''
        AND octet_length(idempotency_key) <= 128
        AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    action text NOT NULL CHECK (action IN (
        'ACKNOWLEDGE', 'DEFER', 'IGNORE', 'FALSE_POSITIVE', 'CREATE_REPAIR_PROPOSAL'
    )),
    reason text CHECK (reason IS NULL OR (btrim(reason) <> '' AND octet_length(reason) <= 4096)),
    defer_until timestamptz,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_ops_health_issue_decision_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_ops_health_issue_decision_idempotency UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT uq_ops_health_issue_decision_version UNIQUE (issue_id, issue_version),
    CONSTRAINT fk_ops_health_issue_decision_issue
        FOREIGN KEY (issue_id, workspace_id)
        REFERENCES ops.health_issue(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT ops_health_issue_decision_payload CHECK (
        (action = 'ACKNOWLEDGE' AND reason IS NULL AND defer_until IS NULL AND proposal_id IS NULL AND repair_option_code IS NULL)
        OR (action IN ('IGNORE', 'FALSE_POSITIVE') AND reason IS NOT NULL AND defer_until IS NULL AND proposal_id IS NULL AND repair_option_code IS NULL)
        OR (
            action = 'DEFER'
            AND reason IS NOT NULL
            AND defer_until IS NOT NULL
            AND defer_until > created_at
            AND proposal_id IS NULL
            AND repair_option_code IS NULL
        )
        OR (
            action = 'CREATE_REPAIR_PROPOSAL'
            AND reason IS NULL
            AND defer_until IS NULL
            AND proposal_id IS NOT NULL
            AND repair_option_code IS NOT NULL
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_ops_health_issue_decision_issue_created
    ON ops.health_issue_decision (issue_id, created_at DESC, id);

CREATE TABLE IF NOT EXISTS ops.health_scan_detector (
    scan_id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    detector_id text NOT NULL CHECK (btrim(detector_id) <> '' AND octet_length(detector_id) <= 128),
    detector_version text NOT NULL CHECK (btrim(detector_version) <> '' AND octet_length(detector_version) <= 128),
    status text NOT NULL CHECK (status IN (
        'PENDING', 'RUNNING', 'SUCCEEDED', 'PARTIAL', 'FAILED', 'CANCELLED', 'UNAVAILABLE'
    )),
    checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(checkpoint) = 'object' AND octet_length(checkpoint::text) <= 16384
    ),
    failure_summary jsonb CHECK (
        failure_summary IS NULL OR (
            jsonb_typeof(failure_summary) = 'object' AND octet_length(failure_summary::text) <= 4096
        )
    ),
    unavailable_reason text CHECK (
        unavailable_reason IS NULL OR (btrim(unavailable_reason) <> '' AND octet_length(unavailable_reason) <= 4096)
    ),
    processed_count bigint NOT NULL DEFAULT 0 CHECK (processed_count >= 0),
    created_count bigint NOT NULL DEFAULT 0 CHECK (created_count >= 0),
    reopened_count bigint NOT NULL DEFAULT 0 CHECK (reopened_count >= 0),
    resolved_count bigint NOT NULL DEFAULT 0 CHECK (resolved_count >= 0),
    unchanged_count bigint NOT NULL DEFAULT 0 CHECK (unchanged_count >= 0),
    failed_count bigint NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    started_at timestamptz,
    completed_at timestamptz,
    PRIMARY KEY (scan_id, detector_id),
    CONSTRAINT uq_ops_health_scan_detector_workspace UNIQUE (scan_id, detector_id, workspace_id),
    CONSTRAINT fk_ops_health_scan_detector_scan
        FOREIGN KEY (scan_id, workspace_id)
        REFERENCES ops.health_scan(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT ops_health_scan_detector_failure_binding CHECK (
        (status = 'FAILED' AND failure_summary IS NOT NULL AND unavailable_reason IS NULL)
        OR (status = 'PARTIAL' AND failure_summary IS NOT NULL AND unavailable_reason IS NULL)
        OR (status = 'UNAVAILABLE' AND failure_summary IS NULL AND unavailable_reason IS NOT NULL)
        OR (
            status NOT IN ('FAILED', 'PARTIAL', 'UNAVAILABLE')
            AND failure_summary IS NULL
            AND unavailable_reason IS NULL
        )
    ),
    CONSTRAINT ops_health_scan_detector_completion CHECK (
        (status IN ('SUCCEEDED', 'PARTIAL', 'FAILED', 'CANCELLED', 'UNAVAILABLE') AND completed_at IS NOT NULL)
        OR (status IN ('PENDING', 'RUNNING') AND completed_at IS NULL)
    )
);

CREATE TABLE IF NOT EXISTS ops.health_scan_seen_identity (
    scan_id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    detector_id text NOT NULL CHECK (btrim(detector_id) <> '' AND octet_length(detector_id) <= 128),
    identity_hash text NOT NULL CHECK (identity_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (scan_id, detector_id, identity_hash),
    CONSTRAINT fk_ops_health_scan_seen_identity_detector
        FOREIGN KEY (scan_id, detector_id, workspace_id)
        REFERENCES ops.health_scan_detector(scan_id, detector_id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_ops_health_scan_seen_identity_scan
        FOREIGN KEY (scan_id, workspace_id)
        REFERENCES ops.health_scan(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_ops_health_scan_seen_identity_lookup
    ON ops.health_scan_seen_identity (workspace_id, detector_id, identity_hash);

CREATE TABLE IF NOT EXISTS ops.health_schedule (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    scope_type text NOT NULL CHECK (scope_type IN ('WORKSPACE', 'TOPIC', 'SMART_COLLECTION')),
    scope_ref uuid NOT NULL,
    scope_version bigint NOT NULL CHECK (scope_version > 0),
    scope_schema_version text NOT NULL CHECK (
        btrim(scope_schema_version) <> '' AND octet_length(scope_schema_version) <= 128
    ),
    scope_hash text CHECK (scope_hash IS NULL OR scope_hash ~ '^[0-9a-f]{64}$'),
    cadence text NOT NULL CHECK (cadence IN ('DISABLED', 'DAILY', 'WEEKLY', 'CRON')),
    cron_expression text,
    timezone text NOT NULL CHECK (btrim(timezone) <> '' AND octet_length(timezone) <= 128),
    max_items bigint NOT NULL CHECK (max_items BETWEEN 1 AND 50000),
    next_run_at timestamptz,
    last_run_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_ops_health_schedule_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_ops_health_schedule_scope UNIQUE (workspace_id, scope_type, scope_ref),
    CONSTRAINT ops_health_schedule_scope_binding CHECK (
        (scope_type = 'SMART_COLLECTION' AND scope_hash IS NOT NULL)
        OR (scope_type <> 'SMART_COLLECTION' AND scope_hash IS NULL)
    ),
    CONSTRAINT ops_health_schedule_cadence_binding CHECK (
        (cadence = 'DISABLED' AND cron_expression IS NULL AND next_run_at IS NULL)
        OR (cadence IN ('DAILY', 'WEEKLY') AND cron_expression IS NULL AND next_run_at IS NOT NULL)
        OR (
            cadence = 'CRON'
            AND cron_expression IS NOT NULL
            AND btrim(cron_expression) <> ''
            AND octet_length(cron_expression) <= 128
            AND next_run_at IS NOT NULL
        )
    ),
    CONSTRAINT ops_health_schedule_time_order CHECK (
        updated_at >= created_at
        AND (last_run_at IS NULL OR last_run_at >= created_at)
        AND (next_run_at IS NULL OR last_run_at IS NULL OR next_run_at >= last_run_at)
    )
);

CREATE INDEX IF NOT EXISTS idx_ops_health_schedule_due
    ON ops.health_schedule (next_run_at, id)
    WHERE cadence <> 'DISABLED';

CREATE TABLE IF NOT EXISTS ops.health_schedule_command (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (
        btrim(idempotency_key) <> ''
        AND octet_length(idempotency_key) <= 128
        AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (command_type IN ('CREATE', 'UPDATE')),
    schedule_id uuid NOT NULL,
    schedule_version bigint NOT NULL CHECK (schedule_version > 0),
    receipt jsonb NOT NULL CHECK (
        jsonb_typeof(receipt) = 'object'
        AND octet_length(receipt::text) <= 32768
    ),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key),
    CONSTRAINT fk_ops_health_schedule_command_schedule
        FOREIGN KEY (schedule_id, workspace_id)
        REFERENCES ops.health_schedule(id, workspace_id) ON DELETE RESTRICT
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.validate_smart_collection_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 OR NEW.status <> 'ACTIVE' THEN
            RAISE EXCEPTION 'smart collection must start active at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'smart collection immutable binding or version changed' USING ERRCODE = '23514';
    END IF;
    IF OLD.status = 'ARCHIVED' THEN
        RAISE EXCEPTION 'archived smart collection is immutable' USING ERRCODE = '55000';
    END IF;
    IF NEW.status NOT IN (OLD.status, 'ARCHIVED') THEN
        RAISE EXCEPTION 'smart collection transition is invalid' USING ERRCODE = '23514';
    END IF;
    IF NEW.query_definition IS DISTINCT FROM OLD.query_definition
       OR NEW.query_hash IS DISTINCT FROM OLD.query_hash
       OR NEW.query_schema_version IS DISTINCT FROM OLD.query_schema_version THEN
        IF NEW.query_version <> OLD.query_version + 1 THEN
            RAISE EXCEPTION 'query change must increment query version' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.query_version <> OLD.query_version THEN
        RAISE EXCEPTION 'query version cannot change without query change' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS smart_collection_validate_write ON learning.smart_collection;
CREATE TRIGGER smart_collection_validate_write
    BEFORE INSERT OR UPDATE ON learning.smart_collection
    FOR EACH ROW EXECUTE FUNCTION learning.validate_smart_collection_write();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.reject_smart_collection_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'smart collection must be archived instead of deleted' USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS smart_collection_reject_delete ON learning.smart_collection;
CREATE TRIGGER smart_collection_reject_delete
    BEFORE DELETE ON learning.smart_collection
    FOR EACH ROW EXECUTE FUNCTION learning.reject_smart_collection_delete();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.validate_smart_collection_command_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_version bigint;
BEGIN
    SELECT version INTO current_version
      FROM learning.smart_collection
     WHERE id = NEW.collection_id
       AND workspace_id = NEW.workspace_id
     FOR KEY SHARE;
    IF current_version IS NULL OR current_version <> NEW.collection_version THEN
        RAISE EXCEPTION 'smart collection command aggregate binding is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS smart_collection_command_validate_insert ON learning.smart_collection_command;
CREATE TRIGGER smart_collection_command_validate_insert
    BEFORE INSERT ON learning.smart_collection_command
    FOR EACH ROW EXECUTE FUNCTION learning.validate_smart_collection_command_insert();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.reject_health_history_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'health history record is immutable' USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS smart_collection_command_reject_update_delete ON learning.smart_collection_command;
CREATE TRIGGER smart_collection_command_reject_update_delete
    BEFORE UPDATE OR DELETE ON learning.smart_collection_command
    FOR EACH ROW EXECUTE FUNCTION ops.reject_health_history_mutation();

DROP TRIGGER IF EXISTS health_schedule_command_reject_update_delete ON ops.health_schedule_command;
CREATE TRIGGER health_schedule_command_reject_update_delete
    BEFORE UPDATE OR DELETE ON ops.health_schedule_command
    FOR EACH ROW EXECUTE FUNCTION ops.reject_health_history_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.health_validate_object_binding(
    workspace_value uuid,
    type_value text,
    id_value uuid
) RETURNS boolean
LANGUAGE plpgsql
AS $$
BEGIN
    CASE type_value
        WHEN 'TOPIC' THEN RETURN EXISTS (SELECT 1 FROM core.topic WHERE id=id_value AND workspace_id=workspace_value);
        WHEN 'CLAIM' THEN RETURN EXISTS (SELECT 1 FROM core.claim WHERE id=id_value AND workspace_id=workspace_value);
        WHEN 'RELATION' THEN RETURN EXISTS (SELECT 1 FROM core.relation WHERE id=id_value AND workspace_id=workspace_value);
        WHEN 'CONFLICT' THEN RETURN EXISTS (SELECT 1 FROM core.conflict WHERE id=id_value AND workspace_id=workspace_value);
        WHEN 'SOURCE_VERSION' THEN RETURN EXISTS (
            SELECT 1 FROM core.source_version sv JOIN core.source s ON s.id=sv.source_id
            WHERE sv.id=id_value AND s.workspace_id=workspace_value
        );
        WHEN 'INDEX_VERSION' THEN RETURN EXISTS (
            SELECT 1 FROM retrieval.index_version WHERE id=id_value AND workspace_id=workspace_value
        );
        ELSE RETURN false;
    END CASE;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.validate_health_issue_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_workspace uuid;
    proposal_type_value text;
BEGIN
    IF NOT ops.health_validate_object_binding(NEW.workspace_id, NEW.target_type, NEW.target_id) THEN
        RAISE EXCEPTION 'health issue target binding is invalid' USING ERRCODE = '23514';
    END IF;
    IF NEW.repair_proposal_id IS NOT NULL THEN
        SELECT workspace_id, proposal_type INTO proposal_workspace, proposal_type_value
          FROM change_control.proposal WHERE id=NEW.repair_proposal_id FOR SHARE;
        IF proposal_workspace IS NULL OR proposal_workspace <> NEW.workspace_id OR proposal_type_value <> 'knowledge_change' THEN
            RAISE EXCEPTION 'health issue proposal binding is invalid' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF TG_OP = 'UPDATE'
       AND NEW.id = OLD.id
       AND NEW.workspace_id = OLD.workspace_id
       AND NEW.type = OLD.type
       AND NEW.target_type = OLD.target_type
       AND NEW.target_id = OLD.target_id
       AND NEW.detector_id = OLD.detector_id
       AND NEW.identity_hash = OLD.identity_hash
       AND NEW.fingerprint_schema_version = OLD.fingerprint_schema_version
       AND NEW.fingerprint = OLD.fingerprint
       AND NEW.detector_version = OLD.detector_version
       AND NEW.severity = OLD.severity
       AND NEW.evidence_summary = OLD.evidence_summary
       AND NEW.status = OLD.status
       AND NEW.status_reason IS NOT DISTINCT FROM OLD.status_reason
       AND NEW.deferred_until IS NOT DISTINCT FROM OLD.deferred_until
       AND NEW.repair_proposal_id IS NOT DISTINCT FROM OLD.repair_proposal_id
       AND NEW.repair_option_code IS NOT DISTINCT FROM OLD.repair_option_code
       AND NEW.repair_binding_fingerprint IS NOT DISTINCT FROM OLD.repair_binding_fingerprint
       AND NEW.repair_binding_object_versions IS NOT DISTINCT FROM OLD.repair_binding_object_versions
       AND NEW.repair_proposal_created_at IS NOT DISTINCT FROM OLD.repair_proposal_created_at
       AND NEW.version = OLD.version
       AND NEW.first_detected_at = OLD.first_detected_at
       AND NEW.last_detected_at = OLD.last_detected_at
       AND NEW.last_verified_at >= OLD.last_verified_at
       AND NEW.resolved_at IS NOT DISTINCT FROM OLD.resolved_at
       AND NEW.created_at = OLD.created_at
       AND NEW.updated_at = OLD.updated_at THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 OR NEW.status <> 'OPEN' THEN
            RAISE EXCEPTION 'health issue must start open at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.type <> OLD.type
       OR NEW.target_type <> OLD.target_type
       OR NEW.target_id <> OLD.target_id
       OR NEW.detector_id <> OLD.detector_id
       OR NEW.identity_hash <> OLD.identity_hash
       OR NEW.first_detected_at <> OLD.first_detected_at
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR NEW.last_detected_at < OLD.last_detected_at
       OR NEW.last_verified_at < OLD.last_verified_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'health issue immutable binding, time, or version changed' USING ERRCODE = '23514';
    END IF;
    IF OLD.status = 'RESOLVED' AND NEW.status <> 'REOPENED' THEN
        RAISE EXCEPTION 'resolved health issue can only reopen' USING ERRCODE = '23514';
    END IF;
    IF NEW.status = 'DEFERRED'
       AND (
           OLD.status <> 'DEFERRED'
           OR NEW.deferred_until IS DISTINCT FROM OLD.deferred_until
       )
       AND NEW.deferred_until <= GREATEST(CURRENT_TIMESTAMP, NEW.updated_at) THEN
        RAISE EXCEPTION 'health issue defer time must be in the future' USING ERRCODE = '23514';
    END IF;
    IF NEW.fingerprint IS DISTINCT FROM OLD.fingerprint THEN
        IF NEW.status <> 'REOPENED'
           OR NEW.status_reason IS NOT NULL
           OR NEW.deferred_until IS NOT NULL
           OR NEW.repair_proposal_id IS NOT NULL
           OR NEW.repair_option_code IS NOT NULL
           OR NEW.repair_binding_fingerprint IS NOT NULL
           OR NEW.repair_binding_object_versions IS NOT NULL
           OR NEW.repair_proposal_created_at IS NOT NULL
           OR NEW.resolved_at IS NOT NULL THEN
            RAISE EXCEPTION 'changed health issue fingerprint must reopen without stale bindings' USING ERRCODE = '23514';
        END IF;
    ELSIF OLD.status <> NEW.status AND NOT (
        (OLD.status IN ('OPEN','ACKNOWLEDGED','DEFERRED','PROPOSAL_CREATED','REOPENED')
            AND NEW.status IN ('ACKNOWLEDGED','DEFERRED','PROPOSAL_CREATED','IGNORED','FALSE_POSITIVE','RESOLVED'))
        OR (OLD.status IN ('IGNORED','FALSE_POSITIVE') AND NEW.status = 'RESOLVED')
        OR (OLD.status = 'RESOLVED' AND NEW.status = 'REOPENED')
    ) THEN
        RAISE EXCEPTION 'health issue status transition is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_issue_validate_write ON ops.health_issue;
CREATE TRIGGER health_issue_validate_write
    BEFORE INSERT OR UPDATE ON ops.health_issue
    FOR EACH ROW EXECUTE FUNCTION ops.validate_health_issue_write();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.validate_health_issue_history_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_version bigint;
    issue_workspace uuid;
    proposal_workspace uuid;
    proposal_type_value text;
BEGIN
    IF TG_TABLE_NAME = 'health_issue_observation' THEN
        SELECT workspace_id, version INTO issue_workspace, current_version
          FROM ops.health_issue WHERE id=NEW.issue_id FOR SHARE;
        IF issue_workspace IS NULL OR issue_workspace <> NEW.workspace_id OR NEW.issue_version <> current_version THEN
            RAISE EXCEPTION 'health observation issue version or workspace mismatch' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'health_issue_decision' THEN
        SELECT workspace_id, version INTO issue_workspace, current_version
          FROM ops.health_issue WHERE id=NEW.issue_id FOR SHARE;
        IF issue_workspace IS NULL OR issue_workspace <> NEW.workspace_id OR NEW.issue_version <> current_version THEN
            RAISE EXCEPTION 'health decision issue version or workspace mismatch' USING ERRCODE = '23514';
        END IF;
        IF NEW.action = 'DEFER' AND NEW.defer_until <= CURRENT_TIMESTAMP THEN
            RAISE EXCEPTION 'health decision defer time must be in the future' USING ERRCODE = '23514';
        END IF;
        IF NEW.proposal_id IS NOT NULL THEN
            SELECT workspace_id, proposal_type INTO proposal_workspace, proposal_type_value
              FROM change_control.proposal WHERE id=NEW.proposal_id FOR SHARE;
            IF proposal_workspace IS NULL OR proposal_workspace <> NEW.workspace_id OR proposal_type_value <> 'knowledge_change' THEN
                RAISE EXCEPTION 'health decision proposal binding is invalid' USING ERRCODE = '23514';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.validate_health_evidence_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT ops.health_validate_object_binding(NEW.workspace_id, NEW.ref_type, NEW.ref_id) THEN
        RAISE EXCEPTION 'health evidence reference binding is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_issue_observation_validate_insert ON ops.health_issue_observation;
CREATE TRIGGER health_issue_observation_validate_insert
    BEFORE INSERT ON ops.health_issue_observation
    FOR EACH ROW EXECUTE FUNCTION ops.validate_health_issue_history_insert();
DROP TRIGGER IF EXISTS health_issue_decision_validate_insert ON ops.health_issue_decision;
CREATE TRIGGER health_issue_decision_validate_insert
    BEFORE INSERT ON ops.health_issue_decision
    FOR EACH ROW EXECUTE FUNCTION ops.validate_health_issue_history_insert();
DROP TRIGGER IF EXISTS health_issue_evidence_validate_insert ON ops.health_issue_evidence;
CREATE TRIGGER health_issue_evidence_validate_insert
    BEFORE INSERT ON ops.health_issue_evidence
    FOR EACH ROW EXECUTE FUNCTION ops.validate_health_evidence_insert();

DROP TRIGGER IF EXISTS health_issue_observation_reject_update_delete ON ops.health_issue_observation;
CREATE TRIGGER health_issue_observation_reject_update_delete
    BEFORE UPDATE OR DELETE ON ops.health_issue_observation
    FOR EACH ROW EXECUTE FUNCTION ops.reject_health_history_mutation();
DROP TRIGGER IF EXISTS health_issue_evidence_reject_update_delete ON ops.health_issue_evidence;
CREATE TRIGGER health_issue_evidence_reject_update_delete
    BEFORE UPDATE OR DELETE ON ops.health_issue_evidence
    FOR EACH ROW EXECUTE FUNCTION ops.reject_health_history_mutation();
DROP TRIGGER IF EXISTS health_issue_decision_reject_update_delete ON ops.health_issue_decision;
CREATE TRIGGER health_issue_decision_reject_update_delete
    BEFORE UPDATE OR DELETE ON ops.health_issue_decision
    FOR EACH ROW EXECUTE FUNCTION ops.reject_health_history_mutation();
DROP TRIGGER IF EXISTS health_scan_seen_identity_reject_update_delete ON ops.health_scan_seen_identity;
CREATE TRIGGER health_scan_seen_identity_reject_update_delete
    BEFORE UPDATE OR DELETE ON ops.health_scan_seen_identity
    FOR EACH ROW EXECUTE FUNCTION ops.reject_health_history_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.validate_health_scan_scope(
    workspace_value uuid,
    scope_type_value text,
    scope_ref_value uuid,
    scope_version_value bigint,
    scope_hash_value text
) RETURNS boolean
LANGUAGE plpgsql
AS $$
BEGIN
    CASE scope_type_value
        WHEN 'WORKSPACE' THEN RETURN EXISTS (
            SELECT 1 FROM core.workspace
             WHERE id=workspace_value
               AND id=scope_ref_value
               AND version=scope_version_value
        );
        WHEN 'TOPIC' THEN RETURN EXISTS (
            SELECT 1 FROM core.topic
             WHERE id=scope_ref_value
               AND workspace_id=workspace_value
               AND version=scope_version_value
        );
        WHEN 'SMART_COLLECTION' THEN RETURN EXISTS (
            SELECT 1 FROM learning.smart_collection
             WHERE id=scope_ref_value
               AND workspace_id=workspace_value
               AND version=scope_version_value
               AND query_hash=scope_hash_value
               AND status='ACTIVE'
        );
        ELSE RETURN false;
    END CASE;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.validate_health_scan_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NOT ops.validate_health_scan_scope(
            NEW.workspace_id,
            NEW.scope_type,
            NEW.scope_ref,
            NEW.scope_version,
            NEW.scope_hash
        ) THEN
            RAISE EXCEPTION 'health scan scope binding is invalid' USING ERRCODE = '23514';
        END IF;
        IF NEW.version <> 1 OR NEW.status NOT IN ('PENDING', 'RUNNING') THEN
            RAISE EXCEPTION 'health scan must start pending or running at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.scope_type <> OLD.scope_type
       OR NEW.scope_ref <> OLD.scope_ref
       OR NEW.scope_version <> OLD.scope_version
       OR NEW.scope_schema_version <> OLD.scope_schema_version
       OR NEW.scope_hash IS DISTINCT FROM OLD.scope_hash
       OR NEW.scope_read_model_revision IS DISTINCT FROM OLD.scope_read_model_revision
       OR NEW.scope_exact_count IS DISTINCT FROM OLD.scope_exact_count
       OR NEW.fingerprint <> OLD.fingerprint
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.request_hash <> OLD.request_hash
       OR NEW.workflow_run_id <> OLD.workflow_run_id
       OR NEW.max_items <> OLD.max_items
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'health scan immutable binding or version changed' USING ERRCODE = '23514';
    END IF;
    IF NEW.processed_count < OLD.processed_count
       OR NEW.created_count < OLD.created_count
       OR NEW.reopened_count < OLD.reopened_count
       OR NEW.resolved_count < OLD.resolved_count
       OR NEW.unchanged_count < OLD.unchanged_count
       OR NEW.failed_count < OLD.failed_count THEN
        RAISE EXCEPTION 'health scan counters cannot decrease' USING ERRCODE = '23514';
    END IF;
    IF OLD.status <> NEW.status AND NOT (
        (OLD.status='PENDING' AND NEW.status IN ('RUNNING','FAILED','CANCELLED'))
        OR (OLD.status='RUNNING' AND NEW.status IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED'))
    ) THEN
        RAISE EXCEPTION 'health scan transition is invalid' USING ERRCODE = '23514';
    END IF;
    IF OLD.status IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED') THEN
        RAISE EXCEPTION 'terminal health scan is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_scan_validate_write ON ops.health_scan;
CREATE TRIGGER health_scan_validate_write
    BEFORE INSERT OR UPDATE ON ops.health_scan
    FOR EACH ROW EXECUTE FUNCTION ops.validate_health_scan_write();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.validate_health_scan_detector_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'PENDING'
           OR NEW.processed_count <> 0
           OR NEW.created_count <> 0
           OR NEW.reopened_count <> 0
           OR NEW.resolved_count <> 0
           OR NEW.unchanged_count <> 0
           OR NEW.failed_count <> 0 THEN
            RAISE EXCEPTION 'health detector coverage must start pending' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.scan_id <> OLD.scan_id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.detector_id <> OLD.detector_id
       OR NEW.detector_version <> OLD.detector_version
       OR NEW.processed_count < OLD.processed_count
       OR NEW.created_count < OLD.created_count
       OR NEW.reopened_count < OLD.reopened_count
       OR NEW.resolved_count < OLD.resolved_count
       OR NEW.unchanged_count < OLD.unchanged_count
       OR NEW.failed_count < OLD.failed_count THEN
        RAISE EXCEPTION 'health detector immutable binding or counters changed invalidly' USING ERRCODE = '23514';
    END IF;
    IF OLD.status <> NEW.status AND NOT (
        (OLD.status='PENDING' AND NEW.status IN ('RUNNING','FAILED','CANCELLED','UNAVAILABLE'))
        OR (OLD.status='RUNNING' AND NEW.status IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED'))
    ) THEN
        RAISE EXCEPTION 'health detector transition is invalid' USING ERRCODE = '23514';
    END IF;
    IF OLD.status IN ('PARTIAL','FAILED','CANCELLED','UNAVAILABLE') THEN
        RAISE EXCEPTION 'terminal health detector coverage is immutable' USING ERRCODE = '55000';
    END IF;
    IF OLD.status = 'SUCCEEDED' AND (
        NEW.status <> 'SUCCEEDED'
        OR NEW.checkpoint <> OLD.checkpoint
        OR NEW.failure_summary IS DISTINCT FROM OLD.failure_summary
        OR NEW.unavailable_reason IS DISTINCT FROM OLD.unavailable_reason
        OR NEW.processed_count <> OLD.processed_count
        OR NEW.created_count <> OLD.created_count
        OR NEW.reopened_count <> OLD.reopened_count
        OR NEW.unchanged_count <> OLD.unchanged_count
        OR NEW.failed_count <> OLD.failed_count
        OR NEW.started_at IS DISTINCT FROM OLD.started_at
        OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
    ) THEN
        RAISE EXCEPTION 'succeeded health detector only accepts resolved count finalization' USING ERRCODE = '55000';
    END IF;
    IF OLD.status = 'SUCCEEDED' AND NEW.resolved_count <> OLD.resolved_count THEN
        PERFORM 1
          FROM ops.health_scan scan
         WHERE scan.id = NEW.scan_id
           AND scan.workspace_id = NEW.workspace_id
           AND scan.status = 'RUNNING'
         FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'succeeded health detector can only finalize resolved count while scan is running' USING ERRCODE = '55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_scan_detector_validate_write ON ops.health_scan_detector;
CREATE TRIGGER health_scan_detector_validate_write
    BEFORE INSERT OR UPDATE ON ops.health_scan_detector
    FOR EACH ROW EXECUTE FUNCTION ops.validate_health_scan_detector_write();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.validate_health_schedule_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NOT ops.validate_health_scan_scope(
            NEW.workspace_id,
            NEW.scope_type,
            NEW.scope_ref,
            NEW.scope_version,
            NEW.scope_hash
        ) THEN
            RAISE EXCEPTION 'health schedule scope binding is invalid' USING ERRCODE = '23514';
        END IF;
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'health schedule must start at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.scope_type <> OLD.scope_type
       OR NEW.scope_ref <> OLD.scope_ref
       OR NEW.scope_version <> OLD.scope_version
       OR NEW.scope_schema_version <> OLD.scope_schema_version
       OR NEW.scope_hash IS DISTINCT FROM OLD.scope_hash
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR (OLD.last_run_at IS NOT NULL AND NEW.last_run_at IS NULL)
       OR (OLD.last_run_at IS NOT NULL AND NEW.last_run_at < OLD.last_run_at)
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'health schedule immutable binding, time, or version changed' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_schedule_validate_write ON ops.health_schedule;
CREATE TRIGGER health_schedule_validate_write
    BEFORE INSERT OR UPDATE ON ops.health_schedule
    FOR EACH ROW EXECUTE FUNCTION ops.validate_health_schedule_write();

INSERT INTO core.schema_meta (key, value)
VALUES ('smart_collection_health', 'm7-03')
ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now();

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.smart_collection)
       OR EXISTS (SELECT 1 FROM learning.smart_collection_command)
       OR EXISTS (SELECT 1 FROM ops.health_issue)
       OR EXISTS (SELECT 1 FROM ops.health_issue_observation)
       OR EXISTS (SELECT 1 FROM ops.health_issue_evidence)
       OR EXISTS (SELECT 1 FROM ops.health_issue_decision)
       OR EXISTS (SELECT 1 FROM ops.health_scan)
       OR EXISTS (SELECT 1 FROM ops.health_scan_detector)
       OR EXISTS (SELECT 1 FROM ops.health_scan_seen_identity)
       OR EXISTS (SELECT 1 FROM ops.health_schedule)
       OR EXISTS (SELECT 1 FROM graph.semantic_link_scan WHERE scope_type='SMART_COLLECTION') THEN
        RAISE EXCEPTION 'cannot remove smart collection health schema with business data' USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS health_scan_detector_validate_write ON ops.health_scan_detector;
DROP FUNCTION IF EXISTS ops.validate_health_scan_detector_write();
DROP TRIGGER IF EXISTS health_schedule_validate_write ON ops.health_schedule;
DROP FUNCTION IF EXISTS ops.validate_health_schedule_write();
DROP TRIGGER IF EXISTS health_scan_validate_write ON ops.health_scan;
DROP FUNCTION IF EXISTS ops.validate_health_scan_write();
DROP FUNCTION IF EXISTS ops.validate_health_scan_scope(uuid,text,uuid,bigint,text);

DROP TRIGGER IF EXISTS health_issue_decision_reject_update_delete ON ops.health_issue_decision;
DROP TRIGGER IF EXISTS health_issue_evidence_reject_update_delete ON ops.health_issue_evidence;
DROP TRIGGER IF EXISTS health_issue_observation_reject_update_delete ON ops.health_issue_observation;
DROP TRIGGER IF EXISTS health_scan_seen_identity_reject_update_delete ON ops.health_scan_seen_identity;
DROP TRIGGER IF EXISTS smart_collection_command_reject_update_delete ON learning.smart_collection_command;
DROP TRIGGER IF EXISTS smart_collection_command_validate_insert ON learning.smart_collection_command;
DROP FUNCTION IF EXISTS learning.validate_smart_collection_command_insert();
DROP TRIGGER IF EXISTS health_schedule_command_reject_update_delete ON ops.health_schedule_command;
DROP TRIGGER IF EXISTS health_issue_decision_validate_insert ON ops.health_issue_decision;
DROP TRIGGER IF EXISTS health_issue_evidence_validate_insert ON ops.health_issue_evidence;
DROP FUNCTION IF EXISTS ops.validate_health_evidence_insert();
DROP TRIGGER IF EXISTS health_issue_observation_validate_insert ON ops.health_issue_observation;
DROP FUNCTION IF EXISTS ops.validate_health_issue_history_insert();
DROP TRIGGER IF EXISTS health_issue_validate_write ON ops.health_issue;
DROP FUNCTION IF EXISTS ops.validate_health_issue_write();
DROP FUNCTION IF EXISTS ops.health_validate_object_binding(uuid,text,uuid);
DROP FUNCTION IF EXISTS ops.reject_health_history_mutation();

DROP TRIGGER IF EXISTS smart_collection_reject_delete ON learning.smart_collection;
DROP FUNCTION IF EXISTS learning.reject_smart_collection_delete();
DROP TRIGGER IF EXISTS smart_collection_validate_write ON learning.smart_collection;
DROP FUNCTION IF EXISTS learning.validate_smart_collection_write();

DROP TRIGGER IF EXISTS semantic_link_smart_collection_scope_validate_write ON graph.semantic_link_scan;
DROP FUNCTION IF EXISTS graph.validate_semantic_link_smart_collection_scope();
ALTER TABLE graph.semantic_link_scan
    DROP CONSTRAINT IF EXISTS graph_semantic_link_scan_smart_collection_binding,
    DROP COLUMN IF EXISTS scope_hash,
    DROP COLUMN IF EXISTS read_model_revision;

DROP TABLE IF EXISTS ops.health_scan_seen_identity;
DROP TABLE IF EXISTS ops.health_scan_detector;
DROP TABLE IF EXISTS ops.health_issue_evidence;
DROP TABLE IF EXISTS ops.health_issue_decision;
DROP TABLE IF EXISTS ops.health_issue_observation;
DROP TABLE IF EXISTS ops.health_issue;
DROP TABLE IF EXISTS ops.health_schedule_command;
DROP TABLE IF EXISTS ops.health_schedule;
DROP TABLE IF EXISTS ops.health_scan;
DROP TABLE IF EXISTS learning.smart_collection_command;
DROP TABLE IF EXISTS learning.smart_collection;

DELETE FROM core.schema_meta WHERE key='smart_collection_health';
