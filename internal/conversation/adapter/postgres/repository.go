package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/jackc/pgx/v5"
)

const conversationColumns = `
	id::text,workspace_id::text,status,title,version,
	last_activity_at,created_at,updated_at,archived_at,idempotency_key,request_hash`

// DB 是 Conversation Repository 所需的最小 pgx 事务和查询边界。
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Repository 持久化 Conversation 事实并批量构造 Turn/Answer 读模型。
type Repository struct {
	db     DB
	events eventsapplication.Appender
}

// NewRepository 构造需要真实数据库和同事务事件追加端口的 Repository。
func NewRepository(db DB, events eventsapplication.Appender) (*Repository, error) {
	if isNilInterface(db) || isNilInterface(events) {
		return nil, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation database or event appender is nil"))
	}
	return &Repository{db: db, events: events}, nil
}

// CreateConversation 在一个事务内创建 Conversation 与 conversation.created 通知。
func (repository *Repository) CreateConversation(ctx context.Context, record conversationapplication.CreateConversationRecord) (conversationapplication.CreateConversationResult, error) {
	if repository == nil || isNilInterface(repository.db) || isNilInterface(repository.events) {
		return conversationapplication.CreateConversationResult{}, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation repository is unavailable"))
	}
	if ctx == nil {
		return conversationapplication.CreateConversationResult{}, invalid(ErrorCodePersistenceInvalid, errors.New("conversation context is nil"))
	}
	request, err := validateCreateRecord(record)
	if err != nil {
		return conversationapplication.CreateConversationResult{}, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return conversationapplication.CreateConversationResult{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var workspaceID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR KEY SHARE`, string(record.Conversation.WorkspaceID)).Scan(&workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conversationapplication.CreateConversationResult{}, notFound(ErrorCodeWorkspaceNotFound, err)
		}
		return conversationapplication.CreateConversationResult{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2,0))`, workspaceID, record.IdempotencyKey); err != nil {
		return conversationapplication.CreateConversationResult{}, classify(err, ErrorCodeDatabaseUnavailable)
	}

	existing, err := scanConversationRecord(tx.QueryRow(ctx, `SELECT `+conversationColumns+`
		FROM agent.conversation WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`,
		string(record.Conversation.WorkspaceID), record.IdempotencyKey))
	if err == nil {
		if existing.RequestHash != record.RequestHash || !sameCreateRequest(existing.Conversation, request) {
			return conversationapplication.CreateConversationResult{}, conflict(ErrorCodeIdempotencyConflict, errors.New("conversation idempotency key is bound to a different request"))
		}
		// Server events are a retained notification projection. Exact command
		// replay must continue to work after that projection is cleaned up.
		if err := tx.Commit(ctx); err != nil {
			return conversationapplication.CreateConversationResult{}, classify(err, ErrorCodeDatabaseUnavailable)
		}
		return conversationapplication.CreateConversationResult{Conversation: existing.Conversation, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return conversationapplication.CreateConversationResult{}, err
	}

	persisted, err := scanConversationRecord(tx.QueryRow(ctx, `INSERT INTO agent.conversation(
		id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,archived_at,idempotency_key,request_hash
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+conversationColumns,
		string(record.Conversation.ID), string(record.Conversation.WorkspaceID), string(record.Conversation.Status), record.Conversation.Title,
		record.Conversation.Version, record.Conversation.LastActivityAt.UTC(), record.Conversation.CreatedAt.UTC(), record.Conversation.UpdatedAt.UTC(),
		record.Conversation.ArchivedAt, record.IdempotencyKey, record.RequestHash))
	if err != nil {
		return conversationapplication.CreateConversationResult{}, err
	}
	if persisted.RequestHash != record.RequestHash || persisted.IdempotencyKey != record.IdempotencyKey || !sameCreateRequest(persisted.Conversation, request) {
		return conversationapplication.CreateConversationResult{}, consistency(ErrorCodePersistenceCorrupt, errors.New("created conversation readback differs from request"))
	}
	replayed, err := repository.appendConversationCreated(ctx, tx, persisted.Conversation)
	if err != nil {
		return conversationapplication.CreateConversationResult{}, err
	}
	if replayed {
		return conversationapplication.CreateConversationResult{}, consistency(ErrorCodePersistenceCorrupt, errors.New("new conversation reused an existing creation event"))
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationapplication.CreateConversationResult{}, classify(err, ErrorCodeDatabaseUnavailable)
	}
	return conversationapplication.CreateConversationResult{Conversation: persisted.Conversation}, nil
}

func (repository *Repository) appendConversationCreated(ctx context.Context, tx pgx.Tx, conversation conversationdomain.Conversation) (bool, error) {
	conversationID := conversation.ID
	_, replayed, err := repository.events.AppendTx(ctx, tx, eventsdomain.AppendRequest{
		WorkspaceID:     conversation.WorkspaceID,
		ConversationID:  &conversationID,
		Type:            "conversation.created",
		ResourceRef:     "conversation:" + string(conversation.ID),
		ResourceVersion: conversation.Version,
		PayloadSummary: eventsdomain.PayloadSummary{
			ConversationID: &conversationID,
			Status:         string(conversation.Status),
		},
		SchemaVersion:  1,
		SourceEventRef: "conversation.created:" + string(conversation.ID) + ":v1",
		OccurredAt:     conversation.CreatedAt,
	})
	return replayed, err
}

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
