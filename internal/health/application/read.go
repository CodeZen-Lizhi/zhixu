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
	// HealthTrendDays 是 Summary 固定返回的 UTC 自然日数量。
	HealthTrendDays   = 7
	issueCursorSchema = "health-issue-cursor/v1"
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

// IssueDetail 是当前 Issue 与完整可审阅历史的只读聚合。
type IssueDetail struct {
	Issue             domain.Issue
	LatestObservation *IssueObservationRecord
	Observations      []IssueObservationRecord
	Decisions         []IssueDecisionRecord
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
	GetIssueDetail(context.Context, foundation.ID, foundation.ID) (IssueDetail, error)
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

// GetIssueDetail 返回当前 Issue、最新 observation/evidence 及不可变历史。
func (service *IssueReadService) GetIssueDetail(ctx context.Context, workspaceID, issueID foundation.ID) (IssueDetail, error) {
	if service == nil || service.reader == nil {
		return IssueDetail{}, readUnavailable("health issue detail service is unavailable")
	}
	if ctx == nil || !validReadID(workspaceID) || !validReadID(issueID) {
		return IssueDetail{}, readInvalid("health issue detail identity is invalid")
	}
	detail, err := service.reader.GetIssueDetail(ctx, workspaceID, issueID)
	if err != nil {
		return IssueDetail{}, err
	}
	if detail.Issue.WorkspaceID != workspaceID || detail.Issue.ID != issueID {
		return IssueDetail{}, readNotFound("health issue is not visible in requested workspace")
	}
	if err := domain.ValidateIssue(detail.Issue); err != nil {
		return IssueDetail{}, err
	}
	return detail, nil
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

func makeCursorBinding(request IssueListRequest, limit int) cursorBinding {
	return cursorBinding{Schema: issueCursorSchema, Workspace: request.WorkspaceID, Limit: limit, Statuses: sortedStatuses(request.Statuses), Severities: sortedSeverities(request.Severities), Types: sortedIssueTypes(request.Types)}
}

func sameCursorBinding(left, right cursorBinding) bool { return reflect.DeepEqual(left, right) }

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
