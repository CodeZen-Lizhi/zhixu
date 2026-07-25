package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/redaction"
)

const (
	// KnowledgeEventSchemaVersion 是 Timeline 投影事件的稳定结构版本。
	KnowledgeEventSchemaVersion = "knowledge-event/v1"
	// ImpactReportSchemaVersion 是 Impact 报告的稳定结构版本。
	ImpactReportSchemaVersion = "impact-report/v1"
	// MaxKnowledgeEventSummaryBytes 限制用户可见事件摘要的大小。
	MaxKnowledgeEventSummaryBytes = 4096
	// MaxKnowledgeEventPayloadBytes 限制事件动态摘要载荷的大小。
	MaxKnowledgeEventPayloadBytes = 32 * 1024
	// MaxKnowledgeEventCorrelationBytes 限制跨模块关联摘要的大小。
	MaxKnowledgeEventCorrelationBytes = 8 * 1024
	// MaxTimelineLimit 是 Timeline 单页硬上限。
	MaxTimelineLimit = 100
	// MaxTimelineEventTypeFilters 是单次 Timeline 查询允许重复传入的事件类型数量上限。
	MaxTimelineEventTypeFilters = 32
	// MaxImpactObjects 是单个 Impact 报告允许携带的影响对象数量。
	MaxImpactObjects = 500
)

// EventType 是由正式状态变化投影出的稳定事件类型。
type EventType string

const (
	EventProposalCreated      EventType = "PROPOSAL_CREATED"
	EventApprovalGranted      EventType = "APPROVAL_GRANTED"
	EventApprovalRejected     EventType = "APPROVAL_REJECTED"
	EventGitCommitted         EventType = "GIT_COMMITTED"
	EventRelationConfirmed    EventType = "RELATION_CONFIRMED"
	EventRelationDeprecated   EventType = "RELATION_DEPRECATED"
	EventConflictOpened       EventType = "CONFLICT_OPENED"
	EventConflictTransitioned EventType = "CONFLICT_TRANSITIONED"
	EventConflictResolved     EventType = "CONFLICT_RESOLVED"
	EventVersionPublished     EventType = "VERSION_PUBLISHED"
	EventVersionSuperseded    EventType = "VERSION_SUPERSEDED"
	EventHealthIssueDetected  EventType = "HEALTH_ISSUE_DETECTED"
	EventHealthIssueResolved  EventType = "HEALTH_ISSUE_RESOLVED"
	EventImpactAnalyzed       EventType = "IMPACT_ANALYZED"
	EventCorrectiveEvent      EventType = "CORRECTIVE_EVENT"
)

// TimelineAggregateType 是事件所属的正式对象类型。
// 它与 Knowledge command receipt 的 AggregateType 分离，因为 Timeline 还投影 Proposal、Commit 与 Health。
type TimelineAggregateType string

const (
	TimelineAggregateProposal     TimelineAggregateType = "PROPOSAL"
	TimelineAggregateApproval     TimelineAggregateType = "APPROVAL"
	TimelineAggregateCommit       TimelineAggregateType = "GIT_COMMIT"
	TimelineAggregateTopic        TimelineAggregateType = "TOPIC"
	TimelineAggregateClaim        TimelineAggregateType = "CLAIM"
	TimelineAggregateRelation     TimelineAggregateType = "RELATION"
	TimelineAggregateConflict     TimelineAggregateType = "CONFLICT"
	TimelineAggregateDocument     TimelineAggregateType = "DOCUMENT"
	TimelineAggregateRevision     TimelineAggregateType = "ARTICLE_REVISION"
	TimelineAggregateHealthIssue  TimelineAggregateType = "HEALTH_ISSUE"
	TimelineAggregateImpactReport TimelineAggregateType = "IMPACT_REPORT"
)

// EventCorrelation 保存跨模块事实的稳定 ID，不保存正文或 Secret。
type EventCorrelation struct {
	ProposalID    *foundation.ID `json:"proposal_id,omitempty"`
	ApprovalID    *foundation.ID `json:"approval_id,omitempty"`
	WorkflowRunID *foundation.ID `json:"workflow_run_id,omitempty"`
	AuditEventID  *foundation.ID `json:"audit_event_id,omitempty"`
	GitCommitRef  string         `json:"git_commit_ref,omitempty"`
}

// KnowledgeEvent 是正式状态变化的不可变 Timeline 投影。
// 客户端不能直接创建任意事件；只有领域事务或受信任 projector 才能调用 Append。
type KnowledgeEvent struct {
	ID             foundation.ID
	WorkspaceID    foundation.ID
	EventType      EventType
	AggregateType  TimelineAggregateType
	AggregateID    *foundation.ID
	SourceEventRef string
	SourceRef      string
	EventVersion   int
	SchemaVersion  string
	Summary        string
	Payload        json.RawMessage
	Correlation    EventCorrelation
	OccurredAt     time.Time
	CreatedAt      time.Time
}

