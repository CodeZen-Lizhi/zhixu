//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

func TestRepositoryGetModelRunRecordTxUsesCallerTransaction(t *testing.T) {
	for _, variant := range agentCallerTransactionIntegrationVariants() {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			testRepositoryGetModelRunRecordUsesCallerTransaction(t, platform, ctx, variant.open(t, platform, ctx))
		})
	}
}

type agentModelRunRecordReader func(context.Context, foundation.ID, foundation.ID, bool) (application.ModelRunRecord, error)

type agentCallerTransactionIntegrationHarness struct {
	repository  application.ModelRunRepository
	within      func(func(application.ModelRunRepository, agentModelRunRecordReader) error) error
	invalidRead func() error
}

type agentCallerTransactionIntegrationVariant struct {
	name string
	open func(*testing.T, *platformpostgres.Pool, context.Context) agentCallerTransactionIntegrationHarness
}

var errAgentCallerTransactionRollback = errors.New("rollback agent caller transaction integration fixture")

func agentCallerTransactionIntegrationVariants() []agentCallerTransactionIntegrationVariant {
	return []agentCallerTransactionIntegrationVariant{
		{name: "legacy", open: openLegacyAgentCallerTransactionIntegration},
		{name: "gorm", open: openGORMAgentCallerTransactionIntegration},
	}
}

func openLegacyAgentCallerTransactionIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
) agentCallerTransactionIntegrationHarness {
	t.Helper()
	repository, err := NewRepository(platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	return agentCallerTransactionIntegrationHarness{
		repository: repository,
		within: func(work func(application.ModelRunRepository, agentModelRunRecordReader) error) error {
			tx, err := platform.DB().Begin(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			transactionRepository, err := NewRepository(tx)
			if err != nil {
				return err
			}
			return work(transactionRepository, func(
				readCtx context.Context,
				workspaceID foundation.ID,
				runID foundation.ID,
				forUpdate bool,
			) (application.ModelRunRecord, error) {
				return repository.GetModelRunRecordTx(readCtx, tx, workspaceID, runID, forUpdate)
			})
		},
		invalidRead: func() error {
			_, err := repository.GetModelRunRecordTx(ctx, nil, testAgentID(1), testAgentID(87), false)
			return err
		},
	}
}

func openGORMAgentCallerTransactionIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
) agentCallerTransactionIntegrationHarness {
	t.Helper()
	repository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	var staleScope foundation.TransactionScope
	return agentCallerTransactionIntegrationHarness{
		repository: repository,
		within: func(work func(application.ModelRunRepository, agentModelRunRecordReader) error) error {
			err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(
				callbackCtx context.Context,
				scope foundation.TransactionScope,
			) error {
				staleScope = scope
				transaction, err := platformpostgres.GORMTransaction(scope)
				if err != nil {
					return err
				}
				transactionRepository := &GORMRepository{database: transaction, unitOfWork: repository.unitOfWork}
				if err := work(transactionRepository, func(
					readCtx context.Context,
					workspaceID foundation.ID,
					runID foundation.ID,
					forUpdate bool,
				) (application.ModelRunRecord, error) {
					return repository.GetModelRunRecordScoped(readCtx, scope, workspaceID, runID, forUpdate)
				}); err != nil {
					return err
				}
				return errAgentCallerTransactionRollback
			})
			if errors.Is(err, errAgentCallerTransactionRollback) {
				return nil
			}
			return err
		},
		invalidRead: func() error {
			_, err := repository.GetModelRunRecordScoped(ctx, staleScope, testAgentID(1), testAgentID(87), false)
			return err
		},
	}
}

