//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQuestionDispatcherAtomicallyCreatesAndExactlyReplaysQuestionWorkflowAndEvents(t *testing.T) {
	conversationRepository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(100)
	conversationID := conversationPostgresID(101)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	createdAt := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Atomic RAG", "atomic-rag-conversation", createdAt,
	)); err != nil {
		t.Fatal(err)
	}
	dispatcher := newQuestionDispatcherIntegration(t, pool)
	record := questionDispatchRecord(t, workspaceID, conversationID, "What changed in the approved design?", "question-atomic-1")

	created, err := dispatcher.SubmitQuestion(ctx, record)
	if err != nil || created.Replayed || created.Question.Ordinal != 1 || created.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending ||
		created.Answer.WorkflowRunID != created.Workflow.RunID || created.NodeRunID == "" || created.JobID < 1 {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			t.Fatalf("SubmitQuestion(first) = %#v, %v, cause=%v", created, err, classified.Cause)
		}
		t.Fatalf("SubmitQuestion(first) = %#v, %v", created, err)
	}

	var questionCount, answerCount, runCount, nodeCount, outboxCount, jobCount, pendingEventCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.question WHERE conversation_id=$1),
		(SELECT count(*) FROM agent.answer WHERE conversation_id=$1),
		(SELECT count(*) FROM workflow.run WHERE id=$2),
		(SELECT count(*) FROM workflow.node_run WHERE id=$3),
		(SELECT count(*) FROM workflow.outbox_event WHERE run_id=$2),
		(SELECT count(*) FROM workflow.river_job WHERE id=$4),
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$5 AND event_type='answer.pending' AND conversation_id=$1 AND workflow_run_id=$2)`,
		string(conversationID), string(created.Workflow.RunID), string(created.NodeRunID), created.JobID, string(workspaceID),
	).Scan(&questionCount, &answerCount, &runCount, &nodeCount, &outboxCount, &jobCount, &pendingEventCount); err != nil {
		t.Fatal(err)
	}
	if questionCount != 1 || answerCount != 1 || runCount != 1 || nodeCount != 1 || outboxCount != 1 || jobCount != 1 || pendingEventCount != 1 {
		t.Fatalf("question=%d answer=%d run=%d node=%d outbox=%d job=%d pending_event=%d",
			questionCount, answerCount, runCount, nodeCount, outboxCount, jobCount, pendingEventCount)
	}
	var eventResourceRef, eventSourceRef, eventConversationID, eventWorkflowID, eventQuestionID, eventAnswerID, publicationStatus, eventSummary string
	var eventResourceVersion int64
	if err := pool.QueryRow(ctx, `SELECT resource_ref,source_event_ref,resource_version,
		payload_summary->>'conversation_id',payload_summary->>'workflow_run_id',
		payload_summary->>'question_id',payload_summary->>'answer_id',payload_summary->>'publication_status',payload_summary::text
		FROM ops.server_event
		WHERE workspace_id=$1 AND conversation_id=$2 AND workflow_run_id=$3 AND event_type='answer.pending'`,
		string(workspaceID), string(conversationID), string(created.Workflow.RunID),
	).Scan(&eventResourceRef, &eventSourceRef, &eventResourceVersion, &eventConversationID, &eventWorkflowID,
		&eventQuestionID, &eventAnswerID, &publicationStatus, &eventSummary); err != nil {
		t.Fatal(err)
	}
	if eventResourceRef != "answer:"+string(created.Answer.ID) || eventSourceRef != "answer.pending:"+string(created.Answer.ID)+":v1" ||
		eventResourceVersion != 1 || eventConversationID != string(conversationID) || eventWorkflowID != string(created.Workflow.RunID) ||
		eventQuestionID != string(created.Question.ID) || eventAnswerID != string(created.Answer.ID) || publicationStatus != "pending" ||
		strings.Contains(eventSummary, record.Request.QuestionText) || strings.Contains(eventSummary, "question_text") {
		t.Fatalf("answer.pending event binding = %q %q v%d %q", eventResourceRef, eventSourceRef, eventResourceVersion, eventSummary)
	}
	var runInput, nodeInput []byte
	if err := pool.QueryRow(ctx, `SELECT r.input,n.input FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id WHERE r.id=$1`,
		string(created.Workflow.RunID)).Scan(&runInput, &nodeInput); err != nil {
		t.Fatal(err)
	}
	for _, input := range [][]byte{runInput, nodeInput} {
		decoded, err := conversationworkflow.DecodeInput(input)
		if err != nil || decoded.ConversationID != conversationID || decoded.QuestionID != created.Question.ID ||
			decoded.AnswerID != created.Answer.ID || decoded.QuestionOrdinal != 1 || decoded.ContextHash != created.Question.ContextHash {
			t.Fatalf("persisted workflow input = %s, decoded=%#v, err=%v", input, decoded, err)
		}
		if strings.Contains(string(input), record.Request.QuestionText) {
			t.Fatalf("workflow input leaked question text: %s", input)
		}
	}
	var conversationVersion int64
	if err := pool.QueryRow(ctx, `SELECT version FROM agent.conversation WHERE workspace_id=$1 AND id=$2`,
		string(workspaceID), string(conversationID)).Scan(&conversationVersion); err != nil {
		t.Fatal(err)
	}
	if conversationVersion != 2 {
		t.Fatalf("conversation version = %d", conversationVersion)
	}

	replayed, err := dispatcher.SubmitQuestion(ctx, record)
	if err != nil || !replayed.Replayed || replayed.Question.ID != created.Question.ID || replayed.Answer.ID != created.Answer.ID ||
		replayed.Workflow.RunID != created.Workflow.RunID || replayed.NodeRunID != created.NodeRunID || replayed.JobID != created.JobID {
		t.Fatalf("SubmitQuestion(replay) = %#v, %v", replayed, err)
	}
	conflicting := questionDispatchRecord(t, workspaceID, conversationID, "A different question", record.IdempotencyKey)
	_, err = dispatcher.SubmitQuestion(ctx, conflicting)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorVersionConflict || classified.Code != "CONVERSATION_QUESTION_IDEMPOTENCY_CONFLICT" {
		t.Fatalf("SubmitQuestion(conflict) error = %#v", err)
	}
}

func TestQuestionDispatcherRejectsActiveWorkflowAndAllowsQuestionAfterTerminalRun(t *testing.T) {
	for index, terminalStatus := range []workflowdomain.RunStatus{workflowdomain.RunStatusFailed, workflowdomain.RunStatusCancelled} {
		t.Run(string(terminalStatus), func(t *testing.T) {
			conversationRepository, pool, ctx := newConversationTestRepository(t)
			workspaceID := conversationPostgresID(110 + index*10)
			conversationID := conversationPostgresID(111 + index*10)
			seedConversationWorkspaces(t, ctx, pool, workspaceID)
			createdAt := time.Date(2026, 7, 20, 9, 30, 0, 0, time.UTC)
			if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
				t, workspaceID, conversationID, "Terminal recovery", "terminal-recovery-conversation", createdAt,
			)); err != nil {
				t.Fatal(err)
			}
			dispatcher := newQuestionDispatcherIntegration(t, pool)
			firstRecord := questionDispatchRecord(t, workspaceID, conversationID, "First question", "question-terminal-1")
			first, err := dispatcher.SubmitQuestion(ctx, firstRecord)
			if err != nil {
				t.Fatal(err)
			}

			secondRecord := questionDispatchRecord(t, workspaceID, conversationID, "Second question", "question-terminal-2")
			_, err = dispatcher.SubmitQuestion(ctx, secondRecord)
			requireQuestionDispatchError(t, err, foundation.ErrorVersionConflict, ErrorCodeQuestionActiveWorkflow)

			if _, err := pool.Exec(ctx, `UPDATE workflow.node_run
				SET status=$2,version=version+1,updated_at=CURRENT_TIMESTAMP,completed_at=CURRENT_TIMESTAMP
				WHERE id=$1`, string(first.NodeRunID), string(terminalStatus)); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE workflow.run
				SET status=$2,version=version+1,updated_at=CURRENT_TIMESTAMP,completed_at=CURRENT_TIMESTAMP
				WHERE id=$1`, string(first.Workflow.RunID), string(terminalStatus)); err != nil {
				t.Fatal(err)
			}

			second, err := dispatcher.SubmitQuestion(ctx, secondRecord)
			if err != nil || second.Replayed || second.Question.Ordinal != 2 || second.Question.ContextThroughOrdinal != 0 ||
				second.Answer.WorkflowRunID == first.Answer.WorkflowRunID {
				t.Fatalf("SubmitQuestion(after terminal %s) = %#v, %v", terminalStatus, second, err)
			}
			var questionCount, answerCount int
			if err := pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM agent.question WHERE conversation_id=$1),
				(SELECT count(*) FROM agent.answer WHERE conversation_id=$1)`, string(conversationID)).Scan(&questionCount, &answerCount); err != nil {
				t.Fatal(err)
			}
			if questionCount != 2 || answerCount != 2 {
				t.Fatalf("question_count=%d answer_count=%d", questionCount, answerCount)
			}
		})
	}
}

func TestQuestionDispatcherSerializesConcurrentSameAndDifferentKeys(t *testing.T) {
	tests := []struct {
		name       string
		records    func(t *testing.T, workspaceID, conversationID foundation.ID) []conversationapplication.SubmitQuestionRecord
		wantReplay int
		wantActive int
	}{
		{
			name: "same key exactly replays",
			records: func(t *testing.T, workspaceID, conversationID foundation.ID) []conversationapplication.SubmitQuestionRecord {
				record := questionDispatchRecord(t, workspaceID, conversationID, "Concurrent exact question", "question-concurrent-same")
				return []conversationapplication.SubmitQuestionRecord{record, record}
			},
			wantReplay: 1,
		},
		{
			name: "different keys reject active workflow",
			records: func(t *testing.T, workspaceID, conversationID foundation.ID) []conversationapplication.SubmitQuestionRecord {
				return []conversationapplication.SubmitQuestionRecord{
					questionDispatchRecord(t, workspaceID, conversationID, "Concurrent first question", "question-concurrent-a"),
					questionDispatchRecord(t, workspaceID, conversationID, "Concurrent second question", "question-concurrent-b"),
				}
			},
			wantActive: 1,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conversationRepository, pool, ctx := newConversationTestRepository(t)
			workspaceID := conversationPostgresID(120 + index*10)
			conversationID := conversationPostgresID(121 + index*10)
			seedConversationWorkspaces(t, ctx, pool, workspaceID)
			if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
				t, workspaceID, conversationID, "Concurrent dispatch", "concurrent-dispatch-conversation", time.Now().UTC(),
			)); err != nil {
				t.Fatal(err)
			}
			dispatcher := newQuestionDispatcherIntegration(t, pool)
			records := test.records(t, workspaceID, conversationID)
			type outcome struct {
				result conversationapplication.SubmitQuestionResult
				err    error
			}
			start := make(chan struct{})
			outcomes := make(chan outcome, len(records))
			var wait sync.WaitGroup
			for _, record := range records {
				record := record
				wait.Add(1)
				go func() {
					defer wait.Done()
					<-start
					result, err := dispatcher.SubmitQuestion(ctx, record)
					outcomes <- outcome{result: result, err: err}
				}()
			}
			close(start)
			wait.Wait()
			close(outcomes)

			created, replayed, active := 0, 0, 0
			var binding *conversationapplication.SubmitQuestionResult
			for outcome := range outcomes {
				if outcome.err != nil {
					var classified *foundation.Error
					if errors.As(outcome.err, &classified) && classified.Kind == foundation.ErrorVersionConflict &&
						classified.Code == ErrorCodeQuestionActiveWorkflow {
						active++
						continue
					}
					t.Fatalf("concurrent dispatch error = %#v", outcome.err)
				}
				if outcome.result.Replayed {
					replayed++
				} else {
					created++
				}
				if binding == nil {
					copy := outcome.result
					binding = &copy
				} else if outcome.result.Question.ID != binding.Question.ID || outcome.result.Answer.ID != binding.Answer.ID ||
					outcome.result.Workflow.RunID != binding.Workflow.RunID || outcome.result.NodeRunID != binding.NodeRunID ||
					outcome.result.JobID != binding.JobID {
					t.Fatalf("concurrent exact bindings differ: %#v %#v", *binding, outcome.result)
				}
			}
			if created != 1 || replayed != test.wantReplay || active != test.wantActive {
				t.Fatalf("created=%d replayed=%d active=%d", created, replayed, active)
			}
			var questionCount, answerCount, runCount, nodeCount, jobCount int
			if err := pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM agent.question WHERE conversation_id=$1),
				(SELECT count(*) FROM agent.answer WHERE conversation_id=$1),
				(SELECT count(*) FROM workflow.run r JOIN agent.answer a ON a.workflow_run_id=r.id WHERE a.conversation_id=$1),
				(SELECT count(*) FROM workflow.node_run n JOIN agent.answer a ON a.workflow_run_id=n.run_id WHERE a.conversation_id=$1),
				(SELECT count(*) FROM workflow.river_job j JOIN workflow.node_run n ON n.id=(j.args->>'node_run_id')::uuid
				 JOIN agent.answer a ON a.workflow_run_id=n.run_id WHERE a.conversation_id=$1)`, string(conversationID)).Scan(
				&questionCount, &answerCount, &runCount, &nodeCount, &jobCount,
			); err != nil {
				t.Fatal(err)
			}
			if questionCount != 1 || answerCount != 1 || runCount != 1 || nodeCount != 1 || jobCount != 1 {
				t.Fatalf("question=%d answer=%d run=%d node=%d job=%d", questionCount, answerCount, runCount, nodeCount, jobCount)
			}
		})
	}
}

