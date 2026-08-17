package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestQuestionWorkflowDispatchPlanSelectsCanonicalModeWithoutChangingRAG(t *testing.T) {
	question := conversationdomain.Question{
		ID: "8c000000-0000-4000-8000-000000000003",
		Request: conversationdomain.QuestionRequest{
			WorkspaceID: "8c000000-0000-4000-8000-000000000001", ConversationID: "8c000000-0000-4000-8000-000000000002",
			Mode: conversationdomain.QuestionModeRAG,
		},
		Ordinal: 2, ContextHash: strings.Repeat("a", 64),
	}
	answerID := foundation.ID("8c000000-0000-4000-8000-000000000004")
	rag, err := buildQuestionWorkflowDispatchPlan(question, answerID)
	if err != nil || !reflect.DeepEqual(rag.definition, conversationworkflow.RegisteredDefinitionV2()) ||
		rag.idempotencyKey != questionWorkflowIdempotencyKey(question.ID) || rag.root.Key != conversationworkflow.NodeKey {
		t.Fatalf("rag plan=%#v err=%v", rag, err)
	}
	if _, err := conversationworkflow.DecodeInput(rag.input); err != nil {
		t.Fatalf("RAG input=%s err=%v", rag.input, err)
	}

	question.Request.Mode = conversationdomain.QuestionModeWorkspaceAnalysis
	analysis, err := buildQuestionWorkflowDispatchPlan(question, answerID)
	if err != nil || !reflect.DeepEqual(analysis.definition, conversationworkflow.RegisteredWorkspaceAnalysisDefinition()) ||
		analysis.idempotencyKey != "workspace-analysis-question:"+string(question.ID) ||
		analysis.root.Key != conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace {
		t.Fatalf("analysis plan=%#v err=%v", analysis, err)
	}
	input, err := conversationworkflow.DecodeWorkspaceAnalysisInput(analysis.input)
	if err != nil || input.QuestionID != question.ID || input.AnswerID != answerID || input.ContextHash != question.ContextHash {
		t.Fatalf("analysis input=%s decoded=%#v err=%v", analysis.input, input, err)
	}
}

