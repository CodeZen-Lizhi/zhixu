-- Interview evidence is persisted as JSON so each UUID must be decoded
-- fail-closed before it participates in a relational provenance check.
CREATE OR REPLACE FUNCTION learning.try_interview_evidence_uuid(raw_value text)
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

-- Every frozen Question evidence item must resolve to the exact Claim Source,
-- Source Span, Index manifest and canonical Chunk selected by the server.
-- Root questions also require current eligibility. Follow-ups may continue
-- after lifecycle drift only by copying their parent's frozen evidence.
CREATE OR REPLACE FUNCTION learning.assert_interview_question_evidence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    parent_claim_id uuid;
    parent_evidence jsonb;
BEGIN
    IF jsonb_typeof(NEW.evidence) IS DISTINCT FROM 'array'
       OR jsonb_array_length(NEW.evidence) = 0
       OR jsonb_array_length(NEW.evidence) > 64 THEN
        RAISE EXCEPTION 'interview question evidence must be a non-empty bounded array'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.follow_up_no > 0 THEN
        SELECT question.claim_id, question.evidence
          INTO parent_claim_id, parent_evidence
          FROM learning.interview_question AS question
         WHERE question.id = NEW.parent_question_id
           AND question.workspace_id = NEW.workspace_id
           AND question.session_id = NEW.session_id;
        IF NOT FOUND
           OR parent_claim_id IS DISTINCT FROM NEW.claim_id
           OR parent_evidence IS DISTINCT FROM NEW.evidence THEN
            RAISE EXCEPTION 'interview follow-up must retain parent evidence'
                USING ERRCODE = '23514';
        END IF;
    END IF;

    IF EXISTS (
        SELECT 1
          FROM jsonb_array_elements(NEW.evidence) AS evidence(item)
         WHERE jsonb_typeof(evidence.item) IS DISTINCT FROM 'object'
            OR CASE
                WHEN jsonb_typeof(evidence.item) = 'object' THEN
                    (SELECT count(*) FROM jsonb_object_keys(evidence.item)) <> 8
                ELSE true
               END
            OR NOT evidence.item ?& ARRAY[
                'schema_version', 'claim_id', 'index_version_id', 'chunk_id',
                'source_version_id', 'source_span_id', 'evidence_hash', 'support_type'
            ]
            OR jsonb_typeof(evidence.item -> 'schema_version') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'claim_id') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'index_version_id') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'chunk_id') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'source_version_id') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'source_span_id') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'evidence_hash') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'support_type') IS DISTINCT FROM 'string'
            OR evidence.item ->> 'schema_version' IS DISTINCT FROM 'interview-evidence/v2'
            OR learning.try_interview_evidence_uuid(evidence.item ->> 'claim_id') IS DISTINCT FROM NEW.claim_id
            OR learning.try_interview_evidence_uuid(evidence.item ->> 'index_version_id') IS NULL
            OR learning.try_interview_evidence_uuid(evidence.item ->> 'chunk_id') IS NULL
            OR learning.try_interview_evidence_uuid(evidence.item ->> 'source_version_id') IS NULL
            OR learning.try_interview_evidence_uuid(evidence.item ->> 'source_span_id') IS NULL
            OR evidence.item ->> 'evidence_hash' !~ '^[0-9a-f]{64}$'
            OR evidence.item ->> 'support_type' IS DISTINCT FROM 'SUPPORTS'
            OR NOT EXISTS (
                SELECT 1
                  FROM core.claim_source AS claim_source
                  JOIN retrieval.index_version AS index_version
                    ON index_version.id = learning.try_interview_evidence_uuid(evidence.item ->> 'index_version_id')
                   AND index_version.workspace_id = claim_source.workspace_id
                  JOIN retrieval.index_manifest_source AS manifest_source
                    ON manifest_source.index_version_id = index_version.id
                   AND manifest_source.workspace_id = index_version.workspace_id
                   AND manifest_source.source_version_id = claim_source.source_version_id
                   AND manifest_source.selection_status = 'included'
                  JOIN ingestion.canonical_chunk AS chunk
                    ON chunk.id = learning.try_interview_evidence_uuid(evidence.item ->> 'chunk_id')
                   AND chunk.workspace_id = claim_source.workspace_id
                   AND chunk.parse_projection_id = manifest_source.parse_projection_id
                   AND chunk.source_span_id = claim_source.source_span_id
                  JOIN retrieval.index_manifest_chunk AS manifest_chunk
                    ON manifest_chunk.index_version_id = index_version.id
                   AND manifest_chunk.workspace_id = index_version.workspace_id
                   AND manifest_chunk.chunk_id = chunk.id
                   AND manifest_chunk.content_hash = chunk.content_hash
                 WHERE claim_source.workspace_id = NEW.workspace_id
                   AND claim_source.claim_id = NEW.claim_id
                   AND claim_source.source_version_id = learning.try_interview_evidence_uuid(evidence.item ->> 'source_version_id')
                   AND claim_source.source_span_id = learning.try_interview_evidence_uuid(evidence.item ->> 'source_span_id')
                   AND claim_source.evidence_hash = evidence.item ->> 'evidence_hash'
                   AND claim_source.support_type = 'SUPPORTS'
                   AND core.knowledge_validate_provenance_binding(
                       NEW.workspace_id,
                       claim_source.source_version_id,
                       claim_source.source_span_id
                   )
                   AND (
                       NEW.follow_up_no > 0
                       OR (
                           index_version.status = 'active'
                           AND EXISTS (
                               SELECT 1
                                 FROM core.claim AS claim
                                WHERE claim.id = NEW.claim_id
                                  AND claim.workspace_id = NEW.workspace_id
                                  AND claim.status = 'CONFIRMED'
                           )
                           AND EXISTS (
                               SELECT 1
                                 FROM core.source_version AS source_version
                                WHERE source_version.id = claim_source.source_version_id
                                  AND source_version.workspace_id = NEW.workspace_id
                                  AND source_version.security_status <> 'quarantined'
                           )
                           AND COALESCE((
                               SELECT attempt.security_status
                                 FROM ingestion.attempt AS attempt
                                WHERE attempt.workspace_id = NEW.workspace_id
                                  AND attempt.source_version_id = claim_source.source_version_id
                                ORDER BY attempt.started_at DESC, attempt.id DESC
                                LIMIT 1
                           ), 'passed') <> 'quarantined'
                           AND NOT EXISTS (
                               SELECT 1
                                 FROM core.conflict_member AS conflict_member
                                 JOIN core.conflict AS conflict
                                   ON conflict.id = conflict_member.conflict_id
                                  AND conflict.workspace_id = conflict_member.workspace_id
                                WHERE conflict_member.workspace_id = NEW.workspace_id
                                  AND conflict_member.claim_id = NEW.claim_id
                                  AND conflict.severity IN ('CRITICAL', 'HIGH')
                                  AND conflict.status NOT IN ('RESOLVED', 'ACCEPTED_DIVERGENCE')
                           )
                       )
                   )
            )
    ) THEN
        RAISE EXCEPTION 'interview question evidence binding is invalid'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS trg_learning_interview_question_evidence ON learning.interview_question;
