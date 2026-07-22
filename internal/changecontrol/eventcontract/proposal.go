// Package eventcontract 定义 Change Control 对外发布的 Proposal 状态事件契约。
package eventcontract

import (
	"fmt"
	"time"

	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ProposalApprovedEventType 表示 Proposal 已完成 Approval。
	ProposalApprovedEventType = "proposal.approved"
	// ProposalRejectedEventType 表示 Proposal 已被驳回。
	ProposalRejectedEventType = "proposal.rejected"
	// ProposalAppliedEventType 表示 knowledge_change 已安全应用。
	ProposalAppliedEventType = "proposal.applied"
)

// ProposalStatusRequest 构造只携带稳定标识和状态的 Proposal SSE 事件。
// sourceID 通常是 Approval ID；同一 sourceID、事件类型和版本只能产生一次事件。
func ProposalStatusRequest(
	workspaceID foundation.ID,
	proposalID foundation.ID,
	sourceID foundation.ID,
	eventType string,
	status string,
	resourceVersion int64,
	occurredAt time.Time,
) eventsdomain.AppendRequest {
	sourceEventID := sourceID
	return eventsdomain.AppendRequest{
		WorkspaceID:     workspaceID,
		Type:            eventType,
		ResourceRef:     fmt.Sprintf("proposal:%s", proposalID),
		ResourceVersion: resourceVersion,
		PayloadSummary: eventsdomain.PayloadSummary{
			SourceEventID: &sourceEventID,
			Status:        status,
		},
		SchemaVersion:  1,
		SourceEventRef: fmt.Sprintf("%s:%s:v1", eventType, sourceID),
		OccurredAt:     occurredAt,
	}
}
