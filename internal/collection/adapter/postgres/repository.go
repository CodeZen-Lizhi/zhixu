package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB 是 Collection PostgreSQL adapter 所需的最小参数化查询边界。
type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type nestedTransactionBeginner struct {
	tx pgx.Tx
}

func (beginner nestedTransactionBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	return beginner.tx.Begin(ctx)
}

func (beginner nestedTransactionBeginner) BeginTx(ctx context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	// PostgreSQL cannot change isolation/access mode inside a transaction. This
	// path is used when a caller deliberately supplies an existing transaction,
	// so preserve that transaction's snapshot and create a savepoint.
	return beginner.tx.Begin(ctx)
}

// Repository 持久化 Smart Collection 聚合与命令 receipt。
type Repository struct {
	db       DB
	beginner transactionBeginner
	cursor   *collectionapp.CursorCodec
}

// NewRepository 创建 Collection PostgreSQL adapter。
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, unavailable(errors.New("collection database is nil"))
	}
	beginner, ok := db.(transactionBeginner)
	if !ok {
		tx, isTransaction := db.(pgx.Tx)
		if !isTransaction {
			return nil, unavailable(errors.New("collection database does not support transactions"))
		}
		beginner = nestedTransactionBeginner{tx: tx}
	}
	cursor, err := collectionapp.NewRandomCursorCodec()
	if err != nil {
		return nil, unavailable(err)
	}
	return &Repository{db: db, beginner: beginner, cursor: cursor}, nil
}

const collectionColumns = `id::text,workspace_id::text,name,normalized_name,description,
	query_schema_version,query_version,query_definition,query_hash,view_type,view_config,status,
	cached_result_version,last_executed_at,version,created_at,updated_at`

const collectionSelect = `SELECT ` + collectionColumns + ` FROM learning.smart_collection`

