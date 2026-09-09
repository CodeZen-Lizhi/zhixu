package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestSearchKnowledgeV3ExecutorGrantsEveryHitAndKeepsPrivateIdentities(t *testing.T) {
	searchResult := workspaceAnalysisSearchResult(5)
	search := &searchFake{result: searchResult}
	citations := &citationEvidenceStoreFake{bindings: workspaceAnalysisCitationBindings(searchResult)}
	executor, err := NewSearchKnowledgeV3Executor(search, citations)
	if err != nil {
		t.Fatal(err)
	}
	request := dynamicExecutorRequest("SearchKnowledge", 3, `{"query":"approved evidence","mode":"keyword","limit":5}`)
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if search.calls != 1 || citations.calls != 1 || search.request.WorkspaceID != testWorkspaceID || search.request.Limit != 5 {
		t.Fatal("dynamic search did not use the bounded workspace request")
	}
	receipt := dynamicExecutorReceipt(t, request.Tool, result, 1)
	identities, err := toolsdomain.SearchKnowledgeV3ReceiptIdentities(receipt)
	if err != nil || len(identities) != 5 || identities[4].EvidenceRef != "E5" {
		t.Fatalf("all five hits must remain readable: count=%d err=%v", len(identities), err)
	}
	output, err := toolsdomain.SearchKnowledgeV3ReceiptModelOutput(receipt, map[string]string{"E1": "E6", "E2": "E7", "E3": "E8", "E4": "E9", "E5": "E10"})
	if err != nil || !strings.Contains(string(output), `"evidence_ref":"E10"`) {
		t.Fatalf("global reference projection failed: %v", err)
	}
	for _, forbidden := range []string{"citation_id", "content_hash", "index_version_id", "source_version_id", "relative_path", string(testIndexID)} {
		if strings.Contains(string(output), forbidden) {
			t.Fatalf("model search projection leaks %q", forbidden)
		}
	}
	workspaceAnalysisValidateDomainReceipt(t, workspaceAnalysisCatalogContract(t, request.Tool), result, generatedID(92, 2), generatedID(93, 2))
}

func TestDynamicRetrievalExecutorsRejectUntrustedInputBeforeDependencies(t *testing.T) {
	search := &searchFake{}
	searchExecutor, err := NewSearchKnowledgeV3Executor(search, &citationEvidenceStoreFake{})
	if err != nil {
		t.Fatal(err)
	}
	for _, arguments := range []string{
		`{"query":"x","mode":"keyword","limit":6}`,
		`{"query":"x","mode":"keyword","limit":1,"workspace_id":"forged"}`,
		`{"query":"x","query":"y","mode":"keyword","limit":1}`,
	} {
		_, err := searchExecutor.Execute(context.Background(), dynamicExecutorRequest("SearchKnowledge", 3, arguments))
		requireErrorCode(t, err, errorCodeSearchInputInvalid)
	}
	legacy := dynamicExecutorRequest("SearchKnowledge", 3, `{"query":"x","mode":"keyword","limit":1}`)
	legacy.Identity.DefinitionVersion = 1
	_, err = searchExecutor.Execute(context.Background(), legacy)
	requireErrorCode(t, err, errorCodeSearchInputInvalid)
	if search.calls != 0 {
		t.Fatal("untrusted search input reached retrieval")
	}

	authority := &dynamicSourceAuthorityFake{}
	reference := &readSourceV3ReferenceFake{}
	readExecutor, err := NewReadSourceV4Executor(authority, reference)
	if err != nil {
		t.Fatal(err)
	}
	for _, arguments := range []string{
		`{"evidence_ref":"E33"}`, `{"evidence_ref":"E01"}`,
		`{"evidence_ref":"E1","path":"/private"}`,
		`{"evidence_ref":"E1","evidence_ref":"E2"}`,
	} {
		_, err := readExecutor.Execute(context.Background(), dynamicExecutorRequest("ReadSource", 4, arguments))
		requireErrorCode(t, err, errorCodeReadSourceInputInvalid)
	}
	if authority.calls != 0 || reference.calls != 0 {
		t.Fatal("untrusted source input reached authority or evidence")
	}
}

