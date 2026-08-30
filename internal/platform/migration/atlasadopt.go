package migration

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"time"

	"ariga.io/atlas/sql/migrate"
)

// adoptOperatorVersion marks revisions written by legacy-history adoption
// instead of a live Atlas execution.
const adoptOperatorVersion = "zhixu-atlas-adopt/1"

const conflictingHistoryError = "MIGRATION_ADOPTION_CONFLICTING_HISTORY"

// adoptLegacyHistory detects pre-Atlas migration state (Goose history or the
// legacy shell-runner schema metadata) and records equivalent Atlas revisions
// without replaying already-applied DDL. It returns whether pending execution
// must tolerate the non-linear 80-82 gap left by the Eino collision adoption.
// Databases whose state cannot be recognized are rejected with a stable error
// instead of being guessed.
func (r *AtlasRunner) adoptLegacyHistory(ctx context.Context, db *sql.DB) (bool, error) {
	revisions, err := newAtlasRevisionStore(db).ReadRevisions(ctx)
	if err != nil {
		return false, fmt.Errorf("read atlas revisions before adoption: %w", err)
	}
	if len(revisions) > 0 {
		applied, gooseExists, err := readGooseAppliedVersions(ctx, db)
		if err != nil {
			return false, err
		}
		if gooseExists {
			if len(applied) == 0 {
				return false, fmt.Errorf(
					"%s: Goose history table exists alongside Atlas revisions but has no applied history",
					conflictingHistoryError,
				)
			}
			if err := validateAtlasGooseTransition(revisions, applied); err != nil {
				return false, err
			}
		}
		return atlasRevisionsRequireNonLinearExecution(revisions), nil
	}
	applied, gooseExists, err := readGooseAppliedVersions(ctx, db)
	if err != nil {
		return false, err
	}
	if !gooseExists {
		return false, r.adoptShellRunnerHistory(ctx, db)
	}
	if len(applied) == 0 {
		var legacyMetaExists bool
		if err := db.QueryRowContext(ctx, "SELECT to_regclass('core.schema_meta') IS NOT NULL").Scan(&legacyMetaExists); err != nil {
			return false, fmt.Errorf("inspect legacy schema metadata: %w", err)
		}
		if legacyMetaExists {
			return false, r.adoptShellRunnerHistory(ctx, db)
		}
		// An empty Goose history table carries no information; dropping it here
		// keeps the Atlas executor's clean-database check satisfied. The final
		// 00092 migration drops the table again for fully adopted databases.
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS public."+projectMigrationTable); err != nil {
			return false, fmt.Errorf("drop empty goose history table: %w", err)
		}
		return false, nil
	}
	return r.adoptGooseHistory(ctx, db, applied)
}

// atlasRevisionsRequireNonLinearExecution preserves collision recovery across
// process restarts. Legacy Eino adoption records 83/84 before the pending
// 80-82 gap is executed; a crash in that window must resume with non-linear
// ordering instead of rejecting the lower pending versions.
func atlasRevisionsRequireNonLinearExecution(revisions []*migrate.Revision) bool {
	applied := make(map[string]struct{}, len(revisions))
	for _, revision := range revisions {
		if revision != nil {
			applied[revision.Version] = struct{}{}
		}
	}
	_, firstEinoApplied := applied[fmt.Sprintf("%05d", adoptedEinoFirstVersion)]
	_, secondEinoApplied := applied[fmt.Sprintf("%05d", adoptedEinoSecondVersion)]
	if !firstEinoApplied && !secondEinoApplied {
		return false
	}
	for _, version := range []int64{legacyCollisionGapFirst, legacyCollisionGapSecond, legacyCollisionGapThird} {
		if _, ok := applied[fmt.Sprintf("%05d", version)]; !ok {
			return true
		}
	}
	return false
}

// readGooseAppliedVersions returns the set of applied positive Goose versions.
// The second return value reports whether the Goose history table exists.
func readGooseAppliedVersions(ctx context.Context, db *sql.DB) (map[int64]bool, bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('public."+projectMigrationTable+"') IS NOT NULL").Scan(&exists); err != nil {
		return nil, false, fmt.Errorf("inspect project migration history: %w", err)
	}
	if !exists {
		return nil, false, nil
	}
	rows, err := db.QueryContext(ctx, "SELECT DISTINCT ON (version_id) version_id, is_applied FROM public."+projectMigrationTable+" WHERE version_id > 0 ORDER BY version_id, id DESC")
	if err != nil {
		return nil, false, fmt.Errorf("read project migration history: %w", err)
	}
	defer rows.Close()
	applied := make(map[int64]bool)
	for rows.Next() {
		var version int64
		var isApplied bool
		if err := rows.Scan(&version, &isApplied); err != nil {
			return nil, false, fmt.Errorf("scan project migration history: %w", err)
		}
		applied[version] = isApplied
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate project migration history: %w", err)
	}
	for version, isApplied := range applied {
		if !isApplied {
			delete(applied, version)
		}
	}
	return applied, true, nil
}

