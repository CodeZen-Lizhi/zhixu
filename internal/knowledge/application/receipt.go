package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func (s *Service) lookupCommandReceipt(ctx context.Context, workspaceID foundation.ID, key, requestHash string, commandType domain.CommandType, aggregateType domain.AggregateType, targetID foundation.ID) (domain.CommandReceipt, bool, error) {
	query := domain.CommandReceiptQuery{WorkspaceID: workspaceID, IdempotencyKey: key, RequestHash: requestHash, CommandType: commandType, AggregateType: aggregateType}
	if err := domain.ValidateCommandReceiptQuery(query); err != nil {
		return domain.CommandReceipt{}, false, err
	}
	lookup, err := s.dependencies.Repository.LookupCommandReceipt(ctx, query)
	if err != nil {
		return domain.CommandReceipt{}, false, err
	}
	if !lookup.Found {
		return domain.CommandReceipt{}, false, nil
	}
	receipt := lookup.Receipt
	if err := domain.ValidateCommandReceipt(receipt); err != nil {
		return domain.CommandReceipt{}, false, consistencyError("repository returned an invalid command receipt")
	}
	if receipt.WorkspaceID != workspaceID || receipt.IdempotencyKey != key || receipt.RequestHash != requestHash || receipt.CommandType != commandType || receipt.AggregateType != aggregateType {
		return domain.CommandReceipt{}, false, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIdempotencyConflict, false, errors.New("idempotency key is bound to a different knowledge command"))
	}
	if targetID != "" && receipt.AggregateID != targetID {
		return domain.CommandReceipt{}, false, consistencyError("command receipt points to a different target aggregate")
	}
	return receipt, true, nil
}

func (s *Service) replayTopic(ctx context.Context, receipt domain.CommandReceipt) (domain.TopicResult, error) {
	topic, err := s.GetTopic(ctx, receipt.WorkspaceID, receipt.AggregateID)
	if err != nil {
		return domain.TopicResult{}, err
	}
	if topic.Version < receipt.AggregateVersion {
		return domain.TopicResult{}, consistencyError("topic version is older than command receipt")
	}
	return domain.TopicResult{Topic: topic, Replayed: true}, nil
}

func (s *Service) replayClaim(ctx context.Context, receipt domain.CommandReceipt) (domain.ClaimResult, error) {
	items, err := s.GetClaims(ctx, GetClaimsQuery{WorkspaceID: receipt.WorkspaceID, IDs: []foundation.ID{receipt.AggregateID}, Limit: 1})
	if err != nil {
		return domain.ClaimResult{}, err
	}
	if len(items) != 1 || items[0].Claim.ID != receipt.AggregateID || items[0].Claim.Version < receipt.AggregateVersion {
		return domain.ClaimResult{}, consistencyError("claim fact does not match command receipt")
	}
	return domain.ClaimResult{Claim: items[0].Claim, Sources: items[0].Sources, Replayed: true}, nil
}

func (s *Service) replayRelation(ctx context.Context, receipt domain.CommandReceipt) (domain.RelationResult, error) {
	items, err := s.GetRelations(ctx, GetRelationsQuery{WorkspaceID: receipt.WorkspaceID, IDs: []foundation.ID{receipt.AggregateID}, Limit: 1})
	if err != nil {
		return domain.RelationResult{}, err
	}
	if len(items) != 1 || items[0].Relation.ID != receipt.AggregateID || items[0].Relation.Version < receipt.AggregateVersion {
		return domain.RelationResult{}, consistencyError("relation fact does not match command receipt")
	}
	return domain.RelationResult{Relation: items[0].Relation, Evidence: items[0].Evidence, Replayed: true}, nil
}

func (s *Service) replayConflict(ctx context.Context, receipt domain.CommandReceipt) (domain.ConflictResult, error) {
	items, err := s.GetConflicts(ctx, GetConflictsQuery{WorkspaceID: receipt.WorkspaceID, IDs: []foundation.ID{receipt.AggregateID}, Limit: 1})
	if err != nil {
		return domain.ConflictResult{}, err
	}
	if len(items) != 1 || items[0].Conflict.ID != receipt.AggregateID || items[0].Conflict.Version < receipt.AggregateVersion {
		return domain.ConflictResult{}, consistencyError("conflict fact does not match command receipt")
	}
	return domain.ConflictResult{Conflict: items[0].Conflict, Members: items[0].Members, Replayed: true}, nil
}
