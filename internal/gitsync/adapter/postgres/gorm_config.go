package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const (
	gormClockTimestampSQL = `SELECT clock_timestamp()`

	gormInsertConfigRevisionSQL = `INSERT INTO ops.git_remote_config_revision(
	workspace_id,revision,configured,normalized_url,branch,auto_sync,token_configured,actor,config_created_at,created_at
) VALUES(?,?,true,?,?,?,?,?,?,clock_timestamp()) RETURNING created_at`

	gormUpdateConfigSQL = `UPDATE ops.git_remote_config SET
	configured=true,normalized_url=?,branch=?,auto_sync=?,token_configured=?,revision=?,updated_at=?
	WHERE workspace_id=? AND revision=?`

	gormInsertConfigSQL = `INSERT INTO ops.git_remote_config(
	workspace_id,configured,normalized_url,branch,auto_sync,token_configured,revision,created_at,updated_at
) VALUES(?,true,?,?,?,?,?,?,?)`

	gormInsertCredentialSQL = `INSERT INTO ops.git_remote_credential(
	workspace_id,config_revision,key_id,nonce,ciphertext,aad_digest,created_at,updated_at
) VALUES(?,?,?,?,?,?,clock_timestamp(),clock_timestamp())`

	gormInsertConfigReceiptSQL = `INSERT INTO ops.git_remote_command_receipt(
	workspace_id,idempotency_key,request_hash,command_type,expected_revision,result_revision,created_at
) VALUES(?,?,?,'SAVE',?,?,clock_timestamp())`

	gormInsertDeleteRevisionSQL = `INSERT INTO ops.git_remote_config_revision(
	workspace_id,revision,configured,normalized_url,branch,auto_sync,token_configured,actor,config_created_at,created_at
) VALUES(?, ?, false, NULL, NULL, false, false, ?, ?, clock_timestamp()) RETURNING created_at`

	gormDeleteConfigSQL = `UPDATE ops.git_remote_config SET
	configured=false,normalized_url=NULL,branch=NULL,auto_sync=false,token_configured=false,
	revision=?,updated_at=? WHERE workspace_id=? AND revision=?`

	gormInsertDeleteReceiptSQL = `INSERT INTO ops.git_remote_command_receipt(
	workspace_id,idempotency_key,request_hash,command_type,expected_revision,result_revision,created_at
) VALUES(?,?,?,'DELETE',?,?,clock_timestamp())`

	gormDeleteCredentialSQL = `DELETE FROM ops.git_remote_credential WHERE workspace_id=?`

	gormFenceConfigRunSQL = `UPDATE ops.git_sync_run SET
	status='STALE',failure_class='STALE_CONFIG',error_code=?,retryable=false,
	completed_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=? AND status='PENDING'`
)

// GetConfig returns the current non-secret configuration projection.
func (repository *GORMRepository) GetConfig(ctx context.Context, workspaceID foundation.ID) (domain.RemoteConfig, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.RemoteConfig{}, err
	}
	if !validID(workspaceID) {
		return domain.RemoteConfig{}, invalid("Git remote configuration query is invalid")
	}
	row, err := gormRawRow(ctx, repository.database, gormGetConfigSQL, string(workspaceID))
	if err != nil {
		return domain.RemoteConfig{}, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	config, err := scanConfig(row)
	if gormNoRows(err) {
		return domain.RemoteConfig{WorkspaceID: workspaceID}, nil
	}
	if err != nil {
		return domain.RemoteConfig{}, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	return config, nil
}

// ReplayConfig returns an exact command receipt before any current-state read.
func (repository *GORMRepository) ReplayConfig(ctx context.Context, command application.ReplayConfigCommand) (application.ConfigReceipt, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return application.ConfigReceipt{}, false, err
	}
	if !validID(command.WorkspaceID) || !validText(command.IdempotencyKey, 128) || !validText(command.RequestHash, 64) ||
		(command.CommandType != "SAVE" && command.CommandType != "DELETE") {
		return application.ConfigReceipt{}, false, invalid("Git remote configuration replay query is invalid")
	}
	return gormLoadConfigReceipt(ctx, repository.database, command.WorkspaceID, command.IdempotencyKey, command.RequestHash, command.CommandType)
}

