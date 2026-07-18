package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestionapplication "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	processorUnavailableCode       = "REINDEX_PROCESSOR_UNAVAILABLE"
	processorOptionsInvalidCode    = "REINDEX_PROCESSOR_OPTIONS_INVALID"
	processorRequestInvalidCode    = "REINDEX_PROCESSOR_REQUEST_INVALID"
	processorContextInvalidCode    = "REINDEX_PROCESSOR_CONTEXT_INVALID"
	processorCaptureInvalidCode    = "REINDEX_PROCESSOR_CAPTURE_RESULT_INVALID"
	processorIngestionInvalidCode  = "REINDEX_PROCESSOR_INGESTION_RESULT_INVALID"
	processorSnapshotInvalidCode   = "REINDEX_PROCESSOR_SNAPSHOT_RESULT_INVALID"
	processorLexicalInvalidCode    = "REINDEX_PROCESSOR_LEXICAL_RESULT_INVALID"
	processorRegressionInvalidCode = "REINDEX_PROCESSOR_REGRESSION_RESULT_INVALID"
	processorIndexStateInvalidCode = "REINDEX_PROCESSOR_INDEX_STATE_INVALID"
	processorReadyInvalidCode      = "REINDEX_PROCESSOR_READY_RESULT_INVALID"
	processorFailureSummary        = "reindex processor result requires manual recovery"
	// FTSOnlyTokenizerID 是 PostgreSQL `simple` tsvector Builder 的稳定 Tokenizer 身份。
	FTSOnlyTokenizerID = "postgres-simple"
	// FTSOnlyTokenizerVersion 冻结当前 lexical token_count 与 tsvector 语义。
	FTSOnlyTokenizerVersion = "v1"
	// FTSOnlyTokenizerConfigHash 是 `to_tsvector(simple)` 与 numnode token count 契约的 SHA-256。
	FTSOnlyTokenizerConfigHash = "7c1191591f7bcaac74555b66c0243ef93f8668a60bd373ac7b7078d9b643fad7"
)

// ProcessorContextDisposition 表示数据库加载结果是否仍属于当前 Session。
type ProcessorContextDisposition string

const (
	// ProcessorContextCurrent 表示当前 Attempt 仍持有有效 lease，可继续处理。
	ProcessorContextCurrent ProcessorContextDisposition = "current"
	// ProcessorContextStale 表示 generation、Attempt 或 owner 已失效。
	ProcessorContextStale ProcessorContextDisposition = "stale"
	// ProcessorContextCommitted 表示业务结果已归约，当前 transport 可结束。
	ProcessorContextCommitted ProcessorContextDisposition = "committed"
)

// ProcessorContext 是从单一数据库快照加载并重新验证的 Reindex 恢复事实。
type ProcessorContext struct {
	Delivery         domain.Delivery
	Attempt          domain.DeliveryAttempt
	Request          reindexcontract.RequestV1
	Binding          reindexcontract.Binding
	TargetSourceID   foundation.ID
	IngestionAttempt *ingestiondomain.AttemptRecord
	IndexVersion     *domain.IndexVersion
}

// ProcessorContextLoadResult 显式区分 current、benign stale 和已提交结果。
type ProcessorContextLoadResult struct {
	Disposition ProcessorContextDisposition
	Context     ProcessorContext
}

// ProcessorContextLoader 严格加载 Delivery、Outbox、Writeback、Commit 与 checkpoint 绑定。
type ProcessorContextLoader interface {
	LoadProcessorContext(context.Context, domain.DeliveryFence) (ProcessorContextLoadResult, error)
}

// ProcessorCapturePort 是 committed SourceVersion 捕获用例的最小接口。
type ProcessorCapturePort interface {
	CaptureCommittedSourceVersion(context.Context, workspaceapplication.CaptureCommittedSourceRequest) (workspacedomain.SourceRegistrationResult, error)
}

// ProcessorIngestionPort 是现有 Ingestion Process 用例的最小接口。
type ProcessorIngestionPort interface {
	Process(context.Context, ingestionapplication.ProcessRequest) (ingestionapplication.ProcessResult, error)
}

// ProcessorRetrievalPort 是 Snapshot、Lexical 与 Ready 用例的最小接口。
type ProcessorRetrievalPort interface {
	BeginWorkspaceSnapshot(context.Context, BeginWorkspaceSnapshotRequest) (domain.WorkspaceSnapshotResult, error)
	BuildLexical(context.Context, TransitionRequest) (domain.ProjectionBatchResult, error)
	Ready(context.Context, TransitionRequest) (domain.IndexVersion, error)
}

// ProcessorRegressionPort 是 Ready 前结构回归用例的最小接口。
type ProcessorRegressionPort interface {
	RunSnapshotStructureV1(context.Context, domain.SnapshotRegressionCommand) (domain.SnapshotRegressionResult, error)
}

