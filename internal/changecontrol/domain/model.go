package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ProposalStatus 是 Proposal 的持久化状态；本期只推进 ready_for_review、approved、rejected、needs_revision。
type ProposalStatus string

const (
	StatusDraft         ProposalStatus = "draft"
	StatusValidating    ProposalStatus = "validating"
	StatusReady         ProposalStatus = "ready_for_review"
	StatusApproved      ProposalStatus = "approved"
	StatusApplying      ProposalStatus = "applying"
	StatusApplied       ProposalStatus = "applied"
	StatusVerifying     ProposalStatus = "verifying"
	StatusCompleted     ProposalStatus = "completed"
	StatusRejected      ProposalStatus = "rejected"
	StatusNeedsRevision ProposalStatus = "needs_revision"
	StatusDeferred      ProposalStatus = "deferred"
	StatusApplyFailed   ProposalStatus = "apply_failed"
	StatusVerifyFailed  ProposalStatus = "verify_failed"
	StatusRolledBack    ProposalStatus = "rolled_back"
	StatusCancelled     ProposalStatus = "cancelled"
)

// Decision 是一次不可变的用户审批决定。
type Decision string

const (
	DecisionApproved Decision = "approved"
	DecisionRejected Decision = "rejected"
)

// Proposal 聚合包含当前 Revision 和可选 Approval 快照。
type Proposal struct {
	ID, WorkspaceID foundation.ID
	TargetPath      string
	IdempotencyKey  string
	RequestHash     string
	Status          ProposalStatus
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Revision        Revision
	Approval        *Approval
}

// TargetUnavailableError 表示 Proposal 目标不再是 Workspace 内可安全读取的普通文件。
type TargetUnavailableError struct{ Cause error }

func (e *TargetUnavailableError) Error() string { return "proposal target is unavailable" }
func (e *TargetUnavailableError) Unwrap() error { return e.Cause }

// Revision 是带目标、基线和证据的不可变变更快照。
type Revision struct {
	ID, ProposalID  foundation.ID
	RevisionNo      int
	TargetPath      string
	BaseHash        string
	Content         string
	EvidenceSummary string
	Risk            string
	RollbackPlan    string
	ChangeHash      string
	CreatedAt       time.Time
}

// Approval 将用户决定绑定到唯一 Revision 和 Change Hash。
type Approval struct {
	ID, ProposalID, RevisionID foundation.ID
	ChangeHash                 string
	Decision                   Decision
	DecidedAt                  time.Time
}

// ComputeChangeHash 对版本化规范载荷计算哈希，保留内容中的语义空白。
func ComputeChangeHash(targetPath, baseHash, content string) string {
	normalizedContent := strings.ReplaceAll(content, "\r\n", "\n")
	canonical := fmt.Sprintf("zhixu-change-v1\ntarget-path-length:%d\ntarget-path:%s\nbase-hash:%s\ncontent-length:%d\ncontent:\n%s", len(targetPath), targetPath, strings.ToLower(baseHash), len(normalizedContent), normalizedContent)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// ComputeRequestHash 对 Proposal 创建请求计算稳定哈希，用于幂等键防误用。
func ComputeRequestHash(workspaceID foundation.ID, targetPath, baseHash, content, evidence, risk, rollback string) string {
	payload := struct {
		WorkspaceID string `json:"workspace_id"`
		TargetPath  string `json:"target_path"`
		BaseHash    string `json:"base_hash"`
		Content     string `json:"content"`
		Evidence    string `json:"evidence"`
		Risk        string `json:"risk"`
		Rollback    string `json:"rollback"`
	}{string(workspaceID), targetPath, strings.ToLower(baseHash), strings.ReplaceAll(content, "\r\n", "\n"), evidence, risk, rollback}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// ValidateTargetPath 只允许 Workspace 内的相对 POSIX 路径。
func ValidateTargetPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return "", errors.New("target path must be a relative POSIX path")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("target path escapes workspace")
	}
	return clean, nil
}

// ValidHash 判断是否为小写或大写十六进制 SHA-256 字符串。
func ValidHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}
