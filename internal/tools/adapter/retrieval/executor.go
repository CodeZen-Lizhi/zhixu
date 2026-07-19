// Package retrieval 把 Retrieval Application 的只读能力适配为严格 Tool Executor。
package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	searchKnowledgeName  = "SearchKnowledge"
	readSourceName       = "ReadSource"
	validateCitationName = "ValidateCitation"

	maxSearchInputBytes      = 64 * 1024
	maxReadSourceInputBytes  = 64 * 1024
	maxCitationInputBytes    = 256 * 1024
	maxCitationBatchSize     = 500
	maxSearchOutputItems     = 100
	maxSearchQueryBytes      = 8 * 1024
	maxSearchFilterValues    = 100
	maxCitationIdentityBytes = 128
)

const (
	errorCodeSearchInputInvalid       = "TOOL_SEARCH_KNOWLEDGE_INPUT_INVALID"
	errorCodeSearchResultInvalid      = "TOOL_SEARCH_KNOWLEDGE_RESULT_INVALID"
	errorCodeSearchReceiptUnavailable = "TOOL_SEARCH_KNOWLEDGE_RECEIPT_UNAVAILABLE"
	errorCodeReadSourceInputInvalid   = "TOOL_READ_SOURCE_INPUT_INVALID"
	errorCodeReadSourceResultInvalid  = "TOOL_READ_SOURCE_RESULT_INVALID"
	errorCodeCitationInputInvalid     = "TOOL_VALIDATE_CITATION_INPUT_INVALID"
	errorCodeCitationResultInvalid    = "TOOL_VALIDATE_CITATION_RESULT_INVALID"
	errorCodeRetrievalDependencyEmpty = "TOOL_RETRIEVAL_DEPENDENCY_UNAVAILABLE"
)

type searcher interface {
	Search(context.Context, retrievaldomain.SearchRequest) (retrievaldomain.SearchResult, error)
}

type sourceReference interface {
	GetSourceSpan(context.Context, foundation.ID, foundation.ID, foundation.ID) (retrievalapplication.SourceSpanView, error)
}

type citationReference interface {
	OpenCitationEvidenceBatch(context.Context, []retrievaldomain.CitationReferenceQuery) ([]retrievalapplication.OpenedCitationEvidence, error)
}

type evidenceEligibility interface {
	CheckEvidenceEligibility(context.Context, knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error)
}

// SearchKnowledgeExecutor 通过 Retrieval SearchService 检索当前服务端 Workspace 的知识。
type SearchKnowledgeExecutor struct{ search searcher }

// NewSearchKnowledgeExecutor 创建只接受服务端 Workspace 身份的检索 Executor。
func NewSearchKnowledgeExecutor(search searcher) (*SearchKnowledgeExecutor, error) {
	if nilDependency(search) {
		return nil, dependencyUnavailable(errors.New("retrieval search is required"))
	}
	return &SearchKnowledgeExecutor{search: search}, nil
}

// Execute 执行一次有界检索并返回稳定 Citation 身份，不暴露路径或检索内部对象。
func (executor *SearchKnowledgeExecutor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	if executor == nil || nilDependency(executor.search) {
		return toolsapplication.ExecutorResult{}, dependencyUnavailable(errors.New("retrieval search is unavailable"))
	}
	if err := validateRequestTool(request, searchKnowledgeName); err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeSearchInputInvalid, err)
	}
	input, err := decodeSearchInput(request.Arguments)
	if err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeSearchInputInvalid, err)
	}
	searchRequest, err := input.domainRequest(request.Identity.WorkspaceID)
	if err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeSearchInputInvalid, err)
	}
	result, err := executor.search.Search(ctx, searchRequest)
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	if err := retrievaldomain.ValidateSearchResult(searchRequest, result); err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeSearchResultInvalid, err)
	}
	output, err := searchOutputFromResult(result)
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeSearchResultInvalid, err)
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeSearchResultInvalid, err)
	}
	return toolsapplication.ExecutorResult{
		Output:    raw,
		ResultRef: "index-version:" + string(result.IndexVersionID),
	}, nil
}

// LoadResultReceipt 在未持久化权威 Search 输出时明确拒绝重放，避免再次调用检索或远程 Embedding。
func (executor *SearchKnowledgeExecutor) LoadResultReceipt(_ context.Context, _ toolsapplication.ExecutorRequest, call toolsdomain.ToolCall) (toolsapplication.ExecutorResult, error) {
	if err := validateReceiptCall(call, searchKnowledgeName); err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeSearchResultInvalid, err)
	}
	return toolsapplication.ExecutorResult{}, resultError(errorCodeSearchReceiptUnavailable, errors.New("authoritative search result receipt is unavailable"))
}

