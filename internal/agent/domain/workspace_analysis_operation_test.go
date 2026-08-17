package domain

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisV1OperationCatalogGolden(t *testing.T) {
	contracts := WorkspaceAnalysisV1OperationContracts()
	encoded, err := json.Marshal(contracts)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"node_key":"inspect_workspace","operation_kind":"GIT_STATUS","ordinal":1,"call_kind":"TOOL","result_kind":"TOOL_RESULT_RECEIPT"},{"node_key":"retrieve_evidence","operation_kind":"RETRIEVAL_PLAN","ordinal":1,"call_kind":"MODEL","result_kind":"MODEL_RESULT_RECEIPT"},{"node_key":"retrieve_evidence","operation_kind":"KNOWLEDGE_SEARCH","ordinal":1,"call_kind":"TOOL","result_kind":"TOOL_RESULT_RECEIPT"},{"node_key":"read_evidence","operation_kind":"SOURCE_READ","ordinal":1,"call_kind":"TOOL","result_kind":"TOOL_RESULT_RECEIPT"},{"node_key":"read_evidence","operation_kind":"SOURCE_READ","ordinal":2,"call_kind":"TOOL","result_kind":"TOOL_RESULT_RECEIPT"},{"node_key":"read_evidence","operation_kind":"SOURCE_READ","ordinal":3,"call_kind":"TOOL","result_kind":"TOOL_RESULT_RECEIPT"},{"node_key":"synthesize_answer","operation_kind":"ANSWER_SYNTHESIS","ordinal":1,"call_kind":"MODEL","result_kind":"SYNTHESIS_CANDIDATE"},{"node_key":"validate_citations","operation_kind":"CITATION_VALIDATION","ordinal":1,"call_kind":"TOOL","result_kind":"TOOL_RESULT_RECEIPT"},{"node_key":"review_publish","operation_kind":"FAITHFULNESS_REVIEW","ordinal":1,"call_kind":"MODEL","result_kind":"MODEL_RESULT_RECEIPT"}]`
	if string(encoded) != want {
		t.Fatalf("workspace-analysis@1 operation catalog drifted\n got: %s\nwant: %s", encoded, want)
	}

	nodes := make(map[WorkspaceAnalysisOperationNodeKey]struct{})
	modelCalls := 0
	toolCalls := 0
	sourceReads := 0
	for _, contract := range contracts {
		nodes[contract.NodeKey] = struct{}{}
		switch contract.CallKind {
		case WorkspaceAnalysisOperationCallModel:
			modelCalls++
		case WorkspaceAnalysisOperationCallTool:
			toolCalls++
		default:
			t.Fatalf("unsupported call kind in frozen catalog: %#v", contract)
		}
		if contract.Kind == WorkspaceAnalysisOperationSourceRead {
			sourceReads++
		}
	}
	if len(nodes) != WorkspaceAnalysisV1MaxNodes || modelCalls != WorkspaceAnalysisV1MaxModelCalls ||
		toolCalls != WorkspaceAnalysisV1MaxToolCalls || sourceReads != WorkspaceAnalysisV1MaxSourceReads {
		t.Fatalf("catalog/policy mismatch: nodes=%d model=%d tool=%d source=%d", len(nodes), modelCalls, toolCalls, sourceReads)
	}

	// 返回副本，避免调用方改变全局冻结目录。
	contracts[0].Kind = WorkspaceAnalysisOperationKind("MUTATED")
	if reflect.DeepEqual(contracts, WorkspaceAnalysisV1OperationContracts()) ||
		WorkspaceAnalysisV1OperationContracts()[0].Kind != WorkspaceAnalysisOperationGitStatus {
		t.Fatal("operation catalog leaked mutable backing storage")
	}
}

func TestWorkspaceAnalysisV1OperationKeysAreAnExactClosedMatrix(t *testing.T) {
	contracts := WorkspaceAnalysisV1OperationContracts()
	allowed := make(map[string]WorkspaceAnalysisOperationContract, len(contracts))
	for _, contract := range contracts {
		identity := workspaceAnalysisOperationContractIdentity(contract.NodeKey, contract.Kind, contract.Ordinal)
		if _, duplicate := allowed[identity]; duplicate {
			t.Fatalf("duplicate operation contract: %s", identity)
		}
		allowed[identity] = contract
	}

	nodes := []WorkspaceAnalysisOperationNodeKey{
		WorkspaceAnalysisOperationNodeInspectWorkspace,
		WorkspaceAnalysisOperationNodeRetrieveEvidence,
		WorkspaceAnalysisOperationNodeReadEvidence,
		WorkspaceAnalysisOperationNodeSynthesizeAnswer,
		WorkspaceAnalysisOperationNodeValidateCitations,
		WorkspaceAnalysisOperationNodeReviewPublish,
		"unknown_node",
	}
	kinds := []WorkspaceAnalysisOperationKind{
		WorkspaceAnalysisOperationGitStatus,
		WorkspaceAnalysisOperationRetrievalPlan,
		WorkspaceAnalysisOperationKnowledgeSearch,
		WorkspaceAnalysisOperationSourceRead,
		WorkspaceAnalysisOperationAnswerSynthesis,
		WorkspaceAnalysisOperationCitationValidation,
		WorkspaceAnalysisOperationFaithfulnessReview,
		"UNKNOWN_KIND",
	}
	for _, node := range nodes {
		for _, kind := range kinds {
			for ordinal := -1; ordinal <= WorkspaceAnalysisV1MaxSourceReads+1; ordinal++ {
				key := WorkspaceAnalysisOperationKey{
					AnalysisRunID: workspaceAnalysisOperationTestID(1),
					NodeKey:       node,
					Kind:          kind,
					Ordinal:       ordinal,
				}
				identity := workspaceAnalysisOperationContractIdentity(node, kind, ordinal)
				want, exists := allowed[identity]
				err := key.Validate()
				if exists && err != nil {
					t.Fatalf("valid key %s rejected: %v", identity, err)
				}
				if !exists && errorCode(err) != ErrorCodeWorkspaceAnalysisOperationInvalid {
					t.Fatalf("invalid key %s accepted or wrong error: %v", identity, err)
				}
				if exists {
					got, lookupErr := WorkspaceAnalysisOperationContractForKey(key)
					if lookupErr != nil || got != want {
						t.Fatalf("lookup %s got=%#v err=%v want=%#v", identity, got, lookupErr, want)
					}
				}
			}
		}
	}

	invalidRun := WorkspaceAnalysisOperationKey{
		AnalysisRunID: "8A000000-0000-4000-8000-000000000001",
		NodeKey:       WorkspaceAnalysisOperationNodeInspectWorkspace,
		Kind:          WorkspaceAnalysisOperationGitStatus,
		Ordinal:       1,
	}
	if err := invalidRun.Validate(); errorCode(err) != ErrorCodeWorkspaceAnalysisOperationInvalid {
		t.Fatalf("non-canonical run id accepted: %v", err)
	}
}

func TestWorkspaceAnalysisOperationLogicalKeyIsStableAcrossAttempts(t *testing.T) {
	operation := validStartedWorkspaceAnalysisOperation(WorkspaceAnalysisV1OperationContracts()[0], 0)
	want := operation.LogicalKey()
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{"analysis_run_id":"8a000000-0000-4000-8000-000000000001","node_key":"inspect_workspace","operation_kind":"GIT_STATUS","ordinal":1}`
	if string(encoded) != golden {
		t.Fatalf("logical key drifted: got=%s want=%s", encoded, golden)
	}

	latestAttemptID := workspaceAnalysisOperationTestID(999)
	operation.LatestNodeAttemptID = &latestAttemptID
	operation.Version++
	operation.UpdatedAt = operation.UpdatedAt.Add(time.Second)
	if err := ValidateWorkspaceAnalysisOperation(operation); err != nil {
		t.Fatalf("replacement attempt binding rejected: %v", err)
	}
	if got := operation.LogicalKey(); got != want {
		t.Fatalf("attempt changed logical key: got=%#v want=%#v", got, want)
	}
}

