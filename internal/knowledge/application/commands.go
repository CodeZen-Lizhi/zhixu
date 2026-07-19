package application

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// CreateTopicCommand 创建一个活动 Topic。
type CreateTopicCommand struct {
	WorkspaceID       foundation.ID
	Name, Description string
	Aliases           []string
	IdempotencyKey    string
}

// SuggestClaimCommand 创建一个带 canonical Applicability 的候选 Claim。
type SuggestClaimCommand struct {
	WorkspaceID       foundation.ID
	Statement         string
	Applicability     json.RawMessage
	ConfidenceScore   *float64
	ConfidenceFactors json.RawMessage
	IdempotencyKey    string
}

// ClaimSourceInput 是 ConfirmClaim 所需的不可变来源输入。
type ClaimSourceInput struct {
	Provenance  domain.ProvenanceRef
	SupportType domain.ClaimSupportType
	Reason      string
	ModelRunRef *string
}

// ConfirmClaimCommand 以一条 SUPPORTS 来源确认 Claim。
type ConfirmClaimCommand struct {
	WorkspaceID, ClaimID foundation.ID
	ExpectedVersion      int64
	Source               ClaimSourceInput
	IdempotencyKey       string
}

// TransitionClaimCommand 执行 Claim 乐观锁状态迁移。
type TransitionClaimCommand struct {
	WorkspaceID, ClaimID foundation.ID
	ExpectedVersion      int64
	Status               domain.ClaimStatus
	IdempotencyKey       string
}

// RelationEvidenceInput 是 Relation Evidence 的边界输入。
type RelationEvidenceInput struct {
	Provenance    domain.ProvenanceRef
	Reason        string
	Applicability json.RawMessage
	ModelRunRef   *string
}

// SuggestRelationCommand 创建一条 Suggested Relation 及可选 Evidence。
type SuggestRelationCommand struct {
	WorkspaceID        foundation.ID
	Source, Target     domain.NodeRef
	Type               domain.RelationType
	ConfidenceScore    *float64
	ValidFrom, ValidTo *time.Time
	Evidence           []RelationEvidenceInput
	IdempotencyKey     string
}

// ConfirmRelationCommand 以经验证的 Evidence 和 Confirmation 确认 Relation。
type ConfirmRelationCommand struct {
	WorkspaceID, RelationID foundation.ID
	ExpectedVersion         int64
	Evidence                RelationEvidenceInput
	Confirmation            domain.Confirmation
	IdempotencyKey          string
}

// TransitionRelationCommand 执行非确认 Relation 状态迁移。
type TransitionRelationCommand struct {
	WorkspaceID, RelationID foundation.ID
	ExpectedVersion         int64
	Status                  domain.RelationStatus
	Evidence                *RelationEvidenceInput
	IdempotencyKey          string
}

// ConflictMemberInput 是冲突成员的 Claim 与 Applicability 快照。
type ConflictMemberInput struct {
	ClaimID         foundation.ID
	Applicability   json.RawMessage
	PositionSummary string
}

// OpenConflictCommand 原子创建 Conflict、成员并争议相关 Claim。
type OpenConflictCommand struct {
	WorkspaceID             foundation.ID
	TopicID                 *foundation.ID
	Severity                domain.ConflictSeverity
	Summary                 string
	ApplicabilityAssessment domain.ApplicabilityAssessment
	ReviewedOverlapReason   string
	Members                 []ConflictMemberInput
	IdempotencyKey          string
}

// TransitionConflictCommand 执行 Conflict 乐观锁状态迁移。
type TransitionConflictCommand struct {
	WorkspaceID, ConflictID         foundation.ID
	ExpectedVersion                 int64
	Status                          domain.ConflictStatus
	Resolution, ResolutionReference *string
	IdempotencyKey                  string
}

