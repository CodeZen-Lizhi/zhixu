package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// TargetReader 从服务端受控的 Workspace 边界读取目标文件当前哈希。
type TargetReader interface {
	CurrentHash(context.Context, foundation.ID, string) (string, error)
}

// CreateOnlyTargetReader 证明受控 Workspace 中的规范目标路径仍不存在。
// 单独的窄接口避免改变既有 REPLACE Reader 的兼容契约。
type CreateOnlyTargetReader interface {
	EnsureTargetAbsent(context.Context, foundation.ID, string, string) error
}

// CurrentContentReader 在同一次受限读取中返回目标正文与哈希，供审阅 UI 展示真实 Diff。
type CurrentContentReader interface {
	CurrentContent(context.Context, foundation.ID, string, int64) ([]byte, string, error)
}

// MaxProposalCurrentContentBytes 是审阅接口允许返回的当前文件正文上限。
const MaxProposalCurrentContentBytes int64 = 1024 * 1024

// ProposalCurrentContent 是当前 Workspace 文件与 Proposal 基线的只读比较结果。
type ProposalCurrentContent struct {
	ProposalID  foundation.ID
	WorkspaceID foundation.ID
	TargetPath  string
	// TargetMode 区分替换既有文件与只创建缺失目标的审阅基线。
	TargetMode    domain.TargetMode
	Content       string
	CurrentHash   string
	BaseHash      string
	BaseHashMatch bool
}

// ApprovalGitInspector 在批准时从服务端 Workspace 读取严格、干净且 attached 的 Git 基线。
// 调用方不能提供 HEAD；实现返回的快照是 Approval Git HEAD 的唯一来源。
type ApprovalGitInspector interface {
	CaptureApprovalSnapshot(context.Context, foundation.ID) (domain.GitSnapshot, error)
}

// CreateOnlyApprovalGitInspector 证明批准的 Git 树中没有 CREATE_ONLY 目标条目。
type CreateOnlyApprovalGitInspector interface {
	EnsureTargetAbsentAt(context.Context, foundation.ID, string, string) error
}

// Service 协调 Proposal、Approval 与无副作用 Apply 前置校验。
type Service struct {
	repo       domain.Repository
	ids        foundation.IDGenerator
	clock      foundation.Clock
	targets    TargetReader
	git        ApprovalGitInspector
	dispatcher ApprovalDispatcher
	// mergeEngine is optional during the expand window; revision endpoints fail
	// closed until Composition Root injects the fixed platform adapter.
	mergeEngine RevisionMergeEngine
	// revisionWorkflowCanceller asks the Workflow owner to converge an old
	// Revision run before the append transaction attempts to supersede it.
	revisionWorkflowCanceller RevisionWorkflowCanceller
	// knowledgeApprovalApplier 是 typed knowledge_change 的原子 Approval→Relation seam。
	// 为空时 file_patch 仍可用，但批准 knowledge_change 必须 fail closed。
	knowledgeApprovalApplier knowledgeapplication.ApprovedRelationApprovalPort
}

func (s *Service) markNeedsRevision(ctx context.Context, proposal domain.Proposal) error {
	if err := s.repo.MarkNeedsRevision(ctx, proposal.ID, proposal.Revision.ID, proposal.Version, s.clock.Now()); err != nil {
		return err
	}
	proposal.Status = domain.StatusNeedsRevision
	_, err := s.ensureRevisionWorkflowCancellation(ctx, proposal)
	return err
}

// NewServiceWithDispatch 创建启用 Approval→Workflow/River 原子投递的 Change Control 应用服务。
func NewServiceWithDispatch(repo domain.Repository, ids foundation.IDGenerator, clock foundation.Clock, targets TargetReader, git ApprovalGitInspector, dispatcher ApprovalDispatcher, knowledgeAppliers ...knowledgeapplication.ApprovedRelationApplyPort) (*Service, error) {
	service, err := NewService(repo, ids, clock, targets, git, knowledgeAppliers...)
	if err != nil {
		return nil, err
	}
	if isNilApprovalDispatcher(dispatcher) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "APPROVAL_DISPATCHER_MISSING", false, errors.New("approval dispatcher is missing"))
	}
	service.dispatcher = dispatcher
	return service, nil
}

// MaxWriteAuthorizationTTL 是单次写权限的服务端有效期上限。
const MaxWriteAuthorizationTTL = 5 * time.Minute

// NewService 创建 Change Control 应用服务。
func NewService(repo domain.Repository, ids foundation.IDGenerator, clock foundation.Clock, targets TargetReader, git ApprovalGitInspector, knowledgeAppliers ...knowledgeapplication.ApprovedRelationApplyPort) (*Service, error) {
	if repo == nil || ids == nil || clock == nil || targets == nil || git == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CHANGE_CONTROL_DEPENDENCY_MISSING", false, errors.New("change control dependency missing"))
	}
	if len(knowledgeAppliers) > 1 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_RELATION_APPLIER_INVALID", false, errors.New("only one knowledge relation applier may be configured"))
	}
	service := &Service{repo: repo, ids: ids, clock: clock, targets: targets, git: git}
	if len(knowledgeAppliers) == 1 && !isNilKnowledgeRelationApplier(knowledgeAppliers[0]) {
		if approvalApplier, ok := knowledgeAppliers[0].(knowledgeapplication.ApprovedRelationApprovalPort); ok && !isNilKnowledgeRelationApprovalApplier(approvalApplier) {
			service.knowledgeApprovalApplier = approvalApplier
		}
	}
	return service, nil
}

// CreateCommand 是创建第一版 Proposal 所需的完整变更快照。
type CreateCommand struct {
	WorkspaceID     foundation.ID
	TargetPath      string
	IdempotencyKey  string
	BaseHash        string
	Content         string
	EvidenceSummary string
	RiskLevel       domain.ProposalRiskLevel
	Risk            string
	RollbackPlan    string
}

// CreateCreateOnlyFileProposalCommand 是 Authoring 首次发布文件的受限 Proposal 命令。
type CreateCreateOnlyFileProposalCommand struct {
	WorkspaceID     foundation.ID
	TargetPath      string
	IdempotencyKey  string
	Content         string
	EvidenceSummary string
	RiskLevel       domain.ProposalRiskLevel
	Risk            string
	RollbackPlan    string
}

// CreateKnowledgeChangeCommand 是创建 `knowledge_change` Proposal 的结构化命令。
type CreateKnowledgeChangeCommand struct {
	WorkspaceID     foundation.ID
	IdempotencyKey  string
	KnowledgeChange domain.KnowledgeChange
	RiskLevel       domain.ProposalRiskLevel
	Risk            string
	RollbackPlan    string
}

// CreatePublishArtifactCommand creates a reviewable, frozen Artifact publication proposal.
// It intentionally has no formal Document or execution capability fields.
type CreatePublishArtifactCommand struct {
	WorkspaceID    foundation.ID
	IdempotencyKey string
	Publication    domain.PublishArtifact
	RiskLevel      domain.ProposalRiskLevel
	Risk           string
	RollbackPlan   string
}

// CreateDownstreamUpdateCommand 选择当前 Impact 报告中的一个 owner-backed target。
type CreateDownstreamUpdateCommand struct {
	WorkspaceID    foundation.ID
	ReportID       foundation.ID
	TargetType     knowledge.ImpactObjectType
	TargetID       foundation.ID
	Action         knowledge.ImpactAction
	IdempotencyKey string
}

// CreateRestoreDocumentCommand creates one typed, append-only Document restore proposal.
type CreateRestoreDocumentCommand struct {
	WorkspaceID     foundation.ID
	TargetPath      string
	IdempotencyKey  string
	Content         string
	EvidenceSummary string
	Risk            string
	RollbackPlan    string
	Restore         domain.RestoreDocument
}

// DownstreamUpdateFactory 从当前报告和 owner 事实重建不可变 Proposal 载荷。
type DownstreamUpdateFactory interface {
	BuildDownstreamUpdate(context.Context, foundation.ID, foundation.ID, knowledge.ImpactObjectType, foundation.ID, knowledge.ImpactAction) (domain.DownstreamUpdate, error)
}

// ProposalCreateLookup 为外部目标事实变化后的精确幂等重放提供持久绑定查询。
type ProposalCreateLookup interface {
	FindProposalByIdempotencyKey(context.Context, foundation.ID, string) (domain.Proposal, bool, error)
}

