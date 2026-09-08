package migration

import (
	"context"
	"database/sql"
	"fmt"

	"ariga.io/atlas/sql/migrate"
)

const (
	proposalRevisionMigrationName     = "00082_proposal_revision_three_way_merge.sql"
	proposalRevisionCompatibilityName = "00093_proposal_revision_backfill_compatibility.sql"
)

// proposalRevisionBackfillCompatibility selects the forward compatibility SQL
// needed before the published 00082 can backfill a database containing Proposals.
// Neither the historical file nor its Atlas statement hashes are rewritten.
func proposalRevisionBackfillCompatibility(dir *migrate.MemDir, file migrate.File) (migrate.File, error) {
	if file.Name() != proposalRevisionMigrationName {
		return nil, nil
	}
	if atlasFileTxModeNone(file) {
		return nil, fmt.Errorf("MIGRATION_PROPOSAL_REVISION_BACKFILL_REQUIRES_TRANSACTION: %s", file.Name())
	}
	files, err := atlasFilesByVersion(dir)
	if err != nil {
		return nil, fmt.Errorf("index proposal revision compatibility migration: %w", err)
	}
	compatibility, ok := files[93]
	if !ok || compatibility.Name() != proposalRevisionCompatibilityName || atlasFileTxModeNone(compatibility) {
		return nil, fmt.Errorf("MIGRATION_PROPOSAL_REVISION_BACKFILL_COMPATIBILITY_MISSING: %s", proposalRevisionCompatibilityName)
	}
	return compatibility, nil
}

// executeProposalRevisionCompatibility runs inside 00082's transaction, both
// before and after Atlas executes the unchanged historical file. Like the Eino
// collision bridge, this does not record a prematurely applied forward version;
// 00093 is recorded normally when the executor reaches it in version order.
func executeProposalRevisionCompatibility(ctx context.Context, tx *sql.Tx, file migrate.File) error {
	stmts, err := file.StmtDecls()
	if err != nil {
		return fmt.Errorf("scan proposal revision compatibility SQL: %w", err)
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt.Text); err != nil {
			return fmt.Errorf("apply proposal revision compatibility SQL: %w", err)
		}
	}
	return nil
}
