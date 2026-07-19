package application

import (
	"bytes"
	"context"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	errorCodeCitationValidatorMissing = "AGENT_CITATION_VALIDATOR_MISSING"
	errorCodeCitationGateInvalid      = "AGENT_CITATION_GATE_INVALID"
)

// CitationValidationRequest 绑定一个 Answer 与产生它的当前 Retrieval Evidence set。
type CitationValidationRequest struct {
	WorkspaceID foundation.ID
	Answer      domain.RAGAnswerResult
	Retrieval   RetrievalBatch
}

// CitationValidationResult 返回全部可打开且有正式资格的 Evidence，或确定性 Refusal。
type CitationValidationResult struct {
	Evidence []domain.Evidence
	Refusal  *domain.RefusalPayload
}

// CitationValidator 依次执行 Identity、Openability、Eligibility 与 assertion 引用闭包校验。
type CitationValidator struct {
	retrieval   RetrievalPort
	eligibility EvidenceEligibilityPort
}

// NewCitationValidator 创建 fail-closed 的 Citation Validator。
func NewCitationValidator(retrieval RetrievalPort, eligibility EvidenceEligibilityPort) (*CitationValidator, error) {
	if isNilPort(retrieval) {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, errorCodeRetrievalPortMissing, false, errors.New("retrieval port is required"))
	}
	if isNilPort(eligibility) {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, errorCodeEligibilityPortMissing, false, errors.New("evidence eligibility port is required"))
	}
	return &CitationValidator{retrieval: retrieval, eligibility: eligibility}, nil
}

