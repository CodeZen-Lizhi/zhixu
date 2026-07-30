// Package application 编排异步导出任务，不暴露 PostgreSQL、River 或文件系统类型。
package application

import (
	"context"
	"encoding/json"
	"io"
	"time"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// Repository 是导出任务的持久化端口。
type Repository interface {
	Create(context.Context, domain.Job) (domain.Job, bool, error)
	Get(context.Context, foundation.ID, foundation.ID) (domain.Job, error)
	List(context.Context, ListQuery) (ListPage, error)
	Claim(context.Context, foundation.ID, foundation.ID, string, time.Duration) (domain.Job, bool, error)
	Prepare(context.Context, PrepareRequest) (domain.Job, error)
	Complete(context.Context, CompleteRequest) (domain.Job, error)
	Fail(context.Context, FailRequest) (domain.Job, error)
	Expire(context.Context, foundation.ID, foundation.ID) (domain.Job, error)
	RecordDownload(context.Context, DownloadRecord) (domain.Job, error)
	RecoveryCandidates(context.Context, int) ([]domain.Job, error)
	ExpireCandidates(context.Context, int) ([]domain.Job, error)
	CleanupCandidates(context.Context, int) ([]domain.Job, error)
	RecordCleanup(context.Context, CleanupRequest) (domain.Job, error)
	UnreferencedStaging(context.Context, foundation.ID, []string) ([]string, error)
	OrphanSweepWorkspaces(context.Context, foundation.ID, int) ([]foundation.ID, error)
}

// Dispatcher 是 River 投递与重启恢复端口。
type Dispatcher interface {
	Dispatch(context.Context, foundation.ID, foundation.ID) error
}

// WorkspaceReader 读取导出目标 Workspace 的安全根路径。
type WorkspaceReader interface {
	GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error)
}

// FileStore 负责受控导出文件的原子写入、校验读取和生命周期清理。
type FileStore interface {
	Stage(context.Context, foundation.ID, foundation.ID, string, []byte) (PreparedFile, error)
	Promote(context.Context, foundation.ID, PreparedFile) error
	Open(context.Context, foundation.ID, string, string, int64) (io.ReadCloser, error)
	DeletePrepared(context.Context, foundation.ID, string, string, int64) error
	DeleteOrphan(context.Context, foundation.ID, string) error
	ListStaging(context.Context, foundation.ID, time.Time, int) ([]StagingFile, error)
}

// AttachmentArchiver 从固定 Workspace 附件根生成受控、确定性的 staging ZIP。
type AttachmentArchiver interface {
	StageAttachments(context.Context, foundation.ID, foundation.ID) (AttachmentArchive, error)
}

// AttachmentArchive 是附件 manifest 与归档文件的不可变 prepared binding。
type AttachmentArchive struct {
	PreparedFile           PreparedFile
	ManifestHash           string
	EntryCount             int64
	TotalUncompressedBytes int64
}

// PreparedFile 是 create-only staging 写入后用于数据库 Prepare 的不可变文件绑定。
type PreparedFile struct {
	StagingPath string
	FinalPath   string
	FileHash    string
	FileSize    int64
}

// StagingFile 是受控 staging 命名空间中的有界清理候选。
type StagingFile struct {
	Path       string
	ModifiedAt time.Time
}

// Authorizer 只决定是否允许请求携带未脱敏字段；普通脱敏导出不依赖认证实现。
type Authorizer interface {
	AuthorizeExport(context.Context, AuthorizationRequest) error
}

// AuthorizationRequest 是导出权限判断所需的最小事实。
type AuthorizationRequest struct {
	WorkspaceID      foundation.ID
	Kind             domain.Kind
	PermissionScope  string
	IncludeSensitive bool
}

// SnapshotReader 读取冻结 Collection 结果，不把数据库模型泄漏到导出模块。
type SnapshotReader interface {
	ReadCollection(context.Context, foundation.ID, domain.Scope, int) (CollectionSnapshot, error)
}

// CollectionSnapshot 是一次导出使用的稳定结果快照。
type CollectionSnapshot struct {
	WorkspaceID       foundation.ID
	CollectionID      *foundation.ID
	CollectionVersion *int64
	QueryHash         string
	ReadModelRevision string
	ExactCount        int64
	Name              string
	Items             []Item
}

// Item 是 Collection read model 的导出投影；不包含正文全文。
type Item struct {
	ObjectType                 string
	ID                         foundation.ID
	TopicID                    *foundation.ID
	Title                      string
	Summary                    string
	Status                     string
	Confidence                 *float64
	Applicability              json.RawMessage
	ApplicabilitySchemaVersion string
	ApplicabilityHash          string
	Aliases                    []string
	Sources                    []Source
	Relations                  []string
	Health                     *Health
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

// Source 是导出中允许出现的来源摘要。
type Source struct {
	Type      string
	Path      string
	Support   string
	CreatedAt time.Time
}

// Health 是导出中允许出现的健康摘要。
type Health struct {
	Count       int
	MaxSeverity string
	IssueTypes  []string
	Summary     string
}

// ListQuery 是导出任务列表的 Workspace-scoped cursor 查询。
type ListQuery struct {
	WorkspaceID  foundation.ID
	ScopeKind    domain.ScopeKind
	CollectionID *foundation.ID
	Limit        int
	Cursor       string
}

// ListPage 是导出任务列表页面。
type ListPage struct {
	Items      []domain.Job
	NextCursor string
}

// PrepareRequest 是 worker 将冻结快照和 create-only staging 固定到 Job 的 CAS 请求。
type PrepareRequest struct {
	WorkspaceID            foundation.ID
	JobID                  foundation.ID
	LeaseOwner             string
	ExpectedVersion        int64
	ReadModelRevision      string
	ExactCount             int64
	ManifestHash           string
	EntryCount             int64
	TotalUncompressedBytes int64
	PreparedFile           PreparedFile
}

// CompleteRequest 是 worker 在已提升固定结果后成功归约的 CAS 请求。
type CompleteRequest struct {
	WorkspaceID     foundation.ID
	JobID           foundation.ID
	LeaseOwner      string
	ExpectedVersion int64
	PreparedFile    PreparedFile
}

// FailRequest 是 worker 失败归约的 CAS 请求。
type FailRequest struct {
	WorkspaceID     foundation.ID
	JobID           foundation.ID
	LeaseOwner      string
	ExpectedVersion int64
	ErrorCode       string
	ErrorMessage    string
	Retryable       bool
}

// DownloadActor 是从当前认证主体投影出的最小下载审计身份。
type DownloadActor struct {
	Type auditdomain.ActorType
	Ref  string
}

// DownloadRecord 是下载统计和 append-only Audit 同事务所需的完整绑定。
type DownloadRecord struct {
	WorkspaceID  foundation.ID
	JobID        foundation.ID
	AuditEventID foundation.ID
	Actor        DownloadActor
	ScopeKind    domain.ScopeKind
	EntryCount   *int64
	FileHash     string
	FileSize     int64
}

// CleanupRequest 是一次过期文件清理结果的 DB-time 持久化命令。
type CleanupRequest struct {
	WorkspaceID     foundation.ID
	JobID           foundation.ID
	ExpectedVersion int64
	Succeeded       bool
	ErrorCode       string
}
