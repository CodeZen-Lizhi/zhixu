package application

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func TestDeterministicScorerPersonalizationChangesOnlyFeedback(t *testing.T) {
	scorer := DeterministicScorer{}
	question := scorerQuestion(t)
	input := ScoreInput{Question: question, UserAnswer: "A service preserves evidence-backed idempotency only when retries use the same request."}
	baseline, err := scorer.Score(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.PersonalContext = PersonalContext{
		Preferences: []json.RawMessage{json.RawMessage(`{"answer_style":"concise"}`)},
		Context:     []json.RawMessage{json.RawMessage(`{"goal":"practice-boundaries"}`)},
	}
	personalized, err := scorer.Score(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Correctness.Value != personalized.Correctness.Value || baseline.Coverage.Value != personalized.Coverage.Value ||
		baseline.Boundaries.Value != personalized.Boundaries.Value || baseline.Clarity.Value != personalized.Clarity.Value {
		t.Fatalf("personal context changed numeric score: baseline=%+v personalized=%+v", baseline, personalized)
	}
	if !reflect.DeepEqual(baseline.Evidence, personalized.Evidence) {
		t.Fatalf("personal context changed evidence: baseline=%+v personalized=%+v", baseline.Evidence, personalized.Evidence)
	}
	if baseline.Correctness.Rationale == personalized.Correctness.Rationale || baseline.Boundaries.Rationale == personalized.Boundaries.Rationale {
		t.Fatalf("whitelisted context did not change stable feedback: baseline=%+v personalized=%+v", baseline, personalized)
	}
}

func TestDeterministicScorerIgnoresUnknownPersonalContext(t *testing.T) {
	scorer := DeterministicScorer{}
	question := scorerQuestion(t)
	input := ScoreInput{Question: question, UserAnswer: "A service preserves evidence-backed idempotency."}
	baseline, err := scorer.Score(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	tests := []PersonalContext{
		{Preferences: []json.RawMessage{json.RawMessage(`{"answer_style":"verbose"}`)}},
		{Context: []json.RawMessage{json.RawMessage(`{"goal":"anything-else"}`)}},
		{Preferences: []json.RawMessage{json.RawMessage(`{"unknown":"value"}`)}},
		{Preferences: []json.RawMessage{json.RawMessage(`{"answer_style":"concise","unknown":"value"}`)}},
	}
	for index, personalContext := range tests {
		input.PersonalContext = personalContext
		actual, err := scorer.Score(context.Background(), input)
		if err != nil {
			t.Fatalf("case %d: %v", index, err)
		}
		if !reflect.DeepEqual(actual, baseline) {
			t.Fatalf("case %d changed score for unknown context: baseline=%+v actual=%+v", index, baseline, actual)
		}
	}
}

func scorerQuestion(t *testing.T) domain.Question {
	t.Helper()
	material := applicationMaterial(t)
	return domain.Question{
		ID: applicationID(201), WorkspaceID: applicationID(1), SessionID: applicationID(202), QuestionNo: 1,
		ClaimID: material.ClaimID, Prompt: "Explain evidence-backed idempotency.", AnswerPoints: material.AnswerPoints,
		Evidence: material.Evidence, Status: domain.QuestionStatusPending, Fingerprint: strings.Repeat("f", 64),
		CreatedAt: time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC),
	}
}
