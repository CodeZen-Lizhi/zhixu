package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

var _ knowledgeapplication.EvidenceTopicRepository = (*GORMRepository)(nil)

// BatchCheckEvidenceEligibility checks the canonical provenance set inside a
// repeatable, read-only snapshot and fails closed on malformed persisted facts.
func (repository *GORMRepository) BatchCheckEvidenceEligibility(ctx context.Context, query domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	canonical, err := domain.CanonicalEvidenceEligibilityQuery(query)
	if err != nil {
		return nil, err
	}
	var result []domain.ProvenanceEligibility
	err = repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		items, queryErr := gormKnowledgeEvidenceEligibility(callbackCtx, transaction, canonical)
		if queryErr != nil {
			return queryErr
		}
		result = items
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func gormKnowledgeEvidenceEligibility(ctx context.Context, database *gorm.DB, query domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error) {
	sourceVersionIDs, sourceSpanIDs := gormKnowledgeProvenanceArrays(query.Provenance)
	rows, err := gormKnowledgeRawRows(ctx, database, gormEvidenceEligibilitySQL,
		pq.Array(sourceVersionIDs), pq.Array(sourceSpanIDs), string(query.WorkspaceID), string(query.WorkspaceID), string(query.WorkspaceID))
	if err != nil {
		return nil, classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	result := make([]domain.ProvenanceEligibility, len(query.Provenance))
	indexByKey := make(map[string]int, len(query.Provenance))
	for index, ref := range query.Provenance {
		result[index] = domain.ProvenanceEligibility{Provenance: ref}
		indexByKey[gormKnowledgeProvenanceKey(ref.SourceVersionID, ref.SourceSpanID)] = index
	}
	seen := make([]bool, len(result))
	for rows.Next() {
		var sourceVersionID, sourceSpanID string
		var ownerType, ownerID, evidenceID, ownerStatus, supportType, relationType *string
		var conflictIDs pq.StringArray
		var disputedApplicability []byte
		var disputedApplicabilitySchema, disputedApplicabilityHash *string
		var disputedClaimUpdatedAt *time.Time
		if err := rows.Scan(
			&sourceVersionID, &sourceSpanID, &ownerType, &ownerID, &evidenceID, &ownerStatus, &supportType, &relationType, &conflictIDs,
			&disputedApplicability, &disputedApplicabilitySchema, &disputedApplicabilityHash, &disputedClaimUpdatedAt,
		); err != nil {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, err)
		}
		resultIndex, ok := indexByKey[gormKnowledgeProvenanceKey(foundation.ID(sourceVersionID), foundation.ID(sourceSpanID))]
		if !ok {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge eligibility query returned unknown provenance"))
		}
		seen[resultIndex] = true
		if ownerType == nil {
			if ownerID != nil || evidenceID != nil || ownerStatus != nil || supportType != nil || relationType != nil || len(conflictIDs) != 0 ||
				len(disputedApplicability) != 0 || disputedApplicabilitySchema != nil || disputedApplicabilityHash != nil || disputedClaimUpdatedAt != nil {
				return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge eligibility empty row is inconsistent"))
			}
			continue
		}
		if ownerID == nil || evidenceID == nil || ownerStatus == nil {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge eligibility row binding is incomplete"))
		}
		binding := domain.EvidenceEligibilityBinding{
			OwnerType:  domain.EvidenceOwnerType(*ownerType),
			OwnerID:    foundation.ID(*ownerID),
			EvidenceID: foundation.ID(*evidenceID),
		}
		switch binding.OwnerType {
		case domain.EvidenceOwnerClaim:
			if supportType == nil || relationType != nil {
				return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge claim eligibility row is inconsistent"))
			}
			binding.ClaimStatus = domain.ClaimStatus(*ownerStatus)
			binding.SupportType = domain.ClaimSupportType(*supportType)
			binding.ConflictIDs = make([]foundation.ID, len(conflictIDs))
			for index := range conflictIDs {
				binding.ConflictIDs[index] = foundation.ID(conflictIDs[index])
			}
			if binding.ClaimStatus == domain.ClaimStatusDisputed {
				if len(disputedApplicability) == 0 || disputedApplicabilitySchema == nil || disputedApplicabilityHash == nil || disputedClaimUpdatedAt == nil {
					return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("disputed claim eligibility row lacks applicability or update time"))
				}
				parsed, parseErr := domain.ParseApplicability(json.RawMessage(disputedApplicability))
				if parseErr != nil || parsed.SchemaVersion != *disputedApplicabilitySchema || parsed.Hash != *disputedApplicabilityHash {
					return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("disputed claim applicability snapshot is inconsistent"))
				}
				binding.DisputedApplicability = parsed
				binding.DisputedClaimUpdatedAtUTC = disputedClaimUpdatedAt.UTC()
			} else if len(disputedApplicability) != 0 || disputedApplicabilitySchema != nil || disputedApplicabilityHash != nil || disputedClaimUpdatedAt != nil {
				return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("non-disputed claim eligibility row contains disputed metadata"))
			}
		case domain.EvidenceOwnerRelation:
			if supportType != nil || relationType == nil || len(conflictIDs) != 0 || len(disputedApplicability) != 0 ||
				disputedApplicabilitySchema != nil || disputedApplicabilityHash != nil || disputedClaimUpdatedAt != nil {
				return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge relation eligibility row is inconsistent"))
			}
			binding.RelationStatus = domain.RelationStatus(*ownerStatus)
			binding.RelationType = domain.RelationType(*relationType)
		default:
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge eligibility row owner is invalid"))
		}
		result[resultIndex].Bindings = append(result[resultIndex].Bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
	}
	for index := range result {
		if !seen[index] {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge eligibility query omitted requested provenance"))
		}
		sort.Slice(result[index].Bindings, func(left, right int) bool {
			leftBinding, rightBinding := result[index].Bindings[left], result[index].Bindings[right]
			if leftBinding.OwnerType != rightBinding.OwnerType {
				return leftBinding.OwnerType < rightBinding.OwnerType
			}
			if leftBinding.OwnerID != rightBinding.OwnerID {
				return leftBinding.OwnerID < rightBinding.OwnerID
			}
			return leftBinding.EvidenceID < rightBinding.EvidenceID
		})
		result[index].Eligibility = domain.ClassifyEvidenceEligibility(result[index].Bindings)
		if err := domain.ValidateProvenanceEligibility(result[index]); err != nil {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, err)
		}
	}
	return result, nil
}

