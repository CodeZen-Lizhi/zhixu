package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateSnapshotRegressionAcceptsExactFTSOnlySnapshotAndHashesDeterministically(t *testing.T) {
	command, observation := validSnapshotRegressionFixture()

	first, err := ValidateSnapshotRegression(command, observation)
	if err != nil {
		t.Fatalf("ValidateSnapshotRegression() error = %v", err)
	}
	second, err := ValidateSnapshotRegression(command, observation)
	if err != nil {
		t.Fatalf("ValidateSnapshotRegression(replay) error = %v", err)
	}
	if first.Code != SnapshotStructureRegressionV1 || first.Hash == "" || first.Hash != second.Hash {
		t.Fatalf("regression result = %#v, replay = %#v", first, second)
	}
	if len(first.Hash) != 64 {
		t.Fatalf("hash length = %d", len(first.Hash))
	}
	observation.Index.FusionConfig = []byte(`{"k":60,"method":"rrf"}`)
	reordered, err := ValidateSnapshotRegression(command, observation)
	if err != nil || reordered.Hash != first.Hash {
		t.Fatalf("semantic JSON reorder = %#v, %v", reordered, err)
	}
}

func TestValidateSnapshotRegressionRejectsEveryStructuralMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SnapshotRegressionCommand, *SnapshotRegressionObservation)
	}{
		{"target source missing", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.TargetManifestSource.SourceID = "a0000000-0000-4000-8000-000000000099"
		}},
		{"target version mismatch", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.TargetManifestSource.SourceVersionID = "a0000000-0000-4000-8000-000000000099"
		}},
		{"result hash mismatch", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.TargetSourceResultHash = strings.Repeat("9", 64)
		}},
		{"source hash mismatch", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.SourceManifestHash = strings.Repeat("8", 64)
		}},
		{"source count mismatch", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.SourceManifestCount++
		}},
		{"chunk hash mismatch", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.ChunkManifestHash = strings.Repeat("7", 64)
		}},
		{"chunk missing", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.MissingChunkCount = 1
		}},
		{"chunk extra", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.ExtraChunkCount = 1
		}},
		{"included source attempt invalid", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.InvalidIncludedSourceCount = 1
		}},
		{"hybrid index", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			embeddingID := foundation.ID("a0000000-0000-4000-8000-000000000098")
			observation.Index.EmbeddingVersionID = &embeddingID
		}},
		{"vector not disabled", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.BuildStatus.VectorDisabledCount = 0
			observation.BuildStatus.VectorPendingCount = 2
		}},
		{"vector degradation missing", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.Index.DegradedCapabilities = nil
			observation.BuildStatus.DegradedCapabilities = nil
		}},
		{"lexical incomplete", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.BuildStatus.LexicalReadyCount = 1
			observation.BuildStatus.LexicalPendingCount = 1
		}},
		{"index not building", func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.Index.Status = IndexStatusReady
			observation.BuildStatus.IndexStatus = IndexStatusReady
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, observation := validSnapshotRegressionFixture()
			test.mutate(&command, &observation)
			_, err := ValidateSnapshotRegression(command, observation)
			assertSnapshotRegressionFailure(t, err)
		})
	}
}

func TestSnapshotRegressionHashChangesWhenVerifiedFactChanges(t *testing.T) {
	command, observation := validSnapshotRegressionFixture()
	base, err := ValidateSnapshotRegression(command, observation)
	if err != nil {
		t.Fatal(err)
	}

	mutations := []func(*SnapshotRegressionCommand, *SnapshotRegressionObservation){
		func(command *SnapshotRegressionCommand, _ *SnapshotRegressionObservation) {
			command.DeliveryID = "a0000000-0000-4000-8000-000000000088"
		},
		func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.Index.Version++
			observation.BuildStatus.IndexVersion++
		},
		func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.BuildStatus.ExcludedSourceCount--
			observation.BuildStatus.IncludedSourceCount++
		},
		func(_ *SnapshotRegressionCommand, observation *SnapshotRegressionObservation) {
			observation.Index.ProcessingContract.ParserID = "goldmark-next"
		},
	}
	for index, mutate := range mutations {
		changedCommand, changedObservation := validSnapshotRegressionFixture()
		mutate(&changedCommand, &changedObservation)
		changed, err := ValidateSnapshotRegression(changedCommand, changedObservation)
		if err != nil {
			t.Fatalf("mutation %d error = %v", index, err)
		}
		if changed.Hash == base.Hash {
			t.Fatalf("mutation %d did not change hash %s", index, base.Hash)
		}
	}
}