func TestWorkspaceAnalysisOperationRefsCannotCarryRawOutputOrFreeLocator(t *testing.T) {
	tests := []struct {
		name string
		got  reflect.Type
		want []string
	}{
		{name: "call ref", got: reflect.TypeOf(WorkspaceAnalysisOperationCallRef{}), want: []string{"Kind", "ID"}},
		{name: "result ref", got: reflect.TypeOf(WorkspaceAnalysisOperationResultRef{}), want: []string{"Kind", "ID", "Hash"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := make([]string, 0, test.got.NumField())
			for index := 0; index < test.got.NumField(); index++ {
				got = append(got, test.got.Field(index).Name)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("reference shape can carry an unfrozen field: got=%v want=%v", got, test.want)
			}
		})
	}
}

func TestWorkspaceAnalysisOperationTransitionsAreExhaustive(t *testing.T) {
	statuses := []WorkspaceAnalysisOperationStatus{
		WorkspaceAnalysisOperationPending,
		WorkspaceAnalysisOperationStarted,
		WorkspaceAnalysisOperationSucceeded,
		WorkspaceAnalysisOperationFailed,
		WorkspaceAnalysisOperationUnknown,
		"UNSUPPORTED",
	}
	allowed := map[[2]WorkspaceAnalysisOperationStatus]struct{}{
		{WorkspaceAnalysisOperationPending, WorkspaceAnalysisOperationStarted}:   {},
		{WorkspaceAnalysisOperationStarted, WorkspaceAnalysisOperationSucceeded}: {},
		{WorkspaceAnalysisOperationStarted, WorkspaceAnalysisOperationFailed}:    {},
		{WorkspaceAnalysisOperationStarted, WorkspaceAnalysisOperationUnknown}:   {},
	}
	for _, from := range statuses {
		for _, to := range statuses {
			_, valid := allowed[[2]WorkspaceAnalysisOperationStatus{from, to}]
			err := ValidateWorkspaceAnalysisOperationTransition(from, to)
			if valid && err != nil {
				t.Fatalf("valid transition %s -> %s rejected: %v", from, to, err)
			}
			if !valid && errorCode(err) != ErrorCodeWorkspaceAnalysisOperationTransitionInvalid {
				t.Fatalf("invalid transition %s -> %s accepted or wrong error: %v", from, to, err)
			}
		}
	}
}

