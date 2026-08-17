package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
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
	// RAGAnswerMetadataSchemaID 是最终流式正文之后生成的内部元数据 Schema ID。
	RAGAnswerMetadataSchemaID = "agent.rag-answer-metadata"
	// RAGAnswerMetadataRefusalSchemaID 是 metadata 校验耗尽后、不含服务端身份的拒答 Schema ID。
	RAGAnswerMetadataRefusalSchemaID = "agent.rag-answer-metadata-refusal"
	// RAGAgentTurnSchemaID 标识 Eino ReAct 内部 assistant/tool-call turn 合同。
	RAGAgentTurnSchemaID = "agent.rag-read-agent-turn"
	// RAGAnswerContentSchemaID 标识最终 tool-free Markdown Stream 合同。
	RAGAnswerContentSchemaID = "agent.rag-answer-content"
	// RefusalSchemaID 是拒答输出的稳定 Schema ID。
	RefusalSchemaID = "agent.refusal"
	// FaithfulnessReviewSchemaID 是逐 assertion 审查输出的稳定 Schema ID。
	FaithfulnessReviewSchemaID = "agent.faithfulness-review"
	// ToolRequestSchemaID 是项目自有单次 Tool Request 的稳定 Schema ID。
	ToolRequestSchemaID = "agent.tool-request"
	// WorkspaceAnalysisPlanSchemaID 是受限工作区检索计划的稳定 Schema ID。
	WorkspaceAnalysisPlanSchemaID = "agent.workspace-analysis-plan"
	// WorkspaceAnalysisCandidateSchemaID 是发布前不可变候选答案的稳定 Schema ID。
	WorkspaceAnalysisCandidateSchemaID = "agent.workspace-analysis-candidate"
	// WorkspaceAnalysisCandidateSchemaVersion 是候选持久表使用的数字 Schema 版本。
	WorkspaceAnalysisCandidateSchemaVersion int64 = 1
	// OutputSchemaVersionV1 是项目自有 Agent 结构化输出的首个版本。
	OutputSchemaVersionV1 = "v1"
	// OutputSchemaVersionV2 是 RAG Answer 增量字段使用的第二个版本。
	OutputSchemaVersionV2 = "v2"

	ResultTypeRelationAssessment = "relation_assessment"
	ResultTypeRAGAnswer          = "rag_answer"
	ResultTypeRAGAnswerMetadata  = "rag_answer_metadata"
	ResultTypeRefusal            = "refusal"
	ResultTypeFaithfulnessReview = "faithfulness_review"
	// ResultTypeArtifactSection 是受控 Artifact 章节生成的稳定终态类型。
	ResultTypeArtifactSection = "artifact_section"
	// ResultTypeToolRequest 是独立 Tool Request 的稳定 Agent 结果类型。
	ResultTypeToolRequest = "tool_request"
	// ResultTypeClarification 是会话澄清终态使用的稳定 Agent 结果类型。
	ResultTypeClarification = "clarification"
	// ResultTypeDocumentKnowledgeProfile 是证据绑定文档知识画像的稳定结果类型。
	ResultTypeDocumentKnowledgeProfile = "document_knowledge_profile"
	// ResultTypeOrganizingOutline 是基于冻结整理材料生成的大纲结果类型。
	ResultTypeOrganizingOutline = "organizing_outline"
	// ResultTypeOrganizingDocument 是基于冻结整理材料生成的文档结果类型。
	ResultTypeOrganizingDocument = "organizing_document"
	// ResultTypeWorkspaceAnalysisPlan 是工作区分析 Query Planner 的稳定终态类型。
	ResultTypeWorkspaceAnalysisPlan = "workspace_analysis_plan"
	// ResultTypeWorkspaceAnalysisAnswer 是工作区分析 Synthesis Candidate 的稳定终态类型。
	ResultTypeWorkspaceAnalysisAnswer = "workspace_analysis_answer"

	maxOutputTextBytes = 16 * 1024
	maxSummaryBytes    = 4 * 1024
	maxReasonBytes     = 2 * 1024
	maxExcerptBytes    = 32 * 1024
	maxPayloadItems    = 500
	maxRelatedTopics   = 50
	maxFollowUps       = 5
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

