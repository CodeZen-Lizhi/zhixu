package domain

import (
	"fmt"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestComputeContextHashBindsOrderedPublishedTurnsWithinBudget(t *testing.T) {
	emptyHash, through, bytes, err := ComputeContextHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	if emptyHash != "f808b8be2cf96b570393ed5a85dd9973162459f7c1e597537457ac987d87a84a" || through != 0 || bytes != 0 {
		t.Fatalf("empty hash=%s through=%d bytes=%d", emptyHash, through, bytes)
	}

	turns := []PublishedTurn{
		{
			QuestionID: "10000000-0000-4000-8000-000000000010", AnswerID: "10000000-0000-4000-8000-000000000011",
			Ordinal: 2, QuestionText: "What changed?", AssistantText: "The approved runtime contract changed.",
			ResultType: "rag_answer", ResultHash: strings.Repeat("a", 64),
		},
		{
			QuestionID: "10000000-0000-4000-8000-000000000012", AnswerID: "10000000-0000-4000-8000-000000000013",
			Ordinal: 4, QuestionText: "Which scope?", AssistantText: "Please select production or development.",
			ResultType: "clarification", ResultHash: strings.Repeat("b", 64),
		},
	}
	hash, through, bytes, err := ComputeContextHash(turns)
	if err != nil {
		t.Fatal(err)
	}
	if hash == emptyHash || through != 4 || bytes != 103 {
		t.Fatalf("hash=%s through=%d bytes=%d", hash, through, bytes)
	}
	turns[0].QuestionText = "mutated"
	if through != 4 {
		t.Fatal("result changed after caller mutation")
	}
}

func TestComputeContextHashRejectsInvalidHistory(t *testing.T) {
	valid := PublishedTurn{
		QuestionID: "10000000-0000-4000-8000-000000000010", AnswerID: "10000000-0000-4000-8000-000000000011",
		Ordinal: 1, QuestionText: "Question", AssistantText: "Answer", ResultType: "rag_answer", ResultHash: strings.Repeat("a", 64),
	}
	tests := []struct {
		name  string
		turns []PublishedTurn
	}{
		{name: "too many turns", turns: repeatPublishedTurn(valid, MaxContextTurns+1)},
		{name: "duplicate identities", turns: []PublishedTurn{valid, valid}},
		{name: "out of order", turns: []PublishedTurn{withOrdinal(valid, 2), withOrdinal(withIDs(valid, 12), 1)}},
		{name: "nul text", turns: []PublishedTurn{func() PublishedTurn { value := valid; value.AssistantText = "bad\x00answer"; return value }()}},
		{name: "oversized context", turns: []PublishedTurn{func() PublishedTurn {
			value := valid
			value.AssistantText = strings.Repeat("x", MaxContextBytes)
			return value
		}()}},
		{name: "unknown result", turns: []PublishedTurn{func() PublishedTurn { value := valid; value.ResultType = "draft"; return value }()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := ComputeContextHash(test.turns); errorCode(err) != ErrorCodeContextInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func repeatPublishedTurn(base PublishedTurn, count int) []PublishedTurn {
	result := make([]PublishedTurn, count)
	for index := range result {
		result[index] = withOrdinal(withIDs(base, index+20), int64(index+1))
	}
	return result
}

func withOrdinal(value PublishedTurn, ordinal int64) PublishedTurn {
	value.Ordinal = ordinal
	return value
}

func withIDs(value PublishedTurn, suffix int) PublishedTurn {
	value.QuestionID = testID(suffix)
	value.AnswerID = testID(suffix + 100)
	return value
}

func testID(suffix int) foundation.ID {
	return foundation.ID(fmt.Sprintf("10000000-0000-4000-8000-%012d", suffix))
}
