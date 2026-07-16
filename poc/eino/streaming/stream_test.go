package streaming

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type fakeReader struct {
	chunks   []string
	i        int
	closed   atomic.Int32
	closeErr error
}

func (f *fakeReader) Recv() (string, error) {
	if f.i >= len(f.chunks) {
		return "", io.EOF
	}
	v := f.chunks[f.i]
	f.i++
	return v, nil
}
func (f *fakeReader) Close() error { f.closed.Add(1); return f.closeErr }

func TestConsumeClosesOnEOFOrError(t *testing.T) {
	r := &fakeReader{chunks: []string{"a", "b"}}
	var got string
	err := Consume(context.Background(), r, func(s string) error { got += s; return nil })
	if err != nil || got != "ab" || r.closed.Load() != 1 {
		t.Fatalf("err=%v got=%q closed=%d", err, got, r.closed.Load())
	}
}

func TestConsumeReturnsCloseFailure(t *testing.T) {
	closeErr := errors.New("close failed")
	r := &fakeReader{closeErr: closeErr}
	err := Consume(context.Background(), r, nil)
	if !errors.Is(err, closeErr) {
		t.Fatalf("err=%v, want close failure", err)
	}
}

type blockingReader struct {
	closed  chan struct{}
	started chan struct{}
	count   atomic.Int32
}

func (r *blockingReader) Recv() (string, error) {
	close(r.started)
	<-r.closed
	return "", context.Canceled
}
func (r *blockingReader) Close() error {
	r.count.Add(1)
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}

func TestConsumeCancellationClosesReader(t *testing.T) {
	r := &blockingReader{closed: make(chan struct{}), started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Consume(ctx, r, nil) }()
	<-r.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		if r.count.Load() != 1 {
			t.Fatalf("close count=%d", r.count.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("consumer did not stop")
	}
}
