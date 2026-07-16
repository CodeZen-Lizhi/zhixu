// Package retrieval defines project-owned embedding and retrieval contracts for the Eino PoC.
package retrieval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/poc/eino/contract"
)

const operationSearch = "eino_retrieval.search"

// Embedder converts text into vectors without exposing provider-specific types.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Retriever selects candidate documents for a query vector.
type Retriever interface {
	Retrieve(ctx context.Context, query Query) ([]Document, error)
}

// Reranker reorders candidates using project-owned documents.
type Reranker interface {
	Rerank(ctx context.Context, query string, documents []Document, limit int) ([]Document, error)
}

// Query contains the semantic query and its generated embedding.
type Query struct {
	Text      string
	Embedding []float32
	Limit     int
}

// Document is the stable retrieval result observed by project code.
type Document struct {
	ID      string
	Content string
	Score   float64
}

// Pipeline composes the three replaceable adapter contracts.
type Pipeline struct {
	embedder  Embedder
	retriever Retriever
	reranker  Reranker
}

// New creates a retrieval pipeline with explicit dependencies.
func New(embedder Embedder, retriever Retriever, reranker Reranker) (*Pipeline, error) {
	if embedder == nil || retriever == nil || reranker == nil {
		return nil, invalidInput("embedder, retriever and reranker are required")
	}
	return &Pipeline{embedder: embedder, retriever: retriever, reranker: reranker}, nil
}

// Search embeds, retrieves and reranks one bounded query.
func (p *Pipeline) Search(ctx context.Context, text string, limit int) ([]Document, error) {
	if p == nil {
		return nil, nonRetryable(errors.New("retrieval pipeline is not initialized"))
	}
	if strings.TrimSpace(text) == "" {
		return nil, invalidInput("query text is required")
	}
	if limit <= 0 {
		return nil, invalidInput("limit must be greater than zero")
	}

	vectors, err := p.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, classify(err)
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil, nonRetryable(fmt.Errorf("embedder returned %d vectors", len(vectors)))
	}

	documents, err := p.retriever.Retrieve(ctx, Query{Text: text, Embedding: vectors[0], Limit: limit})
	if err != nil {
		return nil, classify(err)
	}
	reranked, err := p.reranker.Rerank(ctx, text, cloneDocuments(documents), limit)
	if err != nil {
		return nil, classify(err)
	}
	if len(reranked) > limit {
		return nil, nonRetryable(fmt.Errorf("reranker returned %d documents for limit %d", len(reranked), limit))
	}
	return cloneDocuments(reranked), nil
}

func cloneDocuments(documents []Document) []Document {
	return append([]Document(nil), documents...)
}

func classify(err error) error {
	var classified *contract.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return &contract.Error{Kind: contract.ErrorNonRetryableFailure, Operation: operationSearch, Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &contract.Error{Kind: contract.ErrorRetryableFailure, Operation: operationSearch, Retryable: true, Cause: err}
	}
	return &contract.Error{Kind: contract.ErrorNonRetryableFailure, Operation: operationSearch, Cause: err}
}

func invalidInput(message string) error {
	return &contract.Error{Kind: contract.ErrorInvalidInput, Operation: operationSearch, Cause: errors.New(message)}
}

func nonRetryable(cause error) error {
	return &contract.Error{Kind: contract.ErrorNonRetryableFailure, Operation: operationSearch, Cause: cause}
}
