package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisResultReceiptContractsFreezeExactLimits(t *testing.T) {
	tests := []struct {
		ref             ToolRef
		outputSchema    SchemaRef
		outputBytes     int64
		bindingBytes    int64
		bindingSchemaID string
		required        bool
	}{
		{ToolRef{Name: "ReadGitStatus", Version: 2}, SchemaRef{ID: "tool.read_git_status.output", Version: 1}, 4 * 1024, 1024, "tool.read_git_status.private_binding", false},
		{ToolRef{Name: "SearchKnowledge", Version: 2}, SchemaRef{ID: "tool.search_knowledge.output", Version: 2}, 32 * 1024, 16 * 1024, "tool.search_knowledge.private_binding", true},
		{ToolRef{Name: "ReadSource", Version: 3}, SchemaRef{ID: "tool.read_source.output", Version: 2}, 8 * 1024, 4 * 1024, "tool.read_source.private_binding", true},
		{ToolRef{Name: "ValidateCitation", Version: 3}, SchemaRef{ID: "tool.validate_citation.output", Version: 2}, 16 * 1024, 16 * 1024, "tool.validate_citation.private_binding", true},
	}
	contracts := WorkspaceAnalysisResultReceiptContracts()
	if len(contracts) != len(tests) {
		t.Fatalf("contract count = %d, want %d", len(contracts), len(tests))
	}
	for index, test := range tests {
		contract, found := WorkspaceAnalysisResultReceiptContract(test.ref)
		if !found || contract != contracts[index] || contract.Tool != test.ref || contract.OutputSchema != test.outputSchema || contract.MaxOutputBytes != test.outputBytes ||
			contract.MaxPrivateBindingBytes != test.bindingBytes || contract.PrivateBindingSchema != (SchemaRef{ID: test.bindingSchemaID, Version: 1}) ||
			contract.RequiresPrivateBinding != test.required {
			t.Fatalf("contract for %+v = %+v, found=%t", test.ref, contract, found)
		}
	}
	contracts[0].MaxOutputBytes = 1
	again := WorkspaceAnalysisResultReceiptContracts()
	if again[0].MaxOutputBytes != ReadGitStatusV2ReceiptMaxOutputBytes {
		t.Fatal("contract accessor returned mutable shared state")
	}
	for _, ref := range []ToolRef{{}, {Name: "ReadGitStatus", Version: 1}, {Name: "SearchKnowledge", Version: 3}, {Name: "Unknown", Version: 1}} {
		if _, found := WorkspaceAnalysisResultReceiptContract(ref); found {
			t.Fatalf("unexpected receipt contract for %+v", ref)
		}
	}
}

func TestNewResultReceiptCanonicalizesCopiesAndBindsSuccessfulCall(t *testing.T) {
	ref := ToolRef{Name: "SearchKnowledge", Version: 2}
	definition := validResultReceiptDefinition(t, ref)
	identity := resultReceiptTestCitationIdentity("E1", 11, "a")
	output := resultReceiptTestJSON(t, searchKnowledgeV2ReceiptOutput{
		EffectiveMode: "hybrid", Degradations: []string{},
		Items: []searchKnowledgeV2ReceiptItem{{EvidenceRef: "E1", Rank: 1, Snippet: "bounded evidence"}},
	})
	binding := resultReceiptTestJSON(t, searchKnowledgeV2PrivateBinding{
		Items: []resultReceiptCitationIdentity{identity}, SelectedRefs: []string{"E1"},
	})
	output = json.RawMessage(" \n" + string(output) + " ")
	binding = json.RawMessage(" \n" + string(binding) + " ")
	call := validResultReceiptCall(t, definition, output)
	contract, _ := WorkspaceAnalysisResultReceiptContract(ref)
	draft := ResultReceiptDraft{
		ID: resultReceiptTestID(6), Output: output, PrivateBindingSchema: contract.PrivateBindingSchema,
		PrivateBinding: binding, CreatedAt: resultReceiptTestTime().Add(time.Nanosecond),
	}

	receipt, err := NewResultReceipt(draft, call, definition)
	if err != nil {
		t.Fatalf("NewResultReceipt: %v", err)
	}
	if got, want := string(receipt.Output), `{"degradations":[],"effective_mode":"hybrid","items":[{"evidence_ref":"E1","rank":1,"snippet":"bounded evidence"}]}`; got != want {
		t.Fatalf("canonical output = %s, want %s", got, want)
	}
	wantBinding, canonicalErr := canonicalJSONObject(binding, int(contract.MaxPrivateBindingBytes))
	if canonicalErr != nil || receipt.PrivateBinding == nil || string(receipt.PrivateBinding.Document) != string(wantBinding) {
		t.Fatalf("private binding = %+v", receipt.PrivateBinding)
	}
	if receipt.OutputHash != resultReceiptHash(receipt.Output) || receipt.OutputBytes != int64(len(receipt.Output)) ||
		receipt.PrivateBinding.Hash != resultReceiptHash(receipt.PrivateBinding.Document) || receipt.PrivateBinding.Bytes != int64(len(receipt.PrivateBinding.Document)) {
		t.Fatalf("receipt hashes or bytes drifted: %v", receipt)
	}
	if receipt.ToolCallID != call.ID || receipt.WorkspaceID != call.WorkspaceID || receipt.WorkflowRunID != call.WorkflowRunID ||
		receipt.NodeRunID != call.NodeRunID || receipt.NodeAttemptID != call.NodeAttemptID || receipt.Tool != definition.Ref ||
		receipt.OutputSchema != definition.OutputSchema || receipt.DefinitionHash != definition.DefinitionHash ||
		receipt.PersistencePolicy != ResultPersistenceCanonical || receipt.CreatedAt != resultReceiptTestTime() {
		t.Fatalf("receipt authority binding = %+v", receipt)
	}
	if err := ValidateResultReceipt(receipt, call, definition); err != nil {
		t.Fatalf("ValidateResultReceipt replay: %v", err)
	}

	output[0] = '['
	binding[0] = '['
	if receipt.Output[0] != '{' || receipt.PrivateBinding.Document[0] != '{' {
		t.Fatal("receipt aliases caller-owned documents")
	}
}

