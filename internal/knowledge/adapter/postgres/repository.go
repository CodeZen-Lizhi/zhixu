// Package postgres implements the Knowledge repository with explicit PostgreSQL transactions.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

const (
	commandCreateTopic        = domain.CommandCreateTopic
	commandSuggestClaim       = domain.CommandSuggestClaim
	commandConfirmClaim       = domain.CommandConfirmClaim
	commandTransitionClaim    = domain.CommandTransitionClaim
	commandSuggestRelation    = domain.CommandSuggestRelation
	commandConfirmRelation    = domain.CommandConfirmRelation
	commandTransitionRelation = domain.CommandTransitionRelation
	commandOpenConflict       = domain.CommandOpenConflict
	commandTransitionConflict = domain.CommandTransitionConflict
)

const (
	aggregateTopic    = domain.AggregateTopic
	aggregateClaim    = domain.AggregateClaim
	aggregateRelation = domain.AggregateRelation
	aggregateConflict = domain.AggregateConflict
)

// DB 是 Repository 所需的最小 pgx 事务与查询边界。
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Repository 是 domain.Repository 的 PostgreSQL 实现。
type Repository struct{ db DB }

// NewRepository 构造不向领域层泄漏 pgx 类型的 Knowledge Repository。
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeDatabaseUnavailable, true, errors.New("knowledge database is nil"))
	}
	return &Repository{db: db}, nil
}

type commandReceipt struct {
	RequestHash      string
	CommandType      domain.CommandType
	AggregateType    domain.AggregateType
	AggregateID      foundation.ID
	AggregateVersion int64
}

// LookupCommandReceipt 在外部验证或服务端值生成前读取精确匹配的已提交收据。
func (r *Repository) LookupCommandReceipt(ctx context.Context, query domain.CommandReceiptQuery) (domain.CommandReceiptLookup, error) {
	if err := domain.ValidateCommandReceiptQuery(query); err != nil {
		return domain.CommandReceiptLookup{}, err
	}
	var receipt domain.CommandReceipt
	var workspaceID, aggregateID, commandType, aggregateType string
	err := r.db.QueryRow(ctx, `
		SELECT workspace_id::text,idempotency_key,request_hash,command_type,aggregate_type,
			aggregate_id::text,aggregate_version
		FROM core.knowledge_command_receipt
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(query.WorkspaceID), query.IdempotencyKey).Scan(
		&workspaceID, &receipt.IdempotencyKey, &receipt.RequestHash, &commandType, &aggregateType,
		&aggregateID, &receipt.AggregateVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CommandReceiptLookup{}, nil
	}
	if err != nil {
		return domain.CommandReceiptLookup{}, classify(err, errorCodeDatabaseUnavailable)
	}
	receipt.WorkspaceID = foundation.ID(workspaceID)
	receipt.AggregateID = foundation.ID(aggregateID)
	receipt.CommandType = domain.CommandType(commandType)
	receipt.AggregateType = domain.AggregateType(aggregateType)
	if receipt.RequestHash != query.RequestHash || receipt.CommandType != query.CommandType || receipt.AggregateType != query.AggregateType {
		return domain.CommandReceiptLookup{}, idempotencyConflict(errors.New("idempotency key is bound to a different knowledge command"))
	}
	if err := domain.ValidateCommandReceipt(receipt); err != nil {
		return domain.CommandReceiptLookup{}, consistency(domain.ErrorCodeIdempotencyConflict, err)
	}
	return domain.CommandReceiptLookup{Found: true, Receipt: receipt}, nil
}

func (r *Repository) beginCommand(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string, commandType domain.CommandType, aggregateType domain.AggregateType) (pgx.Tx, *commandReceipt, error) {
	if err := domain.ValidateCommandMetadata(workspaceID, idempotencyKey, requestHash); err != nil {
		return nil, nil, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, nil, classify(err, errorCodeDatabaseUnavailable)
	}
	var persistedWorkspace string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR KEY SHARE`, string(workspaceID)).Scan(&persistedWorkspace); err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, notFound(errorCodeWorkspaceNotFound, err)
		}
		return nil, nil, classify(err, errorCodeDatabaseUnavailable)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2, 0))`, string(workspaceID), idempotencyKey); err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, classify(err, errorCodeDatabaseUnavailable)
	}
	receipt, err := loadReceipt(ctx, tx, workspaceID, idempotencyKey)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, classify(err, errorCodeDatabaseUnavailable)
	}
	if receipt != nil && (receipt.RequestHash != requestHash || receipt.CommandType != commandType || receipt.AggregateType != aggregateType) {
		_ = tx.Rollback(ctx)
		return nil, nil, idempotencyConflict(errors.New("idempotency key is bound to a different knowledge command"))
	}
	return tx, receipt, nil
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadReceipt(ctx context.Context, queryer queryRower, workspaceID foundation.ID, idempotencyKey string) (*commandReceipt, error) {
	var receipt commandReceipt
	var aggregateID, commandType, aggregateType string
	scanErr := queryer.QueryRow(ctx, `
		SELECT request_hash,command_type,aggregate_type,aggregate_id::text,aggregate_version
		FROM core.knowledge_command_receipt
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), idempotencyKey).Scan(
		&receipt.RequestHash, &commandType, &aggregateType, &aggregateID, &receipt.AggregateVersion,
	)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return nil, nil
	}
	if scanErr != nil {
		return nil, scanErr
	}
	receipt.AggregateID = foundation.ID(aggregateID)
	receipt.CommandType = domain.CommandType(commandType)
	receipt.AggregateType = domain.AggregateType(aggregateType)
	return &receipt, nil
}

