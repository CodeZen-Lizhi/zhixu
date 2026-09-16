package organizinghttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

type bodyImpactsStub struct {
	synthesisNotesStub
	read func(context.Context, app.SynthesisBodyImpactQuery) (app.SynthesisBodyImpacts, error)
}

func (s bodyImpactsStub) ReadSynthesisBodyImpacts(ctx context.Context, q app.SynthesisBodyImpactQuery) (app.SynthesisBodyImpacts, error) {
	return s.read(ctx, q)
}

func TestBodyImpactHTTPBoundary(t *testing.T) {
	rev, _ := synthesisHTTPRevision(t)
	calls := 0
	mutate := func(*app.SynthesisBodyImpacts) {}
	notes := bodyImpactsStub{read: func(_ context.Context, q app.SynthesisBodyImpactQuery) (app.SynthesisBodyImpacts, error) {
		calls++
		if q.WorkspaceID != rev.WorkspaceID || q.NoteID != rev.NoteID || q.BaseRevisionID != rev.ID {
			t.Fatal("scope changed")
		}
		result := app.SynthesisBodyImpacts{Items: []app.SynthesisBodyImpact{{ID: testID(300), WorkspaceID: q.WorkspaceID, NoteID: q.NoteID, BaseRevisionID: q.BaseRevisionID, ItemID: rev.Items[0].ID, UpstreamNoteID: testID(301), UpstreamRevisionID: testID(302), UpstreamItemID: testID(303), UpstreamPublicationID: testID(304), PublicationID: testID(305), PublishedRevisionID: testID(306), EventID: testID(307), Reason: "CONTENT_CHANGED", DetectedAt: time.Now().UTC()}}}
		mutate(&result)
		return result, nil
	}}
	router := synthesisRouter(NewSynthesisHandler(notes, synthesisProcessingStub{}, time.Second))
	path := "/api/v1/workspaces/" + string(rev.WorkspaceID) + "/synthesis/notes/" + string(rev.NoteID) + "/revisions/" + string(rev.ID) + "/body-impacts"
	get := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path+query, nil))
		return r
	}
	for _, q := range []string{"?reason=CONTENT_CHANGED", "?limit=0", "?limit=101", "?limit=01", "?limit=1&limit=2", "?after_id=bad", "?after_id=", "?limit=%ZZ"} {
		if r := get(q); r.Code != 400 || calls != 0 {
			t.Fatalf("%s: %d calls=%d", q, r.Code, calls)
		}
	}
	if r := get(""); r.Code != 200 || !strings.Contains(r.Body.String(), `"next_after_id":null`) || strings.Contains(r.Body.String(), "event_id") {
		t.Fatalf("read: %d %s", r.Code, r.Body.String())
	}
	for name, change := range map[string]func(*app.SynthesisBodyImpacts){
		"foreign":          func(r *app.SynthesisBodyImpacts) { r.Items[0].WorkspaceID = testID(99) },
		"base":             func(r *app.SynthesisBodyImpacts) { r.Items[0].BaseRevisionID = testID(99) },
		"reason":           func(r *app.SynthesisBodyImpacts) { r.Items[0].Reason = "OTHER" },
		"duplicate":        func(r *app.SynthesisBodyImpacts) { r.Items = append(r.Items, r.Items[0]) },
		"cursor":           func(r *app.SynthesisBodyImpacts) { r.NextAfterID = testID(99) },
		"same publication": func(r *app.SynthesisBodyImpacts) { r.Items[0].PublicationID = r.Items[0].UpstreamPublicationID },
	} {
		t.Run(name, func(t *testing.T) {
			mutate = change
			if r := get(""); r.Code != 409 {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
	mutate = func(r *app.SynthesisBodyImpacts) { r.NextAfterID = r.Items[0].ID }
	if r := get("?limit=1"); r.Code != 200 {
		t.Fatalf("page %d %s", r.Code, r.Body.String())
	}
	mutate = func(*app.SynthesisBodyImpacts) {}
	if r := get("?after_id=" + string(testID(300))); r.Code != 409 {
		t.Fatalf("non-advancing page %d", r.Code)
	}
}
