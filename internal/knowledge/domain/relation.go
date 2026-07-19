package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// NodeType 是 Relation 端点当前注册的知识对象类型。
type NodeType string

const (
	NodeTypeTopic NodeType = "TOPIC"
	NodeTypeClaim NodeType = "CLAIM"
)

// NodeRef 以类型和稳定 ID 引用一个 Relation 端点。
type NodeRef struct {
	Type NodeType
	ID   foundation.ID
}

// NodeLifecycle 是 Relation 端点校验所需的最小生命周期投影。
type NodeLifecycle string

const (
	NodeLifecycleActive     NodeLifecycle = "ACTIVE"
	NodeLifecycleSuggested  NodeLifecycle = "SUGGESTED"
	NodeLifecycleConfirmed  NodeLifecycle = "CONFIRMED"
	NodeLifecycleDisputed   NodeLifecycle = "DISPUTED"
	NodeLifecycleMerged     NodeLifecycle = "MERGED"
	NodeLifecycleDeprecated NodeLifecycle = "DEPRECATED"
	NodeLifecycleSuperseded NodeLifecycle = "SUPERSEDED"
	NodeLifecycleInvalid    NodeLifecycle = "INVALID"
)

// NodeDescriptor 将端点引用绑定到 Workspace 和当前生命周期。
type NodeDescriptor struct {
	Ref         NodeRef
	WorkspaceID foundation.ID
	Lifecycle   NodeLifecycle
}

// RelationType 是正式持久化知识关系的受控类型。
type RelationType string

const (
	RelationCites          RelationType = "CITES"
	RelationDerivedFrom    RelationType = "DERIVED_FROM"
	RelationBelongsTo      RelationType = "BELONGS_TO"
	RelationSupports       RelationType = "SUPPORTS"
	RelationComplements    RelationType = "COMPLEMENTS"
	RelationDuplicates     RelationType = "DUPLICATES"
	RelationConflictsWith  RelationType = "CONFLICTS_WITH"
	RelationPrerequisiteOf RelationType = "PREREQUISITE_OF"
	RelationVersionOf      RelationType = "VERSION_OF"
	RelationImpacts        RelationType = "IMPACTS"
)

// RelationStatus 是 Relation 的受控建议、确认与历史状态。
type RelationStatus string

const (
	RelationStatusSuggested  RelationStatus = "SUGGESTED"
	RelationStatusConfirmed  RelationStatus = "CONFIRMED"
	RelationStatusRejected   RelationStatus = "REJECTED"
	RelationStatusStale      RelationStatus = "STALE"
	RelationStatusDeprecated RelationStatus = "DEPRECATED"
)

// Relation 是 Topic 或 Claim 之间唯一的正式关系事实。
type Relation struct {
	ID, WorkspaceID      foundation.ID
	Source, Target       NodeRef
	Type                 RelationType
	Status               RelationStatus
	Confirmation         *Confirmation
	ConfidenceScore      *float64
	ValidFrom, ValidTo   *time.Time
	Fingerprint          string
	EvidenceFingerprint  string
	Version              int64
	CreatedAt, UpdatedAt time.Time
}

// RelationEvidence 是 Relation 独有且不可变的 Provenance 证据实体。
type RelationEvidence struct {
	ID, WorkspaceID, RelationID foundation.ID
	Provenance                  ProvenanceRef
	Reason                      string
	Applicability               Applicability
	EvidenceHash                string
	ModelRunRef                 *string
	Confirmation                *Confirmation
	CreatedAt                   time.Time
}

// RelationTypeCompatible 判断关系类型是否允许给定的端点类型组合。
func RelationTypeCompatible(relationType RelationType, source, target NodeType) bool {
	registered := func(value NodeType) bool { return value == NodeTypeTopic || value == NodeTypeClaim }
	if !registered(source) || !registered(target) {
		return false
	}
	switch relationType {
	case RelationCites, RelationDerivedFrom, RelationSupports, RelationConflictsWith:
		return source == NodeTypeClaim && target == NodeTypeClaim
	case RelationBelongsTo:
		return source == NodeTypeClaim && target == NodeTypeTopic
	case RelationComplements, RelationDuplicates, RelationPrerequisiteOf, RelationVersionOf:
		return source == target
	case RelationImpacts:
		return true
	default:
		return false
	}
}

