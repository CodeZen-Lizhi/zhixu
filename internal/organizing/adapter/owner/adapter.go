// Package owner 将各领域公开只读 API 适配为 Organizing 材料端口。
package owner

import (
	"context"
	"errors"
	"reflect"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	// ErrorCodeOwnerDependencyUnavailable 表示材料 owner 读取依赖未完整组装。
	ErrorCodeOwnerDependencyUnavailable = "ORGANIZING_OWNER_DEPENDENCY_UNAVAILABLE"
	// ErrorCodeOwnerCapabilityUnavailable 表示公开 owner API 无法证明请求的精确身份。
	ErrorCodeOwnerCapabilityUnavailable = "ORGANIZING_OWNER_CAPABILITY_UNAVAILABLE"
	// ErrorCodeOwnerResultInvalid 表示 owner 返回了越过 Workspace 或批量请求的结果。
	ErrorCodeOwnerResultInvalid = "ORGANIZING_OWNER_RESULT_INVALID"
)

// SearchReader 是 Retrieval 的有界公开检索边界。
type SearchReader interface {
	// Search 执行 Workspace-scoped 的有界检索。
	Search(context.Context, retrievaldomain.SearchRequest) (retrievaldomain.SearchResult, error)
}

// EvidenceReader 是 Retrieval 的批量 Source Version 与精确 Citation 边界。
type EvidenceReader interface {
	// GetSourceVersions 按请求顺序批量读取不可变 Source Version。
	GetSourceVersions(context.Context, foundation.ID, []foundation.ID) ([]retrievaldomain.SourceVersionReference, error)
	// OpenCitationEvidenceBatch 按完整 Citation tuple 批量复核 Evidence。
	OpenCitationEvidenceBatch(context.Context, []retrievaldomain.CitationReferenceQuery) ([]retrievalapp.OpenedCitationEvidence, error)
	// ResolveProvenanceCitations 将正式知识来源解析为当前可验证 Citation。
	ResolveProvenanceCitations(context.Context, []retrievaldomain.ProvenanceReferenceQuery) ([]retrievaldomain.CitationReferenceQuery, error)
}

// ProfileReader 是 Capture 的有界 Profile 批量读取边界。
type ProfileReader interface {
	// GetProfiles 批量读取已存在的当前 Profile Revision。
	GetProfiles(context.Context, captureapp.ProfileBatchQuery) ([]captureapp.ProfileView, error)
}

// KnowledgeReader 是 Knowledge 的有界正式 Claim 读取边界。
type KnowledgeReader interface {
	// GetClaims 批量读取正式 Claim 与全部来源。
	GetClaims(context.Context, knowledgeapp.GetClaimsQuery) ([]knowledgedomain.ClaimWithSources, error)
}

// CollectionReader 暴露不可变 Collection 扫描绑定而不泄漏其 Repository。
type CollectionReader interface {
	// Get 按 Workspace 读取一个 Smart Collection。
	Get(context.Context, foundation.ID, foundation.ID) (collectionapp.Collection, error)
	// PlanDurableScan 冻结 Collection version、query 与 read-model revision。
	PlanDurableScan(context.Context, foundation.ID, foundation.ID) (collectionapp.DurableScanBinding, error)
	// ReadDurableScanPage 读取固定 binding 的有界成员页。
	ReadDurableScanPage(context.Context, collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error)
	// SearchCollections 返回 Workspace 内按名称匹配的 Active Smart Collection。
	SearchCollections(context.Context, collectionapp.CollectionSearchQuery) ([]collectionapp.Collection, error)
}

// AuthoringReader 暴露由 owner 校验过的精确不可变 Document Revision 批读。
type AuthoringReader interface {
	// GetArticleRevisions 按请求顺序读取 Workspace 内精确 Revision。
	GetArticleRevisions(context.Context, authoringapp.ArticleRevisionBatchQuery) ([]authoringapp.ArticleRevisionSnapshot, error)
	// SearchArticleRevisions 搜索 Document 并返回其最新不可变 Revision identity。
	SearchArticleRevisions(context.Context, authoringapp.ArticleRevisionSearchQuery) ([]authoringapp.ArticleRevisionSearchHit, error)
}

// ClaimSearchReader 提供 Workspace-scoped 的正式 Claim 文本搜索。
type ClaimSearchReader interface {
	// SearchNodes 返回有界 Graph Claim/Topic 摘要；Organizing 只接收 Claim。
	SearchNodes(context.Context, graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error)
}

// Dependencies 是 Organizing 适配器依赖的公开 owner 服务。
type Dependencies struct {
	// Search 提供 Hybrid Search 候选与 Active Index identity。
	Search SearchReader
	// Evidence 批量读取 Source Version 并打开完整 Citation。
	Evidence EvidenceReader
	// Profiles 批量读取 Source Version 当前画像。
	Profiles ProfileReader
	// Knowledge 批量读取正式 Claim 及其来源。
	Knowledge KnowledgeReader
	// Collections 读取 Smart Collection 与 durable binding。
	Collections CollectionReader
	// Authoring 批量读取精确 Document Revision。
	Authoring AuthoringReader
	// Claims 搜索正式知识 Claim identity。
	Claims ClaimSearchReader
}

// Adapter 实现 Organizing 的 MaterialResolver 与 SuggestionProvider。
type Adapter struct {
	dependencies Dependencies
}

var _ organizingapp.MaterialResolver = (*Adapter)(nil)
var _ organizingapp.SuggestionProvider = (*Adapter)(nil)
var _ organizingapp.MaterialSearchProvider = (*Adapter)(nil)

// New 创建覆盖全部 owner 读取边界且失败关闭的适配器。
func New(dependencies Dependencies) (*Adapter, error) {
	if nilDependency(dependencies.Search) || nilDependency(dependencies.Evidence) ||
		nilDependency(dependencies.Profiles) || nilDependency(dependencies.Knowledge) ||
		nilDependency(dependencies.Collections) || nilDependency(dependencies.Authoring) {
		return nil, dependencyUnavailable("organizing owner dependencies are incomplete")
	}
	if nilDependency(dependencies.Claims) {
		return nil, dependencyUnavailable("organizing claim search dependency is incomplete")
	}
	return &Adapter{dependencies: dependencies}, nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	ref := reflect.ValueOf(value)
	switch ref.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return ref.IsNil()
	default:
		return false
	}
}

func dependencyUnavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeOwnerDependencyUnavailable, true, errors.New(message))
}

func capabilityUnavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeOwnerCapabilityUnavailable, false, errors.New(message))
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, organizingapp.ErrorCodeRequestInvalid, false, errors.New(message))
}

func inconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeOwnerResultInvalid, false, errors.New(message))
}

func stale(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, organizingapp.ErrorCodeMaterialStale, false, errors.New(message))
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
