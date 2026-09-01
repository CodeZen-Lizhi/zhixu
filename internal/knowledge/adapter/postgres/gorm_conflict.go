package postgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// OpenConflict atomically creates a Conflict and marks disputable member
// Claims while preserving the common Knowledge command receipt boundary.
func (repository *GORMRepository) OpenConflict(ctx context.Context, record domain.OpenConflictRecord) (domain.ConflictResult, error) {
	if len(record.Members) > domain.MaxBatchLimit {
		return domain.ConflictResult{}, invalidQuery(errors.New("conflict member count exceeds repository limit"))
	}
	if err := domain.ValidateConflictAggregate(record.Conflict, record.Members); err != nil {
		return domain.ConflictResult{}, err
	}
	if record.Conflict.Status != domain.ConflictStatusOpen || record.Conflict.Version != 1 ||
		record.Conflict.Resolution != nil || record.Conflict.ResolutionReference != nil {
		return domain.ConflictResult{}, consistency(domain.ErrorCodeConflictInvalid, errors.New("conflict open requires an open version-one aggregate"))
	}
	var result domain.ConflictResult
	err := repository.gormCommand(ctx, record.Conflict.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandOpenConflict, aggregateConflict,
		func(callbackCtx context.Context, transaction *gorm.DB, receipt *gormKnowledgeReceipt) error {
			if receipt != nil {
				loaded, loadErr := gormKnowledgeLoadConflictResult(callbackCtx, transaction, record.Conflict.WorkspaceID, receipt.AggregateID)
				if loadErr != nil {
					return gormKnowledgeReadError(callbackCtx, loadErr, errorCodeConflictNotFound)
				}
				if err := gormKnowledgeAssertReceipt(receipt, loaded.Conflict.ID, loaded.Conflict.Version, true); err != nil {
					return err
				}
				loaded.Replayed = true
				result = loaded
				return nil
			}

			orderedMembers := append([]domain.ConflictMember(nil), record.Members...)
			sort.Slice(orderedMembers, func(left, right int) bool { return orderedMembers[left].ClaimID < orderedMembers[right].ClaimID })
			if err := gormKnowledgeLockConflictMembers(callbackCtx, transaction, record.Conflict, orderedMembers); err != nil {
				return err
			}
			inserted, insertErr := gormKnowledgeInsertConflict(callbackCtx, transaction, record.Conflict, orderedMembers)
			replayed := false
			if gormKnowledgeNoRows(insertErr) {
				loaded, loadErr := gormKnowledgeLoadOpenConflictByFingerprint(callbackCtx, transaction, record.Conflict.WorkspaceID, record.Conflict.Fingerprint)
				if loadErr != nil {
					return gormKnowledgeReadError(callbackCtx, loadErr, errorCodeConflictNotFound)
				}
				if !equivalentOpenConflict(loaded, record.Conflict, orderedMembers) {
					return versionConflict(errors.New("open conflict fingerprint is already bound to a different payload"))
				}
				inserted, replayed = loaded.Conflict, true
			} else if insertErr != nil {
				return classifyGORMKnowledge(callbackCtx, insertErr, errorCodeDatabaseUnavailable)
			}
			if !replayed {
				if err := gormKnowledgeInsertConflictMembers(callbackCtx, transaction, orderedMembers); err != nil {
					return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
				}
				if _, err := gormKnowledgeExec(callbackCtx, transaction, `
					UPDATE core.claim
					SET status='DISPUTED',version=version+1,updated_at=GREATEST(updated_at,?)
					WHERE workspace_id=? AND id=ANY(?::uuid[]) AND status='CONFIRMED'`,
					record.Conflict.CreatedAt.UTC(), string(record.Conflict.WorkspaceID), pq.Array(memberClaimIDs(orderedMembers))); err != nil {
					return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
				}
			}
			if err := gormKnowledgeInsertReceipt(callbackCtx, transaction, record.Conflict.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandOpenConflict, aggregateConflict, inserted.ID, inserted.Version, record.Conflict.CreatedAt); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			loaded, loadErr := gormKnowledgeLoadConflictResult(callbackCtx, transaction, record.Conflict.WorkspaceID, inserted.ID)
			if loadErr != nil {
				return gormKnowledgeReadError(callbackCtx, loadErr, errorCodeConflictNotFound)
			}
			loaded.Replayed = replayed
			result = loaded
			return nil
		})
	if err != nil {
		return domain.ConflictResult{}, err
	}
	return result, nil
}

