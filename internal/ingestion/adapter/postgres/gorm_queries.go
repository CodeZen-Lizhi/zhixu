package postgres

const attemptProjectionColumns = `id::text, workspace_id::text, source_version_id::text, workflow_run_id::text,
	parse_projection_id::text, status, security_status, failure_stage, error_code,
	retryable, parser_id, parser_version, parser_config_hash, chunk_strategy_version,
	schema_version, idempotency_key, attempt_number, warnings, started_at, completed_at, version`

const projectionColumns = `id::text, workspace_id::text, content_artifact_id::text, parser_id, parser_version,
	parser_config_hash, schema_version, normalized_content_hash, warnings, created_at`

const spanColumns = `id::text, workspace_id::text, content_artifact_id::text, parse_projection_id::text, span_type,
	start_line, end_line, start_byte, end_byte, selector, excerpt_hash, evidence_kind, derived_excerpt,
	parser_version, schema_version`

const chunkColumns = `id::text, workspace_id::text, parse_projection_id::text, sequence, heading_path, content,
	content_hash, source_span_id::text, byte_count, rune_count, parser_version, chunk_strategy_version,
	schema_version, atomic_oversized, status`

const gormCreateAttemptSQL = `INSERT INTO ingestion.attempt (
	id, workspace_id, source_version_id, workflow_run_id, parse_projection_id,
	status, security_status, failure_stage, error_code, retryable,
	parser_id, parser_version, parser_config_hash, chunk_strategy_version,
	schema_version, idempotency_key, attempt_number, warnings, started_at, completed_at, version
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT (source_version_id, parser_id, parser_version, parser_config_hash, chunk_strategy_version, schema_version, idempotency_key)
DO NOTHING RETURNING ` + attemptProjectionColumns

const gormGetAttemptSQL = `SELECT ` + attemptProjectionColumns + ` FROM ingestion.attempt WHERE id=?`

const gormGetAttemptByIdentitySQL = `SELECT ` + attemptProjectionColumns + ` FROM ingestion.attempt
WHERE source_version_id=? AND parser_id=? AND parser_version=? AND parser_config_hash=?
  AND chunk_strategy_version=? AND schema_version=? AND idempotency_key=?`

const gormTransitionAttemptSQL = `UPDATE ingestion.attempt
SET status=?, security_status=?, parse_projection_id=?, failure_stage=?, error_code=?, retryable=?, warnings=?,
	completed_at=?, version=version+1
WHERE id=? AND version=?
RETURNING ` + attemptProjectionColumns

const gormGetProjectionSQL = `SELECT ` + projectionColumns + ` FROM ingestion.parse_projection WHERE id=?`

const gormInsertProjectionSQL = `INSERT INTO ingestion.parse_projection
	(id, workspace_id, content_artifact_id, parser_id, parser_version, parser_config_hash, schema_version, normalized_content_hash, warnings, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT (content_artifact_id, parser_id, parser_version, parser_config_hash, schema_version) DO NOTHING
RETURNING ` + projectionColumns

const gormGetProjectionByContractSQL = `SELECT ` + projectionColumns + ` FROM ingestion.parse_projection
WHERE content_artifact_id=? AND parser_id=? AND parser_version=? AND parser_config_hash=? AND schema_version=?`

const gormListSpansSQL = `SELECT ` + spanColumns + ` FROM ingestion.source_span
WHERE parse_projection_id=? ORDER BY start_byte, id`

const gormListChunksSQL = `SELECT ` + chunkColumns + ` FROM ingestion.canonical_chunk
WHERE parse_projection_id=? ORDER BY sequence, id`

const gormListChunksByStrategySQL = `SELECT ` + chunkColumns + ` FROM ingestion.canonical_chunk
WHERE parse_projection_id=? AND chunk_strategy_version=? AND schema_version=? ORDER BY sequence, id`
