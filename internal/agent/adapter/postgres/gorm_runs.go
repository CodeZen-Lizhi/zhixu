package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// FinalizeModelRun applies the RUNNING-to-terminal version CAS.
func (repository *GORMRepository) FinalizeModelRun(ctx context.Context, command application.FinalizeModelRunCommand) (domain.ModelRun, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ModelRun{}, false, err
	}
	return finalizeGORMModelRun(ctx, repository.database, command)
}

// GetModelRunScoped reads and optionally locks a run in the caller-owned scope.
func (repository *GORMRepository) GetModelRunScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	runID foundation.ID,
	forUpdate bool,
) (domain.ModelRun, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ModelRun{}, err
	}
	if !validID(workspaceID) || !validID(runID) {
		return domain.ModelRun{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeModelRunInvalid,
			false,
			errors.New("agent scoped model run query is invalid"),
		)
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.ModelRun{}, gormUnavailable(errors.Join(errors.New("agent scoped model run transaction is unavailable"), err))
	}
	run, err := loadGORMModelRunByID(ctx, transaction, workspaceID, runID, forUpdate)
	if gormNoRows(err) {
		return domain.ModelRun{}, notFound()
	}
	if err != nil {
		return domain.ModelRun{}, classifyGORM(ctx, err)
	}
	return run, nil
}

// GetModelRunRecordScoped returns the locked run and stable call history from
// the same caller-owned scope.
func (repository *GORMRepository) GetModelRunRecordScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	runID foundation.ID,
	forUpdate bool,
) (application.ModelRunRecord, error) {
	if err := repository.ready(ctx); err != nil {
		return application.ModelRunRecord{}, err
	}
	if !validID(workspaceID) || !validID(runID) {
		return application.ModelRunRecord{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeModelRunInvalid,
			false,
			errors.New("agent scoped model run record query is invalid"),
		)
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return application.ModelRunRecord{}, gormUnavailable(errors.Join(errors.New("agent scoped model run transaction is unavailable"), err))
	}
	run, err := loadGORMModelRunByID(ctx, transaction, workspaceID, runID, forUpdate)
	if gormNoRows(err) {
		return application.ModelRunRecord{}, notFound()
	}
	if err != nil {
		return application.ModelRunRecord{}, classifyGORM(ctx, err)
	}
	calls, err := loadGORMModelCalls(ctx, transaction, workspaceID, runID)
	if err != nil {
		return application.ModelRunRecord{}, classifyGORM(ctx, err)
	}
	return application.ModelRunRecord{Run: run, Calls: calls}, nil
}

// GetModelRunByAttemptScoped finds the unique run for a Node Attempt without
// taking ownership of the caller transaction.
func (repository *GORMRepository) GetModelRunByAttemptScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	workspaceID foundation.ID,
	attemptID foundation.ID,
	forUpdate bool,
) (domain.ModelRun, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ModelRun{}, false, err
	}
	if !validID(workspaceID) || !validID(attemptID) {
		return domain.ModelRun{}, false, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeModelRunInvalid,
			false,
			errors.New("agent scoped model run attempt query is invalid"),
		)
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.ModelRun{}, false, gormUnavailable(errors.Join(errors.New("agent scoped model run transaction is unavailable"), err))
	}
	query := gormModelRunSelect + ` WHERE workspace_id=? AND node_attempt_id=?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormRawRow(transaction.WithContext(ctx), query, string(workspaceID), string(attemptID))
	if err != nil {
		return domain.ModelRun{}, false, classifyGORM(ctx, err)
	}
	run, err := scanModelRun(row)
	if gormNoRows(err) {
		return domain.ModelRun{}, false, nil
	}
	if err != nil {
		return domain.ModelRun{}, false, classifyGORM(ctx, err)
	}
	return run, true, nil
}

// FinalizeModelRunScoped finalizes a run inside the caller's transaction.
func (repository *GORMRepository) FinalizeModelRunScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command application.FinalizeModelRunCommand,
) (domain.ModelRun, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ModelRun{}, false, err
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return domain.ModelRun{}, false, gormUnavailable(errors.Join(errors.New("agent scoped model run transaction is unavailable"), err))
	}
	return finalizeGORMModelRun(ctx, transaction, command)
}

func finalizeGORMModelRun(ctx context.Context, database *gorm.DB, command application.FinalizeModelRunCommand) (domain.ModelRun, bool, error) {
	run := command.Run
	if command.ExpectedVersion <= 0 || run.Version != command.ExpectedVersion+1 || run.Status == domain.ModelRunRunning ||
		domain.ValidateModelRun(run) != nil {
		return domain.ModelRun{}, false, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeModelRunInvalid,
			false,
			errors.New("model run finalization is invalid"),
		)
	}
	result := database.WithContext(ctx).Exec(`
		UPDATE agent.model_run SET
			retrieval_index_version_id=?,embedding_version_id=?,rerank_model_version=?,
			status=?,final_result_type=?,error_code=?,version=?,updated_at=?,completed_at=?
		WHERE id=? AND workspace_id=? AND status='RUNNING' AND version=?`,
		optionalFoundationID(run.Retrieval.IndexVersionID), optionalID(run.Retrieval.EmbeddingVersionID), optionalText(run.Retrieval.RerankModelVersion),
		string(run.Status), optionalText(run.FinalResultType), optionalText(run.FinalErrorCode), run.Version,
		run.UpdatedAt.UTC(), optionalTime(run.CompletedAt), string(run.ID), string(run.WorkspaceID), command.ExpectedVersion,
	)
	if result.Error != nil {
		return domain.ModelRun{}, false, classifyGORM(ctx, result.Error)
	}
	if result.RowsAffected == 1 {
		return run, false, nil
	}
	existing, err := loadGORMModelRunByID(ctx, database, run.WorkspaceID, run.ID, false)
	if gormNoRows(err) {
		return domain.ModelRun{}, false, notFound()
	}
	if err != nil {
		return domain.ModelRun{}, false, classifyGORM(ctx, err)
	}
	if sameModelRunResult(existing, run) {
		return existing, true, nil
	}
	return domain.ModelRun{}, false, versionConflict()
}

var _ application.ScopedModelRunStore = (*GORMRepository)(nil)
