package application

import (
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	QueryKindGlobal             = "global"
	QueryKindNeighborhoodDepth1 = "neighborhood_depth1"
	QueryKindRelationEvidence   = "relation_evidence"
)

type canonicalFilter struct {
	NodeTypes             []knowledge.NodeType       `json:"node_types"`
	RelationTypes         []knowledge.RelationType   `json:"relation_types"`
	TopicIDs              []foundation.ID            `json:"topic_ids"`
	RelationStatuses      []knowledge.RelationStatus `json:"relation_statuses"`
	ClaimStatuses         []knowledge.ClaimStatus    `json:"claim_statuses"`
	ClaimMinConfidence    *float64                   `json:"claim_min_confidence,omitempty"`
	RelationMinConfidence *float64                   `json:"relation_min_confidence,omitempty"`
	UpdatedAfter          *time.Time                 `json:"updated_after,omitempty"`
}

type canonicalGlobalRequest struct {
	WorkspaceID foundation.ID   `json:"workspace_id"`
	Filter      canonicalFilter `json:"filter"`
}

type canonicalNeighborhoodDepth1Request struct {
	WorkspaceID foundation.ID                  `json:"workspace_id"`
	Center      knowledge.NodeRef              `json:"center"`
	Direction   graphdomain.TraversalDirection `json:"direction"`
	Filter      canonicalFilter                `json:"filter"`
	MaxNodes    int                            `json:"max_nodes"`
	MaxEdges    int                            `json:"max_edges"`
}

type canonicalRelationEvidenceRequest struct {
	WorkspaceID foundation.ID `json:"workspace_id"`
	RelationID  foundation.ID `json:"relation_id"`
}

// CanonicalGlobalRequestHash 对全局查询的语义字段规范化；页大小和分页位置由 cursor 独立绑定。
func CanonicalGlobalRequestHash(request graphdomain.GlobalRequest) (string, error) {
	return hashCanonicalCursorValue(canonicalGlobalRequest{WorkspaceID: request.WorkspaceID, Filter: normalizeFilter(request.Filter)})
}

// CanonicalNeighborhoodDepth1RequestHash 对一跳局部图语义规范化，不包含页大小或分页位置。
func CanonicalNeighborhoodDepth1RequestHash(request graphdomain.NeighborhoodRequest) (string, error) {
	direction := request.Direction
	if direction == "" {
		direction = graphdomain.TraversalBoth
	}
	maxNodes, maxEdges := request.MaxNodes, request.MaxEdges
	if maxNodes == 0 {
		maxNodes = graphdomain.MaxNodes
	}
	if maxEdges == 0 {
		maxEdges = graphdomain.MaxEdges
	}
	return hashCanonicalCursorValue(canonicalNeighborhoodDepth1Request{
		WorkspaceID: request.WorkspaceID, Center: request.Center, Direction: direction,
		Filter: normalizeFilter(request.Filter), MaxNodes: maxNodes, MaxEdges: maxEdges,
	})
}

// CanonicalRelationEvidenceRequestHash 对 Evidence owner 规范化；limit 和分页位置不进入请求 hash。
func CanonicalRelationEvidenceRequestHash(workspaceID, relationID foundation.ID) (string, error) {
	return hashCanonicalCursorValue(canonicalRelationEvidenceRequest{WorkspaceID: workspaceID, RelationID: relationID})
}

func normalizeFilter(filter graphdomain.GraphFilter) canonicalFilter {
	nodeTypes := sortedStrings(filter.NodeTypes)
	relationTypes := sortedStrings(filter.RelationTypes)
	topicIDs := sortedStrings(filter.TopicIDs)
	relationStatuses := sortedStrings(filter.RelationStatuses)
	claimStatuses := sortedStrings(filter.ClaimStatuses)
	if len(nodeTypes) == 0 {
		nodeTypes = []knowledge.NodeType{knowledge.NodeTypeClaim, knowledge.NodeTypeTopic}
	}
	if len(relationTypes) == 0 {
		relationTypes = []knowledge.RelationType{
			knowledge.RelationBelongsTo, knowledge.RelationCites, knowledge.RelationComplements,
			knowledge.RelationConflictsWith, knowledge.RelationDerivedFrom, knowledge.RelationDuplicates,
			knowledge.RelationImpacts, knowledge.RelationPrerequisiteOf, knowledge.RelationSupports,
			knowledge.RelationVersionOf,
		}
		sort.Slice(relationTypes, func(left, right int) bool { return relationTypes[left] < relationTypes[right] })
	}
	if len(relationStatuses) == 0 {
		relationStatuses = []knowledge.RelationStatus{knowledge.RelationStatusConfirmed}
	}
	if len(claimStatuses) == 0 {
		claimStatuses = []knowledge.ClaimStatus{knowledge.ClaimStatusConfirmed, knowledge.ClaimStatusDisputed}
	}
	updatedAfter := filter.UpdatedAfter
	if updatedAfter != nil {
		utc := updatedAfter.UTC()
		updatedAfter = &utc
	}
	return canonicalFilter{
		NodeTypes: nodeTypes, RelationTypes: relationTypes, TopicIDs: topicIDs,
		RelationStatuses: relationStatuses, ClaimStatuses: claimStatuses,
		ClaimMinConfidence: filter.ClaimMinConfidence, RelationMinConfidence: filter.RelationMinConfidence,
		UpdatedAfter: updatedAfter,
	}
}

func sortedStrings[T ~string](values []T) []T {
	result := append([]T(nil), values...)
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	if result == nil {
		return []T{}
	}
	return result
}
