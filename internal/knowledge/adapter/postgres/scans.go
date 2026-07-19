package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

type rowScanner interface{ Scan(...any) error }

func getTopic(ctx context.Context, queryer queryRower, workspaceID, topicID foundation.ID) (domain.Topic, error) {
	topic, err := scanTopic(queryer.QueryRow(ctx, `
		SELECT id::text,workspace_id::text,name,normalized_name,description,status,
			merged_into_topic_id::text,version,created_at,updated_at
		FROM core.topic WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(topicID)))
	if err != nil {
		return domain.Topic{}, err
	}
	rows, err := queryRows(ctx, queryer, `
		SELECT alias,normalized_alias
		FROM core.topic_alias
		WHERE workspace_id=$1 AND topic_id=$2
		ORDER BY normalized_alias,id`, string(workspaceID), string(topicID))
	if err != nil {
		return domain.Topic{}, err
	}
	defer rows.Close()
	topic.Aliases = make([]domain.TopicAlias, 0)
	for rows.Next() {
		var alias domain.TopicAlias
		if err := rows.Scan(&alias.Name, &alias.NormalizedName); err != nil {
			return domain.Topic{}, err
		}
		topic.Aliases = append(topic.Aliases, alias)
	}
	if err := rows.Err(); err != nil {
		return domain.Topic{}, err
	}
	if err := domain.ValidateTopic(topic); err != nil {
		return domain.Topic{}, consistency(domain.ErrorCodeTopicInvalid, err)
	}
	return topic, nil
}

type rowQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func queryRows(ctx context.Context, queryer any, sql string, args ...any) (pgx.Rows, error) {
	rows, ok := queryer.(rowQueryer)
	if !ok {
		return nil, errors.New("knowledge query boundary does not support rows")
	}
	return rows.Query(ctx, sql, args...)
}

func scanTopic(row rowScanner) (domain.Topic, error) {
	var topic domain.Topic
	var id, workspaceID, status string
	var mergedInto *string
	if err := row.Scan(&id, &workspaceID, &topic.Name, &topic.NormalizedName, &topic.Description, &status, &mergedInto, &topic.Version, &topic.CreatedAt, &topic.UpdatedAt); err != nil {
		return domain.Topic{}, err
	}
	topic.ID = foundation.ID(id)
	topic.WorkspaceID = foundation.ID(workspaceID)
	topic.Status = domain.TopicStatus(status)
	if mergedInto != nil {
		value := foundation.ID(*mergedInto)
		topic.MergedIntoTopicID = &value
	}
	return topic, nil
}

func scanClaim(row rowScanner) (domain.Claim, error) {
	var claim domain.Claim
	var id, workspaceID, schemaVersion, applicabilityHash, status string
	var applicabilityRaw, factorsRaw []byte
	if err := row.Scan(
		&id, &workspaceID, &claim.Statement, &claim.NormalizedStatement, &applicabilityRaw,
		&schemaVersion, &applicabilityHash, &status, &claim.ConfidenceScore, &factorsRaw,
		&claim.Fingerprint, &claim.Version, &claim.CreatedAt, &claim.UpdatedAt,
	); err != nil {
		return domain.Claim{}, err
	}
	applicability, err := storedApplicability(applicabilityRaw, schemaVersion, applicabilityHash)
	if err != nil {
		return domain.Claim{}, err
	}
	factors, err := domain.NormalizeConfidenceFactors(json.RawMessage(factorsRaw))
	if err != nil {
		return domain.Claim{}, err
	}
	claim.ID = foundation.ID(id)
	claim.WorkspaceID = foundation.ID(workspaceID)
	claim.Applicability = applicability
	claim.ConfidenceFactors = factors
	claim.Status = domain.ClaimStatus(status)
	return claim, nil
}

func scanClaimSource(row rowScanner) (domain.ClaimSource, error) {
	var source domain.ClaimSource
	var id, workspaceID, claimID, sourceVersionID, sourceSpanID, supportType string
	if err := row.Scan(
		&id, &workspaceID, &claimID, &sourceVersionID, &sourceSpanID, &supportType,
		&source.Reason, &source.EvidenceHash, &source.ModelRunRef, &source.CreatedAt,
	); err != nil {
		return domain.ClaimSource{}, err
	}
	source.ID = foundation.ID(id)
	source.WorkspaceID = foundation.ID(workspaceID)
	source.ClaimID = foundation.ID(claimID)
	source.Provenance = domain.ProvenanceRef{WorkspaceID: source.WorkspaceID, SourceVersionID: foundation.ID(sourceVersionID), SourceSpanID: foundation.ID(sourceSpanID)}
	source.SupportType = domain.ClaimSupportType(supportType)
	return source, nil
}

func loadClaimResult(ctx context.Context, queryer interface {
	queryRower
	rowQueryer
}, workspaceID, claimID foundation.ID) (domain.ClaimResult, error) {
	claim, err := scanClaim(queryer.QueryRow(ctx, claimSelect+` WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(claimID)))
	if err != nil {
		return domain.ClaimResult{}, err
	}
	sources, err := loadClaimSources(ctx, queryer, workspaceID, []foundation.ID{claimID})
	if err != nil {
		return domain.ClaimResult{}, err
	}
	if err := domain.ValidateClaimAggregate(claim, sources[claimID]); err != nil {
		return domain.ClaimResult{}, consistency(domain.ErrorCodeClaimInvalid, err)
	}
	return domain.ClaimResult{Claim: claim, Sources: sources[claimID]}, nil
}

