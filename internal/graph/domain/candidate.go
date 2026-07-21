package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	ErrorCodeSemanticLinkCandidateInvalid           = "GRAPH_CANDIDATE_INVALID"
	ErrorCodeSemanticLinkCandidateDecisionInvalid   = "GRAPH_CANDIDATE_DECISION_INVALID"
	ErrorCodeSemanticLinkCandidateTransitionInvalid = "GRAPH_CANDIDATE_TRANSITION_INVALID"
	ErrorCodeSemanticLinkCandidateRequestInvalid    = "GRAPH_CANDIDATE_REQUEST_INVALID"
	ErrorCodeSemanticLinkCandidateResultInvalid     = "GRAPH_CANDIDATE_RESULT_INVALID"
	ErrorCodeSemanticLinkCandidateNotFound          = "GRAPH_CANDIDATE_NOT_FOUND"

	MaxSemanticLinkCandidateEvidence         = 100
	MaxSemanticLinkDiscoveryMethodCount      = 6
	semanticLinkCandidateFingerprintSchemaV1 = "semantic-link-candidate/v1"
)

// SemanticLinkCandidateStatus 是待审阅语义关联候选的受控生命周期状态。
type SemanticLinkCandidateStatus string

const (
	SemanticLinkCandidateStatusActive          SemanticLinkCandidateStatus = "ACTIVE"
	SemanticLinkCandidateStatusDeferred        SemanticLinkCandidateStatus = "DEFERRED"
	SemanticLinkCandidateStatusIgnored         SemanticLinkCandidateStatus = "IGNORED"
	SemanticLinkCandidateStatusFalsePositive   SemanticLinkCandidateStatus = "FALSE_POSITIVE"
	SemanticLinkCandidateStatusProposalCreated SemanticLinkCandidateStatus = "PROPOSAL_CREATED"
	SemanticLinkCandidateStatusSuperseded      SemanticLinkCandidateStatus = "SUPERSEDED"
)

// SemanticLinkDiscoveryMethod 表示一次候选发现实际命中的信号。
type SemanticLinkDiscoveryMethod string

const (
	SemanticLinkDiscoveryMethodTitleAlias              SemanticLinkDiscoveryMethod = "TITLE_ALIAS"
	SemanticLinkDiscoveryMethodTermMatch               SemanticLinkDiscoveryMethod = "TERM_MATCH"
	SemanticLinkDiscoveryMethodClaimSemanticSimilarity SemanticLinkDiscoveryMethod = "CLAIM_SEMANTIC_SIMILARITY"
	SemanticLinkDiscoveryMethodCommonTopic             SemanticLinkDiscoveryMethod = "COMMON_TOPIC"
	SemanticLinkDiscoveryMethodSharedSource            SemanticLinkDiscoveryMethod = "SHARED_SOURCE"
	SemanticLinkDiscoveryMethodRAGCoRetrieval          SemanticLinkDiscoveryMethod = "RAG_CO_RETRIEVAL"
)

// SemanticLinkCandidateReopenedReason 表示重新打开候选的冻结原因。
type SemanticLinkCandidateReopenedReason string

const (
	SemanticLinkCandidateReopenedReasonContentChanged SemanticLinkCandidateReopenedReason = "CONTENT_CHANGED"
)

// SemanticLinkCandidateDecisionAction 表示用户对候选的受控操作。
type SemanticLinkCandidateDecisionAction string

const (
	SemanticLinkCandidateDecisionConfirm                 SemanticLinkCandidateDecisionAction = "CONFIRM"
	SemanticLinkCandidateDecisionConfirmWithRelationType SemanticLinkCandidateDecisionAction = "CONFIRM_WITH_RELATION_TYPE"
	SemanticLinkCandidateDecisionIgnore                  SemanticLinkCandidateDecisionAction = "IGNORE"
	SemanticLinkCandidateDecisionFalsePositive           SemanticLinkCandidateDecisionAction = "FALSE_POSITIVE"
	SemanticLinkCandidateDecisionDefer                   SemanticLinkCandidateDecisionAction = "DEFER"
	SemanticLinkCandidateDecisionResume                  SemanticLinkCandidateDecisionAction = "RESUME"
)

// SemanticLinkCandidateEndpoint 是一侧候选端点的稳定快照。
type SemanticLinkCandidateEndpoint struct {
	Ref     knowledge.NodeRef
	Version int64
	Summary string
	Excerpt string
}

// SemanticLinkCandidateEvidence 是发现阶段的不可变证据引用。
type SemanticLinkCandidateEvidence struct {
	ID           foundation.ID
	Provenance   knowledge.ProvenanceRef
	SemanticHash string
	Reason       string
	Excerpt      string
}

