package domain

import (
	"strings"
	"testing"
	"time"
)

func TestValidateVectorBuildPageRequiresStableHybridInputs(t *testing.T) {
	t.Parallel()
	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	command := VectorBuildPageCommand{WorkspaceID: index.WorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: index.Version, Limit: 2, MaxInputBytes: 16, MaxBatchInputBytes: 32}
	page := VectorBuildPage{Index: index, EmbeddingVersion: embedding, Inputs: []VectorBuildInput{
		{ChunkID: "41000000-0000-4000-8000-000000000001", ContentHash: strings.Repeat("a", 64), InputBytes: 3, Sequence: 0, TokenCount: 1, CachedEmbedding: []float32{1, 0, 0}},
		{ChunkID: "41000000-0000-4000-8000-000000000002", ContentHash: strings.Repeat("b", 64), Content: "two", InputBytes: 3, Sequence: 1, TokenCount: 1},
	}, HasMore: true}
	if err := ValidateVectorBuildPage(command, page); err != nil {
		t.Fatal(err)
	}
	page.Inputs[1].Sequence = 0
	page.Inputs[1].ChunkID = page.Inputs[0].ChunkID
	if err := ValidateVectorBuildPage(command, page); err == nil {
		t.Fatal("duplicate or unstable page accepted")
	}
}

func TestValidateVectorBuildPageRejectsExcessiveLoadedInputBytes(t *testing.T) {
	t.Parallel()
	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	command := VectorBuildPageCommand{
		WorkspaceID: index.WorkspaceID, IndexVersionID: index.ID, ExpectedIndexVersion: index.Version,
		Limit: 2, MaxInputBytes: 16, MaxBatchInputBytes: 20,
	}
	page := VectorBuildPage{Index: index, EmbeddingVersion: embedding, Inputs: []VectorBuildInput{
		{ChunkID: "41000000-0000-4000-8000-000000000001", ContentHash: strings.Repeat("a", 64), Content: strings.Repeat("x", 11), InputBytes: 11, Sequence: 0, TokenCount: 1},
		{ChunkID: "41000000-0000-4000-8000-000000000002", ContentHash: strings.Repeat("b", 64), Content: strings.Repeat("y", 11), InputBytes: 11, Sequence: 1, TokenCount: 1},
	}}
	if err := ValidateVectorBuildPage(command, page); err == nil {
		t.Fatal("page exceeding batch input bytes accepted")
	}
}

func TestValidateVectorBuildBatchRejectsConflictingContentReplay(t *testing.T) {
	t.Parallel()
	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	batch := VectorBuildBatch{
		WorkspaceID: index.WorkspaceID, IndexVersionID: index.ID, EmbeddingVersionID: embedding.ID,
		ExpectedIndexVersion: index.Version, At: time.Now().UTC(),
		Writes: []VectorBuildWrite{
			{ChunkID: "41000000-0000-4000-8000-000000000001", ContentHash: strings.Repeat("a", 64), Embedding: []float32{1, 0, 0}, TokenCount: 1, VectorStatus: VectorStatusReady},
			{ChunkID: "41000000-0000-4000-8000-000000000002", ContentHash: strings.Repeat("a", 64), Embedding: []float32{0, 1, 0}, TokenCount: 1, VectorStatus: VectorStatusReady},
		},
	}
	if err := ValidateVectorBuildBatch(index, embedding, batch); err == nil {
		t.Fatal("conflicting cache identity accepted")
	}
	batch.Writes[1].Embedding = []float32{1, 0, 0}
	if err := ValidateVectorBuildBatch(index, embedding, batch); err != nil {
		t.Fatal(err)
	}
}
