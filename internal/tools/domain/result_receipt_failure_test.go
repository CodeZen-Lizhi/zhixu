package domain

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestNewResultReceiptFailureFreezesAllFailureShapes(t *testing.T) {
	definition := validResultReceiptDefinition(t, ToolRef{Name: "ReadGitStatus", Version: 2})
	output, _ := validResultReceiptDocuments(t, definition.Ref)
	call := validResultReceiptCall(t, definition, output)
	outputBytes := call.ResponseBytes
	zeroBytes := int64(0)
	bindingBytes := int64(91)

	tests := []struct {
		name  string
		code  ResultReceiptFailureCode
		hash  string
		bytes *int64
		bind  string
		bsize *int64
	}{
		{name: "missing", code: ResultReceiptFailureMissing},
		{name: "hash mismatch", code: ResultReceiptFailureHashMismatch, hash: strings.Repeat("a", 64), bytes: &zeroBytes},
		{name: "contract invalid", code: ResultReceiptFailureContractInvalid, hash: call.ResponseHash, bytes: &outputBytes},
		{name: "binding invalid", code: ResultReceiptFailureBindingInvalid, hash: call.ResponseHash, bytes: &outputBytes, bind: strings.Repeat("b", 64), bsize: &bindingBytes},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure, err := NewResultReceiptFailure(ResultReceiptFailureDraft{
				ID: resultReceiptFailureTestID(6), OperationID: resultReceiptFailureTestID(7),
				AnalysisRunID: resultReceiptFailureTestID(8), FailureCode: test.code,
				ObservedOutputHash: test.hash, ObservedOutputBytes: test.bytes,
				ObservedBindingHash: test.bind, ObservedBindingBytes: test.bsize,
				CheckedAt: resultReceiptTestTime().Add(time.Nanosecond),
				CreatedAt: resultReceiptTestTime().Add(2 * time.Nanosecond),
			}, call, definition)
			if err != nil {
				t.Fatalf("NewResultReceiptFailure: %v", err)
			}
			if failure.ExpectedOutputHash != call.ResponseHash ||
				failure.ValidatorVersion != ResultReceiptFailureValidatorVersion ||
				failure.CheckedAt != resultReceiptTestTime() || failure.CreatedAt != resultReceiptTestTime() {
				t.Fatalf("derived failure authority = %+v", failure)
			}
			if err := ValidateResultReceiptFailure(failure, call, definition); err != nil {
				t.Fatalf("ValidateResultReceiptFailure: %v", err)
			}
			if test.bytes != nil {
				original := *test.bytes
				*test.bytes++
				if *failure.ObservedOutputBytes == *test.bytes {
					t.Fatal("failure aliases caller-owned output byte observation")
				}
				*test.bytes = original
			}
			if test.bsize != nil {
				original := *test.bsize
				*test.bsize++
				if *failure.ObservedBindingBytes == *test.bsize {
					t.Fatal("failure aliases caller-owned binding byte observation")
				}
				*test.bsize = original
			}
		})
	}
}