// SemanticLinkCandidateGeneration 冻结参与发现的版本事实。
type SemanticLinkCandidateGeneration struct {
	IndexVersionID      *foundation.ID `json:"index_version_id,omitempty"`
	EmbeddingVersionID  *foundation.ID `json:"embedding_version_id,omitempty"`
	RerankVersionID     *foundation.ID `json:"rerank_version_id,omitempty"`
	ModelVersion        string         `json:"model_version,omitempty"`
	ModelProfileVersion string         `json:"model_profile_version,omitempty"`
	PromptVersion       string         `json:"prompt_version,omitempty"`
	SchemaVersion       string         `json:"schema_version,omitempty"`
	RuleID              *foundation.ID `json:"rule_id,omitempty"`
	RuleVersion         string         `json:"rule_version,omitempty"`
	ModelRunID          *foundation.ID `json:"model_run_id,omitempty"`
}

// SemanticLinkCandidate 是 Graph 模块拥有的待审阅语义关联发现记录。
type SemanticLinkCandidate struct {
	ID                      foundation.ID
	WorkspaceID             foundation.ID
	Source                  SemanticLinkCandidateEndpoint
	Target                  SemanticLinkCandidateEndpoint
	SuggestedRelationType   knowledge.RelationType
	Status                  SemanticLinkCandidateStatus
	Reason                  string
	Confidence              float64
	DiscoveryMethods        []SemanticLinkDiscoveryMethod
	Evidence                []SemanticLinkCandidateEvidence
	Generation              SemanticLinkCandidateGeneration
	Fingerprint             string
	ProposalID              *foundation.ID
	ReopenedFromCandidateID *foundation.ID
	ReopenedReason          SemanticLinkCandidateReopenedReason
	ResumeAfter             *time.Time
	Version                 int64
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// SemanticLinkCandidateQuery 是候选列表的有界查询合同。
type SemanticLinkCandidateQuery struct {
	WorkspaceID     foundation.ID
	NodeRef         *knowledge.NodeRef
	Statuses        []SemanticLinkCandidateStatus
	RelationTypes   []knowledge.RelationType
	ReopenedReasons []SemanticLinkCandidateReopenedReason
	MinConfidence   *float64
	Limit           int
}

// SemanticLinkCandidatePage 是候选列表的有界查询结果。
type SemanticLinkCandidatePage struct {
	WorkspaceID foundation.ID
	Items       []SemanticLinkCandidate
	Meta        PageMeta
}

// SemanticLinkCandidateDecision 是用户对单个候选的受控决策。
type SemanticLinkCandidateDecision struct {
	Action       SemanticLinkCandidateDecisionAction
	Reason       string
	RelationType *knowledge.RelationType
	ResumeAfter  *time.Time
}

// ValidateSemanticLinkCandidate 校验候选身份、端点、证据、版本和 fingerprint。
func ValidateSemanticLinkCandidate(candidate SemanticLinkCandidate) error {
	if !validID(candidate.ID) || !validID(candidate.WorkspaceID) || candidate.ID == candidate.WorkspaceID ||
		candidate.Version <= 0 || candidate.CreatedAt.IsZero() || candidate.UpdatedAt.Before(candidate.CreatedAt) ||
		!validCandidateStatus(candidate.Status) || candidate.Confidence < 0 || candidate.Confidence > 1 ||
		math.IsNaN(candidate.Confidence) || math.IsInf(candidate.Confidence, 0) {
		return candidateInvalid("candidate identity or lifecycle is invalid")
	}
	if err := validateSemanticLinkCandidateEndpoint(candidate.WorkspaceID, candidate.Source); err != nil {
		return err
	}
	if err := validateSemanticLinkCandidateEndpoint(candidate.WorkspaceID, candidate.Target); err != nil {
		return err
	}
	if candidate.Source.Ref == candidate.Target.Ref {
		return candidateInvalid("candidate endpoints must not be identical")
	}
	canonicalSource, canonicalTarget, err := knowledge.CanonicalizeRelationEndpoints(candidate.SuggestedRelationType, candidate.Source.Ref, candidate.Target.Ref)
	if err != nil || canonicalSource != candidate.Source.Ref || canonicalTarget != candidate.Target.Ref {
		return candidateInvalid("candidate endpoints are not canonical or compatible")
	}
	if err := validateSemanticLinkCandidateGeneration(candidate.Generation); err != nil {
		return err
	}
	if err := validateSemanticLinkCandidateDiscoveryMethods(candidate.DiscoveryMethods); err != nil {
		return err
	}
	if err := validateSemanticLinkCandidateEvidence(candidate.WorkspaceID, candidate.Evidence); err != nil {
		return err
	}
	reason, err := knowledge.NormalizeReason(candidate.Reason, true)
	if err != nil || reason != candidate.Reason {
		return candidateInvalid("candidate reason is not canonical")
	}
	if err := validateSemanticLinkCandidateReopenBinding(candidate); err != nil {
		return err
	}
	if err := validateSemanticLinkCandidateOptionals(candidate); err != nil {
		return err
	}
	if candidate.Status == SemanticLinkCandidateStatusProposalCreated {
		if candidate.ProposalID == nil || !validID(*candidate.ProposalID) {
			return candidateInvalid("proposal-created candidate requires a proposal binding")
		}
	} else if candidate.Status == SemanticLinkCandidateStatusSuperseded {
		if candidate.ProposalID != nil && !validID(*candidate.ProposalID) {
			return candidateInvalid("superseded candidate proposal binding is invalid")
		}
	} else if candidate.ProposalID != nil {
		return candidateInvalid("non-proposal candidate must not carry a proposal binding")
	}
	fingerprint, err := ComputeSemanticLinkCandidateFingerprint(candidate)
	if err != nil {
		return err
	}
	if fingerprint != candidate.Fingerprint {
		return candidateInconsistent("candidate fingerprint is inconsistent")
	}
	return nil
}

// ValidateSemanticLinkCandidateTransition 校验状态迁移是否遵循冻结矩阵。
func ValidateSemanticLinkCandidateTransition(from, to SemanticLinkCandidateStatus) error {
	allowed := map[SemanticLinkCandidateStatus]map[SemanticLinkCandidateStatus]struct{}{
		SemanticLinkCandidateStatusActive: {
			SemanticLinkCandidateStatusDeferred:        {},
			SemanticLinkCandidateStatusIgnored:         {},
			SemanticLinkCandidateStatusFalsePositive:   {},
			SemanticLinkCandidateStatusProposalCreated: {},
			SemanticLinkCandidateStatusSuperseded:      {},
		},
		SemanticLinkCandidateStatusDeferred: {
			SemanticLinkCandidateStatusActive:          {},
			SemanticLinkCandidateStatusIgnored:         {},
			SemanticLinkCandidateStatusFalsePositive:   {},
			SemanticLinkCandidateStatusProposalCreated: {},
			SemanticLinkCandidateStatusSuperseded:      {},
		},
		SemanticLinkCandidateStatusIgnored: {
			SemanticLinkCandidateStatusSuperseded: {},
		},
		SemanticLinkCandidateStatusFalsePositive: {
			SemanticLinkCandidateStatusSuperseded: {},
		},
		SemanticLinkCandidateStatusProposalCreated: {
			SemanticLinkCandidateStatusSuperseded: {},
		},
	}
	if _, ok := allowed[from][to]; ok {
		return nil
	}
	return versionConflictCandidate("candidate status transition is not allowed")
}

// ValidateSemanticLinkCandidatePage 校验候选页面的 Workspace 绑定、过滤器和稳定排序。
func ValidateSemanticLinkCandidatePage(request SemanticLinkCandidateQuery, page SemanticLinkCandidatePage) error {
	if err := ValidateSemanticLinkCandidateQuery(request); err != nil {
		return err
	}
	if page.WorkspaceID != request.WorkspaceID || validatePageMeta(page.Meta) != nil {
		return candidateInconsistent("candidate page binding is inconsistent")
	}
	if err := validateSemanticLinkCandidateItems(request, page.Items, request.Limit); err != nil {
		return err
	}
	return nil
}

// ValidateSemanticLinkCandidateWindow 校验 Adapter 返回的完整有界候选窗口。
// windowLimit 是内部窗口上限，用户分页 Limit 仍由 Application 负责。
func ValidateSemanticLinkCandidateWindow(request SemanticLinkCandidateQuery, items []SemanticLinkCandidate, windowLimit int) error {
	if err := ValidateSemanticLinkCandidateQuery(request); err != nil {
		return err
	}
	if windowLimit < request.Limit || windowLimit > 500 {
		return candidateRequestInvalid("candidate result window limit is invalid")
	}
	return validateSemanticLinkCandidateItems(request, items, windowLimit)
}

func validateSemanticLinkCandidateItems(request SemanticLinkCandidateQuery, items []SemanticLinkCandidate, maxItems int) error {
	if len(items) > maxItems {
		return candidateInconsistent("candidate result window exceeds its bound")
	}
	seen := map[foundation.ID]struct{}{}
	var previous *SemanticLinkCandidate
	for _, candidate := range items {
		if err := ValidateSemanticLinkCandidate(candidate); err != nil {
			return err
		}
		if candidate.WorkspaceID != request.WorkspaceID {
			return candidateInconsistent("candidate page item is cross-workspace")
		}
		if !candidateMatchesQuery(request, candidate) {
			return candidateInconsistent("candidate page item violates query filters")
		}
		if _, duplicate := seen[candidate.ID]; duplicate {
			return candidateInconsistent("candidate page items must be unique")
		}
		if previous != nil && !candidatePageLess(*previous, candidate) {
			return candidateInconsistent("candidate page items must be stably ordered")
		}
		seen[candidate.ID] = struct{}{}
		copyCandidate := candidate
		previous = &copyCandidate
	}
	return nil
}

// ValidateSemanticLinkCandidateQuery 校验候选列表请求的边界和过滤器。
func ValidateSemanticLinkCandidateQuery(request SemanticLinkCandidateQuery) error {
	if !validID(request.WorkspaceID) || request.Limit < 1 || request.Limit > MaxLimit || invalidConfidence(request.MinConfidence) {
		return candidateRequestInvalid("candidate query identity or limit is invalid")
	}
	if request.NodeRef != nil && !validSemanticLinkNodeRef(*request.NodeRef) {
		return candidateRequestInvalid("candidate node filter is invalid")
	}
	if !uniqueCandidateStatuses(request.Statuses) || !uniqueRelationTypes(request.RelationTypes) || !uniqueReopenedReasons(request.ReopenedReasons) {
		return candidateRequestInvalid("candidate query filters are invalid")
	}
	return nil
}

// ValidateSemanticLinkCandidateDecision 校验单次决策的动作、原因和目标关系类型。
func ValidateSemanticLinkCandidateDecision(candidate SemanticLinkCandidate, decision SemanticLinkCandidateDecision) error {
	if err := ValidateSemanticLinkCandidate(candidate); err != nil {
		return err
	}
	if !validCandidateDecisionAction(decision.Action) {
		return candidateDecisionInvalid("candidate decision action is unsupported")
	}
	if decision.ResumeAfter != nil && decision.ResumeAfter.IsZero() {
		return candidateDecisionInvalid("candidate defer resume time is invalid")
	}
	switch decision.Action {
	case SemanticLinkCandidateDecisionConfirm:
		if decision.RelationType != nil || strings.TrimSpace(decision.Reason) != "" || decision.ResumeAfter != nil {
			return candidateDecisionInvalid("confirm does not accept relation type, reason, or resume time")
		}
	case SemanticLinkCandidateDecisionConfirmWithRelationType:
		if decision.RelationType == nil || strings.TrimSpace(decision.Reason) != "" || decision.ResumeAfter != nil {
			return candidateDecisionInvalid("typed confirm requires a relation type and nothing else")
		}
		if canonicalSource, canonicalTarget, err := knowledge.CanonicalizeRelationEndpoints(*decision.RelationType, candidate.Source.Ref, candidate.Target.Ref); err != nil || canonicalSource != candidate.Source.Ref || canonicalTarget != candidate.Target.Ref {
			return candidateDecisionInvalid("typed confirm relation type is incompatible with candidate endpoints")
		}
	case SemanticLinkCandidateDecisionIgnore, SemanticLinkCandidateDecisionFalsePositive:
		reason, err := knowledge.NormalizeReason(decision.Reason, true)
		if err != nil || reason != decision.Reason || decision.RelationType != nil || decision.ResumeAfter != nil {
			return candidateDecisionInvalid("ignore or false positive requires a canonical reason only")
		}
	case SemanticLinkCandidateDecisionDefer:
		if decision.RelationType != nil {
			return candidateDecisionInvalid("defer does not accept a relation type")
		}
		if decision.Reason != "" {
			reason, err := knowledge.NormalizeReason(decision.Reason, true)
			if err != nil || reason != decision.Reason {
				return candidateDecisionInvalid("defer reason is not canonical")
			}
		}
	case SemanticLinkCandidateDecisionResume:
		if decision.RelationType != nil || decision.Reason != "" || decision.ResumeAfter != nil {
			return candidateDecisionInvalid("resume does not accept reason, relation type, or resume time")
		}
	}
	next, err := NextSemanticLinkCandidateStatus(candidate.Status, decision)
	if err != nil {
		return err
	}
	if candidate.Status == SemanticLinkCandidateStatusProposalCreated && next != candidate.Status {
		return candidateDecisionInvalid("proposal-created candidate is terminal")
	}
	return nil
}

// NextSemanticLinkCandidateStatus 计算一次决策后的下一个状态。
func NextSemanticLinkCandidateStatus(from SemanticLinkCandidateStatus, decision SemanticLinkCandidateDecision) (SemanticLinkCandidateStatus, error) {
	switch decision.Action {
	case SemanticLinkCandidateDecisionConfirm, SemanticLinkCandidateDecisionConfirmWithRelationType:
		if from != SemanticLinkCandidateStatusActive && from != SemanticLinkCandidateStatusDeferred {
			return "", versionConflictCandidate("confirm is only allowed for active or deferred candidates")
		}
		return SemanticLinkCandidateStatusProposalCreated, nil
	case SemanticLinkCandidateDecisionIgnore:
		if from != SemanticLinkCandidateStatusActive && from != SemanticLinkCandidateStatusDeferred {
			return "", versionConflictCandidate("ignore is only allowed for active or deferred candidates")
		}
		return SemanticLinkCandidateStatusIgnored, nil
	case SemanticLinkCandidateDecisionFalsePositive:
		if from != SemanticLinkCandidateStatusActive && from != SemanticLinkCandidateStatusDeferred {
			return "", versionConflictCandidate("false positive is only allowed for active or deferred candidates")
		}
		return SemanticLinkCandidateStatusFalsePositive, nil
	case SemanticLinkCandidateDecisionDefer:
		if from != SemanticLinkCandidateStatusActive {
			return "", versionConflictCandidate("defer is only allowed for active candidates")
		}
		return SemanticLinkCandidateStatusDeferred, nil
	case SemanticLinkCandidateDecisionResume:
		if from != SemanticLinkCandidateStatusDeferred {
			return "", versionConflictCandidate("resume is only allowed for deferred candidates")
		}
		return SemanticLinkCandidateStatusActive, nil
	default:
		return "", candidateDecisionInvalid("candidate decision action is unsupported")
	}
}

// ComputeSemanticLinkCandidateFingerprint 计算候选 fingerprint 的版本化 canonical SHA-256。
func ComputeSemanticLinkCandidateFingerprint(candidate SemanticLinkCandidate) (string, error) {
	canonicalSource, canonicalTarget, err := knowledge.CanonicalizeRelationEndpoints(candidate.SuggestedRelationType, candidate.Source.Ref, candidate.Target.Ref)
	if err != nil {
		return "", candidateInvalid("candidate endpoints are invalid")
	}
	source := candidate.Source
	target := candidate.Target
	if canonicalSource == candidate.Target.Ref && canonicalTarget == candidate.Source.Ref {
		source, target = candidate.Target, candidate.Source
	}
	evidenceHashes, err := candidateEvidenceHashes(candidate.Evidence)
	if err != nil {
		return "", err
	}
	discoveryMethods, err := candidateDiscoveryMethods(candidate.DiscoveryMethods)
	if err != nil {
		return "", err
	}
	generation, err := canonicalSemanticLinkCandidateGeneration(candidate.Generation)
	if err != nil {
		return "", err
	}
	payload := struct {
		SchemaVersion    string                                 `json:"schema_version"`
		WorkspaceID      foundation.ID                          `json:"workspace_id"`
		Source           semanticLinkCandidateEndpointPayload   `json:"source"`
		Target           semanticLinkCandidateEndpointPayload   `json:"target"`
		SuggestedType    knowledge.RelationType                 `json:"suggested_relation_type"`
		EvidenceHashes   []string                               `json:"evidence_hashes"`
		DiscoveryMethods []SemanticLinkDiscoveryMethod          `json:"discovery_methods"`
		Generation       semanticLinkCandidateGenerationPayload `json:"generation"`
	}{
		SchemaVersion:    semanticLinkCandidateFingerprintSchemaV1,
		WorkspaceID:      candidate.WorkspaceID,
		Source:           semanticLinkCandidateEndpointPayload{Ref: source.Ref, Version: source.Version},
		Target:           semanticLinkCandidateEndpointPayload{Ref: target.Ref, Version: target.Version},
		SuggestedType:    candidate.SuggestedRelationType,
		EvidenceHashes:   evidenceHashes,
		DiscoveryMethods: discoveryMethods,
		Generation:       generation,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", candidateInconsistent("candidate fingerprint payload encoding failed")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func validateSemanticLinkCandidateEndpoint(workspaceID foundation.ID, endpoint SemanticLinkCandidateEndpoint) error {
	if !validSemanticLinkNodeRef(endpoint.Ref) || endpoint.Version <= 0 {
		return candidateInvalid("candidate endpoint identity is invalid")
	}
	if endpoint.Ref.Type != knowledge.NodeTypeTopic && endpoint.Ref.Type != knowledge.NodeTypeClaim {
		return candidateInvalid("candidate endpoint type is unsupported")
	}
	if strings.TrimSpace(endpoint.Summary) == "" {
		return candidateInvalid("candidate endpoint summary is required")
	}
	summary, err := knowledge.NormalizeReason(endpoint.Summary, true)
	if err != nil || summary != endpoint.Summary {
		return candidateInvalid("candidate endpoint summary is not canonical")
	}
	if endpoint.Excerpt != "" {
		excerpt, err := knowledge.NormalizeReason(endpoint.Excerpt, true)
		if err != nil || excerpt != endpoint.Excerpt {
			return candidateInvalid("candidate endpoint excerpt is not canonical")
		}
	}
	return nil
}

func validateSemanticLinkCandidateGeneration(generation SemanticLinkCandidateGeneration) error {
	if generation.IndexVersionID != nil && !validID(*generation.IndexVersionID) {
		return candidateInvalid("candidate index version is invalid")
	}
	if generation.EmbeddingVersionID != nil {
		if !validID(*generation.EmbeddingVersionID) || generation.IndexVersionID == nil || *generation.EmbeddingVersionID == *generation.IndexVersionID {
			return candidateInvalid("candidate embedding version is invalid")
		}
	}
	if generation.RerankVersionID != nil {
		if !validID(*generation.RerankVersionID) || generation.IndexVersionID == nil {
			return candidateInvalid("candidate rerank version is invalid")
		}
	}
	if generation.ModelRunID != nil && !validID(*generation.ModelRunID) {
		return candidateInvalid("candidate model run reference is invalid")
	}
	hasRule := generation.RuleID != nil || generation.RuleVersion != ""
	hasModel := generation.ModelVersion != "" || generation.ModelProfileVersion != "" || generation.PromptVersion != "" || generation.SchemaVersion != ""
	if hasRule && hasModel {
		return candidateInvalid("candidate generation must use either model or rule versions")
	}
	if !hasRule && !hasModel && generation.IndexVersionID == nil {
		return candidateInvalid("candidate generation must freeze at least one participating version")
	}
	if hasRule {
		if generation.RuleID == nil || !validID(*generation.RuleID) || generation.RuleVersion == "" {
			return candidateInvalid("candidate rule generation is incomplete")
		}
	}
	if hasModel {
		for _, value := range []struct {
			name  string
			value string
		}{
			{name: "model version", value: generation.ModelVersion},
			{name: "model profile version", value: generation.ModelProfileVersion},
			{name: "prompt version", value: generation.PromptVersion},
			{name: "schema version", value: generation.SchemaVersion},
		} {
			if _, err := knowledge.NormalizeReference(value.value, true); err != nil {
				return candidateInvalid("candidate " + value.name + " is invalid")
			}
		}
	}
	return nil
}

func validateSemanticLinkCandidateDiscoveryMethods(methods []SemanticLinkDiscoveryMethod) error {
	if len(methods) == 0 || len(methods) > MaxSemanticLinkDiscoveryMethodCount {
		return candidateInvalid("candidate discovery methods are required")
	}
	seen := map[SemanticLinkDiscoveryMethod]struct{}{}
	var previous SemanticLinkDiscoveryMethod
	for index, method := range methods {
		if !validSemanticLinkDiscoveryMethod(method) {
			return candidateInvalid("candidate discovery method is unsupported")
		}
		if _, duplicate := seen[method]; duplicate || (index > 0 && method <= previous) {
			return candidateInvalid("candidate discovery methods must be unique and ordered")
		}
		seen[method] = struct{}{}
		previous = method
	}
	return nil
}

func validateSemanticLinkCandidateEvidence(workspaceID foundation.ID, evidence []SemanticLinkCandidateEvidence) error {
	if len(evidence) == 0 || len(evidence) > MaxSemanticLinkCandidateEvidence {
		return candidateInvalid("candidate evidence is required")
	}
	seenIDs, seenHashes := map[foundation.ID]struct{}{}, map[string]struct{}{}
	var previous string
	for index, item := range evidence {
		if err := validateSemanticLinkCandidateEvidenceItem(workspaceID, item); err != nil {
			return err
		}
		if _, duplicate := seenIDs[item.ID]; duplicate {
			return candidateInvalid("candidate evidence ids must be unique")
		}
		if _, duplicate := seenHashes[item.SemanticHash]; duplicate || (index > 0 && item.SemanticHash <= previous) {
			return candidateInvalid("candidate evidence hashes must be unique and ordered")
		}
		seenIDs[item.ID], seenHashes[item.SemanticHash] = struct{}{}, struct{}{}
		previous = item.SemanticHash
	}
	return nil
}

func validateSemanticLinkCandidateEvidenceItem(workspaceID foundation.ID, item SemanticLinkCandidateEvidence) error {
	if !validID(item.ID) || !validID(item.Provenance.WorkspaceID) || item.Provenance.WorkspaceID != workspaceID || item.Provenance.SourceVersionID == "" || item.Provenance.SourceSpanID == "" || knowledge.ValidateProvenanceRef(item.Provenance) != nil {
		return candidateInvalid("candidate evidence provenance is invalid")
	}
	if !canonicalSHA256(item.SemanticHash) {
		return candidateInvalid("candidate evidence hash is invalid")
	}
	reason, err := knowledge.NormalizeReason(item.Reason, true)
	if err != nil || reason != item.Reason {
		return candidateInvalid("candidate evidence reason is not canonical")
	}
	excerpt, err := knowledge.NormalizeReason(item.Excerpt, true)
	if err != nil || excerpt != item.Excerpt {
		return candidateInvalid("candidate evidence excerpt is not canonical")
	}
	return nil
}

func validateSemanticLinkCandidateReopenBinding(candidate SemanticLinkCandidate) error {
	if candidate.ReopenedFromCandidateID == nil {
		if candidate.ReopenedReason != "" {
			return candidateInvalid("reopened reason requires a reopened-from binding")
		}
		return nil
	}
	if !validID(*candidate.ReopenedFromCandidateID) || *candidate.ReopenedFromCandidateID == candidate.ID {
		return candidateInvalid("reopened candidate binding is invalid")
	}
	if candidate.ReopenedReason != SemanticLinkCandidateReopenedReasonContentChanged {
		return candidateInvalid("reopened candidate requires a content-changed reason")
	}
	return nil
}

func canonicalSemanticLinkCandidateGeneration(generation SemanticLinkCandidateGeneration) (semanticLinkCandidateGenerationPayload, error) {
	if err := validateSemanticLinkCandidateGeneration(generation); err != nil {
		return semanticLinkCandidateGenerationPayload{}, err
	}
	payload := semanticLinkCandidateGenerationPayload{}
	payload.IndexVersionID = generation.IndexVersionID
	if generation.EmbeddingVersionID != nil {
		payload.EmbeddingVersionID = generation.EmbeddingVersionID
	}
	if generation.RerankVersionID != nil {
		payload.RerankVersionID = generation.RerankVersionID
	}
	if generation.ModelRunID != nil {
		payload.ModelRunID = generation.ModelRunID
	}
	if generation.RuleID != nil {
		payload.RuleID = generation.RuleID
	}
	if generation.ModelVersion != "" {
		if modelVersion, err := knowledge.NormalizeReference(generation.ModelVersion, true); err == nil {
			payload.ModelVersion = modelVersion
		} else {
			return semanticLinkCandidateGenerationPayload{}, err
		}
	}
	if generation.ModelProfileVersion != "" {
		if profileVersion, err := knowledge.NormalizeReference(generation.ModelProfileVersion, true); err == nil {
			payload.ModelProfileVersion = profileVersion
		} else {
			return semanticLinkCandidateGenerationPayload{}, err
		}
	}
	if generation.PromptVersion != "" {
		if promptVersion, err := knowledge.NormalizeReference(generation.PromptVersion, true); err == nil {
			payload.PromptVersion = promptVersion
		} else {
			return semanticLinkCandidateGenerationPayload{}, err
		}
	}
	if generation.SchemaVersion != "" {
		if schemaVersion, err := knowledge.NormalizeReference(generation.SchemaVersion, true); err == nil {
			payload.SchemaVersion = schemaVersion
		} else {
			return semanticLinkCandidateGenerationPayload{}, err
		}
	}
	if generation.RuleVersion != "" {
		if ruleVersion, err := knowledge.NormalizeReference(generation.RuleVersion, true); err == nil {
			payload.RuleVersion = ruleVersion
		} else {
			return semanticLinkCandidateGenerationPayload{}, err
		}
	}
	return payload, nil
}

func candidateEvidenceHashes(evidence []SemanticLinkCandidateEvidence) ([]string, error) {
	if len(evidence) == 0 {
		return nil, candidateInvalid("candidate evidence is required")
	}
	hashes := make([]string, len(evidence))
	for index, item := range evidence {
		if !canonicalSHA256(item.SemanticHash) {
			return nil, candidateInvalid("candidate evidence hash is invalid")
		}
		hashes[index] = item.SemanticHash
	}
	sort.Strings(hashes)
	for index := 1; index < len(hashes); index++ {
		if hashes[index] == hashes[index-1] {
			return nil, candidateInvalid("candidate evidence hashes must be unique")
		}
	}
	return hashes, nil
}

func candidateDiscoveryMethods(methods []SemanticLinkDiscoveryMethod) ([]SemanticLinkDiscoveryMethod, error) {
	if len(methods) == 0 {
		return nil, candidateInvalid("candidate discovery methods are required")
	}
	result := append([]SemanticLinkDiscoveryMethod(nil), methods...)
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, candidateInvalid("candidate discovery methods must be unique")
		}
	}
	return result, nil
}

