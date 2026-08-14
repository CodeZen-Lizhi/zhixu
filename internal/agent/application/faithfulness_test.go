package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestStructuredFaithfulnessReviewerUsesIndependentReviewPhaseAndSchema(t *testing.T) {
	answer, _ := validCitationAnswer(false)
	review := validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"cite-1"})
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	catalog, prompt, schema, profile := faithfulnessCatalog(t)
	model := NewDeterministicChatModel(DeterministicChatStep{Response: ChatResponse{
		Model: profile.Model, Content: raw, Usage: domain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}})
	reviewer, err := NewStructuredFaithfulnessReviewer(model, catalog, DefaultRunBudget())
	if err != nil {
		t.Fatal(err)
	}
	result, err := reviewer.Review(context.Background(), FaithfulnessReviewRequest{
		ProfileRef: profile.Ref, PromptRef: prompt.Ref, SchemaRef: schema.Ref,
		Answer: answer, Evidence: faithfulnessEvidenceFor(answer),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Review.Payload.Passed != true || result.Usage.TotalTokens != 15 || result.Runtime.Schema != schema.Ref || model.CallCount() != 1 {
		t.Fatalf("result=%#v calls=%d", result, model.CallCount())
	}
	call := model.Calls()[0]
	if call.Phase != domain.ModelCallReview || call.SchemaRef != schema.Ref || len(call.Messages) != 3 ||
		!strings.Contains(call.Messages[2].Content, "UNTRUSTED TASK INPUT") ||
		!strings.Contains(call.Messages[2].Content, `"model_run_ref":"`+string(answer.ModelRunRef)+`"`) {
		t.Fatalf("review call=%#v", call)
	}
	if call.MaxOutputTokens != 1024 {
		t.Fatalf("review output cap=%d want=1024", call.MaxOutputTokens)
	}
}

func TestFaithfulnessOutputBudgetUsesReasoningProviderFloor(t *testing.T) {
	answer, _ := validCitationAnswer(false)
	if got := effectiveFaithfulnessOutputTokens(8192, 0, answer); got != 1024 {
		t.Fatalf("faithfulness output floor=%d want=1024", got)
	}
	if got := effectiveFaithfulnessOutputTokens(512, 0, answer); got != 512 {
		t.Fatalf("profile must still narrow faithfulness output floor: %d", got)
	}
}

func TestStructuredFaithfulnessReviewerHonorsExplicitOutputLimit(t *testing.T) {
	answer, _ := validCitationAnswer(false)
	review := validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"cite-1"})
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	catalog, prompt, schema, profile := faithfulnessCatalog(t)
	model := NewDeterministicChatModel(DeterministicChatStep{Response: ChatResponse{
		Model: profile.Model, Content: raw, Usage: domain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}})
	reviewer, err := NewStructuredFaithfulnessReviewer(model, catalog, DefaultRunBudget())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewer.Review(context.Background(), FaithfulnessReviewRequest{
		ProfileRef: profile.Ref, PromptRef: prompt.Ref, SchemaRef: schema.Ref,
		Answer: answer, Evidence: faithfulnessEvidenceFor(answer), MaxOutputTokens: 123,
	}); err != nil {
		t.Fatal(err)
	}
	if calls := model.Calls(); len(calls) != 1 || calls[0].MaxOutputTokens != 123 {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestFaithfulnessOutputBudgetScalesPastSmallFixedCeiling(t *testing.T) {
	answer, _ := validCitationAnswer(false)
	assertions := make([]domain.Assertion, 0, 40)
	for index := 0; index < 40; index++ {
		assertion := answer.Payload.Assertions[0]
		assertion.ID = fmt.Sprintf("assertion-%d", index)
		assertions = append(assertions, assertion)
	}
	answer.Payload.Assertions = assertions
	if err := answer.Validate(); err != nil {
		t.Fatal(err)
	}
	budget := effectiveFaithfulnessOutputTokens(8192, 0, answer)
	if budget <= 2048 || budget > 8192 {
		t.Fatalf("faithfulness output budget=%d", budget)
	}
}

func TestStructuredFaithfulnessReviewerRejectsRunIdentityDrift(t *testing.T) {
	answer, _ := validCitationAnswer(false)
	review := validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"cite-1"})
	review.ModelRunRef = "52000000-0000-4000-8000-000000000099"
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	catalog, prompt, schema, profile := faithfulnessCatalog(t)
	model := NewDeterministicChatModel(DeterministicChatStep{Response: ChatResponse{
		Model: profile.Model, Content: raw, Usage: domain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}})
	reviewer, err := NewStructuredFaithfulnessReviewer(model, catalog, DefaultRunBudget())
	if err != nil {
		t.Fatal(err)
	}
	_, err = reviewer.Review(context.Background(), FaithfulnessReviewRequest{
		ProfileRef: profile.Ref, PromptRef: prompt.Ref, SchemaRef: schema.Ref, Answer: answer, Evidence: faithfulnessEvidenceFor(answer),
	})
	if applicationErrorCode(err) != errorCodeFaithfulnessResponseInvalid {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateFaithfulnessReviewRejectsUnsupportedAndUnrelatedSupport(t *testing.T) {
	answer, _ := validCitationAnswer(false)
	valid := validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"cite-1"})
	if err := ValidateFaithfulnessReview(answer, valid); err != nil {
		t.Fatal(err)
	}

	unsupported := validFaithfulnessResult(answer, domain.FaithfulnessUnsupported, []string{"cite-1"})
	if applicationErrorCode(ValidateFaithfulnessReview(answer, unsupported)) != errorCodeFaithfulnessRejected {
		t.Fatal("unsupported assertion was accepted")
	}

	unrelated := validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"unrelated-citation"})
	if applicationErrorCode(ValidateFaithfulnessReview(answer, unrelated)) != errorCodeFaithfulnessRejected {
		t.Fatal("review using an unrelated citation was accepted")
	}

	missingConclusion := validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"cite-1"})
	missingConclusion.Payload.Items = missingConclusion.Payload.Items[:1]
	if applicationErrorCode(ValidateFaithfulnessReview(answer, missingConclusion)) != errorCodeFaithfulnessResponseInvalid {
		t.Fatal("review omitting the publishable conclusion was accepted")
	}

	unsupportedConclusion := validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"cite-1"})
	setReviewTargetVerdict(&unsupportedConclusion, "@answer/conclusion", domain.FaithfulnessUnsupported)
	if applicationErrorCode(ValidateFaithfulnessReview(answer, unsupportedConclusion)) != errorCodeFaithfulnessRejected {
		t.Fatal("unsupported conclusion with a valid citation was accepted")
	}
}

