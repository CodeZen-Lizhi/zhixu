-- Freeze every owner table while the hardening contract is cut over. The
-- preflight below is deliberately fail-closed: an old 00069 terminal fact is
-- never silently accepted under the stronger 00071 trigger contract.
LOCK TABLE
    authoring.document_publication_binding,
    authoring.document_publication_reservation,
    authoring.working_draft_command,
    core.article_revision,
    core.document,
    change_control.proposal,
    change_control.proposal_revision,
    change_control.proposal_commit
IN ACCESS EXCLUSIVE MODE;

-- Validate immutable cross-owner identity before replacing any trigger. This
-- catches legacy Proposal type/key drift and half-closed reservations while
-- the cutover lock prevents a concurrent writer from racing the scan.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM authoring.document_publication_binding binding
          LEFT JOIN authoring.document_publication_reservation reservation
            ON reservation.id=binding.reservation_id
          LEFT JOIN core.article_revision article
            ON article.id=binding.article_revision_id
           AND article.workspace_id=binding.workspace_id
           AND article.document_id=binding.document_id
          LEFT JOIN change_control.proposal proposal
            ON proposal.id=binding.proposal_id
          LEFT JOIN change_control.proposal_revision proposal_revision
            ON proposal_revision.id=binding.proposal_revision_id
         WHERE reservation.id IS NULL
            OR reservation.status IS DISTINCT FROM 'CLOSED'
            OR reservation.workspace_id IS DISTINCT FROM binding.workspace_id
            OR reservation.document_id IS DISTINCT FROM binding.document_id
            OR reservation.article_revision_id IS DISTINCT FROM binding.article_revision_id
            OR reservation.target_path IS DISTINCT FROM binding.target_path
            OR reservation.content_hash IS DISTINCT FROM binding.content_hash
            OR reservation.target_mode IS DISTINCT FROM binding.target_mode
            OR reservation.absence_token IS DISTINCT FROM binding.absence_token
            OR proposal.id IS NULL
            OR proposal.workspace_id IS DISTINCT FROM binding.workspace_id
            OR proposal.proposal_type IS DISTINCT FROM 'file_patch'
            OR proposal.idempotency_key IS DISTINCT FROM reservation.proposal_idempotency_key
            OR proposal_revision.id IS NULL
            OR proposal_revision.proposal_id IS DISTINCT FROM binding.proposal_id
            OR proposal_revision.target_path IS DISTINCT FROM binding.target_path
            OR proposal_revision.target_mode IS DISTINCT FROM binding.target_mode
            OR proposal_revision.base_hash IS DISTINCT FROM reservation.base_version
            OR proposal_revision.content IS DISTINCT FROM article.content
            OR article.id IS NULL
            OR article.content_hash IS DISTINCT FROM binding.content_hash
            OR encode(sha256(convert_to(proposal_revision.content,'UTF8')),'hex')
               IS DISTINCT FROM binding.content_hash
    ) OR EXISTS (
        SELECT 1
          FROM authoring.document_publication_reservation reservation
         WHERE reservation.status='CLOSED'
           AND NOT EXISTS (
               SELECT 1 FROM authoring.document_publication_binding binding
                WHERE binding.reservation_id=reservation.id
                  AND binding.workspace_id=reservation.workspace_id
                  AND binding.document_id=reservation.document_id
                  AND binding.article_revision_id=reservation.article_revision_id
                  AND binding.target_path=reservation.target_path
                  AND binding.content_hash=reservation.content_hash
                  AND binding.target_mode=reservation.target_mode
                  AND binding.absence_token=reservation.absence_token
           )
    ) OR EXISTS (
        SELECT 1
          FROM authoring.document_publication_binding binding
          JOIN core.article_revision article
            ON article.id=binding.article_revision_id
           AND article.workspace_id=binding.workspace_id
           AND article.document_id=binding.document_id
         WHERE (binding.status<>'PUBLISHED' AND article.status IN ('PUBLISHED','SUPERSEDED'))
            OR (binding.status='PUBLISHED' AND article.status NOT IN ('PUBLISHED','SUPERSEDED'))
    ) OR EXISTS (
        SELECT 1
          FROM authoring.document_publication_binding binding
          JOIN core.document document
            ON document.id=binding.document_id
           AND document.workspace_id=binding.workspace_id
         WHERE document.current_published_revision_id=binding.article_revision_id
           AND binding.status<>'PUBLISHED'
    ) THEN
        RAISE EXCEPTION 'existing authoring publication facts are not compatible with 00071'
            USING ERRCODE='55000';
    END IF;
