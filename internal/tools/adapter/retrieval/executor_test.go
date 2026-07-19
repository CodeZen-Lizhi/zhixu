package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	testWorkspaceID     foundation.ID = "71000000-0000-4000-8000-000000000001"
	testDefinitionID    foundation.ID = "71000000-0000-4000-8000-000000000002"
	testRunID           foundation.ID = "71000000-0000-4000-8000-000000000003"
	testNodeRunID       foundation.ID = "71000000-0000-4000-8000-000000000004"
	testAttemptID       foundation.ID = "71000000-0000-4000-8000-000000000005"
	testIndexID         foundation.ID = "71000000-0000-4000-8000-000000000006"
	testChunkID         foundation.ID = "71000000-0000-4000-8000-000000000007"
	testProjectionID    foundation.ID = "71000000-0000-4000-8000-000000000008"
	testSourceID        foundation.ID = "71000000-0000-4000-8000-000000000009"
	testSourceVersionID foundation.ID = "71000000-0000-4000-8000-000000000010"
	testArtifactID      foundation.ID = "71000000-0000-4000-8000-000000000011"
	testSpanID          foundation.ID = "71000000-0000-4000-8000-000000000012"
)

func TestSearchKnowledgeExecutorUsesTrustedWorkspaceAndMatchesCatalog(t *testing.T) {
	search := &searchFake{result: validSearchResult()}
	executor, err := NewSearchKnowledgeExecutor(search)
	if err != nil {
		t.Fatal(err)
	}
	arguments := []byte(`{"query":"approved evidence","mode":"keyword","limit":10,"source_ids":["71000000-0000-4000-8000-000000000009"],"source_version_ids":["71000000-0000-4000-8000-000000000010"]}`)
	original := append([]byte(nil), arguments...)
	result, err := executor.Execute(context.Background(), executorRequest(searchKnowledgeName, arguments))
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments) != string(original) {
		t.Fatal("executor mutated arguments")
	}
	if search.request.WorkspaceID != testWorkspaceID || search.request.Mode != retrievaldomain.SearchModeKeyword ||
		len(search.request.Filter.SourceIDs) != 1 || search.request.Filter.SourceIDs[0] != testSourceID ||
		len(search.request.Filter.SourceVersionIDs) != 1 || search.request.Filter.SourceVersionIDs[0] != testSourceVersionID {
		t.Fatalf("search request = %#v", search.request)
	}
	decodeCatalogOutput(t, searchKnowledgeName, result.Output)
	var output searchKnowledgeOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.IndexVersionID != string(testIndexID) || len(output.Items) != 1 || output.Items[0].CitationID != citationID(testWorkspaceID, testIndexID, testChunkID, testSourceVersionID, testSpanID) ||
		output.Items[0].ContentHash != strings.Repeat("a", 64) || output.Items[0].Rank != 1 || result.ResultRef != "index-version:"+string(testIndexID) {
		t.Fatalf("output=%s ref=%q", result.Output, result.ResultRef)
	}
	if search.calls != 1 {
		t.Fatalf("search calls=%d, want 1", search.calls)
	}
}

func TestSearchKnowledgeExecutorLoadResultReceiptFailsClosedWithoutSearch(t *testing.T) {
	search := &searchFake{result: validSearchResult()}
	executor, err := NewSearchKnowledgeExecutor(search)
	if err != nil {
		t.Fatal(err)
	}
	request := executorRequest(searchKnowledgeName, validSearchArguments())
	tool := request.Tool
	receipt, err := executor.LoadResultReceipt(context.Background(), request, toolsdomain.ToolCall{
		Status: toolsdomain.CallSucceeded, Tool: &tool, RequestedToolName: searchKnowledgeName,
		ResultRef: "index-version:" + string(testIndexID),
	})
	requireErrorCode(t, err, errorCodeSearchReceiptUnavailable)
	if len(receipt.Output) != 0 || receipt.ResultRef != "" {
		t.Fatalf("receipt=%+v, want empty failed result", receipt)
	}
	if search.calls != 0 {
		t.Fatalf("search calls=%d, want 0", search.calls)
	}
}