func testRepositoryGetModelRunRecordUsesCallerTransaction(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	harness agentCallerTransactionIntegrationHarness,
) {
	pool := platform.DB()
	seedAgentRuntime(t, ctx, pool)
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	run := testModelRun(testAgentID(87), testAgentID(5), started)
	if _, replayed, err := harness.repository.CreateModelRun(ctx, run); err != nil || replayed {
		t.Fatalf("CreateModelRun replayed=%t err=%v", replayed, err)
	}

	if err := harness.within(func(transactionRepository application.ModelRunRepository, readRecord agentModelRunRecordReader) error {
		first := domain.ModelCall{
			ID: testAgentID(88), ModelRunID: run.ID, CallNo: 1, Phase: domain.ModelCallInitial,
			Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema, MaxOutputTokens: 128,
			Status: domain.ModelCallStarted, RequestHash: hash64('a'), RequestBytes: 64,
			Version: 1, StartedAt: started.Add(time.Second),
		}
		if _, replayed, err := transactionRepository.StartModelCall(ctx, run.WorkspaceID, first); err != nil || replayed {
			return fmt.Errorf("StartModelCall first replayed=%t: %w", replayed, err)
		}
		firstCompletedAt := started.Add(2 * time.Second)
		firstCompleted := first
		firstCompleted.Status = domain.ModelCallSucceeded
		firstCompleted.ResponseHash = hash64('b')
		firstCompleted.ResponseBytes = 32
		firstCompleted.Usage = domain.TokenUsage{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}
		firstCompleted.LatencyMillis = 10
		firstCompleted.Version = 2
		firstCompleted.CompletedAt = &firstCompletedAt
		if _, replayed, err := transactionRepository.CompleteModelCall(ctx, application.CompleteModelCallCommand{
			WorkspaceID: run.WorkspaceID, ExpectedVersion: 1, Call: firstCompleted,
		}); err != nil || replayed {
			return fmt.Errorf("CompleteModelCall first replayed=%t: %w", replayed, err)
		}
		second := domain.ModelCall{
			ID: testAgentID(89), ModelRunID: run.ID, CallNo: 2, Phase: domain.ModelCallReview,
			Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema, MaxOutputTokens: 64,
			Status: domain.ModelCallStarted, RequestHash: hash64('c'), RequestBytes: 48,
			Version: 1, StartedAt: started.Add(3 * time.Second),
		}
		if _, replayed, err := transactionRepository.StartModelCall(ctx, run.WorkspaceID, second); err != nil || replayed {
			return fmt.Errorf("StartModelCall second replayed=%t: %w", replayed, err)
		}
		record, err := readRecord(ctx, run.WorkspaceID, run.ID, true)
		if err != nil || record.Run.ID != run.ID || len(record.Calls) != 2 ||
			record.Calls[0].CallNo != 1 || record.Calls[1].CallNo != 2 {
			return fmt.Errorf("caller transaction record=%#v: %w", record, err)
		}
		outside, err := harness.repository.GetModelRun(ctx, run.WorkspaceID, run.ID)
		if err != nil || len(outside.Calls) != 0 {
			return fmt.Errorf("outside transaction record=%#v: %w", outside, err)
		}
		if _, err := readRecord(ctx, testAgentID(2), run.ID, false); agentErrorCode(err) != ErrorCodeRuntimeNotFound {
			return fmt.Errorf("cross-workspace code=%s: %w", agentErrorCode(err), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	outside, err := harness.repository.GetModelRun(ctx, run.WorkspaceID, run.ID)
	if err != nil || len(outside.Calls) != 0 {
		t.Fatalf("rolled back transaction record=%#v err=%v", outside, err)
	}
	if err := harness.invalidRead(); agentErrorCode(err) != domain.ErrorCodeModelRunInvalid && agentErrorCode(err) != ErrorCodeDatabaseUnavailable {
		t.Fatalf("invalid/stale transaction code=%s err=%v", agentErrorCode(err), err)
	}
}

func TestRepositoryModelRunCallReplayCASAndUnknownRecovery(t *testing.T) {
	testAgentRepositoryIntegrationVariants(t, testRepositoryModelRunCallReplayCASAndUnknownRecovery)
}

func testRepositoryModelRunCallReplayCASAndUnknownRecovery(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository agentRepositoryIntegrationStore,
) {
	pool := platform.DB()
	seedAgentRuntime(t, ctx, pool)
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

func TestRepositoryUnknownRecoveryConcurrentWorkersIntegration(t *testing.T) {
	testAgentRepositoryIntegrationVariants(t, func(
		t *testing.T,
		platform *platformpostgres.Pool,
		ctx context.Context,
		repository agentRepositoryIntegrationStore,
	) {
		pool := platform.DB()
		seedAgentRuntime(t, ctx, pool)
		started := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
		callIDs := make([]foundation.ID, 0, 4)
		runIDs := make([]foundation.ID, 0, 4)
		for index := 0; index < 4; index++ {
			nodeID := testAgentID(120 + index)
			attemptID := testAgentID(130 + index)
			if _, err := pool.Exec(ctx, `
				INSERT INTO workflow.node_run(
					id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at
				) VALUES($1,$2,$3,'agent.relation-assessment','running','{}','worker',now()+interval '5 minutes',1,now(),now())`,
				nodeID, testAgentID(4), fmt.Sprintf("recovery-%d", index)); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
				INSERT INTO workflow.node_attempt(
					id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
				) VALUES($1,$2,1,1,0,$3,'worker',now()+interval '5 minutes','running',now())`,
				attemptID, nodeID, fmt.Sprintf("recovery-%d", index)); err != nil {
				t.Fatal(err)
			}

			run := testModelRun(testAgentID(140+index), nodeID, started.Add(time.Duration(index)*time.Microsecond))
			run.NodeAttemptID = attemptID
			if _, replayed, err := repository.CreateModelRun(ctx, run); err != nil || replayed {
				t.Fatalf("create recovery run %d replayed=%t err=%v", index, replayed, err)
			}
			call := domain.ModelCall{
				ID: testAgentID(150 + index), ModelRunID: run.ID, CallNo: 1, Phase: domain.ModelCallInitial,
				Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema, MaxOutputTokens: 32,
				Status: domain.ModelCallStarted, RequestHash: hash64(byte('a' + index)), RequestBytes: 32,
				Version: 1, StartedAt: run.CreatedAt.Add(time.Second),
			}
			if _, replayed, err := repository.StartModelCall(ctx, run.WorkspaceID, call); err != nil || replayed {
				t.Fatalf("create recovery call %d replayed=%t err=%v", index, replayed, err)
			}
			callIDs = append(callIDs, call.ID)
			runIDs = append(runIDs, run.ID)
		}

		recovery := application.UnknownRecoveryQuery{
			Before: time.Now().UTC().Add(-time.Minute),
			At:     time.Now().UTC().Truncate(time.Microsecond),
			Limit:  2,
		}
		callResults := make(chan []domain.ModelCall, 2)
		callErrors := make(chan error, 2)
		startCalls := make(chan struct{})
		var callWorkers sync.WaitGroup
		for worker := 0; worker < 2; worker++ {
			callWorkers.Add(1)
			go func() {
				defer callWorkers.Done()
				<-startCalls
				calls, err := repository.MarkStaleModelCallsUnknown(ctx, recovery)
				callResults <- calls
				callErrors <- err
			}()
		}
		close(startCalls)
		callWorkers.Wait()
		close(callResults)
		close(callErrors)
		for err := range callErrors {
			if err != nil {
				t.Fatal(err)
			}
		}
		seenCalls := make(map[foundation.ID]struct{}, len(callIDs))
		for calls := range callResults {
			if len(calls) != recovery.Limit {
				t.Fatalf("concurrent recovery calls=%d, want %d", len(calls), recovery.Limit)
			}
			for _, call := range calls {
				if _, exists := seenCalls[call.ID]; exists {
					t.Fatalf("model call %s was recovered by more than one worker", call.ID)
				}
				seenCalls[call.ID] = struct{}{}
			}
		}
		if len(seenCalls) != len(callIDs) {
			t.Fatalf("recovered model calls=%d, want %d", len(seenCalls), len(callIDs))
		}

		runResults := make(chan []domain.ModelRun, 2)
		runErrors := make(chan error, 2)
		startRuns := make(chan struct{})
		var runWorkers sync.WaitGroup
		for worker := 0; worker < 2; worker++ {
			runWorkers.Add(1)
			go func() {
				defer runWorkers.Done()
				<-startRuns
				runs, err := repository.MarkStaleModelRunsUnknown(ctx, recovery)
				runResults <- runs
				runErrors <- err
			}()
		}
		close(startRuns)
		runWorkers.Wait()
		close(runResults)
		close(runErrors)
		for err := range runErrors {
			if err != nil {
				t.Fatal(err)
			}
		}
		seenRuns := make(map[foundation.ID]struct{}, len(runIDs))
		for runs := range runResults {
			if len(runs) != recovery.Limit {
				t.Fatalf("concurrent recovery runs=%d, want %d", len(runs), recovery.Limit)
			}
			for _, run := range runs {
				if _, exists := seenRuns[run.ID]; exists {
					t.Fatalf("model run %s was recovered by more than one worker", run.ID)
				}
				seenRuns[run.ID] = struct{}{}
			}
		}
		if len(seenRuns) != len(runIDs) {
			t.Fatalf("recovered model runs=%d, want %d", len(seenRuns), len(runIDs))
		}
	})
}

func TestRepositoryRAGRunDefersAndAtomicallyBindsRetrieval(t *testing.T) {
	testAgentRepositoryIntegrationVariants(t, testRepositoryRAGRunDefersAndAtomicallyBindsRetrieval)
}

func testRepositoryRAGRunDefersAndAtomicallyBindsRetrieval(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository agentRepositoryIntegrationStore,
) {
	pool := platform.DB()
	seedAgentRuntime(t, ctx, pool)
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	run := testModelRun(testAgentID(27), testAgentID(5), started)
	run.Schema.Version = domain.OutputSchemaVersionV2
	run.Retrieval = domain.RetrievalRef{}
	if _, replayed, err := repository.CreateModelRun(ctx, run); err != nil || replayed {
		t.Fatalf("CreateModelRun replayed=%t err=%v", replayed, err)
	}

	call := domain.ModelCall{
		ID: testAgentID(28), ModelRunID: run.ID, CallNo: 1, Phase: domain.ModelCallInitial,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema, MaxOutputTokens: 128,
		Status: domain.ModelCallStarted, RequestHash: hash64('f'), RequestBytes: 128, Version: 1,
		StartedAt: started.Add(time.Second),
	}
	if _, _, err := repository.StartModelCall(ctx, run.WorkspaceID, call); err != nil {
		t.Fatal(err)
	}
	callCompletedAt := started.Add(2 * time.Second)
	completedCall := call
	completedCall.Status = domain.ModelCallSucceeded
	completedCall.ResponseHash = hash64('e')
	completedCall.ResponseBytes = 64
	completedCall.Usage = domain.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
	completedCall.LatencyMillis = 10
	completedCall.Version = 2
	completedCall.CompletedAt = &callCompletedAt
	if _, _, err := repository.CompleteModelCall(ctx, application.CompleteModelCallCommand{
		WorkspaceID: run.WorkspaceID, ExpectedVersion: 1, Call: completedCall,
	}); err != nil {
		t.Fatal(err)
	}

	finalizedAt := started.Add(3 * time.Second)
	terminal := run
	terminal.Retrieval = domain.RetrievalRef{IndexVersionID: testAgentID(9)}
	terminal.Status = domain.ModelRunSucceeded
	terminal.FinalResultType = domain.ResultTypeRAGAnswer
	terminal.Version = 2
	terminal.UpdatedAt = finalizedAt
	terminal.CompletedAt = &finalizedAt
	finalized, replayed, err := repository.FinalizeModelRun(ctx, application.FinalizeModelRunCommand{ExpectedVersion: 1, Run: terminal})
	if err != nil || replayed || finalized.Retrieval.IndexVersionID != testAgentID(9) {
		t.Fatalf("FinalizeModelRun=%#v replayed=%t err=%v", finalized, replayed, err)
	}
	if finalized, replayed, err = repository.FinalizeModelRun(ctx, application.FinalizeModelRunCommand{ExpectedVersion: 1, Run: terminal}); err != nil || !replayed || finalized.Retrieval.IndexVersionID != testAgentID(9) {
		t.Fatalf("FinalizeModelRun replay=%#v replayed=%t err=%v", finalized, replayed, err)
	}
	replay := run
	replay.ID = testAgentID(29)
	if existing, replayed, err := repository.CreateModelRun(ctx, replay); err != nil || !replayed || existing.ID != run.ID || !existing.Retrieval.IsBound() {
		t.Fatalf("CreateModelRun terminal replay=%#v replayed=%t err=%v", existing, replayed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.model_run SET retrieval_index_version_id=NULL WHERE id=$1`, string(run.ID)); err == nil {
		t.Fatal("bound retrieval tuple was mutable")
	}
}

func TestRepositoryRAGRefusalCallRequirements(t *testing.T) {
	testAgentRepositoryIntegrationVariants(t, testRepositoryRAGRefusalCallRequirements)
}

func TestGORMRepositoryPreservesSQLStateAndCallerContextIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	seedAgentRuntime(t, ctx, platform.DB())
	repository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}

	foreign := testModelRun(testAgentID(111), testAgentID(5), time.Now().UTC().Add(-time.Minute))
	foreign.WorkspaceID = testAgentID(112)
	_, _, err = repository.CreateModelRun(ctx, foreign)
	assertAgentGORMPostgresError(t, err, "23503", foundation.ErrorConsistencyViolation, ErrorCodeRuntimeConsistency)

	cause := errors.New("agent caller cancellation integration cause")
	cancelCtx, cancel := context.WithCancelCause(ctx)
	cancel(cause)
	_, _, err = repository.CreateModelRun(cancelCtx, testModelRun(testAgentID(113), testAgentID(5), time.Now().UTC().Add(-time.Minute)))
	if agentErrorCode(err) != "AGENT_DATABASE_CANCELLED" || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("cancelled GORM operation code=%s err=%v", agentErrorCode(err), err)
	}

	deadlineCause := errors.New("agent caller deadline integration cause")
	deadlineCtx, deadlineCancel := context.WithDeadlineCause(ctx, time.Now().Add(-time.Second), deadlineCause)
	defer deadlineCancel()
	_, err = repository.GetModelRun(deadlineCtx, testAgentID(1), testAgentID(999))
	if agentErrorCode(err) != "AGENT_DATABASE_TIMEOUT" || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, deadlineCause) {
		t.Fatalf("deadline GORM operation code=%s err=%v", agentErrorCode(err), err)
	}
}

func TestGORMRepositoryDeferredConstraintFailureRollsBackIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	config, _, command := seedWorkspaceAnalysisRunStartIntegration(t, ctx, platform.DB())
	_ = config
	repository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(
		callbackCtx context.Context,
		transaction *gorm.DB,
		_ foundation.TransactionScope,
	) error {
		result := transaction.WithContext(callbackCtx).Exec(workspaceAnalysisRunStartAnswerSQL(true), workspaceAnalysisRunStartAnswerArguments(command)...)
		if result.Error != nil {
			return result.Error
		}
		return transaction.WithContext(callbackCtx).Exec(`SET CONSTRAINTS ALL IMMEDIATE`).Error
	})
	assertAgentGORMPostgresError(t, err, "55000", foundation.ErrorConsistencyViolation, ErrorCodeRuntimeConsistency)
	assertWorkspaceAnalysisDispatchCountsIntegration(t, ctx, platform.DB(), command.QuestionID, 0)
}

func TestGORMRepositoryRejectsCorruptModelCallWithoutPartialRecordIntegration(t *testing.T) {
	for _, variant := range agentRepositoryIntegrationVariants() {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			pool := platform.DB()
			seedAgentRuntime(t, ctx, pool)
			repository := variant.open(t, platform)
			run := testModelRun(testAgentID(114), testAgentID(5), time.Now().UTC().Add(-time.Minute))
			if _, _, err := repository.CreateModelRun(ctx, run); err != nil {
				t.Fatal(err)
			}
			call := domain.ModelCall{
				ID: testAgentID(115), ModelRunID: run.ID, CallNo: 1, Phase: domain.ModelCallInitial,
				Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema, MaxOutputTokens: 32,
				Status: domain.ModelCallStarted, RequestHash: hash64('c'), RequestBytes: 32, Version: 1, StartedAt: run.CreatedAt,
			}
			if _, _, err := repository.StartModelCall(ctx, run.WorkspaceID, call); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `ALTER TABLE agent.model_call DISABLE TRIGGER USER`); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `ALTER TABLE agent.model_call DROP CONSTRAINT agent_model_call_lifecycle`); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE agent.model_call SET response_bytes=1 WHERE id=$1`, string(call.ID)); err != nil {
				t.Fatal(err)
			}
			record, err := repository.GetModelRun(ctx, run.WorkspaceID, run.ID)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation ||
				record.Run.ID != "" || len(record.Calls) != 0 {
				t.Fatalf("corrupt model call record=%#v err=%#v", record, err)
			}
		})
	}
}

func TestGORMRepositoryReturnsConnectionsAfterRowsAndTransactionsIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	seedAgentRuntime(t, ctx, platform.DB())
	repository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	run := testModelRun(testAgentID(116), testAgentID(5), time.Now().UTC().Add(-time.Minute))
	if _, _, err := repository.CreateModelRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		if _, err := repository.GetModelRun(ctx, run.WorkspaceID, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	database, err := platform.GORM()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	if stats := sqlDB.Stats(); stats.InUse != 0 {
		t.Fatalf("GORM connections still in use=%d", stats.InUse)
	}
	if acquired := platform.DB().Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("pgx connections still acquired=%d", acquired)
	}
}

func TestAgentTargetQueryPlansUseDeclaredIndexesIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	seedAgentRuntime(t, ctx, platform.DB())
	queries := []struct {
		name   string
		sql    string
		args   []any
		index  string
		nosort bool
	}{
		{name: "model run workspace", sql: `SELECT id FROM agent.model_run WHERE workspace_id=$1 AND id=$2`, args: []any{string(testAgentID(1)), string(testAgentID(1))}, index: "uq_agent_model_run_id_workspace_workflow"},
		{name: "model call history", sql: `SELECT call.id FROM agent.model_call call JOIN agent.model_run run ON run.id=call.model_run_id WHERE run.workspace_id=$1 AND call.model_run_id=$2 ORDER BY call.call_no,call.id`, args: []any{string(testAgentID(1)), string(testAgentID(2))}, index: "idx_agent_model_call_run_history", nosort: true},
		{name: "model call recovery", sql: `SELECT id FROM agent.model_call WHERE status='STARTED' ORDER BY started_at,id LIMIT $1`, args: []any{10}, index: "idx_agent_model_call_status"},
		{name: "analysis operation slot", sql: `SELECT id FROM agent.workspace_analysis_operation WHERE analysis_run_id=$1 AND node_key=$2 AND operation_kind=$3 AND ordinal=$4`, args: []any{string(testAgentID(1)), "retrieve_evidence", "RETRIEVAL_PLAN", 1}, index: "uq_workspace_analysis_operation_key"},
		{name: "analysis model result", sql: `SELECT id FROM agent.workspace_analysis_model_result WHERE analysis_run_id=$1 AND operation_kind=$2 ORDER BY id`, args: []any{string(testAgentID(1)), "RETRIEVAL_PLAN"}, index: "idx_workspace_analysis_model_result_run"},
		{name: "capability readiness", sql: `SELECT worker_instance_id FROM agent.workspace_analysis_worker_capability WHERE definition_key=$1 AND definition_version=$2 AND definition_hash=$3 AND tool_catalog_hash=$4 AND policy_version=$5 AND config_revision=$6 AND released_at IS NULL AND heartbeat_at<=clock_timestamp() AND lease_until>clock_timestamp()`, args: []any{"workspace-analysis", 1, hash64('a'), hash64('b'), 1, 7}, index: "idx_workspace_analysis_worker_capability_ready"},
	}
	tx, err := platform.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	for _, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+query.sql, query.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var planLines []string
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				planLines = append(planLines, line)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(planLines, "\n")
			if !strings.Contains(plan, query.index) || (query.nosort && strings.Contains(plan, "Sort")) {
				t.Fatalf("query plan missing index or has unbounded sort: expected=%s\n%s", query.index, plan)
			}
		})
	}
}

func assertAgentGORMPostgresError(
	t *testing.T,
	err error,
	state string,
	wantKind foundation.ErrorKind,
	wantCode string,
) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected PostgreSQL SQLSTATE %s", state)
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != state {
		t.Fatalf("error=%v, want SQLSTATE %s", err, state)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != wantKind || classified.Code != wantCode {
		t.Fatalf("classified error=%#v, want kind=%s code=%s", err, wantKind, wantCode)
	}
}

func testRepositoryRAGRefusalCallRequirements(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	repository agentRepositoryIntegrationStore,
) {
	pool := platform.DB()
	seedAgentRuntime(t, ctx, pool)
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)

	zeroCallRun := testModelRun(testAgentID(37), testAgentID(5), started)
	zeroCallRun.Schema.Version = domain.OutputSchemaVersionV2
	zeroCallRun.Retrieval = domain.RetrievalRef{}
	if _, _, err := repository.CreateModelRun(ctx, zeroCallRun); err != nil {
		t.Fatal(err)
	}
	refusedAt := started.Add(time.Second)
	zeroCallRefusal := zeroCallRun
	zeroCallRefusal.Status = domain.ModelRunRefused
	zeroCallRefusal.FinalResultType = domain.ResultTypeRefusal
	zeroCallRefusal.FinalErrorCode = "RAG_SCOPE_UNSUPPORTED"
	zeroCallRefusal.Version = 2
	zeroCallRefusal.UpdatedAt = refusedAt
	zeroCallRefusal.CompletedAt = &refusedAt
	if _, replayed, err := repository.FinalizeModelRun(ctx, application.FinalizeModelRunCommand{ExpectedVersion: 1, Run: zeroCallRefusal}); err != nil || replayed {
		t.Fatalf("zero-call refusal replayed=%t err=%v", replayed, err)
	}

	failedCallRun := testModelRun(testAgentID(47), testAgentID(15), started)
	failedCallRun.Schema.Version = domain.OutputSchemaVersionV2
	failedCallRun.Retrieval = domain.RetrievalRef{}
	if _, _, err := repository.CreateModelRun(ctx, failedCallRun); err != nil {
		t.Fatal(err)
	}
	call := domain.ModelCall{
		ID: testAgentID(48), ModelRunID: failedCallRun.ID, CallNo: 1, Phase: domain.ModelCallInitial,
		Model: failedCallRun.Model, Profile: failedCallRun.Profile, Prompt: failedCallRun.Prompt, Schema: failedCallRun.Schema,
		MaxOutputTokens: 128, Status: domain.ModelCallStarted, RequestHash: hash64('9'), RequestBytes: 64,
		Version: 1, StartedAt: started.Add(time.Second),
	}
	if _, _, err := repository.StartModelCall(ctx, failedCallRun.WorkspaceID, call); err != nil {
		t.Fatal(err)
	}
	callFailedAt := started.Add(2 * time.Second)
	failedCall := call
	failedCall.Status = domain.ModelCallFailed
	failedCall.ErrorCode = "MODEL_PROVIDER_REJECTED"
	failedCall.LatencyMillis = 10
	failedCall.Version = 2
	failedCall.CompletedAt = &callFailedAt
	if _, _, err := repository.CompleteModelCall(ctx, application.CompleteModelCallCommand{
		WorkspaceID: failedCallRun.WorkspaceID, ExpectedVersion: 1, Call: failedCall,
	}); err != nil {
		t.Fatal(err)
	}
	failedCallRefusal := failedCallRun
	failedCallRefusal.Status = domain.ModelRunRefused
	failedCallRefusal.FinalResultType = domain.ResultTypeRefusal
	failedCallRefusal.FinalErrorCode = "RAG_SCOPE_UNSUPPORTED"
	failedCallRefusal.Version = 2
	failedCallRefusal.UpdatedAt = callFailedAt.Add(time.Second)
	failedCallRefusal.CompletedAt = &failedCallRefusal.UpdatedAt
	if _, _, err := repository.FinalizeModelRun(ctx, application.FinalizeModelRunCommand{ExpectedVersion: 1, Run: failedCallRefusal}); err == nil {
		t.Fatal("refusal with failed call but no succeeded call was accepted")
	}
}

type agentRepositoryIntegrationStore interface {
	application.ModelRunRepository
	application.RAGMemorySnapshotRepository
}

type agentRepositoryIntegrationVariant struct {
	name string
	open func(*testing.T, *platformpostgres.Pool) agentRepositoryIntegrationStore
}

func agentRepositoryIntegrationVariants() []agentRepositoryIntegrationVariant {
	return []agentRepositoryIntegrationVariant{
		{name: "legacy", open: func(t *testing.T, platform *platformpostgres.Pool) agentRepositoryIntegrationStore {
			t.Helper()
			repository, err := NewRepository(platform.DB())
			if err != nil {
				t.Fatal(err)
			}
			return repository
		}},
		{name: "gorm", open: func(t *testing.T, platform *platformpostgres.Pool) agentRepositoryIntegrationStore {
			t.Helper()
			repository, err := NewGORMRepository(platform)
			if err != nil {
				t.Fatal(err)
			}
			return repository
		}},
	}
}

func testAgentRepositoryIntegrationVariants(
	t *testing.T,
	test func(*testing.T, *platformpostgres.Pool, context.Context, agentRepositoryIntegrationStore),
) {
	t.Helper()
	for _, variant := range agentRepositoryIntegrationVariants() {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			test(t, platform, ctx, variant.open(t, platform))
		})
	}
}

func newAgentRepositoryIntegrationPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	platform, ctx := newAgentPlatformIntegrationPool(t)
	return platform.DB(), ctx
}

func newAgentPlatformIntegrationPool(t *testing.T) (*platformpostgres.Pool, context.Context) {
	t.Helper()
	// 统一使用 TODO 9 共享 Testcontainers 工厂：数据库 provisioning、迁移与
	// 生命周期由 fixture 拥有；legacy 与 GORM Adapter 共享唯一 platform Pool。
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	return fixture.Pool(), context.Background()
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
