package retrievalhttp

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestCursorCodecPaginatesStableTopWindow(t *testing.T) {
	codec := mustCursorCodec(t, 'a')
	request := cursorSearchRequest("stable query")
	result := cursorSearchResult(5, "92000000-0000-4000-8000-000000000002")

	first, err := codec.Paginate(request, result, 2, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Result.Items) != 2 || first.Result.Items[0].ChunkID != result.Items[0].ChunkID || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %+v", first)
	}
	if strings.Contains(first.NextCursor, request.Query) || strings.Contains(first.NextCursor, string(request.WorkspaceID)) {
		t.Fatal("cursor leaked query or workspace identity in plaintext")
	}

	second, err := codec.Paginate(request, result, 2, first.NextCursor)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Result.Items) != 2 || second.Result.Items[0].ChunkID != result.Items[2].ChunkID || second.NextCursor == "" {
		t.Fatalf("unexpected second page: %+v", second)
	}

	third, err := codec.Paginate(request, result, 2, second.NextCursor)
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if len(third.Result.Items) != 1 || third.Result.Items[0].ChunkID != result.Items[4].ChunkID || third.NextCursor != "" {
		t.Fatalf("unexpected third page: %+v", third)
	}
}

func TestCursorCodecRejectsTamperCrossRequestAndRestart(t *testing.T) {
	codec := mustCursorCodec(t, 'a')
	request := cursorSearchRequest("stable query")
	result := cursorSearchResult(3, "92000000-0000-4000-8000-000000000002")
	page, err := codec.Paginate(request, result, 1, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}

	tampered := page.NextCursor[:len(page.NextCursor)-1] + "A"
	assertCursorError(t, codec, request, result, tampered, foundation.ErrorInvalidInput, domain.ErrorCodeSearchCursorInvalid)

	changedRequest := request
	changedRequest.Query = "other query"
	assertCursorError(t, codec, changedRequest, result, page.NextCursor, foundation.ErrorInvalidInput, domain.ErrorCodeSearchCursorInvalid)

	restarted := mustCursorCodec(t, 'b')
	assertCursorError(t, restarted, request, result, page.NextCursor, foundation.ErrorInvalidInput, domain.ErrorCodeSearchCursorInvalid)
}

func TestCursorCodecRejectsIndexOrResultDrift(t *testing.T) {
	codec := mustCursorCodec(t, 'a')
	request := cursorSearchRequest("stable query")
	result := cursorSearchResult(3, "92000000-0000-4000-8000-000000000002")
	page, err := codec.Paginate(request, result, 1, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}

	newIndex := cursorSearchResult(3, "92000000-0000-4000-8000-000000000099")
	assertCursorError(t, codec, request, newIndex, page.NextCursor, foundation.ErrorVersionConflict, domain.ErrorCodeSearchCursorStale)

	changedResult := result
	changedResult.Items = append([]domain.EvidenceV1(nil), result.Items...)
	changedResult.Items[1], changedResult.Items[2] = changedResult.Items[2], changedResult.Items[1]
	changedResult.Items[1].Fusion.Rank = 2
	changedResult.Items[2].Fusion.Rank = 3
	changedResult.Items[1].Lexical.Rank = 2
	changedResult.Items[2].Lexical.Rank = 3
	assertCursorError(t, codec, request, changedResult, page.NextCursor, foundation.ErrorVersionConflict, domain.ErrorCodeSearchCursorStale)
}

func TestCursorCodecRejectsInvalidPageLimit(t *testing.T) {
	codec := mustCursorCodec(t, 'a')
	request := cursorSearchRequest("stable query")
	result := cursorSearchResult(1, "92000000-0000-4000-8000-000000000002")
	for _, limit := range []int32{0, domain.MaxSearchLimit + 1} {
		_, err := codec.Paginate(request, result, limit, "")
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != domain.ErrorCodeSearchCursorInvalid {
			t.Fatalf("limit %d: expected cursor invalid, got %v", limit, err)
		}
	}
}

