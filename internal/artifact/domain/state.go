package domain

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// SubmitOutline 创建含新大纲的下一 Revision，并将 Artifact 移入 OUTLINE_REVIEW。
func SubmitOutline(artifact Artifact, current Revision, nextRevisionID foundation.ID, outline []OutlineSection, at time.Time) (Artifact, Revision, error) {
	if err := validateCurrent(artifact, current); err != nil {
		return Artifact{}, Revision{}, err
	}
	if artifact.Status != StatusPlanning {
		return Artifact{}, Revision{}, versionConflict(ErrorCodeArtifactTransitionInvalid, "only planning artifact can submit outline")
	}
	return nextRevision(artifact, current, nextRevisionID, outline, nil, CreatorHuman, nil, StatusOutlineReview, at)
}

// ApproveOutline 创建审批快照，并将 Artifact 移入 GENERATING。
func ApproveOutline(artifact Artifact, current Revision, nextRevisionID foundation.ID, at time.Time) (Artifact, Revision, error) {
	if err := validateCurrent(artifact, current); err != nil {
		return Artifact{}, Revision{}, err
	}
	if artifact.Status != StatusOutlineReview || len(current.Outline) == 0 {
		return Artifact{}, Revision{}, versionConflict(ErrorCodeArtifactTransitionInvalid, "only submitted non-empty outline can be approved")
	}
	return nextRevision(artifact, current, nextRevisionID, current.Outline, current.Sections, CreatorHuman, nil, StatusGenerating, at)
}

// StartRevision 让 DRAFT、APPROVED 或 EXPORTED Artifact 回到 GENERATING，以便通过新 Revision 修订。
func StartRevision(artifact Artifact, current Revision, at time.Time) (Artifact, error) {
	if err := validateCurrent(artifact, current); err != nil {
		return Artifact{}, err
	}
	if artifact.Status != StatusDraft && artifact.Status != StatusApproved && artifact.Status != StatusExported {
		return Artifact{}, versionConflict(ErrorCodeArtifactTransitionInvalid, "artifact status cannot start a revision")
	}
	return transitionArtifact(artifact, StatusGenerating, current, at)
}

// RecordSection 创建新的不可变 Revision；正文只能使用已验证的 Citation，缺口章节不能填充正文。
func RecordSection(artifact Artifact, current Revision, nextRevisionID foundation.ID, section Section, creator CreatorType, metadata *GenerationMetadata, at time.Time) (Artifact, Revision, error) {
	if err := validateCurrent(artifact, current); err != nil {
		return Artifact{}, Revision{}, err
	}
	if artifact.Status != StatusGenerating {
		return Artifact{}, Revision{}, versionConflict(ErrorCodeArtifactTransitionInvalid, "section can only be recorded while generating")
	}
	sections := CloneRevision(current).Sections
	replaced := false
	for index := range sections {
		if sections[index].Key == section.Key {
			sections[index] = section
			replaced = true
		}
	}
	if !replaced {
		sections = append(sections, section)
	}
	status := StatusGenerating
	if sectionsComplete(current.Outline, sections) {
		status = StatusDraft
	}
	return nextRevision(artifact, current, nextRevisionID, current.Outline, sections, creator, metadata, status, at)
}

// ApproveDraft 将完整、有来源覆盖的草稿标记为 APPROVED，不写入正式知识。
func ApproveDraft(artifact Artifact, current Revision, at time.Time) (Artifact, error) {
	if err := validateCurrent(artifact, current); err != nil {
		return Artifact{}, err
	}
	if artifact.Status != StatusDraft || !revisionComplete(current) {
		return Artifact{}, versionConflict(ErrorCodeArtifactTransitionInvalid, "only complete draft can be approved")
	}
	return transitionArtifact(artifact, StatusApproved, current, at)
}

// MarkExported 记录导出状态；导出不会把 Artifact 转换为正式知识。
func MarkExported(artifact Artifact, current Revision, at time.Time) (Artifact, error) {
	if err := validateCurrent(artifact, current); err != nil {
		return Artifact{}, err
	}
	if artifact.Status != StatusApproved {
		return Artifact{}, versionConflict(ErrorCodeArtifactTransitionInvalid, "only approved artifact can be exported")
	}
	return transitionArtifact(artifact, StatusExported, current, at)
}

// CreatePublicationRequest 只创建 Publish Artifact Proposal 的输入，并转为 PUBLISH_PROPOSED。
func CreatePublicationRequest(artifact Artifact, current Revision, at time.Time) (Artifact, PublicationRequest, error) {
	if err := validateCurrent(artifact, current); err != nil {
		return Artifact{}, PublicationRequest{}, err
	}
	if artifact.Status != StatusApproved && artifact.Status != StatusExported {
		return Artifact{}, PublicationRequest{}, versionConflict(ErrorCodeArtifactPublicationInvalid, "only approved artifact can request publication")
	}
	updated, err := transitionArtifact(artifact, StatusPublishProposed, current, at)
	if err != nil {
		return Artifact{}, PublicationRequest{}, err
	}
	return updated, PublicationRequest{
		ProposalType: PublishArtifactProposalType, ArtifactID: updated.ID, WorkspaceID: updated.WorkspaceID,
		RevisionID: current.ID, RevisionNo: current.RevisionNo, ArtifactVersion: updated.Version,
		ContentHash: current.ContentHash, SourceCoverage: cloneCoverage(updated.SourceCoverage), RequestedAt: updated.UpdatedAt,
	}, nil
}

