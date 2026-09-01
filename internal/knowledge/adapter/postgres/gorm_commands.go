package postgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

type gormKnowledgeReceipt struct {
	RequestHash      string
	CommandType      domain.CommandType
	AggregateType    domain.AggregateType
	AggregateID      foundation.ID
	AggregateVersion int64
}

func (repository *GORMRepository) LookupCommandReceipt(ctx context.Context, query domain.CommandReceiptQuery) (domain.CommandReceiptLookup, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.CommandReceiptLookup{}, err
	}
	if err := domain.ValidateCommandReceiptQuery(query); err != nil {
		return domain.CommandReceiptLookup{}, err
	}
	row, err := gormKnowledgeRawRow(ctx, repository.database, `
		SELECT workspace_id::text,idempotency_key,request_hash,command_type,aggregate_type,
			aggregate_id::text,aggregate_version
		FROM core.knowledge_command_receipt
		WHERE workspace_id=? AND idempotency_key=?`, string(query.WorkspaceID), query.IdempotencyKey)
	if err != nil {
		return domain.CommandReceiptLookup{}, classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
	}
	var receipt domain.CommandReceipt
	var workspaceID, aggregateID, commandType, aggregateType string
	err = row.Scan(&workspaceID, &receipt.IdempotencyKey, &receipt.RequestHash, &commandType, &aggregateType, &aggregateID, &receipt.AggregateVersion)
	if gormKnowledgeNoRows(err) {
		return domain.CommandReceiptLookup{}, nil
	}
	if err != nil {
		return domain.CommandReceiptLookup{}, classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
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

func (repository *GORMRepository) gormCommand(
	ctx context.Context,
	workspaceID foundation.ID,
	idempotencyKey, requestHash string,
	commandType domain.CommandType,
	aggregateType domain.AggregateType,
	work func(context.Context, *gorm.DB, *gormKnowledgeReceipt) error,
) error {
	if err := domain.ValidateCommandMetadata(workspaceID, idempotencyKey, requestHash); err != nil {
		return err
	}
	return repository.within(ctx, foundation.TransactionOptions{}, errorCodeDatabaseUnavailable, errorCodeDatabaseUnavailable, classifyGORMKnowledge,
		func(callbackCtx context.Context, _ foundation.TransactionScope, transaction *gorm.DB) error {
			row, err := gormKnowledgeRawRow(callbackCtx, transaction, `SELECT id::text FROM core.workspace WHERE id=? FOR KEY SHARE`, string(workspaceID))
			if err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			var persistedWorkspace string
			if err := row.Scan(&persistedWorkspace); err != nil {
				if gormKnowledgeNoRows(err) {
					return notFound(errorCodeWorkspaceNotFound, err)
				}
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			if _, err := gormKnowledgeExec(callbackCtx, transaction, `SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ?, 0))`, string(workspaceID), idempotencyKey); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			receipt, err := gormKnowledgeLoadReceipt(callbackCtx, transaction, workspaceID, idempotencyKey)
			if err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			if receipt != nil && (receipt.RequestHash != requestHash || receipt.CommandType != commandType || receipt.AggregateType != aggregateType) {
				return idempotencyConflict(errors.New("idempotency key is bound to a different knowledge command"))
			}
			return work(callbackCtx, transaction, receipt)
		})
}

func gormKnowledgeLoadReceipt(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, idempotencyKey string) (*gormKnowledgeReceipt, error) {
	row, err := gormKnowledgeRawRow(ctx, database, `
		SELECT request_hash,command_type,aggregate_type,aggregate_id::text,aggregate_version
		FROM core.knowledge_command_receipt
		WHERE workspace_id=? AND idempotency_key=?`, string(workspaceID), idempotencyKey)
	if err != nil {
		return nil, err
	}
	var receipt gormKnowledgeReceipt
	var aggregateID, commandType, aggregateType string
	err = row.Scan(&receipt.RequestHash, &commandType, &aggregateType, &aggregateID, &receipt.AggregateVersion)
	if gormKnowledgeNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	receipt.AggregateID = foundation.ID(aggregateID)
	receipt.CommandType = domain.CommandType(commandType)
	receipt.AggregateType = domain.AggregateType(aggregateType)
	return &receipt, nil
}

func gormKnowledgeInsertReceipt(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, idempotencyKey, requestHash string, commandType domain.CommandType, aggregateType domain.AggregateType, aggregateID foundation.ID, aggregateVersion int64, at time.Time) error {
	_, err := gormKnowledgeExec(ctx, database, `
		INSERT INTO core.knowledge_command_receipt (
			workspace_id,idempotency_key,request_hash,command_type,aggregate_type,
			aggregate_id,aggregate_version,created_at
		) VALUES (?,?,?,?,?,?,?,?)`,
		string(workspaceID), idempotencyKey, requestHash, string(commandType), string(aggregateType), string(aggregateID), aggregateVersion, at.UTC())
	return err
}

func (repository *GORMRepository) CreateTopic(ctx context.Context, record domain.CreateTopicRecord) (domain.TopicResult, error) {
	if len(record.Topic.Aliases) > domain.MaxBatchLimit {
		return domain.TopicResult{}, invalidQuery(errors.New("topic alias count exceeds repository limit"))
	}
	if err := domain.ValidateTopic(record.Topic); err != nil {
		return domain.TopicResult{}, err
	}
	if record.Topic.Status != domain.TopicStatusActive || record.Topic.Version != 1 || record.Topic.MergedIntoTopicID != nil {
		return domain.TopicResult{}, consistency(domain.ErrorCodeTopicInvalid, errors.New("topic create requires an active version-one aggregate"))
	}
	var result domain.TopicResult
	err := repository.gormCommand(ctx, record.Topic.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandCreateTopic, aggregateTopic,
		func(callbackCtx context.Context, transaction *gorm.DB, receipt *gormKnowledgeReceipt) error {
			if receipt != nil {
				topic, err := gormKnowledgeGetTopic(callbackCtx, transaction, record.Topic.WorkspaceID, receipt.AggregateID)
				if err != nil {
					return gormKnowledgeReadError(callbackCtx, err, errorCodeTopicNotFound)
				}
				if err := gormKnowledgeAssertReceipt(receipt, topic.ID, topic.Version, true); err != nil {
					return err
				}
				result = domain.TopicResult{Topic: topic, Replayed: true}
				return nil
			}
			if err := gormKnowledgeLockTopicIdentities(callbackCtx, transaction, record.Topic); err != nil {
				return err
			}
			if _, err := gormKnowledgeExec(callbackCtx, transaction, `
				INSERT INTO core.topic (id,workspace_id,name,normalized_name,description,status,merged_into_topic_id,version,created_at,updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?)`, string(record.Topic.ID), string(record.Topic.WorkspaceID), record.Topic.Name, record.Topic.NormalizedName, record.Topic.Description, string(record.Topic.Status), nil, record.Topic.Version, record.Topic.CreatedAt.UTC(), record.Topic.UpdatedAt.UTC()); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			aliases := append([]domain.TopicAlias(nil), record.Topic.Aliases...)
			sort.Slice(aliases, func(i, j int) bool { return aliases[i].NormalizedName < aliases[j].NormalizedName })
			if len(aliases) > 0 {
				names, normalizedNames := make([]string, len(aliases)), make([]string, len(aliases))
				for index, alias := range aliases {
					names[index], normalizedNames[index] = alias.Name, alias.NormalizedName
				}
				if _, err := gormKnowledgeExec(callbackCtx, transaction, `
					INSERT INTO core.topic_alias (id,workspace_id,topic_id,alias,normalized_alias,created_at)
					SELECT gen_random_uuid(),?,?,input.alias,input.normalized_alias,?
					FROM unnest(?::text[],?::text[]) AS input(alias,normalized_alias)`, string(record.Topic.WorkspaceID), string(record.Topic.ID), record.Topic.CreatedAt.UTC(), pq.Array(names), pq.Array(normalizedNames)); err != nil {
					return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
				}
			}
			if err := gormKnowledgeInsertReceipt(callbackCtx, transaction, record.Topic.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandCreateTopic, aggregateTopic, record.Topic.ID, record.Topic.Version, record.Topic.CreatedAt); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			topic, err := gormKnowledgeGetTopic(callbackCtx, transaction, record.Topic.WorkspaceID, record.Topic.ID)
			if err != nil {
				return gormKnowledgeReadError(callbackCtx, err, errorCodeTopicNotFound)
			}
			result = domain.TopicResult{Topic: topic}
			return nil
		})
	if err != nil {
		return domain.TopicResult{}, err
	}
	return result, nil
}

func (repository *GORMRepository) GetTopic(ctx context.Context, workspaceID, topicID foundation.ID) (domain.Topic, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Topic{}, err
	}
	if err := domain.ValidateBatchQuery(workspaceID, []foundation.ID{topicID}, 1); err != nil {
		return domain.Topic{}, err
	}
	topic, err := gormKnowledgeGetTopic(ctx, repository.database, workspaceID, topicID)
	if err != nil {
		return domain.Topic{}, gormKnowledgeReadError(ctx, err, errorCodeTopicNotFound)
	}
	return topic, nil
}

func (repository *GORMRepository) SuggestClaim(ctx context.Context, record domain.SuggestClaimRecord) (domain.ClaimResult, error) {
	if err := domain.ValidateClaimAggregate(record.Claim, nil); err != nil {
		return domain.ClaimResult{}, err
	}
	if record.Claim.Status != domain.ClaimStatusSuggested || record.Claim.Version != 1 {
		return domain.ClaimResult{}, consistency(domain.ErrorCodeClaimInvalid, errors.New("claim suggestion requires a suggested version-one aggregate"))
	}
	var result domain.ClaimResult
	err := repository.gormCommand(ctx, record.Claim.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandSuggestClaim, aggregateClaim,
		func(callbackCtx context.Context, transaction *gorm.DB, receipt *gormKnowledgeReceipt) error {
			if receipt != nil {
				loaded, err := gormKnowledgeLoadClaimResult(callbackCtx, transaction, record.Claim.WorkspaceID, receipt.AggregateID)
				if err != nil {
					return gormKnowledgeReadError(callbackCtx, err, errorCodeClaimNotFound)
				}
				if err := gormKnowledgeAssertReceipt(receipt, loaded.Claim.ID, loaded.Claim.Version, false); err != nil {
					return err
				}
				loaded.Replayed = true
				result = loaded
				return nil
			}
			inserted, err := gormKnowledgeScanClaim(callbackCtx, transaction, `
				INSERT INTO core.claim (id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at)
				VALUES (?,?,?,?,?::jsonb,?,?,?,? ,?::jsonb,?,?,?,?)
				ON CONFLICT (workspace_id,fingerprint) DO NOTHING
				RETURNING id::text,workspace_id::text,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at`,
				string(record.Claim.ID), string(record.Claim.WorkspaceID), record.Claim.Statement, record.Claim.NormalizedStatement, knowledgeJSONB(record.Claim.Applicability.CanonicalJSON), record.Claim.Applicability.SchemaVersion, record.Claim.Applicability.Hash, string(record.Claim.Status), record.Claim.ConfidenceScore, knowledgeJSONB(record.Claim.ConfidenceFactors), record.Claim.Fingerprint, record.Claim.Version, record.Claim.CreatedAt.UTC(), record.Claim.UpdatedAt.UTC())
			replayed := false
			if gormKnowledgeNoRows(err) {
				inserted, err = gormKnowledgeScanClaim(callbackCtx, transaction, claimSelect+` WHERE workspace_id=? AND fingerprint=? FOR UPDATE`, string(record.Claim.WorkspaceID), record.Claim.Fingerprint)
				replayed = true
			}
			if err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			if replayed && !equivalentSuggestedClaim(inserted, record.Claim) {
				return versionConflict(errors.New("claim fingerprint is already bound to a different payload"))
			}
			loaded, err := gormKnowledgeLoadClaimResult(callbackCtx, transaction, record.Claim.WorkspaceID, inserted.ID)
			if err != nil {
				return gormKnowledgeReadError(callbackCtx, err, errorCodeClaimNotFound)
			}
			if err := gormKnowledgeInsertReceipt(callbackCtx, transaction, record.Claim.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandSuggestClaim, aggregateClaim, loaded.Claim.ID, loaded.Claim.Version, record.Claim.CreatedAt); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			loaded.Replayed = replayed
			result = loaded
			return nil
		})
	if err != nil {
		return domain.ClaimResult{}, err
	}
	return result, nil
}

func (repository *GORMRepository) ConfirmClaim(ctx context.Context, record domain.ConfirmClaimRecord) (domain.ClaimResult, error) {
	var result domain.ClaimResult
	err := repository.gormCommand(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandConfirmClaim, aggregateClaim,
		func(callbackCtx context.Context, transaction *gorm.DB, receipt *gormKnowledgeReceipt) error {
			if receipt != nil {
				loaded, err := gormKnowledgeLoadClaimResult(callbackCtx, transaction, record.WorkspaceID, receipt.AggregateID)
				if err != nil {
					return gormKnowledgeReadError(callbackCtx, err, errorCodeClaimNotFound)
				}
				if err := gormKnowledgeAssertReceipt(receipt, loaded.Claim.ID, loaded.Claim.Version, true); err != nil {
					return err
				}
				loaded.Replayed = true
				result = loaded
				return nil
			}
			current, sources, err := gormKnowledgeLockClaimAggregate(callbackCtx, transaction, record.WorkspaceID, record.ClaimID)
			if err != nil {
				return gormKnowledgeReadError(callbackCtx, err, errorCodeClaimNotFound)
			}
			if current.Version != record.ExpectedVersion {
				return versionConflict(errors.New("claim expected version is stale"))
			}
			if err := domain.ValidateClaimTransition(current.Status, domain.ClaimStatusConfirmed); err != nil {
				return err
			}
			source := record.Source
			if source.WorkspaceID != record.WorkspaceID || source.ClaimID != record.ClaimID || source.SupportType != domain.ClaimSupportSupports {
				return consistency(domain.ErrorCodeClaimSourceInvalid, errors.New("claim confirmation source binding is invalid"))
			}
			if source.EvidenceHash == "" {
				source.EvidenceHash = domain.ComputeClaimSourceEvidenceHash(source, current.Applicability)
			}
			if err := domain.ValidateClaimSource(source, current.Applicability); err != nil {
				return err
			}
			if err := gormKnowledgeInsertClaimSource(callbackCtx, transaction, source); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			updated, err := gormKnowledgeScanClaim(callbackCtx, transaction, `
				UPDATE core.claim SET status=?,version=version+1,updated_at=?
				WHERE workspace_id=? AND id=? AND version=?
				RETURNING id::text,workspace_id::text,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at`, string(domain.ClaimStatusConfirmed), record.At.UTC(), string(record.WorkspaceID), string(record.ClaimID), record.ExpectedVersion)
			if gormKnowledgeNoRows(err) {
				return versionConflict(err)
			}
			if err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			sources = append(sources, source)
			if err := domain.ValidateClaimAggregate(updated, sources); err != nil {
				return consistency(domain.ErrorCodeClaimInvalid, err)
			}
			if err := gormKnowledgeInsertReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandConfirmClaim, aggregateClaim, updated.ID, updated.Version, record.At); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			loaded, err := gormKnowledgeLoadClaimResult(callbackCtx, transaction, record.WorkspaceID, record.ClaimID)
			if err != nil {
				return gormKnowledgeReadError(callbackCtx, err, errorCodeClaimNotFound)
			}
			result = loaded
			return nil
		})
	if err != nil {
		return domain.ClaimResult{}, err
	}
	return result, nil
}