func TestSearchKnowledgeV2ReceiptIndexVersionIDRequiresOneBoundIndex(t *testing.T) {
	definition := validResultReceiptDefinition(t, ToolRef{Name: "SearchKnowledge", Version: 2})
	contract, _ := WorkspaceAnalysisResultReceiptContract(definition.Ref)
	first := resultReceiptTestCitationIdentity("E1", 21, "a")
	second := resultReceiptTestCitationIdentity("E2", 25, "b")
	second.IndexVersionID = first.IndexVersionID
	second.CitationID = "cite-" + strings.Repeat("d", 64)
	output := resultReceiptTestJSON(t, searchKnowledgeV2ReceiptOutput{
		EffectiveMode: "hybrid",
		Items: []searchKnowledgeV2ReceiptItem{
			{EvidenceRef: "E1", Rank: 1, Snippet: "first evidence"},
			{EvidenceRef: "E2", Rank: 2, Snippet: "second evidence"},
		},
		Degradations: []string{},
	})
	binding := resultReceiptTestJSON(t, searchKnowledgeV2PrivateBinding{
		Items: []resultReceiptCitationIdentity{first, second}, SelectedRefs: []string{"E1", "E2"},
	})
	call := validResultReceiptCall(t, definition, output)
	receipt, err := NewResultReceipt(ResultReceiptDraft{
		ID: resultReceiptTestID(6), Output: output, PrivateBindingSchema: contract.PrivateBindingSchema,
		PrivateBinding: binding, CreatedAt: resultReceiptTestTime(),
	}, call, definition)
	if err != nil {
		t.Fatalf("NewResultReceipt: %v", err)
	}
	indexVersionID, found, err := SearchKnowledgeV2ReceiptIndexVersionID(receipt)
	if err != nil || !found || indexVersionID != first.IndexVersionID {
		t.Fatalf("SearchKnowledgeV2ReceiptIndexVersionID = %s, %t, %v", indexVersionID, found, err)
	}
	selected, err := SearchKnowledgeV2ReceiptSelectedRefs(receipt)
	if err != nil || !reflect.DeepEqual(selected, []string{"E1", "E2"}) {
		t.Fatalf("SearchKnowledgeV2ReceiptSelectedRefs = %#v, %v", selected, err)
	}
	selected[0] = "E3"
	selectedAgain, err := SearchKnowledgeV2ReceiptSelectedRefs(receipt)
	if err != nil || !reflect.DeepEqual(selectedAgain, []string{"E1", "E2"}) {
		t.Fatalf("SearchKnowledgeV2ReceiptSelectedRefs aliases binding = %#v, %v", selectedAgain, err)
	}

	t.Run("mixed index versions", func(t *testing.T) {
		mixed := second
		mixed.IndexVersionID = resultReceiptTestID(29)
		mixedBinding := resultReceiptTestJSON(t, searchKnowledgeV2PrivateBinding{
			Items: []resultReceiptCitationIdentity{first, mixed}, SelectedRefs: []string{"E1", "E2"},
		})
		_, err := NewResultReceipt(ResultReceiptDraft{
			ID: resultReceiptTestID(7), Output: output, PrivateBindingSchema: contract.PrivateBindingSchema,
			PrivateBinding: mixedBinding, CreatedAt: resultReceiptTestTime(),
		}, call, definition)
		assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptInvalid)
	})

	t.Run("zero hit", func(t *testing.T) {
		_, _, zero := validResultReceiptFixture(t, ToolRef{Name: "SearchKnowledge", Version: 2})
		indexVersionID, found, err := SearchKnowledgeV2ReceiptIndexVersionID(zero)
		if err != nil || found || indexVersionID != "" {
			t.Fatalf("SearchKnowledgeV2ReceiptIndexVersionID = %s, %t, %v", indexVersionID, found, err)
		}
		selected, err := SearchKnowledgeV2ReceiptSelectedRefs(zero)
		if err != nil || len(selected) != 0 || selected == nil {
			t.Fatalf("SearchKnowledgeV2ReceiptSelectedRefs = %#v, %v", selected, err)
		}
	})
}

func TestReadSourceV3ReceiptEvidenceForSearchClosesExactSearchBinding(t *testing.T) {
	searchDefinition := validResultReceiptDefinition(t, ToolRef{Name: "SearchKnowledge", Version: 2})
	searchContract, _ := WorkspaceAnalysisResultReceiptContract(searchDefinition.Ref)
	identity := resultReceiptTestCitationIdentity("E1", 31, "a")
	searchOutput := resultReceiptTestJSON(t, searchKnowledgeV2ReceiptOutput{
		EffectiveMode: "hybrid",
		Items:         []searchKnowledgeV2ReceiptItem{{EvidenceRef: "E1", Rank: 1, Snippet: "bounded evidence"}},
		Degradations:  []string{},
	})
	searchBinding := resultReceiptTestJSON(t, searchKnowledgeV2PrivateBinding{
		Items: []resultReceiptCitationIdentity{identity}, SelectedRefs: []string{"E1"},
	})
	searchReceipt, err := NewResultReceipt(ResultReceiptDraft{
		ID: resultReceiptTestID(40), Output: searchOutput, PrivateBindingSchema: searchContract.PrivateBindingSchema,
		PrivateBinding: searchBinding, CreatedAt: resultReceiptTestTime(),
	}, validResultReceiptCall(t, searchDefinition, searchOutput), searchDefinition)
	if err != nil {
		t.Fatalf("NewResultReceipt search: %v", err)
	}

	readDefinition := validResultReceiptDefinition(t, ToolRef{Name: "ReadSource", Version: 3})
	readContract, _ := WorkspaceAnalysisResultReceiptContract(readDefinition.Ref)
	truncated := false
	readOutput := resultReceiptTestJSON(t, readSourceV3ReceiptOutput{
		EvidenceRef: "E1", ContentHash: identity.ContentHash, Truncated: &truncated, Excerpt: "opened evidence",
	})
	readBinding := resultReceiptTestJSON(t, readSourceV3PrivateBinding{
		SearchReceiptID: searchReceipt.ID, SearchReceiptHash: searchReceipt.OutputHash,
		resultReceiptCitationIdentity: identity,
	})
	readReceipt, err := NewResultReceipt(ResultReceiptDraft{
		ID: resultReceiptTestID(41), Output: readOutput, PrivateBindingSchema: readContract.PrivateBindingSchema,
		PrivateBinding: readBinding, CreatedAt: resultReceiptTestTime(),
	}, validResultReceiptCall(t, readDefinition, readOutput), readDefinition)
	if err != nil {
		t.Fatalf("NewResultReceipt read: %v", err)
	}

	evidence, err := ReadSourceV3ReceiptEvidenceForSearch(readReceipt, searchReceipt)
	if err != nil || evidence != (ReadSourceV3ReceiptEvidence{EvidenceRef: "E1", Excerpt: "opened evidence"}) {
		t.Fatalf("ReadSourceV3ReceiptEvidenceForSearch = %+v, %v", evidence, err)
	}

	t.Run("search hash drift", func(t *testing.T) {
		drifted := cloneResultReceipt(searchReceipt)
		drifted.OutputHash = strings.Repeat("f", 64)
		if _, err := ReadSourceV3ReceiptEvidenceForSearch(readReceipt, drifted); err == nil {
			t.Fatal("search hash drift was accepted")
		}
	})
	t.Run("citation tuple drift", func(t *testing.T) {
		driftedIdentity := identity
		driftedIdentity.SourceSpanID = resultReceiptTestID(49)
		driftedBinding := resultReceiptTestJSON(t, readSourceV3PrivateBinding{
			SearchReceiptID: searchReceipt.ID, SearchReceiptHash: searchReceipt.OutputHash,
			resultReceiptCitationIdentity: driftedIdentity,
		})
		drifted, err := NewResultReceipt(ResultReceiptDraft{
			ID: resultReceiptTestID(42), Output: readOutput, PrivateBindingSchema: readContract.PrivateBindingSchema,
			PrivateBinding: driftedBinding, CreatedAt: resultReceiptTestTime(),
		}, validResultReceiptCall(t, readDefinition, readOutput), readDefinition)
		if err != nil {
			t.Fatalf("NewResultReceipt drifted read: %v", err)
		}
		if _, err := ReadSourceV3ReceiptEvidenceForSearch(drifted, searchReceipt); err == nil {
			t.Fatal("citation tuple drift was accepted")
		}
	})
}

