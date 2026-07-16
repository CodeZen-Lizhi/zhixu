package domain

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// GitObjectFormat 是 Git 仓库使用的对象哈希格式。
type GitObjectFormat string

// GitOperation 区分同一 Writeback Execution 的正式写回与严格反向 Commit。
type GitOperation string

const (
	// GitObjectFormatSHA1 表示 40 位 Git 对象 ID。
	GitObjectFormatSHA1 GitObjectFormat = "sha1"
	// GitObjectFormatSHA256 表示 64 位 Git 对象 ID。
	GitObjectFormatSHA256 GitObjectFormat = "sha256"
	// GitOperationApply 表示应用批准 Proposal 的正式写回。
	GitOperationApply GitOperation = "apply"
	// GitOperationRevert 表示反向恢复批准 Base 的补偿写回。
	GitOperationRevert GitOperation = "revert"

	// GitCommitSubject 是批准写回 Commit 的固定标题。
	GitCommitSubject = "ZHIXU: apply approved proposal"
	// GitReverseCommitSubject 是自动反向 Commit 的固定标题。
	GitReverseCommitSubject = "ZHIXU: revert approved proposal"
	// GitCommitLookupLimit 是当前分支可达历史的固定恢复查询上限。
	GitCommitLookupLimit = 256

	// GitTrailerWritebackID 标识 Writeback Execution Trailer。
	GitTrailerWritebackID = "Zhixu-Writeback-ID"
	// GitTrailerOperation 区分同一 Writeback Execution 的 apply 与 revert Commit。
	GitTrailerOperation = "Zhixu-Operation"
	// GitTrailerProposalID 标识 Proposal Trailer。
	GitTrailerProposalID = "Zhixu-Proposal-ID"
	// GitTrailerRevisionID 标识 Proposal Revision Trailer。
	GitTrailerRevisionID = "Zhixu-Revision-ID"
	// GitTrailerApprovalID 标识 Approval Trailer。
	GitTrailerApprovalID = "Zhixu-Approval-ID"
	// GitTrailerWorkflowRunID 标识 Workflow Run Trailer。
	GitTrailerWorkflowRunID = "Zhixu-Workflow-Run-ID"
	// GitTrailerWorkflowNodeID 标识 Workflow Node Trailer。
	GitTrailerWorkflowNodeID = "Zhixu-Workflow-Node-ID"
	// GitTrailerTargetPath 标识 Workspace 相对目标路径 Trailer。
	GitTrailerTargetPath = "Zhixu-Target-Path"
	// GitTrailerResultSHA256 标识写回结果原始字节的 SHA-256 Trailer。
	GitTrailerResultSHA256 = "Zhixu-Result-SHA256"
	// GitTrailerDiffSHA256 标识稳定 Git Diff 字节的 SHA-256 Trailer。
	GitTrailerDiffSHA256 = "Zhixu-Diff-SHA256"
	// GitTrailerRevertsCommit 标识反向 Commit 对应的原 Commit。
	GitTrailerRevertsCommit = "Zhixu-Reverts-Commit"

	// GitFileModeRegular 是普通非可执行 blob 的规范 Git mode。
	GitFileModeRegular = "100644"
	// GitFileModeExecutable 是普通可执行 blob 的规范 Git mode。
	GitFileModeExecutable = "100755"
)

var (
	// ErrGitInvalidInput 表示 Git 请求缺少必要绑定或字段格式非法。
	ErrGitInvalidInput = errors.New("git writeback input is invalid")
	// ErrGitNotFound 表示 Workspace、仓库或匹配 Commit 不存在。
	ErrGitNotFound = errors.New("git writeback resource was not found")
	// ErrGitVersionConflict 表示 HEAD、index 或 worktree 已偏离批准基线。
	ErrGitVersionConflict = errors.New("git writeback version does not match")
	// ErrGitPermissionDenied 表示仓库、路径、属性或对象类型不满足安全边界。
	ErrGitPermissionDenied = errors.New("git writeback permission was denied")
	// ErrGitDependencyUnavailable 表示 Git 可执行文件或仓库依赖当前不可用。
	ErrGitDependencyUnavailable = errors.New("git writeback dependency is unavailable")
	// ErrGitRetryableFailure 表示未产生未知副作用且可以退避重试的临时失败。
	ErrGitRetryableFailure = errors.New("git writeback failed temporarily")
	// ErrGitConsistencyViolation 表示 Trailer 或 Commit 内容与持久化绑定冲突。
	ErrGitConsistencyViolation = errors.New("git writeback consistency was violated")
	// ErrGitManualRecoveryRequired 表示 Commit 或 Revert 结果无法自动证明。
	ErrGitManualRecoveryRequired = errors.New("git writeback requires manual recovery")
)

