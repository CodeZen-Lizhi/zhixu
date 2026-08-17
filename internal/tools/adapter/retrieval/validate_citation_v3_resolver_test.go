package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestValidateCitationV3ReceiptResolverExpandsPersistedReceiptsInCandidateOrder(t *testing.T) {
	authority := validateCitationV3TestAuthority(t, []string{"E2", "E1"})
	reader := &validateCitationV3AuthorityReaderFake{authority: authority}
	resolver, err := NewValidateCitationV3ReceiptResolver(reader)
	if err != nil {
		t.Fatal(err)
	}
	request := validateCitationV3TestResolveRequest([]string{"E2", "E1"})

	first, err := resolver.ResolveValidateCitationV3(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolver.ResolveValidateCitationV3(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || len(first.Identities) != 2 {
		t.Fatalf("resolution is not deterministic: identities=%d", len(first.Identities))
	}
	if first.Identities[0].EvidenceRef != "E2" || first.Identities[1].EvidenceRef != "E1" {
		t.Fatalf("resolution is not deterministic in candidate order: refs=%v", []string{
			first.Identities[0].EvidenceRef, first.Identities[1].EvidenceRef,
		})
	}
	if reader.calls != 2 || reader.query != (ValidateCitationV3AuthorityQuery{
		WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, CandidateID: testValidateCitationV3CandidateID,
	}) {
		t.Fatalf("authority query=%+v calls=%d", reader.query, reader.calls)
	}
	for index, identity := range first.Identities {
		if identity != validateCitationV3TestIdentity(identity.EvidenceRef, generatedID(79, int(identity.EvidenceRef[1]-'0')), generatedID(78, int(identity.EvidenceRef[1]-'0'))) {
			t.Fatalf("identity %d did not come from the frozen receipt", index)
		}
	}

	rendered := fmt.Sprintf("%+v %#v %+v %#v", authority, authority, first, first)
	for _, forbidden := range []string{
		"private snippet", "immutable excerpt", "docs/evidence.md", strings.Repeat("c", 64),
		first.Identities[0].CitationID, string(first.Identities[0].ChunkID),
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("safe authority formatting leaked %q", forbidden)
		}
	}
}

func TestValidateCitationV3ReceiptResolverRejectsCandidateAndReceiptDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *ValidateCitationV3Authority)
	}{
		{name: "candidate hash", mutate: func(_ *testing.T, value *ValidateCitationV3Authority) {
			value.CandidateHash = strings.Repeat("d", 64)
		}},
		{name: "candidate ref order", mutate: func(_ *testing.T, value *ValidateCitationV3Authority) {
			value.EvidenceRefs[0], value.EvidenceRefs[1] = value.EvidenceRefs[1], value.EvidenceRefs[0]
		}},
		{name: "analysis run", mutate: func(_ *testing.T, value *ValidateCitationV3Authority) {
			value.AnalysisRunID = value.CandidateID
		}},
		{name: "search run", mutate: func(_ *testing.T, value *ValidateCitationV3Authority) {
			value.SearchReceipt.WorkflowRunID = generatedID(92, 1)
		}},
		{name: "missing read receipt", mutate: func(_ *testing.T, value *ValidateCitationV3Authority) {
			value.ReadSourceReceipts = value.ReadSourceReceipts[:1]
		}},
		{name: "read search hash", mutate: func(t *testing.T, value *ValidateCitationV3Authority) {
			mutateValidateCitationV3ReadBinding(t, &value.ReadSourceReceipts[0], func(binding *readSourceV3PrivateBinding) {
				binding.SearchReceiptHash = strings.Repeat("d", 64)
			})
		}},
		{name: "read identity", mutate: func(t *testing.T, value *ValidateCitationV3Authority) {
			mutateValidateCitationV3ReadBinding(t, &value.ReadSourceReceipts[0], func(binding *readSourceV3PrivateBinding) {
				binding.ContentHash = strings.Repeat("d", 64)
			})
		}},
		{name: "duplicate read receipt", mutate: func(_ *testing.T, value *ValidateCitationV3Authority) {
			value.ReadSourceReceipts[1] = value.ReadSourceReceipts[0]
		}},
		{name: "read output unknown field", mutate: func(t *testing.T, value *ValidateCitationV3Authority) {
			setValidateCitationV3ReceiptOutput(t, &value.ReadSourceReceipts[0], []byte(`{"content_hash":"`+
				strings.Repeat("a", 64)+`","evidence_ref":"E1","excerpt":"immutable excerpt","truncated":false,"path":"docs/evidence.md"}`))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority := validateCitationV3TestAuthority(t, []string{"E2", "E1"})
			test.mutate(t, &authority)
			resolver, err := NewValidateCitationV3ReceiptResolver(&validateCitationV3AuthorityReaderFake{authority: authority})
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.ResolveValidateCitationV3(context.Background(), validateCitationV3TestResolveRequest([]string{"E2", "E1"}))
			requireErrorCode(t, err, errorCodeReadSourceReceiptInvalid)
			if strings.Contains(fmt.Sprint(err), "private snippet") || strings.Contains(fmt.Sprint(err), "docs/evidence.md") {
				t.Fatalf("receipt error leaked a persisted document: %v", err)
			}
		})
	}
}