// ConfirmPublished 只接受已经由外部 Change Control 写回的 Proposal/Document 绑定，领域层不执行写入。
func ConfirmPublished(artifact Artifact, current Revision, confirmation PublicationConfirmation) (Artifact, error) {
	if err := validateCurrent(artifact, current); err != nil {
		return Artifact{}, err
	}
	if artifact.Status != StatusPublishProposed || !validID(confirmation.ProposalID) || !validID(confirmation.FormalDocumentID) || confirmation.ProposalID == confirmation.FormalDocumentID || confirmation.ConfirmedAt.IsZero() {
		return Artifact{}, invalid(ErrorCodeArtifactPublicationInvalid, "publication confirmation is invalid")
	}
	return transitionArtifact(artifact, StatusPublished, current, confirmation.ConfirmedAt)
}

// Archive 将任意非归档 Artifact 转为只读状态，历史 Revision 保持不变。
func Archive(artifact Artifact, current Revision, at time.Time) (Artifact, error) {
	if err := validateCurrent(artifact, current); err != nil {
		return Artifact{}, err
	}
	if artifact.Status == StatusArchived {
		return Artifact{}, versionConflict(ErrorCodeArtifactTransitionInvalid, "artifact is already archived")
	}
	return transitionArtifact(artifact, StatusArchived, current, at)
}

func nextRevision(artifact Artifact, current Revision, nextRevisionID foundation.ID, outline []OutlineSection, sections []Section, creator CreatorType, metadata *GenerationMetadata, status Status, at time.Time) (Artifact, Revision, error) {
	if !validID(nextRevisionID) || nextRevisionID == artifact.ID || nextRevisionID == current.ID || at.IsZero() || at.UTC().Before(current.CreatedAt.UTC()) {
		return Artifact{}, Revision{}, invalid(ErrorCodeArtifactInvalid, "next revision identity or time is invalid")
	}
	nextOutline := make([]OutlineSection, len(outline))
	copy(nextOutline, outline)
	next := Revision{ID: nextRevisionID, ArtifactID: artifact.ID, RevisionNo: current.RevisionNo + 1, Outline: nextOutline, Sections: cloneSections(sections), CreatedBy: creator, Metadata: cloneMetadata(metadata), CreatedAt: at.UTC()}
	hash, err := ComputeRevisionContentHash(next)
	if err != nil {
		return Artifact{}, Revision{}, err
	}
	next.ContentHash = hash
	if err := ValidateRevision(next); err != nil {
		return Artifact{}, Revision{}, err
	}
	updated, err := transitionArtifact(artifact, status, next, at)
	if err != nil {
		return Artifact{}, Revision{}, err
	}
	return updated, next, nil
}

func transitionArtifact(artifact Artifact, status Status, revision Revision, at time.Time) (Artifact, error) {
	if at.IsZero() || at.UTC().Before(artifact.UpdatedAt.UTC()) {
		return Artifact{}, invalid(ErrorCodeArtifactInvalid, "artifact update time is invalid")
	}
	updated := CloneArtifact(artifact)
	updated.Status = status
	updated.CurrentRevisionID = revision.ID
	updated.SourceCoverage = coverageFromRevision(revision)
	updated.Version++
	updated.UpdatedAt = at.UTC()
	if err := ValidateArtifact(updated); err != nil {
		return Artifact{}, err
	}
	return updated, nil
}

func validateCurrent(artifact Artifact, revision Revision) error {
	if err := ValidateArtifact(artifact); err != nil {
		return err
	}
	if err := ValidateRevision(revision); err != nil {
		return err
	}
	if artifact.ID != revision.ArtifactID || artifact.CurrentRevisionID != revision.ID {
		return inconsistent(ErrorCodeArtifactInvalid, "artifact current revision binding is inconsistent")
	}
	return nil
}

func revisionComplete(revision Revision) bool {
	return sectionsComplete(revision.Outline, revision.Sections)
}

func sectionsComplete(outline []OutlineSection, sections []Section) bool {
	if len(outline) == 0 || len(sections) != len(outline) {
		return false
	}
	sectionKeys := make(map[string]struct{}, len(sections))
	for _, section := range sections {
		sectionKeys[section.Key] = struct{}{}
	}
	for _, outlineSection := range outline {
		if _, exists := sectionKeys[outlineSection.Key]; !exists {
			return false
		}
	}
	return true
}

func coverageFromRevision(revision Revision) []Coverage {
	coverage := make([]Coverage, len(revision.Sections))
	for index, section := range revision.Sections {
		coverage[index] = section.Coverage
		coverage[index].Gaps = make([]Gap, len(section.Coverage.Gaps))
		copy(coverage[index].Gaps, section.Coverage.Gaps)
	}
	return coverage
}

func cloneSections(sections []Section) []Section {
	return CloneRevision(Revision{Sections: sections}).Sections
}

func cloneMetadata(metadata *GenerationMetadata) *GenerationMetadata {
	if metadata == nil {
		return nil
	}
	copyValue := *metadata
	return &copyValue
}
