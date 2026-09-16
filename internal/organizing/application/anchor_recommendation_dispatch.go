package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

// AnchorRecommendationDispatchStore 独立管理调度身份，与模型尝试绑定分开。
// PENDING 请求在节点领取前保持 PENDING，避免把已调度运行误认为已完成的模型调用。
type AnchorRecommendationDispatchStore interface {
	ReconcileAnchorRecommendationWorkflows(context.Context, int) (int, error)
	ClaimPendingAnchorRecommendationScoped(context.Context, foundation.TransactionScope) (AnchorRecommendationRequest, bool, error)
	BindAnchorRecommendationWorkflowScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID) error
	AnchorRecommendationScheduledWorkflow(context.Context, foundation.ID, foundation.ID) (foundation.ID, error)
	FailScheduledAnchorRecommendation(context.Context, foundation.ID, foundation.ID, foundation.ID, string, bool) (AnchorRecommendationRequest, error)
}

type AnchorRecommendationStartInput struct {
	ExpectedVersion int64         `json:"expected_version"`
	RequestID       foundation.ID `json:"request_id"`
}

func DecodeAnchorRecommendationStartInput(raw []byte) (AnchorRecommendationStartInput, error) {
	value, err := strictjson.DecodeObject[AnchorRecommendationStartInput](raw, strictjson.Limits{MaxDocumentBytes: 1024, MaxDepth: 2, MaxStringBytes: 128, MaxArrayItems: 1, MaxObjectFields: 2}, nil)
	if err != nil || !anchorID(value.RequestID) || value.ExpectedVersion < 1 {
		return AnchorRecommendationStartInput{}, AnchorInvalid()
	}
	return value, nil
}