func TestQuestionDispatcherRejectsWorkspaceAnalysisBeforeOpeningTransactionWhenCapabilityIsOff(t *testing.T) {
	dispatcher, err := NewQuestionDispatcher(
		&questionConstructorDB{}, &questionConstructorRuntime{}, &questionConstructorAppender{},
		foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: "8c000000-0000-4000-8000-000000000001", ConversationID: "8c000000-0000-4000-8000-000000000002",
		Mode: conversationdomain.QuestionModeWorkspaceAnalysis, QuestionText: "inspect the workspace",
		Scope: conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dispatcher.SubmitQuestion(context.Background(), conversationapplication.SubmitQuestionRecord{
		Request: request, RequestHash: hash, IdempotencyKey: "workspace-analysis-disabled",
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable ||
		classified.Code != conversationdomain.WorkspaceAnalysisCapabilityUnavailableCode || classified.Retryable {
		t.Fatalf("SubmitQuestion() error=%#v", err)
	}
}

func TestQuestionDispatcherWorkspaceAnalysisStartedEventIsExactAndReplayBound(t *testing.T) {
	appender := &workspaceAnalysisEventCapture{}
	dispatcher := &QuestionDispatcher{events: appender}
	run := agentdomain.WorkspaceAnalysisRun{
		ID:             "8c000000-0000-4000-8000-000000000011",
		WorkspaceID:    "8c000000-0000-4000-8000-000000000012",
		ConversationID: "8c000000-0000-4000-8000-000000000013",
		QuestionID:     "8c000000-0000-4000-8000-000000000014",
		AnswerID:       "8c000000-0000-4000-8000-000000000015",
		WorkflowRunID:  "8c000000-0000-4000-8000-000000000016",
		Status:         agentdomain.WorkspaceAnalysisRunQueued,
		Version:        1,
		CreatedAt:      time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC),
	}
	if err := dispatcher.appendWorkspaceAnalysisStartedEvent(context.Background(), nil, run, false); err != nil {
		t.Fatalf("appendWorkspaceAnalysisStartedEvent(new) = %v", err)
	}
	if len(appender.requests) != 1 {
		t.Fatalf("new event requests=%#v", appender.requests)
	}
	request := appender.requests[0]
	if request.Type != "workspace_analysis.started" ||
		request.SourceEventRef != "workspace_analysis.started:"+string(run.ID)+":v1" ||
		request.ResourceRef != "workspace_analysis:"+string(run.ID) || request.ResourceVersion != 1 ||
		request.PayloadSummary.ConversationID == nil || *request.PayloadSummary.ConversationID != run.ConversationID ||
		request.PayloadSummary.WorkflowRunID == nil || *request.PayloadSummary.WorkflowRunID != run.WorkflowRunID ||
		request.PayloadSummary.QuestionID == nil || *request.PayloadSummary.QuestionID != run.QuestionID ||
		request.PayloadSummary.AnswerID == nil || *request.PayloadSummary.AnswerID != run.AnswerID ||
		request.PayloadSummary.Status != "queued" || request.PayloadSummary.ModelRunID != nil ||
		!request.OccurredAt.Equal(run.CreatedAt) {
		t.Fatalf("started event protocol=%#v", request)
	}
	appender.replay = true
	if err := dispatcher.appendWorkspaceAnalysisStartedEvent(context.Background(), nil, run, true); err != nil {
		t.Fatalf("appendWorkspaceAnalysisStartedEvent(replay) = %v", err)
	}
	terminalRun := run
	terminalRun.Status = agentdomain.WorkspaceAnalysisRunSucceeded
	terminalRun.Version = 7
	terminalRun.UpdatedAt = run.CreatedAt.Add(time.Minute)
	if err := dispatcher.appendWorkspaceAnalysisStartedEvent(context.Background(), nil, terminalRun, true); err != nil {
		t.Fatalf("appendWorkspaceAnalysisStartedEvent(terminal replay) = %v", err)
	}
	if len(appender.requests) != 3 || !reflect.DeepEqual(appender.requests[0], appender.requests[2]) {
		t.Fatalf("terminal replay changed immutable started event: %#v", appender.requests)
	}
	appender.replay = false
	if err := dispatcher.appendWorkspaceAnalysisStartedEvent(context.Background(), nil, run, true); err == nil {
		t.Fatal("started event replay accepted a missing event")
	}
}

func TestNewQuestionDispatcherWithWorkspaceAnalysisRejectsTypedNilStarter(t *testing.T) {
	var starter *questionAnalysisRunStarter
	_, err := NewQuestionDispatcherWithWorkspaceAnalysis(
		&questionConstructorDB{}, &questionConstructorRuntime{}, &questionConstructorAppender{},
		foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, starter,
	)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeQuestionDispatchUnavailable {
		t.Fatalf("constructor error=%#v", err)
	}
}

func TestNewQuestionDispatcherRejectsTypedNilDependencies(t *testing.T) {
	db := &questionConstructorDB{}
	runtime := &questionConstructorRuntime{}
	events := &questionConstructorAppender{}
	ids := foundation.NewUUIDGenerator(nil)
	clock := foundation.SystemClock{}

	var nilDB *questionConstructorDB
	var nilRuntime *questionConstructorRuntime
	var nilEvents *questionConstructorAppender
	tests := []struct {
		name    string
		db      DB
		runtime questionRuntimeStarter
		events  eventsapplication.Appender
		kind    foundation.ErrorKind
		code    string
	}{
		{name: "typed nil database", db: nilDB, runtime: runtime, events: events, kind: foundation.ErrorDependencyUnavailable, code: ErrorCodeQuestionDispatchUnavailable},
		{name: "typed nil runtime", db: db, runtime: nilRuntime, events: events, kind: foundation.ErrorDependencyUnavailable, code: ErrorCodeQuestionDispatchUnavailable},
		{name: "typed nil events", db: db, runtime: runtime, events: nilEvents, kind: foundation.ErrorDependencyUnavailable, code: ErrorCodeQuestionDispatchUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewQuestionDispatcher(test.db, test.runtime, test.events, ids, clock)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != test.kind || classified.Code != test.code {
				t.Fatalf("NewQuestionDispatcher() error = %#v", err)
			}
		})
	}
}

func TestNewQuestionDispatcherUsesRegisteredV2Definition(t *testing.T) {
	dispatcher, err := NewQuestionDispatcher(
		&questionConstructorDB{},
		&questionConstructorRuntime{},
		&questionConstructorAppender{},
		foundation.NewUUIDGenerator(nil),
		foundation.SystemClock{},
	)
	if err != nil || dispatcher == nil {
		t.Fatalf("dispatcher=%#v err=%v", dispatcher, err)
	}
	// The constructor deliberately has no Definition selector; startQuestionWorkflow
	// resolves RegisteredDefinitionV2 at the composition boundary.
	if got := conversationworkflow.RegisteredDefinitionV2().Version; got != conversationworkflow.DefinitionVersionV2 {
		t.Fatalf("registered v2 version=%d", got)
	}
}

func TestClassifyQuestionAnswerInsertFailureUsesTriggerContractCodes(t *testing.T) {
	tests := []struct {
		name      string
		cause     error
		kind      foundation.ErrorKind
		code      string
		retryable bool
	}{
		{
			name: "active workflow trigger", cause: &pgconn.PgError{Code: "23505", ConstraintName: answerActiveWorkflowConstraint},
			kind: foundation.ErrorVersionConflict, code: ErrorCodeQuestionActiveWorkflow,
		},
		{
			name: "archived conversation trigger", cause: &pgconn.PgError{Code: "23514", ConstraintName: answerConversationOpenConstraint},
			kind: foundation.ErrorVersionConflict, code: ErrorCodeQuestionConversationArchived,
		},
		{
			name: "unrelated database failure", cause: &pgconn.PgError{Code: "40P01"},
			kind: foundation.ErrorRetryableFailure, code: ErrorCodeQuestionDispatchUnavailable, retryable: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyQuestionAnswerInsertFailure(test.cause)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != test.kind || classified.Code != test.code || classified.Retryable != test.retryable {
				t.Fatalf("classified error = %#v", err)
			}
		})
	}
}

