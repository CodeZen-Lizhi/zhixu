CREATE TABLE IF NOT EXISTS change_control.tool_authorization (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    workflow_run_id uuid NOT NULL REFERENCES workflow.run(id) ON DELETE RESTRICT,
    node_run_id uuid NOT NULL REFERENCES workflow.node_run(id) ON DELETE RESTRICT,
    proposal_id uuid NOT NULL REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    revision_id uuid NOT NULL REFERENCES change_control.proposal_revision(id) ON DELETE RESTRICT,
    approval_id uuid NOT NULL REFERENCES change_control.approval(id) ON DELETE RESTRICT,
    tool_name text NOT NULL CHECK (btrim(tool_name) <> '' AND char_length(tool_name) <= 64),
    capability text NOT NULL CHECK (capability IN ('WRITE_KNOWLEDGE','GIT_WRITE')),
    scope text NOT NULL CHECK (btrim(scope) <> '' AND char_length(scope) <= 512),
    approved_change_hash text NOT NULL CHECK (approved_change_hash ~ '^[0-9a-f]{64}$'),
    target_version text NOT NULL CHECK (btrim(target_version) <> ''),
    token_hash text NOT NULL CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    idempotency_key text NOT NULL CHECK (btrim(idempotency_key) <> '' AND char_length(idempotency_key) <= 128),
    status text NOT NULL CHECK (status IN ('issued','consumed','revoked','expired')),
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    consumed_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CONSTRAINT tool_authorization_expiry CHECK (expires_at > issued_at AND expires_at <= issued_at + interval '5 minutes'),
    CONSTRAINT tool_authorization_timestamp_order CHECK (
        (consumed_at IS NULL OR (consumed_at >= issued_at AND consumed_at < expires_at))
        AND (revoked_at IS NULL OR revoked_at >= issued_at)
    ),
    CONSTRAINT tool_authorization_terminal_timestamps CHECK (
        (status = 'consumed' AND consumed_at IS NOT NULL AND revoked_at IS NULL)
        OR (status = 'revoked' AND revoked_at IS NOT NULL AND consumed_at IS NULL)
        OR (status = 'expired' AND consumed_at IS NULL AND revoked_at IS NULL)
        OR (status = 'issued' AND consumed_at IS NULL AND revoked_at IS NULL)
    ),
    CONSTRAINT uq_tool_authorization_idempotency UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT uq_tool_authorization_token_hash UNIQUE (token_hash)
);

CREATE OR REPLACE FUNCTION change_control.verify_tool_authorization_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    proposal_workspace uuid;
    proposal_status text;
    proposal_target_path text;
    revision_proposal uuid;
    revision_base_hash text;
    revision_change_hash text;
    approval_proposal uuid;
    approval_revision uuid;
    approval_change_hash text;
    approval_decision text;
    run_workspace uuid;
    node_run_id uuid;
