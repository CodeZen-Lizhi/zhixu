package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const maxConversationTitleBytes = 512

// ConversationCreateRequest 是创建短期 RAG 会话前参与幂等绑定的领域输入。
type ConversationCreateRequest struct {
	WorkspaceID foundation.ID
	Title       *string
}

// CanonicalizeConversationCreateRequest 校验并返回不保留调用方指针的规范创建请求。
func CanonicalizeConversationCreateRequest(request ConversationCreateRequest) (ConversationCreateRequest, error) {
	workspaceID, err := foundation.ParseID(string(request.WorkspaceID))
	if err != nil || workspaceID != request.WorkspaceID {
		return ConversationCreateRequest{}, invalid(ErrorCodeConversationInvalid, "conversation workspace is invalid", err)
	}
	canonical := ConversationCreateRequest{WorkspaceID: workspaceID}
	if request.Title == nil {
		return canonical, nil
	}
	title := strings.TrimSpace(*request.Title)
	if !validBoundedText(title, maxConversationTitleBytes, true) {
		return ConversationCreateRequest{}, invalid(ErrorCodeConversationInvalid, "conversation title is invalid", nil)
	}
	canonical.Title = &title
	return canonical, nil
}

// ComputeConversationCreateRequestHash 计算绑定 Workspace 与可选标题的稳定幂等哈希。
func ComputeConversationCreateRequestHash(request ConversationCreateRequest) (string, error) {
	canonical, err := CanonicalizeConversationCreateRequest(request)
	if err != nil {
		return "", err
	}
	payload := struct {
		SchemaVersion int           `json:"schema_version"`
		WorkspaceID   foundation.ID `json:"workspace_id"`
		Title         *string       `json:"title"`
	}{SchemaVersion: 1, WorkspaceID: canonical.WorkspaceID, Title: canonical.Title}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", inconsistent(ErrorCodeConversationInvalid, "conversation create request hash payload is invalid")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ConversationStatus 是短期 RAG 会话的受控生命周期。
type ConversationStatus string

const (
	// ConversationStatusOpen 表示会话可以继续追加 Question。
	ConversationStatusOpen ConversationStatus = "open"
	// ConversationStatusArchived 表示会话只读保留。
	ConversationStatusArchived ConversationStatus = "archived"
)

// Conversation 是按 Workspace 隔离、带乐观锁版本的短期问答上下文。
type Conversation struct {
	ID             foundation.ID
	WorkspaceID    foundation.ID
	Status         ConversationStatus
	Title          *string
	Version        int64
	LastActivityAt time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     *time.Time
}

// ValidateConversation 校验会话身份、标题、时间和状态束。
func ValidateConversation(conversation Conversation) error {
	id, idErr := foundation.ParseID(string(conversation.ID))
	workspaceID, workspaceErr := foundation.ParseID(string(conversation.WorkspaceID))
	if idErr != nil || workspaceErr != nil || id != conversation.ID || workspaceID != conversation.WorkspaceID || id == workspaceID ||
		conversation.Version < 1 || conversation.CreatedAt.IsZero() || conversation.UpdatedAt.Before(conversation.CreatedAt) ||
		conversation.LastActivityAt.Before(conversation.CreatedAt) {
		return invalid(ErrorCodeConversationInvalid, "conversation identity or lifecycle is invalid", nil)
	}
	if conversation.Title != nil && !validBoundedText(*conversation.Title, maxConversationTitleBytes, true) {
		return invalid(ErrorCodeConversationInvalid, "conversation title is invalid", nil)
	}
	switch conversation.Status {
	case ConversationStatusOpen:
		if conversation.ArchivedAt != nil {
			return invalid(ErrorCodeConversationInvalid, "open conversation cannot have an archive time", nil)
		}
	case ConversationStatusArchived:
		if conversation.Version < 2 || conversation.ArchivedAt == nil || conversation.ArchivedAt.Before(conversation.CreatedAt) ||
			!conversation.ArchivedAt.Equal(conversation.UpdatedAt) {
			return invalid(ErrorCodeConversationInvalid, "archived conversation requires a valid archive time", nil)
		}
	default:
		return invalid(ErrorCodeConversationInvalid, "conversation status is unsupported", nil)
	}
	return nil
}

// ValidateConversationTransition 校验会话只能从 open 进入 archived。
func ValidateConversationTransition(from, to ConversationStatus) error {
	if from == ConversationStatusOpen && to == ConversationStatusArchived {
		return nil
	}
	return versionConflict(ErrorCodeConversationTransitionInvalid, "conversation status transition is not allowed")
}
