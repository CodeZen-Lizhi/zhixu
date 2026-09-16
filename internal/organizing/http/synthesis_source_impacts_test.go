package organizinghttp

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type sourceImpactsStub struct {
	synthesisNotesStub
	read func(context.Context, app.SynthesisSourceImpactQuery) (app.SynthesisSourceImpacts, error)
}

func (s sourceImpactsStub) ReadSynthesisSourceImpacts(ctx context.Context, q app.SynthesisSourceImpactQuery) (app.SynthesisSourceImpacts, error) {
	return s.read(ctx, q)
}
func TestSourceImpactHTTPRejectsForeignProjectionAndUnexpectedQueries(t *testing.T) {
	rev, ref := synthesisHTTPRevision(t)
	calls := 0
	foreign := false
	notes := sourceImpactsStub{read: func(_ context.Context, q app.SynthesisSourceImpactQuery) (app.SynthesisSourceImpacts, error) {
		calls++
		if q.WorkspaceID != rev.WorkspaceID || q.NoteID != rev.NoteID || q.RevisionID != rev.ID {
			t.Fatal("scope changed")
		}
		result := app.SynthesisSourceImpacts{WorkspaceID: q.WorkspaceID, NoteID: q.NoteID, RevisionID: q.RevisionID, Items: []app.SynthesisSourceImpact{{ID: rev.ID, Reason: "SOURCE_REMOVED", DetectedAt: time.Now().UTC(), CurrentlyUnavailable: true, Reference: ref, ItemIDs: []foundation.ID{rev.Items[0].ID}}}}
		if foreign {
			result.WorkspaceID = rev.NoteID
		}
		return result, nil
	}}
	router := synthesisRouter(NewSynthesisHandler(notes, synthesisProcessingStub{}, time.Second))
	path := "/api/v1/workspaces/" + string(rev.WorkspaceID) + "/synthesis/notes/" + string(rev.NoteID) + "/revisions/" + string(rev.ID) + "/source-impacts"
	for _, query := range []string{"?reason=SOURCE_REMOVED", "?revision_id=bad"} {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path+query, nil))
		if r.Code != 400 || calls != 0 {
			t.Fatalf("unexpected query status=%d calls=%d", r.Code, calls)
		}
	}
	r := httptest.NewRecorder()
	router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
	if r.Code != 200 {
		t.Fatalf("read %d %s", r.Code, r.Body.String())
	}
	foreign = true
	r = httptest.NewRecorder()
	router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
	if r.Code != 409 {
		t.Fatalf("foreign response=%d", r.Code)
	}
}