// SaveConfig atomically appends a configuration revision, projection,
// credential envelope and command receipt.
func (repository *GORMRepository) SaveConfig(ctx context.Context, command application.PersistConfigCommand) (application.ConfigReceipt, error) {
	if err := repository.ready(ctx); err != nil {
		return application.ConfigReceipt{}, err
	}
	if !validID(command.WorkspaceID) || command.ExpectedRevision < 0 || !validText(command.IdempotencyKey, 128) ||
		!validText(command.RequestHash, 64) || !validText(command.Actor, 256) || command.SecretAction.Validate() != nil {
		return application.ConfigReceipt{}, invalid("Git remote configuration write is invalid")
	}
	defer command.SecretAction.Value.Destroy()
	var result application.ConfigReceipt
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockWorkspace(callbackCtx, tx, command.WorkspaceID, workspaceLockPurpose); err != nil {
			return err
		}
		replayed, found, err := gormLoadConfigReceipt(callbackCtx, tx, command.WorkspaceID, command.IdempotencyKey, command.RequestHash, "SAVE")
		if err != nil {
			return err
		}
		if found {
			result = replayed
			return nil
		}
		current, currentFound, err := gormLockCurrentConfig(callbackCtx, tx, command.WorkspaceID)
		if err != nil {
			return err
		}
		currentRevision := int64(0)
		if currentFound {
			currentRevision = current.Revision
		}
		if currentRevision != command.ExpectedRevision {
			return versionConflict(domain.ErrorCodeConfigRevisionConflict, "Git remote configuration changed")
		}
		if err := gormFenceConfigMutation(callbackCtx, tx, command.WorkspaceID); err != nil {
			return err
		}
		nextRevision := command.ExpectedRevision + 1
		nextContext := domain.CredentialContext{
			WorkspaceID: command.WorkspaceID, Revision: nextRevision, RemoteURL: command.RemoteURL,
			Branch: command.Branch, SchemaVersion: domain.CredentialSchemaVersion,
		}
		var encrypted domain.EncryptedCredential
		switch command.SecretAction.Kind {
		case domain.SecretActionKeep:
			if !currentFound || !current.CredentialBindingMatches(command.RemoteURL, command.Branch) {
				return versionConflict(domain.ErrorCodeConfigRevisionConflict, "a kept token cannot be rebound or recovered")
			}
			currentEnvelope, loadErr := gormLoadCredentialEnvelope(callbackCtx, tx, current.WorkspaceID, current.Revision)
			if loadErr != nil {
				return loadErr
			}
			currentToken, openErr := repository.sealer.Open(currentEnvelope, domain.CredentialContext{
				WorkspaceID: current.WorkspaceID, Revision: current.Revision, RemoteURL: current.RemoteURL,
				Branch: current.Branch, SchemaVersion: domain.CredentialSchemaVersion,
			})
			if openErr != nil {
				return openErr
			}
			defer currentToken.Destroy()
			encrypted, err = repository.sealer.Seal(currentToken, nextContext)
		case domain.SecretActionReplace:
			replacementBytes := command.SecretAction.Value.Bytes()
			defer clear(replacementBytes)
			replacement, copyErr := domain.TokenFromBytes(replacementBytes)
			if copyErr != nil {
				return copyErr
			}
			defer replacement.Destroy()
			encrypted, err = repository.sealer.Seal(replacement, nextContext)
		case domain.SecretActionClear:
			encrypted = domain.EncryptedCredential{}
		default:
			return invalid("Git remote token action is invalid")
		}
		if err != nil {
			return err
		}
		if err := encrypted.Validate(); err != nil {
			return err
		}
		tokenConfigured := encrypted.Configured()
		createdAt := time.Time{}
		if currentFound {
			createdAt = current.CreatedAt
		} else {
			clockRow, clockErr := gormRawRow(callbackCtx, tx, gormClockTimestampSQL)
			if clockErr != nil {
				return classifyGORM(callbackCtx, clockErr, domain.ErrorCodeUnavailable)
			}
			if clockErr = clockRow.Scan(&createdAt); clockErr != nil {
				return classifyGORM(callbackCtx, clockErr, domain.ErrorCodeUnavailable)
			}
		}
		revisionRow, err := gormRawRow(callbackCtx, tx, gormInsertConfigRevisionSQL,
			string(command.WorkspaceID), nextRevision, command.RemoteURL, command.Branch, command.AutoSync,
			tokenConfigured, command.Actor, createdAt.UTC())
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		var revisionAt time.Time
		if err := revisionRow.Scan(&revisionAt); err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if currentFound {
			if _, err := gormExec(callbackCtx, tx, gormDeleteCredentialSQL, string(command.WorkspaceID)); err != nil {
				return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
			}
			rowsAffected, err := gormExec(callbackCtx, tx, gormUpdateConfigSQL,
				command.RemoteURL, command.Branch, command.AutoSync, tokenConfigured, nextRevision,
				revisionAt.UTC(), string(command.WorkspaceID), command.ExpectedRevision)
			if err != nil {
				return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
			}
			if rowsAffected != 1 {
				return versionConflict(domain.ErrorCodeConfigRevisionConflict, "Git remote configuration changed")
			}
		} else {
			if _, err := gormExec(callbackCtx, tx, gormInsertConfigSQL, string(command.WorkspaceID), command.RemoteURL,
				command.Branch, command.AutoSync, tokenConfigured, nextRevision, createdAt.UTC(), revisionAt.UTC()); err != nil {
				return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
			}
		}
		if encrypted.Configured() {
			if _, err := gormExec(callbackCtx, tx, gormInsertCredentialSQL, string(command.WorkspaceID), nextRevision,
				encrypted.KeyID, encrypted.Nonce, encrypted.Ciphertext, encrypted.AADDigest); err != nil {
				return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
			}
		}
		if _, err := gormExec(callbackCtx, tx, gormInsertConfigReceiptSQL, string(command.WorkspaceID), command.IdempotencyKey,
			command.RequestHash, command.ExpectedRevision, nextRevision); err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		config, err := gormLoadConfigRevision(callbackCtx, tx, command.WorkspaceID, nextRevision)
		if err != nil {
			return err
		}
		result = application.ConfigReceipt{Config: config}
		return nil
	})
	if err != nil {
		return application.ConfigReceipt{}, err
	}
	return result, err
}

