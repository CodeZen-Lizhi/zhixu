package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

var _ knowledgeapplication.ClaimQueryRepository = (*GORMRepository)(nil)
var _ knowledgeapplication.ScopedClaimReader = (*GORMRepository)(nil)

// BatchGetClaims loads bounded Claim aggregates from one repeatable, read-only
// snapshot so aggregate rows and their sources cannot be mixed across commits.
func (repository *GORMRepository) BatchGetClaims(ctx context.Context, query domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if err := domain.ValidateBatchQuery(query.WorkspaceID, query.IDs, query.Limit); err != nil {
		return nil, err
	}
	if err := validateClaimStatuses(query.Statuses); err != nil {
		return nil, err
	}
	var result []domain.ClaimWithSources
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		var err error
		result, err = gormKnowledgeBatchGetClaims(callbackCtx, transaction, query)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// BatchGetClaimsScoped 在调用方快照内读取 Claim 与 Sources，不另开事务。
func (repository *GORMRepository) BatchGetClaimsScoped(ctx context.Context, scope foundation.TransactionScope, query domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if err := domain.ValidateBatchQuery(query.WorkspaceID, query.IDs, query.Limit); err != nil {
		return nil, err
	}
	if err := validateClaimStatuses(query.Statuses); err != nil {
		return nil, err
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, knowledgeGORMUnavailable(err)
	}
	return gormKnowledgeBatchGetClaims(ctx, transaction, query)
}

func gormKnowledgeBatchGetClaims(ctx context.Context, transaction *gorm.DB, query domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
	rows, err := gormKnowledgeRawRows(ctx, transaction, claimSelect+`
			WHERE workspace_id=?
			  AND (cardinality(?::uuid[])=0 OR id=ANY(?::uuid[]))
			  AND (cardinality(?::text[])=0 OR status=ANY(?::text[]))
			ORDER BY updated_at DESC,id
			LIMIT ?`, string(query.WorkspaceID), pq.Array(idsAsStrings(query.IDs)), pq.Array(idsAsStrings(query.IDs)), pq.Array(statusStrings(query.Statuses)), pq.Array(statusStrings(query.Statuses)), query.Limit)
	if err != nil {
		return nil, classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	claims := make([]domain.Claim, 0, query.Limit)
	for rows.Next() {
		claim, scanErr := scanClaim(rows)
		if scanErr != nil {
			return nil, consistency(domain.ErrorCodeClaimInvalid, scanErr)
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
	}
	claimIDs := make([]foundation.ID, len(claims))
	for index := range claims {
		claimIDs[index] = claims[index].ID
	}
	sources, err := gormKnowledgeReadClaimSources(ctx, transaction, query.WorkspaceID, claimIDs)
	if err != nil {
		return nil, classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
	}
	result := make([]domain.ClaimWithSources, len(claims))
	for index, claim := range claims {
		if err := domain.ValidateClaimAggregate(claim, sources[claim.ID]); err != nil {
			return nil, consistency(domain.ErrorCodeClaimInvalid, err)
		}
		result[index] = domain.ClaimWithSources{Claim: claim, Sources: sources[claim.ID]}
	}
	return result, nil
}

// BatchGetRelations loads bounded Relation aggregates from one repeatable,
// read-only snapshot without per-Relation evidence queries.
func (repository *GORMRepository) BatchGetRelations(ctx context.Context, query domain.BatchGetRelationsQuery) ([]domain.RelationWithEvidence, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if err := domain.ValidateBatchQuery(query.WorkspaceID, query.IDs, query.Limit); err != nil {
		return nil, err
	}
	if err := validateRelationStatuses(query.Statuses); err != nil {
		return nil, err
	}
	var result []domain.RelationWithEvidence
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		rows, err := gormKnowledgeRawRows(callbackCtx, transaction, relationSelect+`
			WHERE workspace_id=?
			  AND (cardinality(?::uuid[])=0 OR id=ANY(?::uuid[]))
			  AND (cardinality(?::text[])=0 OR status=ANY(?::text[]))
			ORDER BY updated_at DESC,id
			LIMIT ?`, string(query.WorkspaceID), pq.Array(idsAsStrings(query.IDs)), pq.Array(idsAsStrings(query.IDs)), pq.Array(statusStrings(query.Statuses)), pq.Array(statusStrings(query.Statuses)), query.Limit)
		if err != nil {
			return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
		}
		defer rows.Close()
		relations := make([]domain.Relation, 0, query.Limit)
		for rows.Next() {
			relation, scanErr := scanRelation(rows)
			if scanErr != nil {
				return consistency(domain.ErrorCodeRelationInvalid, scanErr)
			}
			relations = append(relations, relation)
		}
		if err := rows.Err(); err != nil {
			return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
		}
		relationIDs := make([]foundation.ID, len(relations))
		for index := range relations {
			relationIDs[index] = relations[index].ID
		}
		evidence, err := gormKnowledgeReadRelationEvidence(callbackCtx, transaction, query.WorkspaceID, relationIDs)
		if err != nil {
			return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
		}
		result = make([]domain.RelationWithEvidence, len(relations))
		for index, relation := range relations {
			if err := domain.ValidateRelationAggregate(relation, evidence[relation.ID]); err != nil {
				return consistency(domain.ErrorCodeRelationInvalid, err)
			}
			result[index] = domain.RelationWithEvidence{Relation: relation, Evidence: evidence[relation.ID]}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (repository *GORMRepository) gormReadSnapshot(ctx context.Context, work func(context.Context, *gorm.DB) error) error {
	return repository.within(ctx,
		foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true},
		errorCodeDatabaseUnavailable,
		errorCodeDatabaseUnavailable,
		classifyGORMKnowledge,
		func(callbackCtx context.Context, _ foundation.TransactionScope, transaction *gorm.DB) error {
			if work == nil {
				return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeRequestInvalid, false, errors.New("knowledge read work is nil"))
			}
			return work(callbackCtx, transaction)
		},
	)
}

func gormKnowledgeReadClaimSources(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, claimIDs []foundation.ID) (map[foundation.ID][]domain.ClaimSource, error) {
	result := make(map[foundation.ID][]domain.ClaimSource, len(claimIDs))
	for _, id := range claimIDs {
		result[id] = []domain.ClaimSource{}
	}
	if len(claimIDs) == 0 {
		return result, nil
	}
	rows, err := gormKnowledgeRawRows(ctx, database, `
		SELECT id::text,workspace_id::text,claim_id::text,source_version_id::text,source_span_id::text,
			support_type,reason,evidence_hash,model_run_ref,created_at
		FROM core.claim_source
		WHERE workspace_id=? AND claim_id=ANY(?::uuid[])
		ORDER BY claim_id,created_at,id`, string(workspaceID), pq.Array(idsAsStrings(claimIDs)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		source, scanErr := scanClaimSource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if _, ok := result[source.ClaimID]; !ok {
			return nil, consistency(domain.ErrorCodeClaimInvalid, errors.New("knowledge claim source is outside requested aggregate set"))
		}
		result[source.ClaimID] = append(result[source.ClaimID], source)
	}
	return result, rows.Err()
}

func gormKnowledgeReadRelationEvidence(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, relationIDs []foundation.ID) (map[foundation.ID][]domain.RelationEvidence, error) {
	result := make(map[foundation.ID][]domain.RelationEvidence, len(relationIDs))
	for _, id := range relationIDs {
		result[id] = []domain.RelationEvidence{}
	}
	if len(relationIDs) == 0 {
		return result, nil
	}
	rows, err := gormKnowledgeRawRows(ctx, database, `
		SELECT evidence.id::text,evidence.workspace_id::text,evidence.relation_id::text,
			evidence.source_version_id::text,evidence.source_span_id::text,evidence.reason,
			evidence.applicability,evidence.applicability_schema_version,evidence.applicability_hash,
			evidence.evidence_hash,evidence.model_run_ref,evidence.confirmation_method,evidence.confirmed_by,
			evidence.created_at
		FROM core.relation_evidence evidence
		WHERE evidence.workspace_id=? AND evidence.relation_id=ANY(?::uuid[])
		ORDER BY evidence.relation_id,evidence.created_at,evidence.id`, string(workspaceID), pq.Array(idsAsStrings(relationIDs)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, scanErr := scanRelationEvidence(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if _, ok := result[item.RelationID]; !ok {
			return nil, consistency(domain.ErrorCodeRelationInvalid, errors.New("knowledge relation evidence is outside requested aggregate set"))
		}
		result[item.RelationID] = append(result[item.RelationID], item)
	}
	return result, rows.Err()
}