// RelatedTopic 是由服务端 Knowledge 候选绑定到实际引用的相关主题。
type RelatedTopic struct {
	TopicID     foundation.ID `json:"topic_id"`
	Name        string        `json:"name"`
	CitationIDs []string      `json:"citation_ids"`
}

// RAGAnswerPayloadV2 在 v1 回答上增加可验证的相关主题和后续问题。
type RAGAnswerPayloadV2 struct {
	RAGAnswerPayload
	RelatedTopics     []RelatedTopic `json:"related_topics"`
	FollowUpQuestions []string       `json:"follow_up_questions"`
}

// RAGAnswerMetadataPayload 是历史 v1 metadata 载荷。v1 要求模型回显
// 服务端摘要；仅为已持久化运行兼容保留。
type RAGAnswerMetadataPayload struct {
	AnswerSHA256      string             `json:"answer_sha256"`
	Assertions        []Assertion        `json:"assertions"`
	Citations         []Citation         `json:"citations"`
	ConflictPositions []ConflictPosition `json:"conflict_positions"`
	ConflictSummary   string             `json:"conflict_summary"`
	RelatedTopics     []RelatedTopic     `json:"related_topics"`
	FollowUpQuestions []string           `json:"follow_up_questions"`
}

// Validate 校验摘要格式和完整 RAG v2 元数据闭包。
func (payload RAGAnswerMetadataPayload) Validate() error {
	if !canonicalSHA256(payload.AnswerSHA256) {
		return invalid(ErrorCodeAnswerInvalid, "rag answer metadata hash is invalid")
	}
	return validateRAGAnswerMetadataFields(
		payload.Assertions, payload.Citations, payload.ConflictPositions, payload.ConflictSummary,
		payload.RelatedTopics, payload.FollowUpQuestions,
	)
}

// RAGAnswerMetadataAssertionV2 是模型拥有的原子语义及其短 Evidence 引用。
// Citation 身份由项目绑定，不进入模型输出。
type RAGAnswerMetadataAssertionV2 struct {
	ID           string        `json:"id"`
	Text         string        `json:"text"`
	Kind         AssertionKind `json:"kind"`
	EvidenceRefs []string      `json:"evidence_refs"`
}

// RAGAnswerMetadataConflictPositionV2 只让模型为服务端短引用生成观点正文。
type RAGAnswerMetadataConflictPositionV2 struct {
	ConflictRef string `json:"conflict_ref"`
	Position    string `json:"position"`
}

// RAGAnswerMetadataPayloadV2 不包含正文或任何服务端身份。模型只选择
// E*/C*/T* 短引用；项目在同一调用栈中还原 Citation、Claim、Topic 和时间戳。
type RAGAnswerMetadataPayloadV2 struct {
	Assertions        []RAGAnswerMetadataAssertionV2        `json:"assertions"`
	ConflictPositions []RAGAnswerMetadataConflictPositionV2 `json:"conflict_positions"`
	ConflictSummary   string                                `json:"conflict_summary"`
	RelatedTopicRefs  []string                              `json:"related_topic_refs"`
	FollowUpQuestions []string                              `json:"follow_up_questions"`
}

