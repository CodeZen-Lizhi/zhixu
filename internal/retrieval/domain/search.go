package domain

import (
	"math"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxSearchQueryBytes 限制用户查询文本，避免无界 FTS/Embedding 输入。
	MaxSearchQueryBytes = 8 * 1024
	// MaxSearchLimit 限制一次检索最终返回的 Evidence 数量。
	MaxSearchLimit int32 = 100
	// MaxSearchFilterValues 限制每类离散过滤值数量。
	MaxSearchFilterValues = 100
	// MaxEvidenceProvenance 限制共享 Chunk 展开的可打开来源数量。
	MaxEvidenceProvenance = 8
	// MaxEvidenceSnippetBytes 限制公开 Evidence 片段大小。
	MaxEvidenceSnippetBytes = 4 * 1024
	// MaxRerankTextBytes 限制仅在应用层传给可选 Reranker 的候选正文。
	MaxRerankTextBytes = 16 * 1024
)

// SearchMode 表示用户选择的检索行为。
type SearchMode string

const (
	// SearchModeKeyword 只使用 FTS/trigram。
	SearchModeKeyword SearchMode = "keyword"
	// SearchModeSemantic 只使用当前 Active Embedding Version。
	SearchModeSemantic SearchMode = "semantic"
	// SearchModeHybrid 使用 Lexical、Vector 与 RRF。
	SearchModeHybrid SearchMode = "hybrid"
)

// SearchFilter 是 Lexical/Vector 必须共享的规范化过滤对象。
// CapturedAtFrom/CapturedAtBefore 采用 [from,before) 语义。
type SearchFilter struct {
	SourceIDs        []foundation.ID
	SourceVersionIDs []foundation.ID
	PathPrefixes     []string
	CapturedAtFrom   *time.Time
	CapturedAtBefore *time.Time
}

// SearchRequest 是进入检索 Application 的有界领域请求。
type SearchRequest struct {
	WorkspaceID foundation.ID
	Query       string
	Mode        SearchMode
	Filter      SearchFilter
	Limit       int32
}

// CanonicalizeSearchRequest 校验并返回不修改调用方 slice 的规范请求。
func CanonicalizeSearchRequest(request SearchRequest) (SearchRequest, error) {
	workspaceID, err := canonicalID(request.WorkspaceID)
	if err != nil || !validSearchMode(request.Mode) || request.Limit <= 0 || request.Limit > MaxSearchLimit {
		return SearchRequest{}, invalid(ErrorCodeSearchRequestInvalid, "search workspace, mode, or limit is invalid")
	}
	query := strings.TrimSpace(request.Query)
	if query == "" || len(query) > MaxSearchQueryBytes || !utf8.ValidString(query) {
		return SearchRequest{}, invalid(ErrorCodeSearchRequestInvalid, "search query is empty, oversized, or invalid utf-8")
	}
	sourceIDs, err := canonicalIDList(request.Filter.SourceIDs)
	if err != nil {
		return SearchRequest{}, err
	}
	sourceVersionIDs, err := canonicalIDList(request.Filter.SourceVersionIDs)
	if err != nil {
		return SearchRequest{}, err
	}
	pathPrefixes, err := canonicalPathPrefixes(request.Filter.PathPrefixes)
	if err != nil {
		return SearchRequest{}, err
	}
	from, err := canonicalSearchTime(request.Filter.CapturedAtFrom)
	if err != nil {
		return SearchRequest{}, err
	}
	before, err := canonicalSearchTime(request.Filter.CapturedAtBefore)
	if err != nil {
		return SearchRequest{}, err
	}
	if from != nil && before != nil && !from.Before(*before) {
		return SearchRequest{}, invalid(ErrorCodeSearchRequestInvalid, "search captured time range must be non-empty")
	}
	return SearchRequest{
		WorkspaceID: workspaceID,
		Query:       query,
		Mode:        request.Mode,
		Limit:       request.Limit,
		Filter: SearchFilter{
			SourceIDs: sourceIDs, SourceVersionIDs: sourceVersionIDs, PathPrefixes: pathPrefixes,
			CapturedAtFrom: from, CapturedAtBefore: before,
		},
	}, nil
}

// EvidenceSpan 保存可打开引用的不可变行号和字节范围。
type EvidenceSpan struct {
	ID        foundation.ID
	StartLine int32
	EndLine   int32
	StartByte int64
	EndByte   int64
}

