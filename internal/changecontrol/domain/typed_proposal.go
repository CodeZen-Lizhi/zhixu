package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// ProposalType 是 Change Control Proposal 的判别类型。
type ProposalType string

const (
	ProposalTypeFilePatch       ProposalType = "file_patch"
	ProposalTypeKnowledgeChange ProposalType = "knowledge_change"
	// ProposalTypePublishArtifact freezes an approved Artifact revision for a
	// future formal-knowledge publication capability.
	ProposalTypePublishArtifact ProposalType = "publish_artifact"
)

const (
	// KnowledgeChangeSchemaVersion 是首版结构化知识关系变更契约版本。
	KnowledgeChangeSchemaVersion = "knowledge-relation-change/v1"
	// PublishArtifactSchemaVersion is the frozen Artifact publication contract.
	PublishArtifactSchemaVersion = "artifact-publication/v1"
)

// ArtifactCoverageStatus is the verified coverage conclusion for one frozen
// Artifact section. It deliberately mirrors the Artifact wire contract without
// making Change Control depend on Artifact domain internals.
type ArtifactCoverageStatus string

const (
	ArtifactCoverageCovered   ArtifactCoverageStatus = "COVERED"
	ArtifactCoveragePartial   ArtifactCoverageStatus = "PARTIAL"
	ArtifactCoverageStatusGap ArtifactCoverageStatus = "GAP"
)

// ArtifactCoverageGap records a concrete verified-knowledge gap.
type ArtifactCoverageGap struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// ArtifactSourceCoverage freezes verified source coverage by section.
type ArtifactSourceCoverage struct {
	SectionKey string                 `json:"section_key"`
	Status     ArtifactCoverageStatus `json:"status"`
	Gaps       []ArtifactCoverageGap  `json:"gaps"`
}

// PublishArtifact is the typed immutable payload for a publish_artifact
// Proposal. It contains no target file or formal Document identity.
type PublishArtifact struct {
	WorkspaceID     foundation.ID            `json:"workspace_id"`
	ArtifactID      foundation.ID            `json:"artifact_id"`
	RevisionID      foundation.ID            `json:"revision_id"`
	RevisionNo      int64                    `json:"revision_no"`
	ArtifactVersion int64                    `json:"artifact_version"`
	ContentHash     string                   `json:"content_hash"`
	SourceCoverage  []ArtifactSourceCoverage `json:"source_coverage"`
	SchemaVersion   string                   `json:"schema_version"`
}

// KnowledgeTargetRefType 是结构化知识变更的目标引用类型。
type KnowledgeTargetRefType string

const (
	// KnowledgeTargetRefRelationCandidate 表示本次变更绑定到一个候选 Relation Candidate。
	KnowledgeTargetRefRelationCandidate KnowledgeTargetRefType = "RELATION_CANDIDATE"
)

// KnowledgeChangeOperation 是结构化知识变更的受控操作。
type KnowledgeChangeOperation string

const (
	// KnowledgeChangeOperationCreateRelation 表示创建或确认一条正式 Relation。
	KnowledgeChangeOperationCreateRelation KnowledgeChangeOperation = "CREATE_RELATION"
)

// KnowledgeTargetRef 绑定本次知识变更的稳定目标引用。
type KnowledgeTargetRef struct {
	Type        KnowledgeTargetRefType `json:"type"`
	ID          foundation.ID          `json:"id"`
	Fingerprint string                 `json:"fingerprint"`
}

// KnowledgeBaseVersion 绑定知识变更所依赖的端点版本。
type KnowledgeBaseVersion struct {
	NodeType knowledge.NodeType `json:"node_type"`
	NodeID   foundation.ID      `json:"node_id"`
	Version  int64              `json:"version"`
}

// KnowledgeChangeSet 描述一次结构化知识关系变更。
type KnowledgeChangeSet struct {
	Operation    KnowledgeChangeOperation `json:"operation"`
	Source       knowledge.NodeRef        `json:"source"`
	Target       knowledge.NodeRef        `json:"target"`
	RelationType knowledge.RelationType   `json:"relation_type"`
}

