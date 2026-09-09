-- Generated content has its own internal provenance. Working Draft commands
-- remain USER-owned and all publication terminal guards remain unchanged.
CREATE TABLE authoring.generated_document (
    document_id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    origin_kind text NOT NULL CHECK (origin_kind='SYNTHESIS_NOTE'),
    origin_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_authoring_generated_document_origin UNIQUE (workspace_id,origin_kind,origin_id),
    CONSTRAINT uq_authoring_generated_document_binding UNIQUE (document_id,workspace_id,origin_kind,origin_id),
    CONSTRAINT fk_authoring_generated_document_workspace FOREIGN KEY (document_id,workspace_id)
        REFERENCES core.document(id,workspace_id) ON DELETE RESTRICT
);

CREATE TABLE authoring.generated_article_revision (
    article_revision_id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    document_id uuid NOT NULL,
    origin_kind text NOT NULL CHECK (origin_kind='SYNTHESIS_NOTE'),
    origin_id uuid NOT NULL,
    origin_revision_id uuid NOT NULL,
    projection_hash text NOT NULL CHECK (projection_hash ~ '^[0-9a-f]{64}$'),
    idempotency_key text NOT NULL CHECK (
        idempotency_key=btrim(idempotency_key) AND idempotency_key<>''
        AND octet_length(idempotency_key)<=128 AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    expected_document_version bigint NOT NULL CHECK (expected_document_version>=0),
    document_version bigint NOT NULL CHECK (document_version>0),
    revision_no integer NOT NULL CHECK (revision_no>0),
    parent_revision_id uuid,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    title text NOT NULL CHECK (title=btrim(title) AND title<>'' AND octet_length(title)<=512),
    target_path text NOT NULL,
    document_created_at timestamptz NOT NULL,
    document_lifecycle text NOT NULL CHECK (document_lifecycle IN ('DRAFT','PUBLISHED')),
    published_revision_id uuid,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_authoring_generated_revision_key UNIQUE (workspace_id,idempotency_key),
    CONSTRAINT uq_authoring_generated_revision_origin UNIQUE (workspace_id,origin_kind,origin_id,origin_revision_id),
    CONSTRAINT uq_authoring_generated_revision_projection
        UNIQUE (article_revision_id,workspace_id,document_id,origin_id,origin_revision_id,projection_hash),
    CONSTRAINT uq_authoring_generated_revision_parent_binding
        UNIQUE (article_revision_id,workspace_id,document_id,origin_kind,origin_id),
    CONSTRAINT fk_authoring_generated_revision_document FOREIGN KEY (document_id,workspace_id,origin_kind,origin_id)
        REFERENCES authoring.generated_document(document_id,workspace_id,origin_kind,origin_id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_generated_revision_article FOREIGN KEY (article_revision_id,workspace_id,document_id)
        REFERENCES core.article_revision(id,workspace_id,document_id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_generated_revision_parent FOREIGN KEY (parent_revision_id,workspace_id,document_id,origin_kind,origin_id)
        REFERENCES authoring.generated_article_revision(article_revision_id,workspace_id,document_id,origin_kind,origin_id) ON DELETE RESTRICT,
    CONSTRAINT fk_authoring_generated_revision_published FOREIGN KEY (published_revision_id,workspace_id,document_id)
        REFERENCES core.article_revision(id,workspace_id,document_id) ON DELETE RESTRICT,
    CONSTRAINT authoring_generated_revision_version_shape CHECK (
        document_version=expected_document_version+1
        AND ((revision_no=1 AND parent_revision_id IS NULL AND expected_document_version=0)
             OR (revision_no>1 AND parent_revision_id IS NOT NULL AND expected_document_version>0))
        AND parent_revision_id IS DISTINCT FROM article_revision_id
    ),
    CONSTRAINT authoring_generated_revision_document_shape CHECK (
        ((document_lifecycle='DRAFT' AND published_revision_id IS NULL)
         OR (document_lifecycle='PUBLISHED' AND published_revision_id IS NOT NULL))
        AND created_at>=document_created_at
    )
);

CREATE TABLE authoring.generated_publication_retirement (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (
        idempotency_key=btrim(idempotency_key) AND idempotency_key<>''
        AND octet_length(idempotency_key)<=128 AND idempotency_key !~ E'[\\r\\n]'
    ),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    publication_id uuid NOT NULL UNIQUE REFERENCES authoring.document_publication_binding(id) ON DELETE RESTRICT,
    document_id uuid NOT NULL,
    article_revision_id uuid NOT NULL,
    origin_kind text NOT NULL CHECK (origin_kind='SYNTHESIS_NOTE'),
    origin_id uuid NOT NULL,
    origin_revision_id uuid NOT NULL,
    projection_hash text NOT NULL CHECK (projection_hash ~ '^[0-9a-f]{64}$'),
    expected_document_version bigint NOT NULL CHECK (expected_document_version>0),
    expected_proposal_version bigint NOT NULL CHECK (expected_proposal_version>0),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id,idempotency_key),
    CONSTRAINT fk_authoring_generated_retirement_projection
        FOREIGN KEY (article_revision_id,workspace_id,document_id,origin_id,origin_revision_id,projection_hash)
        REFERENCES authoring.generated_article_revision(article_revision_id,workspace_id,document_id,origin_id,origin_revision_id,projection_hash)
        ON DELETE RESTRICT
);

CREATE FUNCTION authoring.validate_generated_document_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM core.document
     WHERE id=NEW.document_id AND workspace_id=NEW.workspace_id
       AND lifecycle_status='DRAFT' AND current_published_revision_id IS NULL
       AND version=1 AND created_at=NEW.created_at AND updated_at=NEW.created_at
     FOR UPDATE;
    IF NOT FOUND OR EXISTS (
        SELECT 1 FROM core.article_revision WHERE document_id=NEW.document_id
    ) OR EXISTS (
        SELECT 1 FROM authoring.working_draft WHERE document_id=NEW.document_id
    ) THEN
        RAISE EXCEPTION 'generated origin requires a new unbound document' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER authoring_generated_document_validate_insert
    BEFORE INSERT ON authoring.generated_document
    FOR EACH ROW EXECUTE FUNCTION authoring.validate_generated_document_insert();

CREATE FUNCTION authoring.validate_generated_revision_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    document_record core.document%ROWTYPE;
    revision_record core.article_revision%ROWTYPE;
    parent_no integer;
BEGIN
    SELECT * INTO document_record FROM core.document
     WHERE id=NEW.document_id AND workspace_id=NEW.workspace_id FOR UPDATE;
    SELECT * INTO revision_record FROM core.article_revision
     WHERE id=NEW.article_revision_id AND workspace_id=NEW.workspace_id AND document_id=NEW.document_id FOR SHARE;
    IF document_record.id IS NULL OR revision_record.id IS NULL
       OR document_record.version<>NEW.document_version
       OR document_record.title<>NEW.title OR document_record.canonical_path<>NEW.target_path
       OR document_record.created_at<>NEW.document_created_at OR document_record.updated_at<>NEW.created_at
       OR document_record.lifecycle_status<>NEW.document_lifecycle
       OR document_record.current_published_revision_id IS DISTINCT FROM NEW.published_revision_id
       OR revision_record.created_by_type<>'AGENT' OR revision_record.source_version_id IS NOT NULL
       OR revision_record.status<>'DRAFT' OR revision_record.git_commit IS NOT NULL
       OR revision_record.optimization_mode<>'NONE'
       OR revision_record.content_hash<>NEW.content_hash
       OR encode(sha256(convert_to(revision_record.content,'UTF8')),'hex')<>NEW.content_hash
       OR octet_length(revision_record.content)>10485760 OR position(chr(13) in revision_record.content)>0
       OR revision_record.parent_revision_id IS DISTINCT FROM NEW.parent_revision_id
       OR revision_record.revision_no<>NEW.revision_no OR revision_record.created_at<>NEW.created_at
       OR EXISTS (
           SELECT 1 FROM core.article_revision
            WHERE document_id=NEW.document_id AND revision_no>NEW.revision_no
       ) OR EXISTS (
           SELECT 1 FROM authoring.document_publication_reservation
            WHERE workspace_id=NEW.workspace_id AND document_id=NEW.document_id AND status='PENDING'
       ) OR EXISTS (
           SELECT 1 FROM authoring.document_publication_binding
            WHERE workspace_id=NEW.workspace_id AND document_id=NEW.document_id AND status IN ('PENDING','RECOVERY_REQUIRED')
       ) THEN
        RAISE EXCEPTION 'generated revision does not match its document and agent provenance' USING ERRCODE='23514';
    END IF;
    IF NEW.parent_revision_id IS NOT NULL THEN
        SELECT revision_no INTO parent_no FROM authoring.generated_article_revision
         WHERE article_revision_id=NEW.parent_revision_id AND workspace_id=NEW.workspace_id
           AND document_id=NEW.document_id AND origin_kind=NEW.origin_kind AND origin_id=NEW.origin_id;
        IF parent_no IS NULL OR parent_no<>NEW.revision_no-1 THEN
            RAISE EXCEPTION 'generated revision parent does not match its origin' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER authoring_generated_revision_validate_insert
    BEFORE INSERT ON authoring.generated_article_revision
    FOR EACH ROW EXECUTE FUNCTION authoring.validate_generated_revision_insert();

-- A generated Document and every AGENT Article Revision within it must close
-- with their immutable origin receipt in the same transaction.
CREATE FUNCTION authoring.verify_generated_revision_closure()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='generated_document' THEN
        IF NOT EXISTS (
            SELECT 1 FROM authoring.generated_article_revision
             WHERE document_id=NEW.document_id AND workspace_id=NEW.workspace_id
               AND origin_kind=NEW.origin_kind AND origin_id=NEW.origin_id AND revision_no=1
        ) THEN
            RAISE EXCEPTION 'generated document requires its initial revision' USING ERRCODE='23514';
        END IF;
    ELSIF NEW.created_by_type='AGENT' AND EXISTS (
        SELECT 1 FROM authoring.generated_document
         WHERE document_id=NEW.document_id AND workspace_id=NEW.workspace_id
    ) AND NOT EXISTS (
        SELECT 1 FROM authoring.generated_article_revision
         WHERE article_revision_id=NEW.id AND workspace_id=NEW.workspace_id
           AND document_id=NEW.document_id AND content_hash=NEW.content_hash
           AND parent_revision_id IS NOT DISTINCT FROM NEW.parent_revision_id
           AND revision_no=NEW.revision_no AND created_at=NEW.created_at
    ) THEN
        RAISE EXCEPTION 'generated agent revision requires its origin receipt' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER authoring_generated_document_verify_closure
    AFTER INSERT ON authoring.generated_document DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION authoring.verify_generated_revision_closure();
CREATE CONSTRAINT TRIGGER authoring_generated_article_verify_closure
    AFTER INSERT ON core.article_revision DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION authoring.verify_generated_revision_closure();

CREATE FUNCTION authoring.validate_generated_retirement_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    binding_record authoring.document_publication_binding%ROWTYPE;
    proposal_record change_control.proposal%ROWTYPE;
    document_version bigint;
    latest_revision uuid;
BEGIN
    SELECT * INTO binding_record FROM authoring.document_publication_binding
     WHERE id=NEW.publication_id AND workspace_id=NEW.workspace_id FOR SHARE;
    SELECT * INTO proposal_record FROM change_control.proposal
     WHERE id=binding_record.proposal_id AND workspace_id=NEW.workspace_id FOR SHARE;
    SELECT version INTO document_version FROM core.document
     WHERE id=NEW.document_id AND workspace_id=NEW.workspace_id FOR SHARE;
    SELECT id INTO latest_revision FROM core.article_revision
     WHERE document_id=NEW.document_id AND workspace_id=NEW.workspace_id
     ORDER BY revision_no DESC LIMIT 1;
    IF binding_record.id IS NULL OR proposal_record.id IS NULL
       OR binding_record.document_id<>NEW.document_id OR binding_record.article_revision_id<>NEW.article_revision_id
       OR latest_revision IS DISTINCT FROM NEW.article_revision_id
       OR document_version IS DISTINCT FROM NEW.expected_document_version
       OR binding_record.status<>'CLOSED' OR binding_record.error_code<>'AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION'
       OR binding_record.git_commit IS NOT NULL OR binding_record.published_at IS NOT NULL
       OR proposal_record.proposal_type<>'file_patch' OR proposal_record.status<>'needs_revision'
       OR proposal_record.current_revision_id IS DISTINCT FROM binding_record.proposal_revision_id
       OR proposal_record.version<>NEW.expected_proposal_version+1 OR proposal_record.workflow_run_id IS NOT NULL
       OR NEW.created_at<binding_record.updated_at OR NEW.created_at<proposal_record.updated_at
       OR EXISTS (SELECT 1 FROM change_control.approval WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.proposal_revision_dispatch WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.tool_authorization WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.writeback_execution WHERE proposal_id=proposal_record.id)
       OR EXISTS (SELECT 1 FROM change_control.proposal_commit WHERE proposal_id=proposal_record.id) THEN
        RAISE EXCEPTION 'generated publication retirement lacks an exact unapproved terminal binding' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER authoring_generated_retirement_validate_insert
    BEFORE INSERT ON authoring.generated_publication_retirement
    FOR EACH ROW EXECUTE FUNCTION authoring.validate_generated_retirement_insert();

CREATE TRIGGER authoring_generated_document_append_only
    BEFORE UPDATE OR DELETE ON authoring.generated_document
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER authoring_generated_document_reject_truncate
    BEFORE TRUNCATE ON authoring.generated_document
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER authoring_generated_revision_append_only
    BEFORE UPDATE OR DELETE ON authoring.generated_article_revision
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER authoring_generated_revision_reject_truncate
    BEFORE TRUNCATE ON authoring.generated_article_revision
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER authoring_generated_retirement_append_only
    BEFORE UPDATE OR DELETE ON authoring.generated_publication_retirement
    FOR EACH ROW EXECUTE FUNCTION authoring.reject_immutable_mutation();
CREATE TRIGGER authoring_generated_retirement_reject_truncate
    BEFORE TRUNCATE ON authoring.generated_publication_retirement
    FOR EACH STATEMENT EXECUTE FUNCTION authoring.reject_immutable_mutation();
