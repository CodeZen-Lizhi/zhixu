package postgres

const (
	authSessionTable  = "auth.session"
	authAPITokenTable = "auth.api_token"

	authCheckSQL = `SELECT
		EXISTS (SELECT 1 FROM auth.session),
		EXISTS (SELECT 1 FROM auth.api_token)`

	apiTokenListProjection = `token.id::text,token.token_hash,token.name,token.scopes::text,
		token.created_at,token.last_used_at,token.expires_at,token.revoked_at`

	// GORM Raw binds question-mark placeholders and lets the PostgreSQL
	// Dialector render them as $n.
	gormCreateSessionSQL = `INSERT INTO auth.session(
		id,token_hash,csrf_hash,user_label,scopes,created_at,last_seen_at,expires_at,revoked_at
	) VALUES(?,?,?,?,?::jsonb,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,
		CURRENT_TIMESTAMP + (?::bigint * INTERVAL '1 microsecond'),NULL)
	RETURNING id::text,token_hash,csrf_hash,user_label,scopes::text,created_at,last_seen_at,expires_at,revoked_at`

	gormRotateSessionSQL = `WITH revoked AS (
		UPDATE auth.session SET revoked_at=CURRENT_TIMESTAMP
		WHERE id=? AND revoked_at IS NULL AND expires_at>CURRENT_TIMESTAMP
		RETURNING id
	)
	INSERT INTO auth.session(
		id,token_hash,csrf_hash,user_label,scopes,created_at,last_seen_at,expires_at,revoked_at
	) SELECT ?,?,?,?,?::jsonb,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,
		CURRENT_TIMESTAMP + (?::bigint * INTERVAL '1 microsecond'),NULL FROM revoked
	RETURNING id::text,token_hash,csrf_hash,user_label,scopes::text,created_at,last_seen_at,expires_at,revoked_at`

	gormAuthenticateSessionSQL = `UPDATE auth.session
		SET last_seen_at=GREATEST(last_seen_at,CURRENT_TIMESTAMP)
		WHERE token_hash=? AND revoked_at IS NULL AND expires_at>CURRENT_TIMESTAMP
		RETURNING id::text,token_hash,csrf_hash,user_label,scopes::text,created_at,last_seen_at,expires_at,revoked_at`

	gormCreateAPITokenSQL = `INSERT INTO auth.api_token(
		id,token_hash,name,scopes,created_at,last_used_at,expires_at,revoked_at
	) VALUES(?,?,?,?::jsonb,CURRENT_TIMESTAMP,NULL,
		CURRENT_TIMESTAMP + (?::bigint * INTERVAL '1 microsecond'),NULL)
	RETURNING id::text,token_hash,name,scopes::text,created_at,last_used_at,expires_at,revoked_at`

	gormAuthenticateAPITokenSQL = `UPDATE auth.api_token
		SET last_used_at=GREATEST(COALESCE(last_used_at,CURRENT_TIMESTAMP),CURRENT_TIMESTAMP)
		WHERE token_hash=? AND revoked_at IS NULL AND expires_at>CURRENT_TIMESTAMP
		RETURNING id::text,token_hash,name,scopes::text,created_at,last_used_at,expires_at,revoked_at`
)
