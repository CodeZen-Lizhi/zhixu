package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

const (
	// HealthScanWorkflowDefinitionKey 是知识健康扫描的服务端 Workflow Definition 键。
	HealthScanWorkflowDefinitionKey = "health.knowledge-scan"
	// HealthScanWorkflowDefinitionVersion 是首版知识健康 Workflow 图版本。
	HealthScanWorkflowDefinitionVersion int64 = 1
	// HealthScanInputSchemaVersion 是知识健康 Workflow 输入版本。
	HealthScanInputSchemaVersion = 1
	// HealthScanOutputSchemaVersion 是知识健康 Workflow 输出版本。
	HealthScanOutputSchemaVersion = 1
	// HealthScanNodeKey 是知识健康 Workflow 的唯一 root 节点键。
	HealthScanNodeKey = "health-scan"
	// HealthScanNodeKind 是 Worker Executor Registry 的稳定节点类型。
	HealthScanNodeKind = "health.knowledge_scan"

	maxHealthScanIdempotencyKey = 128
)

// ScanStartCommand 是创建或精确重放一次 durable Health Scan 的命令。
type ScanStartCommand struct {
	WorkspaceID    foundation.ID
	Scope          domain.ScanScope
	Coverage       []domain.DetectorCoverage
	MaxItems       int64
	IdempotencyKey string
	// PreventScopeConcurrency 要求本次 schedule start 与同 scope 任一 active scan 互斥。
	PreventScopeConcurrency bool
	// BindCurrentReadModel 仅供持久 schedule 在 due 时把当前 Collection revision 冻结进新 Scan。
	BindCurrentReadModel bool
}

// ScanStartRequest 是交给 Health/Workflow PostgreSQL UoW 的完整不可变绑定。
type ScanStartRequest struct {
	WorkspaceID                foundation.ID
	Scope                      domain.ScanScope
	Coverage                   []domain.DetectorCoverage
	Fingerprint                string
	RequestHash                string
	MaxItems                   int64
	IdempotencyKey             string
	WorkflowDefinitionKey      string
	WorkflowDefinitionVersion  int64
	WorkflowInputSchemaVersion int
	PreventScopeConcurrency    bool
}

// ScanStartResult 是首次创建或精确重放后的持久 Scan 事实。
type ScanStartResult struct {
	Scan      domain.Scan
	Replayed  bool
	StatusURL string
}

// ScanProgress 以 detector 为单位推进 checkpoint、coverage 状态与计数。
type ScanProgress struct {
	ScanID            foundation.ID
	WorkspaceID       foundation.ID
	ExpectedVersion   int64
	DetectorID        string
	Status            domain.DetectorCoverageStatus
	Checkpoint        domain.ScanCheckpoint
	CountersDelta     domain.ScanCounters
	LastError         *domain.FailureSummary
	UnavailableReason string
}

// ScanTerminal 将 Scan 推进为一个持久终态。
type ScanTerminal struct {
	ScanID          foundation.ID
	WorkspaceID     foundation.ID
	ExpectedVersion int64
	Status          domain.ScanStatus
	LastError       *domain.FailureSummary
}

// ScanStartPort 原子创建/重放 Workflow Runtime、River Job、Scan 与 detector coverage。
type ScanStartPort interface {
	StartOrReplay(context.Context, ScanStartRequest) (ScanStartResult, error)
}

// ScanStatePort 提供 Workspace-scoped 查询与 CAS 状态推进。
type ScanStatePort interface {
	Get(context.Context, foundation.ID, foundation.ID) (domain.Scan, error)
	GetByWorkflowRun(context.Context, foundation.ID, foundation.ID) (domain.Scan, error)
	Advance(context.Context, ScanProgress) (domain.Scan, error)
	Finish(context.Context, ScanTerminal) (domain.Scan, error)
}

// ScanService 编排 Health Scan durable runtime，不依赖 pgx 或 River 类型。
type ScanService struct {
	starter    ScanStartPort
	state      ScanStatePort
	registry   *Registry
	membership SmartCollectionMembershipPort
}