// KnowledgeEvidenceRef 绑定候选证据与其稳定语义哈希。
type KnowledgeEvidenceRef struct {
	CandidateEvidenceID foundation.ID `json:"candidate_evidence_id"`
	SemanticHash        string        `json:"semantic_hash"`
}

// KnowledgeChange 是 `knowledge_change` Proposal Revision 的结构化正文。
type KnowledgeChange struct {
	TargetRefs    []KnowledgeTargetRef   `json:"target_refs"`
	BaseVersions  []KnowledgeBaseVersion `json:"base_versions"`
	ChangeSet     KnowledgeChangeSet     `json:"change_set"`
	EvidenceRefs  []KnowledgeEvidenceRef `json:"evidence_refs"`
	SchemaVersion string                 `json:"schema_version"`
}

var (
	// ErrKnowledgeChangeInvalid 表示知识变更结构化载荷不满足冻结契约。
	ErrKnowledgeChangeInvalid = errors.New("knowledge change payload is invalid")
	// ErrProposalTypeInvalid 表示 Proposal Type 与 Revision 内容不匹配。
	ErrProposalTypeInvalid = errors.New("proposal type does not match revision payload")
	// ErrPublishArtifactInvalid means the frozen Artifact publication payload is invalid.
	ErrPublishArtifactInvalid = errors.New("publish artifact payload is invalid")
)

// NormalizeProposalType 返回去空白、默认 file_patch 的 ProposalType。
func NormalizeProposalType(value ProposalType) ProposalType {
	trimmed := ProposalType(strings.ToLower(strings.TrimSpace(string(value))))
	if trimmed == "" {
		return ProposalTypeFilePatch
	}
	return trimmed
}