// Validate 对 Answer 使用的引用执行确定性三层门禁并检查 disputed disclosure。
func (validator *CitationValidator) Validate(ctx context.Context, request CitationValidationRequest) (CitationValidationResult, error) {
	if validator == nil || isNilPort(validator.retrieval) || isNilPort(validator.eligibility) {
		return CitationValidationResult{}, applicationError(foundation.ErrorDependencyUnavailable, errorCodeCitationValidatorMissing, false, errors.New("citation validator is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !canonicalApplicationID(request.WorkspaceID) {
		return CitationValidationResult{}, applicationError(foundation.ErrorInvalidInput, errorCodeCitationGateInvalid, false, errors.New("citation validation request is invalid"))
	}
	for _, citation := range request.Answer.Payload.Citations {
		if citation.WorkspaceID != request.WorkspaceID {
			return refusedCitation(domain.RefusalCitationUnresolvable, "citation crosses the requested workspace"), nil
		}
	}
	if err := request.Answer.Validate(); err != nil || validateRetrievalBatchScope(request.WorkspaceID, request.Retrieval) != nil {
		return CitationValidationResult{}, applicationError(foundation.ErrorInvalidInput, errorCodeCitationGateInvalid, false, errors.New("citation validation request is invalid"))
	}

	candidates := make(map[string]RetrievedEvidence, len(request.Retrieval.Items))
	for _, item := range request.Retrieval.Items {
		candidates[item.Citation.ID] = item
	}
	for _, citation := range request.Answer.Payload.Citations {
		candidate, exists := candidates[citation.ID]
		if !exists || candidate.Citation != citation || citation.WorkspaceID != request.WorkspaceID ||
			citation.IndexVersionID != request.Retrieval.IndexVersionID {
			return refusedCitation(domain.RefusalCitationUnresolvable, "citation identity is not present in the current evidence set"), nil
		}
	}
	if refusal := validateAssertionCitationClosure(request.Answer.Payload); refusal != nil {
		return CitationValidationResult{Refusal: refusal}, nil
	}
	if len(request.Answer.Payload.Citations) == 0 {
		return CitationValidationResult{}, nil
	}

	opened := make(map[string]OpenedEvidence, len(request.Answer.Payload.Citations))
	provenanceByKey := make(map[string]knowledgedomain.ProvenanceRef, len(request.Answer.Payload.Citations))
	openedBatch, err := validator.retrieval.OpenBatch(ctx, request.Answer.Payload.Citations)
	if err != nil {
		if deterministicEvidenceFailure(err) {
			return refusedCitation(domain.RefusalCitationUnresolvable, "citation source span cannot be opened or verified"), nil
		}
		return CitationValidationResult{}, err
	}
	if len(openedBatch) != len(request.Answer.Payload.Citations) {
		return CitationValidationResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeCitationGateInvalid, false, errors.New("retrieval returned an incomplete opened citation batch"))
	}
	for index, citation := range request.Answer.Payload.Citations {
		value := openedBatch[index]
		if value.Citation != citation || value.Excerpt == "" {
			return refusedCitation(domain.RefusalCitationUnresolvable, "opened citation does not match the current evidence identity"), nil
		}
		opened[citation.ID] = value
		ref := knowledgedomain.ProvenanceRef{
			WorkspaceID: request.WorkspaceID, SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
		}
		provenanceByKey[provenanceKey(ref)] = ref
	}

	provenance := make([]knowledgedomain.ProvenanceRef, 0, len(provenanceByKey))
	for _, ref := range provenanceByKey {
		provenance = append(provenance, ref)
	}
	sort.Slice(provenance, func(left, right int) bool { return provenanceKey(provenance[left]) < provenanceKey(provenance[right]) })
	eligibility, err := validator.eligibility.CheckEvidenceEligibility(ctx, knowledgedomain.EvidenceEligibilityQuery{
		WorkspaceID: request.WorkspaceID,
		Provenance:  provenance,
	})
	if err != nil {
		return CitationValidationResult{}, err
	}
	eligibilityByKey, err := exactEligibility(provenance, eligibility)
	if err != nil {
		return CitationValidationResult{}, err
	}

	result := CitationValidationResult{Evidence: make([]domain.Evidence, 0, len(request.Answer.Payload.Citations))}
	disputed := make(map[string][]knowledgedomain.EvidenceEligibilityBinding)
	for _, citation := range request.Answer.Payload.Citations {
		value := eligibilityByKey[provenanceKey(knowledgedomain.ProvenanceRef{
			WorkspaceID: request.WorkspaceID, SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
		})]
		if value.Eligibility == knowledgedomain.EvidenceIneligible {
			return refusedCitation(domain.RefusalUnapprovedEvidenceOnly, "answer cites evidence without a formal approved knowledge binding"), nil
		}
		evidence, conversionErr := domain.EvidenceFromKnowledgeEligibility(citation, opened[citation.ID].Excerpt, value)
		if conversionErr != nil {
			return CitationValidationResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeCitationGateInvalid, false, conversionErr)
		}
		if evidence.Eligibility == knowledgedomain.EvidenceEligibleWithConflict {
			for _, binding := range value.Bindings {
				if binding.OwnerType == knowledgedomain.EvidenceOwnerClaim && binding.ClaimStatus == knowledgedomain.ClaimStatusDisputed {
					disputed[citation.ID] = append(disputed[citation.ID], binding)
				}
			}
		}
		result.Evidence = append(result.Evidence, evidence)
	}
	if !disputedBindingsDisclosed(disputed, request.Answer.Payload.ConflictPositions) {
		return refusedCitation(domain.RefusalConflictNotConditionable, "disputed evidence is not fully disclosed as a conditional conflict"), nil
	}
	return result, nil
}

func validateAssertionCitationClosure(payload domain.RAGAnswerPayload) *domain.RefusalPayload {
	used := make(map[string]struct{}, len(payload.Citations))
	for _, assertion := range payload.Assertions {
		for _, citationID := range assertion.CitationIDs {
			used[citationID] = struct{}{}
		}
	}
	for _, position := range payload.ConflictPositions {
		for _, citationID := range position.CitationIDs {
			used[citationID] = struct{}{}
		}
	}
	for _, citation := range payload.Citations {
		if _, exists := used[citation.ID]; !exists {
			refusal := newRefusalPayload(domain.RefusalEvidenceInsufficient, "answer contains a citation that supports no assertion or conflict position")
			return &refusal
		}
	}
	return nil
}

