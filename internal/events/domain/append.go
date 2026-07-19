package domain

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// AppendRequest 描述由调用方事务原子追加的 Server Event 稳定绑定。
type AppendRequest struct {
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
}

// Validate 校验待追加事件，不允许调用方控制序号或保留期。
func (request AppendRequest) Validate() error {
	candidate := ServerEvent{
		Seq: 1, WorkspaceID: request.WorkspaceID,
		ConversationID: request.ConversationID, WorkflowRunID: request.WorkflowRunID,
		Type: request.Type, ResourceRef: request.ResourceRef, ResourceVersion: request.ResourceVersion,
		PayloadSummary: request.PayloadSummary, SchemaVersion: request.SchemaVersion,
		SourceEventRef: request.SourceEventRef, OccurredAt: request.OccurredAt,
		ExpiresAt: request.OccurredAt.Add(RetentionWindow),
	}
	if err := candidate.Validate(); err != nil {
		return invalid(ErrorCodeAppendInvalid, "SSE append request is invalid", err)
	}
	return nil
}
