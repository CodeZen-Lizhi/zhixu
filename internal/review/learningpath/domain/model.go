package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// SchemaVersion 标识共享 Learning Path 的持久化语义。
	SchemaVersion = "learning-path/v1"
	// ReviewGapSchemaVersion 标识由 Review Answer 派生的固定策略。
	ReviewGapSchemaVersion = "review-gap/v1"
	// MaxSteps 限制单条路径包含的可执行步骤数。
	MaxSteps      = 16
	maxGapItems   = 128
	maxTitleBytes = 512
	maxTextBytes  = 4096
)

// Origin 表示生成 Path 的唯一业务来源。
type Origin string

const (
	// OriginInterview 表示来源为 Interview 报告。
	OriginInterview Origin = "INTERVIEW"
	// OriginReview 表示来源为已持久化 Review Answer。
	OriginReview Origin = "REVIEW"
)

// Status 是 Learning Path 生命周期状态。
type Status string

const (
	// StatusActive 表示路径可推进。
	StatusActive Status = "ACTIVE"
	// StatusPaused 表示路径暂时冻结。
	StatusPaused Status = "PAUSED"
	// StatusCompleted 表示路径全部结束。
	StatusCompleted Status = "COMPLETED"
)

// StepStatus 是单一学习步骤的用户进度。
type StepStatus string

const (
	// StepStatusPending 表示尚未开始。
	StepStatusPending StepStatus = "PENDING"
	// StepStatusInProgress 表示正在学习。
	StepStatusInProgress StepStatus = "IN_PROGRESS"
	// StepStatusCompleted 表示用户已完成。
	StepStatusCompleted StepStatus = "COMPLETED"
	// StepStatusSkipped 表示用户主动跳过。
	StepStatusSkipped StepStatus = "SKIPPED"
)