// validateAtlasGooseTransition accepts only the recoverable state created when
// adoption committed Atlas revisions but 00092 has not yet removed the legacy
// history table. A separately created or manually altered dual history is
// ambiguous and must not be silently ignored.
func validateAtlasGooseTransition(revisions []*migrate.Revision, applied map[int64]bool) error {
	byVersion := make(map[int64]*migrate.Revision, len(revisions))
	hasAdoptedRevision := false
	for _, revision := range revisions {
		if revision == nil {
			return fmt.Errorf("%s: Atlas revision list contains a nil revision", conflictingHistoryError)
		}
		version, ok := parseAtlasRevisionVersion(revision.Version)
		if !ok || version > legacyGooseMaxVersion {
			return fmt.Errorf(
				"%s: Atlas revision %q is not part of the pre-Atlas transition",
				conflictingHistoryError,
				revision.Version,
			)
		}
		if _, duplicate := byVersion[version]; duplicate {
			return fmt.Errorf("%s: duplicate Atlas revision version %d", conflictingHistoryError, version)
		}
		if revision.OperatorVersion != adoptOperatorVersion && revision.OperatorVersion != atlasOperatorVersion {
			return fmt.Errorf(
				"%s: Atlas revision %q was written by unrecognized operator %q",
				conflictingHistoryError,
				revision.Version,
				revision.OperatorVersion,
			)
		}
		byVersion[version] = revision
		if revision.OperatorVersion == adoptOperatorVersion {
			hasAdoptedRevision = true
		}
	}
	if !hasAdoptedRevision {
		return fmt.Errorf(
			"%s: Goose history exists alongside Atlas revisions without an adoption marker",
			conflictingHistoryError,
		)
	}
	for version := range applied {
		revision, ok := byVersion[version]
		if !ok {
			return fmt.Errorf(
				"%s: Goose applied version %d has no Atlas revision",
				conflictingHistoryError,
				version,
			)
		}
		if revision.OperatorVersion != adoptOperatorVersion {
			return fmt.Errorf(
				"%s: Goose applied version %d is recorded by operator %q, not adoption",
				conflictingHistoryError,
				version,
				revision.OperatorVersion,
			)
		}
		if revision.Applied != revision.Total || revision.Error != "" {
			return fmt.Errorf(
				"%s: Atlas revision for Goose version %d is incomplete",
				conflictingHistoryError,
				version,
			)
		}
	}
	return nil
}

func parseAtlasRevisionVersion(version string) (int64, bool) {
	parsed, err := strconv.ParseInt(version, 10, 64)
	return parsed, err == nil && parsed > 0
}

