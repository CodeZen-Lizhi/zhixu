package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// GetClaimsQuery 是有界 Claim 聚合查询。
type GetClaimsQuery = domain.BatchGetClaimsQuery

// GetRelationsQuery 是有界 Relation 聚合查询。
type GetRelationsQuery = domain.BatchGetRelationsQuery

// GetConflictsQuery 是有界 Conflict 聚合查询。
type GetConflictsQuery = domain.BatchGetConflictsQuery

// GetTopic 按 Workspace 读取并校验 Topic。
func (s *Service) GetTopic(ctx context.Context, workspaceID, topicID foundation.ID) (domain.Topic, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return domain.Topic{}, err
	}
	if !validID(workspaceID) || !validID(topicID) {
		return domain.Topic{}, requestError("topic query identity is invalid")
	}
	topic, err := s.dependencies.Repository.GetTopic(ctx, workspaceID, topicID)
	if err != nil {
		return domain.Topic{}, err
	}
	if topic.ID != topicID || topic.WorkspaceID != workspaceID || domain.ValidateTopic(topic) != nil {
		return domain.Topic{}, consistencyError("repository returned an invalid topic")
	}
	return topic, nil
}

// GetClaims 批量读取最多 500 个 Claim 及其 Sources。
func (s *Service) GetClaims(ctx context.Context, query GetClaimsQuery) ([]domain.ClaimWithSources, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return nil, err
	}
	return getClaims(ctx, s.dependencies.Repository, query)
}