// EvidenceProvenance 保存共享 Chunk 对应的一个 SourceVersion 与受控路径。
type EvidenceProvenance struct {
	SourceID        foundation.ID
	SourceVersionID foundation.ID
	RelativePath    string
	CapturedAt      time.Time
}

// CandidateStageScore 使用 1-based rank 和 finite 原始分数表达阶段结果。
type CandidateStageScore struct {
	Rank  int32
	Score float64
}

// SearchCandidate 是 Store 返回并由 Application 融合的 Chunk 排名身份。
type SearchCandidate struct {
	WorkspaceID         foundation.ID
	IndexVersionID      foundation.ID
	EmbeddingVersionID  *foundation.ID
	ChunkID             foundation.ID
	ParseProjectionID   foundation.ID
	Sequence            int32
	ContentHash         string
	HeadingPath         []string
	Span                EvidenceSpan
	Snippet             string
	RerankText          string
	Provenances         []EvidenceProvenance
	ProvenanceTruncated bool
	Lexical             *CandidateStageScore
	// LexicalFTSScore 保留 PostgreSQL FTS 的原始分数，不与 trigram 混为不可解释值。
	LexicalFTSScore *float64
	// LexicalTrigramScore 保留 pg_trgm similarity 的原始分数。
	LexicalTrigramScore *float64
	Vector              *CandidateStageScore
	Fusion              *CandidateStageScore
	Rerank              *CandidateStageScore
}

// CanonicalizeEvidenceProvenance 校验、排序和去重来源；超过上限必须由 Store 截断并显式标记。
func CanonicalizeEvidenceProvenance(values []EvidenceProvenance) ([]EvidenceProvenance, error) {
	if len(values) == 0 || len(values) > MaxEvidenceProvenance {
		return nil, inconsistent(ErrorCodeSearchCandidateInvalid, "candidate provenance count is invalid")
	}
	canonical := append([]EvidenceProvenance(nil), values...)
	for index := range canonical {
		sourceID, err := canonicalID(canonical[index].SourceID)
		if err != nil || sourceID != canonical[index].SourceID {
			return nil, inconsistent(ErrorCodeSearchCandidateInvalid, "candidate source identity is invalid")
		}
		versionID, err := canonicalID(canonical[index].SourceVersionID)
		if err != nil || versionID != canonical[index].SourceVersionID ||
			!canonicalRelativePath(canonical[index].RelativePath) || canonical[index].CapturedAt.IsZero() {
			return nil, inconsistent(ErrorCodeSearchCandidateInvalid, "candidate provenance binding is invalid")
		}
		canonical[index].CapturedAt = canonical[index].CapturedAt.UTC()
	}
	sort.Slice(canonical, func(left, right int) bool {
		if canonical[left].SourceID != canonical[right].SourceID {
			return canonical[left].SourceID < canonical[right].SourceID
		}
		if canonical[left].SourceVersionID != canonical[right].SourceVersionID {
			return canonical[left].SourceVersionID < canonical[right].SourceVersionID
		}
		return canonical[left].RelativePath < canonical[right].RelativePath
	})
	for index := 1; index < len(canonical); index++ {
		if sameProvenanceIdentity(canonical[index-1], canonical[index]) {
			return nil, inconsistent(ErrorCodeSearchCandidateInvalid, "candidate provenance contains duplicates")
		}
	}
	return canonical, nil
}

