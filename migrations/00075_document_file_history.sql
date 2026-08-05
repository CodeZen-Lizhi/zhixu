-- +goose Up

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_type,
    ADD CONSTRAINT ck_proposal_type CHECK (
        proposal_type IN ('file_patch','restore_document','knowledge_change','publish_artifact','downstream_update')
    );

ALTER TABLE change_control.proposal_revision
    ADD COLUMN IF NOT EXISTS restore_workspace_id uuid,
    ADD COLUMN IF NOT EXISTS restore_document_id uuid,
    ADD COLUMN IF NOT EXISTS restore_target_commit text,
    ADD COLUMN IF NOT EXISTS restore_expected_head text,
    ADD COLUMN IF NOT EXISTS restore_expected_document_version bigint,
    ADD COLUMN IF NOT EXISTS restore_preview_hash text,
    ADD COLUMN IF NOT EXISTS restore_current_content_hash text,
    ADD COLUMN IF NOT EXISTS restore_target_content_hash text;

ALTER TABLE change_control.proposal_revision
    ADD CONSTRAINT ck_proposal_revision_restore_commits CHECK (
        (restore_target_commit IS NULL AND restore_expected_head IS NULL)
        OR (
            restore_target_commit ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'
            AND restore_expected_head ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'
            AND length(restore_target_commit)=length(restore_expected_head)
            AND restore_target_commit<>restore_expected_head
        )
    ),
    ADD CONSTRAINT ck_proposal_revision_restore_version CHECK (
        restore_expected_document_version IS NULL OR restore_expected_document_version>0
    ),
    ADD CONSTRAINT ck_proposal_revision_restore_hashes CHECK (
        (restore_preview_hash IS NULL AND restore_current_content_hash IS NULL AND restore_target_content_hash IS NULL)
        OR (
            restore_preview_hash ~ '^[0-9a-f]{64}$'
            AND restore_current_content_hash ~ '^[0-9a-f]{64}$'
            AND restore_target_content_hash ~ '^[0-9a-f]{64}$'
            AND restore_current_content_hash<>restore_target_content_hash
        )
    ),
    ADD CONSTRAINT fk_proposal_revision_restore_document
        FOREIGN KEY (restore_document_id,restore_workspace_id)
        REFERENCES core.document(id,workspace_id) ON DELETE RESTRICT;

-- Existing typed validation remains authoritative for all existing Proposal
-- types. Restore revisions use a dedicated trigger because the previous
-- function intentionally rejects unknown discriminators.
DROP TRIGGER IF EXISTS proposal_revision_validate_payload ON change_control.proposal_revision;
CREATE TRIGGER proposal_revision_validate_payload
    BEFORE INSERT ON change_control.proposal_revision
    FOR EACH ROW
    WHEN (
        NEW.restore_workspace_id IS NULL
        AND NEW.restore_document_id IS NULL
        AND NEW.restore_target_commit IS NULL
        AND NEW.restore_expected_head IS NULL
        AND NEW.restore_expected_document_version IS NULL
        AND NEW.restore_preview_hash IS NULL
        AND NEW.restore_current_content_hash IS NULL
        AND NEW.restore_target_content_hash IS NULL
    )
    EXECUTE FUNCTION change_control.validate_proposal_revision_payload();

-- +goose StatementBegin
CREATE FUNCTION change_control.validate_restore_document_revision_payload()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_type_value text;
    proposal_workspace_id uuid;
    proposal_risk_level text;
