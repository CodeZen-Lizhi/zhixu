//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryModelRunCallReplayCASAndUnknownRecovery(t *testing.T) {
	pool, ctx := newAgentRepositoryIntegrationPool(t)
	seedAgentRuntime(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	run := testModelRun(testAgentID(7), testAgentID(5), started)
	created, replayed, err := repository.CreateModelRun(ctx, run)
	if err != nil || replayed || created.ID != run.ID {
		t.Fatalf("CreateModelRun=%#v replayed=%t err=%v", created, replayed, err)
	}
	replay := run
	replay.ID = testAgentID(70)
	replayedRun, replayed, err := repository.CreateModelRun(ctx, replay)
	if err != nil || !replayed || replayedRun.ID != run.ID {
		t.Fatalf("CreateModelRun replay=%#v replayed=%t err=%v", replayedRun, replayed, err)
	}
	conflict := replay
	conflict.Model.ModelVersion = "other-version"
	if _, _, err := repository.CreateModelRun(ctx, conflict); agentErrorCode(err) != ErrorCodeRuntimeReplayConflict {
		t.Fatalf("CreateModelRun conflict code=%s err=%v", agentErrorCode(err), err)
	}

	call := domain.ModelCall{
		ID: testAgentID(8), ModelRunID: run.ID, CallNo: 1, Phase: domain.ModelCallInitial,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema, MaxOutputTokens: 128,
		Status: domain.ModelCallStarted, RequestHash: hash64('a'), RequestBytes: 128,
		Version: 1, StartedAt: started.Add(time.Second),
	}
	startedCall, replayed, err := repository.StartModelCall(ctx, run.WorkspaceID, call)
	if err != nil || replayed || startedCall.ID != call.ID {
		t.Fatalf("StartModelCall=%#v replayed=%t err=%v", startedCall, replayed, err)
	}
	callReplay := call
	callReplay.ID = testAgentID(80)
	replayedCall, replayed, err := repository.StartModelCall(ctx, run.WorkspaceID, callReplay)
	if err != nil || !replayed || replayedCall.ID != call.ID {
		t.Fatalf("StartModelCall replay=%#v replayed=%t err=%v", replayedCall, replayed, err)
	}
	callConflict := callReplay
	callConflict.RequestHash = hash64('b')
	if _, _, err := repository.StartModelCall(ctx, run.WorkspaceID, callConflict); agentErrorCode(err) != ErrorCodeRuntimeReplayConflict {
		t.Fatalf("StartModelCall conflict code=%s err=%v", agentErrorCode(err), err)
	}
	callRefConflict := callReplay
	callRefConflict.Prompt = domain.PromptRef{ID: "other-prompt", Version: "v1"}
	if _, _, err := repository.StartModelCall(ctx, run.WorkspaceID, callRefConflict); agentErrorCode(err) != ErrorCodeRuntimeReplayConflict {
		t.Fatalf("StartModelCall ref conflict code=%s err=%v", agentErrorCode(err), err)
	}

	completedAt := started.Add(2 * time.Second)
	completedCall := call
	completedCall.Status = domain.ModelCallSucceeded
	completedCall.ResponseHash = hash64('c')
	completedCall.ResponseBytes = 64
	completedCall.Usage = domain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	completedCall.LatencyMillis = 20
	completedCall.Version = 2
	completedCall.CompletedAt = &completedAt
	completed, replayed, err := repository.CompleteModelCall(ctx, application.CompleteModelCallCommand{
		WorkspaceID: run.WorkspaceID, ExpectedVersion: 1, Call: completedCall,
	})
	if err != nil || replayed || completed.Status != domain.ModelCallSucceeded {
		t.Fatalf("CompleteModelCall=%#v replayed=%t err=%v", completed, replayed, err)
	}
	completed, replayed, err = repository.CompleteModelCall(ctx, application.CompleteModelCallCommand{
		WorkspaceID: run.WorkspaceID, ExpectedVersion: 1, Call: completedCall,
	})
	if err != nil || !replayed || completed.Version != 2 {
		t.Fatalf("CompleteModelCall replay=%#v replayed=%t err=%v", completed, replayed, err)
	}

	reviewCall := domain.ModelCall{
		ID: testAgentID(81), ModelRunID: run.ID, CallNo: 2, Phase: domain.ModelCallReview,
		Model:           domain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v2", ModelID: "review-model", ModelVersion: "2026-07-19"},
		Profile:         domain.ModelProfileRef{ID: "review", Version: "v2"},
		Prompt:          domain.PromptRef{ID: "faithfulness-review", Version: "v3"},
		Schema:          domain.SchemaRef{ID: domain.FaithfulnessReviewSchemaID, Version: domain.OutputSchemaVersionV1},
		MaxOutputTokens: 64, Status: domain.ModelCallStarted, RequestHash: hash64('d'), RequestBytes: 96,
		Version: 1, StartedAt: started.Add(3 * time.Second),
	}
	if _, replayed, err := repository.StartModelCall(ctx, run.WorkspaceID, reviewCall); err != nil || replayed {
		t.Fatalf("StartModelCall review replayed=%t err=%v", replayed, err)
	}
	reviewCompletedAt := started.Add(4 * time.Second)
	completedReview := reviewCall
	completedReview.Status = domain.ModelCallSucceeded
	completedReview.ResponseHash = hash64('e')
	completedReview.ResponseBytes = 32
	completedReview.Usage = domain.TokenUsage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}
	completedReview.LatencyMillis = 12
	completedReview.Version = 2
	completedReview.CompletedAt = &reviewCompletedAt
	if _, replayed, err := repository.CompleteModelCall(ctx, application.CompleteModelCallCommand{
		WorkspaceID: run.WorkspaceID, ExpectedVersion: 1, Call: completedReview,
	}); err != nil || replayed {
		t.Fatalf("CompleteModelCall review replayed=%t err=%v", replayed, err)
	}

	finalizedAt := started.Add(5 * time.Second)
	finalizedRun := run
	finalizedRun.Status = domain.ModelRunSucceeded
	finalizedRun.FinalResultType = domain.ResultTypeRAGAnswer
	finalizedRun.Version = 2
	finalizedRun.UpdatedAt = finalizedAt
	finalizedRun.CompletedAt = &finalizedAt
	finalized, replayed, err := repository.FinalizeModelRun(ctx, application.FinalizeModelRunCommand{ExpectedVersion: 1, Run: finalizedRun})
	if err != nil || replayed || finalized.Status != domain.ModelRunSucceeded {
		t.Fatalf("FinalizeModelRun=%#v replayed=%t err=%v", finalized, replayed, err)
	}
	finalized, replayed, err = repository.FinalizeModelRun(ctx, application.FinalizeModelRunCommand{ExpectedVersion: 1, Run: finalizedRun})
	if err != nil || !replayed || finalized.Version != 2 {
		t.Fatalf("FinalizeModelRun replay=%#v replayed=%t err=%v", finalized, replayed, err)
	}
	record, err := repository.GetModelRun(ctx, run.WorkspaceID, run.ID)
	if err != nil || record.Run.Status != domain.ModelRunSucceeded || len(record.Calls) != 2 ||
		record.Calls[0].Status != domain.ModelCallSucceeded || record.Calls[1].Status != domain.ModelCallSucceeded {
		t.Fatalf("GetModelRun=%#v err=%v", record, err)
	}
	if generation := record.Calls[0]; generation.Model != run.Model || generation.Profile != run.Profile || generation.Prompt != run.Prompt ||
		generation.Schema != run.Schema || generation.MaxOutputTokens != 128 {
		t.Fatalf("generation runtime refs=%+v", generation)
	}
	if review := record.Calls[1]; review.Phase != domain.ModelCallReview || review.Model != reviewCall.Model ||
		review.Profile != reviewCall.Profile || review.Prompt != reviewCall.Prompt || review.Schema != reviewCall.Schema ||
		review.MaxOutputTokens != reviewCall.MaxOutputTokens {
		t.Fatalf("review runtime refs=%+v", review)
	}
	if _, err := repository.GetModelRun(ctx, testAgentID(2), run.ID); agentErrorCode(err) != ErrorCodeRuntimeNotFound {
		t.Fatalf("cross-workspace code=%s err=%v", agentErrorCode(err), err)
	}

	staleRun := testModelRun(testAgentID(17), testAgentID(15), started.Add(10*time.Second))
	if _, _, err := repository.CreateModelRun(ctx, staleRun); err != nil {
		t.Fatal(err)
	}
	staleCall := domain.ModelCall{
		ID: testAgentID(18), ModelRunID: staleRun.ID, CallNo: 1, Phase: domain.ModelCallInitial,
		Model: staleRun.Model, Profile: staleRun.Profile, Prompt: staleRun.Prompt, Schema: staleRun.Schema, MaxOutputTokens: 32,
		Status: domain.ModelCallStarted, RequestHash: hash64('d'), RequestBytes: 32,
		Version: 1, StartedAt: staleRun.CreatedAt.Add(time.Second),
	}
	if _, _, err := repository.StartModelCall(ctx, staleRun.WorkspaceID, staleCall); err != nil {
		t.Fatal(err)
	}
	recoveryBefore := time.Now().UTC().Add(-time.Minute)
	recoveryAt := time.Now().UTC().Truncate(time.Microsecond)
	unknownCalls, err := repository.MarkStaleModelCallsUnknown(ctx, application.UnknownRecoveryQuery{Before: recoveryBefore, At: recoveryAt, Limit: 10})
	if err != nil || len(unknownCalls) != 1 || unknownCalls[0].ID != staleCall.ID || unknownCalls[0].Status != domain.ModelCallUnknown {
		t.Fatalf("unknown calls=%#v err=%v", unknownCalls, err)
	}
	unknownRuns, err := repository.MarkStaleModelRunsUnknown(ctx, application.UnknownRecoveryQuery{Before: recoveryBefore, At: recoveryAt, Limit: 10})
	if err != nil || len(unknownRuns) != 1 || unknownRuns[0].ID != staleRun.ID || unknownRuns[0].Status != domain.ModelRunUnknown {
		t.Fatalf("unknown runs=%#v err=%v", unknownRuns, err)
	}
	recovered, err := repository.GetModelRun(ctx, staleRun.WorkspaceID, staleRun.ID)
	if err != nil || recovered.Run.FinalErrorCode != ErrorCodeModelRunResultUnknown || recovered.Calls[0].ErrorCode != ErrorCodeModelCallResultUnknown {
		t.Fatalf("recovered record=%#v err=%v", recovered, err)
	}
}

func newAgentRepositoryIntegrationPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for Agent repository integration tests")
	}
	ctx := context.Background()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_agent_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 4, 0)
	if err == nil {
		var runner *platformmigration.Runner
		runner, err = platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
		if err == nil {
			err = runner.Up(ctx)
		}
		migrationPool.Close()
	}
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	return pool, ctx
}

func seedAgentRuntime(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
			VALUES($1,'agent-repository','/tmp/agent-repository','/tmp/agent-repository',now(),'active',now(),now())`, []any{testAgentID(1)}},
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
			VALUES($1,$2,'agent-repository',1,'{"nodes":[]}',now())`, []any{testAgentID(3), testAgentID(1)}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',1,now(),now())`, []any{testAgentID(4), testAgentID(1), testAgentID(3)}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at)
			VALUES($1,$2,'agent-a','agent.relation-assessment','running','{}','worker',now()+interval '5 minutes',1,now(),now()),
			      ($3,$2,'agent-b','agent.relation-assessment','running','{}','worker',now()+interval '5 minutes',1,now(),now())`, []any{testAgentID(5), testAgentID(4), testAgentID(15)}},
		{`INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at)
			VALUES($1,$2,1,1,0,'agent-a','worker',now()+interval '5 minutes','running',now()),
			      ($3,$4,1,1,0,'agent-b','worker',now()+interval '5 minutes','running',now())`, []any{testAgentID(6), testAgentID(5), testAgentID(16), testAgentID(15)}},
		{`INSERT INTO retrieval.index_version(
			id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
			source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at
		) VALUES($1,$2,'simple','v1',repeat('1',64),'{}','agent-repository:index',repeat('2',64),0,
			'agent-repository-index','building','["vector"]',1,now(),now())`, []any{testAgentID(9), testAgentID(1)}},
	}
	for _, query := range queries {
		if _, err := pool.Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func testModelRun(id, nodeID foundation.ID, at time.Time) domain.ModelRun {
	at = at.UTC().Truncate(time.Microsecond)
	attemptID := testAgentID(6)
	if nodeID == testAgentID(15) {
		attemptID = testAgentID(16)
	}
	return domain.ModelRun{
		ID: id, WorkspaceID: testAgentID(1), WorkflowRunID: testAgentID(4), NodeRunID: nodeID, NodeAttemptID: attemptID,
		Model:   domain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "model-test", ModelVersion: "2026-07-01"},
		Profile: domain.ModelProfileRef{ID: "default", Version: "v1"}, Prompt: domain.PromptRef{ID: "rag-answer", Version: "v1"},
		Schema:        domain.SchemaRef{ID: domain.RAGAnswerSchemaID, Version: domain.OutputSchemaVersionV1},
		ReducedSchema: domain.SchemaRef{ID: domain.RefusalSchemaID, Version: domain.OutputSchemaVersionV1},
		Retrieval:     domain.RetrievalRef{IndexVersionID: testAgentID(9)}, Status: domain.ModelRunRunning, Version: 1,
		CreatedAt: at, UpdatedAt: at,
	}
}

func testAgentID(seed int) foundation.ID {
	return foundation.ID(fmt.Sprintf("b0000000-0000-4000-8000-%012d", seed))
}

func hash64(value byte) string {
	result := make([]byte, 64)
	for index := range result {
		result[index] = value
	}
	return string(result)
}

func agentErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
