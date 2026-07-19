package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// MaxBatchLimit 是 Knowledge 批量查询允许的最大聚合数。
const MaxBatchLimit = 500

// CommandType 是 Knowledge 幂等收据绑定的稳定命令类别。
type CommandType string

const (
	CommandCreateTopic        CommandType = "topic.create"
	CommandSuggestClaim       CommandType = "claim.suggest"
	CommandConfirmClaim       CommandType = "claim.confirm"
	CommandTransitionClaim    CommandType = "claim.transition"
	CommandSuggestRelation    CommandType = "relation.suggest"
	CommandConfirmRelation    CommandType = "relation.confirm"
	CommandTransitionRelation CommandType = "relation.transition"
	CommandOpenConflict       CommandType = "conflict.open"
	CommandTransitionConflict CommandType = "conflict.transition"
)

// AggregateType 是 Knowledge 收据指向的聚合类别。
type AggregateType string

const (
	AggregateTopic    AggregateType = "TOPIC"
	AggregateClaim    AggregateType = "CLAIM"
	AggregateRelation AggregateType = "RELATION"
	AggregateConflict AggregateType = "CONFLICT"
)

// CommandReceiptQuery 在执行外部验证或生成服务端值前查询已提交命令。
type CommandReceiptQuery struct {
	WorkspaceID    foundation.ID
	IdempotencyKey string
	RequestHash    string
	CommandType    CommandType
	AggregateType  AggregateType
}

// CommandReceipt 是不可变幂等收据的稳定领域投影。
type CommandReceipt struct {
	WorkspaceID, AggregateID    foundation.ID
	IdempotencyKey, RequestHash string
	CommandType                 CommandType
	AggregateType               AggregateType
	AggregateVersion            int64
}

// CommandReceiptLookup 区分收据不存在与已匹配的已提交命令。
type CommandReceiptLookup struct {
	Found   bool
	Receipt CommandReceipt
}

// CreateTopicRecord 是创建 Topic 的持久化命令载荷。
type CreateTopicRecord struct {
	Topic                       Topic
	IdempotencyKey, RequestHash string
}

// SuggestClaimRecord 是创建 Suggested Claim 的持久化命令载荷。
type SuggestClaimRecord struct {
	Claim                       Claim
	IdempotencyKey, RequestHash string
}

// ConfirmClaimRecord 原子追加 SUPPORTS Claim Source 并确认 Claim。
type ConfirmClaimRecord struct {
	WorkspaceID, ClaimID        foundation.ID
	ExpectedVersion             int64
	Source                      ClaimSource
	IdempotencyKey, RequestHash string
	At                          time.Time
}

// TransitionClaimRecord 是 Claim 的带乐观锁状态迁移命令。
type TransitionClaimRecord struct {
	WorkspaceID, ClaimID        foundation.ID
	ExpectedVersion             int64
	Status                      ClaimStatus
	IdempotencyKey, RequestHash string
	At                          time.Time
}

// SuggestRelationRecord 创建规范化 Suggested Relation 及其可选 Evidence。
type SuggestRelationRecord struct {
	Relation                    Relation
	Evidence                    []RelationEvidence
	IdempotencyKey, RequestHash string
}

// ConfirmRelationRecord 原子追加 Evidence、确认信息并确认 Relation。
type ConfirmRelationRecord struct {
	WorkspaceID, RelationID     foundation.ID
	ExpectedVersion             int64
	Evidence                    RelationEvidence
	Confirmation                Confirmation
	IdempotencyKey, RequestHash string
	At                          time.Time
}

// TransitionRelationRecord 是 Relation 的带乐观锁状态迁移命令。
type TransitionRelationRecord struct {
	WorkspaceID, RelationID     foundation.ID
	ExpectedVersion             int64
	Status                      RelationStatus
	Evidence                    *RelationEvidence
	IdempotencyKey, RequestHash string
	At                          time.Time
}

// OpenConflictRecord 原子创建 Conflict、全部成员并争议相关 Confirmed Claims。
type OpenConflictRecord struct {
	Conflict                    Conflict
	Members                     []ConflictMember
	IdempotencyKey, RequestHash string
}

