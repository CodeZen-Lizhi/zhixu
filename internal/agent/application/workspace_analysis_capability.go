package application

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeWorkspaceAnalysisCapabilityInvalid 表示 Worker 能力合同或调用参数不合法。
	ErrorCodeWorkspaceAnalysisCapabilityInvalid = "AGENT_WORKSPACE_ANALYSIS_CAPABILITY_INVALID"
	// ErrorCodeWorkspaceAnalysisCapabilityUnavailable 表示当前事务没有同版且新鲜的 Worker 能力广告。
	ErrorCodeWorkspaceAnalysisCapabilityUnavailable = "AGENT_WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE"
	// ErrorCodeWorkspaceAnalysisCapabilityConflict 表示同一 Worker 实例尝试改变不可变能力合同或操作已退役广告。
	ErrorCodeWorkspaceAnalysisCapabilityConflict = "AGENT_WORKSPACE_ANALYSIS_CAPABILITY_CONFLICT"

	// DefaultWorkspaceAnalysisWorkerCapabilityLease 是 Worker 成功装配后默认的数据库租约。
	DefaultWorkspaceAnalysisWorkerCapabilityLease = 30 * time.Second
	minWorkspaceAnalysisWorkerCapabilityLease     = 10 * time.Second
	maxWorkspaceAnalysisWorkerCapabilityLease     = 60 * time.Second
)

// WorkspaceAnalysisCapabilityContract 是 API 和 Worker 必须精确一致的只读合同。
// 它不含实例身份，使任意一个同版新鲜 Worker 都可以解除 API 的默认关闭门。
type WorkspaceAnalysisCapabilityContract struct {
	DefinitionKey     string
	DefinitionVersion int64
	DefinitionHash    string
	ToolCatalogHash   string
	PolicyVersion     int
	ConfigRevision    int64
}

// Validate 拒绝非 workspace-analysis@1 的能力合同和非 canonical 哈希。
func (contract WorkspaceAnalysisCapabilityContract) Validate() error {
	if contract.DefinitionKey != "workspace-analysis" || contract.DefinitionVersion != 1 ||
		contract.PolicyVersion != 1 || !canonicalWorkspaceAnalysisSHA256(contract.DefinitionHash) ||
		!canonicalWorkspaceAnalysisSHA256(contract.ToolCatalogHash) || contract.ConfigRevision < 0 {
		return workspaceAnalysisCapabilityError(
			foundation.ErrorInvalidInput,
			ErrorCodeWorkspaceAnalysisCapabilityInvalid,
			false,
			errors.New("workspace analysis capability contract is invalid"),
		)
	}
	return nil
}

// WorkspaceAnalysisWorkerAdvertisement 是一个 Worker 实例对同版能力的可续租声明。
type WorkspaceAnalysisWorkerAdvertisement struct {
	WorkerInstanceID foundation.ID
	Contract         WorkspaceAnalysisCapabilityContract
	LeaseDuration    time.Duration
}

// Validate 拒绝非 canonical Worker UUID 与超出固定滚动发布窗口的租约。
func (advertisement WorkspaceAnalysisWorkerAdvertisement) Validate() error {
	if !canonicalApplicationID(advertisement.WorkerInstanceID) || advertisement.LeaseDuration < minWorkspaceAnalysisWorkerCapabilityLease ||
		advertisement.LeaseDuration > maxWorkspaceAnalysisWorkerCapabilityLease ||
		advertisement.LeaseDuration%time.Microsecond != 0 {
		return workspaceAnalysisCapabilityError(
			foundation.ErrorInvalidInput,
			ErrorCodeWorkspaceAnalysisCapabilityInvalid,
			false,
			errors.New("workspace analysis worker advertisement is invalid"),
		)
	}
	return advertisement.Contract.Validate()
}

// WorkspaceAnalysisWorkerCapability 是数据库读回的广告事实；时间由 PostgreSQL 写入。
type WorkspaceAnalysisWorkerCapability struct {
	WorkspaceAnalysisWorkerAdvertisement
	HeartbeatAt time.Time
	LeaseUntil  time.Time
	ReleasedAt  *time.Time
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// WorkspaceAnalysisCapabilityRepository 区分 Worker 自管广告事务与 API 调用方事务中的 ready 检查。
type WorkspaceAnalysisCapabilityRepository interface {
	AdvertiseWorkspaceAnalysisWorker(context.Context, WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error)
	HeartbeatWorkspaceAnalysisWorker(context.Context, WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error)
	ReleaseWorkspaceAnalysisWorker(context.Context, WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error)
	RequireWorkspaceAnalysisWorkerReadyTx(context.Context, any, WorkspaceAnalysisCapabilityContract) error
}

// WorkspaceAnalysisCapabilityService 是 Worker 生命周期调用的窄应用服务。
// Repository 自管三种生命周期写入的短事务，避免把 Worker 心跳塞进调用方事务。
type WorkspaceAnalysisCapabilityService struct {
	repository WorkspaceAnalysisCapabilityRepository
}

// NewWorkspaceAnalysisCapabilityService 创建默认关闭的 Worker 广告服务。
func NewWorkspaceAnalysisCapabilityService(repository WorkspaceAnalysisCapabilityRepository) (*WorkspaceAnalysisCapabilityService, error) {
	if isNilPort(repository) {
		return nil, workspaceAnalysisCapabilityError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisCapabilityUnavailable,
			true,
			errors.New("workspace analysis capability repository is unavailable"),
		)
	}
	return &WorkspaceAnalysisCapabilityService{repository: repository}, nil
}

