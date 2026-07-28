package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MemoryCategoryUserPreference 表示只可调整表达偏好的 Memory。
	MemoryCategoryUserPreference = "user_preferences"
	// MemoryCategoryTaskContext 表示只可辅助理解当前任务意图的 Memory。
	MemoryCategoryTaskContext = "task_context"
	// ErrorCodeMemoryContextUnavailable 表示有效 Memory 查询依赖不可用。
	ErrorCodeMemoryContextUnavailable = "AGENT_MEMORY_CONTEXT_UNAVAILABLE"
	// ErrorCodeMemorySnapshotConflict 表示当前 Node Attempt 已有不可重放的快照 reservation。
	ErrorCodeMemorySnapshotConflict = "AGENT_MEMORY_SNAPSHOT_CONFLICT"
)

// MemoryOwnerRef 是 Agent 自有的稳定 Memory owner 投影。
type MemoryOwnerRef struct {
	Kind string
	ID   foundation.ID
}

// EffectiveMemoryQuery 限制一次 Conversation RAG 的有效 Memory 读取。
type EffectiveMemoryQuery struct {
	WorkspaceID foundation.ID
	Owner       MemoryOwnerRef
	TaskScopeID foundation.ID
	Limit       int
}

// EffectiveMemoryItem 是 Agent Adapter 返回的 canonical、非证据 Memory 项。
type EffectiveMemoryItem struct {
	ID       foundation.ID
	Version  int64
	Type     string
	Category string
	Content  json.RawMessage
}

// EffectiveMemoryLoader 是 Conversation RAG 唯一允许使用的有效 Memory 读取端口。
type EffectiveMemoryLoader interface {
	Load(context.Context, EffectiveMemoryQuery) ([]EffectiveMemoryItem, error)
}

// NonEvidenceContext 是 provider-visible、显式不可信且不可引用的 Memory 上下文。
type NonEvidenceContext struct {
	UntrustedData   bool              `json:"untrusted_data"`
	UserPreferences []json.RawMessage `json:"user_preferences"`
	TaskContext     []json.RawMessage `json:"task_context"`
}

// MemoryContextSnapshot 保存 canonical context 与不含正文的审计摘要。
type MemoryContextSnapshot struct {
	Context       NonEvidenceContext
	SchemaVersion string
	Digest        string
	ItemCount     int
	ByteCount     int64
}

// FinalizeRAGMemorySnapshotCommand 原子闭合 READY snapshot 与首个 Model Run。
type FinalizeRAGMemorySnapshotCommand struct {
	SnapshotID foundation.ID
	ClaimantID foundation.ID
	Context    domain.RAGMemoryContextRef
	Run        domain.ModelRun
}

// FailRAGMemorySnapshotCommand 将唯一 claimant 的加载失败归约为持久 FAILED。
type FailRAGMemorySnapshotCommand struct {
	SnapshotID  foundation.ID
	WorkspaceID foundation.ID
	ClaimantID  foundation.ID
	ErrorCode   string
}

// RAGMemorySnapshotRepository 是 attempt claim 与 Model Run 原子创建的持久边界。
type RAGMemorySnapshotRepository interface {
	BeginRAGMemorySnapshot(context.Context, domain.RAGMemorySnapshot) (domain.RAGMemorySnapshot, bool, error)
	FinalizeRAGMemorySnapshotAndCreateModelRun(context.Context, FinalizeRAGMemorySnapshotCommand) (domain.ModelRun, error)
	FailRAGMemorySnapshot(context.Context, FailRAGMemorySnapshotCommand) (domain.RAGMemorySnapshot, error)
}

