package domain

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ActivationKind 区分首次/替换激活与回滚激活事实。
type ActivationKind string

const (
	// ActivationKindActivate 表示将 Ready Index 激活。
	ActivationKindActivate ActivationKind = "activate"
	// ActivationKindRollback 表示将 Retiring Index 恢复为 Active。
	ActivationKindRollback ActivationKind = "rollback"
)

// Activation 是一次 append-only 的 Active Index 切换事实。
type Activation struct {
	ID                     foundation.ID
	Kind                   ActivationKind
	WorkspaceID            foundation.ID
	TargetIndexVersionID   foundation.ID
	PreviousIndexVersionID *foundation.ID
	TargetVersion          int64
	PreviousVersion        *int64
	IdempotencyKey         string
	ReasonCode             string
	CreatedAt              time.Time
}

// ActivationCommand 原子激活一个 Ready Index，并可替换当前 Active。
type ActivationCommand struct {
	ActivationID                  foundation.ID
	WorkspaceID                   foundation.ID
	TargetIndexVersionID          foundation.ID
	ExpectedTargetVersion         int64
	ExpectedCurrentIndexVersionID *foundation.ID
	ExpectedCurrentVersion        *int64
	IdempotencyKey                string
	ReasonCode                    string
	At                            time.Time
}

// RollbackActivationCommand 原子将当前 Active 退役并恢复指定 Retiring Index。
type RollbackActivationCommand struct {
	ActivationID                  foundation.ID
	WorkspaceID                   foundation.ID
	TargetIndexVersionID          foundation.ID
	ExpectedTargetVersion         int64
	ExpectedCurrentIndexVersionID foundation.ID
	ExpectedCurrentVersion        int64
	IdempotencyKey                string
	ReasonCode                    string
	At                            time.Time
}

// ActivationResult 返回原子切换后的 Active、上一版本与追加记录。
type ActivationResult struct {
	Activation           Activation
	ActiveIndexVersion   IndexVersion
	PreviousIndexVersion *IndexVersion
	Replayed             bool
}

// BuildStatus 汇总一个 Index 的 Manifest 与 Projection 完整性。
type BuildStatus struct {
	WorkspaceID                 foundation.ID
	IndexVersionID              foundation.ID
	IndexStatus                 IndexStatus
	IndexVersion                int64
	ExpectedChunkCount          int64
	ManifestChunkCount          int64
	ProjectionCount             int64
	LexicalPendingCount         int64
	LexicalReadyCount           int64
	LexicalFailedCount          int64
	VectorDisabledCount         int64
	VectorPendingCount          int64
	VectorReadyCount            int64
	VectorSkippedOversizedCount int64
	VectorFailedCount           int64
	DegradedCapabilities        []DegradedCapability
}

// ValidateActivationCommand 校验 Ready 目标、当前 Active 和所有期望版本绑定。
func ValidateActivationCommand(currentActive *IndexVersion, target IndexVersion, command ActivationCommand) error {
	if err := ValidateActivationCommandInput(command); err != nil {
		return err
	}
	if err := ValidateIndexVersion(target); err != nil {
		return err
	}
	if target.WorkspaceID != command.WorkspaceID || target.ID != command.TargetIndexVersionID || target.Version != command.ExpectedTargetVersion {
		return conflict(ErrorCodeActivationInvalid, "activation target binding or expected version is stale")
	}
	if target.Status != IndexStatusReady {
		return conflict(ErrorCodeActivationInvalid, "activation target must be ready")
	}
	if command.At.Before(target.UpdatedAt) {
		return invalid(ErrorCodeActivationInvalid, "activation time predates target state")
	}
	if currentActive == nil {
		if command.ExpectedCurrentIndexVersionID != nil || command.ExpectedCurrentVersion != nil {
			return conflict(ErrorCodeActivationInvalid, "initial activation cannot bind a current active index")
		}
		return nil
	}
	if err := ValidateIndexVersion(*currentActive); err != nil {
		return err
	}
	if command.ExpectedCurrentIndexVersionID == nil || command.ExpectedCurrentVersion == nil ||
		currentActive.WorkspaceID != command.WorkspaceID || currentActive.Status != IndexStatusActive ||
		currentActive.ID != *command.ExpectedCurrentIndexVersionID || currentActive.Version != *command.ExpectedCurrentVersion ||
		currentActive.ID == target.ID {
		return conflict(ErrorCodeActivationInvalid, "current active binding or expected version is stale")
	}
	if command.At.Before(currentActive.UpdatedAt) {
		return invalid(ErrorCodeActivationInvalid, "activation time predates current active state")
	}
	return nil
}