// TimelineFilter 是 Workspace-scoped Timeline 查询过滤器。
type TimelineFilter struct {
	EventTypes     []EventType
	AggregateType  TimelineAggregateType
	AggregateID    *foundation.ID
	SourceEventRef string
	OccurredAfter  *time.Time
	OccurredBefore *time.Time
}

// TimelinePosition 是 (occurred_at DESC, id DESC) keyset 游标位置。
type TimelinePosition struct {
	OccurredAt time.Time
	ID         foundation.ID
}

// TimelineQuery 是已完成边界校验的 Repository 查询。
type TimelineQuery struct {
	WorkspaceID foundation.ID
	Filter      TimelineFilter
	Limit       int
	After       *TimelinePosition
}

// TimelinePage 是有界 Timeline 查询结果。
type TimelinePage struct {
	Items   []KnowledgeEvent
	HasMore bool
	Next    *TimelinePosition
}

// ImpactObjectType 是受影响对象的多态类型。
type ImpactObjectType string

const (
	ImpactObjectTopic       ImpactObjectType = "TOPIC"
	ImpactObjectClaim       ImpactObjectType = "CLAIM"
	ImpactObjectRelation    ImpactObjectType = "RELATION"
	ImpactObjectConflict    ImpactObjectType = "CONFLICT"
	ImpactObjectHealthIssue ImpactObjectType = "HEALTH_ISSUE"
	ImpactObjectProposal    ImpactObjectType = "PROPOSAL"
	ImpactObjectRevision    ImpactObjectType = "ARTICLE_REVISION"
	ImpactObjectAudit       ImpactObjectType = "AUDIT_EVENT"
)

// ImpactAction 描述下游对象需要的下一步动作；它不是写命令。
type ImpactAction string

const (
	ImpactActionReview          ImpactAction = "REVIEW"
	ImpactActionReindex         ImpactAction = "REINDEX"
	ImpactActionResolveConflict ImpactAction = "RESOLVE_CONFLICT"
	ImpactActionRefreshHealth   ImpactAction = "REFRESH_HEALTH"
	ImpactActionNoop            ImpactAction = "NO_ACTION"
)

// ImpactObject 是 Impact Analysis 的只读影响项。
type ImpactObject struct {
	Type             ImpactObjectType `json:"type"`
	ID               foundation.ID    `json:"id"`
	WorkspaceID      foundation.ID    `json:"workspace_id"`
	Version          int64            `json:"version"`
	Action           ImpactAction     `json:"action"`
	Reason           string           `json:"reason"`
	RequiresProposal bool             `json:"requires_proposal"`
}

// ImpactReportStatus 是 Impact 报告的生命周期。
type ImpactReportStatus string

const (
	ImpactReportReady  ImpactReportStatus = "READY"
	ImpactReportStale  ImpactReportStatus = "STALE"
	ImpactReportFailed ImpactReportStatus = "FAILED"
)

// ImpactReport 是可持久化的分析结果，不是 Knowledge 事实源。
type ImpactReport struct {
	ID             foundation.ID
	WorkspaceID    foundation.ID
	SourceEventID  foundation.ID
	SourceEventRef string
	SourceVersion  int64
	Status         ImpactReportStatus
	Objects        []ImpactObject
	Summary        map[string]int
	Fingerprint    string
	ErrorCode      string
	StaleReason    string
	GeneratedAt    time.Time
	CreatedAt      time.Time
	Version        int64
}

// ProposalDraft 是 Impact Analysis 产生的待审批建议；它不执行写回。
type ProposalDraft struct {
	ID                         string           `json:"id,omitempty"`
	WorkspaceID                foundation.ID    `json:"workspace_id"`
	SourceEventID              foundation.ID    `json:"source_event_id"`
	Operation                  string           `json:"operation"`
	TargetType                 ImpactObjectType `json:"target_type"`
	TargetID                   foundation.ID    `json:"target_id"`
	BaseVersion                int64            `json:"base_version"`
	Reason                     string           `json:"reason"`
	RequiresApproval           bool             `json:"requires_approval"`
	RequiresWriteAuthorization bool             `json:"requires_write_authorization"`
}

