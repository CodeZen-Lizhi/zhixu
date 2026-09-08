package postgres

import (
	"errors"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"reflect"
	"strings"
	"unicode/utf8"
)

const conversationColumns = `
	id::text,workspace_id::text,status,title,version,
	last_activity_at,created_at,updated_at,archived_at,idempotency_key,request_hash`

func validateCreateRecord(record conversationapplication.CreateConversationRecord) (conversationdomain.ConversationCreateRequest, error) {
	if err := conversationdomain.ValidateConversation(record.Conversation); err != nil ||
		record.Conversation.Status != conversationdomain.ConversationStatusOpen || record.Conversation.Version != 1 {
		return conversationdomain.ConversationCreateRequest{}, invalid(ErrorCodePersistenceInvalid, err)
	}
	if !canonicalIdempotencyKey(record.IdempotencyKey) {
		return conversationdomain.ConversationCreateRequest{}, invalid(ErrorCodePersistenceInvalid, errors.New("conversation idempotency key is invalid"))
	}
	request, err := conversationdomain.CanonicalizeConversationCreateRequest(conversationdomain.ConversationCreateRequest{
		WorkspaceID: record.Conversation.WorkspaceID,
		Title:       record.Conversation.Title,
	})
	if err != nil {
		return conversationdomain.ConversationCreateRequest{}, err
	}
	requestHash, err := conversationdomain.ComputeConversationCreateRequestHash(request)
	if err != nil || requestHash != record.RequestHash {
		return conversationdomain.ConversationCreateRequest{}, invalid(ErrorCodePersistenceInvalid, errors.New("conversation request hash is inconsistent"))
	}
	return request, nil
}

func sameCreateRequest(conversation conversationdomain.Conversation, request conversationdomain.ConversationCreateRequest) bool {
	return conversation.WorkspaceID == request.WorkspaceID && reflect.DeepEqual(conversation.Title, request.Title)
}

func canonicalIdempotencyKey(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= conversationapplication.MaxIdempotencyKeyBytes &&
		utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n\x00")
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
