package postgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

// SuggestRelation 幂等创建规范化 Suggested Relation 及其可选 Evidence。
func (r *Repository) SuggestRelation(ctx context.Context, record domain.SuggestRelationRecord) (domain.RelationResult, error) {
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
	tx, receipt, err := r.beginCommand(ctx, record.Relation.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandSuggestRelation, aggregateRelation)
	if err != nil {
		return domain.RelationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if receipt != nil {
		result, loadErr := loadRelationResult(ctx, tx, record.Relation.WorkspaceID, receipt.AggregateID)
		if loadErr != nil {
			return domain.RelationResult{}, classifyRead(loadErr, errorCodeRelationNotFound)
		}
		if err := assertReceiptAggregate(receipt, result.Relation.ID); err != nil {
			return domain.RelationResult{}, err
		}
		if err := requireReceiptVersion(receipt, result.Relation.Version); err != nil {
			return domain.RelationResult{}, err
		}
		result.Replayed = true
		if err := commit(ctx, tx); err != nil {
			return domain.RelationResult{}, err
		}
		return result, nil
	}
	if err := lockNodeRefs(ctx, tx, record.Relation.WorkspaceID, []domain.NodeRef{record.Relation.Source, record.Relation.Target}, true); err != nil {
		return domain.RelationResult{}, err
	}
	inserted, err := insertRelation(ctx, tx, record.Relation)
	replayed := false
	if errors.Is(err, pgx.ErrNoRows) {
		result, loadErr := loadRelationByFingerprint(ctx, tx, record.Relation.WorkspaceID, record.Relation.Fingerprint)
		if loadErr != nil {
			return domain.RelationResult{}, classifyRead(loadErr, errorCodeRelationNotFound)
		}
		if !equivalentSuggestedRelation(result.Relation, record.Relation) || !matchesSuggestedEvidenceSet(result.Evidence, record.Evidence) {
			return domain.RelationResult{}, versionConflict(errors.New("relation fingerprint is already bound to a different payload"))
		}
		inserted = result.Relation
		replayed = true
	} else if err != nil {
		return domain.RelationResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if !replayed {
		if err := insertSuggestedRelationEvidence(ctx, tx, sortedEvidence(record.Evidence)); err != nil {
			return domain.RelationResult{}, classify(err, errorCodeDatabaseUnavailable)
		}
	}
	if err := insertReceipt(ctx, tx, record.Relation.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandSuggestRelation, aggregateRelation, inserted.ID, inserted.Version, record.Relation.CreatedAt); err != nil {
		return domain.RelationResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	result, err := loadRelationResult(ctx, tx, record.Relation.WorkspaceID, inserted.ID)
	if err != nil {
		return domain.RelationResult{}, classifyRead(err, errorCodeRelationNotFound)
	}
	result.Replayed = replayed
	if err := commit(ctx, tx); err != nil {
		return domain.RelationResult{}, err
	}
	return result, nil
}

// ConfirmRelation 原子追加 Evidence/Confirmation 并以 expected_version 确认 Relation。
func (r *Repository) ConfirmRelation(ctx context.Context, record domain.ConfirmRelationRecord) (domain.RelationResult, error) {
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
	if evidenceRecord.WorkspaceID != record.WorkspaceID || evidenceRecord.RelationID != record.RelationID ||
		evidenceRecord.Confirmation == nil || *evidenceRecord.Confirmation != record.Confirmation {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationEvidenceInvalid, errors.New("relation confirmation evidence binding is invalid"))
	}
	if err := domain.ValidateRelationEvidence(evidenceRecord); err != nil {
		return domain.RelationResult{}, err
	}
	tx, receipt, err := r.beginCommand(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandConfirmRelation, aggregateRelation)
	if err != nil {
		return domain.RelationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if receipt != nil {
		result, loadErr := loadRelationResult(ctx, tx, record.WorkspaceID, receipt.AggregateID)
		if loadErr != nil {
			return domain.RelationResult{}, classifyRead(loadErr, errorCodeRelationNotFound)
		}
		if err := assertReceiptAggregate(receipt, result.Relation.ID); err != nil {
			return domain.RelationResult{}, err
		}
		if err := requireReceiptVersion(receipt, result.Relation.Version); err != nil {
			return domain.RelationResult{}, err
		}
		result.Replayed = true
		if err := commit(ctx, tx); err != nil {
			return domain.RelationResult{}, err
		}
		return result, nil
	}
	current, evidence, err := lockRelationAggregate(ctx, tx, record.WorkspaceID, record.RelationID, true)
	if err != nil {
		return domain.RelationResult{}, classifyRead(err, errorCodeRelationNotFound)
	}
	if current.Version != record.ExpectedVersion {
		return domain.RelationResult{}, versionConflict(errors.New("relation expected version is stale"))
	}
	if err := domain.ValidateRelationTransition(current.Status, domain.RelationStatusConfirmed); err != nil {
		return domain.RelationResult{}, err
	}
	for _, existing := range evidence {
		if existing.EvidenceHash == evidenceRecord.EvidenceHash {
			return domain.RelationResult{}, versionConflict(errors.New("relation evidence is already present under another command"))
		}
	}
	if err := insertRelationEvidence(ctx, tx, evidenceRecord); err != nil {
		return domain.RelationResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	evidence = append(evidence, evidenceRecord)
	evidenceFingerprint := domain.ComputeRelationEvidenceFingerprint(evidence)
	updated, err := scanRelation(tx.QueryRow(ctx, `
		UPDATE core.relation
		SET status=$1,confirmation_method=$2,confirmation_ref=$3,evidence_fingerprint=$4,
			version=version+1,updated_at=$5
		WHERE workspace_id=$6 AND id=$7 AND version=$8
		RETURNING id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,
			relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,
			fingerprint,evidence_fingerprint,version,created_at,updated_at`,
		string(domain.RelationStatusConfirmed), string(record.Confirmation.Method), record.Confirmation.Reference,
		evidenceFingerprint, record.At.UTC(), string(record.WorkspaceID), string(record.RelationID), record.ExpectedVersion,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RelationResult{}, versionConflict(err)
	}
	if err != nil {
		return domain.RelationResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if err := domain.ValidateRelationAggregate(updated, evidence); err != nil {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationInvalid, err)
	}
	if err := insertReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandConfirmRelation, aggregateRelation, updated.ID, updated.Version, record.At); err != nil {
		return domain.RelationResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.RelationResult{}, err
	}
	return domain.RelationResult{Relation: updated, Evidence: evidence}, nil
}

// TransitionRelation 以 CAS 执行 Relation 合法状态迁移并保留历史确认。
func (r *Repository) TransitionRelation(ctx context.Context, record domain.TransitionRelationRecord) (domain.RelationResult, error) {
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
	tx, receipt, err := r.beginCommand(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionRelation, aggregateRelation)
	if err != nil {
		return domain.RelationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if receipt != nil {
		result, loadErr := loadRelationResult(ctx, tx, record.WorkspaceID, receipt.AggregateID)
		if loadErr != nil {
			return domain.RelationResult{}, classifyRead(loadErr, errorCodeRelationNotFound)
		}
		if err := assertReceiptAggregate(receipt, result.Relation.ID); err != nil {
			return domain.RelationResult{}, err
		}
		if err := requireReceiptVersion(receipt, result.Relation.Version); err != nil {
			return domain.RelationResult{}, err
		}
		result.Replayed = true
		if err := commit(ctx, tx); err != nil {
			return domain.RelationResult{}, err
		}
		return result, nil
	}
	activeEndpoints := record.Status == domain.RelationStatusSuggested || record.Status == domain.RelationStatusConfirmed
	current, evidence, err := lockRelationAggregate(ctx, tx, record.WorkspaceID, record.RelationID, activeEndpoints)
	if err != nil {
		return domain.RelationResult{}, classifyRead(err, errorCodeRelationNotFound)
	}
	if current.Version != record.ExpectedVersion {
		return domain.RelationResult{}, versionConflict(errors.New("relation expected version is stale"))
	}
	evidenceFingerprint := current.EvidenceFingerprint
	confirmationMethod, confirmationRef := confirmationValues(current.Confirmation)
	if transitionEvidence != nil {
		for _, existing := range evidence {
			if existing.EvidenceHash == transitionEvidence.EvidenceHash {
				return domain.RelationResult{}, versionConflict(errors.New("relation resuggestion evidence is already present"))
			}
		}
		evidence = append(evidence, *transitionEvidence)
		evidenceFingerprint = domain.ComputeRelationEvidenceFingerprint(evidence)
		confirmationMethod, confirmationRef = nil, nil
	}
	if err := domain.ValidateRelationTransitionCommand(current, record.Status, evidenceFingerprint); err != nil {
		return domain.RelationResult{}, err
	}
	if transitionEvidence != nil {
		if err := insertRelationEvidence(ctx, tx, *transitionEvidence); err != nil {
			return domain.RelationResult{}, classify(err, errorCodeDatabaseUnavailable)
		}
	}
	updated, err := scanRelation(tx.QueryRow(ctx, `
		UPDATE core.relation
		SET status=$1,evidence_fingerprint=$2,confirmation_method=$3,confirmation_ref=$4,
			version=version+1,updated_at=$5
		WHERE workspace_id=$6 AND id=$7 AND version=$8
		RETURNING id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,
			relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,
			fingerprint,evidence_fingerprint,version,created_at,updated_at`,
		string(record.Status), nullableEvidenceFingerprint(evidenceFingerprint), confirmationMethod, confirmationRef,
		record.At.UTC(), string(record.WorkspaceID), string(record.RelationID), record.ExpectedVersion,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RelationResult{}, versionConflict(err)
	}
	if err != nil {
		return domain.RelationResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if err := domain.ValidateRelationAggregate(updated, evidence); err != nil {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationInvalid, err)
	}
	if err := insertReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionRelation, aggregateRelation, updated.ID, updated.Version, record.At); err != nil {
		return domain.RelationResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.RelationResult{}, err
	}
	return domain.RelationResult{Relation: updated, Evidence: evidence}, nil
}

func lockRelationAggregate(ctx context.Context, tx pgx.Tx, workspaceID, relationID foundation.ID, activeEndpoints bool) (domain.Relation, []domain.RelationEvidence, error) {
	identity, err := scanRelation(tx.QueryRow(ctx, relationSelect+` WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(relationID)))
	if err != nil {
		return domain.Relation{}, nil, err
	}
	if err := lockNodeRefs(ctx, tx, workspaceID, []domain.NodeRef{identity.Source, identity.Target}, activeEndpoints); err != nil {
		return domain.Relation{}, nil, err
	}
	relation, err := scanRelation(tx.QueryRow(ctx, relationSelect+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(workspaceID), string(relationID)))
	if err != nil {
		return domain.Relation{}, nil, err
	}
	evidenceByRelation, err := loadRelationEvidence(ctx, tx, workspaceID, []foundation.ID{relationID})
	if err != nil {
		return domain.Relation{}, nil, err
	}
	evidence := evidenceByRelation[relationID]
	if err := domain.ValidateRelationAggregate(relation, evidence); err != nil {
		return domain.Relation{}, nil, consistency(domain.ErrorCodeRelationInvalid, err)
	}
	return relation, evidence, nil
}

func lockNodeRefs(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, refs []domain.NodeRef, active bool) error {
	groups, err := groupNodeRefs(refs)
	if err != nil {
		return err
	}
	for _, group := range groups {
		var rows pgx.Rows
		switch group.Type {
		case domain.NodeTypeClaim:
			rows, err = tx.Query(ctx, `
				SELECT id::text,status
				FROM core.claim
				WHERE workspace_id=$1 AND id=ANY($2::uuid[])
				ORDER BY id
				FOR SHARE`, string(workspaceID), idsAsStrings(group.IDs))
		case domain.NodeTypeTopic:
			rows, err = tx.Query(ctx, `
				SELECT id::text,status
				FROM core.topic
				WHERE workspace_id=$1 AND id=ANY($2::uuid[])
				ORDER BY id
				FOR SHARE`, string(workspaceID), idsAsStrings(group.IDs))
		default:
			return consistency(domain.ErrorCodeRelationInvalid, errors.New("relation node type is unsupported"))
		}
		if err != nil {
			return classify(err, errorCodeDatabaseUnavailable)
		}
		seen := 0
		for rows.Next() {
			var id, lifecycle string
			if err := rows.Scan(&id, &lifecycle); err != nil {
				rows.Close()
				return classify(err, errorCodeDatabaseUnavailable)
			}
			if active && !activeNodeLifecycle(group.Type, lifecycle) {
				rows.Close()
				return consistency(domain.ErrorCodeRelationInvalid, errors.New("relation endpoint is inactive"))
			}
			seen++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return classify(err, errorCodeDatabaseUnavailable)
		}
		rows.Close()
		if seen != len(group.IDs) {
			return consistency(domain.ErrorCodeRelationInvalid, errors.New("relation endpoint is missing or cross-workspace"))
		}
	}
	return nil
}

type nodeRefGroup struct {
	Type domain.NodeType
	IDs  []foundation.ID
}

func groupNodeRefs(refs []domain.NodeRef) ([]nodeRefGroup, error) {
	ordered := append([]domain.NodeRef(nil), refs...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Type != ordered[j].Type {
			return ordered[i].Type < ordered[j].Type
		}
		return ordered[i].ID < ordered[j].ID
	})
	groups := make([]nodeRefGroup, 0, 2)
	var previous domain.NodeRef
	hasPrevious := false
	for _, ref := range ordered {
		if ref.Type != domain.NodeTypeClaim && ref.Type != domain.NodeTypeTopic {
			return nil, consistency(domain.ErrorCodeRelationInvalid, errors.New("relation node type is unsupported"))
		}
		if hasPrevious && previous == ref {
			continue
		}
		if len(groups) == 0 || groups[len(groups)-1].Type != ref.Type {
			groups = append(groups, nodeRefGroup{Type: ref.Type})
		}
		last := &groups[len(groups)-1]
		last.IDs = append(last.IDs, ref.ID)
		previous = ref
		hasPrevious = true
	}
	return groups, nil
}

func activeNodeLifecycle(nodeType domain.NodeType, lifecycle string) bool {
	if nodeType == domain.NodeTypeTopic {
		return lifecycle == string(domain.TopicStatusActive)
	}
	return lifecycle == string(domain.ClaimStatusSuggested) || lifecycle == string(domain.ClaimStatusConfirmed) || lifecycle == string(domain.ClaimStatusDisputed)
}

func insertRelation(ctx context.Context, tx pgx.Tx, relation domain.Relation) (domain.Relation, error) {
	confirmationMethod, confirmationRef := confirmationValues(relation.Confirmation)
	return scanRelation(tx.QueryRow(ctx, `
		INSERT INTO core.relation (
			id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,
			relation_type,status,confidence_score,fingerprint,evidence_fingerprint,
			confirmation_method,confirmation_ref,valid_from,valid_to,version,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (workspace_id,fingerprint) DO NOTHING
		RETURNING id::text,workspace_id::text,source_node_type,source_node_id::text,target_node_type,target_node_id::text,
			relation_type,status,confirmation_method,confirmation_ref,confidence_score,valid_from,valid_to,
			fingerprint,evidence_fingerprint,version,created_at,updated_at`,
		string(relation.ID), string(relation.WorkspaceID), string(relation.Source.Type), string(relation.Source.ID),
		string(relation.Target.Type), string(relation.Target.ID), string(relation.Type), string(relation.Status),
		relation.ConfidenceScore, relation.Fingerprint, nullableEvidenceFingerprint(relation.EvidenceFingerprint),
		confirmationMethod, confirmationRef, timePointer(relation.ValidFrom), timePointer(relation.ValidTo),
		relation.Version, relation.CreatedAt.UTC(), relation.UpdatedAt.UTC(),
	))
}

func insertRelationEvidence(ctx context.Context, tx pgx.Tx, evidence domain.RelationEvidence) error {
	var confirmationMethod, confirmedBy any
	if evidence.Confirmation != nil {
		confirmationMethod = string(evidence.Confirmation.Method)
		confirmedBy = evidence.Confirmation.Reference
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO core.relation_evidence (
			id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,
			applicability,applicability_schema_version,applicability_hash,model_run_ref,
			confirmation_method,confirmed_by,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,$13,$14)`,
		string(evidence.ID), string(evidence.WorkspaceID), string(evidence.RelationID),
		string(evidence.Provenance.SourceVersionID), string(evidence.Provenance.SourceSpanID), evidence.Reason,
		evidence.EvidenceHash, []byte(evidence.Applicability.CanonicalJSON), evidence.Applicability.SchemaVersion,
		evidence.Applicability.Hash, pointerString(evidence.ModelRunRef), confirmationMethod, confirmedBy, evidence.CreatedAt.UTC(),
	)
	return err
}

func insertSuggestedRelationEvidence(ctx context.Context, tx pgx.Tx, evidence []domain.RelationEvidence) error {
	if len(evidence) == 0 {
		return nil
	}
	ids := make([]string, len(evidence))
	workspaceIDs := make([]string, len(evidence))
	relationIDs := make([]string, len(evidence))
	sourceVersionIDs := make([]string, len(evidence))
	sourceSpanIDs := make([]string, len(evidence))
	reasons := make([]string, len(evidence))
	evidenceHashes := make([]string, len(evidence))
	applicabilities := make([]string, len(evidence))
	applicabilityVersions := make([]string, len(evidence))
	applicabilityHashes := make([]string, len(evidence))
	modelRunRefs := make([]string, len(evidence))
	createdAt := make([]time.Time, len(evidence))
	for index, item := range evidence {
		ids[index] = string(item.ID)
		workspaceIDs[index] = string(item.WorkspaceID)
		relationIDs[index] = string(item.RelationID)
		sourceVersionIDs[index] = string(item.Provenance.SourceVersionID)
		sourceSpanIDs[index] = string(item.Provenance.SourceSpanID)
		reasons[index] = item.Reason
		evidenceHashes[index] = item.EvidenceHash
		applicabilities[index] = string(item.Applicability.CanonicalJSON)
		applicabilityVersions[index] = item.Applicability.SchemaVersion
		applicabilityHashes[index] = item.Applicability.Hash
		if item.ModelRunRef != nil {
			modelRunRefs[index] = *item.ModelRunRef
		}
		createdAt[index] = item.CreatedAt.UTC()
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO core.relation_evidence (
			id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,
			applicability,applicability_schema_version,applicability_hash,model_run_ref,
			confirmation_method,confirmed_by,created_at
		)
		SELECT input.id,input.workspace_id,input.relation_id,input.source_version_id,input.source_span_id,
			input.reason,input.evidence_hash,input.applicability::jsonb,input.applicability_schema_version,
			input.applicability_hash,NULLIF(input.model_run_ref,''),NULL,NULL,input.created_at
		FROM unnest(
			$1::uuid[],$2::uuid[],$3::uuid[],$4::uuid[],$5::uuid[],$6::text[],$7::text[],
			$8::text[],$9::text[],$10::text[],$11::text[],$12::timestamptz[]
		) AS input(
			id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,
			applicability,applicability_schema_version,applicability_hash,model_run_ref,created_at
		)`, ids, workspaceIDs, relationIDs, sourceVersionIDs, sourceSpanIDs, reasons, evidenceHashes,
		applicabilities, applicabilityVersions, applicabilityHashes, modelRunRefs, createdAt)
	return err
}

func loadRelationByFingerprint(ctx context.Context, queryer interface {
	queryRower
	rowQueryer
}, workspaceID foundation.ID, fingerprint string) (domain.RelationResult, error) {
	relation, err := scanRelation(queryer.QueryRow(ctx, relationSelect+` WHERE workspace_id=$1 AND fingerprint=$2 FOR UPDATE`, string(workspaceID), fingerprint))
	if err != nil {
		return domain.RelationResult{}, err
	}
	evidenceByRelation, err := loadRelationEvidence(ctx, queryer, workspaceID, []foundation.ID{relation.ID})
	if err != nil {
		return domain.RelationResult{}, err
	}
	if err := domain.ValidateRelationAggregate(relation, evidenceByRelation[relation.ID]); err != nil {
		return domain.RelationResult{}, consistency(domain.ErrorCodeRelationInvalid, err)
	}
	return domain.RelationResult{Relation: relation, Evidence: evidenceByRelation[relation.ID]}, nil
}

func equivalentSuggestedRelation(left, right domain.Relation) bool {
	return left.WorkspaceID == right.WorkspaceID && left.Source == right.Source && left.Target == right.Target &&
		left.Type == right.Type &&
		equalFloatPointers(left.ConfidenceScore, right.ConfidenceScore) && pointerTimeEqual(left.ValidFrom, right.ValidFrom) &&
		pointerTimeEqual(left.ValidTo, right.ValidTo) && left.Fingerprint == right.Fingerprint
}

func validateDistinctEvidence(evidence []domain.RelationEvidence) error {
	seen := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		if _, duplicate := seen[item.EvidenceHash]; duplicate {
			return consistency(domain.ErrorCodeRelationEvidenceInvalid, errors.New("relation evidence hashes must be distinct"))
		}
		seen[item.EvidenceHash] = struct{}{}
	}
	return nil
}

func matchesSuggestedEvidenceSet(existing, requested []domain.RelationEvidence) bool {
	unconfirmed := make([]domain.RelationEvidence, 0, len(existing))
	for _, item := range existing {
		if item.Confirmation == nil {
			unconfirmed = append(unconfirmed, item)
		}
	}
	if len(requested) != len(unconfirmed) {
		return false
	}
	hashes := make(map[string]int, len(unconfirmed))
	for _, item := range unconfirmed {
		hashes[domain.ComputeRelationEvidenceSemanticHash(item)]++
	}
	for _, item := range requested {
		hash := domain.ComputeRelationEvidenceSemanticHash(item)
		if hashes[hash] == 0 {
			return false
		}
		hashes[hash]--
	}
	return true
}

func sortedEvidence(evidence []domain.RelationEvidence) []domain.RelationEvidence {
	result := append([]domain.RelationEvidence(nil), evidence...)
	sort.Slice(result, func(i, j int) bool { return result[i].EvidenceHash < result[j].EvidenceHash })
	return result
}

func confirmationValues(value *domain.Confirmation) (any, any) {
	if value == nil {
		return nil, nil
	}
	return string(value.Method), value.Reference
}

func nullableEvidenceFingerprint(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func pointerTimeEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
