package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestSearchKnowledgeV2ExecutorFreezesShortRefsAndPrivateIdentities(t *testing.T) {
	searchResult := workspaceAnalysisSearchResult(5)
	search := &searchFake{result: searchResult}
	citations := &citationEvidenceStoreFake{bindings: workspaceAnalysisCitationBindings(searchResult)}
	executor, err := NewSearchKnowledgeV2Executor(search, citations)
	if err != nil {
		t.Fatal(err)
	}
	arguments := []byte(`{"query":"  approved evidence  ","mode":"keyword","limit":5}`)
	original := append([]byte(nil), arguments...)
	result, err := executor.Execute(context.Background(), executorRequestVersion(searchKnowledgeName, searchKnowledgeV2Version, arguments))
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments) != string(original) {
		t.Fatal("executor mutated server-built arguments")
	}
	if search.calls != 1 || search.request.WorkspaceID != testWorkspaceID || search.request.Query != "approved evidence" ||
		search.request.Limit != 5 || len(search.request.Filter.SourceIDs) != 0 || len(search.request.Filter.SourceVersionIDs) != 0 {
		t.Fatalf("search request=%+v calls=%d", search.request, search.calls)
	}
	if citations.calls != 1 || len(citations.queries) != 5 {
		t.Fatalf("citation binding calls=%d queries=%d", citations.calls, len(citations.queries))
	}

	contract := workspaceAnalysisCatalogContract(t, searchKnowledgeV2Ref)
	if _, err := contract.DecodeOutput(result.Output); err != nil {
		t.Fatalf("catalog rejected SearchKnowledge@2 output: %v", err)
	}
	var output searchKnowledgeV2Output
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.EffectiveMode != "keyword" || len(output.Items) != 5 || output.Items[0].EvidenceRef != "E1" ||
		output.Items[4].EvidenceRef != "E5" || output.Items[4].Rank != 5 || output.Degradations == nil {
		t.Fatalf("output=%s", result.Output)
	}
	for _, forbidden := range []string{"citation_id", "content_hash", "index_version_id", "source_version_id", "source_span_id", string(testIndexID)} {
		if strings.Contains(string(result.Output), forbidden) {
			t.Fatalf("model-visible search output leaks %q: %s", forbidden, result.Output)
		}
	}

	receiptContract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(searchKnowledgeV2Ref)
	if !found || result.ResultRef != "" || result.PrivateBinding == nil || result.PrivateBinding.Schema != receiptContract.PrivateBindingSchema ||
		int64(len(result.Output)) > receiptContract.MaxOutputBytes || int64(len(result.PrivateBinding.Document)) > receiptContract.MaxPrivateBindingBytes {
		t.Fatalf("receipt contract mismatch: found=%t result_ref=%q binding=%v", found, result.ResultRef, result.PrivateBinding)
	}
	var binding searchKnowledgeV2PrivateBinding
	if err := json.Unmarshal(result.PrivateBinding.Document, &binding); err != nil {
		t.Fatal(err)
	}
	if len(binding.Items) != 5 || strings.Join(binding.SelectedRefs, ",") != "E1,E2,E3" {
		t.Fatalf("binding=%s", result.PrivateBinding.Document)
	}
	for index, identity := range binding.Items {
		item := search.result.Items[index]
		provenance := item.Provenances[0]
		if identity.EvidenceRef != evidenceRef(index+1) || identity.IndexVersionID != testIndexID || identity.ChunkID != item.ChunkID ||
			identity.SourceVersionID != provenance.SourceVersionID || identity.SourceSpanID != item.Span.ID ||
			identity.ContentHash != citations.bindings[index].Reference.ExcerptHash || identity.ContentHash == item.ContentHash ||
			identity.CitationID != citationID(testWorkspaceID, testIndexID, item.ChunkID, provenance.SourceVersionID, item.Span.ID) {
			t.Fatalf("identity[%d]=%+v", index, identity)
		}
	}
	for _, forbidden := range []string{"model snippet", "private body", "docs/private", "relative_path", "snippet", "excerpt"} {
		if strings.Contains(string(result.PrivateBinding.Document), forbidden) {
			t.Fatalf("private binding leaks %q: %s", forbidden, result.PrivateBinding.Document)
		}
	}
	workspaceAnalysisValidateDomainReceipt(t, contract, result, generatedID(92, 1), generatedID(93, 1))
}

