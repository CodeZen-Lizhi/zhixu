package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

type SourceReviewModelDependencies struct {
	Model      agentapp.ChatModel
	ModelRuns  agentapp.ModelRunRepository
	Store      app.SynthesisSourceReviewStore
	Catalog    *agentapp.RuntimeCatalog
	Scheduler  agentapp.StructuredPhaseScheduler
	ProfileRef agentdomain.ModelProfileRef
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
	Budget     agentapp.RunBudget
}
type SourceReviewModel struct{ dependencies SourceReviewModelDependencies }

func NewSourceReviewModel(d SourceReviewModelDependencies) (*SourceReviewModel, error) {
	if synthesisNil(d.Model) || synthesisNil(d.ModelRuns) || synthesisNil(d.Store) || synthesisNil(d.IDs) || synthesisNil(d.Clock) || d.Catalog == nil || d.ProfileRef.Validate() != nil {
		return nil, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE")
	}
	if d.Budget == (agentapp.RunBudget{}) {
		d.Budget = agentapp.DefaultRunBudget()
	}
	if _, err := agentapp.NewStructuredRunnerWithScheduler(d.Model, d.Catalog, d.Budget, d.Scheduler); err != nil {
		return nil, err
	}
	ref := agentdomain.SchemaRef{ID: app.SynthesisSourceReviewSchema, Version: "v1"}
	if _, err := d.Catalog.Snapshot(agentdomain.PromptRef{ID: ref.ID, Version: ref.Version}, ref, ref, d.ProfileRef); err != nil {
		return nil, err
	}
	return &SourceReviewModel{d}, nil
}
func (m *SourceReviewModel) Review(ctx context.Context, e workflowapp.ExecutionContext, id foundation.ID) (resultRow app.SynthesisManuscriptSourceReview, resultErr error) {
	claimed := false
	knownFailure := false
	defer func() {
		if claimed && resultErr != nil {
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthesisFinalizationTimeout)
			defer cancel()
			cause := resultErr
			if !knownFailure {
				cause = foundation.NewError(foundation.ErrorManualRecoveryRequired, "SYNTHESIS_SOURCE_REVIEW_RECOVERY_REQUIRED", false, cause)
			}
			resultErr = errors.Join(resultErr, m.dependencies.Store.FailSourceReview(finalCtx, e.WorkspaceID, id, e.RunID, cause))
		}
	}()
	row, err := m.dependencies.Store.GetSourceReview(ctx, e.WorkspaceID, id)
	if err != nil {
		return row, err
	}
	if row.WorkflowRunID != e.RunID || e.NodeKind != app.SynthesisSourceReviewModel {
		return row, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
	}
	if row.Status == "REVIEWED" || row.Status == "SUCCEEDED" || row.Status == "REJECTED" {
		// 已接受的字节与终态 ModelRun 原子保存；存储层会在应用证据前独立核验这些字节。
		if row.Snapshot == nil {
			return row, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CORRUPT")
		}
		if _, _, err = app.BindSourceReviewOutput(row.Output, *row.Snapshot); err != nil {
			return row, err
		}
		return row, nil
	}
	if row.Status != "PREPARED" {
		return row, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_RECOVERY_REQUIRED")
	}
	input, err := m.dependencies.Store.ReadSourceReviewInput(ctx, e, id)
	if err != nil {
		return row, err
	}
	payload, err := app.BuildSourceReviewPayload(input)
	if err != nil {
		return row, err
	}
	ref := agentdomain.SchemaRef{ID: app.SynthesisSourceReviewSchema, Version: "v1"}
	request := agentapp.StructuredRunRequest{ProfileRef: m.dependencies.ProfileRef, PromptRef: agentdomain.PromptRef{ID: ref.ID, Version: ref.Version}, SchemaRef: ref, ReducedSchemaRef: ref, Input: payload}
	initial, err := agentapp.InitialStructuredRequest(m.dependencies.Catalog, request)
	if err != nil {
		return row, err
	}
	raw, err := json.Marshal(initial)
	if err != nil {
		return row, err
	}
	modelID, err := m.dependencies.IDs.New()
	if err != nil {
		return row, err
	}
	row, err = m.dependencies.Store.ClaimSourceReview(ctx, e, id, modelID, synthesisHash(raw))
	if err != nil {
		return row, err
	}
	claimed = true
	now := m.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
	run := agentdomain.ModelRun{ID: modelID, WorkspaceID: e.WorkspaceID, WorkflowRunID: e.RunID, NodeRunID: e.NodeRunID, NodeAttemptID: e.NodeAttemptID, ModelSettingsRevision: cloneSynthesisRevisionNumber(e.ModelSettingsRevision), Model: initial.Model, Profile: initial.ProfileRef, Prompt: initial.PromptRef, Schema: initial.SchemaRef, ReducedSchema: ref, Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now}
	stored, replayed, err := m.dependencies.ModelRuns.CreateModelRun(ctx, run)
	if err != nil {
		return row, err
	}
	if replayed || !sameSynthesisModelRun(stored, run) {
		return row, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_RECOVERY_REQUIRED")
	}
	knownFailure = true
	recorded, err := agentapp.NewRecordingChatModel(agentapp.RecordingChatModelDependencies{Model: m.dependencies.Model, Repository: m.dependencies.ModelRuns, WorkspaceID: e.WorkspaceID, ModelRunID: modelID, IDs: m.dependencies.IDs, Clock: m.dependencies.Clock})
	if err != nil {
		return row, err
	}
	runner, err := agentapp.NewStructuredRunnerWithScheduler(recorded, m.dependencies.Catalog, m.dependencies.Budget, m.dependencies.Scheduler)
	if err != nil {
		return row, err
	}
	result, err := runner.Run(ctx, request)
	if err != nil {
		return row, err
	}
	if _, _, err = app.BindSourceReviewOutput(result.Output, input.Snapshot); err != nil {
		return row, err
	}
	knownFailure = false
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthesisFinalizationTimeout)
	defer cancel()
	completed, err := m.dependencies.Store.CompleteSourceReview(finalCtx, e, id, result.Output)
	if err == nil {
		return completed, nil
	}
	// 事务结果未知时，恢复精确的持久化输出，绝不重新生成。
	recovered, recoveryErr := m.dependencies.Store.GetSourceReview(finalCtx, e.WorkspaceID, id)
	if recoveryErr == nil && recovered.ModelRunID == modelID && bytes.Equal(recovered.Output, result.Output) && (recovered.Status == "REVIEWED" || recovered.Status == "REJECTED" || recovered.Status == "SUCCEEDED") {
		return recovered, nil
	}
	return row, errors.Join(err, recoveryErr)
}