// GitRepository 是 Change Control 使用的受限 Git 副作用端口。
// 实现必须通过 Workspace ID 解析仓库，不能接受任意根目录或 Git 参数。
type GitRepository interface {
	Inspect(context.Context, foundation.ID, string) (GitSnapshot, error)
	DiffApproved(context.Context, GitDiffRequest) (GitDiff, error)
	CommitApproved(context.Context, GitCommitRequest) (GitCommit, error)
	FindWritebackCommit(context.Context, GitCommitLookup) (GitCommit, error)
	CreateReverseCommit(context.Context, ReverseCommitRequest) (GitCommit, error)
}

// GitSnapshot 是通过 attached HEAD、author identity 和全仓 clean 校验后的仓库摘要。
type GitSnapshot struct {
	WorkspaceID  foundation.ID
	Branch       string
	Head         string
	ObjectFormat GitObjectFormat
	Clean        bool
}

// GitDiffRequest 请求验证文件 CAS 后的唯一目标 Diff。
type GitDiffRequest struct {
	WorkspaceID     foundation.ID
	TargetPath      string
	ApprovedGitHead string
	ResultHash      string
}

// GitDiff 绑定批准基线、目标结果、稳定 Diff、Git blob 和原始文件 mode。
type GitDiff struct {
	WorkspaceID     foundation.ID
	TargetPath      string
	ApprovedGitHead string
	ResultHash      string
	DiffHash        string
	BaseBlobID      string
	ResultBlobID    string
	BaseMode        string
}

// GitCommitRequest 将唯一 Diff 绑定到 Writeback、Proposal、Approval 和 Workflow 身份。
type GitCommitRequest struct {
	WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
	WritebackExecutionID                  foundation.ID
	ProposalID, RevisionID, ApprovalID    foundation.ID
	Operation                             GitOperation
	TargetPath                            string
	ApprovedGitHead, ResultHash, DiffHash string
	BaseBlobID, ResultBlobID, BaseMode    string
}

// GitCommit 是经过 parent、路径、blob、Diff 和 Trailer 复核的 Commit 摘要。
// Recovered 表示命令结果未知后查证成功；Replayed 表示副作用前找到完全相同的既有 Commit。
type GitCommit struct {
	WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
	WritebackExecutionID                  foundation.ID
	ProposalID, RevisionID, ApprovalID    foundation.ID
	Operation                             GitOperation
	TargetPath                            string
	ApprovedGitHead                       string
	GitCommit, ParentGitCommit            string
	ResultHash, DiffHash                  string
	BaseBlobID, ResultBlobID, BaseMode    string
	Recovered, Replayed                   bool
	RevertsCommit                         string
}

// GitCommitLookup 使用与 Commit 相同的完整不可变绑定查找当前分支可达历史。
type GitCommitLookup struct {
	WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
	WritebackExecutionID                  foundation.ID
	ProposalID, RevisionID, ApprovalID    foundation.ID
	Operation                             GitOperation
	TargetPath                            string
	ApprovedGitHead, ResultHash, DiffHash string
	BaseBlobID, ResultBlobID, BaseMode    string
	RevertsCommit                         string
}

// ReverseCommitRequest 将严格反向操作绑定到系统生成的原 Commit 和批准 Base Hash。
type ReverseCommitRequest struct {
	Commit           GitCommit
	ExpectedBaseHash string
}

// ValidateGitSnapshotBinding 校验 Inspect 返回的 Workspace、对象格式、attached HEAD 和 clean 摘要。
func ValidateGitSnapshotBinding(workspaceID foundation.ID, approvedGitHead string, snapshot GitSnapshot) error {
	if !validGitID(workspaceID) || !ValidGitHead(approvedGitHead) || !validGitID(snapshot.WorkspaceID) || snapshot.WorkspaceID != workspaceID {
		return ErrGitInvalidInput
	}
	if !validSingleLine(snapshot.Branch, 1024) || !snapshot.Clean {
		return ErrGitVersionConflict
	}
	if !validGitObjectIDForFormat(snapshot.Head, snapshot.ObjectFormat) {
		return ErrGitInvalidInput
	}
	if !strings.EqualFold(snapshot.Head, approvedGitHead) {
		return ErrGitVersionConflict
	}
	return nil
}

