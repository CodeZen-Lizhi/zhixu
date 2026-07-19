package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	errorCodeFaithfulnessRunnerMissing   = "AGENT_FAITHFULNESS_RUNNER_MISSING"
	errorCodeFaithfulnessRequestInvalid  = "AGENT_FAITHFULNESS_REQUEST_INVALID"
	errorCodeFaithfulnessResponseInvalid = "AGENT_FAITHFULNESS_RESPONSE_INVALID"
	errorCodeFaithfulnessRejected        = "AGENT_FAITHFULNESS_REJECTED"
)

// FaithfulnessReviewRequest 使用独立 Prompt、Schema 和 REVIEW Model Call 审查一个 Answer。
type FaithfulnessReviewRequest struct {
	ProfileRef domain.ModelProfileRef
	PromptRef  domain.PromptRef
	SchemaRef  domain.SchemaRef
	Answer     domain.RAGAnswerResult
	Evidence   []domain.Evidence
}

// FaithfulnessReviewRunResult 返回独立 REVIEW 调用的结构化结果和可持久化统计。
type FaithfulnessReviewRunResult struct {
	Review        domain.FaithfulnessReviewResult
	Usage         domain.TokenUsage
	RequestBytes  int64
	ResponseBytes int64
	Runtime       FrozenRuntimeRefs
}

type faithfulnessReviewTarget struct {
	ID          string               `json:"id"`
	Text        string               `json:"text"`
	Kind        domain.AssertionKind `json:"kind"`
	CitationIDs []string             `json:"citation_ids"`
}

// FaithfulnessReviewer 是 Answer 发布门禁依赖的独立语义支持审查 seam。
type FaithfulnessReviewer interface {
	// Review 只执行一次 REVIEW Model Call；不在内部切换 Provider 或伪造通过。
	Review(context.Context, FaithfulnessReviewRequest) (FaithfulnessReviewRunResult, error)
}

// StructuredFaithfulnessReviewer 使用版本化 Catalog、严格 Decoder 与 ChatModel 执行 REVIEW 调用。
type StructuredFaithfulnessReviewer struct {
	model   ChatModel
	catalog *RuntimeCatalog
	budget  RunBudget
}

// NewStructuredFaithfulnessReviewer 创建独立 Faithfulness REVIEW Runner。
func NewStructuredFaithfulnessReviewer(model ChatModel, catalog *RuntimeCatalog, budget RunBudget) (*StructuredFaithfulnessReviewer, error) {
	if isNilChatModel(model) || catalog == nil {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, errorCodeFaithfulnessRunnerMissing, false, errors.New("faithfulness model and catalog are required"))
	}
	if err := validateRunBudget(budget); err != nil {
		return nil, err
	}
	return &StructuredFaithfulnessReviewer{model: model, catalog: catalog, budget: budget}, nil
}

