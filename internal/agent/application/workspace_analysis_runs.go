package application

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeWorkspaceAnalysisRunStartInvalid 表示派发绑定或冻结配置不符合 v1 合同。
	ErrorCodeWorkspaceAnalysisRunStartInvalid = "AGENT_WORKSPACE_ANALYSIS_RUN_START_INVALID"
	// ErrorCodeWorkspaceAnalysisRunStartUnavailable 表示 Run 创建事务依赖不可用。
	ErrorCodeWorkspaceAnalysisRunStartUnavailable = "AGENT_WORKSPACE_ANALYSIS_RUN_START_UNAVAILABLE"
	// ErrorCodeWorkspaceAnalysisRunStartConflict 表示重放事实缺失或不可变绑定漂移。
	ErrorCodeWorkspaceAnalysisRunStartConflict = "AGENT_WORKSPACE_ANALYSIS_RUN_START_CONFLICT"
	// ErrorCodeWorkspaceAnalysisRunExecutionInvalid 表示执行端 Run 查询身份不完整。
	ErrorCodeWorkspaceAnalysisRunExecutionInvalid = "AGENT_WORKSPACE_ANALYSIS_RUN_EXECUTION_INVALID"
	// ErrorCodeWorkspaceAnalysisRunExecutionUnavailable 表示执行端无法读取权威 Analysis Run。
	ErrorCodeWorkspaceAnalysisRunExecutionUnavailable = "AGENT_WORKSPACE_ANALYSIS_RUN_EXECUTION_UNAVAILABLE"
	// ErrorCodeWorkspaceAnalysisRunExecutionConflict 表示执行端读取的 Run 与 Workflow 输入绑定漂移。
	ErrorCodeWorkspaceAnalysisRunExecutionConflict = "AGENT_WORKSPACE_ANALYSIS_RUN_EXECUTION_CONFLICT"
)

// WorkspaceAnalysisRunStartConfig 是启用 workspace-analysis@1 前冻结的服务端配置快照。
type WorkspaceAnalysisRunStartConfig struct {
	DefinitionHash                  string
	ToolCatalogHash                 string
	ConfigRevision                  int64
	Timeouts                        WorkspaceAnalysisV1Timeouts
	SynthesisProfileMaxOutputTokens int
	RuntimeLimits                   WorkspaceAnalysisRuntimeLimits
}

// WorkspaceAnalysisRunStartCommand 绑定一次已持久 Question、Answer 与 Workflow Run。
type WorkspaceAnalysisRunStartCommand struct {
	WorkspaceID    foundation.ID
	ConversationID foundation.ID
	QuestionID     foundation.ID
	AnswerID       foundation.ID
	WorkflowRunID  foundation.ID
	CreatedAt      time.Time
	Replayed       bool
}

// WorkspaceAnalysisRunPersistence 隐藏 caller-owned 事务中的 Analysis Run 插入与读取。
type WorkspaceAnalysisRunPersistence interface {
	InsertWorkspaceAnalysisRunTx(context.Context, any, domain.WorkspaceAnalysisRun) (domain.WorkspaceAnalysisRun, error)
	FindWorkspaceAnalysisRunTx(context.Context, any, foundation.ID, foundation.ID) (domain.WorkspaceAnalysisRun, bool, error)
}

// WorkspaceAnalysisRunExecutionQuery 绑定 Workflow 节点执行时必须重新读取的不可变 Run 身份。
type WorkspaceAnalysisRunExecutionQuery struct {
	WorkspaceID    foundation.ID
	WorkflowRunID  foundation.ID
	ConversationID foundation.ID
	QuestionID     foundation.ID
	AnswerID       foundation.ID
}

