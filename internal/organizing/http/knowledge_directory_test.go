package organizinghttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type directoryStub struct {
	app.KnowledgeDirectoryReader
	project func(context.Context, domain.SynthesisSourceRef) (app.SynthesisKnowledgePointProjection, error)
}

func (stub directoryStub) ProjectSynthesisKnowledgeSnapshot(ctx context.Context, ref domain.SynthesisSourceRef, _ foundation.ID) (app.SynthesisKnowledgePointProjection, error) {
	return stub.project(ctx, ref)
}

func TestSynthesisKnowledgeDirectoryUsesOnlySavedRevisionReference(t *testing.T) {
	revision, reference := synthesisHTTPRevision(t)
	calls := 0
	wrongWorkspace := false
	notes := synthesisNotesStub{revision: func(_ context.Context, workspace, note, id foundation.ID) (domain.SynthesisRevision, error) {
		if workspace != revision.WorkspaceID || note != revision.NoteID || id != revision.ID {
			t.Fatal("revision binding changed")
		}
		return revision, nil
	}}
	directory := directoryStub{project: func(_ context.Context, ref domain.SynthesisSourceRef) (app.SynthesisKnowledgePointProjection, error) {
		calls++
		if ref != reference {
			t.Fatal("directory received a caller-supplied reference")
		}
		workspace := revision.WorkspaceID
		if wrongWorkspace {
			workspace = testID(99)
		}
		return app.SynthesisKnowledgePointProjection{Reference: ref, Directory: app.SourceKnowledgeDirectory{
			WorkspaceID: workspace, SourceVersionID: ref.Source.SourceVersionID, Status: app.KnowledgeDirectoryUnanalyzed}, Points: []app.KnowledgeDirectoryPoint{}}, nil
	}}
	bound := knowledgeBindingNotes{SynthesisService: notes, binding: func(q app.SynthesisKnowledgeBindingQuery) (foundation.ID, error) {
		if q.NoteID != revision.NoteID || q.RevisionID != revision.ID || q.Reference != reference {
			t.Fatal("historical binding query drifted")
		}
		return "", nil
	}}
	router := synthesisRouter(NewSynthesisHandlerWithDirectory(bound, synthesisProcessingStub{}, directory, time.Second))
	base := "/api/v1/workspaces/" + string(revision.WorkspaceID) + "/synthesis/notes/" + string(revision.NoteID) + "/revisions/" + string(revision.ID) + "/sources/"
	for _, test := range []struct {
		span  foundation.ID
		want  int
		calls int
	}{{reference.SourceSpanID, 200, 1}, {testID(98), 404, 1}} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+string(test.span)+"/knowledge-points", nil))
		if response.Code != test.want || calls != test.calls {
			t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
		}
	}
	wrongWorkspace = true
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+string(reference.SourceSpanID)+"/knowledge-points", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("wrong workspace status=%d", response.Code)
	}
}

type knowledgeBindingNotes struct {
	SynthesisService
	binding func(app.SynthesisKnowledgeBindingQuery) (foundation.ID, error)
}

func (notes knowledgeBindingNotes) ReadSynthesisKnowledgeBinding(_ context.Context, q app.SynthesisKnowledgeBindingQuery) (foundation.ID, error) {
	return notes.binding(q)
}
