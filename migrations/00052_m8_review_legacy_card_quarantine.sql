-- +goose Up

-- Legacy Review evidence predates the typed review-evidence/v1 contract. Keep
-- UUID decoding fail-closed so malformed JSON cannot abort a Workspace due
-- query or the migration that quarantines those rows.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.try_review_evidence_uuid(raw_value text)
RETURNS uuid
LANGUAGE plpgsql
IMMUTABLE
PARALLEL SAFE
AS $$
DECLARE
    normalized_value text;
BEGIN
    normalized_value := lower(btrim(raw_value));
    IF normalized_value IS NULL
       OR normalized_value !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
        RETURN NULL;
    END IF;
    RETURN normalized_value::uuid;
END
$$;
-- +goose StatementEnd

-- Any Claim transition away from CONFIRMED makes its approved learning cards
-- stale. In particular, opening a LOW/MEDIUM Conflict moves the Claim to
-- DISPUTED even though the conflict-specific triggers only cover HIGH and
-- CRITICAL severity.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.invalidate_review_cards_for_claim_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM 1 FROM core.workspace WHERE id = NEW.workspace_id FOR UPDATE;

    IF NEW.status <> 'CONFIRMED'
       AND NEW.status IS DISTINCT FROM OLD.status THEN
        UPDATE learning.review_card
        SET status = 'INVALIDATED',
            invalidation_reason = 'CLAIM_' || NEW.status,
            invalidated_at = NEW.updated_at,
            version = version + 1,
            updated_at = NEW.updated_at
        WHERE workspace_id = NEW.workspace_id
          AND claim_id = NEW.id
          AND status = 'APPROVED';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

-- Bring every legacy approved card to the same explicit lifecycle invariant.
-- The existing card status triggers delete its schedule and project the
-- corresponding SSE/Health facts in this transaction.
UPDATE learning.review_card AS card
SET status = 'INVALIDATED',
    invalidation_reason = 'CLAIM_' || claim.status,
    invalidated_at = GREATEST(card.updated_at,CURRENT_TIMESTAMP),
    version = card.version + 1,
    updated_at = GREATEST(card.updated_at,CURRENT_TIMESTAMP)
FROM core.claim AS claim
WHERE card.workspace_id = claim.workspace_id
  AND card.claim_id = claim.id
  AND card.status = 'APPROVED'
  AND claim.status <> 'CONFIRMED';