// adoptGooseHistory repairs the Eino version collision when present, then
// writes Atlas revisions for the verified applied Goose versions.
func (r *AtlasRunner) adoptGooseHistory(ctx context.Context, db *sql.DB, applied map[int64]bool) (bool, error) {
	files, err := atlasFilesByVersion(r.dir)
	if err != nil {
		return false, fmt.Errorf("index atlas migrations for Goose adoption: %w", err)
	}
	if err := validateGooseAppliedVersions(applied, files); err != nil {
		return false, err
	}
	needsOutOfOrder, err := r.bridgeAtlasCollision(ctx, db, applied)
	if err != nil {
		return false, err
	}
	// Collision repair may add the adopted 83/84 versions to the set.
	if err := validateGooseAppliedVersions(applied, files); err != nil {
		return false, err
	}
	versions := make([]int64, 0, len(applied))
	for version := range applied {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	checksums, err := r.dir.Checksum()
	if err != nil {
		return false, fmt.Errorf("compute atlas directory checksum for adoption: %w", err)
	}
	adoptedFiles := make([]migrate.File, 0, len(versions))
	for _, version := range versions {
		adoptedFiles = append(adoptedFiles, files[version])
	}
	if err := writeAdoptedRevisions(ctx, db, checksums, adoptedFiles); err != nil {
		return false, err
	}
	return needsOutOfOrder, nil
}

func validateGooseAppliedVersions(applied map[int64]bool, files map[int64]migrate.File) error {
	for version := range applied {
		if version > legacyGooseMaxVersion {
			return fmt.Errorf(
				"MIGRATION_ADOPTION_UNKNOWN_VERSION: goose history applied version %d is outside legacy range 1..%d",
				version,
				legacyGooseMaxVersion,
			)
		}
		if _, ok := files[version]; !ok {
			return fmt.Errorf("MIGRATION_ADOPTION_UNKNOWN_VERSION: goose history applied version %d has no atlas migration file", version)
		}
	}
	return nil
}

// adoptShellRunnerHistory ports the legacy shell-runner adoption: a database
// without Goose history is only adopted when the full core.schema_meta fact
// set matches migrations 00001-00010.
func (r *AtlasRunner) adoptShellRunnerHistory(ctx context.Context, db *sql.DB) error {
	var legacyMetaExists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('core.schema_meta') IS NOT NULL").Scan(&legacyMetaExists); err != nil {
		return fmt.Errorf("inspect legacy schema metadata: %w", err)
	}
	if !legacyMetaExists {
		return nil
	}
	expected := map[string]string{
		"foundation":           "m1",
		"workspace_sources":    "m3",
		"workflow":             "m4",
		"change_control":       "m5.1",
		"content_artifact":     "m5-ingestion",
		"ingestion_projection": "m5-parser",
		"write_authorization":  "m5-03",
		"safe_writeback":       "m5-04d",
		"safe_writeback_saga":  "m5-04d",
	}
	rows, err := db.QueryContext(ctx, "SELECT key, value FROM core.schema_meta WHERE key = ANY($1)", legacyMetaKeys(expected))
	if err != nil {
		return fmt.Errorf("read legacy schema metadata: %w", err)
	}
	defer rows.Close()
	found := make(map[string]string, len(expected))
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return fmt.Errorf("scan legacy schema metadata: %w", err)
		}
		found[key] = value
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy schema metadata: %w", err)
	}
	for key, value := range expected {
		if found[key] != value {
			return fmt.Errorf("WORKFLOW_LEGACY_MIGRATION_MISMATCH: schema metadata %s=%q, want %q", key, found[key], value)
		}
	}
	files, err := atlasFilesByVersion(r.dir)
	if err != nil {
		return fmt.Errorf("index atlas migrations for shell-runner adoption: %w", err)
	}
	checksums, err := r.dir.Checksum()
	if err != nil {
		return fmt.Errorf("compute atlas directory checksum for adoption: %w", err)
	}
	adoptedFiles := make([]migrate.File, 0, legacyMigrationMaxVersion)
	for version := int64(1); version <= legacyMigrationMaxVersion; version++ {
		file, ok := files[version]
		if !ok {
			return fmt.Errorf("MIGRATION_ADOPTION_UNKNOWN_VERSION: legacy shell-runner version %d has no atlas migration file", version)
		}
		adoptedFiles = append(adoptedFiles, file)
	}
	return writeAdoptedRevisions(ctx, db, checksums, adoptedFiles)
}

