package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	validateCitationV3Version       = int64(3)
	maxValidateCitationV3InputBytes = 16 * 1024
)

const (
	validateCitationReasonOK                 = "OK"
	validateCitationReasonEvidenceIneligible = "EVIDENCE_INELIGIBLE"
	validateCitationReasonBindingMismatch    = "BINDING_MISMATCH"
)

// ValidateCitationV3ResolveRequest 只携带从受信 Workflow 身份和服务端候选构造的解析条件。
type ValidateCitationV3ResolveRequest struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	CandidateID   foundation.ID
	CandidateHash string
	EvidenceRefs  []string
}

// ValidateCitationV3Resolution 返回与请求顺序一致的 same-run receipt 身份投影。
type ValidateCitationV3Resolution struct {
	WorkspaceID   foundation.ID          `json:"-"`
	WorkflowRunID foundation.ID          `json:"-"`
	CandidateID   foundation.ID          `json:"-"`
	CandidateHash string                 `json:"-"`
	Identities    []ReadSourceV3Identity `json:"-"`
}

// String 只显示身份数量，不打印候选绑定或 Citation tuple。
func (resolution ValidateCitationV3Resolution) String() string {
	return "ValidateCitationV3Resolution{identities:" + strconv.Itoa(len(resolution.Identities)) + "}"
}

// GoString 避免 %#v 绕过安全投影。
func (resolution ValidateCitationV3Resolution) GoString() string { return resolution.String() }

// LogValue 只向结构化日志提供已解析身份数量。
func (resolution ValidateCitationV3Resolution) LogValue() slog.Value {
	return slog.GroupValue(slog.Int("identities", len(resolution.Identities)))
}

// ValidateCitationV3Resolver 是仅供服务端读取候选及上游 canonical receipt 的窄端口。
//
// 实现必须校验候选绑定，并从同一 Workflow Run 的 SearchKnowledge@2 与每个 ReadSource@3
// 私有 binding 解析身份；不得重新检索、重新读取 Source 正文或返回 snippet/excerpt/path。
type ValidateCitationV3Resolver interface {
	ResolveValidateCitationV3(context.Context, ValidateCitationV3ResolveRequest) (ValidateCitationV3Resolution, error)
}

// ValidateCitationV3AuthorityQuery 是 application authority 查询的兼容别名。
type ValidateCitationV3AuthorityQuery = toolsapplication.ValidateCitationV3AuthorityQuery

// ValidateCitationV3Authority 是 application authority 安全投影的兼容别名。
type ValidateCitationV3Authority = toolsapplication.ValidateCitationV3Authority

// ValidateCitationV3AuthorityReader 是 application authority 端口的兼容别名。
type ValidateCitationV3AuthorityReader = toolsapplication.ValidateCitationV3AuthorityReader

// ValidateCitationV3ReceiptResolver 从候选、SearchKnowledge@2 和 ReadSource@3 回执展开完整 Citation 身份。
type ValidateCitationV3ReceiptResolver struct {
	authority ValidateCitationV3AuthorityReader
}

// NewValidateCitationV3ReceiptResolver 创建只读取服务端持久事实的 resolver。
func NewValidateCitationV3ReceiptResolver(authority ValidateCitationV3AuthorityReader) (*ValidateCitationV3ReceiptResolver, error) {
	if nilDependency(authority) {
		return nil, dependencyUnavailable(errors.New("citation validation authority reader is required"))
	}
	return &ValidateCitationV3ReceiptResolver{authority: authority}, nil
}