func TestSearchKnowledgeExecutorRejectsInputAndInvalidAdapterResult(t *testing.T) {
	executor, err := NewSearchKnowledgeExecutor(&searchFake{result: validSearchResult()})
	if err != nil {
		t.Fatal(err)
	}
	invalidInputs := [][]byte{
		[]byte(`{"query":"q","mode":"keyword","limit":1,"source_ids":[],"source_version_ids":[],"workspace_id":"forged"}`),
		[]byte(`{"query":"q","mode":"keyword","limit":1,"source_ids":null,"source_version_ids":[]}`),
		[]byte(`{"query":"q","mode":"auto","limit":1,"source_ids":[],"source_version_ids":[]}`),
	}
	for _, arguments := range invalidInputs {
		_, err := executor.Execute(context.Background(), executorRequest(searchKnowledgeName, arguments))
		requireErrorCode(t, err, errorCodeSearchInputInvalid)
	}
	invalid := validSearchResult()
	invalid.WorkspaceID = "71000000-0000-4000-8000-000000000099"
	executor, err = NewSearchKnowledgeExecutor(&searchFake{result: invalid})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), executorRequest(searchKnowledgeName, validSearchArguments()))
	requireErrorCode(t, err, errorCodeSearchResultInvalid)
}

func TestSearchKnowledgeExecutorPreservesDependencyAndContextErrors(t *testing.T) {
	dependencyErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "SEARCH_DOWN", false, errors.New("down"))
	executor, err := NewSearchKnowledgeExecutor(&searchFake{err: dependencyErr})
	if err != nil {
		t.Fatal(err)
	}
	_, actual := executor.Execute(context.Background(), executorRequest(searchKnowledgeName, validSearchArguments()))
	if !errors.Is(actual, dependencyErr) {
		t.Fatalf("error=%v", actual)
	}

	executor, err = NewSearchKnowledgeExecutor(&searchFake{waitForContext: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, actual = executor.Execute(ctx, executorRequest(searchKnowledgeName, validSearchArguments()))
	if !errors.Is(actual, context.Canceled) {
		t.Fatalf("cancel error=%v", actual)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	_, actual = executor.Execute(ctx, executorRequest(searchKnowledgeName, validSearchArguments()))
	if !errors.Is(actual, context.DeadlineExceeded) {
		t.Fatalf("deadline error=%v", actual)
	}
}

func TestReadSourceExecutorOpensStableIDsAndValidatesResult(t *testing.T) {
	reference := &evidenceFake{}
	executor, err := NewReadSourceExecutor(reference)
	if err != nil {
		t.Fatal(err)
	}
	arguments := []byte(`{"source_version_id":"71000000-0000-4000-8000-000000000010","source_span_id":"71000000-0000-4000-8000-000000000012"}`)
	original := append([]byte(nil), arguments...)
	result, err := executor.Execute(context.Background(), executorRequest(readSourceName, arguments))
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments) != string(original) || reference.workspaceID != testWorkspaceID || reference.sourceVersionID != testSourceVersionID || reference.spanID != testSpanID {
		t.Fatalf("arguments or binding drifted: workspace=%s version=%s span=%s", reference.workspaceID, reference.sourceVersionID, reference.spanID)
	}
	decodeCatalogOutput(t, readSourceName, result.Output)
	if !strings.Contains(string(result.Output), `"excerpt":"immutable excerpt"`) || result.ResultRef != "source-span:"+string(testSpanID) {
		t.Fatalf("output=%s ref=%q", result.Output, result.ResultRef)
	}

	reference.drift = true
	_, err = executor.Execute(context.Background(), executorRequest(readSourceName, arguments))
	requireErrorCode(t, err, errorCodeReadSourceResultInvalid)
}

func TestReadSourceExecutorPreservesDependencyAndContextErrors(t *testing.T) {
	dependencyErr := foundation.NewError(foundation.ErrorRetryableFailure, "EVIDENCE_DOWN", true, errors.New("down"))
	executor, err := NewReadSourceExecutor(&evidenceFake{err: dependencyErr})
	if err != nil {
		t.Fatal(err)
	}
	arguments := []byte(`{"source_version_id":"71000000-0000-4000-8000-000000000010","source_span_id":"71000000-0000-4000-8000-000000000012"}`)
	_, actual := executor.Execute(context.Background(), executorRequest(readSourceName, arguments))
	if !errors.Is(actual, dependencyErr) {
		t.Fatalf("error=%v", actual)
	}
	executor, err = NewReadSourceExecutor(&evidenceFake{waitForContext: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, actual = executor.Execute(ctx, executorRequest(readSourceName, arguments))
	if !errors.Is(actual, context.Canceled) {
		t.Fatalf("cancel error=%v", actual)
	}
}

func TestValidateCitationExecutorBatchesFiveHundredAndMatchesCatalog(t *testing.T) {
	reference := &evidenceFake{}
	eligibility := &eligibilityFake{}
	executor, err := NewValidateCitationExecutor(reference, eligibility)
	if err != nil {
		t.Fatal(err)
	}
	citations := make([]citationTuple, maxCitationBatchSize)
	for index := range citations {
		chunkID := generatedID(72, index)
		spanID := generatedID(73, index)
		citations[index] = citationTuple{
			IndexVersionID: testIndexID, ChunkID: chunkID, SourceVersionID: testSourceVersionID, SourceSpanID: spanID,
			CitationID: citationID(testWorkspaceID, testIndexID, chunkID, testSourceVersionID, spanID),
		}
	}
	arguments, err := json.Marshal(validateCitationInput{Citations: citations})
	if err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), arguments...)
	result, err := executor.Execute(context.Background(), executorRequest(validateCitationName, arguments))
	if err != nil {
		t.Fatal(err)
	}
	if reference.batchCalls != 1 || reference.batchSize != maxCitationBatchSize || eligibility.calls != 1 || eligibility.batchSize != maxCitationBatchSize || string(arguments) != string(original) {
		t.Fatalf("reference_calls=%d reference_size=%d eligibility_calls=%d eligibility_size=%d mutated=%t", reference.batchCalls, reference.batchSize, eligibility.calls, eligibility.batchSize, string(arguments) != string(original))
	}
	decodeCatalogOutput(t, validateCitationName, result.Output)
	var output validateCitationOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != maxCitationBatchSize || output.Results[0].Valid == nil || !*output.Results[0].Valid || output.Results[0].ReasonCode != "OK" || !strings.HasPrefix(result.ResultRef, "citation-batch:") {
		t.Fatalf("result=%#v ref=%q", output.Results[0], result.ResultRef)
	}
}

func TestValidateCitationExecutorAcceptsCanonicalDependencyOrdering(t *testing.T) {
	citations := []citationTuple{
		{
			IndexVersionID: testIndexID, ChunkID: generatedID(79, 2), SourceVersionID: testSourceVersionID, SourceSpanID: generatedID(78, 2),
		},
		{
			IndexVersionID: testIndexID, ChunkID: generatedID(79, 1), SourceVersionID: testSourceVersionID, SourceSpanID: generatedID(78, 1),
		},
	}
	for index := range citations {
		citations[index].CitationID = citationID(testWorkspaceID, citations[index].IndexVersionID, citations[index].ChunkID, citations[index].SourceVersionID, citations[index].SourceSpanID)
	}
	arguments, err := json.Marshal(validateCitationInput{Citations: citations})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewValidateCitationExecutor(&evidenceFake{canonicalOrder: true}, &eligibilityFake{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), executorRequest(validateCitationName, arguments))
	if err != nil {
		t.Fatal(err)
	}
	var output validateCitationOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != len(citations) || output.Results[0].CitationID != citations[0].CitationID || output.Results[1].CitationID != citations[1].CitationID ||
		output.Results[0].Valid == nil || !*output.Results[0].Valid || output.Results[1].Valid == nil || !*output.Results[1].Valid {
		t.Fatalf("output=%s", result.Output)
	}
}

func TestValidateCitationExecutorReportsForgedIDWithoutCallingDependency(t *testing.T) {
	reference := &evidenceFake{}
	eligibility := &eligibilityFake{}
	executor, err := NewValidateCitationExecutor(reference, eligibility)
	if err != nil {
		t.Fatal(err)
	}
	arguments := []byte(`{"citations":[{"citation_id":"forged","index_version_id":"71000000-0000-4000-8000-000000000006","chunk_id":"71000000-0000-4000-8000-000000000007","source_version_id":"71000000-0000-4000-8000-000000000010","source_span_id":"71000000-0000-4000-8000-000000000012"}]}`)
	result, err := executor.Execute(context.Background(), executorRequest(validateCitationName, arguments))
	if err != nil {
		t.Fatal(err)
	}
	if reference.batchCalls != 0 || eligibility.calls != 0 {
		t.Fatalf("reference calls=%d eligibility calls=%d", reference.batchCalls, eligibility.calls)
	}
	decodeCatalogOutput(t, validateCitationName, result.Output)
	if !strings.Contains(string(result.Output), `"valid":false`) || !strings.Contains(string(result.Output), `"reason_code":"BINDING_MISMATCH"`) {
		t.Fatalf("output=%s", result.Output)
	}
}

func TestValidateCitationExecutorRejectsInvalidAdapterResultAndPreservesErrors(t *testing.T) {
	citation := citationTuple{
		IndexVersionID: testIndexID, ChunkID: testChunkID, SourceVersionID: testSourceVersionID, SourceSpanID: testSpanID,
		CitationID: citationID(testWorkspaceID, testIndexID, testChunkID, testSourceVersionID, testSpanID),
	}
	arguments, _ := json.Marshal(validateCitationInput{Citations: []citationTuple{citation}})
	executor, err := NewValidateCitationExecutor(&evidenceFake{drift: true}, &eligibilityFake{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), executorRequest(validateCitationName, arguments))
	requireErrorCode(t, err, errorCodeCitationResultInvalid)

	dependencyErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "CITATION_DOWN", false, errors.New("down"))
	executor, err = NewValidateCitationExecutor(&evidenceFake{err: dependencyErr}, &eligibilityFake{})
	if err != nil {
		t.Fatal(err)
	}
	_, actual := executor.Execute(context.Background(), executorRequest(validateCitationName, arguments))
	if !errors.Is(actual, dependencyErr) {
		t.Fatalf("error=%v", actual)
	}
	executor, err = NewValidateCitationExecutor(&evidenceFake{waitForContext: true}, &eligibilityFake{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, actual = executor.Execute(ctx, executorRequest(validateCitationName, arguments))
	if !errors.Is(actual, context.Canceled) {
		t.Fatalf("cancel error=%v", actual)
	}

	executor, err = NewValidateCitationExecutor(&evidenceFake{}, &eligibilityFake{drift: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), executorRequest(validateCitationName, arguments))
	requireErrorCode(t, err, errorCodeCitationResultInvalid)

	eligibilityErr := foundation.NewError(foundation.ErrorRetryableFailure, "ELIGIBILITY_DOWN", true, errors.New("down"))
	executor, err = NewValidateCitationExecutor(&evidenceFake{}, &eligibilityFake{err: eligibilityErr})
	if err != nil {
		t.Fatal(err)
	}
	_, actual = executor.Execute(context.Background(), executorRequest(validateCitationName, arguments))
	if !errors.Is(actual, eligibilityErr) {
		t.Fatalf("eligibility error=%v", actual)
	}
}

func TestValidateCitationExecutorReportsIneligibleEvidence(t *testing.T) {
	citation := citationTuple{
		IndexVersionID: testIndexID, ChunkID: testChunkID, SourceVersionID: testSourceVersionID, SourceSpanID: testSpanID,
		CitationID: citationID(testWorkspaceID, testIndexID, testChunkID, testSourceVersionID, testSpanID),
	}
	arguments, _ := json.Marshal(validateCitationInput{Citations: []citationTuple{citation}})
	executor, err := NewValidateCitationExecutor(&evidenceFake{}, &eligibilityFake{ineligible: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), executorRequest(validateCitationName, arguments))
	if err != nil {
		t.Fatal(err)
	}
	decodeCatalogOutput(t, validateCitationName, result.Output)
	if !strings.Contains(string(result.Output), `"valid":false`) || !strings.Contains(string(result.Output), `"reason_code":"EVIDENCE_INELIGIBLE"`) {
		t.Fatalf("output=%s", result.Output)
	}
}

func validSearchArguments() []byte {
	return []byte(`{"query":"approved evidence","mode":"keyword","limit":10,"source_ids":[],"source_version_ids":[]}`)
}

func validSearchResult() retrievaldomain.SearchResult {
	fts, trigram := 0.8, 0.2
	return retrievaldomain.SearchResult{
		WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID,
		RequestedMode: retrievaldomain.SearchModeKeyword, EffectiveMode: retrievaldomain.SearchModeKeyword,
		Items: []retrievaldomain.EvidenceV1{{
			WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID, ChunkID: testChunkID,
			ParseProjectionID: testProjectionID, Sequence: 0, ContentHash: strings.Repeat("a", 64),
			HeadingPath: []string{"Tools"}, Span: retrievaldomain.EvidenceSpan{ID: testSpanID, StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 16},
			Snippet: "bounded evidence", Provenances: []retrievaldomain.EvidenceProvenance{{
				SourceID: testSourceID, SourceVersionID: testSourceVersionID, RelativePath: "docs/evidence.md",
				CapturedAt: time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC),
			}},
			Lexical: &retrievaldomain.CandidateStageScore{Rank: 1, Score: 0.8}, LexicalFTSScore: &fts, LexicalTrigramScore: &trigram,
			Fusion: retrievaldomain.CandidateStageScore{Rank: 1, Score: 0.8},
		}},
	}
}

func validSourceView(sourceVersionID, spanID foundation.ID) retrievalapplication.SourceSpanView {
	excerpt := "immutable excerpt"
	return retrievalapplication.SourceSpanView{
		Reference: retrievaldomain.SourceSpanReference{
			SourceVersion: retrievaldomain.SourceVersionReference{
				WorkspaceID: testWorkspaceID, SourceID: testSourceID, SourceVersionID: sourceVersionID, ContentArtifactID: testArtifactID,
				SourceType: "local_file", LogicalName: "evidence.md", RelativePath: "docs/evidence.md", ContentHash: hashText(excerpt),
				ByteSize: int64(len(excerpt)), MediaType: "text/markdown", SecurityStatus: "passed", CapturedAt: time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC),
			},
			ParseProjectionID: testProjectionID,
			Span:              retrievaldomain.EvidenceSpan{ID: spanID, StartLine: 1, EndLine: 1, StartByte: 0, EndByte: int64(len(excerpt))},
			SpanType:          "section", Selector: json.RawMessage(`{"heading":["Evidence"]}`), ExcerptHash: hashText(excerpt),
			ParserVersion: "goldmark-1", SchemaVersion: "parse-v1",
		},
		Excerpt: excerpt,
	}
}