// Validate 校验模型拥有字段的形状；服务端引用闭包在 Compose 时校验。
func (payload RAGAnswerMetadataPayloadV2) Validate() error {
	if len(payload.Assertions) == 0 || len(payload.Assertions) > maxPayloadItems ||
		payload.ConflictPositions == nil || len(payload.ConflictPositions) > maxPayloadItems ||
		len(payload.RelatedTopicRefs) == 0 || len(payload.RelatedTopicRefs) > maxRelatedTopics ||
		len(payload.FollowUpQuestions) == 0 || len(payload.FollowUpQuestions) > maxFollowUps ||
		!canonicalTextList(payload.FollowUpQuestions, true, maxFollowUps, maxReasonBytes) {
		return invalid(ErrorCodeAnswerInvalid, "rag answer metadata v2 payload is incomplete")
	}
	assertionIDs := make(map[string]struct{}, len(payload.Assertions))
	for _, assertion := range payload.Assertions {
		if !canonicalReference(assertion.ID, maxReferenceIDBytes) || strings.HasPrefix(assertion.ID, "@") ||
			!boundedText(assertion.Text, maxSummaryBytes, true) ||
			assertion.EvidenceRefs == nil || !canonicalMetadataReferences(assertion.EvidenceRefs, "E", false, maxPayloadItems) {
			return invalid(ErrorCodeAnswerInvalid, "rag answer metadata v2 assertion is invalid")
		}
		if _, duplicate := assertionIDs[assertion.ID]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "rag answer metadata v2 repeats an assertion")
		}
		assertionIDs[assertion.ID] = struct{}{}
		switch assertion.Kind {
		case AssertionFactual:
			if len(assertion.EvidenceRefs) == 0 {
				return invalid(ErrorCodeAnswerInvalid, "factual metadata assertion requires evidence")
			}
		case AssertionModelInference:
			if len(assertion.EvidenceRefs) != 0 {
				return invalid(ErrorCodeAnswerInvalid, "metadata inference cannot claim evidence")
			}
		default:
			return invalid(ErrorCodeAnswerInvalid, "metadata assertion kind is unsupported")
		}
	}
	conflictRefs := make(map[string]struct{}, len(payload.ConflictPositions))
	for _, position := range payload.ConflictPositions {
		if !canonicalMetadataReference(position.ConflictRef, "C", maxPayloadItems) ||
			!boundedText(position.Position, maxSummaryBytes, true) {
			return invalid(ErrorCodeAnswerInvalid, "rag answer metadata v2 conflict position is invalid")
		}
		if _, duplicate := conflictRefs[position.ConflictRef]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "rag answer metadata v2 repeats a conflict reference")
		}
		conflictRefs[position.ConflictRef] = struct{}{}
	}
	if len(payload.ConflictPositions) == 0 {
		if payload.ConflictSummary != "" {
			return invalid(ErrorCodeAnswerInvalid, "metadata cannot summarize conflicts without positions")
		}
	} else if len(payload.ConflictPositions) < 2 || !boundedText(payload.ConflictSummary, maxSummaryBytes, true) {
		return invalid(ErrorCodeAnswerInvalid, "metadata conflict disclosure requires both positions and a summary")
	}
	if !canonicalMetadataReferences(payload.RelatedTopicRefs, "T", true, maxRelatedTopics) {
		return invalid(ErrorCodeAnswerInvalid, "rag answer metadata v2 topic references are invalid")
	}
	seenQuestions := make(map[string]struct{}, len(payload.FollowUpQuestions))
	for _, question := range payload.FollowUpQuestions {
		if _, duplicate := seenQuestions[question]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "rag answer metadata v2 repeats a follow-up question")
		}
		seenQuestions[question] = struct{}{}
	}
	return nil
}

// RAGAnswerMetadataEvidenceBinding 将 E* 短引用绑定到项目 Citation 事实。
type RAGAnswerMetadataEvidenceBinding struct {
	Ref      string
	Citation Citation
}

// RAGAnswerMetadataConflictBinding 将 C* 短引用绑定到项目争议事实。
type RAGAnswerMetadataConflictBinding struct {
	Ref           string
	ClaimID       foundation.ID
	Applicability json.RawMessage
	CitationIDs   []string
	UpdatedAt     time.Time
}

// RAGAnswerMetadataTopicBinding 将 T* 短引用绑定到项目 Topic 事实。
type RAGAnswerMetadataTopicBinding struct {
	Ref   string
	Topic RelatedTopic
}

// RAGAnswerMetadataBindings 是一次 metadata 调用的服务端短引用事实表。
type RAGAnswerMetadataBindings struct {
	Evidence      []RAGAnswerMetadataEvidenceBinding
	Conflicts     []RAGAnswerMetadataConflictBinding
	RelatedTopics []RAGAnswerMetadataTopicBinding
}

