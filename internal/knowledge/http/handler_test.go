package knowledgehttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/go-chi/chi/v5"
)

func TestTimelineListAndDetailRoutesReturnWorkspaceBoundProjection(t *testing.T) {
	event := knowledgeHTTPEvent()
	timeline := &timelineServiceStub{page: knowledgeapp.TimelinePage{WorkspaceID: event.WorkspaceID, Items: []domain.KnowledgeEvent{event}}, event: event}
	router := knowledgeHTTPRouter(timeline, &impactServiceStub{})

	list := serveKnowledgeHTTP(router, http.MethodGet, "/workspaces/"+string(event.WorkspaceID)+"/timeline?limit=1", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var page timelineListResponse
	decodeKnowledgeHTTP(t, list, &page)
	if page.WorkspaceID != string(event.WorkspaceID) || len(page.Items) != 1 || page.Items[0].ID != string(event.ID) {
		t.Fatalf("page=%#v", page)
	}

	detail := serveKnowledgeHTTP(router, http.MethodGet, "/workspaces/"+string(event.WorkspaceID)+"/timeline/"+string(event.ID), "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var item knowledgeEventResponse
	decodeKnowledgeHTTP(t, detail, &item)
	if item.SourceEventRef != event.SourceEventRef || item.SchemaVersion != domain.KnowledgeEventSchemaVersion {
		t.Fatalf("item=%#v", item)
	}
}

func TestTimelineListAcceptsAllFiltersAndRejectsAmbiguousQuery(t *testing.T) {
	event := knowledgeHTTPEvent()
	nextCursor := "opaque-next-cursor"
	timeline := &timelineServiceStub{page: knowledgeapp.TimelinePage{
		WorkspaceID: event.WorkspaceID,
		Items:       []domain.KnowledgeEvent{event},
		NextCursor:  nextCursor,
		HasMore:     true,
	}}
	router := knowledgeHTTPRouter(timeline, &impactServiceStub{})

	filtered := serveKnowledgeHTTP(router, http.MethodGet,
		"/workspaces/"+string(event.WorkspaceID)+"/timeline?event_type=CONFLICT_RESOLVED&event_type=VERSION_PUBLISHED&aggregate_type=CONFLICT&aggregate_id="+string(*event.AggregateID)+"&source_event_ref=conflict%3Aresolved%3A1&occurred_after=2026-07-22T00%3A00%3A00Z&occurred_before=2026-07-24T00%3A00%3A00Z&limit=2&cursor=opaque-cursor", "", nil)
	if filtered.Code != http.StatusOK {
		t.Fatalf("filtered list status=%d body=%s", filtered.Code, filtered.Body.String())
	}
	var page timelineListResponse
	decodeKnowledgeHTTP(t, filtered, &page)
	if page.NextCursor == nil || *page.NextCursor != nextCursor || len(page.Items) != 1 {
		t.Fatalf("filtered page=%#v", page)
	}
	request := timeline.lastListRequest
	if request.WorkspaceID != event.WorkspaceID || request.Limit != 2 || request.Cursor != "opaque-cursor" ||
		len(request.Filter.EventTypes) != 2 || request.Filter.EventTypes[0] != domain.EventConflictResolved || request.Filter.EventTypes[1] != domain.EventVersionPublished ||
		request.Filter.AggregateType != event.AggregateType || request.Filter.AggregateID == nil || *request.Filter.AggregateID != *event.AggregateID ||
		request.Filter.SourceEventRef != event.SourceEventRef || request.Filter.OccurredAfter == nil || request.Filter.OccurredBefore == nil ||
		request.Filter.OccurredAfter.UTC().Format(time.RFC3339) != "2026-07-22T00:00:00Z" || request.Filter.OccurredBefore.UTC().Format(time.RFC3339) != "2026-07-24T00:00:00Z" {
		t.Fatalf("parsed timeline request=%#v", request)
	}

	for _, rawQuery := range []string{
		"?unknown=value",
		"?limit=1&limit=2",
		"?aggregate_type=CONFLICT&aggregate_type=TOPIC",
		"?aggregate_id=" + string(*event.AggregateID) + "&aggregate_id=" + string(*event.AggregateID),
		"?source_event_ref=one&source_event_ref=two",
		"?occurred_after=2026-07-22T00%3A00%3A00Z&occurred_after=2026-07-23T00%3A00%3A00Z",
		"?occurred_before=2026-07-23T00%3A00%3A00Z&occurred_before=2026-07-24T00%3A00%3A00Z",
		"?cursor=first&cursor=second",
		"?cursor=" + strings.Repeat("a", maxCursorBytes+1),
	} {
		t.Run(rawQuery, func(t *testing.T) {
			before := timeline.listCalls
			response := serveKnowledgeHTTP(router, http.MethodGet, "/workspaces/"+string(event.WorkspaceID)+"/timeline"+rawQuery, "", nil)
			requireKnowledgeProblem(t, response, http.StatusBadRequest, domain.ErrorCodeTimelineInvalid)
			if timeline.listCalls != before {
				t.Fatalf("invalid query reached timeline service: %q", rawQuery)
			}
		})
	}
}

func TestTimelineListReturnsEmptyPage(t *testing.T) {
	event := knowledgeHTTPEvent()
	timeline := &timelineServiceStub{page: knowledgeapp.TimelinePage{WorkspaceID: event.WorkspaceID}}
	response := serveKnowledgeHTTP(knowledgeHTTPRouter(timeline, &impactServiceStub{}), http.MethodGet, "/workspaces/"+string(event.WorkspaceID)+"/timeline", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("empty page status=%d body=%s", response.Code, response.Body.String())
	}
	var page timelineListResponse
	decodeKnowledgeHTTP(t, response, &page)
	if page.WorkspaceID != string(event.WorkspaceID) || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("empty page=%#v", page)
	}
}

func TestImpactAnalyzeRequiresStrictJSONAndIdempotency(t *testing.T) {
	event := knowledgeHTTPEvent()
	report := knowledgeHTTPReport(t, event)
	impact := &impactServiceStub{result: knowledgeapp.ImpactAnalysisResult{Report: report, ProposalDrafts: []domain.ProposalDraft{knowledgeHTTPDraft(report)}, Replayed: false}, report: report}
	router := knowledgeHTTPRouter(&timelineServiceStub{}, impact)
	path := "/workspaces/" + string(event.WorkspaceID) + "/timeline/" + string(event.ID) + "/impact-analysis"

	missingKey := serveKnowledgeHTTP(router, http.MethodPost, path, `{}`, map[string][]string{"Content-Type": {"application/json"}})
	requireKnowledgeProblem(t, missingKey, http.StatusBadRequest, domain.ErrorCodeImpactInvalid)

	unknownField := serveKnowledgeHTTP(router, http.MethodPost, path, `{"unexpected":true}`, map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"impact-1"}})
	requireKnowledgeProblem(t, unknownField, http.StatusBadRequest, "INVALID_JSON")

	trailingJSON := serveKnowledgeHTTP(router, http.MethodPost, path, `{} {}`, map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"impact-1"}})
	requireKnowledgeProblem(t, trailingJSON, http.StatusBadRequest, "INVALID_JSON")

	duplicateKey := serveKnowledgeHTTP(router, http.MethodPost, path, `{}`, map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"impact-1", "impact-2"}})
	requireKnowledgeProblem(t, duplicateKey, http.StatusBadRequest, domain.ErrorCodeImpactInvalid)

	unsupported := serveKnowledgeHTTP(router, http.MethodPost, path, `{}`, map[string][]string{"Content-Type": {"text/plain"}, "Idempotency-Key": {"impact-1"}})
	requireKnowledgeProblem(t, unsupported, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE")

	created := serveKnowledgeHTTP(router, http.MethodPost, path, `{}`, map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"impact-1"}})
	if created.Code != http.StatusCreated {
		t.Fatalf("created status=%d body=%s", created.Code, created.Body.String())
	}
	var response impactAnalysisResponse
	decodeKnowledgeHTTP(t, created, &response)
	if response.Replayed || len(response.ProposalDrafts) != 1 || !response.ProposalDrafts[0].RequiresApproval || !response.ProposalDrafts[0].RequiresWriteAuthorization {
		t.Fatalf("response=%#v", response)
	}
	if impact.analyzeCalls != 1 || impact.lastRequest.IdempotencyKey != "impact-1" {
		t.Fatalf("analyze calls=%d request=%#v", impact.analyzeCalls, impact.lastRequest)
	}

	impact.result.Replayed = true
	replayed := serveKnowledgeHTTP(router, http.MethodPost, path, `{}`, map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"impact-1"}})
	if replayed.Code != http.StatusOK {
		t.Fatalf("replayed status=%d body=%s", replayed.Code, replayed.Body.String())
	}
}

