package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"gorm.io/gorm"
)

// GlobalWindow returns the same bounded canonical Topic projection as the
// legacy repository, but executes it through one GORM-owned read snapshot.
func (repository *GORMRepository) GlobalWindow(ctx context.Context, request graphdomain.GlobalRequest) (graphapp.GlobalResultWindow, error) {
	if err := repository.ready(ctx); err != nil {
		return graphapp.GlobalResultWindow{}, err
	}
	if err := graphdomain.ValidateGlobalRequest(request); err != nil {
		return graphapp.GlobalResultWindow{}, err
	}
	var result graphapp.GlobalResultWindow
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		if len(request.Filter.NodeTypes) > 0 && !containsNodeType(request.Filter.NodeTypes, knowledge.NodeTypeTopic) {
			result = graphapp.GlobalResultWindow{Items: []graphdomain.GlobalCluster{}}
			return nil
		}
		window, err := gormGlobalWindow(callbackCtx, transaction, request)
		if err == nil {
			result = window
		}
		return err
	})
	if err != nil {
		return graphapp.GlobalResultWindow{}, err
	}
	return result, nil
}

func gormGlobalWindow(ctx context.Context, database *gorm.DB, request graphdomain.GlobalRequest) (graphapp.GlobalResultWindow, error) {
	claimStatuses := request.Filter.ClaimStatuses
	if len(claimStatuses) == 0 {
		claimStatuses = []knowledge.ClaimStatus{knowledge.ClaimStatusConfirmed, knowledge.ClaimStatusDisputed}
	}
	relationStatuses := request.Filter.RelationStatuses
	if len(relationStatuses) == 0 {
		relationStatuses = []knowledge.RelationStatus{knowledge.RelationStatusConfirmed}
	}
	includeClaims := len(request.Filter.NodeTypes) == 0 || containsNodeType(request.Filter.NodeTypes, knowledge.NodeTypeClaim)
	rows, err := gormGraphRawRows(ctx, database, globalClusterSQL,
		string(request.WorkspaceID), ids(request.Filter.TopicIDs), stringsOf(claimStatuses), includeClaims,
		request.Filter.ClaimMinConfidence, stringsOf(relationStatuses), stringsOf(request.Filter.RelationTypes),
		request.Filter.RelationMinConfidence, request.Filter.UpdatedAfter, globalWindowLimit,
	)
	if err != nil {
		return graphapp.GlobalResultWindow{}, classifyGORM(ctx, err)
	}
	defer rows.Close()
	clusters := make([]graphdomain.GlobalCluster, 0, graphapp.MaxResultWindowItems)
	for rows.Next() {
		var cluster graphdomain.GlobalCluster
		var id, workspaceID, status string
		if err := rows.Scan(
			&id, &workspaceID, &cluster.Topic.Name, &cluster.Topic.Description, &status, &cluster.Topic.Version,
			&cluster.Topic.UpdatedAt, &cluster.DirectClaimCount, &cluster.IncidentRelationCount,
			&cluster.ClusterScore, &cluster.UpdatedAt,
		); err != nil {
			return graphapp.GlobalResultWindow{}, inconsistent(err)
		}
		cluster.Topic.Ref = knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: idValue(id)}
		cluster.Topic.WorkspaceID = idValue(workspaceID)
		cluster.Topic.Status = knowledge.TopicStatus(status)
		clusters = append(clusters, cluster)
	}
	if err := rows.Err(); err != nil {
		return graphapp.GlobalResultWindow{}, classifyGORM(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return graphapp.GlobalResultWindow{}, classifyGORM(ctx, err)
	}
	truncated := len(clusters) > graphapp.MaxResultWindowItems
	if truncated {
		clusters = clusters[:graphapp.MaxResultWindowItems]
	}
	window := graphapp.GlobalResultWindow{Items: clusters, Truncated: truncated}
	if truncated {
		window.Reason = "RESULT_WINDOW_LIMIT"
	}
	return window, nil
}