// NewScanService 构造可启动且可推进的 Health Scan 服务。
func NewScanService(starter ScanStartPort, state ScanStatePort) (*ScanService, error) {
	if nilScanDependency(starter) || nilScanDependency(state) {
		return nil, scanUnavailable(errors.New("health scan start or state port is unavailable"))
	}
	return &ScanService{starter: starter, state: state}, nil
}

// NewSmartCollectionScanService 构造会在 start 前绑定 Collection version/query/revision 并生成真实 coverage 的服务。
func NewSmartCollectionScanService(starter ScanStartPort, state ScanStatePort, registry *Registry, membership SmartCollectionMembershipPort) (*ScanService, error) {
	service, err := NewScanService(starter, state)
	if err != nil {
		return nil, err
	}
	if registry == nil || nilScanDependency(membership) {
		return nil, scanUnavailable(errors.New("smart-collection scan registry or membership is unavailable"))
	}
	service.registry = registry
	service.membership = membership
	return service, nil
}

// NewScanStateService 构造仅供 Worker 推进 checkpoint/终态的服务。
func NewScanStateService(state ScanStatePort) (*ScanService, error) {
	if nilScanDependency(state) {
		return nil, scanUnavailable(errors.New("health scan state port is unavailable"))
	}
	return &ScanService{state: state}, nil
}

// Start 创建或精确重放一次 Health Scan。
func (service *ScanService) Start(ctx context.Context, command ScanStartCommand) (ScanStartResult, error) {
	if service == nil || nilScanDependency(service.starter) {
		return ScanStartResult{}, scanUnavailable(errors.New("health scan starter is unavailable"))
	}
	if ctx == nil {
		return ScanStartResult{}, scanInvalid(errors.New("health scan context is nil"))
	}
	if command.Scope.Type == domain.ScanScopeTypeSmartCollection {
		var err error
		command, err = service.prepareSmartCollectionStart(ctx, command)
		if err != nil {
			return ScanStartResult{}, err
		}
	}
	request, err := canonicalScanStart(command)
	if err != nil {
		return ScanStartResult{}, err
	}
	result, err := service.starter.StartOrReplay(ctx, request)
	if err != nil {
		return ScanStartResult{}, err
	}
	if err := validateStartResult(request, result); err != nil {
		return ScanStartResult{}, err
	}
	result.StatusURL = HealthScanStatusURL(result.Scan.WorkspaceID, result.Scan.ID)
	return result, nil
}

func (service *ScanService) prepareSmartCollectionStart(ctx context.Context, command ScanStartCommand) (ScanStartCommand, error) {
	if service.registry == nil || nilScanDependency(service.membership) {
		return ScanStartCommand{}, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeScanScopeUnavailable, false, errors.New("smart-collection membership is unavailable"))
	}
	if !validScanID(command.WorkspaceID) || !validScanID(command.Scope.Ref) {
		return ScanStartCommand{}, scanInvalid(errors.New("smart-collection scan identity is invalid"))
	}
	if command.BindCurrentReadModel {
		if err := domain.ValidateScheduleScope(command.Scope); err != nil {
			return ScanStartCommand{}, err
		}
	} else if err := domain.ValidateScanScope(command.Scope); err != nil {
		return ScanStartCommand{}, err
	}
	binding, err := service.membership.Plan(ctx, command.WorkspaceID, command.Scope.Ref)
	if err != nil {
		return ScanStartCommand{}, err
	}
	if err := validateSmartCollectionBinding(binding); err != nil {
		return ScanStartCommand{}, err
	}
	if binding.WorkspaceID != command.WorkspaceID || binding.CollectionID != command.Scope.Ref ||
		binding.CollectionVersion != command.Scope.Version || binding.QueryHash != command.Scope.Hash {
		return ScanStartCommand{}, scanScopeStale(errors.New("smart-collection scope binding changed before scan start"))
	}
	if command.BindCurrentReadModel {
		command.Scope.ReadModelRevision = binding.ReadModelRevision
		command.Scope.ExactCount = binding.ExactCount
	} else if binding.ReadModelRevision != command.Scope.ReadModelRevision || binding.ExactCount != command.Scope.ExactCount {
		return ScanStartCommand{}, scanScopeStale(errors.New("smart-collection read-model revision changed before scan start"))
	}
	if binding.ExactCount > command.MaxItems {
		return ScanStartCommand{}, scanInvalid(errors.New("smart-collection exact count exceeds scan max_items"))
	}
	command.BindCurrentReadModel = false
	command.Coverage = service.registry.Coverage(Scope{
		WorkspaceID: command.WorkspaceID, Type: command.Scope.Type, Ref: command.Scope.Ref,
		Version: command.Scope.Version, Hash: command.Scope.Hash, ReadModelRevision: command.Scope.ReadModelRevision,
		ExactCount: command.Scope.ExactCount,
	})
	return command, nil
}

