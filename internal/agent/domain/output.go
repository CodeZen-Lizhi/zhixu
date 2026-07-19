package domain

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	// RelationAssessmentSchemaID 是关系五分类输出的稳定 Schema ID。
	RelationAssessmentSchemaID = "agent.relation-assessment"
	// RAGAnswerSchemaID 是可发布 RAG Answer 的稳定 Schema ID。
	RAGAnswerSchemaID = "agent.rag-answer"
	// RefusalSchemaID 是拒答输出的稳定 Schema ID。
	RefusalSchemaID = "agent.refusal"
	// FaithfulnessReviewSchemaID 是逐 assertion 审查输出的稳定 Schema ID。
	FaithfulnessReviewSchemaID = "agent.faithfulness-review"
	// OutputSchemaVersionV1 是 M6-02 四类结构化输出的首个版本。
	OutputSchemaVersionV1 = "v1"

	ResultTypeRelationAssessment = "relation_assessment"
	ResultTypeRAGAnswer          = "rag_answer"
	ResultTypeRefusal            = "refusal"
	ResultTypeFaithfulnessReview = "faithfulness_review"

	maxOutputTextBytes = 16 * 1024
	maxSummaryBytes    = 4 * 1024
	maxReasonBytes     = 2 * 1024
	maxExcerptBytes    = 32 * 1024
	maxPayloadItems    = 500
)