func candidatePageLess(left, right SemanticLinkCandidate) bool {
	if left.Status != right.Status {
		return candidateStatusRank(left.Status) < candidateStatusRank(right.Status)
	}
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		return left.UpdatedAt.After(right.UpdatedAt)
	}
	return left.ID < right.ID
}

func candidateMatchesQuery(request SemanticLinkCandidateQuery, candidate SemanticLinkCandidate) bool {
	if len(request.Statuses) > 0 && !containsCandidateStatus(request.Statuses, candidate.Status) {
		return false
	}
	if len(request.RelationTypes) > 0 && !containsRelationType(request.RelationTypes, candidate.SuggestedRelationType) {
		return false
	}
	if len(request.ReopenedReasons) > 0 && !containsCandidateReopenedReason(request.ReopenedReasons, candidate.ReopenedReason) {
		return false
	}
	if request.MinConfidence != nil && candidate.Confidence < *request.MinConfidence {
		return false
	}
	if request.NodeRef != nil && candidate.Source.Ref != *request.NodeRef && candidate.Target.Ref != *request.NodeRef {
		return false
	}
	return true
}

func validSemanticLinkNodeRef(ref knowledge.NodeRef) bool {
	return (ref.Type == knowledge.NodeTypeTopic || ref.Type == knowledge.NodeTypeClaim) && validID(ref.ID)
}

