package domain

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	DefaultLimit         = 25
	MaxLimit             = 100
	MaxDepth             = 3
	DefaultPathDepth     = 6
	MaxPathDepth         = 8
	MaxNodes             = 500
	MaxEdges             = 1000
	DefaultEvidenceLimit = 20
	MaxSearchLimit       = 50
)

// TraversalDirection 表示相对查询前进方向的边遍历方式。
type TraversalDirection string

const (
	TraversalBoth     TraversalDirection = "BOTH"
	TraversalOutbound TraversalDirection = "OUTBOUND"
	TraversalInbound  TraversalDirection = "INBOUND"
)

// EdgeTraversal 表示响应中一次遍历相对 canonical Relation 的实际方向。
type EdgeTraversal string

const (
	EdgeTraversalForward EdgeTraversal = "FORWARD"
	EdgeTraversalReverse EdgeTraversal = "REVERSE"
)

// TopicNode 是 Graph 中由类型判别的 Topic 只读节点。
type TopicNode struct {
	Ref         knowledge.NodeRef
	WorkspaceID foundation.ID
	Name        string
	Description string
	Status      knowledge.TopicStatus
	Version     int64
	UpdatedAt   time.Time
}

// ClaimNode 是 Graph 中由类型判别的 Claim 只读节点。
type ClaimNode struct {
	Ref           knowledge.NodeRef
	WorkspaceID   foundation.ID
	Statement     string
	Status        knowledge.ClaimStatus
	Confidence    *float64
	Applicability knowledge.Applicability
	Version       int64
	UpdatedAt     time.Time
}

// GraphNode 是恰好包含一种 Topic 或 Claim 的判别联合。
type GraphNode struct {
	Topic *TopicNode
	Claim *ClaimNode
}

// Ref 返回判别节点的 Knowledge NodeRef。
func (node GraphNode) Ref() knowledge.NodeRef {
	if node.Topic != nil && node.Claim == nil {
		return node.Topic.Ref
	}
	if node.Claim != nil && node.Topic == nil {
		return node.Claim.Ref
	}
	return knowledge.NodeRef{}
}

// WorkspaceID 返回判别节点所属 Workspace。
func (node GraphNode) WorkspaceID() foundation.ID {
	if node.Topic != nil && node.Claim == nil {
		return node.Topic.WorkspaceID
	}
	if node.Claim != nil && node.Topic == nil {
		return node.Claim.WorkspaceID
	}
	return ""
}

// GraphEdge 是 canonical Relation 的只读投影及实际遍历方向。
type GraphEdge struct {
	RelationID          foundation.ID
	WorkspaceID         foundation.ID
	Source              knowledge.NodeRef
	Target              knowledge.NodeRef
	Type                knowledge.RelationType
	Status              knowledge.RelationStatus
	Traversal           EdgeTraversal
	Confidence          *float64
	Version             int64
	EvidenceCount       int
	EvidenceFingerprint string
	EvidenceHref        string
	UpdatedAt           time.Time
}

// GraphFilter 是所有图查询共享的显式过滤条件。
type GraphFilter struct {
	NodeTypes             []knowledge.NodeType
	RelationTypes         []knowledge.RelationType
	TopicIDs              []foundation.ID
	RelationStatuses      []knowledge.RelationStatus
	ClaimStatuses         []knowledge.ClaimStatus
	ClaimMinConfidence    *float64
	RelationMinConfidence *float64
	UpdatedAfter          *time.Time
}

// GlobalRequest 是有界 Topic cluster 查询请求。
type GlobalRequest struct {
	WorkspaceID foundation.ID
	Filter      GraphFilter
	Limit       int
}

// GlobalCluster 是过滤后 Topic cluster 的稳定聚合摘要。
type GlobalCluster struct {
	Topic                 TopicNode
	DirectClaimCount      int
	IncidentRelationCount int
	ClusterScore          int
	UpdatedAt             time.Time
}

// GlobalPage 是有界 Topic cluster 页面。
type GlobalPage struct {
	WorkspaceID foundation.ID
	Clusters    []GlobalCluster
	Meta        PageMeta
}

// NodeSearchRequest 是服务端有界节点搜索请求。
type NodeSearchRequest struct {
	WorkspaceID foundation.ID
	Query       string
	Limit       int
}

// NodeSearchMatchKind 是节点搜索的规范匹配级别。
type NodeSearchMatchKind string

const (
	NodeSearchExact  NodeSearchMatchKind = "EXACT"
	NodeSearchPrefix NodeSearchMatchKind = "PREFIX"
)

// NodeSearchMatch 是带确定性匹配级别的节点摘要。
type NodeSearchMatch struct {
	Kind NodeSearchMatchKind
	Node GraphNode
}

// NodeSearchResult 是按匹配优先级稳定排序的节点摘要。
type NodeSearchResult struct {
	WorkspaceID foundation.ID
	Matches     []NodeSearchMatch
}

// NeighborhoodRequest 是有界局部图请求。
type NeighborhoodRequest struct {
	WorkspaceID foundation.ID
	Center      knowledge.NodeRef
	Depth       int
	Limit       int
	Direction   TraversalDirection
	Filter      GraphFilter
	MaxNodes    int
	MaxEdges    int
}

// PathRequest 是有界最短路径请求。
type PathRequest struct {
	WorkspaceID   foundation.ID
	From, To      knowledge.NodeRef
	Direction     TraversalDirection
	RelationTypes []knowledge.RelationType
	MaxDepth      int
	MaxVisited    int
}

// PageMeta 描述结果完整性和有界截断状态。
type PageMeta struct {
	Fingerprint string
	NextCursor  string
	Complete    bool
	Truncated   bool
	Reason      string
}

// Neighborhood 是中心节点的一至三跳完整闭包快照。
type Neighborhood struct {
	WorkspaceID    foundation.ID
	Center         knowledge.NodeRef
	Nodes          []GraphNode
	Edges          []GraphEdge
	BoundaryNodes  []knowledge.NodeRef
	LayerCounts    []int
	CompletedDepth int
	Meta           PageMeta
}

// PathStatus 表示最短路径是否存在。
type PathStatus string

const (
	PathFound    PathStatus = "found"
	PathNotFound PathStatus = "not_found"
)

// PathResult 是已证明最短的连续路径或明确无路径结果。
type PathResult struct {
	WorkspaceID            foundation.ID
	From, To               knowledge.NodeRef
	Status                 PathStatus
	Nodes                  []GraphNode
	Edges                  []GraphEdge
	HopCount               int
	ExploredNodes          int
	CommonTopicSuggestions []TopicNode
}

// RelationDetail 是 Relation 本体及 Evidence 摘要的只读详情。
type RelationDetail struct {
	Edge         GraphEdge
	Confirmation *knowledge.Confirmation
	Fingerprint  string
	ValidFrom    *time.Time
	ValidTo      *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// RelationEvidenceItem 是独立拥有 reason、applicability 与 provenance 的证据项。
type RelationEvidenceItem struct {
	ID            foundation.ID
	WorkspaceID   foundation.ID
	RelationID    foundation.ID
	Provenance    knowledge.ProvenanceRef
	Reason        string
	Applicability knowledge.Applicability
	Confirmation  *knowledge.Confirmation
	SourceHref    string
	SpanHref      string
	CreatedAt     time.Time
}

// RelationEvidencePage 是 Relation Evidence 的按需有界页面。
type RelationEvidencePage struct {
	WorkspaceID foundation.ID
	RelationID  foundation.ID
	Items       []RelationEvidenceItem
	Meta        PageMeta
}