// Validate 校验短引用顺序、身份唯一性及 Citation 闭包。
func (bindings RAGAnswerMetadataBindings) Validate() error {
	if len(bindings.Evidence) == 0 || len(bindings.Evidence) > maxPayloadItems ||
		bindings.Conflicts == nil || len(bindings.Conflicts) > maxPayloadItems ||
		len(bindings.RelatedTopics) == 0 || len(bindings.RelatedTopics) > maxRelatedTopics {
		return invalid(ErrorCodeAnswerInvalid, "rag answer metadata bindings are incomplete")
	}
	citations := make(map[string]Citation, len(bindings.Evidence))
	var workspaceID, indexVersionID foundation.ID
	for index, binding := range bindings.Evidence {
		if binding.Ref != metadataReference("E", index+1) || binding.Citation.Validate() != nil {
			return invalid(ErrorCodeAnswerInvalid, "rag answer evidence binding is invalid")
		}
		if _, duplicate := citations[binding.Citation.ID]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "rag answer evidence binding repeats a citation")
		}
		if index == 0 {
			workspaceID, indexVersionID = binding.Citation.WorkspaceID, binding.Citation.IndexVersionID
		} else if binding.Citation.WorkspaceID != workspaceID || binding.Citation.IndexVersionID != indexVersionID {
			return invalid(ErrorCodeAnswerInvalid, "rag answer evidence bindings cross workspace or index version")
		}
		citations[binding.Citation.ID] = binding.Citation
	}
	claims := make(map[foundation.ID]struct{}, len(bindings.Conflicts))
	for index, binding := range bindings.Conflicts {
		position := ConflictPosition{
			ClaimID: binding.ClaimID, Position: "validated-conflict-position", Applicability: binding.Applicability,
			CitationIDs: binding.CitationIDs, UpdatedAt: binding.UpdatedAt,
		}
		if binding.Ref != metadataReference("C", index+1) || position.Validate() != nil {
			return invalid(ErrorCodeAnswerInvalid, "rag answer conflict binding is invalid")
		}
		if _, duplicate := claims[binding.ClaimID]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "rag answer conflict binding repeats a claim")
		}
		claims[binding.ClaimID] = struct{}{}
		for _, citationID := range binding.CitationIDs {
			if _, exists := citations[citationID]; !exists {
				return invalid(ErrorCodeAnswerInvalid, "rag answer conflict binding references unknown evidence")
			}
		}
	}
	if len(bindings.Conflicts) == 1 {
		return invalid(ErrorCodeAnswerInvalid, "rag answer conflict bindings require both positions")
	}
	topics := make(map[foundation.ID]struct{}, len(bindings.RelatedTopics))
	for index, binding := range bindings.RelatedTopics {
		topic := binding.Topic
		displayName, _, err := knowledgedomain.NormalizeTopicText(topic.Name)
		if binding.Ref != metadataReference("T", index+1) || err != nil || displayName != topic.Name ||
			!canonicalID(topic.TopicID) || !canonicalUniqueReferences(topic.CitationIDs, true) {
			return invalid(ErrorCodeAnswerInvalid, "rag answer topic binding is invalid")
		}
		if _, duplicate := topics[topic.TopicID]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "rag answer topic binding repeats a topic")
		}
		topics[topic.TopicID] = struct{}{}
		for _, citationID := range topic.CitationIDs {
			if _, exists := citations[citationID]; !exists {
				return invalid(ErrorCodeAnswerInvalid, "rag answer topic binding references unknown evidence")
			}
		}
	}
	return nil
}

