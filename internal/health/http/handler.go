// Package healthhttp exposes the strict public Knowledge Health contract.
package healthhttp

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

const (
	defaultIssueLimit = application.DefaultIssueListLimit
	maxIssueLimit     = application.MaxIssueListLimit
	maxHealthBody     = 64 * 1024
	maxHealthKeyBytes = 128
	healthTimeout     = 2 * time.Second
)

// DecisionService 是 Issue 决策的最小应用端口；未注入时路由 fail closed。
type DecisionService interface {
	ApplyDecision(context.Context, foundation.ID, foundation.ID, domain.IssueDecision, time.Time, []domain.RepairOption) (domain.Issue, error)
}

// Handler 将 Health application 映射为 Workspace-scoped REST 资源。
type Handler struct {
	read      *application.IssueReadService
	scans     *application.ScanService
	schedules *application.ScheduleService
	detectors *application.Registry
	decisions DecisionService
	timeout   time.Duration
}

// NewHandler 构造 Health Handler。缺少任一能力时，仅对应路由返回明确 503。
func NewHandler(read *application.IssueReadService, scans *application.ScanService, schedules *application.ScheduleService, detectors *application.Registry, decisions DecisionService) *Handler {
	return &Handler{read: read, scans: scans, schedules: schedules, detectors: detectors, decisions: decisions, timeout: healthTimeout}
}

// Available 报告 Health summary/read 是否真实可用。
func (handler *Handler) Available() bool { return handler != nil && handler.read != nil }

// Routes 在 /api/v1 下注册 Health 资源。
func (handler *Handler) Routes(router chi.Router) {
	router.Get("/health/summary", handler.summary)
	router.Get("/health/issues", handler.issues)
	router.Get("/health/issues/{issue_id}", handler.issueDetail)
	router.Get("/health/issues/{issue_id}/observations", handler.issueObservations)
	router.Get("/health/issues/{issue_id}/decisions", handler.issueDecisions)
	router.Post("/health/issues/{issue_id}/decisions", handler.decision)
	router.Post("/health/issues/{issue_id}/repair-proposals", handler.repairProposal)
	router.Post("/health/scans", handler.startScan)
	router.Get("/health/scans/{scan_id}", handler.getScan)
	router.Get("/health/schedules", handler.getSchedule)
	router.Put("/health/schedules", handler.updateSchedule)
}

type issuePageResponse struct {
	WorkspaceID string              `json:"workspace_id"`
	Items       []issueListItemWire `json:"items"`
	NextCursor  string              `json:"next_cursor,omitempty"`
	HasMore     bool                `json:"has_more"`
}

type issueListItemWire struct {
	ID              string             `json:"id"`
	WorkspaceID     string             `json:"workspace_id"`
	Type            domain.IssueType   `json:"type"`
	Target          objectRefWire      `json:"target"`
	DetectorID      string             `json:"detector_id"`
	DetectorVersion string             `json:"detector_version"`
	Severity        domain.Severity    `json:"severity"`
	EvidenceSummary string             `json:"evidence_summary"`
	Status          domain.IssueStatus `json:"status"`
	FirstDetectedAt time.Time          `json:"first_detected_at"`
	LastDetectedAt  time.Time          `json:"last_detected_at"`
	LastVerifiedAt  time.Time          `json:"last_verified_at"`
	Version         int64              `json:"version"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

type objectRefWire struct {
	Type domain.ObjectType `json:"type"`
	ID   string            `json:"id"`
}

type scanCheckpointWire struct {
	Cursor   string         `json:"cursor"`
	Page     int64          `json:"page"`
	LastItem *objectRefWire `json:"last_item"`
}

type scanCountersWire struct {
	Processed int64 `json:"processed"`
	Created   int64 `json:"created"`
	Reopened  int64 `json:"reopened"`
	Resolved  int64 `json:"resolved"`
	Unchanged int64 `json:"unchanged"`
	Failed    int64 `json:"failed"`
}

type failureSummaryWire struct {
	Stage     string `json:"stage"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

type capabilityAvailabilityWire struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func toCheckpointWire(value domain.ScanCheckpoint) scanCheckpointWire {
	result := scanCheckpointWire{Cursor: value.Cursor, Page: value.Page}
	if value.LastItem != nil {
		result.LastItem = &objectRefWire{Type: value.LastItem.Type, ID: string(value.LastItem.ID)}
	}
	return result
}

func toCountersWire(value domain.ScanCounters) scanCountersWire {
	return scanCountersWire{Processed: value.Processed, Created: value.Created, Reopened: value.Reopened, Resolved: value.Resolved, Unchanged: value.Unchanged, Failed: value.Failed}
}

func toFailureWire(value *domain.FailureSummary) *failureSummaryWire {
	if value == nil {
		return nil
	}
	return &failureSummaryWire{Stage: value.Stage, Code: value.Code, Retryable: value.Retryable}
}

func toIssueListItem(value application.IssueListItem) issueListItemWire {
	return issueListItemWire{ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), Type: value.Type,
		Target: objectRefWire{Type: value.Target.Type, ID: string(value.Target.ID)}, DetectorID: value.DetectorID,
		DetectorVersion: value.DetectorVersion, Severity: value.Severity, EvidenceSummary: value.EvidenceSummary,
		Status: value.Status, FirstDetectedAt: value.FirstDetectedAt.UTC(), LastDetectedAt: value.LastDetectedAt.UTC(),
		LastVerifiedAt: value.LastVerifiedAt.UTC(), Version: value.Version, UpdatedAt: value.UpdatedAt.UTC()}
}