func (repository *GORMRepository) TransitionClaim(ctx context.Context, record domain.TransitionClaimRecord) (domain.ClaimResult, error) {
	var result domain.ClaimResult
	err := repository.gormCommand(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionClaim, aggregateClaim,
		func(callbackCtx context.Context, transaction *gorm.DB, receipt *gormKnowledgeReceipt) error {
			if receipt != nil {
				loaded, err := gormKnowledgeLoadClaimResult(callbackCtx, transaction, record.WorkspaceID, receipt.AggregateID)
				if err != nil {
					return gormKnowledgeReadError(callbackCtx, err, errorCodeClaimNotFound)
				}
				if err := gormKnowledgeAssertReceipt(receipt, loaded.Claim.ID, loaded.Claim.Version, true); err != nil {
					return err
				}
				loaded.Replayed = true
				result = loaded
				return nil
			}
			current, sources, err := gormKnowledgeLockClaimAggregate(callbackCtx, transaction, record.WorkspaceID, record.ClaimID)
			if err != nil {
				return gormKnowledgeReadError(callbackCtx, err, errorCodeClaimNotFound)
			}
			if current.Version != record.ExpectedVersion {
				return versionConflict(errors.New("claim expected version is stale"))
			}
			if err := domain.ValidateClaimTransition(current.Status, record.Status); err != nil {
				return err
			}
			updated, err := gormKnowledgeScanClaim(callbackCtx, transaction, `
				UPDATE core.claim SET status=?,version=version+1,updated_at=?
				WHERE workspace_id=? AND id=? AND version=?
				RETURNING id::text,workspace_id::text,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at`, string(record.Status), record.At.UTC(), string(record.WorkspaceID), string(record.ClaimID), record.ExpectedVersion)
			if gormKnowledgeNoRows(err) {
				return versionConflict(err)
			}
			if err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			if err := domain.ValidateClaimAggregate(updated, sources); err != nil {
				return consistency(domain.ErrorCodeClaimInvalid, err)
			}
			if err := gormKnowledgeInsertReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, commandTransitionClaim, aggregateClaim, updated.ID, updated.Version, record.At); err != nil {
				return classifyGORMKnowledge(callbackCtx, err, errorCodeDatabaseUnavailable)
			}
			result = domain.ClaimResult{Claim: updated, Sources: sources}
			return nil
		})
	if err != nil {
		return domain.ClaimResult{}, err
	}
	return result, nil
}