func TestReadSourceV4ExecutorUsesGlobalReferenceAndExactHistoricalSearch(t *testing.T) {
	authority := dynamicSourceFixture(t, 2, "E10", generatedID(80, 2))
	reference := &readSourceV3ReferenceFake{view: workspaceAnalysisSourceView(readSourceIdentityFromDynamic(authority.Identity))}
	reader := &dynamicSourceAuthorityFake{authority: authority}
	executor, err := NewReadSourceV4Executor(reader, reference)
	if err != nil {
		t.Fatal(err)
	}
	request := dynamicExecutorRequest("ReadSource", 4, `{"evidence_ref":"E10"}`)
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || reader.query.WorkspaceID != testWorkspaceID || reader.query.WorkflowRunID != testRunID ||
		reference.calls != 1 || reference.query.IndexVersionID != authority.Identity.IndexVersionID || reference.query.SourceSpanID != authority.Identity.SourceSpanID {
		t.Fatal("source read did not use the frozen authority tuple")
	}
	receipt := dynamicExecutorReceipt(t, request.Tool, result, 3)
	binding, err := toolsdomain.ReadSourceV4ReceiptBinding(receipt)
	if err != nil || binding.Identity.EvidenceRef != "E10" || binding.SearchEvidenceRef != "E1" ||
		binding.SearchReceiptID != authority.SearchReceipt.ID || binding.SearchReceiptHash != authority.SearchReceipt.OutputHash {
		t.Fatalf("source did not retain both global and search-local identity: %v", err)
	}
	evidence, err := toolsdomain.ReadSourceV4ReceiptEvidenceForSearch(receipt, authority.SearchReceipt)
	if err != nil || evidence.EvidenceRef != "E10" || evidence.Excerpt != reference.view.Excerpt {
		t.Fatalf("source receipt cannot prove exact history: %v", err)
	}
	workspaceAnalysisValidateDomainReceipt(t, workspaceAnalysisCatalogContract(t, request.Tool), result, generatedID(92, 3), generatedID(93, 3))
}

func TestReadSourceV4ExecutorRejectsScopeAndTupleDriftBeforeOpeningEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*toolsapplication.ReadSourceV4Authority)
	}{
		{"workspace", func(a *toolsapplication.ReadSourceV4Authority) { a.WorkspaceID = generatedID(80, 9) }},
		{"workflow", func(a *toolsapplication.ReadSourceV4Authority) { a.WorkflowRunID = generatedID(80, 9) }},
		{"search local ref", func(a *toolsapplication.ReadSourceV4Authority) { a.SearchEvidenceRef = "E2" }},
		{"source tuple", func(a *toolsapplication.ReadSourceV4Authority) { a.Identity.SourceSpanID = generatedID(80, 9) }},
		{"search hash", func(a *toolsapplication.ReadSourceV4Authority) { a.SearchReceipt.OutputHash = strings.Repeat("f", 64) }},
		{"legacy search", func(a *toolsapplication.ReadSourceV4Authority) {
			a.SearchReceipt = workspaceAnalysisSearchReceipt(t, 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := dynamicSourceFixture(t, 2, "E10", testIndexID)
			test.mutate(&authority)
			reference := &readSourceV3ReferenceFake{}
			executor, err := NewReadSourceV4Executor(&dynamicSourceAuthorityFake{authority: authority}, reference)
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Execute(context.Background(), dynamicExecutorRequest("ReadSource", 4, `{"evidence_ref":"E10"}`))
			requireErrorCode(t, err, errorCodeReadSourceReceiptInvalid)
			if reference.calls != 0 {
				t.Fatal("drifted authority opened source evidence")
			}
		})
	}
}

func TestValidateCitationV4BatchesByHistoricalIndexAndPreservesGlobalOrder(t *testing.T) {
	first := dynamicSourceFixture(t, 5, "E1", testIndexID)
	second := dynamicSourceFixture(t, 6, "E6", generatedID(80, 6))
	authority := dynamicCitationFixture(t, second, first)
	reader := &dynamicCitationAuthorityFake{authority: authority}
	reference := &dynamicCitationReferenceFake{inner: validateCitationV3ReferenceFake{contentHashes: map[foundation.ID]string{first.Identity.SourceSpanID: first.Identity.ContentHash}}}
	eligibility := &validateCitationV3EligibilityFake{}
	executor, err := NewValidateCitationV4Executor(reader, reference, eligibility)
	if err != nil {
		t.Fatal(err)
	}
	request := dynamicExecutorRequest("ValidateCitation", 4, `{"candidate_id":null,"candidate_hash":null,"evidence_refs":["E6","E1"]}`)
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(reference.batches) != 2 || reference.batches[0][0].IndexVersionID != second.Identity.IndexVersionID || reference.batches[1][0].IndexVersionID != first.Identity.IndexVersionID || eligibility.calls != 1 {
		t.Fatal("citation validation did not use the two historical index batches")
	}
	if !slices.Equal(reader.query.EvidenceRefs, []string{"E6", "E1"}) || reader.query.CandidateID != "" || reader.query.CandidateHash != "" {
		t.Fatal("loop validation invented a candidate or changed ref order")
	}
	var output validateCitationV3Output
	if json.Unmarshal(result.Output, &output) != nil || len(output.Results) != 2 || output.Results[0].EvidenceRef != "E6" || output.Results[1].EvidenceRef != "E1" || !output.Results[0].Valid || !output.Results[1].Valid {
		t.Fatalf("citation results differ: %s", result.Output)
	}
	workspaceAnalysisValidateDomainReceipt(t, workspaceAnalysisCatalogContract(t, request.Tool), result, generatedID(92, 5), generatedID(93, 5))
}

