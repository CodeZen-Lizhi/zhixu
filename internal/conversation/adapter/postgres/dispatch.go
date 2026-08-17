package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	questionDispatchNo               = 1
	answerActiveWorkflowConstraint   = "agent_answer_active_workflow"
	answerConversationOpenConstraint = "agent_answer_conversation_open"
)

type questionRuntimeStarter interface {
	StartTx(context.Context, pgx.Tx, workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error)
}

// QuestionDispatcher 跨 Conversation、Workflow、River 与 Server Event 维护 Question 原子派发。
type QuestionDispatcher struct {
	db       DB
	runtime  questionRuntimeStarter
	events   eventsapplication.Appender
	ids      foundation.IDGenerator
	clock    foundation.Clock
	analysis agentapplication.WorkspaceAnalysisRunStarter
	audit    WorkspaceAnalysisAuditRecorder
}

var _ conversationapplication.QuestionDispatcher = (*QuestionDispatcher)(nil)

// NewQuestionDispatcher 构造固定使用当前 RAG v2 Definition 的 Question 原子派发器。
func NewQuestionDispatcher(
	db DB,
	runtime questionRuntimeStarter,
	events eventsapplication.Appender,
	ids foundation.IDGenerator,
	clock foundation.Clock,
) (*QuestionDispatcher, error) {
	return newQuestionDispatcher(db, runtime, events, ids, clock, nil, nil)
}

// NewQuestionDispatcherWithWorkspaceAnalysis 显式注入已通过 readiness 的 Analysis Run 原子创建能力。
func NewQuestionDispatcherWithWorkspaceAnalysis(
	db DB,
	runtime questionRuntimeStarter,
	events eventsapplication.Appender,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	analysis agentapplication.WorkspaceAnalysisRunStarter,
) (*QuestionDispatcher, error) {
	if isNilInterface(analysis) {
		return nil, dependency(ErrorCodeQuestionDispatchUnavailable, errors.New("workspace analysis run starter is nil"))
	}
	return newQuestionDispatcher(db, runtime, events, ids, clock, analysis, nil)
}

// NewQuestionDispatcherWithWorkspaceAnalysisAndAudit 显式启用 Analysis Run 创建事务内审计。
func NewQuestionDispatcherWithWorkspaceAnalysisAndAudit(
	db DB,
	runtime questionRuntimeStarter,
	events eventsapplication.Appender,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	analysis agentapplication.WorkspaceAnalysisRunStarter,
	audit WorkspaceAnalysisAuditRecorder,
) (*QuestionDispatcher, error) {
	if isNilInterface(analysis) || isNilInterface(audit) {
		return nil, dependency(ErrorCodeQuestionDispatchUnavailable, errors.New("workspace analysis dispatch dependencies are incomplete"))
	}
	return newQuestionDispatcher(db, runtime, events, ids, clock, analysis, audit)
}

func newQuestionDispatcher(
	db DB,
	runtime questionRuntimeStarter,
	events eventsapplication.Appender,
	ids foundation.IDGenerator,
	clock foundation.Clock,
	analysis agentapplication.WorkspaceAnalysisRunStarter,
	audit WorkspaceAnalysisAuditRecorder,
) (*QuestionDispatcher, error) {
	if isNilInterface(db) || isNilInterface(runtime) || isNilInterface(events) || isNilInterface(ids) || isNilInterface(clock) {
		return nil, dependency(ErrorCodeQuestionDispatchUnavailable, errors.New("question dispatch dependency is nil"))
	}
	return &QuestionDispatcher{db: db, runtime: runtime, events: events, ids: ids, clock: clock, analysis: analysis, audit: audit}, nil
}

