package retrieval

import (
	"context"
	"encoding/json"
	"errors"
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

const testValidateCitationV3CandidateID foundation.ID = "71000000-0000-4000-8000-000000000080"

func TestValidateCitationV3ExecutorUsesReceiptIdentitiesAndEmitsBoundedCanonicalDocuments(t *testing.T) {
	identities := []ReadSourceV3Identity{
		validateCitationV3TestIdentity("E2", generatedID(79, 2), generatedID(78, 2)),
		validateCitationV3TestIdentity("E1", generatedID(79, 1), generatedID(78, 1)),
	}
	resolver := &validateCitationV3ResolverFake{resolution: validateCitationV3TestResolution(identities)}
	reference := &validateCitationV3ReferenceFake{canonicalOrder: true}
	eligibility := &validateCitationV3EligibilityFake{}
	executor, err := NewValidateCitationV3Executor(resolver, reference, eligibility)
	if err != nil {
		t.Fatal(err)
	}
	arguments := validateCitationV3TestArguments(t, []string{"E2", "E1"})
	original := append([]byte(nil), arguments...)
	result, err := executor.Execute(context.Background(), executorRequestVersion(validateCitationName, validateCitationV3Version, arguments))
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments) != string(original) {
		t.Fatal("executor mutated server-built arguments")
	}
	if resolver.calls != 1 || resolver.request.WorkspaceID != testWorkspaceID || resolver.request.WorkflowRunID != testRunID ||
		resolver.request.CandidateID != testValidateCitationV3CandidateID || resolver.request.CandidateHash != strings.Repeat("c", 64) ||
		strings.Join(resolver.request.EvidenceRefs, ",") != "E2,E1" {
		t.Fatalf("resolver request=%+v calls=%d", resolver.request, resolver.calls)
	}
	if reference.calls != 1 || len(reference.queries) != 2 || eligibility.calls != 1 || len(eligibility.query.Provenance) != 2 {
		t.Fatalf("reference calls=%d queries=%d eligibility calls=%d provenance=%d", reference.calls, len(reference.queries), eligibility.calls, len(eligibility.query.Provenance))
	}
	wantOutput := `{"results":[{"evidence_ref":"E2","reason_code":"OK","valid":true},{"evidence_ref":"E1","reason_code":"OK","valid":true}]}`
	if string(result.Output) != wantOutput || result.ResultRef != "" {
		t.Fatalf("output=%s result_ref=%q", result.Output, result.ResultRef)
	}
	catalogContract := validateCitationV3CatalogContract(t)
	canonicalOutput, err := catalogContract.DecodeOutput(result.Output)
	if err != nil || string(canonicalOutput) != string(result.Output) {
		t.Fatalf("catalog output validation failed: canonical_match=%t err=%v", string(canonicalOutput) == string(result.Output), err)
	}
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(toolsdomain.ToolRef{Name: validateCitationName, Version: validateCitationV3Version})
	if !found || int64(len(result.Output)) > contract.MaxOutputBytes || result.PrivateBinding == nil ||
		result.PrivateBinding.Schema != contract.PrivateBindingSchema || int64(len(result.PrivateBinding.Document)) > contract.MaxPrivateBindingBytes {
		t.Fatalf("receipt result contract mismatch: found=%t output_bytes=%d binding_present=%t", found, len(result.Output), result.PrivateBinding != nil)
	}
	var binding validateCitationV3PrivateBinding
	if err := json.Unmarshal(result.PrivateBinding.Document, &binding); err != nil {
		t.Fatal(err)
	}
	if binding.CandidateID != testValidateCitationV3CandidateID || binding.CandidateHash != strings.Repeat("c", 64) ||
		len(binding.Results) != 2 || binding.Results[0].EvidenceRef != "E2" || binding.Results[1].EvidenceRef != "E1" ||
		binding.Results[0].CitationID != identities[0].CitationID || binding.Results[1].CitationID != identities[1].CitationID {
		t.Fatalf("private binding projection mismatch: results=%d", len(binding.Results))
	}
	for _, raw := range [][]byte{result.Output, result.PrivateBinding.Document} {
		text := string(raw)
		for _, forbidden := range []string{"immutable excerpt", "private body", "relative_path", "absolute_path", "snippet", "excerpt", "docs/evidence.md"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("receipt document leaks forbidden field %q", forbidden)
			}
		}
	}
	validateCitationV3DomainReceipt(t, catalogContract, result)
}

