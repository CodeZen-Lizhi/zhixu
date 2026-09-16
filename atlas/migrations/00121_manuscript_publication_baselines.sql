-- A generated manuscript has one immutable publication baseline. Its Article
-- identity selects the proof; callers cannot supply or override a file hash.
CREATE VIEW organizing.synthesis_publication_merge_baseline AS
SELECT r.workspace_id,r.document_id,r.article_revision_id,r.content_hash,r.projection_hash,
       receipt.id AS receipt_id,capture.id AS capture_id,
       cp->>'target_path' AS target_path,
       CASE WHEN (cp->>'exists')::boolean THEN cp->>'content_hash' ELSE cp->>'absence_token' END AS file_base,
       pub.article_revision_id AS published_revision_id,pub.content_hash AS published_content_hash,
       generated.document_version,
       COALESCE(
           r.manuscript=rp->'manuscript'
           AND r.content_hash=rp->'manuscript'->>'content_hash'
           AND ar.content=rp->'manuscript'->>'full_content'
           AND generated.expected_document_version=(ap->'prepared'->'authority'->>'document_version')::bigint
           AND generated.parent_revision_id::text=ap->'prepared'->'authority'->>'latest_article_id'
           AND cp->>'target_path'=ap->'prepared'->'authority'->>'target_path'
           AND ((ap->'prepared'->'merge_input'->'published' IS NULL
                 AND NOT (cp->>'exists')::boolean
                 AND NOT (ap->'prepared'->'authority' ? 'published_publication_id')
                 AND NOT (ap->'prepared'->'authority' ? 'published_proposal_commit_id')
                 AND NOT (ap->'prepared'->'authority' ? 'published_git_commit'))
                OR ((cp->>'exists')::boolean
                    AND pub.revision_id::text=ap->'prepared'->'merge_input'->'published'->>'id'
                    AND pub.note_id=r.note_id AND pub.document_id=r.document_id
                    AND pub.article_revision_id::text=ap->'prepared'->'merge_input'->'published'->>'article_revision_id'
                    AND pub.content_hash=ap->'prepared'->'merge_input'->'published'->>'content_hash'
                    AND pub.projection_hash=ap->'prepared'->'merge_input'->'published'->>'hash'
                    AND pub.publication_id::text=ap->'prepared'->'authority'->>'published_publication_id'
                    AND pub.proposal_commit_id::text=ap->'prepared'->'authority'->>'published_proposal_commit_id'
                    AND pub.git_commit=ap->'prepared'->'authority'->>'published_git_commit')),
           false) AS valid
FROM organizing.synthesis_revision r
JOIN organizing.synthesis_manuscript_receipt receipt ON receipt.id=r.manuscript_receipt_id AND receipt.workspace_id=r.workspace_id
JOIN organizing.synthesis_manuscript_attempt attempt ON attempt.id=receipt.attempt_id AND attempt.workspace_id=r.workspace_id
JOIN organizing.synthesis_manuscript_capture capture ON capture.id=attempt.capture_id AND capture.workspace_id=r.workspace_id
CROSS JOIN LATERAL (SELECT convert_from(receipt.payload,'UTF8')::jsonb rp,
                          convert_from(attempt.payload,'UTF8')::jsonb ap,
                          convert_from(capture.payload,'UTF8')::jsonb cp) payloads
JOIN authoring.generated_article_revision generated ON generated.article_revision_id=r.article_revision_id
 AND generated.workspace_id=r.workspace_id AND generated.document_id=r.document_id
 AND generated.origin_id=r.note_id AND generated.origin_revision_id=r.id
 AND generated.projection_hash=r.projection_hash AND generated.content_hash=r.content_hash
JOIN core.article_revision ar ON ar.id=r.article_revision_id AND ar.workspace_id=r.workspace_id
 AND ar.document_id=r.document_id AND ar.content_hash=r.content_hash AND ar.created_by_type='AGENT'
LEFT JOIN organizing.synthesis_proven_publication pub ON pub.workspace_id=r.workspace_id
 AND pub.revision_id::text=ap->'prepared'->'merge_input'->'published'->>'id'
WHERE r.renderer_version='synthesis-markdown/v2';

