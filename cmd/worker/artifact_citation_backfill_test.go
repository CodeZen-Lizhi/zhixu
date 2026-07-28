package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
)

func TestDispatchCitationBackfillUsesBoundedApplicationLimits(t *testing.T) {
	port := &workerCitationBackfillPortStub{alwaysFound: true}
	dispatcher, err := artifactapplication.NewCitationBackfillDispatcher(port)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := dispatchCitationBackfill(context.Background(), slog.New(slog.NewJSONHandler(io.Discard, nil)), dispatcher, citationBackfillPeriodicPhase)
	if err != nil {
		t.Fatal(err)
	}
	if port.calls != artifactapplication.MaxCitationBackfillWorkspaceBatch || port.lastLimit != artifactapplication.MaxCitationBackfillRevisionBatch ||
		batch.Workspaces != artifactapplication.MaxCitationBackfillWorkspaceBatch || batch.Completed != artifactapplication.MaxCitationBackfillWorkspaceBatch {
		t.Fatalf("calls=%d limit=%d batch=%+v", port.calls, port.lastLimit, batch)
	}
}

func TestDispatchCitationBackfillClassifiesUnknownFailureWithoutLeakingCause(t *testing.T) {
	const privateCause = "citation-backfill-private-cause"
	port := &workerCitationBackfillPortStub{err: errors.New(privateCause)}
	dispatcher, err := artifactapplication.NewCitationBackfillDispatcher(port)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	_, err = dispatchCitationBackfill(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)), dispatcher, citationBackfillStartupPhase)
	if err == nil || !strings.Contains(err.Error(), privateCause) {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(output.String(), privateCause) {
		t.Fatalf("private cause leaked into log: %s", output.String())
	}
	var entry struct {
		Message   string `json:"msg"`
		ErrorCode string `json:"error_code"`
		Retryable bool   `json:"retryable"`
		Phase     string `json:"phase"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode log: %v output=%s", err, output.String())
	}
	if entry.Message != "artifact citation selector backfill failed" || entry.ErrorCode != artifactapplication.ErrorCodeCitationBackfillFailed || entry.Retryable || entry.Phase != citationBackfillStartupPhase {
		t.Fatalf("entry=%+v", entry)
	}
}

type workerCitationBackfillPortStub struct {
	alwaysFound bool
	err         error
	calls       int
	lastLimit   int
}

func (stub *workerCitationBackfillPortStub) BackfillCitationSelectors(_ context.Context, limit int) (artifactapplication.CitationBackfillResult, bool, error) {
	stub.calls++
	stub.lastLimit = limit
	if stub.err != nil {
		return artifactapplication.CitationBackfillResult{}, false, stub.err
	}
	if !stub.alwaysFound {
		return artifactapplication.CitationBackfillResult{}, false, nil
	}
	return artifactapplication.CitationBackfillResult{Completed: true}, true, nil
}