func TestWorkspaceAnalysisOperationLifecycleAcceptsEveryFrozenSlot(t *testing.T) {
	for index, contract := range WorkspaceAnalysisV1OperationContracts() {
		t.Run(workspaceAnalysisOperationContractIdentity(contract.NodeKey, contract.Kind, contract.Ordinal), func(t *testing.T) {
			pending := validPendingWorkspaceAnalysisOperation(contract, index)
			if err := ValidateWorkspaceAnalysisOperation(pending); err != nil {
				t.Fatalf("pending rejected: %v", err)
			}

			started := validStartedWorkspaceAnalysisOperation(contract, index)
			if err := ValidateWorkspaceAnalysisOperation(started); err != nil {
				t.Fatalf("started rejected: %v", err)
			}

			succeeded := terminalWorkspaceAnalysisOperation(contract, index, WorkspaceAnalysisOperationSucceeded)
			if err := ValidateWorkspaceAnalysisOperation(succeeded); err != nil {
				t.Fatalf("succeeded rejected: %v", err)
			}
			if succeeded.Result == nil || succeeded.Result.Kind != contract.ResultKind {
				t.Fatalf("wrong successful result mapping: operation=%#v contract=%#v", succeeded.Result, contract)
			}

			for _, status := range []WorkspaceAnalysisOperationStatus{WorkspaceAnalysisOperationFailed, WorkspaceAnalysisOperationUnknown} {
				terminal := terminalWorkspaceAnalysisOperation(contract, index, status)
				if err := ValidateWorkspaceAnalysisOperation(terminal); err != nil {
					t.Fatalf("%s rejected: %v", status, err)
				}
				if terminal.Result != nil {
					t.Fatalf("%s declared a result: %#v", status, terminal.Result)
				}
			}
		})
	}
}

