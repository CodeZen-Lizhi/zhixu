package domain

import "time"

const (
	// WorkspaceAnalysisPolicyVersionV1 是首版工作区分析预算策略版本。
	WorkspaceAnalysisPolicyVersionV1 = 1
	// WorkspaceAnalysisV1MaxNodes 是静态持久 DAG 的固定节点数。
	WorkspaceAnalysisV1MaxNodes = 6
	// WorkspaceAnalysisV1MaxModelCalls 是一个 Run 最多允许的模型调用数。
	WorkspaceAnalysisV1MaxModelCalls = 3
	// WorkspaceAnalysisV1MaxSourceReads 是一个 Run 最多允许的 Source 读取数。
	WorkspaceAnalysisV1MaxSourceReads = 3
	// WorkspaceAnalysisV1MinCompletedToolCalls 是成功链路包含一次 Source 读取时的 Tool 调用下限。
	WorkspaceAnalysisV1MinCompletedToolCalls = 4
	// WorkspaceAnalysisV1MaxToolCalls 是 Git、Search、Source 与 Citation 校验的总调用上限。
	WorkspaceAnalysisV1MaxToolCalls = 3 + WorkspaceAnalysisV1MaxSourceReads
	// WorkspaceAnalysisV1MaxToolConcurrency 固定 Tool 串行执行。
	WorkspaceAnalysisV1MaxToolConcurrency = 1

	// WorkspaceAnalysisV1MaxInputTokensPerModelCall 是单次模型调用的输入预占上限。
	WorkspaceAnalysisV1MaxInputTokensPerModelCall int64 = 64 * 1024
	// WorkspaceAnalysisV1MaxRunInputTokens 是三个模型调用的 Run 级输入上限。
	WorkspaceAnalysisV1MaxRunInputTokens int64 = WorkspaceAnalysisV1MaxModelCalls * WorkspaceAnalysisV1MaxInputTokensPerModelCall
	// WorkspaceAnalysisV1PlanMaxOutputTokens 是 Query Plan 的输出上限。
	WorkspaceAnalysisV1PlanMaxOutputTokens int64 = 256
	// WorkspaceAnalysisV1SynthesisMaxOutputTokens 是 Synthesis 的策略硬上限。
	WorkspaceAnalysisV1SynthesisMaxOutputTokens int64 = 4_096
	// WorkspaceAnalysisV1ReviewMaxOutputTokens 是 Faithfulness Review 的输出上限。
	WorkspaceAnalysisV1ReviewMaxOutputTokens int64 = 1_024
	// WorkspaceAnalysisV1MaxRunOutputTokens 是三个模型阶段的 Run 级输出硬上限。
	WorkspaceAnalysisV1MaxRunOutputTokens int64 = WorkspaceAnalysisV1PlanMaxOutputTokens + WorkspaceAnalysisV1SynthesisMaxOutputTokens + WorkspaceAnalysisV1ReviewMaxOutputTokens
	// WorkspaceAnalysisV1MaxRunDuration 是策略允许的 Run 全局最长时间。
	WorkspaceAnalysisV1MaxRunDuration = time.Hour
	// WorkspaceAnalysisV1DurableCompletionMargin 为调用后的持久完成保留五秒。
	WorkspaceAnalysisV1DurableCompletionMargin = 5 * time.Second
	// WorkspaceAnalysisV1RiverJobHeadroom 是每个 Node deadline 之外的 River job 固定余量。
	WorkspaceAnalysisV1RiverJobHeadroom = 30 * time.Second
)

const (
	// ErrorCodeWorkspaceAnalysisPolicyInvalid 表示 v1 预算或 deadline 输入不符合冻结合同。
	ErrorCodeWorkspaceAnalysisPolicyInvalid = "AGENT_WORKSPACE_ANALYSIS_POLICY_INVALID"
)

const (
	workspaceAnalysisInspectDeadlineIndex = iota
	workspaceAnalysisRetrieveDeadlineIndex
	workspaceAnalysisReadDeadlineIndex
	workspaceAnalysisSynthesisDeadlineIndex
	workspaceAnalysisValidationDeadlineIndex
	workspaceAnalysisReviewDeadlineIndex
)

// WorkspaceAnalysisV1Timeouts 是一个 Run 冻结的模型和精确 Tool timeout 快照。
type WorkspaceAnalysisV1Timeouts struct {
	PlanModelTimeout            time.Duration
	SynthesisModelTimeout       time.Duration
	ReviewModelTimeout          time.Duration
	GitToolTimeout              time.Duration
	SearchToolTimeout           time.Duration
	SourceReadToolTimeout       time.Duration
	ValidateCitationToolTimeout time.Duration
}

// WorkspaceAnalysisV1Deadlines 是由冻结 timeout 快照唯一派生的六节点和 Run deadline。
type WorkspaceAnalysisV1Deadlines struct {
	nodes                  [WorkspaceAnalysisV1MaxNodes]time.Duration
	run                    time.Duration
	minimumRiverJobTimeout time.Duration
}

// WorkspaceAnalysisRuntimeLimits 是 deadline 对运行时配置的最小要求。
type WorkspaceAnalysisRuntimeLimits struct {
	RiverJobTimeout   time.Duration
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
}

