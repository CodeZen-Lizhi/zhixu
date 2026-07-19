package application

import (
	"context"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const errorCodeAnswerPublisherMissing = "AGENT_ANSWER_PUBLISHER_MISSING"

// AnswerPublicationRequest 绑定 Answer、当前 Evidence set 与独立 Faithfulness 运行版本。
type AnswerPublicationRequest struct {
	WorkspaceID      foundation.ID
	Answer           domain.RAGAnswerResult
	Retrieval        RetrievalBatch
	ReviewProfileRef domain.ModelProfileRef
	ReviewPromptRef  domain.PromptRef
	ReviewSchemaRef  domain.SchemaRef
}

// AnswerPublicationResult 是 Answer 或 Refusal 二选一的发布门禁结果。
type AnswerPublicationResult struct {
	Answer  *domain.RAGAnswerResult
	Refusal *domain.RefusalResult
	Review  *domain.FaithfulnessReviewResult
}

// Publishable 仅在 Citation 与 Faithfulness 全部通过时返回 true。
func (result AnswerPublicationResult) Publishable() bool {
	return result.Answer != nil && result.Refusal == nil && result.Review != nil
}

// AnswerPublisher 组合 Citation 三层确定性门禁与独立 Faithfulness Review。
type AnswerPublisher struct {
	citations *CitationValidator
	reviewer  FaithfulnessReviewer
}

// NewAnswerPublisher 创建 fail-closed 的 Answer 发布门禁。
func NewAnswerPublisher(citations *CitationValidator, reviewer FaithfulnessReviewer) (*AnswerPublisher, error) {
	if citations == nil || isNilPort(reviewer) {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, errorCodeAnswerPublisherMissing, false, errors.New("citation validator and faithfulness reviewer are required"))
	}
	return &AnswerPublisher{citations: citations, reviewer: reviewer}, nil
}

// Publish 只有 Citation、Eligibility、closure 与 Faithfulness 全部通过才返回 Answer。
func (publisher *AnswerPublisher) Publish(ctx context.Context, request AnswerPublicationRequest) (AnswerPublicationResult, error) {
	if publisher == nil || publisher.citations == nil || isNilPort(publisher.reviewer) {
		return AnswerPublicationResult{}, applicationError(foundation.ErrorDependencyUnavailable, errorCodeAnswerPublisherMissing, false, errors.New("answer publisher is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	citations, err := publisher.citations.Validate(ctx, CitationValidationRequest{
		WorkspaceID: request.WorkspaceID,
		Answer:      request.Answer,
		Retrieval:   request.Retrieval,
	})
	if err != nil {
		return AnswerPublicationResult{}, err
	}
	if citations.Refusal != nil {
		return refusalPublication(request.Answer.ModelRunRef, *citations.Refusal), nil
	}

	reviewRun, err := publisher.reviewer.Review(ctx, FaithfulnessReviewRequest{
		ProfileRef: request.ReviewProfileRef,
		PromptRef:  request.ReviewPromptRef,
		SchemaRef:  request.ReviewSchemaRef,
		Answer:     request.Answer,
		Evidence:   citations.Evidence,
	})
	if err != nil {
		if !reviewValidationFailure(err) {
			return AnswerPublicationResult{}, err
		}
		refusal := newRefusalPayload(domain.RefusalValidationExhausted, "faithfulness review could not produce a valid structured decision")
		return refusalPublication(request.Answer.ModelRunRef, refusal), nil
	}
	if err := ValidateFaithfulnessReview(request.Answer, reviewRun.Review); err != nil {
		code := domain.RefusalValidationExhausted
		if applicationErrorCode(err) == errorCodeFaithfulnessRejected {
			code = domain.RefusalEvidenceInsufficient
		}
		refusal := newRefusalPayload(code, "answer assertions are not fully supported by every cited evidence span")
		result := refusalPublication(request.Answer.ModelRunRef, refusal)
		result.Review = &reviewRun.Review
		return result, nil
	}
	answer := request.Answer
	review := reviewRun.Review
	return AnswerPublicationResult{Answer: &answer, Review: &review}, nil
}

func refusalPublication(modelRunRef foundation.ID, payload domain.RefusalPayload) AnswerPublicationResult {
	refusal := domain.RefusalResult{
		ResultType: domain.ResultTypeRefusal, SchemaID: domain.RefusalSchemaID,
		SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: modelRunRef, Payload: payload,
	}
	return AnswerPublicationResult{Refusal: &refusal}
}

func newRefusalPayload(code domain.RefusalReasonCode, summary string) domain.RefusalPayload {
	return domain.RefusalPayload{
		ReasonCode:          code,
		Summary:             summary,
		RetrievalScope:      "current workspace approved knowledge",
		MissingRequirements: []string{"eligible evidence with complete citation and semantic support"},
		SuggestedActions:    []string{"add or approve supporting knowledge and retry"},
	}
}

func applicationErrorCode(err error) string {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return ""
	}
	return classified.Code
}

func reviewValidationFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	switch applicationErrorCode(err) {
	case domain.ErrorCodeStructuredOutputInvalid, domain.ErrorCodeStructuredOutputLimitExceeded, domain.ErrorCodeSchemaInvalid,
		domain.ErrorCodeFaithfulnessReviewInvalid, domain.ErrorCodeValidationExhausted,
		errorCodeDecoderContract, errorCodeFaithfulnessResponseInvalid,
		errorCodeRequestBudget, errorCodeResponseBudget, errorCodeTokenBudget:
		return true
	default:
		return false
	}
}

func isNilPort(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
