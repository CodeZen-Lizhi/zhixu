package application

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

const (
	// HealthAffectedChangeSchemaVersion 是 affected-change outbox 的 payload schema 版本。
	HealthAffectedChangeSchemaVersion = 1
	// HealthAffectedChangeEventVersion 是首版 affected-change 事件版本。
	HealthAffectedChangeEventVersion = 1
	// MaxAffectedChangeDispatchBatch 限制单次 dispatcher 循环持有的事务数量。
	MaxAffectedChangeDispatchBatch = 100

	// ErrorCodeAffectedChangeInvalid 表示 outbox 事件的不可恢复结构错误。
	ErrorCodeAffectedChangeInvalid = "HEALTH_AFFECTED_CHANGE_INVALID"
	// ErrorCodeAffectedChangeSchemaUnsupported 表示 dispatcher 不支持持久化事件版本。
	ErrorCodeAffectedChangeSchemaUnsupported = "HEALTH_AFFECTED_SCHEMA_UNSUPPORTED"
	// ErrorCodeAffectedChangeSourceBindingInvalid 表示 outbox 与源事实不再精确对应。
	ErrorCodeAffectedChangeSourceBindingInvalid = "HEALTH_AFFECTED_SOURCE_BINDING_INVALID"
	// ErrorCodeAffectedChangeScanActive 表示 Workspace 已有其他 active scan，事件已持久退避。
	ErrorCodeAffectedChangeScanActive = "HEALTH_AFFECTED_SCAN_ACTIVE"
)

// AffectedChangeEventType 是 Health affected-change outbox 的稳定事件类型。
type AffectedChangeEventType string

const (
	// AffectedChangeEventKnowledgeChanged 表示正式 Knowledge command 已提交。
	AffectedChangeEventKnowledgeChanged AffectedChangeEventType = "health.affected.knowledge_changed"
	// AffectedChangeEventIndexFailed 表示 Index Version 进入 failed。
	AffectedChangeEventIndexFailed AffectedChangeEventType = "health.affected.index_failed"
	// AffectedChangeEventVectorDegraded 表示 Chunk vector 投影进入失败或 oversized 终态。
	AffectedChangeEventVectorDegraded AffectedChangeEventType = "health.affected.vector_degraded"
	// AffectedChangeEventLexicalDegraded 表示 Chunk lexical 投影进入失败终态。
	AffectedChangeEventLexicalDegraded AffectedChangeEventType = "health.affected.lexical_degraded"
)

// AffectedChangeSourceKind 标识 outbox 必须重新核对的源事实表。
type AffectedChangeSourceKind string

const (
	// AffectedChangeSourceKnowledgeReceipt 绑定 core.knowledge_command_receipt。
	AffectedChangeSourceKnowledgeReceipt AffectedChangeSourceKind = "KNOWLEDGE_COMMAND_RECEIPT"
	// AffectedChangeSourceIndexVersion 绑定 retrieval.index_version。
	AffectedChangeSourceIndexVersion AffectedChangeSourceKind = "INDEX_VERSION"
	// AffectedChangeSourceChunkProjection 绑定 retrieval.chunk_projection。
	AffectedChangeSourceChunkProjection AffectedChangeSourceKind = "CHUNK_PROJECTION"
)

// AffectedChangeEvent 是从 typed/versioned outbox 解码后的不可变源绑定。
type AffectedChangeEvent struct {
	ID               foundation.ID
	WorkspaceID      foundation.ID
	SchemaVersion    int
	EventVersion     int
	EventType        AffectedChangeEventType
	SourceKind       AffectedChangeSourceKind
	SourceKey        string
	SourceHash       string
	AggregateType    string
	AggregateID      foundation.ID
	AggregateSubID   foundation.ID
	AggregateVersion int64
	ChangeStatus     string
	ChangeCode       string
}

// AffectedChangeDispatchOutcome 是一次短事务的持久结果。
type AffectedChangeDispatchOutcome string

const (
	// AffectedChangeDispatchPublished 表示事件已绑定自己的 Scan/Workflow。
	AffectedChangeDispatchPublished AffectedChangeDispatchOutcome = "PUBLISHED"
	// AffectedChangeDispatchDeferred 表示 active Workspace scan 导致持久退避。
	AffectedChangeDispatchDeferred AffectedChangeDispatchOutcome = "DEFERRED"
	// AffectedChangeDispatchPoisoned 表示事件需人工恢复且不会自动重试。
	AffectedChangeDispatchPoisoned AffectedChangeDispatchOutcome = "POISONED"
)

