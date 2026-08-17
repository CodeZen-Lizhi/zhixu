package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	ragDraftStreamTTL           = 10 * time.Minute
	ragDraftWriteTimeout        = 2 * time.Second
	ragDraftStreamQueueFrames   = 32
	ragDraftStreamQueueFullCode = "AGENT_RAG_DRAFT_STREAM_QUEUE_FULL"
)

// ragDraftStreamSink decouples the Provider stream reader from PostgreSQL.
// One producer appends frames, while one worker preserves their database order.
type ragDraftStreamSink struct {
	store   agentapplication.DraftStreamStore
	session agentapplication.DraftStreamSession
	binding agentapplication.DraftStreamBinding

	queue  chan string
	done   chan struct{}
	cancel context.CancelFunc
	base   context.Context

	mu                    sync.Mutex
	closed                bool
	cancelWriterRequested bool
	failure               error
	degradationDone       chan struct{}
}

func beginRAGDraftStream(
	ctx context.Context,
	store agentapplication.DraftStreamStore,
	binding agentapplication.DraftStreamBinding,
) (*ragDraftStreamSink, error) {
	return beginDraftStream(ctx, store, binding, ragDraftStreamTTL)
}

func beginDraftStream(
	ctx context.Context,
	store agentapplication.DraftStreamStore,
	binding agentapplication.DraftStreamBinding,
	ttl time.Duration,
) (*ragDraftStreamSink, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if nilDependency(store) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("rag draft stream store is unavailable"))
	}
	session, err := store.BeginDraftStream(ctx, agentapplication.BeginDraftStreamCommand{
		DraftStreamBinding: binding,
		TTL:                ttl,
	})
	if err != nil {
		return nil, err
	}
	writerContext, cancel := context.WithCancel(ctx)
	sink := &ragDraftStreamSink{
		store: store, session: session, binding: binding,
		queue: make(chan string, ragDraftStreamQueueFrames), done: make(chan struct{}), cancel: cancel,
		base: context.WithoutCancel(ctx),
	}
	go sink.write(writerContext)
	return sink, nil
}