// SearchNodes searches Topic and Claim projections within one read snapshot.
func (repository *GORMRepository) SearchNodes(ctx context.Context, request graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error) {
	if err := repository.ready(ctx); err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	if err := graphdomain.ValidateNodeSearchRequest(request); err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	_, topicQuery, err := knowledge.NormalizeTopicText(request.Query)
	if err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	_, claimQuery, err := knowledge.NormalizeStatement(request.Query)
	if err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	var result graphdomain.NodeSearchResult
	err = repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		matches, err := gormSearchNodes(callbackCtx, transaction, request, topicQuery, claimQuery)
		if err == nil {
			result = matches
		}
		return err
	})
	if err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	return result, nil
}

func gormSearchNodes(ctx context.Context, database *gorm.DB, request graphdomain.NodeSearchRequest, topicQuery, claimQuery string) (graphdomain.NodeSearchResult, error) {
	matches := make([]graphdomain.NodeSearchMatch, 0, request.Limit*2)
	topicRows, err := gormGraphRawRows(ctx, database, topicSearchSQL, string(request.WorkspaceID), topicQuery, request.Limit)
	if err != nil {
		return graphdomain.NodeSearchResult{}, classifyGORM(ctx, err)
	}
	defer topicRows.Close()
	for topicRows.Next() {
		var rank int
		node, scanErr := scanTopicNodeWithRank(topicRows, &rank)
		if scanErr != nil {
			return graphdomain.NodeSearchResult{}, inconsistent(scanErr)
		}
		matches = append(matches, graphdomain.NodeSearchMatch{Kind: matchKind(rank), Node: graphdomain.GraphNode{Topic: &node}})
	}
	if err := topicRows.Err(); err != nil {
		return graphdomain.NodeSearchResult{}, classifyGORM(ctx, err)
	}
	if err := topicRows.Close(); err != nil {
		return graphdomain.NodeSearchResult{}, classifyGORM(ctx, err)
	}

	claimRows, err := gormGraphRawRows(ctx, database, claimSearchSQL, string(request.WorkspaceID), claimQuery, request.Limit)
	if err != nil {
		return graphdomain.NodeSearchResult{}, classifyGORM(ctx, err)
	}
	defer claimRows.Close()
	for claimRows.Next() {
		var rank int
		node, scanErr := scanClaimNodeWithRank(claimRows, &rank)
		if scanErr != nil {
			return graphdomain.NodeSearchResult{}, inconsistent(scanErr)
		}
		matches = append(matches, graphdomain.NodeSearchMatch{Kind: matchKind(rank), Node: graphdomain.GraphNode{Claim: &node}})
	}
	if err := claimRows.Err(); err != nil {
		return graphdomain.NodeSearchResult{}, classifyGORM(ctx, err)
	}
	if err := claimRows.Close(); err != nil {
		return graphdomain.NodeSearchResult{}, classifyGORM(ctx, err)
	}

	sort.Slice(matches, func(left, right int) bool {
		leftRank, rightRank := matchRank(matches[left].Kind), matchRank(matches[right].Kind)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftRef, rightRef := matches[left].Node.Ref(), matches[right].Node.Ref()
		if leftRef.Type != rightRef.Type {
			return leftRef.Type < rightRef.Type
		}
		return leftRef.ID < rightRef.ID
	})
	if len(matches) > request.Limit {
		matches = matches[:request.Limit]
	}
	return graphdomain.NodeSearchResult{WorkspaceID: request.WorkspaceID, Matches: matches}, nil
}

// NodeDetail reads one Workspace-scoped Topic or Claim from one read snapshot.
func (repository *GORMRepository) NodeDetail(ctx context.Context, workspaceID foundation.ID, ref knowledge.NodeRef) (graphdomain.GraphNode, error) {
	if err := repository.ready(ctx); err != nil {
		return graphdomain.GraphNode{}, err
	}
	if ref.Type != knowledge.NodeTypeTopic && ref.Type != knowledge.NodeTypeClaim {
		return graphdomain.GraphNode{}, notFound(graphdomain.ErrorCodeNodeNotFound, sql.ErrNoRows)
	}
	var result graphdomain.GraphNode
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		node, err := gormNodeDetail(callbackCtx, transaction, workspaceID, ref)
		if err == nil {
			result = node
		}
		return err
	})
	if err != nil {
		return graphdomain.GraphNode{}, err
	}
	return result, nil
}

