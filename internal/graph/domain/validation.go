package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// ValidateGlobalRequest 校验全局查询的 Workspace、过滤器和页大小。
func ValidateGlobalRequest(request GlobalRequest) error {
	if !validID(request.WorkspaceID) || request.Limit < 1 || request.Limit > MaxLimit {
		return invalid("global request workspace or limit is invalid")
	}
	return ValidateFilter(request.Filter)
}

// ValidateGlobalPage 校验 cluster 分数、Workspace、唯一性及冻结排序。
func ValidateGlobalPage(request GlobalRequest, page GlobalPage) error {
	if err := ValidateGlobalRequest(request); err != nil {
		return err
	}
	if page.WorkspaceID != request.WorkspaceID || len(page.Clusters) > request.Limit || validatePageMeta(page.Meta) != nil {
		return inconsistent("global page binding is inconsistent")
	}
	if err := ValidateGlobalWindow(request, page.Clusters, request.Limit); err != nil {
		return err
	}
	return nil
}

// ValidateGlobalWindow 校验 Adapter 返回的完整有界 cluster 窗口。
func ValidateGlobalWindow(request GlobalRequest, clusters []GlobalCluster, maxItems int) error {
	if err := ValidateGlobalRequest(request); err != nil {
		return err
	}
	if maxItems < 1 || len(clusters) > maxItems {
		return inconsistent("global window exceeds application bound")
	}
	seen, previousKey := map[foundation.ID]struct{}{}, ""
	previousScore := int(^uint(0) >> 1)
	var previousUpdated time.Time
	for i, cluster := range clusters {
		if cluster.Topic.WorkspaceID != request.WorkspaceID || cluster.Topic.Status != knowledge.TopicStatusActive || ValidateGraphNode(GraphNode{Topic: &cluster.Topic}) != nil || cluster.DirectClaimCount < 0 || cluster.IncidentRelationCount < 0 || cluster.ClusterScore != cluster.DirectClaimCount+cluster.IncidentRelationCount || cluster.UpdatedAt.IsZero() {
			return inconsistent("global cluster is invalid")
		}
		if _, duplicate := seen[cluster.Topic.Ref.ID]; duplicate {
			return inconsistent("global clusters are duplicated")
		}
		if len(request.Filter.TopicIDs) > 0 && !containsID(request.Filter.TopicIDs, cluster.Topic.Ref.ID) {
			return inconsistent("global cluster violates topic filter")
		}
		if request.Filter.UpdatedAfter != nil && !cluster.UpdatedAt.After(*request.Filter.UpdatedAfter) {
			return inconsistent("global cluster violates updated filter")
		}
		key := string(cluster.Topic.Ref.ID)
		if i > 0 && (cluster.ClusterScore > previousScore || (cluster.ClusterScore == previousScore && cluster.UpdatedAt.After(previousUpdated)) || (cluster.ClusterScore == previousScore && cluster.UpdatedAt.Equal(previousUpdated) && key <= previousKey)) {
			return inconsistent("global clusters are not stably ordered")
		}
		seen[cluster.Topic.Ref.ID], previousScore, previousUpdated, previousKey = struct{}{}, cluster.ClusterScore, cluster.UpdatedAt, key
	}
	if len(request.Filter.NodeTypes) > 0 && !containsNodeType(request.Filter.NodeTypes, knowledge.NodeTypeTopic) && len(clusters) != 0 {
		return inconsistent("global result violates node type filter")
	}
	return nil
}

// ValidateNodeSearchRequest 校验规范查询字节长度和结果上限。
func ValidateNodeSearchRequest(request NodeSearchRequest) error {
	query := strings.TrimSpace(request.Query)
	if !validID(request.WorkspaceID) || query != request.Query || !utf8.ValidString(query) || len([]byte(query)) < 2 || len([]byte(query)) > 256 || request.Limit < 1 || request.Limit > MaxSearchLimit {
		return invalid("node search request is invalid")
	}
	return nil
}

