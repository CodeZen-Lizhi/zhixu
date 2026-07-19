package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// BatchCheckEvidenceEligibility 使用单个参数化 SQL 判断最多 500 个 Provenance 的正式知识资格。
func (r *Repository) BatchCheckEvidenceEligibility(ctx context.Context, query domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error) {
	canonical, err := domain.CanonicalEvidenceEligibilityQuery(query)
	if err != nil {
		return nil, err
	}
	sourceVersionIDs := make([]string, len(canonical.Provenance))
	sourceSpanIDs := make([]string, len(canonical.Provenance))
	for index, ref := range canonical.Provenance {
		sourceVersionIDs[index] = string(ref.SourceVersionID)
		sourceSpanIDs[index] = string(ref.SourceSpanID)
	}
	rows, err := r.db.Query(ctx, evidenceEligibilitySQL, string(canonical.WorkspaceID), sourceVersionIDs, sourceSpanIDs)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	result := make([]domain.ProvenanceEligibility, 0, len(canonical.Provenance))
	indexByKey := make(map[string]int, len(canonical.Provenance))
	for rows.Next() {
		var sourceVersionID, sourceSpanID string
		var ownerType, ownerID, evidenceID, ownerStatus, supportType, relationType *string
		var conflictIDs []string
		var disputedApplicability []byte
		var disputedApplicabilitySchema, disputedApplicabilityHash *string
		var disputedClaimUpdatedAt *time.Time
		if err := rows.Scan(
			&sourceVersionID, &sourceSpanID, &ownerType, &ownerID, &evidenceID, &ownerStatus, &supportType, &relationType, &conflictIDs,
			&disputedApplicability, &disputedApplicabilitySchema, &disputedApplicabilityHash, &disputedClaimUpdatedAt,
		); err != nil {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, err)
		}
		ref := domain.ProvenanceRef{WorkspaceID: canonical.WorkspaceID, SourceVersionID: foundation.ID(sourceVersionID), SourceSpanID: foundation.ID(sourceSpanID)}
		key := sourceVersionID + "\x00" + sourceSpanID
		resultIndex, ok := indexByKey[key]
		if !ok {
			resultIndex = len(result)
			indexByKey[key] = resultIndex
			result = append(result, domain.ProvenanceEligibility{Provenance: ref})
		}
		if ownerType == nil {
			continue
		}
		if ownerID == nil || evidenceID == nil || ownerStatus == nil {
			return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge eligibility row binding is incomplete"))
		}
		binding := domain.EvidenceEligibilityBinding{
			OwnerType: domain.EvidenceOwnerType(*ownerType), OwnerID: foundation.ID(*ownerID), EvidenceID: foundation.ID(*evidenceID),
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
				parsedApplicability, parseErr := domain.ParseApplicability(json.RawMessage(disputedApplicability))
				if parseErr != nil || parsedApplicability.SchemaVersion != *disputedApplicabilitySchema || parsedApplicability.Hash != *disputedApplicabilityHash {
					return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("disputed claim applicability snapshot is inconsistent"))
				}
				binding.DisputedApplicability = parsedApplicability
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
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	if len(result) != len(canonical.Provenance) {
		return nil, consistency(domain.ErrorCodeEvidenceEligibilityInvalid, errors.New("knowledge eligibility query omitted requested provenance"))
	}
	for index := range result {
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

const evidenceEligibilitySQL = `
	WITH requested AS (
		SELECT input.source_version_id,input.source_span_id
		FROM unnest($2::uuid[],$3::uuid[]) AS input(source_version_id,source_span_id)
	), bindings AS (
		SELECT requested.source_version_id,requested.source_span_id,
			'CLAIM'::text AS owner_type,source.claim_id AS owner_id,source.id AS evidence_id,
			claim.status AS owner_status,source.support_type,NULL::text AS relation_type,
			claim.applicability AS owner_applicability,claim.applicability_schema_version AS owner_applicability_schema,
			claim.applicability_hash AS owner_applicability_hash,claim.updated_at AS owner_updated_at
		FROM requested
		JOIN core.claim_source source
		  ON source.workspace_id=$1
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
		  ON evidence.workspace_id=$1
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
		  ON member.workspace_id=$1
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