// HealthScanStatusURL 返回公共 API 可恢复的 Scan 资源地址。
func HealthScanStatusURL(workspaceID, scanID foundation.ID) string {
	return "/api/v1/health/scans/" + string(scanID) + "?workspace_id=" + string(workspaceID)
}

// Get 返回 Workspace-scoped Health Scan 事实。
func (service *ScanService) Get(ctx context.Context, workspaceID, scanID foundation.ID) (domain.Scan, error) {
	if service == nil || nilScanDependency(service.state) {
		return domain.Scan{}, scanUnavailable(errors.New("health scan state is unavailable"))
	}
	if ctx == nil || !validScanID(workspaceID) || !validScanID(scanID) {
		return domain.Scan{}, scanInvalid(errors.New("health scan lookup is invalid"))
	}
	scan, err := service.state.Get(ctx, workspaceID, scanID)
	if err != nil {
		return domain.Scan{}, err
	}
	if scan.ID != scanID || scan.WorkspaceID != workspaceID {
		return domain.Scan{}, scanNotFound(errors.New("health scan is not visible in the requested workspace"))
	}
	if err := domain.ValidateScan(scan); err != nil {
		return domain.Scan{}, err
	}
	return scan, nil
}

// GetByWorkflowRun 返回 Worker delivery 绑定的唯一 Health Scan。
func (service *ScanService) GetByWorkflowRun(ctx context.Context, workspaceID, workflowRunID foundation.ID) (domain.Scan, error) {
	if service == nil || nilScanDependency(service.state) {
		return domain.Scan{}, scanUnavailable(errors.New("health scan state is unavailable"))
	}
	if ctx == nil || !validScanID(workspaceID) || !validScanID(workflowRunID) {
		return domain.Scan{}, scanInvalid(errors.New("health scan workflow lookup is invalid"))
	}
	scan, err := service.state.GetByWorkflowRun(ctx, workspaceID, workflowRunID)
	if err != nil {
		return domain.Scan{}, err
	}
	if scan.WorkspaceID != workspaceID || scan.WorkflowRunID != workflowRunID {
		return domain.Scan{}, scanNotFound(errors.New("health scan workflow binding is not visible"))
	}
	if err := domain.ValidateScan(scan); err != nil {
		return domain.Scan{}, err
	}
	return scan, nil
}

// Advance 使用 expected version 原子推进一个 detector 的 bounded checkpoint。
func (service *ScanService) Advance(ctx context.Context, progress ScanProgress) (domain.Scan, error) {
	if service == nil || nilScanDependency(service.state) {
		return domain.Scan{}, scanUnavailable(errors.New("health scan state is unavailable"))
	}
	if ctx == nil {
		return domain.Scan{}, scanInvalid(errors.New("health scan context is nil"))
	}
	if err := validateProgress(progress); err != nil {
		return domain.Scan{}, err
	}
	scan, err := service.state.Advance(ctx, progress)
	if err != nil {
		return domain.Scan{}, err
	}
	if scan.ID != progress.ScanID || scan.WorkspaceID != progress.WorkspaceID || scan.Version != progress.ExpectedVersion+1 || scan.Status != domain.ScanStatusRunning {
		return domain.Scan{}, scanConsistency(errors.New("health scan state port returned an invalid progress projection"))
	}
	coverage, found := coverageByID(scan.Coverage, progress.DetectorID)
	if !found || coverage.Status != progress.Status || !sameCheckpoint(coverage.Checkpoint, progress.Checkpoint) {
		return domain.Scan{}, scanConsistency(errors.New("health scan detector coverage did not match progress"))
	}
	if err := domain.ValidateScan(scan); err != nil {
		return domain.Scan{}, err
	}
	return scan, nil
}

