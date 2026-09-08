package postgres

// These projections use explicit column lists and bind every value through GORM.
const (
	gormConfigColumns = `workspace_id::text,configured,COALESCE(normalized_url,''),COALESCE(branch,''),
	auto_sync,token_configured,revision,created_at,updated_at`

	gormConfigRevisionColumns = `workspace_id::text,configured,COALESCE(normalized_url,''),COALESCE(branch,''),
	auto_sync,token_configured,revision,config_created_at,created_at`

	gormRunColumns = `id::text,workspace_id::text,config_revision,remote_url,branch,trigger,
	COALESCE(retry_of_run_id::text,''),idempotency_key,request_hash,status,direction,failure_class,error_code,retryable,
	COALESCE(expected_head_oid,''),COALESCE(expected_remote_oid,''),COALESCE(verified_head_oid,''),
	COALESCE(verified_remote_oid,''),changed_files,index_status,index_error_code,index_retryable,
	COALESCE(index_version_id::text,''),attempt_count,version,created_at,updated_at,completed_at`

	gormAttemptColumns = `id::text,workspace_id::text,run_id::text,attempt_no,status,phase,
	COALESCE(expected_head_oid,''),COALESCE(expected_remote_oid,''),result_known,error_code,retryable,
	COALESCE(lease_owner,''),lease_expires_at,started_at,completed_at,version`

	gormGetConfigSQL = `SELECT ` + gormConfigColumns + `
	FROM ops.git_remote_config WHERE workspace_id=?`

	gormLoadConfigRevisionSQL = `SELECT ` + gormConfigRevisionColumns + `
	FROM ops.git_remote_config_revision WHERE workspace_id=? AND revision=?`

	gormLockConfigSQL = `SELECT ` + gormConfigColumns + `
	FROM ops.git_remote_config WHERE workspace_id=? FOR UPDATE`

	gormConfigReceiptSQL = `SELECT request_hash,command_type,result_revision
	FROM ops.git_remote_command_receipt WHERE workspace_id=? AND idempotency_key=?`

	gormCredentialSQL = `SELECT key_id,nonce,ciphertext,aad_digest
	FROM ops.git_remote_credential WHERE workspace_id=? AND config_revision=?`

	gormRunByCommandSQL = `SELECT ` + gormRunColumns + `
	FROM ops.git_sync_run WHERE workspace_id=? AND idempotency_key=?`

	gormGetRunSQL = `SELECT ` + gormRunColumns + `
	FROM ops.git_sync_run WHERE workspace_id=? AND id=?`

	gormGetCurrentRunSQL = `SELECT ` + gormRunColumns + `
	FROM ops.git_sync_run WHERE workspace_id=? ORDER BY created_at DESC,id DESC LIMIT 1`

	gormListRunsSQL = `SELECT ` + gormRunColumns + `
	FROM ops.git_sync_run
	WHERE workspace_id=? AND (?::timestamptz IS NULL OR (created_at,id)<(?,?::uuid))
	ORDER BY created_at DESC,id DESC LIMIT ?`

	gormRunForUpdateSQL = `SELECT ` + gormRunColumns + `
	FROM ops.git_sync_run WHERE workspace_id=? AND id=? FOR UPDATE`

	gormRunningAttemptSQL = `SELECT ` + gormAttemptColumns + `
	FROM ops.git_sync_attempt WHERE run_id=? AND status='RUNNING' FOR UPDATE`

	gormConfigFenceSQL = `SELECT id::text,status FROM ops.git_sync_run
	WHERE workspace_id=? AND status IN ('PENDING','FETCHING','COMPARING','FAST_FORWARDING','PUSHING','VERIFYING')
	FOR UPDATE`

	gormRunConfigFenceSQL = `SELECT configured,auto_sync,token_configured,revision,COALESCE(normalized_url,''),COALESCE(branch,'')
	FROM ops.git_remote_config WHERE workspace_id=? FOR SHARE`

	gormAttemptConfigFenceSQL = `SELECT configured,token_configured,revision
	FROM ops.git_remote_config WHERE workspace_id=? FOR SHARE`

	gormOutboxClaimSQL = `WITH candidate AS (
	SELECT id FROM ops.git_sync_outbox
	WHERE published_at IS NULL AND poisoned_at IS NULL AND available_at<=clock_timestamp()
	  AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp())
	ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE ops.git_sync_outbox AS event SET
	lease_owner=?,lease_expires_at=clock_timestamp()+(?::bigint*interval '1 millisecond'),
	attempt_count=event.attempt_count+1,version=event.version+1,updated_at=clock_timestamp()
FROM candidate WHERE event.id=candidate.id
RETURNING event.id::text,event.workspace_id::text,event.run_id::text,event.kind,event.attempt_count,event.version,event.lease_expires_at`

	gormAutoSyncSQL = `
	SELECT commit_mapping.workspace_id::text,commit_mapping.id::text,current_config.revision
	FROM change_control.proposal_commit AS commit_mapping
	JOIN change_control.writeback_execution AS execution
	  ON execution.id=commit_mapping.writeback_execution_id
	 AND execution.workspace_id=commit_mapping.workspace_id
	 AND execution.status='completed'
	 AND execution.completed_at IS NOT NULL
	JOIN LATERAL (
		SELECT revision.configured,revision.auto_sync,revision.token_configured
		FROM ops.git_remote_config_revision AS revision
		WHERE revision.workspace_id=commit_mapping.workspace_id
		  AND revision.created_at<=execution.completed_at
		ORDER BY revision.revision DESC
		LIMIT 1
	) AS effective ON effective.configured AND effective.auto_sync AND effective.token_configured
	JOIN ops.git_remote_config AS current_config
	  ON current_config.workspace_id=commit_mapping.workspace_id
	 AND current_config.configured AND current_config.auto_sync AND current_config.token_configured
	WHERE NOT EXISTS (
		SELECT 1 FROM ops.git_sync_run AS run
		WHERE run.workspace_id=commit_mapping.workspace_id
		  AND run.idempotency_key=? || commit_mapping.id::text || ':config:' || current_config.revision::text
	)
	ORDER BY execution.completed_at,commit_mapping.id
	LIMIT ?`
)