const (
	downstreamUpdateRiskNarrative = "Impact report identified an owner-backed downstream dependency"
	downstreamUpdateRollbackPlan  = "No target write has executed; future execution requires a new Proposal revision and owner executor"
)

// CreateResult 包含 Proposal 和是否命中已有幂等请求。
type CreateResult struct {
	Proposal domain.Proposal
	Replayed bool
}

// CreateProposal 校验并原子创建 ready_for_review Proposal 和 Revision。
func (s *Service) CreateProposal(ctx context.Context, command CreateCommand) (CreateResult, error) {
	return s.createFileProposal(ctx, createFileProposalCommand{
		WorkspaceID: command.WorkspaceID, TargetPath: command.TargetPath, TargetMode: domain.TargetModeReplace,
		IdempotencyKey: command.IdempotencyKey, BaseVersion: command.BaseHash, Content: command.Content,
		EvidenceSummary: command.EvidenceSummary, RiskLevel: command.RiskLevel, Risk: command.Risk, RollbackPlan: command.RollbackPlan,
	})
}

// CreateCreateOnlyFileProposal 为 Authoring 创建目标必须缺失的 file_patch Proposal。
func (s *Service) CreateCreateOnlyFileProposal(ctx context.Context, command CreateCreateOnlyFileProposalCommand) (CreateResult, error) {
	targetPath, err := domain.ValidateTargetPath(command.TargetPath)
	if err != nil || targetPath != command.TargetPath {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("proposal target path is invalid"))
	}
	absenceToken, err := domain.ComputeAbsenceToken(command.WorkspaceID, targetPath)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	return s.createFileProposal(ctx, createFileProposalCommand{
		WorkspaceID: command.WorkspaceID, TargetPath: targetPath, TargetMode: domain.TargetModeCreateOnly,
		IdempotencyKey: command.IdempotencyKey, BaseVersion: absenceToken, Content: command.Content,
		EvidenceSummary: command.EvidenceSummary, RiskLevel: command.RiskLevel, Risk: command.Risk, RollbackPlan: command.RollbackPlan,
	})
}

// CreateRestoreDocumentProposal rebinds current file/Git facts before persisting a typed restore revision.
func (s *Service) CreateRestoreDocumentProposal(ctx context.Context, command CreateRestoreDocumentCommand) (CreateResult, error) {
	repository, ok := s.repo.(domain.RestoreDocumentProposalRepository)
	lookup, lookupOK := s.repo.(ProposalCreateLookup)
	if !ok || !lookupOK {
		return CreateResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "DOCUMENT_RESTORE_PROPOSAL_REPOSITORY_UNAVAILABLE", false, errors.New("restore proposal repository is unavailable"))
	}
	targetPath, pathErr := domain.ValidateTargetPath(command.TargetPath)
	workspaceTargetErr := domain.ValidateWorkspaceTarget(command.WorkspaceID, command.TargetPath)
	restore, restoreErr := domain.ValidateRestoreDocument(command.Restore)
	idempotencyKey := strings.TrimSpace(command.IdempotencyKey)
	evidence, risk, rollback := strings.TrimSpace(command.EvidenceSummary), strings.TrimSpace(command.Risk), strings.TrimSpace(command.RollbackPlan)
	if pathErr != nil || workspaceTargetErr != nil || targetPath != command.TargetPath || restoreErr != nil || restore.WorkspaceID != command.WorkspaceID ||
		idempotencyKey == "" || idempotencyKey != command.IdempotencyKey || len(idempotencyKey) > 128 ||
		strings.TrimSpace(command.Content) == "" || evidence == "" || risk == "" || rollback == "" ||
		domain.ComputeContentHash([]byte(command.Content)) != restore.TargetContentHash {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "DOCUMENT_RESTORE_PROPOSAL_INVALID", false, domain.ErrRestoreDocumentInvalid)
	}
	requestHash, err := domain.ComputeRestoreDocumentRequestHash(
		command.WorkspaceID, restore, targetPath, command.Content, evidence,
		domain.ProposalRiskLevelHigh, risk, rollback,
	)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "DOCUMENT_RESTORE_PROPOSAL_INVALID", false, err)
	}
	if existing, found, lookupErr := lookup.FindProposalByIdempotencyKey(ctx, command.WorkspaceID, idempotencyKey); lookupErr != nil {
		return CreateResult{}, lookupErr
	} else if found {
		if !restoreProposalCreateRequestMatches(existing, restore, targetPath, command.Content, evidence, risk, rollback, requestHash, idempotencyKey) {
			return CreateResult{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		return CreateResult{Proposal: existing, Replayed: true}, nil
	}
	currentHash, err := s.targets.CurrentHash(ctx, command.WorkspaceID, targetPath)
	if err != nil {
		return CreateResult{}, err
	}
	if !strings.EqualFold(currentHash, restore.CurrentContentHash) {
		return CreateResult{}, foundation.NewError(foundation.ErrorVersionConflict, "DOCUMENT_RESTORE_STALE", false, &HashConflict{Expected: restore.CurrentContentHash, Current: strings.ToLower(currentHash)})
	}
	snapshot, err := s.git.CaptureApprovalSnapshot(ctx, command.WorkspaceID)
	if err != nil {
		return CreateResult{}, err
	}
	if !strings.EqualFold(snapshot.Head, restore.ExpectedHead) || snapshot.Branch == "" || !snapshot.Clean {
		return CreateResult{}, foundation.NewError(foundation.ErrorVersionConflict, "DOCUMENT_RESTORE_STALE", false, errors.New("restore git baseline changed"))
	}
	proposalID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	revisionID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	changeHash, err := domain.ComputeChangeHashForTarget(command.WorkspaceID, targetPath, domain.TargetModeReplace, restore.CurrentContentHash, command.Content)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "DOCUMENT_RESTORE_PROPOSAL_INVALID", false, err)
	}
	now := s.clock.Now()
	revision := domain.Revision{
		ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: targetPath,
		TargetMode: domain.TargetModeReplace, BaseHash: restore.CurrentContentHash, Content: command.Content,
		EvidenceSummary: evidence, Risk: risk, RollbackPlan: rollback, ChangeHash: changeHash,
		RestoreDocument: &restore, CreatedAt: now,
	}
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeRestoreDocument, revision); err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "DOCUMENT_RESTORE_PROPOSAL_INVALID", false, err)
	}
	proposal, err := repository.CreateRestoreDocumentProposal(ctx, domain.Proposal{
		ID: proposalID, WorkspaceID: command.WorkspaceID, Type: domain.ProposalTypeRestoreDocument,
		RiskLevel: domain.ProposalRiskLevelHigh, TargetPath: targetPath, IdempotencyKey: idempotencyKey,
		RequestHash: requestHash, Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: revision,
	})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Proposal: proposal, Replayed: proposal.ID != proposalID}, nil
}

func restoreProposalCreateRequestMatches(proposal domain.Proposal, restore domain.RestoreDocument, targetPath, content, evidence, risk, rollback, requestHash, idempotencyKey string) bool {
	return proposalType(proposal) == domain.ProposalTypeRestoreDocument && proposal.RestoreBindingMatches(restore) &&
		proposal.WorkspaceID == restore.WorkspaceID && proposal.TargetPath == targetPath && proposal.IdempotencyKey == idempotencyKey && proposal.RequestHash == requestHash &&
		proposal.RiskLevel == domain.ProposalRiskLevelHigh && proposal.Revision.TargetPath == targetPath &&
		proposal.Revision.BaseHash == restore.CurrentContentHash && proposal.Revision.Content == content &&
		proposal.Revision.EvidenceSummary == evidence && proposal.Revision.Risk == risk && proposal.Revision.RollbackPlan == rollback
}

type createFileProposalCommand struct {
	WorkspaceID     foundation.ID
	TargetPath      string
	TargetMode      domain.TargetMode
	IdempotencyKey  string
	BaseVersion     string
	Content         string
	EvidenceSummary string
	RiskLevel       domain.ProposalRiskLevel
	Risk            string
	RollbackPlan    string
}