const claimSelect = `
	SELECT id::text,workspace_id::text,statement,normalized_statement,applicability,
		applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,
		fingerprint,version,created_at,updated_at
	FROM core.claim`

func loadClaimSources(ctx context.Context, queryer rowQueryer, workspaceID foundation.ID, claimIDs []foundation.ID) (map[foundation.ID][]domain.ClaimSource, error) {
	result := make(map[foundation.ID][]domain.ClaimSource, len(claimIDs))
	for _, id := range claimIDs {
		result[id] = []domain.ClaimSource{}
	}
	if len(claimIDs) == 0 {
		return result, nil
	}
	rows, err := queryer.Query(ctx, `
		SELECT id::text,workspace_id::text,claim_id::text,source_version_id::text,source_span_id::text,
			support_type,reason,evidence_hash,model_run_ref,created_at
		FROM core.claim_source
		WHERE workspace_id=$1 AND claim_id=ANY($2::uuid[])
		ORDER BY claim_id,created_at,id`, string(workspaceID), idsAsStrings(claimIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		source, err := scanClaimSource(rows)
		if err != nil {
			return nil, err
		}
		result[source.ClaimID] = append(result[source.ClaimID], source)
	}
	return result, rows.Err()
}

func scanRelation(row rowScanner) (domain.Relation, error) {
	var relation domain.Relation
	var id, workspaceID, sourceType, sourceID, targetType, targetID, relationType, status string
	var confirmationMethod, confirmationRef, evidenceFingerprint *string
	if err := row.Scan(
		&id, &workspaceID, &sourceType, &sourceID, &targetType, &targetID, &relationType, &status,
		&confirmationMethod, &confirmationRef, &relation.ConfidenceScore, &relation.ValidFrom, &relation.ValidTo,
		&relation.Fingerprint, &evidenceFingerprint, &relation.Version, &relation.CreatedAt, &relation.UpdatedAt,
	); err != nil {
		return domain.Relation{}, err
	}
	relation.ID = foundation.ID(id)
	relation.WorkspaceID = foundation.ID(workspaceID)
	relation.Source = domain.NodeRef{Type: domain.NodeType(sourceType), ID: foundation.ID(sourceID)}
	relation.Target = domain.NodeRef{Type: domain.NodeType(targetType), ID: foundation.ID(targetID)}
	relation.Type = domain.RelationType(relationType)
	relation.Status = domain.RelationStatus(status)
	if confirmationMethod != nil || confirmationRef != nil {
		if confirmationMethod == nil || confirmationRef == nil {
			return domain.Relation{}, errors.New("knowledge relation confirmation pair is inconsistent")
		}
		relation.Confirmation = &domain.Confirmation{Method: domain.ConfirmationMethod(*confirmationMethod), Reference: *confirmationRef}
	}
	if evidenceFingerprint != nil {
		relation.EvidenceFingerprint = *evidenceFingerprint
	}
	return relation, nil
}

func scanRelationEvidence(row rowScanner) (domain.RelationEvidence, error) {
	var evidence domain.RelationEvidence
	var id, workspaceID, relationID, sourceVersionID, sourceSpanID string
	var schemaVersion, applicabilityHash string
	var applicabilityRaw []byte
	var confirmationMethod, confirmedBy *string
	if err := row.Scan(
		&id, &workspaceID, &relationID, &sourceVersionID, &sourceSpanID, &evidence.Reason,
		&applicabilityRaw, &schemaVersion, &applicabilityHash, &evidence.EvidenceHash,
		&evidence.ModelRunRef, &confirmationMethod, &confirmedBy, &evidence.CreatedAt,
	); err != nil {
		return domain.RelationEvidence{}, err
	}
	applicability, err := storedApplicability(applicabilityRaw, schemaVersion, applicabilityHash)
	if err != nil {
		return domain.RelationEvidence{}, err
	}
	evidence.ID = foundation.ID(id)
	evidence.WorkspaceID = foundation.ID(workspaceID)
	evidence.RelationID = foundation.ID(relationID)
	evidence.Provenance = domain.ProvenanceRef{WorkspaceID: evidence.WorkspaceID, SourceVersionID: foundation.ID(sourceVersionID), SourceSpanID: foundation.ID(sourceSpanID)}
	evidence.Applicability = applicability
	if confirmationMethod != nil || confirmedBy != nil {
		if confirmationMethod == nil || confirmedBy == nil {
			return domain.RelationEvidence{}, errors.New("knowledge relation evidence confirmation pair is inconsistent")
		}
		evidence.Confirmation = &domain.Confirmation{Method: domain.ConfirmationMethod(*confirmationMethod), Reference: *confirmedBy}
	}
	return evidence, nil
}

func loadRelationResult(ctx context.Context, queryer interface {
	queryRower
	rowQueryer
}, workspaceID, relationID foundation.ID) (domain.RelationResult, error) {
	relation, err := scanRelation(queryer.QueryRow(ctx, relationSelect+` WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(relationID)))
	if err != nil {
		return domain.RelationResult{}, err
	}
	evidence, err := loadRelationEvidence(ctx, queryer, workspaceID, []foundation.ID{relationID})
	if err != nil {
		return domain.RelationResult{}, err
	}
	if err := domain.ValidateRelationAggregate(relation, evidence[relationID]); err != nil {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationInvalid, err)
	}
	return domain.RelationResult{Relation: relation, Evidence: evidence[relationID]}, nil
}

const relationSelect = `
	SELECT id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,
		relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,
		fingerprint,evidence_fingerprint,version,created_at,updated_at
	FROM core.relation`

func loadRelationEvidence(ctx context.Context, queryer rowQueryer, workspaceID foundation.ID, relationIDs []foundation.ID) (map[foundation.ID][]domain.RelationEvidence, error) {
	result := make(map[foundation.ID][]domain.RelationEvidence, len(relationIDs))
	for _, id := range relationIDs {
		result[id] = []domain.RelationEvidence{}
	}
	if len(relationIDs) == 0 {
		return result, nil
	}
	rows, err := queryer.Query(ctx, `
		SELECT evidence.id::text,evidence.workspace_id::text,evidence.relation_id::text,
			evidence.source_version_id::text,evidence.source_span_id::text,evidence.reason,
			evidence.applicability,evidence.applicability_schema_version,evidence.applicability_hash,
			evidence.evidence_hash,evidence.model_run_ref,evidence.confirmation_method,evidence.confirmed_by,
			evidence.created_at
		FROM core.relation_evidence evidence
		WHERE evidence.workspace_id=$1 AND evidence.relation_id=ANY($2::uuid[])
		ORDER BY evidence.relation_id,evidence.created_at,evidence.id`, string(workspaceID), idsAsStrings(relationIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		evidence, err := scanRelationEvidence(rows)
		if err != nil {
			return nil, err
		}
		result[evidence.RelationID] = append(result[evidence.RelationID], evidence)
	}
	return result, rows.Err()
}

type storedConflict struct {
	Conflict   domain.Conflict
	ExactHash  *string
	ResolvedAt *time.Time
}

func scanConflict(row rowScanner) (storedConflict, error) {
	var stored storedConflict
	var id, workspaceID, status, severity, assessment string
	var topicID, overlapReason *string
	if err := row.Scan(
		&id, &workspaceID, &topicID, &status, &severity, &stored.Conflict.Summary, &assessment,
		&stored.ExactHash, &overlapReason, &stored.Conflict.Fingerprint, &stored.Conflict.Resolution,
		&stored.Conflict.ResolutionReference, &stored.Conflict.Version, &stored.Conflict.CreatedAt,
		&stored.Conflict.UpdatedAt, &stored.ResolvedAt,
	); err != nil {
		return storedConflict{}, err
	}
	stored.Conflict.ID = foundation.ID(id)
	stored.Conflict.WorkspaceID = foundation.ID(workspaceID)
	if topicID != nil {
		value := foundation.ID(*topicID)
		stored.Conflict.TopicID = &value
	}
	stored.Conflict.Status = domain.ConflictStatus(status)
	stored.Conflict.Severity = domain.ConflictSeverity(severity)
	stored.Conflict.ApplicabilityAssessment = domain.ApplicabilityAssessment(assessment)
	if overlapReason != nil {
		stored.Conflict.ReviewedOverlapReason = *overlapReason
	}
	return stored, nil
}

const conflictSelect = `
	SELECT id::text,workspace_id::text,topic_id::text,status,severity,summary,applicability_assessment,
		applicability_hash,overlap_reason,fingerprint,resolution,resolution_reference,version,
		created_at,updated_at,resolved_at
	FROM core.conflict`

func scanConflictMember(row rowScanner) (domain.ConflictMember, error) {
	var member domain.ConflictMember
	var conflictID, claimID, workspaceID, schemaVersion, applicabilityHash string
	var applicabilityRaw []byte
	if err := row.Scan(
		&conflictID, &claimID, &workspaceID, &applicabilityRaw, &schemaVersion,
		&applicabilityHash, &member.PositionSummary, &member.CreatedAt,
	); err != nil {
		return domain.ConflictMember{}, err
	}
	applicability, err := storedApplicability(applicabilityRaw, schemaVersion, applicabilityHash)
	if err != nil {
		return domain.ConflictMember{}, err
	}
	member.ConflictID = foundation.ID(conflictID)
	member.ClaimID = foundation.ID(claimID)
	member.WorkspaceID = foundation.ID(workspaceID)
	member.Applicability = applicability
	member.ApplicabilityHash = applicabilityHash
	return member, nil
}

func loadConflictResult(ctx context.Context, queryer interface {
	queryRower
	rowQueryer
}, workspaceID, conflictID foundation.ID) (domain.ConflictResult, error) {
	stored, err := scanConflict(queryer.QueryRow(ctx, conflictSelect+` WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(conflictID)))
	if err != nil {
		return domain.ConflictResult{}, err
	}
	members, err := loadConflictMembers(ctx, queryer, workspaceID, []foundation.ID{conflictID})
	if err != nil {
		return domain.ConflictResult{}, err
	}
	if err := validateStoredConflict(stored, members[conflictID]); err != nil {
		return domain.ConflictResult{}, err
	}
	return domain.ConflictResult{Conflict: stored.Conflict, Members: members[conflictID]}, nil
}

func loadConflictMembers(ctx context.Context, queryer rowQueryer, workspaceID foundation.ID, conflictIDs []foundation.ID) (map[foundation.ID][]domain.ConflictMember, error) {
	result := make(map[foundation.ID][]domain.ConflictMember, len(conflictIDs))
	for _, id := range conflictIDs {
		result[id] = []domain.ConflictMember{}
	}
	if len(conflictIDs) == 0 {
		return result, nil
	}
	rows, err := queryer.Query(ctx, `
		SELECT conflict_id::text,claim_id::text,workspace_id::text,applicability,
			applicability_schema_version,applicability_hash,position_summary,created_at
		FROM core.conflict_member
		WHERE workspace_id=$1 AND conflict_id=ANY($2::uuid[])
		ORDER BY conflict_id,claim_id`, string(workspaceID), idsAsStrings(conflictIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		member, err := scanConflictMember(rows)
		if err != nil {
			return nil, err
		}
		result[member.ConflictID] = append(result[member.ConflictID], member)
	}
	return result, rows.Err()
}

func validateStoredConflict(stored storedConflict, members []domain.ConflictMember) error {
	terminal := stored.Conflict.Status == domain.ConflictStatusResolved || stored.Conflict.Status == domain.ConflictStatusAcceptedDivergence
	if terminal != (stored.ResolvedAt != nil) {
		return consistency(domain.ErrorCodeConflictInvalid, errors.New("conflict resolved timestamp is inconsistent"))
	}
	if stored.Conflict.ApplicabilityAssessment == domain.ApplicabilityAssessmentExact {
		if stored.ExactHash == nil || len(members) == 0 || *stored.ExactHash != members[0].ApplicabilityHash {
			return consistency(domain.ErrorCodeConflictInvalid, errors.New("exact conflict applicability hash is inconsistent"))
		}
	} else if stored.ExactHash != nil {
		return consistency(domain.ErrorCodeConflictInvalid, errors.New("reviewed overlap cannot store an exact applicability hash"))
	}
	if err := domain.ValidateConflictAggregate(stored.Conflict, members); err != nil {
		return consistency(domain.ErrorCodeConflictInvalid, err)
	}
	return nil
}

func storedApplicability(raw []byte, schemaVersion, storedHash string) (domain.Applicability, error) {
	parsed, err := domain.ParseApplicability(json.RawMessage(raw))
	if err != nil {
		return domain.Applicability{}, consistency(domain.ErrorCodeApplicabilityInvalid, err)
	}
	if parsed.SchemaVersion != schemaVersion || parsed.Hash != storedHash {
		return domain.Applicability{}, consistency(domain.ErrorCodeApplicabilityInvalid, errors.New("stored applicability metadata is inconsistent"))
	}
	return parsed, nil
}

func pointerEqual[T comparable](left, right *T) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func requireReceiptVersion(receipt *commandReceipt, currentVersion int64) error {
	if receipt.AggregateVersion <= 0 || currentVersion < receipt.AggregateVersion {
		return consistency(errorCodeStorageConsistency, fmt.Errorf("receipt version %d exceeds aggregate version %d", receipt.AggregateVersion, currentVersion))
	}
	return nil
}
