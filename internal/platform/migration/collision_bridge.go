package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"
)

const (
	legacyCollisionFirstVersion  int64 = 78
	legacyCollisionSecondVersion int64 = 79
	legacyCollisionGapFirst      int64 = 80
	legacyCollisionGapSecond     int64 = 81
	legacyCollisionGapThird      int64 = 82
	adoptedEinoFirstVersion      int64 = 83
	adoptedEinoSecondVersion     int64 = 84
)

type fingerprintState string

const (
	fingerprintAbsent  fingerprintState = "absent"
	fingerprintPresent fingerprintState = "present"
	fingerprintPartial fingerprintState = "partial"
)

type schemaFingerprint struct {
	name   string
	checks []fingerprintCheck
}

type fingerprintCheck struct {
	name  string
	query string
}

var legacyEino78Fingerprint = schemaFingerprint{
	name: "legacy Eino 78",
	checks: []fingerprintCheck{
		{
			name: "model-call phase constraint",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass('agent.model_call')
  AND conname='agent_model_call_phase_check'
  AND pg_get_constraintdef(oid) LIKE '%''AGENT''%'
  AND pg_get_constraintdef(oid) LIKE '%''ANSWER''%'
)`,
		},
		{
			name: "model-call phase order constraint",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass('agent.model_call')
  AND conname='agent_model_call_phase_order'
  AND pg_get_constraintdef(oid) LIKE '%''AGENT''%'
  AND pg_get_constraintdef(oid) LIKE '%''ANSWER''%'
)`,
		},
		{
			name: "model-call phase index",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_index
WHERE indexrelid=to_regclass('agent.uq_agent_model_call_generation_phase')
  AND pg_get_indexdef(indexrelid) LIKE '%''ANSWER''%'
)`,
		},
		{
			name: "model-call phase guard",
			query: `SELECT COALESCE(
pg_get_functiondef(to_regprocedure('agent.guard_model_call_phase_sequence()'))
    LIKE '%agent model call first phase is invalid%', false)`,
		},
	},
}

var legacyEino79Fingerprint = schemaFingerprint{
	name: "legacy Eino 79",
	checks: []fingerprintCheck{
		{
			name: "answer workspace workflow constraint",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass('agent.answer')
  AND conname='uq_agent_answer_id_workspace_workflow'
)`,
		},
		{
			name: "node attempt identity constraint",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass('workflow.node_attempt')
  AND conname='uq_workflow_node_attempt_id_node_no'
)`,
		},
		{
			name:  "answer draft session",
			query: `SELECT to_regclass('agent.answer_draft_session') IS NOT NULL`,
		},
		{
			name:  "answer draft chunks",
			query: `SELECT to_regclass('agent.answer_draft_chunk') IS NOT NULL`,
		},
		{
			name: "answer draft transition guard",
			query: `SELECT COALESCE(
pg_get_functiondef(to_regprocedure('agent.enforce_answer_draft_session_transition()'))
    LIKE '%answer draft session identity is immutable%', false)`,
		},
		{
			name: "answer draft transition trigger",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_trigger
WHERE tgrelid=to_regclass('agent.answer_draft_session')
  AND tgname='answer_draft_session_transition_guard'
  AND NOT tgisinternal
)`,
		},
		{
			name:  "answer draft visible session index",
			query: `SELECT to_regclass('agent.uq_answer_draft_session_current') IS NOT NULL`,
		},
		{
			name:  "answer draft cleanup index",
			query: `SELECT to_regclass('agent.idx_answer_draft_session_cleanup') IS NOT NULL`,
		},
	},
}