func TestValidateCitationV3ReceiptResultsClosesExactCandidateBinding(t *testing.T) {
	definition := validResultReceiptDefinition(t, ToolRef{Name: "ValidateCitation", Version: 3})
	contract, _ := WorkspaceAnalysisResultReceiptContract(definition.Ref)
	candidateID := resultReceiptTestID(50)
	candidateHash := strings.Repeat("a", 64)
	valid := true
	invalid := false
	first := resultReceiptTestCitationIdentity("E2", 51, "b")
	second := resultReceiptTestCitationIdentity("E1", 55, "c")
	second.IndexVersionID = first.IndexVersionID
	second.CitationID = "cite-" + strings.Repeat("d", 64)
	output := resultReceiptTestJSON(t, validateCitationV3ReceiptOutput{Results: []validateCitationV3ReceiptResult{
		{EvidenceRef: "E2", Valid: &invalid, ReasonCode: "EVIDENCE_INELIGIBLE"},
		{EvidenceRef: "E1", Valid: &valid, ReasonCode: "OK"},
	}})
	binding := resultReceiptTestJSON(t, validateCitationV3PrivateBinding{
		CandidateID: candidateID, CandidateHash: candidateHash,
		Results: []resultReceiptCitationIdentity{first, second},
	})
	receipt, err := NewResultReceipt(ResultReceiptDraft{
		ID: resultReceiptTestID(60), Output: output, PrivateBindingSchema: contract.PrivateBindingSchema,
		PrivateBinding: binding, CreatedAt: resultReceiptTestTime(),
	}, validResultReceiptCall(t, definition, output), definition)
	if err != nil {
		t.Fatalf("NewResultReceipt: %v", err)
	}

	results, err := ValidateCitationV3ReceiptResults(receipt, candidateID, candidateHash, []string{"E2", "E1"})
	want := []ValidateCitationV3ReceiptResult{
		{EvidenceRef: "E2", Valid: false, ReasonCode: "EVIDENCE_INELIGIBLE"},
		{EvidenceRef: "E1", Valid: true, ReasonCode: "OK"},
	}
	if err != nil || !reflect.DeepEqual(results, want) {
		t.Fatalf("ValidateCitationV3ReceiptResults = %#v, %v", results, err)
	}
	encoded, err := json.Marshal(results)
	if err != nil || strings.Contains(string(encoded), string(candidateID)) || strings.Contains(string(encoded), candidateHash) ||
		strings.Contains(string(encoded), string(first.SourceSpanID)) {
		t.Fatalf("safe citation result projection leaked private binding: %s, %v", encoded, err)
	}
	citations, err := ValidateCitationV3ReceiptCitations(receipt, candidateID, candidateHash, []string{"E2", "E1"})
	if err != nil || len(citations) != 2 || citations[0].EvidenceRef != first.EvidenceRef ||
		citations[0].CitationID != first.CitationID || citations[0].IndexVersionID != first.IndexVersionID ||
		citations[0].ChunkID != first.ChunkID || citations[0].SourceVersionID != first.SourceVersionID ||
		citations[0].SourceSpanID != first.SourceSpanID || citations[1].EvidenceRef != second.EvidenceRef {
		t.Fatalf("trusted citation projection drifted: %v, %v", citations, err)
	}
	privateJSON, err := json.Marshal(citations[0])
	if err != nil || string(privateJSON) != `{}` {
		t.Fatalf("trusted citation projection serialized private identity: %s, %v", privateJSON, err)
	}
	for _, formatted := range []string{citations[0].String(), fmt.Sprintf("%#v", citations[0]), citations[0].LogValue().String()} {
		for _, secret := range []string{first.EvidenceRef, first.CitationID, string(first.IndexVersionID), string(first.SourceSpanID)} {
			if strings.Contains(formatted, secret) {
				t.Fatalf("trusted citation projection leaked %q in %q", secret, formatted)
			}
		}
	}

	tests := []struct {
		name          string
		candidateID   foundation.ID
		candidateHash string
		references    []string
		mutate        func(*ResultReceipt)
	}{
		{name: "candidate id", candidateID: resultReceiptTestID(61), candidateHash: candidateHash, references: []string{"E2", "E1"}},
		{name: "candidate hash", candidateID: candidateID, candidateHash: strings.Repeat("f", 64), references: []string{"E2", "E1"}},
		{name: "result order", candidateID: candidateID, candidateHash: candidateHash, references: []string{"E1", "E2"}},
		{name: "duplicate expected ref", candidateID: candidateID, candidateHash: candidateHash, references: []string{"E2", "E2"}},
		{name: "non canonical output", candidateID: candidateID, candidateHash: candidateHash, references: []string{"E2", "E1"}, mutate: func(value *ResultReceipt) {
			value.Output = append(json.RawMessage(" \n"), value.Output...)
			value.OutputBytes = int64(len(value.Output))
			value.OutputHash = resultReceiptHash(value.Output)
		}},
		{name: "non canonical private binding", candidateID: candidateID, candidateHash: candidateHash, references: []string{"E2", "E1"}, mutate: func(value *ResultReceipt) {
			value.PrivateBinding.Document = append(json.RawMessage(" \n"), value.PrivateBinding.Document...)
			value.PrivateBinding.Bytes = int64(len(value.PrivateBinding.Document))
			value.PrivateBinding.Hash = resultReceiptHash(value.PrivateBinding.Document)
		}},
		{name: "private candidate drift", candidateID: candidateID, candidateHash: candidateHash, references: []string{"E2", "E1"}, mutate: func(value *ResultReceipt) {
			value.PrivateBinding.Document = resultReceiptTestSetField(t, value.PrivateBinding.Document, "candidate_hash", strings.Repeat("e", 64))
			value.PrivateBinding.Bytes = int64(len(value.PrivateBinding.Document))
			value.PrivateBinding.Hash = resultReceiptHash(value.PrivateBinding.Document)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drifted := cloneResultReceipt(receipt)
			if test.mutate != nil {
				test.mutate(&drifted)
			}
			if _, err := ValidateCitationV3ReceiptResults(
				drifted, test.candidateID, test.candidateHash, test.references,
			); err == nil {
				t.Fatal("citation receipt drift was accepted")
			}
			if _, err := ValidateCitationV3ReceiptCitations(
				drifted, test.candidateID, test.candidateHash, test.references,
			); err == nil {
				t.Fatal("trusted citation projection accepted receipt drift")
			}
		})
	}
}

