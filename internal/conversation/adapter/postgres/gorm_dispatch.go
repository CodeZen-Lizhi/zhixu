package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
	"time"
)

// GORMQuestionDispatcher 在同一 scope 原子创建 Question、Workflow、River Job 和通知。
type GORMQuestionDispatcher struct {
	db       *gorm.DB
	uow      foundation.UnitOfWork
	runtime  workflowapplication.ScopedRuntimeStarter
	events   eventsapplication.ScopedAppender
	ids      foundation.IDGenerator
	clock    foundation.Clock
	analysis agentapplication.ScopedWorkspaceAnalysisRunStarter
	audit    ScopedWorkspaceAnalysisAuditRecorder
}

// NewGORMQuestionDispatcher 构造固定 RAG v2 的同池派发器。
func NewGORMQuestionDispatcher(pool *platformpostgres.Pool, runtime workflowapplication.ScopedRuntimeStarter, events eventsapplication.ScopedAppender, ids foundation.IDGenerator, clock foundation.Clock) (*GORMQuestionDispatcher, error) {
	return newGORMQuestionDispatcher(pool, runtime, events, ids, clock, nil, nil)
}

// NewGORMQuestionDispatcherWithWorkspaceAnalysis 注入同 scope 的 Analysis Run 创建能力。
func NewGORMQuestionDispatcherWithWorkspaceAnalysis(pool *platformpostgres.Pool, runtime workflowapplication.ScopedRuntimeStarter, events eventsapplication.ScopedAppender, ids foundation.IDGenerator, clock foundation.Clock, analysis agentapplication.ScopedWorkspaceAnalysisRunStarter) (*GORMQuestionDispatcher, error) {
	if isNilInterface(analysis) {
		return nil, dependency(ErrorCodeQuestionDispatchUnavailable, errors.New("workspace analysis run starter is nil"))
	}
	return newGORMQuestionDispatcher(pool, runtime, events, ids, clock, analysis, nil)
}

// NewGORMQuestionDispatcherWithWorkspaceAnalysisAndAudit 将 Analysis Run 与审计绑定到创建事务。
func NewGORMQuestionDispatcherWithWorkspaceAnalysisAndAudit(pool *platformpostgres.Pool, runtime workflowapplication.ScopedRuntimeStarter, events eventsapplication.ScopedAppender, ids foundation.IDGenerator, clock foundation.Clock, analysis agentapplication.ScopedWorkspaceAnalysisRunStarter, audit ScopedWorkspaceAnalysisAuditRecorder) (*GORMQuestionDispatcher, error) {
	if isNilInterface(analysis) || isNilInterface(audit) {
		return nil, dependency(ErrorCodeQuestionDispatchUnavailable, errors.New("workspace analysis dispatch dependencies are incomplete"))
	}
	return newGORMQuestionDispatcher(pool, runtime, events, ids, clock, analysis, audit)
}

func newGORMQuestionDispatcher(pool *platformpostgres.Pool, runtime workflowapplication.ScopedRuntimeStarter, events eventsapplication.ScopedAppender, ids foundation.IDGenerator, clock foundation.Clock, analysis agentapplication.ScopedWorkspaceAnalysisRunStarter, audit ScopedWorkspaceAnalysisAuditRecorder) (*GORMQuestionDispatcher, error) {
	if isNilInterface(runtime) || isNilInterface(events) || isNilInterface(ids) || isNilInterface(clock) {
		return nil, dependency(ErrorCodeQuestionDispatchUnavailable, errors.New("question dispatch dependency is nil"))
	}
	db, uow, err := conversationGORMDependencies(pool, ErrorCodeQuestionDispatchUnavailable)
	if err != nil {
		return nil, err
	}
	return &GORMQuestionDispatcher{db: db, uow: uow, runtime: runtime, events: events, ids: ids, clock: clock, analysis: analysis, audit: audit}, nil
}

