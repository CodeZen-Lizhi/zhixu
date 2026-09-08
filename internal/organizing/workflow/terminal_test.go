package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestTerminalHookBindsFinalReceiptAtWorkflowTerminalTime(t *testing.T) {
	writer := &terminalResultWriterFake{}
	hook, err := NewScopedTerminalHook(writer)
	if err != nil {
		t.Fatal(err)
	}
	receipt := finalReceipt{
		SchemaVersion: receiptSchemaV1,
		ResultID:      terminalTestID(5),
		RunBindingID:  terminalTestID(4),
		SnapshotID:    terminalTestID(3),
		Kind:          organizingdomain.ResultArtifact,
		ResultRef:     terminalTestID(6),
		ResultHash:    terminalTestHash("a"),
	}
	output, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	terminalAt := time.Date(2026, 8, 3, 12, 34, 56, 123456789, time.FixedZone("test", 8*60*60))
	event := workflowapp.WorkflowNodeTerminalEvent{
		WorkspaceID: terminalTestID(1), WorkflowRunID: terminalTestID(2), NodeRunID: terminalTestID(7),
		NodeKind: KnowledgeReportNodeKind, NodeAttemptID: terminalTestID(8),
		Outcome: workflowapp.WorkflowTerminalOutcomeSucceeded, TerminalOutput: string(output), TerminalAt: terminalAt,
	}
	transaction := writer
	if err := hook.OnWorkflowNodeTerminalScoped(context.Background(), transaction, event); err != nil {
		t.Fatal(err)
	}
	if writer.calls != 1 || writer.transaction != transaction {
		t.Fatalf("writer calls=%d transaction=%p", writer.calls, writer.transaction)
	}
	got := writer.request
	if got.DefinitionKey != KnowledgeReportDefinitionKey || got.DefinitionVersion != DefinitionVersion || got.Result.ID != receipt.ResultID ||
		got.Result.WorkspaceID != event.WorkspaceID || got.Result.RunBindingID != receipt.RunBindingID ||
		got.Result.SnapshotID != receipt.SnapshotID || got.Result.WorkflowRunID != event.WorkflowRunID ||
		got.Result.NodeRunID != event.NodeRunID || got.Result.Kind != receipt.Kind ||
		got.Result.ResultRef != receipt.ResultRef || got.Result.ResultHash != receipt.ResultHash ||
		!got.Result.CreatedAt.Equal(terminalAt.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("terminal result=%+v", got)
	}
}

func TestTerminalHookIgnoresNonFinalAndUnsuccessfulNodes(t *testing.T) {
	tests := []workflowapp.WorkflowNodeTerminalEvent{
		{NodeKind: TopicOutlineNodeKind, Outcome: workflowapp.WorkflowTerminalOutcomeSucceeded},
		{NodeKind: TopicArtifactNodeKind, Outcome: workflowapp.WorkflowTerminalOutcomeFailed},
		{NodeKind: MergeProposalNodeKind, Outcome: workflowapp.WorkflowTerminalOutcomeCancelled},
		{NodeKind: "artifact.section.generate", Outcome: workflowapp.WorkflowTerminalOutcomeSucceeded},
	}
	for _, event := range tests {
		writer := &terminalResultWriterFake{}
		hook, err := NewScopedTerminalHook(writer)
		if err != nil {
			t.Fatal(err)
		}
		if err := hook.OnWorkflowNodeTerminalScoped(context.Background(), nil, event); err != nil {
			t.Fatalf("event=%+v error=%v", event, err)
		}
		if writer.calls != 0 {
			t.Fatalf("event=%+v writer calls=%d", event, writer.calls)
		}
	}
}