CREATE TRIGGER trg_learning_interview_question_evidence
    BEFORE INSERT OR UPDATE OF workspace_id, session_id, parent_question_id, follow_up_no, claim_id, evidence
    ON learning.interview_question
    FOR EACH ROW EXECUTE FUNCTION learning.assert_interview_question_evidence();

-- Child writes lock the shared review_session parent row. Parent updates hold
-- that same row lock before their trigger runs, closing both race directions
-- without a second lock ordering scheme.
CREATE OR REPLACE FUNCTION learning.guard_interview_session_shell_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    shell_type text;
    shell_deck_id uuid;
    shell_config jsonb;
BEGIN
    SELECT session_type, deck_id, config
      INTO shell_type, shell_deck_id, shell_config
      FROM learning.review_session
     WHERE id = NEW.session_id
       AND workspace_id = NEW.workspace_id
     FOR UPDATE;

    IF NOT FOUND
       OR shell_type IS DISTINCT FROM 'INTERVIEW'
       OR shell_deck_id IS NOT NULL
       OR shell_config ->> 'schema_version' IS DISTINCT FROM 'interview/v1' THEN
        RAISE EXCEPTION 'interview session must bind a deckless interview shell'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS trg_learning_interview_session_binding ON learning.interview_session;
CREATE TRIGGER trg_learning_interview_session_binding
    BEFORE INSERT OR UPDATE OF session_id, workspace_id ON learning.interview_session
    FOR EACH ROW EXECUTE FUNCTION learning.guard_interview_session_shell_binding();

