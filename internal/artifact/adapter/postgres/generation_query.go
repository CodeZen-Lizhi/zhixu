package postgres

import (
	"context"
	"errors"
	"fmt"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// ListSectionGenerations reads the current Revision and its refresh-recovery
// projection in one read-only repeatable-read snapshot.
func (repository *Repository) ListSectionGenerations(
	ctx context.Context,
	workspaceID, artifactID foundation.ID,
) (artifactapp.SectionGenerationSnapshot, error) {
	if repository == nil || repository.db == nil {
		return artifactapp.SectionGenerationSnapshot{}, unavailable(errors.New("artifact repository is unavailable"))
	}
	if ctx == nil || !validID(workspaceID) || !validID(artifactID) {
		return artifactapp.SectionGenerationSnapshot{}, requestInvalid(errors.New("artifact section generation query is invalid"))
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return artifactapp.SectionGenerationSnapshot{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	state, err := loadState(ctx, tx, workspaceID, artifactID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.SectionGenerationSnapshot{}, notFound(err)
	}
	if err != nil {
		return artifactapp.SectionGenerationSnapshot{}, err
	}
	keys, err := artifactapp.SectionGenerationProjectionKeys(state)
	if err != nil {
		return artifactapp.SectionGenerationSnapshot{}, inconsistent(fmt.Errorf("resolve artifact generation projection: %w", err))
	}
	items := make([]artifactapp.SectionGeneration, 0, len(keys))
	if len(keys) > 0 {
		items, err = loadAuthoritativeSectionGenerations(ctx, tx, workspaceID, artifactID, keys)
		if err != nil {
			return artifactapp.SectionGenerationSnapshot{}, err
		}
	}
	snapshot := artifactapp.SectionGenerationSnapshot{State: state, Items: items}
	if err := artifactapp.ValidateSectionGenerationSnapshot(snapshot, workspaceID, artifactID); err != nil {
		return artifactapp.SectionGenerationSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactapp.SectionGenerationSnapshot{}, classify(err)
	}
	return snapshot, nil
}

func loadAuthoritativeSectionGenerations(
	ctx context.Context,
	db interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	},
	workspaceID, artifactID foundation.ID,
	sectionKeys []string,
) ([]artifactapp.SectionGeneration, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT ON (section_key) `+sectionGenerationColumns+`
		FROM learning.artifact_section_generation
		WHERE workspace_id=$1
		  AND artifact_id=$2
		  AND section_key=ANY($3::text[])
		  AND status IN ('PENDING','FAILED','CANCELLED','RECOVERY_REQUIRED')
		ORDER BY section_key,
		         CASE WHEN status IN ('PENDING','RECOVERY_REQUIRED') THEN 0 ELSE 1 END,
		         source_artifact_version DESC,
		         updated_at DESC,
		         created_at DESC,
		         id DESC`,
		string(workspaceID), string(artifactID), sectionKeys)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()

	selected := make(map[string]artifactapp.SectionGeneration, len(sectionKeys))
	for rows.Next() {
		generation, scanErr := scanSectionGeneration(rows)
		if scanErr != nil {
			return nil, classify(scanErr)
		}
		if _, duplicate := selected[generation.SectionKey]; duplicate {
			return nil, inconsistent(errors.New("artifact generation projection contains duplicate sections"))
		}
		selected[generation.SectionKey] = generation
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	items := make([]artifactapp.SectionGeneration, 0, len(selected))
	for _, key := range sectionKeys {
		if generation, found := selected[key]; found {
			items = append(items, generation)
		}
	}
	return items, nil
}

var _ artifactapp.Repository = (*Repository)(nil)
