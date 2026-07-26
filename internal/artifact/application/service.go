// Package application 提供 Artifact 纯领域操作的 ID 与时间编排，不依赖 HTTP 或持久化实现。
package application

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Service 为 Artifact 领域操作提供 ID 与 UTC 时钟，不包含 Repository 或正式知识写入端口。
type Service struct {
	ids   foundation.IDGenerator
	clock foundation.Clock
}

// PlanArtifactCommand 是规划一个隔离 Artifact 的应用输入。
type PlanArtifactCommand struct {
	WorkspaceID     foundation.ID
	Type            string
	Title           string
	ScopeDefinition string
}

// RevisionCommand 携带当前 Artifact 和其当前不可变 Revision。
type RevisionCommand struct {
	Artifact domain.Artifact
	Revision domain.Revision
}

// SubmitOutlineCommand 提交人工待审的大纲。
type SubmitOutlineCommand struct {
	RevisionCommand
	Outline []domain.OutlineSection
}

// RecordSectionCommand 基于已验证 Citation 记录一个章节生成或修订结果。
type RecordSectionCommand struct {
	RevisionCommand
	Section  domain.Section
	Creator  domain.CreatorType
	Metadata *domain.GenerationMetadata
}

// PublicationCommand 请求创建 PUBLISH_ARTIFACT Proposal，不直接写入正式知识。
type PublicationCommand struct{ RevisionCommand }

// PublicationConfirmationCommand 记录外部 Proposal 已成功写回后的绑定确认。
type PublicationConfirmationCommand struct {
	RevisionCommand
	Confirmation domain.PublicationConfirmation
}

// State 返回同一操作后的 Artifact 和当前 Revision 快照。
type State struct {
	Artifact domain.Artifact
	Revision domain.Revision
}

// NewService 构造不带持久化或知识写入能力的 Artifact 应用服务。
func NewService(ids foundation.IDGenerator, clock foundation.Clock) (*Service, error) {
	if ids == nil || clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "ARTIFACT_SERVICE_UNAVAILABLE", true, errors.New("artifact id generator or clock is unavailable"))
	}
	return &Service{ids: ids, clock: clock}, nil
}

// PlanArtifact 规划 Artifact 并创建首个空 Revision。
func (service *Service) PlanArtifact(command PlanArtifactCommand) (State, error) {
	artifactID, revisionID, now, err := service.newIDsAndTime()
	if err != nil {
		return State{}, err
	}
	artifact, revision, err := domain.PlanArtifact(domain.PlanInput{ArtifactID: artifactID, InitialRevisionID: revisionID, WorkspaceID: command.WorkspaceID, Type: command.Type, Title: command.Title, ScopeDefinition: command.ScopeDefinition, CreatedAt: now})
	if err != nil {
		return State{}, err
	}
	return State{Artifact: artifact, Revision: revision}, nil
}

// SubmitOutline 创建新 Revision 并等待大纲审批。
func (service *Service) SubmitOutline(command SubmitOutlineCommand) (State, error) {
	nextID, now, err := service.newIDAndTime()
	if err != nil {
		return State{}, err
	}
	artifact, revision, err := domain.SubmitOutline(command.Artifact, command.Revision, nextID, command.Outline, now)
	return State{Artifact: artifact, Revision: revision}, err
}

// ApproveOutline 创建审批快照并开启章节生成。
func (service *Service) ApproveOutline(command RevisionCommand) (State, error) {
	nextID, now, err := service.newIDAndTime()
	if err != nil {
		return State{}, err
	}
	artifact, revision, err := domain.ApproveOutline(command.Artifact, command.Revision, nextID, now)
	return State{Artifact: artifact, Revision: revision}, err
}

// StartRevision 将草稿、已批准或已导出的 Artifact 带回 GENERATING，供下一 Revision 使用。
func (service *Service) StartRevision(command RevisionCommand) (State, error) {
	now, err := service.now()
	if err != nil {
		return State{}, err
	}
	artifact, err := domain.StartRevision(command.Artifact, command.Revision, now)
	return State{Artifact: artifact, Revision: domain.CloneRevision(command.Revision)}, err
}

