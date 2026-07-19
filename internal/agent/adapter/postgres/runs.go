package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// FinalizeModelRun 使用 expected_version 把 RUNNING Model Run 归约为唯一终态。
func (r *Repository) FinalizeModelRun(ctx context.Context, command application.FinalizeModelRunCommand) (domain.ModelRun, bool, error) {
	run := command.Run
	if command.ExpectedVersion <= 0 || run.Version != command.ExpectedVersion+1 || run.Status == domain.ModelRunRunning ||
		domain.ValidateModelRun(run) != nil {
		return domain.ModelRun{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeModelRunInvalid, false, errors.New("model run finalization is invalid"))
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE agent.model_run SET
			status=$1,final_result_type=$2,error_code=$3,version=$4,updated_at=$5,completed_at=$6
		WHERE id=$7 AND workspace_id=$8 AND status='RUNNING' AND version=$9`,
		string(run.Status), optionalText(run.FinalResultType), optionalText(run.FinalErrorCode), run.Version,
		run.UpdatedAt.UTC(), optionalTime(run.CompletedAt), string(run.ID), string(run.WorkspaceID), command.ExpectedVersion,
	)
	if err != nil {
		return domain.ModelRun{}, false, classify(err)
	}
	if tag.RowsAffected() == 1 {
		return run, false, nil
	}
	existing, err := loadModelRunByID(ctx, r.db, run.WorkspaceID, run.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModelRun{}, false, notFound()
	}
	if err != nil {
		return domain.ModelRun{}, false, classify(err)
	}
	if sameModelRunResult(existing, run) {
		return existing, true, nil
	}
	return domain.ModelRun{}, false, versionConflict()
}
