//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

type goalViewProcessingStub struct {
	values map[foundation.ID]app.SynthesisProcessing
}

func (stub goalViewProcessingStub) ReadSynthesisGoalProcessingBatchScoped(_ context.Context, _ foundation.TransactionScope, _ foundation.ID, goalIDs []foundation.ID) (map[foundation.ID]app.SynthesisProcessing, error) {
	result := make(map[foundation.ID]app.SynthesisProcessing, len(goalIDs))
	for _, goalID := range goalIDs {
		if value, exists := stub.values[goalID]; exists {
			result[goalID] = value
		}
	}
	return result, nil
}

func TestGoalViewReadsStableGoalFactsAndEmptySelections(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 112)
	ctx := t.Context()
	reader, err := NewGORMGoalViewReader(f.platform, goalViewProcessingStub{})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := f.store.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "整理空目录知识", IdempotencyKey: "goal-view-ready"})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := f.store.FreezeSynthesisGoalCatalog(ctx, ready.Request, app.SynthesisGoalCatalogPage{Items: []app.SynthesisGoalCatalogItem{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO organizing.synthesis_goal_selection_preparation(batch_id,workspace_id,next_check_at,error_code) VALUES(?,?,clock_timestamp()+interval '30 seconds','SYNTHESIS_GOAL_METADATA_TOO_LARGE')`, string(frozen.Batch.ID), string(f.workspace)).Error; err != nil {
		t.Fatal(err)
	}
	blocked, err := reader.GetSynthesisGoalView(ctx, f.workspace, ready.Request.ID)
	if err != nil || blocked.PreparationFailures != 1 || blocked.PreparationErrorCode != "SYNTHESIS_GOAL_METADATA_TOO_LARGE" || blocked.Progress.Ready() {
		t.Fatalf("preparation failure hidden %+v %v", blocked, err)
	}
	// 空的不可变目录仍有已封存、零行的选择清单；它已经完成，不是仍待处理的选择。
	if err := f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`INSERT INTO organizing.synthesis_goal_selection_manifest(batch_id,workspace_id,selection_count,sealed) VALUES(?,?,0,false)`, string(frozen.Batch.ID), string(f.workspace)).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Exec(`UPDATE organizing.synthesis_goal_selection_manifest SET sealed=true WHERE batch_id=? AND workspace_id=?`, string(frozen.Batch.ID), string(f.workspace)).Error
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"goal-view-next", "goal-view-last"} {
		if _, err := f.store.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "等待扫描 " + key, IdempotencyKey: key}); err != nil {
			t.Fatal(err)
		}
	}

	view, err := reader.GetSynthesisGoalView(ctx, f.workspace, ready.Request.ID)
	if err != nil || view.Request.ID != ready.Request.ID || !view.Progress.Ready() || view.Progress.Selections != 0 || view.SelectedPoints != 0 || view.Processing != nil || view.Candidate != nil || view.PreparationFailures != 0 || view.PreparationErrorCode != "" {
		t.Fatalf("empty completed goal view %+v %v", view, err)
	}
	if _, err := reader.GetSynthesisGoalView(ctx, f.otherWorkspace, ready.Request.ID); !organizingIntegrationError(err, foundation.ErrorNotFound, "SYNTHESIS_GOAL_NOT_FOUND") {
		t.Fatalf("cross-workspace goal view: %v", err)
	}

	first, err := reader.ListSynthesisGoalViews(ctx, app.SynthesisListQuery{WorkspaceID: f.workspace, Limit: 1})
	if err != nil || len(first.Items) != 1 || first.NextTime == nil || first.NextID == "" {
		t.Fatalf("first page %+v %v", first, err)
	}
	second, err := reader.ListSynthesisGoalViews(ctx, app.SynthesisListQuery{WorkspaceID: f.workspace, Limit: 2, BeforeTime: first.NextTime, BeforeID: first.NextID})
	if err != nil || len(second.Items) != 2 || second.NextTime != nil || second.NextID != "" {
		t.Fatalf("second page %+v %v", second, err)
	}
	seen := map[foundation.ID]bool{first.Items[0].Request.ID: true}
	for _, item := range second.Items {
		if seen[item.Request.ID] {
			t.Fatalf("cursor repeated goal %s", item.Request.ID)
		}
		seen[item.Request.ID] = true
	}
	if len(seen) != 3 || !seen[ready.Request.ID] {
		t.Fatalf("goal cursor lost a request: %+v", seen)
	}
}
