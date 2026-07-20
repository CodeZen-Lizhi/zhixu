//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAnswerFinalizerAtomicallyPublishesRefusalAndExactlyReplaysConcurrently(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	command := fixture.command(finalizerRefusalProposal(fixture.run.ID))

	type outcome struct {
		replayed bool
		err      error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, replayed, err := fixture.finalizer.Finalize(fixture.ctx, command)
			outcomes <- outcome{replayed, err}
		}()
	}
	close(start)
	wait.Wait()
	close(outcomes)
	created, replayed := 0, 0
	for result := range outcomes {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 1 {
		t.Fatalf("created=%d replayed=%d", created, replayed)
	}

	receipt, found, err := fixture.finalizer.Lookup(fixture.ctx, fixture.lookup)
	if err != nil || !found || receipt.AnswerID != fixture.dispatched.Answer.ID || receipt.ResultHash == "" {
		t.Fatalf("Lookup=%#v found=%t err=%v", receipt, found, err)
	}
	var answerStatus, runStatus, runResult, eventType string
	var answerVersion, runVersion, conversationVersion, eventCount int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,m.status,m.final_result_type,a.version,m.version,c.version,
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.refused'),
		(SELECT event_type FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.refused')
		FROM agent.answer a JOIN agent.model_run m ON m.id=a.model_run_id JOIN agent.conversation c ON c.id=a.conversation_id WHERE a.id=$1`,
		string(fixture.dispatched.Answer.ID)).Scan(&answerStatus, &runStatus, &runResult, &answerVersion, &runVersion, &conversationVersion, &eventCount, &eventType); err != nil {
		t.Fatal(err)
	}
	if answerStatus != "refused" || runStatus != "REFUSED" || runResult != "refusal" || answerVersion != 2 || runVersion != 2 || conversationVersion != 3 || eventCount != 1 || eventType != "answer.refused" {
		t.Fatalf("answer=%s run=%s/%s versions=%d/%d/%d event=%d/%s", answerStatus, runStatus, runResult, answerVersion, runVersion, conversationVersion, eventCount, eventType)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM ops.server_event WHERE workspace_id=$1 AND event_type='answer.refused'`, string(fixture.lookup.WorkspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := fixture.finalizer.Finalize(fixture.ctx, command); err != nil || !replay {
		t.Fatalf("retention replay=%t err=%v", replay, err)
	}
	conflict := command
	conflict.Proposal.Refusal.Payload.Summary = "Different refusal."
	if _, _, err := fixture.finalizer.Finalize(fixture.ctx, conflict); finalizerErrorCode(err) != ErrorCodeAnswerFinalizeConflict {
		t.Fatalf("conflict=%v", err)
	}
}

func TestAnswerFinalizerRollsBackModelRunAndAnswerWhenTerminalEventFails(t *testing.T) {
	fixture := newFinalizerFixture(t, true)
	_, _, err := fixture.finalizer.Finalize(fixture.ctx, fixture.command(finalizerClarificationProposal(fixture.run.ID)))
	if finalizerErrorCode(err) != "TEST_FINALIZER_EVENT_FAILURE" {
		t.Fatalf("Finalize error=%v", err)
	}
	var answerStatus, runStatus string
	var answerVersion, runVersion, conversationVersion int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,m.status,a.version,m.version,c.version FROM agent.answer a
		JOIN agent.model_run m ON m.workflow_run_id=a.workflow_run_id JOIN agent.conversation c ON c.id=a.conversation_id WHERE a.id=$1`, string(fixture.dispatched.Answer.ID)).Scan(&answerStatus, &runStatus, &answerVersion, &runVersion, &conversationVersion); err != nil {
		t.Fatal(err)
	}
	if answerStatus != "pending" || runStatus != "RUNNING" || answerVersion != 1 || runVersion != 1 || conversationVersion != 2 {
		t.Fatalf("rollback=%s/%s v%d/%d/%d", answerStatus, runStatus, answerVersion, runVersion, conversationVersion)
	}
}

func TestAnswerFinalizerPublishesCompletedAnswerWithDeferredRetrievalBinding(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	proposal := finalizerCompletedProposal(fixture.run.ID, fixture.lookup.WorkspaceID)
	indexID := proposal.Retrieval.IndexVersionID
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,
		expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,source_manifest_hash,
		expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version)
		VALUES($1,$2,'default','v1',$3,'{}','finalizer',$4,1,'finalizer-index','building','["vector"]',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,$5,1,'goldmark','v1',$3,'chunk-v1','schema-v1')`,
		string(indexID), string(fixture.lookup.WorkspaceID), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"); err != nil {
		t.Fatal(err)
	}
	receipt, replayed, err := fixture.finalizer.Finalize(fixture.ctx, fixture.command(proposal))
	if err != nil || replayed || receipt.PublicationStatus != "completed" {
		t.Fatalf("Finalize=%#v replay=%t err=%v", receipt, replayed, err)
	}
	var answerStatus, runStatus, persistedIndex string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,m.status,m.retrieval_index_version_id::text FROM agent.answer a JOIN agent.model_run m ON m.id=a.model_run_id WHERE a.id=$1`, string(fixture.dispatched.Answer.ID)).Scan(&answerStatus, &runStatus, &persistedIndex); err != nil {
		t.Fatal(err)
	}
	if answerStatus != "completed" || runStatus != "SUCCEEDED" || persistedIndex != string(indexID) {
		t.Fatalf("completed=%s/%s index=%s", answerStatus, runStatus, persistedIndex)
	}
}

func TestAnswerFinalizerPublishesClarification(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	receipt, replayed, err := fixture.finalizer.Finalize(fixture.ctx, fixture.command(finalizerClarificationProposal(fixture.run.ID)))
	if err != nil || replayed || receipt.PublicationStatus != "clarification_required" || receipt.ResultType != "clarification" {
		t.Fatalf("Finalize=%#v replay=%t err=%v", receipt, replayed, err)
	}
	var answerStatus, runStatus, runResult string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,m.status,m.final_result_type FROM agent.answer a JOIN agent.model_run m ON m.id=a.model_run_id WHERE a.id=$1`, string(fixture.dispatched.Answer.ID)).Scan(&answerStatus, &runStatus, &runResult); err != nil {
		t.Fatal(err)
	}
	if answerStatus != "clarification_required" || runStatus != "SUCCEEDED" || runResult != "clarification" {
		t.Fatalf("clarification=%s/%s/%s", answerStatus, runStatus, runResult)
	}
}