func TestValidateCitationV3ReceiptResolverRejectsInvalidRequestBeforeAuthorityRead(t *testing.T) {
	tests := []struct {
		name    string
		ctx     context.Context
		request ValidateCitationV3ResolveRequest
	}{
		{name: "nil context", request: validateCitationV3TestResolveRequest([]string{"E1"})},
		{name: "candidate hash", ctx: context.Background(), request: func() ValidateCitationV3ResolveRequest {
			value := validateCitationV3TestResolveRequest([]string{"E1"})
			value.CandidateHash = strings.Repeat("A", 64)
			return value
		}()},
		{name: "reused id", ctx: context.Background(), request: func() ValidateCitationV3ResolveRequest {
			value := validateCitationV3TestResolveRequest([]string{"E1"})
			value.CandidateID = value.WorkspaceID
			return value
		}()},
		{name: "unselected ref", ctx: context.Background(), request: validateCitationV3TestResolveRequest([]string{"E4"})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &validateCitationV3AuthorityReaderFake{}
			resolver, err := NewValidateCitationV3ReceiptResolver(reader)
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.ResolveValidateCitationV3(test.ctx, test.request)
			requireErrorCode(t, err, errorCodeCitationInputInvalid)
			if reader.calls != 0 {
				t.Fatalf("invalid request reached authority reader: calls=%d", reader.calls)
			}
		})
	}

	var nilReader *validateCitationV3AuthorityReaderFake
	if _, err := NewValidateCitationV3ReceiptResolver(nilReader); err == nil {
		t.Fatal("constructor accepted typed nil authority reader")
	}
}

func TestValidateCitationV3ReceiptResolverPreservesAuthorityError(t *testing.T) {
	want := foundation.NewError(foundation.ErrorDependencyUnavailable, "CITATION_AUTHORITY_DOWN", true, errors.New("down"))
	resolver, err := NewValidateCitationV3ReceiptResolver(&validateCitationV3AuthorityReaderFake{err: want})
	if err != nil {
		t.Fatal(err)
	}
	_, actual := resolver.ResolveValidateCitationV3(context.Background(), validateCitationV3TestResolveRequest([]string{"E1"}))
	if !errors.Is(actual, want) {
		t.Fatalf("authority error=%v", actual)
	}
}