func TestSearchKnowledgeV2ExecutorUsesSpanHashWhenChunkHashDiffers(t *testing.T) {
	searchResult := workspaceAnalysisSearchResult(1)
	bindings := workspaceAnalysisCitationBindings(searchResult)
	if searchResult.Items[0].ContentHash == bindings[0].Reference.ExcerptHash {
		t.Fatal("fixture must keep chunk and Source Span hashes distinct")
	}
	executor, err := NewSearchKnowledgeV2Executor(
		&searchFake{result: searchResult}, &citationEvidenceStoreFake{bindings: bindings},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), executorRequestVersion(
		searchKnowledgeName, searchKnowledgeV2Version, []byte(`{"query":"evidence","mode":"keyword","limit":1}`),
	))
	if err != nil {
		t.Fatal(err)
	}
	var privateBinding searchKnowledgeV2PrivateBinding
	if result.PrivateBinding == nil {
		t.Fatalf("private binding=%v", result.PrivateBinding)
	}
	if err := json.Unmarshal(result.PrivateBinding.Document, &privateBinding); err != nil {
		t.Fatal(err)
	}
	if len(privateBinding.Items) != 1 {
		t.Fatalf("private binding=%s", result.PrivateBinding.Document)
	}
	if privateBinding.Items[0].ContentHash != bindings[0].Reference.ExcerptHash ||
		privateBinding.Items[0].ContentHash == searchResult.Items[0].ContentHash {
		t.Fatalf("private content_hash=%q chunk_hash=%q span_hash=%q",
			privateBinding.Items[0].ContentHash, searchResult.Items[0].ContentHash, bindings[0].Reference.ExcerptHash)
	}
}

func TestSearchKnowledgeV2ExecutorRejectsUntrustedInputAndOversizedDocuments(t *testing.T) {
	valid := `{"query":"evidence","mode":"keyword","limit":5}`
	tests := []struct {
		name      string
		arguments string
		version   int64
		nilCtx    bool
	}{
		{name: "workspace id", arguments: `{"query":"evidence","mode":"keyword","limit":5,"workspace_id":"forged"}`, version: 2},
		{name: "source ids", arguments: `{"query":"evidence","mode":"keyword","limit":5,"source_ids":[]}`, version: 2},
		{name: "six hits", arguments: `{"query":"evidence","mode":"keyword","limit":6}`, version: 2},
		{name: "wrong version", arguments: valid, version: 1},
		{name: "nil context", arguments: valid, version: 2, nilCtx: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			searchResult := workspaceAnalysisSearchResult(5)
			search := &searchFake{result: searchResult}
			citations := &citationEvidenceStoreFake{bindings: workspaceAnalysisCitationBindings(searchResult)}
			executor, err := NewSearchKnowledgeV2Executor(search, citations)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Context(context.Background())
			if test.nilCtx {
				ctx = nil
			}
			_, err = executor.Execute(ctx, executorRequestVersion(searchKnowledgeName, test.version, []byte(test.arguments)))
			requireErrorCode(t, err, errorCodeSearchInputInvalid)
			if search.calls != 0 || citations.calls != 0 {
				t.Fatalf("untrusted input reached dependencies: search=%d citations=%d", search.calls, citations.calls)
			}
		})
	}

	tests = []struct {
		name      string
		arguments string
		version   int64
		nilCtx    bool
	}{
		{name: "nul snippet", arguments: valid, version: 2},
		{name: "escaped output exceeds 32 KiB", arguments: valid, version: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			searchResult := workspaceAnalysisSearchResult(5)
			if test.name == "nul snippet" {
				searchResult.Items[0].Snippet = "unsafe\x00snippet"
			} else {
				for index := range searchResult.Items {
					searchResult.Items[index].Snippet = strings.Repeat(`"`, retrievaldomain.MaxEvidenceSnippetBytes)
				}
			}
			executor, err := NewSearchKnowledgeV2Executor(
				&searchFake{result: searchResult},
				&citationEvidenceStoreFake{bindings: workspaceAnalysisCitationBindings(searchResult)},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Execute(context.Background(), executorRequestVersion(searchKnowledgeName, 2, []byte(valid)))
			requireErrorCode(t, err, errorCodeSearchResultInvalid)
		})
	}
}

func TestSearchKnowledgeV2ExecutorReturnsCanonicalEmptySlices(t *testing.T) {
	searchResult := workspaceAnalysisSearchResult(0)
	citations := &citationEvidenceStoreFake{}
	executor, err := NewSearchKnowledgeV2Executor(&searchFake{result: searchResult}, citations)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), executorRequestVersion(
		searchKnowledgeName, searchKnowledgeV2Version, []byte(`{"query":"none","mode":"keyword","limit":1}`),
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Output), `"items":[]`) || !strings.Contains(string(result.Output), `"degradations":[]`) ||
		result.PrivateBinding == nil || !strings.Contains(string(result.PrivateBinding.Document), `"selected_refs":[]`) {
		t.Fatalf("empty result is not encoded as arrays: output=%s binding=%v", result.Output, result.PrivateBinding)
	}
	if citations.calls != 0 {
		t.Fatalf("zero-hit search issued an empty citation query: calls=%d", citations.calls)
	}
}

