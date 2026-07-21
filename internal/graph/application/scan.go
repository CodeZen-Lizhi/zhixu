package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	// SemanticLinkScanWorkflowDefinitionKey 是首版 Topic scan 的服务端 Definition 键。
	SemanticLinkScanWorkflowDefinitionKey = "graph.semantic-link-topic-scan"
	// SemanticLinkScanWorkflowDefinitionVersion 是冻结的 Workflow graph 版本。
	SemanticLinkScanWorkflowDefinitionVersion int64 = 1
	// SemanticLinkScanInputSchemaVersion 是 Workflow 输入 Schema 版本。
	SemanticLinkScanInputSchemaVersion = 1
	// SemanticLinkScanOutputSchemaVersion 是 Workflow 输出 Schema 版本。
	SemanticLinkScanOutputSchemaVersion = 1
	// SemanticLinkScanRuleID 是首版确定性发现规则的稳定身份。
	SemanticLinkScanRuleID foundation.ID = "7b39cb10-146c-4b99-a506-f99d04e7c621"
	// SemanticLinkScanRuleVersion 是首版确定性发现规则版本。
	SemanticLinkScanRuleVersion = "semantic-link-rules/v1"
	// SemanticLinkTopicScanScopeSchemaVersion 是 Topic scope 的冻结契约版本。
	SemanticLinkTopicScanScopeSchemaVersion = "semantic-link-topic-scan-scope/v1"
	// SemanticLinkScanWorkflowGenerationVersion 进入 scan fingerprint，和 Workflow Definition 版本分开冻结。
	SemanticLinkScanWorkflowGenerationVersion = "graph.semantic-link-topic-scan/v1"
	// MaxSemanticLinkScanPageNodes 是单个 Workflow page 的节点上限。
	MaxSemanticLinkScanPageNodes = 100
	// MaxSemanticLinkScanPagePairsPerNode 是单节点允许的有界候选 pair 数。
	MaxSemanticLinkScanPagePairsPerNode = 100
	maxSemanticLinkScanIdempotencyKey   = 128
)

// SemanticLinkScanStartCommand 是 Topic scan 的异步启动命令。
type SemanticLinkScanStartCommand struct {
	WorkspaceID    foundation.ID
	Scope          graphdomain.SemanticLinkScanScope
	Generation     graphdomain.SemanticLinkScanGeneration
	TotalNodes     int64
	IdempotencyKey string
}

// SemanticLinkTopicScanPlan 是服务端从正式 Topic/Claim 事实生成的扫描计划。
type SemanticLinkTopicScanPlan struct {
	WorkspaceID  foundation.ID
	TopicID      foundation.ID
	TopicVersion int64
	TotalNodes   int64
}

// SemanticLinkTopicScanPlanner 读取正式 Topic version 和有资格 Claim 计数。
type SemanticLinkTopicScanPlanner interface {
	PlanTopicScan(context.Context, foundation.ID, foundation.ID) (SemanticLinkTopicScanPlan, error)
}

// SemanticLinkTopicScanRequest 是公共 HTTP 边界允许提交的最小命令。
type SemanticLinkTopicScanRequest struct {
	WorkspaceID    foundation.ID
	TopicID        foundation.ID
	IdempotencyKey string
}

// SemanticLinkScanHTTPService 是候选 HTTP 依赖的持久扫描端口。
type SemanticLinkScanHTTPService interface {
	StartTopicScan(context.Context, SemanticLinkTopicScanRequest) (SemanticLinkScanStartResult, error)
	Get(context.Context, foundation.ID, foundation.ID) (graphdomain.SemanticLinkScan, error)
}

// SemanticLinkScanCommandService 将公开 Topic 命令收敛为冻结的内部 StartCommand。
type SemanticLinkScanCommandService struct {
	planner SemanticLinkTopicScanPlanner
	scans   *SemanticLinkScanService
}

// NewSemanticLinkScanCommandService 构造公共 Topic scan 编排服务。
func NewSemanticLinkScanCommandService(planner SemanticLinkTopicScanPlanner, scans *SemanticLinkScanService) (*SemanticLinkScanCommandService, error) {
	if nilInterface(planner) || scans == nil {
		return nil, scanUnavailable(errors.New("semantic-link scan planner or service is unavailable"))
	}
	return &SemanticLinkScanCommandService{planner: planner, scans: scans}, nil
}