// SubmitQuestion 原子创建或精确重放 Question、Answer、Workflow、River Job 与安全摘要事件。
func (dispatcher *QuestionDispatcher) SubmitQuestion(ctx context.Context, record conversationapplication.SubmitQuestionRecord) (conversationapplication.SubmitQuestionResult, error) {
	if dispatcher == nil || isNilInterface(dispatcher.db) || isNilInterface(dispatcher.runtime) || isNilInterface(dispatcher.events) ||
		isNilInterface(dispatcher.ids) || isNilInterface(dispatcher.clock) {
		return conversationapplication.SubmitQuestionResult{}, dependency(ErrorCodeQuestionDispatchUnavailable, errors.New("question dispatcher is unavailable"))
	}
	if ctx == nil {
		return conversationapplication.SubmitQuestionResult{}, invalid(ErrorCodeQuestionDispatchInvalid, errors.New("question dispatch context is nil"))
	}
	request, err := validateQuestionDispatchRecord(record)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if request.Mode == conversationdomain.QuestionModeWorkspaceAnalysis && isNilInterface(dispatcher.analysis) {
		return conversationapplication.SubmitQuestionResult{}, workspaceAnalysisCapabilityUnavailable()
	}

	tx, err := dispatcher.db.Begin(ctx)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	conversationRecord, err := scanConversationRecord(tx.QueryRow(ctx, `SELECT `+conversationColumns+`
		FROM agent.conversation WHERE workspace_id=$1 AND id=$2 FOR UPDATE`,
		string(request.WorkspaceID), string(request.ConversationID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationapplication.SubmitQuestionResult{}, notFound(ErrorCodeConversationNotFound, err)
	}
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	existing, err := scanQuestion(tx.QueryRow(ctx, `SELECT `+questionViewColumns+`
		FROM agent.question q
		WHERE q.workspace_id=$1 AND q.conversation_id=$2 AND q.idempotency_key=$3`,
		string(request.WorkspaceID), string(request.ConversationID), record.IdempotencyKey))
	if err == nil {
		return dispatcher.replayQuestion(ctx, tx, record, existing)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if conversationRecord.Conversation.Status != conversationdomain.ConversationStatusOpen {
		return conversationapplication.SubmitQuestionResult{}, conflict(ErrorCodeQuestionConversationArchived, errors.New("archived conversation does not accept questions"))
	}

	active, err := hasActiveAnswerWorkflow(ctx, tx, request.WorkspaceID, request.ConversationID)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if active {
		return conversationapplication.SubmitQuestionResult{}, conflict(ErrorCodeQuestionActiveWorkflow, errors.New("conversation already has an active answer workflow"))
	}

	ordinal, err := nextQuestionOrdinal(ctx, tx, request.WorkspaceID, request.ConversationID)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	contextTurns, err := loadPublishedContext(ctx, tx, conversationapplication.PublishedContextQuery{
		WorkspaceID: request.WorkspaceID, ConversationID: request.ConversationID, ThroughOrdinal: ordinal - 1,
	})
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	contextHash, contextThroughOrdinal, _, err := conversationdomain.ComputeContextHash(contextTurns)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, err)
	}
	now, err := questionDispatchTime(ctx, tx, conversationRecord.Conversation.LastActivityAt)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	questionID, err := dispatcher.ids.New()
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	answerID, err := dispatcher.ids.New()
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	question := conversationdomain.Question{
		ID: questionID, Request: request, Ordinal: ordinal, ContextThroughOrdinal: contextThroughOrdinal,
		ContextHash: contextHash, RequestHash: record.RequestHash, CreatedAt: now,
	}
	if err := conversationdomain.ValidateQuestion(question); err != nil {
		return conversationapplication.SubmitQuestionResult{}, invalid(ErrorCodeQuestionDispatchInvalid, err)
	}
	scope, err := conversationdomain.EncodeQuestionScope(request)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	persistedQuestion, err := scanQuestion(tx.QueryRow(ctx, `INSERT INTO agent.question(
		id,workspace_id,conversation_id,ordinal,mode,question_text,scope,answer_depth,output_format,
		context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12,$13,$14)
	RETURNING `+questionColumns,
		string(question.ID), string(request.WorkspaceID), string(request.ConversationID), question.Ordinal,
		string(request.Mode), request.QuestionText, scope, string(request.AnswerDepth), string(request.OutputFormat),
		question.ContextThroughOrdinal, question.ContextHash, record.IdempotencyKey, question.RequestHash, question.CreatedAt.UTC()))
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if !sameQuestionDispatchBinding(persistedQuestion, question) {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("question readback differs from dispatched fact"))
	}

	runtimeResult, err := dispatcher.startQuestionWorkflow(ctx, tx, persistedQuestion, answerID)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if runtimeResult.Replayed {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("new question resolved to an existing workflow"))
	}

	answer := conversationdomain.Answer{
		ID: answerID, WorkspaceID: request.WorkspaceID, ConversationID: request.ConversationID,
		QuestionID: persistedQuestion.ID, WorkflowRunID: runtimeResult.Run.ID,
		PublicationStatus: conversationdomain.AnswerPublicationPending,
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := conversationdomain.ValidateAnswer(answer); err != nil {
		return conversationapplication.SubmitQuestionResult{}, invalid(ErrorCodeQuestionDispatchInvalid, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent.answer(
		id,workspace_id,conversation_id,question_id,workflow_run_id,publication_status,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		string(answer.ID), string(answer.WorkspaceID), string(answer.ConversationID), string(answer.QuestionID),
		string(answer.WorkflowRunID), string(answer.PublicationStatus), answer.Version, answer.CreatedAt.UTC(), answer.UpdatedAt.UTC()); err != nil {
		return conversationapplication.SubmitQuestionResult{}, classifyQuestionAnswerInsertFailure(err)
	}
	answerView, err := scanAnswerView(tx.QueryRow(ctx, `SELECT `+answerViewColumns+`
		FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE a.workspace_id=$1 AND a.id=$2`, string(answer.WorkspaceID), string(answer.ID)))
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if !samePendingAnswerDispatchBinding(answerView.Answer, answer) {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("answer readback differs from dispatched slot"))
	}
	if err := dispatcher.startWorkspaceAnalysisRun(ctx, tx, persistedQuestion, answerView.Answer, runtimeResult, false); err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}

	updatedConversation, err := scanConversationRecord(tx.QueryRow(ctx, `UPDATE agent.conversation
		SET version=version+1,last_activity_at=$3,updated_at=$3
		WHERE workspace_id=$1 AND id=$2 AND status='open' AND version=$4
		RETURNING `+conversationColumns,
		string(request.WorkspaceID), string(request.ConversationID), now.UTC(), conversationRecord.Conversation.Version))
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if updatedConversation.Conversation.Version != conversationRecord.Conversation.Version+1 ||
		!updatedConversation.Conversation.LastActivityAt.Equal(now) || !updatedConversation.Conversation.UpdatedAt.Equal(now) {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("conversation activity update differs"))
	}
	if err := dispatcher.appendAnswerPending(ctx, tx, answer, updatedConversation.Conversation.Version, now); err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationapplication.SubmitQuestionResult{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	return submitQuestionResult(persistedQuestion, answerView, runtimeResult, false), nil
}

