package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCardRequiresFormalClaimAndEvidence(t *testing.T) {
	card := validCard(t)
	card.ClaimID = nil
	if err := ValidateCard(card); err == nil {
		t.Fatal("card without claim was accepted")
	}
	card = validCard(t)
	card.Evidence = nil
	if err := ValidateCard(card); err == nil {
		t.Fatal("card without evidence was accepted")
	}
}

func TestCardFingerprintIsStableAcrossEvidenceOrder(t *testing.T) {
	card := validCard(t)
	second := card.Evidence[0]
	second.SourceVersionID = foundation.ID("30000000-0000-4000-8000-000000000005")
	second.SourceSpanID = foundation.ID("30000000-0000-4000-8000-000000000006")
	second.EvidenceHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	card.Evidence = append(card.Evidence, second)
	first, err := ComputeCardFingerprint(card)
	if err != nil {
		t.Fatal(err)
	}
	card.Evidence[0], card.Evidence[1] = card.Evidence[1], card.Evidence[0]
	secondHash, err := ComputeCardFingerprint(card)
	if err != nil {
		t.Fatal(err)
	}
	if first != secondHash {
		t.Fatalf("fingerprint changed with evidence order: %s != %s", first, secondHash)
	}
}

func TestScoreRequiresDimensionExplanations(t *testing.T) {
	score := validScore(t)
	score.Correctness.Rationale = ""
	if err := ValidateScore(score); err == nil {
		t.Fatal("score without rationale was accepted")
	}
}

func TestScoreRejectsOversizedOrInvalidUTF8Text(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	tests := []struct {
		name   string
		mutate func(*Score)
	}{
		{name: "oversized rationale", mutate: func(score *Score) { score.Correctness.Rationale = strings.Repeat("x", MaxScoreRationaleBytes+1) }},
		{name: "invalid rationale utf8", mutate: func(score *Score) { score.Correctness.Rationale = invalidUTF8 }},
		{name: "oversized error", mutate: func(score *Score) { score.Errors = []string{strings.Repeat("x", MaxScoreFeedbackItemBytes+1)} }},
		{name: "invalid omission utf8", mutate: func(score *Score) { score.Omissions = []string{invalidUTF8} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			score := validScore(t)
			test.mutate(&score)
			if err := ValidateScore(score); err == nil {
				t.Fatal("invalid score text was accepted")
			}
		})
	}
}

func TestAnswerRejectsOversizedOrInvalidUTF8Text(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	tests := []struct {
		name   string
		mutate func(*Answer)
	}{
		{name: "oversized question ref", mutate: func(answer *Answer) { answer.QuestionRef = strings.Repeat("x", MaxQuestionRefBytes+1) }},
		{name: "oversized user answer", mutate: func(answer *Answer) { answer.UserAnswer = strings.Repeat("x", MaxUserAnswerBytes+1) }},
		{name: "invalid user answer utf8", mutate: func(answer *Answer) { answer.UserAnswer = invalidUTF8 }},
		{name: "empty scorer version", mutate: func(answer *Answer) { answer.ScorerVersion = "" }},
		{name: "padded scorer version", mutate: func(answer *Answer) { answer.ScorerVersion = " review-test/v1 " }},
		{name: "oversized scorer version", mutate: func(answer *Answer) { answer.ScorerVersion = strings.Repeat("x", MaxScorerVersionBytes+1) }},
		{name: "invalid scorer version utf8", mutate: func(answer *Answer) { answer.ScorerVersion = invalidUTF8 }},
		{name: "oversized feedback", mutate: func(answer *Answer) {
			answer.Feedback = map[string]any{"detail": strings.Repeat("x", MaxAnswerFeedbackBytes+1)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			answer := validAnswer(t)
			test.mutate(&answer)
			if err := ValidateAnswer(answer); err == nil {
				t.Fatal("invalid answer text was accepted")
			}
		})
	}
}

func TestAnswerRejectsUnencodableFeedback(t *testing.T) {
	answer := validAnswer(t)
	answer.Feedback = map[string]any{"unsupported": make(chan int)}
	if err := ValidateAnswer(answer); err == nil {
		t.Fatal("unencodable answer feedback was accepted")
	}
}

func TestDeckBoundReviewSessionPredicate(t *testing.T) {
	deckID := foundation.ID("30000000-0000-4000-8000-000000000004")
	if !IsDeckBoundReviewSession(Session{SessionType: SessionTypeReview, DeckID: &deckID}) {
		t.Fatal("deck-bound Review session was rejected")
	}
	if IsDeckBoundReviewSession(Session{SessionType: SessionTypeReview}) {
		t.Fatal("unbound Review session was accepted")
	}
	if IsDeckBoundReviewSession(Session{SessionType: SessionTypeInterview, DeckID: &deckID}) {
		t.Fatal("Interview session was accepted as Review")
	}
}

func validCard(t *testing.T) Card {
	t.Helper()
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	claimID := foundation.ID("30000000-0000-4000-8000-000000000001")
	return Card{
		ID: foundation.ID("30000000-0000-4000-8000-000000000002"), WorkspaceID: foundation.ID("30000000-0000-4000-8000-000000000003"), DeckID: foundation.ID("30000000-0000-4000-8000-000000000004"), ClaimID: &claimID,
		Question: "为什么 Answer 与 Schedule 必须同事务？", AnswerPoints: []string{"避免重复推进调度"},
		Evidence: []EvidenceBinding{{SchemaVersion: EvidenceSchemaVersionV1, ClaimID: claimID, SourceVersionID: foundation.ID("30000000-0000-4000-8000-000000000005"), SourceSpanID: foundation.ID("30000000-0000-4000-8000-000000000006"), EvidenceHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
		CardType: CardTypeShortAnswer, Difficulty: 0.5, Status: CardStatusDraft, ModelVersion: "manual", Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func validScore(t *testing.T) Score {
	t.Helper()
	card := validCard(t)
	dimension := ScoreDimension{Value: 0.8, Rationale: "与绑定证据一致"}
	return Score{SchemaVersion: ScoreSchemaVersionV1, Correctness: dimension, Coverage: dimension, Boundaries: dimension, Clarity: dimension, Confidence: dimension, Evidence: card.Evidence}
}

func validAnswer(t *testing.T) Answer {
	t.Helper()
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	cardID := foundation.ID("30000000-0000-4000-8000-000000000002")
	return Answer{
		ID: foundation.ID("30000000-0000-4000-8000-000000000007"), WorkspaceID: foundation.ID("30000000-0000-4000-8000-000000000003"), SessionID: foundation.ID("30000000-0000-4000-8000-000000000008"), CardID: &cardID,
		QuestionRef: "card:1", IdempotencyKey: "answer-1", UserAnswer: "同一事务避免重复推进", Rating: RatingGood, ScorerVersion: "review-test/v1", Score: validScore(t), Feedback: map[string]any{}, CreatedAt: now,
	}
}
