package postgres

import (
	"context"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"gorm.io/gorm"
)

// NeighborhoodWindow reads a bounded one-to-three-hop neighborhood from one
// repeatable-read snapshot and only publishes complete expanded layers.
func (repository *GORMRepository) NeighborhoodWindow(ctx context.Context, request graphdomain.NeighborhoodRequest) (graphdomain.Neighborhood, error) {
	if err := repository.ready(ctx); err != nil {
		return graphdomain.Neighborhood{}, err
	}
	if err := graphdomain.ValidateNeighborhoodRequest(request); err != nil {
		return graphdomain.Neighborhood{}, err
	}
	var result graphdomain.Neighborhood
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		neighborhood, err := gormNeighborhoodWindow(callbackCtx, transaction, request)
		if err == nil {
			result = neighborhood
		}
		return err
	})
	if err != nil {
		return graphdomain.Neighborhood{}, err
	}
	return result, nil
}

func gormNeighborhoodWindow(ctx context.Context, database *gorm.DB, request graphdomain.NeighborhoodRequest) (graphdomain.Neighborhood, error) {
	center, err := gormNodeDetail(ctx, database, request.WorkspaceID, request.Center)
	if err != nil {
		return graphdomain.Neighborhood{}, err
	}
	if !nodeMatchesNeighborhoodFilter(center, request.Filter) {
		return graphdomain.Neighborhood{}, notFound(graphdomain.ErrorCodeNodeNotFound, errors.New("graph center is outside the requested projection"))
	}
	if request.Depth > 1 {
		return gormNeighborhoodDeep(ctx, database, request, center)
	}

	relationStatuses := request.Filter.RelationStatuses
	if len(relationStatuses) == 0 {
		relationStatuses = []knowledge.RelationStatus{knowledge.RelationStatusConfirmed}
	}
	claimStatuses := request.Filter.ClaimStatuses
	if len(claimStatuses) == 0 {
		claimStatuses = []knowledge.ClaimStatus{knowledge.ClaimStatusConfirmed, knowledge.ClaimStatusDisputed}
	}
	probeLimit := request.MaxEdges + 1
	if probeLimit > graphapp.MaxResultWindowItems+1 {
		probeLimit = graphapp.MaxResultWindowItems + 1
	}
	rows, err := gormGraphRawRows(ctx, database, neighborhoodDepthOneSQL,
		string(request.WorkspaceID), string(request.Center.Type), string(request.Center.ID), string(request.Direction),
		stringsOf(relationStatuses), stringsOf(request.Filter.RelationTypes), request.Filter.RelationMinConfidence,
		request.Filter.UpdatedAfter, stringsOf(request.Filter.NodeTypes), ids(request.Filter.TopicIDs),
		stringsOf(claimStatuses), request.Filter.ClaimMinConfidence, probeLimit,
	)
	if err != nil {
		return graphdomain.Neighborhood{}, classifyGORM(ctx, err)
	}
	defer rows.Close()
	edges := make([]graphdomain.GraphEdge, 0, probeLimit)
	for rows.Next() {
		edge, scanErr := scanNeighborhoodEdge(rows)
		if scanErr != nil {
			return graphdomain.Neighborhood{}, inconsistent(scanErr)
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return graphdomain.Neighborhood{}, classifyGORM(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return graphdomain.Neighborhood{}, classifyGORM(ctx, err)
	}

	truncated, reason := false, ""
	windowLimit := request.MaxEdges
	if windowLimit > graphapp.MaxResultWindowItems {
		windowLimit = graphapp.MaxResultWindowItems
	}
	if len(edges) > windowLimit {
		edges = edges[:windowLimit]
		truncated = true
		if windowLimit == graphapp.MaxResultWindowItems {
			reason = "RESULT_WINDOW_LIMIT"
		} else {
			reason = "EDGE_BUDGET"
		}
	}

	refs := neighborhoodRefs(request.Center, edges)
	nodes, boundaries, err := gormHydrateNeighborhoodNodes(ctx, database, request, center, refs)
	if err != nil {
		return graphdomain.Neighborhood{}, err
	}
	fingerprint, err := neighborhoodFingerprint(nodes, edges, boundaries)
	if err != nil {
		return graphdomain.Neighborhood{}, inconsistent(err)
	}
	meta := graphdomain.PageMeta{Fingerprint: fingerprint, Complete: !truncated, Truncated: truncated, Reason: reason}
	return graphdomain.Neighborhood{
		WorkspaceID: request.WorkspaceID, Center: request.Center, Nodes: nodes, Edges: edges,
		BoundaryNodes: boundaries, LayerCounts: []int{len(nodes) - 1}, CompletedDepth: 1, Meta: meta,
	}, nil
}

func gormNeighborhoodDeep(ctx context.Context, database *gorm.DB, request graphdomain.NeighborhoodRequest, center graphdomain.GraphNode) (graphdomain.Neighborhood, error) {
	result := graphdomain.Neighborhood{
		WorkspaceID: request.WorkspaceID, Center: request.Center,
		Nodes: []graphdomain.GraphNode{center}, Edges: []graphdomain.GraphEdge{}, BoundaryNodes: []knowledge.NodeRef{},
		LayerCounts: []int{},
	}
	visitedNodes := map[knowledge.NodeRef]struct{}{request.Center: {}}
	seenEdges := make(map[foundation.ID]struct{})
	frontier := []knowledge.NodeRef{request.Center}
	for depth := 1; depth <= request.Depth; depth++ {
		if len(frontier) == 0 {
			result.LayerCounts = append(result.LayerCounts, 0)
			result.CompletedDepth = depth
			continue
		}
		remainingEdges := request.MaxEdges - len(result.Edges)
		if remainingEdges < 0 {
			remainingEdges = 0
		}
		candidates, err := gormQueryFrontierEdges(ctx, database, request, frontier, seenEdges, remainingEdges+1)
		if err != nil {
			return graphdomain.Neighborhood{}, err
		}
		newEdges := make([]graphdomain.GraphEdge, 0, len(candidates))
		newNodeSet := make(map[knowledge.NodeRef]struct{})
		for _, edge := range candidates {
			if _, exists := seenEdges[edge.RelationID]; exists {
				continue
			}
			newEdges = append(newEdges, edge)
			for _, ref := range []knowledge.NodeRef{edge.Source, edge.Target} {
				if _, exists := visitedNodes[ref]; !exists {
					newNodeSet[ref] = struct{}{}
				}
			}
		}
		newRefs := sortedRefs(newNodeSet)
		reason := ""
		switch {
		case len(newEdges) > remainingEdges:
			reason = "EDGE_BUDGET"
		case len(result.Nodes)+len(newRefs) > request.MaxNodes:
			reason = "NODE_BUDGET"
		case len(newRefs) > request.MaxFrontier:
			reason = "FRONTIER_BUDGET"
		}
		if reason != "" {
			fingerprint, fingerprintErr := neighborhoodFingerprint(result.Nodes, result.Edges, nil)
			if fingerprintErr != nil {
				return graphdomain.Neighborhood{}, inconsistent(fingerprintErr)
			}
			result.Meta = graphdomain.PageMeta{Fingerprint: fingerprint, Truncated: true, Reason: reason}
			return result, nil
		}
		layerNodes, err := gormHydrateExactNeighborhoodRefs(ctx, database, request.WorkspaceID, newRefs)
		if err != nil {
			return graphdomain.Neighborhood{}, err
		}
		result.Nodes = append(result.Nodes, layerNodes...)
		result.Edges = append(result.Edges, newEdges...)
		sort.Slice(result.Nodes, func(i, j int) bool { return graphNodeLess(result.Nodes[i], result.Nodes[j]) })
		sort.Slice(result.Edges, func(i, j int) bool { return graphEdgeLess(result.Edges[i], result.Edges[j]) })
		for _, ref := range newRefs {
			visitedNodes[ref] = struct{}{}
		}
		for _, edge := range newEdges {
			seenEdges[edge.RelationID] = struct{}{}
		}
		frontier = newRefs
		result.LayerCounts = append(result.LayerCounts, len(newRefs))
		result.CompletedDepth = depth
	}
	fingerprint, err := neighborhoodFingerprint(result.Nodes, result.Edges, nil)
	if err != nil {
		return graphdomain.Neighborhood{}, inconsistent(err)
	}
	result.Meta = graphdomain.PageMeta{Fingerprint: fingerprint, Complete: true}
	return result, nil
}

func gormQueryFrontierEdges(ctx context.Context, database *gorm.DB, request graphdomain.NeighborhoodRequest, frontier []knowledge.NodeRef, seen map[foundation.ID]struct{}, limit int) ([]graphdomain.GraphEdge, error) {
	frontierTypes := make([]string, len(frontier))
	frontierIDs := make([]string, len(frontier))
	for index, ref := range frontier {
		frontierTypes[index], frontierIDs[index] = string(ref.Type), string(ref.ID)
	}
	seenIDs := make([]foundation.ID, 0, len(seen))
	for id := range seen {
		seenIDs = append(seenIDs, id)
	}
	relationStatuses := request.Filter.RelationStatuses
	if len(relationStatuses) == 0 {
		relationStatuses = []knowledge.RelationStatus{knowledge.RelationStatusConfirmed}
	}
	claimStatuses := request.Filter.ClaimStatuses
	if len(claimStatuses) == 0 {
		claimStatuses = []knowledge.ClaimStatus{knowledge.ClaimStatusConfirmed, knowledge.ClaimStatusDisputed}
	}
	rows, err := gormGraphRawRows(ctx, database, neighborhoodFrontierSQL,
		string(request.WorkspaceID), frontierTypes, frontierIDs, string(request.Direction), ids(seenIDs),
		stringsOf(relationStatuses), stringsOf(request.Filter.RelationTypes), request.Filter.RelationMinConfidence,
		request.Filter.UpdatedAfter, stringsOf(request.Filter.NodeTypes), ids(request.Filter.TopicIDs),
		stringsOf(claimStatuses), request.Filter.ClaimMinConfidence, limit,
	)
	if err != nil {
		return nil, classifyGORM(ctx, err)
	}
	defer rows.Close()
	edges := make([]graphdomain.GraphEdge, 0, limit)
	for rows.Next() {
		edge, scanErr := scanNeighborhoodEdge(rows)
		if scanErr != nil {
			return nil, inconsistent(scanErr)
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORM(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return nil, classifyGORM(ctx, err)
	}
	return edges, nil
}

func gormHydrateExactNeighborhoodRefs(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, refs []knowledge.NodeRef) ([]graphdomain.GraphNode, error) {
	nodeByRef := make(map[knowledge.NodeRef]graphdomain.GraphNode, len(refs))
	topicIDs, claimIDs := make([]foundation.ID, 0), make([]foundation.ID, 0)
	for _, ref := range refs {
		if ref.Type == knowledge.NodeTypeTopic {
			topicIDs = append(topicIDs, ref.ID)
		} else {
			claimIDs = append(claimIDs, ref.ID)
		}
	}
	if err := gormHydrateTopics(ctx, database, workspaceID, topicIDs, nodeByRef); err != nil {
		return nil, err
	}
	if err := gormHydrateClaims(ctx, database, workspaceID, claimIDs, nodeByRef); err != nil {
		return nil, err
	}
	nodes := make([]graphdomain.GraphNode, 0, len(refs))
	for _, ref := range refs {
		node, ok := nodeByRef[ref]
		if !ok {
			return nil, inconsistent(errors.New("graph neighborhood node disappeared during hydration"))
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func gormHydrateNeighborhoodNodes(ctx context.Context, database *gorm.DB, request graphdomain.NeighborhoodRequest, center graphdomain.GraphNode, refs []knowledge.NodeRef) ([]graphdomain.GraphNode, []knowledge.NodeRef, error) {
	topicIDs, claimIDs := make([]foundation.ID, 0), make([]foundation.ID, 0)
	for _, ref := range refs {
		if ref == request.Center {
			continue
		}
		if ref.Type == knowledge.NodeTypeTopic {
			topicIDs = append(topicIDs, ref.ID)
		} else {
			claimIDs = append(claimIDs, ref.ID)
		}
	}
	nodeByRef := map[knowledge.NodeRef]graphdomain.GraphNode{request.Center: center}
	if err := gormHydrateTopics(ctx, database, request.WorkspaceID, topicIDs, nodeByRef); err != nil {
		return nil, nil, err
	}
	if err := gormHydrateClaims(ctx, database, request.WorkspaceID, claimIDs, nodeByRef); err != nil {
		return nil, nil, err
	}
	nodes := make([]graphdomain.GraphNode, 0, request.MaxNodes)
	boundaries := make([]knowledge.NodeRef, 0)
	nodes = append(nodes, center)
	for _, ref := range refs {
		if ref == request.Center {
			continue
		}
		node, ok := nodeByRef[ref]
		if !ok {
			return nil, nil, inconsistent(errors.New("graph neighborhood node disappeared during hydration"))
		}
		if len(nodes) < request.MaxNodes {
			nodes = append(nodes, node)
		} else {
			boundaries = append(boundaries, ref)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return graphNodeLess(nodes[i], nodes[j]) })
	return nodes, boundaries, nil
}

func gormHydrateTopics(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, topicIDs []foundation.ID, target map[knowledge.NodeRef]graphdomain.GraphNode) error {
	if len(topicIDs) == 0 {
		return nil
	}
	rows, err := gormGraphRawRows(ctx, database, `SELECT id::text,workspace_id::text,name,description,status,version,updated_at FROM core.topic WHERE workspace_id=$1 AND id=ANY($2::uuid[])`, string(workspaceID), ids(topicIDs))
	if err != nil {
		return classifyGORM(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		node, scanErr := scanTopicNode(rows)
		if scanErr != nil {
			return inconsistent(scanErr)
		}
		target[node.Ref] = graphdomain.GraphNode{Topic: &node}
	}
	if err := rows.Err(); err != nil {
		return classifyGORM(ctx, err)
	}
	return classifyGORM(ctx, rows.Close())
}

func gormHydrateClaims(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, claimIDs []foundation.ID, target map[knowledge.NodeRef]graphdomain.GraphNode) error {
	if len(claimIDs) == 0 {
		return nil
	}
	rows, err := gormGraphRawRows(ctx, database, `SELECT id::text,workspace_id::text,statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,version,updated_at FROM core.claim WHERE workspace_id=$1 AND id=ANY($2::uuid[])`, string(workspaceID), ids(claimIDs))
	if err != nil {
		return classifyGORM(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		node, scanErr := scanClaimNode(rows)
		if scanErr != nil {
			return inconsistent(scanErr)
		}
		target[node.Ref] = graphdomain.GraphNode{Claim: &node}
	}
	if err := rows.Err(); err != nil {
		return classifyGORM(ctx, err)
	}
	return classifyGORM(ctx, rows.Close())
}
