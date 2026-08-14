package eino

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const runtimeStreamTestTimeout = 2 * time.Second

func TestRecordingRuntimeModelStreamPersistsMultiFrameEOFTerminal(t *testing.T) {
	frames := runtimeStreamSuccessFrames()
	source, control := newRuntimeLifecycleSource(frames...)
	repository := newRuntimeLifecycleRepository()
	model, ledger := newRuntimeLifecycleModel(t, &scriptedRuntimeModel{stream: func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
		return source, nil
	}}, repository)

	reader, err := model.Stream(context.Background(), runtimeLifecycleInput())
	if err != nil {
		t.Fatal(err)
	}
	got := drainRuntimeLifecycleReader(t, reader)
	reader.Close()
	wantFrames := runtimeLifecycleMessages(frames)
	if len(got) != len(wantFrames) {
		t.Fatalf("frames=%d want=%d", len(got), len(wantFrames))
	}
	for index := range wantFrames {
		if got[index] != wantFrames[index] {
			t.Fatalf("frame[%d]=%+v want=%+v", index, got[index], wantFrames[index])
		}
	}
	assertRuntimeSourceClosedAfterDrain(t, control)

	calls, attempts := repository.snapshot()
	if attempts != 1 || len(calls) != 1 {
		t.Fatalf("complete attempts=%d calls=%+v", attempts, calls)
	}
	call := calls[0]
	wantUsage := agentdomain.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
	wantResponse := runtimeLifecycleCanonicalResponse(t, frames)
	if call.Status != agentdomain.ModelCallSucceeded || call.Phase != agentdomain.ModelCallAnswer ||
		call.Usage != wantUsage || call.ResponseHash != runtimeLifecycleHash(wantResponse) ||
		call.ResponseBytes != int64(len(wantResponse)) || call.ErrorCode != "" {
		t.Fatalf("terminal call=%+v", call)
	}
	stateCalls, stateUsage, usageKnown := model.state.snapshot()
	if stateCalls != 1 || stateUsage != wantUsage || !usageKnown {
		t.Fatalf("runtime state calls=%d usage=%+v known=%t", stateCalls, stateUsage, usageKnown)
	}
	snapshot := ledger.Snapshot()
	if snapshot.ModelCalls != 1 || snapshot.InputTokens != wantUsage.InputTokens ||
		snapshot.OutputTokens != wantUsage.OutputTokens || snapshot.InFlightInput != 0 || snapshot.InFlightOutput != 0 {
		t.Fatalf("ledger=%+v", snapshot)
	}
}

func TestRecordingRuntimeModelStreamProviderFailures(t *testing.T) {
	providerErr := errors.New("provider stream failed with secret-canary")
	t.Run("open", func(t *testing.T) {
		repository := newRuntimeLifecycleRepository()
		model, ledger := newRuntimeLifecycleModel(t, &scriptedRuntimeModel{stream: func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, providerErr
		}}, repository)

		reader, err := model.Stream(context.Background(), runtimeLifecycleInput())
		if reader != nil {
			reader.Close()
			t.Fatal("provider open error returned a reader")
		}
		assertRuntimeInvocationFailure(t, err, providerErr, agentRuntimeInvokeCode, foundation.ErrorNonRetryableFailure)
		assertRuntimeFailedCall(t, repository, agentRuntimeInvokeCode)
		assertRuntimeLedgerSettled(t, ledger, 10, 10)
	})

	t.Run("recv", func(t *testing.T) {
		source, control := newRuntimeLifecycleSource(
			runtimeLifecycleFrame{message: &schema.Message{Role: schema.Assistant, Content: "partial"}},
			runtimeLifecycleFrame{err: providerErr},
		)
		repository := newRuntimeLifecycleRepository()
		model, ledger := newRuntimeLifecycleModel(t, &scriptedRuntimeModel{stream: func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
			return source, nil
		}}, repository)

		reader, err := model.Stream(context.Background(), runtimeLifecycleInput())
		if err != nil {
			t.Fatal(err)
		}
		message, err := recvRuntimeLifecycle(t, reader)
		if err != nil || message == nil || message.Content != "partial" {
			t.Fatalf("first frame=%+v err=%v", message, err)
		}
		_, err = recvRuntimeLifecycle(t, reader)
		assertRuntimeInvocationFailure(t, err, providerErr, agentRuntimeInvokeCode, foundation.ErrorNonRetryableFailure)
		assertRuntimeLifecycleEOF(t, reader)
		reader.Close()
		assertRuntimeSourceClosedAfterDrain(t, control)
		assertRuntimeFailedCall(t, repository, agentRuntimeInvokeCode)
		assertRuntimeLedgerSettled(t, ledger, 10, 10)
	})
}