// ValidateRollbackActivationCommand 校验当前 Active 与待恢复 Retiring Index 的版本绑定。
func ValidateRollbackActivationCommand(currentActive, target IndexVersion, command RollbackActivationCommand) error {
	if err := ValidateRollbackActivationCommandInput(command); err != nil {
		return err
	}
	if err := ValidateIndexVersion(currentActive); err != nil {
		return err
	}
	if err := ValidateIndexVersion(target); err != nil {
		return err
	}
	if currentActive.WorkspaceID != command.WorkspaceID || target.WorkspaceID != command.WorkspaceID ||
		currentActive.ID != command.ExpectedCurrentIndexVersionID || currentActive.Version != command.ExpectedCurrentVersion ||
		target.ID != command.TargetIndexVersionID || target.Version != command.ExpectedTargetVersion {
		return conflict(ErrorCodeActivationInvalid, "rollback binding or expected version is stale")
	}
	if currentActive.Status != IndexStatusActive || target.Status != IndexStatusRetiring || currentActive.ID == target.ID {
		return conflict(ErrorCodeActivationInvalid, "rollback requires distinct active and retiring indexes")
	}
	if command.At.Before(currentActive.UpdatedAt) || command.At.Before(target.UpdatedAt) {
		return invalid(ErrorCodeActivationInvalid, "rollback time predates index state")
	}
	return nil
}

// ValidateActivationCommandInput 校验激活命令自身的规范字段，不读取可变 Index 状态。
// 完整状态绑定必须由 Store 在持锁事务内调用 ValidateActivationCommand 校验。
func ValidateActivationCommandInput(command ActivationCommand) error {
	if err := validateActivationFields(command.ActivationID, command.WorkspaceID, command.TargetIndexVersionID,
		command.ExpectedTargetVersion, command.IdempotencyKey, command.ReasonCode, command.At); err != nil {
		return err
	}
	if (command.ExpectedCurrentIndexVersionID == nil) != (command.ExpectedCurrentVersion == nil) {
		return invalid(ErrorCodeActivationInvalid, "current active identity and version must be provided together")
	}
	if command.ExpectedCurrentIndexVersionID != nil && (*command.ExpectedCurrentIndexVersionID == "" || *command.ExpectedCurrentVersion <= 0) {
		return invalid(ErrorCodeActivationInvalid, "current active expectation is invalid")
	}
	return nil
}

// ValidateRollbackActivationCommandInput 校验回滚命令自身的规范字段。
func ValidateRollbackActivationCommandInput(command RollbackActivationCommand) error {
	if err := validateActivationFields(command.ActivationID, command.WorkspaceID, command.TargetIndexVersionID,
		command.ExpectedTargetVersion, command.IdempotencyKey, command.ReasonCode, command.At); err != nil {
		return err
	}
	if command.ExpectedCurrentIndexVersionID == "" || command.ExpectedCurrentVersion <= 0 ||
		command.ExpectedCurrentIndexVersionID == command.TargetIndexVersionID {
		return invalid(ErrorCodeActivationInvalid, "rollback current active expectation is invalid")
	}
	return nil
}

