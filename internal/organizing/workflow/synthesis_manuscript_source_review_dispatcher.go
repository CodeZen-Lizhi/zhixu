package workflow

import (
	"context"
	"encoding/json"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

type SourceReviewDispatcher struct {
	Workspaces  workspacedomain.ActiveWorkspaceRepository
	Store       app.SynthesisSourceReviewDispatchStore
	UnitOfWork  foundation.UnitOfWork
	Starter     workflowapp.ScopedRuntimeStarter
	Definitions *workflowapp.DefinitionRegistry
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
}

func (d *SourceReviewDispatcher) DispatchBatch(ctx context.Context, limit int) (int, error) {
	if d == nil || d.Definitions == nil || nilScopedDependency(d.Store) || nilScopedDependency(d.UnitOfWork) || nilScopedDependency(d.Starter) || nilScopedDependency(d.IDs) || nilScopedDependency(d.Clock) || nilScopedDependency(d.Workspaces) || limit < 1 || limit > 100 {
		return 0, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE")
	}
	if reconciler, ok := d.Store.(interface {
		ReconcileSourceReviews(context.Context, int) (int, error)
	}); ok {
		if _, err := reconciler.ReconcileSourceReviews(ctx, limit); err != nil {
			return 0, err
		}
	}
	workspace, err := d.Workspaces.GetActiveWorkspace(ctx)
	if err != nil {
		return 0, err
	}
	definition, err := d.Definitions.Resolve(app.SynthesisSourceReviewDefinition, 1)
	if err != nil {
		return 0, err
	}
	count := 0
	for count < limit {
		found := false
		err = d.UnitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			row, ok, err := d.Store.DispatchSourceReviewScoped(ctx, scope, workspace.ID)
			if err != nil || !ok {
				return err
			}
			found = true
			raw, err := json.Marshal(sourceReviewStart{row.ID})
			if err != nil {
				return err
			}
			start, err := workflowapp.BuildRuntimeStartRequest(d.IDs, d.Clock, workspace.ID, sourceReviewStartKey(row.ID), raw, definition)
			if err != nil {
				return err
			}
			result, err := d.Starter.StartScoped(ctx, scope, start)
			if err != nil {
				return err
			}
			if result.Run.WorkspaceID != workspace.ID || result.Job.JobID < 1 {
				return app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
			}
			return d.Store.BindSourceReviewWorkflowScoped(ctx, scope, workspace.ID, row.ID, result.Run.ID)
		})
		if err != nil {
			return count, err
		}
		if !found {
			break
		}
		count++
	}
	return count, nil
}
