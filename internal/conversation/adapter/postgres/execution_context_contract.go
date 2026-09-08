package postgres

import (
	"errors"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

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