// CreateTopic 规范化 Topic 后执行幂等创建。
func (s *Service) CreateTopic(ctx context.Context, command CreateTopicCommand) (domain.TopicResult, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return domain.TopicResult{}, err
	}
	key, err := normalizeCreateCommandIdentity(command.WorkspaceID, command.IdempotencyKey)
	if err != nil {
		return domain.TopicResult{}, err
	}
	name, normalized, err := domain.NormalizeTopicText(command.Name)
	if err != nil {
		return domain.TopicResult{}, err
	}
	description, err := domain.NormalizeDescription(command.Description)
	if err != nil {
		return domain.TopicResult{}, err
	}
	if len(command.Aliases) > MaxBatchSize {
		return domain.TopicResult{}, requestError("topic alias count exceeds limit")
	}
	aliases := make([]domain.TopicAlias, len(command.Aliases))
	for i, raw := range command.Aliases {
		display, canonical, aliasErr := domain.NormalizeTopicText(raw)
		if aliasErr != nil {
			return domain.TopicResult{}, aliasErr
		}
		aliases[i] = domain.TopicAlias{Name: display, NormalizedName: canonical}
	}
	sort.Slice(aliases, func(i, j int) bool {
		if aliases[i].NormalizedName == aliases[j].NormalizedName {
			return aliases[i].Name < aliases[j].Name
		}
		return aliases[i].NormalizedName < aliases[j].NormalizedName
	})
	requested := domain.Topic{WorkspaceID: command.WorkspaceID, Name: name, NormalizedName: normalized, Description: description, Aliases: aliases, Status: domain.TopicStatusActive, Version: 1}
	requestHash := hashTopicRequest(requested)
	receipt, found, err := s.lookupCommandReceipt(ctx, command.WorkspaceID, key, requestHash, domain.CommandCreateTopic, domain.AggregateTopic, "")
	if err != nil {
		return domain.TopicResult{}, err
	}
	if found {
		result, replayErr := s.replayTopic(ctx, receipt)
		if replayErr != nil {
			return domain.TopicResult{}, replayErr
		}
		if replayErr = validateTopicResult(command.WorkspaceID, requested, result); replayErr != nil {
			return domain.TopicResult{}, replayErr
		}
		return result, nil
	}
	id, now, err := s.newIdentityAndTime()
	if err != nil {
		return domain.TopicResult{}, err
	}
	topic := requested
	topic.ID = id
	topic.CreatedAt = now
	topic.UpdatedAt = now
	if err := domain.ValidateTopic(topic); err != nil {
		return domain.TopicResult{}, err
	}
	record := domain.CreateTopicRecord{Topic: topic, IdempotencyKey: key, RequestHash: requestHash}
	result, err := s.dependencies.Repository.CreateTopic(ctx, record)
	if err != nil {
		return domain.TopicResult{}, err
	}
	if err := validateTopicResult(command.WorkspaceID, topic, result); err != nil {
		return domain.TopicResult{}, err
	}
	return result, nil
}

// SuggestClaim 规范化 Claim 后执行幂等建议。
func (s *Service) SuggestClaim(ctx context.Context, command SuggestClaimCommand) (domain.ClaimResult, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return domain.ClaimResult{}, err
	}
	key, err := normalizeCreateCommandIdentity(command.WorkspaceID, command.IdempotencyKey)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	statement, normalized, err := domain.NormalizeStatement(command.Statement)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	applicability, err := domain.ParseApplicability(command.Applicability)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	factors, err := domain.NormalizeConfidenceFactors(command.ConfidenceFactors)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	requested := domain.Claim{WorkspaceID: command.WorkspaceID, Statement: statement, NormalizedStatement: normalized, Applicability: applicability, Status: domain.ClaimStatusSuggested, ConfidenceScore: cloneFloat(command.ConfidenceScore), ConfidenceFactors: factors, Version: 1}
	requested.Fingerprint = domain.ComputeClaimFingerprint(requested.WorkspaceID, requested.NormalizedStatement, requested.Applicability)
	requestHash := hashClaimRequest(requested)
	receipt, found, err := s.lookupCommandReceipt(ctx, command.WorkspaceID, key, requestHash, domain.CommandSuggestClaim, domain.AggregateClaim, "")
	if err != nil {
		return domain.ClaimResult{}, err
	}
	if found {
		result, replayErr := s.replayClaim(ctx, receipt)
		if replayErr != nil {
			return domain.ClaimResult{}, replayErr
		}
		if replayErr = validateClaimResult(command.WorkspaceID, requested, result, true); replayErr != nil {
			return domain.ClaimResult{}, replayErr
		}
		return result, nil
	}
	id, now, err := s.newIdentityAndTime()
	if err != nil {
		return domain.ClaimResult{}, err
	}
	claim := requested
	claim.ID = id
	claim.CreatedAt = now
	claim.UpdatedAt = now
	if err := domain.ValidateClaimAggregate(claim, nil); err != nil {
		return domain.ClaimResult{}, err
	}
	result, err := s.dependencies.Repository.SuggestClaim(ctx, domain.SuggestClaimRecord{Claim: claim, IdempotencyKey: key, RequestHash: requestHash})
	if err != nil {
		return domain.ClaimResult{}, err
	}
	if err := validateClaimResult(command.WorkspaceID, claim, result, result.Replayed); err != nil {
		return domain.ClaimResult{}, err
	}
	return result, nil
}

