-- Lifecycle invalidation needs to seek by evidence selector without expanding
-- every approved Card's JSON. This projection contains identities only; the
-- Review Card remains the evidence and lifecycle source of truth.
CREATE TABLE IF NOT EXISTS learning.review_card_evidence_selector (
    workspace_id uuid NOT NULL,
    card_id uuid NOT NULL,
    claim_id uuid NOT NULL,
    selector_kind text NOT NULL CHECK (selector_kind IN ('SOURCE_VERSION','SOURCE_SPAN')),
    selector_id uuid NOT NULL,
    CONSTRAINT pk_learning_review_card_evidence_selector
        PRIMARY KEY (workspace_id,card_id,selector_kind,selector_id),
    CONSTRAINT fk_learning_review_card_evidence_selector_card
        FOREIGN KEY (card_id,workspace_id)
        REFERENCES learning.review_card(id,workspace_id) ON DELETE CASCADE,
    CONSTRAINT fk_learning_review_card_evidence_selector_claim
        FOREIGN KEY (claim_id,workspace_id)
        REFERENCES core.claim(id,workspace_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_learning_review_card_evidence_selector_lookup
    ON learning.review_card_evidence_selector(
        workspace_id,selector_kind,selector_id,card_id
    );

CREATE INDEX IF NOT EXISTS idx_learning_review_card_evidence_selector_claim_lookup
    ON learning.review_card_evidence_selector(
        workspace_id,selector_kind,selector_id,claim_id,card_id
    );

CREATE INDEX IF NOT EXISTS idx_learning_review_card_approved_claim
    ON learning.review_card(workspace_id,claim_id,id)
    WHERE status = 'APPROVED';

CREATE INDEX IF NOT EXISTS idx_learning_review_card_invalidated_claim
    ON learning.review_card(workspace_id,claim_id,id)
    INCLUDE (version,invalidation_reason)
    WHERE status = 'INVALIDATED';

CREATE OR REPLACE FUNCTION learning.sync_review_card_evidence_selector()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF OLD.workspace_id IS DISTINCT FROM NEW.workspace_id OR OLD.id IS DISTINCT FROM NEW.id THEN
            DELETE FROM learning.review_card_evidence_selector AS selector
            WHERE selector.workspace_id = OLD.workspace_id
              AND selector.card_id = OLD.id;
        END IF;
    END IF;

    DELETE FROM learning.review_card_evidence_selector AS selector
    WHERE selector.workspace_id = NEW.workspace_id
      AND selector.card_id = NEW.id;

    IF NEW.status <> 'APPROVED' THEN
        RETURN NEW;
    END IF;

    WITH binding AS MATERIALIZED (
        SELECT DISTINCT
               learning.try_review_evidence_uuid(evidence.item->>'source_version_id') AS source_version_id,
               learning.try_review_evidence_uuid(evidence.item->>'source_span_id') AS source_span_id
        FROM (
            SELECT element.item
            FROM jsonb_array_elements(
                CASE WHEN jsonb_typeof(NEW.evidence) = 'array' THEN NEW.evidence ELSE '[]'::jsonb END
            ) AS element(item)
            LIMIT 128
        ) AS evidence
        WHERE jsonb_typeof(evidence.item) = 'object'
          AND evidence.item->>'schema_version' = 'review-evidence/v1'
    ), selector AS (
        SELECT 'SOURCE_VERSION'::text AS selector_kind,
               binding.source_version_id AS selector_id
        FROM binding
        WHERE binding.source_version_id IS NOT NULL
          AND binding.source_span_id IS NOT NULL

        UNION

        SELECT 'SOURCE_SPAN'::text AS selector_kind,
               binding.source_span_id AS selector_id
        FROM binding
        WHERE binding.source_version_id IS NOT NULL
          AND binding.source_span_id IS NOT NULL
    )
    INSERT INTO learning.review_card_evidence_selector(
        workspace_id,card_id,claim_id,selector_kind,selector_id
    )
    SELECT NEW.workspace_id,NEW.id,NEW.claim_id,selector.selector_kind,
           selector.selector_id
    FROM selector
    ON CONFLICT (workspace_id,card_id,selector_kind,selector_id) DO NOTHING;

    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS review_card_sync_evidence_selector ON learning.review_card;
CREATE TRIGGER review_card_sync_evidence_selector
    AFTER INSERT OR UPDATE OF evidence,status,workspace_id,id,claim_id ON learning.review_card
    FOR EACH ROW EXECUTE FUNCTION learning.sync_review_card_evidence_selector();

WITH binding AS MATERIALIZED (
    SELECT card.workspace_id,
           card.id AS card_id,
           card.claim_id,
           learning.try_review_evidence_uuid(evidence.item->>'source_version_id') AS source_version_id,
           learning.try_review_evidence_uuid(evidence.item->>'source_span_id') AS source_span_id
    FROM learning.review_card AS card
    CROSS JOIN LATERAL (
        SELECT element.item
        FROM jsonb_array_elements(
            CASE WHEN jsonb_typeof(card.evidence) = 'array' THEN card.evidence ELSE '[]'::jsonb END
        ) AS element(item)
        LIMIT 128
    ) AS evidence
    WHERE card.status = 'APPROVED'
      AND jsonb_typeof(evidence.item) = 'object'
      AND evidence.item->>'schema_version' = 'review-evidence/v1'
), selector AS (
    SELECT binding.workspace_id,binding.card_id,binding.claim_id,
           'SOURCE_VERSION'::text AS selector_kind,
           binding.source_version_id AS selector_id
    FROM binding
    WHERE binding.source_version_id IS NOT NULL
      AND binding.source_span_id IS NOT NULL

    UNION

    SELECT binding.workspace_id,binding.card_id,binding.claim_id,
           'SOURCE_SPAN'::text AS selector_kind,
           binding.source_span_id AS selector_id
    FROM binding
    WHERE binding.source_version_id IS NOT NULL
      AND binding.source_span_id IS NOT NULL
)
INSERT INTO learning.review_card_evidence_selector(
    workspace_id,card_id,claim_id,selector_kind,selector_id
)
SELECT selector.workspace_id,selector.card_id,selector.claim_id,selector.selector_kind,
       selector.selector_id
FROM selector
ON CONFLICT (workspace_id,card_id,selector_kind,selector_id) DO NOTHING;

-- A bounded invalidation UPDATE may touch many cards for the same Claim. The
-- old row trigger recalculated the complete Claim projection once per card,
-- turning one batch into repeated aggregate scans. The batch helper consumes
-- every affected Workspace/Claim in one set-based call while preserving the
-- same transaction as Card, Schedule, SSE and Health facts.
CREATE OR REPLACE FUNCTION learning.sync_review_invalidation_health_batch(
    target_workspace_ids uuid[],
    target_claim_ids uuid[],
    projection_occurred_ats timestamptz[]
)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF COALESCE(cardinality(target_workspace_ids),0) <> COALESCE(cardinality(target_claim_ids),0)
       OR COALESCE(cardinality(target_workspace_ids),0) <> COALESCE(cardinality(projection_occurred_ats),0) THEN
        RAISE EXCEPTION 'review invalidation health batch arrays must have equal cardinality'
            USING ERRCODE = '22023';
    END IF;
    IF COALESCE(cardinality(target_workspace_ids),0) = 0 THEN
        RETURN;
    END IF;

    WITH affected AS MATERIALIZED (
        SELECT input.workspace_id,
               input.claim_id,
               max(COALESCE(input.occurred_at,CURRENT_TIMESTAMP)) AS effective_occurred_at
        FROM unnest(target_workspace_ids,target_claim_ids,projection_occurred_ats)
            AS input(workspace_id,claim_id,occurred_at)
        WHERE input.workspace_id IS NOT NULL
          AND input.claim_id IS NOT NULL
        GROUP BY input.workspace_id,input.claim_id
    ), projected AS MATERIALIZED (
        SELECT affected.workspace_id,
               affected.claim_id,
               affected.effective_occurred_at,
               GREATEST(affected.effective_occurred_at,CURRENT_TIMESTAMP) AS persisted_at,
               claim.version AS claim_version,
               encode(sha256(convert_to(string_agg(
                   card.id::text || ':' || card.version::text || ':' || card.invalidation_reason,
                   '|' ORDER BY card.id
               ), 'UTF8')), 'hex') AS evidence_hash
        FROM affected
        JOIN core.claim AS claim
          ON claim.workspace_id=affected.workspace_id
         AND claim.id=affected.claim_id
        JOIN learning.review_card AS card
          ON card.workspace_id=affected.workspace_id
         AND card.claim_id=affected.claim_id
         AND card.status='INVALIDATED'
        GROUP BY affected.workspace_id,affected.claim_id,
                 affected.effective_occurred_at,claim.version
    ), fingerprinted AS (
        SELECT projected.workspace_id,
               projected.claim_id,
               projected.effective_occurred_at,
               projected.persisted_at,
               encode(sha256(convert_to(
                   '{"schema":"health-issue-identity/v1","workspace_id":"' || projected.workspace_id::text ||
                   '","type":"REVIEW_INVALIDATED","target":{"Type":"CLAIM","ID":"' || projected.claim_id::text ||
                   '"},"detector_id":"health.detector.review_invalidated"}',
                   'UTF8'
               )), 'hex') AS identity_hash,
               encode(sha256(convert_to(
                   '{"schema":"health-issue-fingerprint/v1","workspace_id":"' || projected.workspace_id::text ||
                   '","type":"REVIEW_INVALIDATED","target":{"Type":"CLAIM","ID":"' || projected.claim_id::text ||
                   '"},"detector_id":"health.detector.review_invalidated","detector_version":"detector/v1",' ||
                   '"evidence_hashes":["' || projected.evidence_hash || '"],"object_versions":[{"Ref":{"Type":"CLAIM","ID":"' ||
                   projected.claim_id::text || '"},"Version":' || projected.claim_version::text || '}]}',
                   'UTF8'
               )), 'hex') AS fingerprint
        FROM projected
    )
    INSERT INTO ops.health_issue AS issue(
        id,workspace_id,type,target_type,target_id,detector_id,identity_hash,
        fingerprint_schema_version,fingerprint,detector_version,severity,
        evidence_summary,status,version,first_detected_at,last_detected_at,
        last_verified_at,created_at,updated_at
    )
    SELECT gen_random_uuid(),fingerprinted.workspace_id,'REVIEW_INVALIDATED','CLAIM',fingerprinted.claim_id,
           'health.detector.review_invalidated',fingerprinted.identity_hash,'health-issue-fingerprint/v1',
           fingerprinted.fingerprint,'detector/v1','HIGH','Review cards reference invalidated knowledge',
           'OPEN',1,fingerprinted.effective_occurred_at,fingerprinted.effective_occurred_at,
           fingerprinted.effective_occurred_at,fingerprinted.persisted_at,fingerprinted.persisted_at
    FROM fingerprinted
    WHERE true
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

    WITH affected AS MATERIALIZED (
        SELECT input.workspace_id,
               input.claim_id,
               max(COALESCE(input.occurred_at,CURRENT_TIMESTAMP)) AS effective_occurred_at
        FROM unnest(target_workspace_ids,target_claim_ids,projection_occurred_ats)
            AS input(workspace_id,claim_id,occurred_at)
        WHERE input.workspace_id IS NOT NULL
          AND input.claim_id IS NOT NULL
        GROUP BY input.workspace_id,input.claim_id
    ), resolvable AS (
        SELECT affected.workspace_id,
               affected.claim_id,
               affected.effective_occurred_at,
               GREATEST(affected.effective_occurred_at,CURRENT_TIMESTAMP) AS persisted_at,
               encode(sha256(convert_to(
                   '{"schema":"health-issue-identity/v1","workspace_id":"' || affected.workspace_id::text ||
                   '","type":"REVIEW_INVALIDATED","target":{"Type":"CLAIM","ID":"' || affected.claim_id::text ||
                   '"},"detector_id":"health.detector.review_invalidated"}',
                   'UTF8'
               )), 'hex') AS identity_hash
        FROM affected
        WHERE NOT EXISTS (
            SELECT 1
            FROM learning.review_card AS card
            WHERE card.workspace_id=affected.workspace_id
              AND card.claim_id=affected.claim_id
              AND card.status='INVALIDATED'
        )
    )
    UPDATE ops.health_issue AS issue
       SET status = 'RESOLVED',
           status_reason = NULL,
           deferred_until = NULL,
           repair_proposal_id = NULL,
           repair_option_code = NULL,
           repair_binding_fingerprint = NULL,
           repair_binding_object_versions = NULL,
           repair_proposal_created_at = NULL,
           version = issue.version + 1,
           last_verified_at = GREATEST(issue.last_verified_at,resolvable.effective_occurred_at),
           resolved_at = GREATEST(issue.created_at,resolvable.effective_occurred_at),
           updated_at = GREATEST(issue.updated_at,resolvable.persisted_at)
      FROM resolvable
     WHERE issue.workspace_id=resolvable.workspace_id
       AND issue.identity_hash=resolvable.identity_hash
       AND issue.status <> 'RESOLVED';
END
$$;

CREATE OR REPLACE FUNCTION learning.project_review_card_invalidation_health_insert_statement()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    affected_workspace_ids uuid[];
    affected_claim_ids uuid[];
    affected_occurred_ats timestamptz[];
BEGIN
    SELECT array_agg(affected.workspace_id ORDER BY affected.workspace_id,affected.claim_id),
           array_agg(affected.claim_id ORDER BY affected.workspace_id,affected.claim_id),
           array_agg(affected.occurred_at ORDER BY affected.workspace_id,affected.claim_id)
      INTO affected_workspace_ids,affected_claim_ids,affected_occurred_ats
    FROM (
        SELECT card.workspace_id,
               card.claim_id,
               max(COALESCE(card.invalidated_at,card.updated_at,CURRENT_TIMESTAMP)) AS occurred_at
        FROM new_review_cards AS card
        WHERE card.status = 'INVALIDATED'
          AND card.claim_id IS NOT NULL
        GROUP BY card.workspace_id,card.claim_id
    ) AS affected;
    PERFORM learning.sync_review_invalidation_health_batch(
        affected_workspace_ids,affected_claim_ids,affected_occurred_ats
    );
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION learning.project_review_card_invalidation_health_update_statement()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    affected_workspace_ids uuid[];
    affected_claim_ids uuid[];
    affected_occurred_ats timestamptz[];
BEGIN
    SELECT array_agg(affected.workspace_id ORDER BY affected.workspace_id,affected.claim_id),
           array_agg(affected.claim_id ORDER BY affected.workspace_id,affected.claim_id),
           array_agg(affected.occurred_at ORDER BY affected.workspace_id,affected.claim_id)
      INTO affected_workspace_ids,affected_claim_ids,affected_occurred_ats
    FROM (
        SELECT changed.workspace_id,
               changed.claim_id,
               max(changed.occurred_at) AS occurred_at
        FROM (
            SELECT old_card.workspace_id,
                   old_card.claim_id,
                   COALESCE(new_card.updated_at,CURRENT_TIMESTAMP) AS occurred_at
            FROM old_review_cards AS old_card
            JOIN new_review_cards AS new_card ON new_card.id = old_card.id
            WHERE old_card.status = 'INVALIDATED'
              AND old_card.claim_id IS NOT NULL

            UNION ALL

            SELECT new_card.workspace_id,
                   new_card.claim_id,
                   COALESCE(new_card.invalidated_at,new_card.updated_at,CURRENT_TIMESTAMP) AS occurred_at
            FROM old_review_cards AS old_card
            JOIN new_review_cards AS new_card ON new_card.id = old_card.id
            WHERE (new_card.status = 'INVALIDATED' OR old_card.status = 'INVALIDATED')
              AND new_card.claim_id IS NOT NULL
        ) AS changed
        GROUP BY changed.workspace_id,changed.claim_id
    ) AS affected;
    PERFORM learning.sync_review_invalidation_health_batch(
        affected_workspace_ids,affected_claim_ids,affected_occurred_ats
    );
    RETURN NULL;
END
$$;

DROP TRIGGER IF EXISTS review_card_project_invalidation_health ON learning.review_card;

CREATE TRIGGER review_card_project_invalidation_health_insert
    AFTER INSERT ON learning.review_card
    REFERENCING NEW TABLE AS new_review_cards
    FOR EACH STATEMENT
    EXECUTE FUNCTION learning.project_review_card_invalidation_health_insert_statement();

CREATE TRIGGER review_card_project_invalidation_health_update
    AFTER UPDATE ON learning.review_card
    REFERENCING OLD TABLE AS old_review_cards NEW TABLE AS new_review_cards
    FOR EACH STATEMENT
    EXECUTE FUNCTION learning.project_review_card_invalidation_health_update_statement();