func validSnapshotRegressionFixture() (SnapshotRegressionCommand, SnapshotRegressionObservation) {
	now := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	workspaceID := foundation.ID("a0000000-0000-4000-8000-000000000001")
	indexID := foundation.ID("a0000000-0000-4000-8000-000000000002")
	targetSourceID := foundation.ID("a0000000-0000-4000-8000-000000000003")
	targetVersionID := foundation.ID("a0000000-0000-4000-8000-000000000004")
	projectionID := foundation.ID("a0000000-0000-4000-8000-000000000005")
	sourceCount := int64(2)
	index := IndexVersion{
		ID: indexID, WorkspaceID: workspaceID,
		TokenizerID: "postgres-simple", TokenizerVersion: "v1", TokenizerConfigHash: strings.Repeat("1", 64),
		FusionConfig: []byte(`{"method":"rrf","k":60}`), SourceSnapshotRef: "reindex-v1:event",
		ManifestHash: strings.Repeat("2", 64), ExpectedChunkCount: 2,
		SourceManifestHash: strings.Repeat("3", 64), ExpectedSourceCount: &sourceCount,
		ProcessingContract: &ProcessingContract{
			ParserID: "goldmark", ParserVersion: "v1", ParserConfigHash: strings.Repeat("4", 64),
			ChunkStrategyVersion: "structure-v1", SchemaVersion: "v1",
		},
		IdempotencyKey: "reindex:event", Status: IndexStatusBuilding,
		DegradedCapabilities: []DegradedCapability{DegradedVector}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	status := BuildStatus{
		WorkspaceID: workspaceID, IndexVersionID: indexID, IndexStatus: IndexStatusBuilding, IndexVersion: 1,
		ExpectedChunkCount: 2, ManifestChunkCount: 2, SourceManifestHash: index.SourceManifestHash,
		ExpectedSourceCount: &sourceCount, SourceManifestCount: 2, IncludedSourceCount: 1, ExcludedSourceCount: 1,
		ProjectionCount: 2, LexicalReadyCount: 2, VectorDisabledCount: 2,
		DegradedCapabilities: []DegradedCapability{DegradedVector},
	}
	command := SnapshotRegressionCommand{
		WorkspaceID: workspaceID, DeliveryID: "a0000000-0000-4000-8000-000000000006",
		TargetSourceID: targetSourceID, TargetSourceVersionID: targetVersionID,
		TargetResultHash: strings.Repeat("5", 64), TargetParseProjectionID: projectionID, IndexVersionID: indexID,
	}
	observation := SnapshotRegressionObservation{
		Index: index,
		TargetManifestSource: ManifestSource{
			IndexVersionID: indexID, WorkspaceID: workspaceID, SourceID: targetSourceID,
			SourceVersionID: targetVersionID, ParseProjectionID: projectionID,
			SelectionStatus: SourceSelectionIncluded, CreatedAt: now,
		},
		TargetSourceResultHash: command.TargetResultHash,
		SourceManifestHash:     index.SourceManifestHash, SourceManifestCount: sourceCount,
		ChunkManifestHash: index.ManifestHash, ChunkManifestCount: 2, ExpectedUnionChunkCount: 2,
		BuildStatus: status,
	}
	return command, observation
}

func assertSnapshotRegressionFailure(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation ||
		classified.Code != ErrorCodeSnapshotStructureRegressionFailed || classified.Retryable {
		t.Fatalf("regression error = %#v", err)
	}
}