// StartTopicScan 从正式 Topic 快照生成 scope、计数和冻结版本后创建/重放 Scan。
func (service *SemanticLinkScanCommandService) StartTopicScan(ctx context.Context, request SemanticLinkTopicScanRequest) (SemanticLinkScanStartResult, error) {
	if service == nil || nilInterface(service.planner) || service.scans == nil {
		return SemanticLinkScanStartResult{}, scanUnavailable(errors.New("semantic-link scan command service is unavailable"))
	}
	if ctx == nil || !validID(request.WorkspaceID) || !validID(request.TopicID) {
		return SemanticLinkScanStartResult{}, scanInvalid(errors.New("semantic-link topic scan identity is invalid"))
	}
	plan, err := service.planner.PlanTopicScan(ctx, request.WorkspaceID, request.TopicID)
	if err != nil {
		return SemanticLinkScanStartResult{}, err
	}
	if plan.WorkspaceID != request.WorkspaceID || plan.TopicID != request.TopicID || plan.TopicVersion < 1 || plan.TotalNodes < 0 {
		return SemanticLinkScanStartResult{}, scanConsistency(errors.New("semantic-link topic scan plan is inconsistent"))
	}
	ruleID := SemanticLinkScanRuleID
	return service.scans.StartTopicScan(ctx, SemanticLinkScanStartCommand{
		WorkspaceID: request.WorkspaceID,
		Scope: graphdomain.SemanticLinkScanScope{
			Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(request.TopicID),
			Version: plan.TopicVersion, SchemaVersion: SemanticLinkTopicScanScopeSchemaVersion,
		},
		Generation: graphdomain.SemanticLinkScanGeneration{
			Rule:            graphdomain.SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: SemanticLinkScanRuleVersion},
			WorkflowVersion: SemanticLinkScanWorkflowGenerationVersion,
		},
		TotalNodes: plan.TotalNodes, IdempotencyKey: request.IdempotencyKey,
	})
}

// Get 返回 Workspace-scoped Scan 投影。
func (service *SemanticLinkScanCommandService) Get(ctx context.Context, workspaceID, scanID foundation.ID) (graphdomain.SemanticLinkScan, error) {
	if service == nil || service.scans == nil {
		return graphdomain.SemanticLinkScan{}, scanUnavailable(errors.New("semantic-link scan command service is unavailable"))
	}
	return service.scans.Get(ctx, workspaceID, scanID)
}

var _ SemanticLinkScanHTTPService = (*SemanticLinkScanCommandService)(nil)

// SemanticLinkScanStartRequest 是交给跨 Graph/Workflow UoW 的完整绑定。
// State port 必须在一个持久事务边界内实现 Start/replay，不得先返回假 Workflow ID。
type SemanticLinkScanStartRequest struct {
	WorkspaceID                foundation.ID
	Scope                      graphdomain.SemanticLinkScanScope
	Generation                 graphdomain.SemanticLinkScanGeneration
	Fingerprint                string
	RequestHash                string
	TotalNodes                 int64
	IdempotencyKey             string
	WorkflowDefinitionKey      string
	WorkflowDefinitionVersion  int64
	WorkflowInputSchemaVersion int
}

// SemanticLinkScanStartResult 是首次创建或精确重放后的 Scan 事实。
type SemanticLinkScanStartResult struct {
	Scan      graphdomain.SemanticLinkScan
	Replayed  bool
	StatusURL string
}

// SemanticLinkScanStartPort 是 Graph scan 与 Workflow Runtime 的唯一启动接缝。
// 实现负责创建/重放 Workflow Run、Scan、首节点和幂等绑定；River 不拥有这些业务事实。
type SemanticLinkScanStartPort interface {
	StartOrReplay(context.Context, SemanticLinkScanStartRequest) (SemanticLinkScanStartResult, error)
}