// ValidateNodeSearchResult 校验搜索结果的 Workspace、上限、唯一性和稳定顺序。
func ValidateNodeSearchResult(request NodeSearchRequest, result NodeSearchResult) error {
	if err := ValidateNodeSearchRequest(request); err != nil {
		return err
	}
	if result.WorkspaceID != request.WorkspaceID || len(result.Matches) > request.Limit {
		return inconsistent("node search result binding is inconsistent")
	}
	seen := make(map[string]struct{}, len(result.Matches))
	previousKind, previousKey := NodeSearchMatchKind(""), ""
	for _, match := range result.Matches {
		if match.Kind != NodeSearchExact && match.Kind != NodeSearchPrefix || ValidateGraphNode(match.Node) != nil || match.Node.WorkspaceID() != request.WorkspaceID {
			return inconsistent("node search match is invalid")
		}
		key := refKey(match.Node.Ref())
		if _, duplicate := seen[key]; duplicate || (previousKind != "" && (searchKindRank(match.Kind) < searchKindRank(previousKind) || (match.Kind == previousKind && key <= previousKey))) {
			return inconsistent("node search matches must be unique and stably ordered")
		}
		seen[key], previousKind, previousKey = struct{}{}, match.Kind, key
	}
	return nil
}

// ValidateGraphNode 校验判别类型、Workspace、生命周期和只读字段。
func ValidateGraphNode(node GraphNode) error {
	if (node.Topic == nil) == (node.Claim == nil) {
		return inconsistent("graph node must contain exactly one discriminated value")
	}
	ref, workspaceID := node.Ref(), node.WorkspaceID()
	if !validRef(ref) || !validID(workspaceID) {
		return inconsistent("graph node identity is invalid")
	}
	if node.Topic != nil {
		if ref.Type != knowledge.NodeTypeTopic || strings.TrimSpace(node.Topic.Name) == "" || !validTopicStatus(node.Topic.Status) || node.Topic.Version <= 0 || node.Topic.UpdatedAt.IsZero() {
			return inconsistent("topic graph node is invalid")
		}
		return nil
	}
	if ref.Type != knowledge.NodeTypeClaim || strings.TrimSpace(node.Claim.Statement) == "" || !utf8.ValidString(node.Claim.Statement) || len([]byte(node.Claim.Statement)) > 512 || !validClaimStatus(node.Claim.Status) || node.Claim.Version <= 0 || node.Claim.UpdatedAt.IsZero() || invalidConfidence(node.Claim.Confidence) || knowledge.ValidateApplicability(node.Claim.Applicability) != nil {
		return inconsistent("claim graph node is invalid")
	}
	return nil
}

// ValidateFilter 校验枚举白名单、唯一性、置信度和 Topic 范围。
func ValidateFilter(filter GraphFilter) error {
	if invalidConfidence(filter.ClaimMinConfidence) || invalidConfidence(filter.RelationMinConfidence) {
		return invalid("confidence filter must be finite and within zero and one")
	}
	if !uniqueNodeTypes(filter.NodeTypes) || !uniqueRelationTypes(filter.RelationTypes) || !uniqueStatuses(filter.RelationStatuses) || !uniqueClaimStatuses(filter.ClaimStatuses) || !uniqueIDs(filter.TopicIDs) {
		return invalid("filter values must be valid and unique")
	}
	return nil
}

// ValidateNeighborhoodRequest 校验局部图深度、分页、方向和硬预算。
func ValidateNeighborhoodRequest(request NeighborhoodRequest) error {
	if !validID(request.WorkspaceID) || !validRef(request.Center) || request.Depth < 1 || request.Depth > MaxDepth || request.Limit < 1 || request.Limit > MaxLimit || !validDirection(request.Direction) {
		return invalid("neighborhood request identity, depth, limit, or direction is invalid")
	}
	if request.MaxNodes < 1 || request.MaxNodes > MaxNodes || request.MaxEdges < 1 || request.MaxEdges > MaxEdges || request.MaxFrontier < 1 || request.MaxFrontier > MaxNodes {
		return invalid("neighborhood budget exceeds hard limits")
	}
	if len(request.Filter.NodeTypes) > 0 && !containsNodeType(request.Filter.NodeTypes, request.Center.Type) {
		return invalid("neighborhood center type is excluded by node filter")
	}
	if request.Center.Type == knowledge.NodeTypeTopic && len(request.Filter.TopicIDs) > 0 && !containsID(request.Filter.TopicIDs, request.Center.ID) {
		return invalid("neighborhood center is excluded by topic filter")
	}
	return ValidateFilter(request.Filter)
}

