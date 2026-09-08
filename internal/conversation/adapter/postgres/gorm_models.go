package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

// conversationJSONB 将一个 JSON 文档绑定为单个 PostgreSQL jsonb 参数。
type conversationJSONB []byte

func (value conversationJSONB) Value() (driver.Value, error) {
	if !json.Valid(value) {
		return nil, errors.New("conversation JSON document is invalid")
	}
	return string(value), nil
}

// conversationModel 显式映射会话事实；所有时间由领域命令或数据库提供。
type conversationModel struct {
	ID             string     `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID    string     `gorm:"column:workspace_id;type:uuid"`
	Status         string     `gorm:"column:status"`
	Title          *string    `gorm:"column:title"`
	Version        int64      `gorm:"column:version"`
	LastActivityAt time.Time  `gorm:"column:last_activity_at;autoCreateTime:false;autoUpdateTime:false"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;autoUpdateTime:false"`
	ArchivedAt     *time.Time `gorm:"column:archived_at"`
	IdempotencyKey string     `gorm:"column:idempotency_key"`
	RequestHash    string     `gorm:"column:request_hash"`
}

func (conversationModel) TableName() string { return "agent.conversation" }

// questionModel 保留问题、冻结上下文和幂等身份的显式列映射。
type questionModel struct {
	ID                    string            `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID           string            `gorm:"column:workspace_id;type:uuid"`
	ConversationID        string            `gorm:"column:conversation_id;type:uuid"`
	Ordinal               int64             `gorm:"column:ordinal"`
	Mode                  string            `gorm:"column:mode"`
	QuestionText          string            `gorm:"column:question_text"`
	Scope                 conversationJSONB `gorm:"column:scope;type:jsonb"`
	AnswerDepth           string            `gorm:"column:answer_depth"`
	OutputFormat          string            `gorm:"column:output_format"`
	ContextThroughOrdinal int64             `gorm:"column:context_through_ordinal"`
	ContextHash           string            `gorm:"column:context_hash"`
	IdempotencyKey        string            `gorm:"column:idempotency_key"`
	RequestHash           string            `gorm:"column:request_hash"`
	CreatedAt             time.Time         `gorm:"column:created_at;autoCreateTime:false"`
}

func (questionModel) TableName() string { return "agent.question" }

// answerModel 映射唯一 Answer slot，尚未发布的结果和模型身份保持 NULL。
type answerModel struct {
	ID                string             `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID       string             `gorm:"column:workspace_id;type:uuid"`
	ConversationID    string             `gorm:"column:conversation_id;type:uuid"`
	QuestionID        string             `gorm:"column:question_id;type:uuid"`
	WorkflowRunID     string             `gorm:"column:workflow_run_id;type:uuid"`
	ModelRunID        *string            `gorm:"column:model_run_id;type:uuid"`
	PublicationStatus string             `gorm:"column:publication_status"`
	ResultType        *string            `gorm:"column:result_type"`
	Result            *conversationJSONB `gorm:"column:result;type:jsonb"`
	ResultHash        *string            `gorm:"column:result_hash"`
	RetrievalSummary  *conversationJSONB `gorm:"column:retrieval_summary;type:jsonb"`
	Version           int64              `gorm:"column:version"`
	CreatedAt         time.Time          `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt         time.Time          `gorm:"column:updated_at;autoUpdateTime:false"`
	PublishedAt       *time.Time         `gorm:"column:published_at"`
}

func (answerModel) TableName() string { return "agent.answer" }

// feedbackModel 映射只追加的答案反馈及请求身份。
type feedbackModel struct {
	ID             string    `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID    string    `gorm:"column:workspace_id;type:uuid"`
	AnswerID       string    `gorm:"column:answer_id;type:uuid"`
	FeedbackType   string    `gorm:"column:feedback_type"`
	CitationID     *string   `gorm:"column:citation_id"`
	Comment        *string   `gorm:"column:comment"`
	IdempotencyKey string    `gorm:"column:idempotency_key"`
	RequestHash    string    `gorm:"column:request_hash"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (feedbackModel) TableName() string { return "agent.answer_feedback" }

// draftSessionModel 映射草稿代际、字节预算和数据库时间控制的 TTL。
type draftSessionModel struct {
	ID            string     `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID   string     `gorm:"column:workspace_id;type:uuid"`
	AnswerID      string     `gorm:"column:answer_id;type:uuid"`
	WorkflowRunID string     `gorm:"column:workflow_run_id;type:uuid"`
	NodeRunID     string     `gorm:"column:node_run_id;type:uuid"`
	NodeAttemptID string     `gorm:"column:node_attempt_id;type:uuid"`
	AttemptNo     int        `gorm:"column:attempt_no"`
	LeaseOwner    string     `gorm:"column:lease_owner"`
	Generation    int64      `gorm:"column:generation"`
	Status        string     `gorm:"column:status"`
	NextSequence  int64      `gorm:"column:next_sequence"`
	TotalBytes    int        `gorm:"column:total_bytes"`
	ExpiresAt     time.Time  `gorm:"column:expires_at"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime:false"`
	CompletedAt   *time.Time `gorm:"column:completed_at"`
}

