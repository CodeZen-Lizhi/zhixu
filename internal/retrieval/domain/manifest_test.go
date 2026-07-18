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

func TestCanonicalizeSourceManifestIsStableAndCountsExcludedSources(t *testing.T) {
	t.Parallel()
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	indexID := foundation.ID("10000000-0000-4000-8000-000000000002")
	createdAt := time.Date(2026, 7, 18, 2, 3, 4, 0, time.UTC)
	sources := []ManifestSource{
		{
			IndexVersionID: indexID, WorkspaceID: workspaceID, SourceID: "10000000-0000-4000-8000-000000000020",
			SelectionStatus: SourceSelectionExcluded, ExclusionCode: SourceExclusionNoCurrentSuccessfulProjection, CreatedAt: createdAt,
		},
		{
			IndexVersionID: indexID, WorkspaceID: workspaceID, SourceID: "10000000-0000-4000-8000-000000000010",
			SourceVersionID: "10000000-0000-4000-8000-000000000011", ParseProjectionID: "10000000-0000-4000-8000-000000000012",
			SelectionStatus: SourceSelectionIncluded, CreatedAt: createdAt,
		},
	}
	canonical, manifestHash, excludedCount, err := CanonicalizeSourceManifest(workspaceID, indexID, sources)
	if err != nil {
		t.Fatal(err)
	}
	if canonical[0].SourceID != "10000000-0000-4000-8000-000000000010" || excludedCount != 1 {
		t.Fatalf("canonical=%#v excluded=%d", canonical, excludedCount)
	}
	if manifestHash != "74b65e695d7753845e6573cc08e34820d2e6946e3ae47a1da1e26dd26474efbe" {
		t.Fatalf("hash=%q", manifestHash)
	}
	if sources[0].SelectionStatus != SourceSelectionExcluded {
		t.Fatal("CanonicalizeSourceManifest mutated caller order")
	}

	otherIndexID := foundation.ID("10000000-0000-4000-8000-000000000099")
	for index := range sources {
		sources[index].IndexVersionID = otherIndexID
		sources[index].CreatedAt = sources[index].CreatedAt.Add(time.Hour)
	}
	_, replayHash, _, err := CanonicalizeSourceManifest(workspaceID, otherIndexID, sources)
	if err != nil || replayHash != manifestHash {
		t.Fatalf("replay hash=%q err=%v", replayHash, err)
	}
}

func TestCanonicalizeSourceManifestRejectsInvalidSelectionAndBinding(t *testing.T) {
	t.Parallel()
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	indexID := foundation.ID("10000000-0000-4000-8000-000000000002")
	valid := func() []ManifestSource {
		return []ManifestSource{{
			IndexVersionID: indexID, WorkspaceID: workspaceID, SourceID: "10000000-0000-4000-8000-000000000010",
			SourceVersionID: "10000000-0000-4000-8000-000000000011", ParseProjectionID: "10000000-0000-4000-8000-000000000012",
			SelectionStatus: SourceSelectionIncluded, CreatedAt: time.Date(2026, 7, 18, 2, 3, 4, 0, time.UTC),
		}}
	}
	tests := map[string]func([]ManifestSource){
		"wrong workspace":     func(rows []ManifestSource) { rows[0].WorkspaceID = "other" },
		"wrong index":         func(rows []ManifestSource) { rows[0].IndexVersionID = "other" },
		"missing source":      func(rows []ManifestSource) { rows[0].SourceID = "" },
		"included no version": func(rows []ManifestSource) { rows[0].SourceVersionID = "" },
		"included exclusion":  func(rows []ManifestSource) { rows[0].ExclusionCode = SourceExclusionNoCurrentSuccessfulProjection },
		"excluded with version": func(rows []ManifestSource) {
			rows[0].SelectionStatus = SourceSelectionExcluded
			rows[0].ExclusionCode = SourceExclusionNoCurrentSuccessfulProjection
		},
		"unknown exclusion": func(rows []ManifestSource) {
			rows[0].SelectionStatus = SourceSelectionExcluded
			rows[0].SourceVersionID = ""
			rows[0].ParseProjectionID = ""
			rows[0].ExclusionCode = "UNSUPPORTED"
		},
		"missing created":  func(rows []ManifestSource) { rows[0].CreatedAt = time.Time{} },
		"duplicate source": func(rows []ManifestSource) { rows = append(rows, rows[0]) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			rows := valid()
			if name == "duplicate source" {
				rows = append(rows, rows[0])
			} else {
				mutate(rows)
			}
			if _, _, _, err := CanonicalizeSourceManifest(workspaceID, indexID, rows); err == nil {
				t.Fatal("invalid source manifest accepted")
			}
		})
	}
}