// SemanticLinkScanStatePort 持久化 Scan 查询、页检查点和终态 CAS。
type SemanticLinkScanStatePort interface {
	Get(context.Context, foundation.ID, foundation.ID) (graphdomain.SemanticLinkScan, error)
	AdvancePage(context.Context, graphdomain.SemanticLinkScanProgress) (graphdomain.SemanticLinkScan, error)
	Finish(context.Context, graphdomain.SemanticLinkScanTerminal) (graphdomain.SemanticLinkScan, error)
}

// SemanticLinkScanService 编排异步 Scan 合同，不直接依赖 River/pgx。
type SemanticLinkScanService struct {
	starter SemanticLinkScanStartPort
	state   SemanticLinkScanStatePort
}

// NewSemanticLinkScanService 构造 Scan Application Service。
func NewSemanticLinkScanService(starter SemanticLinkScanStartPort, state SemanticLinkScanStatePort) (*SemanticLinkScanService, error) {
	if nilInterface(starter) || nilInterface(state) {
		return nil, scanUnavailable(errors.New("semantic-link scan state or start port is unavailable"))
	}
	return &SemanticLinkScanService{starter: starter, state: state}, nil
}

// NewSemanticLinkScanStateService 构造仅供 Worker 推进 checkpoint/终态的 Scan Service。
// 该实例不提供启动能力，避免把缺少 Runtime UoW 的 state repository 伪装成可启动依赖。
func NewSemanticLinkScanStateService(state SemanticLinkScanStatePort) (*SemanticLinkScanService, error) {
	if nilInterface(state) {
		return nil, scanUnavailable(errors.New("semantic-link scan state port is unavailable"))
	}
	return &SemanticLinkScanService{state: state}, nil
}

// StartTopicScan 校验 scope/version 后提交异步启动，返回可用于 202 的 status URL。
func (service *SemanticLinkScanService) StartTopicScan(ctx context.Context, command SemanticLinkScanStartCommand) (SemanticLinkScanStartResult, error) {
	if service == nil || nilInterface(service.starter) {
		return SemanticLinkScanStartResult{}, scanUnavailable(errors.New("semantic-link scan starter is unavailable"))
	}
	if ctx == nil {
		return SemanticLinkScanStartResult{}, scanInvalid(errors.New("semantic-link scan context is nil"))
	}
	canonical, request, err := canonicalScanStart(command)
	if err != nil {
		return SemanticLinkScanStartResult{}, err
	}
	result, err := service.starter.StartOrReplay(ctx, request)
	if err != nil {
		return SemanticLinkScanStartResult{}, err
	}
	if err := validateScanStartResult(canonical, request, result); err != nil {
		return SemanticLinkScanStartResult{}, err
	}
	result.StatusURL = "/api/v1/workflows/" + string(result.Scan.WorkflowRunID)
	return result, nil
}

// Get 返回 Workspace-scoped Scan 事实；跨 Workspace 结果统一视为 NotFound。
func (service *SemanticLinkScanService) Get(ctx context.Context, workspaceID, scanID foundation.ID) (graphdomain.SemanticLinkScan, error) {
	if service == nil || nilInterface(service.state) {
		return graphdomain.SemanticLinkScan{}, scanUnavailable(errors.New("semantic-link scan state is unavailable"))
	}
	if ctx == nil {
		return graphdomain.SemanticLinkScan{}, scanInvalid(errors.New("semantic-link scan context is nil"))
	}
	if !validID(workspaceID) || !validID(scanID) {
		return graphdomain.SemanticLinkScan{}, scanInvalid(errors.New("scan identity is invalid"))
	}
	scan, err := service.state.Get(ctx, workspaceID, scanID)
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	if scan.ID != scanID || scan.WorkspaceID != workspaceID {
		return graphdomain.SemanticLinkScan{}, scanNotFound(errors.New("scan is not visible in the requested workspace"))
	}
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	return scan, nil
}

