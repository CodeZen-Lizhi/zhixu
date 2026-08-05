// Package postgres 实现 Git 远端配置、运行、Attempt 与 Outbox 的 PostgreSQL 事实源。
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

// DB 是 Repository 所需的最小 pgx pool 契约。
type DB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// Repository 是 Git Sync 的 PostgreSQL 原子边界。
type Repository struct {
	db     DB
	sealer application.CredentialSealer
}

// NewRepository 创建不打开连接或执行迁移的 Repository。
func NewRepository(db DB, sealer application.CredentialSealer) (*Repository, error) {
	if nilInterface(db) || nilInterface(sealer) {
		return nil, unavailable("Git sync repository dependency is unavailable")
	}
	return &Repository{db: db, sealer: sealer}, nil
}

func (repository Repository) String() string {
	return fmt.Sprintf("Repository{database_configured:%t sealer_configured:%t}", !nilInterface(repository.db), !nilInterface(repository.sealer))
}

func (repository Repository) GoString() string { return repository.String() }

var (
	_ application.ConfigStore             = (*Repository)(nil)
	_ application.RunStore                = (*Repository)(nil)
	_ application.AutoSyncCandidateSource = (*Repository)(nil)
	_ application.FollowupStore           = (*Repository)(nil)
	_ application.OutboxStore             = (*Repository)(nil)
)

const configColumns = `workspace_id::text,configured,COALESCE(normalized_url,''),COALESCE(branch,''),
	auto_sync,token_configured,revision,created_at,updated_at`

const workspaceLockPurpose = "git-sync"

func scanConfig(row interface{ Scan(...any) error }) (domain.RemoteConfig, error) {
	var workspaceID string
	var config domain.RemoteConfig
	if err := row.Scan(
		&workspaceID, &config.Configured, &config.RemoteURL, &config.Branch, &config.AutoSync,
		&config.TokenConfigured, &config.Revision, &config.CreatedAt, &config.UpdatedAt,
	); err != nil {
		return domain.RemoteConfig{}, err
	}
	parsed, err := foundation.ParseID(workspaceID)
	if err != nil {
		return domain.RemoteConfig{}, corrupt("persisted Git remote Workspace is invalid")
	}
	config.WorkspaceID = parsed
	config.CreatedAt, config.UpdatedAt = config.CreatedAt.UTC(), config.UpdatedAt.UTC()
	if err := config.Validate(); err != nil {
		return domain.RemoteConfig{}, corrupt("persisted Git remote configuration is invalid")
	}
	return config, nil
}

const runColumns = `id::text,workspace_id::text,config_revision,remote_url,branch,trigger,
	COALESCE(retry_of_run_id::text,''),idempotency_key,request_hash,status,direction,failure_class,error_code,retryable,
	COALESCE(expected_head_oid,''),COALESCE(expected_remote_oid,''),COALESCE(verified_head_oid,''),
	COALESCE(verified_remote_oid,''),changed_files,index_status,index_error_code,index_retryable,
	COALESCE(index_version_id::text,''),attempt_count,version,created_at,updated_at,completed_at`

func scanRun(row interface{ Scan(...any) error }) (domain.SyncRun, error) {
	var id, workspaceID, retryOf, indexVersionID string
	var changedRaw []byte
	var completedAt sql.NullTime
	var run domain.SyncRun
	if err := row.Scan(
		&id, &workspaceID, &run.ConfigRevision, &run.RemoteURL, &run.Branch, &run.Trigger,
		&retryOf, &run.IdempotencyKey, &run.RequestHash, &run.Status, &run.Direction, &run.FailureClass,
		&run.ErrorCode, &run.Retryable, &run.ExpectedHeadOID, &run.ExpectedRemoteOID, &run.VerifiedHeadOID,
		&run.VerifiedRemoteOID, &changedRaw, &run.IndexStatus, &run.IndexErrorCode, &run.IndexRetryable,
		&indexVersionID, &run.AttemptCount, &run.Version, &run.CreatedAt, &run.UpdatedAt, &completedAt,
	); err != nil {
		return domain.SyncRun{}, err
	}
	parsedID, idErr := foundation.ParseID(id)
	parsedWorkspace, workspaceErr := foundation.ParseID(workspaceID)
	if idErr != nil || workspaceErr != nil {
		return domain.SyncRun{}, corrupt("persisted Git sync run identity is invalid")
	}
	run.ID, run.WorkspaceID = parsedID, parsedWorkspace
	if retryOf != "" {
		parsedRetry, err := foundation.ParseID(retryOf)
		if err != nil {
			return domain.SyncRun{}, corrupt("persisted Git sync retry binding is invalid")
		}
		run.RetryOfRunID = parsedRetry
	}
	if indexVersionID != "" {
		parsedIndexVersion, err := foundation.ParseID(indexVersionID)
		if err != nil {
			return domain.SyncRun{}, corrupt("persisted Git sync index binding is invalid")
		}
		run.IndexVersionID = parsedIndexVersion
	}
	if err := json.Unmarshal(changedRaw, &run.ChangedFiles); err != nil || run.ChangedFiles == nil {
		return domain.SyncRun{}, corrupt("persisted Git changed files are invalid")
	}
	run.CreatedAt, run.UpdatedAt = run.CreatedAt.UTC(), run.UpdatedAt.UTC()
	if completedAt.Valid {
		value := completedAt.Time.UTC()
		run.CompletedAt = &value
	}
	if err := run.Validate(); err != nil {
		return domain.SyncRun{}, corrupt("persisted Git sync run is invalid")
	}
	return run, nil
}