func exactEligibility(expected []knowledgedomain.ProvenanceRef, actual []knowledgedomain.ProvenanceEligibility) (map[string]knowledgedomain.ProvenanceEligibility, error) {
	if len(actual) != len(expected) {
		return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeCitationGateInvalid, false, errors.New("eligibility result is incomplete"))
	}
	want := make(map[string]knowledgedomain.ProvenanceRef, len(expected))
	for _, ref := range expected {
		want[provenanceKey(ref)] = ref
	}
	result := make(map[string]knowledgedomain.ProvenanceEligibility, len(actual))
	for _, item := range actual {
		key := provenanceKey(item.Provenance)
		expectedRef, exists := want[key]
		if !exists || item.Provenance != expectedRef || knowledgedomain.ValidateProvenanceEligibility(item) != nil {
			return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeCitationGateInvalid, false, errors.New("eligibility result contains an invalid provenance"))
		}
		if _, duplicate := result[key]; duplicate {
			return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeCitationGateInvalid, false, errors.New("eligibility result contains duplicate provenance"))
		}
		result[key] = item
	}
	return result, nil
}

func disputedBindingsDisclosed(disputed map[string][]knowledgedomain.EvidenceEligibilityBinding, positions []domain.ConflictPosition) bool {
	if len(disputed) == 0 {
		return len(positions) == 0
	}
	positionsByClaim := make(map[foundation.ID]domain.ConflictPosition, len(positions))
	for _, position := range positions {
		positionsByClaim[position.ClaimID] = position
	}
	backedClaims := make(map[foundation.ID]struct{}, len(positions))
	conflictMembers := make(map[foundation.ID]map[foundation.ID]struct{})
	for citationID, bindings := range disputed {
		for _, binding := range bindings {
			position, exists := positionsByClaim[binding.OwnerID]
			if !exists || !containsReference(position.CitationIDs, citationID) {
				return false
			}
			applicability, err := knowledgedomain.ParseApplicability(position.Applicability)
			if err != nil || applicability.SchemaVersion != binding.DisputedApplicability.SchemaVersion ||
				applicability.Hash != binding.DisputedApplicability.Hash ||
				!bytes.Equal(applicability.CanonicalJSON, binding.DisputedApplicability.CanonicalJSON) ||
				!position.UpdatedAt.Equal(binding.DisputedClaimUpdatedAtUTC) {
				return false
			}
			backedClaims[binding.OwnerID] = struct{}{}
			for _, conflictID := range binding.ConflictIDs {
				members := conflictMembers[conflictID]
				if members == nil {
					members = make(map[foundation.ID]struct{})
					conflictMembers[conflictID] = members
				}
				members[binding.OwnerID] = struct{}{}
			}
		}
	}
	if len(backedClaims) != len(positions) {
		return false
	}
	for _, members := range conflictMembers {
		if len(members) < 2 {
			return false
		}
	}
	return len(conflictMembers) > 0
}

func containsReference(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func provenanceKey(ref knowledgedomain.ProvenanceRef) string {
	return string(ref.SourceVersionID) + "\x00" + string(ref.SourceSpanID)
}

func deterministicEvidenceFailure(err error) bool {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return false
	}
	switch classified.Kind {
	case foundation.ErrorInvalidInput, foundation.ErrorNotFound, foundation.ErrorVersionConflict,
		foundation.ErrorPermissionDenied, foundation.ErrorConsistencyViolation:
		return true
	default:
		return false
	}
}

func refusedCitation(code domain.RefusalReasonCode, summary string) CitationValidationResult {
	refusal := newRefusalPayload(code, summary)
	return CitationValidationResult{Refusal: &refusal}
}