func validateQuestionDispatchRecord(record conversationapplication.SubmitQuestionRecord) (conversationdomain.QuestionRequest, error) {
	if !canonicalIdempotencyKey(record.IdempotencyKey) {
		return conversationdomain.QuestionRequest{}, invalid(ErrorCodeQuestionDispatchInvalid, errors.New("question idempotency key is invalid"))
	}
	request, err := conversationdomain.CanonicalizeQuestionRequest(record.Request)
	if err != nil || !reflect.DeepEqual(request, record.Request) {
		return conversationdomain.QuestionRequest{}, invalid(ErrorCodeQuestionDispatchInvalid, err)
	}
	hash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil || hash != record.RequestHash {
		return conversationdomain.QuestionRequest{}, invalid(ErrorCodeQuestionDispatchInvalid, errors.New("question request hash is inconsistent"))
	}
	return request, nil
}

func (dispatcher *QuestionDispatcher) replayQuestion(ctx context.Context, tx pgx.Tx, record conversationapplication.SubmitQuestionRecord, question conversationdomain.Question) (conversationapplication.SubmitQuestionResult, error) {
	if question.RequestHash != record.RequestHash {
		return conversationapplication.SubmitQuestionResult{}, conflict(ErrorCodeQuestionIdempotencyConflict, errors.New("question idempotency key is bound to a different request"))
	}
	var answerIDValue string
	err := tx.QueryRow(ctx, `SELECT id::text FROM agent.answer
		WHERE workspace_id=$1 AND conversation_id=$2 AND question_id=$3`,
		string(question.Request.WorkspaceID), string(question.Request.ConversationID), string(question.ID)).Scan(&answerIDValue)
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("replayed question has no answer workflow binding"))
	}
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	answerID, err := foundation.ParseID(answerIDValue)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("replayed question answer identity is invalid"))
	}
	runtimeResult, err := dispatcher.startQuestionWorkflow(ctx, tx, question, answerID)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	answerView, err := scanAnswerView(tx.QueryRow(ctx, `SELECT `+answerViewColumns+`
		FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE a.workspace_id=$1 AND a.conversation_id=$2 AND a.question_id=$3`,
		string(question.Request.WorkspaceID), string(question.Request.ConversationID), string(question.ID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("replayed question has no answer workflow binding"))
	}
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if !runtimeResult.Replayed || runtimeResult.Run.ID != answerView.Answer.WorkflowRunID {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("replayed question workflow binding differs"))
	}
	if answerView.Workflow.RunID != runtimeResult.Run.ID || answerView.Workflow.Status != runtimeResult.Run.Status ||
		answerView.Workflow.Version != runtimeResult.Run.Version || !answerView.Workflow.UpdatedAt.Equal(runtimeResult.Run.UpdatedAt) {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("replayed answer workflow projection differs"))
	}
	if err := dispatcher.startWorkspaceAnalysisRun(ctx, tx, question, answerView.Answer, runtimeResult, true); err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationapplication.SubmitQuestionResult{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	return submitQuestionResult(question, answerView, runtimeResult, true), nil
}