func (draftSessionModel) TableName() string { return "agent.answer_draft_session" }

// draftChunkModel 保存一条受限草稿 chunk，不自动更新时间或级联。
type draftChunkModel struct {
	SessionID string    `gorm:"column:session_id;type:uuid;primaryKey"`
	Sequence  int64     `gorm:"column:sequence;primaryKey"`
	Content   string    `gorm:"column:content"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (draftChunkModel) TableName() string { return "agent.answer_draft_chunk" }

// workspaceAnalysisSuccessProofModel 保存不可变成功 proof；正文保持 bytea 的精确字节。
type workspaceAnalysisSuccessProofModel struct {
	ID                        string    `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID               string    `gorm:"column:workspace_id;type:uuid"`
	AnalysisRunID             string    `gorm:"column:analysis_run_id;type:uuid"`
	AnswerID                  string    `gorm:"column:answer_id;type:uuid"`
	WorkflowRunID             string    `gorm:"column:workflow_run_id;type:uuid"`
	FinalizationNodeRunID     string    `gorm:"column:finalization_node_run_id;type:uuid"`
	FinalizationNodeAttemptID string    `gorm:"column:finalization_node_attempt_id;type:uuid"`
	CandidateID               string    `gorm:"column:candidate_id;type:uuid"`
	CandidateHash             string    `gorm:"column:candidate_hash"`
	GitReceiptID              string    `gorm:"column:git_receipt_id;type:uuid"`
	GitReceiptHash            string    `gorm:"column:git_receipt_hash"`
	ValidationReceiptID       string    `gorm:"column:validation_receipt_id;type:uuid"`
	ValidationReceiptHash     string    `gorm:"column:validation_receipt_hash"`
	ReviewModelResultID       string    `gorm:"column:review_model_result_id;type:uuid"`
	ReviewDocumentHash        string    `gorm:"column:review_document_hash"`
	PublishedDocument         []byte    `gorm:"column:published_document;type:bytea"`
	PublishedResultHash       string    `gorm:"column:published_result_hash"`
	PublishedBytes            int64     `gorm:"column:published_bytes"`
	CreatedAt                 time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (workspaceAnalysisSuccessProofModel) TableName() string {
	return "agent.workspace_analysis_publication_proof"
}

// workspaceAnalysisTerminationProofModel 显式保留不同终止原因的 nullable 事实。
type workspaceAnalysisTerminationProofModel struct {
	ID                       string     `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID              string     `gorm:"column:workspace_id;type:uuid"`
	AnalysisRunID            string     `gorm:"column:analysis_run_id;type:uuid"`
	AnswerID                 string     `gorm:"column:answer_id;type:uuid"`
	WorkflowRunID            string     `gorm:"column:workflow_run_id;type:uuid"`
	TerminalNodeRunID        string     `gorm:"column:terminal_node_run_id;type:uuid"`
	TerminalNodeAttemptID    *string    `gorm:"column:terminal_node_attempt_id;type:uuid"`
	Reason                   string     `gorm:"column:reason"`
	OperationID              *string    `gorm:"column:operation_id;type:uuid"`
	ArtifactKind             *string    `gorm:"column:artifact_kind"`
	ArtifactID               *string    `gorm:"column:artifact_id;type:uuid"`
	ArtifactHash             *string    `gorm:"column:artifact_hash"`
	RequestedModelCalls      *int       `gorm:"column:requested_model_calls"`
	RequestedToolCalls       *int       `gorm:"column:requested_tool_calls"`
	RequestedSourceReads     *int       `gorm:"column:requested_source_reads"`
	RequestedInputTokens     *int64     `gorm:"column:requested_input_tokens"`
	RequestedOutputTokens    *int64     `gorm:"column:requested_output_tokens"`
	RequestedCostMicrounits  *int64     `gorm:"column:requested_cost_microunits"`
	ReceiptFailureCode       *string    `gorm:"column:receipt_failure_code"`
	ReceiptFailureID         *string    `gorm:"column:receipt_failure_id;type:uuid"`
	ExpectedHash             *string    `gorm:"column:expected_hash"`
	ActualHash               *string    `gorm:"column:actual_hash"`
	PublishedModelRunID      *string    `gorm:"column:published_model_run_id;type:uuid"`
	PublishedDocument        []byte     `gorm:"column:published_document;type:bytea"`
	PublishedResultHash      string     `gorm:"column:published_result_hash"`
	PublishedBytes           int64      `gorm:"column:published_bytes"`
	CheckedAt                time.Time  `gorm:"column:checked_at"`
	CreatedAt                time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	DeadlineOperationKind    *string    `gorm:"column:deadline_operation_kind"`
	DeadlineOperationOrdinal *int       `gorm:"column:deadline_operation_ordinal"`
	RuntimeTerminalAt        *time.Time `gorm:"column:runtime_terminal_at"`
}

func (workspaceAnalysisTerminationProofModel) TableName() string {
	return "agent.workspace_analysis_termination_proof"
}