const attemptColumns = `id::text,workspace_id::text,run_id::text,attempt_no,status,phase,
	COALESCE(expected_head_oid,''),COALESCE(expected_remote_oid,''),result_known,error_code,retryable,
	COALESCE(lease_owner,''),lease_expires_at,started_at,completed_at,version`

func scanAttempt(row interface{ Scan(...any) error }) (domain.SyncAttempt, error) {
	var id, workspaceID, runID string
	var resultKnown sql.NullBool
	var leaseExpiresAt, completedAt sql.NullTime
	var attempt domain.SyncAttempt
	if err := row.Scan(
		&id, &workspaceID, &runID, &attempt.AttemptNo, &attempt.Status, &attempt.Phase,
		&attempt.ExpectedHeadOID, &attempt.ExpectedRemoteOID, &resultKnown, &attempt.ErrorCode, &attempt.Retryable,
		&attempt.LeaseOwner, &leaseExpiresAt, &attempt.StartedAt, &completedAt, &attempt.Version,
	); err != nil {
		return domain.SyncAttempt{}, err
	}
	parsedID, idErr := foundation.ParseID(id)
	parsedWorkspace, workspaceErr := foundation.ParseID(workspaceID)
	parsedRun, runErr := foundation.ParseID(runID)
	if idErr != nil || workspaceErr != nil || runErr != nil {
		return domain.SyncAttempt{}, corrupt("persisted Git sync attempt identity is invalid")
	}
	attempt.ID, attempt.WorkspaceID, attempt.RunID = parsedID, parsedWorkspace, parsedRun
	if resultKnown.Valid {
		value := resultKnown.Bool
		attempt.ResultKnown = &value
	}
	if leaseExpiresAt.Valid {
		attempt.LeaseExpiresAt = leaseExpiresAt.Time.UTC()
	}
	attempt.StartedAt = attempt.StartedAt.UTC()
	if completedAt.Valid {
		value := completedAt.Time.UTC()
		attempt.CompletedAt = &value
	}
	if attempt.AttemptNo < 1 || attempt.Version < 1 || attempt.StartedAt.IsZero() {
		return domain.SyncAttempt{}, corrupt("persisted Git sync attempt is invalid")
	}
	return attempt, nil
}

func marshalChanges(changes []domain.FileChange) ([]byte, error) {
	if changes == nil {
		changes = []domain.FileChange{}
	}
	if len(changes) > domain.MaxChangedFiles {
		return nil, invalid("Git changed file list exceeds the limit")
	}
	for _, change := range changes {
		if err := change.Validate(); err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(changes)
	if err != nil || len(encoded) > 1024*1024 {
		return nil, invalid("Git changed file list is invalid")
	}
	return encoded, nil
}

func lockWorkspace(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, purpose string) error {
	key := string(workspaceID) + ":" + purpose
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return classify(err, domain.ErrorCodeUnavailable)
	}
	return nil
}

func classify(err error, code string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "40001", "40P01", "55P03", "57014", "53300":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		case "23503":
			return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeRunNotFound, false, err)
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRunActive, false, err)
		case "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, errors.New(message))
}

func unavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, errors.New(message))
}

func corrupt(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, errors.New(message))
}

func notFound(message string) error {
	return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeRunNotFound, false, errors.New(message))
}

func versionConflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}

func leaseLost(message string) error {
	return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeLeaseLost, true, errors.New(message))
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validText(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maximum && !strings.ContainsAny(value, "\x00\r\n")
}

func nilInterface(value any) bool {
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

func commit(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return classify(err, domain.ErrorCodeUnavailable)
	}
	return nil
}