func getClaims(ctx context.Context, repository ClaimQueryRepository, query GetClaimsQuery) ([]domain.ClaimWithSources, error) {
	if err := validateBatchQuery(query.WorkspaceID, query.IDs, query.Limit); err != nil {
		return nil, err
	}
	if !validClaimStatuses(query.Statuses) {
		return nil, requestError("claim query statuses are invalid")
	}
	result, err := repository.BatchGetClaims(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(result) > query.Limit {
		return nil, consistencyError("claim query exceeded requested limit")
	}
	ids, statuses := idSet(query.IDs), claimStatusSet(query.Statuses)
	var previous domain.Claim
	for i, item := range result {
		if item.Claim.WorkspaceID != query.WorkspaceID || domain.ValidateClaimAggregate(item.Claim, item.Sources) != nil || !matchesID(ids, item.Claim.ID) || !matchesClaimStatus(statuses, item.Claim.Status) {
			return nil, consistencyError("repository returned an out-of-scope claim")
		}
		if i > 0 && !ordered(previous.UpdatedAt, previous.ID, item.Claim.UpdatedAt, item.Claim.ID) {
			return nil, consistencyError("claim query result is not stably ordered")
		}
		previous = item.Claim
	}
	return result, nil
}

// GetRelations 批量读取最多 500 个 Relation 及其 Evidence。
func (s *Service) GetRelations(ctx context.Context, query GetRelationsQuery) ([]domain.RelationWithEvidence, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return nil, err
	}
	if err := validateBatchQuery(query.WorkspaceID, query.IDs, query.Limit); err != nil {
		return nil, err
	}
	if !validRelationStatuses(query.Statuses) {
		return nil, requestError("relation query statuses are invalid")
	}
	result, err := s.dependencies.Repository.BatchGetRelations(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(result) > query.Limit {
		return nil, consistencyError("relation query exceeded requested limit")
	}
	ids, statuses := idSet(query.IDs), relationStatusSet(query.Statuses)
	var previous domain.Relation
	for i, item := range result {
		if item.Relation.WorkspaceID != query.WorkspaceID || domain.ValidateRelationAggregate(item.Relation, item.Evidence) != nil || !matchesID(ids, item.Relation.ID) || !matchesRelationStatus(statuses, item.Relation.Status) {
			return nil, consistencyError("repository returned an out-of-scope relation")
		}
		if i > 0 && !ordered(previous.UpdatedAt, previous.ID, item.Relation.UpdatedAt, item.Relation.ID) {
			return nil, consistencyError("relation query result is not stably ordered")
		}
		previous = item.Relation
	}
	return result, nil
}

// GetConflicts 批量读取最多 500 个 Conflict 及其 Members。
func (s *Service) GetConflicts(ctx context.Context, query GetConflictsQuery) ([]domain.ConflictWithMembers, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return nil, err
	}
	if err := validateBatchQuery(query.WorkspaceID, query.IDs, query.Limit); err != nil {
		return nil, err
	}
	if !validConflictStatuses(query.Statuses) {
		return nil, requestError("conflict query statuses are invalid")
	}
	result, err := s.dependencies.Repository.BatchGetConflicts(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(result) > query.Limit {
		return nil, consistencyError("conflict query exceeded requested limit")
	}
	ids, statuses := idSet(query.IDs), conflictStatusSet(query.Statuses)
	var previous domain.Conflict
	for i, item := range result {
		if item.Conflict.WorkspaceID != query.WorkspaceID || domain.ValidateConflictAggregate(item.Conflict, item.Members) != nil || !matchesID(ids, item.Conflict.ID) || !matchesConflictStatus(statuses, item.Conflict.Status) {
			return nil, consistencyError("repository returned an out-of-scope conflict")
		}
		if i > 0 && !ordered(previous.UpdatedAt, previous.ID, item.Conflict.UpdatedAt, item.Conflict.ID) {
			return nil, consistencyError("conflict query result is not stably ordered")
		}
		previous = item.Conflict
	}
	return result, nil
}

func validateBatchQuery(workspaceID foundation.ID, ids []foundation.ID, limit int) error {
	return domain.ValidateBatchQuery(workspaceID, ids, limit)
}
func idSet(values []foundation.ID) map[foundation.ID]struct{} {
	if len(values) == 0 {
		return nil
	}
	result := make(map[foundation.ID]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
func matchesID(values map[foundation.ID]struct{}, value foundation.ID) bool {
	if values == nil {
		return true
	}
	_, ok := values[value]
	return ok
}
func ordered(leftAt time.Time, leftID foundation.ID, rightAt time.Time, rightID foundation.ID) bool {
	return leftAt.After(rightAt) || (leftAt.Equal(rightAt) && leftID < rightID)
}

func validClaimStatuses(values []domain.ClaimStatus) bool {
	seen := map[domain.ClaimStatus]struct{}{}
	for _, value := range values {
		if !validClaimStatus(value) {
			return false
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
func validClaimStatus(value domain.ClaimStatus) bool {
	switch value {
	case domain.ClaimStatusSuggested, domain.ClaimStatusConfirmed, domain.ClaimStatusDisputed, domain.ClaimStatusSuperseded, domain.ClaimStatusDeprecated, domain.ClaimStatusInvalid:
		return true
	}
	return false
}
func validRelationStatuses(values []domain.RelationStatus) bool {
	seen := map[domain.RelationStatus]struct{}{}
	for _, value := range values {
		if !validRelationStatus(value) {
			return false
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
func validRelationStatus(value domain.RelationStatus) bool {
	switch value {
	case domain.RelationStatusSuggested, domain.RelationStatusConfirmed, domain.RelationStatusRejected, domain.RelationStatusStale, domain.RelationStatusDeprecated:
		return true
	}
	return false
}
func validConflictStatuses(values []domain.ConflictStatus) bool {
	seen := map[domain.ConflictStatus]struct{}{}
	for _, value := range values {
		if !validConflictStatus(value) {
			return false
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
func validConflictStatus(value domain.ConflictStatus) bool {
	switch value {
	case domain.ConflictStatusOpen, domain.ConflictStatusInvestigating, domain.ConflictStatusResolutionProposed, domain.ConflictStatusResolved, domain.ConflictStatusAcceptedDivergence, domain.ConflictStatusDeferred:
		return true
	}
	return false
}
func claimStatusSet(values []domain.ClaimStatus) map[domain.ClaimStatus]struct{} {
	if len(values) == 0 {
		return nil
	}
	result := map[domain.ClaimStatus]struct{}{}
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
func relationStatusSet(values []domain.RelationStatus) map[domain.RelationStatus]struct{} {
	if len(values) == 0 {
		return nil
	}
	result := map[domain.RelationStatus]struct{}{}
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
func conflictStatusSet(values []domain.ConflictStatus) map[domain.ConflictStatus]struct{} {
	if len(values) == 0 {
		return nil
	}
	result := map[domain.ConflictStatus]struct{}{}
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
func matchesClaimStatus(values map[domain.ClaimStatus]struct{}, value domain.ClaimStatus) bool {
	if values == nil {
		return true
	}
	_, ok := values[value]
	return ok
}
func matchesRelationStatus(values map[domain.RelationStatus]struct{}, value domain.RelationStatus) bool {
	if values == nil {
		return true
	}
	_, ok := values[value]
	return ok
}
func matchesConflictStatus(values map[domain.ConflictStatus]struct{}, value domain.ConflictStatus) bool {
	if values == nil {
		return true
	}
	_, ok := values[value]
	return ok
}