// Validate 校验不可变 Knowledge Event 的身份、摘要和关联边界。
func (event KnowledgeEvent) Validate() error {
	if !validID(event.ID) || !validID(event.WorkspaceID) || event.ID == event.WorkspaceID {
		return timelineInvalid("knowledge event identity is invalid")
	}
	if !validEventType(event.EventType) {
		return timelineInvalid("knowledge event type is invalid")
	}
	if !validTimelineAggregateType(event.AggregateType) {
		return timelineInvalid("knowledge event aggregate type is invalid")
	}
	if event.AggregateID != nil && !validID(*event.AggregateID) {
		return timelineInvalid("knowledge event aggregate id is invalid")
	}
	if !validSafeText(event.SourceEventRef, 512) || !validSafeText(event.SourceRef, 512) {
		return timelineInvalid("knowledge event source reference is invalid")
	}
	if event.EventVersion < 1 || event.SchemaVersion != KnowledgeEventSchemaVersion {
		return timelineInvalid("knowledge event schema or version is invalid")
	}
	if !validSummary(event.Summary, MaxKnowledgeEventSummaryBytes) {
		return timelineInvalid("knowledge event summary is invalid")
	}
	payload, err := normalizeJSONObject(event.Payload, MaxKnowledgeEventPayloadBytes)
	if err != nil || !bytes.Equal(payload, event.Payload) {
		return timelineInvalid("knowledge event payload is not canonical")
	}
	if err := validateEventCorrelation(event.Correlation); err != nil {
		return timelineInvalid("knowledge event correlation is unsafe")
	}
	if !validTimelineTime(event.OccurredAt) || !validTimelineTime(event.CreatedAt) || event.CreatedAt.Before(event.OccurredAt) {
		return timelineInvalid("knowledge event timestamps are invalid")
	}
	return nil
}

// ValidateTimelineQuery 校验 Timeline 查询并复制用户切片，避免 Repository 修改调用方输入。
func ValidateTimelineQuery(query TimelineQuery) error {
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > MaxTimelineLimit {
		return timelineInvalid("timeline query boundary is invalid")
	}
	if query.Filter.AggregateID != nil && !validID(*query.Filter.AggregateID) {
		return timelineInvalid("timeline aggregate id is invalid")
	}
	if query.Filter.AggregateType != "" && !validTimelineAggregateType(query.Filter.AggregateType) {
		return timelineInvalid("timeline aggregate type is invalid")
	}
	if query.Filter.SourceEventRef != "" && !validText(query.Filter.SourceEventRef, 512) {
		return timelineInvalid("timeline source event reference is invalid")
	}
	if len(query.Filter.EventTypes) > MaxTimelineEventTypeFilters {
		return timelineInvalid("timeline event type filter exceeds the bounded range")
	}
	seen := make(map[EventType]struct{}, len(query.Filter.EventTypes))
	for _, eventType := range query.Filter.EventTypes {
		if !validEventType(eventType) {
			return timelineInvalid("timeline event type filter is invalid")
		}
		if _, duplicate := seen[eventType]; duplicate {
			return timelineInvalid("timeline event type filter is duplicated")
		}
		seen[eventType] = struct{}{}
	}
	if query.Filter.OccurredAfter != nil && !validTimelineTime(*query.Filter.OccurredAfter) {
		return timelineInvalid("timeline lower time boundary is invalid")
	}
	if query.Filter.OccurredBefore != nil && !validTimelineTime(*query.Filter.OccurredBefore) {
		return timelineInvalid("timeline upper time boundary is invalid")
	}
	if query.Filter.OccurredAfter != nil && query.Filter.OccurredBefore != nil && query.Filter.OccurredAfter.After(*query.Filter.OccurredBefore) {
		return timelineInvalid("timeline time range is inverted")
	}
	if query.After != nil && (!validID(query.After.ID) || !validTimelineTime(query.After.OccurredAt)) {
		return timelineInvalid("timeline cursor position is invalid")
	}
	return nil
}

