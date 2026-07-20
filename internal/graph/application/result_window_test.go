package application

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

func TestPaginateResultWindowOwnsSliceAndResponseLossReplay(t *testing.T) {
	codec := mustGraphCursorCodec(t, 'w')
	hash, _ := hashCanonicalCursorValue("request")
	request := ResultWindowRequest{QueryKind: QueryKindGlobal, WorkspaceID: graphServiceID(90), CanonicalRequestHash: hash, Limit: 2}
	window := []string{"a", "b", "c", "d", "e"}
	first, err := PaginateResultWindow(codec, request, window)
	if err != nil || len(first.Items) != 2 || first.Complete || first.NextCursor == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	request.Cursor = first.NextCursor
	second, err := PaginateResultWindow(codec, request, window)
	if err != nil || len(second.Items) != 2 || second.Items[0] != "c" {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	replayed, err := PaginateResultWindow(codec, request, window)
	if err != nil || replayed.NextCursor != second.NextCursor || replayed.Fingerprint != second.Fingerprint {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
}

func TestPaginateResultWindowRejectsDriftCrossRequestAndAdapterOverReturn(t *testing.T) {
	codec := mustGraphCursorCodec(t, 'w')
	hash, _ := hashCanonicalCursorValue("request")
	request := ResultWindowRequest{QueryKind: QueryKindRelationEvidence, WorkspaceID: graphServiceID(90), CanonicalRequestHash: hash, Limit: 1}
	first, err := PaginateResultWindow(codec, request, []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	request.Cursor = first.NextCursor
	_, err = PaginateResultWindow(codec, request, []int{1, 3})
	assertClassifiedCursorError(t, err, foundation.ErrorVersionConflict, graphdomain.ErrorCodeCursorStale)
	request.CanonicalRequestHash, _ = hashCanonicalCursorValue("other")
	_, err = PaginateResultWindow(codec, request, []int{1, 2})
	assertClassifiedCursorError(t, err, foundation.ErrorInvalidInput, graphdomain.ErrorCodeCursorInvalid)

	request.Cursor = ""
	window := make([]int, MaxResultWindowItems+1)
	_, err = PaginateResultWindow(codec, request, window)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != graphdomain.ErrorCodeProjectionInconsistent {
		t.Fatalf("over-return error=%v", err)
	}
}
