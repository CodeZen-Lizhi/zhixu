package application

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	searchServiceUnavailableCode = "RETRIEVAL_SEARCH_SERVICE_UNAVAILABLE"
	semanticUnavailableCode      = "RETRIEVAL_SEMANTIC_UNAVAILABLE"
	vectorUnavailableCode        = "RETRIEVAL_VECTOR_UNAVAILABLE"
	rerankUnavailableCode        = "RETRIEVAL_RERANK_UNAVAILABLE"
)

// SearchStore 隐藏 Active Index 读取和两路有界候选查询实现。
type SearchStore interface {
	// LoadActiveSearchIndex 只返回指定 Workspace 的当前 Active Index 事实。
	LoadActiveSearchIndex(context.Context, foundation.ID) (SearchIndex, error)
	// SearchLexical 使用统一过滤器返回按 Chunk 聚合的稳定词法候选。
	SearchLexical(context.Context, LexicalSearchQuery) ([]domain.SearchCandidate, error)
	// SearchVector 使用持久 Embedding Version 的受限距离度量返回稳定向量候选。
	SearchVector(context.Context, VectorSearchQuery) ([]domain.SearchCandidate, error)
}

// QueryEmbedder 是查询时使用的单批 Embedding Port；现有批量 Embedder Adapter 可直接实现。
type QueryEmbedder interface {
	// Contract 返回必须与 Active Index Embedding Version 一致的运行时契约。
	Contract() domain.EmbeddingContract
	// Embed 按输入顺序返回同数量向量；查询编排固定传入单元素批次。
	Embed(context.Context, EmbedRequest) (EmbedResult, error)
}

// SearchIndex 是 Store 返回的 Active Index、可选 Embedding 与已解码 RRF 绑定。
// Fusion 为 nil 仅表示历史 FTS-only Index 没有 strict RRF v1 配置。
type SearchIndex struct {
	Index            domain.IndexVersion
	EmbeddingVersion *domain.EmbeddingVersion
	Fusion           *domain.RRFConfig
}

// LexicalSearchQuery 是传给 Store 的规范化词法候选查询。
type LexicalSearchQuery struct {
	WorkspaceID    foundation.ID
	IndexVersionID foundation.ID
	Query          string
	Filter         domain.SearchFilter
	Limit          int32
}

// VectorSearchQuery 是传给 Store 的规范化 exact-vector 候选查询。
type VectorSearchQuery struct {
	WorkspaceID      foundation.ID
	IndexVersionID   foundation.ID
	EmbeddingVersion domain.EmbeddingVersion
	QueryEmbedding   []float32
	Filter           domain.SearchFilter
	Limit            int32
}

// SearchService 编排 Active-only Keyword、Semantic 与 Hybrid 检索。
type SearchService struct {
	store         SearchStore
	queryEmbedder QueryEmbedder
	reranker      Reranker
}

// NewSearchService 创建检索应用服务；Embedder/Reranker 可为空以支持显式能力降级。
func NewSearchService(store SearchStore, queryEmbedder QueryEmbedder, reranker Reranker) (*SearchService, error) {
	if nilDispatcherDependency(store) {
		return nil, searchDependencyError("search store is unavailable")
	}
	if nilDispatcherDependency(queryEmbedder) {
		queryEmbedder = nil
	}
	if nilDispatcherDependency(reranker) {
		reranker = nil
	}
	return &SearchService{store: store, queryEmbedder: queryEmbedder, reranker: reranker}, nil
}

// Search 执行统一过滤、双路融合、相邻去重、可选重排和 Evidence v1 映射。
func (service *SearchService) Search(ctx context.Context, request domain.SearchRequest) (domain.SearchResult, error) {
	if service == nil || nilDispatcherDependency(service.store) {
		return domain.SearchResult{}, searchDependencyError("search service is unavailable")
	}
	canonical, err := domain.CanonicalizeSearchRequest(request)
	if err != nil {
		return domain.SearchResult{}, err
	}
	index, err := service.store.LoadActiveSearchIndex(ctx, canonical.WorkspaceID)
	if err != nil {
		return domain.SearchResult{}, err
	}
	if err := validateSearchIndex(canonical.WorkspaceID, index); err != nil {
		return domain.SearchResult{}, err
	}

	switch canonical.Mode {
	case domain.SearchModeKeyword:
		return service.searchKeyword(ctx, canonical, index, nil)
	case domain.SearchModeSemantic:
		return service.searchSemantic(ctx, canonical, index)
	case domain.SearchModeHybrid:
		return service.searchHybrid(ctx, canonical, index)
	default:
		return domain.SearchResult{}, searchConsistency("canonical search mode is unsupported")
	}
}

