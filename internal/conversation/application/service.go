package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	errorCodeServiceUnavailable = "CONVERSATION_SERVICE_UNAVAILABLE"
	errorCodeRequestInvalid     = "CONVERSATION_REQUEST_INVALID"
	errorCodeResultInconsistent = "CONVERSATION_RESULT_INCONSISTENT"
)

// Dependencies 是 Conversation Application Service 的显式端口集合。
type Dependencies struct {
	Repository Repository
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
}

// Service 编排 Conversation 命令和查询，不复制 Workflow 或 Answer 状态机。
type Service struct {
	repository Repository
	ids        foundation.IDGenerator
	clock      foundation.Clock
}

// NewService 构造 fail-closed 的 Conversation Application Service。
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Repository == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, serviceUnavailable()
	}
	return &Service{repository: dependencies.Repository, ids: dependencies.IDs, clock: dependencies.Clock}, nil
}

// CreateConversation 规范化请求并创建或精确重放一个 Conversation。
func (service *Service) CreateConversation(ctx context.Context, command CreateConversationCommand) (CreateConversationResult, error) {
	if service == nil || service.repository == nil || service.ids == nil || service.clock == nil {
		return CreateConversationResult{}, serviceUnavailable()
	}
	request, err := conversationdomain.CanonicalizeConversationCreateRequest(conversationdomain.ConversationCreateRequest{
		WorkspaceID: command.WorkspaceID,
		Title:       command.Title,
	})
	if err != nil {
		return CreateConversationResult{}, err
	}
	idempotencyKey, err := canonicalIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return CreateConversationResult{}, err
	}
	requestHash, err := conversationdomain.ComputeConversationCreateRequestHash(request)
	if err != nil {
		return CreateConversationResult{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return CreateConversationResult{}, err
	}
	now := service.clock.Now().UTC()
	candidate := conversationdomain.Conversation{
		ID: id, WorkspaceID: request.WorkspaceID, Status: conversationdomain.ConversationStatusOpen,
		Title: request.Title, Version: 1, LastActivityAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := conversationdomain.ValidateConversation(candidate); err != nil {
		return CreateConversationResult{}, err
	}
	result, err := service.repository.CreateConversation(ctx, CreateConversationRecord{
		Conversation: candidate, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
	})
	if err != nil {
		return CreateConversationResult{}, err
	}
	if err := validateCreateConversationResult(request, result); err != nil {
		return CreateConversationResult{}, err
	}
	return result, nil
}

// ListConversations 返回一个经过边界和结果一致性校验的会话页面。
func (service *Service) ListConversations(ctx context.Context, query ListConversationsQuery) (ConversationPage, error) {
	if service == nil || service.repository == nil {
		return ConversationPage{}, serviceUnavailable()
	}
	if err := validateWorkspaceID(query.WorkspaceID); err != nil {
		return ConversationPage{}, err
	}
	if query.Cursor != nil {
		if err := query.Cursor.Validate(); err != nil {
			return ConversationPage{}, err
		}
	}
	if err := conversationdomain.ValidatePageLimit(query.Limit); err != nil {
		return ConversationPage{}, err
	}
	page, err := service.repository.ListConversations(ctx, query)
	if err != nil {
		return ConversationPage{}, err
	}
	if err := validateConversationPage(query, page); err != nil {
		return ConversationPage{}, err
	}
	return page, nil
}

// GetConversation 返回一个 Workspace 内可见的 Conversation。
func (service *Service) GetConversation(ctx context.Context, workspaceID, conversationID foundation.ID) (conversationdomain.Conversation, error) {
	if service == nil || service.repository == nil {
		return conversationdomain.Conversation{}, serviceUnavailable()
	}
	if err := validateScopedIDs(workspaceID, conversationID); err != nil {
		return conversationdomain.Conversation{}, err
	}
	conversation, err := service.repository.GetConversation(ctx, workspaceID, conversationID)
	if err != nil {
		return conversationdomain.Conversation{}, err
	}
	if err := conversationdomain.ValidateConversation(conversation); err != nil || conversation.WorkspaceID != workspaceID || conversation.ID != conversationID {
		return conversationdomain.Conversation{}, resultInconsistent(err)
	}
	return conversation, nil
}

// ListTurns 返回稳定分页且通过跨对象绑定校验的 Turn 读模型。
func (service *Service) ListTurns(ctx context.Context, query ListTurnsQuery) (TurnPage, error) {
	if service == nil || service.repository == nil {
		return TurnPage{}, serviceUnavailable()
	}
	if err := validateScopedIDs(query.WorkspaceID, query.ConversationID); err != nil {
		return TurnPage{}, err
	}
	if query.Cursor != nil {
		if err := query.Cursor.Validate(); err != nil {
			return TurnPage{}, err
		}
	}
	if err := conversationdomain.ValidatePageLimit(query.Limit); err != nil {
		return TurnPage{}, err
	}
	page, err := service.repository.ListTurns(ctx, query)
	if err != nil {
		return TurnPage{}, err
	}
	if err := validateTurnPage(query, page); err != nil {
		return TurnPage{}, err
	}
	return page, nil
}

// GetAnswer 返回一个 Workspace 内的 Answer 与 Workflow 权威状态。
func (service *Service) GetAnswer(ctx context.Context, workspaceID, answerID foundation.ID) (AnswerView, error) {
	if service == nil || service.repository == nil {
		return AnswerView{}, serviceUnavailable()
	}
	if err := validateScopedIDs(workspaceID, answerID); err != nil {
		return AnswerView{}, err
	}
	view, err := service.repository.GetAnswer(ctx, workspaceID, answerID)
	if err != nil {
		return AnswerView{}, err
	}
	if err := validateAnswerView(workspaceID, &view); err != nil || view.Answer.ID != answerID {
		return AnswerView{}, resultInconsistent(err)
	}
	return view, nil
}

// LoadPublishedContext 返回按时间顺序排列的有界已发布 Conversation 历史。
func (service *Service) LoadPublishedContext(ctx context.Context, query PublishedContextQuery) ([]conversationdomain.PublishedTurn, error) {
	if service == nil || service.repository == nil {
		return nil, serviceUnavailable()
	}
	if err := validateScopedIDs(query.WorkspaceID, query.ConversationID); err != nil || query.ThroughOrdinal < 0 {
		return nil, requestInvalid(err)
	}
	turns, err := service.repository.LoadPublishedContext(ctx, query)
	if err != nil {
		return nil, err
	}
	if _, through, _, err := conversationdomain.ComputeContextHash(turns); err != nil || through > query.ThroughOrdinal {
		return nil, resultInconsistent(err)
	}
	return turns, nil
}

func validateCreateConversationResult(request conversationdomain.ConversationCreateRequest, result CreateConversationResult) error {
	if err := conversationdomain.ValidateConversation(result.Conversation); err != nil ||
		result.Conversation.WorkspaceID != request.WorkspaceID || result.Conversation.Status != conversationdomain.ConversationStatusOpen ||
		!reflect.DeepEqual(result.Conversation.Title, request.Title) {
		return resultInconsistent(err)
	}
	return nil
}

func validateConversationPage(query ListConversationsQuery, page ConversationPage) error {
	if len(page.Items) > query.Limit || (page.NextCursor != nil && len(page.Items) == 0) {
		return resultInconsistent(nil)
	}
	for index, conversation := range page.Items {
		if err := conversationdomain.ValidateConversation(conversation); err != nil || conversation.WorkspaceID != query.WorkspaceID {
			return resultInconsistent(err)
		}
		if index > 0 {
			previous := page.Items[index-1]
			if conversation.LastActivityAt.After(previous.LastActivityAt) ||
				(conversation.LastActivityAt.Equal(previous.LastActivityAt) && conversation.ID <= previous.ID) {
				return resultInconsistent(nil)
			}
		}
	}
	if page.NextCursor != nil {
		last := page.Items[len(page.Items)-1]
		if err := page.NextCursor.Validate(); err != nil || !page.NextCursor.LastActivityAt.Equal(last.LastActivityAt) || page.NextCursor.ID != last.ID {
			return resultInconsistent(err)
		}
	}
	return nil
}

func validateTurnPage(query ListTurnsQuery, page TurnPage) error {
	if len(page.Items) > query.Limit || (page.NextCursor != nil && len(page.Items) == 0) {
		return resultInconsistent(nil)
	}
	for index := range page.Items {
		turn := &page.Items[index]
		if err := conversationdomain.ValidateQuestion(turn.Question); err != nil || turn.Question.Request.WorkspaceID != query.WorkspaceID ||
			turn.Question.Request.ConversationID != query.ConversationID || turn.Answer == nil {
			return resultInconsistent(err)
		}
		if err := validateAnswerView(query.WorkspaceID, turn.Answer); err != nil ||
			turn.Answer.Answer.ConversationID != query.ConversationID || turn.Answer.Answer.QuestionID != turn.Question.ID {
			return resultInconsistent(err)
		}
		if index > 0 {
			previous := page.Items[index-1].Question
			if turn.Question.Ordinal < previous.Ordinal ||
				(turn.Question.Ordinal == previous.Ordinal && turn.Question.ID <= previous.ID) {
				return resultInconsistent(nil)
			}
		}
	}
	if page.NextCursor != nil {
		last := page.Items[len(page.Items)-1].Question
		if err := page.NextCursor.Validate(); err != nil || page.NextCursor.Ordinal != last.Ordinal || page.NextCursor.QuestionID != last.ID {
			return resultInconsistent(err)
		}
	}
	return nil
}

func validateAnswerView(workspaceID foundation.ID, view *AnswerView) error {
	if view == nil {
		return resultInconsistent(nil)
	}
	if err := conversationdomain.ValidateAnswer(view.Answer); err != nil || view.Answer.WorkspaceID != workspaceID ||
		view.Workflow.RunID != view.Answer.WorkflowRunID || view.Workflow.Version < 1 || view.Workflow.UpdatedAt.IsZero() ||
		!validRunStatus(view.Workflow.Status) {
		return resultInconsistent(err)
	}
	if view.Answer.PublicationStatus == conversationdomain.AnswerPublicationPending {
		if view.AssistantText != "" || len(view.Citations) != 0 {
			return resultInconsistent(nil)
		}
		return nil
	}
	projection, err := conversationdomain.ProjectPublishedAnswer(view.Answer.ResultType, view.Answer.Result)
	if err != nil || projection.AssistantText != view.AssistantText || !reflect.DeepEqual(projection.Citations, view.Citations) {
		return resultInconsistent(err)
	}
	return nil
}

func validRunStatus(status workflowdomain.RunStatus) bool {
	switch status {
	case workflowdomain.RunStatusPending, workflowdomain.RunStatusRunning, workflowdomain.RunStatusWaitingForHuman,
		workflowdomain.RunStatusRetryWait, workflowdomain.RunStatusPaused, workflowdomain.RunStatusSucceeded,
		workflowdomain.RunStatusFailed, workflowdomain.RunStatusCancelled:
		return true
	default:
		return false
	}
}

func canonicalIdempotencyKey(value string) (string, error) {
	canonical := strings.TrimSpace(value)
	if canonical == "" || len(canonical) > MaxIdempotencyKeyBytes || !utf8.ValidString(canonical) ||
		strings.ContainsAny(canonical, "\r\n\x00") {
		return "", requestInvalid(nil)
	}
	return canonical, nil
}

func validateWorkspaceID(workspaceID foundation.ID) error {
	parsed, err := foundation.ParseID(string(workspaceID))
	if err != nil || parsed != workspaceID {
		return requestInvalid(err)
	}
	return nil
}

func validateScopedIDs(workspaceID, resourceID foundation.ID) error {
	if err := validateWorkspaceID(workspaceID); err != nil {
		return err
	}
	parsed, err := foundation.ParseID(string(resourceID))
	if err != nil || parsed != resourceID || resourceID == workspaceID {
		return requestInvalid(err)
	}
	return nil
}

func serviceUnavailable() error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false, errors.New("conversation service dependencies are incomplete"))
}

func requestInvalid(cause error) error {
	if cause == nil {
		cause = errors.New("conversation request is invalid")
	}
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeRequestInvalid, false, cause)
}

func resultInconsistent(cause error) error {
	if cause == nil {
		cause = errors.New("conversation repository returned an inconsistent result")
	}
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeResultInconsistent, false, cause)
}
