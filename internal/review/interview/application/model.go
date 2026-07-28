// Package application 编排 Interview、确定性评分、报告及 Learning Path 持久化边界。
package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

const (
	// ArtifactKindInterviewDocument 是 Interview 报告对应的 Artifact 意图。
	ArtifactKindInterviewDocument = "INTERVIEW_DOC"
	// ArtifactKindLearningPath 是 Learning Path 对应的 Artifact 意图。
	ArtifactKindLearningPath = "LEARNING_PATH"
	// maxContextEntries 限制一次评分可接收的用户确认 Memory 数量。
	maxContextEntries = 20
	// maxContextEntryBytes 限制单条偏好或上下文 JSON 的大小。
	maxContextEntryBytes = 16 * 1024
	// MaxSessionListLimit 限制一次 Interview Session 列表查询的最大记录数。
	MaxSessionListLimit = 100
	// MaxCompletionMaintenanceBatch 限制一次清理的过期 Completion reservation 数量。
	MaxCompletionMaintenanceBatch = 50
)

// StartCommand 创建题目已经冻结的可恢复 Interview Session。
type StartCommand struct {
	WorkspaceID    foundation.ID
	Config         domain.Config
	IdempotencyKey string
}

// SubmitTurnCommand 提交当前题目的用户回答；评分字段永不来自调用方。
type SubmitTurnCommand struct {
	WorkspaceID    foundation.ID
	SessionID      foundation.ID
	QuestionID     foundation.ID
	UserAnswer     string
	IdempotencyKey string
}

// CompleteCommand 固定报告与 Learning Path；ManualEnd 只允许结束未答题目。
type CompleteCommand struct {
	WorkspaceID    foundation.ID
	SessionID      foundation.ID
	ManualEnd      bool
	IdempotencyKey string
}

// UpdatePathStepCommand 更新一条学习路径步骤的用户进度。
type UpdatePathStepCommand struct {
	WorkspaceID     foundation.ID
	PathID          foundation.ID
	StepID          foundation.ID
	ExpectedVersion int64
	Status          domain.StepStatus
	IdempotencyKey  string
}

// UpdatePathStatusCommand 暂停、恢复或完成一条 Learning Path。
type UpdatePathStatusCommand struct {
	WorkspaceID     foundation.ID
	PathID          foundation.ID
	ExpectedVersion int64
	Status          domain.PathStatus
	IdempotencyKey  string
}

// SuggestMemoryCandidateCommand 从一条已持久化学习步骤显式创建 Memory Candidate。
type SuggestMemoryCandidateCommand struct {
	WorkspaceID    foundation.ID
	SessionID      foundation.ID
	PathID         foundation.ID
	StepID         foundation.ID
	IdempotencyKey string
}

// StartResult 返回冻结的 Session 与初始题目。
type StartResult struct {
	Session   domain.Session    `json:"session"`
	Questions []domain.Question `json:"questions"`
	Replayed  bool              `json:"replayed"`
}

// SubmitTurnResult 返回持久 Turn、可能新增的追问和当前下一题。
type SubmitTurnResult struct {
	Turn         domain.Turn      `json:"turn"`
	FollowUp     *domain.Question `json:"follow_up,omitempty"`
	NextQuestion *domain.Question `json:"next_question,omitempty"`
	Replayed     bool             `json:"replayed"`
}

// CompleteResult 返回一次不可变的报告和只保存进度事实的 Learning Path。
type CompleteResult struct {
	Session  domain.Session      `json:"session"`
	Report   domain.Report       `json:"report"`
	Path     domain.LearningPath `json:"path"`
	Steps    []domain.PathStep   `json:"steps"`
	Replayed bool                `json:"replayed"`
}

// PathStepResult 返回变更后的路径和步骤快照。
type PathStepResult struct {
	Path     domain.LearningPath `json:"path"`
	Step     domain.PathStep     `json:"step"`
	Replayed bool                `json:"replayed"`
}

