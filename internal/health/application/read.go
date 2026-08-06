package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

const (
	// DefaultIssueListLimit 是 Issue 列表的默认页大小。
	DefaultIssueListLimit = 25
	// MaxIssueListLimit 是 Issue 列表的硬上限，避免无界读取。
	MaxIssueListLimit = 100
	// DefaultIssueHistoryLimit 是 Issue 历史的默认页大小。
	DefaultIssueHistoryLimit = 25
	// MaxIssueHistoryLimit 是 Issue 历史的硬上限，避免无界读取。
	MaxIssueHistoryLimit = 100
	// HealthTrendDays 是 Summary 固定返回的 UTC 自然日数量。
	HealthTrendDays          = 7
	issueCursorSchema        = "health-issue-cursor/v1"
	issueHistoryCursorSchema = "health-issue-history-cursor/v1"
)

type issueHistoryKind string

const (
	issueObservationHistory issueHistoryKind = "observations"
	issueDecisionHistory    issueHistoryKind = "decisions"
)

// IssueListRequest 是 Workspace-scoped Issue 列表查询。
type IssueListRequest struct {
	WorkspaceID foundation.ID
	Statuses    []domain.IssueStatus
	Severities  []domain.Severity
	Types       []domain.IssueType
	Limit       int
	Cursor      string
}

// IssueListPosition 是稳定排序 (updated_at DESC, id DESC) 的 keyset 位置。
type IssueListPosition struct {
	UpdatedAt time.Time
	ID        foundation.ID
}

// IssueListQuery 是 repository 接收的、已验证的列表查询。
type IssueListQuery struct {
	WorkspaceID foundation.ID
	Statuses    []domain.IssueStatus
	Severities  []domain.Severity
	Types       []domain.IssueType
	Limit       int
	After       *IssueListPosition
}

// IssueListItem 是列表页的轻量 Issue 投影，不携带历史 Evidence。
type IssueListItem struct {
	ID              foundation.ID
	WorkspaceID     foundation.ID
	Type            domain.IssueType
	Target          domain.ObjectRef
	DetectorID      string
	DetectorVersion string
	Severity        domain.Severity
	EvidenceSummary string
	Status          domain.IssueStatus
	FirstDetectedAt time.Time
	LastDetectedAt  time.Time
	LastVerifiedAt  time.Time
	Version         int64
	UpdatedAt       time.Time
}

// IssueListResult 是 repository 返回的有界 Issue 列表及下一页位置。
type IssueListResult struct {
	Items   []IssueListItem
	HasMore bool
	Next    *IssueListPosition
}

// IssueListPage 是 application 对外返回的带 opaque cursor 的列表页。
type IssueListPage struct {
	Items      []IssueListItem
	NextCursor string
	HasMore    bool
}

// IssueHistoryRequest 是 Workspace 和 Issue 双重绑定的历史查询。
type IssueHistoryRequest struct {
	WorkspaceID foundation.ID
	IssueID     foundation.ID
	Limit       int
	Cursor      string
}

// IssueHistoryPosition 是稳定排序 (timestamp DESC, id ASC) 的 keyset 位置。
type IssueHistoryPosition struct {
	At time.Time
	ID foundation.ID
}

// IssueObservationQuery 是 repository 接收的、已验证的 observation 历史查询。
type IssueObservationQuery struct {
	WorkspaceID foundation.ID
	IssueID     foundation.ID
	Limit       int
	After       *IssueHistoryPosition
}

// IssueDecisionQuery 是 repository 接收的、已验证的 decision 历史查询。
type IssueDecisionQuery struct {
	WorkspaceID foundation.ID
	IssueID     foundation.ID
	Limit       int
	After       *IssueHistoryPosition
}

// IssueObservationRecord 是不可变 observation 历史投影。
type IssueObservationRecord struct {
	ID                  foundation.ID
	IssueVersion        int64
	ScanID              foundation.ID
	DetectorVersion     string
	Fingerprint         string
	EvidenceFingerprint string
	TargetVersions      []domain.ObjectVersion
	Severity            domain.Severity
	ObservedAt          time.Time
	Evidence            []domain.IssueEvidence
}