// ProcessorDependencies 是 Reindex Processor 的显式应用端口。
type ProcessorDependencies struct {
	Contexts   ProcessorContextLoader
	Capture    ProcessorCapturePort
	Ingestion  ProcessorIngestionPort
	Retrieval  ProcessorRetrievalPort
	Regression ProcessorRegressionPort
}

// ProcessorOptions 冻结 M6-B FTS-only Index 配置、Snapshot 上限与业务退避。
type ProcessorOptions struct {
	TokenizerID         string
	TokenizerVersion    string
	TokenizerConfigHash string
	FusionConfig        json.RawMessage
	PageSize            int32
	MaxSources          int64
	MaxChunks           int64
	RetryDelay          time.Duration
}

// DefaultFTSOnlyProcessorOptions 返回与 PostgreSQL Lexical Builder 完全一致的 M6-B 固定配置。
// Snapshot 容量字段保留零值，由 Retrieval Service 选择已验证的产品默认上限。
func DefaultFTSOnlyProcessorOptions(retryDelay time.Duration) ProcessorOptions {
	return ProcessorOptions{
		TokenizerID: FTSOnlyTokenizerID, TokenizerVersion: FTSOnlyTokenizerVersion,
		TokenizerConfigHash: FTSOnlyTokenizerConfigHash,
		FusionConfig:        json.RawMessage(`{"method":"fts_only"}`),
		RetryDelay:          retryDelay,
	}
}

// ProcessorDisposition 表示 Processor 已准备完成、已失效或业务已归约。
type ProcessorDisposition string

const (
	// ProcessorReady 表示 Index 已 Ready，返回 Fence 可进入 CompleteReindexTx。
	ProcessorReady ProcessorDisposition = "ready"
	// ProcessorStale 表示旧 Session 可安全 no-op。
	ProcessorStale ProcessorDisposition = "stale"
	// ProcessorCommitted 表示业务结果已提交，transport 可返回成功。
	ProcessorCommitted ProcessorDisposition = "committed"
)

// ProcessorRequest 只接收 Worker 与 heartbeat 共享的 Delivery Lease Session。
type ProcessorRequest struct {
	Lease *DeliveryLeaseSession
}

// ProcessorResult 返回 CompleteReindexTx 所需的最新 Fence 与稳定身份。
type ProcessorResult struct {
	Disposition    ProcessorDisposition
	WorkspaceID    foundation.ID
	OutboxEventID  foundation.ID
	IndexVersionID foundation.ID
	Fence          domain.DeliveryFence
}

// ProcessorRunner 是 River Worker 依赖的无框架应用接口。
type ProcessorRunner interface {
	Process(context.Context, ProcessorRequest) (ProcessorResult, error)
}

// ProcessorFailureError 是 DeliveryFailureError 的兼容别名；新调用方应使用共享类型。
type ProcessorFailureError = DeliveryFailureError

// Processor 按持久 checkpoint 编排 committed capture 到 Ready，不执行最终完成事务。
type Processor struct {
	dependencies ProcessorDependencies
	options      ProcessorOptions
}

// NewProcessor 校验全部端口和固定 FTS-only 配置后创建 Processor。
func NewProcessor(dependencies ProcessorDependencies, options ProcessorOptions) (*Processor, error) {
	if nilDispatcherDependency(dependencies.Contexts) || nilDispatcherDependency(dependencies.Capture) ||
		nilDispatcherDependency(dependencies.Ingestion) || nilDispatcherDependency(dependencies.Retrieval) ||
		nilDispatcherDependency(dependencies.Regression) {
		return nil, processorError(foundation.ErrorDependencyUnavailable, processorUnavailableCode, false, errors.New("processor dependencies are incomplete"))
	}
	normalized, err := normalizeProcessorOptions(options)
	if err != nil {
		return nil, err
	}
	return &Processor{dependencies: dependencies, options: normalized}, nil
}

