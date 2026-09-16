package application

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"time"
)

const SynthesisManuscriptReviewVersion = "synthesis-manuscript-review/v1"
const MaxSynthesisManuscriptReviewTargets = MaxSynthesisGeneratedNotes

type SynthesisManuscriptHumanBinding struct {
	WorkspaceID   foundation.ID `json:"workspace_id"`
	ProcessingID  foundation.ID `json:"processing_id"`
	HumanTaskID   foundation.ID `json:"human_task_id"`
	RunID         foundation.ID `json:"run_id"`
	NodeRunID     foundation.ID `json:"node_run_id"`
	TargetVersion int64         `json:"target_version"`
}

// HumanAuthority 必须由已认证的当前 CallerCapabilities、真实 Workflow 任务/运行/节点归属
// 及当前 Root 共同组装。任务 Schema 必须绑定此次精确处理和完整目标集合。
// Authorize 仅为精确恢复持久结果而允许访问已提交任务；VerifyPending 必须锁定实际待处理任务，
// 重新核验不依赖 F 的 L/P/document/source/scope/root 及不可变模型和语义账本身份。
// 两个方法均不得要求旧模型或合并节点仍为 RUNNING。任务完成只激活后继 apply，
// 不会重跑合并或发布。预分配任务 ID、客户端布尔值或上下文标志都不构成授权。
type SynthesisManuscriptHumanAuthority interface {
	AuthorizeSynthesisManuscriptHumanScoped(context.Context, foundation.TransactionScope, SynthesisManuscriptHumanBinding) error
	VerifyPendingSynthesisManuscriptHumanScoped(context.Context, foundation.TransactionScope, SynthesisManuscriptHumanBinding, SynthesisManuscriptPrepared) (SynthesisManuscriptAuthority, error)
}

type DecideSynthesisManuscript struct {
	Binding        SynthesisManuscriptHumanBinding `json:"binding"`
	NoteID         foundation.ID                   `json:"note_id"`
	AttemptID      foundation.ID                   `json:"attempt_id"`
	CaptureID      foundation.ID                   `json:"capture_id"`
	IdempotencyKey string                          `json:"idempotency_key"`
	Resolution     SynthesisManuscriptResolution   `json:"resolution"`
}

type SynthesisManuscriptStageDecision struct {
	ID           foundation.ID                      `json:"id"`
	Command      DecideSynthesisManuscript          `json:"command"`
	Sequence     int                                `json:"sequence"`
	PreviousHash string                             `json:"previous_hash"`
	Result       SynthesisManuscriptResolvedPreview `json:"result"`
	CreatedAt    time.Time                          `json:"created_at"`
	Hash         string                             `json:"hash"`
}

// 嵌入的不可变账本允许在不依赖可变任务状态的情况下核验回执。
// SQL 还会把每个嵌入决策绑定到其所属模块的账本行。
type SynthesisManuscriptReceiptReview struct {
	Version   string                             `json:"version"`
	Decisions []SynthesisManuscriptStageDecision `json:"decisions"`
}

type SynthesisManuscriptReviewTarget struct {
	Attempt SynthesisManuscriptAttempt      `json:"attempt"`
	Preview SynthesisManuscriptMergePreview `json:"preview"`
	Receipt *SynthesisManuscriptReceipt     `json:"receipt,omitempty"`
}

// SynthesisManuscriptReviewManifest 是所属模块内部的有界集合，不是 HTTP 响应。
// 公开传输只能投影其中的身份和状态，仅对已选择且已授权的笔记返回全文。
// Ready 要求覆盖整个冻结生成结果中已有的变更笔记集合，至少包含一个原始冲突，
// 并为每个目标提供精确回执。
type SynthesisManuscriptReviewManifest struct {
	Binding SynthesisManuscriptHumanBinding   `json:"binding"`
	Targets []SynthesisManuscriptReviewTarget `json:"targets"`
	Ready   bool                              `json:"ready"`
}

type SynthesisManuscriptReviewStore interface {
	Decide(context.Context, DecideSynthesisManuscript) (SynthesisManuscriptStageDecision, error)
	ReadManifest(context.Context, SynthesisManuscriptHumanBinding) (SynthesisManuscriptReviewManifest, error)
}
