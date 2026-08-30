-- A CREATE_ONLY parent-chain failure happens before Change Control can persist
-- a Proposal. Preserve that fact as terminal history, but release the document
-- from the PENDING-only path guard so a corrected path can be frozen.
ALTER TABLE authoring.document_publication_reservation
    DROP CONSTRAINT document_publication_reservation_status_check,
    DROP CONSTRAINT authoring_reservation_state_shape,
    ADD COLUMN error_code text NOT NULL DEFAULT '' CHECK (
        error_code='' OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
    ),
    ADD COLUMN abandoned_at timestamptz,
    ADD CONSTRAINT authoring_reservation_status_allowed CHECK (
        status IN ('PENDING','CLOSED','ABANDONED')
    ),
    ADD CONSTRAINT authoring_reservation_state_shape CHECK (
        updated_at>=created_at
        AND ((status='PENDING' AND closed_at IS NULL AND abandoned_at IS NULL AND error_code='')
             OR (status='CLOSED' AND closed_at IS NOT NULL AND closed_at>=created_at
                 AND abandoned_at IS NULL AND error_code='')
             OR (status='ABANDONED' AND closed_at IS NULL AND abandoned_at IS NOT NULL
                 AND abandoned_at>=created_at AND error_code<>''))
    );

CREATE OR REPLACE FUNCTION authoring.validate_publication_reservation_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.status<>'PENDING'
           OR NEW.error_code<>'' OR NEW.closed_at IS NOT NULL OR NEW.abandoned_at IS NOT NULL
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
       OR OLD.status<>'PENDING' THEN
        RAISE EXCEPTION 'publication reservation immutable field or transition violation' USING ERRCODE='23514';
    END IF;

    IF NEW.status='CLOSED' AND NEW.error_code='' AND NEW.abandoned_at IS NULL
       AND EXISTS (
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
        RETURN NEW;
    END IF;

    IF NEW.status='ABANDONED' AND NEW.closed_at IS NULL
       AND NEW.error_code='WRITEBACK_TARGET_PARENT_NOT_FOUND'
       AND NOT EXISTS (
           SELECT 1 FROM authoring.document_publication_binding binding
            WHERE binding.workspace_id=OLD.workspace_id AND binding.reservation_id=OLD.id
       ) THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'publication reservation transition requires its exact terminal proof'
        USING ERRCODE='23514';
END;
$$;