func TestManifestHashersMatchCanonicalFunctionsAcrossPages(t *testing.T) {
	t.Parallel()
	workspaceID := foundation.ID("30000000-0000-4000-8000-000000000001")
	indexID := foundation.ID("30000000-0000-4000-8000-000000000002")
	chunks, wantChunkHash, err := CanonicalizeManifest(workspaceID, indexID, validManifest(workspaceID, indexID))
	if err != nil {
		t.Fatal(err)
	}
	chunkHasher, err := NewManifestHasher(workspaceID, indexID)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		if err := chunkHasher.Add(chunk); err != nil {
			t.Fatal(err)
		}
	}
	chunkHash, chunkCount, err := chunkHasher.Result()
	if err != nil || chunkHash != wantChunkHash || chunkCount != int64(len(chunks)) {
		t.Fatalf("chunk hash=%q count=%d err=%v", chunkHash, chunkCount, err)
	}
	if err := chunkHasher.Add(chunks[0]); err == nil {
		t.Fatal("out-of-order chunk page accepted")
	}

	createdAt := time.Date(2026, 7, 18, 2, 3, 4, 0, time.UTC)
	sources, wantSourceHash, wantExcluded, err := CanonicalizeSourceManifest(workspaceID, indexID, []ManifestSource{
		{IndexVersionID: indexID, WorkspaceID: workspaceID, SourceID: "10000000-0000-4000-8000-000000000020", SelectionStatus: SourceSelectionExcluded, ExclusionCode: SourceExclusionNoCurrentSuccessfulProjection, CreatedAt: createdAt},
		{IndexVersionID: indexID, WorkspaceID: workspaceID, SourceID: "10000000-0000-4000-8000-000000000010", SourceVersionID: "10000000-0000-4000-8000-000000000011", ParseProjectionID: "10000000-0000-4000-8000-000000000012", SelectionStatus: SourceSelectionIncluded, CreatedAt: createdAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceHasher, err := NewSourceManifestHasher(workspaceID, indexID)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if err := sourceHasher.Add(source); err != nil {
			t.Fatal(err)
		}
	}
	sourceHash, sourceCount, excludedCount, err := sourceHasher.Result()
	if err != nil || sourceHash != wantSourceHash || sourceCount != int64(len(sources)) || excludedCount != wantExcluded {
		t.Fatalf("source hash=%q count=%d excluded=%d err=%v", sourceHash, sourceCount, excludedCount, err)
	}
}

func validManifest(workspaceID, indexID foundation.ID) []ManifestChunk {
	createdAt := time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC)
	return []ManifestChunk{
		{IndexVersionID: indexID, ChunkID: "30000000-0000-4000-8000-000000000010", WorkspaceID: workspaceID, ContentHash: strings.Repeat("d", 64), Sequence: 0, ParserVersion: "markdown-v1", ChunkStrategyVersion: "structural-v1", SchemaVersion: "v1", CreatedAt: createdAt},
		{IndexVersionID: indexID, ChunkID: "30000000-0000-4000-8000-000000000011", WorkspaceID: workspaceID, ContentHash: strings.Repeat("e", 64), Sequence: 1, ParserVersion: "markdown-v1", ChunkStrategyVersion: "structural-v1", SchemaVersion: "v1", CreatedAt: createdAt},
	}
}