func (service *SearchService) searchKeyword(
	ctx context.Context,
	request domain.SearchRequest,
	index SearchIndex,
	degradations []domain.SearchDegradation,
) (domain.SearchResult, error) {
	candidates, err := service.store.SearchLexical(ctx, lexicalQuery(request, index.Index.ID, request.Limit))
	if err != nil {
		return domain.SearchResult{}, err
	}
	if err := validateRouteCandidates(index, candidates, domain.SearchRankerLexical, request.Limit); err != nil {
		return domain.SearchResult{}, err
	}
	ranked := rankSingleRoute(candidates, domain.SearchRankerLexical)
	ranked = deduplicateAdjacentCandidates(ranked)
	return buildSearchResult(request, index, domain.SearchModeKeyword, ranked, degradations, "")
}

func (service *SearchService) searchSemantic(ctx context.Context, request domain.SearchRequest, index SearchIndex) (domain.SearchResult, error) {
	if !vectorCapable(index) || service.queryEmbedder == nil {
		return domain.SearchResult{}, semanticUnavailable()
	}
	embedding, err := service.embedQuery(ctx, request.Query, *index.EmbeddingVersion)
	if err != nil {
		return domain.SearchResult{}, err
	}
	limit := index.Fusion.VectorCandidateLimit
	candidates, err := service.store.SearchVector(ctx, vectorQuery(request, index, embedding, limit))
	if err != nil {
		return domain.SearchResult{}, err
	}
	if err := validateRouteCandidates(index, candidates, domain.SearchRankerVector, limit); err != nil {
		return domain.SearchResult{}, err
	}
	ranked := rankSingleRoute(candidates, domain.SearchRankerVector)
	ranked = deduplicateAdjacentCandidates(ranked)
	return buildSearchResult(request, index, domain.SearchModeSemantic, ranked, nil, "")
}

func (service *SearchService) searchHybrid(ctx context.Context, request domain.SearchRequest, index SearchIndex) (domain.SearchResult, error) {
	if !vectorCapable(index) || service.queryEmbedder == nil {
		return service.searchKeyword(ctx, request, index, hybridFallbackDegradations(vectorUnavailableCode, false))
	}

	type routeResult struct {
		candidates []domain.SearchCandidate
		err        error
		degradable bool
	}
	searchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lexicalResult := make(chan routeResult, 1)
	vectorResult := make(chan routeResult, 1)
	go func() {
		candidates, err := service.store.SearchLexical(searchCtx, lexicalQuery(request, index.Index.ID, index.Fusion.LexicalCandidateLimit))
		lexicalResult <- routeResult{candidates: candidates, err: err}
	}()
	go func() {
		embedding, err := service.embedQuery(searchCtx, request.Query, *index.EmbeddingVersion)
		if err == nil {
			candidates, searchErr := service.store.SearchVector(searchCtx, vectorQuery(request, index, embedding, index.Fusion.VectorCandidateLimit))
			vectorResult <- routeResult{candidates: candidates, err: searchErr}
			return
		}
		vectorResult <- routeResult{err: err, degradable: true}
	}()

	lexical := <-lexicalResult
	if lexical.err != nil {
		cancel()
		<-vectorResult
		return domain.SearchResult{}, lexical.err
	}
	if err := validateRouteCandidates(index, lexical.candidates, domain.SearchRankerLexical, index.Fusion.LexicalCandidateLimit); err != nil {
		cancel()
		<-vectorResult
		return domain.SearchResult{}, err
	}
	vector := <-vectorResult
	if vector.err != nil {
		if classified, ok := retryableSearchFailure(vector.err); vector.degradable && ok {
			return buildKeywordFallback(request, index, lexical.candidates, classified.Code, classified.Retryable)
		}
		return domain.SearchResult{}, vector.err
	}
	if err := validateRouteCandidates(index, vector.candidates, domain.SearchRankerVector, index.Fusion.VectorCandidateLimit); err != nil {
		return domain.SearchResult{}, err
	}

	fused, err := fuseSearchCandidates(*index.Fusion, lexical.candidates, vector.candidates)
	if err != nil {
		return domain.SearchResult{}, err
	}
	fused = deduplicateAdjacentCandidates(fused)
	reranked, modelVersion, degradation, err := service.rerankCandidates(ctx, request.Query, fused, index.Fusion.RerankCandidateLimit)
	if err != nil {
		return domain.SearchResult{}, err
	}
	var degradations []domain.SearchDegradation
	if degradation != nil {
		degradations = []domain.SearchDegradation{*degradation}
	}
	return buildSearchResult(request, index, domain.SearchModeHybrid, reranked, degradations, modelVersion)
}