func TestAnswerFinalizerFailsClosedForWorkspaceAndAttemptBinding(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	wrongWorkspace := fixture.lookup
	wrongWorkspace.WorkspaceID = conversationPostgresID(899)
	_, foundWorkspace, workspaceErr := fixture.finalizer.Lookup(fixture.ctx, wrongWorkspace)
	wrongAttempt := fixture.lookup
	wrongAttempt.NodeAttemptID = conversationPostgresID(898)
	_, foundAttempt, attemptErr := fixture.finalizer.Lookup(fixture.ctx, wrongAttempt)
	if foundWorkspace || foundAttempt || finalizerErrorCode(workspaceErr) != ErrorCodeAnswerNotFound || finalizerErrorCode(attemptErr) != ErrorCodeAnswerNotFound {
		t.Fatalf("workspace found=%t err=%v attempt found=%t err=%v", foundWorkspace, workspaceErr, foundAttempt, attemptErr)
	}
}

func TestAnswerFinalizerLookupDetectsTerminalModelRunWithPendingAnswer(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	repository, err := agentpostgres.NewRepository(fixture.pool)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := fixture.run.UpdatedAt.Add(20 * time.Second)
	terminal := fixture.run
	terminal.Status = agentdomain.ModelRunSucceeded
	terminal.FinalResultType = agentdomain.ResultTypeClarification
	terminal.Version = 2
	terminal.UpdatedAt = completedAt
	terminal.CompletedAt = &completedAt
	if _, replayed, err := repository.FinalizeModelRun(fixture.ctx, agentapplication.FinalizeModelRunCommand{ExpectedVersion: 1, Run: terminal}); err != nil || replayed {
		t.Fatalf("split setup replay=%t err=%v", replayed, err)
	}
	_, found, err := fixture.finalizer.Lookup(fixture.ctx, fixture.lookup)
	if found || finalizerErrorCode(err) != ErrorCodeAnswerFinalizeCorrupt {
		t.Fatalf("Lookup found=%t err=%v", found, err)
	}
}

