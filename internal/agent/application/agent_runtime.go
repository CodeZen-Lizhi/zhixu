package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	// MaxAgentMessages bounds the transient transcript handed to a provider.
	MaxAgentMessages = 64
	// MaxAgentMessageBytes bounds one transient message. It is deliberately
	// lower than the persisted tool-document limit because this is prompt data.
	MaxAgentMessageBytes = 512 * 1024
	// MaxAgentTools bounds the frozen model-visible allowlist for one Agent run.
	MaxAgentTools = 16
	// MaxAgentToolReasonBytes bounds the model-provided explanation sent to the
	// project Tool service.
	MaxAgentToolReasonBytes = toolsdomain.MaxToolReasonBytes
	// MaxAgentResultBytes bounds the in-memory Agent result before final answer
	// generation. It is not a persistence limit for the published Answer.
	MaxAgentResultBytes = 4 * 1024 * 1024
	// MaxAnswerStreamChunkBytes bounds one provider stream frame after UTF-8
	// validation. The sink owns its own aggregate/session limit.
	MaxAnswerStreamChunkBytes = 64 * 1024
	// MaxDraftStreamSessionBytes limits the short-lived browser projection. The
	// canonical Answer remains subject to its own domain limit and publication
	// gates; draft bytes are never an Answer fact.
	MaxDraftStreamSessionBytes = 4 * 1024 * 1024
	// MaxDraftStreamTTL bounds replayable draft retention. A current worker
	// lease, not this TTL, remains the authority for writes.
	MaxDraftStreamTTL = 30 * time.Minute
	// MaxDraftStreamReplayChunks bounds one SSE recovery query.
	MaxDraftStreamReplayChunks = 128
)

const (
	ErrorCodeAgentRuntimeInvalid       = "AGENT_RUNTIME_REQUEST_INVALID"
	ErrorCodeAgentRuntimeUnavailable   = "AGENT_RUNTIME_UNAVAILABLE"
	ErrorCodeAgentRuntimeOutputInvalid = "AGENT_RUNTIME_OUTPUT_INVALID"
	ErrorCodeAnswerStreamInvalid       = "AGENT_ANSWER_STREAM_INVALID"
)

// AgentMessageRole is the project-owned subset of chat roles needed by the
// classic Eino message adapter.
type AgentMessageRole string

const (
	AgentMessageSystem    AgentMessageRole = "system"
	AgentMessageUser      AgentMessageRole = "user"
	AgentMessageAssistant AgentMessageRole = "assistant"
)

// AgentMessage deliberately contains no provider call id or tool-result role.
// The complete ReAct transcript remains inside one Eino run; only bounded
// system/user/assistant content crosses the project Port.
type AgentMessage struct {
	Role    AgentMessageRole
	Content string
}

// AgentToolSpec is a frozen, project-owned description of one allowed Tool.
type AgentToolSpec struct {
	Ref         toolsdomain.ToolRef
	Name        string
	Description string
	InputSchema []byte
}

// AgentToolInvocation is the only model-to-project execution request. The
// trusted Attempt identity is bound by the caller's invoker, never supplied by
// the model or by this DTO.
type AgentToolInvocation struct {
	Ref       toolsdomain.ToolRef
	Arguments []byte
	Reason    string
	CallNo    int
}

// AgentToolResult is an untrusted, bounded result returned by the project Tool
// service. The Eino adapter serializes it as a transient tool message.
type AgentToolResult struct {
	Output []byte
}

// AgentToolInvoker is implemented by a workflow-bound bridge around the sole
// project ExecutionService.Execute seam.
type AgentToolInvoker interface {
	Invoke(context.Context, AgentToolInvocation) (AgentToolResult, error)
}

