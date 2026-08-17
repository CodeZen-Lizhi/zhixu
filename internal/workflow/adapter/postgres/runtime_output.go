package workflowpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
)

// A 24-section Organizing outline can carry 32 immutable Evidence tuples per
// section. Keep the owner read bounded without rejecting that valid contract.
const maxSucceededNodeOutputBytes = 512 * 1024

// GetRunInput returns the immutable root input for one Workspace-bound Run.
// Successor nodes use it to recover server-owned root identities without
// copying those identities into every stage output.
func (r *RuntimeRepository) GetRunInput(
	ctx context.Context,
	workspaceID, runID foundation.ID,
) (json.RawMessage, error) {
	parsedWorkspace, workspaceErr := foundation.ParseID(string(workspaceID))
	parsedRun, runErr := foundation.ParseID(string(runID))
	if r == nil || isNilDB(r.db) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RUN_INPUT_UNAVAILABLE", true, errors.New("workflow runtime repository is unavailable"))
	}
	if ctx == nil || workspaceErr != nil || parsedWorkspace != workspaceID || runErr != nil || parsedRun != runID {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_RUN_INPUT_QUERY_INVALID", false, errors.New("workflow run input query is invalid"))
	}
	var input []byte
	err := r.db.QueryRow(ctx, `SELECT input
		FROM workflow.run
		WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(runID)).Scan(&input)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_RUN_INPUT_NOT_FOUND", false, err)
	}
	if err != nil {
		return nil, classify(err, "WORKFLOW_RUN_INPUT_QUERY_FAILED")
	}
	if len(input) == 0 || len(input) > maxSucceededNodeOutputBytes || !json.Valid(input) {
		return nil, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_RUN_INPUT_INVALID", false, errors.New("workflow run input is invalid"))
	}
	return append(json.RawMessage(nil), input...), nil
}

// GetSucceededNodeOutput returns one redacted stage receipt through the
// Workflow owner. It never exposes node inputs, attempts, or private tables to
// another module's adapter.
func (r *RuntimeRepository) GetSucceededNodeOutput(ctx context.Context, workspaceID, runID foundation.ID, nodeKey string) (json.RawMessage, error) {
	parsedWorkspace, workspaceErr := foundation.ParseID(string(workspaceID))
	parsedRun, runErr := foundation.ParseID(string(runID))
	nodeKey = strings.TrimSpace(nodeKey)
	if r == nil || isNilDB(r.db) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_STAGE_OUTPUT_UNAVAILABLE", true, errors.New("workflow runtime repository is unavailable"))
	}
	if ctx == nil || workspaceErr != nil || parsedWorkspace != workspaceID || runErr != nil || parsedRun != runID ||
		nodeKey == "" || len(nodeKey) > 128 || strings.ContainsAny(nodeKey, "\r\n\x00") {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_STAGE_OUTPUT_QUERY_INVALID", false, errors.New("workflow stage output query is invalid"))
	}
	var output []byte
	err := r.db.QueryRow(ctx, `SELECT node.output
		FROM workflow.node_run AS node
		JOIN workflow.run AS run ON run.id=node.run_id
		WHERE run.workspace_id=$1 AND run.id=$2 AND node.node_key=$3
		  AND node.status='succeeded' AND node.output IS NOT NULL`,
		string(workspaceID), string(runID), nodeKey).Scan(&output)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_STAGE_OUTPUT_NOT_FOUND", false, err)
	}
	if err != nil {
		return nil, classify(err, "WORKFLOW_STAGE_OUTPUT_QUERY_FAILED")
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

// GetPendingHumanTaskNode returns the exact waiting node for one pending Human
// Task. The composite lookup prevents another module from trusting a task or
// node identity that is not bound to the requested Workspace and Run.
func (r *RuntimeRepository) GetPendingHumanTaskNode(
	ctx context.Context,
	workspaceID, runID, taskID, nodeRunID foundation.ID,
) (domain.NodeRun, error) {
	if r == nil || isNilDB(r.db) {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_HUMAN_TASK_NODE_UNAVAILABLE", true, errors.New("workflow runtime repository is unavailable"))
	}
	if ctx == nil || !validRuntimeOutputID(workspaceID) || !validRuntimeOutputID(runID) ||
		!validRuntimeOutputID(taskID) || !validRuntimeOutputID(nodeRunID) {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_HUMAN_TASK_NODE_QUERY_INVALID", false, errors.New("workflow human task node query is invalid"))
	}
	node, err := scanNode(r.db.QueryRow(ctx, `SELECT `+nodeColumns+`
		FROM workflow.node_run AS node
		JOIN workflow.run AS run ON run.id=node.run_id
		JOIN workflow.human_task AS task ON task.run_id=run.id AND task.node_run_id=node.id
		WHERE run.workspace_id=$1 AND run.id=$2 AND task.id=$3 AND node.id=$4
		  AND task.status='pending' AND node.status='waiting_for_human'`,
		string(workspaceID), string(runID), string(taskID), string(nodeRunID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_HUMAN_TASK_NODE_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_HUMAN_TASK_NODE_QUERY_FAILED")
	}
	return node, nil
}

// GetRunDefinition returns the immutable Definition identity owned by one Run.
// The Graph is loaded from the same row but remains an opaque Workflow fact to
// callers that only inspect Key and Version.
func (r *RuntimeRepository) GetRunDefinition(ctx context.Context, workspaceID, runID foundation.ID) (domain.Definition, error) {
	if r == nil || isNilDB(r.db) {
		return domain.Definition{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_DEFINITION_QUERY_UNAVAILABLE", true, errors.New("workflow runtime repository is unavailable"))
	}
	if ctx == nil || !validRuntimeOutputID(workspaceID) || !validRuntimeOutputID(runID) {
		return domain.Definition{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_DEFINITION_QUERY_INVALID", false, errors.New("workflow definition query is invalid"))
	}
	definition, err := scanClaimDefinition(r.db.QueryRow(ctx, `SELECT definition.id::text,definition.workspace_id::text,
		definition.key,definition.version,definition.graph,definition.created_at
		FROM workflow.run AS run
		JOIN workflow.definition AS definition
		  ON definition.id=run.definition_id AND definition.workspace_id=run.workspace_id
		WHERE run.workspace_id=$1 AND run.id=$2`, string(workspaceID), string(runID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Definition{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_DEFINITION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Definition{}, classify(err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	return definition, nil
}

func validRuntimeOutputID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
