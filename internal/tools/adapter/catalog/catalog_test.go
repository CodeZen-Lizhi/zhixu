package catalog

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	testID1   = "10000000-0000-4000-8000-000000000001"
	testID2   = "10000000-0000-4000-8000-000000000002"
	testID3   = "10000000-0000-4000-8000-000000000003"
	testID4   = "10000000-0000-4000-8000-000000000004"
	testID5   = "10000000-0000-4000-8000-000000000005"
	testHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testOID   = "cccccccccccccccccccccccccccccccccccccccc"
)

type contractFixture struct {
	name           string
	inputSchemaID  string
	outputSchemaID string
	goodInput      string
	badInput       string
	goodOutput     string
	badOutput      string
}

func TestContractsContainExactUniqueCoreToolSet(t *testing.T) {
	contracts := mustContracts(t)
	if len(contracts) != 11 {
		t.Fatalf("Contracts() count = %d, want 11", len(contracts))
	}
	got := make([]string, 0, len(contracts))
	seen := make(map[string]struct{}, len(contracts))
	for _, contract := range contracts {
		name := contract.Definition.Ref.Name
		if _, duplicate := seen[name]; duplicate {
			t.Fatalf("duplicate built-in tool %q", name)
		}
		seen[name] = struct{}{}
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{
		"ApplyApprovedPatch", "CalculateDiff", "CreateGitCommit", "FetchWebPage", "ReadDocument", "ReadGitStatus",
		"ReadSource", "RebuildIndex", "RunRegressionEvaluation", "SearchKnowledge", "ValidateCitation",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
}

func TestContractsHaveStableExactSchemaRefsAndHashes(t *testing.T) {
	left := mustContracts(t)
	right := mustContracts(t)
	expectedHashes := map[string]string{
		"SearchKnowledge":         "c89a7f23de7ac737aefbbd91a7d2231ce303713a0ee451a5aaa152ac0e8024be",
		"ReadSource":              "c1984a7c2ef56e5fb59ea0c2d38c418023e6e82a814f74143ca9b92d6a111361",
		"ReadDocument":            "af5c0e57ac8099f23909908c56e7dffe1479912b45492b40f4189ba65511488b",
		"FetchWebPage":            "d067073172c9934fdb8a363432127ec8f96dbe8f831c6e9b207912ce2abd7c63",
		"ValidateCitation":        "af9ae558607202a7b9ada28bef5c41859a556b23b9698f03602f5e672e357829",
		"CalculateDiff":           "e7693463f013f1f5f5d5286629907f2689f871c8d6573071aa090eaa0cddb081",
		"ReadGitStatus":           "fef29851ef283d17331787512e3982eeb32c111c6372dcdf1e333166a57f2bc8",
		"ApplyApprovedPatch":      "4a43c6efe8093fd3b25f34683a263fc0b8203fcff2ce24d9be77c92e7493baa5",
		"CreateGitCommit":         "7f31573506e734c450b8e7a8653716bd9840e3677f9bf831e36dafd2ff3eb45c",
		"RebuildIndex":            "4184b8712c0b95a55a6412f2bea6bb30f21762ce886fcf4a4afcbde21f7be142",
		"RunRegressionEvaluation": "4765b2a9a4acc38a8e5c69039d64b55300e7841f0b25082759a9e22fb31fef38",
	}
	expectedRefs := make(map[string][2]string, len(contractFixtures()))
	for _, fixture := range contractFixtures() {
		expectedRefs[fixture.name] = [2]string{fixture.inputSchemaID, fixture.outputSchemaID}
	}
	for index := range left {
		leftDefinition := left[index].Definition
		rightDefinition := right[index].Definition
		refs, exists := expectedRefs[leftDefinition.Ref.Name]
		if !exists || leftDefinition.Ref.Version != 1 || leftDefinition.InputSchema != (domain.SchemaRef{ID: refs[0], Version: 1}) ||
			leftDefinition.OutputSchema != (domain.SchemaRef{ID: refs[1], Version: 1}) {
			t.Fatalf("unexpected refs for %s: input=%+v output=%+v", leftDefinition.Ref.Name, leftDefinition.InputSchema, leftDefinition.OutputSchema)
		}
		if leftDefinition.DefinitionHash != expectedHashes[leftDefinition.Ref.Name] || leftDefinition.DefinitionHash != rightDefinition.DefinitionHash {
			t.Fatalf("unstable definition hash for %s: got %q, replay %q, want %q", leftDefinition.Ref.Name, leftDefinition.DefinitionHash, rightDefinition.DefinitionHash, expectedHashes[leftDefinition.Ref.Name])
		}
		assertStrictSchemaDocument(t, leftDefinition.Ref.Name+" input", leftDefinition.InputSchemaDocument)
		assertStrictSchemaDocument(t, leftDefinition.Ref.Name+" output", leftDefinition.OutputSchemaDocument)
	}
}

func TestContractGoldenDocumentsAndStrictBoundary(t *testing.T) {
	byName := contractsByName(t)
	for _, fixture := range contractFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			contract := byName[fixture.name]
			assertDecoderAcceptsExactCopy(t, contract.DecodeInput, []byte(fixture.goodInput))
			assertDecoderAcceptsExactCopy(t, contract.DecodeOutput, []byte(fixture.goodOutput))
			if _, err := contract.DecodeInput([]byte(fixture.badInput)); err == nil {
				t.Fatal("input decoder accepted bad golden document")
			}
			if _, err := contract.DecodeOutput([]byte(fixture.badOutput)); err == nil {
				t.Fatal("output decoder accepted bad golden document")
			}
			assertRequiredFieldsMatchDecoder(t, contract.DecodeInput, contract.Definition.InputSchemaDocument, []byte(fixture.goodInput))
			assertRequiredFieldsMatchDecoder(t, contract.DecodeOutput, contract.Definition.OutputSchemaDocument, []byte(fixture.goodOutput))
			assertSharedStrictFailures(t, contract.DecodeInput, []byte(fixture.goodInput))
			assertSharedStrictFailures(t, contract.DecodeOutput, []byte(fixture.goodOutput))
			oversized := bytes.Repeat([]byte{'x'}, int(contract.Definition.MaxInputBytes)+1)
			if _, err := contract.DecodeInput(oversized); err == nil {
				t.Fatal("input decoder accepted oversized document")
			}
		})
	}
}