// IssueDecisionRecord 是不可变 decision 历史投影。
type IssueDecisionRecord struct {
	ID             foundation.ID
	IssueVersion   int64
	ProposalID     *foundation.ID
	IdempotencyKey string
	Action         domain.IssueDecisionAction
	Reason         string
	DeferredUntil  *time.Time
	CreatedAt      time.Time
}

// IssueObservationResult 是 repository 返回的有界 observation 历史及下一页位置。
type IssueObservationResult struct {
	Items   []IssueObservationRecord
	HasMore bool
	Next    *IssueHistoryPosition
}

// IssueDecisionResult 是 repository 返回的有界 decision 历史及下一页位置。
type IssueDecisionResult struct {
	Items   []IssueDecisionRecord
	HasMore bool
	Next    *IssueHistoryPosition
}

// IssueObservationPage 是 application 对外返回的 observation 历史页。
type IssueObservationPage struct {
	WorkspaceID foundation.ID
	IssueID     foundation.ID
	Items       []IssueObservationRecord
	NextCursor  string
	HasMore     bool
}

// IssueDecisionPage 是 application 对外返回的 decision 历史页。
type IssueDecisionPage struct {
	WorkspaceID foundation.ID
	IssueID     foundation.ID
	Items       []IssueDecisionRecord
	NextCursor  string
	HasMore     bool
}

// IssueSnapshot 是当前 Issue 及与其 fingerprint 精确绑定的 observation 快照。
type IssueSnapshot struct {
	Issue             domain.Issue
	LatestObservation IssueObservationRecord
}

// IssueDetail 是当前 Issue 与两类历史首个有界页的只读聚合。
type IssueDetail struct {
	Issue                  domain.Issue
	LatestObservation      IssueObservationRecord
	Observations           []IssueObservationRecord
	ObservationsNextCursor string
	ObservationsHasMore    bool
	Decisions              []IssueDecisionRecord
	DecisionsNextCursor    string
	DecisionsHasMore       bool
}

// ScanSummary 是 Summary 使用的最后一次扫描精简投影。
type ScanSummary struct {
	ID          foundation.ID
	Status      domain.ScanStatus
	Scope       domain.ScanScope
	CompletedAt *time.Time
	Counters    domain.ScanCounters
	Coverage    []domain.DetectorCoverage
	UpdatedAt   time.Time
}

// CapabilityAvailability 明确一个当前不可用的能力，不把它伪装为空结果。
type CapabilityAvailability struct {
	Code   string
	Reason string
}

// HealthTrendPoint 是由 terminal scan counters 汇总出的单个 UTC 自然日趋势点。
type HealthTrendPoint struct {
	Date          string
	DetectedCount int64
	ResolvedCount int64
}

// HealthSummary 是 Workspace 健康概览；open 聚合统计 active Issue，趋势来自 terminal scan counters。
type HealthSummary struct {
	WorkspaceID    foundation.ID
	OpenCount      int64
	OpenBySeverity map[domain.Severity]int64
	OpenByType     map[domain.IssueType]int64
	Trend          []HealthTrendPoint
	LastScan       *ScanSummary
	Unavailable    []CapabilityAvailability
}

// IssueReadPort 提供 Workspace 隔离的 Issue 读取能力。
type IssueReadPort interface {
	ListIssues(context.Context, IssueListQuery) (IssueListResult, error)
	GetIssue(context.Context, foundation.ID, foundation.ID) (IssueSnapshot, error)
	ListIssueObservations(context.Context, IssueObservationQuery) (IssueObservationResult, error)
	ListIssueDecisions(context.Context, IssueDecisionQuery) (IssueDecisionResult, error)
	GetHealthSummary(context.Context, foundation.ID) (HealthSummary, error)
}