// ReadSourceExecutor 通过 EvidenceReferenceService 打开一个不可变 Source Span。
type ReadSourceExecutor struct{ reference sourceReference }

// NewReadSourceExecutor 创建不接受路径的 Source Span 读取 Executor。
func NewReadSourceExecutor(reference sourceReference) (*ReadSourceExecutor, error) {
	if nilDependency(reference) {
		return nil, dependencyUnavailable(errors.New("retrieval evidence reference is required"))
	}
	return &ReadSourceExecutor{reference: reference}, nil
}

// Execute 只使用服务端 Workspace 与稳定 Source Version/Span ID 打开证据。
func (executor *ReadSourceExecutor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	if executor == nil || nilDependency(executor.reference) {
		return toolsapplication.ExecutorResult{}, dependencyUnavailable(errors.New("retrieval evidence reference is unavailable"))
	}
	if err := validateRequestTool(request, readSourceName); err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeReadSourceInputInvalid, err)
	}
	input, err := decodeReadSourceInput(request.Arguments)
	if err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeReadSourceInputInvalid, err)
	}
	view, err := executor.reference.GetSourceSpan(ctx, request.Identity.WorkspaceID, input.SourceVersionID, input.SourceSpanID)
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	if err := validateOpenedSource(request.Identity.WorkspaceID, input, view); err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, err)
	}
	output := readSourceOutput{
		SourceVersionID: string(input.SourceVersionID),
		SourceSpanID:    string(input.SourceSpanID),
		ContentHash:     strings.ToLower(view.Reference.SourceVersion.ContentHash),
		Excerpt:         view.Excerpt,
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, err)
	}
	return toolsapplication.ExecutorResult{
		Output:    raw,
		ResultRef: "source-span:" + string(input.SourceSpanID),
	}, nil
}

// LoadResultReceipt 通过不可变 Source Version/Span 重新加载 canonical 输出。
func (executor *ReadSourceExecutor) LoadResultReceipt(ctx context.Context, request toolsapplication.ExecutorRequest, call toolsdomain.ToolCall) (toolsapplication.ExecutorResult, error) {
	if err := validateReceiptCall(call, readSourceName); err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, err)
	}
	return executor.Execute(ctx, request)
}

// ValidateCitationExecutor 单批复核最多 500 个完整 Citation tuple 及正式知识资格。
type ValidateCitationExecutor struct {
	reference   citationReference
	eligibility evidenceEligibility
}

// NewValidateCitationExecutor 创建批量 Citation 可打开性与 Knowledge Eligibility 复核 Executor。
func NewValidateCitationExecutor(reference citationReference, eligibility evidenceEligibility) (*ValidateCitationExecutor, error) {
	if nilDependency(reference) || nilDependency(eligibility) {
		return nil, dependencyUnavailable(errors.New("retrieval evidence reference and knowledge eligibility are required"))
	}
	return &ValidateCitationExecutor{reference: reference, eligibility: eligibility}, nil
}

