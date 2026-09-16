package organizinghttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

type supplementStub struct {
	synthesisNotesStub
	list func(context.Context, app.SynthesisSupplementListQuery) (app.SynthesisSupplementPage, error)
	open func(context.Context, foundation.ID, foundation.ID, foundation.ID) (app.SynthesisSourceSupplement, app.SynthesisSourceView, error)
}

func (s supplementStub) ListSupplements(c context.Context, q app.SynthesisSupplementListQuery) (app.SynthesisSupplementPage, error) {
	return s.list(c, q)
}
func (s supplementStub) OpenSupplement(c context.Context, w, n, id foundation.ID) (app.SynthesisSourceSupplement, app.SynthesisSourceView, error) {
	return s.open(c, w, n, id)
}

func TestSupplementCursorCannotCrossNotes(t *testing.T) {
	revision, ref := synthesisHTTPRevision(t)
	at := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	record := app.SynthesisSourceSupplement{ID: testID(30), WorkspaceID: revision.WorkspaceID, NoteID: revision.NoteID, BaseRevisionID: revision.ID, ItemID: revision.Items[0].ID, Slot: "FACT", AlternativeIndex: -1, ProcessingID: testID(31), Reference: ref, CreatedAt: at}
	calls := 0
	stub := supplementStub{list: func(_ context.Context, q app.SynthesisSupplementListQuery) (app.SynthesisSupplementPage, error) {
		calls++
		return app.SynthesisSupplementPage{Items: []app.SynthesisSourceSupplement{record}, NextTime: &at, NextID: record.ID}, nil
	}}
	router := synthesisRouter(NewSynthesisHandler(stub, synthesisProcessingStub{}, time.Second))
	base := "/api/v1/workspaces/" + string(revision.WorkspaceID) + "/synthesis/notes/"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+string(revision.NoteID)+"/supplements?limit=1", nil))
	if response.Code != 200 {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	var page synthesisPage[app.SynthesisSourceSupplement]
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.NextCursor == nil {
		t.Fatalf("missing cursor: %s", response.Body.String())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+string(testID(99))+"/supplements?limit=1&cursor="+*page.NextCursor, nil))
	if response.Code != 400 || calls != 1 {
		t.Fatalf("cross-note cursor reached owner: %d calls=%d", response.Code, calls)
	}
}
