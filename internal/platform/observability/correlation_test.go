package observability

import (
	"context"
	"testing"
)

func TestWithCorrelationPreservesParentAndRepresentsInitialRetry(t *testing.T) {
	ctx := WithCorrelation(context.Background(), Correlation{RequestID: "request-1", RetryNo: 4})
	ctx = WithCorrelation(ctx, Correlation{WorkflowRunID: "run-1", AttemptNo: 1, DispatchNo: 1, RetryNo: 0})
	got := CorrelationFromContext(ctx)
	if got.RequestID != "request-1" || got.WorkflowRunID != "run-1" || got.AttemptNo != 1 || got.DispatchNo != 1 || got.RetryNo != 0 {
		t.Fatalf("correlation = %#v", got)
	}
	attrs := correlationAttrs(got)
	foundRetry := false
	for _, attr := range attrs {
		if attr.Key == "retry_no" {
			foundRetry = attr.Value.Int64() == 0
		}
	}
	if !foundRetry {
		t.Fatal("initial retry_no=0 must remain observable")
	}
}