func TestValidateCitationV3ReceiptRejectsDuplicatePrivateCitationIdentity(t *testing.T) {
	definition := validResultReceiptDefinition(t, ToolRef{Name: "ValidateCitation", Version: 3})
	contract, _ := WorkspaceAnalysisResultReceiptContract(definition.Ref)
	valid := true
	first := resultReceiptTestCitationIdentity("E1", 71, "a")
	second := first
	second.EvidenceRef = "E2"
	output := resultReceiptTestJSON(t, validateCitationV3ReceiptOutput{Results: []validateCitationV3ReceiptResult{
		{EvidenceRef: "E1", Valid: &valid, ReasonCode: "OK"},
		{EvidenceRef: "E2", Valid: &valid, ReasonCode: "OK"},
	}})
	binding := resultReceiptTestJSON(t, validateCitationV3PrivateBinding{
		CandidateID: resultReceiptTestID(70), CandidateHash: strings.Repeat("a", 64),
		Results: []resultReceiptCitationIdentity{first, second},
	})
	_, err := NewResultReceipt(ResultReceiptDraft{
		ID: resultReceiptTestID(80), Output: output, PrivateBindingSchema: contract.PrivateBindingSchema,
		PrivateBinding: binding, CreatedAt: resultReceiptTestTime(),
	}, validResultReceiptCall(t, definition, output), definition)
	assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptInvalid)
}

func TestValidateResultReceiptReplayRejectsEveryBindingDrift(t *testing.T) {
	definition, call, receipt := validResultReceiptFixture(t, ToolRef{Name: "ReadSource", Version: 3})
	if err := ValidateResultReceipt(receipt, call, definition); err != nil {
		t.Fatalf("baseline replay: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ResultReceipt)
	}{
		{name: "workspace", mutate: func(value *ResultReceipt) { value.WorkspaceID = resultReceiptTestID(9) }},
		{name: "workflow run", mutate: func(value *ResultReceipt) { value.WorkflowRunID = resultReceiptTestID(9) }},
		{name: "node run", mutate: func(value *ResultReceipt) { value.NodeRunID = resultReceiptTestID(9) }},
		{name: "node attempt", mutate: func(value *ResultReceipt) { value.NodeAttemptID = resultReceiptTestID(9) }},
		{name: "tool call", mutate: func(value *ResultReceipt) { value.ToolCallID = resultReceiptTestID(9) }},
		{name: "tool", mutate: func(value *ResultReceipt) { value.Tool.Version++ }},
		{name: "schema", mutate: func(value *ResultReceipt) { value.OutputSchema.Version++ }},
		{name: "definition", mutate: func(value *ResultReceipt) { value.DefinitionHash = strings.Repeat("b", 64) }},
		{name: "policy", mutate: func(value *ResultReceipt) { value.PersistencePolicy = ResultPersistenceDisabled }},
		{name: "output ceiling", mutate: func(value *ResultReceipt) { value.MaxOutputBytes++ }},
		{name: "binding ceiling", mutate: func(value *ResultReceipt) { value.MaxPrivateBindingBytes++ }},
		{name: "output document", mutate: func(value *ResultReceipt) { value.Output = json.RawMessage(`{"changed":true}`) }},
		{name: "output hash", mutate: func(value *ResultReceipt) { value.OutputHash = strings.Repeat("c", 64) }},
		{name: "output bytes", mutate: func(value *ResultReceipt) { value.OutputBytes++ }},
		{name: "binding schema", mutate: func(value *ResultReceipt) { value.PrivateBinding.Schema.Version++ }},
		{name: "binding document", mutate: func(value *ResultReceipt) { value.PrivateBinding.Document = json.RawMessage(`{"evidence_ref":"E2"}`) }},
		{name: "binding hash", mutate: func(value *ResultReceipt) { value.PrivateBinding.Hash = strings.Repeat("d", 64) }},
		{name: "binding bytes", mutate: func(value *ResultReceipt) { value.PrivateBinding.Bytes++ }},
		{name: "binding missing", mutate: func(value *ResultReceipt) { value.PrivateBinding = nil }},
		{name: "created time", mutate: func(value *ResultReceipt) { value.CreatedAt = call.StartedAt.Add(-time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drifted := cloneResultReceipt(receipt)
			test.mutate(&drifted)
			err := ValidateResultReceipt(drifted, call, definition)
			assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptBindingConflict)
		})
	}
}

func TestValidateResultReceiptReplayRejectsNonCanonicalDocuments(t *testing.T) {
	definition, call, receipt := validResultReceiptFixture(t, ToolRef{Name: "SearchKnowledge", Version: 2})

	t.Run("output", func(t *testing.T) {
		drifted := cloneResultReceipt(receipt)
		drifted.Output = json.RawMessage(`{ "items": [] }`)
		drifted.OutputHash = resultReceiptHash(drifted.Output)
		drifted.OutputBytes = int64(len(drifted.Output))
		driftedCall := call
		driftedCall.ResponseHash = drifted.OutputHash
		driftedCall.ResponseBytes = drifted.OutputBytes
		assertResultReceiptErrorCode(t, ValidateResultReceipt(drifted, driftedCall, definition), ErrorCodeResultReceiptBindingConflict)
	})

	t.Run("private binding", func(t *testing.T) {
		drifted := cloneResultReceipt(receipt)
		drifted.PrivateBinding.Document = json.RawMessage(`{ "content_hash": "` + strings.Repeat("a", 64) + `", "selected_refs": [] }`)
		drifted.PrivateBinding.Hash = resultReceiptHash(drifted.PrivateBinding.Document)
		drifted.PrivateBinding.Bytes = int64(len(drifted.PrivateBinding.Document))
		assertResultReceiptErrorCode(t, ValidateResultReceipt(drifted, call, definition), ErrorCodeResultReceiptBindingConflict)
	})
}

func TestValidateCitationReceiptFixtureRejectsPrivateIdentityDrift(t *testing.T) {
	definition, call, receipt := validResultReceiptFixture(t, ToolRef{Name: "ValidateCitation", Version: 3})
	drifted := cloneResultReceipt(receipt)
	drifted.PrivateBinding.Document = json.RawMessage(strings.Replace(
		string(drifted.PrivateBinding.Document), strings.Repeat("a", 64), strings.Repeat("b", 64), 1,
	))
	assertResultReceiptErrorCode(t, ValidateResultReceipt(drifted, call, definition), ErrorCodeResultReceiptBindingConflict)
}

func TestNewResultReceiptAllowsGitWithoutPrivateBinding(t *testing.T) {
	definition := validResultReceiptDefinition(t, ToolRef{Name: "ReadGitStatus", Version: 2})
	output, _ := validResultReceiptDocuments(t, definition.Ref)
	call := validResultReceiptCall(t, definition, output)
	receipt, err := NewResultReceipt(ResultReceiptDraft{
		ID: resultReceiptTestID(6), Output: output, CreatedAt: resultReceiptTestTime(),
	}, call, definition)
	if err != nil {
		t.Fatalf("NewResultReceipt: %v", err)
	}
	if receipt.PrivateBinding != nil {
		t.Fatalf("optional Git private binding = %v", receipt.PrivateBinding)
	}
	if err := ValidateResultReceipt(receipt, call, definition); err != nil {
		t.Fatalf("ValidateResultReceipt: %v", err)
	}
}

func TestNewResultReceiptRejectsFailureUnknownAndNonOptedDefinitions(t *testing.T) {
	ref := ToolRef{Name: "ValidateCitation", Version: 3}
	definition := validResultReceiptDefinition(t, ref)
	output, binding := validResultReceiptDocuments(t, ref)
	base := validResultReceiptCall(t, definition, output)
	contract, _ := WorkspaceAnalysisResultReceiptContract(ref)
	draft := ResultReceiptDraft{
		ID: resultReceiptTestID(6), Output: output, PrivateBindingSchema: contract.PrivateBindingSchema,
		PrivateBinding: binding, CreatedAt: resultReceiptTestTime(),
	}

	for _, status := range []CallStatus{CallStarted, CallFailed, CallRefused, CallUnknown} {
		t.Run(string(status), func(t *testing.T) {
			call := terminalResultReceiptCall(base, status)
			receipt, err := NewResultReceipt(draft, call, definition)
			assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptBindingConflict)
			if receipt.ID != "" || len(receipt.Output) != 0 || receipt.PrivateBinding != nil {
				t.Fatalf("terminal call returned receipt output: %v", receipt)
			}
		})
	}
	for _, status := range []CallStatus{CallFailed, CallUnknown} {
		t.Run(string(status)+" with output claim", func(t *testing.T) {
			call := terminalResultReceiptCall(base, status)
			call.ResponseHash = base.ResponseHash
			call.ResponseBytes = base.ResponseBytes
			call.ResponseSummary = append(json.RawMessage(nil), base.ResponseSummary...)
			receipt, err := NewResultReceipt(draft, call, definition)
			assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptBindingConflict)
			if receipt.ID != "" || len(receipt.Output) != 0 || receipt.PrivateBinding != nil {
				t.Fatalf("terminal output claim returned receipt output: %v", receipt)
			}
		})
	}

	for _, mutate := range []func(*ToolCall){
		func(call *ToolCall) { call.ResultRef = "citation-batch:" + strings.Repeat("a", 64) },
		func(call *ToolCall) { call.SideEffectType, call.SideEffectID = "web_fetch", strings.Repeat("b", 64) },
	} {
		call := base
		mutate(&call)
		_, err := NewResultReceipt(draft, call, definition)
		assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptBindingConflict)
	}

	nonOpted := definition
	nonOpted.ResultPersistencePolicy = ResultPersistenceDisabled
	nonOpted.DefinitionHash = ""
	nonOpted, err := CanonicalizeDefinition(nonOpted)
	if err != nil {
		t.Fatal(err)
	}
	call := validResultReceiptCall(t, nonOpted, output)
	if _, err = NewResultReceipt(draft, call, nonOpted); err == nil {
		t.Fatal("non-opted definition created a result receipt")
	}

	unknownDefinition := definition
	unknownDefinition.Ref.Version = 4
	unknownDefinition.DefinitionHash = ""
	unknownDefinition, err = CanonicalizeDefinition(unknownDefinition)
	if err != nil {
		t.Fatal(err)
	}
	call = base
	if _, err = NewResultReceipt(draft, call, unknownDefinition); err == nil {
		t.Fatal("unknown exact tool version created a result receipt")
	}

	wrongSchema := definition
	wrongSchema.OutputSchema.Version++
	wrongSchema.DefinitionHash = ""
	wrongSchema, err = CanonicalizeDefinition(wrongSchema)
	if err != nil {
		t.Fatal(err)
	}
	call = validResultReceiptCall(t, wrongSchema, output)
	_, err = NewResultReceipt(draft, call, wrongSchema)
	assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptInvalid)
}