// PathStatusResult 返回变更后的路径状态。
type PathStatusResult struct {
	Path     domain.LearningPath `json:"path"`
	Replayed bool                `json:"replayed"`
}

// MemoryCandidateResult 返回由 Interview 受控来源创建的候选标识。
type MemoryCandidateResult struct {
	MemoryID foundation.ID `json:"memory_id"`
	Replayed bool          `json:"replayed"`
}

// Snapshot 是恢复 Interview 时读取的完整持久事实。
type Snapshot struct {
	Session   domain.Session       `json:"session"`
	Questions []domain.Question    `json:"questions"`
	Turns     []domain.Turn        `json:"turns"`
	Report    *domain.Report       `json:"report,omitempty"`
	Path      *domain.LearningPath `json:"path,omitempty"`
	Steps     []domain.PathStep    `json:"steps,omitempty"`
}

// SessionCursor 是按开始时间与 Session ID 排序的稳定 keyset 边界。
type SessionCursor struct {
	StartedAt time.Time
	ID        foundation.ID
}

// SessionListQuery 读取一个 Workspace 内的有界 Interview Session 页面。
type SessionListQuery struct {
	WorkspaceID foundation.ID
	Limit       int
	After       *SessionCursor
}

// SessionListPage 返回不含 Question、Turn、Evidence 或 receipt 的 Session 摘要页。
type SessionListPage struct {
	Items []domain.Session
	Next  *SessionCursor
}

// PathSnapshot 是独立更新 Learning Path 时的最小恢复事实。
type PathSnapshot struct {
	Path  domain.LearningPath `json:"path"`
	Steps []domain.PathStep   `json:"steps"`
}

// Selection 约束选题端口只能读取 Workspace 内的正式知识范围。
type Selection struct {
	WorkspaceID foundation.ID
	Scope       domain.Scope
	Limit       int
}

// QuestionSource 从已确认 Claim 的 SUPPORTS Evidence 返回冻结题目材料。
type QuestionSource interface {
	Select(context.Context, Selection) ([]domain.Material, error)
}

// ContextQuery 只暴露 Interview 需要的 Workspace 与当前任务范围。
// 具体 owner 由生产 ContextLoader 绑定，调用方凭据不能决定长期 Memory owner。
type ContextQuery struct {
	WorkspaceID foundation.ID
	TaskScopeID foundation.ID
	Limit       int
}

// PersonalContext 是用户确认的偏好与非事实上下文。
// 它故意不包含 Citation、Evidence、Source 或 Memory identity。
type PersonalContext struct {
	Preferences []json.RawMessage `json:"preferences"`
	Context     []json.RawMessage `json:"context"`
}

// ContextLoader 为 Interview 加载当前任务可用的用户确认 Memory。
// 实现必须按稳定 owner、Workspace、task scope、ACTIVE、确认与有效期过滤。
type ContextLoader interface {
	Load(context.Context, ContextQuery) (PersonalContext, error)
}

// MemoryCandidateRequest 是 Interview 向 Memory 模块提交的受控候选 DTO。
// 来源类型、稳定 owner 和审计事实必须由 Adapter 固定，不能来自 HTTP 请求。
type MemoryCandidateRequest struct {
	WorkspaceID    foundation.ID
	SessionID      foundation.ID
	PathID         foundation.ID
	StepID         foundation.ID
	Content        json.RawMessage
	IdempotencyKey string
}

// MemoryCandidateWriter 创建仍需用户确认的 Interview 来源候选。
type MemoryCandidateWriter interface {
	CreateCandidate(context.Context, MemoryCandidateRequest) (MemoryCandidateResult, error)
}

// ScoreInput 是受控评分器收到的完整题目快照、用户文本和非事实偏好上下文。
type ScoreInput struct {
	Question        domain.Question
	UserAnswer      string
	PersonalContext PersonalContext
}

