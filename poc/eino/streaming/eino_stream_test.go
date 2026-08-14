package streaming

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const einoStreamTestTimeout = time.Second

type einoProducer func(context.Context, string, *schema.StreamWriter[string])

func compileEinoStream(t *testing.T, producer einoProducer) compose.Runnable[string, string] {
	t.Helper()

	chain := compose.NewChain[string, string]()
	chain.AppendLambda(compose.StreamableLambda(func(ctx context.Context, input string) (*schema.StreamReader[string], error) {
		reader, writer := schema.Pipe[string](0)
		go func() {
			defer writer.Close()
			producer(ctx, input, writer)
		}()
		return reader, nil
	}))

	runnable, err := chain.Compile(context.Background())
	if err != nil {
		t.Fatalf("compile Eino stream: %v", err)
	}
	return runnable
}

func TestCompiledEinoStreamEmitsMultipleFrames(t *testing.T) {
	producerDone := make(chan struct{})
	runnable := compileEinoStream(t, func(_ context.Context, input string, writer *schema.StreamWriter[string]) {
		defer close(producerDone)
		for _, frame := range []string{input, "-", "stream"} {
			if writer.Send(frame, nil) {
				return
			}
		}
	})

	reader, err := runnable.Stream(context.Background(), "eino")
	if err != nil {
		t.Fatalf("start compiled Eino stream: %v", err)
	}
	defer reader.Close()

	var got []string
	for {
		frame, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("receive compiled Eino stream: %v", recvErr)
		}
		got = append(got, frame)
	}
	if want := []string{"eino", "-", "stream"}; !slices.Equal(got, want) {
		t.Fatalf("frames = %#v, want %#v", got, want)
	}

	select {
	case <-producerDone:
	case <-time.After(einoStreamTestTimeout):
		t.Fatal("Eino stream producer did not finish after EOF")
	}
}

func TestCompiledEinoStreamEarlyCloseReleasesProducer(t *testing.T) {
	producerReleased := make(chan struct{})
	runnable := compileEinoStream(t, func(_ context.Context, _ string, writer *schema.StreamWriter[string]) {
		for i := 0; ; i++ {
			if writer.Send(strconv.Itoa(i), nil) {
				close(producerReleased)
				return
			}
		}
	})

	reader, err := runnable.Stream(context.Background(), "unused")
	if err != nil {
		t.Fatalf("start compiled Eino stream: %v", err)
	}
	if _, err = reader.Recv(); err != nil {
		reader.Close()
		t.Fatalf("receive first frame: %v", err)
	}
	reader.Close()

	select {
	case <-producerReleased:
	case <-time.After(einoStreamTestTimeout):
		t.Fatal("top-level Eino reader close did not release producer")
	}
}

func TestCompiledEinoStreamCancellationReachesProducer(t *testing.T) {
	producerStarted := make(chan struct{})
	cancelObserved := make(chan error, 1)
	runnable := compileEinoStream(t, func(ctx context.Context, _ string, _ *schema.StreamWriter[string]) {
		close(producerStarted)
		<-ctx.Done()
		cancelObserved <- ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	reader, err := runnable.Stream(ctx, "unused")
	if err != nil {
		cancel()
		t.Fatalf("start compiled Eino stream: %v", err)
	}

	select {
	case <-producerStarted:
	case <-time.After(einoStreamTestTimeout):
		cancel()
		reader.Close()
		t.Fatal("Eino stream producer did not start")
	}
	cancel()

	select {
	case observed := <-cancelObserved:
		if !errors.Is(observed, context.Canceled) {
			reader.Close()
			t.Fatalf("producer context error = %v, want context.Canceled", observed)
		}
	case <-time.After(einoStreamTestTimeout):
		reader.Close()
		t.Fatal("compiled Eino stream did not propagate cancellation")
	}
	reader.Close()
}
