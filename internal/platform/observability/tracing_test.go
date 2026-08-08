package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTracePropagationKeepsOnlyValidatedTraceParent(t *testing.T) {
	parent := TraceContext{
		TraceID:    "0123456789abcdef0123456789abcdef",
		SpanID:     "0123456789abcdef",
		TraceFlags: "01",
	}
	ctx, err := WithTraceContext(context.Background(), parent)
	if err != nil {
		t.Fatalf("WithTraceContext: %v", err)
	}
	metadata := EncodeTraceMetadata(ctx)
	if len(metadata) != 1 || metadata[TraceParentMetadataKey] != "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01" {
		t.Fatalf("metadata = %#v", metadata)
	}

	decoded, err := DecodeTraceMetadata(context.Background(), metadata)
	if err != nil {
		t.Fatalf("DecodeTraceMetadata: %v", err)
	}
	got, found := TraceContextFromContext(decoded)
	if !found || got != parent {
		t.Fatalf("decoded trace = %#v, found=%t", got, found)
	}
	if correlation := CorrelationFromContext(decoded); correlation.TraceID != parent.TraceID {
		t.Fatalf("correlation trace id = %q", correlation.TraceID)
	}

	_, err = DecodeTraceMetadata(context.Background(), map[string]string{
		TraceParentMetadataKey: metadata[TraceParentMetadataKey],
		"credential":           "must-not-cross-boundary",
	})
	if !errors.Is(err, ErrInvalidTraceContext) {
		t.Fatalf("unknown trace metadata error = %v", err)
	}
}

func TestMemoryTracerMaintainsParentAndRedactsAttributes(t *testing.T) {
	tracer := NewMemoryTracer()
	ctx := WithCorrelation(context.Background(), Correlation{
		WorkspaceID: "workspace-1", WorkflowRunID: "run-1", NodeRunID: "node-1", RiverJobID: 9,
	})
	ctx, parentSpan, err := tracer.Start(ctx, "approval.dispatch", TraceAttribute{Key: "credential", Value: "credential-secret"})
	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}
	_, childSpan, err := tracer.Start(ctx, "safe_writeback.execute", TraceAttribute{Key: "target_path", Value: "/Users/example/private.md"})
	if err != nil {
		t.Fatalf("Start child: %v", err)
	}
	childSpan.RecordError("password=database-secret")
	childSpan.End()
	parentSpan.End()

	spans := tracer.Snapshot()
	if len(spans) != 2 {
		t.Fatalf("span count = %d", len(spans))
	}
	child := spans[0]
	parent := spans[1]
	if child.ParentSpanID != parent.TraceContext.SpanID || child.TraceContext.TraceID != parent.TraceContext.TraceID {
		t.Fatalf("trace linkage child=%#v parent=%#v", child, parent)
	}
	if parent.Attributes["credential"] != RedactedValue || child.Attributes["target_path"] != RedactedValue || child.ErrorCode != RedactedValue {
		t.Fatalf("redaction parent=%#v child=%#v", parent.Attributes, child)
	}
	if child.Attributes["workflow_run_id"] != "run-1" || child.Attributes["node_run_id"] != "node-1" || child.Attributes["river_job_id"] != "9" {
		t.Fatalf("missing correlation attributes: %#v", child.Attributes)
	}
	rendered := strings.Join([]string{parent.Attributes["credential"], child.Attributes["target_path"], child.ErrorCode}, " ")
	for _, secret := range []string{"credential-secret", "/Users/example", "database-secret"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("trace leaked %q: %s", secret, rendered)
		}
	}
}

func TestMemorySpanRecordErrorAllowsOnlyStableErrorCodes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "stable code", input: "WORKFLOW_RETRY_1", want: "WORKFLOW_RETRY_1"},
		{name: "surrounding whitespace", input: " WORKFLOW_FAILED ", want: "WORKFLOW_FAILED"},
		{name: "clean raw error", input: "connection refused", want: RedactedValue},
		{name: "lowercase identifier", input: "workflow_failed", want: RedactedValue},
		{name: "sensitive error", input: "password=database-secret", want: RedactedValue},
		{name: "oversized code", input: strings.Repeat("A", 65), want: RedactedValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracer := NewMemoryTracer()
			_, span, err := tracer.Start(context.Background(), "workflow.node.consume")
			if err != nil {
				t.Fatal(err)
			}
			span.RecordError(test.input)
			span.End()
			spans := tracer.Snapshot()
			if len(spans) != 1 || spans[0].ErrorCode != test.want {
				t.Fatalf("RecordError(%q) snapshot=%#v want=%q", test.input, spans, test.want)
			}
		})
	}
}

func TestMemoryTracerRejectsStartAfterClose(t *testing.T) {
	tracer := NewMemoryTracer()
	if err := tracer.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, err := tracer.Start(context.Background(), "workflow.start"); !errors.Is(err, ErrObservabilityClosed) {
		t.Fatalf("Start after shutdown = %v, want closed", err)
	}
}

func TestNoopTracerCreatesPropagatableChildContext(t *testing.T) {
	parent, err := WithTraceContext(context.Background(), TraceContext{
		TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", TraceFlags: "01",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, span, err := NewNoopTracer().Start(parent, "approval.dispatch")
	if err != nil {
		t.Fatal(err)
	}
	defer span.End()
	trace, found := TraceContextFromContext(child)
	if !found || trace.TraceID != "0123456789abcdef0123456789abcdef" || trace.SpanID == "0123456789abcdef" {
		t.Fatalf("child trace=%+v found=%t", trace, found)
	}
}
