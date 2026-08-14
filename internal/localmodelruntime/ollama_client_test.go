package localmodelruntime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOllamaClientPullRequiresTerminalSuccessAndReadsShowBeforeReturning(t *testing.T) {
	var showCalls atomic.Int32
	client, err := NewOllamaClient(OllamaClientOptions{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := `{"status":"pulling manifest"}` + "\n" + `{"status":"success","completed":2,"total":2,"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}` + "\n"
			if request.URL.Path == "/api/show" {
				showCalls.Add(1)
				body = `{"model":"smollm2:135m","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":2}`
			}
			return responseFor(request, body), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var progress []PullProgress
	model, err := client.Pull(context.Background(), "smollm2:135m", func(value PullProgress) error {
		progress = append(progress, value)
		return nil
	})
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}
	if model.Name != "smollm2:135m" || model.Bytes != 2 || showCalls.Load() != 1 {
		t.Fatalf("model=%+v showCalls=%d", model, showCalls.Load())
	}
	if len(progress) != 2 || progress[1].Completed != 2 {
		t.Fatalf("progress=%+v", progress)
	}
}

func TestOllamaClientPullRejectsHTTP200WithoutSuccess(t *testing.T) {
	client, err := NewOllamaClient(OllamaClientOptions{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return responseFor(request, `{"status":"pulling manifest"}`+"\n"), nil
		}),
		NoProgressTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Pull(context.Background(), "smollm2:135m", nil)
	if !errors.Is(err, ErrOllamaResponse) {
		t.Fatalf("Pull() error = %v, want ErrOllamaResponse", err)
	}
}

func TestOllamaClientPullRejectsMalformedAndOversizedEvents(t *testing.T) {
	for name, stream := range map[string]string{
		"malformed": "not-json\n",
		"oversized": `{"status":"` + strings.Repeat("x", 64) + `"}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			client, err := NewOllamaClient(OllamaClientOptions{
				MaxPullEventBytes: 32,
				Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					return responseFor(request, stream), nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Pull(context.Background(), "smollm2:135m", nil)
			if !errors.Is(err, ErrOllamaResponse) && !errors.Is(err, ErrOllamaRequestLimit) {
				t.Fatalf("Pull() error = %v, want bounded protocol error", err)
			}
		})
	}
}

func TestOllamaClientPullStallCancelsBodyRead(t *testing.T) {
	client, err := NewOllamaClient(OllamaClientOptions{
		NoProgressTimeout: 20 * time.Millisecond,
		PullTimeout:       time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Request: request, Body: &blockingReadCloser{done: request.Context().Done()}, Header: make(http.Header)}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Pull(context.Background(), "smollm2:135m", nil)
	if !errors.Is(err, ErrOllamaPullStalled) {
		t.Fatalf("Pull() error = %v, want ErrOllamaPullStalled", err)
	}
}

func TestOllamaClientRejectsNonFixedOrigin(t *testing.T) {
	if _, err := NewOllamaClient(OllamaClientOptions{BaseURL: "http://localhost:11435"}); err == nil {
		t.Fatal("NewOllamaClient accepted a non-fixed child origin")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func responseFor(request *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Request: request, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type blockingReadCloser struct {
	done <-chan struct{}
}

func (reader *blockingReadCloser) Read([]byte) (int, error) {
	<-reader.done
	return 0, context.Canceled
}

func (reader *blockingReadCloser) Close() error { return nil }
