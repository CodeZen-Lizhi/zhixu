package application

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCitationBackfillDispatcherUsesBoundedWorkspaceAndRevisionBatches(t *testing.T) {
	port := &citationBackfillPortStub{alwaysFound: true}
	dispatcher, err := NewCitationBackfillDispatcher(port)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := dispatcher.DispatchBatch(context.Background(), MaxCitationBackfillWorkspaceBatch, MaxCitationBackfillRevisionBatch)
	if err != nil {
		t.Fatal(err)
	}
	if port.calls != MaxCitationBackfillWorkspaceBatch || port.lastLimit != MaxCitationBackfillRevisionBatch ||
		batch.Workspaces != MaxCitationBackfillWorkspaceBatch || batch.ProcessedRevisions != MaxCitationBackfillWorkspaceBatch ||
		batch.ProcessedSelectors != MaxCitationBackfillWorkspaceBatch || batch.ValidatedRevisions != MaxCitationBackfillWorkspaceBatch ||
		batch.Completed != MaxCitationBackfillWorkspaceBatch {
		t.Fatalf("calls=%d limit=%d batch=%+v", port.calls, port.lastLimit, batch)
	}
}

func TestCitationBackfillDispatcherRejectsInvalidBounds(t *testing.T) {
	dispatcher, err := NewCitationBackfillDispatcher(&citationBackfillPortStub{})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ workspaces, revisions int }{{0, 1}, {1, 0}, {MaxCitationBackfillWorkspaceBatch + 1, 1}, {1, MaxCitationBackfillRevisionBatch + 1}} {
		if _, err := dispatcher.DispatchBatch(context.Background(), request.workspaces, request.revisions); err == nil {
			t.Fatalf("request=%+v accepted", request)
		}
	}
}

type citationBackfillPortStub struct {
	alwaysFound bool
	calls       int
	lastLimit   int
}

func (stub *citationBackfillPortStub) BackfillCitationSelectors(_ context.Context, limit int) (CitationBackfillResult, bool, error) {
	stub.calls++
	stub.lastLimit = limit
	if !stub.alwaysFound {
		return CitationBackfillResult{}, false, nil
	}
	return CitationBackfillResult{
		WorkspaceID: artifactApplicationTestID(9000 + stub.calls), ProcessedRevisions: 1,
		ProcessedSelectors: 1, ValidatedRevisions: 1, Completed: true,
	}, true, nil
}

func artifactApplicationTestID(value int) foundation.ID {
	return foundation.ID("93000000-0000-4000-8000-000000009001")
}