func TestWorkspaceAnalysisOperationBindsActualCanonicalCallRequestHash(t *testing.T) {
	for index, contract := range WorkspaceAnalysisV1OperationContracts() {
		operation := validStartedWorkspaceAnalysisOperation(contract, index)
		binding := canonicalCallBinding(operation)
		if err := ValidateWorkspaceAnalysisOperationCallBinding(operation, binding); err != nil {
			t.Fatalf("contract=%#v exact binding rejected: %v", contract, err)
		}
	}

	operation := validStartedWorkspaceAnalysisOperation(WorkspaceAnalysisV1OperationContracts()[0], 0)
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisCanonicalCallBinding)
	}{
		{name: "actual call kind", mutate: func(binding *WorkspaceAnalysisCanonicalCallBinding) {
			binding.Kind = WorkspaceAnalysisOperationCallModel
		}},
		{name: "actual call id", mutate: func(binding *WorkspaceAnalysisCanonicalCallBinding) {
			binding.CallID = workspaceAnalysisOperationTestID(950)
		}},
		{name: "actual request hash", mutate: func(binding *WorkspaceAnalysisCanonicalCallBinding) { binding.RequestHash = strings.Repeat("b", 64) }},
		{name: "non canonical actual hash", mutate: func(binding *WorkspaceAnalysisCanonicalCallBinding) { binding.RequestHash = strings.Repeat("A", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding := canonicalCallBinding(operation)
			test.mutate(&binding)
			if err := ValidateWorkspaceAnalysisOperationCallBinding(operation, binding); errorCode(err) != ErrorCodeWorkspaceAnalysisOperationRequestBindingMismatch {
				t.Fatalf("mismatched canonical call accepted: %v", err)
			}
		})
	}
}

func TestWorkspaceAnalysisOperationBindsTypedCanonicalResult(t *testing.T) {
	for index, contract := range WorkspaceAnalysisV1OperationContracts() {
		operation := terminalWorkspaceAnalysisOperation(contract, index, WorkspaceAnalysisOperationSucceeded)
		binding := WorkspaceAnalysisCanonicalResultBinding{
			Kind:     operation.Result.Kind,
			ResultID: operation.Result.ID,
			Hash:     operation.Result.Hash,
		}
		if err := ValidateWorkspaceAnalysisOperationResultBinding(operation, binding); err != nil {
			t.Fatalf("contract=%#v exact result binding rejected: %v", contract, err)
		}
	}

	operation := terminalWorkspaceAnalysisOperation(WorkspaceAnalysisV1OperationContracts()[0], 0, WorkspaceAnalysisOperationSucceeded)
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisCanonicalResultBinding)
	}{
		{name: "result kind", mutate: func(binding *WorkspaceAnalysisCanonicalResultBinding) {
			binding.Kind = WorkspaceAnalysisOperationResultCandidate
		}},
		{name: "result id", mutate: func(binding *WorkspaceAnalysisCanonicalResultBinding) {
			binding.ResultID = workspaceAnalysisOperationTestID(951)
		}},
		{name: "result hash", mutate: func(binding *WorkspaceAnalysisCanonicalResultBinding) { binding.Hash = strings.Repeat("c", 64) }},
		{name: "non canonical hash", mutate: func(binding *WorkspaceAnalysisCanonicalResultBinding) { binding.Hash = strings.Repeat("B", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding := WorkspaceAnalysisCanonicalResultBinding{
				Kind:     operation.Result.Kind,
				ResultID: operation.Result.ID,
				Hash:     operation.Result.Hash,
			}
			test.mutate(&binding)
			if err := ValidateWorkspaceAnalysisOperationResultBinding(operation, binding); errorCode(err) != ErrorCodeWorkspaceAnalysisOperationResultBindingMismatch {
				t.Fatalf("mismatched canonical result accepted: %v", err)
			}
		})
	}

	started := validStartedWorkspaceAnalysisOperation(WorkspaceAnalysisV1OperationContracts()[0], 0)
	if err := ValidateWorkspaceAnalysisOperationResultBinding(started, WorkspaceAnalysisCanonicalResultBinding{}); errorCode(err) != ErrorCodeWorkspaceAnalysisOperationResultBindingMismatch {
		t.Fatalf("non-success operation declared result binding: %v", err)
	}
}

