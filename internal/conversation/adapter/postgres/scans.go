package postgres

import (
	"errors"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

type scanner interface {
	Scan(...any) error
}

type conversationRecord struct {
	Conversation   conversationdomain.Conversation
	IdempotencyKey string
	RequestHash    string
}

func scanConversationRecord(row scanner) (conversationRecord, error) {
	var (
		record                  conversationRecord
		id, workspaceID, status string
		title                   *string
	)
	if err := row.Scan(
		&id, &workspaceID, &status, &title, &record.Conversation.Version,
		&record.Conversation.LastActivityAt, &record.Conversation.CreatedAt, &record.Conversation.UpdatedAt,
		&record.Conversation.ArchivedAt, &record.IdempotencyKey, &record.RequestHash,
	); err != nil {
		return conversationRecord{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	parsedID, err := parseCanonicalID(id)
	if err != nil {
		return conversationRecord{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	parsedWorkspaceID, err := parseCanonicalID(workspaceID)
	if err != nil {
		return conversationRecord{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	record.Conversation.ID = parsedID
	record.Conversation.WorkspaceID = parsedWorkspaceID
	record.Conversation.Status = conversationdomain.ConversationStatus(status)
	record.Conversation.Title = title
	if err := conversationdomain.ValidateConversation(record.Conversation); err != nil || !canonicalIdempotencyKey(record.IdempotencyKey) {
		return conversationRecord{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	request, err := conversationdomain.CanonicalizeConversationCreateRequest(conversationdomain.ConversationCreateRequest{
		WorkspaceID: record.Conversation.WorkspaceID,
		Title:       record.Conversation.Title,
	})
	if err != nil {
		return conversationRecord{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	hash, err := conversationdomain.ComputeConversationCreateRequestHash(request)
	if err != nil || hash != record.RequestHash {
		return conversationRecord{}, consistency(ErrorCodePersistenceCorrupt, errors.New("conversation request hash readback is inconsistent"))
	}
	return record, nil
}

func parseCanonicalID(value string) (foundation.ID, error) {
	parsed, err := foundation.ParseID(value)
	if err != nil || string(parsed) != value {
		return "", errors.New("persisted identity is not canonical")
	}
	return parsed, nil
}

type questionScan struct {
	id, workspaceID, conversationID string
	ordinal                         int64
	questionText, scope             string
	answerDepth, outputFormat       string
	contextThroughOrdinal           int64
	contextHash, requestHash        string
	createdAt                       time.Time
}

func (scan *questionScan) destinations() []any {
	return []any{
		&scan.id, &scan.workspaceID, &scan.conversationID, &scan.ordinal, &scan.questionText, &scan.scope,
		&scan.answerDepth, &scan.outputFormat, &scan.contextThroughOrdinal, &scan.contextHash, &scan.requestHash, &scan.createdAt,
	}
}

func (scan *questionScan) build() (conversationdomain.Question, error) {
	id, err := parseCanonicalID(scan.id)
	if err != nil {
		return conversationdomain.Question{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	workspaceID, err := parseCanonicalID(scan.workspaceID)
	if err != nil {
		return conversationdomain.Question{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	conversationID, err := parseCanonicalID(scan.conversationID)
	if err != nil {
		return conversationdomain.Question{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	scope, err := conversationdomain.DecodeQuestionScope(workspaceID, []byte(scan.scope))
	if err != nil {
		return conversationdomain.Question{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	question := conversationdomain.Question{
		ID: id,
		Request: conversationdomain.QuestionRequest{
			WorkspaceID: workspaceID, ConversationID: conversationID, QuestionText: scan.questionText,
			Scope: scope, AnswerDepth: conversationdomain.AnswerDepth(scan.answerDepth), OutputFormat: conversationdomain.OutputFormat(scan.outputFormat),
		},
		Ordinal: scan.ordinal, ContextThroughOrdinal: scan.contextThroughOrdinal, ContextHash: scan.contextHash,
		RequestHash: scan.requestHash, CreatedAt: scan.createdAt,
	}
	if err := conversationdomain.ValidateQuestion(question); err != nil {
		return conversationdomain.Question{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	return question, nil
}

func scanQuestion(row scanner) (conversationdomain.Question, error) {
	fields := &questionScan{}
	if err := row.Scan(fields.destinations()...); err != nil {
		return conversationdomain.Question{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	return fields.build()
}

type answerScan struct {
	id, workspaceID, conversationID, questionID, workflowRunID *string
	modelRunID                                                 *string
	publicationStatus                                          *string
	resultType, result, resultHash, retrievalSummary           *string
	version                                                    *int64
	createdAt, updatedAt                                       *time.Time
	publishedAt                                                *time.Time
	workflowID, workflowStatus                                 *string
	workflowVersion                                            *int64
	workflowUpdatedAt                                          *time.Time
}

func (scan *answerScan) destinations() []any {
	return []any{
		&scan.id, &scan.workspaceID, &scan.conversationID, &scan.questionID, &scan.workflowRunID,
		&scan.modelRunID, &scan.publicationStatus, &scan.resultType, &scan.result, &scan.resultHash, &scan.retrievalSummary,
		&scan.version, &scan.createdAt, &scan.updatedAt, &scan.publishedAt,
		&scan.workflowID, &scan.workflowStatus, &scan.workflowVersion, &scan.workflowUpdatedAt,
	}
}

func (scan *answerScan) build() (conversationapplication.AnswerView, error) {
	if scan.id == nil || scan.workspaceID == nil || scan.conversationID == nil || scan.questionID == nil || scan.workflowRunID == nil ||
		scan.publicationStatus == nil || scan.version == nil || scan.createdAt == nil || scan.updatedAt == nil ||
		scan.workflowID == nil || scan.workflowStatus == nil || scan.workflowVersion == nil || scan.workflowUpdatedAt == nil {
		return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, errors.New("turn is missing its answer or workflow projection"))
	}
	id, err := parseCanonicalID(*scan.id)
	if err != nil {
		return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	workspaceID, err := parseCanonicalID(*scan.workspaceID)
	if err != nil {
		return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	conversationID, err := parseCanonicalID(*scan.conversationID)
	if err != nil {
		return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	questionID, err := parseCanonicalID(*scan.questionID)
	if err != nil {
		return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	workflowRunID, err := parseCanonicalID(*scan.workflowRunID)
	if err != nil {
		return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	workflowID, err := parseCanonicalID(*scan.workflowID)
	if err != nil || workflowID != workflowRunID || *scan.workflowVersion < 1 || scan.workflowUpdatedAt.IsZero() ||
		!validWorkflowRunStatus(workflowdomain.RunStatus(*scan.workflowStatus)) {
		return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	answer := conversationdomain.Answer{
		ID: id, WorkspaceID: workspaceID, ConversationID: conversationID, QuestionID: questionID, WorkflowRunID: workflowRunID,
		PublicationStatus: conversationdomain.AnswerPublicationStatus(*scan.publicationStatus), Version: *scan.version,
		CreatedAt: *scan.createdAt, UpdatedAt: *scan.updatedAt, PublishedAt: scan.publishedAt,
	}
	if scan.modelRunID != nil {
		modelRunID, parseErr := parseCanonicalID(*scan.modelRunID)
		if parseErr != nil {
			return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, parseErr)
		}
		answer.ModelRunID = &modelRunID
	}
	if scan.resultType != nil {
		answer.ResultType = conversationdomain.AnswerResultType(*scan.resultType)
	}
	if scan.resultHash != nil {
		answer.ResultHash = *scan.resultHash
	}
	var projection conversationdomain.PublishedAnswerProjection
	if scan.result != nil {
		projection, err = conversationdomain.ProjectPublishedAnswer(answer.ResultType, []byte(*scan.result))
		if err != nil {
			return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, err)
		}
		answer.Result = append([]byte(nil), projection.Document...)
	}
	if scan.retrievalSummary != nil {
		summary, decodeErr := conversationdomain.DecodeRetrievalSummary(workspaceID, []byte(*scan.retrievalSummary))
		if decodeErr != nil {
			return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, decodeErr)
		}
		answer.RetrievalSummary = &summary
	}
	if err := conversationdomain.ValidateAnswer(answer); err != nil {
		return conversationapplication.AnswerView{}, consistency(ErrorCodePersistenceCorrupt, err)
	}
	return conversationapplication.AnswerView{
		Answer: answer,
		Workflow: conversationapplication.WorkflowRunView{
			RunID: workflowID, Status: workflowdomain.RunStatus(*scan.workflowStatus), Version: *scan.workflowVersion, UpdatedAt: *scan.workflowUpdatedAt,
		},
		AssistantText: projection.AssistantText,
		Citations:     append([]agentdomain.Citation(nil), projection.Citations...),
	}, nil
}

func scanTurnView(row scanner) (conversationapplication.TurnView, error) {
	questionFields := &questionScan{}
	answerFields := &answerScan{}
	destinations := append(questionFields.destinations(), answerFields.destinations()...)
	if err := row.Scan(destinations...); err != nil {
		return conversationapplication.TurnView{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	question, err := questionFields.build()
	if err != nil {
		return conversationapplication.TurnView{}, err
	}
	answer, err := answerFields.build()
	if err != nil {
		return conversationapplication.TurnView{}, err
	}
	if answer.Answer.QuestionID != question.ID || answer.Answer.WorkspaceID != question.Request.WorkspaceID ||
		answer.Answer.ConversationID != question.Request.ConversationID {
		return conversationapplication.TurnView{}, consistency(ErrorCodePersistenceCorrupt, errors.New("turn answer binding is inconsistent"))
	}
	return conversationapplication.TurnView{Question: question, Answer: &answer}, nil
}

func scanAnswerView(row scanner) (conversationapplication.AnswerView, error) {
	fields := &answerScan{}
	if err := row.Scan(fields.destinations()...); err != nil {
		return conversationapplication.AnswerView{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	return fields.build()
}

func validWorkflowRunStatus(status workflowdomain.RunStatus) bool {
	switch status {
	case workflowdomain.RunStatusPending, workflowdomain.RunStatusRunning, workflowdomain.RunStatusWaitingForHuman,
		workflowdomain.RunStatusRetryWait, workflowdomain.RunStatusPaused, workflowdomain.RunStatusSucceeded,
		workflowdomain.RunStatusFailed, workflowdomain.RunStatusCancelled:
		return true
	default:
		return false
	}
}
