//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingspostgres "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/adapter/postgres"
	modelcrypto "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/crypto"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQuestionDispatcherAtomicallyCreatesAndExactlyReplaysQuestionWorkflowAndEvents(t *testing.T) {
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(100)
	conversationID := conversationPostgresID(101)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	createdAt := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Atomic RAG", "atomic-rag-conversation", createdAt,
	)); err != nil {
		t.Fatal(err)
	}
	dispatcher := newQuestionDispatcherIntegration(t, shared)
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

func TestQuestionDispatcherWorkspaceAnalysisAtomicallyCreatesAndReplaysFrozenRun(t *testing.T) {
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(330)
	conversationID := conversationPostgresID(331)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	createdAt := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Workspace analysis", "workspace-analysis-conversation", createdAt,
	)); err != nil {
		t.Fatal(err)
	}
	dispatcher := newWorkspaceAnalysisQuestionDispatcherIntegration(t, shared)
	record := workspaceAnalysisQuestionDispatchRecord(
		t, workspaceID, conversationID, "Inspect the repository and explain the approved evidence.", "workspace-analysis-question-1",
	)

	created, err := dispatcher.SubmitQuestion(ctx, record)
	if err != nil || created.Replayed || created.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending ||
		created.Answer.WorkflowRunID != created.Workflow.RunID || created.NodeRunID == "" || created.JobID < 1 {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			t.Fatalf("SubmitQuestion(workspace analysis)=%#v err=%v cause=%v", created, err, classified.Cause)
		}
		t.Fatalf("SubmitQuestion(workspace analysis)=%#v err=%v", created, err)
	}

	snapshot, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	var mode, definitionKey, definitionHash, toolCatalogHash, nodeKey, nodeType, status string
	var definitionVersion, configRevision, version int64
	var maxNodes, maxModelCalls, maxToolCalls, maxSourceReads, maxConcurrency int
	var maxInputTokens, maxOutputTokens int64
	var runCreatedAt, deadlineAt time.Time
	if err := pool.QueryRow(ctx, `SELECT q.mode,d.key,d.version,wa.definition_hash,wa.tool_catalog_hash,
		wa.config_revision,wa.status,wa.version,wa.created_at,wa.deadline_at,
		wa.max_nodes,wa.max_model_calls,wa.max_tool_calls,wa.max_source_reads,wa.max_tool_concurrency,
		wa.max_input_tokens,wa.max_output_tokens,n.node_key,n.node_type
		FROM agent.question q
		JOIN agent.answer a ON a.question_id=q.id AND a.workspace_id=q.workspace_id
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		JOIN workflow.definition d ON d.id=w.definition_id
		JOIN workflow.node_run n ON n.run_id=w.id
		JOIN agent.workspace_analysis_run wa ON wa.question_id=q.id AND wa.workspace_id=q.workspace_id
		WHERE q.workspace_id=$1 AND q.id=$2`, string(workspaceID), string(created.Question.ID)).Scan(
		&mode, &definitionKey, &definitionVersion, &definitionHash, &toolCatalogHash,
		&configRevision, &status, &version, &runCreatedAt, &deadlineAt,
		&maxNodes, &maxModelCalls, &maxToolCalls, &maxSourceReads, &maxConcurrency,
		&maxInputTokens, &maxOutputTokens, &nodeKey, &nodeType,
	); err != nil {
		t.Fatal(err)
	}
	deadlines, err := agentapplication.DeriveWorkspaceAnalysisV1Deadlines(workspaceAnalysisDispatchTimeouts(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if mode != string(conversationdomain.QuestionModeWorkspaceAnalysis) || definitionKey != definition.Key ||
		definitionVersion != definition.Version || definitionHash != definition.GraphHash || toolCatalogHash != snapshot.Hash ||
		configRevision != 1 || status != "queued" || version != 1 || !deadlineAt.Equal(runCreatedAt.Add(deadlines.RunDeadline())) ||
		maxNodes != agentapplication.WorkspaceAnalysisV1MaxNodes || maxModelCalls != agentapplication.WorkspaceAnalysisV1MaxModelCalls ||
		maxToolCalls != agentapplication.WorkspaceAnalysisV1MaxToolCalls || maxSourceReads != agentapplication.WorkspaceAnalysisV1MaxSourceReads ||
		maxConcurrency != agentapplication.WorkspaceAnalysisV1MaxToolConcurrency || maxInputTokens != agentapplication.WorkspaceAnalysisV1MaxRunInputTokens ||
		maxOutputTokens != agentapplication.WorkspaceAnalysisV1MaxRunOutputTokens ||
		nodeKey != conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace || nodeType != "agent.workspace-analysis.inspect_workspace" {
		t.Fatalf("workspace analysis binding mode=%s definition=%s@%d hashes=%s/%s config=%d status=%s v%d node=%s/%s budget=%d/%d/%d/%d/%d/%d/%d",
			mode, definitionKey, definitionVersion, definitionHash, toolCatalogHash, configRevision, status, version, nodeKey, nodeType,
			maxNodes, maxModelCalls, maxToolCalls, maxSourceReads, maxConcurrency, maxInputTokens, maxOutputTokens)
	}
	var startedEventCount int64
	var startedResourceRef, startedSourceRef, startedSummary string
	if err := pool.QueryRow(ctx, `SELECT
		count(*),min(resource_ref),min(source_event_ref),min(payload_summary::text)
		FROM ops.server_event
		WHERE workspace_id=$1 AND event_type='workspace_analysis.started' AND workflow_run_id=$2`,
		string(workspaceID), string(created.Workflow.RunID),
	).Scan(&startedEventCount, &startedResourceRef, &startedSourceRef, &startedSummary); err != nil {
		t.Fatal(err)
	}
	startedPayload, err := eventsdomain.DecodePayloadSummary([]byte(startedSummary))
	if err != nil {
		t.Fatalf("started event summary=%s err=%v", startedSummary, err)
	}
	if startedEventCount != 1 || startedResourceRef == "" ||
		!strings.HasPrefix(startedSourceRef, "workspace_analysis.started:") ||
		startedPayload.QuestionID == nil || *startedPayload.QuestionID != created.Question.ID ||
		startedPayload.AnswerID == nil || *startedPayload.AnswerID != created.Answer.ID ||
		startedPayload.WorkflowRunID == nil || *startedPayload.WorkflowRunID != created.Workflow.RunID ||
		startedPayload.Status != "queued" ||
		strings.Contains(startedSummary, record.Request.QuestionText) || strings.Contains(startedSummary, "prompt") {
		t.Fatalf("started event count=%d resource=%q source=%q summary=%s payload=%#v",
			startedEventCount, startedResourceRef, startedSourceRef, startedSummary, startedPayload)
	}

	var runInput, nodeInput []byte
	if err := pool.QueryRow(ctx, `SELECT w.input,n.input FROM workflow.run w JOIN workflow.node_run n ON n.run_id=w.id WHERE w.id=$1`,
		string(created.Workflow.RunID)).Scan(&runInput, &nodeInput); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{runInput, nodeInput} {
		input, err := conversationworkflow.DecodeWorkspaceAnalysisInput(raw)
		if err != nil || input.QuestionID != created.Question.ID || input.AnswerID != created.Answer.ID ||
			input.ConversationID != conversationID || strings.Contains(string(raw), record.Request.QuestionText) {
			t.Fatalf("workspace analysis input=%s decoded=%#v err=%v", raw, input, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET status='running',version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND question_id=$2 AND status='queued' AND version=1`,
		string(workspaceID), string(created.Question.ID),
	); err != nil {
		t.Fatal(err)
	}

	replayed, err := dispatcher.SubmitQuestion(ctx, record)
	if err != nil || !replayed.Replayed || replayed.Question.ID != created.Question.ID || replayed.Answer.ID != created.Answer.ID ||
		replayed.Workflow.RunID != created.Workflow.RunID || replayed.NodeRunID != created.NodeRunID || replayed.JobID != created.JobID {
		t.Fatalf("SubmitQuestion(workspace analysis replay)=%#v err=%v", replayed, err)
	}
	var analysisRuns, workflowRuns, jobs int
	var replayStartedEventCount int64
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.workspace_analysis_run WHERE workspace_id=$1 AND question_id=$2),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1 AND id=$3),
		(SELECT count(*) FROM workflow.river_job WHERE id=$4)`,
		string(workspaceID), string(created.Question.ID), string(created.Workflow.RunID), created.JobID,
	).Scan(&analysisRuns, &workflowRuns, &jobs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event
		WHERE workspace_id=$1 AND workflow_run_id=$2 AND event_type='workspace_analysis.started'`,
		string(workspaceID), string(created.Workflow.RunID),
	).Scan(&replayStartedEventCount); err != nil {
		t.Fatal(err)
	}
	if analysisRuns != 1 || workflowRuns != 1 || jobs != 1 || replayStartedEventCount != 1 {
		t.Fatalf("analysis_runs=%d workflow_runs=%d jobs=%d started_events=%d", analysisRuns, workflowRuns, jobs, replayStartedEventCount)
	}

	ragConflict := record
	ragConflict.Request.Mode = conversationdomain.QuestionModeRAG
	ragConflict.RequestHash, err = conversationdomain.ComputeQuestionRequestHash(ragConflict.Request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dispatcher.SubmitQuestion(ctx, ragConflict)
	requireQuestionDispatchError(t, err, foundation.ErrorVersionConflict, ErrorCodeQuestionIdempotencyConflict)
}

func TestQuestionDispatcherWorkspaceAnalysisRunFailureRollsBackEveryDispatchFact(t *testing.T) {
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(340)
	conversationID := conversationPostgresID(341)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Workspace analysis rollback", "workspace-analysis-rollback-conversation", time.Now().UTC(),
	)); err != nil {
		t.Fatal(err)
	}
	runtime, events := newQuestionDispatchDependencies(t, shared)
	dispatcher, err := NewGORMQuestionDispatcherWithWorkspaceAnalysis(
		shared, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, &failingWorkspaceAnalysisRunStarter{},
	)
	if err != nil {
		t.Fatal(err)
	}
	record := workspaceAnalysisQuestionDispatchRecord(t, workspaceID, conversationID, "Inspect atomically", "workspace-analysis-rollback-1")
	_, err = dispatcher.SubmitQuestion(ctx, record)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != agentapplication.ErrorCodeWorkspaceAnalysisRunStartUnavailable {
		t.Fatalf("SubmitQuestion() error=%#v", err)
	}

	var conversationVersion int64
	var questions, answers, definitions, runs, nodes, analysisRuns, outbox, jobs, eventsCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT version FROM agent.conversation WHERE workspace_id=$1 AND id=$2),
		(SELECT count(*) FROM agent.question WHERE workspace_id=$1 AND conversation_id=$2),
		(SELECT count(*) FROM agent.answer WHERE workspace_id=$1 AND conversation_id=$2),
		(SELECT count(*) FROM workflow.definition WHERE workspace_id=$1 AND key='workspace-analysis'),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.node_run n JOIN workflow.run w ON w.id=n.run_id WHERE w.workspace_id=$1),
		(SELECT count(*) FROM agent.workspace_analysis_run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.river_job j JOIN workflow.node_run n ON n.id=(j.args->>'node_run_id')::uuid
		 JOIN workflow.run w ON w.id=n.run_id WHERE w.workspace_id=$1),
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND event_type<>'conversation.created')`,
		string(workspaceID), string(conversationID),
	).Scan(&conversationVersion, &questions, &answers, &definitions, &runs, &nodes, &analysisRuns, &outbox, &jobs, &eventsCount); err != nil {
		t.Fatal(err)
	}
	if conversationVersion != 1 || questions != 0 || answers != 0 || definitions != 0 || runs != 0 || nodes != 0 ||
		analysisRuns != 0 || outbox != 0 || jobs != 0 || eventsCount != 0 {
		t.Fatalf("version=%d questions=%d answers=%d definitions=%d runs=%d nodes=%d analysis=%d outbox=%d jobs=%d events=%d",
			conversationVersion, questions, answers, definitions, runs, nodes, analysisRuns, outbox, jobs, eventsCount)
	}
}

func TestQuestionDispatcherWorkspaceAnalysisAuditFailureRollsBackEveryDispatchFact(t *testing.T) {
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(345)
	conversationID := conversationPostgresID(346)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Workspace analysis audit rollback", "workspace-analysis-audit-rollback-conversation", time.Now().UTC(),
	)); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("injected workspace analysis audit failure")
	dispatcher := newWorkspaceAnalysisQuestionDispatcherAtRevisionAndAuditIntegration(
		t, nil, shared, 1, &workspaceAnalysisAuditCapture{err: cause},
	)
	record := workspaceAnalysisQuestionDispatchRecord(
		t, workspaceID, conversationID, "Inspect atomically with audit", "workspace-analysis-audit-rollback-1",
	)
	if _, err := dispatcher.SubmitQuestion(ctx, record); !errors.Is(err, cause) {
		t.Fatalf("SubmitQuestion() error=%s", conversationErrorChain(err))
	}

	var conversationVersion int64
	var questions, answers, definitions, runs, nodes, analysisRuns, outbox, jobs, eventsCount, auditCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT version FROM agent.conversation WHERE workspace_id=$1 AND id=$2),
		(SELECT count(*) FROM agent.question WHERE workspace_id=$1 AND conversation_id=$2),
		(SELECT count(*) FROM agent.answer WHERE workspace_id=$1 AND conversation_id=$2),
		(SELECT count(*) FROM workflow.definition WHERE workspace_id=$1 AND key='workspace-analysis'),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.node_run n JOIN workflow.run w ON w.id=n.run_id WHERE w.workspace_id=$1),
		(SELECT count(*) FROM agent.workspace_analysis_run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.river_job j JOIN workflow.node_run n ON n.id=(j.args->>'node_run_id')::uuid
		 JOIN workflow.run w ON w.id=n.run_id WHERE w.workspace_id=$1),
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND event_type<>'conversation.created'),
		(SELECT count(*) FROM ops.audit_event WHERE workspace_id=$1)`,
		string(workspaceID), string(conversationID),
	).Scan(
		&conversationVersion, &questions, &answers, &definitions, &runs, &nodes,
		&analysisRuns, &outbox, &jobs, &eventsCount, &auditCount,
	); err != nil {
		t.Fatal(err)
	}
	if conversationVersion != 1 || questions != 0 || answers != 0 || definitions != 0 || runs != 0 || nodes != 0 ||
		analysisRuns != 0 || outbox != 0 || jobs != 0 || eventsCount != 0 || auditCount != 0 {
		t.Fatalf(
			"version=%d questions=%d answers=%d definitions=%d runs=%d nodes=%d analysis=%d outbox=%d jobs=%d events=%d audit=%d",
			conversationVersion, questions, answers, definitions, runs, nodes, analysisRuns, outbox, jobs, eventsCount, auditCount,
		)
	}
}

func TestQuestionDispatcherWorkspaceAnalysisExactlyReplaysCommitResponseLoss(t *testing.T) {
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(350)
	conversationID := conversationPostgresID(351)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Workspace analysis response loss", "workspace-analysis-response-loss-conversation", time.Now().UTC(),
	)); err != nil {
		t.Fatal(err)
	}
	auditRecorder, auditStore := newWorkspaceAnalysisAuditIntegration(t, shared)
	lossDB := &conversationCommitResponseLossDB{UnitOfWork: conversationRepository.uow, loseNext: true}
	dispatcher := newWorkspaceAnalysisQuestionDispatcherAtRevisionAndAuditIntegration(t, lossDB, shared, 1, auditRecorder)
	record := workspaceAnalysisQuestionDispatchRecord(
		t, workspaceID, conversationID, "Inspect after the response is lost", "workspace-analysis-response-loss-1",
	)
	if _, err := dispatcher.SubmitQuestion(ctx, record); err == nil {
		t.Fatal("SubmitQuestion() did not expose the injected commit response loss")
	}

	var questionID, answerID, workflowRunID, nodeRunID, analysisRunID string
	var jobID int64
	if err := pool.QueryRow(ctx, `SELECT q.id::text,a.id::text,a.workflow_run_id::text,n.id::text,j.id,wa.id::text
		FROM agent.question q
		JOIN agent.answer a ON a.question_id=q.id AND a.workspace_id=q.workspace_id
		JOIN workflow.node_run n ON n.run_id=a.workflow_run_id
		JOIN workflow.river_job j ON (j.args->>'node_run_id')::uuid=n.id
		JOIN agent.workspace_analysis_run wa ON wa.question_id=q.id AND wa.workspace_id=q.workspace_id
		WHERE q.workspace_id=$1 AND q.conversation_id=$2 AND q.idempotency_key=$3`,
		string(workspaceID), string(conversationID), record.IdempotencyKey,
	).Scan(&questionID, &answerID, &workflowRunID, &nodeRunID, &jobID, &analysisRunID); err != nil {
		t.Fatal(err)
	}

	replayDispatcher := newWorkspaceAnalysisQuestionDispatcherAtRevisionAndAuditIntegration(t, lossDB, shared, 2, auditRecorder)
	replayed, err := replayDispatcher.SubmitQuestion(ctx, record)
	if err != nil || !replayed.Replayed || string(replayed.Question.ID) != questionID || string(replayed.Answer.ID) != answerID ||
		string(replayed.Workflow.RunID) != workflowRunID || string(replayed.NodeRunID) != nodeRunID || replayed.JobID != jobID {
		t.Fatalf("SubmitQuestion(workspace analysis response-loss replay)=%#v err=%v", replayed, err)
	}
	var analysisRuns int
	var persistedConfigRevision int64
	if err := pool.QueryRow(ctx, `SELECT count(*),max(config_revision) FROM agent.workspace_analysis_run
		WHERE workspace_id=$1 AND question_id=$2 AND id=$3`, string(workspaceID), questionID, analysisRunID).Scan(
		&analysisRuns, &persistedConfigRevision,
	); err != nil {
		t.Fatal(err)
	}
	var startedEvents int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event
		WHERE workspace_id=$1 AND workflow_run_id=$2 AND event_type='workspace_analysis.started'`,
		string(workspaceID), workflowRunID,
	).Scan(&startedEvents); err != nil {
		t.Fatal(err)
	}
	if analysisRuns != 1 || persistedConfigRevision != 1 || startedEvents != 1 {
		t.Fatalf("workspace analysis run count=%d config_revision=%d started_events=%d", analysisRuns, persistedConfigRevision, startedEvents)
	}
	auditEvent := requireWorkspaceAnalysisAuditEventIntegration(
		t, ctx, auditStore, workspaceID, "workspace-analysis:run-started:"+analysisRunID+":v1",
	)
	if auditEvent.WorkspaceID == nil || *auditEvent.WorkspaceID != workspaceID ||
		string(auditEvent.ActorType) != "AGENT" || auditEvent.ActorRef != workspaceAnalysisAuditAgentRef ||
		auditEvent.Action != workspaceAnalysisRunStartedAuditAction || string(auditEvent.Outcome) != "SUCCEEDED" ||
		auditEvent.ErrorCode != "" || auditEvent.ResourceType != workspaceAnalysisAuditResourceType ||
		auditEvent.ResourceRef != "workspace_analysis:"+analysisRunID {
		t.Fatalf("workspace analysis started audit=%#v", auditEvent)
	}
	requireWorkspaceAnalysisAuditJSONObjectIntegration(t, auditEvent.Correlation, map[string]any{
		"analysis_run_id": analysisRunID, "workflow_run_id": workflowRunID,
		"conversation_id": string(conversationID), "question_id": questionID, "answer_id": answerID,
	})
	requireWorkspaceAnalysisAuditJSONObjectIntegration(t, auditEvent.Metadata, map[string]any{
		"definition_key": "workspace-analysis", "definition_version": float64(1),
		"policy_version": float64(1), "config_revision": float64(1), "status": "queued",
	})
	requireWorkspaceAnalysisAuditSafeIntegration(t, auditEvent, record.Request.QuestionText)
}

