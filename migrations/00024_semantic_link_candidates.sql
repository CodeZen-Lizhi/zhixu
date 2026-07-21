-- +goose Up

CREATE SCHEMA IF NOT EXISTS graph;

ALTER TABLE change_control.proposal
    ADD COLUMN IF NOT EXISTS proposal_type text NOT NULL DEFAULT 'file_patch';

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_type,
    ADD CONSTRAINT ck_proposal_type CHECK (
        proposal_type IN ('file_patch', 'knowledge_change')
    );

ALTER TABLE change_control.proposal_revision
    ADD COLUMN IF NOT EXISTS target_refs jsonb,
    ADD COLUMN IF NOT EXISTS base_versions jsonb,
    ADD COLUMN IF NOT EXISTS change_set jsonb,
    ADD COLUMN IF NOT EXISTS evidence_refs jsonb,
    ADD COLUMN IF NOT EXISTS schema_version text;

ALTER TABLE change_control.proposal_revision
    ALTER COLUMN target_path DROP NOT NULL,
    ALTER COLUMN base_hash DROP NOT NULL,
    ALTER COLUMN content DROP NOT NULL,
    ALTER COLUMN evidence_summary DROP NOT NULL;

ALTER TABLE change_control.proposal_revision
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_target_refs,
    ADD CONSTRAINT ck_proposal_revision_target_refs CHECK (
        CASE
            WHEN target_refs IS NULL THEN true
            ELSE jsonb_typeof(target_refs) = 'array'
                AND jsonb_array_length(target_refs) > 0
                AND octet_length(target_refs::text) <= 16384
        END
    ),
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_base_versions,
    ADD CONSTRAINT ck_proposal_revision_base_versions CHECK (
        CASE
            WHEN base_versions IS NULL THEN true
            ELSE jsonb_typeof(base_versions) = 'array'
                AND jsonb_array_length(base_versions) > 0
                AND octet_length(base_versions::text) <= 16384
        END
    ),
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_change_set,
    ADD CONSTRAINT ck_proposal_revision_change_set CHECK (
        CASE
            WHEN change_set IS NULL THEN true
            ELSE jsonb_typeof(change_set) = 'object'
                AND octet_length(change_set::text) <= 32768
        END
    ),
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_evidence_refs,
    ADD CONSTRAINT ck_proposal_revision_evidence_refs CHECK (
        CASE
            WHEN evidence_refs IS NULL THEN true
            ELSE jsonb_typeof(evidence_refs) = 'array'
                AND jsonb_array_length(evidence_refs) > 0
                AND octet_length(evidence_refs::text) <= 16384
        END
    ),
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_schema_version,
    ADD CONSTRAINT ck_proposal_revision_schema_version CHECK (
        schema_version IS NULL OR (
            btrim(schema_version) <> ''
            AND octet_length(schema_version) <= 128
        )
    );