func TestNewResultReceiptFailureRejectsInvalidObservations(t *testing.T) {
	definition, call := validResultReceiptFailureAuthority(t)
	responseBytes := call.ResponseBytes
	otherBytes := responseBytes + 1
	zeroBytes := int64(0)
	oversizedOutput := ResultReceiptFailureMaxObservedOutputBytes + 1
	oversizedBinding := ResultReceiptFailureMaxObservedBindingBytes + 1
	validOtherHash := strings.Repeat("a", 64)

	tests := []struct {
		name   string
		mutate func(*ResultReceiptFailureDraft)
	}{
		{name: "unknown code", mutate: func(value *ResultReceiptFailureDraft) { value.FailureCode = "OTHER" }},
		{name: "missing with output", mutate: func(value *ResultReceiptFailureDraft) {
			value.ObservedOutputHash, value.ObservedOutputBytes = validOtherHash, &zeroBytes
		}},
		{name: "hash mismatch missing hash", mutate: func(value *ResultReceiptFailureDraft) {
			value.FailureCode, value.ObservedOutputBytes = ResultReceiptFailureHashMismatch, &zeroBytes
		}},
		{name: "hash mismatch same hash", mutate: func(value *ResultReceiptFailureDraft) {
			value.FailureCode, value.ObservedOutputHash, value.ObservedOutputBytes = ResultReceiptFailureHashMismatch, call.ResponseHash, &zeroBytes
		}},
		{name: "hash mismatch oversized output", mutate: func(value *ResultReceiptFailureDraft) {
			value.FailureCode, value.ObservedOutputHash, value.ObservedOutputBytes = ResultReceiptFailureHashMismatch, validOtherHash, &oversizedOutput
		}},
		{name: "contract wrong bytes", mutate: func(value *ResultReceiptFailureDraft) {
			value.FailureCode, value.ObservedOutputHash, value.ObservedOutputBytes = ResultReceiptFailureContractInvalid, call.ResponseHash, &otherBytes
		}},
		{name: "contract with binding", mutate: func(value *ResultReceiptFailureDraft) {
			value.FailureCode, value.ObservedOutputHash, value.ObservedOutputBytes = ResultReceiptFailureContractInvalid, call.ResponseHash, &responseBytes
			value.ObservedBindingHash, value.ObservedBindingBytes = strings.Repeat("b", 64), &zeroBytes
		}},
		{name: "binding missing hash", mutate: func(value *ResultReceiptFailureDraft) {
			value.FailureCode, value.ObservedOutputHash, value.ObservedOutputBytes = ResultReceiptFailureBindingInvalid, call.ResponseHash, &responseBytes
			value.ObservedBindingBytes = &zeroBytes
		}},
		{name: "binding oversized", mutate: func(value *ResultReceiptFailureDraft) {
			value.FailureCode, value.ObservedOutputHash, value.ObservedOutputBytes = ResultReceiptFailureBindingInvalid, call.ResponseHash, &responseBytes
			value.ObservedBindingHash, value.ObservedBindingBytes = strings.Repeat("b", 64), &oversizedBinding
		}},
		{name: "checked before call", mutate: func(value *ResultReceiptFailureDraft) {
			value.CheckedAt = resultReceiptTestTime().Add(-time.Microsecond)
		}},
		{name: "created before checked", mutate: func(value *ResultReceiptFailureDraft) {
			value.CreatedAt = resultReceiptTestTime().Add(-time.Microsecond)
		}},
		{name: "duplicate identity", mutate: func(value *ResultReceiptFailureDraft) { value.OperationID = call.ID }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := validResultReceiptFailureDraft(call)
			test.mutate(&draft)
			_, err := NewResultReceiptFailure(draft, call, definition)
			assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptFailureInvalid)
		})
	}
}

func TestResultReceiptFailureAuthorityAllowsRejectedOutputAboveToolCap(t *testing.T) {
	definition, call := validResultReceiptFailureAuthority(t)
	observedBytes := definition.MaxOutputBytes + 1
	call.ResponseHash = strings.Repeat("c", 64)
	call.ResponseBytes = observedBytes

	draft := validResultReceiptFailureDraft(call)
	failure, err := NewResultReceiptFailure(draft, call, definition)
	if err != nil {
		t.Fatalf("NewResultReceiptFailure above tool cap: %v", err)
	}
	if failure.ObservedOutputBytes == nil || *failure.ObservedOutputBytes != observedBytes {
		t.Fatalf("observed output bytes = %v, want %d", failure.ObservedOutputBytes, observedBytes)
	}
	if err := ValidateResultReceiptFailure(failure, call, definition); err != nil {
		t.Fatalf("ValidateResultReceiptFailure above tool cap: %v", err)
	}

	output, binding := validResultReceiptDocuments(t, definition.Ref)
	contract, _ := WorkspaceAnalysisResultReceiptContract(definition.Ref)
	_, err = NewResultReceipt(ResultReceiptDraft{
		ID:                   resultReceiptTestID(9),
		Output:               output,
		PrivateBindingSchema: contract.PrivateBindingSchema,
		PrivateBinding:       binding,
		CreatedAt:            resultReceiptTestTime(),
	}, call, definition)
	assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptBindingConflict)
}

func TestResultReceiptFailureAuthorityRejectsObservationAboveFailureCap(t *testing.T) {
	definition, call := validResultReceiptFailureAuthority(t)
	call.ResponseHash = strings.Repeat("c", 64)
	call.ResponseBytes = ResultReceiptFailureMaxObservedOutputBytes + 1
	draft := validResultReceiptFailureDraft(call)

	_, err := NewResultReceiptFailure(draft, call, definition)
	assertResultReceiptErrorCode(t, err, ErrorCodeResultReceiptFailureBindingConflict)
}

