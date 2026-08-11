package knowledgehttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/gin-gonic/gin"
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
	if page.WorkspaceID != string(event.WorkspaceID) || len(page.Items) != 1 {
		t.Fatalf("page=%#v", page)
	}
	listItem, ok := page.Items[0].(map[string]any)
	if !ok || listItem["id"] != string(event.ID) {
		t.Fatalf("page item=%#v", page.Items[0])
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

func TestKnowledgeHTTPPreservesV1WireFieldSets(t *testing.T) {
	event := knowledgeHTTPEvent()
	report := knowledgeHTTPReport(t, event)
	router := knowledgeHTTPRouter(&timelineServiceStub{event: event}, &impactServiceStub{report: report})

	eventResponse := serveKnowledgeHTTP(router, http.MethodGet, "/workspaces/"+string(event.WorkspaceID)+"/timeline/"+string(event.ID), "", nil)
	if eventResponse.Code != http.StatusOK {
		t.Fatalf("v1 event status=%d body=%s", eventResponse.Code, eventResponse.Body.String())
	}
	eventBody := decodeKnowledgeJSONObject(t, eventResponse)
	requireKnowledgeExactKeys(t, eventBody,
		"id", "workspace_id", "event_type", "aggregate_type", "aggregate_id", "source_event_ref", "source_ref", "event_version",
		"schema_version", "summary", "payload", "correlation", "occurred_at", "created_at",
	)
	if got := decodeKnowledgeJSONString(t, eventBody["schema_version"]); got != domain.KnowledgeEventSchemaVersion {
		t.Fatalf("v1 event schema=%q", got)
	}

	reportResponse := serveKnowledgeHTTP(router, http.MethodGet, "/workspaces/"+string(event.WorkspaceID)+"/impact-reports/"+string(report.ID), "", nil)
	if reportResponse.Code != http.StatusOK {
		t.Fatalf("v1 report status=%d body=%s", reportResponse.Code, reportResponse.Body.String())
	}
	reportBody := decodeKnowledgeJSONObject(t, reportResponse)
	requireKnowledgeExactKeys(t, reportBody,
		"id", "workspace_id", "source_event_id", "source_event_ref", "source_event_version", "status", "objects", "summary",
		"fingerprint", "schema_version", "generated_at", "created_at", "version",
	)
	if got := decodeKnowledgeJSONString(t, reportBody["schema_version"]); got != domain.ImpactReportSchemaVersion {
		t.Fatalf("v1 report schema=%q", got)
	}
	objects := decodeKnowledgeJSONArray(t, reportBody["objects"])
	if len(objects) != 1 {
		t.Fatalf("v1 report objects=%d", len(objects))
	}
	requireKnowledgeExactKeys(t, decodeKnowledgeRawObject(t, objects[0]), "type", "id", "workspace_id", "version", "action", "reason", "requires_proposal")
}

func TestKnowledgeHTTPWritesVersionedV2EventAndImpactContracts(t *testing.T) {
	event := knowledgeHTTPV2ArtifactEvent()
	report := knowledgeHTTPV2Report(t, event)
	router := knowledgeHTTPRouter(&timelineServiceStub{event: event}, &impactServiceStub{report: report})

	eventResponse := serveKnowledgeHTTP(router, http.MethodGet, "/workspaces/"+string(event.WorkspaceID)+"/timeline/"+string(event.ID), "", nil)
	if eventResponse.Code != http.StatusOK {
		t.Fatalf("v2 event status=%d body=%s", eventResponse.Code, eventResponse.Body.String())
	}
	eventBody := decodeKnowledgeJSONObject(t, eventResponse)
	requireKnowledgeExactKeys(t, eventBody,
		"id", "workspace_id", "event_type", "aggregate_type", "aggregate_id", "source_event_ref", "source_ref", "event_version",
		"schema_version", "summary", "payload", "correlation", "operator", "owner_binding", "occurred_at", "created_at",
	)
	if got := decodeKnowledgeJSONString(t, eventBody["schema_version"]); got != domain.KnowledgeEventSchemaVersionV2 {
		t.Fatalf("v2 event schema=%q", got)
	}
	operator := decodeKnowledgeRawObject(t, eventBody["operator"])
	requireKnowledgeExactKeys(t, operator, "type", "id")
	if got := decodeKnowledgeJSONString(t, operator["type"]); got != string(domain.EventOperatorUser) {
		t.Fatalf("operator type=%q", got)
	}
	correlation := decodeKnowledgeRawObject(t, eventBody["correlation"])
	requireKnowledgeExactKeys(t, correlation, "workflow_run_id")
	ownerBinding := decodeKnowledgeRawObject(t, eventBody["owner_binding"])
	requireKnowledgeExactKeys(t, ownerBinding, "artifact")
	requireKnowledgeExactKeys(t, decodeKnowledgeRawObject(t, ownerBinding["artifact"]), "artifact_id", "artifact_version", "revision_id", "revision_no", "content_hash")

	reportResponse := serveKnowledgeHTTP(router, http.MethodGet, "/workspaces/"+string(event.WorkspaceID)+"/impact-reports/"+string(report.ID), "", nil)
	if reportResponse.Code != http.StatusOK {
		t.Fatalf("v2 report status=%d body=%s", reportResponse.Code, reportResponse.Body.String())
	}
	reportBody := decodeKnowledgeJSONObject(t, reportResponse)
	requireKnowledgeExactKeys(t, reportBody,
		"id", "workspace_id", "source_event_id", "source_event_ref", "source_event_version", "status", "objects", "summary",
		"fingerprint", "schema_version", "analysis_version", "supersedes_report_id", "superseded_by_report_id", "generated_at", "created_at", "version",
	)
	if got := decodeKnowledgeJSONString(t, reportBody["schema_version"]); got != domain.ImpactReportSchemaVersionV2 {
		t.Fatalf("v2 report schema=%q", got)
	}
	if got := decodeKnowledgeJSONString(t, reportBody["analysis_version"]); got != string(domain.ImpactAnalysisVersionV2) {
		t.Fatalf("v2 analysis version=%q", got)
	}
	if got := decodeKnowledgeJSONString(t, reportBody["supersedes_report_id"]); got != string(*report.SupersedesReportID) {
		t.Fatalf("supersedes report=%q", got)
	}
	if string(reportBody["superseded_by_report_id"]) != "null" {
		t.Fatalf("superseded_by_report_id=%s want null", reportBody["superseded_by_report_id"])
	}
	objects := decodeKnowledgeJSONArray(t, reportBody["objects"])
	if len(objects) != 2 {
		t.Fatalf("v2 report objects=%d", len(objects))
	}
	byType := make(map[string]map[string]json.RawMessage, len(objects))
	for _, raw := range objects {
		object := decodeKnowledgeRawObject(t, raw)
		byType[decodeKnowledgeJSONString(t, object["type"])] = object
	}
	artifact, artifactFound := byType[string(domain.ImpactObjectArtifact)]
	if !artifactFound {
		t.Fatalf("artifact object is missing: %#v", byType)
	}
	requireKnowledgeExactKeys(t, artifact, "type", "id", "workspace_id", "version", "action", "reason", "requires_proposal", "artifact_binding")
	requireKnowledgeExactKeys(t, decodeKnowledgeRawObject(t, artifact["artifact_binding"]), "artifact_id", "artifact_version", "revision_id", "revision_no", "content_hash")
	reviewCard, reviewCardFound := byType[string(domain.ImpactObjectReviewCard)]
	if !reviewCardFound {
		t.Fatalf("review-card object is missing: %#v", byType)
	}
	requireKnowledgeExactKeys(t, reviewCard, "type", "id", "workspace_id", "version", "action", "reason", "requires_proposal", "review_card_binding")
	requireKnowledgeExactKeys(t, decodeKnowledgeRawObject(t, reviewCard["review_card_binding"]), "card_id", "card_version", "status", "fingerprint", "claim_id", "evidence_binding_fingerprint")
}

func TestTimelineListPreservesMixedEventWireVersions(t *testing.T) {
	v1Event := knowledgeHTTPEvent()
	v2OwnerEvent := knowledgeHTTPV2ArtifactEvent()
	v2Event := knowledgeHTTPEvent()
	v2Event.ID = knowledgeHTTPID(4)
	v2Event.EventVersion = 2
	v2Event.SchemaVersion = domain.KnowledgeEventSchemaVersionV2
	v2Event.Operator = &domain.EventOperator{Type: domain.EventOperatorUnknown}
	timeline := &timelineServiceStub{page: knowledgeapp.TimelinePage{
		WorkspaceID: v1Event.WorkspaceID,
		Items:       []domain.KnowledgeEvent{v1Event, v2OwnerEvent, v2Event},
	}}

	response := serveKnowledgeHTTP(knowledgeHTTPRouter(timeline, &impactServiceStub{}), http.MethodGet, "/workspaces/"+string(v1Event.WorkspaceID)+"/timeline", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("mixed timeline status=%d body=%s", response.Code, response.Body.String())
	}
	body := decodeKnowledgeJSONObject(t, response)
	requireKnowledgeExactKeys(t, body, "workspace_id", "items")
	items := decodeKnowledgeJSONArray(t, body["items"])
	if len(items) != 3 {
		t.Fatalf("mixed timeline items=%d", len(items))
	}

	v1Body := decodeKnowledgeRawObject(t, items[0])
	requireKnowledgeExactKeys(t, v1Body,
		"id", "workspace_id", "event_type", "aggregate_type", "aggregate_id", "source_event_ref", "source_ref", "event_version",
		"schema_version", "summary", "payload", "correlation", "occurred_at", "created_at",
	)
	v2OwnerBody := decodeKnowledgeRawObject(t, items[1])
	requireKnowledgeExactKeys(t, v2OwnerBody,
		"id", "workspace_id", "event_type", "aggregate_type", "aggregate_id", "source_event_ref", "source_ref", "event_version",
		"schema_version", "summary", "payload", "correlation", "operator", "owner_binding", "occurred_at", "created_at",
	)
	ownerBinding := decodeKnowledgeRawObject(t, v2OwnerBody["owner_binding"])
	requireKnowledgeExactKeys(t, ownerBinding, "artifact")
	v2Body := decodeKnowledgeRawObject(t, items[2])
	requireKnowledgeExactKeys(t, v2Body,
		"id", "workspace_id", "event_type", "aggregate_type", "aggregate_id", "source_event_ref", "source_ref", "event_version",
		"schema_version", "summary", "payload", "correlation", "operator", "owner_binding", "occurred_at", "created_at",
	)
	if string(v2Body["owner_binding"]) != "null" {
		t.Fatalf("non-owner v2 owner_binding=%s want null", v2Body["owner_binding"])
	}
	operator := decodeKnowledgeRawObject(t, v2Body["operator"])
	requireKnowledgeExactKeys(t, operator, "type")
	if got := decodeKnowledgeJSONString(t, operator["type"]); got != string(domain.EventOperatorUnknown) {
		t.Fatalf("non-owner v2 operator=%q", got)
	}
}

func TestKnowledgeHTTPFailsClosedOnInvalidVersionedWire(t *testing.T) {
	v2Event := knowledgeHTTPV2ArtifactEvent()
	v2Report := knowledgeHTTPV2Report(t, v2Event)

	invalidV1Event := v2Event
	invalidV1Event.SchemaVersion = domain.KnowledgeEventSchemaVersion
	invalidV2Event := v2Event
	invalidV2Event.OwnerBinding = nil
	invalidV1Report := v2Report
	invalidV1Report.AnalysisVersion = domain.ImpactAnalysisVersionV1
	invalidV1Report.SupersedesReportID = nil
	invalidV2Report := v2Report
	invalidV2Report.Objects = append([]domain.ImpactObject(nil), v2Report.Objects...)
	invalidV2Report.Objects[0].ArtifactBinding = nil
	invalidFingerprintReport := v2Report
	invalidFingerprintReport.Fingerprint = strings.Repeat("d", 64)

	for _, test := range []struct {
		name     string
		timeline *timelineServiceStub
		impact   *impactServiceStub
		path     string
	}{
		{
			name: "v1 event carries v2 fields", timeline: &timelineServiceStub{event: invalidV1Event}, impact: &impactServiceStub{},
			path: "/workspaces/" + string(v2Event.WorkspaceID) + "/timeline/" + string(v2Event.ID),
		},
		{
			name: "v2 owner event loses owner binding", timeline: &timelineServiceStub{event: invalidV2Event}, impact: &impactServiceStub{},
			path: "/workspaces/" + string(v2Event.WorkspaceID) + "/timeline/" + string(v2Event.ID),
		},
		{
			name: "v1 report contains v2 owner object", timeline: &timelineServiceStub{}, impact: &impactServiceStub{report: invalidV1Report},
			path: "/workspaces/" + string(v2Event.WorkspaceID) + "/impact-reports/" + string(v2Report.ID),
		},
		{
			name: "v2 artifact report loses owner binding", timeline: &timelineServiceStub{}, impact: &impactServiceStub{report: invalidV2Report},
			path: "/workspaces/" + string(v2Event.WorkspaceID) + "/impact-reports/" + string(v2Report.ID),
		},
		{
			name: "v2 report fingerprint drifts", timeline: &timelineServiceStub{}, impact: &impactServiceStub{report: invalidFingerprintReport},
			path: "/workspaces/" + string(v2Event.WorkspaceID) + "/impact-reports/" + string(v2Report.ID),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serveKnowledgeHTTP(knowledgeHTTPRouter(test.timeline, test.impact), http.MethodGet, test.path, "", nil)
			requireKnowledgeProblem(t, response, http.StatusInternalServerError, errorCodeHTTPResultInconsistent)
		})
	}
}

