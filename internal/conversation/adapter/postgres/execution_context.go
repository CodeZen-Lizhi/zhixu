package postgres

import (
	"context"
	"errors"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// LoadQuestionExecutionContext 校验 Workflow 输入与 Conversation 事实并返回冻结的有界上下文。
func (repository *Repository) LoadQuestionExecutionContext(
	ctx context.Context,
	query conversationapplication.QuestionExecutionContextQuery,
) (conversationapplication.QuestionExecutionContext, error) {
	if repository == nil || isNilInterface(repository.db) {
		return conversationapplication.QuestionExecutionContext{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if err := validateQuestionExecutionContextQuery(query); err != nil {
		return conversationapplication.QuestionExecutionContext{}, err
	}
	turn, err := scanTurnView(repository.db.QueryRow(ctx, `SELECT `+turnViewColumns+`
		FROM agent.question q
		JOIN agent.answer a ON a.question_id=q.id AND a.workspace_id=q.workspace_id AND a.conversation_id=q.conversation_id
		JOIN workflow.run w ON w.id=a.workflow_run_id AND w.workspace_id=a.workspace_id
		WHERE q.workspace_id=$1 AND q.conversation_id=$2 AND q.id=$3`,
		string(query.WorkspaceID), string(query.ConversationID), string(query.QuestionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationapplication.QuestionExecutionContext{}, consistency(ErrorCodeExecutionContextCorrupt, errors.New("question execution context is missing"))
	}
	if err != nil {
		return conversationapplication.QuestionExecutionContext{}, err
	}
	if turn.Answer == nil || turn.Question.ID != query.QuestionID || turn.Question.Ordinal != query.QuestionOrdinal ||
		turn.Question.Request.WorkspaceID != query.WorkspaceID || turn.Question.Request.ConversationID != query.ConversationID ||
		turn.Question.ContextHash != query.ContextHash || turn.Answer.Answer.ID != query.AnswerID ||
		turn.Answer.Answer.WorkflowRunID != query.WorkflowRunID {
		return conversationapplication.QuestionExecutionContext{}, consistency(ErrorCodeExecutionContextCorrupt, errors.New("question execution binding differs"))
	}
	history, err := loadPublishedContext(ctx, repository.db, conversationapplication.PublishedContextQuery{
		WorkspaceID: query.WorkspaceID, ConversationID: query.ConversationID,
		ThroughOrdinal: turn.Question.ContextThroughOrdinal,
	})
	if err != nil {
		return conversationapplication.QuestionExecutionContext{}, err
	}
	hash, through, _, err := conversationdomain.ComputeContextHash(history)
	if err != nil || hash != query.ContextHash || through != turn.Question.ContextThroughOrdinal {
		return conversationapplication.QuestionExecutionContext{}, consistency(ErrorCodeExecutionContextCorrupt, errors.New("question execution context hash differs"))
	}
	return conversationapplication.QuestionExecutionContext{
		Question: turn.Question, Answer: turn.Answer.Answer, History: history,
	}, nil
}

func validateQuestionExecutionContextQuery(query conversationapplication.QuestionExecutionContextQuery) error {
	identities := []foundation.ID{
		query.WorkspaceID, query.WorkflowRunID, query.ConversationID, query.QuestionID, query.AnswerID,
	}
	seen := make(map[foundation.ID]struct{}, len(identities))
	for _, identity := range identities {
		if !validCanonicalID(identity) {
			return invalid(ErrorCodeExecutionContextInvalid, errors.New("question execution identity is invalid"))
		}
		if _, duplicate := seen[identity]; duplicate {
			return invalid(ErrorCodeExecutionContextInvalid, errors.New("question execution identity is reused"))
		}
		seen[identity] = struct{}{}
	}
	if query.QuestionOrdinal < 1 || conversationdomain.ValidateContextHash(query.ContextHash) != nil {
		return invalid(ErrorCodeExecutionContextInvalid, errors.New("question execution ordinal or context hash is invalid"))
	}
	return nil
}

var _ conversationapplication.QuestionExecutionContextLoader = (*Repository)(nil)