// ValidateImpactReport 校验 Impact 报告不变量和跨 Workspace 绑定。
func ValidateImpactReport(report ImpactReport) error {
	if !validID(report.ID) || !validID(report.WorkspaceID) || !validID(report.SourceEventID) || report.ID == report.WorkspaceID || report.ID == report.SourceEventID {
		return timelineInvalid("impact report identity is invalid")
	}
	if !validSafeText(report.SourceEventRef, 512) || report.SourceVersion < 1 || report.Version < 1 || report.SchemaVersion() != ImpactReportSchemaVersion {
		return timelineInvalid("impact report binding is invalid")
	}
	if report.Status != ImpactReportReady && report.Status != ImpactReportStale && report.Status != ImpactReportFailed {
		return timelineInvalid("impact report status is invalid")
	}
	if len(report.Objects) > MaxImpactObjects {
		return timelineInvalid("impact report contains too many objects")
	}
	seen := make(map[string]struct{}, len(report.Objects))
	for _, object := range report.Objects {
		if err := ValidateImpactObject(object); err != nil || object.WorkspaceID != report.WorkspaceID {
			return timelineInvalid("impact object is invalid")
		}
		key := string(object.Type) + ":" + string(object.ID)
		if _, duplicate := seen[key]; duplicate {
			return timelineInvalid("impact report contains duplicate objects")
		}
		seen[key] = struct{}{}
	}
	if !reflect.DeepEqual(report.Summary, SummarizeImpactObjects(report.Objects)) {
		return timelineInvalid("impact report summary is inconsistent")
	}
	if report.Status == ImpactReportFailed && !validSafeText(report.ErrorCode, 256) {
		return timelineInvalid("failed impact report requires an error code")
	}
	if report.Status != ImpactReportFailed && report.ErrorCode != "" {
		return timelineInvalid("non-failed impact report cannot carry an error code")
	}
	if report.Status == ImpactReportStale && (report.StaleReason == "" || !validSummary(report.StaleReason, MaxKnowledgeEventSummaryBytes)) {
		return timelineInvalid("stale impact report requires a reason")
	}
	if report.Status != ImpactReportStale && report.StaleReason != "" {
		return timelineInvalid("non-stale impact report cannot carry a stale reason")
	}
	if !validTimelineTime(report.GeneratedAt) || !validTimelineTime(report.CreatedAt) || report.CreatedAt.Before(report.GeneratedAt) {
		return timelineInvalid("impact report timestamps are invalid")
	}
	if len(report.Fingerprint) != 64 || strings.ToLower(report.Fingerprint) != report.Fingerprint {
		return timelineInvalid("impact report fingerprint is invalid")
	}
	if _, err := hex.DecodeString(report.Fingerprint); err != nil {
		return timelineInvalid("impact report fingerprint is invalid")
	}
	return nil
}

// CanonicalTimelineTime 将 Timeline/Impact 时间规范为 PostgreSQL 可无损持久化的 UTC 微秒。
func CanonicalTimelineTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}

// ValidateImpactObject 校验只读影响项的身份、枚举和值边界。
func ValidateImpactObject(object ImpactObject) error {
	if !validID(object.ID) || !validID(object.WorkspaceID) || object.Version < 1 || !validImpactObjectType(object.Type) || !validImpactAction(object.Action) || !validSummary(object.Reason, MaxKnowledgeEventSummaryBytes) {
		return timelineInvalid("impact object is invalid")
	}
	if object.Action == ImpactActionNoop && object.RequiresProposal {
		return timelineInvalid("no-op impact object cannot require a proposal")
	}
	return nil
}

// ValidateProposalDraft 校验 Impact 只生成待审批建议且不会绕过写授权。
func ValidateProposalDraft(draft ProposalDraft) error {
	if !validID(draft.WorkspaceID) || !validID(draft.SourceEventID) || !validID(draft.TargetID) || !validImpactObjectType(draft.TargetType) || !validImpactAction(ImpactAction(draft.Operation)) || draft.Operation == string(ImpactActionNoop) || draft.BaseVersion < 1 || !validSummary(draft.Reason, MaxKnowledgeEventSummaryBytes) || !draft.RequiresApproval || !draft.RequiresWriteAuthorization {
		return timelineInvalid("impact proposal draft is invalid")
	}
	if draft.ID != "" && !validSafeText(draft.ID, 256) {
		return timelineInvalid("impact proposal draft identity is invalid")
	}
	return nil
}

// SummarizeImpactObjects 计算报告的稳定对象与动作计数。
func SummarizeImpactObjects(objects []ImpactObject) map[string]int {
	result := make(map[string]int)
	for _, object := range objects {
		result[string(object.Type)]++
		result["action:"+string(object.Action)]++
		if object.RequiresProposal {
			result["requires_proposal"]++
		}
	}
	return result
}

// SchemaVersion 返回 ImpactReport 的固定结构版本，便于避免可变字段重复存储。
func (report ImpactReport) SchemaVersion() string { return ImpactReportSchemaVersion }