// ResolveEvidenceTopics resolves active Topics from formal evidence bindings in
// one parameterized, repeatable read-only query.
func (repository *GORMRepository) ResolveEvidenceTopics(ctx context.Context, workspaceID foundation.ID, provenance []domain.ProvenanceRef) ([]domain.EvidenceTopicBinding, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if len(provenance) == 0 || len(provenance) > domain.MaxBatchLimit {
		return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge evidence topic query size is invalid"))
	}
	seen := make(map[domain.ProvenanceRef]struct{}, len(provenance))
	for _, ref := range provenance {
		if ref.WorkspaceID != workspaceID || domain.ValidateProvenanceRef(ref) != nil {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge evidence topic query provenance is invalid"))
		}
		if _, duplicate := seen[ref]; duplicate {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge evidence topic query contains duplicate provenance"))
		}
		seen[ref] = struct{}{}
	}
	var result []domain.EvidenceTopicBinding
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		sourceVersionIDs, sourceSpanIDs := gormKnowledgeProvenanceArrays(provenance)
		rows, queryErr := gormKnowledgeRawRows(callbackCtx, transaction, gormResolveEvidenceTopicsSQL,
			pq.Array(sourceVersionIDs), pq.Array(sourceSpanIDs), string(workspaceID), string(workspaceID), string(workspaceID))
		if queryErr != nil {
			return classifyGORMKnowledge(callbackCtx, queryErr, errorCodeDatabaseUnavailable)
		}
		defer rows.Close()
		bindings := make([]domain.EvidenceTopicBinding, 0)
		previous := ""
		for rows.Next() {
			var sourceVersionID, sourceSpanID, topicID, topicName string
			if err := rows.Scan(&sourceVersionID, &sourceSpanID, &topicID, &topicName); err != nil {
				return consistency(domain.ErrorCodeEvidenceEligibilityInvalid, err)
			}
			binding := domain.EvidenceTopicBinding{
				Provenance: domain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: foundation.ID(sourceVersionID), SourceSpanID: foundation.ID(sourceSpanID)},
				TopicID:    foundation.ID(topicID),
				TopicName:  topicName,
			}
			key := sourceVersionID + "\x00" + sourceSpanID + "\x00" + topicID
			if domain.ValidateEvidenceTopicBinding(binding) != nil || (previous != "" && key <= previous) {
				return consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge evidence topic row is invalid, duplicate, or unordered"))
			}
			previous = key
			bindings = append(bindings, binding)
		}
		if err := rows.Err(); err != nil {
			return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
		}
		result = bindings
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func gormKnowledgeProvenanceArrays(provenance []domain.ProvenanceRef) ([]string, []string) {
	sourceVersionIDs := make([]string, len(provenance))
	sourceSpanIDs := make([]string, len(provenance))
	for index, ref := range provenance {
		sourceVersionIDs[index] = string(ref.SourceVersionID)
		sourceSpanIDs[index] = string(ref.SourceSpanID)
	}
	return sourceVersionIDs, sourceSpanIDs
}