// CreateCollection 在单事务中写入集合与 create receipt。
func (r *Repository) CreateCollection(ctx context.Context, record collectionapp.CreateRecord) (collectionapp.CommandResult, error) {
	if r == nil || r.db == nil {
		return collectionapp.CommandResult{}, unavailable(errors.New("collection repository is unavailable"))
	}
	if err := validateCreateRecord(record); err != nil {
		return collectionapp.CommandResult{}, err
	}
	return writeTx(ctx, r, func(tx DB) (collectionapp.CommandResult, error) {
		if err := lockWorkspace(ctx, tx, record.Collection.WorkspaceID); err != nil {
			return collectionapp.CommandResult{}, err
		}
		if receipt, found, err := loadCommand(ctx, tx, record.Collection.WorkspaceID, record.IdempotencyKey); err != nil {
			return collectionapp.CommandResult{}, err
		} else if found {
			if receipt.requestHash != record.RequestHash || receipt.commandType != "CREATE" {
				return collectionapp.CommandResult{}, idempotencyConflict(errors.New("collection idempotency key is bound to another request"))
			}
			return commandResult(receipt.collection, receipt, true), nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.smart_collection(`+collectionColumnsForInsert+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, collectionArgs(record.Collection)...); err != nil {
			return collectionapp.CommandResult{}, classify(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.smart_collection_command(workspace_id,idempotency_key,request_hash,command_type,collection_id,collection_version,receipt,created_at) VALUES($1,$2,$3,'CREATE',$4,$5,$6,$7)`, string(record.Collection.WorkspaceID), record.IdempotencyKey, record.RequestHash, string(record.Collection.ID), record.Collection.Version, commandReceiptJSON(record.Collection, record.IdempotencyKey, "CREATE", record.RequestHash), record.Collection.CreatedAt.UTC()); err != nil {
			return collectionapp.CommandResult{}, classify(err)
		}
		receipt := commandReceipt{workspaceID: record.Collection.WorkspaceID, idempotencyKey: record.IdempotencyKey, requestHash: record.RequestHash, commandType: "CREATE", collectionID: record.Collection.ID, collectionVersion: record.Collection.Version}
		return commandResult(record.Collection, receipt, false), nil
	})
}

// UpdateCollection 在单事务中执行名称、查询、视图和描述的替换。
func (r *Repository) UpdateCollection(ctx context.Context, record collectionapp.UpdateRecord) (collectionapp.CommandResult, error) {
	if r == nil || r.db == nil {
		return collectionapp.CommandResult{}, unavailable(errors.New("collection repository is unavailable"))
	}
	if err := validateUpdateRecord(record); err != nil {
		return collectionapp.CommandResult{}, err
	}
	return writeTx(ctx, r, func(tx DB) (collectionapp.CommandResult, error) {
		if err := lockWorkspace(ctx, tx, record.WorkspaceID); err != nil {
			return collectionapp.CommandResult{}, err
		}
		if receipt, found, err := loadCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return collectionapp.CommandResult{}, err
		} else if found {
			if receipt.requestHash != record.RequestHash || receipt.commandType != "UPDATE" || receipt.collectionID != record.CollectionID {
				return collectionapp.CommandResult{}, idempotencyConflict(errors.New("collection idempotency key is bound to another request"))
			}
			return commandResult(receipt.collection, receipt, true), nil
		}
		current, err := loadCollection(ctx, tx, record.WorkspaceID, record.CollectionID, true)
		if err != nil {
			return collectionapp.CommandResult{}, err
		}
		if current.Status == collectionapp.CollectionStatusArchived {
			return collectionapp.CommandResult{}, archivedImmutable(errors.New("archived collection cannot be updated"))
		}
		if current.Version != record.ExpectedVersion {
			return collectionapp.CommandResult{}, versionConflict(collectionapp.ErrorCodeVersionConflict, errors.New("collection expected version mismatch"))
		}
		now := record.At.UTC()
		if now.IsZero() {
			now = record.Collection.UpdatedAt.UTC()
		}
		if now.Before(current.UpdatedAt) {
			now = current.UpdatedAt
		}
		queryVersion := current.QueryVersion
		if current.QueryHash != record.Collection.QueryHash {
			queryVersion++
		}
		if _, err := tx.Exec(ctx, `UPDATE learning.smart_collection SET name=$3,normalized_name=$4,description=$5,query_schema_version=$6,query_version=$7,query_definition=$8,query_hash=$9,view_type=$10,view_config=$11,status='ACTIVE',version=version+1,updated_at=$12 WHERE workspace_id=$1 AND id=$2 AND version=$13`, string(record.WorkspaceID), string(record.CollectionID), record.Collection.Name, record.Collection.NormalizedName, record.Collection.Description, record.Collection.QuerySchemaVersion, queryVersion, queryDefinition(record.Collection), record.Collection.QueryHash, string(record.Collection.ViewType), viewConfig(record.Collection), now, record.ExpectedVersion); err != nil {
			return collectionapp.CommandResult{}, classify(err)
		}
		updated := record.Collection
		updated.QueryVersion = queryVersion
		updated.Version = record.ExpectedVersion + 1
		updated.Status = collectionapp.CollectionStatusActive
		updated.CreatedAt = current.CreatedAt
		updated.UpdatedAt = now
		updated.CachedResultVersion = current.CachedResultVersion
		updated.LastExecutedAt = current.LastExecutedAt
		// The receipt must contain the exact aggregate that was committed, not
		// the caller's partial update payload.
		if _, err := tx.Exec(ctx, `INSERT INTO learning.smart_collection_command(workspace_id,idempotency_key,request_hash,command_type,collection_id,collection_version,receipt,created_at) VALUES($1,$2,$3,'UPDATE',$4,$5,$6,$7)`, string(record.WorkspaceID), record.IdempotencyKey, record.RequestHash, string(record.CollectionID), updated.Version, commandReceiptJSON(updated, record.IdempotencyKey, "UPDATE", record.RequestHash), now); err != nil {
			return collectionapp.CommandResult{}, classify(err)
		}
		receipt := commandReceipt{workspaceID: record.WorkspaceID, idempotencyKey: record.IdempotencyKey, requestHash: record.RequestHash, commandType: "UPDATE", collectionID: record.CollectionID, collectionVersion: updated.Version}
		return commandResult(updated, receipt, false), nil
	})
}

