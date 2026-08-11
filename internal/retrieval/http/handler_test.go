package retrievalhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/gin-gonic/gin"
)

func TestHandlerSearchDefaultsHybridCanonicalizesAndPaginates(t *testing.T) {
	search := &fakeSearchService{}
	search.run = func(request domain.SearchRequest) (domain.SearchResult, error) {
		result := cursorSearchResult(3, "92000000-0000-4000-8000-000000000002")
		result.RequestedMode = domain.SearchModeHybrid
		result.EffectiveMode = domain.SearchModeKeyword
		result.IndexDegradedCapabilities = []domain.DegradedCapability{domain.DegradedVector}
		result.Degradations = []domain.SearchDegradation{
			{Capability: domain.SearchDegradationVector, Code: "RETRIEVAL_VECTOR_UNAVAILABLE"},
			{Capability: domain.SearchDegradationRerank, Code: "RETRIEVAL_RERANK_UNAVAILABLE"},
		}
		return result, nil
	}
	handler := NewHandler(search, &fakeEvidenceReferenceService{}, mustCursorCodec(t, 'a'))
	router := retrievalTestRouter(handler)

	body := `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"  stable query  ","filters":{"path_prefixes":["docs/","docs"]},"limit":2}`
	first := performJSONRequest(t, router, http.MethodPost, "/search", body)
	if first.Code != http.StatusOK {
		t.Fatalf("first search status=%d response_bytes=%d", first.Code, first.Body.Len())
	}
	var response searchResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if search.calls != 1 || search.request.Query != "stable query" || search.request.Mode != domain.SearchModeHybrid ||
		search.request.Limit != domain.MaxSearchLimit || len(search.request.Filter.PathPrefixes) != 1 || search.request.Filter.PathPrefixes[0] != "docs" {
		t.Fatalf("unexpected canonical request: calls=%d query_bytes=%d mode=%q limit=%d path_prefixes=%d", search.calls, len(search.request.Query), search.request.Mode, search.request.Limit, len(search.request.Filter.PathPrefixes))
	}
	if response.RequestedMode != "hybrid" || response.EffectiveMode != "keyword" || len(response.Items) != 2 ||
		response.NextCursor == "" || len(response.IndexDegradedCapabilities) != 1 || len(response.Degradations) != 2 {
		t.Fatalf("unexpected first response: requested=%q effective=%q items=%d cursor=%t index_degraded=%d degradations=%d", response.RequestedMode, response.EffectiveMode, len(response.Items), response.NextCursor != "", len(response.IndexDegradedCapabilities), len(response.Degradations))
	}
	if response.Items[0].Provenances[0].SourceVersionHref == "" || response.Items[0].Provenances[0].SourceSpanHref == "" ||
		response.Items[0].Scores.Vector != nil || response.Items[0].Scores.Fusion.Rank != 1 {
		t.Fatalf("unexpected evidence mapping: chunk_id=%q provenances=%d vector=%t fusion_rank=%d", response.Items[0].ChunkID, len(response.Items[0].Provenances), response.Items[0].Scores.Vector != nil, response.Items[0].Scores.Fusion.Rank)
	}

	secondBody := `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"stable query","retrieval_mode":"hybrid","filters":{"path_prefixes":["docs"]},"cursor":"` + response.NextCursor + `","limit":2}`
	second := performJSONRequest(t, router, http.MethodPost, "/search", secondBody)
	if second.Code != http.StatusOK {
		t.Fatalf("second search status=%d response_bytes=%d", second.Code, second.Body.Len())
	}
	response = searchResponse{}
	if err := json.Unmarshal(second.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if len(response.Items) != 1 || response.NextCursor != "" || response.Items[0].ChunkID != "92000000-0000-4000-8000-000000000102" {
		t.Fatalf("unexpected second response: items=%d cursor=%t first_chunk_id=%q", len(response.Items), response.NextCursor != "", firstChunkID(response.Items))
	}
}

func TestHandlerSearchEncodesRootHeadingAsEmptyArray(t *testing.T) {
	result := cursorSearchResult(1, "92000000-0000-4000-8000-000000000002")
	result.Items[0].HeadingPath = nil
	handler := NewHandler(&fakeSearchService{result: result}, &fakeEvidenceReferenceService{}, mustCursorCodec(t, 'a'))
	router := retrievalTestRouter(handler)

	response := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","retrieval_mode":"keyword"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("search status=%d response_bytes=%d", response.Code, response.Body.Len())
	}
	var wire struct {
		Items []struct {
			HeadingPath json.RawMessage `json:"heading_path"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if len(wire.Items) != 1 || !bytes.Equal(wire.Items[0].HeadingPath, []byte("[]")) {
		t.Fatalf("root heading must be an empty array: items=%d heading_bytes=%d", len(wire.Items), firstHeadingBytes(wire.Items))
	}
}

func TestHandlerSearchRejectsInvalidWireAndMapsServiceError(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","unknown":true}`},
		{name: "unknown filter field", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"unknown":true}}`},
		{name: "workspace", body: `{"workspace_id":"invalid","query":"q"}`},
		{name: "mode", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","retrieval_mode":"magic"}`},
		{name: "limit", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","limit":101}`},
		{name: "time", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"captured_at_from":"not-time"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(&fakeSearchService{}, &fakeEvidenceReferenceService{}, mustCursorCodec(t, 'a'))
			router := retrievalTestRouter(handler)
			response := performJSONRequest(t, router, http.MethodPost, "/search", test.body)
			assertProblem(t, response, http.StatusBadRequest, "")
		})
	}

	handler := NewHandler(&fakeSearchService{}, &fakeEvidenceReferenceService{}, mustCursorCodec(t, 'a'))
	router := retrievalTestRouter(handler)
	invalidFilter := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"source_ids":["invalid"]}}`)
	assertProblem(t, invalidFilter, http.StatusBadRequest, domain.ErrorCodeSearchRequestInvalid)
	invalidTime := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"captured_at_from":"not-time"}}`)
	assertProblem(t, invalidTime, http.StatusBadRequest, domain.ErrorCodeSearchRequestInvalid)
	spacedMode := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","retrieval_mode":" hybrid "}`)
	assertProblem(t, spacedMode, http.StatusBadRequest, domain.ErrorCodeSearchRequestInvalid)
	nulQuery := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"a\u0000b"}`)
	assertProblem(t, nulQuery, http.StatusBadRequest, domain.ErrorCodeSearchRequestInvalid)

	nullAndEmptyTests := []struct {
		name string
		body string
		code string
	}{
		{name: "mode null", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","retrieval_mode":null}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "mode empty", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","retrieval_mode":""}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "cursor null", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","cursor":null}`, code: domain.ErrorCodeSearchCursorInvalid},
		{name: "cursor empty", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","cursor":""}`, code: domain.ErrorCodeSearchCursorInvalid},
		{name: "filters null", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":null}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "limit null", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","limit":null}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "source ids null", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"source_ids":null}}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "source version ids null", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"source_version_ids":null}}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "path prefixes null", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"path_prefixes":null}}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "captured from null", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"captured_at_from":null}}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "captured before null", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"captured_at_before":null}}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "workspace wrong type", body: `{"workspace_id":1,"query":"q"}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "query wrong type", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":1}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "mode wrong type", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","retrieval_mode":1}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "cursor wrong type", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","cursor":1}`, code: domain.ErrorCodeSearchCursorInvalid},
		{name: "filters wrong type", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":[]}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "limit wrong type", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","limit":"20"}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "source ids wrong type", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"source_ids":"invalid"}}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "source id item wrong type", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"source_ids":[1]}}`, code: domain.ErrorCodeSearchRequestInvalid},
		{name: "captured from wrong type", body: `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","filters":{"captured_at_from":1}}`, code: domain.ErrorCodeSearchRequestInvalid},
	}
	for _, test := range nullAndEmptyTests {
		t.Run(test.name, func(t *testing.T) {
			response := performJSONRequest(t, router, http.MethodPost, "/search", test.body)
			assertProblem(t, response, http.StatusBadRequest, test.code)
		})
	}

	search := &fakeSearchService{err: foundation.NewError(foundation.ErrorDependencyUnavailable, "RETRIEVAL_SEMANTIC_UNAVAILABLE", false, errors.New("unavailable"))}
	handler = NewHandler(search, &fakeEvidenceReferenceService{}, mustCursorCodec(t, 'a'))
	router = retrievalTestRouter(handler)
	response := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","retrieval_mode":"semantic"}`)
	assertProblem(t, response, http.StatusServiceUnavailable, "RETRIEVAL_SEMANTIC_UNAVAILABLE")
}

