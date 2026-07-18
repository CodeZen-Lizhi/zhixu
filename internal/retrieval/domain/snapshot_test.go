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
