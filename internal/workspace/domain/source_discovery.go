package domain

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"io/fs"
	"strings"
	"time"
	"unicode"
)

// SourceDiscoveryPage 按目录遍历顺序前进，包括失败
// 和不受支持的条目。后续遍历会重访新增在游标之前的路径。
type SourceDiscoveryPage struct {
	Files  []ScannedFile
	After  string
	Done   bool
	Failed int
}

type SourceDiscoveryPaths struct {
	Failures        []DiscoveryObservation
	ReadDirectories []string
	Paths           []string
	After           string
	Done            bool
	Failed          int
}

type SourceDiscoveryScanner interface {
	ScanPage(context.Context, string, string, int) (SourceDiscoveryPaths, error)
	ObserveSource(context.Context, string, string) (ScannedFile, error)
}

// SourceDiscoverySession 在分页完成前持续持有已授权的
// 物理根目录。注册元数据前必须再次校验。
type SourceDiscoverySession interface {
	FileScanner
	SourceDiscoveryScanner
	Revalidate(context.Context) error
	Close() error
}

type SourceDiscoveryBinder interface {
	BindSourceDiscovery(context.Context, string, func(context.Context) error) (SourceDiscoverySession, error)
}

// DiscoveryObservation 仅包含已观察到的安全路径事实。Code 为空
// 表示已证实成功，绝不表示未被扫描到。WALK 成功仅对应
// 先前的目录读取失败；REGISTER 成功须在采集和数据库操作之后。
type DiscoveryObservation struct {
	Path  string
	Stage string
	Code  string
}
type DiscoveryFailure struct {
	WorkspaceID    foundation.ID `json:"workspace_id"`
	BindingVersion int64         `json:"binding_version"`
	Path           string        `json:"path"`
	Stage          string        `json:"stage"`
	Code           string        `json:"code"`
	Status         string        `json:"status"`
	FailureCount   int64         `json:"failure_count"`
	LastFailedAt   time.Time     `json:"last_failed_at"`
	RecoveredAt    *time.Time    `json:"recovered_at"`
}
type DiscoveryFailurePage struct {
	WorkspaceID    foundation.ID      `json:"workspace_id"`
	BindingVersion int64              `json:"binding_version"`
	Items          []DiscoveryFailure `json:"items"`
	NextCursor     string             `json:"next_cursor"`
}
type DiscoveryFailureRepository interface {
	RecordDiscoveryObservations(context.Context, Workspace, []DiscoveryObservation) error
	ListDiscoveryFailures(context.Context, foundation.ID, string, int) (DiscoveryFailurePage, error)
}

func ValidDiscoveryPath(value string) bool {
	if len(value) > 1024 || !fs.ValidPath(value) || value == "." || strings.ContainsAny(value, "\\:\x00") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".git" || part == ".knowledge" || part == "tmp" || part == ".tmp" {
			return false
		}
	}
	return true
}
func (o DiscoveryObservation) Valid() bool {
	if !ValidDiscoveryPath(o.Path) {
		return false
	}
	switch o.Stage {
	case "WALK":
		return o.Code == "" || o.Code == "DIRECTORY_READ_FAILED"
	case "OBSERVE":
		return o.Code == "FILE_OBSERVATION_FAILED"
	case "REGISTER":
		return o.Code == "" || o.Code == "SOURCE_REGISTRATION_FAILED"
	}
	return false
}