func TestNewResultReceiptRejectsNonCanonicalOversizedAndUnsafeDocumentsWithoutLeak(t *testing.T) {
	definition := validResultReceiptDefinition(t, ToolRef{Name: "SearchKnowledge", Version: 2})
	validOutput, validBinding := validResultReceiptDocuments(t, definition.Ref)
	call := validResultReceiptCall(t, definition, validOutput)
	contract, _ := WorkspaceAnalysisResultReceiptContract(definition.Ref)
	base := ResultReceiptDraft{
		ID: resultReceiptTestID(6), Output: validOutput, PrivateBindingSchema: contract.PrivateBindingSchema,
		PrivateBinding: validBinding, CreatedAt: resultReceiptTestTime(),
	}
	secret := "Bearer receipt-private-canary"
	privatePath := "/Users/private/workspace/source.md"

	tests := []struct {
		name   string
		mutate func(*ResultReceiptDraft)
	}{
		{name: "duplicate output", mutate: func(value *ResultReceiptDraft) { value.Output = json.RawMessage(`{"items":[],"items":[]}`) }},
		{name: "trailing output", mutate: func(value *ResultReceiptDraft) { value.Output = json.RawMessage(`{} {}`) }},
		{name: "array output", mutate: func(value *ResultReceiptDraft) { value.Output = json.RawMessage(`[]`) }},
		{name: "oversized output", mutate: func(value *ResultReceiptDraft) {
			value.Output = json.RawMessage(`{"value":"` + strings.Repeat("x", int(SearchKnowledgeV2ReceiptMaxOutputBytes)) + `"}`)
		}},
		{name: "canonical output expansion", mutate: func(value *ResultReceiptDraft) {
			value.Output = json.RawMessage(`{"value":"` + strings.Repeat("<", 6*1024) + `"}`)
		}},
		{name: "duplicate binding", mutate: func(value *ResultReceiptDraft) { value.PrivateBinding = json.RawMessage(`{"ref":"E1","ref":"E2"}`) }},
		{name: "oversized binding", mutate: func(value *ResultReceiptDraft) {
			value.PrivateBinding = json.RawMessage(`{"value":"` + strings.Repeat("x", int(SearchKnowledgeV2ReceiptMaxPrivateBindingBytes)) + `"}`)
		}},
		{name: "canonical binding expansion", mutate: func(value *ResultReceiptDraft) {
			value.PrivateBinding = json.RawMessage(`{"value":"` + strings.Repeat("<", 3*1024) + `"}`)
		}},
		{name: "missing binding schema", mutate: func(value *ResultReceiptDraft) { value.PrivateBindingSchema = SchemaRef{} }},
		{name: "wrong binding schema", mutate: func(value *ResultReceiptDraft) { value.PrivateBindingSchema.Version++ }},
		{name: "secret binding", mutate: func(value *ResultReceiptDraft) {
			value.PrivateBinding = json.RawMessage(`{"credential":"` + secret + `"}`)
		}},
		{name: "path binding", mutate: func(value *ResultReceiptDraft) {
			value.PrivateBinding = json.RawMessage(`{"absolute_path":"` + privatePath + `"}`)
		}},
		{name: "content binding", mutate: func(value *ResultReceiptDraft) { value.PrivateBinding = json.RawMessage(`{"excerpt":"private body"}`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := base
			test.mutate(&draft)
			_, err := NewResultReceipt(draft, call, definition)
			if err == nil {
				t.Fatal("unsafe receipt document was accepted")
			}
			for _, forbidden := range []string{secret, privatePath, "private body"} {
				if strings.Contains(fmt.Sprintf("%v", err), forbidden) {
					t.Fatalf("error leaked private document: %v", err)
				}
			}
		})
	}
}

func TestNewResultReceiptRejectsExactSchemaUnknownTypeAndEnumDriftWithoutLeak(t *testing.T) {
	const leakCanary = "renamed-private-field-canary"
	tests := []struct {
		name          string
		ref           ToolRef
		mutateOutput  func(*testing.T, json.RawMessage) json.RawMessage
		mutateBinding func(*testing.T, json.RawMessage) json.RawMessage
	}{
		{name: "Git output renamed field", ref: ToolRef{Name: "ReadGitStatus", Version: 2}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "repository_path_alias", leakCanary)
		}},
		{name: "Git binding unknown field", ref: ToolRef{Name: "ReadGitStatus", Version: 2}, mutateBinding: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "repository_alias", leakCanary)
		}},
		{name: "Git output wrong type", ref: ToolRef{Name: "ReadGitStatus", Version: 2}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "clean", "true")
		}},
		{name: "Git binding wrong type", ref: ToolRef{Name: "ReadGitStatus", Version: 2}, mutateBinding: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "workspace_root_hash", 7)
		}},
		{name: "Git output wrong enum", ref: ToolRef{Name: "ReadGitStatus", Version: 2}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "object_format", "sha512")
		}},
		{name: "Search output renamed field", ref: ToolRef{Name: "SearchKnowledge", Version: 2}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "excerpt_alias", leakCanary)
		}},
		{name: "Search binding unknown field", ref: ToolRef{Name: "SearchKnowledge", Version: 2}, mutateBinding: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "identity_alias", leakCanary)
		}},
		{name: "Search output wrong type", ref: ToolRef{Name: "SearchKnowledge", Version: 2}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "items", map[string]any{})
		}},
		{name: "Search binding wrong type", ref: ToolRef{Name: "SearchKnowledge", Version: 2}, mutateBinding: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "selected_refs", "E1")
		}},
		{name: "Search output wrong enum", ref: ToolRef{Name: "SearchKnowledge", Version: 2}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "effective_mode", "all")
		}},
		{name: "Source output renamed field", ref: ToolRef{Name: "ReadSource", Version: 3}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "body_alias", leakCanary)
		}},
		{name: "Source binding unknown field", ref: ToolRef{Name: "ReadSource", Version: 3}, mutateBinding: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "tuple_alias", leakCanary)
		}},
		{name: "Source output wrong type", ref: ToolRef{Name: "ReadSource", Version: 3}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "truncated", "false")
		}},
		{name: "Source binding wrong type", ref: ToolRef{Name: "ReadSource", Version: 3}, mutateBinding: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "search_receipt_id", 7)
		}},
		{name: "Source binding unsafe citation id", ref: ToolRef{Name: "ReadSource", Version: 3}, mutateBinding: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "citation_id", "/tmp/private")
		}},
		{name: "Citation output renamed field", ref: ToolRef{Name: "ValidateCitation", Version: 3}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "provider_alias", leakCanary)
		}},
		{name: "Citation binding nested unknown field", ref: ToolRef{Name: "ValidateCitation", Version: 3}, mutateBinding: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetFirstArrayItemField(t, document, "results", "identity_alias", leakCanary)
		}},
		{name: "Citation output wrong type", ref: ToolRef{Name: "ValidateCitation", Version: 3}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "results", map[string]any{})
		}},
		{name: "Citation binding wrong type", ref: ToolRef{Name: "ValidateCitation", Version: 3}, mutateBinding: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetField(t, document, "results", map[string]any{})
		}},
		{name: "Citation output wrong enum", ref: ToolRef{Name: "ValidateCitation", Version: 3}, mutateOutput: func(t *testing.T, document json.RawMessage) json.RawMessage {
			return resultReceiptTestSetFirstArrayItemField(t, document, "results", "reason_code", "OTHER")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := validResultReceiptDefinition(t, test.ref)
			output, binding := validResultReceiptDocuments(t, test.ref)
			if test.mutateOutput != nil {
				output = test.mutateOutput(t, output)
			}
			if test.mutateBinding != nil {
				binding = test.mutateBinding(t, binding)
			}
			call := validResultReceiptCall(t, definition, output)
			contract, _ := WorkspaceAnalysisResultReceiptContract(test.ref)
			_, err := NewResultReceipt(ResultReceiptDraft{
				ID: resultReceiptTestID(6), Output: output, PrivateBindingSchema: contract.PrivateBindingSchema,
				PrivateBinding: binding, CreatedAt: resultReceiptTestTime(),
			}, call, definition)
			assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptInvalid)
			if strings.Contains(fmt.Sprint(err), leakCanary) {
				t.Fatalf("exact schema error leaked renamed field: %v", err)
			}
		})
	}
}