// TransitionConflictRecord 是 Conflict 的带乐观锁状态迁移命令。
type TransitionConflictRecord struct {
	WorkspaceID, ConflictID     foundation.ID
	ExpectedVersion             int64
	Status                      ConflictStatus
	Resolution                  *string
	ResolutionReference         *string
	IdempotencyKey, RequestHash string
	At                          time.Time
}

// TopicResult 返回 Topic 写命令的持久事实和重放标志。
type TopicResult struct {
	Topic    Topic
	Replayed bool
}

// ClaimResult 返回 Claim 及单批加载的来源和重放标志。
type ClaimResult struct {
	Claim    Claim
	Sources  []ClaimSource
	Replayed bool
}

// RelationResult 返回 Relation 及单批加载的 Evidence 和重放标志。
type RelationResult struct {
	Relation Relation
	Evidence []RelationEvidence
	Replayed bool
}

// ConflictResult 返回 Conflict 及全部成员和重放标志。
type ConflictResult struct {
	Conflict Conflict
	Members  []ConflictMember
	Replayed bool
}

// ClaimWithSources 是批量查询中的完整 Claim read model。
type ClaimWithSources struct {
	Claim   Claim
	Sources []ClaimSource
}

// RelationWithEvidence 是批量查询中的完整 Relation read model。
type RelationWithEvidence struct {
	Relation Relation
	Evidence []RelationEvidence
}

// ConflictWithMembers 是批量查询中的完整 Conflict read model。
type ConflictWithMembers struct {
	Conflict Conflict
	Members  []ConflictMember
}

// BatchGetClaimsQuery 是有界、Workspace-scoped Claim 批量查询。
type BatchGetClaimsQuery struct {
	WorkspaceID foundation.ID
	IDs         []foundation.ID
	Statuses    []ClaimStatus
	Limit       int
}

// BatchGetRelationsQuery 是有界、Workspace-scoped Relation 批量查询。
type BatchGetRelationsQuery struct {
	WorkspaceID foundation.ID
	IDs         []foundation.ID
	Statuses    []RelationStatus
	Limit       int
}

// BatchGetConflictsQuery 是有界、Workspace-scoped Conflict 批量查询。
type BatchGetConflictsQuery struct {
	WorkspaceID foundation.ID
	IDs         []foundation.ID
	Statuses    []ConflictStatus
	Limit       int
}

// Repository 是 Knowledge 聚合唯一的事务持久化边界。
type Repository interface {
	// LookupCommandReceipt 在任何外部副作用前查找精确匹配的 Workspace 幂等收据；同键不同绑定必须返回幂等冲突。
	LookupCommandReceipt(context.Context, CommandReceiptQuery) (CommandReceiptLookup, error)
	// CreateTopic 幂等创建一个 ACTIVE Topic。
	CreateTopic(context.Context, CreateTopicRecord) (TopicResult, error)
	// GetTopic 在指定 Workspace 内读取一个 Topic。
	GetTopic(context.Context, foundation.ID, foundation.ID) (Topic, error)
	// SuggestClaim 幂等创建一个 Suggested Claim。
	SuggestClaim(context.Context, SuggestClaimRecord) (ClaimResult, error)
	// ConfirmClaim 原子追加来源并以 CAS 确认 Claim。
	ConfirmClaim(context.Context, ConfirmClaimRecord) (ClaimResult, error)
	// TransitionClaim 以 CAS 执行 Claim 合法状态迁移。
	TransitionClaim(context.Context, TransitionClaimRecord) (ClaimResult, error)
	// SuggestRelation 幂等创建规范化 Suggested Relation。
	SuggestRelation(context.Context, SuggestRelationRecord) (RelationResult, error)
	// ConfirmRelation 原子追加 Evidence/Confirmation 并以 CAS 确认 Relation。
	ConfirmRelation(context.Context, ConfirmRelationRecord) (RelationResult, error)
	// TransitionRelation 以 CAS 执行 Relation 合法状态迁移。
	TransitionRelation(context.Context, TransitionRelationRecord) (RelationResult, error)
	// OpenConflict 原子创建 Conflict、成员并更新可争议 Claim。
	OpenConflict(context.Context, OpenConflictRecord) (ConflictResult, error)
	// TransitionConflict 以 CAS 执行 Conflict 合法状态迁移。
	TransitionConflict(context.Context, TransitionConflictRecord) (ConflictResult, error)
	// BatchGetClaims 单批返回有界 Claim 与来源，禁止逐 Claim 查库。
	BatchGetClaims(context.Context, BatchGetClaimsQuery) ([]ClaimWithSources, error)
	// BatchGetRelations 单批返回有界 Relation 与 Evidence，禁止 N+1。
	BatchGetRelations(context.Context, BatchGetRelationsQuery) ([]RelationWithEvidence, error)
	// BatchGetConflicts 单批返回有界 Conflict 与成员，禁止 N+1。
	BatchGetConflicts(context.Context, BatchGetConflictsQuery) ([]ConflictWithMembers, error)
}

