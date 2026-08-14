package application

import (
	"context"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeAnswerFinalizationUnknown 表示事务提交结果无法确认，调用方不得重复 Provider 副作用。
	ErrorCodeAnswerFinalizationUnknown = "CONVERSATION_ANSWER_FINALIZE_UNKNOWN"
)

// AnswerPublicationLookup 冻结 Workflow Attempt 与 Conversation 发布槽的完整身份。
type AnswerPublicationLookup struct {
	WorkspaceID    foundation.ID
	WorkflowRunID  foundation.ID
	NodeRunID      foundation.ID
	NodeAttemptID  foundation.ID
	ConversationID foundation.ID
	QuestionID     foundation.ID
	AnswerID       foundation.ID
}

// AnswerDraftTerminalBinding 冻结待原子终结的草稿代际与 Runtime Attempt fence。
// Workspace、Answer、Workflow 与 Node 身份复用 FinalizeAnswerCommand 的终结绑定。
type AnswerDraftTerminalBinding struct {
	SessionID  foundation.ID
	Generation int64
	AttemptNo  int
	LeaseOwner string
}

// AnswerDraftPublication 是 AnswerDraftTerminalBinding 的兼容别名。
// Deprecated: 新调用方应使用 AnswerDraftTerminalBinding；终态可能为 PUBLISHED 或 ABORTED。
type AnswerDraftPublication = AnswerDraftTerminalBinding

// FinalizeAnswerCommand 请求在一个事务中终结 Model Run、Answer、Conversation 和通知事件。
type FinalizeAnswerCommand struct {
	AnswerPublicationLookup
	ModelRunID              foundation.ID
	ExpectedAnswerVersion   int64
	ExpectedModelRunVersion int64
	Proposal                agentapplication.RAGTerminalProposal
	Draft                   *AnswerDraftTerminalBinding
}

// AnswerFinalizer 是 Workflow 访问最终 Answer 发布事实的唯一应用层端口。
type AnswerFinalizer interface {
	// Lookup 在 Provider 调用前查找并验证既有终态；pending 返回 found=false。
	Lookup(context.Context, AnswerPublicationLookup) (workflow.OutputReceipt, bool, error)
	// Finalize 原子发布 canonical proposal；bool 表示精确重放。
	Finalize(context.Context, FinalizeAnswerCommand) (workflow.OutputReceipt, bool, error)
}
