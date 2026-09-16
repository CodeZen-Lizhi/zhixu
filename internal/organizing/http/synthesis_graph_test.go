package organizinghttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type synthesisGraphStub struct {
	synthesisNotesStub
	read func(context.Context, app.SynthesisSourceGraphQuery) (app.SynthesisSourceGraph, error)
}

func (s synthesisGraphStub) ReadSynthesisSourceGraph(ctx context.Context, q app.SynthesisSourceGraphQuery) (app.SynthesisSourceGraph, error) {
	return s.read(ctx, q)
}

func TestSynthesisSourceGraphValidatesPageAndBindings(t *testing.T) {
	rev, ref := synthesisHTTPRevision(t)
	calls := 0
	invalidResult := false
	notes := synthesisGraphStub{read: func(_ context.Context, q app.SynthesisSourceGraphQuery) (app.SynthesisSourceGraph, error) {
		calls++
		if q.WorkspaceID != rev.WorkspaceID || q.NoteID != rev.NoteID || q.RevisionID != rev.ID || q.Limit != 1 {
			t.Fatal("graph query changed")
		}
		graph := app.SynthesisSourceGraph{WorkspaceID: rev.WorkspaceID, NoteID: rev.NoteID, RevisionID: rev.ID, Sources: []domain.SynthesisSourceRef{ref}, SharedNotes: []app.SynthesisSharedSourceNote{}}
		if invalidResult {
			graph.SharedNotes = append(graph.SharedNotes, app.SynthesisSharedSourceNote{NoteID: rev.NoteID, RevisionID: rev.ID, RevisionNo: 1, Title: "self", Sources: graph.Sources})
		}
		return graph, nil
	}}
	router := synthesisRouter(NewSynthesisHandler(notes, synthesisProcessingStub{}, time.Second))
	base := "/api/v1/workspaces/" + string(rev.WorkspaceID) + "/synthesis/notes/" + string(rev.NoteID) + "/revisions/" + string(rev.ID) + "/source-graph"
	for _, q := range []string{"?limit=0", "?limit=21", "?limit=01", "?limit=1&limit=1", "?after_note_id=bad", "?relation=DEPENDENCY"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+q, nil))
		if response.Code != 400 || calls != 0 {
			t.Fatalf("query %s status %d calls %d", q, response.Code, calls)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"?limit=1", nil))
	if response.Code != 200 || calls != 1 {
		t.Fatalf("graph response %d %s", response.Code, response.Body.String())
	}
	invalidResult = true
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"?limit=1", nil))
	if response.Code != 409 {
		t.Fatalf("self link response %d", response.Code)
	}
}
