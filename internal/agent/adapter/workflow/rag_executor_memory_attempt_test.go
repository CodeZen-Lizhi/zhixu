package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRAGWorkflowExecutorNewAttemptReloadsChangedMemorySnapshot(t *testing.T) {
	executionContext := validRAGExecutionContext(t)
	execution, encoded := ragWorkflowExecution(t, executionContext)
	execution.Input = encoded
	providerErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_PROVIDER_DOWN", true, errors.New("provider down"))
	model := agentapplication.NewDeterministicChatModel(
		agentapplication.DeterministicChatStep{Err: providerErr},
		agentapplication.DeterministicChatStep{Err: providerErr},
	)
	snapshots := &ragSnapshotRepositoryFake{}
	memory := &ragMemoryLoaderFake{items: []agentapplication.EffectiveMemoryItem{{
		ID: ragInputID(8), Version: 1, Type: "PREFERENCE", Category: agentapplication.MemoryCategoryUserPreference,
		Content: json.RawMessage(`{"answer_style":"concise"}`),
	}}}
	finalizer := &ragFinalizerFake{}
	executor := newRAGWorkflowExecutorWithDependencies(t, model, &ragContextLoaderFake{result: executionContext}, finalizer, snapshots, memory,
		[]foundation.ID{
			ragAuxiliaryID(1), ragAuxiliaryID(2), ragAuxiliaryID(3), ragAuxiliaryID(4),
			ragAuxiliaryID(5), ragAuxiliaryID(6), ragAuxiliaryID(7), ragAuxiliaryID(8),
		})

	if _, err := executor.Execute(context.Background(), execution); !errors.Is(err, providerErr) {
		t.Fatalf("first attempt error=%v, want provider failure", err)
	}
	first := snapshots.finalize
	if memory.calls != 1 || snapshots.finalizeCalls != 1 || first.Context.Validate() != nil ||
		first.Run.NodeAttemptID != execution.NodeAttemptID || first.Run.MemoryContext != first.Context {
		t.Fatalf("first attempt memory=%#v snapshots=%#v", memory, snapshots)
	}

	memory.items = []agentapplication.EffectiveMemoryItem{{
		ID: ragInputID(8), Version: 2, Type: "PREFERENCE", Category: agentapplication.MemoryCategoryUserPreference,
		Content: json.RawMessage(`{"answer_style":"detailed"}`),
	}}
	secondExecution := execution
	secondExecution.NodeAttemptID = ragAuxiliaryID(9)
	if _, err := executor.Execute(context.Background(), secondExecution); !errors.Is(err, providerErr) {
		t.Fatalf("second attempt error=%v, want provider failure", err)
	}
	second := snapshots.finalize
	if memory.calls != 2 || snapshots.finalizeCalls != 2 || model.CallCount() != 2 || finalizer.finalizeCalls != 0 ||
		second.Run.NodeAttemptID != secondExecution.NodeAttemptID || second.Run.MemoryContext != second.Context {
		t.Fatalf("second attempt memory=%#v snapshots=%#v provider_calls=%d finalizer=%#v", memory, snapshots, model.CallCount(), finalizer)
	}
	if first.Context.SnapshotID == second.Context.SnapshotID || first.Context.Digest == second.Context.Digest {
		t.Fatalf("new attempt did not freeze changed Memory: first=%+v second=%+v", first.Context, second.Context)
	}
}