// Process 从数据库 checkpoint 恢复并构建完整 FTS-only Workspace Snapshot。
func (processor *Processor) Process(ctx context.Context, request ProcessorRequest) (ProcessorResult, error) {
	if processor == nil || request.Lease == nil || nilDispatcherDependency(processor.dependencies.Contexts) {
		return ProcessorResult{}, processorError(foundation.ErrorInvalidInput, processorRequestInvalidCode, false, errors.New("processor request is incomplete"))
	}
	leaseSnapshot := request.Lease.Snapshot()
	if stopped, ok := stoppedProcessorResult(leaseSnapshot); ok {
		return stopped, nil
	}
	loaded, err := processor.dependencies.Contexts.LoadProcessorContext(ctx, leaseSnapshot.Fence)
	if err != nil {
		if processorErrorCodeOf(err) == processorContextInvalidCode {
			return ProcessorResult{}, processor.manualFailure(processorContextInvalidCode, err)
		}
		return ProcessorResult{}, err
	}
	switch loaded.Disposition {
	case ProcessorContextStale:
		return ProcessorResult{Disposition: ProcessorStale}, nil
	case ProcessorContextCommitted:
		return ProcessorResult{Disposition: ProcessorCommitted}, nil
	case ProcessorContextCurrent:
	default:
		return ProcessorResult{}, processor.manualFailure(processorContextInvalidCode, errors.New("processor context disposition is invalid"))
	}
	if err := validateProcessorContext(leaseSnapshot.Fence, loaded.Context); err != nil {
		return ProcessorResult{}, processor.manualFailure(processorContextInvalidCode, err)
	}
	state := processorStateFromContext(loaded.Context)

	if state.sourceVersionID == "" {
		registration, captureErr := processor.dependencies.Capture.CaptureCommittedSourceVersion(ctx, workspaceapplication.CaptureCommittedSourceRequest{
			WorkspaceID: loaded.Context.Request.WorkspaceID, GitCommit: loaded.Context.Request.GitCommit,
			RelativePath: loaded.Context.Request.TargetPath, ExpectedHash: loaded.Context.Request.ResultHash,
		})
		if captureErr != nil {
			return ProcessorResult{}, processor.classifiedFailure(captureErr, "committed source capture failed")
		}
		if err := validateProcessorCapture(loaded.Context, registration); err != nil {
			return ProcessorResult{}, processor.manualFailure(processorCaptureInvalidCode, err)
		}
		state.sourceID = registration.Source.ID
		state.sourceVersionID = registration.Version.ID
		checkpoint, checkpointErr := request.Lease.Checkpoint(ctx, domain.DeliveryCheckpoint{
			Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: state.sourceVersionID,
		})
		if checkpointErr != nil {
			return ProcessorResult{}, checkpointErr
		}
		if stopped, ok := stoppedProcessorResult(checkpoint); ok {
			return stopped, nil
		}
	}

	ingestionWasRun := false
	if state.projectionID == "" {
		ingestionWasRun = true
		result, ingestionErr := processor.dependencies.Ingestion.Process(ctx, ingestionapplication.ProcessRequest{
			SourceVersionID: state.sourceVersionID,
			IdempotencyKey:  processorIngestionKey(loaded.Context.Delivery.OutboxEventID, loaded.Context.Delivery.DispatchNo),
			AttemptNumber:   int32(loaded.Context.Delivery.DispatchNo),
		})
		if ingestionErr != nil {
			if failure := processor.persistedIngestionFailure(result, ingestionErr, state.sourceVersionID); failure != nil {
				return ProcessorResult{}, failure
			}
			return ProcessorResult{}, ingestionErr
		}
		if err := validateProcessorIngestion(loaded.Context, state.sourceVersionID, result.Attempt); err != nil {
			return ProcessorResult{}, processor.manualFailure(processorIngestionInvalidCode, err)
		}
		state.ingestionAttempt = &result.Attempt
		state.ingestionAttemptID = result.Attempt.ID
		state.projectionID = *result.Attempt.ParseProjectionID
	}
	if state.ingestionAttempt == nil {
		return ProcessorResult{}, processor.manualFailure(processorContextInvalidCode, errors.New("ingestion checkpoint evidence is missing"))
	}
	if ingestionWasRun || loaded.Context.Attempt.IngestionAttemptID == nil {
		checkpoint, checkpointErr := request.Lease.Checkpoint(ctx, domain.DeliveryCheckpoint{
			Stage: domain.DeliveryCheckpointIngested, SourceVersionID: state.sourceVersionID,
			IngestionAttemptID: state.ingestionAttemptID, ParseProjectionID: state.projectionID,
		})
		if checkpointErr != nil {
			return ProcessorResult{}, checkpointErr
		}
		if stopped, ok := stoppedProcessorResult(checkpoint); ok {
			return stopped, nil
		}
	}

	if state.index == nil {
		snapshot, snapshotErr := processor.dependencies.Retrieval.BeginWorkspaceSnapshot(ctx, BeginWorkspaceSnapshotRequest{
			WorkspaceID: loaded.Context.Request.WorkspaceID, TargetSourceID: state.sourceID,
			TargetSourceVersionID: state.sourceVersionID, TargetParseProjectionID: state.projectionID,
			TokenizerID: processor.options.TokenizerID, TokenizerVersion: processor.options.TokenizerVersion,
			TokenizerConfigHash: processor.options.TokenizerConfigHash, FusionConfig: append(json.RawMessage(nil), processor.options.FusionConfig...),
			SourceSnapshotRef:  processorSnapshotRef(loaded.Context.Delivery.OutboxEventID),
			IdempotencyKey:     processorSnapshotKey(loaded.Context.Delivery.OutboxEventID),
			ProcessingContract: processorContract(*state.ingestionAttempt), PageSize: processor.options.PageSize,
			MaxSources: processor.options.MaxSources, MaxChunks: processor.options.MaxChunks,
		})
		if snapshotErr != nil {
			return ProcessorResult{}, processor.deterministicStageFailure(snapshotErr, "workspace snapshot build failed")
		}
		if err := processor.validateSnapshotResult(loaded.Context, state, snapshot); err != nil {
			return ProcessorResult{}, processor.manualFailure(processorSnapshotInvalidCode, err)
		}
		state.index = &snapshot.IndexVersion
		state.excludedSourceCount = snapshot.ExcludedSourceCount
		checkpoint, checkpointErr := request.Lease.Checkpoint(ctx, domain.DeliveryCheckpoint{
			Stage: domain.DeliveryCheckpointIndexBuilding, SourceVersionID: state.sourceVersionID,
			IngestionAttemptID: state.ingestionAttemptID, ParseProjectionID: state.projectionID,
			IndexVersionID: state.index.ID, ExcludedSourceCount: int64Pointer(state.excludedSourceCount),
		})
		if checkpointErr != nil {
			return ProcessorResult{}, checkpointErr
		}
		if stopped, ok := stoppedProcessorResult(checkpoint); ok {
			return stopped, nil
		}
	}
	if err := validateProcessorIndex(loaded.Context, state); err != nil {
		return ProcessorResult{}, processor.manualFailure(processorIndexStateInvalidCode, err)
	}
	if state.index.Status == domain.IndexStatusFailed {
		return ProcessorResult{}, processor.failedIndex(*state.index)
	}
	if state.index.Status == domain.IndexStatusReady {
		if state.regression == nil {
			return ProcessorResult{}, processor.manualFailure(processorIndexStateInvalidCode, errors.New("ready index has no persisted regression checkpoint"))
		}
		return processor.readyResult(request.Lease, loaded.Context, *state.index)
	}
	if state.index.Status != domain.IndexStatusBuilding {
		return ProcessorResult{}, processor.manualFailure(processorIndexStateInvalidCode, errors.New("processor index is neither building nor ready"))
	}

	if state.regression == nil {
		lexical, lexicalErr := processor.dependencies.Retrieval.BuildLexical(ctx, TransitionRequest{
			WorkspaceID: loaded.Context.Request.WorkspaceID, IndexVersionID: state.index.ID, ExpectedVersion: state.index.Version,
		})
		if lexicalErr != nil {
			return ProcessorResult{}, processor.deterministicStageFailure(lexicalErr, "lexical index build failed")
		}
		if err := validateProcessorLexical(*state.index, lexical); err != nil {
			return ProcessorResult{}, processor.manualFailure(processorLexicalInvalidCode, err)
		}
		regression, regressionErr := processor.dependencies.Regression.RunSnapshotStructureV1(ctx, domain.SnapshotRegressionCommand{
			WorkspaceID: loaded.Context.Request.WorkspaceID, DeliveryID: loaded.Context.Delivery.ID,
			TargetSourceID: state.sourceID, TargetSourceVersionID: state.sourceVersionID,
			TargetResultHash: loaded.Context.Request.ResultHash, TargetParseProjectionID: state.projectionID,
			IndexVersionID: state.index.ID,
		})
		if regressionErr != nil {
			if processorErrorCodeOf(regressionErr) == domain.ErrorCodeSnapshotStructureRegressionFailed {
				return ProcessorResult{}, newProcessorFailure(
					domain.DeliveryFailureNonRetryable, foundation.ErrorNonRetryableFailure,
					domain.ErrorCodeSnapshotStructureRegressionFailed, "snapshot structural regression failed", 0, regressionErr,
				)
			}
			return ProcessorResult{}, processor.deterministicStageFailure(regressionErr, "snapshot structural regression could not complete")
		}
		if err := validateProcessorRegression(regression); err != nil {
			return ProcessorResult{}, processor.manualFailure(processorRegressionInvalidCode, err)
		}
		state.regression = &domain.DeliveryRegression{Code: regression.Code, Hash: regression.Hash, PassedAt: regression.PassedAt}
		checkpoint, checkpointErr := request.Lease.Checkpoint(ctx, domain.DeliveryCheckpoint{
			Stage: domain.DeliveryCheckpointRegressionPassed, SourceVersionID: state.sourceVersionID,
			IngestionAttemptID: state.ingestionAttemptID, ParseProjectionID: state.projectionID,
			IndexVersionID: state.index.ID, ExcludedSourceCount: int64Pointer(state.excludedSourceCount),
			RegressionCode: regression.Code, RegressionHash: regression.Hash,
		})
		if checkpointErr != nil {
			return ProcessorResult{}, checkpointErr
		}
		if stopped, ok := stoppedProcessorResult(checkpoint); ok {
			return stopped, nil
		}
	}

	ready, readyErr := processor.dependencies.Retrieval.Ready(ctx, TransitionRequest{
		WorkspaceID: loaded.Context.Request.WorkspaceID, IndexVersionID: state.index.ID, ExpectedVersion: state.index.Version,
	})
	if readyErr != nil {
		return ProcessorResult{}, processor.deterministicStageFailure(readyErr, "index ready transition failed")
	}
	if err := validateProcessorReady(*state.index, ready); err != nil {
		return ProcessorResult{}, processor.manualFailure(processorReadyInvalidCode, err)
	}
	return processor.readyResult(request.Lease, loaded.Context, ready)
}

