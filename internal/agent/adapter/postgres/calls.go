package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// StartModelCall 在 Provider 请求前幂等创建 STARTED Model Call。
func (r *Repository) StartModelCall(ctx context.Context, workspaceID foundation.ID, call domain.ModelCall) (domain.ModelCall, bool, error) {
	if !validID(workspaceID) {
		return domain.ModelCall{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeModelCallInvalid, false, errors.New("model call workspace is invalid"))
	}
	if err := domain.ValidateModelCall(call); err != nil || call.Status != domain.ModelCallStarted || call.Version != 1 {
		if err != nil {
			return domain.ModelCall{}, false, err
		}
		return domain.ModelCall{}, false, consistency(errors.New("new model call must be started at version one"))
	}
	tag, err := r.db.Exec(ctx, `
		INSERT INTO agent.model_call(
			id,model_run_id,call_no,phase,
			adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
			prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
			request_hash,response_hash,request_bytes,response_bytes,
			input_tokens,output_tokens,latency_ms,status,error_code,version,started_at,completed_at
		)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
			$16,NULL,$17,0,0,0,0,$18,NULL,$19,$20,NULL
		FROM agent.model_run run
		WHERE run.id=$2 AND run.workspace_id=$21
		ON CONFLICT DO NOTHING`,
		string(call.ID), string(call.ModelRunID), call.CallNo, string(call.Phase),
		call.Model.AdapterName, call.Model.AdapterVersion, call.Model.ModelID, call.Model.ModelVersion,
		call.Profile.ID, call.Profile.Version, call.Prompt.ID, call.Prompt.Version, call.Schema.ID, call.Schema.Version,
		call.MaxOutputTokens, call.RequestHash, call.RequestBytes, string(call.Status), call.Version,
		call.StartedAt.UTC(), string(workspaceID),
	)
	if err != nil {
		return domain.ModelCall{}, false, classify(err)
	}
	if tag.RowsAffected() == 1 {
		return call, false, nil
	}
	existing, err := loadModelCallByNumber(ctx, r.db, workspaceID, call.ModelRunID, call.CallNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModelCall{}, false, notFound()
	}
	if err != nil {
		return domain.ModelCall{}, false, classify(err)
	}
	if !sameModelCallBinding(existing, call) {
		return domain.ModelCall{}, false, replayConflict()
	}
	return existing, true, nil
}

// CompleteModelCall 使用 expected_version 把 STARTED Call 归约为唯一终态。
func (r *Repository) CompleteModelCall(ctx context.Context, command application.CompleteModelCallCommand) (domain.ModelCall, bool, error) {
	call := command.Call
	if !validID(command.WorkspaceID) || command.ExpectedVersion <= 0 || call.Version != command.ExpectedVersion+1 ||
		call.Status == domain.ModelCallStarted || domain.ValidateModelCall(call) != nil {
		return domain.ModelCall{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeModelCallInvalid, false, errors.New("model call completion is invalid"))
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE agent.model_call call SET
			response_hash=$1,response_bytes=$2,input_tokens=$3,output_tokens=$4,latency_ms=$5,
			status=$6,error_code=$7,version=$8,completed_at=$9
		FROM agent.model_run run
		WHERE call.id=$10 AND call.model_run_id=$11 AND call.model_run_id=run.id
		  AND run.workspace_id=$12 AND call.status='STARTED' AND call.version=$13`,
		optionalText(call.ResponseHash), call.ResponseBytes, call.Usage.InputTokens, call.Usage.OutputTokens,
		call.LatencyMillis, string(call.Status), optionalText(call.ErrorCode), call.Version, optionalTime(call.CompletedAt),
		string(call.ID), string(call.ModelRunID), string(command.WorkspaceID), command.ExpectedVersion,
	)
	if err != nil {
		return domain.ModelCall{}, false, classify(err)
	}
	if tag.RowsAffected() == 1 {
		return call, false, nil
	}
	existing, err := loadModelCallByID(ctx, r.db, command.WorkspaceID, call.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModelCall{}, false, notFound()
	}
	if err != nil {
		return domain.ModelCall{}, false, classify(err)
	}
	if sameModelCallResult(existing, call) {
		return existing, true, nil
	}
	return domain.ModelCall{}, false, versionConflict()
}

func loadModelCallByNumber(ctx context.Context, queryer queryRower, workspaceID, runID foundation.ID, callNo int) (domain.ModelCall, error) {
	return scanModelCall(queryer.QueryRow(ctx, modelCallSelect+`
		JOIN agent.model_run run ON run.id=call.model_run_id
		WHERE run.workspace_id=$1 AND call.model_run_id=$2 AND call.call_no=$3`, string(workspaceID), string(runID), callNo))
}

func loadModelCallByID(ctx context.Context, queryer queryRower, workspaceID, callID foundation.ID) (domain.ModelCall, error) {
	return scanModelCall(queryer.QueryRow(ctx, modelCallSelect+`
		JOIN agent.model_run run ON run.id=call.model_run_id
		WHERE run.workspace_id=$1 AND call.id=$2`, string(workspaceID), string(callID)))
}