BEGIN
    SELECT proposal_type,workspace_id,risk_level
      INTO proposal_type_value,proposal_workspace_id,proposal_risk_level
      FROM change_control.proposal
     WHERE id=NEW.proposal_id
     FOR SHARE;

    IF proposal_type_value IS DISTINCT FROM 'restore_document'
       OR proposal_workspace_id IS DISTINCT FROM NEW.restore_workspace_id
       OR proposal_risk_level IS DISTINCT FROM 'HIGH'
       OR NEW.target_path IS NULL
       OR NEW.target_mode IS DISTINCT FROM 'REPLACE'
       OR NEW.base_hash IS NULL
       OR NEW.content IS NULL
       OR NEW.evidence_summary IS NULL
       OR NEW.target_refs IS NOT NULL
       OR NEW.base_versions IS NOT NULL
       OR NEW.change_set IS NOT NULL
       OR NEW.evidence_refs IS NOT NULL
       OR NEW.artifact_id IS NOT NULL
       OR NEW.artifact_revision_id IS NOT NULL
       OR NEW.artifact_revision_no IS NOT NULL
       OR NEW.artifact_version IS NOT NULL
       OR NEW.artifact_content_hash IS NOT NULL
       OR NEW.artifact_source_coverage IS NOT NULL
       OR NEW.downstream_workspace_id IS NOT NULL
       OR NEW.downstream_report_id IS NOT NULL
       OR NEW.downstream_analysis_version IS NOT NULL
       OR NEW.downstream_report_fingerprint IS NOT NULL
       OR NEW.downstream_source_event_id IS NOT NULL
       OR NEW.downstream_source_event_version IS NOT NULL
       OR NEW.downstream_target_type IS NOT NULL
       OR NEW.downstream_target_id IS NOT NULL
       OR NEW.downstream_base_version IS NOT NULL
       OR NEW.downstream_action IS NOT NULL
       OR NEW.downstream_owner_binding IS NOT NULL
       OR NEW.downstream_reason IS NOT NULL
       OR NEW.restore_workspace_id IS NULL
       OR NEW.restore_document_id IS NULL
       OR NEW.restore_target_commit IS NULL
       OR NEW.restore_expected_head IS NULL
       OR NEW.restore_expected_document_version IS NULL
       OR NEW.restore_preview_hash IS NULL
       OR NEW.restore_current_content_hash IS NULL
       OR NEW.restore_target_content_hash IS NULL
       OR NEW.schema_version IS DISTINCT FROM 'document-restore/v1'
       OR NEW.base_hash IS DISTINCT FROM NEW.restore_current_content_hash
       OR encode(sha256(convert_to(NEW.content,'UTF8')),'hex')
          IS DISTINCT FROM NEW.restore_target_content_hash THEN
        RAISE EXCEPTION 'restore document revision requires frozen restore and file fields only'
            USING ERRCODE='23514';
    END IF;

    PERFORM document.id
      FROM core.document AS document
     WHERE document.id=NEW.restore_document_id
       AND document.workspace_id=NEW.restore_workspace_id
       AND document.canonical_path=NEW.target_path
       AND document.version=NEW.restore_expected_document_version
       AND document.lifecycle_status='PUBLISHED'
       AND document.current_published_revision_id IS NOT NULL
     FOR SHARE OF document;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'restore document binding is stale' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER proposal_revision_validate_restore_payload
    BEFORE INSERT ON change_control.proposal_revision
    FOR EACH ROW
    WHEN (
        NEW.restore_workspace_id IS NOT NULL
        OR NEW.restore_document_id IS NOT NULL
        OR NEW.restore_target_commit IS NOT NULL
        OR NEW.restore_expected_head IS NOT NULL
        OR NEW.restore_expected_document_version IS NOT NULL
        OR NEW.restore_preview_hash IS NOT NULL
        OR NEW.restore_current_content_hash IS NOT NULL
        OR NEW.restore_target_content_hash IS NOT NULL
    )
    EXECUTE FUNCTION change_control.validate_restore_document_revision_payload();

