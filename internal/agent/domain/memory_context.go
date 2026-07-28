package domain

import (
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// RAGMemoryContextSchemaVersion 是 Conversation RAG Memory canonical envelope 的版本。
	RAGMemoryContextSchemaVersion = "agent-rag-memory-context/v1"
	// MaxRAGMemoryContextItems 限制一次模型输入可见的有效 Memory 条数。
	MaxRAGMemoryContextItems = 32
	// MaxRAGMemoryContextBytes 限制 provider-visible 非证据上下文的编码字节数。
	MaxRAGMemoryContextBytes = 64 * 1024
)

// RAGMemorySnapshotStatus 是 Node Attempt 级 Memory 快照 reservation 的持久状态。
type RAGMemorySnapshotStatus string

const (
	RAGMemorySnapshotPreparing RAGMemorySnapshotStatus = "PREPARING"
	RAGMemorySnapshotReady     RAGMemorySnapshotStatus = "READY"
	RAGMemorySnapshotFailed    RAGMemorySnapshotStatus = "FAILED"
)

// RAGMemoryContextRef 是 Model Run 持久化的非证据上下文审计摘要，不包含 Memory 正文。
type RAGMemoryContextRef struct {
	SnapshotID    foundation.ID
	SchemaVersion string
	Digest        string
	ItemCount     int
	ByteCount     int64
}

// IsBound 表示该引用至少包含一个字段；部分绑定随后由 Validate 拒绝。
func (ref RAGMemoryContextRef) IsBound() bool {
	return ref.SnapshotID != "" || ref.SchemaVersion != "" || ref.Digest != "" || ref.ItemCount != 0 || ref.ByteCount != 0
}

// Validate 校验 RAG Memory 上下文摘要的版本、哈希和固定资源边界。
func (ref RAGMemoryContextRef) Validate() error {
	if !canonicalID(ref.SnapshotID) || ref.SchemaVersion != RAGMemoryContextSchemaVersion ||
		!lowerHex(ref.Digest, 64) || ref.ItemCount < 0 || ref.ItemCount > MaxRAGMemoryContextItems ||
		ref.ByteCount <= 0 || ref.ByteCount > MaxRAGMemoryContextBytes {
		return invalid(ErrorCodeMemoryContextInvalid, "rag memory context reference is invalid")
	}
	return nil
}

// RAGMemorySnapshot 是唯一 claimant 在加载 Memory 前创建的 attempt-scoped reservation。
type RAGMemorySnapshot struct {
	ID            foundation.ID
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeAttemptID foundation.ID
	ClaimantID    foundation.ID
	OwnerKind     string
	OwnerID       foundation.ID
	TaskScopeID   foundation.ID
	Status        RAGMemorySnapshotStatus
	Context       RAGMemoryContextRef
	ModelRunID    foundation.ID
	ErrorCode     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ValidateRAGMemorySnapshot 校验 reservation identity、终态闭包与无正文审计元组。
func ValidateRAGMemorySnapshot(snapshot RAGMemorySnapshot) error {
	ids := []foundation.ID{
		snapshot.ID, snapshot.WorkspaceID, snapshot.WorkflowRunID, snapshot.NodeRunID,
		snapshot.NodeAttemptID, snapshot.ClaimantID, snapshot.OwnerID, snapshot.TaskScopeID,
	}
	if !canonicalUniqueIDs(ids, false) || !canonicalOwnerKind(snapshot.OwnerKind) || snapshot.CreatedAt.IsZero() ||
		snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		return invalid(ErrorCodeMemorySnapshotInvalid, "rag memory snapshot binding is invalid")
	}
	switch snapshot.Status {
	case RAGMemorySnapshotPreparing:
		if snapshot.Context.IsBound() || snapshot.ModelRunID != "" || snapshot.ErrorCode != "" {
			return inconsistent(ErrorCodeMemorySnapshotInvalid, "preparing rag memory snapshot contains a terminal binding")
		}
	case RAGMemorySnapshotReady:
		if snapshot.Context.Validate() != nil || snapshot.Context.SnapshotID != snapshot.ID ||
			!canonicalID(snapshot.ModelRunID) || snapshot.ModelRunID == snapshot.ID || snapshot.ErrorCode != "" {
			return inconsistent(ErrorCodeMemorySnapshotInvalid, "ready rag memory snapshot is incomplete")
		}
	case RAGMemorySnapshotFailed:
		if snapshot.Context.IsBound() || snapshot.ModelRunID != "" || !canonicalErrorCode(snapshot.ErrorCode) {
			return inconsistent(ErrorCodeMemorySnapshotInvalid, "failed rag memory snapshot is incomplete")
		}
	default:
		return invalid(ErrorCodeMemorySnapshotInvalid, "rag memory snapshot status is unsupported")
	}
	return nil
}

func canonicalOwnerKind(value string) bool {
	if value == "" || len(value) > 32 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && character != '_' {
			return false
		}
	}
	return true
}
