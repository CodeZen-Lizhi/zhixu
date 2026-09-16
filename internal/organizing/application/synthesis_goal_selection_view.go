package application

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// 选择进度不包含模型输入、输出或不可变证据载荷。
type SynthesisGoalSelectionListQuery struct {
	WorkspaceID, RequestID, AfterID foundation.ID
	Limit                           int
}
type SynthesisGoalSelectionPage struct {
	Items       []SynthesisGoalSelection
	NextAfterID foundation.ID
}
type SynthesisGoalSelectionCommands interface {
	ListGoalSelections(context.Context, SynthesisGoalSelectionListQuery) (SynthesisGoalSelectionPage, error)
	GetGoalSelection(context.Context, foundation.ID, foundation.ID) (SynthesisGoalSelection, error)
	RetryGoalSelection(context.Context, RetryGoalSelectionCommand) (SynthesisGoalSelection, error)
}
