package retrieval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	searchKnowledgeV2Version = int64(2)
	readSourceV3Version      = int64(3)

	maxWorkspaceAnalysisSearchHits  = 5
	maxWorkspaceAnalysisSourceReads = 3
	maxWorkspaceAnalysisExcerpt     = 4 * 1024
	maxSearchKnowledgeV2InputBytes  = 64 * 1024
)

const (
	errorCodeReadSourceReceiptInvalid  = "TOOL_READ_SOURCE_RECEIPT_INVALID"
	errorCodeReadSourceReferenceDenied = "TOOL_READ_SOURCE_REFERENCE_DENIED"
)

var (
	searchKnowledgeV2Ref = toolsdomain.ToolRef{Name: searchKnowledgeName, Version: searchKnowledgeV2Version}
	readSourceV3Ref      = toolsdomain.ToolRef{Name: readSourceName, Version: readSourceV3Version}
)

// SearchKnowledgeV2Executor 将服务端检索计划投影为当前 Analysis Run 内的短证据引用。
type SearchKnowledgeV2Executor struct {
	search    searcher
	citations retrievalapplication.CitationEvidenceStore
}

// NewSearchKnowledgeV2Executor 创建不接受 Workspace 或实体 ID 的受限检索 Executor。
func NewSearchKnowledgeV2Executor(
	search searcher,
	citations retrievalapplication.CitationEvidenceStore,
) (*SearchKnowledgeV2Executor, error) {
	if nilDependency(search) || nilDependency(citations) {
		return nil, dependencyUnavailable(errors.New("retrieval search and citation evidence store are required"))
	}
	return &SearchKnowledgeV2Executor{search: search, citations: citations}, nil
}

// Execute 最多返回五个 E<n>，并在私有 binding 中冻结完整 Citation 身份和前 1-3 个读取引用。
func (executor *SearchKnowledgeV2Executor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	return executor.execute(ctx, request, searchKnowledgeV2Ref, maxWorkspaceAnalysisSourceReads)
}

func (executor *SearchKnowledgeV2Executor) execute(ctx context.Context, request toolsapplication.ExecutorRequest, ref toolsdomain.ToolRef, selectedLimit int) (toolsapplication.ExecutorResult, error) {
	if executor == nil || nilDependency(executor.search) || nilDependency(executor.citations) {
		return toolsapplication.ExecutorResult{}, dependencyUnavailable(errors.New("retrieval search or citation evidence store is unavailable"))
	}
	if ctx == nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeSearchInputInvalid, errors.New("search context is required"))
	}
	if err := validateExactRequestTool(request, ref); err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeSearchInputInvalid, err)
	}
	input, err := decodeSearchKnowledgeV2Input(request.Arguments)
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
	queries, err := searchKnowledgeV2CitationQueries(request.Identity.WorkspaceID, result)
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeSearchResultInvalid, err)
	}
	bindings := []retrievalapplication.CitationSourceSpanBinding{}
	if len(queries) > 0 {
		bindings, err = executor.citations.LoadCitationSourceSpanReferences(ctx, queries)
		if err != nil {
			return toolsapplication.ExecutorResult{}, err
		}
	}
	output, privateBinding, err := searchKnowledgeDocuments(request.Identity.WorkspaceID, result, bindings, ref, selectedLimit)
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeSearchResultInvalid, err)
	}
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(ref)
	if !found {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeSearchResultInvalid, errors.New("search receipt contract is unavailable"))
	}
	return toolsapplication.ExecutorResult{
		Output: output,
		PrivateBinding: &toolsapplication.ExecutorPrivateBinding{
			Schema:   contract.PrivateBindingSchema,
			Document: privateBinding,
		},
	}, nil
}

type searchKnowledgeV2Input struct {
	Query string `json:"query"`
	Mode  string `json:"mode"`
	Limit int32  `json:"limit"`
}

func (input searchKnowledgeV2Input) domainRequest(workspaceID foundation.ID) (retrievaldomain.SearchRequest, error) {
	return retrievaldomain.CanonicalizeSearchRequest(retrievaldomain.SearchRequest{
		WorkspaceID: workspaceID,
		Query:       input.Query,
		Mode:        retrievaldomain.SearchMode(input.Mode),
		Limit:       input.Limit,
	})
}