// AdvancePage 以 expected_version 推进一个有界页；失败页不得推进 processed_nodes。
func (service *SemanticLinkScanService) AdvancePage(ctx context.Context, progress graphdomain.SemanticLinkScanProgress) (graphdomain.SemanticLinkScan, error) {
	if service == nil || nilInterface(service.state) {
		return graphdomain.SemanticLinkScan{}, scanUnavailable(errors.New("semantic-link scan state is unavailable"))
	}
	if ctx == nil {
		return graphdomain.SemanticLinkScan{}, scanInvalid(errors.New("semantic-link scan context is nil"))
	}
	if err := validateScanProgress(progress); err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	scan, err := service.state.AdvancePage(ctx, progress)
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	if scan.ID != progress.ScanID || scan.WorkspaceID != progress.WorkspaceID || scan.Version != progress.ExpectedVersion+1 || scan.Status != graphdomain.SemanticLinkScanStatusRunning || !sameScanCheckpoint(scan.Checkpoint, progress.Checkpoint) {
		return graphdomain.SemanticLinkScan{}, scanConsistency(errors.New("scan page state port returned an invalid projection"))
	}
	if scan.ProcessedNodes < progress.ProcessedDelta || scan.CandidateCount < progress.CandidateDelta || scan.SuppressedCount < progress.SuppressedDelta || scan.ReopenedCount < progress.ReopenedDelta || scan.FailedCount < progress.FailedDelta {
		return graphdomain.SemanticLinkScan{}, scanConsistency(errors.New("scan page counters regressed"))
	}
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	return scan, nil
}

// Finish 将 Scan 推进到成功、失败或取消终态。
func (service *SemanticLinkScanService) Finish(ctx context.Context, terminal graphdomain.SemanticLinkScanTerminal) (graphdomain.SemanticLinkScan, error) {
	if service == nil || nilInterface(service.state) {
		return graphdomain.SemanticLinkScan{}, scanUnavailable(errors.New("semantic-link scan state is unavailable"))
	}
	if ctx == nil {
		return graphdomain.SemanticLinkScan{}, scanInvalid(errors.New("semantic-link scan context is nil"))
	}
	if err := validateScanTerminal(terminal); err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	scan, err := service.state.Finish(ctx, terminal)
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	if scan.ID != terminal.ScanID || scan.WorkspaceID != terminal.WorkspaceID || scan.Version != terminal.ExpectedVersion+1 || scan.Status != terminal.Status || !sameScanCompletionTime(scan.CompletedAt, terminal.At) || !sameScanError(scan.LastError, terminal.Error) {
		return graphdomain.SemanticLinkScan{}, scanConsistency(errors.New("scan terminal state port returned an invalid projection"))
	}
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	return scan, nil
}

// SemanticLinkTopicScanPageRequest 是 Workflow executor 的无正文页读取输入。
type SemanticLinkTopicScanPageRequest struct {
	WorkspaceID foundation.ID
	ScanID      foundation.ID
	Scope       graphdomain.SemanticLinkScanScope
	Generation  graphdomain.SemanticLinkScanGeneration
	Cursor      string
	Limit       int
}

// SemanticLinkTopicScanPage 是页读取 Adapter 返回的有界候选 pair。
type SemanticLinkTopicScanPage struct {
	WorkspaceID    foundation.ID
	ScanID         foundation.ID
	ScopeVersion   int64
	Cursor         string
	NextCursor     string
	Complete       bool
	ProcessedNodes int64
	LastNode       *knowledge.NodeRef
	Pairs          []graphdomain.SemanticLinkDiscoveryPair
	Exclusions     []graphdomain.SemanticLinkDiscoveryExclusion
}

// SemanticLinkDiscoveryCandidateWriteRequest 将一页真实节点快照与信号结果交给 Candidate 持久化边界。
type SemanticLinkDiscoveryCandidateWriteRequest struct {
	WorkspaceID foundation.ID
	Generation  graphdomain.SemanticLinkScanGeneration
	Pairs       []graphdomain.SemanticLinkDiscoveryPair
	Discovery   graphdomain.SemanticLinkDiscoveryResult
}

// SemanticLinkDiscoveryCandidateWriteResult 是一页 Candidate upsert 的持久计数。
type SemanticLinkDiscoveryCandidateWriteResult struct {
	Created    int64
	Suppressed int64
	Reopened   int64
	Failed     int64
}