func TestValidateResultReceiptReplayRejectsExactSchemaDriftWithMatchingHashes(t *testing.T) {
	definition, call, receipt := validResultReceiptFixture(t, ToolRef{Name: "SearchKnowledge", Version: 2})
	contract, _ := WorkspaceAnalysisResultReceiptContract(definition.Ref)

	t.Run("output unknown field", func(t *testing.T) {
		drifted := cloneResultReceipt(receipt)
		drifted.Output = resultReceiptTestSetField(t, drifted.Output, "renamed_excerpt", "replay-canary")
		drifted.Output = canonicalResultReceiptTestDocument(t, drifted.Output, contract.MaxOutputBytes)
		drifted.OutputHash = resultReceiptHash(drifted.Output)
		drifted.OutputBytes = int64(len(drifted.Output))
		driftedCall := call
		driftedCall.ResponseHash = drifted.OutputHash
		driftedCall.ResponseBytes = drifted.OutputBytes
		assertResultReceiptErrorCode(t, ValidateResultReceipt(drifted, driftedCall, definition), ErrorCodeResultReceiptBindingConflict)
	})

	t.Run("binding unknown field", func(t *testing.T) {
		drifted := cloneResultReceipt(receipt)
		drifted.PrivateBinding.Document = resultReceiptTestSetField(t, drifted.PrivateBinding.Document, "identity_alias", "replay-canary")
		drifted.PrivateBinding.Document = canonicalResultReceiptTestDocument(t, drifted.PrivateBinding.Document, contract.MaxPrivateBindingBytes)
		drifted.PrivateBinding.Hash = resultReceiptHash(drifted.PrivateBinding.Document)
		drifted.PrivateBinding.Bytes = int64(len(drifted.PrivateBinding.Document))
		assertResultReceiptErrorCode(t, ValidateResultReceipt(drifted, call, definition), ErrorCodeResultReceiptBindingConflict)
	})
}

