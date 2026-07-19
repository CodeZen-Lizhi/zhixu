package application

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestAnswerPublisherReturnsAnswerOnlyAfterAllFourGatesPass(t *testing.T) {
	answer, batch := validCitationAnswer(false)
	reviewer := &fakeFaithfulnessReviewer{result: FaithfulnessReviewRunResult{Review: validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"cite-1"})}}
	publisher := validAnswerPublisher(t, answer, reviewer, knowledgedomain.EvidenceEligible)
	result, err := publisher.Publish(context.Background(), publicationRequest(answer, batch))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Publishable() || result.Answer == nil || result.Answer.ModelRunRef != answer.ModelRunRef || result.Refusal != nil || reviewer.calls != 1 {
		t.Fatalf("result=%#v calls=%d", result, reviewer.calls)
	}
}

func TestAnswerPublisherRoutesDeterministicCitationAndFaithfulnessFailuresToRefusal(t *testing.T) {
	t.Run("unapproved evidence", func(t *testing.T) {
		answer, batch := validCitationAnswer(false)
		reviewer := &fakeFaithfulnessReviewer{result: FaithfulnessReviewRunResult{Review: validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"cite-1"})}}
		publisher := validAnswerPublisher(t, answer, reviewer, knowledgedomain.EvidenceIneligible)
		result, err := publisher.Publish(context.Background(), publicationRequest(answer, batch))
		if err != nil || result.Answer != nil || result.Refusal == nil ||
			result.Refusal.Payload.ReasonCode != domain.RefusalUnapprovedEvidenceOnly || reviewer.calls != 0 {
			t.Fatalf("result=%#v err=%v calls=%d", result, err, reviewer.calls)
		}
	})

	t.Run("unrelated semantic support", func(t *testing.T) {
		answer, batch := validCitationAnswer(false)
		review := validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"cite-1"})
		setReviewTargetVerdict(&review, "@answer/conclusion", domain.FaithfulnessUnsupported)
		reviewer := &fakeFaithfulnessReviewer{result: FaithfulnessReviewRunResult{Review: review}}
		publisher := validAnswerPublisher(t, answer, reviewer, knowledgedomain.EvidenceEligible)
		result, err := publisher.Publish(context.Background(), publicationRequest(answer, batch))
		if err != nil || result.Answer != nil || result.Refusal == nil ||
			result.Refusal.Payload.ReasonCode != domain.RefusalEvidenceInsufficient || result.Review == nil {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("invalid review output", func(t *testing.T) {
		answer, batch := validCitationAnswer(false)
		reviewer := &fakeFaithfulnessReviewer{err: foundation.NewError(
			foundation.ErrorNonRetryableFailure, domain.ErrorCodeValidationExhausted, false, errors.New("invalid review output"),
		)}
		publisher := validAnswerPublisher(t, answer, reviewer, knowledgedomain.EvidenceEligible)
		result, err := publisher.Publish(context.Background(), publicationRequest(answer, batch))
		if err != nil || result.Answer != nil || result.Refusal == nil ||
			result.Refusal.Payload.ReasonCode != domain.RefusalValidationExhausted {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
}

func TestAnswerPublisherRoutesStructuredLimitsAndBudgetsToValidationRefusal(t *testing.T) {
	answer, batch := validCitationAnswer(false)
	tests := []struct {
		name string
		code string
	}{
		{name: "strict json limit", code: domain.ErrorCodeStructuredOutputLimitExceeded},
		{name: "request budget", code: errorCodeRequestBudget},
		{name: "response budget", code: errorCodeResponseBudget},
		{name: "token budget", code: errorCodeTokenBudget},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reviewer := &fakeFaithfulnessReviewer{err: foundation.NewError(foundation.ErrorNonRetryableFailure, test.code, false, errors.New("bounded validation failure"))}
			publisher := validAnswerPublisher(t, answer, reviewer, knowledgedomain.EvidenceEligible)
			result, err := publisher.Publish(context.Background(), publicationRequest(answer, batch))
			if err != nil || result.Answer != nil || result.Refusal == nil ||
				result.Refusal.Payload.ReasonCode != domain.RefusalValidationExhausted {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestAnswerPublisherDoesNotPublishWhenReviewDependencyIsUnavailable(t *testing.T) {
	answer, batch := validCitationAnswer(false)
	unavailable := foundation.NewError(foundation.ErrorDependencyUnavailable, "REVIEW_MODEL_UNAVAILABLE", true, errors.New("review model unavailable"))
	reviewer := &fakeFaithfulnessReviewer{err: unavailable}
	publisher := validAnswerPublisher(t, answer, reviewer, knowledgedomain.EvidenceEligible)
	result, err := publisher.Publish(context.Background(), publicationRequest(answer, batch))
	if !errors.Is(err, unavailable) || result.Answer != nil || result.Publishable() || reviewer.calls != 1 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, reviewer.calls)
	}
}

func TestAnswerPublisherPreservesNonRetryableProviderFailure(t *testing.T) {
	answer, batch := validCitationAnswer(false)
	providerErr := foundation.NewError(foundation.ErrorNonRetryableFailure, "CHAT_PROVIDER_UNAUTHORIZED", false, errors.New("unauthorized"))
	reviewer := &fakeFaithfulnessReviewer{err: providerErr}
	publisher := validAnswerPublisher(t, answer, reviewer, knowledgedomain.EvidenceEligible)
	result, err := publisher.Publish(context.Background(), publicationRequest(answer, batch))
	if !errors.Is(err, providerErr) || result.Answer != nil || result.Refusal != nil || result.Publishable() {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func validAnswerPublisher(t *testing.T, answer domain.RAGAnswerResult, reviewer FaithfulnessReviewer, classification knowledgedomain.EvidenceEligibility) *AnswerPublisher {
	t.Helper()
	retrieval := &fakeAgentRetrieval{opened: map[string]OpenedEvidence{
		"cite-1": {Citation: answer.Payload.Citations[0], Excerpt: "immutable approved evidence"},
	}}
	eligibility := &fakeEligibility{classifications: map[string]knowledgedomain.EvidenceEligibility{
		string(answer.Payload.Citations[0].SourceVersionID): classification,
	}}
	validator := newCitationValidatorForTest(t, retrieval, eligibility)
	publisher, err := NewAnswerPublisher(validator, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	return publisher
}

func publicationRequest(answer domain.RAGAnswerResult, batch RetrievalBatch) AnswerPublicationRequest {
	return AnswerPublicationRequest{
		WorkspaceID: answer.Payload.Citations[0].WorkspaceID, Answer: answer, Retrieval: batch,
		ReviewProfileRef: domain.ModelProfileRef{ID: "review", Version: "v1"},
		ReviewPromptRef:  domain.PromptRef{ID: "faithfulness-review", Version: "v1"},
		ReviewSchemaRef:  domain.SchemaRef{ID: domain.FaithfulnessReviewSchemaID, Version: domain.OutputSchemaVersionV1},
	}
}

type fakeFaithfulnessReviewer struct {
	calls  int
	result FaithfulnessReviewRunResult
	err    error
}

func (fake *fakeFaithfulnessReviewer) Review(context.Context, FaithfulnessReviewRequest) (FaithfulnessReviewRunResult, error) {
	fake.calls++
	return fake.result, fake.err
}
