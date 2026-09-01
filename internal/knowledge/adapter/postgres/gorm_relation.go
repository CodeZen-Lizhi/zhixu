package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func (repository *GORMRepository) SuggestRelation(ctx context.Context, record domain.SuggestRelationRecord) (domain.RelationResult, error) {
	if len(record.Evidence) > domain.MaxBatchLimit {
		return domain.RelationResult{}, invalidQuery(errors.New("relation evidence count exceeds repository limit"))
	}
	record.Evidence = append([]domain.RelationEvidence(nil), record.Evidence...)
	for index := range record.Evidence {
		if record.Evidence[index].Confirmation != nil {
			return domain.RelationResult{}, consistency(domain.ErrorCodeRelationEvidenceInvalid, errors.New("suggested relation evidence cannot carry confirmation"))
		}
		if record.Evidence[index].EvidenceHash == "" {
			record.Evidence[index].EvidenceHash = domain.ComputeRelationEvidenceHash(record.Evidence[index])
		}
	}
	if record.Relation.EvidenceFingerprint == "" && len(record.Evidence) > 0 {
		record.Relation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint(record.Evidence)
	}
	if err := domain.ValidateRelationAggregate(record.Relation, record.Evidence); err != nil {
		return domain.RelationResult{}, err
	}
	if record.Relation.Status != domain.RelationStatusSuggested || record.Relation.Version != 1 || record.Relation.Confirmation != nil {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationInvalid, errors.New("relation suggestion requires a suggested version-one aggregate"))
	}
	if err := validateDistinctEvidence(record.Evidence); err != nil {
		return domain.RelationResult{}, err
	}
	var result domain.RelationResult
	err := repository.gormCommand(ctx, record.Relation.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandSuggestRelation, aggregateRelation,
		func(callbackCtx context.Context, transaction *gorm.DB, receipt *gormKnowledgeReceipt) error {
			if receipt != nil {
				loaded, err := gormKnowledgeLoadRelationResult(callbackCtx, transaction, record.Relation.WorkspaceID, receipt.AggregateID)
				if err != nil {
					return gormKnowledgeReadError(callbackCtx, err, errorCodeRelationNotFound)
				}
				if err := gormKnowledgeAssertReceipt(receipt, loaded.Relation.ID, loaded.Relation.Version, true); err != nil {
					return err
				}
				loaded.Replayed = true
				result = loaded
				return nil
			}
			if err := gormKnowledgeLockNodeRefs(callbackCtx, transaction, record.Relation.WorkspaceID, []domain.NodeRef{record.Relation.Source, record.Relation.Target}, true); err != nil {
				return err
			}
			inserted, err := gormKnowledgeInsertRelation(callbackCtx, transaction, record.Relation)
			replayed := false
			if gormKnowledgeNoRows(err) {
				loaded, loadErr := gormKnowledgeLoadRelationByFingerprint(callbackCtx, transaction, record.Relation.WorkspaceID, record.Relation.Fingerprint)
				if loadErr != nil {
					return gormKnowledgeReadError(callbackCtx, loadErr, errorCodeRelationNotFound)
				}
				if !equivalentSuggestedRelation(loaded.Relation, record.Relation) || !matchesSuggestedEvidenceSet(loaded.Evidence, record.Evidence) {
					return versionConflict(errors.New("relation fingerprint is already bound to a different payload"))
				}
				inserted, replayed = loaded.Relation, true
			} else if err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			if !replayed {
				if err := gormKnowledgeInsertSuggestedRelationEvidence(callbackCtx, transaction, sortedEvidence(record.Evidence)); err != nil {
					return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
				}
			}
			if err := gormKnowledgeInsertReceipt(callbackCtx, transaction, record.Relation.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandSuggestRelation, aggregateRelation, inserted.ID, inserted.Version, record.Relation.CreatedAt); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			loaded, err := gormKnowledgeLoadRelationResult(callbackCtx, transaction, record.Relation.WorkspaceID, inserted.ID)
			if err != nil {
				return gormKnowledgeReadError(callbackCtx, err, errorCodeRelationNotFound)
			}
			loaded.Replayed = replayed
			result = loaded
			return nil
		})
	if err != nil {
		return domain.RelationResult{}, err
	}
	return result, nil
}

