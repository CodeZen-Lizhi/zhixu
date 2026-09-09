package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func dynamicTestReceipt(t *testing.T, ref ToolRef, id int, output, binding any) ResultReceipt {
	t.Helper()
	d := validResultReceiptDefinition(t, ref)
	o := resultReceiptTestJSON(t, output)
	call := validResultReceiptCall(t, d, o)
	c, _ := WorkspaceAnalysisResultReceiptContract(ref)
	r, err := NewResultReceipt(ResultReceiptDraft{ID: resultReceiptTestID(id), Output: o, PrivateBindingSchema: c.PrivateBindingSchema, PrivateBinding: resultReceiptTestJSON(t, binding), CreatedAt: resultReceiptTestTime()}, call, d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDynamicSearchAuthorizesAllHitsAndProjectsStableGlobalRefs(t *testing.T) {
	items := make([]searchKnowledgeV2ReceiptItem, 5)
	identities := make([]resultReceiptCitationIdentity, 5)
	refs := make([]string, 5)
	for i := range items {
		ref := resultReceiptEvidenceRef(i + 1)
		refs[i] = ref
		items[i] = searchKnowledgeV2ReceiptItem{EvidenceRef: ref, Rank: i + 1, Snippet: "safe evidence"}
		identities[i] = resultReceiptTestCitationIdentity(ref, 11+i*10, string(rune('a'+i)))
		identities[i].IndexVersionID = identities[0].IndexVersionID
		identities[i].CitationID = "cite-" + strings.Repeat(string(rune('a'+i)), 64)
	}
	r := dynamicTestReceipt(t, ToolRef{Name: "SearchKnowledge", Version: 3}, 6, searchKnowledgeV2ReceiptOutput{EffectiveMode: "keyword", Items: items, Degradations: []string{}}, searchKnowledgeV2PrivateBinding{Items: identities, SelectedRefs: refs})
	got, err := SearchKnowledgeV3ReceiptIdentities(r)
	if err != nil || len(got) != 5 {
		t.Fatalf("all search hits not authorized: %v", err)
	}
	aliases := map[string]string{"E1": "E6", "E2": "E7", "E3": "E8", "E4": "E9", "E5": "E10"}
	model, err := SearchKnowledgeV3ReceiptModelOutput(r, aliases)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(model), `"evidence_ref":"E10"`) || strings.Contains(string(model), "citation_id") {
		t.Fatal("model output did not use safe global references")
	}
	aliases["E2"] = "E6"
	if _, err := SearchKnowledgeV3ReceiptModelOutput(r, aliases); err == nil {
		t.Fatal("alias collision accepted")
	}
	legacy := searchKnowledgeV2PrivateBinding{Items: identities, SelectedRefs: refs[:3]}
	if validateSearchKnowledgeV3PrivateBinding(legacy) == nil {
		t.Fatal("v3 silently retained v1 three-hit selection")
	}
}

func TestDynamicReadBindsGlobalRefToExactSearchLocalTuple(t *testing.T) {
	id := resultReceiptTestCitationIdentity("E1", 11, "a")
	search := dynamicTestReceipt(t, ToolRef{Name: "SearchKnowledge", Version: 3}, 6, searchKnowledgeV2ReceiptOutput{EffectiveMode: "keyword", Items: []searchKnowledgeV2ReceiptItem{{EvidenceRef: "E1", Rank: 1, Snippet: "historical"}}, Degradations: []string{}}, searchKnowledgeV2PrivateBinding{Items: []resultReceiptCitationIdentity{id}, SelectedRefs: []string{"E1"}})
	id.EvidenceRef = "E12"
	truncated := false
	read := dynamicTestReceipt(t, ToolRef{Name: "ReadSource", Version: 4}, 7, readSourceV3ReceiptOutput{EvidenceRef: "E12", ContentHash: id.ContentHash, Truncated: &truncated, Excerpt: "historical exact excerpt"}, readSourceV4PrivateBinding{SearchReceiptID: search.ID, SearchReceiptHash: search.OutputHash, SearchEvidenceRef: "E1", resultReceiptCitationIdentity: id})
	evidence, err := ReadSourceV4ReceiptEvidenceForSearch(read, search)
	if err != nil || evidence.EvidenceRef != "E12" || evidence.Excerpt != "historical exact excerpt" {
		t.Fatalf("exact read failed: %v", err)
	}
	for _, mutate := range []func(*ResultReceipt){
		func(r *ResultReceipt) { r.ID = resultReceiptTestID(8) },
		func(r *ResultReceipt) { r.WorkspaceID = resultReceiptTestID(90) },
		func(r *ResultReceipt) { r.WorkflowRunID = resultReceiptTestID(91) },
		func(r *ResultReceipt) { r.OutputHash = strings.Repeat("b", 64) },
	} {
		other := search
		mutate(&other)
		if _, err := ReadSourceV4ReceiptEvidenceForSearch(read, other); err == nil {
			t.Fatal("foreign or replacement Search accepted")
		}
	}
	if _, err := ReadSourceV3ReceiptEvidenceForSearch(read, search); err == nil {
		t.Fatal("v1 read accepted a v2 receipt")
	}
}

func TestDynamicCitationLoopCannotServeAsPublicationReceipt(t *testing.T) {
	id := resultReceiptTestCitationIdentity("E20", 11, "a")
	valid := true
	binding := validateCitationV4PrivateBinding{Results: []validateCitationV4PrivateIdentity{{SearchReceiptID: resultReceiptTestID(6), SearchReceiptHash: strings.Repeat("a", 64), SearchEvidenceRef: "E2", ReadReceiptID: resultReceiptTestID(7), ReadReceiptHash: strings.Repeat("b", 64), resultReceiptCitationIdentity: id}}}
	r := dynamicTestReceipt(t, ToolRef{Name: "ValidateCitation", Version: 4}, 8, validateCitationV3ReceiptOutput{Results: []validateCitationV3ReceiptResult{{EvidenceRef: "E20", Valid: &valid, ReasonCode: "OK"}}}, binding)
	if _, err := ValidateCitationV4ReceiptResults(r, "", "", []string{"E20"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateCitationV4ReceiptResults(r, resultReceiptTestID(90), strings.Repeat("c", 64), []string{"E20"}); err == nil {
		t.Fatal("loop validation was promoted to candidate validation")
	}
	if _, err := ValidateCitationV4ReceiptCitations(r, "", "", []string{"E20"}); err == nil {
		t.Fatal("loop validation was promoted to publication")
	}
	malformed := map[string]any{}
	if err := json.Unmarshal(r.PrivateBinding.Document, &malformed); err != nil {
		t.Fatal(err)
	}
	malformed["unexpected_body"] = "private"
	if safePrivateBinding(resultReceiptTestJSON(t, malformed)) {
		t.Fatal("private binding accepted a forbidden body")
	}
}