// AgentRunRequest is the provider-neutral input for a bounded ReAct run.
type AgentRunRequest struct {
	Model    domain.ModelRef
	Profile  domain.ModelProfileRef
	Prompt   domain.PromptRef
	Schema   domain.SchemaRef
	Messages []AgentMessage
	Tools    []AgentToolSpec
	// ReturnDirectlyTools asks the runtime to finish after one of these tools
	// returns. It is useful when a separate project-owned call performs the
	// final synthesis; an empty list keeps the normal ReAct loop.
	ReturnDirectlyTools []string
	MaxIterations       int
	MaxInputTokens      int64
	MaxOutputTokens     int
	Budget              *RunBudgetLedger
	Recorder            *ModelCallRecorder
	ToolInvoker         AgentToolInvoker
}

// AgentRunResult is the only Agent output exposed to the project workflow.
type AgentRunResult struct {
	FinalText  string
	Iterations int
	ToolCalls  int
	Usage      domain.TokenUsage
}

// AgentRuntime executes a bounded Agent loop. Implementations must not retry or
// fail over a provider call internally; Workflow owns retry and Attempt facts.
type AgentRuntime interface {
	Run(context.Context, AgentRunRequest) (AgentRunResult, error)
}

// AnswerStreamRequest describes the second, tool-free provider call used for
// the user-visible answer stream. It is separate from AgentRunRequest so an
// Agent's intermediate messages can never accidentally reach a browser sink.
type AnswerStreamRequest struct {
	Model           domain.ModelRef
	Profile         domain.ModelProfileRef
	Prompt          domain.PromptRef
	Schema          domain.SchemaRef
	Messages        []AgentMessage
	MaxInputTokens  int64
	MaxOutputTokens int
	Budget          *RunBudgetLedger
	Recorder        *ModelCallRecorder
}

// AnswerStreamChunk is a validated UTF-8 text fragment. Sequence numbers are
// assigned by the sink, not trusted from a Provider.
type AnswerStreamChunk struct {
	Content string
}

// AnswerStreamSink owns persistence/CAS of short-lived draft chunks. Returning
// an error allows the runtime to mark the sink degraded while continuing to
// drain and close the Provider stream.
type AnswerStreamSink interface {
	Append(context.Context, AnswerStreamChunk) error
}

// DraftStreamStatus is the lifecycle of a transient final-answer projection.
// COMPLETED means provider EOF only. PUBLISHED is set solely by the Answer
// finalization transaction after all domain gates have passed.
type DraftStreamStatus string

const (
	DraftStreamActive     DraftStreamStatus = "ACTIVE"
	DraftStreamCompleted  DraftStreamStatus = "COMPLETED"
	DraftStreamDegraded   DraftStreamStatus = "DEGRADED"
	DraftStreamPublished  DraftStreamStatus = "PUBLISHED"
	DraftStreamAborted    DraftStreamStatus = "ABORTED"
	DraftStreamSuperseded DraftStreamStatus = "SUPERSEDED"
)

// DraftStreamBinding is the trusted workflow identity which fences a draft
// generation. AttemptNo and LeaseOwner are copied from an already-claimed
// NodeAttempt; callers must never accept them from browser input.
type DraftStreamBinding struct {
	WorkspaceID   foundation.ID
	AnswerID      foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeAttemptID foundation.ID
	AttemptNo     int
	LeaseOwner    string
}

// BeginDraftStreamCommand creates the next generation for an Answer. The
// store verifies the binding against the active DB-time runtime lease, then
// atomically supersedes any previous non-terminal generation.
type BeginDraftStreamCommand struct {
	DraftStreamBinding
	TTL time.Duration
}

