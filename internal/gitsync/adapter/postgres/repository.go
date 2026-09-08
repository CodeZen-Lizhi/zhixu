package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	"reflect"
	"strings"
)

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