// Scorer 是面试的受控服务端评分器。
type Scorer interface {
	// Version 返回会写入 Turn 的评分器版本。
	Version() string
	// Score 返回完全绑定 Question Evidence 的确定性评分；PersonalContext 不能满足证据要求。
	Score(context.Context, ScoreInput) (domain.Score, error)
}

// ArtifactDraftSection 是 Interview 确定性渲染的一节正文及完整内部证据绑定。
type ArtifactDraftSection struct {
	Key      string
	Title    string
	Markdown string
	Evidence []domain.EvidenceRef
}

// ArtifactVisibilityHoldRole 标识 Interview completion 中暂不可见的产物角色。
type ArtifactVisibilityHoldRole string

const (
	// ArtifactVisibilityHoldRoleReport 标识面试报告 Artifact。
	ArtifactVisibilityHoldRoleReport ArtifactVisibilityHoldRole = "REPORT"
	// ArtifactVisibilityHoldRolePath 标识学习路径 Artifact。
	ArtifactVisibilityHoldRolePath ArtifactVisibilityHoldRole = "PATH"
)

// ArtifactVisibilityHold 把一个草稿绑定到负责最终提交的 Interview Session。
// Owner type 由 Bridge 固定为 INTERVIEW_COMPLETE，调用方不能扩展。
type ArtifactVisibilityHold struct {
	OwnerID foundation.ID
	Role    ArtifactVisibilityHoldRole
	// AttemptDigest 绑定 Prepare 阶段冻结的完整 Completion 内容。
	AttemptDigest string
}

// ArtifactDraftRequest 是 Interview 对 Artifact 模块唯一的窄桥接 DTO。
// 正文只来自服务端派生事实，Citation 仍由 Artifact 服务端重新验证并构造。
type ArtifactDraftRequest struct {
	WorkspaceID        foundation.ID
	Kind               string
	Title              string
	Scope              json.RawMessage
	Section            ArtifactDraftSection
	IdempotencyBaseKey string
	VisibilityHold     ArtifactVisibilityHold
}

// ArtifactDraftResult 是 Artifact bridge 完成固定大纲和正文后的 DRAFT Revision 绑定。
type ArtifactDraftResult struct {
	Binding domain.ArtifactBinding
}

// ArtifactBridge 由 Composition Root 实现并只调用 Artifact 应用命令。
// 该 port 必须逐阶段精确重放，不能由 Interview 直接写 Artifact 表。
type ArtifactBridge interface {
	CreateDraft(context.Context, ArtifactDraftRequest) (ArtifactDraftResult, error)
}

// CompletionReservationStatus 标识跨模块 Completion 尝试的持久状态。
type CompletionReservationStatus string

const (
	// CompletionReservationPending 表示冻结快照仍在构建或等待最终提交。
	CompletionReservationPending CompletionReservationStatus = "PENDING"
	// CompletionReservationCompleted 表示报告、路径和 Artifact 绑定已经原子提交。
	CompletionReservationCompleted CompletionReservationStatus = "COMPLETED"
	// CompletionReservationAbandoned 表示超时尝试已被维护任务放弃。
	CompletionReservationAbandoned CompletionReservationStatus = "ABANDONED"
)

// CompletionArtifactBindings 保存 Completion 最终提交的两组 Artifact 绑定。
type CompletionArtifactBindings struct {
	// ReportID 是最终 Interview Report 标识。
	ReportID foundation.ID
	// ReportArtifact 是 Report 对应的不可变 Artifact Revision 绑定。
	ReportArtifact domain.ArtifactBinding
	// PathID 是最终 Learning Path 标识。
	PathID foundation.ID
	// PathArtifact 是 Learning Path 对应的不可变 Artifact Revision 绑定。
	PathArtifact domain.ArtifactBinding
}