// ValidateKnowledgeChange 规范化并校验 `knowledge_change` 载荷。
func ValidateKnowledgeChange(change KnowledgeChange) (KnowledgeChange, error) {
	normalized := change
	normalized.SchemaVersion = strings.TrimSpace(normalized.SchemaVersion)
	if normalized.SchemaVersion != KnowledgeChangeSchemaVersion {
		return KnowledgeChange{}, ErrKnowledgeChangeInvalid
	}
	if len(normalized.TargetRefs) == 0 {
		return KnowledgeChange{}, ErrKnowledgeChangeInvalid
	}
	normalizedTargetRefs := make([]KnowledgeTargetRef, len(normalized.TargetRefs))
	for index, ref := range normalized.TargetRefs {
		ref.Type = KnowledgeTargetRefType(strings.ToUpper(strings.TrimSpace(string(ref.Type))))
		parsedID, err := foundation.ParseID(string(ref.ID))
		if err != nil || ref.Type != KnowledgeTargetRefRelationCandidate || !ValidHash(ref.Fingerprint) {
			return KnowledgeChange{}, ErrKnowledgeChangeInvalid
		}
		ref.ID = parsedID
		ref.Fingerprint = strings.ToLower(strings.TrimSpace(ref.Fingerprint))
		normalizedTargetRefs[index] = ref
	}
	sort.Slice(normalizedTargetRefs, func(i, j int) bool {
		return knowledgeTargetRefKey(normalizedTargetRefs[i]) < knowledgeTargetRefKey(normalizedTargetRefs[j])
	})
	for index := 1; index < len(normalizedTargetRefs); index++ {
		if knowledgeTargetRefKey(normalizedTargetRefs[index-1]) == knowledgeTargetRefKey(normalizedTargetRefs[index]) {
			return KnowledgeChange{}, ErrKnowledgeChangeInvalid
		}
	}
	normalized.TargetRefs = normalizedTargetRefs

	canonicalSource, canonicalTarget, err := knowledge.CanonicalizeRelationEndpoints(
		normalized.ChangeSet.RelationType,
		normalized.ChangeSet.Source,
		normalized.ChangeSet.Target,
	)
	if err != nil || normalized.ChangeSet.Operation != KnowledgeChangeOperationCreateRelation {
		return KnowledgeChange{}, ErrKnowledgeChangeInvalid
	}
	normalized.ChangeSet.Source = canonicalSource
	normalized.ChangeSet.Target = canonicalTarget

	if len(normalized.BaseVersions) != 2 {
		return KnowledgeChange{}, ErrKnowledgeChangeInvalid
	}
	normalizedBaseVersions := make([]KnowledgeBaseVersion, len(normalized.BaseVersions))
	for index, item := range normalized.BaseVersions {
		item.NodeType = knowledge.NodeType(strings.ToUpper(strings.TrimSpace(string(item.NodeType))))
		parsedID, err := foundation.ParseID(string(item.NodeID))
		if err != nil || item.Version <= 0 || (item.NodeType != knowledge.NodeTypeTopic && item.NodeType != knowledge.NodeTypeClaim) {
			return KnowledgeChange{}, ErrKnowledgeChangeInvalid
		}
		item.NodeID = parsedID
		normalizedBaseVersions[index] = item
	}
	sort.Slice(normalizedBaseVersions, func(i, j int) bool {
		return knowledgeBaseVersionKey(normalizedBaseVersions[i]) < knowledgeBaseVersionKey(normalizedBaseVersions[j])
	})
	if knowledgeBaseVersionKey(normalizedBaseVersions[0]) == knowledgeBaseVersionKey(normalizedBaseVersions[1]) {
		return KnowledgeChange{}, ErrKnowledgeChangeInvalid
	}
	expectedVersions := map[string]struct{}{
		knowledgeNodeRefKey(normalized.ChangeSet.Source): {},
		knowledgeNodeRefKey(normalized.ChangeSet.Target): {},
	}
	for _, item := range normalizedBaseVersions {
		if _, ok := expectedVersions[knowledgeBaseVersionNodeKey(item)]; !ok {
			return KnowledgeChange{}, ErrKnowledgeChangeInvalid
		}
	}
	normalized.BaseVersions = normalizedBaseVersions

	normalizedEvidenceRefs := make([]KnowledgeEvidenceRef, len(normalized.EvidenceRefs))
	if len(normalizedEvidenceRefs) == 0 {
		return KnowledgeChange{}, ErrKnowledgeChangeInvalid
	}
	for index, item := range normalized.EvidenceRefs {
		parsedID, err := foundation.ParseID(string(item.CandidateEvidenceID))
		if err != nil || !ValidHash(item.SemanticHash) {
			return KnowledgeChange{}, ErrKnowledgeChangeInvalid
		}
		item.CandidateEvidenceID = parsedID
		item.SemanticHash = strings.ToLower(strings.TrimSpace(item.SemanticHash))
		normalizedEvidenceRefs[index] = item
	}
	sort.Slice(normalizedEvidenceRefs, func(i, j int) bool {
		return knowledgeEvidenceRefKey(normalizedEvidenceRefs[i]) < knowledgeEvidenceRefKey(normalizedEvidenceRefs[j])
	})
	for index := 1; index < len(normalizedEvidenceRefs); index++ {
		if knowledgeEvidenceRefKey(normalizedEvidenceRefs[index-1]) == knowledgeEvidenceRefKey(normalizedEvidenceRefs[index]) {
			return KnowledgeChange{}, ErrKnowledgeChangeInvalid
		}
	}
	normalized.EvidenceRefs = normalizedEvidenceRefs
	return normalized, nil
}

