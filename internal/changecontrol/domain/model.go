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

// TargetMode 描述文件补丁替换既有目标，或只创建始终缺失的新目标。
type TargetMode string

const (
	// TargetModeReplace 保留既有文件替换契约。
	TargetModeReplace TargetMode = "REPLACE"
	// TargetModeCreateOnly 要求 absence token，并禁止替换任何已存在目标。
	TargetModeCreateOnly TargetMode = "CREATE_ONLY"

	absenceTokenPrefix = "workspace-target-absent/v1:"
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

// RestoreBindingMatches compares the complete typed restore intent without dereferencing nil.
func (proposal Proposal) RestoreBindingMatches(expected RestoreDocument) bool {
	return proposal.Revision.RestoreDocument != nil && *proposal.Revision.RestoreDocument == expected
}

// TargetUnavailableError 表示 Proposal 目标不再是 Workspace 内可安全读取的普通文件。
type TargetUnavailableError struct{ Cause error }

func (e *TargetUnavailableError) Error() string { return "proposal target is unavailable" }
func (e *TargetUnavailableError) Unwrap() error { return e.Cause }

// Revision 是带目标、基线和证据的不可变变更快照。
type Revision struct {
	ID, ProposalID foundation.ID
	RevisionNo     int
	TargetPath     string
	// TargetMode 只允许旧内存值为空；空值按 REPLACE 处理。
	TargetMode      TargetMode
	BaseHash        string
	Content         string
	EvidenceSummary string
	Risk            string
	RollbackPlan    string
	ChangeHash      string
	KnowledgeChange *KnowledgeChange
	PublishArtifact *PublishArtifact
	// RestoreDocument is present only for append-only Document restore proposals.
	RestoreDocument *RestoreDocument
	// DownstreamUpdate 仅在 downstream_update Revision 中存在，记录审批意图而非写回命令。
	DownstreamUpdate *DownstreamUpdate
	CreatedAt        time.Time
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
	case ProposalTypeRestoreDocument:
		if parsed != ProposalRiskLevelHigh {
			return "", fmt.Errorf("%w: restore_document proposals require HIGH", ErrProposalRiskLevelInvalid)
		}
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
	case ProposalTypeDownstreamUpdate:
		if parsed != ProposalRiskLevelHigh {
			return "", fmt.Errorf("%w: downstream_update proposals require HIGH", ErrProposalRiskLevelInvalid)
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

// ValidateProposalTransitionForType 按 Proposal 类型收紧状态迁移。
func ValidateProposalTransitionForType(proposalType ProposalType, from, to ProposalStatus) error {
	if NormalizeProposalType(proposalType) == ProposalTypeDownstreamUpdate {
		if from == StatusReady && (to == StatusApproved || to == StatusRejected) {
			return nil
		}
		return ErrProposalInvalidTransition
	}
	return ValidateProposalTransition(from, to)
}

// ComputeChangeHash 对版本化规范载荷计算哈希，保留内容中的语义空白。
func ComputeChangeHash(targetPath, baseHash, content string) string {
	normalizedContent := strings.ReplaceAll(content, "\r\n", "\n")
	canonical := fmt.Sprintf("zhixu-change-v1\ntarget-path-length:%d\ntarget-path:%s\nbase-hash:%s\ncontent-length:%d\ncontent:\n%s", len(targetPath), targetPath, strings.ToLower(baseHash), len(normalizedContent), normalizedContent)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// NormalizeTargetMode 将旧空值表示规范为 REPLACE。
func NormalizeTargetMode(mode TargetMode) TargetMode {
	if mode == "" {
		return TargetModeReplace
	}
	return mode
}

// ValidateTargetMode 只接受两种不可变目标存在性契约。
func ValidateTargetMode(mode TargetMode) (TargetMode, error) {
	mode = NormalizeTargetMode(mode)
	if mode != TargetModeReplace && mode != TargetModeCreateOnly {
		return "", errors.New("target mode is invalid")
	}
	return mode, nil
}

// ComputeAbsenceToken 生成 Authoring 与 Change Control 共享的版本化缺失证明；它不是文件内容哈希。
func ComputeAbsenceToken(workspaceID foundation.ID, targetPath string) (string, error) {
	canonicalPath, err := ValidateTargetPath(targetPath)
	if workspaceID == "" || err != nil || canonicalPath != targetPath {
		return "", errors.New("absence token target is invalid")
	}
	payload := struct {
		Schema  string `json:"schema"`
		Payload struct {
			WorkspaceID foundation.ID `json:"workspace_id"`
			TargetPath  string        `json:"target_path"`
		} `json:"payload"`
	}{Schema: "workspace-target-absent/v1"}
	payload.Payload.WorkspaceID = workspaceID
	payload.Payload.TargetPath = targetPath
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return absenceTokenPrefix + hex.EncodeToString(sum[:]), nil
}

// ValidAbsenceToken 校验版本化 absence token 的格式。
func ValidAbsenceToken(value string) bool {
	return strings.HasPrefix(value, absenceTokenPrefix) && ValidHash(strings.TrimPrefix(value, absenceTokenPrefix))
}

// ValidateTargetBaseVersion 将基线版本绑定到 Workspace、路径和目标模式。
func ValidateTargetBaseVersion(workspaceID foundation.ID, targetPath string, mode TargetMode, baseVersion string) error {
	canonicalPath, err := ValidateTargetPath(targetPath)
	mode, modeErr := ValidateTargetMode(mode)
	if workspaceID == "" || err != nil || modeErr != nil || canonicalPath != targetPath {
		return ErrWritebackInvalidInput
	}
	if mode == TargetModeReplace {
		if !ValidHash(baseVersion) {
			return ErrWritebackInvalidInput
		}
		return nil
	}
	expected, err := ComputeAbsenceToken(workspaceID, targetPath)
	if err != nil || baseVersion != expected || !ValidAbsenceToken(baseVersion) {
		return ErrWritebackInvalidInput
	}
	return nil
}

// ComputeChangeHashForTarget 生成模式绑定的 Change Hash；REPLACE 保持旧 v1 字节不变。
func ComputeChangeHashForTarget(workspaceID foundation.ID, targetPath string, mode TargetMode, baseVersion, content string) (string, error) {
	mode, err := ValidateTargetMode(mode)
	if err != nil || ValidateTargetBaseVersion(workspaceID, targetPath, mode, baseVersion) != nil {
		return "", ErrWritebackInvalidInput
	}
	return computeChangeHashForMode(targetPath, mode, baseVersion, content)
}

func computeChangeHashForMode(targetPath string, mode TargetMode, baseVersion, content string) (string, error) {
	mode, err := ValidateTargetMode(mode)
	canonicalPath, pathErr := ValidateTargetPath(targetPath)
	if err != nil || pathErr != nil || canonicalPath != targetPath {
		return "", ErrWritebackInvalidInput
	}
	if mode == TargetModeReplace {
		if !ValidHash(baseVersion) {
			return "", ErrWritebackInvalidInput
		}
		return ComputeChangeHash(targetPath, baseVersion, content), nil
	}
	if !ValidAbsenceToken(baseVersion) {
		return "", ErrWritebackInvalidInput
	}
	normalizedContent := strings.ReplaceAll(content, "\r\n", "\n")
	canonical := fmt.Sprintf("zhixu-change-v2\ntarget-mode:%s\ntarget-path-length:%d\ntarget-path:%s\nbase-version:%s\ncontent-length:%d\ncontent:\n%s", mode, len(targetPath), targetPath, baseVersion, len(normalizedContent), normalizedContent)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
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

// ComputeRequestHashWithTargetMode 将 CREATE_ONLY 请求绑定到 absence token；REPLACE 保持 v2 字节兼容。
func ComputeRequestHashWithTargetMode(workspaceID foundation.ID, targetPath string, mode TargetMode, baseVersion, content, evidence string, riskLevel ProposalRiskLevel, risk, rollback string) (string, error) {
	mode, err := ValidateTargetMode(mode)
	if err != nil || ValidateTargetBaseVersion(workspaceID, targetPath, mode, baseVersion) != nil {
		return "", ErrWritebackInvalidInput
	}
	if mode == TargetModeReplace {
		return ComputeRequestHashWithRiskLevel(workspaceID, targetPath, baseVersion, content, evidence, riskLevel, risk, rollback)
	}
	normalizedRiskLevel, err := ParseProposalRiskLevel(riskLevel)
	if err != nil {
		return "", err
	}
	payload := struct {
		SchemaVersion string            `json:"schema_version"`
		WorkspaceID   string            `json:"workspace_id"`
		TargetPath    string            `json:"target_path"`
		TargetMode    TargetMode        `json:"target_mode"`
		BaseVersion   string            `json:"base_version"`
		Content       string            `json:"content"`
		Evidence      string            `json:"evidence"`
		RiskLevel     ProposalRiskLevel `json:"risk_level"`
		Risk          string            `json:"risk"`
		Rollback      string            `json:"rollback"`
	}{"proposal-create-request/v3", string(workspaceID), targetPath, mode, baseVersion, strings.ReplaceAll(content, "\r\n", "\n"), evidence, normalizedRiskLevel, risk, rollback}
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