func TestRuntimeStreamAccumulatorBoundsFramesAndRetainedFields(t *testing.T) {
	type namedRuntimeString string

	accumulator := runtimeStreamAccumulator{frames: maxRuntimeStreamFrames}
	if err := accumulator.add(&schema.Message{Role: schema.Assistant, Content: "x"}); errorCode(err) != agentRuntimeOutputCode {
		t.Fatalf("frame bound error=%v code=%q", err, errorCode(err))
	}

	accumulator = runtimeStreamAccumulator{}
	if err := accumulator.add(&schema.Message{Role: schema.Assistant, ReasoningContent: strings.Repeat("r", agentapplication.MaxAgentResultBytes+1)}); errorCode(err) != agentRuntimeOutputCode {
		t.Fatalf("reasoning bound error=%v code=%q", err, errorCode(err))
	}

	accumulator = runtimeStreamAccumulator{}
	if err := accumulator.add(&schema.Message{
		Role:             schema.Assistant,
		Content:          strings.Repeat("c", agentapplication.MaxAgentResultBytes/2+1),
		ReasoningContent: strings.Repeat("r", agentapplication.MaxAgentResultBytes/2),
	}); errorCode(err) != agentRuntimeOutputCode {
		t.Fatalf("aggregate bound error=%v code=%q", err, errorCode(err))
	}

	accumulator = runtimeStreamAccumulator{}
	if err := accumulator.add(&schema.Message{
		Role: schema.Assistant, Content: "answer",
		Extra: map[string]any{"_eino_msg_id": namedRuntimeString("message-id")},
	}); err != nil {
		t.Fatalf("named string metadata error=%v", err)
	}

	accumulator = runtimeStreamAccumulator{}
	if err := accumulator.add(&schema.Message{Role: schema.Assistant, Extra: map[string]any{"reasoning-content": "different"}}); errorCode(err) != agentRuntimeOutputCode {
		t.Fatalf("metadata consistency error=%v code=%q", err, errorCode(err))
	}
}

func TestRecordingRuntimeModelGenerateMapsProviderFailure(t *testing.T) {
	providerErr := errors.New("provider generate failed with secret-canary")
	repository := newRuntimeLifecycleRepository()
	model, ledger := newRuntimeLifecycleModel(t, &scriptedRuntimeModel{generate: []func([]*schema.Message, ...einomodel.Option) (*schema.Message, error){
		func([]*schema.Message, ...einomodel.Option) (*schema.Message, error) {
			return nil, providerErr
		},
	}}, repository)

	message, err := model.Generate(context.Background(), runtimeLifecycleInput())
	if message != nil {
		t.Fatalf("provider failure returned message=%+v", message)
	}
	assertRuntimeInvocationFailure(t, err, providerErr, agentRuntimeInvokeCode, foundation.ErrorNonRetryableFailure)
	assertRuntimeFailedCall(t, repository, agentRuntimeInvokeCode)
	assertRuntimeLedgerSettled(t, ledger, 10, 10)
}