// DeleteConfig appends an unconfigured revision and removes the active
// credential under the same workspace lock.
func (repository *GORMRepository) DeleteConfig(ctx context.Context, command application.DeleteConfigCommand) (application.ConfigReceipt, error) {
	if err := repository.ready(ctx); err != nil {
		return application.ConfigReceipt{}, err
	}
	if !validID(command.WorkspaceID) || command.ExpectedRevision < 1 || !validText(command.IdempotencyKey, 128) ||
		!validText(command.RequestHash, 64) || !validText(command.Actor, 256) {
		return application.ConfigReceipt{}, invalid("Git remote deletion is invalid")
	}
	var result application.ConfigReceipt
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockWorkspace(callbackCtx, tx, command.WorkspaceID, workspaceLockPurpose); err != nil {
			return err
		}
		replayed, found, err := gormLoadConfigReceipt(callbackCtx, tx, command.WorkspaceID, command.IdempotencyKey, command.RequestHash, "DELETE")
		if err != nil {
			return err
		}
		if found {
			result = replayed
			return nil
		}
		current, currentFound, err := gormLockCurrentConfig(callbackCtx, tx, command.WorkspaceID)
		if err != nil {
			return err
		}
		if !currentFound || !current.Configured || current.Revision != command.ExpectedRevision {
			return versionConflict(domain.ErrorCodeConfigRevisionConflict, "Git remote configuration changed")
		}
		if err := gormFenceConfigMutation(callbackCtx, tx, command.WorkspaceID); err != nil {
			return err
		}
		nextRevision := command.ExpectedRevision + 1
		revisionRow, err := gormRawRow(callbackCtx, tx, gormInsertDeleteRevisionSQL, string(command.WorkspaceID), nextRevision,
			command.Actor, current.CreatedAt.UTC())
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		var revisionAt time.Time
		if err := revisionRow.Scan(&revisionAt); err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if _, err := gormExec(callbackCtx, tx, gormDeleteCredentialSQL, string(command.WorkspaceID)); err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		rowsAffected, err := gormExec(callbackCtx, tx, gormDeleteConfigSQL, nextRevision, revisionAt.UTC(), string(command.WorkspaceID), command.ExpectedRevision)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if rowsAffected != 1 {
			return versionConflict(domain.ErrorCodeConfigRevisionConflict, "Git remote configuration changed")
		}
		if _, err := gormExec(callbackCtx, tx, gormInsertDeleteReceiptSQL, string(command.WorkspaceID), command.IdempotencyKey,
			command.RequestHash, command.ExpectedRevision, nextRevision); err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		config, err := gormLoadConfigRevision(callbackCtx, tx, command.WorkspaceID, nextRevision)
		if err != nil {
			return err
		}
		result = application.ConfigReceipt{Config: config}
		return nil
	})
	if err != nil {
		return application.ConfigReceipt{}, err
	}
	return result, err
}

