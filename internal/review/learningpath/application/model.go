// Package application 编排共享 Learning Path 的 Review 派生、Artifact 绑定和进度更新。
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
)

const (
	// ArtifactKind 是 Learning Path 唯一允许绑定的 Artifact 类型。
	ArtifactKind = "LEARNING_PATH"
	// ReviewArtifactSectionKey 是 Review Path 草稿唯一的章节键。
	ReviewArtifactSectionKey = "review-gap"
	// ReviewArtifactCreator 冻结草稿 Revision 的创建来源。
	ReviewArtifactCreator = "AGENT"
	// ReviewArtifactCoverageStatus 冻结草稿章节的覆盖状态。
	ReviewArtifactCoverageStatus = "COVERED"
	// ReviewArtifactPromptVersion 冻结确定性 renderer 的提示版本。
	ReviewArtifactPromptVersion = "review-learning-path-renderer/v1"
	// ReviewArtifactModelVersion 冻结确定性 renderer 的模型身份。
	ReviewArtifactModelVersion = "review-deterministic-renderer/v1"
	// ReviewArtifactWorkflowVersion 冻结 Artifact 工作流定义版本。
	ReviewArtifactWorkflowVersion = "review-learning-path/v1"
	// ReviewArtifactSchemaVersion 冻结最终 Draft 的结构版本。
	ReviewArtifactSchemaVersion = "review-learning-path-artifact/v1"
	// MaxMaintenanceBatch 限制一次超时预留维护的数量。
	MaxMaintenanceBatch = 50
	// ReviewSnapshotSchemaVersion 标识 reservation 内冻结的 Review 来源快照。
	ReviewSnapshotSchemaVersion = "review-learning-path-source/v1"
	// CommandTypeCreateReviewPath 是 Review Path 创建 receipt 的稳定枚举。
	CommandTypeCreateReviewPath = "CREATE_REVIEW_PATH"
	// CommandTypePathStep 是 Step 推进 receipt 的稳定枚举。
	CommandTypePathStep = "PATH_STEP"
	// CommandTypePathStatus 是 Path 生命周期 receipt 的稳定枚举。
	CommandTypePathStatus = "PATH_STATUS"
)

// CreateReviewCommand 只允许调用方提供 Workspace、Answer 和幂等键。
type CreateReviewCommand struct {
	WorkspaceID    foundation.ID
	ReviewAnswerID foundation.ID
	IdempotencyKey string
}

// UpdatePathStatusCommand 更新共享 Path 的用户生命周期。
type UpdatePathStatusCommand struct {
	WorkspaceID     foundation.ID
	PathID          foundation.ID
	ExpectedVersion int64
	Status          domain.Status
	IdempotencyKey  string
}

// UpdateStepCommand 更新单个 Path Step 的用户进度。
type UpdateStepCommand struct {
	WorkspaceID     foundation.ID
	PathID          foundation.ID
	StepID          foundation.ID
	ExpectedVersion int64
	Status          domain.StepStatus
	IdempotencyKey  string
}

// Result 是一个可精确重放的 Path 写命令响应。
type Result struct {
	Path     domain.Path   `json:"path"`
	Steps    []domain.Step `json:"steps,omitempty"`
	Replayed bool          `json:"replayed"`
}

// StepResult 是单步骤命令的响应。
type StepResult struct {
	Path     domain.Path `json:"path"`
	Step     domain.Step `json:"step"`
	Replayed bool        `json:"replayed"`
}

// ReviewSnapshot 是存储层从 append-only Answer 与当前正式证据冻结的服务端输入。
type ReviewSnapshot struct {
	SchemaVersion  string        `json:"schema_version"`
	ReviewAnswerID foundation.ID `json:"review_answer_id"`
	ReviewCardID   foundation.ID `json:"review_card_id"`
	ScorerVersion  string        `json:"scorer_version"`
	PolicyVersion  string        `json:"policy_version"`
	Gap            domain.Gap    `json:"gap"`
}

// ReservationStatus 描述 Path 创建预留状态。
type ReservationStatus string

const (
	// ReservationPending 表示 Artifact 尚未完全绑定。
	ReservationPending ReservationStatus = "PENDING"
	// ReservationCompleted 表示路径和 receipt 已完整写入。
	ReservationCompleted ReservationStatus = "COMPLETED"
	// ReservationAbandoned 表示超时维护放弃了未完成预留。
	ReservationAbandoned ReservationStatus = "ABANDONED"
)