func TestQuestionDispatcherWorkspaceAnalysisUsesWallClockAfterTransactionDelay(t *testing.T) {
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(360)
	conversationID := conversationPostgresID(361)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Workspace analysis delayed transaction", "workspace-analysis-delayed-conversation",
		time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
	)); err != nil {
		t.Fatal(err)
	}
	delayedDB := &delayedQuestionDispatchDB{UnitOfWork: conversationRepository.uow}
	dispatcher := newWorkspaceAnalysisQuestionDispatcherWithDBIntegration(t, delayedDB, shared)
	created, err := dispatcher.SubmitQuestion(ctx, workspaceAnalysisQuestionDispatchRecord(
		t, workspaceID, conversationID, "Inspect after waiting for the transaction", "workspace-analysis-delayed-1",
	))
	if err != nil {
		t.Fatal(err)
	}
	var deadlineAt time.Time
	if err := pool.QueryRow(ctx, `SELECT deadline_at FROM agent.workspace_analysis_run
		WHERE workspace_id=$1 AND question_id=$2`, string(workspaceID), string(created.Question.ID)).Scan(&deadlineAt); err != nil {
		t.Fatal(err)
	}
	snapshot, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	deadlines, err := agentapplication.DeriveWorkspaceAnalysisV1Deadlines(workspaceAnalysisDispatchTimeouts(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if created.Question.CreatedAt.Sub(delayedDB.transactionStartedAt) < 50*time.Millisecond ||
		!deadlineAt.Equal(created.Question.CreatedAt.Add(deadlines.RunDeadline())) {
		t.Fatalf("transaction_started_at=%s question_created_at=%s deadline_at=%s",
			delayedDB.transactionStartedAt, created.Question.CreatedAt, deadlineAt)
	}
}

func TestQuestionDispatcherRejectsActiveWorkflowAndAllowsQuestionAfterTerminalRun(t *testing.T) {
	for index, terminalStatus := range []workflowdomain.RunStatus{workflowdomain.RunStatusFailed, workflowdomain.RunStatusCancelled} {
		t.Run(string(terminalStatus), func(t *testing.T) {
			conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
			workspaceID := conversationPostgresID(110 + index*10)
			conversationID := conversationPostgresID(111 + index*10)
			seedConversationWorkspaces(t, ctx, pool, workspaceID)
			createdAt := time.Date(2026, 7, 20, 9, 30, 0, 0, time.UTC)
			if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
				t, workspaceID, conversationID, "Terminal recovery", "terminal-recovery-conversation", createdAt,
			)); err != nil {
				t.Fatal(err)
			}
			dispatcher := newQuestionDispatcherIntegration(t, shared)
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
			conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
			workspaceID := conversationPostgresID(120 + index*10)
			conversationID := conversationPostgresID(121 + index*10)
			seedConversationWorkspaces(t, ctx, pool, workspaceID)
			if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
				t, workspaceID, conversationID, "Concurrent dispatch", "concurrent-dispatch-conversation", time.Now().UTC(),
			)); err != nil {
				t.Fatal(err)
			}
			dispatcher := newQuestionDispatcherIntegration(t, shared)
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
			conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
			workspaceID := conversationPostgresID(150 + index*10)
			conversationID := conversationPostgresID(151 + index*10)
			seedConversationWorkspaces(t, ctx, pool, workspaceID)
			if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
				t, workspaceID, conversationID, "Atomic rollback", "atomic-rollback-conversation", time.Now().UTC(),
			)); err != nil {
				t.Fatal(err)
			}
			runtime, events := newQuestionDispatchDependencies(t, shared)
			var runtimePort workflowapplication.ScopedRuntimeStarter = runtime
			var eventPort eventsapplication.ScopedAppender = events
			injected := foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_QUESTION_DISPATCH_FAILURE", true, errors.New("injected question dispatch failure"))
			if test.stage == "runtime" {
				runtimePort = failAfterQuestionRuntime{next: runtime, err: injected}
			} else {
				eventPort = failingConversationEventAppender{err: injected}
			}
			dispatcher, err := NewGORMQuestionDispatcher(
				shared, runtimePort, eventPort, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
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
			conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
			workspaceID := conversationPostgresID(220 + index*10)
			conversationID := conversationPostgresID(221 + index*10)
			seedConversationWorkspaces(t, ctx, pool, workspaceID)
			if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
				t, workspaceID, conversationID, "ID failure rollback", "id-failure-rollback-conversation", time.Now().UTC(),
			)); err != nil {
				t.Fatal(err)
			}
			runtime, events := newQuestionDispatchDependencies(t, shared)
			injected := errors.New("injected ID generation failure")
			ids := &failAtQuestionIDGenerator{next: foundation.NewUUIDGenerator(nil), failAt: test.failAt, err: injected}
			dispatcher, err := NewGORMQuestionDispatcher(
				shared, runtime, events, ids, foundation.SystemClock{},
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
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(250)
	conversationID := conversationPostgresID(251)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Runtime result rollback", "runtime-result-rollback-conversation", time.Now().UTC(),
	)); err != nil {
		t.Fatal(err)
	}
	runtime, events := newQuestionDispatchDependencies(t, shared)
	dispatcher, err := NewGORMQuestionDispatcher(
		shared,
		mutatingQuestionRuntime{
			next: runtime,
			mutate: func(result *workflowapplication.RuntimeStartResult) {
				result.Job.Duplicate = !result.Job.Duplicate
			},
		},
		events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
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
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(180)
	conversationID := conversationPostgresID(181)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Response loss", "question-response-loss-conversation", time.Now().UTC(),
	)); err != nil {
		t.Fatal(err)
	}
	runtime, events := newQuestionDispatchDependencies(t, shared)
	lossDB := &conversationCommitResponseLossDB{UnitOfWork: conversationRepository.uow, loseNext: true}
	dispatcher, err := NewGORMQuestionDispatcher(
		shared, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.uow = lossDB

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
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(260)
	conversationID := conversationPostgresID(261)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Concurrent replay", "concurrent-replay-conversation", time.Now().UTC(),
	)); err != nil {
		t.Fatal(err)
	}
	runtime, events := newQuestionDispatchDependencies(t, shared)
	dispatcher, err := NewGORMQuestionDispatcher(
		shared, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
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
	replayDispatcher, err := NewGORMQuestionDispatcher(
		shared, advancingRuntime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
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
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
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
	dispatcher := newQuestionDispatcherIntegration(t, shared)
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
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
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
	dispatcher := newQuestionDispatcherIntegration(t, shared)
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
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(205)
	conversationID := conversationPostgresID(206)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 20, 10, 30, 0, 0, time.UTC)
	if _, err := conversationRepository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Archived replay", "archived-replay-conversation", now,
	)); err != nil {
		t.Fatal(err)
	}
	dispatcher := newQuestionDispatcherIntegration(t, shared)
	record := questionDispatchRecord(t, workspaceID, conversationID, "Already accepted question", "question-before-archive")
	created, err := dispatcher.SubmitQuestion(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := created.Question.CreatedAt.Add(time.Minute)
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
	conversationRepository, shared, pool, ctx := newConversationTestRepository(t)
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

	dispatcher := newQuestionDispatcherIntegration(t, shared)
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

func newQuestionDispatcherIntegration(t *testing.T, pool *platformpostgres.Pool) *GORMQuestionDispatcher {
	t.Helper()
	runtime, events := newQuestionDispatchDependencies(t, pool)
	dispatcher, err := NewGORMQuestionDispatcher(
		pool, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func newWorkspaceAnalysisQuestionDispatcherIntegration(t *testing.T, pool *platformpostgres.Pool) *GORMQuestionDispatcher {
	return newWorkspaceAnalysisQuestionDispatcherWithDBIntegration(t, nil, pool)
}

func newWorkspaceAnalysisQuestionDispatcherWithDBIntegration(t *testing.T, uow foundation.UnitOfWork, pool *platformpostgres.Pool) *GORMQuestionDispatcher {
	return newWorkspaceAnalysisQuestionDispatcherAtRevisionIntegration(t, uow, pool, 1)
}

func newWorkspaceAnalysisQuestionDispatcherAtRevisionIntegration(t *testing.T, uow foundation.UnitOfWork, pool *platformpostgres.Pool, configRevision int64) *GORMQuestionDispatcher {
	return newWorkspaceAnalysisQuestionDispatcherAtRevisionAndAuditIntegration(t, uow, pool, configRevision, nil)
}

func newWorkspaceAnalysisQuestionDispatcherAtRevisionAndAuditIntegration(
	t *testing.T,
	uow foundation.UnitOfWork,
	pool *platformpostgres.Pool,
	configRevision int64,
	audit ScopedWorkspaceAnalysisAuditRecorder,
) *GORMQuestionDispatcher {
	t.Helper()
	runtime, events := newQuestionDispatchDependencies(t, pool)
	repository, err := agentpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	service, err := agentapplication.NewScopedWorkspaceAnalysisRunService(
		repository,
		foundation.NewUUIDGenerator(nil),
		agentapplication.WorkspaceAnalysisRunStartConfig{
			DefinitionHash:                  conversationworkflow.RegisteredWorkspaceAnalysisDefinition().GraphHash,
			ToolCatalogHash:                 snapshot.Hash,
			ConfigRevision:                  configRevision,
			Timeouts:                        workspaceAnalysisDispatchTimeouts(snapshot),
			SynthesisProfileMaxOutputTokens: int(agentapplication.WorkspaceAnalysisV1SynthesisMaxOutputTokens),
			RuntimeLimits: agentapplication.WorkspaceAnalysisRuntimeLimits{
				RiverJobTimeout: time.Hour, LeaseDuration: 30 * time.Second, HeartbeatInterval: 5 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var dispatcher *GORMQuestionDispatcher
	if isNilInterface(audit) {
		dispatcher, err = NewGORMQuestionDispatcherWithWorkspaceAnalysis(
			pool, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, service,
		)
	} else {
		dispatcher, err = NewGORMQuestionDispatcherWithWorkspaceAnalysisAndAudit(
			pool, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, service, audit,
		)
	}
	if err != nil {
		t.Fatal(err)
	}
	if uow != nil {
		dispatcher.uow = uow
	}
	return dispatcher
}

func workspaceAnalysisDispatchTimeouts(snapshot toolcatalog.WorkspaceAnalysisToolCatalog) agentapplication.WorkspaceAnalysisV1Timeouts {
	return agentapplication.WorkspaceAnalysisV1Timeouts{
		PlanModelTimeout: 2 * time.Minute, SynthesisModelTimeout: 5 * time.Minute, ReviewModelTimeout: 3 * time.Minute,
		GitToolTimeout: snapshot.ReadGitStatusTimeout, SearchToolTimeout: snapshot.SearchKnowledgeTimeout,
		SourceReadToolTimeout: snapshot.ReadSourceTimeout, ValidateCitationToolTimeout: snapshot.ValidateCitationTimeout,
	}
}

func newQuestionDispatchDependencies(t *testing.T, pool *platformpostgres.Pool, requestedHooks ...workflowpostgres.GORMRuntimeRepositoryHooks) (*workflowpostgres.GORMRuntimeRepository, *eventspostgres.GORMStore) {
	t.Helper()
	events, err := eventspostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := modelcrypto.NewSealer(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	audit, err := auditpostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := modelsettingspostgres.NewGORMRepository(pool, modelsettingspostgres.WithGORMSecretSealer(sealer), modelsettingspostgres.WithGORMAuditAppender(audit))
	if err != nil {
		t.Fatal(err)
	}
	hooks := workflowpostgres.GORMRuntimeRepositoryHooks{}
	if len(requestedHooks) > 0 {
		hooks = requestedHooks[0]
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(pool, riveradapter.DefaultOptions(), settings, hooks)
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

func workspaceAnalysisQuestionDispatchRecord(t *testing.T, workspaceID, conversationID foundation.ID, questionText, idempotencyKey string) conversationapplication.SubmitQuestionRecord {
	t.Helper()
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: workspaceID, ConversationID: conversationID, Mode: conversationdomain.QuestionModeWorkspaceAnalysis,
		QuestionText: questionText, Scope: conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
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

type failingWorkspaceAnalysisRunStarter struct{}

func (*failingWorkspaceAnalysisRunStarter) StartWorkspaceAnalysisRunScoped(context.Context, foundation.TransactionScope, agentapplication.WorkspaceAnalysisRunStartCommand) (agentdomain.WorkspaceAnalysisRun, error) {
	return agentdomain.WorkspaceAnalysisRun{}, foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		agentapplication.ErrorCodeWorkspaceAnalysisRunStartUnavailable,
		true,
		errors.New("injected workspace analysis run failure"),
	)
}

type delayedQuestionDispatchDB struct {
	foundation.UnitOfWork
	transactionStartedAt time.Time
}

func (database *delayedQuestionDispatchDB) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	return database.UnitOfWork.Within(ctx, options, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Raw(`SELECT CURRENT_TIMESTAMP`).Row().Scan(&database.transactionStartedAt); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`SELECT pg_sleep(0.075)`).Error; err != nil {
			return err
		}
		return work(ctx, scope)
	})
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
	next   workflowapplication.ScopedRuntimeStarter
	mutate func(*workflowapplication.RuntimeStartResult)
}

func (runtime mutatingQuestionRuntime) StartScoped(ctx context.Context, scope foundation.TransactionScope, request workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	result, err := runtime.next.StartScoped(ctx, scope, request)
	if err == nil && runtime.mutate != nil {
		runtime.mutate(&result)
	}
	return result, err
}

type failAfterQuestionRuntime struct {
	next workflowapplication.ScopedRuntimeStarter
	err  error
}

func (runtime failAfterQuestionRuntime) StartScoped(ctx context.Context, scope foundation.TransactionScope, request workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	if _, err := runtime.next.StartScoped(ctx, scope, request); err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	return workflowapplication.RuntimeStartResult{}, runtime.err
}

type claimBeforeQuestionReplayRuntime struct {
	next      *workflowpostgres.GORMRuntimeRepository
	nodeRunID foundation.ID
	jobID     int64
	claimed   bool
}

func (runtime *claimBeforeQuestionReplayRuntime) StartScoped(ctx context.Context, scope foundation.TransactionScope, request workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
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
	return runtime.next.StartScoped(ctx, scope, request)
}