// TransitionConflict performs the same node-lock and Conflict CAS sequence as
// the legacy adapter before persisting the immutable command receipt.
func (repository *GORMRepository) TransitionConflict(ctx context.Context, record domain.TransitionConflictRecord) (domain.ConflictResult, error) {
	var result domain.ConflictResult
	err := repository.gormCommand(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionConflict, aggregateConflict,
		func(callbackCtx context.Context, transaction *gorm.DB, receipt *gormKnowledgeReceipt) error {
			if receipt != nil {
				loaded, loadErr := gormKnowledgeLoadConflictResult(callbackCtx, transaction, record.WorkspaceID, receipt.AggregateID)
				if loadErr != nil {
					return gormKnowledgeReadError(callbackCtx, loadErr, errorCodeConflictNotFound)
				}
				if err := gormKnowledgeAssertReceipt(receipt, loaded.Conflict.ID, loaded.Conflict.Version, true); err != nil {
					return err
				}
				loaded.Replayed = true
				result = loaded
				return nil
			}

			identity, loadErr := gormKnowledgeLoadConflictResult(callbackCtx, transaction, record.WorkspaceID, record.ConflictID)
			if loadErr != nil {
				return gormKnowledgeReadError(callbackCtx, loadErr, errorCodeConflictNotFound)
			}
			refs := make([]domain.NodeRef, 0, len(identity.Members)+1)
			if identity.Conflict.TopicID != nil {
				refs = append(refs, domain.NodeRef{Type: domain.NodeTypeTopic, ID: *identity.Conflict.TopicID})
			}
			for _, member := range identity.Members {
				refs = append(refs, domain.NodeRef{Type: domain.NodeTypeClaim, ID: member.ClaimID})
			}
			if err := gormKnowledgeLockNodeRefs(callbackCtx, transaction, record.WorkspaceID, refs, false); err != nil {
				return err
			}
			stored, scanErr := gormKnowledgeScanConflict(callbackCtx, transaction, conflictSelect+` WHERE workspace_id=? AND id=? FOR UPDATE`, string(record.WorkspaceID), string(record.ConflictID))
			if scanErr != nil {
				return gormKnowledgeReadError(callbackCtx, scanErr, errorCodeConflictNotFound)
			}
			if stored.Conflict.Version != record.ExpectedVersion {
				return versionConflict(errors.New("conflict expected version is stale"))
			}
			if err := domain.ValidateConflictTransition(stored.Conflict.Status, record.Status); err != nil {
				return err
			}
			candidate := stored.Conflict
			candidate.Status, candidate.Resolution, candidate.ResolutionReference = record.Status, record.Resolution, record.ResolutionReference
			candidate.Version++
			candidate.UpdatedAt = record.At.UTC()
			if err := domain.ValidateConflictAggregate(candidate, identity.Members); err != nil {
				return err
			}
			var resolvedAt any
			if record.Status == domain.ConflictStatusResolved || record.Status == domain.ConflictStatusAcceptedDivergence {
				resolvedAt = record.At.UTC()
			}
			updated, updateErr := gormKnowledgeScanConflict(callbackCtx, transaction, `
				UPDATE core.conflict
				SET status=?,resolution=?,resolution_reference=?,resolved_at=?,version=version+1,updated_at=?
				WHERE workspace_id=? AND id=? AND version=?
				RETURNING id::text,workspace_id::text,topic_id::text,status,severity,summary,applicability_assessment,
					applicability_hash,overlap_reason,fingerprint,resolution,resolution_reference,version,created_at,updated_at,resolved_at`,
				string(record.Status), pointerString(record.Resolution), pointerString(record.ResolutionReference), resolvedAt,
				record.At.UTC(), string(record.WorkspaceID), string(record.ConflictID), record.ExpectedVersion)
			if gormKnowledgeNoRows(updateErr) {
				return versionConflict(updateErr)
			}
			if updateErr != nil {
				return classifyGORMKnowledge(callbackCtx, updateErr, errorCodeDatabaseUnavailable)
			}
			if err := validateStoredConflict(updated, identity.Members); err != nil {
				return err
			}
			if err := gormKnowledgeInsertReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionConflict, aggregateConflict, updated.Conflict.ID, updated.Conflict.Version, record.At); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			result = domain.ConflictResult{Conflict: updated.Conflict, Members: identity.Members}
			return nil
		})
	if err != nil {
		return domain.ConflictResult{}, err
	}
	return result, nil
}