-- Restore lifecycle facts already appear in the dedicated Document History.
-- They must not create generic Knowledge Timeline events, and a restore
-- Revision ID must never be projected as an Authoring Article Revision ID.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_proposal_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.proposal_type='restore_document' THEN
        RETURN NEW;
    END IF;
    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,'PROPOSAL_CREATED','PROPOSAL',NEW.id,
        'proposal.created:' || NEW.id::text || ':v1','proposal:' || NEW.id::text,
        1,'Proposal created',jsonb_build_object('proposal_id',NEW.id::text),NEW.created_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_approval_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    approval_workspace_id uuid;
    proposal_type_value text;
    projected_event_type text;
BEGIN
    SELECT workspace_id,proposal_type INTO approval_workspace_id,proposal_type_value
      FROM change_control.proposal
     WHERE id = NEW.proposal_id;
    IF proposal_type_value='restore_document' THEN
        RETURN NEW;
    END IF;
    projected_event_type := CASE WHEN NEW.decision = 'approved' THEN 'APPROVAL_GRANTED' ELSE 'APPROVAL_REJECTED' END;
    PERFORM ops.enqueue_timeline_projection(
        approval_workspace_id,projected_event_type,'APPROVAL',NEW.id,
        'approval:' || NEW.id::text || ':v1','approval:' || NEW.id::text,
        1,CASE WHEN NEW.decision = 'approved' THEN 'Approval granted' ELSE 'Approval rejected' END,
        jsonb_build_object('proposal_id',NEW.proposal_id::text,'approval_id',NEW.id::text),NEW.decided_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_proposal_commit_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    revision_version integer;
    proposal_type_value text;
    projected_correlation jsonb;
BEGIN
    SELECT proposal.proposal_type,revision.revision_no
      INTO proposal_type_value,revision_version
      FROM change_control.proposal AS proposal
      JOIN change_control.proposal_revision AS revision
        ON revision.id=NEW.revision_id AND revision.proposal_id=proposal.id
     WHERE proposal.id=NEW.proposal_id;
    IF proposal_type_value='restore_document' THEN
        RETURN NEW;
    END IF;
    projected_correlation := jsonb_build_object(
        'proposal_id',NEW.proposal_id::text,
        'approval_id',NEW.approval_id::text,
        'git_commit_ref',NEW.git_commit
    );
    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,'GIT_COMMITTED','GIT_COMMIT',NEW.id,
        'proposal-commit:' || NEW.id::text || ':v1','git_commit:' || NEW.git_commit,
        1,'Git commit created',projected_correlation,NEW.created_at
    );
    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,'VERSION_PUBLISHED','ARTICLE_REVISION',NEW.revision_id,
        'proposal-revision-published:' || NEW.id::text || ':v1','article_revision:' || NEW.revision_id::text,
        revision_version,'Proposal revision published',projected_correlation,NEW.created_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Restore publication is an Authoring-owned, append-only bridge from one exact
