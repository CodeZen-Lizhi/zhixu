package domain

import (
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
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
	if version.ID == "" || !isCanonicalText(version.Provider) || !isCanonicalText(version.AdapterName) ||
		!isCanonicalText(version.AdapterVersion) || !isCanonicalText(version.Model) {
		return invalid(ErrorCodeEmbeddingVersionInvalid, "embedding version binding is incomplete or not canonical")
	}
	if version.Dimensions <= 0 {
		return invalid(ErrorCodeEmbeddingVersionInvalid, "embedding dimensions must be positive")
	}
	if !isValidNormalization(version.Normalization) {
		return invalid(ErrorCodeEmbeddingVersionInvalid, "embedding normalization is unsupported")
	}
	if !isValidDistanceMetric(version.DistanceMetric) {
		return invalid(ErrorCodeEmbeddingVersionInvalid, "embedding distance metric is unsupported")
	}
	if !isCanonicalHash(version.ConfigHash) {
		return invalid(ErrorCodeEmbeddingVersionInvalid, "embedding config hash must be lowercase sha256")
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