// ResolveValidateCitationV3 拒绝候选漂移、跨 Run receipt、未读取引用和任何 tuple/hash 不一致。
func (resolver *ValidateCitationV3ReceiptResolver) ResolveValidateCitationV3(
	ctx context.Context,
	request ValidateCitationV3ResolveRequest,
) (ValidateCitationV3Resolution, error) {
	if resolver == nil || nilDependency(resolver.authority) {
		return ValidateCitationV3Resolution{}, dependencyUnavailable(errors.New("citation validation receipt resolver is unavailable"))
	}
	if ctx == nil || !validValidateCitationV3RequestIDs(request) || !lowerHexValue(request.CandidateHash, 64) ||
		!validValidateCitationV3Refs(request.EvidenceRefs) {
		return ValidateCitationV3Resolution{}, inputError(errorCodeCitationInputInvalid, errors.New("citation receipt resolution request is invalid"))
	}
	authority, err := resolver.authority.LoadValidateCitationV3Authority(ctx, ValidateCitationV3AuthorityQuery{
		WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID, CandidateID: request.CandidateID,
	})
	if err != nil {
		return ValidateCitationV3Resolution{}, err
	}
	if err := validateCitationV3AuthorityBinding(request, authority); err != nil {
		return ValidateCitationV3Resolution{}, receiptInvalid(err)
	}

	identities := make([]ReadSourceV3Identity, len(request.EvidenceRefs))
	for index, ref := range request.EvidenceRefs {
		identity, resolveErr := resolveSearchKnowledgeV2Receipt(ReadSourceV3ResolveRequest{
			WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID, EvidenceRef: ref,
		}, authority.SearchReceipt)
		if resolveErr != nil {
			return ValidateCitationV3Resolution{}, resolveErr
		}
		identities[index] = identity
	}
	if err := validateCitationV3ReadReceipts(request, authority.SearchReceipt, identities, authority.ReadSourceReceipts); err != nil {
		return ValidateCitationV3Resolution{}, receiptInvalid(err)
	}
	return ValidateCitationV3Resolution{
		WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID,
		CandidateID: request.CandidateID, CandidateHash: request.CandidateHash, Identities: identities,
	}, nil
}

// ValidateCitationV3Executor 只校验由持久候选及同 Run receipts 展开的 1-3 个短引用。
type ValidateCitationV3Executor struct {
	resolver    ValidateCitationV3Resolver
	reference   citationReference
	eligibility evidenceEligibility
}

// NewValidateCitationV3Executor 创建 Workspace Analysis 专用 Citation 校验 Executor。
func NewValidateCitationV3Executor(
	resolver ValidateCitationV3Resolver,
	reference citationReference,
	eligibility evidenceEligibility,
) (*ValidateCitationV3Executor, error) {
	if nilDependency(resolver) || nilDependency(reference) || nilDependency(eligibility) {
		return nil, dependencyUnavailable(errors.New("citation receipt resolver, evidence reference and eligibility are required"))
	}
	return &ValidateCitationV3Executor{resolver: resolver, reference: reference, eligibility: eligibility}, nil
}

