package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

var _ app.SynthesisKnowledgeBindingReader = (*GORMSynthesisStore)(nil)

func (store *GORMSynthesisStore) ReadSynthesisKnowledgeBinding(ctx context.Context, query app.SynthesisKnowledgeBindingQuery) (foundation.ID, error) {
	ref := query.Reference
	if err := store.ready(ctx, ref.Source.WorkspaceID, query.NoteID, query.RevisionID); err != nil {
		return "", err
	}
	if ref.Validate() != nil {
		return "", invalid(errors.New("synthesis source reference is invalid"))
	}
	var row struct{ ProfileRevisionID *string }
	result := store.database.WithContext(ctx).Raw(`SELECT profile_revision_id::text
		FROM organizing.synthesis_revision_source
		WHERE workspace_id=? AND note_id=? AND revision_id=? AND source_id=?
		AND source_version_id=? AND content_artifact_id=? AND parse_projection_id=?
		AND source_span_id=? AND content_hash=? AND excerpt_hash=? AND title=?`,
		string(ref.Source.WorkspaceID), string(query.NoteID), string(query.RevisionID), string(ref.Source.SourceID),
		string(ref.Source.SourceVersionID), string(ref.Source.ContentArtifactID), string(ref.Source.ParseProjectionID),
		string(ref.SourceSpanID), ref.Source.ContentHash, ref.ExcerptHash, ref.Title).Scan(&row)
	if result.Error != nil {
		return "", synthesisDBError(ctx, result.Error)
	}
	if result.RowsAffected != 1 {
		return "", synthesisNotFound()
	}
	if row.ProfileRevisionID == nil {
		return "", nil
	}
	id, err := foundation.ParseID(*row.ProfileRevisionID)
	if err != nil {
		return "", synthesisConsistency("synthesis profile snapshot identity is invalid")
	}
	return id, nil
}
