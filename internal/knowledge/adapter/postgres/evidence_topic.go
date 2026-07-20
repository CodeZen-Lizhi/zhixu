package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// ResolveEvidenceTopics 使用单个参数化 SQL 解析正式证据绑定的 Active Topic。
func (r *Repository) ResolveEvidenceTopics(ctx context.Context, workspaceID foundation.ID, provenance []domain.ProvenanceRef) ([]domain.EvidenceTopicBinding, error) {
	if len(provenance) == 0 || len(provenance) > domain.MaxBatchLimit {
		return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge evidence topic query size is invalid"))
	}
	seen := make(map[domain.ProvenanceRef]struct{}, len(provenance))
	sourceVersionIDs := make([]string, len(provenance))
	sourceSpanIDs := make([]string, len(provenance))
	for index, ref := range provenance {
		if ref.WorkspaceID != workspaceID || domain.ValidateProvenanceRef(ref) != nil {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge evidence topic query provenance is invalid"))
		}
		if _, duplicate := seen[ref]; duplicate {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge evidence topic query contains duplicate provenance"))
		}
		seen[ref] = struct{}{}
		sourceVersionIDs[index] = string(ref.SourceVersionID)
		sourceSpanIDs[index] = string(ref.SourceSpanID)
	}
	rows, err := r.db.Query(ctx, resolveEvidenceTopicsSQL, string(workspaceID), sourceVersionIDs, sourceSpanIDs)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	result := make([]domain.EvidenceTopicBinding, 0)
	previous := ""
	for rows.Next() {
		var sourceVersionID, sourceSpanID, topicID, topicName string
		if err := rows.Scan(&sourceVersionID, &sourceSpanID, &topicID, &topicName); err != nil {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, err)
		}
		binding := domain.EvidenceTopicBinding{
			Provenance: domain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: foundation.ID(sourceVersionID), SourceSpanID: foundation.ID(sourceSpanID)},
			TopicID:    foundation.ID(topicID), TopicName: topicName,
		}
		key := sourceVersionID + "\x00" + sourceSpanID + "\x00" + topicID
		if domain.ValidateEvidenceTopicBinding(binding) != nil || (previous != "" && key <= previous) {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge evidence topic row is invalid, duplicate, or unordered"))
		}
		previous = key
		result = append(result, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	return result, nil
}

const resolveEvidenceTopicsSQL = `
	WITH requested AS (
		SELECT input.source_version_id,input.source_span_id
		FROM unnest($2::uuid[],$3::uuid[]) AS input(source_version_id,source_span_id)
	), candidates AS (
		SELECT requested.source_version_id,requested.source_span_id,relation.target_node_id AS topic_id
		FROM requested
		JOIN core.claim_source source
		  ON source.workspace_id=$1
		 AND source.source_version_id=requested.source_version_id
		 AND source.source_span_id=requested.source_span_id
		JOIN core.claim claim
		  ON claim.id=source.claim_id AND claim.workspace_id=source.workspace_id
		JOIN core.relation relation
		  ON relation.workspace_id=claim.workspace_id
		 AND relation.source_node_type='CLAIM'
		 AND relation.source_node_id=claim.id
		 AND relation.target_node_type='TOPIC'
		 AND relation.relation_type='BELONGS_TO'
		 AND relation.status='CONFIRMED'
		WHERE claim.status IN ('CONFIRMED','DISPUTED')
		UNION
		SELECT requested.source_version_id,requested.source_span_id,endpoint.node_id
		FROM requested
		JOIN core.relation_evidence evidence
		  ON evidence.workspace_id=$1
		 AND evidence.source_version_id=requested.source_version_id
		 AND evidence.source_span_id=requested.source_span_id
		JOIN core.relation relation
		  ON relation.id=evidence.relation_id
		 AND relation.workspace_id=evidence.workspace_id
		 AND relation.status='CONFIRMED'
		CROSS JOIN LATERAL (VALUES
			(relation.source_node_type,relation.source_node_id),
			(relation.target_node_type,relation.target_node_id)
		) AS endpoint(node_type,node_id)
		WHERE endpoint.node_type='TOPIC'
	)
	SELECT candidates.source_version_id::text,candidates.source_span_id::text,topic.id::text,topic.name
	FROM candidates
	JOIN core.topic topic
	  ON topic.id=candidates.topic_id AND topic.workspace_id=$1 AND topic.status='ACTIVE'
	ORDER BY candidates.source_version_id,candidates.source_span_id,topic.id
`
