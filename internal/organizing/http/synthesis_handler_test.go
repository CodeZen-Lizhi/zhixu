package organizinghttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type synthesisNotesStub struct {
	SynthesisService
	revision func(context.Context, foundation.ID, foundation.ID, foundation.ID) (domain.SynthesisRevision, error)
	source   func(context.Context, foundation.ID, foundation.ID, foundation.ID, domain.SynthesisSourceRef) (app.SynthesisSourceView, error)
}

func (stub synthesisNotesStub) GetSynthesisRevision(ctx context.Context, workspaceID, noteID, revisionID foundation.ID) (domain.SynthesisRevision, error) {
	return stub.revision(ctx, workspaceID, noteID, revisionID)
}

func (stub synthesisNotesStub) OpenSource(ctx context.Context, workspaceID, noteID, revisionID foundation.ID, source domain.SynthesisSourceRef) (app.SynthesisSourceView, error) {
	return stub.source(ctx, workspaceID, noteID, revisionID, source)
}

type synthesisProcessingStub struct {
	SynthesisProcessingService
	list  func(context.Context, app.SynthesisListQuery) (app.SynthesisProcessingPage, error)
	retry func(context.Context, app.RetrySynthesisCommand) (app.RetrySynthesisResult, error)
}

func (stub synthesisProcessingStub) ListProcessing(ctx context.Context, query app.SynthesisListQuery) (app.SynthesisProcessingPage, error) {
	return stub.list(ctx, query)
}

func (stub synthesisProcessingStub) RetryProcessing(ctx context.Context, command app.RetrySynthesisCommand) (app.RetrySynthesisResult, error) {
	return stub.retry(ctx, command)
}

func synthesisRouter(handler *SynthesisHandler) http.Handler {
	router := gin.New()
	handler.Routes(router.Group("/api/v1"))
	return router
}

