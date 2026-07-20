// Package retrieval 将 Retrieval Application 的 Search 与 EvidenceReference seam 适配为 Agent Port。
package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	errorCodeAdapterUnavailable = "AGENT_RETRIEVAL_ADAPTER_UNAVAILABLE"
	errorCodeResultInvalid      = "AGENT_RETRIEVAL_ADAPTER_RESULT_INVALID"
)

type searcher interface {
	Search(context.Context, retrievaldomain.SearchRequest) (retrievaldomain.SearchResult, error)
}

type evidenceReference interface {
	OpenCitationEvidenceBatch(context.Context, []retrievaldomain.CitationReferenceQuery) ([]retrievalapplication.OpenedCitationEvidence, error)
}

// Adapter 复用 Retrieval Search 和 EvidenceReferenceService，不实现检索或文件读取算法。
type Adapter struct {
	searcher  searcher
	reference evidenceReference
}

// NewAdapter 创建 Retrieval Adapter；两个 Application seam 缺一不可。
func NewAdapter(search searcher, reference evidenceReference) (*Adapter, error) {
	if nilDependency(search) || nilDependency(reference) {
		return nil, unavailable("retrieval search and evidence reference are required")
	}
	return &Adapter{searcher: search, reference: reference}, nil
}

// Retrieve 使用 Retrieval Hybrid Search，并把每个返回 Provenance 显式展开为唯一 Citation。
func (adapter *Adapter) Retrieve(ctx context.Context, request agentapplication.RetrievalRequest) (agentapplication.RetrievalBatch, error) {
	if adapter == nil || nilDependency(adapter.searcher) {
		return agentapplication.RetrievalBatch{}, unavailable("retrieval adapter is unavailable")
	}
	if err := agentapplication.ValidateRetrievalRequest(request); err != nil {
		return agentapplication.RetrievalBatch{}, err
	}
	scoped, err := adapter.Search(ctx, retrievaldomain.SearchRequest{
		WorkspaceID: request.WorkspaceID,
		Query:       request.Query,
		Mode:        retrievaldomain.SearchModeHybrid,
		Limit:       request.Limit,
	})
	if err != nil {
		return agentapplication.RetrievalBatch{}, err
	}
	return scoped.RetrievalBatch, nil
}

// Search 执行完整的 scoped Retrieval 请求，严格校验原始结果并展开每个 Provenance。
func (adapter *Adapter) Search(ctx context.Context, request retrievaldomain.SearchRequest) (agentapplication.ScopedRetrievalResult, error) {
	if adapter == nil || nilDependency(adapter.searcher) {
		return agentapplication.ScopedRetrievalResult{}, unavailable("retrieval adapter is unavailable")
	}
	canonical, err := retrievaldomain.CanonicalizeSearchRequest(request)
	if err != nil {
		return agentapplication.ScopedRetrievalResult{}, err
	}
	result, err := adapter.searcher.Search(ctx, canonical)
	if err != nil {
		return agentapplication.ScopedRetrievalResult{}, err
	}
	if err := retrievaldomain.ValidateSearchResult(canonical, result); err != nil {
		return agentapplication.ScopedRetrievalResult{}, invalidResult("retrieval search returned an invalid result", err)
	}

	batch, err := expandRetrievalBatch(result)
	if err != nil {
		return agentapplication.ScopedRetrievalResult{}, err
	}
	return agentapplication.ScopedRetrievalResult{SearchResult: result, RetrievalBatch: batch}, nil
}

func expandRetrievalBatch(result retrievaldomain.SearchResult) (agentapplication.RetrievalBatch, error) {
	batch := agentapplication.RetrievalBatch{
		WorkspaceID:        result.WorkspaceID,
		IndexVersionID:     result.IndexVersionID,
		EmbeddingVersionID: cloneID(result.EmbeddingVersionID),
		Items:              make([]agentapplication.RetrievedEvidence, 0, len(result.Items)),
	}
	totalProvenance := 0
	for _, item := range result.Items {
		totalProvenance += len(item.Provenances)
		batch.Truncated = batch.Truncated || item.ProvenanceTruncated
	}
	batch.Truncated = batch.Truncated || totalProvenance > agentapplication.MaxRetrievedEvidence
	for _, item := range result.Items {
		for _, provenance := range item.Provenances {
			if len(batch.Items) == agentapplication.MaxRetrievedEvidence {
				batch.Truncated = true
				break
			}
			citation := agentdomain.Citation{
				ID:              citationID(result.WorkspaceID, result.IndexVersionID, item.ChunkID, provenance.SourceVersionID, item.Span.ID),
				WorkspaceID:     result.WorkspaceID,
				IndexVersionID:  result.IndexVersionID,
				ChunkID:         item.ChunkID,
				SourceVersionID: provenance.SourceVersionID,
				SourceSpanID:    item.Span.ID,
			}
			if err := citation.Validate(); err != nil {
				return agentapplication.RetrievalBatch{}, invalidResult("retrieval evidence cannot form a citation", err)
			}
			batch.Items = append(batch.Items, agentapplication.RetrievedEvidence{
				Citation: citation, SearchExcerpt: item.Snippet, CapturedAt: provenance.CapturedAt.UTC(),
			})
		}
		if len(batch.Items) == agentapplication.MaxRetrievedEvidence {
			break
		}
	}
	return batch, nil
}

