package eino

import (
	"context"
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/cloudwego/eino/compose"
)

const (
	ragGraphName                 = "zhixu_rag_runtime"
	ragPlanNode                  = "query_plan"
	ragRetrievalNode             = "scoped_retrieval"
	ragEvidenceNode              = "evidence_gate"
	ragGenerationNode            = "structured_generation"
	ragPublicationNode           = "publication_gate"
	ragClarificationTerminalNode = "clarification_terminal"
	ragRetrievalRefusalNode      = "retrieval_refusal_terminal"
	ragEvidenceRefusalNode       = "evidence_refusal_terminal"
	ragGenerationRefusalNode     = "generation_refusal_terminal"
	ragPublicationRefusalNode    = "publication_refusal_terminal"
	ragAnswerTerminalNode        = "answer_terminal"
	ragGraphBuildCode            = "AGENT_EINO_RAG_GRAPH_BUILD_FAILED"
	ragGraphInvokeCode           = "AGENT_EINO_RAG_GRAPH_INVOKE_FAILED"
	ragGraphStateCode            = "AGENT_EINO_RAG_GRAPH_STATE_INVALID"
	ragGraphOutputCode           = "AGENT_EINO_RAG_GRAPH_OUTPUT_INVALID"
)

// RAGExecutionScheduler 用 Eino Graph 驱动项目定义的 RAG 阶段。
// Graph 不持久化状态、不拥有检索/证据规则，也不直接调用 Provider。
type RAGExecutionScheduler struct {
	invoke      func(context.Context, *ragGraphState) (*ragGraphState, error)
	observeNode func(string)
	observer    runtimeMetricObserver
}

type ragGraphState struct {
	run         *agentapplication.RAGPhaseRun
	route       agentapplication.RAGPhaseRoute
	observeNode func(string)
	observer    runtimeMetricObserver
}

var _ agentapplication.RAGExecutionScheduler = (*RAGExecutionScheduler)(nil)

// NewRAGExecutionScheduler 在启动时编译固定 RAG Graph。
func NewRAGExecutionScheduler(ctx context.Context) (*RAGExecutionScheduler, error) {
	return newRAGExecutionScheduler(ctx, nil)
}

// NewRAGExecutionSchedulerWithMetrics 编译带 Eino Graph 节点结果指标的 RAG 调度器。
func NewRAGExecutionSchedulerWithMetrics(ctx context.Context, metrics observability.Metrics) (*RAGExecutionScheduler, error) {
	return newRAGExecutionScheduler(ctx, nil, metrics)
}

func newRAGExecutionScheduler(ctx context.Context, observeNode func(string), metrics ...observability.Metrics) (*RAGExecutionScheduler, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	graph := compose.NewGraph[*ragGraphState, *ragGraphState]()
	for _, node := range []struct {
		name string
		run  ragDomainNodeRun
	}{
		{ragPlanNode, func(ctx context.Context, run *agentapplication.RAGPhaseRun) (agentapplication.RAGPhaseOutcome, error) {
			return run.Plan(ctx)
		}},
		{ragRetrievalNode, func(ctx context.Context, run *agentapplication.RAGPhaseRun) (agentapplication.RAGPhaseOutcome, error) {
			return run.Retrieval(ctx)
		}},
		{ragEvidenceNode, func(ctx context.Context, run *agentapplication.RAGPhaseRun) (agentapplication.RAGPhaseOutcome, error) {
			return run.Evidence(ctx)
		}},
		{ragGenerationNode, func(ctx context.Context, run *agentapplication.RAGPhaseRun) (agentapplication.RAGPhaseOutcome, error) {
			return run.Generation(ctx)
		}},
		{ragPublicationNode, func(ctx context.Context, run *agentapplication.RAGPhaseRun) (agentapplication.RAGPhaseOutcome, error) {
			return run.Publication(ctx)
		}},
	} {
		if err := graph.AddLambdaNode(node.name, ragDomainNode(node.name, node.run)); err != nil {
			return nil, ragGraphBuildError(err)
		}
	}
	for _, terminal := range []struct {
		name  string
		route agentapplication.RAGPhaseRoute
	}{
		{ragClarificationTerminalNode, agentapplication.RAGPhaseClarification},
		{ragRetrievalRefusalNode, agentapplication.RAGPhaseRefusal},
		{ragEvidenceRefusalNode, agentapplication.RAGPhaseRefusal},
		{ragGenerationRefusalNode, agentapplication.RAGPhaseRefusal},
		{ragPublicationRefusalNode, agentapplication.RAGPhaseRefusal},
		{ragAnswerTerminalNode, agentapplication.RAGPhaseAnswer},
	} {
		if err := graph.AddLambdaNode(terminal.name, ragTerminalNode(terminal.name, terminal.route)); err != nil {
			return nil, ragGraphBuildError(err)
		}
		if err := graph.AddEdge(terminal.name, compose.END); err != nil {
			return nil, ragGraphBuildError(err)
		}
	}
	if err := graph.AddEdge(compose.START, ragPlanNode); err != nil {
		return nil, ragGraphBuildError(err)
	}
	for _, branch := range []struct {
		from   string
		routes map[agentapplication.RAGPhaseRoute]string
	}{
		{ragPlanNode, map[agentapplication.RAGPhaseRoute]string{
			agentapplication.RAGPhaseContinue: ragRetrievalNode, agentapplication.RAGPhaseClarification: ragClarificationTerminalNode,
		}},
		{ragRetrievalNode, map[agentapplication.RAGPhaseRoute]string{
			agentapplication.RAGPhaseContinue: ragEvidenceNode, agentapplication.RAGPhaseRefusal: ragRetrievalRefusalNode,
		}},
		{ragEvidenceNode, map[agentapplication.RAGPhaseRoute]string{
			agentapplication.RAGPhaseContinue: ragGenerationNode, agentapplication.RAGPhaseRefusal: ragEvidenceRefusalNode,
		}},
		{ragGenerationNode, map[agentapplication.RAGPhaseRoute]string{
			agentapplication.RAGPhaseContinue: ragPublicationNode, agentapplication.RAGPhaseRefusal: ragGenerationRefusalNode,
		}},
		{ragPublicationNode, map[agentapplication.RAGPhaseRoute]string{
			agentapplication.RAGPhaseAnswer: ragAnswerTerminalNode, agentapplication.RAGPhaseRefusal: ragPublicationRefusalNode,
		}},
	} {
		if err := graph.AddBranch(branch.from, ragRouteBranch(branch.routes)); err != nil {
			return nil, ragGraphBuildError(err)
		}
	}
	runnable, err := graph.Compile(ctx, compose.WithGraphName(ragGraphName))
	if err != nil {
		return nil, ragGraphBuildError(err)
	}
	var selectedMetrics observability.Metrics
	if len(metrics) > 0 {
		selectedMetrics = metrics[len(metrics)-1]
	}
	return &RAGExecutionScheduler{
		invoke: func(runCtx context.Context, state *ragGraphState) (*ragGraphState, error) {
			return runnable.Invoke(runCtx, state)
		},
		observeNode: observeNode,
		observer:    newRuntimeMetricObserver(selectedMetrics),
	}, nil
}

