// Package postgres 提供异步导出任务的参数化 PostgreSQL 持久化实现。
package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

type exportRow interface {
	Scan(...any) error
}

type exportRows interface {
	Close()
	Err() error
	Next() bool
	Scan(...any) error
}

type exportTransaction interface {
	Exec(context.Context, string, ...any) error
	Query(context.Context, string, ...any) (exportRows, error)
	QueryRow(context.Context, string, ...any) exportRow
	Commit(context.Context) error
	Rollback(context.Context) error
	SideFactTransaction() any
}

type exportDatabase interface {
	Query(context.Context, string, ...any) (exportRows, error)
	Begin(context.Context) (exportTransaction, error)
}

// Option 配置同事务的 Export side-fact appender。
type Option func(*Repository) error

// WithEventAppender 配置 Export 生命周期 Server Event 追加器。
func WithEventAppender(appender eventsapplication.Appender) Option {
	return func(repository *Repository) error {
		if isNilDependency(appender) {
			return invalid(errors.New("export event appender is nil"))
		}
		if !isNilDependency(repository.events) {
			return invalid(errors.New("export event appender is duplicated"))
		}
		repository.events = appender
		return nil
	}
}

// WithAuditAppender 配置下载统计同事务 Audit 追加器。
func WithAuditAppender(appender auditapplication.Appender) Option {
	return func(repository *Repository) error {
		if isNilDependency(appender) {
			return invalid(errors.New("export audit appender is nil"))
		}
		if !isNilDependency(repository.audit) {
			return invalid(errors.New("export audit appender is duplicated"))
		}
		repository.audit = appender
		return nil
	}
}

// Repository 持久化导出 Job，并执行 DB-time 租约、prepared 和 cleanup CAS。
type Repository struct {
	db     exportDatabase
	events eventsapplication.Appender
	audit  auditapplication.Appender
}

func newRepository(db exportDatabase, options ...Option) (*Repository, error) {
	if isNilDependency(db) {
		return nil, unavailable(errors.New("export database is nil"))
	}
	repository := &Repository{db: db}
	for _, option := range options {
		if option == nil {
			return nil, invalid(errors.New("export repository option is nil"))
		}
		if err := option(repository); err != nil {
			return nil, err
		}
	}
	return repository, nil
}

var _ exportapp.Repository = (*Repository)(nil)

const selectColumns = `id::text,workspace_id::text,kind,schema_version,query_definition,fields,status,
file_path,file_hash,error_code,idempotency_key,request_hash,request_ttl_seconds,version,
expires_at,created_at,updated_at,completed_at,collection_id::text,collection_version,query_hash,
redaction_policy,include_sensitive,permission_scope,requested_by,read_model_revision,exact_count,
prepared_at,prepared_staging_path,file_size,error_message,attempt_count,lease_owner,lease_expires_at,
started_at,download_count,last_downloaded_at,cleanup_status,cleanup_attempt_count,cleanup_error,
cleanup_updated_at,file_deleted_at,scope_kind,attachment_root_contract_version,
manifest_sha256,entry_count,total_uncompressed_bytes`

