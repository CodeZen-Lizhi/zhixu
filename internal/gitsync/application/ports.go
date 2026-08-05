package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

// URLPolicy 校验 DNS 与地址策略并返回规范 HTTPS 远端 URL。
type URLPolicy interface {
	NormalizeHTTPSRemote(context.Context, string) (string, error)
}

// CredentialSealer 使用绑定 Workspace 与配置的 AAD 保护 Token。
type CredentialSealer interface {
	Seal(domain.Token, domain.CredentialContext) (domain.EncryptedCredential, error)
	Open(domain.EncryptedCredential, domain.CredentialContext) (domain.Token, error)
}

// PersistConfigCommand 是规范化的配置与凭据原子写入命令。
type PersistConfigCommand struct {
	WorkspaceID      foundation.ID
	ExpectedRevision int64
	RemoteURL        string
	Branch           string
	AutoSync         bool
	SecretAction     domain.SecretAction
	IdempotencyKey   string
	RequestHash      string
	Actor            string
}

// DeleteConfigCommand 是同时清除凭据的幂等 CAS 删除命令。
type DeleteConfigCommand struct {
	WorkspaceID      foundation.ID
	ExpectedRevision int64
	IdempotencyKey   string
	RequestHash      string
	Actor            string
}

// ConfigReceipt 是配置命令不含密钥的精确响应。
type ConfigReceipt struct {
	Config   domain.RemoteConfig
	Replayed bool
}

// ReplayConfigCommand 描述配置命令的精确幂等重放查询。
type ReplayConfigCommand struct {
	WorkspaceID    foundation.ID
	IdempotencyKey string
	RequestHash    string
	CommandType    string
}

// ConfigStore 拥有配置 Revision、加密凭据与命令 Receipt。
type ConfigStore interface {
	GetConfig(context.Context, foundation.ID) (domain.RemoteConfig, error)
	ReplayConfig(context.Context, ReplayConfigCommand) (ConfigReceipt, bool, error)
	SaveConfig(context.Context, PersistConfigCommand) (ConfigReceipt, error)
	DeleteConfig(context.Context, DeleteConfigCommand) (ConfigReceipt, error)
	OpenCredential(context.Context, foundation.ID, int64) (domain.Token, error)
}

// GitAccess 是冻结且携带短期凭据的远端操作绑定。
type GitAccess struct {
	WorkspaceID    foundation.ID
	ConfigRevision int64
	RemoteURL      string
	Branch         string
	Token          domain.Token
}

// FastForwardCommand 将 fast-forward 绑定到精确比较过的引用。
type FastForwardCommand struct {
	Access            GitAccess
	ExpectedHeadOID   string
	ExpectedRemoteOID string
}

// PushCommand 将非强制 Push 绑定到精确比较过的引用。
type PushCommand struct {
	Access            GitAccess
	ExpectedHeadOID   string
	ExpectedRemoteOID string
}

// VerifyCommand 要求适配器在不改写历史的前提下证明变更后引用。
type VerifyCommand struct {
	Access            GitAccess
	ExpectedHeadOID   string
	ExpectedRemoteOID string
}

// RemoteRepository 仅暴露白名单内的 Git 远端操作。
type RemoteRepository interface {
	TestConnection(context.Context, GitAccess) error
	Fetch(context.Context, GitAccess) error
	Compare(context.Context, GitAccess) (domain.Comparison, error)
	FastForward(context.Context, FastForwardCommand) (domain.MutationResult, error)
	Push(context.Context, PushCommand) (domain.MutationResult, error)
	Verify(context.Context, VerifyCommand) (domain.Verification, error)
}

// CreateRunRecord 是单次运行的完整不可变绑定。
type CreateRunRecord struct {
	Run domain.SyncRun
}

// ReplayRunCommand 描述运行创建命令的精确幂等重放查询。
type ReplayRunCommand struct {
	WorkspaceID    foundation.ID
	IdempotencyKey string
	RequestHash    string
	Trigger        domain.Trigger
	RetryOfRunID   foundation.ID
}

// BeginAttemptCommand 原子领取待运行任务并栅栏其配置 Revision。
type BeginAttemptCommand struct {
	WorkspaceID   foundation.ID
	RunID         foundation.ID
	AttemptID     foundation.ID
	Owner         string
	LeaseDuration time.Duration
}

// BeginAttemptResult 报告终态重放或新领取的 Attempt。
type BeginAttemptResult struct {
	Run     domain.SyncRun
	Attempt domain.SyncAttempt
	Started bool
}

// TransitionRunCommand 推进租约 Attempt 并冻结新 checkpoint。
type TransitionRunCommand struct {
	WorkspaceID            foundation.ID
	RunID                  foundation.ID
	AttemptID              foundation.ID
	Owner                  string
	ExpectedRunVersion     int64
	ExpectedAttemptVersion int64
	ExpectedStatus         domain.RunStatus
	NextStatus             domain.RunStatus
	Direction              domain.Direction
	ExpectedHeadOID        string
	ExpectedRemoteOID      string
	ChangedFiles           []domain.FileChange
	ResultKnown            *bool
	LeaseDuration          time.Duration
}