// ConfirmClaim 校验 Provenance 后原子追加来源并确认 Claim。
func (s *Service) ConfirmClaim(ctx context.Context, command ConfirmClaimCommand) (domain.ClaimResult, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return domain.ClaimResult{}, err
	}
	key, err := normalizeMutationCommandIdentity(command.WorkspaceID, command.IdempotencyKey, command.ExpectedVersion)
	if err != nil || !validID(command.ClaimID) {
		return domain.ClaimResult{}, requestError("claim id is invalid")
	}
	if command.Source.SupportType != domain.ClaimSupportSupports {
		return domain.ClaimResult{}, requestError("confirm claim requires a supporting source")
	}
	if command.Source.Provenance.WorkspaceID != command.WorkspaceID {
		return domain.ClaimResult{}, requestError("claim source workspace does not match command")
	}
	reason, modelRef, err := normalizeEvidenceText(command.Source.Reason, command.Source.ModelRunRef)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	requestedSource := domain.ClaimSource{WorkspaceID: command.WorkspaceID, ClaimID: command.ClaimID, Provenance: command.Source.Provenance, SupportType: command.Source.SupportType, Reason: reason, ModelRunRef: modelRef}
	requestRecord := domain.ConfirmClaimRecord{WorkspaceID: command.WorkspaceID, ClaimID: command.ClaimID, ExpectedVersion: command.ExpectedVersion, Source: requestedSource, IdempotencyKey: key}
	requestHash := hashConfirmClaimRequest(requestRecord)
	receipt, found, err := s.lookupCommandReceipt(ctx, command.WorkspaceID, key, requestHash, domain.CommandConfirmClaim, domain.AggregateClaim, command.ClaimID)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	if found {
		result, replayErr := s.replayClaim(ctx, receipt)
		if replayErr != nil {
			return domain.ClaimResult{}, replayErr
		}
		if replayErr = validateConfirmedClaimResult(command.WorkspaceID, command.ClaimID, command.ExpectedVersion, requestedSource, result); replayErr != nil {
			return domain.ClaimResult{}, replayErr
		}
		return result, nil
	}
	if err := s.dependencies.Provenance.Verify(ctx, command.Source.Provenance); err != nil {
		return domain.ClaimResult{}, err
	}
	id, now, err := s.newIdentityAndTime()
	if err != nil {
		return domain.ClaimResult{}, err
	}
	source := requestedSource
	source.ID = id
	source.CreatedAt = now
	record := domain.ConfirmClaimRecord{WorkspaceID: command.WorkspaceID, ClaimID: command.ClaimID, ExpectedVersion: command.ExpectedVersion, Source: source, IdempotencyKey: key, At: now}
	record.RequestHash = requestHash
	result, err := s.dependencies.Repository.ConfirmClaim(ctx, record)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	if err := validateConfirmedClaimResult(command.WorkspaceID, command.ClaimID, command.ExpectedVersion, source, result); err != nil {
		return domain.ClaimResult{}, err
	}
	return result, nil
}

// TransitionClaim 执行 Claim 状态迁移并校验返回聚合。
func (s *Service) TransitionClaim(ctx context.Context, command TransitionClaimCommand) (domain.ClaimResult, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return domain.ClaimResult{}, err
	}
	key, err := normalizeMutationCommandIdentity(command.WorkspaceID, command.IdempotencyKey, command.ExpectedVersion)
	if err != nil || !validID(command.ClaimID) || !validClaimTarget(command.Status) {
		return domain.ClaimResult{}, requestError("claim transition input is invalid")
	}
	record := domain.TransitionClaimRecord{WorkspaceID: command.WorkspaceID, ClaimID: command.ClaimID, ExpectedVersion: command.ExpectedVersion, Status: command.Status, IdempotencyKey: key}
	record.RequestHash = hashRequest("transition-claim/v1", string(record.WorkspaceID), string(record.ClaimID), strconv.FormatInt(record.ExpectedVersion, 10), string(record.Status))
	receipt, found, err := s.lookupCommandReceipt(ctx, command.WorkspaceID, key, record.RequestHash, domain.CommandTransitionClaim, domain.AggregateClaim, command.ClaimID)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	if found {
		result, replayErr := s.replayClaim(ctx, receipt)
		if replayErr != nil {
			return domain.ClaimResult{}, replayErr
		}
		if replayErr = validateChangedClaimResult(command.WorkspaceID, command.ClaimID, command.ExpectedVersion, command.Status, result); replayErr != nil {
			return domain.ClaimResult{}, replayErr
		}
		return result, nil
	}
	now, err := s.now()
	if err != nil {
		return domain.ClaimResult{}, err
	}
	record.At = now
	result, err := s.dependencies.Repository.TransitionClaim(ctx, record)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	if err := validateChangedClaimResult(command.WorkspaceID, command.ClaimID, command.ExpectedVersion, command.Status, result); err != nil {
		return domain.ClaimResult{}, err
	}
	return result, nil
}

