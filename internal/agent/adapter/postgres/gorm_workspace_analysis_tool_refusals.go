package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

const gormWorkspaceAnalysisToolRefusalColumns = `
	id::text,workspace_id::text,analysis_run_id::text,workflow_run_id::text,node_run_id::text,
	node_key,operation_kind,ordinal,audit_event_id::text,error_code,created_at`

// RecordWorkspaceAnalysisToolRefusalScoped records one refusal in the caller-owned transaction.
func (repository *GORMRepository) RecordWorkspaceAnalysisToolRefusalScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command application.RecordWorkspaceAnalysisToolRefusalScopedCommand,
) (application.WorkspaceAnalysisToolRefusal, bool, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis tool refusal context is nil"),
		)
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}

	run, found, err := loadGORMWorkspaceAnalysisToolRefusalRunForUpdate(
		ctx, database, command.Identity.WorkspaceID, command.Identity.WorkflowRunID, command.OperationKey.AnalysisRunID,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}
	if !found {
		return application.WorkspaceAnalysisToolRefusal{}, false, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool refusal run was not found"),
		)
	}
	if err := validateGORMWorkspaceAnalysisToolRefusalRunBinding(run, command.Identity, command.OperationKey); err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}

	persisted, found, err := loadGORMWorkspaceAnalysisToolRefusalForUpdate(
		ctx, database, command.Identity.WorkspaceID, command.Identity.WorkflowRunID, command.OperationKey,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}
	operationExists, err := gormWorkspaceAnalysisToolRefusalOperationExists(
		ctx, database, command.Identity.WorkspaceID, command.Identity.WorkflowRunID, command.OperationKey,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}
	if operationExists {
		if found {
			return application.WorkspaceAnalysisToolRefusal{}, false, consistency(
				errors.New("workspace analysis tool refusal and operation coexist"),
			)
		}
		return application.WorkspaceAnalysisToolRefusal{}, false, replayConflict()
	}
	if found {
		if !sameGORMWorkspaceAnalysisToolRefusalCommand(persisted, command) {
			return application.WorkspaceAnalysisToolRefusal{}, false, replayConflict()
		}
		return persisted, true, nil
	}

	databaseNow, err := loadGORMWorkspaceAnalysisToolRefusalDatabaseTime(ctx, database)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}
	if run.Status != domain.WorkspaceAnalysisRunRunning || !run.DeadlineAt.After(databaseNow) {
		return application.WorkspaceAnalysisToolRefusal{}, false, workspaceAnalysisModelConflict(
			errors.New("workspace analysis tool refusal run is no longer active"),
		)
	}

	row, err := gormRawRow(database, `INSERT INTO agent.workspace_analysis_tool_refusal(
		id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,ordinal,
		error_code,audit_event_id,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,clock_timestamp()) RETURNING `+gormWorkspaceAnalysisToolRefusalColumns,
		string(command.RefusalID), string(command.Identity.WorkspaceID), string(command.OperationKey.AnalysisRunID),
		string(command.Identity.WorkflowRunID), string(command.Identity.NodeRunID), string(command.OperationKey.NodeKey),
		string(command.OperationKey.Kind), command.OperationKey.Ordinal, command.ErrorCode, string(command.AuditEventID),
	)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, classifyGORM(ctx, err)
	}
	created, err := scanGORMWorkspaceAnalysisToolRefusal(row)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, classifyGORM(ctx, err)
	}
	if !sameGORMWorkspaceAnalysisToolRefusalCommand(created, command) {
		return application.WorkspaceAnalysisToolRefusal{}, false, consistency(
			errors.New("created workspace analysis tool refusal binding differs"),
		)
	}
	return created, false, nil
}