// bridgeAtlasCollision ports the Eino 78/79 vs 83/84 collision repair: schema
// repairs apply the missing canonical or legacy file content transactionally,
// and the applied version set is adjusted to the adopted numbering.
func (r *AtlasRunner) bridgeAtlasCollision(ctx context.Context, db *sql.DB, applied map[int64]bool) (bool, error) {
	legacyFirst := applied[legacyCollisionFirstVersion]
	legacySecond := applied[legacyCollisionSecondVersion]
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
			if err := r.applyUnversionedAtlasFile(ctx, db, adoptedEinoSecondVersion); err != nil {
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

	adoptedFirst := applied[adoptedEinoFirstVersion]
	adoptedSecond := applied[adoptedEinoSecondVersion]
	if legacyFirstState == fingerprintPresent && legacySecondState == fingerprintAbsent &&
		canonicalFirstState == fingerprintPresent && canonicalSecondState == fingerprintPresent &&
		adoptedFirst && !adoptedSecond {
		// Canonical history may stop cleanly between the current 83 and 84.
		return false, nil
	}

	if legacyFirstState == fingerprintPresent && legacySecondState == fingerprintPresent {
		if canonicalFirstState == fingerprintPartial || canonicalSecondState == fingerprintPartial {
			return false, collisionFingerprintError(legacyFirstState, legacySecondState, canonicalFirstState, canonicalSecondState)
		}
		if canonicalFirstState == fingerprintAbsent {
			if err := r.applyUnversionedAtlasFile(ctx, db, legacyCollisionFirstVersion); err != nil {
				return false, err
			}
		}
		if canonicalSecondState == fingerprintAbsent {
			if err := r.applyUnversionedAtlasFile(ctx, db, legacyCollisionSecondVersion); err != nil {
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
		// Record the adopted numbering: 79 plus the renumbered Eino 83/84.
		applied[legacyCollisionSecondVersion] = true
		applied[adoptedEinoFirstVersion] = true
		applied[adoptedEinoSecondVersion] = true
		return !applied[legacyCollisionGapFirst] || !applied[legacyCollisionGapSecond] || !applied[legacyCollisionGapThird], nil
	}
	if legacyFirstState == fingerprintAbsent && legacySecondState == fingerprintAbsent &&
		canonicalFirstState == fingerprintPresent && canonicalSecondState == fingerprintPresent {
		return false, nil
	}
	return false, collisionFingerprintError(legacyFirstState, legacySecondState, canonicalFirstState, canonicalSecondState)
}

// applyUnversionedAtlasFile executes one migration file's statements without
// recording an Atlas revision, mirroring the collision bridge's historical
// unversioned repair. The file executes atomically unless it declares
// atlas:txmode none.
func (r *AtlasRunner) applyUnversionedAtlasFile(ctx context.Context, db *sql.DB, version int64) error {
	files, err := atlasFilesByVersion(r.dir)
	if err != nil {
		return fmt.Errorf("index atlas migrations for collision repair: %w", err)
	}
	file, ok := files[version]
	if !ok {
		return fmt.Errorf("MIGRATION_VERSION_COLLISION_REPAIR_MISSING: no atlas migration file for version %d", version)
	}
	stmts, err := file.StmtDecls()
	if err != nil {
		return fmt.Errorf("scan statements of %s for collision repair: %w", file.Name(), err)
	}
	if atlasFileTxModeNone(file) {
		for _, stmt := range stmts {
			if _, err := db.ExecContext(ctx, stmt.Text); err != nil {
				return fmt.Errorf("apply unversioned repair %s: %w", file.Name(), err)
			}
		}
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin unversioned repair %s: %w", file.Name(), err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt.Text); err != nil {
			return fmt.Errorf("apply unversioned repair %s: %w", file.Name(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit unversioned repair %s: %w", file.Name(), err)
	}
	return nil
}

// atlasFilesByVersion indexes migration files by their numeric version prefix.
func atlasFilesByVersion(dir *migrate.MemDir) (map[int64]migrate.File, error) {
	if dir == nil {
		return nil, fmt.Errorf("atlas migration directory is nil")
	}
	files, err := dir.Files()
	if err != nil {
		return nil, fmt.Errorf("read atlas migration directory: %w", err)
	}
	byVersion := make(map[int64]migrate.File, len(files))
	for _, file := range files {
		if version, ok := parseMigrationFileVersion(file.Name()); ok {
			if existing, duplicate := byVersion[version]; duplicate {
				return nil, fmt.Errorf("duplicate atlas migration version %d: %s and %s", version, existing.Name(), file.Name())
			}
			byVersion[version] = file
		}
	}
	return byVersion, nil
}

// writeAdoptedRevision records an already-applied migration as a complete Atlas
// revision. The hash comes from the migration directory checksum so later
// integrity validation behaves exactly as for executor-written revisions.
func writeAdoptedRevision(ctx context.Context, store *atlasRevisionStore, checksums migrate.HashFile, file migrate.File) error {
	stmts, err := file.StmtDecls()
	if err != nil {
		return fmt.Errorf("scan statements of %s for adoption: %w", file.Name(), err)
	}
	hash, err := checksums.SumByName(file.Name())
	if err != nil {
		return fmt.Errorf("resolve checksum of %s for adoption: %w", file.Name(), err)
	}
	revision := &migrate.Revision{
		Version:         file.Version(),
		Description:     file.Desc(),
		Type:            migrate.RevisionTypeExecute,
		Applied:         len(stmts),
		Total:           len(stmts),
		ExecutedAt:      time.Now(),
		Hash:            hash,
		OperatorVersion: adoptOperatorVersion,
	}
	if err := store.WriteRevision(ctx, revision); err != nil {
		return fmt.Errorf("adopt migration history %s: %w", file.Name(), err)
	}
	return nil
}

// writeAdoptedRevisions records one legacy history as an atomic batch. A
// process interruption can therefore leave either no adopted revisions or the
// complete verified set, never a prefix that would cause DDL to be replayed.
func writeAdoptedRevisions(ctx context.Context, db *sql.DB, checksums migrate.HashFile, files []migrate.File) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin atlas history adoption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	store := newAtlasRevisionStore(tx)
	for _, file := range files {
		if err := writeAdoptedRevision(ctx, store, checksums, file); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit atlas history adoption: %w", err)
	}
	return nil
}