// IsSymmetricRelationType 判断关系是否必须按端点稳定排序去重。
func IsSymmetricRelationType(relationType RelationType) bool {
	return relationType == RelationDuplicates || relationType == RelationConflictsWith
}

// CanonicalizeRelationEndpoints 校验端点并规范化对称关系的稳定方向。
func CanonicalizeRelationEndpoints(relationType RelationType, source, target NodeRef) (NodeRef, NodeRef, error) {
	if !validNodeRef(source) || !validNodeRef(target) || source == target {
		return NodeRef{}, NodeRef{}, invalid(ErrorCodeRelationInvalid, "relation endpoints are invalid or self-referential")
	}
	if !RelationTypeCompatible(relationType, source.Type, target.Type) {
		return NodeRef{}, NodeRef{}, invalid(ErrorCodeRelationInvalid, "relation type is incompatible with endpoint types")
	}
	if IsSymmetricRelationType(relationType) && nodeRefKey(target) < nodeRefKey(source) {
		return target, source, nil
	}
	return source, target, nil
}

// ValidateRelationEndpoints 校验端点同 Workspace、生命周期有效且类型兼容。
func ValidateRelationEndpoints(workspaceID foundation.ID, relationType RelationType, source, target NodeDescriptor) (NodeRef, NodeRef, error) {
	if !validID(workspaceID) || source.WorkspaceID != workspaceID || target.WorkspaceID != workspaceID ||
		!validNodeLifecycle(source.Ref.Type, source.Lifecycle) || !validNodeLifecycle(target.Ref.Type, target.Lifecycle) {
		return NodeRef{}, NodeRef{}, inconsistent(ErrorCodeRelationInvalid, "relation endpoints are missing, retired, or cross-workspace")
	}
	return CanonicalizeRelationEndpoints(relationType, source.Ref, target.Ref)
}

// ValidateRelation 校验 Relation 的规范端点、指纹、状态、确认和版本字段。
func ValidateRelation(relation Relation) error {
	if !validID(relation.ID) || !validID(relation.WorkspaceID) || relation.ID == relation.WorkspaceID {
		return invalid(ErrorCodeRelationInvalid, "relation identity is invalid")
	}
	source, target, err := CanonicalizeRelationEndpoints(relation.Type, relation.Source, relation.Target)
	if err != nil || source != relation.Source || target != relation.Target {
		return invalid(ErrorCodeRelationInvalid, "relation endpoints are not canonical")
	}
	if relation.Fingerprint != ComputeRelationFingerprint(relation.WorkspaceID, relation.Type, relation.Source, relation.Target) ||
		!validRelationStatus(relation.Status) || relation.Version <= 0 || !validLifecycleTimes(relation.CreatedAt, relation.UpdatedAt) {
		return inconsistent(ErrorCodeRelationInvalid, "relation fingerprint or lifecycle is inconsistent")
	}
	if relation.ConfidenceScore != nil && (math.IsNaN(*relation.ConfidenceScore) || math.IsInf(*relation.ConfidenceScore, 0) || *relation.ConfidenceScore < 0 || *relation.ConfidenceScore > 1) {
		return invalid(ErrorCodeRelationInvalid, "relation confidence score is invalid")
	}
	if relation.ValidTo != nil && (relation.ValidFrom == nil || !relation.ValidTo.After(*relation.ValidFrom)) {
		return invalid(ErrorCodeRelationInvalid, "relation validity interval is invalid")
	}
	requiresConfirmation := relation.Status == RelationStatusConfirmed || relation.Status == RelationStatusStale || relation.Status == RelationStatusDeprecated
	allowsHistoricalConfirmation := requiresConfirmation || relation.Status == RelationStatusRejected
	if requiresConfirmation {
		if relation.Confirmation == nil || ValidateConfirmation(*relation.Confirmation) != nil {
			return inconsistent(ErrorCodeRelationInvalid, "confirmed relation history requires confirmation")
		}
	} else if relation.Confirmation != nil && !allowsHistoricalConfirmation {
		return invalid(ErrorCodeRelationInvalid, "unconfirmed relation cannot carry confirmation")
	} else if relation.Confirmation != nil && ValidateConfirmation(*relation.Confirmation) != nil {
		return invalid(ErrorCodeRelationInvalid, "historical relation confirmation is invalid")
	}
	return nil
}