// LoadWorkspaceAnalysisToolRefusalExactScoped loads a durable exact refusal without live admission checks.
func (repository *GORMRepository) LoadWorkspaceAnalysisToolRefusalExactScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	query application.WorkspaceAnalysisToolRefusalQuery,
) (application.WorkspaceAnalysisToolRefusal, bool, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, workspaceAnalysisModelInvalid(
			errors.New("workspace analysis tool refusal query context is nil"),
		)
	}
	if err := query.Validate(); err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}
	database, err := gormWorkspaceAnalysisToolTransaction(repository, ctx, scope)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}

	run, found, err := loadGORMWorkspaceAnalysisToolRefusalRunForUpdate(
		ctx, database, query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}
	if !found {
		return application.WorkspaceAnalysisToolRefusal{}, false, nil
	}
	if run.ID != query.AnalysisRunID || run.WorkspaceID != query.WorkspaceID || run.WorkflowRunID != query.WorkflowRunID {
		return application.WorkspaceAnalysisToolRefusal{}, false, consistency(
			errors.New("workspace analysis tool refusal run binding differs"),
		)
	}

	persisted, found, err := loadGORMWorkspaceAnalysisToolRefusalForUpdate(
		ctx, database, query.WorkspaceID, query.WorkflowRunID, query.OperationKey,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}
	operationExists, err := gormWorkspaceAnalysisToolRefusalOperationExists(
		ctx, database, query.WorkspaceID, query.WorkflowRunID, query.OperationKey,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, err
	}
	if operationExists {
		return application.WorkspaceAnalysisToolRefusal{}, false, consistency(
			errors.New("workspace analysis tool refusal and operation coexist"),
		)
	}
	if !found {
		return application.WorkspaceAnalysisToolRefusal{}, false, nil
	}
	if !sameGORMWorkspaceAnalysisToolRefusalQuery(persisted, query) {
		return application.WorkspaceAnalysisToolRefusal{}, false, consistency(
			errors.New("workspace analysis tool refusal exact binding differs"),
		)
	}
	return persisted, true, nil
}

func loadGORMWorkspaceAnalysisToolRefusalRunForUpdate(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	analysisRunID foundation.ID,
) (domain.WorkspaceAnalysisRun, bool, error) {
	row, err := gormRawRow(database, `SELECT `+workspaceAnalysisRunColumns+`
		FROM agent.workspace_analysis_run
		WHERE id=? AND workspace_id=? AND workflow_run_id=? FOR UPDATE`,
		string(analysisRunID), string(workspaceID), string(workflowRunID),
	)
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, false, classifyGORM(ctx, err)
	}
	run, err := scanWorkspaceAnalysisRun(row)
	if gormNoRows(err) {
		return domain.WorkspaceAnalysisRun{}, false, nil
	}
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, false, classifyGORM(ctx, err)
	}
	return run, true, nil
}

func validateGORMWorkspaceAnalysisToolRefusalRunBinding(
	run domain.WorkspaceAnalysisRun,
	identity application.WorkspaceAnalysisToolExecutionIdentity,
	key domain.WorkspaceAnalysisOperationKey,
) error {
	if run.ID != key.AnalysisRunID || run.WorkspaceID != identity.WorkspaceID ||
		run.WorkflowRunID != identity.WorkflowRunID || run.DefinitionVersion != identity.DefinitionVersion ||
		run.DefinitionHash != identity.DefinitionHash {
		return workspaceAnalysisModelConflict(errors.New("workspace analysis tool refusal run binding differs"))
	}
	return nil
}

func loadGORMWorkspaceAnalysisToolRefusalForUpdate(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	key domain.WorkspaceAnalysisOperationKey,
) (application.WorkspaceAnalysisToolRefusal, bool, error) {
	row, err := gormRawRow(database, `SELECT `+gormWorkspaceAnalysisToolRefusalColumns+`
		FROM agent.workspace_analysis_tool_refusal
		WHERE workspace_id=? AND workflow_run_id=? AND analysis_run_id=?
		  AND node_key=? AND operation_kind=? AND ordinal=? FOR UPDATE`,
		string(workspaceID), string(workflowRunID), string(key.AnalysisRunID),
		string(key.NodeKey), string(key.Kind), key.Ordinal,
	)
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, classifyGORM(ctx, err)
	}
	refusal, err := scanGORMWorkspaceAnalysisToolRefusal(row)
	if gormNoRows(err) {
		return application.WorkspaceAnalysisToolRefusal{}, false, nil
	}
	if err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, false, classifyGORM(ctx, err)
	}
	return refusal, true, nil
}