type ragDomainNodeRun func(context.Context, *agentapplication.RAGPhaseRun) (agentapplication.RAGPhaseOutcome, error)

func ragDomainNode(name string, runNode ragDomainNodeRun) *compose.Lambda {
	return compose.InvokableLambda(func(ctx context.Context, state *ragGraphState) (*ragGraphState, error) {
		if state == nil || state.run == nil || runNode == nil {
			return state, foundation.NewError(foundation.ErrorConsistencyViolation, ragGraphStateCode, false, errors.New("rag graph received an invalid phase state"))
		}
		state.visit(name)
		state.route = ""
		outcome, err := runNode(ctx, state.run)
		state.observer.observeRAGGraphNode(ctx, name, err)
		if err != nil {
			return state, err
		}
		state.route = outcome.Route
		return state, nil
	})
}

func ragRouteBranch(routes map[agentapplication.RAGPhaseRoute]string) *compose.GraphBranch {
	endNodes := make(map[string]bool, len(routes))
	for _, node := range routes {
		endNodes[node] = true
	}
	return compose.NewGraphBranch(func(_ context.Context, state *ragGraphState) (string, error) {
		if state == nil || state.run == nil {
			return "", foundation.NewError(foundation.ErrorConsistencyViolation, ragGraphStateCode, false, errors.New("rag graph received a nil phase state"))
		}
		next, ok := routes[state.route]
		if !ok {
			return "", foundation.NewError(foundation.ErrorConsistencyViolation, ragGraphStateCode, false, errors.New("rag graph node returned a route outside its declared branch"))
		}
		return next, nil
	}, endNodes)
}

func ragTerminalNode(name string, route agentapplication.RAGPhaseRoute) *compose.Lambda {
	return compose.InvokableLambda(func(ctx context.Context, state *ragGraphState) (*ragGraphState, error) {
		if state == nil || state.run == nil || state.route != route || !state.run.Completed() {
			err := foundation.NewError(foundation.ErrorConsistencyViolation, ragGraphStateCode, false, errors.New("rag graph terminal route is not bound to a completed project proposal"))
			if state != nil {
				state.observer.observeRAGGraphNode(ctx, name, err)
			}
			return state, err
		}
		state.visit(name)
		state.observer.observeRAGGraphNode(ctx, name, nil)
		return state, nil
	})
}

func (state *ragGraphState) visit(name string) {
	if state != nil && state.observeNode != nil {
		state.observeNode(name)
	}
}

// Schedule 执行一次 RAG Graph，并保留 Application 产生的稳定错误。
func (scheduler *RAGExecutionScheduler) Schedule(ctx context.Context, run *agentapplication.RAGPhaseRun) error {
	if scheduler == nil || scheduler.invoke == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, ragGraphInvokeCode, false, errors.New("rag graph scheduler is unavailable"))
	}
	if run == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, ragGraphStateCode, false, errors.New("rag graph phase state is nil"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state := &ragGraphState{run: run, observeNode: scheduler.observeNode, observer: scheduler.observer}
	output, err := scheduler.invoke(ctx, state)
	if err != nil {
		if failure := run.Failure(); failure != nil {
			return failure
		}
		return mapRAGGraphError(err)
	}
	if output != state {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ragGraphOutputCode, false, errors.New("rag graph returned a different phase state"))
	}
	if failure := run.Failure(); failure != nil {
		return failure
	}
	return nil
}

func ragGraphBuildError(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ragGraphBuildCode, false, err)
}

func mapRAGGraphError(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, agentapplication.ErrorCodeOperationCancelled, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, agentapplication.ErrorCodeOperationDeadline, true, err)
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, ragGraphInvokeCode, false, err)
}