func validateRAGAnswerMetadataFields(
	assertions []Assertion,
	citations []Citation,
	conflictPositions []ConflictPosition,
	conflictSummary string,
	relatedTopics []RelatedTopic,
	followUpQuestions []string,
) error {
	candidate := RAGAnswerPayloadV2{
		RAGAnswerPayload: RAGAnswerPayload{
			Conclusion: "validated-stream-answer", Assertions: assertions, Citations: citations,
			ConflictPositions: conflictPositions, ConflictSummary: conflictSummary,
		},
		RelatedTopics: relatedTopics, FollowUpQuestions: followUpQuestions,
	}
	return candidate.Validate()
}

// Validate 校验 v2 扩展字段与 v1 Citation 闭包保持一致。
func (payload RAGAnswerPayloadV2) Validate() error {
	if err := payload.RAGAnswerPayload.Validate(); err != nil {
		return err
	}
	if len(payload.RelatedTopics) == 0 || len(payload.RelatedTopics) > maxRelatedTopics ||
		len(payload.FollowUpQuestions) == 0 || len(payload.FollowUpQuestions) > maxFollowUps ||
		!canonicalTextList(payload.FollowUpQuestions, true, maxFollowUps, maxReasonBytes) {
		return invalid(ErrorCodeAnswerInvalid, "rag answer v2 topics or follow-up questions are invalid")
	}
	citationIDs := make(map[string]struct{}, len(payload.Citations))
	for _, citation := range payload.Citations {
		citationIDs[citation.ID] = struct{}{}
	}
	seenTopics := make(map[foundation.ID]struct{}, len(payload.RelatedTopics))
	for _, topic := range payload.RelatedTopics {
		displayName, _, err := knowledgedomain.NormalizeTopicText(topic.Name)
		if err != nil || displayName != topic.Name || !canonicalID(topic.TopicID) || !canonicalUniqueReferences(topic.CitationIDs, true) {
			return invalid(ErrorCodeAnswerInvalid, "related topic binding is invalid")
		}
		if _, duplicate := seenTopics[topic.TopicID]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "related topic is duplicated")
		}
		seenTopics[topic.TopicID] = struct{}{}
		for _, citationID := range topic.CitationIDs {
			if _, exists := citationIDs[citationID]; !exists {
				return invalid(ErrorCodeAnswerInvalid, "related topic references an unknown citation")
			}
		}
	}
	seenQuestions := make(map[string]struct{}, len(payload.FollowUpQuestions))
	for _, question := range payload.FollowUpQuestions {
		if _, duplicate := seenQuestions[question]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "follow-up question is duplicated")
		}
		seenQuestions[question] = struct{}{}
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

// RAGAnswerResultV2 是向后兼容增加相关主题和后续问题的回答 Envelope。
type RAGAnswerResultV2 struct {
	ResultType    string             `json:"result_type"`
	SchemaID      string             `json:"schema_id"`
	SchemaVersion string             `json:"schema_version"`
	ModelRunRef   foundation.ID      `json:"model_run_ref"`
	Payload       RAGAnswerPayloadV2 `json:"payload"`
}

// RAGAnswerMetadataResult 是不会进入公开 Answer wire 的内部结构化结果。
type RAGAnswerMetadataResult struct {
	ResultType    string                   `json:"result_type"`
	SchemaID      string                   `json:"schema_id"`
	SchemaVersion string                   `json:"schema_version"`
	ModelRunRef   foundation.ID            `json:"model_run_ref"`
	Payload       RAGAnswerMetadataPayload `json:"payload"`
}

// RAGAnswerMetadataResultV2 是当前模型输出合同。最终正文、ModelRun 与
// 服务端知识身份均不由模型回显。
type RAGAnswerMetadataResultV2 struct {
	ResultType    string                     `json:"result_type"`
	SchemaID      string                     `json:"schema_id"`
	SchemaVersion string                     `json:"schema_version"`
	Payload       RAGAnswerMetadataPayloadV2 `json:"payload"`
}

// RAGAnswerMetadataRefusalResultV2 是 metadata REDUCED 阶段的模型输出。
// ModelRunRef 由项目在可信边界内注入，不能要求模型回显。
type RAGAnswerMetadataRefusalResultV2 struct {
	ResultType    string         `json:"result_type"`
	SchemaID      string         `json:"schema_id"`
	SchemaVersion string         `json:"schema_version"`
	Payload       RefusalPayload `json:"payload"`
}

