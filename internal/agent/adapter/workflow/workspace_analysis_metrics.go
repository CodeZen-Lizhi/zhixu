package workflow

import (
	"context"
	"errors"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/observability"
)

// WorkspaceAnalysisFinalizerWithMetrics 只在首次持久终态提交后记录低基数结果指标。
type WorkspaceAnalysisFinalizerWithMetrics struct {
	next    conversationapplication.WorkspaceAnalysisFinalizer
	metrics observability.Metrics
}

// NewWorkspaceAnalysisFinalizerWithMetrics 为终态发布增加不影响业务结果的观测。
func NewWorkspaceAnalysisFinalizerWithMetrics(
	next conversationapplication.WorkspaceAnalysisFinalizer,
	metrics observability.Metrics,
) (*WorkspaceAnalysisFinalizerWithMetrics, error) {
	if nilDependency(next) {
		return nil, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis finalizer metrics dependency is unavailable"),
		)
	}
	if nilDependency(metrics) {
		metrics = observability.NewNoopMetrics()
	}
	return &WorkspaceAnalysisFinalizerWithMetrics{next: next, metrics: metrics}, nil
}

// LookupPublication 保持权威重放查询语义，不产生终态指标。
func (finalizer *WorkspaceAnalysisFinalizerWithMetrics) LookupPublication(
	ctx context.Context,
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	return finalizer.next.LookupPublication(ctx, lookup)
}

// FinalizeSuccess 仅在 fresh commit 后记录 completed。
func (finalizer *WorkspaceAnalysisFinalizerWithMetrics) FinalizeSuccess(
	ctx context.Context,
	command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	output, replayed, err := finalizer.next.FinalizeSuccess(ctx, command)
	if err == nil && !replayed {
		finalizer.record(ctx, string(agentdomain.WorkspaceAnalysisRunSucceeded), string(agentdomain.WorkspaceAnalysisRunCompleted))
	}
	return output, replayed, err
}

// FinalizeTermination 仅在 fresh commit 后按冻结原因记录终态。
func (finalizer *WorkspaceAnalysisFinalizerWithMetrics) FinalizeTermination(
	ctx context.Context,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	output, replayed, err := finalizer.next.FinalizeTermination(ctx, command)
	if err == nil && !replayed {
		finalizer.record(ctx, string(output.PublicationStatus), string(command.Reason))
	}
	return output, replayed, err
}

func (finalizer *WorkspaceAnalysisFinalizerWithMetrics) record(
	ctx context.Context,
	status string,
	reason string,
) {
	defer func() { _ = recover() }()
	measurement, err := observability.NewWorkspaceAnalysisOutcomeMeasurement(status, reason)
	if err != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	_ = finalizer.metrics.Record(ctx, measurement)
}

var _ conversationapplication.WorkspaceAnalysisFinalizer = (*WorkspaceAnalysisFinalizerWithMetrics)(nil)