// Review 执行一个 phase=REVIEW 的严格结构化模型调用。
func (reviewer *StructuredFaithfulnessReviewer) Review(ctx context.Context, request FaithfulnessReviewRequest) (FaithfulnessReviewRunResult, error) {
	if reviewer == nil || isNilChatModel(reviewer.model) || reviewer.catalog == nil {
		return FaithfulnessReviewRunResult{}, applicationError(foundation.ErrorDependencyUnavailable, errorCodeFaithfulnessRunnerMissing, false, errors.New("faithfulness reviewer is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateFaithfulnessRequest(request); err != nil {
		return FaithfulnessReviewRunResult{}, err
	}
	snapshot, err := reviewer.catalog.Snapshot(request.PromptRef, request.SchemaRef, request.SchemaRef, request.ProfileRef)
	if err != nil {
		return FaithfulnessReviewRunResult{}, err
	}
	input, err := encodeFaithfulnessInput(request)
	if err != nil {
		return FaithfulnessReviewRunResult{}, err
	}
	chatRequest := buildChatRequest(snapshot, snapshot.Schema, domain.ModelCallReview, snapshot.Prompt.InitialInstruction, input, "")
	requestBytes, err := encodedChatRequestBytes(chatRequest)
	if err != nil {
		return FaithfulnessReviewRunResult{}, err
	}
	if requestBytes > reviewer.budget.MaxRequestBytes {
		return FaithfulnessReviewRunResult{}, applicationError(foundation.ErrorNonRetryableFailure, errorCodeRequestBudget, false, errors.New("faithfulness request byte budget is exhausted"))
	}

	timeout := minPositiveDuration(reviewer.budget.Timeout, snapshot.Profile.Timeout)
	reviewCtx, cancel := context.WithTimeout(ctx, timeout)
	response, callErr := reviewer.model.Chat(reviewCtx, cloneChatRequest(chatRequest))
	contextErr := reviewCtx.Err()
	cancel()
	if callErr != nil {
		if contextErr != nil {
			return FaithfulnessReviewRunResult{}, operationContextError(contextErr)
		}
		return FaithfulnessReviewRunResult{}, callErr
	}
	if err := ValidateChatResponse(chatRequest, response); err != nil {
		return FaithfulnessReviewRunResult{}, err
	}
	if int64(len(response.Content)) > reviewer.budget.MaxResponseBytes {
		return FaithfulnessReviewRunResult{}, applicationError(foundation.ErrorNonRetryableFailure, errorCodeResponseBudget, false, errors.New("faithfulness response byte budget is exhausted"))
	}
	usage, err := addUsage(domain.TokenUsage{}, response.Usage, reviewer.budget.MaxTotalTokens)
	if err != nil {
		return FaithfulnessReviewRunResult{}, err
	}
	decoded, err := snapshot.Schema.Decode(append([]byte(nil), response.Content...))
	if err != nil {
		return FaithfulnessReviewRunResult{}, err
	}
	if !bytes.Equal(decoded, response.Content) {
		return FaithfulnessReviewRunResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeDecoderContract, false, errors.New("faithfulness decoder transformed the accepted document"))
	}
	review, err := domain.DecodeFaithfulnessReview(decoded, domain.DefaultDecodeLimits())
	if err != nil || review.ModelRunRef != request.Answer.ModelRunRef {
		return FaithfulnessReviewRunResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeFaithfulnessResponseInvalid, false, errors.New("faithfulness response does not match the reviewed answer"))
	}
	return FaithfulnessReviewRunResult{
		Review: review, Usage: usage, RequestBytes: requestBytes, ResponseBytes: int64(len(response.Content)),
		Runtime: FrozenRuntimeRefs{Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref, Schema: snapshot.Schema.Ref, Model: snapshot.Profile.Model},
	}, nil
}

// ValidateFaithfulnessReview 要求 Review 精确覆盖全部 assertion，并审查每一个声明的引用。
func ValidateFaithfulnessReview(answer domain.RAGAnswerResult, review domain.FaithfulnessReviewResult) error {
	if err := answer.Validate(); err != nil {
		return applicationError(foundation.ErrorInvalidInput, errorCodeFaithfulnessRequestInvalid, false, err)
	}
	if err := review.Validate(); err != nil || review.ModelRunRef != answer.ModelRunRef {
		return applicationError(foundation.ErrorConsistencyViolation, errorCodeFaithfulnessResponseInvalid, false, errors.New("faithfulness review envelope is invalid"))
	}
	targets, err := faithfulnessReviewTargets(answer)
	if err != nil {
		return err
	}
	items := make(map[string]domain.FaithfulnessReviewItem, len(review.Payload.Items))
	for _, item := range review.Payload.Items {
		items[item.AssertionID] = item
	}
	if len(items) != len(targets) {
		return applicationError(foundation.ErrorConsistencyViolation, errorCodeFaithfulnessResponseInvalid, false, errors.New("faithfulness review does not cover every publishable target"))
	}
	for _, target := range targets {
		item, exists := items[target.ID]
		if !exists {
			return applicationError(foundation.ErrorConsistencyViolation, errorCodeFaithfulnessResponseInvalid, false, errors.New("faithfulness review omitted a publishable target"))
		}
		switch target.Kind {
		case domain.AssertionFactual:
			if item.Verdict != domain.FaithfulnessSupported || !sameReferenceSet(target.CitationIDs, item.CitationIDs) {
				return applicationError(foundation.ErrorNonRetryableFailure, errorCodeFaithfulnessRejected, false, errors.New("publishable fact is unsupported or not all citations were reviewed"))
			}
		case domain.AssertionModelInference:
			if item.Verdict != domain.FaithfulnessInferenceDisclosed || len(item.CitationIDs) != 0 {
				return applicationError(foundation.ErrorNonRetryableFailure, errorCodeFaithfulnessRejected, false, errors.New("publishable model inference was not explicitly disclosed"))
			}
		default:
			return applicationError(foundation.ErrorConsistencyViolation, errorCodeFaithfulnessResponseInvalid, false, errors.New("answer assertion kind is invalid"))
		}
	}
	if !review.Payload.Passed {
		return applicationError(foundation.ErrorNonRetryableFailure, errorCodeFaithfulnessRejected, false, errors.New("faithfulness review rejected the answer"))
	}
	return nil
}