func TestWorkspaceAnalysisOperationReplayIsFailClosed(t *testing.T) {
	tests := []struct {
		status WorkspaceAnalysisOperationStatus
		want   WorkspaceAnalysisOperationReplayDisposition
	}{
		{status: WorkspaceAnalysisOperationPending, want: WorkspaceAnalysisOperationReplayAuthorize},
		{status: WorkspaceAnalysisOperationStarted, want: WorkspaceAnalysisOperationReplayReconcile},
		{status: WorkspaceAnalysisOperationSucceeded, want: WorkspaceAnalysisOperationReplayReuseResult},
		{status: WorkspaceAnalysisOperationFailed, want: WorkspaceAnalysisOperationReplayFailure},
		{status: WorkspaceAnalysisOperationUnknown, want: WorkspaceAnalysisOperationReplayTerminateUnknown},
	}
	contract := WorkspaceAnalysisV1OperationContracts()[0]
	for _, test := range tests {
		t.Run(string(test.status), func(t *testing.T) {
			operation := operationInStatus(contract, 0, test.status)
			got, err := WorkspaceAnalysisOperationReplay(operation, operation.RequestHash)
			if err != nil || got != test.want {
				t.Fatalf("replay status=%s got=%s err=%v want=%s", test.status, got, err, test.want)
			}
		})
	}

	operation := terminalWorkspaceAnalysisOperation(contract, 0, WorkspaceAnalysisOperationSucceeded)
	for _, requestHash := range []string{strings.Repeat("b", 64), strings.Repeat("A", 64), ""} {
		if got, err := WorkspaceAnalysisOperationReplay(operation, requestHash); got != "" || errorCode(err) != ErrorCodeWorkspaceAnalysisOperationRequestBindingMismatch {
			t.Fatalf("hash mismatch did not fail closed: got=%s err=%v", got, err)
		}
	}
}

func TestWorkspaceAnalysisProviderAndToolUnknownFixturesTerminateWithoutReplay(t *testing.T) {
	contracts := WorkspaceAnalysisV1OperationContracts()
	for _, contract := range []WorkspaceAnalysisOperationContract{contracts[0], contracts[1]} {
		operation := terminalWorkspaceAnalysisOperation(contract, int(contract.Ordinal), WorkspaceAnalysisOperationUnknown)
		got, err := WorkspaceAnalysisOperationReplay(operation, operation.RequestHash)
		if err != nil || got != WorkspaceAnalysisOperationReplayTerminateUnknown {
			t.Fatalf("%s Unknown replay=%s err=%v", contract.CallKind, got, err)
		}
	}
}

