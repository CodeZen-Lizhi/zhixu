package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCanonicalizeManifestIsStableAcrossInputOrderAndRecordIdentity(t *testing.T) {
	t.Parallel()

	workspaceID := foundation.ID("30000000-0000-4000-8000-000000000001")
	indexID := foundation.ID("30000000-0000-4000-8000-000000000002")
	manifest := validManifest(workspaceID, indexID)
	reversed := []ManifestChunk{manifest[1], manifest[0]}
	first, firstHash, err := CanonicalizeManifest(workspaceID, indexID, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Sequence != 0 || first[1].Sequence != 1 {
		t.Fatalf("manifest not sorted: %#v", first)
	}
	if reversed[0].Sequence != 1 || reversed[1].Sequence != 0 {
		t.Fatal("CanonicalizeManifest mutated caller-owned order")
	}

	otherIndexID := foundation.ID("30000000-0000-4000-8000-000000000099")
	secondInput := validManifest(workspaceID, otherIndexID)
	for index := range secondInput {
		secondInput[index].CreatedAt = secondInput[index].CreatedAt.Add(time.Hour)
	}
	_, secondHash, err := CanonicalizeManifest(workspaceID, otherIndexID, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("same snapshot hashes differ: %q != %q", firstHash, secondHash)
	}
}

func TestCanonicalizeManifestRejectsBindingAndContentConflicts(t *testing.T) {
	t.Parallel()

	workspaceID := foundation.ID("30000000-0000-4000-8000-000000000001")
	indexID := foundation.ID("30000000-0000-4000-8000-000000000002")
	tests := []struct {
		name   string
		mutate func([]ManifestChunk)
	}{
		{name: "wrong workspace", mutate: func(chunks []ManifestChunk) { chunks[0].WorkspaceID = "other" }},
		{name: "wrong index", mutate: func(chunks []ManifestChunk) { chunks[0].IndexVersionID = "other" }},
		{name: "duplicate chunk", mutate: func(chunks []ManifestChunk) { chunks[1].ChunkID = chunks[0].ChunkID }},
		{name: "negative sequence", mutate: func(chunks []ManifestChunk) { chunks[0].Sequence = -1 }},
		{name: "invalid content hash", mutate: func(chunks []ManifestChunk) { chunks[0].ContentHash = "hash" }},
		{name: "missing parser version", mutate: func(chunks []ManifestChunk) { chunks[0].ParserVersion = "" }},
		{name: "missing created at", mutate: func(chunks []ManifestChunk) { chunks[0].CreatedAt = time.Time{} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest(workspaceID, indexID)
			test.mutate(manifest)
			_, _, err := CanonicalizeManifest(workspaceID, indexID, manifest)
			if err == nil {
				t.Fatal("invalid manifest accepted")
			}
			assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeManifestInvalid)
		})
	}
}

func validManifest(workspaceID, indexID foundation.ID) []ManifestChunk {
	createdAt := time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC)
	return []ManifestChunk{
		{IndexVersionID: indexID, ChunkID: "30000000-0000-4000-8000-000000000010", WorkspaceID: workspaceID, ContentHash: strings.Repeat("d", 64), Sequence: 0, ParserVersion: "markdown-v1", ChunkStrategyVersion: "structural-v1", SchemaVersion: "v1", CreatedAt: createdAt},
		{IndexVersionID: indexID, ChunkID: "30000000-0000-4000-8000-000000000011", WorkspaceID: workspaceID, ContentHash: strings.Repeat("e", 64), Sequence: 1, ParserVersion: "markdown-v1", ChunkStrategyVersion: "structural-v1", SchemaVersion: "v1", CreatedAt: createdAt},
	}
}