END
$$;

-- Publication bindings are recovery facts. Keep the database-side contract as
-- strict as the Authoring/Change Control application boundary.
CREATE OR REPLACE FUNCTION authoring.validate_publication_binding_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_workspace uuid;
    proposal_type_value text;
    proposal_idempotency_key text;
    proposal_status text;
    revision_proposal uuid;
    revision_target text;
    revision_target_mode text;
    revision_base_hash text;
    revision_content text;
    article_content text;
    article_content_hash text;
    article_status text;
    article_git_commit text;
    document_lifecycle text;
    document_published_revision uuid;
    exact_commit boolean;
    reservation authoring.document_publication_reservation%ROWTYPE;
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 OR NEW.status<>'PENDING' THEN
            RAISE EXCEPTION 'publication binding must start pending at version one' USING ERRCODE='23514';
        END IF;
        SELECT p.workspace_id,p.proposal_type,p.idempotency_key,p.status,
               r.proposal_id,r.target_path,r.target_mode,r.base_hash,r.content
          INTO proposal_workspace,proposal_type_value,proposal_idempotency_key,proposal_status,
               revision_proposal,revision_target,
               revision_target_mode,revision_base_hash,revision_content
          FROM change_control.proposal p
          JOIN change_control.proposal_revision r
            ON r.proposal_id=p.id
         WHERE p.id=NEW.proposal_id AND r.id=NEW.proposal_revision_id
         FOR SHARE OF p,r;
        SELECT content,content_hash
          INTO article_content,article_content_hash
          FROM core.article_revision
         WHERE id=NEW.article_revision_id
           AND workspace_id=NEW.workspace_id
           AND document_id=NEW.document_id
         FOR SHARE;
        SELECT * INTO reservation
          FROM authoring.document_publication_reservation WHERE id=NEW.reservation_id
          FOR UPDATE;
        IF proposal_workspace IS DISTINCT FROM NEW.workspace_id
           OR proposal_type_value IS DISTINCT FROM 'file_patch'
           OR proposal_idempotency_key IS DISTINCT FROM reservation.proposal_idempotency_key
           OR revision_proposal IS DISTINCT FROM NEW.proposal_id
           OR revision_target IS DISTINCT FROM NEW.target_path
           OR revision_target_mode IS DISTINCT FROM NEW.target_mode
           OR revision_base_hash IS DISTINCT FROM reservation.base_version
           OR revision_content IS DISTINCT FROM article_content
           OR article_content_hash IS DISTINCT FROM NEW.content_hash
           OR encode(sha256(convert_to(revision_content,'UTF8')),'hex') IS DISTINCT FROM NEW.content_hash
           OR reservation.id IS NULL OR reservation.status<>'PENDING'
           OR reservation.workspace_id<>NEW.workspace_id
           OR reservation.document_id<>NEW.document_id
           OR reservation.article_revision_id<>NEW.article_revision_id
           OR reservation.target_path<>NEW.target_path
           OR reservation.content_hash<>NEW.content_hash
           OR reservation.target_mode<>NEW.target_mode
           OR reservation.absence_token<>NEW.absence_token
           OR (NEW.target_mode='CREATE_ONLY'
               AND (reservation.absence_token='' OR reservation.base_version<>reservation.absence_token))
           OR (NEW.target_mode='REPLACE'
               AND (reservation.absence_token<>'' OR reservation.base_version !~ '^[0-9a-f]{64}$')) THEN
            RAISE EXCEPTION 'publication proposal binding is invalid' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE'
       OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.reservation_id<>OLD.reservation_id
       OR NEW.document_id<>OLD.document_id
       OR NEW.article_revision_id<>OLD.article_revision_id
       OR NEW.proposal_id<>OLD.proposal_id
       OR NEW.proposal_revision_id<>OLD.proposal_revision_id
       OR NEW.target_path<>OLD.target_path OR NEW.content_hash<>OLD.content_hash
       OR NEW.target_mode<>OLD.target_mode OR NEW.absence_token<>OLD.absence_token
       OR NEW.created_at<>OLD.created_at OR NEW.version<>OLD.version+1
       OR NEW.updated_at<OLD.updated_at
       OR (OLD.status='PENDING' AND NEW.status NOT IN ('PUBLISHED','RECOVERY_REQUIRED','CLOSED'))
       OR (OLD.status='RECOVERY_REQUIRED' AND NEW.status NOT IN ('PUBLISHED','CLOSED'))
       OR OLD.status IN ('PUBLISHED','CLOSED') THEN
       RAISE EXCEPTION 'publication binding immutable field or transition violation' USING ERRCODE='23514';
    END IF;

    SELECT * INTO reservation
      FROM authoring.document_publication_reservation
     WHERE id=NEW.reservation_id
     FOR SHARE;
    SELECT p.workspace_id,p.proposal_type,p.idempotency_key,
           r.proposal_id,r.target_path,r.target_mode,r.base_hash,r.content
      INTO proposal_workspace,proposal_type_value,proposal_idempotency_key,
           revision_proposal,revision_target,revision_target_mode,revision_base_hash,revision_content
      FROM change_control.proposal p
      JOIN change_control.proposal_revision r
        ON r.proposal_id=p.id
     WHERE p.id=NEW.proposal_id AND r.id=NEW.proposal_revision_id
     FOR SHARE OF p,r;
    SELECT content,content_hash
      INTO article_content,article_content_hash
      FROM core.article_revision
     WHERE id=NEW.article_revision_id
       AND workspace_id=NEW.workspace_id
       AND document_id=NEW.document_id
     FOR SHARE;
    IF proposal_workspace IS DISTINCT FROM NEW.workspace_id
       OR proposal_type_value IS DISTINCT FROM 'file_patch'
       OR proposal_idempotency_key IS DISTINCT FROM reservation.proposal_idempotency_key
       OR revision_proposal IS DISTINCT FROM NEW.proposal_id
       OR revision_target IS DISTINCT FROM NEW.target_path
       OR revision_target_mode IS DISTINCT FROM NEW.target_mode
       OR revision_base_hash IS DISTINCT FROM reservation.base_version
       OR revision_content IS DISTINCT FROM article_content
       OR article_content_hash IS DISTINCT FROM NEW.content_hash
       OR encode(sha256(convert_to(revision_content,'UTF8')),'hex') IS DISTINCT FROM NEW.content_hash THEN
        RAISE EXCEPTION 'publication proposal binding identity changed' USING ERRCODE='23514';
    END IF;
    SELECT status INTO proposal_status
      FROM change_control.proposal
     WHERE id=NEW.proposal_id
       AND workspace_id=NEW.workspace_id
     FOR SHARE;
    SELECT status,git_commit
      INTO article_status,article_git_commit
      FROM core.article_revision
     WHERE id=NEW.article_revision_id
       AND workspace_id=NEW.workspace_id
       AND document_id=NEW.document_id
     FOR SHARE;
    SELECT lifecycle_status,current_published_revision_id
      INTO document_lifecycle,document_published_revision
      FROM core.document
     WHERE id=NEW.document_id
       AND workspace_id=NEW.workspace_id
     FOR SHARE;

    IF reservation.id IS NULL OR reservation.status<>'CLOSED' THEN
        RAISE EXCEPTION 'publication binding transition requires a closed reservation' USING ERRCODE='23514';
    END IF;

    IF NEW.status='PUBLISHED' THEN
        SELECT EXISTS (
            SELECT 1
              FROM change_control.proposal_commit commit_mapping
             WHERE commit_mapping.workspace_id=NEW.workspace_id
               AND commit_mapping.proposal_id=NEW.proposal_id
               AND commit_mapping.revision_id=NEW.proposal_revision_id
               AND commit_mapping.target_path=NEW.target_path
               AND commit_mapping.target_mode=NEW.target_mode
               AND commit_mapping.result_hash=NEW.content_hash
               AND commit_mapping.git_commit=NEW.git_commit
        ) INTO exact_commit;
        IF proposal_status NOT IN ('verifying','completed')
           OR article_status IS DISTINCT FROM 'PUBLISHED'
           OR article_git_commit IS DISTINCT FROM NEW.git_commit
           OR document_lifecycle IS DISTINCT FROM 'PUBLISHED'
           OR document_published_revision IS DISTINCT FROM NEW.article_revision_id
           OR NOT exact_commit THEN
            RAISE EXCEPTION 'published binding requires exact published proposal, revision, document, and commit facts'
                USING ERRCODE='23514';
        END IF;
    ELSIF NEW.status='CLOSED' THEN
        IF NOT (
            (proposal_status='rejected' AND NEW.error_code='AUTHORING_PUBLICATION_PROPOSAL_REJECTED')
            OR (proposal_status='needs_revision' AND NEW.error_code='AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION')
            OR (proposal_status='cancelled' AND NEW.error_code='AUTHORING_PUBLICATION_PROPOSAL_CANCELLED')
        ) THEN
            RAISE EXCEPTION 'closed publication binding must match the terminal proposal outcome'
                USING ERRCODE='23514';
        END IF;
    ELSIF NEW.status='RECOVERY_REQUIRED' THEN
        IF NEW.error_code NOT IN (
               'AUTHORING_PUBLICATION_COMMIT_MISMATCH',
               'AUTHORING_PUBLICATION_STATE_MISMATCH'
           )
           OR proposal_status IS NULL
           OR proposal_status IN ('rejected','needs_revision','cancelled') THEN
            RAISE EXCEPTION 'publication recovery binding has no controlled nonterminal recovery reason'
                USING ERRCODE='23514';
        END IF;
    ELSE
        RAISE EXCEPTION 'publication binding target status is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

