package workflowpostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

// GORMRuntimeBindingReader exposes Workflow-owned durable runtime identity to
// another owner without leaking its tables or taking transaction ownership.
type GORMRuntimeBindingReader struct {
	database *gorm.DB
}

// NewGORMRuntimeBindingReader constructs a reader from the shared PostgreSQL
// pool. The reader itself never opens a transaction.
func NewGORMRuntimeBindingReader(pool *platformpostgres.Pool) (*GORMRuntimeBindingReader, error) {
	if pool == nil {
		return nil, gormWorkflowUnavailable("WORKFLOW_RUNTIME_BINDING_UNAVAILABLE", errors.New("workflow binding pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil || !validGORMWorkflowDatabase(database) {
		return nil, gormWorkflowUnavailable("WORKFLOW_RUNTIME_BINDING_UNAVAILABLE", errors.New("workflow binding database is unavailable"))
	}
	return &GORMRuntimeBindingReader{database: database}, nil
}

// LoadRuntimeBindingScoped reads the complete run/definition/node binding in
// the caller-owned live scope. No commit, rollback, or root fallback occurs.
func (reader *GORMRuntimeBindingReader) LoadRuntimeBindingScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	query application.ScopedRuntimeBindingQuery,
) (application.ScopedRuntimeBindingSnapshot, error) {
	if reader == nil || !validGORMWorkflowDatabase(reader.database) {
		return application.ScopedRuntimeBindingSnapshot{}, gormWorkflowUnavailable("WORKFLOW_RUNTIME_BINDING_UNAVAILABLE", errors.New("workflow binding reader is unavailable"))
	}
	if ctx == nil {
		return application.ScopedRuntimeBindingSnapshot{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_RUNTIME_BINDING_CONTEXT_INVALID", false, errors.New("workflow binding context is nil"))
	}
	if !validRuntimeBindingQuery(query) {
		return application.ScopedRuntimeBindingSnapshot{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_RUNTIME_BINDING_QUERY_INVALID", false, errors.New("workflow binding query is invalid"))
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return application.ScopedRuntimeBindingSnapshot{}, gormWorkflowUnavailable("WORKFLOW_RUNTIME_BINDING_TRANSACTION_UNAVAILABLE", err)
	}

	row, err := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT
		run.input::text,definition.key,definition.version,definition.graph::text,
		node.input::text,node.node_key,node.node_type,node.input_schema_version,node.output_schema_version
		FROM workflow.run AS run
		JOIN workflow.definition AS definition
		  ON definition.id=run.definition_id AND definition.workspace_id=run.workspace_id
		JOIN workflow.node_run AS node ON node.run_id=run.id
		WHERE run.workspace_id=?::uuid AND run.id=?::uuid AND node.id=?::uuid`,
		string(query.WorkspaceID), string(query.WorkflowRunID), string(query.NodeRunID))
	if err != nil {
		return application.ScopedRuntimeBindingSnapshot{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUNTIME_BINDING_QUERY_FAILED")
	}
	var runInput, definitionGraph, nodeInput string
	var snapshot application.ScopedRuntimeBindingSnapshot
	var inputSchemaVersion, outputSchemaVersion sql.NullInt64
	if err := row.Scan(&runInput, &snapshot.DefinitionKey, &snapshot.DefinitionVersion, &definitionGraph,
		&nodeInput, &snapshot.NodeKey, &snapshot.NodeType, &inputSchemaVersion, &outputSchemaVersion); err != nil {
		if gormWorkflowNoRows(err) {
			return application.ScopedRuntimeBindingSnapshot{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_RUNTIME_BINDING_NOT_FOUND", false, err)
		}
		return application.ScopedRuntimeBindingSnapshot{}, classifyGORMWorkflow(ctx, err, "WORKFLOW_RUNTIME_BINDING_QUERY_FAILED")
	}
	if inputSchemaVersion.Valid {
		snapshot.InputSchemaVersion = int(inputSchemaVersion.Int64)
	}
	if outputSchemaVersion.Valid {
		snapshot.OutputSchemaVersion = int(outputSchemaVersion.Int64)
	}
	snapshot.RunInput = cloneRuntimeBindingJSON(runInput)
	snapshot.NodeInput = cloneRuntimeBindingJSON(nodeInput)
	snapshot.DefinitionGraph = cloneRuntimeBindingJSON(definitionGraph)
	if len(snapshot.RunInput) == 0 || len(snapshot.NodeInput) == 0 || len(snapshot.DefinitionGraph) == 0 ||
		!json.Valid(snapshot.RunInput) || !json.Valid(snapshot.NodeInput) || !json.Valid(snapshot.DefinitionGraph) ||
		strings.TrimSpace(snapshot.DefinitionKey) == "" || strings.TrimSpace(snapshot.NodeKey) == "" || strings.TrimSpace(snapshot.NodeType) == "" ||
		snapshot.DefinitionVersion < 1 || snapshot.InputSchemaVersion < 1 || snapshot.OutputSchemaVersion < 1 {
		return application.ScopedRuntimeBindingSnapshot{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_RUNTIME_BINDING_INVALID", false, errors.New("workflow runtime binding contains invalid durable facts"))
	}

	if query.NodeAttemptID != "" {
		attemptRow, attemptErr := gormWorkflowRawRow(transaction.WithContext(ctx), `SELECT 1
			FROM workflow.node_attempt
			WHERE id=?::uuid AND node_run_id=?::uuid`, string(query.NodeAttemptID), string(query.NodeRunID))
		if attemptErr != nil {
			return application.ScopedRuntimeBindingSnapshot{}, classifyGORMWorkflow(ctx, attemptErr, "WORKFLOW_RUNTIME_BINDING_ATTEMPT_QUERY_FAILED")
		}
		var found int
		if scanErr := attemptRow.Scan(&found); scanErr == nil {
			snapshot.AttemptFound = found == 1
		} else if !gormWorkflowNoRows(scanErr) {
			return application.ScopedRuntimeBindingSnapshot{}, classifyGORMWorkflow(ctx, scanErr, "WORKFLOW_RUNTIME_BINDING_ATTEMPT_QUERY_FAILED")
		}
	}
	return snapshot, nil
}

func validRuntimeBindingQuery(query application.ScopedRuntimeBindingQuery) bool {
	return validGORMWorkflowID(query.WorkspaceID) && validGORMWorkflowID(query.WorkflowRunID) &&
		validGORMWorkflowID(query.NodeRunID) && (query.NodeAttemptID == "" || validGORMWorkflowID(query.NodeAttemptID))
}

func cloneRuntimeBindingJSON(value string) json.RawMessage {
	return append(json.RawMessage(nil), []byte(value)...)
}

var _ application.ScopedRuntimeBindingReader = (*GORMRuntimeBindingReader)(nil)
