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
	return r.finalizeModelRun(ctx, r.db, command)
}

// FinalizeModelRunTx 在调用方事务内复用 Model Run CAS；不会提交或回滚事务。
func (r *Repository) FinalizeModelRunTx(ctx context.Context, transaction any, command application.FinalizeModelRunCommand) (domain.ModelRun, bool, error) {
	tx, ok := transaction.(pgx.Tx)
	if !ok || tx == nil {
		return domain.ModelRun{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeModelRunInvalid, false, errors.New("agent model run transaction is invalid"))
	}
	return r.finalizeModelRun(ctx, tx, command)
}

// GetModelRunTx 在调用方事务内按 Workspace 读取 Model Run；forUpdate 用于跨聚合终结锁。
func (r *Repository) GetModelRunTx(ctx context.Context, transaction any, workspaceID, runID foundation.ID, forUpdate bool) (domain.ModelRun, error) {
	tx, ok := transaction.(pgx.Tx)
	if !ok || tx == nil || !validID(workspaceID) || !validID(runID) {
		return domain.ModelRun{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeModelRunInvalid, false, errors.New("agent model run transaction query is invalid"))
	}
	query := modelRunSelect + ` WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	run, err := scanModelRun(tx.QueryRow(ctx, query, string(workspaceID), string(runID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModelRun{}, notFound()
	}
	if err != nil {
		return domain.ModelRun{}, classify(err)
	}
	return run, nil
}

// GetModelRunByAttemptTx 在调用方事务内按唯一 Node Attempt 查找 Model Run。
func (r *Repository) GetModelRunByAttemptTx(ctx context.Context, transaction any, workspaceID, attemptID foundation.ID, forUpdate bool) (domain.ModelRun, bool, error) {
	tx, ok := transaction.(pgx.Tx)
	if !ok || tx == nil || !validID(workspaceID) || !validID(attemptID) {
		return domain.ModelRun{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeModelRunInvalid, false, errors.New("agent model run attempt transaction query is invalid"))
	}
	query := modelRunSelect + ` WHERE workspace_id=$1 AND node_attempt_id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	run, err := scanModelRun(tx.QueryRow(ctx, query, string(workspaceID), string(attemptID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModelRun{}, false, nil
	}
	if err != nil {
		return domain.ModelRun{}, false, classify(err)
	}
	return run, true, nil
}

func (r *Repository) finalizeModelRun(ctx context.Context, db DB, command application.FinalizeModelRunCommand) (domain.ModelRun, bool, error) {
	run := command.Run
	if command.ExpectedVersion <= 0 || run.Version != command.ExpectedVersion+1 || run.Status == domain.ModelRunRunning ||
		domain.ValidateModelRun(run) != nil {
		return domain.ModelRun{}, false, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeModelRunInvalid, false, errors.New("model run finalization is invalid"))
	}
	tag, err := db.Exec(ctx, `
		UPDATE agent.model_run SET
			retrieval_index_version_id=$1,embedding_version_id=$2,rerank_model_version=$3,
			status=$4,final_result_type=$5,error_code=$6,version=$7,updated_at=$8,completed_at=$9
		WHERE id=$10 AND workspace_id=$11 AND status='RUNNING' AND version=$12`,
		optionalFoundationID(run.Retrieval.IndexVersionID), optionalID(run.Retrieval.EmbeddingVersionID), optionalText(run.Retrieval.RerankModelVersion),
		string(run.Status), optionalText(run.FinalResultType), optionalText(run.FinalErrorCode), run.Version,
		run.UpdatedAt.UTC(), optionalTime(run.CompletedAt), string(run.ID), string(run.WorkspaceID), command.ExpectedVersion,
	)
	if err != nil {
		return domain.ModelRun{}, false, classify(err)
	}
	if tag.RowsAffected() == 1 {
		return run, false, nil
	}
	existing, err := loadModelRunByID(ctx, db, run.WorkspaceID, run.ID)
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

var _ application.ModelRunTxFinalizer = (*Repository)(nil)
