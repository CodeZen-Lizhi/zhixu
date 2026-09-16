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

// BodyRefreshGenerationDispatcher 与既有合成执行槽串行化，
// 为每个持久化的目标/发布请求启动一次四阶段执行。
// 它不会消费或重新发出用于原始溯源的 source-ready 事件。
type BodyRefreshGenerationDispatcher struct {
	Manuscripts bool
	Workspaces  workspacedomain.ActiveWorkspaceRepository
	Store       SynthesisBodyRefreshGenerationStore
	UnitOfWork  foundation.UnitOfWork
	Starter     workflowapp.ScopedRuntimeStarter
	Definitions *workflowapp.DefinitionRegistry
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
}

type SynthesisBodyRefreshGenerationStore interface {
	SynthesisProcessingStore
	ClaimSynthesisBodyRefreshGenerationScoped(context.Context, foundation.TransactionScope, foundation.ID) (app.SynthesisBodyRefreshGenerationSeed, bool, error)
	FindSynthesisBodyRefreshProcessingScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID) (app.SynthesisProcessing, bool, error)
}

func (d *BodyRefreshGenerationDispatcher) DispatchBatch(ctx context.Context, limit int) (SynthesisDispatchBatchResult, error) {
	var result SynthesisDispatchBatchResult
	if d == nil || ctx == nil || limit < 1 || limit > 100 || nilScopedDependency(d.Workspaces) || nilScopedDependency(d.Store) || nilScopedDependency(d.UnitOfWork) || nilScopedDependency(d.Starter) || d.Definitions == nil || nilScopedDependency(d.IDs) || nilScopedDependency(d.Clock) {
		return result, synthesisInvalid("body refresh generation dispatcher is unavailable")
	}
	workspace, err := d.Workspaces.GetActiveWorkspace(ctx)
	if err != nil {
		return result, err
	}
	definition, err := d.Definitions.Resolve(SynthesisDefinitionKey, synthesisDispatchVersion(d.Manuscripts))
	if err != nil {
		return result, err
	}
	for result.Started < limit {
		found, waiting := false, false
		err := d.UnitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			// 选择请求前先将所有合成生产者串行化，
			// 防止来源生产者在领取与启动之间占用活跃执行槽。
			active, err := d.Store.HasActiveSynthesisProcessingScoped(ctx, scope, workspace.ID)
			if err != nil {
				return err
			}
			if active {
				waiting = true
				return nil
			}
			seed, ok, err := d.Store.ClaimSynthesisBodyRefreshGenerationScoped(ctx, scope, workspace.ID)
			if err != nil || !ok {
				return err
			}
			if !validID(seed.BodyRefreshRequestID) || seed.SourceEvent.Source.WorkspaceID != workspace.ID || seed.SourceEvent.Validate() != nil || seed.SourceEvent.Fusion != nil {
				return synthesisInvalid("body refresh generation seed is invalid")
			}
			_, exists, err := d.Store.FindSynthesisBodyRefreshProcessingScoped(ctx, scope, workspace.ID, seed.BodyRefreshRequestID)
			if err != nil {
				return err
			}
			if exists {
				return synthesisInvalid("claimed body refresh already has a generation")
			}
			id, err := d.IDs.New()
			if err != nil {
				return err
			}
			input, err := json.Marshal(SynthesisStartInput{BodyRefreshRequestID: seed.BodyRefreshRequestID, ProcessingID: id, ExecutionNo: 1})
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
				return synthesisInvalid("body refresh generation start binding changed")
			}
			key, err := app.SynthesisExecutionKey(seed.SourceEvent, "", seed.BodyRefreshRequestID)
			if err != nil {
				return err
			}
			now := canonicalTime(d.Clock.Now())
			processing := app.SynthesisProcessing{BodyRefreshRequestID: seed.BodyRefreshRequestID, ID: id, SourceEvent: seed.SourceEvent, WorkflowRunID: started.Run.ID, RequestHash: strings.TrimPrefix(key, "synthesis-body-refresh:"), Status: app.SynthesisProcessingPending, RevisionIDs: []foundation.ID{}, Version: 1, CreatedAt: now, UpdatedAt: now}
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
