package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestNormalizeDegradedCapabilitiesRejectsUnknownAndDeduplicates(t *testing.T) {
	t.Parallel()

	got, err := NormalizeDegradedCapabilities([]DegradedCapability{DegradedVector, DegradedVector})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != DegradedVector {
		t.Fatalf("capabilities = %#v", got)
	}
	if _, err := NormalizeDegradedCapabilities([]DegradedCapability{"rerank"}); err == nil {
		t.Fatal("unknown degraded capability accepted")
	}
}

func TestValidateIndexVersionEnforcesFTSOnlyCapabilityContract(t *testing.T) {
	t.Parallel()

	index := validIndexVersion(nil)
	if err := ValidateIndexVersion(index); err != nil {
		t.Fatalf("ValidateIndexVersion() error = %v", err)
	}
	index.DegradedCapabilities = nil
	err := ValidateIndexVersion(index)
	if err == nil {
		t.Fatal("FTS-only index without vector degradation accepted")
	}
	assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeIndexVersionInvalid)

	embeddingID := validEmbeddingVersion().ID
	index = validIndexVersion(&embeddingID)
	index.DegradedCapabilities = nil
	if err := ValidateIndexVersion(index); err != nil {
		t.Fatalf("hybrid index error = %v", err)
	}
}

func TestValidateIndexVersionSupportsLegacyAndRequiresCompleteSourceBinding(t *testing.T) {
	t.Parallel()
	legacy := validIndexVersion(nil)
	if err := ValidateIndexVersion(legacy); err != nil {
		t.Fatal(err)
	}
	count := int64(2)
	sourceBound := legacy
	sourceBound.SourceManifestHash = strings.Repeat("a", 64)
	sourceBound.ExpectedSourceCount = &count
	sourceBound.ProcessingContract = testProcessingContract()
	if err := ValidateIndexVersion(sourceBound); err != nil {
		t.Fatal(err)
	}
	sourceBound.ExpectedSourceCount = nil
	if err := ValidateIndexVersion(sourceBound); err == nil {
		t.Fatal("source hash without count accepted")
	}
	zero := int64(0)
	sourceBound.ExpectedSourceCount = &zero
	if err := ValidateIndexVersion(sourceBound); err == nil {
		t.Fatal("zero source count accepted")
	}
}

func TestValidateIndexBuildFreezesCanonicalManifest(t *testing.T) {
	t.Parallel()

	index := validIndexVersion(nil)
	manifest := validManifest(index.WorkspaceID, index.ID)
	canonical, hash, err := CanonicalizeManifest(index.WorkspaceID, index.ID, manifest)
	if err != nil {
		t.Fatal(err)
	}
	index.ManifestHash = hash
	index.ExpectedChunkCount = int64(len(canonical))
	build := IndexBuild{IndexVersion: index, Manifest: canonical}
	if err := ValidateIndexBuild(build); err != nil {
		t.Fatalf("ValidateIndexBuild() error = %v", err)
	}

	build.IndexVersion.ExpectedChunkCount++
	if err := ValidateIndexBuild(build); err == nil {
		t.Fatal("manifest count mismatch accepted")
	}
	build.IndexVersion.ExpectedChunkCount--
	build.IndexVersion.ManifestHash = strings.Repeat("f", 64)
	if err := ValidateIndexBuild(build); err == nil {
		t.Fatal("manifest hash mismatch accepted")
	}
}

func TestValidateIndexTransitionCommandChecksFailureSemanticsAndVersion(t *testing.T) {
	t.Parallel()

	current := validIndexVersion(nil)
	ready := IndexTransition{WorkspaceID: current.WorkspaceID, IndexVersionID: current.ID, ExpectedVersion: current.Version, Status: IndexStatusReady, At: current.UpdatedAt.Add(time.Minute)}
	if err := ValidateIndexTransitionCommand(current, ready); err != nil {
		t.Fatalf("ready transition error = %v", err)
	}

	failed := ready
	failed.Status = IndexStatusFailed
	if err := ValidateIndexTransitionCommand(current, failed); err == nil {
		t.Fatal("failed transition without failure code accepted")
	}
	failed.FailureCode = "EMBEDDING_PROVIDER_UNAVAILABLE"
	if err := ValidateIndexTransitionCommand(current, failed); err != nil {
		t.Fatalf("failed transition error = %v", err)
	}
	failed.ExpectedVersion++
	err := ValidateIndexTransitionCommand(current, failed)
	if err == nil {
		t.Fatal("stale transition accepted")
	}
	assertDomainError(t, err, foundation.ErrorVersionConflict, ErrorCodeIndexTransitionInvalid)
}