func (s *Service) createFileProposal(ctx context.Context, command createFileProposalCommand) (CreateResult, error) {
	targetPath, pathErr := domain.ValidateTargetPath(command.TargetPath)
	workspaceTargetErr := domain.ValidateWorkspaceTarget(command.WorkspaceID, targetPath)
	targetMode, modeErr := domain.ValidateTargetMode(command.TargetMode)
	idempotencyKey := strings.TrimSpace(command.IdempotencyKey)
	evidenceSummary := strings.TrimSpace(command.EvidenceSummary)
	rollbackPlan := strings.TrimSpace(command.RollbackPlan)
	if command.WorkspaceID == "" || pathErr != nil || workspaceTargetErr != nil || modeErr != nil || domain.ValidateTargetBaseVersion(command.WorkspaceID, targetPath, targetMode, command.BaseVersion) != nil || idempotencyKey == "" || len(idempotencyKey) > 128 || strings.TrimSpace(command.Content) == "" || evidenceSummary == "" || strings.TrimSpace(command.Risk) == "" || rollbackPlan == "" {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, errors.New("proposal fields are invalid"))
	}
	risk := strings.TrimSpace(command.Risk)
	riskLevel, riskErr := proposalRiskLevelForCreate(command.RiskLevel)
	if riskErr != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, riskErr)
	}
	baseHash := command.BaseVersion
	if targetMode == domain.TargetModeReplace {
		baseHash = strings.ToLower(baseHash)
	}
	changeHash, changeErr := domain.ComputeChangeHashForTarget(command.WorkspaceID, targetPath, targetMode, baseHash, command.Content)
	if changeErr != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, changeErr)
	}
	requestHash, err := domain.ComputeRequestHashWithTargetMode(command.WorkspaceID, targetPath, targetMode, baseHash, command.Content, evidenceSummary, riskLevel, risk, rollbackPlan)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	lookup, ok := s.repo.(ProposalCreateLookup)
	if !ok {
		return CreateResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_CREATE_LOOKUP_UNAVAILABLE", false, errors.New("proposal create lookup is unavailable"))
	}
	existing, found, lookupErr := lookup.FindProposalByIdempotencyKey(ctx, command.WorkspaceID, idempotencyKey)
	if lookupErr != nil {
		return CreateResult{}, lookupErr
	}
	if found {
		if !fileProposalCreateRequestMatches(existing, command.WorkspaceID, targetPath, targetMode, baseHash, command.Content, evidenceSummary, riskLevel, risk, rollbackPlan, changeHash, requestHash, idempotencyKey) {
			return CreateResult{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		return CreateResult{Proposal: existing, Replayed: true}, nil
	}
	var baseSnapshotContent *string
	if targetMode == domain.TargetModeReplace && len([]byte(command.Content)) <= domain.ProposalRevisionMaxBytes {
		reader, readerOK := s.targets.(CurrentContentReader)
		if !readerOK {
			return CreateResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_CURRENT_CONTENT_UNAVAILABLE", true, errors.New("current content reader is unavailable"))
		}
		content, currentHash, readErr := reader.CurrentContent(ctx, command.WorkspaceID, targetPath, MaxProposalCurrentContentBytes)
		if readErr != nil {
			var classified *foundation.Error
			if !errors.As(readErr, &classified) || classified.Code != "PROPOSAL_CURRENT_CONTENT_TOO_LARGE" {
				return CreateResult{}, readErr
			}
		} else {
			currentHash = strings.ToLower(currentHash)
			if len(content) > int(domain.ProposalRevisionMaxBytes) || !domain.ValidHash(currentHash) || domain.RawContentHash(string(content)) != currentHash {
				return CreateResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_CURRENT_CONTENT_HASH_INVALID", false, errors.New("current content hash is inconsistent"))
			}
			if currentHash != baseHash {
				return CreateResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: baseHash, Current: currentHash})
			}
			snapshot := string(content)
			baseSnapshotContent = &snapshot
		}
	}
	if targetMode == domain.TargetModeCreateOnly {
		reader, ok := s.targets.(CreateOnlyTargetReader)
		if !ok {
			return CreateResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "CREATE_ONLY_TARGET_READER_UNAVAILABLE", false, errors.New("create-only target reader is unavailable"))
		}
		if err := reader.EnsureTargetAbsent(ctx, command.WorkspaceID, targetPath, baseHash); err != nil {
			return CreateResult{}, err
		}
	}
	proposalID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	revisionID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	now := s.clock.Now()
	revision := domain.Revision{
		ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: targetPath,
		TargetMode: targetMode,
		BaseHash:   baseHash, Content: command.Content, EvidenceSummary: evidenceSummary,
		Risk: risk, RollbackPlan: rollbackPlan,
		ChangeHash: changeHash, CreatedAt: now,
	}
	if baseSnapshotContent != nil {
		revision.BaseSnapshot = &domain.RevisionBaseSnapshot{
			ProposalID: proposalID, RevisionID: revisionID, BaseHash: baseHash, Content: *baseSnapshotContent,
			ByteSize: len([]byte(*baseSnapshotContent)), SchemaVersion: domain.ProposalRevisionBaseSnapshotSchemaVersion, CreatedAt: now,
		}
	}
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeFilePatch, revision); err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_INVALID", false, err)
	}
	proposal, err := s.repo.CreateProposal(ctx, domain.Proposal{
		ID: proposalID, WorkspaceID: command.WorkspaceID, Type: domain.ProposalTypeFilePatch, TargetPath: targetPath,
		RiskLevel:      riskLevel,
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
		Status:         domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now, Revision: revision,
	})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Proposal: proposal, Replayed: proposal.ID != proposalID}, nil
}

func fileProposalCreateRequestMatches(
	proposal domain.Proposal,
	workspaceID foundation.ID,
	targetPath string,
	targetMode domain.TargetMode,
	baseHash, content, evidenceSummary string,
	riskLevel domain.ProposalRiskLevel,
	risk, rollbackPlan, changeHash, requestHash, idempotencyKey string,
) bool {
	return proposalType(proposal) == domain.ProposalTypeFilePatch &&
		proposal.WorkspaceID == workspaceID && proposal.RiskLevel == riskLevel &&
		proposal.TargetPath == targetPath && proposal.IdempotencyKey == idempotencyKey && proposal.RequestHash == requestHash &&
		proposal.Revision.TargetPath == targetPath && domain.NormalizeTargetMode(proposal.Revision.TargetMode) == targetMode &&
		proposal.Revision.BaseHash == baseHash && proposal.Revision.Content == content &&
		proposal.Revision.EvidenceSummary == evidenceSummary && proposal.Revision.Risk == risk &&
		proposal.Revision.RollbackPlan == rollbackPlan && proposal.Revision.ChangeHash == changeHash
}

// CreateKnowledgeChangeProposal 校验并创建结构化 `knowledge_change` Proposal。
func (s *Service) CreateKnowledgeChangeProposal(ctx context.Context, command CreateKnowledgeChangeCommand) (CreateResult, error) {
	repository, ok := s.repo.(domain.KnowledgeChangeProposalRepository)
	if !ok {
		return CreateResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "KNOWLEDGE_CHANGE_PROPOSAL_REPOSITORY_UNAVAILABLE", false, errors.New("knowledge change proposal repository is unavailable"))
	}
	if command.WorkspaceID == "" || strings.TrimSpace(command.IdempotencyKey) == "" || len(strings.TrimSpace(command.IdempotencyKey)) > 128 || strings.TrimSpace(command.Risk) == "" || strings.TrimSpace(command.RollbackPlan) == "" {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, errors.New("knowledge change proposal fields are invalid"))
	}
	canonicalChange, err := domain.ValidateKnowledgeChange(command.KnowledgeChange)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, err)
	}
	proposalID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	revisionID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	now := s.clock.Now()
	risk := strings.TrimSpace(command.Risk)
	riskLevel, err := proposalRiskLevelForCreate(command.RiskLevel)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, err)
	}
	if _, err := domain.ValidateProposalRiskLevelForType(domain.ProposalTypeKnowledgeChange, riskLevel); err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, err)
	}
	rollbackPlan := strings.TrimSpace(command.RollbackPlan)
	changeHash, err := domain.ComputeKnowledgeChangeHash(canonicalChange, risk, rollbackPlan)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, err)
	}
	requestHash, err := domain.ComputeKnowledgeChangeRequestHashWithRiskLevel(command.WorkspaceID, canonicalChange, riskLevel, risk, rollbackPlan)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, err)
	}
	revision := domain.Revision{
		ID: revisionID, ProposalID: proposalID, RevisionNo: 1,
		Risk: risk, RollbackPlan: rollbackPlan,
		ChangeHash: changeHash, KnowledgeChange: &canonicalChange, CreatedAt: now,
	}
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeKnowledgeChange, revision); err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "KNOWLEDGE_CHANGE_PROPOSAL_INVALID", false, err)
	}
	proposal, err := repository.CreateKnowledgeChangeProposal(ctx, domain.Proposal{
		ID: proposalID, WorkspaceID: command.WorkspaceID, Type: domain.ProposalTypeKnowledgeChange,
		RiskLevel:      riskLevel,
		IdempotencyKey: strings.TrimSpace(command.IdempotencyKey),
		RequestHash:    requestHash,
		Status:         domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now, Revision: revision,
	})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Proposal: proposal, Replayed: !proposal.CreatedAt.Equal(now)}, nil
}