func TestQuestionDispatcherRollsBackEveryFactAfterRuntimeOrEventFailure(t *testing.T) {
	tests := []struct {
		name  string
		stage string
	}{
		{name: "after workflow and River start", stage: "runtime"},
		{name: "while appending answer pending event", stage: "event"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conversationRepository, pool, ctx := newConversationTestRepository(t)
			workspaceID := conversationPostgresID(150 + index*10)
			conversationID := conversationPostgresID(151 + index*10)
			seedConversationWorkspaces(t, ctx, pool, workspaceID)
			if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
				t, workspaceID, conversationID, "Atomic rollback", "atomic-rollback-conversation", time.Now().UTC(),
			)); err != nil {
				t.Fatal(err)
			}
			runtime, events := newQuestionDispatchDependencies(t, pool)
			var runtimePort questionRuntimeStarter = runtime
			var eventPort eventsapplication.Appender = events
			injected := foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_QUESTION_DISPATCH_FAILURE", true, errors.New("injected question dispatch failure"))
			if test.stage == "runtime" {
				runtimePort = failAfterQuestionRuntime{next: runtime, err: injected}
			} else {
				eventPort = failingConversationEventAppender{err: injected}
			}
			dispatcher, err := NewQuestionDispatcher(
				pool, runtimePort, eventPort, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, conversationworkflow.RegisteredDefinition(),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = dispatcher.SubmitQuestion(ctx, questionDispatchRecord(
				t, workspaceID, conversationID, "Will every fact roll back?", "question-rollback-1",
			))
			requireQuestionDispatchError(t, err, foundation.ErrorDependencyUnavailable, "TEST_QUESTION_DISPATCH_FAILURE")

			var version int64
			var questionCount, answerCount, definitionCount, runCount, nodeCount, outboxCount, jobCount, dispatchEventCount int
			if err := pool.QueryRow(ctx, `SELECT
				(SELECT version FROM agent.conversation WHERE id=$1),
				(SELECT count(*) FROM agent.question WHERE conversation_id=$1),
				(SELECT count(*) FROM agent.answer WHERE conversation_id=$1),
				(SELECT count(*) FROM workflow.definition WHERE workspace_id=$2 AND key=$3),
				(SELECT count(*) FROM workflow.run WHERE workspace_id=$2),
				(SELECT count(*) FROM workflow.node_run n JOIN workflow.run r ON r.id=n.run_id WHERE r.workspace_id=$2),
				(SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$2),
				(SELECT count(*) FROM workflow.river_job),
				(SELECT count(*) FROM ops.server_event WHERE workspace_id=$2 AND event_type<>'conversation.created')`,
				string(conversationID), string(workspaceID), conversationworkflow.DefinitionKey,
			).Scan(&version, &questionCount, &answerCount, &definitionCount, &runCount, &nodeCount, &outboxCount, &jobCount, &dispatchEventCount); err != nil {
				t.Fatal(err)
			}
			if version != 1 || questionCount != 0 || answerCount != 0 || definitionCount != 0 || runCount != 0 ||
				nodeCount != 0 || outboxCount != 0 || jobCount != 0 || dispatchEventCount != 0 {
				t.Fatalf("version=%d question=%d answer=%d definition=%d run=%d node=%d outbox=%d job=%d event=%d",
					version, questionCount, answerCount, definitionCount, runCount, nodeCount, outboxCount, jobCount, dispatchEventCount)
			}
		})
	}
}

