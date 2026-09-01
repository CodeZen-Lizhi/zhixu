package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

// StartModelCall persists STARTED before invoking the provider.
func (repository *GORMRepository) StartModelCall(ctx context.Context, workspaceID foundation.ID, call domain.ModelCall) (domain.ModelCall, bool, error) {
	if err := repository.readyModelCall(ctx); err != nil {
		return domain.ModelCall{}, false, err
	}
	if !validID(workspaceID) {
		return domain.ModelCall{}, false, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeModelCallInvalid,
			false,
			errors.New("model call workspace is invalid"),
		)
	}
	if err := domain.ValidateModelCall(call); err != nil || call.Status != domain.ModelCallStarted || call.Version != 1 {
		if err != nil {
			return domain.ModelCall{}, false, err
		}
		return domain.ModelCall{}, false, consistency(errors.New("new model call must be started at version one"))
	}
	result := repository.database.WithContext(ctx).Exec(
		gormInsertModelCallSQL,
		string(call.ID), string(call.ModelRunID), call.CallNo, string(call.Phase),
		call.Model.AdapterName, call.Model.AdapterVersion, call.Model.ModelID, call.Model.ModelVersion,
		call.Profile.ID, call.Profile.Version, call.Prompt.ID, call.Prompt.Version,
		call.Schema.ID, call.Schema.Version, call.MaxOutputTokens,
		call.RequestHash, call.RequestBytes, string(call.Status), call.Version, call.StartedAt.UTC(),
		string(call.ModelRunID), string(workspaceID),
	)
	if result.Error != nil {
		return domain.ModelCall{}, false, classifyGORM(ctx, result.Error)
	}
	if result.RowsAffected == 1 {
		return call, false, nil
	}
	existing, err := loadGORMModelCallByNumber(ctx, repository.database, workspaceID, call.ModelRunID, call.CallNo)
	if gormNoRows(err) {
		return domain.ModelCall{}, false, notFound()
	}
	if err != nil {
		return domain.ModelCall{}, false, classifyGORM(ctx, err)
	}
	if !sameModelCallBinding(existing, call) {
		return domain.ModelCall{}, false, replayConflict()
	}
	return existing, true, nil
}

// CompleteModelCall applies the STARTED-to-terminal version CAS.
func (repository *GORMRepository) CompleteModelCall(ctx context.Context, command application.CompleteModelCallCommand) (domain.ModelCall, bool, error) {
	if err := repository.readyModelCall(ctx); err != nil {
		return domain.ModelCall{}, false, err
	}
	call := command.Call
	if !validID(command.WorkspaceID) || command.ExpectedVersion <= 0 || call.Version != command.ExpectedVersion+1 ||
		call.Status == domain.ModelCallStarted || domain.ValidateModelCall(call) != nil {
		return domain.ModelCall{}, false, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeModelCallInvalid,
			false,
			errors.New("model call completion is invalid"),
		)
	}
	result := repository.database.WithContext(ctx).Exec(`
		UPDATE agent.model_call call SET
			response_hash=?,response_bytes=?,input_tokens=?,output_tokens=?,latency_ms=?,
			status=?,error_code=?,version=?,completed_at=?
		FROM agent.model_run run
		WHERE call.id=? AND call.model_run_id=? AND call.model_run_id=run.id
		  AND run.workspace_id=? AND call.status='STARTED' AND call.version=?`,
		optionalText(call.ResponseHash), call.ResponseBytes, call.Usage.InputTokens, call.Usage.OutputTokens,
		call.LatencyMillis, string(call.Status), optionalText(call.ErrorCode), call.Version, optionalTime(call.CompletedAt),
		string(call.ID), string(call.ModelRunID), string(command.WorkspaceID), command.ExpectedVersion,
	)
	if result.Error != nil {
		return domain.ModelCall{}, false, classifyGORM(ctx, result.Error)
	}
	if result.RowsAffected == 1 {
		return call, false, nil
	}
	existing, err := loadGORMModelCallByID(ctx, repository.database, command.WorkspaceID, call.ID)
	if gormNoRows(err) {
		return domain.ModelCall{}, false, notFound()
	}
	if err != nil {
		return domain.ModelCall{}, false, classifyGORM(ctx, err)
	}
	if sameModelCallResult(existing, call) {
		return existing, true, nil
	}
	return domain.ModelCall{}, false, versionConflict()
}

func loadGORMModelCallByNumber(ctx context.Context, database *gorm.DB, workspaceID, runID foundation.ID, callNo int) (domain.ModelCall, error) {
	row, err := gormRawRow(database.WithContext(ctx), gormModelCallSelect+`
		JOIN agent.model_run run ON run.id=call.model_run_id
		WHERE run.workspace_id=? AND call.model_run_id=? AND call.call_no=?`,
		string(workspaceID), string(runID), callNo,
	)
	if err != nil {
		return domain.ModelCall{}, err
	}
	return scanModelCall(row)
}

func loadGORMModelCallByID(ctx context.Context, database *gorm.DB, workspaceID, callID foundation.ID) (domain.ModelCall, error) {
	row, err := gormRawRow(database.WithContext(ctx), gormModelCallSelect+`
		JOIN agent.model_run run ON run.id=call.model_run_id
		WHERE run.workspace_id=? AND call.id=?`,
		string(workspaceID), string(callID),
	)
	if err != nil {
		return domain.ModelCall{}, err
	}
	return scanModelCall(row)
}