func TestKnowledgeHTTPDoesNotExposeKnowledgeEventWriteRoute(t *testing.T) {
	timeline := &timelineServiceStub{}
	router := knowledgeHTTPRouter(timeline, &impactServiceStub{})
	response := serveKnowledgeHTTP(router, http.MethodPost, "/workspaces/"+string(knowledgeHTTPID(1))+"/timeline", `{}`, map[string][]string{"Content-Type": {"application/json"}})
	if response.Code >= 200 && response.Code < 300 {
		t.Fatalf("unexpected public event write route: status=%d body=%s", response.Code, response.Body.String())
	}
	if timeline.listCalls != 0 || timeline.getCalls != 0 {
		t.Fatalf("write attempt reached timeline service: %#v", timeline)
	}
}

func TestImpactReportNotFoundMapsToStableProblem(t *testing.T) {
	impact := &impactServiceStub{getErr: foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeImpactNotFound, false, errors.New("missing"))}
	router := knowledgeHTTPRouter(&timelineServiceStub{}, impact)
	response := serveKnowledgeHTTP(router, http.MethodGet, "/workspaces/"+string(knowledgeHTTPID(1))+"/impact-reports/"+string(knowledgeHTTPID(6)), "", nil)
	requireKnowledgeProblem(t, response, http.StatusNotFound, domain.ErrorCodeImpactNotFound)
}

