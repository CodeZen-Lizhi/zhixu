//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConversationAPIVersionsKeepDispatchIdentityAndFilterMixedTurns(t *testing.T) {
	repository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID, otherWorkspaceID := conversationPostgresID(1601), conversationPostgresID(1602)
	conversationID, newConversationID := conversationPostgresID(1603), conversationPostgresID(1604)
	seedConversationWorkspaces(t, ctx, pool, workspaceID, otherWorkspaceID)
	for index, id := range []foundation.ID{conversationID, newConversationID} {
		if _, err := repository.CreateConversation(ctx, conversationCreateRecord(t, workspaceID, id, "API version compatibility", fmt.Sprintf("api-version-conversation-%d", index), time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	legacyDispatcher := newWorkspaceAnalysisQuestionDispatcherIntegration(t, shared)
	currentDispatcher := newAPIVersionV2Dispatcher(t, shared)
	_, coordinator := newWorkspaceAnalysisCancellationHookRuntime(t, shared)

	// Real dispatches and cancellation publication create an interleaved list:
	// RAG, cancelled dynamic v2, cancelled historical v1, pending dynamic v2.
	ragRecord := questionDispatchRecord(t, workspaceID, conversationID, "Legacy RAG", "api-version-rag")
	ragRecord.APIVersion = conversationapplication.APIVersionV1
	oldRecord := workspaceAnalysisQuestionDispatchRecord(t, workspaceID, conversationID, "Historical analysis", "api-version-old")
	oldRecord.APIVersion = conversationapplication.APIVersionV1
	dynamicRecord := workspaceAnalysisQuestionDispatchRecord(t, workspaceID, conversationID, "Dynamic analysis", "api-version-dynamic")
	dynamicRecord.APIVersion = conversationapplication.APIVersionV2
	pendingRecord := workspaceAnalysisQuestionDispatchRecord(t, workspaceID, conversationID, "Pending dynamic analysis", "api-version-dynamic-pending")
	pendingRecord.APIVersion = conversationapplication.APIVersionV2
	records := []conversationapplication.SubmitQuestionRecord{ragRecord, dynamicRecord, oldRecord, pendingRecord}
	dispatched := make([]conversationapplication.SubmitQuestionResult, 0, len(records))
	for index, record := range records {
		dispatcher := currentDispatcher
		if index == 2 {
			dispatcher = legacyDispatcher
		}
		result, err := dispatcher.SubmitQuestion(ctx, record)
		if err != nil {
			t.Fatalf("dispatch %d: %s", index, conversationErrorChain(err))
		}
		dispatched = append(dispatched, result)
		if result.Question.Ordinal != int64(index+1) {
			t.Fatalf("ordinal changed: %#v", result.Question)
		}
		if index < len(records)-1 {
			if _, err := coordinator.Cancel(ctx, workflowapplication.RunControlCommand{WorkflowRunID: result.Workflow.RunID, ExpectedVersion: result.Workflow.Version, IdempotencyKey: fmt.Sprintf("api-version-cancel-%d", index)}); err != nil {
				t.Fatalf("cancel %d: %s", index, conversationErrorChain(err))
			}
		}
	}

	t.Run("mixed lists filter before pagination", func(t *testing.T) {
		counted, counter := countingConversationRepository(t, shared)
		query := conversationapplication.ListTurnsQuery{WorkspaceID: workspaceID, ConversationID: conversationID, Limit: 1, APIVersion: conversationapplication.APIVersionV1}
		first, err := counted.ListTurns(ctx, query)
		if err != nil || len(first.Items) != 1 || first.Items[0].Question.Ordinal != 1 || first.NextCursor == nil || counter.total() > 2 {
			t.Fatalf("first legacy page=%#v queries=%d err=%v", first, counter.total(), err)
		}
		query.Cursor = first.NextCursor
		counter.reset()
		second, err := counted.ListTurns(ctx, query)
		if err != nil || len(second.Items) != 1 || second.Items[0].Question.Ordinal != 3 || second.NextCursor != nil || counter.total() > 2 {
			t.Fatalf("dynamic row consumed legacy page: %#v queries=%d err=%v", second, counter.total(), err)
		}
		query.Cursor, query.Latest = nil, true
		latest, err := counted.ListTurns(ctx, query)
		if err != nil || len(latest.Items) != 1 || latest.Items[0].Question.Ordinal != 3 || latest.NextCursor != nil {
			t.Fatalf("latest legacy turn=%#v err=%v", latest, err)
		}
		query.APIVersion = conversationapplication.APIVersionV2
		latest, err = counted.ListTurns(ctx, query)
		if err != nil || len(latest.Items) != 1 || latest.Items[0].Question.Ordinal != 4 || latest.Items[0].Answer.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending {
			t.Fatalf("v2 latest lost pending dynamic turn: %#v err=%v", latest, err)
		}
		query.Latest, query.Limit = false, 2
		first, err = counted.ListTurns(ctx, query)
		if err != nil || len(first.Items) != 2 || first.Items[0].Question.Ordinal != 1 || first.Items[1].Question.Ordinal != 2 || first.NextCursor == nil {
			t.Fatalf("first v2 page=%#v err=%v", first, err)
		}
		query.Cursor = first.NextCursor
		second, err = counted.ListTurns(ctx, query)
		if err != nil || len(second.Items) != 2 || second.Items[0].Question.Ordinal != 3 || second.Items[1].Question.Ordinal != 4 || second.NextCursor != nil {
			t.Fatalf("second v2 page=%#v err=%v", second, err)
		}
	})

	t.Run("pending and terminal definitions remain authoritative", func(t *testing.T) {
		for _, index := range []int{1, 3} {
			answer, err := repository.GetAnswer(ctx, workspaceID, dispatched[index].Answer.ID)
			if err != nil || answer.Workflow.DefinitionKey != conversationworkflow.WorkspaceAnalysisDefinitionKey || answer.Workflow.DefinitionVersion != 2 {
				t.Fatalf("dynamic definition missing from answer: %#v err=%v", answer.Workflow, err)
			}
			if index == 1 && answer.Answer.ResultType != conversationdomain.AnswerResultWorkspaceAnalysisTermination || index == 3 && answer.Answer.ResultType != "" {
				t.Fatalf("unexpected persisted lifecycle: %#v", answer.Answer)
			}
			query := conversationapplication.WorkspaceAnalysisTimelineQuery{WorkspaceID: workspaceID, AnswerID: answer.Answer.ID, APIVersion: conversationapplication.APIVersionV1}
			_, err = repository.GetWorkspaceAnalysisTimeline(ctx, query)
			requireQuestionDispatchError(t, err, foundation.ErrorVersionConflict, conversationapplication.ErrorCodeAPIVersionUnsupported)
			query.APIVersion = conversationapplication.APIVersionV2
			timeline, err := repository.GetWorkspaceAnalysisTimeline(ctx, query)
			if err != nil || timeline.SchemaVersion != conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV2 {
				t.Fatalf("v2 timeline failed: %#v err=%v", timeline, err)
			}
		}
		query := conversationapplication.WorkspaceAnalysisTimelineQuery{WorkspaceID: workspaceID, AnswerID: dispatched[2].Answer.ID, APIVersion: conversationapplication.APIVersionV1}
		timeline, err := repository.GetWorkspaceAnalysisTimeline(ctx, query)
		// Cancellation immediately after dispatch only has the first persisted
		// node; the API must not fabricate the remaining fixed-plan nodes.
		if err != nil || timeline.SchemaVersion != conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV1 || len(timeline.Items) != 1 ||
			timeline.Items[0].Phase != conversationdomain.WorkspaceAnalysisPhaseInspectWorkspace || timeline.Items[0].Status != conversationdomain.WorkspaceAnalysisTimelineItemCancelled ||
			timeline.Budget.ModelCalls.Max != 3 || timeline.Budget.ToolCalls.Max != 6 {
			t.Fatalf("legacy timeline was rewritten: %#v err=%v", timeline, err)
		}
	})

	runtimeSpy := &apiVersionRuntimeSpy{delegate: currentDispatcher.runtime}
	analysisSpy := &apiVersionAnalysisSpy{delegate: currentDispatcher.analysis}
	idsSpy := &apiVersionIDSpy{delegate: currentDispatcher.ids}
	currentDispatcher.runtime, currentDispatcher.analysis, currentDispatcher.ids = runtimeSpy, analysisSpy, idsSpy
	before := apiVersionDispatchCounts(t, ctx, pool, workspaceID, conversationID)
	t.Run("cross-version replay and new analysis reject before side effects", func(t *testing.T) {
		for _, record := range []conversationapplication.SubmitQuestionRecord{dynamicRecord, pendingRecord} {
			record.APIVersion = conversationapplication.APIVersionV1
			_, err := currentDispatcher.SubmitQuestion(ctx, record)
			requireQuestionDispatchError(t, err, foundation.ErrorVersionConflict, conversationapplication.ErrorCodeAPIVersionUnsupported)
		}
		newRecord := workspaceAnalysisQuestionDispatchRecord(t, workspaceID, newConversationID, "Requires v2", "api-version-new")
		newRecord.APIVersion = conversationapplication.APIVersionV1
		_, err := currentDispatcher.SubmitQuestion(ctx, newRecord)
		requireQuestionDispatchError(t, err, foundation.ErrorVersionConflict, conversationapplication.ErrorCodeAPIVersionUnsupported)
		// The same key with different business input must keep its idempotency conflict.
		conflicting := workspaceAnalysisQuestionDispatchRecord(t, workspaceID, conversationID, "Different question", dynamicRecord.IdempotencyKey)
		conflicting.APIVersion = conversationapplication.APIVersionV1
		_, err = currentDispatcher.SubmitQuestion(ctx, conflicting)
		requireQuestionDispatchError(t, err, foundation.ErrorVersionConflict, ErrorCodeQuestionIdempotencyConflict)
		if runtimeSpy.calls.Load() != 0 || analysisSpy.calls.Load() != 0 || idsSpy.calls.Load() != 0 {
			t.Fatalf("rejected contract invoked side-effect collaborators: runtime=%d analysis=%d ids=%d", runtimeSpy.calls.Load(), analysisSpy.calls.Load(), idsSpy.calls.Load())
		}
		if after := apiVersionDispatchCounts(t, ctx, pool, workspaceID, conversationID); after != before {
			t.Fatalf("rejected contract changed persisted facts: before=%v after=%v", before, after)
		}
		var empty int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.question WHERE conversation_id=$1`, string(newConversationID)).Scan(&empty); err != nil || empty != 0 {
			t.Fatalf("new v1 analysis wrote a Question: count=%d err=%v", empty, err)
		}
	})

	t.Run("v1 replay keeps history under current composition", func(t *testing.T) {
		replayed, err := currentDispatcher.SubmitQuestion(ctx, oldRecord)
		if err != nil || !replayed.Replayed || replayed.Answer.ID != dispatched[2].Answer.ID || replayed.Workflow.RunID != dispatched[2].Workflow.RunID ||
			replayed.Workflow.DefinitionVersion != 1 || replayed.Question.RequestHash != oldRecord.RequestHash || replayed.JobID != dispatched[2].JobID {
			t.Fatalf("current dispatcher changed historical v1 binding: %#v err=%v", replayed, err)
		}
		ragRecord.APIVersion = conversationapplication.APIVersionV2
		ragReplay, err := currentDispatcher.SubmitQuestion(ctx, ragRecord)
		if err != nil || !ragReplay.Replayed || ragReplay.Answer.ID != dispatched[0].Answer.ID || ragReplay.Question.RequestHash != ragRecord.RequestHash {
			t.Fatalf("v2 changed old RAG identity: %#v err=%v", ragReplay, err)
		}
		if after := apiVersionDispatchCounts(t, ctx, pool, workspaceID, conversationID); after != before {
			t.Fatalf("exact replays created new persisted work: before=%v after=%v", before, after)
		}
	})

	t.Run("v1 still creates RAG", func(t *testing.T) {
		record := questionDispatchRecord(t, workspaceID, newConversationID, "Still use evidence Q&A", "api-version-new-rag")
		record.APIVersion = conversationapplication.APIVersionV1
		result, err := currentDispatcher.SubmitQuestion(ctx, record)
		if err != nil || result.Replayed || result.Workflow.DefinitionKey != conversationworkflow.DefinitionKey || result.Workflow.DefinitionVersion != 2 {
			t.Fatalf("legacy RAG was disabled: %#v err=%v", result, err)
		}
	})

	t.Run("workspace isolation precedes version disclosure", func(t *testing.T) {
		_, crossErr := repository.GetAnswer(ctx, otherWorkspaceID, dispatched[3].Answer.ID)
		_, missingErr := repository.GetAnswer(ctx, otherWorkspaceID, conversationPostgresID(1699))
		requireConversationRepositoryError(t, crossErr, foundation.ErrorNotFound, ErrorCodeAnswerNotFound)
		requireConversationRepositoryError(t, missingErr, foundation.ErrorNotFound, ErrorCodeAnswerNotFound)
		_, err := repository.ListTurns(ctx, conversationapplication.ListTurnsQuery{WorkspaceID: otherWorkspaceID, ConversationID: conversationID, Limit: 1, APIVersion: conversationapplication.APIVersionV1})
		requireConversationRepositoryError(t, err, foundation.ErrorNotFound, ErrorCodeConversationNotFound)
		_, err = repository.GetWorkspaceAnalysisTimeline(ctx, conversationapplication.WorkspaceAnalysisTimelineQuery{WorkspaceID: otherWorkspaceID, AnswerID: dispatched[3].Answer.ID, APIVersion: conversationapplication.APIVersionV1})
		requireConversationRepositoryError(t, err, foundation.ErrorNotFound, ErrorCodeAnswerNotFound)
		crossRecord := workspaceAnalysisQuestionDispatchRecord(t, otherWorkspaceID, conversationID, "No disclosure", "api-version-cross-workspace")
		crossRecord.APIVersion = conversationapplication.APIVersionV1
		_, err = currentDispatcher.SubmitQuestion(ctx, crossRecord)
		requireConversationRepositoryError(t, err, foundation.ErrorNotFound, ErrorCodeConversationNotFound)
	})
}

func newAPIVersionV2Dispatcher(t *testing.T, pool *platformpostgres.Pool) *GORMQuestionDispatcher {
	t.Helper()
	runtime, events := newQuestionDispatchDependencies(t, pool)
	repository, err := agentpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotV2()
	if err != nil {
		t.Fatal(err)
	}
	starter, err := agentapplication.NewScopedWorkspaceAnalysisRunServiceV2(repository, foundation.NewUUIDGenerator(nil), agentapplication.WorkspaceAnalysisRunStartConfig{
		DefinitionHash: conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2().GraphHash, ToolCatalogHash: snapshot.Hash, ConfigRevision: 1,
		Timeouts: workspaceAnalysisDispatchTimeouts(snapshot), SynthesisProfileMaxOutputTokens: int(agentdomain.WorkspaceAnalysisV2SynthesisMaxOutputTokens),
		RuntimeLimits: agentapplication.WorkspaceAnalysisRuntimeLimits{RiverJobTimeout: time.Hour, LeaseDuration: 30 * time.Second, HeartbeatInterval: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	audit, _ := newWorkspaceAnalysisAuditIntegration(t, pool)
	dispatcher, err := NewGORMQuestionDispatcherWithWorkspaceAnalysisV2AndAudit(pool, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, starter, audit)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func apiVersionDispatchCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, conversationID foundation.ID) [7]int64 {
	t.Helper()
	var counts [7]int64
	err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.question WHERE workspace_id=$1),
		(SELECT count(*) FROM agent.answer WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.river_job),
		(SELECT count(*) FROM agent.workspace_analysis_run WHERE workspace_id=$1),
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1),
		(SELECT version FROM agent.conversation WHERE workspace_id=$1 AND id=$2)`, string(workspaceID), string(conversationID)).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6])
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

type apiVersionRuntimeSpy struct {
	delegate workflowapplication.ScopedRuntimeStarter
	calls    atomic.Int64
}

func (spy *apiVersionRuntimeSpy) StartScoped(ctx context.Context, scope foundation.TransactionScope, request workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	spy.calls.Add(1)
	return spy.delegate.StartScoped(ctx, scope, request)
}

type apiVersionAnalysisSpy struct {
	delegate agentapplication.ScopedWorkspaceAnalysisRunStarter
	calls    atomic.Int64
}

func (spy *apiVersionAnalysisSpy) StartWorkspaceAnalysisRunScoped(ctx context.Context, scope foundation.TransactionScope, command agentapplication.WorkspaceAnalysisRunStartCommand) (agentdomain.WorkspaceAnalysisRun, error) {
	spy.calls.Add(1)
	return spy.delegate.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
}

type apiVersionIDSpy struct {
	delegate foundation.IDGenerator
	calls    atomic.Int64
}

func (spy *apiVersionIDSpy) New() (foundation.ID, error) {
	spy.calls.Add(1)
	return spy.delegate.New()
}