func (dispatcher *QuestionDispatcher) startQuestionWorkflow(ctx context.Context, tx pgx.Tx, question conversationdomain.Question, answerID foundation.ID) (workflowapplication.RuntimeStartResult, error) {
	plan, err := buildQuestionWorkflowDispatchPlan(question, answerID)
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	request, err := workflowapplication.BuildRuntimeStartRequest(
		dispatcher.ids, dispatcher.clock, question.Request.WorkspaceID, plan.idempotencyKey, plan.input, plan.definition,
	)
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	result, err := dispatcher.runtime.StartTx(ctx, tx, request)
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	if err := validateQuestionRuntime(result, request, plan, question); err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	return result, nil
}

type questionWorkflowDispatchPlan struct {
	definition     workflowdomain.RegisteredDefinition
	input          json.RawMessage
	idempotencyKey string
	root           workflowdomain.NodeDefinition
}

type questionWorkflowInputBinding struct {
	ConversationID  foundation.ID
	QuestionID      foundation.ID
	AnswerID        foundation.ID
	QuestionOrdinal int64
	ContextHash     string
}

func buildQuestionWorkflowDispatchPlan(question conversationdomain.Question, answerID foundation.ID) (questionWorkflowDispatchPlan, error) {
	var definition workflowdomain.RegisteredDefinition
	var input json.RawMessage
	var err error
	switch question.Request.Mode {
	case conversationdomain.QuestionModeRAG:
		definition = conversationworkflow.RegisteredDefinitionV2()
		input, err = conversationworkflow.EncodeInput(conversationworkflow.Input{
			SchemaVersion: conversationworkflow.InputSchemaVersion, ConversationID: question.Request.ConversationID,
			QuestionID: question.ID, AnswerID: answerID, QuestionOrdinal: question.Ordinal, ContextHash: question.ContextHash,
		})
	case conversationdomain.QuestionModeWorkspaceAnalysis:
		definition = conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
		input, err = conversationworkflow.EncodeWorkspaceAnalysisInput(conversationworkflow.WorkspaceAnalysisInput{
			SchemaVersion: conversationworkflow.WorkspaceAnalysisInputSchemaVersion, ConversationID: question.Request.ConversationID,
			QuestionID: question.ID, AnswerID: answerID, QuestionOrdinal: question.Ordinal, ContextHash: question.ContextHash,
		})
	default:
		return questionWorkflowDispatchPlan{}, invalid(ErrorCodeQuestionDispatchInvalid, errors.New("question workflow mode is unsupported"))
	}
	if err != nil {
		return questionWorkflowDispatchPlan{}, err
	}
	root, err := uniqueQuestionWorkflowRoot(definition)
	if err != nil {
		return questionWorkflowDispatchPlan{}, err
	}
	return questionWorkflowDispatchPlan{
		definition: definition, input: input,
		idempotencyKey: questionWorkflowIdempotencyKeyForMode(question.Request.Mode, question.ID), root: root,
	}, nil
}