// CreatePublishArtifactProposal creates a typed publish_artifact Proposal without writing a Document.
func (s *Service) CreatePublishArtifactProposal(ctx context.Context, command CreatePublishArtifactCommand) (CreateResult, error) {
	repository, ok := s.repo.(domain.PublishArtifactProposalRepository)
	if !ok {
		return CreateResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "PUBLISH_ARTIFACT_PROPOSAL_REPOSITORY_UNAVAILABLE", false, errors.New("publish artifact proposal repository is unavailable"))
	}
	if command.WorkspaceID == "" || strings.TrimSpace(command.IdempotencyKey) == "" || len(strings.TrimSpace(command.IdempotencyKey)) > 128 || strings.TrimSpace(command.Risk) == "" || strings.TrimSpace(command.RollbackPlan) == "" {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PUBLISH_ARTIFACT_PROPOSAL_INVALID", false, errors.New("publish artifact proposal fields are invalid"))
	}
	publication, err := domain.ValidatePublishArtifact(command.Publication)
	if err != nil || publication.WorkspaceID != command.WorkspaceID {
		if err == nil {
			err = errors.New("publication workspace differs from command workspace")
		}
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PUBLISH_ARTIFACT_PROPOSAL_INVALID", false, err)
	}
	riskLevel, err := proposalRiskLevelForCreate(command.RiskLevel)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PUBLISH_ARTIFACT_PROPOSAL_INVALID", false, err)
	}
	if _, err := domain.ValidateProposalRiskLevelForType(domain.ProposalTypePublishArtifact, riskLevel); err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PUBLISH_ARTIFACT_PROPOSAL_INVALID", false, err)
	}
	proposalID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	revisionID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	now := s.clock.Now()
	risk, rollbackPlan := strings.TrimSpace(command.Risk), strings.TrimSpace(command.RollbackPlan)
	changeHash, err := domain.ComputePublishArtifactHash(publication, risk, rollbackPlan)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PUBLISH_ARTIFACT_PROPOSAL_INVALID", false, err)
	}
	requestHash, err := domain.ComputePublishArtifactRequestHash(command.WorkspaceID, publication, riskLevel, risk, rollbackPlan)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PUBLISH_ARTIFACT_PROPOSAL_INVALID", false, err)
	}
	revision := domain.Revision{ID: revisionID, ProposalID: proposalID, RevisionNo: 1, Risk: risk, RollbackPlan: rollbackPlan, ChangeHash: changeHash, PublishArtifact: &publication, CreatedAt: now}
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypePublishArtifact, revision); err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "PUBLISH_ARTIFACT_PROPOSAL_INVALID", false, err)
	}
	proposal, err := repository.CreatePublishArtifactProposal(ctx, domain.Proposal{
		ID: proposalID, WorkspaceID: command.WorkspaceID, Type: domain.ProposalTypePublishArtifact, RiskLevel: riskLevel,
		IdempotencyKey: strings.TrimSpace(command.IdempotencyKey), RequestHash: requestHash,
		Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now, Revision: revision,
	})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Proposal: proposal, Replayed: !proposal.CreatedAt.Equal(now)}, nil
}

// CreateDownstreamUpdateProposal 创建 approval-only Proposal，不授予任何目标写回能力。
func (s *Service) CreateDownstreamUpdateProposal(ctx context.Context, command CreateDownstreamUpdateCommand) (CreateResult, error) {
	repository, repositoryOK := s.repo.(domain.DownstreamUpdateProposalRepository)
	factory, factoryOK := s.repo.(DownstreamUpdateFactory)
	lookup, lookupOK := s.repo.(ProposalCreateLookup)
	if !repositoryOK || !factoryOK || !lookupOK {
		return CreateResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "KNOWLEDGE_IMPACT_UNAVAILABLE", false, errors.New("downstream update proposal dependencies are unavailable"))
	}
	workspaceID, workspaceErr := foundation.ParseID(string(command.WorkspaceID))
	reportID, reportErr := foundation.ParseID(string(command.ReportID))
	targetID, targetErr := foundation.ParseID(string(command.TargetID))
	idempotencyKey := strings.TrimSpace(command.IdempotencyKey)
	if workspaceErr != nil || reportErr != nil || targetErr != nil || idempotencyKey == "" || len(idempotencyKey) > 128 ||
		command.TargetType != knowledge.ImpactObjectArtifact && command.TargetType != knowledge.ImpactObjectReviewCard ||
		command.TargetType == knowledge.ImpactObjectArtifact && command.Action != knowledge.ImpactActionRegenerateArtifact ||
		command.TargetType == knowledge.ImpactObjectReviewCard && command.Action != knowledge.ImpactActionRevalidateReviewCard {
		return CreateResult{}, foundation.NewError(foundation.ErrorInvalidInput, "DOWNSTREAM_UPDATE_PROPOSAL_INVALID", false, errors.New("downstream update selection is invalid"))
	}
	command.WorkspaceID, command.ReportID, command.TargetID = workspaceID, reportID, targetID
	if existing, found, err := lookup.FindProposalByIdempotencyKey(ctx, workspaceID, idempotencyKey); err != nil {
		return CreateResult{}, err
	} else if found {
		update := existing.Revision.DownstreamUpdate
		if proposalType(existing) != domain.ProposalTypeDownstreamUpdate || update == nil ||
			update.WorkspaceID != workspaceID || update.ReportID != reportID || update.TargetType != command.TargetType ||
			update.TargetID != targetID || update.Action != command.Action {
			return CreateResult{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		return CreateResult{Proposal: existing, Replayed: true}, nil
	}
	update, err := factory.BuildDownstreamUpdate(ctx, workspaceID, reportID, command.TargetType, targetID, command.Action)
	if err != nil {
		return CreateResult{}, err
	}
	canonical, err := domain.ValidateDownstreamUpdate(update)
	if err != nil || canonical.WorkspaceID != workspaceID || canonical.ReportID != reportID || canonical.TargetType != command.TargetType || canonical.TargetID != targetID || canonical.Action != command.Action {
		if err == nil {
			err = errors.New("downstream update factory returned a different selection")
		}
		return CreateResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "DOWNSTREAM_UPDATE_PROPOSAL_INVALID", false, err)
	}
	proposalID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	revisionID, err := s.ids.New()
	if err != nil {
		return CreateResult{}, err
	}
	changeHash, err := domain.ComputeDownstreamUpdateHash(canonical, downstreamUpdateRiskNarrative, downstreamUpdateRollbackPlan)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "DOWNSTREAM_UPDATE_PROPOSAL_INVALID", false, err)
	}
	requestHash, err := domain.ComputeDownstreamUpdateRequestHash(
		workspaceID, canonical, domain.ProposalRiskLevelHigh, downstreamUpdateRiskNarrative, downstreamUpdateRollbackPlan,
	)
	if err != nil {
		return CreateResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "DOWNSTREAM_UPDATE_PROPOSAL_INVALID", false, err)
	}
	now := s.clock.Now()
	revision := domain.Revision{
		ID: revisionID, ProposalID: proposalID, RevisionNo: 1,
		Risk: downstreamUpdateRiskNarrative, RollbackPlan: downstreamUpdateRollbackPlan,
		ChangeHash: changeHash, DownstreamUpdate: &canonical, CreatedAt: now,
	}
	proposal, err := repository.CreateDownstreamUpdateProposal(ctx, domain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, Type: domain.ProposalTypeDownstreamUpdate,
		RiskLevel: domain.ProposalRiskLevelHigh, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now, Revision: revision,
	})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Proposal: proposal, Replayed: proposal.ID != proposalID}, nil
}

