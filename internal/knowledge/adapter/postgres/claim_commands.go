package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

// ConfirmClaim 原子追加 SUPPORTS Source 并以 expected_version 确认 Claim。
func (r *Repository) ConfirmClaim(ctx context.Context, record domain.ConfirmClaimRecord) (domain.ClaimResult, error) {
	tx, receipt, err := r.beginCommand(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandConfirmClaim, aggregateClaim)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if receipt != nil {
		result, loadErr := loadClaimResult(ctx, tx, record.WorkspaceID, receipt.AggregateID)
		if loadErr != nil {
			return domain.ClaimResult{}, classifyRead(loadErr, errorCodeClaimNotFound)
		}
		if err := assertReceiptAggregate(receipt, result.Claim.ID); err != nil {
			return domain.ClaimResult{}, err
		}
		if err := requireReceiptVersion(receipt, result.Claim.Version); err != nil {
			return domain.ClaimResult{}, err
		}
		result.Replayed = true
		if err := commit(ctx, tx); err != nil {
			return domain.ClaimResult{}, err
		}
		return result, nil
	}
	current, sources, err := lockClaimAggregate(ctx, tx, record.WorkspaceID, record.ClaimID)
	if err != nil {
		return domain.ClaimResult{}, classifyRead(err, errorCodeClaimNotFound)
	}
	if current.Version != record.ExpectedVersion {
		return domain.ClaimResult{}, versionConflict(errors.New("claim expected version is stale"))
	}
	if err := domain.ValidateClaimTransition(current.Status, domain.ClaimStatusConfirmed); err != nil {
		return domain.ClaimResult{}, err
	}
	source := record.Source
	if source.WorkspaceID != record.WorkspaceID || source.ClaimID != record.ClaimID || source.SupportType != domain.ClaimSupportSupports {
		return domain.ClaimResult{}, consistency(domain.ErrorCodeClaimSourceInvalid, errors.New("claim confirmation source binding is invalid"))
	}
	if source.EvidenceHash == "" {
		source.EvidenceHash = domain.ComputeClaimSourceEvidenceHash(source, current.Applicability)
	}
	if err := domain.ValidateClaimSource(source, current.Applicability); err != nil {
		return domain.ClaimResult{}, err
	}
	if err := insertClaimSource(ctx, tx, source); err != nil {
		return domain.ClaimResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	updated, err := scanClaim(tx.QueryRow(ctx, `
		UPDATE core.claim
		SET status=$1,version=version+1,updated_at=$2
		WHERE workspace_id=$3 AND id=$4 AND version=$5
		RETURNING id::text,workspace_id::text,statement,normalized_statement,applicability,
			applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,
			fingerprint,version,created_at,updated_at`,
		string(domain.ClaimStatusConfirmed), record.At.UTC(), string(record.WorkspaceID), string(record.ClaimID), record.ExpectedVersion,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ClaimResult{}, versionConflict(err)
	}
	if err != nil {
		return domain.ClaimResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	sources = append(sources, source)
	if err := domain.ValidateClaimAggregate(updated, sources); err != nil {
		return domain.ClaimResult{}, consistency(domain.ErrorCodeClaimInvalid, err)
	}
	if err := insertReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandConfirmClaim, aggregateClaim, updated.ID, updated.Version, record.At); err != nil {
		return domain.ClaimResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	result, err := loadClaimResult(ctx, tx, record.WorkspaceID, record.ClaimID)
	if err != nil {
		return domain.ClaimResult{}, classifyRead(err, errorCodeClaimNotFound)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.ClaimResult{}, err
	}
	return result, nil
}

// TransitionClaim 以 CAS 执行冻结状态机中的 Claim 迁移。
func (r *Repository) TransitionClaim(ctx context.Context, record domain.TransitionClaimRecord) (domain.ClaimResult, error) {
	tx, receipt, err := r.beginCommand(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionClaim, aggregateClaim)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if receipt != nil {
		result, loadErr := loadClaimResult(ctx, tx, record.WorkspaceID, receipt.AggregateID)
		if loadErr != nil {
			return domain.ClaimResult{}, classifyRead(loadErr, errorCodeClaimNotFound)
		}
		if err := assertReceiptAggregate(receipt, result.Claim.ID); err != nil {
			return domain.ClaimResult{}, err
		}
		if err := requireReceiptVersion(receipt, result.Claim.Version); err != nil {
			return domain.ClaimResult{}, err
		}
		result.Replayed = true
		if err := commit(ctx, tx); err != nil {
			return domain.ClaimResult{}, err
		}
		return result, nil
	}
	current, sources, err := lockClaimAggregate(ctx, tx, record.WorkspaceID, record.ClaimID)
	if err != nil {
		return domain.ClaimResult{}, classifyRead(err, errorCodeClaimNotFound)
	}
	if current.Version != record.ExpectedVersion {
		return domain.ClaimResult{}, versionConflict(errors.New("claim expected version is stale"))
	}
	if err := domain.ValidateClaimTransition(current.Status, record.Status); err != nil {
		return domain.ClaimResult{}, err
	}
	updated, err := scanClaim(tx.QueryRow(ctx, `
		UPDATE core.claim
		SET status=$1,version=version+1,updated_at=$2
		WHERE workspace_id=$3 AND id=$4 AND version=$5
		RETURNING id::text,workspace_id::text,statement,normalized_statement,applicability,
			applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,
			fingerprint,version,created_at,updated_at`,
		string(record.Status), record.At.UTC(), string(record.WorkspaceID), string(record.ClaimID), record.ExpectedVersion,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ClaimResult{}, versionConflict(err)
	}
	if err != nil {
		return domain.ClaimResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if err := domain.ValidateClaimAggregate(updated, sources); err != nil {
		return domain.ClaimResult{}, consistency(domain.ErrorCodeClaimInvalid, err)
	}
	if err := insertReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionClaim, aggregateClaim, updated.ID, updated.Version, record.At); err != nil {
		return domain.ClaimResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.ClaimResult{}, err
	}
	return domain.ClaimResult{Claim: updated, Sources: sources}, nil
}

func lockClaimAggregate(ctx context.Context, tx pgx.Tx, workspaceID, claimID foundation.ID) (domain.Claim, []domain.ClaimSource, error) {
	claim, err := scanClaim(tx.QueryRow(ctx, claimSelect+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(workspaceID), string(claimID)))
	if err != nil {
		return domain.Claim{}, nil, err
	}
	sourcesByClaim, err := loadClaimSources(ctx, tx, workspaceID, []foundation.ID{claimID})
	if err != nil {
		return domain.Claim{}, nil, err
	}
	sources := sourcesByClaim[claimID]
	if err := domain.ValidateClaimAggregate(claim, sources); err != nil {
		return domain.Claim{}, nil, consistency(domain.ErrorCodeClaimInvalid, err)
	}
	return claim, sources, nil
}

func insertClaimSource(ctx context.Context, tx pgx.Tx, source domain.ClaimSource) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO core.claim_source (
			id,workspace_id,claim_id,source_version_id,source_span_id,support_type,
			reason,evidence_hash,model_run_ref,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		string(source.ID), string(source.WorkspaceID), string(source.ClaimID), string(source.Provenance.SourceVersionID),
		string(source.Provenance.SourceSpanID), string(source.SupportType), source.Reason, source.EvidenceHash,
		pointerString(source.ModelRunRef), source.CreatedAt.UTC(),
	)
	return err
}