func TestKnowledgeHTTPMapsVersionedImpactAvailabilityAndKeepsHistoricalV1Readable(t *testing.T) {
	event := knowledgeHTTPEvent()
	report := knowledgeHTTPReport(t, event)
	selectorUnavailable := foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeImpactUnavailable, true, errors.New("selector projection is incomplete"))
	currentConflict := foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeImpactConflict, false, errors.New("current report binding drifted"))
	analysisPath := "/workspaces/" + string(event.WorkspaceID) + "/timeline/" + string(event.ID) + "/impact-analysis"

	for _, test := range []struct {
		name string
		err  error
		want int
		code string
	}{
		{name: "selector readiness unavailable", err: selectorUnavailable, want: http.StatusServiceUnavailable, code: domain.ErrorCodeImpactUnavailable},
		{name: "current impact conflict", err: currentConflict, want: http.StatusConflict, code: domain.ErrorCodeImpactConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			impact := &impactServiceStub{analyzeErr: test.err}
			response := serveKnowledgeHTTP(knowledgeHTTPRouter(&timelineServiceStub{}, impact), http.MethodPost, analysisPath, `{}`, map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"impact-versioned"}})
			requireKnowledgeProblem(t, response, test.want, test.code)
		})
	}

	impact := &impactServiceStub{report: report, analyzeErr: selectorUnavailable}
	historical := serveKnowledgeHTTP(knowledgeHTTPRouter(&timelineServiceStub{}, impact), http.MethodGet, "/workspaces/"+string(event.WorkspaceID)+"/impact-reports/"+string(report.ID), "", nil)
	if historical.Code != http.StatusOK {
		t.Fatalf("historical v1 report status=%d body=%s", historical.Code, historical.Body.String())
	}
	if impact.analyzeCalls != 0 || impact.getCalls != 1 {
		t.Fatalf("historical v1 read calls: analyze=%d get=%d", impact.analyzeCalls, impact.getCalls)
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
			router := gin.New()
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
	getCalls     int
	lastRequest  knowledgeapp.ImpactAnalysisRequest
}

