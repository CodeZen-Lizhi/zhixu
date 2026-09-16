package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// Progress 是读取投影。Ready 表示每个不可变选择切片均已完成，
// 不表示原始证据可用或已经发布。
type SynthesisGoalSelectionProgress struct {
	Request                                                                                            SynthesisGoalRequest
	CatalogBatches, PreparedBatches, Selections, Pending, Running, Succeeded, Failed, RecoveryRequired int64
}

func (p SynthesisGoalSelectionProgress) Ready() bool {
	return p.Request.Status == SynthesisGoalCatalogReady && p.Request.CatalogBatches == p.CatalogBatches && p.CatalogBatches == p.PreparedBatches && p.Selections == p.Succeeded && p.Pending == 0 && p.Running == 0 && p.Failed == 0 && p.RecoveryRequired == 0
}

type GoalSelectionResultQuery struct {
	WorkspaceID, RequestID, AfterSelectionID foundation.ID
	Limit                                    int
}

// 每页包含完整选择切片，包括显式空选择。游标按选择记录推进，
// 所以选中知识点数组为空不能截断后续切片的枚举。
type GoalSelectionResult struct {
	SelectionID, ModelRunID foundation.ID
	Points                  []SynthesisGoalSelectedPoint
}
type GoalSelectionResultPage struct {
	Progress             SynthesisGoalSelectionProgress
	Items                []GoalSelectionResult
	NextAfterSelectionID foundation.ID
}
type GoalSelectionResultReader interface {
	ReadGoalSelectionProgress(context.Context, foundation.ID, foundation.ID) (SynthesisGoalSelectionProgress, error)
	ReadGoalSelectionResults(context.Context, GoalSelectionResultQuery) (GoalSelectionResultPage, error)
}

// 即使多个选中知识点共享同一原文片段，也要保留溯源信息。
type GoalSourcePointBinding struct {
	SelectionID, ModelRunID foundation.ID
	Locator                 KnowledgePointLocator
	Reason                  string
}
type GoalSourceMaterial struct {
	Reference domain.SynthesisSourceRef
	Points    []GoalSourcePointBinding
}
type GoalSourceMaterialPage struct {
	Progress             SynthesisGoalSelectionProgress
	Items                []GoalSourceMaterial
	NextAfterSelectionID foundation.ID
}
type GoalSourceMaterialReader interface {
	ReadGoalSourceMaterials(context.Context, GoalSelectionResultQuery) (GoalSourceMaterialPage, error)
}
