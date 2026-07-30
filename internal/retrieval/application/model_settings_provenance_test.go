package application

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestRegisterEmbeddingOwnsModelSettingsRevision(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	service := newTestService(t, store)
	revision := int64(0)
	_, err := service.RegisterEmbedding(context.Background(), RegisterEmbeddingRequest{
		Provider: "openai", AdapterName: "compatible", AdapterVersion: "v1", Model: "embed",
		Dimensions: 3, Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine,
		ConfigHash: hashA, ModelSettingsRevision: &revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision = 9
	if store.registered.ModelSettingsRevision == nil || *store.registered.ModelSettingsRevision != 0 {
		t.Fatalf("registered revision=%v", store.registered.ModelSettingsRevision)
	}
}

func TestIndexEntryPointsDeriveEmbeddingModelSettingsRevision(t *testing.T) {
	t.Parallel()
	revision := int64(7)
	embedding := validEmbedding(testIDs[1])
	embedding.ModelSettingsRevision = &revision

	t.Run("manifest build", func(t *testing.T) {
		store := &fakeStore{embedding: embedding}
		service := newTestService(t, store)
		_, err := service.BeginIndex(context.Background(), BeginIndexRequest{
			WorkspaceID: testWorkspaceID, EmbeddingVersionID: &embedding.ID,
			TokenizerID: "simple", TokenizerVersion: "v1", TokenizerConfigHash: hashA,
			FusionConfig: json.RawMessage(`{}`), SourceSnapshotRef: "source:managed", IdempotencyKey: "managed-build",
		})
		if err != nil {
			t.Fatal(err)
		}
		assertDerivedRevision(t, store.begun.IndexVersion.ModelSettingsRevision, revision)
	})

	t.Run("workspace snapshot", func(t *testing.T) {
		store := &fakeStore{embedding: embedding}
		service := newTestService(t, store)
		fusion, err := domain.CanonicalRRFConfig(domain.RRFConfig{
			SchemaVersion: domain.RRFFusionSchemaVersionV1, Method: domain.FusionMethodRRF, K: 60,
			LexicalCandidateLimit: 20, VectorCandidateLimit: 20, FusedCandidateLimit: 20, RerankCandidateLimit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.BeginWorkspaceSnapshot(context.Background(), BeginWorkspaceSnapshotRequest{
			WorkspaceID: testWorkspaceID, EmbeddingVersionID: &embedding.ID,
			TargetSourceID:          "71000000-0000-4000-8000-000000000011",
			TargetSourceVersionID:   "71000000-0000-4000-8000-000000000012",
			TargetParseProjectionID: "71000000-0000-4000-8000-000000000013",
			TokenizerID:             "simple", TokenizerVersion: "v1", TokenizerConfigHash: hashA,
			FusionConfig: fusion, SourceSnapshotRef: "reindex-v1:managed", IdempotencyKey: "managed-snapshot",
			ProcessingContract: domain.ProcessingContract{
				ParserID: "goldmark", ParserVersion: "v1", ParserConfigHash: hashB,
				ChunkStrategyVersion: "structure-v1", SchemaVersion: "v1",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertDerivedRevision(t, store.snapshot.IndexVersion.ModelSettingsRevision, revision)
	})
}

func assertDerivedRevision(t *testing.T, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("derived revision=%v want=%d", got, want)
	}
}