// ComputeImpactFingerprint 为相同源事件和影响对象生成稳定指纹。
func ComputeImpactFingerprint(sourceEventID foundation.ID, sourceVersion int64, objects []ImpactObject) (string, error) {
	if !validID(sourceEventID) || sourceVersion < 1 || len(objects) > MaxImpactObjects {
		return "", timelineInvalid("impact fingerprint input is invalid")
	}
	canonical := append([]ImpactObject(nil), objects...)
	for _, object := range canonical {
		if err := ValidateImpactObject(object); err != nil {
			return "", err
		}
	}
	sort.Slice(canonical, func(left, right int) bool {
		leftKey := string(canonical[left].Type) + ":" + string(canonical[left].ID)
		rightKey := string(canonical[right].Type) + ":" + string(canonical[right].ID)
		if leftKey == rightKey {
			return canonical[left].Version < canonical[right].Version
		}
		return leftKey < rightKey
	})
	for index := 1; index < len(canonical); index++ {
		if canonical[index-1].Type == canonical[index].Type && canonical[index-1].ID == canonical[index].ID {
			return "", timelineInvalid("impact fingerprint contains duplicate objects")
		}
	}
	payload := struct {
		SchemaVersion string         `json:"schema_version"`
		SourceEventID foundation.ID  `json:"source_event_id"`
		SourceVersion int64          `json:"source_version"`
		Objects       []ImpactObject `json:"objects"`
	}{ImpactReportSchemaVersion, sourceEventID, sourceVersion, canonical}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func normalizeJSONObject(raw json.RawMessage, max int) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	if len(raw) > max || !utf8.Valid(raw) || !json.Valid(raw) || bytes.TrimSpace(raw)[0] != '{' {
		return nil, errors.New("json object is invalid")
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("json object is null")
	}
	canonical, err := json.Marshal(value)
	if err != nil || redaction.ContainsSecret(string(canonical)) || redaction.ContainsAbsolutePath(string(canonical)) {
		return nil, errors.New("json object contains unsafe text")
	}
	return canonical, nil
}

func validText(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n")
}

func validSafeText(value string, max int) bool {
	return validText(value, max) && utf8.ValidString(value) && !redaction.ContainsSecret(value) && !redaction.ContainsAbsolutePath(value)
}

func validSummary(value string, max int) bool {
	return len(value) <= max && strings.TrimSpace(value) == value && utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n") && !containsControl(value) && !redaction.ContainsSecret(value) && !redaction.ContainsAbsolutePath(value)
}

func validateEventCorrelation(correlation EventCorrelation) error {
	for _, id := range []*foundation.ID{correlation.ProposalID, correlation.ApprovalID, correlation.WorkflowRunID, correlation.AuditEventID} {
		if id != nil && !validID(*id) {
			return errors.New("correlation id is invalid")
		}
	}
	if correlation.GitCommitRef != "" && !validSafeText(correlation.GitCommitRef, 512) {
		return errors.New("git commit reference is invalid")
	}
	raw, err := json.Marshal(correlation)
	if err != nil || len(raw) > MaxKnowledgeEventCorrelationBytes || redaction.ContainsSecret(string(raw)) || redaction.ContainsAbsolutePath(string(raw)) {
		return errors.New("correlation document is unsafe")
	}
	return nil
}

func validImpactObjectType(value ImpactObjectType) bool {
	switch value {
	case ImpactObjectTopic, ImpactObjectClaim, ImpactObjectRelation, ImpactObjectConflict, ImpactObjectHealthIssue, ImpactObjectProposal, ImpactObjectRevision, ImpactObjectAudit:
		return true
	default:
		return false
	}
}

func validImpactAction(value ImpactAction) bool {
	switch value {
	case ImpactActionReview, ImpactActionReindex, ImpactActionResolveConflict, ImpactActionRefreshHealth, ImpactActionNoop:
		return true
	default:
		return false
	}
}

func validEventType(value EventType) bool {
	switch value {
	case EventProposalCreated, EventApprovalGranted, EventApprovalRejected, EventGitCommitted,
		EventRelationConfirmed, EventRelationDeprecated, EventConflictOpened, EventConflictTransitioned,
		EventConflictResolved, EventVersionPublished, EventVersionSuperseded, EventHealthIssueDetected,
		EventHealthIssueResolved, EventImpactAnalyzed, EventCorrectiveEvent:
		return true
	default:
		return false
	}
}

func validTimelineAggregateType(value TimelineAggregateType) bool {
	switch value {
	case TimelineAggregateProposal, TimelineAggregateApproval, TimelineAggregateCommit, TimelineAggregateTopic,
		TimelineAggregateClaim, TimelineAggregateRelation, TimelineAggregateConflict, TimelineAggregateDocument,
		TimelineAggregateRevision, TimelineAggregateHealthIssue, TimelineAggregateImpactReport:
		return true
	default:
		return false
	}
}

func validTimelineTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond()%int(time.Microsecond) == 0
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func timelineInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeTimelineInvalid, false, errors.New(message))
}