func TestQuestionDispatcherClassifiesIDGenerationFailureAndRollsBack(t *testing.T) {
	tests := []struct {
		name   string
		failAt int
	}{
		{name: "question identity", failAt: 1},
		{name: "answer identity", failAt: 2},
		{name: "workflow identity", failAt: 3},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conversationRepository, pool, ctx := newConversationTestRepository(t)
			workspaceID := conversationPostgresID(220 + index*10)
			conversationID := conversationPostgresID(221 + index*10)
			seedConversationWorkspaces(t, ctx, pool, workspaceID)
			if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
				t, workspaceID, conversationID, "ID failure rollback", "id-failure-rollback-conversation", time.Now().UTC(),
			)); err != nil {
				t.Fatal(err)
			}
			runtime, events := newQuestionDispatchDependencies(t, pool)
			injected := errors.New("injected ID generation failure")
			ids := &failAtQuestionIDGenerator{next: foundation.NewUUIDGenerator(nil), failAt: test.failAt, err: injected}
			dispatcher, err := NewQuestionDispatcher(
				pool, runtime, events, ids, foundation.SystemClock{}, conversationworkflow.RegisteredDefinition(),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = dispatcher.SubmitQuestion(ctx, questionDispatchRecord(
				t, workspaceID, conversationID, "ID generation must be atomic", "question-id-failure",
			))
			requireQuestionDispatchError(t, err, foundation.ErrorDependencyUnavailable, ErrorCodeQuestionDispatchUnavailable)
			if !errors.Is(err, injected) {
				t.Fatalf("SubmitQuestion() did not retain ID failure cause: %v", err)
			}
			assertQuestionDispatchHasNoPartialFacts(t, ctx, pool, workspaceID, conversationID)
		})
	}
}