func TestAnswerFinalizerRecoversExactReplayAfterCommitResponseLoss(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	agentRepository, err := agentpostgres.NewRepository(fixture.pool)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewStore(fixture.pool)
	if err != nil {
		t.Fatal(err)
	}
	lossDB := &conversationCommitResponseLossDB{Pool: fixture.pool, loseNext: true}
	finalizer, err := NewAnswerFinalizer(lossDB, agentRepository, events, foundation.FixedClock{Value: fixture.run.CreatedAt.Add(30 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	command := fixture.command(finalizerRefusalProposal(fixture.run.ID))
	if _, _, err := finalizer.Finalize(fixture.ctx, command); finalizerErrorCode(err) != conversationapplication.ErrorCodeAnswerFinalizationUnknown {
		t.Fatalf("Finalize commit response loss code=%q err=%v", finalizerErrorCode(err), err)
	}
	receipt, replayed, err := finalizer.Finalize(fixture.ctx, command)
	if err != nil || !replayed || receipt.ResultHash == "" {
		t.Fatalf("replay=%#v replayed=%t err=%v", receipt, replayed, err)
	}
	var answerVersion, runVersion, eventCount int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.version,m.version,(SELECT count(*) FROM ops.server_event e WHERE e.resource_ref='answer:'||a.id::text AND e.event_type='answer.refused') FROM agent.answer a JOIN agent.model_run m ON m.id=a.model_run_id WHERE a.id=$1`, string(fixture.dispatched.Answer.ID)).Scan(&answerVersion, &runVersion, &eventCount); err != nil {
		t.Fatal(err)
	}
	if answerVersion != 2 || runVersion != 2 || eventCount != 1 {
		t.Fatalf("versions=%d/%d events=%d", answerVersion, runVersion, eventCount)
	}
}

type finalizerFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	finalizer  *AnswerFinalizer
	lookup     conversationapplication.AnswerPublicationLookup
	dispatched conversationapplication.SubmitQuestionResult
	run        agentdomain.ModelRun
}

func newFinalizerFixture(t *testing.T, failingEvent bool) finalizerFixture {
	t.Helper()
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID, conversationID := conversationPostgresID(800), conversationPostgresID(801)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := repository.CreateConversation(ctx, conversationCreateRecord(t, workspaceID, conversationID, "Finalizer", "finalizer", createdAt)); err != nil {
		t.Fatal(err)
	}
	dispatched, err := newQuestionDispatcherIntegration(t, pool).SubmitQuestion(ctx, questionDispatchRecord(t, workspaceID, conversationID, "Need evidence?", "finalizer-question"))
	if err != nil {
		t.Fatal(err)
	}
	attemptID := conversationPostgresID(802)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at)
		VALUES($1,$2,1,1,0,'finalizer-delivery','finalizer-worker',CURRENT_TIMESTAMP+INTERVAL '5 minutes','running',$3)`, string(attemptID), string(dispatched.NodeRunID), createdAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	lookup := conversationapplication.AnswerPublicationLookup{WorkspaceID: workspaceID, WorkflowRunID: dispatched.Workflow.RunID, NodeRunID: dispatched.NodeRunID,
		NodeAttemptID: attemptID, ConversationID: conversationID, QuestionID: dispatched.Question.ID, AnswerID: dispatched.Answer.ID}
	run := finalizerTestRun(lookup, createdAt.Add(2*time.Second))
	run.ID = conversationPostgresID(803)
	agentRepository, err := agentpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, replay, err := agentRepository.CreateModelRun(ctx, run); err != nil || replay {
		t.Fatalf("CreateModelRun replay=%t err=%v", replay, err)
	}
	call := agentdomain.ModelCall{ID: conversationPostgresID(804), ModelRunID: run.ID, CallNo: 1, Phase: agentdomain.ModelCallPlan,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		MaxOutputTokens: 64, Status: agentdomain.ModelCallStarted, RequestHash: string(make([]byte, 64)), RequestBytes: 10, Version: 1, StartedAt: createdAt.Add(3 * time.Second)}
	call.RequestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, _, err := agentRepository.StartModelCall(ctx, workspaceID, call); err != nil {
		t.Fatal(err)
	}
	completedAt := createdAt.Add(4 * time.Second)
	call.Status = agentdomain.ModelCallSucceeded
	call.ResponseHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	call.ResponseBytes = 10
	call.Usage = agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	call.Version = 2
	call.CompletedAt = &completedAt
	if _, _, err := agentRepository.CompleteModelCall(ctx, agentapplication.CompleteModelCallCommand{WorkspaceID: workspaceID, ExpectedVersion: 1, Call: call}); err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	var appender interface {
		AppendTx(context.Context, any, eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error)
	} = events
	if failingEvent {
		appender = finalizerFailingAppender{}
	}
	finalizer, err := NewAnswerFinalizer(pool, agentRepository, appender, foundation.FixedClock{Value: createdAt.Add(10 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	return finalizerFixture{ctx: ctx, pool: pool, finalizer: finalizer, lookup: lookup, dispatched: dispatched, run: run}
}

func (fixture finalizerFixture) command(proposal agentapplication.RAGTerminalProposal) conversationapplication.FinalizeAnswerCommand {
	return conversationapplication.FinalizeAnswerCommand{AnswerPublicationLookup: fixture.lookup, ModelRunID: fixture.run.ID, ExpectedAnswerVersion: 1, ExpectedModelRunVersion: 1, Proposal: proposal}
}

type finalizerFailingAppender struct{}

func (finalizerFailingAppender) AppendTx(context.Context, any, eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error) {
	return eventsdomain.ServerEvent{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_FINALIZER_EVENT_FAILURE", true, errors.New("injected"))
}

func finalizerErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