// Citation 唯一选择一个 Workspace 内的 Chunk、Source Version 与 Source Span。
type Citation struct {
	ID              string        `json:"id"`
	WorkspaceID     foundation.ID `json:"workspace_id"`
	IndexVersionID  foundation.ID `json:"index_version_id"`
	ChunkID         foundation.ID `json:"chunk_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
}

// Validate 校验 Citation 身份完整、canonical 且不存在身份复用。
func (citation Citation) Validate() error {
	if !canonicalReference(citation.ID, maxReferenceIDBytes) {
		return invalid(ErrorCodeCitationInvalid, "citation id is invalid")
	}
	ids := []foundation.ID{citation.WorkspaceID, citation.IndexVersionID, citation.ChunkID, citation.SourceVersionID, citation.SourceSpanID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalID(id) {
			return invalid(ErrorCodeCitationInvalid, "citation identity is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return invalid(ErrorCodeCitationInvalid, "citation identity reuses an id")
		}
		seen[id] = struct{}{}
	}
	return nil
}

// Evidence 是 Agent 可消费的有界证据及其正式知识资格投影。
type Evidence struct {
	Citation    Citation                            `json:"citation"`
	Excerpt     string                              `json:"excerpt"`
	Eligibility knowledgedomain.EvidenceEligibility `json:"eligibility"`
	ConflictIDs []foundation.ID                     `json:"conflict_ids"`
}

// Validate 校验 Evidence 引用、摘录和 disputed Conflict 绑定。
func (evidence Evidence) Validate() error {
	if err := evidence.Citation.Validate(); err != nil || !boundedText(evidence.Excerpt, maxExcerptBytes, true) {
		return invalid(ErrorCodeEvidenceInvalid, "evidence citation or excerpt is invalid")
	}
	switch evidence.Eligibility {
	case knowledgedomain.EvidenceEligible, knowledgedomain.EvidenceIneligible:
		if len(evidence.ConflictIDs) != 0 {
			return invalid(ErrorCodeEvidenceInvalid, "non-disputed evidence cannot bind conflicts")
		}
	case knowledgedomain.EvidenceEligibleWithConflict:
		if !canonicalOrderedIDs(evidence.ConflictIDs, true) {
			return invalid(ErrorCodeEvidenceInvalid, "disputed evidence requires canonical conflict ids")
		}
	default:
		return invalid(ErrorCodeEvidenceInvalid, "evidence eligibility is unsupported")
	}
	return nil
}

// EvidenceFromKnowledgeEligibility 将 Knowledge 唯一事实源显式投影为 Agent Evidence。
func EvidenceFromKnowledgeEligibility(citation Citation, excerpt string, eligibility knowledgedomain.ProvenanceEligibility) (Evidence, error) {
	if err := knowledgedomain.ValidateProvenanceEligibility(eligibility); err != nil ||
		eligibility.Provenance.WorkspaceID != citation.WorkspaceID ||
		eligibility.Provenance.SourceVersionID != citation.SourceVersionID ||
		eligibility.Provenance.SourceSpanID != citation.SourceSpanID {
		return Evidence{}, inconsistent(ErrorCodeEvidenceInvalid, "knowledge eligibility does not match citation provenance")
	}
	conflicts := make(map[foundation.ID]struct{})
	for _, binding := range eligibility.Bindings {
		for _, conflictID := range binding.ConflictIDs {
			conflicts[conflictID] = struct{}{}
		}
	}
	conflictIDs := make([]foundation.ID, 0, len(conflicts))
	for conflictID := range conflicts {
		conflictIDs = append(conflictIDs, conflictID)
	}
	slices.Sort(conflictIDs)
	evidence := Evidence{Citation: citation, Excerpt: excerpt, Eligibility: eligibility.Eligibility, ConflictIDs: conflictIDs}
	if err := evidence.Validate(); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

// AssertionKind 区分需要 Evidence 支持的事实与显式模型推断。
type AssertionKind string

const (
	AssertionFactual        AssertionKind = "FACTUAL"
	AssertionModelInference AssertionKind = "MODEL_INFERENCE"
)

// Assertion 是 Answer 中可独立进行 Faithfulness 审查的原子结论。
type Assertion struct {
	ID          string        `json:"id"`
	Text        string        `json:"text"`
	Kind        AssertionKind `json:"kind"`
	CitationIDs []string      `json:"citation_ids"`
}

// Validate 校验事实 assertion 有引用，模型推断有显式标记且不伪装引用事实。
func (assertion Assertion) Validate() error {
	if !canonicalReference(assertion.ID, maxReferenceIDBytes) || !boundedText(assertion.Text, maxSummaryBytes, true) ||
		assertion.CitationIDs == nil || !canonicalUniqueReferences(assertion.CitationIDs, false) {
		return invalid(ErrorCodeAssertionInvalid, "assertion identity, text, or citation ids are invalid")
	}
	switch assertion.Kind {
	case AssertionFactual:
		if len(assertion.CitationIDs) == 0 {
			return invalid(ErrorCodeAssertionInvalid, "factual assertion requires at least one citation")
		}
	case AssertionModelInference:
		if len(assertion.CitationIDs) != 0 {
			return invalid(ErrorCodeAssertionInvalid, "model inference cannot be presented as cited fact")
		}
	default:
		return invalid(ErrorCodeAssertionInvalid, "assertion kind is unsupported")
	}
	return nil
}

// ConflictPosition 披露一个争议观点、适用条件、来源与更新时间。
type ConflictPosition struct {
	ClaimID       foundation.ID   `json:"claim_id"`
	Position      string          `json:"position"`
	Applicability json.RawMessage `json:"applicability"`
	CitationIDs   []string        `json:"citation_ids"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// Validate 校验冲突观点可定位且 Applicability 复用 Knowledge canonical 语义。
func (position ConflictPosition) Validate() error {
	if !canonicalID(position.ClaimID) || !boundedText(position.Position, maxSummaryBytes, true) ||
		!canonicalUniqueReferences(position.CitationIDs, true) || position.UpdatedAt.IsZero() || position.UpdatedAt.Location() != time.UTC {
		return invalid(ErrorCodeAnswerInvalid, "conflict position is incomplete")
	}
	if _, err := knowledgedomain.ParseApplicability(position.Applicability); err != nil {
		return invalid(ErrorCodeAnswerInvalid, "conflict position applicability is invalid")
	}
	return nil
}

// RAGAnswerPayload 是通过 Citation 与 Faithfulness 门禁后才可发布的回答载荷。
type RAGAnswerPayload struct {
	Conclusion        string             `json:"conclusion"`
	Assertions        []Assertion        `json:"assertions"`
	Citations         []Citation         `json:"citations"`
	ConflictPositions []ConflictPosition `json:"conflict_positions"`
	ConflictSummary   string             `json:"conflict_summary"`
}

// Validate 校验 Answer 的 assertion、Citation 引用闭包和冲突披露。
func (payload RAGAnswerPayload) Validate() error {
	if !boundedText(payload.Conclusion, maxOutputTextBytes, true) || len(payload.Assertions) == 0 ||
		payload.Citations == nil || payload.ConflictPositions == nil ||
		len(payload.Assertions) > maxPayloadItems || len(payload.Citations) > maxPayloadItems ||
		len(payload.ConflictPositions) > maxPayloadItems {
		return invalid(ErrorCodeAnswerInvalid, "answer payload exceeds required bounds")
	}
	citationIDs := make(map[string]struct{}, len(payload.Citations))
	var workspaceID, indexVersionID foundation.ID
	for _, citation := range payload.Citations {
		if err := citation.Validate(); err != nil {
			return invalid(ErrorCodeAnswerInvalid, "answer contains an invalid citation")
		}
		if _, duplicate := citationIDs[citation.ID]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "answer contains duplicate citations")
		}
		if workspaceID == "" {
			workspaceID, indexVersionID = citation.WorkspaceID, citation.IndexVersionID
		} else if citation.WorkspaceID != workspaceID || citation.IndexVersionID != indexVersionID {
			return invalid(ErrorCodeAnswerInvalid, "answer citations cross workspace or index version")
		}
		citationIDs[citation.ID] = struct{}{}
	}
	assertionIDs := make(map[string]struct{}, len(payload.Assertions))
	for _, assertion := range payload.Assertions {
		if err := assertion.Validate(); err != nil {
			return err
		}
		if _, duplicate := assertionIDs[assertion.ID]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "answer contains duplicate assertions")
		}
		assertionIDs[assertion.ID] = struct{}{}
		for _, citationID := range assertion.CitationIDs {
			if _, exists := citationIDs[citationID]; !exists {
				return invalid(ErrorCodeAnswerInvalid, "assertion references an unknown citation")
			}
		}
	}
	claimIDs := make(map[foundation.ID]struct{}, len(payload.ConflictPositions))
	for _, position := range payload.ConflictPositions {
		if err := position.Validate(); err != nil {
			return err
		}
		if _, duplicate := claimIDs[position.ClaimID]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "conflict answer repeats a claim position")
		}
		claimIDs[position.ClaimID] = struct{}{}
		for _, citationID := range position.CitationIDs {
			if _, exists := citationIDs[citationID]; !exists {
				return invalid(ErrorCodeAnswerInvalid, "conflict position references an unknown citation")
			}
		}
	}
	if len(payload.ConflictPositions) == 0 {
		if payload.ConflictSummary != "" {
			return invalid(ErrorCodeAnswerInvalid, "answer cannot disclose a conflict without positions")
		}
	} else if len(payload.ConflictPositions) < 2 || !boundedText(payload.ConflictSummary, maxSummaryBytes, true) {
		return invalid(ErrorCodeAnswerInvalid, "conflict answer requires both positions and a summary")
	}
	return nil
}

