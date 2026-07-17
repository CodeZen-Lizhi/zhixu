package domain

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateChunkProjectionAcceptsFTSOnlyAndHybridReady(t *testing.T) {
	t.Parallel()

	ftsIndex := validIndexVersion(nil)
	fts := validProjection(ftsIndex)
	if err := ValidateChunkProjection(ftsIndex, nil, fts); err != nil {
		t.Fatalf("FTS projection error = %v", err)
	}

	embedding := validEmbeddingVersion()
	hybridIndex := validIndexVersion(&embedding.ID)
	hybrid := validProjection(hybridIndex)
	hybrid.EmbeddingVersionID = &embedding.ID
	hybrid.VectorStatus = VectorStatusReady
	hybrid.Embedding = []float32{0.1, 0.2, 0.3}
	if err := ValidateChunkProjection(hybridIndex, &embedding, hybrid); err != nil {
		t.Fatalf("hybrid projection error = %v", err)
	}
}

func TestValidateChunkProjectionRejectsInvalidVectorValues(t *testing.T) {
	t.Parallel()

	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	tests := []struct {
		name   string
		vector []float32
	}{
		{name: "empty", vector: nil},
		{name: "wrong dimensions", vector: []float32{1, 2}},
		{name: "nan", vector: []float32{1, float32(math.NaN()), 2}},
		{name: "infinite", vector: []float32{1, float32(math.Inf(1)), 2}},
		{name: "zero norm", vector: []float32{0, 0, 0}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection := validProjection(index)
			projection.EmbeddingVersionID = &embedding.ID
			projection.VectorStatus = VectorStatusReady
			projection.Embedding = test.vector
			err := ValidateChunkProjection(index, &embedding, projection)
			if err == nil {
				t.Fatal("invalid ready vector accepted")
			}
			assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeProjectionInvalid)
		})
	}
}

func TestValidateChunkProjectionEnforcesIndexAndStatusBindings(t *testing.T) {
	t.Parallel()

	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	valid := validProjection(index)
	valid.EmbeddingVersionID = &embedding.ID
	valid.VectorStatus = VectorStatusPending

	tests := []struct {
		name   string
		mutate func(*ChunkProjection)
	}{
		{name: "wrong workspace", mutate: func(p *ChunkProjection) { p.WorkspaceID = "other" }},
		{name: "wrong index", mutate: func(p *ChunkProjection) { p.IndexVersionID = "other" }},
		{name: "wrong embedding version", mutate: func(p *ChunkProjection) { id := foundation.ID("other"); p.EmbeddingVersionID = &id }},
		{name: "ready lexical missing search vector", mutate: func(p *ChunkProjection) { p.SearchVector = "" }},
		{name: "disabled hybrid vector", mutate: func(p *ChunkProjection) { p.VectorStatus = VectorStatusDisabled }},
		{name: "ready vector missing value", mutate: func(p *ChunkProjection) { p.VectorStatus = VectorStatusReady }},
		{name: "negative token count", mutate: func(p *ChunkProjection) { p.TokenCount = -1 }},
		{name: "unknown lexical status", mutate: func(p *ChunkProjection) { p.LexicalStatus = "future" }},
		{name: "unknown vector status", mutate: func(p *ChunkProjection) { p.VectorStatus = "future" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection := valid

			test.mutate(&projection)
			err := ValidateChunkProjection(index, &embedding, projection)
			if err == nil {
				t.Fatal("invalid projection accepted")
			}
		})
	}
}

func TestValidateChunkProjectionRequiresStableFailureCode(t *testing.T) {
	t.Parallel()

	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	projection := validProjection(index)
	projection.EmbeddingVersionID = &embedding.ID
	projection.VectorStatus = VectorStatusSkippedOversized
	if err := ValidateChunkProjection(index, &embedding, projection); err == nil {
		t.Fatal("skipped vector without failure code accepted")
	}
	projection.FailureCode = "VECTOR_INPUT_OVERSIZED"
	if err := ValidateChunkProjection(index, &embedding, projection); err != nil {
		t.Fatalf("skipped vector error = %v", err)
	}
}