// ValidatePublishArtifact canonicalizes and validates a frozen Artifact publication payload.
func ValidatePublishArtifact(publication PublishArtifact) (PublishArtifact, error) {
	publication.SchemaVersion = strings.TrimSpace(publication.SchemaVersion)
	if publication.SchemaVersion != PublishArtifactSchemaVersion || publication.RevisionNo < 1 || publication.ArtifactVersion < 1 || !ValidHash(publication.ContentHash) {
		return PublishArtifact{}, ErrPublishArtifactInvalid
	}
	workspaceID, workspaceErr := foundation.ParseID(string(publication.WorkspaceID))
	artifactID, artifactErr := foundation.ParseID(string(publication.ArtifactID))
	revisionID, revisionErr := foundation.ParseID(string(publication.RevisionID))
	if workspaceErr != nil || artifactErr != nil || revisionErr != nil || workspaceID == artifactID || workspaceID == revisionID || artifactID == revisionID {
		return PublishArtifact{}, ErrPublishArtifactInvalid
	}
	publication.WorkspaceID, publication.ArtifactID, publication.RevisionID = workspaceID, artifactID, revisionID
	publication.ContentHash = strings.ToLower(strings.TrimSpace(publication.ContentHash))
	if len(publication.SourceCoverage) == 0 {
		return PublishArtifact{}, ErrPublishArtifactInvalid
	}
	coverage := make([]ArtifactSourceCoverage, len(publication.SourceCoverage))
	for index, item := range publication.SourceCoverage {
		item.SectionKey = strings.TrimSpace(item.SectionKey)
		if item.SectionKey == "" || len(item.SectionKey) > 128 {
			return PublishArtifact{}, ErrPublishArtifactInvalid
		}
		item.Status = ArtifactCoverageStatus(strings.ToUpper(strings.TrimSpace(string(item.Status))))
		switch item.Status {
		case ArtifactCoverageCovered:
			if len(item.Gaps) != 0 {
				return PublishArtifact{}, ErrPublishArtifactInvalid
			}
		case ArtifactCoveragePartial:
			if len(item.Gaps) == 0 {
				return PublishArtifact{}, ErrPublishArtifactInvalid
			}
		case ArtifactCoverageStatusGap:
			if len(item.Gaps) == 0 {
				return PublishArtifact{}, ErrPublishArtifactInvalid
			}
		default:
			return PublishArtifact{}, ErrPublishArtifactInvalid
		}
		gaps := make([]ArtifactCoverageGap, len(item.Gaps))
		for gapIndex, gap := range item.Gaps {
			gap.Code = strings.TrimSpace(gap.Code)
			gap.Description = strings.TrimSpace(gap.Description)
			if gap.Code == "" || len(gap.Code) > 128 || gap.Description == "" || len(gap.Description) > 4096 {
				return PublishArtifact{}, ErrPublishArtifactInvalid
			}
			gaps[gapIndex] = gap
		}
		sort.Slice(gaps, func(i, j int) bool {
			if gaps[i].Code == gaps[j].Code {
				return gaps[i].Description < gaps[j].Description
			}
			return gaps[i].Code < gaps[j].Code
		})
		for gapIndex := 1; gapIndex < len(gaps); gapIndex++ {
			if gaps[gapIndex-1] == gaps[gapIndex] {
				return PublishArtifact{}, ErrPublishArtifactInvalid
			}
		}
		item.Gaps = gaps
		coverage[index] = item
	}
	sort.Slice(coverage, func(i, j int) bool { return coverage[i].SectionKey < coverage[j].SectionKey })
	for index := 1; index < len(coverage); index++ {
		if coverage[index-1].SectionKey == coverage[index].SectionKey {
			return PublishArtifact{}, ErrPublishArtifactInvalid
		}
	}
	publication.SourceCoverage = coverage
	return publication, nil
}