// CompletionReservation 冻结一次 Interview Completion 的请求、快照和恢复绑定。
type CompletionReservation struct {
	// WorkspaceID 是 reservation 所属 Workspace。
	WorkspaceID foundation.ID
	// SessionID 是被关闭的 Interview Session。
	SessionID foundation.ID
	// IdempotencyKey 是客户端 Completion 命令键。
	IdempotencyKey string
	// RequestHash 绑定完整 Completion 请求。
	RequestHash string
	// ManualEnd 标识是否允许跳过未回答问题。
	ManualEnd bool
	// SnapshotVersion 是 Session 行锁下冻结的版本。
	SnapshotVersion int64
	// ArtifactDigest 绑定完整 Report/Path 渲染内容；未 Prepare 时为空。
	ArtifactDigest string
	// Bindings 是 COMPLETED reservation 的最终 Artifact 绑定。
	Bindings CompletionArtifactBindings
	// Status 是当前 reservation 生命周期状态。
	Status CompletionReservationStatus
	// CreatedAt 是数据库首次创建 reservation 的时间。
	CreatedAt time.Time
	// PreparedAt 是首次持久化 Artifact digest 的时间。
	PreparedAt *time.Time
	// CompletedAt 是最终事务提交 Completion 的时间。
	CompletedAt *time.Time
	// AbandonedAt 是维护任务放弃超时尝试的时间。
	AbandonedAt *time.Time
	// UpdatedAt 是数据库最近一次状态变化时间。
	UpdatedAt time.Time
}

// BeginCompleteRecord 是在 Session 行锁下创建或恢复 reservation 的输入。
type BeginCompleteRecord struct {
	// WorkspaceID 是命令所属 Workspace。
	WorkspaceID foundation.ID
	// SessionID 是待完成的 Interview Session。
	SessionID foundation.ID
	// ManualEnd 标识是否允许冻结包含未回答问题的快照。
	ManualEnd bool
	// IdempotencyKey 是客户端 Completion 命令键。
	IdempotencyKey string
	// RequestHash 绑定完整 Completion 请求。
	RequestHash string
}

// BeginCompleteResult 返回冻结快照，或并发完成后的终态精确重放。
type BeginCompleteResult struct {
	// Reservation 是当前持久 reservation。
	Reservation CompletionReservation
	// Snapshot 是 reservation 锁定的 Session 事实。
	Snapshot Snapshot
	// Terminal 在同键已经完成时返回不可变结果。
	Terminal *CompleteResult
}

// PrepareCompleteRecord 把完整 Artifact digest 绑定到冻结 reservation。
type PrepareCompleteRecord struct {
	// WorkspaceID 是命令所属 Workspace。
	WorkspaceID foundation.ID
	// SessionID 是待完成的 Interview Session。
	SessionID foundation.ID
	// IdempotencyKey 是客户端 Completion 命令键。
	IdempotencyKey string
	// RequestHash 绑定完整 Completion 请求。
	RequestHash string
	// ManualEnd 必须与 Begin 阶段一致。
	ManualEnd bool
	// SnapshotVersion 必须与 Begin 阶段冻结版本一致。
	SnapshotVersion int64
	// ArtifactDigest 是完整 Report/Path Artifact 内容摘要。
	ArtifactDigest string
}

// PrepareCompleteResult 返回已准备的 reservation，或并发完成后的终态重放。
type PrepareCompleteResult struct {
	// Reservation 是已绑定 digest 的 reservation。
	Reservation CompletionReservation
	// Terminal 在同键已经完成时返回不可变结果。
	Terminal *CompleteResult
}

// CompletionMaintenanceResult 汇总一次有界过期维护的数据库变更。
type CompletionMaintenanceResult struct {
	// AbandonedReservations 是转为 ABANDONED 的 reservation 数量。
	AbandonedReservations int
	// OrphanedHolds 是转为 ORPHANED 的 ACTIVE hold 数量。
	OrphanedHolds int
}