func TestWorkspaceAnalysisOperationValidationRejectsInconsistentFacts(t *testing.T) {
	toolContract := WorkspaceAnalysisV1OperationContracts()[0]
	modelContract := WorkspaceAnalysisV1OperationContracts()[1]
	pending := validPendingWorkspaceAnalysisOperation(toolContract, 0)
	started := validStartedWorkspaceAnalysisOperation(toolContract, 0)
	succeededTool := terminalWorkspaceAnalysisOperation(toolContract, 0, WorkspaceAnalysisOperationSucceeded)
	succeededModel := terminalWorkspaceAnalysisOperation(modelContract, 1, WorkspaceAnalysisOperationSucceeded)
	failed := terminalWorkspaceAnalysisOperation(toolContract, 0, WorkspaceAnalysisOperationFailed)
	unknown := terminalWorkspaceAnalysisOperation(toolContract, 0, WorkspaceAnalysisOperationUnknown)

	tests := []struct {
		name      string
		operation WorkspaceAnalysisOperation
	}{
		{name: "operation id", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) { value.ID = "invalid" })},
		{name: "operation reuses run id", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) { value.ID = value.AnalysisRunID })},
		{name: "request hash uppercase", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) { value.RequestHash = strings.Repeat("A", 64) })},
		{name: "version", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) { value.Version = 0 })},
		{name: "created at", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) { value.CreatedAt = time.Time{} })},
		{name: "updated before created", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) { value.UpdatedAt = value.CreatedAt.Add(-time.Second) })},
		{name: "unsupported status", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) { value.Status = "UNSUPPORTED" })},
		{name: "pending attempt", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) {
			value.FirstNodeAttemptID = workspaceAnalysisOperationIDPointer(960)
		})},
		{name: "pending reservation", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) {
			value.BudgetReservationID = workspaceAnalysisOperationIDPointer(961)
		})},
		{name: "pending call", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) {
			value.Call = &WorkspaceAnalysisOperationCallRef{Kind: WorkspaceAnalysisOperationCallTool, ID: workspaceAnalysisOperationTestID(962)}
		})},
		{name: "pending result", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) {
			value.Result = &WorkspaceAnalysisOperationResultRef{Kind: WorkspaceAnalysisOperationResultToolReceipt, ID: workspaceAnalysisOperationTestID(963), Hash: strings.Repeat("b", 64)}
		})},
		{name: "pending error", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) { value.ErrorCode = "TEST_FAILURE" })},
		{name: "pending started time", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) {
			value.StartedAt = workspaceAnalysisOperationTimePointer(value.CreatedAt)
		})},
		{name: "pending completed time", operation: mutateWorkspaceAnalysisOperation(pending, func(value *WorkspaceAnalysisOperation) {
			value.CompletedAt = workspaceAnalysisOperationTimePointer(value.CreatedAt)
		})},
		{name: "started missing first attempt", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) { value.FirstNodeAttemptID = nil })},
		{name: "started missing latest attempt", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) { value.LatestNodeAttemptID = nil })},
		{name: "started missing reservation", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) { value.BudgetReservationID = nil })},
		{name: "started missing call", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) { value.Call = nil })},
		{name: "started missing start time", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) { value.StartedAt = nil })},
		{name: "started wrong call kind", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) { value.Call.Kind = WorkspaceAnalysisOperationCallModel })},
		{name: "started duplicate call and reservation", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) { value.Call.ID = *value.BudgetReservationID })},
		{name: "started start before create", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) {
			value.StartedAt = workspaceAnalysisOperationTimePointer(value.CreatedAt.Add(-time.Second))
		})},
		{name: "started start after update", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) {
			value.StartedAt = workspaceAnalysisOperationTimePointer(value.UpdatedAt.Add(time.Second))
		})},
		{name: "started result", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) {
			value.Result = &WorkspaceAnalysisOperationResultRef{Kind: WorkspaceAnalysisOperationResultToolReceipt, ID: workspaceAnalysisOperationTestID(964), Hash: strings.Repeat("b", 64)}
		})},
		{name: "started error", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) { value.ErrorCode = "TEST_FAILURE" })},
		{name: "started completed time", operation: mutateWorkspaceAnalysisOperation(started, func(value *WorkspaceAnalysisOperation) {
			value.CompletedAt = workspaceAnalysisOperationTimePointer(value.UpdatedAt)
		})},
		{name: "succeeded missing result", operation: mutateWorkspaceAnalysisOperation(succeededTool, func(value *WorkspaceAnalysisOperation) { value.Result = nil })},
		{name: "succeeded wrong result kind", operation: mutateWorkspaceAnalysisOperation(succeededTool, func(value *WorkspaceAnalysisOperation) { value.Result.Kind = WorkspaceAnalysisOperationResultCandidate })},
		{name: "succeeded result id invalid", operation: mutateWorkspaceAnalysisOperation(succeededTool, func(value *WorkspaceAnalysisOperation) { value.Result.ID = "free-form-ref" })},
		{name: "succeeded tool result reuses call", operation: mutateWorkspaceAnalysisOperation(succeededTool, func(value *WorkspaceAnalysisOperation) { value.Result.ID = value.Call.ID })},
		{name: "succeeded model result reuses call", operation: mutateWorkspaceAnalysisOperation(succeededModel, func(value *WorkspaceAnalysisOperation) { value.Result.ID = value.Call.ID })},
		{name: "succeeded result hash uppercase", operation: mutateWorkspaceAnalysisOperation(succeededTool, func(value *WorkspaceAnalysisOperation) { value.Result.Hash = strings.Repeat("B", 64) })},
		{name: "succeeded error", operation: mutateWorkspaceAnalysisOperation(succeededTool, func(value *WorkspaceAnalysisOperation) { value.ErrorCode = "TEST_FAILURE" })},
		{name: "succeeded missing completion", operation: mutateWorkspaceAnalysisOperation(succeededTool, func(value *WorkspaceAnalysisOperation) { value.CompletedAt = nil })},
		{name: "succeeded completion before start", operation: mutateWorkspaceAnalysisOperation(succeededTool, func(value *WorkspaceAnalysisOperation) {
			value.CompletedAt = workspaceAnalysisOperationTimePointer(value.StartedAt.Add(-time.Second))
		})},
		{name: "succeeded completion after update", operation: mutateWorkspaceAnalysisOperation(succeededTool, func(value *WorkspaceAnalysisOperation) {
			value.CompletedAt = workspaceAnalysisOperationTimePointer(value.UpdatedAt.Add(time.Second))
		})},
		{name: "failed missing error", operation: mutateWorkspaceAnalysisOperation(failed, func(value *WorkspaceAnalysisOperation) { value.ErrorCode = "" })},
		{name: "failed non canonical error", operation: mutateWorkspaceAnalysisOperation(failed, func(value *WorkspaceAnalysisOperation) { value.ErrorCode = "provider message" })},
		{name: "failed result", operation: mutateWorkspaceAnalysisOperation(failed, func(value *WorkspaceAnalysisOperation) { value.Result = succeededTool.Result })},
		{name: "unknown missing error", operation: mutateWorkspaceAnalysisOperation(unknown, func(value *WorkspaceAnalysisOperation) { value.ErrorCode = "" })},
		{name: "unknown result", operation: mutateWorkspaceAnalysisOperation(unknown, func(value *WorkspaceAnalysisOperation) { value.Result = succeededTool.Result })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateWorkspaceAnalysisOperation(test.operation); errorCode(err) != ErrorCodeWorkspaceAnalysisOperationInvalid {
				t.Fatalf("inconsistent operation accepted or wrong error: %#v err=%v", test.operation, err)
			}
		})
	}
}

