package domain

import (
	"bytes"
	"encoding/json"
	"math/big"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// DegradedCapability 是必须显式暴露给调用方的受限能力降级。
type DegradedCapability string

const (
	// DegradedVector 表示该 Index 不能声明完整向量检索能力。
	DegradedVector DegradedCapability = "vector"
)

// IndexVersion 是一个 Workspace 内不可变索引配置及其受控生命周期投影。
type IndexVersion struct {
	ID                   foundation.ID
	WorkspaceID          foundation.ID
	EmbeddingVersionID   *foundation.ID
	TokenizerID          string
	TokenizerVersion     string
	TokenizerConfigHash  string
	FusionConfig         json.RawMessage
	SourceSnapshotRef    string
	ManifestHash         string
	ExpectedChunkCount   int64
	IdempotencyKey       string
	Status               IndexStatus
	DegradedCapabilities []DegradedCapability
	FailureCode          string
	Version              int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// IndexBuild 将 Index Version 与同一事务中冻结的 Manifest 绑定。
type IndexBuild struct {
	IndexVersion IndexVersion
	Manifest     []ManifestChunk
}

// IndexVersionResult 返回幂等 Begin 后的持久化 Index 与不可变 Manifest。
type IndexVersionResult struct {
	IndexVersion IndexVersion
	Manifest     []ManifestChunk
	Created      bool
	Replayed     bool
}

// IndexTransition 是一次带乐观锁的通用 Index 生命周期迁移。
// 该命令不能进入 Active；Active 只能由 ActivationCommand 产生。
type IndexTransition struct {
	WorkspaceID          foundation.ID
	IndexVersionID       foundation.ID
	ExpectedVersion      int64
	Status               IndexStatus
	DegradedCapabilities []DegradedCapability
	FailureCode          string
	At                   time.Time
}

// NormalizeDegradedCapabilities 校验受限枚举并返回去重后的规范顺序副本。
func NormalizeDegradedCapabilities(capabilities []DegradedCapability) ([]DegradedCapability, error) {
	if len(capabilities) == 0 {
		return nil, nil
	}
	seenVector := false
	for _, capability := range capabilities {
		if capability != DegradedVector {
			return nil, invalid(ErrorCodeIndexVersionInvalid, "unsupported degraded capability")
		}
		seenVector = true
	}
	if seenVector {
		return []DegradedCapability{DegradedVector}, nil
	}
	return nil, nil
}

// HasDegradedCapability 判断规范化能力列表是否包含指定降级。
func HasDegradedCapability(capabilities []DegradedCapability, target DegradedCapability) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

// ValidateIndexVersion 校验 Index 的配置、状态和 FTS/vector 能力声明。
func ValidateIndexVersion(index IndexVersion) error {
	if index.ID == "" || index.WorkspaceID == "" || !isCanonicalText(index.TokenizerID) ||
		!isCanonicalText(index.TokenizerVersion) || !isCanonicalHash(index.TokenizerConfigHash) ||
		!isCanonicalText(index.SourceSnapshotRef) || !isCanonicalHash(index.ManifestHash) ||
		!isCanonicalText(index.IdempotencyKey) || len(index.IdempotencyKey) > 128 {
		return invalid(ErrorCodeIndexVersionInvalid, "index version binding is incomplete or not canonical")
	}
	if index.EmbeddingVersionID != nil && *index.EmbeddingVersionID == "" {
		return invalid(ErrorCodeIndexVersionInvalid, "embedding version identity cannot be empty")
	}
	if len(index.FusionConfig) == 0 || !json.Valid(index.FusionConfig) {
		return invalid(ErrorCodeIndexVersionInvalid, "fusion config must be valid json")
	}
	if index.ExpectedChunkCount < 0 || !IsValidIndexStatus(index.Status) || index.Version <= 0 ||
		index.CreatedAt.IsZero() || index.UpdatedAt.IsZero() || index.UpdatedAt.Before(index.CreatedAt) {
		return invalid(ErrorCodeIndexVersionInvalid, "index lifecycle metadata is invalid")
	}
	normalized, err := NormalizeDegradedCapabilities(index.DegradedCapabilities)
	if err != nil {
		return err
	}
	if !equalCapabilities(index.DegradedCapabilities, normalized) {
		return invalid(ErrorCodeIndexVersionInvalid, "degraded capabilities must be canonical and unique")
	}
	if index.EmbeddingVersionID == nil && !HasDegradedCapability(normalized, DegradedVector) {
		return invalid(ErrorCodeIndexVersionInvalid, "FTS-only index must declare vector degradation")
	}
	if index.Status == IndexStatusFailed {
		if !isCanonicalText(index.FailureCode) {
			return invalid(ErrorCodeIndexVersionInvalid, "failed index requires a stable failure code")
		}
	} else if index.FailureCode != "" {
		return invalid(ErrorCodeIndexVersionInvalid, "non-failed index cannot carry a failure code")
	}
	return nil
}

// ValidateIndexBuild 校验 Begin Index 与冻结 Manifest 的 Count/Hash 完全一致。
func ValidateIndexBuild(build IndexBuild) error {
	if err := ValidateIndexVersion(build.IndexVersion); err != nil {
		return err
	}
	if build.IndexVersion.Status != IndexStatusBuilding {
		return invalid(ErrorCodeIndexVersionInvalid, "new index must start in building status")
	}
	canonical, manifestHash, err := CanonicalizeManifest(build.IndexVersion.WorkspaceID, build.IndexVersion.ID, build.Manifest)
	if err != nil {
		return err
	}
	if int64(len(canonical)) != build.IndexVersion.ExpectedChunkCount || manifestHash != build.IndexVersion.ManifestHash {
		return inconsistent(ErrorCodeManifestInvalid, "manifest count or hash does not match index version")
	}
	return nil
}

// ValidateIndexTransitionCommand 校验乐观锁、绑定、失败码和通用状态迁移。
func ValidateIndexTransitionCommand(current IndexVersion, command IndexTransition) error {
	if command.WorkspaceID != current.WorkspaceID || command.IndexVersionID != current.ID || command.ExpectedVersion != current.Version {
		return conflict(ErrorCodeIndexTransitionInvalid, "index transition binding or expected version is stale")
	}
	if command.At.IsZero() || command.At.Before(current.UpdatedAt) {
		return invalid(ErrorCodeIndexTransitionInvalid, "index transition time is invalid")
	}
	if err := ValidateIndexTransition(current.Status, command.Status); err != nil {
		return err
	}
	capabilities := command.DegradedCapabilities
	if capabilities == nil {
		capabilities = current.DegradedCapabilities
	}
	normalized, err := NormalizeDegradedCapabilities(capabilities)
	if err != nil || !equalCapabilities(capabilities, normalized) {
		return invalid(ErrorCodeIndexTransitionInvalid, "transition degraded capabilities are invalid")
	}
	if current.EmbeddingVersionID == nil && !HasDegradedCapability(normalized, DegradedVector) {
		return invalid(ErrorCodeIndexTransitionInvalid, "FTS-only transition cannot remove vector degradation")
	}
	if command.Status == IndexStatusFailed {
		if !isCanonicalText(command.FailureCode) {
			return invalid(ErrorCodeIndexTransitionInvalid, "failed transition requires a stable failure code")
		}
	} else if command.FailureCode != "" {
		return invalid(ErrorCodeIndexTransitionInvalid, "non-failed transition cannot carry a failure code")
	}
	return nil
}

// SameIndexBuildBinding 判断两次 Begin 请求是否是完全相同的幂等重放。
func SameIndexBuildBinding(left, right IndexBuild) bool {
	if left.IndexVersion.WorkspaceID != right.IndexVersion.WorkspaceID ||
		!optionalIDEqual(left.IndexVersion.EmbeddingVersionID, right.IndexVersion.EmbeddingVersionID) ||
		left.IndexVersion.TokenizerID != right.IndexVersion.TokenizerID ||
		left.IndexVersion.TokenizerVersion != right.IndexVersion.TokenizerVersion ||
		left.IndexVersion.TokenizerConfigHash != right.IndexVersion.TokenizerConfigHash ||
		!sameJSONValue(left.IndexVersion.FusionConfig, right.IndexVersion.FusionConfig) ||
		left.IndexVersion.SourceSnapshotRef != right.IndexVersion.SourceSnapshotRef ||
		left.IndexVersion.ManifestHash != right.IndexVersion.ManifestHash ||
		left.IndexVersion.ExpectedChunkCount != right.IndexVersion.ExpectedChunkCount ||
		left.IndexVersion.IdempotencyKey != right.IndexVersion.IdempotencyKey ||
		!equalCapabilities(left.IndexVersion.DegradedCapabilities, right.IndexVersion.DegradedCapabilities) {
		return false
	}
	return true
}

func sameJSONValue(left, right json.RawMessage) bool {
	var leftValue any
	var rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return false
	}
	return sameDecodedJSONValue(leftValue, rightValue)
}

func sameDecodedJSONValue(left, right any) bool {
	switch leftValue := left.(type) {
	case nil:
		return right == nil
	case bool:
		rightValue, ok := right.(bool)
		return ok && leftValue == rightValue
	case string:
		rightValue, ok := right.(string)
		return ok && leftValue == rightValue
	case json.Number:
		rightValue, ok := right.(json.Number)
		if !ok {
			return false
		}
		leftNumber, leftOK := new(big.Rat).SetString(leftValue.String())
		rightNumber, rightOK := new(big.Rat).SetString(rightValue.String())
		return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for index := range leftValue {
			if !sameDecodedJSONValue(leftValue[index], rightValue[index]) {
				return false
			}
		}
		return true
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for key, leftItem := range leftValue {
			rightItem, exists := rightValue[key]
			if !exists || !sameDecodedJSONValue(leftItem, rightItem) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func equalCapabilities(left, right []DegradedCapability) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func optionalIDEqual(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