func validateCitationV3TestAuthority(t *testing.T, candidateRefs []string) ValidateCitationV3Authority {
	t.Helper()
	identities := []ReadSourceV3Identity{
		validateCitationV3TestIdentity("E1", generatedID(79, 1), generatedID(78, 1)),
		validateCitationV3TestIdentity("E2", generatedID(79, 2), generatedID(78, 2)),
		validateCitationV3TestIdentity("E3", generatedID(79, 3), generatedID(78, 3)),
	}
	searchOutput, err := json.Marshal(searchKnowledgeV2Output{
		EffectiveMode: "keyword",
		Items: []searchKnowledgeV2OutputItem{
			{EvidenceRef: "E1", Rank: 1, Snippet: "private snippet one"},
			{EvidenceRef: "E2", Rank: 2, Snippet: "private snippet two"},
			{EvidenceRef: "E3", Rank: 3, Snippet: "private snippet three"},
		},
		Degradations: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	identityDocuments := make([]readSourceV3IdentityDocument, len(identities))
	for index, identity := range identities {
		identityDocuments[index] = readSourceV3IdentityDocumentFrom(identity)
	}
	searchBinding, err := json.Marshal(searchKnowledgeV2PrivateBinding{
		Items: identityDocuments, SelectedRefs: []string{"E1", "E2", "E3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	searchReceipt := validateCitationV3TestPersistedReceipt(t, searchKnowledgeV2Ref, 1, searchOutput, searchBinding)
	byRef := make(map[string]ReadSourceV3Identity, len(identities))
	for _, identity := range identities {
		byRef[identity.EvidenceRef] = identity
	}
	reads := make([]toolsdomain.ResultReceipt, 0, len(candidateRefs))
	for index := len(candidateRefs) - 1; index >= 0; index-- {
		identity := byRef[candidateRefs[index]]
		truncated := false
		output, marshalErr := json.Marshal(validateCitationV3ReadReceiptOutput{
			ContentHash: identity.ContentHash, EvidenceRef: identity.EvidenceRef,
			Excerpt: "immutable excerpt", Truncated: &truncated,
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		binding, marshalErr := json.Marshal(readSourceV3PrivateBinding{
			SearchReceiptID: searchReceipt.ID, SearchReceiptHash: searchReceipt.OutputHash,
			readSourceV3IdentityDocument: readSourceV3IdentityDocumentFrom(identity),
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		reads = append(reads, validateCitationV3TestPersistedReceipt(t, readSourceV3Ref, index+2, output, binding))
	}
	return ValidateCitationV3Authority{
		WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, AnalysisRunID: generatedID(91, 1),
		CandidateID: testValidateCitationV3CandidateID, CandidateHash: strings.Repeat("c", 64),
		EvidenceRefs: append([]string(nil), candidateRefs...), SearchReceipt: searchReceipt, ReadSourceReceipts: reads,
	}
}

func validateCitationV3TestPersistedReceipt(
	t *testing.T,
	tool toolsdomain.ToolRef,
	ordinal int,
	output []byte,
	binding []byte,
) toolsdomain.ResultReceipt {
	t.Helper()
	output = validateCitationV3TestCanonicalDocument(t, output)
	binding = validateCitationV3TestCanonicalDocument(t, binding)
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(tool)
	if !found {
		t.Fatalf("missing receipt contract for %s@%d", tool.Name, tool.Version)
	}
	return toolsdomain.ResultReceipt{
		ID: generatedID(92, ordinal), ToolCallID: generatedID(93, ordinal),
		WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID,
		NodeRunID: generatedID(94, ordinal), NodeAttemptID: generatedID(95, ordinal), Tool: tool,
		OutputSchema: contract.OutputSchema, DefinitionHash: strings.Repeat("a", 64),
		PersistencePolicy: toolsdomain.ResultPersistenceCanonical,
		MaxOutputBytes:    contract.MaxOutputBytes, MaxPrivateBindingBytes: contract.MaxPrivateBindingBytes,
		Output: append(json.RawMessage(nil), output...), OutputHash: hashDocument(output), OutputBytes: int64(len(output)),
		PrivateBinding: &toolsdomain.ResultReceiptPrivateBinding{
			Schema: contract.PrivateBindingSchema, Document: append(json.RawMessage(nil), binding...),
			Hash: hashDocument(binding), Bytes: int64(len(binding)),
		},
		CreatedAt: time.Date(2026, 8, 15, 9, ordinal, 0, 0, time.UTC),
	}
}

func validateCitationV3TestCanonicalDocument(t *testing.T, document []byte) []byte {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(document, &value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func validateCitationV3TestResolveRequest(refs []string) ValidateCitationV3ResolveRequest {
	return ValidateCitationV3ResolveRequest{
		WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID, CandidateID: testValidateCitationV3CandidateID,
		CandidateHash: strings.Repeat("c", 64), EvidenceRefs: append([]string(nil), refs...),
	}
}

func mutateValidateCitationV3ReadBinding(
	t *testing.T,
	receipt *toolsdomain.ResultReceipt,
	mutate func(*readSourceV3PrivateBinding),
) {
	t.Helper()
	var binding readSourceV3PrivateBinding
	if err := json.Unmarshal(receipt.PrivateBinding.Document, &binding); err != nil {
		t.Fatal(err)
	}
	mutate(&binding)
	document, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	document = validateCitationV3TestCanonicalDocument(t, document)
	receipt.PrivateBinding.Document = document
	receipt.PrivateBinding.Bytes = int64(len(document))
	receipt.PrivateBinding.Hash = hashDocument(document)
}

func setValidateCitationV3ReceiptOutput(t *testing.T, receipt *toolsdomain.ResultReceipt, output []byte) {
	t.Helper()
	output = validateCitationV3TestCanonicalDocument(t, output)
	receipt.Output = append(json.RawMessage(nil), output...)
	receipt.OutputBytes = int64(len(output))
	receipt.OutputHash = hashDocument(output)
}

type validateCitationV3AuthorityReaderFake struct {
	authority ValidateCitationV3Authority
	query     ValidateCitationV3AuthorityQuery
	err       error
	calls     int
}

func (fake *validateCitationV3AuthorityReaderFake) LoadValidateCitationV3Authority(
	_ context.Context,
	query ValidateCitationV3AuthorityQuery,
) (ValidateCitationV3Authority, error) {
	fake.calls++
	fake.query = query
	result := fake.authority
	result.EvidenceRefs = append([]string(nil), result.EvidenceRefs...)
	result.ReadSourceReceipts = append([]toolsdomain.ResultReceipt(nil), result.ReadSourceReceipts...)
	return result, fake.err
}

var _ ValidateCitationV3AuthorityReader = (*validateCitationV3AuthorityReaderFake)(nil)
var _ ValidateCitationV3Resolver = (*ValidateCitationV3ReceiptResolver)(nil)