func workspaceAnalysisOperationContractIdentity(node WorkspaceAnalysisOperationNodeKey, kind WorkspaceAnalysisOperationKind, ordinal int) string {
	return fmt.Sprintf("%s/%s/%d", node, kind, ordinal)
}

func workspaceAnalysisOperationTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("8a000000-0000-4000-8000-%012d", value))
}

func workspaceAnalysisOperationIDPointer(value int) *foundation.ID {
	id := workspaceAnalysisOperationTestID(value)
	return &id
}

func workspaceAnalysisOperationTimePointer(value time.Time) *time.Time {
	return &value
}

func validPendingWorkspaceAnalysisOperation(contract WorkspaceAnalysisOperationContract, index int) WorkspaceAnalysisOperation {
	createdAt := time.Date(2026, 8, 15, 8, 0, index, 0, time.UTC)
	return WorkspaceAnalysisOperation{
		ID:            workspaceAnalysisOperationTestID(100 + index),
		AnalysisRunID: workspaceAnalysisOperationTestID(1),
		NodeKey:       contract.NodeKey,
		Kind:          contract.Kind,
		Ordinal:       contract.Ordinal,
		RequestHash:   strings.Repeat("a", 64),
		Status:        WorkspaceAnalysisOperationPending,
		Version:       1,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
}

func validStartedWorkspaceAnalysisOperation(contract WorkspaceAnalysisOperationContract, index int) WorkspaceAnalysisOperation {
	operation := validPendingWorkspaceAnalysisOperation(contract, index)
	startedAt := operation.CreatedAt.Add(time.Second)
	operation.Status = WorkspaceAnalysisOperationStarted
	operation.FirstNodeAttemptID = workspaceAnalysisOperationIDPointer(200 + index)
	operation.LatestNodeAttemptID = workspaceAnalysisOperationIDPointer(200 + index)
	operation.BudgetReservationID = workspaceAnalysisOperationIDPointer(300 + index)
	operation.Call = &WorkspaceAnalysisOperationCallRef{
		Kind: contract.CallKind,
		ID:   workspaceAnalysisOperationTestID(400 + index),
	}
	operation.Version = 2
	operation.UpdatedAt = startedAt
	operation.StartedAt = &startedAt
	return operation
}

func terminalWorkspaceAnalysisOperation(contract WorkspaceAnalysisOperationContract, index int, status WorkspaceAnalysisOperationStatus) WorkspaceAnalysisOperation {
	operation := validStartedWorkspaceAnalysisOperation(contract, index)
	completedAt := operation.UpdatedAt.Add(time.Second)
	operation.Status = status
	operation.Version = 3
	operation.UpdatedAt = completedAt
	operation.CompletedAt = &completedAt
	switch status {
	case WorkspaceAnalysisOperationSucceeded:
		resultID := workspaceAnalysisOperationTestID(500 + index)
		operation.Result = &WorkspaceAnalysisOperationResultRef{
			Kind: contract.ResultKind,
			ID:   resultID,
			Hash: strings.Repeat("b", 64),
		}
	case WorkspaceAnalysisOperationFailed:
		operation.ErrorCode = "WORKSPACE_ANALYSIS_TEST_FAILURE"
	case WorkspaceAnalysisOperationUnknown:
		operation.ErrorCode = "WORKSPACE_ANALYSIS_RESULT_UNKNOWN"
	}
	return operation
}

func operationInStatus(contract WorkspaceAnalysisOperationContract, index int, status WorkspaceAnalysisOperationStatus) WorkspaceAnalysisOperation {
	switch status {
	case WorkspaceAnalysisOperationPending:
		return validPendingWorkspaceAnalysisOperation(contract, index)
	case WorkspaceAnalysisOperationStarted:
		return validStartedWorkspaceAnalysisOperation(contract, index)
	default:
		return terminalWorkspaceAnalysisOperation(contract, index, status)
	}
}

func canonicalCallBinding(operation WorkspaceAnalysisOperation) WorkspaceAnalysisCanonicalCallBinding {
	return WorkspaceAnalysisCanonicalCallBinding{
		Kind:        operation.Call.Kind,
		CallID:      operation.Call.ID,
		RequestHash: operation.RequestHash,
	}
}

func mutateWorkspaceAnalysisOperation(operation WorkspaceAnalysisOperation, mutate func(*WorkspaceAnalysisOperation)) WorkspaceAnalysisOperation {
	if operation.FirstNodeAttemptID != nil {
		value := *operation.FirstNodeAttemptID
		operation.FirstNodeAttemptID = &value
	}
	if operation.LatestNodeAttemptID != nil {
		value := *operation.LatestNodeAttemptID
		operation.LatestNodeAttemptID = &value
	}
	if operation.BudgetReservationID != nil {
		value := *operation.BudgetReservationID
		operation.BudgetReservationID = &value
	}
	if operation.Call != nil {
		value := *operation.Call
		operation.Call = &value
	}
	if operation.Result != nil {
		value := *operation.Result
		operation.Result = &value
	}
	if operation.StartedAt != nil {
		value := *operation.StartedAt
		operation.StartedAt = &value
	}
	if operation.CompletedAt != nil {
		value := *operation.CompletedAt
		operation.CompletedAt = &value
	}
	mutate(&operation)
	return operation
}