// IssueReadService 负责请求校验、cursor 绑定和 read projection 校验。
type IssueReadService struct {
	reader IssueReadPort
	cursor *IssueCursorCodec
}

// NewIssueReadService 构造 Issue read service。
func NewIssueReadService(reader IssueReadPort, cursor *IssueCursorCodec) (*IssueReadService, error) {
	if reader == nil || cursor == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_READ_UNAVAILABLE", true, errors.New("health read dependencies are unavailable"))
	}
	return &IssueReadService{reader: reader, cursor: cursor}, nil
}

// NewHealthReadService 是 NewIssueReadService 的语义化别名。
func NewHealthReadService(reader IssueReadPort, cursor *IssueCursorCodec) (*IssueReadService, error) {
	return NewIssueReadService(reader, cursor)
}

// ListIssues 返回稳定排序、有限大小且绑定 Workspace/过滤器的 Issue 列表。
func (service *IssueReadService) ListIssues(ctx context.Context, request IssueListRequest) (IssueListPage, error) {
	if service == nil || service.reader == nil || service.cursor == nil {
		return IssueListPage{}, readUnavailable("health issue read service is unavailable")
	}
	if ctx == nil || !validReadID(request.WorkspaceID) {
		return IssueListPage{}, readInvalid("health issue list workspace is invalid")
	}
	limit := request.Limit
	if limit == 0 {
		limit = DefaultIssueListLimit
	}
	if limit < 1 || limit > MaxIssueListLimit {
		return IssueListPage{}, readInvalid("health issue list limit is outside the bounded range")
	}
	if err := validateIssueFilters(request); err != nil {
		return IssueListPage{}, err
	}
	query := IssueListQuery{WorkspaceID: request.WorkspaceID, Statuses: cloneStatuses(request.Statuses), Severities: cloneSeverities(request.Severities), Types: cloneIssueTypes(request.Types), Limit: limit}
	if request.Cursor != "" {
		position, err := service.cursor.Decode(request.Cursor, makeCursorBinding(request, limit))
		if err != nil {
			return IssueListPage{}, err
		}
		query.After = &position
	}
	result, err := service.reader.ListIssues(ctx, query)
	if err != nil {
		return IssueListPage{}, err
	}
	if len(result.Items) > limit {
		return IssueListPage{}, readConsistency("health issue repository returned an unbounded page")
	}
	page := IssueListPage{Items: result.Items, HasMore: result.HasMore}
	if result.HasMore && result.Next == nil {
		return IssueListPage{}, readConsistency("health issue repository omitted next position")
	}
	if result.HasMore && result.Next != nil {
		page.NextCursor, err = service.cursor.Encode(cursorDocument{Binding: makeCursorBinding(request, limit), Position: *result.Next})
		if err != nil {
			return IssueListPage{}, err
		}
	}
	return page, nil
}

