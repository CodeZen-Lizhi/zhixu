package application

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// SynthesisGoalCatalogItem 是按目标选择来源时使用的候选元数据。
// 知识点定位信息始终绑定到一个不可变画像修订。
type SynthesisGoalCatalogItem struct {
	Source    domain.SynthesisSourceVersion
	Title     string
	Directory SourceKnowledgeDirectory
}

type SynthesisGoalCatalogQuery struct {
	WorkspaceID     foundation.ID
	AfterSourceID   foundation.ID
	SourceVersionID foundation.ID
	Limit           int
}

type SynthesisGoalCatalogPage struct {
	Items             []SynthesisGoalCatalogItem
	NextAfterSourceID *foundation.ID
	DeferredCode      string
}

// 目录页不包含来源正文，也不宣称相关性。
// 模型须先按用户目标选择知识点，然后才能打开对应证据。
type SynthesisGoalCatalogReader interface {
	ReadSynthesisGoalCatalog(context.Context, SynthesisGoalCatalogQuery) (SynthesisGoalCatalogPage, error)
}

// SynthesisGoalCatalogReadiness 表达所属模块核验过的目录枚举等待原因。
// 非空错误码必须持久记录为发现延迟，不能将其冻结为空目录或部分目录。
type SynthesisGoalCatalogReadiness struct {
	DeferredCode string
}

// SynthesisGoalCatalogReadinessReader 仅报告请求游标之后的当前原始来源。
// 它不会排队任务或构造 source-ready 事件。
type SynthesisGoalCatalogReadinessReader interface {
	ReadSynthesisGoalCatalogReadiness(context.Context, SynthesisGoalCatalogQuery) (SynthesisGoalCatalogReadiness, error)
}

// 冻结元数据从已保存的画像身份读取，不授权使用来源；
// 选中的来源片段仍须在后续通过当前证据检查。
type SynthesisGoalCatalogSnapshotReader interface {
	ReadSynthesisGoalCatalogSnapshot(context.Context, SynthesisGoalCatalogBatch) ([]SynthesisGoalCatalogItem, error)
}
