package postgres

import (
	"context"
	"errors"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// ListConversations 返回 Workspace 内按最近活动时间稳定排序的有限页面。
func (repository *Repository) ListConversations(ctx context.Context, query conversationapplication.ListConversationsQuery) (conversationapplication.ConversationPage, error) {
	if repository == nil || isNilInterface(repository.db) {
		return conversationapplication.ConversationPage{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if err := validateConversationListQuery(query); err != nil {
		return conversationapplication.ConversationPage{}, err
	}
	pageSize := query.Limit + 1
	var rows pgx.Rows
	var err error
	if query.Cursor == nil {
		rows, err = repository.db.Query(ctx, `SELECT `+conversationColumns+`
			FROM agent.conversation
			WHERE workspace_id=$1
			ORDER BY last_activity_at DESC,id ASC
			LIMIT $2`, string(query.WorkspaceID), pageSize)
	} else {
		rows, err = repository.db.Query(ctx, `SELECT `+conversationColumns+`
			FROM agent.conversation
			WHERE workspace_id=$1
			  AND (last_activity_at < $2 OR (last_activity_at = $2 AND id > $3))
			ORDER BY last_activity_at DESC,id ASC
			LIMIT $4`, string(query.WorkspaceID), query.Cursor.LastActivityAt.UTC(), string(query.Cursor.ID), pageSize)
	}
	if err != nil {
		return conversationapplication.ConversationPage{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer rows.Close()

	items := make([]conversationdomain.Conversation, 0, query.Limit)
	for rows.Next() {
		record, scanErr := scanConversationRecord(rows)
		if scanErr != nil {
			return conversationapplication.ConversationPage{}, scanErr
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

// GetConversation 在 Workspace 联合边界内读取一个 Conversation。
func (repository *Repository) GetConversation(ctx context.Context, workspaceID, conversationID foundation.ID) (conversationdomain.Conversation, error) {
	if repository == nil || isNilInterface(repository.db) {
		return conversationdomain.Conversation{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if err := validateScopedConversationIDs(workspaceID, conversationID); err != nil {
		return conversationdomain.Conversation{}, err
	}
	record, err := scanConversationRecord(repository.db.QueryRow(ctx, `SELECT `+conversationColumns+`
		FROM agent.conversation WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(conversationID)))
	if errors.Is(err, pgx.ErrNoRows) {
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

func validateConversationListQuery(query conversationapplication.ListConversationsQuery) error {
	if !validCanonicalID(query.WorkspaceID) {
		return invalid(ErrorCodePersistenceInvalid, errors.New("conversation list workspace is invalid"))
	}
	if err := conversationdomain.ValidatePageLimit(query.Limit); err != nil {
		return err
	}
	if query.Cursor != nil {
		if err := query.Cursor.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateScopedConversationIDs(workspaceID, conversationID foundation.ID) error {
	if !validCanonicalID(workspaceID) || !validCanonicalID(conversationID) || workspaceID == conversationID {
		return invalid(ErrorCodePersistenceInvalid, errors.New("conversation identity is invalid"))
	}
	return nil
}

func validCanonicalID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
