package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// CollectionStatus 表示集合的生命周期状态。
type CollectionStatus string

const (
	// CollectionStatusActive 表示集合可以被执行和更新。
	CollectionStatusActive CollectionStatus = "ACTIVE"
	// CollectionStatusArchived 表示集合已归档且不可再修改。
	CollectionStatusArchived CollectionStatus = "ARCHIVED"
)

// Collection 是 Smart Collection 的持久化聚合投影；它不携带知识副本。
type Collection struct {
	ID                  foundation.ID
	WorkspaceID         foundation.ID
	Name                string
	NormalizedName      string
	Description         string
	QuerySchemaVersion  string
	QueryVersion        int64
	Query               domain.Query
	QueryHash           string
	ViewType            domain.ViewType
	ViewConfig          domain.ViewConfig
	Status              CollectionStatus
	CachedResultVersion *string
	LastExecutedAt      *time.Time
	Version             int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// CreateCommand 创建一个新的 Smart Collection。
type CreateCommand struct {
	WorkspaceID    foundation.ID
	Name           string
	Description    string
	Query          domain.Query
	ViewType       domain.ViewType
	ViewConfig     domain.ViewConfig
	IdempotencyKey string
}

// UpdateCommand 替换集合定义并使用 expected version 做 CAS。
type UpdateCommand struct {
	WorkspaceID     foundation.ID
	CollectionID    foundation.ID
	ExpectedVersion int64
	Name            string
	Description     string
	Query           domain.Query
	ViewType        domain.ViewType
	ViewConfig      domain.ViewConfig
	IdempotencyKey  string
}

// ArchiveCommand 将活动集合归档；归档后聚合不可修改。
type ArchiveCommand struct {
	WorkspaceID     foundation.ID
	CollectionID    foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

// ListQuery 查询 Workspace 内的有界集合列表。
type ListQuery struct {
	WorkspaceID foundation.ID
	Statuses    []CollectionStatus
	Limit       int
	Cursor      string
}

// CollectionListPage 是 Workspace 内 Smart Collection 的稳定 keyset 分页结果。
type CollectionListPage struct {
	Items      []Collection
	NextCursor string
}

// CollectionSourceSummary 是 Claim 来源的有界摘要；不携带正文或 Evidence。
type CollectionSourceSummary struct {
	SourceType  string    `json:"source_type"`
	FilePath    string    `json:"file_path"`
	SupportType string    `json:"support_type"`
	CreatedAt   time.Time `json:"created_at"`
}

// CollectionHealthSummary 是当前活动 Health Issue 的聚合投影。
type CollectionHealthSummary struct {
	Count       int      `json:"count"`
	MaxSeverity string   `json:"max_severity"`
	IssueTypes  []string `json:"issue_types,omitempty"`
	Summary     string   `json:"summary,omitempty"`
}

// CollectionItem 是统一 read model 的 Topic/Claim 判别结果；不携带全文或 Evidence 正文。
type CollectionItem struct {
	ObjectType string
	ID         foundation.ID
	TopicID    *foundation.ID
	Title      string
	Summary    string
	Status     string
	Confidence *float64
	// Applicability 仅对 Claim 返回 canonical JSON；Topic 为 nil。
	Applicability              json.RawMessage
	ApplicabilitySchemaVersion string
	ApplicabilityHash          string
	Aliases                    []string
	SourceSummaries            []CollectionSourceSummary
	RelationTypes              []string
	HealthSummary              *CollectionHealthSummary
	RelationType               *string
	HealthType                 *string
	SourceType                 *string
	FilePath                   *string
	// SortTextKey 保留 read model 的精确 text 排序键，不进入 HTTP 响应。
	SortTextKey string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ResultPage 是列表、表格和卡片共享的有界结果页。
type ResultPage struct {
	Items        []CollectionItem
	ExactCount   int64
	NextCursor   string
	QueryHash    string
	RevisionHash string
}

// DurableScanBinding 冻结跨进程扫描所依赖的 Collection 与 read-model 快照身份。
// 它不包含进程私有签名或查询结果副本。
type DurableScanBinding struct {
	WorkspaceID       foundation.ID
	CollectionID      foundation.ID
	CollectionVersion int64
	QueryHash         string
	ReadModelRevision string
	ExactCount        int64
}

// DurableScanKey 是可持久化的 Collection 扫描 keyset 边界。
type DurableScanKey struct {
	ObjectType string
	ID         foundation.ID
}

// DurableScanPair 是一个 source 与其后继 target 的稳定成员引用。
type DurableScanPair struct {
	Source DurableScanKey
	Target DurableScanKey
}

// DurableScanNode 是 durable page 同一数据库快照内的最小成员投影。
// Graph/Workflow 消费方不得在 page 返回后重新查询这些可变事实。
type DurableScanNode struct {
	Key              DurableScanKey
	Version          int64
	Status           string
	Title            string
	Summary          string
	Aliases          []string
	TopicIDs         []foundation.ID
	SourceVersionIDs []foundation.ID
}

// DurableScanPageRequest 使用公开 keyset 而不是进程私有 HMAC cursor 恢复扫描。
type DurableScanPageRequest struct {
	Binding         DurableScanBinding
	After           *DurableScanKey
	Limit           int
	PairTargetLimit int
}

// DurableScanPage 返回固定 binding 下的一页成员与可选的有界跨页 pair。
type DurableScanPage struct {
	Binding  DurableScanBinding
	Items    []CollectionItem
	Pairs    []DurableScanPair
	Nodes    []DurableScanNode
	Next     *DurableScanKey
	Complete bool
}

// ResultsQuery 是保存 Collection 结果的执行请求。
type ResultsQuery struct {
	WorkspaceID  foundation.ID
	CollectionID foundation.ID
	Limit        int
	Cursor       string
}

// PreviewQuery 对未保存的 canonical Query 执行同一 read model。
type PreviewQuery struct {
	WorkspaceID foundation.ID
	Query       domain.Query
	Limit       int
	Cursor      string
}

// QueryRepository 执行统一 read model；视图层不得自行实现过滤。
type QueryRepository interface {
	ExecuteQuery(context.Context, ResultsQuery) (ResultPage, error)
}

// PreviewRepository 执行 ad-hoc preview；实现必须与 saved results 共用 compiler/read model。
type PreviewRepository interface {
	ExecutePreview(context.Context, PreviewQuery) (ResultPage, error)
}

// DurableScanRepository 为 Workflow/Worker 提供跨进程稳定的 Collection keyset 扫描。
type DurableScanRepository interface {
	PlanDurableScan(context.Context, foundation.ID, foundation.ID) (DurableScanBinding, error)
	ReadDurableScanPage(context.Context, DurableScanPageRequest) (DurableScanPage, error)
}

// CommandResult 是命令返回的集合和稳定 receipt 绑定。
type CommandResult struct {
	Collection     Collection
	CommandVersion int64
	RequestHash    string
	CommandType    string
	Replayed       bool
}

// Repository 是 Collection application 使用的持久化端口。
type Repository interface {
	// CreateCollection 在一个事务中创建集合和幂等 receipt。
	CreateCollection(context.Context, CreateRecord) (CommandResult, error)
	// UpdateCollection 在一个事务中执行集合更新和 CAS。
	UpdateCollection(context.Context, UpdateRecord) (CommandResult, error)
	// ArchiveCollection 在一个事务中执行不可变归档和 CAS。
	ArchiveCollection(context.Context, ArchiveRecord) (CommandResult, error)
	// GetCollection 按 Workspace 隔离读取集合。
	GetCollection(context.Context, foundation.ID, foundation.ID) (Collection, error)
	// ListCollections 返回稳定排序的有界 keyset 页面。
	ListCollections(context.Context, ListQuery) (CollectionListPage, error)
}

// CreateRecord 是 Repository 的规范化创建输入。
type CreateRecord struct {
	Collection     Collection
	IdempotencyKey string
	RequestHash    string
}

// UpdateRecord 是 Repository 的规范化更新输入。
type UpdateRecord struct {
	WorkspaceID     foundation.ID
	CollectionID    foundation.ID
	ExpectedVersion int64
	Collection      Collection
	IdempotencyKey  string
	RequestHash     string
	At              time.Time
}

// ArchiveRecord 是 Repository 的规范化归档输入。
type ArchiveRecord struct {
	WorkspaceID     foundation.ID
	CollectionID    foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
	RequestHash     string
	At              time.Time
}