// Advertise 以数据库时间创建或恢复相同实例的同一不可变合同。
func (service *WorkspaceAnalysisCapabilityService) Advertise(ctx context.Context, advertisement WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error) {
	if service == nil || isNilPort(service.repository) {
		return WorkspaceAnalysisWorkerCapability{}, workspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis capability service is unavailable"))
	}
	if ctx == nil {
		return WorkspaceAnalysisWorkerCapability{}, workspaceAnalysisCapabilityInvalid(errors.New("workspace analysis capability context is nil"))
	}
	if err := advertisement.Validate(); err != nil {
		return WorkspaceAnalysisWorkerCapability{}, err
	}
	return service.repository.AdvertiseWorkspaceAnalysisWorker(ctx, advertisement)
}

// Heartbeat 只允许为仍处于租约内、合同完全一致的活跃广告续租。
func (service *WorkspaceAnalysisCapabilityService) Heartbeat(ctx context.Context, advertisement WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error) {
	if service == nil || isNilPort(service.repository) {
		return WorkspaceAnalysisWorkerCapability{}, workspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis capability service is unavailable"))
	}
	if ctx == nil {
		return WorkspaceAnalysisWorkerCapability{}, workspaceAnalysisCapabilityInvalid(errors.New("workspace analysis capability context is nil"))
	}
	if err := advertisement.Validate(); err != nil {
		return WorkspaceAnalysisWorkerCapability{}, err
	}
	return service.repository.HeartbeatWorkspaceAnalysisWorker(ctx, advertisement)
}

// Release 将广告标为退役，保留历史供滚动发布和审计排障使用。
func (service *WorkspaceAnalysisCapabilityService) Release(ctx context.Context, advertisement WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error) {
	if service == nil || isNilPort(service.repository) {
		return WorkspaceAnalysisWorkerCapability{}, workspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis capability service is unavailable"))
	}
	if ctx == nil {
		return WorkspaceAnalysisWorkerCapability{}, workspaceAnalysisCapabilityInvalid(errors.New("workspace analysis capability context is nil"))
	}
	if err := advertisement.Validate(); err != nil {
		return WorkspaceAnalysisWorkerCapability{}, err
	}
	return service.repository.ReleaseWorkspaceAnalysisWorker(ctx, advertisement)
}

// WorkspaceAnalysisCapabilityCheckedRunStarter 在 Question 派发持有的同一事务中检查 Worker 合同。
// 它只保护新创建；重放仍须通过同一检查，避免旧 API 在新 Worker 下制造新的 Run 事实。
type WorkspaceAnalysisCapabilityCheckedRunStarter struct {
	readiness WorkspaceAnalysisCapabilityRepository
	delegate  WorkspaceAnalysisRunStarter
	contract  WorkspaceAnalysisCapabilityContract
}

// NewWorkspaceAnalysisCapabilityCheckedRunStarter 将 ready 检查包裹在既有 Analysis Run 创建端口之外。
func NewWorkspaceAnalysisCapabilityCheckedRunStarter(
	readiness WorkspaceAnalysisCapabilityRepository,
	delegate WorkspaceAnalysisRunStarter,
	contract WorkspaceAnalysisCapabilityContract,
) (*WorkspaceAnalysisCapabilityCheckedRunStarter, error) {
	if isNilPort(readiness) || isNilPort(delegate) {
		return nil, workspaceAnalysisCapabilityError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisCapabilityUnavailable,
			true,
			errors.New("workspace analysis readiness or run starter is unavailable"),
		)
	}
	if err := contract.Validate(); err != nil {
		return nil, err
	}
	return &WorkspaceAnalysisCapabilityCheckedRunStarter{readiness: readiness, delegate: delegate, contract: contract}, nil
}

// StartWorkspaceAnalysisRunTx 先在 caller-owned transaction 内确认至少一个精确 Worker 广告新鲜，再创建 Run。
func (starter *WorkspaceAnalysisCapabilityCheckedRunStarter) StartWorkspaceAnalysisRunTx(
	ctx context.Context,
	transaction any,
	command WorkspaceAnalysisRunStartCommand,
) (domain.WorkspaceAnalysisRun, error) {
	if starter == nil || isNilPort(starter.readiness) || isNilPort(starter.delegate) || isNilOpaqueTransaction(transaction) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis ready run starter is unavailable"))
	}
	if ctx == nil {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisCapabilityInvalid(errors.New("workspace analysis capability context is nil"))
	}
	if err := starter.readiness.RequireWorkspaceAnalysisWorkerReadyTx(ctx, transaction, starter.contract); err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	return starter.delegate.StartWorkspaceAnalysisRunTx(ctx, transaction, command)
}

func workspaceAnalysisCapabilityInvalid(cause error) error {
	return workspaceAnalysisCapabilityError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisCapabilityInvalid, false, cause)
}

func workspaceAnalysisCapabilityUnavailable(cause error) error {
	return workspaceAnalysisCapabilityError(foundation.ErrorDependencyUnavailable, ErrorCodeWorkspaceAnalysisCapabilityUnavailable, false, cause)
}

func workspaceAnalysisCapabilityError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return applicationError(kind, code, retryable, cause)
}

var _ WorkspaceAnalysisRunStarter = (*WorkspaceAnalysisCapabilityCheckedRunStarter)(nil)