// ValidatePathRequest 校验路径端点、方向、深度、访问预算和关系类型。
func ValidatePathRequest(request PathRequest) error {
	if !validID(request.WorkspaceID) || !validRef(request.From) || !validRef(request.To) || request.From == request.To || !validDirection(request.Direction) || request.MaxDepth < 1 || request.MaxDepth > MaxPathDepth || request.MaxVisited < 2 || request.MaxVisited > MaxNodes {
		return invalid("path request identity, direction, depth, or budget is invalid")
	}
	if !uniqueRelationTypes(request.RelationTypes) {
		return invalid("path relation types must be valid and unique")
	}
	return nil
}

// ValidateNeighborhood 校验 Workspace、稳定顺序、唯一性、端点闭包和层级元数据。
func ValidateNeighborhood(request NeighborhoodRequest, result Neighborhood) error {
	if err := ValidateNeighborhoodRequest(request); err != nil {
		return err
	}
	if result.WorkspaceID != request.WorkspaceID || result.Center != request.Center || result.CompletedDepth < 0 || result.CompletedDepth > request.Depth || len(result.LayerCounts) != result.CompletedDepth || len(result.Nodes) > request.MaxNodes || len(result.Edges) > request.MaxEdges || validatePageMeta(result.Meta) != nil {
		return inconsistent("neighborhood metadata is inconsistent")
	}
	if result.Meta.Complete && result.CompletedDepth != request.Depth {
		return inconsistent("neighborhood completion state is inconsistent")
	}
	layerTotal := 0
	for _, count := range result.LayerCounts {
		if count < 0 {
			return inconsistent("neighborhood layer count is negative")
		}
		layerTotal += count
	}
	if layerTotal != len(result.Nodes)-1 {
		return inconsistent("neighborhood layer counts do not match hydrated additions")
	}
	nodes, err := validateNodes(request.WorkspaceID, result.Nodes, true)
	if err != nil {
		return err
	}
	if _, ok := nodes[refKey(request.Center)]; !ok {
		return inconsistent("neighborhood center is missing")
	}
	boundary := make(map[string]struct{}, len(result.BoundaryNodes))
	for _, ref := range result.BoundaryNodes {
		key := refKey(ref)
		if !validRef(ref) || key == "" {
			return inconsistent("boundary node is invalid")
		}
		if _, duplicate := boundary[key]; duplicate {
			return inconsistent("boundary nodes are duplicated")
		}
		if _, hydrated := nodes[key]; hydrated {
			return inconsistent("boundary node must not duplicate a hydrated node")
		}
		boundary[key] = struct{}{}
	}
	if err := validateEdges(request.WorkspaceID, result.Edges, nodes, boundary, true); err != nil {
		return err
	}
	return ValidateNeighborhoodResultFilters(request, result)
}

// ValidatePathResult 校验 found/not_found 分型、稳定闭包和逐边连续性。
func ValidatePathResult(request PathRequest, result PathResult) error {
	if err := ValidatePathRequest(request); err != nil {
		return err
	}
	if result.WorkspaceID != request.WorkspaceID || result.From != request.From || result.To != request.To || result.ExploredNodes < 0 || result.ExploredNodes > request.MaxVisited {
		return inconsistent("path result binding is inconsistent")
	}
	if result.Status == PathNotFound {
		if len(result.Nodes) != 0 || len(result.Edges) != 0 || result.HopCount != 0 {
			return inconsistent("not found path must not contain a partial path")
		}
		if len(result.CommonTopicSuggestions) > 5 {
			return inconsistent("too many common topic suggestions")
		}
		seen, previous := map[foundation.ID]struct{}{}, ""
		for _, topic := range result.CommonTopicSuggestions {
			key := string(topic.Ref.ID)
			if topic.WorkspaceID != request.WorkspaceID || topic.Status != knowledge.TopicStatusActive || ValidateGraphNode(GraphNode{Topic: &topic}) != nil {
				return inconsistent("common topic suggestion is invalid")
			}
			if _, duplicate := seen[topic.Ref.ID]; duplicate || (previous != "" && key <= previous) {
				return inconsistent("common topic suggestions must be unique and stably ordered")
			}
			seen[topic.Ref.ID], previous = struct{}{}, key
		}
		return nil
	}
	if result.Status != PathFound || result.HopCount < 1 || result.HopCount > request.MaxDepth || len(result.Edges) != result.HopCount || len(result.Nodes) != result.HopCount+1 || len(result.CommonTopicSuggestions) != 0 {
		return inconsistent("found path shape is inconsistent")
	}
	nodes, err := validateNodes(request.WorkspaceID, result.Nodes, false)
	if err != nil {
		return err
	}
	if err := validateEdges(request.WorkspaceID, result.Edges, nodes, nil, false); err != nil {
		return err
	}
	if result.Nodes[0].Ref() != request.From || result.Nodes[len(result.Nodes)-1].Ref() != request.To {
		return inconsistent("path endpoints are inconsistent")
	}
	for i, edge := range result.Edges {
		from, to := traversalEndpoints(edge)
		if result.Nodes[i].Ref() != from || result.Nodes[i+1].Ref() != to {
			return inconsistent("path edges are not continuous")
		}
	}
	return ValidatePathResultFilters(request, result)
}

