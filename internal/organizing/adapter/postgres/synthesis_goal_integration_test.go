//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	owner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizinghttp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/http"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/gin-gonic/gin"
)

func TestSynthesisGoalPersistsIdempotentCatalogAndResumes(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 109)
	ctx := t.Context()
	first := f.generation(t, 187000, nil, "Redis expiration controls cache lifetime.")
	second := f.generation(t, 188000, nil, "Oracle transactions preserve data integrity.")
	seedGoalCatalogProfile(t, f, first, 187000, "Redis")
	seedGoalCatalogProfile(t, f, second, 188000, "Oracle")
	runs, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(f.platform, runs)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := owner.NewSynthesisGoalCatalogReader(profiles, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	command := app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "整理数据库知识，用于系统学习", IdempotencyKey: "goal-databases"}
	var creations [2]app.SynthesisGoalCreateResult
	var failures [2]error
	var wg sync.WaitGroup
	for i := range creations {
		wg.Add(1)
		go func(i int) { defer wg.Done(); creations[i], failures[i] = f.store.CreateSynthesisGoal(ctx, command) }(i)
	}
	wg.Wait()
	if failures[0] != nil || failures[1] != nil || creations[0].Request.ID != creations[1].Request.ID || creations[0].Replayed == creations[1].Replayed {
		t.Fatalf("concurrent creation %+v %v", creations, failures)
	}
	request := creations[0].Request
	changed := command
	changed.Goal = "整理 Redis 专项知识"
	if _, err := f.store.CreateSynthesisGoal(ctx, changed); !organizingIntegrationError(err, foundation.ErrorVersionConflict, "SYNTHESIS_GOAL_CONFLICT") {
		t.Fatalf("same key accepted changed goal: %v", err)
	}
	if _, err := f.store.GetSynthesisGoal(ctx, f.otherWorkspace, request.ID); !organizingIntegrationError(err, foundation.ErrorNotFound, "SYNTHESIS_GOAL_NOT_FOUND") {
		t.Fatalf("cross workspace goal: %v", err)
	}
	if _, err := f.store.FreezeSynthesisGoalCatalog(ctx, request, app.SynthesisGoalCatalogPage{Items: []app.SynthesisGoalCatalogItem{}, DeferredCode: "SYNTHESIS_GOAL_SOURCE_PENDING"}); !organizingIntegrationError(err, foundation.ErrorInvalidInput, "SYNTHESIS_GOAL_INVALID") {
		t.Fatalf("deferred catalog page advanced request: %v", err)
	}
	page, err := catalog.ReadSynthesisGoalCatalog(ctx, app.SynthesisGoalCatalogQuery{WorkspaceID: f.workspace, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	var frozen [2]app.SynthesisGoalFreezeResult
	for i := range frozen {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			frozen[i], failures[i] = f.store.FreezeSynthesisGoalCatalog(ctx, request, page)
		}(i)
	}
	wg.Wait()
	if failures[0] != nil || failures[1] != nil || frozen[0].Batch.ID != frozen[1].Batch.ID || frozen[0].Replayed == frozen[1].Replayed {
		t.Fatalf("concurrent freeze %+v %v", frozen, failures)
	}
	saved := frozen[0]
	if saved.Request.Status != app.SynthesisGoalDiscovering || saved.Request.CatalogBatches != 1 || saved.Batch.Items[0].ProfileRevisionID != page.Items[0].Directory.ProfileRevisionID {
		t.Fatalf("first frozen %+v", saved)
	}

	// 后续 Profile 重建不得改变从此冻结批次重建的模型输入，即使原始 Artifact 字节不可用。
	f.artifacts.items = nil
	exact, err := profiles.GetProfileRevision(ctx, captureapp.ProfileRevisionQuery{WorkspaceID: f.workspace, SourceVersionID: saved.Batch.Items[0].Source.SourceVersionID, RevisionID: saved.Batch.Items[0].ProfileRevisionID})
	if err != nil {
		t.Fatal(err)
	}
	changedMetadata := exact.Revision.Content
	changedMetadata.KnowledgePoints = append([]capturedomain.ProfilePoint(nil), exact.Revision.Content.KnowledgePoints...)
	changedMetadata.KnowledgePoints[0].Text = "A later AI profile uses different wording."
	digest, err := capturedomain.ComputeProfileDigest(changedMetadata)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(changedMetadata)
	if err != nil {
		t.Fatal(err)
	}
	replacementID := organizingIntegrationID(187195)
	if err := f.db.Exec(`INSERT INTO learning.document_knowledge_profile_revision(id,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,model_run_id,prompt_version,schema_version,content,content_digest,created_at)
 SELECT ?,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,model_run_id,'goal-rebuild/v2',schema_version,?::jsonb,?,clock_timestamp() FROM learning.document_knowledge_profile_revision WHERE id=?`, string(replacementID), string(encoded), digest, string(exact.Revision.ID)).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO learning.document_knowledge_profile_evidence(revision_id,workspace_id,source_version_id,source_span_id,created_at) SELECT ?,workspace_id,source_version_id,source_span_id,clock_timestamp() FROM learning.document_knowledge_profile_evidence WHERE revision_id=?`, string(replacementID), string(exact.Revision.ID)).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`UPDATE learning.document_knowledge_profile SET current_revision_id=?,version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(replacementID), string(exact.Revision.ProfileID)).Error; err != nil {
		t.Fatal(err)
	}
	metadata, err := catalog.ReadSynthesisGoalCatalogSnapshot(ctx, saved.Batch)
	if err != nil || len(metadata) != 1 || metadata[0].Directory.Points[0].Text != first.Input.Sources[0].Text {
		t.Fatalf("frozen metadata replaced %+v %v", metadata, err)
	}
	slices, err := app.BuildGoalSelectionInputs(request, saved.Batch, metadata)
	if err != nil || len(slices) != 1 || slices[0].ProfileRevisionID != exact.Revision.ID {
		t.Fatalf("selection input drifted %+v %v", slices, err)
	}
	// 改变调用方观察值，不能在重试时替换胜出结果。
	replacement := page
	replacement.Items = append([]app.SynthesisGoalCatalogItem(nil), page.Items...)
	replacement.Items[0].Directory.ProfileRevisionID = organizingIntegrationID(999999)
	replay, err := f.store.FreezeSynthesisGoalCatalog(ctx, request, replacement)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Batch, saved.Batch) {
		t.Fatalf("frozen winner replaced %+v %v", replay, err)
	}
	restarted, err := NewGORMSynthesisStore(f.platform, f.store.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := restarted.GetSynthesisGoal(ctx, f.workspace, request.ID)
	if err != nil || resumed.AfterSourceID != saved.Request.AfterSourceID {
		t.Fatalf("resumed %+v %v", resumed, err)
	}
	next, err := catalog.ReadSynthesisGoalCatalog(ctx, app.SynthesisGoalCatalogQuery{WorkspaceID: f.workspace, AfterSourceID: resumed.AfterSourceID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	final, err := restarted.FreezeSynthesisGoalCatalog(ctx, resumed, next)
	if err != nil || final.Request.Status != app.SynthesisGoalCatalogReady || final.Request.CatalogBatches != 2 || final.Request.AfterSourceID != "" || final.Batch.Items[0].Source != second.Input.SourceEvent.Source {
		t.Fatalf("complete catalog %+v %v", final, err)
	}
	due, err := restarted.ListDiscoveringSynthesisGoals(ctx, f.workspace, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("finished enumeration still due %+v %v", due, err)
	}
	if err := f.db.Exec(`UPDATE core.source SET removed_at=clock_timestamp() WHERE id=?`, string(first.Input.SourceEvent.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	historical, err := restarted.GetSynthesisGoalCatalogBatch(ctx, f.workspace, request.ID, 1)
	if err != nil || !reflect.DeepEqual(historical, saved.Batch) {
		t.Fatalf("deleted source rewrote catalog %+v %v", historical, err)
	}
	if _, err := restarted.GetSynthesisGoalCatalogBatch(ctx, f.otherWorkspace, request.ID, 1); !organizingIntegrationError(err, foundation.ErrorNotFound, "SYNTHESIS_GOAL_NOT_FOUND") {
		t.Fatalf("foreign batch accessible %v", err)
	}
	for _, query := range []string{
		`UPDATE organizing.synthesis_goal_catalog_item SET title='rewritten' WHERE batch_id=?`,
		`DELETE FROM organizing.synthesis_goal_catalog_item WHERE batch_id=?`,
		`UPDATE organizing.synthesis_goal_catalog_batch SET item_count=0 WHERE id=?`,
	} {
		if err := f.db.Exec(query, string(saved.Batch.ID)).Error; err == nil {
			t.Fatalf("immutable mutation allowed %s", query)
		}
	}
	if err := f.db.Exec(`UPDATE organizing.synthesis_goal_request SET goal_text='different',version=version+1 WHERE id=?`, string(request.ID)).Error; err == nil {
		t.Fatal("goal retargeting allowed")
	}

	// 一个异常请求持久化退避期间，另一个请求仍可完成发现。
	bad, err := restarted.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "待恢复目标", IdempotencyKey: "goal-bad"})
	if err != nil {
		t.Fatal(err)
	}
	good, err := restarted.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "后续目标", IdempotencyKey: "goal-good"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`UPDATE core.workspace SET status='active',version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(f.workspace)).Error; err != nil {
		t.Fatal(err)
	}
	workspaces, err := workspacepostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := organizingworkflow.SynthesisGoalCatalogDispatcher{Workspaces: workspaces, Store: restarted, Catalog: &goalFailFirstCatalog{reader: catalog}}
	count, dispatchErr := dispatcher.DispatchBatch(ctx, 4)
	if count != 1 || dispatchErr == nil {
		t.Fatalf("failed goal blocked following request: %d %v", count, dispatchErr)
	}
	failed, err := restarted.GetSynthesisGoal(ctx, f.workspace, bad.Request.ID)
	if err != nil || failed.Status != app.SynthesisGoalDiscovering || failed.ErrorCode != "SYNTHESIS_GOAL_DISCOVERY_FAILED" || failed.Version != 2 || !failed.NextCheckAt.After(bad.Request.NextCheckAt) {
		t.Fatalf("deferred request %+v %v", failed, err)
	}
	completed, err := restarted.GetSynthesisGoal(ctx, f.workspace, good.Request.ID)
	if err != nil || completed.Status != app.SynthesisGoalCatalogReady {
		t.Fatalf("next goal %+v %v", completed, err)
	}
	due, err = restarted.ListDiscoveringSynthesisGoals(ctx, f.workspace, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("backoff is not durable %+v %v", due, err)
	}
	recoveryPage, err := catalog.ReadSynthesisGoalCatalog(ctx, app.SynthesisGoalCatalogQuery{WorkspaceID: f.workspace, Limit: 32})
	if err != nil {
		t.Fatal(err)
	}
	// 不可变 Profile 绑定错误时，头记录和请求必须一起回滚。
	incorrect := recoveryPage
	incorrect.Items = append([]app.SynthesisGoalCatalogItem(nil), recoveryPage.Items...)
	incorrect.Items[0].Directory.ProfileRevisionID = page.Items[0].Directory.ProfileRevisionID
	if _, err := restarted.FreezeSynthesisGoalCatalog(ctx, failed, incorrect); err == nil {
		t.Fatal("cross-source profile frozen")
	}
	afterFailure, err := restarted.GetSynthesisGoal(ctx, f.workspace, bad.Request.ID)
	if err != nil || !reflect.DeepEqual(afterFailure, failed) {
		t.Fatalf("failed freeze advanced progress %+v %v", afterFailure, err)
	}
	recovered, err := restarted.FreezeSynthesisGoalCatalog(ctx, failed, recoveryPage)
	if err != nil || recovered.Request.Status != app.SynthesisGoalCatalogReady || recovered.Request.ErrorCode != "" {
		t.Fatalf("recovery failed %+v %v", recovered, err)
	}
	if err := restarted.DeferSynthesisGoal(ctx, failed, "LATE_FAILURE"); err != nil {
		t.Fatal(err)
	}
	stable, err := restarted.GetSynthesisGoal(ctx, f.workspace, bad.Request.ID)
	if err != nil || !reflect.DeepEqual(stable, recovered.Request) {
		t.Fatalf("late failure rewound request %+v %v", stable, err)
	}

	incomplete, err := restarted.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: "原子提交测试", IdempotencyKey: "goal-incomplete"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO organizing.synthesis_goal_catalog_batch(workspace_id,request_id,batch_no,item_count) VALUES(?,?,1,0)`, string(f.workspace), string(incomplete.Request.ID)).Error; err == nil {
		t.Fatal("orphan catalog header committed without request progress")
	}
	f.count(t, "organizing.synthesis_goal_catalog_batch", "request_id", string(incomplete.Request.ID), 0)
	// 目录超时后，仍须通过新的有界上下文持久化退避状态。
	timeoutCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	dispatcher.Catalog = goalTimeoutCatalog{}
	if count, err := dispatcher.DispatchBatch(timeoutCtx, 1); count != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("catalog timeout lost: count=%d err=%v", count, err)
	}
	deferred, err := restarted.GetSynthesisGoal(ctx, f.workspace, incomplete.Request.ID)
	if err != nil || deferred.Version != incomplete.Request.Version+1 || deferred.ErrorCode != "SYNTHESIS_GOAL_DISCOVERY_FAILED" || !deferred.NextCheckAt.After(time.Now().Add(20*time.Second)) {
		t.Fatalf("timeout backoff not committed: %+v %v", deferred, err)
	}
	// 完成枚举不代表生成成功，也不会隐式创建笔记。
	f.count(t, "organizing.synthesis_note", "workspace_id", string(f.workspace), 0)
}

