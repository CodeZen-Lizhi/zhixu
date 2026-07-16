package tooltrace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/poc/eino/contract"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestRunnerAllowsAuthorizedValidToolCall(t *testing.T) {
	recorder := &Recorder{}
	runner := newSearchRunner(t, recorder, Limit{}, nil)
	ctx := WithPermissions(context.Background(), PermissionReadLocal)

	result, err := runner.Run(ctx, Request{
		CallID:    "tool-call-1",
		ToolName:  "search_knowledge",
		Arguments: `{"query":"Eino"}`,
	})
	if err != nil {
		t.Fatalf("run authorized tool: %v", err)
	}
	if result.CallID != "tool-call-1" {
		t.Fatalf("call id = %q, want %q", result.CallID, "tool-call-1")
	}
	if result.Content != "found:Eino" {
		t.Fatalf("content = %q, want %q", result.Content, "found:Eino")
	}
	if recorder.Calls() != 1 {
		t.Fatalf("handler calls = %d, want 1", recorder.Calls())
	}
}

func TestRunnerRejectsPermissionBeforeRealExecution(t *testing.T) {
	recorder := &Recorder{}
	runner := newSearchRunner(t, recorder, Limit{}, nil)

	_, err := runner.Run(context.Background(), Request{
		CallID:    "tool-call-denied",
		ToolName:  "search_knowledge",
		Arguments: `{"query":"private"}`,
	})
	assertErrorKind(t, err, contract.ErrorPermissionDenied, false)
	if recorder.Calls() != 0 {
		t.Fatalf("handler calls = %d, want 0 after permission denial", recorder.Calls())
	}
}

func TestRunnerRejectsUnknownToolBeforeRealExecution(t *testing.T) {
	recorder := &Recorder{}
	runner := newSearchRunner(t, recorder, Limit{}, nil)
	ctx := WithPermissions(context.Background(), PermissionReadLocal)

	_, err := runner.Run(ctx, Request{
		CallID:    "tool-call-unknown",
		ToolName:  "delete_everything",
		Arguments: `{}`,
	})
	assertErrorKind(t, err, contract.ErrorNotFound, false)
	if recorder.Calls() != 0 {
		t.Fatalf("handler calls = %d, want 0 after unknown tool", recorder.Calls())
	}
}