func TestSameIndexBuildBindingDetectsDifferentIdempotentRequest(t *testing.T) {
	t.Parallel()

	index := validIndexVersion(nil)
	manifest, hash, err := CanonicalizeManifest(index.WorkspaceID, index.ID, validManifest(index.WorkspaceID, index.ID))
	if err != nil {
		t.Fatal(err)
	}
	index.ManifestHash = hash
	left := IndexBuild{IndexVersion: index, Manifest: manifest}
	right := left
	right.IndexVersion.ID = "20000000-0000-4000-8000-000000000099"
	right.IndexVersion.CreatedAt = right.IndexVersion.CreatedAt.Add(time.Hour)
	right.Manifest = append([]ManifestChunk(nil), left.Manifest...)
	for manifestIndex := range right.Manifest {
		right.Manifest[manifestIndex].IndexVersionID = right.IndexVersion.ID
	}
	if !SameIndexBuildBinding(left, right) {
		t.Fatal("record identity fields changed exact request binding")
	}
	right.IndexVersion.FusionConfig = json.RawMessage(`{"k": 60, "method": "rrf"}`)
	if !SameIndexBuildBinding(left, right) {
		t.Fatal("semantically identical jsonb fusion config was treated as different binding")
	}
	for _, equivalentNumber := range []string{"1", "1.0", "1e0"} {
		right = left
		right.IndexVersion.FusionConfig = json.RawMessage(`{"method":"rrf","k":` + equivalentNumber + `}`)
		leftWithOne := left
		leftWithOne.IndexVersion.FusionConfig = json.RawMessage(`{"method":"rrf","k":1}`)
		if !SameIndexBuildBinding(leftWithOne, right) {
			t.Fatalf("jsonb-equivalent number %s was treated as a different binding", equivalentNumber)
		}
	}
	largeLeft := left
	largeRight := left
	largeLeft.IndexVersion.FusionConfig = json.RawMessage(`{"method":"rrf","k":9007199254740992}`)
	largeRight.IndexVersion.FusionConfig = json.RawMessage(`{"method":"rrf","k":9007199254740993}`)
	if SameIndexBuildBinding(largeLeft, largeRight) {
		t.Fatal("different large json numbers were treated as the same binding")
	}
	right = left
	right.IndexVersion.TokenizerVersion = "v2"
	if SameIndexBuildBinding(left, right) {
		t.Fatal("different tokenizer binding accepted as replay")
	}
}

func TestSameIndexBuildBindingIncludesSourceManifestIdentity(t *testing.T) {
	t.Parallel()
	left := IndexBuild{IndexVersion: validIndexVersion(nil)}
	right := left
	count := int64(1)
	right.IndexVersion.SourceManifestHash = strings.Repeat("a", 64)
	right.IndexVersion.ExpectedSourceCount = &count
	right.IndexVersion.ProcessingContract = testProcessingContract()
	if SameIndexBuildBinding(left, right) {
		t.Fatal("different source manifest binding accepted as replay")
	}
}

func testProcessingContract() *ProcessingContract {
	return &ProcessingContract{
		ParserID: "goldmark", ParserVersion: "v1", ParserConfigHash: strings.Repeat("b", 64),
		ChunkStrategyVersion: "structure-v1", SchemaVersion: "v1",
	}
}

func validIndexVersion(embeddingID *foundation.ID) IndexVersion {
	degraded := []DegradedCapability(nil)
	if embeddingID == nil {
		degraded = []DegradedCapability{DegradedVector}
	}
	return IndexVersion{
		ID:                   "20000000-0000-4000-8000-000000000001",
		WorkspaceID:          "20000000-0000-4000-8000-000000000002",
		EmbeddingVersionID:   embeddingID,
		TokenizerID:          "simple",
		TokenizerVersion:     "v1",
		TokenizerConfigHash:  strings.Repeat("b", 64),
		FusionConfig:         json.RawMessage(`{"method":"rrf","k":60}`),
		SourceSnapshotRef:    "ingestion-snapshot:2026-07-18T01:00:00Z",
		ManifestHash:         strings.Repeat("c", 64),
		ExpectedChunkCount:   2,
		IdempotencyKey:       "index-build-1",
		Status:               IndexStatusBuilding,
		DegradedCapabilities: degraded,
		Version:              1,
		CreatedAt:            time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC),
		UpdatedAt:            time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC),
	}
}