// ComputeRequestHash 计算版本化命令载荷的稳定 SHA-256；payload 应先由领域规范化。
func ComputeRequestHash(commandType string, payload any) (string, error) {
	if !validCommandType(commandType) {
		return "", invalid(ErrorCodeRequestInvalid, "command type is invalid")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", invalid(ErrorCodeRequestInvalid, "command payload cannot be encoded")
	}
	digest := sha256.Sum256(append([]byte("knowledge-command/v1\n"+commandType+"\n"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

// ValidateCommandMetadata 校验 Workspace、幂等键和 request hash 的公共写入边界。
func ValidateCommandMetadata(workspaceID foundation.ID, idempotencyKey, requestHash string) error {
	if !validID(workspaceID) || !validIdempotencyKey(idempotencyKey) || !validSHA256(requestHash) {
		return invalid(ErrorCodeRequestInvalid, "command metadata is invalid")
	}
	return nil
}

// ValidateCommandReceiptQuery 校验 receipt-first 查询的完整稳定绑定。
func ValidateCommandReceiptQuery(query CommandReceiptQuery) error {
	if err := ValidateCommandMetadata(query.WorkspaceID, query.IdempotencyKey, query.RequestHash); err != nil {
		return err
	}
	if !validKnowledgeCommandType(query.CommandType) || !validAggregateType(query.AggregateType) {
		return invalid(ErrorCodeRequestInvalid, "command receipt query binding is invalid")
	}
	return nil
}

// ValidateCommandReceipt 校验持久收据未越过 Workspace、命令、聚合和版本边界。
func ValidateCommandReceipt(receipt CommandReceipt) error {
	if err := ValidateCommandReceiptQuery(CommandReceiptQuery{WorkspaceID: receipt.WorkspaceID, IdempotencyKey: receipt.IdempotencyKey, RequestHash: receipt.RequestHash, CommandType: receipt.CommandType, AggregateType: receipt.AggregateType}); err != nil {
		return err
	}
	if !validID(receipt.AggregateID) || receipt.AggregateVersion <= 0 {
		return inconsistent(ErrorCodeIdempotencyConflict, "command receipt aggregate binding is inconsistent")
	}
	return nil
}

// ValidateBatchQuery 校验所有批量查询共享的身份、数量和 limit 边界。
func ValidateBatchQuery(workspaceID foundation.ID, ids []foundation.ID, limit int) error {
	if !validID(workspaceID) || limit <= 0 || limit > MaxBatchLimit || len(ids) > MaxBatchLimit {
		return invalid(ErrorCodeRequestInvalid, "batch query scope or limit is invalid")
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !validID(id) {
			return invalid(ErrorCodeRequestInvalid, "batch query contains invalid identity")
		}
		if _, duplicate := seen[id]; duplicate {
			return invalid(ErrorCodeRequestInvalid, "batch query contains duplicate identity")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validIdempotencyKey(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for index, r := range value {
		if unicode.IsControl(r) || (unicode.IsSpace(r) && (index == 0 || index+utf8.RuneLen(r) == len(value))) {
			return false
		}
	}
	return true
}

func validCommandType(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' {
			return false
		}
	}
	return true
}

func validKnowledgeCommandType(value CommandType) bool {
	switch value {
	case CommandCreateTopic, CommandSuggestClaim, CommandConfirmClaim, CommandTransitionClaim, CommandSuggestRelation, CommandConfirmRelation, CommandTransitionRelation, CommandOpenConflict, CommandTransitionConflict:
		return true
	default:
		return false
	}
}

func validAggregateType(value AggregateType) bool {
	return value == AggregateTopic || value == AggregateClaim || value == AggregateRelation || value == AggregateConflict
}
