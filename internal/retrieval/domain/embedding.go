package domain

import (
	"math"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// EmbeddingL2NormTolerance 是 Go 与 PostgreSQL cache 共同使用的单位范数容差。
	EmbeddingL2NormTolerance = 1e-5
)

// EmbeddingNormalization 描述向量写入前的稳定归一化约定。
type EmbeddingNormalization string

const (
	// NormalizationNone 表示 Provider 输出按原值保存。
	NormalizationNone EmbeddingNormalization = "none"
	// NormalizationL2 表示向量按 L2 范数归一化。
	NormalizationL2 EmbeddingNormalization = "l2"
)

// DistanceMetric 描述该 Embedding Version 的距离计算契约。
type DistanceMetric string

const (
	// DistanceCosine 使用余弦距离。
	DistanceCosine DistanceMetric = "cosine"
	// DistanceInnerProduct 使用内积距离。
	DistanceInnerProduct DistanceMetric = "inner_product"
	// DistanceEuclidean 使用欧氏距离。
	DistanceEuclidean DistanceMetric = "euclidean"
)

// EmbeddingVersion 是不可变的 Provider、Adapter、模型与向量形状绑定。
// ConfigHash 必须由包含所有影响向量结果的非敏感配置计算，记录中不保存凭据。
type EmbeddingVersion struct {
	ID             foundation.ID
	Provider       string
	AdapterName    string
	AdapterVersion string
	Model          string
	Dimensions     int32
	Normalization  EmbeddingNormalization
	DistanceMetric DistanceMetric
	ConfigHash     string
	CreatedAt      time.Time
}

// EmbeddingVersionResult 返回幂等注册后的持久化版本。
type EmbeddingVersionResult struct {
	EmbeddingVersion EmbeddingVersion
	Created          bool
	Replayed         bool
}

// ValidateEmbeddingVersion 校验 Embedding Version 的不可变字段与受限枚举。
func ValidateEmbeddingVersion(version EmbeddingVersion) error {
	if version.ID == "" || !validEmbeddingBindingFields(
		version.Provider,
		version.AdapterName,
		version.AdapterVersion,
		version.Model,
		version.Dimensions,
		version.Normalization,
		version.DistanceMetric,
		version.ConfigHash,
	) {
		return invalid(ErrorCodeEmbeddingVersionInvalid, "embedding version binding is incomplete or not canonical")
	}
	if version.CreatedAt.IsZero() {
		return invalid(ErrorCodeEmbeddingVersionInvalid, "embedding created time is required")
	}
	return nil
}

// SameEmbeddingBinding 判断两条记录是否绑定完全相同的向量结果身份。
// ID 与创建时间不参与比较，使 Repository 能识别首次创建和精确重放。
func SameEmbeddingBinding(left, right EmbeddingVersion) bool {
	return left.Provider == right.Provider &&
		left.AdapterName == right.AdapterName &&
		left.AdapterVersion == right.AdapterVersion &&
		left.Model == right.Model &&
		left.Dimensions == right.Dimensions &&
		left.Normalization == right.Normalization &&
		left.DistanceMetric == right.DistanceMetric &&
		left.ConfigHash == right.ConfigHash
}

// NormalizeEmbeddingVector 返回调用方不可变的向量副本，并按版本约定执行 L2 归一化。
func NormalizeEmbeddingVector(normalization EmbeddingNormalization, values []float32) ([]float32, error) {
	if !isValidNormalization(normalization) || len(values) == 0 {
		return nil, invalid(ErrorCodeEmbeddingContractInvalid, "embedding normalization or vector is invalid")
	}
	result := append([]float32(nil), values...)
	normSquared, ok := embeddingNormSquared(result)
	if !ok {
		return nil, invalid(ErrorCodeEmbeddingContractInvalid, "embedding vector must be finite and non-zero")
	}
	if normalization == NormalizationL2 {
		norm := math.Sqrt(normSquared)
		for index := range result {
			result[index] = float32(float64(result[index]) / norm)
		}
		if normalizedNormSquared, normalized := embeddingNormSquared(result); !normalized ||
			math.Abs(math.Sqrt(normalizedNormSquared)-1) > EmbeddingL2NormTolerance {
			return nil, invalid(ErrorCodeEmbeddingContractInvalid, "embedding vector cannot be normalized within tolerance")
		}
	}
	return result, nil
}

// ValidateEmbeddingVector 校验向量维度、有限值、非零范数和可选 L2 单位范数。
func ValidateEmbeddingVector(version EmbeddingVersion, values []float32) error {
	if err := ValidateEmbeddingVersion(version); err != nil {
		return err
	}
	if err := validateEmbeddingVectorValues(version.Dimensions, version.Normalization, values); err != nil {
		return invalid(ErrorCodeEmbeddingContractInvalid, "embedding vector dimensions do not match version")
	}
	return nil
}

func isValidNormalization(value EmbeddingNormalization) bool {
	return value == NormalizationNone || value == NormalizationL2
}

func isValidDistanceMetric(value DistanceMetric) bool {
	switch value {
	case DistanceCosine, DistanceInnerProduct, DistanceEuclidean:
		return true
	default:
		return false
	}
}

func validEmbeddingBindingFields(
	provider, adapterName, adapterVersion, model string,
	dimensions int32,
	normalization EmbeddingNormalization,
	distance DistanceMetric,
	configHash string,
) bool {
	return isCanonicalText(provider) && isCanonicalText(adapterName) && isCanonicalText(adapterVersion) &&
		isCanonicalText(model) && dimensions > 0 && isValidNormalization(normalization) &&
		isValidDistanceMetric(distance) && isCanonicalHash(configHash)
}

func embeddingNormSquared(values []float32) (float64, bool) {
	normSquared := float64(0)
	for _, value := range values {
		component := float64(value)
		if math.IsNaN(component) || math.IsInf(component, 0) {
			return 0, false
		}
		normSquared += component * component
	}
	return normSquared, normSquared > 0 && !math.IsNaN(normSquared) && !math.IsInf(normSquared, 0)
}

func validateEmbeddingVectorValues(dimensions int32, normalization EmbeddingNormalization, values []float32) error {
	if len(values) != int(dimensions) {
		return invalid(ErrorCodeEmbeddingContractInvalid, "embedding vector dimensions are invalid")
	}
	normSquared, ok := embeddingNormSquared(values)
	if !ok {
		return invalid(ErrorCodeEmbeddingContractInvalid, "embedding vector must be finite and non-zero")
	}
	if normalization == NormalizationL2 && math.Abs(math.Sqrt(normSquared)-1) > EmbeddingL2NormTolerance {
		return invalid(ErrorCodeEmbeddingContractInvalid, "l2 embedding vector must have unit norm")
	}
	return nil
}

func isCanonicalText(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func isCanonicalHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