func TestValidateCitationV4RejectsUnopenedRefsAndCandidatePurposeConfusion(t *testing.T) {
	source := dynamicSourceFixture(t, 7, "E1", testIndexID)
	for _, test := range []struct {
		name    string
		request toolsapplication.ExecutorRequest
		mutate  func(*toolsapplication.ValidateCitationV4Authority)
	}{
		{"unopened ref", dynamicExecutorRequest("ValidateCitation", 4, `{"candidate_id":null,"candidate_hash":null,"evidence_refs":["E6"]}`), nil},
		{"workspace drift", dynamicExecutorRequest("ValidateCitation", 4, `{"candidate_id":null,"candidate_hash":null,"evidence_refs":["E1"]}`), func(a *toolsapplication.ValidateCitationV4Authority) { a.WorkspaceID = generatedID(80, 9) }},
		{"missing read", dynamicExecutorRequest("ValidateCitation", 4, `{"candidate_id":null,"candidate_hash":null,"evidence_refs":["E1"]}`), func(a *toolsapplication.ValidateCitationV4Authority) { a.ReadSourceReceipts = nil }},
		{"candidate in loop", dynamicExecutorRequest("ValidateCitation", 4, `{"candidate_id":"`+string(testValidateCitationV3CandidateID)+`","candidate_hash":"`+strings.Repeat("c", 64)+`","evidence_refs":["E1"]}`), nil},
		{"missing candidate key", dynamicExecutorRequest("ValidateCitation", 4, `{"candidate_id":null,"evidence_refs":["E1"]}`), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := dynamicCitationFixture(t, source)
			if test.mutate != nil {
				test.mutate(&authority)
			}
			reference := &validateCitationV3ReferenceFake{}
			eligibility := &validateCitationV3EligibilityFake{}
			executor, err := NewValidateCitationV4Executor(&dynamicCitationAuthorityFake{authority: authority}, reference, eligibility)
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Execute(context.Background(), test.request)
			if err == nil || reference.calls != 0 || eligibility.calls != 0 {
				t.Fatal("unproven citation reached evidence validation")
			}
		})
	}
}

func dynamicExecutorRequest(name string, version int64, arguments string) toolsapplication.ExecutorRequest {
	request := executorRequestVersion(name, version, []byte(arguments))
	request.Identity.DefinitionVersion = 2
	request.Identity.NodeKey = "decide_next"
	return request
}

