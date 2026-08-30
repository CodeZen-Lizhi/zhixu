CREATE TABLE agent.workspace_analysis_tool_refusal (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    analysis_run_id uuid NOT NULL,
    workflow_run_id uuid NOT NULL,
    node_run_id uuid NOT NULL,
    node_key text NOT NULL,
    operation_kind text NOT NULL,
    ordinal integer NOT NULL,
    error_code text NOT NULL CHECK (error_code IN (
        'TOOL_ALLOWED_VERSION_AMBIGUOUS',
        'TOOL_INVOCATION_DENIED',
        'TOOL_WORKFLOW_BINDING_DENIED',
        'TOOL_NOT_ALLOWED',
        'TOOL_PERMISSION_DENIED',
        'TOOL_INPUT_TOO_LARGE',
        'TOOL_INPUT_INVALID',
        'TOOL_IDEMPOTENCY_REQUIRED',
        'TOOL_IDEMPOTENCY_UNEXPECTED',
        'TOOL_WORKSPACE_ANALYSIS_CONTRACT_DENIED'
    )),
    audit_event_id uuid NOT NULL UNIQUE,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_workspace_analysis_tool_refusal_slot
        UNIQUE (analysis_run_id,node_key,operation_kind,ordinal),
    CONSTRAINT fk_workspace_analysis_tool_refusal_run
        FOREIGN KEY (analysis_run_id,workspace_id,workflow_run_id)
        REFERENCES agent.workspace_analysis_run(id,workspace_id,workflow_run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_tool_refusal_node_run
        FOREIGN KEY (node_run_id,workflow_run_id)
        REFERENCES workflow.node_run(id,run_id) ON DELETE RESTRICT,
    CONSTRAINT fk_workspace_analysis_tool_refusal_audit
        FOREIGN KEY (audit_event_id) REFERENCES ops.audit_event(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT workspace_analysis_tool_refusal_slot CHECK (
        (node_key='inspect_workspace' AND operation_kind='GIT_STATUS' AND ordinal=1)
        OR (node_key='retrieve_evidence' AND operation_kind='KNOWLEDGE_SEARCH' AND ordinal=1)
        OR (node_key='read_evidence' AND operation_kind='SOURCE_READ' AND ordinal BETWEEN 1 AND 3)
        OR (node_key='validate_citations' AND operation_kind='CITATION_VALIDATION' AND ordinal=1)
    )
);

CREATE INDEX idx_workspace_analysis_tool_refusal_run
    ON agent.workspace_analysis_tool_refusal (workspace_id,analysis_run_id,created_at,id);

CREATE FUNCTION agent.guard_workspace_analysis_tool_refusal_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM agent.workspace_analysis_run AS analysis
        JOIN workflow.run AS workflow
          ON workflow.id=analysis.workflow_run_id
         AND workflow.workspace_id=analysis.workspace_id
        JOIN workflow.node_run AS node
          ON node.id=NEW.node_run_id
         AND node.run_id=workflow.id
         AND node.node_key=NEW.node_key
        WHERE analysis.id=NEW.analysis_run_id
          AND analysis.workspace_id=NEW.workspace_id
          AND analysis.workflow_run_id=NEW.workflow_run_id
          AND analysis.status='running'
          AND workflow.status='running'
          AND workflow.cancel_requested_at IS NULL
          AND node.status='running'
    ) THEN
        RAISE EXCEPTION 'workspace analysis tool refusal binding is invalid'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER workspace_analysis_tool_refusal_guard_insert
    BEFORE INSERT ON agent.workspace_analysis_tool_refusal
    FOR EACH ROW EXECUTE FUNCTION agent.guard_workspace_analysis_tool_refusal_insert();

CREATE FUNCTION agent.reject_workspace_analysis_tool_refusal_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'workspace analysis tool refusal facts are immutable'
        USING ERRCODE='55000';
END;
$$;

CREATE TRIGGER workspace_analysis_tool_refusal_reject_mutation
    BEFORE UPDATE OR DELETE ON agent.workspace_analysis_tool_refusal
    FOR EACH ROW EXECUTE FUNCTION agent.reject_workspace_analysis_tool_refusal_mutation();

CREATE TRIGGER workspace_analysis_tool_refusal_reject_truncate
    BEFORE TRUNCATE ON agent.workspace_analysis_tool_refusal
    FOR EACH STATEMENT EXECUTE FUNCTION agent.reject_workspace_analysis_tool_refusal_mutation();

INSERT INTO core.schema_meta(key,value)
VALUES ('workspace_analysis_tool_refusal_audit','workspace-analysis-tool-refusal-audit-v1')
ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();
