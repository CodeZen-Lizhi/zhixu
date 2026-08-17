package application

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// WorkspaceAnalysisPolicyVersionV1 是首版 Workspace Analysis 预算与 deadline 策略版本。
	WorkspaceAnalysisPolicyVersionV1 = domain.WorkspaceAnalysisPolicyVersionV1
	// WorkspaceAnalysisV1MaxNodes 是静态持久 DAG 的固定节点数。
	WorkspaceAnalysisV1MaxNodes = domain.WorkspaceAnalysisV1MaxNodes
	// WorkspaceAnalysisV1MaxModelCalls 是一个 Run 最多允许的模型调用数。
	WorkspaceAnalysisV1MaxModelCalls = domain.WorkspaceAnalysisV1MaxModelCalls
	// WorkspaceAnalysisV1MaxSourceReads 是一个 Run 最多允许的 Source 读取数。
	WorkspaceAnalysisV1MaxSourceReads = domain.WorkspaceAnalysisV1MaxSourceReads
	// WorkspaceAnalysisV1MaxToolCalls 是 Git、Search、Source 与 Citation 校验的总调用上限。
	WorkspaceAnalysisV1MaxToolCalls = domain.WorkspaceAnalysisV1MaxToolCalls
	// WorkspaceAnalysisV1MaxToolConcurrency 固定 Tool 串行执行。
	WorkspaceAnalysisV1MaxToolConcurrency = domain.WorkspaceAnalysisV1MaxToolConcurrency

	// WorkspaceAnalysisV1MaxInputTokensPerModelCall 是单次模型调用的输入预占上限。
	WorkspaceAnalysisV1MaxInputTokensPerModelCall int64 = domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall
	// WorkspaceAnalysisV1MaxRunInputTokens 是三个模型调用的 Run 级输入上限。
	WorkspaceAnalysisV1MaxRunInputTokens int64 = domain.WorkspaceAnalysisV1MaxRunInputTokens
	// WorkspaceAnalysisV1PlanMaxOutputTokens 是 Query Plan 的输出上限。
	WorkspaceAnalysisV1PlanMaxOutputTokens int64 = domain.WorkspaceAnalysisV1PlanMaxOutputTokens
	// WorkspaceAnalysisV1SynthesisMaxOutputTokens 是 Synthesis 在模型 Profile 之外的策略硬上限。
	WorkspaceAnalysisV1SynthesisMaxOutputTokens int64 = domain.WorkspaceAnalysisV1SynthesisMaxOutputTokens
	// WorkspaceAnalysisV1ReviewMaxOutputTokens 是 Faithfulness Review 的输出上限。
	WorkspaceAnalysisV1ReviewMaxOutputTokens int64 = domain.WorkspaceAnalysisV1ReviewMaxOutputTokens
	// WorkspaceAnalysisV1MaxRunOutputTokens 是三个模型阶段的 Run 级输出上限。
	WorkspaceAnalysisV1MaxRunOutputTokens int64 = domain.WorkspaceAnalysisV1MaxRunOutputTokens

	// WorkspaceAnalysisV1MaxRunDuration 是策略允许的 Run 全局最长时间。
	WorkspaceAnalysisV1MaxRunDuration = domain.WorkspaceAnalysisV1MaxRunDuration
	// WorkspaceAnalysisV1DurableCompletionMargin 为调用后的持久完成保留五秒。
	WorkspaceAnalysisV1DurableCompletionMargin = domain.WorkspaceAnalysisV1DurableCompletionMargin
	// WorkspaceAnalysisV1RiverJobHeadroom 是每个 Node deadline 之外的 River job 固定余量。
	WorkspaceAnalysisV1RiverJobHeadroom = domain.WorkspaceAnalysisV1RiverJobHeadroom
)

const (
	// ErrorCodeWorkspaceAnalysisPolicyInvalid 表示 v1 预算或 deadline 输入不符合冻结合同。
	ErrorCodeWorkspaceAnalysisPolicyInvalid = domain.ErrorCodeWorkspaceAnalysisPolicyInvalid
	// ErrorCodeWorkspaceAnalysisRuntimeNotReady 表示全局 River 或租约配置不能安全运行 v1。
	ErrorCodeWorkspaceAnalysisRuntimeNotReady = "AGENT_WORKSPACE_ANALYSIS_RUNTIME_NOT_READY"
)

// WorkspaceAnalysisBudgetPolicy 冻结一个 Run 的调用、并发和 Token 上限。
type WorkspaceAnalysisBudgetPolicy struct {
	PolicyVersion              int
	MaxNodes                   int
	MaxModelCalls              int
	MaxToolCalls               int
	MaxSourceReads             int
	MaxToolConcurrency         int
	MaxInputTokensPerModelCall int64
	MaxRunInputTokens          int64
	PlanMaxOutputTokens        int64
	SynthesisMaxOutputTokens   int64
	ReviewMaxOutputTokens      int64
	MaxRunOutputTokens         int64
	MaxRunDuration             time.Duration
}

// WorkspaceAnalysisV1Timeouts 复用 domain 作为冻结 timeout 快照的唯一 owner。
type WorkspaceAnalysisV1Timeouts = domain.WorkspaceAnalysisV1Timeouts

// WorkspaceAnalysisRuntimeLimits 复用 domain 的运行时边界输入。
type WorkspaceAnalysisRuntimeLimits = domain.WorkspaceAnalysisRuntimeLimits

// WorkspaceAnalysisV1Deadlines 是 domain deadline 合同的 application 兼容包装。
type WorkspaceAnalysisV1Deadlines struct {
	domain.WorkspaceAnalysisV1Deadlines
}