func TestRecordingRuntimeModelStreamContextTermination(t *testing.T) {
	tests := []struct {
		name      string
		newCtx    func() (context.Context, context.CancelFunc)
		code      string
		kind      foundation.ErrorKind
		wantCause error
	}{
		{
			name: "cancel",
			newCtx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			code: agentapplication.ErrorCodeOperationCancelled, kind: foundation.ErrorNonRetryableFailure,
			wantCause: context.Canceled,
		},
		{
			name: "deadline",
			newCtx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			code: agentapplication.ErrorCodeOperationDeadline, kind: foundation.ErrorRetryableFailure,
			wantCause: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.newCtx()
			defer cancel()
			var control *runtimeLifecycleSourceControl
			backend := &contextRuntimeStreamModel{stream: func(streamCtx context.Context) *schema.StreamReader[*schema.Message] {
				var source *schema.StreamReader[*schema.Message]
				source, control = newRuntimeContextLifecycleSource(streamCtx)
				return source
			}}
			repository := newRuntimeLifecycleRepository()
			model, ledger := newRuntimeLifecycleModel(t, backend, repository)

			reader, err := model.Stream(ctx, runtimeLifecycleInput())
			if err != nil {
				t.Fatal(err)
			}
			if test.wantCause == context.Canceled {
				cancel()
			}
			_, err = recvRuntimeLifecycle(t, reader)
			if !errors.Is(err, test.wantCause) || errorCode(err) != test.code || errorKind(err) != test.kind {
				t.Fatalf("stream error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
			}
			assertRuntimeLifecycleEOF(t, reader)
			reader.Close()
			assertRuntimeSourceClosedAfterDrain(t, control)
			assertRuntimeFailedCall(t, repository, test.code)
			assertRuntimeLedgerSettled(t, ledger, 10, 10)
		})
	}
}

func TestRecordingRuntimeModelStreamDrainsAfterConsumerClose(t *testing.T) {
	frames := runtimeStreamSuccessFrames()
	source, control := newRuntimeLifecycleSource(frames...)
	repository := newRuntimeLifecycleRepository()
	model, ledger := newRuntimeLifecycleModel(t, &scriptedRuntimeModel{stream: func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
		return source, nil
	}}, repository)

	reader, err := model.Stream(context.Background(), runtimeLifecycleInput())
	if err != nil {
		t.Fatal(err)
	}
	first, err := recvRuntimeLifecycle(t, reader)
	if err != nil || first == nil || first.Content != "Final " {
		t.Fatalf("first frame=%+v err=%v", first, err)
	}
	reader.Close()

	terminal := repository.awaitTerminal(t)
	assertRuntimeSourceClosedAfterDrain(t, control)
	wantResponse := runtimeLifecycleCanonicalResponse(t, frames)
	wantUsage := agentdomain.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
	if terminal.Status != agentdomain.ModelCallSucceeded || terminal.Usage != wantUsage ||
		terminal.ResponseHash != runtimeLifecycleHash(wantResponse) || terminal.ResponseBytes != int64(len(wantResponse)) {
		t.Fatalf("terminal=%+v", terminal)
	}
	calls, attempts := repository.snapshot()
	if attempts != 1 || len(calls) != 1 || calls[0] != terminal {
		t.Fatalf("complete attempts=%d calls=%+v terminal=%+v", attempts, calls, terminal)
	}
	snapshot := ledger.Snapshot()
	if snapshot.ModelCalls != 1 || snapshot.InputTokens != wantUsage.InputTokens ||
		snapshot.OutputTokens != wantUsage.OutputTokens || snapshot.InFlightInput != 0 || snapshot.InFlightOutput != 0 {
		t.Fatalf("ledger=%+v", snapshot)
	}
}

