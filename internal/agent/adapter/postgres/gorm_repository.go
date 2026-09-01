package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

// CreateModelRun persists the RUNNING fact before the first provider call.
func (repository *GORMRepository) CreateModelRun(ctx context.Context, run domain.ModelRun) (domain.ModelRun, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.ModelRun{}, false, err
	}
	if err := domain.ValidateModelRun(run); err != nil || run.Status != domain.ModelRunRunning || run.Version != 1 {
		if err != nil {
			return domain.ModelRun{}, false, err
		}
		return domain.ModelRun{}, false, consistency(errors.New("new model run must be running at version one"))
	}
	result := repository.database.WithContext(ctx).Exec(gormInsertModelRunSQL, modelRunArgs(run)...)
	if result.Error != nil {
		return domain.ModelRun{}, false, classifyGORM(ctx, result.Error)
	}
	if result.RowsAffected == 1 {
		return run, false, nil
	}
	existing, err := loadGORMModelRunByAttempt(ctx, repository.database, run.NodeAttemptID, false)
	if gormNoRows(err) {
		return domain.ModelRun{}, false, replayConflict()
	}
	if err != nil {
		return domain.ModelRun{}, false, classifyGORM(ctx, err)
	}
	if !sameModelRunCreateBinding(existing, run) {
		return domain.ModelRun{}, false, replayConflict()
	}
	return existing, true, nil
}

// GetModelRun returns one Workspace-scoped run and its stably ordered calls.
func (repository *GORMRepository) GetModelRun(ctx context.Context, workspaceID, runID foundation.ID) (application.ModelRunRecord, error) {
	if err := repository.ready(ctx); err != nil {
		return application.ModelRunRecord{}, err
	}
	if !validID(workspaceID) || !validID(runID) {
		return application.ModelRunRecord{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeModelRunInvalid,
			false,
			errors.New("model run query identity is invalid"),
		)
	}
	run, err := loadGORMModelRunByID(ctx, repository.database, workspaceID, runID, false)
	if gormNoRows(err) {
		return application.ModelRunRecord{}, notFound()
	}
	if err != nil {
		return application.ModelRunRecord{}, classifyGORM(ctx, err)
	}
	calls, err := loadGORMModelCalls(ctx, repository.database, workspaceID, runID)
	if err != nil {
		return application.ModelRunRecord{}, classifyGORM(ctx, err)
	}
	return application.ModelRunRecord{Run: run, Calls: calls}, nil
}

func loadGORMModelRunByAttempt(ctx context.Context, database *gorm.DB, attemptID foundation.ID, forUpdate bool) (domain.ModelRun, error) {
	query := gormModelRunSelect + ` WHERE node_attempt_id=?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormRawRow(database.WithContext(ctx), query, string(attemptID))
	if err != nil {
		return domain.ModelRun{}, err
	}
	return scanModelRun(row)
}

func loadGORMModelRunByID(ctx context.Context, database *gorm.DB, workspaceID, runID foundation.ID, forUpdate bool) (domain.ModelRun, error) {
	query := gormModelRunSelect + ` WHERE workspace_id=? AND id=?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormRawRow(database.WithContext(ctx), query, string(workspaceID), string(runID))
	if err != nil {
		return domain.ModelRun{}, err
	}
	return scanModelRun(row)
}

func loadGORMModelCalls(ctx context.Context, database *gorm.DB, workspaceID, runID foundation.ID) (result []domain.ModelCall, err error) {
	rows, err := gormRawRows(database.WithContext(ctx), gormModelCallSelect+`
		JOIN agent.model_run run ON run.id=call.model_run_id
		WHERE run.workspace_id=? AND call.model_run_id=?
		ORDER BY call.call_no,call.id`, string(workspaceID), string(runID))
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	result = make([]domain.ModelCall, 0)
	previous := 0
	for rows.Next() {
		call, scanErr := scanModelCall(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if call.CallNo <= previous {
			return nil, consistency(errors.New("model calls are not stably ordered"))
		}
		previous = call.CallNo
		result = append(result, call)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return result, nil
}

var _ application.ModelRunRepository = (*GORMRepository)(nil)