// SemanticLinkDiscoveryCandidateWriter 将 discovery hit 转为可审阅 Candidate；不得写正式 Relation。
type SemanticLinkDiscoveryCandidateWriter interface {
	PersistDiscoveryCandidates(context.Context, SemanticLinkDiscoveryCandidateWriteRequest) (SemanticLinkDiscoveryCandidateWriteResult, error)
}

// SemanticLinkTopicScanPageSource 批量加载 Topic scope 的节点 pair。
// 实现必须按页返回，不能让 executor 逐节点查询。
type SemanticLinkTopicScanPageSource interface {
	LoadPage(context.Context, SemanticLinkTopicScanPageRequest) (SemanticLinkTopicScanPage, error)
}

// SemanticLinkTopicScanPageResult 是 executor 一页的确定性结果，供 state port CAS 提交。
type SemanticLinkTopicScanPageResult struct {
	Discovery      graphdomain.SemanticLinkDiscoveryResult
	NextCursor     string
	Complete       bool
	ProcessedNodes int64
	LastNode       *knowledge.NodeRef
	Candidates     SemanticLinkDiscoveryCandidateWriteResult
}

// SemanticLinkTopicScanExecutor 执行单个 bounded page；不拥有 Scan 状态或 River lease。
type SemanticLinkTopicScanExecutor struct {
	source    SemanticLinkTopicScanPageSource
	discovery *SemanticLinkDiscoveryService
	writer    SemanticLinkDiscoveryCandidateWriter
	limit     int
	perSource int
}

// NewSemanticLinkTopicScanExecutor 构造可被 River Executor 调用的纯 page worker。
func NewSemanticLinkTopicScanExecutor(source SemanticLinkTopicScanPageSource, discovery *SemanticLinkDiscoveryService, writers ...SemanticLinkDiscoveryCandidateWriter) (*SemanticLinkTopicScanExecutor, error) {
	if nilInterface(source) || discovery == nil {
		return nil, scanUnavailable(errors.New("topic scan page dependencies are unavailable"))
	}
	executor := &SemanticLinkTopicScanExecutor{source: source, discovery: discovery, limit: graphdomain.MaxSemanticLinkDiscoveryLimit, perSource: graphdomain.MaxSemanticLinkDiscoveryPerSource}
	if len(writers) > 1 {
		return nil, scanInvalid(errors.New("topic scan accepts at most one candidate writer"))
	}
	if len(writers) == 1 && !nilInterface(writers[0]) {
		executor.writer = writers[0]
	}
	return executor, nil
}

// ProcessPage 读取并评估单页；空页返回明确的零命中结果，不伪装成候选成功。
func (executor *SemanticLinkTopicScanExecutor) ProcessPage(ctx context.Context, request SemanticLinkTopicScanPageRequest) (SemanticLinkTopicScanPageResult, error) {
	if executor == nil || nilInterface(executor.source) || executor.discovery == nil {
		return SemanticLinkTopicScanPageResult{}, scanUnavailable(errors.New("topic scan executor is unavailable"))
	}
	if ctx == nil {
		return SemanticLinkTopicScanPageResult{}, scanInvalid(errors.New("topic scan context is nil"))
	}
	if err := validateTopicScanPageRequest(request); err != nil {
		return SemanticLinkTopicScanPageResult{}, err
	}
	page, err := executor.source.LoadPage(ctx, request)
	if err != nil {
		return SemanticLinkTopicScanPageResult{}, err
	}
	if err := validateTopicScanPage(request, page); err != nil {
		return SemanticLinkTopicScanPageResult{}, err
	}
	discovery, err := executor.discovery.Discover(ctx, SemanticLinkDiscoveryCommand{
		WorkspaceID: request.WorkspaceID, Pairs: page.Pairs, Exclusions: page.Exclusions,
		Limit: executor.limit, PerSourceLimit: executor.perSource, RuleGeneration: request.Generation.Rule,
	})
	if err != nil {
		return SemanticLinkTopicScanPageResult{}, err
	}
	if err := validateScanDiscoveryGenerations(discovery, request.Generation); err != nil {
		return SemanticLinkTopicScanPageResult{}, err
	}
	writeResult := SemanticLinkDiscoveryCandidateWriteResult{}
	if executor.writer != nil {
		writeResult, err = executor.writer.PersistDiscoveryCandidates(ctx, SemanticLinkDiscoveryCandidateWriteRequest{
			WorkspaceID: request.WorkspaceID, Generation: request.Generation,
			Pairs: append([]graphdomain.SemanticLinkDiscoveryPair(nil), page.Pairs...), Discovery: discovery,
		})
		if err != nil {
			return SemanticLinkTopicScanPageResult{}, err
		}
		if writeResult.Created < 0 || writeResult.Suppressed < 0 || writeResult.Reopened < 0 || writeResult.Failed < 0 || writeResult.Created+writeResult.Suppressed+writeResult.Reopened+writeResult.Failed > int64(len(discovery.Items)) {
			return SemanticLinkTopicScanPageResult{}, scanConsistency(errors.New("candidate writer returned invalid counters"))
		}
	}
	return SemanticLinkTopicScanPageResult{
		Discovery: discovery, NextCursor: page.NextCursor, Complete: page.Complete,
		ProcessedNodes: page.ProcessedNodes, LastNode: cloneScanNodeRef(page.LastNode), Candidates: writeResult,
	}, nil
}