// AffectedChangeDispatchResult 描述一次 outbox 事务的持久结果。
type AffectedChangeDispatchResult struct {
	EventID       foundation.ID
	Outcome       AffectedChangeDispatchOutcome
	ScanID        foundation.ID
	WorkflowRunID foundation.ID
}

// AffectedChangeBatchResult 汇总一次有界 dispatcher 循环。
type AffectedChangeBatchResult struct {
	Processed int
	Published int
	Deferred  int
	Poisoned  int
}

// AffectedChangeDispatchPort 每次在独立短事务内领取并处理最多一个 due event。
type AffectedChangeDispatchPort interface {
	DispatchNext(context.Context) (AffectedChangeDispatchResult, bool, error)
}

// AffectedChangeDispatcher 提供有界批量循环，不拥有 PostgreSQL transaction。
type AffectedChangeDispatcher struct {
	port AffectedChangeDispatchPort
}

// NewAffectedChangeDispatcher 构造 affected-change 批量 dispatcher。
func NewAffectedChangeDispatcher(port AffectedChangeDispatchPort) (*AffectedChangeDispatcher, error) {
	if nilScanDependency(port) {
		return nil, affectedChangeUnavailable(errors.New("health affected-change dispatch port is unavailable"))
	}
	return &AffectedChangeDispatcher{port: port}, nil
}

// DispatchBatch 最多处理 limit 个事件；每个事件由 adapter 独立提交或回滚。
func (dispatcher *AffectedChangeDispatcher) DispatchBatch(ctx context.Context, limit int) (AffectedChangeBatchResult, error) {
	if dispatcher == nil || nilScanDependency(dispatcher.port) {
		return AffectedChangeBatchResult{}, affectedChangeUnavailable(errors.New("health affected-change dispatcher is unavailable"))
	}
	if ctx == nil || limit < 1 || limit > MaxAffectedChangeDispatchBatch {
		return AffectedChangeBatchResult{}, affectedChangeInvalid(errors.New("health affected-change batch request is invalid"))
	}
	var batch AffectedChangeBatchResult
	for batch.Processed < limit {
		result, found, err := dispatcher.port.DispatchNext(ctx)
		if found {
			batch.Processed++
			batch.add(result.Outcome)
		}
		if err != nil {
			return batch, err
		}
		if !found {
			break
		}
	}
	return batch, nil
}

func (result *AffectedChangeBatchResult) add(outcome AffectedChangeDispatchOutcome) {
	switch outcome {
	case AffectedChangeDispatchPublished:
		result.Published++
	case AffectedChangeDispatchDeferred:
		result.Deferred++
	case AffectedChangeDispatchPoisoned:
		result.Poisoned++
	}
}

// AffectedChangePlanner 把受影响源事实保守映射为当前 Workspace scan。
type AffectedChangePlanner struct {
	registry *Registry
}

// NewAffectedChangePlanner 构造 Workspace-only affected-change planner。
func NewAffectedChangePlanner(registry *Registry) (*AffectedChangePlanner, error) {
	if registry == nil || len(registry.Descriptors()) == 0 {
		return nil, affectedChangeUnavailable(errors.New("health affected-change detector registry is unavailable"))
	}
	return &AffectedChangePlanner{registry: registry}, nil
}

// Plan 校验 typed source binding，并生成 event-ID-derived ScanStartRequest。
func (planner *AffectedChangePlanner) Plan(event AffectedChangeEvent, workspaceVersion int64) (ScanStartRequest, error) {
	if planner == nil || planner.registry == nil {
		return ScanStartRequest{}, affectedChangeUnavailable(errors.New("health affected-change planner is unavailable"))
	}
	if err := validateAffectedChangeEvent(event); err != nil {
		return ScanStartRequest{}, err
	}
	if workspaceVersion < 1 {
		return ScanStartRequest{}, affectedChangeManual(ErrorCodeAffectedChangeSourceBindingInvalid, errors.New("health affected-change workspace version is invalid"))
	}
	scope := domain.ScanScope{
		Type:          domain.ScanScopeTypeWorkspace,
		Ref:           event.WorkspaceID,
		Version:       workspaceVersion,
		SchemaVersion: "health-scope/workspace/v1",
	}
	coverage := planner.registry.Coverage(Scope{
		WorkspaceID: event.WorkspaceID,
		Type:        scope.Type,
		Ref:         scope.Ref,
		Version:     scope.Version,
	})
	return canonicalScanStart(ScanStartCommand{
		WorkspaceID:             event.WorkspaceID,
		Scope:                   scope,
		Coverage:                coverage,
		MaxItems:                domain.MaxScanItems,
		IdempotencyKey:          affectedChangeScanIdempotencyKey(event.ID),
		PreventScopeConcurrency: true,
	})
}

