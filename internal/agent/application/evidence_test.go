package application

import (
	"context"
	"testing"

	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestScopedRetrievalPortReturnsApplicationOwnedResult(t *testing.T) {
	want := ScopedRetrievalResult{
		SearchResult:   retrievaldomain.SearchResult{RequestedMode: retrievaldomain.SearchModeHybrid},
		RetrievalBatch: RetrievalBatch{Truncated: true},
	}
	var port ScopedRetrievalPort = scopedRetrievalFake{result: want}

	got, err := port.Search(context.Background(), retrievaldomain.SearchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.SearchResult.RequestedMode != want.SearchResult.RequestedMode || got.RetrievalBatch.Truncated != want.RetrievalBatch.Truncated {
		t.Fatalf("result=%#v, want=%#v", got, want)
	}
}

type scopedRetrievalFake struct {
	result ScopedRetrievalResult
}

func (fake scopedRetrievalFake) Search(context.Context, retrievaldomain.SearchRequest) (ScopedRetrievalResult, error) {
	return fake.result, nil
}
