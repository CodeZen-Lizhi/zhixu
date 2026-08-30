CREATE SCHEMA IF NOT EXISTS authoring;

-- Working Draft is mutable recovery state. It deliberately permits empty and
-- temporarily invalid authoring fields; Freeze performs the publishable checks.
CREATE TABLE IF NOT EXISTS authoring.working_draft (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    document_id uuid,
    title text NOT NULL DEFAULT '' CHECK (
        octet_length(title) <= 512 AND title !~ E'[\\r\\n]'
    ),
    target_path text NOT NULL DEFAULT '' CHECK (
        octet_length(target_path) <= 4096 AND target_path !~ E'[\\r\\n]'
    ),
    body text NOT NULL DEFAULT '' CHECK (octet_length(body) <= 10485760),
    status text NOT NULL DEFAULT 'EDITING' CHECK (status IN ('EDITING','ARCHIVED')),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_authoring_working_draft_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT fk_authoring_working_draft_document_workspace
        FOREIGN KEY (document_id,workspace_id)
        REFERENCES core.document(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT authoring_working_draft_time_order CHECK (updated_at >= created_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_authoring_working_draft_document
    ON authoring.working_draft(workspace_id,document_id)
    WHERE document_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_authoring_working_draft_workspace_updated
    ON authoring.working_draft(workspace_id,updated_at DESC,id DESC);

-- The extra unique key lets authoring bindings prove Document and Workspace on
-- the immutable Article Revision in one composite foreign key.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='uq_core_article_revision_authoring_binding'
          AND conrelid='core.article_revision'::regclass
    ) THEN
        ALTER TABLE core.article_revision
            ADD CONSTRAINT uq_core_article_revision_authoring_binding
            UNIQUE (id,workspace_id,document_id);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='fk_core_article_revision_parent_authoring_binding'
          AND conrelid='core.article_revision'::regclass
    ) THEN
        ALTER TABLE core.article_revision
            ADD CONSTRAINT fk_core_article_revision_parent_authoring_binding
            FOREIGN KEY (parent_revision_id,workspace_id,document_id)
            REFERENCES core.article_revision(id,workspace_id,document_id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='fk_core_document_current_revision_authoring_binding'
          AND conrelid='core.document'::regclass
    ) THEN
        ALTER TABLE core.document
            ADD CONSTRAINT fk_core_document_current_revision_authoring_binding
            FOREIGN KEY (current_published_revision_id,workspace_id,id)
            REFERENCES core.article_revision(id,workspace_id,document_id) ON DELETE RESTRICT;
    END IF;
END
$$;

-- Command receipts retain response identities, not autosaved Markdown bodies.
-- This keeps response-loss replay durable without turning every keystroke into
-- a second content history.
CREATE TABLE IF NOT EXISTS authoring.working_draft_command (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (
        idempotency_key=btrim(idempotency_key) AND btrim(idempotency_key)<>''
        AND octet_length(idempotency_key)<=128 AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    command_type text NOT NULL CHECK (command_type IN ('CREATE','UPDATE','FREEZE','PUBLISH')),
    working_draft_id uuid,
    expected_version bigint NOT NULL CHECK (expected_version >= 0),
    result_draft_version bigint CHECK (result_draft_version IS NULL OR result_draft_version > 0),
    document_id uuid,
    document_version bigint CHECK (document_version IS NULL OR document_version > 0),
    article_revision_id uuid,
    revision_no integer CHECK (revision_no IS NULL OR revision_no > 0),
    parent_revision_id uuid,
    response_draft_created_at timestamptz,
    response_document_created_at timestamptz,
    response_document_lifecycle text CHECK (
        response_document_lifecycle IS NULL OR response_document_lifecycle IN ('DRAFT','PUBLISHED','ARCHIVED','DELETED')
    ),
    response_published_revision_id uuid,
    response_title text,
    response_target_path text,
    response_content_hash text CHECK (
        response_content_hash IS NULL OR response_content_hash ~ '^[0-9a-f]{64}$'
    ),
    proposal_id uuid,
    proposal_revision_id uuid,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id,idempotency_key),
    CONSTRAINT fk_authoring_command_draft_workspace
        FOREIGN KEY (working_draft_id,workspace_id)
        REFERENCES authoring.working_draft(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_command_document_workspace
        FOREIGN KEY (document_id,workspace_id)
        REFERENCES core.document(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_command_revision_binding
        FOREIGN KEY (article_revision_id,workspace_id,document_id)
        REFERENCES core.article_revision(id,workspace_id,document_id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_command_parent_binding
        FOREIGN KEY (parent_revision_id,workspace_id,document_id)
        REFERENCES core.article_revision(id,workspace_id,document_id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_command_published_revision_binding
        FOREIGN KEY (response_published_revision_id,workspace_id,document_id)
        REFERENCES core.article_revision(id,workspace_id,document_id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_command_proposal
        FOREIGN KEY (proposal_id) REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_command_proposal_revision
        FOREIGN KEY (proposal_revision_id) REFERENCES change_control.proposal_revision(id) ON DELETE RESTRICT,
    CONSTRAINT authoring_command_shape CHECK ((
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
    ) IS TRUE)
);

CREATE INDEX IF NOT EXISTS idx_authoring_command_draft
    ON authoring.working_draft_command(workspace_id,working_draft_id,created_at DESC);
CREATE INDEX IF NOT EXISTS idx_authoring_command_revision
    ON authoring.working_draft_command(workspace_id,article_revision_id,created_at DESC)
    WHERE article_revision_id IS NOT NULL;

-- A reservation closes the response-loss window between the local publish
-- command and Change Control Proposal creation. It never owns file or Git work.
CREATE TABLE IF NOT EXISTS authoring.document_publication_reservation (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    document_id uuid NOT NULL,
    article_revision_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (
        idempotency_key=btrim(idempotency_key) AND btrim(idempotency_key)<>''
        AND octet_length(idempotency_key)<=128 AND idempotency_key !~ E'[\r\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    proposal_idempotency_key text NOT NULL CHECK (
        proposal_idempotency_key=btrim(proposal_idempotency_key)
        AND btrim(proposal_idempotency_key)<>''
        AND octet_length(proposal_idempotency_key)<=128
        AND proposal_idempotency_key !~ E'[\r\n]'
    ),
    target_path text NOT NULL CHECK (
        btrim(target_path)<>'' AND left(target_path,1)<>'/'
        AND target_path !~ '(^|/)\.\.(/|$)'
        AND position(E'\\' in target_path)=0
    ),
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    target_mode text NOT NULL CHECK (target_mode IN ('CREATE_ONLY','REPLACE')),
    base_version text NOT NULL CHECK (
        octet_length(base_version)<=192 AND base_version !~ E'[\r\n]'
    ),
    absence_token text NOT NULL DEFAULT '' CHECK (
        octet_length(absence_token)<=192 AND absence_token !~ E'[\r\n]'
    ),
    status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','CLOSED')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    closed_at timestamptz,
    CONSTRAINT uq_authoring_reservation_workspace_key UNIQUE (workspace_id,idempotency_key),
    CONSTRAINT uq_authoring_reservation_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT fk_authoring_reservation_document_workspace
        FOREIGN KEY (document_id,workspace_id)
        REFERENCES core.document(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_reservation_revision_binding
        FOREIGN KEY (article_revision_id,workspace_id,document_id)
        REFERENCES core.article_revision(id,workspace_id,document_id) ON DELETE RESTRICT,
    CONSTRAINT authoring_reservation_mode_shape CHECK (
        (target_mode='CREATE_ONLY'
         AND absence_token ~ '^workspace-target-absent/v1:[0-9a-f]{64}$'
         AND absence_token !~ '^workspace-target-absent/v1:0{64}$'
         AND base_version=absence_token)
        OR (target_mode='REPLACE' AND absence_token='' AND base_version ~ '^[0-9a-f]{64}$')
    ),
    CONSTRAINT authoring_reservation_state_shape CHECK (
        updated_at>=created_at
        AND ((status='PENDING' AND closed_at IS NULL)
             OR (status='CLOSED' AND closed_at IS NOT NULL AND closed_at>=created_at))
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_authoring_reservation_pending_document
    ON authoring.document_publication_reservation(workspace_id,document_id)
    WHERE status='PENDING';
CREATE INDEX IF NOT EXISTS idx_authoring_reservation_workspace_status
    ON authoring.document_publication_reservation(workspace_id,status,updated_at,id);

-- Publication binding freezes the exact Revision/Proposal/path/hash tuple.
-- Only status/recovery metadata may advance after insertion.
CREATE TABLE IF NOT EXISTS authoring.document_publication_binding (
    id uuid PRIMARY KEY,
    reservation_id uuid NOT NULL UNIQUE
        REFERENCES authoring.document_publication_reservation(id) ON DELETE RESTRICT,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    document_id uuid NOT NULL,
    article_revision_id uuid NOT NULL,
    proposal_id uuid NOT NULL REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    proposal_revision_id uuid NOT NULL REFERENCES change_control.proposal_revision(id) ON DELETE RESTRICT,
    target_path text NOT NULL CHECK (
        btrim(target_path)<>'' AND left(target_path,1)<>'/'
        AND target_path !~ '(^|/)\.\.(/|$)'
        AND position(E'\\' in target_path)=0
    ),
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    target_mode text NOT NULL CHECK (target_mode IN ('CREATE_ONLY','REPLACE')),
    absence_token text NOT NULL DEFAULT '' CHECK (
        octet_length(absence_token)<=192 AND absence_token !~ E'[\r\n]'
    ),
    status text NOT NULL DEFAULT 'PENDING' CHECK (
        status IN ('PENDING','PUBLISHED','RECOVERY_REQUIRED','CLOSED')
    ),
    git_commit text CHECK (
        git_commit IS NULL OR git_commit ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'
    ),
    error_code text NOT NULL DEFAULT '' CHECK (
        error_code='' OR error_code ~ '^[A-Z][A-Z0-9_]{0,127}$'
    ),
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    published_at timestamptz,
    CONSTRAINT uq_authoring_publication_id_workspace UNIQUE (id,workspace_id),
    CONSTRAINT uq_authoring_publication_revision UNIQUE (workspace_id,article_revision_id),
    CONSTRAINT uq_authoring_publication_proposal_revision UNIQUE (proposal_revision_id),
    CONSTRAINT fk_authoring_publication_document_workspace
        FOREIGN KEY (document_id,workspace_id)
        REFERENCES core.document(id,workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_publication_revision_binding
        FOREIGN KEY (article_revision_id,workspace_id,document_id)
        REFERENCES core.article_revision(id,workspace_id,document_id) ON DELETE RESTRICT,
    CONSTRAINT authoring_publication_mode_shape CHECK (
        (target_mode='CREATE_ONLY'
         AND absence_token ~ '^workspace-target-absent/v1:[0-9a-f]{64}$'
         AND absence_token !~ '^workspace-target-absent/v1:0{64}$')
        OR (target_mode='REPLACE' AND absence_token='')
    ),
    CONSTRAINT authoring_publication_state_shape CHECK (
        updated_at>=created_at
        AND (
            (status='PENDING' AND git_commit IS NULL AND error_code='' AND published_at IS NULL)
            OR (status='PUBLISHED' AND git_commit IS NOT NULL AND error_code=''
                AND published_at IS NOT NULL AND published_at>=created_at)
            OR (status='RECOVERY_REQUIRED' AND git_commit IS NULL AND error_code<>'' AND published_at IS NULL)
            OR (status='CLOSED' AND git_commit IS NULL AND error_code<>'' AND published_at IS NULL)
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_authoring_publication_workspace_status
    ON authoring.document_publication_binding(workspace_id,status,updated_at DESC,id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS uq_authoring_publication_nonterminal_document
    ON authoring.document_publication_binding(workspace_id,document_id)
    WHERE status IN ('PENDING','RECOVERY_REQUIRED');

CREATE OR REPLACE FUNCTION authoring.validate_working_draft_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 THEN
            RAISE EXCEPTION 'working draft must start at version one' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP='DELETE'
       OR NEW.id<>OLD.id OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.created_at<>OLD.created_at
       OR (OLD.document_id IS NOT NULL AND NEW.document_id IS DISTINCT FROM OLD.document_id)
       OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
        RAISE EXCEPTION 'working draft identity or version violation' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER authoring_working_draft_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON authoring.working_draft
    FOR EACH ROW EXECUTE FUNCTION authoring.validate_working_draft_write();

CREATE OR REPLACE FUNCTION authoring.reject_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'authoring immutable fact cannot be mutated' USING ERRCODE='55000';
END;
$$;

CREATE TRIGGER authoring_command_append_only
    BEFORE UPDATE OR DELETE ON authoring.working_draft_command
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER authoring_command_reject_truncate
    BEFORE TRUNCATE ON authoring.working_draft_command
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

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

CREATE TRIGGER authoring_publication_reservation_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON authoring.document_publication_reservation
    FOR EACH ROW EXECUTE FUNCTION authoring.validate_publication_reservation_write();
CREATE TRIGGER authoring_publication_reservation_reject_truncate
    BEFORE TRUNCATE ON authoring.document_publication_reservation
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

-- Article Revision content and ancestry are immutable. Publication may only
-- advance governance status and attach the verified Git commit.
CREATE OR REPLACE FUNCTION authoring.validate_article_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
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
       OR NEW.created_by_type<>OLD.created_by_type OR NEW.created_at<>OLD.created_at
       OR (OLD.git_commit IS NOT NULL AND NEW.git_commit IS DISTINCT FROM OLD.git_commit)
       OR (OLD.git_commit IS NULL AND NEW.git_commit IS NOT NULL AND NEW.status<>'PUBLISHED') THEN
        RAISE EXCEPTION 'article revision immutable field violation' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER authoring_article_revision_validate_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON core.article_revision
    FOR EACH ROW EXECUTE FUNCTION authoring.validate_article_revision_mutation();
CREATE TRIGGER authoring_article_revision_reject_truncate
    BEFORE TRUNCATE ON core.article_revision
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION authoring.validate_publication_binding_write()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_workspace uuid;
    revision_proposal uuid;
    revision_target text;
    reservation authoring.document_publication_reservation%ROWTYPE;
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 OR NEW.status<>'PENDING' THEN
            RAISE EXCEPTION 'publication binding must start pending at version one' USING ERRCODE='23514';
        END IF;
        SELECT workspace_id INTO proposal_workspace
          FROM change_control.proposal WHERE id=NEW.proposal_id;
        SELECT proposal_id,target_path INTO revision_proposal,revision_target
          FROM change_control.proposal_revision WHERE id=NEW.proposal_revision_id;
        SELECT * INTO reservation
          FROM authoring.document_publication_reservation WHERE id=NEW.reservation_id
          FOR UPDATE;
        IF proposal_workspace IS DISTINCT FROM NEW.workspace_id
           OR revision_proposal IS DISTINCT FROM NEW.proposal_id
           OR revision_target IS DISTINCT FROM NEW.target_path
           OR reservation.id IS NULL OR reservation.status<>'PENDING'
           OR reservation.workspace_id<>NEW.workspace_id
           OR reservation.document_id<>NEW.document_id
           OR reservation.article_revision_id<>NEW.article_revision_id
           OR reservation.target_path<>NEW.target_path
           OR reservation.content_hash<>NEW.content_hash
           OR reservation.target_mode<>NEW.target_mode
           OR reservation.absence_token<>NEW.absence_token THEN
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
    RETURN NEW;
END;
$$;

CREATE TRIGGER authoring_publication_validate_write
    BEFORE INSERT OR UPDATE OR DELETE ON authoring.document_publication_binding
    FOR EACH ROW EXECUTE FUNCTION authoring.validate_publication_binding_write();
CREATE TRIGGER authoring_publication_reject_truncate
    BEFORE TRUNCATE ON authoring.document_publication_binding
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();

-- Canonical path is frozen while a publication can still produce a Commit.
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
           OR
           EXISTS (
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

CREATE TRIGGER authoring_document_guard_publication_path
    BEFORE UPDATE ON core.document
    FOR EACH ROW EXECUTE FUNCTION authoring.guard_document_publication_path();
