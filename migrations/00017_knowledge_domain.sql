-- +goose Up

CREATE TABLE IF NOT EXISTS core.topic (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (
        btrim(name) <> '' AND octet_length(name) <= 256
    ),
    normalized_name text NOT NULL CHECK (
        btrim(normalized_name) <> '' AND octet_length(normalized_name) <= 256
    ),
    description text NOT NULL DEFAULT '' CHECK (octet_length(description) <= 4096),
    status text NOT NULL CHECK (status IN ('ACTIVE', 'MERGED', 'DEPRECATED')),
    merged_into_topic_id uuid,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_knowledge_topic_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_knowledge_topic_workspace_name UNIQUE (workspace_id, normalized_name),
    CONSTRAINT fk_knowledge_topic_merged_workspace
        FOREIGN KEY (merged_into_topic_id, workspace_id)
        REFERENCES core.topic(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT knowledge_topic_merge_binding CHECK (
        (status = 'MERGED' AND merged_into_topic_id IS NOT NULL AND merged_into_topic_id <> id)
        OR (status <> 'MERGED' AND merged_into_topic_id IS NULL)
    ),
    CONSTRAINT knowledge_topic_time_order CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS core.topic_alias (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    topic_id uuid NOT NULL,
    alias text NOT NULL CHECK (btrim(alias) <> '' AND octet_length(alias) <= 256),
    normalized_alias text NOT NULL CHECK (
        btrim(normalized_alias) <> '' AND octet_length(normalized_alias) <= 256
    ),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_knowledge_topic_alias_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_knowledge_topic_alias_workspace_value UNIQUE (workspace_id, normalized_alias),
    CONSTRAINT fk_knowledge_topic_alias_topic_workspace
        FOREIGN KEY (topic_id, workspace_id)
        REFERENCES core.topic(id, workspace_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS core.claim (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    statement text NOT NULL CHECK (
        btrim(statement) <> '' AND octet_length(statement) <= 16384
    ),
    normalized_statement text NOT NULL CHECK (
        btrim(normalized_statement) <> '' AND octet_length(normalized_statement) <= 16384
    ),
    applicability jsonb NOT NULL CHECK (
        jsonb_typeof(applicability) = 'object'
        AND octet_length(applicability::text) <= 16384
    ),
    applicability_schema_version text NOT NULL
        CHECK (applicability_schema_version = 'knowledge-applicability/v1'),
    applicability_hash text NOT NULL CHECK (applicability_hash ~ '^[0-9a-f]{64}$'),
    status text NOT NULL CHECK (
        status IN ('SUGGESTED', 'CONFIRMED', 'DISPUTED', 'SUPERSEDED', 'DEPRECATED', 'INVALID')
    ),
    confidence_score double precision CHECK (
        confidence_score IS NULL OR (confidence_score >= 0 AND confidence_score <= 1)
    ),
    confidence_factors jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(confidence_factors) = 'object'
        AND octet_length(confidence_factors::text) <= 16384
    ),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_knowledge_claim_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_knowledge_claim_workspace_fingerprint UNIQUE (workspace_id, fingerprint),
    CONSTRAINT knowledge_claim_time_order CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS core.claim_source (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    claim_id uuid NOT NULL,
    source_version_id uuid NOT NULL REFERENCES core.source_version(id) ON DELETE RESTRICT,
    source_span_id uuid NOT NULL REFERENCES ingestion.source_span(id) ON DELETE RESTRICT,
    support_type text NOT NULL CHECK (support_type IN ('SUPPORTS', 'REFUTES')),
    reason text NOT NULL CHECK (btrim(reason) <> '' AND octet_length(reason) <= 4096),
    evidence_hash text NOT NULL CHECK (evidence_hash ~ '^[0-9a-f]{64}$'),
    model_run_ref text CHECK (
        model_run_ref IS NULL OR (btrim(model_run_ref) <> '' AND octet_length(model_run_ref) <= 512)
    ),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_knowledge_claim_source_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_knowledge_claim_source_workspace_hash UNIQUE (workspace_id, evidence_hash),
    CONSTRAINT fk_knowledge_claim_source_claim_workspace
        FOREIGN KEY (claim_id, workspace_id)
        REFERENCES core.claim(id, workspace_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS core.relation (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    source_node_type text NOT NULL CHECK (source_node_type IN ('TOPIC', 'CLAIM')),
    source_node_id uuid NOT NULL,
    target_node_type text NOT NULL CHECK (target_node_type IN ('TOPIC', 'CLAIM')),
    target_node_id uuid NOT NULL,
    relation_type text NOT NULL CHECK (
        relation_type IN (
            'CITES', 'DERIVED_FROM', 'BELONGS_TO', 'SUPPORTS', 'COMPLEMENTS',
            'DUPLICATES', 'CONFLICTS_WITH', 'PREREQUISITE_OF', 'VERSION_OF', 'IMPACTS'
        )
    ),
    status text NOT NULL CHECK (
        status IN ('SUGGESTED', 'CONFIRMED', 'REJECTED', 'STALE', 'DEPRECATED')
    ),
    confidence_score double precision CHECK (
        confidence_score IS NULL OR (confidence_score >= 0 AND confidence_score <= 1)
    ),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    evidence_fingerprint text CHECK (
        evidence_fingerprint IS NULL OR evidence_fingerprint ~ '^[0-9a-f]{64}$'
    ),
    confirmation_method text CHECK (
        confirmation_method IS NULL OR confirmation_method IN ('USER_APPROVAL', 'SOURCE_DERIVED')
    ),
    confirmation_ref text CHECK (
        confirmation_ref IS NULL OR (btrim(confirmation_ref) <> '' AND octet_length(confirmation_ref) <= 512)
    ),
    valid_from timestamptz,
    valid_to timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_knowledge_relation_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_knowledge_relation_workspace_fingerprint UNIQUE (workspace_id, fingerprint),
    CONSTRAINT uq_knowledge_relation_edge UNIQUE (
        workspace_id, source_node_type, source_node_id,
        target_node_type, target_node_id, relation_type
    ),
    CONSTRAINT knowledge_relation_distinct_endpoints CHECK (
        source_node_type <> target_node_type OR source_node_id <> target_node_id
    ),
    CONSTRAINT knowledge_relation_symmetric_canonical CHECK (
        relation_type NOT IN ('DUPLICATES', 'CONFLICTS_WITH')
        OR source_node_type < target_node_type
        OR (source_node_type = target_node_type AND source_node_id::text < target_node_id::text)
    ),
    CONSTRAINT knowledge_relation_confirmation_pair CHECK (
        (confirmation_method IS NULL AND confirmation_ref IS NULL)
        OR (confirmation_method IS NOT NULL AND confirmation_ref IS NOT NULL)
    ),
    CONSTRAINT knowledge_relation_confirmation_binding CHECK (
        (
            status IN ('CONFIRMED', 'STALE', 'DEPRECATED')
            AND confirmation_method IS NOT NULL
            AND confirmation_ref IS NOT NULL
            AND evidence_fingerprint IS NOT NULL
        )
        OR (status = 'SUGGESTED' AND confirmation_method IS NULL AND confirmation_ref IS NULL)
        OR status = 'REJECTED'
    ),
    CONSTRAINT knowledge_relation_validity CHECK (
        valid_to IS NULL OR (valid_from IS NOT NULL AND valid_to > valid_from)
    ),
    CONSTRAINT knowledge_relation_time_order CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS core.relation_evidence (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    relation_id uuid NOT NULL,
    source_version_id uuid NOT NULL REFERENCES core.source_version(id) ON DELETE RESTRICT,
    source_span_id uuid NOT NULL REFERENCES ingestion.source_span(id) ON DELETE RESTRICT,
    reason text NOT NULL CHECK (btrim(reason) <> '' AND octet_length(reason) <= 4096),
    evidence_hash text NOT NULL CHECK (evidence_hash ~ '^[0-9a-f]{64}$'),
    applicability jsonb NOT NULL CHECK (
        jsonb_typeof(applicability) = 'object'
        AND octet_length(applicability::text) <= 16384
    ),
    applicability_schema_version text NOT NULL
        CHECK (applicability_schema_version = 'knowledge-applicability/v1'),
    applicability_hash text NOT NULL CHECK (applicability_hash ~ '^[0-9a-f]{64}$'),
    model_run_ref text CHECK (
        model_run_ref IS NULL OR (btrim(model_run_ref) <> '' AND octet_length(model_run_ref) <= 512)
    ),
    confirmation_method text CHECK (
        confirmation_method IS NULL OR confirmation_method IN ('USER_APPROVAL', 'SOURCE_DERIVED')
    ),
    confirmed_by text CHECK (
        confirmed_by IS NULL OR (btrim(confirmed_by) <> '' AND octet_length(confirmed_by) <= 512)
    ),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_knowledge_relation_evidence_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT uq_knowledge_relation_evidence_workspace_hash UNIQUE (workspace_id, evidence_hash),
    CONSTRAINT fk_knowledge_relation_evidence_relation_workspace
        FOREIGN KEY (relation_id, workspace_id)
        REFERENCES core.relation(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT knowledge_relation_evidence_confirmation_pair CHECK (
        (confirmation_method IS NULL AND confirmed_by IS NULL)
        OR (confirmation_method IS NOT NULL AND confirmed_by IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS core.conflict (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    topic_id uuid,
    status text NOT NULL CHECK (
        status IN (
            'OPEN', 'INVESTIGATING', 'RESOLUTION_PROPOSED',
            'RESOLVED', 'ACCEPTED_DIVERGENCE', 'DEFERRED'
        )
    ),
    severity text NOT NULL CHECK (severity IN ('CRITICAL', 'HIGH', 'MEDIUM', 'LOW')),
    summary text NOT NULL CHECK (btrim(summary) <> '' AND octet_length(summary) <= 4096),
    applicability_assessment text NOT NULL CHECK (
        applicability_assessment IN ('EXACT', 'REVIEWED_OVERLAP')
    ),
    applicability_hash text CHECK (
        applicability_hash IS NULL OR applicability_hash ~ '^[0-9a-f]{64}$'
    ),
    overlap_reason text CHECK (
        overlap_reason IS NULL OR (btrim(overlap_reason) <> '' AND octet_length(overlap_reason) <= 4096)
    ),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    resolution text CHECK (
        resolution IS NULL OR (btrim(resolution) <> '' AND octet_length(resolution) <= 16384)
    ),
    resolution_reference text CHECK (
        resolution_reference IS NULL
        OR (btrim(resolution_reference) <> '' AND octet_length(resolution_reference) <= 512)
    ),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    resolved_at timestamptz,
    CONSTRAINT uq_knowledge_conflict_id_workspace UNIQUE (id, workspace_id),
    CONSTRAINT fk_knowledge_conflict_topic_workspace
        FOREIGN KEY (topic_id, workspace_id)
        REFERENCES core.topic(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT knowledge_conflict_assessment_payload CHECK (
        (applicability_assessment = 'EXACT' AND applicability_hash IS NOT NULL AND overlap_reason IS NULL)
        OR (
            applicability_assessment = 'REVIEWED_OVERLAP'
            AND applicability_hash IS NULL
            AND overlap_reason IS NOT NULL
        )
    ),
    CONSTRAINT knowledge_conflict_resolution_binding CHECK (
        (
            status IN ('RESOLVED', 'ACCEPTED_DIVERGENCE')
            AND resolution IS NOT NULL
            AND resolution_reference IS NOT NULL
            AND resolved_at IS NOT NULL
        )
        OR (
            status NOT IN ('RESOLVED', 'ACCEPTED_DIVERGENCE')
            AND resolution IS NULL
            AND resolution_reference IS NULL
            AND resolved_at IS NULL
        )
    ),
    CONSTRAINT knowledge_conflict_time_order CHECK (
        updated_at >= created_at AND (resolved_at IS NULL OR resolved_at >= created_at)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_knowledge_conflict_open_fingerprint
    ON core.conflict (workspace_id, fingerprint)
    WHERE status NOT IN ('RESOLVED', 'ACCEPTED_DIVERGENCE');

CREATE TABLE IF NOT EXISTS core.conflict_member (
    conflict_id uuid NOT NULL,
    claim_id uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    applicability jsonb NOT NULL CHECK (
        jsonb_typeof(applicability) = 'object'
        AND octet_length(applicability::text) <= 16384
    ),
    applicability_schema_version text NOT NULL
        CHECK (applicability_schema_version = 'knowledge-applicability/v1'),
    applicability_hash text NOT NULL CHECK (applicability_hash ~ '^[0-9a-f]{64}$'),
    position_summary text NOT NULL CHECK (
        btrim(position_summary) <> '' AND octet_length(position_summary) <= 4096
    ),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (conflict_id, claim_id),
    CONSTRAINT fk_knowledge_conflict_member_conflict_workspace
        FOREIGN KEY (conflict_id, workspace_id)
        REFERENCES core.conflict(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_knowledge_conflict_member_claim_workspace
        FOREIGN KEY (claim_id, workspace_id)
        REFERENCES core.claim(id, workspace_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS core.knowledge_command_receipt (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (
        btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 128
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (command_type IN (
        'topic.create',
        'claim.suggest', 'claim.confirm', 'claim.transition',
        'relation.suggest', 'relation.confirm', 'relation.transition',
        'conflict.open', 'conflict.transition'
    )),
    aggregate_type text NOT NULL CHECK (aggregate_type IN ('TOPIC', 'CLAIM', 'RELATION', 'CONFLICT')),
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key),
    CONSTRAINT knowledge_command_receipt_type_binding CHECK (
        (command_type = 'topic.create' AND aggregate_type = 'TOPIC')
        OR (command_type IN ('claim.suggest', 'claim.confirm', 'claim.transition') AND aggregate_type = 'CLAIM')
        OR (command_type IN ('relation.suggest', 'relation.confirm', 'relation.transition') AND aggregate_type = 'RELATION')
        OR (command_type IN ('conflict.open', 'conflict.transition') AND aggregate_type = 'CONFLICT')
    )
);

-- Knowledge provenance is always a concrete Source Version import path plus an
-- immutable Source Span. A span_id alone is insufficient because one Parse
-- Projection may be shared by multiple Source Versions.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_provenance_binding(
    expected_workspace_id uuid,
    expected_source_version_id uuid,
    expected_source_span_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS (
        SELECT 1
          FROM core.source_version source_version
          JOIN core.source source
            ON source.id = source_version.source_id
           AND source.workspace_id = expected_workspace_id
          JOIN core.content_artifact artifact
            ON artifact.id = source_version.content_artifact_id
           AND artifact.workspace_id = expected_workspace_id
           AND artifact.content_hash = source_version.content_hash
           AND artifact.byte_size = source_version.byte_size
          JOIN ingestion.source_version_projection source_projection
            ON source_projection.source_version_id = source_version.id
           AND source_projection.workspace_id = expected_workspace_id
          JOIN ingestion.parse_projection parse_projection
            ON parse_projection.id = source_projection.parse_projection_id
           AND parse_projection.workspace_id = expected_workspace_id
           AND parse_projection.content_artifact_id = artifact.id
          JOIN ingestion.source_span source_span
            ON source_span.id = expected_source_span_id
           AND source_span.workspace_id = expected_workspace_id
           AND source_span.content_artifact_id = artifact.id
           AND source_span.parse_projection_id = parse_projection.id
           AND source_span.parser_version = parse_projection.parser_version
           AND source_span.schema_version = parse_projection.schema_version
         WHERE source_version.id = expected_source_version_id
    );
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_reject_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'knowledge record is immutable' USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_topic_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    merged_target_status text;
BEGIN
    PERFORM id FROM core.workspace WHERE id = NEW.workspace_id FOR KEY SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'knowledge topic workspace does not exist' USING ERRCODE = '23514';
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'ACTIVE' OR NEW.version <> 1 THEN
            RAISE EXCEPTION 'knowledge topic must start active at version one' USING ERRCODE = '23514';
        END IF;
        PERFORM pg_advisory_xact_lock(
            hashtextextended(NEW.workspace_id::text || E'\\x1f' || NEW.normalized_name, 0)
        );
        IF EXISTS (
            SELECT 1 FROM core.topic_alias
             WHERE workspace_id = NEW.workspace_id
               AND normalized_alias = NEW.normalized_name
        ) THEN
            RAISE EXCEPTION 'knowledge topic name collides with an alias' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.name <> OLD.name
       OR NEW.normalized_name <> OLD.normalized_name
       OR NEW.created_at <> OLD.created_at
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'knowledge topic immutable binding or version changed' USING ERRCODE = '23514';
    END IF;
    IF OLD.status IN ('MERGED', 'DEPRECATED') THEN
        RAISE EXCEPTION 'historical knowledge topic is immutable' USING ERRCODE = '55000';
    END IF;
    IF NEW.status <> OLD.status AND NOT (
        OLD.status = 'ACTIVE' AND NEW.status IN ('MERGED', 'DEPRECATED')
    ) THEN
        RAISE EXCEPTION 'knowledge topic transition is invalid' USING ERRCODE = '23514';
    END IF;
    IF NEW.status = 'MERGED' THEN
        SELECT status INTO merged_target_status
          FROM core.topic
         WHERE id = NEW.merged_into_topic_id
           AND workspace_id = NEW.workspace_id
         FOR SHARE;
        IF merged_target_status IS DISTINCT FROM 'ACTIVE' THEN
            RAISE EXCEPTION 'knowledge topic merge target is invalid' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_topic_alias_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner_status text;
BEGIN
    PERFORM id FROM core.workspace WHERE id = NEW.workspace_id FOR KEY SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'knowledge topic alias workspace does not exist' USING ERRCODE = '23514';
    END IF;
    PERFORM pg_advisory_xact_lock(
        hashtextextended(NEW.workspace_id::text || E'\\x1f' || NEW.normalized_alias, 0)
    );
    SELECT status INTO owner_status
      FROM core.topic
     WHERE id = NEW.topic_id
       AND workspace_id = NEW.workspace_id
     FOR SHARE;
    IF owner_status IS DISTINCT FROM 'ACTIVE' THEN
        RAISE EXCEPTION 'knowledge topic alias owner is not active' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (
        SELECT 1 FROM core.topic
         WHERE workspace_id = NEW.workspace_id
           AND normalized_name = NEW.normalized_alias
    ) THEN
        RAISE EXCEPTION 'knowledge topic alias collides with a topic name' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_claim_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'SUGGESTED' OR NEW.version <> 1 THEN
            RAISE EXCEPTION 'knowledge claim has an invalid initial state' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.statement <> OLD.statement
       OR NEW.normalized_statement <> OLD.normalized_statement
       OR NEW.applicability <> OLD.applicability
       OR NEW.applicability_schema_version <> OLD.applicability_schema_version
       OR NEW.applicability_hash <> OLD.applicability_hash
       OR NEW.fingerprint <> OLD.fingerprint
       OR NEW.created_at <> OLD.created_at
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'knowledge claim immutable binding or version changed' USING ERRCODE = '23514';
    END IF;
    IF OLD.status IN ('SUPERSEDED', 'DEPRECATED', 'INVALID') THEN
        RAISE EXCEPTION 'terminal knowledge claim is immutable' USING ERRCODE = '55000';
    END IF;
    IF NEW.status <> OLD.status AND NOT (
        (OLD.status = 'SUGGESTED' AND NEW.status IN ('CONFIRMED', 'INVALID'))
        OR (OLD.status = 'CONFIRMED' AND NEW.status IN ('DISPUTED', 'SUPERSEDED', 'DEPRECATED', 'INVALID'))
        OR (OLD.status = 'DISPUTED' AND NEW.status IN ('CONFIRMED', 'SUPERSEDED', 'DEPRECATED', 'INVALID'))
    ) THEN
        RAISE EXCEPTION 'knowledge claim transition is invalid' USING ERRCODE = '23514';
    END IF;
    IF OLD.status = 'DISPUTED' AND NEW.status = 'CONFIRMED' AND EXISTS (
        SELECT 1
          FROM core.conflict_member member
          JOIN core.conflict conflict
            ON conflict.id = member.conflict_id
           AND conflict.workspace_id = member.workspace_id
         WHERE member.workspace_id = NEW.workspace_id
           AND member.claim_id = NEW.id
           AND conflict.status NOT IN ('RESOLVED', 'ACCEPTED_DIVERGENCE')
    ) THEN
        RAISE EXCEPTION 'knowledge claim remains in an unresolved conflict' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_provenance_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT core.knowledge_validate_provenance_binding(
        NEW.workspace_id, NEW.source_version_id, NEW.source_span_id
    ) THEN
        RAISE EXCEPTION 'knowledge provenance binding is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_verify_claim_confirmation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner_id uuid;
    owner_workspace_id uuid;
    owner_status text;
BEGIN
    IF TG_TABLE_NAME = 'claim' THEN
        owner_id := NEW.id;
        owner_workspace_id := NEW.workspace_id;
        owner_status := NEW.status;
    ELSE
        owner_id := NEW.claim_id;
        owner_workspace_id := NEW.workspace_id;
        SELECT status INTO owner_status
          FROM core.claim
         WHERE id = owner_id AND workspace_id = owner_workspace_id;
    END IF;

    IF owner_status = 'CONFIRMED' AND NOT EXISTS (
        SELECT 1 FROM core.claim_source
         WHERE claim_id = owner_id
           AND workspace_id = owner_workspace_id
           AND support_type = 'SUPPORTS'
    ) THEN
        RAISE EXCEPTION 'confirmed knowledge claim requires supporting provenance' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_relation_endpoint(
    expected_workspace_id uuid,
    endpoint_type text,
    endpoint_id uuid
)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF endpoint_type = 'TOPIC' THEN
        PERFORM id FROM core.topic
         WHERE id = endpoint_id
           AND workspace_id = expected_workspace_id
           AND status = 'ACTIVE'
         FOR SHARE;
    ELSIF endpoint_type = 'CLAIM' THEN
        PERFORM id FROM core.claim
         WHERE id = endpoint_id
           AND workspace_id = expected_workspace_id
           AND status IN ('SUGGESTED', 'CONFIRMED', 'DISPUTED')
         FOR SHARE;
    ELSE
        RAISE EXCEPTION 'knowledge relation node type is invalid' USING ERRCODE = '23514';
    END IF;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'knowledge relation endpoint is missing or inactive' USING ERRCODE = '23514';
    END IF;
END;
$$;
-- +goose StatementEnd

-- Historical Relation transitions must remain possible after an endpoint was
-- merged, superseded, deprecated, or invalidated. This verifier preserves the
-- Workspace identity without reopening that endpoint for a live Relation.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_relation_endpoint_identity(
    expected_workspace_id uuid,
    endpoint_type text,
    endpoint_id uuid
)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF endpoint_type = 'TOPIC' THEN
        PERFORM id FROM core.topic
         WHERE id = endpoint_id
           AND workspace_id = expected_workspace_id
         FOR KEY SHARE;
    ELSIF endpoint_type = 'CLAIM' THEN
        PERFORM id FROM core.claim
         WHERE id = endpoint_id
           AND workspace_id = expected_workspace_id
         FOR KEY SHARE;
    ELSE
        RAISE EXCEPTION 'knowledge relation node type is invalid' USING ERRCODE = '23514';
    END IF;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'knowledge relation endpoint identity is missing' USING ERRCODE = '23514';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_relation_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    compatible boolean;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.id <> OLD.id
           OR NEW.workspace_id <> OLD.workspace_id
           OR NEW.source_node_type <> OLD.source_node_type
           OR NEW.source_node_id <> OLD.source_node_id
           OR NEW.target_node_type <> OLD.target_node_type
           OR NEW.target_node_id <> OLD.target_node_id
           OR NEW.relation_type <> OLD.relation_type
           OR NEW.fingerprint <> OLD.fingerprint
           OR NEW.valid_from IS DISTINCT FROM OLD.valid_from
           OR NEW.created_at <> OLD.created_at
           OR NEW.version <> OLD.version + 1
           OR NEW.updated_at < OLD.updated_at
           OR (OLD.valid_to IS NOT NULL AND NEW.valid_to IS DISTINCT FROM OLD.valid_to)
           OR (
                (NEW.confirmation_method IS DISTINCT FROM OLD.confirmation_method
                 OR NEW.confirmation_ref IS DISTINCT FROM OLD.confirmation_ref)
                AND NOT (
                    OLD.status = 'STALE'
                    AND NEW.status = 'CONFIRMED'
                    AND NEW.confirmation_method IS NOT NULL
                    AND NEW.confirmation_ref IS NOT NULL
                )
                AND NOT (
                    OLD.status = 'STALE'
                    AND NEW.status = 'REJECTED'
                    AND NEW.confirmation_method IS NULL
                    AND NEW.confirmation_ref IS NULL
                )
                AND NOT (
                    OLD.status = 'REJECTED'
                    AND NEW.status = 'SUGGESTED'
                    AND NEW.confirmation_method IS NULL
                    AND NEW.confirmation_ref IS NULL
                )
                AND NOT (
                    OLD.confirmation_method IS NULL
                    AND OLD.confirmation_ref IS NULL
                    AND NEW.status = 'CONFIRMED'
                    AND NEW.confirmation_method IS NOT NULL
                    AND NEW.confirmation_ref IS NOT NULL
                )
           ) THEN
            RAISE EXCEPTION 'knowledge relation immutable binding or version changed' USING ERRCODE = '23514';
        END IF;
        IF OLD.status = 'DEPRECATED' THEN
            RAISE EXCEPTION 'deprecated knowledge relation is immutable' USING ERRCODE = '55000';
        END IF;
        IF NEW.status <> OLD.status AND NOT (
            (OLD.status = 'SUGGESTED' AND NEW.status IN ('CONFIRMED', 'REJECTED'))
            OR (OLD.status = 'CONFIRMED' AND NEW.status IN ('STALE', 'DEPRECATED'))
            OR (OLD.status = 'STALE' AND NEW.status IN ('CONFIRMED', 'REJECTED', 'DEPRECATED'))
            OR (OLD.status = 'REJECTED' AND NEW.status = 'SUGGESTED')
        ) THEN
            RAISE EXCEPTION 'knowledge relation transition is invalid' USING ERRCODE = '23514';
        END IF;
        IF OLD.status = 'REJECTED' AND NEW.status = 'SUGGESTED' THEN
            IF NEW.evidence_fingerprint IS NULL
               OR NEW.evidence_fingerprint IS NOT DISTINCT FROM OLD.evidence_fingerprint THEN
                RAISE EXCEPTION 'knowledge relation resuggestion requires new evidence' USING ERRCODE = '23514';
            END IF;
        ELSIF NEW.evidence_fingerprint IS DISTINCT FROM OLD.evidence_fingerprint
              AND NEW.status NOT IN ('SUGGESTED', 'CONFIRMED') THEN
            RAISE EXCEPTION 'knowledge relation evidence fingerprint changed outside resuggestion' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.status <> 'SUGGESTED' OR NEW.version <> 1 THEN
        RAISE EXCEPTION 'knowledge relation has an invalid initial state' USING ERRCODE = '23514';
    END IF;

    IF TG_OP = 'INSERT' OR NEW.status IN ('SUGGESTED', 'CONFIRMED') THEN
        PERFORM core.knowledge_validate_relation_endpoint(
            NEW.workspace_id, NEW.source_node_type, NEW.source_node_id
        );
        PERFORM core.knowledge_validate_relation_endpoint(
            NEW.workspace_id, NEW.target_node_type, NEW.target_node_id
        );
    ELSE
        PERFORM core.knowledge_validate_relation_endpoint_identity(
            NEW.workspace_id, NEW.source_node_type, NEW.source_node_id
        );
        PERFORM core.knowledge_validate_relation_endpoint_identity(
            NEW.workspace_id, NEW.target_node_type, NEW.target_node_id
        );
    END IF;

    compatible := CASE NEW.relation_type
        WHEN 'CITES' THEN NEW.source_node_type = 'CLAIM' AND NEW.target_node_type = 'CLAIM'
        WHEN 'DERIVED_FROM' THEN NEW.source_node_type = 'CLAIM' AND NEW.target_node_type = 'CLAIM'
        WHEN 'BELONGS_TO' THEN NEW.source_node_type = 'CLAIM' AND NEW.target_node_type = 'TOPIC'
        WHEN 'SUPPORTS' THEN NEW.source_node_type = 'CLAIM' AND NEW.target_node_type = 'CLAIM'
        WHEN 'CONFLICTS_WITH' THEN NEW.source_node_type = 'CLAIM' AND NEW.target_node_type = 'CLAIM'
        WHEN 'COMPLEMENTS' THEN NEW.source_node_type = NEW.target_node_type
        WHEN 'DUPLICATES' THEN NEW.source_node_type = NEW.target_node_type
        WHEN 'PREREQUISITE_OF' THEN NEW.source_node_type = NEW.target_node_type
        WHEN 'VERSION_OF' THEN NEW.source_node_type = NEW.target_node_type
        WHEN 'IMPACTS' THEN true
        ELSE false
    END;
    IF NOT compatible THEN
        RAISE EXCEPTION 'knowledge relation endpoint combination is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_verify_relation_confirmation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner_id uuid;
    owner_workspace_id uuid;
    owner_status text;
    owner_confirmation_method text;
    owner_confirmation_ref text;
BEGIN
    IF TG_TABLE_NAME = 'relation' THEN
        owner_id := NEW.id;
        owner_workspace_id := NEW.workspace_id;
        owner_status := NEW.status;
        owner_confirmation_method := NEW.confirmation_method;
        owner_confirmation_ref := NEW.confirmation_ref;
    ELSE
        owner_id := NEW.relation_id;
        owner_workspace_id := NEW.workspace_id;
        SELECT status, confirmation_method, confirmation_ref
          INTO owner_status, owner_confirmation_method, owner_confirmation_ref
          FROM core.relation
         WHERE id = owner_id AND workspace_id = owner_workspace_id;
    END IF;

    IF owner_status = 'CONFIRMED' AND (
        owner_confirmation_method IS NULL
        OR owner_confirmation_ref IS NULL
        OR NOT EXISTS (
            SELECT 1 FROM core.relation_evidence
             WHERE relation_id = owner_id
               AND workspace_id = owner_workspace_id
               AND confirmation_method = owner_confirmation_method
               AND confirmed_by = owner_confirmation_ref
        )
    ) THEN
        RAISE EXCEPTION 'confirmed knowledge relation requires confirmed evidence' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_conflict_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'OPEN' OR NEW.version <> 1 THEN
            RAISE EXCEPTION 'knowledge conflict must start open at version one' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.topic_id IS DISTINCT FROM OLD.topic_id
       OR NEW.applicability_assessment <> OLD.applicability_assessment
       OR NEW.applicability_hash IS DISTINCT FROM OLD.applicability_hash
       OR NEW.overlap_reason IS DISTINCT FROM OLD.overlap_reason
       OR NEW.fingerprint <> OLD.fingerprint
       OR NEW.created_at <> OLD.created_at
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at
       OR (OLD.resolution_reference IS NOT NULL
           AND NEW.resolution_reference IS DISTINCT FROM OLD.resolution_reference) THEN
        RAISE EXCEPTION 'knowledge conflict immutable binding or version changed' USING ERRCODE = '23514';
    END IF;
    IF OLD.status IN ('RESOLVED', 'ACCEPTED_DIVERGENCE') THEN
        RAISE EXCEPTION 'terminal knowledge conflict is immutable' USING ERRCODE = '55000';
    END IF;
    IF NEW.status <> OLD.status AND NOT (
        (OLD.status = 'OPEN' AND NEW.status IN ('INVESTIGATING', 'DEFERRED'))
        OR (OLD.status = 'INVESTIGATING' AND NEW.status IN ('RESOLUTION_PROPOSED', 'DEFERRED'))
        OR (OLD.status = 'DEFERRED' AND NEW.status = 'INVESTIGATING')
        OR (
            OLD.status = 'RESOLUTION_PROPOSED'
            AND NEW.status IN ('RESOLVED', 'ACCEPTED_DIVERGENCE', 'INVESTIGATING')
        )
    ) THEN
        RAISE EXCEPTION 'knowledge conflict transition is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_conflict_member_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    claim_status text;
    claim_applicability jsonb;
    claim_schema_version text;
    claim_applicability_hash text;
BEGIN
    SELECT status, applicability, applicability_schema_version, applicability_hash
      INTO claim_status, claim_applicability, claim_schema_version, claim_applicability_hash
      FROM core.claim
     WHERE id = NEW.claim_id
       AND workspace_id = NEW.workspace_id
     FOR SHARE;
    IF claim_status IS NULL OR claim_status NOT IN ('CONFIRMED', 'DISPUTED') THEN
        RAISE EXCEPTION 'knowledge conflict member claim is not disputable' USING ERRCODE = '23514';
    END IF;
    IF NEW.applicability <> claim_applicability
       OR NEW.applicability_schema_version <> claim_schema_version
       OR NEW.applicability_hash <> claim_applicability_hash THEN
        RAISE EXCEPTION 'knowledge conflict member applicability snapshot is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_verify_conflict_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner_id uuid;
    owner_workspace_id uuid;
    owner_status text;
    owner_assessment text;
    owner_applicability_hash text;
    owner_resolution text;
    member_count bigint;
    distinct_hash_count bigint;
    exact_hash text;
BEGIN
    IF TG_TABLE_NAME = 'conflict' THEN
        owner_id := NEW.id;
        owner_workspace_id := NEW.workspace_id;
        owner_status := NEW.status;
        owner_assessment := NEW.applicability_assessment;
        owner_applicability_hash := NEW.applicability_hash;
        owner_resolution := NEW.resolution;
    ELSE
        owner_id := NEW.conflict_id;
        owner_workspace_id := NEW.workspace_id;
        SELECT status, applicability_assessment, applicability_hash, resolution
          INTO owner_status, owner_assessment, owner_applicability_hash, owner_resolution
          FROM core.conflict
         WHERE id = owner_id AND workspace_id = owner_workspace_id;
    END IF;

    SELECT count(*), count(DISTINCT applicability_hash), min(applicability_hash)
      INTO member_count, distinct_hash_count, exact_hash
      FROM core.conflict_member
     WHERE conflict_id = owner_id
       AND workspace_id = owner_workspace_id;

    IF member_count < 2 THEN
        RAISE EXCEPTION 'knowledge conflict requires at least two claims' USING ERRCODE = '55000';
    END IF;
    IF EXISTS (
        SELECT 1
          FROM core.conflict_member member
          JOIN core.claim claim
            ON claim.id = member.claim_id
           AND claim.workspace_id = member.workspace_id
         WHERE member.conflict_id = owner_id
           AND member.workspace_id = owner_workspace_id
           AND (
                member.applicability <> claim.applicability
                OR member.applicability_schema_version <> claim.applicability_schema_version
                OR member.applicability_hash <> claim.applicability_hash
           )
    ) THEN
        RAISE EXCEPTION 'knowledge conflict member snapshot drifted' USING ERRCODE = '55000';
    END IF;
    IF owner_assessment = 'EXACT' AND (
        distinct_hash_count <> 1 OR owner_applicability_hash IS DISTINCT FROM exact_hash
    ) THEN
        RAISE EXCEPTION 'knowledge exact conflict applicability is inconsistent' USING ERRCODE = '55000';
    END IF;
    IF owner_assessment = 'REVIEWED_OVERLAP' AND distinct_hash_count < 2 THEN
        RAISE EXCEPTION 'knowledge reviewed overlap requires different applicability hashes' USING ERRCODE = '55000';
    END IF;
    IF owner_status NOT IN ('RESOLVED', 'ACCEPTED_DIVERGENCE') AND EXISTS (
        SELECT 1
          FROM core.conflict_member member
          JOIN core.claim claim
            ON claim.id = member.claim_id
           AND claim.workspace_id = member.workspace_id
         WHERE member.conflict_id = owner_id
           AND member.workspace_id = owner_workspace_id
           AND claim.status <> 'DISPUTED'
    ) THEN
        RAISE EXCEPTION 'unresolved knowledge conflict requires disputed claims' USING ERRCODE = '55000';
    END IF;
    IF owner_status = 'ACCEPTED_DIVERGENCE' AND (
        distinct_hash_count < 2 OR owner_resolution IS NULL
    ) THEN
        RAISE EXCEPTION 'accepted divergence requires distinct applicability and resolution' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- A Claim participating in an unresolved Conflict must remain DISPUTED. The
-- deferred check still permits one transaction to terminate every owning
-- Conflict before moving the Claim to a later lifecycle state.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_verify_claim_conflict_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status <> 'DISPUTED' AND EXISTS (
        SELECT 1
          FROM core.conflict_member member
          JOIN core.conflict conflict
            ON conflict.id = member.conflict_id
           AND conflict.workspace_id = member.workspace_id
         WHERE member.claim_id = NEW.id
           AND member.workspace_id = NEW.workspace_id
           AND conflict.status NOT IN ('RESOLVED', 'ACCEPTED_DIVERGENCE')
    ) THEN
        RAISE EXCEPTION 'unresolved knowledge conflict requires disputed claims'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Active Relations may only point at live Claim endpoints. The deferred check
-- permits one transaction to retire every affected Relation before retiring
-- the Claim, while preventing a valid edge from referencing a historical fact.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_verify_claim_relation_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status NOT IN ('SUGGESTED', 'CONFIRMED', 'DISPUTED') AND EXISTS (
        SELECT 1
          FROM core.relation relation
         WHERE relation.workspace_id = NEW.workspace_id
           AND relation.status IN ('SUGGESTED', 'CONFIRMED')
           AND (
                (relation.source_node_type = 'CLAIM' AND relation.source_node_id = NEW.id)
                OR (relation.target_node_type = 'CLAIM' AND relation.target_node_id = NEW.id)
           )
    ) THEN
        RAISE EXCEPTION 'historical knowledge claim cannot retain an active relation'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Active Relations may only point at ACTIVE Topics. As with Claim retirement,
-- the deferred check permits Relation retirement and Topic retirement in one
-- transaction but prevents a live edge from surviving the lifecycle change.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_verify_topic_relation_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status <> 'ACTIVE' AND EXISTS (
        SELECT 1
          FROM core.relation relation
         WHERE relation.workspace_id = NEW.workspace_id
           AND relation.status IN ('SUGGESTED', 'CONFIRMED')
           AND (
                (relation.source_node_type = 'TOPIC' AND relation.source_node_id = NEW.id)
                OR (relation.target_node_type = 'TOPIC' AND relation.target_node_id = NEW.id)
           )
    ) THEN
        RAISE EXCEPTION 'historical knowledge topic cannot retain an active relation'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION core.knowledge_validate_command_receipt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_version bigint;
BEGIN
    IF NEW.aggregate_type = 'TOPIC' THEN
        SELECT version INTO current_version FROM core.topic
         WHERE id = NEW.aggregate_id AND workspace_id = NEW.workspace_id FOR KEY SHARE;
    ELSIF NEW.aggregate_type = 'CLAIM' THEN
        SELECT version INTO current_version FROM core.claim
         WHERE id = NEW.aggregate_id AND workspace_id = NEW.workspace_id FOR KEY SHARE;
    ELSIF NEW.aggregate_type = 'RELATION' THEN
        SELECT version INTO current_version FROM core.relation
         WHERE id = NEW.aggregate_id AND workspace_id = NEW.workspace_id FOR KEY SHARE;
    ELSIF NEW.aggregate_type = 'CONFLICT' THEN
        SELECT version INTO current_version FROM core.conflict
         WHERE id = NEW.aggregate_id AND workspace_id = NEW.workspace_id FOR KEY SHARE;
    END IF;
    IF current_version IS NULL OR current_version <> NEW.aggregate_version THEN
        RAISE EXCEPTION 'knowledge command receipt aggregate binding is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS knowledge_topic_validate_write ON core.topic;
CREATE TRIGGER knowledge_topic_validate_write
    BEFORE INSERT OR UPDATE ON core.topic
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_validate_topic_write();

DROP TRIGGER IF EXISTS knowledge_topic_reject_delete ON core.topic;
CREATE TRIGGER knowledge_topic_reject_delete
    BEFORE DELETE ON core.topic
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_reject_immutable_mutation();

DROP TRIGGER IF EXISTS knowledge_topic_alias_validate_insert ON core.topic_alias;
CREATE TRIGGER knowledge_topic_alias_validate_insert
    BEFORE INSERT ON core.topic_alias
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_validate_topic_alias_insert();

DROP TRIGGER IF EXISTS knowledge_topic_alias_reject_mutation ON core.topic_alias;
CREATE TRIGGER knowledge_topic_alias_reject_mutation
    BEFORE UPDATE OR DELETE ON core.topic_alias
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_reject_immutable_mutation();

DROP TRIGGER IF EXISTS knowledge_claim_validate_write ON core.claim;
CREATE TRIGGER knowledge_claim_validate_write
    BEFORE INSERT OR UPDATE ON core.claim
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_validate_claim_write();

DROP TRIGGER IF EXISTS knowledge_claim_reject_delete ON core.claim;
CREATE TRIGGER knowledge_claim_reject_delete
    BEFORE DELETE ON core.claim
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_reject_immutable_mutation();

DROP TRIGGER IF EXISTS knowledge_claim_source_validate_provenance ON core.claim_source;
CREATE TRIGGER knowledge_claim_source_validate_provenance
    BEFORE INSERT ON core.claim_source
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_validate_provenance_insert();

DROP TRIGGER IF EXISTS knowledge_claim_source_reject_mutation ON core.claim_source;
CREATE TRIGGER knowledge_claim_source_reject_mutation
    BEFORE UPDATE OR DELETE ON core.claim_source
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_reject_immutable_mutation();

DROP TRIGGER IF EXISTS knowledge_claim_verify_confirmation ON core.claim;
CREATE CONSTRAINT TRIGGER knowledge_claim_verify_confirmation
    AFTER INSERT OR UPDATE ON core.claim
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_verify_claim_confirmation();

DROP TRIGGER IF EXISTS knowledge_claim_source_verify_confirmation ON core.claim_source;
CREATE CONSTRAINT TRIGGER knowledge_claim_source_verify_confirmation
    AFTER INSERT ON core.claim_source
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_verify_claim_confirmation();

DROP TRIGGER IF EXISTS knowledge_relation_validate_write ON core.relation;
CREATE TRIGGER knowledge_relation_validate_write
    BEFORE INSERT OR UPDATE ON core.relation
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_validate_relation_write();

DROP TRIGGER IF EXISTS knowledge_relation_reject_delete ON core.relation;
CREATE TRIGGER knowledge_relation_reject_delete
    BEFORE DELETE ON core.relation
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_reject_immutable_mutation();

DROP TRIGGER IF EXISTS knowledge_relation_evidence_validate_provenance ON core.relation_evidence;
CREATE TRIGGER knowledge_relation_evidence_validate_provenance
    BEFORE INSERT ON core.relation_evidence
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_validate_provenance_insert();

DROP TRIGGER IF EXISTS knowledge_relation_evidence_reject_mutation ON core.relation_evidence;
CREATE TRIGGER knowledge_relation_evidence_reject_mutation
    BEFORE UPDATE OR DELETE ON core.relation_evidence
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_reject_immutable_mutation();

DROP TRIGGER IF EXISTS knowledge_relation_verify_confirmation ON core.relation;
CREATE CONSTRAINT TRIGGER knowledge_relation_verify_confirmation
    AFTER INSERT OR UPDATE ON core.relation
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_verify_relation_confirmation();

DROP TRIGGER IF EXISTS knowledge_relation_evidence_verify_confirmation ON core.relation_evidence;
CREATE CONSTRAINT TRIGGER knowledge_relation_evidence_verify_confirmation
    AFTER INSERT ON core.relation_evidence
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_verify_relation_confirmation();

DROP TRIGGER IF EXISTS knowledge_conflict_validate_write ON core.conflict;
CREATE TRIGGER knowledge_conflict_validate_write
    BEFORE INSERT OR UPDATE ON core.conflict
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_validate_conflict_write();

DROP TRIGGER IF EXISTS knowledge_conflict_reject_delete ON core.conflict;
CREATE TRIGGER knowledge_conflict_reject_delete
    BEFORE DELETE ON core.conflict
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_reject_immutable_mutation();

DROP TRIGGER IF EXISTS knowledge_conflict_member_validate_insert ON core.conflict_member;
CREATE TRIGGER knowledge_conflict_member_validate_insert
    BEFORE INSERT ON core.conflict_member
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_validate_conflict_member_insert();

DROP TRIGGER IF EXISTS knowledge_conflict_member_reject_mutation ON core.conflict_member;
CREATE TRIGGER knowledge_conflict_member_reject_mutation
    BEFORE UPDATE OR DELETE ON core.conflict_member
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_reject_immutable_mutation();

DROP TRIGGER IF EXISTS knowledge_conflict_verify_integrity ON core.conflict;
CREATE CONSTRAINT TRIGGER knowledge_conflict_verify_integrity
    AFTER INSERT OR UPDATE ON core.conflict
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_verify_conflict_integrity();

DROP TRIGGER IF EXISTS knowledge_conflict_member_verify_integrity ON core.conflict_member;
CREATE CONSTRAINT TRIGGER knowledge_conflict_member_verify_integrity
    AFTER INSERT ON core.conflict_member
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_verify_conflict_integrity();

DROP TRIGGER IF EXISTS knowledge_claim_verify_conflict_integrity ON core.claim;
CREATE CONSTRAINT TRIGGER knowledge_claim_verify_conflict_integrity
    AFTER UPDATE ON core.claim
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_verify_claim_conflict_integrity();

DROP TRIGGER IF EXISTS knowledge_claim_verify_relation_integrity ON core.claim;
CREATE CONSTRAINT TRIGGER knowledge_claim_verify_relation_integrity
    AFTER UPDATE ON core.claim
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_verify_claim_relation_integrity();

DROP TRIGGER IF EXISTS knowledge_topic_verify_relation_integrity ON core.topic;
CREATE CONSTRAINT TRIGGER knowledge_topic_verify_relation_integrity
    AFTER UPDATE ON core.topic
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_verify_topic_relation_integrity();

DROP TRIGGER IF EXISTS knowledge_command_receipt_validate_insert ON core.knowledge_command_receipt;
CREATE TRIGGER knowledge_command_receipt_validate_insert
    BEFORE INSERT ON core.knowledge_command_receipt
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_validate_command_receipt_insert();

DROP TRIGGER IF EXISTS knowledge_command_receipt_reject_mutation ON core.knowledge_command_receipt;
CREATE TRIGGER knowledge_command_receipt_reject_mutation
    BEFORE UPDATE OR DELETE ON core.knowledge_command_receipt
    FOR EACH ROW EXECUTE FUNCTION core.knowledge_reject_immutable_mutation();

CREATE INDEX IF NOT EXISTS idx_knowledge_topic_workspace_status
    ON core.topic (workspace_id, status, updated_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_topic_alias_topic
    ON core.topic_alias (workspace_id, topic_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_claim_workspace_status
    ON core.claim (workspace_id, status, updated_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_claim_source_owner
    ON core.claim_source (workspace_id, claim_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_claim_source_provenance
    ON core.claim_source (workspace_id, source_version_id, source_span_id, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_relation_workspace_status
    ON core.relation (workspace_id, status, updated_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_relation_source
    ON core.relation (workspace_id, source_node_type, source_node_id, relation_type, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_relation_target
    ON core.relation (workspace_id, target_node_type, target_node_id, relation_type, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_relation_evidence_owner
    ON core.relation_evidence (workspace_id, relation_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_relation_evidence_provenance
    ON core.relation_evidence (workspace_id, source_version_id, source_span_id, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_conflict_workspace_status
    ON core.conflict (workspace_id, status, updated_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_conflict_member_claim
    ON core.conflict_member (workspace_id, claim_id, conflict_id);
CREATE INDEX IF NOT EXISTS idx_knowledge_command_receipt_aggregate
    ON core.knowledge_command_receipt (workspace_id, aggregate_type, aggregate_id, created_at);

INSERT INTO core.schema_meta (key, value)
VALUES ('knowledge_domain', 'm5-05')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    LOCK TABLE core.knowledge_command_receipt,
               core.conflict_member,
               core.conflict,
               core.relation_evidence,
               core.relation,
               core.claim_source,
               core.claim,
               core.topic_alias,
               core.topic
        IN ACCESS EXCLUSIVE MODE;

    IF EXISTS (SELECT 1 FROM core.knowledge_command_receipt)
       OR EXISTS (SELECT 1 FROM core.conflict_member)
       OR EXISTS (SELECT 1 FROM core.conflict)
       OR EXISTS (SELECT 1 FROM core.relation_evidence)
       OR EXISTS (SELECT 1 FROM core.relation)
       OR EXISTS (SELECT 1 FROM core.claim_source)
       OR EXISTS (SELECT 1 FROM core.claim)
       OR EXISTS (SELECT 1 FROM core.topic_alias)
       OR EXISTS (SELECT 1 FROM core.topic) THEN
        RAISE EXCEPTION 'cannot downgrade Knowledge Domain while knowledge business data exists'
            USING ERRCODE = '55000';
    END IF;
END;
$$;
-- +goose StatementEnd

DELETE FROM core.schema_meta WHERE key = 'knowledge_domain';

DROP TRIGGER IF EXISTS knowledge_command_receipt_reject_mutation ON core.knowledge_command_receipt;
DROP TRIGGER IF EXISTS knowledge_command_receipt_validate_insert ON core.knowledge_command_receipt;
DROP TABLE IF EXISTS core.knowledge_command_receipt;

DROP TRIGGER IF EXISTS knowledge_conflict_member_verify_integrity ON core.conflict_member;
DROP TRIGGER IF EXISTS knowledge_conflict_member_reject_mutation ON core.conflict_member;
DROP TRIGGER IF EXISTS knowledge_conflict_member_validate_insert ON core.conflict_member;
DROP TABLE IF EXISTS core.conflict_member;

DROP TRIGGER IF EXISTS knowledge_conflict_verify_integrity ON core.conflict;
DROP TRIGGER IF EXISTS knowledge_conflict_reject_delete ON core.conflict;
DROP TRIGGER IF EXISTS knowledge_conflict_validate_write ON core.conflict;
DROP TABLE IF EXISTS core.conflict;

DROP TRIGGER IF EXISTS knowledge_relation_evidence_verify_confirmation ON core.relation_evidence;
DROP TRIGGER IF EXISTS knowledge_relation_evidence_reject_mutation ON core.relation_evidence;
DROP TRIGGER IF EXISTS knowledge_relation_evidence_validate_provenance ON core.relation_evidence;
DROP TABLE IF EXISTS core.relation_evidence;

DROP TRIGGER IF EXISTS knowledge_relation_verify_confirmation ON core.relation;
DROP TRIGGER IF EXISTS knowledge_relation_reject_delete ON core.relation;
DROP TRIGGER IF EXISTS knowledge_relation_validate_write ON core.relation;
DROP TABLE IF EXISTS core.relation;

DROP TRIGGER IF EXISTS knowledge_claim_verify_relation_integrity ON core.claim;
DROP TRIGGER IF EXISTS knowledge_claim_verify_conflict_integrity ON core.claim;
DROP TRIGGER IF EXISTS knowledge_claim_source_verify_confirmation ON core.claim_source;
DROP TRIGGER IF EXISTS knowledge_claim_source_reject_mutation ON core.claim_source;
DROP TRIGGER IF EXISTS knowledge_claim_source_validate_provenance ON core.claim_source;
DROP TABLE IF EXISTS core.claim_source;

DROP TRIGGER IF EXISTS knowledge_claim_verify_confirmation ON core.claim;
DROP TRIGGER IF EXISTS knowledge_claim_reject_delete ON core.claim;
DROP TRIGGER IF EXISTS knowledge_claim_validate_write ON core.claim;
DROP TABLE IF EXISTS core.claim;

DROP TRIGGER IF EXISTS knowledge_topic_alias_reject_mutation ON core.topic_alias;
DROP TRIGGER IF EXISTS knowledge_topic_alias_validate_insert ON core.topic_alias;
DROP TABLE IF EXISTS core.topic_alias;

DROP TRIGGER IF EXISTS knowledge_topic_verify_relation_integrity ON core.topic;
DROP TRIGGER IF EXISTS knowledge_topic_reject_delete ON core.topic;
DROP TRIGGER IF EXISTS knowledge_topic_validate_write ON core.topic;
DROP TABLE IF EXISTS core.topic;

DROP FUNCTION IF EXISTS core.knowledge_validate_command_receipt_insert();
DROP FUNCTION IF EXISTS core.knowledge_verify_topic_relation_integrity();
DROP FUNCTION IF EXISTS core.knowledge_verify_claim_relation_integrity();
DROP FUNCTION IF EXISTS core.knowledge_verify_claim_conflict_integrity();
DROP FUNCTION IF EXISTS core.knowledge_verify_conflict_integrity();
DROP FUNCTION IF EXISTS core.knowledge_validate_conflict_member_insert();
DROP FUNCTION IF EXISTS core.knowledge_validate_conflict_write();
DROP FUNCTION IF EXISTS core.knowledge_verify_relation_confirmation();
DROP FUNCTION IF EXISTS core.knowledge_validate_relation_write();
DROP FUNCTION IF EXISTS core.knowledge_validate_relation_endpoint_identity(uuid, text, uuid);
DROP FUNCTION IF EXISTS core.knowledge_validate_relation_endpoint(uuid, text, uuid);
DROP FUNCTION IF EXISTS core.knowledge_verify_claim_confirmation();
DROP FUNCTION IF EXISTS core.knowledge_validate_provenance_insert();
DROP FUNCTION IF EXISTS core.knowledge_validate_claim_write();
DROP FUNCTION IF EXISTS core.knowledge_validate_topic_alias_insert();
DROP FUNCTION IF EXISTS core.knowledge_validate_topic_write();
DROP FUNCTION IF EXISTS core.knowledge_reject_immutable_mutation();
DROP FUNCTION IF EXISTS core.knowledge_validate_provenance_binding(uuid, uuid, uuid);
