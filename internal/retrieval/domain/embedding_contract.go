package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	// MaxEmbeddingBatchSize 限制单次 Provider 调用的输入数量，避免无界远程请求。
	MaxEmbeddingBatchSize int32 = 1000
	// MaxEmbeddingInputBytes 限制单个输入的配置上界；具体 Adapter 可以使用更小值。
	MaxEmbeddingInputBytes int32 = 10 * 1024 * 1024
	// MaxEmbeddingBatchInputBytes 限制一次 Provider 请求输入正文的累计字节数。
	MaxEmbeddingBatchInputBytes int64 = 64 * 1024 * 1024
)

// EmbeddingContract 是运行时 Embedder 对向量结果和批量边界的不可变声明。
// ConfigHash 必须覆盖 EndpointIdentity、全部影响结果的非敏感配置和三个 limit。
type EmbeddingContract struct {
	Provider           string
	AdapterName        string
	AdapterVersion     string
	Model              string
	Dimensions         int32
	Normalization      EmbeddingNormalization
	DistanceMetric     DistanceMetric
	EndpointIdentity   string
	ConfigHash         string
	MaxBatchSize       int32
	MaxInputBytes      int32
	MaxBatchInputBytes int64
}

// ValidateEmbeddingContract 校验运行时 Adapter 的稳定身份、向量形状和有界批量能力。
func ValidateEmbeddingContract(contract EmbeddingContract) error {
	if !validEmbeddingBindingFields(
		contract.Provider,
		contract.AdapterName,
		contract.AdapterVersion,
		contract.Model,
		contract.Dimensions,
		contract.Normalization,
		contract.DistanceMetric,
		contract.ConfigHash,
	) || !isCanonicalText(contract.EndpointIdentity) || len(contract.EndpointIdentity) > 2048 {
		return invalid(ErrorCodeEmbeddingContractInvalid, "embedding contract result binding is invalid")
	}
	if contract.MaxBatchSize <= 0 || contract.MaxBatchSize > MaxEmbeddingBatchSize ||
		contract.MaxInputBytes <= 0 || contract.MaxInputBytes > MaxEmbeddingInputBytes ||
		contract.MaxBatchInputBytes < int64(contract.MaxInputBytes) ||
		contract.MaxBatchInputBytes > MaxEmbeddingBatchInputBytes {
		return invalid(ErrorCodeEmbeddingContractInvalid, "embedding contract limits are invalid")
	}
	wantHash, err := ComputeEmbeddingConfigHash(contract)
	if err != nil || wantHash != contract.ConfigHash {
		return invalid(ErrorCodeEmbeddingContractInvalid, "embedding contract config hash is inconsistent")
	}
	return nil
}

// ComputeEmbeddingConfigHash 计算不含 Credential 的 canonical SHA-256 配置身份。
func ComputeEmbeddingConfigHash(contract EmbeddingContract) (string, error) {
	if !isCanonicalText(contract.Provider) || !isCanonicalText(contract.AdapterName) ||
		!isCanonicalText(contract.AdapterVersion) || !isCanonicalText(contract.Model) ||
		!isCanonicalText(contract.EndpointIdentity) || len(contract.EndpointIdentity) > 2048 ||
		contract.Dimensions <= 0 || !isValidNormalization(contract.Normalization) ||
		!isValidDistanceMetric(contract.DistanceMetric) || contract.MaxBatchSize <= 0 ||
		contract.MaxBatchSize > MaxEmbeddingBatchSize || contract.MaxInputBytes <= 0 ||
		contract.MaxInputBytes > MaxEmbeddingInputBytes ||
		contract.MaxBatchInputBytes < int64(contract.MaxInputBytes) ||
		contract.MaxBatchInputBytes > MaxEmbeddingBatchInputBytes {
		return "", invalid(ErrorCodeEmbeddingContractInvalid, "embedding config descriptor is invalid")
	}
	payload := struct {
		SchemaVersion      int32                  `json:"schema_version"`
		Provider           string                 `json:"provider"`
		AdapterName        string                 `json:"adapter_name"`
		AdapterVersion     string                 `json:"adapter_version"`
		Model              string                 `json:"model"`
		Dimensions         int32                  `json:"dimensions"`
		Normalization      EmbeddingNormalization `json:"normalization"`
		DistanceMetric     DistanceMetric         `json:"distance_metric"`
		EndpointIdentity   string                 `json:"endpoint_identity"`
		MaxBatchSize       int32                  `json:"max_batch_size"`
		MaxInputBytes      int32                  `json:"max_input_bytes"`
		MaxBatchInputBytes int64                  `json:"max_batch_input_bytes"`
	}{
		SchemaVersion: 1, Provider: contract.Provider, AdapterName: contract.AdapterName,
		AdapterVersion: contract.AdapterVersion, Model: contract.Model, Dimensions: contract.Dimensions,
		Normalization: contract.Normalization, DistanceMetric: contract.DistanceMetric,
		EndpointIdentity: contract.EndpointIdentity, MaxBatchSize: contract.MaxBatchSize,
		MaxInputBytes: contract.MaxInputBytes, MaxBatchInputBytes: contract.MaxBatchInputBytes,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", invalid(ErrorCodeEmbeddingContractInvalid, "embedding config descriptor cannot be encoded")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ValidateEmbeddingContractBinding 证明运行时 Adapter 与持久化 Embedding Version 完全一致。
func ValidateEmbeddingContractBinding(contract EmbeddingContract, version EmbeddingVersion) error {
	if err := ValidateEmbeddingContract(contract); err != nil {
		return err
	}
	if err := ValidateEmbeddingVersion(version); err != nil {
		return err
	}
	contractVersion := EmbeddingVersion{
		Provider:       contract.Provider,
		AdapterName:    contract.AdapterName,
		AdapterVersion: contract.AdapterVersion,
		Model:          contract.Model,
		Dimensions:     contract.Dimensions,
		Normalization:  contract.Normalization,
		DistanceMetric: contract.DistanceMetric,
		ConfigHash:     contract.ConfigHash,
	}
	if !SameEmbeddingBinding(contractVersion, version) {
		return inconsistent(ErrorCodeEmbeddingContractInvalid, "embedding contract does not match persisted version")
	}
	return nil
}

// ValidateEmbeddingContractVector 校验 Provider 结果已经满足运行时 Contract。
func ValidateEmbeddingContractVector(contract EmbeddingContract, values []float32) error {
	if err := ValidateEmbeddingContract(contract); err != nil {
		return err
	}
	if err := validateEmbeddingVectorValues(contract.Dimensions, contract.Normalization, values); err != nil {
		return invalid(ErrorCodeEmbeddingContractInvalid, "embedding result does not satisfy runtime contract")
	}
	return nil
}
