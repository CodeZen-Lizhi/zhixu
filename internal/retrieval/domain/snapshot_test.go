package domain

import (
	"strings"
	"testing"
)

func TestValidateWorkspaceSnapshotCommandRequiresBoundedFTSOnlyContract(t *testing.T) {
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
	if err := ValidateWorkspaceSnapshotCommand(command); err == nil {
		t.Fatal("M6-B snapshot accepted vector configuration")
	}
}
