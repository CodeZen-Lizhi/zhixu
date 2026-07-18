package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

type activationLockedIndexes struct {
	target  domain.IndexVersion
	current *domain.IndexVersion
}

// activateTx 是普通 Activate 与 Reindex 完成事务共享的唯一 Index 切换实现。
// locked 非空时调用方已经按更大的跨域锁序持有目标和当前 Active 行锁。
func activateTx(ctx context.Context, tx pgx.Tx, command domain.ActivationCommand, kind domain.ActivationKind, locked *activationLockedIndexes) (domain.ActivationResult, error) {
	if existing, found, queryErr := getActivationByKey(ctx, tx, command.WorkspaceID, command.IdempotencyKey); queryErr != nil {
		return domain.ActivationResult{}, queryErr
	} else if found {
		return replayActivationTx(ctx, tx, command, kind, existing)
	}

	var target domain.IndexVersion
	var current *domain.IndexVersion
	if locked != nil {
		target, current = locked.target, locked.current
	} else {
		var err error
		target, err = getIndexForUpdate(ctx, tx, command.WorkspaceID, command.TargetIndexVersionID)
		if err != nil {
			return domain.ActivationResult{}, err
		}
		active, activeErr := scanIndex(tx.QueryRow(ctx, `SELECT `+indexColumns+` FROM retrieval.index_version WHERE workspace_id=$1 AND status='active' FOR UPDATE`, string(command.WorkspaceID)))
		if activeErr == nil {
			current = &active
		} else if !errors.Is(activeErr, pgx.ErrNoRows) {
			return domain.ActivationResult{}, classify(activeErr, "RETRIEVAL_ACTIVE_QUERY_FAILED")
		}
	}

	rollback := kind == domain.ActivationKindRollback
	if rollback {
		if current == nil {
			return domain.ActivationResult{}, notFound("RETRIEVAL_ACTIVE_NOT_FOUND", pgx.ErrNoRows)
		}
		rollbackCommand := domain.RollbackActivationCommand{
			ActivationID: command.ActivationID, WorkspaceID: command.WorkspaceID,
			TargetIndexVersionID: command.TargetIndexVersionID, ExpectedTargetVersion: command.ExpectedTargetVersion,
			ExpectedCurrentIndexVersionID: *command.ExpectedCurrentIndexVersionID, ExpectedCurrentVersion: *command.ExpectedCurrentVersion,
			IdempotencyKey: command.IdempotencyKey, ReasonCode: command.ReasonCode, At: command.At,
		}
		if err := domain.ValidateRollbackActivationCommand(*current, target, rollbackCommand); err != nil {
			return domain.ActivationResult{}, err
		}
	} else if err := domain.ValidateActivationCommand(current, target, command); err != nil {
		return domain.ActivationResult{}, err
	}

	activation := domain.Activation{
		ID: command.ActivationID, Kind: kind, WorkspaceID: command.WorkspaceID,
		TargetIndexVersionID: command.TargetIndexVersionID, PreviousIndexVersionID: command.ExpectedCurrentIndexVersionID,
		TargetVersion: command.ExpectedTargetVersion + 1, IdempotencyKey: command.IdempotencyKey,
		ReasonCode: command.ReasonCode, CreatedAt: command.At,
	}
	var previousNext any
	if command.ExpectedCurrentVersion != nil {
		value := *command.ExpectedCurrentVersion + 1
		activation.PreviousVersion = &value
		previousNext = value
	}
	if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(
		id,kind,workspace_id,target_index_version_id,previous_index_version_id,target_version,previous_version,idempotency_key,reason_code,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, string(command.ActivationID), string(kind), string(command.WorkspaceID),
		string(command.TargetIndexVersionID), optionalID(command.ExpectedCurrentIndexVersionID), command.ExpectedTargetVersion+1,
		previousNext, command.IdempotencyKey, command.ReasonCode, command.At.UTC()); err != nil {
		return domain.ActivationResult{}, classify(err, "RETRIEVAL_ACTIVATION_CREATE_FAILED")
	}
	var previous *domain.IndexVersion
	if current != nil {
		updated, err := scanIndex(tx.QueryRow(ctx, `UPDATE retrieval.index_version SET status='retiring',version=version+1,
			updated_at=$1,retired_at=$1 WHERE id=$2 AND workspace_id=$3 AND status='active' AND version=$4 RETURNING `+indexColumns,
			command.At.UTC(), string(current.ID), string(command.WorkspaceID), current.Version))
		if err != nil {
			return domain.ActivationResult{}, classify(err, "RETRIEVAL_PREVIOUS_RETIRE_FAILED")
		}
		previous = &updated
	}
	updatedTarget, err := scanIndex(tx.QueryRow(ctx, `UPDATE retrieval.index_version SET status='active',version=version+1,
		updated_at=$1,activated_at=$1 WHERE id=$2 AND workspace_id=$3 AND status=$4 AND version=$5 RETURNING `+indexColumns,
		command.At.UTC(), string(target.ID), string(command.WorkspaceID), string(target.Status), target.Version))
	if err != nil {
		return domain.ActivationResult{}, classify(err, "RETRIEVAL_TARGET_ACTIVATE_FAILED")
	}
	return domain.ActivationResult{Activation: activation, ActiveIndexVersion: updatedTarget, PreviousIndexVersion: previous}, nil
}

func replayActivationTx(ctx context.Context, tx pgx.Tx, command domain.ActivationCommand, kind domain.ActivationKind, existing domain.Activation) (domain.ActivationResult, error) {
	expectedTargetNext := command.ExpectedTargetVersion + 1
	var expectedPreviousNext *int64
	if command.ExpectedCurrentVersion != nil {
		value := *command.ExpectedCurrentVersion + 1
		expectedPreviousNext = &value
	}
	if existing.Kind != kind || existing.TargetIndexVersionID != command.TargetIndexVersionID ||
		!sameOptionalID(existing.PreviousIndexVersionID, command.ExpectedCurrentIndexVersionID) ||
		existing.ReasonCode != command.ReasonCode || existing.TargetVersion != expectedTargetNext ||
		!sameOptionalInt64(existing.PreviousVersion, expectedPreviousNext) {
		return domain.ActivationResult{}, conflict("RETRIEVAL_ACTIVATION_IDEMPOTENCY_CONFLICT", errors.New("activation idempotency key has a different binding"))
	}
	activeCurrent, err := getIndexTx(ctx, tx, command.WorkspaceID, existing.TargetIndexVersionID)
	if err != nil {
		return domain.ActivationResult{}, err
	}
	active := activationIndexSnapshot(activeCurrent, domain.IndexStatusActive, existing.TargetVersion, existing.CreatedAt)
	var previous *domain.IndexVersion
	if existing.PreviousIndexVersionID != nil {
		currentPrevious, queryErr := getIndexTx(ctx, tx, command.WorkspaceID, *existing.PreviousIndexVersionID)
		if queryErr != nil {
			return domain.ActivationResult{}, queryErr
		}
		value := activationIndexSnapshot(currentPrevious, domain.IndexStatusRetiring, *existing.PreviousVersion, existing.CreatedAt)
		previous = &value
	}
	return domain.ActivationResult{Activation: existing, ActiveIndexVersion: active, PreviousIndexVersion: previous, Replayed: true}, nil
}