func TestValidateResultReceiptFailureRejectsAuthorityAndReplayDrift(t *testing.T) {
	definition, call := validResultReceiptFailureAuthority(t)
	failure, err := NewResultReceiptFailure(validResultReceiptFailureDraft(call), call, definition)
	if err != nil {
		t.Fatalf("NewResultReceiptFailure: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ResultReceiptFailure)
	}{
		{name: "tool call", mutate: func(value *ResultReceiptFailure) { value.ToolCallID = resultReceiptFailureTestID(9) }},
		{name: "workspace", mutate: func(value *ResultReceiptFailure) { value.WorkspaceID = resultReceiptFailureTestID(9) }},
		{name: "workflow", mutate: func(value *ResultReceiptFailure) { value.WorkflowRunID = resultReceiptFailureTestID(9) }},
		{name: "node run", mutate: func(value *ResultReceiptFailure) { value.NodeRunID = resultReceiptFailureTestID(9) }},
		{name: "attempt", mutate: func(value *ResultReceiptFailure) { value.NodeAttemptID = resultReceiptFailureTestID(9) }},
		{name: "expected hash", mutate: func(value *ResultReceiptFailure) { value.ExpectedOutputHash = strings.Repeat("c", 64) }},
		{name: "validator", mutate: func(value *ResultReceiptFailure) { value.ValidatorVersion++ }},
		{name: "failure code", mutate: func(value *ResultReceiptFailure) { value.FailureCode = ResultReceiptFailureMissing }},
		{name: "observation hash", mutate: func(value *ResultReceiptFailure) { value.ObservedOutputHash = strings.Repeat("d", 64) }},
		{name: "observation bytes", mutate: func(value *ResultReceiptFailure) { *value.ObservedOutputBytes++ }},
		{name: "checked time", mutate: func(value *ResultReceiptFailure) { value.CheckedAt = call.StartedAt }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drifted := cloneResultReceiptFailure(failure)
			test.mutate(&drifted)
			assertResultReceiptErrorCode(
				t,
				ValidateResultReceiptFailure(drifted, call, definition),
				ErrorCodeResultReceiptFailureBindingConflict,
			)
		})
	}

	driftedCall := call
	driftedCall.ResponseHash = strings.Repeat("e", 64)
	assertResultReceiptErrorCode(
		t,
		ValidateResultReceiptFailure(failure, driftedCall, definition),
		ErrorCodeResultReceiptFailureBindingConflict,
	)
}

func TestResultReceiptFailureStringDoesNotExposeRejectedDocuments(t *testing.T) {
	definition, call := validResultReceiptFailureAuthority(t)
	failure, err := NewResultReceiptFailure(validResultReceiptFailureDraft(call), call, definition)
	if err != nil {
		t.Fatalf("NewResultReceiptFailure: %v", err)
	}
	for _, rendered := range []string{failure.String(), fmt.Sprintf("%#v", failure)} {
		if strings.Contains(rendered, "document") || strings.Contains(rendered, "private body") {
			t.Fatalf("safe projection leaked rejected content: %s", rendered)
		}
		if !strings.Contains(rendered, string(failure.FailureCode)) || !strings.Contains(rendered, failure.ExpectedOutputHash) {
			t.Fatalf("safe projection omitted audit identity: %s", rendered)
		}
	}
}

func validResultReceiptFailureAuthority(t *testing.T) (Definition, ToolCall) {
	t.Helper()
	definition := validResultReceiptDefinition(t, ToolRef{Name: "ReadGitStatus", Version: 2})
	output, _ := validResultReceiptDocuments(t, definition.Ref)
	return definition, validResultReceiptCall(t, definition, output)
}

func validResultReceiptFailureDraft(call ToolCall) ResultReceiptFailureDraft {
	outputBytes := call.ResponseBytes
	return ResultReceiptFailureDraft{
		ID: resultReceiptFailureTestID(6), OperationID: resultReceiptFailureTestID(7),
		AnalysisRunID: resultReceiptFailureTestID(8), FailureCode: ResultReceiptFailureContractInvalid,
		ObservedOutputHash: call.ResponseHash, ObservedOutputBytes: &outputBytes,
		CheckedAt: resultReceiptTestTime(), CreatedAt: resultReceiptTestTime(),
	}
}

func cloneResultReceiptFailure(failure ResultReceiptFailure) ResultReceiptFailure {
	cloned := failure
	cloned.ObservedOutputBytes = cloneResultReceiptFailureBytes(failure.ObservedOutputBytes)
	cloned.ObservedBindingBytes = cloneResultReceiptFailureBytes(failure.ObservedBindingBytes)
	return cloned
}

func resultReceiptFailureTestID(suffix int) foundation.ID {
	return foundation.ID(fmt.Sprintf("8b000000-0000-4000-8000-%012d", suffix))
}