func TestSynthesisHTTPRejectsAmbiguousRequestsBeforeOwnerCalls(t *testing.T) {
	router := synthesisRouter(NewSynthesisHandler(synthesisNotesStub{}, synthesisProcessingStub{}, time.Second))
	base := "/api/v1/workspaces/" + string(testID(1)) + "/synthesis"
	for _, query := range []string{"?workspace_id=" + string(testID(2)), "?limit=1&limit=2", "?limit=0", "?limit=101", "?limit=01", "?cursor=", "?cursor=bad", "?unknown=1"} {
		t.Run(query, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"/notes"+query, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
	for _, body := range []string{`{}`, `{"expected_version":null}`, `{"expected_version":0}`, `{"expected_version":1,"expected_version":2}`, `{"expected_version":1,"workspace_id":"` + string(testID(2)) + `"}`, `{"expected_version":1,"retryable":true}`} {
		t.Run(body, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, newJSONRequest(http.MethodPost, base+"/processing/"+string(testID(4))+"/retry", body, "retry-operation"))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSynthesisHTTPListsInitialFailureWithoutNoteOrModelPayload(t *testing.T) {
	processing := synthesisFailedProcessing()
	stub := synthesisProcessingStub{list: func(_ context.Context, query app.SynthesisListQuery) (app.SynthesisProcessingPage, error) {
		if query.WorkspaceID != testID(1) || query.Limit != 20 {
			t.Fatalf("query = %#v", query)
		}
		return app.SynthesisProcessingPage{Items: []app.SynthesisProcessing{processing}}, nil
	}}
	response := httptest.NewRecorder()
	synthesisRouter(NewSynthesisHandler(synthesisNotesStub{}, stub, time.Second)).ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+string(testID(1))+"/synthesis/processing", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var page synthesisPage[synthesisProcessingResponse]
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Status != app.SynthesisProcessingFailed || len(page.Items[0].RevisionIDs) != 0 || page.Items[0].Failure == nil || !page.Items[0].Failure.Retryable || page.NextCursor != nil {
		t.Fatalf("first failure projection = %#v", page)
	}
	for _, secret := range []string{"model_run_id", "request_hash", "source_event", "ingestion_attempt_id", "RAW_MODEL_INPUT"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("response exposed %s", secret)
		}
	}
	processing.Status = app.SynthesisProcessingRecoveryRequired
	if _, err := toSynthesisProcessing(processing, testID(1)); err == nil {
		t.Fatal("unknown outcome advertised as retryable")
	}
	processing.Failure.Retryable = false
	if _, err := toSynthesisProcessing(processing, testID(1)); err != nil {
		t.Fatal(err)
	}
}

func TestSynthesisHTTPSourceUsesOnlyFrozenRevisionReferences(t *testing.T) {
	revision, reference := synthesisHTTPRevision(t)
	availability := domain.MaterialAvailable
	openCalls := 0
	stub := synthesisNotesStub{revision: func(_ context.Context, workspaceID, noteID, revisionID foundation.ID) (domain.SynthesisRevision, error) {
		if noteID != revision.NoteID || revisionID != revision.ID {
			t.Fatal("wrong revision lookup")
		}
		return revision, nil
	}, source: func(_ context.Context, workspaceID, noteID, revisionID foundation.ID, source domain.SynthesisSourceRef) (app.SynthesisSourceView, error) {
		openCalls++
		if workspaceID != revision.WorkspaceID || noteID != revision.NoteID || revisionID != revision.ID || source != reference {
			t.Fatal("source tuple was not bound to the frozen revision")
		}
		source.Title = "Owner display title"
		text := ""
		if availability == domain.MaterialAvailable {
			text = "immutable source excerpt"
		}
		return app.SynthesisSourceView{Reference: source, Availability: availability, Text: text}, nil
	}}
	router := synthesisRouter(NewSynthesisHandler(stub, synthesisProcessingStub{}, time.Second))
	base := "/api/v1/workspaces/" + string(revision.WorkspaceID) + "/synthesis/notes/" + string(revision.NoteID) + "/revisions/" + string(revision.ID) + "/sources/"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+string(testID(999)), nil))
	if response.Code != http.StatusNotFound || openCalls != 0 {
		t.Fatalf("unknown span response = %d calls=%d", response.Code, openCalls)
	}
	for _, state := range []domain.MaterialAvailability{domain.MaterialAvailable, domain.MaterialStale, domain.MaterialUnavailable} {
		availability = state
		response = httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+string(reference.SourceSpanID), nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", state, response.Code, response.Body.String())
		}
		if state == domain.MaterialAvailable && !strings.Contains(response.Body.String(), `"text":"immutable source excerpt"`) || state != domain.MaterialAvailable && !strings.Contains(response.Body.String(), `"text":null`) {
			t.Fatalf("invalid source projection = %s", response.Body.String())
		}
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, strings.Replace(base, string(revision.WorkspaceID), string(testID(1000)), 1)+string(reference.SourceSpanID), nil))
	if response.Code != http.StatusConflict || openCalls != 3 {
		t.Fatalf("foreign workspace response = %d calls=%d", response.Code, openCalls)
	}
}

func TestSynthesisHTTPCursorBindsWorkspaceListLimitAndServer(t *testing.T) {
	handler := NewSynthesisHandler(synthesisNotesStub{}, synthesisProcessingStub{}, time.Second)
	now := testTime()
	position := synthesisCursor{WorkspaceID: testID(1), Kind: "notes", Limit: 20, BeforeTime: &now, BeforeID: testID(5)}
	cursor, err := handler.encodeCursor(position)
	if err != nil || cursor == nil {
		t.Fatalf("encode cursor = %v, %v", cursor, err)
	}
	expected := synthesisCursor{WorkspaceID: testID(1), Kind: "notes", Limit: 20}
	decoded, err := handler.decodeCursor(*cursor, expected)
	if err != nil || decoded.BeforeID != position.BeforeID || decoded.BeforeTime == nil || !decoded.BeforeTime.Equal(now) {
		t.Fatalf("decode = %#v, %v", decoded, err)
	}
	for _, changed := range []synthesisCursor{
		{WorkspaceID: testID(2), Kind: "notes", Limit: 20},
		{WorkspaceID: testID(1), Kind: "processing", Limit: 20},
		{WorkspaceID: testID(1), Kind: "notes", Limit: 10},
		{WorkspaceID: testID(1), Kind: "revisions", NoteID: testID(3), Limit: 20},
	} {
		if _, err := handler.decodeCursor(*cursor, changed); err == nil {
			t.Fatalf("accepted changed scope %#v", changed)
		}
	}
	parts := strings.Split(*cursor, ".")
	first := "A"
	if strings.HasPrefix(parts[1], first) {
		first = "B"
	}
	if _, err := handler.decodeCursor(parts[0]+"."+first+parts[1][1:], expected); err == nil {
		t.Fatal("accepted tampered signature")
	}
	if _, err := NewSynthesisHandler(synthesisNotesStub{}, synthesisProcessingStub{}, time.Second).decodeCursor(*cursor, expected); err == nil {
		t.Fatal("accepted cursor from a previous server")
	}
}