func gormNodeDetail(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, ref knowledge.NodeRef) (graphdomain.GraphNode, error) {
	switch ref.Type {
	case knowledge.NodeTypeTopic:
		row, err := gormGraphRawRow(ctx, database, topicDetailSQL, string(workspaceID), string(ref.ID))
		if err != nil {
			return graphdomain.GraphNode{}, classifyGORM(ctx, err)
		}
		node, err := scanTopicNode(row)
		if gormGraphNoRows(err) {
			return graphdomain.GraphNode{}, notFound(graphdomain.ErrorCodeNodeNotFound, err)
		}
		if err != nil {
			return graphdomain.GraphNode{}, classifyGORMQueryScan(ctx, err)
		}
		return graphdomain.GraphNode{Topic: &node}, nil
	case knowledge.NodeTypeClaim:
		row, err := gormGraphRawRow(ctx, database, claimDetailSQL, string(workspaceID), string(ref.ID))
		if err != nil {
			return graphdomain.GraphNode{}, classifyGORM(ctx, err)
		}
		node, err := scanClaimNode(row)
		if gormGraphNoRows(err) {
			return graphdomain.GraphNode{}, notFound(graphdomain.ErrorCodeNodeNotFound, err)
		}
		if err != nil {
			return graphdomain.GraphNode{}, classifyGORMQueryScan(ctx, err)
		}
		return graphdomain.GraphNode{Claim: &node}, nil
	default:
		return graphdomain.GraphNode{}, notFound(graphdomain.ErrorCodeNodeNotFound, sql.ErrNoRows)
	}
}

// RelationDetail reads the Relation and its bounded Evidence summary from one snapshot.
func (repository *GORMRepository) RelationDetail(ctx context.Context, workspaceID, relationID foundation.ID) (graphdomain.RelationDetail, error) {
	if err := repository.ready(ctx); err != nil {
		return graphdomain.RelationDetail{}, err
	}
	var result graphdomain.RelationDetail
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		detail, err := gormRelationDetail(callbackCtx, transaction, workspaceID, relationID)
		if err == nil {
			result = detail
		}
		return err
	})
	if err != nil {
		return graphdomain.RelationDetail{}, err
	}
	return result, nil
}

func gormRelationDetail(ctx context.Context, database *gorm.DB, workspaceID, relationID foundation.ID) (graphdomain.RelationDetail, error) {
	row, err := gormGraphRawRow(ctx, database, relationDetailSQL, string(workspaceID), string(relationID))
	if err != nil {
		return graphdomain.RelationDetail{}, classifyGORM(ctx, err)
	}
	var detail graphdomain.RelationDetail
	var id, storedWorkspace, sourceType, sourceID, targetType, targetID, relationType, status string
	var confirmationMethod, confirmationRef, evidenceFingerprint *string
	err = row.Scan(
		&id, &storedWorkspace, &sourceType, &sourceID, &targetType, &targetID, &relationType, &status,
		&confirmationMethod, &confirmationRef, &detail.Edge.Confidence, &detail.ValidFrom, &detail.ValidTo,
		&detail.Fingerprint, &evidenceFingerprint, &detail.Edge.Version, &detail.CreatedAt, &detail.UpdatedAt,
		&detail.Edge.EvidenceCount,
	)
	if gormGraphNoRows(err) {
		return graphdomain.RelationDetail{}, notFound(graphdomain.ErrorCodeRelationNotFound, err)
	}
	if err != nil {
		return graphdomain.RelationDetail{}, classifyGORMQueryScan(ctx, err)
	}
	detail.Edge.RelationID = idValue(id)
	detail.Edge.WorkspaceID = idValue(storedWorkspace)
	detail.Edge.Source = knowledge.NodeRef{Type: knowledge.NodeType(sourceType), ID: idValue(sourceID)}
	detail.Edge.Target = knowledge.NodeRef{Type: knowledge.NodeType(targetType), ID: idValue(targetID)}
	detail.Edge.Type = knowledge.RelationType(relationType)
	detail.Edge.Status = knowledge.RelationStatus(status)
	detail.Edge.Traversal = graphdomain.EdgeTraversalForward
	detail.Edge.UpdatedAt = detail.UpdatedAt
	detail.Edge.EvidenceHref = relationEvidenceHref(detail.Edge.WorkspaceID, detail.Edge.RelationID)
	if evidenceFingerprint != nil {
		detail.Edge.EvidenceFingerprint = *evidenceFingerprint
	}
	if confirmationMethod != nil || confirmationRef != nil {
		if confirmationMethod == nil || confirmationRef == nil {
			return graphdomain.RelationDetail{}, inconsistent(errors.New("relation confirmation pair is inconsistent"))
		}
		detail.Confirmation = &knowledge.Confirmation{Method: knowledge.ConfirmationMethod(*confirmationMethod), Reference: *confirmationRef}
	}
	return detail, nil
}