func TestValidateCitationV3ExecutorRejectsUntrustedInputBeforeResolver(t *testing.T) {
	valid := string(validateCitationV3TestArguments(t, []string{"E1"}))
	tests := []struct {
		name      string
		arguments string
		version   int64
		nilCtx    bool
	}{
		{name: "unknown workspace", arguments: strings.TrimSuffix(valid, "}") + `,"workspace_id":"forged"}`, version: 3},
		{name: "candidate id", arguments: `{"candidate_hash":"` + strings.Repeat("c", 64) + `","candidate_id":"forged","evidence_refs":["E1"]}`, version: 3},
		{name: "uppercase hash", arguments: `{"candidate_hash":"` + strings.Repeat("C", 64) + `","candidate_id":"` + string(testValidateCitationV3CandidateID) + `","evidence_refs":["E1"]}`, version: 3},
		{name: "missing refs", arguments: `{"candidate_hash":"` + strings.Repeat("c", 64) + `","candidate_id":"` + string(testValidateCitationV3CandidateID) + `","evidence_refs":[]}`, version: 3},
		{name: "duplicate ref", arguments: `{"candidate_hash":"` + strings.Repeat("c", 64) + `","candidate_id":"` + string(testValidateCitationV3CandidateID) + `","evidence_refs":["E1","E1"]}`, version: 3},
		{name: "unopened ref", arguments: `{"candidate_hash":"` + strings.Repeat("c", 64) + `","candidate_id":"` + string(testValidateCitationV3CandidateID) + `","evidence_refs":["E4"]}`, version: 3},
		{name: "too many refs", arguments: `{"candidate_hash":"` + strings.Repeat("c", 64) + `","candidate_id":"` + string(testValidateCitationV3CandidateID) + `","evidence_refs":["E1","E2","E3","E1"]}`, version: 3},
		{name: "wrong type", arguments: `{"candidate_hash":"` + strings.Repeat("c", 64) + `","candidate_id":"` + string(testValidateCitationV3CandidateID) + `","evidence_refs":"E1"}`, version: 3},
		{name: "wrong version", arguments: valid, version: 2},
		{name: "nil context", arguments: valid, version: 3, nilCtx: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := &validateCitationV3ResolverFake{resolution: validateCitationV3TestResolution([]ReadSourceV3Identity{
				validateCitationV3TestIdentity("E1", testChunkID, testSpanID),
			})}
			executor, err := NewValidateCitationV3Executor(resolver, &validateCitationV3ReferenceFake{}, &validateCitationV3EligibilityFake{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Context(context.Background())
			if test.nilCtx {
				ctx = nil
			}
			_, err = executor.Execute(ctx, executorRequestVersion(validateCitationName, test.version, []byte(test.arguments)))
			requireErrorCode(t, err, errorCodeCitationInputInvalid)
			if resolver.calls != 0 {
				t.Fatalf("resolver calls=%d", resolver.calls)
			}
		})
	}
}

func TestValidateCitationV3ExecutorRejectsResolverDriftBeforeEvidenceRead(t *testing.T) {
	baseIdentity := validateCitationV3TestIdentity("E1", testChunkID, testSpanID)
	tests := []struct {
		name   string
		mutate func(*ValidateCitationV3Resolution)
	}{
		{name: "workspace", mutate: func(value *ValidateCitationV3Resolution) { value.WorkspaceID = generatedID(81, 1) }},
		{name: "run", mutate: func(value *ValidateCitationV3Resolution) { value.WorkflowRunID = generatedID(81, 2) }},
		{name: "candidate", mutate: func(value *ValidateCitationV3Resolution) { value.CandidateID = generatedID(81, 3) }},
		{name: "candidate hash", mutate: func(value *ValidateCitationV3Resolution) { value.CandidateHash = strings.Repeat("d", 64) }},
		{name: "ref order", mutate: func(value *ValidateCitationV3Resolution) { value.Identities[0].EvidenceRef = "E2" }},
		{name: "citation id", mutate: func(value *ValidateCitationV3Resolution) { value.Identities[0].CitationID = "forged" }},
		{name: "content hash", mutate: func(value *ValidateCitationV3Resolution) { value.Identities[0].ContentHash = strings.Repeat("A", 64) }},
		{name: "duplicate tuple", mutate: func(value *ValidateCitationV3Resolution) {
			value.Identities = append(value.Identities, value.Identities[0])
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution := validateCitationV3TestResolution([]ReadSourceV3Identity{baseIdentity})
			test.mutate(&resolution)
			resolver := &validateCitationV3ResolverFake{resolution: resolution}
			reference := &validateCitationV3ReferenceFake{}
			eligibility := &validateCitationV3EligibilityFake{}
			executor, err := NewValidateCitationV3Executor(resolver, reference, eligibility)
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Execute(context.Background(), executorRequestVersion(
				validateCitationName, validateCitationV3Version, validateCitationV3TestArguments(t, []string{"E1"}),
			))
			requireErrorCode(t, err, errorCodeCitationResultInvalid)
			if reference.calls != 0 || eligibility.calls != 0 {
				t.Fatalf("untrusted resolution reached dependencies: reference=%d eligibility=%d", reference.calls, eligibility.calls)
			}
		})
	}
}

func TestValidateCitationV3ExecutorPreservesRefOrderAndReportsPerRefReasons(t *testing.T) {
	identities := []ReadSourceV3Identity{
		validateCitationV3TestIdentity("E3", generatedID(79, 3), generatedID(78, 3)),
		validateCitationV3TestIdentity("E1", generatedID(79, 1), generatedID(78, 1)),
		validateCitationV3TestIdentity("E2", generatedID(79, 2), generatedID(78, 2)),
	}
	reference := &validateCitationV3ReferenceFake{
		canonicalOrder: true,
		contentHashes: map[foundation.ID]string{
			identities[0].SourceSpanID: strings.Repeat("d", 64),
		},
	}
	eligibility := &validateCitationV3EligibilityFake{
		statuses: map[foundation.ID]knowledgedomain.EvidenceEligibility{
			identities[1].SourceSpanID: knowledgedomain.EvidenceIneligible,
		},
	}
	executor, err := NewValidateCitationV3Executor(
		&validateCitationV3ResolverFake{resolution: validateCitationV3TestResolution(identities)}, reference, eligibility,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), executorRequestVersion(
		validateCitationName, validateCitationV3Version, validateCitationV3TestArguments(t, []string{"E3", "E1", "E2"}),
	))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"results":[{"evidence_ref":"E3","reason_code":"BINDING_MISMATCH","valid":false},{"evidence_ref":"E1","reason_code":"EVIDENCE_INELIGIBLE","valid":false},{"evidence_ref":"E2","reason_code":"OK","valid":true}]}`
	if string(result.Output) != want {
		t.Fatalf("output=%s", result.Output)
	}
	if eligibility.calls != 1 || len(eligibility.query.Provenance) != 2 {
		t.Fatalf("eligibility calls=%d provenance=%d", eligibility.calls, len(eligibility.query.Provenance))
	}
}

func TestValidateCitationV3ExecutorPreservesDependencyAndContextErrors(t *testing.T) {
	identity := validateCitationV3TestIdentity("E1", testChunkID, testSpanID)
	arguments := validateCitationV3TestArguments(t, []string{"E1"})
	request := executorRequestVersion(validateCitationName, validateCitationV3Version, arguments)
	resolverErr := foundation.NewError(foundation.ErrorConsistencyViolation, "CITATION_RECEIPT_DRIFT", false, errors.New("drift"))
	executor, err := NewValidateCitationV3Executor(
		&validateCitationV3ResolverFake{err: resolverErr}, &validateCitationV3ReferenceFake{}, &validateCitationV3EligibilityFake{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, actual := executor.Execute(context.Background(), request); !errors.Is(actual, resolverErr) {
		t.Fatalf("resolver error=%v", actual)
	}

	referenceErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "CITATION_OPEN_DOWN", false, errors.New("down"))
	executor, err = NewValidateCitationV3Executor(
		&validateCitationV3ResolverFake{resolution: validateCitationV3TestResolution([]ReadSourceV3Identity{identity})},
		&validateCitationV3ReferenceFake{err: referenceErr}, &validateCitationV3EligibilityFake{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, actual := executor.Execute(context.Background(), request); !errors.Is(actual, referenceErr) {
		t.Fatalf("reference error=%v", actual)
	}

	eligibilityErr := foundation.NewError(foundation.ErrorRetryableFailure, "CITATION_ELIGIBILITY_DOWN", true, errors.New("down"))
	executor, err = NewValidateCitationV3Executor(
		&validateCitationV3ResolverFake{resolution: validateCitationV3TestResolution([]ReadSourceV3Identity{identity})},
		&validateCitationV3ReferenceFake{}, &validateCitationV3EligibilityFake{err: eligibilityErr},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, actual := executor.Execute(context.Background(), request); !errors.Is(actual, eligibilityErr) {
		t.Fatalf("eligibility error=%v", actual)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor, err = NewValidateCitationV3Executor(
		&validateCitationV3ResolverFake{waitForContext: true}, &validateCitationV3ReferenceFake{}, &validateCitationV3EligibilityFake{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, actual := executor.Execute(ctx, request); !errors.Is(actual, context.Canceled) {
		t.Fatalf("context error=%v", actual)
	}

	var nilResolver *validateCitationV3ResolverFake
	if _, err := NewValidateCitationV3Executor(nilResolver, &validateCitationV3ReferenceFake{}, &validateCitationV3EligibilityFake{}); err == nil {
		t.Fatal("constructor accepted typed nil resolver")
	}
}

func validateCitationV3TestArguments(t *testing.T, refs []string) []byte {
	t.Helper()
	raw, err := json.Marshal(validateCitationV3Input{
		CandidateHash: strings.Repeat("c", 64), CandidateID: testValidateCitationV3CandidateID, EvidenceRefs: refs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func validateCitationV3CatalogContract(t *testing.T) toolsapplication.Contract {
	t.Helper()
	contracts, err := catalog.Contracts()
	if err != nil {
		t.Fatal(err)
	}
	ref := toolsdomain.ToolRef{Name: validateCitationName, Version: validateCitationV3Version}
	for _, contract := range contracts {
		if contract.Definition.Ref == ref {
			return contract
		}
	}
	t.Fatal("ValidateCitation@3 catalog contract is missing")
	return toolsapplication.Contract{}
}

func validateCitationV3DomainReceipt(
	t *testing.T,
	contract toolsapplication.Contract,
	result toolsapplication.ExecutorResult,
) {
	t.Helper()
	startedAt := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(time.Second)
	tool := contract.Definition.Ref
	inputSchema := contract.Definition.InputSchema
	outputSchema := contract.Definition.OutputSchema
	call := toolsdomain.ToolCall{
		ID: "71000000-0000-4000-8000-000000000081", WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID,
		NodeRunID: testNodeRunID, NodeAttemptID: testAttemptID, CallNo: 1, RequestedToolName: validateCitationName,
		Tool: &tool, DefinitionHash: contract.Definition.DefinitionHash, InputSchema: &inputSchema, OutputSchema: &outputSchema,
		Capability: contract.Definition.RequiredCapability, SideEffectLevel: contract.Definition.SideEffectLevel,
		InvocationPolicy: contract.Definition.InvocationPolicy, RequestHash: strings.Repeat("a", 64), RequestBytes: 1,
		RequestSummary: json.RawMessage(`{}`), ResponseHash: hashText(string(result.Output)), ResponseBytes: int64(len(result.Output)),
		ResponseSummary: json.RawMessage(`{}`), Status: toolsdomain.CallSucceeded, Version: 2,
		StartedAt: startedAt, CompletedAt: &completedAt, DurationMillis: 1000,
	}
	receipt, err := toolsdomain.NewResultReceipt(toolsdomain.ResultReceiptDraft{
		ID: "71000000-0000-4000-8000-000000000082", Output: result.Output,
		PrivateBindingSchema: result.PrivateBinding.Schema, PrivateBinding: result.PrivateBinding.Document,
		CreatedAt: completedAt.Add(time.Microsecond),
	}, call, contract.Definition)
	if err != nil {
		t.Fatalf("domain receipt rejected executor documents: %v", err)
	}
	if receipt.PrivateBinding == nil || string(receipt.Output) != string(result.Output) ||
		string(receipt.PrivateBinding.Document) != string(result.PrivateBinding.Document) {
		t.Fatalf("domain receipt canonical round-trip mismatch: output_match=%t binding_match=%t",
			string(receipt.Output) == string(result.Output),
			receipt.PrivateBinding != nil && string(receipt.PrivateBinding.Document) == string(result.PrivateBinding.Document))
	}
}

func validateCitationV3TestIdentity(ref string, chunkID, spanID foundation.ID) ReadSourceV3Identity {
	identity := ReadSourceV3Identity{
		EvidenceRef: ref, IndexVersionID: testIndexID, ChunkID: chunkID,
		SourceVersionID: testSourceVersionID, SourceSpanID: spanID,
		ContentHash: hashText("immutable excerpt"),
	}
	identity.CitationID = citationID(testWorkspaceID, identity.IndexVersionID, identity.ChunkID, identity.SourceVersionID, identity.SourceSpanID)
	return identity
}

func validateCitationV3TestResolution(identities []ReadSourceV3Identity) ValidateCitationV3Resolution {
	return ValidateCitationV3Resolution{
		WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, CandidateID: testValidateCitationV3CandidateID,
		CandidateHash: strings.Repeat("c", 64), Identities: append([]ReadSourceV3Identity(nil), identities...),
	}
}

type validateCitationV3ResolverFake struct {
	resolution     ValidateCitationV3Resolution
	request        ValidateCitationV3ResolveRequest
	err            error
	calls          int
	waitForContext bool
}

func (fake *validateCitationV3ResolverFake) ResolveValidateCitationV3(
	ctx context.Context,
	request ValidateCitationV3ResolveRequest,
) (ValidateCitationV3Resolution, error) {
	fake.calls++
	request.EvidenceRefs = append([]string(nil), request.EvidenceRefs...)
	fake.request = request
	if fake.waitForContext {
		<-ctx.Done()
		return ValidateCitationV3Resolution{}, ctx.Err()
	}
	result := fake.resolution
	result.Identities = append([]ReadSourceV3Identity(nil), result.Identities...)
	return result, fake.err
}

type validateCitationV3ReferenceFake struct {
	queries        []retrievaldomain.CitationReferenceQuery
	contentHashes  map[foundation.ID]string
	err            error
	calls          int
	canonicalOrder bool
}

func (fake *validateCitationV3ReferenceFake) OpenCitationEvidenceBatch(
	_ context.Context,
	queries []retrievaldomain.CitationReferenceQuery,
) ([]retrievalapplication.OpenedCitationEvidence, error) {
	fake.calls++
	fake.queries = append([]retrievaldomain.CitationReferenceQuery(nil), queries...)
	if fake.err != nil {
		return nil, fake.err
	}
	result := make([]retrievalapplication.OpenedCitationEvidence, len(queries))
	for index, query := range queries {
		view := validSourceView(query.SourceVersionID, query.SourceSpanID)
		view.Reference.SourceVersion.ContentHash = hashText("private body")
		if hash := fake.contentHashes[query.SourceSpanID]; hash != "" {
			view.Reference.ExcerptHash = hash
		}
		result[index] = retrievalapplication.OpenedCitationEvidence{Query: query, View: view}
	}
	if fake.canonicalOrder {
		sort.Slice(result, func(left, right int) bool { return result[left].Query.ChunkID < result[right].Query.ChunkID })
	}
	return result, nil
}

type validateCitationV3EligibilityFake struct {
	query    knowledgedomain.EvidenceEligibilityQuery
	statuses map[foundation.ID]knowledgedomain.EvidenceEligibility
	err      error
	calls    int
}

func (fake *validateCitationV3EligibilityFake) CheckEvidenceEligibility(
	_ context.Context,
	query knowledgedomain.EvidenceEligibilityQuery,
) ([]knowledgedomain.ProvenanceEligibility, error) {
	fake.calls++
	fake.query = query
	if fake.err != nil {
		return nil, fake.err
	}
	result := make([]knowledgedomain.ProvenanceEligibility, len(query.Provenance))
	for index, provenance := range query.Provenance {
		status := fake.statuses[provenance.SourceSpanID]
		if status == "" {
			status = knowledgedomain.EvidenceEligible
		}
		result[index] = knowledgedomain.ProvenanceEligibility{Provenance: provenance, Eligibility: status}
		if status != knowledgedomain.EvidenceIneligible {
			result[index].Bindings = []knowledgedomain.EvidenceEligibilityBinding{{
				OwnerType: knowledgedomain.EvidenceOwnerClaim, OwnerID: "71000000-0000-4000-8000-000000000090",
				EvidenceID: "71000000-0000-4000-8000-000000000091", ClaimStatus: knowledgedomain.ClaimStatusConfirmed,
				SupportType: knowledgedomain.ClaimSupportSupports,
			}}
		}
	}
	return result, nil
}

var _ ValidateCitationV3Resolver = (*validateCitationV3ResolverFake)(nil)
var _ citationReference = (*validateCitationV3ReferenceFake)(nil)
var _ evidenceEligibility = (*validateCitationV3EligibilityFake)(nil)
var _ toolsapplication.Executor = (*ValidateCitationV3Executor)(nil)
