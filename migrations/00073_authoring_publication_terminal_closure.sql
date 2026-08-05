-- +goose Up

-- This migration is intentionally additive. Some databases may already have
-- executed the original 00069/00071 definitions, so changing those files alone
-- would not repair their command CHECK or validate existing publication facts.
LOCK TABLE
    authoring.working_draft_command,
    authoring.document_publication_reservation,
    authoring.document_publication_binding,
    core.article_revision,
    core.document,
    change_control.proposal,
    change_control.proposal_revision,
    change_control.proposal_commit
IN ACCESS EXCLUSIVE MODE;

-- Install the NULL-safe command shape before removing the legacy constraint.
-- NOT VALID keeps the legacy constraint active while existing rows are checked.
ALTER TABLE authoring.working_draft_command
    ADD CONSTRAINT authoring_command_shape_v2 CHECK ((
        (command_type='CREATE'
         AND working_draft_id IS NOT NULL AND expected_version=0 AND result_draft_version=1
         AND document_id IS NULL AND document_version IS NULL
         AND article_revision_id IS NULL AND revision_no IS NULL AND parent_revision_id IS NULL
         AND response_draft_created_at IS NOT NULL
         AND response_document_created_at IS NULL AND response_document_lifecycle IS NULL
         AND response_published_revision_id IS NULL
         AND response_title IS NULL AND response_target_path IS NULL AND response_content_hash IS NULL
         AND proposal_id IS NULL AND proposal_revision_id IS NULL)
        OR
        (command_type='UPDATE'
         AND working_draft_id IS NOT NULL AND expected_version>0
         AND result_draft_version=expected_version+1
         AND article_revision_id IS NULL AND revision_no IS NULL AND parent_revision_id IS NULL
         AND response_draft_created_at IS NOT NULL
         AND response_document_created_at IS NULL AND response_document_lifecycle IS NULL
         AND response_published_revision_id IS NULL
         AND response_title IS NULL AND response_target_path IS NULL AND response_content_hash IS NULL
         AND proposal_id IS NULL AND proposal_revision_id IS NULL)
        OR
        (command_type='FREEZE'
         AND working_draft_id IS NOT NULL AND expected_version>0
         AND result_draft_version=expected_version+1
         AND document_id IS NOT NULL AND document_version IS NOT NULL
         AND article_revision_id IS NOT NULL AND revision_no IS NOT NULL
         AND (revision_no>1 OR parent_revision_id IS NULL)
         AND (revision_no=1 OR parent_revision_id IS NOT NULL)
         AND response_draft_created_at IS NOT NULL AND response_document_created_at IS NOT NULL
         AND response_document_lifecycle IS NOT NULL
         AND response_title IS NOT NULL AND response_target_path IS NOT NULL
         AND response_content_hash IS NOT NULL
         AND proposal_id IS NULL AND proposal_revision_id IS NULL)
        OR
        (command_type='PUBLISH'
         AND working_draft_id IS NULL AND expected_version=0 AND result_draft_version IS NULL
         AND document_id IS NOT NULL AND document_version IS NOT NULL
         AND article_revision_id IS NOT NULL AND revision_no IS NOT NULL
         AND response_draft_created_at IS NULL AND response_document_created_at IS NOT NULL
         AND response_document_lifecycle IS NOT NULL
         AND response_title IS NOT NULL AND response_target_path IS NOT NULL
         AND response_content_hash IS NOT NULL
         AND proposal_id IS NOT NULL AND proposal_revision_id IS NOT NULL)
    ) IS TRUE) NOT VALID;

ALTER TABLE authoring.working_draft_command
    VALIDATE CONSTRAINT authoring_command_shape_v2;
ALTER TABLE authoring.working_draft_command
    DROP CONSTRAINT authoring_command_shape;