func canonicalScanStart(command SemanticLinkScanStartCommand) (SemanticLinkScanStartCommand, SemanticLinkScanStartRequest, error) {
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if !validID(command.WorkspaceID) || command.IdempotencyKey == "" || len(command.IdempotencyKey) > maxSemanticLinkScanIdempotencyKey || strings.ContainsAny(command.IdempotencyKey, "\r\n") || command.TotalNodes < 0 {
		return SemanticLinkScanStartCommand{}, SemanticLinkScanStartRequest{}, scanInvalid(errors.New("scan start command is invalid"))
	}
	if err := graphdomain.ValidateSemanticLinkScanScope(command.Scope); err != nil {
		return SemanticLinkScanStartCommand{}, SemanticLinkScanStartRequest{}, err
	}
	if err := graphdomain.ValidateSemanticLinkScanGeneration(command.Generation); err != nil {
		return SemanticLinkScanStartCommand{}, SemanticLinkScanStartRequest{}, err
	}
	fingerprint, err := graphdomain.ComputeSemanticLinkScanFingerprint(command.WorkspaceID, command.Scope, command.Generation)
	if err != nil {
		return SemanticLinkScanStartCommand{}, SemanticLinkScanStartRequest{}, err
	}
	requestHash, err := semanticLinkScanRequestHash(command.WorkspaceID, command.Scope, fingerprint, command.Generation)
	if err != nil {
		return SemanticLinkScanStartCommand{}, SemanticLinkScanStartRequest{}, err
	}
	return command, SemanticLinkScanStartRequest{
		WorkspaceID: command.WorkspaceID, Scope: command.Scope, Generation: command.Generation,
		Fingerprint: fingerprint, RequestHash: requestHash, TotalNodes: command.TotalNodes,
		IdempotencyKey:             command.IdempotencyKey,
		WorkflowDefinitionKey:      SemanticLinkScanWorkflowDefinitionKey,
		WorkflowDefinitionVersion:  SemanticLinkScanWorkflowDefinitionVersion,
		WorkflowInputSchemaVersion: SemanticLinkScanInputSchemaVersion,
	}, nil
}

func validateScanStartResult(command SemanticLinkScanStartCommand, request SemanticLinkScanStartRequest, result SemanticLinkScanStartResult) error {
	scan := result.Scan
	if result.StatusURL != "" || scan.WorkspaceID != command.WorkspaceID || scan.Scope != command.Scope || scan.Fingerprint != request.Fingerprint || scan.RequestHash != request.RequestHash || scan.IdempotencyKey != command.IdempotencyKey || scan.TotalNodes != command.TotalNodes || !validID(scan.ID) || !validID(scan.WorkflowRunID) {
		return scanConsistency(errors.New("scan start result is not bound to the request"))
	}
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil {
		return err
	}
	return nil
}