// RefusalReasonCode 是可程序判断的稳定拒答原因。
type RefusalReasonCode string

const (
	RefusalNoRelevantEvidence       RefusalReasonCode = "NO_RELEVANT_EVIDENCE"
	RefusalUnapprovedEvidenceOnly   RefusalReasonCode = "UNAPPROVED_EVIDENCE_ONLY"
	RefusalCitationUnresolvable     RefusalReasonCode = "CITATION_UNRESOLVABLE"
	RefusalEvidenceInsufficient     RefusalReasonCode = "EVIDENCE_INSUFFICIENT"
	RefusalExternalFactUnauthorized RefusalReasonCode = "EXTERNAL_FACT_NOT_AUTHORIZED"
	RefusalConflictNotConditionable RefusalReasonCode = "CONFLICT_NOT_CONDITIONABLE"
	RefusalValidationExhausted      RefusalReasonCode = RefusalReasonCode(ErrorCodeValidationExhausted)
)

// RefusalPayload 说明拒答原因、证据范围、缺口和安全下一步。
type RefusalPayload struct {
	ReasonCode          RefusalReasonCode `json:"reason_code"`
	Summary             string            `json:"summary"`
	RetrievalScope      string            `json:"retrieval_scope"`
	MissingRequirements []string          `json:"missing_requirements"`
	SuggestedActions    []string          `json:"suggested_actions"`
}

