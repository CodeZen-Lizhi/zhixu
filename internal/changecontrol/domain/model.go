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

// ProposalRiskLevel 是 Proposal 审批、API、列表筛选和幂等绑定使用的受控风险等级。
type ProposalRiskLevel string

const (
	// ProposalRiskLevelCritical 表示可能导致正式知识错误或数据不一致的最高风险。
	ProposalRiskLevelCritical ProposalRiskLevel = "CRITICAL"
	// ProposalRiskLevelHigh 表示会影响 RAG、引用或知识判断的高风险。
	ProposalRiskLevelHigh ProposalRiskLevel = "HIGH"
	// ProposalRiskLevelMedium 表示影响组织和维护质量的中风险。
	ProposalRiskLevelMedium ProposalRiskLevel = "MEDIUM"
	// ProposalRiskLevelLow 表示不影响正确性的低风险优化。
	ProposalRiskLevelLow ProposalRiskLevel = "LOW"
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
	Type            ProposalType
	// RiskLevel 是 Proposal 级持久事实；Revision.Risk 仅保存独立的自由文本说明。
	RiskLevel      ProposalRiskLevel
	TargetPath     string
	IdempotencyKey string
	RequestHash    string
	// WorkflowRunID 是 Approved Proposal 原子派发后绑定的唯一 Workflow Run；未派发记录为 nil，绑定后不随后续状态变化清除。
	WorkflowRunID *foundation.ID
	Status        ProposalStatus
	// Version 用于 Proposal 的乐观锁；每次受控状态变更必须递增一。
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
	Revision  Revision
	Approval  *Approval
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
	KnowledgeChange *KnowledgeChange
	PublishArtifact *PublishArtifact
	CreatedAt       time.Time
}

// Approval 将用户决定绑定到唯一 Revision 和 Change Hash。
type Approval struct {
	ID, ProposalID, RevisionID foundation.ID
	ChangeHash                 string
	Decision                   Decision
	// ApprovedGitHead 是服务端在批准时观察到的 Git HEAD；nil 表示历史审批没有该绑定，不能用于写回。
	ApprovedGitHead *string
	DecidedAt       time.Time
}

// ErrProposalInvalidTransition 表示 Proposal 状态迁移不在领域允许的迁移表中。
var ErrProposalInvalidTransition = errors.New("proposal status transition is not allowed")

// ErrProposalRiskLevelInvalid 表示风险等级不属于冻结枚举。
var ErrProposalRiskLevelInvalid = errors.New("proposal risk level is invalid")

// ParseProposalRiskLevel 严格校验 Proposal 使用的风险等级，不执行大小写或空白规范化。
func ParseProposalRiskLevel(level ProposalRiskLevel) (ProposalRiskLevel, error) {
	switch level {
	case ProposalRiskLevelCritical, ProposalRiskLevelHigh, ProposalRiskLevelMedium, ProposalRiskLevelLow:
		return level, nil
	default:
		return "", ErrProposalRiskLevelInvalid
	}
}

// ValidateProposalRiskLevelForType 校验 Proposal 类型的等级策略。
// 当前 knowledge_change 仅承载 Semantic Candidate Relation Proposal，因此固定为 HIGH。
func ValidateProposalRiskLevelForType(proposalType ProposalType, level ProposalRiskLevel) (ProposalRiskLevel, error) {
	parsed, err := ParseProposalRiskLevel(level)
	if err != nil {
		return "", err
	}
	switch NormalizeProposalType(proposalType) {
	case ProposalTypeFilePatch:
		return parsed, nil
	case ProposalTypeKnowledgeChange:
		if parsed != ProposalRiskLevelHigh {
			return "", fmt.Errorf("%w: knowledge_change proposals require HIGH", ErrProposalRiskLevelInvalid)
		}
		return parsed, nil
	case ProposalTypePublishArtifact:
		if parsed != ProposalRiskLevelHigh {
			return "", fmt.Errorf("%w: publish_artifact proposals require HIGH", ErrProposalRiskLevelInvalid)
		}
		return parsed, nil
	default:
		return "", ErrProposalTypeInvalid
	}
}

// ValidateProposalTransition 是 Proposal 状态迁移的唯一领域事实源。
// 数据库触发器和 Application 必须复用同一张迁移表，禁止各层自行放宽状态转移。
func ValidateProposalTransition(from, to ProposalStatus) error {
	allowed := map[ProposalStatus]map[ProposalStatus]struct{}{
		StatusReady: {
			StatusApproved: {}, StatusRejected: {}, StatusNeedsRevision: {},
		},
		StatusApproved: {
			StatusApplying: {}, StatusNeedsRevision: {},
		},
		StatusApplying: {
			StatusApplied: {}, StatusApplyFailed: {}, StatusNeedsRevision: {},
		},
		StatusApplied: {
			StatusVerifying: {},
		},
		StatusVerifying: {
			StatusCompleted: {}, StatusVerifyFailed: {}, StatusRolledBack: {},
		},
		StatusVerifyFailed: {
			StatusVerifying: {}, StatusRolledBack: {},
		},
		StatusApplyFailed: {
			StatusApplying: {}, StatusRolledBack: {},
		},
		StatusNeedsRevision: {
			StatusDraft: {}, StatusCancelled: {},
		},
	}
	if _, ok := allowed[from][to]; !ok {
		return ErrProposalInvalidTransition
	}
	return nil
}

// ComputeChangeHash 对版本化规范载荷计算哈希，保留内容中的语义空白。
func ComputeChangeHash(targetPath, baseHash, content string) string {
	normalizedContent := strings.ReplaceAll(content, "\r\n", "\n")
	canonical := fmt.Sprintf("zhixu-change-v1\ntarget-path-length:%d\ntarget-path:%s\nbase-hash:%s\ncontent-length:%d\ncontent:\n%s", len(targetPath), targetPath, strings.ToLower(baseHash), len(normalizedContent), normalizedContent)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// ComputeRequestHash 重现历史 Proposal 创建请求的 v1 哈希。
// 该函数只用于已持久化历史记录的精确重放；新 Proposal 必须使用包含 RiskLevel 的 v2 哈希。
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

// ComputeRequestHashWithRiskLevel 将显式风险等级纳入 Proposal 创建请求的幂等绑定。
func ComputeRequestHashWithRiskLevel(workspaceID foundation.ID, targetPath, baseHash, content, evidence string, riskLevel ProposalRiskLevel, risk, rollback string) (string, error) {
	normalizedRiskLevel, err := ParseProposalRiskLevel(riskLevel)
	if err != nil {
		return "", err
	}
	payload := struct {
		SchemaVersion string            `json:"schema_version"`
		WorkspaceID   string            `json:"workspace_id"`
		TargetPath    string            `json:"target_path"`
		BaseHash      string            `json:"base_hash"`
		Content       string            `json:"content"`
		Evidence      string            `json:"evidence"`
		RiskLevel     ProposalRiskLevel `json:"risk_level"`
		Risk          string            `json:"risk"`
		Rollback      string            `json:"rollback"`
	}{"proposal-create-request/v2", string(workspaceID), targetPath, strings.ToLower(baseHash), strings.ReplaceAll(content, "\r\n", "\n"), evidence, normalizedRiskLevel, risk, rollback}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
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