// ValidateSearchCandidate 校验 Store 候选的跨层绑定和可解释阶段分数。
func ValidateSearchCandidate(candidate SearchCandidate) error {
	ids := []foundation.ID{
		candidate.WorkspaceID, candidate.IndexVersionID, candidate.ChunkID,
		candidate.ParseProjectionID, candidate.Span.ID,
	}
	for _, value := range ids {
		parsed, err := canonicalID(value)
		if err != nil || parsed != value {
			return inconsistent(ErrorCodeSearchCandidateInvalid, "candidate identity is invalid")
		}
	}
	if candidate.EmbeddingVersionID != nil {
		parsed, err := canonicalID(*candidate.EmbeddingVersionID)
		if err != nil || parsed != *candidate.EmbeddingVersionID {
			return inconsistent(ErrorCodeSearchCandidateInvalid, "candidate embedding identity is invalid")
		}
	}
	if candidate.Sequence < 0 || !isCanonicalHash(candidate.ContentHash) ||
		candidate.Span.StartLine < 1 || candidate.Span.EndLine < candidate.Span.StartLine ||
		candidate.Span.StartByte < 0 || candidate.Span.EndByte < candidate.Span.StartByte {
		return inconsistent(ErrorCodeSearchCandidateInvalid, "candidate chunk or span metadata is invalid")
	}
	if !validHeadingPath(candidate.HeadingPath) || candidate.Snippet == "" ||
		len(candidate.Snippet) > MaxEvidenceSnippetBytes || !utf8.ValidString(candidate.Snippet) ||
		len(candidate.RerankText) > MaxRerankTextBytes || !utf8.ValidString(candidate.RerankText) {
		return inconsistent(ErrorCodeSearchCandidateInvalid, "candidate text metadata is invalid")
	}
	canonical, err := CanonicalizeEvidenceProvenance(candidate.Provenances)
	if err != nil || !equalProvenance(candidate.Provenances, canonical) {
		return inconsistent(ErrorCodeSearchCandidateInvalid, "candidate provenance must be canonical")
	}
	if candidate.Lexical == nil && candidate.Vector == nil {
		return inconsistent(ErrorCodeSearchCandidateInvalid, "candidate must include a lexical or vector stage")
	}
	if candidate.Lexical == nil {
		if candidate.LexicalFTSScore != nil || candidate.LexicalTrigramScore != nil {
			return inconsistent(ErrorCodeSearchCandidateInvalid, "non-lexical candidate cannot carry lexical raw scores")
		}
	} else if candidate.LexicalFTSScore == nil || candidate.LexicalTrigramScore == nil ||
		!finiteScore(*candidate.LexicalFTSScore) || !finiteScore(*candidate.LexicalTrigramScore) {
		return inconsistent(ErrorCodeSearchCandidateInvalid, "lexical candidate raw scores are invalid")
	}
	if candidate.Vector != nil && candidate.EmbeddingVersionID == nil {
		return inconsistent(ErrorCodeSearchCandidateInvalid, "vector candidate requires an embedding version")
	}
	for _, score := range []*CandidateStageScore{candidate.Lexical, candidate.Vector, candidate.Fusion, candidate.Rerank} {
		if score != nil && !validStageScore(*score) {
			return inconsistent(ErrorCodeSearchCandidateInvalid, "candidate stage score is invalid")
		}
	}
	return nil
}

// SearchDegradationCapability 是单次查询的瞬时降级能力，不写入 Index Version。
type SearchDegradationCapability string

const (
	// SearchDegradationVector 表示本次查询未能使用完整 Vector 能力。
	SearchDegradationVector SearchDegradationCapability = "vector"
	// SearchDegradationRerank 表示本次查询返回了 RRF 顺序而未完成 Rerank。
	SearchDegradationRerank SearchDegradationCapability = "rerank"
)

// SearchDegradation 记录单次查询的稳定降级原因。
type SearchDegradation struct {
	Capability SearchDegradationCapability
	Code       string
	Retryable  bool
}

// NormalizeSearchDegradations 校验并按 vector、rerank 返回唯一规范顺序。
func NormalizeSearchDegradations(values []SearchDegradation) ([]SearchDegradation, error) {
	if len(values) == 0 {
		return nil, nil
	}
	byCapability := make(map[SearchDegradationCapability]SearchDegradation, 2)
	for _, value := range values {
		if value.Capability != SearchDegradationVector && value.Capability != SearchDegradationRerank ||
			!isCanonicalText(value.Code) || len(value.Code) > 128 {
			return nil, invalid(ErrorCodeSearchResultInvalid, "search degradation is invalid")
		}
		if existing, ok := byCapability[value.Capability]; ok && existing != value {
			return nil, inconsistent(ErrorCodeSearchResultInvalid, "search degradation reasons conflict")
		}
		byCapability[value.Capability] = value
	}
	result := make([]SearchDegradation, 0, len(byCapability))
	for _, capability := range []SearchDegradationCapability{SearchDegradationVector, SearchDegradationRerank} {
		if value, ok := byCapability[capability]; ok {
			result = append(result, value)
		}
	}
	return result, nil
}