type processorState struct {
	sourceID, sourceVersionID, ingestionAttemptID, projectionID foundation.ID
	ingestionAttempt                                            *ingestiondomain.AttemptRecord
	index                                                       *domain.IndexVersion
	excludedSourceCount                                         int64
	regression                                                  *domain.DeliveryRegression
}

func processorStateFromContext(contextValue ProcessorContext) processorState {
	state := processorState{sourceID: contextValue.TargetSourceID, ingestionAttempt: contextValue.IngestionAttempt, index: contextValue.IndexVersion, regression: contextValue.Delivery.Regression}
	if contextValue.Delivery.SourceVersionID != nil {
		state.sourceVersionID = *contextValue.Delivery.SourceVersionID
	}
	if contextValue.Delivery.ParseProjectionID != nil {
		state.projectionID = *contextValue.Delivery.ParseProjectionID
	}
	if contextValue.IngestionAttempt != nil {
		state.ingestionAttemptID = contextValue.IngestionAttempt.ID
	}
	if contextValue.Delivery.ExcludedSourceCount != nil {
		state.excludedSourceCount = *contextValue.Delivery.ExcludedSourceCount
	}
	return state
}

func (processor *Processor) readyResult(lease *DeliveryLeaseSession, contextValue ProcessorContext, index domain.IndexVersion) (ProcessorResult, error) {
	snapshot := lease.Snapshot()
	if stopped, ok := stoppedProcessorResult(snapshot); ok {
		return stopped, nil
	}
	return ProcessorResult{
		Disposition: ProcessorReady, WorkspaceID: contextValue.Request.WorkspaceID,
		OutboxEventID: contextValue.Delivery.OutboxEventID, IndexVersionID: index.ID, Fence: snapshot.Fence,
	}, nil
}