// RelationEvidenceWindow returns the stable, bounded Evidence window from one snapshot.
func (repository *GORMRepository) RelationEvidenceWindow(ctx context.Context, workspaceID, relationID foundation.ID) (graphapp.RelationEvidenceResultWindow, error) {
	if err := repository.ready(ctx); err != nil {
		return graphapp.RelationEvidenceResultWindow{}, err
	}
	var result graphapp.RelationEvidenceResultWindow
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		window, err := gormRelationEvidenceWindow(callbackCtx, transaction, workspaceID, relationID)
		if err == nil {
			result = window
		}
		return err
	})
	if err != nil {
		return graphapp.RelationEvidenceResultWindow{}, err
	}
	return result, nil
}

func gormRelationEvidenceWindow(ctx context.Context, database *gorm.DB, workspaceID, relationID foundation.ID) (graphapp.RelationEvidenceResultWindow, error) {
	rows, err := gormGraphRawRows(ctx, database, relationEvidenceWindowSQL, string(workspaceID), string(relationID), graphapp.MaxResultWindowItems+1)
	if err != nil {
		return graphapp.RelationEvidenceResultWindow{}, classifyGORM(ctx, err)
	}
	defer rows.Close()
	items := make([]graphdomain.RelationEvidenceItem, 0, graphapp.MaxResultWindowItems)
	expectedCount := -1
	for rows.Next() {
		count, item, scanErr := scanRelationEvidenceItem(rows)
		if scanErr != nil {
			return graphapp.RelationEvidenceResultWindow{}, inconsistent(scanErr)
		}
		if expectedCount >= 0 && expectedCount != count {
			return graphapp.RelationEvidenceResultWindow{}, inconsistent(errors.New("relation evidence owner count changed within one result"))
		}
		expectedCount = count
		if item != nil {
			items = append(items, *item)
		}
	}
	if err := rows.Err(); err != nil {
		return graphapp.RelationEvidenceResultWindow{}, classifyGORM(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return graphapp.RelationEvidenceResultWindow{}, classifyGORM(ctx, err)
	}
	if expectedCount < 0 {
		return graphapp.RelationEvidenceResultWindow{}, notFound(graphdomain.ErrorCodeRelationNotFound, errors.New("graph relation does not exist"))
	}
	truncated := len(items) > graphapp.MaxResultWindowItems
	if truncated {
		items = items[:graphapp.MaxResultWindowItems]
	}
	if expectedCount <= graphapp.MaxResultWindowItems && len(items) != expectedCount || expectedCount > graphapp.MaxResultWindowItems && !truncated {
		return graphapp.RelationEvidenceResultWindow{}, inconsistent(errors.New("relation evidence count is inconsistent"))
	}
	window := graphapp.RelationEvidenceResultWindow{Items: items, Truncated: truncated}
	if truncated {
		window.Reason = "RESULT_WINDOW_LIMIT"
	}
	return window, nil
}

var _ graphapp.QueryPort = (*GORMRepository)(nil)

func classifyGORMQueryScan(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if graphGORMContextCause(ctx, err) != nil {
		return classifyGORM(ctx, err)
	}
	return classifyProjectionScan(err)
}
