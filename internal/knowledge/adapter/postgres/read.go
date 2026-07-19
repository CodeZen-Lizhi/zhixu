package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

// BatchGetClaims 在一个一致读事务中单批加载 Claims 与全部 Sources。
func (r *Repository) BatchGetClaims(ctx context.Context, query domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
	if err := domain.ValidateBatchQuery(query.WorkspaceID, query.IDs, query.Limit); err != nil {
		return nil, err
	}
	if err := validateClaimStatuses(query.Statuses); err != nil {
		return nil, err
	}
	tx, err := beginRead(ctx, r.db)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, claimSelect+`
		WHERE workspace_id=$1
		  AND (cardinality($2::uuid[])=0 OR id=ANY($2::uuid[]))
		  AND (cardinality($3::text[])=0 OR status=ANY($3::text[]))
		ORDER BY updated_at DESC,id
		LIMIT $4`, string(query.WorkspaceID), idsAsStrings(query.IDs), statusStrings(query.Statuses), query.Limit)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	claims := make([]domain.Claim, 0, query.Limit)
	for rows.Next() {
		claim, scanErr := scanClaim(rows)
		if scanErr != nil {
			rows.Close()
			return nil, consistency(domain.ErrorCodeClaimInvalid, scanErr)
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	rows.Close()
	claimIDs := make([]foundation.ID, len(claims))
	for index := range claims {
		claimIDs[index] = claims[index].ID
	}
	sources, err := loadClaimSources(ctx, tx, query.WorkspaceID, claimIDs)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	result := make([]domain.ClaimWithSources, len(claims))
	for index, claim := range claims {
		if err := domain.ValidateClaimAggregate(claim, sources[claim.ID]); err != nil {
			return nil, consistency(domain.ErrorCodeClaimInvalid, err)
		}
		result[index] = domain.ClaimWithSources{Claim: claim, Sources: sources[claim.ID]}
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return result, nil
}

// BatchGetRelations 在一个一致读事务中单批加载 Relations 与全部 Evidence。
func (r *Repository) BatchGetRelations(ctx context.Context, query domain.BatchGetRelationsQuery) ([]domain.RelationWithEvidence, error) {
	if err := domain.ValidateBatchQuery(query.WorkspaceID, query.IDs, query.Limit); err != nil {
		return nil, err
	}
	if err := validateRelationStatuses(query.Statuses); err != nil {
		return nil, err
	}
	tx, err := beginRead(ctx, r.db)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, relationSelect+`
		WHERE workspace_id=$1
		  AND (cardinality($2::uuid[])=0 OR id=ANY($2::uuid[]))
		  AND (cardinality($3::text[])=0 OR status=ANY($3::text[]))
		ORDER BY updated_at DESC,id
		LIMIT $4`, string(query.WorkspaceID), idsAsStrings(query.IDs), statusStrings(query.Statuses), query.Limit)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	relations := make([]domain.Relation, 0, query.Limit)
	for rows.Next() {
		relation, scanErr := scanRelation(rows)
		if scanErr != nil {
			rows.Close()
			return nil, consistency(domain.ErrorCodeRelationInvalid, scanErr)
		}
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	rows.Close()
	relationIDs := make([]foundation.ID, len(relations))
	for index := range relations {
		relationIDs[index] = relations[index].ID
	}
	evidence, err := loadRelationEvidence(ctx, tx, query.WorkspaceID, relationIDs)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	result := make([]domain.RelationWithEvidence, len(relations))
	for index, relation := range relations {
		if err := domain.ValidateRelationAggregate(relation, evidence[relation.ID]); err != nil {
			return nil, consistency(domain.ErrorCodeRelationInvalid, err)
		}
		result[index] = domain.RelationWithEvidence{Relation: relation, Evidence: evidence[relation.ID]}
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return result, nil
}

// BatchGetConflicts 在一个一致读事务中单批加载 Conflicts 与全部 Members。
func (r *Repository) BatchGetConflicts(ctx context.Context, query domain.BatchGetConflictsQuery) ([]domain.ConflictWithMembers, error) {
	if err := domain.ValidateBatchQuery(query.WorkspaceID, query.IDs, query.Limit); err != nil {
		return nil, err
	}
	if err := validateConflictStatuses(query.Statuses); err != nil {
		return nil, err
	}
	tx, err := beginRead(ctx, r.db)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, conflictSelect+`
		WHERE workspace_id=$1
		  AND (cardinality($2::uuid[])=0 OR id=ANY($2::uuid[]))
		  AND (cardinality($3::text[])=0 OR status=ANY($3::text[]))
		ORDER BY updated_at DESC,id
		LIMIT $4`, string(query.WorkspaceID), idsAsStrings(query.IDs), statusStrings(query.Statuses), query.Limit)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	stored := make([]storedConflict, 0, query.Limit)
	for rows.Next() {
		conflict, scanErr := scanConflict(rows)
		if scanErr != nil {
			rows.Close()
			return nil, consistency(domain.ErrorCodeConflictInvalid, scanErr)
		}
		stored = append(stored, conflict)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	rows.Close()
	conflictIDs := make([]foundation.ID, len(stored))
	for index := range stored {
		conflictIDs[index] = stored[index].Conflict.ID
	}
	members, err := loadConflictMembers(ctx, tx, query.WorkspaceID, conflictIDs)
	if err != nil {
		return nil, classify(err, errorCodeDatabaseUnavailable)
	}
	result := make([]domain.ConflictWithMembers, len(stored))
	for index, conflict := range stored {
		if err := validateStoredConflict(conflict, members[conflict.Conflict.ID]); err != nil {
			return nil, err
		}
		result[index] = domain.ConflictWithMembers{Conflict: conflict.Conflict, Members: members[conflict.Conflict.ID]}
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return result, nil
}

type beginTxer interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

func beginRead(ctx context.Context, db DB) (pgx.Tx, error) {
	if beginTx, ok := db.(beginTxer); ok {
		return beginTx.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	}
	return db.Begin(ctx)
}

func validateClaimStatuses(statuses []domain.ClaimStatus) error {
	for _, status := range statuses {
		switch status {
		case domain.ClaimStatusSuggested, domain.ClaimStatusConfirmed, domain.ClaimStatusDisputed,
			domain.ClaimStatusSuperseded, domain.ClaimStatusDeprecated, domain.ClaimStatusInvalid:
		default:
			return invalidQuery(errors.New("claim status filter is invalid"))
		}
	}
	return nil
}

func validateRelationStatuses(statuses []domain.RelationStatus) error {
	for _, status := range statuses {
		switch status {
		case domain.RelationStatusSuggested, domain.RelationStatusConfirmed, domain.RelationStatusRejected,
			domain.RelationStatusStale, domain.RelationStatusDeprecated:
		default:
			return invalidQuery(errors.New("relation status filter is invalid"))
		}
	}
	return nil
}

func validateConflictStatuses(statuses []domain.ConflictStatus) error {
	for _, status := range statuses {
		switch status {
		case domain.ConflictStatusOpen, domain.ConflictStatusInvestigating, domain.ConflictStatusResolutionProposed,
			domain.ConflictStatusResolved, domain.ConflictStatusAcceptedDivergence, domain.ConflictStatusDeferred:
		default:
			return invalidQuery(errors.New("conflict status filter is invalid"))
		}
	}
	return nil
}

func invalidQuery(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeRequestInvalid, false, err)
}