// ValidateRelationAggregate 校验 Relation 与完整 Evidence 集合及确认信息的一致性。
func ValidateRelationAggregate(relation Relation, evidence []RelationEvidence) error {
	if err := ValidateRelation(relation); err != nil {
		return err
	}
	matchedConfirmation := false
	for _, item := range evidence {
		if item.WorkspaceID != relation.WorkspaceID || item.RelationID != relation.ID {
			return inconsistent(ErrorCodeRelationEvidenceInvalid, "relation evidence owner binding is inconsistent")
		}
		if err := ValidateRelationEvidence(item); err != nil {
			return err
		}
		matchedConfirmation = matchedConfirmation || (relation.Confirmation != nil && item.Confirmation != nil && *relation.Confirmation == *item.Confirmation)
	}
	wantFingerprint := ComputeRelationEvidenceFingerprint(evidence)
	if relation.EvidenceFingerprint != wantFingerprint {
		return inconsistent(ErrorCodeRelationInvalid, "relation evidence fingerprint is inconsistent")
	}
	if relation.Status == RelationStatusConfirmed && (len(evidence) == 0 || !matchedConfirmation) {
		return inconsistent(ErrorCodeRelationInvalid, "confirmed relation requires evidence with matching confirmation")
	}
	return nil
}

// ValidateRelationEvidence 校验证据的 owner、Provenance、Applicability 和内容哈希。
func ValidateRelationEvidence(evidence RelationEvidence) error {
	if !validID(evidence.ID) || !validID(evidence.WorkspaceID) || !validID(evidence.RelationID) || evidence.ID == evidence.RelationID {
		return invalid(ErrorCodeRelationEvidenceInvalid, "relation evidence identity is invalid")
	}
	if err := ValidateProvenanceRef(evidence.Provenance); err != nil || evidence.Provenance.WorkspaceID != evidence.WorkspaceID {
		return inconsistent(ErrorCodeRelationEvidenceInvalid, "relation evidence provenance workspace is inconsistent")
	}
	reason, err := NormalizeReason(evidence.Reason, true)
	if err != nil || reason != evidence.Reason || evidence.CreatedAt.IsZero() || ValidateApplicability(evidence.Applicability) != nil || validateOptionalReference(evidence.ModelRunRef) != nil {
		return invalid(ErrorCodeRelationEvidenceInvalid, "relation evidence payload is invalid")
	}
	if evidence.Confirmation != nil && ValidateConfirmation(*evidence.Confirmation) != nil {
		return invalid(ErrorCodeRelationEvidenceInvalid, "relation evidence confirmation is invalid")
	}
	if !validSHA256(evidence.EvidenceHash) || evidence.EvidenceHash != ComputeRelationEvidenceHash(evidence) {
		return inconsistent(ErrorCodeRelationEvidenceInvalid, "relation evidence hash is inconsistent")
	}
	return nil
}

// ValidateRelationTransition 校验 Relation 是否遵循冻结的状态迁移矩阵。
func ValidateRelationTransition(from, to RelationStatus) error {
	allowed := map[RelationStatus]map[RelationStatus]struct{}{
		RelationStatusSuggested: {RelationStatusConfirmed: {}, RelationStatusRejected: {}},
		RelationStatusConfirmed: {RelationStatusStale: {}, RelationStatusDeprecated: {}},
		RelationStatusStale:     {RelationStatusConfirmed: {}, RelationStatusRejected: {}, RelationStatusDeprecated: {}},
		RelationStatusRejected:  {RelationStatusSuggested: {}},
	}
	if _, ok := allowed[from][to]; ok {
		return nil
	}
	return versionConflict(ErrorCodeRelationTransitionInvalid, "relation status transition is not allowed")
}

