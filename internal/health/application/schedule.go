package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

// ScheduleCreateCommand 创建默认关闭或显式启用的持久 Health Schedule。
type ScheduleCreateCommand struct {
	WorkspaceID    foundation.ID
	Scope          domain.ScanScope
	Cadence        domain.ScheduleCadence
	CronExpression string
	Timezone       string
	MaxItems       int64
	NextRunAt      *time.Time
	IdempotencyKey string
}

// ScheduleUpdateCommand 以 expected version 更新 cadence，不允许改变 scope identity。
type ScheduleUpdateCommand struct {
	WorkspaceID     foundation.ID
	ScheduleID      foundation.ID
	ExpectedVersion int64
	Cadence         domain.ScheduleCadence
	CronExpression  string
	Timezone        string
	MaxItems        int64
	NextRunAt       *time.Time
	IdempotencyKey  string
}

// DueScheduleClaim 表示 dispatcher 用 DB time 领取的一次 missed-once 调度。
type DueScheduleClaim struct {
	Schedule   domain.Schedule
	DueAt      time.Time
	ClaimedAt  time.Time
	LeaseUntil time.Time
}

// ScheduleStatePort 持久化 schedule 并通过 SKIP LOCKED 领取 due rows。
type ScheduleStatePort interface {
	Create(context.Context, ScheduleCreateCommand) (domain.Schedule, error)
	Update(context.Context, ScheduleUpdateCommand) (domain.Schedule, error)
	Get(context.Context, foundation.ID, foundation.ID) (domain.Schedule, error)
	ClaimDue(context.Context, int) ([]DueScheduleClaim, error)
}

// ScheduleDispatchPort 持久化 due delivery 的领取、确认和释放生命周期。
type ScheduleDispatchPort interface {
	ScheduleStatePort
	// AcknowledgeDue 在 Scan 已创建或精确重放后清除同一个 pending due。
	AcknowledgeDue(context.Context, DueScheduleClaim) error
	// ReleaseDue 在 Scan Start 或 Ack 未确认时释放租约，但保留同一个 pending due。
	ReleaseDue(context.Context, DueScheduleClaim) error
}

// ScheduleService 提供 schedule 状态与 due dispatch 编排。
type ScheduleService struct {
	state    ScheduleStatePort
	dispatch ScheduleDispatchPort
	scans    interface {
		Start(context.Context, ScanStartCommand) (ScanStartResult, error)
	}
	registry *Registry
}

// NewScheduleService 构造 schedule CRUD 服务；dispatcher 依赖可以留空。
func NewScheduleService(state ScheduleStatePort) (*ScheduleService, error) {
	if nilScanDependency(state) {
		return nil, scanUnavailable(errors.New("health schedule state is unavailable"))
	}
	return &ScheduleService{state: state}, nil
}

// NewScheduleDispatcher 构造带 Scan Start 与 detector registry 的 due dispatcher。
func NewScheduleDispatcher(state ScheduleDispatchPort, scans interface {
	Start(context.Context, ScanStartCommand) (ScanStartResult, error)
}, registry *Registry) (*ScheduleService, error) {
	if nilScanDependency(state) || nilScanDependency(scans) || registry == nil {
		return nil, scanUnavailable(errors.New("health schedule dispatcher dependencies are unavailable"))
	}
	return &ScheduleService{state: state, dispatch: state, scans: scans, registry: registry}, nil
}

// Create 创建 schedule；未指定 cadence 时默认 DISABLED。
func (service *ScheduleService) Create(ctx context.Context, command ScheduleCreateCommand) (domain.Schedule, error) {
	if service == nil || nilScanDependency(service.state) {
		return domain.Schedule{}, scanUnavailable(errors.New("health schedule state is unavailable"))
	}
	command = canonicalCreateSchedule(command)
	if !validScheduleIdempotencyKey(command.IdempotencyKey) {
		return domain.Schedule{}, scanInvalid(errors.New("health schedule idempotency key is invalid"))
	}
	return service.state.Create(ctx, command)
}

// Update 以 expected version 更新 schedule。
func (service *ScheduleService) Update(ctx context.Context, command ScheduleUpdateCommand) (domain.Schedule, error) {
	if service == nil || nilScanDependency(service.state) {
		return domain.Schedule{}, scanUnavailable(errors.New("health schedule state is unavailable"))
	}
	command = canonicalUpdateSchedule(command)
	if !validScheduleIdempotencyKey(command.IdempotencyKey) {
		return domain.Schedule{}, scanInvalid(errors.New("health schedule idempotency key is invalid"))
	}
	return service.state.Update(ctx, command)
}

// Get 返回 Workspace-scoped schedule。
func (service *ScheduleService) Get(ctx context.Context, workspaceID, scheduleID foundation.ID) (domain.Schedule, error) {
	if service == nil || nilScanDependency(service.state) {
		return domain.Schedule{}, scanUnavailable(errors.New("health schedule state is unavailable"))
	}
	return service.state.Get(ctx, workspaceID, scheduleID)
}

