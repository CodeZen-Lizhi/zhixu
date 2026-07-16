package domain

import "testing"

func TestBuildCanonicalChunksPreservesAtomicOversizedBlock(t *testing.T) {
	parsed := ParsedDocument{
		ParserVersion: "markdown-1",
		Blocks: []ParsedBlock{{
			SpanType: "code_block", StartByte: 10, EndByte: 30,
			HeadingPath: []string{"API"}, Content: "0123456789", Atomic: true,
		}},
	}
	spans := []SourceSpan{{
		ID:                "10000000-0000-4000-8000-000000000001",
		WorkspaceID:       "10000000-0000-4000-8000-000000000002",
		ParseProjectionID: "10000000-0000-4000-8000-000000000003",
	}}
	result, err := BuildCanonicalChunks(parsed, spans, ChunkOptions{StrategyVersion: "structure-v1", SchemaVersion: "v1", SoftMaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chunks) != 1 || !result.Chunks[0].AtomicOversized || result.Chunks[0].Content != parsed.Blocks[0].Content {
		t.Fatalf("chunks = %#v", result.Chunks)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "ATOMIC_BLOCK_OVERSIZED" {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestBuildCanonicalChunksIsDeterministicAndSkipsEmptyBlocks(t *testing.T) {
	parsed := ParsedDocument{ParserVersion: "text-1", Blocks: []ParsedBlock{{Content: ""}, {Content: "你好", HeadingPath: []string{"标题"}}}}
	spans := []SourceSpan{
		{ID: "20000000-0000-4000-8000-000000000001"},
		{ID: "20000000-0000-4000-8000-000000000002", WorkspaceID: "20000000-0000-4000-8000-000000000003", ParseProjectionID: "20000000-0000-4000-8000-000000000004"},
	}
	options := ChunkOptions{StrategyVersion: "structure-v1", SchemaVersion: "v1", SoftMaxBytes: 1024}
	first, err := BuildCanonicalChunks(parsed, spans, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildCanonicalChunks(parsed, spans, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Chunks) != 1 || first.Chunks[0].Sequence != 0 || first.Chunks[0].ContentHash != second.Chunks[0].ContentHash {
		t.Fatalf("deterministic chunks = %#v / %#v", first.Chunks, second.Chunks)
	}
	if first.Chunks[0].ByteCount != 6 || first.Chunks[0].RuneCount != 2 {
		t.Fatalf("counts = %d/%d", first.Chunks[0].ByteCount, first.Chunks[0].RuneCount)
	}
}
