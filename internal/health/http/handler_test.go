package healthhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/go-chi/chi/v5"
)

const healthHTTPWorkspaceID = foundation.ID("10000000-0000-4000-8000-000000000001")

const (
	healthHTTPOtherWorkspaceID foundation.ID = "10000000-0000-4000-8000-000000000002"
	healthHTTPIssueID          foundation.ID = "20000000-0000-4000-8000-000000000001"
	healthHTTPScanID           foundation.ID = "30000000-0000-4000-8000-000000000001"
	healthHTTPScheduleID       foundation.ID = "40000000-0000-4000-8000-000000000001"
)

func TestHealthSummaryUsesSnakeCasePublicContract(t *testing.T) {
	codec, err := application.NewIssueCursorCodec([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	read, err := application.NewIssueReadService(healthReadFake{}, codec)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(read, nil, nil, nil, nil).Routes(router)
	request := httptest.NewRequest(http.MethodGet, "/health/summary?workspace_id="+string(healthHTTPWorkspaceID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, fragment := range []string{`"workspace_id"`, `"open_count":2`, `"open_by_severity"`, `"open_by_type"`, `"trend":[{"date":"2026-07-16","detected_count":0,"resolved_count":0}`} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("missing %s in %s", fragment, response.Body.String())
		}
	}
	if strings.Contains(response.Body.String(), "WorkspaceID") {
		t.Fatalf("domain field leaked into public JSON: %s", response.Body.String())
	}
	var payload struct {
		Trend []map[string]json.RawMessage `json:"trend"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Trend) != application.HealthTrendDays {
		t.Fatalf("trend points=%d body=%s", len(payload.Trend), response.Body.String())
	}
	for index, point := range payload.Trend {
		if len(point) != 3 || point["date"] == nil || point["detected_count"] == nil || point["resolved_count"] == nil {
			t.Fatalf("trend[%d] is not strict: %#v", index, point)
		}
	}
}

func TestHealthRoutesFailClosedPerCapability(t *testing.T) {
	router := chi.NewRouter()
	NewHandler(nil, nil, nil, nil, nil).Routes(router)
	for _, target := range []string{
		"/health/summary?workspace_id=" + string(healthHTTPWorkspaceID),
		"/health/scans/20000000-0000-4000-8000-000000000001?workspace_id=" + string(healthHTTPWorkspaceID),
		"/health/schedules?workspace_id=" + string(healthHTTPWorkspaceID) + "&schedule_id=30000000-0000-4000-8000-000000000001",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "UNAVAILABLE") {
			t.Fatalf("target=%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}

func TestHealthDecisionRequiresIdempotencyKeyBeforeMutation(t *testing.T) {
	router := chi.NewRouter()
	decisions := &healthDecisionFake{}
	NewHandler(nil, nil, nil, nil, decisions).Routes(router)
	body := `{"workspace_id":"` + string(healthHTTPWorkspaceID) + `","expected_version":1,"action":"ACKNOWLEDGE"}`
	request := httptest.NewRequest(http.MethodPost, "/health/issues/20000000-0000-4000-8000-000000000001/decisions?workspace_id="+string(healthHTTPWorkspaceID), strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || decisions.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, decisions.calls, response.Body.String())
	}
}

func TestHealthIdempotencyKeyRejectsRepeatedAndWhitespaceValues(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "repeated", values: []string{"repair-1", "repair-2"}},
		{name: "ascii whitespace", values: []string{"repair key"}},
		{name: "unicode whitespace", values: []string{"repair\u00a0key"}},
		{name: "invalid utf8", values: []string{string([]byte{'r', 0xff})}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/health/issues/"+string(healthHTTPIssueID)+"/repair-proposals?workspace_id="+string(healthHTTPWorkspaceID), nil)
			request.Header["Idempotency-Key"] = test.values
			response := httptest.NewRecorder()
			newHealthRouter(NewHandler(nil, nil, nil, nil, nil)).ServeHTTP(response, request)
			problem := requireHealthProblem(t, response)
			if response.Code != http.StatusBadRequest || problem.ErrorCode != "HEALTH_IDEMPOTENCY_KEY_REQUIRED" {
				t.Fatalf("status=%d problem=%+v body=%s", response.Code, problem, response.Body.String())
			}
		})
	}
}

func TestHealthBodyRoutesUseStrictJSONAndMediaType(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		key    string
	}{
		{name: "decision", method: http.MethodPost, path: "/health/issues/" + string(healthHTTPIssueID) + "/decisions?workspace_id=" + string(healthHTTPWorkspaceID), body: `{"workspace_id":"` + string(healthHTTPWorkspaceID) + `","expected_version":1,"action":"ACKNOWLEDGE"}`, key: "decision-1"},
		{name: "scan", method: http.MethodPost, path: "/health/scans", body: `{"workspace_id":"` + string(healthHTTPWorkspaceID) + `","scope":{"type":"WORKSPACE","ref":"` + string(healthHTTPWorkspaceID) + `","version":1,"schema_version":"health-scope/v1","hash":""},"coverage":[{"detector_id":"orphan","detector_version":"v1"}],"max_items":100,"prevent_scope_concurrency":true}`, key: "scan-1"},
		{name: "schedule", method: http.MethodPut, path: "/health/schedules", body: `{"workspace_id":"` + string(healthHTTPWorkspaceID) + `","scope":{"type":"WORKSPACE","ref":"` + string(healthHTTPWorkspaceID) + `","version":1,"schema_version":"health-scope/v1","hash":""},"cadence":"DISABLED","timezone":"UTC","max_items":100}`, key: "schedule-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			variants := []struct {
				name        string
				body        string
				contentType string
				status      int
				code        string
			}{
				{name: "unknown", body: strings.TrimSuffix(test.body, "}") + `,"unknown":true}`, contentType: "application/json", status: http.StatusBadRequest, code: "INVALID_JSON"},
				{name: "duplicate", body: strings.Replace(test.body, `"workspace_id":"`+string(healthHTTPWorkspaceID)+`"`, `"workspace_id":"`+string(healthHTTPWorkspaceID)+`","workspace_id":"`+string(healthHTTPWorkspaceID)+`"`, 1), contentType: "application/json", status: http.StatusBadRequest, code: "INVALID_JSON"},
				{name: "trailing", body: test.body + `{}`, contentType: "application/json", status: http.StatusBadRequest, code: "INVALID_JSON"},
				{name: "oversized", body: test.body + strings.Repeat(" ", maxHealthBody-len(test.body)+1), contentType: "application/json", status: http.StatusBadRequest, code: "INVALID_JSON"},
				{name: "wrong media type", body: test.body, contentType: "application/problem+json", status: http.StatusUnsupportedMediaType, code: "UNSUPPORTED_MEDIA_TYPE"},
			}
			for _, variant := range variants {
				t.Run(variant.name, func(t *testing.T) {
					request := httptest.NewRequest(test.method, test.path, strings.NewReader(variant.body))
					request.Header.Set("Content-Type", variant.contentType)
					if test.key != "" {
						request.Header.Set("Idempotency-Key", test.key)
					}
					response := httptest.NewRecorder()
					newHealthRouter(NewHandler(nil, nil, nil, nil, nil)).ServeHTTP(response, request)
					problem := requireHealthProblem(t, response)
					if response.Code != variant.status || problem.ErrorCode != variant.code {
						t.Fatalf("status=%d code=%s body=%s", response.Code, problem.ErrorCode, response.Body.String())
					}
				})
			}
		})
	}

	nestedDuplicate := strings.Replace(tests[1].body, `"type":"WORKSPACE"`, `"type":"WORKSPACE","type":"WORKSPACE"`, 1)
	request := httptest.NewRequest(http.MethodPost, tests[1].path, strings.NewReader(nestedDuplicate))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", tests[1].key)
	response := httptest.NewRecorder()
	newHealthRouter(NewHandler(nil, nil, nil, nil, nil)).ServeHTTP(response, request)
	problem := requireHealthProblem(t, response)
	if response.Code != http.StatusBadRequest || problem.ErrorCode != "INVALID_JSON" {
		t.Fatalf("nested duplicate status=%d problem=%+v body=%s", response.Code, problem, response.Body.String())
	}
}

func TestHealthSmartCollectionScopeAcceptsExplicitReadModelRevision(t *testing.T) {
	body := `{"workspace_id":"` + string(healthHTTPWorkspaceID) + `","scope":{"type":"SMART_COLLECTION","ref":"` + string(healthHTTPIssueID) + `","version":2,"schema_version":"health-scope/smart-collection/v1","hash":"` + strings.Repeat("a", 64) + `","read_model_revision":"` + strings.Repeat("b", 64) + `","exact_count":0},"max_items":100,"prevent_scope_concurrency":true}`
	request := httptest.NewRequest(http.MethodPost, "/health/scans", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "smart-collection-scan")
	response := httptest.NewRecorder()
	newHealthRouter(NewHandler(nil, nil, nil, nil, nil)).ServeHTTP(response, request)
	problem := requireHealthProblem(t, response)
	if response.Code != http.StatusServiceUnavailable || problem.ErrorCode != "HEALTH_SCAN_UNAVAILABLE" {
		t.Fatalf("status=%d problem=%+v body=%s", response.Code, problem, response.Body.String())
	}

	encoded, err := json.Marshal(toScopeResponse(domain.ScanScope{
		Type: domain.ScanScopeTypeSmartCollection, Ref: healthHTTPIssueID, Version: 2,
		SchemaVersion: "health-scope/smart-collection/v1", Hash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 0,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"read_model_revision":"`+strings.Repeat("b", 64)+`"`) || !strings.Contains(string(encoded), `"exact_count":0`) || strings.Contains(string(encoded), "ReadModelRevision") {
		t.Fatalf("scope response did not preserve frozen binding: %s", encoded)
	}

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing smart exact count", body: strings.Replace(body, `,"exact_count":0`, "", 1)},
		{name: "workspace exact count", body: `{"workspace_id":"` + string(healthHTTPWorkspaceID) + `","scope":{"type":"WORKSPACE","ref":"` + string(healthHTTPWorkspaceID) + `","version":1,"schema_version":"health-scope/workspace/v1","hash":"","read_model_revision":"","exact_count":0},"max_items":100}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/health/scans", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "scope-count")
			response := httptest.NewRecorder()
			newHealthRouter(NewHandler(nil, nil, nil, nil, nil)).ServeHTTP(response, request)
			problem := requireHealthProblem(t, response)
			if response.Code != http.StatusBadRequest || problem.ErrorCode != "HEALTH_SCAN_INVALID" {
				t.Fatalf("status=%d problem=%+v body=%s", response.Code, problem, response.Body.String())
			}
		})
	}

	scheduleScope, err := json.Marshal(toScheduleScopeResponse(domain.ScanScope{Type: domain.ScanScopeTypeSmartCollection, Ref: healthHTTPIssueID, Version: 2, SchemaVersion: "health-scope/smart-collection/v1", Hash: strings.Repeat("a", 64)}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(scheduleScope), "exact_count") || strings.Contains(string(scheduleScope), "read_model_revision") {
		t.Fatalf("schedule leaked per-run binding: %s", scheduleScope)
	}
}

func TestHealthRoutesRejectUnknownAndRepeatedQueryParameters(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, nil)
	router := newHealthRouter(handler)
	tests := []struct {
		name, method, path string
	}{
		{name: "summary unknown", method: http.MethodGet, path: "/health/summary?workspace_id=" + string(healthHTTPWorkspaceID) + "&unknown=x"},
		{name: "issues workspace repeated", method: http.MethodGet, path: "/health/issues?workspace_id=" + string(healthHTTPWorkspaceID) + "&workspace_id=" + string(healthHTTPWorkspaceID)},
		{name: "issue unknown", method: http.MethodGet, path: "/health/issues/" + string(healthHTTPIssueID) + "?workspace_id=" + string(healthHTTPWorkspaceID) + "&unknown=x"},
		{name: "decision workspace repeated", method: http.MethodPost, path: "/health/issues/" + string(healthHTTPIssueID) + "/decisions?workspace_id=" + string(healthHTTPWorkspaceID) + "&workspace_id=" + string(healthHTTPWorkspaceID)},
		{name: "repair workspace repeated", method: http.MethodPost, path: "/health/issues/" + string(healthHTTPIssueID) + "/repair-proposals?workspace_id=" + string(healthHTTPWorkspaceID) + "&workspace_id=" + string(healthHTTPWorkspaceID)},
		{name: "scan start unknown", method: http.MethodPost, path: "/health/scans?unknown=x"},
		{name: "scan get workspace repeated", method: http.MethodGet, path: "/health/scans/" + string(healthHTTPScanID) + "?workspace_id=" + string(healthHTTPWorkspaceID) + "&workspace_id=" + string(healthHTTPWorkspaceID)},
		{name: "schedule get id repeated", method: http.MethodGet, path: "/health/schedules?workspace_id=" + string(healthHTTPWorkspaceID) + "&schedule_id=" + string(healthHTTPScheduleID) + "&schedule_id=" + string(healthHTTPScheduleID)},
		{name: "schedule update unknown", method: http.MethodPut, path: "/health/schedules?unknown=x"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			problem := requireHealthProblem(t, response)
			if response.Code != http.StatusBadRequest || problem.ErrorCode != "HEALTH_QUERY_INVALID" {
				t.Fatalf("status=%d problem=%+v body=%s", response.Code, problem, response.Body.String())
			}
		})
	}
}

func TestHealthIssueFiltersRejectDuplicateItems(t *testing.T) {
	for _, test := range []struct {
		name, query, code string
	}{
		{name: "status", query: "status=OPEN&status=OPEN", code: "HEALTH_STATUS_INVALID"},
		{name: "severity", query: "severity=HIGH&severity=HIGH", code: "HEALTH_SEVERITY_INVALID"},
		{name: "type", query: "type=CONFLICT&type=CONFLICT", code: "HEALTH_TYPE_INVALID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/health/issues?workspace_id="+string(healthHTTPWorkspaceID)+"&"+test.query, nil)
			response := httptest.NewRecorder()
			newHealthRouter(NewHandler(nil, nil, nil, nil, nil)).ServeHTTP(response, request)
			problem := requireHealthProblem(t, response)
			if response.Code != http.StatusBadRequest || problem.ErrorCode != test.code {
				t.Fatalf("status=%d problem=%+v body=%s", response.Code, problem, response.Body.String())
			}
		})
	}
}

func TestHealthRepairProposalRemainsExplicitlyUnavailable(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/health/issues/"+string(healthHTTPIssueID)+"/repair-proposals?workspace_id="+string(healthHTTPWorkspaceID), nil)
	request.Header.Set("Idempotency-Key", "repair-1")
	response := httptest.NewRecorder()
	newHealthRouter(NewHandler(nil, nil, nil, nil, nil)).ServeHTTP(response, request)
	problem := requireHealthProblem(t, response)
	if response.Code != http.StatusServiceUnavailable || problem.ErrorCode != "HEALTH_REPAIR_PROPOSAL_UNAVAILABLE" {
		t.Fatalf("status=%d problem=%+v body=%s", response.Code, problem, response.Body.String())
	}
}

func TestHealthGetRoutesHideCrossWorkspaceAndMissingResources(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		cross     *Handler
		missing   *Handler
		errorCode string
	}{
		{
			name: "issue", path: "/health/issues/" + string(healthHTTPIssueID) + "?workspace_id=" + string(healthHTTPWorkspaceID), errorCode: "HEALTH_NOT_FOUND",
			cross:   healthHandlerWithRead(t, healthReadFake{detail: application.IssueDetail{Issue: domain.Issue{ID: healthHTTPIssueID, WorkspaceID: healthHTTPOtherWorkspaceID}}}),
			missing: healthHandlerWithRead(t, healthReadFake{detailErr: foundation.NewError(foundation.ErrorNotFound, "HEALTH_NOT_FOUND", false, errors.New("missing issue"))}),
		},
		{
			name: "scan", path: "/health/scans/" + string(healthHTTPScanID) + "?workspace_id=" + string(healthHTTPWorkspaceID), errorCode: "HEALTH_SCAN_NOT_FOUND",
			cross:   healthHandlerWithScan(t, &healthScanStateFake{scan: domain.Scan{ID: healthHTTPScanID, WorkspaceID: healthHTTPOtherWorkspaceID}}),
			missing: healthHandlerWithScan(t, &healthScanStateFake{err: foundation.NewError(foundation.ErrorNotFound, "HEALTH_SCAN_NOT_FOUND", false, errors.New("missing scan"))}),
		},
		{
			name: "schedule", path: "/health/schedules?workspace_id=" + string(healthHTTPWorkspaceID) + "&schedule_id=" + string(healthHTTPScheduleID), errorCode: "HEALTH_SCAN_NOT_FOUND",
			cross:   healthHandlerWithSchedule(t, &healthScheduleStateFake{schedule: domain.Schedule{ID: healthHTTPScheduleID, WorkspaceID: healthHTTPOtherWorkspaceID}}),
			missing: healthHandlerWithSchedule(t, &healthScheduleStateFake{err: foundation.NewError(foundation.ErrorNotFound, "HEALTH_SCAN_NOT_FOUND", false, errors.New("missing schedule"))}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			responses := make([]*httptest.ResponseRecorder, 0, 2)
			for _, handler := range []*Handler{test.cross, test.missing} {
				response := httptest.NewRecorder()
				newHealthRouter(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
				responses = append(responses, response)
			}
			crossProblem := requireHealthProblem(t, responses[0])
			missingProblem := requireHealthProblem(t, responses[1])
			if responses[0].Code != http.StatusNotFound || responses[1].Code != http.StatusNotFound ||
				crossProblem.ErrorCode != test.errorCode || missingProblem.ErrorCode != test.errorCode ||
				crossProblem.Message != missingProblem.Message || crossProblem.Retryable != missingProblem.Retryable {
				t.Fatalf("cross=%d/%+v missing=%d/%+v", responses[0].Code, crossProblem, responses[1].Code, missingProblem)
			}
		})
	}
}

func TestHealthScheduleMutationRejectsMismatchedProjection(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		schedule domain.Schedule
	}{
		{
			name:     "create crossed workspace",
			body:     `{"workspace_id":"` + string(healthHTTPWorkspaceID) + `","scope":{"type":"WORKSPACE","ref":"` + string(healthHTTPWorkspaceID) + `","version":1,"schema_version":"health-scope/v1","hash":""},"cadence":"DISABLED","timezone":"UTC","max_items":100}`,
			schedule: domain.Schedule{ID: healthHTTPScheduleID, WorkspaceID: healthHTTPOtherWorkspaceID},
		},
		{
			name:     "update returned another id",
			body:     `{"workspace_id":"` + string(healthHTTPWorkspaceID) + `","schedule_id":"` + string(healthHTTPScheduleID) + `","expected_version":1,"cadence":"DISABLED","timezone":"UTC","max_items":100}`,
			schedule: domain.Schedule{ID: healthHTTPScanID, WorkspaceID: healthHTTPWorkspaceID},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "/health/schedules", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "schedule-mismatch")
			response := httptest.NewRecorder()
			newHealthRouter(healthHandlerWithSchedule(t, &healthScheduleStateFake{schedule: test.schedule})).ServeHTTP(response, request)
			problem := requireHealthProblem(t, response)
			if response.Code != http.StatusNotFound || problem.ErrorCode != "HEALTH_SCAN_NOT_FOUND" {
				t.Fatalf("status=%d problem=%+v body=%s", response.Code, problem, response.Body.String())
			}
		})
	}
}

func TestHealthNestedScanWireUsesSnakeCase(t *testing.T) {
	summary := application.HealthSummary{
		WorkspaceID:    healthHTTPWorkspaceID,
		OpenCount:      1,
		OpenBySeverity: map[domain.Severity]int64{domain.SeverityHigh: 1},
		OpenByType:     map[domain.IssueType]int64{domain.IssueTypeConflict: 1},
		Unavailable:    []application.CapabilityAvailability{{Code: "REVIEW_INVALIDATED", Reason: "review owner unavailable"}},
		Trend:          healthHTTPTrendFixture(),
		LastScan: &application.ScanSummary{ID: healthHTTPScanID, Status: domain.ScanStatusRunning,
			Scope:    domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: healthHTTPWorkspaceID, Version: 1, SchemaVersion: "health-scope/v1"},
			Counters: domain.ScanCounters{Processed: 2}, Coverage: []domain.DetectorCoverage{{DetectorID: "orphan", DetectorVersion: "v1", Status: domain.DetectorCoverageStatusRunning, Checkpoint: domain.ScanCheckpoint{Cursor: "next", Page: 1}, Counters: domain.ScanCounters{Processed: 2}}}, UpdatedAt: time.Now().UTC()},
	}
	response := httptest.NewRecorder()
	newHealthRouter(healthHandlerWithRead(t, healthReadFake{summary: &summary})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/summary?workspace_id="+string(healthHTTPWorkspaceID), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, fragment := range []string{`"checkpoint":{"cursor":"next","page":1,"last_item":null}`, `"counters":{"processed":2`, `"unavailable":[{"code":"REVIEW_INVALIDATED","reason":"review owner unavailable"}]`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing %s in %s", fragment, body)
		}
	}
	for _, leaked := range []string{`"Cursor"`, `"Processed"`, `"Code"`} {
		if strings.Contains(body, leaked) {
			t.Fatalf("domain field %s leaked into %s", leaked, body)
		}
	}
}

type healthReadFake struct {
	detail    application.IssueDetail
	detailErr error
	summary   *application.HealthSummary
}

func (healthReadFake) ListIssues(context.Context, application.IssueListQuery) (application.IssueListResult, error) {
	return application.IssueListResult{}, nil
}
func (fake healthReadFake) GetIssueDetail(context.Context, foundation.ID, foundation.ID) (application.IssueDetail, error) {
	return fake.detail, fake.detailErr
}
func (fake healthReadFake) GetHealthSummary(_ context.Context, workspaceID foundation.ID) (application.HealthSummary, error) {
	if fake.summary != nil {
		return *fake.summary, nil
	}
	return application.HealthSummary{WorkspaceID: workspaceID, OpenCount: 2, OpenBySeverity: map[domain.Severity]int64{domain.SeverityHigh: 2}, OpenByType: map[domain.IssueType]int64{domain.IssueTypeConflict: 2}, Trend: healthHTTPTrendFixture()}, nil
}

func healthHTTPTrendFixture() []application.HealthTrendPoint {
	start := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	result := make([]application.HealthTrendPoint, 0, application.HealthTrendDays)
	for offset := 0; offset < application.HealthTrendDays; offset++ {
		result = append(result, application.HealthTrendPoint{Date: start.AddDate(0, 0, offset).Format("2006-01-02"), DetectedCount: int64(offset), ResolvedCount: int64(offset / 2)})
	}
	return result
}

type healthScanStateFake struct {
	scan domain.Scan
	err  error
}

func (fake *healthScanStateFake) Get(context.Context, foundation.ID, foundation.ID) (domain.Scan, error) {
	return fake.scan, fake.err
}
func (fake *healthScanStateFake) GetByWorkflowRun(context.Context, foundation.ID, foundation.ID) (domain.Scan, error) {
	return domain.Scan{}, nil
}
func (fake *healthScanStateFake) Advance(context.Context, application.ScanProgress) (domain.Scan, error) {
	return domain.Scan{}, nil
}
func (fake *healthScanStateFake) Finish(context.Context, application.ScanTerminal) (domain.Scan, error) {
	return domain.Scan{}, nil
}

type healthScheduleStateFake struct {
	schedule domain.Schedule
	err      error
}

func (fake *healthScheduleStateFake) Create(context.Context, application.ScheduleCreateCommand) (domain.Schedule, error) {
	return fake.schedule, fake.err
}
func (fake *healthScheduleStateFake) Update(context.Context, application.ScheduleUpdateCommand) (domain.Schedule, error) {
	return fake.schedule, fake.err
}
func (fake *healthScheduleStateFake) Get(context.Context, foundation.ID, foundation.ID) (domain.Schedule, error) {
	return fake.schedule, fake.err
}
func (fake *healthScheduleStateFake) ClaimDue(context.Context, int) ([]application.DueScheduleClaim, error) {
	return nil, nil
}

func healthHandlerWithRead(t *testing.T, reader application.IssueReadPort) *Handler {
	t.Helper()
	codec, err := application.NewIssueCursorCodec([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	read, err := application.NewIssueReadService(reader, codec)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(read, nil, nil, nil, nil)
}

func healthHandlerWithScan(t *testing.T, state application.ScanStatePort) *Handler {
	t.Helper()
	scans, err := application.NewScanStateService(state)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(nil, scans, nil, nil, nil)
}

func healthHandlerWithSchedule(t *testing.T, state application.ScheduleStatePort) *Handler {
	t.Helper()
	schedules, err := application.NewScheduleService(state)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(nil, nil, schedules, nil, nil)
}

func newHealthRouter(handler *Handler) chi.Router {
	router := chi.NewRouter()
	handler.Routes(router)
	return router
}

func requireHealthProblem(t *testing.T, response *httptest.ResponseRecorder) struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
} {
	t.Helper()
	var problem struct {
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("problem body=%s err=%v", response.Body.String(), err)
	}
	return problem
}

type healthDecisionFake struct{ calls int }

func (fake *healthDecisionFake) ApplyDecision(context.Context, foundation.ID, foundation.ID, domain.IssueDecision, time.Time, []domain.RepairOption) (domain.Issue, error) {
	fake.calls++
	return domain.Issue{}, nil
}