// 在所属模块边界注入临时元数据依赖故障。
type goalFailFirstCatalog struct {
	reader app.SynthesisGoalCatalogReader
	failed bool
}

func (r *goalFailFirstCatalog) ReadSynthesisGoalCatalog(ctx context.Context, q app.SynthesisGoalCatalogQuery) (app.SynthesisGoalCatalogPage, error) {
	if !r.failed {
		r.failed = true
		return app.SynthesisGoalCatalogPage{}, errors.New("metadata dependency unavailable")
	}
	return r.reader.ReadSynthesisGoalCatalog(ctx, q)
}

type goalTimeoutCatalog struct{}

func (goalTimeoutCatalog) ReadSynthesisGoalCatalog(ctx context.Context, _ app.SynthesisGoalCatalogQuery) (app.SynthesisGoalCatalogPage, error) {
	<-ctx.Done()
	return app.SynthesisGoalCatalogPage{}, ctx.Err()
}

func TestSourcePromotionFreezesOneSourceAndReplaysWithoutReanalysis(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 113)
	ctx := t.Context()
	first := f.generation(t, 207000, nil, "Redis expiration bounds cache lifetime.")
	second := f.generation(t, 208000, nil, "Oracle transactions preserve data integrity.")
	seedGoalCatalogProfile(t, f, first, 207000, "Redis")
	seedGoalCatalogProfile(t, f, second, 208000, "Oracle")
	runs, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(f.platform, runs)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := owner.NewSynthesisGoalCatalogReader(profiles, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewSynthesisSourcePromotionService(f.store, catalog, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	command := app.PromoteSynthesisSourceCommand{WorkspaceID: f.workspace, SourceVersionID: first.Input.SourceEvent.Source.SourceVersionID, IdempotencyKey: "promote-source"}
	page, err := catalog.ReadSynthesisGoalCatalog(ctx, app.SynthesisGoalCatalogQuery{WorkspaceID: f.workspace, SourceVersionID: command.SourceVersionID, Limit: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("promotion catalog: %+v %v", page, err)
	}
	// 使用旧版可预测内部键的普通目标，即使目标文本相同，也不得阻塞来源提升或充当其回执。
	digest := sha256.Sum256([]byte("source-promotion:" + command.IdempotencyKey))
	ordinary, err := f.store.CreateSynthesisGoal(ctx, app.CreateSynthesisGoalCommand{WorkspaceID: f.workspace, Goal: app.SynthesisSourcePromotionGoal(page.Items[0].Title), IdempotencyKey: "source-promotion:" + hex.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	var results [2]app.SynthesisSourcePromotion
	var failures [2]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], failures[i] = service.PromoteSynthesisSource(ctx, command) }(i)
	}
	wg.Wait()
	if failures[0] != nil || failures[1] != nil || results[0].Request.ID != results[1].Request.ID || results[0].Replayed == results[1].Replayed {
		t.Fatalf("promotion race %+v %v", results, failures)
	}
	saved := results[0]
	if saved.Request.ID == ordinary.Request.ID {
		t.Fatal("promotion reused an ordinary goal")
	}
	handler := organizinghttp.NewSynthesisHandlerWithGoals(promotionHTTPNotes{}, promotionHTTPProcessing{}, nil, f.store, nil, nil, time.Second, service)
	router := gin.New()
	handler.Routes(router.Group("/api/v1"))
	url := "/api/v1/workspaces/" + string(f.workspace) + "/synthesis/sources/" + string(command.SourceVersionID) + "/promote"
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"goal":"expand scope"}`, http.StatusBadRequest}, {`{}`, http.StatusAccepted}} {
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", command.IdempotencyKey)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("promotion HTTP %d %s", response.Code, response.Body.String())
		}
		if tc.status == http.StatusAccepted {
			var result struct {
				Request         struct{ ID foundation.ID }
				SourceVersionID foundation.ID `json:"source_version_id"`
				Replayed        bool
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Request.ID != saved.Request.ID || !result.Replayed || result.SourceVersionID != command.SourceVersionID {
				t.Fatalf("promotion response %s %v", response.Body.String(), err)
			}
		}
	}
	batch, err := f.store.GetSynthesisGoalCatalogBatch(ctx, f.workspace, saved.Request.ID, 1)
	if err != nil || saved.Request.Status != app.SynthesisGoalCatalogReady || len(batch.Items) != 1 || batch.Items[0].Source != first.Input.SourceEvent.Source || batch.NextAfterSourceID != "" {
		t.Fatalf("promotion lost its single source %+v %v", batch, err)
	}
	if err := f.db.Exec(`UPDATE core.source SET removed_at=clock_timestamp() WHERE id=?`, string(first.Input.SourceEvent.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	replay, err := service.PromoteSynthesisSource(ctx, command)
	if err != nil || !replay.Replayed || replay.Request.ID != saved.Request.ID {
		t.Fatalf("source removal broke receipt replay %+v %v", replay, err)
	}
	changed := command
	changed.SourceVersionID = second.Input.SourceEvent.Source.SourceVersionID
	if _, err := service.PromoteSynthesisSource(ctx, changed); !organizingIntegrationError(err, foundation.ErrorVersionConflict, "SYNTHESIS_GOAL_CONFLICT") {
		t.Fatalf("changed command replayed: %v", err)
	}
	changed = command
	changed.IdempotencyKey = "removed-new-command"
	if _, err := service.PromoteSynthesisSource(ctx, changed); err == nil {
		t.Fatal("removed source promoted again")
	}
	changed = command
	changed.WorkspaceID = f.otherWorkspace
	if _, err := service.PromoteSynthesisSource(ctx, changed); err == nil {
		t.Fatal("cross-workspace promotion accepted")
	}
	f.count(t, "organizing.synthesis_source_promotion", "request_id", string(saved.Request.ID), 1)
	f.count(t, "organizing.synthesis_goal_request", "workspace_id", string(f.workspace), 2)
	f.count(t, "organizing.synthesis_processing", "goal_request_id", string(saved.Request.ID), 0)
}

type promotionHTTPNotes struct {
	organizinghttp.SynthesisService
}
type promotionHTTPProcessing struct {
	organizinghttp.SynthesisProcessingService
}