var canonicalModelSettings78Fingerprint = schemaFingerprint{
	name: "canonical model settings 78",
	checks: []fingerprintCheck{
		{
			name: "chat API style column",
			query: `SELECT EXISTS (
SELECT 1 FROM information_schema.columns
WHERE table_schema='ops' AND table_name='model_settings_revisions'
  AND column_name='chat_api_style'
)`,
		},
		{
			name: "chat API style constraint",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass('ops.model_settings_revisions')
  AND conname='ops_model_settings_chat_api_style'
)`,
		},
	},
}

var canonicalModelSettings79Fingerprint = schemaFingerprint{
	name: "canonical model settings 79",
	checks: []fingerprintCheck{
		{
			name:  "hot activation participant table",
			query: `SELECT to_regclass('ops.model_settings_rollout_participant') IS NOT NULL`,
		},
		{
			name: "hot activation participant guard",
			query: `SELECT COALESCE(
pg_get_functiondef(to_regprocedure('ops.enforce_model_settings_participant()'))
    LIKE '%model settings participant history cannot be deleted%', false)`,
		},
		{
			name: "hot activation participant transition trigger",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_trigger
WHERE tgrelid=to_regclass('ops.model_settings_rollout_participant')
  AND tgname='model_settings_participant_guard'
  AND NOT tgisinternal
)`,
		},
		{
			name: "hot activation participant truncate trigger",
			query: `SELECT EXISTS (
SELECT 1 FROM pg_trigger
WHERE tgrelid=to_regclass('ops.model_settings_rollout_participant')
  AND tgname='model_settings_participant_reject_truncate'
  AND NOT tgisinternal
)`,
		},
		{
			name: "hot activation state guard",
			query: `SELECT COALESCE(
pg_get_functiondef(to_regprocedure('ops.enforce_model_settings_state()'))
    LIKE '%both model settings activation roles must be fresh and armed%', false)`,
		},
		{
			name: "hot activation runtime guard",
			query: `SELECT COALESCE(
pg_get_functiondef(to_regprocedure('ops.enforce_model_settings_runtime()'))
    LIKE '%model settings runtime activation binding is invalid%', false)`,
		},
	},
}