// EvidenceV1 是 M6-D 可直接映射为 HTTP 引用的有界检索证据。
type EvidenceV1 struct {
	WorkspaceID         foundation.ID
	IndexVersionID      foundation.ID
	EmbeddingVersionID  *foundation.ID
	ChunkID             foundation.ID
	ParseProjectionID   foundation.ID
	Sequence            int32
	ContentHash         string
	HeadingPath         []string
	Span                EvidenceSpan
	Snippet             string
	Provenances         []EvidenceProvenance
	ProvenanceTruncated bool
	Lexical             *CandidateStageScore
	LexicalFTSScore     *float64
	LexicalTrigramScore *float64
	// Vector 的 Score 保存持久 Distance Metric 对应 SQL operator 的原始 distance；排序使用升序。
	Vector             *CandidateStageScore
	Fusion             CandidateStageScore
	Rerank             *CandidateStageScore
	RerankModelVersion string
}

// SearchResult 区分持久 Index 降级与本次 Query 降级，并冻结实际执行模式。
type SearchResult struct {
	WorkspaceID               foundation.ID
	IndexVersionID            foundation.ID
	EmbeddingVersionID        *foundation.ID
	RequestedMode             SearchMode
	EffectiveMode             SearchMode
	IndexDegradedCapabilities []DegradedCapability
	Degradations              []SearchDegradation
	Items                     []EvidenceV1
}

// ValidateSearchResult 校验模式降级矩阵、Index/Query 分型和全部 Evidence 绑定。
func ValidateSearchResult(request SearchRequest, result SearchResult) error {
	canonicalRequest, err := CanonicalizeSearchRequest(request)
	if err != nil {
		return err
	}
	if result.WorkspaceID != canonicalRequest.WorkspaceID || result.RequestedMode != canonicalRequest.Mode ||
		result.IndexVersionID == "" || !validSearchMode(result.EffectiveMode) || len(result.Items) > int(canonicalRequest.Limit) {
		return inconsistent(ErrorCodeSearchResultInvalid, "search result request binding is invalid")
	}
	indexCapabilities, err := NormalizeDegradedCapabilities(result.IndexDegradedCapabilities)
	if err != nil || !equalCapabilities(indexCapabilities, result.IndexDegradedCapabilities) {
		return inconsistent(ErrorCodeSearchResultInvalid, "search index degradation is invalid")
	}
	degradations, err := NormalizeSearchDegradations(result.Degradations)
	if err != nil || !equalSearchDegradations(degradations, result.Degradations) {
		return inconsistent(ErrorCodeSearchResultInvalid, "search query degradation is not canonical")
	}
	if err := validateSearchModeMatrix(canonicalRequest.Mode, result); err != nil {
		return err
	}
	seenChunks := make(map[foundation.ID]struct{}, len(result.Items))
	for _, item := range result.Items {
		if err := validateEvidenceV1(item); err != nil {
			return err
		}
		if item.WorkspaceID != result.WorkspaceID || item.IndexVersionID != result.IndexVersionID ||
			!optionalIDEqual(item.EmbeddingVersionID, result.EmbeddingVersionID) {
			return inconsistent(ErrorCodeSearchResultInvalid, "evidence does not match search result index")
		}
		if _, duplicate := seenChunks[item.ChunkID]; duplicate {
			return inconsistent(ErrorCodeSearchResultInvalid, "search result contains duplicate chunk ranks")
		}
		seenChunks[item.ChunkID] = struct{}{}
	}
	return nil
}

func validateEvidenceV1(item EvidenceV1) error {
	candidate := SearchCandidate{
		WorkspaceID: item.WorkspaceID, IndexVersionID: item.IndexVersionID,
		EmbeddingVersionID: item.EmbeddingVersionID, ChunkID: item.ChunkID,
		ParseProjectionID: item.ParseProjectionID, Sequence: item.Sequence,
		ContentHash: item.ContentHash, HeadingPath: item.HeadingPath, Span: item.Span,
		Snippet: item.Snippet, Provenances: item.Provenances,
		ProvenanceTruncated: item.ProvenanceTruncated, Lexical: item.Lexical,
		LexicalFTSScore: item.LexicalFTSScore, LexicalTrigramScore: item.LexicalTrigramScore,
		Vector: item.Vector, Fusion: &item.Fusion, Rerank: item.Rerank,
	}
	if err := ValidateSearchCandidate(candidate); err != nil {
		return err
	}
	if item.Rerank == nil && item.RerankModelVersion != "" ||
		item.Rerank != nil && !isCanonicalText(item.RerankModelVersion) {
		return inconsistent(ErrorCodeSearchCandidateInvalid, "rerank score and model version must be present together")
	}
	return nil
}

