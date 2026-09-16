package application

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const MaxProfileDirectoryPage = 32

// ProfileDirectoryQuery 枚举当前已分析的来源，而非历史
// 版本。即使 AI 随后拒绝候选，AfterSourceID 仍会前进。
type ProfileDirectoryQuery struct {
	WorkspaceID     foundation.ID
	AfterSourceID   foundation.ID
	SourceVersionID foundation.ID
	Limit           int
}

// ProfileDirectoryEntry 仅携带元数据和证据定位信息。构建此目录
// 不会加载来源字节或规范分块内容。
type ProfileDirectoryEntry struct {
	SourceID          foundation.ID
	Title             string
	ContentArtifactID foundation.ID
	ContentHash       string
	View              ProfileView
}

type ProfileDirectoryPage struct {
	Items             []ProfileDirectoryEntry
	NextAfterSourceID *foundation.ID
}

// ProfileDirectoryReader 是候选发现边界。进入目录不代表
// 相关性已获批准，也不授权生成或发布。
type ProfileDirectoryReader interface {
	ListProfileDirectory(context.Context, ProfileDirectoryQuery) (ProfileDirectoryPage, error)
}