func TestKnowledgeHTTPMapsNotFoundConflictUnavailableAndTimeout(t *testing.T) {
	event := knowledgeHTTPEvent()
	notFound := foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeTimelineNotFound, false, errors.New("missing"))
	unavailable := foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeImpactUnavailable, true, errors.New("unavailable"))
	conflict := foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeImpactConflict, false, errors.New("conflict"))
	analysisPath := "/workspaces/" + string(event.WorkspaceID) + "/timeline/" + string(event.ID) + "/impact-analysis"

	for _, test := range []struct {
		name     string
		timeline *timelineServiceStub
		impact   *impactServiceStub
		method   string
		path     string
		body     string
		headers  map[string][]string
		status   int
		code     string
	}{
		{name: "timeline not found", timeline: &timelineServiceStub{getErr: notFound}, impact: &impactServiceStub{}, method: http.MethodGet, path: "/workspaces/" + string(event.WorkspaceID) + "/timeline/" + string(event.ID), status: http.StatusNotFound, code: domain.ErrorCodeTimelineNotFound},
		{name: "impact conflict", timeline: &timelineServiceStub{}, impact: &impactServiceStub{analyzeErr: conflict}, method: http.MethodPost, path: analysisPath, body: `{}`, headers: map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"impact-conflict"}}, status: http.StatusConflict, code: domain.ErrorCodeImpactConflict},
		{name: "impact unavailable", timeline: &timelineServiceStub{}, impact: &impactServiceStub{getErr: unavailable}, method: http.MethodGet, path: "/workspaces/" + string(event.WorkspaceID) + "/impact-reports/" + string(knowledgeHTTPID(6)), status: http.StatusServiceUnavailable, code: domain.ErrorCodeImpactUnavailable},
		{name: "timeline timeout", timeline: &timelineServiceStub{listErr: context.DeadlineExceeded}, impact: &impactServiceStub{}, method: http.MethodGet, path: "/workspaces/" + string(event.WorkspaceID) + "/timeline", status: http.StatusServiceUnavailable, code: domain.ErrorCodeTimelineUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serveKnowledgeHTTP(knowledgeHTTPRouter(test.timeline, test.impact), test.method, test.path, test.body, test.headers)
			requireKnowledgeProblem(t, response, test.status, test.code)
		})
	}
}