// ValidateNeighborhoodResultFilters 校验 adapter 结果未绕过过滤器与遍历方向。
func ValidateNeighborhoodResultFilters(request NeighborhoodRequest, result Neighborhood) error {
	for _, node := range result.Nodes {
		if len(request.Filter.NodeTypes) > 0 && !containsNodeType(request.Filter.NodeTypes, node.Ref().Type) {
			return inconsistent("neighborhood node violates node type filter")
		}
		if node.Topic != nil {
			if node.Topic.Status != knowledge.TopicStatusActive {
				return inconsistent("neighborhood topic is not active")
			}
			if len(request.Filter.TopicIDs) > 0 && !containsID(request.Filter.TopicIDs, node.Topic.Ref.ID) {
				return inconsistent("neighborhood topic violates topic filter")
			}
		}
		if node.Claim != nil {
			if len(request.Filter.ClaimStatuses) == 0 && node.Claim.Status != knowledge.ClaimStatusConfirmed && node.Claim.Status != knowledge.ClaimStatusDisputed {
				return inconsistent("neighborhood claim violates default status filter")
			}
			if len(request.Filter.ClaimStatuses) > 0 && !containsClaimStatus(request.Filter.ClaimStatuses, node.Claim.Status) {
				return inconsistent("neighborhood claim violates status filter")
			}
			if request.Filter.ClaimMinConfidence != nil && (node.Claim.Confidence == nil || *node.Claim.Confidence < *request.Filter.ClaimMinConfidence) {
				return inconsistent("neighborhood claim violates confidence filter")
			}
		}
		if request.Filter.UpdatedAfter != nil && !nodeUpdatedAt(node).After(*request.Filter.UpdatedAfter) {
			return inconsistent("neighborhood node violates updated filter")
		}
	}
	for _, ref := range result.BoundaryNodes {
		if len(request.Filter.NodeTypes) > 0 && !containsNodeType(request.Filter.NodeTypes, ref.Type) {
			return inconsistent("neighborhood boundary violates node type filter")
		}
		if ref.Type == knowledge.NodeTypeTopic && len(request.Filter.TopicIDs) > 0 && !containsID(request.Filter.TopicIDs, ref.ID) {
			return inconsistent("neighborhood boundary violates topic filter")
		}
	}
	for _, edge := range result.Edges {
		if len(request.Filter.RelationTypes) > 0 && !containsRelationType(request.Filter.RelationTypes, edge.Type) {
			return inconsistent("neighborhood edge violates relation type filter")
		}
		if len(request.Filter.RelationStatuses) == 0 && edge.Status != knowledge.RelationStatusConfirmed {
			return inconsistent("neighborhood edge violates default confirmed status")
		}
		if len(request.Filter.RelationStatuses) > 0 && !containsRelationStatus(request.Filter.RelationStatuses, edge.Status) {
			return inconsistent("neighborhood edge violates status filter")
		}
		if request.Filter.RelationMinConfidence != nil && (edge.Confidence == nil || *edge.Confidence < *request.Filter.RelationMinConfidence) {
			return inconsistent("neighborhood edge violates confidence filter")
		}
		if request.Filter.UpdatedAfter != nil && !edge.UpdatedAt.After(*request.Filter.UpdatedAfter) {
			return inconsistent("neighborhood edge violates updated filter")
		}
		if !knowledge.IsSymmetricRelationType(edge.Type) && request.Direction == TraversalOutbound && edge.Traversal == EdgeTraversalReverse {
			return inconsistent("outbound neighborhood contains reverse traversal")
		}
		if !knowledge.IsSymmetricRelationType(edge.Type) && request.Direction == TraversalInbound && edge.Traversal == EdgeTraversalForward {
			return inconsistent("inbound neighborhood contains forward traversal")
		}
	}
	return nil
}