func gormKnowledgeLockTopicIdentities(ctx context.Context, database *gorm.DB, topic domain.Topic) error {
	identities := make([]string, 0, len(topic.Aliases)+1)
	identities = append(identities, topic.NormalizedName)
	for _, alias := range topic.Aliases {
		identities = append(identities, alias.NormalizedName)
	}
	sort.Strings(identities)
	for _, identity := range identities {
		if _, err := gormKnowledgeExec(ctx, database, `SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ?, 0))`, string(topic.WorkspaceID), identity); err != nil {
			return classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
		}
	}
	return nil
}

func gormKnowledgeGetTopic(ctx context.Context, database *gorm.DB, workspaceID, topicID foundation.ID) (domain.Topic, error) {
	row, err := gormKnowledgeRawRow(ctx, database, `
		SELECT id::text,workspace_id::text,name,normalized_name,description,status,merged_into_topic_id::text,version,created_at,updated_at
		FROM core.topic WHERE workspace_id=? AND id=?`, string(workspaceID), string(topicID))
	if err != nil {
		return domain.Topic{}, err
	}
	topic, err := scanTopic(row)
	if err != nil {
		return domain.Topic{}, err
	}
	rows, err := gormKnowledgeRawRows(ctx, database, `SELECT alias,normalized_alias FROM core.topic_alias WHERE workspace_id=? AND topic_id=? ORDER BY normalized_alias,id`, string(workspaceID), string(topicID))
	if err != nil {
		return domain.Topic{}, err
	}
	defer rows.Close()
	topic.Aliases = make([]domain.TopicAlias, 0)
	for rows.Next() {
		var alias domain.TopicAlias
		if err := rows.Scan(&alias.Name, &alias.NormalizedName); err != nil {
			return domain.Topic{}, err
		}
		topic.Aliases = append(topic.Aliases, alias)
	}
	if err := rows.Err(); err != nil {
		return domain.Topic{}, err
	}
	if err := domain.ValidateTopic(topic); err != nil {
		return domain.Topic{}, consistency(domain.ErrorCodeTopicInvalid, err)
	}
	return topic, nil
}