// GetIssueDetail 返回当前 Issue、最新 observation/evidence 及两类历史首个有界页。
func (service *IssueReadService) GetIssueDetail(ctx context.Context, workspaceID, issueID foundation.ID) (IssueDetail, error) {
	if service == nil || service.reader == nil || service.cursor == nil {
		return IssueDetail{}, readUnavailable("health issue detail service is unavailable")
	}
	if ctx == nil || !validReadID(workspaceID) || !validReadID(issueID) {
		return IssueDetail{}, readInvalid("health issue detail identity is invalid")
	}
	snapshot, err := service.getIssue(ctx, workspaceID, issueID)
	if err != nil {
		return IssueDetail{}, err
	}
	observationPage, err := service.listIssueObservations(ctx, IssueHistoryRequest{WorkspaceID: workspaceID, IssueID: issueID, Limit: DefaultIssueHistoryLimit}, false)
	if err != nil {
		return IssueDetail{}, err
	}
	decisionPage, err := service.listIssueDecisions(ctx, IssueHistoryRequest{WorkspaceID: workspaceID, IssueID: issueID, Limit: DefaultIssueHistoryLimit}, false)
	if err != nil {
		return IssueDetail{}, err
	}
	if len(observationPage.Items) == 0 {
		return IssueDetail{}, readConsistency("health issue current history is missing")
	}
	detail := IssueDetail{
		Issue:                  snapshot.Issue,
		LatestObservation:      snapshot.LatestObservation,
		Observations:           observationPage.Items,
		ObservationsNextCursor: observationPage.NextCursor,
		ObservationsHasMore:    observationPage.HasMore,
		Decisions:              decisionPage.Items,
		DecisionsNextCursor:    decisionPage.NextCursor,
		DecisionsHasMore:       decisionPage.HasMore,
	}
	if !validReadID(detail.LatestObservation.ID) || !validReadID(detail.LatestObservation.ScanID) ||
		detail.LatestObservation.IssueVersion < 1 || detail.LatestObservation.IssueVersion > detail.Issue.Version || detail.LatestObservation.ObservedAt.IsZero() ||
		detail.LatestObservation.Fingerprint != detail.Issue.Fingerprint ||
		detail.LatestObservation.DetectorVersion != detail.Issue.DetectorVersion ||
		detail.LatestObservation.Severity != detail.Issue.Severity ||
		!reflect.DeepEqual(detail.LatestObservation.Evidence, detail.Issue.Evidence) ||
		!reflect.DeepEqual(detail.LatestObservation.TargetVersions, detail.Issue.ObjectVersions) {
		return IssueDetail{}, readConsistency("health issue current observation is missing or stale")
	}
	if err := domain.ValidateIssue(detail.Issue); err != nil {
		return IssueDetail{}, err
	}
	return detail, nil
}

// ListIssueObservations 返回绑定 Workspace、Issue、类型和 limit 的 observation 历史页。
func (service *IssueReadService) ListIssueObservations(ctx context.Context, request IssueHistoryRequest) (IssueObservationPage, error) {
	return service.listIssueObservations(ctx, request, true)
}

func (service *IssueReadService) listIssueObservations(ctx context.Context, request IssueHistoryRequest, ensureIssue bool) (IssueObservationPage, error) {
	limit, err := service.validateHistoryRequest(ctx, request)
	if err != nil {
		return IssueObservationPage{}, err
	}
	query := IssueObservationQuery{WorkspaceID: request.WorkspaceID, IssueID: request.IssueID, Limit: limit}
	binding := makeHistoryCursorBinding(request, issueObservationHistory, limit)
	if request.Cursor != "" {
		position, err := service.cursor.decodeHistory(request.Cursor, binding)
		if err != nil {
			return IssueObservationPage{}, err
		}
		query.After = &position
	}
	if ensureIssue {
		if _, err := service.getIssue(ctx, request.WorkspaceID, request.IssueID); err != nil {
			return IssueObservationPage{}, err
		}
	}
	result, err := service.reader.ListIssueObservations(ctx, query)
	if err != nil {
		return IssueObservationPage{}, err
	}
	if err := validateObservationResult(result, limit); err != nil {
		return IssueObservationPage{}, err
	}
	page := IssueObservationPage{WorkspaceID: request.WorkspaceID, IssueID: request.IssueID, Items: result.Items, HasMore: result.HasMore}
	if result.HasMore {
		page.NextCursor, err = service.cursor.encodeHistory(historyCursorDocument{Binding: binding, Position: *result.Next})
		if err != nil {
			return IssueObservationPage{}, err
		}
	}
	return page, nil
}

// ListIssueDecisions 返回绑定 Workspace、Issue、类型和 limit 的 decision 历史页。
func (service *IssueReadService) ListIssueDecisions(ctx context.Context, request IssueHistoryRequest) (IssueDecisionPage, error) {
	return service.listIssueDecisions(ctx, request, true)
}