// SuggestRelation 校验端点类型与 Provenance 后建议正式关系候选。
func (s *Service) SuggestRelation(ctx context.Context, command SuggestRelationCommand) (domain.RelationResult, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return domain.RelationResult{}, err
	}
	key, err := normalizeCreateCommandIdentity(command.WorkspaceID, command.IdempotencyKey)
	if err != nil {
		return domain.RelationResult{}, err
	}
	source, target, err := domain.CanonicalizeRelationEndpoints(command.Type, command.Source, command.Target)
	if err != nil {
		return domain.RelationResult{}, err
	}
	prepared, err := prepareRelationEvidenceInputs(command.WorkspaceID, command.Evidence)
	if err != nil {
		return domain.RelationResult{}, err
	}
	requested := domain.Relation{WorkspaceID: command.WorkspaceID, Source: source, Target: target, Type: command.Type, Status: domain.RelationStatusSuggested, ConfidenceScore: cloneFloat(command.ConfidenceScore), ValidFrom: cloneTime(command.ValidFrom), ValidTo: cloneTime(command.ValidTo), Version: 1}
	requested.Fingerprint = domain.ComputeRelationFingerprint(requested.WorkspaceID, requested.Type, requested.Source, requested.Target)
	hashEvidence := preparedEvidenceForHash(command.WorkspaceID, "", prepared, nil)
	requestHash := hashSuggestRelationRequest(requested, hashEvidence)
	receipt, found, err := s.lookupCommandReceipt(ctx, command.WorkspaceID, key, requestHash, domain.CommandSuggestRelation, domain.AggregateRelation, "")
	if err != nil {
		return domain.RelationResult{}, err
	}
	if found {
		result, replayErr := s.replayRelation(ctx, receipt)
		if replayErr != nil {
			return domain.RelationResult{}, replayErr
		}
		if replayErr = validateRelationResult(command.WorkspaceID, requested, hashEvidence, result, true); replayErr != nil {
			return domain.RelationResult{}, replayErr
		}
		return result, nil
	}
	id, now, err := s.newIdentityAndTime()
	if err != nil {
		return domain.RelationResult{}, err
	}
	relation := requested
	relation.ID = id
	relation.CreatedAt = now
	relation.UpdatedAt = now
	evidence, err := s.buildPreparedRelationEvidence(ctx, relation.ID, command.WorkspaceID, prepared, nil, now)
	if err != nil {
		return domain.RelationResult{}, err
	}
	relation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint(evidence)
	if err := domain.ValidateRelationAggregate(relation, evidence); err != nil {
		return domain.RelationResult{}, err
	}
	result, err := s.dependencies.Repository.SuggestRelation(ctx, domain.SuggestRelationRecord{Relation: relation, Evidence: evidence, IdempotencyKey: key, RequestHash: requestHash})
	if err != nil {
		return domain.RelationResult{}, err
	}
	if err := validateRelationResult(command.WorkspaceID, relation, evidence, result, result.Replayed); err != nil {
		return domain.RelationResult{}, err
	}
	return result, nil
}