func stoppedProcessorResult(snapshot DeliveryLeaseSnapshot) (ProcessorResult, bool) {
	switch snapshot.Disposition {
	case DeliveryLeaseStale:
		return ProcessorResult{Disposition: ProcessorStale}, true
	case DeliveryLeaseCommitted:
		return ProcessorResult{Disposition: ProcessorCommitted}, true
	default:
		return ProcessorResult{}, false
	}
}

func normalizeProcessorOptions(options ProcessorOptions) (ProcessorOptions, error) {
	options.TokenizerID = strings.TrimSpace(options.TokenizerID)
	options.TokenizerVersion = strings.TrimSpace(options.TokenizerVersion)
	options.TokenizerConfigHash = strings.ToLower(strings.TrimSpace(options.TokenizerConfigHash))
	if !processorText(options.TokenizerID) || !processorText(options.TokenizerVersion) || !processorHash(options.TokenizerConfigHash) ||
		!processorJSONObject(options.FusionConfig) || options.PageSize < 0 || options.PageSize > domain.MaxSnapshotPageSize ||
		options.MaxSources < 0 || options.MaxChunks < 0 || options.RetryDelay <= 0 || options.RetryDelay > MaxDeliveryRetryDelay {
		return ProcessorOptions{}, processorError(foundation.ErrorInvalidInput, processorOptionsInvalidCode, false, errors.New("processor options are invalid"))
	}
	canonical, err := canonicalJSONObject(options.FusionConfig)
	if err != nil {
		return ProcessorOptions{}, processorError(foundation.ErrorInvalidInput, processorOptionsInvalidCode, false, err)
	}
	options.FusionConfig = canonical
	return options, nil
}