func scanJob(row interface{ Scan(...any) error }) (domain.Job, error) {
	var (
		id, workspaceID, kind, schemaVersion, status                               string
		queryDefinition, fields                                                    []byte
		filePath, fileHash, errorCode, collectionID, queryHash                     sql.NullString
		readModelRevision, preparedStagingPath, errorMessage, leaseOwner           sql.NullString
		cleanupError, attachmentRootVersion, manifestHash                          sql.NullString
		idempotencyKey, requestHash, redactionPolicy, permissionScope, requestedBy string
		scopeKind                                                                  string
		requestTTLSeconds, version, fileSize                                       int64
		collectionVersion, exactCount, entryCount, totalUncompressedBytes          sql.NullInt64
		attemptCount, downloadCount, cleanupAttemptCount                           int
		includeSensitive                                                           bool
		expiresAt, createdAt, updatedAt                                            time.Time
		completedAt, preparedAt, leaseExpiresAt, startedAt, lastDownloadedAt       sql.NullTime
		cleanupUpdatedAt, fileDeletedAt                                            sql.NullTime
		cleanupStatus                                                              string
	)
	if err := row.Scan(
		&id, &workspaceID, &kind, &schemaVersion, &queryDefinition, &fields, &status,
		&filePath, &fileHash, &errorCode, &idempotencyKey, &requestHash, &requestTTLSeconds, &version,
		&expiresAt, &createdAt, &updatedAt, &completedAt, &collectionID, &collectionVersion, &queryHash,
		&redactionPolicy, &includeSensitive, &permissionScope, &requestedBy, &readModelRevision, &exactCount,
		&preparedAt, &preparedStagingPath, &fileSize, &errorMessage, &attemptCount, &leaseOwner, &leaseExpiresAt,
		&startedAt, &downloadCount, &lastDownloadedAt, &cleanupStatus, &cleanupAttemptCount, &cleanupError,
		&cleanupUpdatedAt, &fileDeletedAt, &scopeKind, &attachmentRootVersion,
		&manifestHash, &entryCount, &totalUncompressedBytes,
	); err != nil {
		return domain.Job{}, err
	}
	parsedID, err := foundation.ParseID(id)
	if err != nil {
		return domain.Job{}, err
	}
	parsedWorkspaceID, err := foundation.ParseID(workspaceID)
	if err != nil {
		return domain.Job{}, err
	}
	job := domain.Job{
		ID: parsedID, WorkspaceID: parsedWorkspaceID, Kind: domain.Kind(kind), SchemaVersion: schemaVersion,
		Redaction: domain.RedactionPolicy(redactionPolicy), IncludeSensitive: includeSensitive,
		PermissionScope: permissionScope, RequestedBy: requestedBy, IdempotencyKey: idempotencyKey,
		RequestHash: requestHash, RequestTTLSeconds: requestTTLSeconds, Status: domain.Status(status), Version: version,
		FileSize: fileSize, AttemptCount: attemptCount, DownloadCount: downloadCount,
		ExpiresAt: expiresAt.UTC(), CreatedAt: createdAt.UTC(), UpdatedAt: updatedAt.UTC(),
		CleanupStatus: domain.CleanupStatus(cleanupStatus), CleanupAttemptCount: cleanupAttemptCount,
		Scope: domain.Scope{Kind: domain.ScopeKind(scopeKind), AttachmentRootContractVersion: attachmentRootVersion.String},
	}
	if err := json.Unmarshal(fields, &job.Fields); err != nil {
		return domain.Job{}, err
	}
	var definition struct {
		CollectionID      *string `json:"collection_id"`
		CollectionVersion *int64  `json:"collection_version"`
		QueryHash         string  `json:"query_hash"`
	}
	if err := json.Unmarshal(queryDefinition, &definition); err != nil {
		return domain.Job{}, err
	}
	if collectionID.Valid {
		parsed, parseErr := foundation.ParseID(collectionID.String)
		if parseErr != nil {
			return domain.Job{}, parseErr
		}
		job.Scope.CollectionID = &parsed
	} else if definition.CollectionID != nil {
		parsed, parseErr := foundation.ParseID(*definition.CollectionID)
		if parseErr != nil {
			return domain.Job{}, parseErr
		}
		job.Scope.CollectionID = &parsed
	}
	if collectionVersion.Valid {
		value := collectionVersion.Int64
		job.Scope.CollectionVersion = &value
	} else {
		job.Scope.CollectionVersion = definition.CollectionVersion
	}
	if queryHash.Valid {
		job.Scope.QueryHash = queryHash.String
	} else {
		job.Scope.QueryHash = definition.QueryHash
	}
	job.FilePath = filePath.String
	job.FileHash = fileHash.String
	job.ErrorCode = errorCode.String
	job.ReadModelRevision = readModelRevision.String
	job.PreparedStagingPath = preparedStagingPath.String
	job.ErrorMessage = errorMessage.String
	job.LeaseOwner = leaseOwner.String
	job.CleanupError = cleanupError.String
	job.ManifestHash = manifestHash.String
	if exactCount.Valid {
		value := exactCount.Int64
		job.ExactCount = &value
	}
	if entryCount.Valid {
		value := entryCount.Int64
		job.EntryCount = &value
	}
	if totalUncompressedBytes.Valid {
		value := totalUncompressedBytes.Int64
		job.TotalUncompressedBytes = &value
	}
	assignTime := func(source sql.NullTime, target **time.Time) {
		if source.Valid {
			value := source.Time.UTC()
			*target = &value
		}
	}
	assignTime(completedAt, &job.CompletedAt)
	assignTime(preparedAt, &job.PreparedAt)
	assignTime(leaseExpiresAt, &job.LeaseExpiresAt)
	assignTime(startedAt, &job.StartedAt)
	assignTime(lastDownloadedAt, &job.LastDownloadedAt)
	assignTime(cleanupUpdatedAt, &job.CleanupUpdatedAt)
	assignTime(fileDeletedAt, &job.FileDeletedAt)
	if err := job.Validate(); err != nil {
		return domain.Job{}, err
	}
	return job, nil
}