// ConfirmRelation 校验 Provenance 和 Confirmation 后原子确认 Relation。
func (s *Service) ConfirmRelation(ctx context.Context, command ConfirmRelationCommand) (domain.RelationResult, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return domain.RelationResult{}, err
	}
	key, err := normalizeMutationCommandIdentity(command.WorkspaceID, command.IdempotencyKey, command.ExpectedVersion)
	if err != nil || !validID(command.RelationID) {
		return domain.RelationResult{}, requestError("relation identity is invalid")
	}
	confirmation, err := normalizeConfirmation(command.Confirmation)
	if err != nil {
		return domain.RelationResult{}, err
	}
	prepared, err := prepareRelationEvidenceInputs(command.WorkspaceID, []RelationEvidenceInput{command.Evidence})
	if err != nil {
		return domain.RelationResult{}, err
	}
	requestedEvidence := preparedEvidenceForHash(command.WorkspaceID, command.RelationID, prepared, &confirmation)[0]
	requestRecord := domain.ConfirmRelationRecord{WorkspaceID: command.WorkspaceID, RelationID: command.RelationID, ExpectedVersion: command.ExpectedVersion, Evidence: requestedEvidence, Confirmation: confirmation, IdempotencyKey: key}
	requestHash := hashConfirmRelationRequest(requestRecord)
	receipt, found, err := s.lookupCommandReceipt(ctx, command.WorkspaceID, key, requestHash, domain.CommandConfirmRelation, domain.AggregateRelation, command.RelationID)
	if err != nil {
		return domain.RelationResult{}, err
	}
	if found {
		result, replayErr := s.replayRelation(ctx, receipt)
		if replayErr != nil {
			return domain.RelationResult{}, replayErr
		}
		if replayErr = validateChangedRelationResult(command, requestedEvidence, confirmation, result); replayErr != nil {
			return domain.RelationResult{}, replayErr
		}
		return result, nil
	}
	if err := s.dependencies.Provenance.Verify(ctx, command.Evidence.Provenance); err != nil {
		return domain.RelationResult{}, err
	}
	if err := s.dependencies.Confirmation.Verify(ctx, confirmation); err != nil {
		return domain.RelationResult{}, err
	}
	id, now, err := s.newIdentityAndTime()
	if err != nil {
		return domain.RelationResult{}, err
	}
	evidence, err := buildPreparedRelationEvidenceValue(id, command.WorkspaceID, command.RelationID, prepared[0], &confirmation, now)
	if err != nil {
		return domain.RelationResult{}, err
	}
	record := domain.ConfirmRelationRecord{WorkspaceID: command.WorkspaceID, RelationID: command.RelationID, ExpectedVersion: command.ExpectedVersion, Evidence: evidence, Confirmation: confirmation, IdempotencyKey: key, At: now}
	record.RequestHash = requestHash
	result, err := s.dependencies.Repository.ConfirmRelation(ctx, record)
	if err != nil {
		return domain.RelationResult{}, err
	}
	if err := validateChangedRelationResult(command, evidence, confirmation, result); err != nil {
		return domain.RelationResult{}, err
	}
	return result, nil
}

// TransitionRelation 执行非确认 Relation 状态迁移。
func (s *Service) TransitionRelation(ctx context.Context, command TransitionRelationCommand) (domain.RelationResult, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return domain.RelationResult{}, err
	}
	key, err := normalizeMutationCommandIdentity(command.WorkspaceID, command.IdempotencyKey, command.ExpectedVersion)
	if err != nil || !validID(command.RelationID) || !validRelationTransitionTarget(command.Status) {
		return domain.RelationResult{}, requestError("relation transition input is invalid")
	}
	var prepared *preparedRelationEvidence
	var requestedEvidence *domain.RelationEvidence
	if command.Status == domain.RelationStatusSuggested {
		if command.Evidence == nil {
			return domain.RelationResult{}, requestError("resuggest relation requires new evidence")
		}
		items, prepareErr := prepareRelationEvidenceInputs(command.WorkspaceID, []RelationEvidenceInput{*command.Evidence})
		if prepareErr != nil {
			return domain.RelationResult{}, prepareErr
		}
		prepared = &items[0]
		value := preparedEvidenceForHash(command.WorkspaceID, command.RelationID, items, nil)[0]
		requestedEvidence = &value
	} else if command.Evidence != nil {
		return domain.RelationResult{}, requestError("only resuggest relation can append transition evidence")
	}
	evidenceHash := ""
	if requestedEvidence != nil {
		evidenceHash = relationEvidenceSemanticHash(*requestedEvidence)
	}
	record := domain.TransitionRelationRecord{WorkspaceID: command.WorkspaceID, RelationID: command.RelationID, ExpectedVersion: command.ExpectedVersion, Status: command.Status, IdempotencyKey: key}
	record.RequestHash = hashRequest("transition-relation/v2", string(record.WorkspaceID), string(record.RelationID), strconv.FormatInt(record.ExpectedVersion, 10), string(record.Status), evidenceHash)
	receipt, found, err := s.lookupCommandReceipt(ctx, command.WorkspaceID, key, record.RequestHash, domain.CommandTransitionRelation, domain.AggregateRelation, command.RelationID)
	if err != nil {
		return domain.RelationResult{}, err
	}
	if found {
		result, replayErr := s.replayRelation(ctx, receipt)
		if replayErr != nil {
			return domain.RelationResult{}, replayErr
		}
		if replayErr = validateTransitionedRelationResult(command, requestedEvidence, result); replayErr != nil {
			return domain.RelationResult{}, replayErr
		}
		return result, nil
	}
	if prepared != nil {
		if err := s.dependencies.Provenance.Verify(ctx, prepared.Provenance); err != nil {
			return domain.RelationResult{}, err
		}
		id, now, buildErr := s.newIdentityAndTime()
		if buildErr != nil {
			return domain.RelationResult{}, buildErr
		}
		evidence, buildErr := buildPreparedRelationEvidenceValue(id, command.WorkspaceID, command.RelationID, *prepared, nil, now)
		if buildErr != nil {
			return domain.RelationResult{}, buildErr
		}
		record.Evidence = &evidence
		record.At = now
	} else {
		now, timeErr := s.now()
		if timeErr != nil {
			return domain.RelationResult{}, timeErr
		}
		record.At = now
	}
	result, err := s.dependencies.Repository.TransitionRelation(ctx, record)
	if err != nil {
		return domain.RelationResult{}, err
	}
	if err := validateTransitionedRelationResult(command, record.Evidence, result); err != nil {
		return domain.RelationResult{}, err
	}
	return result, nil
}