// OpenCredential decrypts only the exact configured revision bound to a run.
func (repository *GORMRepository) OpenCredential(ctx context.Context, workspaceID foundation.ID, revision int64) (domain.Token, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Token{}, err
	}
	if !validID(workspaceID) || revision < 1 {
		return domain.Token{}, invalid("Git remote credential query is invalid")
	}
	row, err := gormRawRow(ctx, repository.database, gormGetConfigSQL, string(workspaceID))
	if err != nil {
		return domain.Token{}, classifyGORM(ctx, err, domain.ErrorCodeSecretUnavailable)
	}
	config, err := scanConfig(row)
	if gormNoRows(err) || (err == nil && (!config.Configured || !config.TokenConfigured || config.Revision != revision)) {
		return domain.Token{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeConfigStale, false, errors.New("Git remote configuration changed"))
	}
	if err != nil {
		return domain.Token{}, classifyGORM(ctx, err, domain.ErrorCodeSecretUnavailable)
	}
	envelope, err := gormLoadCredentialEnvelope(ctx, repository.database, workspaceID, revision)
	if err != nil {
		return domain.Token{}, err
	}
	return repository.sealer.Open(envelope, domain.CredentialContext{
		WorkspaceID: workspaceID, Revision: revision, RemoteURL: config.RemoteURL,
		Branch: config.Branch, SchemaVersion: domain.CredentialSchemaVersion,
	})
}

func gormLockCurrentConfig(ctx context.Context, database *gorm.DB, workspaceID foundation.ID) (domain.RemoteConfig, bool, error) {
	row, err := gormRawRow(ctx, database, gormLockConfigSQL, string(workspaceID))
	if err != nil {
		return domain.RemoteConfig{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	config, err := scanConfig(row)
	if gormNoRows(err) {
		return domain.RemoteConfig{WorkspaceID: workspaceID}, false, nil
	}
	if err != nil {
		return domain.RemoteConfig{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	return config, true, nil
}

func gormLoadConfigRevision(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, revision int64) (domain.RemoteConfig, error) {
	row, err := gormRawRow(ctx, database, gormLoadConfigRevisionSQL, string(workspaceID), revision)
	if err != nil {
		return domain.RemoteConfig{}, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	config, err := scanConfig(row)
	if gormNoRows(err) {
		return domain.RemoteConfig{}, corrupt("Git remote configuration receipt points to a missing revision")
	}
	if err != nil {
		return domain.RemoteConfig{}, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	return config, nil
}

func gormLoadConfigReceipt(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key, requestHash, commandType string) (application.ConfigReceipt, bool, error) {
	row, err := gormRawRow(ctx, database, gormConfigReceiptSQL, string(workspaceID), key)
	if err != nil {
		return application.ConfigReceipt{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	var storedHash, storedType string
	var revision int64
	if err := row.Scan(&storedHash, &storedType, &revision); gormNoRows(err) {
		return application.ConfigReceipt{}, false, nil
	} else if err != nil {
		return application.ConfigReceipt{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	if storedHash != requestHash || storedType != commandType {
		return application.ConfigReceipt{}, false, versionConflict(domain.ErrorCodeIdempotencyConflict, "Git remote idempotency key is bound to another command")
	}
	config, err := gormLoadConfigRevision(ctx, database, workspaceID, revision)
	if err != nil {
		return application.ConfigReceipt{}, false, err
	}
	return application.ConfigReceipt{Config: config, Replayed: true}, true, nil
}

func gormLoadCredentialEnvelope(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, revision int64) (domain.EncryptedCredential, error) {
	row, err := gormRawRow(ctx, database, gormCredentialSQL, string(workspaceID), revision)
	if err != nil {
		return domain.EncryptedCredential{}, classifyGORM(ctx, err, domain.ErrorCodeSecretUnavailable)
	}
	var envelope domain.EncryptedCredential
	if err := row.Scan(&envelope.KeyID, &envelope.Nonce, &envelope.Ciphertext, &envelope.AADDigest); gormNoRows(err) {
		return domain.EncryptedCredential{}, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeSecretUnavailable, false, errors.New("configured Git remote credential is missing"))
	} else if err != nil {
		return domain.EncryptedCredential{}, classifyGORM(ctx, err, domain.ErrorCodeSecretUnavailable)
	}
	if err := envelope.Validate(); err != nil {
		return domain.EncryptedCredential{}, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeSecretUnavailable, false, err)
	}
	return envelope, nil
}

func gormFenceConfigMutation(ctx context.Context, database *gorm.DB, workspaceID foundation.ID) error {
	row, err := gormRawRow(ctx, database, gormConfigFenceSQL, string(workspaceID))
	if err != nil {
		return classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	var runID, status string
	if err := row.Scan(&runID, &status); gormNoRows(err) {
		return nil
	} else if err != nil {
		return classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	if status != string(domain.RunPending) {
		return versionConflict(domain.ErrorCodeRunActive, "an executing Git sync run blocks configuration changes")
	}
	rowsAffected, err := gormExec(ctx, database, gormFenceConfigRunSQL, domain.ErrorCodeConfigStale, string(workspaceID), runID)
	if err != nil {
		return classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	if rowsAffected != 1 {
		return versionConflict(domain.ErrorCodeRunActive, "Git sync run changed while updating configuration")
	}
	return nil
}