// Validate 校验 metadata envelope 与 payload。
func (result RAGAnswerMetadataResult) Validate() error {
	if result.ResultType != ResultTypeRAGAnswerMetadata || result.SchemaID != RAGAnswerMetadataSchemaID ||
		result.SchemaVersion != OutputSchemaVersionV1 || !canonicalID(result.ModelRunRef) {
		return invalid(ErrorCodeSchemaInvalid, "rag answer metadata envelope is invalid")
	}
	return result.Payload.Validate()
}

// Validate 校验 v2 metadata envelope 与 payload。
func (result RAGAnswerMetadataResultV2) Validate() error {
	if result.ResultType != ResultTypeRAGAnswerMetadata || result.SchemaID != RAGAnswerMetadataSchemaID ||
		result.SchemaVersion != OutputSchemaVersionV2 {
		return invalid(ErrorCodeSchemaInvalid, "rag answer metadata v2 envelope is invalid")
	}
	return result.Payload.Validate()
}

// Validate 校验 metadata 专用的无身份拒答 envelope。
func (result RAGAnswerMetadataRefusalResultV2) Validate() error {
	if result.ResultType != ResultTypeRefusal || result.SchemaID != RAGAnswerMetadataRefusalSchemaID ||
		result.SchemaVersion != OutputSchemaVersionV2 {
		return invalid(ErrorCodeSchemaInvalid, "rag answer metadata refusal envelope is invalid")
	}
	if err := result.Payload.Validate(); err != nil {
		return err
	}
	if !canonicalUniqueTextList(result.Payload.MissingRequirements, true, 50, maxReasonBytes) ||
		!canonicalUniqueTextList(result.Payload.SuggestedActions, true, 50, maxReasonBytes) {
		return invalid(ErrorCodeRefusalInvalid, "rag answer metadata refusal lists are invalid")
	}
	return nil
}

// ComposeRefusal 在模型输出通过严格校验后绑定项目拥有的 ModelRunRef。
func (result RAGAnswerMetadataRefusalResultV2) ComposeRefusal(modelRunRef foundation.ID) (RefusalResult, error) {
	if err := result.Validate(); err != nil {
		return RefusalResult{}, err
	}
	refusal := RefusalResult{
		ResultType: ResultTypeRefusal, SchemaID: RefusalSchemaID, SchemaVersion: OutputSchemaVersionV1,
		ModelRunRef: modelRunRef, Payload: result.Payload,
	}
	if err := refusal.Validate(); err != nil {
		return RefusalResult{}, err
	}
	return refusal, nil
}

// ComposeRAGAnswerV2 把原始 Stream 字节与 metadata 确定性组合为公开结果。
// 正文不会 trim、重写或从 metadata 中读取。
func (result RAGAnswerMetadataResult) ComposeRAGAnswerV2(finalText string) (RAGAnswerResultV2, error) {
	if err := result.Validate(); err != nil {
		return RAGAnswerResultV2{}, err
	}
	sum := sha256.Sum256([]byte(finalText))
	if hex.EncodeToString(sum[:]) != result.Payload.AnswerSHA256 {
		return RAGAnswerResultV2{}, invalid(ErrorCodeAnswerInvalid, "rag answer metadata does not bind the final stream")
	}
	return composeRAGAnswerV2(
		result.ModelRunRef, finalText, result.Payload.Assertions, result.Payload.Citations,
		result.Payload.ConflictPositions, result.Payload.ConflictSummary, result.Payload.RelatedTopics,
		result.Payload.FollowUpQuestions,
	)
}