func (sink *ragDraftStreamSink) Append(ctx context.Context, chunk agentapplication.AnswerStreamChunk) error {
	if sink == nil || chunk.Content == "" || len(chunk.Content) > agentapplication.MaxAnswerStreamChunkBytes || !utf8.ValidString(chunk.Content) {
		return workflowError(foundation.ErrorInvalidInput, ragDraftStreamQueueFullCode, false, errors.New("rag draft stream chunk is invalid"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sink.mu.Lock()
	if sink.closed {
		sink.mu.Unlock()
		return workflowError(foundation.ErrorVersionConflict, ragDraftStreamQueueFullCode, false, errors.New("rag draft stream is closed"))
	}
	if sink.failure != nil {
		failure := sink.failure
		sink.mu.Unlock()
		return failure
	}
	select {
	case <-ctx.Done():
		sink.mu.Unlock()
		return ctx.Err()
	case sink.queue <- chunk.Content:
		sink.mu.Unlock()
		return nil
	default:
		err := workflowError(foundation.ErrorRetryableFailure, ragDraftStreamQueueFullCode, true, errors.New("rag draft stream queue is full"))
		sink.failure = err
		sink.mu.Unlock()
		sink.startDegradation()
		return err
	}
}

func (sink *ragDraftStreamSink) write(ctx context.Context) {
	defer close(sink.done)
	var pending string
	firstFrame := true
	for {
		content, open := pending, true
		pending = ""
		if content == "" {
			content, open = <-sink.queue
			if !open {
				return
			}
		}
		var batch strings.Builder
		batch.Grow(len(content))
		batch.WriteString(content)
		queueClosed := false
		if !firstFrame {
		collect:
			for batch.Len() < agentapplication.MaxAnswerStreamChunkBytes {
				select {
				case next, ok := <-sink.queue:
					if !ok {
						queueClosed = true
						break collect
					}
					if len(next) > agentapplication.MaxAnswerStreamChunkBytes-batch.Len() {
						pending = next
						break collect
					}
					batch.WriteString(next)
				default:
					break collect
				}
			}
		}
		writeContext, cancel := context.WithTimeout(ctx, ragDraftWriteTimeout)
		_, err := sink.store.AppendDraftStream(writeContext, agentapplication.DraftStreamAppendCommand{
			SessionID: sink.session.ID,
			Binding:   sink.binding,
			Content:   batch.String(),
		})
		cancel()
		if err != nil {
			sink.recordFailure(err)
			return
		}
		firstFrame = false
		if queueClosed && pending == "" {
			return
		}
	}
}

func (sink *ragDraftStreamSink) Complete(ctx context.Context) (agentapplication.DraftStreamSession, error) {
	if sink == nil {
		return agentapplication.DraftStreamSession{}, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("rag draft stream is unavailable"))
	}
	if err := sink.closeAndWait(ctx, false); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	command := agentapplication.DraftStreamTransitionCommand{SessionID: sink.session.ID, Binding: sink.binding}
	if sink.recordedFailure() != nil {
		return sink.store.DegradeDraftStream(ctxOrBackground(ctx), command)
	}
	return sink.store.CompleteDraftStream(ctxOrBackground(ctx), command)
}

func (sink *ragDraftStreamSink) Abort(ctx context.Context) error {
	if sink == nil {
		return nil
	}
	waitErr := sink.closeAndWait(ctx, true)
	_, transitionErr := sink.store.AbortDraftStream(ctxOrBackground(ctx), agentapplication.DraftStreamTransitionCommand{
		SessionID: sink.session.ID,
		Binding:   sink.binding,
	})
	return errors.Join(waitErr, transitionErr)
}

// SealForTerminal closes the writer without changing the database lifecycle.
// Refusal and clarification must pass the still-bound session to the Answer
// Finalizer so the business terminal state and ABORTED transition commit
// together. Error paths that have no business proposal should use Abort.
func (sink *ragDraftStreamSink) SealForTerminal(ctx context.Context) (agentapplication.DraftStreamSession, error) {
	if sink == nil {
		return agentapplication.DraftStreamSession{}, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false, errors.New("rag draft stream is unavailable"))
	}
	if err := sink.closeAndWait(ctx, true); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	return sink.session, nil
}

func (sink *ragDraftStreamSink) closeAndWait(ctx context.Context, cancelWriter bool) error {
	ctx = ctxOrBackground(ctx)
	sink.mu.Lock()
	if !sink.closed {
		sink.closed = true
		close(sink.queue)
	}
	if cancelWriter {
		sink.cancelWriterRequested = true
		sink.cancel()
	}
	sink.mu.Unlock()
	select {
	case <-sink.done:
		sink.cancel()
		return sink.waitForDegradation(ctx)
	case <-ctx.Done():
		sink.cancel()
		return ctx.Err()
	}
}

func (sink *ragDraftStreamSink) recordFailure(err error) {
	if err == nil {
		return
	}
	sink.mu.Lock()
	if sink.cancelWriterRequested && errors.Is(err, context.Canceled) {
		sink.mu.Unlock()
		return
	}
	first := sink.failure == nil
	if sink.failure == nil {
		sink.failure = err
	}
	sink.mu.Unlock()
	if first {
		sink.startDegradation()
	}
}

func (sink *ragDraftStreamSink) startDegradation() {
	if sink == nil {
		return
	}
	sink.mu.Lock()
	if sink.degradationDone != nil {
		sink.mu.Unlock()
		return
	}
	done := make(chan struct{})
	sink.degradationDone = done
	base := sink.base
	sink.mu.Unlock()
	go func() {
		degradeContext, cancel := context.WithTimeout(ctxOrBackground(base), ragDraftWriteTimeout)
		defer cancel()
		defer close(done)
		_, _ = sink.store.DegradeDraftStream(degradeContext, agentapplication.DraftStreamTransitionCommand{
			SessionID: sink.session.ID,
			Binding:   sink.binding,
		})
	}()
}

func (sink *ragDraftStreamSink) waitForDegradation(ctx context.Context) error {
	sink.mu.Lock()
	done := sink.degradationDone
	sink.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (sink *ragDraftStreamSink) recordedFailure() error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.failure
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ agentapplication.AnswerStreamSink = (*ragDraftStreamSink)(nil)