// Execute 先校验稳定 Citation ID，再用一次 EvidenceReference 批量调用复核可打开性。
func (executor *ValidateCitationExecutor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	if executor == nil || nilDependency(executor.reference) || nilDependency(executor.eligibility) {
		return toolsapplication.ExecutorResult{}, dependencyUnavailable(errors.New("retrieval evidence reference or knowledge eligibility is unavailable"))
	}
	if err := validateRequestTool(request, validateCitationName); err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeCitationInputInvalid, err)
	}
	input, err := decodeCitationInput(request.Arguments)
	if err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeCitationInputInvalid, err)
	}

	queries := make([]retrievaldomain.CitationReferenceQuery, 0, len(input.Citations))
	queryIndexes := make([]int, 0, len(input.Citations))
	results := make([]citationValidationResult, len(input.Citations))
	for index, citation := range input.Citations {
		results[index] = citationValidationResult{citationTuple: citation, Valid: boolPointer(false), ReasonCode: "BINDING_MISMATCH"}
		expectedID := citationID(request.Identity.WorkspaceID, citation.IndexVersionID, citation.ChunkID, citation.SourceVersionID, citation.SourceSpanID)
		if citation.CitationID != expectedID {
			continue
		}
		queries = append(queries, retrievaldomain.CitationReferenceQuery{
			WorkspaceID: request.Identity.WorkspaceID, IndexVersionID: citation.IndexVersionID, ChunkID: citation.ChunkID,
			SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
		})
		queryIndexes = append(queryIndexes, index)
	}
	if len(queries) > 0 {
		opened, openErr := executor.reference.OpenCitationEvidenceBatch(ctx, queries)
		if openErr != nil {
			return toolsapplication.ExecutorResult{}, openErr
		}
		openedByQuery, err := indexOpenedCitations(request.Identity.WorkspaceID, queries, opened)
		if err != nil {
			return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, err)
		}
		provenance, err := uniqueProvenance(queries)
		if err != nil {
			return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, err)
		}
		eligibility, eligibilityErr := executor.eligibility.CheckEvidenceEligibility(ctx, knowledgedomain.EvidenceEligibilityQuery{
			WorkspaceID: request.Identity.WorkspaceID,
			Provenance:  provenance,
		})
		if eligibilityErr != nil {
			return toolsapplication.ExecutorResult{}, eligibilityErr
		}
		eligibilityByKey, err := exactEligibility(provenance, eligibility)
		if err != nil {
			return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, err)
		}
		for index, inputIndex := range queryIndexes {
			query := queries[index]
			openedItem := openedByQuery[query]
			if openedItem.View.Excerpt == "" || !utf8.ValidString(openedItem.View.Excerpt) {
				return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, errors.New("opened citation excerpt is invalid"))
			}
			eligibilityValue := eligibilityByKey[provenanceKey(knowledgedomain.ProvenanceRef{
				WorkspaceID: request.Identity.WorkspaceID, SourceVersionID: query.SourceVersionID, SourceSpanID: query.SourceSpanID,
			})]
			if eligibilityValue.Eligibility == knowledgedomain.EvidenceIneligible {
				results[inputIndex].ReasonCode = "EVIDENCE_INELIGIBLE"
				continue
			}
			results[inputIndex].Valid = boolPointer(true)
			results[inputIndex].ReasonCode = "OK"
		}
	}
	output := validateCitationOutput{Results: results}
	raw, err := json.Marshal(output)
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, err)
	}
	digest := sha256.Sum256(raw)
	return toolsapplication.ExecutorResult{
		Output:    raw,
		ResultRef: "citation-batch:" + hex.EncodeToString(digest[:]),
	}, nil
}

// LoadResultReceipt 重新读取同一完整 Citation tuple；Eligibility 漂移会由持久 hash/ref 比较 fail closed。
func (executor *ValidateCitationExecutor) LoadResultReceipt(ctx context.Context, request toolsapplication.ExecutorRequest, call toolsdomain.ToolCall) (toolsapplication.ExecutorResult, error) {
	if err := validateReceiptCall(call, validateCitationName); err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, err)
	}
	return executor.Execute(ctx, request)
}

func validateReceiptCall(call toolsdomain.ToolCall, name string) error {
	if call.Status != toolsdomain.CallSucceeded || call.Tool == nil || call.Tool.Name != name || call.Tool.Version != 1 || call.RequestedToolName != name || call.ResultRef == "" {
		return errors.New("persisted read receipt binding is invalid")
	}
	return nil
}

type searchKnowledgeInput struct {
	Query            string   `json:"query"`
	Mode             string   `json:"mode"`
	Limit            int32    `json:"limit"`
	SourceIDs        []string `json:"source_ids"`
	SourceVersionIDs []string `json:"source_version_ids"`
}

func (input searchKnowledgeInput) domainRequest(workspaceID foundation.ID) (retrievaldomain.SearchRequest, error) {
	sourceIDs, err := parseIDs(input.SourceIDs)
	if err != nil {
		return retrievaldomain.SearchRequest{}, err
	}
	sourceVersionIDs, err := parseIDs(input.SourceVersionIDs)
	if err != nil {
		return retrievaldomain.SearchRequest{}, err
	}
	return retrievaldomain.CanonicalizeSearchRequest(retrievaldomain.SearchRequest{
		WorkspaceID: workspaceID,
		Query:       input.Query,
		Mode:        retrievaldomain.SearchMode(input.Mode),
		Limit:       input.Limit,
		Filter: retrievaldomain.SearchFilter{
			SourceIDs: sourceIDs, SourceVersionIDs: sourceVersionIDs,
		},
	})
}