// OpenConflict 原子打开至少两个成员的持续冲突。
func (s *Service) OpenConflict(ctx context.Context, command OpenConflictCommand) (domain.ConflictResult, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return domain.ConflictResult{}, err
	}
	key, err := normalizeCreateCommandIdentity(command.WorkspaceID, command.IdempotencyKey)
	if err != nil || len(command.Members) < 2 || len(command.Members) > domain.MaxBatchLimit {
		return domain.ConflictResult{}, requestError("conflict member count is invalid")
	}
	if command.TopicID != nil && !validID(*command.TopicID) {
		return domain.ConflictResult{}, requestError("conflict topic id is invalid")
	}
	summary, err := domain.NormalizeReason(command.Summary, true)
	if err != nil {
		return domain.ConflictResult{}, err
	}
	overlap, err := domain.NormalizeReason(command.ReviewedOverlapReason, command.ApplicabilityAssessment == domain.ApplicabilityAssessmentReviewedOverlap)
	if err != nil {
		return domain.ConflictResult{}, err
	}
	members := make([]domain.ConflictMember, len(command.Members))
	for i, input := range command.Members {
		if !validID(input.ClaimID) {
			return domain.ConflictResult{}, requestError("conflict claim id is invalid")
		}
		app, parseErr := domain.ParseApplicability(input.Applicability)
		if parseErr != nil {
			return domain.ConflictResult{}, parseErr
		}
		position, normalizeErr := domain.NormalizeReason(input.PositionSummary, true)
		if normalizeErr != nil {
			return domain.ConflictResult{}, normalizeErr
		}
		members[i] = domain.ConflictMember{WorkspaceID: command.WorkspaceID, ClaimID: input.ClaimID, Applicability: app, ApplicabilityHash: app.Hash, PositionSummary: position}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ClaimID < members[j].ClaimID })
	requested := domain.Conflict{WorkspaceID: command.WorkspaceID, TopicID: cloneID(command.TopicID), Status: domain.ConflictStatusOpen, Severity: command.Severity, Summary: summary, ApplicabilityAssessment: command.ApplicabilityAssessment, ReviewedOverlapReason: overlap, Version: 1}
	requested.Fingerprint = domain.ComputeConflictFingerprint(requested.WorkspaceID, requested.ApplicabilityAssessment, members)
	requestHash := hashOpenConflictRequest(requested, members)
	receipt, found, err := s.lookupCommandReceipt(ctx, command.WorkspaceID, key, requestHash, domain.CommandOpenConflict, domain.AggregateConflict, "")
	if err != nil {
		return domain.ConflictResult{}, err
	}
	if found {
		result, replayErr := s.replayConflict(ctx, receipt)
		if replayErr != nil {
			return domain.ConflictResult{}, replayErr
		}
		if replayErr = validateConflictResult(command.WorkspaceID, requested, members, result, true); replayErr != nil {
			return domain.ConflictResult{}, replayErr
		}
		return result, nil
	}
	id, now, err := s.newIdentityAndTime()
	if err != nil {
		return domain.ConflictResult{}, err
	}
	for i := range members {
		members[i].ConflictID = id
		members[i].CreatedAt = now
	}
	conflict := requested
	conflict.ID = id
	conflict.CreatedAt = now
	conflict.UpdatedAt = now
	if err := domain.ValidateConflictAggregate(conflict, members); err != nil {
		return domain.ConflictResult{}, err
	}
	result, err := s.dependencies.Repository.OpenConflict(ctx, domain.OpenConflictRecord{Conflict: conflict, Members: members, IdempotencyKey: key, RequestHash: requestHash})
	if err != nil {
		return domain.ConflictResult{}, err
	}
	if err := validateConflictResult(command.WorkspaceID, conflict, members, result, result.Replayed); err != nil {
		return domain.ConflictResult{}, err
	}
	return result, nil
}