func encodeJobDefinition(job domain.Job) ([]byte, []byte, error) {
	definition := struct {
		ScopeKind                     domain.ScopeKind `json:"scope_kind"`
		CollectionID                  *foundation.ID   `json:"collection_id"`
		CollectionVersion             *int64           `json:"collection_version"`
		QueryHash                     string           `json:"query_hash"`
		AttachmentRootContractVersion string           `json:"attachment_root_contract_version,omitempty"`
	}{job.Scope.Kind, job.Scope.CollectionID, job.Scope.CollectionVersion, job.Scope.QueryHash, job.Scope.AttachmentRootContractVersion}
	queryDefinition, err := json.Marshal(definition)
	if err != nil {
		return nil, nil, err
	}
	fieldValues := job.Fields
	if fieldValues == nil {
		fieldValues = []domain.Field{}
	}
	fields, err := json.Marshal(fieldValues)
	if err != nil {
		return nil, nil, err
	}
	return queryDefinition, fields, nil
}

type cursorValue struct {
	WorkspaceID  foundation.ID
	ScopeKind    domain.ScopeKind
	CollectionID *foundation.ID
	Limit        int
	CreatedAt    time.Time
	ID           foundation.ID
}

type cursorEnvelope struct {
	Version      int              `json:"version"`
	Kind         string           `json:"kind"`
	WorkspaceID  foundation.ID    `json:"workspace_id"`
	ScopeKind    domain.ScopeKind `json:"scope_kind"`
	CollectionID *foundation.ID   `json:"collection_id,omitempty"`
	Limit        int              `json:"limit"`
	CreatedAt    time.Time        `json:"created_at"`
	ID           foundation.ID    `json:"id"`
}

func encodeCursor(value cursorValue) string {
	raw, _ := json.Marshal(cursorEnvelope{
		Version: 1, Kind: "export-list", WorkspaceID: value.WorkspaceID,
		ScopeKind: value.ScopeKind, CollectionID: cloneID(value.CollectionID), Limit: value.Limit,
		CreatedAt: value.CreatedAt.UTC(), ID: value.ID,
	})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(value string, workspaceID foundation.ID, scopeKind domain.ScopeKind, collectionID *foundation.ID, limit int) (cursorValue, error) {
	if len(value) > 4096 {
		return cursorValue{}, errors.New("export cursor is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursorValue{}, errors.New("export cursor is invalid")
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = 2048
	parsed, err := strictjson.DecodeObject[cursorEnvelope](raw, limits, nil)
	if err != nil || parsed.Version != 1 || parsed.Kind != "export-list" || parsed.WorkspaceID != workspaceID ||
		parsed.ScopeKind != scopeKind || !sameOptionalID(parsed.CollectionID, collectionID) || parsed.Limit != limit || parsed.CreatedAt.IsZero() {
		return cursorValue{}, errors.New("export cursor is invalid")
	}
	if !validID(parsed.WorkspaceID) || !validID(parsed.ID) || parsed.CollectionID != nil && !validID(*parsed.CollectionID) {
		return cursorValue{}, errors.New("export cursor is invalid")
	}
	return cursorValue{
		WorkspaceID: parsed.WorkspaceID, ScopeKind: parsed.ScopeKind, CollectionID: cloneID(parsed.CollectionID), Limit: parsed.Limit,
		CreatedAt: parsed.CreatedAt.UTC(), ID: parsed.ID,
	}, nil
}

func cloneID(value *foundation.ID) *foundation.ID {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func sameOptionalID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func databaseNow(ctx context.Context, tx exportTransaction) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, classify(err, true)
	}
	return now.UTC(), nil
}

func isNilDependency(value any) bool {
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

func classify(err error, retryable bool) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		return notFound(err)
	}
	var postgresError interface{ SQLState() string }
	if errors.As(err, &postgresError) {
		switch postgresError.SQLState() {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, "EXPORT_DATABASE_UNAVAILABLE", true, err)
		case "23503", "23505", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, "EXPORT_DATABASE_CONSISTENCY", false, err)
		}
	}
	if retryable {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_DATABASE_UNAVAILABLE", true, err)
	}
	return foundation.NewError(foundation.ErrorConsistencyViolation, "EXPORT_DATABASE_CONSISTENCY", false, err)
}

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, cause)
}

func unavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, cause)
}

func notFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeNotFound, false, cause)
}

func idempotencyConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeConflict, false, cause)
}

func expired(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeExpired, false, cause)
}

func leaseLost(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "EXPORT_LEASE_LOST", true, cause)
}

func versionConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "EXPORT_VERSION_CONFLICT", true, cause)
}

func resultInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeResultInvalid, false, cause)
}

func boundedText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) > maximum {
		value = value[:maximum]
	}
	return value
}