// bridgeMigrationVersionCollision repairs databases that recorded the old Eino
// migrations as Goose 78/79 before those version numbers were reassigned to
// model settings migrations. It only acts on a verified collision state.
func bridgeMigrationVersionCollision(ctx context.Context, db *sql.DB, projectFS fs.FS) (bool, error) {
	history, exists, err := appliedMigrationHistory(ctx, db,
		legacyCollisionFirstVersion, legacyCollisionSecondVersion,
		legacyCollisionGapFirst, legacyCollisionGapSecond, legacyCollisionGapThird,
		adoptedEinoFirstVersion, adoptedEinoSecondVersion,
	)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	legacyFirst := history[legacyCollisionFirstVersion]
	legacySecond := history[legacyCollisionSecondVersion]
	if !legacyFirst && !legacySecond {
		return false, nil
	}

	legacyFirstState, err := inspectSchemaFingerprint(ctx, db, legacyEino78Fingerprint)
	if err != nil {
		return false, err
	}
	legacySecondState, err := inspectSchemaFingerprint(ctx, db, legacyEino79Fingerprint)
	if err != nil {
		return false, err
	}
	canonicalFirstState, err := inspectSchemaFingerprint(ctx, db, canonicalModelSettings78Fingerprint)
	if err != nil {
		return false, err
	}
	canonicalSecondState, err := inspectSchemaFingerprint(ctx, db, canonicalModelSettings79Fingerprint)
	if err != nil {
		return false, err
	}
	if canonicalSecondState == fingerprintPresent && canonicalFirstState != fingerprintPresent {
		return false, collisionFingerprintError(legacyFirstState, legacySecondState, canonicalFirstState, canonicalSecondState)
	}
	if legacyFirst != legacySecond {
		if legacyFirst && !legacySecond &&
			legacyFirstState == fingerprintAbsent && legacySecondState == fingerprintAbsent &&
			canonicalFirstState == fingerprintPresent && canonicalSecondState == fingerprintAbsent {
			// Canonical history may stop cleanly between model settings 78 and 79.
			return false, nil
		}
		if !legacyFirst || legacySecond || legacyFirstState != fingerprintPresent ||
			legacySecondState == fingerprintPartial || canonicalFirstState == fingerprintPartial ||
			canonicalSecondState == fingerprintPartial {
			return false, fmt.Errorf(
				"MIGRATION_VERSION_COLLISION_HISTORY_PARTIAL: Goose history 78=%t 79=%t: %w",
				legacyFirst, legacySecond,
				collisionFingerprintError(legacyFirstState, legacySecondState, canonicalFirstState, canonicalSecondState),
			)
		}
		if legacySecondState == fingerprintAbsent {
			if err := applyUnversionedMigrationSubset(ctx, db, projectFS, adoptedEinoSecondVersion); err != nil {
				return false, err
			}
			legacySecondState, err = inspectSchemaFingerprint(ctx, db, legacyEino79Fingerprint)
			if err != nil {
				return false, err
			}
			if legacySecondState != fingerprintPresent {
				return false, fmt.Errorf("MIGRATION_VERSION_COLLISION_REPAIR_INCOMPLETE: legacy79=%s", legacySecondState)
			}
		}
	}

	adoptedFirst := history[adoptedEinoFirstVersion]
	adoptedSecond := history[adoptedEinoSecondVersion]
	if legacyFirstState == fingerprintPresent && legacySecondState == fingerprintAbsent &&
		canonicalFirstState == fingerprintPresent && canonicalSecondState == fingerprintPresent &&
		adoptedFirst && !adoptedSecond {
		// Canonical history may stop cleanly between the current 83 and 84.
		// Goose will resume 84 normally; this is not the legacy 78/79 collision.
		return false, nil
	}

	if legacyFirstState == fingerprintPresent && legacySecondState == fingerprintPresent {
		if canonicalFirstState == fingerprintPartial || canonicalSecondState == fingerprintPartial {
			return false, collisionFingerprintError(legacyFirstState, legacySecondState, canonicalFirstState, canonicalSecondState)
		}
		if canonicalFirstState == fingerprintAbsent {
			if err := applyUnversionedMigrationSubset(ctx, db, projectFS, legacyCollisionFirstVersion); err != nil {
				return false, err
			}
		}
		if canonicalSecondState == fingerprintAbsent {
			if err := applyUnversionedMigrationSubset(ctx, db, projectFS, legacyCollisionSecondVersion); err != nil {
				return false, err
			}
		}
		canonicalFirstState, err = inspectSchemaFingerprint(ctx, db, canonicalModelSettings78Fingerprint)
		if err != nil {
			return false, err
		}
		canonicalSecondState, err = inspectSchemaFingerprint(ctx, db, canonicalModelSettings79Fingerprint)
		if err != nil {
			return false, err
		}
		if canonicalFirstState != fingerprintPresent || canonicalSecondState != fingerprintPresent {
			return false, fmt.Errorf(
				"MIGRATION_VERSION_COLLISION_REPAIR_INCOMPLETE: canonical78=%s canonical79=%s",
				canonicalFirstState, canonicalSecondState,
			)
		}
		if !legacySecond || !adoptedFirst || !adoptedSecond {
			if err := recordCollisionBridgeHistory(ctx, db); err != nil {
				return false, err
			}
		}
		return collisionBridgeNeedsOutOfOrder(history), nil
	}
	if legacyFirstState == fingerprintAbsent && legacySecondState == fingerprintAbsent &&
		canonicalFirstState == fingerprintPresent && canonicalSecondState == fingerprintPresent {
		return false, nil
	}
	return false, collisionFingerprintError(legacyFirstState, legacySecondState, canonicalFirstState, canonicalSecondState)
}

func collisionFingerprintError(legacyFirst, legacySecond, canonicalFirst, canonicalSecond fingerprintState) error {
	return fmt.Errorf(
		"MIGRATION_VERSION_COLLISION_FINGERPRINT_AMBIGUOUS: legacy78=%s legacy79=%s canonical78=%s canonical79=%s",
		legacyFirst, legacySecond, canonicalFirst, canonicalSecond,
	)
}

func collisionBridgeNeedsOutOfOrder(history map[int64]bool) bool {
	return !history[legacyCollisionGapFirst] ||
		!history[legacyCollisionGapSecond] ||
		!history[legacyCollisionGapThird]
}

func appliedMigrationHistory(ctx context.Context, db *sql.DB, versions ...int64) (map[int64]bool, bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('public."+projectMigrationTable+"') IS NOT NULL").Scan(&exists); err != nil {
		return nil, false, fmt.Errorf("inspect project migration history for collision bridge: %w", err)
	}
	history := make(map[int64]bool, len(versions))
	if !exists {
		return history, false, nil
	}
	rows, err := db.QueryContext(ctx, "SELECT DISTINCT ON (version_id) version_id, is_applied FROM "+projectMigrationTable+" WHERE version_id = ANY($1) ORDER BY version_id, id DESC", versions)
	if err != nil {
		return nil, false, fmt.Errorf("read project migration collision history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int64
		var applied bool
		if err := rows.Scan(&version, &applied); err != nil {
			return nil, false, fmt.Errorf("scan project migration collision history: %w", err)
		}
		history[version] = applied
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate project migration collision history: %w", err)
	}
	return history, true, nil
}