// SubmitQuestion 提交或精确重放所有派发事实；任何协作者失败均回滚整个事务。
func (dispatcher *GORMQuestionDispatcher) SubmitQuestion(ctx context.Context, record conversationapplication.SubmitQuestionRecord) (conversationapplication.SubmitQuestionResult, error) {
	if dispatcher == nil || dispatcher.db == nil || isNilInterface(dispatcher.uow) || isNilInterface(dispatcher.runtime) || isNilInterface(dispatcher.events) ||
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

	var result conversationapplication.SubmitQuestionResult
	err = withinConversationTransaction(ctx, dispatcher.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var err error
		result, err = dispatcher.submitQuestion(ctx, tx, scope, record, request)
		return err
	})
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	return result, nil
}

func (dispatcher *GORMQuestionDispatcher) submitQuestion(ctx context.Context, tx *gorm.DB, transaction foundation.TransactionScope, record conversationapplication.SubmitQuestionRecord, request conversationdomain.QuestionRequest) (conversationapplication.SubmitQuestionResult, error) {
	conversationRecord, err := scanConversationRecord(gormScanRow(tx.WithContext(ctx).Raw(`SELECT `+conversationColumns+`
		FROM agent.conversation WHERE workspace_id=? AND id=? FOR UPDATE`, string(request.WorkspaceID), string(request.ConversationID))))
	if errors.Is(err, sql.ErrNoRows) {
		return conversationapplication.SubmitQuestionResult{}, notFound(ErrorCodeConversationNotFound, err)
	}
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	existing, err := scanQuestion(gormScanRow(tx.WithContext(ctx).Raw(`SELECT `+questionViewColumns+`
		FROM agent.question q
		WHERE q.workspace_id=? AND q.conversation_id=? AND q.idempotency_key=?`, string(request.WorkspaceID), string(request.ConversationID), record.IdempotencyKey)))
	if err == nil {
		return dispatcher.replayQuestion(ctx, tx, transaction, record, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if conversationRecord.Conversation.Status != conversationdomain.ConversationStatusOpen {
		return conversationapplication.SubmitQuestionResult{}, conflict(ErrorCodeQuestionConversationArchived, errors.New("archived conversation does not accept questions"))
	}

	active, err := gormHasActiveAnswerWorkflow(ctx, tx, request.WorkspaceID, request.ConversationID)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if active {
		return conversationapplication.SubmitQuestionResult{}, conflict(ErrorCodeQuestionActiveWorkflow, errors.New("conversation already has an active answer workflow"))
	}

	ordinal, err := gormNextQuestionOrdinal(ctx, tx, request.WorkspaceID, request.ConversationID)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	contextTurns, err := loadGORMPublishedContext(ctx, tx, conversationapplication.PublishedContextQuery{
		WorkspaceID: request.WorkspaceID, ConversationID: request.ConversationID, ThroughOrdinal: ordinal - 1,
	})
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	contextHash, contextThroughOrdinal, _, err := conversationdomain.ComputeContextHash(contextTurns)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, err)
	}
	now, err := gormQuestionDispatchTime(ctx, tx, conversationRecord.Conversation.LastActivityAt)
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
	model := questionModel{
		ID: string(question.ID), WorkspaceID: string(request.WorkspaceID), ConversationID: string(request.ConversationID),
		Ordinal: question.Ordinal, Mode: string(request.Mode), QuestionText: request.QuestionText, Scope: conversationJSONB(scope),
		AnswerDepth: string(request.AnswerDepth), OutputFormat: string(request.OutputFormat), ContextThroughOrdinal: question.ContextThroughOrdinal,
		ContextHash: question.ContextHash, IdempotencyKey: record.IdempotencyKey, RequestHash: question.RequestHash, CreatedAt: question.CreatedAt.UTC(),
	}
	if err := tx.Create(&model).Error; err != nil {
		return conversationapplication.SubmitQuestionResult{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	persistedQuestion, err := scanQuestion(gormScanRow(tx.Model(&questionModel{}).Select(questionColumns).Where("workspace_id=? AND id=?", model.WorkspaceID, model.ID)))
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if !sameQuestionDispatchBinding(persistedQuestion, question) {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("question readback differs from dispatched fact"))
	}

	runtimeResult, err := dispatcher.startQuestionWorkflow(ctx, tx, transaction, persistedQuestion, answerID)
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
	answerRow := answerModel{ID: string(answer.ID), WorkspaceID: string(answer.WorkspaceID), ConversationID: string(answer.ConversationID), QuestionID: string(answer.QuestionID), WorkflowRunID: string(answer.WorkflowRunID), PublicationStatus: string(answer.PublicationStatus), Version: answer.Version, CreatedAt: answer.CreatedAt.UTC(), UpdatedAt: answer.UpdatedAt.UTC()}
	if err := tx.Create(&answerRow).Error; err != nil {
		return conversationapplication.SubmitQuestionResult{}, classifyQuestionAnswerInsertFailure(err)
	}
	answerView, err := scanAnswerView(gormScanRow(tx.WithContext(ctx).Raw(`SELECT `+answerViewColumns+`
		FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE a.workspace_id=? AND a.id=?`, string(answer.WorkspaceID), string(answer.ID))))
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if !samePendingAnswerDispatchBinding(answerView.Answer, answer) {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("answer readback differs from dispatched slot"))
	}
	if err := dispatcher.startWorkspaceAnalysisRun(ctx, tx, transaction, persistedQuestion, answerView.Answer, runtimeResult, false); err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}

	updatedConversation, err := scanConversationRecord(gormScanRow(tx.WithContext(ctx).Raw(`UPDATE agent.conversation
		SET version=version+1,last_activity_at=?,updated_at=?
		WHERE workspace_id=? AND id=? AND status='open' AND version=?
		RETURNING `+conversationColumns, now.UTC(), now.UTC(), string(request.WorkspaceID), string(request.ConversationID), conversationRecord.Conversation.Version)))
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	if updatedConversation.Conversation.Version != conversationRecord.Conversation.Version+1 ||
		!updatedConversation.Conversation.LastActivityAt.Equal(now) || !updatedConversation.Conversation.UpdatedAt.Equal(now) {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("conversation activity update differs"))
	}
	if err := dispatcher.appendAnswerPending(ctx, tx, transaction, answer, updatedConversation.Conversation.Version, now); err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	return submitQuestionResult(persistedQuestion, answerView, runtimeResult, false), nil
}