func validCandidateStatus(status SemanticLinkCandidateStatus) bool {
	switch status {
	case SemanticLinkCandidateStatusActive, SemanticLinkCandidateStatusDeferred, SemanticLinkCandidateStatusIgnored,
		SemanticLinkCandidateStatusFalsePositive, SemanticLinkCandidateStatusProposalCreated, SemanticLinkCandidateStatusSuperseded:
		return true
	default:
		return false
	}
}

func validSemanticLinkDiscoveryMethod(method SemanticLinkDiscoveryMethod) bool {
	switch method {
	case SemanticLinkDiscoveryMethodTitleAlias, SemanticLinkDiscoveryMethodTermMatch, SemanticLinkDiscoveryMethodClaimSemanticSimilarity,
		SemanticLinkDiscoveryMethodCommonTopic, SemanticLinkDiscoveryMethodSharedSource, SemanticLinkDiscoveryMethodRAGCoRetrieval:
		return true
	default:
		return false
	}
}

func validCandidateDecisionAction(action SemanticLinkCandidateDecisionAction) bool {
	switch action {
	case SemanticLinkCandidateDecisionConfirm, SemanticLinkCandidateDecisionConfirmWithRelationType,
		SemanticLinkCandidateDecisionIgnore, SemanticLinkCandidateDecisionFalsePositive,
		SemanticLinkCandidateDecisionDefer, SemanticLinkCandidateDecisionResume:
		return true
	default:
		return false
	}
}