func TestRunnerRejectsInvalidArgumentsBeforeRealExecution(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
	}{
		{name: "malformed JSON", arguments: `{"query":`},
		{name: "missing required", arguments: `{}`},
		{name: "wrong type", arguments: `{"query":42}`},
		{name: "null object", arguments: `null`},
		{name: "unknown field", arguments: `{"query":"ok","secret":"no"}`},
		{name: "multiple values", arguments: `{"query":"ok"} {}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &Recorder{}
			runner := newSearchRunner(t, recorder, Limit{}, nil)
			ctx := WithPermissions(context.Background(), PermissionReadLocal)

			_, err := runner.Run(ctx, Request{
				CallID:    "tool-call-invalid",
				ToolName:  "search_knowledge",
				Arguments: tt.arguments,
			})
			assertErrorKind(t, err, contract.ErrorInvalidInput, false)
			if recorder.Calls() != 0 {
				t.Fatalf("handler calls = %d, want 0 after invalid arguments", recorder.Calls())
			}
		})
	}
}

func TestRunnerSnapshotsDefinitionParameters(t *testing.T) {
	parameters := map[string]Parameter{
		"query": {Type: string(schema.String), Required: true},
	}
	runner, err := NewRunner(context.Background(), []Definition{{
		Name:       "snapshot_tool",
		Permission: PermissionReadLocal,
		Parameters: parameters,
		Handler: func(_ context.Context, arguments map[string]any) (string, error) {
			return arguments["query"].(string), nil
		},
	}}, NewLimiter(nil), nil, nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	delete(parameters, "query")
	parameters["replacement"] = Parameter{Type: string(schema.Integer), Required: true}
	ctx := WithPermissions(context.Background(), PermissionReadLocal)
	result, runErr := runner.Run(ctx, Request{
		CallID:    "snapshot-call",
		ToolName:  "snapshot_tool",
		Arguments: `{"query":"stable"}`,
	})
	if runErr != nil {
		t.Fatalf("run with snapshotted definition: %v", runErr)
	}
	if result.Content != "stable" {
		t.Fatalf("content = %q, want %q", result.Content, "stable")
	}
}

func TestTraceCorrelatesProjectIDsAndRedactsSecrets(t *testing.T) {
	const rawSecret = "sk-poisoned-secret-value"
	sink := &recordingSink{}
	runner, err := NewRunner(context.Background(), []Definition{{
		Name:       "failing_tool",
		Permission: PermissionReadExternal,
		Parameters: map[string]Parameter{
			"url": {Type: string(schema.String), Required: true},
		},
		Handler: func(context.Context, map[string]any) (string, error) {
			return "", fmt.Errorf("provider failed api_key=%s Authorization: Bearer %s secret=%s", rawSecret, rawSecret, rawSecret)
		},
	}}, NewLimiter(nil), sink, NewRedactor(rawSecret))
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	trace := TraceContext{
		RequestID:     "request-42",
		WorkflowRunID: "workflow-7",
		NodeRunID:     "node-3",
	}
	ctx := WithTrace(context.Background(), trace)
	ctx = WithPermissions(ctx, PermissionReadExternal)
	_, runErr := runner.Run(ctx, Request{
		CallID:    "tool-call-trace",
		ToolName:  "failing_tool",
		Arguments: `{"url":"https://example.test"}`,
	})
	assertErrorKind(t, runErr, contract.ErrorNonRetryableFailure, false)

	events := sink.Events()
	if len(events) == 0 {
		t.Fatal("no callback trace events recorded")
	}
	var foundError bool
	for _, event := range events {
		if event.RequestID != trace.RequestID || event.WorkflowRunID != trace.WorkflowRunID || event.NodeRunID != trace.NodeRunID {
			t.Fatalf("trace correlation = %#v, want %#v", event, trace)
		}
		if strings.Contains(fmt.Sprintf("%+v", event), rawSecret) {
			t.Fatalf("trace event leaks raw secret: %#v", event)
		}
		if event.Phase == TraceError {
			foundError = true
			if event.ErrorKind != contract.ErrorNonRetryableFailure {
				t.Fatalf("trace error kind = %q, want %q", event.ErrorKind, contract.ErrorNonRetryableFailure)
			}
		}
	}
	if !foundError {
		t.Fatalf("trace phases = %v, want an error callback", tracePhases(events))
	}
}

func TestRedactorRemovesAuthorizationSchemesAndCookies(t *testing.T) {
	redactor := NewRedactor()
	input := "Authorization: Basic c2VjcmV0, Proxy-Authorization=Digest abc123; Cookie: session=private; Set-Cookie: refresh=private"
	redacted := redactor.Redact(input)
	for _, leaked := range []string{"c2VjcmV0", "abc123", "session=private", "refresh=private"} {
		if strings.Contains(redacted, leaked) {
			t.Fatalf("redacted text leaks %q: %s", leaked, redacted)
		}
	}
}

func TestWriteProposalUnknownFailureRequiresManualRecovery(t *testing.T) {
	cause := errors.New("write result is unknown")
	runner, err := NewRunner(context.Background(), []Definition{{
		Name:       "write_proposal",
		Permission: PermissionWriteProposal,
		Handler: func(context.Context, map[string]any) (string, error) {
			return "", cause
		},
	}}, NewLimiter(nil), nil, nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	ctx := WithPermissions(context.Background(), PermissionWriteProposal)
	_, runErr := runner.Run(ctx, Request{CallID: "write-call", ToolName: "write_proposal", Arguments: `{}`})
	assertErrorKind(t, runErr, contract.ErrorManualRecovery, false)
	if !errors.Is(runErr, cause) {
		t.Fatalf("error = %v, want wrapped cause", runErr)
	}
}

func TestLimiterIsRepeatableWithoutSleeping(t *testing.T) {
	clock := &manualClock{now: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)}
	recorder := &Recorder{}
	limit := Limit{Requests: 2, Window: time.Minute}
	runner := newSearchRunner(t, recorder, limit, NewLimiter(clock))
	ctx := WithTrace(context.Background(), TraceContext{RequestID: "same-caller"})
	ctx = WithPermissions(ctx, PermissionReadLocal)
	request := Request{CallID: "rate-call", ToolName: "search_knowledge", Arguments: `{"query":"quota"}`}

	for invocation := 1; invocation <= 2; invocation++ {
		if _, err := runner.Run(ctx, request); err != nil {
			t.Fatalf("allowed invocation %d: %v", invocation, err)
		}
	}
	_, err := runner.Run(ctx, request)
	assertErrorKind(t, err, contract.ErrorRetryableFailure, true)
	if recorder.Calls() != 2 {
		t.Fatalf("handler calls = %d, want 2 before window reset", recorder.Calls())
	}

	clock.Advance(time.Minute)
	if _, err := runner.Run(ctx, request); err != nil {
		t.Fatalf("invoke after deterministic window reset: %v", err)
	}
	if recorder.Calls() != 3 {
		t.Fatalf("handler calls = %d, want 3 after window reset", recorder.Calls())
	}
}

func TestRateLimitedToolRequiresCorrelationID(t *testing.T) {
	recorder := &Recorder{}
	runner := newSearchRunner(t, recorder, Limit{Requests: 1, Window: time.Minute}, NewLimiter(nil))
	ctx := WithPermissions(context.Background(), PermissionReadLocal)
	_, err := runner.Run(ctx, Request{CallID: "rate-no-trace", ToolName: "search_knowledge", Arguments: `{"query":"quota"}`})
	assertErrorKind(t, err, contract.ErrorInvalidInput, false)
	if recorder.Calls() != 0 {
		t.Fatalf("handler calls = %d, want 0", recorder.Calls())
	}
}

func TestRunnerClassifiesTimeoutAndCancellation(t *testing.T) {
	tests := []struct {
		name      string
		failure   error
		kind      contract.ErrorKind
		retryable bool
	}{
		{name: "deadline", failure: context.DeadlineExceeded, kind: contract.ErrorRetryableFailure, retryable: true},
		{name: "caller cancellation", failure: context.Canceled, kind: contract.ErrorNonRetryableFailure, retryable: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := NewRunner(context.Background(), []Definition{{
				Name:       "timed_tool",
				Permission: PermissionReadLocal,
				Handler: func(context.Context, map[string]any) (string, error) {
					return "", tt.failure
				},
			}}, NewLimiter(nil), nil, nil)
			if err != nil {
				t.Fatalf("create runner: %v", err)
			}
			ctx := WithPermissions(context.Background(), PermissionReadLocal)
			_, runErr := runner.Run(ctx, Request{CallID: "time-call", ToolName: "timed_tool", Arguments: `{}`})
			assertErrorKind(t, runErr, tt.kind, tt.retryable)
			if !errors.Is(runErr, tt.failure) {
				t.Fatalf("error = %v, want wrapped %v", runErr, tt.failure)
			}
		})
	}
}

func TestRunnerEnforcesConfiguredTimeout(t *testing.T) {
	started := make(chan struct{})
	runner, err := NewRunner(context.Background(), []Definition{{
		Name:       "slow_tool",
		Permission: PermissionReadExternal,
		Timeout:    10 * time.Millisecond,
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		},
	}}, NewLimiter(nil), nil, nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		ctx := WithPermissions(context.Background(), PermissionReadExternal)
		_, runErr := runner.Run(ctx, Request{CallID: "slow-call", ToolName: "slow_tool", Arguments: `{}`})
		result <- runErr
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("slow tool handler did not start")
	}
	select {
	case runErr := <-result:
		assertErrorKind(t, runErr, contract.ErrorRetryableFailure, true)
		if !errors.Is(runErr, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context deadline exceeded", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("configured tool timeout did not stop execution")
	}
}

func TestRedactorRemovesUnconfiguredLabeledCredentials(t *testing.T) {
	redacted := NewRedactor().Redact("Authorization: Bearer abc123 api_key=def456 secret:ghi789")
	for _, credential := range []string{"abc123", "def456", "ghi789"} {
		if strings.Contains(redacted, credential) {
			t.Fatalf("redacted text %q contains credential %q", redacted, credential)
		}
	}
}

func TestWithToolsCreatesIndependentPerRequestModels(t *testing.T) {
	base := &bindingModel{}
	definitions := []Definition{{
		Name:       "search_knowledge",
		Permission: PermissionReadLocal,
		Parameters: map[string]Parameter{"query": {Type: string(schema.String), Required: true}},
		Handler:    func(context.Context, map[string]any) (string, error) { return "", nil },
	}}

	const workers = 32
	results := make(chan *bindingModel, workers)
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			bound, err := bindToolSchemas(base, definitions)
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- bound.(*bindingModel)
		}()
	}
	wait.Wait()
	close(results)
	close(errorsChannel)

	for err := range errorsChannel {
		t.Fatalf("bind tools: %v", err)
	}
	if len(base.tools) != 0 {
		t.Fatalf("base model mutated with %d tools", len(base.tools))
	}
	count := 0
	for bound := range results {
		count++
		if len(bound.tools) != 1 || bound.tools[0].Name != "search_knowledge" {
			t.Fatalf("bound tools = %#v, want independent search_knowledge schema", bound.tools)
		}
	}
	if count != workers {
		t.Fatalf("bound models = %d, want %d", count, workers)
	}
}

func newSearchRunner(t *testing.T, recorder *Recorder, limit Limit, limiter *Limiter) *Runner {
	t.Helper()
	if limiter == nil {
		limiter = NewLimiter(nil)
	}
	runner, err := NewRunner(context.Background(), []Definition{{
		Name:        "search_knowledge",
		Description: "Search trusted project knowledge.",
		Permission:  PermissionReadLocal,
		Parameters: map[string]Parameter{
			"query": {Type: string(schema.String), Description: "Search query", Required: true},
		},
		RateLimit: limit,
		Handler: func(_ context.Context, arguments map[string]any) (string, error) {
			recorder.Record()
			return "found:" + arguments["query"].(string), nil
		},
	}}, limiter, nil, nil)
	if err != nil {
		t.Fatalf("create search runner: %v", err)
	}
	return runner
}

func assertErrorKind(t *testing.T, err error, kind contract.ErrorKind, retryable bool) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want kind %q", kind)
	}
	var classified *contract.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error type = %T, want *contract.Error: %v", err, err)
	}
	if classified.Kind != kind {
		t.Fatalf("error kind = %q, want %q: %v", classified.Kind, kind, err)
	}
	if classified.Retryable != retryable {
		t.Fatalf("retryable = %t, want %t", classified.Retryable, retryable)
	}
}

type recordingSink struct {
	mu     sync.Mutex
	events []TraceEvent
}

type Recorder struct {
	mu    sync.Mutex
	calls int
}

func (r *Recorder) Record() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
}

func (r *Recorder) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (s *recordingSink) Record(_ context.Context, event TraceEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *recordingSink) Events() []TraceEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TraceEvent(nil), s.events...)
}

func tracePhases(events []TraceEvent) []TracePhase {
	phases := make([]TracePhase, 0, len(events))
	for _, event := range events {
		phases = append(phases, event.Phase)
	}
	return phases
}

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

type bindingModel struct {
	tools []*schema.ToolInfo
}

var _ model.ToolCallingChatModel = (*bindingModel)(nil)

func (m *bindingModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("unused", nil), nil
}

func (m *bindingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
}

func (m *bindingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &bindingModel{tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}