// GenerateSection 记录 Agent 基于已验证内容生成的单章 Revision。
func (service *Service) GenerateSection(command RecordSectionCommand) (State, error) {
	command.Creator = domain.CreatorAgent
	return service.recordSection(command)
}

// ReviseArtifact 记录人工或 Agent 对单章的修订，并始终创建新的 Revision。
func (service *Service) ReviseArtifact(command RecordSectionCommand) (State, error) {
	if command.Creator == "" {
		command.Creator = domain.CreatorHuman
	}
	return service.recordSection(command)
}

// ApproveDraft 标记完整 Revision 为 APPROVED；该操作不触发正式知识写入。
func (service *Service) ApproveDraft(command RevisionCommand) (State, error) {
	now, err := service.now()
	if err != nil {
		return State{}, err
	}
	artifact, err := domain.ApproveDraft(command.Artifact, command.Revision, now)
	return State{Artifact: artifact, Revision: domain.CloneRevision(command.Revision)}, err
}

// MarkExported 标记已批准 Artifact 已导出，导出不会改变其正式知识边界。
func (service *Service) MarkExported(command RevisionCommand) (State, error) {
	now, err := service.now()
	if err != nil {
		return State{}, err
	}
	artifact, err := domain.MarkExported(command.Artifact, command.Revision, now)
	return State{Artifact: artifact, Revision: domain.CloneRevision(command.Revision)}, err
}

// PublishArtifactProposal 创建只读的 Publish Proposal 请求，调用方必须交给 Change Control。
func (service *Service) PublishArtifactProposal(command PublicationCommand) (State, domain.PublicationRequest, error) {
	now, err := service.now()
	if err != nil {
		return State{}, domain.PublicationRequest{}, err
	}
	artifact, request, err := domain.CreatePublicationRequest(command.Artifact, command.Revision, now)
	return State{Artifact: artifact, Revision: domain.CloneRevision(command.Revision)}, request, err
}

// ConfirmPublished 仅记录 Proposal 已写回正式知识的外部确认，不执行 Document 写入。
func (service *Service) ConfirmPublished(command PublicationConfirmationCommand) (State, error) {
	artifact, err := domain.ConfirmPublished(command.Artifact, command.Revision, command.Confirmation)
	return State{Artifact: artifact, Revision: domain.CloneRevision(command.Revision)}, err
}

func (service *Service) recordSection(command RecordSectionCommand) (State, error) {
	nextID, now, err := service.newIDAndTime()
	if err != nil {
		return State{}, err
	}
	artifact, revision, err := domain.RecordSection(command.Artifact, command.Revision, nextID, command.Section, command.Creator, command.Metadata, now)
	return State{Artifact: artifact, Revision: revision}, err
}

func (service *Service) newIDsAndTime() (foundation.ID, foundation.ID, time.Time, error) {
	if err := service.available(); err != nil {
		return "", "", time.Time{}, err
	}
	artifactID, err := service.ids.New()
	if err != nil {
		return "", "", time.Time{}, err
	}
	revisionID, err := service.ids.New()
	if err != nil {
		return "", "", time.Time{}, err
	}
	now, err := service.now()
	return artifactID, revisionID, now, err
}

func (service *Service) newIDAndTime() (foundation.ID, time.Time, error) {
	if err := service.available(); err != nil {
		return "", time.Time{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return "", time.Time{}, err
	}
	now, err := service.now()
	return id, now, err
}

func (service *Service) now() (time.Time, error) {
	if err := service.available(); err != nil {
		return time.Time{}, err
	}
	now := service.clock.Now().UTC()
	if now.IsZero() {
		return time.Time{}, foundation.NewError(foundation.ErrorConsistencyViolation, "ARTIFACT_CLOCK_INVALID", false, errors.New("artifact clock returned zero time"))
	}
	return now, nil
}

func (service *Service) available() error {
	if service == nil || service.ids == nil || service.clock == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "ARTIFACT_SERVICE_UNAVAILABLE", true, errors.New("artifact service is unavailable"))
	}
	return nil
}