type searchKnowledgeOutput struct {
	IndexVersionID string                `json:"index_version_id"`
	EffectiveMode  string                `json:"effective_mode"`
	Items          []searchKnowledgeItem `json:"items"`
	Degradations   []string              `json:"degradations"`
}

type searchKnowledgeItem struct {
	CitationID      string `json:"citation_id"`
	ChunkID         string `json:"chunk_id"`
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
	ContentHash     string `json:"content_hash"`
	Rank            int    `json:"rank"`
	Snippet         string `json:"snippet"`
}

type readSourceInput struct {
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
}

type readSourceOutput struct {
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
	ContentHash     string `json:"content_hash"`
	Excerpt         string `json:"excerpt"`
}

type citationTuple struct {
	CitationID      string        `json:"citation_id"`
	IndexVersionID  foundation.ID `json:"index_version_id"`
	ChunkID         foundation.ID `json:"chunk_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
}

type validateCitationInput struct {
	Citations []citationTuple `json:"citations"`
}

type citationValidationResult struct {
	citationTuple
	Valid      *bool  `json:"valid"`
	ReasonCode string `json:"reason_code"`
}

type validateCitationOutput struct {
	Results []citationValidationResult `json:"results"`
}

func decodeSearchInput(raw []byte) (searchKnowledgeInput, error) {
	limits := strictLimits(maxSearchInputBytes, maxSearchQueryBytes, maxSearchFilterValues)
	return strictjson.DecodeObject(raw, limits, func(value searchKnowledgeInput) error {
		if value.SourceIDs == nil || value.SourceVersionIDs == nil || len(value.SourceIDs) > maxSearchFilterValues || len(value.SourceVersionIDs) > maxSearchFilterValues {
			return errors.New("search filters are missing or oversized")
		}
		_, err := value.domainRequest("00000000-0000-4000-8000-000000000001")
		return err
	})
}

func decodeReadSourceInput(raw []byte) (readSourceInput, error) {
	return strictjson.DecodeObject(raw, strictLimits(maxReadSourceInputBytes, maxReadSourceInputBytes, 0), func(value readSourceInput) error {
		if !canonicalID(value.SourceVersionID) || !canonicalID(value.SourceSpanID) || value.SourceVersionID == value.SourceSpanID {
			return errors.New("source version or span identity is invalid")
		}
		return nil
	})
}

func decodeCitationInput(raw []byte) (validateCitationInput, error) {
	return strictjson.DecodeObject(raw, strictLimits(maxCitationInputBytes, maxCitationInputBytes, maxCitationBatchSize), func(value validateCitationInput) error {
		if len(value.Citations) == 0 || len(value.Citations) > maxCitationBatchSize {
			return errors.New("citation batch count is invalid")
		}
		seen := make(map[string]struct{}, len(value.Citations))
		for _, citation := range value.Citations {
			if !validCitationTuple(citation) {
				return errors.New("citation tuple is invalid")
			}
			key := citationKey(citation)
			if _, duplicate := seen[key]; duplicate {
				return errors.New("citation tuple is duplicated")
			}
			seen[key] = struct{}{}
		}
		return nil
	})
}

func searchOutputFromResult(result retrievaldomain.SearchResult) (searchKnowledgeOutput, error) {
	if len(result.Items) > maxSearchOutputItems {
		return searchKnowledgeOutput{}, errors.New("search result exceeds tool output item limit")
	}
	degradations, err := searchDegradations(result)
	if err != nil {
		return searchKnowledgeOutput{}, err
	}
	output := searchKnowledgeOutput{
		IndexVersionID: string(result.IndexVersionID),
		EffectiveMode:  string(result.EffectiveMode),
		Items:          make([]searchKnowledgeItem, len(result.Items)),
		Degradations:   degradations,
	}
	for index, item := range result.Items {
		if len(item.Provenances) == 0 {
			return searchKnowledgeOutput{}, errors.New("search evidence has no source provenance")
		}
		provenance := item.Provenances[0]
		output.Items[index] = searchKnowledgeItem{
			CitationID:      citationID(result.WorkspaceID, result.IndexVersionID, item.ChunkID, provenance.SourceVersionID, item.Span.ID),
			ChunkID:         string(item.ChunkID),
			SourceVersionID: string(provenance.SourceVersionID),
			SourceSpanID:    string(item.Span.ID),
			ContentHash:     strings.ToLower(item.ContentHash),
			Rank:            index + 1,
			Snippet:         item.Snippet,
		}
	}
	return output, nil
}

func searchDegradations(result retrievaldomain.SearchResult) ([]string, error) {
	values := make(map[string]struct{}, len(result.IndexDegradedCapabilities)+len(result.Degradations))
	for _, capability := range result.IndexDegradedCapabilities {
		if capability == retrievaldomain.DegradedVector {
			values["INDEX_VECTOR_DEGRADED"] = struct{}{}
		}
	}
	for _, degradation := range result.Degradations {
		values[degradation.Code] = struct{}{}
	}
	resultValues := make([]string, 0, len(values))
	for value := range values {
		if !validStableToken(value) {
			return nil, errors.New("search degradation code does not satisfy the tool contract")
		}
		resultValues = append(resultValues, value)
	}
	sort.Strings(resultValues)
	return resultValues, nil
}

func validStableToken(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value[1:] {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' && character != '.' && character != '-' {
			return false
		}
	}
	return true
}

func uniqueProvenance(queries []retrievaldomain.CitationReferenceQuery) ([]knowledgedomain.ProvenanceRef, error) {
	byKey := make(map[string]knowledgedomain.ProvenanceRef, len(queries))
	for _, query := range queries {
		ref := knowledgedomain.ProvenanceRef{
			WorkspaceID: query.WorkspaceID, SourceVersionID: query.SourceVersionID, SourceSpanID: query.SourceSpanID,
		}
		key := provenanceKey(ref)
		byKey[key] = ref
	}
	result := make([]knowledgedomain.ProvenanceRef, 0, len(byKey))
	for _, ref := range byKey {
		result = append(result, ref)
	}
	sort.Slice(result, func(left, right int) bool { return provenanceKey(result[left]) < provenanceKey(result[right]) })
	for _, ref := range result {
		if !canonicalID(ref.WorkspaceID) || !canonicalID(ref.SourceVersionID) || !canonicalID(ref.SourceSpanID) {
			return nil, errors.New("citation provenance identity is invalid")
		}
	}
	return result, nil
}

func exactEligibility(expected []knowledgedomain.ProvenanceRef, actual []knowledgedomain.ProvenanceEligibility) (map[string]knowledgedomain.ProvenanceEligibility, error) {
	if len(actual) != len(expected) {
		return nil, errors.New("knowledge eligibility result is incomplete")
	}
	want := make(map[string]knowledgedomain.ProvenanceRef, len(expected))
	for _, ref := range expected {
		want[provenanceKey(ref)] = ref
	}
	result := make(map[string]knowledgedomain.ProvenanceEligibility, len(actual))
	for _, item := range actual {
		key := provenanceKey(item.Provenance)
		expectedRef, exists := want[key]
		if !exists || item.Provenance != expectedRef || knowledgedomain.ValidateProvenanceEligibility(item) != nil {
			return nil, errors.New("knowledge eligibility result contains an invalid provenance")
		}
		if _, duplicate := result[key]; duplicate {
			return nil, errors.New("knowledge eligibility result contains duplicate provenance")
		}
		result[key] = item
	}
	return result, nil
}

func provenanceKey(ref knowledgedomain.ProvenanceRef) string {
	return string(ref.SourceVersionID) + "\x00" + string(ref.SourceSpanID)
}

func validateOpenedSource(workspaceID foundation.ID, input readSourceInput, view retrievalapplication.SourceSpanView) error {
	if err := retrievaldomain.ValidateSourceSpanReference(view.Reference); err != nil {
		return err
	}
	if view.Reference.SourceVersion.WorkspaceID != workspaceID || view.Reference.SourceVersion.SourceVersionID != input.SourceVersionID ||
		view.Reference.Span.ID != input.SourceSpanID || view.Excerpt == "" || !utf8.ValidString(view.Excerpt) {
		return errors.New("opened source span does not match the trusted request")
	}
	return nil
}

func indexOpenedCitations(workspaceID foundation.ID, queries []retrievaldomain.CitationReferenceQuery, opened []retrievalapplication.OpenedCitationEvidence) (map[retrievaldomain.CitationReferenceQuery]retrievalapplication.OpenedCitationEvidence, error) {
	if len(opened) != len(queries) {
		return nil, errors.New("opened citation batch is incomplete")
	}
	wanted := make(map[retrievaldomain.CitationReferenceQuery]struct{}, len(queries))
	for _, query := range queries {
		wanted[query] = struct{}{}
	}
	result := make(map[retrievaldomain.CitationReferenceQuery]retrievalapplication.OpenedCitationEvidence, len(opened))
	for _, item := range opened {
		if item.Query.WorkspaceID != workspaceID {
			return nil, errors.New("opened citation batch workspace drifted")
		}
		if _, exists := wanted[item.Query]; !exists {
			return nil, errors.New("opened citation batch identity drifted")
		}
		if _, duplicate := result[item.Query]; duplicate {
			return nil, errors.New("opened citation batch contains a duplicate")
		}
		if err := retrievaldomain.ValidateSourceSpanReference(item.View.Reference); err != nil {
			return nil, err
		}
		if item.View.Reference.SourceVersion.WorkspaceID != workspaceID ||
			item.View.Reference.SourceVersion.SourceVersionID != item.Query.SourceVersionID ||
			item.View.Reference.Span.ID != item.Query.SourceSpanID {
			return nil, errors.New("opened citation evidence does not match the complete tuple")
		}
		result[item.Query] = item
	}
	return result, nil
}

func validateRequestTool(request toolsapplication.ExecutorRequest, name string) error {
	if request.Tool.Name != name || request.Tool.Version != 1 {
		return errors.New("executor request references the wrong tool")
	}
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	return nil
}

func parseIDs(values []string) ([]foundation.ID, error) {
	result := make([]foundation.ID, len(values))
	seen := make(map[foundation.ID]struct{}, len(values))
	for index, value := range values {
		id, err := foundation.ParseID(value)
		if err != nil || string(id) != value {
			return nil, errors.New("search filter identity is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, errors.New("search filter identity is duplicated")
		}
		seen[id] = struct{}{}
		result[index] = id
	}
	return result, nil
}

func validCitationTuple(value citationTuple) bool {
	return value.CitationID != "" && len(value.CitationID) <= maxCitationIdentityBytes && utf8.ValidString(value.CitationID) &&
		!strings.ContainsRune(value.CitationID, '\x00') && canonicalID(value.IndexVersionID) && canonicalID(value.ChunkID) &&
		canonicalID(value.SourceVersionID) && canonicalID(value.SourceSpanID)
}

func citationKey(value citationTuple) string {
	return strings.Join([]string{value.CitationID, string(value.IndexVersionID), string(value.ChunkID), string(value.SourceVersionID), string(value.SourceSpanID)}, "\x00")
}

func citationID(workspaceID, indexVersionID, chunkID, sourceVersionID, spanID foundation.ID) string {
	payload := string(workspaceID) + "\x00" + string(indexVersionID) + "\x00" + string(chunkID) + "\x00" + string(sourceVersionID) + "\x00" + string(spanID)
	digest := sha256.Sum256([]byte(payload))
	return "cite-" + hex.EncodeToString(digest[:])
}

func strictLimits(documentBytes, stringBytes, arrayItems int) strictjson.Limits {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = documentBytes
	limits.MaxDepth = 8
	limits.MaxStringBytes = stringBytes
	limits.MaxArrayItems = arrayItems
	limits.MaxObjectFields = 128
	return limits
}

func canonicalID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func boolPointer(value bool) *bool {
	result := value
	return &result
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

func dependencyUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeRetrievalDependencyEmpty, false, cause)
}

func inputError(code string, cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, cause)
}

func resultError(code string, cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, cause)
}

var (
	_ searcher                             = (*retrievalapplication.SearchService)(nil)
	_ sourceReference                      = (*retrievalapplication.EvidenceReferenceService)(nil)
	_ citationReference                    = (*retrievalapplication.EvidenceReferenceService)(nil)
	_ evidenceEligibility                  = (*knowledgeapplication.EvidenceEligibilityService)(nil)
	_ toolsapplication.Executor            = (*SearchKnowledgeExecutor)(nil)
	_ toolsapplication.Executor            = (*ReadSourceExecutor)(nil)
	_ toolsapplication.Executor            = (*ValidateCitationExecutor)(nil)
	_ toolsapplication.ResultReceiptLoader = (*SearchKnowledgeExecutor)(nil)
	_ toolsapplication.ResultReceiptLoader = (*ReadSourceExecutor)(nil)
	_ toolsapplication.ResultReceiptLoader = (*ValidateCitationExecutor)(nil)
)