func TestKnowledgeHTTPHandlerTimeoutCancelsDownstream(t *testing.T) {
	event := knowledgeHTTPEvent()

	for _, test := range []struct {
		name     string
		method   string
		path     string
		body     string
		headers  map[string][]string
		wantCall string
	}{
		{name: "timeline list", method: http.MethodGet, path: "/workspaces/" + string(event.WorkspaceID) + "/timeline", wantCall: "List"},
		{name: "timeline event detail", method: http.MethodGet, path: "/workspaces/" + string(event.WorkspaceID) + "/timeline/" + string(event.ID), wantCall: "Get"},
		{name: "impact analysis", method: http.MethodPost, path: "/workspaces/" + string(event.WorkspaceID) + "/timeline/" + string(event.ID) + "/impact-analysis", body: `{}`, headers: map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"impact-timeout"}}, wantCall: "Analyze"},
		{name: "impact report detail", method: http.MethodGet, path: "/workspaces/" + string(event.WorkspaceID) + "/impact-reports/" + string(knowledgeHTTPID(6)), wantCall: "GetReport"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &cancelAwareKnowledgeServiceStub{}
			router := chi.NewRouter()
			NewHandler(service, service, 10*time.Millisecond).Routes(router)

			response := serveKnowledgeHTTP(router, test.method, test.path, test.body, test.headers)

			requireKnowledgeProblem(t, response, http.StatusServiceUnavailable, domain.ErrorCodeTimelineUnavailable)
			if service.called != test.wantCall {
				t.Fatalf("called=%q want=%q", service.called, test.wantCall)
			}
			if !errors.Is(service.contextErr, context.DeadlineExceeded) {
				t.Fatalf("downstream context error=%v want deadline exceeded", service.contextErr)
			}
		})
	}
}

type cancelAwareKnowledgeServiceStub struct {
	called     string
	contextErr error
}

func (service *cancelAwareKnowledgeServiceStub) List(ctx context.Context, _ knowledgeapp.TimelineListRequest) (knowledgeapp.TimelinePage, error) {
	return knowledgeapp.TimelinePage{}, service.waitForCancellation(ctx, "List")
}

func (service *cancelAwareKnowledgeServiceStub) Get(ctx context.Context, _, _ foundation.ID) (domain.KnowledgeEvent, error) {
	return domain.KnowledgeEvent{}, service.waitForCancellation(ctx, "Get")
}

func (service *cancelAwareKnowledgeServiceStub) Analyze(ctx context.Context, _ knowledgeapp.ImpactAnalysisRequest) (knowledgeapp.ImpactAnalysisResult, error) {
	return knowledgeapp.ImpactAnalysisResult{}, service.waitForCancellation(ctx, "Analyze")
}

func (service *cancelAwareKnowledgeServiceStub) GetReport(ctx context.Context, _, _ foundation.ID) (domain.ImpactReport, error) {
	return domain.ImpactReport{}, service.waitForCancellation(ctx, "GetReport")
}

func (service *cancelAwareKnowledgeServiceStub) waitForCancellation(ctx context.Context, called string) error {
	service.called = called
	<-ctx.Done()
	service.contextErr = ctx.Err()
	return service.contextErr
}

type timelineServiceStub struct {
	page            knowledgeapp.TimelinePage
	event           domain.KnowledgeEvent
	listErr         error
	getErr          error
	listCalls       int
	getCalls        int
	lastListRequest knowledgeapp.TimelineListRequest
}