// GetProposal 返回 Proposal 当前 Revision 与已有审批决定。
func (s *Service) GetProposal(ctx context.Context, proposalID foundation.ID) (domain.Proposal, error) {
	if proposalID == "" {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_ID_INVALID", false, errors.New("proposal id is required"))
	}
	return s.repo.GetProposal(ctx, proposalID)
}

// GetProposalCurrentContent 安全读取 file_patch Proposal 的当前目标正文，不执行写入。
func (s *Service) GetProposalCurrentContent(ctx context.Context, proposalID foundation.ID) (ProposalCurrentContent, error) {
	if proposalID == "" {
		return ProposalCurrentContent{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_ID_INVALID", false, errors.New("proposal id is required"))
	}
	proposal, err := s.repo.GetProposal(ctx, proposalID)
	if err != nil {
		return ProposalCurrentContent{}, err
	}
	if !domain.ProposalSupportsFileWriteback(proposal.Type) {
		return ProposalCurrentContent{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_CURRENT_CONTENT_UNSUPPORTED", false, errors.New("current content is only available for file patch proposals"))
	}
	if err := domain.ValidateWorkspaceTarget(proposal.WorkspaceID, proposal.TargetPath); err != nil {
		return ProposalCurrentContent{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_TARGET_INVALID", false, err)
	}
	targetMode := domain.NormalizeTargetMode(proposal.Revision.TargetMode)
	if targetMode == domain.TargetModeCreateOnly {
		currentHash, verifyErr := s.verifyProposalTarget(ctx, proposal)
		if verifyErr != nil {
			return ProposalCurrentContent{}, verifyErr
		}
		return ProposalCurrentContent{
			ProposalID: proposal.ID, WorkspaceID: proposal.WorkspaceID, TargetPath: proposal.TargetPath,
			TargetMode: targetMode, Content: "", CurrentHash: currentHash, BaseHash: proposal.Revision.BaseHash,
			BaseHashMatch: currentHash == proposal.Revision.BaseHash,
		}, nil
	}
	reader, ok := s.targets.(CurrentContentReader)
	if !ok {
		return ProposalCurrentContent{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_CURRENT_CONTENT_UNAVAILABLE", true, errors.New("current content reader is unavailable"))
	}
	content, currentHash, err := reader.CurrentContent(ctx, proposal.WorkspaceID, proposal.TargetPath, MaxProposalCurrentContentBytes)
	if err != nil {
		return ProposalCurrentContent{}, err
	}
	return ProposalCurrentContent{
		ProposalID: proposal.ID, WorkspaceID: proposal.WorkspaceID, TargetPath: proposal.TargetPath, Content: string(content),
		TargetMode: targetMode, CurrentHash: currentHash, BaseHash: proposal.Revision.BaseHash,
		BaseHashMatch: currentHash == proposal.Revision.BaseHash,
	}, nil
}

// ListProposals 返回按更新时间和 ID 倒序排列的 Proposal 摘要页。
func (s *Service) ListProposals(ctx context.Context, query domain.ProposalListQuery) ([]domain.ProposalListItem, bool, error) {
	if query.WorkspaceID == "" || query.Limit < 1 || query.Limit > 100 {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_LIST_INVALID", false, errors.New("workspace and bounded limit are required"))
	}
	repository, ok := s.repo.(domain.ProposalListRepository)
	if !ok {
		return nil, false, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_LIST_UNAVAILABLE", true, errors.New("proposal list repository is unavailable"))
	}
	items, hasMore, err := repository.ListProposals(ctx, query)
	if err != nil {
		return nil, false, err
	}
	for index := range items {
		items[index].RevisionCapability = domain.GateProposalRevisionMergeEngine(items[index].RevisionCapability, s.RevisionMergeAvailable())
	}
	return items, hasMore, nil
}

// DecideProposal 由服务端生成 Approval ID，并绑定 Revision 与 Change Hash。
func (s *Service) DecideProposal(ctx context.Context, proposalID, revisionID foundation.ID, changeHash string, decision domain.Decision) (domain.Approval, error) {
	if s.dispatcher != nil {
		result, err := s.DecideProposalWithDispatch(ctx, proposalID, revisionID, changeHash, decision)
		return result.Approval, err
	}
	return s.decideProposalLegacy(ctx, proposalID, revisionID, changeHash, decision)
}

func (s *Service) decideProposalLegacy(ctx context.Context, proposalID, revisionID foundation.ID, changeHash string, decision domain.Decision) (domain.Approval, error) {
	if proposalID == "" || revisionID == "" || !domain.ValidHash(changeHash) || decision != domain.DecisionApproved && decision != domain.DecisionRejected {
		return domain.Approval{}, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_INVALID", false, errors.New("approval fields are invalid"))
	}
	proposal, err := s.repo.GetProposal(ctx, proposalID)
	if err != nil {
		return domain.Approval{}, err
	}
	if proposal.Revision.ID != revisionID || proposal.Revision.ChangeHash != strings.ToLower(changeHash) {
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_CONFLICT", false, errors.New("approval is not bound to requested revision"))
	}
	if err := validateProposalForApproval(proposal); err != nil {
		return domain.Approval{}, err
	}
	if proposalType(proposal) == domain.ProposalTypeKnowledgeChange && decision == domain.DecisionApproved {
		approval, _, approvalErr := s.approveKnowledgeChangeProposal(ctx, proposal, revisionID, changeHash)
		return approval, approvalErr
	}
	if proposal.Approval != nil {
		if proposal.Approval.RevisionID == revisionID && proposal.Approval.ChangeHash == strings.ToLower(changeHash) && proposal.Approval.Decision == decision {
			return *proposal.Approval, nil
		}
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_DECISION_CONFLICT", false, errors.New("proposal already has a different approval decision"))
	}
	var approvedGitHead *string
	if proposal.Status == domain.StatusReady && decision == domain.DecisionApproved && domain.ProposalSupportsFileWriteback(proposal.Type) {
		currentHash, readErr := s.verifyProposalTarget(ctx, proposal)
		if readErr != nil {
			return s.rejectUnavailableTarget(ctx, proposal, readErr)
		}
		if domain.NormalizeTargetMode(proposal.Revision.TargetMode) == domain.TargetModeReplace && strings.ToLower(currentHash) != proposal.Revision.BaseHash {
			if markErr := s.markNeedsRevision(ctx, proposal); markErr != nil {
				return domain.Approval{}, markErr
			}
			return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: proposal.Revision.BaseHash, Current: strings.ToLower(currentHash)})
		}
		snapshot, inspectErr := s.git.CaptureApprovalSnapshot(ctx, proposal.WorkspaceID)
		if inspectErr != nil {
			return domain.Approval{}, inspectErr
		}
		if bindingErr := domain.ValidateGitSnapshotBinding(proposal.WorkspaceID, snapshot.Head, snapshot); bindingErr != nil {
			return domain.Approval{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_GIT_SNAPSHOT_INVALID", false, bindingErr)
		}
		if proposalType(proposal) == domain.ProposalTypeRestoreDocument && (proposal.Revision.RestoreDocument == nil || !strings.EqualFold(snapshot.Head, proposal.Revision.RestoreDocument.ExpectedHead)) {
			return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "DOCUMENT_RESTORE_STALE", false, errors.New("restore git baseline changed before approval"))
		}
		if gitErr := s.verifyCreateOnlyGitTarget(ctx, proposal, snapshot.Head); gitErr != nil {
			return domain.Approval{}, gitErr
		}
		head := strings.ToLower(snapshot.Head)
		approvedGitHead = &head
	}
	approvalID, err := s.ids.New()
	if err != nil {
		return domain.Approval{}, err
	}
	approval, err := s.repo.Approve(ctx, domain.Approval{
		ID: approvalID, ProposalID: proposalID, RevisionID: revisionID,
		ChangeHash: strings.ToLower(changeHash), Decision: decision, ApprovedGitHead: approvedGitHead, DecidedAt: s.clock.Now(),
	})
	if err != nil {
		return domain.Approval{}, err
	}
	return approval, nil
}

// DecideProposalWithDispatch 在外部文件/Git 安全门后，通过单一 UoW 保存 Approval 并投递唯一 Safe Writeback Workflow。
// 只有完整 Approval→Run 绑定重放可以跳过可变文件和 Git 事实读取。
func (s *Service) DecideProposalWithDispatch(ctx context.Context, proposalID, revisionID foundation.ID, changeHash string, decision domain.Decision) (ApprovalDecisionResult, error) {
	if s == nil || isNilApprovalDispatcher(s.dispatcher) {
		return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "APPROVAL_DISPATCHER_MISSING", false, errors.New("approval dispatcher is missing"))
	}
	if proposalID == "" || revisionID == "" || !domain.ValidHash(changeHash) || decision != domain.DecisionApproved && decision != domain.DecisionRejected {
		return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorInvalidInput, "APPROVAL_INVALID", false, errors.New("approval fields are invalid"))
	}
	changeHash = strings.ToLower(changeHash)
	proposal, err := s.repo.GetProposal(ctx, proposalID)
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	if proposal.Revision.ID != revisionID || proposal.Revision.ChangeHash != changeHash {
		return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_CONFLICT", false, errors.New("approval is not bound to requested revision"))
	}
	if err := validateProposalForApproval(proposal); err != nil {
		return ApprovalDecisionResult{}, err
	}
	if proposalType(proposal) == domain.ProposalTypeKnowledgeChange {
		if decision == domain.DecisionApproved {
			approval, replayed, approvalErr := s.approveKnowledgeChangeProposal(ctx, proposal, revisionID, changeHash)
			if approvalErr != nil {
				return ApprovalDecisionResult{Approval: approval, Replayed: replayed}, approvalErr
			}
			return ApprovalDecisionResult{Approval: approval, Replayed: replayed}, nil
		}
		approval, approvalErr := s.decideProposalLegacy(ctx, proposalID, revisionID, changeHash, decision)
		if approvalErr != nil {
			return ApprovalDecisionResult{}, approvalErr
		}
		return ApprovalDecisionResult{Approval: approval, Replayed: proposal.Approval != nil}, nil
	}
	if proposalType(proposal) == domain.ProposalTypePublishArtifact || proposalType(proposal) == domain.ProposalTypeDownstreamUpdate {
		approval, approvalErr := s.decideProposalLegacy(ctx, proposalID, revisionID, changeHash, decision)
		if approvalErr != nil {
			return ApprovalDecisionResult{}, approvalErr
		}
		return ApprovalDecisionResult{Approval: approval, Replayed: proposal.Approval != nil}, nil
	}

	approval := domain.Approval{ProposalID: proposalID, RevisionID: revisionID, ChangeHash: changeHash, Decision: decision}
	if proposal.Approval != nil {
		if proposal.Approval.RevisionID != revisionID || !strings.EqualFold(proposal.Approval.ChangeHash, changeHash) || proposal.Approval.Decision != decision {
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_DECISION_CONFLICT", false, errors.New("proposal already has a different approval decision"))
		}
		approval = *proposal.Approval
		if decision == domain.DecisionRejected || proposal.WorkflowRunID != nil {
			return s.dispatchApproval(ctx, ApprovalDispatchCommand{WorkspaceID: proposal.WorkspaceID, Approval: approval})
		}
		if approval.ApprovedGitHead == nil || !domain.ValidGitHead(*approval.ApprovedGitHead) {
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_GIT_BASELINE_MISSING", false, errors.New("historical approval has no valid git baseline"))
		}
	} else {
		approvalID, idErr := s.ids.New()
		if idErr != nil {
			return ApprovalDecisionResult{}, idErr
		}
		approval.ID = approvalID
		approval.DecidedAt = s.clock.Now()
	}

	command := ApprovalDispatchCommand{WorkspaceID: proposal.WorkspaceID, Approval: approval}
	if decision == domain.DecisionApproved {
		currentHash, readErr := s.verifyProposalTarget(ctx, proposal)
		if readErr != nil {
			_, rejectionErr := s.rejectUnavailableTarget(ctx, proposal, readErr)
			return ApprovalDecisionResult{}, rejectionErr
		}
		currentHash = strings.ToLower(currentHash)
		if domain.NormalizeTargetMode(proposal.Revision.TargetMode) == domain.TargetModeReplace && currentHash != proposal.Revision.BaseHash {
			if markErr := s.markNeedsRevision(ctx, proposal); markErr != nil {
				return ApprovalDecisionResult{}, markErr
			}
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: proposal.Revision.BaseHash, Current: currentHash})
		}
		snapshot, inspectErr := s.git.CaptureApprovalSnapshot(ctx, proposal.WorkspaceID)
		if inspectErr != nil {
			return ApprovalDecisionResult{}, inspectErr
		}
		if bindingErr := domain.ValidateGitSnapshotBinding(proposal.WorkspaceID, snapshot.Head, snapshot); bindingErr != nil {
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "APPROVAL_GIT_SNAPSHOT_INVALID", false, bindingErr)
		}
		if proposalType(proposal) == domain.ProposalTypeRestoreDocument && (proposal.Revision.RestoreDocument == nil || !strings.EqualFold(snapshot.Head, proposal.Revision.RestoreDocument.ExpectedHead)) {
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "DOCUMENT_RESTORE_STALE", false, errors.New("restore git baseline changed before approval"))
		}
		if gitErr := s.verifyCreateOnlyGitTarget(ctx, proposal, snapshot.Head); gitErr != nil {
			return ApprovalDecisionResult{}, gitErr
		}
		head := strings.ToLower(snapshot.Head)
		if approval.ApprovedGitHead != nil && !strings.EqualFold(*approval.ApprovedGitHead, head) {
			return ApprovalDecisionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "APPROVAL_GIT_HEAD_CONFLICT", false, errors.New("git head differs from approved baseline"))
		}
		approval.ApprovedGitHead = &head
		command.Approval = approval
		command.ObservedBaseHash = currentHash
		command.ObservedGitHead = head
	}
	return s.dispatchApproval(ctx, command)
}