// ComputePublishArtifactHash computes the canonical typed change hash.
func ComputePublishArtifactHash(publication PublishArtifact, risk, rollbackPlan string) (string, error) {
	canonical, err := ValidatePublishArtifact(publication)
	if err != nil {
		return "", err
	}
	payload := struct {
		ProposalType ProposalType    `json:"proposal_type"`
		Publication  PublishArtifact `json:"publication"`
		Risk         string          `json:"risk"`
		RollbackPlan string          `json:"rollback_plan"`
	}{ProposalType: ProposalTypePublishArtifact, Publication: canonical, Risk: strings.TrimSpace(risk), RollbackPlan: strings.TrimSpace(rollbackPlan)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// ComputePublishArtifactRequestHash binds workspace, frozen publication payload and risk to idempotency.
func ComputePublishArtifactRequestHash(workspaceID foundation.ID, publication PublishArtifact, riskLevel ProposalRiskLevel, risk, rollbackPlan string) (string, error) {
	canonical, err := ValidatePublishArtifact(publication)
	if err != nil || canonical.WorkspaceID != workspaceID {
		return "", ErrPublishArtifactInvalid
	}
	normalizedRiskLevel, err := ParseProposalRiskLevel(riskLevel)
	if err != nil {
		return "", err
	}
	payload := struct {
		RequestSchema string            `json:"request_schema"`
		WorkspaceID   foundation.ID     `json:"workspace_id"`
		ProposalType  ProposalType      `json:"proposal_type"`
		Publication   PublishArtifact   `json:"publication"`
		RiskLevel     ProposalRiskLevel `json:"risk_level"`
		Risk          string            `json:"risk"`
		RollbackPlan  string            `json:"rollback_plan"`
	}{RequestSchema: "publish-artifact-proposal-request/v1", WorkspaceID: workspaceID, ProposalType: ProposalTypePublishArtifact, Publication: canonical, RiskLevel: normalizedRiskLevel, Risk: strings.TrimSpace(risk), RollbackPlan: strings.TrimSpace(rollbackPlan)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// ComputeKnowledgeChangeHash 计算 `knowledge_change` 的 canonical typed change hash。
func ComputeKnowledgeChangeHash(change KnowledgeChange, risk, rollbackPlan string) (string, error) {
	canonicalChange, err := ValidateKnowledgeChange(change)
	if err != nil {
		return "", err
	}
	payload := struct {
		ProposalType  ProposalType    `json:"proposal_type"`
		Change        KnowledgeChange `json:"change"`
		Risk          string          `json:"risk"`
		RollbackPlan  string          `json:"rollback_plan"`
		SchemaVersion string          `json:"schema_version"`
	}{
		ProposalType:  ProposalTypeKnowledgeChange,
		Change:        canonicalChange,
		Risk:          strings.TrimSpace(risk),
		RollbackPlan:  strings.TrimSpace(rollbackPlan),
		SchemaVersion: KnowledgeChangeSchemaVersion,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// ComputeKnowledgeChangeRequestHash 重现历史 `knowledge_change` Proposal 创建请求的 v1 哈希。
// 该函数只用于已持久化历史记录的精确重放；新 Proposal 必须使用包含 RiskLevel 的 v2 哈希。
func ComputeKnowledgeChangeRequestHash(workspaceID foundation.ID, change KnowledgeChange, risk, rollbackPlan string) (string, error) {
	canonicalChange, err := ValidateKnowledgeChange(change)
	if err != nil {
		return "", err
	}
	payload := struct {
		WorkspaceID  foundation.ID   `json:"workspace_id"`
		ProposalType ProposalType    `json:"proposal_type"`
		Change       KnowledgeChange `json:"change"`
		Risk         string          `json:"risk"`
		RollbackPlan string          `json:"rollback_plan"`
	}{
		WorkspaceID:  workspaceID,
		ProposalType: ProposalTypeKnowledgeChange,
		Change:       canonicalChange,
		Risk:         strings.TrimSpace(risk),
		RollbackPlan: strings.TrimSpace(rollbackPlan),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// ComputeKnowledgeChangeRequestHashWithRiskLevel 将显式风险等级纳入知识变更创建请求的幂等绑定。
func ComputeKnowledgeChangeRequestHashWithRiskLevel(workspaceID foundation.ID, change KnowledgeChange, riskLevel ProposalRiskLevel, risk, rollbackPlan string) (string, error) {
	canonicalChange, err := ValidateKnowledgeChange(change)
	if err != nil {
		return "", err
	}
	normalizedRiskLevel, err := ParseProposalRiskLevel(riskLevel)
	if err != nil {
		return "", err
	}
	payload := struct {
		RequestSchema string            `json:"request_schema"`
		WorkspaceID   foundation.ID     `json:"workspace_id"`
		ProposalType  ProposalType      `json:"proposal_type"`
		Change        KnowledgeChange   `json:"change"`
		RiskLevel     ProposalRiskLevel `json:"risk_level"`
		Risk          string            `json:"risk"`
		RollbackPlan  string            `json:"rollback_plan"`
	}{
		RequestSchema: "knowledge-change-proposal-request/v2",
		WorkspaceID:   workspaceID,
		ProposalType:  ProposalTypeKnowledgeChange,
		Change:        canonicalChange,
		RiskLevel:     normalizedRiskLevel,
		Risk:          strings.TrimSpace(risk),
		RollbackPlan:  strings.TrimSpace(rollbackPlan),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// ValidateProposalRevisionForType 校验 Proposal Type 与 Revision 结构是否严格匹配。
func ValidateProposalRevisionForType(proposalType ProposalType, revision Revision) error {
	switch NormalizeProposalType(proposalType) {
	case ProposalTypeFilePatch:
		if revision.KnowledgeChange != nil || revision.PublishArtifact != nil {
			return ErrProposalTypeInvalid
		}
		targetPath, err := ValidateTargetPath(revision.TargetPath)
		if err != nil || targetPath != revision.TargetPath || !ValidHash(revision.BaseHash) || strings.TrimSpace(revision.Content) == "" || revision.ChangeHash != ComputeChangeHash(revision.TargetPath, revision.BaseHash, revision.Content) {
			return ErrProposalTypeInvalid
		}
		return nil
	case ProposalTypeKnowledgeChange:
		if strings.TrimSpace(revision.TargetPath) != "" || strings.TrimSpace(revision.BaseHash) != "" || strings.TrimSpace(revision.Content) != "" || strings.TrimSpace(revision.EvidenceSummary) != "" || strings.TrimSpace(revision.Risk) == "" || strings.TrimSpace(revision.RollbackPlan) == "" || revision.KnowledgeChange == nil || revision.PublishArtifact != nil {
			return ErrProposalTypeInvalid
		}
		canonicalChange, err := ValidateKnowledgeChange(*revision.KnowledgeChange)
		if err != nil {
			return ErrProposalTypeInvalid
		}
		expectedHash, err := ComputeKnowledgeChangeHash(canonicalChange, revision.Risk, revision.RollbackPlan)
		if err != nil || revision.ChangeHash != expectedHash {
			return ErrProposalTypeInvalid
		}
		return nil
	case ProposalTypePublishArtifact:
		if strings.TrimSpace(revision.TargetPath) != "" || strings.TrimSpace(revision.BaseHash) != "" || strings.TrimSpace(revision.Content) != "" || strings.TrimSpace(revision.EvidenceSummary) != "" || strings.TrimSpace(revision.Risk) == "" || strings.TrimSpace(revision.RollbackPlan) == "" || revision.KnowledgeChange != nil || revision.PublishArtifact == nil {
			return ErrProposalTypeInvalid
		}
		canonical, err := ValidatePublishArtifact(*revision.PublishArtifact)
		if err != nil {
			return ErrProposalTypeInvalid
		}
		expectedHash, err := ComputePublishArtifactHash(canonical, revision.Risk, revision.RollbackPlan)
		if err != nil || revision.ChangeHash != expectedHash {
			return ErrProposalTypeInvalid
		}
		return nil
	default:
		return ErrProposalTypeInvalid
	}
}

func knowledgeTargetRefKey(value KnowledgeTargetRef) string {
	return strings.Join([]string{string(value.Type), string(value.ID), strings.ToLower(value.Fingerprint)}, "|")
}

func knowledgeNodeRefKey(value knowledge.NodeRef) string {
	return strings.Join([]string{string(value.Type), string(value.ID)}, "|")
}

func knowledgeBaseVersionNodeKey(value KnowledgeBaseVersion) string {
	return strings.Join([]string{string(value.NodeType), string(value.NodeID)}, "|")
}

func knowledgeBaseVersionKey(value KnowledgeBaseVersion) string {
	return fmt.Sprintf("%s|%020d", knowledgeBaseVersionNodeKey(value), value.Version)
}

func knowledgeEvidenceRefKey(value KnowledgeEvidenceRef) string {
	return strings.Join([]string{strings.ToLower(value.SemanticHash), string(value.CandidateEvidenceID)}, "|")
}