func TestSearchKnowledgeV2ExecutorRejectsCitationBindingDrift(t *testing.T) {
	searchResult := workspaceAnalysisSearchResult(2)
	tests := []struct {
		name   string
		mutate func([]retrievalapplication.CitationSourceSpanBinding) []retrievalapplication.CitationSourceSpanBinding
	}{
		{name: "incomplete batch", mutate: func(values []retrievalapplication.CitationSourceSpanBinding) []retrievalapplication.CitationSourceSpanBinding {
			return values[:1]
		}},
		{name: "query order", mutate: func(values []retrievalapplication.CitationSourceSpanBinding) []retrievalapplication.CitationSourceSpanBinding {
			values[0], values[1] = values[1], values[0]
			return values
		}},
		{name: "projection", mutate: func(values []retrievalapplication.CitationSourceSpanBinding) []retrievalapplication.CitationSourceSpanBinding {
			values[0].Reference.ParseProjectionID = generatedID(99, 1)
			return values
		}},
		{name: "excerpt hash", mutate: func(values []retrievalapplication.CitationSourceSpanBinding) []retrievalapplication.CitationSourceSpanBinding {
			values[0].Reference.ExcerptHash = strings.Repeat("A", 64)
			return values
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bindings := workspaceAnalysisCitationBindings(searchResult)
			bindings = test.mutate(bindings)
			executor, err := NewSearchKnowledgeV2Executor(
				&searchFake{result: searchResult}, &citationEvidenceStoreFake{bindings: bindings},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Execute(context.Background(), executorRequestVersion(
				searchKnowledgeName, 2, []byte(`{"query":"evidence","mode":"keyword","limit":2}`),
			))
			requireErrorCode(t, err, errorCodeSearchResultInvalid)
		})
	}
}

func TestReadSourceV3ReceiptResolverScopesSelectedReferences(t *testing.T) {
	receipt := workspaceAnalysisSearchReceipt(t, 5)
	reader := &searchKnowledgeV2ReceiptReaderFake{receipt: receipt}
	resolver, err := NewReadSourceV3ReceiptResolver(reader)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolver.ResolveReadSourceV3(context.Background(), ReadSourceV3ResolveRequest{
		WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, EvidenceRef: "E2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || reader.workspaceID != testWorkspaceID || reader.workflowRunID != testRunID ||
		resolution.WorkspaceID != testWorkspaceID || resolution.WorkflowRunID != testRunID ||
		resolution.SearchReceiptID != receipt.ID || resolution.SearchReceiptHash != receipt.OutputHash || resolution.Identity.EvidenceRef != "E2" {
		t.Fatalf("resolution=%+v reader_calls=%d", resolution, reader.calls)
	}

	shortReader := &searchKnowledgeV2ReceiptReaderFake{receipt: workspaceAnalysisSearchReceipt(t, 2)}
	shortResolver, err := NewReadSourceV3ReceiptResolver(shortReader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = shortResolver.ResolveReadSourceV3(context.Background(), ReadSourceV3ResolveRequest{
		WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, EvidenceRef: "E3",
	})
	requireErrorCode(t, err, errorCodeReadSourceReferenceDenied)
}

func TestReadSourceV3ReceiptResolverRejectsReceiptDrift(t *testing.T) {
	base := workspaceAnalysisSearchReceipt(t, 3)
	tests := []struct {
		name   string
		mutate func(*toolsdomain.ResultReceipt)
	}{
		{name: "cross run", mutate: func(value *toolsdomain.ResultReceipt) { value.WorkflowRunID = generatedID(96, 1) }},
		{name: "missing node", mutate: func(value *toolsdomain.ResultReceipt) { value.NodeRunID = "" }},
		{name: "duplicate id", mutate: func(value *toolsdomain.ResultReceipt) { value.NodeAttemptID = value.NodeRunID }},
		{name: "definition hash", mutate: func(value *toolsdomain.ResultReceipt) { value.DefinitionHash = strings.Repeat("D", 64) }},
		{name: "output hash", mutate: func(value *toolsdomain.ResultReceipt) { value.OutputHash = strings.Repeat("0", 64) }},
		{name: "non-canonical output", mutate: func(value *toolsdomain.ResultReceipt) {
			value.Output = append(json.RawMessage{' '}, value.Output...)
			value.OutputBytes = int64(len(value.Output))
			value.OutputHash = hashDocument(value.Output)
		}},
		{name: "missing binding", mutate: func(value *toolsdomain.ResultReceipt) { value.PrivateBinding = nil }},
		{name: "binding hash", mutate: func(value *toolsdomain.ResultReceipt) { value.PrivateBinding.Hash = strings.Repeat("0", 64) }},
		{name: "non-canonical binding", mutate: func(value *toolsdomain.ResultReceipt) {
			value.PrivateBinding.Document = append(json.RawMessage{' '}, value.PrivateBinding.Document...)
			value.PrivateBinding.Bytes = int64(len(value.PrivateBinding.Document))
			value.PrivateBinding.Hash = hashDocument(value.PrivateBinding.Document)
		}},
		{name: "sub-microsecond created at", mutate: func(value *toolsdomain.ResultReceipt) { value.CreatedAt = value.CreatedAt.Add(time.Nanosecond) }},
		{name: "binding body field", mutate: func(value *toolsdomain.ResultReceipt) {
			var document map[string]any
			if err := json.Unmarshal(value.PrivateBinding.Document, &document); err != nil {
				t.Fatal(err)
			}
			document["source_body"] = "private body"
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			value.PrivateBinding.Document = encoded
			value.PrivateBinding.Bytes = int64(len(encoded))
			value.PrivateBinding.Hash = hashDocument(encoded)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receipt := cloneWorkspaceAnalysisReceipt(base)
			test.mutate(&receipt)
			resolver, err := NewReadSourceV3ReceiptResolver(&searchKnowledgeV2ReceiptReaderFake{receipt: receipt})
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.ResolveReadSourceV3(context.Background(), ReadSourceV3ResolveRequest{
				WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, EvidenceRef: "E1",
			})
			requireErrorCode(t, err, errorCodeReadSourceReceiptInvalid)
		})
	}
}

func TestReadSourceV3ExecutorOpensFrozenTupleAndBuildsPrivateBinding(t *testing.T) {
	identity := workspaceAnalysisIdentity("E1")
	resolution := workspaceAnalysisResolution(identity)
	resolver := &readSourceV3ResolverFake{resolution: resolution}
	reference := &readSourceV3ReferenceFake{view: workspaceAnalysisSourceView(identity)}
	executor, err := NewReadSourceV3Executor(resolver, reference)
	if err != nil {
		t.Fatal(err)
	}
	arguments := []byte(`{"evidence_ref":"E1"}`)
	original := append([]byte(nil), arguments...)
	result, err := executor.Execute(context.Background(), executorRequestVersion(readSourceName, readSourceV3Version, arguments))
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments) != string(original) || resolver.calls != 1 || resolver.request.WorkspaceID != testWorkspaceID ||
		resolver.request.WorkflowRunID != testRunID || resolver.request.EvidenceRef != "E1" || reference.calls != 1 {
		t.Fatalf("resolver=%+v resolver_calls=%d reference_calls=%d", resolver.request, resolver.calls, reference.calls)
	}
	wantQuery := retrievaldomain.CitationReferenceQuery{
		WorkspaceID: testWorkspaceID, IndexVersionID: identity.IndexVersionID, ChunkID: identity.ChunkID,
		SourceVersionID: identity.SourceVersionID, SourceSpanID: identity.SourceSpanID,
	}
	if reference.query != wantQuery {
		t.Fatalf("citation query=%+v", reference.query)
	}
	if _, err := workspaceAnalysisCatalogContract(t, readSourceV3Ref).DecodeOutput(result.Output); err != nil {
		t.Fatalf("catalog rejected ReadSource@3 output: %v", err)
	}
	var output readSourceV3Output
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.EvidenceRef != "E1" || output.ContentHash != identity.ContentHash || output.Truncated || output.Excerpt != "immutable excerpt" || result.ResultRef != "" {
		t.Fatalf("output=%s result_ref=%q", result.Output, result.ResultRef)
	}
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(readSourceV3Ref)
	if !found || result.PrivateBinding == nil || result.PrivateBinding.Schema != contract.PrivateBindingSchema ||
		int64(len(result.Output)) > contract.MaxOutputBytes || int64(len(result.PrivateBinding.Document)) > contract.MaxPrivateBindingBytes {
		t.Fatalf("receipt result contract mismatch: found=%t binding=%v", found, result.PrivateBinding)
	}
	var binding readSourceV3PrivateBinding
	if err := json.Unmarshal(result.PrivateBinding.Document, &binding); err != nil {
		t.Fatal(err)
	}
	if binding.SearchReceiptID != resolution.SearchReceiptID || binding.SearchReceiptHash != resolution.SearchReceiptHash || binding.identity() != identity {
		t.Fatalf("private binding=%s", result.PrivateBinding.Document)
	}
	for _, forbidden := range []string{"immutable excerpt", "docs/evidence.md", "relative_path", "source_body", "excerpt"} {
		if strings.Contains(string(result.PrivateBinding.Document), forbidden) {
			t.Fatalf("source private binding leaks %q: %s", forbidden, result.PrivateBinding.Document)
		}
	}
	workspaceAnalysisValidateDomainReceipt(
		t, workspaceAnalysisCatalogContract(t, readSourceV3Ref), result, generatedID(92, 2), generatedID(93, 2),
	)
}

func TestReadSourceV3ExecutorUsesSpanHashWhenSourceVersionHashDiffers(t *testing.T) {
	identity := workspaceAnalysisIdentity("E1")
	view := workspaceAnalysisSourceView(identity)
	view.Reference.SourceVersion.ContentHash = hashText("whole source version")
	if view.Reference.SourceVersion.ContentHash == identity.ContentHash {
		t.Fatal("fixture must keep Source Version and Source Span hashes distinct")
	}
	executor, err := NewReadSourceV3Executor(
		&readSourceV3ResolverFake{resolution: workspaceAnalysisResolution(identity)},
		&readSourceV3ReferenceFake{view: view},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), executorRequestVersion(readSourceName, 3, []byte(`{"evidence_ref":"E1"}`)))
	if err != nil {
		t.Fatal(err)
	}
	var output readSourceV3Output
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.ContentHash != identity.ContentHash || output.ContentHash == view.Reference.SourceVersion.ContentHash {
		t.Fatalf("output_hash=%q source_version_hash=%q span_hash=%q",
			output.ContentHash, view.Reference.SourceVersion.ContentHash, view.Reference.ExcerptHash)
	}
}

func TestReadSourceV3ExecutorUsesFullSpanHashForTruncatedExcerpt(t *testing.T) {
	identity := workspaceAnalysisIdentity("E1")
	identity.ContentHash = hashText("complete source span beyond the returned prefix")
	view := workspaceAnalysisSourceView(identity)
	view.ExcerptTruncated = true
	if hashText(view.Excerpt) == identity.ContentHash {
		t.Fatal("fixture must keep returned prefix and complete Source Span hashes distinct")
	}
	executor, err := NewReadSourceV3Executor(
		&readSourceV3ResolverFake{resolution: workspaceAnalysisResolution(identity)},
		&readSourceV3ReferenceFake{view: view},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), executorRequestVersion(readSourceName, 3, []byte(`{"evidence_ref":"E1"}`)))
	if err != nil {
		t.Fatal(err)
	}
	var output readSourceV3Output
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if !output.Truncated || output.ContentHash != identity.ContentHash || hashText(output.Excerpt) == output.ContentHash {
		t.Fatalf("output=%s", result.Output)
	}
}

func TestReadSourceV3ExecutorRejectsUntrustedInputAndResolverDrift(t *testing.T) {
	identity := workspaceAnalysisIdentity("E1")
	validResolution := workspaceAnalysisResolution(identity)
	tests := []struct {
		name      string
		arguments string
		version   int64
		nilCtx    bool
	}{
		{name: "workspace id", arguments: `{"evidence_ref":"E1","workspace_id":"forged"}`, version: 3},
		{name: "full source id", arguments: `{"evidence_ref":"E1","source_version_id":"` + string(testSourceVersionID) + `"}`, version: 3},
		{name: "unselected ref", arguments: `{"evidence_ref":"E4"}`, version: 3},
		{name: "wrong version", arguments: `{"evidence_ref":"E1"}`, version: 2},
		{name: "nil context", arguments: `{"evidence_ref":"E1"}`, version: 3, nilCtx: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := &readSourceV3ResolverFake{resolution: validResolution}
			reference := &readSourceV3ReferenceFake{view: workspaceAnalysisSourceView(identity)}
			executor, err := NewReadSourceV3Executor(resolver, reference)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Context(context.Background())
			if test.nilCtx {
				ctx = nil
			}
			_, err = executor.Execute(ctx, executorRequestVersion(readSourceName, test.version, []byte(test.arguments)))
			requireErrorCode(t, err, errorCodeReadSourceInputInvalid)
			if resolver.calls != 0 || reference.calls != 0 {
				t.Fatalf("untrusted input reached dependencies: resolver=%d reference=%d", resolver.calls, reference.calls)
			}
		})
	}

	driftTests := []struct {
		name   string
		mutate func(*ReadSourceV3Resolution)
	}{
		{name: "workspace", mutate: func(value *ReadSourceV3Resolution) { value.WorkspaceID = generatedID(97, 1) }},
		{name: "run", mutate: func(value *ReadSourceV3Resolution) { value.WorkflowRunID = generatedID(97, 2) }},
		{name: "requested ref", mutate: func(value *ReadSourceV3Resolution) { value.Identity.EvidenceRef = "E2" }},
		{name: "receipt id", mutate: func(value *ReadSourceV3Resolution) { value.SearchReceiptID = "forged" }},
		{name: "receipt hash", mutate: func(value *ReadSourceV3Resolution) { value.SearchReceiptHash = strings.Repeat("A", 64) }},
		{name: "citation id", mutate: func(value *ReadSourceV3Resolution) { value.Identity.CitationID = "forged" }},
	}
	for _, test := range driftTests {
		t.Run("resolver "+test.name, func(t *testing.T) {
			resolution := workspaceAnalysisResolution(identity)
			test.mutate(&resolution)
			resolver := &readSourceV3ResolverFake{resolution: resolution}
			reference := &readSourceV3ReferenceFake{view: workspaceAnalysisSourceView(identity)}
			executor, err := NewReadSourceV3Executor(resolver, reference)
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Execute(context.Background(), executorRequestVersion(readSourceName, 3, []byte(`{"evidence_ref":"E1"}`)))
			requireErrorCode(t, err, errorCodeReadSourceReceiptInvalid)
			if reference.calls != 0 {
				t.Fatalf("drifted resolution reached source: calls=%d", reference.calls)
			}
		})
	}
}