// Validate 校验 Refusal 原因和恢复信息完整且有界。
func (payload RefusalPayload) Validate() error {
	if !validRefusalReason(payload.ReasonCode) || !boundedText(payload.Summary, maxSummaryBytes, true) ||
		!boundedText(payload.RetrievalScope, maxSummaryBytes, true) ||
		!canonicalTextList(payload.MissingRequirements, true, 50, maxReasonBytes) ||
		!canonicalTextList(payload.SuggestedActions, true, 50, maxReasonBytes) {
		return invalid(ErrorCodeRefusalInvalid, "refusal payload is incomplete or invalid")
	}
	return nil
}

// FaithfulnessVerdict 是逐 assertion 的结构化语义支持结论。
type FaithfulnessVerdict string

const (
	FaithfulnessSupported          FaithfulnessVerdict = "SUPPORTED"
	FaithfulnessUnsupported        FaithfulnessVerdict = "UNSUPPORTED"
	FaithfulnessInferenceDisclosed FaithfulnessVerdict = "INFERENCE_DISCLOSED"
)

// FaithfulnessReviewItem 绑定一个 assertion、判定、引用和理由。
type FaithfulnessReviewItem struct {
	AssertionID string              `json:"assertion_id"`
	Verdict     FaithfulnessVerdict `json:"verdict"`
	CitationIDs []string            `json:"citation_ids"`
	Reason      string              `json:"reason"`
}

// Validate 校验 Faithfulness Review Item 的受控判定与引用形状。
func (item FaithfulnessReviewItem) Validate() error {
	if !canonicalReference(item.AssertionID, maxReferenceIDBytes) ||
		item.CitationIDs == nil || !canonicalUniqueReferences(item.CitationIDs, false) || !boundedText(item.Reason, maxReasonBytes, true) {
		return invalid(ErrorCodeFaithfulnessReviewInvalid, "faithfulness review item is invalid")
	}
	switch item.Verdict {
	case FaithfulnessSupported:
		if len(item.CitationIDs) == 0 {
			return invalid(ErrorCodeFaithfulnessReviewInvalid, "supported assertion requires reviewed citations")
		}
	case FaithfulnessUnsupported:
		// Unsupported 可以保留导致失败的引用，也可以为空。
	case FaithfulnessInferenceDisclosed:
		if len(item.CitationIDs) != 0 {
			return invalid(ErrorCodeFaithfulnessReviewInvalid, "disclosed inference cannot be reviewed as cited fact")
		}
	default:
		return invalid(ErrorCodeFaithfulnessReviewInvalid, "faithfulness verdict is unsupported")
	}
	return nil
}

// FaithfulnessReviewPayload 是 Answer 是否可发布的逐 assertion 审查结果。
type FaithfulnessReviewPayload struct {
	Passed  bool                     `json:"passed"`
	Items   []FaithfulnessReviewItem `json:"items"`
	Summary string                   `json:"summary"`
}

// Validate 校验 Review 覆盖集合唯一且 Passed 与逐项判定一致。
func (payload FaithfulnessReviewPayload) Validate() error {
	if len(payload.Items) == 0 || len(payload.Items) > maxPayloadItems || !boundedText(payload.Summary, maxSummaryBytes, true) {
		return invalid(ErrorCodeFaithfulnessReviewInvalid, "faithfulness review payload is incomplete")
	}
	seen := make(map[string]struct{}, len(payload.Items))
	allPassed := true
	for _, item := range payload.Items {
		if err := item.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[item.AssertionID]; duplicate {
			return invalid(ErrorCodeFaithfulnessReviewInvalid, "faithfulness review contains duplicate assertions")
		}
		seen[item.AssertionID] = struct{}{}
		allPassed = allPassed && item.Verdict != FaithfulnessUnsupported
	}
	if payload.Passed != allPassed {
		return invalid(ErrorCodeFaithfulnessReviewInvalid, "faithfulness passed flag contradicts item verdicts")
	}
	return nil
}