func TestStructuredFaithfulnessReviewerRejectsDepthArrayAndStringLimits(t *testing.T) {
	answer, _ := validCitationAnswer(false)
	deep := strings.Repeat(`{"a":`, 17) + `0` + strings.Repeat(`}`, 17)
	array := `{"a":[` + strings.Repeat(`0,`, domain.DefaultDecodeLimits().MaxArrayItems) + `0]}`
	longString := `{"a":"` + strings.Repeat("x", domain.DefaultDecodeLimits().MaxStringBytes+1) + `"}`
	for name, raw := range map[string]string{"depth": deep, "array": array, "string": longString} {
		t.Run(name, func(t *testing.T) {
			catalog, prompt, schema, profile := faithfulnessCatalog(t)
			model := NewDeterministicChatModel(DeterministicChatStep{Response: ChatResponse{
				Model: profile.Model, Content: []byte(raw), Usage: domain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			}})
			reviewer, err := NewStructuredFaithfulnessReviewer(model, catalog, DefaultRunBudget())
			if err != nil {
				t.Fatal(err)
			}
			_, err = reviewer.Review(context.Background(), FaithfulnessReviewRequest{
				ProfileRef: profile.Ref, PromptRef: prompt.Ref, SchemaRef: schema.Ref,
				Answer: answer, Evidence: faithfulnessEvidenceFor(answer),
			})
			if applicationErrorCode(err) != domain.ErrorCodeStructuredOutputLimitExceeded {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestValidateFaithfulnessReviewCoversEveryConflictDisplayField(t *testing.T) {
	answer, _ := validCitationAnswer(true)
	answer.Payload.ConflictPositions = []domain.ConflictPosition{
		{ClaimID: "52000000-0000-4000-8000-000000000030", Position: "position one", Applicability: json.RawMessage(`{"environment":"prod"}`), CitationIDs: []string{"cite-1"}, UpdatedAt: time.Unix(1, 0).UTC()},
		{ClaimID: "52000000-0000-4000-8000-000000000031", Position: "position two", Applicability: json.RawMessage(`{"environment":"dev"}`), CitationIDs: []string{"cite-2"}, UpdatedAt: time.Unix(2, 0).UTC()},
	}
	answer.Payload.ConflictSummary = "sources disagree under different conditions"
	targetIDs := []string{
		"@conflict/52000000-0000-4000-8000-000000000030/position",
		"@conflict/52000000-0000-4000-8000-000000000031/position",
		"@answer/conflict-summary",
	}
	for _, targetID := range targetIDs {
		t.Run(targetID, func(t *testing.T) {
			review := validFaithfulnessResult(answer, domain.FaithfulnessSupported, []string{"cite-1", "cite-2"})
			review.Payload.Items = removeReviewTarget(review.Payload.Items, targetID)
			if applicationErrorCode(ValidateFaithfulnessReview(answer, review)) != errorCodeFaithfulnessResponseInvalid {
				t.Fatalf("review omitting %s was accepted", targetID)
			}
		})
	}
}

func TestStructuredFaithfulnessReviewerPreservesProviderFailureWithoutFallback(t *testing.T) {
	answer, _ := validCitationAnswer(false)
	catalog, prompt, schema, profile := faithfulnessCatalog(t)
	providerErr := foundation.NewError(foundation.ErrorRetryableFailure, "CHAT_PROVIDER_RATE_LIMITED", true, errors.New("rate limited"))
	model := NewDeterministicChatModel(DeterministicChatStep{Err: providerErr})
	reviewer, err := NewStructuredFaithfulnessReviewer(model, catalog, DefaultRunBudget())
	if err != nil {
		t.Fatal(err)
	}
	_, err = reviewer.Review(context.Background(), FaithfulnessReviewRequest{
		ProfileRef: profile.Ref, PromptRef: prompt.Ref, SchemaRef: schema.Ref, Answer: answer, Evidence: faithfulnessEvidenceFor(answer),
	})
	if !errors.Is(err, providerErr) || model.CallCount() != 1 {
		t.Fatalf("error=%v calls=%d", err, model.CallCount())
	}
}

func faithfulnessCatalog(t *testing.T) (*RuntimeCatalog, PromptDefinition, SchemaDefinition, ModelProfile) {
	t.Helper()
	prompt := PromptDefinition{
		Ref:    domain.PromptRef{ID: "faithfulness-review", Version: "v1"},
		System: "review evidence support", InitialInstruction: "review every assertion",
		RepairInstruction: "unused repair contract", ReducedInstruction: "unused reduced contract",
	}
	schema := SchemaDefinition{
		Ref:        domain.SchemaRef{ID: domain.FaithfulnessReviewSchemaID, Version: domain.OutputSchemaVersionV1},
		JSONSchema: []byte(`{"type":"object"}`),
		Decode: func(raw []byte) (json.RawMessage, error) {
			if _, err := domain.DecodeFaithfulnessReview(raw, domain.DefaultDecodeLimits()); err != nil {
				return nil, err
			}
			return append(json.RawMessage(nil), raw...), nil
		},
	}
	profile := ModelProfile{
		Ref:     domain.ModelProfileRef{ID: "review", Version: "v1"},
		Model:   domain.ModelRef{AdapterName: "fake", AdapterVersion: "v1", ModelID: "review-model", ModelVersion: "v1"},
		Timeout: time.Second, MaxOutputTokens: 1024,
	}
	catalog := NewRuntimeCatalog()
	if err := catalog.RegisterPrompt(prompt); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(schema); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	return catalog, prompt, schema, profile
}

func validFaithfulnessResult(answer domain.RAGAnswerResult, verdict domain.FaithfulnessVerdict, citations []string) domain.FaithfulnessReviewResult {
	targets, err := faithfulnessReviewTargets(answer)
	if err != nil {
		panic(err)
	}
	items := make([]domain.FaithfulnessReviewItem, len(targets))
	passed := true
	for index, target := range targets {
		targetVerdict := domain.FaithfulnessSupported
		if target.Kind == domain.AssertionModelInference {
			targetVerdict = domain.FaithfulnessInferenceDisclosed
		}
		items[index] = domain.FaithfulnessReviewItem{
			AssertionID: target.ID, Verdict: targetVerdict, CitationIDs: append([]string{}, target.CitationIDs...),
			Reason: "structured semantic support decision",
		}
	}
	items[0].Verdict = verdict
	items[0].CitationIDs = append([]string{}, citations...)
	if verdict == domain.FaithfulnessUnsupported {
		passed = false
	}
	return domain.FaithfulnessReviewResult{
		ResultType: domain.ResultTypeFaithfulnessReview, SchemaID: domain.FaithfulnessReviewSchemaID,
		SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: answer.ModelRunRef,
		Payload: domain.FaithfulnessReviewPayload{
			Passed:  passed,
			Items:   items,
			Summary: "review completed",
		},
	}
}

func setReviewTargetVerdict(review *domain.FaithfulnessReviewResult, targetID string, verdict domain.FaithfulnessVerdict) {
	for index := range review.Payload.Items {
		if review.Payload.Items[index].AssertionID == targetID {
			review.Payload.Items[index].Verdict = verdict
			review.Payload.Passed = verdict != domain.FaithfulnessUnsupported
			return
		}
	}
}

func removeReviewTarget(items []domain.FaithfulnessReviewItem, targetID string) []domain.FaithfulnessReviewItem {
	result := make([]domain.FaithfulnessReviewItem, 0, len(items)-1)
	for _, item := range items {
		if item.AssertionID != targetID {
			result = append(result, item)
		}
	}
	return result
}

func faithfulnessEvidenceFor(answer domain.RAGAnswerResult) []domain.Evidence {
	result := make([]domain.Evidence, len(answer.Payload.Citations))
	for index, citation := range answer.Payload.Citations {
		result[index] = domain.Evidence{Citation: citation, Excerpt: "immutable approved evidence", Eligibility: "ELIGIBLE"}
	}
	return result
}