// BatchGetConflicts loads bounded Conflict aggregates from one repeatable,
// read-only snapshot without per-Conflict member queries.
func (repository *GORMRepository) BatchGetConflicts(ctx context.Context, query domain.BatchGetConflictsQuery) ([]domain.ConflictWithMembers, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if err := domain.ValidateBatchQuery(query.WorkspaceID, query.IDs, query.Limit); err != nil {
		return nil, err
	}
	if err := validateConflictStatuses(query.Statuses); err != nil {
		return nil, err
	}
	var result []domain.ConflictWithMembers
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		rows, err := gormKnowledgeRawRows(callbackCtx, transaction, conflictSelect+`
			WHERE workspace_id=?
			  AND (cardinality(?::uuid[])=0 OR id=ANY(?::uuid[]))
			  AND (cardinality(?::text[])=0 OR status=ANY(?::text[]))
			ORDER BY updated_at DESC,id
			LIMIT ?`, string(query.WorkspaceID), pq.Array(idsAsStrings(query.IDs)), pq.Array(idsAsStrings(query.IDs)), pq.Array(statusStrings(query.Statuses)), pq.Array(statusStrings(query.Statuses)), query.Limit)
		if err != nil {
			return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
		}
		defer rows.Close()
		stored := make([]storedConflict, 0, query.Limit)
		for rows.Next() {
			conflict, scanErr := scanConflict(rows)
			if scanErr != nil {
				return consistency(domain.ErrorCodeConflictInvalid, scanErr)
			}
			stored = append(stored, conflict)
		}
		if err := rows.Err(); err != nil {
			return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
		}
		conflictIDs := make([]foundation.ID, len(stored))
		for index := range stored {
			conflictIDs[index] = stored[index].Conflict.ID
		}
		members, err := gormKnowledgeReadConflictMembers(callbackCtx, transaction, query.WorkspaceID, conflictIDs)
		if err != nil {
			return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
		}
		result = make([]domain.ConflictWithMembers, len(stored))
		for index, conflict := range stored {
			if err := validateStoredConflict(conflict, members[conflict.Conflict.ID]); err != nil {
				return err
			}
			result[index] = domain.ConflictWithMembers{Conflict: conflict.Conflict, Members: members[conflict.Conflict.ID]}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func gormKnowledgeReadConflictMembers(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, conflictIDs []foundation.ID) (map[foundation.ID][]domain.ConflictMember, error) {
	result := make(map[foundation.ID][]domain.ConflictMember, len(conflictIDs))
	for _, id := range conflictIDs {
		result[id] = []domain.ConflictMember{}
	}
	if len(conflictIDs) == 0 {
		return result, nil
	}
	rows, err := gormKnowledgeRawRows(ctx, database, `
		SELECT conflict_id::text,claim_id::text,workspace_id::text,applicability,
			applicability_schema_version,applicability_hash,position_summary,created_at
		FROM core.conflict_member
		WHERE workspace_id=? AND conflict_id=ANY(?::uuid[])
		ORDER BY conflict_id,claim_id`, string(workspaceID), pq.Array(idsAsStrings(conflictIDs)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		member, scanErr := scanConflictMember(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if _, ok := result[member.ConflictID]; !ok {
			return nil, consistency(domain.ErrorCodeConflictInvalid, errors.New("knowledge conflict member is outside requested aggregate set"))
		}
		result[member.ConflictID] = append(result[member.ConflictID], member)
	}
	return result, rows.Err()
}

func gormKnowledgeScanConflict(ctx context.Context, database *gorm.DB, query string, arguments ...any) (storedConflict, error) {
	row, err := gormKnowledgeRawRow(ctx, database, query, arguments...)
	if err != nil {
		return storedConflict{}, err
	}
	return scanConflict(row)
}

func gormKnowledgeLoadConflictResult(ctx context.Context, database *gorm.DB, workspaceID, conflictID foundation.ID) (domain.ConflictResult, error) {
	stored, err := gormKnowledgeScanConflict(ctx, database, conflictSelect+` WHERE workspace_id=? AND id=?`, string(workspaceID), string(conflictID))
	if err != nil {
		return domain.ConflictResult{}, err
	}
	members, err := gormKnowledgeReadConflictMembers(ctx, database, workspaceID, []foundation.ID{conflictID})
	if err != nil {
		return domain.ConflictResult{}, err
	}
	if err := validateStoredConflict(stored, members[conflictID]); err != nil {
		return domain.ConflictResult{}, err
	}
	return domain.ConflictResult{Conflict: stored.Conflict, Members: members[conflictID]}, nil
}

func gormKnowledgeLoadOpenConflictByFingerprint(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, fingerprint string) (domain.ConflictResult, error) {
	stored, err := gormKnowledgeScanConflict(ctx, database, conflictSelect+`
		WHERE workspace_id=? AND fingerprint=? AND status NOT IN ('RESOLVED','ACCEPTED_DIVERGENCE') FOR UPDATE`,
		string(workspaceID), fingerprint)
	if err != nil {
		return domain.ConflictResult{}, err
	}
	members, err := gormKnowledgeReadConflictMembers(ctx, database, workspaceID, []foundation.ID{stored.Conflict.ID})
	if err != nil {
		return domain.ConflictResult{}, err
	}
	if err := validateStoredConflict(stored, members[stored.Conflict.ID]); err != nil {
		return domain.ConflictResult{}, err
	}
	return domain.ConflictResult{Conflict: stored.Conflict, Members: members[stored.Conflict.ID]}, nil
}

func gormKnowledgeLockConflictMembers(ctx context.Context, database *gorm.DB, conflict domain.Conflict, members []domain.ConflictMember) error {
	if conflict.TopicID != nil {
		if err := gormKnowledgeLockNodeRefs(ctx, database, conflict.WorkspaceID, []domain.NodeRef{{Type: domain.NodeTypeTopic, ID: *conflict.TopicID}}, true); err != nil {
			return err
		}
	}
	expectedHashes := make(map[foundation.ID]string, len(members))
	for _, member := range members {
		expectedHashes[member.ClaimID] = member.ApplicabilityHash
	}
	rows, err := gormKnowledgeRawRows(ctx, database, `
		SELECT id::text,status,applicability_hash
		FROM core.claim
		WHERE workspace_id=? AND id=ANY(?::uuid[])
		ORDER BY id FOR UPDATE`, string(conflict.WorkspaceID), pq.Array(memberClaimIDs(members)))
	if err != nil {
		return classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id, status, applicabilityHash string
		if err := rows.Scan(&id, &status, &applicabilityHash); err != nil {
			return classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
		}
		claimID := foundation.ID(id)
		if status != string(domain.ClaimStatusConfirmed) && status != string(domain.ClaimStatusDisputed) {
			return consistency(domain.ErrorCodeConflictInvalid, errors.New("conflict member claim is not disputable"))
		}
		if expectedHashes[claimID] != applicabilityHash {
			return consistency(domain.ErrorCodeConflictInvalid, errors.New("conflict member applicability does not match claim"))
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
	}
	if seen != len(members) {
		return notFound(errorCodeClaimNotFound, errors.New("one or more conflict member claims are missing or cross-workspace"))
	}
	return nil
}

func gormKnowledgeInsertConflict(ctx context.Context, database *gorm.DB, conflict domain.Conflict, members []domain.ConflictMember) (domain.Conflict, error) {
	var exactHash, overlapReason any
	if conflict.ApplicabilityAssessment == domain.ApplicabilityAssessmentExact {
		exactHash = members[0].ApplicabilityHash
	} else {
		overlapReason = conflict.ReviewedOverlapReason
	}
	stored, err := gormKnowledgeScanConflict(ctx, database, `
		INSERT INTO core.conflict (
			id,workspace_id,topic_id,status,severity,summary,applicability_assessment,
			applicability_hash,overlap_reason,fingerprint,resolution,resolution_reference,
			version,created_at,updated_at,resolved_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,NULL,NULL,?,?,?,NULL)
		ON CONFLICT (workspace_id,fingerprint)
		WHERE status NOT IN ('RESOLVED','ACCEPTED_DIVERGENCE') DO NOTHING
		RETURNING id::text,workspace_id::text,topic_id::text,status,severity,summary,applicability_assessment,
			applicability_hash,overlap_reason,fingerprint,resolution,resolution_reference,version,created_at,updated_at,resolved_at`,
		string(conflict.ID), string(conflict.WorkspaceID), optionalIDValue(conflict.TopicID), string(conflict.Status),
		string(conflict.Severity), conflict.Summary, string(conflict.ApplicabilityAssessment), exactHash, overlapReason,
		conflict.Fingerprint, conflict.Version, conflict.CreatedAt.UTC(), conflict.UpdatedAt.UTC())
	if err != nil {
		return domain.Conflict{}, err
	}
	return stored.Conflict, nil
}

func gormKnowledgeInsertConflictMembers(ctx context.Context, database *gorm.DB, members []domain.ConflictMember) error {
	if len(members) == 0 {
		return nil
	}
	conflictIDs := make([]string, len(members))
	claimIDs := make([]string, len(members))
	workspaceIDs := make([]string, len(members))
	applicabilities := make([]string, len(members))
	applicabilityVersions := make([]string, len(members))
	applicabilityHashes := make([]string, len(members))
	positionSummaries := make([]string, len(members))
	createdAt := make([]time.Time, len(members))
	for index, member := range members {
		conflictIDs[index] = string(member.ConflictID)
		claimIDs[index] = string(member.ClaimID)
		workspaceIDs[index] = string(member.WorkspaceID)
		applicabilities[index] = string(member.Applicability.CanonicalJSON)
		applicabilityVersions[index] = member.Applicability.SchemaVersion
		applicabilityHashes[index] = member.ApplicabilityHash
		positionSummaries[index] = member.PositionSummary
		createdAt[index] = member.CreatedAt.UTC()
	}
	_, err := gormKnowledgeExec(ctx, database, `
		INSERT INTO core.conflict_member (
			conflict_id,claim_id,workspace_id,applicability,applicability_schema_version,
			applicability_hash,position_summary,created_at
		)
		SELECT input.conflict_id,input.claim_id,input.workspace_id,input.applicability::jsonb,
			input.applicability_schema_version,input.applicability_hash,input.position_summary,input.created_at
		FROM unnest(
			?::uuid[],?::uuid[],?::uuid[],?::text[],?::text[],?::text[],?::text[],?::timestamptz[]
		) AS input(
			conflict_id,claim_id,workspace_id,applicability,applicability_schema_version,
			applicability_hash,position_summary,created_at
		)`, pq.Array(conflictIDs), pq.Array(claimIDs), pq.Array(workspaceIDs), pq.Array(applicabilities),
		pq.Array(applicabilityVersions), pq.Array(applicabilityHashes), pq.Array(positionSummaries), pq.Array(createdAt))
	return err
}

var _ domain.Repository = (*GORMRepository)(nil)