func validateProcessorContext(fence domain.DeliveryFence, contextValue ProcessorContext) error {
	if domain.ValidateDelivery(contextValue.Delivery) != nil || domain.ValidateDeliveryAttempt(contextValue.Attempt) != nil ||
		contextValue.Delivery.Status != domain.DeliveryStatusProcessing || contextValue.Attempt.Status != domain.DeliveryAttemptProcessing ||
		contextValue.Delivery.ID != fence.DeliveryID || contextValue.Delivery.DispatchNo != fence.DispatchNo ||
		contextValue.Delivery.AttemptNo != fence.AttemptNo || contextValue.Delivery.CurrentAttemptID == nil ||
		*contextValue.Delivery.CurrentAttemptID != fence.AttemptID || contextValue.Attempt.ID != fence.AttemptID ||
		contextValue.Attempt.DeliveryID != fence.DeliveryID || contextValue.Attempt.DispatchNo != fence.DispatchNo ||
		contextValue.Attempt.AttemptNo != fence.AttemptNo || contextValue.Attempt.LeaseOwner != fence.Owner ||
		contextValue.Delivery.Version < fence.DeliveryVersion {
		return errors.New("delivery, attempt, or fence binding is inconsistent")
	}
	if _, err := reindexcontract.EncodeCanonical(contextValue.Request); err != nil {
		return errors.New("outbox request is invalid")
	}
	if err := reindexcontract.ValidateBinding(contextValue.Request, contextValue.Binding); err != nil ||
		contextValue.Request.WorkspaceID != contextValue.Delivery.WorkspaceID ||
		contextValue.Request.WritebackExecutionID != contextValue.Delivery.WritebackExecutionID {
		return errors.New("outbox, writeback, or delivery binding is inconsistent")
	}
	if contextValue.Delivery.SourceVersionID == nil {
		if contextValue.TargetSourceID != "" || contextValue.IngestionAttempt != nil || contextValue.IndexVersion != nil {
			return errors.New("pre-capture context contains later-stage evidence")
		}
		return nil
	}
	if _, err := foundation.ParseID(string(contextValue.TargetSourceID)); err != nil {
		return errors.New("target source identity is invalid")
	}
	if contextValue.Delivery.ParseProjectionID == nil {
		if contextValue.IngestionAttempt != nil || contextValue.IndexVersion != nil || contextValue.Attempt.IngestionAttemptID != nil {
			return errors.New("source-only context contains later-stage evidence")
		}
		return nil
	}
	if contextValue.IngestionAttempt == nil || !validPersistedProcessorIngestion(contextValue, *contextValue.IngestionAttempt) {
		return errors.New("ingestion checkpoint evidence is invalid")
	}
	if contextValue.Attempt.IngestionAttemptID != nil && *contextValue.Attempt.IngestionAttemptID != contextValue.IngestionAttempt.ID {
		return errors.New("current delivery attempt references different ingestion evidence")
	}
	if contextValue.Delivery.IndexVersionID == nil {
		if contextValue.IndexVersion != nil {
			return errors.New("ingested context contains an unexpected index")
		}
		return nil
	}
	if contextValue.IndexVersion == nil || contextValue.IndexVersion.ID != *contextValue.Delivery.IndexVersionID ||
		contextValue.IndexVersion.WorkspaceID != contextValue.Delivery.WorkspaceID || domain.ValidateIndexVersion(*contextValue.IndexVersion) != nil {
		return errors.New("index checkpoint evidence is invalid")
	}
	return nil
}

func validPersistedProcessorIngestion(contextValue ProcessorContext, attempt ingestiondomain.AttemptRecord) bool {
	if attempt.ID == "" || attempt.WorkspaceID != contextValue.Delivery.WorkspaceID ||
		contextValue.Delivery.SourceVersionID == nil || attempt.SourceVersionID != *contextValue.Delivery.SourceVersionID ||
		attempt.Status != ingestiondomain.AttemptChunked || attempt.SecurityStatus != ingestiondomain.SecurityPassed ||
		attempt.ParseProjectionID == nil || contextValue.Delivery.ParseProjectionID == nil ||
		*attempt.ParseProjectionID != *contextValue.Delivery.ParseProjectionID || attempt.WorkflowRunID != nil ||
		attempt.AttemptNumber < 1 || int(attempt.AttemptNumber) > contextValue.Delivery.DispatchNo ||
		attempt.IdempotencyKey != processorIngestionKey(contextValue.Delivery.OutboxEventID, int(attempt.AttemptNumber)) ||
		!processorText(attempt.ParserID) || !processorText(attempt.ParserVersion) || !processorHash(attempt.ParserConfigHash) ||
		!processorText(attempt.ChunkStrategyVersion) || !processorText(attempt.SchemaVersion) ||
		attempt.CompletedAt == nil || attempt.StartedAt.IsZero() || attempt.CompletedAt.Before(attempt.StartedAt) {
		return false
	}
	return true
}

func validateProcessorCapture(contextValue ProcessorContext, result workspacedomain.SourceRegistrationResult) error {
	if result.Source.ID == "" || result.Source.WorkspaceID != contextValue.Request.WorkspaceID ||
		result.Source.OriginalLocation != contextValue.Request.TargetPath || result.Version.ID == "" ||
		result.Version.SourceID != result.Source.ID || result.Version.ContentHash != contextValue.Request.ResultHash ||
		result.Version.OriginalContentLocation != contextValue.Request.TargetPath || result.Version.CapturedAt.IsZero() {
		return errors.New("committed capture result does not match outbox request")
	}
	return nil
}

func validateProcessorIngestion(contextValue ProcessorContext, sourceVersionID foundation.ID, result ingestiondomain.AttemptRecord) error {
	projection := result.ParseProjectionID
	if result.ID == "" || result.WorkspaceID != contextValue.Request.WorkspaceID || result.SourceVersionID != sourceVersionID ||
		result.WorkflowRunID != nil || result.Status != ingestiondomain.AttemptChunked || result.SecurityStatus != ingestiondomain.SecurityPassed ||
		projection == nil || result.AttemptNumber != int32(contextValue.Delivery.DispatchNo) ||
		result.IdempotencyKey != processorIngestionKey(contextValue.Delivery.OutboxEventID, contextValue.Delivery.DispatchNo) ||
		!processorText(result.ParserID) || !processorText(result.ParserVersion) || !processorHash(result.ParserConfigHash) ||
		!processorText(result.ChunkStrategyVersion) || !processorText(result.SchemaVersion) ||
		result.CompletedAt == nil || result.StartedAt.IsZero() || result.CompletedAt.Before(result.StartedAt) {
		return errors.New("ingestion result does not prove a successful current-generation projection")
	}
	return nil
}

