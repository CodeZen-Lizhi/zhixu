package workflow

import (
	"context"
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"time"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// 目标目录发现是只处理元数据的生产任务。模型选择和生成是独立的持久执行，
// 目录完成不能直接发布。
type SynthesisGoalCatalogDispatcher struct {
	Workspaces workspacedomain.ActiveWorkspaceRepository
	Store      app.SynthesisGoalRequestStore
	Catalog    app.SynthesisGoalCatalogReader
}

func (d *SynthesisGoalCatalogDispatcher) DispatchBatch(ctx context.Context, limit int) (int, error) {
	if d == nil || ctx == nil || nilScopedDependency(d.Workspaces) || nilScopedDependency(d.Store) || nilScopedDependency(d.Catalog) || limit < 1 || limit > 100 {
		return 0, synthesisInvalid("goal catalog dispatcher is unavailable")
	}
	workspace, err := d.Workspaces.GetActiveWorkspace(ctx)
	if err != nil {
		return 0, err
	}
	requests, err := d.Store.ListDiscoveringSynthesisGoals(ctx, workspace.ID, limit)
	if err != nil {
		return 0, err
	}
	progressed := 0
	var failures error
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return progressed, errors.Join(failures, err)
		}
		if request.WorkspaceID != workspace.ID || request.Status != app.SynthesisGoalDiscovering {
			return progressed, synthesisInvalid("goal catalog request is outside active workspace")
		}
		page, err := d.Catalog.ReadSynthesisGoalCatalog(ctx, app.SynthesisGoalCatalogQuery{WorkspaceID: workspace.ID, AfterSourceID: request.AfterSourceID, Limit: 32})
		var result app.SynthesisGoalFreezeResult
		if err == nil && page.DeferredCode != "" {
			recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			deferErr := d.Store.DeferSynthesisGoal(recoveryCtx, request, page.DeferredCode)
			cancel()
			failures = errors.Join(failures, deferErr)
			continue
		}
		if err == nil {
			result, err = d.Store.FreezeSynthesisGoalCatalog(ctx, request, page)
		}
		if err != nil {
			code := "SYNTHESIS_GOAL_DISCOVERY_FAILED"
			var classified *foundation.Error
			if errors.As(err, &classified) {
				code = classified.Code
			}
			recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			deferErr := d.Store.DeferSynthesisGoal(recoveryCtx, request, code)
			cancel()
			failures = errors.Join(failures, err, deferErr)
			continue
		}
		if !result.Replayed {
			progressed++
		}
	}
	return progressed, failures
}
