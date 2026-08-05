package domain

import (
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateWorkspaceSnapshotCommandSupportsBoundedFTSOnlyAndHybridContracts(t *testing.T) {
	t.Parallel()
	index := validIndexVersion(nil)
	index.ManifestHash = ""
	index.ExpectedChunkCount = 0
	index.SourceSnapshotRef = "reindex-v1:event"
	index.ProcessingContract = &ProcessingContract{
		ParserID: "goldmark", ParserVersion: "v1", ParserConfigHash: strings.Repeat("a", 64),
		ChunkStrategyVersion: "structure-v1", SchemaVersion: "v1",
	}
	command := WorkspaceSnapshotCommand{
		IndexVersion:            index,
		TargetSourceID:          "91000000-0000-4000-8000-000000000001",
		TargetSourceVersionID:   "92000000-0000-4000-8000-000000000001",
		TargetParseProjectionID: "93000000-0000-4000-8000-000000000001",
		PageSize:                DefaultSnapshotPageSize, MaxSources: DefaultSnapshotMaxSources, MaxChunks: DefaultSnapshotMaxChunks,
	}
	if err := ValidateWorkspaceSnapshotCommand(command); err != nil {
		t.Fatal(err)
	}
	command.IndexVersion.SourceSnapshotRef = "source-refresh-v1:capture"
	if err := ValidateWorkspaceSnapshotCommand(command); err != nil {
		t.Fatalf("generic source refresh snapshot rejected: %v", err)
	}
	command.IndexVersion.SourceSnapshotRef = "reindex-v1:event"
	command.MaxChunks = 0
	if err := ValidateWorkspaceSnapshotCommand(command); err == nil {
		t.Fatal("unbounded snapshot accepted")
	}
	command.MaxChunks = DefaultSnapshotMaxChunks
	embeddingID := validEmbeddingVersion().ID
	command.IndexVersion.EmbeddingVersionID = &embeddingID
	command.IndexVersion.DegradedCapabilities = nil
	command.IndexVersion.FusionConfig = mustCanonicalSnapshotRRFConfig(t)
	if err := ValidateWorkspaceSnapshotCommand(command); err != nil {
		t.Fatalf("hybrid snapshot rejected: %v", err)
	}

	command.IndexVersion.DegradedCapabilities = []DegradedCapability{DegradedVector}
	if err := ValidateWorkspaceSnapshotCommand(command); err == nil {
		t.Fatal("hybrid snapshot accepted premature vector degradation")
	}
	command.IndexVersion.DegradedCapabilities = nil
	command.IndexVersion.FusionConfig = []byte(`{"method":"rrf","k":60}`)
	if err := ValidateWorkspaceSnapshotCommand(command); err == nil {
		t.Fatal("hybrid snapshot accepted legacy fusion config")
	}
	command.IndexVersion.FusionConfig = mustCanonicalSnapshotRRFConfig(t)
	emptyEmbeddingID := foundation.ID("")
	command.IndexVersion.EmbeddingVersionID = &emptyEmbeddingID
	if err := ValidateWorkspaceSnapshotCommand(command); err == nil {
		t.Fatal("hybrid snapshot accepted empty embedding identity")
	}
}

func TestValidateWorkspaceSnapshotCommandAcceptsCanonicalGitBatchTargetsAndRemovals(t *testing.T) {
	t.Parallel()
	index := validIndexVersion(nil)
	index.ManifestHash = ""
	index.ExpectedChunkCount = 0
	index.SourceSnapshotRef = "git-fast-forward-v1:10000000-0000-4000-8000-000000000001"
	index.ProcessingContract = &ProcessingContract{
		ParserID: "goldmark", ParserVersion: "v1", ParserConfigHash: strings.Repeat("a", 64),
		ChunkStrategyVersion: "structure-v1", SchemaVersion: "v1",
	}
	command := WorkspaceSnapshotCommand{
		IndexVersion: index,
		Targets: []SnapshotTarget{
			{SourceID: "91000000-0000-4000-8000-000000000001", SourceVersionID: "92000000-0000-4000-8000-000000000001", ParseProjectionID: "93000000-0000-4000-8000-000000000001"},
			{SourceID: "91000000-0000-4000-8000-000000000002", SourceVersionID: "92000000-0000-4000-8000-000000000002", ParseProjectionID: "93000000-0000-4000-8000-000000000002"},
		},
		RemovedSourceIDs: []foundation.ID{"91000000-0000-4000-8000-000000000003"},
		PageSize:         DefaultSnapshotPageSize, MaxSources: DefaultSnapshotMaxSources, MaxChunks: DefaultSnapshotMaxChunks,
	}
	if err := ValidateWorkspaceSnapshotCommand(command); err != nil {
		t.Fatalf("canonical Git batch rejected: %v", err)
	}

	command.Targets[0], command.Targets[1] = command.Targets[1], command.Targets[0]
	if err := ValidateWorkspaceSnapshotCommand(command); err == nil {
		t.Fatal("unsorted batch targets accepted")
	}
	command.Targets[0], command.Targets[1] = command.Targets[1], command.Targets[0]
	command.RemovedSourceIDs = []foundation.ID{command.Targets[0].SourceID}
	if err := ValidateWorkspaceSnapshotCommand(command); err == nil {
		t.Fatal("target/removal overlap accepted")
	}
}

func mustCanonicalSnapshotRRFConfig(t *testing.T) []byte {
	t.Helper()
	encoded, err := CanonicalRRFConfig(RRFConfig{
		SchemaVersion: RRFFusionSchemaVersionV1, Method: FusionMethodRRF, K: 60,
		LexicalCandidateLimit: 200, VectorCandidateLimit: 200, FusedCandidateLimit: 200, RerankCandidateLimit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