func validateSearchModeMatrix(requested SearchMode, result SearchResult) error {
	vectorDegraded := hasSearchDegradation(result.Degradations, SearchDegradationVector)
	switch requested {
	case SearchModeKeyword:
		if result.EffectiveMode != SearchModeKeyword || len(result.Degradations) != 0 {
			return inconsistent(ErrorCodeSearchResultInvalid, "keyword search cannot change mode or report query degradation")
		}
	case SearchModeSemantic:
		if result.EffectiveMode != SearchModeSemantic || result.EmbeddingVersionID == nil ||
			HasDegradedCapability(result.IndexDegradedCapabilities, DegradedVector) || vectorDegraded {
			return inconsistent(ErrorCodeSearchResultInvalid, "semantic search requires complete vector capability")
		}
	case SearchModeHybrid:
		if result.EffectiveMode != SearchModeHybrid && result.EffectiveMode != SearchModeKeyword {
			return inconsistent(ErrorCodeSearchResultInvalid, "hybrid search effective mode is invalid")
		}
		if result.EffectiveMode == SearchModeKeyword && !vectorDegraded {
			return inconsistent(ErrorCodeSearchResultInvalid, "hybrid keyword fallback requires vector degradation")
		}
		if result.EffectiveMode == SearchModeHybrid && result.EmbeddingVersionID == nil {
			return inconsistent(ErrorCodeSearchResultInvalid, "effective hybrid search requires an embedding version")
		}
	}
	return nil
}

func validSearchMode(mode SearchMode) bool {
	return mode == SearchModeKeyword || mode == SearchModeSemantic || mode == SearchModeHybrid
}

func canonicalID(value foundation.ID) (foundation.ID, error) {
	parsed, err := foundation.ParseID(string(value))
	if err != nil {
		return "", invalid(ErrorCodeSearchRequestInvalid, "search filter contains an invalid id")
	}
	return parsed, nil
}

func canonicalIDList(values []foundation.ID) ([]foundation.ID, error) {
	if len(values) > MaxSearchFilterValues {
		return nil, invalid(ErrorCodeSearchRequestInvalid, "search filter contains too many ids")
	}
	canonical := make([]foundation.ID, 0, len(values))
	seen := make(map[foundation.ID]struct{}, len(values))
	for _, value := range values {
		parsed, err := canonicalID(value)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[parsed]; !duplicate {
			seen[parsed] = struct{}{}
			canonical = append(canonical, parsed)
		}
	}
	sort.Slice(canonical, func(left, right int) bool { return canonical[left] < canonical[right] })
	return canonical, nil
}

func canonicalPathPrefixes(values []string) ([]string, error) {
	if len(values) > MaxSearchFilterValues {
		return nil, invalid(ErrorCodeSearchRequestInvalid, "search filter contains too many path prefixes")
	}
	canonical := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
			return nil, invalid(ErrorCodeSearchRequestInvalid, "search path prefix must be a relative posix path")
		}
		cleaned := path.Clean(value)
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return nil, invalid(ErrorCodeSearchRequestInvalid, "search path prefix escapes workspace")
		}
		if _, duplicate := seen[cleaned]; !duplicate {
			seen[cleaned] = struct{}{}
			canonical = append(canonical, cleaned)
		}
	}
	sort.Strings(canonical)
	return canonical, nil
}

func canonicalSearchTime(value *time.Time) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if value.IsZero() {
		return nil, invalid(ErrorCodeSearchRequestInvalid, "search captured time is invalid")
	}
	canonical := value.UTC()
	return &canonical, nil
}

func canonicalRelativePath(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') ||
		strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned == value && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func validHeadingPath(values []string) bool {
	if len(values) > 32 {
		return false
	}
	for _, value := range values {
		if !isCanonicalText(value) || len(value) > 256 || !utf8.ValidString(value) {
			return false
		}
	}
	return true
}

func validStageScore(value CandidateStageScore) bool {
	return value.Rank > 0 && finiteScore(value.Score)
}

func finiteScore(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func sameProvenanceIdentity(left, right EvidenceProvenance) bool {
	return left.SourceID == right.SourceID && left.SourceVersionID == right.SourceVersionID && left.RelativePath == right.RelativePath
}

func equalProvenance(left, right []EvidenceProvenance) bool {
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

func equalSearchDegradations(left, right []SearchDegradation) bool {
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

func hasSearchDegradation(values []SearchDegradation, target SearchDegradationCapability) bool {
	for _, value := range values {
		if value.Capability == target {
			return true
		}
	}
	return false
}