func TestQuestionDispatcherRejectsInvalidRuntimeResultAndRollsBack(t *testing.T) {
	conversationRepository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(250)
	conversationID := conversationPostgresID(251)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Runtime result rollback", "runtime-result-rollback-conversation", time.Now().UTC(),
	)); err != nil {
		t.Fatal(err)
	}
	runtime, events := newQuestionDispatchDependencies(t, pool)
	dispatcher, err := NewQuestionDispatcher(
		pool,
		mutatingQuestionRuntime{
			next: runtime,
			mutate: func(result *workflowapplication.RuntimeStartResult) {
				result.Job.Duplicate = !result.Job.Duplicate
			},
		},
		events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, conversationworkflow.RegisteredDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dispatcher.SubmitQuestion(ctx, questionDispatchRecord(
		t, workspaceID, conversationID, "Reject a drifted runtime receipt", "question-runtime-result-drift",
	))
	requireQuestionDispatchError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeQuestionDispatchCorrupt)
	assertQuestionDispatchHasNoPartialFacts(t, ctx, pool, workspaceID, conversationID)
}

func TestQuestionDispatcherExactlyReplaysCommitResponseLossAfterEventCleanup(t *testing.T) {
	conversationRepository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(180)
	conversationID := conversationPostgresID(181)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Response loss", "question-response-loss-conversation", time.Now().UTC(),
	)); err != nil {
		t.Fatal(err)
	}
	runtime, events := newQuestionDispatchDependencies(t, pool)
	lossDB := &conversationCommitResponseLossDB{Pool: pool, loseNext: true}
	dispatcher, err := NewQuestionDispatcher(
		lossDB, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, conversationworkflow.RegisteredDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	record := questionDispatchRecord(t, workspaceID, conversationID, "Did the transaction commit?", "question-response-loss-1")
	if _, err := dispatcher.SubmitQuestion(ctx, record); err == nil {
		t.Fatal("SubmitQuestion() did not expose the injected commit response loss")
	}

	var questionID, answerID, runID, nodeID string
	var jobID int64
	if err := pool.QueryRow(ctx, `SELECT q.id::text,a.id::text,a.workflow_run_id::text,n.id::text,j.id
		FROM agent.question q
		JOIN agent.answer a ON a.question_id=q.id AND a.workspace_id=q.workspace_id
		JOIN workflow.node_run n ON n.run_id=a.workflow_run_id
		JOIN workflow.river_job j ON (j.args->>'node_run_id')::uuid=n.id
		WHERE q.workspace_id=$1 AND q.conversation_id=$2 AND q.idempotency_key=$3`,
		string(workspaceID), string(conversationID), record.IdempotencyKey,
	).Scan(&questionID, &answerID, &runID, &nodeID, &jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ops.server_event
		WHERE workspace_id=$1 AND (workflow_run_id=$2 OR (conversation_id=$3 AND event_type='answer.pending'))`,
		string(workspaceID), runID, string(conversationID)); err != nil {
		t.Fatal(err)
	}

	replayed, err := dispatcher.SubmitQuestion(ctx, record)
	if err != nil || !replayed.Replayed || string(replayed.Question.ID) != questionID || string(replayed.Answer.ID) != answerID ||
		string(replayed.Workflow.RunID) != runID || string(replayed.NodeRunID) != nodeID || replayed.JobID != jobID {
		t.Fatalf("SubmitQuestion(replay after response loss/event cleanup) = %#v, %v", replayed, err)
	}
	var questionCount, answerCount, runCount, nodeCount, jobCount, cleanedEventCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.question WHERE conversation_id=$1),
		(SELECT count(*) FROM agent.answer WHERE conversation_id=$1),
		(SELECT count(*) FROM workflow.run WHERE id=$2),
		(SELECT count(*) FROM workflow.node_run WHERE id=$3),
		(SELECT count(*) FROM workflow.river_job WHERE id=$4),
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$5 AND
		 (workflow_run_id=$2 OR (conversation_id=$1 AND event_type='answer.pending')))`,
		string(conversationID), runID, nodeID, jobID, string(workspaceID),
	).Scan(&questionCount, &answerCount, &runCount, &nodeCount, &jobCount, &cleanedEventCount); err != nil {
		t.Fatal(err)
	}
	if questionCount != 1 || answerCount != 1 || runCount != 1 || nodeCount != 1 || jobCount != 1 || cleanedEventCount != 0 {
		t.Fatalf("question=%d answer=%d run=%d node=%d job=%d cleaned_event=%d",
			questionCount, answerCount, runCount, nodeCount, jobCount, cleanedEventCount)
	}
}

