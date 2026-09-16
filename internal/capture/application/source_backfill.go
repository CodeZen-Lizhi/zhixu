package application

import "context"

// SourceBackfillScheduler 将当前工作区文件纳入既有
// Capture 流水线。调度有界且持久；重复扫描不会
// 重试失败的模型调用，也不会重复入队同一来源版本。
type SourceBackfillScheduler interface {
	BackfillSources(context.Context, int) (int, error)
}