CREATE OR REPLACE FUNCTION learning.guard_review_session_learning_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM learning.interview_session AS interview
         WHERE interview.session_id = OLD.id
           AND interview.workspace_id = OLD.workspace_id
    ) AND (
        NEW.session_type IS DISTINCT FROM 'INTERVIEW'
        OR NEW.deck_id IS NOT NULL
        OR NEW.config ->> 'schema_version' IS DISTINCT FROM 'interview/v1'
    ) THEN
        RAISE EXCEPTION 'interview review shell binding is immutable'
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM learning.review_answer AS answer
         WHERE answer.session_id = OLD.id
           AND answer.workspace_id = OLD.workspace_id
    ) AND (
        NEW.session_type IS DISTINCT FROM 'REVIEW'
        OR NEW.deck_id IS NULL
    ) THEN
        RAISE EXCEPTION 'answered review session must remain deck-bound review'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS trg_learning_review_session_learning_binding ON learning.review_session;
CREATE TRIGGER trg_learning_review_session_learning_binding
    BEFORE UPDATE OF session_type, deck_id, config ON learning.review_session
    FOR EACH ROW EXECUTE FUNCTION learning.guard_review_session_learning_binding();

CREATE OR REPLACE FUNCTION learning.guard_review_answer_session_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    shell_type text;
    shell_deck_id uuid;
BEGIN
    SELECT session_type, deck_id
      INTO shell_type, shell_deck_id
      FROM learning.review_session
     WHERE id = NEW.session_id
       AND workspace_id = NEW.workspace_id
     FOR UPDATE;

    IF NOT FOUND
       OR shell_type IS DISTINCT FROM 'REVIEW'
       OR shell_deck_id IS NULL
       OR NEW.card_id IS NULL
       OR NOT EXISTS (
           SELECT 1
             FROM learning.review_card AS card
            WHERE card.id = NEW.card_id
              AND card.workspace_id = NEW.workspace_id
              AND card.deck_id = shell_deck_id
       ) THEN
        RAISE EXCEPTION 'review answer must bind a card in a deck-bound review session'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS trg_learning_review_answer_session_binding ON learning.review_answer;
CREATE TRIGGER trg_learning_review_answer_session_binding
    BEFORE INSERT ON learning.review_answer
    FOR EACH ROW EXECUTE FUNCTION learning.guard_review_answer_session_binding();