func TestQuestionDispatcherExactlyReplaysAfterWorkflowAdvancesDuringReplay(t *testing.T) {
	conversationRepository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(260)
	conversationID := conversationPostgresID(261)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Concurrent replay", "concurrent-replay-conversation", time.Now().UTC(),
	)); err != nil {
		t.Fatal(err)
	}
	runtime, events := newQuestionDispatchDependencies(t, pool)
	dispatcher, err := NewQuestionDispatcher(
		pool, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, conversationworkflow.RegisteredDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	record := questionDispatchRecord(t, workspaceID, conversationID, "Can this command replay while the worker starts?", "question-concurrent-replay")
	created, err := dispatcher.SubmitQuestion(ctx, record)
	if err != nil {
		t.Fatal(err)
	}

	advancingRuntime := &claimBeforeQuestionReplayRuntime{
		next: runtime, nodeRunID: created.NodeRunID, jobID: created.JobID,
	}
	replayDispatcher, err := NewQuestionDispatcher(
		pool, advancingRuntime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, conversationworkflow.RegisteredDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := replayDispatcher.SubmitQuestion(ctx, record)
	if err != nil || !advancingRuntime.claimed || !replayed.Replayed || replayed.Question.ID != created.Question.ID ||
		replayed.Answer.ID != created.Answer.ID || replayed.Workflow.RunID != created.Workflow.RunID ||
		replayed.Workflow.Status != workflowdomain.RunStatusRunning || replayed.Workflow.Version <= created.Workflow.Version ||
		replayed.NodeRunID != created.NodeRunID || replayed.JobID != created.JobID {
		t.Fatalf("SubmitQuestion(replay after workflow advance) = %#v, claimed=%t, %v", replayed, advancingRuntime.claimed, err)
	}
	var status string
	var version int64
	var updatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT status,version,updated_at FROM workflow.run WHERE id=$1`,
		string(created.Workflow.RunID)).Scan(&status, &version, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if status != string(replayed.Workflow.Status) || version != replayed.Workflow.Version || !updatedAt.Equal(replayed.Workflow.UpdatedAt) {
		t.Fatalf("persisted workflow = %s v%d %s, replay projection = %#v", status, version, updatedAt, replayed.Workflow)
	}
}

func TestQuestionDispatcherScopesExternalIdempotencyKeyToConversation(t *testing.T) {
	conversationRepository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(190)
	conversationA := conversationPostgresID(191)
	conversationB := conversationPostgresID(192)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Now().UTC()
	for _, record := range []conversationapplication.CreateConversationRecord{
		conversationCreateRecord(t, workspaceID, conversationA, "Conversation A", "shared-key-conversation-a", now),
		conversationCreateRecord(t, workspaceID, conversationB, "Conversation B", "shared-key-conversation-b", now),
	} {
		if _, err := conversationRepository.CreateConversation(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	dispatcher := newQuestionDispatcherIntegration(t, pool)
	first, err := dispatcher.SubmitQuestion(ctx, questionDispatchRecord(
		t, workspaceID, conversationA, "Question in A", "shared-external-question-key",
	))
	if err != nil {
		t.Fatal(err)
	}
	second, err := dispatcher.SubmitQuestion(ctx, questionDispatchRecord(
		t, workspaceID, conversationB, "Question in B", "shared-external-question-key",
	))
	if err != nil || first.Workflow.RunID == second.Workflow.RunID || first.NodeRunID == second.NodeRunID || first.JobID == second.JobID {
		t.Fatalf("cross-conversation dispatch = %#v %#v, %v", first, second, err)
	}
	var questionCount, runCount, jobCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.question WHERE workspace_id=$1 AND idempotency_key=$2),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1 AND idempotency_key IN ($3,$4)),
		(SELECT count(*) FROM workflow.river_job j WHERE (j.args->>'node_run_id')::uuid IN ($5,$6))`,
		string(workspaceID), "shared-external-question-key",
		questionWorkflowIdempotencyKey(first.Question.ID), questionWorkflowIdempotencyKey(second.Question.ID),
		string(first.NodeRunID), string(second.NodeRunID),
	).Scan(&questionCount, &runCount, &jobCount); err != nil {
		t.Fatal(err)
	}
	if questionCount != 2 || runCount != 2 || jobCount != 2 {
		t.Fatalf("question=%d run=%d job=%d", questionCount, runCount, jobCount)
	}
}