func dynamicExecutorReceipt(t *testing.T, tool toolsdomain.ToolRef, result toolsapplication.ExecutorResult, id int) toolsdomain.ResultReceipt {
	t.Helper()
	receipt := workspaceAnalysisSearchReceipt(t, 1)
	contract := workspaceAnalysisCatalogContract(t, tool)
	receiptContract, _ := toolsdomain.WorkspaceAnalysisResultReceiptContract(tool)
	receipt.ID, receipt.ToolCallID = generatedID(94, id), generatedID(95, id)
	receipt.Tool, receipt.DefinitionHash, receipt.OutputSchema = tool, contract.Definition.DefinitionHash, contract.Definition.OutputSchema
	receipt.MaxOutputBytes, receipt.MaxPrivateBindingBytes = receiptContract.MaxOutputBytes, receiptContract.MaxPrivateBindingBytes
	var err error
	receipt.Output, err = contract.DecodeOutput(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	receipt.OutputHash, receipt.OutputBytes = hashDocument(receipt.Output), int64(len(receipt.Output))
	if result.PrivateBinding == nil {
		t.Fatal("missing private binding")
	}
	private := workspaceAnalysisCanonicalDocument(t, result.PrivateBinding.Document)
	receipt.PrivateBinding = &toolsdomain.ResultReceiptPrivateBinding{Schema: result.PrivateBinding.Schema, Document: private, Hash: hashDocument(private), Bytes: int64(len(private))}
	return receipt
}

func dynamicSourceFixture(t *testing.T, id int, ref string, index foundation.ID) toolsapplication.ReadSourceV4Authority {
	t.Helper()
	search := workspaceAnalysisSearchResult(1)
	search.IndexVersionID = index
	for i := range search.Items {
		search.Items[i].IndexVersionID = index
	}
	executor, err := NewSearchKnowledgeV3Executor(&searchFake{result: search}, &citationEvidenceStoreFake{bindings: workspaceAnalysisCitationBindings(search)})
	if err != nil {
		t.Fatal(err)
	}
	request := dynamicExecutorRequest("SearchKnowledge", 3, `{"query":"approved evidence","mode":"keyword","limit":1}`)
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	receipt := dynamicExecutorReceipt(t, request.Tool, result, id)
	identities, err := toolsdomain.SearchKnowledgeV3ReceiptIdentities(receipt)
	if err != nil {
		t.Fatal(err)
	}
	identity := identities[0]
	identity.EvidenceRef = ref
	return toolsapplication.ReadSourceV4Authority{WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, AnalysisRunID: generatedID(96, 1), EvidenceRef: ref, SearchEvidenceRef: "E1", SearchReceipt: receipt, Identity: identity}
}

func dynamicCitationFixture(t *testing.T, sources ...toolsapplication.ReadSourceV4Authority) toolsapplication.ValidateCitationV4Authority {
	t.Helper()
	authority := toolsapplication.ValidateCitationV4Authority{WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, AnalysisRunID: generatedID(96, 1)}
	for i, source := range sources {
		executor, err := NewReadSourceV4Executor(&dynamicSourceAuthorityFake{authority: source}, &readSourceV3ReferenceFake{view: workspaceAnalysisSourceView(readSourceIdentityFromDynamic(source.Identity))})
		if err != nil {
			t.Fatal(err)
		}
		request := dynamicExecutorRequest("ReadSource", 4, `{"evidence_ref":"`+source.EvidenceRef+`"}`)
		result, err := executor.Execute(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		authority.EvidenceRefs = append(authority.EvidenceRefs, source.EvidenceRef)
		authority.SearchReceipts = append(authority.SearchReceipts, source.SearchReceipt)
		authority.ReadSourceReceipts = append(authority.ReadSourceReceipts, dynamicExecutorReceipt(t, request.Tool, result, 20+i))
	}
	return authority
}

type dynamicSourceAuthorityFake struct {
	query     toolsapplication.ReadSourceV4AuthorityQuery
	authority toolsapplication.ReadSourceV4Authority
	calls     int
}

func (f *dynamicSourceAuthorityFake) LoadReadSourceV4Authority(_ context.Context, query toolsapplication.ReadSourceV4AuthorityQuery) (toolsapplication.ReadSourceV4Authority, error) {
	f.calls++
	f.query = query
	return f.authority, nil
}

type dynamicCitationAuthorityFake struct {
	query     toolsapplication.ValidateCitationV4AuthorityQuery
	authority toolsapplication.ValidateCitationV4Authority
}

func (f *dynamicCitationAuthorityFake) LoadValidateCitationV4Authority(_ context.Context, query toolsapplication.ValidateCitationV4AuthorityQuery) (toolsapplication.ValidateCitationV4Authority, error) {
	f.query = query
	return f.authority, nil
}

type dynamicCitationReferenceFake struct {
	inner   validateCitationV3ReferenceFake
	batches [][]retrievaldomain.CitationReferenceQuery
}

func (f *dynamicCitationReferenceFake) OpenCitationEvidenceBatch(ctx context.Context, queries []retrievaldomain.CitationReferenceQuery) ([]retrievalapplication.OpenedCitationEvidence, error) {
	for _, query := range queries {
		if query.IndexVersionID != queries[0].IndexVersionID {
			return nil, errors.New("cross-index batch is forbidden by the evidence owner")
		}
	}
	f.batches = append(f.batches, slices.Clone(queries))
	return f.inner.OpenCitationEvidenceBatch(ctx, queries)
}