func (processor *Processor) validateSnapshotResult(contextValue ProcessorContext, state processorState, result domain.WorkspaceSnapshotResult) error {
	index := result.IndexVersion
	if domain.ValidateIndexVersion(index) != nil || index.WorkspaceID != contextValue.Request.WorkspaceID ||
		index.Status != domain.IndexStatusBuilding || index.EmbeddingVersionID != nil ||
		index.SourceSnapshotRef != processorSnapshotRef(contextValue.Delivery.OutboxEventID) ||
		index.IdempotencyKey != processorSnapshotKey(contextValue.Delivery.OutboxEventID) ||
		index.TokenizerID != processor.options.TokenizerID || index.TokenizerVersion != processor.options.TokenizerVersion ||
		index.TokenizerConfigHash != processor.options.TokenizerConfigHash || !domain.SameJSONValue(index.FusionConfig, processor.options.FusionConfig) ||
		index.ProcessingContract == nil || !domain.SameProcessingContract(index.ProcessingContract, processorContractPointer(*state.ingestionAttempt)) ||
		result.SourceCount <= 0 || result.ChunkCount != index.ExpectedChunkCount || result.ExcludedSourceCount < 0 ||
		index.ExpectedSourceCount == nil || result.SourceCount != *index.ExpectedSourceCount {
		return errors.New("snapshot result does not match processor request")
	}
	return nil
}

func validateProcessorIndex(contextValue ProcessorContext, state processorState) error {
	if state.index == nil || domain.ValidateIndexVersion(*state.index) != nil || state.index.WorkspaceID != contextValue.Request.WorkspaceID ||
		state.index.ID == "" || state.index.SourceSnapshotRef != processorSnapshotRef(contextValue.Delivery.OutboxEventID) ||
		state.index.IdempotencyKey != processorSnapshotKey(contextValue.Delivery.OutboxEventID) || state.index.ProcessingContract == nil ||
		state.ingestionAttempt == nil || !domain.SameProcessingContract(state.index.ProcessingContract, processorContractPointer(*state.ingestionAttempt)) {
		return errors.New("persisted index does not match processor checkpoint")
	}
	return nil
}

func validateProcessorLexical(index domain.IndexVersion, result domain.ProjectionBatchResult) error {
	returnIfInvalid := result.IndexVersionID != index.ID || result.AttemptedCount < 0 || result.InsertedCount < 0 ||
		result.ReplayedCount < 0 || result.InsertedCount+result.ReplayedCount != result.AttemptedCount ||
		result.AttemptedCount != index.ExpectedChunkCount
	if returnIfInvalid {
		return errors.New("lexical build result does not cover the frozen manifest")
	}
	return nil
}

func validateProcessorRegression(result domain.SnapshotRegressionResult) error {
	if result.Code != domain.SnapshotStructureRegressionV1 || !processorHash(result.Hash) || result.PassedAt.IsZero() {
		return errors.New("snapshot regression result is invalid")
	}
	return nil
}

func validateProcessorReady(building, ready domain.IndexVersion) error {
	if domain.ValidateIndexVersion(ready) != nil || ready.ID != building.ID || ready.WorkspaceID != building.WorkspaceID ||
		ready.Status != domain.IndexStatusReady || ready.Version <= building.Version || ready.UpdatedAt.Before(building.UpdatedAt) ||
		ready.SourceSnapshotRef != building.SourceSnapshotRef || ready.IdempotencyKey != building.IdempotencyKey ||
		ready.ManifestHash != building.ManifestHash || ready.ExpectedChunkCount != building.ExpectedChunkCount ||
		ready.SourceManifestHash != building.SourceManifestHash || !optionalInt64Equal(ready.ExpectedSourceCount, building.ExpectedSourceCount) ||
		!domain.SameProcessingContract(ready.ProcessingContract, building.ProcessingContract) {
		return errors.New("ready result does not preserve the building index binding")
	}
	return nil
}

func (processor *Processor) persistedIngestionFailure(result ingestionapplication.ProcessResult, cause error, sourceVersionID foundation.ID) error {
	attempt := result.Attempt
	if attempt.ID == "" || attempt.SourceVersionID != sourceVersionID || attempt.ParseProjectionID != nil ||
		(attempt.Status != ingestiondomain.AttemptParseFailed && attempt.Status != ingestiondomain.AttemptCancelled) ||
		attempt.CompletedAt == nil || !processorCode(attempt.ErrorCode) {
		return nil
	}
	class, kind, delay := domain.DeliveryFailureNonRetryable, foundation.ErrorNonRetryableFailure, time.Duration(0)
	if attempt.Retryable {
		class, kind, delay = domain.DeliveryFailureRetryable, foundation.ErrorRetryableFailure, processor.options.RetryDelay
	}
	return newProcessorFailure(class, kind, attempt.ErrorCode, "ingestion attempt reached a persisted failure", delay, cause)
}