func TestHandlerSearchRejectsTamperedCursor(t *testing.T) {
	search := &fakeSearchService{result: cursorSearchResult(2, "92000000-0000-4000-8000-000000000002")}
	handler := NewHandler(search, &fakeEvidenceReferenceService{}, mustCursorCodec(t, 'a'))
	router := retrievalTestRouter(handler)
	first := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"stable query","retrieval_mode":"keyword","limit":1}`)
	var response searchResponse
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &response) != nil || response.NextCursor == "" {
		t.Fatalf("unexpected first response: status=%d response_bytes=%d cursor=%t", first.Code, first.Body.Len(), response.NextCursor != "")
	}
	tampered := response.NextCursor[:len(response.NextCursor)-1] + "A"
	second := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"stable query","retrieval_mode":"keyword","cursor":"`+tampered+`","limit":1}`)
	assertProblem(t, second, http.StatusBadRequest, domain.ErrorCodeSearchCursorInvalid)
	if search.calls != 1 {
		t.Fatalf("tampered cursor should fail before search, calls=%d", search.calls)
	}
}

func TestHandlerSearchRejectsCrossRequestAndStaleCursor(t *testing.T) {
	search := &fakeSearchService{result: cursorSearchResult(2, "92000000-0000-4000-8000-000000000002")}
	handler := NewHandler(search, &fakeEvidenceReferenceService{}, mustCursorCodec(t, 'a'))
	router := retrievalTestRouter(handler)
	first := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"stable query","retrieval_mode":"keyword","limit":1}`)
	var response searchResponse
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &response) != nil || response.NextCursor == "" {
		t.Fatalf("unexpected first response: status=%d response_bytes=%d cursor=%t", first.Code, first.Body.Len(), response.NextCursor != "")
	}

	crossRequest := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"other query","retrieval_mode":"keyword","cursor":"`+response.NextCursor+`","limit":1}`)
	assertProblem(t, crossRequest, http.StatusBadRequest, domain.ErrorCodeSearchCursorInvalid)
	if search.calls != 1 {
		t.Fatalf("cross-request cursor should fail before search, calls=%d", search.calls)
	}

	search.result = cursorSearchResult(2, "92000000-0000-4000-8000-000000000099")
	stale := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"stable query","retrieval_mode":"keyword","cursor":"`+response.NextCursor+`","limit":1}`)
	assertProblem(t, stale, http.StatusConflict, domain.ErrorCodeSearchCursorStale)
}

func TestHandlerOpensSourceVersionAndImmutableSpan(t *testing.T) {
	reference := handlerSourceSpanReference()
	evidence := &fakeEvidenceReferenceService{
		sourceVersion: reference.SourceVersion,
		sourceSpan:    application.SourceSpanView{Reference: reference, Excerpt: "immutable excerpt", ExcerptTruncated: true},
	}
	handler := NewHandler(&fakeSearchService{}, evidence, mustCursorCodec(t, 'a'))
	router := retrievalTestRouter(handler)

	versionPath := "/workspaces/" + string(reference.SourceVersion.WorkspaceID) + "/source-versions/" + string(reference.SourceVersion.SourceVersionID)
	versionResponse := httptest.NewRecorder()
	router.ServeHTTP(versionResponse, httptest.NewRequest(http.MethodGet, versionPath, nil))
	if versionResponse.Code != http.StatusOK {
		t.Fatalf("source version status=%d response_bytes=%d", versionResponse.Code, versionResponse.Body.Len())
	}
	var version sourceVersionResponse
	if err := json.Unmarshal(versionResponse.Body.Bytes(), &version); err != nil || version.SourceVersionID != string(reference.SourceVersion.SourceVersionID) || version.RelativePath != "docs/evidence.md" ||
		version.IngestionStatus != "parsed" || version.WorkflowStatus != "running" || version.IndexStatus != "included" {
		t.Fatalf("unexpected source version response: source_version_id=%q relative_path=%q err=%v", version.SourceVersionID, version.RelativePath, err)
	}

	spanResponse := httptest.NewRecorder()
	router.ServeHTTP(spanResponse, httptest.NewRequest(http.MethodGet, versionPath+"/spans/"+string(reference.Span.ID), nil))
	if spanResponse.Code != http.StatusOK {
		t.Fatalf("source span status=%d response_bytes=%d", spanResponse.Code, spanResponse.Body.Len())
	}
	var span sourceSpanResponse
	if err := json.Unmarshal(spanResponse.Body.Bytes(), &span); err != nil || span.SpanID != string(reference.Span.ID) || span.Excerpt != "immutable excerpt" || !span.ExcerptTruncated {
		t.Fatalf("unexpected source span response: span_id=%q excerpt_bytes=%d truncated=%t err=%v", span.SpanID, len(span.Excerpt), span.ExcerptTruncated, err)
	}
	if evidence.workspaceID != reference.SourceVersion.WorkspaceID || evidence.sourceVersionID != reference.SourceVersion.SourceVersionID || evidence.spanID != reference.Span.ID {
		t.Fatalf("handler did not preserve evidence scope: workspace_id=%q source_version_id=%q span_id=%q", evidence.workspaceID, evidence.sourceVersionID, evidence.spanID)
	}
}

func TestHandlerUnavailableDependenciesReturnProblem(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	router := retrievalTestRouter(handler)
	search := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q"}`)
	assertProblem(t, search, http.StatusServiceUnavailable, "RETRIEVAL_SEARCH_SERVICE_UNAVAILABLE")

	version := httptest.NewRecorder()
	router.ServeHTTP(version, httptest.NewRequest(http.MethodGet, "/workspaces/92000000-0000-4000-8000-000000000001/source-versions/92000000-0000-4000-8000-000000000003", nil))
	assertProblem(t, version, http.StatusServiceUnavailable, "RETRIEVAL_EVIDENCE_REFERENCE_SERVICE_UNAVAILABLE")
}

