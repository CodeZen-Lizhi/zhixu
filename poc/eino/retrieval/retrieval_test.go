package retrieval

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/poc/eino/contract"
)

type fakeEmbedder struct {
	texts   []string
	vectors [][]float32
	err     error
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.texts = append([]string(nil), texts...)
	return f.vectors, f.err
}

type fakeRetriever struct {
	query     Query
	documents []Document
	err       error
}

func (f *fakeRetriever) Retrieve(_ context.Context, query Query) ([]Document, error) {
	f.query = query
	return append([]Document(nil), f.documents...), f.err
}

type fakeReranker struct {
	query     string
	documents []Document
	limit     int
	result    []Document
	err       error
}

func (f *fakeReranker) Rerank(_ context.Context, q string, d []Document, l int) ([]Document, error) {
	f.query = q
	f.documents = append([]Document(nil), d...)
	f.limit = l
	return append([]Document(nil), f.result...), f.err
}

func TestPipelineContract(t *testing.T) {
	embedder := &fakeEmbedder{vectors: [][]float32{{0.25, 0.75}}}
	retriever := &fakeRetriever{documents: []Document{{ID: "a", Content: "first", Score: .5}, {ID: "b", Content: "second", Score: .4}}}
	reranker := &fakeReranker{result: []Document{{ID: "b", Content: "second", Score: .9}, {ID: "a", Content: "first", Score: .8}}}
	pipeline, err := New(embedder, retriever, reranker)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pipeline.Search(context.Background(), "knowledge", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(embedder.texts, []string{"knowledge"}) {
		t.Fatalf("embed input=%v", embedder.texts)
	}
	if retriever.query.Text != "knowledge" || retriever.query.Limit != 2 || !reflect.DeepEqual(retriever.query.Embedding, []float32{.25, .75}) {
		t.Fatalf("retrieval query=%#v", retriever.query)
	}
	if reranker.query != "knowledge" || reranker.limit != 2 || !reflect.DeepEqual(reranker.documents, retriever.documents) {
		t.Fatalf("rerank input=%#v", reranker)
	}
	if !reflect.DeepEqual(got, reranker.result) {
		t.Fatalf("result=%#v", got)
	}
}

func TestPipelineRejectsInvalidInputBeforeAdapters(t *testing.T) {
	embedder := &fakeEmbedder{}
	pipeline, _ := New(embedder, &fakeRetriever{}, &fakeReranker{})
	_, err := pipeline.Search(context.Background(), " ", 1)
	assertKind(t, err, contract.ErrorInvalidInput, false)
	if embedder.texts != nil {
		t.Fatalf("embedder called with %v", embedder.texts)
	}
}

func TestPipelineDefaultsUnknownFailureToNonRetryable(t *testing.T) {
	cause := errors.New("embedding provider unavailable")
	pipeline, _ := New(&fakeEmbedder{err: cause}, &fakeRetriever{}, &fakeReranker{})
	_, err := pipeline.Search(context.Background(), "query", 1)
	assertKind(t, err, contract.ErrorNonRetryableFailure, false)
	if !errors.Is(err, cause) {
		t.Fatalf("error=%v", err)
	}
}

func assertKind(t *testing.T, err error, kind contract.ErrorKind, retryable bool) {
	t.Helper()
	var classified *contract.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error type=%T", err)
	}
	if classified.Kind != kind || classified.Retryable != retryable {
		t.Fatalf("error=%#v", classified)
	}
}
