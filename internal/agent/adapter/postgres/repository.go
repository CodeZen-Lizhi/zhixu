package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB 是 Agent Repository 所需的最小 pgx 边界。
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Repository 持久化 Model Run/Call 且不保存 Prompt、Evidence 或原始响应。
type Repository struct{ db DB }

// NewRepository 创建 Agent PostgreSQL Repository。
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, errors.New("agent database is nil"))
	}
	return &Repository{db: db}, nil
}

// CreateModelRun 在 Provider 调用前幂等创建 RUNNING Model Run。
func (r *Repository) CreateModelRun(ctx context.Context, run domain.ModelRun) (domain.ModelRun, bool, error) {
	if err := domain.ValidateModelRun(run); err != nil || run.Status != domain.ModelRunRunning || run.Version != 1 {
		if err != nil {
			return domain.ModelRun{}, false, err
		}
		return domain.ModelRun{}, false, consistency(errors.New("new model run must be running at version one"))
	}
	tag, err := r.db.Exec(ctx, insertModelRunSQL, modelRunArgs(run)...)
	if err != nil {
		return domain.ModelRun{}, false, classify(err)
	}
	if tag.RowsAffected() == 1 {
		return run, false, nil
	}
	existing, err := loadModelRunByAttempt(ctx, r.db, run.NodeAttemptID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ModelRun{}, false, replayConflict()
		}
		return domain.ModelRun{}, false, classify(err)
	}
	if !sameModelRunCreateBinding(existing, run) {
		return domain.ModelRun{}, false, replayConflict()
	}
	return existing, true, nil
}

// GetModelRun 按 Workspace 返回 Model Run 和全部调用历史。
func (r *Repository) GetModelRun(ctx context.Context, workspaceID, runID foundation.ID) (application.ModelRunRecord, error) {
	if !validID(workspaceID) || !validID(runID) {
		return application.ModelRunRecord{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeModelRunInvalid, false, errors.New("model run query identity is invalid"))
	}
	run, err := loadModelRunByID(ctx, r.db, workspaceID, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ModelRunRecord{}, notFound()
	}
	if err != nil {
		return application.ModelRunRecord{}, classify(err)
	}
	calls, err := loadModelCalls(ctx, r.db, workspaceID, runID)
	if err != nil {
		return application.ModelRunRecord{}, classify(err)
	}
	return application.ModelRunRecord{Run: run, Calls: calls}, nil
}

var _ application.ModelRunRepository = (*Repository)(nil)

const insertModelRunSQL = `
	INSERT INTO agent.model_run(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,model_settings_revision,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,
		retrieval_index_version_id,embedding_version_id,rerank_model_version,
		memory_snapshot_id,memory_context_schema_version,memory_context_digest,memory_context_item_count,memory_context_bytes,
		status,final_result_type,error_code,version,started_at,updated_at,completed_at
	) VALUES(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33
	) ON CONFLICT DO NOTHING`

func modelRunArgs(run domain.ModelRun) []any {
	return []any{
		string(run.ID), string(run.WorkspaceID), string(run.WorkflowRunID), string(run.NodeRunID), string(run.NodeAttemptID),
		nullableInt64(run.ModelSettingsRevision),
		run.Model.AdapterName, run.Model.AdapterVersion, run.Model.ModelID, run.Model.ModelVersion,
		run.Profile.ID, run.Profile.Version, run.Prompt.ID, run.Prompt.Version, run.Schema.ID, run.Schema.Version,
		run.ReducedSchema.ID, run.ReducedSchema.Version,
		optionalFoundationID(run.Retrieval.IndexVersionID), optionalID(run.Retrieval.EmbeddingVersionID), optionalText(run.Retrieval.RerankModelVersion),
		optionalFoundationID(run.MemoryContext.SnapshotID), optionalText(run.MemoryContext.SchemaVersion), optionalText(run.MemoryContext.Digest),
		optionalInt(run.MemoryContext.IsBound(), run.MemoryContext.ItemCount), optionalInt64(run.MemoryContext.IsBound(), run.MemoryContext.ByteCount),
		string(run.Status), optionalText(run.FinalResultType), optionalText(run.FinalErrorCode), run.Version,
		run.CreatedAt.UTC(), run.UpdatedAt.UTC(), optionalTime(run.CompletedAt),
	}
}
