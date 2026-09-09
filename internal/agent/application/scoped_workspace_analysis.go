package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ScopedWorkspaceAnalysisRunPersistence 在调用方持有的强类型事务 scope 内读写 Analysis Run。
type ScopedWorkspaceAnalysisRunPersistence interface {
	InsertWorkspaceAnalysisRunScoped(context.Context, foundation.TransactionScope, domain.WorkspaceAnalysisRun) (domain.WorkspaceAnalysisRun, error)
	FindWorkspaceAnalysisRunScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID) (domain.WorkspaceAnalysisRun, bool, error)
}

// ScopedWorkspaceAnalysisRunStarter 是 Conversation owner 在单一强类型事务内创建或验证 Analysis Run 的端口。
type ScopedWorkspaceAnalysisRunStarter interface {
	StartWorkspaceAnalysisRunScoped(context.Context, foundation.TransactionScope, WorkspaceAnalysisRunStartCommand) (domain.WorkspaceAnalysisRun, error)
}

// ScopedWorkspaceAnalysisReadiness 在调用方强类型事务 scope 内确认精确 Worker 能力合同。
type ScopedWorkspaceAnalysisReadiness interface {
	RequireWorkspaceAnalysisWorkerReadyScoped(context.Context, foundation.TransactionScope, WorkspaceAnalysisCapabilityContract) error
}

// ScopedWorkspaceAnalysisRunService 复用冻结配置和重放校验，在调用方事务 scope 内创建或读取 Run。
type ScopedWorkspaceAnalysisRunService struct {
	repository ScopedWorkspaceAnalysisRunPersistence
	ids        foundation.IDGenerator
	config     WorkspaceAnalysisRunStartConfig
	budget     WorkspaceAnalysisBudgetPolicy
	deadlines  WorkspaceAnalysisV1Deadlines
}

// NewScopedWorkspaceAnalysisRunService 创建仅供 caller-owned 强类型事务使用的 Run 服务。
func NewScopedWorkspaceAnalysisRunService(
	repository ScopedWorkspaceAnalysisRunPersistence,
	ids foundation.IDGenerator,
	config WorkspaceAnalysisRunStartConfig,
) (*ScopedWorkspaceAnalysisRunService, error) {
	if isNilPort(repository) {
		return nil, workspaceAnalysisRunStartError(
			foundation.ErrorInvalidInput,
			ErrorCodeWorkspaceAnalysisRunStartInvalid,
			false,
			errors.New("workspace analysis scoped run persistence is unavailable"),
		)
	}
	budget, deadlines, err := newWorkspaceAnalysisRunStartDependencies(ids, config)
	if err != nil {
		return nil, err
	}
	return &ScopedWorkspaceAnalysisRunService{
		repository: repository,
		ids:        ids,
		config:     config,
		budget:     budget,
		deadlines:  deadlines,
	}, nil
}