WITH unverifiable_card AS (
    SELECT card.workspace_id,card.id
    FROM learning.review_card AS card
    JOIN core.claim AS claim
      ON claim.workspace_id = card.workspace_id
     AND claim.id = card.claim_id
     AND claim.status = 'CONFIRMED'
    WHERE card.status = 'APPROVED'
      AND CASE
          WHEN jsonb_typeof(card.evidence) = 'array' THEN
              jsonb_array_length(card.evidence) NOT BETWEEN 1 AND 128
              OR jsonb_array_length(card.evidence) <> (
                  SELECT count(DISTINCT evidence.item->>'evidence_hash')
                  FROM jsonb_array_elements(card.evidence) AS evidence(item)
              )
              OR EXISTS (
                  SELECT 1
                  FROM jsonb_array_elements(card.evidence) AS evidence(item)
                  WHERE jsonb_typeof(evidence.item) IS DISTINCT FROM 'object'
                     OR jsonb_typeof(evidence.item->'schema_version') IS DISTINCT FROM 'string'
                     OR evidence.item->>'schema_version' IS DISTINCT FROM 'review-evidence/v1'
                     OR jsonb_typeof(evidence.item->'claim_id') IS DISTINCT FROM 'string'
                     OR evidence.item->>'claim_id' IS DISTINCT FROM card.claim_id::text
                     OR learning.try_review_evidence_uuid(evidence.item->>'claim_id') IS DISTINCT FROM card.claim_id
                     OR jsonb_typeof(evidence.item->'source_version_id') IS DISTINCT FROM 'string'
                     OR learning.try_review_evidence_uuid(evidence.item->>'source_version_id') IS NULL
                     OR jsonb_typeof(evidence.item->'source_span_id') IS DISTINCT FROM 'string'
                     OR learning.try_review_evidence_uuid(evidence.item->>'source_span_id') IS NULL
                     OR jsonb_typeof(evidence.item->'evidence_hash') IS DISTINCT FROM 'string'
                     OR NOT COALESCE(evidence.item->>'evidence_hash' ~ '^[0-9a-f]{64}$', false)
                     OR (
                         evidence.item ? 'quote'
                         AND evidence.item->'quote' <> 'null'::jsonb
                         AND (
                             jsonb_typeof(evidence.item->'quote') IS DISTINCT FROM 'string'
                             OR octet_length(evidence.item->>'quote') > 16384
                         )
                     )
                     OR NOT EXISTS (
                         SELECT 1
                         FROM core.claim_source AS claim_source
                         JOIN core.source_version AS source_version
                           ON source_version.workspace_id = claim_source.workspace_id
                          AND source_version.id = claim_source.source_version_id
                          AND source_version.security_status <> 'quarantined'
                         WHERE claim_source.workspace_id = card.workspace_id
                           AND claim_source.claim_id = card.claim_id
                           AND claim_source.support_type = 'SUPPORTS'
                           AND claim_source.source_version_id = learning.try_review_evidence_uuid(evidence.item->>'source_version_id')
                           AND claim_source.source_span_id = learning.try_review_evidence_uuid(evidence.item->>'source_span_id')
                           AND claim_source.evidence_hash = evidence.item->>'evidence_hash'
                           AND COALESCE((
                               SELECT attempt.security_status
                               FROM ingestion.attempt AS attempt
                               WHERE attempt.workspace_id = card.workspace_id
                                 AND attempt.source_version_id = claim_source.source_version_id
                               ORDER BY attempt.started_at DESC,attempt.id DESC
                               LIMIT 1
                           ), 'passed') <> 'quarantined'
                     )
              )
          ELSE true
      END
)
UPDATE learning.review_card AS card
SET status = 'INVALIDATED',
    invalidation_reason = 'LEGACY_EVIDENCE_UNVERIFIABLE',
    invalidated_at = GREATEST(card.updated_at,CURRENT_TIMESTAMP),
    version = card.version + 1,
    updated_at = GREATEST(card.updated_at,CURRENT_TIMESTAMP)
FROM unverifiable_card
WHERE card.workspace_id = unverifiable_card.workspace_id
  AND card.id = unverifiable_card.id
  AND card.status = 'APPROVED';

-- Legacy rows without a Claim cannot join the typed verification query.
UPDATE learning.review_card
SET status = 'INVALIDATED',
    invalidation_reason = 'LEGACY_EVIDENCE_UNVERIFIABLE',
    invalidated_at = GREATEST(updated_at,CURRENT_TIMESTAMP),
    version = version + 1,
    updated_at = GREATEST(updated_at,CURRENT_TIMESTAMP)
WHERE status = 'APPROVED'
  AND claim_id IS NULL;

-- +goose Down

-- An invalidated card cannot be restored safely without an explicit edit and
-- reapproval, so a downgrade must not pretend to reverse these facts.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM learning.review_card
        WHERE status = 'INVALIDATED'
          AND invalidation_reason IN (
              'CLAIM_DISPUTED',
              'CLAIM_SUGGESTED',
              'LEGACY_EVIDENCE_UNVERIFIABLE'
          )
    ) THEN
        RAISE EXCEPTION 'review legacy quarantine history exists; archive before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

-- Restore the 00044 lifecycle contract for an old binary after a clean
-- downgrade.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION learning.invalidate_review_cards_for_claim_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM 1 FROM core.workspace WHERE id = NEW.workspace_id FOR UPDATE;

    IF NEW.status IN ('SUPERSEDED', 'DEPRECATED', 'INVALID')
       AND NEW.status IS DISTINCT FROM OLD.status THEN
        UPDATE learning.review_card
        SET status = 'INVALIDATED',
            invalidation_reason = 'CLAIM_' || NEW.status,
            invalidated_at = NEW.updated_at,
            version = version + 1,
            updated_at = NEW.updated_at
        WHERE workspace_id = NEW.workspace_id
          AND claim_id = NEW.id
          AND status = 'APPROVED';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS learning.try_review_evidence_uuid(text);
