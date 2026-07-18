package application

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestValidateEmbedRequestEnforcesBatchAndInputLimits(t *testing.T) {
	t.Parallel()
	contract := applicationEmbeddingContract(t)
	if err := ValidateEmbedRequest(contract, EmbedRequest{Inputs: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	for _, request := range []EmbedRequest{
		{},
		{Inputs: []string{"  "}},
		{Inputs: []string{strings.Repeat("x", int(contract.MaxInputBytes)+1)}},
		{Inputs: []string{strings.Repeat("x", 11), strings.Repeat("y", 11)}},
		{Inputs: make([]string, int(contract.MaxBatchSize)+1)},
	} {
		err := ValidateEmbedRequest(contract, request)
		if err == nil {
			t.Fatalf("invalid request accepted: %#v", request)
		}
		assertApplicationError(t, err, foundation.ErrorInvalidInput, domain.ErrorCodeEmbedRequestInvalid)
	}
}

func TestValidateEmbedResultNormalizesWithoutMutatingProviderOutput(t *testing.T) {
	t.Parallel()
	contract := applicationEmbeddingContract(t)
	request := EmbedRequest{Inputs: []string{"first", "second"}}
	result := EmbedResult{Model: contract.Model, Embeddings: [][]float32{{3, 4, 0}, {0, 0, 2}}}
	normalized, err := ValidateEmbedResult(contract, request, result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Embeddings[0][0] != 3 || math.Abs(float64(normalized[0][0])-0.6) > 1e-6 || normalized[1][2] != 1 {
		t.Fatalf("result=%v normalized=%v", result.Embeddings, normalized)
	}
}

func TestValidateEmbedResultFailsClosedOnProviderContractDamage(t *testing.T) {
	t.Parallel()
	contract := applicationEmbeddingContract(t)
	request := EmbedRequest{Inputs: []string{"first"}}
	results := []EmbedResult{
		{Model: "other", Embeddings: [][]float32{{1, 0, 0}}},
		{Model: contract.Model},
		{Model: contract.Model, Embeddings: [][]float32{{1, 0}}},
		{Model: contract.Model, Embeddings: [][]float32{{0, 0, 0}}},
		{Model: contract.Model, Embeddings: [][]float32{{float32(math.NaN()), 0, 0}}},
	}
	for _, result := range results {
		_, err := ValidateEmbedResult(contract, request, result)
		if err == nil {
			t.Fatalf("invalid result accepted: %#v", result)
		}
		assertApplicationError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeEmbedResultInvalid)
	}
}

func applicationEmbeddingContract(t *testing.T) domain.EmbeddingContract {
	t.Helper()
	contract := domain.EmbeddingContract{
		Provider: "openai-compatible", AdapterName: "direct-http", AdapterVersion: "v1",
		Model: "embed-v1", Dimensions: 3, Normalization: domain.NormalizationL2,
		DistanceMetric: domain.DistanceCosine, EndpointIdentity: "https://models.example.test",
		MaxBatchSize: 3, MaxInputBytes: 16, MaxBatchInputBytes: 20,
	}
	hash, err := domain.ComputeEmbeddingConfigHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	contract.ConfigHash = hash
	return contract
}

func assertApplicationError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error=%T %v", err, err)
	}
	if classified.Kind != kind || classified.Code != code || classified.Retryable {
		t.Fatalf("error=%#v want kind=%q code=%q", classified, kind, code)
	}
}
