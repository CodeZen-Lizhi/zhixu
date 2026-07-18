package contract

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	testWorkspaceID foundation.ID = "a0000000-0000-4000-8000-000000000001"
	testRunID       foundation.ID = "b0000000-0000-4000-8000-000000000001"
	testNodeID      foundation.ID = "c0000000-0000-4000-8000-000000000001"
	testProposalID  foundation.ID = "d0000000-0000-4000-8000-000000000001"
	testRevisionID  foundation.ID = "e0000000-0000-4000-8000-000000000001"
	testApprovalID  foundation.ID = "f0000000-0000-4000-8000-000000000001"
	testExecutionID foundation.ID = "70000000-0000-4000-8000-00000000000a"
	testTargetPath                = "notes/a.md"
	testResultHash                = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testGitCommit                 = "dddddddddddddddddddddddddddddddddddddddd"
)

func TestRequestV1RoundTripIsCanonical(t *testing.T) {
	request := validRequestV1()
	request.ResultHash = strings.ToUpper(request.ResultHash)
	request.GitCommit = strings.ToUpper(request.GitCommit)

	encoded, err := EncodeCanonical(request)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"schema_version":1,"workspace_id":"a0000000-0000-4000-8000-000000000001","workflow_run_id":"b0000000-0000-4000-8000-000000000001","node_run_id":"c0000000-0000-4000-8000-000000000001","proposal_id":"d0000000-0000-4000-8000-000000000001","revision_id":"e0000000-0000-4000-8000-000000000001","approval_id":"f0000000-0000-4000-8000-000000000001","writeback_execution_id":"70000000-0000-4000-8000-00000000000a","target_path":"notes/a.md","result_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","git_commit":"dddddddddddddddddddddddddddddddddddddddd"}`)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("encoded=%s", encoded)
	}

	decoded, err := DecodeStrict(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != validRequestV1() {
		t.Fatalf("decoded=%#v", decoded)
	}
}

func TestRequestV1SupportsCanonicalSHA256GitCommit(t *testing.T) {
	request := validRequestV1()
	request.GitCommit = strings.Repeat("E", 64)
	encoded, err := EncodeCanonical(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStrict(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.GitCommit != strings.Repeat("e", 64) {
		t.Fatalf("git commit=%q", decoded.GitCommit)
	}
}

func TestRequestV1DecodeRejectsMalformedOrNonCanonicalPayload(t *testing.T) {
	canonical, err := EncodeCanonical(validRequestV1())
	if err != nil {
		t.Fatal(err)
	}
	invalid := map[string][]byte{
		"empty":             nil,
		"array":             []byte(`[]`),
		"unknown field":     bytes.Replace(canonical, []byte(`}`), []byte(`,"credential":"secret"}`), 1),
		"missing field":     bytes.Replace(canonical, []byte(`,"git_commit":"`+testGitCommit+`"`), nil, 1),
		"unknown version":   bytes.Replace(canonical, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"wrong field type":  bytes.Replace(canonical, []byte(`"schema_version":1`), []byte(`"schema_version":"1"`), 1),
		"null field":        bytes.Replace(canonical, []byte(`"target_path":"`+testTargetPath+`"`), []byte(`"target_path":null`), 1),
		"noncanonical id":   bytes.Replace(canonical, []byte(testWorkspaceID), []byte(strings.ToUpper(string(testWorkspaceID))), 1),
		"noncanonical path": bytes.Replace(canonical, []byte(testTargetPath), []byte(`notes/../a.md`), 1),
		"uppercase hash":    bytes.Replace(canonical, []byte(testResultHash), []byte(strings.ToUpper(testResultHash)), 1),
		"uppercase commit":  bytes.Replace(canonical, []byte(testGitCommit), []byte(strings.ToUpper(testGitCommit)), 1),
		"invalid hash":      bytes.Replace(canonical, []byte(testResultHash), []byte(strings.Repeat("z", 64)), 1),
		"invalid commit":    bytes.Replace(canonical, []byte(testGitCommit), []byte(strings.Repeat("d", 39)), 1),
		"trailing json":     append(append([]byte(nil), canonical...), []byte(` {}`)...),
		"duplicate field":   bytes.Replace(canonical, []byte(`{"schema_version":1`), []byte(`{"schema_version":1,"schema_version":1`), 1),
	}
	for name, payload := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeStrict(payload); err == nil {
				t.Fatalf("invalid payload accepted: %s", payload)
			} else {
				assertContractError(t, err)
			}
		})
	}
}

func TestRequestV1DecodeAcceptsObjectFieldOrderAndWhitespace(t *testing.T) {
	payload := []byte(` {
        "git_commit":"dddddddddddddddddddddddddddddddddddddddd",
        "result_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "target_path":"notes/a.md",
        "writeback_execution_id":"70000000-0000-4000-8000-00000000000a",
        "approval_id":"f0000000-0000-4000-8000-000000000001",
        "revision_id":"e0000000-0000-4000-8000-000000000001",
        "proposal_id":"d0000000-0000-4000-8000-000000000001",
        "node_run_id":"c0000000-0000-4000-8000-000000000001",
        "workflow_run_id":"b0000000-0000-4000-8000-000000000001",
        "workspace_id":"a0000000-0000-4000-8000-000000000001",
        "schema_version":1
    } `)
	decoded, err := DecodeStrict(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != validRequestV1() {
		t.Fatalf("decoded=%#v", decoded)
	}
}

func TestRequestV1EncodeRejectsNonCanonicalIdentityAndPath(t *testing.T) {
	tests := map[string]func(*RequestV1){
		"id": func(request *RequestV1) {
			request.WorkspaceID = foundation.ID(strings.ToUpper(string(request.WorkspaceID)))
		},
		"path": func(request *RequestV1) { request.TargetPath = "notes/../a.md" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := validRequestV1()
			mutate(&request)
			if _, err := EncodeCanonical(request); err == nil {
				t.Fatal("noncanonical request accepted")
			} else {
				assertContractError(t, err)
			}
		})
	}
}

func TestValidateBindingRejectsEveryCrossBinding(t *testing.T) {
	request := validRequestV1()
	binding := bindingFromRequest(request)
	if err := ValidateBinding(request, binding); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*Binding){
		"workspace": func(value *Binding) { value.WorkspaceID = testRunID },
		"run":       func(value *Binding) { value.WorkflowRunID = testNodeID },
		"node":      func(value *Binding) { value.NodeRunID = testProposalID },
		"proposal":  func(value *Binding) { value.ProposalID = testRevisionID },
		"revision":  func(value *Binding) { value.RevisionID = testApprovalID },
		"approval":  func(value *Binding) { value.ApprovalID = testExecutionID },
		"execution": func(value *Binding) { value.WritebackExecutionID = testWorkspaceID },
		"path":      func(value *Binding) { value.TargetPath = "notes/b.md" },
		"hash":      func(value *Binding) { value.ResultHash = strings.Repeat("b", 64) },
		"commit":    func(value *Binding) { value.GitCommit = strings.Repeat("e", 40) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := binding
			mutate(&changed)
			if err := ValidateBinding(request, changed); err == nil {
				t.Fatal("cross-binding accepted")
			} else {
				assertContractError(t, err)
			}
		})
	}
}

func validRequestV1() RequestV1 {
	return RequestV1{
		SchemaVersion: SchemaVersionV1, WorkspaceID: testWorkspaceID, WorkflowRunID: testRunID,
		NodeRunID: testNodeID, ProposalID: testProposalID, RevisionID: testRevisionID,
		ApprovalID: testApprovalID, WritebackExecutionID: testExecutionID, TargetPath: testTargetPath,
		ResultHash: testResultHash, GitCommit: testGitCommit,
	}
}

func bindingFromRequest(request RequestV1) Binding {
	return Binding{
		WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID, NodeRunID: request.NodeRunID,
		ProposalID: request.ProposalID, RevisionID: request.RevisionID, ApprovalID: request.ApprovalID,
		WritebackExecutionID: request.WritebackExecutionID, TargetPath: request.TargetPath,
		ResultHash: request.ResultHash, GitCommit: request.GitCommit,
	}
}

func assertContractError(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != ErrorCodeInvalidContract {
		t.Fatalf("error=%#v", err)
	}
}