func TestQuestionDispatcherHidesCrossWorkspaceAndRejectsArchivedConversation(t *testing.T) {
	conversationRepository, pool, ctx := newConversationTestRepository(t)
	workspaceA := conversationPostgresID(200)
	workspaceB := conversationPostgresID(201)
	conversationID := conversationPostgresID(202)
	seedConversationWorkspaces(t, ctx, pool, workspaceA, workspaceB)
	now := time.Now().UTC()
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceA, conversationID, "Scoped conversation", "scoped-question-conversation", now,
	)); err != nil {
		t.Fatal(err)
	}
	dispatcher := newQuestionDispatcherIntegration(t, pool)
	_, crossWorkspaceErr := dispatcher.SubmitQuestion(ctx, questionDispatchRecord(
		t, workspaceB, conversationID, "Cross workspace", "question-cross-workspace",
	))
	_, missingErr := dispatcher.SubmitQuestion(ctx, questionDispatchRecord(
		t, workspaceA, conversationPostgresID(299), "Missing conversation", "question-missing-conversation",
	))
	requireQuestionDispatchError(t, crossWorkspaceErr, foundation.ErrorNotFound, ErrorCodeConversationNotFound)
	requireQuestionDispatchError(t, missingErr, foundation.ErrorNotFound, ErrorCodeConversationNotFound)

	archivedAt := now.Add(time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE agent.conversation SET
		status='archived',archived_at=$2,last_activity_at=$2,updated_at=$2,version=version+1
		WHERE id=$1`, string(conversationID), archivedAt); err != nil {
		t.Fatal(err)
	}
	_, archivedErr := dispatcher.SubmitQuestion(ctx, questionDispatchRecord(
		t, workspaceA, conversationID, "Archived conversation", "question-archived-conversation",
	))
	requireQuestionDispatchError(t, archivedErr, foundation.ErrorVersionConflict, ErrorCodeQuestionConversationArchived)
	var questionCount, runCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.question WHERE conversation_id=$1),
		(SELECT count(*) FROM workflow.run WHERE workspace_id IN ($2,$3))`,
		string(conversationID), string(workspaceA), string(workspaceB)).Scan(&questionCount, &runCount); err != nil {
		t.Fatal(err)
	}
	if questionCount != 0 || runCount != 0 {
		t.Fatalf("question=%d run=%d", questionCount, runCount)
	}
}