// DeriveWorkspaceAnalysisV1Deadlines 按冻结 timeout 快照推导 v1 的全部 deadline。
func DeriveWorkspaceAnalysisV1Deadlines(timeouts WorkspaceAnalysisV1Timeouts) (WorkspaceAnalysisV1Deadlines, error) {
	if !validWorkspaceAnalysisV1Timeouts(timeouts) {
		return WorkspaceAnalysisV1Deadlines{}, invalid(ErrorCodeWorkspaceAnalysisPolicyInvalid, "workspace analysis call timeout is invalid")
	}
	deadlines := WorkspaceAnalysisV1Deadlines{nodes: [WorkspaceAnalysisV1MaxNodes]time.Duration{
		timeouts.GitToolTimeout + WorkspaceAnalysisV1DurableCompletionMargin,
		timeouts.PlanModelTimeout + timeouts.SearchToolTimeout + 15*time.Second,
		WorkspaceAnalysisV1MaxSourceReads*timeouts.SourceReadToolTimeout + 10*time.Second,
		timeouts.SynthesisModelTimeout + 15*time.Second,
		timeouts.ValidateCitationToolTimeout + 10*time.Second,
		timeouts.ReviewModelTimeout + 15*time.Second,
	}}
	var maximumNode time.Duration
	for _, deadline := range deadlines.nodes {
		deadlines.run += deadline
		if deadline > maximumNode {
			maximumNode = deadline
		}
	}
	if deadlines.run > WorkspaceAnalysisV1MaxRunDuration {
		return WorkspaceAnalysisV1Deadlines{}, invalid(ErrorCodeWorkspaceAnalysisPolicyInvalid, "workspace analysis node deadlines exceed the one hour run limit")
	}
	deadlines.minimumRiverJobTimeout = maximumNode + WorkspaceAnalysisV1RiverJobHeadroom
	return deadlines, nil
}

// InspectWorkspaceDeadline 返回 Git 聚合节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) InspectWorkspaceDeadline() time.Duration {
	return deadlines.nodes[workspaceAnalysisInspectDeadlineIndex]
}

// RetrieveEvidenceDeadline 返回查询规划与检索节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) RetrieveEvidenceDeadline() time.Duration {
	return deadlines.nodes[workspaceAnalysisRetrieveDeadlineIndex]
}

// ReadEvidenceDeadline 返回顺序 Source 阅读节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) ReadEvidenceDeadline() time.Duration {
	return deadlines.nodes[workspaceAnalysisReadDeadlineIndex]
}

// SynthesizeAnswerDeadline 返回候选答案生成节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) SynthesizeAnswerDeadline() time.Duration {
	return deadlines.nodes[workspaceAnalysisSynthesisDeadlineIndex]
}

// ValidateCitationsDeadline 返回 Citation 校验节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) ValidateCitationsDeadline() time.Duration {
	return deadlines.nodes[workspaceAnalysisValidationDeadlineIndex]
}

// ReviewPublishDeadline 返回 Faithfulness Review 与发布节点的完整执行 deadline。
func (deadlines WorkspaceAnalysisV1Deadlines) ReviewPublishDeadline() time.Duration {
	return deadlines.nodes[workspaceAnalysisReviewDeadlineIndex]
}

// RunDeadline 返回六个 Node deadline 的精确总和。
func (deadlines WorkspaceAnalysisV1Deadlines) RunDeadline() time.Duration {
	return deadlines.run
}

// MinimumRiverJobTimeout 返回全局 River job 必须覆盖的最小 timeout。
func (deadlines WorkspaceAnalysisV1Deadlines) MinimumRiverJobTimeout() time.Duration {
	return deadlines.minimumRiverJobTimeout
}

// ValidateRuntimeReadiness 校验派生 deadline 与可续租运行时的安全余量。
func (deadlines WorkspaceAnalysisV1Deadlines) ValidateRuntimeReadiness(limits WorkspaceAnalysisRuntimeLimits) error {
	if !deadlines.valid() {
		return invalid(ErrorCodeWorkspaceAnalysisPolicyInvalid, "workspace analysis deadlines are not derived")
	}
	if limits.RiverJobTimeout <= 0 || limits.LeaseDuration <= 0 || limits.HeartbeatInterval <= 0 ||
		limits.RiverJobTimeout < deadlines.minimumRiverJobTimeout || limits.LeaseDuration <= time.Nanosecond ||
		limits.HeartbeatInterval > (limits.LeaseDuration-time.Nanosecond)/3 ||
		limits.LeaseDuration < WorkspaceAnalysisV1DurableCompletionMargin ||
		limits.HeartbeatInterval > (limits.LeaseDuration-WorkspaceAnalysisV1DurableCompletionMargin)/3 {
		return invalid(ErrorCodeWorkspaceAnalysisPolicyInvalid, "workspace analysis runtime limits are not ready")
	}
	return nil
}

func validWorkspaceAnalysisV1Timeouts(timeouts WorkspaceAnalysisV1Timeouts) bool {
	values := [...]time.Duration{
		timeouts.PlanModelTimeout, timeouts.SynthesisModelTimeout, timeouts.ReviewModelTimeout,
		timeouts.GitToolTimeout, timeouts.SearchToolTimeout, timeouts.SourceReadToolTimeout,
		timeouts.ValidateCitationToolTimeout,
	}
	for _, value := range values {
		if value <= 0 || value > WorkspaceAnalysisV1MaxRunDuration {
			return false
		}
	}
	return true
}

func (deadlines WorkspaceAnalysisV1Deadlines) valid() bool {
	var total, maximum time.Duration
	for _, deadline := range deadlines.nodes {
		if deadline <= 0 {
			return false
		}
		total += deadline
		if deadline > maximum {
			maximum = deadline
		}
	}
	return total == deadlines.run && total <= WorkspaceAnalysisV1MaxRunDuration &&
		maximum+WorkspaceAnalysisV1RiverJobHeadroom == deadlines.minimumRiverJobTimeout
}
