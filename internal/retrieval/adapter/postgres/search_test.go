package postgres

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestAppendSearchFilterBuildsStrictSharedPredicatesInStableOrder(t *testing.T) {
	from := time.Date(2026, 7, 19, 1, 0, 0, 0, time.FixedZone("offset", 8*60*60))
	before := from.Add(time.Hour)
	filter := domain.SearchFilter{
		SourceIDs:        []foundation.ID{searchTestID(1)},
		SourceVersionIDs: []foundation.ID{searchTestID(2)},
		PathPrefixes:     []string{"docs/api", "notes/100%"},
		CapturedAtFrom:   &from,
		CapturedAtBefore: &before,
	}
	arguments := []any{"workspace", "index", "query", int32(10), domain.MaxEvidenceProvenance}
	actual := appendSearchFilter(&arguments, filter)
	for _, predicate := range []string{
		"source_manifest.source_id = ANY($6::uuid[])",
		"source_manifest.source_version_id = ANY($7::uuid[])",
		"unnest($8::text[])",
		"source_record.original_location = path_filter.prefix",
		"left(source_record.original_location, length(path_filter.prefix) + 1) = path_filter.prefix || '/'",
		"source_version.captured_at >= $9",
		"source_version.captured_at < $10",
	} {
		if !strings.Contains(actual, predicate) {
			t.Fatalf("filter SQL missing %q:\n%s", predicate, actual)
		}
	}
	if len(arguments) != 10 {
		t.Fatalf("argument count=%d", len(arguments))
	}
	if captured, ok := arguments[8].(time.Time); !ok || !captured.Equal(from.UTC()) || captured.Location() != time.UTC {
		t.Fatalf("from argument=%#v", arguments[8])
	}
	if captured, ok := arguments[9].(time.Time); !ok || !captured.Equal(before.UTC()) || captured.Location() != time.UTC {
		t.Fatalf("before argument=%#v", arguments[9])
	}
	if strings.Contains(actual, "LIKE path_filter.prefix || '%'") {
		t.Fatal("path filter regressed to adjacent-name or wildcard matching")
	}
}

func TestVectorDistanceExpressionUsesOnlyFixedOperators(t *testing.T) {
	tests := []struct {
		metric   domain.DistanceMetric
		operator string
		ok       bool
	}{
		{metric: domain.DistanceCosine, operator: "<=>", ok: true},
		{metric: domain.DistanceInnerProduct, operator: "<#>", ok: true},
		{metric: domain.DistanceEuclidean, operator: "<->", ok: true},
		{metric: domain.DistanceMetric("cosine; DROP TABLE retrieval.index_version"), ok: false},
	}
	for _, test := range tests {
		expression, ok := vectorDistanceExpression(test.metric, 384)
		if ok != test.ok {
			t.Fatalf("metric %q ok=%v", test.metric, ok)
		}
		if test.ok && expression != "projection.embedding::vector(384) "+test.operator+" $4::vector(384)" {
			t.Fatalf("metric %q expression=%q", test.metric, expression)
		}
		if !test.ok && expression != "" {
			t.Fatalf("unknown metric expression=%q", expression)
		}
	}
}

func TestCandidateSQLSharesActiveIncludedAndChunkPredicates(t *testing.T) {
	lexicalSQL := fmt.Sprintf(lexicalCandidateSQL, "", searchSnippetCharacterLimit, searchRerankCharacterLimit)
	vectorSQL := fmt.Sprintf(vectorCandidateSQL, "", "projection.embedding::vector(3) <=> $4::vector(3)", 3,
		"", "projection.embedding::vector(3) <=> $4::vector(3)",
		searchSnippetCharacterLimit, searchRerankCharacterLimit)
	shared := []string{
		"WHERE workspace_id=$1 AND id=$2",
		"status='active'",
		"source_manifest.selection_status='included'",
		"source_record.removed_at IS NULL",
		"chunk.status='active'",
		"projection.lexical_status='ready'",
		"ORDER BY filtered_provenance.source_id,filtered_provenance.source_version_id,filtered_provenance.relative_path",
	}
	for _, predicate := range shared {
		if !strings.Contains(lexicalSQL, predicate) || !strings.Contains(vectorSQL, predicate) {
			t.Fatalf("shared predicate %q is not present in both candidate SQL templates", predicate)
		}
	}
	if !strings.Contains(lexicalSQL, "websearch_to_tsquery('simple',$3)") ||
		!strings.Contains(lexicalSQL, "chunk.content % $3") {
		t.Fatal("lexical SQL does not combine websearch FTS and trigram")
	}
	for _, predicate := range []string{
		"embedding_version_id=$3", "projection.vector_status='ready'", "projection.embedding_version_id=$3",
		"projection.embedding IS NOT NULL", "vector_dims(projection.embedding)=3", "CROSS JOIN LATERAL",
		"ORDER BY projection.embedding::vector(3) <=> $4::vector(3),projection.chunk_id", "LIMIT $5",
	} {
		if !strings.Contains(vectorSQL, predicate) {
			t.Fatalf("vector SQL missing %q", predicate)
		}
	}
	for name, query := range map[string]string{"lexical": lexicalSQL, "vector": vectorSQL} {
		limited := strings.Index(query, "limited AS MATERIALIZED")
		provenance := strings.Index(query, "ranked_provenance AS MATERIALIZED")
		if limited < 0 || provenance < 0 || limited > provenance {
			t.Fatalf("%s SQL aggregates provenance before limiting Chunk ranks", name)
		}
	}
}

func TestTruncateSearchTextPreservesUTF8AndByteLimits(t *testing.T) {
	value := strings.Repeat("知", domain.MaxEvidenceSnippetBytes)
	truncated := truncateSearchText(value, domain.MaxEvidenceSnippetBytes)
	if len(truncated) > domain.MaxEvidenceSnippetBytes || !strings.HasPrefix(value, truncated) || truncated == "" {
		t.Fatalf("truncated bytes=%d", len(truncated))
	}
	if got := truncateSearchText("short", domain.MaxEvidenceSnippetBytes); got != "short" {
		t.Fatalf("short value=%q", got)
	}
}

func searchTestID(ordinal int) foundation.ID {
	return foundation.ID("b0000000-0000-4000-8000-" + strings.Repeat("0", 11) + string(rune('0'+ordinal)))
}