// DraftStreamSession is the project-owned, short-lived stream projection.
type DraftStreamSession struct {
	ID         foundation.ID
	Binding    DraftStreamBinding
	Generation int64
	Status     DraftStreamStatus
	NextSeq    int64
	TotalBytes int
	ExpiresAt  time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DraftStreamAppendCommand stores one validated final-answer frame. Sequence
// is allocated by the store after the lease fence is checked.
type DraftStreamAppendCommand struct {
	SessionID foundation.ID
	Binding   DraftStreamBinding
	Content   string
}

// DraftStreamChunk is the persisted, generation-scoped SSE recovery unit.
type DraftStreamChunk struct {
	SessionID  foundation.ID
	Generation int64
	Sequence   int64
	Content    string
	CreatedAt  time.Time
}

// DraftStreamTransitionCommand moves a session through a terminal or
// degradation transition under the same active Attempt lease fence.
type DraftStreamTransitionCommand struct {
	SessionID foundation.ID
	Binding   DraftStreamBinding
}

// DraftStreamReadCursor is the parsed generation/sequence Last-Event-ID.
type DraftStreamReadCursor struct {
	Generation int64
	Sequence   int64
}

// DraftStreamReadQuery is the bounded recovery query used by the dedicated
// Answer SSE endpoint. The cursor is never a workflow authority.
type DraftStreamReadQuery struct {
	WorkspaceID foundation.ID
	AnswerID    foundation.ID
	After       *DraftStreamReadCursor
	Limit       int
}

// DraftStreamReadResult returns the latest generation and a contiguous page.
// A nil Session means no currently replayable projection exists.
type DraftStreamReadResult struct {
	Session *DraftStreamSession
	Chunks  []DraftStreamChunk
	// Invalidated distinguishes an existing projection whose Runtime Claim or
	// TTL is no longer valid from an answer that has never started streaming.
	// The HTTP boundary uses it to close a cursor-less SSE connection promptly.
	Invalidated bool
}

// DraftStreamStore owns the transient PostgreSQL projection. It deliberately
// does not expose PUBLISHED: AnswerFinalizer changes that state in the same
// transaction as the canonical Answer publication.
type DraftStreamStore interface {
	BeginDraftStream(context.Context, BeginDraftStreamCommand) (DraftStreamSession, error)
	AppendDraftStream(context.Context, DraftStreamAppendCommand) (DraftStreamChunk, error)
	CompleteDraftStream(context.Context, DraftStreamTransitionCommand) (DraftStreamSession, error)
	DegradeDraftStream(context.Context, DraftStreamTransitionCommand) (DraftStreamSession, error)
	AbortDraftStream(context.Context, DraftStreamTransitionCommand) (DraftStreamSession, error)
	ReadDraftStream(context.Context, DraftStreamReadQuery) (DraftStreamReadResult, error)
	CleanupExpiredDraftStreams(context.Context, int) (int64, error)
}

// AnswerStreamResult is the bounded final concatenation and provider usage.
type AnswerStreamResult struct {
	Content       string
	Usage         domain.TokenUsage
	DraftDegraded bool
}

// AnswerStreamRuntime consumes a tool-free Eino Stream exactly once and sends
// validated chunks to a project-owned sink.
type AnswerStreamRuntime interface {
	Stream(context.Context, AnswerStreamRequest, AnswerStreamSink) (AnswerStreamResult, error)
}

func validateAgentRunRequest(request AgentRunRequest) error {
	if request.Model.Validate() != nil || request.Profile.Validate() != nil || request.Prompt.Validate() != nil || request.Schema.Validate() != nil ||
		len(request.Messages) == 0 || len(request.Messages) > MaxAgentMessages || request.MaxIterations <= 0 || request.MaxIterations > 64 ||
		request.MaxInputTokens <= 0 || request.MaxInputTokens > MaxRunTokens ||
		request.MaxOutputTokens <= 0 || request.MaxOutputTokens > MaxOutputTokens || request.Budget == nil || request.Recorder == nil {
		return applicationError(foundation.ErrorInvalidInput, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent runtime request binding or budget is invalid"))
	}
	if err := validateAgentMessages(request.Messages); err != nil {
		return err
	}
	if len(request.Tools) > MaxAgentTools {
		return applicationError(foundation.ErrorInvalidInput, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent tool allowlist is too large"))
	}
	seen := make(map[string]struct{}, len(request.Tools))
	toolNames := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		if err := tool.Ref.Validate(); err != nil || !toolNameMatches(tool.Name, tool.Ref) || strings.TrimSpace(tool.Description) == "" ||
			len(tool.Description) > toolsdomain.MaxToolDescriptionBytes || len(tool.InputSchema) == 0 || len(tool.InputSchema) > toolsdomain.MaxToolSchemaBytes ||
			!json.Valid(tool.InputSchema) || !isJSONObject(tool.InputSchema) {
			return applicationError(foundation.ErrorInvalidInput, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent tool schema is invalid"))
		}
		key := tool.Ref.Name + "@" + strconv.FormatInt(tool.Ref.Version, 10)
		if _, exists := seen[key]; exists {
			return applicationError(foundation.ErrorConsistencyViolation, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent tool allowlist contains a duplicate"))
		}
		if _, exists := toolNames[tool.Name]; exists {
			return applicationError(foundation.ErrorConsistencyViolation, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent tool allowlist contains multiple versions of one tool name"))
		}
		seen[key] = struct{}{}
		toolNames[tool.Name] = struct{}{}
	}
	returnDirectlySeen := make(map[string]struct{}, len(request.ReturnDirectlyTools))
	for _, name := range request.ReturnDirectlyTools {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > toolsdomain.MaxToolNameBytes {
			return applicationError(foundation.ErrorInvalidInput, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent return-directly tool name is invalid"))
		}
		if _, ok := toolNames[name]; !ok {
			return applicationError(foundation.ErrorConsistencyViolation, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent return-directly tool is not allowlisted"))
		}
		if _, duplicate := returnDirectlySeen[name]; duplicate {
			return applicationError(foundation.ErrorConsistencyViolation, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent return-directly tool list contains a duplicate"))
		}
		returnDirectlySeen[name] = struct{}{}
	}
	if len(request.Tools) > 0 && request.ToolInvoker == nil {
		return applicationError(foundation.ErrorDependencyUnavailable, ErrorCodeAgentRuntimeUnavailable, false, errors.New("agent tool invoker is unavailable"))
	}
	return nil
}

// ValidateAgentRunRequest is the public boundary validator used by adapters.
func ValidateAgentRunRequest(request AgentRunRequest) error { return validateAgentRunRequest(request) }

func validateAnswerStreamRequest(request AnswerStreamRequest) error {
	if request.Model.Validate() != nil || request.Profile.Validate() != nil || request.Prompt.Validate() != nil || request.Schema.Validate() != nil ||
		len(request.Messages) == 0 || len(request.Messages) > MaxAgentMessages || request.MaxInputTokens <= 0 || request.MaxInputTokens > MaxRunTokens ||
		request.MaxOutputTokens <= 0 || request.MaxOutputTokens > MaxOutputTokens ||
		request.Budget == nil || request.Recorder == nil {
		return applicationError(foundation.ErrorInvalidInput, ErrorCodeAnswerStreamInvalid, false, errors.New("answer stream request binding or budget is invalid"))
	}
	return validateAgentMessages(request.Messages)
}

// ValidateAnswerStreamRequest is the public boundary validator used by adapters.
func ValidateAnswerStreamRequest(request AnswerStreamRequest) error {
	return validateAnswerStreamRequest(request)
}

func validateAgentMessages(messages []AgentMessage) error {
	for index, message := range messages {
		if (message.Role != AgentMessageSystem && message.Role != AgentMessageUser && message.Role != AgentMessageAssistant) ||
			strings.TrimSpace(message.Content) == "" || len(message.Content) > MaxAgentMessageBytes || !utf8.ValidString(message.Content) {
			return applicationError(foundation.ErrorInvalidInput, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent message is invalid"))
		}
		if index == 0 && message.Role != AgentMessageSystem {
			return applicationError(foundation.ErrorInvalidInput, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent messages require one leading system message"))
		}
		if index > 0 && message.Role == AgentMessageSystem {
			return applicationError(foundation.ErrorInvalidInput, ErrorCodeAgentRuntimeInvalid, false, errors.New("agent messages contain more than one system message"))
		}
	}
	return nil
}

func toolNameMatches(name string, ref toolsdomain.ToolRef) bool {
	return name == ref.Name
}

func isJSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}