// ValidateRelationTransitionCommand 额外保证被拒绝关系只能凭新 Evidence 集合重新建议。
func ValidateRelationTransitionCommand(current Relation, to RelationStatus, evidenceFingerprint string) error {
	if err := ValidateRelationTransition(current.Status, to); err != nil {
		return err
	}
	if current.Status == RelationStatusRejected && to == RelationStatusSuggested && (!validSHA256(evidenceFingerprint) || evidenceFingerprint == current.EvidenceFingerprint) {
		return versionConflict(ErrorCodeRelationTransitionInvalid, "rejected relation requires a new evidence fingerprint")
	}
	return nil
}

// ComputeRelationFingerprint 计算绑定 Workspace、类型和规范端点的稳定指纹。
func ComputeRelationFingerprint(workspaceID foundation.ID, relationType RelationType, source, target NodeRef) string {
	canonicalSource, canonicalTarget, err := CanonicalizeRelationEndpoints(relationType, source, target)
	if err != nil {
		return ""
	}
	payload := strings.Join([]string{"knowledge-relation/v1", string(workspaceID), string(relationType), nodeRefKey(canonicalSource), nodeRefKey(canonicalTarget)}, "\n")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// ComputeRelationEvidenceHash 计算只属于 Relation Evidence 语义的 canonical SHA-256。
func ComputeRelationEvidenceHash(evidence RelationEvidence) string {
	payload := struct {
		SchemaVersion string `json:"schema_version"`
		RelationID    string `json:"relation_id"`
		SemanticHash  string `json:"semantic_hash"`
	}{"knowledge-relation-evidence/v1", string(evidence.RelationID), ComputeRelationEvidenceSemanticHash(evidence)}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// ComputeRelationEvidenceSemanticHash 计算不含 owner ID、记录 ID 和时间的 Evidence 业务语义哈希。
func ComputeRelationEvidenceSemanticHash(evidence RelationEvidence) string {
	method, reference := "", ""
	if evidence.Confirmation != nil {
		method, reference = string(evidence.Confirmation.Method), evidence.Confirmation.Reference
	}
	payload := struct {
		SchemaVersion         string `json:"schema_version"`
		WorkspaceID           string `json:"workspace_id"`
		SourceVersionID       string `json:"source_version_id"`
		SourceSpanID          string `json:"source_span_id"`
		Reason                string `json:"reason"`
		ApplicabilityHash     string `json:"applicability_hash"`
		ModelRunRef           string `json:"model_run_ref"`
		ConfirmationMethod    string `json:"confirmation_method"`
		ConfirmationReference string `json:"confirmation_reference"`
	}{"knowledge-relation-evidence-semantic/v1", string(evidence.WorkspaceID), string(evidence.Provenance.SourceVersionID), string(evidence.Provenance.SourceSpanID), evidence.Reason, evidence.Applicability.Hash, optionalString(evidence.ModelRunRef), method, reference}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// ComputeRelationEvidenceFingerprint 计算与 Evidence 顺序无关、保留多重性的集合指纹。
func ComputeRelationEvidenceFingerprint(evidence []RelationEvidence) string {
	if len(evidence) == 0 {
		return ""
	}
	hashes := make([]string, len(evidence))
	for index := range evidence {
		hashes[index] = evidence[index].EvidenceHash
	}
	sort.Strings(hashes)
	digest := sha256.Sum256([]byte("knowledge-relation-evidence-set/v1\n" + strings.Join(hashes, "\n")))
	return hex.EncodeToString(digest[:])
}

// RelationAssessment 是模型或人工分析产生的五分类结果，不是正式 RelationType。
type RelationAssessment string

const (
	AssessmentNew           RelationAssessment = "NEW"
	AssessmentComplementary RelationAssessment = "COMPLEMENTARY"
	AssessmentDuplicate     RelationAssessment = "DUPLICATE"
	AssessmentConflict      RelationAssessment = "CONFLICT"
	AssessmentLowConfidence RelationAssessment = "LOW_CONFIDENCE"
)

// AssessmentDecision 是 Relation Assessment 映射出的下一步领域动作。
type AssessmentDecision string

const (
	AssessmentDecisionProposeNewClaim    AssessmentDecision = "PROPOSE_NEW_CLAIM"
	AssessmentDecisionSuggestRelation    AssessmentDecision = "SUGGEST_RELATION"
	AssessmentDecisionOpenConflict       AssessmentDecision = "OPEN_CONFLICT"
	AssessmentDecisionRequireHumanReview AssessmentDecision = "REQUIRE_HUMAN_REVIEW"
)

// AssessmentAction 描述纯映射后的候选动作，不执行任何持久化写入。
type AssessmentAction struct {
	Decision       AssessmentDecision
	RelationType   *RelationType
	Source, Target NodeRef
	OpenConflict   bool
}

// MapAssessment 将五分类结果确定性映射为新主张、候选关系、Conflict 或人工处理动作。
func MapAssessment(assessment RelationAssessment, source, target NodeRef, suggestConflictRelation bool) (AssessmentAction, error) {
	switch assessment {
	case AssessmentNew:
		return AssessmentAction{Decision: AssessmentDecisionProposeNewClaim}, nil
	case AssessmentLowConfidence:
		return AssessmentAction{Decision: AssessmentDecisionRequireHumanReview}, nil
	case AssessmentComplementary, AssessmentDuplicate:
		relationType := RelationComplements
		if assessment == AssessmentDuplicate {
			relationType = RelationDuplicates
		}
		canonicalSource, canonicalTarget, err := CanonicalizeRelationEndpoints(relationType, source, target)
		if err != nil {
			return AssessmentAction{}, invalid(ErrorCodeRelationAssessmentInvalid, "assessment endpoints are invalid")
		}
		return AssessmentAction{Decision: AssessmentDecisionSuggestRelation, RelationType: &relationType, Source: canonicalSource, Target: canonicalTarget}, nil
	case AssessmentConflict:
		canonicalSource, canonicalTarget, err := CanonicalizeRelationEndpoints(RelationConflictsWith, source, target)
		if err != nil {
			return AssessmentAction{}, invalid(ErrorCodeRelationAssessmentInvalid, "conflict assessment requires two distinct claims")
		}
		action := AssessmentAction{Decision: AssessmentDecisionOpenConflict, Source: canonicalSource, Target: canonicalTarget, OpenConflict: true}
		if suggestConflictRelation {
			relationType := RelationConflictsWith
			action.RelationType = &relationType
		}
		return action, nil
	default:
		return AssessmentAction{}, invalid(ErrorCodeRelationAssessmentInvalid, "relation assessment is unsupported")
	}
}

func validNodeRef(ref NodeRef) bool {
	return (ref.Type == NodeTypeTopic || ref.Type == NodeTypeClaim) && validID(ref.ID)
}

func nodeRefKey(ref NodeRef) string { return string(ref.Type) + "\x00" + string(ref.ID) }

func validNodeLifecycle(nodeType NodeType, lifecycle NodeLifecycle) bool {
	if nodeType == NodeTypeTopic {
		return lifecycle == NodeLifecycleActive
	}
	if nodeType == NodeTypeClaim {
		return lifecycle == NodeLifecycleSuggested || lifecycle == NodeLifecycleConfirmed || lifecycle == NodeLifecycleDisputed
	}
	return false
}

func validRelationStatus(status RelationStatus) bool {
	switch status {
	case RelationStatusSuggested, RelationStatusConfirmed, RelationStatusRejected, RelationStatusStale, RelationStatusDeprecated:
		return true
	default:
		return false
	}
}