// ValidatePathResultFilters 校验路径遵守关系类型、正式状态与遍历方向。
func ValidatePathResultFilters(request PathRequest, result PathResult) error {
	for _, edge := range result.Edges {
		if edge.Status != knowledge.RelationStatusConfirmed {
			return inconsistent("path edge is not confirmed")
		}
		if len(request.RelationTypes) > 0 && !containsRelationType(request.RelationTypes, edge.Type) {
			return inconsistent("path edge violates relation type filter")
		}
		if !knowledge.IsSymmetricRelationType(edge.Type) && request.Direction == TraversalOutbound && edge.Traversal == EdgeTraversalReverse {
			return inconsistent("outbound path contains reverse traversal")
		}
		if !knowledge.IsSymmetricRelationType(edge.Type) && request.Direction == TraversalInbound && edge.Traversal == EdgeTraversalForward {
			return inconsistent("inbound path contains forward traversal")
		}
	}
	return nil
}

func validateNodes(workspaceID foundation.ID, values []GraphNode, requireOrder bool) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(values))
	previous := ""
	for _, node := range values {
		if ValidateGraphNode(node) != nil || node.WorkspaceID() != workspaceID {
			return nil, inconsistent("graph node is invalid or cross-workspace")
		}
		key := refKey(node.Ref())
		if _, duplicate := seen[key]; duplicate || (requireOrder && previous != "" && key <= previous) {
			return nil, inconsistent("graph nodes must be unique and stably ordered")
		}
		seen[key], previous = struct{}{}, key
	}
	return seen, nil
}

func validateEdges(workspaceID foundation.ID, values []GraphEdge, nodes map[string]struct{}, boundary map[string]struct{}, requireOrder bool) error {
	seen := make(map[foundation.ID]struct{}, len(values))
	previous := ""
	for _, edge := range values {
		key := edgeKey(edge)
		if !validID(edge.RelationID) || edge.WorkspaceID != workspaceID || !validRef(edge.Source) || !validRef(edge.Target) || edge.Source == edge.Target || !validRelationType(edge.Type) || !knowledge.RelationTypeCompatible(edge.Type, edge.Source.Type, edge.Target.Type) || !validRelationStatus(edge.Status) || !validEdgeTraversal(edge.Traversal) || edge.Version <= 0 || edge.EvidenceCount < 0 || invalidConfidence(edge.Confidence) || strings.TrimSpace(edge.EvidenceHref) == "" || edge.UpdatedAt.IsZero() || !validEvidenceSummary(edge.EvidenceCount, edge.EvidenceFingerprint) {
			return inconsistent("graph edge is invalid")
		}
		if _, duplicate := seen[edge.RelationID]; duplicate || (requireOrder && previous != "" && key <= previous) {
			return inconsistent("graph edges must be unique and stably ordered")
		}
		for _, ref := range []knowledge.NodeRef{edge.Source, edge.Target} {
			if _, ok := nodes[refKey(ref)]; !ok {
				if _, ok = boundary[refKey(ref)]; !ok {
					return inconsistent("graph edge endpoint is outside the closure")
				}
			}
		}
		seen[edge.RelationID], previous = struct{}{}, key
	}
	return nil
}

// ValidateRelationDetail 校验 Relation 详情绑定及 validity 区间。
func ValidateRelationDetail(workspaceID foundation.ID, detail RelationDetail) error {
	if !validID(workspaceID) || detail.Edge.WorkspaceID != workspaceID || !canonicalSHA256(detail.Fingerprint) || detail.CreatedAt.IsZero() || detail.UpdatedAt.Before(detail.CreatedAt) || (detail.ValidTo != nil && (detail.ValidFrom == nil || !detail.ValidTo.After(*detail.ValidFrom))) || (detail.Confirmation != nil && knowledge.ValidateConfirmation(*detail.Confirmation) != nil) {
		return inconsistent("relation detail is invalid")
	}
	nodes := map[string]struct{}{refKey(detail.Edge.Source): {}, refKey(detail.Edge.Target): {}}
	return validateEdges(workspaceID, []GraphEdge{detail.Edge}, nodes, nil, false)
}