func TestReadSourceV3ExecutorRejectsOpenedSourceDriftAndOversizedExcerpt(t *testing.T) {
	identity := workspaceAnalysisIdentity("E1")
	tests := []struct {
		name   string
		mutate func(*retrievalapplication.SourceSpanView)
	}{
		{name: "excerpt hash", mutate: func(value *retrievalapplication.SourceSpanView) {
			value.Reference.ExcerptHash = strings.Repeat("f", 64)
		}},
		{name: "source version", mutate: func(value *retrievalapplication.SourceSpanView) {
			value.Reference.SourceVersion.SourceVersionID = generatedID(98, 1)
		}},
		{name: "span", mutate: func(value *retrievalapplication.SourceSpanView) { value.Reference.Span.ID = generatedID(98, 2) }},
		{name: "nul excerpt", mutate: func(value *retrievalapplication.SourceSpanView) { value.Excerpt = "unsafe\x00excerpt" }},
		{name: "excerpt over 4 KiB", mutate: func(value *retrievalapplication.SourceSpanView) {
			value.Excerpt = strings.Repeat("x", maxWorkspaceAnalysisExcerpt+1)
			value.Reference.SourceVersion.ByteSize = int64(len(value.Excerpt))
			value.Reference.SourceVersion.ContentHash = hashText(value.Excerpt)
			value.Reference.Span.EndByte = int64(len(value.Excerpt))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := workspaceAnalysisSourceView(identity)
			test.mutate(&view)
			executor, err := NewReadSourceV3Executor(
				&readSourceV3ResolverFake{resolution: workspaceAnalysisResolution(identity)},
				&readSourceV3ReferenceFake{view: view},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Execute(context.Background(), executorRequestVersion(readSourceName, 3, []byte(`{"evidence_ref":"E1"}`)))
			requireErrorCode(t, err, errorCodeReadSourceResultInvalid)
		})
	}
}

func TestWorkspaceAnalysisRetrievalExecutorsPreserveDependencyErrors(t *testing.T) {
	dependencyErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "RETRIEVAL_DOWN", true, errors.New("down"))
	searchExecutor, err := NewSearchKnowledgeV2Executor(&searchFake{err: dependencyErr}, &citationEvidenceStoreFake{})
	if err != nil {
		t.Fatal(err)
	}
	if _, actual := searchExecutor.Execute(context.Background(), executorRequestVersion(
		searchKnowledgeName, 2, []byte(`{"query":"evidence","mode":"keyword","limit":1}`),
	)); !errors.Is(actual, dependencyErr) {
		t.Fatalf("search error=%v", actual)
	}
	citationErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "CITATION_BINDING_DOWN", true, errors.New("down"))
	searchResult := workspaceAnalysisSearchResult(1)
	searchExecutor, err = NewSearchKnowledgeV2Executor(
		&searchFake{result: searchResult}, &citationEvidenceStoreFake{err: citationErr},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, actual := searchExecutor.Execute(context.Background(), executorRequestVersion(
		searchKnowledgeName, 2, []byte(`{"query":"evidence","mode":"keyword","limit":1}`),
	)); !errors.Is(actual, citationErr) {
		t.Fatalf("citation binding error=%v", actual)
	}

	identity := workspaceAnalysisIdentity("E1")
	resolverErr := foundation.NewError(foundation.ErrorConsistencyViolation, "RECEIPT_DOWN", false, errors.New("drift"))
	readExecutor, err := NewReadSourceV3Executor(
		&readSourceV3ResolverFake{err: resolverErr}, &readSourceV3ReferenceFake{view: workspaceAnalysisSourceView(identity)},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := executorRequestVersion(readSourceName, 3, []byte(`{"evidence_ref":"E1"}`))
	if _, actual := readExecutor.Execute(context.Background(), request); !errors.Is(actual, resolverErr) {
		t.Fatalf("resolver error=%v", actual)
	}

	referenceErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "SOURCE_DOWN", true, errors.New("down"))
	readExecutor, err = NewReadSourceV3Executor(
		&readSourceV3ResolverFake{resolution: workspaceAnalysisResolution(identity)}, &readSourceV3ReferenceFake{err: referenceErr},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, actual := readExecutor.Execute(context.Background(), request); !errors.Is(actual, referenceErr) {
		t.Fatalf("reference error=%v", actual)
	}
}

func workspaceAnalysisSearchResult(count int) retrievaldomain.SearchResult {
	base := validSearchResult()
	base.Items = make([]retrievaldomain.EvidenceV1, count)
	for index := range base.Items {
		item := validSearchResult().Items[0]
		item.ChunkID = generatedID(82, index)
		item.ParseProjectionID = generatedID(83, index)
		item.Sequence = int32(index)
		item.ContentHash = hashText("private body " + evidenceRef(index+1))
		item.Snippet = "model snippet " + evidenceRef(index+1)
		item.Span.ID = generatedID(84, index)
		item.Span.EndByte = int64(len("private body " + evidenceRef(index+1)))
		item.Provenances = []retrievaldomain.EvidenceProvenance{{
			SourceID: generatedID(85, index), SourceVersionID: generatedID(86, index),
			RelativePath: "docs/private/" + evidenceRef(index+1) + ".md", CapturedAt: item.Provenances[0].CapturedAt,
		}}
		item.Lexical = &retrievaldomain.CandidateStageScore{Rank: int32(index + 1), Score: 0.8 - float64(index)/100}
		item.Fusion = retrievaldomain.CandidateStageScore{Rank: int32(index + 1), Score: 0.8 - float64(index)/100}
		base.Items[index] = item
	}
	return base
}

func workspaceAnalysisSearchReceipt(t *testing.T, hits int) toolsdomain.ResultReceipt {
	t.Helper()
	searchResult := workspaceAnalysisSearchResult(hits)
	output, binding, err := searchKnowledgeV2Documents(
		testWorkspaceID, searchResult, workspaceAnalysisCitationBindings(searchResult),
	)
	if err != nil {
		t.Fatal(err)
	}
	output, err = workspaceAnalysisCatalogContract(t, searchKnowledgeV2Ref).DecodeOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	binding = workspaceAnalysisCanonicalDocument(t, binding)
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(searchKnowledgeV2Ref)
	if !found {
		t.Fatal("SearchKnowledge@2 receipt contract not found")
	}
	return toolsdomain.ResultReceipt{
		ID: generatedID(90, 1), ToolCallID: generatedID(91, 1), WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID,
		NodeRunID: testNodeRunID, NodeAttemptID: testAttemptID, Tool: searchKnowledgeV2Ref, OutputSchema: contract.OutputSchema,
		DefinitionHash: strings.Repeat("d", 64), PersistencePolicy: toolsdomain.ResultPersistenceCanonical,
		MaxOutputBytes: contract.MaxOutputBytes, MaxPrivateBindingBytes: contract.MaxPrivateBindingBytes,
		Output: output, OutputHash: hashDocument(output), OutputBytes: int64(len(output)),
		PrivateBinding: &toolsdomain.ResultReceiptPrivateBinding{
			Schema: contract.PrivateBindingSchema, Document: binding, Hash: hashDocument(binding), Bytes: int64(len(binding)),
		},
		CreatedAt: time.Date(2026, 8, 15, 9, 0, 0, 123000000, time.UTC),
	}
}

func workspaceAnalysisCitationBindings(result retrievaldomain.SearchResult) []retrievalapplication.CitationSourceSpanBinding {
	bindings := make([]retrievalapplication.CitationSourceSpanBinding, len(result.Items))
	for index, item := range result.Items {
		provenance := item.Provenances[0]
		query := retrievaldomain.CitationReferenceQuery{
			WorkspaceID: result.WorkspaceID, IndexVersionID: result.IndexVersionID, ChunkID: item.ChunkID,
			SourceVersionID: provenance.SourceVersionID, SourceSpanID: item.Span.ID,
		}
		view := validSourceView(query.SourceVersionID, query.SourceSpanID)
		view.Reference.ParseProjectionID = item.ParseProjectionID
		view.Reference.Span = item.Span
		view.Reference.ExcerptHash = hashText("source span " + evidenceRef(index+1))
		bindings[index] = retrievalapplication.CitationSourceSpanBinding{Query: query, Reference: view.Reference}
	}
	return bindings
}

func workspaceAnalysisIdentity(ref string) ReadSourceV3Identity {
	identity := ReadSourceV3Identity{
		EvidenceRef: ref, IndexVersionID: testIndexID, ChunkID: testChunkID,
		SourceVersionID: testSourceVersionID, SourceSpanID: testSpanID, ContentHash: hashText("immutable excerpt"),
	}
	identity.CitationID = citationID(testWorkspaceID, identity.IndexVersionID, identity.ChunkID, identity.SourceVersionID, identity.SourceSpanID)
	return identity
}

func workspaceAnalysisResolution(identity ReadSourceV3Identity) ReadSourceV3Resolution {
	return ReadSourceV3Resolution{
		WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, SearchReceiptID: generatedID(90, 2),
		SearchReceiptHash: strings.Repeat("e", 64), Identity: identity,
	}
}

func workspaceAnalysisSourceView(identity ReadSourceV3Identity) retrievalapplication.SourceSpanView {
	view := validSourceView(identity.SourceVersionID, identity.SourceSpanID)
	view.Reference.ExcerptHash = identity.ContentHash
	return view
}

func workspaceAnalysisCatalogContract(t *testing.T, ref toolsdomain.ToolRef) toolsapplication.Contract {
	t.Helper()
	contracts, err := catalog.Contracts()
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range contracts {
		if contract.Definition.Ref == ref {
			return contract
		}
	}
	t.Fatalf("catalog contract %s@%d not found", ref.Name, ref.Version)
	return toolsapplication.Contract{}
}

func workspaceAnalysisCanonicalDocument(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func workspaceAnalysisValidateDomainReceipt(
	t *testing.T,
	contract toolsapplication.Contract,
	result toolsapplication.ExecutorResult,
	callID foundation.ID,
	receiptID foundation.ID,
) {
	t.Helper()
	if result.PrivateBinding == nil {
		t.Fatal("executor omitted private binding")
	}
	output, err := contract.DecodeOutput(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	binding := workspaceAnalysisCanonicalDocument(t, result.PrivateBinding.Document)
	startedAt := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(time.Second)
	tool := contract.Definition.Ref
	inputSchema, outputSchema := contract.Definition.InputSchema, contract.Definition.OutputSchema
	call := toolsdomain.ToolCall{
		ID: callID, WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, NodeRunID: testNodeRunID, NodeAttemptID: testAttemptID,
		CallNo: 1, RequestedToolName: tool.Name, Tool: &tool, DefinitionHash: contract.Definition.DefinitionHash,
		InputSchema: &inputSchema, OutputSchema: &outputSchema, Capability: contract.Definition.RequiredCapability,
		SideEffectLevel: contract.Definition.SideEffectLevel, InvocationPolicy: contract.Definition.InvocationPolicy,
		RequestHash: strings.Repeat("a", 64), RequestBytes: 1, RequestSummary: json.RawMessage(`{}`),
		ResponseHash: hashDocument(output), ResponseBytes: int64(len(output)), ResponseSummary: json.RawMessage(`{}`),
		Status: toolsdomain.CallSucceeded, Version: 2, StartedAt: startedAt, CompletedAt: &completedAt, DurationMillis: 1000,
	}
	receipt, err := toolsdomain.NewResultReceipt(toolsdomain.ResultReceiptDraft{
		ID: receiptID, Output: output, PrivateBindingSchema: result.PrivateBinding.Schema,
		PrivateBinding: binding, CreatedAt: completedAt.Add(time.Microsecond),
	}, call, contract.Definition)
	if err != nil {
		t.Fatalf("domain receipt rejected executor documents: %v", err)
	}
	if receipt.PrivateBinding == nil || string(receipt.Output) != string(output) || string(receipt.PrivateBinding.Document) != string(binding) {
		t.Fatalf("domain receipt canonical round-trip mismatch: receipt=%v", receipt)
	}
}

func cloneWorkspaceAnalysisReceipt(receipt toolsdomain.ResultReceipt) toolsdomain.ResultReceipt {
	clone := receipt
	clone.Output = append(json.RawMessage(nil), receipt.Output...)
	if receipt.PrivateBinding != nil {
		binding := *receipt.PrivateBinding
		binding.Document = append(json.RawMessage(nil), receipt.PrivateBinding.Document...)
		clone.PrivateBinding = &binding
	}
	return clone
}

type searchKnowledgeV2ReceiptReaderFake struct {
	calls                      int
	workspaceID, workflowRunID foundation.ID
	receipt                    toolsdomain.ResultReceipt
	err                        error
}

type citationEvidenceStoreFake struct {
	calls    int
	queries  []retrievaldomain.CitationReferenceQuery
	bindings []retrievalapplication.CitationSourceSpanBinding
	err      error
}

func (fake *citationEvidenceStoreFake) LoadCitationSourceSpanReferences(
	_ context.Context,
	queries []retrievaldomain.CitationReferenceQuery,
) ([]retrievalapplication.CitationSourceSpanBinding, error) {
	fake.calls++
	fake.queries = append([]retrievaldomain.CitationReferenceQuery(nil), queries...)
	return append([]retrievalapplication.CitationSourceSpanBinding(nil), fake.bindings...), fake.err
}

func (fake *searchKnowledgeV2ReceiptReaderFake) LoadSearchKnowledgeV2Receipt(
	_ context.Context,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
) (toolsdomain.ResultReceipt, error) {
	fake.calls++
	fake.workspaceID, fake.workflowRunID = workspaceID, workflowRunID
	return fake.receipt, fake.err
}

type readSourceV3ResolverFake struct {
	calls      int
	request    ReadSourceV3ResolveRequest
	resolution ReadSourceV3Resolution
	err        error
}

func (fake *readSourceV3ResolverFake) ResolveReadSourceV3(_ context.Context, request ReadSourceV3ResolveRequest) (ReadSourceV3Resolution, error) {
	fake.calls++
	fake.request = request
	return fake.resolution, fake.err
}

type readSourceV3ReferenceFake struct {
	calls int
	query retrievaldomain.CitationReferenceQuery
	view  retrievalapplication.SourceSpanView
	err   error
}

func (fake *readSourceV3ReferenceFake) OpenCitationEvidence(
	_ context.Context,
	query retrievaldomain.CitationReferenceQuery,
) (retrievalapplication.SourceSpanView, error) {
	fake.calls++
	fake.query = query
	return fake.view, fake.err
}