// StartRecord 是 Store 原子创建 Session shell、初始题目和 receipt 的输入。
type StartRecord struct {
	Session        domain.Session
	Questions      []domain.Question
	IdempotencyKey string
	RequestHash    string
}

// SubmitRecord 是 Store 原子写入 Turn、可选追问和 receipt 的输入。
type SubmitRecord struct {
	WorkspaceID     foundation.ID
	SessionID       foundation.ID
	ExpectedVersion int64
	Question        domain.Question
	Turn            domain.Turn
	FollowUp        *domain.Question
	IdempotencyKey  string
	RequestHash     string
}

// CompleteRecord 是 Store 原子关闭 Session、固定 Report/Path 和 receipt 的输入。
type CompleteRecord struct {
	WorkspaceID     foundation.ID
	SessionID       foundation.ID
	ExpectedVersion int64
	ManualEnd       bool
	// ArtifactDigest 必须精确匹配 Prepare 阶段的完整内容摘要。
	ArtifactDigest string
	Report         domain.Report
	Path           domain.LearningPath
	Steps          []domain.PathStep
	IdempotencyKey string
	RequestHash    string
	At             time.Time
}

// UpdatePathStepRecord 是 Store 原子推进单步骤和 receipt 的输入。
type UpdatePathStepRecord struct {
	WorkspaceID     foundation.ID
	PathID          foundation.ID
	StepID          foundation.ID
	ExpectedVersion int64
	Status          domain.StepStatus
	IdempotencyKey  string
	RequestHash     string
	At              time.Time
}

// UpdatePathStatusRecord 是 Store 原子推进路径生命周期和 receipt 的输入。
type UpdatePathStatusRecord struct {
	WorkspaceID     foundation.ID
	PathID          foundation.ID
	ExpectedVersion int64
	Status          domain.PathStatus
	IdempotencyKey  string
	RequestHash     string
	At              time.Time
}

// Store 是 Interview 专属持久化端口；不得实现为 review_answer 或 FSRS 的包装。
type Store interface {
	FindStartReplay(context.Context, foundation.ID, string, string) (StartResult, bool, error)
	Start(context.Context, StartRecord) (StartResult, error)
	Get(context.Context, foundation.ID, foundation.ID) (Snapshot, error)
	List(context.Context, SessionListQuery) (SessionListPage, error)
	FindSubmitReplay(context.Context, foundation.ID, string, string) (SubmitTurnResult, bool, error)
	Submit(context.Context, SubmitRecord) (SubmitTurnResult, error)
	FindCompleteReplay(context.Context, foundation.ID, string, string) (CompleteResult, bool, error)
	// BeginComplete 在 Session 行锁下冻结或恢复 Completion reservation。
	BeginComplete(context.Context, BeginCompleteRecord) (BeginCompleteResult, error)
	// PrepareComplete 持久化完整 Artifact digest 并恢复匹配的 hidden hold。
	PrepareComplete(context.Context, PrepareCompleteRecord) (PrepareCompleteResult, error)
	Complete(context.Context, CompleteRecord) (CompleteResult, error)
	GetPath(context.Context, foundation.ID, foundation.ID) (PathSnapshot, error)
	FindPathStepReplay(context.Context, foundation.ID, string, string) (PathStepResult, bool, error)
	UpdatePathStep(context.Context, UpdatePathStepRecord) (PathStepResult, error)
	FindPathStatusReplay(context.Context, foundation.ID, string, string) (PathStatusResult, bool, error)
	UpdatePathStatus(context.Context, UpdatePathStatusRecord) (PathStatusResult, error)
}

// Dependencies 是构造 Service 时必须提供的运行依赖。
type Dependencies struct {
	Store           Store
	QuestionSource  QuestionSource
	ContextLoader   ContextLoader
	CandidateWriter MemoryCandidateWriter
	Scorer          Scorer
	ArtifactBridge  ArtifactBridge
	IDGenerator     foundation.IDGenerator
	Clock           foundation.Clock
}