type searchFake struct {
	calls          int
	request        retrievaldomain.SearchRequest
	result         retrievaldomain.SearchResult
	err            error
	waitForContext bool
}

func (fake *searchFake) Search(ctx context.Context, request retrievaldomain.SearchRequest) (retrievaldomain.SearchResult, error) {
	fake.calls++
	fake.request = request
	if fake.waitForContext {
		<-ctx.Done()
		return retrievaldomain.SearchResult{}, ctx.Err()
	}
	return fake.result, fake.err
}

type evidenceFake struct {
	workspaceID, sourceVersionID, spanID foundation.ID
	batchCalls, batchSize                int
	err                                  error
	drift                                bool
	waitForContext                       bool
	canonicalOrder                       bool
}

type eligibilityFake struct {
	calls, batchSize int
	err              error
	drift            bool
	ineligible       bool
	waitForContext   bool
}

func (fake *eligibilityFake) CheckEvidenceEligibility(ctx context.Context, query knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error) {
	fake.calls++
	fake.batchSize = len(query.Provenance)
	if fake.waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if fake.err != nil {
		return nil, fake.err
	}
	result := make([]knowledgedomain.ProvenanceEligibility, len(query.Provenance))
	for index, ref := range query.Provenance {
		if fake.drift && index == 0 {
			ref.SourceSpanID = "71000000-0000-4000-8000-000000000099"
		}
		result[index] = knowledgedomain.ProvenanceEligibility{Provenance: ref, Eligibility: knowledgedomain.EvidenceIneligible}
		if !fake.ineligible {
			result[index].Eligibility = knowledgedomain.EvidenceEligible
			result[index].Bindings = []knowledgedomain.EvidenceEligibilityBinding{{
				OwnerType: knowledgedomain.EvidenceOwnerClaim, OwnerID: "71000000-0000-4000-8000-000000000090",
				EvidenceID: "71000000-0000-4000-8000-000000000091", ClaimStatus: knowledgedomain.ClaimStatusConfirmed,
				SupportType: knowledgedomain.ClaimSupportSupports,
			}}
		}
	}
	return result, nil
}

