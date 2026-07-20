package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

// NodeDetail 读取一个 Workspace-scoped Topic 或 Claim 摘要。
func (repository *Repository) NodeDetail(ctx context.Context, workspaceID foundation.ID, ref knowledge.NodeRef) (graphdomain.GraphNode, error) {
	if repository == nil || repository.db == nil {
		return graphdomain.GraphNode{}, unavailable(errors.New("graph repository is unavailable"))
	}
	switch ref.Type {
	case knowledge.NodeTypeTopic:
		node, err := scanTopicNode(repository.db.QueryRow(ctx, topicDetailSQL, string(workspaceID), string(ref.ID)))
		if errors.Is(err, pgx.ErrNoRows) {
			return graphdomain.GraphNode{}, notFound(graphdomain.ErrorCodeNodeNotFound, err)
		}
		if err != nil {
			return graphdomain.GraphNode{}, classifyProjectionScan(err)
		}
		return graphdomain.GraphNode{Topic: &node}, nil
	case knowledge.NodeTypeClaim:
		node, err := scanClaimNode(repository.db.QueryRow(ctx, claimDetailSQL, string(workspaceID), string(ref.ID)))
		if errors.Is(err, pgx.ErrNoRows) {
			return graphdomain.GraphNode{}, notFound(graphdomain.ErrorCodeNodeNotFound, err)
		}
		if err != nil {
			return graphdomain.GraphNode{}, classifyProjectionScan(err)
		}
		return graphdomain.GraphNode{Claim: &node}, nil
	default:
		return graphdomain.GraphNode{}, notFound(graphdomain.ErrorCodeNodeNotFound, pgx.ErrNoRows)
	}
}

// RelationDetail 读取 Relation 本体和 Evidence 摘要，不加载 Evidence 正文。
func (repository *Repository) RelationDetail(ctx context.Context, workspaceID, relationID foundation.ID) (graphdomain.RelationDetail, error) {
	if repository == nil || repository.db == nil {
		return graphdomain.RelationDetail{}, unavailable(errors.New("graph repository is unavailable"))
	}
	var detail graphdomain.RelationDetail
	var id, storedWorkspace, sourceType, sourceID, targetType, targetID, relationType, status string
	var confirmationMethod, confirmationRef, evidenceFingerprint *string
	err := repository.db.QueryRow(ctx, relationDetailSQL, string(workspaceID), string(relationID)).Scan(
		&id, &storedWorkspace, &sourceType, &sourceID, &targetType, &targetID, &relationType, &status,
		&confirmationMethod, &confirmationRef, &detail.Edge.Confidence, &detail.ValidFrom, &detail.ValidTo,
		&detail.Fingerprint, &evidenceFingerprint, &detail.Edge.Version, &detail.CreatedAt, &detail.UpdatedAt,
		&detail.Edge.EvidenceCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return graphdomain.RelationDetail{}, notFound(graphdomain.ErrorCodeRelationNotFound, err)
	}
	if err != nil {
		return graphdomain.RelationDetail{}, classify(err)
	}
	detail.Edge.RelationID = idValue(id)
	detail.Edge.WorkspaceID = idValue(storedWorkspace)
	detail.Edge.Source = knowledge.NodeRef{Type: knowledge.NodeType(sourceType), ID: idValue(sourceID)}
	detail.Edge.Target = knowledge.NodeRef{Type: knowledge.NodeType(targetType), ID: idValue(targetID)}
	detail.Edge.Type = knowledge.RelationType(relationType)
	detail.Edge.Status = knowledge.RelationStatus(status)
	detail.Edge.Traversal = graphdomain.EdgeTraversalForward
	detail.Edge.UpdatedAt = detail.UpdatedAt
	detail.Edge.EvidenceHref = nodeEvidenceHref(detail.Edge.RelationID)
	if evidenceFingerprint != nil {
		detail.Edge.EvidenceFingerprint = *evidenceFingerprint
	}
	if confirmationMethod != nil || confirmationRef != nil {
		if confirmationMethod == nil || confirmationRef == nil {
			return graphdomain.RelationDetail{}, inconsistent(errors.New("relation confirmation pair is inconsistent"))
		}
		confirmation := knowledge.Confirmation{Method: knowledge.ConfirmationMethod(*confirmationMethod), Reference: *confirmationRef}
		detail.Confirmation = &confirmation
	}
	return detail, nil
}

const topicDetailSQL = `
SELECT id::text,workspace_id::text,name,description,status,version,updated_at
FROM core.topic WHERE workspace_id=$1 AND id=$2`

const claimDetailSQL = `
SELECT id::text,workspace_id::text,statement,applicability,applicability_schema_version,
       applicability_hash,status,confidence_score,version,updated_at
FROM core.claim WHERE workspace_id=$1 AND id=$2`

const relationDetailSQL = `
SELECT r.id::text,r.workspace_id::text,r.source_node_type,r.source_node_id::text,
       r.target_node_type,r.target_node_id::text,r.relation_type,r.status,
       r.confirmation_method,r.confirmation_ref,r.confidence_score,r.valid_from,r.valid_to,
       r.fingerprint,r.evidence_fingerprint,r.version,r.created_at,r.updated_at,
       (SELECT COUNT(*)::int FROM core.relation_evidence e WHERE e.workspace_id=r.workspace_id AND e.relation_id=r.id)
FROM core.relation r
WHERE r.workspace_id=$1 AND r.id=$2`
