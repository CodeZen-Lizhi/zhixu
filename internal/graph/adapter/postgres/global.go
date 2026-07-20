package postgres

import (
	"context"
	"errors"

	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const globalWindowLimit = graphapp.MaxResultWindowItems + 1

// GlobalWindow 返回按冻结 cluster_score 排序的有界 Topic cluster 窗口。
func (repository *Repository) GlobalWindow(ctx context.Context, request graphdomain.GlobalRequest) (graphapp.GlobalResultWindow, error) {
	if repository == nil || repository.db == nil {
		return graphapp.GlobalResultWindow{}, unavailable(errors.New("graph repository is unavailable"))
	}
	if err := graphdomain.ValidateGlobalRequest(request); err != nil {
		return graphapp.GlobalResultWindow{}, err
	}
	if len(request.Filter.NodeTypes) > 0 && !containsNodeType(request.Filter.NodeTypes, knowledge.NodeTypeTopic) {
		return graphapp.GlobalResultWindow{Items: []graphdomain.GlobalCluster{}}, nil
	}
	claimStatuses := request.Filter.ClaimStatuses
	if len(claimStatuses) == 0 {
		claimStatuses = []knowledge.ClaimStatus{knowledge.ClaimStatusConfirmed, knowledge.ClaimStatusDisputed}
	}
	relationStatuses := request.Filter.RelationStatuses
	if len(relationStatuses) == 0 {
		relationStatuses = []knowledge.RelationStatus{knowledge.RelationStatusConfirmed}
	}
	includeClaims := len(request.Filter.NodeTypes) == 0 || containsNodeType(request.Filter.NodeTypes, knowledge.NodeTypeClaim)
	rows, err := repository.db.Query(ctx, globalClusterSQL,
		string(request.WorkspaceID), ids(request.Filter.TopicIDs), stringsOf(claimStatuses), includeClaims,
		request.Filter.ClaimMinConfidence, stringsOf(relationStatuses), stringsOf(request.Filter.RelationTypes),
		request.Filter.RelationMinConfidence, request.Filter.UpdatedAfter, globalWindowLimit,
	)
	if err != nil {
		return graphapp.GlobalResultWindow{}, classify(err)
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
		return graphapp.GlobalResultWindow{}, classify(err)
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

const globalClusterSQL = `
WITH eligible_claims AS (
    SELECT c.id,c.updated_at
    FROM core.claim c
    WHERE c.workspace_id=$1
      AND $4
      AND c.status=ANY($3::text[])
      AND ($5::double precision IS NULL OR (c.confidence_score IS NOT NULL AND c.confidence_score >= $5))
), memberships AS (
    SELECT DISTINCT r.target_node_id AS topic_id,r.source_node_id AS claim_id,
           GREATEST(r.updated_at,c.updated_at) AS updated_at
    FROM core.relation r
    JOIN eligible_claims c ON c.id=r.source_node_id
    WHERE r.workspace_id=$1 AND r.source_node_type='CLAIM' AND r.target_node_type='TOPIC'
      AND r.relation_type='BELONGS_TO' AND r.status='CONFIRMED'
      AND ($9::timestamptz IS NULL OR GREATEST(r.updated_at,c.updated_at) > $9)
), incident AS (
    SELECT DISTINCT x.topic_id,r.id AS relation_id,r.updated_at
    FROM (
        SELECT t.id AS topic_id,t.id AS node_id,'TOPIC'::text AS node_type FROM core.topic t WHERE t.workspace_id=$1
        UNION ALL
        SELECT m.topic_id,m.claim_id,'CLAIM'::text FROM memberships m
    ) x
    JOIN core.relation r ON r.workspace_id=$1 AND (
        (r.source_node_type=x.node_type AND r.source_node_id=x.node_id) OR
        (r.target_node_type=x.node_type AND r.target_node_id=x.node_id)
    )
    WHERE r.status=ANY($6::text[])
      AND (cardinality($7::text[])=0 OR r.relation_type=ANY($7::text[]))
      AND ($8::double precision IS NULL OR (r.confidence_score IS NOT NULL AND r.confidence_score >= $8))
      AND ($9::timestamptz IS NULL OR r.updated_at > $9)
      AND (r.relation_type<>'BELONGS_TO' OR NOT $4 OR EXISTS (
          SELECT 1 FROM eligible_claims eligible_membership_claim
          WHERE eligible_membership_claim.id=r.source_node_id
      ))
)
SELECT t.id::text,t.workspace_id::text,t.name,t.description,t.status,t.version,t.updated_at,
       COUNT(DISTINCT m.claim_id)::int AS direct_claim_count,
       COUNT(DISTINCT i.relation_id)::int AS incident_relation_count,
       (COUNT(DISTINCT m.claim_id)+COUNT(DISTINCT i.relation_id))::int AS cluster_score,
       GREATEST(t.updated_at,COALESCE(MAX(m.updated_at),t.updated_at),COALESCE(MAX(i.updated_at),t.updated_at)) AS cluster_updated_at
FROM core.topic t
LEFT JOIN memberships m ON m.topic_id=t.id
LEFT JOIN incident i ON i.topic_id=t.id
WHERE t.workspace_id=$1 AND t.status='ACTIVE'
  AND (cardinality($2::uuid[])=0 OR t.id=ANY($2::uuid[]))
GROUP BY t.id,t.workspace_id,t.name,t.description,t.status,t.version,t.updated_at
HAVING $9::timestamptz IS NULL OR GREATEST(t.updated_at,COALESCE(MAX(m.updated_at),t.updated_at),COALESCE(MAX(i.updated_at),t.updated_at)) > $9
ORDER BY cluster_score DESC,cluster_updated_at DESC,t.id
LIMIT $10`

func containsNodeType(values []knowledge.NodeType, target knowledge.NodeType) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