func (fake *evidenceFake) GetSourceSpan(ctx context.Context, workspaceID, sourceVersionID, spanID foundation.ID) (retrievalapplication.SourceSpanView, error) {
	fake.workspaceID, fake.sourceVersionID, fake.spanID = workspaceID, sourceVersionID, spanID
	if fake.waitForContext {
		<-ctx.Done()
		return retrievalapplication.SourceSpanView{}, ctx.Err()
	}
	if fake.err != nil {
		return retrievalapplication.SourceSpanView{}, fake.err
	}
	view := validSourceView(sourceVersionID, spanID)
	if fake.drift {
		view.Reference.SourceVersion.WorkspaceID = "71000000-0000-4000-8000-000000000099"
	}
	return view, nil
}

func (fake *evidenceFake) OpenCitationEvidenceBatch(ctx context.Context, queries []retrievaldomain.CitationReferenceQuery) ([]retrievalapplication.OpenedCitationEvidence, error) {
	fake.batchCalls++
	fake.batchSize = len(queries)
	if fake.waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if fake.err != nil {
		return nil, fake.err
	}
	result := make([]retrievalapplication.OpenedCitationEvidence, len(queries))
	for index, query := range queries {
		view := validSourceView(query.SourceVersionID, query.SourceSpanID)
		result[index] = retrievalapplication.OpenedCitationEvidence{Query: query, View: view}
	}
	if fake.canonicalOrder {
		sort.Slice(result, func(left, right int) bool {
			return string(result[left].Query.ChunkID) < string(result[right].Query.ChunkID)
		})
	}
	if fake.drift && len(result) > 0 {
		result[0].Query.ChunkID = "71000000-0000-4000-8000-000000000099"
	}
	return result, nil
}