-- Installing write-time guards is insufficient when Questions already exist.
-- Audit their frozen historical provenance without requiring the referenced
-- Claim, Index or Source to remain currently eligible.
DO $$
BEGIN
    -- Keep array expansion in later phases safe for malformed legacy JSON.
    IF EXISTS (
        SELECT 1
          FROM learning.interview_question AS question
         WHERE jsonb_typeof(question.evidence) IS DISTINCT FROM 'array'
            OR CASE
                WHEN jsonb_typeof(question.evidence) = 'array' THEN
                    jsonb_array_length(question.evidence) NOT BETWEEN 1 AND 64
                ELSE true
               END
    ) THEN
        RAISE EXCEPTION 'legacy interview question evidence array shape is invalid'
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM learning.interview_question AS question
          CROSS JOIN LATERAL jsonb_array_elements(question.evidence) AS evidence(item)
         WHERE jsonb_typeof(evidence.item) IS DISTINCT FROM 'object'
            OR CASE
                WHEN jsonb_typeof(evidence.item) = 'object' THEN
                    (SELECT count(*) FROM jsonb_object_keys(evidence.item)) <> 8
                ELSE true
               END
            OR NOT evidence.item ?& ARRAY[
                'schema_version', 'claim_id', 'index_version_id', 'chunk_id',
                'source_version_id', 'source_span_id', 'evidence_hash', 'support_type'
            ]
            OR jsonb_typeof(evidence.item -> 'schema_version') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'claim_id') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'index_version_id') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'chunk_id') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'source_version_id') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'source_span_id') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'evidence_hash') IS DISTINCT FROM 'string'
            OR jsonb_typeof(evidence.item -> 'support_type') IS DISTINCT FROM 'string'
            OR evidence.item ->> 'schema_version' IS DISTINCT FROM 'interview-evidence/v2'
            OR learning.try_interview_evidence_uuid(evidence.item ->> 'claim_id') IS DISTINCT FROM question.claim_id
            OR learning.try_interview_evidence_uuid(evidence.item ->> 'index_version_id') IS NULL
            OR learning.try_interview_evidence_uuid(evidence.item ->> 'chunk_id') IS NULL
            OR learning.try_interview_evidence_uuid(evidence.item ->> 'source_version_id') IS NULL
            OR learning.try_interview_evidence_uuid(evidence.item ->> 'source_span_id') IS NULL
            OR evidence.item ->> 'evidence_hash' !~ '^[0-9a-f]{64}$'
            OR evidence.item ->> 'support_type' IS DISTINCT FROM 'SUPPORTS'
    ) THEN
        RAISE EXCEPTION 'legacy interview question evidence item shape is invalid'
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM learning.interview_question AS question
          LEFT JOIN learning.interview_question AS parent
            ON parent.id = question.parent_question_id
           AND parent.workspace_id = question.workspace_id
           AND parent.session_id = question.session_id
         WHERE question.follow_up_no > 0
           AND (
               parent.id IS NULL
               OR parent.id = question.id
               OR parent.claim_id IS DISTINCT FROM question.claim_id
               OR parent.evidence IS DISTINCT FROM question.evidence
           )
    ) THEN
        RAISE EXCEPTION 'legacy interview follow-up evidence binding is invalid'
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM learning.interview_question AS question
          CROSS JOIN LATERAL jsonb_array_elements(question.evidence) AS evidence(item)
         WHERE NOT EXISTS (
             SELECT 1
               FROM core.claim_source AS claim_source
               JOIN retrieval.index_version AS index_version
                 ON index_version.id = learning.try_interview_evidence_uuid(evidence.item ->> 'index_version_id')
                AND index_version.workspace_id = claim_source.workspace_id
               JOIN retrieval.index_manifest_source AS manifest_source
                 ON manifest_source.index_version_id = index_version.id
                AND manifest_source.workspace_id = index_version.workspace_id
                AND manifest_source.source_version_id = claim_source.source_version_id
                AND manifest_source.selection_status = 'included'
               JOIN ingestion.canonical_chunk AS chunk
                 ON chunk.id = learning.try_interview_evidence_uuid(evidence.item ->> 'chunk_id')
                AND chunk.workspace_id = claim_source.workspace_id
                AND chunk.parse_projection_id = manifest_source.parse_projection_id
                AND chunk.source_span_id = claim_source.source_span_id
               JOIN retrieval.index_manifest_chunk AS manifest_chunk
                 ON manifest_chunk.index_version_id = index_version.id
                AND manifest_chunk.workspace_id = index_version.workspace_id
                AND manifest_chunk.chunk_id = chunk.id
                AND manifest_chunk.content_hash = chunk.content_hash
              WHERE claim_source.workspace_id = question.workspace_id
                AND claim_source.claim_id = question.claim_id
                AND claim_source.source_version_id = learning.try_interview_evidence_uuid(evidence.item ->> 'source_version_id')
                AND claim_source.source_span_id = learning.try_interview_evidence_uuid(evidence.item ->> 'source_span_id')
                AND claim_source.evidence_hash = evidence.item ->> 'evidence_hash'
                AND claim_source.support_type = 'SUPPORTS'
                AND core.knowledge_validate_provenance_binding(
                    question.workspace_id,
                    claim_source.source_version_id,
                    claim_source.source_span_id
                )
         )
    ) THEN
        RAISE EXCEPTION 'legacy interview question evidence relational binding is invalid'
            USING ERRCODE = '23514';
    END IF;
END
$$;
