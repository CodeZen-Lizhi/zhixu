package workspace

import (
	"context"
	"errors"

	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

// ReadGitStatusV3Executor reuses the fixed-argument inspector under the v2 tool tuple.
type ReadGitStatusV3Executor struct{ inner *ReadGitStatusV2Executor }

func NewReadGitStatusV3Executor(inspector gitStatusAggregateInspector) (*ReadGitStatusV3Executor, error) {
	inner, err := NewReadGitStatusV2Executor(inspector)
	if err != nil {
		return nil, err
	}
	return &ReadGitStatusV3Executor{inner: inner}, nil
}
func (e *ReadGitStatusV3Executor) Execute(ctx context.Context, r toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	if r.Identity.DefinitionVersion != 2 {
		return toolsapplication.ExecutorResult{}, gitInputError(errors.New("dynamic Git status requires workspace analysis v2"))
	}
	var inner *ReadGitStatusV2Executor
	if e != nil {
		inner = e.inner
	}
	return inner.execute(ctx, r, toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 3})
}
