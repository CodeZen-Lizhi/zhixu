-- +goose Up

-- Review invalidation is projected into the existing Health owner. The
-- existing Health Timeline trigger then creates the durable Timeline outbox;
-- review_card remains the lifecycle source of truth.
ALTER TABLE ops.health_issue
    DROP CONSTRAINT IF EXISTS health_issue_type_check;
ALTER TABLE ops.health_issue
    DROP CONSTRAINT IF EXISTS ops_health_issue_type_check;
ALTER TABLE ops.health_issue
    ADD CONSTRAINT ops_health_issue_type_check CHECK (type IN (
        'ORPHAN', 'DUPLICATE', 'CONFLICT', 'STALE', 'MISSING_SOURCE',
        'LOW_CONFIDENCE', 'BROKEN_REFERENCE', 'INDEX_ERROR',
        'SUPERSEDED_USAGE', 'REVIEW_INVALIDATED'
    ));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.sync_review_invalidation_health(
    target_workspace_id uuid,
    target_claim_id uuid,
    projection_occurred_at timestamptz
)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    invalidated_count bigint;
    claim_version bigint;
    evidence_hash text;
    projected_identity_hash text;
    projected_fingerprint text;
    effective_occurred_at timestamptz;
    persisted_at timestamptz;
BEGIN
    IF target_workspace_id IS NULL OR target_claim_id IS NULL THEN
        RETURN;
    END IF;

    effective_occurred_at := COALESCE(projection_occurred_at, CURRENT_TIMESTAMP);
    persisted_at := GREATEST(effective_occurred_at, CURRENT_TIMESTAMP);

    SELECT count(*),
           encode(sha256(convert_to(string_agg(
               card.id::text || ':' || card.version::text || ':' || card.invalidation_reason,
               '|' ORDER BY card.id
           ), 'UTF8')), 'hex')
      INTO invalidated_count,evidence_hash
      FROM learning.review_card AS card
     WHERE card.workspace_id = target_workspace_id
       AND card.claim_id = target_claim_id
       AND card.status = 'INVALIDATED';

    projected_identity_hash := encode(sha256(convert_to(
        '{"schema":"health-issue-identity/v1","workspace_id":"' || target_workspace_id::text ||
        '","type":"REVIEW_INVALIDATED","target":{"Type":"CLAIM","ID":"' || target_claim_id::text ||
        '"},"detector_id":"health.detector.review_invalidated"}',
        'UTF8'
    )), 'hex');

    IF invalidated_count > 0 THEN
        SELECT version INTO STRICT claim_version
          FROM core.claim
         WHERE workspace_id = target_workspace_id
           AND id = target_claim_id;

        projected_fingerprint := encode(sha256(convert_to(
            '{"schema":"health-issue-fingerprint/v1","workspace_id":"' || target_workspace_id::text ||
            '","type":"REVIEW_INVALIDATED","target":{"Type":"CLAIM","ID":"' || target_claim_id::text ||
            '"},"detector_id":"health.detector.review_invalidated","detector_version":"detector/v1",' ||
            '"evidence_hashes":["' || evidence_hash || '"],"object_versions":[{"Ref":{"Type":"CLAIM","ID":"' ||
            target_claim_id::text || '"},"Version":' || claim_version::text || '}]}',
            'UTF8'
        )), 'hex');

        INSERT INTO ops.health_issue AS issue(
            id,workspace_id,type,target_type,target_id,detector_id,identity_hash,
            fingerprint_schema_version,fingerprint,detector_version,severity,
            evidence_summary,status,version,first_detected_at,last_detected_at,
            last_verified_at,created_at,updated_at
        ) VALUES (
            gen_random_uuid(),target_workspace_id,'REVIEW_INVALIDATED','CLAIM',target_claim_id,
            'health.detector.review_invalidated',projected_identity_hash,'health-issue-fingerprint/v1',
            projected_fingerprint,'detector/v1','HIGH','Review cards reference invalidated knowledge',
            'OPEN',1,effective_occurred_at,effective_occurred_at,effective_occurred_at,
            persisted_at,persisted_at
        )
        ON CONFLICT (workspace_id,identity_hash) DO UPDATE SET
            fingerprint_schema_version = EXCLUDED.fingerprint_schema_version,
            fingerprint = EXCLUDED.fingerprint,
            detector_version = EXCLUDED.detector_version,
            severity = EXCLUDED.severity,
            evidence_summary = EXCLUDED.evidence_summary,
            status = 'REOPENED',
            status_reason = NULL,
            deferred_until = NULL,
            repair_proposal_id = NULL,
            repair_option_code = NULL,
            repair_binding_fingerprint = NULL,
            repair_binding_object_versions = NULL,
            repair_proposal_created_at = NULL,
            version = issue.version + 1,
            last_detected_at = GREATEST(issue.last_detected_at,EXCLUDED.last_detected_at),
            last_verified_at = GREATEST(issue.last_verified_at,EXCLUDED.last_verified_at),
            resolved_at = NULL,
            updated_at = GREATEST(issue.updated_at,EXCLUDED.updated_at)
        WHERE issue.fingerprint IS DISTINCT FROM EXCLUDED.fingerprint
           OR issue.status = 'RESOLVED';
    ELSE
        UPDATE ops.health_issue
           SET status = 'RESOLVED',
               status_reason = NULL,
               deferred_until = NULL,
               repair_proposal_id = NULL,
               repair_option_code = NULL,
               repair_binding_fingerprint = NULL,
               repair_binding_object_versions = NULL,
               repair_proposal_created_at = NULL,
               version = version + 1,
               last_verified_at = GREATEST(last_verified_at,effective_occurred_at),
               resolved_at = GREATEST(created_at,effective_occurred_at),
               updated_at = GREATEST(updated_at,persisted_at)
         WHERE workspace_id = target_workspace_id
           AND ops.health_issue.identity_hash = projected_identity_hash
           AND status <> 'RESOLVED';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.project_review_card_invalidation_health()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status = 'INVALIDATED' THEN
            PERFORM learning.sync_review_invalidation_health(
                NEW.workspace_id,NEW.claim_id,COALESCE(NEW.invalidated_at,NEW.updated_at)
            );
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.status = 'INVALIDATED' AND OLD.claim_id IS DISTINCT FROM NEW.claim_id THEN
        PERFORM learning.sync_review_invalidation_health(
            OLD.workspace_id,OLD.claim_id,COALESCE(NEW.updated_at,CURRENT_TIMESTAMP)
        );
    END IF;
    IF NEW.status = 'INVALIDATED' OR OLD.status = 'INVALIDATED' THEN
        PERFORM learning.sync_review_invalidation_health(
            NEW.workspace_id,NEW.claim_id,COALESCE(NEW.invalidated_at,NEW.updated_at,CURRENT_TIMESTAMP)
        );
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_card_project_invalidation_health ON learning.review_card;
CREATE TRIGGER review_card_project_invalidation_health
    AFTER INSERT OR UPDATE OF status,claim_id,invalidation_reason,invalidated_at
    ON learning.review_card
    FOR EACH ROW EXECUTE FUNCTION learning.project_review_card_invalidation_health();