// Reservation 是单一 Review Answer 创建路径的可恢复事实。
type Reservation struct {
	WorkspaceID          foundation.ID
	ReviewAnswerID       foundation.ID
	IdempotencyKey       string
	RequestHash          string
	SourceSnapshot       ReviewSnapshot
	SourceSnapshotDigest string
	AttemptNo            int64
	ArtifactDigest       string
	Status               ReservationStatus
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// DraftRequest 是传给 Artifact bridge 的完全服务端构造输入。
type DraftRequest struct {
	WorkspaceID        foundation.ID
	ReviewAnswerID     foundation.ID
	ArtifactDigest     string
	IdempotencyBaseKey string
	Title              string
	Scope              []byte
	Markdown           string
	Citations          []domain.Citation
}

// ArtifactBridge 创建或恢复一个由 Learning Path reservation 持有的隐藏 Artifact 草稿。
type ArtifactBridge interface {
	CreateDraft(context.Context, DraftRequest) (domain.ArtifactBinding, error)
}

// Store 是共享 Path 的持久化、来源投影和 receipt 边界。
type Store interface {
	FindCreateReplay(context.Context, foundation.ID, string, string) (Result, bool, error)
	BeginReviewCreate(context.Context, foundation.ID, foundation.ID, string, string) (Reservation, ReviewSnapshot, *Result, error)
	PrepareReviewCreate(context.Context, Reservation, string) (Reservation, *Result, error)
	CompleteReviewCreate(context.Context, Reservation, domain.Path, []domain.Step) (Result, error)
	GetByReviewAnswer(context.Context, foundation.ID, foundation.ID) (Result, error)
	Get(context.Context, foundation.ID, foundation.ID) (Result, error)
	FindPathStatusReplay(context.Context, foundation.ID, string, string, int64) (Result, bool, error)
	UpdatePathStatus(context.Context, UpdatePathStatusRecord) (Result, error)
	FindStepReplay(context.Context, foundation.ID, string, string, int64) (StepResult, bool, error)
	UpdateStep(context.Context, UpdateStepRecord) (StepResult, error)
	MaintainReservations(context.Context, time.Time, int) (int, error)
}

// UpdatePathStatusRecord 是 Store 原子状态 CAS 的输入。
type UpdatePathStatusRecord struct {
	WorkspaceID     foundation.ID
	PathID          foundation.ID
	ExpectedVersion int64
	Status          domain.Status
	IdempotencyKey  string
	RequestHash     string
	At              time.Time
}

// UpdateStepRecord 是 Store 原子步骤 CAS 的输入。
type UpdateStepRecord struct {
	WorkspaceID     foundation.ID
	PathID          foundation.ID
	StepID          foundation.ID
	ExpectedVersion int64
	Status          domain.StepStatus
	IdempotencyKey  string
	RequestHash     string
	At              time.Time
}

// Dependencies 是构造共享 Path 服务时必须具备的依赖。
type Dependencies struct {
	Store          Store
	ArtifactBridge ArtifactBridge
	Clock          foundation.Clock
}

// CanonicalReviewSnapshot 编码并摘要一个可恢复、顺序稳定的来源快照。
func CanonicalReviewSnapshot(snapshot ReviewSnapshot) (ReviewSnapshot, []byte, string, error) {
	canonical := snapshot
	gap, err := domain.CanonicalGap(snapshot.Gap)
	if err != nil {
		return ReviewSnapshot{}, nil, "", err
	}
	canonical.Gap = gap
	if err := ValidateReviewSnapshot(canonical); err != nil {
		return ReviewSnapshot{}, nil, "", err
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return ReviewSnapshot{}, nil, "", domain.InvalidError(domain.ErrorCodePersistenceInvalid, "review path snapshot cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return canonical, encoded, hex.EncodeToString(digest[:]), nil
}

// ValidateReviewSnapshot 校验 Answer、Card、评分器、策略与 Gap 的冻结绑定。
func ValidateReviewSnapshot(snapshot ReviewSnapshot) error {
	if snapshot.SchemaVersion != ReviewSnapshotSchemaVersion || snapshot.PolicyVersion != domain.ReviewGapSchemaVersion ||
		!applicationID(snapshot.ReviewAnswerID) || !applicationID(snapshot.ReviewCardID) || snapshot.ReviewAnswerID == snapshot.ReviewCardID ||
		strings.TrimSpace(snapshot.ScorerVersion) != snapshot.ScorerVersion || snapshot.ScorerVersion == "" || len(snapshot.ScorerVersion) > 128 {
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "review path source snapshot is invalid")
	}
	if err := domain.ValidateGap(snapshot.Gap); err != nil {
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "review path source snapshot gap is invalid")
	}
	return nil
}

func applicationID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