func (service *IssueReadService) listIssueDecisions(ctx context.Context, request IssueHistoryRequest, ensureIssue bool) (IssueDecisionPage, error) {
	limit, err := service.validateHistoryRequest(ctx, request)
	if err != nil {
		return IssueDecisionPage{}, err
	}
	query := IssueDecisionQuery{WorkspaceID: request.WorkspaceID, IssueID: request.IssueID, Limit: limit}
	binding := makeHistoryCursorBinding(request, issueDecisionHistory, limit)
	if request.Cursor != "" {
		position, err := service.cursor.decodeHistory(request.Cursor, binding)
		if err != nil {
			return IssueDecisionPage{}, err
		}
		query.After = &position
	}
	if ensureIssue {
		if _, err := service.getIssue(ctx, request.WorkspaceID, request.IssueID); err != nil {
			return IssueDecisionPage{}, err
		}
	}
	result, err := service.reader.ListIssueDecisions(ctx, query)
	if err != nil {
		return IssueDecisionPage{}, err
	}
	if err := validateDecisionResult(result, limit); err != nil {
		return IssueDecisionPage{}, err
	}
	page := IssueDecisionPage{WorkspaceID: request.WorkspaceID, IssueID: request.IssueID, Items: result.Items, HasMore: result.HasMore}
	if result.HasMore {
		page.NextCursor, err = service.cursor.encodeHistory(historyCursorDocument{Binding: binding, Position: *result.Next})
		if err != nil {
			return IssueDecisionPage{}, err
		}
	}
	return page, nil
}

func (service *IssueReadService) validateHistoryRequest(ctx context.Context, request IssueHistoryRequest) (int, error) {
	if service == nil || service.reader == nil || service.cursor == nil {
		return 0, readUnavailable("health issue history service is unavailable")
	}
	if ctx == nil || !validReadID(request.WorkspaceID) || !validReadID(request.IssueID) {
		return 0, readInvalid("health issue history identity is invalid")
	}
	limit := request.Limit
	if limit == 0 {
		limit = DefaultIssueHistoryLimit
	}
	if limit < 1 || limit > MaxIssueHistoryLimit {
		return 0, readInvalid("health issue history limit is outside the bounded range")
	}
	return limit, nil
}

func (service *IssueReadService) getIssue(ctx context.Context, workspaceID, issueID foundation.ID) (IssueSnapshot, error) {
	snapshot, err := service.reader.GetIssue(ctx, workspaceID, issueID)
	if err != nil {
		return IssueSnapshot{}, err
	}
	if snapshot.Issue.WorkspaceID != workspaceID || snapshot.Issue.ID != issueID {
		return IssueSnapshot{}, readNotFound("health issue is not visible in requested workspace")
	}
	return snapshot, nil
}

// GetHealthSummary 返回 Workspace 健康概览，并保留不可用能力说明。
func (service *IssueReadService) GetHealthSummary(ctx context.Context, workspaceID foundation.ID) (HealthSummary, error) {
	if service == nil || service.reader == nil {
		return HealthSummary{}, readUnavailable("health summary service is unavailable")
	}
	if ctx == nil || !validReadID(workspaceID) {
		return HealthSummary{}, readInvalid("health summary workspace is invalid")
	}
	summary, err := service.reader.GetHealthSummary(ctx, workspaceID)
	if err != nil {
		return HealthSummary{}, err
	}
	if summary.WorkspaceID != workspaceID {
		return HealthSummary{}, readNotFound("health summary is not visible in requested workspace")
	}
	if summary.OpenCount < 0 {
		return HealthSummary{}, readConsistency("health summary open count is negative")
	}
	if err := validateHealthTrend(summary.Trend); err != nil {
		return HealthSummary{}, err
	}
	return summary, nil
}

func validateHealthTrend(points []HealthTrendPoint) error {
	if len(points) != HealthTrendDays {
		return readConsistency("health summary trend must contain exactly seven UTC days")
	}
	var previous time.Time
	for _, point := range points {
		day, err := time.Parse("2006-01-02", point.Date)
		if err != nil || day.Format("2006-01-02") != point.Date || point.DetectedCount < 0 || point.ResolvedCount < 0 {
			return readConsistency("health summary trend point is invalid")
		}
		if !previous.IsZero() && !day.Equal(previous.AddDate(0, 0, 1)) {
			return readConsistency("health summary trend days are not consecutive")
		}
		previous = day
	}
	return nil
}