// EvidenceRef 是已激活索引、Chunk、Source 与 Span 的完整可验证引用。
type EvidenceRef struct {
	IndexVersionID  foundation.ID `json:"index_version_id"`
	ChunkID         foundation.ID `json:"chunk_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
}

// Citation 是写入 Artifact 和 Path 的来源绑定。
type Citation struct {
	EvidenceRef
	ClaimID      foundation.ID  `json:"claim_id"`
	TopicID      *foundation.ID `json:"topic_id,omitempty"`
	EvidenceHash string         `json:"evidence_hash"`
}

// ArtifactBinding 标识唯一的 Learning Path Artifact 草稿。
type ArtifactBinding struct {
	ArtifactID         foundation.ID `json:"artifact_id"`
	ArtifactRevisionID foundation.ID `json:"artifact_revision_id"`
	ArtifactVersion    int64         `json:"artifact_version"`
}

// Path 是跨 Interview 与 Review 共用的路径聚合。
type Path struct {
	ID                  foundation.ID   `json:"id"`
	WorkspaceID         foundation.ID   `json:"workspace_id"`
	OriginType          Origin          `json:"origin_type"`
	InterviewSessionID  *foundation.ID  `json:"interview_session_id,omitempty"`
	InterviewReportID   *foundation.ID  `json:"interview_report_id,omitempty"`
	ReviewAnswerID      *foundation.ID  `json:"review_answer_id,omitempty"`
	SourcePolicyVersion string          `json:"source_policy_version"`
	Artifact            ArtifactBinding `json:"artifact"`
	Status              Status          `json:"status"`
	Version             int64           `json:"version"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// Step 是 Path 内一个引用正式知识的可执行动作。
type Step struct {
	ID              foundation.ID  `json:"id"`
	WorkspaceID     foundation.ID  `json:"workspace_id"`
	PathID          foundation.ID  `json:"path_id"`
	StepNo          int            `json:"step_no"`
	ClaimID         foundation.ID  `json:"claim_id"`
	TopicID         *foundation.ID `json:"topic_id,omitempty"`
	SourceVersionID foundation.ID  `json:"source_version_id"`
	SourceSpanID    foundation.ID  `json:"source_span_id"`
	EvidenceHash    string         `json:"evidence_hash"`
	Title           string         `json:"title"`
	Rationale       string         `json:"rationale"`
	Status          StepStatus     `json:"status"`
	Version         int64          `json:"version"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// Gap 是服务端从评分派生的受控学习缺口。
type Gap struct {
	Errors      []string   `json:"errors"`
	Omissions   []string   `json:"omissions"`
	Correctness float64    `json:"correctness"`
	Coverage    float64    `json:"coverage"`
	Boundaries  float64    `json:"boundaries"`
	Citations   []Citation `json:"citations"`
}

// Actionable 表示固定 review-gap/v1 是否要求创建路径。
func (gap Gap) Actionable() bool {
	return len(gap.Errors) > 0 || len(gap.Omissions) > 0 || (gap.Correctness+gap.Coverage+gap.Boundaries)/3 < 0.75
}

// Digest 返回一条可恢复 Artifact 草稿的稳定摘要。
func (gap Gap) Digest(answerID foundation.ID) (string, error) {
	if !validID(answerID) {
		return "", InvalidError(ErrorCodePathInvalid, "review gap digest input is invalid")
	}
	if err := ValidateGap(gap); err != nil {
		return "", InvalidError(ErrorCodePathInvalid, "review gap digest input is invalid")
	}
	copyValue, err := CanonicalGap(gap)
	if err != nil {
		return "", err
	}
	payload := struct {
		Schema string        `json:"schema"`
		Answer foundation.ID `json:"answer_id"`
		Gap    Gap           `json:"gap"`
	}{ReviewGapSchemaVersion, answerID, copyValue}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", InvalidError(ErrorCodePathInvalid, "review gap digest cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ValidatePath 验证共享路径来源、Artifact 绑定和状态。
func ValidatePath(path Path) error {
	if !validID(path.ID) || !validID(path.WorkspaceID) || path.ID == path.WorkspaceID || !validText(path.SourcePolicyVersion, 128) ||
		path.Version < 1 || path.CreatedAt.IsZero() || path.UpdatedAt.Before(path.CreatedAt) ||
		!validStatus(path.Status, StatusActive, StatusPaused, StatusCompleted) || !validArtifact(path.Artifact) {
		return InvalidError(ErrorCodePathInvalid, "learning path is invalid")
	}
	switch path.OriginType {
	case OriginInterview:
		if !validPointerID(path.InterviewSessionID) || !validPointerID(path.InterviewReportID) || path.ReviewAnswerID != nil {
			return InvalidError(ErrorCodePathInvalid, "interview path source is invalid")
		}
	case OriginReview:
		if !validPointerID(path.ReviewAnswerID) || path.InterviewSessionID != nil || path.InterviewReportID != nil || path.SourcePolicyVersion != ReviewGapSchemaVersion {
			return InvalidError(ErrorCodePathInvalid, "review path source is invalid")
		}
	default:
		return InvalidError(ErrorCodePathInvalid, "learning path origin is invalid")
	}
	return nil
}

// ValidateStep 验证一条可被用户推进的学习步骤。
func ValidateStep(step Step) error {
	if !validID(step.ID) || !validID(step.WorkspaceID) || !validID(step.PathID) || step.StepNo < 1 || step.StepNo > MaxSteps ||
		!validID(step.ClaimID) || !validOptionalID(step.TopicID) || !validID(step.SourceVersionID) || !validID(step.SourceSpanID) || !validHash(step.EvidenceHash) ||
		!validText(step.Title, maxTitleBytes) || !validText(step.Rationale, maxTextBytes) || step.Version < 1 || step.CreatedAt.IsZero() || step.UpdatedAt.Before(step.CreatedAt) ||
		!validStatus(step.Status, StepStatusPending, StepStatusInProgress, StepStatusCompleted, StepStatusSkipped) {
		return InvalidError(ErrorCodePathInvalid, "learning path step is invalid")
	}
	return nil
}

// ValidateCitation 验证 Citation 具备完整可追溯的索引与来源元组。
func ValidateCitation(citation Citation) error {
	if !validID(citation.ClaimID) || !validOptionalID(citation.TopicID) || !validID(citation.IndexVersionID) || !validID(citation.ChunkID) || !validID(citation.SourceVersionID) || !validID(citation.SourceSpanID) || !validHash(citation.EvidenceHash) {
		return InvalidError(ErrorCodeEvidenceStale, "learning path citation is incomplete")
	}
	return nil
}

// ValidateGap 验证服务端评分缺口的固定输入。
func ValidateGap(gap Gap) error {
	if len(gap.Citations) == 0 || len(gap.Citations) > maxGapItems || len(gap.Errors) > maxGapItems || len(gap.Omissions) > maxGapItems || !validScore(gap.Correctness) || !validScore(gap.Coverage) || !validScore(gap.Boundaries) {
		return InvalidError(ErrorCodePathInvalid, "review gap is invalid")
	}
	for _, item := range append(append([]string{}, gap.Errors...), gap.Omissions...) {
		if !validText(item, maxTextBytes) {
			return InvalidError(ErrorCodePathInvalid, "review gap feedback is invalid")
		}
	}
	seen := make(map[string]struct{}, len(gap.Citations))
	for _, citation := range gap.Citations {
		if err := ValidateCitation(citation); err != nil {
			return err
		}
		key := citationKey(citation)
		if _, duplicate := seen[key]; duplicate {
			return InvalidError(ErrorCodePathInvalid, "learning path citations are duplicated")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// CanonicalGap 返回用于快照、Artifact 和重试恢复的稳定 Gap 副本。
func CanonicalGap(gap Gap) (Gap, error) {
	if err := ValidateGap(gap); err != nil {
		return Gap{}, err
	}
	copyValue := gap
	copyValue.Errors = sortedStrings(gap.Errors)
	copyValue.Omissions = sortedStrings(gap.Omissions)
	copyValue.Citations = append([]Citation(nil), gap.Citations...)
	sort.Slice(copyValue.Citations, func(i, j int) bool {
		return citationKey(copyValue.Citations[i]) < citationKey(copyValue.Citations[j])
	})
	return copyValue, nil
}

// AllStepsTerminal 报告 Path 的每条 Step 是否都已完成或跳过。
func AllStepsTerminal(steps []Step) bool {
	if len(steps) == 0 {
		return false
	}
	for _, step := range steps {
		if step.Status != StepStatusCompleted && step.Status != StepStatusSkipped {
			return false
		}
	}
	return true
}

// CanTransitionPath 返回用户允许的 Path 生命周期转换。
func CanTransitionPath(from, to Status) bool {
	return (from == StatusActive && (to == StatusPaused || to == StatusCompleted)) || (from == StatusPaused && to == StatusActive)
}

// CanTransitionStep 返回用户允许的 Step 生命周期转换。
func CanTransitionStep(from, to StepStatus) bool {
	if !IsStepTransitionTarget(to) {
		return false
	}
	if from == StepStatusPending {
		return to == StepStatusInProgress || to == StepStatusCompleted || to == StepStatusSkipped
	}
	return from == StepStatusInProgress && (to == StepStatusCompleted || to == StepStatusSkipped)
}

// IsStepTransitionTarget 报告状态是否可作为用户进度命令的目标；PENDING 仅是读取态。
func IsStepTransitionTarget(status StepStatus) bool {
	return status == StepStatusInProgress || status == StepStatusCompleted || status == StepStatusSkipped
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
func validPointerID(value *foundation.ID) bool  { return value != nil && validID(*value) }
func validOptionalID(value *foundation.ID) bool { return value == nil || validID(*value) }
func validText(value string, limit int) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= limit && utf8.ValidString(value)
}
func validArtifact(value ArtifactBinding) bool {
	return validID(value.ArtifactID) && validID(value.ArtifactRevisionID) && value.ArtifactVersion > 0
}
func validStatus[T ~string](value T, values ...T) bool {
	for _, allowed := range values {
		if value == allowed {
			return true
		}
	}
	return false
}
func validScore(value float64) bool {
	return value >= 0 && value <= 1 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
func citationKey(value Citation) string {
	return string(value.ClaimID) + "\x00" + string(value.IndexVersionID) + "\x00" + string(value.ChunkID) + "\x00" + string(value.SourceVersionID) + "\x00" + string(value.SourceSpanID) + "\x00" + value.EvidenceHash
}
func sortedStrings(values []string) []string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	return copied
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