// ValidateBuildReady 证明 Manifest、FTS 与向量 Projection 足以安全进入 Ready。
func ValidateBuildReady(index IndexVersion, status BuildStatus) error {
	if err := ValidateIndexVersion(index); err != nil {
		return err
	}
	if index.Status != IndexStatusBuilding || status.WorkspaceID != index.WorkspaceID || status.IndexVersionID != index.ID ||
		status.IndexStatus != index.Status || status.IndexVersion != index.Version || status.ExpectedChunkCount != index.ExpectedChunkCount {
		return inconsistent(ErrorCodeBuildIncomplete, "build status does not match current building index")
	}
	if hasNegativeBuildCount(status) || status.ManifestChunkCount != index.ExpectedChunkCount ||
		status.ProjectionCount != index.ExpectedChunkCount || status.LexicalReadyCount != index.ExpectedChunkCount ||
		status.LexicalPendingCount != 0 || status.LexicalFailedCount != 0 {
		return inconsistent(ErrorCodeBuildIncomplete, "manifest or lexical projection is incomplete")
	}
	if status.LexicalPendingCount+status.LexicalReadyCount+status.LexicalFailedCount != status.ProjectionCount ||
		status.VectorDisabledCount+status.VectorPendingCount+status.VectorReadyCount+
			status.VectorSkippedOversizedCount+status.VectorFailedCount != status.ProjectionCount {
		return inconsistent(ErrorCodeBuildIncomplete, "projection status counts are inconsistent")
	}
	normalized, err := NormalizeDegradedCapabilities(status.DegradedCapabilities)
	if err != nil || !equalCapabilities(normalized, status.DegradedCapabilities) ||
		!equalCapabilities(status.DegradedCapabilities, index.DegradedCapabilities) {
		return inconsistent(ErrorCodeBuildIncomplete, "build degradation does not match index declaration")
	}
	if index.EmbeddingVersionID == nil {
		if !HasDegradedCapability(normalized, DegradedVector) || status.VectorDisabledCount != index.ExpectedChunkCount ||
			status.VectorPendingCount != 0 || status.VectorReadyCount != 0 || status.VectorSkippedOversizedCount != 0 || status.VectorFailedCount != 0 {
			return inconsistent(ErrorCodeBuildIncomplete, "FTS-only build has inconsistent vector state")
		}
		return nil
	}
	if status.VectorDisabledCount != 0 || status.VectorPendingCount != 0 {
		return inconsistent(ErrorCodeBuildIncomplete, "hybrid build still has disabled or pending vectors")
	}
	degradedCount := status.VectorSkippedOversizedCount + status.VectorFailedCount
	if HasDegradedCapability(normalized, DegradedVector) {
		if degradedCount == 0 || status.VectorReadyCount+degradedCount != index.ExpectedChunkCount {
			return inconsistent(ErrorCodeBuildIncomplete, "vector degradation declaration does not match projection states")
		}
		return nil
	}
	if degradedCount != 0 || status.VectorReadyCount != index.ExpectedChunkCount {
		return inconsistent(ErrorCodeBuildIncomplete, "complete hybrid capability requires every vector ready")
	}
	return nil
}

func validateActivationFields(activationID, workspaceID, targetID foundation.ID, expectedTargetVersion int64, idempotencyKey, reasonCode string, at time.Time) error {
	if activationID == "" || workspaceID == "" || targetID == "" || expectedTargetVersion <= 0 ||
		!isCanonicalText(idempotencyKey) || len(idempotencyKey) > 128 ||
		!isCanonicalText(reasonCode) || len(reasonCode) > 128 || at.IsZero() {
		return invalid(ErrorCodeActivationInvalid, "activation command is incomplete or not canonical")
	}
	return nil
}

func hasNegativeBuildCount(status BuildStatus) bool {
	return status.ExpectedChunkCount < 0 || status.ManifestChunkCount < 0 || status.ProjectionCount < 0 ||
		status.LexicalPendingCount < 0 || status.LexicalReadyCount < 0 || status.LexicalFailedCount < 0 ||
		status.VectorDisabledCount < 0 || status.VectorPendingCount < 0 || status.VectorReadyCount < 0 ||
		status.VectorSkippedOversizedCount < 0 || status.VectorFailedCount < 0
}
