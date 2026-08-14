package eino

import (
	"context"
	"errors"
	"testing"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestAnswerStreamRuntimeRecordsFirstTokenCompletionAndDraftDegradation(t *testing.T) {
	backend := &scriptedRuntimeModel{stream: func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
		return schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Assistant, Content: "answer"},
			{ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}}},
		}), nil
	}}
	metrics := observability.NewMemoryMetrics()
	runtime, err := NewAnswerStreamRuntimeWithMetrics(backend, metrics)
	if err != nil {
		t.Fatal(err)
	}
	repository := &runtimeRecordingRepository{}
	ledger, recorder := newRuntimeTestLedgerAndRecorder(t, repository, 5, 1, 0)
	result, err := runtime.Stream(context.Background(), runtimeAnswerStreamRequest(ledger, recorder), &degradingRuntimeSink{})
	if err != nil || result.Content != "answer" || !result.DraftDegraded {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	counts := runtimeMetricCounts(metrics.Snapshot())
	for _, name := range []observability.MetricName{
		observability.MetricAnswerFirstTokenDuration,
		observability.MetricAnswerCompletionDuration,
		observability.MetricAnswerResultTotal,
		observability.MetricDraftDegradationTotal,
	} {
		if counts[name] != 1 {
			t.Fatalf("metric %s count=%d snapshot=%+v", name, counts[name], metrics.Snapshot())
		}
	}
}

func TestAgentRuntimeRecordsIterationsAndToolCalls(t *testing.T) {
	backend := &scriptedRuntimeModel{generate: []func([]*schema.Message, ...einomodel.Option) (*schema.Message, error){
		func([]*schema.Message, ...einomodel.Option) (*schema.Message, error) {
			return &schema.Message{
				Role: schema.Assistant, Content: "final",
				ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}},
			}, nil
		},
	}}
	metrics := observability.NewMemoryMetrics()
	runtime, err := NewAgentRuntimeWithMetrics(backend, metrics)
	if err != nil {
		t.Fatal(err)
	}
	repository := &runtimeRecordingRepository{}
	ledger, recorder := newRuntimeTestLedgerAndRecorder(t, repository, 6, 1, 0)
	result, err := runtime.Run(context.Background(), agentapplication.AgentRunRequest{
		Model: runtimeTestModelRef(), Profile: runtimeTestProfileRef(), Prompt: runtimeTestPromptRef(), Schema: runtimeTestSchemaRef(),
		Messages: []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Use evidence."}, {Role: agentapplication.AgentMessageUser, Content: "Answer."}},
		Tools:    []agentapplication.AgentToolSpec{}, MaxIterations: 1, MaxInputTokens: 10, MaxOutputTokens: 10,
		Budget: ledger, Recorder: recorder,
	})
	if err != nil || result.Iterations != 1 || result.ToolCalls != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	measurements := metrics.Snapshot()
	counts := runtimeMetricCounts(measurements)
	for _, name := range []observability.MetricName{
		observability.MetricAgentIterations,
		observability.MetricAgentToolCalls,
		observability.MetricAgentResultTotal,
	} {
		if counts[name] != 1 {
			t.Fatalf("metric %s count=%d snapshot=%+v", name, counts[name], measurements)
		}
	}
	for _, measurement := range measurements {
		if measurement.Name == observability.MetricAgentToolCalls && measurement.Value != 0 {
			t.Fatalf("tool call measurement=%+v", measurement)
		}
	}
}

func TestRAGExecutionSchedulerRecordsFixedGraphNodes(t *testing.T) {
	metrics := observability.NewMemoryMetrics()
	scheduler, err := NewRAGExecutionSchedulerWithMetrics(context.Background(), metrics)
	if err != nil {
		t.Fatal(err)
	}
	executor := newRAGSchedulerExecutor(t, approvedRAGSchedulerPorts(), scheduler)
	if _, err := executor.Execute(context.Background(), ragSchedulerRequest()); err != nil {
		t.Fatal(err)
	}
	measurements := metrics.Snapshot()
	if got := runtimeMetricCounts(measurements)[observability.MetricRAGGraphNodeResultTotal]; got != 6 {
		t.Fatalf("graph node metrics=%d snapshot=%+v", got, measurements)
	}
	for _, measurement := range measurements {
		if measurement.Name != observability.MetricRAGGraphNodeResultTotal || measurement.Labels.Map()["result"] != "success" {
			t.Fatalf("unexpected graph measurement=%+v", measurement)
		}
	}
}

func TestRAGExecutionSchedulerRecordsStableGraphNodeFailure(t *testing.T) {
	metrics := observability.NewMemoryMetrics()
	scheduler, err := NewRAGExecutionSchedulerWithMetrics(context.Background(), metrics)
	if err != nil {
		t.Fatal(err)
	}
	ports := approvedRAGSchedulerPorts()
	ports.planErr = foundation.NewError(foundation.ErrorRetryableFailure, "AGENT_RAG_PLAN_TEST_FAILED", true, errors.New("plan failed"))
	executor := newRAGSchedulerExecutor(t, ports, scheduler)
	if _, err := executor.Execute(context.Background(), ragSchedulerRequest()); err == nil {
		t.Fatal("expected graph execution to fail")
	}
	measurements := metrics.Snapshot()
	if len(measurements) != 1 || measurements[0].Name != observability.MetricRAGGraphNodeResultTotal {
		t.Fatalf("measurements=%+v", measurements)
	}
	labels := measurements[0].Labels.Map()
	if labels["node_kind"] != "eino_rag_"+ragPlanNode || labels["result"] != "failure" || labels["error_code"] != "AGENT_RAG_PLAN_TEST_FAILED" {
		t.Fatalf("labels=%+v", labels)
	}
}

func TestRuntimeMetricsFailureNeverChangesBusinessResult(t *testing.T) {
	observer := newRuntimeMetricObserver(panickingRuntimeMetrics{})
	observer.observeAgent(context.Background(), 1, 0, nil)
	observer.observeAnswerFirstToken(context.Background(), observer.start())
	observer.observeAnswerCompletion(context.Background(), observer.start(), errors.New("draft failed"), nil)
	observer.observeRAGGraphNode(context.Background(), ragPlanNode, errors.New("node failed"))
}

func runtimeAnswerStreamRequest(ledger *agentapplication.RunBudgetLedger, recorder *agentapplication.ModelCallRecorder) agentapplication.AnswerStreamRequest {
	return agentapplication.AnswerStreamRequest{
		Model: runtimeTestModelRef(), Profile: runtimeTestProfileRef(), Prompt: runtimeTestPromptRef(), Schema: runtimeTestSchemaRef(),
		Messages:       []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Return the final answer."}, {Role: agentapplication.AgentMessageUser, Content: "Use the frozen evidence."}},
		MaxInputTokens: 10, MaxOutputTokens: 10, Budget: ledger, Recorder: recorder,
	}
}

func runtimeMetricCounts(measurements []observability.Measurement) map[observability.MetricName]int {
	counts := make(map[observability.MetricName]int)
	for _, measurement := range measurements {
		counts[measurement.Name]++
	}
	return counts
}

type panickingRuntimeMetrics struct{}

func (panickingRuntimeMetrics) Record(context.Context, observability.Measurement) error {
	panic("metrics exporter failed")
}

var _ observability.Metrics = panickingRuntimeMetrics{}