// Finish 使用 expected version 把 Scan 推进到成功、部分、失败或取消终态。
func (service *ScanService) Finish(ctx context.Context, terminal ScanTerminal) (domain.Scan, error) {
	if service == nil || nilScanDependency(service.state) {
		return domain.Scan{}, scanUnavailable(errors.New("health scan state is unavailable"))
	}
	if ctx == nil {
		return domain.Scan{}, scanInvalid(errors.New("health scan context is nil"))
	}
	if err := validateTerminal(terminal); err != nil {
		return domain.Scan{}, err
	}
	scan, err := service.state.Finish(ctx, terminal)
	if err != nil {
		return domain.Scan{}, err
	}
	if scan.ID != terminal.ScanID || scan.WorkspaceID != terminal.WorkspaceID || scan.Version != terminal.ExpectedVersion+1 || scan.Status != terminal.Status || !sameFailure(scan.LastError, terminal.LastError) {
		return domain.Scan{}, scanConsistency(errors.New("health scan state port returned an invalid terminal projection"))
	}
	if err := domain.ValidateScan(scan); err != nil {
		return domain.Scan{}, err
	}
	return scan, nil
}

func canonicalScanStart(command ScanStartCommand) (ScanStartRequest, error) {
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if !validScanID(command.WorkspaceID) || command.MaxItems < 1 || command.MaxItems > domain.MaxScanItems || command.IdempotencyKey == "" || len(command.IdempotencyKey) > maxHealthScanIdempotencyKey || strings.ContainsAny(command.IdempotencyKey, "\r\n") {
		return ScanStartRequest{}, scanInvalid(errors.New("health scan start command is invalid"))
	}
	if err := domain.ValidateScanScope(command.Scope); err != nil {
		return ScanStartRequest{}, err
	}
	if command.Scope.Type == domain.ScanScopeTypeWorkspace && command.Scope.Ref != command.WorkspaceID {
		return ScanStartRequest{}, scanInvalid(errors.New("workspace health scan must bind its workspace scope"))
	}
	coverage, err := canonicalStartCoverage(command.Coverage)
	if err != nil {
		return ScanStartRequest{}, err
	}
	fingerprint, err := computeScanHash("health-scan-fingerprint/v1", command.WorkspaceID, command.Scope, coverage, command.MaxItems, command.PreventScopeConcurrency)
	if err != nil {
		return ScanStartRequest{}, err
	}
	requestHash, err := computeScanHash("health-scan-request/v1", command.WorkspaceID, command.Scope, coverage, command.MaxItems, command.PreventScopeConcurrency)
	if err != nil {
		return ScanStartRequest{}, err
	}
	return ScanStartRequest{
		WorkspaceID: command.WorkspaceID, Scope: command.Scope, Coverage: coverage,
		Fingerprint: fingerprint, RequestHash: requestHash, MaxItems: command.MaxItems,
		IdempotencyKey: command.IdempotencyKey, WorkflowDefinitionKey: HealthScanWorkflowDefinitionKey,
		WorkflowDefinitionVersion: HealthScanWorkflowDefinitionVersion, WorkflowInputSchemaVersion: HealthScanInputSchemaVersion,
		PreventScopeConcurrency: command.PreventScopeConcurrency,
	}, nil
}