// TransitionConflict 执行 Conflict 状态迁移并校验终结信息。
func (s *Service) TransitionConflict(ctx context.Context, command TransitionConflictCommand) (domain.ConflictResult, error) {
	if err := validateCommandContext(s, ctx); err != nil {
		return domain.ConflictResult{}, err
	}
	key, err := normalizeMutationCommandIdentity(command.WorkspaceID, command.IdempotencyKey, command.ExpectedVersion)
	if err != nil || !validID(command.ConflictID) || !validConflictTarget(command.Status) {
		return domain.ConflictResult{}, requestError("conflict transition input is invalid")
	}
	resolution, err := normalizeOptionalReason(command.Resolution)
	if err != nil {
		return domain.ConflictResult{}, err
	}
	reference, err := normalizeOptionalReference(command.ResolutionReference)
	if err != nil {
		return domain.ConflictResult{}, err
	}
	requiresResolution := command.Status == domain.ConflictStatusResolved || command.Status == domain.ConflictStatusAcceptedDivergence
	if requiresResolution != (resolution != nil && reference != nil) || (!requiresResolution && (resolution != nil || reference != nil)) {
		return domain.ConflictResult{}, requestError("conflict resolution and reference do not match target status")
	}
	record := domain.TransitionConflictRecord{WorkspaceID: command.WorkspaceID, ConflictID: command.ConflictID, ExpectedVersion: command.ExpectedVersion, Status: command.Status, Resolution: resolution, ResolutionReference: reference, IdempotencyKey: key}
	record.RequestHash = hashRequest("transition-conflict/v1", string(record.WorkspaceID), string(record.ConflictID), strconv.FormatInt(record.ExpectedVersion, 10), string(record.Status), optional(record.Resolution), optional(record.ResolutionReference))
	receipt, found, err := s.lookupCommandReceipt(ctx, command.WorkspaceID, key, record.RequestHash, domain.CommandTransitionConflict, domain.AggregateConflict, command.ConflictID)
	if err != nil {
		return domain.ConflictResult{}, err
	}
	if found {
		result, replayErr := s.replayConflict(ctx, receipt)
		if replayErr != nil {
			return domain.ConflictResult{}, replayErr
		}
		if replayErr = validateTransitionedConflictResult(command, result); replayErr != nil {
			return domain.ConflictResult{}, replayErr
		}
		return result, nil
	}
	now, err := s.now()
	if err != nil {
		return domain.ConflictResult{}, err
	}
	record.At = now
	result, err := s.dependencies.Repository.TransitionConflict(ctx, record)
	if err != nil {
		return domain.ConflictResult{}, err
	}
	if err := validateTransitionedConflictResult(command, result); err != nil {
		return domain.ConflictResult{}, err
	}
	return result, nil
}

func (s *Service) newIdentityAndTime() (foundation.ID, time.Time, error) {
	id, err := s.dependencies.IDs.New()
	if err != nil {
		return "", time.Time{}, err
	}
	if !validID(id) {
		return "", time.Time{}, consistencyError("id generator returned invalid id")
	}
	now, err := s.now()
	return id, now, err
}
func (s *Service) now() (time.Time, error) {
	now := s.dependencies.Clock.Now().UTC()
	if now.IsZero() {
		return time.Time{}, consistencyError("clock returned zero time")
	}
	return now, nil
}

type preparedRelationEvidence struct {
	Provenance    domain.ProvenanceRef
	Reason        string
	Applicability domain.Applicability
	ModelRunRef   *string
}

func prepareRelationEvidenceInputs(workspaceID foundation.ID, inputs []RelationEvidenceInput) ([]preparedRelationEvidence, error) {
	if len(inputs) > domain.MaxBatchLimit {
		return nil, requestError("relation evidence count exceeds limit")
	}
	result := make([]preparedRelationEvidence, len(inputs))
	for i, input := range inputs {
		if input.Provenance.WorkspaceID != workspaceID || domain.ValidateProvenanceRef(input.Provenance) != nil {
			return nil, requestError("relation evidence workspace does not match command")
		}
		reason, modelRef, err := normalizeEvidenceText(input.Reason, input.ModelRunRef)
		if err != nil {
			return nil, err
		}
		app, err := domain.ParseApplicability(input.Applicability)
		if err != nil {
			return nil, err
		}
		result[i] = preparedRelationEvidence{Provenance: input.Provenance, Reason: reason, Applicability: app, ModelRunRef: modelRef}
	}
	return result, nil
}

func preparedEvidenceForHash(workspaceID, relationID foundation.ID, prepared []preparedRelationEvidence, confirmation *domain.Confirmation) []domain.RelationEvidence {
	result := make([]domain.RelationEvidence, len(prepared))
	for i, item := range prepared {
		result[i] = domain.RelationEvidence{WorkspaceID: workspaceID, RelationID: relationID, Provenance: item.Provenance, Reason: item.Reason, Applicability: item.Applicability, ModelRunRef: item.ModelRunRef, Confirmation: cloneConfirmation(confirmation)}
	}
	return result
}