func (service *timelineServiceStub) List(_ context.Context, request knowledgeapp.TimelineListRequest) (knowledgeapp.TimelinePage, error) {
	service.listCalls++
	service.lastListRequest = request
	return service.page, service.listErr
}

func (service *timelineServiceStub) Get(_ context.Context, _, _ foundation.ID) (domain.KnowledgeEvent, error) {
	service.getCalls++
	return service.event, service.getErr
}

type impactServiceStub struct {
	result       knowledgeapp.ImpactAnalysisResult
	report       domain.ImpactReport
	getErr       error
	analyzeErr   error
	analyzeCalls int
	lastRequest  knowledgeapp.ImpactAnalysisRequest
}

func (service *impactServiceStub) Analyze(_ context.Context, request knowledgeapp.ImpactAnalysisRequest) (knowledgeapp.ImpactAnalysisResult, error) {
	service.analyzeCalls++
	service.lastRequest = request
	return service.result, service.analyzeErr
}

func (service *impactServiceStub) GetReport(_ context.Context, _, _ foundation.ID) (domain.ImpactReport, error) {
	return service.report, service.getErr
}

func knowledgeHTTPRouter(timeline TimelineService, impact ImpactService) http.Handler {
	router := chi.NewRouter()
	NewHandler(timeline, impact, time.Second).Routes(router)
	return router
}

func serveKnowledgeHTTP(handler http.Handler, method, path, body string, headers map[string][]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requireKnowledgeProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var problem httpapi.Problem
	decodeKnowledgeHTTP(t, response, &problem)
	if problem.ErrorCode != code {
		t.Fatalf("problem=%#v want code=%s", problem, code)
	}
}

func decodeKnowledgeHTTP(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatal(err)
	}
}

func knowledgeHTTPEvent() domain.KnowledgeEvent {
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	aggregateID := knowledgeHTTPID(2)
	return domain.KnowledgeEvent{
		ID: knowledgeHTTPID(3), WorkspaceID: knowledgeHTTPID(1), EventType: domain.EventConflictResolved,
		AggregateType: domain.TimelineAggregateConflict, AggregateID: &aggregateID, SourceEventRef: "conflict:resolved:1", SourceRef: "conflict:2",
		EventVersion: 1, SchemaVersion: domain.KnowledgeEventSchemaVersion, Summary: "conflict resolved", Payload: json.RawMessage(`{}`),
		OccurredAt: now, CreatedAt: now,
	}
}

func knowledgeHTTPReport(t *testing.T, event domain.KnowledgeEvent) domain.ImpactReport {
	t.Helper()
	objects := []domain.ImpactObject{{Type: domain.ImpactObjectConflict, ID: knowledgeHTTPID(4), WorkspaceID: event.WorkspaceID, Version: 2, Action: domain.ImpactActionResolveConflict, Reason: "conflict requires review", RequiresProposal: true}}
	fingerprint, err := domain.ComputeImpactFingerprint(event.ID, 1, objects)
	if err != nil {
		t.Fatal(err)
	}
	return domain.ImpactReport{
		ID: knowledgeHTTPID(6), WorkspaceID: event.WorkspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: 1, Status: domain.ImpactReportReady, Objects: objects, Summary: domain.SummarizeImpactObjects(objects), Fingerprint: fingerprint,
		GeneratedAt: event.CreatedAt, CreatedAt: event.CreatedAt, Version: 1,
	}
}

func knowledgeHTTPDraft(report domain.ImpactReport) domain.ProposalDraft {
	object := report.Objects[0]
	return domain.ProposalDraft{
		WorkspaceID: report.WorkspaceID, SourceEventID: report.SourceEventID, Operation: string(object.Action), TargetType: object.Type,
		TargetID: object.ID, BaseVersion: object.Version, Reason: object.Reason, RequiresApproval: true, RequiresWriteAuthorization: true,
	}
}

func knowledgeHTTPID(seed int) foundation.ID {
	return foundation.ID("37000000-0000-4000-8000-00000000000" + strconv.Itoa(seed))
}