func (repository *GORMRepository) ConfirmRelation(ctx context.Context, record domain.ConfirmRelationRecord) (domain.RelationResult, error) {
	if err := domain.ValidateConfirmation(record.Confirmation); err != nil {
		return domain.RelationResult{}, err
	}
	evidenceRecord := record.Evidence
	if evidenceRecord.Confirmation == nil {
		confirmation := record.Confirmation
		evidenceRecord.Confirmation = &confirmation
	}
	if evidenceRecord.EvidenceHash == "" {
		evidenceRecord.EvidenceHash = domain.ComputeRelationEvidenceHash(evidenceRecord)
	}
	if evidenceRecord.WorkspaceID != record.WorkspaceID || evidenceRecord.RelationID != record.RelationID || evidenceRecord.Confirmation == nil || *evidenceRecord.Confirmation != record.Confirmation {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationEvidenceInvalid, errors.New("relation confirmation evidence binding is invalid"))
	}
	if err := domain.ValidateRelationEvidence(evidenceRecord); err != nil {
		return domain.RelationResult{}, err
	}
	var result domain.RelationResult
	err := repository.gormCommand(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandConfirmRelation, aggregateRelation,
		func(callbackCtx context.Context, transaction *gorm.DB, receipt *gormKnowledgeReceipt) error {
			if receipt != nil {
				loaded, err := gormKnowledgeLoadRelationResult(callbackCtx, transaction, record.WorkspaceID, receipt.AggregateID)
				if err != nil {
					return gormKnowledgeReadError(callbackCtx, err, errorCodeRelationNotFound)
				}
				if err := gormKnowledgeAssertReceipt(receipt, loaded.Relation.ID, loaded.Relation.Version, true); err != nil {
					return err
				}
				loaded.Replayed = true
				result = loaded
				return nil
			}
			current, evidence, err := gormKnowledgeLockRelationAggregate(callbackCtx, transaction, record.WorkspaceID, record.RelationID, true)
			if err != nil {
				return gormKnowledgeReadError(callbackCtx, err, errorCodeRelationNotFound)
			}
			if current.Version != record.ExpectedVersion {
				return versionConflict(errors.New("relation expected version is stale"))
			}
			if err := domain.ValidateRelationTransition(current.Status, domain.RelationStatusConfirmed); err != nil {
				return err
			}
			for _, existing := range evidence {
				if existing.EvidenceHash == evidenceRecord.EvidenceHash {
					return versionConflict(errors.New("relation evidence is already present under another command"))
				}
			}
			if err := gormKnowledgeInsertRelationEvidence(callbackCtx, transaction, evidenceRecord); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			evidence = append(evidence, evidenceRecord)
			evidenceFingerprint := domain.ComputeRelationEvidenceFingerprint(evidence)
			updated, err := gormKnowledgeScanRelation(callbackCtx, transaction, `
				UPDATE core.relation SET status=?,confirmation_method=?,confirmation_ref=?,evidence_fingerprint=?,version=version+1,updated_at=?
				WHERE workspace_id=? AND id=? AND version=?
				RETURNING id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,fingerprint,evidence_fingerprint,version,created_at,updated_at`, string(domain.RelationStatusConfirmed), string(record.Confirmation.Method), record.Confirmation.Reference, evidenceFingerprint, record.At.UTC(), string(record.WorkspaceID), string(record.RelationID), record.ExpectedVersion)
			if gormKnowledgeNoRows(err) {
				return versionConflict(err)
			}
			if err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			if err := domain.ValidateRelationAggregate(updated, evidence); err != nil {
				return consistency(domain.ErrorCodeRelationInvalid, err)
			}
			if err := gormKnowledgeInsertReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandConfirmRelation, aggregateRelation, updated.ID, updated.Version, record.At); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			result = domain.RelationResult{Relation: updated, Evidence: evidence}
			return nil
		})
	if err != nil {
		return domain.RelationResult{}, err
	}
	return result, nil
}

