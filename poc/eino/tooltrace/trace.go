package tooltrace

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/poc/eino/contract"
	"github.com/cloudwego/eino/callbacks"
)

// TraceContext contains the project correlation identifiers propagated across Eino callbacks.
type TraceContext struct {
	RequestID     string
	WorkflowRunID string
	NodeRunID     string
}

type traceContextKey struct{}

// WithTrace adds project correlation identifiers to a context.
func WithTrace(ctx context.Context, trace TraceContext) context.Context {
	return context.WithValue(ctx, traceContextKey{}, trace)
}

// TraceFromContext returns correlation identifiers without inventing framework IDs.
func TraceFromContext(ctx context.Context) TraceContext {
	trace, _ := ctx.Value(traceContextKey{}).(TraceContext)
	return trace
}

// TracePhase identifies one callback lifecycle phase.
type TracePhase string

const (
	TraceStart TracePhase = "start"
	TraceEnd   TracePhase = "end"
	TraceError TracePhase = "error"
)

// TraceEvent is the safe, project-owned callback record emitted by the PoC.
type TraceEvent struct {
	Phase         TracePhase
	RequestID     string
	WorkflowRunID string
	NodeRunID     string
	ComponentName string
	ComponentType string
	ErrorKind     contract.ErrorKind
	Error         string
}

// TraceSink receives sanitized callback records.
type TraceSink interface {
	Record(ctx context.Context, event TraceEvent)
}

// TraceSinkFunc adapts a function to TraceSink.
type TraceSinkFunc func(ctx context.Context, event TraceEvent)

// Record implements TraceSink.
func (f TraceSinkFunc) Record(ctx context.Context, event TraceEvent) {
	f(ctx, event)
}

// newTraceHandler creates an Eino callback handler while keeping framework types private.
func newTraceHandler(sink TraceSink, redactor *Redactor) callbacks.Handler {
	if sink == nil {
		return nil
	}
	if redactor == nil {
		redactor = NewRedactor()
	}

	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			sink.Record(ctx, newTraceEvent(ctx, info, TraceStart, nil, redactor))
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
			sink.Record(ctx, newTraceEvent(ctx, info, TraceEnd, nil, redactor))
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			sink.Record(ctx, newTraceEvent(ctx, info, TraceError, err, redactor))
			return ctx
		}).
		Build()
}

func newTraceEvent(ctx context.Context, info *callbacks.RunInfo, phase TracePhase, err error, redactor *Redactor) TraceEvent {
	trace := TraceFromContext(ctx)
	event := TraceEvent{
		Phase:         phase,
		RequestID:     trace.RequestID,
		WorkflowRunID: trace.WorkflowRunID,
		NodeRunID:     trace.NodeRunID,
	}
	if info != nil {
		event.ComponentName = redactor.Redact(info.Name)
		event.ComponentType = redactor.Redact(info.Type)
	}
	if err != nil {
		event.Error = redactor.Redact(err.Error())
		var classified *contract.Error
		if errors.As(err, &classified) {
			event.ErrorKind = classified.Kind
		} else {
			event.ErrorKind = contract.ErrorDependencyUnavailable
		}
	}
	return event
}

var labeledSecret = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|api[-_]?key|token|secret)\b\s*[:=]\s*[^,;\r\n]*`)

// Redactor removes configured raw secrets and common labeled credentials from trace text.
type Redactor struct {
	secrets []string
}

// NewRedactor creates an immutable redactor. Longer secrets are removed first.
func NewRedactor(secrets ...string) *Redactor {
	filtered := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			filtered = append(filtered, secret)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return len(filtered[i]) > len(filtered[j]) })
	return &Redactor{secrets: filtered}
}

// Redact returns text safe for ordinary logs and traces.
func (r *Redactor) Redact(value string) string {
	redacted := value
	if r != nil {
		for _, secret := range r.secrets {
			redacted = strings.ReplaceAll(redacted, secret, "[REDACTED]")
		}
	}
	return labeledSecret.ReplaceAllString(redacted, "$1=[REDACTED]")
}
