package models

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformobservability "github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

const (
	einoChatCallbackName       = "zhixu.chat.generate"
	einoChatTraceOperation     = "model.chat.generate"
	einoChatTelemetryComponent = "eino_chat"
)

var telemetryErrorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

type einoChatCallbackCall struct {
	telemetry ModelTelemetry
	phase     string

	mutex     sync.Mutex
	startedAt time.Time
	endedAt   time.Time
	span      platformobservability.Span
	started   bool
	finish    sync.Once
}

func newEinoChatCallbackCall(telemetry ModelTelemetry, phase agentdomain.ModelCallPhase) *einoChatCallbackCall {
	return &einoChatCallbackCall{
		telemetry: telemetry,
		phase:     platformobservability.RedactString("phase", string(phase)),
	}
}

func (call *einoChatCallbackCall) contextWithHandler(ctx context.Context) context.Context {
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			return call.onStart(ctx)
		}).
		OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
			call.onEnd()
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, _ *callbacks.RunInfo, _ error) context.Context {
			call.onError()
			return ctx
		}).
		Build()
	return callbacks.InitCallbacks(ctx, &callbacks.RunInfo{
		Name: einoChatCallbackName, Type: "OpenAI", Component: components.ComponentOfChatModel,
	}, handler)
}

func (call *einoChatCallbackCall) onStart(ctx context.Context) context.Context {
	if call == nil {
		return ctx
	}
	call.mutex.Lock()
	call.started = true
	call.startedAt = time.Now()
	call.mutex.Unlock()

	traced, span := safeStartModelSpan(ctx, call.telemetry.tracer,
		platformobservability.TraceAttribute{Key: "component", Value: einoChatTelemetryComponent},
		platformobservability.TraceAttribute{Key: "phase", Value: call.phase},
	)
	call.mutex.Lock()
	call.span = span
	call.mutex.Unlock()
	return traced
}

func (call *einoChatCallbackCall) onEnd() {
	call.markCallbackEnded()
}

func (call *einoChatCallbackCall) onError() {
	call.markCallbackEnded()
}

func (call *einoChatCallbackCall) markCallbackEnded() {
	if call == nil {
		return
	}
	call.mutex.Lock()
	if call.endedAt.IsZero() {
		call.endedAt = time.Now()
	}
	call.mutex.Unlock()
}

func (call *einoChatCallbackCall) finishSuccess(ctx context.Context) {
	call.finishWithResult(ctx, "success", "")
}

func (call *einoChatCallbackCall) finishError(ctx context.Context, err error) {
	code := stableModelTelemetryErrorCode(err)
	result := "failure"
	if code == ErrorCodeChatCancelled {
		result = "cancelled"
	}
	call.finishWithResult(ctx, result, code)
}

func (call *einoChatCallbackCall) finishWithResult(ctx context.Context, result, errorCode string) {
	if call == nil {
		return
	}
	call.finish.Do(func() {
		call.mutex.Lock()
		started := call.started
		startedAt := call.startedAt
		endedAt := call.endedAt
		span := call.span
		call.mutex.Unlock()
		if !started {
			return
		}

		result = platformobservability.RedactString("status", result)
		errorCode = sanitizeModelTelemetryErrorCode(errorCode)
		attributes := []platformobservability.TraceAttribute{{Key: "status", Value: result}}
		if errorCode != "" {
			attributes = append(attributes, platformobservability.TraceAttribute{Key: "error_code", Value: errorCode})
		}
		safeFinishModelSpan(span, attributes, errorCode)

		labels := map[string]string{
			"component": einoChatTelemetryComponent,
			"phase":     call.phase,
			"result":    result,
		}
		if errorCode != "" {
			labels["error_code"] = errorCode
		}
		duration := time.Since(startedAt)
		if !endedAt.IsZero() && !endedAt.Before(startedAt) {
			duration = endedAt.Sub(startedAt)
		}
		durationMilliseconds := float64(duration) / float64(time.Millisecond)
		safeRecordModelMetric(ctx, call.telemetry.metrics, platformobservability.MetricModelCallDuration, platformobservability.MetricKindHistogram, durationMilliseconds, labels)
		safeRecordModelMetric(ctx, call.telemetry.metrics, platformobservability.MetricModelCallTotal, platformobservability.MetricKindCounter, 1, labels)
	})
}

func stableModelTelemetryErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		if code := sanitizeModelTelemetryErrorCode(classified.Code); code != "" {
			return code
		}
	}
	return ErrorCodeChatRequestFailed
}

func sanitizeModelTelemetryErrorCode(code string) string {
	code = platformobservability.RedactString("error_code", code)
	if !telemetryErrorCodePattern.MatchString(code) {
		return ""
	}
	return code
}

func safeStartModelSpan(ctx context.Context, tracer platformobservability.Tracer, attrs ...platformobservability.TraceAttribute) (next context.Context, span platformobservability.Span) {
	next = ctx
	defer func() {
		if recover() != nil {
			next = ctx
			span = nil
		}
	}()
	if tracer == nil {
		return next, nil
	}
	started, candidate, err := tracer.Start(ctx, einoChatTraceOperation, attrs...)
	if err != nil || started == nil || candidate == nil {
		return next, nil
	}
	return started, candidate
}

func safeFinishModelSpan(span platformobservability.Span, attrs []platformobservability.TraceAttribute, errorCode string) {
	if span == nil {
		return
	}
	safeModelSpanCall(func() { span.SetAttributes(attrs...) })
	if errorCode != "" {
		safeModelSpanCall(func() { span.RecordError(errorCode) })
	}
	safeModelSpanCall(span.End)
}

func safeModelSpanCall(call func()) {
	defer func() { _ = recover() }()
	call()
}

func safeRecordModelMetric(ctx context.Context, metrics platformobservability.Metrics, name platformobservability.MetricName, kind platformobservability.MetricKind, value float64, rawLabels map[string]string) {
	defer func() { _ = recover() }()
	if metrics == nil {
		return
	}
	labels, err := platformobservability.NewLabels(rawLabels)
	if err != nil {
		return
	}
	measurement, err := platformobservability.NewMeasurement(name, kind, value, labels)
	if err != nil {
		return
	}
	_ = metrics.Record(ctx, measurement)
}
