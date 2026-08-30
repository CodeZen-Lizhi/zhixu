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