func (s *Service) approveKnowledgeChangeProposal(ctx context.Context, proposal domain.Proposal, revisionID foundation.ID, changeHash string) (domain.Approval, bool, error) {
	if isNilKnowledgeRelationApprovalApplier(s.knowledgeApprovalApplier) {
		return domain.Approval{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, "KNOWLEDGE_RELATION_APPROVAL_UOW_UNAVAILABLE", false, errors.New("knowledge relation approval UoW is not configured"))
	}
	requested := domain.Approval{
		ProposalID: proposal.ID, RevisionID: revisionID,
		ChangeHash: strings.ToLower(changeHash), Decision: domain.DecisionApproved, DecidedAt: s.clock.Now(),
	}
	if proposal.Approval != nil && proposal.Approval.ID != "" {
		// Exact replays must not depend on a fresh ID or clock value. The
		// persisted Approval is the only authoritative identity after response
		// loss or a previously committed atomic apply.
		requested = *proposal.Approval
	} else {
		approvalID, err := s.ids.New()
		if err != nil {
			return domain.Approval{}, false, err
		}
		requested.ID = approvalID
	}
	approval, result, err := s.knowledgeApprovalApplier.ApproveAndApplyRelation(ctx, requested)
	if err != nil {
		return approval, proposal.Approval != nil, err
	}
	if err := validateApprovedKnowledgeRelationResult(proposal, approval, result); err != nil {
		return domain.Approval{}, false, err
	}
	return approval, proposal.Approval != nil || result.Replayed, nil
}

func (s *Service) dispatchApproval(ctx context.Context, command ApprovalDispatchCommand) (ApprovalDecisionResult, error) {
	result, err := s.dispatcher.DecideAndDispatch(ctx, command)
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	if err := validateApprovalDispatchResult(command, result); err != nil {
		return ApprovalDecisionResult{}, err
	}
	return decisionResultFromDispatch(result), nil
}

func (s *Service) rejectUnavailableTarget(ctx context.Context, proposal domain.Proposal, readErr error) (domain.Approval, error) {
	var unavailable *domain.TargetUnavailableError
	if !errors.As(readErr, &unavailable) {
		return domain.Approval{}, readErr
	}
	if markErr := s.markNeedsRevision(ctx, proposal); markErr != nil {
		return domain.Approval{}, markErr
	}
	return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_UNAVAILABLE", false, unavailable)
}

// ApplyPreflightResult 仅表示服务端当前检查通过，不代表已写回或已签发写权限。
type ApplyPreflightResult struct {
	ProposalID foundation.ID
	RevisionID foundation.ID
	ChangeHash string
	// TargetMode 绑定本次检查使用的目标存在性契约。
	TargetMode domain.TargetMode
	BaseHash   string
}