func validateFaithfulnessRequest(request FaithfulnessReviewRequest) error {
	if err := request.ProfileRef.Validate(); err != nil {
		return applicationError(foundation.ErrorInvalidInput, errorCodeFaithfulnessRequestInvalid, false, err)
	}
	if err := request.PromptRef.Validate(); err != nil {
		return applicationError(foundation.ErrorInvalidInput, errorCodeFaithfulnessRequestInvalid, false, err)
	}
	if err := request.SchemaRef.Validate(); err != nil || request.SchemaRef.ID != domain.FaithfulnessReviewSchemaID ||
		request.SchemaRef.Version != domain.OutputSchemaVersionV1 {
		return applicationError(foundation.ErrorInvalidInput, errorCodeFaithfulnessRequestInvalid, false, errors.New("faithfulness schema reference is invalid"))
	}
	if err := request.Answer.Validate(); err != nil || len(request.Evidence) != len(request.Answer.Payload.Citations) ||
		len(request.Evidence) > MaxRetrievedEvidence {
		return applicationError(foundation.ErrorInvalidInput, errorCodeFaithfulnessRequestInvalid, false, errors.New("faithfulness answer or evidence is invalid"))
	}
	if _, err := faithfulnessReviewTargets(request.Answer); err != nil {
		return err
	}
	expected := make(map[string]domain.Citation, len(request.Answer.Payload.Citations))
	for _, citation := range request.Answer.Payload.Citations {
		expected[citation.ID] = citation
	}
	seen := make(map[string]struct{}, len(request.Evidence))
	for _, evidence := range request.Evidence {
		citation, exists := expected[evidence.Citation.ID]
		if err := evidence.Validate(); err != nil {
			return applicationError(foundation.ErrorInvalidInput, errorCodeFaithfulnessRequestInvalid, false, err)
		}
		if !exists || citation != evidence.Citation || evidence.Eligibility == knowledgedomain.EvidenceIneligible {
			return applicationError(foundation.ErrorInvalidInput, errorCodeFaithfulnessRequestInvalid, false, errors.New("faithfulness evidence is not the exact eligible answer citation set"))
		}
		if _, duplicate := seen[evidence.Citation.ID]; duplicate {
			return applicationError(foundation.ErrorInvalidInput, errorCodeFaithfulnessRequestInvalid, false, errors.New("faithfulness evidence contains duplicate citations"))
		}
		seen[evidence.Citation.ID] = struct{}{}
	}
	return nil
}

func encodeFaithfulnessInput(request FaithfulnessReviewRequest) ([]byte, error) {
	targets, err := faithfulnessReviewTargets(request.Answer)
	if err != nil {
		return nil, err
	}
	payload := struct {
		SchemaVersion string                     `json:"schema_version"`
		Answer        domain.RAGAnswerPayload    `json:"answer"`
		ReviewTargets []faithfulnessReviewTarget `json:"review_targets"`
		Evidence      []domain.Evidence          `json:"evidence"`
	}{SchemaVersion: "agent-faithfulness-input/v1", Answer: request.Answer.Payload, ReviewTargets: targets, Evidence: request.Evidence}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, applicationError(foundation.ErrorNonRetryableFailure, errorCodeFaithfulnessRequestInvalid, false, err)
	}
	if len(encoded) == 0 || len(encoded) > MaxStructuredInputBytes {
		return nil, applicationError(foundation.ErrorNonRetryableFailure, errorCodeRequestBudget, false, errors.New("faithfulness input exceeds the structured input limit"))
	}
	return encoded, nil
}

