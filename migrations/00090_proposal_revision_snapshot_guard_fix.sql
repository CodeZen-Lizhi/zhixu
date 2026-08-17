-- +goose Up

-- 00082 used the same identifier for the PL/pgSQL variable and the
-- proposal.proposal_type column. PostgreSQL accepts the function definition,
-- but every snapshot INSERT fails at runtime with SQLSTATE 42702.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION change_control.validate_revision_base_snapshot()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    revision_row change_control.proposal_revision%ROWTYPE;
    proposal_type_value text;
BEGIN
    SELECT revision.* INTO revision_row
      FROM change_control.proposal_revision AS revision
     WHERE revision.id=NEW.revision_id
       AND revision.proposal_id=NEW.proposal_id;
    SELECT proposal.proposal_type INTO proposal_type_value
      FROM change_control.proposal AS proposal
     WHERE proposal.id=NEW.proposal_id;
    IF revision_row.id IS NULL
       OR proposal_type_value<>'file_patch'
       OR revision_row.target_mode<>'REPLACE'
       OR revision_row.base_hash<>NEW.base_hash
       OR encode(sha256(convert_to(NEW.content,'UTF8')),'hex')<>NEW.base_hash THEN
        RAISE EXCEPTION 'proposal revision base snapshot binding mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose Down

-- This is a monotonic repair. Reinstalling the ambiguous 00082 function would
-- knowingly restore a write outage, so a version rollback preserves the fixed
-- function body. Reapplying 00090 is therefore idempotent.
SELECT 1;