// Execute 展开受信短引用，复核可打开性和正式知识资格，并返回无正文的 canonical receipt 草稿。
func (executor *ValidateCitationV3Executor) Execute(
	ctx context.Context,
	request toolsapplication.ExecutorRequest,
) (toolsapplication.ExecutorResult, error) {
	if executor == nil || nilDependency(executor.resolver) || nilDependency(executor.reference) || nilDependency(executor.eligibility) {
		return toolsapplication.ExecutorResult{}, dependencyUnavailable(errors.New("citation validation dependency is unavailable"))
	}
	if ctx == nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeCitationInputInvalid, errors.New("citation validation context is nil"))
	}
	if request.Tool != (toolsdomain.ToolRef{Name: validateCitationName, Version: validateCitationV3Version}) {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeCitationInputInvalid, errors.New("executor request references the wrong tool"))
	}
	if err := request.Identity.Validate(); err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeCitationInputInvalid, err)
	}
	input, err := decodeValidateCitationV3Input(request.Arguments)
	if err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeCitationInputInvalid, err)
	}

	resolved, err := executor.resolver.ResolveValidateCitationV3(ctx, ValidateCitationV3ResolveRequest{
		WorkspaceID: request.Identity.WorkspaceID, WorkflowRunID: request.Identity.WorkflowRunID,
		CandidateID: input.CandidateID, CandidateHash: input.CandidateHash,
		EvidenceRefs: append([]string(nil), input.EvidenceRefs...),
	})
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	if err := validateCitationV3Resolution(request, input, resolved); err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, err)
	}

	queries := make([]retrievaldomain.CitationReferenceQuery, len(resolved.Identities))
	for index, identity := range resolved.Identities {
		queries[index] = retrievaldomain.CitationReferenceQuery{
			WorkspaceID: request.Identity.WorkspaceID, IndexVersionID: identity.IndexVersionID, ChunkID: identity.ChunkID,
			SourceVersionID: identity.SourceVersionID, SourceSpanID: identity.SourceSpanID,
		}
	}
	opened, err := executor.reference.OpenCitationEvidenceBatch(ctx, queries)
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	openedByQuery, err := indexOpenedCitations(request.Identity.WorkspaceID, queries, opened)
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, err)
	}

	results := make([]validateCitationV3Result, len(resolved.Identities))
	eligibleQueries := make([]retrievaldomain.CitationReferenceQuery, 0, len(queries))
	for index, identity := range resolved.Identities {
		results[index] = validateCitationV3Result{
			EvidenceRef: identity.EvidenceRef, ReasonCode: validateCitationReasonBindingMismatch, Valid: false,
		}
		openedItem := openedByQuery[queries[index]]
		if openedItem.View.Excerpt == "" || !utf8.ValidString(openedItem.View.Excerpt) {
			return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, errors.New("opened citation excerpt is invalid"))
		}
		if openedItem.View.Reference.ExcerptHash != identity.ContentHash {
			continue
		}
		eligibleQueries = append(eligibleQueries, queries[index])
	}

	eligibilityByKey := map[string]knowledgedomain.ProvenanceEligibility{}
	if len(eligibleQueries) > 0 {
		provenance, provenanceErr := uniqueProvenance(eligibleQueries)
		if provenanceErr != nil {
			return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, provenanceErr)
		}
		eligibility, eligibilityErr := executor.eligibility.CheckEvidenceEligibility(ctx, knowledgedomain.EvidenceEligibilityQuery{
			WorkspaceID: request.Identity.WorkspaceID,
			Provenance:  provenance,
		})
		if eligibilityErr != nil {
			return toolsapplication.ExecutorResult{}, eligibilityErr
		}
		eligibilityByKey, err = exactEligibility(provenance, eligibility)
		if err != nil {
			return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, err)
		}
	}
	for index, identity := range resolved.Identities {
		if openedByQuery[queries[index]].View.Reference.ExcerptHash != identity.ContentHash {
			continue
		}
		eligibility := eligibilityByKey[provenanceKey(knowledgedomain.ProvenanceRef{
			WorkspaceID: request.Identity.WorkspaceID, SourceVersionID: identity.SourceVersionID, SourceSpanID: identity.SourceSpanID,
		})]
		if eligibility.Eligibility == knowledgedomain.EvidenceIneligible {
			results[index].ReasonCode = validateCitationReasonEvidenceIneligible
			continue
		}
		results[index].ReasonCode = validateCitationReasonOK
		results[index].Valid = true
	}

	return validateCitationV3ExecutorResult(input, resolved.Identities, results)
}

type validateCitationV3Input struct {
	CandidateHash string        `json:"candidate_hash"`
	CandidateID   foundation.ID `json:"candidate_id"`
	EvidenceRefs  []string      `json:"evidence_refs"`
}

type validateCitationV3Result struct {
	EvidenceRef string `json:"evidence_ref"`
	ReasonCode  string `json:"reason_code"`
	Valid       bool   `json:"valid"`
}

type validateCitationV3Output struct {
	Results []validateCitationV3Result `json:"results"`
}