func faithfulnessReviewTargets(answer domain.RAGAnswerResult) ([]faithfulnessReviewTarget, error) {
	if err := answer.Validate(); err != nil {
		return nil, applicationError(foundation.ErrorInvalidInput, errorCodeFaithfulnessRequestInvalid, false, err)
	}
	targets := make([]faithfulnessReviewTarget, 0, len(answer.Payload.Assertions)+len(answer.Payload.ConflictPositions)+2)
	seen := make(map[string]struct{}, cap(targets))
	add := func(target faithfulnessReviewTarget) error {
		if _, duplicate := seen[target.ID]; duplicate {
			return applicationError(foundation.ErrorInvalidInput, errorCodeFaithfulnessRequestInvalid, false, errors.New("faithfulness review target identity collides"))
		}
		seen[target.ID] = struct{}{}
		target.CitationIDs = append([]string(nil), target.CitationIDs...)
		targets = append(targets, target)
		return nil
	}
	for _, assertion := range answer.Payload.Assertions {
		if err := add(faithfulnessReviewTarget{ID: assertion.ID, Text: assertion.Text, Kind: assertion.Kind, CitationIDs: assertion.CitationIDs}); err != nil {
			return nil, err
		}
	}
	factualCitations := factualAssertionCitations(answer.Payload.Assertions)
	conclusionKind := domain.AssertionFactual
	if len(factualCitations) == 0 {
		conclusionKind = domain.AssertionModelInference
	}
	if err := add(faithfulnessReviewTarget{ID: "@answer/conclusion", Text: answer.Payload.Conclusion, Kind: conclusionKind, CitationIDs: factualCitations}); err != nil {
		return nil, err
	}
	for _, position := range answer.Payload.ConflictPositions {
		prefix := "@conflict/" + string(position.ClaimID)
		if err := add(faithfulnessReviewTarget{
			ID: prefix + "/position", Text: position.Position, Kind: domain.AssertionFactual, CitationIDs: position.CitationIDs,
		}); err != nil {
			return nil, err
		}
	}
	if len(answer.Payload.ConflictPositions) > 0 {
		if err := add(faithfulnessReviewTarget{
			ID: "@answer/conflict-summary", Text: answer.Payload.ConflictSummary, Kind: domain.AssertionFactual,
			CitationIDs: conflictPositionCitations(answer.Payload.ConflictPositions),
		}); err != nil {
			return nil, err
		}
	}
	if len(targets) > MaxRetrievedEvidence {
		return nil, applicationError(foundation.ErrorNonRetryableFailure, errorCodeRequestBudget, false, errors.New("faithfulness review target count exceeds the fixed limit"))
	}
	return targets, nil
}

func factualAssertionCitations(assertions []domain.Assertion) []string {
	values := make([]string, 0)
	seen := make(map[string]struct{})
	for _, assertion := range assertions {
		if assertion.Kind != domain.AssertionFactual {
			continue
		}
		for _, citationID := range assertion.CitationIDs {
			if _, exists := seen[citationID]; !exists {
				seen[citationID] = struct{}{}
				values = append(values, citationID)
			}
		}
	}
	return values
}

func conflictPositionCitations(positions []domain.ConflictPosition) []string {
	values := make([]string, 0)
	seen := make(map[string]struct{})
	for _, position := range positions {
		for _, citationID := range position.CitationIDs {
			if _, exists := seen[citationID]; !exists {
				seen[citationID] = struct{}{}
				values = append(values, citationID)
			}
		}
	}
	return values
}

func sameReferenceSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		if _, exists := values[value]; !exists {
			return false
		}
	}
	return true
}

var _ FaithfulnessReviewer = (*StructuredFaithfulnessReviewer)(nil)