ALTER TABLE authoring.document_publication_reservation
 ADD COLUMN merge_receipt_id uuid,
 ADD COLUMN merge_capture_id uuid,
 ADD COLUMN merge_published_revision_id uuid,
 ADD COLUMN merge_published_content_hash text,
 ADD CONSTRAINT publication_merge_receipt_fk FOREIGN KEY(merge_receipt_id,workspace_id)
  REFERENCES organizing.synthesis_manuscript_receipt(id,workspace_id) ON DELETE RESTRICT,
 ADD CONSTRAINT publication_merge_capture_fk FOREIGN KEY(merge_capture_id,workspace_id)
  REFERENCES organizing.synthesis_manuscript_capture(id,workspace_id) ON DELETE RESTRICT,
 ADD CONSTRAINT publication_merge_published_fk FOREIGN KEY(merge_published_revision_id)
  REFERENCES core.article_revision(id) ON DELETE RESTRICT,
 ADD CONSTRAINT publication_merge_shape CHECK (
  (merge_receipt_id IS NULL AND merge_capture_id IS NULL AND merge_published_revision_id IS NULL AND merge_published_content_hash IS NULL)
  OR (merge_receipt_id IS NOT NULL AND merge_capture_id IS NOT NULL AND
      ((target_mode='CREATE_ONLY' AND merge_published_revision_id IS NULL AND merge_published_content_hash IS NULL)
       OR (target_mode='REPLACE' AND merge_published_revision_id IS NOT NULL AND merge_published_content_hash IS NOT NULL
           AND merge_published_content_hash ~ '^[0-9a-f]{64}$'))));

CREATE FUNCTION authoring.validate_publication_merge_baseline() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE proof record; document_record core.document%ROWTYPE;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.merge_receipt_id IS DISTINCT FROM OLD.merge_receipt_id
     OR NEW.merge_capture_id IS DISTINCT FROM OLD.merge_capture_id
     OR NEW.merge_published_revision_id IS DISTINCT FROM OLD.merge_published_revision_id
     OR NEW.merge_published_content_hash IS DISTINCT FROM OLD.merge_published_content_hash THEN
   RAISE EXCEPTION 'publication merge baseline is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO proof FROM organizing.synthesis_publication_merge_baseline
  WHERE workspace_id=NEW.workspace_id AND document_id=NEW.document_id AND article_revision_id=NEW.article_revision_id;
 IF NOT FOUND THEN
  IF NEW.merge_receipt_id IS NOT NULL OR EXISTS(SELECT 1 FROM organizing.synthesis_revision
       WHERE workspace_id=NEW.workspace_id AND article_revision_id=NEW.article_revision_id AND renderer_version='synthesis-markdown/v2') THEN
   RAISE EXCEPTION 'publication merge proof is missing' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO document_record FROM core.document WHERE id=NEW.document_id AND workspace_id=NEW.workspace_id FOR UPDATE;
 IF (proof.valid AND NEW.merge_receipt_id=proof.receipt_id AND NEW.merge_capture_id=proof.capture_id
     AND NEW.merge_published_revision_id IS NOT DISTINCT FROM proof.published_revision_id
     AND NEW.merge_published_content_hash IS NOT DISTINCT FROM proof.published_content_hash
     AND NEW.base_version=proof.file_base AND NEW.target_path=proof.target_path AND NEW.content_hash=proof.content_hash
     AND document_record.canonical_path=proof.target_path AND document_record.version=proof.document_version
     AND document_record.current_published_revision_id IS NOT DISTINCT FROM proof.published_revision_id
     AND NOT EXISTS(SELECT 1 FROM core.article_revision newer JOIN core.article_revision candidate ON candidate.id=NEW.article_revision_id
         WHERE newer.document_id=NEW.document_id AND newer.workspace_id=NEW.workspace_id AND newer.revision_no>candidate.revision_no)
     AND ((NEW.target_mode='CREATE_ONLY' AND proof.published_revision_id IS NULL AND document_record.lifecycle_status='DRAFT')
          OR (NEW.target_mode='REPLACE' AND proof.published_revision_id IS NOT NULL AND document_record.lifecycle_status='PUBLISHED'
              AND authoring.publication_is_exact_current(NEW.workspace_id,NEW.document_id,proof.published_revision_id)))) IS NOT TRUE THEN
  RAISE EXCEPTION 'publication merge baseline differs from proven owner facts' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER authoring_publication_merge_validate BEFORE INSERT OR UPDATE ON authoring.document_publication_reservation
 FOR EACH ROW EXECUTE FUNCTION authoring.validate_publication_merge_baseline();

-- File CAS (base_version) and published Article CAS are separate only when the
-- immutable manuscript proof supplies both. Ordinary reservations remain exact.
CREATE FUNCTION authoring.publication_replaces_revision(checked_reservation_id uuid, checked_workspace_id uuid, checked_document_id uuid, checked_revision_id uuid, checked_content_hash text)
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM authoring.document_publication_reservation r
 WHERE r.id=checked_reservation_id AND r.workspace_id=checked_workspace_id AND r.document_id=checked_document_id
 AND r.target_mode='REPLACE' AND
 ((r.merge_receipt_id IS NULL AND r.base_version=checked_content_hash)
  OR (r.merge_receipt_id IS NOT NULL AND r.merge_published_revision_id=checked_revision_id AND r.merge_published_content_hash=checked_content_hash)));
$$;

