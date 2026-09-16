package application

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"time"
)

// 请求、选择进度和生成账本是不同的事实。
// 目录就绪本身不表示主笔记已完成。
type SynthesisGoalView struct {
	Request              SynthesisGoalRequest
	Progress             SynthesisGoalSelectionProgress
	SelectedPoints       int64
	PreparationFailures  int64
	PreparationErrorCode string
	Processing           *SynthesisProcessing
	Candidate            *SynthesisGoalCandidate
}
type SynthesisGoalCandidate struct{ NoteID, RevisionID foundation.ID }
type SynthesisGoalPage struct {
	Items    []SynthesisGoalView
	NextTime *time.Time
	NextID   foundation.ID
}
type SynthesisGoalViewReader interface {
	GetSynthesisGoalView(context.Context, foundation.ID, foundation.ID) (SynthesisGoalView, error)
	ListSynthesisGoalViews(context.Context, SynthesisListQuery) (SynthesisGoalPage, error)
}
