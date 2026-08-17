package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRAGDraftStreamSinkPersistsInOrderAndCompletes(t *testing.T) {
	store := newDraftStreamStoreFake()
	sink, err := beginRAGDraftStream(context.Background(), store, draftStreamTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"first", " second", " third"} {
		if err := sink.Append(context.Background(), agentapplication.AnswerStreamChunk{Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	session, err := sink.Complete(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != agentapplication.DraftStreamCompleted || store.status() != agentapplication.DraftStreamCompleted || strings.Join(store.contents(), "") != "first second third" {
		t.Fatalf("session=%+v status=%s contents=%q", session, store.status(), store.contents())
	}
	if ttl := store.beginTTL(); ttl != ragDraftStreamTTL {
		t.Fatalf("RAG draft TTL = %s, want %s", ttl, ragDraftStreamTTL)
	}
}

func TestRAGDraftStreamSinkPersistsFirstProviderFrameSeparately(t *testing.T) {
	store := newDraftStreamStoreFake()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &ragDraftStreamSink{
		store: store, session: store.session, binding: draftStreamTestBinding(),
		queue: make(chan string, 3), done: make(chan struct{}), cancel: cancel,
		base: context.Background(),
	}
	for _, content := range []string{"first", " second", " third"} {
		sink.queue <- content
	}
	close(sink.queue)

	sink.write(ctx)

	contents := store.contents()
	if len(contents) < 2 || contents[0] != "first" || strings.Join(contents, "") != "first second third" {
		t.Fatalf("persisted draft chunks=%q", contents)
	}
}

func TestRAGDraftStreamSinkDegradesAfterWriterFailure(t *testing.T) {
	store := newDraftStreamStoreFake()
	store.appendErr = errors.New("postgres unavailable")
	sink, err := beginRAGDraftStream(context.Background(), store, draftStreamTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append(context.Background(), agentapplication.AnswerStreamChunk{Content: "still returned by provider"}); err != nil {
		t.Fatal(err)
	}
	awaitDraftStatus(t, store, agentapplication.DraftStreamDegraded)
	session, err := sink.Complete(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != agentapplication.DraftStreamDegraded || store.status() != agentapplication.DraftStreamDegraded {
		t.Fatalf("session=%+v status=%s", session, store.status())
	}
}

func TestRAGDraftStreamSinkQueueIsBounded(t *testing.T) {
	store := newDraftStreamStoreFake()
	store.appendStarted = make(chan struct{})
	store.appendRelease = make(chan struct{})
	sink, err := beginRAGDraftStream(context.Background(), store, draftStreamTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append(context.Background(), agentapplication.AnswerStreamChunk{Content: "first"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.appendStarted:
	case <-time.After(time.Second):
		t.Fatal("draft writer did not start")
	}
	for index := 0; index < ragDraftStreamQueueFrames; index++ {
		if err := sink.Append(context.Background(), agentapplication.AnswerStreamChunk{Content: "queued"}); err != nil {
			t.Fatalf("append %d: %v", index, err)
		}
	}
	if err := sink.Append(context.Background(), agentapplication.AnswerStreamChunk{Content: "overflow"}); err == nil {
		t.Fatal("expected bounded queue failure")
	}
	awaitDraftStatus(t, store, agentapplication.DraftStreamDegraded)
	close(store.appendRelease)
	session, err := sink.Complete(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != agentapplication.DraftStreamDegraded {
		t.Fatalf("session=%+v", session)
	}
}

func TestRAGDraftStreamSinkAbortCancelsWriter(t *testing.T) {
	store := newDraftStreamStoreFake()
	store.appendStarted = make(chan struct{})
	store.waitForContext = true
	sink, err := beginRAGDraftStream(context.Background(), store, draftStreamTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append(context.Background(), agentapplication.AnswerStreamChunk{Content: "unverified"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.appendStarted:
	case <-time.After(time.Second):
		t.Fatal("draft writer did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sink.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	if store.status() != agentapplication.DraftStreamAborted {
		t.Fatalf("status=%s", store.status())
	}
}

func TestRAGDraftStreamSinkSealForTerminalDoesNotTransition(t *testing.T) {
	store := newDraftStreamStoreFake()
	sink, err := beginRAGDraftStream(context.Background(), store, draftStreamTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append(context.Background(), agentapplication.AnswerStreamChunk{Content: "unverified refusal draft"}); err != nil {
		t.Fatal(err)
	}
	session, err := sink.SealForTerminal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != store.session.ID || session.Status != agentapplication.DraftStreamActive || store.status() != agentapplication.DraftStreamActive {
		t.Fatalf("sealed session=%+v store=%s", session, store.status())
	}
}

func TestCompleteRAGDraftAbortsAfterTransitionFailure(t *testing.T) {
	store := newDraftStreamStoreFake()
	completeErr := errors.New("complete transition unavailable")
	store.completeErr = completeErr
	sink, err := beginRAGDraftStream(context.Background(), store, draftStreamTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append(context.Background(), agentapplication.AnswerStreamChunk{Content: "unpublished"}); err != nil {
		t.Fatal(err)
	}
	if _, err := completeRAGDraft(context.Background(), sink); !errors.Is(err, completeErr) {
		t.Fatalf("completeRAGDraft() error = %v", err)
	}
	if store.status() != agentapplication.DraftStreamAborted || store.abortCount() != 1 {
		t.Fatalf("status=%s aborts=%d", store.status(), store.abortCount())
	}
}

type draftStreamStoreFake struct {
	mu             sync.Mutex
	session        agentapplication.DraftStreamSession
	appended       []string
	appendErr      error
	completeErr    error
	aborts         int
	abortCommand   agentapplication.DraftStreamTransitionCommand
	appendStarted  chan struct{}
	appendRelease  chan struct{}
	waitForContext bool
	startOnce      sync.Once
	beginCommand   agentapplication.BeginDraftStreamCommand
}

func newDraftStreamStoreFake() *draftStreamStoreFake {
	binding := draftStreamTestBinding()
	now := time.Unix(1, 0).UTC()
	return &draftStreamStoreFake{session: agentapplication.DraftStreamSession{
		ID: foundation.ID("8a000000-0000-4000-8000-000000000001"), Binding: binding,
		Generation: 1, Status: agentapplication.DraftStreamActive, NextSeq: 1,
		ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	}}
}

func draftStreamTestBinding() agentapplication.DraftStreamBinding {
	return agentapplication.DraftStreamBinding{
		WorkspaceID: testWorkspaceID, AnswerID: foundation.ID("8a000000-0000-4000-8000-000000000002"),
		WorkflowRunID: testWorkflowRunID, NodeRunID: testNodeRunID, NodeAttemptID: testAttemptID,
		AttemptNo: 1, LeaseOwner: "worker-1",
	}
}

func (store *draftStreamStoreFake) BeginDraftStream(_ context.Context, command agentapplication.BeginDraftStreamCommand) (agentapplication.DraftStreamSession, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.beginCommand = command
	return store.session, nil
}

func (store *draftStreamStoreFake) AppendDraftStream(ctx context.Context, command agentapplication.DraftStreamAppendCommand) (agentapplication.DraftStreamChunk, error) {
	if store.appendStarted != nil {
		store.startOnce.Do(func() { close(store.appendStarted) })
	}
	if store.waitForContext {
		<-ctx.Done()
		return agentapplication.DraftStreamChunk{}, ctx.Err()
	}
	if store.appendRelease != nil {
		select {
		case <-store.appendRelease:
		case <-ctx.Done():
			return agentapplication.DraftStreamChunk{}, ctx.Err()
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.appendErr != nil {
		return agentapplication.DraftStreamChunk{}, store.appendErr
	}
	store.appended = append(store.appended, command.Content)
	sequence := store.session.NextSeq
	store.session.NextSeq++
	store.session.TotalBytes += len(command.Content)
	return agentapplication.DraftStreamChunk{SessionID: store.session.ID, Generation: store.session.Generation, Sequence: sequence, Content: command.Content}, nil
}

func (store *draftStreamStoreFake) CompleteDraftStream(_ context.Context, _ agentapplication.DraftStreamTransitionCommand) (agentapplication.DraftStreamSession, error) {
	store.mu.Lock()
	if store.completeErr != nil {
		err := store.completeErr
		store.mu.Unlock()
		return agentapplication.DraftStreamSession{}, err
	}
	store.mu.Unlock()
	return store.transition(agentapplication.DraftStreamCompleted), nil
}

func (store *draftStreamStoreFake) DegradeDraftStream(_ context.Context, _ agentapplication.DraftStreamTransitionCommand) (agentapplication.DraftStreamSession, error) {
	return store.transition(agentapplication.DraftStreamDegraded), nil
}

func (store *draftStreamStoreFake) AbortDraftStream(_ context.Context, command agentapplication.DraftStreamTransitionCommand) (agentapplication.DraftStreamSession, error) {
	store.mu.Lock()
	store.aborts++
	store.abortCommand = command
	store.mu.Unlock()
	return store.transition(agentapplication.DraftStreamAborted), nil
}

func (store *draftStreamStoreFake) ReadDraftStream(context.Context, agentapplication.DraftStreamReadQuery) (agentapplication.DraftStreamReadResult, error) {
	return agentapplication.DraftStreamReadResult{}, nil
}

func (store *draftStreamStoreFake) CleanupExpiredDraftStreams(context.Context, int) (int64, error) {
	return 0, nil
}

func (store *draftStreamStoreFake) transition(status agentapplication.DraftStreamStatus) agentapplication.DraftStreamSession {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.session.Status = status
	return store.session
}

func (store *draftStreamStoreFake) status() agentapplication.DraftStreamStatus {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.session.Status
}

func (store *draftStreamStoreFake) contents() []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]string(nil), store.appended...)
}

func (store *draftStreamStoreFake) beginTTL() time.Duration {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.beginCommand.TTL
}

func awaitDraftStatus(t *testing.T, store *draftStreamStoreFake, want agentapplication.DraftStreamStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if store.status() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("draft status=%s want=%s", store.status(), want)
}

func (store *draftStreamStoreFake) abortCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.aborts
}

var _ agentapplication.DraftStreamStore = (*draftStreamStoreFake)(nil)