func canonicalStartCoverage(values []domain.DetectorCoverage) ([]domain.DetectorCoverage, error) {
	if len(values) == 0 {
		return nil, scanInvalid(errors.New("health scan requires detector coverage"))
	}
	result := append([]domain.DetectorCoverage(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].DetectorID < result[j].DetectorID })
	seen := make(map[string]struct{}, len(result))
	for _, item := range result {
		if strings.TrimSpace(item.DetectorID) != item.DetectorID || item.DetectorID == "" || strings.TrimSpace(item.DetectorVersion) != item.DetectorVersion || item.DetectorVersion == "" {
			return nil, scanInvalid(errors.New("health scan detector identity is invalid"))
		}
		if _, exists := seen[item.DetectorID]; exists {
			return nil, scanInvalid(errors.New("health scan detector coverage is duplicated"))
		}
		seen[item.DetectorID] = struct{}{}
		if item.Status != domain.DetectorCoverageStatusPending && item.Status != domain.DetectorCoverageStatusUnavailable {
			return nil, scanInvalid(errors.New("health scan coverage must start pending or unavailable"))
		}
		if item.Checkpoint != (domain.ScanCheckpoint{}) || item.Counters != (domain.ScanCounters{}) || item.LastError != nil {
			return nil, scanInvalid(errors.New("health scan initial coverage contains progress"))
		}
		if item.Status == domain.DetectorCoverageStatusUnavailable {
			if strings.TrimSpace(item.UnavailableReason) == "" || strings.TrimSpace(item.UnavailableReason) != item.UnavailableReason {
				return nil, scanInvalid(errors.New("unavailable detector requires a canonical reason"))
			}
		} else if item.UnavailableReason != "" {
			return nil, scanInvalid(errors.New("pending detector cannot carry an unavailable reason"))
		}
	}
	return result, nil
}

func computeScanHash(schema string, workspaceID foundation.ID, scope domain.ScanScope, coverage []domain.DetectorCoverage, maxItems int64, preventScopeConcurrency bool) (string, error) {
	payload := struct {
		Schema                  string                    `json:"schema"`
		WorkspaceID             foundation.ID             `json:"workspace_id"`
		Scope                   domain.ScanScope          `json:"scope"`
		Coverage                []domain.DetectorCoverage `json:"coverage"`
		MaxItems                int64                     `json:"max_items"`
		Workflow                int64                     `json:"workflow_version"`
		PreventScopeConcurrency bool                      `json:"prevent_scope_concurrency"`
	}{schema, workspaceID, scope, coverage, maxItems, HealthScanWorkflowDefinitionVersion, preventScopeConcurrency}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", scanConsistency(err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateStartResult(request ScanStartRequest, result ScanStartResult) error {
	scan := result.Scan
	if result.StatusURL != "" || scan.WorkspaceID != request.WorkspaceID || scan.Scope != request.Scope || scan.Fingerprint != request.Fingerprint || scan.RequestHash != request.RequestHash || scan.IdempotencyKey != request.IdempotencyKey || scan.MaxItems != request.MaxItems || !validScanID(scan.ID) || !validScanID(scan.WorkflowRunID) || !sameCoverageBinding(scan.Coverage, request.Coverage) {
		return scanConsistency(errors.New("health scan start result is not bound to the request"))
	}
	return domain.ValidateScan(scan)
}