// ArchiveCollection 在单事务中将集合转为不可变归档状态。
func (r *Repository) ArchiveCollection(ctx context.Context, record collectionapp.ArchiveRecord) (collectionapp.CommandResult, error) {
	if r == nil || r.db == nil {
		return collectionapp.CommandResult{}, unavailable(errors.New("collection repository is unavailable"))
	}
	if err := validateArchiveRecord(record); err != nil {
		return collectionapp.CommandResult{}, err
	}
	return writeTx(ctx, r, func(tx DB) (collectionapp.CommandResult, error) {
		if err := lockWorkspace(ctx, tx, record.WorkspaceID); err != nil {
			return collectionapp.CommandResult{}, err
		}
		if receipt, found, err := loadCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return collectionapp.CommandResult{}, err
		} else if found {
			if receipt.requestHash != record.RequestHash || receipt.commandType != "ARCHIVE" || receipt.collectionID != record.CollectionID {
				return collectionapp.CommandResult{}, idempotencyConflict(errors.New("collection idempotency key is bound to another request"))
			}
			return commandResult(receipt.collection, receipt, true), nil
		}
		current, err := loadCollection(ctx, tx, record.WorkspaceID, record.CollectionID, true)
		if err != nil {
			return collectionapp.CommandResult{}, err
		}
		if current.Status == collectionapp.CollectionStatusArchived {
			return collectionapp.CommandResult{}, archivedImmutable(errors.New("collection is already archived"))
		}
		if current.Version != record.ExpectedVersion {
			return collectionapp.CommandResult{}, versionConflict(collectionapp.ErrorCodeVersionConflict, errors.New("collection expected version mismatch"))
		}
		now := record.At.UTC()
		if now.IsZero() {
			now = time.Now().UTC()
		}
		if now.Before(current.UpdatedAt) {
			now = current.UpdatedAt
		}
		if _, err := tx.Exec(ctx, `UPDATE learning.smart_collection SET status='ARCHIVED',version=version+1,updated_at=$3 WHERE workspace_id=$1 AND id=$2 AND version=$4`, string(record.WorkspaceID), string(record.CollectionID), now, record.ExpectedVersion); err != nil {
			return collectionapp.CommandResult{}, classify(err)
		}
		current.Status = collectionapp.CollectionStatusArchived
		current.Version = record.ExpectedVersion + 1
		current.UpdatedAt = now
		if _, err := tx.Exec(ctx, `INSERT INTO learning.smart_collection_command(workspace_id,idempotency_key,request_hash,command_type,collection_id,collection_version,receipt,created_at) VALUES($1,$2,$3,'ARCHIVE',$4,$5,$6,$7)`, string(record.WorkspaceID), record.IdempotencyKey, record.RequestHash, string(record.CollectionID), current.Version, commandReceiptJSON(current, record.IdempotencyKey, "ARCHIVE", record.RequestHash), now); err != nil {
			return collectionapp.CommandResult{}, classify(err)
		}
		receipt := commandReceipt{workspaceID: record.WorkspaceID, idempotencyKey: record.IdempotencyKey, requestHash: record.RequestHash, commandType: "ARCHIVE", collectionID: record.CollectionID, collectionVersion: current.Version}
		return commandResult(current, receipt, false), nil
	})
}

// GetCollection 按 Workspace 与 ID 读取集合。
func (r *Repository) GetCollection(ctx context.Context, workspaceID, collectionID foundation.ID) (collectionapp.Collection, error) {
	if r == nil || r.db == nil {
		return collectionapp.Collection{}, unavailable(errors.New("collection repository is unavailable"))
	}
	return loadCollection(ctx, r.db, workspaceID, collectionID, false)
}