func (repository *GORMRepository) TransitionRelation(ctx context.Context, record domain.TransitionRelationRecord) (domain.RelationResult, error) {
	var transitionEvidence *domain.RelationEvidence
	if record.Status == domain.RelationStatusSuggested {
		if record.Evidence == nil {
			return domain.RelationResult{}, invalidQuery(errors.New("relation resuggestion requires new evidence"))
		}
		value := *record.Evidence
		if value.WorkspaceID != record.WorkspaceID || value.RelationID != record.RelationID || value.Confirmation != nil {
			return domain.RelationResult{}, consistency(domain.ErrorCodeRelationEvidenceInvalid, errors.New("relation resuggestion evidence binding is invalid"))
		}
		if value.EvidenceHash == "" {
			value.EvidenceHash = domain.ComputeRelationEvidenceHash(value)
		}
		if err := domain.ValidateRelationEvidence(value); err != nil {
			return domain.RelationResult{}, err
		}
		transitionEvidence = &value
	} else if record.Evidence != nil {
		return domain.RelationResult{}, invalidQuery(errors.New("only relation resuggestion can append transition evidence"))
	}
	var result domain.RelationResult
	err := repository.gormCommand(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionRelation, aggregateRelation,
		func(callbackCtx context.Context, transaction *gorm.DB, receipt *gormKnowledgeReceipt) error {
			if receipt != nil {
				loaded, err := gormKnowledgeLoadRelationResult(callbackCtx, transaction, record.WorkspaceID, receipt.AggregateID)
				if err != nil {
					return gormKnowledgeReadError(callbackCtx, err, errorCodeRelationNotFound)
				}
				if err := gormKnowledgeAssertReceipt(receipt, loaded.Relation.ID, loaded.Relation.Version, true); err != nil {
					return err
				}
				loaded.Replayed = true
				result = loaded
				return nil
			}
			activeEndpoints := record.Status == domain.RelationStatusSuggested || record.Status == domain.RelationStatusConfirmed
			current, evidence, err := gormKnowledgeLockRelationAggregate(callbackCtx, transaction, record.WorkspaceID, record.RelationID, activeEndpoints)
			if err != nil {
				return gormKnowledgeReadError(callbackCtx, err, errorCodeRelationNotFound)
			}
			if current.Version != record.ExpectedVersion {
				return versionConflict(errors.New("relation expected version is stale"))
			}
			evidenceFingerprint := current.EvidenceFingerprint
			confirmationMethod, confirmationRef := confirmationValues(current.Confirmation)
			if transitionEvidence != nil {
				for _, existing := range evidence {
					if existing.EvidenceHash == transitionEvidence.EvidenceHash {
						return versionConflict(errors.New("relation resuggestion evidence is already present"))
					}
				}
				evidence = append(evidence, *transitionEvidence)
				evidenceFingerprint, confirmationMethod, confirmationRef = domain.ComputeRelationEvidenceFingerprint(evidence), nil, nil
			}
			if err := domain.ValidateRelationTransitionCommand(current, record.Status, evidenceFingerprint); err != nil {
				return err
			}
			if transitionEvidence != nil {
				if err := gormKnowledgeInsertRelationEvidence(callbackCtx, transaction, *transitionEvidence); err != nil {
					return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
				}
			}
			updated, err := gormKnowledgeScanRelation(callbackCtx, transaction, `
				UPDATE core.relation SET status=?,evidence_fingerprint=?,confirmation_method=?,confirmation_ref=?,version=version+1,updated_at=?
				WHERE workspace_id=? AND id=? AND version=?
				RETURNING id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,fingerprint,evidence_fingerprint,version,created_at,updated_at`, string(record.Status), nullableEvidenceFingerprint(evidenceFingerprint), confirmationMethod, confirmationRef, record.At.UTC(), string(record.WorkspaceID), string(record.RelationID), record.ExpectedVersion)
			if gormKnowledgeNoRows(err) {
				return versionConflict(err)
			}
			if err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			if err := domain.ValidateRelationAggregate(updated, evidence); err != nil {
				return consistency(domain.ErrorCodeRelationInvalid, err)
			}
			if err := gormKnowledgeInsertReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionRelation, aggregateRelation, updated.ID, updated.Version, record.At); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			result = domain.RelationResult{Relation: updated, Evidence: evidence}
			return nil
		})
	if err != nil {
		return domain.RelationResult{}, err
	}
	return result, nil
}

