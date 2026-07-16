-- +goose Up
CREATE SCHEMA IF NOT EXISTS change_control;

CREATE TABLE IF NOT EXISTS change_control.proposal (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES core.workspace(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN (
        'draft','validating','ready_for_review','approved','applying','applied',
        'verifying','completed','rejected','needs_revision','deferred',
        'apply_failed','verify_failed','rolled_back','cancelled'
    )),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS change_control.proposal_revision (
    id uuid PRIMARY KEY,
    proposal_id uuid NOT NULL REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    revision_no integer NOT NULL CHECK (revision_no > 0),
    target_path text NOT NULL CHECK (
        btrim(target_path) <> '' AND
        left(target_path, 1) <> '/' AND
        target_path !~ '(^|/)\.\.(/|$)' AND
        position(E'\\\\' in target_path) = 0
    ),
    base_hash text NOT NULL CHECK (base_hash ~ '^[0-9a-f]{64}$'),
    content text NOT NULL CHECK (btrim(content) <> ''),
    evidence_summary text NOT NULL CHECK (btrim(evidence_summary) <> ''),
    risk text NOT NULL CHECK (btrim(risk) <> ''),
    rollback_plan text NOT NULL CHECK (btrim(rollback_plan) <> ''),
    change_hash text NOT NULL CHECK (change_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_proposal_revision_no UNIQUE (proposal_id, revision_no),
    CONSTRAINT uq_proposal_revision_binding UNIQUE (proposal_id, id, change_hash)
);

CREATE TABLE IF NOT EXISTS change_control.approval (
    id uuid PRIMARY KEY,
    proposal_id uuid NOT NULL REFERENCES change_control.proposal(id) ON DELETE RESTRICT,
    revision_id uuid NOT NULL,
    change_hash text NOT NULL CHECK (change_hash ~ '^[0-9a-f]{64}$'),
    decision text NOT NULL CHECK (decision IN ('approved','rejected')),
    decided_at timestamptz NOT NULL,
    CONSTRAINT uq_approval_revision UNIQUE (revision_id),
    CONSTRAINT fk_approval_revision_binding FOREIGN KEY (proposal_id, revision_id, change_hash)
      REFERENCES change_control.proposal_revision(proposal_id, id, change_hash)
      DEFERRABLE INITIALLY IMMEDIATE
);

CREATE INDEX IF NOT EXISTS idx_proposal_workspace_status
    ON change_control.proposal(workspace_id,status,created_at DESC,id);
CREATE INDEX IF NOT EXISTS idx_revision_proposal_created
    ON change_control.proposal_revision(proposal_id,created_at DESC,id);

CREATE OR REPLACE FUNCTION change_control.reject_immutable_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is immutable', TG_TABLE_NAME USING ERRCODE='55000';
END;
$$;

CREATE OR REPLACE FUNCTION change_control.guard_proposal_identity()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.workspace_id <> OLD.workspace_id OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'proposal identity is immutable' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS proposal_guard_identity ON change_control.proposal;
CREATE TRIGGER proposal_guard_identity
BEFORE UPDATE ON change_control.proposal
FOR EACH ROW EXECUTE FUNCTION change_control.guard_proposal_identity();

DROP TRIGGER IF EXISTS proposal_revision_reject_update_delete ON change_control.proposal_revision;
CREATE TRIGGER proposal_revision_reject_update_delete
BEFORE UPDATE OR DELETE ON change_control.proposal_revision
FOR EACH ROW EXECUTE FUNCTION change_control.reject_immutable_mutation();

DROP TRIGGER IF EXISTS approval_reject_update_delete ON change_control.approval;
CREATE TRIGGER approval_reject_update_delete
BEFORE UPDATE OR DELETE ON change_control.approval
FOR EACH ROW EXECUTE FUNCTION change_control.reject_immutable_mutation();

INSERT INTO core.schema_meta(key,value) VALUES ('change_control','m5')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();

-- +goose Down
DROP TRIGGER IF EXISTS proposal_guard_identity ON change_control.proposal;
DROP TRIGGER IF EXISTS approval_reject_update_delete ON change_control.approval;
DROP TRIGGER IF EXISTS proposal_revision_reject_update_delete ON change_control.proposal_revision;
DROP FUNCTION IF EXISTS change_control.guard_proposal_identity();
DROP FUNCTION IF EXISTS change_control.reject_immutable_mutation();
DROP TABLE IF EXISTS change_control.approval;
DROP TABLE IF EXISTS change_control.proposal_revision;
DROP TABLE IF EXISTS change_control.proposal;
DELETE FROM core.schema_meta WHERE key='change_control';