func TestQuestionDispatcherExactlyReplaysExistingQuestionAfterConversationArchived(t *testing.T) {
	conversationRepository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(205)
	conversationID := conversationPostgresID(206)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 20, 10, 30, 0, 0, time.UTC)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Archived replay", "archived-replay-conversation", now,
	)); err != nil {
		t.Fatal(err)
	}
	dispatcher := newQuestionDispatcherIntegration(t, pool)
	record := questionDispatchRecord(t, workspaceID, conversationID, "Already accepted question", "question-before-archive")
	created, err := dispatcher.SubmitQuestion(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := now.Add(time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE agent.conversation SET
		status='archived',archived_at=$2,last_activity_at=$2,updated_at=$2,version=version+1
		WHERE id=$1`, string(conversationID), archivedAt); err != nil {
		t.Fatal(err)
	}

	replayed, err := dispatcher.SubmitQuestion(ctx, record)
	if err != nil || !replayed.Replayed || replayed.Question.ID != created.Question.ID || replayed.Answer.ID != created.Answer.ID ||
		replayed.Workflow.RunID != created.Workflow.RunID || replayed.NodeRunID != created.NodeRunID || replayed.JobID != created.JobID {
		t.Fatalf("SubmitQuestion(archived exact replay) = %#v, %v", replayed, err)
	}
	conflicting := questionDispatchRecord(t, workspaceID, conversationID, "Different archived retry", record.IdempotencyKey)
	_, err = dispatcher.SubmitQuestion(ctx, conflicting)
	requireQuestionDispatchError(t, err, foundation.ErrorVersionConflict, ErrorCodeQuestionIdempotencyConflict)
	_, err = dispatcher.SubmitQuestion(ctx, questionDispatchRecord(
		t, workspaceID, conversationID, "New question after archive", "question-after-archive",
	))
	requireQuestionDispatchError(t, err, foundation.ErrorVersionConflict, ErrorCodeQuestionConversationArchived)

	var questionCount, answerCount, runCount, jobCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.question WHERE conversation_id=$1),
		(SELECT count(*) FROM agent.answer WHERE conversation_id=$1),
		(SELECT count(*) FROM workflow.run r JOIN agent.answer a ON a.workflow_run_id=r.id WHERE a.conversation_id=$1),
		(SELECT count(*) FROM workflow.river_job j JOIN workflow.node_run n ON n.id=(j.args->>'node_run_id')::uuid
		 JOIN agent.answer a ON a.workflow_run_id=n.run_id WHERE a.conversation_id=$1)`, string(conversationID)).Scan(
		&questionCount, &answerCount, &runCount, &jobCount,
	); err != nil {
		t.Fatal(err)
	}
	if questionCount != 1 || answerCount != 1 || runCount != 1 || jobCount != 1 {
		t.Fatalf("question=%d answer=%d run=%d job=%d", questionCount, answerCount, runCount, jobCount)
	}
}

func TestQuestionDispatcherFreezesPublishedContextHashInWorkflowInput(t *testing.T) {
	conversationRepository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(210)
	conversationID := conversationPostgresID(211)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 20, 11, 0, 0, 0, time.UTC)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Frozen context", "frozen-context-conversation", now,
	)); err != nil {
		t.Fatal(err)
	}
	fixture := seedConversationRuntimeBase(t, ctx, pool, workspaceID, conversationID, 8000, now)
	type turnFixture struct {
		ordinal                  int64
		questionText, answerText string
	}
	turns := []turnFixture{
		{ordinal: 1, questionText: "First approved question", answerText: "First approved answer"},
		{ordinal: 2, questionText: "Second approved question", answerText: "Second approved answer"},
	}
	expectedTurns := make([]conversationdomain.PublishedTurn, 0, len(turns))
	for _, turn := range turns {
		at := now.Add(time.Duration(turn.ordinal) * time.Minute)
		result, _ := publishedTurnDocuments(t, fixture, turn.ordinal, conversationdomain.AnswerPublicationCompleted, fixture.modelRunID(turn.ordinal), turn.answerText)
		seedPublishedTurn(t, ctx, pool, fixture, turn.ordinal, conversationdomain.AnswerPublicationCompleted, turn.questionText, turn.answerText, at)
		expectedTurns = append(expectedTurns, conversationdomain.PublishedTurn{
			QuestionID: fixture.questionID(turn.ordinal), AnswerID: fixture.answerID(turn.ordinal), Ordinal: turn.ordinal,
			QuestionText: turn.questionText, AssistantText: turn.answerText,
			ResultType: string(conversationdomain.AnswerResultRAGAnswer), ResultHash: result.Hash,
		})
	}
	expectedHash, expectedThrough, _, err := conversationdomain.ComputeContextHash(expectedTurns)
	if err != nil {
		t.Fatal(err)
	}

	dispatcher := newQuestionDispatcherIntegration(t, pool)
	result, err := dispatcher.SubmitQuestion(ctx, questionDispatchRecord(
		t, workspaceID, conversationID, "Use the frozen conversation context", "question-frozen-context",
	))
	if err != nil || result.Question.Ordinal != 3 || result.Question.ContextThroughOrdinal != expectedThrough ||
		result.Question.ContextHash != expectedHash {
		t.Fatalf("SubmitQuestion(context) = %#v, %v", result, err)
	}
	var runInput, nodeInput []byte
	if err := pool.QueryRow(ctx, `SELECT r.input,n.input
		FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id WHERE r.id=$1`,
		string(result.Workflow.RunID)).Scan(&runInput, &nodeInput); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{runInput, nodeInput} {
		input, err := conversationworkflow.DecodeInput(raw)
		if err != nil || input.ContextHash != expectedHash || input.QuestionOrdinal != 3 ||
			input.QuestionID != result.Question.ID || input.AnswerID != result.Answer.ID {
			t.Fatalf("frozen workflow input=%s decoded=%#v err=%v", raw, input, err)
		}
		for _, body := range []string{"First approved question", "First approved answer", "Use the frozen conversation context"} {
			if strings.Contains(string(raw), body) {
				t.Fatalf("workflow input leaked conversation body %q: %s", body, raw)
			}
		}
	}
}