func gormKnowledgeScanRelation(ctx context.Context, database *gorm.DB, query string, arguments ...any) (domain.Relation, error) {
	row, err := gormKnowledgeRawRow(ctx, database, query, arguments...)
	if err != nil {
		return domain.Relation{}, err
	}
	return scanRelation(row)
}

func gormKnowledgeLoadRelationResult(ctx context.Context, database *gorm.DB, workspaceID, relationID foundation.ID) (domain.RelationResult, error) {
	relation, err := gormKnowledgeScanRelation(ctx, database, relationSelect+` WHERE workspace_id=? AND id=?`, string(workspaceID), string(relationID))
	if err != nil {
		return domain.RelationResult{}, err
	}
	evidence, err := gormKnowledgeLoadRelationEvidence(ctx, database, workspaceID, []foundation.ID{relationID})
	if err != nil {
		return domain.RelationResult{}, err
	}
	if err := domain.ValidateRelationAggregate(relation, evidence[relationID]); err != nil {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationInvalid, err)
	}
	return domain.RelationResult{Relation: relation, Evidence: evidence[relationID]}, nil
}

func gormKnowledgeLoadRelationByFingerprint(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, fingerprint string) (domain.RelationResult, error) {
	relation, err := gormKnowledgeScanRelation(ctx, database, relationSelect+` WHERE workspace_id=? AND fingerprint=? FOR UPDATE`, string(workspaceID), fingerprint)
	if err != nil {
		return domain.RelationResult{}, err
	}
	evidence, err := gormKnowledgeLoadRelationEvidence(ctx, database, workspaceID, []foundation.ID{relation.ID})
	if err != nil {
		return domain.RelationResult{}, err
	}
	if err := domain.ValidateRelationAggregate(relation, evidence[relation.ID]); err != nil {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationInvalid, err)
	}
	return domain.RelationResult{Relation: relation, Evidence: evidence[relation.ID]}, nil
}

