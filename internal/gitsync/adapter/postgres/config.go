package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

// GetConfig 返回当前非密钥配置；无配置时返回 revision=0 的规范空快照。
func (repository *Repository) GetConfig(ctx context.Context, workspaceID foundation.ID) (domain.RemoteConfig, error) {
	if repository == nil || nilInterface(repository.db) || !validID(workspaceID) {
		return domain.RemoteConfig{}, invalid("Git remote configuration query is invalid")
	}
	config, err := scanConfig(repository.db.QueryRow(ctx, `SELECT `+configColumns+`
		FROM ops.git_remote_config WHERE workspace_id=$1`, string(workspaceID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RemoteConfig{WorkspaceID: workspaceID}, nil
	}
	if err != nil {
		return domain.RemoteConfig{}, classify(err, domain.ErrorCodeUnavailable)
	}
	return config, nil
}

// ReplayConfig 在 URL/DNS 校验和 CAS 前返回精确匹配的配置命令结果。
func (repository *Repository) ReplayConfig(ctx context.Context, command application.ReplayConfigCommand) (application.ConfigReceipt, bool, error) {
	if repository == nil || nilInterface(repository.db) || !validID(command.WorkspaceID) ||
		!validText(command.IdempotencyKey, 128) || !validText(command.RequestHash, 64) ||
		(command.CommandType != "SAVE" && command.CommandType != "DELETE") {
		return application.ConfigReceipt{}, false, invalid("Git remote configuration replay query is invalid")
	}
	receipt, found, err := loadConfigReceipt(ctx, repository.db, command.WorkspaceID, command.IdempotencyKey, command.RequestHash, command.CommandType)
	return receipt, found, err
}

// SaveConfig 原子提交配置 Revision、当前投影、密文与幂等 Receipt。
func (repository *Repository) SaveConfig(ctx context.Context, command application.PersistConfigCommand) (application.ConfigReceipt, error) {
	if repository == nil || nilInterface(repository.db) || nilInterface(repository.sealer) ||
		!validID(command.WorkspaceID) || command.ExpectedRevision < 0 || !validText(command.IdempotencyKey, 128) ||
		!validText(command.RequestHash, 64) || !validText(command.Actor, 256) || command.SecretAction.Validate() != nil {
		return application.ConfigReceipt{}, invalid("Git remote configuration write is invalid")
	}
	defer command.SecretAction.Value.Destroy()
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockWorkspace(ctx, tx, command.WorkspaceID, workspaceLockPurpose); err != nil {
		return application.ConfigReceipt{}, err
	}
	if receipt, found, err := loadConfigReceipt(ctx, tx, command.WorkspaceID, command.IdempotencyKey, command.RequestHash, "SAVE"); err != nil || found {
		return receipt, err
	}
	current, found, err := lockCurrentConfig(ctx, tx, command.WorkspaceID)
	if err != nil {
		return application.ConfigReceipt{}, err
	}
	currentRevision := int64(0)
	if found {
		currentRevision = current.Revision
	}
	if currentRevision != command.ExpectedRevision {
		return application.ConfigReceipt{}, versionConflict(domain.ErrorCodeConfigRevisionConflict, "Git remote configuration changed")
	}
	if err := fenceConfigMutation(ctx, tx, command.WorkspaceID); err != nil {
		return application.ConfigReceipt{}, err
	}
	nextRevision := command.ExpectedRevision + 1
	nextContext := domain.CredentialContext{
		WorkspaceID: command.WorkspaceID, Revision: nextRevision, RemoteURL: command.RemoteURL,
		Branch: command.Branch, SchemaVersion: domain.CredentialSchemaVersion,
	}
	var encrypted domain.EncryptedCredential
	switch command.SecretAction.Kind {
	case domain.SecretActionKeep:
		if !found || !current.CredentialBindingMatches(command.RemoteURL, command.Branch) {
			return application.ConfigReceipt{}, versionConflict(domain.ErrorCodeConfigRevisionConflict, "a kept token cannot be rebound or recovered")
		}
		currentEnvelope, loadErr := loadCredentialEnvelope(ctx, tx, current.WorkspaceID, current.Revision)
		if loadErr != nil {
			return application.ConfigReceipt{}, loadErr
		}
		currentToken, openErr := repository.sealer.Open(currentEnvelope, domain.CredentialContext{
			WorkspaceID: current.WorkspaceID, Revision: current.Revision, RemoteURL: current.RemoteURL,
			Branch: current.Branch, SchemaVersion: domain.CredentialSchemaVersion,
		})
		if openErr != nil {
			return application.ConfigReceipt{}, openErr
		}
		defer currentToken.Destroy()
		encrypted, err = repository.sealer.Seal(currentToken, nextContext)
	case domain.SecretActionReplace:
		replacementBytes := command.SecretAction.Value.Bytes()
		defer clear(replacementBytes)
		replacement, copyErr := domain.TokenFromBytes(replacementBytes)
		if copyErr != nil {
			return application.ConfigReceipt{}, copyErr
		}
		defer replacement.Destroy()
		encrypted, err = repository.sealer.Seal(replacement, nextContext)
	case domain.SecretActionClear:
		encrypted = domain.EncryptedCredential{}
	default:
		return application.ConfigReceipt{}, invalid("Git remote token action is invalid")
	}
	if err != nil {
		return application.ConfigReceipt{}, err
	}
	if err := encrypted.Validate(); err != nil {
		return application.ConfigReceipt{}, err
	}
	tokenConfigured := encrypted.Configured()
	var createdAt time.Time
	if found {
		createdAt = current.CreatedAt
	} else if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&createdAt); err != nil {
		return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	var revisionAt time.Time
	if err := tx.QueryRow(ctx, `INSERT INTO ops.git_remote_config_revision(
		workspace_id,revision,configured,normalized_url,branch,auto_sync,token_configured,actor,config_created_at,created_at
	) VALUES($1,$2,true,$3,$4,$5,$6,$7,$8,clock_timestamp()) RETURNING created_at`,
		string(command.WorkspaceID), nextRevision, command.RemoteURL, command.Branch, command.AutoSync,
		tokenConfigured, command.Actor, createdAt.UTC()).Scan(&revisionAt); err != nil {
		return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	if found {
		if _, err := tx.Exec(ctx, `DELETE FROM ops.git_remote_credential WHERE workspace_id=$1`, string(command.WorkspaceID)); err != nil {
			return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
		}
		tag, err := tx.Exec(ctx, `UPDATE ops.git_remote_config SET
			configured=true,normalized_url=$2,branch=$3,auto_sync=$4,token_configured=$5,
			revision=$6,updated_at=$8 WHERE workspace_id=$1 AND revision=$7`,
			string(command.WorkspaceID), command.RemoteURL, command.Branch, command.AutoSync,
			tokenConfigured, nextRevision, command.ExpectedRevision, revisionAt.UTC())
		if err != nil {
			return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
		}
		if tag.RowsAffected() != 1 {
			return application.ConfigReceipt{}, versionConflict(domain.ErrorCodeConfigRevisionConflict, "Git remote configuration changed")
		}
	} else {
		if _, err := tx.Exec(ctx, `INSERT INTO ops.git_remote_config(
			workspace_id,configured,normalized_url,branch,auto_sync,token_configured,revision,created_at,updated_at
		) VALUES($1,true,$2,$3,$4,$5,$6,$7,$8)`,
			string(command.WorkspaceID), command.RemoteURL, command.Branch, command.AutoSync,
			tokenConfigured, nextRevision, createdAt.UTC(), revisionAt.UTC()); err != nil {
			return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
		}
	}
	if encrypted.Configured() {
		if _, err := tx.Exec(ctx, `INSERT INTO ops.git_remote_credential(
			workspace_id,config_revision,key_id,nonce,ciphertext,aad_digest,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,clock_timestamp(),clock_timestamp())`,
			string(command.WorkspaceID), nextRevision, encrypted.KeyID, encrypted.Nonce,
			encrypted.Ciphertext, encrypted.AADDigest); err != nil {
			return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.git_remote_command_receipt(
		workspace_id,idempotency_key,request_hash,command_type,expected_revision,result_revision,created_at
	) VALUES($1,$2,$3,'SAVE',$4,$5,clock_timestamp())`,
		string(command.WorkspaceID), command.IdempotencyKey, command.RequestHash, command.ExpectedRevision, nextRevision); err != nil {
		return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	config, err := loadConfigRevision(ctx, tx, command.WorkspaceID, nextRevision)
	if err != nil {
		return application.ConfigReceipt{}, err
	}
	if err := commit(ctx, tx); err != nil {
		return application.ConfigReceipt{}, err
	}
	return application.ConfigReceipt{Config: config}, nil
}

// DeleteConfig 追加 unconfigured Revision 并销毁当前密文。
func (repository *Repository) DeleteConfig(ctx context.Context, command application.DeleteConfigCommand) (application.ConfigReceipt, error) {
	if repository == nil || nilInterface(repository.db) || !validID(command.WorkspaceID) || command.ExpectedRevision < 1 ||
		!validText(command.IdempotencyKey, 128) || !validText(command.RequestHash, 64) || !validText(command.Actor, 256) {
		return application.ConfigReceipt{}, invalid("Git remote deletion is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockWorkspace(ctx, tx, command.WorkspaceID, workspaceLockPurpose); err != nil {
		return application.ConfigReceipt{}, err
	}
	if receipt, found, err := loadConfigReceipt(ctx, tx, command.WorkspaceID, command.IdempotencyKey, command.RequestHash, "DELETE"); err != nil || found {
		return receipt, err
	}
	current, found, err := lockCurrentConfig(ctx, tx, command.WorkspaceID)
	if err != nil {
		return application.ConfigReceipt{}, err
	}
	if !found || !current.Configured || current.Revision != command.ExpectedRevision {
		return application.ConfigReceipt{}, versionConflict(domain.ErrorCodeConfigRevisionConflict, "Git remote configuration changed")
	}
	if err := fenceConfigMutation(ctx, tx, command.WorkspaceID); err != nil {
		return application.ConfigReceipt{}, err
	}
	nextRevision := command.ExpectedRevision + 1
	var revisionAt time.Time
	if err := tx.QueryRow(ctx, `INSERT INTO ops.git_remote_config_revision(
		workspace_id,revision,configured,normalized_url,branch,auto_sync,token_configured,actor,config_created_at,created_at
	) VALUES($1,$2,false,NULL,NULL,false,false,$3,$4,clock_timestamp()) RETURNING created_at`,
		string(command.WorkspaceID), nextRevision, command.Actor, current.CreatedAt.UTC()).Scan(&revisionAt); err != nil {
		return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ops.git_remote_credential WHERE workspace_id=$1`, string(command.WorkspaceID)); err != nil {
		return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	tag, err := tx.Exec(ctx, `UPDATE ops.git_remote_config SET
		configured=false,normalized_url=NULL,branch=NULL,auto_sync=false,token_configured=false,
		revision=$2,updated_at=$4 WHERE workspace_id=$1 AND revision=$3`,
		string(command.WorkspaceID), nextRevision, command.ExpectedRevision, revisionAt.UTC())
	if err != nil {
		return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return application.ConfigReceipt{}, versionConflict(domain.ErrorCodeConfigRevisionConflict, "Git remote configuration changed")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.git_remote_command_receipt(
		workspace_id,idempotency_key,request_hash,command_type,expected_revision,result_revision,created_at
	) VALUES($1,$2,$3,'DELETE',$4,$5,clock_timestamp())`,
		string(command.WorkspaceID), command.IdempotencyKey, command.RequestHash, command.ExpectedRevision, nextRevision); err != nil {
		return application.ConfigReceipt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	config, err := loadConfigRevision(ctx, tx, command.WorkspaceID, nextRevision)
	if err != nil {
		return application.ConfigReceipt{}, err
	}
	if err := commit(ctx, tx); err != nil {
		return application.ConfigReceipt{}, err
	}
	return application.ConfigReceipt{Config: config}, nil
}

// OpenCredential 只解密当前且 revision 精确匹配的 Token。
func (repository *Repository) OpenCredential(ctx context.Context, workspaceID foundation.ID, revision int64) (domain.Token, error) {
	if repository == nil || nilInterface(repository.db) || nilInterface(repository.sealer) || !validID(workspaceID) || revision < 1 {
		return domain.Token{}, invalid("Git remote credential query is invalid")
	}
	config, err := scanConfig(repository.db.QueryRow(ctx, `SELECT `+configColumns+`
		FROM ops.git_remote_config WHERE workspace_id=$1`, string(workspaceID)))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!config.Configured || !config.TokenConfigured || config.Revision != revision)) {
		return domain.Token{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeConfigStale, false, errors.New("Git remote configuration changed"))
	}
	if err != nil {
		return domain.Token{}, classify(err, domain.ErrorCodeSecretUnavailable)
	}
	envelope, err := loadCredentialEnvelope(ctx, repository.db, workspaceID, revision)
	if err != nil {
		return domain.Token{}, err
	}
	return repository.sealer.Open(envelope, domain.CredentialContext{
		WorkspaceID: workspaceID, Revision: revision, RemoteURL: config.RemoteURL,
		Branch: config.Branch, SchemaVersion: domain.CredentialSchemaVersion,
	})
}

func lockCurrentConfig(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID) (domain.RemoteConfig, bool, error) {
	config, err := scanConfig(tx.QueryRow(ctx, `SELECT `+configColumns+`
		FROM ops.git_remote_config WHERE workspace_id=$1 FOR UPDATE`, string(workspaceID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RemoteConfig{WorkspaceID: workspaceID}, false, nil
	}
	if err != nil {
		return domain.RemoteConfig{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	return config, true, nil
}

func loadConfigRevision(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID foundation.ID, revision int64) (domain.RemoteConfig, error) {
	config, err := scanConfig(queryer.QueryRow(ctx, `SELECT workspace_id::text,configured,
		COALESCE(normalized_url,''),COALESCE(branch,''),auto_sync,token_configured,revision,config_created_at,created_at
		FROM ops.git_remote_config_revision WHERE workspace_id=$1 AND revision=$2`, string(workspaceID), revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RemoteConfig{}, corrupt("Git remote configuration receipt points to a missing revision")
	}
	if err != nil {
		return domain.RemoteConfig{}, classify(err, domain.ErrorCodeUnavailable)
	}
	return config, nil
}

func loadConfigReceipt(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID foundation.ID, key, requestHash, commandType string) (application.ConfigReceipt, bool, error) {
	var storedHash, storedType string
	var revision int64
	err := queryer.QueryRow(ctx, `SELECT request_hash,command_type,result_revision
		FROM ops.git_remote_command_receipt WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(workspaceID), key).Scan(&storedHash, &storedType, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ConfigReceipt{}, false, nil
	}
	if err != nil {
		return application.ConfigReceipt{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	if storedHash != requestHash || storedType != commandType {
		return application.ConfigReceipt{}, false, versionConflict(domain.ErrorCodeIdempotencyConflict, "Git remote idempotency key is bound to another command")
	}
	config, err := loadConfigRevision(ctx, queryer, workspaceID, revision)
	if err != nil {
		return application.ConfigReceipt{}, false, err
	}
	return application.ConfigReceipt{Config: config, Replayed: true}, true, nil
}

func loadCredentialEnvelope(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID foundation.ID, revision int64) (domain.EncryptedCredential, error) {
	var envelope domain.EncryptedCredential
	err := queryer.QueryRow(ctx, `SELECT key_id,nonce,ciphertext,aad_digest
		FROM ops.git_remote_credential WHERE workspace_id=$1 AND config_revision=$2`, string(workspaceID), revision).
		Scan(&envelope.KeyID, &envelope.Nonce, &envelope.Ciphertext, &envelope.AADDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EncryptedCredential{}, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeSecretUnavailable, false, errors.New("configured Git remote credential is missing"))
	}
	if err != nil {
		return domain.EncryptedCredential{}, classify(err, domain.ErrorCodeSecretUnavailable)
	}
	if err := envelope.Validate(); err != nil {
		return domain.EncryptedCredential{}, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeSecretUnavailable, false, err)
	}
	return envelope, nil
}

func fenceConfigMutation(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID) error {
	var runID string
	var status string
	err := tx.QueryRow(ctx, `SELECT id::text,status FROM ops.git_sync_run
		WHERE workspace_id=$1 AND status IN ('PENDING','FETCHING','COMPARING','FAST_FORWARDING','PUSHING','VERIFYING')
		FOR UPDATE`, string(workspaceID)).Scan(&runID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return classify(err, domain.ErrorCodeUnavailable)
	}
	if status != string(domain.RunPending) {
		return versionConflict(domain.ErrorCodeRunActive, "an executing Git sync run blocks configuration changes")
	}
	tag, err := tx.Exec(ctx, `UPDATE ops.git_sync_run SET
		status='STALE',failure_class='STALE_CONFIG',error_code=$3,retryable=false,
		completed_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND id=$2 AND status='PENDING'`,
		string(workspaceID), runID, domain.ErrorCodeConfigStale)
	if err != nil {
		return classify(err, domain.ErrorCodeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return versionConflict(domain.ErrorCodeRunActive, "Git sync run changed while updating configuration")
	}
	return nil
}
