package capacity

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestDefaultSpecLocksM10Capacity(t *testing.T) {
	spec, err := DefaultSpec("")
	if err != nil {
		t.Fatal(err)
	}
	if spec.ChunkCount != 500_000 || spec.RelationCount != 500_000 || RelationNodeCount(spec) != 100_000 {
		t.Fatalf("unexpected M10 capacity spec: %#v nodes=%d", spec, RelationNodeCount(spec))
	}
	if DefaultGraphP95Limit != 1500*time.Millisecond || DefaultRetrievalP95Limit != 2*time.Second || DefaultFrontendFPSFloor != 45 {
		t.Fatal("capacity budgets drifted")
	}
}

func TestSpecRejectsManifestAlgorithmDrift(t *testing.T) {
	spec, err := DefaultSpec("algorithm-drift-test")
	if err != nil {
		t.Fatal(err)
	}
	spec.GraphAlgorithm = "unrecorded-topology"
	if err := spec.Validate(); err == nil {
		t.Fatal("unrecorded graph algorithm was accepted")
	}
}

func TestStableRecordsAreCanonicalAndReplayable(t *testing.T) {
	spec, err := DefaultSpec("capacity-test-v1")
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := ChunkAt(spec, spec.ChunkCount-1)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, parseErr := foundation.ParseID(chunk.ID); parseErr != nil || string(parsed) != chunk.ID {
		t.Fatalf("chunk id=%q parseErr=%v", chunk.ID, parseErr)
	}
	if chunk.ContentHash == "" || chunk.ByteCount != int64(len(chunk.Content)) || chunk.Status != "active" {
		t.Fatalf("unexpected chunk=%#v", chunk)
	}
	replayed, err := ChunkAt(spec, spec.ChunkCount-1)
	if err != nil || replayed != chunk {
		t.Fatalf("chunk replay=%#v err=%v", replayed, err)
	}

	relation, err := RelationAt(spec, spec.RelationCount-1)
	if err != nil {
		t.Fatal(err)
	}
	if relation.SourceID == relation.TargetID || relation.Relation != "IMPACTS" || relation.Status != "CONFIRMED" {
		t.Fatalf("unexpected relation=%#v", relation)
	}
	for _, id := range []string{relation.ID, relation.SourceID, relation.TargetID} {
		parsed, parseErr := foundation.ParseID(id)
		if parseErr != nil || string(parsed) != id {
			t.Fatalf("relation id=%q parseErr=%v", id, parseErr)
		}
	}
}

func TestRelationTopologyAvoidsDuplicateEdgesInReferenceWindow(t *testing.T) {
	spec, err := DefaultSpec("relation-window-v1")
	if err != nil {
		t.Fatal(err)
	}
	spec.RelationCount = 5_000
	seen := make(map[string]struct{}, spec.RelationCount)
	for ordinal := int64(0); ordinal < spec.RelationCount; ordinal++ {
		relation, relationErr := RelationAt(spec, ordinal)
		if relationErr != nil {
			t.Fatal(relationErr)
		}
		key := relation.SourceID + "\x00" + relation.TargetID
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate edge at ordinal=%d", ordinal)
		}
		seen[key] = struct{}{}
	}
}

func TestManifestAndJSONLStayStreamingAndDeterministic(t *testing.T) {
	spec, err := DefaultSpec("stream-test-v1")
	if err != nil {
		t.Fatal(err)
	}
	spec.ChunkCount, spec.RelationCount = 3, 2
	var first, second bytes.Buffer
	if err := WriteManifest(&first, spec); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(&second, spec); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() || !json.Valid(first.Bytes()) {
		t.Fatalf("manifest is not deterministic JSON:\n%s", first.String())
	}
	var chunks bytes.Buffer
	count, err := WriteJSONL(&chunks, spec, "chunks")
	if err != nil || count != 3 || strings.Count(chunks.String(), "\n") != 3 {
		t.Fatalf("chunk JSONL count=%d err=%v payload=%q", count, err, chunks.String())
	}
	var relations bytes.Buffer
	count, err = WriteJSONL(&relations, spec, "relations")
	if err != nil || count != 2 || strings.Count(relations.String(), "\n") != 2 {
		t.Fatalf("relation JSONL count=%d err=%v payload=%q", count, err, relations.String())
	}
}

func TestPercentileNearestRank(t *testing.T) {
	values := []time.Duration{10 * time.Millisecond, time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond, 2 * time.Millisecond}
	p95, err := PercentileNearestRank(values, 95)
	if err != nil || p95 != 10*time.Millisecond {
		t.Fatalf("p95=%s err=%v", p95, err)
	}
	if _, err := PercentileNearestRank(nil, 95); err == nil {
		t.Fatal("empty percentile values were accepted")
	}
}

func BenchmarkGenerateCapacityRecords(b *testing.B) {
	spec, err := DefaultSpec("benchmark-capacity-v1")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for iteration := 0; iteration < b.N; iteration++ {
		for ordinal := int64(0); ordinal < spec.ChunkCount; ordinal++ {
			if _, err := ChunkAt(spec, ordinal); err != nil {
				b.Fatal(err)
			}
		}
		for ordinal := int64(0); ordinal < spec.RelationCount; ordinal++ {
			if _, err := RelationAt(spec, ordinal); err != nil {
				b.Fatal(err)
			}
		}
	}
}
