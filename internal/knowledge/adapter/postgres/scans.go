package postgres

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

type rowScanner interface{ Scan(...any) error }

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

const claimSelect = `
	SELECT id::text,workspace_id::text,statement,normalized_statement,applicability,
		applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,
		fingerprint,version,created_at,updated_at
	FROM core.claim`

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

const relationSelect = `
	SELECT id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,
		relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,
		fingerprint,evidence_fingerprint,version,created_at,updated_at
	FROM core.relation`

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
