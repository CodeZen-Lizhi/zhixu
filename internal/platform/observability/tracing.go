package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

const TraceParentMetadataKey = "traceparent"

var (
	ErrInvalidTraceContext = errors.New("invalid trace context")
	traceParentPattern     = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)
	traceAttributePattern  = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
)

type traceContextKey struct{}

// TraceContext is the project-owned subset of W3C trace context persisted
// across asynchronous boundaries. Baggage and arbitrary metadata are excluded.
type TraceContext struct {
	TraceID    string
	SpanID     string
	TraceFlags string
}

// TraceParent returns a validated W3C traceparent value.
func (trace TraceContext) TraceParent() (string, error) {
	trace.TraceID = strings.ToLower(strings.TrimSpace(trace.TraceID))
	trace.SpanID = strings.ToLower(strings.TrimSpace(trace.SpanID))
	trace.TraceFlags = strings.ToLower(strings.TrimSpace(trace.TraceFlags))
	value := fmt.Sprintf("00-%s-%s-%s", trace.TraceID, trace.SpanID, trace.TraceFlags)
	if _, err := parseTraceParent(value); err != nil {
		return "", err
	}
	return value, nil
}

// WithTraceContext validates and attaches trace context to ctx.
func WithTraceContext(ctx context.Context, trace TraceContext) (context.Context, error) {
	if _, err := trace.TraceParent(); err != nil {
		return ctx, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	trace.TraceID = strings.ToLower(strings.TrimSpace(trace.TraceID))
	trace.SpanID = strings.ToLower(strings.TrimSpace(trace.SpanID))
	trace.TraceFlags = strings.ToLower(strings.TrimSpace(trace.TraceFlags))
	ctx = context.WithValue(ctx, traceContextKey{}, trace)
	ctx = WithCorrelation(ctx, Correlation{TraceID: trace.TraceID})
	return ctx, nil
}

// TraceContextFromContext returns a valid trace context when one is present.
func TraceContextFromContext(ctx context.Context) (TraceContext, bool) {
	if ctx == nil {
		return TraceContext{}, false
	}
	trace, found := ctx.Value(traceContextKey{}).(TraceContext)
	if !found {
		return TraceContext{}, false
	}
	if _, err := trace.TraceParent(); err != nil {
		return TraceContext{}, false
	}
	return trace, true
}

// EncodeTraceMetadata returns the only River metadata field owned by this
// package. It never serializes baggage, credentials, content or paths.
func EncodeTraceMetadata(ctx context.Context) map[string]string {
	trace, found := TraceContextFromContext(ctx)
	if !found {
		return nil
	}
	traceParent, err := trace.TraceParent()
	if err != nil {
		return nil
	}
	return map[string]string{TraceParentMetadataKey: traceParent}
}

// DecodeTraceMetadata accepts only the fixed traceparent field, rejecting
// unknown metadata so an asynchronous job cannot smuggle arbitrary payloads.
func DecodeTraceMetadata(ctx context.Context, metadata map[string]string) (context.Context, error) {
	if len(metadata) == 0 {
		return ctx, nil
	}
	if len(metadata) != 1 {
		return ctx, ErrInvalidTraceContext
	}
	value, found := metadata[TraceParentMetadataKey]
	if !found {
		return ctx, ErrInvalidTraceContext
	}
	trace, err := parseTraceParent(value)
	if err != nil {
		return ctx, err
	}
	return WithTraceContext(ctx, trace)
}

func parseTraceParent(value string) (TraceContext, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	matches := traceParentPattern.FindStringSubmatch(value)
	if len(matches) != 4 || allZero(matches[1]) || allZero(matches[2]) {
		return TraceContext{}, ErrInvalidTraceContext
	}
	return TraceContext{TraceID: matches[1], SpanID: matches[2], TraceFlags: matches[3]}, nil
}

func allZero(value string) bool {
	return strings.Trim(value, "0") == ""
}

// TraceAttribute is a sanitized span attribute. High-cardinality correlation
// IDs are allowed in traces but are never promoted to metric labels.
type TraceAttribute struct {
	Key   string
	Value string
}

// Span is the project-owned trace span contract.
type Span interface {
	SetAttributes(...TraceAttribute)
	RecordError(string)
	End()
}

// Tracer starts spans without exposing a concrete telemetry SDK.
type Tracer interface {
	Start(context.Context, string, ...TraceAttribute) (context.Context, Span, error)
}

type noopTracer struct{}
type noopSpan struct{}

// NewNoopTracer returns a tracer that exports nothing. Incoming trace context
// remains available for propagation even while export is disabled.
func NewNoopTracer() Tracer {
	return noopTracer{}
}

func (noopTracer) Start(ctx context.Context, operation string, attrs ...TraceAttribute) (context.Context, Span, error) {
	if err := validateTraceInput(operation, attrs); err != nil {
		return ctx, nil, err
	}
	child, err := newChildTraceContext(ctx)
	if err != nil {
		return ctx, nil, err
	}
	return child, noopSpan{}, nil
}

func (noopSpan) SetAttributes(...TraceAttribute) {}
func (noopSpan) RecordError(string)              {}
func (noopSpan) End()                            {}

// SpanSnapshot is an immutable completed in-memory span.
type SpanSnapshot struct {
	Operation    string
	TraceContext TraceContext
	ParentSpanID string
	Attributes   map[string]string
	ErrorCode    string
	StartedAt    time.Time
	EndedAt      time.Time
}

// MemoryTracer is a concurrency-safe test and local diagnostic adapter.
type MemoryTracer struct {
	mutex     sync.RWMutex
	active    map[string]*memorySpan
	completed []SpanSnapshot
	closed    bool
	now       func() time.Time
}

type memorySpan struct {
	tracer       *MemoryTracer
	operation    string
	traceContext TraceContext
	parentSpanID string
	attributes   map[string]string
	errorCode    string
	startedAt    time.Time
	once         sync.Once
}

// NewMemoryTracer constructs an in-memory tracer.
func NewMemoryTracer() *MemoryTracer {
	return &MemoryTracer{active: make(map[string]*memorySpan), now: time.Now}
}

// Start creates a child span when ctx has a parent, otherwise a new trace.
func (tracer *MemoryTracer) Start(ctx context.Context, operation string, attrs ...TraceAttribute) (context.Context, Span, error) {
	if err := validateTraceInput(operation, attrs); err != nil {
		return ctx, nil, err
	}
	tracer.mutex.Lock()
	defer tracer.mutex.Unlock()
	if tracer.closed {
		return ctx, nil, ErrObservabilityClosed
	}
	parent, hasParent := TraceContextFromContext(ctx)
	child, err := newChildTraceContext(ctx)
	if err != nil {
		return ctx, nil, err
	}
	trace, _ := TraceContextFromContext(child)
	parentSpanID := ""
	if hasParent {
		parentSpanID = parent.SpanID
	}
	span := &memorySpan{
		tracer:       tracer,
		operation:    operation,
		traceContext: trace,
		parentSpanID: parentSpanID,
		attributes:   make(map[string]string, len(attrs)+12),
		startedAt:    tracer.now(),
	}
	span.setAttributesLocked(attrs...)
	for _, attr := range correlationTraceAttributes(CorrelationFromContext(ctx)) {
		span.setAttributesLocked(attr)
	}
	spanID := trace.SpanID
	tracer.active[spanID] = span
	return child, span, nil
}

func newChildTraceContext(ctx context.Context) (context.Context, error) {
	parent, hasParent := TraceContextFromContext(ctx)
	traceID := parent.TraceID
	traceFlags := parent.TraceFlags
	if !hasParent {
		var err error
		traceID, err = randomHex(16)
		if err != nil {
			return ctx, ErrInvalidTraceContext
		}
		traceFlags = "01"
	}
	spanID, err := randomHex(8)
	if err != nil {
		return ctx, ErrInvalidTraceContext
	}
	return WithTraceContext(ctx, TraceContext{TraceID: traceID, SpanID: spanID, TraceFlags: traceFlags})
}

func validateTraceInput(operation string, attrs []TraceAttribute) error {
	operation = strings.TrimSpace(operation)
	if operation == "" || len(operation) > 128 || redactString("operation", operation) == RedactedValue {
		return ErrInvalidTraceContext
	}
	for _, attr := range attrs {
		if !traceAttributePattern.MatchString(attr.Key) {
			return ErrInvalidTraceContext
		}
	}
	return nil
}

func correlationTraceAttributes(correlation Correlation) []TraceAttribute {
	attrs := make([]TraceAttribute, 0, 12)
	appendString := func(key, value string) {
		if value != "" {
			attrs = append(attrs, TraceAttribute{Key: key, Value: value})
		}
	}
	appendString("request_id", correlation.RequestID)
	appendString("workspace_id", correlation.WorkspaceID)
	appendString("workflow_run_id", correlation.WorkflowRunID)
	appendString("node_run_id", correlation.NodeRunID)
	appendString("proposal_id", correlation.ProposalID)
	appendString("tool_call_id", correlation.ToolCallID)
	appendString("document_id", correlation.DocumentID)
	if correlation.AttemptNo > 0 {
		appendString("attempt_no", fmt.Sprintf("%d", correlation.AttemptNo))
	}
	if correlation.DispatchNo > 0 {
		appendString("dispatch_no", fmt.Sprintf("%d", correlation.DispatchNo))
	}
	if correlation.RetryNo > 0 || correlation.AttemptNo > 0 || correlation.DispatchNo > 0 {
		appendString("retry_no", fmt.Sprintf("%d", correlation.RetryNo))
	}
	if correlation.RiverJobID > 0 {
		appendString("river_job_id", fmt.Sprintf("%d", correlation.RiverJobID))
	}
	return attrs
}

func (span *memorySpan) SetAttributes(attrs ...TraceAttribute) {
	span.tracer.mutex.Lock()
	defer span.tracer.mutex.Unlock()
	if span.tracer.closed {
		return
	}
	span.setAttributesLocked(attrs...)
}

func (span *memorySpan) setAttributesLocked(attrs ...TraceAttribute) {
	for _, attr := range attrs {
		if !traceAttributePattern.MatchString(attr.Key) {
			continue
		}
		span.attributes[attr.Key] = redactString(attr.Key, attr.Value)
	}
}

func (span *memorySpan) RecordError(errorCode string) {
	span.tracer.mutex.Lock()
	defer span.tracer.mutex.Unlock()
	if span.tracer.closed {
		return
	}
	span.errorCode = redactString("error_code", strings.TrimSpace(errorCode))
}

func (span *memorySpan) End() {
	span.once.Do(func() {
		span.tracer.mutex.Lock()
		defer span.tracer.mutex.Unlock()
		if span.tracer.closed {
			return
		}
		delete(span.tracer.active, span.traceContext.SpanID)
		span.tracer.completed = append(span.tracer.completed, SpanSnapshot{
			Operation:    span.operation,
			TraceContext: span.traceContext,
			ParentSpanID: span.parentSpanID,
			Attributes:   cloneStringMap(span.attributes),
			ErrorCode:    span.errorCode,
			StartedAt:    span.startedAt,
			EndedAt:      span.tracer.now(),
		})
	})
}

// Snapshot returns completed spans in completion order.
func (tracer *MemoryTracer) Snapshot() []SpanSnapshot {
	tracer.mutex.RLock()
	defer tracer.mutex.RUnlock()
	result := make([]SpanSnapshot, len(tracer.completed))
	for index, snapshot := range tracer.completed {
		snapshot.Attributes = cloneStringMap(snapshot.Attributes)
		result[index] = snapshot
	}
	return result
}

func (tracer *MemoryTracer) close() error {
	tracer.mutex.Lock()
	defer tracer.mutex.Unlock()
	tracer.closed = true
	clear(tracer.active)
	return nil
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func randomHex(byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