// Open 只按 Citation 身份调用 EvidenceReferenceService，不接受或读取路径。
func (adapter *Adapter) Open(ctx context.Context, citation agentdomain.Citation) (agentapplication.OpenedEvidence, error) {
	opened, err := adapter.OpenBatch(ctx, []agentdomain.Citation{citation})
	if err != nil {
		return agentapplication.OpenedEvidence{}, err
	}
	if len(opened) != 1 || opened[0].Citation != citation {
		return agentapplication.OpenedEvidence{}, invalidResult("opened citation batch returned an invalid single result", nil)
	}
	return opened[0], nil
}

// OpenBatch 单批验证完整 Citation tuple，并返回与输入顺序一致的服务端 excerpt。
func (adapter *Adapter) OpenBatch(ctx context.Context, citations []agentdomain.Citation) ([]agentapplication.OpenedEvidence, error) {
	if adapter == nil || nilDependency(adapter.reference) {
		return nil, unavailable("retrieval evidence reference is unavailable")
	}
	if len(citations) == 0 || len(citations) > agentapplication.MaxRetrievedEvidence {
		return nil, invalidResult("citation batch count is invalid", nil)
	}
	queries := make([]retrievaldomain.CitationReferenceQuery, len(citations))
	byQuery := make(map[retrievaldomain.CitationReferenceQuery]agentdomain.Citation, len(citations))
	for index, citation := range citations {
		if err := citation.Validate(); err != nil {
			return nil, err
		}
		query := retrievaldomain.CitationReferenceQuery{
			WorkspaceID: citation.WorkspaceID, IndexVersionID: citation.IndexVersionID, ChunkID: citation.ChunkID,
			SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
		}
		if _, duplicate := byQuery[query]; duplicate {
			return nil, invalidResult("citation batch contains duplicate identities", nil)
		}
		queries[index], byQuery[query] = query, citation
	}
	views, err := adapter.reference.OpenCitationEvidenceBatch(ctx, queries)
	if err != nil {
		return nil, err
	}
	if len(views) != len(citations) {
		return nil, invalidResult("opened citation batch is incomplete", nil)
	}
	openedByQuery := make(map[retrievaldomain.CitationReferenceQuery]agentapplication.OpenedEvidence, len(views))
	for _, item := range views {
		citation, exists := byQuery[item.Query]
		if !exists || item.View.Reference.SourceVersion.WorkspaceID != citation.WorkspaceID ||
			item.View.Reference.SourceVersion.SourceVersionID != citation.SourceVersionID || item.View.Reference.Span.ID != citation.SourceSpanID ||
			item.View.Excerpt == "" || !utf8.ValidString(item.View.Excerpt) {
			return nil, invalidResult("opened source span does not match citation identity", nil)
		}
		if _, duplicate := openedByQuery[item.Query]; duplicate {
			return nil, invalidResult("opened citation batch contains duplicates", nil)
		}
		openedByQuery[item.Query] = agentapplication.OpenedEvidence{Citation: citation, Excerpt: item.View.Excerpt}
	}
	result := make([]agentapplication.OpenedEvidence, len(citations))
	for index, query := range queries {
		opened, exists := openedByQuery[query]
		if !exists {
			return nil, invalidResult("opened citation batch is incomplete", nil)
		}
		result[index] = opened
	}
	return result, nil
}

func citationID(workspaceID, indexVersionID, chunkID, sourceVersionID, spanID foundation.ID) string {
	payload := string(workspaceID) + "\x00" + string(indexVersionID) + "\x00" + string(chunkID) + "\x00" +
		string(sourceVersionID) + "\x00" + string(spanID)
	digest := sha256.Sum256([]byte(payload))
	return "cite-" + hex.EncodeToString(digest[:])
}

func cloneID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func unavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeAdapterUnavailable, false, errors.New(message))
}

func invalidResult(message string, cause error) error {
	if cause == nil {
		cause = errors.New(message)
	}
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeResultInvalid, false, cause)
}

var _ agentapplication.RetrievalPort = (*Adapter)(nil)
var _ agentapplication.ScopedRetrievalPort = (*Adapter)(nil)