func validateProgress(progress ScanProgress) error {
	if !validScanID(progress.ScanID) || !validScanID(progress.WorkspaceID) || progress.ExpectedVersion < 1 || strings.TrimSpace(progress.DetectorID) != progress.DetectorID || progress.DetectorID == "" || progress.Checkpoint.Page < 0 || len(progress.Checkpoint.Cursor) > 512 {
		return scanInvalid(errors.New("health scan progress is invalid"))
	}
	if progress.CountersDelta.Processed < 0 || progress.CountersDelta.Created < 0 || progress.CountersDelta.Reopened < 0 || progress.CountersDelta.Resolved < 0 || progress.CountersDelta.Unchanged < 0 || progress.CountersDelta.Failed < 0 {
		return scanInvalid(errors.New("health scan progress counters must be non-negative"))
	}
	switch progress.Status {
	case domain.DetectorCoverageStatusRunning, domain.DetectorCoverageStatusSucceeded, domain.DetectorCoverageStatusCancelled:
		if progress.LastError != nil || progress.UnavailableReason != "" {
			return scanInvalid(errors.New("detector status does not accept failure metadata"))
		}
	case domain.DetectorCoverageStatusPartial, domain.DetectorCoverageStatusFailed:
		if progress.LastError == nil || strings.TrimSpace(progress.LastError.Stage) == "" || strings.TrimSpace(progress.LastError.Code) == "" || progress.UnavailableReason != "" {
			return scanInvalid(errors.New("failed detector progress requires a stable error"))
		}
	case domain.DetectorCoverageStatusUnavailable:
		if progress.LastError != nil || strings.TrimSpace(progress.UnavailableReason) == "" || strings.TrimSpace(progress.UnavailableReason) != progress.UnavailableReason || progress.CountersDelta != (domain.ScanCounters{}) {
			return scanInvalid(errors.New("unavailable detector progress is invalid"))
		}
	default:
		return scanInvalid(errors.New("detector progress status is invalid"))
	}
	return nil
}

func validateTerminal(terminal ScanTerminal) error {
	if !validScanID(terminal.ScanID) || !validScanID(terminal.WorkspaceID) || terminal.ExpectedVersion < 1 {
		return scanInvalid(errors.New("health scan terminal command is invalid"))
	}
	switch terminal.Status {
	case domain.ScanStatusSucceeded, domain.ScanStatusCancelled:
		if terminal.LastError != nil {
			return scanInvalid(errors.New("successful or cancelled scan cannot carry an error"))
		}
	case domain.ScanStatusPartial:
		if terminal.LastError != nil && (strings.TrimSpace(terminal.LastError.Stage) == "" || strings.TrimSpace(terminal.LastError.Code) == "") {
			return scanInvalid(errors.New("partial scan error summary is invalid"))
		}
	case domain.ScanStatusFailed:
		if terminal.LastError == nil || strings.TrimSpace(terminal.LastError.Stage) == "" || strings.TrimSpace(terminal.LastError.Code) == "" {
			return scanInvalid(errors.New("failed health scan requires a stable error"))
		}
	default:
		return scanInvalid(errors.New("health scan terminal status is invalid"))
	}
	return nil
}

func sameCoverageBinding(left, right []domain.DetectorCoverage) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].DetectorID != right[index].DetectorID || left[index].DetectorVersion != right[index].DetectorVersion {
			return false
		}
		if right[index].Status == domain.DetectorCoverageStatusUnavailable && (left[index].Status != domain.DetectorCoverageStatusUnavailable || left[index].UnavailableReason != right[index].UnavailableReason) {
			return false
		}
		if right[index].Status == domain.DetectorCoverageStatusPending && left[index].Status == domain.DetectorCoverageStatusUnavailable {
			return false
		}
	}
	return true
}

func coverageByID(values []domain.DetectorCoverage, detectorID string) (domain.DetectorCoverage, bool) {
	for _, item := range values {
		if item.DetectorID == detectorID {
			return item, true
		}
	}
	return domain.DetectorCoverage{}, false
}

func sameCheckpoint(left, right domain.ScanCheckpoint) bool {
	if left.Cursor != right.Cursor || left.Page != right.Page {
		return false
	}
	if left.LastItem == nil || right.LastItem == nil {
		return left.LastItem == nil && right.LastItem == nil
	}
	return *left.LastItem == *right.LastItem
}

func sameFailure(left, right *domain.FailureSummary) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validScanID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func nilScanDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func scanInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeScanInvalid, false, cause)
}

func scanConsistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeScanInvalid, false, cause)
}

func scanUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_SCAN_UNAVAILABLE", true, cause)
}

func scanNotFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, "HEALTH_SCAN_NOT_FOUND", false, cause)
}