// BuildMemoryContextSnapshot 生成稳定 provider context 与绑定身份/版本/内容的 SHA-256 摘要。
func BuildMemoryContextSnapshot(query EffectiveMemoryQuery, items []EffectiveMemoryItem) (MemoryContextSnapshot, error) {
	if err := validateEffectiveMemoryQuery(query); err != nil || len(items) > query.Limit {
		return MemoryContextSnapshot{}, memoryContextInvalid("effective memory result exceeds its query boundary")
	}
	contextValue := NonEvidenceContext{
		UntrustedData:   true,
		UserPreferences: make([]json.RawMessage, 0, len(items)),
		TaskContext:     make([]json.RawMessage, 0, len(items)),
	}
	digestItems := make([]memoryDigestItem, 0, len(items))
	seen := make(map[foundation.ID]struct{}, len(items))
	for _, item := range items {
		content, err := canonicalMemoryContent(item.Content)
		if err != nil || !canonicalApplicationID(item.ID) || item.Version < 1 || !canonicalMemoryType(item.Type) ||
			(item.Category != MemoryCategoryUserPreference && item.Category != MemoryCategoryTaskContext) {
			return MemoryContextSnapshot{}, memoryContextInvalid("effective memory item is invalid")
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return MemoryContextSnapshot{}, memoryContextInvalid("effective memory item is duplicated")
		}
		seen[item.ID] = struct{}{}
		copied := append(json.RawMessage(nil), content...)
		if item.Category == MemoryCategoryUserPreference {
			contextValue.UserPreferences = append(contextValue.UserPreferences, copied)
		} else {
			contextValue.TaskContext = append(contextValue.TaskContext, copied)
		}
		digestItems = append(digestItems, memoryDigestItem{
			ID: item.ID, Version: item.Version, Type: item.Type, Content: append(json.RawMessage(nil), content...),
		})
	}
	visible, err := json.Marshal(contextValue)
	if err != nil || len(visible) == 0 || len(visible) > domain.MaxRAGMemoryContextBytes {
		return MemoryContextSnapshot{}, memoryContextInvalid("effective memory context exceeds its byte boundary")
	}
	envelope := memoryDigestEnvelope{
		SchemaVersion: domain.RAGMemoryContextSchemaVersion,
		WorkspaceID:   query.WorkspaceID,
		OwnerKind:     query.Owner.Kind,
		OwnerID:       query.Owner.ID,
		TaskScopeID:   query.TaskScopeID,
		Items:         digestItems,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return MemoryContextSnapshot{}, memoryContextInvalid("effective memory digest envelope cannot be encoded")
	}
	sum := sha256.Sum256(encoded)
	return MemoryContextSnapshot{
		Context: contextValue, SchemaVersion: domain.RAGMemoryContextSchemaVersion,
		Digest: hex.EncodeToString(sum[:]), ItemCount: len(items), ByteCount: int64(len(visible)),
	}, nil
}

// ValidateNonEvidenceContext 防御性校验模型输入中的非证据上下文保持 canonical 且有界。
func ValidateNonEvidenceContext(contextValue NonEvidenceContext) error {
	if !contextValue.UntrustedData || contextValue.UserPreferences == nil || contextValue.TaskContext == nil ||
		len(contextValue.UserPreferences)+len(contextValue.TaskContext) > domain.MaxRAGMemoryContextItems {
		return memoryContextInvalid("non-evidence context shape is invalid")
	}
	for _, content := range append(append([]json.RawMessage(nil), contextValue.UserPreferences...), contextValue.TaskContext...) {
		if _, err := canonicalMemoryContent(content); err != nil {
			return memoryContextInvalid("non-evidence context content is invalid")
		}
	}
	encoded, err := json.Marshal(contextValue)
	if err != nil || len(encoded) == 0 || len(encoded) > domain.MaxRAGMemoryContextBytes {
		return memoryContextInvalid("non-evidence context exceeds its byte boundary")
	}
	return nil
}

type memoryDigestEnvelope struct {
	SchemaVersion string             `json:"schema_version"`
	WorkspaceID   foundation.ID      `json:"workspace_id"`
	OwnerKind     string             `json:"owner_kind"`
	OwnerID       foundation.ID      `json:"owner_id"`
	TaskScopeID   foundation.ID      `json:"task_scope_id"`
	Items         []memoryDigestItem `json:"items"`
}

type memoryDigestItem struct {
	ID      foundation.ID   `json:"memory_id"`
	Version int64           `json:"version"`
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
}

func validateEffectiveMemoryQuery(query EffectiveMemoryQuery) error {
	if !canonicalApplicationID(query.WorkspaceID) || !canonicalApplicationID(query.Owner.ID) ||
		!canonicalApplicationID(query.TaskScopeID) || !canonicalOwnerKind(query.Owner.Kind) ||
		query.Limit < 1 || query.Limit > domain.MaxRAGMemoryContextItems {
		return memoryContextInvalid("effective memory query is invalid")
	}
	return nil
}

func canonicalMemoryContent(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > domain.MaxRAGMemoryContextBytes || !json.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("memory content is not valid json")
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || len(object) == 0 {
		return nil, errors.New("memory content must be a nonempty object")
	}
	encoded, err := json.Marshal(object)
	if err != nil || !bytes.Equal(encoded, raw) {
		return nil, errors.New("memory content is not canonical")
	}
	return append(json.RawMessage(nil), encoded...), nil
}

func canonicalMemoryType(value string) bool {
	return value != "" && len(value) <= 64 && strings.TrimSpace(value) == value
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

func memoryContextInvalid(message string) error {
	return applicationError(foundation.ErrorInvalidInput, domain.ErrorCodeMemoryContextInvalid, false, errors.New(message))
}
