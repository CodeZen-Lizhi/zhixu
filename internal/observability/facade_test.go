package observability

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestFacadeUsesPlatformRedactionAndCorrelation(t *testing.T) {
	var output bytes.Buffer
	ctx := WithCorrelation(context.Background(), Correlation{RequestID: "request-1"})
	NewLogger("info", &output).InfoContext(ctx, "completed",
		"token_hash", "must-not-leak",
		"content_hash", "sha256:safe-summary",
	)
	got := output.String()
	if strings.Contains(got, "must-not-leak") || !strings.Contains(got, RedactedValue) {
		t.Fatalf("facade logger did not apply platform redaction: %s", got)
	}
	if !strings.Contains(got, "request-1") || !strings.Contains(got, "sha256:safe-summary") {
		t.Fatalf("facade logger lost safe fields: %s", got)
	}
}

func TestFacadeAliasesTelemetryAndTraceContracts(t *testing.T) {
	if MetricModelCallDuration != "model.chat.duration_ms" || MetricModelCallTotal != "model.chat.result_total" {
		t.Fatal("facade omitted model callback metric aliases")
	}
	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{Mode: TelemetryModeDisabled})
	if err != nil {
		t.Fatalf("InitializeTelemetry: %v", err)
	}
	if telemetry.Status().Code != TelemetryStatusDisabled {
		t.Fatalf("status=%#v", telemetry.Status())
	}
	ctx, span, err := NewNoopTracer().Start(context.Background(), "audit.append")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	span.End()
	if _, found := TraceContextFromContext(ctx); !found {
		t.Fatal("facade tracer did not preserve trace context")
	}
}
