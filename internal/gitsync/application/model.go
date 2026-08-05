package application

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

// SaveConfigCommand 是公开配置更新命令。
type SaveConfigCommand struct {
	WorkspaceID      foundation.ID
	ExpectedRevision int64
	RemoteURL        string
	Branch           string
	AutoSync         bool
	SecretAction     domain.SecretAction
	IdempotencyKey   string
	Actor            string
}

// RemoveConfigCommand 是公开配置删除命令。
type RemoveConfigCommand struct {
	WorkspaceID      foundation.ID
	ExpectedRevision int64
	IdempotencyKey   string
	Actor            string
}

// TestConfigCommand 校验不持久化的远端配置草案。
type TestConfigCommand struct {
	WorkspaceID      foundation.ID
	ExpectedRevision int64
	RemoteURL        string
	Branch           string
	SecretAction     domain.SecretAction
}

// TestConfigResult 是不含密钥的连接测试成功投影。
type TestConfigResult struct {
	RemoteURL string
	Branch    string
}

// CreateRunCommand 将一次手动或自动运行入队。
type CreateRunCommand struct {
	WorkspaceID            foundation.ID
	Trigger                domain.Trigger
	RetryOfRunID           foundation.ID
	ExpectedConfigRevision int64
	IdempotencyKey         string
}

// RetryRunCommand 创建新 Git 运行或重试失败的索引 follow-up。
type RetryRunCommand struct {
	WorkspaceID     foundation.ID
	RunID           foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

// RunReceipt 是工作入队或重放的精确结果。
type RunReceipt struct {
	Run      domain.SyncRun
	Replayed bool
}

// ListRunsCommand 描述公开的不透明游标分页请求。
type ListRunsCommand struct {
	WorkspaceID foundation.ID
	Cursor      string
	Limit       int
}

// PublicRunPage 是携带不透明下一页游标的运行分页。
type PublicRunPage struct {
	Items      []domain.SyncRun
	NextCursor string
}

// StatusSnapshot 分离不含密钥的配置与当前运行。
type StatusSnapshot struct {
	Config     domain.RemoteConfig
	CurrentRun *domain.SyncRun
}

// Clock 是创建运行时使用的确定性时间边界。
type Clock interface{ Now() time.Time }