func (service *impactServiceStub) Analyze(_ context.Context, request knowledgeapp.ImpactAnalysisRequest) (knowledgeapp.ImpactAnalysisResult, error) {
	service.analyzeCalls++
	service.lastRequest = request
	return service.result, service.analyzeErr
}

func (service *impactServiceStub) GetReport(_ context.Context, _, _ foundation.ID) (domain.ImpactReport, error) {
	service.getCalls++
	return service.report, service.getErr
}

func knowledgeHTTPRouter(timeline TimelineService, impact ImpactService) http.Handler {
	router := gin.New()
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

func knowledgeHTTPV2ArtifactEvent() domain.KnowledgeEvent {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	artifactID := knowledgeHTTPID(3)
	revisionID := knowledgeHTTPID(4)
	operatorID := knowledgeHTTPID(5)
	workflowRunID := knowledgeHTTPID(6)
	return domain.KnowledgeEvent{
		ID: knowledgeHTTPID(2), WorkspaceID: knowledgeHTTPID(1), EventType: domain.EventArtifactGenerated,
		AggregateType: domain.TimelineAggregateArtifact, AggregateID: &artifactID,
		SourceEventRef: "artifact-generated:3:4:v2", SourceRef: "artifact:3", EventVersion: 2,
		SchemaVersion: domain.KnowledgeEventSchemaVersionV2, Summary: "artifact revision generated", Payload: json.RawMessage(`{}`),
		Correlation: domain.EventCorrelation{WorkflowRunID: &workflowRunID},
		Operator:    &domain.EventOperator{Type: domain.EventOperatorUser, ID: &operatorID},
		OwnerBinding: &domain.EventOwnerBinding{Artifact: &domain.ArtifactImpactBinding{
			ArtifactID: artifactID, ArtifactVersion: 2, RevisionID: revisionID, RevisionNo: 2, ContentHash: strings.Repeat("a", 64),
		}},
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

func knowledgeHTTPV2Report(t *testing.T, event domain.KnowledgeEvent) domain.ImpactReport {
	t.Helper()
	artifactBinding := *event.OwnerBinding.Artifact
	reviewBinding := domain.ReviewCardImpactBinding{
		CardID: knowledgeHTTPID(9), CardVersion: 3, Status: "APPROVED", Fingerprint: strings.Repeat("b", 64),
		ClaimID: event.ID, EvidenceBindingFingerprint: strings.Repeat("c", 64),
	}
	objects := []domain.ImpactObject{
		{
			Type: domain.ImpactObjectArtifact, ID: artifactBinding.ArtifactID, WorkspaceID: event.WorkspaceID,
			Version: artifactBinding.ArtifactVersion, Action: domain.ImpactActionRegenerateArtifact,
			Reason: "artifact depends on changed evidence", RequiresProposal: true, ArtifactBinding: &artifactBinding,
		},
		{
			Type: domain.ImpactObjectReviewCard, ID: reviewBinding.CardID, WorkspaceID: event.WorkspaceID,
			Version: reviewBinding.CardVersion, Action: domain.ImpactActionRevalidateReviewCard,
			Reason: "review card evidence changed", RequiresProposal: true, ReviewCardBinding: &reviewBinding,
		},
	}
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), objects)
	if err != nil {
		t.Fatal(err)
	}
	supersedesReportID := knowledgeHTTPID(8)
	return domain.ImpactReport{
		ID: knowledgeHTTPID(7), WorkspaceID: event.WorkspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2, SupersedesReportID: &supersedesReportID,
		Status: domain.ImpactReportReady, Objects: objects, Summary: domain.SummarizeImpactObjects(objects), Fingerprint: fingerprint,
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

func decodeKnowledgeJSONObject(t *testing.T, response *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	return decodeKnowledgeRawObject(t, response.Body.Bytes())
}

func decodeKnowledgeRawObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode JSON object %s: %v", raw, err)
	}
	if object == nil {
		t.Fatalf("JSON object is null: %s", raw)
	}
	return object
}

func decodeKnowledgeJSONArray(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("decode JSON array %s: %v", raw, err)
	}
	if values == nil {
		t.Fatalf("JSON array is null: %s", raw)
	}
	return values
}

func decodeKnowledgeJSONString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode JSON string %s: %v", raw, err)
	}
	return value
}

func requireKnowledgeExactKeys(t *testing.T, object map[string]json.RawMessage, expected ...string) {
	t.Helper()
	if len(object) != len(expected) {
		t.Fatalf("JSON keys=%v want=%v", knowledgeJSONKeys(object), expected)
	}
	for _, key := range expected {
		if _, found := object[key]; !found {
			t.Fatalf("JSON key %q is missing from %v", key, knowledgeJSONKeys(object))
		}
	}
}

func knowledgeJSONKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