func inspectSchemaFingerprint(ctx context.Context, db *sql.DB, fingerprint schemaFingerprint) (fingerprintState, error) {
	matched := 0
	for _, check := range fingerprint.checks {
		var present bool
		if err := db.QueryRowContext(ctx, check.query).Scan(&present); err != nil {
			return fingerprintPartial, fmt.Errorf("inspect %s fingerprint %s: %w", fingerprint.name, check.name, err)
		}
		if present {
			matched++
		}
	}
	switch {
	case matched == 0:
		return fingerprintAbsent, nil
	case matched == len(fingerprint.checks):
		return fingerprintPresent, nil
	default:
		return fingerprintPartial, nil
	}
}

func applyUnversionedMigrationSubset(ctx context.Context, db *sql.DB, projectFS fs.FS, versions ...int64) error {
	subset, err := newMigrationSubsetFS(projectFS, versions...)
	if err != nil {
		return fmt.Errorf("prepare canonical collision migrations: %w", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		subset,
		goose.WithTableName(projectMigrationTable),
		goose.WithDisableVersioning(true),
	)
	if err != nil {
		return fmt.Errorf("create canonical collision migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply canonical collision migrations: %w", err)
	}
	return nil
}

func recordCollisionBridgeHistory(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin adopted Eino migration history: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, version := range []int64{legacyCollisionSecondVersion, adoptedEinoFirstVersion, adoptedEinoSecondVersion} {
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+projectMigrationTable+" (version_id, is_applied, tstamp) SELECT $1, true, now() WHERE NOT EXISTS (SELECT 1 FROM "+projectMigrationTable+" WHERE version_id=$1 AND is_applied)", version); err != nil {
			return fmt.Errorf("record adopted Eino migration %d: %w", version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit adopted Eino migration history: %w", err)
	}
	return nil
}

type migrationSubsetFS struct {
	base     fs.FS
	versions map[int64]struct{}
}

func newMigrationSubsetFS(base fs.FS, versions ...int64) (fs.FS, error) {
	if base == nil {
		return nil, errors.New("migration filesystem is nil")
	}
	selected := make(map[int64]struct{}, len(versions))
	for _, version := range versions {
		if version <= 0 {
			return nil, fmt.Errorf("invalid migration subset version %d", version)
		}
		selected[version] = struct{}{}
	}
	entries, err := fs.ReadDir(base, ".")
	if err != nil {
		return nil, err
	}
	found := make(map[int64]struct{}, len(selected))
	for _, entry := range entries {
		if version, ok := parseMigrationFileVersion(entry.Name()); ok {
			if _, selected := selected[version]; selected {
				found[version] = struct{}{}
			}
		}
	}
	if len(found) != len(selected) {
		return nil, fmt.Errorf("migration subset is incomplete: requested=%d found=%d", len(selected), len(found))
	}
	return migrationSubsetFS{base: base, versions: selected}, nil
}

func (f migrationSubsetFS) Open(name string) (fs.File, error) {
	if name == "." {
		return f.base.Open(name)
	}
	version, ok := parseMigrationFileVersion(name)
	if !ok {
		return nil, fs.ErrNotExist
	}
	if _, selected := f.versions[version]; !selected {
		return nil, fs.ErrNotExist
	}
	return f.base.Open(name)
}

func (f migrationSubsetFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, fs.ErrNotExist
	}
	entries, err := fs.ReadDir(f.base, name)
	if err != nil {
		return nil, err
	}
	filtered := make([]fs.DirEntry, 0, len(f.versions))
	for _, entry := range entries {
		version, ok := parseMigrationFileVersion(entry.Name())
		if !ok {
			continue
		}
		if _, selected := f.versions[version]; selected {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

func parseMigrationFileVersion(name string) (int64, bool) {
	if !strings.HasSuffix(name, ".sql") {
		return 0, false
	}
	versionText, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, false
	}
	version, err := strconv.ParseInt(versionText, 10, 64)
	return version, err == nil && version > 0
}