type searchKnowledgeV2Output struct {
	EffectiveMode string                        `json:"effective_mode"`
	Items         []searchKnowledgeV2OutputItem `json:"items"`
	Degradations  []string                      `json:"degradations"`
}

type searchKnowledgeV2OutputItem struct {
	EvidenceRef string `json:"evidence_ref"`
	Rank        int    `json:"rank"`
	Snippet     string `json:"snippet"`
}

type searchKnowledgeV2PrivateBinding struct {
	Items        []readSourceV3IdentityDocument `json:"items"`
	SelectedRefs []string                       `json:"selected_refs"`
}

func decodeSearchKnowledgeV2Input(raw []byte) (searchKnowledgeV2Input, error) {
	limits := strictLimits(maxSearchKnowledgeV2InputBytes, maxSearchQueryBytes, 0)
	return strictjson.DecodeObject(raw, limits, func(value searchKnowledgeV2Input) error {
		_, err := value.domainRequest("00000000-0000-4000-8000-000000000001")
		if err != nil || value.Limit > maxWorkspaceAnalysisSearchHits {
			return errors.New("workspace analysis search plan is invalid")
		}
		return nil
	})
}

func searchKnowledgeV2CitationQueries(workspaceID foundation.ID, result retrievaldomain.SearchResult) ([]retrievaldomain.CitationReferenceQuery, error) {
	if len(result.Items) > maxWorkspaceAnalysisSearchHits {
		return nil, errors.New("search result exceeds workspace analysis hit limit")
	}
	queries := make([]retrievaldomain.CitationReferenceQuery, len(result.Items))
	for index, item := range result.Items {
		if len(item.Provenances) == 0 {
			return nil, errors.New("search evidence has no source provenance")
		}
		queries[index] = retrievaldomain.CitationReferenceQuery{
			WorkspaceID: workspaceID, IndexVersionID: result.IndexVersionID, ChunkID: item.ChunkID,
			SourceVersionID: item.Provenances[0].SourceVersionID, SourceSpanID: item.Span.ID,
		}
		if err := retrievaldomain.ValidateCitationReferenceQuery(queries[index]); err != nil {
			return nil, err
		}
	}
	return queries, nil
}

func searchKnowledgeV2Documents(
	workspaceID foundation.ID,
	result retrievaldomain.SearchResult,
	bindings []retrievalapplication.CitationSourceSpanBinding,
) (json.RawMessage, json.RawMessage, error) {
	return searchKnowledgeDocuments(workspaceID, result, bindings, searchKnowledgeV2Ref, maxWorkspaceAnalysisSourceReads)
}