CREATE OR REPLACE FUNCTION authoring.validate_article_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    exact_publication_fact boolean;
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.status='PUBLISHED' AND NEW.git_commit IS NOT NULL THEN
            SELECT EXISTS (
                SELECT 1 FROM authoring.document_restore_publication restore
                 WHERE restore.article_revision_id=NEW.id
                   AND restore.workspace_id=NEW.workspace_id
                   AND restore.document_id=NEW.document_id
                   AND restore.target_content_hash=NEW.content_hash
                   AND restore.git_commit=NEW.git_commit
            ) INTO exact_publication_fact;
            IF NOT exact_publication_fact THEN
                RAISE EXCEPTION 'published restore revision requires an exact restore publication' USING ERRCODE='23514';
            END IF;
            RETURN NEW;
        END IF;
        IF NEW.status='PUBLISHED' OR NEW.git_commit IS NOT NULL THEN
            RAISE EXCEPTION 'article revision must be inserted before publication' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE'
       OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.document_id<>OLD.document_id
       OR NEW.source_version_id IS DISTINCT FROM OLD.source_version_id
       OR NEW.parent_revision_id IS DISTINCT FROM OLD.parent_revision_id
       OR NEW.revision_no<>OLD.revision_no OR NEW.content<>OLD.content
       OR NEW.content_hash<>OLD.content_hash
       OR NEW.optimization_mode<>OLD.optimization_mode
       OR NEW.created_by_type<>OLD.created_by_type OR NEW.created_at<>OLD.created_at THEN
        RAISE EXCEPTION 'article revision immutable field violation' USING ERRCODE='55000';
    END IF;

    IF OLD.status IN ('DRAFT','REVIEW','APPROVED') AND NEW.status='PUBLISHED'
       AND OLD.git_commit IS NULL AND NEW.git_commit IS NOT NULL THEN
        SELECT EXISTS (
            SELECT 1
              FROM authoring.document_publication_binding binding
              JOIN change_control.proposal_commit commit_mapping
                ON commit_mapping.proposal_id=binding.proposal_id
               AND commit_mapping.revision_id=binding.proposal_revision_id
             WHERE binding.workspace_id=NEW.workspace_id
               AND binding.document_id=NEW.document_id
               AND binding.article_revision_id=NEW.id
               AND binding.content_hash=NEW.content_hash
               AND binding.status IN ('PENDING','RECOVERY_REQUIRED')
               AND commit_mapping.workspace_id=binding.workspace_id
               AND commit_mapping.target_path=binding.target_path
               AND commit_mapping.target_mode=binding.target_mode
               AND commit_mapping.result_hash=binding.content_hash
               AND commit_mapping.git_commit=NEW.git_commit
        ) INTO exact_publication_fact;
        IF NOT exact_publication_fact THEN
            RAISE EXCEPTION 'article revision publish requires an exact proposal commit' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.status='PUBLISHED' AND NEW.status='SUPERSEDED'
       AND NEW.git_commit IS NOT DISTINCT FROM OLD.git_commit THEN
        SELECT EXISTS (
            SELECT 1
              FROM authoring.document_publication_binding binding
              JOIN authoring.document_publication_reservation reservation
                ON reservation.id=binding.reservation_id
              JOIN change_control.proposal_commit commit_mapping
                ON commit_mapping.proposal_id=binding.proposal_id
               AND commit_mapping.revision_id=binding.proposal_revision_id
             WHERE binding.workspace_id=OLD.workspace_id
               AND binding.document_id=OLD.document_id
               AND binding.article_revision_id<>OLD.id
               AND binding.target_mode='REPLACE'
               AND binding.status IN ('PENDING','RECOVERY_REQUIRED')
               AND reservation.status='CLOSED'
               AND authoring.publication_replaces_revision(reservation.id,OLD.workspace_id,OLD.document_id,OLD.id,OLD.content_hash)
               AND commit_mapping.workspace_id=binding.workspace_id
               AND commit_mapping.target_path=binding.target_path
               AND commit_mapping.target_mode='REPLACE'
               AND commit_mapping.result_hash=binding.content_hash
        ) INTO exact_publication_fact;
        IF NOT exact_publication_fact THEN
            SELECT EXISTS (
                SELECT 1 FROM authoring.document_restore_publication restore
                 WHERE restore.workspace_id=OLD.workspace_id
                   AND restore.document_id=OLD.document_id
                   AND restore.article_revision_id<>OLD.id
                   AND restore.current_content_hash=OLD.content_hash
            ) INTO exact_publication_fact;
        END IF;
        IF NOT exact_publication_fact THEN
            RAISE EXCEPTION 'article revision supersede requires an exact replacement commit' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.git_commit IS DISTINCT FROM OLD.git_commit
       OR NOT (
           NEW.status=OLD.status
           OR (OLD.status='DRAFT' AND NEW.status IN ('REVIEW','APPROVED','ARCHIVED'))
           OR (OLD.status='REVIEW' AND NEW.status IN ('DRAFT','APPROVED','ARCHIVED'))
           OR (OLD.status='APPROVED' AND NEW.status IN ('DRAFT','REVIEW','ARCHIVED'))
           OR (OLD.status='SUPERSEDED' AND NEW.status='ARCHIVED')
       ) THEN
        RAISE EXCEPTION 'article revision publication state transition is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION authoring.verify_publication_terminal_state()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    has_authoring_binding boolean;
    exact_terminal_state boolean;