func uniqueQuestionWorkflowRoot(definition workflowdomain.RegisteredDefinition) (workflowdomain.NodeDefinition, error) {
	var root workflowdomain.NodeDefinition
	count := 0
	for _, node := range definition.Graph.Nodes {
		if len(node.Dependencies) == 0 {
			root = node
			count++
		}
	}
	if count != 1 {
		return workflowdomain.NodeDefinition{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("question workflow definition does not have exactly one root"))
	}
	return root, nil
}

func validateQuestionRuntime(result workflowapplication.RuntimeStartResult, request workflowapplication.RuntimeStartRequest, plan questionWorkflowDispatchPlan, question conversationdomain.Question) error {
	if result.Run.WorkspaceID != question.Request.WorkspaceID || result.Run.ID == "" || result.Run.DefinitionID == "" ||
		result.Run.IdempotencyKey != request.Run.IdempotencyKey || result.Run.RequestHash != request.RequestHash ||
		result.FirstNode.ID == "" || result.Job.JobID < 1 || result.Replayed != result.Job.Duplicate ||
		result.FirstNode.RunID != result.Run.ID || result.FirstNode.NodeKey != plan.root.Key ||
		result.FirstNode.NodeType != plan.root.Kind || result.FirstNode.InputSchemaVersion != plan.root.InputSchemaVersion ||
		result.FirstNode.OutputSchemaVersion != plan.root.OutputSchemaVersion || result.FirstNode.DispatchNo != questionDispatchNo ||
		result.FirstNode.IdempotencyKey != request.FirstNode.IdempotencyKey || !validWorkflowRunStatus(result.Run.Status) ||
		!validQuestionNodeStatus(result.FirstNode.Status) || result.Run.Version < 1 || result.FirstNode.Version < 1 ||
		result.Run.CreatedAt.IsZero() || result.Run.UpdatedAt.Before(result.Run.CreatedAt) || result.FirstNode.CreatedAt.IsZero() ||
		result.FirstNode.UpdatedAt.Before(result.FirstNode.CreatedAt) ||
		(!result.Replayed && (result.Run.Status != workflowdomain.RunStatusPending || result.FirstNode.Status != workflowdomain.NodeStatusPending)) {
		return consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("RAG workflow start result binding is invalid"))
	}
	runInput, err := decodeQuestionWorkflowInput(question.Request.Mode, result.Run.Input)
	if err != nil {
		return consistency(ErrorCodeQuestionDispatchCorrupt, err)
	}
	nodeInput, err := decodeQuestionWorkflowInput(question.Request.Mode, result.FirstNode.Input)
	if err != nil || runInput != nodeInput {
		return consistency(ErrorCodeQuestionDispatchCorrupt, err)
	}
	expected, err := decodeQuestionWorkflowInput(question.Request.Mode, plan.input)
	if err != nil || expected != runInput {
		return consistency(ErrorCodeQuestionDispatchCorrupt, err)
	}
	identities := []foundation.ID{
		question.Request.WorkspaceID, runInput.ConversationID, runInput.QuestionID, runInput.AnswerID,
		result.Run.DefinitionID, result.Run.ID, result.FirstNode.ID,
	}
	seen := make(map[foundation.ID]struct{}, len(identities))
	for _, identity := range identities {
		parsed, parseErr := foundation.ParseID(string(identity))
		if parseErr != nil || parsed != identity {
			return consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("RAG workflow start identity is invalid"))
		}
		if _, duplicate := seen[identity]; duplicate {
			return consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("RAG workflow start identity is reused"))
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func decodeQuestionWorkflowInput(mode conversationdomain.QuestionMode, raw json.RawMessage) (questionWorkflowInputBinding, error) {
	switch mode {
	case conversationdomain.QuestionModeRAG:
		input, err := conversationworkflow.DecodeInput(raw)
		if err != nil {
			return questionWorkflowInputBinding{}, err
		}
		return questionWorkflowInputBinding{
			ConversationID: input.ConversationID, QuestionID: input.QuestionID, AnswerID: input.AnswerID,
			QuestionOrdinal: input.QuestionOrdinal, ContextHash: input.ContextHash,
		}, nil
	case conversationdomain.QuestionModeWorkspaceAnalysis:
		input, err := conversationworkflow.DecodeWorkspaceAnalysisInput(raw)
		if err != nil {
			return questionWorkflowInputBinding{}, err
		}
		return questionWorkflowInputBinding{
			ConversationID: input.ConversationID, QuestionID: input.QuestionID, AnswerID: input.AnswerID,
			QuestionOrdinal: input.QuestionOrdinal, ContextHash: input.ContextHash,
		}, nil
	default:
		return questionWorkflowInputBinding{}, invalid(ErrorCodeQuestionDispatchInvalid, errors.New("question workflow mode is unsupported"))
	}
}

