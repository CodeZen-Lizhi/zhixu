package workflow

import (
	"context"
	"errors"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/observability"
)

func TestWorkspaceAnalysisFinalizerMetricsRecordsFreshTerminalFactsOnly(t *testing.T) {
	metrics := observability.NewMemoryMetrics()
	next := &workspaceAnalysisFinalizerMetricsFake{}
	finalizer, err := NewWorkspaceAnalysisFinalizerWithMetrics(next, metrics)
	if err != nil {
		t.Fatalf("NewWorkspaceAnalysisFinalizerWithMetrics: %v", err)
	}

	if _, _, err := finalizer.FinalizeSuccess(context.Background(), conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand{}); err != nil {
		t.Fatalf("FinalizeSuccess: %v", err)
	}
	for _, reason := range []agentdomain.WorkspaceAnalysisRunTerminationReason{
		agentdomain.WorkspaceAnalysisRunEvidenceInsufficient,
		agentdomain.WorkspaceAnalysisRunNeedsClarification,
		agentdomain.WorkspaceAnalysisRunToolFailed,
		agentdomain.WorkspaceAnalysisRunCancellation,
	} {
		if _, _, err := finalizer.FinalizeTermination(
			context.Background(),
			conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{Reason: reason},
		); err != nil {
			t.Fatalf("FinalizeTermination(%s): %v", reason, err)
		}
	}

	measurements := metrics.Snapshot()
	if len(measurements) != 5 {
		t.Fatalf("measurement count=%d", len(measurements))
	}
	wantOutcomes := []string{"completed", "refused", "clarification_required", "failure", "cancelled"}
	for index, measurement := range measurements {
		if measurement.Name != observability.MetricWorkspaceAnalysisOutcomeTotal ||
			measurement.Labels.Map()["outcome"] != wantOutcomes[index] {
			t.Fatalf("measurement[%d]=%#v", index, measurement)
		}
	}

	next.replayed = true
	_, _, _ = finalizer.FinalizeSuccess(context.Background(), conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand{})
	_, _, _ = finalizer.FinalizeTermination(
		context.Background(),
		conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{Reason: agentdomain.WorkspaceAnalysisRunToolFailed},
	)
	next.replayed = false
	next.err = errors.New("commit failed")
	_, _, _ = finalizer.FinalizeSuccess(context.Background(), conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand{})
	if got := len(metrics.Snapshot()); got != len(measurements) {
		t.Fatalf("replay or error recorded metrics: got=%d", got)
	}
}

func TestWorkspaceAnalysisFinalizerMetricsCannotBreakPublication(t *testing.T) {
	finalizer, err := NewWorkspaceAnalysisFinalizerWithMetrics(
		&workspaceAnalysisFinalizerMetricsFake{},
		workspaceAnalysisPanickingMetrics{},
	)
	if err != nil {
		t.Fatalf("NewWorkspaceAnalysisFinalizerWithMetrics: %v", err)
	}
	if _, replayed, err := finalizer.FinalizeSuccess(
		context.Background(), conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand{},
	); err != nil || replayed {
		t.Fatalf("FinalizeSuccess replayed=%t err=%v", replayed, err)
	}
}

func TestWorkspaceAnalysisFinalizerMetricsKeepV2DefinitionOnFreshCommit(t *testing.T) {
	metrics := observability.NewMemoryMetrics()
	next := &workspaceAnalysisFinalizerMetricsFake{}
	finalizer, err := NewWorkspaceAnalysisFinalizerWithMetrics(next, metrics)
	if err != nil {
		t.Fatal(err)
	}
	lookup := conversationapplication.WorkspaceAnalysisPublicationLookup{DefinitionVersion: 2}
	success := conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand{WorkspaceAnalysisPublicationLookup: lookup}
	if _, _, err := finalizer.FinalizeSuccess(context.Background(), success); err != nil {
		t.Fatal(err)
	}
	termination := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{WorkspaceAnalysisPublicationLookup: lookup, Reason: agentdomain.WorkspaceAnalysisRunToolFailed}
	if _, _, err := finalizer.FinalizeTermination(context.Background(), termination); err != nil {
		t.Fatal(err)
	}
	next.replayed = true
	_, _, _ = finalizer.FinalizeSuccess(context.Background(), success)
	_, _, _ = finalizer.FinalizeTermination(context.Background(), termination)
	measurements := metrics.Snapshot()
	if len(measurements) != 2 {
		t.Fatalf("fresh v2 metric count=%d", len(measurements))
	}
	for _, measurement := range measurements {
		if measurement.Labels.Map()["definition"] != "workspace-analysis-v2" {
			t.Fatal("v2 publication was recorded as a different definition")
		}
	}
}

type workspaceAnalysisFinalizerMetricsFake struct {
	replayed bool
	err      error
}

func (*workspaceAnalysisFinalizerMetricsFake) LookupPublication(
	context.Context,
	conversationapplication.WorkspaceAnalysisPublicationLookup,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, nil
}

func (fake *workspaceAnalysisFinalizerMetricsFake) FinalizeSuccess(
	context.Context,
	conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, fake.replayed, fake.err
}

func (fake *workspaceAnalysisFinalizerMetricsFake) FinalizeTermination(
	_ context.Context,
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	status := conversationworkflow.PublicationStatus("failed")
	switch command.Reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient:
		status = "refused"
	case agentdomain.WorkspaceAnalysisRunNeedsClarification:
		status = "clarification_required"
	case agentdomain.WorkspaceAnalysisRunCancellation:
		status = "cancelled"
	}
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{PublicationStatus: status}, fake.replayed, fake.err
}

type workspaceAnalysisPanickingMetrics struct{}

func (workspaceAnalysisPanickingMetrics) Record(context.Context, observability.Measurement) error {
	panic("metrics failure")
}

var _ conversationapplication.WorkspaceAnalysisFinalizer = (*workspaceAnalysisFinalizerMetricsFake)(nil)
var _ observability.Metrics = workspaceAnalysisPanickingMetrics{}