func TestNewQuestionDispatcherRejectsTypedNilIdentityAndClockDependencies(t *testing.T) {
	db := &questionConstructorDB{}
	runtime := &questionConstructorRuntime{}
	events := &questionConstructorAppender{}
	var nilIDs *questionConstructorIDs
	var nilClock *questionConstructorClock
	tests := []struct {
		name  string
		ids   foundation.IDGenerator
		clock foundation.Clock
	}{
		{name: "typed nil identity generator", ids: nilIDs, clock: &questionConstructorClock{}},
		{name: "typed nil clock", ids: &questionConstructorIDs{}, clock: nilClock},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewQuestionDispatcher(db, runtime, events, test.ids, test.clock)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable ||
				classified.Code != ErrorCodeQuestionDispatchUnavailable {
				t.Fatalf("NewQuestionDispatcher() error = %#v", err)
			}
		})
	}
}

type questionConstructorDB struct{}

func (*questionConstructorDB) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unused")
}
func (*questionConstructorDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}
func (*questionConstructorDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type questionConstructorRuntime struct{}

func (*questionConstructorRuntime) StartTx(context.Context, pgx.Tx, workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	return workflowapplication.RuntimeStartResult{}, errors.New("unused")
}

type questionAnalysisRunStarter struct{}

func (*questionAnalysisRunStarter) StartWorkspaceAnalysisRunTx(context.Context, any, agentapplication.WorkspaceAnalysisRunStartCommand) (agentdomain.WorkspaceAnalysisRun, error) {
	return agentdomain.WorkspaceAnalysisRun{}, errors.New("unused")
}

type questionConstructorAppender struct{}

func (*questionConstructorAppender) AppendTx(context.Context, any, eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error) {
	return eventsdomain.ServerEvent{}, false, errors.New("unused")
}

type questionConstructorIDs struct{}

func (*questionConstructorIDs) New() (foundation.ID, error) {
	return "", errors.New("unused")
}

type questionConstructorClock struct{}

func (*questionConstructorClock) Now() time.Time { return time.Time{} }