func validQuestionNodeStatus(status workflowdomain.NodeStatus) bool {
	switch status {
	case workflowdomain.NodeStatusPending, workflowdomain.NodeStatusRunning, workflowdomain.NodeStatusWaitingForHuman,
		workflowdomain.NodeStatusRetryWait, workflowdomain.NodeStatusPaused, workflowdomain.NodeStatusSucceeded,
		workflowdomain.NodeStatusFailed, workflowdomain.NodeStatusCancelled:
		return true
	default:
		return false
	}
}

func classifyQuestionAnswerInsertFailure(cause error) error {
	var postgresError *pgconn.PgError
	if errors.As(cause, &postgresError) {
		switch postgresError.ConstraintName {
		case answerActiveWorkflowConstraint:
			return conflict(ErrorCodeQuestionActiveWorkflow, cause)
		case answerConversationOpenConstraint:
			return conflict(ErrorCodeQuestionConversationArchived, cause)
		}
	}
	return classify(cause, ErrorCodeQuestionDispatchUnavailable)
}

func hasActiveAnswerWorkflow(ctx context.Context, tx pgx.Tx, workspaceID, conversationID foundation.ID) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE a.workspace_id=$1 AND a.conversation_id=$2
		  AND w.status NOT IN ('succeeded','failed','cancelled')
	)`, string(workspaceID), string(conversationID)).Scan(&active)
	if err != nil {
		return false, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	return active, nil
}

func nextQuestionOrdinal(ctx context.Context, tx pgx.Tx, workspaceID, conversationID foundation.ID) (int64, error) {
	var ordinal int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(ordinal),0)+1
		FROM agent.question WHERE workspace_id=$1 AND conversation_id=$2`, string(workspaceID), string(conversationID)).Scan(&ordinal)
	if err != nil {
		return 0, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	if ordinal < 1 {
		return 0, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("next question ordinal is invalid"))
	}
	return ordinal, nil
}