func TestResultReceiptFormattingNeverLeaksOutputOrPrivateBinding(t *testing.T) {
	definition, _, receipt := validResultReceiptFixture(t, ToolRef{Name: "SearchKnowledge", Version: 2})
	outputCanary := "model-safe-output-canary"
	bindingCanary := "private-binding-canary"
	receipt.Output = json.RawMessage(`{"value":"` + outputCanary + `"}`)
	receipt.PrivateBinding.Document = json.RawMessage(`{"value":"` + bindingCanary + `"}`)

	encodedReceipt, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	encodedBinding, err := json.Marshal(receipt.PrivateBinding)
	if err != nil {
		t.Fatal(err)
	}
	for _, formatted := range []string{
		fmt.Sprint(receipt), fmt.Sprintf("%v", receipt), fmt.Sprintf("%+v", receipt), fmt.Sprintf("%#v", receipt),
		fmt.Sprint(&receipt), fmt.Sprintf("%+v", &receipt), fmt.Sprintf("%#v", &receipt),
		fmt.Sprint(receipt.PrivateBinding), fmt.Sprintf("%+v", *receipt.PrivateBinding), fmt.Sprintf("%#v", receipt.PrivateBinding),
		string(encodedReceipt), string(encodedBinding),
	} {
		if strings.Contains(formatted, outputCanary) || strings.Contains(formatted, bindingCanary) {
			t.Fatalf("receipt formatting leaked a document: %s", formatted)
		}
		if !strings.Contains(strings.ToLower(formatted), "hash") && !strings.Contains(formatted, definition.OutputSchema.ID) {
			t.Fatalf("safe receipt formatting lost diagnostic identity: %s", formatted)
		}
	}
}

func validResultReceiptFixture(t *testing.T, ref ToolRef) (Definition, ToolCall, ResultReceipt) {
	t.Helper()
	definition := validResultReceiptDefinition(t, ref)
	contract, _ := WorkspaceAnalysisResultReceiptContract(ref)
	output, binding := validResultReceiptDocuments(t, ref)
	call := validResultReceiptCall(t, definition, output)
	receipt, err := NewResultReceipt(ResultReceiptDraft{
		ID: resultReceiptTestID(6), Output: output, PrivateBindingSchema: contract.PrivateBindingSchema,
		PrivateBinding: binding, CreatedAt: resultReceiptTestTime(),
	}, call, definition)
	if err != nil {
		t.Fatalf("fixture receipt: %v", err)
	}
	return definition, call, receipt
}

func validResultReceiptDefinition(t *testing.T, ref ToolRef) Definition {
	t.Helper()
	contract, found := WorkspaceAnalysisResultReceiptContract(ref)
	if !found {
		t.Fatalf("missing fixture contract for %+v", ref)
	}
	definition, err := CanonicalizeDefinition(Definition{
		Ref: ref, Description: "Persist one bounded canonical workspace analysis tool result.",
		InputSchema:          SchemaRef{ID: strings.TrimSuffix(contract.OutputSchema.ID, ".output") + ".input", Version: contract.OutputSchema.Version},
		InputSchemaDocument:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
		OutputSchema:         contract.OutputSchema,
		OutputSchemaDocument: json.RawMessage(`{"type":"object","additionalProperties":true}`),
		RequiredCapability:   capability.ReadLocal, SideEffectLevel: SideEffectNone,
		InvocationPolicy: InvocationTrustedWorkflowOnly, ResultPersistencePolicy: ResultPersistenceCanonical,
		Timeout: 15 * time.Second, RetryPolicy: RetryPolicy{MaxAttempts: 1}, IdempotencyMode: IdempotencyNone,
		AllowedWorkflows: []WorkflowBinding{{Key: workspaceAnalysisWorkflowKey, Version: workspaceAnalysisWorkflowVersion}},
		MaxInputBytes:    4096, MaxOutputBytes: contract.MaxOutputBytes,
	})
	if err != nil {
		t.Fatalf("canonicalize fixture definition: %v", err)
	}
	return definition
}

func validResultReceiptCall(t *testing.T, definition Definition, output json.RawMessage) ToolCall {
	t.Helper()
	contract, found := WorkspaceAnalysisResultReceiptContract(definition.Ref)
	if !found {
		t.Fatalf("missing fixture contract for %+v", definition.Ref)
	}
	canonical, err := canonicalJSONObject(output, int(contract.MaxOutputBytes))
	if err != nil {
		t.Fatalf("canonicalize fixture output: %v", err)
	}
	completedAt := resultReceiptTestTime()
	tool := definition.Ref
	inputSchema := definition.InputSchema
	outputSchema := definition.OutputSchema
	return ToolCall{
		ID: resultReceiptTestID(1), WorkspaceID: resultReceiptTestID(2), WorkflowRunID: resultReceiptTestID(3),
		NodeRunID: resultReceiptTestID(4), NodeAttemptID: resultReceiptTestID(5), CallNo: 1,
		RequestedToolName: definition.Ref.Name, Tool: &tool, DefinitionHash: definition.DefinitionHash,
		InputSchema: &inputSchema, OutputSchema: &outputSchema, Capability: definition.RequiredCapability,
		SideEffectLevel: definition.SideEffectLevel, InvocationPolicy: definition.InvocationPolicy,
		RequestHash: strings.Repeat("1", 64), RequestBytes: 2, RequestSummary: json.RawMessage(`{}`),
		ResponseHash: resultReceiptHash(canonical), ResponseBytes: int64(len(canonical)), ResponseSummary: json.RawMessage(`{}`),
		Status: CallSucceeded, Version: 2, StartedAt: completedAt.Add(-time.Second), CompletedAt: &completedAt, DurationMillis: 1000,
	}
}

