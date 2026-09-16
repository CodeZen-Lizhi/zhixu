package organizinghttp

import (
	"context"
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type updateSummariesStub struct {
	synthesisNotesStub
	read func(context.Context, app.SynthesisUpdateSummaryQuery) (app.SynthesisUpdateSummaries, error)
}

func (s updateSummariesStub) ReadSynthesisUpdateSummaries(ctx context.Context, q app.SynthesisUpdateSummaryQuery) (app.SynthesisUpdateSummaries, error) {
	return s.read(ctx, q)
}
func TestUpdateSummaryHTTPBoundary(t *testing.T) {
	calls := 0
	var failure error
	mutate := func(*app.SynthesisUpdateSummaries) {}
	stub := updateSummariesStub{read: func(_ context.Context, q app.SynthesisUpdateSummaryQuery) (app.SynthesisUpdateSummaries, error) {
		calls++
		out := app.SynthesisUpdateSummaries{WorkspaceID: q.WorkspaceID, Items: []app.SynthesisNoteUpdateSummary{{NoteID: q.NoteIDs[0], CurrentRevisionID: testID(3), PublishedRevisionID: testID(4), Items: []app.SynthesisUpdateSummary{{RevisionID: testID(3), BodyReviewCount: 1}, {RevisionID: testID(4), SourceReviewCount: 2}}}}}
		mutate(&out)
		return out, failure
	}}
	router := synthesisRouter(NewSynthesisHandler(stub, synthesisProcessingStub{}, time.Second))
	path := "/api/v1/workspaces/" + string(testID(1)) + "/synthesis/update-summaries"
	get := func(q string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path+q, nil))
		return r
	}
	for _, q := range []string{"", "?note_ids=", "?note_ids=bad", "?note_ids=" + string(testID(2)) + "," + string(testID(2)), "?note_ids=" + string(testID(2)) + "&note_ids=" + string(testID(3)), "?note_ids=%ZZ", "?note_ids=" + strings.Repeat(string(testID(2))+",", 50) + string(testID(3)), "?note_ids=" + string(testID(2)) + "&other=1"} {
		if r := get(q); r.Code != 400 || calls != 0 {
			t.Fatalf("invalid query %s: %d calls=%d", q, r.Code, calls)
		}
	}
	q := "?note_ids=" + string(testID(2))
	if r := get(q); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, fn := range []func(*app.SynthesisUpdateSummaries){
		func(r *app.SynthesisUpdateSummaries) { r.WorkspaceID = testID(9) },
		func(r *app.SynthesisUpdateSummaries) { r.Items[0].NoteID = testID(9) },
		func(r *app.SynthesisUpdateSummaries) { r.Items[0].Items[0].RevisionID = testID(9) },
		func(r *app.SynthesisUpdateSummaries) { r.Items[0].Items[0].BodyReviewCount = -1 },
		func(r *app.SynthesisUpdateSummaries) { r.Items[0].Items[0].SourceReviewCount = 9007199254740992 },
		func(r *app.SynthesisUpdateSummaries) { r.Items[0].Items[1] = r.Items[0].Items[0] },
		func(r *app.SynthesisUpdateSummaries) { r.Items = nil },
		func(r *app.SynthesisUpdateSummaries) { r.Items[0].CurrentRevisionID = foundation.ID("bad") },
	} {
		mutate = fn
		if r := get(q); r.Code != 409 {
			t.Fatalf("bad result accepted %d %s", r.Code, r.Body.String())
		}
	}
	mutate = func(*app.SynthesisUpdateSummaries) {}
	failure = errors.New("backend unavailable")
	if r := get(q); r.Code != 500 {
		t.Fatal("backend error hidden", r.Code)
	}
}
