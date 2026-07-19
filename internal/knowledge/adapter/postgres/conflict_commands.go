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

// OpenConflict 原子创建 Conflict/Members，并把相关 Confirmed Claim 标记为 DISPUTED。
func (r *Repository) OpenConflict(ctx context.Context, record domain.OpenConflictRecord) (domain.ConflictResult, error) {
	if len(record.Members) > domain.MaxBatchLimit {
		return domain.ConflictResult{}, invalidQuery(errors.New("conflict member count exceeds repository limit"))
	}
	if err := domain.ValidateConflictAggregate(record.Conflict, record.Members); err != nil {
		return domain.ConflictResult{}, err
	}
	if record.Conflict.Status != domain.ConflictStatusOpen || record.Conflict.Version != 1 || record.Conflict.Resolution != nil || record.Conflict.ResolutionReference != nil {
		return domain.ConflictResult{}, consistency(domain.ErrorCodeConflictInvalid, errors.New("conflict open requires an open version-one aggregate"))
	}
	tx, receipt, err := r.beginCommand(ctx, record.Conflict.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandOpenConflict, aggregateConflict)
	if err != nil {
		return domain.ConflictResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if receipt != nil {
		result, loadErr := loadConflictResult(ctx, tx, record.Conflict.WorkspaceID, receipt.AggregateID)
		if loadErr != nil {
			return domain.ConflictResult{}, classifyRead(loadErr, errorCodeConflictNotFound)
		}
		if err := assertReceiptAggregate(receipt, result.Conflict.ID); err != nil {
			return domain.ConflictResult{}, err
		}
		if err := requireReceiptVersion(receipt, result.Conflict.Version); err != nil {
			return domain.ConflictResult{}, err
		}
		result.Replayed = true
		if err := commit(ctx, tx); err != nil {
			return domain.ConflictResult{}, err
		}
		return result, nil
	}
	orderedMembers := append([]domain.ConflictMember(nil), record.Members...)
	sort.Slice(orderedMembers, func(i, j int) bool { return orderedMembers[i].ClaimID < orderedMembers[j].ClaimID })
	if err := lockConflictMembers(ctx, tx, record.Conflict, orderedMembers); err != nil {
		return domain.ConflictResult{}, err
	}
	inserted, err := insertConflict(ctx, tx, record.Conflict, orderedMembers)
	replayed := false
	if errors.Is(err, pgx.ErrNoRows) {
		result, loadErr := loadOpenConflictByFingerprint(ctx, tx, record.Conflict.WorkspaceID, record.Conflict.Fingerprint)
		if loadErr != nil {
			return domain.ConflictResult{}, classifyRead(loadErr, errorCodeConflictNotFound)
		}
		if !equivalentOpenConflict(result, record.Conflict, orderedMembers) {
			return domain.ConflictResult{}, versionConflict(errors.New("open conflict fingerprint is already bound to a different payload"))
		}
		inserted = result.Conflict
		replayed = true
	} else if err != nil {
		return domain.ConflictResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if !replayed {
		if err := insertConflictMembers(ctx, tx, orderedMembers); err != nil {
			return domain.ConflictResult{}, classify(err, errorCodeDatabaseUnavailable)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE core.claim
			SET status='DISPUTED',version=version+1,updated_at=GREATEST(updated_at,$1)
			WHERE workspace_id=$2 AND id=ANY($3::uuid[]) AND status='CONFIRMED'`,
			record.Conflict.CreatedAt.UTC(), string(record.Conflict.WorkspaceID), memberClaimIDs(orderedMembers),
		); err != nil {
			return domain.ConflictResult{}, classify(err, errorCodeDatabaseUnavailable)
		}
	}
	if err := insertReceipt(ctx, tx, record.Conflict.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandOpenConflict, aggregateConflict, inserted.ID, inserted.Version, record.Conflict.CreatedAt); err != nil {
		return domain.ConflictResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	result, err := loadConflictResult(ctx, tx, record.Conflict.WorkspaceID, inserted.ID)
	if err != nil {
		return domain.ConflictResult{}, classifyRead(err, errorCodeConflictNotFound)
	}
	result.Replayed = replayed
	if err := commit(ctx, tx); err != nil {
		return domain.ConflictResult{}, err
	}
	return result, nil
}

func lockConflictMembers(ctx context.Context, tx pgx.Tx, conflict domain.Conflict, members []domain.ConflictMember) error {
	if conflict.TopicID != nil {
		if err := lockNodeRefs(ctx, tx, conflict.WorkspaceID, []domain.NodeRef{{Type: domain.NodeTypeTopic, ID: *conflict.TopicID}}, true); err != nil {
			return err
		}
	}
	expectedHashes := make(map[foundation.ID]string, len(members))
	for _, member := range members {
		expectedHashes[member.ClaimID] = member.ApplicabilityHash
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text,status,applicability_hash
		FROM core.claim
		WHERE workspace_id=$1 AND id=ANY($2::uuid[])
		ORDER BY id
		FOR UPDATE`, string(conflict.WorkspaceID), memberClaimIDs(members))
	if err != nil {
		return classify(err, errorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id, status, applicabilityHash string
		if err := rows.Scan(&id, &status, &applicabilityHash); err != nil {
			return classify(err, errorCodeDatabaseUnavailable)
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
		return classify(err, errorCodeDatabaseUnavailable)
	}
	if seen != len(members) {
		return notFound(errorCodeClaimNotFound, errors.New("one or more conflict member claims are missing or cross-workspace"))
	}
	return nil
}

func insertConflictMembers(ctx context.Context, tx pgx.Tx, members []domain.ConflictMember) error {
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
	_, err := tx.Exec(ctx, `
		INSERT INTO core.conflict_member (
			conflict_id,claim_id,workspace_id,applicability,applicability_schema_version,
			applicability_hash,position_summary,created_at
		)
		SELECT input.conflict_id,input.claim_id,input.workspace_id,input.applicability::jsonb,
			input.applicability_schema_version,input.applicability_hash,input.position_summary,input.created_at
		FROM unnest(
			$1::uuid[],$2::uuid[],$3::uuid[],$4::text[],$5::text[],$6::text[],$7::text[],$8::timestamptz[]
		) AS input(
			conflict_id,claim_id,workspace_id,applicability,applicability_schema_version,
			applicability_hash,position_summary,created_at
		)`, conflictIDs, claimIDs, workspaceIDs, applicabilities, applicabilityVersions,
		applicabilityHashes, positionSummaries, createdAt)
	return err
}

// TransitionConflict 以 CAS 执行 Conflict 状态迁移并保留 resolution reference。
func (r *Repository) TransitionConflict(ctx context.Context, record domain.TransitionConflictRecord) (domain.ConflictResult, error) {
	tx, receipt, err := r.beginCommand(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionConflict, aggregateConflict)
	if err != nil {
		return domain.ConflictResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if receipt != nil {
		result, loadErr := loadConflictResult(ctx, tx, record.WorkspaceID, receipt.AggregateID)
		if loadErr != nil {
			return domain.ConflictResult{}, classifyRead(loadErr, errorCodeConflictNotFound)
		}
		if err := assertReceiptAggregate(receipt, result.Conflict.ID); err != nil {
			return domain.ConflictResult{}, err
		}
		if err := requireReceiptVersion(receipt, result.Conflict.Version); err != nil {
			return domain.ConflictResult{}, err
		}
		result.Replayed = true
		if err := commit(ctx, tx); err != nil {
			return domain.ConflictResult{}, err
		}
		return result, nil
	}
	identity, err := loadConflictResult(ctx, tx, record.WorkspaceID, record.ConflictID)
	if err != nil {
		return domain.ConflictResult{}, classifyRead(err, errorCodeConflictNotFound)
	}
	refs := make([]domain.NodeRef, 0, len(identity.Members)+1)
	if identity.Conflict.TopicID != nil {
		refs = append(refs, domain.NodeRef{Type: domain.NodeTypeTopic, ID: *identity.Conflict.TopicID})
	}
	for _, member := range identity.Members {
		refs = append(refs, domain.NodeRef{Type: domain.NodeTypeClaim, ID: member.ClaimID})
	}
	if err := lockNodeRefs(ctx, tx, record.WorkspaceID, refs, false); err != nil {
		return domain.ConflictResult{}, err
	}
	stored, err := scanConflict(tx.QueryRow(ctx, conflictSelect+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(record.WorkspaceID), string(record.ConflictID)))
	if err != nil {
		return domain.ConflictResult{}, classifyRead(err, errorCodeConflictNotFound)
	}
	if stored.Conflict.Version != record.ExpectedVersion {
		return domain.ConflictResult{}, versionConflict(errors.New("conflict expected version is stale"))
	}
	if err := domain.ValidateConflictTransition(stored.Conflict.Status, record.Status); err != nil {
		return domain.ConflictResult{}, err
	}
	candidate := stored.Conflict
	candidate.Status = record.Status
	candidate.Resolution = record.Resolution
	candidate.ResolutionReference = record.ResolutionReference
	candidate.Version++
	candidate.UpdatedAt = record.At.UTC()
	if err := domain.ValidateConflictAggregate(candidate, identity.Members); err != nil {
		return domain.ConflictResult{}, err
	}
	resolvedAt := any(nil)
	if record.Status == domain.ConflictStatusResolved || record.Status == domain.ConflictStatusAcceptedDivergence {
		resolvedAt = record.At.UTC()
	}
	updated, err := scanConflict(tx.QueryRow(ctx, `
		UPDATE core.conflict
		SET status=$1,resolution=$2,resolution_reference=$3,resolved_at=$4,
			version=version+1,updated_at=$5
		WHERE workspace_id=$6 AND id=$7 AND version=$8
		RETURNING id::text,workspace_id::text,topic_id::text,status,severity,summary,applicability_assessment,
			applicability_hash,overlap_reason,fingerprint,resolution,resolution_reference,version,
			created_at,updated_at,resolved_at`,
		string(record.Status), pointerString(record.Resolution), pointerString(record.ResolutionReference), resolvedAt,
		record.At.UTC(), string(record.WorkspaceID), string(record.ConflictID), record.ExpectedVersion,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ConflictResult{}, versionConflict(err)
	}
	if err != nil {
		return domain.ConflictResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if err := validateStoredConflict(updated, identity.Members); err != nil {
		return domain.ConflictResult{}, err
	}
	if err := insertReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionConflict, aggregateConflict, updated.Conflict.ID, updated.Conflict.Version, record.At); err != nil {
		return domain.ConflictResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.ConflictResult{}, err
	}
	return domain.ConflictResult{Conflict: updated.Conflict, Members: identity.Members}, nil
}

func insertConflict(ctx context.Context, tx pgx.Tx, conflict domain.Conflict, members []domain.ConflictMember) (domain.Conflict, error) {
	var exactHash any
	var overlapReason any
	if conflict.ApplicabilityAssessment == domain.ApplicabilityAssessmentExact {
		exactHash = members[0].ApplicabilityHash
	} else {
		overlapReason = conflict.ReviewedOverlapReason
	}
	stored, err := scanConflict(tx.QueryRow(ctx, `
		INSERT INTO core.conflict (
			id,workspace_id,topic_id,status,severity,summary,applicability_assessment,
			applicability_hash,overlap_reason,fingerprint,resolution,resolution_reference,
			version,created_at,updated_at,resolved_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,NULL,$11,$12,$13,NULL)
		ON CONFLICT (workspace_id,fingerprint)
		WHERE status NOT IN ('RESOLVED','ACCEPTED_DIVERGENCE') DO NOTHING
		RETURNING id::text,workspace_id::text,topic_id::text,status,severity,summary,applicability_assessment,
			applicability_hash,overlap_reason,fingerprint,resolution,resolution_reference,version,
			created_at,updated_at,resolved_at`,
		string(conflict.ID), string(conflict.WorkspaceID), optionalIDValue(conflict.TopicID), string(conflict.Status),
		string(conflict.Severity), conflict.Summary, string(conflict.ApplicabilityAssessment), exactHash, overlapReason,
		conflict.Fingerprint, conflict.Version, conflict.CreatedAt.UTC(), conflict.UpdatedAt.UTC(),
	))
	return stored.Conflict, err
}

func loadOpenConflictByFingerprint(ctx context.Context, queryer interface {
	queryRower
	rowQueryer
}, workspaceID foundation.ID, fingerprint string) (domain.ConflictResult, error) {
	stored, err := scanConflict(queryer.QueryRow(ctx, conflictSelect+`
		WHERE workspace_id=$1 AND fingerprint=$2 AND status NOT IN ('RESOLVED','ACCEPTED_DIVERGENCE') FOR UPDATE`,
		string(workspaceID), fingerprint,
	))
	if err != nil {
		return domain.ConflictResult{}, err
	}
	membersByConflict, err := loadConflictMembers(ctx, queryer, workspaceID, []foundation.ID{stored.Conflict.ID})
	if err != nil {
		return domain.ConflictResult{}, err
	}
	if err := validateStoredConflict(stored, membersByConflict[stored.Conflict.ID]); err != nil {
		return domain.ConflictResult{}, err
	}
	return domain.ConflictResult{Conflict: stored.Conflict, Members: membersByConflict[stored.Conflict.ID]}, nil
}

func equivalentOpenConflict(existing domain.ConflictResult, requested domain.Conflict, members []domain.ConflictMember) bool {
	if existing.Conflict.WorkspaceID != requested.WorkspaceID || !pointerEqual(existing.Conflict.TopicID, requested.TopicID) ||
		existing.Conflict.Severity != requested.Severity || existing.Conflict.Summary != requested.Summary ||
		existing.Conflict.ApplicabilityAssessment != requested.ApplicabilityAssessment ||
		existing.Conflict.ReviewedOverlapReason != requested.ReviewedOverlapReason || existing.Conflict.Fingerprint != requested.Fingerprint ||
		len(existing.Members) != len(members) {
		return false
	}
	want := make(map[foundation.ID]string, len(members))
	for _, member := range members {
		want[member.ClaimID] = member.ApplicabilityHash + "\x00" + member.PositionSummary
	}
	for _, member := range existing.Members {
		if want[member.ClaimID] != member.ApplicabilityHash+"\x00"+member.PositionSummary {
			return false
		}
	}
	return true
}

func memberClaimIDs(members []domain.ConflictMember) []string {
	result := make([]string, len(members))
	for index := range members {
		result[index] = string(members[index].ClaimID)
	}
	return result
}

func optionalIDValue(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}
