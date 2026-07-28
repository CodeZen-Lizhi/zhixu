package domain

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRAGMemoryContextRefAcceptsEmptySnapshotAndRejectsPartialBinding(t *testing.T) {
	ref := RAGMemoryContextRef{
		SnapshotID: memoryContextTestID(1), SchemaVersion: RAGMemoryContextSchemaVersion,
		Digest: strings.Repeat("a", 64), ItemCount: 0, ByteCount: 68,
	}
	if err := ref.Validate(); err != nil || !ref.IsBound() {
		t.Fatalf("valid empty context ref rejected: ref=%#v err=%v", ref, err)
	}
	partial := RAGMemoryContextRef{SnapshotID: ref.SnapshotID}
	if err := partial.Validate(); errorCode(err) != ErrorCodeMemoryContextInvalid {
		t.Fatalf("partial context ref err=%v", err)
	}
	tooMany := ref
	tooMany.ItemCount = MaxRAGMemoryContextItems + 1
	if err := tooMany.Validate(); errorCode(err) != ErrorCodeMemoryContextInvalid {
		t.Fatalf("oversized context ref err=%v", err)
	}
}

func TestRAGMemorySnapshotRequiresExactTerminalClosure(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	snapshot := RAGMemorySnapshot{
		ID: memoryContextTestID(1), WorkspaceID: memoryContextTestID(2), WorkflowRunID: memoryContextTestID(3),
		NodeRunID: memoryContextTestID(4), NodeAttemptID: memoryContextTestID(5), ClaimantID: memoryContextTestID(6),
		OwnerKind: "USER", OwnerID: memoryContextTestID(7), TaskScopeID: memoryContextTestID(8),
		Status: RAGMemorySnapshotPreparing, CreatedAt: now, UpdatedAt: now,
	}
	if err := ValidateRAGMemorySnapshot(snapshot); err != nil {
		t.Fatalf("valid preparing snapshot rejected: %v", err)
	}

	ready := snapshot
	ready.Status = RAGMemorySnapshotReady
	ready.Context = RAGMemoryContextRef{
		SnapshotID: ready.ID, SchemaVersion: RAGMemoryContextSchemaVersion,
		Digest: strings.Repeat("b", 64), ItemCount: 0, ByteCount: 68,
	}
	ready.ModelRunID = memoryContextTestID(9)
	ready.UpdatedAt = now.Add(time.Microsecond)
	if err := ValidateRAGMemorySnapshot(ready); err != nil {
		t.Fatalf("valid ready snapshot rejected: %v", err)
	}
	ready.Context.SnapshotID = memoryContextTestID(10)
	if err := ValidateRAGMemorySnapshot(ready); errorCode(err) != ErrorCodeMemorySnapshotInvalid {
		t.Fatalf("ready snapshot with mismatched context err=%v", err)
	}

	failed := snapshot
	failed.Status = RAGMemorySnapshotFailed
	failed.ErrorCode = "AGENT_MEMORY_CONTEXT_UNAVAILABLE"
	failed.UpdatedAt = now.Add(time.Microsecond)
	if err := ValidateRAGMemorySnapshot(failed); err != nil {
		t.Fatalf("valid failed snapshot rejected: %v", err)
	}
	failed.ModelRunID = memoryContextTestID(9)
	if err := ValidateRAGMemorySnapshot(failed); errorCode(err) != ErrorCodeMemorySnapshotInvalid {
		t.Fatalf("failed snapshot with model run err=%v", err)
	}
}

func memoryContextTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("62000000-0000-4000-8000-%012d", value))
}
