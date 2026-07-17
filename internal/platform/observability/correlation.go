package observability

import (
	"context"
	"log/slog"
)

type correlationContextKey struct{}

// Correlation carries identifiers that may be attached to logs and traces.
// Identifiers deliberately never become metric labels.
type Correlation struct {
	RequestID     string
	TraceID       string
	WorkspaceID   string
	WorkflowRunID string
	NodeRunID     string
	ProposalID    string
	ToolCallID    string
	DocumentID    string
	AttemptNo     int
	DispatchNo    int
	RetryNo       int
	RiverJobID    int64
}

// WithCorrelation merges correlation fields into ctx. Empty and non-positive
// fields preserve the parent value, which keeps nested asynchronous boundaries
// from accidentally dropping an existing run or request identifier.
func WithCorrelation(ctx context.Context, next Correlation) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	merged := mergeCorrelation(CorrelationFromContext(ctx), next)
	return context.WithValue(ctx, correlationContextKey{}, merged)
}

// CorrelationFromContext returns the current correlation or its zero value.
func CorrelationFromContext(ctx context.Context) Correlation {
	if ctx == nil {
		return Correlation{}
	}
	value, _ := ctx.Value(correlationContextKey{}).(Correlation)
	return value
}

func mergeCorrelation(current, next Correlation) Correlation {
	if next.RequestID != "" {
		current.RequestID = next.RequestID
	}
	if next.TraceID != "" {
		current.TraceID = next.TraceID
	}
	if next.WorkspaceID != "" {
		current.WorkspaceID = next.WorkspaceID
	}
	if next.WorkflowRunID != "" {
		current.WorkflowRunID = next.WorkflowRunID
	}
	if next.NodeRunID != "" {
		current.NodeRunID = next.NodeRunID
	}
	if next.ProposalID != "" {
		current.ProposalID = next.ProposalID
	}
	if next.ToolCallID != "" {
		current.ToolCallID = next.ToolCallID
	}
	if next.DocumentID != "" {
		current.DocumentID = next.DocumentID
	}
	if next.AttemptNo > 0 {
		current.AttemptNo = next.AttemptNo
	}
	if next.DispatchNo > 0 {
		current.DispatchNo = next.DispatchNo
	}
	if next.RetryNo > 0 || next.AttemptNo > 0 || next.DispatchNo > 0 {
		current.RetryNo = next.RetryNo
	}
	if next.RiverJobID > 0 {
		current.RiverJobID = next.RiverJobID
	}
	return current
}

func correlationAttrs(correlation Correlation) []slog.Attr {
	attrs := make([]slog.Attr, 0, 12)
	attrs = appendStringAttr(attrs, "request_id", correlation.RequestID)
	attrs = appendStringAttr(attrs, "trace_id", correlation.TraceID)
	attrs = appendStringAttr(attrs, "workspace_id", correlation.WorkspaceID)
	attrs = appendStringAttr(attrs, "workflow_run_id", correlation.WorkflowRunID)
	attrs = appendStringAttr(attrs, "node_run_id", correlation.NodeRunID)
	attrs = appendStringAttr(attrs, "proposal_id", correlation.ProposalID)
	attrs = appendStringAttr(attrs, "tool_call_id", correlation.ToolCallID)
	attrs = appendStringAttr(attrs, "document_id", correlation.DocumentID)
	if correlation.AttemptNo > 0 {
		attrs = append(attrs, slog.Int("attempt_no", correlation.AttemptNo))
	}
	if correlation.DispatchNo > 0 {
		attrs = append(attrs, slog.Int("dispatch_no", correlation.DispatchNo))
	}
	if correlation.RetryNo > 0 || correlation.AttemptNo > 0 || correlation.DispatchNo > 0 {
		attrs = append(attrs, slog.Int("retry_no", correlation.RetryNo))
	}
	if correlation.RiverJobID > 0 {
		attrs = append(attrs, slog.Int64("river_job_id", correlation.RiverJobID))
	}
	return attrs
}

func appendStringAttr(attrs []slog.Attr, key, value string) []slog.Attr {
	if value == "" {
		return attrs
	}
	return append(attrs, slog.String(key, value))
}
