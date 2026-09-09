// Package agent adapts synthesis generation to the existing recorded Agent runtime.
package agent

import (
	"context"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// SynthesisModelDependencies are provided by the process composition. No model,
// pool, scheduler or configuration selector is constructed by the adapter.
type SynthesisModelDependencies struct {
	Model      agentapp.ChatModel
	Scheduler  agentapp.StructuredPhaseScheduler
	Catalog    *agentapp.RuntimeCatalog
	ModelRuns  agentapp.ModelRunRepository
	Store      organizingapp.SynthesisModelStore
	ProfileRef agentdomain.ModelProfileRef
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
	Budget     agentapp.RunBudget
}

type synthesisExecutionKey struct{}

// WithSynthesisExecution binds current trusted execution without changing the
// frozen generation input. Workflow uses the explicit ForExecution methods.
func WithSynthesisExecution(ctx context.Context, execution workflowapp.ExecutionContext) context.Context {
	if ctx == nil {
		return nil
	}
	execution.ModelSettingsRevision = cloneSynthesisRevisionNumber(execution.ModelSettingsRevision)
	return context.WithValue(ctx, synthesisExecutionKey{}, execution)
}

func cloneSynthesisRevisionNumber(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