func TestRecordingRuntimeModelStreamPersistsUnknownWhenTerminalWriteFails(t *testing.T) {
	frames := runtimeStreamSuccessFrames()
	source, control := newRuntimeLifecycleSource(frames...)
	repository := newRuntimeLifecycleRepository(errors.New("terminal write result unknown"))
	model, ledger := newRuntimeLifecycleModel(t, &scriptedRuntimeModel{stream: func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
		return source, nil
	}}, repository)

	reader, err := model.Stream(context.Background(), runtimeLifecycleInput())
	if err != nil {
		t.Fatal(err)
	}
	for range runtimeLifecycleMessages(frames) {
		if _, err = recvRuntimeLifecycle(t, reader); err != nil {
			t.Fatalf("frame error=%v", err)
		}
	}
	_, err = recvRuntimeLifecycle(t, reader)
	if errorCode(err) != agentapplication.ErrorCodeModelCallPersistenceUnknown || errorKind(err) != foundation.ErrorManualRecoveryRequired {
		t.Fatalf("terminal error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
	}
	assertRuntimeLifecycleEOF(t, reader)
	reader.Close()
	assertRuntimeSourceClosedAfterDrain(t, control)

	calls, attempts := repository.snapshot()
	if attempts != 2 || len(calls) != 1 {
		t.Fatalf("complete attempts=%d calls=%+v", attempts, calls)
	}
	call := calls[0]
	if call.Status != agentdomain.ModelCallUnknown || call.ErrorCode != agentapplication.ErrorCodeModelCallPersistenceUnknown ||
		call.ResponseHash != "" || call.ResponseBytes != 0 || call.Usage != (agentdomain.TokenUsage{}) {
		t.Fatalf("unknown terminal=%+v", call)
	}
	stateCalls, stateUsage, usageKnown := model.state.snapshot()
	if stateCalls != 0 || stateUsage != (agentdomain.TokenUsage{}) || usageKnown {
		t.Fatalf("runtime state calls=%d usage=%+v known=%t", stateCalls, stateUsage, usageKnown)
	}
	snapshot := ledger.Snapshot()
	if snapshot.ModelCalls != 1 || snapshot.InputTokens != 3 || snapshot.OutputTokens != 2 ||
		snapshot.InFlightInput != 0 || snapshot.InFlightOutput != 0 {
		t.Fatalf("ledger=%+v", snapshot)
	}
}

type runtimeLifecycleFrame struct {
	message *schema.Message
	err     error
}

type runtimeLifecycleSourceControl struct {
	probe  chan struct{}
	result chan bool
}

func newRuntimeLifecycleSource(frames ...runtimeLifecycleFrame) (*schema.StreamReader[*schema.Message], *runtimeLifecycleSourceControl) {
	reader, writer := schema.Pipe[*schema.Message](0)
	control := &runtimeLifecycleSourceControl{probe: make(chan struct{}), result: make(chan bool, 1)}
	go func() {
		for _, frame := range frames {
			if writer.Send(frame.message, frame.err) {
				control.result <- false
				writer.Close()
				return
			}
		}
		<-control.probe
		control.result <- writer.Send(&schema.Message{Role: schema.Assistant, Content: "post-terminal probe"}, nil)
		writer.Close()
	}()
	return reader, control
}

func newRuntimeContextLifecycleSource(ctx context.Context) (*schema.StreamReader[*schema.Message], *runtimeLifecycleSourceControl) {
	reader, writer := schema.Pipe[*schema.Message](0)
	control := &runtimeLifecycleSourceControl{probe: make(chan struct{}), result: make(chan bool, 1)}
	go func() {
		<-ctx.Done()
		if writer.Send(nil, ctx.Err()) {
			control.result <- false
			writer.Close()
			return
		}
		<-control.probe
		control.result <- writer.Send(&schema.Message{Role: schema.Assistant, Content: "post-terminal probe"}, nil)
		writer.Close()
	}()
	return reader, control
}

func assertRuntimeSourceClosedAfterDrain(t *testing.T, control *runtimeLifecycleSourceControl) {
	t.Helper()
	if control == nil {
		t.Fatal("source control was not initialized")
	}
	// StreamReader.Close has no error result. A post-terminal send on the real
	// Eino pipe must instead observe that the receiver was closed after drain.
	close(control.probe)
	select {
	case closed := <-control.result:
		if !closed {
			t.Fatal("source reader closed before all frames drained or remained open after terminal")
		}
	case <-time.After(runtimeStreamTestTimeout):
		t.Fatal("source reader close was not observed")
	}
}

func runtimeStreamSuccessFrames() []runtimeLifecycleFrame {
	return []runtimeLifecycleFrame{
		{message: &schema.Message{Role: schema.Assistant, Content: "Final "}},
		{message: &schema.Message{Content: "answer."}},
		{message: &schema.Message{ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage:        &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
		}}},
		{err: io.EOF},
	}
}