func buildKeywordFallback(
	request domain.SearchRequest,
	index SearchIndex,
	candidates []domain.SearchCandidate,
	code string,
	retryable bool,
) (domain.SearchResult, error) {
	ranked := rankSingleRoute(candidates, domain.SearchRankerLexical)
	ranked = deduplicateAdjacentCandidates(ranked)
	return buildSearchResult(request, index, domain.SearchModeKeyword, ranked, hybridFallbackDegradations(code, retryable), "")
}

func (service *SearchService) embedQuery(ctx context.Context, query string, version domain.EmbeddingVersion) ([]float32, error) {
	if service.queryEmbedder == nil {
		return nil, semanticUnavailable()
	}
	contract := service.queryEmbedder.Contract()
	if err := domain.ValidateEmbeddingContractBinding(contract, version); err != nil {
		return nil, err
	}
	request := EmbedRequest{Inputs: []string{query}}
	if err := ValidateEmbedRequest(contract, request); err != nil {
		return nil, err
	}
	result, err := service.queryEmbedder.Embed(ctx, request)
	if err != nil {
		return nil, err
	}
	values, err := ValidateEmbedResult(contract, request, result)
	if err != nil {
		return nil, err
	}
	return append([]float32(nil), values[0]...), nil
}

func (service *SearchService) rerankCandidates(
	ctx context.Context,
	query string,
	candidates []domain.SearchCandidate,
	limit int32,
) ([]domain.SearchCandidate, string, *domain.SearchDegradation, error) {
	if service.reranker == nil {
		degradation := domain.SearchDegradation{Capability: domain.SearchDegradationRerank, Code: rerankUnavailableCode}
		return candidates, "", &degradation, nil
	}
	count := len(candidates)
	if count > int(limit) {
		count = int(limit)
	}
	if count == 0 {
		return candidates, "", nil, nil
	}
	request := RerankRequest{Query: query, Candidates: make([]RerankCandidate, count)}
	for index := 0; index < count; index++ {
		request.Candidates[index] = RerankCandidate{ChunkID: candidates[index].ChunkID, Text: candidates[index].RerankText}
	}
	if err := validateRerankRequest(request, limit); err != nil {
		return nil, "", nil, err
	}
	result, err := service.reranker.Rerank(ctx, request)
	if err != nil {
		if classified, ok := retryableSearchFailure(err); ok {
			degradation := domain.SearchDegradation{Capability: domain.SearchDegradationRerank, Code: classified.Code, Retryable: classified.Retryable}
			return candidates, "", &degradation, nil
		}
		return nil, "", nil, err
	}
	if err := validateRerankResult(request, result); err != nil {
		return nil, "", nil, err
	}
	byChunk := make(map[foundation.ID]domain.SearchCandidate, count)
	for _, candidate := range candidates[:count] {
		byChunk[candidate.ChunkID] = candidate
	}
	reranked := make([]domain.SearchCandidate, 0, len(candidates))
	for index, item := range result.Items {
		candidate := cloneSearchCandidate(byChunk[item.ChunkID])
		candidate.Rerank = &domain.CandidateStageScore{Rank: int32(index + 1), Score: item.Score}
		reranked = append(reranked, candidate)
	}
	for _, candidate := range candidates[count:] {
		reranked = append(reranked, cloneSearchCandidate(candidate))
	}
	return reranked, result.ModelVersion, nil, nil
}

