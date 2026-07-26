package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestRegisteredDefinitionHasStableCanonicalGraphHash(t *testing.T) {
	definition := RegisteredDefinition()
	wantHash, err := workflowapplication.ComputeCanonicalGraphHash(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Key != DefinitionKey || definition.Version != DefinitionVersion ||
		definition.InputSchemaVersion != InputSchemaVersion || definition.GraphHash != wantHash ||
		definition.GraphHash != definitionGraphHash || len(definition.Graph.Nodes) != 1 {
		t.Fatalf("definition = %#v, canonical hash = %q", definition, wantHash)
	}
	node := definition.Graph.Nodes[0]
	if node.Key != NodeKey || node.Kind != NodeKind || node.InputSchemaVersion != InputSchemaVersion ||
		node.OutputSchemaVersion != OutputSchemaVersion || len(node.AllowedTools) != 0 ||
		len(node.RequiredPermissions) != 2 || node.RequiredPermissions[0] != workflowdomain.PermissionReadLocal ||
		node.RequiredPermissions[1] != workflowdomain.PermissionWriteProposal {
		t.Fatalf("node = %#v", node)
	}
}

func TestInputRoundTripContainsExactlyFrozenSixFields(t *testing.T) {
	input := validWorkflowInput()
	encoded, err := EncodeInput(input)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 6 {
		t.Fatalf("fields = %v", fields)
	}
	decoded, err := DecodeInput(encoded)
	if err != nil || decoded != input {
		t.Fatalf("decoded = %#v, err = %v", decoded, err)
	}
}

func TestDecodeInputRejectsUnknownDuplicateTrailingAndNull(t *testing.T) {
	encoded, err := EncodeInput(validWorkflowInput())
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"unknown":   append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"workspace_id":"90000000-0000-4000-8000-000000000099"}`)...),
		"duplicate": append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"section_key":"second"}`)...),
		"trailing":  append(append([]byte(nil), encoded...), []byte(` {}`)...),
		"null":      []byte(strings.Replace(string(encoded), `"section_key":"first"`, `"section_key":null`, 1)),
		"content":   append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"content":"caller controlled"}`)...),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeInput(raw); testWorkflowErrorCode(err) != ErrorCodeInputInvalid {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestOutputReceiptAllowsIndependentSectionRebase(t *testing.T) {
	input := validWorkflowInput()
	receipt := validOutputReceipt(input)
	receipt.BaseRevisionID = artifactWorkflowTestID(20)
	receipt.RevisionID = artifactWorkflowTestID(21)
	receipt.RevisionNo = input.RevisionNo + 2
	receipt.ArtifactVersion = input.ArtifactVersion + 2
	if err := ValidateOutputReceiptBinding(receipt, input); err != nil {
		t.Fatalf("rebased receipt rejected: %v", err)
	}
	encoded, err := EncodeOutputReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOutputReceipt(encoded)
	if err != nil || decoded != receipt {
		t.Fatalf("decoded = %#v, err = %v", decoded, err)
	}

	stale := receipt
	stale.RevisionNo = input.RevisionNo
	if testWorkflowErrorCode(ValidateOutputReceiptBinding(stale, input)) != ErrorCodeOutputInvalid {
		t.Fatal("receipt did not require a revision newer than the frozen source")
	}
	stale = receipt
	stale.ArtifactVersion = input.ArtifactVersion
	if testWorkflowErrorCode(ValidateOutputReceiptBinding(stale, input)) != ErrorCodeOutputInvalid {
		t.Fatal("receipt did not require an artifact version newer than the frozen source")
	}
}

func TestDecodeOutputReceiptRejectsUnknownDuplicateTrailingNullAndBody(t *testing.T) {
	encoded, err := EncodeOutputReceipt(validOutputReceipt(validWorkflowInput()))
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"unknown":   append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"provider":"forbidden"}`)...),
		"duplicate": append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"section_key":"second"}`)...),
		"trailing":  append(append([]byte(nil), encoded...), []byte(` []`)...),
		"null":      []byte(strings.Replace(string(encoded), `"content_hash":"`+strings.Repeat("a", 64)+`"`, `"content_hash":null`, 1)),
		"body":      append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"content":"forbidden"}`)...),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeOutputReceipt(raw); testWorkflowErrorCode(err) != ErrorCodeOutputInvalid {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func validWorkflowInput() Input {
	return Input{
		SchemaVersion: InputSchemaVersion, ArtifactID: artifactWorkflowTestID(2), RevisionID: artifactWorkflowTestID(3),
		RevisionNo: 3, ArtifactVersion: 3, SectionKey: "first",
	}
}

func validOutputReceipt(input Input) OutputReceipt {
	return OutputReceipt{
		SchemaVersion: OutputSchemaVersion, ArtifactID: input.ArtifactID, BaseRevisionID: input.RevisionID,
		RevisionID: artifactWorkflowTestID(4), RevisionNo: input.RevisionNo + 1,
		ArtifactVersion: input.ArtifactVersion + 1, SectionKey: input.SectionKey,
		ModelRunID: artifactWorkflowTestID(5), ContentHash: strings.Repeat("a", 64),
	}
}

func artifactWorkflowTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("90000000-0000-4000-8000-%012d", value))
}

func testWorkflowErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