func insertReceipt(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, idempotencyKey, requestHash string, commandType domain.CommandType, aggregateType domain.AggregateType, aggregateID foundation.ID, aggregateVersion int64, at time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO core.knowledge_command_receipt (
			workspace_id,idempotency_key,request_hash,command_type,aggregate_type,
			aggregate_id,aggregate_version,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		string(workspaceID), idempotencyKey, requestHash, string(commandType), string(aggregateType),
		string(aggregateID), aggregateVersion, at.UTC(),
	)
	return err
}

func commit(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return classify(err, errorCodeDatabaseUnavailable)
	}
	return nil
}

// CreateTopic 幂等创建 ACTIVE Topic 及其规范化别名。
func (r *Repository) CreateTopic(ctx context.Context, record domain.CreateTopicRecord) (domain.TopicResult, error) {
	if len(record.Topic.Aliases) > domain.MaxBatchLimit {
		return domain.TopicResult{}, invalidQuery(errors.New("topic alias count exceeds repository limit"))
	}
	if err := domain.ValidateTopic(record.Topic); err != nil {
		return domain.TopicResult{}, err
	}
	if record.Topic.Status != domain.TopicStatusActive || record.Topic.Version != 1 || record.Topic.MergedIntoTopicID != nil {
		return domain.TopicResult{}, consistency(domain.ErrorCodeTopicInvalid, errors.New("topic create requires an active version-one aggregate"))
	}
	tx, receipt, err := r.beginCommand(ctx, record.Topic.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandCreateTopic, aggregateTopic)
	if err != nil {
		return domain.TopicResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if receipt != nil {
		topic, loadErr := getTopic(ctx, tx, record.Topic.WorkspaceID, receipt.AggregateID)
		if loadErr != nil {
			return domain.TopicResult{}, classifyRead(loadErr, errorCodeTopicNotFound)
		}
		if err := assertReceiptAggregate(receipt, topic.ID); err != nil {
			return domain.TopicResult{}, err
		}
		if err := requireReceiptVersion(receipt, topic.Version); err != nil {
			return domain.TopicResult{}, err
		}
		if err := commit(ctx, tx); err != nil {
			return domain.TopicResult{}, err
		}
		return domain.TopicResult{Topic: topic, Replayed: true}, nil
	}
	if err := lockTopicIdentities(ctx, tx, record.Topic); err != nil {
		return domain.TopicResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO core.topic (
			id,workspace_id,name,normalized_name,description,status,merged_into_topic_id,
			version,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		string(record.Topic.ID), string(record.Topic.WorkspaceID), record.Topic.Name, record.Topic.NormalizedName,
		record.Topic.Description, string(record.Topic.Status), nil, record.Topic.Version,
		record.Topic.CreatedAt.UTC(), record.Topic.UpdatedAt.UTC(),
	); err != nil {
		return domain.TopicResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	aliases := append([]domain.TopicAlias(nil), record.Topic.Aliases...)
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].NormalizedName < aliases[j].NormalizedName })
	if len(aliases) > 0 {
		names := make([]string, len(aliases))
		normalizedNames := make([]string, len(aliases))
		for index, alias := range aliases {
			names[index] = alias.Name
			normalizedNames[index] = alias.NormalizedName
		}
		if _, err := tx.Exec(ctx, `
				INSERT INTO core.topic_alias (id,workspace_id,topic_id,alias,normalized_alias,created_at)
				SELECT gen_random_uuid(),$1,$2,input.alias,input.normalized_alias,$5
				FROM unnest($3::text[],$4::text[]) AS input(alias,normalized_alias)`,
			string(record.Topic.WorkspaceID), string(record.Topic.ID), names, normalizedNames, record.Topic.CreatedAt.UTC(),
		); err != nil {
			return domain.TopicResult{}, classify(err, errorCodeDatabaseUnavailable)
		}
	}
	if err := insertReceipt(ctx, tx, record.Topic.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandCreateTopic, aggregateTopic, record.Topic.ID, record.Topic.Version, record.Topic.CreatedAt); err != nil {
		return domain.TopicResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	persisted, err := getTopic(ctx, tx, record.Topic.WorkspaceID, record.Topic.ID)
	if err != nil {
		return domain.TopicResult{}, classifyRead(err, errorCodeTopicNotFound)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.TopicResult{}, err
	}
	return domain.TopicResult{Topic: persisted}, nil
}

func lockTopicIdentities(ctx context.Context, tx pgx.Tx, topic domain.Topic) error {
	identities := make([]string, 0, len(topic.Aliases)+1)
	identities = append(identities, topic.NormalizedName)
	for _, alias := range topic.Aliases {
		identities = append(identities, alias.NormalizedName)
	}
	sort.Strings(identities)
	for _, identity := range identities {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2, 0))`, string(topic.WorkspaceID), identity); err != nil {
			return classify(err, errorCodeDatabaseUnavailable)
		}
	}
	return nil
}

// GetTopic 在指定 Workspace 中读取 Topic 及全部别名。
func (r *Repository) GetTopic(ctx context.Context, workspaceID, topicID foundation.ID) (domain.Topic, error) {
	if err := domain.ValidateBatchQuery(workspaceID, []foundation.ID{topicID}, 1); err != nil {
		return domain.Topic{}, err
	}
	topic, err := getTopic(ctx, r.db, workspaceID, topicID)
	if err != nil {
		return domain.Topic{}, classifyRead(err, errorCodeTopicNotFound)
	}
	return topic, nil
}

// SuggestClaim 幂等创建 Suggested Claim；相同 fingerprint 的等价命令复用同一正式事实。
func (r *Repository) SuggestClaim(ctx context.Context, record domain.SuggestClaimRecord) (domain.ClaimResult, error) {
	if err := domain.ValidateClaimAggregate(record.Claim, nil); err != nil {
		return domain.ClaimResult{}, err
	}
	if record.Claim.Status != domain.ClaimStatusSuggested || record.Claim.Version != 1 {
		return domain.ClaimResult{}, consistency(domain.ErrorCodeClaimInvalid, errors.New("claim suggestion requires a suggested version-one aggregate"))
	}
	tx, receipt, err := r.beginCommand(ctx, record.Claim.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandSuggestClaim, aggregateClaim)
	if err != nil {
		return domain.ClaimResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if receipt != nil {
		result, loadErr := loadClaimResult(ctx, tx, record.Claim.WorkspaceID, receipt.AggregateID)
		if loadErr != nil {
			return domain.ClaimResult{}, classifyRead(loadErr, errorCodeClaimNotFound)
		}
		if err := assertReceiptAggregate(receipt, result.Claim.ID); err != nil {
			return domain.ClaimResult{}, err
		}
		result.Replayed = true
		if err := commit(ctx, tx); err != nil {
			return domain.ClaimResult{}, err
		}
		return result, nil
	}
	inserted, err := scanClaim(tx.QueryRow(ctx, `
		INSERT INTO core.claim (
			id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,
			applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10::jsonb,$11,$12,$13,$14)
		ON CONFLICT (workspace_id,fingerprint) DO NOTHING
		RETURNING id::text,workspace_id::text,statement,normalized_statement,applicability,
			applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,
			fingerprint,version,created_at,updated_at`,
		string(record.Claim.ID), string(record.Claim.WorkspaceID), record.Claim.Statement, record.Claim.NormalizedStatement,
		[]byte(record.Claim.Applicability.CanonicalJSON), record.Claim.Applicability.SchemaVersion, record.Claim.Applicability.Hash,
		string(record.Claim.Status), record.Claim.ConfidenceScore, []byte(record.Claim.ConfidenceFactors), record.Claim.Fingerprint,
		record.Claim.Version, record.Claim.CreatedAt.UTC(), record.Claim.UpdatedAt.UTC(),
	))
	replayed := false
	if errors.Is(err, pgx.ErrNoRows) {
		inserted, err = scanClaim(tx.QueryRow(ctx, `
			SELECT id::text,workspace_id::text,statement,normalized_statement,applicability,
				applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,
				fingerprint,version,created_at,updated_at
			FROM core.claim WHERE workspace_id=$1 AND fingerprint=$2 FOR UPDATE`,
			string(record.Claim.WorkspaceID), record.Claim.Fingerprint,
		))
		replayed = true
	}
	if err != nil {
		return domain.ClaimResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if replayed && !equivalentSuggestedClaim(inserted, record.Claim) {
		return domain.ClaimResult{}, versionConflict(errors.New("claim fingerprint is already bound to a different payload"))
	}
	result, err := loadClaimResult(ctx, tx, record.Claim.WorkspaceID, inserted.ID)
	if err != nil {
		return domain.ClaimResult{}, classifyRead(err, errorCodeClaimNotFound)
	}
	if err := insertReceipt(ctx, tx, record.Claim.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandSuggestClaim, aggregateClaim, result.Claim.ID, result.Claim.Version, record.Claim.CreatedAt); err != nil {
		return domain.ClaimResult{}, classify(err, errorCodeDatabaseUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.ClaimResult{}, err
	}
	result.Replayed = replayed
	return result, nil
}

func equivalentSuggestedClaim(left, right domain.Claim) bool {
	return left.WorkspaceID == right.WorkspaceID && left.Statement == right.Statement &&
		left.NormalizedStatement == right.NormalizedStatement && left.Applicability.Hash == right.Applicability.Hash &&
		equalFloatPointers(left.ConfidenceScore, right.ConfidenceScore) &&
		string(left.ConfidenceFactors) == string(right.ConfidenceFactors)
}

func equalFloatPointers(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func classifyRead(err error, notFoundCode string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(notFoundCode, err)
	}
	return classify(err, errorCodeDatabaseUnavailable)
}

func idsAsStrings(ids []foundation.ID) []string {
	result := make([]string, len(ids))
	for index := range ids {
		result[index] = string(ids[index])
	}
	return result
}

func statusStrings[T ~string](statuses []T) []string {
	result := make([]string, len(statuses))
	for index := range statuses {
		result[index] = string(statuses[index])
	}
	return result
}

func timePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func pointerString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func assertReceiptAggregate(receipt *commandReceipt, aggregateID foundation.ID) error {
	if receipt.AggregateID != aggregateID {
		return consistency(errorCodeStorageConsistency, fmt.Errorf("knowledge receipt aggregate mismatch: %s", aggregateID))
	}
	return nil
}

var _ domain.Repository = (*Repository)(nil)