func (handler *Handler) summary(w http.ResponseWriter, r *http.Request) {
	query, err := parseQuery(r, "workspace_id")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := queryID(query, "workspace_id")
	if err != nil {
		writeError(w, err)
		return
	}
	if handler == nil || handler.read == nil {
		writeUnavailable(w, "HEALTH_READ_UNAVAILABLE")
		return
	}
	ctx, cancel := handler.withTimeout(r.Context())
	defer cancel()
	summary, err := handler.read.GetHealthSummary(ctx, workspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toSummaryResponse(summary))
}

func (handler *Handler) issues(w http.ResponseWriter, r *http.Request) {
	query, err := parseQuery(r, "workspace_id", "limit", "cursor", "status", "severity", "type")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := queryID(query, "workspace_id")
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := parseLimit(singleQuery(query, "limit"))
	if err != nil {
		writeError(w, err)
		return
	}
	request := application.IssueListRequest{WorkspaceID: workspaceID, Limit: limit, Cursor: singleQuery(query, "cursor")}
	request.Statuses, err = parseIssueStatuses(query["status"])
	if err != nil {
		writeError(w, err)
		return
	}
	request.Severities, err = parseSeverities(query["severity"])
	if err != nil {
		writeError(w, err)
		return
	}
	request.Types, err = parseIssueTypes(query["type"])
	if err != nil {
		writeError(w, err)
		return
	}
	if handler == nil || handler.read == nil {
		writeUnavailable(w, "HEALTH_READ_UNAVAILABLE")
		return
	}
	ctx, cancel := handler.withTimeout(r.Context())
	defer cancel()
	page, err := handler.read.ListIssues(ctx, request)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]issueListItemWire, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, toIssueListItem(item))
	}
	httpapi.WriteJSON(w, http.StatusOK, issuePageResponse{WorkspaceID: string(workspaceID), Items: items, NextCursor: page.NextCursor, HasMore: page.HasMore})
}

func (handler *Handler) issueDetail(w http.ResponseWriter, r *http.Request) {
	query, err := parseQuery(r, "workspace_id")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, issueID, err := queryAndPathIDs(r, query, "issue_id")
	if err != nil {
		writeError(w, err)
		return
	}
	if handler == nil || handler.read == nil {
		writeUnavailable(w, "HEALTH_READ_UNAVAILABLE")
		return
	}
	ctx, cancel := handler.withTimeout(r.Context())
	defer cancel()
	detail, err := handler.read.GetIssueDetail(ctx, workspaceID, issueID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toIssueDetailResponse(detail))
}

func (handler *Handler) issueObservations(w http.ResponseWriter, r *http.Request) {
	request, err := issueHistoryRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if handler == nil || handler.read == nil {
		writeUnavailable(w, "HEALTH_READ_UNAVAILABLE")
		return
	}
	ctx, cancel := handler.withTimeout(r.Context())
	defer cancel()
	page, err := handler.read.ListIssueObservations(ctx, request)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]observationWire, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, toObservation(item))
	}
	httpapi.WriteJSON(w, http.StatusOK, observationPageResponse{WorkspaceID: string(page.WorkspaceID), IssueID: string(page.IssueID), Items: items, NextCursor: page.NextCursor, HasMore: page.HasMore})
}

func (handler *Handler) issueDecisions(w http.ResponseWriter, r *http.Request) {
	request, err := issueHistoryRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if handler == nil || handler.read == nil {
		writeUnavailable(w, "HEALTH_READ_UNAVAILABLE")
		return
	}
	ctx, cancel := handler.withTimeout(r.Context())
	defer cancel()
	page, err := handler.read.ListIssueDecisions(ctx, request)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]decisionWire, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, toDecision(item))
	}
	httpapi.WriteJSON(w, http.StatusOK, decisionPageResponse{WorkspaceID: string(page.WorkspaceID), IssueID: string(page.IssueID), Items: items, NextCursor: page.NextCursor, HasMore: page.HasMore})
}

type decisionRequest struct {
	WorkspaceID      string     `json:"workspace_id"`
	ExpectedVersion  int64      `json:"expected_version"`
	Action           string     `json:"action"`
	Reason           *string    `json:"reason,omitempty"`
	DeferredUntil    *time.Time `json:"deferred_until,omitempty"`
	ProposalID       *string    `json:"proposal_id,omitempty"`
	RepairOptionCode string     `json:"repair_option_code,omitempty"`
}