// IssueCursorCodec 使用 HMAC 签发不暴露内部 key 的 opaque cursor。
type IssueCursorCodec struct{ key []byte }

// NewIssueCursorCodec 创建 cursor codec；密钥至少 32 字节。
func NewIssueCursorCodec(key []byte) (*IssueCursorCodec, error) {
	if len(key) < 32 {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_CURSOR_KEY_UNAVAILABLE", true, errors.New("health cursor key is too short"))
	}
	return &IssueCursorCodec{key: append([]byte(nil), key...)}, nil
}

type cursorBinding struct {
	Schema     string               `json:"schema"`
	Workspace  foundation.ID        `json:"workspace_id"`
	Limit      int                  `json:"limit"`
	Statuses   []domain.IssueStatus `json:"statuses,omitempty"`
	Severities []domain.Severity    `json:"severities,omitempty"`
	Types      []domain.IssueType   `json:"types,omitempty"`
}

type cursorDocument struct {
	Binding  cursorBinding     `json:"binding"`
	Position IssueListPosition `json:"position"`
	MAC      string            `json:"mac"`
}

type historyCursorBinding struct {
	Schema    string           `json:"schema"`
	Workspace foundation.ID    `json:"workspace_id"`
	Issue     foundation.ID    `json:"issue_id"`
	Kind      issueHistoryKind `json:"kind"`
	Limit     int              `json:"limit"`
}

type historyCursorDocument struct {
	Binding  historyCursorBinding `json:"binding"`
	Position IssueHistoryPosition `json:"position"`
	MAC      string               `json:"mac"`
}