// StartWorkspaceAnalysisRunScoped 创建全空预算 queued Run；重放只接受既有精确不可变绑定。
func (service *ScopedWorkspaceAnalysisRunService) StartWorkspaceAnalysisRunScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command WorkspaceAnalysisRunStartCommand,
) (domain.WorkspaceAnalysisRun, error) {
	if service == nil || isNilPort(service.repository) || isNilPort(service.ids) || isNilPort(scope) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisRunStartUnavailable,
			true,
			errors.New("workspace analysis scoped run service or transaction scope is unavailable"),
		)
	}
	if ctx == nil || !validWorkspaceAnalysisRunStartCommand(command) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(
			foundation.ErrorInvalidInput,
			ErrorCodeWorkspaceAnalysisRunStartInvalid,
			false,
			errors.New("workspace analysis run start command is invalid"),
		)
	}

	if command.Replayed {
		existing, found, err := service.repository.FindWorkspaceAnalysisRunScoped(ctx, scope, command.WorkspaceID, command.QuestionID)
		if err != nil {
			return domain.WorkspaceAnalysisRun{}, err
		}
		if !found || domain.ValidateWorkspaceAnalysisRun(existing) != nil ||
			!sameWorkspaceAnalysisRunDispatchBinding(existing, command) {
			return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(
				foundation.ErrorConsistencyViolation,
				ErrorCodeWorkspaceAnalysisRunStartConflict,
				false,
				errors.New("replayed workspace analysis run is missing or drifted"),
			)
		}
		return existing, nil
	}

	runID, err := service.ids.New()
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisRunStartUnavailable,
			true,
			err,
		)
	}
	run := newWorkspaceAnalysisQueuedRun(runID, command, service.config, service.budget, service.deadlines)
	if err := domain.ValidateWorkspaceAnalysisRun(run); err != nil {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(
			foundation.ErrorInvalidInput,
			ErrorCodeWorkspaceAnalysisRunStartInvalid,
			false,
			err,
		)
	}
	persisted, err := service.repository.InsertWorkspaceAnalysisRunScoped(ctx, scope, run)
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	if domain.ValidateWorkspaceAnalysisRun(persisted) != nil ||
		!sameWorkspaceAnalysisQueuedRun(persisted, run) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(
			foundation.ErrorConsistencyViolation,
			ErrorCodeWorkspaceAnalysisRunStartConflict,
			false,
			errors.New("persisted workspace analysis run differs from the queued candidate"),
		)
	}
	return persisted, nil
}

// ScopedWorkspaceAnalysisCapabilityCheckedRunStarter 在同一 scope 内校验新请求 readiness，重放沿用历史绑定。
type ScopedWorkspaceAnalysisCapabilityCheckedRunStarter struct {
	readiness ScopedWorkspaceAnalysisReadiness
	delegate  ScopedWorkspaceAnalysisRunStarter
	contract  WorkspaceAnalysisCapabilityContract
}

// NewScopedWorkspaceAnalysisCapabilityCheckedRunStarter 创建不可拆分的 scoped readiness 与 Run 启动组合器。
func NewScopedWorkspaceAnalysisCapabilityCheckedRunStarter(
	readiness ScopedWorkspaceAnalysisReadiness,
	delegate ScopedWorkspaceAnalysisRunStarter,
	contract WorkspaceAnalysisCapabilityContract,
) (*ScopedWorkspaceAnalysisCapabilityCheckedRunStarter, error) {
	if isNilPort(readiness) || isNilPort(delegate) {
		return nil, workspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis scoped readiness or run starter is unavailable"))
	}
	if err := contract.Validate(); err != nil {
		return nil, err
	}
	return &ScopedWorkspaceAnalysisCapabilityCheckedRunStarter{
		readiness: readiness,
		delegate:  delegate,
		contract:  contract,
	}, nil
}

// StartWorkspaceAnalysisRunScoped 对新请求先确认 Worker readiness 再创建 Run，对重放读取已持久绑定。
func (starter *ScopedWorkspaceAnalysisCapabilityCheckedRunStarter) StartWorkspaceAnalysisRunScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command WorkspaceAnalysisRunStartCommand,
) (domain.WorkspaceAnalysisRun, error) {
	if starter == nil || isNilPort(starter.readiness) || isNilPort(starter.delegate) || isNilPort(scope) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis scoped ready run starter is unavailable"))
	}
	if ctx == nil {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisCapabilityInvalid(errors.New("workspace analysis capability context is nil"))
	}
	if command.Replayed {
		// The durable run owns the version on replay. A newer deployment's
		// capability advertisement must not rewrite or gate historical requests.
		return starter.delegate.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
	}
	if err := starter.readiness.RequireWorkspaceAnalysisWorkerReadyScoped(ctx, scope, starter.contract); err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	return starter.delegate.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
}

var _ ScopedWorkspaceAnalysisRunStarter = (*ScopedWorkspaceAnalysisRunService)(nil)
var _ ScopedWorkspaceAnalysisRunStarter = (*ScopedWorkspaceAnalysisCapabilityCheckedRunStarter)(nil)