func gormKnowledgeLoadRelationEvidence(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, relationIDs []foundation.ID) (map[foundation.ID][]domain.RelationEvidence, error) {
	result := make(map[foundation.ID][]domain.RelationEvidence, len(relationIDs))
	for _, id := range relationIDs {
		result[id] = []domain.RelationEvidence{}
	}
	if len(relationIDs) == 0 {
		return result, nil
	}
	rows, err := gormKnowledgeRawRows(ctx, database, `
		SELECT evidence.id::text,evidence.workspace_id::text,evidence.relation_id::text,evidence.source_version_id::text,evidence.source_span_id::text,evidence.reason,evidence.applicability,evidence.applicability_schema_version,evidence.applicability_hash,evidence.evidence_hash,evidence.model_run_ref,evidence.confirmation_method,evidence.confirmed_by,evidence.created_at
		FROM core.relation_evidence evidence
		WHERE evidence.workspace_id=? AND evidence.relation_id=ANY(?::uuid[])
		ORDER BY evidence.relation_id,evidence.created_at,evidence.id`, string(workspaceID), pq.Array(idsAsStrings(relationIDs)))
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

func gormKnowledgeLockRelationAggregate(ctx context.Context, database *gorm.DB, workspaceID, relationID foundation.ID, activeEndpoints bool) (domain.Relation, []domain.RelationEvidence, error) {
	identity, err := gormKnowledgeScanRelation(ctx, database, relationSelect+` WHERE workspace_id=? AND id=?`, string(workspaceID), string(relationID))
	if err != nil {
		return domain.Relation{}, nil, err
	}
	if err := gormKnowledgeLockNodeRefs(ctx, database, workspaceID, []domain.NodeRef{identity.Source, identity.Target}, activeEndpoints); err != nil {
		return domain.Relation{}, nil, err
	}
	relation, err := gormKnowledgeScanRelation(ctx, database, relationSelect+` WHERE workspace_id=? AND id=? FOR UPDATE`, string(workspaceID), string(relationID))
	if err != nil {
		return domain.Relation{}, nil, err
	}
	evidence, err := gormKnowledgeLoadRelationEvidence(ctx, database, workspaceID, []foundation.ID{relationID})
	if err != nil {
		return domain.Relation{}, nil, err
	}
	if err := domain.ValidateRelationAggregate(relation, evidence[relationID]); err != nil {
		return domain.Relation{}, nil, consistency(domain.ErrorCodeRelationInvalid, err)
	}
	return relation, evidence[relationID], nil
}

func gormKnowledgeLockNodeRefs(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, refs []domain.NodeRef, active bool) error {
	groups, err := groupNodeRefs(refs)
	if err != nil {
		return err
	}
	for _, group := range groups {
		var query string
		switch group.Type {
		case domain.NodeTypeClaim:
			query = `SELECT id::text,status FROM core.claim WHERE workspace_id=? AND id=ANY(?::uuid[]) ORDER BY id FOR SHARE`
		case domain.NodeTypeTopic:
			query = `SELECT id::text,status FROM core.topic WHERE workspace_id=? AND id=ANY(?::uuid[]) ORDER BY id FOR SHARE`
		default:
			return consistency(domain.ErrorCodeRelationInvalid, errors.New("relation node type is unsupported"))
		}
		rows, err := gormKnowledgeRawRows(ctx, database, query, string(workspaceID), pq.Array(idsAsStrings(group.IDs)))
		if err != nil {
			return classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
		}
		seen := 0
		for rows.Next() {
			var id, lifecycle string
			if err := rows.Scan(&id, &lifecycle); err != nil {
				rows.Close()
				return classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
			}
			if active && !activeNodeLifecycle(group.Type, lifecycle) {
				rows.Close()
				return consistency(domain.ErrorCodeRelationInvalid, errors.New("relation endpoint is inactive"))
			}
			seen++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
		}
		rows.Close()
		if seen != len(group.IDs) {
			return consistency(domain.ErrorCodeRelationInvalid, errors.New("relation endpoint is missing or cross-workspace"))
		}
	}
	return nil
}

func gormKnowledgeInsertRelation(ctx context.Context, database *gorm.DB, relation domain.Relation) (domain.Relation, error) {
	confirmationMethod, confirmationRef := confirmationValues(relation.Confirmation)
	return gormKnowledgeScanRelation(ctx, database, `
		INSERT INTO core.relation (id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,valid_from,valid_to,version,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (workspace_id,fingerprint) DO NOTHING
		RETURNING id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,fingerprint,evidence_fingerprint,version,created_at,updated_at`, string(relation.ID), string(relation.WorkspaceID), string(relation.Source.Type), string(relation.Source.ID), string(relation.Target.Type), string(relation.Target.ID), string(relation.Type), string(relation.Status), relation.ConfidenceScore, relation.Fingerprint, nullableEvidenceFingerprint(relation.EvidenceFingerprint), confirmationMethod, confirmationRef, timePointer(relation.ValidFrom), timePointer(relation.ValidTo), relation.Version, relation.CreatedAt.UTC(), relation.UpdatedAt.UTC())
}

func gormKnowledgeInsertRelationEvidence(ctx context.Context, database *gorm.DB, evidence domain.RelationEvidence) error {
	var confirmationMethod, confirmedBy any
	if evidence.Confirmation != nil {
		confirmationMethod, confirmedBy = string(evidence.Confirmation.Method), evidence.Confirmation.Reference
	}
	_, err := gormKnowledgeExec(ctx, database, `
		INSERT INTO core.relation_evidence (id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,model_run_ref,confirmation_method,confirmed_by,created_at)
		VALUES (?,?,?,?,?,?,?,?::jsonb,?,?,?,?,?,?)`, string(evidence.ID), string(evidence.WorkspaceID), string(evidence.RelationID), string(evidence.Provenance.SourceVersionID), string(evidence.Provenance.SourceSpanID), evidence.Reason, evidence.EvidenceHash, knowledgeJSONB(evidence.Applicability.CanonicalJSON), evidence.Applicability.SchemaVersion, evidence.Applicability.Hash, pointerString(evidence.ModelRunRef), confirmationMethod, confirmedBy, evidence.CreatedAt.UTC())
	return err
}

func gormKnowledgeInsertSuggestedRelationEvidence(ctx context.Context, database *gorm.DB, evidence []domain.RelationEvidence) error {
	if len(evidence) == 0 {
		return nil
	}
	ids, workspaceIDs, relationIDs := make([]string, len(evidence)), make([]string, len(evidence)), make([]string, len(evidence))
	sourceVersionIDs, sourceSpanIDs, reasons, hashes := make([]string, len(evidence)), make([]string, len(evidence)), make([]string, len(evidence)), make([]string, len(evidence))
	applicabilities, versions, applicabilityHashes, modelRunRefs := make([]string, len(evidence)), make([]string, len(evidence)), make([]string, len(evidence)), make([]string, len(evidence))
	createdAt := make([]time.Time, len(evidence))
	for index, item := range evidence {
		ids[index], workspaceIDs[index], relationIDs[index] = string(item.ID), string(item.WorkspaceID), string(item.RelationID)
		sourceVersionIDs[index], sourceSpanIDs[index], reasons[index], hashes[index] = string(item.Provenance.SourceVersionID), string(item.Provenance.SourceSpanID), item.Reason, item.EvidenceHash
		applicabilities[index], versions[index], applicabilityHashes[index] = string(item.Applicability.CanonicalJSON), item.Applicability.SchemaVersion, item.Applicability.Hash
		if item.ModelRunRef != nil {
			modelRunRefs[index] = *item.ModelRunRef
		}
		createdAt[index] = item.CreatedAt.UTC()
	}
	_, err := gormKnowledgeExec(ctx, database, `
		INSERT INTO core.relation_evidence (id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,model_run_ref,confirmation_method,confirmed_by,created_at)
		SELECT input.id,input.workspace_id,input.relation_id,input.source_version_id,input.source_span_id,input.reason,input.evidence_hash,input.applicability::jsonb,input.applicability_schema_version,input.applicability_hash,NULLIF(input.model_run_ref,''),NULL,NULL,input.created_at
		FROM unnest(?::uuid[],?::uuid[],?::uuid[],?::uuid[],?::uuid[],?::text[],?::text[],?::text[],?::text[],?::text[],?::text[],?::timestamptz[]) AS input(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,model_run_ref,created_at)`, pq.Array(ids), pq.Array(workspaceIDs), pq.Array(relationIDs), pq.Array(sourceVersionIDs), pq.Array(sourceSpanIDs), pq.Array(reasons), pq.Array(hashes), pq.Array(applicabilities), pq.Array(versions), pq.Array(applicabilityHashes), pq.Array(modelRunRefs), pq.Array(createdAt))
	return err
}