// Encode 将 cursor 位置编码为签名字符串。
func (codec *IssueCursorCodec) Encode(document cursorDocument) (string, error) {
	if codec == nil || len(codec.key) < 32 || document.Binding.Schema != issueCursorSchema || !validReadID(document.Binding.Workspace) || document.Binding.Limit < 1 || document.Position.UpdatedAt.IsZero() || !validReadID(document.Position.ID) {
		return "", readInvalid("health cursor document is invalid")
	}
	document.MAC = ""
	payload, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(payload)
	document.MAC = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	raw, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode 验证签名并拒绝跨 Workspace、过滤器或 limit 复用。
func (codec *IssueCursorCodec) Decode(raw string, binding cursorBinding) (IssueListPosition, error) {
	if codec == nil || len(codec.key) < 32 || len(raw) == 0 || len(raw) > 2048 || binding.Schema != issueCursorSchema {
		return IssueListPosition{}, readInvalid("health cursor is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return IssueListPosition{}, readInvalid("health cursor is invalid")
	}
	var document cursorDocument
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	if err := decoder.Decode(&document); err != nil {
		return IssueListPosition{}, readInvalid("health cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || document.MAC == "" || !sameCursorBinding(document.Binding, binding) || !validReadID(document.Position.ID) || document.Position.UpdatedAt.IsZero() {
		return IssueListPosition{}, readInvalid("health cursor is invalid")
	}
	macValue, err := base64.RawURLEncoding.DecodeString(document.MAC)
	if err != nil {
		return IssueListPosition{}, readInvalid("health cursor is invalid")
	}
	expected := document
	expected.MAC = ""
	payload, err := json.Marshal(expected)
	if err != nil {
		return IssueListPosition{}, readInvalid("health cursor is invalid")
	}
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(macValue, mac.Sum(nil)) {
		return IssueListPosition{}, readInvalid("health cursor is invalid")
	}
	return document.Position, nil
}

func (codec *IssueCursorCodec) encodeHistory(document historyCursorDocument) (string, error) {
	if codec == nil || len(codec.key) < 32 || !validHistoryCursorBinding(document.Binding) || document.Position.At.IsZero() || !validReadID(document.Position.ID) {
		return "", readInvalid("health issue history cursor document is invalid")
	}
	document.MAC = ""
	payload, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(payload)
	document.MAC = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	raw, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (codec *IssueCursorCodec) decodeHistory(raw string, binding historyCursorBinding) (IssueHistoryPosition, error) {
	if codec == nil || len(codec.key) < 32 || len(raw) == 0 || len(raw) > 2048 || !validHistoryCursorBinding(binding) {
		return IssueHistoryPosition{}, readInvalid("health issue history cursor is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return IssueHistoryPosition{}, readInvalid("health issue history cursor is invalid")
	}
	var document historyCursorDocument
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	if err := decoder.Decode(&document); err != nil {
		return IssueHistoryPosition{}, readInvalid("health issue history cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || document.MAC == "" || document.Binding != binding || document.Position.At.IsZero() || !validReadID(document.Position.ID) {
		return IssueHistoryPosition{}, readInvalid("health issue history cursor is invalid")
	}
	macValue, err := base64.RawURLEncoding.DecodeString(document.MAC)
	if err != nil {
		return IssueHistoryPosition{}, readInvalid("health issue history cursor is invalid")
	}
	expected := document
	expected.MAC = ""
	payload, err := json.Marshal(expected)
	if err != nil {
		return IssueHistoryPosition{}, readInvalid("health issue history cursor is invalid")
	}
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(macValue, mac.Sum(nil)) {
		return IssueHistoryPosition{}, readInvalid("health issue history cursor is invalid")
	}
	return document.Position, nil
}

func makeCursorBinding(request IssueListRequest, limit int) cursorBinding {
	return cursorBinding{Schema: issueCursorSchema, Workspace: request.WorkspaceID, Limit: limit, Statuses: sortedStatuses(request.Statuses), Severities: sortedSeverities(request.Severities), Types: sortedIssueTypes(request.Types)}
}

func makeHistoryCursorBinding(request IssueHistoryRequest, kind issueHistoryKind, limit int) historyCursorBinding {
	return historyCursorBinding{Schema: issueHistoryCursorSchema, Workspace: request.WorkspaceID, Issue: request.IssueID, Kind: kind, Limit: limit}
}

func validHistoryCursorBinding(binding historyCursorBinding) bool {
	return binding.Schema == issueHistoryCursorSchema && validReadID(binding.Workspace) && validReadID(binding.Issue) && binding.Workspace != binding.Issue &&
		(binding.Kind == issueObservationHistory || binding.Kind == issueDecisionHistory) && binding.Limit >= 1 && binding.Limit <= MaxIssueHistoryLimit
}

func sameCursorBinding(left, right cursorBinding) bool { return reflect.DeepEqual(left, right) }

func validateObservationResult(result IssueObservationResult, limit int) error {
	if len(result.Items) > limit {
		return readConsistency("health observation repository returned an unbounded page")
	}
	positions := make([]IssueHistoryPosition, 0, len(result.Items))
	for _, item := range result.Items {
		positions = append(positions, IssueHistoryPosition{At: item.ObservedAt, ID: item.ID})
	}
	return validateHistoryResult(positions, result.HasMore, result.Next)
}

func validateDecisionResult(result IssueDecisionResult, limit int) error {
	if len(result.Items) > limit {
		return readConsistency("health decision repository returned an unbounded page")
	}
	positions := make([]IssueHistoryPosition, 0, len(result.Items))
	for _, item := range result.Items {
		positions = append(positions, IssueHistoryPosition{At: item.CreatedAt, ID: item.ID})
	}
	return validateHistoryResult(positions, result.HasMore, result.Next)
}

func validateHistoryResult(positions []IssueHistoryPosition, hasMore bool, next *IssueHistoryPosition) error {
	for index, position := range positions {
		if position.At.IsZero() || !validReadID(position.ID) {
			return readConsistency("health issue history repository returned an invalid item")
		}
		if index > 0 {
			previous := positions[index-1]
			if position.At.After(previous.At) || (position.At.Equal(previous.At) && string(position.ID) <= string(previous.ID)) {
				return readConsistency("health issue history repository returned unstable ordering")
			}
		}
	}
	if !hasMore {
		if next != nil {
			return readConsistency("health issue history repository returned an unexpected next position")
		}
		return nil
	}
	if len(positions) == 0 || next == nil {
		return readConsistency("health issue history repository omitted next position")
	}
	last := positions[len(positions)-1]
	if !next.At.Equal(last.At) || next.ID != last.ID {
		return readConsistency("health issue history repository returned a mismatched next position")
	}
	return nil
}

func validateIssueFilters(request IssueListRequest) error {
	seenStatus := map[domain.IssueStatus]struct{}{}
	for _, value := range request.Statuses {
		if !validIssueStatus(value) {
			return readInvalid("health issue status filter is invalid")
		}
		if _, ok := seenStatus[value]; ok {
			return readInvalid("health issue status filter is duplicated")
		}
		seenStatus[value] = struct{}{}
	}
	seenSeverity := map[domain.Severity]struct{}{}
	for _, value := range request.Severities {
		if !validSeverity(value) {
			return readInvalid("health issue severity filter is invalid")
		}
		if _, ok := seenSeverity[value]; ok {
			return readInvalid("health issue severity filter is duplicated")
		}
		seenSeverity[value] = struct{}{}
	}
	seenType := map[domain.IssueType]struct{}{}
	for _, value := range request.Types {
		if !validIssueType(value) {
			return readInvalid("health issue type filter is invalid")
		}
		if _, ok := seenType[value]; ok {
			return readInvalid("health issue type filter is duplicated")
		}
		seenType[value] = struct{}{}
	}
	return nil
}

func validReadID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
func validIssueStatus(value domain.IssueStatus) bool {
	switch value {
	case domain.IssueStatusOpen, domain.IssueStatusAcknowledged, domain.IssueStatusDeferred, domain.IssueStatusProposalCreated, domain.IssueStatusResolved, domain.IssueStatusIgnored, domain.IssueStatusFalsePositive, domain.IssueStatusReopened:
		return true
	}
	return false
}
func validSeverity(value domain.Severity) bool {
	switch value {
	case domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium, domain.SeverityLow:
		return true
	}
	return false
}
func validIssueType(value domain.IssueType) bool {
	switch value {
	case domain.IssueTypeOrphan, domain.IssueTypeDuplicate, domain.IssueTypeConflict, domain.IssueTypeStale, domain.IssueTypeMissingSource, domain.IssueTypeLowConfidence, domain.IssueTypeBrokenReference, domain.IssueTypeIndexError, domain.IssueTypeSupersededUsage, domain.IssueTypeReviewInvalidated:
		return true
	}
	return false
}
func sortedStatuses(values []domain.IssueStatus) []domain.IssueStatus {
	result := append([]domain.IssueStatus(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
func sortedSeverities(values []domain.Severity) []domain.Severity {
	result := append([]domain.Severity(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
func sortedIssueTypes(values []domain.IssueType) []domain.IssueType {
	result := append([]domain.IssueType(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
func cloneStatuses(values []domain.IssueStatus) []domain.IssueStatus {
	return append([]domain.IssueStatus(nil), values...)
}
func cloneSeverities(values []domain.Severity) []domain.Severity {
	return append([]domain.Severity(nil), values...)
}
func cloneIssueTypes(values []domain.IssueType) []domain.IssueType {
	return append([]domain.IssueType(nil), values...)
}
func readInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "HEALTH_READ_INVALID", false, errors.New(message))
}
func readNotFound(message string) error {
	return foundation.NewError(foundation.ErrorNotFound, "HEALTH_NOT_FOUND", false, errors.New(message))
}
func readUnavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_READ_UNAVAILABLE", true, errors.New(message))
}
func readConsistency(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "HEALTH_READ_INCONSISTENT", false, errors.New(message))
}