// ValidateRelationEvidencePage 校验证据 owner、顺序、唯一性、Provenance 和页面上限。
func ValidateRelationEvidencePage(workspaceID, relationID foundation.ID, limit int, page RelationEvidencePage) error {
	if !validID(workspaceID) || !validID(relationID) || limit < 1 || limit > MaxLimit || page.WorkspaceID != workspaceID || page.RelationID != relationID || len(page.Items) > limit || validatePageMeta(page.Meta) != nil {
		return inconsistent("relation evidence page binding is invalid")
	}
	return ValidateRelationEvidenceWindow(workspaceID, relationID, page.Items, limit)
}

// ValidateRelationEvidenceWindow 校验 Adapter 返回的完整有界 Evidence 窗口。
func ValidateRelationEvidenceWindow(workspaceID, relationID foundation.ID, items []RelationEvidenceItem, maxItems int) error {
	if !validID(workspaceID) || !validID(relationID) || maxItems < 1 || len(items) > maxItems {
		return inconsistent("relation evidence window binding is invalid")
	}
	seen, previous := map[foundation.ID]struct{}{}, ""
	for _, item := range items {
		key := item.CreatedAt.UTC().Format(time.RFC3339Nano) + "\x00" + string(item.ID)
		if !validID(item.ID) || item.WorkspaceID != workspaceID || item.RelationID != relationID || knowledge.ValidateProvenanceRef(item.Provenance) != nil || knowledge.ValidateApplicability(item.Applicability) != nil || strings.TrimSpace(item.Reason) == "" || (item.Confirmation != nil && knowledge.ValidateConfirmation(*item.Confirmation) != nil) || strings.TrimSpace(item.SourceHref) == "" || strings.TrimSpace(item.SpanHref) == "" || item.CreatedAt.IsZero() {
			return inconsistent("relation evidence item is invalid")
		}
		if _, duplicate := seen[item.ID]; duplicate || (previous != "" && key <= previous) {
			return inconsistent("relation evidence items must be unique and stably ordered")
		}
		seen[item.ID], previous = struct{}{}, key
	}
	return nil
}

func traversalEndpoints(edge GraphEdge) (knowledge.NodeRef, knowledge.NodeRef) {
	if edge.Traversal == EdgeTraversalReverse {
		return edge.Target, edge.Source
	}
	return edge.Source, edge.Target
}