func searchKnowledgeDocuments(workspaceID foundation.ID, result retrievaldomain.SearchResult, bindings []retrievalapplication.CitationSourceSpanBinding, ref toolsdomain.ToolRef, selectedLimit int) (json.RawMessage, json.RawMessage, error) {
	if len(result.Items) > maxWorkspaceAnalysisSearchHits {
		return nil, nil, errors.New("search result exceeds workspace analysis hit limit")
	}
	if len(bindings) != len(result.Items) {
		return nil, nil, errors.New("citation evidence store returned an incomplete search binding")
	}
	degradations, err := searchDegradations(result)
	if err != nil {
		return nil, nil, err
	}
	output := searchKnowledgeV2Output{
		EffectiveMode: string(result.EffectiveMode),
		Items:         make([]searchKnowledgeV2OutputItem, len(result.Items)),
		Degradations:  degradations,
	}
	binding := searchKnowledgeV2PrivateBinding{
		Items:        make([]readSourceV3IdentityDocument, len(result.Items)),
		SelectedRefs: make([]string, min(selectedLimit, len(result.Items))),
	}
	seenCitations := make(map[string]struct{}, len(result.Items))
	for index, item := range result.Items {
		if len(item.Provenances) == 0 || !validWorkspaceAnalysisText(item.Snippet, 1, retrievaldomain.MaxEvidenceSnippetBytes) {
			return nil, nil, errors.New("search evidence is not safe for the workspace analysis contract")
		}
		provenance := item.Provenances[0]
		sourceBinding := bindings[index]
		query := retrievaldomain.CitationReferenceQuery{
			WorkspaceID: workspaceID, IndexVersionID: result.IndexVersionID, ChunkID: item.ChunkID,
			SourceVersionID: provenance.SourceVersionID, SourceSpanID: item.Span.ID,
		}
		if sourceBinding.Query != query || retrievaldomain.ValidateSourceSpanReference(sourceBinding.Reference) != nil ||
			sourceBinding.Reference.SourceVersion.WorkspaceID != workspaceID || sourceBinding.Reference.SourceVersion.SourceVersionID != provenance.SourceVersionID ||
			sourceBinding.Reference.ParseProjectionID != item.ParseProjectionID || sourceBinding.Reference.Span != item.Span {
			return nil, nil, errors.New("citation evidence store returned a drifted search binding")
		}
		ref := evidenceRef(index + 1)
		identity := ReadSourceV3Identity{
			EvidenceRef:     ref,
			CitationID:      citationID(workspaceID, result.IndexVersionID, item.ChunkID, provenance.SourceVersionID, item.Span.ID),
			IndexVersionID:  result.IndexVersionID,
			ChunkID:         item.ChunkID,
			SourceVersionID: provenance.SourceVersionID,
			SourceSpanID:    item.Span.ID,
			ContentHash:     strings.ToLower(sourceBinding.Reference.ExcerptHash),
		}
		if err := validateReadSourceV3Identity(workspaceID, identity, maxWorkspaceAnalysisSearchHits); err != nil {
			return nil, nil, err
		}
		if _, duplicate := seenCitations[identity.CitationID]; duplicate {
			return nil, nil, errors.New("search evidence contains a duplicate citation identity")
		}
		seenCitations[identity.CitationID] = struct{}{}
		output.Items[index] = searchKnowledgeV2OutputItem{EvidenceRef: ref, Rank: index + 1, Snippet: item.Snippet}
		binding.Items[index] = readSourceV3IdentityDocumentFrom(identity)
		if index < len(binding.SelectedRefs) {
			binding.SelectedRefs[index] = ref
		}
	}
	outputDocument, err := json.Marshal(output)
	if err != nil {
		return nil, nil, err
	}
	bindingDocument, err := json.Marshal(binding)
	if err != nil {
		return nil, nil, err
	}
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(ref)
	if !found || int64(len(outputDocument)) > contract.MaxOutputBytes || int64(len(bindingDocument)) > contract.MaxPrivateBindingBytes {
		return nil, nil, errors.New("search receipt documents exceed the frozen byte limits")
	}
	return outputDocument, bindingDocument, nil
}

// SearchKnowledgeV2ReceiptReader 是 application authority 端口的兼容别名。
type SearchKnowledgeV2ReceiptReader = toolsapplication.SearchKnowledgeV2ReceiptReader

// ReadSourceV3ResolveRequest 只携带服务端 Workspace/Run 身份和模型可见的短引用。
type ReadSourceV3ResolveRequest struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	EvidenceRef   string
}

// ReadSourceV3Identity 是 Search receipt 冻结的完整 Citation 身份；不得投影到模型、HTTP 或日志。
type ReadSourceV3Identity struct {
	EvidenceRef     string        `json:"-"`
	CitationID      string        `json:"-"`
	IndexVersionID  foundation.ID `json:"-"`
	ChunkID         foundation.ID `json:"-"`
	SourceVersionID foundation.ID `json:"-"`
	SourceSpanID    foundation.ID `json:"-"`
	ContentHash     string        `json:"-"`
}

// String 仅暴露模型可见短引用，不把服务端实体身份或哈希写入调试输出。
func (identity ReadSourceV3Identity) String() string {
	return fmt.Sprintf("ReadSourceV3Identity{evidence_ref:%s}", identity.EvidenceRef)
}

// GoString 避免 %#v 绕过安全调试投影。
func (identity ReadSourceV3Identity) GoString() string { return identity.String() }

// LogValue 仅向结构化日志提供模型可见短引用。
func (identity ReadSourceV3Identity) LogValue() slog.Value {
	return slog.GroupValue(slog.String("evidence_ref", identity.EvidenceRef))
}