-- A Reservation is only a recoverable precursor. It may close only after the
-- exact Pending binding has been inserted in the same transaction.
CREATE OR REPLACE FUNCTION authoring.validate_publication_reservation_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.status<>'PENDING'
           OR EXISTS (
               SELECT 1 FROM authoring.document_publication_binding binding
               WHERE binding.workspace_id=NEW.workspace_id
                 AND binding.document_id=NEW.document_id
                 AND binding.status IN ('PENDING','RECOVERY_REQUIRED')
           ) THEN
            RAISE EXCEPTION 'publication reservation conflicts with a nonterminal publication'
                USING ERRCODE='23505', CONSTRAINT='uq_authoring_publication_nonterminal_document';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE'
       OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.document_id<>OLD.document_id OR NEW.article_revision_id<>OLD.article_revision_id
       OR NEW.idempotency_key<>OLD.idempotency_key OR NEW.request_hash<>OLD.request_hash
       OR NEW.proposal_idempotency_key<>OLD.proposal_idempotency_key
       OR NEW.target_path<>OLD.target_path OR NEW.content_hash<>OLD.content_hash
       OR NEW.target_mode<>OLD.target_mode OR NEW.base_version<>OLD.base_version
       OR NEW.absence_token<>OLD.absence_token
       OR NEW.created_at<>OLD.created_at OR NEW.updated_at<OLD.updated_at
       OR OLD.status<>'PENDING' OR NEW.status<>'CLOSED' THEN
        RAISE EXCEPTION 'publication reservation immutable field or transition violation' USING ERRCODE='23514';
    END IF;
    IF NOT EXISTS (
        SELECT 1
          FROM authoring.document_publication_binding binding
         WHERE binding.reservation_id=OLD.id
           AND binding.workspace_id=OLD.workspace_id
           AND binding.document_id=OLD.document_id
           AND binding.article_revision_id=OLD.article_revision_id
           AND binding.target_path=OLD.target_path
           AND binding.content_hash=OLD.content_hash
           AND binding.target_mode=OLD.target_mode
           AND binding.absence_token=OLD.absence_token
           AND binding.status='PENDING'
    ) THEN
        RAISE EXCEPTION 'publication reservation close requires an exact pending binding' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