// HashConflict 提供不含绝对路径的版本冲突详情。
type HashConflict struct {
	Expected string
	Current  string
}

func (e *HashConflict) Error() string { return "target base hash changed" }

// CheckApplyPreflight 从服务端读取真实目标哈希，并校验审批绑定和基线。
// Safe Writeback seam 在真正写入前仍必须再次执行同等校验和 Git 检查。
func (s *Service) CheckApplyPreflight(ctx context.Context, proposalID, revisionID foundation.ID, approvedChangeHash string) (ApplyPreflightResult, error) {
	if proposalID == "" || revisionID == "" || !domain.ValidHash(approvedChangeHash) {
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorInvalidInput, "APPLY_PREFLIGHT_INVALID", false, errors.New("apply preflight fields are invalid"))
	}
	proposal, err := s.repo.GetProposal(ctx, proposalID)
	if err != nil {
		return ApplyPreflightResult{}, err
	}
	if err := requireFilePatchProposal(proposal, "APPLY_PREFLIGHT_PROPOSAL_TYPE_UNSUPPORTED"); err != nil {
		return ApplyPreflightResult{}, err
	}
	if proposal.Status != domain.StatusApproved || proposal.Approval == nil || proposal.Approval.Decision != domain.DecisionApproved {
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "PROPOSAL_NOT_APPROVED", false, errors.New("proposal has no approved decision"))
	}
	if proposal.Revision.ID != revisionID || proposal.Approval.RevisionID != revisionID {
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_CONFLICT", false, errors.New("approval is not bound to requested revision"))
	}
	approvedChangeHash = strings.ToLower(approvedChangeHash)
	if proposal.Revision.ChangeHash != approvedChangeHash || proposal.Approval.ChangeHash != approvedChangeHash {
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CHANGE_HASH_MISMATCH", false, errors.New("approved hash does not match revision"))
	}
	if !proposalChangeHashValid(proposal) {
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CHANGE_HASH_INVALID", false, errors.New("revision change hash is not reproducible"))
	}
	currentBaseHash, err := s.verifyProposalSafety(ctx, proposal)
	if err != nil {
		var unavailable *domain.TargetUnavailableError
		if errors.As(err, &unavailable) {
			if markErr := s.markNeedsRevision(ctx, proposal); markErr != nil {
				return ApplyPreflightResult{}, markErr
			}
			return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_UNAVAILABLE", false, unavailable)
		}
		return ApplyPreflightResult{}, err
	}
	currentBaseHash = strings.ToLower(currentBaseHash)
	if domain.NormalizeTargetMode(proposal.Revision.TargetMode) == domain.TargetModeReplace && proposal.Revision.BaseHash != currentBaseHash {
		if markErr := s.markNeedsRevision(ctx, proposal); markErr != nil {
			return ApplyPreflightResult{}, markErr
		}
		return ApplyPreflightResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: proposal.Revision.BaseHash, Current: currentBaseHash})
	}
	return ApplyPreflightResult{
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, TargetMode: domain.NormalizeTargetMode(proposal.Revision.TargetMode),
		BaseHash: proposal.Revision.BaseHash,
	}, nil
}

// IssueWriteAuthorization 在批准事实和当前目标版本一致时签发服务端短期写权限。
// 返回的 Credential 只在首次签发时出现，调用方不得写入日志、模型上下文或 API 响应。
func (s *Service) IssueWriteAuthorization(ctx context.Context, command domain.AuthorizationIssue) (domain.AuthorizationIssueResult, error) {
	authorizations, ok := s.repo.(domain.AuthorizationRepository)
	if !ok {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITE_AUTHORIZATION_REPOSITORY_UNAVAILABLE", false, errors.New("authorization repository is unavailable"))
	}
	if err := domain.ValidateAuthorizationIssue(command, MaxWriteAuthorizationTTL); err != nil {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_INVALID", false, err)
	}
	proposal, err := s.repo.GetProposal(ctx, command.ProposalID)
	if err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	if err := requireFilePatchProposal(proposal, "WRITE_AUTHORIZATION_PROPOSAL_TYPE_UNSUPPORTED"); err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	if proposal.WorkspaceID != command.WorkspaceID || proposal.Revision.ID != command.RevisionID || proposal.Approval == nil || proposal.Approval.ID != command.ApprovalID || proposal.Status != domain.StatusApproved || proposal.Approval.Decision != domain.DecisionApproved {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED", false, errors.New("proposal approval binding is not valid"))
	}
	if strings.TrimSpace(command.Scope) != domain.ExpectedAuthorizationScopeForTarget(proposal.Revision.TargetPath, proposal.Revision.TargetMode) {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_SCOPE_INVALID", false, errors.New("authorization scope is broader than the approved target"))
	}
	if err := domain.ValidateToolBinding(command.ToolName, command.Capability); err != nil {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_INVALID", false, err)
	}
	if proposal.Approval.ChangeHash != proposal.Revision.ChangeHash || !proposalChangeHashValid(proposal) {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WRITE_AUTHORIZATION_CHANGE_HASH_INVALID", false, errors.New("approved change hash is not reproducible"))
	}
	currentHash, err := s.verifyProposalSafety(ctx, proposal)
	if err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	if domain.NormalizeTargetMode(proposal.Revision.TargetMode) == domain.TargetModeReplace && strings.ToLower(currentHash) != proposal.Revision.BaseHash {
		if markErr := s.markNeedsRevision(ctx, proposal); markErr != nil {
			return domain.AuthorizationIssueResult{}, markErr
		}
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: proposal.Revision.BaseHash, Current: strings.ToLower(currentHash)})
	}
	if err := authorizations.ValidateWorkflowContext(ctx, command.WorkspaceID, command.WorkflowRunID, command.NodeRunID); err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	authorizationID, err := s.ids.New()
	if err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	credential, err := newCredential()
	if err != nil {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITE_AUTHORIZATION_RANDOM_UNAVAILABLE", true, err)
	}
	now := s.clock.Now()
	result, err := authorizations.CreateAuthorization(ctx, domain.ToolAuthorization{
		ID:          authorizationID,
		WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID, NodeRunID: command.NodeRunID,
		ProposalID: command.ProposalID, RevisionID: command.RevisionID, ApprovalID: command.ApprovalID,
		ToolName: strings.TrimSpace(command.ToolName), Capability: command.Capability, Scope: strings.TrimSpace(command.Scope),
		ApprovedChangeHash: proposal.Revision.ChangeHash, TargetMode: domain.NormalizeTargetMode(proposal.Revision.TargetMode), TargetVersion: proposal.Revision.BaseHash,
		TokenHash: hashCredential(credential), IdempotencyKey: strings.TrimSpace(command.IdempotencyKey),
		Status: domain.AuthorizationIssued, IssuedAt: now, ExpiresAt: now.Add(command.TTL), Version: 1,
	})
	if err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	result.Authorization.TokenHash = ""
	if result.Replayed {
		return result, nil
	}
	result.Credential = credential
	return result, nil
}

