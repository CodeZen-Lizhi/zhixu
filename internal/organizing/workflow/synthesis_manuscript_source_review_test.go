package workflow

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestSourceReviewExecutorPreservesOwnerUntilReadRetryBudgetEnds(t *testing.T) {
	id := foundation.ID("00000000-0000-4000-8000-000000000001")
	workspace := foundation.ID("00000000-0000-4000-8000-000000000002")
	transient := foundation.NewError(foundation.ErrorDependencyUnavailable, "DEPENDENCY_BUSY", true, nil)
	for _, kind := range []string{app.SynthesisSourceReviewPrepare, app.SynthesisSourceReviewApply, app.SynthesisSourceReviewModel} {
		t.Run(kind, func(t *testing.T) {
			store := &sourceReviewRetryStore{err: transient}
			executor := SourceReviewExecutor{Runs: sourceReviewRetryRun{workflowdomain.Run{WorkspaceID: workspace, Input: []byte(`{"review_id":"` + string(id) + `"}`), IdempotencyKey: sourceReviewStartKey(id)}}, Store: store, Model: store}
			execution := workflowapp.ExecutionContext{WorkspaceID: workspace, NodeKind: kind, AttemptNo: 1}
			if _, err := executor.Execute(t.Context(), execution); err == nil {
				t.Fatal("transient failure was reported as success")
			}
			if kind == app.SynthesisSourceReviewModel {
				if store.failures != 1 {
					t.Fatal("model uncertainty was left eligible for automatic retry")
				}
				return
			}
			if store.failures != 0 {
				t.Fatal("read owner was made terminal before workflow retry")
			}
			store.err = nil
			execution.AttemptNo++
			if result, err := executor.Execute(t.Context(), execution); err != nil || len(result.Output) == 0 || store.failures != 0 {
				t.Fatalf("read retry did not recover: err=%v failures=%d", err, store.failures)
			}
			store.err = transient
			execution.AttemptNo = sourceReviewReadRetries + 1
			if _, err := executor.Execute(t.Context(), execution); err == nil || store.failures != 1 {
				t.Fatalf("exhausted read failure was not persisted: err=%v failures=%d", err, store.failures)
			}
		})
	}
}

type sourceReviewRetryRun struct{ run workflowdomain.Run }

func (r sourceReviewRetryRun) GetRun(context.Context, foundation.ID) (workflowdomain.Run, error) {
	return r.run, nil
}

type sourceReviewRetryStore struct {
	app.SynthesisSourceReviewStore
	err      error
	failures int
}

func (s *sourceReviewRetryStore) PrepareSourceReview(context.Context, workflowapp.ExecutionContext, foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	return app.SynthesisManuscriptSourceReview{Status: "PREPARED"}, s.err
}
func (s *sourceReviewRetryStore) ApplySourceReview(context.Context, workflowapp.ExecutionContext, foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	return app.SynthesisManuscriptSourceReview{Status: "SUCCEEDED"}, s.err
}
func (s *sourceReviewRetryStore) Review(context.Context, workflowapp.ExecutionContext, foundation.ID) (app.SynthesisManuscriptSourceReview, error) {
	return app.SynthesisManuscriptSourceReview{}, s.err
}
func (s *sourceReviewRetryStore) FailSourceReview(context.Context, foundation.ID, foundation.ID, foundation.ID, error) error {
	s.failures++
	return nil
}
