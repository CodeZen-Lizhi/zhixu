package domain

import (
	"math"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestEmbeddingContractHashFreezesEndpointAndLimits(t *testing.T) {
	t.Parallel()
	contract := validEmbeddingContract(t)
	if err := ValidateEmbeddingContract(contract); err != nil {
		t.Fatalf("ValidateEmbeddingContract() error = %v", err)
	}

	mutations := []func(*EmbeddingContract){
		func(value *EmbeddingContract) { value.EndpointIdentity = "https://models.example.test/tenant-b" },
		func(value *EmbeddingContract) { value.MaxBatchSize++ },
		func(value *EmbeddingContract) { value.MaxInputBytes++ },
		func(value *EmbeddingContract) { value.MaxBatchInputBytes++ },
		func(value *EmbeddingContract) { value.Model = "embed-v2" },
	}
	for index, mutate := range mutations {
		candidate := contract
		mutate(&candidate)
		hash, err := ComputeEmbeddingConfigHash(candidate)
		if err != nil {
			t.Fatalf("mutation %d hash error = %v", index, err)
		}
		if hash == contract.ConfigHash {
			t.Fatalf("mutation %d did not change config hash", index)
		}
		if err := ValidateEmbeddingContract(candidate); err == nil {
			t.Fatalf("mutation %d accepted stale config hash", index)
		}
	}
}

func TestValidateEmbeddingContractRejectsInvalidLimitsAndBinding(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*EmbeddingContract){
		func(value *EmbeddingContract) { value.EndpointIdentity = " endpoint " },
		func(value *EmbeddingContract) { value.MaxBatchSize = 0 },
		func(value *EmbeddingContract) { value.MaxBatchSize = MaxEmbeddingBatchSize + 1 },
		func(value *EmbeddingContract) { value.MaxInputBytes = 0 },
		func(value *EmbeddingContract) { value.MaxInputBytes = MaxEmbeddingInputBytes + 1 },
		func(value *EmbeddingContract) { value.MaxBatchInputBytes = int64(value.MaxInputBytes) - 1 },
		func(value *EmbeddingContract) { value.MaxBatchInputBytes = MaxEmbeddingBatchInputBytes + 1 },
		func(value *EmbeddingContract) { value.ConfigHash = strings.Repeat("f", 64) },
	} {
		contract := validEmbeddingContract(t)
		mutate(&contract)
		err := ValidateEmbeddingContract(contract)
		if err == nil {
			t.Fatalf("invalid contract accepted: %#v", contract)
		}
		assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeEmbeddingContractInvalid)
	}
}

func TestValidateEmbeddingContractBindingDetectsPersistedMismatch(t *testing.T) {
	t.Parallel()
	contract := validEmbeddingContract(t)
	version := validEmbeddingVersion()
	version.Provider = contract.Provider
	version.AdapterName = contract.AdapterName
	version.AdapterVersion = contract.AdapterVersion
	version.Model = contract.Model
	version.Dimensions = contract.Dimensions
	version.Normalization = contract.Normalization
	version.DistanceMetric = contract.DistanceMetric
	version.ConfigHash = contract.ConfigHash
	if err := ValidateEmbeddingContractBinding(contract, version); err != nil {
		t.Fatal(err)
	}
	version.Model = "other-model"
	err := ValidateEmbeddingContractBinding(contract, version)
	if err == nil {
		t.Fatal("mismatched persisted version accepted")
	}
	assertDomainError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeEmbeddingContractInvalid)
}

func TestNormalizeAndValidateEmbeddingVectorEnforcesL2Tolerance(t *testing.T) {
	t.Parallel()
	input := []float32{3, 4, 0}
	normalized, err := NormalizeEmbeddingVector(NormalizationL2, input)
	if err != nil {
		t.Fatal(err)
	}
	if input[0] != 3 || math.Abs(float64(normalized[0])-0.6) > 1e-6 || math.Abs(float64(normalized[1])-0.8) > 1e-6 {
		t.Fatalf("input=%v normalized=%v", input, normalized)
	}
	version := validEmbeddingVersion()
	if err := ValidateEmbeddingVector(version, normalized); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEmbeddingVector(version, []float32{1, 1, 0}); err == nil {
		t.Fatal("non-unit l2 vector accepted")
	}
	if _, err := NormalizeEmbeddingVector(NormalizationL2, []float32{0, 0}); err == nil {
		t.Fatal("zero vector normalized")
	}
	if _, err := NormalizeEmbeddingVector(NormalizationNone, []float32{float32(math.Inf(1))}); err == nil {
		t.Fatal("infinite vector accepted")
	}
}

func validEmbeddingContract(t *testing.T) EmbeddingContract {
	t.Helper()
	contract := EmbeddingContract{
		Provider: "openai-compatible", AdapterName: "direct-http", AdapterVersion: "v1",
		Model: "embed-v1", Dimensions: 3, Normalization: NormalizationL2,
		DistanceMetric: DistanceCosine, EndpointIdentity: "https://models.example.test",
		MaxBatchSize: 128, MaxInputBytes: 64 * 1024, MaxBatchInputBytes: 8 * 1024 * 1024,
	}
	hash, err := ComputeEmbeddingConfigHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	contract.ConfigHash = hash
	return contract
}