func terminalResultReceiptCall(base ToolCall, status CallStatus) ToolCall {
	call := base
	call.Status = status
	call.ResponseHash = ""
	call.ResponseBytes = 0
	call.ResponseSummary = nil
	call.ResultRef = ""
	call.Retryable = false
	call.ErrorCode = ""
	call.DurationMillis = 0
	if status == CallStarted {
		call.CompletedAt = nil
	} else {
		call.ErrorCode = "TOOL_FIXTURE_TERMINAL"
		call.DurationMillis = 1000
	}
	return call
}

func cloneResultReceipt(receipt ResultReceipt) ResultReceipt {
	cloned := receipt
	cloned.Output = append(json.RawMessage(nil), receipt.Output...)
	if receipt.PrivateBinding != nil {
		binding := *receipt.PrivateBinding
		binding.Document = append(json.RawMessage(nil), receipt.PrivateBinding.Document...)
		cloned.PrivateBinding = &binding
	}
	return cloned
}

func assertResultReceiptErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func resultReceiptTestID(suffix int) foundation.ID {
	return foundation.ID(fmt.Sprintf("8a000000-0000-4000-8000-%012d", suffix))
}

func resultReceiptTestTime() time.Time {
	return time.Date(2026, 8, 15, 8, 9, 10, 123456000, time.UTC)
}

func validResultReceiptDocuments(t *testing.T, ref ToolRef) (json.RawMessage, json.RawMessage) {
	t.Helper()
	falseValue := false
	trueValue := true
	zero := 0
	identity := resultReceiptTestCitationIdentity("E1", 11, "b")
	switch ref {
	case ToolRef{Name: "ReadGitStatus", Version: 2}:
		return resultReceiptTestJSON(t, readGitStatusV2ReceiptOutput{
				Branch: "main", Head: strings.Repeat("a", 40), ObjectFormat: "sha1", Clean: &trueValue,
				StagedCount: &zero, UnstagedCount: &zero, UntrackedCount: &zero, ConflictCount: &zero,
			}), resultReceiptTestJSON(t, readGitStatusV2PrivateBinding{
				WorkspaceRootHash: strings.Repeat("c", 64), RepositoryIdentityHash: strings.Repeat("d", 64),
			})
	case ToolRef{Name: "SearchKnowledge", Version: 2}:
		return resultReceiptTestJSON(t, searchKnowledgeV2ReceiptOutput{
				EffectiveMode: "hybrid", Items: []searchKnowledgeV2ReceiptItem{}, Degradations: []string{},
			}), resultReceiptTestJSON(t, searchKnowledgeV2PrivateBinding{
				Items: []resultReceiptCitationIdentity{}, SelectedRefs: []string{},
			})
	case ToolRef{Name: "ReadSource", Version: 3}:
		return resultReceiptTestJSON(t, readSourceV3ReceiptOutput{
				EvidenceRef: "E1", ContentHash: identity.ContentHash, Truncated: &falseValue, Excerpt: "bounded source excerpt",
			}), resultReceiptTestJSON(t, readSourceV3PrivateBinding{
				SearchReceiptID: resultReceiptTestID(15), SearchReceiptHash: strings.Repeat("e", 64),
				resultReceiptCitationIdentity: identity,
			})
	case ToolRef{Name: "ValidateCitation", Version: 3}:
		return resultReceiptTestJSON(t, validateCitationV3ReceiptOutput{
				Results: []validateCitationV3ReceiptResult{{EvidenceRef: "E1", Valid: &trueValue, ReasonCode: "OK"}},
			}), resultReceiptTestJSON(t, validateCitationV3PrivateBinding{
				CandidateID: resultReceiptTestID(16), CandidateHash: strings.Repeat("a", 64),
				Results: []resultReceiptCitationIdentity{identity},
			})
	default:
		t.Fatalf("missing exact receipt documents for %+v", ref)
		return nil, nil
	}
}

func resultReceiptTestCitationIdentity(ref string, firstIDSuffix int, hashCharacter string) resultReceiptCitationIdentity {
	return resultReceiptCitationIdentity{
		EvidenceRef: ref, CitationID: "cite-" + strings.Repeat("c", 64),
		IndexVersionID: resultReceiptTestID(firstIDSuffix), ChunkID: resultReceiptTestID(firstIDSuffix + 1),
		SourceVersionID: resultReceiptTestID(firstIDSuffix + 2), SourceSpanID: resultReceiptTestID(firstIDSuffix + 3),
		ContentHash: strings.Repeat(hashCharacter, 64),
	}
}

func resultReceiptTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	document, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal receipt fixture: %v", err)
	}
	return document
}

func resultReceiptTestSetField(t *testing.T, document json.RawMessage, key string, value any) json.RawMessage {
	t.Helper()
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(document)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil || decoded == nil {
		t.Fatalf("decode receipt fixture: %v", err)
	}
	decoded[key] = value
	return resultReceiptTestJSON(t, decoded)
}

func resultReceiptTestSetFirstArrayItemField(t *testing.T, document json.RawMessage, arrayKey, key string, value any) json.RawMessage {
	t.Helper()
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(document)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil || decoded == nil {
		t.Fatalf("decode receipt fixture: %v", err)
	}
	items, ok := decoded[arrayKey].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("receipt fixture field %q is not a non-empty array", arrayKey)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("receipt fixture field %q first item is not an object", arrayKey)
	}
	item[key] = value
	return resultReceiptTestJSON(t, decoded)
}

func canonicalResultReceiptTestDocument(t *testing.T, document json.RawMessage, maxBytes int64) json.RawMessage {
	t.Helper()
	canonical, err := canonicalJSONObject(document, int(maxBytes))
	if err != nil {
		t.Fatalf("canonicalize receipt fixture: %v", err)
	}
	return canonical
}

func TestResultReceiptFixtureHashesAreStable(t *testing.T) {
	definition, call, receipt := validResultReceiptFixture(t, ToolRef{Name: "SearchKnowledge", Version: 2})
	if err := ValidateResultReceipt(receipt, call, definition); err != nil {
		t.Fatal(err)
	}
	const (
		wantOutputHash  = "5cde649cdd268c8e6c4f4e09687502296ffdca49d5d583e067ea68a354b0f6ee"
		wantBindingHash = "f021edb2ef03c81486bc9f0e50f30665037c81c892622d7ec41eaf5d974bbf2b"
	)
	if string(receipt.Output) != `{"degradations":[],"effective_mode":"hybrid","items":[]}` || receipt.OutputHash != wantOutputHash || receipt.PrivateBinding.Hash != wantBindingHash {
		t.Fatalf("stable receipt fixture drifted: %v", receipt)
	}
}
