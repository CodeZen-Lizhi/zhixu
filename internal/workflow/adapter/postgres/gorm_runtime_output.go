package workflowpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const gormRuntimeOutputNodeColumns = `node.id::text,node.run_id::text,node.node_key,node.node_type,node.status,node.attempt,node.input::text,node.output::text,node.idempotency_key,node.input_schema_version,node.output_schema_version,node.dispatch_no,node.lease_owner,node.lease_until,node.version,node.created_at,node.updated_at,node.completed_at`

// GetRunInput 返回一个 Workspace-bound Run 的不可变根输入。
func (repository *GORMRuntimeRepository) GetRunInput(ctx context.Context, workspaceID, runID foundation.ID) (json.RawMessage, error) {
	if err := gormRuntimeOutputDependency(repository, "WORKFLOW_RUN_INPUT_UNAVAILABLE"); err != nil {
		return nil, err
	}
	parsedWorkspace, workspaceErr := foundation.ParseID(string(workspaceID))
	parsedRun, runErr := foundation.ParseID(string(runID))
	if ctx == nil || workspaceErr != nil || parsedWorkspace != workspaceID || runErr != nil || parsedRun != runID {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_RUN_INPUT_QUERY_INVALID", false, errors.New("workflow run input query is invalid"))
	}

	row, err := gormWorkflowRawRow(repository.database.WithContext(ctx), `SELECT input::text
		FROM workflow.run
		WHERE workspace_id=? AND id=?`, string(workspaceID), string(runID))
	if err != nil {
		return nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_INPUT_QUERY_FAILED")
	}
	var input workflowJSONB
	if err := row.Scan(&input); gormWorkflowNoRows(err) {
		return nil, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_RUN_INPUT_NOT_FOUND", false, err)
	} else if err != nil {
		return nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUN_INPUT_QUERY_FAILED")
	}
	if len(input) == 0 || len(input) > maxSucceededNodeOutputBytes || !json.Valid(input) {
		return nil, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_RUN_INPUT_INVALID", false, errors.New("workflow run input is invalid"))
	}
	return append(json.RawMessage(nil), input...), nil
}

// GetSucceededNodeOutput 返回一个成功节点的 Workspace-bound 脱敏输出。
func (repository *GORMRuntimeRepository) GetSucceededNodeOutput(ctx context.Context, workspaceID, runID foundation.ID, nodeKey string) (json.RawMessage, error) {
	if err := gormRuntimeOutputDependency(repository, "WORKFLOW_STAGE_OUTPUT_UNAVAILABLE"); err != nil {
		return nil, err
	}
	parsedWorkspace, workspaceErr := foundation.ParseID(string(workspaceID))
	parsedRun, runErr := foundation.ParseID(string(runID))
	nodeKey = strings.TrimSpace(nodeKey)
	if ctx == nil || workspaceErr != nil || parsedWorkspace != workspaceID || runErr != nil || parsedRun != runID ||
		nodeKey == "" || len(nodeKey) > 128 || strings.ContainsAny(nodeKey, "\r\n\x00") {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_STAGE_OUTPUT_QUERY_INVALID", false, errors.New("workflow stage output query is invalid"))
	}

	row, err := gormWorkflowRawRow(repository.database.WithContext(ctx), `SELECT node.output::text
		FROM workflow.node_run AS node
		JOIN workflow.run AS run ON run.id=node.run_id
		WHERE run.workspace_id=? AND run.id=? AND node.node_key=?
		  AND node.status='succeeded' AND node.output IS NOT NULL`, string(workspaceID), string(runID), nodeKey)
	if err != nil {
		return nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_STAGE_OUTPUT_QUERY_FAILED")
	}
	var output workflowJSONB
	if err := row.Scan(&output); gormWorkflowNoRows(err) {
		return nil, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_STAGE_OUTPUT_NOT_FOUND", false, err)
	} else if err != nil {
		return nil, classifyGORMWorkflow(ctx, err, "WORKFLOW_STAGE_OUTPUT_QUERY_FAILED")
	}
	if len(output) == 0 || len(output) > maxSucceededNodeOutputBytes || !json.Valid(output) {
		return nil, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_STAGE_OUTPUT_INVALID", false, errors.New("workflow stage output is invalid"))
	}
	var object map[string]any
	if json.Unmarshal(output, &object) != nil || object == nil {
		return nil, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_STAGE_OUTPUT_INVALID", false, errors.New("workflow stage output is not an object"))
	}
	return append(json.RawMessage(nil), output...), nil
}