func runtimeLifecycleInput() []*schema.Message {
	return []*schema.Message{{Role: schema.User, Content: "Use the frozen evidence."}}
}

func runtimeLifecycleMessages(frames []runtimeLifecycleFrame) []*schema.Message {
	messages := make([]*schema.Message, 0, len(frames))
	for _, frame := range frames {
		if frame.err == nil {
			messages = append(messages, frame.message)
		}
	}
	return messages
}

func drainRuntimeLifecycleReader(t *testing.T, reader *schema.StreamReader[*schema.Message]) []*schema.Message {
	t.Helper()
	messages := make([]*schema.Message, 0, 4)
	for len(messages) < 16 {
		message, err := recvRuntimeLifecycle(t, reader)
		if errors.Is(err, io.EOF) {
			return messages
		}
		if err != nil {
			t.Fatalf("stream recv error=%v", err)
		}
		messages = append(messages, message)
	}
	t.Fatal("stream did not terminate within the frame bound")
	return nil
}

type runtimeLifecycleRecv struct {
	message *schema.Message
	err     error
}

func recvRuntimeLifecycle(t *testing.T, reader *schema.StreamReader[*schema.Message]) (*schema.Message, error) {
	t.Helper()
	result := make(chan runtimeLifecycleRecv, 1)
	go func() {
		message, err := reader.Recv()
		result <- runtimeLifecycleRecv{message: message, err: err}
	}()
	select {
	case received := <-result:
		return received.message, received.err
	case <-time.After(runtimeStreamTestTimeout):
		t.Fatal("stream recv blocked")
		return nil, nil
	}
}

func assertRuntimeLifecycleEOF(t *testing.T, reader *schema.StreamReader[*schema.Message]) {
	t.Helper()
	message, err := recvRuntimeLifecycle(t, reader)
	if message != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("terminal recv message=%+v err=%v", message, err)
	}
}

func runtimeLifecycleCanonicalResponse(t *testing.T, frames []runtimeLifecycleFrame) []byte {
	t.Helper()
	messages := make([]*schema.Message, 0, len(frames))
	for _, frame := range frames {
		if frame.err == nil {
			messages = append(messages, frame.message)
		}
	}
	combined, err := schema.ConcatMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	response, err := canonicalRuntimeResponse(combined)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func runtimeLifecycleHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func assertRuntimeInvocationFailure(t *testing.T, err, providerErr error, code string, kind foundation.ErrorKind) {
	t.Helper()
	if err == nil || errorCode(err) != code || errorKind(err) != kind {
		t.Fatalf("error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
	}
	if errors.Is(err, providerErr) || strings.Contains(err.Error(), "secret-canary") {
		t.Fatalf("provider error escaped the adapter boundary: %v", err)
	}
}

func assertRuntimeFailedCall(t *testing.T, repository *runtimeLifecycleRepository, code string) {
	t.Helper()
	calls, attempts := repository.snapshot()
	if attempts != 1 || len(calls) != 1 || calls[0].Status != agentdomain.ModelCallFailed ||
		calls[0].ErrorCode != code || calls[0].ResponseHash != "" || calls[0].ResponseBytes != 0 {
		t.Fatalf("complete attempts=%d calls=%+v", attempts, calls)
	}
}

func assertRuntimeLedgerSettled(t *testing.T, ledger *agentapplication.RunBudgetLedger, inputTokens, outputTokens int64) {
	t.Helper()
	snapshot := ledger.Snapshot()
	if snapshot.ModelCalls != 1 || snapshot.InputTokens != inputTokens || snapshot.OutputTokens != outputTokens ||
		snapshot.InFlightInput != 0 || snapshot.InFlightOutput != 0 {
		t.Fatalf("ledger=%+v", snapshot)
	}
}

type contextRuntimeStreamModel struct {
	stream func(context.Context) *schema.StreamReader[*schema.Message]
}

func (model *contextRuntimeStreamModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected generate")
}

func (model *contextRuntimeStreamModel) Stream(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return model.stream(ctx), nil
}

func (model *contextRuntimeStreamModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return model, nil
}

type runtimeLifecycleRepository struct {
	runtimeRecordingRepository
	completeErrors   []error
	completeAttempts int
	terminals        chan agentdomain.ModelCall
}

func newRuntimeLifecycleRepository(completeErrors ...error) *runtimeLifecycleRepository {
	return &runtimeLifecycleRepository{
		completeErrors: append([]error(nil), completeErrors...),
		terminals:      make(chan agentdomain.ModelCall, 2),
	}
}

func (repository *runtimeLifecycleRepository) CompleteModelCall(_ context.Context, command agentapplication.CompleteModelCallCommand) (agentdomain.ModelCall, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.completeAttempts++
	if index := repository.completeAttempts - 1; index < len(repository.completeErrors) && repository.completeErrors[index] != nil {
		return agentdomain.ModelCall{}, false, repository.completeErrors[index]
	}
	for index := range repository.calls {
		if repository.calls[index].ID == command.Call.ID {
			repository.calls[index] = command.Call
			repository.terminals <- command.Call
			return command.Call, false, nil
		}
	}
	return agentdomain.ModelCall{}, false, errors.New("call not started")
}

func (repository *runtimeLifecycleRepository) snapshot() ([]agentdomain.ModelCall, int) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]agentdomain.ModelCall(nil), repository.calls...), repository.completeAttempts
}

