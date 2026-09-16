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
	"github.com/gin-gonic/gin"
)

type fusionHTTPStub struct {
	app.AnchorService
	app.AnchorFusionRequestReader
	anchor   domain.Anchor
	requests []app.AnchorFusionRequest
}

func (s *fusionHTTPStub) GetAnchor(_ context.Context, w, id foundation.ID) (domain.Anchor, error) {
	if w != s.anchor.WorkspaceID || id != s.anchor.ID {
		return domain.Anchor{}, app.AnchorInvalid()
	}
	return s.anchor, nil
}
func (s *fusionHTTPStub) ListAnchorFusionRequests(_ context.Context, w, id foundation.ID, limit int) ([]app.AnchorFusionRequest, error) {
	if w != s.anchor.WorkspaceID || id != s.anchor.ID || limit != 20 {
		return nil, app.AnchorInvalid()
	}
	return s.requests, nil
}
func TestAnchorFusionHTTPKeepsQueuedDistinctFromOutcome(t *testing.T) {
	rev, ref := synthesisHTTPRevision(t)
	now := time.Now().UTC()
	anchor := domain.Anchor{ID: testID(60), WorkspaceID: rev.WorkspaceID, NoteID: rev.NoteID, BasisRevisionID: rev.ID, Title: "Redis", Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"复习"}, Description: "Redis 复习"}, ScopeVersion: 1, Version: 1, CreatedAt: now, UpdatedAt: now}
	request := app.AnchorFusionRequest{ID: testID(61), WorkspaceID: rev.WorkspaceID, AnchorID: anchor.ID, NoteID: rev.NoteID, ProposalID: testID(62), ScopeVersion: 1, SourceEvent: domain.SynthesisSourceReady{ID: testID(63), Source: ref.Source, IngestionAttemptID: testID(64), ProcessorVersion: domain.SynthesisProcessorVersion, CreatedAt: now}, AllowedSources: []domain.SynthesisSourceRef{ref}, Status: "PENDING", CreatedAt: now, UpdatedAt: now}
	stub := &fusionHTTPStub{anchor: anchor, requests: []app.AnchorFusionRequest{request}}
	router := gin.New()
	NewAnchorHandler(stub, time.Second).Routes(router.Group("/api/v1"))
	path := "/api/v1/workspaces/" + string(rev.WorkspaceID) + "/synthesis/anchors/" + string(anchor.ID) + "/fusion-requests"
	for _, tc := range []struct {
		status     string
		processing foundation.ID
		note       foundation.ID
		want       int
	}{{"PENDING", "", rev.NoteID, 200}, {"DISPATCHED", testID(65), rev.NoteID, 200}, {"STALE", "", rev.NoteID, 200}, {"SUCCEEDED", testID(65), rev.NoteID, 409}, {"PENDING", testID(65), rev.NoteID, 409}, {"DISPATCHED", "", rev.NoteID, 409}, {"PENDING", "", testID(66), 409}} {
		stub.requests[0].Status, stub.requests[0].ProcessingID, stub.requests[0].NoteID = tc.status, tc.processing, tc.note
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != tc.want {
			t.Fatalf("status %s response %d %s", tc.status, recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path+"?limit=100", nil))
	if recorder.Code != 400 {
		t.Fatal(recorder.Code)
	}
}