func semanticLinkScanRequestHash(workspaceID foundation.ID, scope graphdomain.SemanticLinkScanScope, fingerprint string, generation graphdomain.SemanticLinkScanGeneration) (string, error) {
	payload := struct {
		Schema      string                                 `json:"schema"`
		WorkspaceID foundation.ID                          `json:"workspace_id"`
		Scope       graphdomain.SemanticLinkScanScope      `json:"scope"`
		Fingerprint string                                 `json:"fingerprint"`
		Generation  graphdomain.SemanticLinkScanGeneration `json:"generation"`
	}{"semantic-link-scan-request/v1", workspaceID, scope, fingerprint, generation}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", scanConsistency(err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateScanProgress(progress graphdomain.SemanticLinkScanProgress) error {
	if !validID(progress.WorkspaceID) || !validID(progress.ScanID) || progress.ExpectedVersion < 1 || progress.ProcessedDelta < 0 || progress.CandidateDelta < 0 || progress.SuppressedDelta < 0 || progress.ReopenedDelta < 0 || progress.FailedDelta < 0 || progress.Checkpoint.ProcessedPage < 1 || len(progress.Checkpoint.Cursor) > 512 {
		return scanInvalid(errors.New("scan page progress is invalid"))
	}
	if progress.Checkpoint.LastNode != nil && !validNodeRef(*progress.Checkpoint.LastNode) {
		return scanInvalid(errors.New("scan checkpoint node is invalid"))
	}
	return nil
}

func sameScanCheckpoint(left, right graphdomain.SemanticLinkScanCheckpoint) bool {
	if left.Cursor != right.Cursor || left.ProcessedPage != right.ProcessedPage {
		return false
	}
	if left.LastNode == nil || right.LastNode == nil {
		return left.LastNode == nil && right.LastNode == nil
	}
	return *left.LastNode == *right.LastNode
}

func sameScanError(left, right *graphdomain.SemanticLinkScanError) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameScanCompletionTime(stored *time.Time, requested time.Time) bool {
	if stored == nil {
		return false
	}
	delta := stored.UTC().Sub(requested.UTC())
	if delta < 0 {
		delta = -delta
	}
	return delta < time.Microsecond
}

func validateScanTerminal(terminal graphdomain.SemanticLinkScanTerminal) error {
	if !validID(terminal.WorkspaceID) || !validID(terminal.ScanID) || terminal.ExpectedVersion < 1 || terminal.At.IsZero() || (terminal.Status != graphdomain.SemanticLinkScanStatusSucceeded && terminal.Status != graphdomain.SemanticLinkScanStatusFailed && terminal.Status != graphdomain.SemanticLinkScanStatusCancelled) {
		return scanInvalid(errors.New("scan terminal command is invalid"))
	}
	if terminal.Status == graphdomain.SemanticLinkScanStatusFailed {
		if terminal.Error == nil || strings.TrimSpace(terminal.Error.Stage) == "" || strings.TrimSpace(terminal.Error.Code) == "" {
			return scanInvalid(errors.New("failed scan requires a stable error summary"))
		}
	} else if terminal.Error != nil {
		return scanInvalid(errors.New("successful or cancelled scan must not carry a failure summary"))
	}
	return nil
}

func validateTopicScanPageRequest(request SemanticLinkTopicScanPageRequest) error {
	if !validID(request.WorkspaceID) || !validID(request.ScanID) || request.Limit < 1 || request.Limit > MaxSemanticLinkScanPageNodes || len(request.Cursor) > 512 {
		return scanInvalid(errors.New("topic scan page request is invalid"))
	}
	if err := graphdomain.ValidateSemanticLinkScanScope(request.Scope); err != nil {
		return err
	}
	if err := graphdomain.ValidateSemanticLinkScanGeneration(request.Generation); err != nil {
		return err
	}
	if request.Scope.Type != graphdomain.SemanticLinkScanScopeTopic && request.Scope.Type != graphdomain.SemanticLinkScanScopeNode {
		return scanInvalid(errors.New("topic scan page scope is unsupported"))
	}
	return nil
}