func gormKnowledgeProvenanceKey(sourceVersionID, sourceSpanID foundation.ID) string {
	return string(sourceVersionID) + "\x00" + string(sourceSpanID)
}

const gormEvidenceEligibilitySQL = `
	WITH requested AS (
		SELECT input.source_version_id,input.source_span_id
		FROM unnest(?::uuid[],?::uuid[]) AS input(source_version_id,source_span_id)
	), bindings AS (
		SELECT requested.source_version_id,requested.source_span_id,
			'CLAIM'::text AS owner_type,source.claim_id AS owner_id,source.id AS evidence_id,
			claim.status AS owner_status,source.support_type,NULL::text AS relation_type,
			claim.applicability AS owner_applicability,claim.applicability_schema_version AS owner_applicability_schema,
			claim.applicability_hash AS owner_applicability_hash,claim.updated_at AS owner_updated_at
		FROM requested
		JOIN core.claim_source source
		  ON source.workspace_id=?
		 AND source.source_version_id=requested.source_version_id
		 AND source.source_span_id=requested.source_span_id
		JOIN core.claim claim
		  ON claim.id=source.claim_id AND claim.workspace_id=source.workspace_id
		WHERE claim.status IN ('CONFIRMED','DISPUTED')
		UNION ALL
		SELECT requested.source_version_id,requested.source_span_id,
			'RELATION'::text,evidence.relation_id,evidence.id,relation.status,NULL::text,relation.relation_type,
			NULL::jsonb,NULL::text,NULL::text,NULL::timestamptz
		FROM requested
		JOIN core.relation_evidence evidence
		  ON evidence.workspace_id=?
		 AND evidence.source_version_id=requested.source_version_id
		 AND evidence.source_span_id=requested.source_span_id
		JOIN core.relation relation
		  ON relation.id=evidence.relation_id AND relation.workspace_id=evidence.workspace_id
		WHERE relation.status='CONFIRMED'
	), requested_disputed_claims AS (
		SELECT DISTINCT bindings.owner_id AS claim_id
		FROM bindings
		WHERE bindings.owner_type='CLAIM'
		  AND bindings.owner_status='DISPUTED'
	), claim_conflicts AS (
		SELECT member.claim_id,array_agg(DISTINCT member.conflict_id::text ORDER BY member.conflict_id::text) AS conflict_ids
		FROM requested_disputed_claims requested_claim
		JOIN core.conflict_member member
		  ON member.workspace_id=?
		 AND member.claim_id=requested_claim.claim_id
		JOIN core.conflict conflict
		  ON conflict.id=member.conflict_id AND conflict.workspace_id=member.workspace_id
		GROUP BY member.claim_id
	)
	SELECT requested.source_version_id::text,requested.source_span_id::text,
		bindings.owner_type,bindings.owner_id::text,bindings.evidence_id::text,bindings.owner_status,
		bindings.support_type,bindings.relation_type,
		CASE WHEN bindings.owner_type='CLAIM' AND bindings.owner_status='DISPUTED'
			THEN COALESCE(claim_conflicts.conflict_ids,ARRAY[]::text[])
			ELSE ARRAY[]::text[] END AS conflict_ids,
		CASE WHEN bindings.owner_type='CLAIM' AND bindings.owner_status='DISPUTED' THEN bindings.owner_applicability END,
		CASE WHEN bindings.owner_type='CLAIM' AND bindings.owner_status='DISPUTED' THEN bindings.owner_applicability_schema END,
		CASE WHEN bindings.owner_type='CLAIM' AND bindings.owner_status='DISPUTED' THEN bindings.owner_applicability_hash END,
		CASE WHEN bindings.owner_type='CLAIM' AND bindings.owner_status='DISPUTED' THEN bindings.owner_updated_at END
	FROM requested
	LEFT JOIN bindings
	  ON bindings.source_version_id=requested.source_version_id
	 AND bindings.source_span_id=requested.source_span_id
	LEFT JOIN claim_conflicts
	  ON bindings.owner_type='CLAIM' AND claim_conflicts.claim_id=bindings.owner_id
	ORDER BY requested.source_version_id,requested.source_span_id,
		bindings.owner_type NULLS FIRST,bindings.owner_id,bindings.evidence_id`

const gormResolveEvidenceTopicsSQL = `
	WITH requested AS (
		SELECT input.source_version_id,input.source_span_id
		FROM unnest(?::uuid[],?::uuid[]) AS input(source_version_id,source_span_id)
	), candidates AS (
		SELECT requested.source_version_id,requested.source_span_id,relation.target_node_id AS topic_id
		FROM requested
		JOIN core.claim_source source
		  ON source.workspace_id=?
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
		  ON evidence.workspace_id=?
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
	  ON topic.id=candidates.topic_id AND topic.workspace_id=? AND topic.status='ACTIVE'
	ORDER BY candidates.source_version_id,candidates.source_span_id,topic.id`