func (handler *Handler) decision(w http.ResponseWriter, r *http.Request) {
	query, err := parseQuery(r, "workspace_id")
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, issueID, err := queryAndPathIDs(r, query, "issue_id")
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[decisionRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	if wire.WorkspaceID != string(workspaceID) || wire.ExpectedVersion < 1 || strings.TrimSpace(wire.Action) == "" {
		writeError(w, invalid("HEALTH_DECISION_INVALID", "decision request is invalid"))
		return
	}
	decision := domain.IssueDecision{ExpectedVersion: wire.ExpectedVersion, IdempotencyKey: key, Action: domain.IssueDecisionAction(wire.Action), DeferredUntil: wire.DeferredUntil, RepairOptionCode: strings.TrimSpace(wire.RepairOptionCode)}
	if wire.Reason != nil {
		decision.Reason = *wire.Reason
	}
	if wire.ProposalID != nil {
		id, parseErr := foundation.ParseID(*wire.ProposalID)
		if parseErr != nil {
			writeError(w, parseErr)
			return
		}
		decision.ProposalID = &id
	}
	if handler == nil || handler.decisions == nil {
		writeUnavailable(w, "HEALTH_DECISION_UNAVAILABLE")
		return
	}
	ctx, cancel := handler.withTimeout(r.Context())
	defer cancel()
	issue, err := handler.decisions.ApplyDecision(ctx, workspaceID, issueID, decision, time.Now().UTC(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toIssueResponse(issue))
}

func (handler *Handler) repairProposal(w http.ResponseWriter, r *http.Request) {
	query, err := parseQuery(r, "workspace_id")
	if err != nil {
		writeError(w, err)
		return
	}
	if _, _, err := queryAndPathIDs(r, query, "issue_id"); err != nil {
		writeError(w, err)
		return
	}
	if _, err := idempotencyKey(r); err != nil {
		writeError(w, err)
		return
	}
	writeUnavailable(w, "HEALTH_REPAIR_PROPOSAL_UNAVAILABLE")
}

type scanStartRequest struct {
	WorkspaceID             string                 `json:"workspace_id"`
	Scope                   scanScopeWire          `json:"scope"`
	Coverage                []detectorCoverageWire `json:"coverage,omitempty"`
	MaxItems                int64                  `json:"max_items"`
	PreventScopeConcurrency bool                   `json:"prevent_scope_concurrency"`
}

type scanScopeWire struct {
	Type              string `json:"type"`
	Ref               string `json:"ref"`
	Version           int64  `json:"version"`
	SchemaVersion     string `json:"schema_version"`
	Hash              string `json:"hash"`
	ReadModelRevision string `json:"read_model_revision"`
	ExactCount        *int64 `json:"exact_count"`
}
type detectorCoverageWire struct {
	DetectorID        string `json:"detector_id"`
	DetectorVersion   string `json:"detector_version"`
	Status            string `json:"status,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

func scanScopeFromWire(wire scanScopeWire, requireExactCount bool) (domain.ScanScope, error) {
	ref, err := foundation.ParseID(wire.Ref)
	if err != nil {
		return domain.ScanScope{}, invalid("HEALTH_SCAN_INVALID", "health scan scope is invalid")
	}
	typeValue := domain.ScanScopeType(wire.Type)
	if typeValue == domain.ScanScopeTypeSmartCollection {
		if requireExactCount && wire.ExactCount == nil {
			return domain.ScanScope{}, invalid("HEALTH_SCAN_INVALID", "smart-collection scope requires exact_count")
		}
		if !requireExactCount && wire.ExactCount != nil {
			return domain.ScanScope{}, invalid("HEALTH_SCHEDULE_INVALID", "smart-collection schedule cannot persist exact_count")
		}
	} else if wire.ExactCount != nil {
		return domain.ScanScope{}, invalid("HEALTH_SCAN_INVALID", "only smart-collection scope accepts exact_count")
	}
	exactCount := int64(0)
	if wire.ExactCount != nil {
		exactCount = *wire.ExactCount
	}
	return domain.ScanScope{
		Type: typeValue, Ref: ref, Version: wire.Version, SchemaVersion: wire.SchemaVersion,
		Hash: wire.Hash, ReadModelRevision: wire.ReadModelRevision, ExactCount: exactCount,
	}, nil
}

func (handler *Handler) startScan(w http.ResponseWriter, r *http.Request) {
	if _, err := parseQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[scanStartRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := foundation.ParseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	scope, err := scanScopeFromWire(wire.Scope, true)
	if err != nil {
		writeError(w, err)
		return
	}
	coverage := make([]domain.DetectorCoverage, 0, len(wire.Coverage))
	for _, item := range wire.Coverage {
		status := domain.DetectorCoverageStatusPending
		if item.Status != "" {
			status = domain.DetectorCoverageStatus(item.Status)
		}
		coverage = append(coverage, domain.DetectorCoverage{DetectorID: item.DetectorID, DetectorVersion: item.DetectorVersion, Status: status, UnavailableReason: item.UnavailableReason})
	}
	if handler == nil || handler.scans == nil {
		writeUnavailable(w, "HEALTH_SCAN_UNAVAILABLE")
		return
	}
	if len(coverage) == 0 && handler.detectors != nil {
		coverage = handler.detectors.Coverage(application.Scope{WorkspaceID: workspaceID, Type: scope.Type, Ref: scope.Ref, Version: scope.Version, Hash: scope.Hash, ReadModelRevision: scope.ReadModelRevision, ExactCount: scope.ExactCount})
	}
	ctx, cancel := handler.withTimeout(r.Context())
	defer cancel()
	result, err := handler.scans.Start(ctx, application.ScanStartCommand{WorkspaceID: workspaceID, Scope: scope, Coverage: coverage, MaxItems: wire.MaxItems, IdempotencyKey: key, PreventScopeConcurrency: wire.PreventScopeConcurrency})
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, map[string]any{"scan": toScanResponse(result.Scan), "health_scan_id": string(result.Scan.ID), "workflow_run_id": string(result.Scan.WorkflowRunID), "status_url": result.StatusURL, "replayed": result.Replayed})
}

func (handler *Handler) getScan(w http.ResponseWriter, r *http.Request) {
	query, err := parseQuery(r, "workspace_id")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, scanID, err := queryAndPathIDs(r, query, "scan_id")
	if err != nil {
		writeError(w, err)
		return
	}
	if handler == nil || handler.scans == nil {
		writeUnavailable(w, "HEALTH_SCAN_UNAVAILABLE")
		return
	}
	ctx, cancel := handler.withTimeout(r.Context())
	defer cancel()
	scan, err := handler.scans.Get(ctx, workspaceID, scanID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toScanResponse(scan))
}

func (handler *Handler) getSchedule(w http.ResponseWriter, r *http.Request) {
	query, err := parseQuery(r, "workspace_id", "schedule_id")
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := queryID(query, "workspace_id")
	if err != nil {
		writeError(w, err)
		return
	}
	scheduleID, err := queryID(query, "schedule_id")
	if err != nil {
		writeError(w, err)
		return
	}
	if handler == nil || handler.schedules == nil {
		writeUnavailable(w, "HEALTH_SCHEDULE_UNAVAILABLE")
		return
	}
	ctx, cancel := handler.withTimeout(r.Context())
	defer cancel()
	schedule, err := handler.schedules.Get(ctx, workspaceID, scheduleID)
	if err != nil {
		writeError(w, err)
		return
	}
	if schedule.WorkspaceID != workspaceID || schedule.ID != scheduleID {
		writeError(w, notFound("HEALTH_SCAN_NOT_FOUND", "health schedule is not visible in the requested workspace"))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toScheduleResponse(schedule))
}

type scheduleUpdateRequest struct {
	WorkspaceID     string         `json:"workspace_id"`
	ScheduleID      string         `json:"schedule_id,omitempty"`
	ExpectedVersion int64          `json:"expected_version"`
	Scope           *scanScopeWire `json:"scope,omitempty"`
	Cadence         string         `json:"cadence"`
	CronExpression  string         `json:"cron_expression,omitempty"`
	Timezone        string         `json:"timezone"`
	MaxItems        int64          `json:"max_items"`
	NextRunAt       *time.Time     `json:"next_run_at,omitempty"`
}

func (handler *Handler) updateSchedule(w http.ResponseWriter, r *http.Request) {
	if _, err := parseQuery(r); err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := decodeJSON[scheduleUpdateRequest](r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := foundation.ParseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if handler == nil || handler.schedules == nil {
		writeUnavailable(w, "HEALTH_SCHEDULE_UNAVAILABLE")
		return
	}
	ctx, cancel := handler.withTimeout(r.Context())
	defer cancel()
	var schedule domain.Schedule
	var expectedScheduleID foundation.ID
	status := http.StatusOK
	if strings.TrimSpace(wire.ScheduleID) == "" {
		if wire.Scope == nil {
			writeError(w, invalid("HEALTH_SCHEDULE_INVALID", "scope is required when creating a schedule"))
			return
		}
		scope, parseErr := scanScopeFromWire(*wire.Scope, false)
		if parseErr != nil {
			writeError(w, parseErr)
			return
		}
		schedule, err = handler.schedules.Create(ctx, application.ScheduleCreateCommand{WorkspaceID: workspaceID, Scope: scope, Cadence: domain.ScheduleCadence(wire.Cadence), CronExpression: wire.CronExpression, Timezone: wire.Timezone, MaxItems: wire.MaxItems, NextRunAt: wire.NextRunAt, IdempotencyKey: key})
		status = http.StatusCreated
	} else {
		scheduleID, parseErr := foundation.ParseID(wire.ScheduleID)
		if parseErr != nil {
			writeError(w, parseErr)
			return
		}
		expectedScheduleID = scheduleID
		schedule, err = handler.schedules.Update(ctx, application.ScheduleUpdateCommand{WorkspaceID: workspaceID, ScheduleID: scheduleID, ExpectedVersion: wire.ExpectedVersion, Cadence: domain.ScheduleCadence(wire.Cadence), CronExpression: wire.CronExpression, Timezone: wire.Timezone, MaxItems: wire.MaxItems, NextRunAt: wire.NextRunAt, IdempotencyKey: key})
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if schedule.WorkspaceID != workspaceID || expectedScheduleID != "" && schedule.ID != expectedScheduleID {
		writeError(w, notFound("HEALTH_SCAN_NOT_FOUND", "health schedule is not visible in the requested workspace"))
		return
	}
	httpapi.WriteJSON(w, status, toScheduleResponse(schedule))
}

func (handler *Handler) withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, handler.timeout)
}

func queryAndPathIDs(r *http.Request, query url.Values, path string) (foundation.ID, foundation.ID, error) {
	workspaceID, err := queryID(query, "workspace_id")
	if err != nil {
		return "", "", err
	}
	raw := chi.URLParam(r, path)
	value, err := foundation.ParseID(raw)
	if err != nil || string(value) != raw {
		return "", "", invalid("HEALTH_ID_INVALID", "resource id is invalid")
	}
	return workspaceID, value, nil
}

func queryID(query url.Values, key string) (foundation.ID, error) {
	raw := singleQuery(query, key)
	if raw == "" {
		return "", invalid("HEALTH_QUERY_INVALID", key+" is required")
	}
	id, err := foundation.ParseID(raw)
	if err != nil || string(id) != raw {
		return "", invalid("HEALTH_ID_INVALID", key+" is invalid")
	}
	return id, nil
}

func parseQuery(r *http.Request, allowed ...string) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalid("HEALTH_QUERY_INVALID", "query parameters are invalid")
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := allowedSet[key]; !ok || len(entries) == 0 {
			return nil, invalid("HEALTH_QUERY_INVALID", "query parameter is not allowed")
		}
		switch key {
		case "status", "severity", "type":
			for _, entry := range entries {
				if entry == "" || entry != strings.TrimSpace(entry) {
					return nil, invalid("HEALTH_QUERY_INVALID", "query parameter is invalid")
				}
			}
		default:
			if len(entries) != 1 || entries[0] == "" || entries[0] != strings.TrimSpace(entries[0]) {
				return nil, invalid("HEALTH_QUERY_INVALID", "query parameter must occur exactly once")
			}
		}
	}
	return values, nil
}

func singleQuery(values url.Values, key string) string {
	entries := values[key]
	if len(entries) == 1 {
		return entries[0]
	}
	return ""
}

func parseLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultIssueLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maxIssueLimit {
		return 0, invalid("HEALTH_LIMIT_INVALID", "limit must be between 1 and 100")
	}
	return value, nil
}

func issueHistoryRequest(r *http.Request) (application.IssueHistoryRequest, error) {
	query, err := parseQuery(r, "workspace_id", "limit", "cursor")
	if err != nil {
		return application.IssueHistoryRequest{}, err
	}
	workspaceID, issueID, err := queryAndPathIDs(r, query, "issue_id")
	if err != nil {
		return application.IssueHistoryRequest{}, err
	}
	limit, err := parseLimit(singleQuery(query, "limit"))
	if err != nil {
		return application.IssueHistoryRequest{}, err
	}
	return application.IssueHistoryRequest{WorkspaceID: workspaceID, IssueID: issueID, Limit: limit, Cursor: singleQuery(query, "cursor")}, nil
}

func parseIssueStatuses(values []string) ([]domain.IssueStatus, error) {
	result := make([]domain.IssueStatus, 0, len(values))
	seen := make(map[domain.IssueStatus]struct{}, len(values))
	for _, value := range values {
		parsed := domain.IssueStatus(value)
		if parsed == "" || !validStatus(parsed) {
			return nil, invalid("HEALTH_STATUS_INVALID", "status filter is invalid")
		}
		if _, duplicate := seen[parsed]; duplicate {
			return nil, invalid("HEALTH_STATUS_INVALID", "status filter is duplicated")
		}
		seen[parsed] = struct{}{}
		result = append(result, parsed)
	}
	return result, nil
}
func parseSeverities(values []string) ([]domain.Severity, error) {
	result := make([]domain.Severity, 0, len(values))
	seen := make(map[domain.Severity]struct{}, len(values))
	for _, value := range values {
		parsed := domain.Severity(value)
		if parsed == "" || !validSeverity(parsed) {
			return nil, invalid("HEALTH_SEVERITY_INVALID", "severity filter is invalid")
		}
		if _, duplicate := seen[parsed]; duplicate {
			return nil, invalid("HEALTH_SEVERITY_INVALID", "severity filter is duplicated")
		}
		seen[parsed] = struct{}{}
		result = append(result, parsed)
	}
	return result, nil
}
func parseIssueTypes(values []string) ([]domain.IssueType, error) {
	result := make([]domain.IssueType, 0, len(values))
	seen := make(map[domain.IssueType]struct{}, len(values))
	for _, value := range values {
		parsed := domain.IssueType(value)
		if parsed == "" || !validIssueType(parsed) {
			return nil, invalid("HEALTH_TYPE_INVALID", "type filter is invalid")
		}
		if _, duplicate := seen[parsed]; duplicate {
			return nil, invalid("HEALTH_TYPE_INVALID", "type filter is duplicated")
		}
		seen[parsed] = struct{}{}
		result = append(result, parsed)
	}
	return result, nil
}

func idempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) {
		return "", invalid("HEALTH_IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required")
	}
	key := values[0]
	if key == "" || key != strings.TrimSpace(key) || len(key) > maxHealthKeyBytes {
		return "", invalid("HEALTH_IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required")
	}
	for _, character := range key {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return "", invalid("HEALTH_IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required")
		}
	}
	return key, nil
}

type issueResponse struct {
	ID              string               `json:"id"`
	WorkspaceID     string               `json:"workspace_id"`
	Type            domain.IssueType     `json:"type"`
	Target          objectRefWire        `json:"target"`
	DetectorID      string               `json:"detector_id"`
	DetectorVersion string               `json:"detector_version"`
	IdentityHash    string               `json:"identity_hash"`
	Fingerprint     string               `json:"fingerprint"`
	Severity        domain.Severity      `json:"severity"`
	EvidenceSummary string               `json:"evidence_summary"`
	Evidence        []evidenceWire       `json:"evidence,omitempty"`
	ObjectVersions  []objectVersionWire  `json:"object_versions,omitempty"`
	RepairOptions   []repairOptionWire   `json:"repair_options,omitempty"`
	Status          domain.IssueStatus   `json:"status"`
	StatusReason    string               `json:"status_reason,omitempty"`
	DeferredUntil   *time.Time           `json:"deferred_until,omitempty"`
	Proposal        *proposalBindingWire `json:"proposal,omitempty"`
	FirstDetectedAt time.Time            `json:"first_detected_at"`
	LastDetectedAt  time.Time            `json:"last_detected_at"`
	LastVerifiedAt  time.Time            `json:"last_verified_at"`
	ResolvedAt      *time.Time           `json:"resolved_at,omitempty"`
	Version         int64                `json:"version"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

type evidenceWire struct {
	Ref     objectRefWire `json:"ref"`
	Hash    string        `json:"hash"`
	Summary string        `json:"summary"`
}
type objectVersionWire struct {
	Ref     objectRefWire `json:"ref"`
	Version int64         `json:"version"`
}
type repairOptionWire struct {
	Code              string `json:"code"`
	Title             string `json:"title"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}
type proposalBindingWire struct {
	ProposalID       string              `json:"proposal_id"`
	RepairOptionCode string              `json:"repair_option_code"`
	Fingerprint      string              `json:"fingerprint"`
	ObjectVersions   []objectVersionWire `json:"object_versions"`
	CreatedAt        time.Time           `json:"created_at"`
}

func toIssueResponse(issue domain.Issue) issueResponse {
	response := issueResponse{ID: string(issue.ID), WorkspaceID: string(issue.WorkspaceID), Type: issue.Type,
		Target: objectRefWire{Type: issue.Target.Type, ID: string(issue.Target.ID)}, DetectorID: issue.DetectorID,
		DetectorVersion: issue.DetectorVersion, IdentityHash: issue.IdentityHash, Fingerprint: issue.Fingerprint,
		Severity: issue.Severity, EvidenceSummary: issue.EvidenceSummary, Status: issue.Status, StatusReason: issue.StatusReason,
		DeferredUntil: issue.DeferredUntil, FirstDetectedAt: issue.FirstDetectedAt.UTC(), LastDetectedAt: issue.LastDetectedAt.UTC(),
		LastVerifiedAt: issue.LastVerifiedAt.UTC(), ResolvedAt: issue.ResolvedAt, Version: issue.Version, CreatedAt: issue.CreatedAt.UTC(), UpdatedAt: issue.UpdatedAt.UTC(),
		Evidence: make([]evidenceWire, 0, len(issue.Evidence)), ObjectVersions: make([]objectVersionWire, 0, len(issue.ObjectVersions)), RepairOptions: make([]repairOptionWire, 0, len(issue.RepairOptions))}
	for _, evidence := range issue.Evidence {
		response.Evidence = append(response.Evidence, evidenceWire{Ref: objectRefWire{Type: evidence.Ref.Type, ID: string(evidence.Ref.ID)}, Hash: evidence.Hash, Summary: evidence.Summary})
	}
	for _, version := range issue.ObjectVersions {
		response.ObjectVersions = append(response.ObjectVersions, objectVersionWire{Ref: objectRefWire{Type: version.Ref.Type, ID: string(version.Ref.ID)}, Version: version.Version})
	}
	for _, option := range issue.RepairOptions {
		response.RepairOptions = append(response.RepairOptions, repairOptionWire{Code: option.Code, Title: option.Title, Available: option.Available, UnavailableReason: option.UnavailableReason})
	}
	if issue.Proposal != nil {
		binding := &proposalBindingWire{ProposalID: string(issue.Proposal.ProposalID), RepairOptionCode: issue.Proposal.RepairOptionCode, Fingerprint: issue.Proposal.Fingerprint, ObjectVersions: make([]objectVersionWire, 0, len(issue.Proposal.ObjectVersions)), CreatedAt: issue.Proposal.CreatedAt.UTC()}
		for _, version := range issue.Proposal.ObjectVersions {
			binding.ObjectVersions = append(binding.ObjectVersions, objectVersionWire{Ref: objectRefWire{Type: version.Ref.Type, ID: string(version.Ref.ID)}, Version: version.Version})
		}
		response.Proposal = binding
	}
	return response
}

type observationWire struct {
	ID                  string              `json:"id"`
	IssueVersion        int64               `json:"issue_version"`
	ScanID              string              `json:"scan_id"`
	DetectorVersion     string              `json:"detector_version"`
	Fingerprint         string              `json:"fingerprint"`
	EvidenceFingerprint string              `json:"evidence_fingerprint"`
	TargetVersions      []objectVersionWire `json:"target_versions"`
	Severity            domain.Severity     `json:"severity"`
	ObservedAt          time.Time           `json:"observed_at"`
	Evidence            []evidenceWire      `json:"evidence"`
}
type decisionWire struct {
	ID             string                     `json:"id"`
	IssueVersion   int64                      `json:"issue_version"`
	ProposalID     *string                    `json:"proposal_id,omitempty"`
	IdempotencyKey string                     `json:"idempotency_key"`
	Action         domain.IssueDecisionAction `json:"action"`
	Reason         string                     `json:"reason,omitempty"`
	DeferredUntil  *time.Time                 `json:"deferred_until,omitempty"`
	CreatedAt      time.Time                  `json:"created_at"`
}
type observationPageResponse struct {
	WorkspaceID string            `json:"workspace_id"`
	IssueID     string            `json:"issue_id"`
	Items       []observationWire `json:"items"`
	NextCursor  string            `json:"next_cursor,omitempty"`
	HasMore     bool              `json:"has_more"`
}
type decisionPageResponse struct {
	WorkspaceID string         `json:"workspace_id"`
	IssueID     string         `json:"issue_id"`
	Items       []decisionWire `json:"items"`
	NextCursor  string         `json:"next_cursor,omitempty"`
	HasMore     bool           `json:"has_more"`
}
type issueDetailResponse struct {
	Issue                  issueResponse     `json:"issue"`
	LatestObservation      observationWire   `json:"latest_observation"`
	Observations           []observationWire `json:"observations"`
	ObservationsNextCursor string            `json:"observations_next_cursor,omitempty"`
	ObservationsHasMore    bool              `json:"observations_has_more"`
	Decisions              []decisionWire    `json:"decisions"`
	DecisionsNextCursor    string            `json:"decisions_next_cursor,omitempty"`
	DecisionsHasMore       bool              `json:"decisions_has_more"`
}

func toIssueDetailResponse(detail application.IssueDetail) issueDetailResponse {
	result := issueDetailResponse{
		Issue:                  toIssueResponse(detail.Issue),
		LatestObservation:      toObservation(detail.LatestObservation),
		Observations:           make([]observationWire, 0, len(detail.Observations)),
		ObservationsNextCursor: detail.ObservationsNextCursor,
		ObservationsHasMore:    detail.ObservationsHasMore,
		Decisions:              make([]decisionWire, 0, len(detail.Decisions)),
		DecisionsNextCursor:    detail.DecisionsNextCursor,
		DecisionsHasMore:       detail.DecisionsHasMore,
	}
	for _, value := range detail.Observations {
		result.Observations = append(result.Observations, toObservation(value))
	}
	for _, value := range detail.Decisions {
		result.Decisions = append(result.Decisions, toDecision(value))
	}
	return result
}

func toDecision(value application.IssueDecisionRecord) decisionWire {
	item := decisionWire{ID: string(value.ID), IssueVersion: value.IssueVersion, IdempotencyKey: value.IdempotencyKey, Action: value.Action, Reason: value.Reason, DeferredUntil: value.DeferredUntil, CreatedAt: value.CreatedAt.UTC()}
	if value.ProposalID != nil {
		proposal := string(*value.ProposalID)
		item.ProposalID = &proposal
	}
	return item
}

func toObservation(value application.IssueObservationRecord) observationWire {
	item := observationWire{ID: string(value.ID), IssueVersion: value.IssueVersion, ScanID: string(value.ScanID), DetectorVersion: value.DetectorVersion, Fingerprint: value.Fingerprint, EvidenceFingerprint: value.EvidenceFingerprint, TargetVersions: make([]objectVersionWire, 0, len(value.TargetVersions)), Severity: value.Severity, ObservedAt: value.ObservedAt.UTC(), Evidence: make([]evidenceWire, 0, len(value.Evidence))}
	for _, version := range value.TargetVersions {
		item.TargetVersions = append(item.TargetVersions, objectVersionWire{Ref: objectRefWire{Type: version.Ref.Type, ID: string(version.Ref.ID)}, Version: version.Version})
	}
	for _, evidence := range value.Evidence {
		item.Evidence = append(item.Evidence, evidenceWire{Ref: objectRefWire{Type: evidence.Ref.Type, ID: string(evidence.Ref.ID)}, Hash: evidence.Hash, Summary: evidence.Summary})
	}
	return item
}

type summaryResponse struct {
	WorkspaceID    string                       `json:"workspace_id"`
	OpenCount      int64                        `json:"open_count"`
	OpenBySeverity map[domain.Severity]int64    `json:"open_by_severity"`
	OpenByType     map[domain.IssueType]int64   `json:"open_by_type"`
	Trend          []healthTrendPointWire       `json:"trend"`
	LastScan       *scanSummaryResponse         `json:"last_scan,omitempty"`
	Unavailable    []capabilityAvailabilityWire `json:"unavailable,omitempty"`
}
type healthTrendPointWire struct {
	Date          string `json:"date"`
	DetectedCount int64  `json:"detected_count"`
	ResolvedCount int64  `json:"resolved_count"`
}
type scanSummaryResponse struct {
	ID          string                     `json:"id"`
	Status      domain.ScanStatus          `json:"status"`
	Scope       scanScopeResponse          `json:"scope"`
	CompletedAt *time.Time                 `json:"completed_at,omitempty"`
	Counters    scanCountersWire           `json:"counters"`
	Coverage    []detectorCoverageResponse `json:"coverage"`
	UpdatedAt   time.Time                  `json:"updated_at"`
}
type detectorCoverageResponse struct {
	DetectorID        string                        `json:"detector_id"`
	DetectorVersion   string                        `json:"detector_version"`
	Status            domain.DetectorCoverageStatus `json:"status"`
	Checkpoint        scanCheckpointWire            `json:"checkpoint"`
	Counters          scanCountersWire              `json:"counters"`
	LastError         *failureSummaryWire           `json:"last_error,omitempty"`
	UnavailableReason string                        `json:"unavailable_reason,omitempty"`
}
type scanScopeResponse struct {
	Type              domain.ScanScopeType `json:"type"`
	Ref               string               `json:"ref"`
	Version           int64                `json:"version"`
	SchemaVersion     string               `json:"schema_version"`
	Hash              string               `json:"hash,omitempty"`
	ReadModelRevision string               `json:"read_model_revision,omitempty"`
	ExactCount        *int64               `json:"exact_count,omitempty"`
}

func toScopeResponse(scope domain.ScanScope) scanScopeResponse {
	result := scanScopeResponse{Type: scope.Type, Ref: string(scope.Ref), Version: scope.Version, SchemaVersion: scope.SchemaVersion, Hash: scope.Hash, ReadModelRevision: scope.ReadModelRevision}
	if scope.Type == domain.ScanScopeTypeSmartCollection {
		exactCount := scope.ExactCount
		result.ExactCount = &exactCount
	}
	return result
}

func toScheduleScopeResponse(scope domain.ScanScope) scanScopeResponse {
	return scanScopeResponse{Type: scope.Type, Ref: string(scope.Ref), Version: scope.Version, SchemaVersion: scope.SchemaVersion, Hash: scope.Hash}
}
func toSummaryResponse(value application.HealthSummary) summaryResponse {
	result := summaryResponse{WorkspaceID: string(value.WorkspaceID), OpenCount: value.OpenCount, OpenBySeverity: value.OpenBySeverity, OpenByType: value.OpenByType, Trend: make([]healthTrendPointWire, 0, len(value.Trend))}
	for _, point := range value.Trend {
		result.Trend = append(result.Trend, healthTrendPointWire{Date: point.Date, DetectedCount: point.DetectedCount, ResolvedCount: point.ResolvedCount})
	}
	for _, unavailable := range value.Unavailable {
		result.Unavailable = append(result.Unavailable, capabilityAvailabilityWire{Code: unavailable.Code, Reason: unavailable.Reason})
	}
	if value.LastScan != nil {
		result.LastScan = &scanSummaryResponse{ID: string(value.LastScan.ID), Status: value.LastScan.Status, Scope: toScopeResponse(value.LastScan.Scope), CompletedAt: value.LastScan.CompletedAt, Counters: toCountersWire(value.LastScan.Counters), Coverage: make([]detectorCoverageResponse, 0, len(value.LastScan.Coverage)), UpdatedAt: value.LastScan.UpdatedAt.UTC()}
		for _, coverage := range value.LastScan.Coverage {
			result.LastScan.Coverage = append(result.LastScan.Coverage, toCoverageResponse(coverage))
		}
	}
	return result
}

func toCoverageResponse(value domain.DetectorCoverage) detectorCoverageResponse {
	return detectorCoverageResponse{DetectorID: value.DetectorID, DetectorVersion: value.DetectorVersion, Status: value.Status, Checkpoint: toCheckpointWire(value.Checkpoint), Counters: toCountersWire(value.Counters), LastError: toFailureWire(value.LastError), UnavailableReason: value.UnavailableReason}
}

type scanResponse struct {
	ID            string                     `json:"id"`
	WorkspaceID   string                     `json:"workspace_id"`
	WorkflowRunID string                     `json:"workflow_run_id"`
	Scope         scanScopeResponse          `json:"scope"`
	Fingerprint   string                     `json:"fingerprint"`
	RequestHash   string                     `json:"request_hash"`
	MaxItems      int64                      `json:"max_items"`
	Status        domain.ScanStatus          `json:"status"`
	Checkpoint    scanCheckpointWire         `json:"checkpoint"`
	Counters      scanCountersWire           `json:"counters"`
	Coverage      []detectorCoverageResponse `json:"coverage"`
	LastError     *failureSummaryWire        `json:"last_error"`
	Version       int64                      `json:"version"`
	CreatedAt     time.Time                  `json:"created_at"`
	UpdatedAt     time.Time                  `json:"updated_at"`
	CompletedAt   *time.Time                 `json:"completed_at"`
	StatusURL     string                     `json:"status_url"`
}

func toScanResponse(value domain.Scan) scanResponse {
	coverage := make([]detectorCoverageResponse, 0, len(value.Coverage))
	for _, item := range value.Coverage {
		coverage = append(coverage, toCoverageResponse(item))
	}
	return scanResponse{ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), WorkflowRunID: string(value.WorkflowRunID), Scope: toScopeResponse(value.Scope), Fingerprint: value.Fingerprint, RequestHash: value.RequestHash, MaxItems: value.MaxItems, Status: value.Status, Checkpoint: toCheckpointWire(value.Checkpoint), Counters: toCountersWire(value.Counters), Coverage: coverage, LastError: toFailureWire(value.LastError), Version: value.Version, CreatedAt: value.CreatedAt.UTC(), UpdatedAt: value.UpdatedAt.UTC(), CompletedAt: value.CompletedAt, StatusURL: application.HealthScanStatusURL(value.WorkspaceID, value.ID)}
}

type scheduleResponse struct {
	ID             string                 `json:"id"`
	WorkspaceID    string                 `json:"workspace_id"`
	Scope          scanScopeResponse      `json:"scope"`
	Cadence        domain.ScheduleCadence `json:"cadence"`
	CronExpression string                 `json:"cron_expression"`
	Timezone       string                 `json:"timezone"`
	MaxItems       int64                  `json:"max_items"`
	NextRunAt      *time.Time             `json:"next_run_at"`
	LastRunAt      *time.Time             `json:"last_run_at"`
	Version        int64                  `json:"version"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

func toScheduleResponse(value domain.Schedule) scheduleResponse {
	return scheduleResponse{ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), Scope: toScheduleScopeResponse(value.Scope), Cadence: value.Cadence, CronExpression: value.CronExpression, Timezone: value.Timezone, MaxItems: value.MaxItems, NextRunAt: value.NextRunAt, LastRunAt: value.LastRunAt, Version: value.Version, CreatedAt: value.CreatedAt.UTC(), UpdatedAt: value.UpdatedAt.UTC()}
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var zero T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, invalid("UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHealthBody+1))
	if err != nil || len(body) == 0 || len(body) > maxHealthBody {
		return zero, invalid("INVALID_JSON", "request JSON is invalid")
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxHealthBody
	decoded, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		return zero, invalid("INVALID_JSON", "request JSON is invalid")
	}
	return decoded, nil
}

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func notFound(code, message string) error {
	return foundation.NewError(foundation.ErrorNotFound, code, false, errors.New(message))
}

func writeUnavailable(w http.ResponseWriter, code string) {
	httpapi.WriteProblem(w, http.StatusServiceUnavailable, code, "Knowledge Health 能力暂不可用", true, nil)
}

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, "HEALTH_DEPENDENCY_UNAVAILABLE", "Knowledge Health 请求未完成", !errors.Is(err, context.Canceled), nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "HEALTH_INTERNAL_ERROR", "Knowledge Health 请求失败", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	message := "Knowledge Health 请求未完成"
	switch classified.Code {
	case "UNSUPPORTED_MEDIA_TYPE":
		status, message = http.StatusUnsupportedMediaType, "请求必须使用 application/json"
	case "INVALID_JSON":
		status, message = http.StatusBadRequest, "请求 JSON 无效"
	}
	httpapi.WriteProblem(w, status, classified.Code, message, classified.Retryable, nil)
}

func validStatus(value domain.IssueStatus) bool {
	switch value {
	case domain.IssueStatusOpen, domain.IssueStatusAcknowledged, domain.IssueStatusDeferred, domain.IssueStatusProposalCreated, domain.IssueStatusResolved, domain.IssueStatusIgnored, domain.IssueStatusFalsePositive, domain.IssueStatusReopened:
		return true
	default:
		return false
	}
}
func validSeverity(value domain.Severity) bool {
	switch value {
	case domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium, domain.SeverityLow:
		return true
	default:
		return false
	}
}
func validIssueType(value domain.IssueType) bool {
	switch value {
	case domain.IssueTypeOrphan, domain.IssueTypeDuplicate, domain.IssueTypeConflict, domain.IssueTypeStale, domain.IssueTypeMissingSource, domain.IssueTypeLowConfidence, domain.IssueTypeBrokenReference, domain.IssueTypeIndexError, domain.IssueTypeSupersededUsage, domain.IssueTypeReviewInvalidated:
		return true
	default:
		return false
	}
}
