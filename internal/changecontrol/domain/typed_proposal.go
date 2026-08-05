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
	ProposalTypeFilePatch ProposalType = "file_patch"
	// ProposalTypeRestoreDocument restores a published Document through append-only Safe Writeback.
	ProposalTypeRestoreDocument ProposalType = "restore_document"
	ProposalTypeKnowledgeChange ProposalType = "knowledge_change"
	// ProposalTypePublishArtifact freezes an approved Artifact revision for a
	// future formal-knowledge publication capability.
	ProposalTypePublishArtifact ProposalType = "publish_artifact"
	// ProposalTypeDownstreamUpdate 记录 Impact 报告中的下游更新意图，不授予执行能力。
	ProposalTypeDownstreamUpdate ProposalType = "downstream_update"
)

const (
	// KnowledgeChangeSchemaVersion 是首版结构化知识关系变更契约版本。
	KnowledgeChangeSchemaVersion = "knowledge-relation-change/v1"
	// PublishArtifactSchemaVersion is the frozen Artifact publication contract.
	PublishArtifactSchemaVersion = "artifact-publication/v1"
	// DownstreamUpdateSchemaVersion 是 Impact 下游更新 Proposal 的首版冻结契约。
	DownstreamUpdateSchemaVersion = "impact-downstream-update/v1"
	// RestoreDocumentSchemaVersion is the immutable Document restore intent contract.
	RestoreDocumentSchemaVersion = "document-restore/v1"
	// DownstreamUpdateApplyUnavailableCode 表示尚未提供下游更新的执行能力。
	DownstreamUpdateApplyUnavailableCode = "DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE"
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

// DownstreamUpdate 是 `downstream_update` Proposal Revision 的不可变结构化正文。
type DownstreamUpdate struct {
	WorkspaceID        foundation.ID                   `json:"workspace_id"`
	ReportID           foundation.ID                   `json:"report_id"`
	AnalysisVersion    knowledge.ImpactAnalysisVersion `json:"analysis_version"`
	ReportFingerprint  string                          `json:"report_fingerprint"`
	SourceEventID      foundation.ID                   `json:"source_event_id"`
	SourceEventVersion int64                           `json:"source_event_version"`
	TargetType         knowledge.ImpactObjectType      `json:"target_type"`
	TargetID           foundation.ID                   `json:"target_id"`
	BaseVersion        int64                           `json:"base_version"`
	Action             knowledge.ImpactAction          `json:"action"`
	OwnerBinding       knowledge.EventOwnerBinding     `json:"owner_binding"`
	Reason             string                          `json:"reason"`
	SchemaVersion      string                          `json:"schema_version"`
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

// RestoreDocument freezes the server-derived restore target and optimistic baselines.
type RestoreDocument struct {
	WorkspaceID             foundation.ID `json:"workspace_id"`
	DocumentID              foundation.ID `json:"document_id"`
	TargetCommit            string        `json:"target_commit"`
	ExpectedHead            string        `json:"expected_head"`
	ExpectedDocumentVersion int64         `json:"expected_document_version"`
	PreviewHash             string        `json:"preview_hash"`
	CurrentContentHash      string        `json:"current_content_hash"`
	TargetContentHash       string        `json:"target_content_hash"`
	SchemaVersion           string        `json:"schema_version"`
}

var (
	// ErrKnowledgeChangeInvalid 表示知识变更结构化载荷不满足冻结契约。
	ErrKnowledgeChangeInvalid = errors.New("knowledge change payload is invalid")
	// ErrProposalTypeInvalid 表示 Proposal Type 与 Revision 内容不匹配。
	ErrProposalTypeInvalid = errors.New("proposal type does not match revision payload")
	// ErrPublishArtifactInvalid means the frozen Artifact publication payload is invalid.
	ErrPublishArtifactInvalid = errors.New("publish artifact payload is invalid")
	// ErrDownstreamUpdateInvalid 表示下游更新载荷未绑定当前 Impact 与 owner 快照。
	ErrDownstreamUpdateInvalid = errors.New("downstream update payload is invalid")
	// ErrRestoreDocumentInvalid means the frozen restore payload is incomplete or inconsistent.
	ErrRestoreDocumentInvalid = errors.New("restore document payload is invalid")
)

// NewDownstreamUpdateApplyUnavailableError 返回下游更新禁止进入任何写回链路的稳定错误。
func NewDownstreamUpdateApplyUnavailableError() error {
	return foundation.NewError(
		foundation.ErrorVersionConflict,
		DownstreamUpdateApplyUnavailableCode,
		false,
		errors.New("downstream update execution capability is unavailable"),
	)
}

// NormalizeProposalType 返回去空白、默认 file_patch 的 ProposalType。
func NormalizeProposalType(value ProposalType) ProposalType {
	trimmed := ProposalType(strings.ToLower(strings.TrimSpace(string(value))))
	if trimmed == "" {
		return ProposalTypeFilePatch
	}
	return trimmed
}

// ProposalSupportsFileWriteback reports whether a Proposal uses the governed file writeback path.
func ProposalSupportsFileWriteback(value ProposalType) bool {
	normalized := NormalizeProposalType(value)
	return normalized == ProposalTypeFilePatch || normalized == ProposalTypeRestoreDocument
}

// ValidateRestoreDocument canonicalizes and validates one immutable restore intent.
func ValidateRestoreDocument(restore RestoreDocument) (RestoreDocument, error) {
	workspaceID, workspaceErr := foundation.ParseID(string(restore.WorkspaceID))
	documentID, documentErr := foundation.ParseID(string(restore.DocumentID))
	restore.SchemaVersion = strings.TrimSpace(restore.SchemaVersion)
	restore.TargetCommit = strings.ToLower(strings.TrimSpace(restore.TargetCommit))
	restore.ExpectedHead = strings.ToLower(strings.TrimSpace(restore.ExpectedHead))
	restore.PreviewHash = strings.ToLower(strings.TrimSpace(restore.PreviewHash))
	restore.CurrentContentHash = strings.ToLower(strings.TrimSpace(restore.CurrentContentHash))
	restore.TargetContentHash = strings.ToLower(strings.TrimSpace(restore.TargetContentHash))
	if workspaceErr != nil || documentErr != nil || workspaceID == documentID ||
		restore.SchemaVersion != RestoreDocumentSchemaVersion || restore.ExpectedDocumentVersion < 1 ||
		!ValidGitObjectID(restore.TargetCommit) || !ValidGitObjectID(restore.ExpectedHead) ||
		len(restore.TargetCommit) != len(restore.ExpectedHead) || restore.TargetCommit == restore.ExpectedHead ||
		!ValidHash(restore.PreviewHash) || !ValidHash(restore.CurrentContentHash) || !ValidHash(restore.TargetContentHash) ||
		restore.CurrentContentHash == restore.TargetContentHash {
		return RestoreDocument{}, ErrRestoreDocumentInvalid
	}
	restore.WorkspaceID, restore.DocumentID = workspaceID, documentID
	return restore, nil
}

// ComputeContentHash returns the exact-byte SHA-256 used by restore payloads.
func ComputeContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// ComputeRestoreDocumentRequestHash binds idempotency to all restore facts and exact target bytes.
func ComputeRestoreDocumentRequestHash(workspaceID foundation.ID, restore RestoreDocument, targetPath, content, evidence string, riskLevel ProposalRiskLevel, risk, rollback string) (string, error) {
	canonical, err := ValidateRestoreDocument(restore)
	if err != nil || canonical.WorkspaceID != workspaceID {
		return "", ErrRestoreDocumentInvalid
	}
	targetPath, pathErr := ValidateTargetPath(targetPath)
	riskLevel, riskErr := ValidateProposalRiskLevelForType(ProposalTypeRestoreDocument, riskLevel)
	if pathErr != nil || strings.TrimSpace(content) == "" || strings.TrimSpace(evidence) == "" || strings.TrimSpace(risk) == "" || strings.TrimSpace(rollback) == "" || riskErr != nil {
		return "", ErrRestoreDocumentInvalid
	}
	payload := struct {
		RequestSchema string            `json:"request_schema"`
		WorkspaceID   foundation.ID     `json:"workspace_id"`
		ProposalType  ProposalType      `json:"proposal_type"`
		Restore       RestoreDocument   `json:"restore"`
		TargetPath    string            `json:"target_path"`
		Content       string            `json:"content"`
		Evidence      string            `json:"evidence"`
		RiskLevel     ProposalRiskLevel `json:"risk_level"`
		Risk          string            `json:"risk"`
		Rollback      string            `json:"rollback"`
	}{
		RequestSchema: "restore-document-proposal-request/v1", WorkspaceID: workspaceID,
		ProposalType: ProposalTypeRestoreDocument, Restore: canonical, TargetPath: targetPath,
		Content: strings.ReplaceAll(content, "\r\n", "\n"), Evidence: strings.TrimSpace(evidence),
		RiskLevel: riskLevel, Risk: strings.TrimSpace(risk), Rollback: strings.TrimSpace(rollback),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
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
	if knowledgeBaseVersionNodeKey(normalizedBaseVersions[0]) == knowledgeBaseVersionNodeKey(normalizedBaseVersions[1]) {
		return KnowledgeChange{}, ErrKnowledgeChangeInvalid
	}
	expectedVersions := map[string]struct{}{
		knowledgeNodeRefKey(normalized.ChangeSet.Source): {},
		knowledgeNodeRefKey(normalized.ChangeSet.Target): {},
	}
	for _, item := range normalizedBaseVersions {
		key := knowledgeBaseVersionNodeKey(item)
		if _, ok := expectedVersions[key]; !ok {
			return KnowledgeChange{}, ErrKnowledgeChangeInvalid
		}
		delete(expectedVersions, key)
	}
	if len(expectedVersions) != 0 {
		return KnowledgeChange{}, ErrKnowledgeChangeInvalid
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

// ValidateDownstreamUpdate 规范化并校验 Impact 下游更新的完整 owner 绑定。
func ValidateDownstreamUpdate(update DownstreamUpdate) (DownstreamUpdate, error) {
	update.SchemaVersion = strings.TrimSpace(update.SchemaVersion)
	update.AnalysisVersion = knowledge.ImpactAnalysisVersion(strings.TrimSpace(string(update.AnalysisVersion)))
	update.TargetType = knowledge.ImpactObjectType(strings.ToUpper(strings.TrimSpace(string(update.TargetType))))
	update.Action = knowledge.ImpactAction(strings.ToUpper(strings.TrimSpace(string(update.Action))))
	update.ReportFingerprint = strings.ToLower(strings.TrimSpace(update.ReportFingerprint))
	update.Reason = strings.TrimSpace(update.Reason)
	if update.SchemaVersion != DownstreamUpdateSchemaVersion || update.AnalysisVersion != knowledge.ImpactAnalysisVersionV2 ||
		!ValidHash(update.ReportFingerprint) || update.SourceEventVersion < 1 || update.BaseVersion < 1 ||
		update.Reason == "" || len(update.Reason) > 4096 || strings.ContainsAny(update.Reason, "\r\n") {
		return DownstreamUpdate{}, ErrDownstreamUpdateInvalid
	}

	workspaceID, workspaceErr := foundation.ParseID(string(update.WorkspaceID))
	reportID, reportErr := foundation.ParseID(string(update.ReportID))
	eventID, eventErr := foundation.ParseID(string(update.SourceEventID))
	targetID, targetErr := foundation.ParseID(string(update.TargetID))
	if workspaceErr != nil || reportErr != nil || eventErr != nil || targetErr != nil ||
		workspaceID == reportID || workspaceID == eventID || reportID == eventID {
		return DownstreamUpdate{}, ErrDownstreamUpdateInvalid
	}
	update.WorkspaceID, update.ReportID, update.SourceEventID, update.TargetID = workspaceID, reportID, eventID, targetID

	switch update.TargetType {
	case knowledge.ImpactObjectArtifact:
		if update.Action != knowledge.ImpactActionRegenerateArtifact || update.OwnerBinding.Artifact == nil || update.OwnerBinding.ReviewCard != nil {
			return DownstreamUpdate{}, ErrDownstreamUpdateInvalid
		}
		binding := *update.OwnerBinding.Artifact
		artifactID, artifactErr := foundation.ParseID(string(binding.ArtifactID))
		revisionID, revisionErr := foundation.ParseID(string(binding.RevisionID))
		if artifactErr != nil || revisionErr != nil {
			return DownstreamUpdate{}, ErrDownstreamUpdateInvalid
		}
		binding.ArtifactID, binding.RevisionID = artifactID, revisionID
		binding.ContentHash = strings.ToLower(strings.TrimSpace(binding.ContentHash))
		update.OwnerBinding = knowledge.EventOwnerBinding{Artifact: &binding}
	case knowledge.ImpactObjectReviewCard:
		if update.Action != knowledge.ImpactActionRevalidateReviewCard || update.OwnerBinding.Artifact != nil || update.OwnerBinding.ReviewCard == nil {
			return DownstreamUpdate{}, ErrDownstreamUpdateInvalid
		}
		binding := *update.OwnerBinding.ReviewCard
		cardID, cardErr := foundation.ParseID(string(binding.CardID))
		claimID, claimErr := foundation.ParseID(string(binding.ClaimID))
		if cardErr != nil || claimErr != nil {
			return DownstreamUpdate{}, ErrDownstreamUpdateInvalid
		}
		binding.CardID, binding.ClaimID = cardID, claimID
		binding.Status = strings.ToUpper(strings.TrimSpace(binding.Status))
		binding.Fingerprint = strings.ToLower(strings.TrimSpace(binding.Fingerprint))
		binding.EvidenceBindingFingerprint = strings.ToLower(strings.TrimSpace(binding.EvidenceBindingFingerprint))
		update.OwnerBinding = knowledge.EventOwnerBinding{ReviewCard: &binding}
	default:
		return DownstreamUpdate{}, ErrDownstreamUpdateInvalid
	}

	object := knowledge.ImpactObject{
		Type: update.TargetType, ID: update.TargetID, WorkspaceID: update.WorkspaceID,
		Version: update.BaseVersion, Action: update.Action, Reason: update.Reason, RequiresProposal: true,
		ArtifactBinding: update.OwnerBinding.Artifact, ReviewCardBinding: update.OwnerBinding.ReviewCard,
	}
	if err := knowledge.ValidateImpactObject(object); err != nil {
		return DownstreamUpdate{}, ErrDownstreamUpdateInvalid
	}
	return update, nil
}

// ComputeDownstreamUpdateHash 计算下游更新意图的 canonical change hash。
func ComputeDownstreamUpdateHash(update DownstreamUpdate, risk, rollbackPlan string) (string, error) {
	canonical, err := ValidateDownstreamUpdate(update)
	if err != nil {
		return "", err
	}
	payload := struct {
		ProposalType ProposalType     `json:"proposal_type"`
		Update       DownstreamUpdate `json:"update"`
		Risk         string           `json:"risk"`
		RollbackPlan string           `json:"rollback_plan"`
	}{ProposalType: ProposalTypeDownstreamUpdate, Update: canonical, Risk: strings.TrimSpace(risk), RollbackPlan: strings.TrimSpace(rollbackPlan)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// ComputeDownstreamUpdateRequestHash 将 Workspace、owner binding 与风险说明绑定到幂等键。
func ComputeDownstreamUpdateRequestHash(workspaceID foundation.ID, update DownstreamUpdate, riskLevel ProposalRiskLevel, risk, rollbackPlan string) (string, error) {
	canonical, err := ValidateDownstreamUpdate(update)
	if err != nil || canonical.WorkspaceID != workspaceID {
		return "", ErrDownstreamUpdateInvalid
	}
	normalizedRiskLevel, err := ValidateProposalRiskLevelForType(ProposalTypeDownstreamUpdate, riskLevel)
	if err != nil {
		return "", err
	}
	payload := struct {
		RequestSchema string            `json:"request_schema"`
		WorkspaceID   foundation.ID     `json:"workspace_id"`
		ProposalType  ProposalType      `json:"proposal_type"`
		Update        DownstreamUpdate  `json:"update"`
		RiskLevel     ProposalRiskLevel `json:"risk_level"`
		Risk          string            `json:"risk"`
		RollbackPlan  string            `json:"rollback_plan"`
	}{
		RequestSchema: "downstream-update-proposal-request/v1", WorkspaceID: workspaceID,
		ProposalType: ProposalTypeDownstreamUpdate, Update: canonical, RiskLevel: normalizedRiskLevel,
		Risk: strings.TrimSpace(risk), RollbackPlan: strings.TrimSpace(rollbackPlan),
	}
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
		if revision.KnowledgeChange != nil || revision.PublishArtifact != nil || revision.DownstreamUpdate != nil || revision.RestoreDocument != nil {
			return ErrProposalTypeInvalid
		}
		targetPath, err := ValidateTargetPath(revision.TargetPath)
		mode, modeErr := ValidateTargetMode(revision.TargetMode)
		expectedHash, hashErr := computeChangeHashForMode(revision.TargetPath, mode, revision.BaseHash, revision.Content)
		if err != nil || modeErr != nil || targetPath != revision.TargetPath || strings.TrimSpace(revision.Content) == "" || hashErr != nil || revision.ChangeHash != expectedHash {
			return ErrProposalTypeInvalid
		}
		return nil
	case ProposalTypeRestoreDocument:
		if revision.KnowledgeChange != nil || revision.PublishArtifact != nil || revision.DownstreamUpdate != nil || revision.RestoreDocument == nil ||
			NormalizeTargetMode(revision.TargetMode) != TargetModeReplace {
			return ErrProposalTypeInvalid
		}
		restore, err := ValidateRestoreDocument(*revision.RestoreDocument)
		targetPath, pathErr := ValidateTargetPath(revision.TargetPath)
		expectedHash, hashErr := ComputeChangeHashForTarget(restore.WorkspaceID, revision.TargetPath, revision.TargetMode, revision.BaseHash, revision.Content)
		if err != nil || pathErr != nil || targetPath != revision.TargetPath || hashErr != nil ||
			strings.TrimSpace(revision.Content) == "" || strings.TrimSpace(revision.EvidenceSummary) == "" ||
			strings.TrimSpace(revision.Risk) == "" || strings.TrimSpace(revision.RollbackPlan) == "" ||
			revision.BaseHash != restore.CurrentContentHash || ComputeContentHash([]byte(revision.Content)) != restore.TargetContentHash || revision.ChangeHash != expectedHash {
			return ErrProposalTypeInvalid
		}
		return nil
	case ProposalTypeKnowledgeChange:
		if NormalizeTargetMode(revision.TargetMode) != TargetModeReplace || strings.TrimSpace(revision.TargetPath) != "" || strings.TrimSpace(revision.BaseHash) != "" || strings.TrimSpace(revision.Content) != "" || strings.TrimSpace(revision.EvidenceSummary) != "" || strings.TrimSpace(revision.Risk) == "" || strings.TrimSpace(revision.RollbackPlan) == "" || revision.KnowledgeChange == nil || revision.PublishArtifact != nil || revision.DownstreamUpdate != nil || revision.RestoreDocument != nil {
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
		if NormalizeTargetMode(revision.TargetMode) != TargetModeReplace || strings.TrimSpace(revision.TargetPath) != "" || strings.TrimSpace(revision.BaseHash) != "" || strings.TrimSpace(revision.Content) != "" || strings.TrimSpace(revision.EvidenceSummary) != "" || strings.TrimSpace(revision.Risk) == "" || strings.TrimSpace(revision.RollbackPlan) == "" || revision.KnowledgeChange != nil || revision.PublishArtifact == nil || revision.DownstreamUpdate != nil || revision.RestoreDocument != nil {
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
	case ProposalTypeDownstreamUpdate:
		if NormalizeTargetMode(revision.TargetMode) != TargetModeReplace || strings.TrimSpace(revision.TargetPath) != "" || strings.TrimSpace(revision.BaseHash) != "" || strings.TrimSpace(revision.Content) != "" || strings.TrimSpace(revision.EvidenceSummary) != "" || strings.TrimSpace(revision.Risk) == "" || strings.TrimSpace(revision.RollbackPlan) == "" || revision.KnowledgeChange != nil || revision.PublishArtifact != nil || revision.DownstreamUpdate == nil || revision.RestoreDocument != nil {
			return ErrProposalTypeInvalid
		}
		canonical, err := ValidateDownstreamUpdate(*revision.DownstreamUpdate)
		if err != nil {
			return ErrProposalTypeInvalid
		}
		expectedHash, err := ComputeDownstreamUpdateHash(canonical, revision.Risk, revision.RollbackPlan)
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
