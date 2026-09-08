package postgres

import (
	"errors"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

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