// ValidateGitDiffRequest 校验 Diff 请求的 Workspace、Markdown 目标、批准 HEAD 和结果哈希。
func ValidateGitDiffRequest(request GitDiffRequest) error {
	if !validGitID(request.WorkspaceID) || validateGitTargetPath(request.TargetPath) != nil || !ValidGitHead(request.ApprovedGitHead) || !ValidHash(request.ResultHash) {
		return ErrGitInvalidInput
	}
	return nil
}

// ValidateGitDiffBinding 确认 Diff 只能由给定请求派生，且对象 ID 长度与批准 HEAD 一致。
func ValidateGitDiffBinding(request GitDiffRequest, diff GitDiff) error {
	if err := ValidateGitDiffRequest(request); err != nil {
		return err
	}
	if !validGitDiff(diff) {
		return ErrGitConsistencyViolation
	}
	if diff.WorkspaceID != request.WorkspaceID || diff.TargetPath != request.TargetPath || !strings.EqualFold(diff.ApprovedGitHead, request.ApprovedGitHead) || !strings.EqualFold(diff.ResultHash, request.ResultHash) {
		return ErrGitConsistencyViolation
	}
	if !sameGitObjectWidth(diff.ApprovedGitHead, diff.BaseBlobID) || !sameGitObjectWidth(diff.ApprovedGitHead, diff.ResultBlobID) {
		return ErrGitConsistencyViolation
	}
	return nil
}

// ValidateGitCommitRequest 校验 Commit 的完整审批、Workflow 和 Diff 绑定。
func ValidateGitCommitRequest(request GitCommitRequest) error {
	if !validGitCommitIdentity(request.WorkspaceID, request.WorkflowRunID, request.NodeRunID, request.WritebackExecutionID, request.ProposalID, request.RevisionID, request.ApprovalID) || request.Operation != GitOperationApply {
		return ErrGitInvalidInput
	}
	diff := GitDiff{
		WorkspaceID: request.WorkspaceID, TargetPath: request.TargetPath, ApprovedGitHead: request.ApprovedGitHead,
		ResultHash: request.ResultHash, DiffHash: request.DiffHash, BaseBlobID: request.BaseBlobID,
		ResultBlobID: request.ResultBlobID, BaseMode: request.BaseMode,
	}
	if !validGitDiff(diff) || !sameGitObjectWidth(diff.ApprovedGitHead, diff.BaseBlobID) || !sameGitObjectWidth(diff.ApprovedGitHead, diff.ResultBlobID) {
		return ErrGitInvalidInput
	}
	return nil
}

// ValidateGitCommitBinding 确认 Commit 保留完整请求绑定且 parent 必须是批准 HEAD。
func ValidateGitCommitBinding(request GitCommitRequest, commit GitCommit) error {
	if err := ValidateGitCommitRequest(request); err != nil {
		return err
	}
	if !validGitCommit(commit, false) {
		return ErrGitConsistencyViolation
	}
	if commit.Operation != GitOperationApply || commit.RevertsCommit != "" || commit.Recovered && commit.Replayed || !sameCommitRequest(request, commit) || !strings.EqualFold(commit.ParentGitCommit, request.ApprovedGitHead) || !sameGitObjectWidth(commit.GitCommit, request.ApprovedGitHead) {
		return ErrGitConsistencyViolation
	}
	return nil
}

// ValidateGitCommitLookup 校验恢复查询必须携带与原 Commit 请求相同的完整绑定。
func ValidateGitCommitLookup(lookup GitCommitLookup) error {
	if !validGitCommitIdentity(lookup.WorkspaceID, lookup.WorkflowRunID, lookup.NodeRunID, lookup.WritebackExecutionID, lookup.ProposalID, lookup.RevisionID, lookup.ApprovalID) {
		return ErrGitInvalidInput
	}
	diff := GitDiff{
		WorkspaceID: lookup.WorkspaceID, TargetPath: lookup.TargetPath, ApprovedGitHead: lookup.ApprovedGitHead,
		ResultHash: lookup.ResultHash, DiffHash: lookup.DiffHash, BaseBlobID: lookup.BaseBlobID,
		ResultBlobID: lookup.ResultBlobID, BaseMode: lookup.BaseMode,
	}
	if !validGitDiff(diff) || !sameGitObjectWidth(diff.ApprovedGitHead, diff.BaseBlobID) || !sameGitObjectWidth(diff.ApprovedGitHead, diff.ResultBlobID) {
		return ErrGitInvalidInput
	}
	switch lookup.Operation {
	case GitOperationApply:
		if lookup.RevertsCommit != "" {
			return ErrGitInvalidInput
		}
	case GitOperationRevert:
		if !ValidGitHead(lookup.RevertsCommit) || !sameGitObjectWidth(lookup.RevertsCommit, lookup.ApprovedGitHead) {
			return ErrGitInvalidInput
		}
	default:
		return ErrGitInvalidInput
	}
	return nil
}

