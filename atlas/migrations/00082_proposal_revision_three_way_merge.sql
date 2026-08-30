-- Proposal Revision append-only storage. This migration is additive; previous
-- migrations are intentionally left untouched.

ALTER TABLE change_control.proposal
    ADD COLUMN IF NOT EXISTS current_revision_id uuid;

ALTER TABLE change_control.proposal_revision
    ADD CONSTRAINT uq_proposal_revision_proposal_id_id UNIQUE (proposal_id,id);

DO $$
DECLARE missing_count bigint;
BEGIN
    SELECT count(*) INTO missing_count
      FROM change_control.proposal p
     WHERE NOT EXISTS (SELECT 1 FROM change_control.proposal_revision r WHERE r.proposal_id=p.id);
    IF missing_count <> 0 THEN
        RAISE EXCEPTION 'proposal revision pointer backfill found proposals without revisions: %', missing_count USING ERRCODE='23514';
    END IF;
END $$;

UPDATE change_control.proposal p
   SET current_revision_id = (
       SELECT r.id FROM change_control.proposal_revision r
        WHERE r.proposal_id=p.id ORDER BY r.revision_no DESC,r.id DESC LIMIT 1
   )
 WHERE p.current_revision_id IS NULL;

-- Keep the column nullable during the expand window.  Older integration and
-- owner projections may insert a Proposal before its Revision; new Change
-- Control commands always write the pointer in the same transaction and the
-- feature gate treats a missing pointer as legacy/read-only.
ALTER TABLE change_control.proposal
    ADD CONSTRAINT fk_proposal_current_revision
      FOREIGN KEY (id,current_revision_id)
      REFERENCES change_control.proposal_revision(proposal_id,id)
      DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE IF NOT EXISTS change_control.proposal_revision_base_snapshot (
    proposal_id uuid NOT NULL,
    revision_id uuid PRIMARY KEY,
    base_hash text NOT NULL CHECK (base_hash ~ '^[0-9a-f]{64}$'),
    content text NOT NULL,
    base_byte_size integer NOT NULL CHECK (base_byte_size=octet_length(content) AND base_byte_size BETWEEN 0 AND 1048576),
    schema_version text NOT NULL DEFAULT 'proposal-base-snapshot/v1' CHECK (schema_version='proposal-base-snapshot/v1'),
    created_at timestamptz NOT NULL,
    CONSTRAINT fk_proposal_revision_snapshot FOREIGN KEY (proposal_id,revision_id)
      REFERENCES change_control.proposal_revision(proposal_id,id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS change_control.proposal_revision_lineage (
    proposal_id uuid NOT NULL,
    revision_id uuid PRIMARY KEY,
    source_revision_id uuid NOT NULL,
    source_change_hash text NOT NULL CHECK (source_change_hash ~ '^[0-9a-f]{64}$'),
    lineage_kind text NOT NULL CHECK (lineage_kind IN ('DIRECT_EDIT','THREE_WAY_MERGE')),
    merge_algorithm text NOT NULL CHECK (btrim(merge_algorithm)<>'' AND octet_length(merge_algorithm)<=128),
    merge_algorithm_version text NOT NULL CHECK (btrim(merge_algorithm_version)<>'' AND octet_length(merge_algorithm_version)<=128),
    merge_fingerprint text NOT NULL CHECK (merge_fingerprint ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_proposal_revision_lineage_source UNIQUE (source_revision_id),
    CONSTRAINT fk_proposal_revision_lineage_revision FOREIGN KEY (proposal_id,revision_id)
      REFERENCES change_control.proposal_revision(proposal_id,id) ON DELETE RESTRICT,
    CONSTRAINT fk_proposal_revision_lineage_source FOREIGN KEY (proposal_id,source_revision_id)
      REFERENCES change_control.proposal_revision(proposal_id,id) ON DELETE RESTRICT,
    CONSTRAINT lineage_not_self CHECK (revision_id<>source_revision_id)
);

CREATE TABLE IF NOT EXISTS change_control.proposal_revision_command (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key)<>'' AND octet_length(idempotency_key)<=128 AND idempotency_key !~ '[\r\n]'),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    proposal_id uuid NOT NULL REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    source_revision_id uuid NOT NULL,
    source_change_hash text NOT NULL CHECK (source_change_hash ~ '^[0-9a-f]{64}$'),
    expected_proposal_version bigint NOT NULL CHECK (expected_proposal_version>0),
    expected_current_hash text NOT NULL CHECK (expected_current_hash ~ '^[0-9a-f]{64}$'),
    preview_hash text NOT NULL CHECK (preview_hash ~ '^[0-9a-f]{64}$'),
    merge_algorithm text NOT NULL CHECK (btrim(merge_algorithm)<>'' AND octet_length(merge_algorithm)<=128),
    merge_algorithm_version text NOT NULL CHECK (btrim(merge_algorithm_version)<>'' AND octet_length(merge_algorithm_version)<=128),
    result_revision_id uuid NOT NULL UNIQUE,
    result_revision_no integer NOT NULL CHECK (result_revision_no>1),
    result_proposal_version bigint NOT NULL CHECK (result_proposal_version=expected_proposal_version+1),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id,idempotency_key),
    CONSTRAINT fk_revision_command_source FOREIGN KEY (proposal_id,source_revision_id)
      REFERENCES change_control.proposal_revision(proposal_id,id) ON DELETE RESTRICT,
    CONSTRAINT fk_revision_command_result FOREIGN KEY (proposal_id,result_revision_id)
      REFERENCES change_control.proposal_revision(proposal_id,id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS change_control.proposal_revision_dispatch (
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    proposal_id uuid NOT NULL REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    revision_id uuid PRIMARY KEY,
    approval_id uuid NOT NULL UNIQUE REFERENCES change_control.approval(id) ON DELETE RESTRICT,
    workflow_run_id uuid NOT NULL UNIQUE REFERENCES workflow.run(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL,
    CONSTRAINT fk_revision_dispatch_revision FOREIGN KEY (proposal_id,revision_id)
      REFERENCES change_control.proposal_revision(proposal_id,id) ON DELETE RESTRICT
);

-- Snapshot, lineage, receipt and dispatch facts are append-only.
CREATE OR REPLACE FUNCTION change_control.reject_revision_append_only_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE='55000';
END $$;

DROP TRIGGER IF EXISTS proposal_revision_snapshot_append_only ON change_control.proposal_revision_base_snapshot;
CREATE TRIGGER proposal_revision_snapshot_append_only BEFORE UPDATE OR DELETE ON change_control.proposal_revision_base_snapshot
FOR EACH ROW EXECUTE FUNCTION change_control.reject_revision_append_only_mutation();
DROP TRIGGER IF EXISTS proposal_revision_snapshot_reject_truncate ON change_control.proposal_revision_base_snapshot;
CREATE TRIGGER proposal_revision_snapshot_reject_truncate BEFORE TRUNCATE ON change_control.proposal_revision_base_snapshot
FOR EACH STATEMENT EXECUTE FUNCTION change_control.reject_revision_append_only_mutation();
DROP TRIGGER IF EXISTS proposal_revision_lineage_append_only ON change_control.proposal_revision_lineage;
CREATE TRIGGER proposal_revision_lineage_append_only BEFORE UPDATE OR DELETE ON change_control.proposal_revision_lineage
FOR EACH ROW EXECUTE FUNCTION change_control.reject_revision_append_only_mutation();
DROP TRIGGER IF EXISTS proposal_revision_lineage_reject_truncate ON change_control.proposal_revision_lineage;
CREATE TRIGGER proposal_revision_lineage_reject_truncate BEFORE TRUNCATE ON change_control.proposal_revision_lineage
FOR EACH STATEMENT EXECUTE FUNCTION change_control.reject_revision_append_only_mutation();
DROP TRIGGER IF EXISTS proposal_revision_command_append_only ON change_control.proposal_revision_command;
CREATE TRIGGER proposal_revision_command_append_only BEFORE UPDATE OR DELETE ON change_control.proposal_revision_command
FOR EACH ROW EXECUTE FUNCTION change_control.reject_revision_append_only_mutation();
DROP TRIGGER IF EXISTS proposal_revision_command_reject_truncate ON change_control.proposal_revision_command;
CREATE TRIGGER proposal_revision_command_reject_truncate BEFORE TRUNCATE ON change_control.proposal_revision_command
FOR EACH STATEMENT EXECUTE FUNCTION change_control.reject_revision_append_only_mutation();
DROP TRIGGER IF EXISTS proposal_revision_dispatch_append_only ON change_control.proposal_revision_dispatch;
CREATE TRIGGER proposal_revision_dispatch_append_only BEFORE UPDATE OR DELETE ON change_control.proposal_revision_dispatch
FOR EACH ROW EXECUTE FUNCTION change_control.reject_revision_append_only_mutation();
DROP TRIGGER IF EXISTS proposal_revision_dispatch_reject_truncate ON change_control.proposal_revision_dispatch;
CREATE TRIGGER proposal_revision_dispatch_reject_truncate BEFORE TRUNCATE ON change_control.proposal_revision_dispatch
FOR EACH STATEMENT EXECUTE FUNCTION change_control.reject_revision_append_only_mutation();

CREATE OR REPLACE FUNCTION change_control.validate_proposal_revision_dispatch()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE approval_proposal uuid; approval_revision uuid; approval_decision text; approval_hash text;
        revision_hash text; proposal_workspace uuid; proposal_current_revision uuid; run_workspace uuid;
BEGIN
    SELECT proposal_id,revision_id,decision,change_hash INTO approval_proposal,approval_revision,approval_decision,approval_hash
      FROM change_control.approval WHERE id=NEW.approval_id;
    SELECT change_hash INTO revision_hash FROM change_control.proposal_revision
      WHERE proposal_id=NEW.proposal_id AND id=NEW.revision_id;
    SELECT workspace_id,current_revision_id INTO proposal_workspace,proposal_current_revision
      FROM change_control.proposal WHERE id=NEW.proposal_id FOR UPDATE;
    SELECT workspace_id INTO run_workspace FROM workflow.run WHERE id=NEW.workflow_run_id;
    IF approval_proposal IS NULL OR approval_revision IS NULL OR revision_hash IS NULL
       OR proposal_workspace IS NULL OR proposal_current_revision IS NULL OR run_workspace IS NULL
       OR approval_proposal<>NEW.proposal_id OR approval_revision<>NEW.revision_id
       OR approval_decision<>'approved' OR approval_hash<>revision_hash
       OR proposal_workspace<>NEW.workspace_id OR proposal_current_revision<>NEW.revision_id
       OR run_workspace<>NEW.workspace_id THEN
        RAISE EXCEPTION 'proposal revision dispatch binding mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS proposal_revision_dispatch_validate ON change_control.proposal_revision_dispatch;
CREATE TRIGGER proposal_revision_dispatch_validate BEFORE INSERT ON change_control.proposal_revision_dispatch
FOR EACH ROW EXECUTE FUNCTION change_control.validate_proposal_revision_dispatch();

CREATE OR REPLACE FUNCTION change_control.validate_revision_base_snapshot()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE revision change_control.proposal_revision%ROWTYPE; proposal_type text;
BEGIN
    SELECT * INTO revision FROM change_control.proposal_revision WHERE id=NEW.revision_id AND proposal_id=NEW.proposal_id;
    SELECT proposal_type INTO proposal_type FROM change_control.proposal WHERE id=NEW.proposal_id;
    IF revision.id IS NULL OR proposal_type<>'file_patch' OR revision.target_mode<>'REPLACE'
       OR revision.base_hash<>NEW.base_hash
       OR encode(sha256(convert_to(NEW.content,'UTF8')),'hex')<>NEW.base_hash THEN
        RAISE EXCEPTION 'proposal revision base snapshot binding mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS proposal_revision_snapshot_validate ON change_control.proposal_revision_base_snapshot;
CREATE TRIGGER proposal_revision_snapshot_validate BEFORE INSERT ON change_control.proposal_revision_base_snapshot
FOR EACH ROW EXECUTE FUNCTION change_control.validate_revision_base_snapshot();

CREATE OR REPLACE FUNCTION change_control.validate_revision_lineage()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE source_no integer; result_no integer; source_hash text;
BEGIN
    SELECT revision_no,change_hash INTO source_no,source_hash FROM change_control.proposal_revision
     WHERE proposal_id=NEW.proposal_id AND id=NEW.source_revision_id;
    SELECT revision_no INTO result_no FROM change_control.proposal_revision
     WHERE proposal_id=NEW.proposal_id AND id=NEW.revision_id;
    IF source_no IS NULL OR result_no<>source_no+1 OR source_hash<>NEW.source_change_hash THEN
        RAISE EXCEPTION 'proposal revision lineage binding mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS proposal_revision_lineage_validate ON change_control.proposal_revision_lineage;
CREATE TRIGGER proposal_revision_lineage_validate BEFORE INSERT ON change_control.proposal_revision_lineage
FOR EACH ROW EXECUTE FUNCTION change_control.validate_revision_lineage();

CREATE OR REPLACE FUNCTION change_control.validate_revision_command_receipt()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    proposal_workspace uuid;
    source_no integer;
    source_hash text;
    result_no integer;
    result_base_hash text;
    lineage_source uuid;
    lineage_fingerprint text;
BEGIN
    SELECT workspace_id INTO proposal_workspace
      FROM change_control.proposal WHERE id=NEW.proposal_id;
    SELECT revision_no,change_hash INTO source_no,source_hash
      FROM change_control.proposal_revision
     WHERE proposal_id=NEW.proposal_id AND id=NEW.source_revision_id;
    SELECT revision_no,base_hash INTO result_no,result_base_hash
      FROM change_control.proposal_revision
     WHERE proposal_id=NEW.proposal_id AND id=NEW.result_revision_id;
    SELECT source_revision_id,merge_fingerprint INTO lineage_source,lineage_fingerprint
      FROM change_control.proposal_revision_lineage
     WHERE proposal_id=NEW.proposal_id AND revision_id=NEW.result_revision_id;
    IF proposal_workspace IS NULL OR proposal_workspace<>NEW.workspace_id
       OR source_no IS NULL OR source_hash<>NEW.source_change_hash
       OR result_no IS NULL OR result_no<>source_no+1 OR result_no<>NEW.result_revision_no
       OR result_base_hash<>NEW.expected_current_hash
       OR lineage_source IS NULL OR lineage_fingerprint IS NULL
       OR lineage_source<>NEW.source_revision_id OR lineage_fingerprint<>NEW.preview_hash THEN
        RAISE EXCEPTION 'proposal revision command receipt binding mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS proposal_revision_command_validate ON change_control.proposal_revision_command;
CREATE TRIGGER proposal_revision_command_validate BEFORE INSERT ON change_control.proposal_revision_command
FOR EACH ROW EXECUTE FUNCTION change_control.validate_revision_command_receipt();

-- The pointer update happens before the receipt is inserted in the append UoW.
-- A deferred constraint verifies the complete closure at commit, so a caller
-- with direct database access cannot advance the aggregate with only a bare
-- proposal_revision row.
CREATE OR REPLACE FUNCTION change_control.validate_revision_append_closure()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    source_change_hash text;
    result_base_hash text;
    snapshot_base_hash text;
    lineage_source uuid;
    lineage_source_hash text;
    lineage_algorithm text;
    lineage_algorithm_version text;
    lineage_fingerprint text;
    receipt_workspace uuid;
    receipt_source uuid;
    receipt_source_hash text;
    receipt_expected_version bigint;
    receipt_expected_hash text;
    receipt_preview_hash text;
    receipt_algorithm text;
    receipt_algorithm_version text;
    receipt_result_version bigint;
BEGIN
    IF NEW.current_revision_id IS NOT DISTINCT FROM OLD.current_revision_id THEN
        RETURN NEW;
    END IF;
    SELECT change_hash INTO source_change_hash
      FROM change_control.proposal_revision
     WHERE proposal_id=NEW.id AND id=OLD.current_revision_id;
    SELECT base_hash INTO result_base_hash
      FROM change_control.proposal_revision
     WHERE proposal_id=NEW.id AND id=NEW.current_revision_id;
    SELECT base_hash INTO snapshot_base_hash
      FROM change_control.proposal_revision_base_snapshot
     WHERE proposal_id=NEW.id AND revision_id=NEW.current_revision_id;
    SELECT source_revision_id,source_change_hash,merge_algorithm,merge_algorithm_version,merge_fingerprint
      INTO lineage_source,lineage_source_hash,lineage_algorithm,lineage_algorithm_version,lineage_fingerprint
      FROM change_control.proposal_revision_lineage
     WHERE proposal_id=NEW.id AND revision_id=NEW.current_revision_id;
    SELECT workspace_id,source_revision_id,source_change_hash,expected_proposal_version,
           expected_current_hash,preview_hash,merge_algorithm,merge_algorithm_version,result_proposal_version
      INTO receipt_workspace,receipt_source,receipt_source_hash,receipt_expected_version,
           receipt_expected_hash,receipt_preview_hash,receipt_algorithm,receipt_algorithm_version,receipt_result_version
      FROM change_control.proposal_revision_command
     WHERE proposal_id=NEW.id AND result_revision_id=NEW.current_revision_id;
    IF source_change_hash IS NULL OR result_base_hash IS NULL OR snapshot_base_hash IS NULL
       OR lineage_source IS NULL OR receipt_workspace IS NULL
       OR snapshot_base_hash<>result_base_hash
       OR lineage_source<>OLD.current_revision_id OR lineage_source_hash<>source_change_hash
       OR receipt_workspace<>NEW.workspace_id OR receipt_source<>OLD.current_revision_id
       OR receipt_source_hash<>source_change_hash OR receipt_expected_version<>OLD.version
       OR receipt_expected_hash<>result_base_hash OR receipt_preview_hash<>lineage_fingerprint
       OR receipt_algorithm<>lineage_algorithm OR receipt_algorithm_version<>lineage_algorithm_version
       OR receipt_result_version<>NEW.version THEN
        RAISE EXCEPTION 'proposal revision append closure is incomplete' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS proposal_revision_append_closure ON change_control.proposal;
CREATE CONSTRAINT TRIGGER proposal_revision_append_closure
AFTER UPDATE ON change_control.proposal
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
WHEN (OLD.current_revision_id IS DISTINCT FROM NEW.current_revision_id)
EXECUTE FUNCTION change_control.validate_revision_append_closure();

CREATE OR REPLACE FUNCTION change_control.validate_proposal_revision_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE source_revision change_control.proposal_revision%ROWTYPE; current_revision change_control.proposal_revision%ROWTYPE;
BEGIN
    IF TG_OP='INSERT' THEN
        RETURN NEW;
    END IF;
    IF NEW.current_revision_id IS NOT DISTINCT FROM OLD.current_revision_id THEN
        RETURN NEW;
    END IF;
    SELECT * INTO source_revision FROM change_control.proposal_revision WHERE proposal_id=NEW.id AND id=OLD.current_revision_id;
    SELECT * INTO current_revision FROM change_control.proposal_revision WHERE proposal_id=NEW.id AND id=NEW.current_revision_id;
    IF OLD.status NOT IN ('ready_for_review','needs_revision') OR NEW.status<>'ready_for_review'
       OR NEW.version<>OLD.version+1 OR source_revision.id IS NULL OR current_revision.id IS NULL
       OR current_revision.revision_no<>source_revision.revision_no+1
       OR current_revision.target_path<>source_revision.target_path OR current_revision.target_mode<>'REPLACE' THEN
        RAISE EXCEPTION 'proposal current revision transition is not allowed' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS proposal_revision_pointer_transition ON change_control.proposal;
CREATE TRIGGER proposal_revision_pointer_transition BEFORE UPDATE OF current_revision_id ON change_control.proposal
FOR EACH ROW EXECUTE FUNCTION change_control.validate_proposal_revision_transition();

-- The active Proposal transition function is replaced here (rather than
-- editing an already published migration) so an append can atomically move
-- needs_revision -> ready_for_review together with the explicit pointer.  All
-- historical status and workflow-binding transitions remain unchanged.
CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    workflow_binding_added boolean;
    pointer_changed boolean;
    source_no integer;
    result_no integer;
    source_path text;
    result_path text;
    source_mode text;
    result_mode text;
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.version<>1 THEN
            RAISE EXCEPTION 'proposal must start at version one' USING ERRCODE='23514';
        END IF;
        IF NEW.proposal_type='publish_artifact' AND NEW.workflow_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'publish artifact proposal cannot bind a writeback workflow' USING ERRCODE='23514';
        END IF;
        IF NEW.proposal_type='downstream_update'
           AND (NEW.workflow_run_id IS NOT NULL OR NEW.status<>'ready_for_review' OR NEW.risk_level<>'HIGH') THEN
            RAISE EXCEPTION 'downstream update proposal must start approval-only' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.id<>OLD.id
       OR NEW.workspace_id<>OLD.workspace_id
       OR NEW.idempotency_key<>OLD.idempotency_key
       OR NEW.request_hash<>OLD.request_hash
       OR NEW.proposal_type<>OLD.proposal_type
       OR NEW.risk_level<>OLD.risk_level
       OR NEW.created_at<>OLD.created_at
       OR NEW.updated_at<OLD.updated_at
       OR NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'proposal immutable field or version violation' USING ERRCODE='23514';
    END IF;

    pointer_changed := NEW.current_revision_id IS DISTINCT FROM OLD.current_revision_id;
    IF pointer_changed THEN
        IF NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
           OR NEW.proposal_type<>'file_patch'
           OR OLD.status NOT IN ('ready_for_review','needs_revision')
           OR NEW.status<>'ready_for_review'
           OR OLD.current_revision_id IS NULL OR NEW.current_revision_id IS NULL THEN
            RAISE EXCEPTION 'proposal revision pointer transition is not allowed' USING ERRCODE='23514';
        END IF;
        SELECT revision_no,target_path,target_mode
          INTO source_no,source_path,source_mode
          FROM change_control.proposal_revision
         WHERE proposal_id=NEW.id AND id=OLD.current_revision_id;
        SELECT revision_no,target_path,target_mode
          INTO result_no,result_path,result_mode
          FROM change_control.proposal_revision
         WHERE proposal_id=NEW.id AND id=NEW.current_revision_id;
        IF source_no IS NULL OR result_no<>source_no+1
           OR source_path<>result_path OR source_mode<>'REPLACE' OR result_mode<>'REPLACE' THEN
            RAISE EXCEPTION 'proposal revision pointer binding is invalid' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.proposal_type='publish_artifact' AND NEW.workflow_run_id IS NOT NULL THEN
        RAISE EXCEPTION 'publish artifact proposal cannot bind a writeback workflow' USING ERRCODE='23514';
    END IF;
    IF NEW.proposal_type='downstream_update' THEN
        IF NEW.workflow_run_id IS NOT NULL
           OR NOT (OLD.status='ready_for_review' AND NEW.status IN ('approved','rejected')) THEN
            RAISE EXCEPTION 'downstream update apply is unavailable' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

    workflow_binding_added := OLD.workflow_run_id IS NULL AND NEW.workflow_run_id IS NOT NULL;
    IF workflow_binding_added
       AND NEW.status='approved'
       AND OLD.status IN ('ready_for_review','approved') THEN
        RETURN NEW;
    END IF;

    IF NOT (
        (OLD.status='ready_for_review' AND NEW.status IN ('approved','rejected','needs_revision'))
        OR (OLD.status='approved' AND NEW.status IN ('applying','needs_revision'))
        OR (OLD.status='applying' AND NEW.status IN ('applied','apply_failed','needs_revision'))
        OR (OLD.status='applied' AND NEW.status='verifying')
        OR (OLD.status='verifying' AND NEW.status IN ('completed','verify_failed','rolled_back'))
        OR (OLD.status='verify_failed' AND NEW.status IN ('verifying','rolled_back'))
        OR (OLD.status='apply_failed' AND NEW.status IN ('applying','rolled_back'))
        OR (OLD.status='needs_revision' AND NEW.status IN ('draft','cancelled'))
    ) THEN
        RAISE EXCEPTION 'proposal status transition is not allowed' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

-- Existing authorization and writeback triggers validate the complete
-- Approval/Workflow tuple. These trailing triggers add the new exact-current
-- invariant after those locks have been acquired, so old credentials cannot
-- become usable again when a later Revision is approved.
CREATE OR REPLACE FUNCTION change_control.validate_authorization_current_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_revision uuid;
    dispatch_run uuid;
    dispatch_approval uuid;
BEGIN
    IF NEW.status NOT IN ('issued','consumed') THEN
        RETURN NEW;
    END IF;
    SELECT p.current_revision_id,d.workflow_run_id,d.approval_id
      INTO current_revision,dispatch_run,dispatch_approval
      FROM change_control.proposal p
      LEFT JOIN change_control.proposal_revision_dispatch d
        ON d.proposal_id=p.id AND d.revision_id=NEW.revision_id
     WHERE p.id=NEW.proposal_id
     FOR UPDATE OF p;
    IF current_revision IS NULL OR current_revision<>NEW.revision_id
       OR dispatch_run IS NULL OR dispatch_run<>NEW.workflow_run_id
       OR dispatch_approval IS NULL OR dispatch_approval<>NEW.approval_id THEN
        RAISE EXCEPTION 'tool authorization is not bound to the current revision dispatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS zzz_tool_authorization_current_revision ON change_control.tool_authorization;
CREATE TRIGGER zzz_tool_authorization_current_revision
BEFORE INSERT OR UPDATE OF status,proposal_id,revision_id,approval_id,workflow_run_id
ON change_control.tool_authorization
FOR EACH ROW EXECUTE FUNCTION change_control.validate_authorization_current_revision();

CREATE OR REPLACE FUNCTION change_control.validate_writeback_current_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_revision uuid;
    dispatch_run uuid;
    dispatch_approval uuid;
BEGIN
    SELECT p.current_revision_id,d.workflow_run_id,d.approval_id
      INTO current_revision,dispatch_run,dispatch_approval
      FROM change_control.proposal p
      LEFT JOIN change_control.proposal_revision_dispatch d
        ON d.proposal_id=p.id AND d.revision_id=NEW.revision_id
     WHERE p.id=NEW.proposal_id
     FOR UPDATE OF p;
    IF current_revision IS NULL OR current_revision<>NEW.revision_id
       OR dispatch_run IS NULL OR dispatch_run<>NEW.workflow_run_id
       OR dispatch_approval IS NULL OR dispatch_approval<>NEW.approval_id THEN
        RAISE EXCEPTION 'writeback execution is not bound to the current revision dispatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS zzz_writeback_execution_current_revision ON change_control.writeback_execution;
CREATE TRIGGER zzz_writeback_execution_current_revision
BEFORE INSERT ON change_control.writeback_execution
FOR EACH ROW EXECUTE FUNCTION change_control.validate_writeback_current_revision();

-- Every legacy Proposal-level Workflow binding must resolve through the
-- immutable safe-writeback node input. Refuse the migration instead of guessing
-- from the maximum Revision or a partially-created execution.
DO $$
DECLARE
    unresolved_count bigint;
BEGIN
    SELECT count(*)
      INTO unresolved_count
      FROM change_control.proposal p
     WHERE p.workflow_run_id IS NOT NULL
       AND (
           SELECT count(*)
             FROM workflow.node_run n
             JOIN change_control.proposal_revision r
               ON r.proposal_id=p.id AND r.id::text=n.input->>'revision_id'
             JOIN change_control.approval a
               ON a.proposal_id=p.id AND a.revision_id=r.id
              AND a.decision='approved' AND a.change_hash=r.change_hash
            WHERE n.run_id=p.workflow_run_id
              AND n.node_key='safe-writeback'
              AND n.node_type='change_control.safe_writeback'
              AND n.input_schema_version=1
              AND n.input->>'schema_version'='1'
              AND n.input->>'proposal_id'=p.id::text
              AND n.input->>'approved_change_hash'=r.change_hash
       )<>1;
    IF unresolved_count<>0 THEN
        RAISE EXCEPTION 'legacy proposal workflow bindings are ambiguous or orphaned: %', unresolved_count USING ERRCODE='23514';
    END IF;
END;
$$;

INSERT INTO change_control.proposal_revision_dispatch(workspace_id,proposal_id,revision_id,approval_id,workflow_run_id,created_at)
SELECT p.workspace_id,p.id,r.id,a.id,p.workflow_run_id,n.created_at
  FROM change_control.proposal p
  JOIN workflow.node_run n
    ON n.run_id=p.workflow_run_id
   AND n.node_key='safe-writeback'
   AND n.node_type='change_control.safe_writeback'
   AND n.input_schema_version=1
   AND n.input->>'schema_version'='1'
   AND n.input->>'proposal_id'=p.id::text
  JOIN change_control.proposal_revision r
    ON r.proposal_id=p.id
   AND r.id::text=n.input->>'revision_id'
   AND r.change_hash=n.input->>'approved_change_hash'
  JOIN change_control.approval a
    ON a.proposal_id=p.id AND a.revision_id=r.id
   AND a.decision='approved' AND a.change_hash=r.change_hash
 WHERE p.workflow_run_id IS NOT NULL;

DO $$
DECLARE
    invalid_authorization_count bigint;
    invalid_execution_count bigint;
BEGIN
    SELECT count(*)
      INTO invalid_authorization_count
      FROM change_control.tool_authorization auth
      JOIN change_control.proposal proposal ON proposal.id=auth.proposal_id
      LEFT JOIN change_control.proposal_revision_dispatch dispatch
        ON dispatch.proposal_id=auth.proposal_id
       AND dispatch.revision_id=auth.revision_id
     WHERE auth.status IN ('issued','consumed')
       AND (proposal.current_revision_id IS DISTINCT FROM auth.revision_id
            OR dispatch.approval_id IS DISTINCT FROM auth.approval_id
            OR dispatch.workflow_run_id IS DISTINCT FROM auth.workflow_run_id);
    SELECT count(*)
      INTO invalid_execution_count
      FROM change_control.writeback_execution execution
      JOIN change_control.proposal proposal ON proposal.id=execution.proposal_id
      LEFT JOIN change_control.proposal_revision_dispatch dispatch
        ON dispatch.proposal_id=execution.proposal_id
       AND dispatch.revision_id=execution.revision_id
     WHERE proposal.current_revision_id IS DISTINCT FROM execution.revision_id
        OR dispatch.approval_id IS DISTINCT FROM execution.approval_id
        OR dispatch.workflow_run_id IS DISTINCT FROM execution.workflow_run_id;
    IF invalid_authorization_count<>0 OR invalid_execution_count<>0 THEN
        RAISE EXCEPTION 'legacy change-control execution bindings are inconsistent: authorizations %, executions %', invalid_authorization_count, invalid_execution_count USING ERRCODE='23514';
    END IF;
END;
$$;

INSERT INTO core.schema_meta(key,value) VALUES ('change_control_revision','proposal-revision/v1')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();