// ConsumeWriteAuthorization 原子消费一次性写权限，不执行任何文件/Git 副作用。
func (s *Service) ConsumeWriteAuthorization(ctx context.Context, request domain.AuthorizationConsume) (domain.AuthorizationConsumeResult, error) {
	authorizations, ok := s.repo.(domain.AuthorizationRepository)
	if !ok {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITE_AUTHORIZATION_REPOSITORY_UNAVAILABLE", false, errors.New("authorization repository is unavailable"))
	}
	if strings.TrimSpace(request.Credential) == "" || len(request.Credential) > domain.MaxAuthorizationCredentialBytes || strings.TrimSpace(request.IdempotencyKey) == "" || len(strings.TrimSpace(request.IdempotencyKey)) > 128 || request.WorkspaceID == "" || request.WorkflowRunID == "" || request.NodeRunID == "" || request.ProposalID == "" || request.RevisionID == "" || request.ApprovalID == "" || strings.TrimSpace(request.ToolName) == "" || len(strings.TrimSpace(request.ToolName)) > 64 || strings.TrimSpace(request.Scope) == "" || len(strings.TrimSpace(request.Scope)) > 512 || !domain.ValidHash(request.ApprovedChangeHash) {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_CONSUME_INVALID", false, errors.New("authorization consume binding is incomplete"))
	}
	if err := domain.ValidateToolBinding(request.ToolName, request.Capability); err != nil {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_CONSUME_INVALID", false, err)
	}
	request.ToolName = strings.TrimSpace(request.ToolName)
	request.Scope = strings.TrimSpace(request.Scope)
	request.ApprovedChangeHash = strings.ToLower(request.ApprovedChangeHash)
	request.Credential = hashCredential(request.Credential)
	proposal, err := s.repo.GetProposal(ctx, request.ProposalID)
	if err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	if err := requireFilePatchProposal(proposal, "WRITE_AUTHORIZATION_PROPOSAL_TYPE_UNSUPPORTED"); err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	if domain.NormalizeTargetMode(proposal.Revision.TargetMode) == domain.TargetModeReplace {
		request.TargetVersion = strings.ToLower(request.TargetVersion)
	}
	existing, err := authorizations.GetAuthorization(ctx, request.WorkspaceID, request.IdempotencyKey, request.Credential)
	if err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	if err := domain.ValidateAuthorizationConsumeBinding(existing, request); err != nil {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_BINDING_CONFLICT", false, err)
	}
	if existing.Status != domain.AuthorizationIssued {
		result, consumeErr := authorizations.ConsumeAuthorization(ctx, request)
		result.Authorization.TokenHash = ""
		return result, consumeErr
	}
	if proposal.WorkspaceID != request.WorkspaceID || proposal.Revision.ID != request.RevisionID || proposal.Approval == nil || proposal.Approval.ID != request.ApprovalID || proposal.Status != domain.StatusApproved || proposal.Approval.Decision != domain.DecisionApproved || proposal.Revision.ChangeHash != request.ApprovedChangeHash || proposal.Revision.BaseHash != request.TargetVersion || proposal.Approval.ChangeHash != request.ApprovedChangeHash || strings.TrimSpace(request.Scope) != domain.ExpectedAuthorizationScopeForTarget(proposal.Revision.TargetPath, proposal.Revision.TargetMode) || !proposalChangeHashValid(proposal) {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED", false, errors.New("authorization approval binding is no longer valid"))
	}
	if domain.NormalizeTargetMode(request.TargetMode) != domain.NormalizeTargetMode(proposal.Revision.TargetMode) {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_BINDING_CONFLICT", false, errors.New("authorization target mode differs"))
	}
	currentHash, err := s.verifyProposalSafety(ctx, proposal)
	if err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	if domain.NormalizeTargetMode(proposal.Revision.TargetMode) == domain.TargetModeReplace && strings.ToLower(currentHash) != proposal.Revision.BaseHash {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, &HashConflict{Expected: proposal.Revision.BaseHash, Current: strings.ToLower(currentHash)})
	}
	if err := authorizations.ValidateWorkflowContext(ctx, request.WorkspaceID, request.WorkflowRunID, request.NodeRunID); err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	result, err := authorizations.ConsumeAuthorization(ctx, request)
	result.Authorization.TokenHash = ""
	return result, err
}

// RevokeWriteAuthorization 使尚未消费的授权立即失效。
func (s *Service) RevokeWriteAuthorization(ctx context.Context, id foundation.ID) error {
	authorizations, ok := s.repo.(domain.AuthorizationRepository)
	if !ok {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITE_AUTHORIZATION_REPOSITORY_UNAVAILABLE", false, errors.New("authorization repository is unavailable"))
	}
	if id == "" {
		return foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_ID_INVALID", false, errors.New("authorization id is required"))
	}
	return authorizations.RevokeAuthorization(ctx, id, s.clock.Now())
}

func newCredential() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func hashCredential(credential string) string {
	digest := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(digest[:])
}

func proposalType(proposal domain.Proposal) domain.ProposalType {
	return domain.NormalizeProposalType(proposal.Type)
}

func proposalChangeHashValid(proposal domain.Proposal) bool {
	expected, err := domain.ComputeChangeHashForTarget(
		proposal.WorkspaceID,
		proposal.Revision.TargetPath,
		proposal.Revision.TargetMode,
		proposal.Revision.BaseHash,
		proposal.Revision.Content,
	)
	return err == nil && strings.EqualFold(expected, proposal.Revision.ChangeHash) &&
		(proposalType(proposal) != domain.ProposalTypeRestoreDocument || domain.ValidateProposalRevisionForType(domain.ProposalTypeRestoreDocument, proposal.Revision) == nil)
}

func (s *Service) verifyProposalTarget(ctx context.Context, proposal domain.Proposal) (string, error) {
	if domain.NormalizeTargetMode(proposal.Revision.TargetMode) == domain.TargetModeCreateOnly {
		reader, ok := s.targets.(CreateOnlyTargetReader)
		if !ok {
			return "", foundation.NewError(foundation.ErrorDependencyUnavailable, "CREATE_ONLY_TARGET_READER_UNAVAILABLE", false, errors.New("create-only target reader is unavailable"))
		}
		if err := reader.EnsureTargetAbsent(ctx, proposal.WorkspaceID, proposal.Revision.TargetPath, proposal.Revision.BaseHash); err != nil {
			return "", err
		}
		return proposal.Revision.BaseHash, nil
	}
	return s.targets.CurrentHash(ctx, proposal.WorkspaceID, proposal.Revision.TargetPath)
}

func (s *Service) verifyCreateOnlyGitTarget(ctx context.Context, proposal domain.Proposal, approvedHead string) error {
	if domain.NormalizeTargetMode(proposal.Revision.TargetMode) != domain.TargetModeCreateOnly {
		return nil
	}
	inspector, ok := s.git.(CreateOnlyApprovalGitInspector)
	if !ok {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "CREATE_ONLY_GIT_INSPECTOR_UNAVAILABLE", false, errors.New("create-only git inspector is unavailable"))
	}
	return inspector.EnsureTargetAbsentAt(ctx, proposal.WorkspaceID, approvedHead, proposal.Revision.TargetPath)
}

func (s *Service) verifyProposalSafety(ctx context.Context, proposal domain.Proposal) (string, error) {
	version, err := s.verifyProposalTarget(ctx, proposal)
	if err != nil {
		return "", err
	}
	if domain.NormalizeTargetMode(proposal.Revision.TargetMode) == domain.TargetModeCreateOnly {
		if proposal.Approval == nil || proposal.Approval.ApprovedGitHead == nil {
			return "", foundation.NewError(foundation.ErrorConsistencyViolation, "CREATE_ONLY_GIT_HEAD_MISSING", false, errors.New("create-only approval git head is missing"))
		}
		if err := s.verifyCreateOnlyGitTarget(ctx, proposal, *proposal.Approval.ApprovedGitHead); err != nil {
			return "", err
		}
	}
	return version, nil
}

func validateProposalForApproval(proposal domain.Proposal) error {
	if _, err := domain.ValidateProposalRiskLevelForType(proposalType(proposal), proposal.RiskLevel); err != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_RISK_LEVEL_INVALID", false, err)
	}
	if !domain.ProposalSupportsFileWriteback(proposal.Type) && strings.TrimSpace(proposal.TargetPath) != "" {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "TYPED_PROPOSAL_INVALID", false, errors.New("typed proposal must not carry file target fields"))
	}
	if err := domain.ValidateProposalRevisionForType(proposalType(proposal), proposal.Revision); err != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "PROPOSAL_REVISION_INVALID", false, err)
	}
	return nil
}

// proposalRiskLevelForCreate 只接受调用方显式提供的冻结枚举，不从自由文本 Risk 推导等级。
func proposalRiskLevelForCreate(level domain.ProposalRiskLevel) (domain.ProposalRiskLevel, error) {
	return domain.ParseProposalRiskLevel(level)
}

func requireFilePatchProposal(proposal domain.Proposal, code string) error {
	if proposalType(proposal) == domain.ProposalTypeDownstreamUpdate {
		return downstreamUpdateApplyUnavailable()
	}
	if !domain.ProposalSupportsFileWriteback(proposal.Type) {
		return foundation.NewError(foundation.ErrorPermissionDenied, code, false, errors.New("proposal type does not support file writeback"))
	}
	return nil
}

func downstreamUpdateApplyUnavailable() error {
	return domain.NewDownstreamUpdateApplyUnavailableError()
}
