package domain

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	// RetentionWindow 是 Server Event 的逻辑重放保留期。
	RetentionWindow = 24 * time.Hour
	maxSummaryCount = int64(1_000_000_000)
)

var (
	summaryTokenPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	eventTypePattern    = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
	referencePattern    = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}:[a-z0-9][a-z0-9_.:-]*$`)
)

// PayloadSummary 只允许浏览器失效查询所需的稳定 ID、状态和计数。
type PayloadSummary struct {
	SourceEventID     *foundation.ID `json:"source_event_id,omitempty"`
	ConversationID    *foundation.ID `json:"conversation_id,omitempty"`
	WorkflowRunID     *foundation.ID `json:"workflow_run_id,omitempty"`
	QuestionID        *foundation.ID `json:"question_id,omitempty"`
	AnswerID          *foundation.ID `json:"answer_id,omitempty"`
	ModelRunID        *foundation.ID `json:"model_run_id,omitempty"`
	Status            string         `json:"status,omitempty"`
	PublicationStatus string         `json:"publication_status,omitempty"`
	ResultType        string         `json:"result_type,omitempty"`
	Stage             string         `json:"stage,omitempty"`
	ScopeKind         string         `json:"scope_kind,omitempty"`
	CandidateCount    *int64         `json:"candidate_count,omitempty"`
	SelectedCount     *int64         `json:"selected_count,omitempty"`
	ConflictCount     *int64         `json:"conflict_count,omitempty"`
	DegradationCount  *int64         `json:"degradation_count,omitempty"`
	RewriteCount      *int64         `json:"rewrite_count,omitempty"`
	CitationCount     *int64         `json:"citation_count,omitempty"`
}

// Validate 校验摘要不含正文型自由文本且所有值有界。
func (summary PayloadSummary) Validate() error {
	for _, id := range []*foundation.ID{
		summary.SourceEventID, summary.ConversationID, summary.WorkflowRunID,
		summary.QuestionID, summary.AnswerID, summary.ModelRunID,
	} {
		if id == nil {
			continue
		}
		parsed, err := foundation.ParseID(string(*id))
		if err != nil || parsed != *id {
			return invalid(ErrorCodePayloadSummaryInvalid, "SSE payload summary identity is invalid", err)
		}
	}
	for _, token := range []string{summary.Status, summary.PublicationStatus, summary.ResultType, summary.Stage} {
		if token != "" && !summaryTokenPattern.MatchString(token) {
			return invalid(ErrorCodePayloadSummaryInvalid, "SSE payload summary token is invalid", nil)
		}
	}
	if summary.ScopeKind != "" && summary.ScopeKind != "collection" && summary.ScopeKind != "workspace_attachments" {
		return invalid(ErrorCodePayloadSummaryInvalid, "SSE payload summary scope kind is invalid", nil)
	}
	for _, count := range []*int64{
		summary.CandidateCount, summary.SelectedCount, summary.ConflictCount,
		summary.DegradationCount, summary.RewriteCount, summary.CitationCount,
	} {
		if count != nil && (*count < 0 || *count > maxSummaryCount) {
			return invalid(ErrorCodePayloadSummaryInvalid, "SSE payload summary count is invalid", nil)
		}
	}
	return nil
}

// DecodePayloadSummary 从不可信 JSONB readback 严格解码摘要白名单。
func DecodePayloadSummary(raw []byte) (PayloadSummary, error) {
	limits := foundationstrictjson.Limits{
		MaxDocumentBytes: 16 * 1024,
		MaxDepth:         2,
		MaxStringBytes:   256,
		MaxArrayItems:    0,
		MaxObjectFields:  17,
	}
	summary, err := foundationstrictjson.DecodeObject(raw, limits, PayloadSummary.Validate)
	if err != nil {
		return PayloadSummary{}, invalid(ErrorCodePayloadSummaryInvalid, "SSE payload summary is invalid", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return PayloadSummary{}, invalid(ErrorCodePayloadSummaryInvalid, "SSE payload summary is invalid", err)
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return PayloadSummary{}, invalid(ErrorCodePayloadSummaryInvalid, "SSE payload summary cannot contain null", nil)
		}
	}
	return summary, nil
}

// ServerEvent 是 PostgreSQL 事件投影的完整持久记录。
type ServerEvent struct {
	Seq             int64
	WorkspaceID     foundation.ID
	ConversationID  *foundation.ID
	WorkflowRunID   *foundation.ID
	Type            string
	ResourceRef     string
	ResourceVersion int64
	PayloadSummary  PayloadSummary
	SchemaVersion   int
	SourceEventRef  string
	OccurredAt      time.Time
	ExpiresAt       time.Time
}

// Validate 校验持久事件的身份、摘要绑定、版本和固定保留期。
func (event ServerEvent) Validate() error {
	workspaceID, err := foundation.ParseID(string(event.WorkspaceID))
	if err != nil || workspaceID != event.WorkspaceID || event.Seq <= 0 || event.ResourceVersion <= 0 || event.SchemaVersion <= 0 ||
		!eventTypePattern.MatchString(event.Type) || !validReference(event.ResourceRef) || !validReference(event.SourceEventRef) ||
		event.OccurredAt.IsZero() || event.ExpiresAt.IsZero() || !event.ExpiresAt.Equal(event.OccurredAt.Add(RetentionWindow)) {
		return invalid(ErrorCodeEventInvalid, "SSE server event is invalid", err)
	}
	for _, id := range []*foundation.ID{event.ConversationID, event.WorkflowRunID} {
		if id == nil {
			continue
		}
		parsed, parseErr := foundation.ParseID(string(*id))
		if parseErr != nil || parsed != *id {
			return invalid(ErrorCodeEventInvalid, "SSE server event binding is invalid", parseErr)
		}
	}
	if err := event.PayloadSummary.Validate(); err != nil {
		return err
	}
	if event.PayloadSummary.ConversationID != nil && (event.ConversationID == nil || *event.PayloadSummary.ConversationID != *event.ConversationID) {
		return inconsistent(ErrorCodeEventInvalid, "SSE conversation summary binding is inconsistent")
	}
	if event.PayloadSummary.WorkflowRunID != nil && (event.WorkflowRunID == nil || *event.PayloadSummary.WorkflowRunID != *event.WorkflowRunID) {
		return inconsistent(ErrorCodeEventInvalid, "SSE workflow summary binding is inconsistent")
	}
	return nil
}

// Envelope 是发往浏览器的强类型通知，不包含来源幂等键或过期时间。
type Envelope struct {
	SchemaVersion   int            `json:"schema_version"`
	ID              string         `json:"id"`
	Type            string         `json:"type"`
	OccurredAt      time.Time      `json:"occurred_at"`
	WorkspaceID     foundation.ID  `json:"workspace_id"`
	ResourceRef     string         `json:"resource_ref"`
	ResourceVersion int64          `json:"resource_version"`
	PayloadSummary  PayloadSummary `json:"payload_summary"`
}

// Envelope 将持久记录投影为可公开的 SSE Envelope。
func (event ServerEvent) Envelope() (Envelope, error) {
	if err := event.Validate(); err != nil {
		return Envelope{}, err
	}
	return Envelope{
		SchemaVersion: event.SchemaVersion,
		ID:            strconv.FormatInt(event.Seq, 10), Type: event.Type,
		OccurredAt: event.OccurredAt.UTC(), WorkspaceID: event.WorkspaceID,
		ResourceRef: event.ResourceRef, ResourceVersion: event.ResourceVersion,
		PayloadSummary: event.PayloadSummary,
	}, nil
}

func validReference(value string) bool {
	return len(value) <= 256 && referencePattern.MatchString(value)
}