func (dispatcher *GORMQuestionDispatcher) replayQuestion(ctx context.Context, tx *gorm.DB, transaction foundation.TransactionScope, record conversationapplication.SubmitQuestionRecord, question conversationdomain.Question) (conversationapplication.SubmitQuestionResult, error) {
	if question.RequestHash != record.RequestHash {
		return conversationapplication.SubmitQuestionResult{}, conflict(ErrorCodeQuestionIdempotencyConflict, errors.New("question idempotency key is bound to a different request"))
	}
	var answerIDValue string
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT id::text FROM agent.answer
		WHERE workspace_id=? AND conversation_id=? AND question_id=?`, string(question.Request.WorkspaceID), string(question.Request.ConversationID), string(question.ID))).Scan(&answerIDValue)
	if errors.Is(err, sql.ErrNoRows) {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("replayed question has no answer workflow binding"))
	}
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	answerID, err := foundation.ParseID(answerIDValue)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("replayed question answer identity is invalid"))
	}
	runtimeResult, err := dispatcher.startQuestionWorkflow(ctx, tx, transaction, question, answerID)
	if err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	answerView, err := scanAnswerView(gormScanRow(tx.WithContext(ctx).Raw(`SELECT `+answerViewColumns+`
		FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE a.workspace_id=? AND a.conversation_id=? AND a.question_id=?`, string(question.Request.WorkspaceID), string(question.Request.ConversationID), string(question.ID))))
	if errors.Is(err, sql.ErrNoRows) {
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
	if err := dispatcher.startWorkspaceAnalysisRun(ctx, tx, transaction, question, answerView.Answer, runtimeResult, true); err != nil {
		return conversationapplication.SubmitQuestionResult{}, err
	}
	return submitQuestionResult(question, answerView, runtimeResult, true), nil
}

func (dispatcher *GORMQuestionDispatcher) startQuestionWorkflow(ctx context.Context, tx *gorm.DB, transaction foundation.TransactionScope, question conversationdomain.Question, answerID foundation.ID) (workflowapplication.RuntimeStartResult, error) {
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
	result, err := dispatcher.runtime.StartScoped(ctx, transaction, request)
	if err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	if err := validateQuestionRuntime(result, request, plan, question); err != nil {
		return workflowapplication.RuntimeStartResult{}, err
	}
	return result, nil
}

func gormHasActiveAnswerWorkflow(ctx context.Context, tx *gorm.DB, workspaceID, conversationID foundation.ID) (bool, error) {
	var active bool
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT EXISTS(
		SELECT 1
		FROM agent.answer a
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE a.workspace_id=? AND a.conversation_id=?
		  AND w.status NOT IN ('succeeded','failed','cancelled')
	)`, string(workspaceID), string(conversationID))).Scan(&active)
	if err != nil {
		return false, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	return active, nil
}