func candidateStatusRank(status SemanticLinkCandidateStatus) int {
	switch status {
	case SemanticLinkCandidateStatusActive:
		return 0
	case SemanticLinkCandidateStatusDeferred:
		return 1
	case SemanticLinkCandidateStatusIgnored:
		return 2
	case SemanticLinkCandidateStatusFalsePositive:
		return 3
	case SemanticLinkCandidateStatusProposalCreated:
		return 4
	case SemanticLinkCandidateStatusSuperseded:
		return 5
	default:
		return 6
	}
}

func containsCandidateStatus(values []SemanticLinkCandidateStatus, want SemanticLinkCandidateStatus) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsCandidateReopenedReason(values []SemanticLinkCandidateReopenedReason, want SemanticLinkCandidateReopenedReason) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func uniqueCandidateStatuses(values []SemanticLinkCandidateStatus) bool {
	seen := map[SemanticLinkCandidateStatus]struct{}{}
	for _, value := range values {
		if !validCandidateStatus(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func uniqueReopenedReasons(values []SemanticLinkCandidateReopenedReason) bool {
	seen := map[SemanticLinkCandidateReopenedReason]struct{}{}
	for _, value := range values {
		if value != SemanticLinkCandidateReopenedReasonContentChanged {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validateSemanticLinkCandidateOptionals(candidate SemanticLinkCandidate) error {
	if candidate.ResumeAfter != nil {
		if candidate.Status != SemanticLinkCandidateStatusDeferred {
			return candidateInvalid("resume time is only allowed for deferred candidates")
		}
		if candidate.ResumeAfter.IsZero() {
			return candidateInvalid("deferred candidate resume time is invalid")
		}
	}
	return nil
}

func candidateInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkCandidateInvalid, false, errors.New(message))
}

func candidateDecisionInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkCandidateDecisionInvalid, false, errors.New(message))
}

func candidateRequestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkCandidateRequestInvalid, false, errors.New(message))
}

func candidateInconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkCandidateResultInvalid, false, errors.New(message))
}

func versionConflictCandidate(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeSemanticLinkCandidateTransitionInvalid, false, errors.New(message))
}

type semanticLinkCandidateEndpointPayload struct {
	Ref     knowledge.NodeRef `json:"ref"`
	Version int64             `json:"version"`
}

type semanticLinkCandidateGenerationPayload struct {
	IndexVersionID      *foundation.ID `json:"index_version_id,omitempty"`
	EmbeddingVersionID  *foundation.ID `json:"embedding_version_id,omitempty"`
	RerankVersionID     *foundation.ID `json:"rerank_version_id,omitempty"`
	ModelVersion        string         `json:"model_version,omitempty"`
	ModelProfileVersion string         `json:"model_profile_version,omitempty"`
	PromptVersion       string         `json:"prompt_version,omitempty"`
	SchemaVersion       string         `json:"schema_version,omitempty"`
	RuleID              *foundation.ID `json:"rule_id,omitempty"`
	RuleVersion         string         `json:"rule_version,omitempty"`
	ModelRunID          *foundation.ID `json:"model_run_id,omitempty"`
}
