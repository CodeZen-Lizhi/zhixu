// Package application 编排 RAG Conversation 的短事务命令与有界读模型。
package application

import (
	"context"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// MaxIdempotencyKeyBytes 是 Conversation 命令允许的幂等键最大字节数。
	MaxIdempotencyKeyBytes = 128
)

// CreateConversationCommand 是创建短期 RAG 会话的应用命令。
type CreateConversationCommand struct {
	WorkspaceID    foundation.ID
	Title          *string
	IdempotencyKey string
}

// CreateConversationRecord 是 Repository 原子创建所需的完整规范事实。
type CreateConversationRecord struct {
	Conversation   conversationdomain.Conversation
	IdempotencyKey string
	RequestHash    string
}

// CreateConversationResult 区分首次创建和精确幂等重放。
type CreateConversationResult struct {
	Conversation conversationdomain.Conversation
	Replayed     bool
}

// ListConversationsQuery 是 Workspace 内按活动时间稳定分页的查询。
type ListConversationsQuery struct {
	WorkspaceID foundation.ID
	Cursor      *conversationdomain.ConversationCursor
	Limit       int
}

// ConversationPage 是有界 Conversation 列表及下一页边界。
type ConversationPage struct {
	Items      []conversationdomain.Conversation
	NextCursor *conversationdomain.ConversationCursor
}

// ListTurnsQuery 是一个 Conversation 内按 ordinal 稳定分页的查询。
type ListTurnsQuery struct {
	WorkspaceID    foundation.ID
	ConversationID foundation.ID
	Cursor         *conversationdomain.TurnCursor
	Limit          int
}

// TurnPage 是 Question 与 Answer 读投影的有界页面。
type TurnPage struct {
	Items      []TurnView
	NextCursor *conversationdomain.TurnCursor
}

// TurnView 把一个不可变 Question 与其唯一 Answer 槽组合为读模型。
type TurnView struct {
	Question conversationdomain.Question
	Answer   *AnswerView
}

// WorkflowRunView 是未发布 Answer 状态的唯一运行时权威投影。
type WorkflowRunView struct {
	RunID     foundation.ID
	Status    workflowdomain.RunStatus
	Version   int64
	UpdatedAt time.Time
}

// AnswerView 返回 Answer 事实、Workflow 状态和统一结果投影。
type AnswerView struct {
	Answer        conversationdomain.Answer
	Workflow      WorkflowRunView
	AssistantText string
	Citations     []agentdomain.Citation
}

// PublishedContextQuery 是 Query Plan 使用的有界已发布历史查询。
type PublishedContextQuery struct {
	WorkspaceID    foundation.ID
	ConversationID foundation.ID
	ThroughOrdinal int64
}

// Repository 是 Conversation 持久化和读模型的唯一应用端口。
type Repository interface {
	// CreateConversation 原子创建 Conversation；相同键与哈希返回精确重放。
	CreateConversation(context.Context, CreateConversationRecord) (CreateConversationResult, error)
	// ListConversations 返回稳定、有限的 Workspace 会话页面。
	ListConversations(context.Context, ListConversationsQuery) (ConversationPage, error)
	// GetConversation 按 Workspace 读取一个 Conversation，跨 Workspace 与不存在必须同为 NotFound。
	GetConversation(context.Context, foundation.ID, foundation.ID) (conversationdomain.Conversation, error)
	// ListTurns 批量返回 Question、Answer 和 Workflow 投影，禁止逐 Turn 查询。
	ListTurns(context.Context, ListTurnsQuery) (TurnPage, error)
	// GetAnswer 按 Workspace 返回一个 Answer 及其 Workflow 权威状态。
	GetAnswer(context.Context, foundation.ID, foundation.ID) (AnswerView, error)
	// LoadPublishedContext 返回指定 ordinal 之前可进入 Query Plan 的有界已发布历史。
	LoadPublishedContext(context.Context, PublishedContextQuery) ([]conversationdomain.PublishedTurn, error)
}