func validateSearchIndex(workspaceID foundation.ID, value SearchIndex) error {
	if err := domain.ValidateIndexVersion(value.Index); err != nil {
		return searchConsistency("active search index is invalid")
	}
	if !canonicalSearchID(value.Index.ID) || !canonicalSearchID(value.Index.WorkspaceID) ||
		value.Index.WorkspaceID != workspaceID || value.Index.Status != domain.IndexStatusActive {
		return searchConsistency("store returned a non-active or cross-workspace index")
	}
	if value.Index.EmbeddingVersionID != nil && !canonicalSearchID(*value.Index.EmbeddingVersionID) {
		return searchConsistency("active index embedding identity is invalid")
	}
	if value.Fusion != nil {
		if err := domain.ValidateRRFConfig(*value.Fusion); err != nil {
			return searchConsistency("search fusion config is invalid")
		}
		persisted, err := domain.DecodeRRFConfig(value.Index.FusionConfig)
		if err != nil || persisted != *value.Fusion {
			return searchConsistency("search fusion config does not match active index")
		}
	}
	if value.Index.EmbeddingVersionID == nil {
		if value.EmbeddingVersion != nil {
			return searchConsistency("fts-only index returned an embedding version")
		}
		return nil
	}
	if value.Fusion == nil || value.EmbeddingVersion == nil || value.EmbeddingVersion.ID != *value.Index.EmbeddingVersionID {
		return searchConsistency("hybrid index embedding or fusion binding is incomplete")
	}
	if err := domain.ValidateEmbeddingVersion(*value.EmbeddingVersion); err != nil {
		return searchConsistency("active embedding version is invalid")
	}
	if !canonicalSearchID(value.EmbeddingVersion.ID) {
		return searchConsistency("active embedding version identity is invalid")
	}
	return nil
}

func validateRouteCandidates(index SearchIndex, candidates []domain.SearchCandidate, ranker domain.SearchRanker, limit int32) error {
	if len(candidates) > int(limit) {
		return searchConsistency("store candidate count exceeds requested limit")
	}
	seen := make(map[foundation.ID]struct{}, len(candidates))
	for position, candidate := range candidates {
		if err := domain.ValidateSearchCandidate(candidate); err != nil {
			return err
		}
		if candidate.WorkspaceID != index.Index.WorkspaceID || candidate.IndexVersionID != index.Index.ID ||
			!sameOptionalID(candidate.EmbeddingVersionID, index.Index.EmbeddingVersionID) || candidate.Fusion != nil || candidate.Rerank != nil {
			return searchConsistency("store candidate does not match active index or contains application stages")
		}
		switch ranker {
		case domain.SearchRankerLexical:
			if candidate.Lexical == nil || candidate.Lexical.Rank != int32(position+1) || candidate.Vector != nil {
				return searchConsistency("lexical candidate stage is invalid")
			}
		case domain.SearchRankerVector:
			if candidate.Vector == nil || candidate.Vector.Rank != int32(position+1) || candidate.Lexical != nil ||
				candidate.LexicalFTSScore != nil || candidate.LexicalTrigramScore != nil {
				return searchConsistency("vector candidate stage is invalid")
			}
		default:
			return searchConsistency("candidate route is unsupported")
		}
		if _, duplicate := seen[candidate.ChunkID]; duplicate {
			return searchConsistency("store candidate route contains duplicate chunks")
		}
		seen[candidate.ChunkID] = struct{}{}
	}
	return nil
}

func rankSingleRoute(candidates []domain.SearchCandidate, ranker domain.SearchRanker) []domain.SearchCandidate {
	ranked := make([]domain.SearchCandidate, len(candidates))
	for index, candidate := range candidates {
		ranked[index] = cloneSearchCandidate(candidate)
		stage := candidate.Lexical
		if ranker == domain.SearchRankerVector {
			stage = candidate.Vector
		}
		ranked[index].Fusion = &domain.CandidateStageScore{Rank: int32(index + 1), Score: stage.Score}
	}
	return ranked
}

func fuseSearchCandidates(config domain.RRFConfig, lexical, vector []domain.SearchCandidate) ([]domain.SearchCandidate, error) {
	lexicalRanks := make([]domain.RankedCandidate, len(lexical))
	vectorRanks := make([]domain.RankedCandidate, len(vector))
	byChunk := make(map[foundation.ID]domain.SearchCandidate, len(lexical)+len(vector))
	for index, candidate := range lexical {
		lexicalRanks[index] = domain.RankedCandidate{ChunkID: candidate.ChunkID, Rank: candidate.Lexical.Rank, Score: candidate.Lexical.Score}
		byChunk[candidate.ChunkID] = cloneSearchCandidate(candidate)
	}
	for index, candidate := range vector {
		vectorRanks[index] = domain.RankedCandidate{ChunkID: candidate.ChunkID, Rank: candidate.Vector.Rank, Score: candidate.Vector.Score}
		if existing, exists := byChunk[candidate.ChunkID]; exists {
			if !sameSearchCandidateCore(existing, candidate) {
				return nil, searchConsistency("lexical and vector candidate metadata conflict")
			}
			existing.Vector = cloneStage(candidate.Vector)
			byChunk[candidate.ChunkID] = existing
		} else {
			byChunk[candidate.ChunkID] = cloneSearchCandidate(candidate)
		}
	}
	fused, err := domain.FuseRRF(config, lexicalRanks, vectorRanks)
	if err != nil {
		return nil, err
	}
	result := make([]domain.SearchCandidate, len(fused))
	for index, fusedCandidate := range fused {
		candidate := cloneSearchCandidate(byChunk[fusedCandidate.ChunkID])
		candidate.Fusion = &domain.CandidateStageScore{Rank: fusedCandidate.Rank, Score: fusedCandidate.Score}
		result[index] = candidate
	}
	return result, nil
}