BEGIN
    IF TG_TABLE_SCHEMA='authoring'
       AND TG_TABLE_NAME='document_publication_binding' THEN
        IF NEW.status<>'PUBLISHED'
           OR (TG_OP='UPDATE' AND OLD.status='PUBLISHED') THEN
            RETURN NEW;
        END IF;
        exact_terminal_state := authoring.publication_is_exact_current(
            NEW.workspace_id,NEW.document_id,NEW.article_revision_id);
    ELSIF TG_TABLE_SCHEMA='core' AND TG_TABLE_NAME='article_revision' THEN
        IF TG_OP<>'UPDATE' OR NEW.status IS NOT DISTINCT FROM OLD.status THEN
            RETURN NEW;
        END IF;
        IF NEW.status='PUBLISHED' THEN
            exact_terminal_state := authoring.publication_is_exact_current(
                NEW.workspace_id,NEW.document_id,NEW.id)
                OR authoring.restore_publication_is_exact_current(
                    NEW.workspace_id,NEW.document_id,NEW.id);
        ELSIF OLD.status='PUBLISHED' AND NEW.status='SUPERSEDED' THEN
            SELECT EXISTS (
                SELECT 1
                  FROM core.document document
                  JOIN authoring.document_publication_binding replacement
                    ON replacement.workspace_id=document.workspace_id
                   AND replacement.document_id=document.id
                   AND replacement.article_revision_id=document.current_published_revision_id
                  JOIN authoring.document_publication_reservation replacement_reservation
                    ON replacement_reservation.id=replacement.reservation_id
                 WHERE document.id=NEW.document_id
                   AND document.workspace_id=NEW.workspace_id
                   AND document.lifecycle_status='PUBLISHED'
                   AND document.current_published_revision_id<>NEW.id
                   AND replacement.target_mode='REPLACE'
                   AND replacement.status='PUBLISHED'
                   AND replacement_reservation.status='CLOSED'
                   AND authoring.publication_replaces_revision(replacement_reservation.id,NEW.workspace_id,NEW.document_id,NEW.id,NEW.content_hash)
                   AND authoring.publication_is_exact_current(
                       replacement.workspace_id,replacement.document_id,
                       replacement.article_revision_id)
            ) OR EXISTS (
                SELECT 1
                  FROM core.document document
                  JOIN authoring.document_restore_publication restore
                    ON restore.workspace_id=document.workspace_id
                   AND restore.document_id=document.id
                   AND restore.article_revision_id=document.current_published_revision_id
                 WHERE document.id=NEW.document_id
                   AND document.workspace_id=NEW.workspace_id
                   AND document.lifecycle_status='PUBLISHED'
                   AND document.current_published_revision_id<>NEW.id
                   AND restore.current_content_hash=NEW.content_hash
                   AND authoring.restore_publication_is_exact_current(
                       restore.workspace_id,restore.document_id,restore.article_revision_id)
            ) INTO exact_terminal_state;
        ELSE
            RETURN NEW;
        END IF;
    ELSE
        IF NEW.lifecycle_status<>'PUBLISHED'
           OR NEW.current_published_revision_id IS NULL THEN
            RETURN NEW;
        END IF;
        SELECT EXISTS (
            SELECT 1 FROM authoring.document_publication_binding binding
             WHERE binding.workspace_id=NEW.workspace_id
               AND binding.document_id=NEW.id
               AND binding.article_revision_id=NEW.current_published_revision_id
        ) INTO has_authoring_binding;
        IF NOT has_authoring_binding
           AND OLD.lifecycle_status IS NOT DISTINCT FROM NEW.lifecycle_status
           AND OLD.current_published_revision_id IS NOT DISTINCT FROM NEW.current_published_revision_id THEN
            RETURN NEW;
        END IF;
        exact_terminal_state := authoring.publication_is_exact_current(
            NEW.workspace_id,NEW.id,NEW.current_published_revision_id)
            OR authoring.restore_publication_is_exact_current(
                NEW.workspace_id,NEW.id,NEW.current_published_revision_id);
    END IF;

    IF exact_terminal_state IS NOT TRUE THEN
        RAISE EXCEPTION 'authoring publication terminal state is not closed'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

