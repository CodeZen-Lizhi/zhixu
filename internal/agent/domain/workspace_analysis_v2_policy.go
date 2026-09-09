package domain

import "time"

// v2 freezes a bounded, model-directed evidence loop. The last two model calls
// and citation tool are reserved for independent publication gates.
const (
	WorkspaceAnalysisPolicyVersionV2                    = 2
	WorkspaceAnalysisV2MaxNodes                         = 4
	WorkspaceAnalysisV2MaxDecisions                     = 12
	WorkspaceAnalysisV2MaxModelCalls                    = WorkspaceAnalysisV2MaxDecisions + 2
	WorkspaceAnalysisV2MaxToolCalls                     = WorkspaceAnalysisV2MaxDecisions + 1
	WorkspaceAnalysisV2MaxSourceReads                   = 8
	WorkspaceAnalysisV2MaxEvidenceRefs                  = 32
	WorkspaceAnalysisV2MaxToolConcurrency               = 1
	WorkspaceAnalysisV2MinCompletedModelCalls           = 5
	WorkspaceAnalysisV2MinCompletedToolCalls            = 3
	WorkspaceAnalysisV2MaxInputTokensPerModelCall int64 = 64 * 1024
	WorkspaceAnalysisV2MaxRunInputTokens          int64 = WorkspaceAnalysisV2MaxModelCalls * WorkspaceAnalysisV2MaxInputTokensPerModelCall
	WorkspaceAnalysisV2DecisionMaxOutputTokens    int64 = 512
	WorkspaceAnalysisV2SynthesisMaxOutputTokens   int64 = 4096
	WorkspaceAnalysisV2ReviewMaxOutputTokens      int64 = 1024
	WorkspaceAnalysisV2MaxRunOutputTokens         int64 = WorkspaceAnalysisV2MaxDecisions*WorkspaceAnalysisV2DecisionMaxOutputTokens + WorkspaceAnalysisV2SynthesisMaxOutputTokens + WorkspaceAnalysisV2ReviewMaxOutputTokens
	WorkspaceAnalysisV2MaxRunDuration                   = time.Hour
	WorkspaceAnalysisV2DurableCompletionMargin          = 5 * time.Second
	WorkspaceAnalysisV2RiverJobHeadroom                 = 30 * time.Second
)

// WorkspaceAnalysisV2Timeouts retains the persisted timeout fields. PlanModelTimeout
// is the per-decision timeout in version 2; it is not an extra planning call.
type WorkspaceAnalysisV2Timeouts = WorkspaceAnalysisV1Timeouts

// WorkspaceAnalysisV2Deadlines is the deterministic deadline snapshot for the four-node DAG.
type WorkspaceAnalysisV2Deadlines struct {
	nodes                  [WorkspaceAnalysisV2MaxNodes]time.Duration
	run                    time.Duration
	minimumRiverJobTimeout time.Duration
}

// DeriveWorkspaceAnalysisV2Deadlines reserves time for every permitted external
// call and its durable completion. A workflow retry never grants a new budget.
func DeriveWorkspaceAnalysisV2Deadlines(timeouts WorkspaceAnalysisV2Timeouts) (WorkspaceAnalysisV2Deadlines, error) {
	if !validWorkspaceAnalysisV1Timeouts(timeouts) {
		return WorkspaceAnalysisV2Deadlines{}, invalid(ErrorCodeWorkspaceAnalysisPolicyInvalid, "workspace analysis v2 call timeout is invalid")
	}
	toolTimeout := max(timeouts.GitToolTimeout, timeouts.SearchToolTimeout, timeouts.SourceReadToolTimeout, timeouts.ValidateCitationToolTimeout)
	deadlines := WorkspaceAnalysisV2Deadlines{nodes: [WorkspaceAnalysisV2MaxNodes]time.Duration{
		WorkspaceAnalysisV2MaxDecisions*(timeouts.PlanModelTimeout+toolTimeout+WorkspaceAnalysisV2DurableCompletionMargin) + 15*time.Second,
		timeouts.SynthesisModelTimeout + 15*time.Second,
		timeouts.ValidateCitationToolTimeout + 10*time.Second,
		timeouts.ReviewModelTimeout + 15*time.Second,
	}}
	for _, value := range deadlines.nodes {
		deadlines.run += value
		deadlines.minimumRiverJobTimeout = max(deadlines.minimumRiverJobTimeout, value)
	}
	if deadlines.run > WorkspaceAnalysisV2MaxRunDuration {
		return WorkspaceAnalysisV2Deadlines{}, invalid(ErrorCodeWorkspaceAnalysisPolicyInvalid, "workspace analysis v2 deadlines exceed the run limit")
	}
	deadlines.minimumRiverJobTimeout += WorkspaceAnalysisV2RiverJobHeadroom
	return deadlines, nil
}