func TestCursorCodecStopsAtExactTopHundredBoundary(t *testing.T) {
	codec := mustCursorCodec(t, 'a')
	request := cursorSearchRequest("bounded query")
	result := cursorSearchResult(int(domain.MaxSearchLimit), "92000000-0000-4000-8000-000000000002")

	first, err := codec.Paginate(request, result, 40, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := codec.Paginate(request, result, 40, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	third, err := codec.Paginate(request, result, 40, second.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Result.Items) != 40 || len(second.Result.Items) != 40 || len(third.Result.Items) != 20 || third.NextCursor != "" {
		t.Fatalf("page sizes=%d/%d/%d final_cursor=%q", len(first.Result.Items), len(second.Result.Items), len(third.Result.Items), third.NextCursor)
	}
}

func TestRandomCursorCodecFailsClosedWhenEntropyUnavailable(t *testing.T) {
	codec, err := newRandomCursorCodec(failingCursorRandom{})
	var classified *foundation.Error
	if codec != nil || !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable || classified.Code != searchCursorUnavailableCode {
		t.Fatalf("codec=%#v err=%v", codec, err)
	}
}

func mustCursorCodec(t *testing.T, fill byte) *CursorCodec {
	t.Helper()
	codec, err := NewCursorCodec(bytesOf(fill, 32))
	if err != nil {
		t.Fatalf("new cursor codec: %v", err)
	}
	return codec
}

func assertCursorError(
	t *testing.T,
	codec *CursorCodec,
	request domain.SearchRequest,
	result domain.SearchResult,
	cursor string,
	kind foundation.ErrorKind,
	code string,
) {
	t.Helper()
	_, err := codec.Paginate(request, result, 1, cursor)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("expected %s/%s, got %v", kind, code, err)
	}
}

func cursorSearchRequest(query string) domain.SearchRequest {
	return domain.SearchRequest{
		WorkspaceID: "92000000-0000-4000-8000-000000000001", Query: query,
		Mode: domain.SearchModeKeyword, Limit: domain.MaxSearchLimit,
		Filter: domain.SearchFilter{PathPrefixes: []string{"docs"}},
	}
}

func cursorSearchResult(count int, indexID foundation.ID) domain.SearchResult {
	items := make([]domain.EvidenceV1, count)
	for position := range items {
		rank := int32(position + 1)
		lexicalScore := float64(count-position) / 10
		ftsScore := lexicalScore / 2
		trigramScore := lexicalScore / 3
		chunkID := foundation.ID(fmt.Sprintf("92000000-0000-4000-8000-%012d", 100+position))
		items[position] = domain.EvidenceV1{
			WorkspaceID: "92000000-0000-4000-8000-000000000001", IndexVersionID: indexID,
			ChunkID: chunkID, ParseProjectionID: "92000000-0000-4000-8000-000000000003",
			Sequence: int32(position), ContentHash: strings.Repeat(fmt.Sprintf("%x", (position%15)+1), 64),
			HeadingPath: []string{"Search"}, Span: domain.EvidenceSpan{
				ID:        foundation.ID(fmt.Sprintf("92000000-0000-4000-8000-%012d", 200+position)),
				StartLine: 1, EndLine: 1, StartByte: int64(position * 10), EndByte: int64(position*10 + 5),
			},
			Snippet: fmt.Sprintf("result-%d", position),
			Provenances: []domain.EvidenceProvenance{{
				SourceID:        foundation.ID(fmt.Sprintf("92000000-0000-4000-8000-%012d", 300+position)),
				SourceVersionID: foundation.ID(fmt.Sprintf("92000000-0000-4000-8000-%012d", 400+position)),
				RelativePath:    fmt.Sprintf("docs/result-%d.md", position), CapturedAt: time.Date(2026, 7, 19, 1, 2, position, 0, time.UTC),
			}},
			Lexical:         &domain.CandidateStageScore{Rank: rank, Score: lexicalScore},
			LexicalFTSScore: &ftsScore, LexicalTrigramScore: &trigramScore,
			Fusion: domain.CandidateStageScore{Rank: rank, Score: 1 / float64(60+rank)},
		}
	}
	return domain.SearchResult{
		WorkspaceID: "92000000-0000-4000-8000-000000000001", IndexVersionID: indexID,
		RequestedMode: domain.SearchModeKeyword, EffectiveMode: domain.SearchModeKeyword, Items: items,
	}
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

type failingCursorRandom struct{}

func (failingCursorRandom) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