func TestValidateVectorProjectionBatchAcceptsReadySkippedAndFailed(t *testing.T) {
	t.Parallel()

	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	tests := []struct {
		name  string
		write VectorProjectionWrite
	}{
		{name: "ready", write: VectorProjectionWrite{ChunkID: "41000000-0000-4000-8000-000000000001", Embedding: []float32{1, 0, 0}, TokenCount: 3, VectorStatus: VectorStatusReady}},
		{name: "skipped oversized", write: VectorProjectionWrite{ChunkID: "41000000-0000-4000-8000-000000000002", TokenCount: 4096, VectorStatus: VectorStatusSkippedOversized, FailureCode: "VECTOR_INPUT_OVERSIZED"}},
		{name: "failed", write: VectorProjectionWrite{ChunkID: "41000000-0000-4000-8000-000000000003", TokenCount: 3, VectorStatus: VectorStatusFailed, FailureCode: "EMBEDDING_PROVIDER_FAILED"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := validVectorProjectionBatch(index, embedding, test.write)
			if err := ValidateVectorProjectionBatch(index, embedding, batch); err != nil {
				t.Fatalf("ValidateVectorProjectionBatch() error = %v", err)
			}
		})
	}

	projectionType := reflect.TypeOf(VectorProjectionWrite{})
	for _, forbidden := range []string{"SearchVector", "LexicalStatus"} {
		if _, exists := projectionType.FieldByName(forbidden); exists {
			t.Fatalf("VectorProjectionWrite leaked lexical field %q", forbidden)
		}
	}
}

func TestValidateVectorProjectionBatchRejectsPendingAndDisabled(t *testing.T) {
	t.Parallel()

	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	for _, status := range []VectorStatus{VectorStatusPending, VectorStatusDisabled} {
		batch := validVectorProjectionBatch(index, embedding, VectorProjectionWrite{
			ChunkID: "41000000-0000-4000-8000-000000000001", TokenCount: 1, VectorStatus: status,
		})
		if err := ValidateVectorProjectionBatch(index, embedding, batch); err == nil {
			t.Errorf("vector status %q accepted", status)
		}
	}
}

func TestValidateVectorProjectionBatchRejectsInvalidReadyVector(t *testing.T) {
	t.Parallel()

	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	tests := []struct {
		name   string
		vector []float32
	}{
		{name: "empty", vector: nil},
		{name: "wrong dimensions", vector: []float32{1, 2}},
		{name: "nan", vector: []float32{1, float32(math.NaN()), 2}},
		{name: "infinite", vector: []float32{1, float32(math.Inf(1)), 2}},
		{name: "zero norm", vector: []float32{0, 0, 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := validVectorProjectionBatch(index, embedding, VectorProjectionWrite{
				ChunkID: "41000000-0000-4000-8000-000000000001", Embedding: test.vector, TokenCount: 1, VectorStatus: VectorStatusReady,
			})
			err := ValidateVectorProjectionBatch(index, embedding, batch)
			if err == nil {
				t.Fatal("invalid ready vector accepted")
			}
			assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeProjectionInvalid)
		})
	}
}

func TestValidateVectorProjectionBatchRejectsDuplicateAndFailurePayloadConflicts(t *testing.T) {
	t.Parallel()

	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	valid := VectorProjectionWrite{ChunkID: "41000000-0000-4000-8000-000000000001", Embedding: []float32{1, 0, 0}, TokenCount: 1, VectorStatus: VectorStatusReady}
	batch := validVectorProjectionBatch(index, embedding, valid, valid)
	if err := ValidateVectorProjectionBatch(index, embedding, batch); err == nil {
		t.Fatal("duplicate vector write accepted")
	}

	tests := []VectorProjectionWrite{
		{ChunkID: valid.ChunkID, Embedding: valid.Embedding, TokenCount: 1, VectorStatus: VectorStatusReady, FailureCode: "UNEXPECTED"},
		{ChunkID: valid.ChunkID, TokenCount: 1, VectorStatus: VectorStatusSkippedOversized},
		{ChunkID: valid.ChunkID, TokenCount: 1, VectorStatus: VectorStatusFailed},
		{ChunkID: valid.ChunkID, Embedding: valid.Embedding, TokenCount: 1, VectorStatus: VectorStatusFailed, FailureCode: "EMBEDDING_PROVIDER_FAILED"},
	}
	for _, write := range tests {
		batch = validVectorProjectionBatch(index, embedding, write)
		if err := ValidateVectorProjectionBatch(index, embedding, batch); err == nil {
			t.Errorf("conflicting vector payload accepted: %#v", write)
		}
	}
}

