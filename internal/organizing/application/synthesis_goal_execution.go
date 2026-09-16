package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type GoalSelectionStatus string

const (
	GoalSelectionPending          GoalSelectionStatus = "PENDING"
	GoalSelectionRunning          GoalSelectionStatus = "RUNNING"
	GoalSelectionSucceeded        GoalSelectionStatus = "SUCCEEDED"
	GoalSelectionFailed           GoalSelectionStatus = "FAILED"
	GoalSelectionRecoveryRequired GoalSelectionStatus = "RECOVERY_REQUIRED"
)

// 选择记录是一个不可变目录项的持久切片。成功表示知识点选择完成，
// 不表示来源证据或笔记已发布。
type SynthesisGoalSelection struct {
	ID, WorkspaceID, RequestID, CatalogBatchID                   foundation.ID
	SourceOrdinal, PointOffset, PointCount                       int
	PayloadHash                                                  string
	Status                                                       GoalSelectionStatus
	ScheduledWorkflowID, WorkflowRunID, NodeRunID, NodeAttemptID foundation.ID
	ModelInputHash                                               string
	ModelRunID                                                   foundation.ID
	ModelOutput                                                  []byte
	ErrorCode                                                    string
	Retryable                                                    bool
	Version                                                      int64
	CreatedAt, UpdatedAt                                         time.Time
}

type ClaimGoalSelectionCommand struct {
	WorkspaceID, SelectionID                foundation.ID
	ExpectedVersion                         int64
	WorkflowRunID, NodeRunID, NodeAttemptID foundation.ID
	ModelInputHash                          string
}
type CompleteGoalSelectionCommand struct {
	WorkspaceID, SelectionID foundation.ID
	ExpectedVersion          int64
	ModelRunID               foundation.ID
	ModelOutput              []byte
}
type FailGoalSelectionCommand struct {
	WorkspaceID, SelectionID foundation.ID
	ExpectedVersion          int64
	Status                   GoalSelectionStatus
	ErrorCode                string
	Retryable                bool
}

// 所属模块从不可变目录重建输入；调用方在调度或完成选择时不能提供或替换元数据。
type SynthesisGoalSelectionStore interface {
	PrepareGoalSelections(context.Context, foundation.ID, foundation.ID, int64) ([]SynthesisGoalSelection, error)
	GetGoalSelection(context.Context, foundation.ID, foundation.ID) (SynthesisGoalSelection, error)
	ReadGoalSelectionInput(context.Context, foundation.ID, foundation.ID) (SynthesisGoalSelectionInput, error)
	ClaimGoalSelection(context.Context, ClaimGoalSelectionCommand) (SynthesisGoalSelection, error)
	CompleteGoalSelection(context.Context, CompleteGoalSelectionCommand) (SynthesisGoalSelection, error)
	FailGoalSelection(context.Context, FailGoalSelectionCommand) (SynthesisGoalSelection, error)
}