func (repository *runtimeLifecycleRepository) awaitTerminal(t *testing.T) agentdomain.ModelCall {
	t.Helper()
	select {
	case terminal := <-repository.terminals:
		return terminal
	case <-time.After(runtimeStreamTestTimeout):
		t.Fatal("model call terminal was not persisted")
		return agentdomain.ModelCall{}
	}
}

func newRuntimeLifecycleModel(
	t *testing.T,
	backend einomodel.ToolCallingChatModel,
	repository *runtimeLifecycleRepository,
) (*recordingRuntimeModel, *agentapplication.RunBudgetLedger) {
	t.Helper()
	now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	reservations := map[agentapplication.RunBudgetPhase]agentapplication.RunBudgetReservation{}
	for _, phase := range []agentapplication.RunBudgetPhase{
		agentapplication.RunBudgetPhaseAnswer,
		agentapplication.RunBudgetPhaseInitial,
		agentapplication.RunBudgetPhaseRepair,
		agentapplication.RunBudgetPhaseReduced,
		agentapplication.RunBudgetPhaseReview,
	} {
		reservations[phase] = agentapplication.RunBudgetReservation{
			ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10,
		}
	}
	ledger, err := agentapplication.NewRunBudgetLedger(agentapplication.RunBudgetLedgerConfig{
		NodeAttemptID:      "99100000-0000-4000-8000-000000000001",
		ModelRunID:         "99100000-0000-4000-8000-000000000002",
		MaxTotalModelCalls: 5, MaxAgentIterations: 1, MaxToolCalls: 0,
		MaxInputTokens: 50, MaxOutputTokens: 50, Deadline: now.Add(time.Minute),
		Clock: foundation.FixedClock{Value: now}, DownstreamReservations: reservations,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := agentapplication.NewModelCallRecorder(agentapplication.ModelCallRecorderDependencies{
		Repository:  repository,
		WorkspaceID: "99100000-0000-4000-8000-000000000003",
		ModelRunID:  "99100000-0000-4000-8000-000000000002",
		IDs:         &runtimeIDs{next: 10},
		Clock:       foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := newRecordingRuntimeModel(backend, runtimeCallBinding{
		model: runtimeTestModelRef(), profile: runtimeTestProfileRef(), prompt: runtimeTestPromptRef(), schema: runtimeTestSchemaRef(),
		maxInputTokens: 10, maxOutputTokens: 10, phase: agentdomain.ModelCallAnswer, budget: ledger, recorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	return model, ledger
}

var _ einomodel.ToolCallingChatModel = (*contextRuntimeStreamModel)(nil)
var _ agentapplication.ModelRunRepository = (*runtimeLifecycleRepository)(nil)