func validateScanDiscoveryGenerations(result graphdomain.SemanticLinkDiscoveryResult, expected graphdomain.SemanticLinkScanGeneration) error {
	if len(result.Signals) != graphdomain.MaxSemanticLinkDiscoveryMethodCount {
		return scanConsistency(errors.New("scan discovery result does not report all signal capabilities"))
	}
	seen := make(map[graphdomain.SemanticLinkDiscoveryMethod]struct{}, len(result.Signals))
	for _, signal := range result.Signals {
		if _, duplicate := seen[signal.Method]; duplicate {
			return scanConsistency(errors.New("scan discovery result contains duplicate signal reports"))
		}
		seen[signal.Method] = struct{}{}
		switch signal.Method {
		case graphdomain.SemanticLinkDiscoveryMethodTitleAlias,
			graphdomain.SemanticLinkDiscoveryMethodTermMatch,
			graphdomain.SemanticLinkDiscoveryMethodCommonTopic,
			graphdomain.SemanticLinkDiscoveryMethodSharedSource:
			if signal.Status != graphdomain.SemanticLinkDiscoverySignalStatusExecuted || !graphdomain.EqualSemanticLinkCandidateGeneration(signal.Generation, expected.Rule) {
				return scanConsistency(errors.New("scan deterministic signal generation drifted"))
			}
		case graphdomain.SemanticLinkDiscoveryMethodClaimSemanticSimilarity:
			if !externalSignalGenerationMatches(signal, expected.Semantic) {
				return scanConsistency(errors.New("scan semantic signal generation drifted"))
			}
		case graphdomain.SemanticLinkDiscoveryMethodRAGCoRetrieval:
			if !externalSignalGenerationMatches(signal, expected.RAG) {
				return scanConsistency(errors.New("scan rag signal generation drifted"))
			}
		default:
			return scanConsistency(errors.New("scan discovery result contains an unknown signal"))
		}
	}
	return nil
}

func externalSignalGenerationMatches(report graphdomain.SemanticLinkDiscoverySignalReport, expected *graphdomain.SemanticLinkCandidateGeneration) bool {
	if expected == nil {
		return report.Status == graphdomain.SemanticLinkDiscoverySignalStatusUnsupported && graphdomain.EqualSemanticLinkCandidateGeneration(report.Generation, graphdomain.SemanticLinkCandidateGeneration{}) && report.Reason != ""
	}
	return report.Status == graphdomain.SemanticLinkDiscoverySignalStatusExecuted && graphdomain.EqualSemanticLinkCandidateGeneration(report.Generation, *expected) && report.Reason == ""
}

func validateTopicScanPage(request SemanticLinkTopicScanPageRequest, page SemanticLinkTopicScanPage) error {
	if page.WorkspaceID != request.WorkspaceID || page.ScanID != request.ScanID || page.ScopeVersion != request.Scope.Version || page.Cursor != request.Cursor || len(page.NextCursor) > 512 || (!page.Complete && strings.TrimSpace(page.NextCursor) == "") || (page.Complete && page.NextCursor != "") || page.ProcessedNodes < 0 || page.ProcessedNodes > MaxSemanticLinkScanPageNodes || len(page.Pairs) > int(page.ProcessedNodes)*MaxSemanticLinkScanPagePairsPerNode || (page.ProcessedNodes == 0) != (page.LastNode == nil) || (page.LastNode != nil && !validNodeRef(*page.LastNode)) {
		return scanConsistency(errors.New("topic scan page is not bounded or request-bound"))
	}
	for _, pair := range page.Pairs {
		if err := graphdomain.ValidateSemanticLinkDiscoveryNode(request.WorkspaceID, pair.Source); err != nil {
			return err
		}
		if err := graphdomain.ValidateSemanticLinkDiscoveryNode(request.WorkspaceID, pair.Target); err != nil {
			return err
		}
	}
	return nil
}

func cloneScanNodeRef(value *knowledge.NodeRef) *knowledge.NodeRef {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func scanInvalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeSemanticLinkScanInvalid, false, err)
}

func scanConsistency(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeSemanticLinkScanInvalid, false, err)
}

func scanUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeSemanticLinkDiscoveryUnavailable, false, err)
}

func scanNotFound(err error) error {
	return foundation.NewError(foundation.ErrorNotFound, graphdomain.ErrorCodeSemanticLinkScanNotFound, false, err)
}