func TestEveryContractDecoderRejectsUnknownDuplicateTrailingNullAndType(t *testing.T) {
	byName := contractsByName(t)
	fixture := contractFixtures()[0]
	contract := byName[fixture.name]
	bad := map[string][]byte{
		"unknown":   []byte(`{"query":"q","mode":"keyword","limit":1,"source_ids":[],"source_version_ids":[],"workspace_id":"` + testID1 + `"}`),
		"duplicate": []byte(`{"query":"q","query":"other","mode":"keyword","limit":1,"source_ids":[],"source_version_ids":[]}`),
		"trailing":  []byte(fixture.goodInput + ` {}`),
		"null":      []byte(`{"query":null,"mode":"keyword","limit":1,"source_ids":[],"source_version_ids":[]}`),
		"type":      []byte(`{"query":"q","mode":"keyword","limit":"1","source_ids":[],"source_version_ids":[]}`),
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.DecodeInput(raw); err == nil {
				t.Fatalf("decoder accepted %s document", name)
			}
		})
	}
}

func TestContractDecoderCanonicalizesEquivalentDocuments(t *testing.T) {
	contract := contractsByName(t)["RunRegressionEvaluation"]
	left, err := contract.DecodeInput([]byte(`{"dataset_id":"10000000-0000-4000-8000-000000000001","index_version_id":"10000000-0000-4000-8000-000000000002"}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := contract.DecodeInput([]byte("{ \n \"index_version_id\" : \"10000000-0000-4000-8000-000000000002\", \"dataset_id\" : \"10000000-0000-4000-8000-000000000001\" }"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatalf("canonical documents differ: %s != %s", left, right)
	}
}

func TestPolicyMappingKeepsWritesHiddenAndMaintenanceCapabilitiesExact(t *testing.T) {
	contracts := contractsByName(t)
	for _, name := range []string{"ApplyApprovedPatch", "CreateGitCommit"} {
		definition := contracts[name].Definition
		if definition.InvocationPolicy != domain.InvocationTrustedWorkflowOnly || definition.SideEffectLevel != domain.SideEffectDomainWrite {
			t.Fatalf("%s policy = %s/%s", name, definition.InvocationPolicy, definition.SideEffectLevel)
		}
	}
	if contracts["FetchWebPage"].Definition.RequiredCapability != capability.ReadExternal ||
		contracts["FetchWebPage"].Definition.SideEffectLevel != domain.SideEffectExternalRead {
		t.Fatal("FetchWebPage is not bound to external-read policy")
	}
	if contracts["CalculateDiff"].Definition.RequiredCapability != "" ||
		len(contracts["CalculateDiff"].Definition.AllowedWorkflows) != 1 {
		t.Fatal("CalculateDiff must remain capability-free but workflow-bound")
	}
	index := contracts["RebuildIndex"].Definition
	evaluation := contracts["RunRegressionEvaluation"].Definition
	if index.RequiredCapability != capability.IndexMaintenance || evaluation.RequiredCapability != capability.EvaluationRun ||
		index.RequiredCapability == evaluation.RequiredCapability || index.AllowedWorkflows[0] == evaluation.AllowedWorkflows[0] ||
		index.InvocationPolicy != domain.InvocationTrustedWorkflowOnly || evaluation.InvocationPolicy != domain.InvocationTrustedWorkflowOnly {
		t.Fatalf("maintenance permissions or workflow bindings are interchangeable: index=%+v evaluation=%+v", index, evaluation)
	}
	for _, contract := range contracts {
		if contract.Definition.InvocationPolicy == domain.InvocationModelRequestable &&
			(contract.Definition.SideEffectLevel == domain.SideEffectDomainWrite || contract.Definition.SideEffectLevel == domain.SideEffectUnknown) {
			t.Fatalf("write tool %s is model requestable", contract.Definition.Ref.Name)
		}
	}
}

func TestCatalogDoesNotProvideExecutorsOrAvailabilityFallback(t *testing.T) {
	registry := application.NewExecutionRegistry()
	for _, contract := range mustContracts(t) {
		if err := registry.RegisterContract(contract); err != nil {
			t.Fatalf("RegisterContract(%s): %v", contract.Definition.Ref.Name, err)
		}
	}
	if err := registry.Freeze(); err == nil {
		t.Fatal("contract catalog made tools executable without real executors")
	}
}

func assertDecoderAcceptsExactCopy(t *testing.T, decoder application.DocumentDecoder, raw []byte) {
	t.Helper()
	original := append([]byte(nil), raw...)
	accepted, err := decoder(raw)
	if err != nil {
		t.Fatalf("decoder rejected good document: %v", err)
	}
	if !bytes.Equal(accepted, original) || !bytes.Equal(raw, original) {
		t.Fatal("decoder transformed or mutated accepted document")
	}
	if len(accepted) > 0 {
		accepted[0] ^= 1
		if !bytes.Equal(raw, original) {
			t.Fatal("decoder result aliases caller-owned document")
		}
	}
}

func assertRequiredFieldsMatchDecoder(t *testing.T, decoder application.DocumentDecoder, schemaDocument, good []byte) {
	t.Helper()
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schemaDocument, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(good, &value); err != nil {
		t.Fatalf("decode good document: %v", err)
	}
	for _, field := range schema.Required {
		copyValue := make(map[string]json.RawMessage, len(value)-1)
		for key, raw := range value {
			if key != field {
				copyValue[key] = raw
			}
		}
		without, err := json.Marshal(copyValue)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decoder(without); err == nil {
			t.Fatalf("decoder accepted missing required field %q", field)
		}
		copyValue[field] = json.RawMessage(`null`)
		withNull, err := json.Marshal(copyValue)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decoder(withNull); err == nil {
			t.Fatalf("decoder accepted null required field %q", field)
		}
	}
}

func assertSharedStrictFailures(t *testing.T, decoder application.DocumentDecoder, good []byte) {
	t.Helper()
	if len(good) < 2 || good[0] != '{' || good[len(good)-1] != '}' {
		t.Fatal("golden document is not an object")
	}
	inner := good[1 : len(good)-1]
	bad := [][]byte{
		append(append([]byte(nil), good...), []byte(` {}`)...),
		append([]byte(`{"__unknown":true,`), append(inner, '}')...),
		append([]byte(`{"__duplicate":1,"__duplicate":2,`), append(inner, '}')...),
	}
	for _, raw := range bad {
		if _, err := decoder(raw); err == nil {
			t.Fatalf("decoder accepted strict-boundary violation: %s", raw)
		}
	}
}

func assertStrictSchemaDocument(t *testing.T, label string, raw []byte) {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("%s schema is invalid JSON: %v", label, err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("%s schema root is not a strict object", label)
	}
}

func mustContracts(t *testing.T) []application.Contract {
	t.Helper()
	contracts, err := Contracts()
	if err != nil {
		t.Fatalf("Contracts(): %v", err)
	}
	return contracts
}

func contractsByName(t *testing.T) map[string]application.Contract {
	t.Helper()
	result := make(map[string]application.Contract, 11)
	for _, contract := range mustContracts(t) {
		result[contract.Definition.Ref.Name] = contract
	}
	return result
}

func contractFixtures() []contractFixture {
	citation := `{"citation_id":"cite-1","index_version_id":"` + testID1 + `","chunk_id":"` + testID2 + `","source_version_id":"` + testID3 + `","source_span_id":"` + testID4 + `"}`
	return []contractFixture{
		{name: "SearchKnowledge", inputSchemaID: "tool.search_knowledge.input", outputSchemaID: "tool.search_knowledge.output",
			goodInput:  `{"query":"bounded query","mode":"hybrid","limit":10,"source_ids":[],"source_version_ids":["` + testID1 + `"]}`,
			badInput:   `{"query":"bounded query","mode":"all","limit":10,"source_ids":[],"source_version_ids":[]}`,
			goodOutput: `{"index_version_id":"` + testID1 + `","effective_mode":"hybrid","items":[{"citation_id":"cite-1","chunk_id":"` + testID2 + `","source_version_id":"` + testID3 + `","source_span_id":"` + testID4 + `","content_hash":"` + testHashA + `","rank":1,"snippet":"evidence"}],"degradations":[]}`,
			badOutput:  `{"index_version_id":"` + testID1 + `","effective_mode":"hybrid","items":[{"citation_id":"cite-1","chunk_id":"` + testID2 + `","source_version_id":"` + testID3 + `","source_span_id":"` + testID4 + `","content_hash":"` + testHashA + `","rank":0,"snippet":"evidence"}],"degradations":[]}`},
		{name: "ReadSource", inputSchemaID: "tool.read_source.input", outputSchemaID: "tool.read_source.output",
			goodInput:  `{"source_version_id":"` + testID1 + `","source_span_id":"` + testID2 + `"}`,
			badInput:   `{"source_version_id":"not-an-id","source_span_id":"` + testID2 + `"}`,
			goodOutput: `{"source_version_id":"` + testID1 + `","source_span_id":"` + testID2 + `","content_hash":"` + testHashA + `","excerpt":"immutable excerpt"}`,
			badOutput:  `{"source_version_id":"` + testID1 + `","source_span_id":"` + testID2 + `","content_hash":"` + testHashA + `","excerpt":""}`},
		{name: "ReadDocument", inputSchemaID: "tool.read_document.input", outputSchemaID: "tool.read_document.output",
			goodInput: `{"document_id":"` + testID1 + `"}`, badInput: `{"document_id":null}`,
			goodOutput: `{"document_id":"` + testID1 + `","document_version_id":"` + testID2 + `","content_hash":"` + testHashA + `","title":"Document","text":"approved content"}`,
			badOutput:  `{"document_id":"` + testID1 + `","document_version_id":"` + testID2 + `","content_hash":"` + testHashA + `","title":"Document","text":null}`},
		{name: "FetchWebPage", inputSchemaID: "tool.fetch_web_page.input", outputSchemaID: "tool.fetch_web_page.output",
			goodInput: `{"url":"https://example.test/page"}`, badInput: `{"url":"ftp://example.test/page"}`,
			goodOutput: `{"final_url":"https://example.test/page","fetched_at":"2026-07-19T00:00:00Z","content_type":"text/html","byte_count":4,"content_hash":"` + testHashA + `","text":"page","untrusted_data":true}`,
			badOutput:  `{"final_url":"https://example.test/page","fetched_at":"2026-07-19T00:00:00Z","content_type":"text/html","byte_count":4,"content_hash":"` + testHashA + `","text":"page","untrusted_data":false}`},
		{name: "ValidateCitation", inputSchemaID: "tool.validate_citation.input", outputSchemaID: "tool.validate_citation.output",
			goodInput: `{"citations":[` + citation + `]}`, badInput: `{"citations":[]}`,
			goodOutput: `{"results":[` + strings.TrimSuffix(citation, `}`) + `,"valid":true,"reason_code":"OK"}]}`,
			badOutput:  `{"results":[` + strings.TrimSuffix(citation, `}`) + `,"valid":true,"reason_code":"BINDING_MISMATCH"}]}`},
		{name: "CalculateDiff", inputSchemaID: "tool.calculate_diff.input", outputSchemaID: "tool.calculate_diff.output",
			goodInput: `{"before":"before","after":"after"}`, badInput: `{"before":null,"after":"after"}`,
			goodOutput: `{"changed":true,"before_hash":"` + testHashA + `","after_hash":"` + testHashB + `","patch":"@@ -1 +1 @@"}`,
			badOutput:  `{"changed":false,"before_hash":"` + testHashA + `","after_hash":"` + testHashB + `","patch":""}`},
		{name: "ReadGitStatus", inputSchemaID: "tool.read_git_status.input", outputSchemaID: "tool.read_git_status.output",
			goodInput: `{}`, badInput: `{"repo":"/tmp/repo"}`,
			goodOutput: `{"branch":"main","head":"` + testOID + `","object_format":"sha1","clean":true,"staged_count":0,"unstaged_count":0,"untracked_count":0,"conflict_count":0}`,
			badOutput:  `{"branch":"main","head":"` + testOID + `","object_format":"sha1","clean":true,"staged_count":1,"unstaged_count":0,"untracked_count":0,"conflict_count":0}`},
		{name: "ApplyApprovedPatch", inputSchemaID: "tool.apply_approved_patch.input", outputSchemaID: "tool.apply_approved_patch.output",
			goodInput: `{"writeback_execution_id":"` + testID1 + `"}`, badInput: `{"writeback_execution_id":"execution"}`,
			goodOutput: `{"writeback_execution_id":"` + testID1 + `","result_ref":"writeback:` + testID1 + `","result_hash":"` + testHashA + `","status":"APPLIED"}`,
			badOutput:  `{"writeback_execution_id":"` + testID1 + `","result_ref":"writeback:` + testID1 + `","result_hash":"` + testHashA + `","status":"FAILED"}`},
		{name: "CreateGitCommit", inputSchemaID: "tool.create_git_commit.input", outputSchemaID: "tool.create_git_commit.output",
			goodInput: `{"writeback_execution_id":"` + testID1 + `"}`, badInput: `{"writeback_execution_id":null}`,
			goodOutput: `{"writeback_execution_id":"` + testID1 + `","result_ref":"writeback:` + testID1 + `","commit_oid":"` + testOID + `","status":"COMMITTED"}`,
			badOutput:  `{"writeback_execution_id":"` + testID1 + `","result_ref":"writeback:` + testID1 + `","commit_oid":"invalid","status":"COMMITTED"}`},
		{name: "RebuildIndex", inputSchemaID: "tool.rebuild_index.input", outputSchemaID: "tool.rebuild_index.output",
			goodInput: `{"reindex_delivery_id":"` + testID1 + `"}`, badInput: `{"reindex_delivery_id":"delivery"}`,
			goodOutput: `{"reindex_delivery_id":"` + testID1 + `","index_version_id":"` + testID2 + `","result_ref":"reindex:` + testID1 + `","status":"SUCCEEDED"}`,
			badOutput:  `{"reindex_delivery_id":"` + testID1 + `","index_version_id":"` + testID2 + `","result_ref":"reindex:` + testID1 + `","status":"STARTED"}`},
		{name: "RunRegressionEvaluation", inputSchemaID: "tool.run_regression_evaluation.input", outputSchemaID: "tool.run_regression_evaluation.output",
			goodInput:  `{"dataset_id":"` + testID1 + `","index_version_id":"` + testID2 + `"}`,
			badInput:   `{"dataset_id":"` + testID1 + `","index_version_id":"index"}`,
			goodOutput: `{"evaluation_run_id":"` + testID5 + `","result_ref":"evaluation:` + testID5 + `","passed":true,"status":"PASSED"}`,
			badOutput:  `{"evaluation_run_id":"` + testID5 + `","result_ref":"evaluation:` + testID5 + `","passed":false,"status":"PASSED"}`},
	}
}