func questionDispatchTime(ctx context.Context, tx pgx.Tx, minimum time.Time) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	now = now.UTC()
	if minimum.After(now) {
		now = minimum.UTC()
	}
	return now, nil
}

func (dispatcher *QuestionDispatcher) appendAnswerPending(ctx context.Context, tx pgx.Tx, answer conversationdomain.Answer, conversationVersion int64, occurredAt time.Time) error {
	conversationID, workflowRunID, questionID, answerID := answer.ConversationID, answer.WorkflowRunID, answer.QuestionID, answer.ID
	_, replayed, err := dispatcher.events.AppendTx(ctx, tx, eventsdomain.AppendRequest{
		WorkspaceID: answer.WorkspaceID, ConversationID: &conversationID, WorkflowRunID: &workflowRunID,
		Type: "answer.pending", ResourceRef: "answer:" + string(answer.ID), ResourceVersion: answer.Version,
		PayloadSummary: eventsdomain.PayloadSummary{
			ConversationID: &conversationID, WorkflowRunID: &workflowRunID, QuestionID: &questionID, AnswerID: &answerID,
			PublicationStatus: string(answer.PublicationStatus),
		},
		SchemaVersion: 1, SourceEventRef: "answer.pending:" + string(answer.ID) + ":v1", OccurredAt: occurredAt,
	})
	if err != nil {
		return err
	}
	if replayed || conversationVersion < 2 {
		return consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("new answer reused an existing pending event"))
	}
	return nil
}

func (dispatcher *QuestionDispatcher) startWorkspaceAnalysisRun(
	ctx context.Context,
	tx pgx.Tx,
	question conversationdomain.Question,
	answer conversationdomain.Answer,
	runtime workflowapplication.RuntimeStartResult,
	replayed bool,
) error {
	if question.Request.Mode != conversationdomain.QuestionModeWorkspaceAnalysis {
		return nil
	}
	if isNilInterface(dispatcher.analysis) {
		return workspaceAnalysisCapabilityUnavailable()
	}
	run, err := dispatcher.analysis.StartWorkspaceAnalysisRunTx(ctx, tx, agentapplication.WorkspaceAnalysisRunStartCommand{
		WorkspaceID: question.Request.WorkspaceID, ConversationID: question.Request.ConversationID,
		QuestionID: question.ID, AnswerID: answer.ID, WorkflowRunID: runtime.Run.ID,
		CreatedAt: question.CreatedAt, Replayed: replayed,
	})
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Code == agentapplication.ErrorCodeWorkspaceAnalysisCapabilityUnavailable {
			return workspaceAnalysisCapabilityUnavailable()
		}
		return err
	}
	if run.WorkspaceID != question.Request.WorkspaceID || run.ConversationID != question.Request.ConversationID ||
		run.QuestionID != question.ID || run.AnswerID != answer.ID || run.WorkflowRunID != runtime.Run.ID {
		return consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("workspace analysis run binding differs from the dispatched question"))
	}
	if err := dispatcher.appendWorkspaceAnalysisStartedEvent(ctx, tx, run, replayed); err != nil {
		return err
	}
	if !replayed {
		if err := appendWorkspaceAnalysisRunStartedAudit(ctx, tx, dispatcher.audit, run); err != nil {
			return err
		}
	}
	return nil
}