ALTER TABLE authoring.working_draft_command
    RENAME CONSTRAINT authoring_command_shape_v2 TO authoring_command_shape;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION authoring.publication_is_exact_current(
    checked_workspace_id uuid,
    checked_document_id uuid,
    checked_revision_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS (
        SELECT 1
          FROM core.document document
          JOIN core.article_revision article
            ON article.id=checked_revision_id
           AND article.workspace_id=checked_workspace_id
           AND article.document_id=checked_document_id
          JOIN authoring.document_publication_binding binding
            ON binding.workspace_id=article.workspace_id
           AND binding.document_id=article.document_id
           AND binding.article_revision_id=article.id
          JOIN authoring.document_publication_reservation reservation
            ON reservation.id=binding.reservation_id
          JOIN change_control.proposal proposal
            ON proposal.id=binding.proposal_id
          JOIN change_control.proposal_revision proposal_revision
            ON proposal_revision.id=binding.proposal_revision_id
           AND proposal_revision.proposal_id=proposal.id
          JOIN change_control.proposal_commit commit_mapping
            ON commit_mapping.workspace_id=binding.workspace_id
           AND commit_mapping.proposal_id=binding.proposal_id
           AND commit_mapping.revision_id=binding.proposal_revision_id
         WHERE document.id=checked_document_id
           AND document.workspace_id=checked_workspace_id
           AND document.lifecycle_status='PUBLISHED'
           AND document.current_published_revision_id=article.id
           AND document.canonical_path=binding.target_path
           AND article.status='PUBLISHED'
           AND article.git_commit IS NOT NULL
           AND article.git_commit=binding.git_commit
           AND article.content_hash=binding.content_hash
           AND binding.status='PUBLISHED'
           AND binding.error_code=''
           AND binding.published_at IS NOT NULL
           AND reservation.status='CLOSED'
           AND reservation.workspace_id=binding.workspace_id
           AND reservation.document_id=binding.document_id
           AND reservation.article_revision_id=binding.article_revision_id
           AND reservation.target_path=binding.target_path
           AND reservation.content_hash=binding.content_hash
           AND reservation.target_mode=binding.target_mode
           AND reservation.absence_token=binding.absence_token
           AND proposal.workspace_id=binding.workspace_id
           AND proposal.proposal_type='file_patch'
           AND proposal.idempotency_key=reservation.proposal_idempotency_key
           AND proposal.status IN ('verifying','completed')
           AND proposal_revision.target_path=binding.target_path
           AND proposal_revision.target_mode=binding.target_mode
           AND proposal_revision.base_hash=reservation.base_version
           AND proposal_revision.content=article.content
           AND encode(sha256(convert_to(proposal_revision.content,'UTF8')),'hex')=binding.content_hash
           AND commit_mapping.target_path=binding.target_path
           AND commit_mapping.target_mode=binding.target_mode
           AND commit_mapping.result_hash=binding.content_hash
           AND commit_mapping.git_commit=binding.git_commit
    );
$$;
-- +goose StatementEnd

-- Existing bindings were created under a weaker trigger. Validate their entire
-- immutable identity before installing terminal closure constraints.
-- +goose StatementBegin
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
    ) THEN
        RAISE EXCEPTION 'existing authoring publication identity is invalid'
            USING ERRCODE='55000';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM authoring.document_publication_reservation reservation
         WHERE reservation.status='CLOSED'
           AND NOT EXISTS (
               SELECT 1
                 FROM authoring.document_publication_binding binding
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
          FROM authoring.document_publication_reservation reservation
         WHERE reservation.status='ABANDONED'
           AND (reservation.error_code<>'WRITEBACK_TARGET_PARENT_NOT_FOUND'
                OR EXISTS (
                    SELECT 1 FROM authoring.document_publication_binding binding
                     WHERE binding.reservation_id=reservation.id
                ))
    ) THEN
        RAISE EXCEPTION 'existing authoring publication reservation is invalid'
            USING ERRCODE='55000';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM authoring.document_publication_binding binding
          JOIN core.article_revision article
            ON article.id=binding.article_revision_id
           AND article.workspace_id=binding.workspace_id
           AND article.document_id=binding.document_id
          JOIN core.document document
            ON document.id=binding.document_id
           AND document.workspace_id=binding.workspace_id
          LEFT JOIN change_control.proposal proposal
            ON proposal.id=binding.proposal_id
           AND proposal.workspace_id=binding.workspace_id
          LEFT JOIN change_control.proposal_commit commit_mapping
            ON commit_mapping.workspace_id=binding.workspace_id
           AND commit_mapping.proposal_id=binding.proposal_id
           AND commit_mapping.revision_id=binding.proposal_revision_id
           AND commit_mapping.target_path=binding.target_path
           AND commit_mapping.target_mode=binding.target_mode
           AND commit_mapping.result_hash=binding.content_hash
           AND commit_mapping.git_commit=binding.git_commit
         WHERE (binding.status<>'PUBLISHED' AND article.status IN ('PUBLISHED','SUPERSEDED'))
            OR (document.current_published_revision_id=binding.article_revision_id
                AND binding.status<>'PUBLISHED')
            OR (binding.status='PUBLISHED' AND (
                   binding.git_commit IS DISTINCT FROM article.git_commit
                   OR proposal.status NOT IN ('verifying','completed')
                   OR commit_mapping.proposal_id IS NULL
                   OR article.status NOT IN ('PUBLISHED','SUPERSEDED')
                   OR (article.status='PUBLISHED'
                       AND NOT authoring.publication_is_exact_current(
                           binding.workspace_id,binding.document_id,binding.article_revision_id))
                   OR (article.status='SUPERSEDED' AND NOT EXISTS (
                       SELECT 1
                         FROM authoring.document_publication_binding replacement
                         JOIN authoring.document_publication_reservation replacement_reservation
                           ON replacement_reservation.id=replacement.reservation_id
                        WHERE replacement.workspace_id=binding.workspace_id
                          AND replacement.document_id=binding.document_id
                          AND replacement.article_revision_id=document.current_published_revision_id
                          AND replacement.article_revision_id<>binding.article_revision_id
                          AND replacement.target_mode='REPLACE'
                          AND replacement.status='PUBLISHED'
                          AND replacement_reservation.status='CLOSED'
                          AND replacement_reservation.base_version=article.content_hash
                          AND authoring.publication_is_exact_current(
                              replacement.workspace_id,replacement.document_id,
                              replacement.article_revision_id)
                   ))
               ))
    ) THEN
        RAISE EXCEPTION 'existing authoring publication terminal state is not closed'
            USING ERRCODE='55000';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
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
                   AND replacement_reservation.base_version=NEW.content_hash
                   AND authoring.publication_is_exact_current(
                       replacement.workspace_id,replacement.document_id,
                       replacement.article_revision_id)
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
            NEW.workspace_id,NEW.id,NEW.current_published_revision_id);
    END IF;

    IF exact_terminal_state IS NOT TRUE THEN
        RAISE EXCEPTION 'authoring publication terminal state is not closed'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER authoring_binding_verify_terminal_state
    AFTER INSERT OR UPDATE ON authoring.document_publication_binding
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION authoring.verify_publication_terminal_state();

CREATE CONSTRAINT TRIGGER authoring_revision_verify_terminal_state
    AFTER INSERT OR UPDATE ON core.article_revision
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION authoring.verify_publication_terminal_state();

CREATE CONSTRAINT TRIGGER authoring_document_verify_terminal_state
    AFTER UPDATE ON core.document
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION authoring.verify_publication_terminal_state();

-- +goose Down

LOCK TABLE
    authoring.document_publication_binding,
    authoring.document_publication_reservation,
    core.article_revision,
    core.document
IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM authoring.document_publication_binding LIMIT 1)
       OR EXISTS (SELECT 1 FROM authoring.document_publication_reservation LIMIT 1) THEN
        RAISE EXCEPTION 'authoring publication facts exist; cannot remove terminal closure constraints'
            USING ERRCODE='55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS authoring_document_verify_terminal_state ON core.document;
DROP TRIGGER IF EXISTS authoring_revision_verify_terminal_state ON core.article_revision;
DROP TRIGGER IF EXISTS authoring_binding_verify_terminal_state ON authoring.document_publication_binding;
DROP FUNCTION IF EXISTS authoring.verify_publication_terminal_state();
DROP FUNCTION IF EXISTS authoring.publication_is_exact_current(uuid,uuid,uuid);