func TestValidateVectorProjectionBatchRejectsStaleBindingTimeAndToken(t *testing.T) {
	t.Parallel()

	embedding := validEmbeddingVersion()
	index := validIndexVersion(&embedding.ID)
	write := VectorProjectionWrite{ChunkID: "41000000-0000-4000-8000-000000000001", Embedding: []float32{1, 0, 0}, TokenCount: 1, VectorStatus: VectorStatusReady}
	tests := []struct {
		name   string
		mutate func(*IndexVersion, *EmbeddingVersion, *VectorProjectionBatch)
	}{
		{name: "invalid index", mutate: func(index *IndexVersion, _ *EmbeddingVersion, _ *VectorProjectionBatch) {
			index.ManifestHash = "invalid"
		}},
		{name: "FTS-only index", mutate: func(index *IndexVersion, _ *EmbeddingVersion, _ *VectorProjectionBatch) {
			index.EmbeddingVersionID = nil
			index.DegradedCapabilities = []DegradedCapability{DegradedVector}
		}},
		{name: "wrong workspace", mutate: func(_ *IndexVersion, _ *EmbeddingVersion, batch *VectorProjectionBatch) { batch.WorkspaceID = "other" }},
		{name: "wrong index", mutate: func(_ *IndexVersion, _ *EmbeddingVersion, batch *VectorProjectionBatch) {
			batch.IndexVersionID = "other"
		}},
		{name: "wrong batch embedding", mutate: func(_ *IndexVersion, _ *EmbeddingVersion, batch *VectorProjectionBatch) {
			batch.EmbeddingVersionID = "other"
		}},
		{name: "wrong embedding record", mutate: func(_ *IndexVersion, embedding *EmbeddingVersion, _ *VectorProjectionBatch) { embedding.ID = "other" }},
		{name: "stale version", mutate: func(_ *IndexVersion, _ *EmbeddingVersion, batch *VectorProjectionBatch) { batch.ExpectedIndexVersion++ }},
		{name: "not building", mutate: func(index *IndexVersion, _ *EmbeddingVersion, _ *VectorProjectionBatch) {
			index.Status = IndexStatusReady
		}},
		{name: "zero time", mutate: func(_ *IndexVersion, _ *EmbeddingVersion, batch *VectorProjectionBatch) { batch.At = time.Time{} }},
		{name: "stale time", mutate: func(index *IndexVersion, _ *EmbeddingVersion, batch *VectorProjectionBatch) {
			batch.At = index.UpdatedAt.Add(-time.Nanosecond)
		}},
		{name: "empty batch", mutate: func(_ *IndexVersion, _ *EmbeddingVersion, batch *VectorProjectionBatch) {
			batch.Projections = nil
		}},
		{name: "missing chunk", mutate: func(_ *IndexVersion, _ *EmbeddingVersion, batch *VectorProjectionBatch) {
			batch.Projections[0].ChunkID = ""
		}},
		{name: "negative token", mutate: func(_ *IndexVersion, _ *EmbeddingVersion, batch *VectorProjectionBatch) {
			batch.Projections[0].TokenCount = -1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateIndex := index
			candidateEmbedding := embedding
			batch := validVectorProjectionBatch(candidateIndex, candidateEmbedding, write)
			test.mutate(&candidateIndex, &candidateEmbedding, &batch)
			if err := ValidateVectorProjectionBatch(candidateIndex, candidateEmbedding, batch); err == nil {
				t.Fatal("invalid vector batch accepted")
			}
		})
	}
}

func validVectorProjectionBatch(index IndexVersion, embedding EmbeddingVersion, projections ...VectorProjectionWrite) VectorProjectionBatch {
	return VectorProjectionBatch{
		WorkspaceID:          index.WorkspaceID,
		IndexVersionID:       index.ID,
		EmbeddingVersionID:   embedding.ID,
		ExpectedIndexVersion: index.Version,
		Projections:          projections,
		At:                   index.UpdatedAt.Add(time.Minute),
	}
}

func validProjection(index IndexVersion) ChunkProjection {
	return ChunkProjection{
		IndexVersionID: index.ID,
		ChunkID:        "40000000-0000-4000-8000-000000000001",
		WorkspaceID:    index.WorkspaceID,
		SearchVector:   "'hello':1A 'world':2B",
		TokenCount:     2,
		LexicalStatus:  LexicalStatusReady,
		VectorStatus:   VectorStatusDisabled,
		CreatedAt:      time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC),
	}
}
