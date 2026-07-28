-- +goose Up

-- Interview completion constructs two Artifact drafts before it can bind the
-- report and learning path in one transaction. A hold keeps either partial
-- draft out of public Artifact queries while preserving command recovery.
CREATE TABLE learning.artifact_visibility_hold (
    workspace_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    owner_type text NOT NULL CHECK (owner_type = 'INTERVIEW_COMPLETE'),
    owner_id uuid NOT NULL,
    owner_role text NOT NULL CHECK (owner_role IN ('REPORT', 'PATH')),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, artifact_id),
    CONSTRAINT uq_learning_artifact_visibility_hold_owner_role
        UNIQUE (workspace_id, owner_type, owner_id, owner_role),
    CONSTRAINT fk_learning_artifact_visibility_hold_artifact
        FOREIGN KEY (artifact_id, workspace_id)
        REFERENCES learning.artifact(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT fk_learning_artifact_visibility_hold_interview
        FOREIGN KEY (owner_id, workspace_id)
        REFERENCES learning.interview_session(session_id, workspace_id) ON DELETE RESTRICT
);

-- +goose Down

-- Dropping a non-empty hold table would make an incomplete Interview Artifact
-- visible. Keep the rows and migration version so a forward fix can resume.
LOCK TABLE learning.artifact_visibility_hold IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM learning.artifact_visibility_hold LIMIT 1) THEN
        RAISE EXCEPTION 'interview artifact visibility holds exist; complete or recover them before rollback'
            USING ERRCODE = '55000';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TABLE learning.artifact_visibility_hold;