func validID(id foundation.ID) bool {
	parsed, err := foundation.ParseID(string(id))
	return err == nil && parsed == id
}
func validRef(ref knowledge.NodeRef) bool {
	return (ref.Type == knowledge.NodeTypeTopic || ref.Type == knowledge.NodeTypeClaim) && validID(ref.ID)
}
func refKey(ref knowledge.NodeRef) string { return string(ref.Type) + "\x00" + string(ref.ID) }
func edgeKey(edge GraphEdge) string {
	return string(edge.Type) + "\x00" + refKey(edge.Source) + "\x00" + refKey(edge.Target) + "\x00" + string(edge.RelationID)
}
func invalidConfidence(value *float64) bool {
	return value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 1)
}
func validEvidenceSummary(count int, fingerprint string) bool {
	if count == 0 {
		return fingerprint == ""
	}
	if len(fingerprint) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(fingerprint)
	return err == nil && hex.EncodeToString(decoded) == fingerprint
}
func canonicalSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
func validatePageMeta(meta PageMeta) error {
	if !canonicalSHA256(meta.Fingerprint) {
		return inconsistent("page fingerprint is invalid")
	}
	if meta.Truncated && strings.TrimSpace(meta.Reason) == "" {
		return inconsistent("truncated page metadata requires a reason")
	}
	if !meta.Truncated && meta.Reason != "" {
		return inconsistent("non-truncated page metadata must not carry a reason")
	}
	if meta.Complete {
		if meta.Truncated || meta.NextCursor != "" {
			return inconsistent("complete page metadata is inconsistent")
		}
		return nil
	}
	if meta.Truncated {
		return nil
	}
	if strings.TrimSpace(meta.NextCursor) == "" {
		return inconsistent("paged metadata is inconsistent")
	}
	return nil
}
func containsNodeType(values []knowledge.NodeType, want knowledge.NodeType) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func containsRelationType(values []knowledge.RelationType, want knowledge.RelationType) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func containsRelationStatus(values []knowledge.RelationStatus, want knowledge.RelationStatus) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func containsClaimStatus(values []knowledge.ClaimStatus, want knowledge.ClaimStatus) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func containsID(values []foundation.ID, want foundation.ID) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func nodeUpdatedAt(node GraphNode) time.Time {
	if node.Topic != nil {
		return node.Topic.UpdatedAt
	}
	return node.Claim.UpdatedAt
}
func searchKindRank(value NodeSearchMatchKind) int {
	if value == NodeSearchExact {
		return 0
	}
	return 1
}
func validDirection(value TraversalDirection) bool {
	return value == TraversalBoth || value == TraversalOutbound || value == TraversalInbound
}
func validEdgeTraversal(value EdgeTraversal) bool {
	return value == EdgeTraversalForward || value == EdgeTraversalReverse
}
func validRelationStatus(value knowledge.RelationStatus) bool {
	return value == knowledge.RelationStatusConfirmed || value == knowledge.RelationStatusStale
}
func validTopicStatus(value knowledge.TopicStatus) bool {
	return value == knowledge.TopicStatusActive || value == knowledge.TopicStatusMerged || value == knowledge.TopicStatusDeprecated
}
func validClaimStatus(value knowledge.ClaimStatus) bool {
	switch value {
	case knowledge.ClaimStatusSuggested, knowledge.ClaimStatusConfirmed, knowledge.ClaimStatusDisputed, knowledge.ClaimStatusSuperseded, knowledge.ClaimStatusDeprecated, knowledge.ClaimStatusInvalid:
		return true
	default:
		return false
	}
}
func validRelationType(value knowledge.RelationType) bool {
	return knowledge.RelationTypeCompatible(value, knowledge.NodeTypeClaim, knowledge.NodeTypeClaim) || knowledge.RelationTypeCompatible(value, knowledge.NodeTypeClaim, knowledge.NodeTypeTopic) || knowledge.RelationTypeCompatible(value, knowledge.NodeTypeTopic, knowledge.NodeTypeClaim) || knowledge.RelationTypeCompatible(value, knowledge.NodeTypeTopic, knowledge.NodeTypeTopic)
}

func uniqueNodeTypes(values []knowledge.NodeType) bool {
	seen := map[knowledge.NodeType]struct{}{}
	for _, v := range values {
		if v != knowledge.NodeTypeTopic && v != knowledge.NodeTypeClaim {
			return false
		}
		if _, ok := seen[v]; ok {
			return false
		}
		seen[v] = struct{}{}
	}
	return true
}
func uniqueRelationTypes(values []knowledge.RelationType) bool {
	seen := map[knowledge.RelationType]struct{}{}
	for _, v := range values {
		if !validRelationType(v) {
			return false
		}
		if _, ok := seen[v]; ok {
			return false
		}
		seen[v] = struct{}{}
	}
	return true
}
func uniqueStatuses(values []knowledge.RelationStatus) bool {
	seen := map[knowledge.RelationStatus]struct{}{}
	for _, v := range values {
		if !validRelationStatus(v) {
			return false
		}
		if _, ok := seen[v]; ok {
			return false
		}
		seen[v] = struct{}{}
	}
	return true
}
func uniqueClaimStatuses(values []knowledge.ClaimStatus) bool {
	valid := map[knowledge.ClaimStatus]bool{knowledge.ClaimStatusConfirmed: true, knowledge.ClaimStatusDisputed: true}
	seen := map[knowledge.ClaimStatus]struct{}{}
	for _, v := range values {
		if !valid[v] {
			return false
		}
		if _, ok := seen[v]; ok {
			return false
		}
		seen[v] = struct{}{}
	}
	return true
}
func uniqueIDs(values []foundation.ID) bool {
	seen := map[foundation.ID]struct{}{}
	for _, v := range values {
		if !validID(v) {
			return false
		}
		if _, ok := seen[v]; ok {
			return false
		}
		seen[v] = struct{}{}
	}
	return true
}