func newQuestionDispatcherIntegration(t *testing.T, pool *pgxpool.Pool) *QuestionDispatcher {
	t.Helper()
	runtime, events := newQuestionDispatchDependencies(t, pool)
	dispatcher, err := NewQuestionDispatcher(
		pool, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, conversationworkflow.RegisteredDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func newQuestionDispatchDependencies(t *testing.T, pool *pgxpool.Pool) (*workflowpostgres.RuntimeRepository, *eventspostgres.Store) {
	t.Helper()
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, events
}

func questionDispatchRecord(t *testing.T, workspaceID, conversationID foundation.ID, questionText, idempotencyKey string) conversationapplication.SubmitQuestionRecord {
	t.Helper()
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: workspaceID, ConversationID: conversationID, QuestionText: questionText,
		Scope: conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	return conversationapplication.SubmitQuestionRecord{Request: request, IdempotencyKey: idempotencyKey, RequestHash: requestHash}
}

func requireQuestionDispatchError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("dispatch error = %#v, want kind=%s code=%s", err, kind, code)
	}
}

func assertQuestionDispatchHasNoPartialFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, conversationID foundation.ID) {
	t.Helper()
	var version int64
	var questionCount, answerCount, definitionCount, runCount, nodeCount, outboxCount, dispatchEventCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT version FROM agent.conversation WHERE workspace_id=$1 AND id=$2),
		(SELECT count(*) FROM agent.question WHERE conversation_id=$2),
		(SELECT count(*) FROM agent.answer WHERE conversation_id=$2),
		(SELECT count(*) FROM workflow.definition WHERE workspace_id=$1 AND key=$3),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.node_run n JOIN workflow.run r ON r.id=n.run_id WHERE r.workspace_id=$1),
		(SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$1),
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND event_type<>'conversation.created')`,
		string(workspaceID), string(conversationID), conversationworkflow.DefinitionKey,
	).Scan(&version, &questionCount, &answerCount, &definitionCount, &runCount, &nodeCount, &outboxCount, &dispatchEventCount); err != nil {
		t.Fatal(err)
	}
	if version != 1 || questionCount != 0 || answerCount != 0 || definitionCount != 0 || runCount != 0 ||
		nodeCount != 0 || outboxCount != 0 || dispatchEventCount != 0 {
		t.Fatalf("version=%d question=%d answer=%d definition=%d run=%d node=%d outbox=%d event=%d",
			version, questionCount, answerCount, definitionCount, runCount, nodeCount, outboxCount, dispatchEventCount)
	}
}

type failAtQuestionIDGenerator struct {
	next   foundation.IDGenerator
	failAt int
	calls  int
	err    error
}

func (generator *failAtQuestionIDGenerator) New() (foundation.ID, error) {
	generator.calls++
	if generator.calls == generator.failAt {
		return "", generator.err
	}
	return generator.next.New()
}

type mutatingQuestionRuntime struct {
	next   questionRuntimeStarter
	mutate func(*workflowapplication.RuntimeStartResult)
}

func (runtime mutatingQuestionRuntime) StartTx(ctx context.Context, tx pgx.Tx, request workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	result, err := runtime.next.StartTx(ctx, tx, request)
	if err == nil && runtime.mutate != nil {
		runtime.mutate(&result)
	}
	return result, err
}

type failAfterQuestionRuntime struct {
	next questionRuntimeStarter
	err  error
}

func (runtime failAfterQuestionRuntime) StartTx(ctx context.Context, tx pgx.Tx, request workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	if _, err := runtime.next.StartTx(ctx, tx, request); err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	return workflowapplication.RuntimeStartResult{}, runtime.err
}

type claimBeforeQuestionReplayRuntime struct {
	next      *workflowpostgres.RuntimeRepository
	nodeRunID foundation.ID
	jobID     int64
	claimed   bool
}

func (runtime *claimBeforeQuestionReplayRuntime) StartTx(ctx context.Context, tx pgx.Tx, request workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	claimed, err := runtime.next.Claim(ctx, workflowapplication.ClaimCommand{
		NodeRunID: runtime.nodeRunID, DispatchNo: questionDispatchNo, DeliveryID: "question-concurrent-replay-delivery",
		RiverJobID: runtime.jobID, RiverJobAttempt: 1, LeaseOwner: "question-concurrent-replay-worker", LeaseDuration: time.Minute,
	})
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	if claimed.Disposition != workflowapplication.ClaimDispositionClaimed {
		return workflowapplication.RuntimeStartResult{}, errors.New("workflow claim did not advance the replayed run")
	}
	runtime.claimed = true
	return runtime.next.StartTx(ctx, tx, request)
}
