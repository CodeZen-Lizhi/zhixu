package workflow

import (
	"context"
	"fmt"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestGoalCatalogDefersReadinessWithoutFreezing(t *testing.T) {
	workspaceID := goalCatalogWorkflowID(1)
	request := app.SynthesisGoalRequest{ID: goalCatalogWorkflowID(2), WorkspaceID: workspaceID, Status: app.SynthesisGoalDiscovering, Version: 1}
	store := &goalCatalogWorkflowStore{request: request}
	dispatcher := SynthesisGoalCatalogDispatcher{
		Workspaces: goalCatalogWorkflowWorkspace{workspace: workspacedomain.Workspace{ID: workspaceID}},
		Store:      store,
		Catalog:    goalCatalogWorkflowReader{page: app.SynthesisGoalCatalogPage{Items: []app.SynthesisGoalCatalogItem{}, DeferredCode: "SYNTHESIS_GOAL_SOURCE_PENDING"}},
	}
	count, err := dispatcher.DispatchBatch(t.Context(), 1)
	if err != nil || count != 0 || store.freezes != 0 || len(store.deferred) != 1 || store.deferred[0] != "SYNTHESIS_GOAL_SOURCE_PENDING" {
		t.Fatalf("pending catalog was frozen: count=%d err=%v store=%+v", count, err, store)
	}
}

type goalCatalogWorkflowWorkspace struct{ workspace workspacedomain.Workspace }

func (f goalCatalogWorkflowWorkspace) GetActiveWorkspace(context.Context) (workspacedomain.Workspace, error) {
	return f.workspace, nil
}

type goalCatalogWorkflowReader struct{ page app.SynthesisGoalCatalogPage }

func (f goalCatalogWorkflowReader) ReadSynthesisGoalCatalog(context.Context, app.SynthesisGoalCatalogQuery) (app.SynthesisGoalCatalogPage, error) {
	return f.page, nil
}

type goalCatalogWorkflowStore struct {
	request  app.SynthesisGoalRequest
	deferred []string
	freezes  int
}

func (f *goalCatalogWorkflowStore) CreateSynthesisGoal(context.Context, app.CreateSynthesisGoalCommand) (app.SynthesisGoalCreateResult, error) {
	return app.SynthesisGoalCreateResult{}, fmt.Errorf("unused")
}
func (f *goalCatalogWorkflowStore) GetSynthesisGoal(context.Context, foundation.ID, foundation.ID) (app.SynthesisGoalRequest, error) {
	return f.request, nil
}
func (f *goalCatalogWorkflowStore) ListDiscoveringSynthesisGoals(context.Context, foundation.ID, int) ([]app.SynthesisGoalRequest, error) {
	return []app.SynthesisGoalRequest{f.request}, nil
}
func (f *goalCatalogWorkflowStore) FreezeSynthesisGoalCatalog(context.Context, app.SynthesisGoalRequest, app.SynthesisGoalCatalogPage) (app.SynthesisGoalFreezeResult, error) {
	f.freezes++
	return app.SynthesisGoalFreezeResult{}, nil
}
func (f *goalCatalogWorkflowStore) GetSynthesisGoalCatalogBatch(context.Context, foundation.ID, foundation.ID, int64) (app.SynthesisGoalCatalogBatch, error) {
	return app.SynthesisGoalCatalogBatch{}, fmt.Errorf("unused")
}
func (f *goalCatalogWorkflowStore) DeferSynthesisGoal(_ context.Context, _ app.SynthesisGoalRequest, code string) error {
	f.deferred = append(f.deferred, code)
	return nil
}

func goalCatalogWorkflowID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", value))
}