// GetPendingHumanTaskNode 返回一个 pending Human Task 精确绑定的 waiting 节点。
func (repository *GORMRuntimeRepository) GetPendingHumanTaskNode(ctx context.Context, workspaceID, runID, taskID, nodeRunID foundation.ID) (domain.NodeRun, error) {
	if err := gormRuntimeOutputDependency(repository, "WORKFLOW_HUMAN_TASK_NODE_UNAVAILABLE"); err != nil {
		return domain.NodeRun{}, err
	}
	if ctx == nil || !validRuntimeOutputID(workspaceID) || !validRuntimeOutputID(runID) ||
		!validRuntimeOutputID(taskID) || !validRuntimeOutputID(nodeRunID) {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_HUMAN_TASK_NODE_QUERY_INVALID", false, errors.New("workflow human task node query is invalid"))
	}

	row, err := gormWorkflowRawRow(repository.database.WithContext(ctx), `SELECT `+gormRuntimeOutputNodeColumns+`
		FROM workflow.node_run AS node
		JOIN workflow.run AS run ON run.id=node.run_id
		JOIN workflow.human_task AS task ON task.run_id=run.id AND task.node_run_id=node.id
		WHERE run.workspace_id=? AND run.id=? AND task.id=? AND node.id=?
		  AND task.status='pending' AND node.status='waiting_for_human'`, string(workspaceID), string(runID), string(taskID), string(nodeRunID))
	if err != nil {
		return domain.NodeRun{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_HUMAN_TASK_NODE_QUERY_FAILED")
	}
	node, err := scanGORMStartNode(row)
	if gormWorkflowNoRows(err) {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_HUMAN_TASK_NODE_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.NodeRun{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_HUMAN_TASK_NODE_QUERY_FAILED")
	}
	if node.ID != nodeRunID || node.RunID != runID || node.Status != domain.NodeStatusWaitingForHuman {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_HUMAN_TASK_NODE_INVALID", false, errors.New("workflow human task node binding is invalid"))
	}
	return node, nil
}

// GetRunDefinition 返回一个 Workspace-bound Run 使用的不可变 Definition。
func (repository *GORMRuntimeRepository) GetRunDefinition(ctx context.Context, workspaceID, runID foundation.ID) (domain.Definition, error) {
	if err := gormRuntimeOutputDependency(repository, "WORKFLOW_DEFINITION_QUERY_UNAVAILABLE"); err != nil {
		return domain.Definition{}, err
	}
	if ctx == nil || !validRuntimeOutputID(workspaceID) || !validRuntimeOutputID(runID) {
		return domain.Definition{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_DEFINITION_QUERY_INVALID", false, errors.New("workflow definition query is invalid"))
	}

	row, err := gormWorkflowRawRow(repository.database.WithContext(ctx), `SELECT definition.id::text,definition.workspace_id::text,
		definition.key,definition.version,definition.graph::text,definition.created_at
		FROM workflow.run AS run
		JOIN workflow.definition AS definition
		  ON definition.id=run.definition_id AND definition.workspace_id=run.workspace_id
		WHERE run.workspace_id=? AND run.id=?`, string(workspaceID), string(runID))
	if err != nil {
		return domain.Definition{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	definition, err := scanGORMRuntimeOutputDefinition(row)
	if gormWorkflowNoRows(err) {
		return domain.Definition{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_DEFINITION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Definition{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	if definition.WorkspaceID != workspaceID || !validRuntimeOutputID(definition.ID) || strings.TrimSpace(definition.Key) == "" || definition.Version < 1 {
		return domain.Definition{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_BINDING_INVALID", false, errors.New("workflow definition binding is invalid"))
	}
	return definition, nil
}

func scanGORMRuntimeOutputDefinition(row workflowRowScanner) (domain.Definition, error) {
	var definition domain.Definition
	var id, workspaceID string
	var graphJSON workflowJSONB
	if err := row.Scan(&id, &workspaceID, &definition.Key, &definition.Version, &graphJSON, &definition.CreatedAt); err != nil {
		return domain.Definition{}, err
	}
	graph, err := application.DecodeCanonicalGraph(graphJSON)
	if err != nil {
		return domain.Definition{}, err
	}
	canonicalGraph, err := json.Marshal(graph)
	if err != nil {
		return domain.Definition{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_GRAPH_INVALID", false, err)
	}
	graphHash, err := application.ComputeCanonicalGraphHash(graph)
	if err != nil {
		return domain.Definition{}, err
	}
	definition.ID = foundation.ID(id)
	definition.WorkspaceID = foundation.ID(workspaceID)
	definition.Graph = canonicalGraph
	definition.GraphHash = graphHash
	return definition, nil
}

func gormRuntimeOutputDependency(repository *GORMRuntimeRepository, code string) error {
	if repository == nil || !validGORMWorkflowDatabase(repository.database) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, errors.New("workflow runtime repository is unavailable"))
	}
	return nil
}