func gormKnowledgeScanClaim(ctx context.Context, database *gorm.DB, query string, arguments ...any) (domain.Claim, error) {
	row, err := gormKnowledgeRawRow(ctx, database, query, arguments...)
	if err != nil {
		return domain.Claim{}, err
	}
	return scanClaim(row)
}

func gormKnowledgeLoadClaimResult(ctx context.Context, database *gorm.DB, workspaceID, claimID foundation.ID) (domain.ClaimResult, error) {
	claim, err := gormKnowledgeScanClaim(ctx, database, claimSelect+` WHERE workspace_id=? AND id=?`, string(workspaceID), string(claimID))
	if err != nil {
		return domain.ClaimResult{}, err
	}
	sources, err := gormKnowledgeLoadClaimSources(ctx, database, workspaceID, []foundation.ID{claimID})
	if err != nil {
		return domain.ClaimResult{}, err
	}
	if err := domain.ValidateClaimAggregate(claim, sources[claimID]); err != nil {
		return domain.ClaimResult{}, consistency(domain.ErrorCodeClaimInvalid, err)
	}
	return domain.ClaimResult{Claim: claim, Sources: sources[claimID]}, nil
}

func gormKnowledgeLoadClaimSources(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, claimIDs []foundation.ID) (map[foundation.ID][]domain.ClaimSource, error) {
	result := make(map[foundation.ID][]domain.ClaimSource, len(claimIDs))
	for _, id := range claimIDs {
		result[id] = []domain.ClaimSource{}
	}
	if len(claimIDs) == 0 {
		return result, nil
	}
	rows, err := gormKnowledgeRawRows(ctx, database, `
		SELECT id::text,workspace_id::text,claim_id::text,source_version_id::text,source_span_id::text,support_type,reason,evidence_hash,model_run_ref,created_at
		FROM core.claim_source WHERE workspace_id=? AND claim_id=ANY(?::uuid[]) ORDER BY claim_id,created_at,id`, string(workspaceID), pq.Array(idsAsStrings(claimIDs)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		source, err := scanClaimSource(rows)
		if err != nil {
			return nil, err
		}
		result[source.ClaimID] = append(result[source.ClaimID], source)
	}
	return result, rows.Err()
}

func gormKnowledgeLockClaimAggregate(ctx context.Context, database *gorm.DB, workspaceID, claimID foundation.ID) (domain.Claim, []domain.ClaimSource, error) {
	claim, err := gormKnowledgeScanClaim(ctx, database, claimSelect+` WHERE workspace_id=? AND id=? FOR UPDATE`, string(workspaceID), string(claimID))
	if err != nil {
		return domain.Claim{}, nil, err
	}
	sources, err := gormKnowledgeLoadClaimSources(ctx, database, workspaceID, []foundation.ID{claimID})
	if err != nil {
		return domain.Claim{}, nil, err
	}
	if err := domain.ValidateClaimAggregate(claim, sources[claimID]); err != nil {
		return domain.Claim{}, nil, consistency(domain.ErrorCodeClaimInvalid, err)
	}
	return claim, sources[claimID], nil
}

func gormKnowledgeInsertClaimSource(ctx context.Context, database *gorm.DB, source domain.ClaimSource) error {
	_, err := gormKnowledgeExec(ctx, database, `
		INSERT INTO core.claim_source (id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,model_run_ref,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, string(source.ID), string(source.WorkspaceID), string(source.ClaimID), string(source.Provenance.SourceVersionID), string(source.Provenance.SourceSpanID), string(source.SupportType), source.Reason, source.EvidenceHash, pointerString(source.ModelRunRef), source.CreatedAt.UTC())
	return err
}

func gormKnowledgeReadError(ctx context.Context, err error, notFoundCode string) error {
	if gormKnowledgeNoRows(err) {
		return notFound(notFoundCode, err)
	}
	return classifyGORMKnowledge(ctx, err, errorCodeDatabaseUnavailable)
}

func gormKnowledgeAssertReceipt(receipt *gormKnowledgeReceipt, aggregateID foundation.ID, currentVersion int64, requireVersion bool) error {
	if receipt.AggregateID != aggregateID {
		return consistency(errorCodeStorageConsistency, errors.New("knowledge receipt aggregate mismatch"))
	}
	if requireVersion && (receipt.AggregateVersion <= 0 || currentVersion < receipt.AggregateVersion) {
		return consistency(errorCodeStorageConsistency, errors.New("knowledge receipt version exceeds aggregate version"))
	}
	return nil
}