CREATE TABLE IF NOT EXISTS graph.semantic_link_candidate (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    source_node_type text NOT NULL CHECK (source_node_type IN ('TOPIC', 'CLAIM')),
    source_node_id uuid NOT NULL,
    source_node_version bigint NOT NULL CHECK (source_node_version > 0),
    target_node_type text NOT NULL CHECK (target_node_type IN ('TOPIC', 'CLAIM')),
    target_node_id uuid NOT NULL,
    target_node_version bigint NOT NULL CHECK (target_node_version > 0),
    relation_type text NOT NULL CHECK (
        relation_type IN (
            'CITES', 'DERIVED_FROM', 'BELONGS_TO', 'SUPPORTS', 'COMPLEMENTS',
            'DUPLICATES', 'CONFLICTS_WITH', 'PREREQUISITE_OF', 'VERSION_OF', 'IMPACTS'
        )
    ),
    fingerprint_schema_version text NOT NULL CHECK (
        btrim(fingerprint_schema_version) <> ''
        AND octet_length(fingerprint_schema_version) <= 128
    ),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    status text NOT NULL CHECK (
        status IN (
            'ACTIVE', 'DEFERRED', 'IGNORED',
            'FALSE_POSITIVE', 'PROPOSAL_CREATED', 'SUPERSEDED'
        )
    ),
    reopened_reason text CHECK (
        reopened_reason IS NULL
        OR reopened_reason = 'CONTENT_CHANGED'
    ),
    reopened_from_candidate_id uuid,
    current_proposal_id uuid REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    confidence_score double precision CHECK (
        confidence_score IS NULL OR (confidence_score >= 0 AND confidence_score <= 1)
    ),
    reason text NOT NULL CHECK (
        btrim(reason) <> '' AND octet_length(reason) <= 4096
    ),
    source_summary text NOT NULL DEFAULT '' CHECK (octet_length(source_summary) <= 4096),
    source_excerpt text NOT NULL DEFAULT '' CHECK (octet_length(source_excerpt) <= 4096),
    target_summary text NOT NULL DEFAULT '' CHECK (octet_length(target_summary) <= 4096),
    target_excerpt text NOT NULL DEFAULT '' CHECK (octet_length(target_excerpt) <= 4096),
    discovery_methods jsonb NOT NULL CHECK (
        CASE
            WHEN discovery_methods IS NULL THEN false
            ELSE jsonb_typeof(discovery_methods) = 'array'
                AND jsonb_array_length(discovery_methods) > 0
                AND octet_length(discovery_methods::text) <= 4096
        END
    ),
    evidence_semantic_hashes jsonb NOT NULL CHECK (
        CASE
            WHEN evidence_semantic_hashes IS NULL THEN false
            ELSE jsonb_typeof(evidence_semantic_hashes) = 'array'
                AND jsonb_array_length(evidence_semantic_hashes) > 0
                AND octet_length(evidence_semantic_hashes::text) <= 8192
        END
    ),
    generation jsonb NOT NULL CHECK (
        CASE
            WHEN generation IS NULL THEN false
            ELSE jsonb_typeof(generation) = 'object'
                AND octet_length(generation::text) <= 16384
        END
    ),
    deferred_until timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_graph_semantic_link_candidate_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_graph_semantic_link_candidate_fingerprint UNIQUE (workspace_id, fingerprint),
    CONSTRAINT uq_graph_semantic_link_candidate_proposal UNIQUE (current_proposal_id),
    CONSTRAINT fk_graph_semantic_link_candidate_reopened_from
        FOREIGN KEY (reopened_from_candidate_id, workspace_id)
        REFERENCES graph.semantic_link_candidate(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT graph_semantic_link_candidate_distinct_endpoints CHECK (
        source_node_type <> target_node_type OR source_node_id <> target_node_id
    ),
    CONSTRAINT graph_semantic_link_candidate_relation_compatibility CHECK (
        (relation_type IN ('CITES', 'DERIVED_FROM', 'SUPPORTS', 'CONFLICTS_WITH')
            AND source_node_type = 'CLAIM' AND target_node_type = 'CLAIM')
        OR (relation_type = 'BELONGS_TO'
            AND source_node_type = 'CLAIM' AND target_node_type = 'TOPIC')
        OR (relation_type IN ('COMPLEMENTS', 'DUPLICATES', 'PREREQUISITE_OF', 'VERSION_OF')
            AND source_node_type = target_node_type)
        OR (relation_type = 'IMPACTS')
    ),
    CONSTRAINT graph_semantic_link_candidate_symmetric_canonical CHECK (
        relation_type NOT IN ('DUPLICATES', 'CONFLICTS_WITH')
        OR source_node_type < target_node_type
        OR (source_node_type = target_node_type AND source_node_id::text < target_node_id::text)
    ),
    CONSTRAINT graph_semantic_link_candidate_reopen_binding CHECK (
        (reopened_from_candidate_id IS NULL AND reopened_reason IS NULL)
        OR (reopened_from_candidate_id IS NOT NULL AND reopened_reason IS NOT NULL)
    ),
    CONSTRAINT graph_semantic_link_candidate_proposal_binding CHECK (
        (
            status = 'PROPOSAL_CREATED'
            AND current_proposal_id IS NOT NULL
        ) OR (
            status = 'SUPERSEDED'
        ) OR (
            status IN ('ACTIVE', 'DEFERRED', 'IGNORED', 'FALSE_POSITIVE')
            AND current_proposal_id IS NULL
        )
    ),
    CONSTRAINT graph_semantic_link_candidate_deferred_binding CHECK (
        deferred_until IS NULL OR status = 'DEFERRED'
    ),
    CONSTRAINT graph_semantic_link_candidate_time_order CHECK (updated_at >= created_at)
);

CREATE INDEX IF NOT EXISTS idx_graph_semantic_link_candidate_workspace_status_updated
    ON graph.semantic_link_candidate (workspace_id, status, updated_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_graph_semantic_link_candidate_workspace_endpoints
    ON graph.semantic_link_candidate (
        workspace_id,
        source_node_type, source_node_id,
        target_node_type, target_node_id,
        updated_at DESC, id
    );

CREATE TABLE IF NOT EXISTS graph.semantic_link_candidate_evidence (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    candidate_id uuid NOT NULL,
    source_version_id uuid NOT NULL REFERENCES core.source_version(id) ON DELETE RESTRICT,
    source_span_id uuid NOT NULL REFERENCES ingestion.source_span(id) ON DELETE RESTRICT,
    evidence_no integer NOT NULL CHECK (evidence_no > 0),
    semantic_hash text NOT NULL CHECK (semantic_hash ~ '^[0-9a-f]{64}$'),
    summary text NOT NULL CHECK (
        btrim(summary) <> '' AND octet_length(summary) <= 4096
    ),
    excerpt text NOT NULL DEFAULT '' CHECK (octet_length(excerpt) <= 4096),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_graph_semantic_link_candidate_evidence_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_graph_semantic_link_candidate_evidence_semantic_hash UNIQUE (candidate_id, semantic_hash),
    CONSTRAINT uq_graph_semantic_link_candidate_evidence_no UNIQUE (candidate_id, evidence_no),
    CONSTRAINT fk_graph_semantic_link_candidate_evidence_candidate
        FOREIGN KEY (candidate_id, workspace_id)
        REFERENCES graph.semantic_link_candidate(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_graph_semantic_link_candidate_evidence_candidate
    ON graph.semantic_link_candidate_evidence (candidate_id, evidence_no, id);

CREATE TABLE IF NOT EXISTS graph.semantic_link_candidate_decision (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    candidate_id uuid NOT NULL,
    candidate_version bigint NOT NULL CHECK (candidate_version > 0),
    proposal_id uuid REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (
        btrim(idempotency_key) <> ''
        AND octet_length(idempotency_key) <= 128
        AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    action text NOT NULL CHECK (
        action IN (
            'CONFIRM', 'CONFIRM_WITH_RELATION_TYPE',
            'IGNORE', 'FALSE_POSITIVE', 'DEFER', 'RESUME'
        )
    ),
    relation_type text CHECK (
        relation_type IS NULL OR relation_type IN (
            'CITES', 'DERIVED_FROM', 'BELONGS_TO', 'SUPPORTS', 'COMPLEMENTS',
            'DUPLICATES', 'CONFLICTS_WITH', 'PREREQUISITE_OF', 'VERSION_OF', 'IMPACTS'
        )
    ),
    reason text CHECK (
        reason IS NULL OR (
            btrim(reason) <> '' AND octet_length(reason) <= 4096
        )
    ),
    defer_until timestamptz,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_graph_semantic_link_candidate_decision_idempotency
        UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT uq_graph_semantic_link_candidate_decision_version
        UNIQUE (candidate_id, candidate_version),
    CONSTRAINT fk_graph_semantic_link_candidate_decision_candidate
        FOREIGN KEY (candidate_id, workspace_id)
        REFERENCES graph.semantic_link_candidate(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT graph_semantic_link_candidate_decision_action_payload CHECK (
        (
            action = 'CONFIRM'
            AND proposal_id IS NOT NULL
            AND relation_type IS NULL
            AND reason IS NULL
        ) OR (
            action = 'CONFIRM_WITH_RELATION_TYPE'
            AND proposal_id IS NOT NULL
            AND relation_type IS NOT NULL
            AND reason IS NULL
        ) OR (
            action IN ('IGNORE', 'FALSE_POSITIVE')
            AND proposal_id IS NULL
            AND relation_type IS NULL
            AND reason IS NOT NULL
        ) OR (
            action = 'DEFER'
            AND proposal_id IS NULL
            AND relation_type IS NULL
        ) OR (
            action = 'RESUME'
            AND proposal_id IS NULL
            AND relation_type IS NULL
            AND reason IS NULL
            AND defer_until IS NULL
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_graph_semantic_link_candidate_decision_candidate_created
    ON graph.semantic_link_candidate_decision (candidate_id, created_at DESC, id);

CREATE TABLE IF NOT EXISTS graph.semantic_link_scan (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    scope_type text NOT NULL CHECK (
        scope_type IN ('NODE', 'TOPIC', 'DIRECTORY', 'SMART_COLLECTION')
    ),
    scope_ref text NOT NULL CHECK (
        btrim(scope_ref) <> ''
        AND octet_length(scope_ref) <= 512
        AND scope_ref !~ E'[\\r\\n]'
    ),
    scope_version bigint NOT NULL CHECK (scope_version > 0),
    scope_schema_version text NOT NULL CHECK (
        btrim(scope_schema_version) <> ''
        AND octet_length(scope_schema_version) <= 128
    ),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    idempotency_key text NOT NULL CHECK (
        btrim(idempotency_key) <> ''
        AND octet_length(idempotency_key) <= 128
        AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    workflow_run_id uuid NOT NULL REFERENCES workflow.run(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (
        status IN ('PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED')
    ),
    total_nodes bigint NOT NULL DEFAULT 0 CHECK (total_nodes >= 0),
    processed_nodes bigint NOT NULL DEFAULT 0 CHECK (processed_nodes >= 0),
    candidate_count bigint NOT NULL DEFAULT 0 CHECK (candidate_count >= 0),
    suppressed_count bigint NOT NULL DEFAULT 0 CHECK (suppressed_count >= 0),
    reopened_count bigint NOT NULL DEFAULT 0 CHECK (reopened_count >= 0),
    failed_count bigint NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(checkpoint) = 'object'
        AND octet_length(checkpoint::text) <= 16384
    ),
    last_error jsonb CHECK (
        CASE
            WHEN last_error IS NULL THEN true
            ELSE jsonb_typeof(last_error) = 'object'
                AND octet_length(last_error::text) <= 4096
        END
    ),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT uq_graph_semantic_link_scan_idempotency UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT uq_graph_semantic_link_scan_workflow_run UNIQUE (workflow_run_id),
    CONSTRAINT graph_semantic_link_scan_error CHECK (
        (status = 'FAILED' AND last_error IS NOT NULL)
        OR (status <> 'FAILED' AND last_error IS NULL)
    ),
    CONSTRAINT graph_semantic_link_scan_progress CHECK (
        processed_nodes <= total_nodes OR total_nodes = 0
    ),
    CONSTRAINT graph_semantic_link_scan_completion CHECK (
        (
            status IN ('SUCCEEDED', 'FAILED', 'CANCELLED')
            AND completed_at IS NOT NULL
        ) OR (
            status IN ('PENDING', 'RUNNING')
            AND completed_at IS NULL
        )
    ),
    CONSTRAINT graph_semantic_link_scan_time_order CHECK (
        updated_at >= created_at
        AND (completed_at IS NULL OR completed_at >= created_at)
    )
);

CREATE INDEX IF NOT EXISTS idx_graph_semantic_link_scan_workspace_status_updated
    ON graph.semantic_link_scan (workspace_id, status, updated_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_graph_semantic_link_scan_workspace_fingerprint_created
    ON graph.semantic_link_scan (workspace_id, fingerprint, created_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_knowledge_relation_semantic_scan_topic_claim
    ON core.relation (workspace_id, target_node_id, source_node_id)
    WHERE source_node_type = 'CLAIM'
      AND target_node_type = 'TOPIC'
      AND relation_type = 'BELONGS_TO'
      AND status = 'CONFIRMED';

CREATE INDEX IF NOT EXISTS idx_graph_semantic_link_scan_scope
    ON graph.semantic_link_scan (workspace_id, scope_type, scope_ref, scope_version, updated_at DESC, id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    workflow_binding_added boolean;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'proposal must be inserted at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.request_hash <> OLD.request_hash
       OR NEW.proposal_type <> OLD.proposal_type
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'proposal immutable field or version violation' USING ERRCODE = '23514';
    END IF;

    workflow_binding_added := OLD.workflow_run_id IS NULL AND NEW.workflow_run_id IS NOT NULL;
    IF workflow_binding_added
       AND NEW.status = 'approved'
       AND OLD.status IN ('ready_for_review', 'approved') THEN
        RETURN NEW;
    END IF;

    IF NOT (
        (OLD.status = 'ready_for_review' AND NEW.status IN ('approved','rejected','needs_revision'))
        OR (OLD.status = 'approved' AND NEW.status IN ('applying','needs_revision'))
        OR (OLD.status = 'applying' AND NEW.status IN ('applied','apply_failed','needs_revision'))
        OR (OLD.status = 'applied' AND NEW.status = 'verifying')
        OR (OLD.status = 'verifying' AND NEW.status IN ('completed','verify_failed','rolled_back'))
        OR (OLD.status = 'verify_failed' AND NEW.status IN ('verifying','rolled_back'))
        OR (OLD.status = 'apply_failed' AND NEW.status IN ('applying','rolled_back'))
        OR (OLD.status = 'needs_revision' AND NEW.status IN ('draft','cancelled'))
    ) THEN
        RAISE EXCEPTION 'proposal status transition is not allowed' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

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
     WHERE id = NEW.proposal_id
     FOR SHARE;

    IF proposal_type_value IS NULL THEN
        RETURN NEW;
    END IF;

    IF proposal_type_value = 'file_patch' THEN
        IF NEW.target_path IS NULL
           OR NEW.base_hash IS NULL
           OR NEW.content IS NULL
           OR NEW.evidence_summary IS NULL
           OR NEW.target_refs IS NOT NULL
           OR NEW.base_versions IS NOT NULL
           OR NEW.change_set IS NOT NULL
           OR NEW.evidence_refs IS NOT NULL
           OR NEW.schema_version IS NOT NULL THEN
            RAISE EXCEPTION 'file patch proposal revision requires file fields only' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.target_path IS NOT NULL
       OR NEW.base_hash IS NOT NULL
       OR NEW.content IS NOT NULL
       OR NEW.evidence_summary IS NOT NULL
       OR NEW.target_refs IS NULL
       OR NEW.base_versions IS NULL
       OR NEW.change_set IS NULL
       OR NEW.evidence_refs IS NULL
       OR NEW.schema_version IS DISTINCT FROM 'knowledge-relation-change/v1' THEN
        RAISE EXCEPTION 'knowledge change proposal revision requires typed fields only' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS proposal_revision_validate_payload ON change_control.proposal_revision;
CREATE TRIGGER proposal_revision_validate_payload
    BEFORE INSERT ON change_control.proposal_revision
    FOR EACH ROW EXECUTE FUNCTION change_control.validate_proposal_revision_payload();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.reject_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'graph record is immutable' USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.validate_semantic_link_candidate_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_workspace uuid;
    proposal_type_value text;
BEGIN
    IF NEW.current_proposal_id IS NOT NULL THEN
        SELECT workspace_id, proposal_type
          INTO proposal_workspace, proposal_type_value
          FROM change_control.proposal
         WHERE id = NEW.current_proposal_id
         FOR SHARE;
        IF proposal_workspace IS NOT NULL
           AND (proposal_workspace <> NEW.workspace_id OR proposal_type_value <> 'knowledge_change') THEN
            RAISE EXCEPTION 'candidate proposal workspace or type mismatch' USING ERRCODE = '23514';
        END IF;
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1
           OR NEW.status NOT IN ('ACTIVE', 'DEFERRED')
           OR NEW.current_proposal_id IS NOT NULL THEN
            RAISE EXCEPTION 'semantic link candidate must start active or deferred at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.source_node_type <> OLD.source_node_type
       OR NEW.source_node_id <> OLD.source_node_id
       OR NEW.source_node_version <> OLD.source_node_version
       OR NEW.target_node_type <> OLD.target_node_type
       OR NEW.target_node_id <> OLD.target_node_id
       OR NEW.target_node_version <> OLD.target_node_version
       OR NEW.relation_type <> OLD.relation_type
       OR NEW.fingerprint_schema_version <> OLD.fingerprint_schema_version
       OR NEW.fingerprint <> OLD.fingerprint
       OR NEW.reopened_reason IS DISTINCT FROM OLD.reopened_reason
       OR NEW.reopened_from_candidate_id IS DISTINCT FROM OLD.reopened_from_candidate_id
       OR NEW.confidence_score IS DISTINCT FROM OLD.confidence_score
       OR NEW.reason <> OLD.reason
       OR NEW.source_summary <> OLD.source_summary
       OR NEW.source_excerpt <> OLD.source_excerpt
       OR NEW.target_summary <> OLD.target_summary
       OR NEW.target_excerpt <> OLD.target_excerpt
       OR NEW.discovery_methods <> OLD.discovery_methods
       OR NEW.evidence_semantic_hashes <> OLD.evidence_semantic_hashes
       OR NEW.generation <> OLD.generation
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'semantic link candidate immutable binding or version changed' USING ERRCODE = '23514';
    END IF;

    IF OLD.status = 'SUPERSEDED' THEN
        RAISE EXCEPTION 'superseded semantic link candidate is immutable' USING ERRCODE = '55000';
    END IF;

    IF OLD.current_proposal_id IS NOT NULL
       AND NEW.current_proposal_id IS DISTINCT FROM OLD.current_proposal_id THEN
        RAISE EXCEPTION 'candidate proposal binding is immutable once assigned' USING ERRCODE = '23514';
    END IF;

    IF NOT (
        (OLD.status = 'ACTIVE' AND NEW.status IN ('DEFERRED', 'IGNORED', 'FALSE_POSITIVE', 'PROPOSAL_CREATED', 'SUPERSEDED'))
        OR (OLD.status = 'DEFERRED' AND NEW.status IN ('ACTIVE', 'IGNORED', 'FALSE_POSITIVE', 'PROPOSAL_CREATED', 'SUPERSEDED'))
        OR (OLD.status = 'IGNORED' AND NEW.status = 'SUPERSEDED')
        OR (OLD.status = 'FALSE_POSITIVE' AND NEW.status = 'SUPERSEDED')
        OR (OLD.status = 'PROPOSAL_CREATED' AND NEW.status = 'SUPERSEDED')
    ) THEN
        RAISE EXCEPTION 'semantic link candidate transition is invalid' USING ERRCODE = '23514';
    END IF;

    IF NEW.status <> 'DEFERRED' AND NEW.deferred_until IS NOT NULL THEN
        RAISE EXCEPTION 'deferred timestamp requires deferred candidate status' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS semantic_link_candidate_validate_write ON graph.semantic_link_candidate;
CREATE TRIGGER semantic_link_candidate_validate_write
    BEFORE INSERT OR UPDATE ON graph.semantic_link_candidate
    FOR EACH ROW EXECUTE FUNCTION graph.validate_semantic_link_candidate_write();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.validate_semantic_link_candidate_evidence_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT core.knowledge_validate_provenance_binding(
        NEW.workspace_id, NEW.source_version_id, NEW.source_span_id
    ) THEN
        RAISE EXCEPTION 'candidate evidence provenance binding is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS semantic_link_candidate_evidence_validate_insert
    ON graph.semantic_link_candidate_evidence;
CREATE TRIGGER semantic_link_candidate_evidence_validate_insert
    BEFORE INSERT ON graph.semantic_link_candidate_evidence
    FOR EACH ROW EXECUTE FUNCTION graph.validate_semantic_link_candidate_evidence_insert();

DROP TRIGGER IF EXISTS semantic_link_candidate_evidence_reject_update_delete
    ON graph.semantic_link_candidate_evidence;
CREATE TRIGGER semantic_link_candidate_evidence_reject_update_delete
    BEFORE UPDATE OR DELETE ON graph.semantic_link_candidate_evidence
    FOR EACH ROW EXECUTE FUNCTION graph.reject_immutable_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.validate_semantic_link_candidate_decision_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    candidate_status text;
    current_version bigint;
    proposal_binding uuid;
    proposal_workspace uuid;
    proposal_type_value text;
BEGIN
    SELECT status, version, current_proposal_id
      INTO candidate_status, current_version, proposal_binding
      FROM graph.semantic_link_candidate
     WHERE id = NEW.candidate_id
       AND workspace_id = NEW.workspace_id
     FOR SHARE;

    IF current_version IS NULL THEN
        RETURN NEW;
    END IF;

    IF NEW.candidate_version <> current_version THEN
        RAISE EXCEPTION 'candidate decision expected version mismatch' USING ERRCODE = '23514';
    END IF;

    IF NEW.proposal_id IS NOT NULL THEN
        SELECT workspace_id, proposal_type
          INTO proposal_workspace, proposal_type_value
          FROM change_control.proposal
         WHERE id = NEW.proposal_id
         FOR SHARE;
        IF proposal_workspace IS NOT NULL
           AND (proposal_workspace <> NEW.workspace_id OR proposal_type_value <> 'knowledge_change') THEN
            RAISE EXCEPTION 'candidate decision proposal workspace or type mismatch' USING ERRCODE = '23514';
        END IF;
    END IF;

    IF NEW.action IN ('CONFIRM', 'CONFIRM_WITH_RELATION_TYPE') THEN
        IF candidate_status NOT IN ('ACTIVE', 'DEFERRED') OR proposal_binding IS NOT NULL THEN
            RAISE EXCEPTION 'candidate confirm requires an unresolved candidate without a proposal' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.action IN ('IGNORE', 'FALSE_POSITIVE') THEN
        IF candidate_status NOT IN ('ACTIVE', 'DEFERRED') THEN
            RAISE EXCEPTION 'candidate suppression requires an active or deferred candidate' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.action = 'DEFER' THEN
        IF candidate_status <> 'ACTIVE' THEN
            RAISE EXCEPTION 'candidate defer requires an active candidate' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.action = 'RESUME' THEN
        IF candidate_status <> 'DEFERRED' THEN
            RAISE EXCEPTION 'candidate resume requires a deferred candidate' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS semantic_link_candidate_decision_validate_insert
    ON graph.semantic_link_candidate_decision;
CREATE TRIGGER semantic_link_candidate_decision_validate_insert
    BEFORE INSERT ON graph.semantic_link_candidate_decision
    FOR EACH ROW EXECUTE FUNCTION graph.validate_semantic_link_candidate_decision_insert();

DROP TRIGGER IF EXISTS semantic_link_candidate_decision_reject_update_delete
    ON graph.semantic_link_candidate_decision;
CREATE TRIGGER semantic_link_candidate_decision_reject_update_delete
    BEFORE UPDATE OR DELETE ON graph.semantic_link_candidate_decision
    FOR EACH ROW EXECUTE FUNCTION graph.reject_immutable_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.validate_semantic_link_scan_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_workspace uuid;
BEGIN
    SELECT workspace_id
      INTO run_workspace
      FROM workflow.run
     WHERE id = NEW.workflow_run_id
     FOR SHARE;
    IF run_workspace IS NOT NULL AND run_workspace <> NEW.workspace_id THEN
        RAISE EXCEPTION 'semantic link scan workflow workspace mismatch' USING ERRCODE = '23514';
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 OR NEW.status NOT IN ('PENDING', 'RUNNING') THEN
            RAISE EXCEPTION 'semantic link scan must start pending or running at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.scope_type <> OLD.scope_type
       OR NEW.scope_ref <> OLD.scope_ref
       OR NEW.scope_version <> OLD.scope_version
       OR NEW.scope_schema_version <> OLD.scope_schema_version
       OR NEW.fingerprint <> OLD.fingerprint
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.request_hash <> OLD.request_hash
       OR NEW.workflow_run_id <> OLD.workflow_run_id
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'semantic link scan immutable binding or version changed' USING ERRCODE = '23514';
    END IF;

    IF NEW.total_nodes < OLD.total_nodes
       OR NEW.processed_nodes < OLD.processed_nodes
       OR NEW.candidate_count < OLD.candidate_count
       OR NEW.suppressed_count < OLD.suppressed_count
       OR NEW.reopened_count < OLD.reopened_count
       OR NEW.failed_count < OLD.failed_count THEN
        RAISE EXCEPTION 'semantic link scan counters cannot decrease' USING ERRCODE = '23514';
    END IF;

    IF OLD.status <> NEW.status AND NOT (
        (OLD.status = 'PENDING' AND NEW.status IN ('RUNNING', 'FAILED', 'CANCELLED'))
        OR (OLD.status = 'RUNNING' AND NEW.status IN ('SUCCEEDED', 'FAILED', 'CANCELLED'))
    ) THEN
        RAISE EXCEPTION 'semantic link scan transition is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS semantic_link_scan_validate_write ON graph.semantic_link_scan;
CREATE TRIGGER semantic_link_scan_validate_write
    BEFORE INSERT OR UPDATE ON graph.semantic_link_scan
    FOR EACH ROW EXECUTE FUNCTION graph.validate_semantic_link_scan_write();

INSERT INTO core.schema_meta (key, value)
VALUES ('semantic_link_candidates', 'm7-02')
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = now();

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM graph.semantic_link_candidate) THEN
        RAISE EXCEPTION 'cannot remove semantic link candidates with candidate data'
            USING ERRCODE = '55000';
    END IF;
    IF EXISTS (SELECT 1 FROM graph.semantic_link_candidate_evidence) THEN
        RAISE EXCEPTION 'cannot remove semantic link candidates with candidate evidence data'
            USING ERRCODE = '55000';
    END IF;
    IF EXISTS (SELECT 1 FROM graph.semantic_link_candidate_decision) THEN
        RAISE EXCEPTION 'cannot remove semantic link candidates with decision data'
            USING ERRCODE = '55000';
    END IF;
    IF EXISTS (SELECT 1 FROM graph.semantic_link_scan) THEN
        RAISE EXCEPTION 'cannot remove semantic link candidates with scan data'
            USING ERRCODE = '55000';
    END IF;
    IF EXISTS (
        SELECT 1
          FROM change_control.proposal
         WHERE proposal_type = 'knowledge_change'
    ) THEN
        RAISE EXCEPTION 'cannot remove semantic link candidates with knowledge change proposals'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS semantic_link_scan_validate_write ON graph.semantic_link_scan;
DROP FUNCTION IF EXISTS graph.validate_semantic_link_scan_write();

DROP INDEX IF EXISTS core.idx_knowledge_relation_semantic_scan_topic_claim;

DROP TRIGGER IF EXISTS semantic_link_candidate_decision_reject_update_delete
    ON graph.semantic_link_candidate_decision;
DROP TRIGGER IF EXISTS semantic_link_candidate_decision_validate_insert
    ON graph.semantic_link_candidate_decision;
DROP FUNCTION IF EXISTS graph.validate_semantic_link_candidate_decision_insert();

DROP TRIGGER IF EXISTS semantic_link_candidate_evidence_reject_update_delete
    ON graph.semantic_link_candidate_evidence;
DROP TRIGGER IF EXISTS semantic_link_candidate_evidence_validate_insert
    ON graph.semantic_link_candidate_evidence;
DROP FUNCTION IF EXISTS graph.validate_semantic_link_candidate_evidence_insert();

DROP TRIGGER IF EXISTS semantic_link_candidate_validate_write ON graph.semantic_link_candidate;
DROP FUNCTION IF EXISTS graph.validate_semantic_link_candidate_write();

DROP FUNCTION IF EXISTS graph.reject_immutable_mutation();

DROP TABLE IF EXISTS graph.semantic_link_scan;
DROP TABLE IF EXISTS graph.semantic_link_candidate_decision;
DROP TABLE IF EXISTS graph.semantic_link_candidate_evidence;
DROP TABLE IF EXISTS graph.semantic_link_candidate;
DROP SCHEMA IF EXISTS graph;

DROP TRIGGER IF EXISTS proposal_revision_validate_payload ON change_control.proposal_revision;
DROP FUNCTION IF EXISTS change_control.validate_proposal_revision_payload();

ALTER TABLE change_control.proposal_revision
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_schema_version,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_evidence_refs,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_change_set,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_base_versions,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_target_refs,
    ALTER COLUMN target_path SET NOT NULL,
    ALTER COLUMN base_hash SET NOT NULL,
    ALTER COLUMN content SET NOT NULL,
    ALTER COLUMN evidence_summary SET NOT NULL,
    DROP COLUMN IF EXISTS schema_version,
    DROP COLUMN IF EXISTS evidence_refs,
    DROP COLUMN IF EXISTS change_set,
    DROP COLUMN IF EXISTS base_versions,
    DROP COLUMN IF EXISTS target_refs;

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_type,
    DROP COLUMN IF EXISTS proposal_type;

-- Restore the file-only Proposal transition contract.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    workflow_binding_added boolean;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'proposal must be inserted at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.request_hash <> OLD.request_hash
       OR NEW.created_at <> OLD.created_at
       OR NEW.updated_at < OLD.updated_at
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'proposal immutable field or version violation' USING ERRCODE = '23514';
    END IF;

    workflow_binding_added := OLD.workflow_run_id IS NULL AND NEW.workflow_run_id IS NOT NULL;
    IF workflow_binding_added
       AND NEW.status = 'approved'
       AND OLD.status IN ('ready_for_review', 'approved') THEN
        RETURN NEW;
    END IF;

    IF NOT (
        (OLD.status = 'ready_for_review' AND NEW.status IN ('approved','rejected','needs_revision'))
        OR (OLD.status = 'approved' AND NEW.status IN ('applying','needs_revision'))
        OR (OLD.status = 'applying' AND NEW.status IN ('applied','apply_failed','needs_revision'))
        OR (OLD.status = 'applied' AND NEW.status = 'verifying')
        OR (OLD.status = 'verifying' AND NEW.status IN ('completed','verify_failed','rolled_back'))
        OR (OLD.status = 'verify_failed' AND NEW.status IN ('verifying','rolled_back'))
        OR (OLD.status = 'apply_failed' AND NEW.status IN ('applying','rolled_back'))
        OR (OLD.status = 'needs_revision' AND NEW.status IN ('draft','cancelled'))
    ) THEN
        RAISE EXCEPTION 'proposal status transition is not allowed' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DELETE FROM core.schema_meta WHERE key = 'semantic_link_candidates';
