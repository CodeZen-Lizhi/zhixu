package workflow

import (
	"context"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const SynthesisManuscriptDefinitionVersion int64 = 2
const SynthesisMergeReviewNodeKind = "organizing.synthesis.merge_review"

// SynthesisManuscriptRuntime 只能使用运行时的类型化领取凭证调用。
// 准备阶段先持久保存全部尝试，再返回持久人工等待。
type SynthesisManuscriptRuntime interface {
	PrepareManuscripts(context.Context, workflowapp.ExecutionContext, app.SynthesisGenerationInput, app.SynthesisGenerationResult) (*workflowapp.HumanWaitResult, error)
	ApplyManuscripts(context.Context, workflowapp.ExecutionContext, app.SynthesisGenerationInput, app.SynthesisGenerationResult) (app.SynthesisApplyResult, error)
}

func synthesisDispatchVersion(manuscripts bool) int64 {
	if manuscripts {
		return SynthesisManuscriptDefinitionVersion
	}
	return SynthesisDefinitionVersion
}