// CompleteRunCommand 关闭 Git 与 Attempt 状态，并按需发出索引 follow-up。
type CompleteRunCommand struct {
	WorkspaceID            foundation.ID
	RunID                  foundation.ID
	AttemptID              foundation.ID
	Owner                  string
	ExpectedRunVersion     int64
	ExpectedAttemptVersion int64
	ExpectedStatus         domain.RunStatus
	Status                 domain.RunStatus
	Direction              domain.Direction
	ExpectedHeadOID        string
	ExpectedRemoteOID      string
	ChangedFiles           []domain.FileChange
	FailureClass           domain.FailureClass
	ErrorCode              string
	Retryable              bool
	VerifiedHeadOID        string
	VerifiedRemoteOID      string
	IndexStatus            domain.IndexStatus
	ResultKnown            *bool
}

// ListRunsQuery 是有界 keyset 查询。
type ListRunsQuery struct {
	WorkspaceID foundation.ID
	BeforeTime  time.Time
	BeforeID    foundation.ID
	Limit       int
}

// RunPage 是稳定运行分页及其下一页 keyset 边界。
type RunPage struct {
	Items    []domain.SyncRun
	NextTime time.Time
	NextID   foundation.ID
}

// RetryIndexCommand 仅重新入队失败的索引 follow-up。
type RetryIndexCommand struct {
	WorkspaceID     foundation.ID
	RunID           foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
	RequestHash     string
}

// RunStore 拥有运行与 Attempt 转换、活动唯一性和 follow-up Outbox 写入。
type RunStore interface {
	ReplayRun(context.Context, ReplayRunCommand) (domain.SyncRun, bool, error)
	ReplayIndex(context.Context, RetryIndexCommand) (domain.SyncRun, bool, error)
	CreateRun(context.Context, CreateRunRecord) (domain.SyncRun, bool, error)
	GetRun(context.Context, foundation.ID, foundation.ID) (domain.SyncRun, error)
	GetCurrentRun(context.Context, foundation.ID) (domain.SyncRun, bool, error)
	ListRuns(context.Context, ListRunsQuery) (RunPage, error)
	BeginAttempt(context.Context, BeginAttemptCommand) (BeginAttemptResult, error)
	TransitionRun(context.Context, TransitionRunCommand) (domain.SyncRun, domain.SyncAttempt, error)
	CompleteRun(context.Context, CompleteRunCommand) (domain.SyncRun, error)
	RetryIndex(context.Context, RetryIndexCommand) (domain.SyncRun, bool, error)
}

// AutoSyncCandidate 是一个已完成且尚未创建自动同步 Run 的批准写回。
type AutoSyncCandidate struct {
	WorkspaceID      foundation.ID
	ProposalCommitID foundation.ID
	ConfigRevision   int64
}

// AutoSyncCandidateSource 枚举在完成时和当前都允许自动同步的写回事实。
type AutoSyncCandidateSource interface {
	ListAutoSyncCandidates(context.Context, int) ([]AutoSyncCandidate, error)
}

// AutomaticRunCreator 复用标准 Run/Outbox 创建边界。
type AutomaticRunCreator interface {
	CreateRun(context.Context, CreateRunCommand) (RunReceipt, error)
}

// ExternalChangeCommand 将一个 fast-forward 范围绑定到幂等批量刷新。
type ExternalChangeCommand struct {
	WorkspaceID    foundation.ID
	RunID          foundation.ID
	BeforeCommit   string
	AfterCommit    string
	IdempotencyKey string
}

// ExternalChangeResult 是可安全持久化的批量捕获与索引 Receipt。
type ExternalChangeResult struct {
	IndexVersionID  foundation.ID
	NoIndexRequired bool
}

// ExternalChangeCapture 将全部变更和删除的已跟踪 Source 作为一个批次刷新。
type ExternalChangeCapture interface {
	CaptureGitFastForward(context.Context, ExternalChangeCommand) (ExternalChangeResult, error)
}

// FollowupStore 拥有独立索引状态转换。
type FollowupStore interface {
	BeginIndexFollowup(context.Context, domain.OutboxLease, int64) (domain.SyncRun, bool, error)
	CompleteIndexFollowup(context.Context, domain.OutboxLease, int64, foundation.ID, bool) (domain.SyncRun, error)
	FailIndexFollowup(context.Context, domain.OutboxLease, int64, string, bool) (domain.SyncRun, error)
}

// OutboxStore 拥有基于数据库时间的运行与索引投递租约。
type OutboxStore interface {
	ClaimNext(context.Context, string, time.Duration) (domain.OutboxLease, bool, error)
	MarkPublished(context.Context, domain.OutboxLease) error
	Reschedule(context.Context, domain.OutboxLease, string, time.Duration) error
	Poison(context.Context, domain.OutboxLease, string) error
}
