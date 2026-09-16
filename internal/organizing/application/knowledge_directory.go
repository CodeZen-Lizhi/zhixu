package application

import (
	"context"

	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// KnowledgeDirectoryStatus 区分尚未产出画像的来源与画像能力或生成不可用的来源。
// 这两种状态都不允许调用方根据标题、标签或正文推断知识点。
type KnowledgeDirectoryStatus string

const (
	KnowledgeDirectoryAnalyzed    KnowledgeDirectoryStatus = "ANALYZED"
	KnowledgeDirectoryUnanalyzed  KnowledgeDirectoryStatus = "UNANALYZED"
	KnowledgeDirectoryUnavailable KnowledgeDirectoryStatus = "UNAVAILABLE"
	KnowledgeDirectoryUnrecorded  KnowledgeDirectoryStatus = "UNRECORDED"
)

// KnowledgePointKind 标识 ProfileRevision 中的不可变集合。
// 索引只有与 ProfileRevisionID 和 Kind 一起使用才有意义。
type KnowledgePointKind string

const (
	KnowledgePointKindKnowledgePoint KnowledgePointKind = "KNOWLEDGE_POINT"
	KnowledgePointKindExample        KnowledgePointKind = "EXAMPLE"
)

// KnowledgePointLocator 在对应 ProfileRevision 的生命周期内保持稳定。
// 后续 API 可用它定位提取出的事实，不会将可变的画像当前指针当作身份。
type KnowledgePointLocator struct {
	ProfileRevisionID foundation.ID      `json:"profile_revision_id"`
	Kind              KnowledgePointKind `json:"kind"`
	Index             int                `json:"index"`
}

// KnowledgeDirectoryPoint 是不可变 ProfileRevision 中有证据支持的知识点。
// SourceSpanIDs 是支持该知识点的精确解析片段。
type KnowledgeDirectoryPoint struct {
	Locator       KnowledgePointLocator `json:"locator"`
	Text          string                `json:"text"`
	SourceSpanIDs []foundation.ID       `json:"source_span_ids"`
}

// SourceKnowledgeDirectoryQuery 精确选择一个由 Workspace 管理的来源版本。
// 此查询不提供正文查询或标签回退。
type SourceKnowledgeDirectoryQuery struct {
	WorkspaceID     foundation.ID
	SourceVersionID foundation.ID
}

// SourceKnowledgeDirectory 投影一个不可变 ProfileRevision。
// 画像内容是候选元数据，不是正式知识声明或归属分类。
type SourceKnowledgeDirectory struct {
	WorkspaceID       foundation.ID                    `json:"workspace_id"`
	SourceVersionID   foundation.ID                    `json:"source_version_id"`
	Status            KnowledgeDirectoryStatus         `json:"status"`
	ProfileStatus     capturedomain.ProfileStatus      `json:"profile_status,omitempty"`
	ProfileRevisionID foundation.ID                    `json:"profile_revision_id,omitempty"`
	ParseProjectionID foundation.ID                    `json:"parse_projection_id,omitempty"`
	Summary           string                           `json:"summary,omitempty"`
	Topics            []capturedomain.ProfileCandidate `json:"topics,omitempty"`
	Terms             []capturedomain.ProfileCandidate `json:"terms,omitempty"`
	Points            []KnowledgeDirectoryPoint        `json:"points,omitempty"`
}

// SynthesisKnowledgePointProjection 保留请求中的不可变合成引用，
// 只投影证据包含其精确片段的知识点。
type SynthesisKnowledgePointProjection struct {
	Reference domain.SynthesisSourceRef `json:"reference"`
	Directory SourceKnowledgeDirectory  `json:"directory"`
	Points    []KnowledgeDirectoryPoint `json:"points"`
}

// KnowledgeDirectoryReader 为后续 HTTP 组装提供只读的来源画像投影，
// 不会触发画像生成。
type KnowledgeDirectoryReader interface {
	GetSourceKnowledgeDirectory(context.Context, SourceKnowledgeDirectoryQuery) (SourceKnowledgeDirectory, error)
	ProjectSynthesisKnowledgePoints(context.Context, domain.SynthesisSourceRef) (SynthesisKnowledgePointProjection, error)
}

type SynthesisKnowledgeBindingQuery struct {
	NoteID     foundation.ID
	RevisionID foundation.ID
	Reference  domain.SynthesisSourceRef
}

// 空值表示已保存修订没有画像快照，不能因此在查询时替换为当前画像。
// 来源行缺失属于错误。
type SynthesisKnowledgeBindingReader interface {
	ReadSynthesisKnowledgeBinding(context.Context, SynthesisKnowledgeBindingQuery) (foundation.ID, error)
}

type SynthesisKnowledgeSnapshotReader interface {
	ProjectSynthesisKnowledgeSnapshot(context.Context, domain.SynthesisSourceRef, foundation.ID) (SynthesisKnowledgePointProjection, error)
}
