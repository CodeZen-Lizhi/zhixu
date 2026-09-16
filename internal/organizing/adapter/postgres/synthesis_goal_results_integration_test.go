//go:build integration

package postgres

import (
	"fmt"
	"sort"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	domain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestGoalResultsWaitForAllSelectionsAndPreserveEmptyPageCursor(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 112)
	ctx := t.Context()
	events := []domain.SynthesisSourceReady{}
	for i := 0; i < 3; i++ {
		base := 197000 + i*1000
		source := f.generation(t, base, nil, fmt.Sprintf("Redis expiration bounds cache lifetime: source %d.", i))
		seedGoalCatalogProfile(t, f, source, base, "Redis")
		events = append(events, source.Input.SourceEvent)
	}
	runs, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(f.platform, runs)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := organizingowner.NewSynthesisGoalCatalogReader(profiles, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewGORMGoalSelectionStore(f.platform, f.store, directory, runs)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := organizingowner.NewSynthesisGoalMaterialReader(store, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	created, err := f.store.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "整理 Redis 知识", IdempotencyKey: "goal-results"})
	if err != nil {
		t.Fatal(err)
	}
	query := app.GoalSelectionResultQuery{WorkspaceID: f.workspace, RequestID: created.Request.ID, Limit: 1}
	checkWaiting := func(prepared, pending, succeeded int64) {
		t.Helper()
		page, err := store.ReadGoalSelectionResults(ctx, query)
		if err != nil || page.Progress.Ready() || len(page.Items) != 0 || page.NextAfterSelectionID != "" || page.Progress.PreparedBatches != prepared || page.Progress.Pending != pending || page.Progress.Succeeded != succeeded {
			t.Fatalf("incomplete selection exposed as ready: %+v %v", page, err)
		}
	}
	checkWaiting(0, 0, 0)
	catalogPage, err := directory.ReadSynthesisGoalCatalog(ctx, app.SynthesisGoalCatalogQuery{WorkspaceID: f.workspace, Limit: 32})
	if err != nil || len(catalogPage.Items) != 3 {
		t.Fatalf("catalog %+v %v", catalogPage, err)
	}
	if _, err := f.store.FreezeSynthesisGoalCatalog(ctx, created.Request, catalogPage); err != nil {
		t.Fatal(err)
	}
	checkWaiting(0, 0, 0) // 仅达到 CATALOG_READY 不得暴露生成输入。
	selections, err := store.PrepareGoalSelections(ctx, f.workspace, created.Request.ID, 1)
	if err != nil || len(selections) != 3 {
		t.Fatalf("prepare %+v %v", selections, err)
	}
	checkWaiting(1, 3, 0)
	sort.Slice(selections, func(i, j int) bool { return selections[i].ID < selections[j].ID })
	catalog := agentapp.NewRuntimeCatalog()
	if err := organizingagent.RegisterGoalSelectionRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	profile := agentapp.ModelProfile{Ref: agentdomain.ModelProfileRef{ID: "goal-results-test", Version: "v1"}, Model: agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "goal-results", ModelVersion: "v1"}, Timeout: time.Second, MaxOutputTokens: 2048}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	provider := agentapp.NewDeterministicChatModel(
		agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: profile.Model, Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}, Content: []byte(`{"selections":[],"explanation":"No relevant point in this slice."}`)}},
		agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: profile.Model, Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}, Content: []byte(`{"selections":[{"point":"P001","reason":"Redis expiry supports the goal."}],"explanation":"Relevant evidence."}`)}},
		agentapp.DeterministicChatStep{Response: agentapp.ChatResponse{Model: profile.Model, Usage: agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}, Content: []byte(`{"selections":[{"point":"P001","reason":"Redis expiry supports the goal."}],"explanation":"Relevant evidence."}`)}},
	)
	model, err := organizingagent.NewGoalSelectionModel(organizingagent.GoalSelectionModelDependencies{Model: provider, ModelRuns: runs, Store: store, Catalog: catalog, Scheduler: scheduler, ProfileRef: profile.Ref, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	// 选择聚合和证据身份解析必须在不开启产物字节的情况下工作；生成阶段另行核验这些字节。
	originalArtifacts := f.artifacts.items
	f.artifacts.items = nil
	for i, selection := range selections {
		execution := seedGoalSelectionExecution(t, f, store, selection, 201000+i*100)
		selection, err = store.GetGoalSelection(ctx, f.workspace, selection.ID)
		if err != nil {
			t.Fatal(err)
		}
		completed, err := model.Select(ctx, execution, selection)
		if err != nil {
			organizingIntegrationFatal(t, err)
		}
		selections[i] = completed
		if i < 2 {
			checkWaiting(1, int64(2-i), int64(i+1))
		}
	}
	preparer, err := app.NewSynthesisGoalPreparer(materials, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable, err := preparer.Prepare(ctx, f.workspace, created.Request.ID); err == nil || len(unavailable.Sources) != 0 {
		t.Fatalf("missing original bytes produced generation material: %+v %v", unavailable, err)
	}
	f.artifacts.items = originalArtifacts
	prepared, err := preparer.Prepare(ctx, f.workspace, created.Request.ID)
	if err != nil || len(prepared.Sources) != 2 {
		t.Fatalf("multi-source original preparation %+v %v", prepared, err)
	}
	binding, err := app.BuildSynthesisGoalBinding(prepared)
	if err != nil || binding.Text != created.Request.Goal || len(binding.Materials) != 2 {
		t.Fatalf("goal binding %+v %v", binding, err)
	}
	checkGoalProcessingPersistence(t, f, directory, binding, events)
	for _, source := range prepared.Sources {
		if source.Excerpt.Validate(f.workspace) != nil || len(source.Points) != 1 {
			t.Fatal("original source lost hash or point proof")
		}
	}
	progress, err := store.ReadGoalSelectionProgress(ctx, f.workspace, created.Request.ID)
	if err != nil || !progress.Ready() || progress.Selections != 3 || progress.Succeeded != 3 {
		t.Fatalf("progress %+v %v", progress, err)
	}
	for i, selection := range selections {
		page, err := store.ReadGoalSelectionResults(ctx, query)
		if err != nil || !page.Progress.Ready() || len(page.Items) != 1 || page.Items[0].SelectionID != selection.ID || page.Items[0].ModelRunID != selection.ModelRunID {
			t.Fatalf("page %+v %v", page, err)
		}
		material, err := materials.ReadGoalSourceMaterials(ctx, query)
		if err != nil || material.NextAfterSelectionID != page.NextAfterSelectionID {
			t.Fatalf("material %+v %v", material, err)
		}
		if i == 0 {
			if len(page.Items[0].Points) != 0 || len(material.Items) != 0 || page.NextAfterSelectionID != selection.ID {
				t.Fatal("explicit empty selection lost the next page")
			}
		} else {
			input, err := store.ReadGoalSelectionInput(ctx, f.workspace, selection.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items[0].Points) != 1 || len(material.Items) != 1 {
				t.Fatalf("missing selected evidence %+v %+v", page, material)
			}
			point, source := page.Items[0].Points[0], material.Items[0]
			if point.Source != input.Source || point.Locator != input.Points[0].Locator || source.Reference.Source != input.Source || source.Reference.SourceSpanID != input.Points[0].SourceSpanIDs[0] || len(source.Points) != 1 || source.Points[0].Locator != point.Locator || source.Points[0].SelectionID != selection.ID || source.Points[0].ModelRunID != selection.ModelRunID {
				t.Fatal("selected point lost exact provenance")
			}
		}
		if i < 2 && page.NextAfterSelectionID != selection.ID || i == 2 && page.NextAfterSelectionID != "" {
			t.Fatalf("cursor %+v", page)
		}
		query.AfterSelectionID = selection.ID
	}
	last, err := store.ReadGoalSelectionResults(ctx, query)
	if err != nil || !last.Progress.Ready() || len(last.Items) != 0 || last.NextAfterSelectionID != "" {
		t.Fatalf("end page %+v %v", last, err)
	}
	query.AfterSelectionID = organizingIntegrationID(999991)
	if _, err := store.ReadGoalSelectionResults(ctx, query); err == nil {
		t.Fatal("foreign cursor accepted")
	}
	query.WorkspaceID = f.otherWorkspace
	query.AfterSelectionID = ""
	if _, err := store.ReadGoalSelectionResults(ctx, query); !organizingIntegrationError(err, foundation.ErrorNotFound, "SYNTHESIS_GOAL_NOT_FOUND") {
		t.Fatalf("cross-workspace result %v", err)
	}
	if _, err := store.ReadGoalSelectionProgress(ctx, f.otherWorkspace, created.Request.ID); !organizingIntegrationError(err, foundation.ErrorNotFound, "SYNTHESIS_GOAL_NOT_FOUND") {
		t.Fatalf("cross-workspace progress %v", err)
	}
	if provider.CallCount() != 3 {
		t.Fatalf("result reads reran provider: %d", provider.CallCount())
	}
	f.count(t, "organizing.synthesis_note", "workspace_id", string(f.workspace), 0)
}