// DispatchDue 领取 due schedules 并各启动一次 durable Scan；失败不会返回假成功。
func (service *ScheduleService) DispatchDue(ctx context.Context, limit int) (int, error) {
	if service == nil || nilScanDependency(service.state) || nilScanDependency(service.dispatch) || nilScanDependency(service.scans) || service.registry == nil {
		return 0, scanUnavailable(errors.New("health schedule dispatcher is unavailable"))
	}
	if limit < 1 || limit > 100 {
		return 0, scanInvalid(errors.New("health schedule dispatch limit is invalid"))
	}
	claims, err := service.state.ClaimDue(ctx, limit)
	if err != nil {
		return 0, err
	}
	succeeded := 0
	var dispatchErrors []error
	for _, claim := range claims {
		coverage := service.registry.Coverage(Scope{WorkspaceID: claim.Schedule.WorkspaceID, Type: claim.Schedule.Scope.Type, Ref: claim.Schedule.Scope.Ref, Version: claim.Schedule.Scope.Version, Hash: claim.Schedule.Scope.Hash})
		_, err := service.scans.Start(ctx, ScanStartCommand{WorkspaceID: claim.Schedule.WorkspaceID, Scope: claim.Schedule.Scope, Coverage: coverage, MaxItems: claim.Schedule.MaxItems, IdempotencyKey: scheduleScanIdempotencyKey(claim.Schedule.ID, claim.DueAt), PreventScopeConcurrency: true, BindCurrentReadModel: claim.Schedule.Scope.Type == domain.ScanScopeTypeSmartCollection})
		if err != nil {
			dispatchErrors = append(dispatchErrors, err)
			if releaseErr := service.dispatch.ReleaseDue(context.WithoutCancel(ctx), claim); releaseErr != nil {
				dispatchErrors = append(dispatchErrors, releaseErr)
			}
			continue
		}
		if ackErr := service.dispatch.AcknowledgeDue(context.WithoutCancel(ctx), claim); ackErr != nil {
			dispatchErrors = append(dispatchErrors, ackErr)
			if releaseErr := service.dispatch.ReleaseDue(context.WithoutCancel(ctx), claim); releaseErr != nil {
				dispatchErrors = append(dispatchErrors, releaseErr)
			}
			continue
		}
		succeeded++
	}
	return succeeded, errors.Join(dispatchErrors...)
}

func canonicalCreateSchedule(command ScheduleCreateCommand) ScheduleCreateCommand {
	if command.Cadence == "" {
		command.Cadence = domain.ScheduleCadenceDisabled
	}
	if command.Timezone == "" {
		command.Timezone = "UTC"
	}
	if command.MaxItems == 0 {
		command.MaxItems = domain.MaxScheduleItems
	}
	if command.Cadence == domain.ScheduleCadenceDisabled {
		command.CronExpression = ""
		command.NextRunAt = nil
	}
	return command
}

func canonicalUpdateSchedule(command ScheduleUpdateCommand) ScheduleUpdateCommand {
	if command.Timezone == "" {
		command.Timezone = "UTC"
	}
	if command.Cadence == "" || command.Cadence == domain.ScheduleCadenceDisabled {
		command.Cadence = domain.ScheduleCadenceDisabled
		command.CronExpression = ""
		command.NextRunAt = nil
	}
	return command
}

// ScheduleCreateRequestHash 返回不包含 Idempotency-Key 的规范创建请求哈希。
func ScheduleCreateRequestHash(command ScheduleCreateCommand) (string, error) {
	command = canonicalCreateSchedule(command)
	payload, err := json.Marshal(struct {
		Operation      string
		WorkspaceID    foundation.ID
		Scope          domain.ScanScope
		Cadence        domain.ScheduleCadence
		CronExpression string
		Timezone       string
		MaxItems       int64
		NextRunAt      *time.Time
	}{"CREATE", command.WorkspaceID, command.Scope, command.Cadence, command.CronExpression, command.Timezone, command.MaxItems, command.NextRunAt})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// ScheduleUpdateRequestHash 返回不包含 Idempotency-Key 的规范更新请求哈希。
func ScheduleUpdateRequestHash(command ScheduleUpdateCommand) (string, error) {
	command = canonicalUpdateSchedule(command)
	payload, err := json.Marshal(struct {
		Operation       string
		WorkspaceID     foundation.ID
		ScheduleID      foundation.ID
		ExpectedVersion int64
		Cadence         domain.ScheduleCadence
		CronExpression  string
		Timezone        string
		MaxItems        int64
		NextRunAt       *time.Time
	}{"UPDATE", command.WorkspaceID, command.ScheduleID, command.ExpectedVersion, command.Cadence, command.CronExpression, command.Timezone, command.MaxItems, command.NextRunAt})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func validScheduleIdempotencyKey(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func scheduleScanIdempotencyKey(scheduleID foundation.ID, dueAt time.Time) string {
	digest := sha256.Sum256([]byte("health-schedule/v1\n" + string(scheduleID) + "\n" + dueAt.UTC().Format(time.RFC3339Nano)))
	return "health-schedule:" + hex.EncodeToString(digest[:])
}