// ComposeRAGAnswerV2 把同一次已完整消费的 Stream、项目 ModelRun 和服务端
// 短引用绑定组装为公开结果。正文和身份都不会从模型 metadata 中读取。
func (result RAGAnswerMetadataResultV2) ComposeRAGAnswerV2(modelRunRef foundation.ID, finalText string, bindings RAGAnswerMetadataBindings) (RAGAnswerResultV2, error) {
	if err := result.Validate(); err != nil {
		return RAGAnswerResultV2{}, err
	}
	if !canonicalID(modelRunRef) {
		return RAGAnswerResultV2{}, invalid(ErrorCodeAnswerInvalid, "rag answer metadata model run binding is invalid")
	}
	if err := bindings.Validate(); err != nil {
		return RAGAnswerResultV2{}, err
	}
	evidenceByRef := make(map[string]Citation, len(bindings.Evidence))
	for _, binding := range bindings.Evidence {
		evidenceByRef[binding.Ref] = binding.Citation
	}
	assertions := make([]Assertion, 0, len(result.Payload.Assertions))
	referencedCitations := make(map[string]struct{}, len(bindings.Evidence))
	for _, metadataAssertion := range result.Payload.Assertions {
		citationIDs := make([]string, 0, len(metadataAssertion.EvidenceRefs))
		for _, ref := range metadataAssertion.EvidenceRefs {
			citation, exists := evidenceByRef[ref]
			if !exists {
				return RAGAnswerResultV2{}, invalid(ErrorCodeAnswerInvalid, "metadata assertion references unknown evidence")
			}
			citationIDs = append(citationIDs, citation.ID)
			referencedCitations[citation.ID] = struct{}{}
		}
		assertions = append(assertions, Assertion{
			ID: metadataAssertion.ID, Text: metadataAssertion.Text, Kind: metadataAssertion.Kind, CitationIDs: citationIDs,
		})
	}
	positionsByRef := make(map[string]string, len(result.Payload.ConflictPositions))
	for _, position := range result.Payload.ConflictPositions {
		positionsByRef[position.ConflictRef] = position.Position
	}
	if len(positionsByRef) != len(bindings.Conflicts) {
		return RAGAnswerResultV2{}, invalid(ErrorCodeAnswerInvalid, "metadata conflict positions do not cover the server bindings")
	}
	conflictPositions := make([]ConflictPosition, 0, len(bindings.Conflicts))
	for _, binding := range bindings.Conflicts {
		position, exists := positionsByRef[binding.Ref]
		if !exists {
			return RAGAnswerResultV2{}, invalid(ErrorCodeAnswerInvalid, "metadata conflict positions do not cover the server bindings")
		}
		citationIDs := append([]string(nil), binding.CitationIDs...)
		for _, citationID := range citationIDs {
			referencedCitations[citationID] = struct{}{}
		}
		conflictPositions = append(conflictPositions, ConflictPosition{
			ClaimID: binding.ClaimID, Position: position, Applicability: append(json.RawMessage(nil), binding.Applicability...),
			CitationIDs: citationIDs, UpdatedAt: binding.UpdatedAt,
		})
	}
	topicsByRef := make(map[string]RelatedTopic, len(bindings.RelatedTopics))
	for _, binding := range bindings.RelatedTopics {
		topicsByRef[binding.Ref] = binding.Topic
	}
	relatedTopics := make([]RelatedTopic, 0, len(result.Payload.RelatedTopicRefs))
	for _, ref := range result.Payload.RelatedTopicRefs {
		topic, exists := topicsByRef[ref]
		if !exists {
			return RAGAnswerResultV2{}, invalid(ErrorCodeAnswerInvalid, "metadata references an unknown related topic")
		}
		topic.CitationIDs = append([]string(nil), topic.CitationIDs...)
		for _, citationID := range topic.CitationIDs {
			referencedCitations[citationID] = struct{}{}
		}
		relatedTopics = append(relatedTopics, topic)
	}
	citations := make([]Citation, 0, len(referencedCitations))
	for _, binding := range bindings.Evidence {
		if _, referenced := referencedCitations[binding.Citation.ID]; referenced {
			citations = append(citations, binding.Citation)
		}
	}
	return composeRAGAnswerV2(
		modelRunRef, finalText, assertions, citations,
		conflictPositions, result.Payload.ConflictSummary, relatedTopics,
		result.Payload.FollowUpQuestions,
	)
}