-- proposal_commit to the new Article Revision created by that commit. The
-- deferred Revision FK lets the bridge be inserted before guarded Revision
-- mutations in the same transaction.
CREATE TABLE IF NOT EXISTS authoring.document_restore_publication (
    writeback_execution_id uuid PRIMARY KEY
        REFERENCES change_control.writeback_execution(id) ON DELETE RESTRICT,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    document_id uuid NOT NULL,
    article_revision_id uuid NOT NULL UNIQUE,
    proposal_id uuid NOT NULL REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    proposal_revision_id uuid NOT NULL REFERENCES change_control.proposal_revision(id) ON DELETE RESTRICT,
    proposal_commit_id uuid NOT NULL UNIQUE REFERENCES change_control.proposal_commit(id) ON DELETE RESTRICT,
    expected_document_version bigint NOT NULL CHECK (expected_document_version>0),
    target_path text NOT NULL CHECK (
        btrim(target_path)<>'' AND left(target_path,1)<>'/'
        AND target_path !~ '(^|/)\.\.(/|$)' AND position(E'\\' in target_path)=0
    ),
    current_content_hash text NOT NULL CHECK (current_content_hash ~ '^[0-9a-f]{64}$'),
    target_content_hash text NOT NULL CHECK (
        target_content_hash ~ '^[0-9a-f]{64}$' AND target_content_hash<>current_content_hash
    ),
    git_commit text NOT NULL CHECK (git_commit ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_document_restore_publication_proposal_revision UNIQUE (proposal_id,proposal_revision_id),
    CONSTRAINT fk_document_restore_publication_document
        FOREIGN KEY (document_id,workspace_id)
        REFERENCES core.document(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_restore_publication_article_revision
        FOREIGN KEY (article_revision_id,workspace_id,document_id)
        REFERENCES core.article_revision(id,workspace_id,document_id)
        ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

-- +goose StatementBegin
CREATE FUNCTION authoring.validate_document_restore_publication()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    exact_restore_fact boolean;
BEGIN
    IF TG_OP<>'INSERT' THEN
        RAISE EXCEPTION 'document restore publication is append-only' USING ERRCODE='55000';
    END IF;
    SELECT EXISTS (
        SELECT 1
          FROM change_control.proposal proposal
          JOIN change_control.proposal_revision revision
            ON revision.proposal_id=proposal.id
           AND revision.id=NEW.proposal_revision_id
          JOIN change_control.proposal_commit commit_mapping
            ON commit_mapping.id=NEW.proposal_commit_id
           AND commit_mapping.writeback_execution_id=NEW.writeback_execution_id
           AND commit_mapping.proposal_id=proposal.id
           AND commit_mapping.revision_id=revision.id
         WHERE proposal.id=NEW.proposal_id
           AND proposal.workspace_id=NEW.workspace_id
           AND proposal.proposal_type='restore_document'
           AND proposal.status IN ('verifying','completed')
           AND revision.restore_workspace_id=NEW.workspace_id
           AND revision.restore_document_id=NEW.document_id
           AND revision.restore_expected_document_version=NEW.expected_document_version
           AND revision.target_path=NEW.target_path
           AND revision.target_mode='REPLACE'
           AND revision.base_hash=NEW.current_content_hash
           AND revision.restore_current_content_hash=NEW.current_content_hash
           AND revision.restore_target_content_hash=NEW.target_content_hash
           AND revision.schema_version='document-restore/v1'
           AND encode(sha256(convert_to(revision.content,'UTF8')),'hex')=NEW.target_content_hash
           AND commit_mapping.workspace_id=NEW.workspace_id
           AND commit_mapping.target_path=NEW.target_path
           AND commit_mapping.target_mode='REPLACE'
           AND commit_mapping.result_hash=NEW.target_content_hash
           AND commit_mapping.git_commit=NEW.git_commit
    ) INTO exact_restore_fact;
    IF NOT exact_restore_fact THEN
        RAISE EXCEPTION 'document restore publication requires an exact proposal commit'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER document_restore_publication_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON authoring.document_restore_publication
    FOR EACH ROW EXECUTE FUNCTION authoring.validate_document_restore_publication();
CREATE TRIGGER document_restore_publication_reject_truncate
    BEFORE TRUNCATE ON authoring.document_restore_publication
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.validate_document_restore_publication();

CREATE INDEX IF NOT EXISTS idx_authoring_publication_history_git
    ON authoring.document_publication_binding(workspace_id,document_id,target_path,git_commit)
    WHERE git_commit IS NOT NULL;

-- Extend the existing Authoring mutation guard only for the exact append-only
-- restore publication fact above. All previous publication rules stay intact.
-- +goose StatementBegin
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
               AND reservation.base_version=OLD.content_hash
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
-- +goose StatementEnd

-- +goose StatementBegin
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
           OR EXISTS (
               SELECT 1 FROM authoring.document_restore_publication restore
               WHERE restore.workspace_id=OLD.workspace_id AND restore.document_id=OLD.id
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
            SELECT EXISTS (
                SELECT 1
                  FROM core.article_revision revision
                  JOIN authoring.document_restore_publication restore
                    ON restore.article_revision_id=revision.id
                   AND restore.workspace_id=revision.workspace_id
                   AND restore.document_id=revision.document_id
                 WHERE revision.id=NEW.current_published_revision_id
                   AND revision.workspace_id=NEW.workspace_id
                   AND revision.document_id=NEW.id
                   AND revision.status='PUBLISHED'
                   AND revision.git_commit=restore.git_commit
                   AND revision.content_hash=restore.target_content_hash
                   AND restore.target_path=NEW.canonical_path
            ) INTO exact_publication_fact;
        END IF;
        IF NOT exact_publication_fact THEN
            RAISE EXCEPTION 'document publication requires an exact published revision and proposal commit' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION authoring.restore_publication_is_exact_current(
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
          JOIN core.article_revision revision
            ON revision.id=checked_revision_id
           AND revision.workspace_id=checked_workspace_id
           AND revision.document_id=checked_document_id
          JOIN authoring.document_restore_publication restore
            ON restore.article_revision_id=revision.id
           AND restore.workspace_id=revision.workspace_id
           AND restore.document_id=revision.document_id
          JOIN change_control.proposal proposal
            ON proposal.id=restore.proposal_id
           AND proposal.workspace_id=restore.workspace_id
          JOIN change_control.proposal_commit commit_mapping
            ON commit_mapping.id=restore.proposal_commit_id
           AND commit_mapping.writeback_execution_id=restore.writeback_execution_id
         WHERE document.id=checked_document_id
           AND document.workspace_id=checked_workspace_id
           AND document.lifecycle_status='PUBLISHED'
           AND document.current_published_revision_id=revision.id
           AND document.canonical_path=restore.target_path
           AND document.version=restore.expected_document_version+1
           AND revision.status='PUBLISHED'
           AND revision.content_hash=restore.target_content_hash
           AND revision.git_commit=restore.git_commit
           AND proposal.proposal_type='restore_document'
           AND proposal.status IN ('verifying','completed')
           AND commit_mapping.proposal_id=restore.proposal_id
           AND commit_mapping.revision_id=restore.proposal_revision_id
           AND commit_mapping.workspace_id=restore.workspace_id
           AND commit_mapping.target_path=restore.target_path
           AND commit_mapping.target_mode='REPLACE'
           AND commit_mapping.result_hash=restore.target_content_hash
           AND commit_mapping.git_commit=restore.git_commit
    );
$$;
-- +goose StatementEnd

-- The existing deferred terminal-state trigger remains authoritative; it now
-- recognizes an exact restore bridge as a second valid Authoring publication.
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
                   AND replacement_reservation.base_version=NEW.content_hash
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
-- +goose StatementEnd

INSERT INTO core.schema_meta(key,value) VALUES ('document_file_history','v1')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM change_control.proposal WHERE proposal_type='restore_document') THEN
        RAISE EXCEPTION 'cannot downgrade document file history while restore proposals exist';
    END IF;
END;
$$;
-- +goose StatementEnd

DELETE FROM core.schema_meta WHERE key='document_file_history';

DROP INDEX IF EXISTS authoring.idx_authoring_publication_history_git;

-- Restore the exact pre-00075 Authoring publication guards before dropping the
-- restore bridge they no longer reference.
-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose StatementBegin
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

DROP FUNCTION IF EXISTS authoring.restore_publication_is_exact_current(uuid,uuid,uuid);

DROP TRIGGER IF EXISTS document_restore_publication_reject_truncate ON authoring.document_restore_publication;
DROP TRIGGER IF EXISTS document_restore_publication_validate_write ON authoring.document_restore_publication;
DROP FUNCTION IF EXISTS authoring.validate_document_restore_publication();
DROP TABLE IF EXISTS authoring.document_restore_publication;

-- Restore all three generic Timeline projectors to their pre-00075 behavior.
-- The guarded Down above guarantees that no restore facts can be re-projected.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_proposal_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,'PROPOSAL_CREATED','PROPOSAL',NEW.id,
        'proposal.created:' || NEW.id::text || ':v1','proposal:' || NEW.id::text,
        1,'Proposal created',jsonb_build_object('proposal_id',NEW.id::text),NEW.created_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_approval_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    approval_workspace_id uuid;
    projected_event_type text;
BEGIN
    SELECT workspace_id INTO approval_workspace_id
      FROM change_control.proposal
     WHERE id = NEW.proposal_id;
    projected_event_type := CASE WHEN NEW.decision = 'approved' THEN 'APPROVAL_GRANTED' ELSE 'APPROVAL_REJECTED' END;
    PERFORM ops.enqueue_timeline_projection(
        approval_workspace_id,projected_event_type,'APPROVAL',NEW.id,
        'approval:' || NEW.id::text || ':v1','approval:' || NEW.id::text,
        1,CASE WHEN NEW.decision = 'approved' THEN 'Approval granted' ELSE 'Approval rejected' END,
        jsonb_build_object('proposal_id',NEW.proposal_id::text,'approval_id',NEW.id::text),NEW.decided_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.project_proposal_commit_timeline_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    revision_version integer;
    projected_correlation jsonb;
BEGIN
    SELECT revision_no INTO revision_version
      FROM change_control.proposal_revision
     WHERE id = NEW.revision_id AND proposal_id = NEW.proposal_id;
    projected_correlation := jsonb_build_object(
        'proposal_id',NEW.proposal_id::text,
        'approval_id',NEW.approval_id::text,
        'git_commit_ref',NEW.git_commit
    );
    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,'GIT_COMMITTED','GIT_COMMIT',NEW.id,
        'proposal-commit:' || NEW.id::text || ':v1','git_commit:' || NEW.git_commit,
        1,'Git commit created',projected_correlation,NEW.created_at
    );
    PERFORM ops.enqueue_timeline_projection(
        NEW.workspace_id,'VERSION_PUBLISHED','ARTICLE_REVISION',NEW.revision_id,
        'proposal-revision-published:' || NEW.id::text || ':v1','article_revision:' || NEW.revision_id::text,
        revision_version,'Proposal revision published',projected_correlation,NEW.created_at
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS proposal_revision_validate_restore_payload ON change_control.proposal_revision;
DROP FUNCTION IF EXISTS change_control.validate_restore_document_revision_payload();

DROP TRIGGER IF EXISTS proposal_revision_validate_payload ON change_control.proposal_revision;
CREATE TRIGGER proposal_revision_validate_payload
    BEFORE INSERT ON change_control.proposal_revision
    FOR EACH ROW EXECUTE FUNCTION change_control.validate_proposal_revision_payload();

ALTER TABLE change_control.proposal_revision
    DROP CONSTRAINT IF EXISTS fk_proposal_revision_restore_document,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_restore_hashes,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_restore_version,
    DROP CONSTRAINT IF EXISTS ck_proposal_revision_restore_commits,
    DROP COLUMN IF EXISTS restore_target_content_hash,
    DROP COLUMN IF EXISTS restore_current_content_hash,
    DROP COLUMN IF EXISTS restore_preview_hash,
    DROP COLUMN IF EXISTS restore_expected_document_version,
    DROP COLUMN IF EXISTS restore_expected_head,
    DROP COLUMN IF EXISTS restore_target_commit,
    DROP COLUMN IF EXISTS restore_document_id,
    DROP COLUMN IF EXISTS restore_workspace_id;

ALTER TABLE change_control.proposal
    DROP CONSTRAINT IF EXISTS ck_proposal_type,
    ADD CONSTRAINT ck_proposal_type CHECK (
        proposal_type IN ('file_patch','knowledge_change','publish_artifact','downstream_update')
    );