-- A Revision may claim a Git commit only when the exact immutable
-- proposal_commit already exists. Superseding the current Revision likewise
-- requires the exact REPLACE publication that is about to take its place.
CREATE OR REPLACE FUNCTION authoring.validate_article_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    exact_publication_fact boolean;
BEGIN
    IF TG_OP='INSERT' THEN
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
               AND reservation.base_version=OLD.content_hash
               AND commit_mapping.workspace_id=binding.workspace_id
               AND commit_mapping.target_path=binding.target_path
               AND commit_mapping.target_mode='REPLACE'
               AND commit_mapping.result_hash=binding.content_hash
        ) INTO exact_publication_fact;
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

DROP TRIGGER IF EXISTS authoring_article_revision_validate_mutation ON core.article_revision;
CREATE TRIGGER authoring_article_revision_validate_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON core.article_revision
    FOR EACH ROW EXECUTE FUNCTION authoring.validate_article_revision_mutation();

-- Keep Document publication pointers inside the same exact proposal-commit
-- proof used by Article Revision and Publication Binding.
CREATE OR REPLACE FUNCTION authoring.guard_document_publication_path()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    exact_publication_fact boolean;
BEGIN
    IF NEW.canonical_path IS DISTINCT FROM OLD.canonical_path
       AND (OLD.lifecycle_status='PUBLISHED' OR NEW.lifecycle_status='PUBLISHED'
           OR OLD.current_published_revision_id IS NOT NULL
           OR NEW.current_published_revision_id IS NOT NULL
           OR EXISTS (
               SELECT 1 FROM authoring.document_publication_reservation reservation
               WHERE reservation.workspace_id=OLD.workspace_id
                 AND reservation.document_id=OLD.id AND reservation.status='PENDING'
           )
           OR EXISTS (
               SELECT 1 FROM authoring.document_publication_binding binding
               WHERE binding.workspace_id=OLD.workspace_id
                 AND binding.document_id=OLD.id
                 AND binding.status IN ('PENDING','RECOVERY_REQUIRED')
           )
       ) THEN
       RAISE EXCEPTION 'document path cannot change during publication' USING ERRCODE='23514';
    END IF;
    IF NEW.lifecycle_status='PUBLISHED'
       AND (OLD.lifecycle_status IS DISTINCT FROM NEW.lifecycle_status
            OR OLD.current_published_revision_id IS DISTINCT FROM NEW.current_published_revision_id) THEN
        SELECT EXISTS (
            SELECT 1
              FROM core.article_revision revision
              JOIN authoring.document_publication_binding binding
                ON binding.workspace_id=revision.workspace_id
               AND binding.document_id=revision.document_id
               AND binding.article_revision_id=revision.id
              JOIN change_control.proposal_commit commit_mapping
                ON commit_mapping.proposal_id=binding.proposal_id
               AND commit_mapping.revision_id=binding.proposal_revision_id
             WHERE revision.id=NEW.current_published_revision_id
               AND revision.workspace_id=NEW.workspace_id
               AND revision.document_id=NEW.id
               AND revision.status='PUBLISHED'
               AND revision.git_commit IS NOT NULL
               AND binding.target_path=NEW.canonical_path
               AND binding.content_hash=revision.content_hash
               AND binding.status IN ('PENDING','RECOVERY_REQUIRED')
               AND commit_mapping.workspace_id=binding.workspace_id
               AND commit_mapping.target_path=binding.target_path
               AND commit_mapping.target_mode=binding.target_mode
               AND commit_mapping.result_hash=binding.content_hash
               AND commit_mapping.git_commit=revision.git_commit
        ) INTO exact_publication_fact;
        IF NOT exact_publication_fact THEN
            RAISE EXCEPTION 'document publication requires an exact published revision and proposal commit' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