func scanGORMWorkspaceAnalysisToolRefusal(row rowScanner) (application.WorkspaceAnalysisToolRefusal, error) {
	var refusal application.WorkspaceAnalysisToolRefusal
	var id, workspaceID, analysisRunID, workflowRunID, nodeRunID, nodeKey, operationKind, auditEventID string
	var ordinal int
	if err := row.Scan(
		&id, &workspaceID, &analysisRunID, &workflowRunID, &nodeRunID,
		&nodeKey, &operationKind, &ordinal, &auditEventID, &refusal.ErrorCode, &refusal.CreatedAt,
	); err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, err
	}
	refusal.ID, refusal.WorkspaceID = foundation.ID(id), foundation.ID(workspaceID)
	refusal.AnalysisRunID, refusal.WorkflowRunID = foundation.ID(analysisRunID), foundation.ID(workflowRunID)
	refusal.NodeRunID, refusal.AuditEventID = foundation.ID(nodeRunID), foundation.ID(auditEventID)
	refusal.OperationKey = domain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: refusal.AnalysisRunID,
		NodeKey:       domain.WorkspaceAnalysisOperationNodeKey(nodeKey),
		Kind:          domain.WorkspaceAnalysisOperationKind(operationKind),
		Ordinal:       ordinal,
	}
	refusal.CreatedAt = refusal.CreatedAt.UTC().Truncate(time.Microsecond)
	if err := refusal.Validate(); err != nil {
		return application.WorkspaceAnalysisToolRefusal{}, consistency(err)
	}
	return refusal, nil
}

func gormWorkspaceAnalysisToolRefusalOperationExists(
	ctx context.Context,
	database *gorm.DB,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
	key domain.WorkspaceAnalysisOperationKey,
) (bool, error) {
	row, err := gormRawRow(database, `SELECT id::text
		FROM agent.workspace_analysis_operation
		WHERE workspace_id=? AND workflow_run_id=? AND analysis_run_id=?
		  AND node_key=? AND operation_kind=? AND ordinal=? FOR UPDATE`,
		string(workspaceID), string(workflowRunID), string(key.AnalysisRunID),
		string(key.NodeKey), string(key.Kind), key.Ordinal,
	)
	if err != nil {
		return false, classifyGORM(ctx, err)
	}
	var operationID string
	err = row.Scan(&operationID)
	if gormNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, classifyGORM(ctx, err)
	}
	return true, nil
}

func loadGORMWorkspaceAnalysisToolRefusalDatabaseTime(ctx context.Context, database *gorm.DB) (time.Time, error) {
	row, err := gormRawRow(database, `SELECT clock_timestamp()`)
	if err != nil {
		return time.Time{}, classifyGORM(ctx, err)
	}
	var databaseNow time.Time
	if err := row.Scan(&databaseNow); err != nil {
		return time.Time{}, classifyGORM(ctx, err)
	}
	return databaseNow.UTC().Truncate(time.Microsecond), nil
}

func sameGORMWorkspaceAnalysisToolRefusalCommand(
	refusal application.WorkspaceAnalysisToolRefusal,
	command application.RecordWorkspaceAnalysisToolRefusalScopedCommand,
) bool {
	return refusal.ID == command.RefusalID && refusal.WorkspaceID == command.Identity.WorkspaceID &&
		refusal.AnalysisRunID == command.OperationKey.AnalysisRunID && refusal.WorkflowRunID == command.Identity.WorkflowRunID &&
		refusal.NodeRunID == command.Identity.NodeRunID && refusal.OperationKey == command.OperationKey &&
		refusal.AuditEventID == command.AuditEventID && refusal.ErrorCode == command.ErrorCode
}

func sameGORMWorkspaceAnalysisToolRefusalQuery(
	refusal application.WorkspaceAnalysisToolRefusal,
	query application.WorkspaceAnalysisToolRefusalQuery,
) bool {
	return refusal.ID == query.RefusalID && refusal.WorkspaceID == query.WorkspaceID &&
		refusal.AnalysisRunID == query.AnalysisRunID && refusal.WorkflowRunID == query.WorkflowRunID &&
		refusal.NodeRunID == query.NodeRunID && refusal.OperationKey == query.OperationKey &&
		refusal.AuditEventID == query.AuditEventID && refusal.ErrorCode == query.ErrorCode
}

var _ application.ScopedWorkspaceAnalysisToolRefusalStore = (*GORMRepository)(nil)
