package postgres

import (
	"context"
	"database/sql"
	"errors"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateConversation 在同一 scope 创建或精确重放会话及通知。
func (repository *GORMRepository) CreateConversation(ctx context.Context, record conversationapplication.CreateConversationRecord) (conversationapplication.CreateConversationResult, error) {
	if repository == nil || repository.db == nil || isNilInterface(repository.uow) || isNilInterface(repository.events) {
		return conversationapplication.CreateConversationResult{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if ctx == nil {
		return conversationapplication.CreateConversationResult{}, invalid(ErrorCodePersistenceInvalid, errors.New("conversation context is nil"))
	}
	request, err := validateCreateRecord(record)
	if err != nil {
		return conversationapplication.CreateConversationResult{}, err
	}
	var result conversationapplication.CreateConversationResult
	err = withinConversationTransaction(ctx, repository.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var workspaceID string
		if err := gormScanRow(tx.Raw(`SELECT id::text FROM core.workspace WHERE id=? FOR KEY SHARE`, string(record.Conversation.WorkspaceID))).Scan(&workspaceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return notFound(ErrorCodeWorkspaceNotFound, err)
			}
			return classify(err, ErrorCodeDatabaseUnavailable)
		}
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ?,0))`, workspaceID, record.IdempotencyKey).Error; err != nil {
			return classify(err, ErrorCodeDatabaseUnavailable)
		}
		existing, err := scanConversationRecord(gormScanRow(tx.Model(&conversationModel{}).Select(conversationColumns).
			Where("workspace_id=? AND idempotency_key=?", workspaceID, record.IdempotencyKey).Clauses(clause.Locking{Strength: "UPDATE"})))
		if err == nil {
			if existing.RequestHash != record.RequestHash || !sameCreateRequest(existing.Conversation, request) {
				return conflict(ErrorCodeIdempotencyConflict, errors.New("conversation idempotency key is bound to a different request"))
			}
			result = conversationapplication.CreateConversationResult{Conversation: existing.Conversation, Replayed: true}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		conversation := record.Conversation
		model := conversationModel{
			ID: string(conversation.ID), WorkspaceID: workspaceID, Status: string(conversation.Status), Title: conversation.Title,
			Version: conversation.Version, LastActivityAt: conversation.LastActivityAt.UTC(), CreatedAt: conversation.CreatedAt.UTC(),
			UpdatedAt: conversation.UpdatedAt.UTC(), ArchivedAt: conversation.ArchivedAt, IdempotencyKey: record.IdempotencyKey, RequestHash: record.RequestHash,
		}
		if err := tx.Create(&model).Error; err != nil {
			return classify(err, ErrorCodeDatabaseUnavailable)
		}
		persisted, err := scanConversationRecord(gormScanRow(tx.Model(&conversationModel{}).Select(conversationColumns).
			Where("workspace_id=? AND id=?", workspaceID, model.ID)))
		if err != nil {
			return err
		}
		if persisted.RequestHash != record.RequestHash || persisted.IdempotencyKey != record.IdempotencyKey || !sameCreateRequest(persisted.Conversation, request) {
			return consistency(ErrorCodePersistenceCorrupt, errors.New("created conversation readback differs from request"))
		}
		conversationID := persisted.Conversation.ID
		_, replayed, err := repository.events.AppendScoped(ctx, scope, eventsdomain.AppendRequest{
			WorkspaceID: persisted.Conversation.WorkspaceID, ConversationID: &conversationID,
			Type: "conversation.created", ResourceRef: "conversation:" + string(conversationID), ResourceVersion: persisted.Conversation.Version,
			PayloadSummary: eventsdomain.PayloadSummary{ConversationID: &conversationID, Status: string(persisted.Conversation.Status)},
			SchemaVersion:  1, SourceEventRef: "conversation.created:" + string(conversationID) + ":v1", OccurredAt: persisted.Conversation.CreatedAt,
		})
		if err != nil {
			return err
		}
		if replayed {
			return consistency(ErrorCodePersistenceCorrupt, errors.New("new conversation reused an existing creation event"))
		}
		result = conversationapplication.CreateConversationResult{Conversation: persisted.Conversation}
		return nil
	})
	if err != nil {
		return conversationapplication.CreateConversationResult{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	return result, nil
}

// ListConversations 返回 Workspace 内最近活动时间与 ID 稳定排序的有限页面。
func (repository *GORMRepository) ListConversations(ctx context.Context, query conversationapplication.ListConversationsQuery) (conversationapplication.ConversationPage, error) {
	if repository == nil || repository.db == nil {
		return conversationapplication.ConversationPage{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if err := validateConversationListQuery(query); err != nil {
		return conversationapplication.ConversationPage{}, err
	}
	statement := repository.db.WithContext(ctx).Model(&conversationModel{}).Select(conversationColumns).Where("workspace_id=?", string(query.WorkspaceID))
	if query.Cursor != nil {
		statement = statement.Where("last_activity_at < ? OR (last_activity_at = ? AND id > ?)", query.Cursor.LastActivityAt.UTC(), query.Cursor.LastActivityAt.UTC(), string(query.Cursor.ID))
	}
	rows, err := statement.Order("last_activity_at DESC,id ASC").Limit(query.Limit + 1).Rows()
	if err != nil {
		return conversationapplication.ConversationPage{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer rows.Close()
	items := make([]conversationdomain.Conversation, 0, query.Limit)
	for rows.Next() {
		record, err := scanConversationRecord(rows)
		if err != nil {
			return conversationapplication.ConversationPage{}, err
		}
		if record.Conversation.WorkspaceID != query.WorkspaceID {
			return conversationapplication.ConversationPage{}, consistency(ErrorCodePersistenceCorrupt, errors.New("conversation list crossed workspace boundary"))
		}
		items = append(items, record.Conversation)
	}
	if err := rows.Err(); err != nil {
		return conversationapplication.ConversationPage{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	page := conversationapplication.ConversationPage{Items: items}
	if len(items) > query.Limit {
		last := items[query.Limit-1]
		page.Items = items[:query.Limit]
		page.NextCursor = &conversationdomain.ConversationCursor{LastActivityAt: last.LastActivityAt, ID: last.ID}
	}
	return page, nil
}

// GetConversation 始终按 Workspace 和 Conversation 双身份读取。
func (repository *GORMRepository) GetConversation(ctx context.Context, workspaceID, conversationID foundation.ID) (conversationdomain.Conversation, error) {
	if repository == nil || repository.db == nil {
		return conversationdomain.Conversation{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if err := validateScopedConversationIDs(workspaceID, conversationID); err != nil {
		return conversationdomain.Conversation{}, err
	}
	record, err := scanConversationRecord(gormScanRow(repository.db.WithContext(ctx).Model(&conversationModel{}).Select(conversationColumns).
		Where("workspace_id=? AND id=?", string(workspaceID), string(conversationID))))
	if errors.Is(err, sql.ErrNoRows) {
		return conversationdomain.Conversation{}, notFound(ErrorCodeConversationNotFound, err)
	}
	if err != nil {
		return conversationdomain.Conversation{}, err
	}
	if record.Conversation.WorkspaceID != workspaceID || record.Conversation.ID != conversationID {
		return conversationdomain.Conversation{}, consistency(ErrorCodePersistenceCorrupt, errors.New("conversation read crossed workspace boundary"))
	}
	return record.Conversation, nil
}
