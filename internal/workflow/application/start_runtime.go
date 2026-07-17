package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	runtimeStartRequestSchemaVersion = 1
	runtimeEventSchemaVersion        = 1
	runtimeEventVersion              = 1
	initialDispatchNo                = 1
)

// RuntimeDependencies 是 Registered Workflow Start 所需的服务端依赖。
type RuntimeDependencies struct {
	// Definitions 提供冻结且只读的 Workflow Definition。
	Definitions *DefinitionRegistry
	// Starter 原子持久化 Workflow 事实并投递首个 Job。
	Starter RuntimeStarter
	// State 提供 DB-time lease、结果归约和控制命令事务。
	State RuntimeStatePort
	// Human 提供 DB-time Human Task 等待与提交事务。
	Human RuntimeHumanStatePort
}

// RuntimeStarter 隐藏 PostgreSQL 与任务投递实现，并负责 Start replay、冲突和 legacy active 判定。
type RuntimeStarter interface {
	// Start 原子创建或重放 Definition、Run、首节点、Outbox 与持久 Job。
	Start(context.Context, RuntimeStartRequest) (RuntimeStartResult, error)
}

// RuntimeStartRequest 是事务型 Workflow Start 的完整应用契约。
type RuntimeStartRequest struct {
	// Definition 是带 canonical graph 的待持久化 Definition 记录。
	Definition domain.Definition
	// DefinitionGraphHash 是 Registry 冻结的 canonical graph hash。
	DefinitionGraphHash string
	// DefinitionInputSchemaVersion 固化 Definition 入口输入契约版本。
	DefinitionInputSchemaVersion int
	// Run 是包含 Runtime identity 的新 Run 候选记录。
	Run domain.Run
	// FirstNode 是唯一 root 对应的首个 Node Run 候选记录。
	FirstNode domain.NodeRun
	// Event 是与 Start 同事务写入的版本化 Outbox 事件。
	Event domain.OutboxEvent
	// RequestHash 与 Run.RequestHash 相同，供事务端口执行 replay binding 校验。
	RequestHash string
}

// JobReceipt 是任务投递边界返回的稳定回执。
type JobReceipt struct {
	// JobID 是持久任务的稳定数字标识。
	JobID int64
	// Duplicate 表示事务端口复用了已存在的同 Args Job。
	Duplicate bool
}

// RuntimeStartResult 返回首次创建或幂等重放后的完整业务身份。
type RuntimeStartResult struct {
	// Run 是首次创建或重放得到的持久 Workflow Run。
	Run domain.Run
	// FirstNode 是首次创建或重放得到的首个 Node Run。
	FirstNode domain.NodeRun
	// Job 是首个节点对应的持久任务回执。
	Job JobReceipt
}

// StartCommand 只以 Workspace、注册 Definition、Input 与 Idempotency Key 决定启动能力。
type StartCommand struct {
	// WorkspaceID 是本次 Workflow Run 所属的知识空间。
	WorkspaceID foundation.ID
	// DefinitionKey 是服务端 Registry 中的稳定流程键。
	DefinitionKey string
	// DefinitionVersion 固化本次运行使用的 Definition 版本。
	DefinitionVersion int64
	// Input 是按 Definition 输入 Schema 校验前的 JSON 请求体。
	Input json.RawMessage
	// IdempotencyKey 在 Workspace 内绑定一次 Start 请求。
	IdempotencyKey string

	// Deprecated: Graph 仅用于校验旧客户端看到的图与 Registry canonical graph 完全一致。
	Graph json.RawMessage
	// Deprecated: FirstNodeKey 仅用于校验旧客户端看到的唯一 root。
	FirstNodeKey string
	// Deprecated: FirstNodeType 仅用于校验旧客户端看到的唯一 root kind。
	FirstNodeType string
}

// NewRuntimeService 构造启用 Registered Workflow Start 的应用服务。
func NewRuntimeService(repository domain.Repository, ids foundation.IDGenerator, clock foundation.Clock, runtime RuntimeDependencies) (*Service, error) {
	service, err := NewService(repository, ids, clock)
	if err != nil {
		return nil, err
	}
	if runtime.Definitions == nil || isNilRuntimeStarter(runtime.Starter) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RUNTIME_DEPENDENCY_MISSING", false, errors.New("workflow runtime dependency is nil"))
	}
	service.definitions = runtime.Definitions
	service.runtime = runtime.Starter
	if !isNilRuntimeStatePort(runtime.State) {
		coordinator, coordinatorErr := NewRuntimeCoordinator(runtime.State)
		if coordinatorErr != nil {
			return nil, coordinatorErr
		}
		service.coordinator = coordinator
	}
	if !isNilRuntimeHumanStatePort(runtime.Human) {
		humanCoordinator, humanErr := NewRuntimeHumanCoordinator(runtime.Human)
		if humanErr != nil {
			return nil, humanErr
		}
		service.humanCoordinator = humanCoordinator
	}
	return service, nil
}

