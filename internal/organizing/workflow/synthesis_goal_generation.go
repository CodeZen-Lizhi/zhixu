package workflow

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// GoalGenerationDispatcher 在全部不可变选择完成后启动既有四阶段合成图。
// 目标拥有生成身份；其真实摄取事件仅保留为溯源，不能重新发布。
type GoalGenerationDispatcher struct {
	Workspaces  workspacedomain.ActiveWorkspaceRepository
	Store       SynthesisGoalGenerationStore
	UnitOfWork  foundation.UnitOfWork
	Starter     workflowapp.ScopedRuntimeStarter
	Definitions *workflowapp.DefinitionRegistry
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
}

type SynthesisGoalGenerationStore interface {
	SynthesisProcessingStore
	ClaimSynthesisGoalGenerationScoped(context.Context, foundation.TransactionScope, foundation.ID) (app.SynthesisGoalGenerationSeed, bool, error)
	FindSynthesisGoalProcessingScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID) (app.SynthesisProcessing, bool, error)
}

func (d *GoalGenerationDispatcher) DispatchBatch(ctx context.Context, limit int) (SynthesisDispatchBatchResult, error) {
	var result SynthesisDispatchBatchResult
	if d == nil || ctx == nil || limit < 1 || limit > 100 || nilScopedDependency(d.Workspaces) || nilScopedDependency(d.Store) || nilScopedDependency(d.UnitOfWork) || nilScopedDependency(d.Starter) || d.Definitions == nil || nilScopedDependency(d.IDs) || nilScopedDependency(d.Clock) {
		return result, synthesisInvalid("goal generation dispatcher is unavailable")
	}
	workspace, err := d.Workspaces.GetActiveWorkspace(ctx)
	if err != nil {
		return result, err
	}
	definition, err := d.Definitions.Resolve(SynthesisDefinitionKey, SynthesisDefinitionVersion)
	if err != nil {
		return result, err
	}
	for result.Started < limit {
		found, waiting := false, false
		err := d.UnitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			// 选择目标前先将所有合成生产者串行化，
			// 同时防止来源生产者在领取与启动之间占用活跃执行槽。
			active, err := d.Store.HasActiveSynthesisProcessingScoped(ctx, scope, workspace.ID)
			if err != nil {
				return err
			}
			if active {
				waiting = true
				return nil
			}
			seed, ok, err := d.Store.ClaimSynthesisGoalGenerationScoped(ctx, scope, workspace.ID)
			if err != nil || !ok {
				return err
			}
			if !validID(seed.GoalRequestID) || seed.SourceEvent.Source.WorkspaceID != workspace.ID || seed.SourceEvent.Validate() != nil || seed.SourceEvent.Fusion != nil {
				return synthesisInvalid("goal generation seed is invalid")
			}
			_, exists, err := d.Store.FindSynthesisGoalProcessingScoped(ctx, scope, workspace.ID, seed.GoalRequestID)
			if err != nil {
				return err
			}
			if exists {
				return synthesisInvalid("claimed goal already has a generation")
			}
			id, err := d.IDs.New()
			if err != nil {
				return err
			}
			input, err := json.Marshal(SynthesisStartInput{GoalRequestID: seed.GoalRequestID, ProcessingID: id, ExecutionNo: 1})
			if err != nil {
				return err
			}
			request, err := workflowapp.BuildRuntimeStartRequest(d.IDs, d.Clock, workspace.ID, SynthesisStartIdempotencyKey(id, 1), input, definition)
			if err != nil {
				return err
			}
			started, err := d.Starter.StartScoped(ctx, scope, request)
			if err != nil {
				return err
			}
			if started.Run.WorkspaceID != workspace.ID || !validID(started.Run.ID) || started.Run.IdempotencyKey != request.Run.IdempotencyKey || started.Job.JobID < 1 {
				return synthesisInvalid("goal generation start binding changed")
			}
			key, err := app.SynthesisProcessingKey(seed.SourceEvent, seed.GoalRequestID)
			if err != nil {
				return err
			}
			now := canonicalTime(d.Clock.Now())
			processing := app.SynthesisProcessing{GoalRequestID: seed.GoalRequestID, ID: id, SourceEvent: seed.SourceEvent, WorkflowRunID: started.Run.ID, RequestHash: strings.TrimPrefix(key, "synthesis-goal:"), Status: app.SynthesisProcessingPending, RevisionIDs: []foundation.ID{}, Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := d.Store.CreateSynthesisProcessingScoped(ctx, scope, SynthesisCreateProcessing{Processing: processing, Start: started}); err != nil {
				return err
			}
			found = true
			return nil
		})
		if err != nil {
			return result, err
		}
		if waiting {
			result.Waiting++
			break
		}
		if !found {
			break
		}
		result.Claimed++
		result.Started++
	}
	return result, nil
}
