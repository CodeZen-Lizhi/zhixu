package application

import (
	"context"
	"errors"
	"time"

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

// Validate accepts only the two frozen definition/policy pairs and canonical hashes.
func (contract WorkspaceAnalysisCapabilityContract) Validate() error {
	versioned := contract.DefinitionVersion == 1 && contract.PolicyVersion == 1 || contract.DefinitionVersion == 2 && contract.PolicyVersion == 2
	if contract.DefinitionKey != "workspace-analysis" || !versioned || !canonicalWorkspaceAnalysisSHA256(contract.DefinitionHash) ||
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

// WorkspaceAnalysisCapabilityLifecyclePort 是 Worker 自管广告事务所需的最小生命周期依赖。
type WorkspaceAnalysisCapabilityLifecyclePort interface {
	AdvertiseWorkspaceAnalysisWorker(context.Context, WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error)
	HeartbeatWorkspaceAnalysisWorker(context.Context, WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error)
	ReleaseWorkspaceAnalysisWorker(context.Context, WorkspaceAnalysisWorkerAdvertisement) (WorkspaceAnalysisWorkerCapability, error)
}

// WorkspaceAnalysisCapabilityService 是 Worker 生命周期调用的窄应用服务。
// Repository 自管三种生命周期写入的短事务，避免把 Worker 心跳塞进调用方事务。
type WorkspaceAnalysisCapabilityService struct {
	repository WorkspaceAnalysisCapabilityLifecyclePort
}

// NewWorkspaceAnalysisCapabilityService 创建默认关闭的 Worker 广告服务。
func NewWorkspaceAnalysisCapabilityService(repository WorkspaceAnalysisCapabilityLifecyclePort) (*WorkspaceAnalysisCapabilityService, error) {
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

func workspaceAnalysisCapabilityInvalid(cause error) error {
	return workspaceAnalysisCapabilityError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisCapabilityInvalid, false, cause)
}

func workspaceAnalysisCapabilityUnavailable(cause error) error {
	return workspaceAnalysisCapabilityError(foundation.ErrorDependencyUnavailable, ErrorCodeWorkspaceAnalysisCapabilityUnavailable, false, cause)
}

func workspaceAnalysisCapabilityError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return applicationError(kind, code, retryable, cause)
}