func composeRAGAnswerV2(
	modelRunRef foundation.ID,
	finalText string,
	assertions []Assertion,
	citations []Citation,
	conflictPositions []ConflictPosition,
	conflictSummary string,
	relatedTopics []RelatedTopic,
	followUpQuestions []string,
) (RAGAnswerResultV2, error) {
	answer := RAGAnswerResultV2{
		ResultType: ResultTypeRAGAnswer, SchemaID: RAGAnswerSchemaID, SchemaVersion: OutputSchemaVersionV2,
		ModelRunRef: modelRunRef,
		Payload: RAGAnswerPayloadV2{
			RAGAnswerPayload: RAGAnswerPayload{
				Conclusion: finalText, Assertions: assertions, Citations: citations,
				ConflictPositions: conflictPositions, ConflictSummary: conflictSummary,
			},
			RelatedTopics: relatedTopics, FollowUpQuestions: followUpQuestions,
		},
	}
	if err := answer.Validate(); err != nil {
		return RAGAnswerResultV2{}, err
	}
	return answer, nil
}

// Validate 校验 RAG Answer v2 Envelope 与扩展载荷。
func (result RAGAnswerResultV2) Validate() error {
	if result.ResultType != ResultTypeRAGAnswer || result.SchemaID != RAGAnswerSchemaID ||
		result.SchemaVersion != OutputSchemaVersionV2 || !canonicalID(result.ModelRunRef) {
		return invalid(ErrorCodeSchemaInvalid, "rag answer v2 envelope is invalid")
	}
	return result.Payload.Validate()
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

// DecodeRAGAnswerV2 严格解析一个 RAG answer v2 文档。
func DecodeRAGAnswerV2(raw []byte, limits DecodeLimits) (RAGAnswerResultV2, error) {
	return DecodeStrict(raw, limits, RAGAnswerResultV2.Validate)
}

// DecodeRAGAnswerMetadata 严格解析一个内部 metadata v1 文档。
func DecodeRAGAnswerMetadata(raw []byte, limits DecodeLimits) (RAGAnswerMetadataResult, error) {
	return DecodeStrict(raw, limits, RAGAnswerMetadataResult.Validate)
}

// DecodeRAGAnswerMetadataV2 严格解析一个当前 metadata v2 文档。
func DecodeRAGAnswerMetadataV2(raw []byte, limits DecodeLimits) (RAGAnswerMetadataResultV2, error) {
	return DecodeStrict(raw, limits, RAGAnswerMetadataResultV2.Validate)
}

// DecodeRAGAnswerMetadataRefusalV2 严格解析 metadata REDUCED 的无身份拒答。
func DecodeRAGAnswerMetadataRefusalV2(raw []byte, limits DecodeLimits) (RAGAnswerMetadataRefusalResultV2, error) {
	return DecodeStrict(raw, limits, RAGAnswerMetadataRefusalResultV2.Validate)
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

func canonicalSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func canonicalID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func boundedText(value string, maximum int, required bool) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') || len(value) > maximum {
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

func canonicalMetadataReferences(values []string, prefix string, required bool, maximum int) bool {
	if len(values) > maximum || (required && len(values) == 0) {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !canonicalMetadataReference(value, prefix, maximum) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func canonicalMetadataReference(value, prefix string, maximum int) bool {
	if !strings.HasPrefix(value, prefix) || len(value) <= len(prefix) {
		return false
	}
	number := value[len(prefix):]
	if number[0] == '0' {
		return false
	}
	parsed, err := strconv.Atoi(number)
	return err == nil && parsed > 0 && parsed <= maximum && value == metadataReference(prefix, parsed)
}

func metadataReference(prefix string, number int) string {
	return prefix + strconv.Itoa(number)
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

func canonicalUniqueTextList(values []string, required bool, maximumItems, maximumBytes int) bool {
	if !canonicalTextList(values, required, maximumItems, maximumBytes) {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