// Start 解析服务端注册 Definition，并把完整 Runtime identity 交给事务端口。
func (s *Service) Start(ctx context.Context, command StartCommand) (domain.Run, error) {
	definitionKey := strings.TrimSpace(command.DefinitionKey)
	idempotencyKey := strings.TrimSpace(command.IdempotencyKey)
	canonicalInput, err := canonicalRuntimeInput(command.Input)
	if command.WorkspaceID == "" || definitionKey == "" || command.DefinitionVersion < 1 || idempotencyKey == "" || len(idempotencyKey) > 128 || err != nil {
		return domain.Run{}, invalid("WORKFLOW_START_INVALID")
	}
	if s.definitions == nil || s.runtime == nil {
		return domain.Run{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED", false, errors.New("registered workflow runtime is not configured"))
	}
	definition, err := s.definitions.Resolve(definitionKey, command.DefinitionVersion)
	if err != nil {
		return domain.Run{}, err
	}
	root, err := uniqueDefinitionRoot(definition)
	if err != nil {
		return domain.Run{}, err
	}
	if err := validateLegacyGraph(command.Graph, definition); err != nil {
		return domain.Run{}, err
	}
	if err := validateLegacyRoot(command.FirstNodeKey, command.FirstNodeType, root); err != nil {
		return domain.Run{}, err
	}
	requestHash, err := computeRuntimeStartRequestHash(command.WorkspaceID, definition, canonicalInput)
	if err != nil {
		return domain.Run{}, err
	}
	request, err := s.buildRuntimeStartRequest(command.WorkspaceID, idempotencyKey, canonicalInput, requestHash, definition, root)
	if err != nil {
		return domain.Run{}, err
	}
	result, err := s.runtime.Start(ctx, request)
	if err != nil {
		return domain.Run{}, err
	}
	if !validRuntimeStartResult(result, request) {
		return domain.Run{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_START_RESULT_INVALID", false, errors.New("workflow runtime returned an incomplete or conflicting start result"))
	}
	return result.Run, nil
}

func (s *Service) buildRuntimeStartRequest(workspaceID foundation.ID, idempotencyKey string, input json.RawMessage, requestHash string, definition domain.RegisteredDefinition, root domain.NodeDefinition) (RuntimeStartRequest, error) {
	definitionID, err := s.ids.New()
	if err != nil {
		return RuntimeStartRequest{}, err
	}
	runID, err := s.ids.New()
	if err != nil {
		return RuntimeStartRequest{}, err
	}
	nodeID, err := s.ids.New()
	if err != nil {
		return RuntimeStartRequest{}, err
	}
	eventID, err := s.ids.New()
	if err != nil {
		return RuntimeStartRequest{}, err
	}
	graph, err := json.Marshal(definition.Graph)
	if err != nil {
		return RuntimeStartRequest{}, foundation.NewError(foundation.ErrorNonRetryableFailure, "WORKFLOW_DEFINITION_CANONICAL_ENCODING_FAILED", false, err)
	}
	now := s.clock.Now()
	nodeIdempotencyKey := runtimeNodeIdempotencyKey(definition, root)
	eventKey := runtimeStartEventKey(workspaceID, idempotencyKey)
	return RuntimeStartRequest{
		Definition:                   domain.Definition{ID: definitionID, WorkspaceID: workspaceID, Key: definition.Key, Version: definition.Version, Graph: graph, CreatedAt: now},
		DefinitionGraphHash:          definition.GraphHash,
		DefinitionInputSchemaVersion: definition.InputSchemaVersion,
		Run: domain.Run{
			ID: runID, WorkspaceID: workspaceID, DefinitionID: definitionID, Status: domain.StatusPending,
			Input: input, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		FirstNode: domain.NodeRun{
			ID: nodeID, RunID: runID, NodeKey: root.Key, NodeType: root.Kind, Status: domain.StatusPending,
			Input: input, IdempotencyKey: nodeIdempotencyKey, InputSchemaVersion: root.InputSchemaVersion,
			OutputSchemaVersion: root.OutputSchemaVersion, DispatchNo: initialDispatchNo,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		Event: domain.OutboxEvent{
			ID: eventID, WorkspaceID: workspaceID, RunID: &runID, Type: "workflow.run.started",
			IdempotencyKey: "workflow-start:" + string(workspaceID) + ":" + idempotencyKey,
			EventKey:       eventKey, SchemaVersion: runtimeEventSchemaVersion, EventVersion: runtimeEventVersion,
			Payload: json.RawMessage(`{}`), OccurredAt: now,
		},
		RequestHash: requestHash,
	}, nil
}

func canonicalRuntimeInput(input json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("workflow input contains multiple JSON values")
		}
		return nil, err
	}
	return json.Marshal(value)
}

func computeRuntimeStartRequestHash(workspaceID foundation.ID, definition domain.RegisteredDefinition, canonicalInput json.RawMessage) (string, error) {
	canonical, err := json.Marshal(struct {
		SchemaVersion       int             `json:"schema_version"`
		WorkspaceID         foundation.ID   `json:"workspace_id"`
		DefinitionKey       string          `json:"definition_key"`
		DefinitionVersion   int64           `json:"definition_version"`
		DefinitionGraphHash string          `json:"definition_graph_hash"`
		InputSchemaVersion  int             `json:"definition_input_schema_version"`
		Input               json.RawMessage `json:"input"`
	}{
		SchemaVersion: runtimeStartRequestSchemaVersion, WorkspaceID: workspaceID,
		DefinitionKey: definition.Key, DefinitionVersion: definition.Version,
		DefinitionGraphHash: definition.GraphHash, InputSchemaVersion: definition.InputSchemaVersion, Input: canonicalInput,
	})
	if err != nil {
		return "", foundation.NewError(foundation.ErrorNonRetryableFailure, "WORKFLOW_START_HASH_FAILED", false, err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func uniqueDefinitionRoot(definition domain.RegisteredDefinition) (domain.NodeDefinition, error) {
	var root domain.NodeDefinition
	count := 0
	for _, node := range definition.Graph.Nodes {
		if len(node.Dependencies) == 0 {
			root = node
			count++
		}
	}
	if count != 1 {
		return domain.NodeDefinition{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_DEFINITION_ROOT_NOT_UNIQUE", false, errors.New("registered workflow definition must contain exactly one root"))
	}
	return root, nil
}

func validateLegacyGraph(raw json.RawMessage, definition domain.RegisteredDefinition) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var graph domain.CanonicalGraph
	if err := decoder.Decode(&graph); err != nil {
		return invalid("WORKFLOW_START_GRAPH_MISMATCH")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return invalid("WORKFLOW_START_GRAPH_MISMATCH")
	}
	candidate, err := canonicalizeDefinition(domain.RegisteredDefinition{
		Key: definition.Key, Version: definition.Version, InputSchemaVersion: definition.InputSchemaVersion, Graph: graph,
	})
	if err != nil || candidate.GraphHash != definition.GraphHash {
		return invalid("WORKFLOW_START_GRAPH_MISMATCH")
	}
	return nil
}

func validateLegacyRoot(key, kind string, root domain.NodeDefinition) error {
	key = strings.TrimSpace(key)
	kind = strings.TrimSpace(kind)
	if key == "" && kind == "" {
		return nil
	}
	if key != root.Key || kind != root.Kind {
		return invalid("WORKFLOW_START_ROOT_MISMATCH")
	}
	return nil
}

func runtimeNodeIdempotencyKey(definition domain.RegisteredDefinition, root domain.NodeDefinition) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("workflow-node-start-v1\x00%s\x00%d\x00%s", definition.Key, definition.Version, root.Key)))
	return "node-start:" + hex.EncodeToString(digest[:])
}

func runtimeStartEventKey(workspaceID foundation.ID, idempotencyKey string) string {
	digest := sha256.Sum256([]byte("workflow-run-started-v1\x00" + string(workspaceID) + "\x00" + idempotencyKey))
	return "workflow.run.started:" + hex.EncodeToString(digest[:])
}

func validRuntimeStartResult(result RuntimeStartResult, request RuntimeStartRequest) bool {
	return result.Run.ID != "" &&
		result.Run.WorkspaceID == request.Run.WorkspaceID &&
		result.Run.DefinitionID != "" &&
		result.Run.IdempotencyKey == request.Run.IdempotencyKey &&
		result.Run.RequestHash == request.RequestHash &&
		result.FirstNode.ID != "" &&
		result.FirstNode.RunID == result.Run.ID &&
		result.FirstNode.NodeKey == request.FirstNode.NodeKey &&
		result.FirstNode.NodeType == request.FirstNode.NodeType &&
		result.FirstNode.IdempotencyKey == request.FirstNode.IdempotencyKey &&
		result.FirstNode.InputSchemaVersion == request.FirstNode.InputSchemaVersion &&
		result.FirstNode.OutputSchemaVersion == request.FirstNode.OutputSchemaVersion &&
		result.FirstNode.DispatchNo == request.FirstNode.DispatchNo &&
		result.Job.JobID > 0
}

func isNilRuntimeStarter(starter RuntimeStarter) bool {
	if starter == nil {
		return true
	}
	value := reflect.ValueOf(starter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
