package domain

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestEmbeddingModelSettingsRevisionIsIdentityButNotVectorCompatibility(t *testing.T) {
	t.Parallel()
	contract := validEmbeddingContract(t)
	leftRevision := int64(0)
	version := EmbeddingVersion{
		ID: "71000000-0000-4000-8000-000000000001", Provider: contract.Provider,
		AdapterName: contract.AdapterName, AdapterVersion: contract.AdapterVersion,
		Model: contract.Model, Dimensions: contract.Dimensions, Normalization: contract.Normalization,
		DistanceMetric: contract.DistanceMetric, ConfigHash: contract.ConfigHash,
		ModelSettingsRevision: &leftRevision, CreatedAt: time.Unix(1, 0).UTC(),
	}
	if err := ValidateEmbeddingContractBinding(contract, version); err != nil {
		t.Fatalf("managed embedding rejected by vector contract: %v", err)
	}
	right := version
	rightRevision := int64(7)
	right.ModelSettingsRevision = &rightRevision
	if SameEmbeddingBinding(version, right) {
		t.Fatal("different settings revisions were treated as one embedding identity")
	}
	if !SameEmbeddingContractBinding(version, right) {
		t.Fatal("settings provenance changed an otherwise identical vector contract")
	}
}

func TestExecutionProvenanceRejectsNegativeRevisionsAndFTSBinding(t *testing.T) {
	t.Parallel()
	negative := int64(-1)
	embedding := validEmbeddingVersion()
	embedding.ModelSettingsRevision = &negative
	err := ValidateEmbeddingVersion(embedding)
	assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeEmbeddingVersionInvalid)

	index := validIndexVersion(nil)
	zero := int64(0)
	index.ModelSettingsRevision = &zero
	err = ValidateIndexVersion(index)
	assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeIndexVersionInvalid)

	embeddingID := foundation.ID("71000000-0000-4000-8000-000000000002")
	index = validIndexVersion(&embeddingID)
	index.ModelSettingsRevision = &negative
	err = ValidateIndexVersion(index)
	assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeIndexVersionInvalid)
}

func TestSameIndexBuildBindingIncludesModelSettingsRevision(t *testing.T) {
	t.Parallel()
	embeddingID := foundation.ID("71000000-0000-4000-8000-000000000003")
	left := IndexBuild{IndexVersion: validIndexVersion(&embeddingID)}
	leftRevision := int64(0)
	left.IndexVersion.ModelSettingsRevision = &leftRevision
	right := left
	rightRevision := int64(1)
	right.IndexVersion.ModelSettingsRevision = &rightRevision
	if SameIndexBuildBinding(left, right) {
		t.Fatal("index replay accepted a different embedding settings revision")
	}
}