func (d WorkspaceAnalysisV2Deadlines) DecideNextDeadline() time.Duration        { return d.nodes[0] }
func (d WorkspaceAnalysisV2Deadlines) SynthesizeAnswerDeadline() time.Duration  { return d.nodes[1] }
func (d WorkspaceAnalysisV2Deadlines) ValidateCitationsDeadline() time.Duration { return d.nodes[2] }
func (d WorkspaceAnalysisV2Deadlines) ReviewPublishDeadline() time.Duration     { return d.nodes[3] }
func (d WorkspaceAnalysisV2Deadlines) RunDeadline() time.Duration               { return d.run }
func (d WorkspaceAnalysisV2Deadlines) MinimumRiverJobTimeout() time.Duration {
	return d.minimumRiverJobTimeout
}

func (d WorkspaceAnalysisV2Deadlines) ValidateRuntimeReadiness(limits WorkspaceAnalysisRuntimeLimits) error {
	if d.run <= 0 || d.run > WorkspaceAnalysisV2MaxRunDuration || limits.RiverJobTimeout < d.minimumRiverJobTimeout ||
		limits.LeaseDuration < WorkspaceAnalysisV2DurableCompletionMargin || limits.HeartbeatInterval <= 0 ||
		limits.HeartbeatInterval > (limits.LeaseDuration-WorkspaceAnalysisV2DurableCompletionMargin)/3 {
		return invalid(ErrorCodeWorkspaceAnalysisPolicyInvalid, "workspace analysis v2 runtime limits are not ready")
	}
	return nil
}

func validWorkspaceAnalysisV2Limits(limits WorkspaceAnalysisBudgetLimits) bool {
	return limits.Nodes == WorkspaceAnalysisV2MaxNodes && limits.ToolConcurrency == WorkspaceAnalysisV2MaxToolConcurrency &&
		limits.Amount.ModelCalls == WorkspaceAnalysisV2MaxModelCalls && limits.Amount.ToolCalls == WorkspaceAnalysisV2MaxToolCalls &&
		limits.Amount.SourceReads == WorkspaceAnalysisV2MaxSourceReads && limits.Amount.InputTokens == WorkspaceAnalysisV2MaxRunInputTokens &&
		limits.Amount.OutputTokens > WorkspaceAnalysisV2MaxDecisions*WorkspaceAnalysisV2DecisionMaxOutputTokens+WorkspaceAnalysisV2ReviewMaxOutputTokens &&
		limits.Amount.OutputTokens <= WorkspaceAnalysisV2MaxRunOutputTokens && limits.Amount.CostMicrounits == nil
}

func validWorkspaceAnalysisVersion(definition int64, policy int) bool {
	return definition == 1 && policy == WorkspaceAnalysisPolicyVersionV1 || definition == 2 && policy == WorkspaceAnalysisPolicyVersionV2
}

func workspaceAnalysisRunDuration(run WorkspaceAnalysisRun) (time.Duration, error) {
	if run.PolicyVersion == WorkspaceAnalysisPolicyVersionV2 {
		deadlines, err := DeriveWorkspaceAnalysisV2Deadlines(run.Timeouts)
		return deadlines.RunDeadline(), err
	}
	deadlines, err := DeriveWorkspaceAnalysisV1Deadlines(run.Timeouts)
	return deadlines.RunDeadline(), err
}