// ListCollections 返回稳定排序的有界集合列表。
func (r *Repository) ListCollections(ctx context.Context, query collectionapp.ListQuery) (collectionapp.CollectionListPage, error) {
	if r == nil || r.db == nil {
		return collectionapp.CollectionListPage{}, unavailable(errors.New("collection repository is unavailable"))
	}
	tx, err := r.beginner.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return collectionapp.CollectionListPage{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := configureCollectionStatementTimeout(ctx, tx, defaultCollectionStatementTimeout); err != nil {
		return collectionapp.CollectionListPage{}, classify(err)
	}
	values := canonicalCollectionStatuses(query.Statuses)
	statusParts := append([]string{"collection-list-status/v1"}, values...)
	statusHash := collectionListHash(statusParts...)
	sortHash := collectionListHash("collection-list-sort/v1", "updated_at:desc", "id:desc")
	revisionHash, err := collectionListRevision(ctx, tx, query.WorkspaceID, values)
	if err != nil {
		return collectionapp.CollectionListPage{}, err
	}
	args := []any{string(query.WorkspaceID), values}
	where := ` WHERE workspace_id=$1 AND status=ANY($2::text[])`
	if query.Cursor != "" {
		cursor, decodeErr := r.cursor.Decode(query.Cursor, collectionapp.CursorBinding{
			Scope: collectionapp.CursorScopeList, WorkspaceID: query.WorkspaceID,
			QueryHash: statusHash, SortHash: sortHash, Limit: query.Limit, RevisionHash: revisionHash,
		})
		if decodeErr != nil {
			return collectionapp.CollectionListPage{}, decodeErr
		}
		lastUpdatedAt, parseErr := time.Parse(time.RFC3339Nano, *cursor.LastSortValues[0])
		if parseErr != nil || *cursor.LastSortValues[1] != string(cursor.LastID) {
			return collectionapp.CollectionListPage{}, foundation.NewError(foundation.ErrorInvalidInput, collectionapp.ErrorCodeCursorInvalid, false, errors.New("collection list cursor key is invalid"))
		}
		where += ` AND (updated_at < $3 OR (updated_at = $3 AND id < $4::uuid))`
		args = append(args, lastUpdatedAt.UTC(), string(cursor.LastID))
	}
	limitPosition := len(args) + 1
	args = append(args, query.Limit+1)
	rows, err := tx.Query(ctx, collectionSelect+where+fmt.Sprintf(` ORDER BY updated_at DESC,id DESC LIMIT $%d`, limitPosition), args...)
	if err != nil {
		return collectionapp.CollectionListPage{}, classify(err)
	}
	defer rows.Close()
	result := make([]collectionapp.Collection, 0, query.Limit+1)
	for rows.Next() {
		item, err := scanCollection(rows)
		if err != nil {
			return collectionapp.CollectionListPage{}, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return collectionapp.CollectionListPage{}, classify(err)
	}
	page := collectionapp.CollectionListPage{Items: result}
	if len(result) > query.Limit {
		last := result[query.Limit-1]
		page.Items = result[:query.Limit]
		updatedAt := last.UpdatedAt.UTC().Format(time.RFC3339Nano)
		id := string(last.ID)
		page.NextCursor, err = r.cursor.Encode(collectionapp.ResultCursor{
			Scope: collectionapp.CursorScopeList, WorkspaceID: query.WorkspaceID,
			QueryHash: statusHash, SortHash: sortHash, Limit: query.Limit, RevisionHash: revisionHash,
			LastObjectType: "COLLECTION", LastID: last.ID, LastSortValues: []*string{&updatedAt, &id},
		})
		if err != nil {
			return collectionapp.CollectionListPage{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return collectionapp.CollectionListPage{}, classify(err)
	}
	return page, nil
}

// SearchCollections searches active Smart Collections within one Workspace and a strict result bound.
func (r *Repository) SearchCollections(ctx context.Context, query collectionapp.CollectionSearchQuery) ([]collectionapp.Collection, error) {
	if r == nil || r.db == nil {
		return nil, unavailable(errors.New("collection repository is unavailable"))
	}
	if !validID(query.WorkspaceID) || query.Query == "" || query.Limit < 1 || query.Limit > collectionapp.MaxCollectionSearchLimit {
		return nil, requestInvalid(errors.New("collection search request is invalid"))
	}
	rows, err := r.db.Query(ctx, collectionSelect+`
		WHERE workspace_id=$1 AND status='ACTIVE'
		  AND (position($2 in normalized_name)>0 OR position($2 in lower(description))>0)
		ORDER BY CASE
			WHEN normalized_name=$2 THEN 0
			WHEN position($2 in normalized_name)=1 THEN 1
			ELSE 2 END,
			updated_at DESC,id DESC
		LIMIT $3`, string(query.WorkspaceID), query.Query, query.Limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	items := make([]collectionapp.Collection, 0, query.Limit)
	for rows.Next() {
		item, scanErr := scanCollection(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return items, nil
}

func canonicalCollectionStatuses(statuses []collectionapp.CollectionStatus) []string {
	if len(statuses) == 0 {
		return []string{string(collectionapp.CollectionStatusActive)}
	}
	unique := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		unique[string(status)] = struct{}{}
	}
	values := make([]string, 0, len(unique))
	for status := range unique {
		values = append(values, status)
	}
	sort.Strings(values)
	return values
}

func collectionListRevision(ctx context.Context, db DB, workspaceID foundation.ID, statuses []string) (string, error) {
	var count int64
	var updatedAt *time.Time
	var digest string
	if err := db.QueryRow(ctx, `SELECT count(*),max(updated_at),md5(COALESCE(string_agg(id::text || ':' || version::text || ':' || status, E'\n' ORDER BY id),'')) FROM learning.smart_collection WHERE workspace_id=$1 AND status=ANY($2::text[])`, string(workspaceID), statuses).Scan(&count, &updatedAt, &digest); err != nil {
		return "", classify(err)
	}
	updated := ""
	if updatedAt != nil {
		updated = updatedAt.UTC().Format(time.RFC3339Nano)
	}
	return collectionListHash("collection-list-revision/v1", fmt.Sprintf("%d", count), updated, digest), nil
}

func collectionListHash(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(digest[:])
}

type commandReceipt struct {
	workspaceID                              foundation.ID
	idempotencyKey, requestHash, commandType string
	collectionID                             foundation.ID
	collectionVersion                        int64
	collection                               collectionapp.Collection
}

func commandResult(collection collectionapp.Collection, receipt commandReceipt, replayed bool) collectionapp.CommandResult {
	if replayed {
		collection = receipt.collection
	}
	return collectionapp.CommandResult{Collection: collection, CommandVersion: receipt.collectionVersion, RequestHash: receipt.requestHash, CommandType: receipt.commandType, Replayed: replayed}
}

func loadCommand(ctx context.Context, db DB, workspaceID foundation.ID, key string) (commandReceipt, bool, error) {
	var receipt commandReceipt
	var workspace, collectionID string
	var receiptRaw []byte
	query := `SELECT workspace_id::text,request_hash,command_type,collection_id::text,collection_version,receipt FROM learning.smart_collection_command WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`
	var row pgx.Row
	if database, ok := db.(*gormDB); ok {
		row = database.receiptRow(ctx, query, string(workspaceID), key)
	} else {
		row = db.QueryRow(ctx, query, string(workspaceID), key)
	}
	err := row.Scan(&workspace, &receipt.requestHash, &receipt.commandType, &collectionID, &receipt.collectionVersion, &receiptRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return commandReceipt{}, false, nil
	}
	if err != nil {
		return commandReceipt{}, false, classify(err)
	}
	receipt.workspaceID = foundation.ID(workspace)
	receipt.collectionID = foundation.ID(collectionID)
	receipt.idempotencyKey = key
	// The database columns and the immutable receipt are two independent
	// bindings. Require both to agree before replaying anything.
	payload, err := decodeReceiptPayload(receiptRaw)
	if err != nil {
		return commandReceipt{}, false, err
	}
	if err := validateCommandReceiptPayload(payload, key, receipt); err != nil {
		return commandReceipt{}, false, err
	}
	receipt.collection = payload.Collection
	return receipt, true, nil
}

func lockWorkspace(ctx context.Context, db DB, workspaceID foundation.ID) error {
	var id string
	err := db.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR UPDATE`, string(workspaceID)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(errors.New("collection workspace not found"))
	}
	if err != nil {
		return classify(err)
	}
	return nil
}

func loadCollection(ctx context.Context, db DB, workspaceID, collectionID foundation.ID, forUpdate bool) (collectionapp.Collection, error) {
	query := collectionSelect + ` WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var row pgx.Row
	if database, ok := db.(*gormDB); ok {
		row = database.collectionRow(ctx, query, string(workspaceID), string(collectionID))
	} else {
		row = db.QueryRow(ctx, query, string(workspaceID), string(collectionID))
	}
	item, err := scanCollection(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return collectionapp.Collection{}, notFound(errors.New("collection not found"))
	}
	if err == nil && (item.WorkspaceID != workspaceID || item.ID != collectionID) {
		return collectionapp.Collection{}, inconsistent(errors.New("collection query returned a different identity"))
	}
	return item, err
}

func scanCollection(row rowScanner) (collectionapp.Collection, error) {
	var item collectionapp.Collection
	var id, workspace, schema, viewType, status string
	var queryRaw, viewRaw []byte
	if err := row.Scan(&id, &workspace, &item.Name, &item.NormalizedName, &item.Description, &schema, &item.QueryVersion, &queryRaw, &item.QueryHash, &viewType, &viewRaw, &status, &item.CachedResultVersion, &item.LastExecutedAt, &item.Version, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return collectionapp.Collection{}, err
		}
		return collectionapp.Collection{}, classify(err)
	}
	item.ID, item.WorkspaceID, item.QuerySchemaVersion = foundation.ID(id), foundation.ID(workspace), schema
	item.ViewType, item.Status = domain.ViewType(viewType), collectionapp.CollectionStatus(status)
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	if item.LastExecutedAt != nil {
		value := item.LastExecutedAt.UTC()
		item.LastExecutedAt = &value
	}
	if !validID(item.ID) || !validID(item.WorkspaceID) || item.Name == "" || item.NormalizedName == "" ||
		item.QuerySchemaVersion != domain.QuerySchemaVersionV1 || item.QueryVersion < 1 || item.Version < 1 ||
		(item.Status != collectionapp.CollectionStatusActive && item.Status != collectionapp.CollectionStatusArchived) ||
		item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) {
		return collectionapp.Collection{}, inconsistent(errors.New("persisted collection shape is invalid"))
	}
	if !isJSONObject(queryRaw) || !isJSONObject(viewRaw) {
		return collectionapp.Collection{}, inconsistent(errors.New("persisted collection JSON shape is invalid"))
	}
	if err := json.Unmarshal(queryRaw, &item.Query); err != nil {
		return collectionapp.Collection{}, inconsistent(err)
	}
	if err := json.Unmarshal(viewRaw, &item.ViewConfig); err != nil {
		return collectionapp.Collection{}, inconsistent(err)
	}
	canonicalQuery, err := domain.CanonicalizeQuery(item.Query)
	if err != nil || canonicalQuery.Hash != item.QueryHash {
		if err == nil {
			err = errors.New("stored collection query hash does not match canonical query")
		}
		return collectionapp.Collection{}, inconsistent(err)
	}
	if _, err := domain.CanonicalizeViewConfig(item.ViewType, item.ViewConfig); err != nil {
		return collectionapp.Collection{}, inconsistent(err)
	}
	return item, nil
}

func isJSONObject(raw []byte) bool {
	trimmed := strings.TrimSpace(string(raw))
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid(raw)
}

type rowScanner interface{ Scan(...any) error }

func writeTx[T any](ctx context.Context, repository *Repository, fn func(DB) (T, error)) (T, error) {
	var zero T
	tx, err := repository.beginner.Begin(ctx)
	if err != nil {
		return zero, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, classify(err)
	}
	return result, nil
}

const collectionColumnsForInsert = `id,workspace_id,name,normalized_name,description,query_schema_version,query_version,query_definition,query_hash,view_type,view_config,status,cached_result_version,last_executed_at,version,created_at,updated_at`

func collectionArgs(c collectionapp.Collection) []any {
	return []any{string(c.ID), string(c.WorkspaceID), c.Name, c.NormalizedName, c.Description, c.QuerySchemaVersion, c.QueryVersion, queryDefinition(c), c.QueryHash, string(c.ViewType), viewConfig(c), string(c.Status), c.CachedResultVersion, c.LastExecutedAt, c.Version, c.CreatedAt.UTC(), c.UpdatedAt.UTC()}
}
func queryDefinition(c collectionapp.Collection) []byte {
	value, _ := json.Marshal(c.Query)
	return value
}
func viewConfig(c collectionapp.Collection) []byte {
	value, _ := json.Marshal(c.ViewConfig)
	return value
}

type commandReceiptPayload struct {
	SchemaVersion  string                   `json:"schema_version"`
	WorkspaceID    foundation.ID            `json:"workspace_id"`
	IdempotencyKey string                   `json:"idempotency_key"`
	RequestHash    string                   `json:"request_hash"`
	SnapshotHash   string                   `json:"snapshot_hash"`
	CommandType    string                   `json:"command_type"`
	CollectionID   foundation.ID            `json:"collection_id"`
	Version        int64                    `json:"version"`
	Collection     collectionapp.Collection `json:"collection"`
}

func commandReceiptJSON(collection collectionapp.Collection, idempotencyKey, commandType, requestHash string) []byte {
	snapshotHash, _ := collectionSnapshotHash(collection)
	value, _ := json.Marshal(commandReceiptPayload{
		SchemaVersion: "collection-command-receipt/v2",
		WorkspaceID:   collection.WorkspaceID, IdempotencyKey: idempotencyKey,
		RequestHash: requestHash, SnapshotHash: snapshotHash, CommandType: commandType,
		CollectionID: collection.ID, Version: collection.Version, Collection: collection,
	})
	return value
}

func decodeReceiptPayload(raw []byte) (commandReceiptPayload, error) {
	var payload commandReceiptPayload
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return commandReceiptPayload{}, inconsistent(fmt.Errorf("decode collection command receipt: %w", err))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return commandReceiptPayload{}, inconsistent(errors.New("collection command receipt has trailing JSON"))
		}
		return commandReceiptPayload{}, inconsistent(fmt.Errorf("decode collection command receipt suffix: %w", err))
	}
	if payload.SchemaVersion != "collection-command-receipt/v2" {
		return commandReceiptPayload{}, inconsistent(errors.New("collection command receipt schema is unsupported"))
	}
	return payload, nil
}

func validateReceiptCollection(collection collectionapp.Collection, receipt commandReceipt) error {
	if !validID(collection.ID) || !validID(collection.WorkspaceID) || collection.ID != receipt.collectionID || collection.WorkspaceID != receipt.workspaceID || collection.Version != receipt.collectionVersion || collection.QuerySchemaVersion != domain.QuerySchemaVersionV1 || collection.QueryVersion < 1 || collection.Name == "" || collection.NormalizedName == "" || !collection.CreatedAt.UTC().Equal(collection.CreatedAt) || !collection.UpdatedAt.UTC().Equal(collection.UpdatedAt) {
		return inconsistent(errors.New("collection command receipt snapshot identity is invalid"))
	}
	if receipt.commandType == "ARCHIVE" {
		if collection.Status != collectionapp.CollectionStatusArchived {
			return inconsistent(errors.New("archived collection command receipt snapshot is invalid"))
		}
	} else if collection.Status != collectionapp.CollectionStatusActive {
		return inconsistent(errors.New("active collection command receipt snapshot is invalid"))
	}
	canonical, err := domain.CanonicalizeQuery(collection.Query)
	if err != nil || canonical.Hash != collection.QueryHash {
		if err == nil {
			err = errors.New("collection command receipt query hash does not match canonical query")
		}
		return inconsistent(err)
	}
	if _, err := domain.CanonicalizeViewConfig(collection.ViewType, collection.ViewConfig); err != nil {
		return inconsistent(fmt.Errorf("collection command receipt view config is invalid: %w", err))
	}
	return nil
}

func validateCommandReceiptPayload(payload commandReceiptPayload, key string, receipt commandReceipt) error {
	if payload.WorkspaceID != receipt.workspaceID || payload.IdempotencyKey != key || payload.RequestHash != receipt.requestHash || payload.CommandType != receipt.commandType || payload.CollectionID != receipt.collectionID || payload.Version != receipt.collectionVersion {
		return inconsistent(errors.New("collection command receipt binding is invalid"))
	}
	if !isHash(payload.SnapshotHash) {
		return inconsistent(errors.New("collection command receipt snapshot hash is invalid"))
	}
	hash, err := collectionSnapshotHash(payload.Collection)
	if err != nil || hash != payload.SnapshotHash {
		if err == nil {
			err = errors.New("collection command receipt snapshot hash mismatch")
		}
		return inconsistent(err)
	}
	if err := validateReceiptCollection(payload.Collection, receipt); err != nil {
		return err
	}
	var definition *collectionapp.Collection
	collectionID := receipt.collectionID
	expectedVersion := receipt.collectionVersion - 1
	if receipt.commandType == "CREATE" {
		definition = &payload.Collection
		collectionID = ""
		expectedVersion = 0
	} else if receipt.commandType == "UPDATE" {
		definition = &payload.Collection
	} else if receipt.commandType != "ARCHIVE" {
		return inconsistent(errors.New("collection command receipt type is invalid"))
	}
	requestHash, err := collectionapp.ComputeRequestHash(receipt.commandType, receipt.workspaceID, collectionID, expectedVersion, definition)
	if err != nil || requestHash != receipt.requestHash {
		if err == nil {
			err = errors.New("collection command receipt request hash mismatch")
		}
		return inconsistent(err)
	}
	return nil
}

func collectionSnapshotHash(collection collectionapp.Collection) (string, error) {
	encoded, err := json.Marshal(collection)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateCreateRecord(record collectionapp.CreateRecord) error {
	if !validCollection(record.Collection) || record.Collection.Version != 1 || record.Collection.Status != collectionapp.CollectionStatusActive {
		return requestInvalid(errors.New("create collection record is invalid"))
	}
	if _, err := normalizeKey(record.IdempotencyKey); err != nil {
		return err
	}
	if !isHash(record.RequestHash) {
		return requestInvalid(errors.New("collection request hash is invalid"))
	}
	return nil
}
func validateUpdateRecord(record collectionapp.UpdateRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.CollectionID) || record.ExpectedVersion < 1 || !validCollection(record.Collection) || record.Collection.ID != record.CollectionID || record.Collection.WorkspaceID != record.WorkspaceID {
		return requestInvalid(errors.New("update collection record is invalid"))
	}
	if _, err := normalizeKey(record.IdempotencyKey); err != nil {
		return err
	}
	if record.At.IsZero() {
		return requestInvalid(errors.New("update timestamp is required"))
	}
	if !isHash(record.RequestHash) {
		return requestInvalid(errors.New("collection request hash is invalid"))
	}
	return nil
}
func validateArchiveRecord(record collectionapp.ArchiveRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.CollectionID) || record.ExpectedVersion < 1 {
		return requestInvalid(errors.New("archive collection record is invalid"))
	}
	if _, err := normalizeKey(record.IdempotencyKey); err != nil {
		return err
	}
	if record.At.IsZero() {
		return requestInvalid(errors.New("archive timestamp is required"))
	}
	if !isHash(record.RequestHash) {
		return requestInvalid(errors.New("collection request hash is invalid"))
	}
	return nil
}
func validCollection(c collectionapp.Collection) bool {
	if !validID(c.ID) || !validID(c.WorkspaceID) || c.QuerySchemaVersion != domain.QuerySchemaVersionV1 || c.QueryVersion < 1 || c.Version < 1 || c.Status != collectionapp.CollectionStatusActive || c.Name == "" || c.NormalizedName == "" || !c.CreatedAt.UTC().Equal(c.CreatedAt) || !c.UpdatedAt.UTC().Equal(c.UpdatedAt) {
		return false
	}
	canonical, err := domain.CanonicalizeQuery(c.Query)
	if err != nil || canonical.Hash != c.QueryHash {
		return false
	}
	if _, err := domain.CanonicalizeViewConfig(c.ViewType, c.ViewConfig); err != nil {
		return false
	}
	return true
}
func validID(value foundation.ID) bool {
	_, err := foundation.ParseID(string(value))
	return err == nil
}
func isHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func normalizeKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n") {
		return "", requestInvalid(errors.New("collection idempotency key is invalid"))
	}
	return value, nil
}