func TestSynthesisHTTPRetryPreservesCommandIdentityAndCAS(t *testing.T) {
	processing := synthesisFailedProcessing()
	calls := 0
	stub := synthesisProcessingStub{retry: func(_ context.Context, command app.RetrySynthesisCommand) (app.RetrySynthesisResult, error) {
		calls++
		if command.WorkspaceID != testID(1) || command.ProcessingID != processing.ID || command.ExpectedVersion != 1 || command.IdempotencyKey != "one-retry" {
			t.Fatalf("command = %#v", command)
		}
		processing.Status, processing.Version, processing.Failure, processing.CompletedAt = app.SynthesisProcessingPending, 2, nil, nil
		return app.RetrySynthesisResult{Processing: processing, Replayed: calls > 1}, nil
	}}
	router := synthesisRouter(NewSynthesisHandler(synthesisNotesStub{}, stub, time.Second))
	for attempt := range 2 {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, newJSONRequest(http.MethodPost, "/api/v1/workspaces/"+string(testID(1))+"/synthesis/processing/"+string(processing.ID)+"/retry", `{"expected_version":1}`, "one-retry"))
		if response.Code != http.StatusAccepted {
			t.Fatalf("retry %d = %d %s", attempt, response.Code, response.Body.String())
		}
		if (attempt == 1) != strings.Contains(response.Body.String(), `"replayed":true`) {
			t.Fatal("exact replay flag drifted")
		}
	}
}

func synthesisFailedProcessing() app.SynthesisProcessing {
	now := testTime()
	source := domain.SynthesisSourceVersion{WorkspaceID: testID(1), SourceID: testID(2), SourceVersionID: testID(3), ContentArtifactID: testID(4), ParseProjectionID: testID(5), ContentHash: testHash}
	return app.SynthesisProcessing{ID: testID(6), SourceEvent: domain.SynthesisSourceReady{ID: testID(7), Source: source, IngestionAttemptID: testID(8), ProcessorVersion: domain.SynthesisProcessorVersion, CreatedAt: now},
		WorkflowRunID: testID(9), ModelRunID: testID(10), RequestHash: "RAW_MODEL_INPUT", Status: app.SynthesisProcessingFailed,
		Failure: &domain.SynthesisFailure{Code: "SYNTHESIS_MODEL_CAPABILITY_UNAVAILABLE", Retryable: true}, Version: 1, CreatedAt: now, UpdatedAt: now, CompletedAt: &now}
}

func synthesisHTTPRevision(t *testing.T) (domain.SynthesisRevision, domain.SynthesisSourceRef) {
	t.Helper()
	processing := synthesisFailedProcessing()
	excerpt := sha256.Sum256([]byte("immutable source excerpt"))
	reference := domain.SynthesisSourceRef{Source: processing.SourceEvent.Source, SourceSpanID: testID(11), ExcerptHash: hex.EncodeToString(excerpt[:]), Title: "原始资料"}
	item := domain.SynthesisItem{ID: testID(12), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "不可变来源用于精确追溯", Sources: []domain.SynthesisSourceRef{reference}}}
	revision := domain.SynthesisRevision{ID: testID(13), WorkspaceID: testID(1), NoteID: testID(14), DocumentID: testID(15), ArticleRevisionID: testID(16), RevisionNo: 1, ArticleRevisionNo: 1,
		Title: "来源追溯", RendererVersion: domain.SynthesisRendererVersion, Items: []domain.SynthesisItem{item}, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddFact, Item: &item}}},
		SourceEventID: processing.SourceEvent.ID, WorkflowRunID: processing.WorkflowRunID, ModelRunID: processing.ModelRunID, CreatedAt: testTime()}
	markdown, err := domain.RenderSynthesisMarkdown(revision.WorkspaceID, revision.NoteID, revision.Title, revision.Items)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(markdown))
	revision.ContentHash = hex.EncodeToString(digest[:])
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	if err != nil {
		t.Fatal(err)
	}
	return revision, reference
}