func TestHandlerMapsSearchAndEvidenceFailuresWithoutLeakingDetails(t *testing.T) {
	searchErrors := []struct {
		kind   foundation.ErrorKind
		status int
	}{
		{kind: foundation.ErrorNotFound, status: http.StatusNotFound},
		{kind: foundation.ErrorVersionConflict, status: http.StatusConflict},
		{kind: foundation.ErrorConsistencyViolation, status: http.StatusConflict},
		{kind: foundation.ErrorRetryableFailure, status: http.StatusServiceUnavailable},
	}
	for _, test := range searchErrors {
		search := &fakeSearchService{err: foundation.NewError(test.kind, "RETRIEVAL_TEST_FAILURE", test.kind == foundation.ErrorRetryableFailure, errors.New("private database detail"))}
		handler := NewHandler(search, &fakeEvidenceReferenceService{}, mustCursorCodec(t, 'a'))
		router := retrievalTestRouter(handler)
		response := performJSONRequest(t, router, http.MethodPost, "/search", `{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q","retrieval_mode":"keyword"}`)
		assertProblem(t, response, test.status, "RETRIEVAL_TEST_FAILURE")
		if strings.Contains(response.Body.String(), "private database detail") {
			t.Fatalf("problem leaked private cause: status=%d response_bytes=%d", response.Code, response.Body.Len())
		}
	}

	evidence := &fakeEvidenceReferenceService{err: foundation.NewError(foundation.ErrorNotFound, "RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND", false, errors.New("other workspace identity"))}
	handler := NewHandler(&fakeSearchService{}, evidence, mustCursorCodec(t, 'a'))
	router := retrievalTestRouter(handler)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/workspaces/93000000-0000-4000-8000-000000000001/source-versions/93000000-0000-4000-8000-000000000003", nil))
	assertProblem(t, response, http.StatusNotFound, "RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND")
	if strings.Contains(response.Body.String(), "other workspace identity") {
		t.Fatalf("evidence problem leaked private cause: status=%d response_bytes=%d", response.Code, response.Body.Len())
	}
}

func performJSONRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func retrievalTestRouter(handler *Handler) http.Handler {
	router := gin.New()
	handler.Routes(router)
	return router
}

func assertProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d response_bytes=%d", response.Code, status, response.Body.Len())
	}
	var problem struct {
		ErrorCode string `json:"error_code"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if code != "" && problem.ErrorCode != code {
		t.Fatalf("problem code=%s want=%s", problem.ErrorCode, code)
	}
}

func firstChunkID(items []evidenceResponse) string {
	if len(items) == 0 {
		return ""
	}
	return items[0].ChunkID
}

func firstHeadingBytes(items []struct {
	HeadingPath json.RawMessage `json:"heading_path"`
}) int {
	if len(items) == 0 {
		return 0
	}
	return len(items[0].HeadingPath)
}

func handlerSourceSpanReference() domain.SourceSpanReference {
	return domain.SourceSpanReference{
		SourceVersion: domain.SourceVersionReference{
			WorkspaceID: "93000000-0000-4000-8000-000000000001", SourceID: "93000000-0000-4000-8000-000000000002",
			SourceVersionID: "93000000-0000-4000-8000-000000000003", ContentArtifactID: "93000000-0000-4000-8000-000000000004",
			SourceType: "local_file", LogicalName: "evidence.md", RelativePath: "docs/evidence.md",
			ContentHash: strings.Repeat("a", 64), ByteSize: 64, MediaType: "text/markdown", SecurityStatus: "passed",
			IngestionStatus: "parsed", WorkflowStatus: "running", IndexStatus: "included",
			CapturedAt: time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC),
		},
		ParseProjectionID: "93000000-0000-4000-8000-000000000005",
		Span:              domain.EvidenceSpan{ID: "93000000-0000-4000-8000-000000000006", StartLine: 2, EndLine: 3, StartByte: 5, EndByte: 25},
		SpanType:          "section", Selector: json.RawMessage(`{"heading":["Evidence"]}`), ExcerptHash: strings.Repeat("b", 64),
		ParserVersion: "goldmark-1", SchemaVersion: "parse-v1",
	}
}

type fakeSearchService struct {
	result  domain.SearchResult
	err     error
	run     func(domain.SearchRequest) (domain.SearchResult, error)
	request domain.SearchRequest
	calls   int
}

func (f *fakeSearchService) Search(_ context.Context, request domain.SearchRequest) (domain.SearchResult, error) {
	f.calls++
	f.request = request
	if f.run != nil {
		return f.run(request)
	}
	return f.result, f.err
}

type fakeEvidenceReferenceService struct {
	sourceVersion   domain.SourceVersionReference
	sourceSpan      application.SourceSpanView
	err             error
	workspaceID     foundation.ID
	sourceVersionID foundation.ID
	spanID          foundation.ID
}

func (f *fakeEvidenceReferenceService) GetSourceVersion(_ context.Context, workspaceID, sourceVersionID foundation.ID) (domain.SourceVersionReference, error) {
	f.workspaceID, f.sourceVersionID = workspaceID, sourceVersionID
	return f.sourceVersion, f.err
}

func (f *fakeEvidenceReferenceService) GetSourceSpan(_ context.Context, workspaceID, sourceVersionID, spanID foundation.ID) (application.SourceSpanView, error) {
	f.workspaceID, f.sourceVersionID, f.spanID = workspaceID, sourceVersionID, spanID
	return f.sourceSpan, f.err
}