func gormNextQuestionOrdinal(ctx context.Context, tx *gorm.DB, workspaceID, conversationID foundation.ID) (int64, error) {
	var ordinal int64
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT COALESCE(MAX(ordinal),0)+1
		FROM agent.question WHERE workspace_id=? AND conversation_id=?`, string(workspaceID), string(conversationID))).Scan(&ordinal)
	if err != nil {
		return 0, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	if ordinal < 1 {
		return 0, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("next question ordinal is invalid"))
	}
	return ordinal, nil
}

func gormQuestionDispatchTime(ctx context.Context, tx *gorm.DB, minimum time.Time) (time.Time, error) {
	var now time.Time
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT clock_timestamp()`)).Scan(&now); err != nil {
		return time.Time{}, classify(err, ErrorCodeQuestionDispatchUnavailable)
	}
	now = now.UTC()
	if minimum.After(now) {
		now = minimum.UTC()
	}
	return now, nil
}

func (dispatcher *GORMQuestionDispatcher) appendAnswerPending(ctx context.Context, tx *gorm.DB, transaction foundation.TransactionScope, answer conversationdomain.Answer, conversationVersion int64, occurredAt time.Time) error {
	conversationID, workflowRunID, questionID, answerID := answer.ConversationID, answer.WorkflowRunID, answer.QuestionID, answer.ID
	_, replayed, err := dispatcher.events.AppendScoped(ctx, transaction, eventsdomain.AppendRequest{
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

func (dispatcher *GORMQuestionDispatcher) startWorkspaceAnalysisRun(
	ctx context.Context,
	tx *gorm.DB, transaction foundation.TransactionScope,
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
	run, err := dispatcher.analysis.StartWorkspaceAnalysisRunScoped(ctx, transaction, agentapplication.WorkspaceAnalysisRunStartCommand{
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
	if err := dispatcher.appendWorkspaceAnalysisStartedEvent(ctx, transaction, run, replayed); err != nil {
		return err
	}
	if !replayed {
		if err := appendScopedWorkspaceAnalysisRunStartedAudit(ctx, transaction, dispatcher.audit, run); err != nil {
			return err
		}
	}
	return nil
}

func (dispatcher *GORMQuestionDispatcher) appendWorkspaceAnalysisStartedEvent(
	ctx context.Context,
	transaction foundation.TransactionScope,
	run agentdomain.WorkspaceAnalysisRun,
	wantReplay bool,
) error {
	// The source ref identifies the immutable creation transition, not the
	// current mutable Run projection returned during an idempotency replay.
	// Preserve the exact queued/v1 binding so AppendScoped can reject any real
	// source-event drift.
	const initialVersion int64 = 1
	const initialStatus = agentdomain.WorkspaceAnalysisRunQueued
	conversationID, workflowRunID, questionID, answerID := run.ConversationID, run.WorkflowRunID, run.QuestionID, run.AnswerID
	_, replayed, err := dispatcher.events.AppendScoped(ctx, transaction, eventsdomain.AppendRequest{
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

var _ conversationapplication.QuestionDispatcher = (*GORMQuestionDispatcher)(nil)