func TestTerminalHookRejectsMismatchedOrMalformedReceipt(t *testing.T) {
	base := finalReceipt{
		SchemaVersion: receiptSchemaV1, ResultID: terminalTestID(5), RunBindingID: terminalTestID(4),
		SnapshotID: terminalTestID(3), Kind: organizingdomain.ResultArtifact,
		ResultRef: terminalTestID(6), ResultHash: terminalTestHash("b"),
	}
	tests := []struct {
		name     string
		nodeKind string
		output   string
	}{
		{name: "malformed", nodeKind: TopicArtifactNodeKind, output: `{"schema_version":1`},
		{name: "wrong kind", nodeKind: MergeProposalNodeKind, output: terminalReceiptJSON(t, base)},
		{name: "unknown field", nodeKind: TopicArtifactNodeKind, output: `{"schema_version":1,"result_id":"00000000-0000-4000-8000-000000000005","run_binding_id":"00000000-0000-4000-8000-000000000004","snapshot_id":"00000000-0000-4000-8000-000000000003","kind":"ARTIFACT","result_ref":"00000000-0000-4000-8000-000000000006","result_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","extra":true}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &terminalResultWriterFake{}
			hook, err := NewScopedTerminalHook(writer)
			if err != nil {
				t.Fatal(err)
			}
			err = hook.OnWorkflowNodeTerminalScoped(context.Background(), writer, workflowapp.WorkflowNodeTerminalEvent{
				WorkspaceID: terminalTestID(1), WorkflowRunID: terminalTestID(2), NodeRunID: terminalTestID(7),
				NodeKind: test.nodeKind, NodeAttemptID: terminalTestID(8), Outcome: workflowapp.WorkflowTerminalOutcomeSucceeded,
				TerminalOutput: test.output, TerminalAt: time.Date(2026, 8, 3, 5, 0, 0, 0, time.UTC),
			})
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != organizingapp.ErrorCodeResultInvalid {
				t.Fatalf("error=%v", err)
			}
			if writer.calls != 0 {
				t.Fatalf("writer calls=%d", writer.calls)
			}
		})
	}
}

func TestTerminalHookPropagatesTransactionalWriteFailure(t *testing.T) {
	cause := foundation.NewError(foundation.ErrorRetryableFailure, organizingapp.ErrorCodeRepositoryUnavailable, true, errors.New("serialization failure"))
	writer := &terminalResultWriterFake{err: cause}
	hook, err := NewScopedTerminalHook(writer)
	if err != nil {
		t.Fatal(err)
	}
	receipt := finalReceipt{
		SchemaVersion: receiptSchemaV1, ResultID: terminalTestID(5), RunBindingID: terminalTestID(4),
		SnapshotID: terminalTestID(3), Kind: organizingdomain.ResultMergeProposal,
		ResultRef: terminalTestID(6), ResultHash: terminalTestHash("c"),
	}
	err = hook.OnWorkflowNodeTerminalScoped(context.Background(), writer, workflowapp.WorkflowNodeTerminalEvent{
		WorkspaceID: terminalTestID(1), WorkflowRunID: terminalTestID(2), NodeRunID: terminalTestID(7),
		NodeKind: MergeProposalNodeKind, NodeAttemptID: terminalTestID(8), Outcome: workflowapp.WorkflowTerminalOutcomeSucceeded,
		TerminalOutput: terminalReceiptJSON(t, receipt), TerminalAt: time.Date(2026, 8, 3, 5, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, cause) || writer.calls != 1 {
		t.Fatalf("error=%v calls=%d", err, writer.calls)
	}
}

type terminalResultWriterFake struct {
	calls       int
	transaction foundation.TransactionScope
	request     organizingapp.TerminalResultRequest
	err         error
}

func (writer *terminalResultWriterFake) TransactionScope() {}

func (writer *terminalResultWriterFake) BindSucceededResultScoped(_ context.Context, transaction foundation.TransactionScope, request organizingapp.TerminalResultRequest) error {
	writer.calls++
	writer.transaction = transaction
	writer.request = request
	return writer.err
}

func terminalReceiptJSON(t *testing.T, receipt finalReceipt) string {
	t.Helper()
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func terminalTestID(value int) foundation.ID {
	return foundation.ID("00000000-0000-4000-8000-00000000000" + string(rune('0'+value)))
}

func terminalTestHash(character string) string {
	result := ""
	for len(result) < 64 {
		result += character
	}
	return result
}