func (processor *Processor) classifiedFailure(cause error, summary string) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	var classified *foundation.Error
	if !errors.As(cause, &classified) || !processorCode(classified.Code) {
		return cause
	}
	switch classified.Kind {
	case foundation.ErrorDependencyUnavailable, foundation.ErrorRetryableFailure:
		return newProcessorFailure(domain.DeliveryFailureRetryable, classified.Kind, classified.Code, summary, processor.options.RetryDelay, cause)
	case foundation.ErrorInvalidInput, foundation.ErrorNotFound, foundation.ErrorPermissionDenied, foundation.ErrorNonRetryableFailure:
		return newProcessorFailure(domain.DeliveryFailureNonRetryable, classified.Kind, classified.Code, summary, 0, cause)
	case foundation.ErrorVersionConflict, foundation.ErrorConsistencyViolation, foundation.ErrorManualRecoveryRequired:
		return newProcessorFailure(domain.DeliveryFailureManualRecovery, classified.Kind, classified.Code, processorFailureSummary, 0, cause)
	default:
		return cause
	}
}

// deterministicStageFailure 只归约已经由领域或 Adapter 明确分类的确定性错误。
// 数据库连接、序列化冲突或 Commit 响应未知仍原样返回，由同一 River dispatch 重投并依靠幂等恢复。
func (processor *Processor) deterministicStageFailure(cause error, summary string) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	var classified *foundation.Error
	if !errors.As(cause, &classified) || !processorCode(classified.Code) {
		return cause
	}
	switch classified.Kind {
	case foundation.ErrorInvalidInput, foundation.ErrorNotFound, foundation.ErrorPermissionDenied, foundation.ErrorNonRetryableFailure:
		return newProcessorFailure(domain.DeliveryFailureNonRetryable, classified.Kind, classified.Code, summary, 0, cause)
	case foundation.ErrorVersionConflict, foundation.ErrorConsistencyViolation, foundation.ErrorManualRecoveryRequired:
		return newProcessorFailure(domain.DeliveryFailureManualRecovery, classified.Kind, classified.Code, processorFailureSummary, 0, cause)
	default:
		return cause
	}
}

func (processor *Processor) failedIndex(index domain.IndexVersion) error {
	if index.FailureCode == domain.ErrorCodeSnapshotStructureRegressionFailed {
		return newProcessorFailure(domain.DeliveryFailureNonRetryable, foundation.ErrorNonRetryableFailure,
			index.FailureCode, "snapshot structural regression failed", 0, errors.New("snapshot index is failed"))
	}
	return processor.manualFailure(processorIndexStateInvalidCode, errors.New("index reached an unexpected failed state"))
}

func (processor *Processor) manualFailure(code string, cause error) error {
	foundationCause := processorError(foundation.ErrorConsistencyViolation, code, false, cause)
	return newProcessorFailure(domain.DeliveryFailureManualRecovery, foundation.ErrorConsistencyViolation, code, processorFailureSummary, 0, foundationCause)
}

func newProcessorFailure(class domain.DeliveryFailureClass, kind foundation.ErrorKind, code, summary string, retryDelay time.Duration, cause error) error {
	return NewDeliveryFailure(domain.DeliveryFailure{Class: class, ErrorKind: kind, Code: code, Summary: summary}, retryDelay, cause)
}

// NewProcessorFailure 创建供 Worker 测试或受控 Adapter 返回的安全业务归约错误。
// 非法 Failure/延迟返回普通 consistency error，不能被 Worker 当作可归约结果。
func NewProcessorFailure(failure domain.DeliveryFailure, retryDelay time.Duration, cause error) error {
	return NewDeliveryFailure(failure, retryDelay, cause)
}

func processorContract(attempt ingestiondomain.AttemptRecord) domain.ProcessingContract {
	return domain.ProcessingContract{
		ParserID: attempt.ParserID, ParserVersion: attempt.ParserVersion, ParserConfigHash: attempt.ParserConfigHash,
		ChunkStrategyVersion: attempt.ChunkStrategyVersion, SchemaVersion: attempt.SchemaVersion,
	}
}

func processorContractPointer(attempt ingestiondomain.AttemptRecord) *domain.ProcessingContract {
	value := processorContract(attempt)
	return &value
}

func processorSnapshotRef(eventID foundation.ID) string { return "reindex-v1:" + string(eventID) }
func processorSnapshotKey(eventID foundation.ID) string {
	return processorSnapshotRef(eventID) + ":snapshot"
}
func processorIngestionKey(eventID foundation.ID, dispatchNo int) string {
	return fmt.Sprintf("reindex-v1:%s:ingestion:%d", eventID, dispatchNo)
}

func processorText(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.RuneCountInString(value) <= 128
}

func processorHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func processorJSONObject(value json.RawMessage) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

func processorCode(value string) bool {
	if value == "" || len(value) > 128 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func processorErrorCodeOf(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func optionalInt64Equal(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func int64Pointer(value int64) *int64 { return &value }

func processorError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}