// appendWorkspaceAnalysisStartedEvent 在 Analysis Run 创建事务中写入唯一的安全失效通知。
// 重放只接受已提交且完整相同的事件，避免手工补写掩盖拆分状态。
func (dispatcher *QuestionDispatcher) appendWorkspaceAnalysisStartedEvent(
	ctx context.Context,
	tx pgx.Tx,
	run agentdomain.WorkspaceAnalysisRun,
	wantReplay bool,
) error {
	// The source ref identifies the immutable creation transition, not the
	// current mutable Run projection returned during an idempotency replay.
	// Preserve the exact queued/v1 binding so AppendTx can reject any real
	// source-event drift.
	const initialVersion int64 = 1
	const initialStatus = agentdomain.WorkspaceAnalysisRunQueued
	conversationID, workflowRunID, questionID, answerID := run.ConversationID, run.WorkflowRunID, run.QuestionID, run.AnswerID
	_, replayed, err := dispatcher.events.AppendTx(ctx, tx, eventsdomain.AppendRequest{
		WorkspaceID: run.WorkspaceID, ConversationID: &conversationID, WorkflowRunID: &workflowRunID,
		Type: "workspace_analysis.started", ResourceRef: "workspace_analysis:" + string(run.ID), ResourceVersion: initialVersion,
		PayloadSummary: eventsdomain.PayloadSummary{
			ConversationID: &conversationID, WorkflowRunID: &workflowRunID, QuestionID: &questionID, AnswerID: &answerID,
			Status: string(initialStatus),
		},
		SchemaVersion:  1,
		SourceEventRef: "workspace_analysis.started:" + string(run.ID) + ":v1",
		OccurredAt:     run.CreatedAt,
	})
	if err != nil {
		return err
	}
	if replayed != wantReplay {
		return consistency(
			ErrorCodeQuestionDispatchCorrupt,
			fmt.Errorf("workspace analysis started event replay=%t, want %t", replayed, wantReplay),
		)
	}
	return nil
}

func submitQuestionResult(question conversationdomain.Question, answerView conversationapplication.AnswerView, runtime workflowapplication.RuntimeStartResult, replayed bool) conversationapplication.SubmitQuestionResult {
	return conversationapplication.SubmitQuestionResult{
		Question: question, Answer: answerView.Answer,
		Workflow: conversationapplication.WorkflowRunView{
			RunID: runtime.Run.ID, Status: runtime.Run.Status, Version: runtime.Run.Version, UpdatedAt: runtime.Run.UpdatedAt,
		},
		NodeRunID: runtime.FirstNode.ID, JobID: runtime.Job.JobID, Replayed: replayed,
	}
}

func questionWorkflowIdempotencyKey(questionID foundation.ID) string {
	return "rag-question:" + string(questionID)
}

func questionWorkflowIdempotencyKeyForMode(mode conversationdomain.QuestionMode, questionID foundation.ID) string {
	if mode == conversationdomain.QuestionModeWorkspaceAnalysis {
		return "workspace-analysis-question:" + string(questionID)
	}
	return questionWorkflowIdempotencyKey(questionID)
}

func workspaceAnalysisCapabilityUnavailable() error {
	return foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		conversationdomain.WorkspaceAnalysisCapabilityUnavailableCode,
		false,
		errors.New("workspace analysis dispatch capability is unavailable"),
	)
}

func sameQuestionDispatchBinding(left, right conversationdomain.Question) bool {
	return left.ID == right.ID && left.Request.WorkspaceID == right.Request.WorkspaceID &&
		left.Request.ConversationID == right.Request.ConversationID && left.RequestHash == right.RequestHash &&
		left.Ordinal == right.Ordinal && left.ContextThroughOrdinal == right.ContextThroughOrdinal &&
		left.ContextHash == right.ContextHash && left.CreatedAt.Equal(right.CreatedAt)
}

func samePendingAnswerDispatchBinding(left, right conversationdomain.Answer) bool {
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID && left.ConversationID == right.ConversationID &&
		left.QuestionID == right.QuestionID && left.WorkflowRunID == right.WorkflowRunID && left.ModelRunID == nil &&
		left.PublicationStatus == right.PublicationStatus && left.ResultType == "" && len(left.Result) == 0 &&
		left.ResultHash == "" && left.RetrievalSummary == nil && left.Version == right.Version &&
		left.CreatedAt.Equal(right.CreatedAt) && left.UpdatedAt.Equal(right.UpdatedAt) && left.PublishedAt == nil
}
