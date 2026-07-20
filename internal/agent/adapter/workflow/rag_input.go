package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type ragModelHistoryTurn struct {
	Ordinal       int64  `json:"ordinal"`
	QuestionText  string `json:"question_text"`
	AssistantText string `json:"assistant_text"`
	ResultType    string `json:"result_type"`
}

type ragModelScope struct {
	RetrievalMode        string          `json:"retrieval_mode"`
	SourceIDs            []foundation.ID `json:"source_ids"`
	SourceVersionIDs     []foundation.ID `json:"source_version_ids"`
	PathPrefixes         []string        `json:"path_prefixes"`
	CapturedAtFrom       *time.Time      `json:"captured_at_from"`
	CapturedAtBefore     *time.Time      `json:"captured_at_before"`
	AllowOriginalSources bool            `json:"allow_original_sources"`
	AllowWeb             bool            `json:"allow_web"`
}

type ragModelInput struct {
	SchemaVersion int                   `json:"schema_version"`
	UntrustedData bool                  `json:"untrusted_data"`
	Question      string                `json:"question"`
	History       []ragModelHistoryTurn `json:"history"`
	Scope         ragModelScope         `json:"scope"`
	AnswerDepth   string                `json:"answer_depth"`
	OutputFormat  string                `json:"output_format"`
}

// buildRAGModelInputs 只从已校验的 Conversation 执行上下文构造有界、不持久化的模型输入。
func buildRAGModelInputs(execution conversationapplication.QuestionExecutionContext) ([]byte, []byte, error) {
	if err := conversationdomain.ValidateQuestion(execution.Question); err != nil ||
		conversationdomain.ValidateAnswer(execution.Answer) != nil ||
		execution.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending ||
		execution.Answer.QuestionID != execution.Question.ID ||
		execution.Answer.WorkspaceID != execution.Question.Request.WorkspaceID ||
		execution.Answer.ConversationID != execution.Question.Request.ConversationID {
		return nil, nil, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false, errors.New("rag execution context binding is invalid"))
	}
	contextHash, throughOrdinal, _, err := conversationdomain.ComputeContextHash(execution.History)
	if err != nil || contextHash != execution.Question.ContextHash || throughOrdinal != execution.Question.ContextThroughOrdinal {
		return nil, nil, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false, errors.New("rag execution context hash is inconsistent"))
	}

	history := make([]ragModelHistoryTurn, len(execution.History))
	for index, turn := range execution.History {
		history[index] = ragModelHistoryTurn{
			Ordinal: turn.Ordinal, QuestionText: turn.QuestionText,
			AssistantText: turn.AssistantText, ResultType: turn.ResultType,
		}
	}
	scope := execution.Question.Request.Scope
	payload := ragModelInput{
		SchemaVersion: 1,
		UntrustedData: true,
		Question:      execution.Question.Request.QuestionText,
		History:       history,
		Scope: ragModelScope{
			RetrievalMode:        string(scope.RetrievalMode),
			SourceIDs:            append([]foundation.ID(nil), scope.Filter.SourceIDs...),
			SourceVersionIDs:     append([]foundation.ID(nil), scope.Filter.SourceVersionIDs...),
			PathPrefixes:         append([]string(nil), scope.Filter.PathPrefixes...),
			CapturedAtFrom:       scope.Filter.CapturedAtFrom,
			CapturedAtBefore:     scope.Filter.CapturedAtBefore,
			AllowOriginalSources: scope.AllowOriginalSources,
			AllowWeb:             scope.AllowWeb,
		},
		AnswerDepth:  string(execution.Question.Request.AnswerDepth),
		OutputFormat: string(execution.Question.Request.OutputFormat),
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) == 0 || len(encoded) > agentapplication.MaxStructuredInputBytes || bytes.IndexByte(encoded, 0) >= 0 {
		return nil, nil, workflowError(foundation.ErrorNonRetryableFailure, ErrorCodeInputInvalid, false, errors.New("rag model input could not be encoded within the budget"))
	}
	return append([]byte(nil), encoded...), append([]byte(nil), encoded...), nil
}
