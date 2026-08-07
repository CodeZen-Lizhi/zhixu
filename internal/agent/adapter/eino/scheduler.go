// Package eino 提供受项目 Application Port 约束的 Eino 短阶段调度器。
package eino

import (
	"context"
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/cloudwego/eino/compose"
)

const (
	graphName       = "zhixu_structured_output_scheduler"
	initialNodeName = "initial_call_validate"
	repairNodeName  = "repair_call_validate"
	reducedNodeName = "reduced_call_validate"
	graphBuildCode  = "AGENT_EINO_STRUCTURED_GRAPH_BUILD_FAILED"
	graphInvokeCode = "AGENT_EINO_STRUCTURED_GRAPH_INVOKE_FAILED"
	graphStateCode  = "AGENT_EINO_STRUCTURED_GRAPH_STATE_INVALID"
	graphOutputCode = "AGENT_EINO_STRUCTURED_GRAPH_OUTPUT_INVALID"
)

// StructuredPhaseScheduler 是 Eino Graph 对项目 phase-scheduler Port 的实现。
// Graph 本身不保存 checkpoint、不自动重试，也不持有持久工作流状态。
type StructuredPhaseScheduler struct {
	invoke func(context.Context, *phaseGraphState) (*phaseGraphState, error)
}

// phaseGraphState 避免把含 Prompt/Input 的 Application 状态直接暴露给 Eino callback。
type phaseGraphState struct {
	run *agentapplication.StructuredPhaseRun
}

var _ agentapplication.StructuredPhaseScheduler = (*StructuredPhaseScheduler)(nil)

// NewStructuredPhaseScheduler 编译固定的 INITIAL→REPAIR→REDUCED 短 Graph。
// 每个节点只调用 Application 的 Advance；成功时通过分支提前结束。
func NewStructuredPhaseScheduler(ctx context.Context) (*StructuredPhaseScheduler, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	graph := compose.NewGraph[*phaseGraphState, *phaseGraphState]()
	if err := graph.AddLambdaNode(initialNodeName, phaseNode(domain.ModelCallInitial)); err != nil {
		return nil, graphBuildError(err)
	}
	if err := graph.AddLambdaNode(repairNodeName, phaseNode(domain.ModelCallRepair)); err != nil {
		return nil, graphBuildError(err)
	}
	if err := graph.AddLambdaNode(reducedNodeName, phaseNode(domain.ModelCallReduced)); err != nil {
		return nil, graphBuildError(err)
	}
	if err := graph.AddEdge(compose.START, initialNodeName); err != nil {
		return nil, graphBuildError(err)
	}
	if err := graph.AddBranch(initialNodeName, phaseBranch(repairNodeName)); err != nil {
		return nil, graphBuildError(err)
	}
	if err := graph.AddBranch(repairNodeName, phaseBranch(reducedNodeName)); err != nil {
		return nil, graphBuildError(err)
	}
	if err := graph.AddEdge(reducedNodeName, compose.END); err != nil {
		return nil, graphBuildError(err)
	}

	runnable, err := graph.Compile(ctx, compose.WithGraphName(graphName))
	if err != nil {
		return nil, graphBuildError(err)
	}
	return &StructuredPhaseScheduler{
		invoke: func(runCtx context.Context, state *phaseGraphState) (*phaseGraphState, error) {
			return runnable.Invoke(runCtx, state)
		},
	}, nil
}

func phaseNode(phase domain.ModelCallPhase) *compose.Lambda {
	return compose.InvokableLambda(func(ctx context.Context, state *phaseGraphState) (*phaseGraphState, error) {
		if state == nil || state.run == nil {
			return state, foundation.NewError(foundation.ErrorConsistencyViolation, graphStateCode, false, errors.New("structured graph received an invalid phase state"))
		}
		if err := state.run.Advance(ctx, phase); err != nil {
			return state, err
		}
		return state, nil
	})
}

func phaseBranch(next string) *compose.GraphBranch {
	return compose.NewGraphBranch(func(_ context.Context, state *phaseGraphState) (string, error) {
		if state == nil || state.run == nil {
			return "", foundation.NewError(foundation.ErrorConsistencyViolation, graphStateCode, false, errors.New("structured graph received a nil phase state"))
		}
		if state.run.Completed() {
			return compose.END, nil
		}
		return next, nil
	}, map[string]bool{compose.END: true, next: true})
}

// Schedule 运行一次短 Graph，并原样返回 Application 产生的业务错误。
func (scheduler *StructuredPhaseScheduler) Schedule(ctx context.Context, run *agentapplication.StructuredPhaseRun) error {
	if scheduler == nil || scheduler.invoke == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, graphInvokeCode, false, errors.New("structured graph scheduler is unavailable"))
	}
	if run == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, graphStateCode, false, errors.New("structured graph phase state is nil"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state := &phaseGraphState{run: run}
	output, err := scheduler.invoke(ctx, state)
	if err != nil {
		if failure := run.Failure(); failure != nil {
			return failure
		}
		return mapGraphError(err)
	}
	if output != state {
		return foundation.NewError(foundation.ErrorConsistencyViolation, graphOutputCode, false, errors.New("structured graph returned a different phase state"))
	}
	if failure := run.Failure(); failure != nil {
		return failure
	}
	return nil
}

func graphBuildError(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, graphBuildCode, false, err)
}

func mapGraphError(err error) error {
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
	return foundation.NewError(foundation.ErrorNonRetryableFailure, graphInvokeCode, false, err)
}
