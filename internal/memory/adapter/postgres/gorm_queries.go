package postgres

const (
	gormMemoryCommandSQL = `SELECT owner_principal_kind,owner_principal_id::text,request_hash,command_type,memory_id::text,expected_version,memory_version,response::text
		FROM learning.memory_command WHERE workspace_id=? AND idempotency_key=? FOR UPDATE`

	gormInterviewCandidateCommandSQL = `SELECT c.owner_principal_kind,c.owner_principal_id::text,c.request_hash,c.command_type,c.memory_id::text,c.expected_version,c.memory_version,c.response::text
		FROM learning.memory AS m
		JOIN learning.memory_command AS c ON c.workspace_id=m.workspace_id AND c.memory_id=m.id
		WHERE m.workspace_id=? AND m.owner_principal_kind=? AND m.owner_principal_id=?
		  AND m.source_type='INTERVIEW' AND m.source_ref=? AND c.command_type='CREATE_CANDIDATE'
		ORDER BY c.created_at ASC,c.idempotency_key ASC LIMIT 1 FOR UPDATE OF m,c`

	gormLockCommandSQL = `SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ?,0))`

	gormLockInterviewProvenanceSQL = `SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ? || chr(31) || ? || chr(31) || ? || chr(31) || ?,0))`

	gormLockWorkspaceSQL = `SELECT id::text FROM core.workspace WHERE id=? FOR KEY SHARE`

	gormInsertCandidateSQL = `INSERT INTO learning.memory(
		id,workspace_id,owner_principal_kind,owner_principal_id,memory_type,content,source_type,source_ref,task_scope_id,
		status,expires_at,confirmed_at,confirmed_by_principal_kind,confirmed_by_principal_id,version,created_at,updated_at
	) VALUES(?,?,?,?,?,?::jsonb,?,?,?,?,?,?,?,?,?,?,?)
	RETURNING ` + memoryColumns

	gormUpdateMemorySQL = `UPDATE learning.memory SET
		content=?::jsonb,source_type=?,source_ref=?,task_scope_id=?,status=?,expires_at=?,
		confirmed_at=?,confirmed_by_principal_kind=?,confirmed_by_principal_id=?,version=?,updated_at=?
		WHERE workspace_id=? AND id=? AND owner_principal_kind=? AND owner_principal_id=? AND version=?
		RETURNING ` + memoryColumns

	gormGetOwnedMemorySQL = `SELECT ` + memoryColumns + ` FROM learning.memory
		WHERE workspace_id=? AND id=? AND owner_principal_kind=? AND owner_principal_id=?`

	gormLockOwnedMemorySQL = gormGetOwnedMemorySQL + ` FOR UPDATE`

	gormListMemorySQL = `SELECT ` + memoryColumns + ` FROM learning.memory
		WHERE workspace_id=? AND owner_principal_kind=? AND owner_principal_id=?
		  AND (?::text[] IS NULL OR memory_type=ANY(?::text[]))
		  AND (?::text[] IS NULL OR status=ANY(?::text[]))
		  AND (?::timestamptz IS NULL OR (updated_at,id)<(?::timestamptz,?::uuid))
		ORDER BY updated_at DESC,id DESC LIMIT ?`

	gormLoadEffectiveMemorySQL = `SELECT ` + memoryColumns + ` FROM learning.memory
		WHERE workspace_id=? AND owner_principal_kind=? AND owner_principal_id=?
		  AND status='ACTIVE' AND confirmed_at IS NOT NULL AND confirmed_by_principal_kind IS NOT NULL AND confirmed_by_principal_id IS NOT NULL
		  AND (expires_at IS NULL OR expires_at>clock_timestamp())
		  AND (task_scope_id IS NULL OR task_scope_id=?::uuid)
		ORDER BY updated_at DESC,id DESC LIMIT ?`

	gormExpireDueMemorySQL = `SELECT ` + memoryColumns + ` FROM learning.memory
		WHERE status IN ('CANDIDATE','ACTIVE','PAUSED') AND expires_at IS NOT NULL AND expires_at<=?
		ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT ?`

	gormInsertMemoryCommandSQL = `INSERT INTO learning.memory_command(
		workspace_id,idempotency_key,owner_principal_kind,owner_principal_id,request_hash,command_type,memory_id,expected_version,memory_version,response,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?::jsonb,?)`

	gormInsertMemoryAuditSQL = `INSERT INTO learning.memory_audit(
		workspace_id,memory_id,owner_principal_kind,owner_principal_id,actor_principal_kind,actor_principal_id,
		action,from_status,to_status,memory_version,idempotency_key,request_hash,occurred_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`

	gormDatabaseNowSQL = `SELECT clock_timestamp()`
)
