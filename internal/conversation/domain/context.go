package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxContextTurns 是一次执行可以使用的已发布历史 Turn 上限。
	MaxContextTurns = 8
	// MaxContextBytes 是历史 Question 与 Assistant 投影的合计 UTF-8 字节上限。
	MaxContextBytes = 32 * 1024

	maxContextAssistantBytes = 16 * 1024
)

// ValidateContextHash 校验 Workflow 与 Question 共享的 canonical SHA-256 上下文哈希。
func ValidateContextHash(value string) error {
	if !validLowerHash(value) {
		return invalid(ErrorCodeContextInvalid, "conversation context hash is invalid", nil)
	}
	return nil
}

// PublishedTurn 是 Query Plan 可消费的一组已发布 Question/Answer 文本投影。
type PublishedTurn struct {
	QuestionID    foundation.ID
	AnswerID      foundation.ID
	Ordinal       int64
	QuestionText  string
	AssistantText string
	ResultType    string
	ResultHash    string
}

// ComputeContextHash 校验有界顺序上下文并返回稳定哈希、最大 ordinal 与文本字节数。
func ComputeContextHash(turns []PublishedTurn) (string, int64, int, error) {
	if len(turns) > MaxContextTurns {
		return "", 0, 0, invalid(ErrorCodeContextInvalid, "conversation context contains too many turns", nil)
	}
	type hashTurn struct {
		QuestionID    foundation.ID `json:"question_id"`
		AnswerID      foundation.ID `json:"answer_id"`
		Ordinal       int64         `json:"ordinal"`
		QuestionText  string        `json:"question_text"`
		AssistantText string        `json:"assistant_text"`
		ResultType    string        `json:"result_type"`
		ResultHash    string        `json:"result_hash"`
	}
	canonical := make([]hashTurn, 0, len(turns))
	seenIDs := make(map[foundation.ID]struct{}, len(turns)*2)
	var through int64
	byteCount := 0
	for _, turn := range turns {
		questionID, questionErr := foundation.ParseID(string(turn.QuestionID))
		answerID, answerErr := foundation.ParseID(string(turn.AnswerID))
		if questionErr != nil || answerErr != nil || questionID != turn.QuestionID || answerID != turn.AnswerID ||
			questionID == answerID || turn.Ordinal <= through || !validContextResultType(turn.ResultType) ||
			!validLowerHash(turn.ResultHash) || !validBoundedText(turn.QuestionText, MaxQuestionBytes, true) ||
			!validBoundedText(turn.AssistantText, maxContextAssistantBytes, true) {
			return "", 0, 0, invalid(ErrorCodeContextInvalid, "conversation context turn is invalid", nil)
		}
		if _, duplicate := seenIDs[questionID]; duplicate {
			return "", 0, 0, invalid(ErrorCodeContextInvalid, "conversation context identity is duplicated", nil)
		}
		seenIDs[questionID] = struct{}{}
		if _, duplicate := seenIDs[answerID]; duplicate {
			return "", 0, 0, invalid(ErrorCodeContextInvalid, "conversation context identity is duplicated", nil)
		}
		seenIDs[answerID] = struct{}{}
		byteCount += len(turn.QuestionText) + len(turn.AssistantText)
		if byteCount > MaxContextBytes {
			return "", 0, 0, invalid(ErrorCodeContextInvalid, "conversation context exceeds byte budget", nil)
		}
		through = turn.Ordinal
		canonical = append(canonical, hashTurn{
			QuestionID: questionID, AnswerID: answerID, Ordinal: turn.Ordinal,
			QuestionText: turn.QuestionText, AssistantText: turn.AssistantText,
			ResultType: turn.ResultType, ResultHash: turn.ResultHash,
		})
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion int        `json:"schema_version"`
		Turns         []hashTurn `json:"turns"`
	}{SchemaVersion: 1, Turns: canonical})
	if err != nil {
		return "", 0, 0, inconsistent(ErrorCodeContextInvalid, "conversation context hash payload is invalid")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), through, byteCount, nil
}

func validContextResultType(value string) bool {
	return value == agentdomain.ResultTypeRAGAnswer || value == agentdomain.ResultTypeRefusal || value == agentdomain.ResultTypeClarification
}

func validLowerHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
