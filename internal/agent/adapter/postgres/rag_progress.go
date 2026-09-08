package postgres

import (
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func validateRAGProgressRecord(record agentapplication.RAGProgressRecord) (int, error) {
	ids := []foundation.ID{record.WorkspaceID, record.WorkflowRunID, record.ConversationID, record.QuestionID, record.AnswerID, record.ModelRunID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return 0, foundation.NewError(foundation.ErrorInvalidInput, agentapplication.ErrorCodeRAGProgressUnknown, false, errors.New("rag progress identity is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return 0, foundation.NewError(foundation.ErrorInvalidInput, agentapplication.ErrorCodeRAGProgressUnknown, false, errors.New("rag progress identity is reused"))
		}
		seen[id] = struct{}{}
	}
	if record.OccurredAt.IsZero() || record.Update.RewriteCount < 0 || record.Update.RewriteCount > 3 ||
		record.Update.CandidateCount < 0 || record.Update.SelectedCount < 0 || record.Update.ConflictCount < 0 || record.Update.DegradationCount < 0 {
		return 0, foundation.NewError(foundation.ErrorInvalidInput, agentapplication.ErrorCodeRAGProgressUnknown, false, errors.New("rag progress counts or time are invalid"))
	}
	stages := map[agentapplication.RAGProgressStage]int{
		agentapplication.RAGProgressPlanStarted: 1, agentapplication.RAGProgressPlanCompleted: 2,
		agentapplication.RAGProgressRetrievalStarted: 3, agentapplication.RAGProgressRetrievalCompleted: 4,
		agentapplication.RAGProgressValidationStarted: 5, agentapplication.RAGProgressValidationCompleted: 6,
	}
	order, ok := stages[record.Update.Stage]
	if !ok {
		return 0, foundation.NewError(foundation.ErrorInvalidInput, agentapplication.ErrorCodeRAGProgressUnknown, false, errors.New("rag progress stage is invalid"))
	}
	return order, nil
}
