package organizinghttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/gin-gonic/gin"
)

type recommendationHTTPStub struct {
	app.AnchorService
	app.AnchorRecommendationService
	request app.AnchorRecommendationRequest
	calls   int
}

func (s *recommendationHTTPStub) RequestInitialAnchorRecommendation(_ context.Context, c app.RequestInitialAnchorRecommendationCommand) (app.AnchorRecommendationRequest, error) {
	s.calls++
	if c.WorkspaceID != s.request.WorkspaceID || c.NoteID != s.request.NoteID || c.BasisRevisionID != s.request.BasisRevisionID || c.ExpectedNoteVersion != 1 || c.IdempotencyKey != "infer" {
		return app.AnchorRecommendationRequest{}, app.AnchorInvalid()
	}
	return s.request, nil
}
func (s *recommendationHTTPStub) GetAnchorRecommendation(_ context.Context, w, id foundation.ID) (app.AnchorRecommendationRequest, error) {
	s.calls++
	return s.request, nil
}
func TestAnchorRecommendationHTTPBindsRequestAndProjectsExactEvidence(t *testing.T) {
	revision, ref := synthesisHTTPRevision(t)
	now := time.Now().UTC()
	stub := &recommendationHTTPStub{request: app.AnchorRecommendationRequest{ID: testID(70), WorkspaceID: revision.WorkspaceID, NoteID: revision.NoteID, BasisRevisionID: revision.ID, Kind: app.AnchorInitialScopeRecommendation, Evidence: []domain.SynthesisSourceRef{ref}, Status: domain.AnchorRecommendationPending, Version: 1, CreatedAt: now, UpdatedAt: now}}
	router := gin.New()
	NewAnchorHandler(stub, time.Second).Routes(router.Group("/api/v1"))
	base := "/api/v1/workspaces/" + string(revision.WorkspaceID) + "/synthesis/anchor-recommendations"
	body := `{"note_id":"` + string(revision.NoteID) + `","basis_revision_id":"` + string(revision.ID) + `","expected_note_version":1}`
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, newJSONRequest(http.MethodPost, base, body, "infer"))
	if recorder.Code != 202 || stub.calls != 1 {
		t.Fatalf("request=%d %s", recorder.Code, recorder.Body.String())
	}
	stub.request.Status, stub.request.ModelRunID, stub.request.ModelOutput = domain.AnchorRecommendationSucceeded, testID(71), json.RawMessage(`{"recommendation":{"title":"Redis","kind":"INITIAL_SCOPE","scope":{"topics":["Redis"],"audiences":["review"],"description":"Redis review"},"reason":"The exact module is Redis.","evidence":["S001"]},"no_recommendation":false}`)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, base+"/"+string(stub.request.ID), nil))
	if recorder.Code != 200 {
		t.Fatalf("result=%d %s", recorder.Code, recorder.Body.String())
	}
	var view anchorRecommendationView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil || view.Recommendation == nil || len(view.Recommendation.Evidence) != 1 || view.Recommendation.Evidence[0] != ref {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	for _, private := range []string{"model_output", "model_run_id", "model_input_hash", "S001"} {
		if strings.Contains(recorder.Body.String(), private) {
			t.Fatalf("private control field leaked: %s", private)
		}
	}
	stub.request.NoteID = testID(72)
	stub.request.WorkspaceID = testID(73)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, base+"/"+string(stub.request.ID), nil))
	if recorder.Code != 409 {
		t.Fatal("cross-workspace response accepted", recorder.Code)
	}
}
func TestAnchorRecommendationHTTPRejectsProviderControlInputBeforeOwner(t *testing.T) {
	stub := &recommendationHTTPStub{}
	router := gin.New()
	NewAnchorHandler(stub, time.Second).Routes(router.Group("/api/v1"))
	base := "/api/v1/workspaces/" + string(testID(1)) + "/synthesis/anchor-recommendations"
	for _, body := range []string{`{}`, `{"model_run_id":"` + string(testID(2)) + `"}`, `{"note_id":"` + string(testID(2)) + `","note_id":"` + string(testID(3)) + `"}`, `{"scope":{"topics":["Redis"]}}`} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, newJSONRequest(http.MethodPost, base, body, "infer"))
		if recorder.Code != 400 {
			t.Fatalf("invalid request=%d", recorder.Code)
		}
	}
	if stub.calls != 0 {
		t.Fatal("invalid input reached owner")
	}
}