SELECT learning.sync_review_invalidation_health(
    card.workspace_id,card.claim_id,max(card.invalidated_at)
)
FROM learning.review_card AS card
WHERE card.status = 'INVALIDATED'
  AND card.claim_id IS NOT NULL
GROUP BY card.workspace_id,card.claim_id;

-- +goose Down

-- Health and Timeline history is durable and cannot be discarded implicitly.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ops.health_issue WHERE type = 'REVIEW_INVALIDATED') THEN
        RAISE EXCEPTION 'review invalidation Health history exists; archive before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS review_card_project_invalidation_health ON learning.review_card;
DROP FUNCTION IF EXISTS learning.project_review_card_invalidation_health();
DROP FUNCTION IF EXISTS learning.sync_review_invalidation_health(uuid,uuid,timestamptz);

ALTER TABLE ops.health_issue
    DROP CONSTRAINT IF EXISTS ops_health_issue_type_check;
ALTER TABLE ops.health_issue
    ADD CONSTRAINT ops_health_issue_type_check CHECK (type IN (
        'ORPHAN', 'DUPLICATE', 'CONFLICT', 'STALE', 'MISSING_SOURCE',
        'LOW_CONFIDENCE', 'BROKEN_REFERENCE', 'INDEX_ERROR', 'SUPERSEDED_USAGE'
    ));