func (s *Service) buildPreparedRelationEvidence(ctx context.Context, relationID, workspaceID foundation.ID, prepared []preparedRelationEvidence, confirmation *domain.Confirmation, at time.Time) ([]domain.RelationEvidence, error) {
	values := make([]domain.RelationEvidence, len(prepared))
	for i, input := range prepared {
		if err := s.dependencies.Provenance.Verify(ctx, input.Provenance); err != nil {
			return nil, err
		}
		id, err := s.dependencies.IDs.New()
		if err != nil {
			return nil, err
		}
		if !validID(id) {
			return nil, consistencyError("id generator returned invalid id")
		}
		value, err := buildPreparedRelationEvidenceValue(id, workspaceID, relationID, input, confirmation, at)
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	sort.Slice(values, func(i, j int) bool { return values[i].EvidenceHash < values[j].EvidenceHash })
	return values, nil
}

func buildPreparedRelationEvidenceValue(id, workspaceID, relationID foundation.ID, input preparedRelationEvidence, confirmation *domain.Confirmation, at time.Time) (domain.RelationEvidence, error) {
	value := domain.RelationEvidence{ID: id, WorkspaceID: workspaceID, RelationID: relationID, Provenance: input.Provenance, Reason: input.Reason, Applicability: input.Applicability, ModelRunRef: input.ModelRunRef, Confirmation: cloneConfirmation(confirmation), CreatedAt: at}
	value.EvidenceHash = domain.ComputeRelationEvidenceHash(value)
	if err := domain.ValidateRelationEvidence(value); err != nil {
		return domain.RelationEvidence{}, err
	}
	return value, nil
}

func normalizeEvidenceText(reason string, modelRef *string) (string, *string, error) {
	normalized, err := domain.NormalizeReason(reason, true)
	if err != nil {
		return "", nil, err
	}
	ref, err := normalizeOptionalReference(modelRef)
	return normalized, ref, err
}
func normalizeConfirmation(value domain.Confirmation) (domain.Confirmation, error) {
	reference, err := domain.NormalizeReference(value.Reference, true)
	if err != nil {
		return domain.Confirmation{}, err
	}
	result := domain.Confirmation{Method: value.Method, Reference: reference}
	if err := domain.ValidateConfirmation(result); err != nil {
		return domain.Confirmation{}, err
	}
	return result, nil
}
func normalizeOptionalReference(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized, err := domain.NormalizeReference(*value, true)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}
func normalizeOptionalReason(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized, err := domain.NormalizeReason(*value, true)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}
func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}
func cloneID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneConfirmation(value *domain.Confirmation) *domain.Confirmation {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func optional(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
func normalizeCreateCommandIdentity(workspaceID foundation.ID, key string) (string, error) {
	return normalizeCommandIdentity(workspaceID, key, false, 0)
}
func normalizeMutationCommandIdentity(workspaceID foundation.ID, key string, expectedVersion int64) (string, error) {
	return normalizeCommandIdentity(workspaceID, key, true, expectedVersion)
}
func normalizeCommandIdentity(workspaceID foundation.ID, key string, requiresVersion bool, expectedVersion int64) (string, error) {
	if !validID(workspaceID) || (requiresVersion && expectedVersion <= 0) {
		return "", requestError("workspace or expected version is invalid")
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > MaxIdempotencyKeyBytes {
		return "", requestError("idempotency key is invalid")
	}
	return key, nil
}
func validateCommandContext(s *Service, ctx context.Context) error {
	if s == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeServiceUnavailable, false, errors.New("knowledge service is nil"))
	}
	if ctx == nil {
		return requestError("context is nil")
	}
	return nil
}
func validClaimTarget(value domain.ClaimStatus) bool {
	switch value {
	case domain.ClaimStatusConfirmed, domain.ClaimStatusDisputed, domain.ClaimStatusSuperseded, domain.ClaimStatusDeprecated, domain.ClaimStatusInvalid:
		return true
	}
	return false
}
func validRelationTransitionTarget(value domain.RelationStatus) bool {
	switch value {
	case domain.RelationStatusSuggested, domain.RelationStatusRejected, domain.RelationStatusStale, domain.RelationStatusDeprecated:
		return true
	}
	return false
}
func validConflictTarget(value domain.ConflictStatus) bool {
	switch value {
	case domain.ConflictStatusInvestigating, domain.ConflictStatusResolutionProposed, domain.ConflictStatusResolved, domain.ConflictStatusAcceptedDivergence, domain.ConflictStatusDeferred:
		return true
	}
	return false
}

func requestError(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeRequestInvalid, false, errors.New(message))
}
func consistencyError(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeResultConsistency, false, errors.New(message))
}