func deduplicateAdjacentCandidates(candidates []domain.SearchCandidate) []domain.SearchCandidate {
	kept := make([]domain.SearchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		versions := candidateSourceVersions(candidate)
		duplicate := false
		for _, existing := range kept {
			if candidate.ProvenanceTruncated || existing.ProvenanceTruncated {
				continue
			}
			if adjacentSequence(existing.Sequence, candidate.Sequence) && equalIDs(candidateSourceVersions(existing), versions) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			kept = append(kept, cloneSearchCandidate(candidate))
		}
	}
	return kept
}

func candidateSourceVersions(candidate domain.SearchCandidate) []foundation.ID {
	versions := make([]foundation.ID, 0, len(candidate.Provenances))
	for _, provenance := range candidate.Provenances {
		versions = append(versions, provenance.SourceVersionID)
	}
	sort.Slice(versions, func(left, right int) bool { return versions[left] < versions[right] })
	unique := versions[:0]
	for _, version := range versions {
		if len(unique) == 0 || unique[len(unique)-1] != version {
			unique = append(unique, version)
		}
	}
	return unique
}

func buildSearchResult(
	request domain.SearchRequest,
	index SearchIndex,
	effectiveMode domain.SearchMode,
	candidates []domain.SearchCandidate,
	degradations []domain.SearchDegradation,
	rerankModelVersion string,
) (domain.SearchResult, error) {
	canonicalDegradations, err := domain.NormalizeSearchDegradations(degradations)
	if err != nil {
		return domain.SearchResult{}, err
	}
	count := len(candidates)
	if count > int(request.Limit) {
		count = int(request.Limit)
	}
	items := make([]domain.EvidenceV1, count)
	for position, candidate := range candidates[:count] {
		fusion := cloneStage(candidate.Fusion)
		if fusion == nil {
			return domain.SearchResult{}, searchConsistency("ranked candidate is missing its final stage")
		}
		modelVersion := ""
		if candidate.Rerank != nil {
			modelVersion = rerankModelVersion
		}
		items[position] = domain.EvidenceV1{
			WorkspaceID: candidate.WorkspaceID, IndexVersionID: candidate.IndexVersionID,
			EmbeddingVersionID: cloneOptionalID(candidate.EmbeddingVersionID), ChunkID: candidate.ChunkID,
			ParseProjectionID: candidate.ParseProjectionID, Sequence: candidate.Sequence,
			ContentHash: candidate.ContentHash, HeadingPath: append([]string(nil), candidate.HeadingPath...),
			Span: candidate.Span, Snippet: candidate.Snippet,
			Provenances:         append([]domain.EvidenceProvenance(nil), candidate.Provenances...),
			ProvenanceTruncated: candidate.ProvenanceTruncated,
			Lexical:             cloneStage(candidate.Lexical), Vector: cloneStage(candidate.Vector),
			LexicalFTSScore: cloneFloat(candidate.LexicalFTSScore), LexicalTrigramScore: cloneFloat(candidate.LexicalTrigramScore),
			Fusion: *fusion, Rerank: cloneStage(candidate.Rerank), RerankModelVersion: modelVersion,
		}
	}
	result := domain.SearchResult{
		WorkspaceID: request.WorkspaceID, IndexVersionID: index.Index.ID,
		EmbeddingVersionID: cloneOptionalID(index.Index.EmbeddingVersionID),
		RequestedMode:      request.Mode, EffectiveMode: effectiveMode,
		IndexDegradedCapabilities: append([]domain.DegradedCapability(nil), index.Index.DegradedCapabilities...),
		Degradations:              canonicalDegradations, Items: items,
	}
	if err := domain.ValidateSearchResult(request, result); err != nil {
		return domain.SearchResult{}, err
	}
	return result, nil
}