type validateCitationV3PrivateIdentity struct {
	ChunkID         foundation.ID `json:"chunk_id"`
	CitationID      string        `json:"citation_id"`
	ContentHash     string        `json:"content_hash"`
	EvidenceRef     string        `json:"evidence_ref"`
	IndexVersionID  foundation.ID `json:"index_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
}

type validateCitationV3PrivateBinding struct {
	CandidateHash string                              `json:"candidate_hash"`
	CandidateID   foundation.ID                       `json:"candidate_id"`
	Results       []validateCitationV3PrivateIdentity `json:"results"`
}

type validateCitationV3ReadReceiptOutput struct {
	ContentHash string `json:"content_hash"`
	EvidenceRef string `json:"evidence_ref"`
	Excerpt     string `json:"excerpt"`
	Truncated   *bool  `json:"truncated"`
}

func decodeValidateCitationV3Input(raw []byte) (validateCitationV3Input, error) {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxValidateCitationV3InputBytes
	limits.MaxDepth = 4
	limits.MaxStringBytes = 128
	limits.MaxArrayItems = 3
	limits.MaxObjectFields = 3
	return strictjson.DecodeObject(raw, limits, func(value validateCitationV3Input) error {
		if !canonicalID(value.CandidateID) || !lowerHexValue(value.CandidateHash, 64) ||
			!validValidateCitationV3Refs(value.EvidenceRefs) {
			return errors.New("citation candidate binding is invalid")
		}
		return nil
	})
}

func validateCitationV3AuthorityBinding(request ValidateCitationV3ResolveRequest, authority ValidateCitationV3Authority) error {
	if authority.WorkspaceID != request.WorkspaceID || authority.WorkflowRunID != request.WorkflowRunID ||
		authority.CandidateID != request.CandidateID || authority.CandidateHash != request.CandidateHash ||
		!canonicalID(authority.AnalysisRunID) || len(authority.EvidenceRefs) != len(request.EvidenceRefs) ||
		len(authority.ReadSourceReceipts) != len(request.EvidenceRefs) {
		return errors.New("citation candidate authority binding drifted")
	}
	ids := []foundation.ID{authority.WorkspaceID, authority.WorkflowRunID, authority.AnalysisRunID, authority.CandidateID}
	seenIDs := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalID(id) {
			return errors.New("citation candidate authority contains an invalid id")
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return errors.New("citation candidate authority reuses an id")
		}
		seenIDs[id] = struct{}{}
	}
	for index, ref := range request.EvidenceRefs {
		if authority.EvidenceRefs[index] != ref {
			return errors.New("citation candidate references drifted")
		}
	}
	return nil
}

func validateCitationV3ReadReceipts(
	request ValidateCitationV3ResolveRequest,
	searchReceipt toolsdomain.ResultReceipt,
	identities []ReadSourceV3Identity,
	readReceipts []toolsdomain.ResultReceipt,
) error {
	if len(readReceipts) != len(identities) {
		return errors.New("citation source receipt set is incomplete")
	}
	expected := make(map[string]ReadSourceV3Identity, len(identities))
	for _, identity := range identities {
		expected[identity.EvidenceRef] = identity
	}
	seenRefs := make(map[string]struct{}, len(readReceipts))
	seenReceipts := map[foundation.ID]struct{}{searchReceipt.ID: {}}
	seenCalls := map[foundation.ID]struct{}{searchReceipt.ToolCallID: {}}
	for _, receipt := range readReceipts {
		output, binding, err := decodeValidateCitationV3ReadReceipt(request, receipt)
		if err != nil {
			return err
		}
		identity, exists := expected[output.EvidenceRef]
		if !exists || binding.identity() != identity || binding.SearchReceiptID != searchReceipt.ID ||
			binding.SearchReceiptHash != searchReceipt.OutputHash || output.ContentHash != identity.ContentHash ||
			binding.EvidenceRef != output.EvidenceRef {
			return errors.New("citation source receipt binding drifted")
		}
		if _, duplicate := seenRefs[output.EvidenceRef]; duplicate {
			return errors.New("citation source receipt reference is duplicated")
		}
		if _, duplicate := seenReceipts[receipt.ID]; duplicate {
			return errors.New("citation source receipt is duplicated")
		}
		if _, duplicate := seenCalls[receipt.ToolCallID]; duplicate {
			return errors.New("citation source receipt call is duplicated")
		}
		seenRefs[output.EvidenceRef] = struct{}{}
		seenReceipts[receipt.ID] = struct{}{}
		seenCalls[receipt.ToolCallID] = struct{}{}
	}
	return nil
}

func decodeValidateCitationV3ReadReceipt(
	request ValidateCitationV3ResolveRequest,
	receipt toolsdomain.ResultReceipt,
) (validateCitationV3ReadReceiptOutput, readSourceV3PrivateBinding, error) {
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(readSourceV3Ref)
	if !found || receipt.Tool != readSourceV3Ref || receipt.WorkspaceID != request.WorkspaceID ||
		receipt.WorkflowRunID != request.WorkflowRunID || receipt.OutputSchema != contract.OutputSchema ||
		receipt.PersistencePolicy != toolsdomain.ResultPersistenceCanonical || receipt.MaxOutputBytes != contract.MaxOutputBytes ||
		receipt.MaxPrivateBindingBytes != contract.MaxPrivateBindingBytes || !validResultReceiptIDs(receipt) ||
		receipt.CreatedAt.IsZero() || !receipt.CreatedAt.Equal(receipt.CreatedAt.UTC().Truncate(time.Microsecond)) ||
		!lowerHexValue(receipt.DefinitionHash, 64) ||
		receipt.OutputBytes != int64(len(receipt.Output)) || receipt.OutputBytes < 1 ||
		receipt.OutputBytes > contract.MaxOutputBytes || receipt.OutputHash != hashDocument(receipt.Output) ||
		!canonicalWorkspaceAnalysisDocument(receipt.Output) || receipt.PrivateBinding == nil {
		return validateCitationV3ReadReceiptOutput{}, readSourceV3PrivateBinding{}, errors.New("citation source receipt authority is invalid")
	}
	bindingFact := receipt.PrivateBinding
	if bindingFact.Schema != contract.PrivateBindingSchema || bindingFact.Bytes != int64(len(bindingFact.Document)) ||
		bindingFact.Bytes < 1 || bindingFact.Bytes > contract.MaxPrivateBindingBytes ||
		bindingFact.Hash != hashDocument(bindingFact.Document) || !canonicalWorkspaceAnalysisDocument(bindingFact.Document) {
		return validateCitationV3ReadReceiptOutput{}, readSourceV3PrivateBinding{}, errors.New("citation source receipt private binding is invalid")
	}
	output, err := strictjson.DecodeObject(receipt.Output, receiptLimits(contract.MaxOutputBytes), func(value validateCitationV3ReadReceiptOutput) error {
		if !validEvidenceRef(value.EvidenceRef, maxWorkspaceAnalysisSourceReads) ||
			!lowerHexValue(value.ContentHash, 64) || value.Truncated == nil ||
			!validWorkspaceAnalysisText(value.Excerpt, 1, maxWorkspaceAnalysisExcerpt) {
			return errors.New("citation source receipt output is invalid")
		}
		return nil
	})
	if err != nil {
		return validateCitationV3ReadReceiptOutput{}, readSourceV3PrivateBinding{}, err
	}
	binding, err := strictjson.DecodeObject(bindingFact.Document, receiptLimits(contract.MaxPrivateBindingBytes), func(value readSourceV3PrivateBinding) error {
		if !canonicalID(value.SearchReceiptID) || !lowerHexValue(value.SearchReceiptHash, 64) ||
			validateReadSourceV3Identity(request.WorkspaceID, value.identity(), maxWorkspaceAnalysisSourceReads) != nil {
			return errors.New("citation source receipt private identity is invalid")
		}
		return nil
	})
	if err != nil {
		return validateCitationV3ReadReceiptOutput{}, readSourceV3PrivateBinding{}, err
	}
	return output, binding, nil
}

func validateCitationV3Resolution(
	request toolsapplication.ExecutorRequest,
	input validateCitationV3Input,
	resolution ValidateCitationV3Resolution,
) error {
	if resolution.WorkspaceID != request.Identity.WorkspaceID || resolution.WorkflowRunID != request.Identity.WorkflowRunID ||
		resolution.CandidateID != input.CandidateID || resolution.CandidateHash != input.CandidateHash ||
		len(resolution.Identities) != len(input.EvidenceRefs) {
		return errors.New("citation receipt resolution binding drifted")
	}
	seenCitations := make(map[string]struct{}, len(resolution.Identities))
	seenTuples := make(map[string]struct{}, len(resolution.Identities))
	var indexVersionID foundation.ID
	for index, identity := range resolution.Identities {
		if identity.EvidenceRef != input.EvidenceRefs[index] ||
			validateReadSourceV3Identity(request.Identity.WorkspaceID, identity, maxWorkspaceAnalysisSourceReads) != nil {
			return errors.New("citation receipt resolution contains an invalid identity")
		}
		if index == 0 {
			indexVersionID = identity.IndexVersionID
		} else if identity.IndexVersionID != indexVersionID {
			return errors.New("citation receipt resolution crosses index versions")
		}
		tupleKey := strings.Join([]string{
			string(identity.IndexVersionID), string(identity.ChunkID), string(identity.SourceVersionID), string(identity.SourceSpanID),
		}, "\x00")
		if _, duplicate := seenCitations[identity.CitationID]; duplicate {
			return errors.New("citation receipt resolution duplicates a citation")
		}
		if _, duplicate := seenTuples[tupleKey]; duplicate {
			return errors.New("citation receipt resolution duplicates an identity")
		}
		seenCitations[identity.CitationID] = struct{}{}
		seenTuples[tupleKey] = struct{}{}
	}
	return nil
}

func validValidateCitationV3Refs(refs []string) bool {
	if len(refs) < 1 || len(refs) > 3 {
		return false
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if !validEvidenceRef(ref, maxWorkspaceAnalysisSourceReads) {
			return false
		}
		if _, duplicate := seen[ref]; duplicate {
			return false
		}
		seen[ref] = struct{}{}
	}
	return true
}

func validValidateCitationV3RequestIDs(request ValidateCitationV3ResolveRequest) bool {
	ids := []foundation.ID{request.WorkspaceID, request.WorkflowRunID, request.CandidateID}
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

func validateCitationV3ExecutorResult(
	input validateCitationV3Input,
	identities []ReadSourceV3Identity,
	results []validateCitationV3Result,
) (toolsapplication.ExecutorResult, error) {
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(toolsdomain.ToolRef{
		Name: validateCitationName, Version: validateCitationV3Version,
	})
	if !found {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, errors.New("citation receipt contract is unavailable"))
	}
	output, err := json.Marshal(validateCitationV3Output{Results: results})
	if err != nil || int64(len(output)) > contract.MaxOutputBytes {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, errors.New("citation output exceeds its exact contract"))
	}
	bindingResults := make([]validateCitationV3PrivateIdentity, len(identities))
	for index, identity := range identities {
		bindingResults[index] = validateCitationV3PrivateIdentity{
			ChunkID: identity.ChunkID, CitationID: identity.CitationID, ContentHash: identity.ContentHash,
			EvidenceRef: identity.EvidenceRef, IndexVersionID: identity.IndexVersionID,
			SourceSpanID: identity.SourceSpanID, SourceVersionID: identity.SourceVersionID,
		}
	}
	binding, err := json.Marshal(validateCitationV3PrivateBinding{
		CandidateHash: input.CandidateHash, CandidateID: input.CandidateID, Results: bindingResults,
	})
	if err != nil || int64(len(binding)) > contract.MaxPrivateBindingBytes {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, errors.New("citation private binding exceeds its exact contract"))
	}
	return toolsapplication.ExecutorResult{
		Output: output,
		PrivateBinding: &toolsapplication.ExecutorPrivateBinding{
			Schema: contract.PrivateBindingSchema, Document: binding,
		},
	}, nil
}

var (
	_ toolsapplication.Executor  = (*ValidateCitationV3Executor)(nil)
	_ ValidateCitationV3Resolver = (*ValidateCitationV3ReceiptResolver)(nil)
)