// ValidateGitCommitLookupBinding 确认查询结果是完全相同的可恢复或重放 Commit。
func ValidateGitCommitLookupBinding(lookup GitCommitLookup, commit GitCommit) error {
	if err := ValidateGitCommitLookup(lookup); err != nil {
		return err
	}
	if !validGitCommit(commit, lookup.Operation == GitOperationRevert) || commit.Recovered && commit.Replayed || !commit.Recovered && !commit.Replayed || !sameLookupBinding(lookup, commit) {
		return ErrGitConsistencyViolation
	}
	if lookup.Operation == GitOperationApply && !strings.EqualFold(commit.ParentGitCommit, lookup.ApprovedGitHead) {
		return ErrGitConsistencyViolation
	}
	if lookup.Operation == GitOperationRevert && !strings.EqualFold(commit.ParentGitCommit, lookup.RevertsCommit) {
		return ErrGitConsistencyViolation
	}
	return nil
}

// ValidateReverseCommitRequest 校验反向操作只接受完整有效的系统前向 Commit 和 Base Hash。
func ValidateReverseCommitRequest(request ReverseCommitRequest) error {
	if !ValidHash(request.ExpectedBaseHash) {
		return ErrGitInvalidInput
	}
	if !validGitCommit(request.Commit, false) || request.Commit.Operation != GitOperationApply || request.Commit.RevertsCommit != "" || request.Commit.Recovered && request.Commit.Replayed || !strings.EqualFold(request.Commit.ParentGitCommit, request.Commit.ApprovedGitHead) || !sameGitObjectWidth(request.Commit.GitCommit, request.Commit.ApprovedGitHead) || !sameGitObjectWidth(request.Commit.BaseBlobID, request.Commit.ApprovedGitHead) || !sameGitObjectWidth(request.Commit.ResultBlobID, request.Commit.ApprovedGitHead) {
		return ErrGitInvalidInput
	}
	return nil
}

// ValidateReverseCommitBinding 确认反向 Commit 的 parent、reverts、Base 内容和 blob 完整绑定原 Commit。
func ValidateReverseCommitBinding(request ReverseCommitRequest, reverse GitCommit) error {
	if err := ValidateReverseCommitRequest(request); err != nil {
		return err
	}
	original := request.Commit
	if !validGitCommit(reverse, true) || reverse.Recovered && reverse.Replayed {
		return ErrGitConsistencyViolation
	}
	if reverse.Operation != GitOperationRevert || !sameGitCommitIdentity(original, reverse) || reverse.TargetPath != original.TargetPath || !strings.EqualFold(reverse.ApprovedGitHead, original.ApprovedGitHead) || !strings.EqualFold(reverse.ParentGitCommit, original.GitCommit) || !strings.EqualFold(reverse.RevertsCommit, original.GitCommit) || !strings.EqualFold(reverse.ResultHash, request.ExpectedBaseHash) || !strings.EqualFold(reverse.BaseBlobID, original.ResultBlobID) || !strings.EqualFold(reverse.ResultBlobID, original.BaseBlobID) || reverse.BaseMode != original.BaseMode || !sameGitObjectWidth(reverse.GitCommit, original.GitCommit) {
		return ErrGitConsistencyViolation
	}
	return nil
}

// ValidGitObjectID 判断值是否为 Git SHA-1 或 SHA-256 对象 ID。
func ValidGitObjectID(value string) bool {
	return ValidGitHead(value)
}

// ValidGitFileMode 判断目标是否为支持的普通 blob mode。
func ValidGitFileMode(value string) bool {
	return value == GitFileModeRegular || value == GitFileModeExecutable
}

func validGitObjectIDForFormat(value string, format GitObjectFormat) bool {
	switch format {
	case GitObjectFormatSHA1:
		return len(value) == 40 && ValidGitObjectID(value)
	case GitObjectFormatSHA256:
		return len(value) == 64 && ValidGitObjectID(value)
	default:
		return false
	}
}

func validGitDiff(diff GitDiff) bool {
	return validGitID(diff.WorkspaceID) && validateGitTargetPath(diff.TargetPath) == nil && ValidGitHead(diff.ApprovedGitHead) && ValidHash(diff.ResultHash) && ValidHash(diff.DiffHash) && ValidGitObjectID(diff.BaseBlobID) && ValidGitObjectID(diff.ResultBlobID) && ValidGitFileMode(diff.BaseMode)
}