// AffectedChangeScanIdempotencyKey 返回由 event ID 唯一派生的 Scan 幂等键。
func AffectedChangeScanIdempotencyKey(eventID foundation.ID) (string, error) {
	if !validScanID(eventID) {
		return "", affectedChangeManual(ErrorCodeAffectedChangeInvalid, errors.New("health affected-change event id is invalid"))
	}
	return affectedChangeScanIdempotencyKey(eventID), nil
}

func affectedChangeScanIdempotencyKey(eventID foundation.ID) string {
	return "health-affected-change:" + string(eventID)
}

func validateAffectedChangeEvent(event AffectedChangeEvent) error {
	if !validScanID(event.ID) || !validScanID(event.WorkspaceID) || !validScanID(event.AggregateID) || event.AggregateVersion < 1 {
		return affectedChangeManual(ErrorCodeAffectedChangeInvalid, errors.New("health affected-change identity is invalid"))
	}
	if event.SchemaVersion != HealthAffectedChangeSchemaVersion || event.EventVersion != HealthAffectedChangeEventVersion {
		return affectedChangeManual(ErrorCodeAffectedChangeSchemaUnsupported, errors.New("health affected-change schema or event version is unsupported"))
	}
	if event.SourceKey == "" || len(event.SourceKey) > 512 || !canonicalAffectedText(event.AggregateType, 64) || !canonicalAffectedText(event.ChangeStatus, 128) || len(event.ChangeCode) > 128 || strings.TrimSpace(event.ChangeCode) != event.ChangeCode {
		return affectedChangeManual(ErrorCodeAffectedChangeInvalid, errors.New("health affected-change source fields are invalid"))
	}
	switch event.SourceKind {
	case AffectedChangeSourceKnowledgeReceipt:
		if event.EventType != AffectedChangeEventKnowledgeChanged || event.AggregateSubID != "" || !validAffectedHash(event.SourceHash) || event.ChangeCode != "" || !validKnowledgeChange(event.ChangeStatus, event.AggregateType) {
			return affectedChangeManual(ErrorCodeAffectedChangeInvalid, errors.New("health knowledge affected-change binding is invalid"))
		}
	case AffectedChangeSourceIndexVersion:
		if event.EventType != AffectedChangeEventIndexFailed || event.AggregateType != "INDEX_VERSION" || event.AggregateSubID != "" || event.SourceKey != string(event.AggregateID) || event.SourceHash != "" || event.ChangeStatus != "failed" || event.ChangeCode == "" {
			return affectedChangeManual(ErrorCodeAffectedChangeInvalid, errors.New("health index affected-change binding is invalid"))
		}
	case AffectedChangeSourceChunkProjection:
		validProjectionStatus := event.EventType == AffectedChangeEventVectorDegraded && (event.ChangeStatus == "failed" || event.ChangeStatus == "skipped_oversized")
		validProjectionStatus = validProjectionStatus || event.EventType == AffectedChangeEventLexicalDegraded && event.ChangeStatus == "failed"
		if !validProjectionStatus || event.AggregateType != "CHUNK_PROJECTION" || !validScanID(event.AggregateSubID) || event.SourceKey != string(event.AggregateID)+":"+string(event.AggregateSubID) || event.SourceHash != "" || event.ChangeCode == "" {
			return affectedChangeManual(ErrorCodeAffectedChangeInvalid, errors.New("health projection affected-change binding is invalid"))
		}
	default:
		return affectedChangeManual(ErrorCodeAffectedChangeInvalid, errors.New("health affected-change source kind is unsupported"))
	}
	return nil
}

func validKnowledgeChange(commandType, aggregateType string) bool {
	switch commandType {
	case "topic.create":
		return aggregateType == "TOPIC"
	case "claim.suggest", "claim.confirm", "claim.transition":
		return aggregateType == "CLAIM"
	case "relation.suggest", "relation.confirm", "relation.transition":
		return aggregateType == "RELATION"
	case "conflict.open", "conflict.transition":
		return aggregateType == "CONFLICT"
	default:
		return false
	}
}

func canonicalAffectedText(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n")
}

func validAffectedHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func affectedChangeInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeAffectedChangeInvalid, false, cause)
}

func affectedChangeManual(code string, cause error) error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, code, false, cause)
}

func affectedChangeUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_AFFECTED_CHANGE_UNAVAILABLE", true, cause)
}
