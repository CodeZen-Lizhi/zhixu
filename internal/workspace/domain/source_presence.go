package domain

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// LocalSourcePresence 检查一个已注册路径，不读取其内容。
// 仅在证实路径不存在时返回 true；不可访问或不安全的路径返回错误。
type LocalSourcePresence interface {
	MissingLocalSource(context.Context, string, string) (bool, error)
}

// SourcePresencePage 限定单轮范围；After 也会越过仍存在的文件。
type SourcePresencePage struct {
	After   foundation.ID
	Checked int
	Removed int
}

// SourcePresenceRepository 将缺失检查与来源注册串行化。
type SourcePresenceRepository interface {
	ReconcileLocalSourcePresence(context.Context, foundation.ID, foundation.ID, int, LocalSourcePresence) (SourcePresencePage, error)
}