func validGitCommit(commit GitCommit, reverse bool) bool {
	if !validGitCommitIdentity(commit.WorkspaceID, commit.WorkflowRunID, commit.NodeRunID, commit.WritebackExecutionID, commit.ProposalID, commit.RevisionID, commit.ApprovalID) || validateGitTargetPath(commit.TargetPath) != nil || !ValidGitHead(commit.ApprovedGitHead) || !ValidGitHead(commit.GitCommit) || !ValidGitHead(commit.ParentGitCommit) || !ValidHash(commit.ResultHash) || !ValidHash(commit.DiffHash) || !ValidGitObjectID(commit.BaseBlobID) || !ValidGitObjectID(commit.ResultBlobID) || !ValidGitFileMode(commit.BaseMode) {
		return false
	}
	if reverse {
		return commit.Operation == GitOperationRevert && ValidGitHead(commit.RevertsCommit)
	}
	return commit.Operation == GitOperationApply && commit.RevertsCommit == ""
}

func validGitCommitIdentity(ids ...foundation.ID) bool {
	for _, id := range ids {
		if !validGitID(id) {
			return false
		}
	}
	return true
}

func validGitID(value foundation.ID) bool {
	return validSingleLine(string(value), 128) && !strings.ContainsFunc(string(value), unicode.IsSpace)
}

func validSingleLine(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func validateGitTargetPath(targetPath string) error {
	if err := validateWritebackTargetPath(targetPath); err != nil || strings.ContainsFunc(targetPath, unicode.IsControl) {
		return ErrGitInvalidInput
	}
	return nil
}

func sameGitObjectWidth(left, right string) bool {
	return (len(left) == 40 || len(left) == 64) && len(left) == len(right)
}

func sameCommitRequest(request GitCommitRequest, commit GitCommit) bool {
	return request.WorkspaceID == commit.WorkspaceID &&
		request.WorkflowRunID == commit.WorkflowRunID &&
		request.NodeRunID == commit.NodeRunID &&
		request.WritebackExecutionID == commit.WritebackExecutionID &&
		request.ProposalID == commit.ProposalID &&
		request.RevisionID == commit.RevisionID &&
		request.ApprovalID == commit.ApprovalID &&
		request.Operation == commit.Operation &&
		request.TargetPath == commit.TargetPath &&
		strings.EqualFold(request.ApprovedGitHead, commit.ApprovedGitHead) &&
		strings.EqualFold(request.ResultHash, commit.ResultHash) &&
		strings.EqualFold(request.DiffHash, commit.DiffHash) &&
		strings.EqualFold(request.BaseBlobID, commit.BaseBlobID) &&
		strings.EqualFold(request.ResultBlobID, commit.ResultBlobID) &&
		request.BaseMode == commit.BaseMode
}

func sameGitCommitIdentity(left, right GitCommit) bool {
	return left.WorkspaceID == right.WorkspaceID &&
		left.WorkflowRunID == right.WorkflowRunID &&
		left.NodeRunID == right.NodeRunID &&
		left.WritebackExecutionID == right.WritebackExecutionID &&
		left.ProposalID == right.ProposalID &&
		left.RevisionID == right.RevisionID &&
		left.ApprovalID == right.ApprovalID
}

func sameLookupBinding(lookup GitCommitLookup, commit GitCommit) bool {
	return lookup.WorkspaceID == commit.WorkspaceID &&
		lookup.WorkflowRunID == commit.WorkflowRunID &&
		lookup.NodeRunID == commit.NodeRunID &&
		lookup.WritebackExecutionID == commit.WritebackExecutionID &&
		lookup.ProposalID == commit.ProposalID &&
		lookup.RevisionID == commit.RevisionID &&
		lookup.ApprovalID == commit.ApprovalID &&
		lookup.Operation == commit.Operation &&
		lookup.TargetPath == commit.TargetPath &&
		strings.EqualFold(lookup.ApprovedGitHead, commit.ApprovedGitHead) &&
		strings.EqualFold(lookup.ResultHash, commit.ResultHash) &&
		strings.EqualFold(lookup.DiffHash, commit.DiffHash) &&
		strings.EqualFold(lookup.BaseBlobID, commit.BaseBlobID) &&
		strings.EqualFold(lookup.ResultBlobID, commit.ResultBlobID) &&
		lookup.BaseMode == commit.BaseMode &&
		strings.EqualFold(lookup.RevertsCommit, commit.RevertsCommit)
}