// WorkspaceAnalysisBudgetV1 返回按 Synthesis Profile 收窄后的 v1 预算。
func WorkspaceAnalysisBudgetV1(synthesisProfileMaxOutputTokens int) (WorkspaceAnalysisBudgetPolicy, error) {
	if synthesisProfileMaxOutputTokens <= 0 || synthesisProfileMaxOutputTokens > MaxOutputTokens {
		return WorkspaceAnalysisBudgetPolicy{}, workspaceAnalysisPolicyError(errors.New("workspace analysis synthesis profile output limit is invalid"))
	}
	synthesisLimit := int64(synthesisProfileMaxOutputTokens)
	if synthesisLimit > WorkspaceAnalysisV1SynthesisMaxOutputTokens {
		synthesisLimit = WorkspaceAnalysisV1SynthesisMaxOutputTokens
	}
	return WorkspaceAnalysisBudgetPolicy{
		PolicyVersion:              WorkspaceAnalysisPolicyVersionV1,
		MaxNodes:                   WorkspaceAnalysisV1MaxNodes,
		MaxModelCalls:              WorkspaceAnalysisV1MaxModelCalls,
		MaxToolCalls:               WorkspaceAnalysisV1MaxToolCalls,
		MaxSourceReads:             WorkspaceAnalysisV1MaxSourceReads,
		MaxToolConcurrency:         WorkspaceAnalysisV1MaxToolConcurrency,
		MaxInputTokensPerModelCall: WorkspaceAnalysisV1MaxInputTokensPerModelCall,
		MaxRunInputTokens:          WorkspaceAnalysisV1MaxRunInputTokens,
		PlanMaxOutputTokens:        WorkspaceAnalysisV1PlanMaxOutputTokens,
		SynthesisMaxOutputTokens:   synthesisLimit,
		ReviewMaxOutputTokens:      WorkspaceAnalysisV1ReviewMaxOutputTokens,
		MaxRunOutputTokens:         WorkspaceAnalysisV1PlanMaxOutputTokens + synthesisLimit + WorkspaceAnalysisV1ReviewMaxOutputTokens,
		MaxRunDuration:             WorkspaceAnalysisV1MaxRunDuration,
	}, nil
}

// DeriveWorkspaceAnalysisV1Deadlines 按冻结公式推导六个 Node、Run 与 River job 边界。
func DeriveWorkspaceAnalysisV1Deadlines(timeouts WorkspaceAnalysisV1Timeouts) (WorkspaceAnalysisV1Deadlines, error) {
	deadlines, err := domain.DeriveWorkspaceAnalysisV1Deadlines(timeouts)
	return WorkspaceAnalysisV1Deadlines{WorkspaceAnalysisV1Deadlines: deadlines}, err
}

// InspectWorkspaceDeadline 返回 Git 聚合节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) InspectWorkspaceDeadline() time.Duration {
	return deadlines.WorkspaceAnalysisV1Deadlines.InspectWorkspaceDeadline()
}

// RetrieveEvidenceDeadline 返回查询规划与检索节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) RetrieveEvidenceDeadline() time.Duration {
	return deadlines.WorkspaceAnalysisV1Deadlines.RetrieveEvidenceDeadline()
}

// ReadEvidenceDeadline 返回顺序 Source 阅读节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) ReadEvidenceDeadline() time.Duration {
	return deadlines.WorkspaceAnalysisV1Deadlines.ReadEvidenceDeadline()
}

// SynthesizeAnswerDeadline 返回候选答案生成节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) SynthesizeAnswerDeadline() time.Duration {
	return deadlines.WorkspaceAnalysisV1Deadlines.SynthesizeAnswerDeadline()
}

// ValidateCitationsDeadline 返回 Citation 校验节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) ValidateCitationsDeadline() time.Duration {
	return deadlines.WorkspaceAnalysisV1Deadlines.ValidateCitationsDeadline()
}

// ReviewPublishDeadline 返回 Faithfulness Review 与发布节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) ReviewPublishDeadline() time.Duration {
	return deadlines.WorkspaceAnalysisV1Deadlines.ReviewPublishDeadline()
}

// RunDeadline 返回六个 Node deadline 之和；v1 构造时保证不超过一小时。
func (deadlines WorkspaceAnalysisV1Deadlines) RunDeadline() time.Duration {
	return deadlines.WorkspaceAnalysisV1Deadlines.RunDeadline()
}

// MinimumRiverJobTimeout 返回全局 River job timeout 必须覆盖的最小值。
func (deadlines WorkspaceAnalysisV1Deadlines) MinimumRiverJobTimeout() time.Duration {
	return deadlines.WorkspaceAnalysisV1Deadlines.MinimumRiverJobTimeout()
}

// ValidateRuntimeReadiness 校验 River job 与可续租 Workflow lease 的全局配置。
func (deadlines WorkspaceAnalysisV1Deadlines) ValidateRuntimeReadiness(limits WorkspaceAnalysisRuntimeLimits) error {
	if deadlines.RunDeadline() <= 0 {
		return workspaceAnalysisPolicyError(errors.New("workspace analysis deadlines are not derived"))
	}
	if err := deadlines.WorkspaceAnalysisV1Deadlines.ValidateRuntimeReadiness(limits); err != nil {
		return workspaceAnalysisRuntimeNotReadyError(err)
	}
	return nil
}

func workspaceAnalysisPolicyError(cause error) error {
	return applicationError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisPolicyInvalid, false, cause)
}

func workspaceAnalysisRuntimeNotReadyError(cause error) error {
	return applicationError(foundation.ErrorDependencyUnavailable, ErrorCodeWorkspaceAnalysisRuntimeNotReady, false, cause)
}