// readSourceV3IdentityDocument 是 canonical private binding 专用 wire DTO。
// 字段顺序属于已冻结的 receipt 字节合同，不得复用可被普通 JSON/日志序列化的运行时类型。
type readSourceV3IdentityDocument struct {
	EvidenceRef     string        `json:"evidence_ref"`
	CitationID      string        `json:"citation_id"`
	IndexVersionID  foundation.ID `json:"index_version_id"`
	ChunkID         foundation.ID `json:"chunk_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
	ContentHash     string        `json:"content_hash"`
}

func readSourceV3IdentityDocumentFrom(identity ReadSourceV3Identity) readSourceV3IdentityDocument {
	return readSourceV3IdentityDocument{
		EvidenceRef: identity.EvidenceRef, CitationID: identity.CitationID,
		IndexVersionID: identity.IndexVersionID, ChunkID: identity.ChunkID,
		SourceVersionID: identity.SourceVersionID, SourceSpanID: identity.SourceSpanID,
		ContentHash: identity.ContentHash,
	}
}

func (document readSourceV3IdentityDocument) identity() ReadSourceV3Identity {
	return ReadSourceV3Identity{
		EvidenceRef: document.EvidenceRef, CitationID: document.CitationID,
		IndexVersionID: document.IndexVersionID, ChunkID: document.ChunkID,
		SourceVersionID: document.SourceVersionID, SourceSpanID: document.SourceSpanID,
		ContentHash: document.ContentHash,
	}
}

// ReadSourceV3Resolution 是同 Run Search receipt 解出的受信 Source 读取输入。
type ReadSourceV3Resolution struct {
	WorkspaceID       foundation.ID        `json:"-"`
	WorkflowRunID     foundation.ID        `json:"-"`
	SearchReceiptID   foundation.ID        `json:"-"`
	SearchReceiptHash string               `json:"-"`
	Identity          ReadSourceV3Identity `json:"-"`
}

// String 仅显示模型可见短引用，不打印 Workspace、Run、receipt 或 Citation tuple。
func (resolution ReadSourceV3Resolution) String() string {
	return fmt.Sprintf("ReadSourceV3Resolution{evidence_ref:%s}", resolution.Identity.EvidenceRef)
}

// GoString 避免 %#v 绕过安全调试投影。
func (resolution ReadSourceV3Resolution) GoString() string { return resolution.String() }

// LogValue 仅向结构化日志提供模型可见短引用。
func (resolution ReadSourceV3Resolution) LogValue() slog.Value {
	return slog.GroupValue(slog.String("evidence_ref", resolution.Identity.EvidenceRef))
}

// ReadSourceV3Resolver 在 Executor 访问 Retrieval 前解析并验证同 Run 的已选短引用。
type ReadSourceV3Resolver interface {
	ResolveReadSourceV3(context.Context, ReadSourceV3ResolveRequest) (ReadSourceV3Resolution, error)
}

// ReadSourceV3ReceiptResolver 从 canonical Search receipt 解析一个已冻结的选中引用。
type ReadSourceV3ReceiptResolver struct {
	receipts SearchKnowledgeV2ReceiptReader
}

// NewReadSourceV3ReceiptResolver 创建只读取私有 Search binding 的窄 resolver。
func NewReadSourceV3ReceiptResolver(receipts SearchKnowledgeV2ReceiptReader) (*ReadSourceV3ReceiptResolver, error) {
	if nilDependency(receipts) {
		return nil, dependencyUnavailable(errors.New("search receipt reader is required"))
	}
	return &ReadSourceV3ReceiptResolver{receipts: receipts}, nil
}

// ResolveReadSourceV3 拒绝跨 Workspace、跨 Run、未选中和发生漂移的 E<n>。
func (resolver *ReadSourceV3ReceiptResolver) ResolveReadSourceV3(ctx context.Context, request ReadSourceV3ResolveRequest) (ReadSourceV3Resolution, error) {
	if resolver == nil || nilDependency(resolver.receipts) {
		return ReadSourceV3Resolution{}, dependencyUnavailable(errors.New("search receipt resolver is unavailable"))
	}
	if ctx == nil {
		return ReadSourceV3Resolution{}, inputError(errorCodeReadSourceInputInvalid, errors.New("source receipt context is required"))
	}
	if !canonicalID(request.WorkspaceID) || !canonicalID(request.WorkflowRunID) || request.WorkspaceID == request.WorkflowRunID ||
		!validEvidenceRef(request.EvidenceRef, maxWorkspaceAnalysisSourceReads) {
		return ReadSourceV3Resolution{}, inputError(errorCodeReadSourceInputInvalid, errors.New("source receipt resolution request is invalid"))
	}
	receipt, err := resolver.receipts.LoadSearchKnowledgeV2Receipt(ctx, request.WorkspaceID, request.WorkflowRunID)
	if err != nil {
		return ReadSourceV3Resolution{}, err
	}
	identity, err := resolveSearchKnowledgeV2Receipt(request, receipt)
	if err != nil {
		return ReadSourceV3Resolution{}, err
	}
	return ReadSourceV3Resolution{
		WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID,
		SearchReceiptID: receipt.ID, SearchReceiptHash: receipt.OutputHash, Identity: identity,
	}, nil
}

func resolveSearchKnowledgeV2Receipt(request ReadSourceV3ResolveRequest, receipt toolsdomain.ResultReceipt) (ReadSourceV3Identity, error) {
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(searchKnowledgeV2Ref)
	if !found || receipt.Tool != searchKnowledgeV2Ref || receipt.WorkspaceID != request.WorkspaceID || receipt.WorkflowRunID != request.WorkflowRunID ||
		receipt.OutputSchema != contract.OutputSchema || receipt.PersistencePolicy != toolsdomain.ResultPersistenceCanonical ||
		receipt.MaxOutputBytes != contract.MaxOutputBytes || receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes ||
		!validResultReceiptIDs(receipt) || receipt.CreatedAt.IsZero() || !receipt.CreatedAt.Equal(receipt.CreatedAt.UTC().Truncate(time.Microsecond)) ||
		!lowerHexValue(receipt.DefinitionHash, 64) || receipt.OutputBytes != int64(len(receipt.Output)) ||
		receipt.OutputBytes < 1 || receipt.OutputBytes > contract.MaxOutputBytes || receipt.OutputHash != hashDocument(receipt.Output) ||
		!canonicalWorkspaceAnalysisDocument(receipt.Output) || receipt.PrivateBinding == nil {
		return ReadSourceV3Identity{}, receiptInvalid(errors.New("search receipt authority binding is invalid"))
	}
	bindingFact := receipt.PrivateBinding
	if bindingFact.Schema != contract.PrivateBindingSchema || bindingFact.Bytes != int64(len(bindingFact.Document)) ||
		bindingFact.Bytes < 1 || bindingFact.Bytes > contract.MaxPrivateBindingBytes || bindingFact.Hash != hashDocument(bindingFact.Document) ||
		!canonicalWorkspaceAnalysisDocument(bindingFact.Document) {
		return ReadSourceV3Identity{}, receiptInvalid(errors.New("search receipt private binding is invalid"))
	}
	output, err := decodeSearchKnowledgeV2Output(receipt.Output)
	if err != nil {
		return ReadSourceV3Identity{}, receiptInvalid(err)
	}
	binding, err := decodeSearchKnowledgeV2PrivateBinding(request.WorkspaceID, bindingFact.Document)
	if err != nil || len(output.Items) != len(binding.Items) {
		return ReadSourceV3Identity{}, receiptInvalid(errOr(err, "search receipt output and binding counts differ"))
	}
	for index := range output.Items {
		if output.Items[index].EvidenceRef != binding.Items[index].EvidenceRef {
			return ReadSourceV3Identity{}, receiptInvalid(errors.New("search receipt output and binding references differ"))
		}
	}
	for _, selected := range binding.SelectedRefs {
		if selected == request.EvidenceRef {
			for _, document := range binding.Items {
				identity := document.identity()
				if identity.EvidenceRef == selected {
					return identity, nil
				}
			}
			return ReadSourceV3Identity{}, receiptInvalid(errors.New("selected search reference has no identity"))
		}
	}
	return ReadSourceV3Identity{}, referenceDenied(errors.New("evidence reference was not selected by the frozen search receipt"))
}

func decodeSearchKnowledgeV2Output(raw []byte) (searchKnowledgeV2Output, error) {
	contract, _ := toolsdomain.WorkspaceAnalysisResultReceiptContract(searchKnowledgeV2Ref)
	return strictjson.DecodeObject(raw, receiptLimits(contract.MaxOutputBytes), func(value searchKnowledgeV2Output) error {
		if !validSearchModeValue(value.EffectiveMode) || value.Items == nil || len(value.Items) > maxWorkspaceAnalysisSearchHits ||
			value.Degradations == nil || len(value.Degradations) > 16 || !validSortedUniqueTokens(value.Degradations) {
			return errors.New("search receipt output is invalid")
		}
		for index, item := range value.Items {
			if item.EvidenceRef != evidenceRef(index+1) || item.Rank != index+1 ||
				!validWorkspaceAnalysisText(item.Snippet, 1, retrievaldomain.MaxEvidenceSnippetBytes) {
				return errors.New("search receipt output item is invalid")
			}
		}
		return nil
	})
}

func decodeSearchKnowledgeV2PrivateBinding(workspaceID foundation.ID, raw []byte) (searchKnowledgeV2PrivateBinding, error) {
	contract, _ := toolsdomain.WorkspaceAnalysisResultReceiptContract(searchKnowledgeV2Ref)
	return strictjson.DecodeObject(raw, receiptLimits(contract.MaxPrivateBindingBytes), func(value searchKnowledgeV2PrivateBinding) error {
		if value.Items == nil || len(value.Items) > maxWorkspaceAnalysisSearchHits || value.SelectedRefs == nil ||
			len(value.SelectedRefs) != min(maxWorkspaceAnalysisSourceReads, len(value.Items)) {
			return errors.New("search receipt private binding is invalid")
		}
		seenCitations := make(map[string]struct{}, len(value.Items))
		for index, document := range value.Items {
			identity := document.identity()
			if identity.EvidenceRef != evidenceRef(index+1) || validateReadSourceV3Identity(workspaceID, identity, maxWorkspaceAnalysisSearchHits) != nil {
				return errors.New("search receipt private identity is invalid")
			}
			if _, duplicate := seenCitations[identity.CitationID]; duplicate {
				return errors.New("search receipt private identity is duplicated")
			}
			seenCitations[identity.CitationID] = struct{}{}
		}
		for index, ref := range value.SelectedRefs {
			if ref != evidenceRef(index+1) {
				return errors.New("search receipt selected references are invalid")
			}
		}
		return nil
	})
}

type citationSourceReference interface {
	OpenCitationEvidence(context.Context, retrievaldomain.CitationReferenceQuery) (retrievalapplication.SourceSpanView, error)
}

// ReadSourceV3Executor 只打开同 Run Search receipt 已冻结的一个 E<n>。
type ReadSourceV3Executor struct {
	resolver  ReadSourceV3Resolver
	reference citationSourceReference
}

// NewReadSourceV3Executor 创建使用 receipt resolver 和完整 Citation tuple 的 Source Executor。
func NewReadSourceV3Executor(resolver ReadSourceV3Resolver, reference citationSourceReference) (*ReadSourceV3Executor, error) {
	if nilDependency(resolver) || nilDependency(reference) {
		return nil, dependencyUnavailable(errors.New("source receipt resolver and citation reference are required"))
	}
	return &ReadSourceV3Executor{resolver: resolver, reference: reference}, nil
}

// Execute 只向模型返回 E<n>、完整 span hash、truncated 与 4 KiB 内 excerpt。
func (executor *ReadSourceV3Executor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	if executor == nil || nilDependency(executor.resolver) || nilDependency(executor.reference) {
		return toolsapplication.ExecutorResult{}, dependencyUnavailable(errors.New("workspace analysis source reader is unavailable"))
	}
	if ctx == nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeReadSourceInputInvalid, errors.New("source context is required"))
	}
	if err := validateExactRequestTool(request, readSourceV3Ref); err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeReadSourceInputInvalid, err)
	}
	input, err := decodeReadSourceV3Input(request.Arguments)
	if err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeReadSourceInputInvalid, err)
	}
	resolution, err := executor.resolver.ResolveReadSourceV3(ctx, ReadSourceV3ResolveRequest{
		WorkspaceID: request.Identity.WorkspaceID, WorkflowRunID: request.Identity.WorkflowRunID, EvidenceRef: input.EvidenceRef,
	})
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	if err := validateReadSourceV3Resolution(request, input.EvidenceRef, resolution); err != nil {
		return toolsapplication.ExecutorResult{}, receiptInvalid(err)
	}
	identity := resolution.Identity
	view, err := executor.reference.OpenCitationEvidence(ctx, retrievaldomain.CitationReferenceQuery{
		WorkspaceID: request.Identity.WorkspaceID, IndexVersionID: identity.IndexVersionID, ChunkID: identity.ChunkID,
		SourceVersionID: identity.SourceVersionID, SourceSpanID: identity.SourceSpanID,
	})
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	if err := validateReadSourceV3View(request.Identity.WorkspaceID, identity, view); err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, err)
	}
	output := readSourceV3Output{
		EvidenceRef: identity.EvidenceRef, ContentHash: identity.ContentHash,
		Truncated: view.ExcerptTruncated, Excerpt: view.Excerpt,
	}
	binding := readSourceV3PrivateBinding{
		SearchReceiptID: resolution.SearchReceiptID, SearchReceiptHash: resolution.SearchReceiptHash,
		readSourceV3IdentityDocument: readSourceV3IdentityDocumentFrom(identity),
	}
	outputDocument, err := json.Marshal(output)
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, err)
	}
	bindingDocument, err := json.Marshal(binding)
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, err)
	}
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(readSourceV3Ref)
	if !found || int64(len(outputDocument)) > contract.MaxOutputBytes || int64(len(bindingDocument)) > contract.MaxPrivateBindingBytes {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, errors.New("source receipt documents exceed the frozen byte limits"))
	}
	return toolsapplication.ExecutorResult{
		Output: outputDocument,
		PrivateBinding: &toolsapplication.ExecutorPrivateBinding{
			Schema: contract.PrivateBindingSchema, Document: bindingDocument,
		},
	}, nil
}

type readSourceV3Input struct {
	EvidenceRef string `json:"evidence_ref"`
}

type readSourceV3Output struct {
	EvidenceRef string `json:"evidence_ref"`
	ContentHash string `json:"content_hash"`
	Truncated   bool   `json:"truncated"`
	Excerpt     string `json:"excerpt"`
}

type readSourceV3PrivateBinding struct {
	SearchReceiptID   foundation.ID `json:"search_receipt_id"`
	SearchReceiptHash string        `json:"search_receipt_hash"`
	readSourceV3IdentityDocument
}

func (binding readSourceV3PrivateBinding) identity() ReadSourceV3Identity {
	return binding.readSourceV3IdentityDocument.identity()
}

func decodeReadSourceV3Input(raw []byte) (readSourceV3Input, error) {
	return strictjson.DecodeObject(raw, strictLimits(4*1024, 16, 0), func(value readSourceV3Input) error {
		if !validEvidenceRef(value.EvidenceRef, maxWorkspaceAnalysisSourceReads) {
			return errors.New("source evidence reference is invalid")
		}
		return nil
	})
}

func validateReadSourceV3Resolution(request toolsapplication.ExecutorRequest, evidenceRef string, resolution ReadSourceV3Resolution) error {
	if resolution.WorkspaceID != request.Identity.WorkspaceID || resolution.WorkflowRunID != request.Identity.WorkflowRunID ||
		!canonicalID(resolution.SearchReceiptID) || !lowerHexValue(resolution.SearchReceiptHash, 64) ||
		resolution.Identity.EvidenceRef != evidenceRef || validateReadSourceV3Identity(request.Identity.WorkspaceID, resolution.Identity, maxWorkspaceAnalysisSourceReads) != nil {
		return errors.New("source receipt resolution drifted")
	}
	return nil
}

func validateReadSourceV3Identity(workspaceID foundation.ID, identity ReadSourceV3Identity, maxRef int) error {
	ids := []foundation.ID{identity.IndexVersionID, identity.ChunkID, identity.SourceVersionID, identity.SourceSpanID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalID(id) {
			return errors.New("source identity contains an invalid id")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("source identity reuses an id")
		}
		seen[id] = struct{}{}
	}
	if !validEvidenceRef(identity.EvidenceRef, maxRef) || identity.CitationID != citationID(workspaceID, identity.IndexVersionID, identity.ChunkID, identity.SourceVersionID, identity.SourceSpanID) ||
		!lowerHexValue(identity.ContentHash, 64) {
		return errors.New("source citation identity is invalid")
	}
	return nil
}

func validateReadSourceV3View(workspaceID foundation.ID, identity ReadSourceV3Identity, view retrievalapplication.SourceSpanView) error {
	if err := retrievaldomain.ValidateSourceSpanReference(view.Reference); err != nil {
		return err
	}
	if view.Reference.SourceVersion.WorkspaceID != workspaceID || view.Reference.SourceVersion.SourceVersionID != identity.SourceVersionID ||
		view.Reference.Span.ID != identity.SourceSpanID || strings.ToLower(view.Reference.ExcerptHash) != identity.ContentHash ||
		!validWorkspaceAnalysisText(view.Excerpt, 1, maxWorkspaceAnalysisExcerpt) {
		return errors.New("opened source span does not match the frozen search identity")
	}
	return nil
}

func validateExactRequestTool(request toolsapplication.ExecutorRequest, ref toolsdomain.ToolRef) error {
	if request.Tool != ref {
		return errors.New("executor request references the wrong exact tool version")
	}
	return request.Identity.Validate()
}

func receiptLimits(maxBytes int64) strictjson.Limits {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = int(maxBytes)
	limits.MaxDepth = 8
	limits.MaxStringBytes = int(maxBytes)
	limits.MaxArrayItems = 16
	limits.MaxObjectFields = 16
	return limits
}

func validSearchModeValue(value string) bool {
	return value == string(retrievaldomain.SearchModeKeyword) || value == string(retrievaldomain.SearchModeSemantic) || value == string(retrievaldomain.SearchModeHybrid)
}

func validSortedUniqueTokens(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	previous := ""
	for index, value := range values {
		if !validStableToken(value) || index > 0 && value <= previous {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
		previous = value
	}
	return true
}

func validWorkspaceAnalysisText(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') &&
		(minimum == 0 || strings.TrimSpace(value) != "")
}

func validEvidenceRef(value string, maximum int) bool {
	if maximum <= 5 {
		return len(value) == 2 && value[0] == 'E' && value[1] >= '1' && value[1] <= byte('0'+maximum)
	}
	if !toolsdomain.ValidDynamicEvidenceRef(value) {
		return false
	}
	n, _ := strconv.Atoi(value[1:])
	return n <= maximum
}

func evidenceRef(ordinal int) string {
	return "E" + strconv.Itoa(ordinal)
}

func lowerHexValue(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func validResultReceiptIDs(receipt toolsdomain.ResultReceipt) bool {
	ids := []foundation.ID{receipt.ID, receipt.ToolCallID, receipt.WorkspaceID, receipt.WorkflowRunID, receipt.NodeRunID, receipt.NodeAttemptID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalID(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func hashDocument(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func canonicalWorkspaceAnalysisDocument(document json.RawMessage) bool {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil {
		return false
	}
	canonical, err := json.Marshal(value)
	return err == nil && bytes.Equal(canonical, document)
}

func receiptInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeReadSourceReceiptInvalid, false, cause)
}

func referenceDenied(cause error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, errorCodeReadSourceReferenceDenied, false, cause)
}

func errOr(err error, fallback string) error {
	if err != nil {
		return err
	}
	return errors.New(fallback)
}

var (
	_ toolsapplication.Executor = (*SearchKnowledgeV2Executor)(nil)
	_ toolsapplication.Executor = (*ReadSourceV3Executor)(nil)
	_ ReadSourceV3Resolver      = (*ReadSourceV3ReceiptResolver)(nil)
	_ citationSourceReference   = (*retrievalapplication.EvidenceReferenceService)(nil)
)