func executorRequest(name string, arguments []byte) toolsapplication.ExecutorRequest {
	return toolsapplication.ExecutorRequest{
		Identity: toolsdomain.TrustedExecutionIdentity{
			WorkspaceID: testWorkspaceID, DefinitionID: testDefinitionID, DefinitionVersion: 1, DefinitionHash: strings.Repeat("a", 64),
			WorkflowRunID: testRunID, NodeKey: "tool-node", NodeRunID: testNodeRunID, NodeAttemptID: testAttemptID,
			LeaseOwner: "worker-1", LeaseFence: 1,
		},
		Tool: toolsdomain.ToolRef{Name: name, Version: 1}, Arguments: arguments, Reason: "test",
	}
}

func decodeCatalogOutput(t *testing.T, name string, raw []byte) {
	t.Helper()
	contracts, err := catalog.Contracts()
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range contracts {
		if contract.Definition.Ref.Name == name {
			if _, err := contract.DecodeOutput(raw); err != nil {
				t.Fatalf("catalog rejected %s output %s: %v", name, raw, err)
			}
			return
		}
	}
	t.Fatalf("catalog contract %s not found", name)
}

func requireErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v code=%q", err, code)
	}
}

func generatedID(prefix int, index int) foundation.ID {
	return foundation.ID(fmt.Sprintf("%02d%06d-0000-4000-8000-%012d", prefix, index, index+1))
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
