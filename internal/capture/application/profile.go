package application

import (
	"context"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxProfileSourceChunks 限制一次画像生成可加载的结构化 Chunk 数量。
	MaxProfileSourceChunks = 500
	// MaxProfileChunkBytes 限制单个 Chunk 进入模型边界的 UTF-8 字节数。
	MaxProfileChunkBytes = 128 * 1024
	// MaxProfileBatchSize limits one owner-bound Profile hydration request.
	MaxProfileBatchSize = 100
)

// ProfileGenerationRequest 冻结一次画像生成所需的 Capture、投影与 Workflow 身份。
type ProfileGenerationRequest struct {
	ProfileID             foundation.ID
	Capture               domain.Capture
	Attempt               domain.ProcessingAttempt
	ParseProjectionID     foundation.ID
	IndexVersionID        foundation.ID
	WorkflowRunID         foundation.ID
	NodeRunID             foundation.ID
	NodeAttemptID         foundation.ID
	ModelSettingsRevision *int64
}

// ProfileGenerationResult 指向已持久化的 READY 画像 Revision。
type ProfileGenerationResult struct {
	ProfileID  foundation.ID
	RevisionID foundation.ID
}

// ProfileGenerator 通过冻结 Agent runtime 生成并持久化证据绑定画像。
type ProfileGenerator interface {
	Generate(context.Context, ProfileGenerationRequest) (ProfileGenerationResult, error)
}

// ProfileLookup 用 Workspace、Capture 与不可变 Source Version 查找画像事实。
type ProfileLookup struct {
	WorkspaceID     foundation.ID
	CaptureID       foundation.ID
	SourceVersionID foundation.ID
}

// ProfileQuery 按 Workspace 与不可变 Source Version 读取画像详情。
type ProfileQuery struct {
	WorkspaceID     foundation.ID
	SourceVersionID foundation.ID
}

// ProfileBatchQuery reads existing Profile facts for a bounded set of Source Versions.
// Missing optional profiles are omitted; returned views are ordered by SourceVersionID.
type ProfileBatchQuery struct {
	WorkspaceID      foundation.ID
	SourceVersionIDs []foundation.ID
}

// ProfileContract 冻结决定一个 Profile Revision 是否可精确重放的全部派生版本。
type ProfileContract struct {
	ParseProjectionID     foundation.ID
	IndexVersionID        foundation.ID
	ModelSettingsRevision *int64
	PromptVersion         string
	SchemaVersion         string
}

// ReadyProfile 指向已验证的 READY 画像及其当前不可变 Revision。
type ReadyProfile struct {
	ProfileID  foundation.ID
	RevisionID foundation.ID
}

// ProfileSourceChunk 是从冻结 ingestion/retrieval 投影读取的有序证据片段。
type ProfileSourceChunk struct {
	ChunkID      foundation.ID
	SourceSpanID foundation.ID
	Sequence     int
	HeadingPath  []string
	Content      string
}

// ProfileSourceSnapshot 固化画像生成使用的 Parse Projection、Index Version 与 Chunk 集合。
type ProfileSourceSnapshot struct {
	WorkspaceID        foundation.ID
	SourceVersionID    foundation.ID
	ParseProjectionID  foundation.ID
	IndexVersionID     foundation.ID
	EmbeddingVersionID *foundation.ID
	Chunks             []ProfileSourceChunk
}

// PrepareProfileCommand 为一个 Workflow Node Attempt 创建或恢复画像生成 Attempt。
type PrepareProfileCommand struct {
	ProfileLookup
	Contract           ProfileContract
	RequestedProfileID foundation.ID
	ProfileAttemptID   foundation.ID
	WorkflowRunID      foundation.ID
	NodeRunID          foundation.ID
	NodeAttemptID      foundation.ID
	StartedAt          time.Time
}

// PreparedProfile 返回画像生成 Attempt 的真实持久化身份；ReadyRevisionID 非空表示直接重放。
type PreparedProfile struct {
	ProfileID             foundation.ID
	ProfileAttemptID      foundation.ID
	ProfileAttemptNumber  int
	ProfileAttemptVersion int64
	StartedAt             time.Time
	ModelRunID            foundation.ID
	ReadyRevisionID       foundation.ID
}

// BindProfileModelRunCommand 在 Provider 调用前把画像 Attempt 绑定到 Agent Model Run。
type BindProfileModelRunCommand struct {
	WorkspaceID                   foundation.ID
	ProfileID                     foundation.ID
	ProfileAttemptID              foundation.ID
	ExpectedProfileAttemptVersion int64
	ModelRunID                    foundation.ID
}

// CompleteProfileCommand 原子写入画像 Revision/Evidence、推进 current pointer 并终结 Model Run。
type CompleteProfileCommand struct {
	WorkspaceID                   foundation.ID
	ProfileAttemptID              foundation.ID
	ExpectedProfileAttemptVersion int64
	ExpectedModelRunVersion       int64
	Revision                      domain.ProfileRevision
	Evidence                      []domain.ProfileEvidence
	CompletedAt                   time.Time
}

// FailProfileCommand 原子终结画像 Attempt/Profile 与可选的已创建 Agent Model Run。
type FailProfileCommand struct {
	WorkspaceID                   foundation.ID
	ProfileID                     foundation.ID
	ProfileAttemptID              foundation.ID
	ExpectedProfileAttemptVersion int64
	ModelRunID                    foundation.ID
	ExpectedModelRunVersion       int64
	ModelRunStatus                agentdomain.ModelRunStatus
	ProfileStatus                 domain.ProfileStatus
	ErrorCode                     string
	Retryable                     bool
	CompletedAt                   time.Time
}

// ProfileView 是服务端可恢复的画像状态及其可选当前不可变 Revision。
type ProfileView struct {
	Profile  domain.Profile
	Revision *domain.ProfileRevision
	Evidence []domain.ProfileEvidence
}

// ProfileReader 按 Workspace 与 Source Version 读取画像权威状态。
type ProfileReader interface {
	GetProfile(context.Context, ProfileQuery) (ProfileView, error)
}

// ProfileBatchReader hydrates Profile candidates without a per-material query loop.
type ProfileBatchReader interface {
	GetProfiles(context.Context, ProfileBatchQuery) ([]ProfileView, error)
}

// ProfileGenerationRepository 负责冻结证据读取与 Profile/Agent 跨聚合原子终结。
type ProfileGenerationRepository interface {
	LookupReady(context.Context, ProfileLookup, ProfileContract) (ReadyProfile, bool, error)
	LoadSource(context.Context, ProfileLookup, foundation.ID, foundation.ID) (ProfileSourceSnapshot, error)
	PrepareProfile(context.Context, PrepareProfileCommand) (PreparedProfile, error)
	BindProfileModelRun(context.Context, BindProfileModelRunCommand) (PreparedProfile, error)
	CompleteProfile(context.Context, CompleteProfileCommand) (ReadyProfile, error)
	FailProfile(context.Context, FailProfileCommand) error
}
