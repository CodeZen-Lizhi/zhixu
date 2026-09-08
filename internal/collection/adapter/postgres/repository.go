package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const collectionColumns = `id::text,workspace_id::text,name,normalized_name,description,
	query_schema_version,query_version,query_definition,query_hash,view_type,view_config,status,
	cached_result_version,last_executed_at,version,created_at,updated_at`

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

func collectionListRevision(ctx context.Context, db *gorm.DB, workspaceID foundation.ID, statuses []string) (string, error) {
	var count int64
	var updatedAt *time.Time
	var digest string
	if err := db.WithContext(ctx).Raw(`SELECT count(*),max(updated_at),md5(COALESCE(string_agg(id::text || ':' || version::text || ':' || status, E'\n' ORDER BY id),'')) FROM learning.smart_collection WHERE workspace_id=? AND status=ANY(?::text[])`, string(workspaceID), pq.Array(statuses)).Row().Scan(&count, &updatedAt, &digest); err != nil {
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

func loadCommand(ctx context.Context, db *gorm.DB, workspaceID foundation.ID, key string) (commandReceipt, bool, error) {
	var receipt commandReceipt
	var workspace, collectionID string
	var receiptRaw collectionJSONB
	row := db.WithContext(ctx).Model(&collectionCommandModel{}).
		Select("workspace_id::text,request_hash,command_type,collection_id::text,collection_version,receipt").
		Where("workspace_id = ? AND idempotency_key = ?", string(workspaceID), key).
		Clauses(clause.Locking{Strength: "UPDATE"}).Row()
	err := row.Scan(&workspace, &receipt.requestHash, &receipt.commandType, &collectionID, &receipt.collectionVersion, &receiptRaw)
	if errors.Is(err, sql.ErrNoRows) {
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

func lockWorkspace(ctx context.Context, db *gorm.DB, workspaceID foundation.ID) error {
	var id string
	err := db.WithContext(ctx).Table("core.workspace").Select("id::text").Where("id = ?", string(workspaceID)).Clauses(clause.Locking{Strength: "UPDATE"}).Row().Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound(errors.New("collection workspace not found"))
	}
	if err != nil {
		return classify(err)
	}
	return nil
}

func loadCollection(ctx context.Context, db *gorm.DB, workspaceID, collectionID foundation.ID, forUpdate bool) (collectionapp.Collection, error) {
	query := db.WithContext(ctx).Model(&collectionModel{}).Select(collectionColumns).
		Where("workspace_id = ? AND id = ?", string(workspaceID), string(collectionID))
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	row := query.Row()
	item, err := scanCollection(row)
	if errors.Is(err, sql.ErrNoRows) {
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
	var queryRaw, viewRaw collectionJSONB
	if err := row.Scan(&id, &workspace, &item.Name, &item.NormalizedName, &item.Description, &schema, &item.QueryVersion, &queryRaw, &item.QueryHash, &viewType, &viewRaw, &status, &item.CachedResultVersion, &item.LastExecutedAt, &item.Version, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
