package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const sourceReviewReadRetries = 3

func SourceReviewDefinitions() []workflowdomain.RegisteredDefinition {
	kinds := []string{app.SynthesisSourceReviewPrepare, app.SynthesisSourceReviewModel, app.SynthesisSourceReviewApply}
	nodes := []workflowdomain.NodeDefinition{}
	for i, k := range kinds {
		var dependencies []string
		if i > 0 {
			dependencies = []string{kinds[i-1]}
		}
		retry := workflowdomain.RetryPolicy{}
		if i != 1 {
			retry = workflowdomain.RetryPolicy{MaxRetries: sourceReviewReadRetries, BaseDelay: time.Second, MaxDelay: 10 * time.Second}
		}
		nodes = append(nodes, workflowdomain.NodeDefinition{Key: k, Kind: k, Dependencies: dependencies, InputSchemaVersion: 1, OutputSchemaVersion: 1, RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionReadLocal, workflowdomain.PermissionWriteProposal}, RetryPolicy: retry})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Key < nodes[j].Key })
	return []workflowdomain.RegisteredDefinition{{Key: app.SynthesisSourceReviewDefinition, Version: 1, InputSchemaVersion: 1, Graph: workflowdomain.CanonicalGraph{Nodes: nodes}}, {Key: app.SynthesisSourceReviewRecoveryDefinition, Version: 1, InputSchemaVersion: 1, Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{Key: app.SynthesisSourceReviewRecover, Kind: app.SynthesisSourceReviewRecover, InputSchemaVersion: 1, OutputSchemaVersion: 1, RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionReadLocal, workflowdomain.PermissionWriteProposal}, RetryPolicy: workflowdomain.RetryPolicy{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: 10 * time.Second}}}}}}
}

type SourceReviewModel interface {
	Review(context.Context, workflowapp.ExecutionContext, foundation.ID) (app.SynthesisManuscriptSourceReview, error)
}
type SourceReviewExecutor struct {
	Runs  WorkflowRunReader
	Store app.SynthesisSourceReviewStore
	Model SourceReviewModel
}
type sourceReviewStart struct {
	ReviewID foundation.ID `json:"review_id"`
}

func (e *SourceReviewExecutor) Execute(ctx context.Context, execution workflowapp.ExecutionContext) (workflowapp.ExecutionResult, error) {
	if e == nil || nilScopedDependency(e.Runs) || nilScopedDependency(e.Store) || nilScopedDependency(e.Model) {
		return workflowapp.ExecutionResult{}, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE")
	}
	run, err := e.Runs.GetRun(ctx, execution.RunID)
	if err != nil {
		return workflowapp.ExecutionResult{}, err
	}
	input, err := strictjson.DecodeObject[sourceReviewStart](run.Input, strictjson.Limits{MaxDocumentBytes: 256, MaxDepth: 2, MaxStringBytes: 64, MaxArrayItems: 1, MaxObjectFields: 1}, nil)
	if err != nil || !validID(input.ReviewID) || run.WorkspaceID != execution.WorkspaceID || (execution.NodeKind != app.SynthesisSourceReviewRecover && run.IdempotencyKey != sourceReviewStartKey(input.ReviewID)) {
		return workflowapp.ExecutionResult{}, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
	}
	var result app.SynthesisManuscriptSourceReview
	switch execution.NodeKind {
	case app.SynthesisSourceReviewPrepare:
		result, err = e.Store.PrepareSourceReview(ctx, execution, input.ReviewID)
	case app.SynthesisSourceReviewModel:
		result, err = e.Model.Review(ctx, execution, input.ReviewID)
	case app.SynthesisSourceReviewApply, app.SynthesisSourceReviewRecover:
		result, err = e.Store.ApplySourceReview(ctx, execution, input.ReviewID)
	default:
		err = app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_EXECUTION_INVALID")
	}
	if err != nil {
		// 准备和应用阶段可以安全使用固定的工作流重试预算。
		// 不能在已计划重试执行前将其所属模块标记为终态。
		// 模型步骤始终记录不确定结果，绝不自动重新执行。
		failure, classifyErr := workflowdomain.ClassifyFailure(workflowdomain.FailureInput{Err: err})
		readNode := execution.NodeKind == app.SynthesisSourceReviewPrepare || (execution.NodeKind == app.SynthesisSourceReviewApply || execution.NodeKind == app.SynthesisSourceReviewRecover)
		if readNode && execution.AttemptNo >= 1 && execution.AttemptNo <= sourceReviewReadRetries && classifyErr == nil && failure.Class == workflowdomain.FailureClassRetryable {
			return workflowapp.ExecutionResult{}, err
		}
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if execution.NodeKind == app.SynthesisSourceReviewRecover {
			return workflowapp.ExecutionResult{}, err
		}
		saveErr := e.Store.FailSourceReview(recoveryCtx, execution.WorkspaceID, input.ReviewID, execution.RunID, err)
		return workflowapp.ExecutionResult{}, errors.Join(err, saveErr)
	}
	raw, _ := json.Marshal(struct {
		ReviewID foundation.ID `json:"review_id"`
		Status   string        `json:"status"`
	}{result.ID, result.Status})
	return workflowapp.ExecutionResult{Output: raw}, nil
}
func sourceReviewStartKey(id foundation.ID) string { return "source-review-start:" + string(id) }