BEGIN
    IF TG_OP = 'INSERT' AND (NEW.status <> 'issued' OR NEW.version <> 1 OR NEW.consumed_at IS NOT NULL OR NEW.revoked_at IS NOT NULL) THEN
        RAISE EXCEPTION 'tool authorization must be inserted as issued version one' USING ERRCODE = '23514';
    END IF;
    IF (NEW.capability = 'WRITE_KNOWLEDGE' AND NEW.tool_name <> 'ApplyApprovedPatch')
       OR (NEW.capability = 'GIT_WRITE' AND NEW.tool_name <> 'CreateGitCommit') THEN
        RAISE EXCEPTION 'tool authorization capability/tool mismatch' USING ERRCODE = '23514';
    END IF;
    SELECT p.workspace_id, p.status, r.target_path, r.proposal_id, r.base_hash, r.change_hash,
           a.proposal_id, a.revision_id, a.change_hash, a.decision
      INTO proposal_workspace, proposal_status, proposal_target_path, revision_proposal, revision_base_hash, revision_change_hash,
           approval_proposal, approval_revision, approval_change_hash, approval_decision
      FROM change_control.proposal p
      JOIN change_control.proposal_revision r ON r.id = NEW.revision_id
      JOIN change_control.approval a ON a.id = NEW.approval_id
     WHERE p.id = NEW.proposal_id
     FOR UPDATE OF p, r, a;
    IF proposal_workspace IS NULL OR proposal_workspace <> NEW.workspace_id
       OR proposal_status <> 'approved'
       OR NEW.scope <> 'target:' || proposal_target_path
       OR revision_proposal <> NEW.proposal_id
       OR approval_proposal <> NEW.proposal_id
       OR approval_revision <> NEW.revision_id
       OR approval_decision <> 'approved'
       OR revision_change_hash <> NEW.approved_change_hash
       OR approval_change_hash <> NEW.approved_change_hash
       OR revision_base_hash <> NEW.target_version THEN
        RAISE EXCEPTION 'tool authorization approval binding mismatch' USING ERRCODE = '23514';
    END IF;

    SELECT r.workspace_id, n.id
      INTO run_workspace, node_run_id
      FROM workflow.run r
      JOIN workflow.node_run n ON n.run_id = r.id
     WHERE r.id = NEW.workflow_run_id AND n.id = NEW.node_run_id
       AND r.status IN ('pending','running','waiting_for_human')
       AND n.status IN ('pending','running','waiting_for_human')
     FOR UPDATE OF r, n;
    IF run_workspace IS NULL OR run_workspace <> NEW.workspace_id OR node_run_id IS NULL THEN
        RAISE EXCEPTION 'tool authorization workflow binding mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION change_control.validate_tool_authorization_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id <> OLD.id
       OR NEW.workspace_id <> OLD.workspace_id
       OR NEW.workflow_run_id <> OLD.workflow_run_id
       OR NEW.node_run_id <> OLD.node_run_id
       OR NEW.proposal_id <> OLD.proposal_id
       OR NEW.revision_id <> OLD.revision_id
       OR NEW.approval_id <> OLD.approval_id
       OR NEW.tool_name <> OLD.tool_name
       OR NEW.capability <> OLD.capability
       OR NEW.scope <> OLD.scope
       OR NEW.approved_change_hash <> OLD.approved_change_hash
       OR NEW.target_version <> OLD.target_version
       OR NEW.token_hash <> OLD.token_hash
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.issued_at <> OLD.issued_at
       OR NEW.expires_at <> OLD.expires_at
       OR NEW.version <> OLD.version + 1
       OR OLD.status <> 'issued'
       OR NOT (NEW.status IN ('consumed','revoked','expired')) THEN
        RAISE EXCEPTION 'tool authorization immutable field or transition violation' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION change_control.reject_tool_authorization_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'tool authorization is immutable' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS tool_authorization_verify_binding ON change_control.tool_authorization;
CREATE TRIGGER tool_authorization_verify_binding
BEFORE INSERT OR UPDATE OF workspace_id,workflow_run_id,node_run_id,proposal_id,revision_id,approval_id,approved_change_hash,target_version
ON change_control.tool_authorization
FOR EACH ROW EXECUTE FUNCTION change_control.verify_tool_authorization_binding();

DROP TRIGGER IF EXISTS tool_authorization_validate_transition ON change_control.tool_authorization;
CREATE TRIGGER tool_authorization_validate_transition
BEFORE UPDATE ON change_control.tool_authorization
FOR EACH ROW EXECUTE FUNCTION change_control.validate_tool_authorization_transition();
DROP TRIGGER IF EXISTS tool_authorization_reject_delete ON change_control.tool_authorization;
CREATE TRIGGER tool_authorization_reject_delete
BEFORE DELETE ON change_control.tool_authorization
FOR EACH ROW EXECUTE FUNCTION change_control.reject_tool_authorization_delete();

CREATE INDEX IF NOT EXISTS idx_tool_authorization_expiry
    ON change_control.tool_authorization (status, expires_at);
CREATE INDEX IF NOT EXISTS idx_tool_authorization_workflow
    ON change_control.tool_authorization (workspace_id, workflow_run_id, node_run_id, status);

INSERT INTO core.schema_meta (key, value)
VALUES ('write_authorization', 'm5-03')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