func vectorCapable(index SearchIndex) bool {
	return index.Index.EmbeddingVersionID != nil && index.EmbeddingVersion != nil && index.Fusion != nil &&
		!domain.HasDegradedCapability(index.Index.DegradedCapabilities, domain.DegradedVector)
}

func lexicalQuery(request domain.SearchRequest, indexVersionID foundation.ID, limit int32) LexicalSearchQuery {
	return LexicalSearchQuery{WorkspaceID: request.WorkspaceID, IndexVersionID: indexVersionID, Query: request.Query, Filter: cloneSearchFilter(request.Filter), Limit: limit}
}

func vectorQuery(request domain.SearchRequest, index SearchIndex, embedding []float32, limit int32) VectorSearchQuery {
	return VectorSearchQuery{
		WorkspaceID: request.WorkspaceID, IndexVersionID: index.Index.ID,
		EmbeddingVersion: *index.EmbeddingVersion, QueryEmbedding: append([]float32(nil), embedding...),
		Filter: cloneSearchFilter(request.Filter), Limit: limit,
	}
}

func cloneSearchFilter(filter domain.SearchFilter) domain.SearchFilter {
	result := filter
	result.SourceIDs = append([]foundation.ID(nil), filter.SourceIDs...)
	result.SourceVersionIDs = append([]foundation.ID(nil), filter.SourceVersionIDs...)
	result.PathPrefixes = append([]string(nil), filter.PathPrefixes...)
	if filter.CapturedAtFrom != nil {
		value := *filter.CapturedAtFrom
		result.CapturedAtFrom = &value
	}
	if filter.CapturedAtBefore != nil {
		value := *filter.CapturedAtBefore
		result.CapturedAtBefore = &value
	}
	return result
}

func cloneSearchCandidate(candidate domain.SearchCandidate) domain.SearchCandidate {
	result := candidate
	result.EmbeddingVersionID = cloneOptionalID(candidate.EmbeddingVersionID)
	result.HeadingPath = append([]string(nil), candidate.HeadingPath...)
	result.Provenances = append([]domain.EvidenceProvenance(nil), candidate.Provenances...)
	result.Lexical = cloneStage(candidate.Lexical)
	result.Vector = cloneStage(candidate.Vector)
	result.Fusion = cloneStage(candidate.Fusion)
	result.Rerank = cloneStage(candidate.Rerank)
	result.LexicalFTSScore = cloneFloat(candidate.LexicalFTSScore)
	result.LexicalTrigramScore = cloneFloat(candidate.LexicalTrigramScore)
	return result
}

func sameSearchCandidateCore(left, right domain.SearchCandidate) bool {
	left = cloneSearchCandidate(left)
	right = cloneSearchCandidate(right)
	left.Lexical, left.Vector, left.Fusion, left.Rerank = nil, nil, nil, nil
	right.Lexical, right.Vector, right.Fusion, right.Rerank = nil, nil, nil, nil
	left.LexicalFTSScore, left.LexicalTrigramScore = nil, nil
	right.LexicalFTSScore, right.LexicalTrigramScore = nil, nil
	return reflect.DeepEqual(left, right)
}

func cloneStage(value *domain.CandidateStageScore) *domain.CandidateStageScore {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneOptionalID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func sameOptionalID(left, right *foundation.ID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func canonicalSearchID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func adjacentSequence(left, right int32) bool {
	return left+1 == right || right+1 == left
}

func equalIDs(left, right []foundation.ID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func retryableSearchFailure(err error) (*foundation.Error, bool) {
	var classified *foundation.Error
	if !errors.As(err, &classified) || !classified.Retryable {
		return nil, false
	}
	if classified.Kind != foundation.ErrorRetryableFailure && classified.Kind != foundation.ErrorDependencyUnavailable {
		return nil, false
	}
	return classified, true
}

func hybridFallbackDegradations(code string, retryable bool) []domain.SearchDegradation {
	return []domain.SearchDegradation{
		{Capability: domain.SearchDegradationVector, Code: code, Retryable: retryable},
		{Capability: domain.SearchDegradationRerank, Code: rerankUnavailableCode},
	}
}

func semanticUnavailable() error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, semanticUnavailableCode, false, errors.New("active index has no usable semantic capability"))
}

func searchDependencyError(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, searchServiceUnavailableCode, false, errors.New(message))
}

func searchConsistency(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeSearchCandidateInvalid, false, errors.New(message))
}