// Validate 拒绝缺失、格式错误或复用的持久身份。
func (query WorkspaceAnalysisRunExecutionQuery) Validate() error {
	ids := []foundation.ID{
		query.WorkspaceID, query.WorkflowRunID, query.ConversationID, query.QuestionID, query.AnswerID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return workspaceAnalysisRunStartError(
				foundation.ErrorInvalidInput,
				ErrorCodeWorkspaceAnalysisRunExecutionInvalid,
				false,
				errors.New("workspace analysis execution query identity is invalid"),
			)
		}
		if _, duplicate := seen[id]; duplicate {
			return workspaceAnalysisRunStartError(
				foundation.ErrorInvalidInput,
				ErrorCodeWorkspaceAnalysisRunExecutionInvalid,
				false,
				errors.New("workspace analysis execution query identity is reused"),
			)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// WorkspaceAnalysisRunLoader 是 Workflow 节点恢复 Analysis Run 权威身份的窄只读端口。
type WorkspaceAnalysisRunLoader interface {
	LoadWorkspaceAnalysisRunForExecution(context.Context, WorkspaceAnalysisRunExecutionQuery) (domain.WorkspaceAnalysisRun, error)
}

// WorkspaceAnalysisRunStarter 是 Conversation Dispatcher 使用的窄原子创建端口。
type WorkspaceAnalysisRunStarter interface {
	StartWorkspaceAnalysisRunTx(context.Context, any, WorkspaceAnalysisRunStartCommand) (domain.WorkspaceAnalysisRun, error)
}

// WorkspaceAnalysisRunService 构造并验证冻结预算后，在调用方事务中创建或校验 Run。
type WorkspaceAnalysisRunService struct {
	repository WorkspaceAnalysisRunPersistence
	ids        foundation.IDGenerator
	config     WorkspaceAnalysisRunStartConfig
	budget     WorkspaceAnalysisBudgetPolicy
	deadlines  WorkspaceAnalysisV1Deadlines
}

// NewWorkspaceAnalysisRunService 创建默认关闭能力被显式启用时使用的 Run 服务。
func NewWorkspaceAnalysisRunService(
	repository WorkspaceAnalysisRunPersistence,
	ids foundation.IDGenerator,
	config WorkspaceAnalysisRunStartConfig,
) (*WorkspaceAnalysisRunService, error) {
	if isNilPort(repository) {
		return nil, workspaceAnalysisRunStartError(
			foundation.ErrorInvalidInput,
			ErrorCodeWorkspaceAnalysisRunStartInvalid,
			false,
			errors.New("workspace analysis run persistence is unavailable"),
		)
	}
	budget, deadlines, err := newWorkspaceAnalysisRunStartDependencies(ids, config)
	if err != nil {
		return nil, err
	}
	return &WorkspaceAnalysisRunService{
		repository: repository,
		ids:        ids,
		config:     config,
		budget:     budget,
		deadlines:  deadlines,
	}, nil
}

// StartWorkspaceAnalysisRunTx 创建全空预算 queued Run；重放只接受已存在的精确不可变绑定。
func (service *WorkspaceAnalysisRunService) StartWorkspaceAnalysisRunTx(
	ctx context.Context,
	transaction any,
	command WorkspaceAnalysisRunStartCommand,
) (domain.WorkspaceAnalysisRun, error) {
	if service == nil || isNilPort(service.repository) || isNilPort(service.ids) || isNilOpaqueTransaction(transaction) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisRunStartUnavailable,
			true,
			errors.New("workspace analysis run service or transaction is unavailable"),
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
		existing, found, err := service.repository.FindWorkspaceAnalysisRunTx(ctx, transaction, command.WorkspaceID, command.QuestionID)
		if err != nil {
			return domain.WorkspaceAnalysisRun{}, err
		}
		if !found || domain.ValidateWorkspaceAnalysisRun(existing) != nil || !sameWorkspaceAnalysisRunDispatchBinding(existing, command) {
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
	run := service.newQueuedRun(runID, command)
	if err := domain.ValidateWorkspaceAnalysisRun(run); err != nil {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(
			foundation.ErrorInvalidInput,
			ErrorCodeWorkspaceAnalysisRunStartInvalid,
			false,
			err,
		)
	}
	persisted, err := service.repository.InsertWorkspaceAnalysisRunTx(ctx, transaction, run)
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	if domain.ValidateWorkspaceAnalysisRun(persisted) != nil || !service.sameQueuedRun(persisted, run) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(
			foundation.ErrorConsistencyViolation,
			ErrorCodeWorkspaceAnalysisRunStartConflict,
			false,
			errors.New("persisted workspace analysis run differs from the queued candidate"),
		)
	}
	return persisted, nil
}

func (service *WorkspaceAnalysisRunService) sameQueuedRun(persisted, expected domain.WorkspaceAnalysisRun) bool {
	return sameWorkspaceAnalysisQueuedRun(persisted, expected, service.config, service.budget, service.deadlines)
}

func (service *WorkspaceAnalysisRunService) newQueuedRun(id foundation.ID, command WorkspaceAnalysisRunStartCommand) domain.WorkspaceAnalysisRun {
	return newWorkspaceAnalysisQueuedRun(id, command, service.config, service.budget, service.deadlines)
}

func newWorkspaceAnalysisQueuedRun(
	id foundation.ID,
	command WorkspaceAnalysisRunStartCommand,
	config WorkspaceAnalysisRunStartConfig,
	budget WorkspaceAnalysisBudgetPolicy,
	deadlines WorkspaceAnalysisV1Deadlines,
) domain.WorkspaceAnalysisRun {
	return domain.WorkspaceAnalysisRun{
		ID:                id,
		WorkspaceID:       command.WorkspaceID,
		ConversationID:    command.ConversationID,
		QuestionID:        command.QuestionID,
		AnswerID:          command.AnswerID,
		WorkflowRunID:     command.WorkflowRunID,
		DefinitionKey:     "workspace-analysis",
		DefinitionVersion: 1,
		DefinitionHash:    config.DefinitionHash,
		ToolCatalogHash:   config.ToolCatalogHash,
		PolicyVersion:     budget.PolicyVersion,
		ConfigRevision:    config.ConfigRevision,
		DeadlineAt:        command.CreatedAt.Add(deadlines.RunDeadline()),
		Timeouts:          config.Timeouts,
		Limits: domain.WorkspaceAnalysisBudgetLimits{
			Nodes:           budget.MaxNodes,
			ToolConcurrency: budget.MaxToolConcurrency,
			Amount: domain.WorkspaceAnalysisBudgetAmount{
				ModelCalls:   budget.MaxModelCalls,
				ToolCalls:    budget.MaxToolCalls,
				SourceReads:  budget.MaxSourceReads,
				InputTokens:  budget.MaxRunInputTokens,
				OutputTokens: budget.MaxRunOutputTokens,
			},
		},
		Status:    domain.WorkspaceAnalysisRunQueued,
		Version:   1,
		CreatedAt: command.CreatedAt.UTC(),
		UpdatedAt: command.CreatedAt.UTC(),
	}
}

func (service *WorkspaceAnalysisRunService) sameFrozenStartBinding(run domain.WorkspaceAnalysisRun, command WorkspaceAnalysisRunStartCommand) bool {
	return sameWorkspaceAnalysisFrozenStartBinding(run, command, service.config, service.budget, service.deadlines)
}

func sameWorkspaceAnalysisQueuedRun(
	persisted domain.WorkspaceAnalysisRun,
	expected domain.WorkspaceAnalysisRun,
	config WorkspaceAnalysisRunStartConfig,
	budget WorkspaceAnalysisBudgetPolicy,
	deadlines WorkspaceAnalysisV1Deadlines,
) bool {
	return persisted.ID == expected.ID && sameWorkspaceAnalysisFrozenStartBinding(persisted, WorkspaceAnalysisRunStartCommand{
		WorkspaceID: expected.WorkspaceID, ConversationID: expected.ConversationID,
		QuestionID: expected.QuestionID, AnswerID: expected.AnswerID, WorkflowRunID: expected.WorkflowRunID,
		CreatedAt: expected.CreatedAt,
	}, config, budget, deadlines) &&
		persisted.Status == expected.Status && persisted.Version == expected.Version &&
		persisted.TerminationReason == expected.TerminationReason && persisted.ValidationReceiptID == nil &&
		persisted.ReviewModelRunID == nil && persisted.CompletedAt == nil &&
		reflect.DeepEqual(persisted.Reserved, expected.Reserved) && reflect.DeepEqual(persisted.Settled, expected.Settled) &&
		persisted.UpdatedAt.Equal(expected.UpdatedAt)
}

func sameWorkspaceAnalysisFrozenStartBinding(
	run domain.WorkspaceAnalysisRun,
	command WorkspaceAnalysisRunStartCommand,
	config WorkspaceAnalysisRunStartConfig,
	budget WorkspaceAnalysisBudgetPolicy,
	deadlines WorkspaceAnalysisV1Deadlines,
) bool {
	expected := newWorkspaceAnalysisQueuedRun(run.ID, command, config, budget, deadlines)
	return run.WorkspaceID == expected.WorkspaceID && run.ConversationID == expected.ConversationID &&
		run.QuestionID == expected.QuestionID && run.AnswerID == expected.AnswerID && run.WorkflowRunID == expected.WorkflowRunID &&
		run.DefinitionKey == expected.DefinitionKey && run.DefinitionVersion == expected.DefinitionVersion &&
		run.DefinitionHash == expected.DefinitionHash && run.ToolCatalogHash == expected.ToolCatalogHash &&
		run.PolicyVersion == expected.PolicyVersion && run.ConfigRevision == expected.ConfigRevision &&
		run.DeadlineAt.Equal(expected.DeadlineAt) && reflect.DeepEqual(run.Timeouts, expected.Timeouts) &&
		reflect.DeepEqual(run.Limits, expected.Limits) && run.CreatedAt.Equal(expected.CreatedAt)
}

func newWorkspaceAnalysisRunStartDependencies(
	ids foundation.IDGenerator,
	config WorkspaceAnalysisRunStartConfig,
) (WorkspaceAnalysisBudgetPolicy, WorkspaceAnalysisV1Deadlines, error) {
	if isNilPort(ids) || !canonicalWorkspaceAnalysisSHA256(config.DefinitionHash) ||
		!canonicalWorkspaceAnalysisSHA256(config.ToolCatalogHash) || config.ConfigRevision < 0 {
		return WorkspaceAnalysisBudgetPolicy{}, WorkspaceAnalysisV1Deadlines{}, workspaceAnalysisRunStartError(
			foundation.ErrorInvalidInput,
			ErrorCodeWorkspaceAnalysisRunStartInvalid,
			false,
			errors.New("workspace analysis run dependencies or frozen identity are invalid"),
		)
	}
	budget, err := WorkspaceAnalysisBudgetV1(config.SynthesisProfileMaxOutputTokens)
	if err != nil {
		return WorkspaceAnalysisBudgetPolicy{}, WorkspaceAnalysisV1Deadlines{}, err
	}
	deadlines, err := DeriveWorkspaceAnalysisV1Deadlines(config.Timeouts)
	if err != nil {
		return WorkspaceAnalysisBudgetPolicy{}, WorkspaceAnalysisV1Deadlines{}, err
	}
	if err := deadlines.ValidateRuntimeReadiness(config.RuntimeLimits); err != nil {
		return WorkspaceAnalysisBudgetPolicy{}, WorkspaceAnalysisV1Deadlines{}, err
	}
	return budget, deadlines, nil
}

func sameWorkspaceAnalysisRunDispatchBinding(run domain.WorkspaceAnalysisRun, command WorkspaceAnalysisRunStartCommand) bool {
	return run.WorkspaceID == command.WorkspaceID && run.ConversationID == command.ConversationID &&
		run.QuestionID == command.QuestionID && run.AnswerID == command.AnswerID &&
		run.WorkflowRunID == command.WorkflowRunID && run.CreatedAt.Equal(command.CreatedAt)
}

func validWorkspaceAnalysisRunStartCommand(command WorkspaceAnalysisRunStartCommand) bool {
	ids := []foundation.ID{
		command.WorkspaceID,
		command.ConversationID,
		command.QuestionID,
		command.AnswerID,
		command.WorkflowRunID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return !command.CreatedAt.IsZero()
}

func canonicalWorkspaceAnalysisSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == lowerASCII(value)
}

func lowerASCII(value string) string {
	result := []byte(value)
	for index, current := range result {
		if current >= 'A' && current <= 'Z' {
			result[index] = current + ('a' - 'A')
		}
	}
	return string(result)
}

func isNilOpaqueTransaction(transaction any) bool {
	return isNilPort(transaction)
}

func workspaceAnalysisRunStartError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return applicationError(kind, code, retryable, cause)
}

var _ WorkspaceAnalysisRunStarter = (*WorkspaceAnalysisRunService)(nil)