// ApplicabilityComparison 比较候选与已有 Claim 的 Knowledge Applicability。
type ApplicabilityComparison struct {
	Candidate json.RawMessage `json:"candidate"`
	Existing  json.RawMessage `json:"existing,omitempty"`
	Summary   string          `json:"summary"`
}

// RelationConflictDisclosure 是 Relation Assessment 对 Disputed Claim 的机器可验证披露。
type RelationConflictDisclosure struct {
	ClaimID       foundation.ID   `json:"claim_id"`
	ConflictIDs   []foundation.ID `json:"conflict_ids"`
	Applicability json.RawMessage `json:"applicability"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// Validate 校验争议 Claim、Conflict、适用条件和更新时间均来自规范事实。
func (disclosure RelationConflictDisclosure) Validate() error {
	if !canonicalID(disclosure.ClaimID) || !canonicalOrderedIDs(disclosure.ConflictIDs, true) ||
		disclosure.UpdatedAt.IsZero() || disclosure.UpdatedAt.Location() != time.UTC {
		return invalid(ErrorCodeRelationAssessmentInvalid, "relation conflict disclosure identity is invalid")
	}
	if _, err := knowledgedomain.ParseApplicability(disclosure.Applicability); err != nil {
		return invalid(ErrorCodeRelationAssessmentInvalid, "relation conflict disclosure applicability is invalid")
	}
	return nil
}

// Validate 校验 Applicability JSON 复用 Knowledge canonical parser。
func (comparison ApplicabilityComparison) Validate(requireExisting bool) error {
	if _, err := knowledgedomain.ParseApplicability(comparison.Candidate); err != nil {
		return invalid(ErrorCodeRelationAssessmentInvalid, "candidate applicability is invalid")
	}
	if requireExisting {
		if _, err := knowledgedomain.ParseApplicability(comparison.Existing); err != nil || !boundedText(comparison.Summary, maxSummaryBytes, true) {
			return invalid(ErrorCodeRelationAssessmentInvalid, "existing applicability comparison is invalid")
		}
	} else if len(bytes.TrimSpace(comparison.Existing)) != 0 || comparison.Summary != "" {
		return invalid(ErrorCodeRelationAssessmentInvalid, "new assessment cannot invent an existing applicability")
	}
	return nil
}

// RelationAssessmentPayload 使用 Knowledge RelationAssessment 五分类而非复制枚举。
type RelationAssessmentPayload struct {
	Assessment            knowledgedomain.RelationAssessment `json:"assessment"`
	CandidateEvidenceRefs []string                           `json:"candidate_evidence_refs"`
	ExistingEvidenceRefs  []string                           `json:"existing_evidence_refs"`
	ConflictDisclosures   []RelationConflictDisclosure       `json:"conflict_disclosures"`
	Applicability         ApplicabilityComparison            `json:"applicability"`
	Reason                string                             `json:"reason"`
	ConfidenceFactors     []string                           `json:"confidence_factors"`
	UncertaintyReasons    []string                           `json:"uncertainty_reasons"`
}

// Validate 校验五分类、双侧 Evidence、Applicability 和解释字段。
func (payload RelationAssessmentPayload) Validate() error {
	if !validAssessment(payload.Assessment) ||
		payload.ExistingEvidenceRefs == nil || payload.ConflictDisclosures == nil || payload.UncertaintyReasons == nil ||
		!canonicalUniqueReferences(payload.CandidateEvidenceRefs, true) ||
		!canonicalTextList(payload.ConfidenceFactors, true, 50, maxReasonBytes) ||
		!canonicalTextList(payload.UncertaintyReasons, false, 50, maxReasonBytes) ||
		!boundedText(payload.Reason, maxReasonBytes, true) {
		return invalid(ErrorCodeRelationAssessmentInvalid, "relation assessment payload is incomplete")
	}
	requiresExisting := payload.Assessment != knowledgedomain.AssessmentNew
	if requiresExisting {
		if !canonicalUniqueReferences(payload.ExistingEvidenceRefs, true) {
			return invalid(ErrorCodeRelationAssessmentInvalid, "non-new assessment requires existing evidence")
		}
	} else if len(payload.ExistingEvidenceRefs) != 0 {
		return invalid(ErrorCodeRelationAssessmentInvalid, "new assessment cannot bind existing evidence")
	}
	if payload.Assessment == knowledgedomain.AssessmentLowConfidence && len(payload.UncertaintyReasons) == 0 {
		return invalid(ErrorCodeRelationAssessmentInvalid, "low confidence assessment requires uncertainty reasons")
	}
	if len(payload.ConflictDisclosures) > maxPayloadItems {
		return invalid(ErrorCodeRelationAssessmentInvalid, "relation conflict disclosure count exceeds limit")
	}
	previousClaimID := foundation.ID("")
	for _, disclosure := range payload.ConflictDisclosures {
		if disclosure.Validate() != nil || (previousClaimID != "" && disclosure.ClaimID <= previousClaimID) {
			return invalid(ErrorCodeRelationAssessmentInvalid, "relation conflict disclosures are not canonical")
		}
		previousClaimID = disclosure.ClaimID
	}
	return payload.Applicability.Validate(requiresExisting)
}

// RelationAssessmentResult 是独立版本化的关系五分类 Envelope。
type RelationAssessmentResult struct {
	ResultType    string                    `json:"result_type"`
	SchemaID      string                    `json:"schema_id"`
	SchemaVersion string                    `json:"schema_version"`
	ModelRunRef   foundation.ID             `json:"model_run_ref"`
	Payload       RelationAssessmentPayload `json:"payload"`
}

// Validate 校验 Relation Assessment Envelope 与载荷。
func (result RelationAssessmentResult) Validate() error {
	if result.ResultType != ResultTypeRelationAssessment || result.SchemaID != RelationAssessmentSchemaID ||
		result.SchemaVersion != OutputSchemaVersionV1 || !canonicalID(result.ModelRunRef) {
		return invalid(ErrorCodeSchemaInvalid, "relation assessment envelope is invalid")
	}
	return result.Payload.Validate()
}

// RAGAnswerResult 是独立版本化的可发布回答 Envelope。
type RAGAnswerResult struct {
	ResultType    string           `json:"result_type"`
	SchemaID      string           `json:"schema_id"`
	SchemaVersion string           `json:"schema_version"`
	ModelRunRef   foundation.ID    `json:"model_run_ref"`
	Payload       RAGAnswerPayload `json:"payload"`
}

// Validate 校验 RAG Answer Envelope 与载荷。
func (result RAGAnswerResult) Validate() error {
	if result.ResultType != ResultTypeRAGAnswer || result.SchemaID != RAGAnswerSchemaID ||
		result.SchemaVersion != OutputSchemaVersionV1 || !canonicalID(result.ModelRunRef) {
		return invalid(ErrorCodeSchemaInvalid, "rag answer envelope is invalid")
	}
	return result.Payload.Validate()
}

// RefusalResult 是独立版本化的拒答 Envelope。
type RefusalResult struct {
	ResultType    string         `json:"result_type"`
	SchemaID      string         `json:"schema_id"`
	SchemaVersion string         `json:"schema_version"`
	ModelRunRef   foundation.ID  `json:"model_run_ref"`
	Payload       RefusalPayload `json:"payload"`
}

// Validate 校验 Refusal Envelope 与载荷。
func (result RefusalResult) Validate() error {
	if result.ResultType != ResultTypeRefusal || result.SchemaID != RefusalSchemaID ||
		result.SchemaVersion != OutputSchemaVersionV1 || !canonicalID(result.ModelRunRef) {
		return invalid(ErrorCodeSchemaInvalid, "refusal envelope is invalid")
	}
	return result.Payload.Validate()
}

// FaithfulnessReviewResult 是独立版本化的语义支持审查 Envelope。
type FaithfulnessReviewResult struct {
	ResultType    string                    `json:"result_type"`
	SchemaID      string                    `json:"schema_id"`
	SchemaVersion string                    `json:"schema_version"`
	ModelRunRef   foundation.ID             `json:"model_run_ref"`
	Payload       FaithfulnessReviewPayload `json:"payload"`
}

// Validate 校验 Faithfulness Review Envelope 与载荷。
func (result FaithfulnessReviewResult) Validate() error {
	if result.ResultType != ResultTypeFaithfulnessReview || result.SchemaID != FaithfulnessReviewSchemaID ||
		result.SchemaVersion != OutputSchemaVersionV1 || !canonicalID(result.ModelRunRef) {
		return invalid(ErrorCodeSchemaInvalid, "faithfulness review envelope is invalid")
	}
	return result.Payload.Validate()
}

// DecodeRelationAssessment 严格解析一个 relation assessment v1 文档。
func DecodeRelationAssessment(raw []byte, limits DecodeLimits) (RelationAssessmentResult, error) {
	return DecodeStrict(raw, limits, RelationAssessmentResult.Validate)
}

// DecodeRAGAnswer 严格解析一个 RAG answer v1 文档。
func DecodeRAGAnswer(raw []byte, limits DecodeLimits) (RAGAnswerResult, error) {
	return DecodeStrict(raw, limits, RAGAnswerResult.Validate)
}

// DecodeRefusal 严格解析一个 refusal v1 文档。
func DecodeRefusal(raw []byte, limits DecodeLimits) (RefusalResult, error) {
	return DecodeStrict(raw, limits, RefusalResult.Validate)
}

// DecodeFaithfulnessReview 严格解析一个 faithfulness review v1 文档。
func DecodeFaithfulnessReview(raw []byte, limits DecodeLimits) (FaithfulnessReviewResult, error) {
	return DecodeStrict(raw, limits, FaithfulnessReviewResult.Validate)
}

func validAssessment(value knowledgedomain.RelationAssessment) bool {
	switch value {
	case knowledgedomain.AssessmentNew, knowledgedomain.AssessmentComplementary, knowledgedomain.AssessmentDuplicate,
		knowledgedomain.AssessmentConflict, knowledgedomain.AssessmentLowConfidence:
		return true
	default:
		return false
	}
}

func validRefusalReason(value RefusalReasonCode) bool {
	switch value {
	case RefusalNoRelevantEvidence, RefusalUnapprovedEvidenceOnly, RefusalCitationUnresolvable,
		RefusalEvidenceInsufficient, RefusalExternalFactUnauthorized, RefusalConflictNotConditionable,
		RefusalValidationExhausted:
		return true
	default:
		return false
	}
}

func canonicalID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func boundedText(value string, maximum int, required bool) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || len(value) > maximum {
		return false
	}
	return !required || value != ""
}

func canonicalUniqueReferences(values []string, required bool) bool {
	if len(values) > maxPayloadItems || (required && len(values) == 0) {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !canonicalReference(value, maxReferenceIDBytes) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func canonicalUniqueIDs(values []foundation.ID, required bool) bool {
	if len(values) > maxPayloadItems || (required && len(values) == 0) {
		return false
	}
	seen := make(map[foundation.ID]struct{}, len(values))
	for _, value := range values {
		if !canonicalID(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func canonicalOrderedIDs(values []foundation.ID, required bool) bool {
	if !canonicalUniqueIDs(values, required) {
		return false
	}
	for index := 1; index < len(values); index++ {
		if values[index] <= values[index-1] {
			return false
		}
	}
	return true
}

func canonicalTextList(values []string, required bool, maximumItems, maximumBytes int) bool {
	if len(values) > maximumItems || (required && len(values) == 0) {
		return false
	}
	for _, value := range values {
		if !boundedText(value, maximumBytes, true) {
			return false
		}
	}
	return true
}
